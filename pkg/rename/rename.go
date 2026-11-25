// Package rename plans the renaming of a name: given a place in a file
// and a new name, every occurrence of what is named there, the edits that
// rename it, or the reason it must not be renamed as asked. Nothing in it
// writes a file; an editor (textDocument/rename) and the command line
// show or apply the plan.
//
// The rule is the linter's: prove it or refuse. Every place the old name
// stands as an identifier is classified (Occurrence): Certain, the
// symbol; Excluded, provably something else; Possible, code the tools
// cannot read (a branch of #if that is not compiled, a statement that
// does not parse), which might be the symbol. A plan with a Possible
// occurrence is refused, and so is one where the new name would change
// what another name means: a declaration it would hide or be hidden by,
// a macro, a function it would take the place of.
//
// What is renamed are the symbols that one file decides: a local
// variable or a parameter (a catch variable among them), a private
// function and a private global variable of the file's object, an
// inherit label, and a macro the file defines for itself. Everything
// else is named by other files too, and is refused with the reason: a
// function or a global variable that is not private, what is inherited,
// the auto object's, a kfun, a header's macro, a declaration in a
// profile's stub.
//
// DGD does not call a private function by name: call_other, call_out
// and every callback a lib's registrar makes look the name up in the
// object's table of functions, which has no private ones (DGD's
// Control::makeSymbols; Frame::call). The string "fn" of
// call_out("fn") and this_object()->fn() never reach a private fn, so
// they are not occurrences of it: they are Excluded, left as they are,
// and reported as a warning, since such a call fails today.
package rename

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/calls"
	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/kfun"
	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/resolve"
	"github.com/mmcdole/dgdtools/pkg/syntax"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// File is the file a rename is asked in, as a lint has it.
type File struct {
	// Resolver answers what the places of the file name
	// (lint.Document's).
	Resolver *resolve.Resolver
	// AutosaveMarkers are the names whose mention says that an object
	// saves itself with save_object (conventions.autosave_markers).
	AutosaveMarkers []string
}

// Symbol is what a rename is about: a declaration.
type Symbol struct {
	// Kind is resolve.Local, Parameter, Function, Variable, Macro or
	// Label, and Name its name.
	Kind resolve.Kind
	Name string
	// Def is the declaration: the file, the bytes of the declared name.
	Def resolve.Def
}

// Target is what can be renamed at a place: the symbol, and the bytes
// of the name there, which an editor selects for the new name.
type Target struct {
	Symbol
	Start, End uint32
}

// Class says what an occurrence of the old name is.
type Class uint8

const (
	// Certain: the symbol.
	Certain Class = iota + 1
	// Possible: could be the symbol, and nobody can say.
	Possible
	// Excluded: provably something else.
	Excluded
)

func (c Class) String() string {
	switch c {
	case Certain:
		return "certain"
	case Possible:
		return "possible"
	case Excluded:
		return "excluded"
	}
	return "unknown"
}

// Occurrence is a place the old name stands at.
type Occurrence struct {
	Path       string
	Start, End uint32
	Class      Class
	// Decl says the place declares the symbol.
	Decl bool
	// Reason says why an occurrence is Possible or Excluded.
	Reason string
}

// Edit replaces the bytes [Start, End) of the file at Path.
type Edit struct {
	Path       string
	Start, End uint32
	NewText    string
}

// Plan is a rename as it is to be made.
type Plan struct {
	Symbol Symbol
	New    string
	// Occurrences are the places of the old name, in the order of their
	// files and of the files: the Certain ones are renamed.
	Occurrences []Occurrence
	// Edits are those of the Certain occurrences, in the same order.
	Edits []Edit
	// Warnings are what the person renaming should know that does not
	// stop the rename.
	Warnings []string
}

// Refusal is the reason a name cannot be renamed, or not to the name
// asked for.
type Refusal struct {
	Reason string
}

func (r *Refusal) Error() string { return r.Reason }

func refuse(format string, args ...any) error {
	return &Refusal{Reason: fmt.Sprintf(format, args...)}
}

