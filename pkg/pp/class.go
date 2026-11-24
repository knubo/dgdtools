package pp

import (
	"strings"

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

// Classify derives a macro's class from the tokens of its body.
func Classify(body []Tok) Class {
	if len(body) == 0 {
		return ClassEmpty
	}
	kind := func(i int) token.Kind { return body[i].Kind }
	n := len(body)

	// Specifiers and types.
	i := 0
	for i < n && kind(i).IsSpecifier() {
		i++
	}
	if i == n {
		return ClassSpecifier
	}
	if kind(i).IsType() {
		j := i + 1
		for j < n && kind(j) == token.Star {
			j++
		}
		if j == n {
			return ClassType
		}
		if kind(n-1) == token.Semicolon {
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
	for i := range body {
		k := kind(i)
		if (k.IsType() || k.IsSpecifier()) && !(i > 0 && kind(i-1) == token.LParen) {
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
		if i > 0 && isOperand(k) && isOperand(kind(i-1)) &&
			!(k == token.StringLit && kind(i-1) == token.StringLit) {
			if !startsStatement(kind(0)) {
				return ClassFragment // two operands in a row
			}
		}
	}
	if depth != 0 {
		return ClassFragment
	}
	first, last := kind(0), kind(n-1)
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
	if !startsExpr(first) || !endsExpr(last) || strayType {
		return ClassFragment
	}
	if allString {
		return ClassString
	}
	return ClassExpr
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

// startsExpr: tokens that can begin an expression.
func startsExpr(k token.Kind) bool {
	switch k {
	case token.Minus, token.Plus, token.Not, token.Tilde, token.Inc, token.Dec,
		token.Amp, token.Star, token.LParen, token.ColonColon,
		token.KwCatch, token.KwNew:
		return true
	}
	return isOperand(k)
}

// endsExpr: tokens that can end an expression.
func endsExpr(k token.Kind) bool {
	switch k {
	case token.RParen, token.RBracket, token.Inc, token.Dec:
		return true
	}
	return isOperand(k)
}
