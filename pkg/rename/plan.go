package rename

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/resolve"
	"github.com/mmcdole/dgdtools/pkg/syntax"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// what names a kind of symbol in a message.
func what(k resolve.Kind) string {
	switch k {
	case resolve.Local:
		return "local variable"
	case resolve.Parameter:
		return "parameter"
	case resolve.Function:
		return "function"
	case resolve.Variable:
		return "global variable"
	case resolve.Macro:
		return "macro"
	case resolve.Label:
		return "inherit label"
	}
	return k.String()
}

// valid refuses a new name that is no name, a keyword, or a macro.
func (rn *renamer) valid(sym Symbol, n string) error {
	if !token.IsIdent(n) {
		return refuse("'%s' is not a name: a name is letters, digits and '_', and does not begin with a digit", n)
	}
	if token.Keywords[n] != 0 || n == "defined" && sym.Kind == resolve.Macro {
		return refuse("'%s' is a keyword", n)
	}
	if rn.env != nil {
		if def := rn.env.Definition(n); def != nil && !def.Undefined {
			return refuse("'%s' is a macro here (#define in %s)", n, rn.libName(def.Path))
		}
		if defined, _ := rn.env.Defined(n); defined || rn.env.Lookup(n) != nil || rn.env.Outside(n) {
			return refuse("'%s' is a macro here (the driver's or the project's configuration's)", n)
		}
	}
	return nil
}

// hit reports whether a place At classified names the symbol.
func hit(at *resolve.Ref, sym Symbol) bool {
	if at == nil || at.Kind != sym.Kind {
		return false
	}
	return slices.ContainsFunc(at.Defs, func(d resolve.Def) bool { return d.Path == sym.Def.Path && d.Start == sym.Def.Start })
}

// describe says what a place At classified names, for a message.
func (rn *renamer) describe(at *resolve.Ref) string {
	if at == nil || len(at.Defs) == 0 {
		return "names nothing the tools know of"
	}
	d := at.Defs[0]
	return fmt.Sprintf("names the %s '%s' (%s)", what(at.Kind), at.Name, rn.where(d))
}

