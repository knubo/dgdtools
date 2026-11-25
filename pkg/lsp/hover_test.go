package lsp_test

import (
	"context"
	"encoding/json"
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

// ask sends a request about the byte offset off of the open document at
// path and returns the answer as it came.
func ask(t testing.TB, c *lsptest.Client, method, path string, off int) string {
	t.Helper()
	var result json.RawMessage
	require.NoError(t, c.Call(method, lsp.DefinitionParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: lsptest.URI(path)},
		Position:     lsptest.PositionOf(c.Doc(path).Text, off, c.Encoding),
	}, &result))
	return string(result)
}

// The server's hovers and signatures are the library's: for every place
// of every file of a lib, in both encodings and for a client that reads
// Markdown and one that does not, what textDocument/hover and
// textDocument/signatureHelp answer is what lint.Document.Hover and
// Signature say of a fresh lint, laid out by HoverContent and
// SignatureContent, with the range worked out by the test from the text.
func TestHoverIsResolve(t *testing.T) {
	files := resolveLib()
	// Comments to show, with what Markdown would read as marks, and a
	// macro with parameters.
	files["std/base.c"] = "/* grøt */ int weight; /* 🥣 */ void create() {}\n/*\n * Says *foo*:\n *   1. a_b\n */\nint foo(int a) { return a; }\nstatic void sfn() {}\n"
	files["include/std.h"] += "// Twice x.\n#define TWICE2(y, x) ((x)+(x))\n"
	files["obj/a.c"] += "void more() { foo(1, 2); TWICE2(1, 2); strlen(\"🫐\"); \"/obj/b\"->bar(foo(3)); x->y(4); call_out(\"cb\", 1, 5); }\n"
	dir := tree(t, files)

	fsys := vfs.OS{}
	prj, err := config.Discover(fsys, []string{dir}, config.DiscoverOptions{})
	require.NoError(t, err)
	l, err := lint.New(prj, fsys)
	require.NoError(t, err)

	for _, caps := range []lsptest.Capabilities{{}, {Encodings: []string{lsp.UTF16, lsp.UTF8}, Plain: true}} {
		c := start(t)
		c.Initialize(caps, dir)
		rich := !caps.Plain
		hovers, signatures, places := map[string]int{}, 0, 0
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
				at := rel + " at " + text[off:min(off+12, len(text))]
				want := "null"
				if h := doc.Hover(uint32(off)); h != nil {
					data, err := json.Marshal(lsp.Hover{
						Contents: lsp.HoverContent(h, rich),
						Range:    &lsp.Range{Start: lsptest.PositionOf(text, int(h.Ref.Start), c.Encoding), End: lsptest.PositionOf(text, int(h.Ref.End), c.Encoding)},
					})
					require.NoError(t, err)
					want = string(data)
					hovers[h.Ref.Kind.String()]++
				}
				require.JSONEq(t, want, ask(t, c, "textDocument/hover", path, off), at)

				want = "null"
				if s := doc.Signature(uint32(off)); s != nil {
					data, err := json.Marshal(lsp.SignatureContent(s, lsp.SignatureFormat{Markdown: rich, Offsets: rich, Active: rich}))
					require.NoError(t, err)
					want = string(data)
					signatures++
				}
				require.JSONEq(t, want, ask(t, c, "textDocument/signatureHelp", path, off), at)
				places++
			}
			c.Close(path)
		}
		require.NoError(t, c.Shutdown())
		// The lib has something of everything.
		for _, kind := range []string{"include", "object", "label", "function", "variable", "macro", "local"} {
			assert.NotZero(t, hovers[kind], kind)
		}
		assert.NotZero(t, signatures)
		t.Logf("%s, markdown %v: %d places, hovers %v, %d in the arguments of a call", c.Encoding, rich, places, hovers, signatures)
	}
}

