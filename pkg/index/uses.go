package index

import (
	"cmp"
	"context"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unique"

	"github.com/mmcdole/dgdtools/internal/par"
	"github.com/mmcdole/dgdtools/pkg/pp"
)

// UseKind says what a Use names.
type UseKind uint8

const (
	// UseFunction is a function: called, handed as a callback, pointed
	// to with &, defined or declared.
	UseFunction UseKind = iota + 1
	// UseVariable is a global variable, used or declared.
	UseVariable
	// UseMacro is a macro, used or defined.
	UseMacro
	// UseObject is an object, named by its path: in an inherit
	// statement, or handed to a function that takes one.
	UseObject
	// UseInclude is a file an #include names.
	UseInclude
)

// UseForm says how a Use of a function names it, which is what decides
// where the name is looked up.
type UseForm uint8

const (
	// FormOwn is name(), &name, a definition or a prototype: the file's
	// own function, else the inherited one, else the auto object's.
	FormOwn UseForm = iota
	// FormSuper is ::name(): the inherited function, else the auto
	// object's.
	FormSuper
	// FormLabel is label::name(): the function of the objects inherited
	// with that label (Use.Aux).
	FormLabel
	// FormSelf is a call bound when it runs, made in this_object(): x->name()
	// with x being this_object(), a callback of the object itself. The
	// name is looked up as for FormOwn.
	FormSelf
	// FormPath is a call bound when it runs, made in an object whose path
	// the source says (Use.Aux): the function in that object's chain,
	// else the auto object's.
	FormPath
	// FormUnknown is a call bound when it runs, made in an object nobody
	// can tell. It names no declaration; it is a place that may call any
	// function of the name.
	FormUnknown
)

// Use is a place in a file that names something another file can name
// too: a function, a global variable, a macro, an object, an included
// file. It is what the file alone says of the place, before the name is
// looked up: the form of a call and its receiver, but not the function
// it reaches, which depends on the objects the file inherits and is
// looked up in the generation that asks (see resolve). Locals,
// parameters and inherit labels are no Uses: only their own file names
// them.
//
// A unit has the Uses of its file when the index was opened with an
// extractor (WithUses); they are made with the unit and go with it, so
// they are as fresh as the unit's other facts.
type Use struct {
	// Name is what is named: a function, a variable or a macro by its
	// name, an object by its lib path; an included file by its name as
	// written, and in a unit's Uses by its path, the same however each
	// #include writes it (a unit keeps no #include that resolves to
	// nothing).
	Name string
	// Start and End are the bytes that name it: the identifier, the
	// callback's name inside its quotes, the path of an inherit
	// statement, the target of an #include. Line and Col are where Start
	// is, 1-based, Col counting bytes.
	Start, End uint32
	Line, Col  uint32
	Kind       UseKind
	Form       UseForm
	// Decl says the place declares the name: a function's definition or
	// prototype, a global variable's declarator, a macro's #define.
	Decl bool
	// ASCII says the line holds nothing but ASCII up to End and the place
	// is on one line: a column counted in UTF-16 code units is then Col,
	// and End is on the line at Col+End-Start.
	ASCII bool
	// Aux is, by Kind and Form: the label of FormLabel; the lib path of
	// the object FormPath calls; the file whose #define a macro is ("":
	// the configuration's, defined nowhere); the file an #include
	// resolves to ("": none).
	Aux string
	// Off is where a macro's name stands in its #define, in Aux.
	Off uint32
	// Macro is the #define of a macro, when the file's environment has
	// it. A unit does not keep it (Unit.Uses has none): that would keep
	// every file's macros.
	Macro *pp.Macro
}

// Extractor reads the Uses of a file, from its analysis and the unit
// being built of it (complete but for its Uses). It is called once per
// unit, by whichever goroutine builds it, and must ask the snapshot
// nothing that builds another unit.
type Extractor func(p *Parsed, s *Snapshot) []Use

// Option configures an index.
type Option func(*lib)

// WithUses makes the index read the Uses of every unit it builds with
// extract (resolve.Extractor), which is what a search for the
// references of a name needs (Snapshot.UseUnits).
func WithUses(extract Extractor) Option { return func(l *lib) { l.uses = extract } }

// Uses returns the file's Uses of the name, in the order of the file.
func (u *Unit) Uses(name string) []Use {
	us := u.uses
	if us == nil {
		return nil
	}
	n, found := slices.BinarySearchFunc(us.names, name, func(h unique.Handle[string], name string) int {
		return strings.Compare(h.Value(), name)
	})
	if !found {
		return nil
	}
	i := sort.Search(len(us.recs), func(i int) bool { return us.recs[i].name >= uint32(n) })
	var out []Use
	for ; i < len(us.recs) && us.recs[i].name == uint32(n); i++ {
		out = append(out, us.use(&us.recs[i]))
	}
	return out
}

// AllUses returns the file's Uses, by name and then in the order of the
// file.
func (u *Unit) AllUses() []Use {
	us := u.uses
	if us == nil {
		return nil
	}
	out := make([]Use, len(us.recs))
	for i := range us.recs {
		out[i] = us.use(&us.recs[i])
	}
	return out
}

// HasUses reports whether the unit's Uses were read: the index has an
// extractor, and the file is not a profile's stub.
func (u *Unit) HasUses() bool { return u.uses != nil }

