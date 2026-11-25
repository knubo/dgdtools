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
	"github.com/mmcdole/dgdtools/pkg/resolve"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// completionLib is a lib with something of everything a completion is
// asked in, behind characters of several bytes.
func completionLib() map[string]string {
	files := resolveLib()
	files["std/base.c"] = "/* grøt */ int weight; /* 🥣 */ void create() {}\n/*\n * Says *foo*:\n *   1. a_b\n */\nint foo(int a) { return a; }\nstatic void sfn() {}\n"
	files["include/std.h"] += "// Twice x.\n#define TWICE2(y, x) ((x)+(x))\n#ifdef ST\n#endif\n"
	files["obj/a.c"] += "void more(int n) { int æ; foo(n); ::create(); l::old(); this_object()->cb(); \"/obj/b\"->bar(TWICE2(1, n)); x->y(4); call_out(\"cb\", 1, 5); }\n"
	return files
}

// complete asks the server what can be written at byte offset off of
// the open document at path, as typed by the character trigger when
// there is one.
func complete(t testing.TB, c *lsptest.Client, path string, off int, trigger string) string {
	t.Helper()
	params := map[string]any{
		"textDocument": lsp.TextDocumentIdentifier{URI: lsptest.URI(path)},
		"position":     lsptest.PositionOf(c.Doc(path).Text, off, c.Encoding),
	}
	if trigger != "" {
		params["context"] = map[string]any{"triggerKind": lsp.TriggerCharacter, "triggerCharacter": trigger}
	}
	var result json.RawMessage
	require.NoError(t, c.Call("textDocument/completion", params, &result))
	return string(result)
}

// The server's completions are the library's: at every place of every
// file of a lib, in both encodings and for a client that reads Markdown
// and snippets and one that does not, what textDocument/completion
// answers is what lint.Document.Complete says of a fresh lint, laid out
// by CompletionContent, with the range worked out by the test from the
// text.
func TestCompletionIsResolve(t *testing.T) {
	files := completionLib()
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
		contexts, places := map[resolve.Context]int{}, 0
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
				want := "null"
				if comp := doc.Complete(uint32(off)); comp != nil {
					rng := lsp.Range{Start: lsptest.PositionOf(text, int(comp.Start), c.Encoding), End: lsptest.PositionOf(text, int(comp.End), c.Encoding)}
					data, err := json.Marshal(lsp.CompletionContent(comp, lsptest.URI(path), rng, lsp.CompletionFormat{Markdown: rich, Snippets: rich}))
					require.NoError(t, err)
					want = string(data)
					contexts[comp.Context]++
				}
				require.JSONEq(t, want, complete(t, c, path, off, ""), "%s at %s", rel, text[off:min(off+12, len(text))])
				places++
			}
			c.Close(path)
		}
		require.NoError(t, c.Shutdown())
		// The lib has every kind of place.
		for _, ctx := range []resolve.Context{resolve.InCode, resolve.AfterArrow, resolve.AfterScope, resolve.InInclude, resolve.InInherit, resolve.InCallback, resolve.InDirective} {
			assert.NotZero(t, contexts[ctx], "context %d", ctx)
		}
		t.Logf("%s, markdown and snippets %v: %d places, by context %v", c.Encoding, rich, places, contexts)
	}
}

// items reads a completion list into its items by label; a function
// before a variable of its name.
func items(t testing.TB, list string) (map[string]lsp.CompletionItem, bool) {
	t.Helper()
	var l lsp.CompletionList
	require.NoError(t, json.Unmarshal([]byte(list), &l))
	out := map[string]lsp.CompletionItem{}
	for _, it := range l.Items {
		if _, ok := out[it.Label]; !ok || it.Kind == lsp.ItemFunction {
			out[it.Label] = it
		}
	}
	return out, l.IsIncomplete
}

