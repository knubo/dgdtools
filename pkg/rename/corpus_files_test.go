//go:build corpus

package rename_test

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
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

// moved is an overlay of the lib in which files can be gone too: what a
// move leaves behind.
type moved struct {
	*vfs.Overlay
	mu   sync.RWMutex
	gone map[string]bool
}

func (m *moved) isGone(p string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.gone[filepath.Clean(p)]
}

func (m *moved) ReadFile(p string) ([]byte, error) {
	if m.isGone(p) {
		return nil, &fs.PathError{Op: "read", Path: p, Err: fs.ErrNotExist}
	}
	return m.Overlay.ReadFile(p)
}

func (m *moved) Exists(p string) bool { return !m.isGone(p) && m.Overlay.Exists(p) }

func (m *moved) WalkDir(root string, fn fs.WalkDirFunc) error {
	return m.Overlay.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if m.isGone(p) {
			return nil
		}
		return fn(p, d, err)
	})
}

// TestCorpusRenameFiles moves files of a real lib ($DGDTOOLS_CONFIG,
// with an absolute root) with rename.Files and lints the whole lib again
// with the edits made and the files moved (in an overlay of the lib,
// which is not written): the objects most inherited, the headers most
// included, every so many objects that #include a file beside them, and
// a directory ($LPC_FILES of each, 3 when not set). Each in turn is
// renamed in its directory and moved to another (/qq_moved and its
// path).
//
// Invariant: the diagnostics of every file are those it had (a moved
// file's at its new place), but for the moved files' paths in their
// messages; the files with a place left possible (a path made by a
// macro), and those that inherit or include them, are left out, and
// counted, as are, for a header, the files that include a header the
// configuration excludes, which nothing searches. Logged: each move, its edits and possible places, the files
// left out, how long the plan took.
func TestCorpusRenameFiles(t *testing.T) {
	cfgPath := os.Getenv("DGDTOOLS_CONFIG")
	if cfgPath == "" {
		t.Skip("DGDTOOLS_CONFIG not set")
	}
	each := 3
	if n, err := strconv.Atoi(os.Getenv("LPC_FILES")); err == nil {
		each = n
	}
	ctx := context.Background()
	over := &moved{Overlay: vfs.NewOverlay(vfs.OS{}), gone: map[string]bool{}}
	prj, err := config.Discover(over, nil, config.DiscoverOptions{ConfigPath: cfgPath})
	require.NoError(t, err)
	require.NotEmpty(t, prj.Root, "the configuration needs the whole lib")
	l, err := lint.New(prj, over)
	require.NoError(t, err)
	require.True(t, l.Scan(ctx))
	before := lintAll(t, l)
	t.Logf("lint of the lib: %d files with diagnostics", len(before))

	targets := fileTargets(t, l, each)
	for i, from := range targets {
		snap := l.Index().Snapshot()
		lf := snap.LibFile(from)
		var to string
		if i%2 == 0 {
			to = filepath.Join(filepath.Dir(from), "q_"+filepath.Base(from))
		} else {
			root, _ := strings.CutSuffix(from, filepath.FromSlash(lf))
			to = filepath.Join(root, "qq_moved", filepath.FromSlash(lf))
		}
		require.True(t, l.Scan(ctx))
		began := time.Now()
		plan, err := rename.Files(ctx, l.Index().Snapshot(), []rename.Move{{From: from, To: to}})
		took := time.Since(began)
		var no *rename.Refusal
		if errors.As(err, &no) {
			t.Errorf("refused %s: %s", lf, no.Reason)
			continue
		}
		require.NoError(t, err)
		require.NotNil(t, plan, lf)
		out := checkMove(t, l, over, before, plan)
		t.Logf("moved %-45s to %-55s files %3d  edits %4d  possible %3d  left out %5d  %s",
			lf, snap.LibFile(to), len(plan.Moves), len(plan.Edits), plan.Count(rename.Possible), out, took.Round(time.Millisecond))
	}
}

// fileTargets picks the files to move, by path on disk.
func fileTargets(t *testing.T, l *lint.Linter, each int) []string {
	ctx := context.Background()
	snap := l.Index().Snapshot()
	units, complete := snap.UseUnits(ctx)
	require.True(t, complete)
	type counted struct {
		path string
		n    int
	}
	var objects, headers []counted
	var beside []string
	dirs := map[string]int{}
	for _, u := range units {
		if filepath.Ext(u.Path) == ".h" {
			inc, _ := snap.Includers(ctx, u.Path)
			headers = append(headers, counted{u.Path, len(inc)})
			continue
		}
		dirs[filepath.Dir(u.Path)]++
		if users, _ := snap.Users(ctx, u.LibPath); len(users) > 0 {
			objects = append(objects, counted{u.Path, len(users)})
		}
		for _, use := range u.AllUses() {
			if use.Kind == index.UseInclude && filepath.Dir(use.Name) == filepath.Dir(u.Path) {
				beside = append(beside, u.Path)
				break
			}
		}
	}
	most := func(cs []counted) []string {
		slices.SortFunc(cs, func(a, b counted) int { return cmp.Or(cmp.Compare(b.n, a.n), strings.Compare(a.path, b.path)) })
		var out []string
		for _, c := range cs[:min(each, len(cs))] {
			out = append(out, c.path)
		}
		return out
	}
	out := append(most(objects), most(headers)...)
	slices.Sort(beside)
	for i := 0; i < len(beside) && each > 0; i += max(1, len(beside)/each) {
		out = append(out, beside[i])
	}
	// Directories of a few objects, the first ones in path order.
	var small []string
	for d, n := range dirs {
		if n >= 3 && n <= 8 {
			small = append(small, d)
		}
	}
	slices.Sort(small)
	for i := 0; i < len(small) && i < each; i++ {
		out = append(out, small[i*max(1, len(small)/each)])
	}
	return out
}

