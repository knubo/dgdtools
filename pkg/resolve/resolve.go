// Package resolve says what a place in a file refers to: the file an
// #include names, the object an inherit statement or a path handed to
// clone_object names, the function a call reaches, the #define behind a
// macro, the declaration of a global variable.
//
// It is the one answer behind an editor's "go to definition", and what
// hover, references and rename start from: a Resolver's At classifies
// the token at an offset (a Ref) and lists the declarations it resolves
// to (its Defs), each with the facts the index has of it.
//
// The rule is the linter's: prove it or stay silent. A name resolves
// only through what the index knows (the file's own declarations, its
// inherit chain, the auto object, the #define environment, a receiver
// that is this_object() or a constant path). A call whose receiver is
// unknown is a Ref without Defs; a name nothing is known of is no Ref at
// all. A parameter or a local variable is the one in scope where the
// name stands (syntax.Scope), and a name is a global variable only
// where none is.
//
// Beside it: the outline of the file (Symbols), and what can be written
// at a place (Complete).
package resolve

import (
	"bytes"
	"slices"
	"sort"
	"sync"

	"github.com/mmcdole/dgdtools/pkg/calls"
	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/kfun"
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
	// Local is a local variable of a function, Parameter one of its
	// parameters.
	Local
	Parameter
	// Keyword is a word of the language, a type among them, and
	// Directory a directory in a path: what a completion offers beside
	// the names above (Item).
	Keyword
	Directory
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
	case Local:
		return "local"
	case Parameter:
		return "parameter"
	case Keyword:
		return "keyword"
	case Directory:
		return "directory"
	}
	return "unknown"
}

// File is the file questions are asked of, as a lint has it
// (lint.Document.Resolve asks for a document).
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
	// Kfuns are the kernel functions of the project's driver; nil is
	// stock DGD's (kfun.DGD).
	Kfuns *kfun.Table
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
	// of a function at its definition or prototype, of a variable or a
	// parameter where it is declared, of a macro in its #define, a label
	// in its inherit statement.
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
	// What the index knows of the declaration, by Kind; of a local
	// variable or a parameter, what its function's scope has.
	Func  *index.FuncInfo
	Var   *index.VarInfo
	Macro *pp.Macro
	Local *syntax.Local
}

// Resolver answers the questions about one file. It is safe for
// concurrent use, and reads the file's call sites once for all the
// questions that need them.
type Resolver struct {
	File
	s *index.Snapshot
	u *index.Unit

	once  sync.Once
	facts *calls.Facts

	scoped sync.Once
	scopes []*syntax.Scope

	// own are the Uses of the file, read on first need when its unit has
	// none.
	ownUses sync.Once
	own     []index.Use
}

// New returns the resolver of a file.
func New(f File) *Resolver { return &Resolver{File: f, s: f.Snapshot, u: f.Unit} }

// At returns what the token at byte offset off of the file refers to, or
// nil when that is nothing the tools can name: no name, or a name in
// code that is not compiled or could not be read. An
// offset just after a name is on it, as an editor's cursor at the end of
// a word is.
func (r *Resolver) At(off uint32) *Ref {
	ref, use := r.classify(off)
	if use == nil {
		return ref
	}
	if ref.Defs = r.Lookup(*use); len(ref.Defs) == 0 && use.Kind == index.UseVariable && !use.Decl {
		return nil // a name that is no variable the file can see
	}
	return ref
}

// classify is At before a name that other files can name too is looked
// up: the Ref without its Defs, and the Use to look up for them. A
// local, a parameter and a label are decided by the file, and come with
// their Defs and no Use.
func (r *Resolver) classify(off uint32) (*Ref, *index.Use) {
	f := &r.File
	if f.Tok == nil || f.Tree == nil || f.Tree.Sel == nil || f.Unit == nil || f.Snapshot == nil {
		return nil, nil
	}
	toks := f.Tok.Tokens
	ti := f.Tok.TokenAt(off)
	if !named(toks[ti].Kind) && ti > 0 && toks[ti].Off == off && named(toks[ti-1].Kind) {
		ti--
	}
	switch tok := toks[ti]; tok.Kind {
	case token.Directive:
		return r.directive(ti, off)
	case token.Ident:
		if !r.compiled(ti) {
			return nil, nil
		}
		return r.ident(ti, syntax.PathAt(f.Tree, ti))
	case token.StringLit:
		if !r.compiled(ti) {
			return nil, nil
		}
		return r.str(ti, syntax.PathAt(f.Tree, ti))
	}
	// Anywhere else in an inherit statement, its keywords and the ';'
	// included: the object inherited.
	if r.compiled(ti) {
		return r.inherit(syntax.PathAt(f.Tree, ti), ti)
	}
	return nil, nil
}

