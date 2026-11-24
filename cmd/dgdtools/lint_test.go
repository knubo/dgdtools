package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const castle = `mapping c;
void create() {
    c["Quair"] = ({
        "a", "southeast",
        })
    c["Repairshop"] = ({ "b", "enter" });
}
`

func TestLintFromAnywhere(t *testing.T) {
	dir := tree(t, map[string]string{
		"mud/dgdtools.yml":          "root: lib\n",
		"mud/lib/d/Foo/castle.c":    castle,
		"mud/lib/d/Foo/fine.c":      "void create() { }\n",
		"stray/castle.c":            castle,
		"mud/lib/std/placeholder.c": "int x;\n",
	})
	file := filepath.Join(dir, "mud/lib/d/Foo/castle.c")

	// A single file, named by absolute path: the project is found by
	// walking up from the file, not from the working directory.
	code, out, errs := dgdtools(t, "", "lint", file)
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "castle.c:5:11: error: missing ';' before 'c' [syntax-error]")
	assert.Contains(t, errs, "1 file(s): 1 error(s)")

	// A directory.
	code, out, _ = dgdtools(t, "", "lint", filepath.Join(dir, "mud/lib/d"))
	assert.Equal(t, 1, code)
	assert.Equal(t, 1, strings.Count(out, "\n"))

	// No path: the project of the working directory, all of it.
	t.Chdir(filepath.Join(dir, "mud/lib/std"))
	code, out, errs = dgdtools(t, "", "lint")
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "castle.c:5:11")
	assert.Contains(t, errs, "3 file(s)")

	// A clean file: exit 0, no output.
	code, out, _ = dgdtools(t, "", "lint", filepath.Join(dir, "mud/lib/d/Foo/fine.c"))
	assert.Equal(t, 0, code)
	assert.Empty(t, out)

	// No config anywhere: file-local mode still catches it, and says so.
	code, out, errs = dgdtools(t, "", "lint", filepath.Join(dir, "stray/castle.c"))
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "[syntax-error]")
	assert.Contains(t, errs, "file-local rules only")

	// --exit-zero, --quiet; flags before and after the path; "--" ends
	// them.
	code, _, errs = dgdtools(t, "", "lint", "--exit-zero", "-q", file)
	assert.Equal(t, 0, code)
	assert.Empty(t, errs)
	code, _, errs = dgdtools(t, "", "lint", file, "--quiet", "--exit-zero")
	assert.Equal(t, 0, code)
	assert.Empty(t, errs)
	code, _, errs = dgdtools(t, "", "lint", "--", "--quiet")
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "--quiet: no such file or directory")

	// Two projects in one invocation are refused.
	other := tree(t, map[string]string{"dgdtools.yml": "", "b.c": ""})
	code, _, errs = dgdtools(t, "", "lint", file, filepath.Join(other, "b.c"))
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "dgdtools lint: paths belong to different projects")
}

func TestLintOutputFormats(t *testing.T) {
	dir := tree(t, map[string]string{
		"dgdtools.yml": "",
		"a.c":          "void f() {\n write(\"a\" \"b\");\n if (1) {\n}\n",
	})
	file := filepath.Join(dir, "a.c")

	code, out, _ := dgdtools(t, "", "lint", "--output-format", "json", file)
	assert.Equal(t, 1, code)
	var doc struct {
		Version     int
		Tool        string
		Diagnostics []struct {
			Rule, Code, Severity, Path, Message string
			Line, Col                           int
			Range                               struct{ Offset, End_Offset int }
			Related                             []struct{ Message string }
			Fixes                               []struct {
				Title string
				Edits []struct {
					Offset   int
					New_Text string
				}
			}
		}
		Summary struct{ Errors int }
	}
	require.NoError(t, json.Unmarshal([]byte(out), &doc), out)
	assert.Equal(t, 1, doc.Version)
	assert.Equal(t, "dgdtools dev", doc.Tool)
	require.Len(t, doc.Diagnostics, 2)
	adj := doc.Diagnostics[0]
	assert.Equal(t, "adjacent-strings", adj.Rule)
	assert.Equal(t, "E002", adj.Code)
	require.Len(t, adj.Fixes, 1)
	assert.Equal(t, " +", adj.Fixes[0].Edits[0].New_Text)
	brace := doc.Diagnostics[1]
	assert.Contains(t, brace.Message, "missing '}'")
	require.Len(t, brace.Related, 1)
	assert.Equal(t, 2, doc.Summary.Errors)

	_, out, _ = dgdtools(t, "", "lint", "--output-format", "sarif", file)
	var sarif map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &sarif))
	assert.Equal(t, "2.1.0", sarif["version"])
	assert.Contains(t, out, `"name": "dgdtools"`)
	assert.Contains(t, out, `"ruleId": "adjacent-strings"`)
	assert.Contains(t, out, `"relatedLocations"`)

	_, out, _ = dgdtools(t, "", "lint", "--output-format", "github", file)
	assert.Contains(t, out, "::error file=")
	assert.Contains(t, out, "title=dgdtools adjacent-strings::")

	code, _, errs := dgdtools(t, "", "lint", "--output-format", "xml", file)
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "unknown output format")

	// --output writes the report to a file.
	outFile := filepath.Join(dir, "report.json")
	code, out, _ = dgdtools(t, "", "lint", "--output-format", "json", "--output", outFile, file)
	assert.Equal(t, 1, code)
	assert.Empty(t, out)
	assert.Contains(t, read(t, outFile), `"diagnostics"`)
}