// Prepare says what can be renamed at byte offset off of the file: the
// symbol and the name's place, or a *Refusal saying why it cannot be. It
// is nil, nil where nothing is named (a keyword, a comment, a number).
//
// A symbol that other files could name too (anything but a local, a
// parameter or a label) needs the whole-lib scan, to tell that no other
// file includes this one; Prepare runs it, for as long as ctx allows.
func Prepare(ctx context.Context, f File, off uint32) (*Target, error) {
	rn := newRenamer(f)
	if rn == nil {
		return nil, nil
	}
	return rn.prepare(ctx, off)
}

// Rename plans the renaming of what is named at byte offset off of the
// file to newName: Prepare's symbol, every occurrence of it and the
// edits; or a *Refusal. A new name that is the old one is a plan without
// edits.
func Rename(ctx context.Context, f File, off uint32, newName string) (*Plan, error) {
	rn := newRenamer(f)
	if rn == nil {
		return nil, refuse("nothing here can be renamed")
	}
	t, err := rn.prepare(ctx, off)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, refuse("nothing here can be renamed")
	}
	plan := &Plan{Symbol: t.Symbol, New: newName}
	if newName == t.Name {
		return plan, nil
	}
	if err := rn.valid(t.Symbol, newName); err != nil {
		return nil, err
	}
	if t.Kind == resolve.Macro {
		err = rn.planMacro(plan)
	} else {
		err = rn.planNames(plan)
	}
	if err != nil {
		return nil, err
	}
	for _, o := range plan.Occurrences {
		if o.Class == Certain {
			plan.Edits = append(plan.Edits, Edit{Path: o.Path, Start: o.Start, End: o.End, NewText: newName})
		}
	}
	return plan, nil
}

// renamer is a rename in one file.
// badSpan is code that does not parse, tokens [first, last], and why
// what it names cannot be told.
type badSpan struct {
	first, last int
	why         string
}

const notParsed = "in code that does not parse"

type renamer struct {
	r       *resolve.Resolver
	tok     *token.File
	tree    *syntax.File
	env     *pp.Env
	unit    *index.Unit
	snap    *index.Snapshot
	kfuns   *kfun.Table
	markers []string
	scopes  []*syntax.Scope
	// bad are the spans of the code that does not parse: what it names
	// cannot be told.
	bad []badSpan
}

func newRenamer(f File) *renamer {
	r := f.Resolver
	if r == nil || r.Tok == nil || r.Tree == nil || r.Tree.Sel == nil || r.Unit == nil || r.Snapshot == nil {
		return nil
	}
	rn := &renamer{r: r, tok: r.Tok, tree: r.Tree, env: r.Env, unit: r.Unit, snap: r.Snapshot,
		kfuns: r.Kfuns, markers: f.AutosaveMarkers}
	if rn.kfuns == nil {
		rn.kfuns = kfun.DGD()
	}
	rn.scopes = r.Tree.Scopes()
	add := func(n syntax.Node, why string) {
		first, last := n.Span()
		rn.bad = append(rn.bad, badSpan{first, last, why})
	}
	syntax.Inspect(r.Tree, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.BadDecl:
			add(n, rn.opaque(n, n.Opaque))
		case *syntax.BadExpr:
			add(n, notParsed)
		case *syntax.BadStmt:
			if !n.Missing {
				add(n, rn.opaque(n, n.Opaque))
			}
			if n.Resumed != nil {
				// Read after a skip, and evidence of nothing.
				add(n.Resumed, notParsed)
			}
		case *syntax.CallExpr:
			if n.Opaque && n.Fun != nil {
				// A macro's arguments that are no expressions.
				why := "in the arguments of the macro " + n.Fun.Name + ", which are not read as code"
				if m := rn.env.Lookup(n.Fun.Name); m != nil && strings.Contains(m.Body, "#") {
					why = "an argument of the macro " + n.Fun.Name + ", which makes a string of its arguments or pastes them (# or ##)"
				}
				add(n, why)
			}
		}
		return true
	})
	return rn
}

