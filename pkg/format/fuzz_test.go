package format

import (
	"errors"
	"strings"
	"testing"

	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// fuzzSeeds are texts with the things the formatter has to get right:
// headers, literals in literals, comments of every kind, conditionals,
// statements the parser gives up on.
var fuzzSeeds = []string{
	"static void create() {\n    ::create();\n}\n",
	"mapping m = ([ \"a\": ({ 1,\n2 }), \"b\": ([ ]), ]);\n",
	"void f()\n{\n    x = \"a\" \"b\";\n    if (x = 1) }\n",
	"f(){\nif(1)}",
	"int g(int a, varargs string b) {\n#ifdef FOO\n  return a ? b : 1;\n#else\n  switch (a) { case 1: break; default: a++; }\n#endif\n}\n",
	"/* multi\n   line\n\t*/ int x; // line\n/* dgdtools-ignore-start format */\nint   y;\n/* dgdtools-ignore-end format */\n",
	"#define M(a) \\\n   a + 1\nvoid f() { for (;;) { do x--; while (x); } catch (y) : { } rlimits (1; 2) { } }\r\n",
	"inherit \"/std/base\";\nprivate nomask int *f(object o, mixed m...) { return ({ o->g(m...), &m, -1, !o }); }\n",
}

// FuzzFormat holds the formatter to what it promises of any text: a
// result it has proven (the same tokens and comments, a fixed point), or
// a refusal because the text does not tokenize; never a failed gate and
// never a crash. The edits of a result reproduce it, and formatting one
// line of the text is held to the same.
//
// `go test` runs the seeds; `go test -fuzz FuzzFormat ./pkg/format`
// looks for more.
func FuzzFormat(f *testing.F) {
	for _, seed := range fuzzSeeds {
		f.Add(seed, uint8(0))
	}
	f.Fuzz(func(t *testing.T, src string, pick uint8) {
		// A carriage return by itself is a line break to the lexer and
		// to no editor. What rewriting line endings does to a text that
		// has one the gate refuses; that is not looked for here.
		if strings.Contains(strings.ReplaceAll(src, "\r\n", ""), "\r") {
			return
		}
		file := lexer.Lex("/fuzz.c", []byte(src), token.Dialect{SlashSlash: true})
		o := []Options{{}, {IndentStyle: Tabs}, {LineEndings: LF}, {LineEndings: CRLF, IndentWidth: 2}}[pick%4]
		out, err := Format(file, o)
		if errors.Is(err, ErrIllegal) {
			return
		}
		if err != nil {
			t.Fatalf("%v\n%q", err, src)
		}
		got := []byte(src)
		edits := Edits(file, out)
		for i := len(edits) - 1; i >= 0; i-- {
			e := edits[i]
			got = append(got[:e.Start:e.Start], append([]byte(e.NewText), got[e.End:]...)...)
		}
		if string(got) != string(out) {
			t.Fatalf("the edits give\n%q, the formatter\n%q, of\n%q", got, out, src)
		}
		line := 1 + int(pick)%len(file.Lines)
		lines := []LineRange{{First: line, Last: line + int(pick)%3}}
		out, err = Lines(file, o, lines)
		if err != nil {
			t.Fatalf("line %d: %v\n%q", line, err, src)
		}
		if err := editsWithin(file, out, lines); err != nil {
			t.Fatalf("%v\n%q", err, src)
		}
	})
}