func TestLintSelectionAndFailOn(t *testing.T) {
	dir := tree(t, map[string]string{
		"dgdtools.yml": "",
		"a.c":          "void f() {\n int x;\n if (x = 1) x == 2;\n}\n",
	})
	file := filepath.Join(dir, "a.c")

	// Warnings do not fail by default.
	code, out, _ := dgdtools(t, "", "lint", file)
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "[assignment-in-condition]")
	assert.Contains(t, out, "[no-effect-statement]")

	code, _, _ = dgdtools(t, "", "lint", "--fail-on", "warning", file)
	assert.Equal(t, 1, code)

	_, out, _ = dgdtools(t, "", "lint", "--select", "S002", file)
	assert.NotContains(t, out, "assignment-in-condition")
	assert.Contains(t, out, "no-effect-statement")

	_, out, _ = dgdtools(t, "", "lint", "--ignore", "suspicious", file)
	assert.Empty(t, out)

	_, out, _ = dgdtools(t, "", "lint", file, "--ignore", "no-effect-statement", "--enable", "missing-visibility")
	assert.NotContains(t, out, "no-effect-statement")
	assert.Contains(t, out, "[missing-visibility]")

	code, _, errs := dgdtools(t, "", "lint", "--select", "bogus", file)
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "unknown rule \"bogus\" (see `dgdtools rules`)")
	code, _, errs = dgdtools(t, "", "lint", "--fail-on", "fatal", file)
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "unknown severity")
	code, _, _ = dgdtools(t, "", "lint", filepath.Join(dir, "missing.c"))
	assert.Equal(t, 2, code)
	// The old spellings are not flags.
	for _, old := range []string{"--disable", "--rules", "-f"} {
		code, _, _ = dgdtools(t, "", "lint", old, "x", file)
		assert.Equal(t, 2, code, old)
	}
}

func TestLintSettingsScopes(t *testing.T) {
	const bare = "int f() { return 1; }\n"
	dir := tree(t, map[string]string{
		"dgdtools.yml":             "lint:\n  rules: {missing-visibility: warning}\n  fail_on: error\n",
		"std/a.c":                  bare,
		"players/ann/dgdtools.yml": "root: false\nlint:\n  rules: {missing-visibility: error}\n",
		"players/ann/b.c":          bare,
		"players/bob/dgdtools.yml": "root: false\nlint:\n  rules: {missing-visibility: off}\n",
		"players/bob/c.c":          bare,
	})
	// Files under different scopes are one project and one run; each is
	// linted by its own settings.
	code, out, errs := dgdtools(t, "", "lint", filepath.Join(dir, "std/a.c"), filepath.Join(dir, "players/ann/b.c"), filepath.Join(dir, "players/bob/c.c"))
	assert.Equal(t, 1, code, errs)
	assert.Contains(t, out, "a.c:1:5: warning:")
	assert.Contains(t, out, "b.c:1:5: error:")
	assert.NotContains(t, out, "c.c")
	// From inside a scope the project is still the one above.
	t.Chdir(filepath.Join(dir, "players/bob"))
	code, out, _ = dgdtools(t, "", "lint")
	assert.Equal(t, 1, code)
	assert.Equal(t, 2, strings.Count(out, "\n"))

	// A project key in a scope is an error, and names the file.
	write(t, dir, map[string]string{"players/bob/dgdtools.yml": "root: false\ninclude_dirs: [/x]\n"})
	code, _, errs = dgdtools(t, "", "lint")
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, filepath.Join(dir, "players/bob/dgdtools.yml")+": include_dirs belongs in the project's root config")
	// So is a rule that does not exist.
	write(t, dir, map[string]string{"players/bob/dgdtools.yml": "root: false\nlint:\n  rules: {nope: off}\n"})
	code, _, errs = dgdtools(t, "", "lint")
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "unknown rule \"nope\"")
}

