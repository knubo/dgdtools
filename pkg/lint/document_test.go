package lint_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/syntax"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// Lint is the document-level entry point: one file in, its diagnostics
// and everything they were computed from out.
func TestLintDocument(t *testing.T) {
	prj, m := memProject(map[string]string{
		"obj/defs.h": "#define LIMIT 3\n",
		"obj/a.c":    "#include \"defs.h\"\ninherit \"/std/base\";\nint count;\nvoid f() { this_object()->nope(); \"/std/base\"->hidden(); }\n",
	})
	cfs := &countingFS{FS: vfs.NewOverlay(m)}
	l, err := lint.New(prj, cfs)
	require.NoError(t, err)
	ctx := context.Background()

	doc, err := l.Lint(ctx, "/lib/obj/a.c", lint.Options{})
	require.NoError(t, err)
	assert.Equal(t, "/lib/obj/a.c", doc.Path)
	assert.Equal(t, l.Generation(), doc.Generation)
	assert.False(t, doc.NeedsScan)
	assert.Equal(t, 1, cfs.reads("/lib/obj/a.c"), "one read gives the tree, the facts and the diagnostics")

	// The tree, tokens, macro view and index entry the diagnostics came
	// from.
	require.NotNil(t, doc.Tree)
	var funcs []string
	doc.Tree.Funcs(func(fn *syntax.FuncDecl) bool {
		funcs = append(funcs, fn.Name.Name)
		return true
	})
	assert.Equal(t, []string{"f"}, funcs)
	assert.Equal(t, "/lib/obj/a.c", doc.File.Path)
	assert.Equal(t, "3", doc.Env.Lookup("LIMIT").Body)
	assert.Equal(t, "/obj/a", doc.Unit.LibPath)
	assert.Contains(t, doc.Unit.Vars, "count")
	assert.Equal(t, "/lib/obj/a.c", doc.Unit.Vars["count"].Path)
	chain := l.Index().Snapshot().Chain("/obj/a")
	assert.Equal(t, "/obj/a", chain.Vars["count"].DefinedIn, "chains carry variables")

	require.Len(t, doc.Diagnostics, 2)
	assert.Equal(t, "callable-not-found", doc.Diagnostics[0].Rule)
	static := doc.Diagnostics[1]
	assert.Equal(t, "static-crossobj", static.Rule)
	require.Len(t, static.Related, 1)
	assert.Equal(t, "/lib/std/base.c", static.Related[0].Path)
	assert.Equal(t, 3, static.Related[0].Line)
	assert.Equal(t, 12, static.Related[0].Col)
	assert.Equal(t, 1, cfs.reads("/lib/std/base.c"), "the related location comes from the index, not from reading the file again")
	for _, d := range doc.Diagnostics {
		assert.NotEmpty(t, d.Fingerprint)
	}

	// Options select rules as a run's do; a bad one is an error.
	doc, err = l.Lint(ctx, "/lib/obj/a.c", lint.Options{Select: []string{"static-crossobj"}})
	require.NoError(t, err)
	assert.Len(t, doc.Diagnostics, 1)
	_, err = l.Lint(ctx, "/lib/obj/a.c", lint.Options{Enable: []string{"no-such-rule"}})
	assert.ErrorContains(t, err, "unknown rule")

	_, err = l.Lint(ctx, "/lib/obj/none.c", lint.Options{})
	assert.ErrorContains(t, err, "cannot be read")
	_, err = l.Lint(ctx, "obj/a.c", lint.Options{})
	assert.ErrorContains(t, err, "not an absolute path")
	_, err = l.Run(ctx, lint.Request{Paths: []string{"obj/a.c"}})
	assert.ErrorContains(t, err, "not an absolute path")

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	doc, err = l.Lint(cctx, "/lib/obj/a.c", lint.Options{})
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, doc)
	assert.NotNil(t, doc.Tree, "a cancelled lint still returns the tree")
}

