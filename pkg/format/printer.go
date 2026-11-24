package format

import (
	"bytes"
	"slices"

	"github.com/mmcdole/dgdtools/pkg/token"
)

// frame is one bracket the printer has open inside the statement it is
// laying out. The braces of blocks the parser understood are not here:
// their nesting is already in the plan's levels.
type frame struct {
	kind     token.Kind // LParen, LBracket, LBrace
	literal  bool       // "({" array or "([" mapping literal
	isSwitch bool       // brace of a switch body, in code the parser did not understand
}

// state is what the printer carries from token to token and what the
// branches of a conditional each start from.
type state struct {
	stack      []frame
	pendSwitch bool  // saw 'switch', its brace not yet opened
	ternary    []int // stack depths of pending '?'s
}

func (s state) clone() state {
	return state{slices.Clone(s.stack), s.pendSwitch, slices.Clone(s.ternary)}
}

// branching is one conditional the printer is inside of.
type branching struct {
	start state // at the #if
	after state // at the end of the selected branch
	taken bool
}

type printer struct {
	f    *token.File
	o    Options
	buf  bytes.Buffer
	nl   []byte
	plan *plan

	state
	conds []branching

	prev       int        // index of the last significant token (-1 = none)
	prevKind   token.Kind // its kind
	prevLast   byte       // its last byte
	lastUnary  bool       // last emitted +,-,*,& was unary
	lastPrefix bool       // last emitted ++/-- was prefix
}

func (p *printer) run() {
	var gap []token.Token
	for i, t := range p.f.Tokens {
		switch {
		case t.Kind.IsTrivia():
			gap = append(gap, t)
		case t.Kind == token.EOF:
			p.finish(gap)
			return
		default:
			p.renderGap(gap, i)
			gap = gap[:0]
			p.emit(i)
		}
	}
}

// gapComments reports whether the gap contains any comment.
func gapComments(gap []token.Token) bool {
	for _, g := range gap {
		if g.Kind == token.LineComment || g.Kind == token.BlockComment {
			return true
		}
	}
	return false
}

func gapNewlines(gap []token.Token) int {
	n := 0
	for _, g := range gap {
		if g.Kind == token.Newline {
			n++
		}
	}
	return n
}

// renderGap writes the trivia between the previous significant token and
// the token at index next, applying header plans, cuddling, blank-line
// capping, spacing normalization, and comment preservation.
func (p *printer) renderGap(gap []token.Token, next int) {
	hasComments := gapComments(gap)

	// Planned KNF header layout (headers with comments get no plan).
	if !hasComments && p.prev >= 0 {
		if p.plan.has(next, join) {
			p.buf.WriteString(p.sep(next))
			return
		}
		if p.plan.has(next, breakBefore) || p.plan.has(p.prev, breakAfter) {
			p.writeNewline(nil)
			p.pad(p.indentOf(next))
			return
		}
	}

	// Cuddle control-flow braces and else.
	if !hasComments && gapNewlines(gap) > 0 && p.shouldCuddle(next) {
		p.buf.WriteByte(' ')
		return
	}

	// General path.
	pending := 0                // newlines seen but not yet written
	var pendingNl []token.Token // their tokens, for byte preservation
	var lastSpace []byte        // space run immediately before a comment
	wroteComment := false

	flushNewlines := func() {
		if pending == 0 {
			return
		}
		maxN := p.o.maxBlank() + 1
		if p.buf.Len() == 0 {
			maxN = p.o.maxBlank() // leading region: no line to end
		}
		n := min(pending, maxN)
		for i := 0; i < n; i++ {
			var orig []byte
			if i < len(pendingNl) {
				orig = p.f.Text(pendingNl[i])
			}
			p.writeNewline(orig)
		}
		pending = 0
		pendingNl = pendingNl[:0]
	}

	for _, g := range gap {
		switch g.Kind {
		case token.Newline:
			pending++
			pendingNl = append(pendingNl, g)
			lastSpace = nil
		case token.Space:
			lastSpace = p.f.Text(g)
		case token.LineComment, token.BlockComment:
			if pending > 0 {
				flushNewlines()
				p.pad(p.commentIndent(next))
			} else if p.buf.Len() > 0 {
				// Trailing comment: preserve the author's alignment run.
				if len(lastSpace) > 0 {
					p.buf.Write(lastSpace)
				} else {
					p.buf.WriteByte(' ')
				}
			}
			p.emitComment(g)
			wroteComment = true
			lastSpace = nil
		}
	}

	switch {
	case pending > 0:
		flushNewlines()
		p.pad(p.indentOf(next))
	case p.buf.Len() == 0:
		// Start of file: next begins at column 0.
	case wroteComment:
		p.buf.WriteByte(' ')
	case p.prev >= 0:
		p.buf.WriteString(p.sep(next))
	}
}

