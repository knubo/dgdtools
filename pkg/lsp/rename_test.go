package lsp_test

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
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

// reason is what the server says of a refusal: its reason, and of one
// the command line can overrule, how.
func reason(no *rename.Refusal) string {
	if no.Forcible {
		return no.Reason + " (`dgdtools rename --force` renames it all the same)"
	}
	return no.Reason
}

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
		places, targets, refused, across, possible := 0, 0, 0, 0, 0
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
					require.EqualError(t, gotErr, failed+reason(no), where())
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
				plan, err := doc.Rename(ctx, uint32(off), "renamed", false)
				if errors.As(err, &no) {
					require.EqualError(t, gotErr, failed+reason(no), where())
					continue
				}
				require.NoError(t, gotErr, where())
				// The plan's edits, each in its file's text; the possible
				// ones to confirm.
				type place struct {
					uri string
					e   lsp.TextEdit
				}
				var want, gotEdits []place
				add := func(e rename.Edit) {
					rel, err := filepath.Rel(dir, e.Path)
					require.NoError(t, err)
					ft := files[filepath.ToSlash(rel)]
					r := lsp.Range{Start: lsptest.PositionOf(ft, int(e.Start), c.Encoding), End: lsptest.PositionOf(ft, int(e.End), c.Encoding)}
					want = append(want, place{lsptest.URI(e.Path), lsp.TextEdit{Range: r, NewText: e.NewText}})
				}
				for _, e := range plan.Edits {
					add(e)
				}
				for _, e := range plan.PossibleEdits {
					add(e)
				}
				for _, dc := range edit.DocumentChanges {
					if dc.TextDocument.URI == uri {
						require.NotNil(t, dc.TextDocument.Version, where())
						assert.Equal(t, c.Doc(path).Version, *dc.TextDocument.Version)
					} else {
						assert.Nil(t, dc.TextDocument.Version, where())
					}
					for _, e := range dc.Edits {
						if len(plan.PossibleEdits) > 0 {
							require.Contains(t, edit.ChangeAnnotations, e.AnnotationID, where())
						}
						e.AnnotationID = ""
						gotEdits = append(gotEdits, place{dc.TextDocument.URI, e})
					}
				}
				sortPlaces := func(ps []place) {
					slices.SortFunc(ps, func(a, b place) int {
						return cmp.Or(cmp.Compare(a.uri, b.uri), cmp.Compare(a.e.Range.Start.Line, b.e.Range.Start.Line),
							cmp.Compare(a.e.Range.Start.Character, b.e.Range.Start.Character))
					})
				}
				sortPlaces(want)
				sortPlaces(gotEdits)
				require.Equal(t, want, gotEdits, where())
				if len(plan.Edits) > 0 && plan.Edits[0].Path != path {
					across++
				}
				if len(plan.PossibleEdits) > 0 {
					possible++
				}
			}
			c.Close(path)
		}
		require.NoError(t, c.Shutdown())
		assert.Greater(t, targets, 80)
		assert.Greater(t, refused, 100)
		assert.Greater(t, across, 0)
		t.Logf("%s: %d places, %d to rename, %d refused, %d plans that edit other files, %d with possible edits",
			c.Encoding, places, targets, refused, across, possible)
	}
}
