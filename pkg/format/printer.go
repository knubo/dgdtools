package format

import (
	"bytes"
	"slices"

	"github.com/mmcdole/dgdtools/pkg/directive"
	"github.com/mmcdole/dgdtools/pkg/lexer"
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
	unit string // one level of indentation
	nl   []byte
	plan *plan

	state
	conds []branching

	// sel says which gaps are formatted; nil is all of them. ord is the
	// gap being written: the number of significant tokens before it.
	sel *selection
	ord int
	// off is set between a `dgdtools-ignore-start format` comment and
	// the `dgdtools-ignore-end` that answers it.
	off bool
	// taught is, per nesting level, the indentation an unselected line
	// that begins a statement at that level has (nil: none seen). A
	// selected line is indented from it; see indent.
	taught [][]byte

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
			p.gap(gap, i)
			return
		default:
			p.gap(gap, i)
			gap = gap[:0]
			p.emit(i)
			p.ord++
		}
	}
}

// gap writes the trivia between the previous significant token and the
// one at index next (the EOF token after the last). A gap that is not
// selected, the rows at either end of one that are not, and the part of
// any gap that lies in a region the formatter is told to ignore, are
// copied as they are; the rest is laid out.
func (p *printer) gap(gap []token.Token, next int) {
	if !p.sel.has(p.ord) {
		for _, g := range gap {
			p.toggle(g)
		}
		p.copyGap(gap, next)
		return
	}
	head, tail := p.sel.kept(p.ord)
	if head > 0 {
		// Up to the line break that begins the first selected row.
		k := nthNewline(gap, head)
		for _, g := range gap[:k+1] {
			p.toggle(g)
		}
		p.raw(gap[:k+1])
		gap = gap[k+1:]
	}
	var rest []token.Token
	if tail > 0 {
		// From the line break that ends the last selected row.
		k := nthNewline(gap, gapNewlines(gap)-tail+1)
		gap, rest = gap[:k], gap[k:]
	}
	defer func() {
		if rest != nil {
			for _, g := range rest {
				p.toggle(g)
			}
			p.copyGap(rest, next)
		}
	}()
	// The off region begins after its marker and ends after the marker
	// that closes it: each comment is laid out, or kept, with the side
	// it stands on. Next to rows that are kept, nothing joins lines.
	start, resumed := 0, head > 0 || tail > 0
	for k, g := range gap {
		switch was := p.off; {
		case p.toggle(g) == was:
		case was: // ...-end: on again
			p.raw(gap[start : k+1])
			start, resumed = k+1, true
		default: // ...-start: off
			p.layout(gap[start:k+1], next, resumed, false)
			start = k + 1
		}
	}
	if p.off {
		p.raw(gap[start:])
		return
	}
	p.layout(gap[start:], next, resumed, rest == nil)
}

// nthNewline returns the index in gap of its n-th line break, from 1.
func nthNewline(gap []token.Token, n int) int {
	for k, g := range gap {
		if g.Kind == token.Newline {
			if n--; n == 0 {
				return k
			}
		}
	}
	panic("format: a gap has fewer line breaks than its selection says")
}

// toggle turns the formatter off or on if g is a comment that says so
// (`dgdtools-ignore-start format`, `dgdtools-ignore-end format`, or an
// end of everything), and reports the state after it.
func (p *printer) toggle(g token.Token) bool {
	if d, ok := directive.Parse(p.f, g); ok {
		switch {
		case d.Form == directive.Start && d.Has(directive.Format):
			p.off = true
		case d.Form == directive.End && (d.Has(directive.Format) || len(d.Targets) == 0):
			p.off = false
		}
	}
	return p.off
}

// raw copies trivia as it is.
func (p *printer) raw(gap []token.Token) {
	for _, g := range gap {
		p.buf.Write(p.f.Text(g))
	}
}

// copyGap writes a gap that is not selected: as it is, except that the
// blanks at the end of a selected line go. The line that follows the gap
// keeps its indentation, and says what the indentation of its level is.
func (p *printer) copyGap(gap []token.Token, next int) {
	trim := p.sel.trims(p.ord)
	lineStart := -1 // index in gap of what follows its last newline
	for k, g := range gap {
		if g.Kind == token.Newline {
			lineStart, trim = k+1, false
		}
		if g.Kind == token.BlockComment && bytes.ContainsAny(p.f.Text(g), "\r\n") {
			trim = false // the selected line has ended inside it
		}
		if trim && g.Kind == token.Space && k+1 < len(gap) && gap[k+1].Kind == token.Newline {
			continue
		}
		p.buf.Write(p.f.Text(g))
	}
	if lineStart < 0 || p.f.Tokens[next].Kind == token.EOF {
		return
	}
	var lead []byte
	for _, g := range gap[lineStart:] {
		if g.Kind != token.Space {
			return // a comment before the token: not the line's indentation
		}
		lead = append(lead, p.f.Text(g)...)
	}
	p.teach(next, lead)
}

