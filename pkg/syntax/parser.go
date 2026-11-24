package syntax

import (
	"fmt"

	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// Options configures a parse.
type Options struct {
	// Macros is the macro view of the file. nil means "nothing is known":
	// the view is treated as empty and partial.
	Macros *pp.View
	// Sel is the active-branch selection; nil computes the default.
	Sel *pp.Selection
}

// Parse builds the syntax tree of f. It never fails and never panics on
// malformed input.
func Parse(f *token.File, opt Options) *File {
	sel := opt.Sel
	if sel == nil {
		sel = pp.Select(f, nil)
	}
	p := &parser{
		f: f, toks: f.Tokens, sig: sel.Sig, view: opt.Macros,
		partial: opt.Macros == nil || !opt.Macros.Complete,
		file:    &File{Tok: f, Sel: sel},
	}
	p.parseFile()
	p.finish()
	return p.file
}

type parser struct {
	f       *token.File
	toks    []token.Token
	sig     []int
	i       int
	view    *pp.View
	partial bool
	file    *File
	errs    []*Error

	spec   int  // speculation depth: errors unwind instead of recording
	opaque bool // the current bail is macro-induced, not an error

	// abort is set when a function definition is found inside a function
	// body: every open construct unwinds and top-level parsing resumes at
	// resume.
	abort  bool
	resume int

	resumes map[int]bool // tokens where parsing resumed after a skip

	fnName  string // enclosing function, for messages
	fnBrace int    // its opening brace token
}

type (
	stmtBail struct{}
	specBail struct{}
)

// ------------------------------------------------------------- cursor

func (p *parser) kind() token.Kind { return p.kindAt(0) }

func (p *parser) kindAt(d int) token.Kind {
	if j := p.i + d; j >= 0 && j < len(p.sig) {
		return p.toks[p.sig[j]].Kind
	}
	return token.EOF
}

// idx returns the token index at the cursor (the EOF token past the end).
func (p *parser) idx() int { return p.idxAt(0) }

func (p *parser) idxAt(d int) int {
	if j := p.i + d; j >= 0 && j < len(p.sig) {
		return p.sig[j]
	}
	return len(p.toks) - 1
}

// markResume records where parsing resumed after skipping a construct
// that failed or was opaque. The resynchronization point is a guess, so an
// error in the very next construct is not trusted: it is either a
// consequence of the first, or of a macro.
func (p *parser) markResume() {
	if p.resumes == nil {
		p.resumes = map[int]bool{}
	}
	p.resumes[p.idx()] = true
}

// sync skips to the end of the construct that began at cursor position
// start and failed at the current position. It accounts for the brackets
// the construct had open when it failed, then stops after the ';' that
// ends it, after a block that ends it, or before the '}' that closes the
// enclosing block (at top level a stray '}' is consumed).
func (p *parser) sync(start int, top bool) (eof bool) {
	// One entry per unclosed bracket: true = a block '{'. braces
	// parallels it: true = any '{' (block or "({" array literal).
	var open, braces []bool
	push := func(j int) {
		k := p.toks[p.sig[j]].Kind
		block := k == token.LBrace && !(j > 0 && p.toks[p.sig[j-1]].Kind == token.LParen)
		open = append(open, block)
		braces = append(braces, k == token.LBrace)
	}
	pop := func(n int) {
		open, braces = open[:n], braces[:n]
	}
	// In a for/rlimits header ';' separates clauses: there the skip must
	// not stop at a ';' while the header's parenthesis is open.
	header := !top && (p.toks[p.tokAt(start)].Kind == token.KwFor || p.toks[p.tokAt(start)].Kind == token.KwRlimits)
	for j := start; j < p.i && j < len(p.sig); j++ {
		switch p.toks[p.sig[j]].Kind {
		case token.LParen, token.LBracket, token.LBrace:
			push(j)
		case token.RParen, token.RBracket, token.RBrace:
			if len(open) > 0 {
				pop(len(open) - 1)
			}
		}
	}
	for {
		switch p.kind() {
		case token.EOF:
			return true
		case token.LParen, token.LBracket, token.LBrace:
			push(p.i)
		case token.RParen, token.RBracket:
			if n := len(open); n > 0 && !braces[n-1] {
				pop(n - 1)
			}
		case token.RBrace:
			// Find the brace this closes, discarding parens left open
			// above it.
			j := len(open) - 1
			for j >= 0 && !braces[j] {
				j--
			}
			if j < 0 {
				if top {
					p.next()
				}
				return false // closes the enclosing block
			}
			block := open[j]
			pop(j)
			if block && !anyBlock(open) {
				p.next()
				return false // the construct's own block just closed
			}
		case token.Semicolon:
			if !anyBlock(open) && !(header && len(open) > 0) {
				p.next()
				return false
			}
		}
		p.next()
	}
}

func anyBlock(open []bool) bool {
	for _, b := range open {
		if b {
			return true
		}
	}
	return false
}

// tokAt maps a cursor position to a token index, clamping at EOF.
func (p *parser) tokAt(i int) int {
	if i >= 0 && i < len(p.sig) {
		return p.sig[i]
	}
	return len(p.toks) - 1
}

func (p *parser) prevIdx() int {
	if p.i > 0 && p.i-1 < len(p.sig) {
		return p.sig[p.i-1]
	}
	if p.i > 0 && len(p.sig) > 0 {
		return p.sig[len(p.sig)-1]
	}
	return 0
}

func (p *parser) next() int {
	t := p.idx()
	if p.i < len(p.sig) {
		p.i++
	}
	return t
}

func (p *parser) text(tok int) string { return string(p.f.Text(p.toks[tok])) }

func (p *parser) macroAt(d int) *pp.Macro {
	if p.view == nil || p.kindAt(d) != token.Ident {
		return nil
	}
	return p.view.Lookup(p.text(p.idxAt(d)))
}

func (p *parser) classAt(d int) (pp.Class, bool) {
	if m := p.macroAt(d); m != nil {
		return m.Class, true
	}
	return 0, false
}

func (p *parser) describe(tok int) string {
	t := p.toks[tok]
	switch t.Kind {
	case token.EOF:
		return "end of file"
	case token.Ident, token.IntLit, token.FloatLit, token.CharLit:
		return "'" + p.text(tok) + "'"
	case token.StringLit:
		s := p.text(tok)
		if len(s) > 24 {
			s = s[:21] + `..."`
		}
		return s
	case token.Illegal:
		return "illegal token"
	}
	return "'" + t.Kind.String() + "'"
}

func (p *parser) newlineBetween(a, b int) bool {
	for i := a + 1; i < b && i < len(p.toks); i++ {
		switch p.toks[i].Kind {
		case token.Newline, token.Directive:
			return true
		case token.BlockComment:
			for _, c := range p.f.Text(p.toks[i]) {
				if c == '\n' {
					return true
				}
			}
		}
	}
	return false
}

// ------------------------------------------------------------- errors

// fail records an error at the current token and unwinds to the enclosing
// statement or declaration.
func (p *parser) fail(format string, args ...any) {
	p.failAt(p.idx(), -1, "", format, args...)
}

func (p *parser) failAt(tok, related int, relMsg, format string, args ...any) {
	if p.spec > 0 {
		panic(specBail{})
	}
	if !p.abort {
		if p.toks[tok].Kind == token.Illegal {
			p.opaque = true // the lexer already reported it
		} else {
			p.errs = append(p.errs, &Error{
				Msg: fmt.Sprintf(format, args...), Tok: tok,
				Related: related, RelatedMsg: relMsg,
				RegionFirst: tok, RegionLast: tok,
			})
		}
	}
	panic(stmtBail{})
}

// bailOpaque abandons the current statement without an error: a macro
// makes it unparseable as written.
func (p *parser) bailOpaque() {
	if p.spec > 0 {
		panic(specBail{})
	}
	p.opaque = true
	panic(stmtBail{})
}

func (p *parser) unexpected(want string) {
	if want == "" {
		p.fail("unexpected %s", p.describe(p.idx()))
	}
	p.fail("expected %s, found %s", want, p.describe(p.idx()))
}

func (p *parser) expect(k token.Kind) int {
	if p.kind() == k {
		return p.next()
	}
	if p.abort {
		return p.idx()
	}
	p.unexpected("'" + k.String() + "'")
	return 0
}

// expectClose expects the closer of the bracket opened at open.
func (p *parser) expectClose(k token.Kind, open int) int {
	if p.kind() == k {
		return p.next()
	}
	if p.abort {
		return p.idx()
	}
	line := p.f.Pos(p.toks[open].Off).Line
	p.failAt(p.idx(), open, "opened here",
		"expected '%s' to close '%s' opened on line %d, found %s",
		k, p.toks[open].Kind, line, p.describe(p.idx()))
	return 0
}

// try runs fn speculatively: on any error the cursor is restored and try
// reports false.
func (p *parser) try(fn func()) (ok bool) {
	i, nerr := p.i, len(p.errs)
	p.spec++
	defer func() {
		p.spec--
		if r := recover(); r != nil {
			if _, is := r.(specBail); !is {
				panic(r)
			}
			p.i, p.errs = i, p.errs[:nerr]
			ok = false
		}
	}()
	fn()
	return true
}

// ------------------------------------------------------------ top level

func (p *parser) parseFile() {
	for p.i < len(p.sig) {
		start := p.i
		d := p.parseDeclRecover()
		if d != nil {
			p.file.Decls = append(p.file.Decls, d)
		}
		if p.abort {
			p.abort = false
			p.i = p.resume
			if p.i <= start {
				p.i = start + 1
			}
			continue
		}
		if p.i == start { // no progress: never loop
			p.i++
		}
	}
}

func (p *parser) parseDeclRecover() (d Decl) {
	start := p.i
	nerr := len(p.errs)
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if _, ok := r.(stmtBail); !ok {
			panic(r)
		}
		opaque := p.opaque && len(p.errs) == nerr
		p.opaque = false
		eof := p.sync(start, true)
		last := p.prevIdx()
		if p.i <= start {
			p.i = start + 1
			last = p.tokAt(start)
		}
		p.markResume()
		if !p.abort {
			for _, e := range p.errs[nerr:] {
				// The region is what was read before the failure, not
				// what was skipped after it.
				e.RegionFirst, e.RegionLast = p.tokAt(start), e.Tok
				e.Severe = true
			}
		}
		_ = eof
		p.file.Partial = true
		d = &BadDecl{span: span{p.tokAt(start), maxInt(last, p.tokAt(start))}, Opaque: opaque}
	}()
	return p.parseDecl()
}

