package rename_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/rename"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// filesLib is a lib whose files name each other every way there is: an
// inherit statement by its path, with and without ".c", and by a macro;
// object paths handed to functions; #include "..." beside the file and
// from the lib's root, #include <...> from an include directory.
func filesLib() vfs.Mem {
	return vfs.Mem{
		"/lib/dgdtools.yml":           "root: .\ninclude_dirs: [/include]\n",
		"/lib/include/std.h":          "#define ROOM \"/std/room\"\n",
		"/lib/include/sub/deep.h":     "#define DEEP 1\n",
		"/lib/std/room.h":             "#define LIGHT 1\n",
		"/lib/std/room.c":             "#include \"room.h\"\nint light;\n",
		"/lib/obj/a.c":                "inherit \"/std/room\";\n",
		"/lib/obj/b.c":                "#include <std.h>\ninherit ROOM;\n",
		"/lib/obj/c.c":                "void f() { clone_object(\"/std/room\"); clone_object(\"/std/room.c\"); }\n",
		"/lib/obj/d.c":                "#include \"/std/room.h\"\n#include <sub/deep.h>\nint x;\n",
		"/lib/obj/e.c":                "inherit \"/obj/a\";\n#include \"../std/room.h\"\n",
		"/lib/obj/f.c":                "#include <std.h>\nvoid f() { \"/std/room\"->g(); call_other(\"/std/room.c\", \"g\"); ROOM->g(); }\n",
		"/lib/players/ann/workroom.c": "inherit \"std/room\";\n",
	}
}

// moved plans moving the files (FROM=TO pairs of lib paths) and returns
// the files after the edits, each as "== FILE" and its text, in path
// order; then the possible places and the warnings. Or "refused: REASON".
func (w *world) moved(pairs ...string) string {
	w.t.Helper()
	var moves []rename.Move
	for _, p := range pairs {
		from, to, ok := strings.Cut(p, "=")
		require.True(w.t, ok)
		moves = append(moves, rename.Move{From: "/lib" + from, To: "/lib" + to})
	}
	plan, err := rename.Files(context.Background(), w.l.Index().Snapshot(), moves)
	var no *rename.Refusal
	if errors.As(err, &no) {
		return "refused: " + no.Reason
	}
	require.NoError(w.t, err)
	if plan == nil {
		return "nil"
	}
	byPath := map[string][]rename.Edit{}
	for _, e := range plan.Edits {
		byPath[e.Path] = append(byPath[e.Path], e)
	}
	var b strings.Builder
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		src := w.fs[p]
		es := byPath[p]
		for i := len(es) - 1; i >= 0; i-- {
			e := es[i]
			src = src[:e.Start] + e.NewText + src[e.End:]
		}
		fmt.Fprintf(&b, "== %s\n%s", p, src)
	}
	for _, o := range plan.Occurrences {
		if o.Class != rename.Certain {
			fmt.Fprintf(&b, "%s %s:%d:%d %s\n", o.Class, o.Path, o.Line, o.Col, o.Reason)
		}
	}
	for _, warn := range plan.Warnings {
		fmt.Fprintf(&b, "! %s\n", warn)
	}
	return b.String()
}

func TestFiles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		moves []string
		want  string
	}{{
		name:  "object in its directory",
		moves: []string{"/std/room.c=/std/place.c"},
		want: `== /lib/obj/a.c
inherit "/std/place";
== /lib/obj/c.c
void f() { clone_object("/std/place"); clone_object("/std/place.c"); }
== /lib/obj/f.c
#include <std.h>
void f() { "/std/place"->g(); call_other("/std/place.c", "g"); ROOM->g(); }
== /lib/players/ann/workroom.c
inherit "std/place";
possible /lib/obj/b.c:2:9 names /std/room.c by the macro ROOM, which other files may use too: not rewritten
possible /lib/obj/f.c:2:62 names /std/room.c by the macro ROOM, which other files may use too: not rewritten
! moved base files: /std/room.c (4 objects inherit it); an object keeps the program it was compiled with, so the move takes effect everywhere at the scheduled reboot
`,
	}, {
		name:  "object to another directory: its own relative #include",
		moves: []string{"/std/room.c=/domain/room.c"},
		want: `== /lib/obj/a.c
inherit "/domain/room";
== /lib/obj/c.c
void f() { clone_object("/domain/room"); clone_object("/domain/room.c"); }
== /lib/obj/f.c
#include <std.h>
void f() { "/domain/room"->g(); call_other("/domain/room.c", "g"); ROOM->g(); }
== /lib/players/ann/workroom.c
inherit "domain/room";
== /lib/std/room.c
#include "/std/room.h"
int light;
possible /lib/obj/b.c:2:9 names /std/room.c by the macro ROOM, which other files may use too: not rewritten
possible /lib/obj/f.c:2:62 names /std/room.c by the macro ROOM, which other files may use too: not rewritten
! moved base files: /std/room.c (4 objects inherit it); an object keeps the program it was compiled with, so the move takes effect everywhere at the scheduled reboot
`,
	}, {
		name:  "header to an include directory",
		moves: []string{"/std/room.h=/include/room.h"},
		want: `== /lib/obj/d.c
#include "/include/room.h"
#include <sub/deep.h>
int x;
== /lib/obj/e.c
inherit "/obj/a";
#include "room.h"
`,
	}, {
		name:  "header in an include directory",
		moves: []string{"/include/sub/deep.h=/include/other/deep.h"},
		want: `== /lib/obj/d.c
#include "/std/room.h"
#include <other/deep.h>
int x;
`,
	}, {
		name:  "a directory",
		moves: []string{"/std=/base"},
		want: `== /lib/obj/a.c
inherit "/base/room";
== /lib/obj/c.c
void f() { clone_object("/base/room"); clone_object("/base/room.c"); }
== /lib/obj/d.c
#include "/base/room.h"
#include <sub/deep.h>
int x;
== /lib/obj/e.c
inherit "/obj/a";
#include "../base/room.h"
== /lib/obj/f.c
#include <std.h>
void f() { "/base/room"->g(); call_other("/base/room.c", "g"); ROOM->g(); }
== /lib/players/ann/workroom.c
inherit "base/room";
possible /lib/obj/b.c:2:9 names /std/room.c by the macro ROOM, which other files may use too: not rewritten
possible /lib/obj/f.c:2:62 names /std/room.c by the macro ROOM, which other files may use too: not rewritten
! moved base files: /std/room.c (4 objects inherit it); an object keeps the program it was compiled with, so the move takes effect everywhere at the scheduled reboot
`,
	}, {
		name:  "out of the lib",
		moves: []string{"/obj/a.c=/../a.c"},
		want: `possible /lib/obj/e.c:1:9 names /obj/a.c, which moves out of the mudlib
! moved base files: /obj/a.c (1 object inherits it); an object keeps the program it was compiled with, so the move takes effect everywhere at the scheduled reboot
`,
	}, {
		name:  "not of the lib",
		moves: []string{"/../x.c=/../y.c"},
		want:  "nil",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, filesLib(), "/lib")
			assert.Equal(t, tc.want, w.moved(tc.moves...))
		})
	}
}
