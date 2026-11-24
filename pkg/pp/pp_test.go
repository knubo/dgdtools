package pp

import (
	"strings"
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
		got := Classify(Tokens(strings.TrimSpace(CleanDirective([]byte(tt.body))), lexer.Default))
		assert.Equal(t, tt.want.String(), got.String(), "%q", tt.body)
	}
}

// scan reads src on its own, with the given names predefined.
func scan(src string, defined ...string) (*token.File, *Selection, *Table) {
	f := lexer.Lex("t.c", []byte(src), lexer.Default)
	d := map[string]bool{}
	for _, n := range defined {
		d[n] = true
	}
	env := NewEnv(d, nil)
	sel := env.Scan(f, nil)
	return f, sel, env.Own()
}

func sigText(f *token.File, sel *Selection) string {
	s := ""
	for _, i := range sel.Sig {
		s += string(f.Text(f.Tokens[i])) + " "
	}
	return s
}

func guessedText(f *token.File, sel *Selection) string {
	s := ""
	for _, i := range sel.Sig {
		if sel.Guessed(i) {
			s += string(f.Text(f.Tokens[i]))
		}
	}
	return s
}

func TestScanSelects(t *testing.T) {
	// Decided: #if 0 / 1, predefined names, names defined earlier in the
	// file. A file read on its own cannot know that a name is defined
	// nowhere: that takes the first branch and is marked guessed.
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
#else
o
#endif
`, "__DGD__")
	assert.Equal(t, "a c d g i j l o ", sigText(f, sel))
	assert.Equal(t, "j", guessedText(f, sel), "only the undecided branch is a guess")
	if assert.Len(t, sel.Conds, 8) {
		c := sel.Conds[2] // #ifndef __DGD__ / #elif 1
		assert.Len(t, c.Arms, 2)
		assert.False(t, c.Arms[0].Taken)
		assert.True(t, c.Arms[1].Taken)
		assert.Equal(t, token.Directive, f.Tokens[c.End].Kind)
	}

	// An include guard is decided, so a header's contents are certain.
	f, sel, _ = scan("/* header */\n#ifndef X_H\n#define X_H\nint a;\n#endif\n")
	assert.Equal(t, "", guessedText(f, sel))
	// ...but `#ifndef X` that is not a guard is a guess.
	f, sel, _ = scan("int z;\n#ifndef X\nint a;\n#endif\n")
	assert.True(t, sel.Guessed(sel.Sig[len(sel.Sig)-1]))
}

func TestScanConditions(t *testing.T) {
	// The one evaluator reads the conditions: arithmetic, comparison,
	// logic, defined, and macros that name macros.
	f, sel, _ := scan(`#define LEVEL 3
#define LIMIT (LEVEL + 1)
#define NAME "x"
#if LIMIT == 4 && LEVEL > 2
a
#endif
#if LEVEL << 1 != 6 || !defined(LEVEL)
b
#endif
#if defined LEVEL && (LIMIT - 4 ? 0 : 1)
c
#endif
#if 0 && UNKNOWN(1)
d
#endif
#if 1 || UNKNOWN
e
#endif
#if UNKNOWN && 1
f
#endif
#if NAME
g
#endif
#if 0x10 == 16 && 'a' == 97
h
#endif
`)
	assert.Equal(t, "a c e f g h ", sigText(f, sel))
	assert.Equal(t, "fg", guessedText(f, sel), "what cannot be evaluated is a guess")
}

