package resolve

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/mmcdole/dgdtools/internal/par"
	"github.com/mmcdole/dgdtools/pkg/calls"
	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/syntax"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// Extractor returns the index's extractor of the Uses of a file
// (index.WithUses), with the project's registry of the functions that
// take a function's name or an object's path (nil: the driver's). A
// file's Uses are exactly the places At names a function, a global
// variable, a macro, an object or an included file at, so that a search
// of the index finds what At would find in every file, without reading
// them; and the name of a macro in an inherit statement, which At gives
// to the object inherited, is a Use of the macro too.
func Extractor(reg *calls.Registry) index.Extractor {
	return func(p *index.Parsed, s *index.Snapshot) []index.Use {
		r := New(File{Tok: p.Tok, Tree: p.Tree, Env: p.Env, Unit: p.Unit, Snapshot: s, Registry: reg})
		return r.uses()
	}
}

// uses returns the Uses of the file, in no particular order and possibly
// more than once each.
func (r *Resolver) uses() []index.Use {
	f := &r.File
	if f.Tok == nil || f.Tree == nil || f.Tree.Sel == nil || f.Unit == nil || f.Snapshot == nil {
		return nil
	}
	var out []index.Use
	add := func(ref *Ref, u *index.Use) {
		if ref == nil || u == nil {
			return
		}
		if u.Kind == index.UseInclude {
			// The file, however the #include writes its name; one that
			// is not there names nothing to search for.
			if u.Aux == "" {
				return
			}
			u.Name = u.Aux
		}
		r.place(u)
		out = append(out, *u)
	}
	toks := f.Tok.Tokens
	var names []int
	for _, ti := range f.Tree.Sel.Sig {
		if k := toks[ti].Kind; k == token.Ident || k == token.StringLit {
			names = append(names, ti)
		}
	}
	syntax.PathsAt(f.Tree, names, func(ti int, path []syntax.Node) {
		tok := toks[ti]
		if tok.Kind == token.StringLit {
			add(r.str(ti, path))
			return
		}
		ref, u := r.ident(ti, path)
		add(ref, u)
		if ref != nil && ref.Kind == Object {
			// `inherit ROOM;` is about the room, and names the macro all
			// the same.
			add(r.macro(string(f.Tok.Text(tok)), tok.Off))
		}
	})
	// The calls whose function's name is made when they run, which a
	// rename lists.
	for _, d := range r.calls().Dynamic {
		u := index.Use{Name: index.DynamicName, Start: d.Start, End: d.End, Kind: index.UseDynamic,
			Aux: d.Prefix + "*" + d.Suffix}
		r.place(&u)
		out = append(out, u)
	}
	for ti, tok := range toks {
		if tok.Kind != token.Directive {
			continue
		}
		text := f.Tok.Text(tok)
		switch name, _ := pp.SplitDirective(text); name {
		case "include":
			add(r.directive(ti, tok.Off))
		case "define", "undef", "if", "ifdef", "ifndef", "elif":
			arg := pp.ArgOffset(text)
			for _, w := range lexWords(text[arg:], f.Tok.Dialect) {
				add(r.directive(ti, tok.Off+uint32(arg)+w))
			}
		}
	}
	return out
}

// lexWords returns the offsets of the identifiers in the words of a
// directive.
func lexWords(text []byte, d token.Dialect) []uint32 {
	var out []uint32
	for _, w := range lexer.Lex("", text, d).Tokens {
		if w.Kind == token.Ident {
			out = append(out, w.Off)
		}
	}
	return out
}

// place sets where a Use is in lines and columns.
func (r *Resolver) place(u *index.Use) {
	pos := r.Tok.Pos(u.Start)
	u.Line, u.Col = uint32(pos.Line), uint32(pos.Col)
	src := r.Tok.Src
	u.ASCII = true
	for i := u.Start - (u.Col - 1); i < u.End && int(i) < len(src); i++ {
		if c := src[i]; c >= 0x80 || i >= u.Start && (c == '\n' || c == '\r') {
			u.ASCII = false
			break
		}
	}
}

// fileUses returns the Uses of the resolver's file: the unit's, or, of a
// unit made without them, read from the file now.
func (r *Resolver) fileUses() []index.Use {
	if r.u.HasUses() {
		return r.u.AllUses()
	}
	r.ownUses.Do(func() {
		uses := r.uses()
		slices.SortFunc(uses, func(a, b index.Use) int {
			return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Start, b.Start))
		})
		r.own = slices.CompactFunc(uses, func(a, b index.Use) bool {
			return a.Name == b.Name && a.Start == b.Start && a.End == b.End && a.Kind == b.Kind
		})
	})
	return r.own
}

// Access says how a place uses a variable, for a highlight of it.
type Access uint8

const (
	// Text: the place is no variable's, or how it uses one is not told.
	Text Access = iota
	// Read: the variable's value is read.
	Read
	// Write: the variable is declared, assigned or incremented there.
	Write
)

