package lsp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/lsp"
	"github.com/mmcdole/dgdtools/pkg/lsp/lsptest"
	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// resolveLib is a lib with something of everything a place can name,
// with characters that are more than a byte and more than a UTF-16 unit
// before the names, and a file that is Latin-1 on disk.
func resolveLib() map[string]string {
	files := lib()
	files["dgdtools.yml"] += "conventions:\n  call_registry:\n    add_action: { function: 0 }\n"
	files["include/std.h"] = "/* blåbær 🫐 */ #define STD \"/std/base\"\n#define TWICE(x) ((x)+(x))\n"
	files["std/base.c"] = "/* grøt */ int weight; /* 🥣 */ void create() {}\nint foo(int a) { return a; }\nstatic void sfn() {}\n"
	files["std/latin.c"] = "/* gr\xf8t \xe5 */ void old() {}\nint \xe6; int age;\n"
	files["obj/a.c"] = "#include <std.h>\ninherit STD;\ninherit l \"/std/latin\";\n" +
		"void create() { /* først 🫐 */ ::create(); l::old(); \"/obj/b\"->bar(); this_object()->foo(1,2); call_out(\"cb\", 1);\n" +
		"  autof(); weight = TWICE(age) + AUTO; add_action(\"sfn\", \"🥣\"); \"/obj/b\"->nofn(); x->y(); sizeof(({})); }\n" +
		"void cb() { int weight; weight = 1; }\n"
	return files
}

// definition asks the server where what is at the byte offset off of the
// open document at path is declared.
func definition(t testing.TB, c *lsptest.Client, path string, off int) []lsp.LocationLink {
	t.Helper()
	var links []lsp.LocationLink
	require.NoError(t, c.Call("textDocument/definition", lsp.DefinitionParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: lsptest.URI(path)},
		Position:     lsptest.PositionOf(c.Doc(path).Text, off, c.Encoding),
	}, &links))
	return links
}

// The server's definitions are the library's: for every place of every
// file of a lib, in both encodings, what textDocument/definition answers
// is what lint.Document.Resolve says of a fresh lint, with the positions
// worked out by the test from the texts. The lib has characters that
// are more than a byte and more than a UTF-16 unit before the names, and
// a file that is Latin-1 on disk.
func TestDefinitionIsResolve(t *testing.T) {
	files := resolveLib()
	dir := tree(t, files)

	fsys := vfs.OS{}
	prj, err := config.Discover(fsys, []string{dir}, config.DiscoverOptions{})
	require.NoError(t, err)
	l, err := lint.New(prj, fsys)
	require.NoError(t, err)
	disk := func(path string) string {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		return string(data)
	}

	for _, enc := range [][]string{nil, {lsp.UTF16, lsp.UTF8}} {
		c := start(t)
		c.Initialize(lsptest.Capabilities{Encodings: enc}, dir)
		kinds, places := map[string]int{}, 0
		for rel, text := range files {
			if !utf8.ValidString(text) || filepath.Ext(rel) == ".yml" {
				continue
			}
			path := filepath.Join(dir, filepath.FromSlash(rel))
			c.Open(path, text)
			doc, err := l.Lint(context.Background(), path, lint.Options{})
			require.NoError(t, err)
			for off := 0; off <= len(text); off++ {
				if off < len(text) && !utf8.RuneStart(text[off]) {
					continue
				}
				var want []lsp.LocationLink
				if ref := doc.Resolve(uint32(off)); ref != nil {
					for _, d := range ref.Defs {
						there := disk(d.Path)
						rng := lsp.Range{Start: lsptest.PositionOf(there, int(d.Start), c.Encoding), End: lsptest.PositionOf(there, int(d.End), c.Encoding)}
						want = append(want, lsp.LocationLink{
							OriginSelectionRange: lsp.Range{Start: lsptest.PositionOf(text, int(ref.Start), c.Encoding), End: lsptest.PositionOf(text, int(ref.End), c.Encoding)},
							TargetURI:            lsptest.URI(d.Path), TargetRange: rng, TargetSelectionRange: rng,
						})
						kinds[ref.Kind.String()]++
					}
				}
				require.Equal(t, want, definition(t, c, path, off), "%s at %d (%q)", rel, off, text[off:min(off+12, len(text))])
				places++
			}
			c.Close(path)
		}
		require.NoError(t, c.Shutdown())
		// The lib has something of everything.
		for _, kind := range []string{"include", "object", "label", "function", "variable", "macro"} {
			assert.NotZero(t, kinds[kind], kind)
		}
		t.Logf("%s: %d places, %v", c.Encoding, places, kinds)
	}
}

