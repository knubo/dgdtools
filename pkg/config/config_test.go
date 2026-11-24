package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

const driverCfg = `telnet_port	= 2001;			/* telnet port number */
directory	= "/usr/local/mud/lib";
typechecking	= 1;			/* global typechecking */
include_file	= "/dgd/include/Std.h";	/* standard include file */
include_dirs	= ({ "/dgd/include",
		     "/std/include" }); /* directories to search */
auto_object	= "/dgd/lib/object";	/* auto inherited object */
create		= "_F_create";		/* name of create function */
modules = ([ "/x/ext.so" : "" ]);
`

func TestDiscoverWalksUpFromThePath(t *testing.T) {
	m := vfs.Mem{
		"/mud/dgdtools.yml":         "root: lib\nprofile_build:\n  driver_config: etc/mud.dgd\n",
		"/mud/etc/mud.dgd":          driverCfg,
		"/mud/lib/d/Foo/castle.c":   "",
		"/other/.dgdtools.yml":      "include_dirs: [/inc]\n",
		"/other/src/x.c":            "",
		"/nowhere/deep/down/y.c":    "",
		"/mud/lib/sub/dgdtools.yml": "root: .\n",
		"/mud/lib/sub/z.c":          "",
	}

	// From a file deep in the lib, from any working directory.
	prj, err := Discover(m, []string{"/mud/lib/d/Foo/castle.c"}, DiscoverOptions{})
	require.NoError(t, err)
	assert.Equal(t, "/mud/dgdtools.yml", prj.ConfigPath)
	assert.Equal(t, "/mud/lib", prj.Root)
	// The driver config is the compile database.
	assert.Equal(t, []string{"/dgd/include", "/std/include"}, prj.Config.IncludeDirs)
	assert.Equal(t, "/dgd/include/Std.h", prj.Config.IncludeFile)
	assert.Equal(t, []string{"/dgd/lib/object"}, prj.Config.AutoObjects)
	assert.Equal(t, "_F_create", prj.Config.Create)
	require.NotNil(t, prj.Config.Dialect.Typechecking)
	assert.Equal(t, 1, *prj.Config.Dialect.Typechecking)

	// The alternate file name; a directory argument.
	prj, err = Discover(m, []string{"/other/src"}, DiscoverOptions{})
	require.NoError(t, err)
	assert.Equal(t, "/other/.dgdtools.yml", prj.ConfigPath)
	assert.Equal(t, "/other", prj.Root)
	assert.Nil(t, prj.Config.Dialect.Typechecking)

	// No config anywhere: no root is invented.
	prj, err = Discover(m, []string{"/nowhere/deep/down/y.c"}, DiscoverOptions{})
	require.NoError(t, err)
	assert.Empty(t, prj.ConfigPath)
	assert.Empty(t, prj.Root)

	// An explicit root without a config.
	prj, err = Discover(m, []string{"/nowhere/deep/down/y.c"}, DiscoverOptions{Root: "/nowhere"})
	require.NoError(t, err)
	assert.Equal(t, "/nowhere", prj.Root)

	// --no-config ignores a config that exists; --config forces one.
	prj, err = Discover(m, []string{"/mud/lib/d/Foo/castle.c"}, DiscoverOptions{NoConfig: true})
	require.NoError(t, err)
	assert.Empty(t, prj.Root)
	prj, err = Discover(m, []string{"/nowhere/deep/down/y.c"}, DiscoverOptions{ConfigPath: "/mud/dgdtools.yml"})
	require.NoError(t, err)
	assert.Equal(t, "/mud/lib", prj.Root)

	// The nearest root config wins; paths from two projects are refused.
	prj, err = Discover(m, []string{"/mud/lib/sub/z.c"}, DiscoverOptions{})
	require.NoError(t, err)
	assert.Equal(t, "/mud/lib/sub/dgdtools.yml", prj.ConfigPath)
	_, err = Discover(m, []string{"/mud/lib/sub/z.c", "/other/src/x.c"}, DiscoverOptions{})
	assert.ErrorContains(t, err, "different projects")

	// A root that is not a directory is an error, not a silent no-op.
	_, err = Discover(m, nil, DiscoverOptions{NoConfig: true, Root: "/does/not/exist"})
	assert.ErrorContains(t, err, "not a directory")
}

