//go:build corpus

package rename_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/rename"
	"github.com/mmcdole/dgdtools/pkg/resolve"
	"github.com/mmcdole/dgdtools/pkg/token"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// TestCorpusRename renames symbols of a real lib ($DGDTOOLS_CONFIG, with
// an absolute root) and lints the result: in a sample of its .c files
// ($LPC_SAMPLE of them, 400 when not set), every symbol a name of the
// file's compiled code names is asked for (Prepare), and up to five of
// those that can be renamed in each file, of every kind there is, are
// renamed to a name of the same length that is nowhere in the file.
//
// Invariants: the edits replace the old name and nothing else; the file
// renamed has the diagnostics it had, but for the name in their
// messages; the references of the new name are those of the old one,
// but for the places the plan excludes (the strings of calls DGD makes
// by name, which cannot reach a private function). How many symbols of
// each kind were renamed, and how many were refused for each reason, is
// logged, with the time a plan takes.
func TestCorpusRename(t *testing.T) {
	cfgPath := os.Getenv("DGDTOOLS_CONFIG")
	if cfgPath == "" {
		t.Skip("DGDTOOLS_CONFIG not set")
	}
	want := 400
	if n, err := strconv.Atoi(os.Getenv("LPC_SAMPLE")); err == nil {
		want = n
	}
	ctx := context.Background()
	over := vfs.NewOverlay(vfs.OS{})
	prj, err := config.Discover(over, nil, config.DiscoverOptions{ConfigPath: cfgPath})
	require.NoError(t, err)
	require.NotEmpty(t, prj.Root, "the configuration needs the whole lib")
	l, err := lint.New(prj, over)
	require.NoError(t, err)
	began := time.Now()
	require.True(t, l.Scan(ctx))
	t.Logf("scan: %s", time.Since(began).Round(time.Millisecond))

	units, complete := l.Index().Snapshot().UseUnits(ctx)
	require.True(t, complete)
	var files []string
	for _, u := range units {
		if filepath.Ext(u.Path) == ".c" {
			files = append(files, u.Path)
		}
	}
	slices.Sort(files)
	// Every so many files of the lib, and as many again of those that
	// declare a private function or variable, which few files do.
	var sample []string
	for i := 0; i < len(files); i += max(1, len(files)/want) {
		sample = append(sample, files[i])
	}
	var privates []string
	for _, u := range units {
		if filepath.Ext(u.Path) == ".c" && (slices.ContainsFunc(u.Funcs, func(f index.FuncInfo) bool { return f.Private && f.Path == u.Path }) ||
			slices.ContainsFunc(u.Vars, func(v index.VarInfo) bool { return v.Private && v.Path == u.Path })) {
			privates = append(privates, u.Path)
		}
	}
	slices.Sort(privates)
	for i := 0; i < len(privates); i += max(1, len(privates)/want) {
		if !slices.Contains(sample, privates[i]) {
			sample = append(sample, privates[i])
		}
	}
	t.Logf("%d files of %d, %d of them of the %d that declare something private", len(sample), len(files), len(sample)-min(want, len(files)), len(privates))

	renamed := map[resolve.Kind]int{}
	refused := map[string]int{}
	var asked, planned, warned int
	var planTime time.Duration
	for _, path := range sample {
		doc, err := l.Lint(ctx, path, lint.Options{})
		if err != nil {
			continue
		}
		src := string(doc.File.Src)
		seen := map[string]bool{}
		var cands []cand
		for _, ti := range doc.Tree.Sel.Sig {
			tok := doc.File.Tokens[ti]
			if tok.Kind != token.Ident {
				continue
			}
			ref := doc.Resolve(tok.Off)
			if ref == nil || len(ref.Defs) == 0 {
				continue
			}
			key := fmt.Sprintf("%s %s %d", ref.Kind, ref.Defs[0].Path, ref.Defs[0].Start)
			if seen[key] {
				continue
			}
			seen[key] = true
			asked++
			tg, err := doc.PrepareRename(ctx, tok.Off)
			var no *rename.Refusal
			switch {
			case errors.As(err, &no):
				refused[reasonClass(no.Reason)]++
			case err != nil:
				t.Fatalf("%s: %v", path, err)
			case tg != nil:
				cands = append(cands, cand{tok.Off, tg})
			}
		}
		// The first of each kind, then others, five at most.
		first := map[resolve.Kind]bool{}
		rank := map[uint32]int{}
		for _, c := range cands {
			if !first[c.t.Kind] {
				first[c.t.Kind] = true
			} else {
				rank[c.off] = 1
			}
		}
		slices.SortStableFunc(cands, func(a, b cand) int { return rank[a.off] - rank[b.off] })
		for _, c := range cands[:min(5, len(cands))] {
			fresh := freshName(src, c.t.Name)
			if fresh == "" {
				continue
			}
			// A rename checked before moved the index on.
			doc, err := l.Lint(ctx, path, lint.Options{})
			require.NoError(t, err)
			began := time.Now()
			plan, err := doc.Rename(ctx, c.off, fresh)
			planTime += time.Since(began)
			planned++
			var no *rename.Refusal
			if errors.As(err, &no) {
				refused["(rename) "+reasonClass(no.Reason)]++
				continue
			}
			require.NoError(t, err)
			renamed[c.t.Kind]++
			warned += len(plan.Warnings)
			checkRename(t, l, over, doc, plan, c.t)
		}
	}
	t.Logf("%d symbols asked about, %d plans (%s each on average); %d strings left that name a private function DGD cannot call by name", asked, planned,
		(planTime / time.Duration(max(1, planned))).Round(time.Microsecond), warned)
	for _, k := range slices.Sorted(maps.Keys(renamed)) {
		t.Logf("renamed: %-9s %d", k, renamed[k])
	}
	reasons := slices.Collect(maps.Keys(refused))
	slices.SortFunc(reasons, func(a, b string) int { return refused[b] - refused[a] })
	for _, r := range reasons {
		t.Logf("refused: %6d %s", refused[r], r)
	}
	assert.NotEmpty(t, renamed[resolve.Local])
	assert.NotEmpty(t, renamed[resolve.Function])
}

