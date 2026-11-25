package syntax

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// parse parses src in an environment made of the file's own #defines.
func parse(src string, complete bool) *File {
	f := lexer.Lex("test.c", []byte(src), lexer.Default)
	env := pp.NewEnv(map[string]bool{"__DGD__": true}, map[string]pp.Class{"public": pp.ClassEmpty})
	env.Complete = complete
	return Parse(f, Options{Env: env, Sel: env.Scan(f, nil)})
}

// errs renders the reportable (untainted) errors as "line:col msg".
func errs(f *File) []string {
	var out []string
	for _, e := range f.Errors {
		if e.Tainted {
			continue
		}
		t := f.Tok.Tokens[e.Tok]
		off := t.Off
		if e.AtPrevEnd {
			off = t.End
		}
		p := f.Tok.Pos(off)
		out = append(out, fmt.Sprintf("%d:%d %s", p.Line, p.Col, e.Msg))
	}
	return out
}

func body(stmts string) string { return "void f() {\n" + stmts + "\n}\n" }

func TestValidPrograms(t *testing.T) {
	srcs := map[string]string{
		"decls": `
#define PREFIX "/std/"
inherit "/std/room";
private inherit base "/std/base";
inherit OBJ (PREFIX + "thing");
int a, *b, c(int x, varargs string y), **d;
static mapping m;
object "/std/thing" typed;
public void create() { ::create(); base::create(); }
static nomask varargs mixed *query(string s, varargs int n, mixed rest...) { return ({ }); }
typeless(a, b) { return a + b; }
string operator+ (string s) { return s; }
int operator[] (int i) { return i; }
void none(void) { }
`,
		"statements": body(`
    int i, *arr; string s; mapping m; object ob;
    if (i) i = 1; else if (!i) { i = 2; } else i = 3;
    while (i--) continue;
    do { i++; } while (i < 10);
    for (i = 0, s = ""; i < 10; i++) ;
    for (;;) break;
    switch (i) { case 1: case 2..5: i = 0; break; case 'a': default: break; }
    rlimits (10; 100) { i = 1; }
    catch { i = 1; } : { i = 2; }
    catch { i = 1; }
    lab: i = 1;
    goto lab;
    return;
`),
		"expressions": body(`
    mixed x, y; mixed *a; mapping m; object ob; string s; int i;
    x = ({ 1, 2, ({ 3, }), });
    m = ([ "a" : 1, "b" : ({ }), ]);
    m = ([ ]);
    x = a[0]; x = a[1..2]; x = a[..2]; x = a[1..]; x = a[..];
    x = ob->foo(1, 2)->bar()[0];
    x = "/std/x"->query(a...);
    x = call_other(ob, "fn", 1);
    x = (int) y + (string *) a + (mixed) x;
    x = i ? 1 : i ? 2 : 3;
    x = y = i += 2;
    x = -i + !i + ~i + ++i + --i + i++ + i--;
    x = i << 2 >> 1 | 3 & 4 ^ 5;
    x = i == 1 && i != 2 || i <= 3 && i >= 4 || i < 5 && i > 6;
    x = i * 2 / 3 % 4 - 1;
    x = catch(i = 1, i = 2);
    x = new "/obj/thing";
    x = new object "/obj/thing" (1, 2);
    x = ob <- "/std/room";
    x = ::query(1);
    x = sizeof(a), y = 2;
    x = 1.5 + .5 + 1e3 + 0x1F + 'a' + '\n' + nil;
    (x);
    x = "a" + "b" "c";
    x = (: this_object(), "fn" :);
`) + "\nint foo(int a) { return a; }\n",
		"macros": `
#define public
#define status int
#define function mixed *
#define IW "/d/Wild/"
#define MAX(a, b) ((a) > (b) ? (a) : (b))
#define CAST(t, x) ((t) (x))
#define FAIL(s) { write(s); return 0; }
#define FOREACH(i, a) for (i = 0; i < sizeof(a); i++)
#define TP this_player()
#define NOTHING
#define DBG(x)
public status ok() { return 1; }
function query_fp() { return ({ }); }
status flag;
int test(status arg, function f) {
    status s; function g; int i; mixed *a;
    s = MAX(1, 2);
    s = CAST(int, s);
    if (!s) FAIL("no")
    FAIL("really")
    FOREACH(i, a) { s += a[i]; }
    FOREACH(i, a) s += a[i];
    TP->foo(IW + "x");
    NOTHING
    DBG("x")
    DBG(int garbage here)
    return (status) 1;
}
`,
		"conditionals": `
void f() {
#ifdef __DGD__
    if (a &&
#else
    if (b &&
#endif
        c) { d(); }
#if 0
    this is ( not code at all 'x
#endif
}
`,
	}
	for name, src := range srcs {
		t.Run(name, func(t *testing.T) {
			f := parse(src, true)
			assert.Empty(t, errs(f), "unexpected syntax errors")
			assert.False(t, f.Partial)
		})
	}
}

