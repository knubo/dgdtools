package lint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"sort"

	"github.com/mmcdole/dgdtools/pkg/diag"
)

// BaselineFileName is the default baseline file, next to the config.
const BaselineFileName = "dgdlint-baseline.json"

// Baseline is the set of known findings a project has agreed to live
// with. A run subtracts them, so only new findings are reported.
//
// An entry identifies a finding by file, rule, and a fingerprint of the
// text of the source line it is on — not by line number — so edits
// elsewhere in the file do not resurrect it. Count allows several
// identical findings in one file.
type Baseline struct {
	Version int             `json:"version"`
	Entries []BaselineEntry `json:"entries"`
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

// BaselinePath returns where the project's baseline lives, or "" when the
// project has no config file to anchor it.
func (l *Linter) BaselinePath() string {
	cfg := l.Project.Config
	if cfg.Path == "" {
		return ""
	}
	name := cfg.Lint.Baseline
	if name == "" {
		name = BaselineFileName
	}
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(cfg.Dir, name)
}

// LoadBaseline reads the project's baseline; nil when there is none.
func (l *Linter) LoadBaseline() (*Baseline, error) {
	p := l.BaselinePath()
	if p == "" || !l.FS.Exists(p) {
		return nil, nil
	}
	data, err := l.FS.ReadFile(p)
	if err != nil {
		return nil, err
	}
	b := &Baseline{}
	if err := json.Unmarshal(data, b); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return b, nil
}

func (l *Linter) baselineKey(d diag.Diagnostic, cache map[string][]byte) baselineKey {
	rel := d.Path
	if base := l.BaselinePath(); base != "" {
		if r, err := filepath.Rel(filepath.Dir(base), d.Path); err == nil {
			rel = r
		}
	}
	src, ok := cache[d.Path]
	if !ok {
		src, _ = l.FS.ReadFile(d.Path)
		cache[d.Path] = src
	}
	return baselineKey{filepath.ToSlash(rel), d.Rule, lineFingerprint(src, d.Start)}
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

// Filter removes baselined findings from ds and reports how many were
// removed.
func (b *Baseline) Filter(l *Linter, ds []diag.Diagnostic) ([]diag.Diagnostic, int) {
	left := make(map[baselineKey]int, len(b.Entries))
	for _, e := range b.Entries {
		left[baselineKey{e.Path, e.Rule, e.Line}] += e.Count
	}
	cache := map[string][]byte{}
	out := ds[:0]
	removed := 0
	for _, d := range ds {
		k := l.baselineKey(d, cache)
		if left[k] > 0 {
			left[k]--
			removed++
			continue
		}
		out = append(out, d)
	}
	return out, removed
}

// NewBaseline builds a baseline from the findings of a run.
func (l *Linter) NewBaseline(ds []diag.Diagnostic) *Baseline {
	counts := map[baselineKey]*BaselineEntry{}
	cache := map[string][]byte{}
	for _, d := range ds {
		k := l.baselineKey(d, cache)
		e := counts[k]
		if e == nil {
			e = &BaselineEntry{Path: k.path, Rule: k.rule, Line: k.line, Message: d.Message}
			counts[k] = e
		}
		e.Count++
	}
	b := &Baseline{Version: 1}
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