func TestScanOrder(t *testing.T) {
	// A condition sees the environment as it is at that line: what was
	// read before the file, what an #include brought in by then, and the
	// file's own directives so far.
	header := func(src string) *Table { _, _, tab := scan(src); return tab }
	base := header("#define BASE 1\n#define DEBUG 0\n")
	inc := header("#ifndef INC_H\n#define INC_H\n#define FROM_INC 2\n#endif\n")

	src := `#ifdef FROM_INC
early
#endif
#include "inc.h"
#ifdef FROM_INC
late
#endif
#if BASE && !DEBUG && FROM_INC == 2
values
#endif
#ifdef NOWHERE
no
#else
yes
#endif
#undef BASE
#ifdef BASE
gone
#endif
`
	f := lexer.Lex("t.c", []byte(src), lexer.Default)
	env := NewEnv(nil, nil)
	env.Add(base)
	env.Complete = true
	sel := env.Scan(f, func(i Include) {
		assert.Equal(t, "inc.h", i.Name)
		env.Add(inc)
	})
	assert.Equal(t, "late values yes ", sigText(f, sel))
	assert.Equal(t, "", guessedText(f, sel), "in a complete environment every one of these is decided")

	// The same file where an #include could not be read: a name defined
	// nowhere that was seen may be defined there.
	env = NewEnv(nil, nil)
	env.Add(base)
	env.Complete = true
	sel = env.Scan(f, func(Include) { env.Complete = false })
	assert.Equal(t, "late values no ", sigText(f, sel))
	assert.Equal(t, "latevaluesno", guessedText(f, sel))

	// And the same text as a header: whoever includes it may define the
	// name first. What it does see defined still decides.
	env = NewEnv(nil, nil)
	env.Add(base)
	env.Complete, env.Header = true, true
	sel = env.Scan(f, func(Include) { env.Add(inc) })
	assert.Equal(t, "early late values no ", sigText(f, sel))
	assert.Equal(t, "earlyno", guessedText(f, sel))
}

func TestScanUnsure(t *testing.T) {
	// What a header defines under a condition it cannot decide is neither
	// defined nor undefined for the files that include it.
	_, _, hdr := scan(`#ifdef MAYBE
#define A 1
#undef B
#else
#define C 1
#endif
#if 0
#define NEVER 1
#endif
#define SURE 1
`)
	assert.Equal(t, map[string]bool{"A": true, "B": true, "C": true}, hdr.Unsure)

	f := lexer.Lex("t.c", []byte(`#ifdef A
a
#endif
#ifndef C
c
#endif
#ifdef NEVER
never
#endif
#if SURE
sure
#endif
`), lexer.Default)
	env := NewEnv(nil, nil)
	env.Add(hdr)
	env.Complete = true
	sel := env.Scan(f, nil)
	assert.Equal(t, "a c sure ", sigText(f, sel))
	assert.Equal(t, "ac", guessedText(f, sel))
}

func TestScanArms(t *testing.T) {
	src := "a\n#ifdef X\nb\n#if 0\nc\n#else\nd\n#endif\n#else\ne\n#endif\nf\n"
	f := lexer.Lex("t.c", []byte(src), lexer.Default)
	first := NewEnv(nil, nil).Scan(f, nil)
	assert.Equal(t, "a b d f ", sigText(f, first))
	if assert.Len(t, first.Conds, 2) {
		outer, inner := first.Conds[0], first.Conds[1]
		// Take the other arm of each.
		sel := NewEnv(nil, nil).ScanArms(f, map[int]bool{
			outer.Arms[0].Dir: true, outer.Arms[1].Dir: false,
			inner.Arms[0].Dir: true, inner.Arms[1].Dir: false,
		})
		assert.Equal(t, "a b c f ", sigText(f, sel))
		sel = NewEnv(nil, nil).ScanArms(f, map[int]bool{outer.Arms[0].Dir: false, outer.Arms[1].Dir: true})
		assert.Equal(t, "a e f ", sigText(f, sel))
		assert.Equal(t, "", guessedText(f, sel))
	}
}

func TestScanDirectives(t *testing.T) {
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
#define BRIEF 1
#undef BRIEF
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
	assert.Len(t, tab.Macros["MULTI"].Toks, 3)
	assert.False(t, tab.Macros["SPACED"].FuncLike, "a space before '(' makes it object-like")
	// #undef followed by #define is a plain redefinition.
	assert.Equal(t, ClassExpr, tab.Macros["OBJ"].Class)
	assert.False(t, tab.Macros["OBJ"].Undefined)
	assert.True(t, tab.Undefs["OBJ"])
	// Two different definitions with no #undef: trust neither.
	assert.Equal(t, ClassFragment, tab.Macros["TWICE"].Class)
	// A macro for part of the file only.
	assert.True(t, tab.Macros["BRIEF"].Undefined)
	assert.Equal(t, ClassFragment, tab.Macros["BRIEF"].Class)
	assert.Empty(t, tab.Unsure)
}