// skipBalanced skips a bracketed group starting at the cursor. It stops at
// EOF if the group is never closed.
func (p *parser) skipBalanced() {
	depth := 0
	for p.kind() != token.EOF {
		switch p.kind() {
		case token.LParen, token.LBracket, token.LBrace:
			depth++
		case token.RParen, token.RBracket, token.RBrace:
			depth--
		}
		p.next()
		if depth <= 0 {
			return
		}
	}
}

func isSpecKw(k token.Kind) bool {
	switch k {
	case token.KwPrivate, token.KwStatic, token.KwAtomic, token.KwNomask, token.KwVarargs:
		return true
	}
	return false
}

// parseSpecs consumes class specifiers: keywords, and identifiers that are
// empty/specifier macros (or, in a partial view, unknown identifiers
// directly followed by a specifier or type keyword).
func (p *parser) parseSpecs() []Spec {
	var specs []Spec
	for {
		k := p.kind()
		if isSpecKw(k) {
			specs = append(specs, Spec{Tok: p.next(), Kind: k})
			continue
		}
		if k == token.Ident && p.kindAt(1) != token.LParen {
			c, known := p.classAt(0)
			if known && (c == pp.ClassEmpty || c == pp.ClassSpecifier) {
				specs = append(specs, Spec{Tok: p.next(), Kind: token.Ident})
				continue
			}
			if !known && p.partial {
				if n := p.kindAt(1); isSpecKw(n) || pp.IsTypeKeyword(n) || n == token.KwInherit {
					specs = append(specs, Spec{Tok: p.next(), Kind: token.Ident})
					continue
				}
			}
		}
		return specs
	}
}