// A wizard's folder with a profile: a declaration that is the profile's
// is at the stub's URI, in the text the server gives for it.
func TestDefinitionInStub(t *testing.T) {
	p := &profile.Profile{
		Manifest: profile.Manifest{Name: "Mini Lib", Dialect: profile.Dialect{Driver: "dgd"},
			IncludeDirs: []string{"/include"}, AutoObjects: []string{"/sys/auto"}, Covered: []string{"/std/", "/include/", "/sys/"}},
		Files: map[string]string{
			"/include/std.h": "/* å */ #define ROOM \"/std/room\"\n",
			"/sys/auto.c":    "void autof();\n",
			"/std/room.c":    "inherit \"/std/object\";\n\nint room_var;\n/* å */ void set_short(string s);\n",
			"/std/object.c":  "void create();\n",
		},
	}
	data, err := p.Marshal()
	require.NoError(t, err)
	const hall = "#include <std.h>\ninherit ROOM;\nvoid create() { ::create(); set_short(\"x\"); room_var = 1; autof(); \"/players/ann/other\"->there(); }\n"
	files := map[string]string{"ann/mini.profile": string(data), "ann/hall.c": hall, "ann/other.c": "void there() {}\n"}
	dir := tree(t, files)
	path := filepath.Join(dir, "ann/hall.c")

	c := start(t)
	c.Settings = func(uri string) any {
		return map[string]any{"profile": filepath.Join(dir, "ann/mini.profile"), "libPath": "/players/ann"}
	}
	c.Initialize(lsptest.Capabilities{}, filepath.Join(dir, "ann"))
	c.Open(path, hall)
	c.Idle() // the folder's settings have been asked for, and the project is the profile's
	const stubs = "dgdtools-profile://Mini%20Lib"
	// What is named at each place, and the file it is declared in.
	for at, want := range map[string][2]string{
		"<st|d.h>":        {"", stubs + "/include/std.h"},
		"inherit RO|OM":   {"", stubs + "/std/room.c"},
		"::cre|ate()":     {"create", stubs + "/std/object.c"},
		"set_sh|ort":      {"set_short", stubs + "/std/room.c"},
		"room_v|ar":       {"room_var", stubs + "/std/room.c"},
		"auto|f()":        {"autof", stubs + "/sys/auto.c"},
		"other\"->the|re": {"there", lsptest.URI(filepath.Join(dir, "ann/other.c"))},
	} {
		cursor := strings.IndexByte(at, '|')
		links := definition(t, c, path, strings.Index(hall, at[:cursor]+at[cursor+1:])+cursor)
		require.Len(t, links, 1, at)
		assert.Equal(t, want[1], links[0].TargetURI, at)
		// The text of the target, as an editor gets it.
		var text string
		if strings.HasPrefix(links[0].TargetURI, stubs) {
			var res lsp.TextDocumentContentResult
			require.NoError(t, c.Call(lsp.MethodTextDocumentContent, lsp.TextDocumentContentParams{URI: links[0].TargetURI}, &res), at)
			text = res.Text
		} else {
			text = files["ann/other.c"]
		}
		r := links[0].TargetSelectionRange
		assert.Equal(t, want[0], text[lsptest.OffsetOf(text, r.Start, c.Encoding):lsptest.OffsetOf(text, r.End, c.Encoding)], at)
	}
	require.NoError(t, c.Shutdown())
}

// What is asked of a document the server says nothing about, and what
// is asked badly.
func TestDefinitionOfNothing(t *testing.T) {
	dir := tree(t, map[string]string{"dgdtools.yml": "", "obj/a.c": "void f() { f(); }\n"})
	c := start(t)
	c.Initialize(lsptest.Capabilities{}, dir)
	c.Notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{TextDocument: lsp.TextDocumentItem{URI: "untitled:Untitled-1", Version: 1, Text: "void f() { f(); }\n"}})
	for _, uri := range []string{
		"untitled:Untitled-1",                        // open, and no file
		lsptest.URI(filepath.Join(dir, "obj/a.c")),   // a file, and not open
		"dgdtools-profile://nobody/std/room.c",       // a stub: shown, not analyzed
		lsptest.URI(filepath.Join(dir, "nowhere.c")), // nothing at all
	} {
		var result json.RawMessage
		require.NoError(t, c.Call("textDocument/definition", lsp.DefinitionParams{
			TextDocument: lsp.TextDocumentIdentifier{URI: uri}, Position: lsp.Position{Line: 0, Character: 12}}, &result), uri)
		assert.Equal(t, "null", string(result), uri)
	}
	err := c.Call("textDocument/definition", map[string]any{"textDocument": 3}, nil)
	assert.True(t, lsptest.IsCode(err, -32602), "%v", err)

	// A file that cannot be linted has no definitions, and no error an
	// editor would show for each move of the cursor.
	path := filepath.Join(dir, "obj/a.c")
	c.Open(path, "void f() { f(); }\n")
	assert.Len(t, definition(t, c, path, 12), 1)
	c.Idle()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "dgdtools.yml"), []byte("lint: {rules: {no-such-rule: error}}\n"), 0o644))
	c.WatchedFiles(2, filepath.Join(dir, "dgdtools.yml"))
	c.Idle()
	assert.Empty(t, definition(t, c, path, 12))
	require.NoError(t, c.Shutdown())
}
