package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lib writes a small project to a temp dir and returns its directory.
func lib(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	// macOS temp dirs are symlinked; resolve so paths compare equal.
	dir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	for rel, src := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(src), 0o644))
	}
	return dir
}

func dgdlint(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

const castle = `mapping c;
void create() {
    c["Quair"] = ({
        "a", "southeast",
        })
    c["Repairshop"] = ({ "b", "enter" });
}
`

func TestCastleFromAnywhere(t *testing.T) {
	dir := lib(t, map[string]string{
		"mud/dgdtools.yml":          "root: lib\n",
		"mud/lib/d/Foo/castle.c":    castle,
		"mud/lib/d/Foo/fine.c":      "void create() { }\n",
		"stray/castle.c":            castle,
		"mud/lib/std/placeholder.c": "int x;\n",
	})
	file := filepath.Join(dir, "mud/lib/d/Foo/castle.c")

	// A single file, named by absolute path: the project is found by
	// walking up from the file, not from the working directory.
	code, out, errs := dgdlint(t, file)
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "castle.c:5:11: error: missing ';' before 'c' [syntax-error]")
	assert.Contains(t, errs, "1 file(s): 1 error(s)")

	// A directory.
	code, out, _ = dgdlint(t, filepath.Join(dir, "mud/lib/d"))
	assert.Equal(t, 1, code)
	assert.Equal(t, 1, strings.Count(out, "\n"))

	// A clean file: exit 0, no output.
	code, out, _ = dgdlint(t, filepath.Join(dir, "mud/lib/d/Foo/fine.c"))
	assert.Equal(t, 0, code)
	assert.Empty(t, out)

	// No config anywhere: file-local mode still catches it, and says so.
	code, out, errs = dgdlint(t, filepath.Join(dir, "stray/castle.c"))
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "[syntax-error]")
	assert.Contains(t, errs, "file-local rules only")

	// --exit-zero, --quiet.
	code, _, errs = dgdlint(t, "--exit-zero", "-q", file)
	assert.Equal(t, 0, code)
	assert.Empty(t, errs)
}

