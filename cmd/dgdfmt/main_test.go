package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/profile"
)

// tree writes files to a temp dir and returns the directory.
func tree(t *testing.T, files map[string]string) string {
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

func dgdfmt(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(context.Background(), args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

const (
	messy     = "void f() {\nif (x)\ny();\n}\n"
	formatted = "void\nf()\n{\n    if (x)\n        y();\n}\n"
	indent2   = "void\nf()\n{\n  if (x)\n    y();\n}\n"
	tabbed    = "void\nf()\n{\n\tif (x)\n\t\ty();\n}\n"
)

func TestModes(t *testing.T) {
	dir := tree(t, map[string]string{
		"a.c":       messy,
		"b.c":       formatted,
		"sub/c.h":   messy,
		"sub/d.txt": "not source\n",
	})
	a, b := filepath.Join(dir, "a.c"), filepath.Join(dir, "b.c")

	// Default: the formatted source on stdout, nothing written.
	code, out, errs := dgdfmt(t, "", a)
	assert.Equal(t, 0, code, errs)
	assert.Equal(t, formatted, out)
	assert.Equal(t, messy, read(t, a))

	// -l lists what would change, by the name the user gave; a directory
	// stands for the .c and .h files under it.
	t.Chdir(dir)
	code, out, _ = dgdfmt(t, "", "-l", ".")
	assert.Equal(t, 0, code)
	assert.Equal(t, "a.c\nsub/c.h\n", filepath.ToSlash(out))

	// -d prints a diff.
	code, out, _ = dgdfmt(t, "", "-d", "a.c", "b.c")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "--- a.c.orig")
	assert.Contains(t, out, "+        y();")
	assert.NotContains(t, out, "b.c")

	// -w rewrites the files that change and only those.
	before, err := os.Stat(b)
	require.NoError(t, err)
	code, out, _ = dgdfmt(t, "", "-w", ".")
	assert.Equal(t, 0, code)
	assert.Empty(t, out)
	assert.Equal(t, formatted, read(t, a))
	assert.Equal(t, formatted, read(t, filepath.Join(dir, "sub/c.h")))
	after, err := os.Stat(b)
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime())
	code, out, _ = dgdfmt(t, "", "-l", ".")
	assert.Equal(t, 0, code)
	assert.Empty(t, out, "formatting is a fixed point")
}

func TestStdin(t *testing.T) {
	dir := tree(t, map[string]string{"dgdtools.yml": "format: {indent_width: 2}\n"})
	// Standard input is formatted with the working directory's project.
	t.Chdir(dir)
	code, out, _ := dgdfmt(t, messy)
	assert.Equal(t, 0, code)
	assert.Equal(t, indent2, out)

	code, _, errs := dgdfmt(t, messy, "-w")
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "cannot use -w with standard input")
}

func TestEachFileHasItsProject(t *testing.T) {
	dir := tree(t, map[string]string{
		"two/dgdtools.yml":           "root: lib\nformat: {indent_width: 2}\nexclude: [\"d/old/**\"]\n",
		"two/lib/d/a.c":              messy,
		"two/lib/d/old/gone.c":       messy,
		"two/lib/d/sub/deep.c":       messy,
		"four/dgdtools.yml":          "format: {indent_style: tab}\n",
		"four/b.c":                   messy,
		"none/c.c":                   messy,
		"two/lib/d/sub/dgdtools.yml": "format: {indent_width: 8}\n",
	})
	// From an unrelated working directory: the project is found from the
	// file, not from where dgdfmt runs.
	t.Chdir(filepath.Join(dir, "none"))
	two, four := filepath.Join(dir, "two/lib/d/a.c"), filepath.Join(dir, "four/b.c")
	code, out, errs := dgdfmt(t, "", two, four, "c.c")
	assert.Equal(t, 0, code, errs)
	assert.Equal(t, indent2+tabbed+formatted, out)

	// A directory: the excludes of its project are matched from the lib
	// root wherever the walk starts, and a file with a nearer config is
	// formatted by that one.
	code, out, _ = dgdfmt(t, "", "-l", filepath.Join(dir, "two/lib/d"))
	assert.Equal(t, 0, code)
	assert.Equal(t, []string{"a.c", "sub/deep.c"}, relLines(out, filepath.Join(dir, "two/lib/d")))
	code, out, _ = dgdfmt(t, "", filepath.Join(dir, "two/lib/d/sub/deep.c"))
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "\n        if (x)\n")

	// A file named explicitly is formatted even if excluded.
	code, out, _ = dgdfmt(t, "", filepath.Join(dir, "two/lib/d/old/gone.c"))
	assert.Equal(t, 0, code)
	assert.Equal(t, indent2, out)

	// --config replaces discovery.
	code, out, _ = dgdfmt(t, "", "-config", filepath.Join(dir, "four/dgdtools.yml"), two)
	assert.Equal(t, 0, code)
	assert.Equal(t, tabbed, out)
	code, out, _ = dgdfmt(t, "", "-no-config", two)
	assert.Equal(t, 0, code)
	assert.Equal(t, formatted, out)
}

