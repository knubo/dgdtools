package lsp

import (
	"net/url"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/profile"
)

// PositionMapper converts between the positions of the protocol and byte
// offsets in one text, in the position encoding the client and the server
// agreed on. Every position that comes in and every range that goes out
// passes through one.
//
// A line ends where the protocol says it does: at "\n", "\r\n" or a
// "\r" alone. The lexer's own line table is not used: it agrees except
// about a lone "\r" inside a token (a string that is never closed), and
// an editor counts lines by the protocol. With the encoding "utf-8" a
// column is a byte offset in the line, which is what the tools count,
// and nothing is converted; with "utf-16" it is a number of UTF-16 code
// units (diag.UTF16Col, diag.ByteCol). A position past the end of its
// line is the end of the line, and one past the last line is the end of
// the text.
type PositionMapper struct {
	src   []byte
	lines []uint32 // where each line starts
	utf8  bool
}

// NewPositionMapper returns the mapper of src for the given encoding
// (UTF8, or UTF16 for anything else).
func NewPositionMapper(src []byte, encoding string) *PositionMapper {
	return &PositionMapper{src: src, lines: lineStarts(src), utf8: encoding == UTF8}
}

// lineStarts returns the offset each line of src starts at.
func lineStarts(src []byte) []uint32 {
	lines := []uint32{0}
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '\r':
			if i+1 < len(src) && src[i+1] == '\n' {
				i++
			}
			fallthrough
		case '\n':
			lines = append(lines, uint32(i+1))
		}
	}
	return lines
}

// line returns the bytes of the 0-based line n, without its line break.
func (m *PositionMapper) line(n int) []byte {
	end := len(m.src)
	if n+1 < len(m.lines) {
		end = int(m.lines[n+1])
		if end > 0 && m.src[end-1] == '\n' {
			end--
		}
		if end > int(m.lines[n]) && m.src[end-1] == '\r' {
			end--
		}
	}
	return m.src[m.lines[n]:end]
}

// Lines returns the number of lines of the text; the last may be empty.
func (m *PositionMapper) Lines() int { return len(m.lines) }

// Offset returns the byte offset of p.
func (m *PositionMapper) Offset(p Position) uint32 {
	switch {
	case p.Line < 0:
		return 0
	case p.Line >= len(m.lines):
		return uint32(len(m.src))
	}
	line := m.line(p.Line)
	col := max(p.Character, 0)
	if m.utf8 {
		col = min(col, len(line))
	} else {
		col = diag.ByteCol(line, col) - 1
	}
	return m.lines[p.Line] + uint32(col)
}

// Position returns the position of the byte offset off. An offset inside
// a line break is the end of the line.
func (m *PositionMapper) Position(off uint32) Position {
	off = min(off, uint32(len(m.src)))
	n := sort.Search(len(m.lines), func(i int) bool { return m.lines[i] > off }) - 1
	line := m.line(n)
	col := min(int(off-m.lines[n]), len(line))
	if !m.utf8 {
		col = diag.UTF16Col(line, col+1)
	}
	return Position{Line: n, Character: col}
}

// Range returns the range of the bytes [start, end).
func (m *PositionMapper) Range(start, end uint32) Range {
	return Range{Start: m.Position(start), End: m.Position(max(start, end))}
}

// A document is named by a URI and the tools name files by path.

// pathOf returns the file a URI names, "" when it names none: a document
// that was never saved, or a stub of a profile.
func pathOf(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") {
		return ""
	}
	p := u.Path
	if runtime.GOOS == "windows" {
		// file:///C:/lib/a.c
		if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
		p = filepath.FromSlash(p)
	}
	if !filepath.IsAbs(p) {
		return ""
	}
	return filepath.Clean(p)
}

// uriOf returns the URI of a location the tools name: a file by its
// absolute path, or a stub of a profile, which is named by a URI already
// and needs no more than escaping.
func uriOf(path string) string {
	if name, file, stub := profile.SplitLocation(path); stub {
		return stubURI(name, file)
	}
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}
