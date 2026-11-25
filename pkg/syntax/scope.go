package syntax

import "github.com/mmcdole/dgdtools/pkg/token"

// The scopes of a function: which parameter or local variable a name
// means at a place in its body. The rules are DGD's (comp/compile.cpp,
// CodeBlock):
//
//   - The parameters and the variables declared in the outermost block
//     of the body are one scope.
//   - Every other block is a scope of its own, inside the one around it.
//     A variable declared in it hides one of the same name outside it,
//     and is gone at the block's end.
//   - A variable is there from its declarator on, not before: in
//     `int a, b;` a is declared before b.
//   - `try { ... } catch (e) { ... }` declares e, a string, for the
//     handler's block.
//
// DGD wants a block's declarations before its statements and refuses a
// second declaration of a name in one scope. The tree has what was
// written: a declaration is in scope from where it stands, wherever
// that is, and of two in one scope the later one counts from its place
// on.

// Local is a parameter or a local variable of a function.
type Local struct {
	Name string
	// Type is the declared type as written, with its '*'s ("string *");
	// "" for a parameter that has none. An exception is a string.
	Type string
	// Param says it is a parameter; Catch that it is the variable a
	// catch handler has the error in.
	Param, Catch bool
	// Ident is the name where it is declared.
	Ident *Ident
	// From is the byte offset from which the name means this variable:
	// the end of its name in its declarator, the start of the body for
	// a parameter, of the handler's block for an exception.
	From uint32
}

// Scope is a function's body, or a block in it, with the variables
// declared directly in it.
type Scope struct {
	// Parent is the scope around this one; nil for the function's own.
	Parent *Scope
	// Func is the function the scope is in.
	Func *FuncDecl
	// Start and End are the bytes the scope reaches over, both
	// included: from behind the block's '{' to its '}', or, of a block
	// that is not closed, to where the next thing begins.
	Start, End uint32
	// Locals are the variables declared in the scope, in the order of
	// their declarations; of the function's scope the parameters first.
	Locals []*Local
	// Blocks are the scopes of the blocks directly inside.
	Blocks []*Scope
	// Unread are the bytes [start, end) of the statements directly in
	// the scope that could not be parsed: one of them may be a
	// declaration, of any name it has.
	Unread [][2]uint32
}

// Scopes returns the scope of each function of the file that has a
// body, in the order of the file.
func (f *File) Scopes() []*Scope {
	var out []*Scope
	for _, d := range f.Decls {
		if fn, ok := d.(*FuncDecl); ok && fn.Body != nil {
			out = append(out, f.Scope(fn))
		}
	}
	return out
}

// Scope returns the scope of a function that has a body: its parameters
// and the variables of its outermost block, and inside it the scopes of
// its blocks.
func (f *File) Scope(fn *FuncDecl) *Scope {
	b := &scoper{f: f, fn: fn}
	s := b.open(nil, fn.Body)
	for _, p := range fn.Params {
		if p.Name != nil {
			s.Locals = append(s.Locals, &Local{Name: p.Name.Name, Type: f.TypeText(p.Type, p.Stars), Param: true, Ident: p.Name, From: s.Start})
		}
	}
	for _, st := range fn.Body.Stmts {
		b.stmt(s, st)
	}
	return s
}

type scoper struct {
	f  *File
	fn *FuncDecl
}

// open returns the scope of a block.
func (b *scoper) open(parent *Scope, block *BlockStmt) *Scope {
	toks := b.f.Tok.Tokens
	s := &Scope{Parent: parent, Func: b.fn, Start: toks[block.First].End, End: toks[block.Last].Off}
	if block.Unclosed {
		// To where the next thing begins, or the file ends.
		next := block.Last + 1
		for next < len(toks) && (toks[next].Kind.IsTrivia() || toks[next].Kind == token.Directive) {
			next++
		}
		s.End = uint32(len(b.f.Tok.Src))
		if next < len(toks) && toks[next].Kind != token.EOF {
			s.End = toks[next].Off - 1
		}
	}
	s.End = max(s.End, s.Start)
	if parent != nil {
		parent.Blocks = append(parent.Blocks, s)
	}
	return s
}