// compiled reports whether the token at index ti is code the compiler
// reads: not in a branch of a conditional that is skipped.
func (r *Resolver) compiled(ti int) bool {
	_, ok := slices.BinarySearch(r.Tree.Sel.Sig, ti)
	return ok
}

// Lookup returns the declarations a Use of the resolver's file names, in
// the resolver's generation of the index: what At gives as the Defs of
// the place. A Use is the index's record of a place (index.Unit.Uses),
// so the places of the whole lib are looked up without reading their
// files again.
func (r *Resolver) Lookup(u index.Use) []Def {
	name := u.Name
	switch u.Kind {
	case index.UseFunction:
		switch u.Form {
		case index.FormOwn, index.FormSelf:
			// What the object's own program has of the name. A function
			// only an inheritor provides is not searched for: which
			// inheritor is running nobody can say.
			return r.function(name)
		case index.FormSuper:
			// ::fn() is the inherited function, and the auto object's
			// when no inherited object has one.
			if defs := r.defs(name, r.inherited(name, "")); len(defs) > 0 {
				return defs
			}
			return r.auto(name, r.u.LibPath)
		case index.FormLabel:
			return r.defs(name, r.inherited(name, u.Aux))
		case index.FormPath:
			return r.callable(u.Aux, name)
		}
	case index.UseVariable:
		return r.variable(name)
	case index.UseMacro:
		if u.Aux != "" {
			return []Def{{Kind: Macro, Name: name, Path: u.Aux, Start: u.Off, End: u.Off + uint32(len(name)), Macro: u.Macro}}
		}
	case index.UseObject:
		return r.object(name)
	case index.UseInclude:
		if u.Aux != "" {
			return []Def{{Kind: Include, Name: name, Path: u.Aux}}
		}
	}
	return nil
}

// use is the Use of the place ref is, of the given kind and form.
func use(ref *Ref, kind index.UseKind, form index.UseForm) *index.Use {
	return &index.Use{Name: ref.Name, Start: ref.Start, End: ref.End, Kind: kind, Form: form, Decl: ref.Decl}
}

// named reports whether a token of kind k can name something.
func named(k token.Kind) bool {
	return k == token.Ident || k == token.StringLit || k == token.Directive
}

func has(n syntax.Node, tok int) bool {
	first, last := n.Span()
	return first <= tok && tok <= last
}

// ----------------------------------------------------------- directives

// directive classifies a place in an active directive: the target of an
// #include, anywhere on its line, and a macro's name in the others.
func (r *Resolver) directive(ti int, off uint32) (*Ref, *index.Use) {
	if r.Env == nil || r.Env.Own() == nil || !r.read(ti) {
		return nil, nil
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
			u := use(ref, index.UseInclude, 0)
			u.Aux, _ = r.s.ResolveInclude(r.Tok.Path, inc)
			return ref, u
		}
	case "define", "undef", "if", "ifdef", "ifndef", "elif":
		// The words of the line, as the preprocessor reads them.
		rel := int(off - tok.Off)
		if rel < arg {
			return nil, nil
		}
		words := lexer.Lex("", text[arg:], r.Tok.Dialect)
		rel -= arg
		wi := words.TokenAt(uint32(rel))
		if w := words.Tokens[wi]; w.Kind != token.Ident && wi > 0 && int(w.Off) == rel {
			wi--
		}
		w := words.Tokens[wi]
		if w.Kind != token.Ident {
			return nil, nil
		}
		word := string(words.Text(w))
		start := tok.Off + uint32(arg) + w.Off
		if name == "define" {
			if w.Off == 0 {
				// The name being defined, here.
				ref := &Ref{Kind: Macro, Name: word, Start: start, End: start + uint32(len(word)), Decl: true}
				u := use(ref, index.UseMacro, 0)
				u.Aux, u.Off = r.Tok.Path, start
				if m := r.Env.Own().Macros[word]; m != nil && m.NameOff == start {
					u.Macro = m
				}
				return ref, u
			}
			// A parameter of the macro is no name outside it.
			if first := words.Tokens[0]; first.Kind == token.Ident {
				if m := r.Env.Own().Macros[string(words.Text(first))]; m != nil && slices.Contains(m.Params, word) {
					return nil, nil
				}
			}
		}
		return r.macro(word, start)
	}
	return nil, nil
}