func TestCastleMissingSemicolon(t *testing.T) {
	src := `mapping connections;
void create() {
    connections["Quair"] = ({
        IW+"Mloria/east4", "southeast",
        IW+"Quair/room/infrontofcastle",
        })
    connections["Repairshop"] = ({
        IW+"Quests/Smalltree/room/slope","enter",
        });
}
int after() { return 1; }
`
	for _, complete := range []bool{true, false} {
		f := parse(src, complete)
		got := errs(f)
		require.Len(t, got, 1, "complete=%v: %v", complete, got)
		assert.Equal(t, "6:11 missing ';' before 'connections'", got[0])
		assert.False(t, f.Partial, "one missing ';' must not cost the symbol table")
		assert.Len(t, f.Decls, 3)
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string // substrings, in order
	}{
		{"missing semi same line", body("x = 1 y = 2;"), []string{"2:7 expected ';' before 'y'"}},
		{"missing semi before brace", body("return 1"), []string{"2:9 missing ';' before '}'"}},
		{"two independent", body("a = 1\nb = 2\nc = 3;"), []string{"2:6 missing ';'", "3:6 missing ';'"}},
		{"unclosed paren", body("foo(1, 2;\nx = 1;"), []string{"expected ')' to close '(' opened on line 2"}},
		{"bad mapping", "mapping m() { return ([ \"bronze\" : , ]); }", []string{"unexpected ','"}},
		{"trailing comma in call", body("foo(a,);"), []string{"unexpected ')'"}},
		{"arrow without call", body("x = ob->name;"), []string{"expected '(' after '->name'"}},
		{"else before brace", "void f() {\n if (x)\n  g();\n else\n}\nint h() { return 1 }\n", []string{"5:1 unexpected '}'", "6:19 missing ';'"}},
		{"if before brace", "void f() {\n while (x) {\n  if (y)\n }\n z();\n}\nint h() { return 1 }\n", []string{"4:2 unexpected '}'", "7:19 missing ';'"}},
		{"else without if", body("else x = 1;"), []string{"'else' without a matching 'if'"}},
		{"missing close brace", "void f() {\n if (x) {\n  y();\n}\n", []string{"missing '}' to close '{' opened on line 1"}},
		{"nested function", "void f() {\n if (x) {\n  y();\n\nstatic int g(int a) {\n return a;\n}\nvoid h() { z() }\n",
			[]string{"5:1 function 'g' begins inside the body of 'f'"}},
		{"extra close brace", "void f() {\n x();\n}\n}\nint g() { return 1 }\n", []string{"4:1 unmatched '}'"}},
		{"stray top-level statement", "int x;\nx = 5;\nvoid f() { }\n", []string{"2:1 unexpected 'x' at top level"}},
		{"case before brace", body("switch (x) { case 1: x = 2; default: }"), []string{"label must be followed by a statement"}},
		{"function pointer without closures", body("x = &g(1);"), []string{"unexpected '&'"}},
		{"inherit of undefined macro", "inherit NOT_DEFINED;\nvoid f() { }\n", []string{"1:9 inherit path must be a string constant, but 'NOT_DEFINED' is not a defined macro"}},
		{"unnamed parameter", "int f(int);\n", []string{"expected parameter name"}},
		{"typeless prototype", "f();\n", []string{"has no body"}},
		{"unterminated string", "void f() {\n write(\"abc);\n x = 1;\n}\n", []string{"unterminated string literal"}},
		{"illegal char", "void f() {\n x = 1 @ 2;\n}\n", []string{"illegal character"}},
		{"unknown type complete view", "status f() { return 1; }\n", []string{"1:1 unknown type 'status'"}},
		{"decided branches", "#define ON 1\n#define FLAG\nvoid f() {\n#if ON\n a = 1\n#endif\n#ifdef FLAG\n b = 2\n#endif\n#ifndef __DGD__\n garbage here (\n#else\n c = 3\n#endif\n}\n",
			[]string{"5:7 missing ';'", "8:7 missing ';'", "13:7 missing ';'"}},
		{"cascade after a skip is dropped", body("x = foo(1, 2;\nelse y();\nelse z();\nok();\nq = 1\nr = 2;"), []string{"expected ')'", "6:6 missing ';'"}},
		{"for with declaration", body("for (int t = 0; t < n; t++) {\n  x = t\n}\nok();\ny = 1\nz = 2;"), []string{"2:6 unexpected 'int'", "6:6 missing ';'"}},
		{"expr macro without semi", "#define MAX(a,b) ((a)>(b)?(a):(b))\nvoid f() {\n MAX(1,2)\n g();\n}\n", []string{"3:10 missing ';'"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := errs(parse(tt.src, true))
			require.Len(t, got, len(tt.want), "%v", got)
			for i, w := range tt.want {
				assert.Contains(t, got[i], w)
			}
		})
	}
}

