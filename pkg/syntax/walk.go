package syntax

import "slices"

// Inspect walks the tree rooted at n in source order, calling fn for each
// node. If fn returns false the node's children are skipped.
func Inspect(n Node, fn func(Node) bool) {
	if n == nil || !fn(n) {
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

// PathAt returns the nodes whose tokens include the one at index tok of
// the file's tokens (token.File.TokenAt), the innermost first and the
// file last: the way Inspect goes down to the token. A name that is part
// of a node and no node of its own (the callee of a CallExpr, the name of
// a FuncDecl or of an ArrowExpr, a label) is found in the innermost node,
// by its own span. A token that is not in the tree (a comment, a
// directive, code in a branch that is not compiled, what the parser
// skipped) has the nodes around it, the file at least.
func PathAt(f *File, tok int) []Node {
	path := []Node{f}
	for at := Node(f); at != nil; {
		parent := at
		at = nil
		// Of two children that both have the token (a statement that is
		// missing has the span of what comes next) the later is the one.
		Inspect(parent, func(n Node) bool {
			if n == parent {
				return true
			}
			if first, last := n.Span(); first <= tok && tok <= last {
				at = n
			}
			return false
		})
		if at != nil {
			path = append(path, at)
		}
	}
	slices.Reverse(path)
	return path
}

// PathsAt calls visit with PathAt(f, tok) for each of toks, which are in
// increasing order, walking the tree once instead of once for each
// token: what a question about every name of a file asks. The path is
// visit's to read during the call only.
func PathsAt(f *File, toks []int, visit func(tok int, path []Node)) {
	var stack, buf []Node
	var walk func(n Node, toks []int)
	walk = func(n Node, toks []int) {
		stack = append(stack, n)
		defer func() { stack = stack[:len(stack)-1] }()
		var kids []Node
		Inspect(n, func(c Node) bool {
			if c != n {
				kids = append(kids, c)
			}
			return c == n
		})
		// The later of two children that have a token is the one, as in
		// PathAt.
		owner := make([]int, len(toks))
		for i := range owner {
			owner[i] = -1
		}
		for k, c := range kids {
			first, last := c.Span()
			i, _ := slices.BinarySearch(toks, first)
			for ; i < len(toks) && toks[i] <= last; i++ {
				owner[i] = k
			}
		}
		for i, tok := range toks {
			if owner[i] >= 0 {
				continue
			}
			buf = append(buf[:0], stack...)
			slices.Reverse(buf)
			visit(tok, buf)
		}
		for k, c := range kids {
			var mine []int
			for i, tok := range toks {
				if owner[i] == k {
					mine = append(mine, tok)
				}
			}
			if len(mine) > 0 {
				walk(c, mine)
			}
		}
	}
	walk(f, toks)
}
