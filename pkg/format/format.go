// Package format is the formatter: the engine behind `dgdtools format`,
// the `unformatted` lint rule and an editor's formatting requests.
//
// The engine is a trivia-only rewriter: it emits every significant token's
// bytes verbatim and synthesizes only the whitespace between them. Safety is
// enforced twice: structurally (there is no code path that alters a
// significant token) and by the gate in Format, which re-lexes the output,
// requires token-stream equality with the input (tokcmp), the same
// comments, and idempotence, before any byte is returned.
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
// There is one style and three settings (indentation with tabs or
// spaces, its width, line endings); see docs/design/formatter.md for why.
// Between a comment `dgdtools-ignore-start format` and the
// `dgdtools-ignore-end format` that answers it (pkg/directive) nothing is
// touched, and Lines formats only part of a file.
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
	"strings"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/tokcmp"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// IndentStyle selects what a level of indentation is written with.
type IndentStyle int

const (
	Spaces IndentStyle = iota // IndentWidth spaces per level
	Tabs                      // one tab per level
)

// LineEndings selects the newline policy.
type LineEndings int

const (
	Preserve LineEndings = iota // keep each newline's original bytes
	LF                          // normalize to \n
	CRLF                        // normalize to \r\n
)

// maxBlankLines is the most consecutive blank lines the formatter keeps.
const maxBlankLines = 2

// Options are the formatter's settings and what the project says about
// its macros. There are three settings, the ones an editor has to agree
// with; everything else is the one style (docs/design/formatter.md). The
// dialect is not an option here: a result is always relexed under the
// dialect the input File was lexed with.
type Options struct {
	IndentStyle IndentStyle
	IndentWidth int // spaces per level with Spaces; 0 means config.DefaultIndentWidth
	LineEndings LineEndings

	// Defined are the names defined before the first line of every file
	// (config.Predefined), and Macros the macro classes the project
	// configures (config.MacroClasses): what lets `public int f()` be
	// read as a function in a file that does not define `public` itself.
	Defined map[string]bool
	Macros  map[string]pp.Class
}

// OptionsFor returns the options for a file of the project configured by
// c whose format settings are s (config.Settings.Format; unset keys are
// the defaults).
func OptionsFor(c *config.Config, s config.Format) Options {
	o := Options{IndentWidth: s.IndentWidth, Defined: c.Predefined(), Macros: c.MacroClasses()}
	if s.IndentStyle == config.IndentTab {
		o.IndentStyle = Tabs
	}
	switch s.LineEndings {
	case config.LineEndingsLF:
		o.LineEndings = LF
	case config.LineEndingsCRLF:
		o.LineEndings = CRLF
	}
	return o
}

// Source formats src, the text of the file at path (absolute; it need
// not exist), as its project prj has that file formatted: in the
// project's dialect, with the file's own settings (config.Project.Settings).
// With lines, only those lines are formatted (Lines). It is the whole of
// what `dgdtools format` and an editor's formatting request do to a
// text; Edits turns the result into text edits.
func Source(prj *config.Project, path string, src []byte, lines []LineRange) ([]byte, error) {
	set, err := prj.Settings(path)
	if err != nil {
		return nil, err
	}
	f := lexer.Lex(path, src, prj.Config.TokenDialect())
	o := OptionsFor(prj.Config, set.Format)
	if len(lines) == 0 {
		return Format(f, o)
	}
	return Lines(f, o, lines)
}

// unit returns one level of indentation.
func (o Options) unit() string {
	if o.IndentStyle == Tabs {
		return "\t"
	}
	n := o.IndentWidth
	if n <= 0 {
		n = config.DefaultIndentWidth
	}
	return strings.Repeat(" ", n)
}

// ErrIllegal is returned for files that did not fully tokenize; nothing
// is formatted that could not be proven lossless.
var ErrIllegal = errors.New("file contains unlexable content; refusing to format")

// ErrGateFailure means the engine produced output whose significant token
// stream differs from the input: an engine bug caught before writing.
var ErrGateFailure = errors.New("formatter gate failure (output tokens diverge from input)")