// A server lints with NoScan, runs the scan in the background, and lints
// again.
func TestLintWithoutScan(t *testing.T) {
	prj, m := memProject(map[string]string{
		"std/mod.c":  "void f() { call_out(\"cb\", 1); call_out(\"gone\", 1); }\n",
		"obj/leaf.c": "inherit \"/std/mod\";\nvoid cb() { }\n",
	})
	cfs := &countingFS{FS: vfs.NewOverlay(m)}
	ov := cfs.FS.(*vfs.Overlay)
	l, err := lint.New(prj, cfs)
	require.NoError(t, err)
	ctx := context.Background()
	noScan := lint.Options{NoScan: true}

	doc, err := l.Lint(ctx, "/lib/std/mod.c", noScan)
	require.NoError(t, err)
	assert.Empty(t, doc.Diagnostics, "who inherits mod is not known yet: nothing is concluded")
	assert.True(t, doc.NeedsScan)
	assert.Zero(t, cfs.walks())
	assert.Zero(t, cfs.reads("/lib/obj/leaf.c"))

	require.True(t, l.Scan(ctx))
	doc, err = l.Lint(ctx, "/lib/std/mod.c", noScan)
	require.NoError(t, err)
	assert.False(t, doc.NeedsScan)
	require.Len(t, doc.Diagnostics, 1)
	assert.Contains(t, doc.Diagnostics[0].Message, "'gone'")

	// An edit: the document that depends on it is named, its generation
	// is no longer current, and the scan is one file short.
	gen := doc.Generation
	ov.Set("/lib/obj/leaf.c", []byte("inherit \"/std/mod\";\nvoid gone() { }\n"))
	assert.Contains(t, l.Invalidate("/lib/obj/leaf.c"), "/lib/std/mod.c")
	assert.NotEqual(t, gen, l.Generation())
	doc, err = l.Lint(ctx, "/lib/std/mod.c", noScan)
	require.NoError(t, err)
	assert.True(t, doc.NeedsScan)
	assert.Empty(t, doc.Diagnostics)
	cfs.reset()
	require.True(t, l.Scan(ctx))
	assert.Zero(t, cfs.walks())
	assert.Equal(t, 1, cfs.reads("/lib/obj/leaf.c"))
	assert.Zero(t, cfs.reads("/lib/std/base.c"))
	doc, err = l.Lint(ctx, "/lib/std/mod.c", noScan)
	require.NoError(t, err)
	require.Len(t, doc.Diagnostics, 1)
	assert.Contains(t, doc.Diagnostics[0].Message, "'cb'")

	// A run with NoScan says so instead of scanning.
	l, err = lint.New(prj, cfs)
	require.NoError(t, err)
	res, err := l.Run(ctx, lint.Request{Paths: []string{"/lib/std/mod.c"}, Options: noScan})
	require.NoError(t, err)
	assert.True(t, res.NeedsScan)
	assert.Empty(t, res.Diagnostics)
	res, err = l.Run(ctx, lint.Request{Paths: []string{"/lib/std/mod.c"}})
	require.NoError(t, err)
	assert.False(t, res.NeedsScan)
	assert.Len(t, res.Diagnostics, 1)
	assert.Equal(t, l.Generation(), res.Generation)
}

// A run over the whole lib parses each file once; only the files with a
// question for the whole-lib scan are parsed a second time.
func TestRunParsesEachFileOnce(t *testing.T) {
	files := map[string]string{
		"std/mod.c":      "void f() { call_out(\"cb\", 1); }\n",
		"include/code.h": "void from_header() { }\n",
	}
	for i := range 40 {
		files[fmt.Sprintf("obj/o%d.c", i)] = "#include <std.h>\ninherit \"/std/mod\";\n#include <code.h>\nvoid cb() { from_header(); }\n"
	}
	prj, m := memProject(files)
	cfs := &countingFS{FS: m}
	l, err := lint.New(prj, cfs)
	require.NoError(t, err)
	res, err := l.Run(context.Background(), lint.Request{})
	require.NoError(t, err)
	assert.Empty(t, res.Diagnostics)
	assert.Equal(t, 43, res.Stats.Files)

	assert.Equal(t, 1, cfs.walks(), "the run's listing of the lib serves the scan")
	for i := range 40 {
		assert.Equal(t, 1, cfs.reads(fmt.Sprintf("/lib/obj/o%d.c", i)))
	}
	assert.Equal(t, 1, cfs.reads("/lib/include/std.h"), "a header is read once however many include it")
	assert.Equal(t, 1, cfs.reads("/lib/include/code.h"))
	assert.Equal(t, 1, cfs.reads("/lib/sys/forced.h"))
	assert.LessOrEqual(t, cfs.reads("/lib/std/base.c"), 2, "at most once as somebody's parent and once for its own tree")
	assert.LessOrEqual(t, cfs.reads("/lib/std/mod.c"), 3, "and once more for a file that had to wait for the scan")
}

