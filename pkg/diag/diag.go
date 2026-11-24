// Package diag defines diagnostics shared by the lexer, formatter, and linter.
package diag

import (
	"fmt"
	"unicode/utf8"
)

type Severity uint8

const (
	Info Severity = iota
	Warning
	Error
)

func (s Severity) String() string {
	switch s {
	case Info:
		return "info"
	case Warning:
		return "warning"
	case Error:
		return "error"
	}
	return "unknown"
}

// ParseSeverity maps a config spelling to a Severity.
func ParseSeverity(s string) (Severity, bool) {
	switch s {
	case "info", "note":
		return Info, true
	case "warning", "warn":
		return Warning, true
	case "error":
		return Error, true
	}
	return 0, false
}

// Diagnostic is one finding tied to a source range. Rule and Code are
// empty for diagnostics that do not come from a lint rule (lexer errors
// reported by dgdfmt).
//
// Line and Col are 1-based; Col counts bytes. Start and End are byte
// offsets into the file (End exclusive); use UTF16Col to derive the
// columns an LSP client expects.
type Diagnostic struct {
	Path     string
	Line     int
	Col      int
	EndLine  int
	EndCol   int
	Start    uint32
	End      uint32
	Severity Severity
	Message  string
	Rule     string // stable rule name, e.g. "syntax-error"
	Code     string // stable short code, e.g. "E001"
	Related  []Related
	Fixes    []Fix
}

// Related is a secondary location that explains a diagnostic.
type Related struct {
	Path    string
	Line    int
	Col     int
	Start   uint32
	End     uint32
	Message string
}

// Fix is a suggested change, expressed as text edits against the file the
// diagnostic is in. The linter never applies fixes itself.
type Fix struct {
	Title string
	Edits []TextEdit
}

// TextEdit replaces the bytes [Start, End) with NewText.
type TextEdit struct {
	Start   uint32
	End     uint32
	NewText string
}

func (d Diagnostic) String() string {
	if d.Rule != "" {
		return fmt.Sprintf("%s:%d:%d: %s: %s [%s]", d.Path, d.Line, d.Col, d.Severity, d.Message, d.Rule)
	}
	return fmt.Sprintf("%s:%d:%d: %s: %s", d.Path, d.Line, d.Col, d.Severity, d.Message)
}

// UTF16Col converts a 1-based byte column within line (the bytes of one
// source line) to a 0-based UTF-16 code-unit offset, the unit LSP
// positions use by default. Bytes that are not valid UTF-8 (the corpus
// has Latin-1 files) count as one unit each, which is how editors that
// decode them as single-byte text see them.
func UTF16Col(line []byte, byteCol int) int {
	n := byteCol - 1
	if n > len(line) {
		n = len(line)
	}
	units := 0
	for i := 0; i < n; {
		r, size := utf8.DecodeRune(line[i:])
		if r == utf8.RuneError && size <= 1 {
			units++
			i++
			continue
		}
		if i+size > n { // column points into the middle of a rune
			units++
			break
		}
		if r >= 0x10000 {
			units += 2
		} else {
			units++
		}
		i += size
	}
	return units
}