// opaque says why what code that does not parse names cannot be told:
// of code a macro made opaque, which macro, and whether it makes strings
// of its arguments.
func (rn *renamer) opaque(n syntax.Node, opaque bool) string {
	if !opaque {
		return notParsed
	}
	first, last := n.Span()
	toks := rn.tok.Tokens
	for ti := first; ti <= last && ti < len(toks); ti++ {
		if toks[ti].Kind != token.Ident {
			continue
		}
		name := rn.text(ti)
		m := rn.env.Lookup(name)
		switch {
		case m == nil:
		case m.FuncLike && strings.Contains(m.Body, "#"):
			return "an argument of the macro " + name + ", which makes a string of its arguments or pastes them (# or ##)"
		case m.Class == pp.ClassStatement || m.Class == pp.ClassFragment:
			return "in code the macro " + name + " makes unreadable"
		}
	}
	return notParsed
}

// ---------------------------------------------------------------- the place

// tokenAt returns the index of the token at off: a name that ends at off
// is at it, as an editor's cursor at the end of a word is.
func (rn *renamer) tokenAt(off uint32) int {
	toks := rn.tok.Tokens
	ti := rn.tok.TokenAt(off)
	if toks[ti].Kind != token.Ident && ti > 0 && toks[ti].Off == off && toks[ti-1].Kind == token.Ident {
		ti--
	}
	return ti
}

func (rn *renamer) compiled(ti int) bool {
	_, ok := slices.BinarySearch(rn.tree.Sel.Sig, ti)
	return ok
}

// unparsed says why the token at index ti is in code that does not
// parse, the innermost first; "" when it is not.
func (rn *renamer) unparsed(ti int) string {
	why := ""
	for _, b := range rn.bad {
		if b.first <= ti && ti <= b.last {
			why = b.why
		}
	}
	return why
}

func (rn *renamer) text(ti int) string { return string(rn.tok.Text(rn.tok.Tokens[ti])) }

func (rn *renamer) line(off uint32) int { return rn.tok.Pos(off).Line }

// where names a place of a declaration for a message: "line 12" in the
// file, else the file's lib path or its path and the line.
func (rn *renamer) where(d resolve.Def) string {
	if d.Path == rn.tok.Path {
		return fmt.Sprintf("line %d", rn.line(d.Start))
	}
	if _, file, ok := profile.SplitLocation(d.Path); ok {
		return file + " (the mudlib profile)"
	}
	if lf := rn.snap.LibFile(d.Path); lf != "" {
		return lf
	}
	return d.Path
}

func (rn *renamer) libName(p string) string {
	if lf := rn.snap.LibFile(p); lf != "" {
		return lf
	}
	return p
}

