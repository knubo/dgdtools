package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// scoped is a lib with a root config above it, two settings scopes, one
// nested in the other, and a wizard's own root config.
func scoped() vfs.Mem {
	return vfs.Mem{
		"/mud/dgdtools.yml": "root: lib\nexclude: [\"attic/**\"]\nformat: {indent_width: 8, line_endings: lf}\n" +
			"lint:\n  rules: {missing-visibility: warning}\n  overrides:\n    - paths: [\"players/**\"]\n      rules: {unreachable-code: info}\n",
		"/mud/lib/std/room.c":           "",
		"/mud/lib/players/dgdtools.yml": "root: false\nformat: {indent_width: 2}\nlint:\n  rules: {missing-visibility: off}\n",
		"/mud/lib/players/ann/a.c":      "",
		"/mud/lib/players/bob/.dgdtools.yml": "root: false\nformat: {indent_style: tab}\n" +
			"lint:\n  rules: {missing-visibility: error}\n  overrides:\n    - paths: [\"old/**\"]\n      rules: {all: off}\n",
		"/mud/lib/players/bob/old/b.c":      "",
		"/mud/lib/players/cyd/dgdtools.yml": "profile: viking\nlib_path: /players/cyd\nformat: {line_endings: crlf}\nlint:\n  fail_on: warning\n",
		"/mud/lib/players/cyd/c.c":          "",
	}
}

func TestScopesBelongToTheProjectAbove(t *testing.T) {
	m := scoped()
	// A settings scope is passed over when the project is looked for.
	root, err := FindRoot(m, "/mud/lib/players/bob/old")
	require.NoError(t, err)
	assert.Equal(t, "/mud/dgdtools.yml", root)
	// Files under different scopes are one project.
	prj, err := Discover(m, []string{"/mud/lib/players/ann/a.c", "/mud/lib/players/bob/old/b.c", "/mud/lib/std/room.c"}, DiscoverOptions{})
	require.NoError(t, err)
	assert.Equal(t, "/mud/dgdtools.yml", prj.ConfigPath)
	assert.Equal(t, "/mud/lib", prj.Root)
	// A nested root config is still the project of the paths under it.
	root, err = FindRoot(m, "/mud/lib/players/cyd")
	require.NoError(t, err)
	assert.Equal(t, "/mud/lib/players/cyd/dgdtools.yml", root)
	_, err = Discover(m, []string{"/mud/lib/players/ann/a.c", "/mud/lib/players/cyd/c.c"}, DiscoverOptions{})
	assert.ErrorContains(t, err, "different projects")
}