// An override's globs apply to a header named on the command line by its
// own name.
func TestOverrideMatchesHeaderByName(t *testing.T) {
	prj, m := memProject(map[string]string{"include/h.h": "void f() { x == 1; }\n"})
	prj.Config.Lint.Overrides = []config.Override{{
		Paths: []string{"include/*.h"},
		Rules: map[string]config.RuleSettings{"no-effect-statement": {Severity: "off"}},
	}}
	res := run(t, prj, m, lint.Request{Paths: []string{"/lib/include/h.h"}})
	assert.Empty(t, res.Diagnostics)
}

// A document is handed to whoever answers requests about it, on as many
// goroutines as there are requests: reading its macro environment there
// is safe.
func TestDocumentEnvIsReadConcurrently(t *testing.T) {
	prj, m := memProject(map[string]string{
		"obj/a.c": "#include <std.h>\ninherit STD \"base\";\nstatus f() { return TRUE; }\n",
	})
	l, err := lint.New(prj, m)
	require.NoError(t, err)
	doc, err := l.Lint(context.Background(), "/lib/obj/a.c", lint.Options{})
	require.NoError(t, err)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, name := range []string{"STD", "TRUE", "status", "public", "nope"} {
				doc.Env.Lookup(name)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, `"/std/"`, doc.Env.Lookup("STD").Body)
}

// A file whose text changed without the linter being told is not linted
// from a mixture of what the index knew and what the file now says.
func TestLintRefusesTextItWasNotToldOf(t *testing.T) {
	prj, m := memProject(map[string]string{"obj/a.c": "inherit \"/std/nope\";\n"})
	ov := vfs.NewOverlay(m)
	l, err := lint.New(prj, ov)
	require.NoError(t, err)
	ctx := context.Background()
	doc, err := l.Lint(ctx, "/lib/obj/a.c", lint.Options{})
	require.NoError(t, err)
	require.Len(t, doc.Diagnostics, 1)
	assert.Same(t, l.Index().Snapshot(), doc.Snapshot)

	ov.Set("/lib/obj/a.c", []byte("\n\n\n\ninherit \"/std/base\";\n"))
	_, err = l.Lint(ctx, "/lib/obj/a.c", lint.Options{})
	var fe *lint.FileError
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, lint.FileError{Path: "/lib/obj/a.c", Kind: lint.Stale, Message: "changed while it was being linted"}, *fe)
	// A run says the same of that file, and lints the rest.
	res, err := l.Run(ctx, lint.Request{Paths: []string{"/lib/obj/a.c", "/lib/std/base.c"}})
	require.NoError(t, err)
	require.Len(t, res.Errors, 1)
	assert.Equal(t, lint.Stale, res.Errors[0].Kind)
	assert.Equal(t, 1, res.Stats.Files)

	l.Invalidate("/lib/obj/a.c")
	doc, err = l.Lint(ctx, "/lib/obj/a.c", lint.Options{})
	require.NoError(t, err)
	assert.Empty(t, doc.Diagnostics)
}

// An object whose name has a dot is an object like any other: found by
// those that name it, checked against the lib itself, and matched by the
// globs of an override under its file name.
func TestObjectNameWithDot(t *testing.T) {
	prj, m := memProject(map[string]string{
		"obj/sword.v2.c": "inherit \"/std/base\";\nvoid swing() { this_object()->nowhere(); }\n",
		"obj/user.c":     "inherit \"/obj/sword.v2\";\nvoid f() { \"/obj/sword.v2\"->swing(); clone_object(\"/obj/sword.v2\"); \"/obj/sword.v2\"->gone(); }\n",
	})
	prj.Config.Lint.Overrides = []config.Override{{Paths: []string{"obj/*.v2.c"}, Rules: map[string]config.RuleSettings{"callable-not-found": {Severity: "info"}}}}
	l, err := lint.New(prj, m)
	require.NoError(t, err)
	res, err := l.Run(context.Background(), lint.Request{Paths: []string{"/lib/obj"}})
	require.NoError(t, err)
	var got []string
	for _, d := range res.Diagnostics {
		got = append(got, fmt.Sprintf("%s:%d: %s: %s", d.Path, d.Line, d.Severity, d.Rule))
	}
	assert.Equal(t, []string{
		"/lib/obj/sword.v2.c:2: info: callable-not-found",
		"/lib/obj/user.c:2: error: callable-not-found",
	}, got, "%v", res.Diagnostics)
	rules, err := l.EffectiveRules("/lib/obj/sword.v2.c")
	require.NoError(t, err)
	for _, r := range rules {
		if r.Name == "callable-not-found" {
			assert.Equal(t, "info", r.Severity.String(), "`dgdtools config` and the lint agree")
		}
	}
}