// read reports whether the preprocessor reads the directive at token
// ti: it is not in a branch of a conditional that is skipped.
func (r *Resolver) read(ti int) bool {
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

// macro classifies the name at start as a macro's, nil when the name is
// none in this file. The macro is what the file's environment has at
// its end: of two #defines of a name, the later.
func (r *Resolver) macro(name string, start uint32) (*Ref, *index.Use) {
	def := r.Env.Definition(name)
	if def == nil && r.Env.Lookup(name) == nil {
		return nil, nil
	}
	ref := &Ref{Kind: Macro, Name: name, Start: start, End: start + uint32(len(name))}
	u := use(ref, index.UseMacro, 0)
	if def != nil {
		u.Aux, u.Off, u.Macro = def.Path, def.NameOff, def
	}
	return ref, u
}

// ---------------------------------------------------------------- names

// ident classifies an identifier of the compiled code, path being the
// nodes around it (syntax.PathAt).
func (r *Resolver) ident(ti int, path []syntax.Node) (*Ref, *index.Use) {
	t := r.Tree
	tok := r.Tok.Tokens[ti]
	name := string(r.Tok.Text(tok))
	if ref, u := r.inherit(path, ti); ref != nil {
		return ref, u
	}
	// The preprocessor takes a macro's name wherever it stands.
	if ref, u := r.macro(name, tok.Off); ref != nil {
		return ref, u
	}
	ref := &Ref{Name: name, Start: tok.Off, End: tok.End}
	switch n := path[0].(type) {
	case *syntax.InheritDecl:
		if n.Label != nil && has(n.Label, ti) {
			ref.Kind, ref.Decl = Label, true
			ref.Defs = r.labels(name)
			return ref, nil
		}
	case *syntax.CallExpr:
		switch {
		case n.Label != nil && has(n.Label, ti):
			ref.Kind, ref.Defs = Label, r.labels(name)
			return ref, nil
		case !has(n.Fun, ti):
		case !n.Scoped:
			ref.Kind = Function
			return ref, use(ref, index.UseFunction, index.FormOwn)
		case n.Label != nil:
			ref.Kind = Function
			u := use(ref, index.UseFunction, index.FormLabel)
			u.Aux = n.Label.Name
			return ref, u
		default:
			ref.Kind = Function
			return ref, use(ref, index.UseFunction, index.FormSuper)
		}
	case *syntax.ArrowExpr:
		if has(n.Name, ti) {
			return r.late(tok)
		}
	case *syntax.FuncDecl:
		if n.Name != nil && has(n.Name, ti) {
			ref.Kind, ref.Decl = Function, true
			return ref, use(ref, index.UseFunction, index.FormOwn)
		}
	case *syntax.Declarator:
		// A declarator of the file itself is a global variable; one in a
		// function is a local.
		vd, ok := path[1].(*syntax.VarDecl)
		switch {
		case !ok || n.Name == nil || !has(n.Name, ti):
		case len(path) == 3:
			ref.Kind, ref.Decl = Variable, true
			return ref, use(ref, index.UseVariable, 0)
		default:
			return r.local(tok, r.declared(ti, &syntax.Local{Name: name, Type: t.TypeText(vd.Type, n.Stars), Ident: n.Name}), true), nil
		}
	case *syntax.Param:
		if n.Name != nil && has(n.Name, ti) {
			return r.local(tok, r.declared(ti, &syntax.Local{Name: name, Type: t.TypeText(n.Type, n.Stars), Param: true, Ident: n.Name}), true), nil
		}
	case *syntax.CatchStmt:
		if n.Exc != nil && has(n.Exc, ti) {
			return r.local(tok, r.declared(ti, &syntax.Local{Name: name, Type: "string", Catch: true, Ident: n.Exc}), true), nil
		}
	case *syntax.Ident:
		// &name(...) names a function; in (*f)(...) f is a variable.
		if fp, ok := path[1].(*syntax.FuncPtrExpr); ok && fp.Fun == syntax.Expr(n) && r.after(fp.First, token.Amp) == ti {
			ref.Kind = Function
			return ref, use(ref, index.UseFunction, index.FormOwn)
		}
		if n.Name != name {
			return nil, nil // ::name without a call is no variable
		}
		// The parameter or the local variable in scope here, before a
		// global. A statement before this one that the parser could not
		// read may declare the name: nothing is said then.
		if sc := r.scope(tok.Off); sc != nil {
			if l := sc.Lookup(name, tok.Off); l != nil {
				return r.local(tok, l, false), nil
			}
			if sc.Unsure(tok.Off, func(start, end uint32) bool { return r.mentions(start, end, name) }) {
				return nil, nil
			}
		}
		ref.Kind = Variable
		return ref, use(ref, index.UseVariable, 0)
	}
	return nil, nil
}

// after returns the index of the token of the code that follows the
// one at index tok, when that one is of kind k; -1 otherwise.
func (r *Resolver) after(tok int, k token.Kind) int {
	toks := r.Tok.Tokens
	if toks[tok].Kind != k {
		return -1
	}
	for i := tok + 1; i < len(toks); i++ {
		if !toks[i].Kind.IsTrivia() {
			return i
		}
	}
	return -1
}

// str classifies a string constant of the compiled code: the path of an
// inherit statement, the name of a function in a call that names one,
// an object path handed to a function that takes one.
func (r *Resolver) str(ti int, path []syntax.Node) (*Ref, *index.Use) {
	if ref, u := r.inherit(path, ti); ref != nil {
		return ref, u
	}
	tok := r.Tok.Tokens[ti]
	if ref, u := r.late(tok); ref != nil {
		return ref, u
	}
	for _, p := range r.calls().Paths {
		if p.Start == tok.Off && p.End == tok.End {
			ref := &Ref{Kind: Object, Name: p.Path, Start: tok.Off + 1, End: tok.End - 1}
			return ref, use(ref, index.UseObject, 0)
		}
	}
	return nil, nil
}

// inherit classifies a token of an inherit statement but its label as
// the object inherited. The whole statement names it, a macro in the
// path included: `inherit ROOM;` is about the room, not about the
// #define.
func (r *Resolver) inherit(path []syntax.Node, ti int) (*Ref, *index.Use) {
	if len(path) < 2 {
		return nil, nil
	}
	d, ok := path[len(path)-2].(*syntax.InheritDecl)
	if !ok || d.Path == nil || !has(d, ti) || d.Label != nil && has(d.Label, ti) {
		return nil, nil
	}
	off := r.Tree.Off(d.Path)
	for _, in := range r.u.Inherits {
		if in.Off == off && in.Resolved {
			ref := &Ref{Kind: Object, Name: in.Path, Start: in.Off, End: in.End}
			return ref, use(ref, index.UseObject, 0)
		}
	}
	return nil, nil
}

// object returns the file of the object at libPath, when it has one.
func (r *Resolver) object(libPath string) []Def {
	if !r.s.ObjectExists(libPath) {
		return nil
	}
	return []Def{{Kind: Object, Name: libPath, Path: r.s.ObjectFile(libPath), Object: libPath}}
}

// labels returns the inherit statements of the file that have the label.
func (r *Resolver) labels(label string) []Def {
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
func (r *Resolver) calls() *calls.Facts {
	r.once.Do(func() {
		r.facts = calls.Extract(calls.File{
			Tree: r.Tree, Env: r.Env, LibPath: r.u.LibPath,
			ObjectPath: func(written string) string { return r.s.ObjectPath(written, r.u.LibPath) },
		}, r.registry())
	})
	return r.facts
}

// registry returns the project's registry of the functions that take
// the name of a function or the path of an object.
func (r *Resolver) registry() *calls.Registry {
	if r.Registry != nil {
		return r.Registry
	}
	return driverRegistry()
}

var driverRegistry = sync.OnceValue(func() *calls.Registry { return calls.NewRegistry(profile.Conventions{}) })

// late classifies the function name at tok in a call that is bound when
// it runs: x->name(), or "name" handed to call_other, call_out or one of
// the lib's registrars. It is nil when tok is no such name.
func (r *Resolver) late(tok token.Token) (*Ref, *index.Use) {
	for i := range r.calls().Named {
		c := &r.calls().Named[i]
		if c.Start != tok.Off || c.End != tok.End {
			continue
		}
		ref := &Ref{Kind: Function, Name: c.Func, Start: c.Start, End: c.End, Call: c}
		if tok.Kind == token.StringLit {
			ref.Start, ref.End = c.Start+1, c.End-1
		}
		u := use(ref, index.UseFunction, index.FormUnknown)
		switch c.Target {
		case calls.TargetSelf:
			u.Form = index.FormSelf
		case calls.TargetPath:
			u.Form, u.Aux = index.FormPath, c.TargetPath
		}
		return ref, u
	}
	return nil, nil
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
func (r *Resolver) inherited(name, label string) []candidate {
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

func (r *Resolver) defs(name string, cs []candidate) []Def {
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

// auto returns the function called name that the auto object gives the
// object at libPath, which has none of that name in what is known of
// its chain. It is that object's only when no part of the chain is
// unknown: an object it inherits that the index cannot see may have a
// function of the name, which would be the one, as the lib's own objects
// have for many of the auto object's. A nomask function is everybody's.
// The auto object gives nothing to itself or to what it is made of.
func (r *Resolver) auto(name, libPath string) []Def {
	if r.u.LibPath == "" || r.s.AutoMember(libPath) {
		return nil
	}
	fn, ok := r.s.AutoFunc(name)
	if !ok || (!fn.Nomask && r.s.ChainUnseen(libPath)) {
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
func (r *Resolver) function(name string) []Def {
	var groups [][]Def
	if own, ok := r.u.Funcs[name]; ok {
		groups = append(groups, []Def{funcDef(name, index.ChainFunc{FuncInfo: own, DefinedIn: r.u.LibPath})})
	}
	if in := r.defs(name, r.inherited(name, "")); len(in) > 0 {
		groups = append(groups, in)
	}
	if auto := r.auto(name, r.u.LibPath); len(auto) > 0 {
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
func (r *Resolver) callable(target, name string) []Def {
	if r.s.Unit(target) == nil {
		return nil
	}
	if fn, ok := r.s.ChainFunc(target, name); ok {
		if fn.Private && fn.DefinedIn != target {
			return nil
		}
		return []Def{funcDef(name, fn)}
	}
	return r.auto(name, target)
}

// ------------------------------------------------------------ variables

// variable returns the global variable called name as the file's code
// sees it: its own, else the one an inherited object declares and does
// not keep private.
func (r *Resolver) variable(name string) []Def {
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

// --------------------------------------------------------------- locals

// scope returns the scope of the function whose body byte offset off
// is in, nil when it is in none. The scopes are read from the tree on
// first need.
func (r *Resolver) scope(off uint32) *syntax.Scope {
	scopes := r.allScopes()
	i := sort.Search(len(scopes), func(i int) bool { return scopes[i].Start > off }) - 1
	if i < 0 || scopes[i].At(off) == nil {
		return nil
	}
	return scopes[i]
}

// allScopes returns the scopes of the file's functions, in the order of
// the file, read from the tree on first need.
func (r *Resolver) allScopes() []*syntax.Scope {
	r.scoped.Do(func() { r.scopes = r.Tree.Scopes() })
	return r.scopes
}

// declared returns the variable that the token at index ti declares,
// as the scope of its function has it; l, which describes it, when it
// is in no scope: a parameter of a prototype.
func (r *Resolver) declared(ti int, l *syntax.Local) *syntax.Local {
	var find func(s *syntax.Scope) *syntax.Local
	find = func(s *syntax.Scope) *syntax.Local {
		for _, have := range s.Locals {
			if have.Ident == l.Ident {
				return have
			}
		}
		for _, in := range s.Blocks {
			if have := find(in); have != nil {
				return have
			}
		}
		return nil
	}
	for _, s := range r.allScopes() {
		if first, last := s.Func.Span(); first <= ti && ti <= last {
			if have := find(s); have != nil {
				return have
			}
		}
	}
	return l
}

// local is the reference of the name at tok to a parameter or a local
// variable of the function it is in.
func (r *Resolver) local(tok token.Token, l *syntax.Local, decl bool) *Ref {
	kind := Local
	if l.Param {
		kind = Parameter
	}
	start := r.Tree.Off(l.Ident)
	return &Ref{Kind: kind, Name: l.Name, Start: tok.Off, End: tok.End, Decl: decl,
		Defs: []Def{{Kind: kind, Name: l.Name, Path: r.Tok.Path, Start: start, End: r.Tree.End(l.Ident), Local: l}}}
}

// mentions reports whether the bytes [start, end) of the file have the
// identifier name.
func (r *Resolver) mentions(start, end uint32, name string) bool {
	toks := r.Tok.Tokens
	for i := r.Tok.TokenAt(start); i < len(toks) && toks[i].Off < end; i++ {
		if toks[i].Kind == token.Ident && string(r.Tok.Text(toks[i])) == name {
			return true
		}
	}
	return false
}
