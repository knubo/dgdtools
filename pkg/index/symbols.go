package index

import (
	"cmp"
	"path"
	"slices"

	"github.com/mmcdole/dgdtools/internal/fuzzy"
	"github.com/mmcdole/dgdtools/pkg/pp"
)

// SymbolKind says what a Symbol is the name of.
type SymbolKind uint8

const (
	SymbolFunction SymbolKind = iota + 1
	// SymbolVariable is a global variable of an object.
	SymbolVariable
	// SymbolMacro is a macro that a header defines.
	SymbolMacro
)

// Symbol is a name the lib declares, as a search by name finds it.
type Symbol struct {
	Kind SymbolKind
	Name string
	// Loc is where the name is declared. A macro has its file and offset
	// and no line.
	Loc
	// In is what declares it: the object, by its lib path ("/std/room"),
	// or the header, by its name in the lib ("/include/std.h"); "" for a
	// file outside the lib.
	In string
	// Stub says the declaration is known from a profile's stub and not
	// from a file of the checkout.
	Stub bool
	// Score is how well the name matched what was searched for (the
	// scores of internal/fuzzy: more is better).
	Score int
}

// symbolTable is where the names of a generation are: the units it has,
// and the tables of the headers it has read.
type symbolTable struct {
	units  []*Unit
	tables []*pp.Table
}

// symbolTable returns the generation's table. It is kept once the scan
// of the whole lib is complete, which is when it has everything; before
// that it is what has been indexed so far, and is made again when asked
// again. Of a partial checkout the profile's stubs are part of it: the
// scan covers the checkout only.
func (s *Snapshot) symbolTable() *symbolTable {
	if t, ok := s.symbols.get(); ok {
		return t
	}
	if s.Scanned() {
		return s.symbols.load(s.buildSymbolTable)
	}
	return s.buildSymbolTable()
}

func (s *Snapshot) buildSymbolTable() *symbolTable {
	// The stubs first, so that they are read: what is known already is
	// then all there is.
	for _, file := range s.lay.StubFiles {
		p, _ := s.locate(file)
		if !s.isStub(p) {
			continue // the checkout has the file, or speaks for where it would be
		}
		if path.Ext(file) == ".c" {
			s.UnitAt(p)
		} else {
			s.header(p)
		}
	}
	t := &symbolTable{}
	for _, u := range s.units.complete() {
		if u != nil {
			t.units = append(t.units, u)
		}
	}
	for _, h := range s.headers.complete() {
		if h.table != nil {
			t.tables = append(t.tables, h.table)
		}
	}
	slices.SortFunc(t.units, func(a, b *Unit) int { return cmp.Compare(a.Path, b.Path) })
	slices.SortFunc(t.tables, func(a, b *pp.Table) int { return cmp.Compare(a.Path, b.Path) })
	return t
}

// FindSymbols returns the functions and global variables of the lib's
// objects, and the macros of its headers, whose names match query: the
// letters of the query in their order, in either case (internal/fuzzy).
// The best matches come first, a shorter name before a longer one, and
// there are at most limit of them; more says there were more. An empty
// query matches nothing.
//
// What is searched is what this generation of the index has read: the
// whole lib once Scan has completed, and until then the files that were
// asked about. Of a partial checkout the profile's stubs are searched
// too, and what one declares is marked (Symbol.Stub).
func (s *Snapshot) FindSymbols(query string, limit int) (out []Symbol, more bool) {
	m := fuzzy.New(query)
	if m.Empty() || limit <= 0 || !s.HasLib() {
		return nil, false
	}
	t := s.symbolTable()
	order := func(a, b Symbol) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(len(a.Name), len(b.Name)), cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Path, b.Path), cmp.Compare(a.Off, b.Off))
	}
	// The best limit are kept, not everything that matched: a letter or
	// two match most of a lib.
	add := func(sym Symbol) {
		if out = append(out, sym); len(out) >= 8*limit {
			slices.SortFunc(out, order)
			out, more = out[:limit], true
		}
	}
	// A declaration in a file that several units include is found in
	// each of them.
	type place struct {
		path string
		off  uint32
	}
	seen := map[place]bool{}
	symbol := func(kind SymbolKind, name string, loc Loc, u *Unit, score int) {
		sym := Symbol{Kind: kind, Name: name, Loc: loc, In: u.LibPath, Stub: s.isStub(loc.Path), Score: score}
		if loc.Path != u.Path {
			if seen[place{loc.Path, loc.Off}] {
				return
			}
			seen[place{loc.Path, loc.Off}] = true
			sym.In = s.LibFile(loc.Path)
		}
		add(sym)
	}
	for _, u := range t.units {
		for _, fn := range u.Funcs {
			if score := m.Score(fn.Name); score > 0 {
				symbol(SymbolFunction, fn.Name, fn.Loc, u, score)
			}
		}
		for _, v := range u.Vars {
			if score := m.Score(v.Name); score > 0 {
				symbol(SymbolVariable, v.Name, v.Loc, u, score)
			}
		}
	}
	for _, tab := range t.tables {
		for name, mac := range tab.Macros {
			if score := m.Score(name); score > 0 && !mac.Undefined {
				add(Symbol{Kind: SymbolMacro, Name: name, Loc: Loc{Path: mac.Path, Off: mac.NameOff},
					In: s.LibFile(mac.Path), Stub: s.isStub(mac.Path), Score: score})
			}
		}
	}
	slices.SortFunc(out, order)
	if len(out) > limit {
		out, more = out[:limit], true
	}
	return out, more
}
