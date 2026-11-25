package syntax

import (
	"testing"

	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// FuzzParse holds the lexer and the parser to their invariants on any
// text: the tokens are the text, the parser does not panic, every node's
// span is ordered and inside the file, and a function's body lies inside
// the function.
//
// `go test` runs the seeds; `go test -fuzz FuzzParse ./pkg/syntax` looks
// for more.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"inherit \"/std/base\";\nstatic int x, *y;\nvoid create() { ::create(); x = y[0..1][2]; }\n",
		"#include <std.h>\n#if 0\nint f(\n#else\nint f() { return ({ 1, ([ \"a\": 2 ]) }); }\n#endif\n",
		"void f() { for (int i = 0; i < n; i++) g(i); if (x) else }\n",
		"mixed f(varargs mixed a...) { catch { a->b(c...); } : { return (: f :); } }\n",
		"void f() { switch (x) { case 'a'..'z': case \"s\": default: break; } rlimits (1; 2) { } }\n",
		"FOO(int) BAR\nint f() { return x ? y : z ? 1 : 2.5e3 + '\\x41' + 0x1f; }\n",
		"void f() {\n",
		"})])\"'\\",
	} {
		f.Add(seed, false)
	}
	f.Fuzz(func(t *testing.T, src string, complete bool) {
		file := lexer.Lex("/fuzz.c", []byte(src), token.Dialect{SlashSlash: true})
		if err := file.CheckRoundTrip(); err != nil {
			t.Fatalf("%v\n%q", err, src)
		}
		env := pp.NewEnv(nil, nil)
		env.Complete = complete
		tree := Parse(file, Options{Env: env, Sel: env.Scan(file, nil)})
		n := len(file.Tokens)
		Inspect(tree, func(node Node) bool {
			a, b := node.Span()
			if a < 0 || b < a || b >= n {
				t.Fatalf("%T has span [%d,%d] of %d tokens\n%q", node, a, b, n, src)
			}
			if fn, ok := node.(*FuncDecl); ok && fn.Body != nil && (fn.Body.First < fn.First || fn.Body.Last > fn.Last) {
				t.Fatalf("a function's body is outside the function\n%q", src)
			}
			return true
		})
	})
}
