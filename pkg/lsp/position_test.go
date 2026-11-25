package lsp_test

import (
	"math/rand"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/lsp"
	"github.com/mmcdole/dgdtools/pkg/lsp/lsptest"
	"github.com/mmcdole/dgdtools/pkg/profile"
)

// texts have every kind of line break and of character: ASCII, two and
// three bytes, outside the basic plane (two UTF-16 units), and bytes
// that are no UTF-8 at all (a Latin-1 file).
var texts = []string{
	"",
	"\n",
	"int x;",
	"int x;\nint y;\n",
	"a\r\nb\rc\n\r\n\rd",
	"string s = \"blåbær € \U0001F600 end\"; x == 1;\n/* \U0001F600\U0001F600 */ y == 2;\r\n",
	"string s = \"bl\xe5b\xe6r\"; x == 1;\n// gr\xf8t\n",
	"\U0001F600",
	"\r",
	"x\r\r\n\n\ry",
}

// The mapper agrees with arithmetic done the long way, in both
// encodings, at every offset; and a position and its offset give each
// other back wherever the offset begins a character.
func TestPositionMapper(t *testing.T) {
	for _, text := range texts {
		assert.Equal(t, lexer.Lex("t.c", []byte(text), lexer.Default).Lines, lsp.LineStarts([]byte(text)), "%q: the lexer's lines", text)
		for _, enc := range []string{lsp.UTF16, lsp.UTF8} {
			m := lsp.NewPositionMapper([]byte(text), enc)
			for _, off := range starts(text) {
				p := m.Position(uint32(off))
				require.Equal(t, lsptest.PositionOf(text, off, enc), p, "%q %s offset %d", text, enc, off)
				back := int(m.Offset(p))
				require.Equal(t, lsptest.OffsetOf(text, p, enc), back, "%q %s %+v", text, enc, p)
				// An offset between the two bytes of a line break is no
				// position: it is given as the end of the line.
				if inBreak := off > 0 && text[off-1] == '\r' && off < len(text) && text[off] == '\n'; !inBreak {
					require.Equal(t, off, back, "%q %s offset %d: there and back", text, enc, off)
				}
			}
			// Past the end of a line is the end of the line; past the
			// last line is the end of the text.
			lines := m.Lines()
			for line := 0; line < lines; line++ {
				end := m.Offset(lsp.Position{Line: line, Character: 1 << 20})
				assert.Equal(t, m.Position(end), m.Position(m.Offset(lsp.Position{Line: line, Character: m.Position(end).Character})))
				if line+1 < lines {
					assert.Less(t, int(end), int(m.Offset(lsp.Position{Line: line + 1})), "%q: before the line break", text)
				}
			}
			assert.Equal(t, len(text), int(m.Offset(lsp.Position{Line: lines, Character: 0})))
			assert.Equal(t, len(text), int(m.Offset(lsp.Position{Line: lines + 7, Character: 3})))
			assert.Equal(t, 0, int(m.Offset(lsp.Position{Line: -1, Character: -1})))
			assert.Equal(t, lsptest.PositionOf(text, len(text), enc), m.Position(uint32(len(text)+10)))
		}
	}
	// A lone carriage return inside a string that is never closed: a
	// line break to an editor, which is whom the positions are for, and
	// none to the lexer.
	odd := "x = \"a\rb;\n"
	assert.Equal(t, []uint32{0, 7, 10}, lsp.LineStarts([]byte(odd)))
	assert.Equal(t, []uint32{0, 10}, lexer.Lex("t.c", []byte(odd), lexer.Default).Lines)
	assert.Equal(t, lsp.Position{Line: 1, Character: 1}, lsp.NewPositionMapper([]byte(odd), lsp.UTF16).Position(8))

	// The columns are those of diag.UTF16Col, which the reports use.
	line := []byte("\"blå \U0001F600\" x")
	m := lsp.NewPositionMapper(line, lsp.UTF16)
	for _, off := range starts(string(line)) {
		assert.Equal(t, diag.UTF16Col(line, off+1), m.Position(uint32(off)).Character)
	}
}

// starts returns the offsets in text at which a character begins, a byte
// that is no UTF-8 being one, and the end of the text.
func starts(text string) []int {
	var out []int
	for i := 0; i < len(text); {
		out = append(out, i)
		_, size := utf8.DecodeRuneInString(text[i:])
		i += size
	}
	return append(out, len(text))
}

