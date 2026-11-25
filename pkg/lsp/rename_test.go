package lsp_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/lsp"
	"github.com/mmcdole/dgdtools/pkg/lsp/lsptest"
	"github.com/mmcdole/dgdtools/pkg/rename"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// renameLib is resolveLib with a file of what is renamed: locals,
// parameters, a private function and variable, a macro, a label, with
// characters before them that are more than a byte and more than a
// UTF-16 unit.
func renameLib() map[string]string {
	files := resolveLib()
	files["obj/r.c"] = "/* blåbær 🫐 */ #include <std.h>\ninherit base STD;\n#define LIMIT 3\n" +
		"private int count; /* grøt */ private static int seen;\n" +
		"private int step(int n) { int i; for (i = 0; i < LIMIT; i++) { count += n; } return base::foo(i); }\n" +
		"static void go() { /* 🥣 */ step(1); call_out(\"step\", 1); seen = TWICE(count); catch(step(2)) : (e) { write(e); } }\n"
	return files
}

const failed = "jsonrpc2: code -32803 message: "

// The server's renames are the library's: for every place of every file
// of a lib, in both encodings, what textDocument/prepareRename answers is
// lint.Document.PrepareRename's target or refusal, and what
// textDocument/rename answers for a new name is Document.Rename's edits
// or refusal, with the positions worked out by the test from the text.
func TestRenameIsTheLibrarys(t *testing.T) {
	files := renameLib()
	dir := tree(t, files)
	fsys := vfs.OS{}
	prj, err := config.Discover(fsys, []string{dir}, config.DiscoverOptions{})
	require.NoError(t, err)
	l, err := lint.New(prj, fsys)
	require.NoError(t, err)
	require.True(t, l.Scan(context.Background()))
	ctx := context.Background()

	for _, enc := range [][]string{nil, {lsp.UTF16, lsp.UTF8}} {
		c := start(t)
		c.Initialize(lsptest.Capabilities{Encodings: enc}, dir)
		places, targets, refused := 0, 0, 0
		for rel, text := range files {
			if !utf8.ValidString(text) || filepath.Ext(rel) == ".yml" {
				continue
			}
			path := filepath.Join(dir, filepath.FromSlash(rel))
			c.Open(path, text)
			doc, err := l.Lint(ctx, path, lint.Options{})
			require.NoError(t, err)
			uri := lsptest.URI(path)
			rng := func(start, end uint32) lsp.Range {
				return lsp.Range{Start: lsptest.PositionOf(text, int(start), c.Encoding), End: lsptest.PositionOf(text, int(end), c.Encoding)}
			}
			for off := 0; off <= len(text); off++ {
				if off < len(text) && !utf8.RuneStart(text[off]) {
					continue
				}
				places++
				at := lsp.TextDocumentIdentifier{URI: uri}
				pos := lsptest.PositionOf(text, off, c.Encoding)
				where := func() string { return rel + " at " + text[off:min(off+12, len(text))] }

				var got json.RawMessage
				gotErr := c.Call("textDocument/prepareRename", lsp.PrepareRenameParams{TextDocument: at, Position: pos}, &got)
				target, err := doc.PrepareRename(ctx, uint32(off))
				var no *rename.Refusal
				switch {
				case errors.As(err, &no):
					refused++
					require.EqualError(t, gotErr, failed+no.Reason, where())
				case target == nil:
					require.NoError(t, gotErr, where())
					require.Equal(t, "null", string(got), where())
				default:
					targets++
					require.NoError(t, gotErr, where())
					want, _ := json.Marshal(lsp.PrepareRenameResult{Range: rng(target.Start, target.End), Placeholder: target.Name})
					require.JSONEq(t, string(want), string(got), where())
				}

				var edit *lsp.WorkspaceEdit
				gotErr = c.Call("textDocument/rename", lsp.RenameParams{TextDocument: at, Position: pos, NewName: "renamed"}, &edit)
				plan, err := doc.Rename(ctx, uint32(off), "renamed")
				if errors.As(err, &no) {
					require.EqualError(t, gotErr, failed+no.Reason, where())
					continue
				}
				require.NoError(t, gotErr, where())
				want := []lsp.TextEdit{}
				for _, e := range plan.Edits {
					want = append(want, lsp.TextEdit{Range: rng(e.Start, e.End), NewText: e.NewText})
				}
				require.Len(t, edit.DocumentChanges, 1, where())
				assert.Equal(t, c.Doc(path).Version, edit.DocumentChanges[0].TextDocument.Version)
				require.Equal(t, want, edit.DocumentChanges[0].Edits, where())
			}
			c.Close(path)
		}
		require.NoError(t, c.Shutdown())
		assert.Greater(t, targets, 80)
		assert.Greater(t, refused, 100)
		t.Logf("%s: %d places, %d to rename, %d refused", c.Encoding, places, targets, refused)
	}
}