func TestSettingsPerFile(t *testing.T) {
	m := scoped()
	prj, err := Discover(m, []string{"/mud/lib"}, DiscoverOptions{})
	require.NoError(t, err)
	settings := func(path string) *Settings {
		t.Helper()
		s, err := prj.Settings(path)
		require.NoError(t, err)
		return s
	}

	// No scope: the project's own settings, and the defaults.
	s := settings("/mud/lib/std/room.c")
	assert.Equal(t, Format{IndentSpace, 8, LineEndingsLF}, s.Format)
	assert.Equal(t, FormatSources{FromDefault, "/mud/dgdtools.yml", "/mud/dgdtools.yml"}, s.From)
	assert.Empty(t, s.Scopes)

	// One scope: its keys win, the rest comes from above.
	s = settings("/mud/lib/players/ann/a.c")
	assert.Equal(t, Format{IndentSpace, 2, LineEndingsLF}, s.Format)
	assert.Equal(t, "/mud/lib/players/dgdtools.yml", s.From.IndentWidth)
	require.Len(t, s.Scopes, 1)
	assert.Equal(t, "off", s.Scopes[0].Rules["missing-visibility"].Severity)

	// Two: per key the nearest that sets it; outermost first in Scopes.
	s = settings("/mud/lib/players/bob/old/b.c")
	assert.Equal(t, Format{IndentTab, 2, LineEndingsLF}, s.Format)
	assert.Equal(t, FormatSources{"/mud/lib/players/bob/.dgdtools.yml", "/mud/lib/players/dgdtools.yml", "/mud/dgdtools.yml"}, s.From)
	require.Len(t, s.Scopes, 2)
	assert.Equal(t, "/mud/lib/players", s.Scopes[0].Dir)
	assert.Equal(t, "/mud/lib/players/bob", s.Scopes[1].Dir)
	assert.Equal(t, []string{"old/**"}, s.Scopes[1].Overrides[0].Paths)
	// A file that does not exist yet has settings too (an editor buffer).
	assert.Equal(t, s.Format, settings("/mud/lib/players/bob/new/unsaved.c").Format)

	// A root config below the project being run counts as a scope: its
	// settings apply, its project keys do not.
	s = settings("/mud/lib/players/cyd/c.c")
	assert.Equal(t, Format{IndentSpace, 2, LineEndingsCRLF}, s.Format)
	require.Len(t, s.Scopes, 2)
	assert.Equal(t, "/mud/lib/players/cyd/dgdtools.yml", s.Scopes[1].Path)

	// The caller's settings win over every file.
	prj, err = Discover(m, []string{"/mud/lib"}, DiscoverOptions{Format: Format{IndentWidth: 3}})
	require.NoError(t, err)
	s = settings("/mud/lib/players/bob/old/b.c")
	assert.Equal(t, Format{IndentTab, 3, LineEndingsLF}, s.Format)
	assert.Equal(t, FromFlag, s.From.IndentWidth)
	_, err = Discover(m, []string{"/mud/lib"}, DiscoverOptions{Format: Format{IndentWidth: 99}})
	assert.ErrorContains(t, err, "indent width 99")

	// A config named by the caller is the whole configuration: no nested
	// file is looked for. Without configuration nothing is read at all.
	prj, err = Discover(m, []string{"/mud/lib"}, DiscoverOptions{ConfigPath: "/mud/dgdtools.yml"})
	require.NoError(t, err)
	s = settings("/mud/lib/players/bob/old/b.c")
	assert.Equal(t, Format{IndentSpace, 8, LineEndingsLF}, s.Format)
	assert.Empty(t, s.Scopes)
	prj, err = Discover(m, []string{"/mud/lib"}, DiscoverOptions{NoConfig: true})
	require.NoError(t, err)
	s = settings("/mud/lib/players/bob/old/b.c")
	assert.Equal(t, Format{DefaultIndentStyle, DefaultIndentWidth, DefaultLineEndings}, s.Format)
	assert.Equal(t, FormatSources{FromDefault, FromDefault, FromDefault}, s.From)
	assert.Empty(t, s.Scopes)

	// The lib somewhere else than the config: a scope at the lib root
	// applies, and nothing above the lib is looked at.
	m = vfs.Mem{
		"/proj/dgdtools.yml": "root: ../lib\n",
		"/lib/dgdtools.yml":  "root: false\nformat: {indent_width: 3}\n",
		"/dgdtools.yml":      "root: false\nformat: {indent_style: tab}\n",
		"/lib/std/a.c":       "",
		"/elsewhere/b.c":     "",
	}
	prj, err = Discover(m, nil, DiscoverOptions{WorkDir: "/proj"})
	require.NoError(t, err)
	assert.Equal(t, "/lib", prj.Root)
	assert.Equal(t, Format{IndentSpace, 3, LineEndingsPreserve}, settings("/lib/std/a.c").Format)
	// A file that is not the project's has the project's settings only.
	assert.Equal(t, Format{IndentSpace, 4, LineEndingsPreserve}, settings("/elsewhere/b.c").Format)
}

