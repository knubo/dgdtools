// Package syntax is the DGD LPC syntax tree and its error-tolerant parser.
//
// The parser works on unpreprocessed source: it is given the lossless
// token stream and a view of the macros visible in the file, and treats a
// macro name as a token of the macro's class (type, specifier, statement,
// expression, ...). It never fails: syntax errors are recorded as data and
// a tree is always produced.
//
// Every node records the first and last token it covers as indexes into
// token.File.Tokens, so the tree addresses source bytes exactly and any
// consumer can get back to the original text, comments included.
package syntax

import (
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// Node is any syntax tree node.
type Node interface {
	// Span returns the indexes of the first and last token of the node.
	Span() (first, last int)
}

type span struct{ First, Last int }

func (s span) Span() (int, int) { return s.First, s.Last }

// Expr, Stmt and Decl are the three node families.
type (
	Expr interface {
		Node
		expr()
	}
	Stmt interface {
		Node
		stmt()
	}
	Decl interface {
		Node
		decl()
	}
)

// File is one parsed source file.
type File struct {
	Tok   *token.File
	Sel   *pp.Selection
	Decls []Decl
	// Errors are the syntax errors, in source order, after cascade
	// suppression. Tainted errors (possibly caused by a macro the parser
	// could not see through) are included and flagged.
	Errors []*Error
	// Partial is true when some top-level region could not be understood:
	// the declarations found are real, but there may be more.
	Partial bool
}

// ErrorKind classifies a syntax error.
type ErrorKind uint8

const (
	ErrSyntax ErrorKind = iota
	ErrLex              // reported by the lexer
)

// Error is one syntax error.
type Error struct {
	Kind ErrorKind
	Msg  string
	// Tok is the token the error is about. When AtPrevEnd is set the
	// error is positioned just after the previous significant token
	// (where the missing token belongs) and Tok is that previous token.
	Tok       int
	AtPrevEnd bool
	// Related optionally points at another token (the unclosed bracket,
	// the enclosing function).
	Related    int // -1 when absent
	RelatedMsg string
	// RegionFirst/RegionLast delimit the statement or declaration that
	// failed, for macro-taint analysis.
	RegionFirst, RegionLast int
	// Severe marks errors after which the rest of the file's structure is
	// in doubt; later errors in the file are dropped.
	Severe bool
	// Tainted marks an error that may be an artifact of a macro whose
	// expansion the parser cannot see. Tainted errors are not reported.
	Tainted bool
}

// ---------------------------------------------------------------- decls

// Spec is one class specifier: a keyword token or a specifier-macro name.
type Spec struct {
	Tok  int
	Kind token.Kind // KwPrivate/KwStatic/...; Ident for a macro
}

// TypeSpec is a type: keyword or type macro, optional object path.
type TypeSpec struct {
	span
	Kind  token.Kind // type keyword kind; Ident for a type macro
	Path  Expr       // object "path", optional
	Stars int        // from the declarator; filled per declarator
}

// InheritDecl: [private] inherit [label] [object] path ;
type InheritDecl struct {
	span
	Private bool
	Label   *Ident
	Path    Expr
}

// Param is one formal parameter.
type Param struct {
	span
	Varargs bool
	Type    *TypeSpec // nil for typeless
	Stars   int
	Name    *Ident
}

// FuncDecl is a function definition (Body != nil) or prototype.
type FuncDecl struct {
	span
	Specs    []Spec
	Type     *TypeSpec // nil for typeless (K&R) functions
	Stars    int
	Name     *Ident // nil for operator functions
	Operator bool
	NameTok  int
	LParen   int
	RParen   int
	Params   []*Param
	Ellipsis bool
	Body     *BlockStmt
}

// Has reports whether the function carries the specifier keyword k.
func (d *FuncDecl) Has(k token.Kind) bool { return hasSpec(d.Specs, k) }

func hasSpec(specs []Spec, k token.Kind) bool {
	for _, s := range specs {
		if s.Kind == k {
			return true
		}
	}
	return false
}

// Declarator is one declared variable.
type Declarator struct {
	span
	Stars int
	Name  *Ident
	Init  Expr // legacy initializer; nil normally
}

// VarDecl declares variables (globals at top level, locals in a block).
// Function prototypes mixed into the declarator list become Protos.
type VarDecl struct {
	span
	Specs  []Spec
	Type   *TypeSpec
	Vars   []*Declarator
	Protos []*FuncDecl
}

func (d *VarDecl) Has(k token.Kind) bool { return hasSpec(d.Specs, k) }

// BadDecl is a top-level region that could not be parsed (an Error
// describes it), or one made opaque by a macro.
type BadDecl struct {
	span
	Opaque bool // macro-induced; no error was recorded
}

func (*InheritDecl) decl() {}
func (*FuncDecl) decl()    {}
func (*VarDecl) decl()     {}
func (*BadDecl) decl()     {}

// ---------------------------------------------------------------- stmts

type (
	BlockStmt struct {
		span
		Stmts []Stmt
	}
	ExprStmt struct {
		span
		X Expr
	}
	DeclStmt struct {
		span
		Decl *VarDecl
	}
	EmptyStmt struct{ span }
	IfStmt    struct {
		span
		Cond Expr
		Then Stmt
		Else Stmt
	}
	WhileStmt struct {
		span
		Cond Expr
		Body Stmt
	}
	DoStmt struct {
		span
		Body Stmt
		Cond Expr
	}
	ForStmt struct {
		span
		Init, Cond, Post Expr
		Body             Stmt
	}
	SwitchStmt struct {
		span
		Tag  Expr
		Body Stmt
	}
	// CaseStmt is `case lo[..hi]:` or `default:` followed by its statement.
	CaseStmt struct {
		span
		Default bool
		Lo, Hi  Expr
		Body    Stmt // nil when the label directly precedes '}'
	}
	LabelStmt struct {
		span
		Name *Ident
		Body Stmt
	}
	GotoStmt struct {
		span
		Label *Ident
	}
	BreakStmt    struct{ span }
	ContinueStmt struct{ span }
	ReturnStmt   struct {
		span
		X Expr
	}
	RlimitsStmt struct {
		span
		Stack, Ticks Expr
		Body         Stmt
	}
	// CatchStmt is `catch { ... } [: stmt]` or `try { ... } catch (e) { ... }`.
	CatchStmt struct {
		span
		Body    Stmt
		Handler Stmt
	}
	// MacroStmt is a use of a statement-class or empty macro standing as
	// a statement, with or without ';'.
	MacroStmt struct {
		span
		Name *Ident
		Args []Expr // nil when the arguments are not expressions
	}
	// BadStmt is a statement that failed to parse (an Error describes
	// it) or that was made opaque by a fragment macro.
	BadStmt struct {
		span
		Opaque bool
	}
)

func (*BlockStmt) stmt()    {}
func (*ExprStmt) stmt()     {}
func (*DeclStmt) stmt()     {}
func (*EmptyStmt) stmt()    {}
func (*IfStmt) stmt()       {}
func (*WhileStmt) stmt()    {}
func (*DoStmt) stmt()       {}
func (*ForStmt) stmt()      {}
func (*SwitchStmt) stmt()   {}
func (*CaseStmt) stmt()     {}
func (*LabelStmt) stmt()    {}
func (*GotoStmt) stmt()     {}
func (*BreakStmt) stmt()    {}
func (*ContinueStmt) stmt() {}
func (*ReturnStmt) stmt()   {}
func (*RlimitsStmt) stmt()  {}
func (*CatchStmt) stmt()    {}
func (*MacroStmt) stmt()    {}
func (*BadStmt) stmt()      {}

// ---------------------------------------------------------------- exprs

type (
	Ident struct {
		span
		Name string
	}
	// BasicLit is an int, float, string or char literal, or nil.
	BasicLit struct {
		span
		Kind token.Kind
	}
	// AdjacentStrings is the pre-DGD implicit concatenation "a" "b".
	AdjacentStrings struct {
		span
		Parts []Expr
	}
	ArrayLit struct {
		span
		Elems []Expr
	}
	MappingLit struct {
		span
		Keys, Values []Expr
	}
	// LegacyClosure is the pre-DGD `(: a, b :)` literal.
	LegacyClosure struct {
		span
		Elems []Expr
	}
	ParenExpr struct {
		span
		X Expr
	}
	// CallExpr is name(args), ::name(args) or label::name(args).
	CallExpr struct {
		span
		Scoped bool   // has '::'
		Label  *Ident // label::name
		Fun    *Ident
		LParen int
		Args   []Expr
		Spread bool // last argument followed by '...'
		// Opaque is set when the callee is a macro whose arguments are
		// not expressions; Args is then nil.
		Opaque bool
	}
	// ArrowExpr is x->name(args).
	ArrowExpr struct {
		span
		X      Expr
		Name   *Ident
		Args   []Expr
		Spread bool
	}
	IndexExpr struct {
		span
		X, Index Expr
	}
	RangeExpr struct {
		span
		X, Lo, Hi Expr // Lo/Hi may be nil
	}
	UnaryExpr struct {
		span
		Op token.Kind
		X  Expr
	}
	PostfixExpr struct {
		span
		Op token.Kind
		X  Expr
	}
	BinaryExpr struct {
		span
		Op    token.Kind
		OpTok int
		X, Y  Expr
	}
	CondExpr struct {
		span
		Cond, Then, Else Expr
	}
	AssignExpr struct {
		span
		Op    token.Kind
		OpTok int
		L, R  Expr
	}
	CastExpr struct {
		span
		Type *TypeSpec
		X    Expr
	}
	// CommaExpr is a list expression a, b, c.
	CommaExpr struct {
		span
		List []Expr
	}
	// CatchExpr is catch(expr).
	CatchExpr struct {
		span
		X Expr
	}
	// NewExpr is new [object] path [(args)].
	NewExpr struct {
		span
		Path Expr
		Args []Expr
	}
	// InstanceOfExpr is x <- [object] path.
	InstanceOfExpr struct {
		span
		X, Path Expr
	}
	// FuncPtrExpr is &name(args), &(*f)(args) or (*f)(args).
	FuncPtrExpr struct {
		span
		Fun  Expr
		Args []Expr
	}
	// BadExpr stands in where an expression was expected.
	BadExpr struct{ span }
)

func (*Ident) expr()           {}
func (*BasicLit) expr()        {}
func (*AdjacentStrings) expr() {}
func (*ArrayLit) expr()        {}
func (*MappingLit) expr()      {}
func (*LegacyClosure) expr()   {}
func (*ParenExpr) expr()       {}
func (*CallExpr) expr()        {}
func (*ArrowExpr) expr()       {}
func (*IndexExpr) expr()       {}
func (*RangeExpr) expr()       {}
func (*UnaryExpr) expr()       {}
func (*PostfixExpr) expr()     {}
func (*BinaryExpr) expr()      {}
func (*CondExpr) expr()        {}
func (*AssignExpr) expr()      {}
func (*CastExpr) expr()        {}
func (*CommaExpr) expr()       {}
func (*CatchExpr) expr()       {}
func (*NewExpr) expr()         {}
func (*InstanceOfExpr) expr()  {}
func (*FuncPtrExpr) expr()     {}
func (*BadExpr) expr()         {}

// Unparen strips any enclosing parentheses.
func Unparen(e Expr) Expr {
	for {
		p, ok := e.(*ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// Text returns the source text of a node.
func (f *File) Text(n Node) string {
	a, b := n.Span()
	if a < 0 || b < a || b >= len(f.Tok.Tokens) {
		return ""
	}
	return string(f.Tok.Src[f.Tok.Tokens[a].Off:f.Tok.Tokens[b].End])
}

// Off returns the byte offset of a node's first token.
func (f *File) Off(n Node) uint32 {
	a, _ := n.Span()
	return f.Tok.Tokens[a].Off
}

// End returns the byte offset just past a node's last token.
func (f *File) End(n Node) uint32 {
	_, b := n.Span()
	return f.Tok.Tokens[b].End
}

// Funcs iterates every function definition and prototype, including
// prototypes declared inside variable declarator lists.
func (f *File) Funcs(yield func(*FuncDecl) bool) {
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *FuncDecl:
			if !yield(d) {
				return
			}
		case *VarDecl:
			for _, p := range d.Protos {
				if !yield(p) {
					return
				}
			}
		}
	}
}