// finish handles the trivia after the last significant token: trailing
// comments are kept, trailing blank lines dropped, and the file ends with
// exactly one newline.
func (p *printer) finish(gap []token.Token) {
	sawNewline := false
	var lastSpace []byte
	for _, g := range gap {
		switch g.Kind {
		case token.Newline:
			sawNewline = true
			lastSpace = nil
		case token.Space:
			lastSpace = p.f.Text(g)
		case token.LineComment, token.BlockComment:
			if p.buf.Len() > 0 {
				if sawNewline {
					p.writeNewline(nil)
				} else if len(lastSpace) > 0 {
					p.buf.Write(lastSpace)
				} else {
					p.buf.WriteByte(' ')
				}
			}
			p.emitComment(g)
			sawNewline = false
			lastSpace = nil
		}
	}
	if p.buf.Len() > 0 {
		p.writeNewline(nil)
	}
}

// shouldCuddle reports whether the token at next, currently on its own
// line, should be joined to the previous line: the '{' of a control
// statement's block, and 'else' after the '}' of the block before it.
//
// Cuddling only fires on Allman-style layout: a '{' whose block content
// starts on the next line, and an 'else' after a '}' alone on its line.
// One-line brace blocks (`{ msg(); }`) keep their own lines; joining them
// would cascade an if/else-if ladder into one enormous line. And it only
// fires where the tree says what the token is: in code the parser did not
// understand, every line stays a line.
func (p *printer) shouldCuddle(next int) bool {
	if !p.plan.has(next, cuddle) {
		return false
	}
	switch p.f.Tokens[next].Kind {
	case token.LBrace:
		return p.prevKind != token.Directive && p.newlineAfter(next)
	case token.KwElse:
		return p.prevKind == token.RBrace && p.newlineBefore(p.prev)
	}
	return false
}

// newlineAfter reports whether a newline separates the token at idx from
// the next significant token.
func (p *printer) newlineAfter(idx int) bool {
	for i := idx + 1; i < len(p.f.Tokens); i++ {
		switch k := p.f.Tokens[i].Kind; {
		case k == token.Newline:
			return true
		case !k.IsTrivia():
			return false
		}
	}
	return false
}

// newlineBefore reports whether a newline separates the token at idx from
// the previous significant token (i.e. it starts its source line).
func (p *printer) newlineBefore(idx int) bool {
	for i := idx - 1; i >= 0; i-- {
		switch k := p.f.Tokens[i].Kind; {
		case k == token.Newline:
			return true
		case !k.IsTrivia():
			return false
		}
	}
	return true
}

func (p *printer) writeNewline(orig []byte) {
	switch p.o.LineEndings {
	case LF:
		p.buf.WriteByte('\n')
	case CRLF:
		p.buf.WriteString("\r\n")
	default:
		if orig == nil {
			p.buf.Write(p.nl)
		} else {
			p.buf.Write(orig)
		}
	}
}

func (p *printer) pad(level int) {
	for n := level * p.o.indent(); n > 0; n-- {
		p.buf.WriteByte(' ')
	}
}

// braces counts the braces open inside the current statement.
func (p *printer) braces() int {
	n := 0
	for _, fr := range p.stack {
		if fr.kind == token.LBrace {
			n++
		}
	}
	return n
}

// inSwitch reports whether the innermost open brace is a switch body's.
func (p *printer) inSwitch() bool {
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].kind == token.LBrace {
			return p.stack[i].isSwitch
		}
	}
	return false
}

// indentOf returns the indentation level of a line whose first token is
// the one at index i.
func (p *printer) indentOf(i int) int {
	t, x := p.f.Tokens[i], p.plan.info[i]
	if t.Kind == token.Directive {
		return 0 // directives start at column 0
	}
	if x.flags&head != 0 {
		return int(x.level)
	}
	// Inside a statement: the braces open in it, and one more for a line
	// that continues it.
	level := int(x.level) + p.braces()
	switch t.Kind {
	case token.RBrace:
		if p.braces() > 0 {
			level--
		}
	case token.RParen, token.RBracket:
		// Closing a continuation bracket sits at the base level.
	case token.KwCase, token.KwDefault:
		if p.inSwitch() {
			level--
		}
	default:
		if p.continues(x) {
			level++
		}
	}
	return level
}