func TestScopeValidation(t *testing.T) {
	for name, c := range map[string]struct{ yml, want string }{
		"root path":      {"root: false\nexclude: [\"old/**\"]\n", "exclude belongs in the project's root config"},
		"profile":        {"root: false\nprofile: viking\n", "profile belongs in the project's root config"},
		"dialect":        {"root: false\ndialect: {closures: true}\n", "dialect belongs"},
		"driver fact":    {"root: false\ninclude_dirs: [/inc]\n", "include_dirs belongs"},
		"conventions":    {"root: false\nconventions: {defines: [X]}\n", "conventions belongs"},
		"build":          {"root: false\nprofile_build: {name: x}\n", "profile_build belongs"},
		"fail_on":        {"root: false\nlint: {fail_on: warning}\n", "lint.fail_on belongs"},
		"baseline":       {"root: false\nlint: {baseline: b.json}\n", "lint.baseline belongs"},
		"extended":       {"root: false\nextends: base.yml\n", "lib_path belongs"},
		"root true":      {"root: true\n", "root: true is not a setting"},
		"width":          {"format: {indent_width: 40}\n", "format.indent_width: indent width 40 is not between 1 and 16"},
		"style":          {"format:\n  indent_style: tabs\n", "line 2: unknown indent style \"tabs\""},
		"endings":        {"format: {line_endings: dos}\n", "unknown line endings \"dos\""},
		"format key":     {"format: {line_width: 80}\n", "line_width"},
		"fail_on value":  {"lint: {fail_on: fatal}\n", `lint.fail_on: unknown severity "fatal" (want info, warning or error)`},
		"object paths":   {"relative_object_paths: cwd\n", `relative_object_paths: unknown value "cwd" (want root or file)`},
		"macro class":    {"conventions:\n  macros: {FOREACH: statment}\n", `conventions.macros: FOREACH: unknown class "statment" (want expression, empty, specifier, type, string, statement, opaque)`},
		"typechecking":   {"dialect: {typechecking: 3}\n", "dialect.typechecking: 3 is not a typechecking level"},
		"registry index": {"conventions:\n  format_registry: {scan: -1}\n", "conventions.format_registry: scan: -1 is not an argument index"},
		"owners":         {"profile_build: {share_min_owners: -2}\n", "profile_build.share_min_owners"},
		"severity":       {"lint:\n  rules:\n    syntax: loud\n", `line 3: lint.rules: syntax: unknown severity "loud"`},
		"scope severity": {"root: false\nlint:\n  overrides:\n    - paths: [\"a/**\"]\n      rules: {all: none}\n", `lint.overrides[0].rules: all: unknown severity "none"`},
		"override paths": {"lint:\n  overrides:\n    - rules: {all: off}\n", "lint.overrides[0]: no paths"},
	} {
		m := vfs.Mem{"/p/sub/dgdtools.yml": c.yml, "/p/sub/base.yml": "lib_path: /players/x\n", "/p/sub/a.c": ""}
		_, err := Load(m, "/p/sub/dgdtools.yml")
		assert.ErrorContains(t, err, c.want, name)
		assert.ErrorContains(t, err, "/p/sub/dgdtools.yml", name)
	}

	// What a scope may hold, with a base it shares with others.
	m := vfs.Mem{
		"/p/dgdtools.yml":        "format: {indent_width: 8}\n",
		"/p/sub/dgdtools.yml":    "root: false\nextends: ../style.yml\nlint:\n  rules: {syntax: error}\n",
		"/p/style.yml":           "format: {indent_style: tab}\nlint:\n  overrides:\n    - paths: [\"x/**\"]\n      rules: {all: off}\n",
		"/p/sub/a.c":             "",
		"/p/bad/dgdtools.yml":    "root: false\nlib_path: /x\n",
		"/p/bad/deep/b.c":        "",
		"/p/quoted/dgdtools.yml": "root: \"false\"\n",
	}
	c, err := Load(m, "/p/sub/dgdtools.yml")
	require.NoError(t, err)
	assert.True(t, c.Scope)
	assert.Equal(t, IndentTab, c.Format.IndentStyle)
	assert.Len(t, c.Lint.Overrides, 1)
	// A quoted false is a directory name.
	c, err = Load(m, "/p/quoted/dgdtools.yml")
	require.NoError(t, err)
	assert.False(t, c.Scope)
	assert.Equal(t, "/p/quoted/false", c.AbsRoot())

	// A scope that is not valid fails the settings of the files under
	// it, and only those.
	prj, err := Discover(m, []string{"/p"}, DiscoverOptions{})
	require.NoError(t, err)
	_, err = prj.Settings("/p/bad/deep/b.c")
	assert.ErrorContains(t, err, "/p/bad/dgdtools.yml: lib_path belongs")
	s, err := prj.Settings("/p/sub/a.c")
	require.NoError(t, err)
	assert.Equal(t, Format{IndentTab, 8, LineEndingsPreserve}, s.Format)

	// A scope cannot be the project.
	_, err = Discover(m, []string{"/p"}, DiscoverOptions{ConfigPath: "/p/sub/dgdtools.yml"})
	assert.ErrorContains(t, err, "is a settings scope")
	// Fixed, it is read again once the cache is reset.
	m["/p/bad/dgdtools.yml"] = "root: false\nformat: {indent_width: 1}\n"
	_, err = prj.Settings("/p/bad/deep/b.c")
	assert.Error(t, err)
	prj.ResetSettings()
	s, err = prj.Settings("/p/bad/deep/b.c")
	require.NoError(t, err)
	assert.Equal(t, 1, s.Format.IndentWidth)
}

