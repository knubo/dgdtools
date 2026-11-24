package lint

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// A rule that crashes on one file costs that file, and says which rule.
func TestRulePanicIsTheFilesError(t *testing.T) {
	m := vfs.Mem{"/lib/a.c": "void f() { x == 1; }\n", "/lib/b.c": "void boom() { x == 1; }\n"}
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
	res, err := l.Run(context.Background(), Request{})
	require.NoError(t, err)
	require.Len(t, res.Diagnostics, 1)
	assert.Equal(t, "/lib/a.c", res.Diagnostics[0].Path)
	require.Len(t, res.Errors, 1)
	assert.Equal(t, FileError{
		Path: "/lib/b.c", Kind: Internal,
		Message: "internal error in rule no-effect-statement (please report): index out of range",
	}, *res.Errors[0])
	assert.Equal(t, 1, res.Stats.Files)
}
