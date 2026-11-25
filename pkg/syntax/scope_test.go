package syntax

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scopeAt parses src, which has a '|' where the cursor is, and returns
// the scope of the function the cursor is in, the file and the offset.
func scopeAt(t *testing.T, src string) (*Scope, *File, uint32) {
	t.Helper()
	off := strings.IndexByte(src, '|')
	require.GreaterOrEqual(t, off, 0, "no cursor")
	f := parse(src[:off]+src[off+1:], true)
	for _, s := range f.Scopes() {
		if s.At(uint32(off)) != nil {
			return s, f, uint32(off)
		}
	}
	return nil, f, uint32(off)
}

// visible says what can be named at the cursor: "TYPE NAME@LINE", with
// "param" or "catch" for what is no plain local, the innermost first.
func visible(t *testing.T, src string) string {
	t.Helper()
	s, f, off := scopeAt(t, src)
	if s == nil {
		return "outside"
	}
	var out []string
	for _, l := range s.Visible(off) {
		assert.Same(t, l, s.Lookup(l.Name, off), l.Name)
		text := l.Name
		if l.Type != "" {
			text = l.Type + " " + l.Name
		}
		switch {
		case l.Param:
			text += " param"
		case l.Catch:
			text += " catch"
		}
		out = append(out, text+"@"+f.Tok.Pos(f.Off(l.Ident)).String())
	}
	return strings.Join(out, ", ")
}

func TestScopes(t *testing.T) {
	for src, want := range map[string]string{
		// Parameters, and the variables of the outermost block from
		// their declarators on.
		"void f(int a, string *b) { | }":           "string * b param@1:23, int a param@1:12",
		"void f(int a) { int x, *y; object o; | }": "object o@1:35, int * y@1:25, int x@1:21, int a param@1:12",
		"void f(int a) { | int x; }":               "int a param@1:12",
		"void f(int a) { int x|, y; }":             "int x@1:21, int a param@1:12",
		"void f(int a) { int |x, y; }":             "int a param@1:12",
		"void f(int a) { int x, y|; }":             "int y@1:24, int x@1:21, int a param@1:12",
		"void f(a, b) { | }":                       "b param@1:11, a param@1:8",
		"void f(varargs int a, mixed rest...) {|}": "mixed rest param@1:29, int a param@1:20",
		"void f() {|}":                             "",
		"void f() { int x; }| void g(int a) { }":   "outside",
		"void f(int |a) { }":                       "outside",
		"int g; void f() { | }":                    "",
		"void f(int a); void g() { | }":            "",
		"void f(int a) { } void g(string b) { | }": "string b param@1:33",
		"void f(int a) { int x; }\nvoid g() {\n|}": "",

		// A block is a scope of its own: what it declares hides what is
		// outside, and is gone after it.
		"void f(int a) { int x; { string x; | } }":             "string x@1:33, int a param@1:12",
		"void f(int a) { int x; { string x; } | }":             "int x@1:21, int a param@1:12",
		"void f(int a) { int x; { | string x; } }":             "int x@1:21, int a param@1:12",
		"void f(int a) { { string a; { float a; | } } }":       "float a@1:37",
		"void f(int a) { { string a; { float a; } | } }":       "string a@1:26",
		"void f() { if (1) { int x; | } else { int y; } }":     "int x@1:25",
		"void f() { if (1) { int x; } else { int y; | } }":     "int y@1:41",
		"void f() { while (1) { int x; } for (;;) { | } }":     "",
		"void f() { int i; for (i = 0; i < |3; i++) { } }":     "int i@1:16",
		"void f() { switch (1) { case 1: { int x; | } } }":     "int x@1:39",
		"void f() { switch (1) { case 1: { int x; } | } }":     "",
		"void f() { do { int x; | } while (1); }":              "int x@1:21",
		"void f() { rlimits (1; 2) { int x; | } }":             "int x@1:33",
		"void f() { int x; {|} }":                              "int x@1:16",
		"void f() { int x; {}| }":                              "int x@1:16",
		"void f() { int x; { int y; |} }":                      "int y@1:25, int x@1:16",
		"void f() { int x; { int y; }| }":                      "int x@1:16",
		"void f() { catch { int x; | } : { int y; } }":         "int x@1:24",
		"void f() { catch { int x; } : { int y; | } }":         "int y@1:37",
		"void f() { try { int x; } catch (err) { int y; | } }": "int y@1:45, string err catch@1:34",
		"void f() { try { int x; | } catch (err) { } }":        "int x@1:22",
		"void f() { try { } catch (...) { | } }":               "",
		"void f() { try { } catch (err) { } | }":               "",

		// What was written counts, where DGD would refuse it: a second
		// declaration of a name from its place on, a declaration after a
		// statement, one that is a statement's body.
		"void f(int a) { string a; | }":         "string a@1:24",
		"void f(int a) { | string a; }":         "int a param@1:12",
		"void f() { int x; x = 1; float x; | }": "float x@1:32",
		"void f() { g(); int x; | }":            "int x@1:21",
		"void f() { if (1) int x; | }":          "int x@1:23",

		// A function that is being written: what is declared before the
		// cursor is there, a block that is not closed reaches to the end.
		"void f(int a) { int x; x = |":                     "int x@1:21, int a param@1:12",
		"void f(int a) { int x; { string s; s = | \n":      "string s@1:33, int x@1:21, int a param@1:12",
		"void f(int a) { int x; g(a, | }":                  "int x@1:21, int a param@1:12",
		"void f(int a) { int x; if (a) { int y; x->| } }":  "int y@1:37, int x@1:21, int a param@1:12",
		"void f(int a) { int x; x = ;\n  int y; | }":       "int y@2:7, int x@1:21, int a param@1:12",
		"void f(int a) { int x; {\n | \nvoid g(int b) { }": "int x@1:21, int a param@1:12",
		"void f(int a) { int x;\n | \nvoid g(int b) { }":   "int x@1:21, int a param@1:12",
	} {
		assert.Equal(t, want, visible(t, src), "%s", src)
	}
}

// A statement that did not parse may declare what it names: Unsure says
// so for the places after it in its block and in the blocks inside.
func TestScopeUnsure(t *testing.T) {
	for src, want := range map[string]string{
		"void f() { int x = ; | }":                "int x = ;",
		"void f() { | int x = ; }":                "",
		"void f() { int x = ; { { | } } }":        "int x = ;",
		"void f() { { int x = ; } | }":            "",
		"void f() { { int x = ; } { | } }":        "",
		"void f() { int x = ; int y = ; g(); | }": "g();, int x = ;, int y = ;",
		"void f() { g(); | }":                     "",
		"void f() { int x = ; int y; | }":         "int x = ;",
	} {
		s, f, off := scopeAt(t, src)
		require.NotNil(t, s, src)
		var got []string
		s.Unsure(off, func(start, end uint32) bool {
			got = append(got, string(f.Tok.Src[start:end]))
			return false
		})
		sort.Strings(got)
		assert.Equal(t, want, strings.Join(got, ", "), "%s", src)
		assert.Equal(t, want != "", s.Unsure(off, func(uint32, uint32) bool { return true }), "%s", src)
	}
}