// typeStartAt reports whether the token at offset d begins a type.
func (p *parser) typeStartAt(d int) bool {
	k := p.kindAt(d)
	if pp.IsTypeKeyword(k) {
		return true
	}
	if k != token.Ident {
		return false
	}
	c, known := p.classAt(d)
	return known && c == pp.ClassType
}

func (p *parser) parseType() *TypeSpec {
	t := &TypeSpec{Kind: p.kind()}
	t.First = p.next()
	t.Last = t.First
	if t.Kind == token.KwObject {
		switch {
		case p.kind() == token.StringLit:
			t.Path = p.parsePrimary()
			_, t.Last = t.Path.Span()
		case p.kind() == token.Ident && (p.kindAt(1) == token.Ident ||
			(p.kindAt(1) == token.Star && (p.kindAt(2) == token.Ident || p.kindAt(2) == token.Star))):
			// object PATH_MACRO name: a typed object whose path is a macro.
			t.Path = p.ident()
			_, t.Last = t.Path.Span()
		}
	}
	return t
}

func (p *parser) parseStars() int {
	n := 0
	for p.kind() == token.Star {
		p.next()
		n++
	}
	return n
}

func (p *parser) ident() *Ident {
	if p.kind() != token.Ident {
		p.unexpected("identifier")
	}
	t := p.next()
	return &Ident{span{t, t}, p.text(t)}
}

func (p *parser) parseDecl() Decl {
	si := p.i
	start := p.idx()
	if p.kind() == token.Illegal {
		p.bailOpaque()
	}
	specs := p.parseSpecs()

	if p.kind() == token.KwInherit {
		return p.parseInherit(si, start, specs)
	}

	// A macro standing at declaration level expands to something we
	// cannot see: make it opaque.
	if p.kind() == token.Ident {
		if m := p.macroAt(0); m != nil && m.Class != pp.ClassType {
			return p.opaqueDecl(start)
		}
	}

	var typ *TypeSpec
	switch {
	case p.typeStartAt(0) && !(p.kind() == token.Ident && p.kindAt(1) == token.LParen):
		typ = p.parseType()
	case p.partial && p.kind() == token.Ident && p.macroAt(0) == nil &&
		(p.kindAt(1) == token.Ident || p.kindAt(1) == token.Star) &&
		(p.kindAt(2) == token.LParen || p.kindAt(2) == token.Ident ||
			p.kindAt(2) == token.Star || p.kindAt(2) == token.Semicolon || p.kindAt(2) == token.Comma):
		// Partial view: `status foo(` / `status x;` — an unseen type macro.
		typ = &TypeSpec{Kind: token.Ident}
		typ.First = p.next()
		typ.Last = typ.First
	}
	stars := p.parseStars()

	switch {
	case p.kind() == token.KwOperator:
		return p.parseFunc(start, specs, typ, stars)
	case p.kind() == token.Ident && p.kindAt(1) == token.LParen:
		fn := p.parseFunc(start, specs, typ, stars)
		if fn.Body != nil || typ == nil {
			return fn
		}
		// Prototype, possibly the first of a declarator list.
		vd := &VarDecl{span: span{start, fn.Last}, Specs: specs, Type: typ, Protos: []*FuncDecl{fn}}
		if p.kind() == token.Comma {
			p.next()
			p.parseDeclarators(vd, true)
		}
		vd.Last = p.endDecl(si)
		fn.Last = vd.Last
		if len(vd.Vars) == 0 && len(vd.Protos) == 1 {
			return fn
		}
		return vd
	case typ != nil && p.kind() == token.Ident:
		vd := &VarDecl{span: span{start, start}, Specs: specs, Type: typ}
		p.parseDeclaratorsFrom(vd, stars, true)
		vd.Last = p.endDecl(si)
		return vd
	}
	if typ == nil && len(specs) == 0 {
		switch p.kind() {
		case token.Semicolon:
			p.fail("unexpected ';' outside a function")
		case token.RBrace:
			p.fail("unmatched '}'")
		}
	}
	if typ != nil || len(specs) > 0 {
		p.unexpected("a name")
	}
	if p.kind() == token.Ident && (p.kindAt(1) == token.Ident || p.kindAt(1) == token.Star) {
		p.fail("unknown type '%s' (not a type keyword or a visible type macro)", p.text(p.idx()))
	}
	p.fail("unexpected %s at top level; expected a declaration", p.describe(p.idx()))
	return nil
}

// endDecl expects the ';' ending a declaration, with the same missing-';'
// repair as statements. It returns the last token of the declaration.
func (p *parser) endDecl(start int) int {
	if p.kind() == token.Semicolon {
		return p.next()
	}
	p.missingSemi(start)
	return p.prevIdx()
}

func (p *parser) opaqueDecl(start int) Decl {
	p.next()
	if p.kind() == token.LParen {
		p.skipBalanced()
	}
	if p.kind() == token.LBrace {
		p.skipBalanced()
	} else if p.kind() == token.Semicolon {
		p.next()
	}
	p.file.Partial = true
	p.markResume()
	return &BadDecl{span: span{start, p.prevIdx()}, Opaque: true}
}

func (p *parser) parseInherit(si, start int, specs []Spec) Decl {
	d := &InheritDecl{span: span{start, start}, Private: hasSpec(specs, token.KwPrivate)}
	p.next() // inherit
	// A label is a plain identifier; a macro name expands and cannot be
	// one.
	if p.kind() == token.Ident && p.macroAt(0) == nil {
		switch p.kindAt(1) {
		case token.StringLit, token.KwObject, token.Ident, token.LParen:
			d.Label = p.ident()
		}
	}
	if p.kind() == token.KwObject {
		p.next()
	}
	pathStart := p.idx()
	d.Path = p.parseBinary(9) // string concatenation: '+' level and tighter
	// The path must be a string constant after preprocessing. With every
	// include resolved, an identifier that is not a macro cannot be one.
	// (Recorded without abandoning the declaration: the inherit is real,
	// only unresolvable.)
	if !p.partial && p.spec == 0 {
		for t := pathStart; t <= p.prevIdx(); t++ {
			if p.toks[t].Kind == token.Ident && p.view.Lookup(p.text(t)) == nil {
				p.errs = append(p.errs, &Error{
					Msg: fmt.Sprintf("inherit path must be a string constant, but '%s' is not a defined macro (missing #include?)", p.text(t)),
					Tok: t, Related: -1, RegionFirst: start, RegionLast: p.prevIdx(),
				})
				break
			}
		}
	}
	d.Last = p.endDecl(si)
	return d
}