// block reads a block that is a scope of its own inside s.
func (b *scoper) block(s *Scope, block *BlockStmt) *Scope {
	in := b.open(s, block)
	for _, st := range block.Stmts {
		b.stmt(in, st)
	}
	return in
}

// stmt reads a statement that stands in scope s.
func (b *scoper) stmt(s *Scope, st Stmt) {
	toks := b.f.Tok.Tokens
	switch n := st.(type) {
	case *BlockStmt:
		b.block(s, n)
	case *DeclStmt:
		b.decl(s, n)
	case *IfStmt:
		b.stmt(s, n.Then)
		b.stmt(s, n.Else)
	case *WhileStmt:
		b.stmt(s, n.Body)
	case *DoStmt:
		b.stmt(s, n.Body)
	case *ForStmt:
		b.stmt(s, n.Body)
	case *SwitchStmt:
		b.stmt(s, n.Body)
	case *CaseStmt:
		b.stmt(s, n.Body)
	case *LabelStmt:
		b.stmt(s, n.Body)
	case *RlimitsStmt:
		b.stmt(s, n.Body)
	case *CatchStmt:
		b.stmt(s, n.Body)
		if block, ok := n.Handler.(*BlockStmt); ok && n.Exc != nil {
			in := b.block(s, block)
			in.Locals = append([]*Local{{Name: n.Exc.Name, Type: "string", Catch: true, Ident: n.Exc, From: in.Start}}, in.Locals...)
		} else {
			b.stmt(s, n.Handler)
		}
	case *BadStmt:
		// The statement after one that was skipped is no evidence of
		// anything (BadStmt.Resumed), but a declaration is one wherever
		// the skip ended: nothing else begins with a type and a name.
		if d, ok := n.Resumed.(*DeclStmt); ok {
			b.decl(s, d)
		} else if !n.Missing {
			s.Unread = append(s.Unread, [2]uint32{toks[n.First].Off, toks[n.Last].End})
		}
	}
}

// decl adds the variables a declaration declares to scope s.
func (b *scoper) decl(s *Scope, n *DeclStmt) {
	if n.Decl == nil {
		return
	}
	for _, v := range n.Decl.Vars {
		if v.Name != nil {
			s.Locals = append(s.Locals, &Local{Name: v.Name.Name, Type: b.f.TypeText(n.Decl.Type, v.Stars), Ident: v.Name,
				From: b.f.Tok.Tokens[v.Name.Last].End})
		}
	}
}

// At returns the innermost scope that reaches over byte offset off: s
// itself, a block inside it, or nil when off is outside s.
func (s *Scope) At(off uint32) *Scope {
	if s == nil || off < s.Start || off > s.End {
		return nil
	}
	for _, in := range s.Blocks {
		if at := in.At(off); at != nil {
			return at
		}
	}
	return s
}

// Lookup returns the parameter or local variable that name means at
// byte offset off, which is in s: the one declared last before off in
// the innermost scope that has one. It is nil when the name is none
// there.
func (s *Scope) Lookup(name string, off uint32) *Local {
	for at := s.At(off); at != nil; at = at.Parent {
		for i := len(at.Locals) - 1; i >= 0; i-- {
			if l := at.Locals[i]; l.Name == name && l.From <= off {
				return l
			}
		}
	}
	return nil
}

// Visible returns the parameters and local variables that can be named
// at byte offset off, the innermost and latest first; one that another
// of its name hides is not among them.
func (s *Scope) Visible(off uint32) []*Local {
	var out []*Local
	seen := map[string]bool{}
	for at := s.At(off); at != nil; at = at.Parent {
		for i := len(at.Locals) - 1; i >= 0; i-- {
			if l := at.Locals[i]; l.From <= off && !seen[l.Name] {
				seen[l.Name] = true
				out = append(out, l)
			}
		}
	}
	return out
}

// Unsure reports whether a statement that could not be parsed stands
// before byte offset off in a scope around it: what it declares, if it
// is a declaration, would be in scope at off. Each such statement is
// given to mentions, which says whether it has the name asked about.
func (s *Scope) Unsure(off uint32, mentions func(start, end uint32) bool) bool {
	for at := s.At(off); at != nil; at = at.Parent {
		for _, u := range at.Unread {
			if u[0] < off && mentions(u[0], u[1]) {
				return true
			}
		}
	}
	return false
}