// commentIndent returns the indentation level of a comment on a line of
// its own before the token at index i: that of the code it stands with.
func (p *printer) commentIndent(i int) int {
	// A directive has no level of its own: look past it.
	for p.f.Tokens[i].Kind == token.Directive || p.f.Tokens[i].Kind.IsTrivia() {
		i++
	}
	t, x := p.f.Tokens[i], p.plan.info[i]
	switch {
	case t.Kind == token.EOF:
		return 0
	case x.flags&head != 0:
		// Before the end of a block, or between the cases of a switch, a
		// comment belongs to the statements inside.
		if (x.flags&block != 0 && t.Kind == token.RBrace) || x.flags&caseLabel != 0 {
			return int(x.level) + 1
		}
		return int(x.level)
	case x.flags&known == 0:
		return int(x.level) + p.braces()
	}
	switch t.Kind {
	case token.RBrace:
		return int(x.level) + p.braces()
	case token.RParen, token.RBracket:
		return int(x.level) + p.braces() + 1
	}
	return p.indentOf(i)
}

// continues reports whether a line starting with a token planned as x,
// which does not begin a statement, continues an unfinished construct. In
// code the parser understood that is every such line outside a literal's
// braces; inside them, and in code it did not understand, it is a line
// inside an unclosed parenthesis or bracket, or after a line that ends in
// an operator.
func (p *printer) continues(x info) bool {
	if n := len(p.stack); n > 0 {
		if k := p.stack[n-1].kind; k == token.LParen || k == token.LBracket {
			return true
		}
	} else if x.flags&known != 0 {
		return true
	}
	return trailingOpContinues(p.prevKind)
}

// trailingOpContinues lists the operators that, at end of line, mark the
// next line as a continuation. Comma and colon are excluded deliberately:
// comma separates elements at the current level, colon ends case labels.
func trailingOpContinues(k token.Kind) bool {
	switch k {
	case token.Question, token.Arrow, token.ColonColon:
		return true
	}
	return isBinaryOp(k)
}

// emit writes the significant token at index i and updates layout state.
func (p *printer) emit(i int) {
	t, x := p.f.Tokens[i], p.plan.info[i]
	understood := x.flags&known != 0
	operand := endsOperand(p.prevKind)

	if x.flags&head != 0 {
		// A statement begins: nothing of the one before is still open.
		p.stack, p.ternary, p.pendSwitch = p.stack[:0], p.ternary[:0], false
	}
	switch t.Kind {
	case token.Directive:
		p.conditional(i)
	case token.KwSwitch:
		p.pendSwitch = !understood
	case token.LParen:
		p.stack = append(p.stack, frame{kind: t.Kind})
	case token.LBracket:
		lit := x.flags&literal != 0
		if !understood {
			lit = p.prevKind == token.LParen
		}
		p.stack = append(p.stack, frame{kind: t.Kind, literal: lit})
	case token.LBrace:
		switch {
		case x.flags&block != 0:
			// Its nesting is in the levels of what it holds.
		case understood:
			p.stack = append(p.stack, frame{kind: t.Kind, literal: true})
		default:
			lit := p.prevKind == token.LParen
			p.stack = append(p.stack, frame{kind: t.Kind, literal: lit, isSwitch: p.pendSwitch && !lit})
			if !lit {
				p.pendSwitch = false
			}
		}
	case token.RParen, token.RBracket:
		if n := len(p.stack); n > 0 && p.stack[n-1].kind == opener(t.Kind) {
			p.stack = p.stack[:n-1]
		}
	case token.RBrace:
		// A parenthesis left open inside the braces ends with them.
		n := len(p.stack)
		for n > 0 && p.stack[n-1].kind != token.LBrace {
			n--
		}
		if n > 0 {
			p.stack = p.stack[:n-1]
		}
	case token.Question:
		if !understood {
			p.ternary = append(p.ternary, len(p.stack))
		}
	case token.Colon:
		if n := len(p.ternary); n > 0 && p.ternary[n-1] == len(p.stack) {
			p.ternary = p.ternary[:n-1]
		}
	case token.Plus, token.Minus, token.Star, token.Amp:
		p.lastUnary = x.flags&binary == 0
		if !understood {
			p.lastUnary = !operand
		}
	case token.Inc, token.Dec:
		p.lastPrefix = x.flags&postfix == 0
		if !understood {
			p.lastPrefix = !operand
		}
	}

	text := p.f.Text(t)
	p.buf.Write(text)
	p.prev, p.prevKind = i, t.Kind
	if len(text) > 0 {
		p.prevLast = text[len(text)-1]
	}
}