// prepare is Prepare.
func (rn *renamer) prepare(ctx context.Context, off uint32) (*Target, error) {
	ti := rn.tokenAt(off)
	ref := rn.r.At(off)
	if ref == nil {
		if rn.tok.Tokens[ti].Kind != token.Ident {
			return nil, nil
		}
		name := rn.text(ti)
		switch {
		case token.Keywords[name] != 0:
			return nil, nil
		case !rn.compiled(ti):
			return nil, refuse("'%s' is in code that is not compiled here (a branch of #if that is not taken): what it names cannot be told", name)
		case rn.unparsed(ti) != "":
			return nil, refuse("'%s' is %s: what it names cannot be told", name, rn.unparsed(ti))
		}
		return nil, refuse("'%s' names nothing the tools know of here", name)
	}
	t := &Target{Symbol: Symbol{Kind: ref.Kind, Name: ref.Name}, Start: ref.Start, End: ref.End}
	switch ref.Kind {
	case resolve.Include, resolve.Object:
		return nil, refuse("'%s' names a file: renaming files comes in a later version", ref.Name)
	case resolve.Local, resolve.Parameter:
		t.Def = ref.Defs[0]
		return t, nil
	case resolve.Label:
		// The file's own, unless another file includes it.
	case resolve.Function:
		if err := rn.function(ref); err != nil {
			return nil, err
		}
	case resolve.Variable:
		if err := rn.variable(ref); err != nil {
			return nil, err
		}
	case resolve.Macro:
		if err := rn.macro(ref); err != nil {
			return nil, err
		}
	default:
		return nil, nil
	}
	t.Def = ref.Defs[0]
	if err := rn.included(ctx); err != nil {
		return nil, err
	}
	if ref.Kind == resolve.Variable && !t.Def.Var.Static {
		if err := rn.saved(ctx, ref.Name); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// Later is what a refusal says of what this version does not rename.
const Later = "rename across files comes in a later version"

// stub reports whether a declaration is in a profile's stub.
func stub(d resolve.Def) bool {
	_, _, ok := profile.SplitLocation(d.Path)
	return ok
}

// function refuses a function that is not the file's own private one.
func (rn *renamer) function(ref *resolve.Ref) error {
	name := ref.Name
	switch {
	case len(ref.Defs) == 0 && ref.Call != nil && ref.Call.Target == calls.TargetUnknown:
		return refuse("'%s' is called in an object that cannot be told: what it names is not known", name)
	case len(ref.Defs) == 0 && rn.kfuns.Lookup(name) != nil:
		return refuse("'%s' is a kfun, a function of the driver", name)
	case len(ref.Defs) == 0:
		return refuse("no file the tools see declares a function '%s'", name)
	case len(ref.Defs) > 1:
		return refuse("'%s' is ambiguous here: more than one inherited object has it", name)
	}
	d := ref.Defs[0]
	fi := d.Func
	switch {
	case stub(d):
		return refuse("'%s' is defined in the mudlib profile, not in your checkout", name)
	case d.Auto:
		return refuse("'%s' is a function of the auto object, which every object has: %s", name, Later)
	case d.Object != rn.unit.LibPath:
		return refuse("'%s' is inherited from %s: %s", name, d.Object, Later)
	case d.Path != rn.tok.Path:
		return refuse("'%s' is declared in %s, which other files may include: %s", name, rn.libName(d.Path), Later)
	case fi == nil:
		return refuse("nothing is known of the function '%s'", name)
	case !fi.Private:
		what := "public"
		if fi.Static {
			what = "static"
		}
		return refuse("'%s' is a %s function: objects that inherit this one or call it may name it; %s", name, what, Later)
	case ref.Call != nil:
		return refuse("DGD does not call a private function by name (%s): this is not a call of '%s'; rename it where it is declared or called", via(ref), name)
	}
	return nil
}

// via says how a late-bound call is made, for a message.
func via(ref *resolve.Ref) string {
	if ref.Call.Via == "->" {
		return "->"
	}
	return ref.Call.Via + "()"
}

// variable refuses a global variable that is not the file's own private
// one.
func (rn *renamer) variable(ref *resolve.Ref) error {
	name := ref.Name
	switch {
	case len(ref.Defs) == 0:
		return refuse("no file the tools see declares a variable '%s'", name)
	case len(ref.Defs) > 1:
		return refuse("'%s' is ambiguous here: more than one inherited object has it", name)
	}
	d := ref.Defs[0]
	switch {
	case stub(d):
		return refuse("'%s' is declared in the mudlib profile, not in your checkout", name)
	case d.Object != rn.unit.LibPath:
		return refuse("'%s' is inherited from %s: %s", name, d.Object, Later)
	case d.Path != rn.tok.Path:
		return refuse("'%s' is declared in %s, which other files may include: %s", name, rn.libName(d.Path), Later)
	case d.Var == nil:
		return refuse("nothing is known of the variable '%s'", name)
	case !d.Var.Private:
		return refuse("'%s' is a global variable that objects inheriting this one can use: %s", name, Later)
	}
	return nil
}

// macro refuses a macro that is not one the file defines for itself.
func (rn *renamer) macro(ref *resolve.Ref) error {
	name := ref.Name
	if len(ref.Defs) == 0 {
		return refuse("'%s' is defined by the driver or the project's configuration, not in a file", name)
	}
	d := ref.Defs[0]
	switch {
	case stub(d):
		return refuse("'%s' is defined in the mudlib profile, not in your checkout", name)
	case d.Path != rn.tok.Path:
		return refuse("'%s' is defined in %s: renaming a header's macro comes in a later version", name, rn.libName(d.Path))
	case rn.env.Outside(name):
		return refuse("'%s' is defined outside this file too (by a header it includes, the driver or the project's configuration)", name)
	}
	if h := rn.mentionedInHeaders(name); h != "" {
		return refuse("'%s' is used in %s, which this file includes: %s", name, h, Later)
	}
	if what := rn.outsideName(name); what != "" {
		// The name means something else where the macro is not defined.
		if own := rn.env.Own(); own != nil && own.Undefs[name] {
			return refuse("'%s' is #undef'd in this file, and is %s where it is not a macro", name, what)
		}
		first := ^uint32(0)
		for _, m := range rn.ownDefines(name) {
			first = min(first, m)
		}
		for ti, tok := range rn.tok.Tokens {
			if tok.Kind == token.Ident && tok.Off < first && rn.text(ti) == name {
				return refuse("'%s' at line %d, before its #define, is %s", name, rn.line(tok.Off), what)
			}
		}
	}
	return nil
}

// outsideName says what else name is in the file's object, for a
// message: a function or a global variable it inherits, the auto
// object's function, a kfun; "" when nothing.
func (rn *renamer) outsideName(name string) string {
	lp := rn.unit.LibPath
	if lp != "" {
		if fn, ok := rn.snap.ChainFunc(lp, name); ok && fn.DefinedIn != lp && !fn.Private {
			return "a function inherited from " + fn.DefinedIn
		}
		if v, ok := rn.snap.Chain(lp).Vars[name]; ok && v.DefinedIn != lp && !v.Private {
			return "a global variable inherited from " + v.DefinedIn
		}
	}
	if _, ok := rn.snap.AutoFunc(name); ok {
		return "a function of the auto object"
	}
	if rn.kfuns.Lookup(name) != nil {
		return "a kfun"
	}
	return ""
}

// ownDefines returns where the file's #defines of name have the name.
func (rn *renamer) ownDefines(name string) []uint32 {
	var out []uint32
	for _, w := range rn.directiveWords() {
		if w.name == name && w.define {
			out = append(out, w.off)
		}
	}
	return out
}

// included refuses when another file of the lib includes this one: what
// it declares is theirs too.
func (rn *renamer) included(ctx context.Context) error {
	includers, ok := rn.snap.Includers(ctx, rn.tok.Path)
	if !ok {
		return rn.unknown("whether another file includes this one")
	}
	if len(includers) > 0 {
		return refuse("this file is included by %s, which has what it declares too: %s", rn.libName(includers[0]), Later)
	}
	return nil
}

// unknown refuses for want of an answer from the whole lib: the scan is
// not complete, or files changed since the question was asked.
func (rn *renamer) unknown(what string) error {
	if !rn.snap.Current() {
		return refuse("files changed while the rename was being planned, so it cannot be told %s: try again", what)
	}
	return refuse("the mudlib is not indexed in full yet, so it cannot be told %s: try again in a moment", what)
}

// saved refuses a global variable that save_object writes: one of an
// object that saves itself, or of an object that inherits it, or is made
// of programs one of which does. The object saves itself when one of
// its programs names save_object, restore_object or one of the
// conventions' autosave markers, or a macro of the file that expands to
// one; the auto object is taken to wrap them, not to call them on its
// own.
func (rn *renamer) saved(ctx context.Context, name string) error {
	names := map[string]bool{"save_object": true, "restore_object": true}
	for _, m := range rn.markers {
		names[m] = true
	}
	if rn.env != nil {
		for _, m := range rn.env.Names() {
			if names[m] {
				continue
			}
			exp := map[string]bool{}
			rn.expansion(m, map[string]bool{}, exp)
			for n := range exp {
				if names[n] {
					names[m] = true
					break
				}
			}
		}
	}
	refuseSaved := func(who string) error {
		return refuse("'%s' is written by save_object to the save files under its name (%s): renamed, restore_object no longer finds the saved value", name, who)
	}
	for _, ti := range rn.tree.Sel.Sig {
		if tok := rn.tok.Tokens[ti]; tok.Kind == token.Ident && names[rn.text(ti)] {
			return refuseSaved(fmt.Sprintf("this file names %s at line %d", rn.text(ti), rn.line(tok.Off)))
		}
	}
	lp := rn.unit.LibPath
	if lp == "" {
		return nil
	}
	if rn.snap.Partial() && rn.snap.Shared(rn.snap.LibFile(rn.tok.Path)) {
		return refuse("'%s' may be written by save_object: objects of the mudlib outside this checkout inherit this file, and cannot be seen from here", name)
	}
	users, ok := rn.snap.Users(ctx, lp)
	if !ok {
		return rn.unknown(fmt.Sprintf("whether an object that inherits this one saves '%s'", name))
	}
	programs := map[string]bool{}
	for _, o := range rn.snap.Chain(lp).Objects {
		programs[o] = true
	}
	for _, u := range users {
		programs[u] = true
		for _, o := range rn.snap.Chain(u).Objects {
			programs[o] = true
		}
	}
	delete(programs, lp)
	for _, p := range slices.Sorted(maps.Keys(programs)) {
		if ctx.Err() != nil {
			return refuse("cancelled before it could be told whether an object that inherits this one saves '%s'", name)
		}
		if rn.snap.AutoMember(p) {
			continue
		}
		u := rn.snap.Unit(p)
		if u == nil {
			continue
		}
		for n := range names {
			if len(u.Uses(n)) > 0 {
				return refuseSaved(fmt.Sprintf("%s names %s", p, n))
			}
		}
	}
	return nil
}

// expansion adds to out the identifiers the macro called name expands
// to, through the macros of its body, but for its parameters.
func (rn *renamer) expansion(name string, seen, out map[string]bool) {
	if rn.env == nil || seen[name] {
		return
	}
	seen[name] = true
	m := rn.env.Lookup(name)
	if m == nil {
		return
	}
	for _, t := range m.Toks {
		if t.Kind != token.Ident || slices.Contains(m.Params, t.Text) {
			continue
		}
		out[t.Text] = true
		rn.expansion(t.Text, seen, out)
	}
}

// ---------------------------------------------------------------- headers

// mentionedInHeaders returns the first of the files the file includes
// (directly or not, and the header read before it) that has name as an
// identifier, in its code or its directives; "" when none has.
func (rn *renamer) mentionedInHeaders(name string) string {
	if len(rn.unit.Deps) < 2 {
		return ""
	}
	for _, p := range rn.unit.Deps[1:] {
		src, err := rn.snap.ReadFile(p)
		if err != nil {
			continue
		}
		if !strings.Contains(string(src), name) {
			continue
		}
		f := lexer.Lex(p, src, rn.tok.Dialect)
		for _, tok := range f.Tokens {
			switch tok.Kind {
			case token.Ident:
				if string(f.Text(tok)) == name {
					return rn.libName(p)
				}
			case token.Directive:
				text := f.Text(tok)
				arg := pp.ArgOffset(text)
				for _, w := range lexer.Lex("", text[arg:], rn.tok.Dialect).Tokens {
					if w.Kind == token.Ident && string(text[arg:][w.Off:w.End]) == name {
						return rn.libName(p)
					}
				}
			}
		}
	}
	return ""
}

// word is an identifier in a directive of the file that names a macro
// or may: in #define, #undef, #if, #ifdef, #ifndef and #elif, read or
// not.
type word struct {
	name   string
	off    uint32
	define bool // the name a #define defines
	tok    int  // the directive's token
}

func (rn *renamer) directiveWords() []word {
	var out []word
	for ti, tok := range rn.tok.Tokens {
		if tok.Kind != token.Directive {
			continue
		}
		text := rn.tok.Text(tok)
		dir, _ := pp.SplitDirective(text)
		switch dir {
		case "define", "undef", "if", "ifdef", "ifndef", "elif":
		default:
			continue
		}
		arg := pp.ArgOffset(text)
		first := true
		for _, w := range lexer.Lex("", text[arg:], rn.tok.Dialect).Tokens {
			if w.Kind.IsTrivia() {
				continue
			}
			if w.Kind == token.Ident {
				out = append(out, word{name: string(text[arg:][w.Off:w.End]), off: tok.Off + uint32(arg) + w.Off,
					define: first && dir == "define", tok: ti})
			}
			first = false
		}
	}
	return out
}
