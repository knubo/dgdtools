package index

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/fileset"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// found writes what a search found, a symbol a line:
//
//	KIND NAME IN FILE:LINE [stub]
func found(syms []Symbol) []string {
	var out []string
	for _, s := range syms {
		line := fmt.Sprintf("%s %s %s %s:%d", map[SymbolKind]string{SymbolFunction: "function", SymbolVariable: "variable", SymbolMacro: "macro"}[s.Kind],
			s.Name, s.In, s.Path, s.Line)
		if s.Stub {
			line += " stub"
		}
		out = append(out, line)
	}
	return out
}

func TestFindSymbols(t *testing.T) {
	l := newLib(t, map[string]string{
		"include/std.h":  "#define SET_NAME 1\n#define NAMES ({ })\n#define GONE 1\n#undef GONE\n",
		"include/code.h": "int set_named() { return 1; }\n",
		"std/base.c":     "#include <std.h>\nstring name;\nvoid set_name(string s) { name = s; }\nstring query_name() { return name; }\n",
		"obj/a.c":        "#include <code.h>\ninherit \"/std/base\";\nvoid set_names(string *s);\nprivate int rename;\nvoid create() { }\n",
		"obj/b.c":        "#include <code.h>\n#define NAME_HERE 1\nvoid SetName() { }\n",
	}, func(c *config.Config) { c.IncludeDirs = []string{"/include"} })
	s := l.ix.Snapshot()

	// Before anything was read there is nothing to find; what one file
	// needed is found once it was asked about.
	syms, more := s.FindSymbols("name", 10)
	assert.Empty(t, syms)
	assert.False(t, more)
	require.Contains(t, s.Chain("/obj/a").Funcs, "set_name")
	assert.Equal(t, []string{
		"variable name /std/base /lib/std/base.c:2",
		"macro NAMES /include/std.h /lib/include/std.h:0",
		"variable rename /obj/a /lib/obj/a.c:4",
		"function set_name /std/base /lib/std/base.c:3",
		"function set_named /include/code.h /lib/include/code.h:1",
		"function set_names /obj/a /lib/obj/a.c:3",
		"function query_name /std/base /lib/std/base.c:4",
		"macro SET_NAME /include/std.h /lib/include/std.h:0",
	}, found(first(s.FindSymbols("name", 10))))

	// The whole lib after the scan: the exact name first, then names
	// that begin with what was typed, that have it, that have its
	// letters; in the case typed before the other; a function a header
	// gives two objects once. A macro of a .c file is the file's own.
	require.True(t, s.Scan(bg))
	assert.Equal(t, []string{
		"function set_name /std/base /lib/std/base.c:3",
		"function set_named /include/code.h /lib/include/code.h:1",
		"function set_names /obj/a /lib/obj/a.c:3",
		"macro SET_NAME /include/std.h /lib/include/std.h:0",
	}, found(first(s.FindSymbols("set_nam", 10))))
	assert.Equal(t, []string{"function SetName /obj/b /lib/obj/b.c:3", "macro SET_NAME /include/std.h /lib/include/std.h:0"},
		found(first(s.FindSymbols("SetName", 2))))
	assert.Equal(t, []string{"function SetName /obj/b /lib/obj/b.c:3", "macro SET_NAME /include/std.h /lib/include/std.h:0", "function set_name /std/base /lib/std/base.c:3"},
		found(first(s.FindSymbols("setname", 3))))
	syms, more = s.FindSymbols("sn", 3)
	assert.Len(t, syms, 3)
	assert.True(t, more)
	for _, q := range []string{"", "  ", "zzz", "GONE"} {
		syms, more = s.FindSymbols(q, 10)
		assert.Empty(t, syms, q)
		assert.False(t, more)
	}
	syms, _ = s.FindSymbols("name", 0)
	assert.Empty(t, syms)

	// The next generation finds what changed.
	l.set(t, "obj/b.c", "void SetTitle() { }\n")
	s = l.ix.Snapshot()
	require.True(t, s.Scan(bg))
	assert.Equal(t, []string{"function SetTitle /obj/b /lib/obj/b.c:1"}, found(first(s.FindSymbols("sett", 10))))
	assert.NotContains(t, found(first(s.FindSymbols("SetName", 10))), "function SetName /obj/b /lib/obj/b.c:3")
}

func first(syms []Symbol, _ bool) []Symbol { return syms }

// Of a checkout the profile's stubs are searched too, read or not, and
// what they declare is marked; a file of the checkout stands before the
// stub of its name.
func TestFindSymbolsInStubs(t *testing.T) {
	stubs := vfs.Mem{
		"stub://lib/std/room.c":        "void set_short(string s);\nint short_var;\n",
		"stub://lib/include/std.h":     "#define SHORT_MAX 10\n",
		"stub://lib/players/ann/old.c": "void short_gone();\n",
	}
	disk := vfs.Mem{"/home/ann/hall.c": "void shorten() { }\n"}
	ix := New(&config.Config{}, Layout{
		Tree:  fileset.Tree{CheckoutDir: "/home/ann", CheckoutLib: "/players/ann"},
		Stubs: "stub://lib", StubFiles: []string{"/include/std.h", "/players/ann/old.c", "/std/room.c"},
	}, unionFS{disk, stubs})
	s := ix.Snapshot()
	require.True(t, s.Scan(bg))
	assert.Equal(t, []string{
		"function shorten /players/ann/hall /home/ann/hall.c:1",
		"variable short_var /std/room stub://lib/std/room.c:2 stub",
		"macro SHORT_MAX /include/std.h stub://lib/include/std.h:0 stub",
		"function set_short /std/room stub://lib/std/room.c:1 stub",
	}, found(first(s.FindSymbols("short", 10))))

	// No lib, nothing to search.
	none := New(&config.Config{}, Layout{}, disk)
	assert.Empty(t, first(none.Snapshot().FindSymbols("short", 10)))
}