// uses are a unit's Uses as kept: a whole lib has millions, so a Use is
// a record of 28 bytes, its name and its Aux by number.
type uses struct {
	// names are the names used, sorted; recs refer to them by index.
	names []unique.Handle[string]
	// aux are the distinct Aux and Off of the Uses; a rec refers to one
	// by index plus one, 0 being none.
	aux  []useAux
	recs []useRec
}

type useAux struct {
	aux unique.Handle[string]
	off uint32
}

type useRec struct {
	start, end, line uint32
	name, aux        uint32
	col              uint16 // 0: more than a uint16 holds; Line and Col are then not kept
	kind             UseKind
	form             UseForm
	flags            uint8
}

const (
	recDecl = 1 << iota
	recASCII
)

func (us *uses) use(r *useRec) Use {
	u := Use{Name: us.names[r.name].Value(), Start: r.start, End: r.end, Kind: r.kind, Form: r.form,
		Decl: r.flags&recDecl != 0, ASCII: r.flags&recASCII != 0}
	if r.col != 0 {
		u.Line, u.Col = r.line, uint32(r.col)
	}
	if r.aux > 0 {
		a := us.aux[r.aux-1]
		u.Aux, u.Off = a.aux.Value(), a.off
	}
	return u
}

// setUses keeps the Uses of the unit, sorted by name, then place, each
// once.
func (u *Unit) setUses(in []Use) {
	slices.SortFunc(in, func(a, b Use) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), cmp.Compare(a.Start, b.Start),
			cmp.Compare(a.End, b.End), cmp.Compare(a.Kind, b.Kind))
	})
	in = slices.CompactFunc(in, func(a, b Use) bool {
		return a.Name == b.Name && a.Start == b.Start && a.End == b.End && a.Kind == b.Kind
	})
	us := &uses{recs: make([]useRec, 0, len(in))}
	auxes := map[useAux]uint32{}
	for _, x := range in {
		if len(us.names) == 0 || us.names[len(us.names)-1].Value() != x.Name {
			us.names = append(us.names, unique.Make(x.Name))
		}
		r := useRec{start: x.Start, end: x.End, line: x.Line, name: uint32(len(us.names) - 1),
			kind: x.Kind, form: x.Form}
		if x.Col <= 0xffff {
			r.col = uint16(x.Col)
		}
		if x.Decl {
			r.flags |= recDecl
		}
		if x.ASCII && r.col != 0 {
			r.flags |= recASCII
		}
		if x.Aux != "" || x.Off != 0 {
			a := useAux{aux: unique.Make(x.Aux), off: x.Off}
			i, ok := auxes[a]
			if !ok {
				us.aux = append(us.aux, a)
				i = uint32(len(us.aux))
				auxes[a] = i
			}
			r.aux = i
		}
		us.recs = append(us.recs, r)
	}
	us.names = slices.Clip(us.names)
	us.aux = slices.Clip(us.aux)
	u.uses = us
}

// useScope is the units a search for the references of a name looks
// at, once the whole lib has been scanned.
type useScope struct {
	units []*Unit
}

// UseUnits returns the units whose Uses a search for the references of a
// name looks at: every file the whole-lib scan covers (the .c files of
// the lib, or of the checkout) and every header one of them includes
// that lies in the same tree and is not excluded, each header read as a
// file on its own, as an editor that opens it reads it. In path order.
//
// The second result says the list is the whole of it: the scan is
// complete for this generation. When it is not, the list is what the
// generation has built so far of those files, and a search is partial.
// Run Scan first. Once complete, the list is kept with the generation.
// The headers are read here, on first need; a cancelled ctx leaves the
// list partial.
func (s *Snapshot) UseUnits(ctx context.Context) ([]*Unit, bool) {
	if us, ok := s.useScope.get(); ok {
		return us.units, true
	}
	if !s.Scanned() {
		return s.usesSoFar(), false
	}
	// The headers first, outside the lazy value: building one may wait on
	// another goroutine that builds the same.
	par.Each(ctx, s.includedHeaders(), func(p string) { s.UnitAt(p) })
	if !s.Scanned() || ctx.Err() != nil {
		return s.usesSoFar(), false
	}
	us := s.useScope.load(func() *useScope { return &useScope{units: s.usesSoFar()} })
	return us.units, true
}

// usesSoFar returns the units built so far that a search looks at.
func (s *Snapshot) usesSoFar() []*Unit {
	var out []*Unit
	for p, u := range s.units.complete() {
		if u != nil && u.uses != nil && (s.inScope(p) || s.inHeaderScope(p)) {
			out = append(out, u)
		}
	}
	slices.SortFunc(out, func(a, b *Unit) int { return cmp.Compare(a.Path, b.Path) })
	return out
}

// includedHeaders returns the headers the scanned units were built from
// that a search looks at.
func (s *Snapshot) includedHeaders() []string {
	deps := map[string]struct{}{}
	for p, u := range s.units.complete() {
		if u == nil || filepath.Ext(p) != ".c" {
			continue
		}
		for _, d := range u.Deps {
			if filepath.Ext(d) == ".h" {
				deps[d] = struct{}{}
			}
		}
	}
	var out []string
	for d := range deps {
		if s.inHeaderScope(d) {
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return out
}

// inHeaderScope reports whether p is a header a search for references
// looks at, when one of the scanned files includes it: a .h file under
// the scan's root, not excluded, not a stub.
func (l *lib) inHeaderScope(p string) bool {
	return filepath.Ext(p) == ".h" && l.covers(p)
}