// edit returns a random edit of text: a range that begins and ends at a
// character, replaced by a few characters of every kind.
func edit(r *rand.Rand, text string) lsptest.Edit {
	bound := func() int {
		i := r.Intn(len(text) + 1)
		for i < len(text) && !utf8.RuneStart(text[i]) {
			i++
		}
		// Not between the two bytes of a line break: no position is.
		if i > 0 && i < len(text) && text[i-1] == '\r' && text[i] == '\n' {
			i++
		}
		return i
	}
	a, b := bound(), bound()
	if a > b {
		a, b = b, a
	}
	if r.Intn(3) > 0 && b-a > 12 {
		b = a + r.Intn(12)
		for b < len(text) && !utf8.RuneStart(text[b]) {
			b++
		}
		if b > 0 && b < len(text) && text[b-1] == '\r' && text[b] == '\n' {
			b++
		}
	}
	pieces := []string{"x", " ", "\n", "\r\n", "\r", ";", "{", "}", "(", ")", "\"", "/*", "*/", "==", "1", "å", "€", "\U0001F600",
		"int ", "f();", "#include <std.h>\n", "inherit \"/std/base\";\n", "\"/obj/b\"->bar();", "\\"}
	ins := ""
	for range r.Intn(4) {
		ins += pieces[r.Intn(len(pieces))]
	}
	return lsptest.Edit{Start: a, End: b, Text: ins}
}

// Incremental changes leave the server with the text the editor has: a
// random sequence of edits, sent as ranges in either encoding, against
// the same edits made to a plain string.
func TestIncrementalChanges(t *testing.T) {
	for seed := range int64(40) {
		r := rand.New(rand.NewSource(seed))
		enc := []string{lsp.UTF16, lsp.UTF8}[seed%2]
		text := texts[r.Intn(len(texts))]
		if !utf8.ValidString(text) {
			text = texts[5] // an editor's text is Unicode
		}
		server := []byte(text)
		for step := range 60 {
			var changes []lsp.TextDocumentContentChangeEvent
			for range 1 + r.Intn(3) { // one notification may carry several
				e := edit(r, text)
				changes = append(changes, lsp.TextDocumentContentChangeEvent{
					Range: &lsp.Range{Start: lsptest.PositionOf(text, e.Start, enc), End: lsptest.PositionOf(text, e.End, enc)},
					Text:  e.Text,
				})
				text = text[:e.Start] + e.Text + text[e.End:]
			}
			old, was := server, string(server)
			server = lsp.Change(server, enc, changes...)
			require.Equal(t, text, string(server), "seed %d step %d (%s): %+v", seed, step, enc, changes)
			require.Equal(t, was, string(old), "the text before is whoever still reads it's")
		}
		// A change without a range is the whole text.
		assert.Equal(t, "new", string(lsp.Change(server, enc, lsp.TextDocumentContentChangeEvent{Text: "new"})))
	}
}

// FuzzChange is TestIncrementalChanges with the edits read from the
// fuzzer's bytes: go test -run '^$' -fuzz FuzzChange ./pkg/lsp
func FuzzChange(f *testing.F) {
	for _, text := range texts {
		f.Add(text, int64(1), true)
		f.Add(text, int64(2), false)
	}
	f.Fuzz(func(t *testing.T, text string, seed int64, wide bool) {
		if !utf8.ValidString(text) {
			t.Skip()
		}
		enc := lsp.UTF8
		if wide {
			enc = lsp.UTF16
		}
		r := rand.New(rand.NewSource(seed))
		server := []byte(text)
		for range 20 {
			e := edit(r, text)
			c := lsp.TextDocumentContentChangeEvent{
				Range: &lsp.Range{Start: lsptest.PositionOf(text, e.Start, enc), End: lsptest.PositionOf(text, e.End, enc)}, Text: e.Text}
			text = text[:e.Start] + e.Text + text[e.End:]
			if server = lsp.Change(server, enc, c); string(server) != text {
				t.Fatalf("%+v: have %q, want %q", c, server, text)
			}
			m := lsp.NewPositionMapper(server, enc)
			for _, off := range starts(text) {
				if p := m.Position(uint32(off)); p != lsptest.PositionOf(text, off, enc) {
					t.Fatalf("%q offset %d: %+v, want %+v", text, off, p, lsptest.PositionOf(text, off, enc))
				}
			}
		}
	})
}

