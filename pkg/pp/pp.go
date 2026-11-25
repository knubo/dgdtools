// Package pp is the tools' model of the DGD preprocessor: enough of it to
// work on source as written.
//
// There is one model. An Env is the ordered #define environment of a
// translation unit: what the driver and the project predefine, then the
// definitions of every file read so far, in the order they take effect.
// Env.Scan reads one file's directives into it, in order, and with them
// decides which conditional branches are active; an #include met on the
// way adds the included files' definitions at that point. The same
// environment then answers what a name is where it is used (Lookup), and
// the one constant evaluator (Env.EvalString, and the conditions of #if)
// reads macro values from it.
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

// Tok is one token of a macro body or of a constant expression.
type Tok struct {
	Kind token.Kind
	Text string
}

// Tokens lexes text (a macro body, the rest of an #if line) into its
// significant tokens.
func Tokens(text string, d token.Dialect) []Tok {
	if text == "" {
		return nil
	}
	f := lexer.Lex("", []byte(text), d)
	toks := make([]Tok, 0, len(f.Tokens))
	for _, t := range f.Tokens {
		if !t.Kind.IsTrivia() && t.Kind != token.EOF {
			toks = append(toks, Tok{t.Kind, text[t.Off:t.End]})
		}
	}
	return toks
}

// FileTokens returns the significant tokens of f.Tokens[first..last],
// directives excluded: the tokens of an expression in the source.
func FileTokens(f *token.File, first, last int) []Tok {
	var toks []Tok
	for i := first; i <= last && i < len(f.Tokens); i++ {
		if t := f.Tokens[i]; !t.Kind.IsTrivia() && t.Kind != token.EOF && t.Kind != token.Directive {
			toks = append(toks, Tok{t.Kind, string(f.Text(t))})
		}
	}
	return toks
}

// Macro is one #define.
type Macro struct {
	Name     string
	FuncLike bool
	Params   []string
	Body     string // comment-free, continuation-free, trimmed
	Toks     []Tok  // the body's tokens
	Class    Class
	Path     string // file containing the definition
	Off      uint32 // byte offset of the directive in that file
	// Undefined is set when the file #undef'd the name after this
	// definition and did not define it again: it is a macro for only part
	// of the file, and its Class is ClassFragment.
	Undefined bool
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
	// Partial is set by `#pragma dgdtools partial`, which a profile stub
	// uses to say that the file it stands for could not be fully read.
	Partial bool
	// Undefs are the names this file #undef'd. A definition that follows
	// an #undef deliberately replaces whatever an include defined.
	Undefs map[string]bool
	// Unsure are the names this file #defines or #undefs inside a
	// conditional that could not be decided, in whichever branch: whether
	// the file leaves them defined, and as what, is not known.
	Unsure map[string]bool
}

// Predefined are the macros the DGD preprocessor defines itself.
var Predefined = []string{"__DGD__", "__FILE__", "__LINE__", "__DATE__", "__TIME__"}

// Directives are the names the DGD preprocessor knows after '#'. Any
// other is its "undefined control" error.
var Directives = []string{
	"define", "elif", "else", "endif", "error", "if", "ifdef", "ifndef",
	"include", "line", "pragma", "undef",
}

// SplitDirective returns a directive's name ("define", "include", ...) and
// the rest of its logical line with comments removed and continuations
// joined.
func SplitDirective(text []byte) (name, rest string) {
	s := CleanDirective(text)
	s = strings.TrimSpace(strings.TrimPrefix(s, "#"))
	i := 0
	for i < len(s) && s[i] >= 'a' && s[i] <= 'z' {
		i++
	}
	return s[:i], s[i:]
}

// CleanDirective joins backslash-newline continuations and blanks out
// comments (string and character literals are respected).
func CleanDirective(text []byte) string {
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

// firstWord returns the identifier s begins with.
func firstWord(s string) string {
	for i := 0; i < len(s); i++ {
		if !token.IsIdentByte(s[i]) {
			return s[:i]
		}
	}
	return s
}

// parseDefine reads the rest of a #define line: the one reader of macro
// definitions.
func parseDefine(rest string, d token.Dialect) *Macro {
	rest = strings.TrimLeft(rest, " \t")
	n := firstWord(rest)
	if !token.IsIdent(n) {
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
	m.Toks = Tokens(m.Body, d)
	m.Class = Classify(m.Toks)
	return m
}

// parseInclude reads the rest of an #include line. A target that is not
// written out ("..." or <...>) is not resolvable here.
func parseInclude(rest string) (inc Include, ok bool) {
	rest = strings.TrimSpace(rest)
	if len(rest) < 2 {
		return inc, false
	}
	closer := byte('"')
	switch rest[0] {
	case '"':
	case '<':
		closer, inc.Angle = '>', true
	default:
		return inc, false
	}
	end := strings.IndexByte(rest[1:], closer)
	if end < 0 {
		return inc, false
	}
	inc.Name = rest[1 : 1+end]
	return inc, true
}
