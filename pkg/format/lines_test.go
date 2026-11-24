package format

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/lexer"
)

func fmtLines(t *testing.T, src string, o Options, lines ...LineRange) string {
	t.Helper()
	f := lexer.Lex("t.c", []byte(src), lexer.Default)
	require.NoError(t, f.CheckRoundTrip())
	out, err := Lines(f, o, lines)
	require.NoError(t, err)
	return string(out)
}

func TestSkipMarkers(t *testing.T) {
	// Between the markers every byte stays; around them the file is
	// formatted, the marker comments included.
	in := strings.Join([]string{
		"void f() {",
		"x=1;",
		"        /* dgdfmt: off */",
		"  exits = ([ \"north\" : \"/room/hall\",",
		"             \"south\" : \"/room/yard\",   ]);",
		"   if(x){y=1;}",
		"        // dgdfmt: on",
		"y=2;",
		"}",
		"",
	}, "\n")
	want := strings.Join([]string{
		"void",
		"f()",
		"{",
		"    x = 1;",
		"    /* dgdfmt: off */",
		"  exits = ([ \"north\" : \"/room/hall\",",
		"             \"south\" : \"/room/yard\",   ]);",
		"   if(x){y=1;}",
		"        // dgdfmt: on",
		"    y = 2;",
		"}",
		"",
	}, "\n")
	assert.Equal(t, want, fmtSrc(t, in, Options{}))
	assert.Equal(t, want, fmtSrc(t, want, Options{}))

	// Without an "on" the rest of the file is kept, its end included.
	in = "int   a;\n/* dgdfmt:off */\nint   b;\nvoid f() {\n}\n\n\n"
	assert.Equal(t, "int a;\n/* dgdfmt:off */\nint   b;\nvoid f() {\n}\n\n\n", fmtSrc(t, in, Options{}))
	// At the top it is the whole file.
	in = "// dgdfmt: off\nint   a;\nvoid f() {\n}\n"
	assert.Equal(t, in, fmtSrc(t, in, Options{}))

	// On the line of a statement, the marker acts from where it stands.
	in = "int   a;   /* dgdfmt: off */   int   b;\nint   c; /* dgdfmt: on */ int   d;\n"
	assert.Equal(t, "int a;   /* dgdfmt: off */   int   b;\nint   c; /* dgdfmt: on */ int d;\n", fmtSrc(t, in, Options{}))

	// A comment that only mentions a marker is not one.
	in = "/* use dgdfmt: off to keep a table */\nint   a;\n/* dgdfmt: offline */\nint   b;\n"
	assert.Equal(t, "/* use dgdfmt: off to keep a table */\nint a;\n/* dgdfmt: offline */\nint b;\n", fmtSrc(t, in, Options{}))

	// The code after a skipped region is laid out from the right state:
	// the brackets opened in it count.
	in = "void\nf()\n{\n    x = foo(a,\n/* dgdfmt: off */\n  b,\n/* dgdfmt: on */\nc);\n}\n"
	assert.Equal(t, "void\nf()\n{\n    x = foo(a,\n        /* dgdfmt: off */\n  b,\n/* dgdfmt: on */\n        c);\n}\n", fmtSrc(t, in, Options{}))
}

const legacy = `void create() {
  int i;
  for (i=0;i<3;i++) {
    foo( i );
    bar(i)   ;
  }
  if (x)
  {
    baz();
  }
}
int   query()   { return 1; }
`

func TestLines(t *testing.T) {
	// Only the selected line changes, and it is indented like the lines
	// of its level around it, not by the configured width.
	want := strings.Replace(legacy, "    bar(i)   ;", "    bar(i);", 1)
	assert.Equal(t, want, fmtLines(t, legacy, Options{}, LineRange{5, 5}))
	// The first line of a block has no neighbour to follow: one unit
	// more than the line that opens it.
	want = strings.Replace(legacy, "    foo( i );", "      foo(i);", 1)
	assert.Equal(t, want, fmtLines(t, legacy, Options{}, LineRange{4, 4}))
	// Several ranges; a selected brace joins the line above it.
	want = strings.Replace(legacy, "  for (i=0;i<3;i++) {", "  for (i = 0; i < 3; i++) {", 1)
	want = strings.Replace(want, "  if (x)\n  {", "  if (x) {", 1)
	assert.Equal(t, want, fmtLines(t, legacy, Options{}, LineRange{3, 3}, LineRange{8, 8}))
	// The last line and the end of the file.
	want = strings.Replace(legacy, "int   query()   { return 1; }", "int query() { return 1; }", 1)
	assert.Equal(t, want, fmtLines(t, legacy+"\n\n", Options{}, LineRange{12, 12}))

	// No lines: nothing changes. All lines: the same as Format.
	assert.Equal(t, legacy, fmtLines(t, legacy, Options{}))
	assert.Equal(t, fmtSrc(t, legacy, Options{}), fmtLines(t, legacy, Options{}, LineRange{1, 1 << 30}))

	// Blanks at the end of a selected line go, though the line break
	// belongs to the next line; the next line is not touched.
	in := "int a;   \nint   b;\n"
	assert.Equal(t, "int a;\nint   b;\n", fmtLines(t, in, Options{}, LineRange{1, 1}))

	// A comment line that is selected is formatted with the line it
	// stands above.
	in = "void\nf()\n{\n    x = 1;\n  /* note */\n        y = 2;\n}\n"
	assert.Equal(t, "void\nf()\n{\n    x = 1;\n    /* note */\n    y = 2;\n}\n", fmtLines(t, in, Options{}, LineRange{5, 5}))
}