// teach records that the line beginning with the token at index i is
// indented by lead, when that says something about its level.
func (p *printer) teach(i int, lead []byte) {
	p.forget(i)
	if level, ok := p.teaches(i); ok {
		p.learn(level, lead)
	}
}

// teaches reports whether the indentation of a line beginning with the
// token at index i says how its level is indented, and which level that
// is: the token begins a statement the parser understood, inside a
// function.
func (p *printer) teaches(i int) (level int, ok bool) {
	x := p.plan.info[i]
	switch {
	case x.flags&(known|head) != known|head, x.flags&(caseLabel|block) != 0:
		return 0, false // not a statement's first line
	case p.f.Tokens[i].Kind == token.KwElse, x.level == 0:
		return 0, false // part of the statement above; and the top level is the margin
	}
	return int(x.level), true
}

func (p *printer) learn(level int, lead []byte) {
	for len(p.taught) <= level {
		p.taught = append(p.taught, nil)
	}
	p.taught[level] = append([]byte{}, lead...)
}

// forget drops what deeper levels taught when a line begins with the
// token at index i and that token begins a statement: their blocks have
// ended. Case labels stand at their switch's level in the middle of its
// block, and end nothing.
func (p *printer) forget(i int) {
	x := p.plan.info[i]
	if x.flags&head == 0 || x.flags&caseLabel != 0 {
		return
	}
	if n := int(x.level) + 1; len(p.taught) > n {
		p.taught = p.taught[:n]
	}
}

// indent writes the indentation of a line at the given level whose first
// token is the one at index i. When only part of the file is formatted
// that is the indentation the nearest level around it has in this file,
// plus the configured unit for each level beyond that.
func (p *printer) indent(i, level int) {
	if p.sel != nil {
		// The line's own level is asked of the lines above it, then of
		// the lines below; a line that continues a statement, or closes
		// something, is indented from its statement's.
		own := min(level, int(p.plan.info[i].level))
		if own > 0 && (own >= len(p.taught) || p.taught[own] == nil) {
			if lead, ok := p.below(i, own); ok {
				p.learn(own, lead)
			}
		}
		for base := min(level, len(p.taught)-1); base > 0; base-- {
			if p.taught[base] != nil {
				p.buf.Write(p.taught[base])
				level -= base
				break
			}
		}
		p.forget(i)
	}
	for ; level > 0; level-- {
		p.buf.WriteString(p.unit)
	}
}