// What an item looks like to the two kinds of client, and what asking
// for it adds.
func TestCompletionContent(t *testing.T) {
	files := completionLib()
	dir := tree(t, files)
	path := filepath.Join(dir, "obj/a.c")
	text := files["obj/a.c"]
	at := strings.Index(text, "foo(n)")
	more := strings.Index(text, "void more") // line 6

	c := start(t)
	c.Initialize(lsptest.Capabilities{}, dir)
	c.Open(path, text)
	got, incomplete := items(t, complete(t, c, path, at+2, ""))
	assert.False(t, incomplete)
	line := lsptest.PositionOf(text, at, c.Encoding).Line
	col := lsptest.PositionOf(text, at, c.Encoding).Character
	edit := func(newText string) string {
		data, err := json.Marshal(lsp.TextEdit{Range: lsp.Range{Start: lsp.Position{Line: line, Character: col}, End: lsp.Position{Line: line, Character: col + 3}}, NewText: newText})
		require.NoError(t, err)
		return string(data)
	}
	show := func(it lsp.CompletionItem) string {
		data, err := json.Marshal(it)
		require.NoError(t, err)
		return string(data)
	}
	assert.Equal(t, more, strings.Index(text, "more(int n)")-5)
	// A '(' follows: the name alone.
	assert.JSONEq(t, `{"label": "foo", "kind": 3, "detail": "int foo(int a)", "documentation": {"kind": "markdown", "value": "/std/base.c:6"},
		"sortText": "2foo", "textEdit": `+edit("foo")+`, "data": {"uri": "`+lsptest.URI(path)+`", "path": "`+filepath.Join(dir, "std/base.c")+`", "off": `+
		itoa(strings.Index(files["std/base.c"], "foo(int"))+`}}`, show(got["foo"]))
	assert.JSONEq(t, `{"label": "n", "kind": 6, "detail": "int n", "documentation": {"kind": "markdown", "value": "/obj/a.c:7"}, "sortText": "0n", "textEdit": `+edit("n")+`}`, show(got["n"]))
	assert.JSONEq(t, `{"label": "strlen", "kind": 3, "detail": "int strlen(string)", "documentation": {"kind": "markdown", "value": "return the length of a string\n\nkfun, DGD 1.7.9"},
		"sortText": "5strlen", "textEdit": `+edit("strlen")+`}`, show(got["strlen"]))
	assert.JSONEq(t, `{"label": "return", "kind": 14, "sortText": "6return", "textEdit": `+edit("return")+`}`, show(got["return"]))
	assert.Equal(t, lsp.ItemConstant, got["TWICE2"].Kind)
	assert.Equal(t, "#define TWICE2(y, x) ((x)+(x))", got["TWICE2"].Detail)
	assert.Equal(t, "TWICE2", got["TWICE2"].TextEdit.NewText)
	assert.Equal(t, lsp.ItemVariable, got["weight"].Kind)

	// Asked for, an item has the comment above its declaration before
	// where it is; one that has none comes back as it was.
	resolve := func(c *lsptest.Client, it lsp.CompletionItem) lsp.CompletionItem {
		var resolved lsp.CompletionItem
		require.NoError(t, c.Call("completionItem/resolve", it, &resolved))
		return resolved
	}
	resolved := resolve(c, got["foo"])
	assert.Equal(t, lsp.MarkupContent{Kind: lsp.Markdown, Value: "Says \\*foo\\*:  \n  1\\. a\\_b\n\n/std/base.c:6"}, resolved.Documentation)
	assert.Equal(t, got["foo"].TextEdit, resolved.TextEdit)
	resolved = resolve(c, got["TWICE2"])
	assert.Equal(t, lsp.MarkupContent{Kind: lsp.Markdown, Value: "Twice x.\n\n/include/std.h"}, resolved.Documentation)
	for _, name := range []string{"n", "strlen", "return", "sfn"} {
		assert.JSONEq(t, show(got[name]), show(resolve(c, got[name])), name)
	}
	// An item of a document that is gone, and one that is nobody's.
	var raw json.RawMessage
	require.NoError(t, c.Call("completionItem/resolve", json.RawMessage(`{"label": "x", "kind": 3, "data": {"uri": "file:///nowhere/a.c", "path": "/nowhere/b.c", "off": 3}}`), &raw))
	assert.JSONEq(t, `{"label": "x", "kind": 3, "data": {"uri": "file:///nowhere/a.c", "path": "/nowhere/b.c", "off": 3}}`, string(raw))
	require.NoError(t, c.Call("completionItem/resolve", json.RawMessage(`{"label": "x", "kind": 3, "documentation": null, "data": 7}`), &raw))
	assert.JSONEq(t, `{"label": "x", "kind": 3, "data": 7}`, string(raw))

	// No '(' follows: a function's name with its parentheses, the
	// cursor in them when it takes arguments; a macro with parameters
	// too; nothing else.
	c.Change(path, lsptest.Edit{Start: at, End: at + len("foo(n)"), Text: "fo"})
	got, _ = items(t, complete(t, c, path, at+2, ""))
	assert.Equal(t, "foo($0)", got["foo"].TextEdit.NewText)
	assert.Equal(t, lsp.FormatSnippet, got["foo"].InsertTextFormat)
	assert.Equal(t, "create()$0", got["create"].TextEdit.NewText)
	assert.Equal(t, "TWICE2($0)", got["TWICE2"].TextEdit.NewText)
	assert.Equal(t, "strlen($0)", got["strlen"].TextEdit.NewText)
	for _, name := range []string{"n", "weight", "STD", "return", "int"} {
		assert.Equal(t, name, got[name].TextEdit.NewText)
		assert.Zero(t, got[name].InsertTextFormat, name)
	}
	require.NoError(t, c.Shutdown())

	// A client that reads neither Markdown nor snippets.
	c = start(t)
	c.Initialize(lsptest.Capabilities{Plain: true}, dir)
	c.Open(path, strings.Replace(text, "foo(n)", "fo", 1))
	got, _ = items(t, complete(t, c, path, at+2, ""))
	assert.Equal(t, "foo", got["foo"].TextEdit.NewText)
	assert.Zero(t, got["foo"].InsertTextFormat)
	assert.Equal(t, "/std/base.c:6", got["foo"].Documentation)
	assert.Equal(t, "return the length of a string\n\nkfun, DGD 1.7.9", got["strlen"].Documentation)
	resolved = resolve(c, got["foo"])
	assert.Equal(t, "Says *foo*:\n  1. a_b\n\n/std/base.c:6", resolved.Documentation)
	require.NoError(t, c.Shutdown())
}