// Site is a place that names what a search for references is about.
type Site struct {
	// Path is the file, a path on disk or the location of a profile's
	// stub; Start and End are the bytes that name it.
	Path       string
	Start, End uint32
	// Line and Col are where Start is, 1-based, Col counting bytes; 0
	// when not known, and then the file's text says. ASCII says the
	// line holds nothing but ASCII up to End and the place is on one
	// line (index.Use.ASCII).
	Line, Col uint32
	ASCII     bool
	// Decl says the place declares the name (Ref.Decl), or is the
	// declaration itself.
	Decl bool
	// Access is how the place uses a variable, a local or a parameter;
	// it is told only for the places of the resolver's own file
	// (Highlights).
	Access Access
}

// References is what a search for the references of a name found.
type References struct {
	// Ref is what the search is about: the place it started from.
	Ref *Ref
	// Sites are the places that name it, in the order of their files'
	// paths and then of the files.
	Sites []Site
	// Files is how many files were searched. Complete says they are all
	// the files of the lib that can name it: the whole-lib scan is
	// complete, or the name is a local, a parameter or a label, which
	// only its own file names.
	Files    int
	Complete bool
	// Partial says the lib is known only in part, a checkout of one
	// part of it and a profile's stubs for the rest: what the rest of
	// the lib names cannot be searched, so the search is complete for
	// the checkout only.
	Partial bool
	// Possible counts the calls of a function's name, made in an object
	// nobody can tell (ob->name(), call_other(ob, "name")), that may
	// call it and may not; PossibleFiles is in how many files. They are
	// not among the Sites: nothing proves that they are.
	Possible, PossibleFiles int
}

// key identifies a declaration.
type key struct {
	kind  Kind
	path  string
	start uint32
}

func keyOf(d Def) key { return key{d.Kind, d.Path, d.Start} }

// References finds the places that name what the place at byte offset
// off names, in the resolver's generation of the index: the places where
// At gives one of the same declarations. A local, a parameter and a
// label are searched for in the file; a function, a global variable, a
// macro, an object and an included file in every file of the lib
// (Snapshot.UseUnits), which needs the whole-lib scan: without it the
// search covers what is indexed so far, and says so (Complete). With
// decl, the declarations are among the places, and the declaration
// itself is, wherever it is (a profile's stub too).
//
// It is nil where At is nil, and where what is named is declared
// nowhere the tools see: a function of the driver's, a call on an
// unknown receiver, a macro only the configuration defines. Nothing is
// guessed: a call on an object nobody can tell is counted as Possible,
// not listed.
func (r *Resolver) References(ctx context.Context, off uint32, decl bool) *References {
	ref := r.At(off)
	if ref == nil || len(ref.Defs) == 0 {
		return nil
	}
	res := &References{Ref: ref, Partial: r.s.Partial()}
	switch ref.Kind {
	case Local, Parameter, Label:
		res.Sites = r.inFile(ref)
		res.Files, res.Complete = 1, true
	default:
		r.inLib(ctx, ref, res)
	}
	if decl {
		have := map[Site]bool{}
		for _, s := range res.Sites {
			have[Site{Path: s.Path, Start: s.Start, End: s.End}] = true
		}
		for _, d := range ref.Defs {
			if d.Kind == Object || d.Kind == Include {
				continue // a file as a whole is not a place in it
			}
			if s := (Site{Path: d.Path, Start: d.Start, End: d.End}); !have[s] {
				have[s] = true
				s.Decl = true
				res.Sites = append(res.Sites, s)
			}
		}
	} else {
		res.Sites = slices.DeleteFunc(res.Sites, func(s Site) bool { return s.Decl })
	}
	slices.SortFunc(res.Sites, func(a, b Site) int {
		return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Start, b.Start), cmp.Compare(a.End, b.End))
	})
	return res
}

// Highlights returns the places of the resolver's file that name what
// the place at byte offset off names (References, in this file only, the
// declarations included), each with how it uses a variable, a local or a
// parameter. It is nil where At is nil or names nothing declared.
func (r *Resolver) Highlights(off uint32) []Site {
	ref := r.At(off)
	if ref == nil || len(ref.Defs) == 0 {
		return nil
	}
	var sites []Site
	switch ref.Kind {
	case Local, Parameter, Label:
		sites = r.inFile(ref)
	default:
		want := map[key]bool{}
		for _, d := range ref.Defs {
			want[keyOf(d)] = true
		}
		sites, _ = r.matches(r.Tok.Path, r, r.fileUses(), ref, want)
	}
	for i := range sites {
		if ref.Kind == Variable || ref.Kind == Local || ref.Kind == Parameter {
			sites[i].Access = r.access(sites[i])
		}
	}
	slices.SortFunc(sites, func(a, b Site) int { return cmp.Compare(a.Start, b.Start) })
	return sites
}

