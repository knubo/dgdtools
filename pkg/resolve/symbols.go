package resolve

import (
	"cmp"
	"slices"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/syntax"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// Symbol is a declaration of a file, as an outline of the file lists
// it.
type Symbol struct {
	// Kind is Object for an inherit statement, Function for a function's
	// definition or prototype, Variable for a global variable, Macro for
	// a #define.
	Kind Kind
	// Name is the declared name; of an inherit statement the object
	// inherited, by its lib path, or the path as written when that
	// cannot be told.
	Name string
	// Detail is what the declaration says beside the name: a function's
	// specifiers, return type and parameters ("static int (string
	// what)"), a variable's type, "inherit" with the label, "#define"
	// with the parameters.
	Detail string
	// Start and End are the bytes of the whole declaration, a function's
	// with its body; NameStart and NameEnd those of the name in it (of
	// an inherit statement the path).
	Start, End         uint32
	NameStart, NameEnd uint32
}

// Symbols returns the declarations of the file in the order they stand
// in it: its inherit statements, its functions (a prototype and a
// definition are two), its global variables, and the macros it defines
// where the preprocessor reads them. What an included file declares is
// that file's, and what could not be parsed is not there.
func (r *Resolver) Symbols() []Symbol {
	t := r.Tree
	if r.Tok == nil || t == nil {
		return nil
	}
	var out []Symbol
	at := func(kind Kind, name, detail string, whole, named syntax.Node) {
		out = append(out, Symbol{Kind: kind, Name: utf8Text(name), Detail: utf8Text(detail),
			Start: t.Off(whole), End: t.End(whole), NameStart: t.Off(named), NameEnd: t.End(named)})
	}
	function := func(fn *syntax.FuncDecl) {
		detail := t.DeclPrefix(fn.Specs, fn.Type) + strings.Repeat("*", fn.Stars) + "(" + t.SourceText(fn.LParen+1, fn.RParen-1) + ")"
		switch {
		case fn.Name != nil:
			at(Function, fn.Name.Name, detail, fn, fn.Name)
		case fn.Operator && fn.LParen > fn.NameTok:
			// "operator+", "operator[]": the keyword and what it is the
			// operator of.
			op := &syntax.BasicLit{}
			op.First, op.Last = fn.NameTok, fn.NameTok
			name := "operator"
			for i := fn.NameTok + 1; i < fn.LParen; i++ {
				if tok := t.Tok.Tokens[i]; !tok.Kind.IsTrivia() && tok.Kind != token.Directive {
					name += string(t.Tok.Text(tok))
					op.Last = i
				}
			}
			at(Function, name, detail, fn, op)
		}
	}
	for _, d := range t.Decls {
		switch d := d.(type) {
		case *syntax.InheritDecl:
			if d.Path == nil {
				continue
			}
			name := t.Text(d.Path)
			if r.u != nil {
				for _, in := range r.u.Inherits {
					if in.Off == t.Off(d.Path) && in.Resolved {
						name = in.Path
					}
				}
			}
			detail := "inherit"
			if d.Private {
				detail = "private inherit"
			}
			if d.Label != nil {
				detail += " " + d.Label.Name
			}
			at(Object, name, detail, d, d.Path)
		case *syntax.FuncDecl:
			function(d)
		case *syntax.VarDecl:
			for _, v := range d.Vars {
				if v.Name == nil {
					continue
				}
				whole := syntax.Node(v)
				if len(d.Vars)+len(d.Protos) == 1 {
					whole = d
				}
				at(Variable, v.Name.Name, t.DeclPrefix(d.Specs, nil)+t.TypeText(d.Type, v.Stars), whole, v.Name)
			}
			for _, p := range d.Protos {
				function(p)
			}
		}
	}
	if t.Sel != nil {
		for _, ti := range t.Sel.Dirs {
			tok := r.Tok.Tokens[ti]
			text := r.Tok.Text(tok)
			if name, _ := pp.SplitDirective(text); name != "define" {
				continue
			}
			arg := pp.ArgOffset(text)
			words := lexer.Lex("", text[arg:], r.Tok.Dialect)
			if len(words.Tokens) == 0 || words.Tokens[0].Kind != token.Ident {
				continue
			}
			name := string(words.Text(words.Tokens[0]))
			start := tok.Off + uint32(arg)
			detail := "#define"
			if r.Env != nil && r.Env.Own() != nil {
				if m := r.Env.Own().Macros[name]; m != nil && m.NameOff == start && m.FuncLike {
					detail += " (" + strings.Join(m.Params, ", ") + ")"
				}
			}
			out = append(out, Symbol{Kind: Macro, Name: name, Detail: detail,
				Start: tok.Off, End: tok.End, NameStart: start, NameEnd: start + uint32(len(name))})
		}
	}
	slices.SortStableFunc(out, func(a, b Symbol) int { return cmp.Compare(a.Start, b.Start) })
	return out
}