func TestMacroTolerance(t *testing.T) {
	clean := []struct {
		name     string
		src      string
		complete bool
	}{
		{"fragment macro", "#define TPQ this_player()->\nvoid f() { TPQ query_name(); x = 1; }\n", true},
		{"brace macros", "#define BEGIN {\n#define END }\nvoid f() BEGIN x(); END\n", true},
		{"conflicting defs", "#ifdef A\n#define LOG(x) write(x);\n#else\n#define LOG(x)\n#endif\n#undef LOG\nvoid f() { LOG(1) LOG(2) }\n", true},
		{"unseen statement macro", "void f() {\n FAIL(\"x\")\n return;\n}\n", false},
		{"unseen type macro", "status f(status a) {\n status b;\n return a;\n}\nstatus *g() { return 0; }\n", false},
		{"unseen specifier macro", "PUBLIC void f() { }\nprivate PUBLIC nomask int g() { return 1; }\n", false},
		{"unseen caps in expression", "void f() {\n x = FOO BAR;\n}\n", false},
		{"dead branch garbage", "#if 0\n)))) 'unterminated\n#else\nvoid f() { }\n#endif\n", true},
		{"macro call with type arg", "void f() {\n x = NEW(int, 3);\n}\n", false},
		{"top-level macro", "#define DEFUN(n) int n() { return 1; }\nDEFUN(a)\nDEFUN(b);\nvoid f() { }\n", true},
		{"undef then define", "#undef DIR\n#define DIR \"/x/\"\nvoid f(string s) {\n switch (s) {\n case DIR+\"a\":\n  if (s) g(); else h();\n  break;\n }\n}\n", true},
		{"error after opaque skip", "#define FRAG foo(\nvoid f() {\n switch (s) {\n case FRAG 1):\n  if (s) g();\n  else h();\n }\n}\n", true},
		{"arrow macro", "#define QN query_name()\nvoid f(object o) { write(o->QN + \"x\"); }\n", true},
		{"typed object macro", "#define I_P \"/std/player\"\nobject I_P who;\nvoid f(object I_P a, object I_P *b) {\n object I_P ply;\n ply = (object I_P) a;\n}\n", true},
		{"ifndef __DGD__", "void f() {\n#ifndef __DGD__\n x = (*fp)();\n#endif\n}\n", true},
		{"error in undecided branch", "void f() {\n#ifdef KILL\n x = store(a});\n#endif\n#if OPEN\n if (a && b(c) {\n#endif\n y = 1;\n#ifndef CLOSED\n write(\"x);\n#endif\n}\n", false},
		{"skip keeps bracket depth", "#define E\nvoid f() {\n c['C'] = m + ({ s[1..E] });\n x = 1;\n}\nvoid g() { }\n", true},
		{"redefined after undef across layers", "#ifdef NAME\n#undef NAME\n#endif\n#define NAME \"x\"\nvoid f() { g(\"A \" + NAME + \"\"); }\n", true},
		{"stringized macro argument", "#define TR(x) write(#x)\nvoid f() {\n TR(You can't \\n);\n}\n", true},
		{"keyword as macro", "#define catch \"x\"\nvoid f() { g(({ \"a\" + catch, 1 })); }\n", true},
		{"inherit of unseen macro", "inherit SOME_ROOM;\ninherit lab OTHER;\n", false},
		{"line splice", "void f() {\n g(\"a\" + \\\n  \"b\");\n}\n", true},
		{"ifdef inside failed statement", "void f() {\n foo(a\n#ifdef X\n , b);\n#else\n );\n#endif\n}\n", true},
	}
	for _, tt := range clean {
		t.Run(tt.name, func(t *testing.T) {
			assert.Empty(t, errs(parse(tt.src, tt.complete)))
		})
	}

	// With every definition in view the same conditions are decided: a
	// name defined nowhere is not defined, and the branch that compiles is
	// checked like any other code.
	got := errs(parse("void f() {\n#ifdef KILL\n x = store(a});\n#endif\n#ifndef CLOSED\n write(\"x);\n#endif\n}\n", true))
	if assert.Len(t, got, 1) {
		assert.Contains(t, got[0], "6:8 unterminated string")
	}

	// A partial view must still report errors no macro could explain.
	got = errs(parse("void f() {\n int count;\n count = 1\n count = 2;\n}\n", false))
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "3:11 missing ';'")
}

