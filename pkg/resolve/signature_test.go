package resolve_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/calls"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/resolve"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// showSignature writes what a call takes, a line for each declaration:
//
//	NAME #ARG `LABEL` [ACTIVE PARAMETER] @ WHERE // DOC
func showSignature(t *testing.T, help *resolve.SignatureHelp) string {
	if help == nil {
		return "nothing"
	}
	require.NotEmpty(t, help.Signatures)
	var lines []string
	for _, s := range help.Signatures {
		line := fmt.Sprintf("%s #%d `%s`", help.Name, help.Arg, s.Label)
		if s.Active >= 0 {
			require.Less(t, s.Active, len(s.Params), s.Label)
			line += " [" + s.Label[s.Params[s.Active][0]:s.Params[s.Active][1]] + "]"
		} else {
			require.Equal(t, -1, s.Active)
		}
		if where := s.Origin.String(); where != "" {
			line += " @ " + where
		}
		if s.Doc != "" {
			line += " // " + strings.ReplaceAll(s.Doc, "\n", "\\n")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (w *world) sig(path, at string) string {
	w.t.Helper()
	off, doc := w.offset(path, at)
	return showSignature(w.t, doc.Signature(off))
}

func signatureLib() vfs.Mem {
	fs := hoverLib()
	fs["/lib/obj/s.c"] = `#include <std.h>
inherit STD;
inherit side "/std/side";

void later(int when, varargs string why, mixed more...);
int two(int a, int b) { return a; }

void calls() {
    two(1, 2);
    two (  );
    two(two(3, 4), 5);
    two(({ 6, 7 }), ([ 8: 9, 10: 11 ])[8, 12]);
    two((13, 14), "a,b" /* , */);
    later(1, "x", 2, 3, 4);
    foo(15, "y", "z");
    ::foo(16);
    ::create(17);
    side::create(18);
    write("w");
    strlen("s");
    sscanf("s", "%d%d", count, count);
    read_file("f", 1, 2, 3);
    rusage(19);
    argon2_hash("p", 20, 21);
    ::find_object("o");
    this_object()->two(22, 23);
    ME->later(24);
    "/obj/b"->later(25);
    ("/obj/" + "b")->later(26);
    "/obj/b"->hidden(27);
    "/obj/nowhere"->later(28);
    ob->later(29);
    this_player()->later(30);
    a->b()->later(31);
    call_other("/obj/b", "later", 32);
    call_other(this_object(), "two", 33, 34);
    call_other(ob, "later", 35);
    call_out("two", 36, 37, 38);
    add_action("two", "verb", 39);
    SQUARE(40);
    AUTO(41);
    nope(42);
    (*fp)(43);
    if (two(44, 45)) return;
    while (46, 47) { two(48); }
    catch(two(49, 50));
}
int count;
void later(int when, varargs string why, mixed more...) {}
void typing() {
    two(51,
}
`
	return fs
}

func TestSignature(t *testing.T) {
	w := newWorld(t, signatureLib(), "/lib")
	const s = "/lib/obj/s.c"
	const two, later = "`int two(int a, int b)`", "`void later(int when, varargs string why, mixed more...)`"
	for at, want := range map[string]string{
		// The argument the cursor is in, from just inside the '(' to
		// just before the ')'.
		"two(|1, 2)": "two #0 " + two + " [int a] @ /obj/s.c:6",
		"two(1|, 2)": "two #0 " + two + " [int a] @ /obj/s.c:6",
		"two(1,| 2)": "two #1 " + two + " [int b] @ /obj/s.c:6",
		"two(1, 2|)": "two #1 " + two + " [int b] @ /obj/s.c:6",
		"two(1, 2)|": "nothing",
		"tw|o(1, 2)": "nothing",
		"two|(1, 2)": "nothing",
		"two ( | );": "two #0 " + two + " [int a] @ /obj/s.c:6",
		"two |(  );": "nothing",
		// The innermost call, and the outer one after it.
		"two(two(3|, 4), 5)": "two #0 " + two + " [int a] @ /obj/s.c:6",
		"two(two(3, |4), 5)": "two #1 " + two + " [int b] @ /obj/s.c:6",
		"two(two(3, 4)|, 5)": "two #0 " + two + " [int a] @ /obj/s.c:6",
		"two(two(3, 4), |5)": "two #1 " + two + " [int b] @ /obj/s.c:6",
		"two(|two(3, 4), 5)": "two #0 " + two + " [int a] @ /obj/s.c:6",
		// Commas that are not the call's: of an array, a mapping, an
		// index, parentheses, a string, a comment.
		"two(({ 6, |7 })":     "two #0 " + two + " [int a] @ /obj/s.c:6",
		"([ 8: 9, 10: |11 ])": "two #1 " + two + " [int b] @ /obj/s.c:6",
		"[8, |12]);":          "two #1 " + two + " [int b] @ /obj/s.c:6",
		"two((13, |14)":       "two #0 " + two + " [int a] @ /obj/s.c:6",
		"\"a,|b\" /* , */":    "two #1 " + two + " [int b] @ /obj/s.c:6",
		"\"a,b\" /* ,| */":    "two #1 " + two + " [int b] @ /obj/s.c:6",
		"\"a,b\" /* , */|);":  "two #1 " + two + " [int b] @ /obj/s.c:6",
		// A parameter that may be left out, and one that takes the rest;
		// too many arguments have no parameter.
		"later(1|, \"x\", 2, 3, 4)":       "later #0 " + later + " [int when] @ /obj/s.c:49",
		"later(1, \"x|\", 2, 3, 4)":       "later #1 " + later + " [varargs string why] @ /obj/s.c:49",
		"later(1, \"x\", |2, 3, 4)":       "later #2 " + later + " [mixed more...] @ /obj/s.c:49",
		"later(1, \"x\", 2, 3, |4)":       "later #4 " + later + " [mixed more...] @ /obj/s.c:49",
		"two(two(3, 4), 5|);\n    two(({": "two #1 " + two + " [int b] @ /obj/s.c:6",
		"foo(15, \"y\", |\"z\")":          "foo #2 `static varargs int foo(int a, string b...)` [string b...] @ /std/base.c:14",
		"read_file(\"f\", 1, 2, |3)":      "read_file #3 `string read_file(string, varargs int, int)` @ kfun, DGD 1.7.9 // read a string from file",
		"rusage(|19)":                     "rusage #0 `int *rusage()` @ kfun, declared by the project (dialect.kfuns)",

		// Inherited, the auto object's, the driver's.
		"::foo(|16)": "foo #0 `static varargs int foo(int a, string b...)` [int a] @ /std/base.c:14",
		"::create(|17)": "create #0 `void create()` @ /std/base.c:10 // Called when the object is made.\\n\\nTwice, at times.\n" +
			"create #0 `void create()` @ /std/side.c:1",
		"side::create(|18)":                      "create #0 `void create()` @ /std/side.c:1",
		"write(|\"w\")":                          "write #0 `static void write(string s)` [string s] @ auto object, /sys/auto.c:5 // Tell the user.\\n  Indented.",
		"strlen(|\"s\")":                         "strlen #0 `int strlen(string)` [string] @ kfun, DGD 1.7.9 // return the length of a string",
		"sscanf(\"s\", \"%d%d\", count, |count)": "sscanf #3 `int sscanf(string, string, lvalue...)` [lvalue...] @ kfun, DGD 1.7.9 // scan a string",
		"sscanf(\"s\", |\"%d%d\"":                "sscanf #1 `int sscanf(string, string, lvalue...)` [string] @ kfun, DGD 1.7.9 // scan a string",
		"argon2_hash(\"p\", 20, |21)":            "argon2_hash #2 `string argon2_hash(string password, int ops_limit, int mem_limit)` [int mem_limit] @ kfun, declared by the project (dialect.kfuns)",
		"::find_object(|\"o\")":                  "find_object #0 `object find_object(string)` [string] @ kfun, DGD 1.7.9 // find the loaded object for a given object name",

		// Calls bound when they run: a receiver that is known.
		"this_object()->two(22, |23)":     "two #1 " + two + " [int b] @ /obj/s.c:6",
		"ME->later(|24)":                  "later #0 " + later + " [int when] @ /obj/s.c:49",
		"\"/obj/b\"->later(|25)":          "later #0 `void later(int when)` [int when] @ /obj/b.c:3 // Some time.",
		"(\"/obj/\" + \"b\")->later(|26)": "later #0 `void later(int when)` [int when] @ /obj/b.c:3 // Some time.",
		// And one that is not, or has no such function for a caller:
		// nothing is guessed.
		"\"/obj/b\"->hidden(|27)":      "hidden #0 `private void hidden()` @ /obj/b.c:4",
		"\"/obj/nowhere\"->later(|28)": "nothing",
		"ob->later(|29)":               "nothing",
		"this_player()->later(|30)":    "nothing",
		"a->b()->later(|31)":           "nothing",

		// The arguments a call passes on are those of the function it
		// names; the ones before them are its own.
		"call_other(\"/obj/b\", \"later\", |32)":     "later #0 `void later(int when)` [int when] @ /obj/b.c:3 // Some time.",
		"call_other(\"/obj/b\", |\"later\", 32)":     "call_other #1 `mixed call_other(mixed, string, mixed...)` [string] @ kfun, DGD 1.7.9 // call a function in another object",
		"call_other(this_object(), \"two\", 33, |34": "two #1 " + two + " [int b] @ /obj/s.c:6",
		"call_other(ob, \"later\", |35)":             "call_other #2 `mixed call_other(mixed, string, mixed...)` [mixed...] @ kfun, DGD 1.7.9 // call a function in another object",
		"call_out(\"two\", 36, 37, |38)":             "two #1 " + two + " [int b] @ /obj/s.c:6",
		"call_out(\"two\", |36, 37, 38)":             "call_out #1 `int call_out(string, mixed, mixed...)` [mixed] @ kfun, DGD 1.7.9 // start a call_out",
		// A registrar that does not say which arguments it passes on.
		"add_action(\"two\", \"verb\", |39)": "nothing",

		// A macro with parameters; one without is no call.
		"SQUARE(|40)": "SQUARE #0 `SQUARE(x)` [x] @ /include/std.h:5 // The square\\nof x.",
		"AUTO(|41)":   "nothing",
		// What nobody declares, and what is no name.
		"nope(|42)":       "nothing",
		"(*fp)(|43)":      "nothing",
		"while (46, |47)": "nothing",
		// Calls in conditions and in catch.
		"if (two(44, |45))":   "two #1 " + two + " [int b] @ /obj/s.c:6",
		"{ two(|48); }":       "two #0 " + two + " [int a] @ /obj/s.c:6",
		"{ two(48); |}":       "nothing",
		"catch(two(49, |50))": "two #1 " + two + " [int b] @ /obj/s.c:6",
		"catch(two(49, 50)|)": "nothing",

		// A declaration is no call.
		"void later(int when, |varargs string why, mixed more...);":   "nothing",
		"int two(int a, |int b)":                                      "nothing",
		"void later(int when, varargs string why, |mixed more...) {}": "nothing",
		// A call that is being written: the statement does not parse.
		"two(51,|\n}": "two #1 " + two + " [int b] @ /obj/s.c:6",
	} {
		assert.Equal(t, want, w.sig(s, at), "at %q", at)
	}
	// The add_action of the configuration says nothing of arguments; one
	// that does is read like the driver's own.
	assert.Equal(t, "nothing", w.sig(s, "add_action(\"two\", |\"verb\""))
}

// While a call is typed the file is no program yet; the signature is
// there all the same, at every step.
func TestSignatureWhileTyping(t *testing.T) {
	const head = "inherit \"/std/base\";\nint two(int a, int b) { return a; }\nvoid f() {\n    int x;\n    x = 1;\n    "
	const tail = "\n    x = 2;\n}\nvoid g() { two(1, 2); }\n"
	const two = "two #%d `int two(int a, int b)` [%s] @ /obj/t.c:2"
	for typed, want := range map[string]string{
		"two(":                                fmt.Sprintf(two, 0, "int a"),
		"two(x":                               fmt.Sprintf(two, 0, "int a"),
		"two(x,":                              fmt.Sprintf(two, 1, "int b"),
		"two(x, two(":                         fmt.Sprintf(two, 0, "int a"),
		"two(x, two(1, 2)":                    fmt.Sprintf(two, 1, "int b"),
		"two(x, ({ 1, ":                       fmt.Sprintf(two, 1, "int b"),
		"two(x, ([ 1: ":                       fmt.Sprintf(two, 1, "int b"),
		"two(x, \"":                           fmt.Sprintf(two, 1, "int b"),
		"x = two(1, (":                        fmt.Sprintf(two, 1, "int b"),
		"if (two(":                            fmt.Sprintf(two, 0, "int a"),
		"return two(1, strlen(":               "strlen #0 `int strlen(string)` [string] @ kfun, DGD 1.7.9 // return the length of a string",
		"this_object()->two(1, ":              fmt.Sprintf(two, 1, "int b"),
		"\"/obj/t\"->two(":                    fmt.Sprintf(two, 0, "int a"),
		"call_other(\"/obj/t\", \"two\", 1, ": fmt.Sprintf(two, 1, "int b"),
		"ob->two(":                            "nothing",
		"foo(1, \"a\", \"b\", ":               "foo #3 `static varargs int foo(int a, string b...)` [string b...] @ /std/base.c:14",
		"two(1, 2); ":                         "nothing",
		"two":                                 "nothing",
		"{ ":                                  "nothing",
	} {
		fs := hoverLib()
		fs["/lib/obj/t.c"] = head + typed + tail
		w := newWorld(t, fs, "/lib")
		doc, err := w.l.Lint(context.Background(), "/lib/obj/t.c", lint.Options{NoScan: true})
		require.NoError(t, err)
		assert.Equal(t, want, showSignature(t, doc.Signature(uint32(len(head)+len(typed)))), "typed %q", typed)
	}
}

// What a receiver is, read from the tokens before a call that is being
// written, is what calls.Extract reads from the tree of one that parsed.
func TestSignatureReceiverIsExtracts(t *testing.T) {
	fs := wholeLib()
	fs["/lib/obj/r.c"] = `#include <std.h>
inherit STD;
#define HERE "/obj/"
#define B HERE + "b"
#define PB (B)
void f(object ob, string s) {
    this_object()->foo(1);
    (this_object())->foo(2);
    ME->foo(3);
    "/obj/b"->bar(4);
    ("/obj/b")->bar(5);
    (HERE + "b")->bar(6);
    PB->bar(7);
    ROOM_D->register(8);
    STD->foo(9);
    ob->foo(10);
    s->foo(11);
    (s + "x")->foo(12);
    this_player()->foo(13);
    ::this_object()->foo(14);
    ob->a()->foo(15);
    ("/obj/" + s)->foo(16);
    SQUARE(2)->foo(17);
    ""->foo(18);
    f(ob, s)->foo(19);
    (ob ? "/obj/b" : "/obj/a")->foo(20);
    ({ "/obj/b" })[0]->foo(21);
    "obj/b"->bar(22);
}
`
	w := newWorld(t, fs, "/lib")
	doc, err := w.l.Lint(context.Background(), "/lib/obj/r.c", lint.Options{NoScan: true})
	require.NoError(t, err)
	facts := calls.Extract(calls.File{Tree: doc.Tree, Env: doc.Env, LibPath: doc.Unit.LibPath,
		ObjectPath: func(written string) string { return doc.Snapshot.ObjectPath(written, doc.Unit.LibPath) }},
		calls.NewRegistry(profile.Conventions{}))
	require.Len(t, facts.Named, 23)
	targets := map[calls.Target]int{}
	for _, c := range facts.Named {
		targets[c.Target]++
		// Just inside the '(' of the call.
		open := uint32(strings.IndexByte(string(doc.File.Src[c.End:]), '(')) + c.End + 1
		help := doc.Signature(open)
		ref := doc.Resolve(c.Start)
		require.NotNil(t, ref)
		at := string(doc.File.Src[c.Start-12 : open+3])
		if c.Target == calls.TargetUnknown || len(ref.Defs) == 0 {
			assert.Nil(t, help, at)
			continue
		}
		require.NotNil(t, help, at)
		require.Len(t, help.Signatures, len(ref.Defs), at)
		for i, d := range ref.Defs {
			assert.Equal(t, d.Func.Decl, help.Signatures[i].Label, at)
		}
	}
	assert.Equal(t, map[calls.Target]int{calls.TargetSelf: 3, calls.TargetPath: 7, calls.TargetUnknown: 13}, targets)
}

// Nothing to ask.
func TestSignatureOfNothing(t *testing.T) {
	assert.Nil(t, resolve.New(resolve.File{}).Signature(0))
	assert.Nil(t, resolve.New(resolve.File{}).Hover(0))
	w := newWorld(t, vfs.Mem{"/lib/dgdtools.yml": "", "/lib/a.c": "int x;\n", "/lib/b.c": "", "/lib/c.c": "void f() { f(); }"}, "/lib")
	for _, path := range []string{"/lib/a.c", "/lib/b.c", "/lib/c.c"} {
		doc, err := w.l.Lint(context.Background(), path, lint.Options{NoScan: true})
		require.NoError(t, err)
		for off := uint32(0); off < uint32(len(doc.File.Src))+3; off++ {
			doc.Hover(off)
			if s := doc.Signature(off); s != nil {
				assert.Equal(t, "f", s.Name)
			}
		}
	}
}

// A file as it is typed, a byte at a time, is mostly no program; at the
// end of every such text the questions are asked, and answered or not,
// and nothing falls over.
func TestEveryPrefix(t *testing.T) {
	if testing.Short() {
		t.Skip("lints a file once for each of its bytes")
	}
	lib := signatureLib()
	text := lib["/lib/obj/s.c"]
	answered := 0
	for n := 0; n <= len(text); n++ {
		fs := vfs.Mem{}
		for k, v := range lib {
			fs[k] = v
		}
		fs["/lib/obj/s.c"] = text[:n]
		w := newWorld(t, fs, "/lib")
		doc, err := w.l.Lint(context.Background(), "/lib/obj/s.c", lint.Options{NoScan: true})
		require.NoError(t, err)
		for _, off := range []uint32{uint32(n), uint32(max(n-1, 0))} {
			doc.Hover(off)
			if help := doc.Signature(off); help != nil {
				answered++
				for _, s := range help.Signatures {
					require.Less(t, s.Active, len(s.Params)+1, "%q", text[:n])
				}
			}
		}
	}
	// Most of the file is the arguments of calls.
	assert.Greater(t, answered, len(text)/4)
}