// What a hover and a signature look like to the two kinds of client.
func TestHoverContent(t *testing.T) {
	files := resolveLib()
	files["std/base.c"] = "/*\n * Says *foo*:\n *   1. a_b\n */\nint foo(int grams, string b...) { return 1; }\nvoid create() {}\n"
	files["obj/a.c"] = "inherit \"/std/base\";\nvoid f() { foo(1, \"a\", \"b\"); foo(); }\n"
	dir := tree(t, files)
	path := filepath.Join(dir, "obj/a.c")
	text := files["obj/a.c"]

	c := start(t)
	c.Initialize(lsptest.Capabilities{}, dir)
	c.Open(path, text)
	assert.JSONEq(t, `{"contents": {"kind": "markdown", "value": "`+
		"```dgdlpc\\nint foo(int grams, string b...)\\n```\\n\\nSays \\\\*foo\\\\*:  \\n\u00a0\u00a01\\\\. a\\\\_b\\n\\n/std/base.c:5"+
		`"}, "range": {"start": {"line": 1, "character": 11}, "end": {"line": 1, "character": 14}}}`,
		ask(t, c, "textDocument/hover", path, strings.Index(text, "foo")))
	// The parameters as UTF-16 units of the label, the last one for
	// every argument past it.
	assert.JSONEq(t, `{"signatures": [{"label": "int foo(int grams, string b...)", "parameters": [{"label": [8, 17]}, {"label": [19, 30]}],
		"documentation": {"kind": "markdown", "value": "Says \\*foo\\*:  \n`+"\u00a0\u00a0"+`1\\. a\\_b\n\n/std/base.c:5"}, "activeParameter": 1}],
		"activeSignature": 0, "activeParameter": 1}`,
		ask(t, c, "textDocument/signatureHelp", path, strings.Index(text, "\"b\"")))
	require.NoError(t, c.Shutdown())

	c = start(t)
	c.Initialize(lsptest.Capabilities{Plain: true}, dir)
	c.Open(path, text)
	assert.JSONEq(t, `{"contents": {"kind": "plaintext", "value": "int foo(int grams, string b...)\n\nSays *foo*:\n  1. a_b\n\n/std/base.c:5"},
		"range": {"start": {"line": 1, "character": 11}, "end": {"line": 1, "character": 14}}}`,
		ask(t, c, "textDocument/hover", path, strings.Index(text, "foo")))
	assert.JSONEq(t, `{"signatures": [{"label": "int foo(int grams, string b...)", "parameters": [{"label": "int grams"}, {"label": "string b..."}],
		"documentation": "Says *foo*:\n  1. a_b\n\n/std/base.c:5"}], "activeSignature": 0, "activeParameter": 0}`,
		ask(t, c, "textDocument/signatureHelp", path, strings.Index(text, "foo();")+4))
	require.NoError(t, c.Shutdown())
}

func TestMarkdownText(t *testing.T) {
	for text, want := range map[string]string{
		"plain words, as they are.": "plain words, as they are.",
		"one\ntwo":                  "one  \ntwo",
		"one\n\ntwo\n":              "one\n\ntwo\n",
		"  in\n    deeper":          "\u00a0\u00a0in  \n\u00a0\u00a0\u00a0\u00a0deeper",
		"a*b* _c_ `d` [e](f) <g> h&amp; ~i~ j|k \\l":                                               "a\\*b\\* \\_c\\_ \\`d\\` \\[e\\](f) \\<g\\> h\\&amp; \\~i\\~ j\\|k \\\\l",
		"# head\n- item\n+ item\n=====\n1. one\n12) two\n3.14 is no item, nor is this. Or (this).": "\\# head  \n\\- item  \n\\+ item  \n\\=====  \n1\\. one  \n12\\) two  \n3\\.14 is no item, nor is this. Or (this).",
		"a - b # c = d + e": "a - b # c = d + e",
		"blåbær 🫐":          "blåbær 🫐",
		"":                  "",
	} {
		assert.Equal(t, want, lsp.MarkdownText(text), "%q", text)
	}
}

