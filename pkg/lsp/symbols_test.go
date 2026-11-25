package lsp_test

import (
	"context"
	"encoding/json"
	"fmt"
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

// The server's outline is the library's: for every file of a lib, in
// both encodings, what textDocument/documentSymbol answers is what
// lint.Document.Symbols says of a fresh lint, with the ranges worked out
// by the test from the text: DocumentSymbols for a client that reads
// them, SymbolInformation for one that does not.
func TestDocumentSymbolIsResolve(t *testing.T) {
	files := resolveLib()
	files["obj/a.c"] += "#define LOCAL(x) x\nprivate static string *names, title; /* 🥣 */ static varargs int later(int when, string why...);\n"
	dir := tree(t, files)
	fsys := vfs.OS{}
	prj, err := config.Discover(fsys, []string{dir}, config.DiscoverOptions{})
	require.NoError(t, err)
	l, err := lint.New(prj, fsys)
	require.NoError(t, err)

	for _, caps := range []lsptest.Capabilities{{}, {Encodings: []string{lsp.UTF16, lsp.UTF8}, Plain: true}} {
		c := start(t)
		c.Initialize(caps, dir)
		kinds := map[int]int{}
		for rel, text := range files {
			if !utf8.ValidString(text) || filepath.Ext(rel) == ".yml" {
				continue
			}
			path := filepath.Join(dir, filepath.FromSlash(rel))
			c.Open(path, text)
			doc, err := l.Lint(context.Background(), path, lint.Options{})
			require.NoError(t, err)
			rng := func(start, end uint32) lsp.Range {
				return lsp.Range{Start: lsptest.PositionOf(text, int(start), c.Encoding), End: lsptest.PositionOf(text, int(end), c.Encoding)}
			}
			want := []any{}
			for _, s := range doc.Symbols() {
				kinds[lsp.SymbolKindOf(s.Kind)]++
				if caps.Plain {
					want = append(want, lsp.SymbolInformation{Name: s.Name, Kind: lsp.SymbolKindOf(s.Kind),
						Location: lsp.Location{URI: lsptest.URI(path), Range: rng(s.Start, s.End)}})
				} else {
					want = append(want, lsp.DocumentSymbol{Name: s.Name, Detail: s.Detail, Kind: lsp.SymbolKindOf(s.Kind),
						Range: rng(s.Start, s.End), SelectionRange: rng(s.NameStart, s.NameEnd)})
				}
			}
			data, err := json.Marshal(want)
			require.NoError(t, err)
			var got json.RawMessage
			require.NoError(t, c.Call("textDocument/documentSymbol", lsp.DocumentSymbolParams{TextDocument: lsp.TextDocumentIdentifier{URI: lsptest.URI(path)}}, &got))
			require.JSONEq(t, string(data), string(got), rel)
			c.Close(path)
		}
		require.NoError(t, c.Shutdown())
		// The lib has something of every kind.
		for _, kind := range []int{lsp.SymbolClass, lsp.SymbolFunction, lsp.SymbolVariable, lsp.SymbolConstant} {
			assert.NotZero(t, kinds[kind], "kind %d", kind)
		}
	}
}

// found asks the server for the symbols that match query and writes
// each as a line: NAME KIND IN URI LINE:CHARACTER.
func found(t testing.TB, c *lsptest.Client, dir, query string) []string {
	t.Helper()
	var syms []lsp.SymbolInformation
	require.NoError(t, c.Call("workspace/symbol", lsp.WorkspaceSymbolParams{Query: query}, &syms))
	require.NotNil(t, syms)
	out := []string{}
	for _, s := range syms {
		r := s.Location.Range
		assert.Equal(t, r.Start.Line, r.End.Line)
		assert.Equal(t, r.Start.Character+len(s.Name), r.End.Character)
		out = append(out, fmt.Sprintf("%s %d %s %s %d:%d", s.Name, s.Kind, s.ContainerName,
			strings.ReplaceAll(s.Location.URI, lsptest.URI(dir), "$DIR"), r.Start.Line, r.Start.Character))
	}
	return out
}

// workspace/symbol in a lib: where each name is, counted in the units
// the client counts in, behind characters of several bytes; a document
// that is open is searched as the editor has it; no more than the cap.
func TestWorkspaceSymbol(t *testing.T) {
	files := resolveLib()
	var many strings.Builder
	for i := range lsp.MaxWorkspaceSymbols + 50 {
		fmt.Fprintf(&many, "void numbered_%03d() {}\n", i)
	}
	files["obj/many.c"] = many.String()
	dir := tree(t, files)
	path := filepath.Join(dir, "obj/a.c")

	for _, caps := range []lsptest.Capabilities{{}, {Encodings: []string{lsp.UTF16, lsp.UTF8}, Plain: true}} {
		c := start(t)
		c.Initialize(caps, dir)
		// Nothing is open: the folder's lib is searched.
		col := func(rel, name string) int {
			return lsptest.PositionOf(files[rel], strings.Index(files[rel], name), c.Encoding).Character
		}
		assert.Equal(t, []string{fmt.Sprintf("weight 13 /std/base $DIR/std/base.c 0:%d", col("std/base.c", "weight"))}, found(t, c, dir, "weig"))
		assert.Contains(t, found(t, c, dir, "create"), fmt.Sprintf("create 12 /std/base $DIR/std/base.c 0:%d", col("std/base.c", "create")))
		assert.Equal(t, []string{fmt.Sprintf("STD 14 /include/std.h $DIR/include/std.h 0:%d", col("include/std.h", "STD"))}, found(t, c, dir, "STD"))
		// The columns are not the same in the two encodings.
		assert.Equal(t, map[string]int{lsp.UTF8: 40, lsp.UTF16: 37}[c.Encoding], col("std/base.c", "create"))
		assert.Len(t, found(t, c, dir, "numbered"), lsp.MaxWorkspaceSymbols)
		assert.Empty(t, found(t, c, dir, ""))

		// What the editor has is what is searched.
		c.Open(path, files["obj/a.c"]+"void brand_new() {}\n")
		assert.Equal(t, []string{"brand_new 12 /obj/a $DIR/obj/a.c 6:5"}, found(t, c, dir, "brand"))
		c.Change(path, lsptest.Edit{Start: len(files["obj/a.c"]) + 5, End: len(files["obj/a.c"]) + 10, Text: "newer"})
		assert.Equal(t, []string{"newer_new 12 /obj/a $DIR/obj/a.c 6:5"}, found(t, c, dir, "newer"))
		assert.Empty(t, found(t, c, dir, "brand"))
		require.NoError(t, c.Shutdown())
	}
}

// A wizard's folder with a profile: the names of the profile's stubs are
// found beside the checkout's own, each at its stub's URI, and said to
// be the profile's.
func TestWorkspaceSymbolInStubs(t *testing.T) {
	p := &profile.Profile{
		Manifest: profile.Manifest{Name: "Mini Lib", Dialect: profile.Dialect{Driver: "dgd"},
			IncludeDirs: []string{"/include"}, AutoObjects: []string{"/sys/auto"}, Covered: []string{"/std/", "/include/", "/sys/"}},
		Files: map[string]string{
			"/include/std.h": "#define ROOM \"/std/room\"\n#define SHORT_MAX 80\n",
			"/sys/auto.c":    "void autof(int n);\n",
			"/std/room.c":    "inherit \"/std/object\";\n\nint room_var;\nvoid set_short(string s);\n",
			"/std/object.c":  "void create();\n",
		},
	}
	data, err := p.Marshal()
	require.NoError(t, err)
	files := map[string]string{"ann/mini.profile": string(data), "ann/hall.c": "inherit \"/std/room\";\nvoid shorten() {}\n",
		"ann/dgdtools.yml": "profile: mini.profile\nlib_path: /players/ann\n"}
	dir := tree(t, files)
	c := start(t)
	c.Initialize(lsptest.Capabilities{}, filepath.Join(dir, "ann"))
	assert.Equal(t, []string{
		"shorten 12 /players/ann/hall $DIR/ann/hall.c 1:5",
		"SHORT_MAX 14 /include/std.h (profile Mini Lib) dgdtools-profile://Mini%20Lib/include/std.h 1:8",
		"set_short 12 /std/room (profile Mini Lib) dgdtools-profile://Mini%20Lib/std/room.c 3:5",
	}, found(t, c, dir, "short"))
	assert.Equal(t, []string{"autof 12 /sys/auto (profile Mini Lib) dgdtools-profile://Mini%20Lib/sys/auto.c 0:5"}, found(t, c, dir, "autof"))
	require.NoError(t, c.Shutdown())
}

// Documents nothing is said about have no outline.
func TestSymbolsOfNothing(t *testing.T) {
	dir := tree(t, lib())
	c := start(t)
	c.Initialize(lsptest.Capabilities{}, dir)
	c.Open("untitled-1", "void f() {}\n")
	for _, uri := range []string{"untitled:Untitled-1", lsptest.URI(filepath.Join(dir, "obj/closed.c")), "dgdtools-profile://x/std/room.c"} {
		var got json.RawMessage
		require.NoError(t, c.Call("textDocument/documentSymbol", lsp.DocumentSymbolParams{TextDocument: lsp.TextDocumentIdentifier{URI: uri}}, &got))
		assert.Equal(t, "null", string(got), uri)
	}
	// A file with nothing in it has an outline, of nothing.
	path := filepath.Join(dir, "obj/empty.c")
	c.Open(path, "")
	var got json.RawMessage
	require.NoError(t, c.Call("textDocument/documentSymbol", lsp.DocumentSymbolParams{TextDocument: lsp.TextDocumentIdentifier{URI: lsptest.URI(path)}}, &got))
	assert.Equal(t, "[]", string(got))
	require.NoError(t, c.Shutdown())
}