func TestExplicitKeysBeatDriverConfig(t *testing.T) {
	m := vfs.Mem{
		"/mud/dgdtools.yml": "profile_build:\n  driver_config: mud.dgd\ninclude_dirs: [/mine]\ncreate: init\ndialect: {typechecking: 2}\n",
		"/mud/mud.dgd":      driverCfg,
		"/mud/a.c":          "",
	}
	prj, err := Discover(m, []string{"/mud/a.c"}, DiscoverOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"/mine"}, prj.Config.IncludeDirs)
	assert.Equal(t, 2, *prj.Config.Dialect.Typechecking)
	assert.Equal(t, "init", prj.Config.Create)
	assert.Equal(t, "/dgd/include/Std.h", prj.Config.IncludeFile)
}

func TestExtendsAndRuleSettings(t *testing.T) {
	m := vfs.Mem{
		"/shared/base.yml": `root: lib
exclude: ["attic/**"]
conventions:
  specifier_macros: [public]
  call_registry: {add_action: {function: 0}}
lint:
  rules:
    syntax: error
    raw-inherit-path: { severity: warning, deny: ["/std/"] }
    lifecycle-chain: off
`,
		"/shared/lib/x.c": "",
		"/proj/dgdtools.yml": `extends: ../shared/base.yml
exclude: ["**/old/**"]
conventions:
  call_registry: {store_fp: [{object: 0, function: 1}, {function: 0}]}
  macros: { FOREACH: statement }
  defines: [__FEATURE__]
lint:
  rules:
    lifecycle-chain: { names: [create, reset] }
    E001: info
  overrides:
    - paths: ["players/**"]
      rules: { runtime: off, adjacent-strings: warning }
`,
	}
	c, err := Load(m, "/proj/dgdtools.yml")
	require.NoError(t, err)
	// root from the base is relative to the base file.
	assert.Equal(t, "/shared/lib", c.AbsRoot())
	// Lists replace, maps merge.
	assert.Equal(t, []string{"**/old/**"}, c.Exclude)
	zero := 0
	assert.Equal(t, map[string]profile.CallForms{
		"add_action": {{Function: 0}},
		"store_fp":   {{Object: &zero, Function: 1}, {Function: 0}},
	}, c.Conventions.CallRegistry)
	// Scalar and mapping rule settings.
	assert.Equal(t, "error", c.Lint.Rules["syntax"].Severity)
	assert.Equal(t, "info", c.Lint.Rules["E001"].Severity)
	assert.Equal(t, []string{"/std/"}, c.Lint.Rules["raw-inherit-path"].Deny)
	assert.Equal(t, []string{"create", "reset"}, c.Lint.Rules["lifecycle-chain"].Names)
	require.Len(t, c.Lint.Overrides, 1)
	assert.Equal(t, "off", c.Lint.Overrides[0].Rules["runtime"].Severity)
	assert.Equal(t, "statement", c.Conventions.Macros["FOREACH"])

	// A misspelled key is an error, not a silently ignored setting.
	m["/typo/dgdtools.yml"] = "include_dir: [/x]\n"
	_, err = Load(m, "/typo/dgdtools.yml")
	assert.ErrorContains(t, err, "include_dir")
	// So is a project key in the lint section, where it does not belong.
	m["/typo/dgdtools.yml"] = "lint:\n  include_dirs: [/x]\n"
	_, err = Load(m, "/typo/dgdtools.yml")
	assert.ErrorContains(t, err, "include_dirs")

	// Cycles are reported.
	m["/loop/a.yml"] = "extends: b.yml\n"
	m["/loop/b.yml"] = "extends: a.yml\n"
	_, err = Load(m, "/loop/a.yml")
	assert.ErrorContains(t, err, "cycle")
}

