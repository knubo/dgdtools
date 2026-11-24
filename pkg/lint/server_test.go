package lint_test

import (
	"context"
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
