package fileset

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/vfs"
)

func TestMatcher(t *testing.T) {
	m := NewMatcher([]string{"attic/**", "**/old/**", "**/*.pre*", "d/*/tmp.c"})
	for rel, want := range map[string]bool{
		"attic/a.c":        true,
		"d/attic/a.c":      false,
		"d/x/old/a.c":      true,
		"old/a.c":          true,
		"d/x/a.preBob.c":   true,
		"d/x/tmp.c":        true,
		"d/x/y/tmp.c":      false,
		"std/room.c":       false,
		"/std/old/room.c/": true,
	} {
		assert.Equal(t, want, m.Match(rel), rel)
	}
	assert.True(t, m.MatchDir("attic"))
	assert.True(t, m.MatchDir("d/x/old"))
	assert.False(t, m.MatchDir("d/x"))
	assert.False(t, NewMatcher(nil).Match("anything"))
}

func TestTreePaths(t *testing.T) {
	whole := Tree{Root: "/mud/lib"}
	rel, ok := whole.Rel("/mud/lib/std/room.c")
	assert.True(t, ok)
	assert.Equal(t, "std/room.c", rel)
	_, ok = whole.Rel("/mud/other/x.c")
	assert.False(t, ok)
	p, ok := whole.Path("/std/room.c")
	assert.True(t, ok)
	assert.Equal(t, "/mud/lib/std/room.c", p)
	assert.False(t, whole.InCheckout("/std/room.c"))

	// A partial checkout: one lib directory, somewhere else on disk.
	part := Tree{CheckoutDir: "/home/ann", CheckoutLib: "/players/ann"}
	rel, ok = part.Rel("/home/ann/rooms/hall.c")
	assert.True(t, ok)
	assert.Equal(t, "players/ann/rooms/hall.c", rel)
	_, ok = part.Rel("/home/bob/x.c")
	assert.False(t, ok)
	p, ok = part.Path("/players/ann/rooms/hall.c")
	assert.True(t, ok)
	assert.Equal(t, "/home/ann/rooms/hall.c", p)
	_, ok = part.Path("/std/room.c")
	assert.False(t, ok, "nothing on disk holds the rest of the lib")
	assert.True(t, part.InCheckout("/players/ann"))
	assert.False(t, part.InCheckout("/players/annabel/x.c"))

	_, ok = Tree{}.Rel("/anywhere/x.c")
	assert.False(t, ok)
}

func TestTreeWalk(t *testing.T) {
	fsys := vfs.Mem{
		"/mud/lib/std/room.c":       "",
		"/mud/lib/std/room.h":       "",
		"/mud/lib/std/notes.txt":    "",
		"/mud/lib/d/x/a.c":          "",
		"/mud/lib/d/x/old/b.c":      "",
		"/mud/lib/d/old/c.c":        "",
		"/mud/lib/attic/d.c":        "",
		"/mud/elsewhere/old/e.c":    "",
		"/home/ann/rooms/hall.c":    "",
		"/home/ann/old/rooms/was.c": "",
	}
	walk := func(tr Tree, dir string, exts []string, exclude ...string) []string {
		var got []string
		require.NoError(t, tr.Walk(context.Background(), fsys, dir, exts, NewMatcher(exclude), func(p string) { got = append(got, p) }))
		return got
	}
	whole := Tree{Root: "/mud/lib"}
	assert.Equal(t, []string{"/mud/lib/d/x/a.c", "/mud/lib/std/room.c"},
		walk(whole, "/mud/lib", []string{".c"}, "attic/**", "**/old/**"))
	assert.Equal(t, []string{"/mud/lib/std/room.c", "/mud/lib/std/room.h"},
		walk(whole, "/mud/lib/std", []string{".c", ".h"}))
	// The globs are lib-relative wherever the walk starts.
	assert.Equal(t, []string{"/mud/lib/d/x/a.c", "/mud/lib/d/x/old/b.c"},
		walk(whole, "/mud/lib/d", []string{".c"}, "d/old/**"))
	assert.Empty(t, walk(whole, "/mud/lib/d/old", []string{".c"}, "d/old/**"),
		"the directory walked is itself excluded file by file")
	// A file outside the lib is never excluded.
	assert.Equal(t, []string{"/mud/elsewhere/old/e.c"}, walk(whole, "/mud/elsewhere", []string{".c"}, "**/old/**"))
	// In a partial checkout the globs see lib paths too.
	part := Tree{CheckoutDir: "/home/ann", CheckoutLib: "/players/ann"}
	assert.Equal(t, []string{"/home/ann/rooms/hall.c"}, walk(part, "/home/ann", []string{".c"}, "players/*/old/**"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, whole.Walk(ctx, fsys, "/mud/lib", []string{".c"}, NewMatcher(nil), func(string) {}), context.Canceled)
}
