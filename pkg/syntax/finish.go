package syntax

import (
	"sort"

	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// finish post-processes the parse: lexer errors become syntax errors,
// errors are ordered, cascades after a severe error are dropped, and each
// error is checked for macro taint.
func (p *parser) finish() {
	f := p.file
	errs := p.errs

	// Lexer errors on active tokens.
	firstLex := -1
	if len(p.f.Errs) > 0 {
		active := map[uint32]int{}
		for _, i := range p.sig {
			if p.toks[i].Kind == token.Illegal {
				active[p.toks[i].Off] = i
			}
		}
		for _, le := range p.f.Errs {
			i, ok := active[le.Off]
			if !ok {
				continue
			}
			if firstLex < 0 || i < firstLex {
				firstLex = i
			}
			errs = append(errs, &Error{
				Kind: ErrLex, Msg: le.Msg, Tok: i, Related: -1,
				RegionFirst: i, RegionLast: i,
				Tainted: p.file.Sel.Guessed(i) || p.inMacroArgs(i),
			})
		}
	}

	sort.SliceStable(errs, func(a, b int) bool { return errs[a].Tok < errs[b].Tok })

	var declared map[string]bool
	out := errs[:0]
	for _, e := range errs {
		if firstLex >= 0 && e.Kind != ErrLex && e.Tok > firstLex {
			continue // downstream of a lexical error
		}
		if e.Kind != ErrLex {
			if p.partial && declared == nil {
				declared = p.declaredNames()
			}
			e.Tainted = p.tainted(e, declared)
		}
		out = append(out, e)
		if e.Severe {
			break
		}
	}
	f.Errors = out
}

func (p *parser) declaredNames() map[string]bool {
	names := map[string]bool{}
	Inspect(p.file, func(n Node) bool {
		switch n := n.(type) {
		case *FuncDecl:
			if n.Name != nil {
				names[n.Name.Name] = true
			}
		case *Param:
			if n.Name != nil {
				names[n.Name.Name] = true
			}
		case *Declarator:
			if n.Name != nil {
				names[n.Name.Name] = true
			}
		}
		return true
	})
	return names
}

// tainted reports whether e might be an artifact of a macro the parser
// cannot see through.
func (p *parser) tainted(e *Error, declared map[string]bool) bool {
	lo, hi := e.RegionFirst, e.RegionLast
	if lo > e.Tok {
		lo = e.Tok
	}
	if hi < e.Tok {
		hi = e.Tok
	}
	if hi >= len(p.toks) {
		hi = len(p.toks) - 1
	}
	if p.resumes[e.RegionFirst] {
		return true // first construct after a skip: consequence or macro
	}
	if p.file.Sel.Guessed(e.Tok) {
		return true // in a conditional branch we could not decide
	}
	for _, t := range []int{e.Tok, p.prevSig(e.Tok)} {
		// A keyword that is also a macro name: the preprocessor wins.
		if t >= 0 && p.toks[t].Kind.IsKeyword() && p.env.Lookup(p.text(t)) != nil {
			return true
		}
	}
	var prev, cur int
	if e.AtPrevEnd {
		prev, cur = e.Tok, p.nextSig(e.Tok)
	} else {
		prev, cur = p.prevSig(e.Tok), e.Tok
	}
	for i := lo; i <= hi; i++ {
		t := p.toks[i]
		switch t.Kind {
		case token.Directive:
			switch name, _ := pp.SplitDirective(p.f.Text(t)); name {
			case "if", "ifdef", "ifndef", "else", "elif", "endif", "include":
				return true // conditional or included code inside the region
			}
		case token.Ident:
			if m := p.env.Lookup(p.text(i)); m != nil && m.Class == pp.ClassFragment {
				return true
			}
		}
	}
	if !p.partial {
		return false
	}
	// With a partial view an unseen macro can only explain the error if
	// it sits where the parse failed: the statement head, either side of
	// the failure point, or the callee of the call that ends there.
	unseen := func(tok int) bool {
		if tok < 0 || tok >= len(p.toks) || p.toks[tok].Kind != token.Ident {
			return false
		}
		name := p.text(tok)
		return isAllCaps(name) && !declared[name] && p.env.Lookup(name) == nil
	}
	if unseen(lo) || unseen(prev) || unseen(cur) || unseen(p.calleeOf(prev)) {
		return true
	}
	// Adjacent identifiers where the first is unknown: `status foo`,
	// `public nomask`, an unseen type or specifier macro.
	if prev < 0 || cur < 0 || p.toks[prev].Kind != token.Ident {
		return false
	}
	name := p.text(prev)
	if declared[name] || p.env.Lookup(name) != nil {
		return false
	}
	k := p.toks[cur].Kind
	return k == token.Ident || k.IsSpecifier() || k.IsType()
}

// inMacroArgs reports whether tok lies inside the argument list of a
// function-like macro (or, in a partial view, of an unseen ALL_CAPS
// name). Macro arguments are raw text to the preprocessor: `TR(don't)`
// is fine if TR stringizes its argument.
func (p *parser) inMacroArgs(tok int) bool {
	j := sort.SearchInts(p.sig, tok) - 1
	depth := 0
	for ; j > 0; j-- {
		switch p.toks[p.sig[j]].Kind {
		case token.RParen:
			depth++
		case token.LParen:
			if depth > 0 {
				depth--
				continue
			}
			callee := p.sig[j-1]
			if p.toks[callee].Kind != token.Ident {
				continue
			}
			name := p.text(callee)
			if m := p.env.Lookup(name); m != nil {
				if m.FuncLike {
					return true
				}
			} else if p.partial && isAllCaps(name) {
				return true
			}
		case token.Semicolon, token.LBrace, token.RBrace:
			return false
		}
	}
	return false
}

// calleeOf returns the identifier naming the call whose ')' is tok, or -1.
func (p *parser) calleeOf(tok int) int {
	if tok < 0 || p.toks[tok].Kind != token.RParen {
		return -1
	}
	j := sort.SearchInts(p.sig, tok)
	depth := 0
	for ; j >= 0; j-- {
		switch p.toks[p.sig[j]].Kind {
		case token.RParen:
			depth++
		case token.LParen:
			depth--
			if depth == 0 {
				if j > 0 {
					return p.sig[j-1]
				}
				return -1
			}
		}
	}
	return -1
}

func (p *parser) prevSig(tok int) int {
	j := sort.SearchInts(p.sig, tok)
	if j > 0 {
		return p.sig[j-1]
	}
	return -1
}

func (p *parser) nextSig(tok int) int {
	j := sort.SearchInts(p.sig, tok+1)
	if j < len(p.sig) {
		return p.sig[j]
	}
	return -1
}
