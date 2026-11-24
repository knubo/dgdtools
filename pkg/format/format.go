// Package format implements the dgdfmt layout engine.
//
// The engine is a trivia-only rewriter: it emits every significant token's
// bytes verbatim and synthesizes only the whitespace between them. Safety is
// enforced twice: structurally (there is no code path that alters a
// significant token) and by the gate in Format, which re-lexes the output,
// requires token-stream equality with the input (tokcmp), and requires
// idempotence, before any byte is returned.
//
// It works in two steps. The plan (plan.go) reads the file the way every
// other tool does, with pp and syntax, and records for each token what the
// syntax tree says about it: the indentation level of its statement, and
// its role (binary or unary operator, block or literal brace, the name of
// a function definition). The printer (printer.go) then walks the token
// stream once and lays it out from the plan. Where the parser could not
// make sense of the source the plan says so, and the printer falls back
// to what the neighbouring tokens suggest, relative to the last level it
// knew, and leaves the line structure there alone.
//
// A file is formatted from its own text and the options alone: the same
// bytes give the same result in an editor buffer, in a partial checkout
// and in the whole lib.
//
// Style: KNF as practiced in DGD-era LPC. Function definitions get their
// specifiers and return type on one line, the name at column 0, and the
// opening brace on its own line at column 0; control-flow braces are
// cuddled. Statements are indented by their nesting, with or without
// braces; case labels sit at the switch statement's level; a line that
// continues a statement is indented one level further. Horizontal spacing
// between tokens is normalized gofmt-style while the author's line breaks
// are preserved. Comments are never reflowed: line comments keep their
// bytes, multi-line block comments are shifted whole, and function
// headers containing comments are left in their original layout.
package format

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/tokcmp"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// LineEndings selects the newline policy.
type LineEndings int

const (
	Preserve LineEndings = iota // keep each newline's original bytes
	LF                          // normalize to \n
	CRLF                        // normalize to \r\n
)

// HeaderStyle selects function-definition header layout.
type HeaderStyle int

const (
	// HeadersSplit is KNF: specifiers and return type on one line, the
	// function name at column 0, the brace on its own line.
	HeadersSplit HeaderStyle = iota
	// HeadersJoined keeps the whole header on one line; the brace still
	// gets its own line.
	HeadersJoined
)

// Options carries the formatter's (deliberately few) toggles and what the
// project says about its macros. The dialect is not an option here:
// Format always relexes its output under the same dialect the input File
// was lexed with.
type Options struct {
	Indent        int // spaces per level; 0 means the default 4
	LineEndings   LineEndings
	MaxBlankLines int // max consecutive blank lines; 0 means the default 2
	FuncHeaders   HeaderStyle

	// Defined are the names defined before the first line of every file
	// (config.Predefined), and Macros the macro classes the project
	// configures (config.MacroClasses): what lets `public int f()` be
	// read as a function in a file that does not define `public` itself.
	Defined map[string]bool
	Macros  map[string]pp.Class
}

// OptionsFromConfig returns the options a project's configuration asks
// for: its format settings and its macro conventions.
func OptionsFromConfig(c *config.Config) (Options, error) {
	o := Options{
		Indent:        c.Format.Indent,
		MaxBlankLines: c.Format.MaxBlankLines,
		Defined:       c.Predefined(),
		Macros:        c.MacroClasses(),
	}
	var err error
	if o.LineEndings, err = ParseLineEndings(c.Format.LineEndings); err != nil {
		return o, fmt.Errorf("format.line_endings: %w", err)
	}
	if o.FuncHeaders, err = ParseHeaderStyle(c.Format.FunctionHeaders); err != nil {
		return o, fmt.Errorf("format.function_headers: %w", err)
	}
	return o, nil
}

// ParseLineEndings maps a configuration spelling to a LineEndings; the
// empty string is the default, Preserve.
func ParseLineEndings(s string) (LineEndings, error) {
	switch s {
	case "", "preserve":
		return Preserve, nil
	case "lf":
		return LF, nil
	case "crlf":
		return CRLF, nil
	}
	return Preserve, fmt.Errorf("unknown line endings %q (want preserve, lf or crlf)", s)
}

// ParseHeaderStyle maps a configuration spelling to a HeaderStyle; the
// empty string is the default, HeadersSplit.
func ParseHeaderStyle(s string) (HeaderStyle, error) {
	switch s {
	case "", "split":
		return HeadersSplit, nil
	case "joined":
		return HeadersJoined, nil
	}
	return HeadersSplit, fmt.Errorf("unknown function header style %q (want split or joined)", s)
}

func (o Options) indent() int {
	if o.Indent <= 0 {
		return 4
	}
	return o.Indent
}

func (o Options) maxBlank() int {
	if o.MaxBlankLines <= 0 {
		return 2
	}
	return o.MaxBlankLines
}

// ErrIllegal is returned for files that did not fully tokenize; dgdfmt
// refuses to format anything it could not prove lossless.
var ErrIllegal = errors.New("file contains unlexable content; refusing to format")

// ErrGateFailure means the engine produced output whose significant token
// stream differs from the input: an engine bug caught before writing.
var ErrGateFailure = errors.New("formatter gate failure (output tokens diverge from input)")

// ErrNotIdempotent means formatting its own output changed it again.
var ErrNotIdempotent = errors.New("formatter is not idempotent on this input")

// ErrInternal means the engine failed on this input: a bug, reported
// instead of taking down a run over many files or an editor session.
var ErrInternal = errors.New("internal error (please report)")

// Format returns the formatted source, or an error if the gate trips.
// The input File is not modified.
func Format(f *token.File, o Options) (out []byte, err error) {
	if f.HasIllegal() {
		return nil, fmt.Errorf("%s: %w", f.Path, ErrIllegal)
	}
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("%s: %w: %v", f.Path, ErrInternal, r)
		}
	}()

	out = engine(f, o)

	relexed := lexer.Lex(f.Path, out, f.Dialect)
	if err := relexed.CheckRoundTrip(); err != nil {
		return nil, fmt.Errorf("%s: %w: %v", f.Path, ErrGateFailure, err)
	}
	if eq, div := tokcmp.Compare(f, relexed); !eq {
		return nil, fmt.Errorf("%s: %w: input %s vs output %s", f.Path, ErrGateFailure, div.APos, div.BPos)
	}
	if again := engine(relexed, o); !bytes.Equal(again, out) {
		return nil, fmt.Errorf("%s: %w", f.Path, ErrNotIdempotent)
	}
	return out, nil
}

// engine performs the actual layout. It must be deterministic.
func engine(f *token.File, o Options) []byte {
	p := &printer{
		f:    f,
		o:    o,
		nl:   dominantNewline(f),
		plan: buildPlan(f, o),
		prev: -1,
	}
	p.run()
	return p.buf.Bytes()
}

func dominantNewline(f *token.File) []byte {
	crlf, lf := 0, 0
	for _, t := range f.Tokens {
		if t.Kind == token.Newline {
			if t.Len() >= 2 { // \r\n
				crlf++
			} else if f.Src[t.Off] == '\n' {
				lf++
			}
		}
	}
	if crlf > lf {
		return []byte("\r\n")
	}
	return []byte("\n")
}