// checkMove makes the plan's edits and moves in the overlay, lints the
// whole lib again and holds it to what it was, and takes it all back. It
// returns how many files were left out of the comparison.
func checkMove(t *testing.T, l *lint.Linter, over *moved, before map[string][]string, plan *rename.Plan) int {
	t.Helper()
	ctx := context.Background()
	snap := l.Index().Snapshot()
	what := "move " + plan.Symbol.Name + " to " + plan.New

	// The files left out: those with a possible place, and their users;
	// for a header, the files that include a header the configuration
	// excludes (an attic's), which is not searched.
	out := map[string]bool{}
	units, _ := snap.UseUnits(ctx)
	searched := map[string]bool{}
	for _, u := range units {
		searched[u.Path] = true
	}
	for _, m := range plan.Moves {
		if filepath.Ext(m.From) != ".h" {
			continue
		}
		for _, u := range units {
			for _, dep := range u.Deps {
				if !searched[dep] && filepath.Ext(dep) == ".h" {
					out[u.Path] = true
				}
			}
		}
	}
	for _, o := range plan.Occurrences {
		if o.Class != rename.Possible || out[o.Path] {
			continue
		}
		out[o.Path] = true
		inc, _ := snap.Includers(ctx, o.Path)
		for _, p := range inc {
			out[p] = true
		}
		for _, p := range append([]string{o.Path}, inc...) {
			users, _ := snap.Users(ctx, snap.LibPath(p))
			for _, lp := range users {
				out[snap.ObjectFile(lp)] = true
			}
		}
	}

	texts := map[string]string{}
	for _, e := range plan.Edits {
		if _, ok := texts[e.Path]; !ok {
			src, err := snap.ReadFile(e.Path)
			require.NoError(t, err)
			texts[e.Path] = string(src)
		}
	}
	byPath := map[string][]rename.Edit{}
	for _, e := range plan.Edits {
		byPath[e.Path] = append(byPath[e.Path], e)
	}
	for p, es := range byPath {
		s := texts[p]
		for i := len(es) - 1; i >= 0; i-- {
			s = s[:es[i].Start] + es[i].NewText + s[es[i].End:]
		}
		texts[p] = s
	}
	where := map[string]string{} // old path to new
	var changed []string
	for _, m := range plan.Moves {
		where[m.From] = m.To
		if _, ok := texts[m.From]; !ok {
			src, err := snap.ReadFile(m.From)
			require.NoError(t, err)
			texts[m.From] = string(src)
		}
	}
	for p, s := range texts {
		dst := cmp.Or(where[p], p)
		over.Set(dst, []byte(s))
		changed = append(changed, p, dst)
	}
	over.mu.Lock()
	for _, m := range plan.Moves {
		over.gone[m.From] = true
	}
	over.mu.Unlock()
	l.Invalidate(changed...)
	defer func() {
		over.mu.Lock()
		clear(over.gone)
		over.mu.Unlock()
		for _, p := range changed {
			over.Delete(p)
		}
		l.Invalidate(changed...)
	}()

	after := lintAll(t, l)
	// The moved files' paths, as messages write them, back to the old.
	var names []string
	for _, m := range plan.Moves {
		of, nf := snap.LibFile(m.From), snap.LibFile(m.To)
		names = append(names, nf, of, strings.TrimSuffix(nf, ".c"), strings.TrimSuffix(of, ".c"))
	}
	back := strings.NewReplacer(names...)
	files := map[string]bool{}
	for p := range before {
		files[cmp.Or(where[p], p)] = true
	}
	for p := range after {
		files[p] = true
	}
	origin := map[string]string{}
	for from, to := range where {
		origin[to] = from
	}
	// The lines of the edits, whose columns move with the paths.
	edited := map[string]map[int]bool{}
	for _, o := range plan.Occurrences {
		if o.Class == rename.Certain {
			if edited[o.Path] == nil {
				edited[o.Path] = map[int]bool{}
			}
			edited[o.Path][int(o.Line)] = true
		}
	}
	lineOf := regexp.MustCompile(`^(\d+):\d+-\d+:\d+ `)
	cols := func(path, d string) string {
		if m := lineOf.FindStringSubmatch(d); m != nil {
			if n, _ := strconv.Atoi(m[1]); edited[path][n] {
				return m[1] + ":* " + d[len(m[0]):]
			}
		}
		return d
	}
	differ := 0
	for _, p := range sortedKeys(files) {
		old := cmp.Or(origin[p], p)
		if out[old] {
			continue
		}
		var got, want []string
		for _, d := range after[p] {
			got = append(got, cols(old, back.Replace(d)))
		}
		for _, d := range before[old] {
			want = append(want, cols(old, d))
		}
		slices.Sort(got)
		slices.Sort(want)
		if !assert.Equal(t, want, got, "%s: the diagnostics of %s", what, path.Base(p)+" ("+p+")") {
			if differ++; differ > 5 {
				t.Fatalf("%s: more files differ", what)
			}
		}
	}
	return len(out)
}
