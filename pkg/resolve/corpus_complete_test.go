//go:build corpus

package resolve_test

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/mmcdole/dgdtools/pkg/calls"
	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/fileset"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/profilebuild"
	"github.com/mmcdole/dgdtools/pkg/resolve"
	"github.com/mmcdole/dgdtools/pkg/token"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// completions asks, in each of the files, at a sample of the places
// that name something (perFile of them a file), what a completion
// offers there once the name is taken out: the text an editor has when
// the name is about to be typed. l lints through texts, where the
// edited text is put.
//
// Invariants: nothing panics; what an answer replaces is at the cursor;
// a call on a receiver that is not known is offered nothing. The name
// that stood there is to be among the offers wherever the tools know it
// (the place resolves to a declaration, or to a kfun): the hits are
// counted, by the form of the place, and returned with the misses.
type completions struct {
	t       *testing.T
	l       *lint.Linter
	texts   *vfs.Overlay
	perFile int

	forms    map[string]*formCount
	misses   []string
	failures int
	took     time.Duration
	asked    int
	slowest  time.Duration
	items    []int
	capped   int
}

type formCount struct{ sites, known, hits, hitsUnknown, items int }

func (c *completions) file(ctx context.Context, path string) {
	doc, err := c.l.Lint(ctx, path, lint.Options{NoScan: true})
	if err != nil || doc.Tree.Sel == nil {
		return
	}
	src := doc.File.Src
	sig := doc.Tree.Sel.Sig
	type site struct {
		form       string
		name       string
		start, end uint32
		known, fn  bool
		unknown    bool // a receiver nobody knows
	}
	var sites []site
	for si, ti := range sig {
		tok := doc.File.Tokens[ti]
		if tok.Kind != token.Ident && tok.Kind != token.StringLit {
			continue
		}
		ref := doc.Resolve(tok.Off + (tok.End-tok.Off)/2)
		if ref == nil || ref.Decl {
			continue
		}
		s := site{name: ref.Name, start: ref.Start, end: ref.End, known: len(ref.Defs) > 0}
		prev := func(n int) token.Kind {
			if si-n < 0 {
				return token.EOF
			}
			return doc.File.Tokens[sig[si-n]].Kind
		}
		switch ref.Kind {
		case resolve.Function:
			s.fn = true
			switch {
			case tok.Kind == token.StringLit:
				s.form = "callback"
			case prev(1) == token.Arrow:
				s.form = "x->fn"
			case prev(1) == token.ColonColon && prev(2) == token.Ident:
				s.form = "label::fn"
			case prev(1) == token.ColonColon:
				s.form = "::fn"
			default:
				s.form = "fn()"
				if h := doc.Hover(tok.Off); !s.known && h != nil && len(h.Infos) > 0 && h.Infos[0].Kfun {
					s.known, s.form = true, "kfun()"
				}
			}
			if ref.Call != nil && ref.Call.Target == calls.TargetUnknown {
				s.unknown = true
				s.form += " (unknown receiver)"
			}
		case resolve.Variable:
			s.form = "global"
		case resolve.Local, resolve.Parameter:
			s.form = "local"
		case resolve.Macro:
			s.form, s.known = "macro", true
		default:
			continue
		}
		sites = append(sites, s)
	}
	if len(sites) == 0 {
		return
	}
	defer func() {
		c.texts.Delete(path)
		c.l.Invalidate(path)
	}()
	for i := 0; i < len(sites); i += max(len(sites)/c.perFile, 1) {
		s := sites[i]
		edited := slices.Concat(src[:s.start], src[s.end:])
		c.texts.Set(path, edited)
		c.l.Invalidate(path)
		doc2, err := c.l.Lint(ctx, path, lint.Options{NoScan: true})
		if err != nil {
			continue
		}
		start := time.Now()
		comp := doc2.Complete(s.start)
		d := time.Since(start)
		c.took += d
		c.asked++
		c.slowest = max(c.slowest, d)
		fc := c.forms[s.form]
		if fc == nil {
			fc = &formCount{}
			c.forms[s.form] = fc
		}
		fc.sites++
		hit := false
		if comp != nil {
			if comp.Start != s.start || comp.End < s.start || int(comp.End) > len(edited) {
				c.fail("%s:%d: a completion for %s replaces [%d,%d)", path, s.start, s.name, comp.Start, comp.End)
			}
			fc.items += len(comp.Items)
			c.items = append(c.items, len(comp.Items))
			if comp.Incomplete {
				c.capped++
			}
			for _, it := range comp.Items {
				if it.Name == s.name && (it.Kind == resolve.Function) == s.fn {
					hit = true
				}
			}
			if s.unknown && len(comp.Items) > 0 {
				c.fail("%s:%d: %d offers for %s on a receiver that is not known", path, s.start, len(comp.Items), s.name)
			}
		}
		switch {
		case s.known:
			fc.known++
			if hit {
				fc.hits++
			} else if len(c.misses) < 400 {
				ctx := "nil"
				if comp != nil {
					ctx = fmt.Sprint(comp.Context)
				}
				c.misses = append(c.misses, fmt.Sprintf("%s %s:%d %s (context %s)", s.form, path, doc.File.Pos(s.start).Line, s.name, ctx))
			}
		case hit:
			fc.hitsUnknown++
		}
	}
}