func (p *parser) parseFunc(start int, specs []Spec, typ *TypeSpec, stars int) *FuncDecl {
	fn := &FuncDecl{span: span{start, start}, Specs: specs, Type: typ, Stars: stars}
	if p.kind() == token.KwOperator {
		fn.Operator = true
		fn.NameTok = p.next()
		for p.kind() != token.LParen {
			switch p.kind() {
			case token.EOF, token.Semicolon, token.LBrace, token.RBrace:
				p.unexpected("operator symbol")
			}
			p.next()
		}
	} else {
		fn.Name = p.ident()
		fn.NameTok = fn.Name.First
	}
	fn.LParen = p.expect(token.LParen)
	p.parseParams(fn)
	fn.RParen = p.expectClose(token.RParen, fn.LParen)
	fn.Last = fn.RParen

	switch p.kind() {
	case token.LBrace:
		saveName, saveBrace := p.fnName, p.fnBrace
		if fn.Name != nil {
			p.fnName = fn.Name.Name
		} else {
			p.fnName = "operator"
		}
		p.fnBrace = p.idx()
		fn.Body = p.parseBlock()
		p.fnName, p.fnBrace = saveName, saveBrace
		fn.Last = fn.Body.Last
	case token.Semicolon, token.Comma:
		if typ == nil {
			p.fail("function '%s' has no body (a prototype needs a return type)", p.text(fn.NameTok))
		}
	default:
		p.unexpected("'{' or ';' after function header")
	}
	return fn
}

func (p *parser) parseParams(fn *FuncDecl) {
	if p.kind() == token.RParen {
		return
	}
	if p.kind() == token.KwVoid && p.kindAt(1) == token.RParen {
		p.next()
		return
	}
	for {
		par := &Param{span: span{p.idx(), p.idx()}}
		if p.kind() == token.KwVarargs {
			par.Varargs = true
			p.next()
		}
		switch {
		case p.typeStartAt(0) && !(p.kind() == token.Ident &&
			(p.kindAt(1) == token.Comma || p.kindAt(1) == token.RParen || p.kindAt(1) == token.Ellipsis)):
			par.Type = p.parseType()
			par.Stars = p.parseStars()
			if p.kind() != token.Ident {
				p.unexpected("parameter name")
			}
			par.Name = p.ident()
		case p.kind() == token.Ident && p.kindAt(1) == token.Ident && p.partial && p.macroAt(0) == nil,
			p.kind() == token.Ident && p.kindAt(1) == token.Star && p.partial && p.macroAt(0) == nil:
			// Partial view: an unseen type macro.
			par.Type = &TypeSpec{Kind: token.Ident}
			par.Type.First = p.next()
			par.Type.Last = par.Type.First
			par.Stars = p.parseStars()
			par.Name = p.ident()
		case p.kind() == token.Ident:
			if m := p.macroAt(0); m != nil && m.Class != pp.ClassExpr {
				p.bailOpaque()
			}
			par.Name = p.ident()
		default:
			p.unexpected("parameter")
		}
		par.Last = p.prevIdx()
		fn.Params = append(fn.Params, par)
		if p.kind() == token.Ellipsis {
			p.next()
			fn.Ellipsis = true
			return
		}
		if p.kind() != token.Comma {
			return
		}
		p.next()
	}
}

// parseDeclarators parses `[*]* name [= init] (, ...)*` into vd.
func (p *parser) parseDeclarators(vd *VarDecl, top bool) {
	p.parseDeclaratorsFrom(vd, p.parseStars(), top)
}

func (p *parser) parseDeclaratorsFrom(vd *VarDecl, stars int, top bool) {
	for {
		if p.kind() != token.Ident {
			p.unexpected("variable name")
		}
		if p.kindAt(1) == token.LParen {
			// A prototype inside a declarator list.
			fn := p.parseFunc(p.idx(), vd.Specs, vd.Type, stars)
			if fn.Body != nil {
				p.failAt(fn.NameTok, -1, "", "function body in a declarator list")
			}
			vd.Protos = append(vd.Protos, fn)
		} else {
			d := &Declarator{span: span{p.idx(), p.idx()}, Stars: stars}
			d.Name = p.ident()
			if p.kind() == token.Assign {
				p.next()
				d.Init = p.parseExpr()
			}
			d.Last = p.prevIdx()
			vd.Vars = append(vd.Vars, d)
		}
		if p.kind() != token.Comma {
			return
		}
		p.next()
		stars = p.parseStars()
	}
}

// ------------------------------------------------------------ statements

func (p *parser) parseBlock() *BlockStmt {
	b := &BlockStmt{}
	b.First = p.expect(token.LBrace)
	for !p.abort {
		switch p.kind() {
		case token.RBrace:
			b.Last = p.next()
			return b
		case token.EOF:
			if p.spec > 0 {
				panic(specBail{})
			}
			line := p.f.Pos(p.toks[b.First].Off).Line
			p.errs = append(p.errs, &Error{
				Msg: fmt.Sprintf("missing '}' to close '{' opened on line %d", line),
				Tok: p.prevIdx(), AtPrevEnd: true, Related: b.First, RelatedMsg: "opened here",
				RegionFirst: b.First, RegionLast: p.prevIdx(), Severe: true,
			})
			b.Last = p.prevIdx()
			return b
		}
		start := p.i
		s := p.parseStmt()
		if p.i == start && !p.abort {
			p.i++ // guarantee progress
		}
		if s != nil {
			b.Stmts = append(b.Stmts, s)
		}
	}
	b.Last = p.prevIdx()
	return b
}

