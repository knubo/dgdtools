package resolve

import (
	"slices"
	"sort"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/calls"
	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/syntax"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// SignatureHelp is what a call takes, for someone writing its
// arguments.
type SignatureHelp struct {
	// Name is the function called: the callee, or, in the arguments
	// that a call_other, a call_out or one of the lib's registrars
	// passes on, the function they name.
	Name string
	// Arg is the 0-based argument of that function the cursor is in.
	Arg int
	// Signatures are the declarations the call may be of: one, or those
	// an ambiguous ::name() could mean.
	Signatures []Signature
}

// Signature is one declaration of a function.
type Signature struct {
	// Label is the declaration as LPC: "varargs int query(string what,
	// mixed extra...)", a kfun's prototype, a macro's name and
	// parameters.
	Label string
	// Params are the bytes [start, end) of each parameter in Label.
	Params [][2]int
	// Active is the index of the parameter the cursor's argument is
	// for: the argument's own, the last one for every argument a
	// parameter with "..." takes; -1 when the function takes no such
	// argument.
	Active int
	// Doc is the comment above the declaration, or the kfun table's
	// line (Info.Doc).
	Doc string
	Origin
}

// Signature returns what the call takes whose argument list the byte
// offset off of the file is in, nil when off is in none or nothing is
// known of the function called. The call is the innermost one: in
// f(g(1, |), 2) it is g's.
//
// It is found in the tokens to the left of the cursor, not in the tree,
// so that it is there while the call is being written and the statement
// does not parse yet. What the callee is, is then asked as At asks it:
// name() is the file's function, the inherited one, the auto object's, a
// kfun; ::name() and label::name() the inherited one; x->name() the
// function of x when x is this_object() or a constant path, and nothing
// is said when x is not known. In a call that names a function to call
// (call_other, call_out, the project's registrars), the arguments that
// are passed on are that function's, when it can be told.
func (r *Resolver) Signature(off uint32) *SignatureHelp {
	f := &r.File
	if f.Tok == nil || f.Tree == nil || f.Tree.Sel == nil || f.Unit == nil || f.Snapshot == nil {
		return nil
	}
	toks, sig := f.Tok.Tokens, f.Tree.Sel.Sig
	// The tokens of the code that end at or before the cursor.
	n := sort.Search(len(sig), func(i int) bool { return toks[sig[i]].End > off })
	if n == 0 {
		return nil
	}
	body := r.bodyAround(sig[n-1])
	if body < 0 {
		return nil
	}
	open, commas, ok := r.callAround(n, body)
	if !ok {
		return nil
	}
	return r.signature(open, commas)
}

// bodyAround returns the index of the '{' that opens the body of the
// function the token at index tok is in; -1 when it is in none. Code is
// the body of a function; anything else is a declaration.
func (r *Resolver) bodyAround(tok int) int {
	for _, d := range r.Tree.Decls {
		if fn, ok := d.(*syntax.FuncDecl); ok && fn.Body != nil {
			if first, last := fn.Body.Span(); first < tok && tok <= last {
				return first
			}
		}
	}
	return -1
}

// callAround finds the call whose argument list is open after the first
// n compiled tokens, which are those of the body of a function that
// opens at token body: the innermost one. open is the index in the
// compiled tokens of its '(', which follows a name, and commas are the
// commas of its argument list so far.
func (r *Resolver) callAround(n, body int) (open int, commas []int, ok bool) {
	toks, sig := r.Tok.Tokens, r.Tree.Sel.Sig
	kind := func(i int) token.Kind {
		if i < 0 || sig[i] <= body {
			return token.EOF
		}
		return toks[sig[i]].Kind
	}

	// Back to the '(' that is open at the cursor and follows a name. An
	// opening bracket of another kind starts the count of commas again,
	// its commas being its own.
	depth, aggregate := 0, false
	for i := n - 1; i >= 0 && sig[i] > body; i-- {
		switch toks[sig[i]].Kind {
		case token.RParen, token.RBracket, token.RBrace:
			depth++
		case token.Semicolon:
			if depth == 0 {
				return 0, nil, false
			}
		case token.Comma:
			if depth == 0 {
				commas = append(commas, i)
			}
		case token.LBracket, token.LBrace:
			if depth > 0 {
				depth--
				continue
			}
			// ({ and ([ open an array and a mapping; a '{' alone opens
			// a block, where no call is being written.
			aggregate = kind(i-1) == token.LParen
			if !aggregate && toks[sig[i]].Kind == token.LBrace {
				return 0, nil, false
			}
			commas = nil
		case token.LParen:
			if depth > 0 {
				depth--
				continue
			}
			if !aggregate && kind(i-1) == token.Ident {
				slices.Reverse(commas)
				return i, commas, true
			}
			// Parentheses around an expression, a condition, the '('
			// of an aggregate.
			aggregate, commas = false, nil
		}
	}
	return 0, nil, false
}

// signature describes the call whose '(' is at index open of the
// compiled tokens, with the commas of its argument list that are before
// the cursor.
func (r *Resolver) signature(open int, commas []int) *SignatureHelp {
	toks, sig := r.Tok.Tokens, r.Tree.Sel.Sig
	text := func(i int) string { return string(r.Tok.Text(toks[sig[i]])) }
	kind := func(i int) token.Kind {
		if i < 0 {
			return token.EOF
		}
		return toks[sig[i]].Kind
	}
	name := text(open - 1)
	help := &SignatureHelp{Name: name, Arg: len(commas)}
	texts := &texts{r: r}

	switch kind(open - 2) {
	case token.Arrow:
		first, ok := r.operand(open - 3)
		if !ok {
			return nil
		}
		help.Signatures = r.signatures(r.lateDefs(name, first, open-3), help.Arg, texts)
	case token.ColonColon:
		if kind(open-3) == token.Ident {
			help.Signatures = r.signatures(r.defs(name, r.inherited(name, text(open-3))), help.Arg, texts)
		} else {
			defs := r.defs(name, r.inherited(name, ""))
			if len(defs) == 0 {
				defs = r.auto(name, r.u.LibPath)
			}
			help.Signatures = r.signatures(defs, help.Arg, texts)
		}
		if len(help.Signatures) == 0 {
			help.Signatures = r.kfunSignature(name, help.Arg)
		}
	default:
		if m := r.Env.Lookup(name); m != nil {
			// A macro with parameters is called like a function.
			def := r.Env.Definition(name)
			if def == nil || !def.FuncLike {
				return nil
			}
			s := Signature{Label: def.Name + "(" + strings.Join(def.Params, ", ") + ")", Origin: r.origin(def.Path)}
			s.Params = index.ParamSpans(s.Label)
			s.Active = active(len(s.Params), false, help.Arg)
			s.Line, s.Doc = texts.declared(def.Path, def.Off, s.Profile != "")
			help.Signatures = []Signature{s}
			return help
		}
		// The arguments a registrar passes on are those of the function
		// it names.
		if fn := r.passedOn(name, open, commas); fn != nil {
			if sigs := r.signatures(fn.defs, help.Arg-fn.first, texts); len(sigs) > 0 {
				return &SignatureHelp{Name: fn.name, Arg: help.Arg - fn.first, Signatures: sigs}
			}
		}
		help.Signatures = r.signatures(r.function(name), help.Arg, texts)
		if len(help.Signatures) == 0 {
			help.Signatures = r.kfunSignature(name, help.Arg)
		}
	}
	if len(help.Signatures) == 0 {
		return nil
	}
	return help
}

// signatures writes the declarations of a function as signatures for
// the argument at index arg.
func (r *Resolver) signatures(defs []Def, arg int, texts *texts) []Signature {
	var out []Signature
	for i := range defs {
		d := &defs[i]
		if d.Func == nil || arg < 0 {
			continue
		}
		s := Signature{Label: d.Func.Decl, Params: index.ParamSpans(d.Func.Decl), Origin: r.origin(d.Path)}
		s.Auto = d.Auto
		s.Active = active(len(s.Params), d.Func.MaxArgs < 0, arg)
		s.Line, s.Doc = texts.declared(d.Path, d.Start, s.Profile != "")
		out = append(out, s)
	}
	return out
}

// kfunSignature is the signature of the kfun called name, when a call
// of name is known to reach it (kfun).
func (r *Resolver) kfunSignature(name string, arg int) []Signature {
	kf := r.kfun(name)
	if kf == nil {
		return nil
	}
	info := kfunInfo(kf)
	s := Signature{Label: kf.Decl, Params: index.ParamSpans(kf.Decl), Doc: info.Doc, Origin: info.Origin}
	s.Active = active(len(s.Params), kf.Ellipsis, arg)
	return []Signature{s}
}

// active returns the parameter, of n, that the argument at index arg is
// for; -1 when there is none.
func active(n int, ellipsis bool, arg int) int {
	switch {
	case arg < n:
		return arg
	case ellipsis && n > 0:
		return n - 1
	}
	return -1
}

// passed is a function that a call names for another to call, with
// arguments that are passed on to it.
type passed struct {
	name string
	defs []Def
	// first is the argument of the call that is the function's first.
	first int
}

// passedOn reads the call of name whose '(' is at index open as one
// that names a function to call and passes arguments on to it, when the
// cursor is in one of those: call_other(x, "fn", |), call_out("fn", 1,
// |), a registrar of the project. The call is read by the first form it
// fits, as calls.Extract reads it. It is nil when the call is none, the
// cursor is in an argument that is the call's own, or the function or
// the object it is called in cannot be told.
func (r *Resolver) passedOn(name string, open int, commas []int) *passed {
	toks, sig := r.Tok.Tokens, r.Tree.Sel.Sig
	// arg returns the tokens of the argument at index i, which is one
	// before the cursor's.
	arg := func(i int) (first, last int) {
		first = open + 1
		if i > 0 {
			first = commas[i-1] + 1
		}
		return first, commas[i] - 1
	}
	for _, form := range r.registry().Forms(name) {
		if form.Function >= len(commas) || (form.Object != nil && *form.Object >= len(commas)) {
			continue
		}
		first, last := arg(form.Function)
		if first != last || toks[sig[first]].Kind != token.StringLit {
			continue
		}
		fn, ok := pp.Unquote(string(r.Tok.Text(toks[sig[first]])))
		if !ok || !token.IsIdent(fn) {
			continue
		}
		if form.Args == nil || len(commas) < *form.Args {
			return nil
		}
		p := &passed{name: fn, first: *form.Args}
		if form.Object == nil {
			p.defs = r.function(fn)
		} else if first, last := arg(*form.Object); first <= last {
			p.defs = r.lateDefs(fn, first, last)
		}
		if len(p.defs) == 0 {
			return nil
		}
		return p
	}
	return nil
}

// lateDefs returns the function called name of the object that the
// tokens first..last of the compiled code denote, as a call bound when
// it runs reaches it: the file's own for this_object(), the function of
// the object's chain for a constant path, nothing for an object that is
// not known.
func (r *Resolver) lateDefs(name string, first, last int) []Def {
	switch target, path := r.target(first, last); target {
	case calls.TargetSelf:
		return r.function(name)
	case calls.TargetPath:
		return r.callable(path, name)
	}
	return nil
}

// operand returns the index of the first token of the operand that
// ends at index last of the compiled tokens and stands before a "->": a
// string, a name, a call name(...), or an expression in parentheses. It
// is false for anything else, and for a call that is itself made in
// another object or scope (a->b()->c()).
func (r *Resolver) operand(last int) (first int, ok bool) {
	toks, sig := r.Tok.Tokens, r.Tree.Sel.Sig
	if last < 0 {
		return 0, false
	}
	switch toks[sig[last]].Kind {
	case token.StringLit, token.Ident:
		return last, true
	case token.RParen:
		depth := 0
		for i := last; i >= 0; i-- {
			switch toks[sig[i]].Kind {
			case token.RParen:
				depth++
			case token.LParen:
				if depth--; depth > 0 {
					continue
				}
				if i == 0 || toks[sig[i-1]].Kind != token.Ident {
					return i, true
				}
				if i > 1 && (toks[sig[i-2]].Kind == token.Arrow || toks[sig[i-2]].Kind == token.ColonColon) {
					return 0, false
				}
				return i - 1, true
			}
		}
	}
	return 0, false
}

// target says what object the expression that is the tokens first..last
// of the compiled code denotes: this_object(), written so or as a macro;
// a constant path, a string or what evaluates to one; or one that is not
// known. It reads the tokens as calls.Extract reads the tree of a
// receiver, and says the same.
func (r *Resolver) target(first, last int) (calls.Target, string) {
	toks, sig := r.Tok.Tokens, r.Tree.Sel.Sig
	kind := func(i int) token.Kind { return toks[sig[i]].Kind }
	// Parentheses around the whole of it say nothing.
	for last > first && kind(first) == token.LParen && kind(last) == token.RParen {
		depth, whole := 0, true
		for i := first; i < last; i++ {
			switch kind(i) {
			case token.LParen:
				depth++
			case token.RParen:
				depth--
			}
			if depth == 0 {
				whole = false
				break
			}
		}
		if !whole {
			break
		}
		first, last = first+1, last-1
	}
	if first > last {
		return calls.TargetUnknown, ""
	}
	text := func(i int) string { return string(r.Tok.Text(toks[sig[i]])) }
	switch {
	case last == first+2 && kind(first) == token.Ident && kind(first+1) == token.LParen && kind(last) == token.RParen:
		if text(first) == "this_object" {
			return calls.TargetSelf, ""
		}
		return calls.TargetUnknown, ""
	case last == first && kind(first) == token.Ident:
		body := r.Env.Expansion(text(first))
		if body == nil {
			return calls.TargetUnknown, ""
		}
		if len(body) == 3 && body[0].Kind == token.Ident && body[0].Text == "this_object" &&
			body[1].Kind == token.LParen && body[2].Kind == token.RParen {
			return calls.TargetSelf, ""
		}
	}
	// A call is no constant, whatever a macro of its name may stand for.
	for i := first; i < last; i++ {
		if kind(i) == token.Ident && kind(i+1) == token.LParen {
			return calls.TargetUnknown, ""
		}
	}
	if r.u.LibPath == "" {
		return calls.TargetUnknown, ""
	}
	s, ok := r.Env.EvalString(pp.FileTokens(r.Tok, sig[first], sig[last]))
	if !ok || s == "" {
		return calls.TargetUnknown, ""
	}
	return calls.TargetPath, r.s.ObjectPath(s, r.u.LibPath)
}