// The documented examples must stay loadable and name only real settings.
func TestExampleConfigsLoad(t *testing.T) {
	c, err := Load(vfs.OS{}, "../../dgdtools.example.yml")
	require.NoError(t, err)
	assert.Equal(t, "mud.dgd", c.ProfileBuild.DriverConfig)
	assert.Equal(t, "root", c.RelativeObjectPaths)
	assert.Empty(t, c.Conventions.SpecifierMacros)
	assert.Equal(t, Format{IndentSpace, 4, LineEndingsPreserve}, c.Format)
	assert.Equal(t, "warning", c.Lint.Rules["raw-inherit-path"].Severity)
	require.Len(t, c.Lint.Overrides, 1)
	assert.Equal(t, "off", c.Lint.Overrides[0].Rules["lifecycle-chain"].Severity)

	c, err = Load(vfs.OS{}, "../../examples/viking/dgdtools.yml")
	require.NoError(t, err)
	assert.Equal(t, "etc/config.viking", c.ProfileBuild.DriverConfig)
	assert.Contains(t, c.ProfileBuild.Share, "std/**")
	assert.Equal(t, []string{"public"}, c.Conventions.SpecifierMacros)
	zero := 0
	assert.Equal(t, map[string]profile.CallForms{
		"add_action": {{Function: 0}},
		"store_fp":   {{Object: &zero, Function: 1}, {Function: 0}},
	}, c.Conventions.CallRegistry)
	assert.Equal(t, []string{"create"}, c.Lint.Rules["lifecycle-chain"].Names)
	assert.Contains(t, c.Exclude, "attic/**")
}

// What a call registry entry may say.
func TestCallRegistry(t *testing.T) {
	load := func(entry string) (*Config, error) {
		return Load(vfs.Mem{"/p/dgdtools.yml": "conventions:\n  call_registry:\n    f: " + entry + "\n"}, "/p/dgdtools.yml")
	}
	one, two := 1, 2
	c, err := load("{function: 0}")
	require.NoError(t, err)
	assert.Equal(t, profile.CallForms{{Function: 0}}, c.Conventions.CallRegistry["f"])
	c, err = load("[{object: 1, function: 0, args: 2}, {function: 1}]")
	require.NoError(t, err)
	assert.Equal(t, profile.CallForms{{Object: &one, Function: 0, Args: &two}, {Function: 1}}, c.Conventions.CallRegistry["f"])

	for entry, want := range map[string]string{
		"0":                         "{function: N}",
		"{object: 0}":               "needs function",
		"{function: 0, obj: 1}":     `unknown key "obj"`,
		"{function: -1}":            "0-based argument index",
		"{function: first}":         "0-based argument index",
		"[]":                        "at least one form",
		"[{function: 0}, 1]":        "{function: N}",
		"{function: 0, args: 1.5}":  "0-based argument index",
		"{function: 0, object: no}": "0-based argument index",
	} {
		_, err := load(entry)
		assert.ErrorContains(t, err, want, entry)
		assert.ErrorContains(t, err, "line 3", entry)
	}
}

func TestInferCheckout(t *testing.T) {
	pats := []string{"players/*", "d/*"}
	dir, lib, ok := InferCheckout("/home/me/mud/players/drake/rooms/east", pats)
	assert.True(t, ok)
	assert.Equal(t, "/home/me/mud/players/drake", dir)
	assert.Equal(t, "/players/drake", lib)
	_, lib, ok = InferCheckout("/srv/d/Wild", pats)
	assert.True(t, ok)
	assert.Equal(t, "/d/Wild", lib)
	// players/* is tried first, so a stray /d/ higher up does not win.
	_, lib, _ = InferCheckout("/d/home/players/drake", pats)
	assert.Equal(t, "/players/drake", lib)
	_, _, ok = InferCheckout("/home/me/drake", pats)
	assert.False(t, ok)
	_, _, ok = InferCheckout("/home/me/players", pats)
	assert.False(t, ok)
}