func (p *parser) parseStmt() (s Stmt) {
	start := p.i
	nerr := len(p.errs)
	resumed := p.resumes[p.idx()]
	defer func() {
		r := recover()
		if r == nil {
			// A statement read right after a skip may be the tail of
			// the construct that failed (`for (int i = 0; i < n; i++)`
			// resumes at `i < n;`). It parsed, but it is not evidence
			// of anything: hide it from the rules.
			if resumed && s != nil && !p.abort {
				if _, block := s.(*BlockStmt); !block {
					a, b := s.Span()
					s = &BadStmt{span: span{a, b}, Opaque: true}
				}
			}
			return
		}
		if _, ok := r.(stmtBail); !ok {
			panic(r)
		}
		opaque := p.opaque && len(p.errs) == nerr
		p.opaque = false
		eof := p.sync(start, false)
		// Guarantee progress, but never by eating the '}' that closes
		// the enclosing block: `if (x) y(); else }` is one error, not a
		// missing brace as well. (The block loop handles the '}'.)
		if p.i <= start && p.kind() != token.RBrace {
			p.i = start + 1
		}
		if p.i > start {
			p.markResume()
		}
		last := p.prevIdx()
		if !p.abort {
			for _, e := range p.errs[nerr:] {
				e.RegionFirst, e.RegionLast = p.tokAt(start), e.Tok
				if eof {
					e.Severe = true
				}
			}
		}
		s = &BadStmt{span: span{p.tokAt(start), maxInt(last, p.tokAt(start))}, Opaque: opaque}
	}()
	return p.stmt()
}

func canStartStmt(k token.Kind) bool {
	switch k {
	case token.Ident, token.LBrace, token.LParen, token.Inc, token.Dec, token.ColonColon,
		token.StringLit, token.IntLit:
		return true
	}
	return k.IsKeyword()
}

// missingSemi handles a statement or declaration that is not followed by
// ';'. If the next token begins a new line and can begin a statement, the
// ';' is assumed (one error, nothing skipped); otherwise the statement is
// abandoned.
func (p *parser) missingSemi(start int) {
	if p.abort {
		return
	}
	if p.spec > 0 {
		panic(specBail{})
	}
	prev, cur := p.prevIdx(), p.idx()
	if p.kind() == token.Illegal {
		p.bailOpaque()
	}
	if p.kind() == token.RBrace || p.kind() == token.EOF ||
		(canStartStmt(p.kind()) && p.newlineBetween(prev, cur)) {
		p.errs = append(p.errs, &Error{
			Msg: "missing ';' before " + p.describe(cur), Tok: prev, AtPrevEnd: true,
			Related: -1, RegionFirst: p.tokAt(start), RegionLast: prev,
		})
		return
	}
	p.fail("expected ';' before %s", p.describe(cur))
}

func (p *parser) endStmt(start int) {
	if p.kind() == token.Semicolon {
		p.next()
		return
	}
	p.missingSemi(start)
}

func (p *parser) stmt() Stmt {
	start := p.i
	first := p.idx()
	done := func() span { return span{first, p.prevIdx()} }

	switch k := p.kind(); k {
	case token.LBrace:
		return p.parseBlock()
	case token.Semicolon:
		p.next()
		return &EmptyStmt{span{first, first}}
	case token.KwIf:
		p.next()
		s := &IfStmt{}
		s.Cond = p.parenCond()
		s.Then = p.parseStmt()
		if p.kind() == token.KwElse && !p.abort {
			p.next()
			s.Else = p.parseStmt()
		}
		s.span = done()
		return s
	case token.KwElse:
		p.fail("'else' without a matching 'if'")
	case token.KwWhile:
		p.next()
		s := &WhileStmt{}
		s.Cond = p.parenCond()
		s.Body = p.parseStmt()
		s.span = done()
		return s
	case token.KwDo:
		p.next()
		s := &DoStmt{}
		s.Body = p.parseStmt()
		if p.abort {
			s.span = done()
			return s
		}
		p.expect(token.KwWhile)
		s.Cond = p.parenCond()
		p.endStmt(start)
		s.span = done()
		return s
	case token.KwFor:
		p.next()
		s := &ForStmt{}
		lp := p.expect(token.LParen)
		if p.kind() != token.Semicolon {
			s.Init = p.parseExprList()
		}
		p.expect(token.Semicolon)
		if p.kind() != token.Semicolon {
			s.Cond = p.parseExprList()
		}
		p.expect(token.Semicolon)
		if p.kind() != token.RParen {
			s.Post = p.parseExprList()
		}
		p.expectClose(token.RParen, lp)
		s.Body = p.parseStmt()
		s.span = done()
		return s
	case token.KwSwitch:
		p.next()
		s := &SwitchStmt{}
		s.Tag = p.parenCond()
		if p.kind() != token.LBrace {
			p.unexpected("'{' after switch (...)")
		}
		s.Body = p.parseBlock()
		s.span = done()
		return s
	case token.KwCase, token.KwDefault:
		s := &CaseStmt{}
		p.next()
		if k == token.KwDefault {
			s.Default = true
		} else {
			s.Lo = p.parseExpr()
			if p.kind() == token.DotDot {
				p.next()
				s.Hi = p.parseExpr()
			}
		}
		p.expect(token.Colon)
		if p.kind() == token.RBrace {
			p.fail("a case label must be followed by a statement (add ';')")
		}
		s.Body = p.parseStmt()
		s.span = done()
		return s
	case token.KwReturn:
		p.next()
		s := &ReturnStmt{}
		if p.kind() != token.Semicolon {
			s.X = p.parseExprList()
		}
		p.endStmt(start)
		s.span = done()
		return s
	case token.KwBreak:
		p.next()
		p.endStmt(start)
		return &BreakStmt{done()}
	case token.KwContinue:
		p.next()
		p.endStmt(start)
		return &ContinueStmt{done()}
	case token.KwGoto:
		p.next()
		s := &GotoStmt{Label: p.ident()}
		p.endStmt(start)
		s.span = done()
		return s
	case token.KwRlimits:
		p.next()
		s := &RlimitsStmt{}
		lp := p.expect(token.LParen)
		s.Stack = p.parseExprList()
		p.expect(token.Semicolon)
		s.Ticks = p.parseExprList()
		p.expectClose(token.RParen, lp)
		if p.kind() != token.LBrace {
			p.unexpected("'{' after rlimits (...)")
		}
		s.Body = p.parseBlock()
		s.span = done()
		return s
	case token.KwCatch:
		if p.kindAt(1) == token.LBrace {
			p.next()
			s := &CatchStmt{Body: p.parseBlock()}
			if p.kind() == token.Colon && !p.abort {
				p.next()
				s.Handler = p.parseStmt()
			}
			s.span = done()
			return s
		}
	case token.KwTry:
		p.next()
		s := &CatchStmt{}
		if p.kind() != token.LBrace {
			p.unexpected("'{' after try")
		}
		s.Body = p.parseBlock()
		p.expect(token.KwCatch)
		lp := p.expect(token.LParen)
		if p.kind() == token.Ellipsis {
			p.next()
		} else {
			p.ident()
		}
		p.expectClose(token.RParen, lp)
		if p.kind() != token.LBrace {
			p.unexpected("'{'")
		}
		s.Handler = p.parseBlock()
		s.span = done()
		return s
	case token.Illegal:
		p.bailOpaque()
	case token.Ident:
		if p.kindAt(1) == token.Colon {
			s := &LabelStmt{Name: p.ident()}
			p.next()
			if p.kind() != token.RBrace {
				s.Body = p.parseStmt()
			}
			s.span = done()
			return s
		}
		if m := p.macroAt(0); m != nil {
			switch m.Class {
			case pp.ClassStatement, pp.ClassEmpty:
				return p.macroStmt(m)
			case pp.ClassFragment:
				p.bailOpaque()
			case pp.ClassSpecifier:
				return p.localDecl(start)
			case pp.ClassType:
				if n := p.kindAt(1); n == token.Ident || n == token.Star {
					return p.localDecl(start)
				}
			}
		} else if p.partial && p.kindAt(1) == token.Ident {
			// Partial view: `status ok;` — an unseen type macro.
			switch p.kindAt(2) {
			case token.Semicolon, token.Comma, token.Assign:
				return p.localDecl(start)
			}
		}
	default:
		if isSpecKw(k) || pp.IsTypeKeyword(k) {
			return p.localDecl(start)
		}
	}

	s := &ExprStmt{X: p.parseExprList()}
	p.endStmt(start)
	s.span = done()
	return s
}