// inFile returns the places of the file that name the local, the
// parameter or the label ref names: its identifiers of the name, asked
// of At one by one.
func (r *Resolver) inFile(ref *Ref) []Site {
	want := ref.Defs[0]
	var out []Site
	for _, ti := range r.Tree.Sel.Sig {
		tok := r.Tok.Tokens[ti]
		if tok.Kind != token.Ident || string(r.Tok.Text(tok)) != ref.Name {
			continue
		}
		at := r.At(tok.Off)
		if at == nil || at.Kind != ref.Kind || len(at.Defs) == 0 || at.Defs[0].Start != want.Start {
			continue
		}
		pos := r.Tok.Pos(at.Start)
		out = append(out, Site{Path: r.Tok.Path, Start: at.Start, End: at.End,
			Line: uint32(pos.Line), Col: uint32(pos.Col), Decl: at.Decl})
	}
	return out
}

// inLib searches the files of the lib for the places that name one of
// the declarations of ref.
func (r *Resolver) inLib(ctx context.Context, ref *Ref, res *References) {
	want := map[key]bool{}
	for _, d := range ref.Defs {
		want[keyOf(d)] = true
	}
	units, complete := r.s.UseUnits(ctx)
	own := r.Tok.Path
	var (
		mu       sync.Mutex
		possible atomic.Int64
		files    atomic.Int64
	)
	collect := func(sites []Site, maybe int) {
		if maybe > 0 {
			possible.Add(int64(maybe))
			files.Add(1)
		}
		if len(sites) > 0 {
			mu.Lock()
			res.Sites = append(res.Sites, sites...)
			mu.Unlock()
		}
	}
	collect(r.matches(own, r, r.fileUses(), ref, want))
	par.Each(ctx, units, func(u *index.Unit) {
		if u.Path == own {
			return
		}
		other := New(File{Unit: u, Snapshot: r.s, Registry: r.Registry, Kfuns: r.Kfuns})
		collect(other.matches(u.Path, other, u.Uses(searched(ref)), ref, want))
	})
	res.Files = len(units)
	if !slices.ContainsFunc(units, func(u *index.Unit) bool { return u.Path == own }) {
		res.Files++
	}
	res.Complete = complete && ctx.Err() == nil
	res.Possible, res.PossibleFiles = int(possible.Load()), int(files.Load())
}

// matches returns the places among uses, the Uses of the file at path,
// that look up (with lookup, the file's resolver) to one of the
// declarations wanted; and, for a function, how many of the uses of its
// name are calls on an object nobody can tell.
func (r *Resolver) matches(path string, lookup *Resolver, uses []index.Use, ref *Ref, want map[key]bool) ([]Site, int) {
	var out []Site
	maybe := 0
	name := searched(ref)
	for _, u := range uses {
		if u.Name != name {
			continue
		}
		if u.Kind == index.UseFunction && u.Form == index.FormUnknown {
			if ref.Kind == Function {
				maybe++
			}
			continue
		}
		if kindOf(u.Kind) != ref.Kind {
			continue
		}
		for _, d := range lookup.Lookup(u) {
			if want[keyOf(d)] {
				out = append(out, Site{Path: path, Start: u.Start, End: u.End,
					Line: u.Line, Col: u.Col, ASCII: u.ASCII, Decl: u.Decl})
				break
			}
		}
	}
	return out, maybe
}

// searched returns the name the Uses of what ref names have in the
// index: an included file's path, however each #include writes it; the
// name of anything else.
func searched(ref *Ref) string {
	if ref.Kind == Include {
		return ref.Defs[0].Path
	}
	return ref.Name
}

// kindOf returns the kind of what a Use of the kind names.
func kindOf(k index.UseKind) Kind {
	switch k {
	case index.UseFunction:
		return Function
	case index.UseVariable:
		return Variable
	case index.UseMacro:
		return Macro
	case index.UseObject:
		return Object
	case index.UseInclude:
		return Include
	}
	return 0
}

// access says how the place of the file uses its variable.
func (r *Resolver) access(s Site) Access {
	if s.Decl {
		return Write
	}
	ti := r.Tok.TokenAt(s.Start)
	path := syntax.PathAt(r.Tree, ti)
	if len(path) < 2 {
		return Read
	}
	var x syntax.Node = path[0]
	parent := 1
	for parent < len(path) {
		if p, ok := path[parent].(*syntax.ParenExpr); ok && syntax.Node(p.X) == x {
			x = p
			parent++
			continue
		}
		break
	}
	if parent >= len(path) {
		return Read
	}
	switch p := path[parent].(type) {
	case *syntax.AssignExpr:
		if syntax.Node(p.L) == x {
			return Write
		}
	case *syntax.UnaryExpr:
		if (p.Op == token.Inc || p.Op == token.Dec) && syntax.Node(p.X) == x {
			return Write
		}
	case *syntax.PostfixExpr:
		if (p.Op == token.Inc || p.Op == token.Dec) && syntax.Node(p.X) == x {
			return Write
		}
	}
	return Read
}