func TestLineRange(t *testing.T) {
	for _, c := range []struct {
		r           lsp.Range
		first, last int
	}{
		{lsp.Range{Start: lsp.Position{Line: 2, Character: 4}, End: lsp.Position{Line: 2, Character: 9}}, 3, 3},
		{lsp.Range{Start: lsp.Position{Line: 2}, End: lsp.Position{Line: 5, Character: 1}}, 3, 6},
		// A selection of whole lines ends at the start of the next.
		{lsp.Range{Start: lsp.Position{Line: 2}, End: lsp.Position{Line: 5}}, 3, 5},
		{lsp.Range{Start: lsp.Position{Line: 2}, End: lsp.Position{Line: 2}}, 3, 3},
	} {
		first, last := lsp.LineRange(c.r)
		assert.Equal(t, [2]int{c.first, c.last}, [2]int{first, last}, "%+v", c.r)
	}
}

func TestURIs(t *testing.T) {
	assert.Equal(t, "/lib/obj/a.c", lsp.PathOf("file:///lib/obj/a.c"))
	assert.Equal(t, "/lib/a b/å.c", lsp.PathOf("file:///lib/a%20b/%C3%A5.c"))
	assert.Equal(t, "/lib/a.c", lsp.PathOf("file://localhost/lib/x/../a.c"))
	assert.Equal(t, "", lsp.PathOf("untitled:Untitled-1"))
	assert.Equal(t, "", lsp.PathOf("dgdtools-profile://mylib/std/room.c"))
	assert.Equal(t, "file:///lib/a%20b/%C3%A5.c", lsp.URIOf("/lib/a b/å.c"))
	// A stub of a profile is named by its URI already.
	assert.Equal(t, "dgdtools-profile://mylib/std/room.c", lsp.URIOf("dgdtools-profile://mylib/std/room.c"))
	assert.Equal(t, "dgdtools-profile://my%20lib/std/a%20b%3F.c", lsp.URIOf("dgdtools-profile://my lib/std/a b?.c"))
}

// The URI of a stub comes back from an editor as the editor writes
// URIs, and names the same stub.
func TestStubURIs(t *testing.T) {
	type stub struct{ name, file string }
	for uri, want := range map[string]stub{
		"dgdtools-profile://mylib/std/room.c":                     {"mylib", "/std/room.c"},
		"dgdtools-profile://my%20lib/std/a%20b%3F.c":              {"my lib", "/std/a b?.c"},
		"dgdtools-profile://mylib/include/std.h?x=1#L5":           {"mylib", "/include/std.h"},
		"dgdtools-profile://mylib":                                {"mylib", "/"},
		lsp.URIOf(profile.Location("Viking MUD") + "/std/room.c"): {"Viking MUD", "/std/room.c"},
	} {
		name, file, ok := lsp.StubOf(uri)
		assert.True(t, ok, uri)
		assert.Equal(t, want, stub{name, file}, uri)
		// What the server says is what the library splits.
		n, f, ok := profile.SplitLocation(profile.Location(name) + file)
		assert.True(t, ok)
		assert.Equal(t, want, stub{n, f}, uri)
	}
	for _, not := range []string{"file:///std/room.c", "dgdtools-profile:/std/room.c", "dgdtools-profile:///std/room.c", "dgdtools-profile://a%zz/x.c", ""} {
		_, _, ok := lsp.StubOf(not)
		assert.False(t, ok, not)
	}
}

func TestSafeFixes(t *testing.T) {
	fix := func(a diag.Applicability, edits ...diag.TextEdit) diag.Diagnostic {
		return diag.Diagnostic{Fixes: []diag.Fix{{Applicability: a, Edits: edits}}}
	}
	got := lsp.SafeFixes([]diag.Diagnostic{
		fix(diag.Safe, diag.TextEdit{Start: 20, End: 22, NewText: "c"}),
		fix(diag.Unsafe, diag.TextEdit{Start: 1, End: 2, NewText: "never"}),
		fix(diag.Safe, diag.TextEdit{Start: 4, End: 8, NewText: "a"}, diag.TextEdit{Start: 30, End: 30, NewText: "a2"}),
		// Overlaps the one before it, and one that inserts where another does.
		fix(diag.Safe, diag.TextEdit{Start: 6, End: 9, NewText: "b"}),
		fix(diag.Safe, diag.TextEdit{Start: 30, End: 30, NewText: "d"}),
	})
	assert.Equal(t, []diag.TextEdit{{Start: 4, End: 8, NewText: "a"}, {Start: 20, End: 22, NewText: "c"}, {Start: 30, End: 30, NewText: "a2"}}, got)
}