// planNames plans the rename of a local, a parameter, a private function
// or variable, or a label: the occurrences of the old name, and the
// checks of the new one.
func (rn *renamer) planNames(plan *Plan) error {
	sym, old, n := plan.Symbol, plan.Symbol.Name, plan.New
	own, toks := rn.tok.Path, rn.tok.Tokens

	// reach: where the old name may be the symbol; area: where code
	// that cannot be read, and the new name, matter.
	lo, hi := uint32(0), uint32(len(rn.tok.Src))
	areaLo, areaHi := lo, hi
	var scope *syntax.Scope // a local's
	if sym.Kind == resolve.Local || sym.Kind == resolve.Parameter {
		scope = rn.declScope(sym.Def.Local.Ident)
		if scope == nil {
			// A parameter of a prototype: its name is all there is.
			lo, hi = sym.Def.Start, sym.Def.End
			areaLo, areaHi = lo, hi
		} else {
			lo, hi = sym.Def.Start, scope.End
			first, last := scope.Func.Span()
			areaLo, areaHi = toks[first].Off, toks[last].End
		}
	}
	tokens := func(from, to uint32, name string, visit func(ti int, tok token.Token) error) error {
		for ti := rn.tok.TokenAt(from); ti < len(toks) && toks[ti].Off <= to; ti++ {
			if tok := toks[ti]; tok.Kind == token.Ident && tok.Off >= from && rn.text(ti) == name {
				if err := visit(ti, tok); err != nil {
					return err
				}
			}
		}
		return nil
	}

	type warning struct {
		off  uint32
		text string
	}
	var warnings []warning
	warn := func(off uint32, text string) { warnings = append(warnings, warning{off, text}) }
	defer func() {
		slices.SortStableFunc(warnings, func(a, b warning) int { return cmp.Compare(a.off, b.off) })
		for _, w := range warnings {
			plan.Warnings = append(plan.Warnings, w.text)
		}
	}()

	// The old name.
	err := tokens(lo, hi, old, func(ti int, tok token.Token) error {
		o := Occurrence{Path: own, Start: tok.Off, End: tok.End}
		switch {
		case !rn.compiled(ti):
			o.Class, o.Reason = Possible, "in code that is not compiled here (a branch of #if that is not taken)"
		case rn.unparsed(ti) != "":
			o.Class, o.Reason = Possible, rn.unparsed(ti)
		default:
			at := rn.r.At(tok.Off)
			switch {
			case hit(at, sym) && at.Call != nil:
				o.Class, o.Reason = Excluded, "DGD does not call a private function by name ("+via(at)+")"
				warn(tok.Off, fmt.Sprintf("line %d: ->%s cannot reach the private function '%s' (call_other does not), and is left as it is", rn.line(tok.Off), old, old))
			case hit(at, sym):
				o.Class, o.Decl = Certain, at.Decl
			default:
				o.Class, o.Reason = Excluded, rn.describe(at)
			}
		}
		plan.Occurrences = append(plan.Occurrences, o)
		return nil
	})
	if err != nil {
		return err
	}
	if sym.Kind == resolve.Function {
		rn.callbacks(plan, warn)
	}
	for _, o := range plan.Occurrences {
		if o.Class == Possible {
			return refuse("'%s' at line %d is %s: it may be the %s being renamed", old, rn.line(o.Start), o.Reason, what(sym.Kind))
		}
	}
	// Code that cannot be read around the symbol, which names the new
	// name: what it means there cannot be told.
	err = tokens(areaLo, areaHi, n, func(ti int, tok token.Token) error {
		switch {
		case !rn.compiled(ti):
			return refuse("'%s' is at line %d, in code that is not compiled here (a branch of #if that is not taken): what it names there cannot be told", n, rn.line(tok.Off))
		case rn.unparsed(ti) != "":
			return refuse("'%s' is at line %d, %s: what it names there cannot be told", n, rn.line(tok.Off), rn.unparsed(ti))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := rn.throughMacros(plan, lo, hi); err != nil {
		return err
	}
	if err := rn.stringized(plan); err != nil {
		return err
	}

	switch sym.Kind {
	case resolve.Local, resolve.Parameter:
		err = rn.localClashes(plan, scope)
	case resolve.Variable:
		err = rn.variableClashes(plan, tokens)
	case resolve.Function:
		err = rn.functionClashes(plan, tokens)
	case resolve.Label:
		err = rn.labelClashes(plan, tokens)
	}
	if err != nil {
		return err
	}
	slices.SortFunc(plan.Occurrences, func(a, b Occurrence) int { return cmp.Compare(a.Start, b.Start) })
	return nil
}

// callbacks lists the strings that name a private function in a call
// DGD makes by name (call_out("fn"), call_other(this_object(), "fn"), a
// registrar's): they do not reach it, and are excluded, with a warning.
func (rn *renamer) callbacks(plan *Plan, warn func(off uint32, text string)) {
	sym := plan.Symbol
	quoted := `"` + sym.Name + `"`
	for _, ti := range rn.tree.Sel.Sig {
		tok := rn.tok.Tokens[ti]
		if tok.Kind != token.StringLit || rn.text(ti) != quoted {
			continue
		}
		at := rn.r.At(tok.Off + 1)
		if !hit(at, sym) || at.Call == nil {
			continue
		}
		plan.Occurrences = append(plan.Occurrences, Occurrence{Path: rn.tok.Path, Start: at.Start, End: at.End,
			Class: Excluded, Reason: "DGD does not call a private function by name (" + via(at) + ")"})
		warn(tok.Off, fmt.Sprintf("line %d: %s(\"%s\") cannot reach the private function '%s' (DGD calls no private function by name), and is left as it is",
			rn.line(tok.Off), at.Call.Via, sym.Name, sym.Name))
	}
}

// throughMacros refuses when a macro used in [lo, hi] names the old name
// or the new one in its body: the macro's uses would mean another thing
// after the rename, or not be renamed.
func (rn *renamer) throughMacros(plan *Plan, lo, hi uint32) error {
	if rn.env == nil {
		return nil
	}
	old, n := plan.Symbol.Name, plan.New
	done := map[string]bool{}
	for _, ti := range rn.tree.Sel.Sig {
		tok := rn.tok.Tokens[ti]
		if tok.Off < lo || tok.Off > hi || tok.Kind != token.Ident {
			continue
		}
		name := rn.text(ti)
		if done[name] || rn.env.Lookup(name) == nil {
			continue
		}
		done[name] = true
		exp := map[string]bool{}
		rn.expansion(name, map[string]bool{}, exp)
		switch {
		case exp[old]:
			return refuse("'%s' is named in the body of the macro %s, used at line %d: the macro would have to change with it", old, name, rn.line(tok.Off))
		case exp[n]:
			return refuse("the macro %s, used at line %d, names '%s' in its body: it would name the renamed %s there", name, rn.line(tok.Off), n, what(plan.Symbol.Kind))
		}
	}
	return nil
}

// stringized refuses when an occurrence to rename is an argument of a
// macro that makes a string of its arguments or pastes them (# and ##):
// the string would change with the name.
func (rn *renamer) stringized(plan *Plan) error {
	if rn.env == nil {
		return nil
	}
	for _, o := range plan.Occurrences {
		if o.Class != Certain || o.Decl {
			continue
		}
		ti := rn.tok.TokenAt(o.Start)
		if !rn.compiled(ti) {
			continue
		}
		for _, node := range syntax.PathAt(rn.tree, ti) {
			c, ok := node.(*syntax.CallExpr)
			if !ok || c.Scoped || c.Fun == nil || c.Fun.Name == plan.Symbol.Name {
				continue
			}
			if m := rn.env.Lookup(c.Fun.Name); m != nil && m.FuncLike && strings.Contains(m.Body, "#") {
				return refuse("'%s' at line %d is an argument of the macro %s, which makes a string of its arguments or pastes them (# or ##)", plan.Symbol.Name, rn.line(o.Start), c.Fun.Name)
			}
		}
	}
	return nil
}

// declScope returns the scope that declares the local whose name is
// ident, nil when it is in none.
func (rn *renamer) declScope(ident *syntax.Ident) *syntax.Scope {
	var find func(s *syntax.Scope) *syntax.Scope
	find = func(s *syntax.Scope) *syntax.Scope {
		for _, l := range s.Locals {
			if l.Ident == ident {
				return s
			}
		}
		for _, in := range s.Blocks {
			if f := find(in); f != nil {
				return f
			}
		}
		return nil
	}
	for _, s := range rn.scopes {
		if f := find(s); f != nil {
			return f
		}
	}
	return nil
}

// funcScope returns the scope of the function whose body off is in.
func (rn *renamer) funcScope(off uint32) *syntax.Scope {
	for _, s := range rn.scopes {
		if s.At(off) != nil {
			return s
		}
	}
	return nil
}

// within reports whether scope s is outer or inside it.
func within(s, outer *syntax.Scope) bool {
	for ; s != nil; s = s.Parent {
		if s == outer {
			return true
		}
	}
	return false
}

func (rn *renamer) localLine(l *syntax.Local) int { return rn.line(rn.tree.Off(l.Ident)) }

// localClashes refuses a new name for a local or a parameter that is
// declared in its scope already, that a declaration between would hide
// it behind, or that names something else where the renamed variable
// would be in scope.
func (rn *renamer) localClashes(plan *Plan, scope *syntax.Scope) error {
	if scope == nil {
		return nil
	}
	n, kind := plan.New, what(plan.Symbol.Kind)
	for _, l := range scope.Locals {
		if l.Name == n {
			return refuse("'%s' is declared in the same scope already, at line %d", n, rn.localLine(l))
		}
	}
	root := scope
	for root.Parent != nil {
		root = root.Parent
	}
	for _, o := range plan.Occurrences {
		if o.Class != Certain || o.Decl {
			continue
		}
		if m := root.Lookup(n, o.Start); m != nil {
			if ms := rn.declScope(m.Ident); ms != scope && within(ms, scope) {
				return refuse("at line %d the renamed %s would be hidden by the '%s' declared at line %d", rn.line(o.Start), kind, n, rn.localLine(m))
			}
		}
	}
	// The new name where the renamed variable is in scope: it must mean a
	// variable declared inside that scope, which keeps it.
	toks := rn.tok.Tokens
	for ti := rn.tok.TokenAt(plan.Symbol.Def.End); ti < len(toks) && toks[ti].Off <= scope.End; ti++ {
		tok := toks[ti]
		if tok.Kind != token.Ident || tok.Off < plan.Symbol.Def.End || rn.text(ti) != n || !rn.compiled(ti) {
			continue
		}
		at := rn.r.At(tok.Off)
		if at != nil {
			switch at.Kind {
			case resolve.Function, resolve.Label, resolve.Macro:
				continue // other names
			case resolve.Local, resolve.Parameter:
				if ds := rn.declScope(at.Defs[0].Local.Ident); ds != scope && within(ds, scope) {
					continue
				}
			}
		}
		return refuse("'%s' at line %d %s: the renamed %s would take its place there", n, rn.line(tok.Off), rn.describe(at), kind)
	}
	return nil
}

type tokensFunc func(from, to uint32, name string, visit func(ti int, tok token.Token) error) error

// variableClashes refuses a new name for a private global variable that
// the object has already, that it inherits, that a local hides where the
// variable is used, or that the file uses for something else.
func (rn *renamer) variableClashes(plan *Plan, tokens tokensFunc) error {
	n := plan.New
	if v, ok := rn.unit.Var(n); ok {
		return refuse("the object has a global variable '%s' already (%s)", n, rn.libLine(v.Path, v.Line))
	}
	if defs := rn.r.Lookup(index.Use{Name: n, Kind: index.UseVariable}); len(defs) > 0 {
		return refuse("'%s' is a global variable inherited from %s: the renamed variable would hide it here", n, defs[0].Object)
	}
	for _, o := range plan.Occurrences {
		if o.Class != Certain || o.Decl {
			continue
		}
		if sc := rn.funcScope(o.Start); sc != nil {
			if m := sc.Lookup(n, o.Start); m != nil {
				return refuse("at line %d the renamed variable would be hidden by the %s '%s' declared at line %d", rn.line(o.Start), localWhat(m), n, rn.localLine(m))
			}
		}
	}
	return tokens(0, uint32(len(rn.tok.Src)), n, func(ti int, tok token.Token) error {
		at := rn.r.At(tok.Off)
		if at != nil {
			switch at.Kind {
			case resolve.Function, resolve.Label, resolve.Macro, resolve.Local, resolve.Parameter:
				return nil
			}
		}
		return refuse("'%s' at line %d %s: it would name the renamed variable", n, rn.line(tok.Off), rn.describe(at))
	})
}

func localWhat(l *syntax.Local) string {
	if l.Param {
		return "parameter"
	}
	return "local variable"
}

func (rn *renamer) libLine(p string, line int) string {
	if p == rn.tok.Path {
		return fmt.Sprintf("line %d", line)
	}
	return fmt.Sprintf("%s:%d", rn.libName(p), line)
}

// functionClashes refuses a new name for a private function that the
// object has already, inherits, has from the auto object or the driver,
// or that the file calls.
func (rn *renamer) functionClashes(plan *Plan, tokens tokensFunc) error {
	n := plan.New
	if fn, ok := rn.unit.Func(n); ok {
		return refuse("the object has a function '%s' already (%s)", n, rn.libLine(fn.Path, fn.Line))
	}
	if defs := rn.r.Lookup(index.Use{Name: n, Kind: index.UseFunction, Form: index.FormOwn}); len(defs) > 0 {
		from := "inherits from " + defs[0].Object
		if defs[0].Auto {
			from = "has from the auto object"
		}
		return refuse("'%s' is a function the object %s: the renamed function would take its place in this file's calls", n, from)
	}
	if rn.kfuns.Lookup(n) != nil {
		return refuse("'%s' is a kfun: this file's calls of it would call the renamed function instead", n)
	}
	return tokens(0, uint32(len(rn.tok.Src)), n, func(ti int, tok token.Token) error {
		at := rn.r.At(tok.Off)
		prev, next := rn.neighbours(ti)
		called := at != nil && at.Kind == resolve.Function && at.Call == nil && prev != token.ColonColon
		if at == nil && next == token.LParen && prev != token.Arrow && prev != token.ColonColon {
			called = true
		}
		if called {
			return refuse("'%s' is called at line %d: the call would reach the renamed function", n, rn.line(tok.Off))
		}
		return nil
	})
}

// neighbours returns the kinds of the significant tokens around the one
// at index ti.
func (rn *renamer) neighbours(ti int) (prev, next token.Kind) {
	toks := rn.tok.Tokens
	for i := ti - 1; i >= 0; i-- {
		if !toks[i].Kind.IsTrivia() {
			prev = toks[i].Kind
			break
		}
	}
	for i := ti + 1; i < len(toks); i++ {
		if !toks[i].Kind.IsTrivia() {
			next = toks[i].Kind
			break
		}
	}
	return prev, next
}

// labelClashes refuses a new label that another inherit statement has,
// or that the file names as a label.
func (rn *renamer) labelClashes(plan *Plan, tokens tokensFunc) error {
	n := plan.New
	for _, d := range rn.tree.Decls {
		if in, ok := d.(*syntax.InheritDecl); ok && in.Label != nil && in.Label.Name == n {
			return refuse("another inherit statement has the label '%s' (line %d)", n, rn.line(rn.tree.Off(in.Label)))
		}
	}
	return tokens(0, uint32(len(rn.tok.Src)), n, func(ti int, tok token.Token) error {
		if at := rn.r.At(tok.Off); at != nil && at.Kind == resolve.Label {
			return refuse("'%s' at line %d names another label", n, rn.line(tok.Off))
		}
		return nil
	})
}

// planMacro plans the rename of a macro the file defines for itself:
// every identifier of the name in the file, in code compiled or not and
// in the directives that name macros, becomes the new name, which must
// be nowhere in the file, in what it includes, or in the names the
// object has. Nothing outside the file knows either name, so the file
// means after the rename what it meant before.
func (rn *renamer) planMacro(plan *Plan) error {
	old, n := plan.Symbol.Name, plan.New
	own := rn.tok.Path
	words := rn.directiveWords()
	for ti, tok := range rn.tok.Tokens {
		if tok.Kind == token.Ident && rn.text(ti) == n {
			return refuse("'%s' is used in this file already, at line %d", n, rn.line(tok.Off))
		}
	}
	for _, w := range words {
		if w.name == n {
			return refuse("'%s' is used in this file already, at line %d", n, rn.line(w.off))
		}
	}
	if what := rn.outsideName(n); what != "" {
		return refuse("'%s' is %s: the macro would replace it in this file", n, what)
	}
	if h := rn.mentionedInHeaders(n); h != "" {
		return refuse("'%s' is used in %s, which this file includes", n, h)
	}
	for ti, tok := range rn.tok.Tokens {
		if tok.Kind == token.Ident && rn.text(ti) == old {
			plan.Occurrences = append(plan.Occurrences, Occurrence{Path: own, Start: tok.Off, End: tok.End, Class: Certain})
		}
	}
	for _, w := range words {
		if w.name == old {
			plan.Occurrences = append(plan.Occurrences, Occurrence{Path: own, Start: w.off, End: w.off + uint32(len(old)),
				Class: Certain, Decl: w.define})
		}
	}
	if err := rn.stringized(plan); err != nil {
		return err
	}
	slices.SortFunc(plan.Occurrences, func(a, b Occurrence) int { return cmp.Compare(a.Start, b.Start) })
	return nil
}
