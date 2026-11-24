// Package directive reads the comments in a source file that are
// addressed to dgdtools: the ones that tell the linter or the formatter
// to leave something alone.
//
//	x = "a" "b";  /* dgdtools-ignore lint/adjacent-strings */
//
//	/* dgdtools-ignore lint/callable-not-found: defined by the daemon */
//	"/sys/d"->late();
//
//	/* dgdtools-ignore-start format */
//	int *table = ({ 1,   2,
//	                10, 20 });
//	/* dgdtools-ignore-end format */
//
// There is one prefix, the tool's name, for every part of the tool; what
// is ignored is said after it, as in Biome's `biome-ignore lint/...` and
// `biome-ignore format`. A comment is a directive when it begins with the
// prefix; a comment that only mentions it is prose.
//
//	directive = "dgdtools-ignore" [ "-start" | "-end" ] [ targets ] [ ":" reason ]
//	targets   = target { "," target }
//	target    = "format" | "lint" | "lint/" rule
//
// The plain form is about one line, -start and -end about what lies
// between them. The directive is the first thing in its comment and its
// targets are on that line; the reason is for people, and may go on. This package only reads the
// comments; what a target means is the linter's and the formatter's
// business (pkg/lint, pkg/format).
package directive

import (
	"bytes"

	"github.com/mmcdole/dgdtools/pkg/token"
)

// Prefix is what a directive begins with.
const Prefix = "dgdtools-ignore"

// The tools a target can name.
const (
	Lint   = "lint"
	Format = "format"
)

// Form is what a directive covers.
type Form uint8

const (
	// Line: a line of code.
	Line Form = iota
	// Start: everything from here to the End that answers it, or to the
	// end of the file.
	Start
	// End: closes what a Start opened.
	End
)

func (f Form) String() string {
	return [...]string{Prefix, Prefix + "-start", Prefix + "-end"}[f]
}

// Target is one thing a directive names.
type Target struct {
	// Tool is Lint, Format, or whatever else was written there.
	Tool string
	// Rule is what follows "lint/", as written.
	Rule string
	// Off and End are the target's place in the file.
	Off, End uint32
}

// String returns the target as written.
func (t Target) String() string {
	if t.Rule != "" {
		return t.Tool + "/" + t.Rule
	}
	return t.Tool
}

// Directive is one comment addressed to dgdtools.
type Directive struct {
	Form    Form
	Targets []Target
	// Off and End are the comment's place in the file.
	Off, End uint32
}

// Has reports whether the directive names the tool.
func (d *Directive) Has(tool string) bool {
	for _, t := range d.Targets {
		if t.Tool == tool {
			return true
		}
	}
	return false
}

// Scan returns the directives of a file, in order.
func Scan(f *token.File) []Directive {
	var out []Directive
	for _, t := range f.Tokens {
		if d, ok := Parse(f, t); ok {
			out = append(out, d)
		}
	}
	return out
}

// Parse reads the token t of f as a directive; false when it is not one.
func Parse(f *token.File, t token.Token) (Directive, bool) {
	text := f.Text(t)
	if t.Kind != token.LineComment && t.Kind != token.BlockComment {
		return Directive{}, false
	}
	// i walks text, from after the comment's opener and what decorates
	// it, to before what closes it.
	i := 2
	for i < len(text) && (text[i] == ' ' || text[i] == '\t' || text[i] == '*') {
		i++
	}
	if t.Kind == token.BlockComment {
		text = bytes.TrimRight(bytes.TrimSuffix(text, []byte("*/")), "* \t")
	}
	if i > len(text) {
		return Directive{}, false // nothing but decoration
	}
	if !bytes.HasPrefix(text[i:], []byte(Prefix)) {
		return Directive{}, false
	}
	i += len(Prefix)
	d := Directive{Off: t.Off, End: t.End}
	switch {
	case bytes.HasPrefix(text[i:], []byte("-start")):
		d.Form, i = Start, i+len("-start")
	case bytes.HasPrefix(text[i:], []byte("-end")):
		d.Form, i = End, i+len("-end")
	}
	if i < len(text) && !separates(text[i]) && text[i] != ':' && text[i] != '\r' && text[i] != '\n' {
		return Directive{}, false // another word that begins the same
	}
	// The targets: the rest of the line, up to the reason.
	if n := bytes.IndexAny(text[i:], ":\r\n"); n >= 0 {
		text = text[:i+n]
	}
	for {
		for i < len(text) && separates(text[i]) {
			i++
		}
		if i == len(text) {
			return d, true
		}
		start := i
		for i < len(text) && !separates(text[i]) {
			i++
		}
		tg := Target{Tool: string(text[start:i]), Off: t.Off + uint32(start), End: t.Off + uint32(i)}
		if tool, rule, ok := bytes.Cut(text[start:i], []byte("/")); ok && len(rule) > 0 {
			tg.Tool, tg.Rule = string(tool), string(rule)
		}
		d.Targets = append(d.Targets, tg)
	}
}

// separates reports whether c stands between targets.
func separates(c byte) bool { return c == ' ' || c == '\t' || c == ',' }