func TestEditorConfig(t *testing.T) {
	m := vfs.Mem{
		"/.editorconfig":         "[*]\nindent_size = 7\nend_of_line = crlf\n",
		"/mud/.editorconfig":     "root = true\n[*]\nindent_style = tab\nindent_size = tab\ntab_width = 8\nend_of_line = lf\n[*.h]\nindent_style = space\nindent_size = 2\n[legacy/**]\nend_of_line = cr\nindent_size = 99\n",
		"/mud/dgdtools.yml":      "format: {line_endings: preserve}\n",
		"/mud/std/a.c":           "",
		"/mud/sub/.editorconfig": "[*.c]\nindent_style = space\nindent_size = 3\n",
		"/mud/sub/dgdtools.yml":  "root: false\nformat: {indent_width: 5}\n",
		"/mud/sub/b.c":           "",
		"/bare/c.c":              "",
	}
	prj, err := Discover(m, []string{"/mud"}, DiscoverOptions{})
	require.NoError(t, err)
	settings := func(prj *Project, path string) *Settings {
		t.Helper()
		s, err := prj.Settings(path)
		require.NoError(t, err)
		return s
	}

	// What format: leaves unset comes from .editorconfig; what it sets
	// wins, however near the .editorconfig is.
	s := settings(prj, "/mud/std/a.c")
	assert.Equal(t, Format{IndentTab, 8, LineEndingsPreserve}, s.Format)
	assert.Equal(t, FormatSources{"/mud/.editorconfig", "/mud/.editorconfig", "/mud/dgdtools.yml"}, s.From)
	// Sections are matched against the file.
	assert.Equal(t, Format{IndentSpace, 2, LineEndingsPreserve}, settings(prj, "/mud/std/a.h").Format)
	// The nearer .editorconfig wins per property; a dgdtools.yml on the
	// way wins over both.
	s = settings(prj, "/mud/sub/b.c")
	assert.Equal(t, Format{IndentSpace, 5, LineEndingsPreserve}, s.Format)
	assert.Equal(t, FormatSources{"/mud/sub/.editorconfig", "/mud/sub/dgdtools.yml", "/mud/dgdtools.yml"}, s.From)
	// Values that mean nothing to the formatter are passed over.
	s = settings(prj, "/mud/legacy/old.c")
	assert.Equal(t, 4, s.Format.IndentWidth)
	assert.Equal(t, FromDefault, s.From.IndentWidth)

	// Without any dgdtools.yml, .editorconfig is all there is; the files
	// above one with root = true are not read.
	bare, err := Discover(m, []string{"/bare"}, DiscoverOptions{})
	require.NoError(t, err)
	assert.Equal(t, Format{IndentSpace, 7, LineEndingsCRLF}, settings(bare, "/bare/c.c").Format)
	delete(m, "/mud/dgdtools.yml")
	delete(m, "/mud/sub/dgdtools.yml")
	none, err := Discover(m, []string{"/mud"}, DiscoverOptions{})
	require.NoError(t, err)
	assert.Equal(t, Format{IndentTab, 8, LineEndingsLF}, settings(none, "/mud/std/a.c").Format)
	// A config named by the caller still leaves .editorconfig its part;
	// "no configuration" does not.
	m["/cfg.yml"] = "root: /mud\nformat: {indent_width: 6}\n"
	named, err := Discover(m, nil, DiscoverOptions{ConfigPath: "/cfg.yml"})
	require.NoError(t, err)
	assert.Equal(t, Format{IndentTab, 6, LineEndingsLF}, settings(named, "/mud/std/a.c").Format)
	isolated, err := Discover(m, []string{"/mud"}, DiscoverOptions{NoConfig: true})
	require.NoError(t, err)
	assert.Equal(t, Format{IndentSpace, 4, LineEndingsPreserve}, settings(isolated, "/mud/std/a.c").Format)
}

