package lint_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// FuzzLint lints any text as an object of a small lib, with every rule
// on: no rule and no analysis crashes on it, and every finding and every
// fix lies inside the text, and the ';' a syntax error offers mends it.
//
// `go test` runs the seeds; `go test -fuzz FuzzLint ./pkg/lint` looks for
// more.
func FuzzLint(f *testing.F) {
	for _, seed := range []string{
		"#include <std.h>\ninherit STD \"base\";\nvoid create() { ::create(); \"/std/base\"->hidden(); this_object()->nope(1, 2); call_out(\"cb\", 1); }\n",
		"void f() {\n    x = \"a\" \"b\";\n    if (x = 1) }\n",
		"void f() {\n    a = 1\n    b = 2;\n}\nint g\nint h;\n",
		"/* dgdtools-ignore lint/E001, format */\nint f(int a, varargs string b) { sscanf(b, \"%d %s\", a); return; x == 1; }\n",
		"#if TRUE\ninherit \"/std/nope\";\n#elif 1\n#undef STD\n#endif\nstatus g() { return clone_object(\"/sys/auto\")->object_name(1); }\n",
		"/* dgdtools-ignore-start lint/syntax-error */\nvoid f() { goto x; (: f :); new nil function atomic try ...\n",
	} {
		f.Add(seed)
	}
	prj, m := memProject(nil)
	f.Fuzz(func(t *testing.T, src string) {
		const path = "/lib/obj/fuzz.c"
		ov := vfs.NewOverlay(m)
		ov.Set(path, []byte(src))
		l, err := lint.New(prj, ov)
		require.NoError(t, err)
		doc, err := l.Lint(context.Background(), path, lint.Options{Select: []string{"all"}})
		if err != nil {
			t.Fatalf("%v\n%q", err, src)
		}
		for _, e := range doc.Errors {
			t.Fatalf("%v\n%q", e, src)
		}
		n := uint32(len(src))
		for _, d := range doc.Diagnostics {
			if d.End < d.Start || d.End > n || d.Line < 1 || d.Col < 1 || d.EndLine < d.Line {
				t.Fatalf("a finding outside the text: %+v\n%q", d, src)
			}
			for _, fix := range d.Fixes {
				for _, e := range fix.Edits {
					if e.End < e.Start || e.End > n {
						t.Fatalf("an edit outside the text: %+v\n%q", d, src)
					}
				}
			}
		}
		if crashed := l.Index().Snapshot().Crashed(); len(crashed) > 0 {
			t.Fatalf("%v\n%q", crashed[0], src)
		}
		// The ';' a syntax error offers is what the parser assumed: with
		// it the file has one syntax error fewer.
		syntaxErrors := func(doc *lint.Document) (n int, fix *diag.Fix) {
			for i := range doc.Diagnostics {
				if d := &doc.Diagnostics[i]; d.Rule == "syntax-error" {
					if n++; fix == nil && len(d.Fixes) > 0 {
						fix = &d.Fixes[0]
					}
				}
			}
			return n, fix
		}
		if before, fix := syntaxErrors(doc); fix != nil {
			e := fix.Edits[0]
			ov.Set(path, []byte(src[:e.Start]+e.NewText+src[e.End:]))
			l, err := lint.New(prj, ov)
			require.NoError(t, err)
			again, err := l.Lint(context.Background(), path, lint.Options{Select: []string{"all"}})
			require.NoError(t, err)
			if after, _ := syntaxErrors(again); after >= before {
				t.Fatalf("%d syntax errors, %d after the fix %q at %d\n%q", before, after, e.NewText, e.Start, src)
			}
		}
	})
}
