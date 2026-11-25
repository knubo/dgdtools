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
	"github.com/mmcdole/dgdtools/pkg/resolve"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// references asks the server for the references of what is at the byte
// offset off of the open document at path.
func references(t testing.TB, c *lsptest.Client, path string, off int, decl bool) []lsp.Location {
	t.Helper()
	p := lsp.ReferenceParams{TextDocument: lsp.TextDocumentIdentifier{URI: lsptest.URI(path)},
		Position: lsptest.PositionOf(c.Doc(path).Text, off, c.Encoding)}
	p.Context.IncludeDeclaration = decl
	var locs []lsp.Location
	require.NoError(t, c.Call("textDocument/references", p, &locs))
	return locs
}

// highlights asks the server for the highlights of what is at the byte
// offset off of the open document at path.
func highlights(t testing.TB, c *lsptest.Client, path string, off int) []lsp.DocumentHighlight {
	t.Helper()
	var hs []lsp.DocumentHighlight
	require.NoError(t, c.Call("textDocument/documentHighlight", lsp.DocumentHighlightParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: lsptest.URI(path)},
		Position:     lsptest.PositionOf(c.Doc(path).Text, off, c.Encoding),
	}, &hs))
	return hs
}

// The server's references and highlights are the library's: for every
// place of every file of a lib, in both encodings, with and without the
// declarations, what textDocument/references answers is what
// lint.Document.References says of a fresh lint of the scanned lib, and
// what textDocument/documentHighlight answers is Document.Highlights,
// with the positions worked out by the test from the texts. The lib has
// characters that are more than a byte and more than a UTF-16 unit
// before the names, and a file that is Latin-1 on disk.
func TestReferencesAreTheLibrarys(t *testing.T) {
	files := resolveLib()
	dir := tree(t, files)
	fsys := vfs.OS{}
	prj, err := config.Discover(fsys, []string{dir}, config.DiscoverOptions{})
	require.NoError(t, err)
	l, err := lint.New(prj, fsys)
	require.NoError(t, err)
	require.True(t, l.Scan(context.Background()))
	disk := func(path string) string {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		return string(data)
	}

	for _, enc := range [][]string{nil, {lsp.UTF16, lsp.UTF8}} {
		c := start(t)
		c.Initialize(lsptest.Capabilities{Encodings: enc}, dir)
		places, found := 0, 0
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
				for _, decl := range []bool{false, true} {
					var want []lsp.Location
					if refs := doc.References(context.Background(), uint32(off), decl); refs != nil {
						want = []lsp.Location{}
						for _, s := range refs.Sites {
							there := disk(s.Path)
							want = append(want, lsp.Location{URI: lsptest.URI(s.Path), Range: lsp.Range{
								Start: lsptest.PositionOf(there, int(s.Start), c.Encoding), End: lsptest.PositionOf(there, int(s.End), c.Encoding)}})
						}
						found += len(refs.Sites)
					}
					require.Equal(t, want, references(t, c, path, off, decl), "%s at %d (%q), decl %v", rel, off, text[off:min(off+12, len(text))], decl)
				}
				var want []lsp.DocumentHighlight
				if sites := doc.Highlights(uint32(off)); sites != nil {
					want = []lsp.DocumentHighlight{}
					for _, s := range sites {
						kind := map[resolve.Access]int{resolve.Text: lsp.HighlightText, resolve.Read: lsp.HighlightRead, resolve.Write: lsp.HighlightWrite}[s.Access]
						want = append(want, lsp.DocumentHighlight{Kind: kind, Range: lsp.Range{
							Start: lsptest.PositionOf(text, int(s.Start), c.Encoding), End: lsptest.PositionOf(text, int(s.End), c.Encoding)}})
					}
				}
				require.Equal(t, want, highlights(t, c, path, off), "%s at %d (%q)", rel, off, text[off:min(off+12, len(text))])
				places++
			}
			c.Close(path)
		}
		require.NoError(t, c.Shutdown())
		assert.Greater(t, found, 200)
		t.Logf("%s: %d places, %d references found", c.Encoding, places, found)
	}
}

// A wizard's folder with a profile: the references are those of the
// checkout, a declaration in a stub among them at the stub's URI, and
// the server's log says the answer is the checkout's only, and that
// calls on objects that cannot be told are not listed.
func TestReferencesInCheckout(t *testing.T) {
	p := &profile.Profile{
		Manifest: profile.Manifest{Name: "Mini Lib", Dialect: profile.Dialect{Driver: "dgd"},
			IncludeDirs: []string{"/include"}, AutoObjects: []string{"/sys/auto"}, Covered: []string{"/std/", "/include/", "/sys/"}},
		Files: map[string]string{
			"/include/std.h": "#define ROOM \"/std/room\"\n",
			"/sys/auto.c":    "void autof();\n",
			"/std/room.c":    "inherit \"/std/object\";\n\nint room_var;\nvoid set_short(string s);\n",
			"/std/object.c":  "void create();\n",
		},
	}
	data, err := p.Marshal()
	require.NoError(t, err)
	const hall = "#include <std.h>\ninherit ROOM;\nvoid create() { set_short(\"x\"); ob->set_short(\"y\"); }\n"
	const yard = "inherit \"/std/room\";\nvoid create() { set_short(\"z\"); }\n"
	files := map[string]string{"ann/mini.profile": string(data), "ann/hall.c": hall, "ann/yard.c": yard}
	dir := tree(t, files)
	path := filepath.Join(dir, "ann/hall.c")

	c := start(t)
	c.Settings = func(uri string) any {
		return map[string]any{"profile": filepath.Join(dir, "ann/mini.profile"), "libPath": "/players/ann"}
	}
	c.Initialize(lsptest.Capabilities{}, filepath.Join(dir, "ann"))
	c.Open(path, hall)
	c.Idle()
	locs := references(t, c, path, strings.Index(hall, "set_short"), true)
	var got []string
	for _, l := range locs {
		got = append(got, strings.TrimPrefix(l.URI, lsptest.URI(dir)))
	}
	assert.Equal(t, []string{"/ann/hall.c", "/ann/yard.c", "dgdtools-profile://Mini%20Lib/std/room.c"}, got)
	c.Idle()
	var notes []string
	for _, m := range c.Received("window/logMessage") {
		var lm lsp.LogMessageParams
		require.NoError(t, json.Unmarshal(m.Params, &lm))
		if strings.HasPrefix(lm.Message, "references") {
			notes = append(notes, lm.Message)
		}
	}
	require.Len(t, notes, 1)
	assert.Contains(t, notes[0], "references to function set_short: 3")
	assert.Contains(t, notes[0], "1 calls of set_short on objects that cannot be told (in 1 files) may call it too and are not listed")
	assert.Contains(t, notes[0], "complete for this checkout only (2 files searched)")
	require.NoError(t, c.Shutdown())
}
