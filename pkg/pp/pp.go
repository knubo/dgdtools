// Package pp understands just enough of the DGD preprocessor to let the
// tools work on source as written: which conditional branches are active,
// what each #define's body looks like (and therefore what syntactic role
// the macro plays), and which files are #included.
//
// It is deliberately not a preprocessor. Nothing is expanded; a macro is
// classified from its body and the parser treats the macro's name as a
// first-class token of that class.
package pp

import (
	"strings"

	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// Class is the syntactic role a macro plays where it is used.
type Class uint8

const (
	// ClassExpr: the body is a complete expression; the name parses as an
	// identifier (or a call, for function-like macros).
	ClassExpr Class = iota
	// ClassEmpty: the body is empty (`#define public`).
	ClassEmpty
	// ClassSpecifier: the body is only class specifiers.
	ClassSpecifier
	// ClassType: the body is a type (`#define status int`).
	ClassType
	// ClassString: the body is a string literal or a concatenation of them.
	ClassString
	// ClassStatement: the body is one or more statements; a use may stand
	// as a statement without a terminating ';'.
	ClassStatement
	// ClassFragment: the body is not a complete syntactic unit (unbalanced
	// brackets, dangling operator, partial declaration) or the macro has
	// conflicting definitions. Anything touching it is opaque.
	ClassFragment
)

var classNames = map[string]Class{
	"expression": ClassExpr, "empty": ClassEmpty, "specifier": ClassSpecifier,
	"type": ClassType, "string": ClassString, "statement": ClassStatement,
	"opaque": ClassFragment, "fragment": ClassFragment,
}

// ParseClass maps a config spelling to a Class.
func ParseClass(s string) (Class, bool) {
	c, ok := classNames[strings.ToLower(strings.TrimSpace(s))]
	return c, ok
}

func (c Class) String() string {
	switch c {
	case ClassExpr:
		return "expression"
	case ClassEmpty:
		return "empty"
	case ClassSpecifier:
		return "specifier"
	case ClassType:
		return "type"
	case ClassString:
		return "string"
	case ClassStatement:
		return "statement"
	}
	return "opaque"
}

// Macro is one #define.
type Macro struct {
	Name     string
	FuncLike bool
	Params   []string
	Body     string // comment-free, continuation-free, trimmed
	Class    Class
	Path     string // file containing the definition
	Off      uint32 // byte offset of the directive in that file
}

// Include is one active #include directive.
type Include struct {
	Name  string // target without delimiters
	Angle bool   // <name> rather than "name"
	Tok   int    // index of the directive token
	Off   uint32
}

// Raw returns the target with its delimiters.
func (i Include) Raw() string {
	if i.Angle {
		return "<" + i.Name + ">"
	}
	return `"` + i.Name + `"`
}

// Table holds one file's own active directives.
type Table struct {
	Path     string
	Macros   map[string]*Macro
	Includes []Include
}

// Selection is the single preprocessor configuration every consumer of a
// file agrees on.
type Selection struct {
	// Sig are the indexes of active significant tokens (no trivia, no
	// directives, no EOF).
	Sig []int
	// Dirs are the indexes of active directive tokens.
	Dirs []int
	// Conditional reports whether the file contains any #if family
	// directive.
	Conditional bool
}

// Select chooses the active branch of every conditional. `#if 0` and
// `#if 1` are decided, `#ifdef`/`#ifndef`/`#if defined(X)` of a name in
// defined are decided, and everything else takes the first branch.
func Select(f *token.File, defined map[string]bool) *Selection {
	sel := &Selection{}
	type frame struct {
		parentActive bool
		active       bool
		anyTaken     bool
	}
	var stack []frame
	active := true
	for i, t := range f.Tokens {
		if t.Kind == token.Directive {
			name, rest := SplitDirective(f.Text(t))
			switch name {
			case "if", "ifdef", "ifndef":
				sel.Conditional = true
				take := active && decide(name, rest, defined)
				stack = append(stack, frame{parentActive: active, active: take, anyTaken: take})
				active = take
				continue
			case "elif":
				if n := len(stack); n > 0 {
					fr := &stack[n-1]
					take := fr.parentActive && !fr.anyTaken && decide("if", rest, defined)
					fr.active, fr.anyTaken = take, fr.anyTaken || take
					active = take
				}
				continue
			case "else":
				if n := len(stack); n > 0 {
					fr := &stack[n-1]
					take := fr.parentActive && !fr.anyTaken
					fr.active, fr.anyTaken = take, true
					active = take
				}
				continue
			case "endif":
				if n := len(stack); n > 0 {
					active = stack[n-1].parentActive
					stack = stack[:n-1]
				}
				continue
			}
			if active {
				sel.Dirs = append(sel.Dirs, i)
			}
			continue
		}
		if !active || t.Kind.IsTrivia() || t.Kind == token.EOF {
			continue
		}
		sel.Sig = append(sel.Sig, i)
	}
	return sel
}

// decide evaluates a conditional when that is certain; otherwise true
// (first branch).
func decide(name, rest string, defined map[string]bool) bool {
	rest = strings.TrimSpace(rest)
	switch name {
	case "ifdef":
		return true
	case "ifndef":
		return !defined[firstWord(rest)]
	}
	switch rest {
	case "0":
		return false
	case "1":
		return true
	}
	neg := false
	r := rest
	if strings.HasPrefix(r, "!") {
		neg = true
		r = strings.TrimSpace(r[1:])
	}
	if strings.HasPrefix(r, "defined") {
		r = strings.TrimSpace(strings.TrimPrefix(r, "defined"))
		r = strings.TrimSuffix(strings.TrimPrefix(r, "("), ")")
		r = strings.TrimSpace(r)
		if isIdent(r) && neg {
			return !defined[r]
		}
	}
	return true
}

func firstWord(s string) string {
	for i := 0; i < len(s); i++ {
		if !isIdentByte(s[i]) {
			return s[:i]
		}
	}
	return s
}

func isIdentByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isIdent(s string) bool {
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isIdentByte(s[i]) {
			return false
		}
	}
	return true
}