// below returns the indentation of the first line after the token at
// index i that is not selected and teaches the given level, looking no
// further than the end of the block the token is in.
func (p *printer) below(i, level int) ([]byte, bool) {
	toks := p.f.Tokens
	ord := p.ord
	lineStart := -1 // index of what follows the last newline of the gap
	for j := i + 1; j < len(toks); j++ {
		switch t := toks[j]; {
		case t.Kind == token.Newline:
			lineStart = j + 1
		case t.Kind == token.Space:
		case t.Kind.IsTrivia():
			lineStart = -1 // a comment: what follows is not at the line's start
		case t.Kind == token.EOF:
			return nil, false
		default:
			ord++
			x := p.plan.info[j]
			if x.flags&head != 0 && x.flags&caseLabel == 0 && int(x.level) < level {
				return nil, false // the block has ended
			}
			if l, ok := p.teaches(j); ok && l == level && lineStart >= 0 && p.sel.keepsLine(ord) {
				return p.f.Src[toks[lineStart].Off:t.Off], true
			}
			lineStart = -1
		}
	}
	return nil, false
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

// layout writes the trivia of a gap, or of the part of one that a skip
// marker leaves to the formatter, in the one style: the line breaks
// kept, blank-line capping, spacing normalization, comments kept.
// resumed says the part follows a comment already written (the marker
// that turned the formatter back on) or rows of the gap that are kept,
// and so must not be joined to what is before it; tail says it reaches
// the token at index next, so that the token's indentation or separator
// is written, and otherwise the line breaks it ends with are kept.
func (p *printer) layout(gap []token.Token, next int, resumed, tail bool) {
	if p.f.Tokens[next].Kind == token.EOF && tail {
		p.finish(gap)
		return
	}
	pending := 0                // newlines seen but not yet written
	var pendingNl []token.Token // their tokens, for byte preservation
	var lastSpace []byte        // space run immediately before a comment
	wroteComment := resumed
	// A line is begun already when the rows before are kept.
	fresh := p.buf.Len() > 0 && p.buf.Bytes()[p.buf.Len()-1] == '\n'

	flushNewlines := func() {
		if pending == 0 {
			return
		}
		maxN := maxBlankLines + 1
		if p.buf.Len() == 0 || fresh {
			maxN = maxBlankLines // leading region, or a line begun: no line to end
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
			if pending > 0 || fresh {
				flushNewlines()
				p.indent(next, p.commentIndent(next))
				fresh = false
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
	case !tail:
		// What follows is kept as it is, and so are the line breaks
		// before it.
		for _, nl := range pendingNl {
			p.buf.Write(p.f.Text(nl))
		}
	case pending > 0 || fresh:
		flushNewlines()
		p.indent(next, p.indentOf(next))
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
	// The line break that ends a line here keeps its bytes, like every
	// other the author wrote: the first of those that follow the line.
	// Not a carriage return by itself, which ends no line for anybody
	// who reads the file afterwards.
	var newline, lastSpace []byte
	sawNewline := false
	for _, g := range gap {
		switch g.Kind {
		case token.Newline:
			if text := p.f.Text(g); !sawNewline && text[len(text)-1] == '\n' {
				newline = text
			}
			sawNewline, lastSpace = true, nil
		case token.Space:
			lastSpace = p.f.Text(g)
		case token.LineComment, token.BlockComment:
			if p.buf.Len() > 0 {
				if sawNewline {
					p.writeNewline(newline)
				} else if len(lastSpace) > 0 {
					p.buf.Write(lastSpace)
				} else {
					p.buf.WriteByte(' ')
				}
			}
			p.emitComment(g)
			sawNewline, newline, lastSpace = false, nil, nil
		}
	}
	if p.buf.Len() > 0 && !p.runsOn() {
		p.writeNewline(newline)
	}
}

// runsOn reports whether the file ends in a directive that a line break
// would not end: one that stops at a backslash, or inside a comment. It
// has no line to go on to, and is not given one.
func (p *printer) runsOn() bool {
	if p.prevKind != token.Directive {
		return false
	}
	text := p.f.Text(p.f.Tokens[p.prev])
	again := lexer.Lex(p.f.Path, append(bytes.Clone(text), '\n'), p.f.Dialect)
	return again.Tokens[0].Len() != len(text)
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

// depth counts what is open inside the current statement that holds
// lines of its own: the braces, and the brackets of mapping literals.
// Each indents the lines inside it by one level, so a literal inside a
// literal is one level further in. A parenthesis or an index bracket
// does not: a line inside one continues its statement, however many are
// open.
func (p *printer) depth() int {
	n := 0
	for _, fr := range p.stack {
		if fr.kind == token.LBrace || fr.kind == token.LBracket && fr.literal {
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
	// Inside a statement: what is open in it, and one more for a line
	// that continues it.
	level := int(x.level) + p.depth()
	switch t.Kind {
	case token.RBrace:
		if p.depth() > 0 {
			level--
		}
	case token.RBracket:
		// The bracket of a mapping stands where the mapping began; one
		// that closes a continuation sits at the base level.
		if p.topLiteral(token.LBracket) {
			level--
		}
	case token.RParen:
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
		return int(x.level) + p.depth()
	}
	switch t.Kind {
	case token.RBrace:
		return int(x.level) + p.depth()
	case token.RParen, token.RBracket:
		if t.Kind == token.RBracket && p.topLiteral(token.LBracket) {
			return int(x.level) + p.depth()
		}
		return int(x.level) + p.depth() + 1
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
		if fr := p.stack[n-1]; fr.kind == token.LParen || fr.kind == token.LBracket && !fr.literal {
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
	// A number takes a lone '.' for its own.
	return p.prevKind == token.IntLit && next.Kind == token.Dot
}

// emitComment writes a comment; multi-line block comments are shifted
// whole: every interior line moves as the "/*" line does, preserving
// internal alignment.
func (p *printer) emitComment(t token.Token) {
	text := p.f.Text(t)
	if t.Kind == token.BlockComment && bytes.ContainsAny(text, "\r\n") {
		p.emitShiftedComment(t, text)
		return
	}
	p.buf.Write(text)
}

func (p *printer) emitShiftedComment(t token.Token, text []byte) {
	// What stood before the comment on its line, and what stands there
	// now.
	was := p.f.Src[p.f.Lines[p.f.Pos(t.Off).Line-1]:t.Off]
	now := bytes.Clone(p.buf.Bytes()[p.buf.Len()-lineLen(p.buf.Bytes()):])
	own := isBlank(was) && isBlank(now) // the comment begins its line
	delta := len(now) - len(was)

	lines := splitKeepEndings(text)
	p.buf.Write(lines[0])
	for _, line := range lines[1:] {
		switch {
		case own && bytes.HasPrefix(line, was):
			// Indented like its first line, and then some: the first
			// line's indentation is replaced by the new one.
			p.buf.Write(now)
			p.buf.Write(line[len(was):])
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

func isBlank(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' {
			return false
		}
	}
	return true
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
