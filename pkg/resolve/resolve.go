// Package resolve says what a place in a file refers to: the file an
// #include names, the object an inherit statement or a path handed to
// clone_object names, the function a call reaches, the #define behind a
// macro, the declaration of a global variable.
//
// It is the one answer behind an editor's "go to definition", and what
// hover, references and rename start from: At classifies the token at an
// offset (a Ref) and lists the declarations it resolves to (its Defs),
// each with the facts the index has of it.
//
// The rule is the linter's: prove it or stay silent. A name resolves
// only through what the index knows (the file's own declarations, its
// inherit chain, the auto object, the #define environment, a receiver
// that is this_object() or a constant path). A call whose receiver is
// unknown is a Ref without Defs; a name nothing is known of is no Ref at
// all. Local variables and parameters are not resolved, and a name they
// may shadow is not taken for a global.
package resolve

import (
	"bytes"
	"slices"

	"github.com/mmcdole/dgdtools/pkg/calls"
	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/syntax"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// Kind says what a name is the name of.
type Kind uint8

const (
	// Include is a file, as an #include names it.
	Include Kind = iota + 1
	// Object is an object, named by its path: in an inherit statement,
	// or handed to a function that takes one (clone_object).
	Object
	// Label is the label an inherit statement gives what it inherits.
	Label
	Function
	// Variable is a global variable of an object.
	Variable
	Macro
)

func (k Kind) String() string {
	switch k {
	case Include:
		return "include"
	case Object:
		return "object"
	case Label:
		return "label"
	case Function:
		return "function"
	case Variable:
		return "variable"
	case Macro:
		return "macro"
	}
	return "unknown"
}

// File is the file a question is asked of, as a lint has it
// (lint.Document.Resolve fills one in).
type File struct {
	Tok  *token.File
	Tree *syntax.File
	// Env is the file's #define environment and Unit its entry in the
	// index.
	Env  *pp.Env
	Unit *index.Unit
	// Snapshot is the generation of the index the file was read in.
	Snapshot *index.Snapshot
	// Registry knows the lib's functions that take a function name or an
	// object path; nil is the driver's own.
	Registry *calls.Registry
}

// Ref is a place in a file that names something.
type Ref struct {
	Kind Kind
	// Name is what is named: a function, variable, macro or label by its
	// name, an object by its lib path, an included file as written.
	Name string
	// Start and End are the bytes of the file that name it: the
	// identifier, the callback's name inside its quotes, the path
	// expression of an inherit statement, the target of an #include.
	Start, End uint32
	// Decl says the place is itself a declaration of the name: the name
	// of a function at its definition or prototype, of a global variable
	// where it is declared, of a macro in its #define, a label in its
	// inherit statement.
	Decl bool
	// Call is the call site when the name is a function's in a call
	// that is bound when it runs (x->fn(), call_other, a registrar):
	// how it is made, and what is known of the object it is made in.
	Call *calls.Named
	// Defs are the declarations the name resolves to. None: what is
	// named is known and where it is declared is not (a function of the
	// driver's, a call with an unknown receiver, a macro the project
	// configures, an object or a file that is not there). More than
	// one: the name is ambiguous among them.
	Defs []Def
}

// Def is a declaration a Ref resolves to.
type Def struct {
	Kind Kind
	Name string
	// Path is the file the declaration is in: a path on disk, or the
	// location of a profile's stub (profile.SplitLocation). Start and
	// End are the bytes of the declared name in it; both are 0 for a
	// file or an object as a whole.
	Path       string
	Start, End uint32
	// Object is the lib path of the object that has the function or the
	// variable, or of the object itself; "" when the declaration belongs
	// to no object of the lib. Auto says the function is one the auto
	// object gives every object.
	Object string
	Auto   bool
	// What the index knows of the declaration, by Kind.
	Func  *index.FuncInfo
	Var   *index.VarInfo
	Macro *pp.Macro
}

// At returns what the token at byte offset off of the file refers to, or
// nil when that is nothing the tools can name: no name, a local
// variable, a name in code that is not compiled or could not be read. An
// offset just after a name is on it, as an editor's cursor at the end of
// a word is.
func At(f File, off uint32) *Ref {
	if f.Tok == nil || f.Tree == nil || f.Tree.Sel == nil || f.Unit == nil || f.Snapshot == nil {
		return nil
	}
	r := &resolver{File: f, s: f.Snapshot, u: f.Unit}
	toks := f.Tok.Tokens
	ti := f.Tok.TokenAt(off)
	if !named(toks[ti].Kind) && ti > 0 && toks[ti].Off == off && named(toks[ti-1].Kind) {
		ti--
	}
	switch tok := toks[ti]; tok.Kind {
	case token.Directive:
		return r.directive(ti, off)
	case token.Ident:
		if _, compiled := slices.BinarySearch(f.Tree.Sel.Sig, ti); !compiled {
			return nil
		}
		return r.ident(ti)
	case token.StringLit:
		if _, compiled := slices.BinarySearch(f.Tree.Sel.Sig, ti); !compiled {
			return nil
		}
		return r.str(ti)
	}
	return nil
}

// named reports whether a token of kind k can name something.
func named(k token.Kind) bool {
	return k == token.Ident || k == token.StringLit || k == token.Directive
}

type resolver struct {
	File
	s     *index.Snapshot
	u     *index.Unit
	facts *calls.Facts
}

func has(n syntax.Node, tok int) bool {
	first, last := n.Span()
	return first <= tok && tok <= last
}

// ----------------------------------------------------------- directives

// directive resolves a place in an active directive: the target of an
// #include, anywhere on its line, and a macro's name in the others.
func (r *resolver) directive(ti int, off uint32) *Ref {
	if r.Env == nil || r.Env.Own() == nil || !r.read(ti) {
		return nil
	}
	tok := r.Tok.Tokens[ti]
	text := r.Tok.Text(tok)
	name, _ := pp.SplitDirective(text)
	arg := pp.ArgOffset(text)
	switch name {
	case "include":
		for _, inc := range r.Env.Own().Includes {
			if inc.Tok != ti {
				continue
			}
			ref := &Ref{Kind: Include, Name: inc.Name, Start: tok.Off, End: tok.End}
			if raw := inc.Raw(); bytes.HasPrefix(text[arg:], []byte(raw)) {
				ref.Start = tok.Off + uint32(arg)
				ref.End = ref.Start + uint32(len(raw))
			}
			if p, ok := r.s.ResolveInclude(r.Tok.Path, inc); ok {
				ref.Defs = []Def{{Kind: Include, Name: inc.Name, Path: p}}
			}
			return ref
		}
	case "define", "undef", "if", "ifdef", "ifndef", "elif":
		// The words of the line, as the preprocessor reads them.
		rel := int(off - tok.Off)
		if rel < arg {
			return nil
		}
		words := lexer.Lex("", text[arg:], r.Tok.Dialect)
		rel -= arg
		wi := words.TokenAt(uint32(rel))
		if w := words.Tokens[wi]; w.Kind != token.Ident && wi > 0 && int(w.Off) == rel {
			wi--
		}
		w := words.Tokens[wi]
		if w.Kind != token.Ident {
			return nil
		}
		word := string(words.Text(w))
		start := tok.Off + uint32(arg) + w.Off
		if name == "define" {
			if w.Off == 0 {
				// The name being defined, here.
				ref := &Ref{Kind: Macro, Name: word, Start: start, End: start + uint32(len(word)), Decl: true}
				def := Def{Kind: Macro, Name: word, Path: r.Tok.Path, Start: ref.Start, End: ref.End}
				if m := r.Env.Own().Macros[word]; m != nil && m.NameOff == start {
					def.Macro = m
				}
				ref.Defs = []Def{def}
				return ref
			}
			// A parameter of the macro is no name outside it.
			if first := words.Tokens[0]; first.Kind == token.Ident {
				if m := r.Env.Own().Macros[string(words.Text(first))]; m != nil && slices.Contains(m.Params, word) {
					return nil
				}
			}
		}
		return r.macro(word, start)
	}
	return nil
}

// read reports whether the preprocessor reads the directive at token
// ti: it is not in a branch of a conditional that is skipped.
func (r *resolver) read(ti int) bool {
	for _, c := range r.Tree.Sel.Conds {
		if len(c.Arms) == 0 || ti <= c.Arms[0].Dir || ti >= c.End {
			continue
		}
		// The arm the directive is in, unless it opens one itself.
		in := c.Arms[0]
		for _, a := range c.Arms {
			if a.Dir <= ti {
				in = a
			}
		}
		if in.Dir != ti && !in.Taken {
			return false
		}
	}
	return true
}

// macro returns the reference of the name at start to a macro, nil when
// the name is none in this file.
func (r *resolver) macro(name string, start uint32) *Ref {
	def := r.Env.Definition(name)
	if def == nil && r.Env.Lookup(name) == nil {
		return nil
	}
	ref := &Ref{Kind: Macro, Name: name, Start: start, End: start + uint32(len(name))}
	if def != nil {
		ref.Defs = []Def{{
			Kind: Macro, Name: name, Path: def.Path,
			Start: def.NameOff, End: def.NameOff + uint32(len(name)), Macro: def,
		}}
	}
	return ref
}

// ---------------------------------------------------------------- names

// ident resolves an identifier of the compiled code.
func (r *resolver) ident(ti int) *Ref {
	t := r.Tree
	tok := r.Tok.Tokens[ti]
	name := string(r.Tok.Text(tok))
	path := syntax.PathAt(t, ti)
	if ref := r.inherit(path, ti); ref != nil {
		return ref
	}
	// The preprocessor takes a macro's name wherever it stands.
	if ref := r.macro(name, tok.Off); ref != nil {
		return ref
	}
	ref := &Ref{Name: name, Start: tok.Off, End: tok.End}
	switch n := path[0].(type) {
	case *syntax.InheritDecl:
		if n.Label != nil && has(n.Label, ti) {
			ref.Kind, ref.Decl = Label, true
			ref.Defs = r.labels(name)
			return ref
		}
	case *syntax.CallExpr:
		switch {
		case n.Label != nil && has(n.Label, ti):
			ref.Kind, ref.Defs = Label, r.labels(name)
			return ref
		case !has(n.Fun, ti):
		case !n.Scoped:
			ref.Kind, ref.Defs = Function, r.function(name)
			return ref
		case n.Label != nil:
			ref.Kind, ref.Defs = Function, r.defs(name, r.inherited(name, n.Label.Name))
			return ref
		default:
			// ::fn() is the inherited function, and the auto object's
			// when no inherited object has one.
			ref.Kind, ref.Defs = Function, r.defs(name, r.inherited(name, ""))
			if len(ref.Defs) == 0 {
				ref.Defs = r.auto(name)
			}
			return ref
		}
	case *syntax.ArrowExpr:
		if has(n.Name, ti) {
			return r.late(tok)
		}
	case *syntax.FuncDecl:
		if n.Name != nil && has(n.Name, ti) {
			ref.Kind, ref.Decl, ref.Defs = Function, true, r.function(name)
			return ref
		}
	case *syntax.Declarator:
		// A declarator of the file itself is a global variable; one in a
		// function is a local.
		if _, top := path[1].(*syntax.VarDecl); top && len(path) == 3 && has(n.Name, ti) {
			ref.Kind, ref.Decl, ref.Defs = Variable, true, r.variable(name)
			return ref
		}
	case *syntax.Ident:
		if fp, ok := path[1].(*syntax.FuncPtrExpr); ok && fp.Fun == syntax.Expr(n) {
			ref.Kind, ref.Defs = Function, r.function(name)
			return ref
		}
		if r.shadowed(path, name) {
			return nil
		}
		if defs := r.variable(name); len(defs) > 0 {
			ref.Kind, ref.Defs = Variable, defs
			return ref
		}
	}
	return nil
}

// str resolves a string constant of the compiled code: the path of an
// inherit statement, the name of a function in a call that names one,
// an object path handed to a function that takes one.
func (r *resolver) str(ti int) *Ref {
	if ref := r.inherit(syntax.PathAt(r.Tree, ti), ti); ref != nil {
		return ref
	}
	tok := r.Tok.Tokens[ti]
	if ref := r.late(tok); ref != nil {
		return ref
	}
	for _, p := range r.calls().Paths {
		if p.Start == tok.Off && p.End == tok.End {
			return &Ref{Kind: Object, Name: p.Path, Start: tok.Off + 1, End: tok.End - 1, Defs: r.object(p.Path)}
		}
	}
	return nil
}

// inherit resolves a token in the path of an inherit statement to the
// object inherited. The whole expression names it, a macro in it
// included: `inherit ROOM;` is about the room, not about the #define.
func (r *resolver) inherit(path []syntax.Node, ti int) *Ref {
	if len(path) < 2 {
		return nil
	}
	d, ok := path[len(path)-2].(*syntax.InheritDecl)
	if !ok || d.Path == nil || !has(d.Path, ti) {
		return nil
	}
	off := r.Tree.Off(d.Path)
	for _, in := range r.u.Inherits {
		if in.Off == off && in.Resolved {
			return &Ref{Kind: Object, Name: in.Path, Start: in.Off, End: in.End, Defs: r.object(in.Path)}
		}
	}
	return nil
}

// object returns the file of the object at libPath, when it has one.
func (r *resolver) object(libPath string) []Def {
	if !r.s.ObjectExists(libPath) {
		return nil
	}
	return []Def{{Kind: Object, Name: libPath, Path: r.s.ObjectFile(libPath), Object: libPath}}
}

// labels returns the inherit statements of the file that have the label.
func (r *resolver) labels(label string) []Def {
	var defs []Def
	for _, d := range r.Tree.Decls {
		if in, ok := d.(*syntax.InheritDecl); ok && in.Label != nil && in.Label.Name == label {
			defs = append(defs, Def{Kind: Label, Name: label, Path: r.Tok.Path,
				Start: r.Tree.Off(in.Label), End: r.Tree.End(in.Label)})
		}
	}
	return defs
}

// ------------------------------------------------------------ functions

// calls returns the file's call sites, extracted on first need.
func (r *resolver) calls() *calls.Facts {
	if r.facts == nil {
		reg := r.Registry
		if reg == nil {
			reg = calls.NewRegistry(profile.Conventions{})
		}
		r.facts = calls.Extract(calls.File{
			Tree: r.Tree, Env: r.Env, LibPath: r.u.LibPath,
			ObjectPath: func(written string) string { return r.s.ObjectPath(written, r.u.LibPath) },
		}, reg)
	}
	return r.facts
}

// late resolves the function name at tok in a call that is bound when it
// runs: x->name(), or "name" handed to call_other, call_out or one of
// the lib's registrars. It is nil when tok is no such name.
func (r *resolver) late(tok token.Token) *Ref {
	for i := range r.calls().Named {
		c := &r.calls().Named[i]
		if c.Start != tok.Off || c.End != tok.End {
			continue
		}
		ref := &Ref{Kind: Function, Name: c.Func, Start: c.Start, End: c.End, Call: c}
		if tok.Kind == token.StringLit {
			ref.Start, ref.End = c.Start+1, c.End-1
		}
		switch c.Target {
		case calls.TargetSelf:
			// What the object's own program has of the name. A function
			// only an inheritor provides is not searched for: which
			// inheritor is running nobody can say.
			ref.Defs = r.function(c.Func)
		case calls.TargetPath:
			ref.Defs = r.callable(c.TargetPath, c.Func)
		}
		return ref
	}
	return nil
}

// candidate is a function as an inherited object's chain provides it,
// and the inherit statements (their objects) it was found through.
type candidate struct {
	fn  index.ChainFunc
	via []string
}

// inherited returns the functions called name that the file's inherit
// statements provide, those with the given label only when one is given.
// A function that is private where it is inherited from is not among
// them; neither is one that another of them redefines.
func (r *resolver) inherited(name, label string) []candidate {
	var cs []candidate
	for _, in := range r.u.Inherits {
		if !in.Resolved || (label != "" && in.Label != label) {
			continue
		}
		fn, ok := r.s.Chain(in.Path).Funcs[name]
		if !ok || fn.Private {
			continue
		}
		i := slices.IndexFunc(cs, func(c candidate) bool { return c.fn.Loc == fn.Loc })
		if i < 0 {
			cs = append(cs, candidate{fn: fn})
			i = len(cs) - 1
		}
		cs[i].via = append(cs[i].via, in.Path)
	}
	if len(cs) < 2 {
		return cs
	}
	// A definition counts before a prototype, and a function is gone
	// where the object another candidate came through inherits its
	// object and has the other one all the same: that one redefines it.
	masked := func(c candidate) bool {
		return slices.ContainsFunc(cs, func(o candidate) bool {
			if o.fn.Loc == c.fn.Loc {
				return false
			}
			if c.fn.PrototypeOnly && !o.fn.PrototypeOnly {
				return true
			}
			return slices.ContainsFunc(o.via, func(obj string) bool {
				return slices.Contains(r.s.Chain(obj).Objects, c.fn.DefinedIn)
			})
		})
	}
	var out []candidate
	for _, c := range cs {
		if !masked(c) {
			out = append(out, c)
		}
	}
	return out
}

func (r *resolver) defs(name string, cs []candidate) []Def {
	var defs []Def
	for _, c := range cs {
		defs = append(defs, funcDef(name, c.fn))
	}
	return defs
}

func funcDef(name string, fn index.ChainFunc) Def {
	fi := fn.FuncInfo
	return Def{Kind: Function, Name: name, Path: fn.Path, Start: fn.Off, End: fn.Off + uint32(len(name)),
		Object: fn.DefinedIn, Func: &fi}
}

// auto returns the function the auto object gives the file's object.
func (r *resolver) auto(name string) []Def {
	if r.u.LibPath == "" {
		return nil
	}
	fn, ok := r.s.AutoFunc(name)
	if !ok {
		return nil
	}
	d := funcDef(name, fn)
	d.Object, d.Auto = r.s.LibPath(fn.Path), true
	return []Def{d}
}

// function returns what the name of a function means in the file's own
// code: the file's function, else the inherited one, else the auto
// object's; and of those a definition before a prototype. Nothing is
// left for a function of the driver's.
func (r *resolver) function(name string) []Def {
	var groups [][]Def
	if own, ok := r.u.Funcs[name]; ok {
		groups = append(groups, []Def{funcDef(name, index.ChainFunc{FuncInfo: own, DefinedIn: r.u.LibPath})})
	}
	if in := r.defs(name, r.inherited(name, "")); len(in) > 0 {
		groups = append(groups, in)
	}
	if auto := r.auto(name); len(auto) > 0 {
		groups = append(groups, auto)
	}
	for _, g := range groups {
		if !g[0].Func.PrototypeOnly {
			return g
		}
	}
	if len(groups) > 0 {
		return groups[0]
	}
	return nil
}

// callable returns the function called name of the object at target, as
// a call from outside names it: from the object's chain, else the auto
// object's. Nothing is said of an object that is not there. A function that is private in an object the target inherits
// is not the target's, and may hide one that is: nothing is said then.
func (r *resolver) callable(target, name string) []Def {
	if r.s.Unit(target) == nil {
		return nil
	}
	if fn, ok := r.s.Chain(target).Funcs[name]; ok {
		if fn.Private && fn.DefinedIn != target {
			return nil
		}
		return []Def{funcDef(name, fn)}
	}
	return r.auto(name)
}

// ------------------------------------------------------------ variables

// variable returns the global variable called name as the file's code
// sees it: its own, else the one an inherited object declares and does
// not keep private.
func (r *resolver) variable(name string) []Def {
	def := func(v index.VarInfo, object string) Def {
		return Def{Kind: Variable, Name: name, Path: v.Path, Start: v.Off, End: v.Off + uint32(len(name)),
			Object: object, Var: &v}
	}
	if own, ok := r.u.Vars[name]; ok {
		return []Def{def(own, r.u.LibPath)}
	}
	var defs []Def
	for _, in := range r.u.Inherits {
		if !in.Resolved {
			continue
		}
		v, ok := r.s.Chain(in.Path).Vars[name]
		if !ok || v.Private {
			continue
		}
		if !slices.ContainsFunc(defs, func(d Def) bool { return d.Var.Loc == v.Loc }) {
			defs = append(defs, def(v.VarInfo, v.DefinedIn))
		}
	}
	return defs
}

// shadowed reports whether name, used at the place path leads to, may be
// a parameter or a local variable of the function it is in: the function
// declares one of that name anywhere, or has a statement the parser
// could not read that mentions the name.
func (r *resolver) shadowed(path []syntax.Node, name string) bool {
	var fn *syntax.FuncDecl
	for _, n := range path {
		if f, ok := n.(*syntax.FuncDecl); ok {
			fn = f
		}
	}
	if fn == nil {
		return false
	}
	for _, p := range fn.Params {
		if p.Name != nil && p.Name.Name == name {
			return true
		}
	}
	if fn.Body == nil {
		return false
	}
	found := false
	syntax.Inspect(fn.Body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Declarator:
			found = found || (n.Name != nil && n.Name.Name == name)
		case *syntax.BadStmt:
			first, last := n.Span()
			for i := first; i <= last && i < len(r.Tok.Tokens) && !n.Missing; i++ {
				if t := r.Tok.Tokens[i]; t.Kind == token.Ident && string(r.Tok.Text(t)) == name {
					found = true
				}
			}
		}
		return !found
	})
	return found
}
