package pp

import (
	"strconv"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/token"
)

// maxMacroDepth bounds how far the evaluator follows macros that name
// macros.
const maxMacroDepth = 8

// EvalString evaluates a constant expression of the source as the
// preprocessor and compiler would and returns its value when that is a
// string: string literals, '+', parentheses, adjacent literals, and
// object-like macros that themselves evaluate. Anything else is unknown
// (false).
func (e *Env) EvalString(expr []Tok) (string, bool) {
	// Fast path: a single plain literal.
	if len(expr) == 1 && expr[0].Kind == token.StringLit {
		return Unquote(expr[0].Text)
	}
	v, ok := (&eval{env: e, toks: expr}).constant()
	if !ok || !v.isString {
		return "", false
	}
	return v.s, true
}

// Expansion returns the tokens that an object-like macro stands for, with
// enclosing parentheses removed and a body that only names another such
// macro followed to that macro's. It is nil when name is not an
// object-like macro with a usable body.
func (e *Env) Expansion(name string) []Tok {
	for depth := 0; depth <= maxMacroDepth; depth++ {
		m := e.Lookup(name)
		if m == nil || m.FuncLike || m.Class == ClassFragment {
			return nil
		}
		toks := unparen(m.Toks)
		if len(toks) != 1 || toks[0].Kind != token.Ident || e.Lookup(toks[0].Text) == nil {
			return toks
		}
		name = toks[0].Text
	}
	return nil
}

// unparen strips parentheses that enclose the whole of toks.
func unparen(toks []Tok) []Tok {
	for len(toks) >= 2 && toks[0].Kind == token.LParen && toks[len(toks)-1].Kind == token.RParen {
		depth := 0
		for i, t := range toks {
			switch t.Kind {
			case token.LParen:
				depth++
			case token.RParen:
				depth--
			}
			if depth == 0 && i < len(toks)-1 {
				return toks // the first '(' closes before the end
			}
		}
		toks = toks[1 : len(toks)-1]
	}
	return toks
}

// Unquote extracts a string literal's value. Literals with escapes beyond
// \\ and \" are rejected (never seen in function names or object paths).
func Unquote(lit string) (string, bool) {
	if len(lit) < 2 || lit[0] != '"' || lit[len(lit)-1] != '"' {
		return "", false
	}
	body := lit[1 : len(lit)-1]
	if !strings.ContainsRune(body, '\\') {
		return body, true
	}
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			b.WriteByte(body[i])
			continue
		}
		i++
		if i >= len(body) {
			return "", false
		}
		switch body[i] {
		case '\\', '"':
			b.WriteByte(body[i])
		default:
			return "", false
		}
	}
	return b.String(), true
}

// value is a constant: an integer or a string.
type value struct {
	n        int64
	s        string
	isString bool
}

// eval is the constant evaluator: one expression grammar for the
// conditions of #if lines and for the constant expressions of the source.
// Every production returns its value and whether the value is known; an
// unknown operand makes the result unknown except where the other operand
// decides it (0 && x, 1 || x). A token sequence that is not an expression
// at all sets bad.
type eval struct {
	env  *Env
	toks []Tok
	i    int
	// directive selects the reading of an #if line: `defined` is an
	// operator, a name is looked up as of the directive, and a name that
	// is certainly not defined is 0.
	directive bool
	depth     int
	bad       bool
}

// constant evaluates the whole token sequence.
func (ev *eval) constant() (value, bool) {
	v, ok := ev.ternary()
	if ev.bad || ev.i != len(ev.toks) {
		return value{}, false
	}
	return v, ok
}

func (ev *eval) kind() token.Kind {
	if ev.i < len(ev.toks) {
		return ev.toks[ev.i].Kind
	}
	return token.EOF
}

func (ev *eval) accept(k token.Kind) bool {
	if ev.kind() == k {
		ev.i++
		return true
	}
	return false
}

func (ev *eval) expect(k token.Kind) {
	if !ev.accept(k) {
		ev.bad = true
	}
}

func truth(b bool) value {
	if b {
		return value{n: 1}
	}
	return value{}
}

func (ev *eval) ternary() (value, bool) {
	c, ok := ev.binary(1)
	if !ev.accept(token.Question) {
		return c, ok
	}
	a, aok := ev.ternary()
	ev.expect(token.Colon)
	b, bok := ev.ternary()
	switch {
	case !ok || c.isString:
		return value{}, false
	case c.n != 0:
		return a, aok
	}
	return b, bok
}

func (ev *eval) binary(min int) (value, bool) {
	x, xok := ev.unary()
	for !ev.bad {
		op := ev.kind()
		prec := op.Precedence()
		if prec == 0 || prec < min {
			break
		}
		ev.i++
		y, yok := ev.binary(prec + 1)
		x, xok = apply(op, x, xok, y, yok)
	}
	return x, xok
}

