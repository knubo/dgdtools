package lint_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/lint"
)

// lintDoc lints the file at lib path rel of a memProject made of files.
func lintDoc(t *testing.T, files map[string]string, rel string) *lint.Document {
	t.Helper()
	prj, m := memProject(files)
	l, err := lint.New(prj, m)
	require.NoError(t, err)
	l.Scan(context.Background())
	doc, err := l.Lint(context.Background(), "/lib/"+rel, lint.Options{})
	require.NoError(t, err)
	return doc
}

// applied is the document's text with the fix made, or "none".
func applied(doc *lint.Document, fix *diag.Fix) string {
	if fix == nil {
		return "none"
	}
	src := string(doc.File.Src)
	for i := len(fix.Edits) - 1; i >= 0; i-- {
		e := fix.Edits[i]
		src = src[:e.Start] + e.NewText + src[e.End:]
	}
	return fix.Title + "\n" + src
}

func TestPrototype(t *testing.T) {
	for _, tc := range []struct {
		name, src, at, want string
		extra               map[string]string
	}{{
		name: "above the function of the first call and its comment",
		src:  "int x;\n\n/* f does it */\nvoid f() {\n    g(1);\n}\n\nvoid h() { g(2); }\n\nstatic int *g(int n, string s...) { return 0; }\n",
		at:   "g(2)",
		want: "Add the prototype of 'g' before its first call\nint x;\n\nstatic int *g(int n, string s...);\n\n/* f does it */\nvoid f() {\n    g(1);\n}\n\nvoid h() { g(2); }\n\nstatic int *g(int n, string s...) { return 0; }\n",
	}, {
		name: "a comment after code is not the function's",
		src:  "int x; // the x\nvoid f() { g(); }\nvoid g() {}\n",
		at:   "g()",
		want: "Add the prototype of 'g' before its first call\nint x; // the x\nvoid g();\n\nvoid f() { g(); }\nvoid g() {}\n",
	}, {
		name: "declared before",
		src:  "void g();\nvoid f() { g(); }\nvoid g() {}\n",
		at:   "g()",
		want: "none",
	}, {
		name: "defined before",
		src:  "void g() {}\nvoid f() { g(); }\n",
		at:   "g()",
		want: "none",
	}, {
		name: "inherited",
		src:  "inherit \"/std/base\";\nvoid f() { query(1); }\nint query(int a) { return 0; }\n",
		at:   "query(1)",
		want: "none",
	}, {
		name: "the auto object's",
		src:  "void f() { object_name(); }\nstring object_name() { return \"\"; }\n",
		at:   "object_name()",
		want: "none",
	}, {
		name:  "a header's",
		src:   "#include \"defs.h\"\nvoid f() { g(); }\nvoid g() {}\n",
		at:    "g()",
		extra: map[string]string{"obj/defs.h": "void g();\n"},
		want:  "none",
	}, {
		name: "no return type",
		src:  "void f() { g(); }\ng() {}\n",
		at:   "g()",
		want: "none",
	}, {
		name: "a macro defined after the place",
		src:  "void f() { g(); }\n#define T int\nT g() { return 0; }\n",
		at:   "g()",
		want: "none",
	}, {
		name: "a header's macro",
		src:  "#include <std.h>\nvoid f() { g(); }\nstatus g() { return 0; }\n",
		at:   "g()",
		want: "Add the prototype of 'g' before its first call\n#include <std.h>\nstatus g();\n\nvoid f() { g(); }\nstatus g() { return 0; }\n",
	}, {
		name: "not a call",
		src:  "void f() { g(); }\nvoid g() {}\n",
		at:   "void f",
		want: "none",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{"obj/t.c": tc.src}
			for k, v := range tc.extra {
				files[k] = v
			}
			doc := lintDoc(t, files, "obj/t.c")
			at := strings.Index(tc.src, tc.at)
			require.GreaterOrEqual(t, at, 0)
			assert.Equal(t, tc.want, applied(doc, doc.Prototype(uint32(at))))
		})
	}
}

func TestPathFixes(t *testing.T) {
	files := map[string]string{
		"include/sub/deep.h":      "#define DEEP 1\n",
		"players/ann/defs.h":      "#define ANN 1\n",
		"players/ann/obj/sword.c": "int x;\n",
		"players/ann/room/hall.c": "#include \"defs.h\"\n#include <deep.h>\ninherit \"/players/ann/sword\";\n" +
			"void f() { clone_object(\"/players/ann/sword.c\"); }\n#include \"nothing.h\"\n",
		"d/a/dup.h": "", "d/b/dup.h": "",
		"d/c/x.c": "#include \"dup.h\"\n",
	}
	doc := lintDoc(t, files, "players/ann/room/hall.c")
	var got []string
	for _, f := range doc.Diagnostics {
		fixes := doc.PathFixes(context.Background(), f)
		if len(fixes) == 0 {
			got = append(got, f.Rule+": none")
			continue
		}
		for _, fix := range fixes {
			require.Equal(t, diag.Unsafe, fix.Applicability)
			e := fix.Edits[0]
			got = append(got, f.Rule+": "+fix.Title+": "+string(doc.File.Src[e.Start:e.End])+" -> "+e.NewText)
		}
	}
	assert.Equal(t, []string{
		`include-not-found: Include "/players/ann/defs.h": "defs.h" -> "/players/ann/defs.h"`,
		`include-not-found: Include <sub/deep.h>: <deep.h> -> <sub/deep.h>`,
		`target-object-missing: Name /players/ann/obj/sword: /players/ann/sword -> /players/ann/obj/sword`,
		`target-object-missing: Name /players/ann/obj/sword: /players/ann/sword.c -> /players/ann/obj/sword.c`,
		`include-not-found: none`,
	}, got)

	// Two files of the name: none.
	doc = lintDoc(t, files, "d/c/x.c")
	require.Len(t, doc.Diagnostics, 1)
	assert.Equal(t, "include-not-found", doc.Diagnostics[0].Rule)
	assert.Empty(t, doc.PathFixes(context.Background(), doc.Diagnostics[0]))
}
