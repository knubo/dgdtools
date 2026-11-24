package pp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/token"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		body string
		want Class
	}{
		{"", ClassEmpty},
		{"/* nothing */", ClassEmpty},
		{"private static", ClassSpecifier},
		{"int", ClassType},
		{"mixed *", ClassType},
		{"object", ClassType},
		{`"/d/Wild/"`, ClassString},
		{`("/std/" + "room")`, ClassString},
		{"(LIB_DIR + \"thing\")", ClassExpr},
		{"this_player()", ClassExpr},
		{"((a) > (b) ? (a) : (b))", ClassExpr},
		{"((int) x)", ClassExpr},
		{"-1", ClassExpr},
		{"{ write(s); return 0; }", ClassStatement},
		{"if (!x) return 0", ClassStatement},
		{"for (i = 0; i < sizeof(a); i++)", ClassStatement},
		{"do { x; } while (0)", ClassStatement},
		{"a = 1; b = 2", ClassStatement},
		{"int x;", ClassStatement},
		{"return", ClassStatement},
		{"this_player()->", ClassFragment},
		{"{", ClassFragment},
		{"}", ClassFragment},
		{"&& ready", ClassFragment},
		{"case x:", ClassFragment},
		{"else return 0;", ClassFragment},
		{"foo bar", ClassFragment},
		{"int x", ClassFragment},
		{"(string a, int b)", ClassFragment},
		{"x ## y", ClassFragment},
		{"static int", ClassType},
		{"static int x", ClassFragment},
	}
	for _, tt := range tests {
		got := Classify(cleanDirective([]byte(tt.body)), lexer.Default)
		assert.Equal(t, tt.want.String(), got.String(), "%q", tt.body)
	}
}

func scan(src string, defined ...string) (*token.File, *Selection, *Table) {
	f := lexer.Lex("t.c", []byte(src), lexer.Default)
	d := map[string]bool{}
	for _, n := range defined {
		d[n] = true
	}
	sel := Select(f, d)
	return f, sel, Scan(f, sel)
}

func sigText(f *token.File, sel *Selection) string {
	s := ""
	for _, i := range sel.Sig {
		s += string(f.Text(f.Tokens[i])) + " "
	}
	return s
}

func TestSelect(t *testing.T) {
	// Decided: #if 0 / 1, predefined names, names defined earlier in the
	// file. Everything else takes the first branch and is marked guessed.
	f, sel, _ := scan(`a
#if 0
b
#else
c
#endif
#ifdef __DGD__
d
#else
e
#endif
#ifndef __DGD__
f
#elif 1
g
#endif
#define LOCAL 0
#if LOCAL
h
#else
i
#endif
#ifdef UNKNOWN
j
#else
k
#endif
#if defined(__DGD__)
l
#endif
#if !defined(__DGD__)
m
#endif
#undef LOCAL
#ifdef LOCAL
n
#endif
`, "__DGD__")
	assert.Equal(t, "a c d g i j l n ", sigText(f, sel))
	guessed := ""
	for _, i := range sel.Sig {
		if sel.Guessed(i) {
			guessed += string(f.Text(f.Tokens[i]))
		}
	}
	assert.Equal(t, "jn", guessed, "only the undecided branches are guesses")
	assert.True(t, sel.Conditional)

	// An include guard is decided, so a header's contents are certain.
	f, sel, _ = scan("/* header */\n#ifndef X_H\n#define X_H\nint a;\n#endif\n")
	for _, i := range sel.Sig {
		assert.False(t, sel.Guessed(i))
	}
	// ...but `#ifndef X` that is not a guard is a guess.
	f, sel, _ = scan("int z;\n#ifndef X\nint a;\n#endif\n")
	assert.True(t, sel.Guessed(sel.Sig[len(sel.Sig)-1]))
	_ = f
}

func TestScan(t *testing.T) {
	_, _, tab := scan(`#include <std.h>
#include "local.h" /* comment */
#  include   "../up.h"
#define OBJ "/obj/x"
#define MAX(a, b) ((a) > (b) ? (a) : (b))
#define MULTI first \
              + second
#define SPACED (x)
#if 0
#define DEAD 1
#include "dead.h"
#endif
#undef OBJ
#define OBJ this_object()
#undef GONE
#define TWICE 1
#define TWICE { }
`)
	if assert.Len(t, tab.Includes, 3) {
		assert.Equal(t, "<std.h>", tab.Includes[0].Raw())
		assert.Equal(t, `"local.h"`, tab.Includes[1].Raw())
		assert.Equal(t, "../up.h", tab.Includes[2].Name)
	}
	assert.Nil(t, tab.Macros["DEAD"], "defines in inactive branches are not defines")
	max := tab.Macros["MAX"]
	if assert.NotNil(t, max) {
		assert.True(t, max.FuncLike)
		assert.Equal(t, []string{"a", "b"}, max.Params)
		assert.Equal(t, ClassExpr, max.Class)
	}
	assert.Regexp(t, `^first\s+\+ second$`, tab.Macros["MULTI"].Body)
	assert.False(t, tab.Macros["SPACED"].FuncLike, "a space before '(' makes it object-like")
	// #undef followed by #define is a plain redefinition.
	assert.Equal(t, ClassExpr, tab.Macros["OBJ"].Class)
	assert.True(t, tab.Undefs["OBJ"])
	// Two different definitions with no #undef: trust neither.
	assert.Equal(t, ClassFragment, tab.Macros["TWICE"].Class)
}

func TestViewLayers(t *testing.T) {
	_, _, own := scan("#undef NAME\n#define NAME \"mine\"\n#define LOCAL 1\n")
	_, _, inc := scan("#define NAME this_player()->query_name()\n#define SHARED int\n#define CLASH 1\n")
	_, _, base := scan("#define CLASH { }\n#define public\n")
	v := &View{Layers: []*Table{own, inc, base}, Override: map[string]Class{"FORCED": ClassStatement}}

	assert.Equal(t, ClassString, v.Lookup("NAME").Class, "the file #undef'd it: its own definition stands")
	assert.Equal(t, ClassType, v.Lookup("SHARED").Class)
	assert.Equal(t, ClassFragment, v.Lookup("CLASH").Class, "conflicting definitions across layers")
	assert.Equal(t, ClassEmpty, v.Lookup("public").Class)
	assert.Equal(t, ClassStatement, v.Lookup("FORCED").Class)
	assert.Nil(t, v.Lookup("nope"))
	assert.Nil(t, (*View)(nil).Lookup("anything"))
}
