package format

import (
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/syntax"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// flag is one thing the plan knows about a token.
type flag uint16

const (
	// known: the token lies in code the parser understood, and the flags
	// below are the syntax tree's word on it. Where it is unset the
	// printer goes by the neighbouring tokens instead.
	known flag = 1 << iota
	// head: the token begins a statement or declaration, or is one of its
	// structural parts (an else, a block's brace, the name of a function
	// being defined). A line that starts with it is indented at the
	// token's level; a line that starts with anything else continues
	// what came before.
	head
	binary      // '+' '-' '*' '&' between two operands
	postfix     // '++' or '--' after its operand
	ternary     // the '?' or ':' of a conditional expression
	literal     // a brace of ({ }) or a bracket of ([ ])
	block       // a brace of a block statement or function body
	caseLabel   // 'case' or 'default' in the block of its switch
	cuddle      // '{' or 'else' that joins the line before it when it stands alone
	join        // stays on the line of the token before it
	breakBefore // starts a line
	breakAfter  // ends its line
)

// info is the plan for one token.
type info struct {
	// level is the indentation level of the statement or declaration the
	// token belongs to. For a token that is not known it is the level of
	// the region the parser gave up on.
	level int16
	flags flag
}

// plan is what the syntax tree says about the tokens of a file, indexed
// by token.
type plan struct {
	info []info
	// conds marks the conditional directives of the file: the printer
	// lays out each branch from the state the conditional began with.
	conds map[int]condMark
}

func (pl *plan) has(i int, fl flag) bool { return pl.info[i].flags&fl != 0 }

// condMark says what a conditional directive does to the code around it.
type condMark struct {
	kind condKind
	// taken says the branch that ends at this directive is the one the
	// preprocessor model selected: the code after the conditional
	// continues from it.
	taken bool
}

type condKind uint8

const (
	condOpen condKind = iota + 1 // #if, #ifdef, #ifndef
	condNext                     // #elif, #else
	condEnd                      // #endif
)

// maxRounds bounds how many times a file is read to reach every branch of
// its conditionals.
const maxRounds = 32

// buildPlan reads f and plans its layout.
//
// The file is read as the preprocessor model selects it, and then again
// with other branches of its conditionals selected, until every token has
// been read in some branch: an #else is laid out by the same rules as the
// code above it. A token is planned by the first round that reads it.
func buildPlan(f *token.File, o Options) *plan {
	b := &builder{f: f, o: o, info: make([]info, len(f.Tokens)), owner: make([]uint8, len(f.Tokens))}
	env := pp.NewEnv(o.Defined, o.Macros)
	sel := env.Scan(f, nil)
	b.read(env, sel)
	for b.round < maxRounds {
		take := b.unread(sel)
		if take == nil {
			break
		}
		env := pp.NewEnv(o.Defined, o.Macros)
		b.read(env, env.ScanArms(f, take))
	}
	return &plan{info: b.info, conds: condMarks(f, sel)}
}

func condMarks(f *token.File, sel *pp.Selection) map[int]condMark {
	if len(sel.Conds) == 0 {
		return nil
	}
	m := make(map[int]condMark, 3*len(sel.Conds))
	for _, c := range sel.Conds {
		for j, a := range c.Arms {
			if j == 0 {
				m[a.Dir] = condMark{kind: condOpen}
			} else {
				m[a.Dir] = condMark{kind: condNext, taken: c.Arms[j-1].Taken}
			}
		}
		if f.Tokens[c.End].Kind == token.Directive {
			m[c.End] = condMark{kind: condEnd, taken: c.Arms[len(c.Arms)-1].Taken}
		}
	}
	return m
}

type builder struct {
	f    *token.File
	o    Options
	env  *pp.Env // of the round being read
	info []info
	// owner is the round that plans each token: the first whose selection
	// has it. 0 is a token no round has read yet.
	owner []uint8
	round uint8
}

// read plans the tokens that sel selects and no earlier round has read.
func (b *builder) read(env *pp.Env, sel *pp.Selection) {
	b.round++
	b.env = env
	for _, i := range sel.Sig {
		if b.owner[i] == 0 {
			b.owner[i] = b.round
		}
	}
	tree := syntax.Parse(b.f, syntax.Options{Env: env, Sel: sel})
	for _, d := range tree.Decls {
		b.decl(d)
	}
	syntax.Inspect(tree, b.role)
}

// unread chooses, for the next round, a branch of each conditional that
// holds tokens no round has read; where there is none, the branch sel
// selected. It returns nil when every token has been read.
func (b *builder) unread(sel *pp.Selection) map[int]bool {
	toks := b.f.Tokens
	left := make([]int32, len(toks)+1) // left[i]: unread code tokens before token i
	for i, t := range toks {
		left[i+1] = left[i]
		if b.owner[i] == 0 && !t.Kind.IsTrivia() && t.Kind != token.Directive && t.Kind != token.EOF {
			left[i+1]++
		}
	}
	if left[len(toks)] == 0 {
		return nil
	}
	// armEnd is where arm j of c ends.
	armEnd := func(c *pp.Cond, j int) int {
		if j+1 < len(c.Arms) {
			return c.Arms[j+1].Dir
		}
		return c.End
	}
	// open are the conditionals around the one being looked at, with the
	// branch chosen for each (-1: none, or the conditional is itself in a
	// branch that was not chosen).
	type chosen struct {
		cond *pp.Cond
		arm  int
	}
	var open []chosen
	take := map[int]bool{}
	for ci := range sel.Conds {
		c := &sel.Conds[ci]
		at := c.Arms[0].Dir
		for len(open) > 0 && open[len(open)-1].cond.End < at {
			open = open[:len(open)-1]
		}
		reached := true
		if n := len(open); n > 0 {
			outer := open[n-1]
			reached = outer.arm >= 0 && outer.cond.Arms[outer.arm].Dir < at && at < armEnd(outer.cond, outer.arm)
		}
		pick := -1
		if reached {
			for j, a := range c.Arms {
				if a.Taken && pick < 0 {
					pick = j
				}
				if left[armEnd(c, j)]-left[a.Dir+1] > 0 {
					pick = j
					break
				}
			}
			for j, a := range c.Arms {
				take[a.Dir] = j == pick
			}
		}
		open = append(open, chosen{c, pick})
	}
	return take
}

// mine reports whether the current round plans token i.
func (b *builder) mine(i int) bool { return i >= 0 && i < len(b.owner) && b.owner[i] == b.round }

// span plans the tokens first..last at level with the given flags.
func (b *builder) span(first, last, level int, fl flag) {
	for i := max(first, 0); i <= last && i < len(b.info); i++ {
		if b.owner[i] == b.round {
			b.info[i] = info{int16(level), fl}
		}
	}
}

// mark adds flags to token i.
func (b *builder) mark(i int, fl flag) {
	if b.mine(i) {
		b.info[i].flags |= fl
	}
}

// markKind is mark for a token the tree names by position: after an
// error the position may not hold the token the node expected.
func (b *builder) markKind(i int, k token.Kind, fl flag) {
	if b.mine(i) && b.f.Tokens[i].Kind == k {
		b.info[i].flags |= fl
	}
}

// startsLine reports whether token i is the first on its line.
func (b *builder) startsLine(i int) bool {
	for i--; i >= 0; i-- {
		switch k := b.f.Tokens[i].Kind; {
		case k == token.Newline:
			return true
		case !k.IsTrivia():
			return false
		}
	}
	return true
}

// multiline reports whether a line break lies between tokens a and b.
func (b *builder) multiline(first, last int) bool {
	for i := first; i <= last && i < len(b.f.Tokens); i++ {
		if b.f.Tokens[i].Kind == token.Newline {
			return true
		}
	}
	return false
}

func (b *builder) decl(d syntax.Decl) {
	first, last := d.Span()
	switch d := d.(type) {
	case *syntax.FuncDecl:
		b.function(d)
	case *syntax.BadDecl:
		b.span(first, last, 0, 0)
	case *syntax.VarDecl:
		b.span(first, last, 0, known)
		b.variables(d)
	default:
		b.span(first, last, 0, known)
	}
	b.mark(first, head)
}

// variables plans a variable declaration: its specifiers and type stand
// at the declaration's level wherever they start a line, like a
// function's; the names that follow on further lines continue it.
func (b *builder) variables(d *syntax.VarDecl) {
	first, _ := d.Span()
	end := first
	if n := len(d.Specs); n > 0 {
		end = d.Specs[n-1].Tok
	}
	if d.Type != nil {
		end = d.Type.Last
	}
	for i := first; i <= end; i++ {
		b.mark(i, head)
	}
}

func (b *builder) function(d *syntax.FuncDecl) {
	first, last := d.Span()
	b.span(first, last, 0, known)
	// Whatever part of the header starts a line stands at the left.
	for i := first; i < d.LParen; i++ {
		b.mark(i, head)
	}
	if d.Body != nil {
		b.block(d.Body, 0, false)
	}
	b.header(d)
}

// header plans the KNF layout of a function's header and of the braces of
// its body. A header with a comment or a directive in it is left as
// written, and so is a definition written entirely on one line: that is a
// deliberate vertical decision (the accessor idiom).
func (b *builder) header(d *syntax.FuncDecl) {
	toks := b.f.Tokens
	first, _ := d.Span()
	for i := first; i <= d.RParen; i++ {
		switch toks[i].Kind {
		case token.LineComment, token.BlockComment, token.Directive:
			return
		}
	}
	body := d.Body
	if body != nil && !b.multiline(first, body.Last) {
		return
	}
	// A name the parser took for a specifier macro only because of where
	// it stands (`SETUP` on the line above `static int f()`) may be
	// anything: the header that is laid out begins after it.
	for _, spec := range d.Specs {
		if spec.Kind == token.Ident && b.env.Lookup(string(b.f.Text(toks[spec.Tok]))) == nil {
			first = spec.Tok + 1
		}
	}
	lead := false // specifiers, type or stars come before the name
	for i := first; i < d.NameTok; i++ {
		if toks[i].Kind.IsTrivia() {
			continue
		}
		if lead {
			b.mark(i, join)
		}
		lead = true
	}
	if lead {
		if body != nil {
			b.mark(d.NameTok, breakBefore) // name at column 0
		} else {
			b.mark(d.NameTok, join)
		}
	}
	if body != nil {
		b.markKind(body.First, token.LBrace, breakBefore|breakAfter)
		if body.Last > body.First && !body.Unclosed {
			b.markKind(body.Last, token.RBrace, breakBefore)
		}
	}
}

// block plans a block whose braces stand at level. inSwitch says it is the
// body of a switch, where case labels stand with the braces.
func (b *builder) block(s *syntax.BlockStmt, level int, inSwitch bool) {
	// What the parser skipped inside the block is not understood, but it
	// is inside the block.
	b.span(s.First, s.Last, level+1, 0)
	brace := info{int16(level), known | head | block}
	if b.mine(s.First) && b.f.Tokens[s.First].Kind == token.LBrace {
		b.info[s.First] = brace
	}
	if !s.Unclosed && b.mine(s.Last) && b.f.Tokens[s.Last].Kind == token.RBrace {
		b.info[s.Last] = brace
	}
	for _, c := range s.Stmts {
		if label, ok := c.(*syntax.CaseStmt); ok && inSwitch {
			b.switchCase(label, level)
		} else {
			b.stmt(c, level+1)
		}
	}
}

// switchCase plans a case label of the switch at level: the label stands
// at the switch's level, what it labels one further in.
func (b *builder) switchCase(s *syntax.CaseStmt, level int) {
	first, last := s.Span()
	b.span(first, last, level, known)
	b.mark(first, head|caseLabel)
	switch body := s.Body.(type) {
	case nil:
	case *syntax.CaseStmt:
		b.switchCase(body, level)
	default:
		b.stmt(body, level+1)
	}
}

// body plans the statement a control statement at level governs. A block
// stands at the level of its statement and its brace joins the statement's
// line. Any other statement is one level in when it has a line of its
// own, and part of the statement's line when it follows on it.
func (b *builder) body(s syntax.Stmt, level int) {
	switch s := s.(type) {
	case nil:
	case *syntax.BlockStmt:
		b.block(s, level, false)
		b.mark(s.First, cuddle)
	default:
		if first, _ := s.Span(); b.startsLine(first) {
			level++
		}
		b.stmt(s, level)
	}
}

// stmt plans a statement that stands at level.
func (b *builder) stmt(s syntax.Stmt, level int) {
	first, last := s.Span()
	switch s := s.(type) {
	case *syntax.BlockStmt:
		b.block(s, level, false)
		return
	case *syntax.BadStmt:
		if s.Missing {
			// Nothing to lay out: the token it stands at (the brace that
			// closes the block, in `if (x) }`) is planned as what it is.
			return
		}
		if s.Resumed != nil {
			// The statement after one the parser gave up on: hidden from
			// the tree, but its shape is known.
			b.stmt(s.Resumed, level)
			syntax.Inspect(s.Resumed, b.role)
			return
		}
		b.span(first, last, level, 0)
		b.mark(first, head)
		return
	}
	b.span(first, last, level, known)
	b.mark(first, head)
	switch s := s.(type) {
	case *syntax.DeclStmt:
		b.variables(s.Decl)
	case *syntax.IfStmt:
		b.body(s.Then, level)
		if s.Else == nil {
			break
		}
		b.markKind(s.ElseTok, token.KwElse, head)
		if _, braced := s.Then.(*syntax.BlockStmt); braced {
			b.markKind(s.ElseTok, token.KwElse, cuddle)
		}
		if elif, ok := s.Else.(*syntax.IfStmt); ok && !b.startsLine(elif.First) {
			b.stmt(elif, level) // else if: a chain, not a nesting
		} else {
			b.body(s.Else, level)
		}
	case *syntax.WhileStmt:
		b.body(s.Body, level)
	case *syntax.ForStmt:
		b.body(s.Body, level)
	case *syntax.RlimitsStmt:
		b.body(s.Body, level)
	case *syntax.DoStmt:
		b.body(s.Body, level)
		b.markKind(s.WhileTok, token.KwWhile, head)
	case *syntax.SwitchStmt:
		if blk, ok := s.Body.(*syntax.BlockStmt); ok {
			b.block(blk, level, true)
			b.mark(blk.First, cuddle)
		}
	case *syntax.CatchStmt:
		b.body(s.Body, level)
		if s.Handler != nil {
			b.markKind(s.HandlerTok, token.KwCatch, head)
			b.body(s.Handler, level)
		}
	case *syntax.CaseStmt: // a label outside the block of its switch
		if s.Body != nil {
			b.stmt(s.Body, level)
		}
	case *syntax.LabelStmt:
		if s.Body != nil {
			b.stmt(s.Body, level)
		}
	case *syntax.MacroStmt:
		if s.Args == nil { // arguments that are not expressions
			b.span(s.Name.Last+1, last, level, 0)
		}
	}
}

// role records what the tree says about the tokens of an expression.
func (b *builder) role(n syntax.Node) bool {
	switch n := n.(type) {
	case *syntax.BinaryExpr:
		b.mark(n.OpTok, binary)
	case *syntax.PostfixExpr:
		b.mark(n.Last, postfix)
	case *syntax.CondExpr:
		b.markKind(n.Question, token.Question, ternary)
		b.markKind(n.Colon, token.Colon, ternary)
	case *syntax.ArrayLit:
		b.markKind(n.LBrace, token.LBrace, literal)
		b.markKind(n.RBrace, token.RBrace, literal)
	case *syntax.MappingLit:
		b.markKind(n.LBracket, token.LBracket, literal)
		b.markKind(n.RBracket, token.RBracket, literal)
	case *syntax.CallExpr:
		if n.Opaque { // a macro's arguments that are not expressions
			for i := n.LParen; i <= n.Last; i++ {
				if b.mine(i) {
					b.info[i].flags = 0
				}
			}
		}
	}
	return true
}
