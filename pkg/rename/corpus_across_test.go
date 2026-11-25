//go:build corpus

package rename_test

import (
	"cmp"
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
	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/rename"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// TestCorpusRenameAcross renames symbols of a real lib ($DGDTOOLS_CONFIG,
// with an absolute root) that other files name too, and lints the whole
// lib again with the certain edits made (in an overlay of the lib, which
// is not written): the functions called most, and every so many of the
// public functions, global variables and header macros the lib declares
// ($LPC_ACROSS of each kind, 8 when not set), each renamed to a name of
// the same length that the lib does not use. $LPC_ACROSS_ONLY keeps the
// symbols of the names it lists, comma-separated.
//
// Invariants: every edit replaces the old name; after the rename the
// diagnostics of every file of the lib are those it had, but for the new
// name in their messages and for the lines of the possible places, which
// were left; the references of the renamed declarations are the certain
// places, as before the rename they were. Logged: what was renamed, the
// counts of each class, the files, how long each plan took, the refusals.
func TestCorpusRenameAcross(t *testing.T) {
	cfgPath := os.Getenv("DGDTOOLS_CONFIG")
	if cfgPath == "" {
		t.Skip("DGDTOOLS_CONFIG not set")
	}
	each := 8
	if n, err := strconv.Atoi(os.Getenv("LPC_ACROSS")); err == nil {
		each = n
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

	began = time.Now()
	before := lintAll(t, l)
	t.Logf("lint of the lib: %d files with diagnostics, %s", len(before), time.Since(began).Round(time.Millisecond))

	targets := acrossTargets(t, l, each)
	if only := os.Getenv("LPC_ACROSS_ONLY"); only != "" {
		// Those of the names, comma-separated.
		targets = slices.DeleteFunc(targets, func(tg target) bool { return !slices.Contains(strings.Split(only, ","), tg.name) })
	}
	refused := map[string]int{}
	type done struct {
		what                        string
		certain, possible, excluded int
		files                       int
		took                        time.Duration
	}
	var renamed []done
	for _, tg := range targets {
		// The rename checked before moved the index on.
		require.True(t, l.Scan(ctx))
		doc, err := l.Lint(ctx, tg.path, lint.Options{})
		require.NoError(t, err)
		fresh := unusedName(l, tg.name)
		require.NotEmpty(t, fresh, tg.name)
		start := time.Now()
		plan, err := doc.Rename(ctx, tg.off, fresh, false)
		took := time.Since(start)
		var no *rename.Refusal
		if errors.As(err, &no) {
			refused[reasonClass(no.Reason)]++
			t.Logf("refused %s %s: %s", tg.kind, tg.name, no.Reason)
			continue
		}
		require.NoError(t, err)
		files := checkAcross(t, l, over, before, plan)
		renamed = append(renamed, done{fmt.Sprintf("%s %s (%s)", plan.Symbol.Kind, tg.name, l.Index().Snapshot().LibFile(plan.Symbol.Def.Path)),
			plan.Count(rename.Certain), plan.Count(rename.Possible), plan.Count(rename.Excluded), files, took})
	}
	slices.SortFunc(renamed, func(a, b done) int { return cmp.Compare(b.certain, a.certain) })
	for _, d := range renamed {
		t.Logf("renamed %-50s certain %6d  possible %5d  excluded %4d  files %5d  %s", d.what, d.certain, d.possible, d.excluded, d.files, d.took.Round(time.Millisecond))
	}
	for _, r := range slices.Sorted(maps.Keys(refused)) {
		t.Logf("refused: %3d %s", refused[r], r)
	}
	assert.NotEmpty(t, renamed)
}

// target is a symbol to rename: where it is declared.
type target struct {
	kind, name, path string
	off              uint32
}

// acrossTargets picks the symbols: the public functions called most, and
// every so many of the lib's public functions, global variables that are
// not private and macros of headers.
func acrossTargets(t *testing.T, l *lint.Linter, each int) []target {
	ctx := context.Background()
	snap := l.Index().Snapshot()
	units, complete := snap.UseUnits(ctx)
	require.True(t, complete)
	var funcs, vars, macros []target
	calls := map[string]int{}
	declared := map[string]target{}
	for _, u := range units {
		if filepath.Ext(u.Path) == ".h" {
			for _, use := range u.AllUses() {
				if use.Kind == index.UseMacro && use.Decl && use.Aux == u.Path {
					macros = append(macros, target{"macro", use.Name, u.Path, use.Start})
				}
			}
			continue
		}
		for _, use := range u.AllUses() {
			if use.Kind == index.UseFunction && use.Form == index.FormOwn && !use.Decl {
				calls[use.Name]++
			}
		}
		for _, f := range u.Funcs {
			if !f.Private && !f.PrototypeOnly && f.Path == u.Path {
				tg := target{"function", f.Name, f.Path, f.Off}
				funcs = append(funcs, tg)
				if _, ok := declared[f.Name]; !ok {
					declared[f.Name] = tg
				}
			}
		}
		for _, v := range u.Vars {
			if !v.Private && v.Path == u.Path {
				vars = append(vars, target{"variable", v.Name, v.Path, v.Off})
			}
		}
	}
	// The functions called most that the lib declares.
	names := slices.Collect(maps.Keys(calls))
	slices.SortFunc(names, func(a, b string) int { return cmp.Or(cmp.Compare(calls[b], calls[a]), strings.Compare(a, b)) })
	var out []target
	for _, n := range names {
		if tg, ok := declared[n]; ok && len(out) < each {
			out = append(out, tg)
		}
	}
	pick := func(ts []target) {
		slices.SortFunc(ts, func(a, b target) int { return cmp.Or(strings.Compare(a.path, b.path), cmp.Compare(a.off, b.off)) })
		for i := 0; i < len(ts) && each > 0; i += max(1, len(ts)/each) {
			out = append(out, ts[i])
		}
	}
	pick(funcs)
	pick(vars)
	pick(macros)
	return out
}

// unusedName returns a name of old's length that no file of the lib
// uses.
func unusedName(l *lint.Linter, old string) string {
	units, _ := l.Index().Snapshot().UseUnits(context.Background())
	for _, c := range "QZJXqzjx" {
		n := string(c) + old[1:]
		if n == old {
			continue
		}
		if !slices.ContainsFunc(units, func(u *index.Unit) bool { return len(u.Uses(n)) > 0 }) {
			return n
		}
	}
	return ""
}

// lintAll lints the whole lib and returns the diagnostics of each file.
func lintAll(t *testing.T, l *lint.Linter) map[string][]string {
	t.Helper()
	res, err := l.Run(context.Background(), lint.Request{Options: lint.Options{NoBaseline: true, MaxPerFile: -1}})
	require.NoError(t, err)
	out := map[string][]string{}
	for _, d := range res.Diagnostics {
		out[d.Path] = append(out[d.Path], fmt.Sprintf("%d:%d-%d:%d %s %s", d.Line, d.Col, d.EndLine, d.EndCol, d.Code, d.Message))
	}
	return out
}

// checkAcross makes the plan's certain edits in the overlay, lints the
// whole lib again and holds it to what it was, checks the references of
// the renamed declarations, and takes the edits back. It returns how
// many files were edited.
func checkAcross(t *testing.T, l *lint.Linter, over *vfs.Overlay, before map[string][]string, plan *rename.Plan) int {
	t.Helper()
	ctx := context.Background()
	snap := l.Index().Snapshot()
	old, fresh := plan.Symbol.Name, plan.New
	what := fmt.Sprintf("%s %s to %s", plan.Symbol.Kind, old, fresh)

	// The certain places, and the references before.
	certain := map[string]bool{}
	var decls []rename.Occurrence
	for _, o := range plan.Occurrences {
		if o.Class == rename.Certain {
			certain[fmt.Sprintf("%s:%d", o.Path, o.Start)] = true
			if o.Decl {
				decls = append(decls, o)
			}
		}
	}
	require.NotEmpty(t, decls, what)
	// A family of many redefinitions is a search of the lib for each: its
	// references are not compared.
	checkRefs := len(decls) <= 40
	refsOf := func() map[string]bool {
		got := map[string]bool{}
		for _, d := range decls {
			doc, err := l.Lint(ctx, d.Path, lint.Options{})
			require.NoError(t, err, d.Path)
			refs := doc.References(ctx, d.Start, true)
			require.NotNil(t, refs, "%s: %s at %d", what, d.Path, d.Start)
			for _, s := range refs.Sites {
				got[fmt.Sprintf("%s:%d", s.Path, s.Start)] = true
			}
		}
		return got
	}
	if checkRefs {
		assert.Equal(t, sortedKeys(certain), sortedKeys(refsOf()), "%s: the certain places are the references of the declarations", what)
	} else {
		t.Logf("%s: %d declarations, references not compared", what, len(decls))
	}

	byPath := map[string][]rename.Edit{}
	for _, e := range plan.Edits {
		byPath[e.Path] = append(byPath[e.Path], e)
	}
	possibleLines := map[string]map[int]bool{}
	for _, o := range plan.Occurrences {
		if o.Class == rename.Possible {
			if possibleLines[o.Path] == nil {
				possibleLines[o.Path] = map[int]bool{}
			}
			possibleLines[o.Path][int(o.Line)] = true
		}
	}
	var paths []string
	for p, es := range byPath {
		src, err := snap.ReadFile(p)
		require.NoError(t, err)
		slices.SortFunc(es, func(a, b rename.Edit) int { return cmp.Compare(a.Start, b.Start) })
		out := string(src)
		for i := len(es) - 1; i >= 0; i-- {
			e := es[i]
			require.Equal(t, old, out[e.Start:e.End], "%s: %s at %d", what, p, e.Start)
			out = out[:e.Start] + e.NewText + out[e.End:]
		}
		over.Set(p, []byte(out))
		paths = append(paths, p)
	}
	l.Invalidate(paths...)
	defer func() {
		for _, p := range paths {
			over.Delete(p)
		}
		l.Invalidate(paths...)
	}()

	after := lintAll(t, l)
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(fresh) + `\b`)
	lineOf := regexp.MustCompile(`^(\d+):`)
	norm := func(path string, ds []string, renamed bool) []string {
		var out []string
		for _, d := range ds {
			if m := lineOf.FindStringSubmatch(d); m != nil {
				if n, _ := strconv.Atoi(m[1]); possibleLines[path][n] {
					continue
				}
			}
			if renamed {
				d = word.ReplaceAllString(d, old)
			}
			out = append(out, d)
		}
		slices.Sort(out)
		return out
	}
	files := map[string]bool{}
	for p := range before {
		files[p] = true
	}
	for p := range after {
		files[p] = true
	}
	differ := 0
	for _, p := range sortedKeys(files) {
		if !assert.Equal(t, norm(p, before[p], false), norm(p, after[p], true), "%s: the diagnostics of %s", what, p) {
			if differ++; differ > 5 {
				t.Fatalf("%s: more files differ", what)
			}
		}
	}
	if checkRefs {
		assert.Equal(t, sortedKeys(certain), sortedKeys(refsOf()), "%s: the references of the renamed declarations are the certain places", what)
	}
	return len(paths)
}

func sortedKeys(m map[string]bool) []string { return slices.Sorted(maps.Keys(m)) }