func TestLookup(t *testing.T) {
	_, _, inc := scan("#define NAME this_player()->query_name()\n#define SHARED int\n#define CLASH 1\n")
	_, _, base := scan("#define CLASH { }\n#define public\n")
	f := lexer.Lex("t.c", []byte("#undef NAME\n#define NAME \"mine\"\n#define LOCAL 1\n"), lexer.Default)
	env := NewEnv(nil, map[string]Class{"FORCED": ClassStatement})
	env.Add(base, inc)
	env.Scan(f, nil)

	assert.Equal(t, ClassString, env.Lookup("NAME").Class, "the file #undef'd it: its own definition stands")
	assert.Equal(t, ClassType, env.Lookup("SHARED").Class)
	assert.Equal(t, ClassFragment, env.Lookup("CLASH").Class, "conflicting definitions across files")
	assert.Equal(t, ClassEmpty, env.Lookup("public").Class)
	assert.Equal(t, ClassStatement, env.Lookup("FORCED").Class)
	assert.Nil(t, env.Lookup("nope"))
	assert.Nil(t, (*Env)(nil).Lookup("anything"))

	for name, want := range map[string][2]bool{
		"NAME": {true, true}, "SHARED": {true, true}, "FORCED": {true, true}, "nope": {false, false},
	} {
		defined, known := env.Defined(name)
		assert.Equal(t, want, [2]bool{defined, known}, name)
	}
	env.Complete = true
	_, known := env.Defined("nope")
	assert.True(t, known, "complete: a name defined nowhere is not defined")
}

func TestEvalString(t *testing.T) {
	_, _, hdr := scan(`#define DIR "/d/Wild/"
#define ROOMS (DIR + "rooms/")
#define ALIAS ROOMS
#define TWO "a" "b"
#define FN(x) DIR + x
#define ME this_object()
#define SELF (ME)
#define N 3
#define LOOP LOOP
`)
	env := NewEnv(nil, nil)
	env.Add(hdr)
	str := func(expr string) (string, bool) { return env.EvalString(Tokens(expr, lexer.Default)) }

	for expr, want := range map[string]string{
		`"plain"`:                   "plain",
		`"a\\b\"c"`:                 `a\b"c`,
		`DIR + "x"`:                 "/d/Wild/x",
		`ROOMS + "cave"`:            "/d/Wild/rooms/cave",
		`(ALIAS) + ("a" "b")`:       "/d/Wild/rooms/ab",
		`TWO "c"`:                   "abc",
		`1 ? DIR : "no"`:            "/d/Wild/",
		`"a" + (N > 2 ? "b" : "c")`: "ab",
	} {
		got, ok := str(expr)
		assert.True(t, ok, expr)
		assert.Equal(t, want, got, expr)
	}
	for _, expr := range []string{
		``, `DIR +`, `"a" DIR`, `name`, `DIR + name`, `FN("x")`, `"a" + 1`, `N`, `LOOP`,
		`"tab\t"`, `"a" + query()`, `ME`,
	} {
		_, ok := str(expr)
		assert.False(t, ok, expr)
	}

	texts := func(toks []Tok) (s []string) {
		for _, t := range toks {
			s = append(s, t.Text)
		}
		return s
	}
	assert.Equal(t, []string{"this_object", "(", ")"}, texts(env.Expansion("SELF")), "parentheses stripped, names followed")
	assert.Equal(t, []string{"DIR", "+", `"rooms/"`}, texts(env.Expansion("ALIAS")))
	assert.Nil(t, env.Expansion("FN"))
	assert.Nil(t, env.Expansion("LOOP"))
	assert.Nil(t, env.Expansion("nope"))
}

func TestFileTokens(t *testing.T) {
	f := lexer.Lex("t.c", []byte("inherit DIR /* the */\n  + \"room\";\n"), lexer.Default)
	var got []string
	for _, tok := range FileTokens(f, 2, len(f.Tokens)-4) {
		got = append(got, tok.Text)
	}
	assert.Equal(t, []string{"DIR", "+", `"room"`}, got)
}
