package index

import (
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
	Static, Private bool
	// PrototypeOnly marks a declared-but-never-defined function: DGD
	// compiles these fine and raises "Undefined function" at call time.
	PrototypeOnly bool
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
	Static, Private bool
	// Type is the declared type as written, including '*'s.
	Type string
	Loc
}

// Params returns the parameters of a function declaration as written
// ("string what", "mixed extra..."), for signature help.
func Params(decl string) []string {
	open := strings.IndexByte(decl, '(')
	close := strings.LastIndexByte(decl, ')')
	if open < 0 || close <= open {
		return nil
	}
	inner := strings.TrimSpace(decl[open+1 : close])
	if inner == "" || inner == "void" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(inner, ",") {
		out = append(out, strings.TrimSpace(p))
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

	Funcs    map[string]FuncInfo
	Vars     map[string]VarInfo
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
	// watch are the files, other than Path and the force-included header's,
	// whose change makes the unit stale: its include closure, and the
	// places an #include was looked for and not found.
	watch []string
	// code are the lib paths of the #included files that contribute
	// declarations to this program.
	code []string
}

// collectDecls records the functions and variables declared in tree.
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
		fi := FuncInfo{
			Static: fn.Has(token.KwStatic), Private: fn.Has(token.KwPrivate),
			PrototypeOnly: fn.Body == nil,
			MinArgs:       minA, MaxArgs: maxA,
			Loc:  loc(fn.Name),
			Decl: tree.FuncDeclText(fn), Type: tree.TypeText(fn.Type, fn.Stars),
		}
		if old, ok := u.Funcs[fn.Name.Name]; !ok || (old.PrototypeOnly && !fi.PrototypeOnly) {
			u.Funcs[fn.Name.Name] = fi
		}
		return true
	})
	for _, d := range tree.Decls {
		if vd, ok := d.(*syntax.VarDecl); ok {
			for _, v := range vd.Vars {
				u.Vars[v.Name.Name] = VarInfo{
					Static: vd.Has(token.KwStatic), Private: vd.Has(token.KwPrivate),
					Type: tree.TypeText(vd.Type, v.Stars), Loc: loc(v.Name),
				}
			}
		}
	}
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
