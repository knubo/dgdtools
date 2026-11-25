package lint

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// A rule that crashes on one file costs that file the rule's findings,
// and says which rule; what the other rules found there stands.
func TestRulePanicCostsOneRule(t *testing.T) {
	m := vfs.Mem{"/lib/a.c": "void f() { x == 1; }\n", "/lib/b.c": "void boom() { x == 1; }\nvoid g() {\n"}
	cfg := &config.Config{Root: "/lib"}
	l, err := New(&config.Project{Config: cfg, Root: "/lib"}, m)
	require.NoError(t, err)
	rule := l.byKey["no-effect-statement"][0]
	run := rule.Run
	rule.Run = func(p *Pass) {
		if p.LibPath == "/b" {
			panic("index out of range")
		}
		run(p)
	}
	crash := FileError{
		Path: "/lib/b.c", Kind: Internal,
		Message: "internal error in rule no-effect-statement (please report): index out of range",
	}

	res, err := l.Run(context.Background(), Request{})
	require.NoError(t, err)
	var found []string
	for _, d := range res.Diagnostics {
		found = append(found, d.Path+" "+d.Rule)
	}
	assert.Equal(t, []string{"/lib/a.c no-effect-statement", "/lib/b.c syntax-error"}, found)
	require.Len(t, res.Errors, 1)
	assert.Equal(t, crash, *res.Errors[0])
	assert.Equal(t, 2, res.Stats.Files)

	// The document is there for whoever wants its tree.
	doc, err := l.Lint(context.Background(), "/lib/b.c", Options{})
	require.NoError(t, err)
	require.NotNil(t, doc.Tree)
	require.Len(t, doc.Diagnostics, 1)
	assert.Equal(t, "syntax-error", doc.Diagnostics[0].Rule)
	require.Len(t, doc.Errors, 1)
	assert.Equal(t, crash, *doc.Errors[0])
}

// The formatter declining a file is not a crash of the rule that asks
// whether the file is formatted, and takes no other finding with it.
func TestUnformattedOnAFileTheFormatterRefuses(t *testing.T) {
	m := vfs.Mem{"/lib/a.c": "void f()\n{\n    x = \"a\" \"b\";\n    if (x) }\n", "/lib/b.c": "void f() { '\\q; }\n"}
	l, err := New(&config.Project{Config: &config.Config{Root: "/lib"}, Root: "/lib"}, m)
	require.NoError(t, err)
	res, err := l.Run(context.Background(), Request{Options: Options{Enable: []string{"unformatted"}}})
	require.NoError(t, err)
	assert.Empty(t, res.Errors)
	var syntax int
	for _, d := range res.Diagnostics {
		if d.Rule == "syntax-error" {
			syntax++
		}
	}
	assert.GreaterOrEqual(t, syntax, 2, "%v", res.Diagnostics)
}