func itoa(n int) string {
	data, _ := json.Marshal(n)
	return string(data)
}

// A trigger character gets an answer where it begins something, and
// null where it is an operator like another.
func TestCompletionTrigger(t *testing.T) {
	files := completionLib()
	dir := tree(t, files)
	path := filepath.Join(dir, "obj/t.c")
	c := start(t)
	c.Initialize(lsptest.Capabilities{}, dir)
	c.Open(path, "")
	for text, want := range map[string]string{
		"#include <|":                               "std.h",
		"#include \"|":                              "std.h",
		"#include <std.h>\n#include \"../|":         "obj",
		"inherit \"|":                               "std",
		"inherit \"/std/|":                          "base",
		"inherit \"/std/base\";\nvoid f() { ::|":    "create",
		"inherit l \"/std/base\";\nvoid f() { l::|": "foo",
		"void g() {}\nvoid f() { this_object()->|":  "g",
		"void f() { \"/obj/b\"->|":                  "bar",
		"void g() {}\nvoid f() { call_out(\"|":      "g",
		// Nothing begins here.
		"void f(int a) { a = a >|":              "",
		"void f(int a) { a = a ? 1 :|":          "",
		"void f(int a) { a = a /|":              "",
		"void f(int a) { a = a <|":              "",
		"void f(int a) { write(\"|":             "",
		"void f(int a) { write(\"a/|":           "",
		"void f(int a) { switch (a) { case 1:|": "",
		"#if 1 <|":                              "",
		"#define X \"|":                         "",
	} {
		off := strings.IndexByte(text, '|')
		c.Replace(path, text[:off]+text[off+1:])
		got := complete(t, c, path, off, text[off-1:off])
		if want == "" {
			assert.Equal(t, "null", got, text)
			continue
		}
		require.NotEqual(t, "null", got, text)
		have, _ := items(t, got)
		assert.Contains(t, have, want, text)
	}
	// The same places asked about without a character typed have what
	// can stand there.
	c.Replace(path, "void f(int a) { a = a > }")
	have, _ := items(t, complete(t, c, path, len("void f(int a) { a = a >"), ""))
	assert.Contains(t, have, "a")

	// Documents nothing is said about.
	c.Open("untitled-1", "void f() {}\n")
	for _, uri := range []string{"untitled:Untitled-1", lsptest.URI(filepath.Join(dir, "obj/closed.c")), "dgdtools-profile://x/std/room.c"} {
		var got json.RawMessage
		require.NoError(t, c.Call("textDocument/completion", map[string]any{"textDocument": lsp.TextDocumentIdentifier{URI: uri}, "position": lsp.Position{}}, &got))
		assert.Equal(t, "null", string(got), uri)
	}
	require.NoError(t, c.Shutdown())
}