func TestOutputFormats(t *testing.T) {
	dir := lib(t, map[string]string{
		"dgdtools.yml": "",
		"a.c":          "void f() {\n write(\"a\" \"b\");\n if (1) {\n}\n",
	})
	file := filepath.Join(dir, "a.c")

	code, out, _ := dgdlint(t, "--format", "json", file)
	assert.Equal(t, 1, code)
	var doc struct {
		Version     int
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

	// Legacy spelling of the flag.
	code, out2, _ := dgdlint(t, "-f", "json", file)
	assert.Equal(t, 1, code)
	assert.Equal(t, out, out2)

	_, out, _ = dgdlint(t, "--format", "sarif", file)
	var sarif map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &sarif))
	assert.Equal(t, "2.1.0", sarif["version"])
	assert.Contains(t, out, `"ruleId": "adjacent-strings"`)
	assert.Contains(t, out, `"relatedLocations"`)

	_, out, _ = dgdlint(t, "--format", "github", file)
	assert.Contains(t, out, "::error file=")
	assert.Contains(t, out, "title=dgdlint adjacent-strings::")

	code, _, errs := dgdlint(t, "--format", "xml", file)
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "unknown output format")

	// --output writes the report to a file.
	outFile := filepath.Join(dir, "report.json")
	code, out, _ = dgdlint(t, "--format", "json", "--output", outFile, file)
	assert.Equal(t, 1, code)
	assert.Empty(t, out)
	data, err := os.ReadFile(outFile)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"diagnostics"`)
}

func TestSelectionFlagsAndFailOn(t *testing.T) {
	dir := lib(t, map[string]string{
		"dgdtools.yml": "",
		"a.c":          "void f() {\n int x;\n if (x = 1) x == 2;\n}\n",
	})
	file := filepath.Join(dir, "a.c")

	// Warnings do not fail by default.
	code, out, _ := dgdlint(t, file)
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "[assignment-in-condition]")
	assert.Contains(t, out, "[no-effect-statement]")

	code, _, _ = dgdlint(t, "--fail-on", "warning", file)
	assert.Equal(t, 1, code)

	_, out, _ = dgdlint(t, "--select", "S002", file)
	assert.NotContains(t, out, "assignment-in-condition")
	assert.Contains(t, out, "no-effect-statement")

	_, out, _ = dgdlint(t, "--ignore", "suspicious", file)
	assert.Empty(t, out)

	// Flags after the path, old single-dash spellings.
	_, out, _ = dgdlint(t, file, "-disable", "no-effect-statement", "-enable", "missing-visibility")
	assert.NotContains(t, out, "no-effect-statement")
	assert.Contains(t, out, "[missing-visibility]")

	code, _, errs := dgdlint(t, "--select", "bogus", file)
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "unknown rule")
	code, _, errs = dgdlint(t, "--fail-on", "fatal", file)
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "unknown severity")
	code, _, _ = dgdlint(t, filepath.Join(dir, "missing.c"))
	assert.Equal(t, 2, code)
}

func TestBaselineWorkflow(t *testing.T) {
	dir := lib(t, map[string]string{
		"dgdtools.yml": "",
		"old.c":        "void f() {\n a = 1\n b = 2;\n}\n",
	})
	code, _, errs := dgdlint(t, "--write-baseline", dir)
	assert.Equal(t, 0, code)
	assert.Contains(t, errs, "wrote 1 finding(s)")
	assert.FileExists(t, filepath.Join(dir, "dgdlint-baseline.json"))

	// Legacy noise no longer gates...
	code, out, errs := dgdlint(t, dir)
	assert.Equal(t, 0, code)
	assert.Empty(t, out)
	assert.Contains(t, errs, "1 in baseline")

	// ...new breakage does.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.c"), []byte("void g() { x = 1 y = 2; }\n"), 0o644))
	code, out, _ = dgdlint(t, dir)
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "new.c")
	assert.NotContains(t, out, "old.c")

	code, out, _ = dgdlint(t, "--no-baseline", dir)
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "old.c")

	// Without a project there is nowhere to anchor a baseline.
	stray := lib(t, map[string]string{"x.c": "void f() { }\n"})
	code, _, errs = dgdlint(t, "--write-baseline", filepath.Join(stray, "x.c"))
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "needs a dgdtools.yml")
}

func TestProfileCommands(t *testing.T) {
	code, out, _ := dgdlint(t, "profile", "show")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "NAME.profile")
	assert.Contains(t, out, filepath.Join("dgdtools", "profiles"))

	dir := lib(t, map[string]string{
		"dgdtools.yml":            "root: lib\nprofile_build:\n  name: tiny\n  share: [\"std/**\"]\n  checkouts: [\"players/*\"]\ninclude_dirs: [/std/include]\n",
		"lib/std/include/m.h":     "#define I_BASE \"/std/base\"\n",
		"lib/std/base.c":          "void create() { }\nint query(int a) { return a; }\n",
		"lib/players/x/ignored.c": "void f() { }\n",
	})
	wiz := lib(t, map[string]string{
		"players/me/a.c": "#include <m.h>\ninherit I_BASE;\nvoid create() {\n ::create();\n I_BASE->query(1, 2);\n I_BASE->nope();\n}\n",
	})
	code, _, errs := dgdlint(t, "--config", filepath.Join(dir, "dgdtools.yml"), "profile", "build")
	assert.Equal(t, 0, code, errs)
	prof := filepath.Join(dir, "dgdlint.profile")
	assert.FileExists(t, prof)
	code, out, _ = dgdlint(t, "profile", "show", prof, "/std/base.c")
	assert.Equal(t, 0, code)
	assert.Equal(t, "void create() {}\nint query(int a) {}\n", out)

	// The wizard: their folder, the profile, nothing else.
	file := filepath.Join(wiz, "players/me/a.c")
	code, out, errs = dgdlint(t, "--profile", prof, file)
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "a.c:5:10: warning: 'query' in /std/base takes at most 1 argument(s)")
	assert.Contains(t, out, "a.c:6:10: error: function 'nope' is not defined in /std/base")
	assert.Contains(t, errs, "is /players/me")

	// By name, found through $DGDTOOLS_PROFILE_PATH.
	t.Setenv("DGDTOOLS_PROFILE_PATH", dir)
	require.NoError(t, os.Rename(prof, filepath.Join(dir, "tiny.profile")))
	prof = filepath.Join(dir, "tiny.profile")
	code, out, _ = dgdlint(t, "profile", "show", "tiny")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "dialect:      lpc dgd")
	assert.Contains(t, out, "complete for: /std/")
	code, out, _ = dgdlint(t, "--profile", "tiny", file)
	assert.Equal(t, 1, code)
	assert.Contains(t, out, "function 'nope' is not defined in /std/base")
	code, _, errs = dgdlint(t, "--profile", "nosuch", file)
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "not found")

	code, _, errs = dgdlint(t, "--profile", prof, "profile", "build")
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "needs the whole lib")
}

func TestInformationalCommands(t *testing.T) {
	code, out, _ := dgdlint(t, "rules")
	assert.Equal(t, 0, code)
	for _, want := range []string{"syntax:", "runtime:", "suspicious:", "convention:", "E001 syntax-error", "R001 callable-not-found"} {
		assert.Contains(t, out, want)
	}
	_, legacy, _ := dgdlint(t, "-rules")
	assert.Equal(t, out, legacy)

	code, out, _ = dgdlint(t, "explain", "E005")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "declaration-after-statement")
	assert.Contains(t, out, "dgdlint:disable-next-line")
	code, _, _ = dgdlint(t, "explain", "nope")
	assert.Equal(t, 2, code)

	dir := lib(t, map[string]string{
		"dgdtools.yml": "root: lib\ndialect: {typechecking: 1}\ninclude_dirs: [/include]\n",
		"lib/a.c":      "",
	})
	code, out, _ = dgdlint(t, "config", filepath.Join(dir, "lib/a.c"))
	assert.Equal(t, 0, code)
	assert.Contains(t, out, filepath.Join(dir, "dgdtools.yml"))
	assert.Contains(t, out, "lib root:      "+filepath.Join(dir, "lib"))
	assert.Contains(t, out, "typechecking:  1")
	assert.Contains(t, out, "incomplete", "the configured include dir does not exist")

	code, out, _ = dgdlint(t, "--version")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "dgdlint")

	// Two projects in one invocation are refused.
	other := lib(t, map[string]string{"dgdtools.yml": "", "b.c": ""})
	code, _, errs := dgdlint(t, filepath.Join(dir, "lib/a.c"), filepath.Join(other, "b.c"))
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "different projects")
}