// apply computes x op y.
func apply(op token.Kind, x value, xok bool, y value, yok bool) (value, bool) {
	// The logical operators are decided by one known operand.
	switch op {
	case token.LAnd:
		if (xok && !x.isString && x.n == 0) || (yok && !y.isString && y.n == 0) {
			return value{}, true
		}
	case token.LOr:
		if (xok && !x.isString && x.n != 0) || (yok && !y.isString && y.n != 0) {
			return value{n: 1}, true
		}
	}
	if !xok || !yok || x.isString != y.isString {
		return value{}, false
	}
	if x.isString {
		switch op {
		case token.Plus:
			return value{s: x.s + y.s, isString: true}, true
		case token.EqEq:
			return truth(x.s == y.s), true
		case token.NotEq:
			return truth(x.s != y.s), true
		}
		return value{}, false
	}
	a, b := x.n, y.n
	switch op {
	case token.LOr:
		return truth(a != 0 || b != 0), true
	case token.LAnd:
		return truth(a != 0 && b != 0), true
	case token.Pipe:
		return value{n: a | b}, true
	case token.Caret:
		return value{n: a ^ b}, true
	case token.Amp:
		return value{n: a & b}, true
	case token.EqEq:
		return truth(a == b), true
	case token.NotEq:
		return truth(a != b), true
	case token.Lt:
		return truth(a < b), true
	case token.Gt:
		return truth(a > b), true
	case token.LtEq:
		return truth(a <= b), true
	case token.GtEq:
		return truth(a >= b), true
	case token.Shl:
		if b < 0 || b > 63 {
			return value{}, false
		}
		return value{n: a << uint(b)}, true
	case token.Shr:
		if b < 0 || b > 63 {
			return value{}, false
		}
		return value{n: a >> uint(b)}, true
	case token.Plus:
		return value{n: a + b}, true
	case token.Minus:
		return value{n: a - b}, true
	case token.Star:
		return value{n: a * b}, true
	case token.Slash, token.Percent:
		if b == 0 {
			return value{}, false
		}
		if op == token.Slash {
			return value{n: a / b}, true
		}
		return value{n: a % b}, true
	}
	return value{}, false
}

func (ev *eval) unary() (value, bool) {
	switch op := ev.kind(); op {
	case token.Not, token.Tilde, token.Minus, token.Plus:
		ev.i++
		x, ok := ev.unary()
		if !ok || x.isString {
			return value{}, false
		}
		switch op {
		case token.Not:
			return truth(x.n == 0), true
		case token.Tilde:
			return value{n: ^x.n}, true
		case token.Minus:
			return value{n: -x.n}, true
		}
		return x, true
	}
	return ev.primary()
}

func (ev *eval) primary() (value, bool) {
	if ev.i >= len(ev.toks) {
		ev.bad = true
		return value{}, false
	}
	t := ev.toks[ev.i]
	ev.i++
	var v value
	ok := false
	switch t.Kind {
	case token.LParen:
		v, ok = ev.ternary()
		ev.expect(token.RParen)
	case token.IntLit:
		n, err := strconv.ParseInt(t.Text, 0, 64)
		v, ok = value{n: n}, err == nil
	case token.CharLit:
		v.n, ok = charValue(t.Text)
	case token.StringLit:
		v.isString = true
		v.s, ok = Unquote(t.Text)
	case token.Ident:
		v, ok = ev.name(t.Text)
	default:
		ev.bad = true
		return value{}, false
	}
	// Adjacent string literals concatenate.
	for v.isString && ev.kind() == token.StringLit {
		s, sok := Unquote(ev.toks[ev.i].Text)
		ev.i++
		v.s, ok = v.s+s, ok && sok
	}
	return v, ok
}

// name evaluates an identifier.
func (ev *eval) name(name string) (value, bool) {
	if ev.directive && name == "defined" {
		paren := ev.accept(token.LParen)
		if ev.kind() != token.Ident {
			ev.bad = true
			return value{}, false
		}
		defined, known := ev.env.Defined(ev.toks[ev.i].Text)
		ev.i++
		if paren {
			ev.expect(token.RParen)
		}
		return truth(defined), known
	}
	var m *Macro
	if ev.directive {
		var defined, known bool
		switch m, defined, known = ev.env.state(name); {
		case !known:
			m = nil
		case !defined:
			return value{}, !ev.call() // an undefined name is 0
		}
	} else {
		m = ev.env.Lookup(name)
	}
	if ev.call() || m == nil || m.FuncLike || m.Class == ClassFragment || ev.depth >= maxMacroDepth {
		return value{}, false
	}
	sub := &eval{env: ev.env, toks: m.Toks, directive: ev.directive, depth: ev.depth + 1}
	return sub.constant()
}

// call skips an argument list at the cursor, if there is one: a call is
// never a constant, but what follows it may still decide the expression.
func (ev *eval) call() bool {
	if ev.kind() != token.LParen {
		return false
	}
	depth := 0
	for ev.i < len(ev.toks) {
		switch ev.toks[ev.i].Kind {
		case token.LParen:
			depth++
		case token.RParen:
			depth--
		}
		ev.i++
		if depth == 0 {
			return true
		}
	}
	ev.bad = true
	return true
}

// charValue is the value of a character constant with no escape or a
// simple one.
func charValue(lit string) (int64, bool) {
	if len(lit) < 3 || lit[0] != '\'' || lit[len(lit)-1] != '\'' {
		return 0, false
	}
	switch body := lit[1 : len(lit)-1]; {
	case len(body) == 1 && body[0] != '\\':
		return int64(body[0]), true
	case len(body) == 2 && body[0] == '\\':
		switch body[1] {
		case 'n':
			return '\n', true
		case 't':
			return '\t', true
		case 'r':
			return '\r', true
		case '0':
			return 0, true
		case '\\', '\'', '"':
			return int64(body[1]), true
		}
	}
	return 0, false
}
