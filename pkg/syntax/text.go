package syntax

import (
	"strings"

	"github.com/mmcdole/dgdtools/pkg/token"
)

// Declarations rendered as text: what a signature display, a hover or a
// profile stub shows of them.

// SourceText returns the significant tokens first..last joined into one
// line: the text as written minus comments, directives and layout.
func (f *File) SourceText(first, last int) string {
	var b strings.Builder
	toks := f.Tok.Tokens
	prev := token.EOF
	for i := first; i <= last && i < len(toks); i++ {
		t := toks[i]
		if t.Kind.IsTrivia() || t.Kind == token.Directive || t.Kind == token.EOF {
			continue
		}
		switch {
		case b.Len() == 0, prev == token.LParen, prev == token.Star && t.Kind != token.Star && t.Kind != token.Comma && t.Kind != token.RParen,
			t.Kind == token.Comma, t.Kind == token.RParen, t.Kind == token.Semicolon, t.Kind == token.Ellipsis,
			t.Kind == token.LParen && prev == token.Ident:
		default:
			b.WriteByte(' ')
		}
		b.Write(f.Tok.Text(t))
		prev = t.Kind
	}
	return b.String()
}

// DeclPrefix renders the specifiers and type of a declaration as written,
// with a trailing space when non-empty.
func (f *File) DeclPrefix(specs []Spec, typ *TypeSpec) string {
	var parts []string
	for _, s := range specs {
		parts = append(parts, string(f.Tok.Text(f.Tok.Tokens[s.Tok])))
	}
	if typ != nil {
		parts = append(parts, f.SourceText(typ.First, typ.Last))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ") + " "
}

// TypeText renders a declared type with its '*'s: "string *".
func (f *File) TypeText(typ *TypeSpec, stars int) string {
	if typ == nil {
		return ""
	}
	s := f.SourceText(typ.First, typ.Last)
	if stars > 0 {
		s += " " + strings.Repeat("*", stars)
	}
	return s
}

// FuncDeclText renders a function's declaration as written, without body
// or terminator: specifiers, return type, name and parameter list.
func (f *File) FuncDeclText(fn *FuncDecl) string {
	name := "operator"
	if fn.Name != nil {
		name = fn.Name.Name
	}
	return f.DeclPrefix(fn.Specs, fn.Type) + strings.Repeat("*", fn.Stars) + name +
		"(" + f.SourceText(fn.LParen+1, fn.RParen-1) + ")"
}