func (p *parser) parenCond() Expr {
	lp := p.expect(token.LParen)
	x := p.parseExprList()
	p.expectClose(token.RParen, lp)
	return x
}

func (p *parser) macroStmt(m *pp.Macro) Stmt {
	s := &MacroStmt{}
	s.First = p.idx()
	s.Name = p.ident()
	if p.kind() == token.LParen && (m.FuncLike || m.Class == pp.ClassStatement) {
		lp := p.idx()
		ok := p.try(func() {
			p.next()
			s.Args, _ = p.parseArgs()
			if s.Args == nil {
				s.Args = []Expr{}
			}
			p.expectClose(token.RParen, lp)
		})
		if !ok {
			s.Args = nil
			p.skipBalanced()
		}
	}
	if p.kind() == token.Semicolon {
		p.next()
	}
	s.Last = p.prevIdx()
	return s
}

// nestedFunc reports whether the cursor is at a function *definition*
// header: [specs] type [*]* name ( ... ) {
func (p *parser) nestedFunc() (name int, ok bool) {
	d := 0
	for isSpecKw(p.kindAt(d)) {
		d++
	}
	if !p.typeStartAt(d) {
		return 0, false
	}
	d++
	if p.kindAt(d-1) == token.KwObject && p.kindAt(d) == token.StringLit {
		d++
	}
	for p.kindAt(d) == token.Star {
		d++
	}
	if p.kindAt(d) != token.Ident || p.kindAt(d+1) != token.LParen {
		return 0, false
	}
	name = p.idxAt(d)
	depth := 0
	for j := d + 1; j < d+400; j++ {
		switch p.kindAt(j) {
		case token.LParen:
			depth++
		case token.RParen:
			depth--
			if depth == 0 {
				return name, p.kindAt(j+1) == token.LBrace
			}
		case token.EOF, token.Semicolon, token.LBrace, token.RBrace:
			return 0, false
		}
	}
	return 0, false
}

func (p *parser) localDecl(start int) Stmt {
	first := p.idx()
	if name, ok := p.nestedFunc(); ok && p.fnBrace > 0 {
		if p.spec > 0 {
			panic(specBail{})
		}
		line := p.f.Pos(p.toks[p.fnBrace].Off).Line
		p.errs = append(p.errs, &Error{
			Msg: fmt.Sprintf("function '%s' begins inside the body of '%s': missing '}' (body opened on line %d)",
				p.text(name), p.fnName, line),
			Tok: first, Related: p.fnBrace, RelatedMsg: "body opened here",
			RegionFirst: p.fnBrace, RegionLast: name,
			// The braces are unbalanced from here on: whatever follows
			// (often the same message for the next function) is a
			// consequence.
			Severe: true,
		})
		p.file.Partial = true
		p.abort, p.resume = true, start
		return nil
	}
	vd := &VarDecl{span: span{first, first}}
	vd.Specs = p.parseSpecs()
	if p.typeStartAt(0) {
		vd.Type = p.parseType()
	} else if p.kind() == token.Ident && p.kindAt(1) == token.Ident {
		vd.Type = &TypeSpec{Kind: token.Ident}
		vd.Type.First = p.next()
		vd.Type.Last = vd.Type.First
	} else {
		p.unexpected("type")
	}
	p.parseDeclarators(vd, false)
	p.endStmt(start)
	vd.Last = p.prevIdx()
	return &DeclStmt{span: vd.span, Decl: vd}
}

// ----------------------------------------------------------- expressions

func (p *parser) parseExprList() Expr {
	x := p.parseExpr()
	if p.kind() != token.Comma {
		return x
	}
	c := &CommaExpr{List: []Expr{x}}
	c.First, _ = x.Span()
	for p.kind() == token.Comma {
		p.next()
		c.List = append(c.List, p.parseExpr())
	}
	c.Last = p.prevIdx()
	return c
}

func isAssignOp(k token.Kind) bool {
	switch k {
	case token.Assign, token.PlusEq, token.MinusEq, token.StarEq, token.SlashEq,
		token.PercentEq, token.AmpEq, token.PipeEq, token.CaretEq, token.ShlEq, token.ShrEq:
		return true
	}
	return false
}