// conditional keeps the printer's state right across the branches of a
// conditional: each branch is laid out from the state the conditional
// began with, and the code after it continues from the branch that was
// selected.
func (p *printer) conditional(i int) {
	m, ok := p.plan.conds[i]
	if !ok {
		return
	}
	if m.kind == condOpen {
		p.conds = append(p.conds, branching{start: p.state.clone()})
		return
	}
	n := len(p.conds)
	if n == 0 {
		return
	}
	c := &p.conds[n-1]
	if m.taken {
		c.after, c.taken = p.state.clone(), true
	}
	switch {
	case m.kind == condNext:
		p.state = c.start.clone()
	case c.taken:
		p.state = c.after
	default:
		p.state = c.start
	}
	if m.kind == condEnd {
		p.conds = p.conds[:n-1]
	}
}

func opener(closer token.Kind) token.Kind {
	if closer == token.RParen {
		return token.LParen
	}
	return token.LBracket
}

// endsOperand reports whether k can end an expression operand. In code
// the parser did not understand it tells unary from binary +,-,*,& and
// prefix from postfix ++/--; everywhere it tells a call or an index from
// a parenthesis or bracket that begins an operand.
func endsOperand(k token.Kind) bool {
	switch k {
	case token.Ident, token.IntLit, token.FloatLit, token.StringLit,
		token.CharLit, token.RParen, token.RBracket, token.RBrace,
		token.Inc, token.Dec, token.KwNil:
		return true
	}
	return false
}

// colonIsTernary reports whether the colon at index i closes a '?'.
func (p *printer) colonIsTernary(i int) bool {
	if x := p.plan.info[i]; x.flags&known != 0 {
		return x.flags&ternary != 0
	}
	n := len(p.ternary)
	return n > 0 && p.ternary[n-1] == len(p.stack)
}

// isPostfix reports whether the ++ or -- at index i follows its operand.
func (p *printer) isPostfix(i int) bool {
	if x := p.plan.info[i]; x.flags&known != 0 {
		return x.flags&postfix != 0
	}
	return endsOperand(p.prevKind)
}

// topLiteral reports whether the innermost open frame is a literal of the
// given bracket kind.
func (p *printer) topLiteral(kind token.Kind) bool {
	if n := len(p.stack); n > 0 {
		fr := p.stack[n-1]
		return fr.kind == kind && fr.literal
	}
	return false
}

// sep decides the canonical same-line spacing between the previously
// emitted significant token and the one at index next: "" or " ".
func (p *printer) sep(next int) string {
	s := p.sepRaw(next)
	if s == "" && p.mergeRisk(p.f.Tokens[next]) {
		return " "
	}
	return s
}