func TestUserConfigIsAFallback(t *testing.T) {
	m := vfs.Mem{
		"/home/u/.config/dgdtools/dgdtools.yml": "format: {indent_width: 2}\nconventions: {specifier_macros: [visible]}\nlint:\n  rules: {missing-visibility: warning}\n",
		"/work/mine/a.c":                        "",
		"/work/mine/sub/dgdtools.yml":           "root: false\nformat: {indent_style: tab}\n",
		"/work/mine/sub/b.c":                    "",
		"/proj/dgdtools.yml":                    "format: {line_endings: lf}\n",
		"/proj/c.c":                             "",
	}
	user := UserConfigPath(m, "", "/home/u")
	assert.Equal(t, "/home/u/.config/dgdtools/dgdtools.yml", user)
	assert.Empty(t, UserConfigPath(m, "/nowhere", "/home/other"))
	opt := DiscoverOptions{UserConfig: user}

	// No project config: the user's is the project's, but its directory
	// is not the project's, and no lib root comes of it.
	prj, err := Discover(m, []string{"/work/mine/a.c"}, opt)
	require.NoError(t, err)
	assert.True(t, prj.UserConfig)
	assert.Equal(t, user, prj.ConfigPath)
	assert.Empty(t, prj.Root)
	assert.Equal(t, "/work/mine", prj.Base)
	assert.Equal(t, []string{"visible"}, prj.Config.Conventions.SpecifierMacros)
	s, err := prj.Settings("/work/mine/a.c")
	require.NoError(t, err)
	assert.Equal(t, 2, s.Format.IndentWidth)
	// A settings scope adjusts it like any project's.
	s, err = prj.Settings("/work/mine/sub/b.c")
	require.NoError(t, err)
	assert.Equal(t, Format{IndentTab, 2, LineEndingsPreserve}, s.Format)

	// A project config: the user's is not read, let alone merged.
	prj, err = Discover(m, []string{"/proj/c.c"}, opt)
	require.NoError(t, err)
	assert.False(t, prj.UserConfig)
	assert.Equal(t, "/proj/dgdtools.yml", prj.ConfigPath)
	assert.Empty(t, prj.Config.Conventions.SpecifierMacros)
	s, err = prj.Settings("/proj/c.c")
	require.NoError(t, err)
	assert.Equal(t, Format{IndentSpace, 4, LineEndingsLF}, s.Format)
	// Nor with --no-config.
	prj, err = Discover(m, []string{"/work/mine/a.c"}, DiscoverOptions{UserConfig: user, NoConfig: true})
	require.NoError(t, err)
	assert.Empty(t, prj.ConfigPath)

	// It has no directory to be a lib: a relative root is refused, an
	// absolute one is the lib.
	m[user] = "root: lib\n"
	_, err = Discover(m, []string{"/work/mine/a.c"}, opt)
	assert.ErrorContains(t, err, "needs an absolute root")
	m[user] = "root: /proj\n"
	prj, err = Discover(m, []string{"/work/mine/a.c"}, opt)
	require.NoError(t, err)
	assert.Equal(t, "/proj", prj.Root)
}