func (p *parser) parseExpr() Expr {
	l := p.parseCond()
	if !isAssignOp(p.kind()) {
		return l
	}
	a := &AssignExpr{Op: p.kind(), L: l}
	a.First, _ = l.Span()
	a.OpTok = p.next()
	a.R = p.parseExpr()
	a.Last = p.prevIdx()
	return a
}

func (p *parser) parseCond() Expr {
	c := p.parseBinary(1)
	if p.kind() != token.Question {
		return c
	}
	x := &CondExpr{Cond: c}
	x.First, _ = c.Span()
	p.next()
	x.Then = p.parseExprList()
	p.expect(token.Colon)
	x.Else = p.parseCond()
	x.Last = p.prevIdx()
	return x
}

func binPrec(k token.Kind) int {
	switch k {
	case token.LOr:
		return 1
	case token.LAnd:
		return 2
	case token.Pipe:
		return 3
	case token.Caret:
		return 4
	case token.Amp:
		return 5
	case token.EqEq, token.NotEq:
		return 6
	case token.Lt, token.Gt, token.LtEq, token.GtEq:
		return 7
	case token.Shl, token.Shr:
		return 8
	case token.Plus, token.Minus:
		return 9
	case token.Star, token.Slash, token.Percent:
		return 10
	}
	return 0
}

func (p *parser) parseBinary(min int) Expr {
	x := p.parseUnary()
	for {
		prec := binPrec(p.kind())
		if prec < min {
			return x
		}
		b := &BinaryExpr{Op: p.kind(), X: x}
		b.First, _ = x.Span()
		b.OpTok = p.next()
		b.Y = p.parseBinary(prec + 1)
		b.Last = p.prevIdx()
		x = b
	}
}

func (p *parser) parseUnary() Expr {
	first := p.idx()
	switch k := p.kind(); k {
	case token.Inc, token.Dec, token.Minus, token.Plus, token.Not, token.Tilde:
		p.next()
		x := p.parseUnary()
		return &UnaryExpr{span{first, p.prevIdx()}, k, x}
	case token.LParen:
		// Cast: ( type [*]* ) operand
		if pp.IsTypeKeyword(p.kindAt(1)) || (p.kindAt(1) == token.Ident && p.typeStartAt(1) &&
			(p.kindAt(2) == token.RParen || p.kindAt(2) == token.Star)) {
			lp := p.next()
			t := p.parseType()
			if t.Kind == token.KwObject && t.Path == nil && p.kind() == token.Ident && p.kindAt(1) == token.RParen {
				t.Path = p.ident() // (object PATH_MACRO) x
			}
			t.Stars = p.parseStars()
			p.expectClose(token.RParen, lp)
			x := p.parseUnary()
			return &CastExpr{span{first, p.prevIdx()}, t, x}
		}
	}
	x := p.parsePostfixOps()
	for p.kind() == token.Inc || p.kind() == token.Dec {
		k := p.kind()
		p.next()
		x = &PostfixExpr{span{first, p.prevIdx()}, k, x}
	}
	return x
}

func (p *parser) parsePostfixOps() Expr {
	first := p.idx()
	x := p.parsePrimary()
	for {
		switch p.kind() {
		case token.LBracket:
			lb := p.next()
			var lo Expr
			if p.kind() != token.DotDot {
				lo = p.parseExprList()
			}
			if p.kind() == token.DotDot {
				p.next()
				var hi Expr
				if p.kind() != token.RBracket {
					hi = p.parseExprList()
				}
				p.expectClose(token.RBracket, lb)
				x = &RangeExpr{span{first, p.prevIdx()}, x, lo, hi}
			} else {
				p.expectClose(token.RBracket, lb)
				x = &IndexExpr{span{first, p.prevIdx()}, x, lo}
			}
		case token.Arrow:
			p.next()
			a := &ArrowExpr{X: x}
			if p.kind() != token.Ident {
				p.unexpected("function name after '->'")
			}
			if p.kindAt(1) != token.LParen && p.macroAt(0) != nil {
				p.bailOpaque() // ob->QN with #define QN query_name()
			}
			a.Name = p.ident()
			if p.kind() != token.LParen {
				p.unexpected("'(' after '->" + a.Name.Name + "'")
			}
			lp := p.next()
			a.Args, a.Spread = p.parseArgs()
			p.expectClose(token.RParen, lp)
			a.span = span{first, p.prevIdx()}
			x = a
		case token.LArrow:
			p.next()
			if p.kind() == token.KwObject {
				p.next()
			}
			path := p.parsePrimary()
			x = &InstanceOfExpr{span{first, p.prevIdx()}, x, path}
		default:
			return x
		}
	}
}

// parseArgs parses call arguments up to (not including) ')'.
func (p *parser) parseArgs() (args []Expr, spread bool) {
	if p.kind() == token.RParen {
		return nil, false
	}
	for {
		args = append(args, p.parseExpr())
		if p.kind() == token.Ellipsis {
			p.next()
			return args, true
		}
		if p.kind() != token.Comma {
			return args, false
		}
		p.next()
	}
}

func (p *parser) isStringStart(d int) bool {
	switch p.kindAt(d) {
	case token.StringLit:
		return true
	case token.Ident:
		c, ok := p.classAt(d)
		return ok && c == pp.ClassString && p.kindAt(d+1) != token.LParen
	}
	return false
}

