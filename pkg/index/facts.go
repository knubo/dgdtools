package index

import (
	"slices"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/syntax"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// Loc is where a declaration is: its file (a path on disk, or the
// location of a profile stub), the byte offset of the declared name, and
// that offset as a 1-based line and byte column.
type Loc struct {
	Path      string
	Off       uint32
	Line, Col int
}

// FuncInfo describes one function of an object.
type FuncInfo struct {
	Name            string
	Static, Private bool
	// Nomask says no object that inherits this one can have a function
	// of the name: what the name means there is this function.
	Nomask bool
	// PrototypeOnly marks a declared-but-never-defined function: DGD
	// compiles these fine and raises "Undefined function" at call time.
	PrototypeOnly bool
	// Refused says DGD refuses the program's declarations of the name:
	// one is private and another is not ("declaration does not match
	// prototype"), or one is private and nomask ("private contradicts
	// nomask"). Whether the function is private is then moot.
	Refused bool
	// MinArgs/MaxArgs bound the accepted argument count. MaxArgs -1 means
	// unbounded (trailing ellipsis).
	MinArgs, MaxArgs int
	Loc
	// Decl is the declaration as written, without its body:
	// "static varargs int query(string what, mixed extra...)".
	// Type is its return type as written ("int", "string *", a type
	// macro such as "status"), or "" for a function declared without one.
	// Both come from real files and from profile stubs alike; Params
	// splits Decl into its parameters.
	Decl, Type string
}

// VarInfo describes one global variable of an object.
type VarInfo struct {
	Name            string
	Static, Private bool
	// Type is the declared type as written, including '*'s.
	Type string
	Loc
}

// Params returns the parameters of a function declaration as written
// ("string what", "mixed extra..."), for signature help.
func Params(decl string) []string {
	var out []string
	for _, s := range ParamSpans(decl) {
		out = append(out, decl[s[0]:s[1]])
	}
	return out
}

// ParamSpans returns where the parameters of a function declaration
// stand in it: the bytes [start, end) of each, without the space around
// it. A kfun's prototype is read the same way.
func ParamSpans(decl string) [][2]int {
	open := strings.IndexByte(decl, '(')
	close := strings.LastIndexByte(decl, ')')
	if open < 0 || close <= open {
		return nil
	}
	if inner := strings.TrimSpace(decl[open+1 : close]); inner == "" || inner == "void" {
		return nil
	}
	var out [][2]int
	for start := open + 1; start <= close; {
		end := start + strings.IndexByte(decl[start:close]+",", ',')
		s, e := start, end
		for s < e && (decl[s] == ' ' || decl[s] == '\t') {
			s++
		}
		for e > s && (decl[e-1] == ' ' || decl[e-1] == '\t') {
			e--
		}
		out = append(out, [2]int{s, e})
		start = end + 1
	}
	return out
}

// InheritRef is one inherit statement.
type InheritRef struct {
	Raw      string // source text of the path expression
	Path     string // resolved lib path (no .c)
	Label    string
	Resolved bool
	Private  bool
	Off, End uint32
}

// BadInclude is an #include that provably resolves to nothing.
type BadInclude struct {
	Raw      string
	Off, End uint32
}

// Unit is the indexed view of one source file: facts only, no tokens or
// tree, so a whole lib of units stays small. A Unit is immutable and is
// shared by every generation it is valid in.
type Unit struct {
	// Path is where the file is: a path on disk or a stub's location.
	Path    string
	LibPath string // "" when the file is outside the lib

	// Funcs and Vars are the object's functions and global variables,
	// sorted by name, each name once (Func and Var find one). A whole
	// lib has hundreds of thousands, most objects a few: a sorted slice
	// holds them in a fraction of what a map of each would.
	Funcs    []FuncInfo
	Vars     []VarInfo
	Inherits []InheritRef

	// Partial is true when the symbol table may be missing entries: a
	// top-level region did not parse, an include is unresolved, or a
	// macro stands at declaration level.
	Partial bool
	// Stub is true when the file is a profile's stub: it lists the
	// object's public interface only.
	Stub bool
	// Declares is true when the file certainly declares a function or
	// variable (outside any conditional branch chosen by guess).
	Declares    bool
	BadIncludes []BadInclude
	// UnknownIncludes counts includes that could not be resolved but are
	// not provably missing (the lib is incomplete).
	UnknownIncludes int
	// Deps are the files this unit was built from: itself, then every
	// file it #includes directly or indirectly, then the force-included
	// header and what that includes.
	Deps []string

	// gen is the generation the unit was built in, and sum the textSum
	// of the source it was built from.
	gen uint64
	sum uint64
	// The files, other than Path and the force-included header's, whose
	// change makes the unit stale (watched): its include closure, the
	// first incs of Deps but Path, and absent, the places an #include
	// was looked for and not found.
	incs   int
	absent []string
	// code are the lib paths of the #included files that contribute
	// declarations to this program.
	code []string
	// uses are the file's Uses; nil when they were not read.
	uses *uses
}

// watched yields the files, other than Path and the force-included
// header's, whose change makes the unit stale: its include closure, and
// the places an #include was looked for and not found.
func (u *Unit) watched(yield func(string) bool) {
	for _, list := range [][]string{u.Deps[min(1, u.incs):u.incs], u.absent} {
		for _, p := range list {
			if !yield(p) {
				return
			}
		}
	}
}

// Func returns the unit's function called name.
func (u *Unit) Func(name string) (FuncInfo, bool) {
	i, ok := slices.BinarySearchFunc(u.Funcs, name, func(f FuncInfo, name string) int { return strings.Compare(f.Name, name) })
	if !ok {
		return FuncInfo{}, false
	}
	return u.Funcs[i], true
}

// Var returns the unit's global variable called name.
func (u *Unit) Var(name string) (VarInfo, bool) {
	i, ok := slices.BinarySearchFunc(u.Vars, name, func(v VarInfo, name string) int { return strings.Compare(v.Name, name) })
	if !ok {
		return VarInfo{}, false
	}
	return u.Vars[i], true
}

// collectDecls appends the functions and variables declared in tree to
// u's, in the order of the file: sortDecls makes them the unit's.
func collectDecls(u *Unit, tree *syntax.File) {
	loc := func(n syntax.Node) Loc {
		off := tree.Off(n)
		pos := tree.Tok.Pos(off)
		return Loc{Path: u.Path, Off: off, Line: pos.Line, Col: pos.Col}
	}
	tree.Funcs(func(fn *syntax.FuncDecl) bool {
		if fn.Name == nil {
			return true // operator functions are not name-callable
		}
		minA, maxA := ParamRange(fn)
		u.Funcs = append(u.Funcs, FuncInfo{
			Name:   fn.Name.Name,
			Static: fn.Has(token.KwStatic), Private: fn.Has(token.KwPrivate), Nomask: fn.Has(token.KwNomask),
			PrototypeOnly: fn.Body == nil,
			MinArgs:       minA, MaxArgs: maxA,
			Loc:  loc(fn.Name),
			Decl: tree.FuncDeclText(fn), Type: tree.TypeText(fn.Type, fn.Stars),
		})
		return true
	})
	for _, d := range tree.Decls {
		if vd, ok := d.(*syntax.VarDecl); ok {
			for _, v := range vd.Vars {
				u.Vars = append(u.Vars, VarInfo{
					Name:   v.Name.Name,
					Static: vd.Has(token.KwStatic), Private: vd.Has(token.KwPrivate),
					Type: tree.TypeText(vd.Type, v.Stars), Loc: loc(v.Name),
				})
			}
		}
	}
}

// sortDecls sorts the functions and variables collectDecls appended,
// keeping one of each name: of the functions, the first definition, or
// the first prototype where there is none; of the variables, the last
// declaration (a later one replaces an earlier one).
func sortDecls(u *Unit) {
	u.Funcs = sortFuncs(u.Funcs)
	u.Vars = sortVars(u.Vars, true)
}

// sortFuncs returns fs sorted by name, keeping of each name the first
// definition, else the first prototype; in an array of its own length,
// since a whole lib of units keeps them.
func sortFuncs(fs []FuncInfo) []FuncInfo {
	slices.SortStableFunc(fs, func(a, b FuncInfo) int { return strings.Compare(a.Name, b.Name) })
	out := fs[:0]
	for i := 0; i < len(fs); {
		j, pick := i, i
		refused := false
		for ; j < len(fs) && fs[j].Name == fs[i].Name; j++ {
			if fs[pick].PrototypeOnly && !fs[j].PrototypeOnly {
				pick = j
			}
			refused = refused || fs[j].Private != fs[i].Private || fs[j].Private && fs[j].Nomask
		}
		f := fs[pick]
		f.Refused = f.Refused || refused
		out = append(out, f)
		i = j
	}
	return slices.Clone(out)
}

// sortVars returns vs sorted by name, keeping of each name the last, or
// the first; in an array of its own length.
func sortVars(vs []VarInfo, last bool) []VarInfo {
	slices.SortStableFunc(vs, func(a, b VarInfo) int { return strings.Compare(a.Name, b.Name) })
	out := vs[:0]
	for i, v := range vs {
		if last && i+1 < len(vs) && vs[i+1].Name == v.Name || !last && i > 0 && vs[i-1].Name == v.Name {
			continue
		}
		out = append(out, v)
	}
	return slices.Clone(out)
}

// ParamRange derives the accepted argument-count range of a function: a
// per-parameter `varargs` makes that and later parameters optional, a
// class-level `varargs` makes all optional, and a trailing `...` collects
// unbounded extras.
func ParamRange(fn *syntax.FuncDecl) (minArgs, maxArgs int) {
	n := len(fn.Params)
	minArgs, maxArgs = n, n
	for i, p := range fn.Params {
		if p.Varargs {
			minArgs = i
			break
		}
	}
	if fn.Has(token.KwVarargs) {
		minArgs = 0
	}
	if fn.Ellipsis {
		maxArgs = -1
		if minArgs == n {
			minArgs = n - 1
		}
	}
	if minArgs < 0 {
		minArgs = 0
	}
	return minArgs, maxArgs
}