// cand is a symbol that can be renamed, and a place that names it.
type cand struct {
	off uint32
	t   *rename.Target
}

var quoted = regexp.MustCompile(`'[^']*'`)

// reasonClass is a refusal's reason without its names and lines.
func reasonClass(r string) string {
	r = quoted.ReplaceAllString(r, "'…'")
	r = regexp.MustCompile(`line \d+`).ReplaceAllString(r, "line N")
	r = regexp.MustCompile(`(/[\w.-]+)+`).ReplaceAllString(r, "PATH")
	r = regexp.MustCompile(`macro [A-Za-z_]\w*`).ReplaceAllString(r, "macro M")
	return r
}

// freshName returns a name of old's length that is nowhere in src.
func freshName(src, old string) string {
	for _, c := range "QZJXqzjx" {
		n := string(c) + old[1:]
		if n != old && !strings.Contains(src, n) {
			return n
		}
	}
	return ""
}

// checkRename applies a plan to its file, lints the result, and holds it
// to what the file was.
func checkRename(t *testing.T, l *lint.Linter, over *vfs.Overlay, doc *lint.Document, plan *rename.Plan, tg *rename.Target) {
	t.Helper()
	ctx := context.Background()
	path, src := doc.Path, string(doc.File.Src)
	old, fresh := plan.Symbol.Name, plan.New
	out := src
	for i := len(plan.Edits) - 1; i >= 0; i-- {
		e := plan.Edits[i]
		require.Equal(t, path, e.Path)
		require.Equal(t, old, src[e.Start:e.End], "%s: an edit replaces the old name", path)
		out = out[:e.Start] + e.NewText + out[e.End:]
	}
	declOff := tg.Start
	refsBefore := doc.References(ctx, declOff, true)
	require.NotNil(t, refsBefore, "%s: references of %s", path, old)
	excluded := map[uint32]bool{}
	for _, o := range plan.Occurrences {
		if o.Class == rename.Excluded {
			excluded[o.Start] = true
		}
	}
	var before []string
	for _, s := range refsBefore.Sites {
		if s.Path != path || !excluded[s.Start] {
			before = append(before, fmt.Sprintf("%s:%d-%d", s.Path, s.Start, s.End))
		}
	}

	over.Set(path, []byte(out))
	l.Invalidate(path)
	defer func() {
		over.Delete(path)
		l.Invalidate(path)
	}()
	after, err := l.Lint(ctx, path, lint.Options{})
	require.NoError(t, err)
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(fresh) + `\b`)
	// A string the plan leaves, naming a private function DGD cannot
	// call by name, names nothing after the rename: what the linter says
	// of it may change, and the call fails as it did.
	left := func(d diag.Diagnostic) bool {
		for _, o := range plan.Occurrences {
			if o.Class == rename.Excluded && d.Start <= o.Start && o.End <= d.End+1 {
				return true
			}
		}
		return false
	}
	norm := func(ds []diag.Diagnostic, rename bool) []string {
		var outs []string
		for _, d := range ds {
			if left(d) {
				continue
			}
			msg := d.Message
			if rename {
				msg = word.ReplaceAllString(msg, old)
			}
			outs = append(outs, fmt.Sprintf("%d:%d-%d:%d %s %s", d.Line, d.Col, d.EndLine, d.EndCol, d.Code, msg))
		}
		slices.Sort(outs)
		return outs
	}
	assert.Equal(t, norm(doc.Diagnostics, false), norm(after.Diagnostics, true), "%s: %s %s to %s", path, tg.Kind, old, fresh)
	refsAfter := after.References(ctx, declOff, true)
	require.NotNil(t, refsAfter, "%s: references of %s", path, fresh)
	var got []string
	for _, s := range refsAfter.Sites {
		got = append(got, fmt.Sprintf("%s:%d-%d", s.Path, s.Start, s.End))
	}
	assert.Equal(t, before, got, "%s: the references of %s %s, renamed %s", path, tg.Kind, old, fresh)
}