func (p *parser) parsePrimary() Expr {
	first := p.idx()
	switch k := p.kind(); k {
	case token.IntLit, token.FloatLit, token.CharLit, token.KwNil:
		p.next()
		return &BasicLit{span{first, first}, k}

	case token.StringLit:
		p.next()
		var x Expr = &BasicLit{span{first, first}, k}
		return p.adjacent(x)

	case token.Ident:
		if p.kindAt(1) == token.ColonColon {
			label := p.ident()
			p.next()
			return p.parseCall(first, label, true)
		}
		if p.kindAt(1) == token.LParen {
			return p.parseCall(first, nil, false)
		}
		if m := p.macroAt(0); m != nil {
			switch m.Class {
			case pp.ClassExpr, pp.ClassString:
				if m.FuncLike { // function-like macro named without a call
					p.bailOpaque()
				}
			default:
				p.bailOpaque()
			}
			id := p.ident()
			if m.Class == pp.ClassString {
				return p.adjacent(id)
			}
			return id
		}
		return p.ident()

	case token.ColonColon:
		p.next()
		if p.kindAt(1) == token.LParen {
			return p.parseCall(first, nil, true)
		}
		id := p.ident()
		id.First = first
		id.Name = "::" + id.Name
		return id

	case token.LParen:
		switch p.kindAt(1) {
		case token.LBrace:
			p.next()
			lb := p.next()
			a := &ArrayLit{}
			for p.kind() != token.RBrace {
				a.Elems = append(a.Elems, p.parseExpr())
				if p.kind() != token.Comma {
					break
				}
				p.next()
			}
			p.expectClose(token.RBrace, lb)
			p.expectClose(token.RParen, first)
			a.span = span{first, p.prevIdx()}
			return a
		case token.LBracket:
			p.next()
			lb := p.next()
			m := &MappingLit{}
			for p.kind() != token.RBracket {
				m.Keys = append(m.Keys, p.parseExpr())
				p.expect(token.Colon)
				m.Values = append(m.Values, p.parseExpr())
				if p.kind() != token.Comma {
					break
				}
				p.next()
			}
			p.expectClose(token.RBracket, lb)
			p.expectClose(token.RParen, first)
			m.span = span{first, p.prevIdx()}
			return m
		case token.Colon:
			p.next()
			p.next()
			c := &LegacyClosure{}
			for p.kind() != token.Colon {
				c.Elems = append(c.Elems, p.parseExpr())
				if p.kind() != token.Comma {
					break
				}
				p.next()
			}
			p.expect(token.Colon)
			p.expectClose(token.RParen, first)
			c.span = span{first, p.prevIdx()}
			return c
		case token.Star:
			if !p.f.Dialect.Closures {
				p.fail("unexpected '(*': function pointers need a driver built with CLOSURES")
			}
			p.next()
			p.next()
			fp := &FuncPtrExpr{Fun: p.parseUnary()}
			p.expectClose(token.RParen, first)
			lp := p.expect(token.LParen)
			fp.Args, _ = p.parseArgs()
			p.expectClose(token.RParen, lp)
			fp.span = span{first, p.prevIdx()}
			return fp
		}
		p.next()
		x := p.parseExprList()
		p.expectClose(token.RParen, first)
		return &ParenExpr{span{first, p.prevIdx()}, x}

	case token.Amp:
		if !p.f.Dialect.Closures {
			p.unexpected("")
		}
		p.next()
		fp := &FuncPtrExpr{}
		if p.kind() == token.LParen && p.kindAt(1) == token.Star {
			lp0 := p.next()
			p.next()
			fp.Fun = p.parseUnary()
			p.expectClose(token.RParen, lp0)
		} else {
			fp.Fun = p.ident()
		}
		lp := p.expect(token.LParen)
		fp.Args, _ = p.parseArgs()
		p.expectClose(token.RParen, lp)
		fp.span = span{first, p.prevIdx()}
		return fp

	case token.KwCatch:
		p.next()
		lp := p.expect(token.LParen)
		x := p.parseExprList()
		p.expectClose(token.RParen, lp)
		return &CatchExpr{span{first, p.prevIdx()}, x}

	case token.KwNew:
		p.next()
		if p.kind() == token.KwObject {
			p.next()
		}
		n := &NewExpr{Path: p.parsePrimary()}
		if p.kind() == token.LParen {
			lp := p.next()
			n.Args, _ = p.parseArgs()
			p.expectClose(token.RParen, lp)
		}
		n.span = span{first, p.prevIdx()}
		return n
	}
	if p.kind() == token.Illegal {
		p.bailOpaque()
	}
	p.unexpected("")
	return nil
}

// adjacent folds following string operands into an AdjacentStrings node
// (the pre-DGD implicit concatenation).
func (p *parser) adjacent(x Expr) Expr {
	if !p.isStringStart(0) {
		return x
	}
	a := &AdjacentStrings{Parts: []Expr{x}}
	a.First, _ = x.Span()
	for p.isStringStart(0) {
		t := p.next()
		if p.toks[t].Kind == token.StringLit {
			a.Parts = append(a.Parts, &BasicLit{span{t, t}, token.StringLit})
		} else {
			a.Parts = append(a.Parts, &Ident{span{t, t}, p.text(t)})
		}
	}
	a.Last = p.prevIdx()
	return a
}

func isAllCaps(s string) bool {
	letters := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			letters++
		case c == '_' || (c >= '0' && c <= '9'):
		default:
			return false
		}
	}
	return letters > 0
}

// parseCall parses name(args). The cursor is at the function name.
func (p *parser) parseCall(first int, label *Ident, scoped bool) Expr {
	c := &CallExpr{Scoped: scoped, Label: label}
	var m *pp.Macro
	if !scoped {
		m = p.macroAt(0)
	}
	c.Fun = p.ident()
	if p.kind() != token.LParen {
		p.unexpected("'(' after '::" + c.Fun.Name + "'")
	}
	if m != nil {
		switch m.Class {
		case pp.ClassExpr, pp.ClassString:
		case pp.ClassEmpty:
			if !m.FuncLike {
				p.bailOpaque()
			}
		default:
			p.bailOpaque()
		}
	}
	c.LParen = p.idx()
	tolerant := (m != nil && m.FuncLike) || (m == nil && p.partial && !scoped && isAllCaps(c.Fun.Name))
	if tolerant {
		ok := p.try(func() {
			p.next()
			c.Args, c.Spread = p.parseArgs()
			p.expectClose(token.RParen, c.LParen)
		})
		if !ok {
			c.Args, c.Opaque = nil, true
			p.skipBalanced()
			if p.kindAt(-1) != token.RParen {
				p.bailOpaque()
			}
		}
	} else {
		p.next()
		c.Args, c.Spread = p.parseArgs()
		p.expectClose(token.RParen, c.LParen)
	}
	c.span = span{first, p.prevIdx()}
	if m != nil && m.Class == pp.ClassString {
		return p.adjacent(c)
	}
	return c
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