func TestTreeShape(t *testing.T) {
	f := parse(`#define status int
inherit base "/std/base";
static status flag, *list;
varargs int f(int a, varargs string b, mixed c...) {
    if (a = 1) return b ? 1 : 2;
    ob->call(a, b...);
}
int proto(int x);
`, true)
	require.Empty(t, errs(f))
	require.Len(t, f.Decls, 4)

	inh := f.Decls[0].(*InheritDecl)
	assert.Equal(t, "base", inh.Label.Name)
	assert.Equal(t, `"/std/base"`, f.Text(inh.Path))

	vd := f.Decls[1].(*VarDecl)
	assert.True(t, vd.Has(token.KwStatic))
	require.Len(t, vd.Vars, 2)
	assert.Equal(t, 1, vd.Vars[1].Stars)

	fn := f.Decls[2].(*FuncDecl)
	assert.Equal(t, "f", fn.Name.Name)
	assert.True(t, fn.Has(token.KwVarargs))
	require.Len(t, fn.Params, 3)
	assert.True(t, fn.Params[1].Varargs)
	assert.True(t, fn.Ellipsis)
	ifs := fn.Body.Stmts[0].(*IfStmt)
	assert.IsType(t, &AssignExpr{}, ifs.Cond)
	arrow := fn.Body.Stmts[1].(*ExprStmt).X.(*ArrowExpr)
	assert.Equal(t, "call", arrow.Name.Name)
	assert.True(t, arrow.Spread)

	proto := f.Decls[3].(*FuncDecl)
	assert.Nil(t, proto.Body)

	// Every node's span is inside the file and ordered.
	Inspect(f, func(n Node) bool {
		a, b := n.Span()
		assert.LessOrEqual(t, a, b, "%T", n)
		assert.Less(t, b, len(f.Tok.Tokens))
		return true
	})
}

func TestNeverPanics(t *testing.T) {
	seeds := []string{
		"", "{", "}", "(", ")", "({", "([", "(:", ";", "int", "int x", "int f(", "int f() {",
		"void f() { if (", "void f() { x = ({ 1, ", "void f() { case", "inherit", "inherit ;",
		"void f() { a ? b", "void f() { catch", "void f() { new", "void f() { x->", "operator",
		"int operator", "#define X\nX", "#if 1\n", "void f() { for (;;", "void f() { do x(); while",
		"void f() { switch (x) { case 1: } }", "void f() { x = &g(1); y = (*x)(2); }", "private", "x(", "f() {}}}}", "'", "\"", "/*",
		"void f() { goto ; }", "void f() { if (x)", "void f() { if (x) y(); else", "void f() { while (1)",
		"void f() { do", "void f() { for (;;)", "void f() { case 1:", "void f() { lab:", "int x,", "int f(int a,",
		"inherit \"/x\"", "void f() { return", "void f() { x = a ? b :", "void f() { ({", "void f() { ([ a :", "void f() { rlimits (1; 2) x; }", "void f() { try { } catch (...) { } }",
		strings.Repeat("(", 2000), strings.Repeat("{", 500), "void f() {" + strings.Repeat("if (x) ", 300) + ";}",
	}
	for _, s := range seeds {
		for _, complete := range []bool{true, false} {
			assert.NotPanics(t, func() {
				f := parse(s, complete)
				// Spans stay ordered and inside the file even when the
				// input ends mid-construct.
				Inspect(f, func(n Node) bool {
					a, b := n.Span()
					if a > b || b >= len(f.Tok.Tokens) {
						t.Errorf("%q: %T span [%d,%d] of %d", s, n, a, b, len(f.Tok.Tokens))
					}
					return true
				})
			}, "%q", s)
		}
	}
}

