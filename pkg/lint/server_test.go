package lint_test

import (
	"context"
	"fmt"
	"io/fs"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// These tests hold the linter to what a long-lived server needs from it:
// runs in flight while files change, cancellation at any point, and an
// Invalidate that names every file whose diagnostics a change can touch.

// gatedFS blocks the first WalkDir until released, so a test can hold a
// whole-lib scan open at a known point.
type gatedFS struct {
	vfs.FS
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func newGatedFS(base vfs.FS) *gatedFS {
	return &gatedFS{FS: base, entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *gatedFS) WalkDir(root string, fn fs.WalkDirFunc) error {
	first := false
	g.once.Do(func() { first = true })
	if first {
		close(g.entered)
		<-g.release
	}
	return g.FS.WalkDir(root, fn)
}

func rules(res *lint.Result) []string {
	var out []string
	for _, d := range res.Diagnostics {
		out = append(out, d.Rule+": "+d.Message)
	}
	return out
}

// A whole-lib scan cancelled by one run must not turn into a "missing"
// for another run that was never cancelled, nor stick for later runs.
func TestCancelledScanIsNotAnAnswer(t *testing.T) {
	prj, m := memProject(map[string]string{
		"std/mod.c":  "void f() { call_out(\"cb\", 1); }\n",
		"obj/leaf.c": "inherit \"/std/mod\";\nvoid cb() { }\n",
	})
	gate := newGatedFS(m)
	l, err := lint.New(prj, gate)
	require.NoError(t, err)
	req := lint.Request{Paths: []string{"/lib/std/mod.c"}}

	cctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	var errA, errB error
	var resB *lint.Result
	go func() {
		defer wg.Done()
		_, errA = l.Run(cctx, req)
	}()
	<-gate.entered // A is inside the scan's directory walk
	go func() {
		defer wg.Done()
		resB, errB = l.Run(context.Background(), req)
	}()
	cancel()
	close(gate.release)
	wg.Wait()

	assert.ErrorIs(t, errA, context.Canceled)
	require.NoError(t, errB)
	assert.Empty(t, rules(resB), "cb is provided by the inheritor obj/leaf")

	res, err := l.Run(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, rules(res), "a cancelled scan must not stick")
}

// The force-included header's macros are part of every file's view; a
// change to it must reach files linted afterwards.
func TestInvalidateForceInclude(t *testing.T) {
	prj, m := memProject(map[string]string{
		"sys/forced.h": "#define TARGET \"/std/a\"\n",
		"std/a.c":      "void foo() { }\n",
		"std/b.c":      "void bar() { }\n",
		"obj/x.c":      "void f() { TARGET->foo(); }\n",
	})
	ov := vfs.NewOverlay(m)
	l, err := lint.New(prj, ov)
	require.NoError(t, err)
	ctx := context.Background()
	req := lint.Request{Paths: []string{"/lib/obj/x.c"}}

	res, err := l.Run(ctx, req)
	require.NoError(t, err)
	assert.Empty(t, rules(res))

	ov.Set("/lib/sys/forced.h", []byte("#define TARGET \"/std/b\"\n"))
	changed := l.Invalidate("/lib/sys/forced.h")
	assert.Contains(t, changed, "/lib/obj/x.c", "every file is built from the force-included header")

	res, err = l.Run(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Diagnostics, 1)
	assert.Equal(t, "callable-not-found", res.Diagnostics[0].Rule)
	assert.Contains(t, res.Diagnostics[0].Message, "/std/b")

	// A header the forced header includes is part of the same state.
	ov.Set("/lib/sys/forced.h", []byte("#include \"where.h\"\n"))
	ov.Set("/lib/sys/where.h", []byte("#define TARGET \"/std/a\"\n"))
	l.Invalidate("/lib/sys/forced.h", "/lib/sys/where.h")
	res, err = l.Run(ctx, req)
	require.NoError(t, err)
	assert.Empty(t, rules(res))
	ov.Set("/lib/sys/where.h", []byte("#define TARGET \"/std/b\"\n"))
	assert.Contains(t, l.Invalidate("/lib/sys/where.h"), "/lib/obj/x.c")
	res, err = l.Run(ctx, req)
	require.NoError(t, err)
	require.Len(t, res.Diagnostics, 1)
}

// Invalidate reports the files whose diagnostics the change can affect:
// inheritors, callers, includers, and files waiting on a path that did
// not exist.
func TestInvalidateReportsDependents(t *testing.T) {
	ctx := context.Background()
	setup := func(files map[string]string, lintPaths ...string) (*lint.Linter, *vfs.Overlay) {
		prj, m := memProject(files)
		ov := vfs.NewOverlay(m)
		l, err := lint.New(prj, ov)
		require.NoError(t, err)
		_, err = l.Run(ctx, lint.Request{Paths: lintPaths})
		require.NoError(t, err)
		return l, ov
	}
	lintOne := func(l *lint.Linter, p string) []string {
		res, err := l.Run(ctx, lint.Request{Paths: []string{p}})
		require.NoError(t, err)
		return rules(res)
	}

	t.Run("inheritor of an edited parent", func(t *testing.T) {
		l, ov := setup(map[string]string{
			"std/a.c":  "void foo() { }\n",
			"std/m.c":  "inherit \"/std/a\";\n",
			"obj/x.c":  "inherit \"/std/m\";\nvoid f() { this_object()->foo(); }\n",
			"obj/un.c": "void g() { }\n",
		}, "/lib/obj/x.c", "/lib/obj/un.c")
		ov.Set("/lib/std/a.c", []byte("void renamed() { }\n"))
		changed := l.Invalidate("/lib/std/a.c")
		assert.Contains(t, changed, "/lib/std/a.c")
		assert.Contains(t, changed, "/lib/obj/x.c", "x inherits a through m")
		assert.NotContains(t, changed, "/lib/obj/un.c")
		assert.Len(t, lintOne(l, "/lib/obj/x.c"), 1)
	})

	t.Run("caller of an edited object", func(t *testing.T) {
		l, ov := setup(map[string]string{
			"std/a.c":  "void foo() { }\n",
			"obj/t.c":  "inherit \"/std/a\";\n",
			"obj/x.c":  "void f() { \"/obj/t\"->foo(); }\n",
			"obj/un.c": "void g() { }\n",
		}, "/lib/obj/x.c", "/lib/obj/un.c")
		ov.Set("/lib/std/a.c", []byte("void renamed() { }\n"))
		changed := l.Invalidate("/lib/std/a.c")
		assert.Contains(t, changed, "/lib/obj/x.c", "x calls into an object that inherits a")
		assert.NotContains(t, changed, "/lib/obj/un.c")
		assert.Len(t, lintOne(l, "/lib/obj/x.c"), 1)
	})

	t.Run("module whose callback an inheritor provides", func(t *testing.T) {
		l, ov := setup(map[string]string{
			"std/mod.c":  "void f() { call_out(\"cb\", 1); }\n",
			"obj/leaf.c": "inherit \"/std/mod\";\nvoid cb() { }\n",
		}, "/lib/std/mod.c")
		ov.Set("/lib/obj/leaf.c", []byte("inherit \"/std/mod\";\n"))
		changed := l.Invalidate("/lib/obj/leaf.c")
		assert.Contains(t, changed, "/lib/std/mod.c", "mod's callback was found in leaf")
		assert.Len(t, lintOne(l, "/lib/std/mod.c"), 1)
	})

	t.Run("a header that did not exist is created", func(t *testing.T) {
		l, ov := setup(map[string]string{
			"obj/x.c": "#include \"defs.h\"\nvoid f() { }\n",
		}, "/lib/obj/x.c")
		assert.Len(t, lintOne(l, "/lib/obj/x.c"), 1)
		ov.Set("/lib/obj/defs.h", []byte("#define X 1\n"))
		assert.Contains(t, l.Invalidate("/lib/obj/defs.h"), "/lib/obj/x.c")
		assert.Empty(t, lintOne(l, "/lib/obj/x.c"))
		// A header earlier in the search order than the one in use.
		ov.Set("/lib/include/defs.h", []byte("#define X 2\n"))
		ov.Delete("/lib/obj/defs.h")
		assert.Contains(t, l.Invalidate("/lib/obj/defs.h", "/lib/include/defs.h"), "/lib/obj/x.c")
		assert.Empty(t, lintOne(l, "/lib/obj/x.c"))
	})

	t.Run("an object that did not exist is created", func(t *testing.T) {
		l, ov := setup(map[string]string{
			"obj/x.c": "inherit \"/std/new\";\nvoid f() { \"/obj/new\"->go(); }\n",
		}, "/lib/obj/x.c")
		assert.Len(t, lintOne(l, "/lib/obj/x.c"), 2)
		ov.Set("/lib/std/new.c", []byte("void create() { }\n"))
		assert.Contains(t, l.Invalidate("/lib/std/new.c"), "/lib/obj/x.c")
		ov.Set("/lib/obj/new.c", []byte("void go() { }\n"))
		assert.Contains(t, l.Invalidate("/lib/obj/new.c"), "/lib/obj/x.c")
		assert.Empty(t, lintOne(l, "/lib/obj/x.c"))
	})

	t.Run("edges are pruned when a file stops depending", func(t *testing.T) {
		l, ov := setup(map[string]string{
			"obj/defs.h": "#define X 1\n",
			"obj/x.c":    "#include \"defs.h\"\ninherit \"/std/base\";\nvoid f() { }\n",
		}, "/lib/obj/x.c")
		ov.Set("/lib/obj/x.c", []byte("void f() { }\n"))
		l.Invalidate("/lib/obj/x.c")
		assert.Empty(t, lintOne(l, "/lib/obj/x.c"))
		assert.NotContains(t, l.Invalidate("/lib/obj/defs.h"), "/lib/obj/x.c")
		assert.NotContains(t, l.Invalidate("/lib/std/base.c"), "/lib/obj/x.c")
	})
}

// A whole-lib scan survives edits: after one, a reverse lookup reads only
// what changed.
func TestScanStateSurvivesInvalidate(t *testing.T) {
	files := map[string]string{
		"std/mod.c":  "void f() { call_out(\"cb\", 1); }\n",
		"obj/leaf.c": "inherit \"/std/mod\";\nvoid cb() { }\n",
	}
	for i := range 50 {
		files[fmt.Sprintf("obj/o%d.c", i)] = "void g() { }\n"
	}
	prj, m := memProject(files)
	cfs := &countingFS{FS: vfs.NewOverlay(m)}
	ov := cfs.FS.(*vfs.Overlay)
	l, err := lint.New(prj, cfs)
	require.NoError(t, err)
	ctx := context.Background()
	req := lint.Request{Paths: []string{"/lib/std/mod.c"}}

	res, err := l.Run(ctx, req)
	require.NoError(t, err)
	assert.Empty(t, rules(res))
	require.Greater(t, cfs.reads("/lib/obj/o7.c"), 0, "the first reverse lookup scans the lib")

	cfs.reset()
	ov.Set("/lib/obj/o3.c", []byte("void g() { g(); }\n"))
	l.Invalidate("/lib/obj/o3.c")
	res, err = l.Run(ctx, req)
	require.NoError(t, err)
	assert.Empty(t, rules(res))
	assert.Equal(t, 1, cfs.reads("/lib/obj/o3.c"), "the changed file is read again")
	assert.Zero(t, cfs.reads("/lib/obj/o7.c"), "an unchanged file is not")
	assert.Zero(t, cfs.walks(), "the lib is not walked again")

	// A new inheritor and a deleted one are picked up without a rescan.
	ov.Set("/lib/obj/leaf.c", []byte("inherit \"/std/mod\";\n"))
	ov.Set("/lib/obj/late.c", []byte("inherit \"/std/mod\";\nvoid cb() { }\n"))
	l.Invalidate("/lib/obj/leaf.c", "/lib/obj/late.c")
	res, err = l.Run(ctx, req)
	require.NoError(t, err)
	assert.Empty(t, rules(res), "late.c now provides cb")
	ov.Set("/lib/obj/late.c", []byte("inherit \"/std/mod\";\n"))
	l.Invalidate("/lib/obj/late.c")
	res, err = l.Run(ctx, req)
	require.NoError(t, err)
	assert.Len(t, rules(res), 1, "nobody provides cb any more")
	assert.Zero(t, cfs.walks())
}

type countingFS struct {
	vfs.FS
	mu     sync.Mutex
	nread  map[string]int
	nwalks int
}

func (c *countingFS) ReadFile(p string) ([]byte, error) {
	c.mu.Lock()
	if c.nread == nil {
		c.nread = map[string]int{}
	}
	c.nread[p]++
	c.mu.Unlock()
	return c.FS.ReadFile(p)
}

func (c *countingFS) WalkDir(root string, fn fs.WalkDirFunc) error {
	c.mu.Lock()
	c.nwalks++
	c.mu.Unlock()
	return c.FS.WalkDir(root, fn)
}

func (c *countingFS) reads(p string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nread[p]
}

func (c *countingFS) counts() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]int{}
	for p, n := range c.nread {
		out[p] = n
	}
	return out
}

func (c *countingFS) walks() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nwalks
}