func (c *completions) fail(format string, args ...any) {
	if c.failures++; c.failures <= 20 {
		c.t.Errorf(format, args...)
	}
}

// report logs the counts and returns the hits and the places known.
func (c *completions) report(what string) (hits, known int) {
	t := c.t
	forms := make([]string, 0, len(c.forms))
	for f := range c.forms {
		forms = append(forms, f)
	}
	sort.Strings(forms)
	for _, f := range forms {
		fc := c.forms[f]
		rate := "    -"
		if fc.known > 0 {
			rate = fmt.Sprintf("%5.1f", 100*float64(fc.hits)/float64(fc.known))
		}
		t.Logf("  %-30s %6d places, %6d known, %6d offered (%s%%); offered where not known %4d; %5d items on average",
			f, fc.sites, fc.known, fc.hits, rate, fc.hitsUnknown, fc.items/max(fc.sites, 1))
		hits += fc.hits
		known += fc.known
	}
	sort.Ints(c.items)
	if n := len(c.items); n > 0 {
		t.Logf("%s: %d completions, %v each, the slowest %v; items: median %d, 95th percentile %d, most %d; cut at the cap %d",
			what, c.asked, c.took/time.Duration(max(c.asked, 1)), c.slowest, c.items[n/2], c.items[n*95/100], c.items[n-1], c.capped)
	}
	t.Logf("%s: the name was offered at %d of %d places where it is known (%.2f%%)", what, hits, known, 100*float64(hits)/float64(max(known, 1)))
	for i, m := range c.misses {
		if i < 25 {
			t.Logf("  not offered: %s", m)
		}
	}
	return hits, known
}

// TestCorpusComplete takes names out of the sample of TestCorpusResolve
// (twelve places a file) and asks for completions where they stood.
func TestCorpusComplete(t *testing.T) {
	prj, _, sample := corpusSample(t)
	texts := vfs.NewOverlay(vfs.OS{})
	l, err := lint.New(prj, texts)
	require.NoError(t, err)
	c := &completions{t: t, l: l, texts: texts, perFile: 12, forms: map[string]*formCount{}}
	ctx := context.Background()
	for _, file := range sample {
		c.file(ctx, file)
	}
	hits, known := c.report("the whole lib")
	assert.Zero(t, c.failures)
	assert.GreaterOrEqual(t, hits*1000, known*970, "a name the tools know is not offered where it stands")
}

// TestCorpusCompletePartial does the same in the trees of
// $LPC_CHECKOUTS, each copied alone to a temporary directory with a
// profile built from the lib, as TestCorpusResolvePartial has them.
func TestCorpusCompletePartial(t *testing.T) {
	cfgPath, trees := os.Getenv("DGDTOOLS_CONFIG"), os.Getenv("LPC_CHECKOUTS")
	if cfgPath == "" || trees == "" {
		t.Skip("DGDTOOLS_CONFIG and LPC_CHECKOUTS not both set")
	}
	ctx := context.Background()
	fsys := vfs.OS{}
	full, err := config.Discover(fsys, nil, config.DiscoverOptions{ConfigPath: cfgPath})
	require.NoError(t, err)
	built, err := profilebuild.Build(ctx, full, fsys)
	require.NoError(t, err)
	data, err := built.Marshal()
	require.NoError(t, err)
	tmp := t.TempDir()
	prof := filepath.Join(tmp, "lib.profile")
	require.NoError(t, os.WriteFile(prof, data, 0o644))
	total := &completions{t: t, perFile: 6, forms: map[string]*formCount{}}
	for _, tree := range strings.Split(trees, ",") {
		tree = strings.Trim(tree, "/ ")
		src := filepath.Join(full.Root, filepath.FromSlash(tree))
		dst := filepath.Join(tmp, "co", filepath.FromSlash(tree))
		require.NoError(t, filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(src, p)
			if d.IsDir() {
				return os.MkdirAll(filepath.Join(dst, rel), 0o755)
			}
			if !d.Type().IsRegular() {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
		}))
		y, _ := yaml.Marshal(map[string]any{"profile": prof, "exclude": full.Config.Exclude})
		require.NoError(t, os.WriteFile(filepath.Join(dst, "dgdtools.yml"), y, 0o644))
		texts := vfs.NewOverlay(fsys)
		part, err := config.Discover(texts, []string{dst}, config.DiscoverOptions{})
		require.NoError(t, err)
		total.l, err = lint.New(part, texts)
		require.NoError(t, err)
		total.texts = texts
		var files []string
		require.NoError(t, part.Tree().Walk(ctx, texts, dst, []string{".c"}, fileset.NewMatcher(part.Config.Exclude),
			func(p string) { files = append(files, p) }))
		slices.Sort(files)
		for i := 0; i < len(files); i += max(len(files)/150, 1) {
			total.file(ctx, files[i])
		}
	}
	hits, known := total.report("ten checkouts with the profile")
	assert.Zero(t, total.failures)
	assert.GreaterOrEqual(t, hits*1000, known*970, "a name the tools know is not offered where it stands")
}