// ErrNotIdempotent means formatting its own output changed it again.
var ErrNotIdempotent = errors.New("formatter is not idempotent on this input")

// ErrInternal means the engine failed on this input: a bug, reported
// instead of taking down a run over many files or an editor session.
var ErrInternal = errors.New("internal error (please report)")

// LineRange is a range of source lines, 1-based and inclusive.
type LineRange struct{ First, Last int }

// Format returns the formatted source, or an error if the gate trips.
// The input File is not modified.
func Format(f *token.File, o Options) ([]byte, error) {
	return gated(f, o, nil)
}

// Lines formats only the given lines of the file and returns the whole
// text, every other line byte for byte as it was.
//
// What is formatted is the whitespace that belongs to a selected line:
// its indentation, the line break before it and the spacing inside it,
// and the comments and blank lines directly above it when they are
// selected too. A selected line is indented from the lines around it: an
// unselected line that begins a statement says how deep its nesting
// level is indented in this file, and a selected line follows that. In a
// file that is already formatted the result is what Format gives.
func Lines(f *token.File, o Options, lines []LineRange) ([]byte, error) {
	return gated(f, o, selectLines(f, lines))
}

// gated runs the engine and returns its output only when it is provably
// a formatting of f: the same significant tokens, and a fixed point.
func gated(f *token.File, o Options, sel *selection) (out []byte, err error) {
	if f.HasIllegal() {
		return nil, fmt.Errorf("%s: %w", f.Path, ErrIllegal)
	}
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("%s: %w: %v", f.Path, ErrInternal, r)
		}
	}()

	out = engine(f, o, sel)

	relexed := lexer.Lex(f.Path, out, f.Dialect)
	if err := relexed.CheckRoundTrip(); err != nil {
		return nil, fmt.Errorf("%s: %w: %v", f.Path, ErrGateFailure, err)
	}
	if eq, div := tokcmp.Compare(f, relexed); !eq {
		return nil, fmt.Errorf("%s: %w: input %s vs output %s", f.Path, ErrGateFailure, div.APos, div.BPos)
	}
	if !sameComments(f, relexed) {
		return nil, fmt.Errorf("%s: %w: a comment is not what it was", f.Path, ErrGateFailure)
	}
	// A selection is by gap, and the gaps of the output are those of the
	// input: the same selection applies to it.
	if again := engine(relexed, o, sel); !bytes.Equal(again, out) {
		return nil, fmt.Errorf("%s: %w", f.Path, ErrNotIdempotent)
	}
	return out, nil
}

// sameComments reports whether b has the comments of a, in their order:
// each byte for byte, but for the indentation of a block comment's
// further lines, which moves with the comment.
func sameComments(a, b *token.File) bool {
	next := func(f *token.File, i int) int {
		for ; i < len(f.Tokens); i++ {
			if k := f.Tokens[i].Kind; k == token.LineComment || k == token.BlockComment {
				return i
			}
		}
		return -1
	}
	i, j := next(a, 0), next(b, 0)
	for ; i >= 0 && j >= 0; i, j = next(a, i+1), next(b, j+1) {
		x, y := a.Text(a.Tokens[i]), b.Text(b.Tokens[j])
		if !bytes.Equal(x, y) && !bytes.Equal(unindented(x), unindented(y)) {
			return false
		}
	}
	return i < 0 && j < 0
}

// unindented returns a comment without the blanks its lines after the
// first begin with.
func unindented(comment []byte) []byte {
	var out []byte
	for k, line := range splitKeepEndings(comment) {
		if k > 0 {
			line = bytes.TrimLeft(line, " \t")
		}
		out = append(out, line...)
	}
	return out
}

// engine performs the actual layout. It must be deterministic. A nil
// selection formats everything.
func engine(f *token.File, o Options, sel *selection) []byte {
	p := &printer{
		f:    f,
		o:    o,
		unit: o.unit(),
		nl:   dominantNewline(f),
		plan: buildPlan(f, o),
		sel:  sel,
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
