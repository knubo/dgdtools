package lint_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// wholeLib is a small lib with shared code, a domain, and two wizards.
func wholeLib() vfs.Mem {
	return vfs.Mem{
		"/mud/dgdtools.yml": `root: lib
profile_build:
  name: mini
  driver_config: etc/mud.dgd
  share: ["std/**", "secure/**"]
  omit: ["secure/private/**"]
  checkouts: ["players/*", "d/*"]
lint:
  call_registry: {add_action: 0}
`,
		"/mud/etc/mud.dgd":                "include_file = \"/sys/forced.h\";\ninclude_dirs = ({ \"/std/include\" });\nauto_object = \"/sys/auto\";\ntypechecking = 1;\n",
		"/mud/lib/sys/forced.h":           "#define TRUE 1\n#define public\n",
		"/mud/lib/sys/auto.c":             "static string object_name() { return \"x\"; }\nvoid secret_impl() { int body; body = 42; }\n",
		"/mud/lib/std/include/std.h":      "#ifndef STD_H\n#define STD_H\n#include <paths.h>\n#define status int\n#define MAX(a, b) ((a) > (b) ? (a) : (b))\n#endif\n",
		"/mud/lib/std/include/paths.h":    "#define I_ROOM \"/std/room\"\n#define D_TIME \"/secure/timed\"\n#define SECRETD \"/secure/private/vault\"\n",
		"/mud/lib/std/base.c":             "#include <std.h>\nprivate int hidden_state;\nint shared_state;\nstatic string *names, title;\nstatic void create() { hidden_state = 1; }\nvarargs int query(int a, int b) { return a; }\nprivate void internal() { }\nlegacy(x, y) { }\nnomask status *flags(object ob, varargs mixed rest...) { return 0; }\n",
		"/mud/lib/std/room.c":             "#include <std.h>\n#define LOCAL_ONLY 99\ninherit \"/std/base\";\nstatic void create() { ::create(); }\nvoid add_exit(string to, string dir) { /* body with LOCAL_ONLY */ }\nstatic int room_static() { return LOCAL_ONLY; }\n",
		"/mud/lib/secure/timed.c":         "int query_season() { return 1; }\n",
		"/mud/lib/secure/private/vault.c": "string password() { return \"hunter2\"; }\n",
		"/mud/lib/d/Wild/std/mob.c":       "inherit \"/std/base\";\nvoid roar() { }\n",
		"/mud/lib/players/ann/tools.h":    "#define ANN \"/players/ann/\"\n",
		"/mud/lib/players/ann/base.c":     "#include <std.h>\ninherit I_ROOM;\nstatic void create() { ::create(); }\nvoid helper() { }\n",
		"/mud/lib/players/ann/hall.c": `#include <std.h>
#include "tools.h"
inherit ANN + "base";
static void create() {
    status ok;
    ::create();
    add_exit("x", "north", 3);
    this_object()->add_exit("y", "south", 3);
    this_object()->nope();
    D_TIME->query_season();
    D_TIME->no_such();
    I_ROOM->room_static();
    (ANN + "base")->helper();
    (ANN + "gone")->x();
    "/std/missing"->x();
    "/d/Wild/std/mob"->roar();
    "/d/Wild/std/mob"->not_there();
    "/players/bob/thing"->whatever();
    SECRETD->password();
    add_action("typo_fn", "go");
    ok = MAX(1, 2)
    ok = 3;
}
`,
		"/mud/lib/players/bob/thing.c": "void f() { }\n",
	}
}

func findings(t *testing.T, prj *config.Project, fsys vfs.FS, paths ...string) []string {
	t.Helper()
	l, err := lint.New(prj, fsys)
	require.NoError(t, err)
	res, err := l.Run(context.Background(), lint.Request{Paths: paths})
	require.NoError(t, err)
	var out []string
	for _, d := range res.Diagnostics {
		msg := strings.Replace(d.Message, " (nor in this checkout; inheritors elsewhere in the lib cannot be seen)", "", 1)
		msg = strings.Replace(msg, " or is private there (a profile lists public functions only)", "", 1)
		out = append(out, fmt.Sprintf("%s:%d %s %s", d.Path[strings.LastIndex(d.Path, "/")+1:], d.Line, d.Rule, msg))
	}
	sort.Strings(out)
	return out
}

