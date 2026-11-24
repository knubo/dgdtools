package lint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"sort"
	"sync"

	"github.com/mmcdole/dgdtools/pkg/diag"
)

// BaselineFileName is the default baseline file, next to the config.
const BaselineFileName = "dgdtools-baseline.json"

// Baseline is the set of known findings a project has agreed to live
// with. A lint subtracts them, so only new findings are reported.
//
// An entry identifies a finding by file, rule, and a fingerprint of the
// text of the source line it is on (diag.Diagnostic.Fingerprint) — not by
// line number — so edits elsewhere in the file do not resurrect it. Count
// allows several identical findings in one file.
type Baseline struct {
	Version int             `json:"version"`
	Entries []BaselineEntry `json:"entries"`

	// dir is what entry paths are relative to: the baseline file's
	// directory.
	dir    string
	counts map[baselineKey]int
}

type BaselineEntry struct {
	Path  string `json:"path"` // slash-separated, relative to the baseline file
	Rule  string `json:"rule"`
	Line  string `json:"fingerprint"`
	Count int    `json:"count"`
	// Message is informational: it is not used for matching.
	Message string `json:"message,omitempty"`
}

type baselineKey struct{ path, rule, line string }

// BaselinePath returns where the project's baseline lives: lint.baseline
// when that is an absolute path; else that name, or BaselineFileName,
// next to the project's config file. It is "" when the project has no
// config file of its own to anchor it (the user-level configuration is
// not one).
func (l *Linter) BaselinePath() string {
	cfg := l.project.Config
	name := cfg.Lint.Baseline
	switch {
	case filepath.IsAbs(name):
		return name
	case cfg.Path == "" || l.project.UserConfig:
		return ""
	case name == "":
		name = BaselineFileName
	}
	return filepath.Join(cfg.Dir, name)
}

// baselineFile is a baseline file as the linter read it.
type baselineFile struct {
	once sync.Once
	b    *Baseline
	err  error
}

// Baseline returns the baseline in the file at path (absolute), nil when
// there is no such file. A file is read once in the linter's life: a
// linter is rebuilt when a baseline changes, as when its configuration
// does.
func (l *Linter) Baseline(path string) (*Baseline, error) {
	if path == "" {
		return nil, nil
	}
	v, _ := l.baselines.LoadOrStore(path, &baselineFile{})
	f := v.(*baselineFile)
	f.once.Do(func() {
		if !l.fs.Exists(path) {
			return
		}
		data, err := l.fs.ReadFile(path)
		if err != nil {
			f.err = err
			return
		}
		b := &Baseline{dir: filepath.Dir(path), counts: map[baselineKey]int{}}
		if err := json.Unmarshal(data, b); err != nil {
			f.err = fmt.Errorf("%s: %w", path, err)
			return
		}
		for _, e := range b.Entries {
			b.counts[baselineKey{e.Path, e.Rule, e.Line}] += e.Count
		}
		f.b = b
	})
	return f.b, f.err
}

// baselineFor returns the baseline a lint with the given options
// subtracts, nil for none.
func (l *Linter) baselineFor(opt *Options) (*Baseline, error) {
	switch {
	case opt.NoBaseline:
		return nil, nil
	case opt.Baseline != "":
		return l.Baseline(opt.Baseline)
	}
	return l.Baseline(l.BaselinePath())
}

func (b *Baseline) key(d diag.Diagnostic) baselineKey {
	rel := d.Path
	if r, err := filepath.Rel(b.dir, d.Path); err == nil {
		rel = r
	}
	return baselineKey{filepath.ToSlash(rel), d.Rule, d.Fingerprint}
}

// lineFingerprint hashes the whitespace-trimmed text of the line holding
// off.
func lineFingerprint(src []byte, off uint32) string {
	if int(off) > len(src) {
		off = uint32(len(src))
	}
	a := bytes.LastIndexByte(src[:off], '\n') + 1
	b := bytes.IndexByte(src[off:], '\n')
	if b < 0 {
		b = len(src)
	} else {
		b += int(off)
	}
	h := fnv.New64a()
	h.Write(bytes.TrimSpace(src[a:b]))
	return fmt.Sprintf("%016x", h.Sum64())
}

// Filter removes baselined findings from ds, which must hold all the
// findings of the files it holds any of, and reports how many were
// removed. It does not change the baseline and is safe for concurrent
// use.
func (b *Baseline) Filter(ds []diag.Diagnostic) ([]diag.Diagnostic, int) {
	used := map[baselineKey]int{}
	out := ds[:0]
	removed := 0
	for _, d := range ds {
		k := b.key(d)
		if used[k] < b.counts[k] {
			used[k]++
			removed++
			continue
		}
		out = append(out, d)
	}
	return out, removed
}

// NewBaseline builds the baseline to be written to the file at path
// (absolute) from the findings of a run.
func NewBaseline(path string, ds []diag.Diagnostic) *Baseline {
	b := &Baseline{Version: 1, dir: filepath.Dir(path)}
	counts := map[baselineKey]*BaselineEntry{}
	for _, d := range ds {
		k := b.key(d)
		e := counts[k]
		if e == nil {
			e = &BaselineEntry{Path: k.path, Rule: k.rule, Line: k.line, Message: d.Message}
			counts[k] = e
		}
		e.Count++
	}
	for _, e := range counts {
		b.Entries = append(b.Entries, *e)
	}
	sort.Slice(b.Entries, func(i, j int) bool {
		x, y := b.Entries[i], b.Entries[j]
		if x.Path != y.Path {
			return x.Path < y.Path
		}
		if x.Rule != y.Rule {
			return x.Rule < y.Rule
		}
		return x.Line < y.Line
	})
	return b
}

// Marshal renders the baseline as stable, diff-friendly JSON.
func (b *Baseline) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", " ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(b); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