// A wizard's folder with a profile: what a stub declares is told as the
// profile's.
func TestHoverInStub(t *testing.T) {
	p := &profile.Profile{
		Manifest: profile.Manifest{Name: "Mini Lib", Dialect: profile.Dialect{Driver: "dgd", Kfuns: []string{"int *rusage()"}},
			IncludeDirs: []string{"/include"}, AutoObjects: []string{"/sys/auto"}, Covered: []string{"/std/", "/include/", "/sys/"}},
		Files: map[string]string{
			"/include/std.h": "#define ROOM \"/std/room\"\n",
			"/sys/auto.c":    "void autof(int n);\n",
			"/std/room.c":    "inherit \"/std/object\";\n\nint room_var;\nvoid set_short(string s);\n",
			"/std/object.c":  "void create();\n",
		},
	}
	data, err := p.Marshal()
	require.NoError(t, err)
	const hall = "#include <std.h>\ninherit ROOM;\nvoid create() { ::create(); set_short(\"x\"); room_var = 1; autof(2); rusage(); \"/players/ann/other\"->there(); \"/players/bob/x\"->there(); }\n"
	files := map[string]string{"ann/mini.profile": string(data), "ann/hall.c": hall, "ann/other.c": "/* Over there. */\nvoid there() {}\n"}
	dir := tree(t, files)
	path := filepath.Join(dir, "ann/hall.c")

	// A client that shows plain text and cannot be asked for settings:
	// it says them when it starts.
	c := start(t)
	c.Initialize(lsptest.Capabilities{Plain: true,
		Options: map[string]any{"profile": filepath.Join(dir, "ann/mini.profile"), "libPath": "/players/ann"}}, filepath.Join(dir, "ann"))
	c.Open(path, hall)
	// What the hover says at each place, and the signature there.
	for at, want := range map[string][2]string{
		"<st|d.h>":          {"#include <std.h>\n\nprofile Mini Lib, /include/std.h", ""},
		"inherit RO|OM":     {"object /std/room\n\nprofile Mini Lib, /std/room.c", ""},
		"::create(|)":       {"", "void create()"},
		"set_sh|ort":        {"void set_short(string s)\n\nprofile Mini Lib, /std/room.c", ""},
		"set_short(|\"x\"":  {"", "void set_short(string s)"},
		"room_v|ar":         {"int room_var\n\nprofile Mini Lib, /std/room.c", ""},
		"auto|f(2)":         {"void autof(int n)\n\nauto object, profile Mini Lib, /sys/auto.c", ""},
		"autof(|2)":         {"", "void autof(int n)"},
		"rus|age()":         {"int *rusage()\n\nkfun, declared by the project (dialect.kfuns)", ""},
		"other\"->the|re":   {"void there()\n\nOver there.\n\n/players/ann/other.c:2", ""},
		"other\"->there(|)": {"", "void there()"},
		"bob/x\"->the|re":   {"", ""},
		"bob/x\"->there(|)": {"", ""},
	} {
		cursor := strings.IndexByte(at, '|')
		off := strings.Index(hall, at[:cursor]+at[cursor+1:]) + cursor
		var h *lsp.Hover
		require.NoError(t, json.Unmarshal([]byte(ask(t, c, "textDocument/hover", path, off)), &h), at)
		if want[0] == "" {
			if want[1] == "" {
				assert.Nil(t, h, at)
			}
		} else if assert.NotNil(t, h, at) {
			assert.Equal(t, lsp.MarkupContent{Kind: lsp.PlainText, Value: want[0]}, h.Contents, at)
		}
		var s *lsp.SignatureHelp
		require.NoError(t, json.Unmarshal([]byte(ask(t, c, "textDocument/signatureHelp", path, off)), &s), at)
		if want[1] == "" {
			assert.Nil(t, s, at)
		} else if assert.NotNil(t, s, at) {
			assert.Equal(t, want[1], s.Signatures[0].Label, at)
		}
	}
	require.NoError(t, c.Shutdown())
}