func TestProfileBuildAndPartialCheckout(t *testing.T) {
	m := wholeLib()
	full, err := config.Discover(m, []string{"/mud/lib/players/ann"}, config.DiscoverOptions{})
	require.NoError(t, err)
	l, err := lint.New(full, m)
	require.NoError(t, err)
	p, err := l.BuildProfile(context.Background())
	require.NoError(t, err)

	// --- what a profile contains
	assert.Equal(t, "mini", p.Name)
	assert.Equal(t, "dgd", p.Dialect.Driver)
	require.NotNil(t, p.Dialect.Typechecking)
	assert.Equal(t, 1, *p.Dialect.Typechecking)
	assert.Equal(t, []string{"/std/include"}, p.IncludeDirs)
	assert.Equal(t, "/sys/forced.h", p.IncludeFile)
	assert.Equal(t, map[string]int{"add_action": 0}, p.Conventions.CallRegistry)
	assert.Equal(t, []string{"/secure/", "/std/", "/std/include/"}, p.Covered)
	var files []string
	for f := range p.Files {
		files = append(files, f)
	}
	sort.Strings(files)
	assert.Equal(t, []string{
		"/secure/timed.c", "/std/base.c", "/std/include/paths.h", "/std/include/std.h", "/std/room.c",
		"/sys/auto.c", "/sys/forced.h", // not shared, but everything needs them
	}, files, "shared files plus the driver's includes and auto object; no wizard or domain code, nothing omitted")

	room := p.Files["/std/room.c"]
	assert.Equal(t, "#include <std.h>\ninherit \"/std/base\";\nstatic void create() {}\nvoid add_exit(string to, string dir) {}\nstatic int room_static() {}\n", room)
	// Declarations exactly as written, minus bodies: real types,
	// specifiers, parameter names; a typeless function stays typeless; a
	// type macro stays the token that was written.
	assert.Equal(t, "#include <std.h>\nint shared_state;\nstatic string *names, title;\nstatic void create() {}\nvarargs int query(int a, int b) {}\nlegacy(x, y) {}\nnomask status *flags(object ob, varargs mixed rest...) {}\n",
		p.Files["/std/base.c"], "no private functions or variables")
	all := ""
	for _, s := range p.Files {
		all += s
	}
	for _, leak := range []string{"hunter2", "42", "LOCAL_ONLY", "body", "vault.c"} {
		assert.NotContains(t, all, leak, "no bodies, no object-local macros, nothing omitted")
	}
	assert.Contains(t, p.Files["/std/include/std.h"], "#define MAX(a, b) ((a) > (b) ? (a) : (b))")

	// --- declared types are index facts, from real files and stubs alike
	fn := l.Index().Snapshot().Chain("/std/room").Funcs["flags"]
	assert.Equal(t, "nomask status *flags(object ob, varargs mixed rest...)", fn.Decl)
	assert.Equal(t, "status *", fn.Type)
	assert.Equal(t, "/std/base", fn.DefinedIn)
	assert.Equal(t, []string{"object ob", "varargs mixed rest..."}, index.Params(fn.Decl))
	assert.Equal(t, "", l.Index().Snapshot().Chain("/std/base").Funcs["legacy"].Type)
	assert.Equal(t, "string *", l.Index().Snapshot().Unit("/std/base").Vars["names"].Type)
	assert.Equal(t, "string", l.Index().Snapshot().Unit("/std/base").Vars["title"].Type)

	// --- the file format: deterministic, round-trips
	data, err := p.Marshal()
	require.NoError(t, err)
	again, err := p.Marshal()
	require.NoError(t, err)
	assert.Equal(t, data, again)
	loaded, err := profile.Parse(data)
	require.NoError(t, err)
	assert.Equal(t, p.Manifest, loaded.Manifest)
	assert.Equal(t, p.Files, loaded.Files)
	_, err = profile.Parse([]byte("dgdlint-profile 99\n{}\n"))
	assert.ErrorContains(t, err, "format 99")
	_, err = profile.Parse([]byte("dgdlint-profile 1\n{\"dialect\":{\"driver\":\"fluffos\"}}\n"))
	assert.ErrorContains(t, err, "only the dgd dialect")

	// --- whole lib vs. the wizard's folder alone with the profile
	want := findings(t, full, m, "/mud/lib/players/ann")

	alone := vfs.Mem{
		"/home/ann/players/ann/dgdtools.yml": "profile: ../../mini.profile\n",
		"/home/ann/mini.profile":             string(data),
	}
	for path, src := range m {
		if rest, ok := strings.CutPrefix(path, "/mud/lib/players/ann/"); ok {
			alone["/home/ann/players/ann/"+rest] = src
		}
	}
	part, err := config.Discover(alone, []string{"/home/ann/players/ann/hall.c"}, config.DiscoverOptions{})
	require.NoError(t, err)
	assert.True(t, part.Partial)
	assert.Equal(t, "/players/ann", part.CheckoutLib, "read off the folder's path")
	assert.Equal(t, "/home/ann/players/ann", part.CheckoutDir)
	got := findings(t, part, alone) // no paths: the checkout
	pl, err := lint.New(part, alone)
	require.NoError(t, err)
	assert.Equal(t, fn.Decl, pl.Index().Snapshot().Chain("/players/ann/hall").Funcs["flags"].Decl, "the same declaration, read from the stub")
	assert.Equal(t, "status *", pl.Index().Snapshot().Chain("/players/ann/hall").Funcs["flags"].Type)

	// Everything that is about the wizard's own files or shared code is
	// found identically; what points elsewhere in the lib is unknown.
	var expect []string
	for _, w := range want {
		if strings.Contains(w, "/d/Wild/") || strings.Contains(w, "/players/bob/") {
			continue // trees the wizard does not have: unknown, silent
		}
		expect = append(expect, w)
	}
	assert.Equal(t, expect, got)
	assert.Len(t, want, len(got)+2)
	joined := strings.Join(got, "\n")
	for _, must := range []string{
		"hall.c:8 callback-arity",                     // signature from the profile
		"hall.c:9 callable-not-found function 'nope'", // own chain through profile stubs
		"hall.c:11 callable-not-found function 'no_such' is not defined in /secure/timed",
		"hall.c:12 static-crossobj",
		"hall.c:14 target-object-missing object /players/ann/gone", // own tree: covered
		"hall.c:15 target-object-missing object /std/missing",      // profile-covered dir
		"hall.c:20 callable-not-found function 'typo_fn'",          // registry from the profile
		"hall.c:21 syntax-error missing ';'",                       // macros from profile headers
	} {
		assert.Contains(t, joined, must)
	}
	for _, never := range []string{"bob", "vault", "hall.c:18", "hall.c:19", "include-not-found", "hall.c:5"} {
		assert.NotContains(t, joined, never)
	}

	// --- explicit lib_path when the folder's path says nothing
	loose := vfs.Mem{"/work/stuff/dgdtools.yml": "profile: /work/mini.profile\nlib_path: /players/ann\n", "/work/mini.profile": string(data)}
	nolib := vfs.Mem{"/work/stuff/dgdtools.yml": "profile: /work/mini.profile\n", "/work/mini.profile": string(data)}
	for path, src := range alone {
		if rest, ok := strings.CutPrefix(path, "/home/ann/players/ann/"); ok && rest != "dgdtools.yml" {
			loose["/work/stuff/"+rest] = src
			nolib["/work/stuff/"+rest] = src
		}
	}
	prj, err := config.Discover(loose, []string{"/work/stuff"}, config.DiscoverOptions{})
	require.NoError(t, err)
	assert.Equal(t, got, findings(t, prj, loose))

	// Without it: no claims between the wizard's own files, but the
	// profile's macros still make the syntax check exact.
	prj, err = config.Discover(nolib, []string{"/work/stuff"}, config.DiscoverOptions{})
	require.NoError(t, err)
	assert.Empty(t, prj.CheckoutLib)
	require.NotEmpty(t, prj.Notes)
	assert.Equal(t, diag.NoteUnplacedCheckout, prj.Notes[0].Kind)
	assert.Contains(t, prj.Notes[0].Message, "lib_path")
	assert.Equal(t, []string{"hall.c:21 syntax-error missing ';' before 'ok'"}, findings(t, prj, nolib))

	// --- real files win over stubs; a whole lib with a profile is whole
	m["/mud/mini.profile"] = string(data)
	m["/mud/dgdtools.yml"] = "root: lib\nprofile: mini.profile\n"
	m["/mud/lib/secure/timed.c"] = "int query_season() { return 1; }\nint no_such() { return 2; }\n"
	prj, err = config.Discover(m, []string{"/mud/lib/players/ann"}, config.DiscoverOptions{})
	require.NoError(t, err)
	assert.False(t, prj.Partial)
	assert.Equal(t, "/mud/lib", prj.Root)
	now := strings.Join(findings(t, prj, m, "/mud/lib/players/ann"), "\n")
	assert.NotContains(t, now, "no_such", "the file on disk defines it; the stale stub does not matter")
	assert.Contains(t, now, "hall.c:17 callable-not-found", "and the rest of the lib is visible again")
}

