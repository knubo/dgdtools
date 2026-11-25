package calls_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mmcdole/dgdtools/pkg/calls"
	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/syntax"
)

// extract returns the named calls of src as "via func kind target nargs".
func extract(src string, conv profile.Conventions) (named, paths []string) {
	f := lexer.Lex("/lib/obj/t.c", []byte(src), lexer.Default)
	env := pp.NewEnv(nil, nil)
	env.Complete = true
	sel := env.Scan(f, nil)
	tree := syntax.Parse(f, syntax.Options{Env: env, Sel: sel})
	facts := calls.Extract(calls.File{
		Tree: tree, Env: env, LibPath: "/obj/t",
		ObjectPath: func(written string) string { return "<" + written + ">" },
	}, calls.NewRegistry(conv))
	for _, c := range facts.Named {
		kind := map[calls.Kind]string{calls.CrossObject: "other", calls.SelfCallback: "self"}[c.Kind]
		target := map[calls.Target]string{calls.TargetUnknown: "?", calls.TargetSelf: "this", calls.TargetPath: c.TargetPath}[c.Target]
		named = append(named, fmt.Sprintf("%s %s %s %s %d", c.Via, c.Func, kind, target, c.NArgs))
	}
	for _, p := range facts.Paths {
		paths = append(paths, p.Via+" "+p.Path)
	}
	return named, paths
}

func body(stmts string) string {
	return "#define HERE this_object()\n#define D \"/sys/d\"\nvoid f(object ob, mixed *rest) {\n" + stmts + "\n}\n"
}

// The driver's own late-bound calls need no configuration.
func TestDriverCalls(t *testing.T) {
	named, paths := extract(body(`
    ob->a(1, 2);
    this_object()->b();
    HERE->c(rest...);
    "/std/x"->d();
    D->e();
    ("/std/" + "y")->g();
    call_other(ob, "h", 1);
    call_other(this_object(), "i");
    call_other(D, "j", 1, 2, 3);
    call_other(D, "k", rest...);
    call_other(D, name);
    call_out("l", 5);
    call_out("m", 5, 1, 2);
    call_out("n");
    clone_object("/obj/thing");
    find_object("sys/daemon");
    compile_object(name);
    plain("o");`), profile.Conventions{})
	assert.Equal(t, []string{
		"-> a other ? 2",
		"-> b other this 0",
		"-> c other this -1",
		"-> d other </std/x> 0",
		"-> e other </sys/d> 0",
		"-> g other </std/y> 0",
		"call_other h other ? 1",
		"call_other i other this 0",
		"call_other j other </sys/d> 3",
		"call_other k other </sys/d> -1",
		"call_out l self this 0",
		"call_out m self this 2",
		"call_out n self this 0",
	}, named)
	assert.Equal(t, []string{"clone_object </obj/thing>", "find_object <sys/daemon>"}, paths)
}

// A lib's registrars are read by the forms the configuration declares,
// and by nothing else.
func TestRegistryForms(t *testing.T) {
	zero, one, two := 0, 1, 2
	conv := profile.Conventions{
		CallRegistry: map[string]profile.CallForms{
			"add_action": {{Function: 0}},
			"bind":       {{Object: &zero, Function: 1}, {Function: 0}},
			"defer":      {{Function: 1, Object: &two, Args: &one}},
			"call_out":   {{Function: 1}}, // the lib's own call_out(delay, "fn")
		},
		ObjectRegistry: map[string]int{"load": 1},
	}
	named, paths := extract(body(`
    add_action("go", "north");
    add_action("/std/x", "go");
    add_action(verb, "go");
    bind("solo");
    bind(ob, "theirs");
    bind("/std/x", "there");
    bind(D, "daemon");
    bind("a", "b");
    bind("not a name");
    bind(ob, name);
    defer(3, "later", D, 1, 2);
    defer(3, "short");
    call_out(2, "tick");
    call_out("old", 2);
    load(1, "/obj/thing");
    load("/obj/other");`), conv)
	assert.Equal(t, []string{
		"add_action go self this -1",
		"bind solo self this -1",
		"bind theirs other ? -1",
		"bind there other </std/x> -1",
		"bind daemon other </sys/d> -1",
		"bind b other <a> -1", // by the first form it fits: an object and a function
		"defer later other </sys/d> 4",
		"call_out tick self this -1",
	}, named, "a string that is not a function name, or a call that fits no form, is no call")
	assert.Equal(t, []string{"load </obj/thing>"}, paths)
}

// Without a lib path nothing is known about object paths.
func TestNoLibPath(t *testing.T) {
	f := lexer.Lex("/tmp/t.c", []byte("void f() { \"/std/x\"->a(); this_object()->b(); }\n"), lexer.Default)
	env := pp.NewEnv(nil, nil)
	tree := syntax.Parse(f, syntax.Options{Env: env, Sel: env.Scan(f, nil)})
	facts := calls.Extract(calls.File{Tree: tree, Env: env, ObjectPath: func(s string) string { return s }}, calls.NewRegistry(profile.Conventions{}))
	assert.Equal(t, calls.TargetUnknown, facts.Named[0].Target)
	assert.Equal(t, calls.TargetSelf, facts.Named[1].Target)
	assert.Len(t, facts.Direct, 1, "this_object()")
}

// The forms a registry has of a function: the driver's, the project's,
// and none of any other.
func TestForms(t *testing.T) {
	zero, two := 0, 2
	reg := calls.NewRegistry(profile.Conventions{CallRegistry: map[string]profile.CallForms{
		"add_action": {{Function: 0}},
		"call_out":   {{Function: 1, Args: &two}},
	}})
	assert.Equal(t, profile.CallForms{{Object: &zero, Function: 1, Args: &two}}, reg.Forms("call_other"))
	assert.Equal(t, profile.CallForms{{Function: 1, Args: &two}}, reg.Forms("call_out"), "the project's word of the driver's function")
	assert.Equal(t, profile.CallForms{{Function: 0}}, reg.Forms("add_action"))
	assert.Empty(t, reg.Forms("clone_object"))
	assert.Empty(t, reg.Forms("nothing"))
}