// Asked again while it shows signatures, the server keeps the one that
// is shown when the call is still the same.
func TestSignatureRetrigger(t *testing.T) {
	files := map[string]string{
		"dgdtools.yml": "",
		"std/a.c":      "void create(int a) {}\n",
		"std/b.c":      "void create(string b, int c) {}\nvoid other() {}\n",
		"obj/x.c":      "inherit \"/std/a\";\ninherit \"/std/b\";\nvoid create() { ::create(1, 2); ::other(); }\n",
	}
	dir := tree(t, files)
	path := filepath.Join(dir, "obj/x.c")
	c := start(t)
	c.Initialize(lsptest.Capabilities{}, dir)
	c.Open(path, files["obj/x.c"])
	signature := func(at string, context any) *lsp.SignatureHelp {
		cursor := strings.IndexByte(at, '|')
		off := strings.Index(files["obj/x.c"], at[:cursor]+at[cursor+1:]) + cursor
		var help *lsp.SignatureHelp
		require.NoError(t, c.Call("textDocument/signatureHelp", map[string]any{
			"textDocument": map[string]any{"uri": lsptest.URI(path)},
			"position":     lsptest.PositionOf(files["obj/x.c"], off, c.Encoding),
			"context":      context,
		}, &help))
		return help
	}
	// Two functions the call may be of: the first is shown, and each
	// has its own parameter for the argument.
	first := signature("::create(|1, 2)", map[string]any{"triggerKind": 2, "triggerCharacter": "(", "isRetrigger": false})
	require.Len(t, first.Signatures, 2)
	assert.Equal(t, 0, first.ActiveSignature)
	assert.Equal(t, 0, first.ActiveParameter)
	assert.Equal(t, []string{"void create(int a)", "void create(string b, int c)"}, []string{first.Signatures[0].Label, first.Signatures[1].Label})

	// The user turns to the second, and types on.
	first.ActiveSignature = 1
	again := signature("::create(1, |2)", map[string]any{"triggerKind": 2, "triggerCharacter": ",", "isRetrigger": true, "activeSignatureHelp": first})
	assert.Equal(t, 1, again.ActiveSignature)
	assert.Equal(t, 1, again.ActiveParameter)
	assert.Equal(t, 1, *again.Signatures[0].ActiveParameter, "past the only parameter: none")
	assert.Equal(t, 1, *again.Signatures[1].ActiveParameter)
	// Without that, the first again.
	fresh := signature("::create(1, |2)", nil)
	assert.Equal(t, 0, fresh.ActiveSignature)
	assert.Equal(t, 1, fresh.ActiveParameter)
	// In another call what was shown is nothing to keep.
	other := signature("::other(|)", map[string]any{"isRetrigger": true, "activeSignatureHelp": first})
	require.Len(t, other.Signatures, 1)
	assert.Equal(t, 0, other.ActiveSignature)
	// And past the call there is nothing.
	assert.Nil(t, signature("::other()|", map[string]any{"isRetrigger": true, "activeSignatureHelp": first}))
	require.NoError(t, c.Shutdown())
}

// What is asked of a document the server says nothing about, and what
// is asked badly.
func TestHoverOfNothing(t *testing.T) {
	dir := tree(t, map[string]string{"dgdtools.yml": "", "obj/a.c": "void f() { f(); }\n"})
	c := start(t)
	c.Initialize(lsptest.Capabilities{}, dir)
	c.Notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{TextDocument: lsp.TextDocumentItem{URI: "untitled:Untitled-1", Version: 1, Text: "void f() { f(); }\n"}})
	for _, method := range []string{"textDocument/hover", "textDocument/signatureHelp"} {
		for _, uri := range []string{
			"untitled:Untitled-1",                        // open, and no file
			lsptest.URI(filepath.Join(dir, "obj/a.c")),   // a file, and not open
			"dgdtools-profile://nobody/std/room.c",       // a stub: shown, not analyzed
			lsptest.URI(filepath.Join(dir, "nowhere.c")), // nothing at all
		} {
			var result json.RawMessage
			require.NoError(t, c.Call(method, lsp.DefinitionParams{
				TextDocument: lsp.TextDocumentIdentifier{URI: uri}, Position: lsp.Position{Line: 0, Character: 13}}, &result), uri)
			assert.Equal(t, "null", string(result), uri)
		}
		err := c.Call(method, map[string]any{"textDocument": 3}, nil)
		assert.True(t, lsptest.IsCode(err, -32602), "%v", err)
	}
	path := filepath.Join(dir, "obj/a.c")
	c.Open(path, "void f() { f(); }\n")
	assert.NotEqual(t, "null", ask(t, c, "textDocument/hover", path, 12))
	assert.NotEqual(t, "null", ask(t, c, "textDocument/signatureHelp", path, 13))
	// Positions past the text are its end, where there is nothing.
	for _, pos := range []lsp.Position{{Line: 0, Character: 999}, {Line: 99, Character: 0}} {
		for _, method := range []string{"textDocument/hover", "textDocument/signatureHelp"} {
			var result json.RawMessage
			require.NoError(t, c.Call(method, lsp.DefinitionParams{TextDocument: lsp.TextDocumentIdentifier{URI: lsptest.URI(path)}, Position: pos}, &result))
			assert.Equal(t, "null", string(result))
		}
	}
	require.NoError(t, c.Shutdown())
}