func (p *printer) sepRaw(next int) string {
	prev := p.prevKind
	nk := p.f.Tokens[next].Kind

	// Separators and closers bind tight.
	switch nk {
	case token.Comma, token.Semicolon:
		return ""
	case token.RParen:
		return ""
	case token.RBracket:
		if p.topLiteral(token.LBracket) {
			return " " // ([ ... ])
		}
		return ""
	case token.RBrace:
		return " " // ({ ... }) padding and one-line blocks alike
	}

	// After openers.
	switch prev {
	case token.LParen:
		return "" // (x, ((, ({, ([
	case token.LBracket:
		if p.topLiteral(token.LBracket) {
			return " " // ([ "k" ...
		}
		return ""
	case token.LBrace:
		return " " // ({ "a" and one-line blocks
	}

	// Tight glue operators.
	if nk == token.Arrow || prev == token.Arrow ||
		nk == token.Dot || prev == token.Dot ||
		nk == token.DotDot || prev == token.DotDot ||
		nk == token.Ellipsis {
		return ""
	}
	if nk == token.ColonColon {
		if prev == token.Ident { // label::func
			return ""
		}
		return " "
	}
	if prev == token.ColonColon {
		return ""
	}

	// Increment/decrement.
	if nk == token.Inc || nk == token.Dec {
		if p.isPostfix(next) {
			return ""
		}
		return " "
	}
	if (prev == token.Inc || prev == token.Dec) && p.lastPrefix {
		return "" // ++i
	}

	// Unary operators bind tight to their operand.
	if prev == token.Not || prev == token.Tilde {
		return ""
	}
	if (prev == token.Plus || prev == token.Minus || prev == token.Star || prev == token.Amp) && p.lastUnary {
		return ""
	}

	// Colons: ternary spaced; case/label/mapping colons tight on the left.
	if nk == token.Colon {
		if p.colonIsTernary(next) {
			return " "
		}
		return ""
	}
	if prev == token.Colon || nk == token.Question || prev == token.Question {
		return " "
	}

	// Keywords space out from their neighbors.
	if prev.IsKeyword() || nk.IsKeyword() {
		return " "
	}

	// Calls and indexing.
	if nk == token.LParen || nk == token.LBracket {
		if endsOperand(prev) {
			return ""
		}
		return " "
	}
	return " "
}

// isBinaryOp reports whether k is an operator that stands between two
// operands (or, for the four that can also be unary, may).
func isBinaryOp(k token.Kind) bool {
	return k.Precedence() > 0 || k.IsAssign() || k == token.LArrow
}

// mergeRisk reports whether writing next directly after the previous token
// could fuse them into a different token. The gate would catch any miss;
// this keeps it from ever tripping.
func (p *printer) mergeRisk(next token.Token) bool {
	if next.Len() == 0 {
		return false
	}
	a, b := p.prevLast, p.f.Text(next)[0]
	if token.IsIdentByte(a) && token.IsIdentByte(b) {
		return true
	}
	switch a {
	case '+':
		return b == '+' || b == '='
	case '-':
		return b == '-' || b == '=' || b == '>'
	case '<':
		return b == '<' || b == '=' || b == '-'
	case '>':
		return b == '>' || b == '='
	case '=', '!', '^', '*', '%':
		return b == '='
	case '&':
		return b == '&' || b == '='
	case '|':
		return b == '|' || b == '='
	case '/':
		return b == '=' || b == '*' || b == '/'
	case '.':
		if b == '.' {
			return true
		}
		// Only a lone '.' fuses with a digit (into a float); ".." + digit
		// relexes to DotDot IntLit unchanged.
		return p.prevKind == token.Dot && b >= '0' && b <= '9'
	case ':':
		return b == ':'
	case '#':
		return b == '#'
	}
	return false
}

// emitComment writes a comment; multi-line block comments are shifted
// whole: every interior line moves by the same column delta as the "/*"
// line, preserving internal alignment.
func (p *printer) emitComment(t token.Token) {
	text := p.f.Text(t)
	if t.Kind == token.BlockComment && bytes.ContainsAny(text, "\r\n") {
		p.emitShiftedComment(t, text)
		return
	}
	p.buf.Write(text)
}

func (p *printer) emitShiftedComment(t token.Token, text []byte) {
	oldCol := int(t.Off - p.f.Lines[p.f.Pos(t.Off).Line-1])
	newCol := lineLen(p.buf.Bytes())
	delta := newCol - oldCol

	lines := splitKeepEndings(text)
	p.buf.Write(lines[0])
	for _, line := range lines[1:] {
		switch {
		case delta > 0:
			for i := 0; i < delta; i++ {
				p.buf.WriteByte(' ')
			}
			p.buf.Write(line)
		case delta < 0:
			p.buf.Write(trimLeadingWS(line, -delta))
		default:
			p.buf.Write(line)
		}
	}
}

// lineLen returns the length in bytes of the last (current) line of buf.
func lineLen(buf []byte) int {
	if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
		return len(buf) - i - 1
	}
	return len(buf)
}

// splitKeepEndings splits text after each newline, keeping the terminators
// attached to the preceding piece.
func splitKeepEndings(text []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			out = append(out, text[start:i+1])
			start = i + 1
		}
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}

// trimLeadingWS removes up to n leading space/tab bytes from line.
func trimLeadingWS(line []byte, n int) []byte {
	i := 0
	for i < len(line) && i < n && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	return line[i:]
}