func (c *countingFS) reset() {
	c.mu.Lock()
	c.nread, c.nwalks = nil, 0
	c.mu.Unlock()
}

// raceLib is a lib whose every object asks a reverse question (the
// callback "gone" is nowhere), calls into two others, and sits behind a
// force-included header.
func raceLib(n int) map[string]string {
	files := map[string]string{}
	for i := range n {
		files[fmt.Sprintf("attic/t%d.c", i)] = "void hit() { }\n"
		files[fmt.Sprintf("obj/o%d.c", i)] = fmt.Sprintf(
			"void f() { call_out(\"gone\", 1); \"/attic/t%d\"->hit(); \"/attic/t%d\"->hit(); }\n", i, (i+7)%n)
	}
	return files
}

// Runs in flight while files are edited and invalidated: the shape of an
// editor session. Run under -race.
func TestRunWhileInvalidate(t *testing.T) {
	const n = 120
	prj, m := memProject(raceLib(n))
	prj.Config.Exclude = []string{"attic/**"}
	ov := vfs.NewOverlay(m)
	l, err := lint.New(prj, ov)
	require.NoError(t, err)
	ctx := context.Background()

	res, err := l.Run(ctx, lint.Request{})
	require.NoError(t, err)
	require.Len(t, res.Diagnostics, n, "one missing callback per object")

	var wg sync.WaitGroup
	for g := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range 25 {
				p := fmt.Sprintf("/lib/obj/o%d.c", (g*30+k)%n)
				if _, err := l.Run(ctx, lint.Request{Paths: []string{p}}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	const edited = 40
	wg.Add(1)
	go func() {
		defer wg.Done()
		for k := range edited {
			p := fmt.Sprintf("/lib/obj/o%d.c", k)
			ov.Set(p, []byte("void f() { }\n"))
			l.Invalidate(p)
			if k%10 == 0 {
				l.Invalidate("/lib/sys/forced.h")
			}
		}
	}()
	wg.Wait()

	// Once the edits are in, a run sees exactly the edited lib.
	res, err = l.Run(ctx, lint.Request{})
	require.NoError(t, err)
	assert.Len(t, res.Diagnostics, n-edited)
}

// Runs cancelled at arbitrary points, some of them in the middle of the
// whole-lib scan, while other runs continue. Run under -race.
func TestCancelMidScan(t *testing.T) {
	const n = 120
	prj, m := memProject(raceLib(n))
	prj.Config.Exclude = []string{"attic/**"}
	ctx := context.Background()

	for round := range 8 {
		ov := vfs.NewOverlay(m)
		l, err := lint.New(prj, ov)
		require.NoError(t, err)
		var wg sync.WaitGroup
		for g := range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cctx, cancel := context.WithCancel(ctx)
				defer cancel()
				if g%2 == 0 {
					go func() {
						for range (g + round) * 200 {
							_ = cctx.Err() // spin a little, then cancel
						}
						cancel()
					}()
				}
				p := fmt.Sprintf("/lib/obj/o%d.c", g)
				res, err := l.Run(cctx, lint.Request{Paths: []string{p}})
				if err == nil {
					assert.Len(t, res.Diagnostics, 1, "an uncancelled run has the whole answer")
				} else {
					assert.ErrorIs(t, err, context.Canceled)
					for _, d := range res.Diagnostics {
						assert.Equal(t, "callable-not-found", d.Rule, "a cancelled run reports nothing it could not prove")
					}
				}
			}()
		}
		wg.Wait()
		res, err := l.Run(ctx, lint.Request{})
		require.NoError(t, err)
		assert.Len(t, res.Diagnostics, n)
	}
}
