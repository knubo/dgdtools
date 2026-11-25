package index

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/fileset"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// listed writes directory entries as names, a directory with a '/'
// behind it and what a stub has with a '*'.
func listed(es []DirEntry) []string {
	out := []string{}
	for _, e := range es {
		name := e.Name
		if e.Dir {
			name += "/"
		}
		if e.Stub {
			name += "*"
		}
		out = append(out, name)
	}
	return out
}

func TestDirEntries(t *testing.T) {
	l := newLib(t, map[string]string{
		"include/sys/types.h": "", "include/.hidden.h": "", "std/room.c": "", "std/room/exit.c": "", "std/notes.txt": "",
		"obj/a.c": "", "obj/defs.h": "", "obj/sub/b.c": "", "obj/sub/more.h": "",
	})
	s := l.ix.Snapshot()
	// Beside the file and in the include directories; the <...> form
	// the include directories only.
	assert.Equal(t, []string{"a.c", "defs.h", "std.h", "sub/", "sys/"}, listed(s.IncludeEntries("/lib/obj/a.c", "", false)))
	assert.Equal(t, []string{"std.h", "sys/"}, listed(s.IncludeEntries("/lib/obj/a.c", "", true)))
	assert.Equal(t, []string{"types.h"}, listed(s.IncludeEntries("/lib/obj/a.c", "sys", true)))
	assert.Equal(t, []string{"b.c", "more.h"}, listed(s.IncludeEntries("/lib/obj/a.c", "sub", false)))
	assert.Equal(t, []string{"notes.txt", "room/", "room.c"}, listed(s.IncludeEntries("/lib/obj/a.c", "../std", false)))
	assert.Equal(t, []string{"notes.txt", "room/", "room.c"}, listed(s.IncludeEntries("/lib/obj/a.c", "/std", false)))
	assert.Empty(t, s.IncludeEntries("/lib/obj/a.c", "nowhere", false))
	// Objects by their names; a directory and an object of one name.
	assert.Equal(t, []string{"include/", "obj/", "std/", "sys/"}, listed(s.ObjectEntries("", "/obj/a")))
	assert.Equal(t, []string{"room", "room/"}, listed(s.ObjectEntries("/std", "/obj/a")))
	assert.Equal(t, []string{"room", "room/"}, listed(s.ObjectEntries("std/", "/obj/a")))
	assert.Equal(t, []string{"exit"}, listed(s.ObjectEntries("/std/room", "/obj/a")))
	assert.Empty(t, s.ObjectEntries("/std", ""))
	// What an editor has and the disk has not.
	l.set(t, "obj/new/c.c", "")
	assert.Contains(t, listed(l.ix.Snapshot().ObjectEntries("/obj", "/obj/a")), "new/")

	// A lib where an object path is read from the file that writes it.
	rel := newLib(t, map[string]string{"obj/a.c": "", "obj/sub/b.c": ""}, func(c *config.Config) { c.RelativeObjectPaths = config.ObjectPathsFromFile })
	assert.Equal(t, []string{"a", "sub/"}, listed(rel.ix.Snapshot().ObjectEntries("", "/obj/a")))
	assert.Equal(t, []string{"b"}, listed(rel.ix.Snapshot().ObjectEntries("sub", "/obj/a")))
	assert.Equal(t, []string{"auto", "forced.h"}[:1], listed(rel.ix.Snapshot().ObjectEntries("/sys", "/obj/a")))

	// The auto object's functions, by name.
	assert.Equal(t, []string{"hidden_auto", "object_name"}, slices.Sorted(maps.Keys(s.AutoFuncs())))
}

// A checkout with a profile: the disk for the checkout, the stubs for
// the rest, and the directories that lead to the checkout.
func TestDirEntriesInStubs(t *testing.T) {
	stubs := vfs.Mem{"stub://lib/std/room.c": "", "stub://lib/std/room/exit.c": "", "stub://lib/include/std.h": "", "stub://lib/players/ann/old.c": ""}
	disk := vfs.Mem{"/home/ann/hall.c": "", "/home/ann/mine.h": "", "/home/ann/sub/closet.c": ""}
	cfg := &config.Config{}
	cfg.IncludeDirs = []string{"/include"}
	ix := New(cfg, Layout{
		Tree:  fileset.Tree{CheckoutDir: "/home/ann", CheckoutLib: "/players/ann"},
		Stubs: "stub://lib", StubFiles: []string{"/include/std.h", "/players/ann/old.c", "/std/room.c", "/std/room/exit.c"},
	}, unionFS{disk, stubs})
	s := ix.Snapshot()
	assert.Equal(t, []string{"hall.c", "mine.h", "std.h*", "sub/"}, listed(s.IncludeEntries("/home/ann/hall.c", "", false)))
	assert.Equal(t, []string{"std.h*"}, listed(s.IncludeEntries("/home/ann/hall.c", "", true)))
	assert.Equal(t, []string{"include/*", "players/", "std/*"}, listed(s.ObjectEntries("/", "/players/ann/hall")))
	assert.Equal(t, []string{"ann/"}, listed(s.ObjectEntries("/players", "/players/ann/hall")))
	assert.Equal(t, []string{"hall", "sub/"}, listed(s.ObjectEntries("/players/ann", "/players/ann/hall")))
	assert.Equal(t, []string{"room*", "room/*"}, listed(s.ObjectEntries("/std", "/players/ann/hall")))
}

// The disk as it is: a directory is listed and not gone into.
func TestDirEntriesOnDisk(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"std/room.c", "std/room/exit.c", "std/deep/er/x.c", "include/std.h"} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, nil, 0o644))
	}
	cfg := &config.Config{}
	cfg.Root = root
	s := New(cfg, Layout{Tree: fileset.Tree{Root: root}}, vfs.OS{}).Snapshot()
	assert.Equal(t, []string{"deep/", "room", "room/"}, listed(s.ObjectEntries("/std", "/std/room")))
	assert.Equal(t, []string{"include/", "std/"}, listed(s.ObjectEntries("/", "/std/room")))
	// A file with no lib around it: the headers beside it.
	none := New(&config.Config{}, Layout{}, vfs.OS{}).Snapshot()
	assert.Equal(t, []string{"deep/", "room/", "room.c"}, listed(none.IncludeEntries(filepath.Join(root, "std", "room.c"), "", false)))
	assert.Equal(t, []string{"std.h"}, listed(none.IncludeEntries(filepath.Join(root, "std", "room.c"), "../include", false)))
	assert.Empty(t, none.IncludeEntries(filepath.Join(root, "std", "room.c"), "", true))
}