// PathAt: the nodes around a token, innermost first.
func TestPathAt(t *testing.T) {
	src := `inherit base "/std/" + "room";
int a, b;
void f(int p) {
	int local;
	if (a) base::g(p, "x"->h(b + 1));
	else
		;
	#if 0
	dead(a);
	#endif
}
`
	f := parse(src, true)
	path := func(at string) string {
		cursor := strings.IndexByte(at, '|')
		needle := at[:cursor] + at[cursor+1:]
		require.Equal(t, 1, strings.Count(src, needle), at)
		off := strings.Index(src, needle) + cursor
		var out []string
		for _, n := range PathAt(f, f.Tok.TokenAt(uint32(off))) {
			out = append(out, strings.TrimPrefix(fmt.Sprintf("%T", n), "*syntax."))
		}
		return strings.Join(out, " ")
	}
	for at, want := range map[string]string{
		"inherit ba|se": "InheritDecl File",
		"\"/st|d/\"":    "BasicLit BinaryExpr InheritDecl File",
		"\"ro|om\"":     "BasicLit BinaryExpr InheritDecl File",
		"int a, |b":     "Declarator VarDecl File",
		"i|nt a":        "VarDecl File",
		"void |f(":      "FuncDecl File",
		"int |p)":       "Param FuncDecl File",
		"int lo|cal":    "Declarator VarDecl DeclStmt BlockStmt FuncDecl File",
		"if (|a)":       "Ident IfStmt BlockStmt FuncDecl File",
		"ba|se::g":      "CallExpr ExprStmt IfStmt BlockStmt FuncDecl File",
		"base::|g":      "CallExpr ExprStmt IfStmt BlockStmt FuncDecl File",
		"g(|p,":         "Ident CallExpr ExprStmt IfStmt BlockStmt FuncDecl File",
		"\"|x\"->":      "BasicLit ArrowExpr CallExpr ExprStmt IfStmt BlockStmt FuncDecl File",
		"->|h(":         "ArrowExpr CallExpr ExprStmt IfStmt BlockStmt FuncDecl File",
		"h(|b + 1":      "Ident BinaryExpr ArrowExpr CallExpr ExprStmt IfStmt BlockStmt FuncDecl File",
		"b |+ 1":        "BinaryExpr ArrowExpr CallExpr ExprStmt IfStmt BlockStmt FuncDecl File",
		"el|se":         "IfStmt BlockStmt FuncDecl File",
		"else\n\t\t|;":  "EmptyStmt IfStmt BlockStmt FuncDecl File",
		"de|ad(a)":      "BlockStmt FuncDecl File",
		"#if| 0":        "BlockStmt FuncDecl File",
		"int a, b;|\n":  "File",
		"#endif\n}|\n":  "File",
	} {
		assert.Equal(t, want, path(at), "at %q", at)
	}
	// Any token of any file has a path, and the file ends it.
	for _, src := range []string{"", "void f() { if (x) }", "int ;;; void g( {", "#if 1\nvoid f() {\n#else\n}\n#endif\n"} {
		f := parse(src, true)
		for i := range f.Tok.Tokens {
			p := PathAt(f, i)
			require.NotEmpty(t, p)
			assert.Same(t, f, p[len(p)-1])
			for _, n := range p {
				first, last := n.Span()
				assert.True(t, first <= i && i <= last, "%q: token %d in %T", src, i, n)
			}
		}
		assert.Equal(t, len(f.Tok.Tokens)-1, f.Tok.TokenAt(uint32(len(src))+7))
	}
}