func TestLintBaseline(t *testing.T) {
	dir := tree(t, map[string]string{
		"dgdtools.yml": "",
		"old.c":        "void f() {\n a = 1\n b = 2;\n}\n",
	})
	code, _, errs := dgdtools(t, "", "lint", "--write-baseline", dir)
	assert.Equal(t, 0, code)
	assert.Contains(t, errs, "wrote 1 finding(s)")
	assert.FileExists(t, filepath.Join(dir, "dgdtools-baseline.json"))

	// Legacy noise no longer gates...
	code, out, errs := dgdtools(t, "", "lint", dir)
	assert.Equal(t, 0, code)
	assert.Empty(t, out)
	assert.Contains(t, errs, "1 in baseline")

	// ...new breakage does.
	write(t, dir, map[string]string{"new.c": "void g() { x = 1 y = 2; }\n"})
	code, out, _ = dgdtools(t, "", "lint", dir)
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "new.c")
	assert.NotContains(t, out, "old.c")

	code, out, _ = dgdtools(t, "", "lint", "--no-baseline", dir)
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "old.c")

	// Without a project there is nowhere to anchor a baseline, unless
	// one is named.
	stray := tree(t, map[string]string{"x.c": "void f() { a = 1 }\n"})
	code, _, errs = dgdtools(t, "", "lint", "--write-baseline", filepath.Join(stray, "x.c"))
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "needs a dgdtools.yml")
	named := filepath.Join(stray, "known.json")
	code, _, _ = dgdtools(t, "", "lint", "--write-baseline", "--baseline", named, filepath.Join(stray, "x.c"))
	assert.Equal(t, 0, code)
	assert.FileExists(t, named)
	code, out, _ = dgdtools(t, "", "lint", "--baseline", named, filepath.Join(stray, "x.c"))
	assert.Equal(t, 0, code)
	assert.Empty(t, out)
}

func TestProfileCommands(t *testing.T) {
	code, out, _ := dgdtools(t, "", "profile", "show")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "NAME.profile")
	assert.Contains(t, out, filepath.Join("dgdtools", "profiles"))
	code, _, errs := dgdtools(t, "", "profile")
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "usage: dgdtools profile build")

	dir := tree(t, map[string]string{
		"dgdtools.yml":            "root: lib\nprofile_build:\n  name: tiny\n  share: [\"std/**\"]\n  checkouts: [\"players/*\"]\ninclude_dirs: [/std/include]\n",
		"lib/std/include/m.h":     "#define I_BASE \"/std/base\"\n",
		"lib/std/base.c":          "void create() { }\nint query(int a) { return a; }\n",
		"lib/players/x/ignored.c": "void f() { }\n",
	})
	wiz := tree(t, map[string]string{
		"players/me/a.c": "#include <m.h>\ninherit I_BASE;\nvoid create() {\n ::create();\n I_BASE->query(1, 2);\n I_BASE->nope();\n}\n",
	})
	code, _, errs = dgdtools(t, "", "profile", "build", "--config", filepath.Join(dir, "dgdtools.yml"))
	assert.Equal(t, 0, code, errs)
	assert.Contains(t, errs, `profile "tiny"`)
	prof := filepath.Join(dir, "dgdtools.profile")
	assert.FileExists(t, prof)
	code, out, _ = dgdtools(t, "", "profile", "show", prof, "/std/base.c")
	assert.Equal(t, 0, code)
	assert.Equal(t, "void create() {}\nint query(int a) {}\n", out)
	// From inside the project, to a named file, quietly.
	t.Chdir(filepath.Join(dir, "lib/std"))
	elsewhere := filepath.Join(dir, "out.profile")
	code, _, errs = dgdtools(t, "", "profile", "build", "-o", elsewhere, "-q")
	assert.Equal(t, 0, code)
	assert.Empty(t, errs)
	assert.Equal(t, read(t, prof), read(t, elsewhere), "a profile depends only on the lib")

	// The wizard: their folder, the profile, nothing else.
	file := filepath.Join(wiz, "players/me/a.c")
	code, out, errs = dgdtools(t, "", "lint", "--profile", prof, file)
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "a.c:5:10: warning: 'query' in /std/base takes at most 1 argument(s)")
	assert.Contains(t, out, "a.c:6:10: error: function 'nope' is not defined in /std/base")
	assert.Contains(t, errs, "is /players/me")

	// By name, found through $DGDTOOLS_PROFILE_PATH.
	t.Setenv("DGDTOOLS_PROFILE_PATH", dir)
	require.NoError(t, os.Rename(prof, filepath.Join(dir, "tiny.profile")))
	prof = filepath.Join(dir, "tiny.profile")
	code, out, _ = dgdtools(t, "", "profile", "show", "tiny")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "dialect:      lpc dgd")
	assert.Contains(t, out, "complete for: /std/")
	code, out, _ = dgdtools(t, "", "lint", "--profile", "tiny", file)
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "function 'nope' is not defined in /std/base")
	code, _, errs = dgdtools(t, "", "lint", "--profile", "nosuch", file)
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "not found")
	code, _, errs = dgdtools(t, "", "profile", "show", "tiny", "/std/none.c")
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "has no stub for /std/none.c")

	t.Chdir(wiz)
	code, _, errs = dgdtools(t, "", "profile", "build", "--profile", prof)
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "needs the whole lib")
}