func TestLinesFollowTheFile(t *testing.T) {
	// A file indented with tabs, formatted with spaces: the selected
	// line follows its neighbours, and a deeper one adds the unit.
	in := "void\nf()\n{\n\tx = 1;\n\tif (x) {\n  y=2;\n\t}\n  z=3;\n}\n"
	want := "void\nf()\n{\n\tx = 1;\n\tif (x) {\n\t    y = 2;\n\t}\n\tz = 3;\n}\n"
	assert.Equal(t, want, fmtLines(t, in, Options{}, LineRange{6, 6}, LineRange{8, 8}))
	// In a formatted file a range gives what Format gives.
	whole := fmtSrc(t, in, Options{})
	messy := strings.Replace(whole, "        y = 2;", "y=2;", 1)
	assert.Equal(t, whole, fmtLines(t, messy, Options{}, LineRange{6, 6}))
	// Case labels stand at their switch and teach nothing about it.
	in = "void\nf()\n{\n\tswitch (x) {\n\t    case 1:\n\t\ta();\n   b();\n\t    case 2:\n c();\n\t}\n}\n"
	want = "void\nf()\n{\n\tswitch (x) {\n\t    case 1:\n\t\ta();\n\t\tb();\n\t    case 2:\n\t\tc();\n\t}\n}\n"
	assert.Equal(t, want, fmtLines(t, in, Options{}, LineRange{7, 7}, LineRange{9, 9}))
}

func TestLinesAndSkipMarkers(t *testing.T) {
	in := "int   a;\n/* dgdfmt: off */\nint   b;\n/* dgdfmt: on */\nint   c;\n"
	assert.Equal(t, "int   a;\n/* dgdfmt: off */\nint   b;\n/* dgdfmt: on */\nint c;\n", fmtLines(t, in, Options{}, LineRange{3, 5}))
}

func TestEdits(t *testing.T) {
	in := "void f() {\n  x=1;\n    y = 2;\n}\n"
	f := lexer.Lex("t.c", []byte(in), lexer.Default)
	out, err := Format(f, Options{})
	require.NoError(t, err)
	edits := Edits(f, out)
	require.NotEmpty(t, edits)
	// Applied back to front they give the output.
	got := in
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		got = got[:e.Start] + e.NewText + got[e.End:]
	}
	assert.Equal(t, string(out), got)
	// Edits are in order, do not overlap, and touch only whitespace.
	end := uint32(0)
	for _, e := range edits {
		assert.GreaterOrEqual(t, e.Start, end)
		assert.Empty(t, strings.TrimSpace(in[e.Start:e.End]))
		assert.Empty(t, strings.TrimSpace(e.NewText))
		end = e.End
	}
	// The line that was already right is in no edit.
	at := uint32(strings.Index(in, "y = 2;"))
	for _, e := range edits {
		assert.False(t, e.Start <= at+1 && at+1 < e.End, "edit %v covers an unchanged line", e)
	}
	// Nothing to do: no edits. Not a formatting of the file: one edit.
	assert.Empty(t, Edits(lexer.Lex("t.c", out, lexer.Default), out))
	assert.Len(t, Edits(f, []byte("int x;\n")), 1)

	// A range's edits stay in the range.
	out, err = Lines(f, Options{}, []LineRange{{2, 2}})
	require.NoError(t, err)
	edits = Edits(f, out)
	require.Len(t, edits, 3)
	for _, e := range edits {
		assert.Equal(t, 2, f.Pos(e.Start).Line)
	}
}

func TestLinesEveryRange(t *testing.T) {
	samples := []string{
		legacy,
		"#include <std.h>\n\ninherit \"/std/room\";\n\n/*\n * A room.\n */\nstatic void create()\n{\n  ::create();\n#ifdef FANCY\n\tset_long(\"a\"\n\t  + \"b\");\n#else\n   set_long(\"c\") ;\n#endif\n   switch(x){\n   case 1:\n       foo();break;\n   default:\n   bar();\n   }\n}\n",
		"void f() {\n  foreach(x in y)\n  {\n     z();\n  }\n   if (a) b(); else\n  c();\n}\n\n\n\nint g() { return 1; }   \n",
		"mapping m;\nvoid\nf()\n{\n\tm = ([ \"a\" : 1,\n\t       \"b\" : 2, ]);   /* trailing\n\t\t\t\t    comment */\n\treturn;\n}\n",
		"int a;\r\nvoid f() {\r\n  a=1;\r\n}\r\n",
	}
	for _, src := range samples {
		f := lexer.Lex("t.c", []byte(src), lexer.Default)
		whole, err := Format(f, Options{})
		require.NoError(t, err)
		n := strings.Count(src, "\n") + 1
		all, err := Lines(f, Options{}, []LineRange{{1, n}})
		require.NoError(t, err)
		assert.Equal(t, string(whole), string(all))
		for a := 1; a <= n; a++ {
			for b := a; b <= n; b++ {
				for _, o := range []Options{{}, {IndentStyle: Tabs}} {
					_, err := Lines(f, o, []LineRange{{a, b}})
					assert.NoError(t, err, "lines %d-%d of %q", a, b, src)
				}
			}
		}
		// Formatting a formatted file by ranges changes nothing.
		g := lexer.Lex("t.c", whole, lexer.Default)
		m := strings.Count(string(whole), "\n") + 1
		for a := 1; a <= m; a++ {
			out, err := Lines(g, Options{}, []LineRange{{a, min(a+2, m)}})
			require.NoError(t, err)
			assert.Equal(t, string(whole), string(out), "lines %d-%d", a, a+2)
		}
	}
}