func relLines(out, base string) []string {
	var rel []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		r, _ := filepath.Rel(base, l)
		rel = append(rel, filepath.ToSlash(r))
	}
	return rel
}

func TestProfileDialectAndConventions(t *testing.T) {
	p := &profile.Profile{Manifest: profile.Manifest{
		Format: profile.Format, Name: "test",
		Dialect:     profile.Dialect{Language: "lpc", Driver: "dgd", SlashSlash: true, Closures: true},
		Conventions: profile.Conventions{SpecifierMacros: []string{"visible"}},
	}}
	data, err := p.Marshal()
	require.NoError(t, err)
	const src = "visible function make() {\nreturn 0;\n}\n"
	dir := tree(t, map[string]string{
		"mine/test.profile": string(data),
		"mine/dgdtools.yml": "profile: test\n",
		"mine/a.c":          src,
		"bare/a.c":          src,
	})
	// With the profile `function` is a type and `visible` a specifier:
	// this is a function definition.
	code, out, errs := dgdfmt(t, "", filepath.Join(dir, "mine/a.c"))
	assert.Equal(t, 0, code, errs)
	assert.Equal(t, "visible function\nmake()\n{\n    return 0;\n}\n", out)
	// Without it the same text is not one.
	code, out, _ = dgdfmt(t, "", filepath.Join(dir, "bare/a.c"))
	assert.Equal(t, 0, code)
	assert.Equal(t, "visible function make() {\n    return 0;\n}\n", out)
	// A dialect flag overrides the project's.
	code, out, _ = dgdfmt(t, "", "-closures", filepath.Join(dir, "bare/a.c"))
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "\nmake()\n{\n")
}

func TestErrors(t *testing.T) {
	dir := tree(t, map[string]string{
		"ok.c":              messy,
		"bad.c":             "int x = \"unterminated;\n",
		"cfg/dgdtools.yml":  "format: {line_endings: dos}\n",
		"cfg/a.c":           messy,
		"typo/dgdtools.yml": "fromat: {indent_width: 2}\n",
		"typo/a.c":          messy,
	})
	// A file that cannot be formatted is reported and left alone; the
	// others are still done.
	code, out, errs := dgdfmt(t, "", "-w", filepath.Join(dir, "bad.c"), filepath.Join(dir, "ok.c"), filepath.Join(dir, "missing.c"))
	assert.Equal(t, 2, code)
	assert.Empty(t, out)
	assert.Contains(t, errs, "bad.c: file contains unlexable content")
	assert.Contains(t, errs, "missing.c: no such file or directory")
	assert.Equal(t, formatted, read(t, filepath.Join(dir, "ok.c")))
	assert.Equal(t, "int x = \"unterminated;\n", read(t, filepath.Join(dir, "bad.c")))

	// Configuration that does not make sense is an error, not a default.
	code, _, errs = dgdfmt(t, "", filepath.Join(dir, "cfg/a.c"))
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "unknown line endings \"dos\"")
	code, _, errs = dgdfmt(t, "", filepath.Join(dir, "typo/a.c"))
	assert.Equal(t, 2, code)
	assert.Contains(t, errs, "fromat")

	// So are flags.
	code, _, _ = dgdfmt(t, "", "-nonsense")
	assert.Equal(t, 2, code)

	code, out, _ = dgdfmt(t, "", "-version")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "dgdfmt")
}
