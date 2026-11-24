package syntax

import (
	"sort"

	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// Inspect walks the tree rooted at n in source order, calling fn for each
// node. If fn returns false the node's children are skipped.
func Inspect(n Node, fn func(Node) bool) {
	if isNil(n) || !fn(n) {
		return
	}
	switch n := n.(type) {
	case *File:
		for _, d := range n.Decls {
			Inspect(d, fn)
		}
	case *InheritDecl:
		Inspect(n.Path, fn)
	case *FuncDecl:
		for _, p := range n.Params {
			Inspect(p, fn)
		}
		if n.Body != nil {
			Inspect(n.Body, fn)
		}
	case *VarDecl:
		for _, v := range n.Vars {
			Inspect(v, fn)
		}
		for _, p := range n.Protos {
			Inspect(p, fn)
		}
	case *Declarator:
		Inspect(n.Init, fn)
	case *BlockStmt:
		for _, s := range n.Stmts {
			Inspect(s, fn)
		}
	case *ExprStmt:
		Inspect(n.X, fn)
	case *DeclStmt:
		Inspect(n.Decl, fn)
	case *IfStmt:
		Inspect(n.Cond, fn)
		Inspect(n.Then, fn)
		Inspect(n.Else, fn)
	case *WhileStmt:
		Inspect(n.Cond, fn)
		Inspect(n.Body, fn)
	case *DoStmt:
		Inspect(n.Body, fn)
		Inspect(n.Cond, fn)
	case *ForStmt:
		Inspect(n.Init, fn)
		Inspect(n.Cond, fn)
		Inspect(n.Post, fn)
		Inspect(n.Body, fn)
	case *SwitchStmt:
		Inspect(n.Tag, fn)
		Inspect(n.Body, fn)
	case *CaseStmt:
		Inspect(n.Lo, fn)
		Inspect(n.Hi, fn)
		Inspect(n.Body, fn)
	case *LabelStmt:
		Inspect(n.Body, fn)
	case *ReturnStmt:
		Inspect(n.X, fn)
	case *RlimitsStmt:
		Inspect(n.Stack, fn)
		Inspect(n.Ticks, fn)
		Inspect(n.Body, fn)
	case *CatchStmt:
		Inspect(n.Body, fn)
		Inspect(n.Handler, fn)
	case *MacroStmt:
		for _, a := range n.Args {
			Inspect(a, fn)
		}
	case *AdjacentStrings:
		for _, e := range n.Parts {
			Inspect(e, fn)
		}
	case *ArrayLit:
		for _, e := range n.Elems {
			Inspect(e, fn)
		}
	case *MappingLit:
		for i := range n.Keys {
			Inspect(n.Keys[i], fn)
			if i < len(n.Values) {
				Inspect(n.Values[i], fn)
			}
		}
	case *LegacyClosure:
		for _, e := range n.Elems {
			Inspect(e, fn)
		}
	case *ParenExpr:
		Inspect(n.X, fn)
	case *CallExpr:
		for _, a := range n.Args {
			Inspect(a, fn)
		}
	case *ArrowExpr:
		Inspect(n.X, fn)
		for _, a := range n.Args {
			Inspect(a, fn)
		}
	case *IndexExpr:
		Inspect(n.X, fn)
		Inspect(n.Index, fn)
	case *RangeExpr:
		Inspect(n.X, fn)
		Inspect(n.Lo, fn)
		Inspect(n.Hi, fn)
	case *UnaryExpr:
		Inspect(n.X, fn)
	case *PostfixExpr:
		Inspect(n.X, fn)
	case *BinaryExpr:
		Inspect(n.X, fn)
		Inspect(n.Y, fn)
	case *CondExpr:
		Inspect(n.Cond, fn)
		Inspect(n.Then, fn)
		Inspect(n.Else, fn)
	case *AssignExpr:
		Inspect(n.L, fn)
		Inspect(n.R, fn)
	case *CastExpr:
		Inspect(n.X, fn)
	case *CommaExpr:
		for _, e := range n.List {
			Inspect(e, fn)
		}
	case *CatchExpr:
		Inspect(n.X, fn)
	case *NewExpr:
		Inspect(n.Path, fn)
		for _, a := range n.Args {
			Inspect(a, fn)
		}
	case *InstanceOfExpr:
		Inspect(n.X, fn)
		Inspect(n.Path, fn)
	case *FuncPtrExpr:
		Inspect(n.Fun, fn)
		for _, a := range n.Args {
			Inspect(a, fn)
		}
	}
}

func isNil(n Node) bool { return n == nil }

func (p *Param) Span() (int, int)      { return p.First, p.Last }
func (d *Declarator) Span() (int, int) { return d.First, d.Last }
func (f *File) Span() (int, int)       { return 0, len(f.Tok.Tokens) - 1 }

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
		for _, d := range p.f.Errs {
			if d.Line < 1 || d.Line > len(p.f.Lines) {
				continue
			}
			off := p.f.Lines[d.Line-1] + uint32(d.Col-1)
			i, ok := active[off]
			if !ok {
				continue
			}
			if firstLex < 0 || i < firstLex {
				firstLex = i
			}
			errs = append(errs, &Error{
				Kind: ErrLex, Msg: d.Message, Tok: i, Related: -1,
				RegionFirst: i, RegionLast: i,
				Tainted: p.file.Sel.Guessed(i) || p.inMacroArgs(i) || p.lineSplice(i),
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
		if t >= 0 && p.toks[t].Kind.IsKeyword() && p.view.Lookup(p.text(t)) != nil {
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
			if m := p.view.Lookup(p.text(i)); m != nil && m.Class == pp.ClassFragment {
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
		return isAllCaps(name) && !declared[name] && p.view.Lookup(name) == nil
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
	if declared[name] || p.view.Lookup(name) != nil {
		return false
	}
	k := p.toks[cur].Kind
	return k == token.Ident || isSpecKw(k) || pp.IsTypeKeyword(k)
}

// lineSplice reports whether the illegal token is a backslash at the end
// of a line: a line continuation, which the preprocessor removes.
func (p *parser) lineSplice(tok int) bool {
	if p.text(tok) != "\\" || tok+1 >= len(p.toks) {
		return false
	}
	return p.toks[tok+1].Kind == token.Newline
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
			if m := p.view.Lookup(name); m != nil {
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