// SplitDirective returns a directive's name ("define", "include", ...) and
// the rest of its logical line with comments removed and continuations
// joined.
func SplitDirective(text []byte) (name, rest string) {
	s := cleanDirective(text)
	s = strings.TrimSpace(strings.TrimPrefix(s, "#"))
	i := 0
	for i < len(s) && s[i] >= 'a' && s[i] <= 'z' {
		i++
	}
	return s[:i], s[i:]
}

// cleanDirective joins backslash-newline continuations and blanks out
// comments (string and character literals are respected).
func cleanDirective(text []byte) string {
	if !needsCleaning(text) {
		return string(text)
	}
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '\\' && i+1 < len(text) && (text[i+1] == '\n' || text[i+1] == '\r'):
			i++
			if text[i] == '\r' && i+1 < len(text) && text[i+1] == '\n' {
				i++
			}
			b.WriteByte(' ')
		case c == '/' && i+1 < len(text) && text[i+1] == '*':
			i += 2
			for i < len(text) && !(text[i] == '*' && i+1 < len(text) && text[i+1] == '/') {
				i++
			}
			i++
			b.WriteByte(' ')
		case c == '/' && i+1 < len(text) && text[i+1] == '/':
			i = len(text)
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(text) && text[j] != c {
				if text[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(text) {
				j = len(text) - 1
			}
			b.Write(text[i : j+1])
			i = j
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func needsCleaning(text []byte) bool {
	for i, c := range text {
		if c == '\\' || (c == '/' && i+1 < len(text) && (text[i+1] == '*' || text[i+1] == '/')) {
			return true
		}
	}
	return false
}

// Scan extracts the active #define and #include directives of f.
func Scan(f *token.File, sel *Selection) *Table {
	t := &Table{Path: f.Path, Macros: map[string]*Macro{}}
	for _, i := range sel.Dirs {
		tok := f.Tokens[i]
		name, rest := SplitDirective(f.Text(tok))
		switch name {
		case "define":
			m := parseDefine(rest, f.Dialect)
			if m == nil {
				continue
			}
			m.Path, m.Off = f.Path, tok.Off
			if old, ok := t.Macros[m.Name]; ok && (old.Class != m.Class || old.FuncLike != m.FuncLike) {
				m.Class = ClassFragment // conflicting definitions: trust neither
			}
			t.Macros[m.Name] = m
		case "undef":
			n := firstWord(strings.TrimSpace(rest))
			if old, ok := t.Macros[n]; ok {
				old.Class = ClassFragment // defined for part of the file only
			} else if n != "" {
				t.Macros[n] = &Macro{Name: n, Class: ClassFragment, Path: f.Path, Off: tok.Off}
			}
		case "include":
			rest = strings.TrimSpace(rest)
			if len(rest) < 2 {
				continue
			}
			var inc Include
			switch rest[0] {
			case '"':
				end := strings.IndexByte(rest[1:], '"')
				if end < 0 {
					continue
				}
				inc.Name = rest[1 : 1+end]
			case '<':
				end := strings.IndexByte(rest[1:], '>')
				if end < 0 {
					continue
				}
				inc.Name, inc.Angle = rest[1:1+end], true
			default:
				continue // macro-valued include: not resolvable here
			}
			inc.Tok, inc.Off = i, tok.Off
			t.Includes = append(t.Includes, inc)
		}
	}
	return t
}

func parseDefine(rest string, d token.Dialect) *Macro {
	rest = strings.TrimLeft(rest, " \t")
	n := firstWord(rest)
	if !isIdent(n) {
		return nil
	}
	m := &Macro{Name: n}
	rest = rest[len(n):]
	if strings.HasPrefix(rest, "(") {
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			return nil
		}
		m.FuncLike = true
		for _, p := range strings.Split(rest[1:end], ",") {
			if p = strings.TrimSpace(p); p != "" {
				m.Params = append(m.Params, p)
			}
		}
		rest = rest[end+1:]
	}
	m.Body = strings.TrimSpace(rest)
	m.Class = Classify(m.Body, d)
	return m
}

// Classify derives a macro's class from its body.
func Classify(body string, d token.Dialect) Class {
	if body == "" {
		return ClassEmpty
	}
	f := lexer.Lex("", []byte(body), d)
	var ks []token.Kind
	for _, t := range f.Tokens {
		if !t.Kind.IsTrivia() && t.Kind != token.EOF {
			ks = append(ks, t.Kind)
		}
	}
	if len(ks) == 0 {
		return ClassEmpty
	}

	// Specifiers and types.
	i := 0
	for i < len(ks) && isSpec(ks[i]) {
		i++
	}
	if i == len(ks) {
		return ClassSpecifier
	}
	if IsTypeKeyword(ks[i]) {
		j := i + 1
		for j < len(ks) && ks[j] == token.Star {
			j++
		}
		if j == len(ks) {
			return ClassType
		}
		if ks[len(ks)-1] == token.Semicolon {
			return ClassStatement // a declaration macro
		}
		return ClassFragment
	}
	if i > 0 {
		return ClassFragment // specifiers followed by something else
	}

	// Bracket balance; statement punctuation at depth 0.
	depth, semi := 0, false
	allString, strayType := true, false
	for n, k := range ks {
		if (IsTypeKeyword(k) || isSpec(k)) && !(n > 0 && ks[n-1] == token.LParen) {
			strayType = true // a type outside a cast: part of a declaration
		}
		switch k {
		case token.LParen, token.LBracket, token.LBrace:
			depth++
		case token.RParen, token.RBracket, token.RBrace:
			depth--
			if depth < 0 {
				return ClassFragment
			}
		case token.Semicolon:
			if depth == 0 {
				semi = true
			}
		case token.Illegal, token.Hash, token.HashHash:
			return ClassFragment
		}
		switch k {
		case token.StringLit, token.Plus, token.LParen, token.RParen:
		default:
			allString = false
		}
		if n > 0 && isOperand(k) && isOperand(ks[n-1]) &&
			!(k == token.StringLit && ks[n-1] == token.StringLit) {
			if !startsStatement(ks[0]) {
				return ClassFragment // two operands in a row
			}
		}
	}
	if depth != 0 {
		return ClassFragment
	}
	first, last := ks[0], ks[len(ks)-1]
	switch first {
	case token.KwElse, token.KwCase, token.KwDefault:
		return ClassFragment
	}
	if startsStatement(first) {
		return ClassStatement
	}
	if first == token.LBrace || semi || last == token.Semicolon {
		return ClassStatement
	}
	if isBinaryOnly(first) || endsOpen(last) || strayType {
		return ClassFragment
	}
	if allString {
		return ClassString
	}
	return ClassExpr
}

func isSpec(k token.Kind) bool {
	switch k {
	case token.KwPrivate, token.KwStatic, token.KwAtomic, token.KwNomask, token.KwVarargs:
		return true
	}
	return false
}

// IsTypeKeyword reports whether k is a DGD type keyword.
func IsTypeKeyword(k token.Kind) bool {
	switch k {
	case token.KwInt, token.KwFloat, token.KwString, token.KwObject,
		token.KwMapping, token.KwMixed, token.KwVoid, token.KwFunction:
		return true
	}
	return false
}

func isOperand(k token.Kind) bool {
	switch k {
	case token.Ident, token.IntLit, token.FloatLit, token.StringLit, token.CharLit, token.KwNil:
		return true
	}
	return false
}

func startsStatement(k token.Kind) bool {
	switch k {
	case token.KwIf, token.KwWhile, token.KwFor, token.KwDo, token.KwSwitch,
		token.KwReturn, token.KwBreak, token.KwContinue, token.KwGoto,
		token.KwRlimits, token.KwTry:
		return true
	}
	return false
}

// isBinaryOnly: tokens that cannot begin an expression.
func isBinaryOnly(k token.Kind) bool {
	switch k {
	case token.Minus, token.Plus, token.Not, token.Tilde, token.Inc, token.Dec,
		token.Amp, token.Star, token.LParen, token.ColonColon,
		token.Ident, token.IntLit, token.FloatLit, token.StringLit, token.CharLit,
		token.KwNil, token.KwCatch, token.KwNew:
		return false
	}
	return true
}

// endsOpen: tokens that cannot end an expression.
func endsOpen(k token.Kind) bool {
	switch k {
	case token.RParen, token.RBracket, token.Inc, token.Dec,
		token.Ident, token.IntLit, token.FloatLit, token.StringLit, token.CharLit,
		token.KwNil:
		return false
	}
	return true
}

// View is the set of macros visible in one file: its own table first, then
// the tables of everything it includes, then the force-included header's.
// A View is not safe for concurrent use (it memoizes lookups); the Tables
// it points at are immutable and shared.
type View struct {
	Layers []*Table
	// Complete is true when every #include reachable from the file was
	// resolved, so "not in the view" means "not a macro".
	Complete bool
	// Override holds configured classes (macros: in dgdtools.yml).
	Override map[string]Class

	cache map[string]*Macro
}

// Lookup returns the visible definition of name, or nil. A name with
// conflicting definitions across layers comes back as ClassFragment.
func (v *View) Lookup(name string) *Macro {
	if v == nil {
		return nil
	}
	if m, ok := v.cache[name]; ok {
		return m
	}
	var found *Macro
	if c, ok := v.Override[name]; ok {
		found = &Macro{Name: name, Class: c}
	} else {
		for _, t := range v.Layers {
			m := t.Macros[name]
			if m == nil {
				continue
			}
			if found == nil {
				found = m
				continue
			}
			if m.Class != found.Class || m.FuncLike != found.FuncLike {
				amb := *found
				amb.Class = ClassFragment
				found = &amb
				break
			}
		}
	}
	if v.cache == nil {
		v.cache = map[string]*Macro{}
	}
	v.cache[name] = found
	return found
}

// Defined reports whether name is a visible macro.
func (v *View) Defined(name string) bool { return v.Lookup(name) != nil }