func TestInferCheckout(t *testing.T) {
	pats := []string{"players/*", "d/*"}
	dir, lib, ok := config.InferCheckout("/home/me/mud/players/drake/rooms/east", pats)
	assert.True(t, ok)
	assert.Equal(t, "/home/me/mud/players/drake", dir)
	assert.Equal(t, "/players/drake", lib)
	_, lib, ok = config.InferCheckout("/srv/d/Wild", pats)
	assert.True(t, ok)
	assert.Equal(t, "/d/Wild", lib)
	// players/* is tried first, so a stray /d/ higher up does not win.
	_, lib, _ = config.InferCheckout("/d/home/players/drake", pats)
	assert.Equal(t, "/players/drake", lib)
	_, _, ok = config.InferCheckout("/home/me/drake", pats)
	assert.False(t, ok)
	_, _, ok = config.InferCheckout("/home/me/players", pats)
	assert.False(t, ok)
}

// A profile named in a config is found next to the config, then in
// $DGDTOOLS_PROFILE_PATH, then in the user config directory; nothing is
// built in.
func TestProfileResolution(t *testing.T) {
	m := wholeLib()
	full, err := config.Discover(m, []string{"/mud/lib"}, config.DiscoverOptions{})
	require.NoError(t, err)
	l, err := lint.New(full, m)
	require.NoError(t, err)
	p, err := l.BuildProfile(context.Background())
	require.NoError(t, err)
	data, err := p.Marshal()
	require.NoError(t, err)

	// What a front end reads from its environment and hands over.
	dirs := profile.UserDirs("/opt/a"+string(os.PathListSeparator)+"/opt/b", "/home/zed/Library/Application Support", "/home/zed")
	assert.Equal(t, []string{"/opt/a", "/opt/b", "/home/zed/Library/Application Support/dgdtools/profiles", "/home/zed/.config/dgdtools/profiles"}, dirs)
	assert.Equal(t, []string{"/home/zed/.config/dgdtools/profiles"}, profile.UserDirs("", "/home/zed/.config", "/home/zed"))
	assert.Empty(t, profile.UserDirs("", "", ""))
	opt := config.DiscoverOptions{ProfileDirs: dirs}

	room := "#include <std.h>\ninherit I_ROOM;\nstatic void create() {\n ::create();\n this_object()->no_such_function_anywhere();\n}\n"
	wiz := vfs.Mem{
		"/home/zed/players/zed/dgdtools.yml": "profile: mini\n",
		"/home/zed/players/zed/room.c":       room,
	}
	// Not installed anywhere: a clear error naming every place searched.
	_, err = config.Discover(wiz, []string{"/home/zed/players/zed"}, opt)
	require.Error(t, err)
	for _, want := range []string{`profile "mini" not found`, "/home/zed/players/zed", "/opt/a", "/opt/b", "/home/zed/.config/dgdtools/profiles", profile.PathEnv} {
		assert.Contains(t, err.Error(), want)
	}

	// Each location works, in order of precedence.
	other := *p
	other.Name = "from-user-dir"
	otherData, err := other.Marshal()
	require.NoError(t, err)
	wiz["/home/zed/.config/dgdtools/profiles/mini.profile"] = string(otherData)
	prj, err := config.Discover(wiz, []string{"/home/zed/players/zed"}, opt)
	require.NoError(t, err)
	assert.Equal(t, "from-user-dir", prj.Profile.Name)
	assert.Equal(t, "/home/zed/.config/dgdtools/profiles/mini.profile", prj.Profile.Path)

	wiz["/opt/b/mini.profile"] = string(data)
	prj, err = config.Discover(wiz, []string{"/home/zed/players/zed"}, opt)
	require.NoError(t, err)
	assert.Equal(t, "/opt/b/mini.profile", prj.Profile.Path, "the environment path beats the user directory")

	wiz["/home/zed/players/zed/mini.profile"] = string(data)
	prj, err = config.Discover(wiz, []string{"/home/zed/players/zed/room.c"}, opt)
	require.NoError(t, err)
	assert.Equal(t, "/home/zed/players/zed/mini.profile", prj.Profile.Path, "next to the config beats everything")
	assert.Equal(t, "/players/zed", prj.CheckoutLib)
	got := findings(t, prj, wiz, "/home/zed/players/zed/room.c")
	require.Len(t, got, 1, "%v", got)
	assert.Contains(t, got[0], "room.c:5 callable-not-found function 'no_such_function_anywhere'")

	// The library API the CLI and a language server share.
	path, err := profile.Resolve(wiz, "mini", "/somewhere/else", dirs)
	require.NoError(t, err)
	assert.Equal(t, "/opt/b/mini.profile", path)
	path, err = profile.Resolve(wiz, "../../../../opt/b/mini.profile", "/home/zed/players/zed", nil)
	require.NoError(t, err)
	assert.Equal(t, "/opt/b/mini.profile", path)
	_, err = profile.Resolve(wiz, "/nowhere/x.profile", "/", nil)
	assert.ErrorContains(t, err, "does not exist")

	// By flag, with no config file at all.
	delete(wiz, "/home/zed/players/zed/dgdtools.yml")
	prj, err = config.Discover(wiz, []string{"/home/zed/players/zed/room.c"}, config.DiscoverOptions{Profile: "mini", WorkDir: "/home/zed", ProfileDirs: dirs})
	require.NoError(t, err)
	assert.Equal(t, "/players/zed", prj.CheckoutLib)
}
