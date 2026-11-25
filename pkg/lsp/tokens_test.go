package lsp_test

import (
	"context"
	"encoding/json"
	"math/rand"
	"path/filepath"
	"slices"
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

// tokensResult is an answer of the semantic tokens requests: all of
// them, or a delta.
type tokensResult struct {
	ResultID string                    `json:"resultId"`
	Data     *[]uint32                 `json:"data"`
	Edits    *[]lsp.SemanticTokensEdit `json:"edits"`
}

func semanticTokens(t testing.TB, c *lsptest.Client, method string, params any) tokensResult {
	t.Helper()
	var raw json.RawMessage
	require.NoError(t, c.Call(method, params, &raw))
	var res tokensResult
	require.NoError(t, json.Unmarshal(raw, &res), string(raw))
	return res
}

// The server's semantic tokens are the library's: of every file of a
// lib, in both encodings, the tokens of textDocument/semanticTokens/full
// decoded by the test from the text are lint.Document.Tokens of a fresh
// lint, each over the same text; a range is the tokens that begin in it.
// The lib has characters that are more than a byte and more than a
// UTF-16 unit before the names.
func TestTokensAreTheLibrarys(t *testing.T) {
	files := resolveLib()
	dir := tree(t, files)
	fsys := vfs.OS{}
	prj, err := config.Discover(fsys, []string{dir}, config.DiscoverOptions{})
	require.NoError(t, err)
	l, err := lint.New(prj, fsys)
	require.NoError(t, err)
	for _, enc := range [][]string{nil, {lsp.UTF16, lsp.UTF8}} {
		c := start(t)
		c.Initialize(lsptest.Capabilities{Encodings: enc}, dir)
		n := 0
		for rel, text := range files {
			if !utf8.ValidString(text) || filepath.Ext(rel) == ".yml" {
				continue
			}
			path := filepath.Join(dir, filepath.FromSlash(rel))
			c.Open(path, text)
			doc, err := l.Lint(context.Background(), path, lint.Options{})
			require.NoError(t, err)
			want := doc.Tokens()
			uri := lsp.TextDocumentIdentifier{URI: lsptest.URI(path)}
			res := semanticTokens(t, c, "textDocument/semanticTokens/full", lsp.SemanticTokensParams{TextDocument: uri})
			require.NotNil(t, res.Data)
			assert.NotEmpty(t, res.ResultID)
			got := lsptest.Tokens(text, *res.Data, c.Encoding)
			require.Len(t, got, len(want), rel)
			for i, tk := range got {
				w := want[i]
				assert.Equal(t, text[w.Start:w.End], tk.Text, "%s token %d", rel, i)
				assert.Equal(t, lsptest.PositionOf(text, int(w.Start), c.Encoding), lsp.Position{Line: tk.Line, Character: tk.Character}, "%s token %d", rel, i)
				assert.Equal(t, w.Type.String(), tk.Type)
				assert.Equal(t, w.Mods.Names(), tk.Mods)
			}
			n += len(got)
			// Ranges: every pair of lines, and one that ends inside a line.
			lines := lsptest.Lines(text)
			for a := 0; a <= lines; a++ {
				for b := a; b <= lines; b++ {
					rng := lsp.Range{Start: lsp.Position{Line: a}, End: lsp.Position{Line: b}}
					res := semanticTokens(t, c, "textDocument/semanticTokens/range", lsp.SemanticTokensRangeParams{TextDocument: uri, Range: rng})
					require.NotNil(t, res.Data)
					var in []lsptest.Token
					for _, tk := range got {
						if tk.Line >= a && tk.Line < b {
							in = append(in, tk)
						}
					}
					assert.Equal(t, in, lsptest.Tokens(text, *res.Data, c.Encoding), "%s lines %d-%d", rel, a, b)
				}
			}
			c.Close(path)
		}
		require.NoError(t, c.Shutdown())
		assert.Greater(t, n, 40)
	}
}

// A delta applied to the tokens the client has is what all of them are:
// after each of many random edits of a document, in both encodings; and
// a delta from an id that is not the last answer's is all the tokens.
func TestTokensDelta(t *testing.T) {
	files := resolveLib()
	dir := tree(t, files)
	path := filepath.Join(dir, "obj", "a.c")
	for _, enc := range [][]string{nil, {lsp.UTF16, lsp.UTF8}} {
		c := start(t)
		c.Initialize(lsptest.Capabilities{Encodings: enc}, dir)
		c.Open(path, files["obj/a.c"])
		uri := lsp.TextDocumentIdentifier{URI: lsptest.URI(path)}
		full := func() tokensResult {
			res := semanticTokens(t, c, "textDocument/semanticTokens/full", lsp.SemanticTokensParams{TextDocument: uri})
			require.NotNil(t, res.Data)
			return res
		}
		first := full()
		have, id := *first.Data, first.ResultID
		r := rand.New(rand.NewSource(int64(len(enc))))
		edits := 0
		for i := range 200 {
			text := c.Doc(path).Text
			if i%40 == 39 {
				c.Replace(path, files["obj/a.c"]) // back to something that parses
			} else {
				c.Change(path, edit(r, text))
			}
			res := semanticTokens(t, c, "textDocument/semanticTokens/full/delta",
				lsp.SemanticTokensDeltaParams{TextDocument: uri, PreviousResultID: id})
			require.NotNil(t, res.Edits, "a delta from the last answer")
			require.NotEqual(t, id, res.ResultID)
			got := slices.Clone(have)
			for j := len(*res.Edits) - 1; j >= 0; j-- {
				e := (*res.Edits)[j]
				assert.Zero(t, e.Start%5, "an edit begins at a token")
				got = slices.Replace(got, e.Start, e.Start+e.DeleteCount, e.Data...)
				edits++
			}
			want := full()
			require.Equal(t, *want.Data, got, "after edit %d of %q", i, c.Doc(path).Text)
			have, id = got, want.ResultID
		}
		assert.Greater(t, edits, 30)
		// An id that is not the last answer's.
		res := semanticTokens(t, c, "textDocument/semanticTokens/full/delta",
			lsp.SemanticTokensDeltaParams{TextDocument: uri, PreviousResultID: first.ResultID})
		require.NotNil(t, res.Data)
		assert.Equal(t, have, *res.Data)
		require.NoError(t, c.Shutdown())
	}
}

// The tokens of the legend are the library's, in its order.
func TestTokenLegend(t *testing.T) {
	assert.Equal(t, resolve.TokenTypes, lsp.TokenLegend.TokenTypes)
	assert.Equal(t, resolve.TokenModNames, lsp.TokenLegend.TokenModifiers)
	assert.Equal(t, "function", resolve.TokenFunction.String())
	assert.Equal(t, []string{"declaration", "local"}, (resolve.ModDeclaration | resolve.ModLocal).Names())
}