func TestWorkspace(t *testing.T) {
	m := scoped()
	m["/other/dgdtools.yml"] = "format: {indent_width: 3}\n"
	m["/other/x.c"] = ""
	m["/loose/y.c"] = ""
	m["/broken/dgdtools.yml"] = "frmat: {}\n"
	m["/broken/z.c"] = ""
	ws := NewWorkspace(m, DiscoverOptions{})
	project := func(path string) *Project {
		t.Helper()
		prj, err := ws.Project(path)
		require.NoError(t, err)
		return prj
	}
	// Each path has the project of its nearest root config; a project is
	// resolved once.
	lib := project("/mud/lib/std/room.c")
	assert.Equal(t, "/mud/dgdtools.yml", lib.ConfigPath)
	assert.Same(t, lib, project("/mud/lib/players/bob/old/b.c"))
	assert.Same(t, lib, project("/mud/lib/players"))
	// A root config further down is the project of what is under it, and
	// resolved as one: this one names a profile that is not there.
	_, err := ws.Project("/mud/lib/players/cyd/c.c")
	assert.ErrorContains(t, err, `profile "viking" not found`)
	assert.Equal(t, "/other/dgdtools.yml", project("/other/x.c").ConfigPath)
	// Paths without one share a project without a config.
	loose := project("/loose/y.c")
	assert.Empty(t, loose.ConfigPath)
	assert.Same(t, loose, project("/elsewhere/q.c"))
	// A config that is not valid is the error of its paths only.
	_, err = ws.Project("/broken/z.c")
	assert.ErrorContains(t, err, "frmat")

	// With a config named by the caller there is one project.
	ws = NewWorkspace(m, DiscoverOptions{ConfigPath: "/other/dgdtools.yml"})
	one := project("/mud/lib/std/room.c")
	assert.Equal(t, "/other/dgdtools.yml", one.ConfigPath)
	assert.Same(t, one, project("/loose/y.c"))
}

// What a profile or the driver's configuration contributes is checked
// like what the file says, and the error names where it came from.
func TestContributedValuesAreValidated(t *testing.T) {
	m := vfs.Mem{
		"/p/dgdtools.yml": "profile: bad.profile\n",
		"/p/bad.profile":  "dgdtools-profile 1\n{\"name\":\"bad\",\"dialect\":{\"driver\":\"dgd\"},\"relative_object_paths\":\"sideways\",\"conventions\":{}}\n",
		"/p/a.c":          "",
		"/q/dgdtools.yml": "profile_build: {driver_config: mud.dgd}\n",
		"/q/mud.dgd":      "typechecking = 7;\n",
		"/q/a.c":          "",
	}
	_, err := Discover(m, []string{"/p/a.c"}, DiscoverOptions{})
	assert.ErrorContains(t, err, `/p/bad.profile: relative_object_paths: unknown value "sideways"`)
	_, err = Discover(m, []string{"/q/a.c"}, DiscoverOptions{})
	assert.ErrorContains(t, err, "/q/mud.dgd: dialect.typechecking: 7 is not a typechecking level")
}
