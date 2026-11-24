package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/vfs"
)

const driverCfg = `telnet_port	= 2001;			/* telnet port number */
directory	= "/usr/local/mud/lib";
typechecking	= 1;			/* global typechecking */
include_file	= "/dgd/include/Std.h";	/* standard include file */
include_dirs	= ({ "/dgd/include",
		     "/std/include" }); /* directories to search */
auto_object	= "/dgd/lib/object";	/* auto inherited object */
modules = ([ "/x/ext.so" : "" ]);
`

func TestDiscoverWalksUpFromThePath(t *testing.T) {
	m := vfs.Mem{
		"/mud/dgdtools.yml":         "root: lib\nprofile_build:\n  driver_config: etc/mud.dgd\n",
		"/mud/etc/mud.dgd":          driverCfg,
		"/mud/lib/d/Foo/castle.c":   "",
		"/other/.dgdtools.yml":      "lint:\n  include_dirs: [/inc]\n",
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
	assert.Equal(t, []string{"/dgd/include", "/std/include"}, prj.Config.Lint.IncludeDirs)
	assert.Equal(t, "/dgd/include/Std.h", prj.Config.Lint.IncludeFile)
	assert.Equal(t, []string{"/dgd/lib/object"}, prj.Config.Lint.AutoObjects)
	require.NotNil(t, prj.Config.Lint.Typechecking)
	assert.Equal(t, 1, *prj.Config.Lint.Typechecking)

	// The alternate file name; a directory argument.
	prj, err = Discover(m, []string{"/other/src"}, DiscoverOptions{})
	require.NoError(t, err)
	assert.Equal(t, "/other/.dgdtools.yml", prj.ConfigPath)
	assert.Equal(t, "/other", prj.Root)
	assert.Nil(t, prj.Config.Lint.Typechecking)

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

	// The nearest config wins; paths from two projects are refused.
	prj, err = Discover(m, []string{"/mud/lib/sub/z.c"}, DiscoverOptions{})
	require.NoError(t, err)
	assert.Equal(t, "/mud/lib/sub/dgdtools.yml", prj.ConfigPath)
	_, err = Discover(m, []string{"/mud/lib/sub/z.c", "/other/src/x.c"}, DiscoverOptions{})
	assert.ErrorContains(t, err, "different projects")

	// A root that is not a directory is an error, not a silent no-op.
	_, err = Discover(m, nil, DiscoverOptions{NoConfig: true, Root: "/does/not/exist"})
	assert.ErrorContains(t, err, "not a directory")
}

func TestExplicitLintSettingsBeatDriverConfig(t *testing.T) {
	m := vfs.Mem{
		"/mud/dgdtools.yml": "profile_build:\n  driver_config: mud.dgd\nlint:\n  include_dirs: [/mine]\n  typechecking: 2\n",
		"/mud/mud.dgd":      driverCfg,
		"/mud/a.c":          "",
	}
	prj, err := Discover(m, []string{"/mud/a.c"}, DiscoverOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"/mine"}, prj.Config.Lint.IncludeDirs)
	assert.Equal(t, 2, *prj.Config.Lint.Typechecking)
	assert.Equal(t, "/dgd/include/Std.h", prj.Config.Lint.IncludeFile)
}

func TestExtendsAndRuleSettings(t *testing.T) {
	m := vfs.Mem{
		"/shared/base.yml": `root: lib
exclude: ["attic/**"]
lint:
  specifier_macros: [public]
  call_registry: {add_action: 0}
  rules:
    syntax: error
    raw-inherit-path: { severity: warning, deny: ["/std/"] }
    lifecycle-chain: off
`,
		"/shared/lib/x.c": "",
		"/proj/dgdtools.yml": `extends: ../shared/base.yml
exclude: ["**/old/**"]
lint:
  call_registry: {store_fp: 0}
  rules:
    lifecycle-chain: { names: [create, reset] }
    E001: info
  overrides:
    - paths: ["players/**"]
      rules: { runtime: off, adjacent-strings: warning }
  macros: { FOREACH: statement }
  defines: [__FEATURE__]
`,
	}
	c, err := Load(m, "/proj/dgdtools.yml")
	require.NoError(t, err)
	// root from the base is relative to the base file.
	assert.Equal(t, "/shared/lib", c.AbsRoot())
	// Lists replace, maps merge.
	assert.Equal(t, []string{"**/old/**"}, c.Exclude)
	assert.Equal(t, map[string]int{"add_action": 0, "store_fp": 0}, c.Lint.CallRegistry)
	// Scalar and mapping rule settings.
	assert.Equal(t, "error", c.Lint.Rules["syntax"].Severity)
	assert.Equal(t, "info", c.Lint.Rules["E001"].Severity)
	assert.Equal(t, []string{"/std/"}, c.Lint.Rules["raw-inherit-path"].Deny)
	assert.Equal(t, []string{"create", "reset"}, c.Lint.Rules["lifecycle-chain"].Names)
	require.Len(t, c.Lint.Overrides, 1)
	assert.Equal(t, "off", c.Lint.Overrides[0].Rules["runtime"].Severity)
	assert.Equal(t, "statement", c.Lint.Macros["FOREACH"])

	// A misspelled key is an error, not a silently ignored setting.
	m["/typo/dgdtools.yml"] = "lint:\n  include_dir: [/x]\n"
	_, err = Load(m, "/typo/dgdtools.yml")
	assert.ErrorContains(t, err, "include_dir")

	// Cycles are reported.
	m["/loop/a.yml"] = "extends: b.yml\n"
	m["/loop/b.yml"] = "extends: a.yml\n"
	_, err = Load(m, "/loop/a.yml")
	assert.ErrorContains(t, err, "cycle")
}

// The documented example must stay loadable and name only real settings.
func TestExampleConfigLoads(t *testing.T) {
	c, err := Load(vfs.OS{}, "../../dgdtools.example.yml")
	require.NoError(t, err)
	assert.Equal(t, "etc/config.viking", c.ProfileBuild.DriverConfig)
	assert.Contains(t, c.ProfileBuild.Share, "std/**")
	assert.Equal(t, "root", c.Lint.RelativeObjectPaths)
	assert.Equal(t, "warning", c.Lint.Rules["raw-inherit-path"].Severity)
	require.Len(t, c.Lint.Overrides, 1)
	assert.Equal(t, "off", c.Lint.Overrides[0].Rules["lifecycle-chain"].Severity)
	assert.Contains(t, c.Exclude, "**/attic/**")
}

func TestLegacyConfigStillLoads(t *testing.T) {
	// The shape of a dgdtools.yml written for the previous dgdlint.
	m := vfs.Mem{"/mud/dgdtools.yml": `dialect: {slash_slash: true, closures: false}
root: lib
exclude: ["attic/**"]
lint:
  include_dirs: ["/dgd/include", "/std/include"]
  include_file: "/dgd/include/Std.h"
  auto_objects: ["/dgd/lib/object"]
  specifier_macros: [public]
  call_registry: {store_fp: 0, add_action: 0}
  object_registry: {}
  format_registry: {}
  autosave_markers: [set_auto_save, D_STORE]
  virtual_paths: []
  enable: []
  disable: []
  rules:
    raw-inherit-path: { severity: warning, deny: ["/std/", "/obj/"] }
    lifecycle-chain: { names: [create] }
  path_rules:
    - paths: ["legacy/**"]
      disable: [lifecycle-chain]
  fail_on: error
`}
	c, err := Load(m, "/mud/dgdtools.yml")
	require.NoError(t, err)
	assert.Equal(t, "/mud/lib", c.AbsRoot())
	assert.Equal(t, "warning", c.Lint.Rules["raw-inherit-path"].Severity)
	assert.Len(t, c.Lint.PathRules, 1)
	assert.True(t, c.TokenDialect().SlashSlash)
}
