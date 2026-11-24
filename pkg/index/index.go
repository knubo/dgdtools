// Package index is the cross-file view of an LPC lib that dgdlint's
// whole-lib rules need: every object's functions and variables, its
// resolved inherit chain, and the macros visible in each file.
//
// In DGD every cross-object call is late-bound by name and a missing
// function silently returns nil; the compiler only checks names within a
// file's own inherit chain. The index is what lets a linter give LPC the
// missing-function check other languages get from their compiler.
//
// Three properties shape the design:
//
//   - Lazy. Nothing is read until asked for. Linting one file loads its
//     include closure, its inherit chain, and the targets of its calls.
//     Only the reverse "who inherits me" map needs the whole lib, and it
//     is built on first use.
//   - Three-valued. Lookups answer Found, Missing or Unknown. An inherit
//     that cannot be resolved, a parent that could not be fully parsed, a
//     missing header: each makes the affected chain Partial, and a miss in
//     a partial chain is Unknown, never Missing.
//   - Invalidatable. Each unit records the files it was built from, so a
//     changed file drops exactly the units that depend on it.
//
// All file access goes through vfs.FS. An Index is safe for concurrent
// use and holds no global state.
package index

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/fileset"
	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/syntax"
	"github.com/mmcdole/dgdtools/pkg/token"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// FuncInfo describes one function of an object.
type FuncInfo struct {
	Static, Private bool
	// PrototypeOnly marks a declared-but-never-defined function: DGD
	// compiles these fine and raises "Undefined function" at call time.
	PrototypeOnly bool
	// MinArgs/MaxArgs bound the accepted argument count. MaxArgs -1 means
	// unbounded (trailing ellipsis).
	MinArgs, MaxArgs int
	Path             string // fs path of the defining file
	Off              uint32
}

type VarInfo struct {
	Static bool
	Off    uint32
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
// tree, so a whole lib of units stays small.
type Unit struct {
	FSPath  string
	LibPath string // "" when the file is outside the lib root

	Funcs    map[string]FuncInfo
	Vars     map[string]VarInfo
	Inherits []InheritRef

	// Partial is true when the symbol table may be missing entries: a
	// top-level region did not parse, an include is unresolved, or a
	// macro stands at declaration level.
	Partial bool
	// MacrosComplete is true when every reachable #include resolved.
	MacrosComplete bool
	// Declares is true when the file certainly declares a function or
	// variable (outside any conditional branch chosen by guess).
	Declares    bool
	BadIncludes []BadInclude
	// UnknownIncludes counts includes that could not be resolved but are
	// not provably missing (the lib is incomplete).
	UnknownIncludes int
	AutoSave        bool
	// Deps are the fs paths this unit was built from (itself included).
	Deps []string
}

// Parsed is a Unit together with its token stream, tree and macro view,
// for the file currently being linted.
type Parsed struct {
	Unit *Unit
	Tok  *token.File
	Tree *syntax.File
	View *pp.View
}

// Stats counts what the index could not see. They are how a drop in
// coverage becomes observable.
type Stats struct {
	Units             atomic.Int64
	UnresolvedInherit atomic.Int64
	UnknownIncludes   atomic.Int64
	PartialUnits      atomic.Int64
}

// Index is the lazy whole-lib view.
type Index struct {
	FS   vfs.FS
	Root string // absolute lib root; "" in file-local mode
	Cfg  *config.Config

	// Complete is false when the lib on disk lacks things the config says
	// it has (include dirs, the force-include, an auto object): a partial
	// checkout or a wrong root. Missing references are then Unknown.
	Complete bool
	// Notes describe project-level degradations, for the user.
	Notes []string

	Stats Stats

	dialect  token.Dialect
	exclude  *fileset.Matcher
	virtual  *fileset.Matcher
	defined  map[string]bool
	override map[string]pp.Class
	markers  map[string]bool

	mu       sync.Mutex
	tables   map[string]*tableEntry
	units    map[string]*unitEntry
	chains   map[string]*Chain
	exists   map[string]bool
	incUsers map[string]map[string]bool // lib path of an included code file -> includer lib paths
	users    map[string][]string
	usersOK  bool
	allOnce  *sync.Once

	baseOnce   *sync.Once
	baseLayers []*pp.Table
	baseDeps   []string
	baseOK     bool

	autoOnce  *sync.Once
	autoFuncs map[string]ChainFunc
}

type tableEntry struct {
	once  sync.Once
	ok    bool // file exists and was read
	table *pp.Table
}

type unitEntry struct {
	once sync.Once
	unit *Unit
}

// New creates an index over the project's lib. root may be "" (no lib:
// only file-local facts are available).
func New(cfg *config.Config, root string, fsys vfs.FS) *Index {
	ix := &Index{
		FS: fsys, Root: root, Cfg: cfg,
		dialect:  cfg.TokenDialect(),
		exclude:  fileset.NewMatcher(cfg.Exclude),
		virtual:  fileset.NewMatcher(cfg.Lint.VirtualPaths),
		defined:  map[string]bool{},
		override: map[string]pp.Class{},
		markers:  map[string]bool{},
	}
	for _, d := range pp.Predefined {
		ix.defined[d] = true
	}
	for _, d := range cfg.Lint.Defines {
		ix.defined[d] = true
	}
	for s := range cfg.SpecifierMacroSet() {
		ix.override[s] = pp.ClassEmpty
	}
	for name, cls := range cfg.Lint.Macros {
		if c, ok := pp.ParseClass(cls); ok {
			ix.override[name] = c
		}
	}
	markers := cfg.Lint.AutosaveMarkers
	if len(markers) == 0 {
		markers = []string{"set_auto_save"}
	}
	for _, m := range markers {
		ix.markers[m] = true
	}
	ix.reset()

	ix.Complete = root != ""
	if root != "" {
		var missing []string
		for _, d := range cfg.Lint.IncludeDirs {
			if !fsys.IsDir(ix.fsPath(d)) {
				missing = append(missing, d)
			}
		}
		if f := cfg.Lint.IncludeFile; f != "" && !fsys.Exists(ix.fsPath(f)) {
			missing = append(missing, f)
		}
		for _, a := range cfg.Lint.AutoObjects {
			if !fsys.Exists(fileset.FSPath(root, a)) {
				missing = append(missing, a)
			}
		}
		if len(missing) > 0 {
			ix.Complete = false
			ix.Notes = append(ix.Notes, "lib looks incomplete under "+root+" (missing "+
				strings.Join(missing, ", ")+"): missing-reference findings are disabled")
		}
	}
	return ix
}

func (ix *Index) reset() {
	ix.tables = map[string]*tableEntry{}
	ix.units = map[string]*unitEntry{}
	ix.chains = map[string]*Chain{}
	ix.exists = map[string]bool{}
	ix.incUsers = map[string]map[string]bool{}
	ix.users, ix.usersOK = nil, false
	ix.allOnce = &sync.Once{}
	ix.baseOnce = &sync.Once{}
	ix.autoOnce = &sync.Once{}
}

// Invalidate tells the index that the named files changed (on disk or in
// an overlay). Units built from them are dropped, along with every
// memoized chain and the reverse-inheritance map; everything else is
// kept. It returns the fs paths of the units that were dropped, which are
// the files whose diagnostics may have changed.
func (ix *Index) Invalidate(paths ...string) []string {
	changed := map[string]bool{}
	for _, p := range paths {
		if abs, err := filepath.Abs(p); err == nil {
			changed[abs] = true
		}
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	baseHit := false
	for _, d := range ix.baseDeps {
		if changed[d] {
			baseHit = true
		}
	}
	var dropped []string
	if baseHit {
		for p := range ix.units {
			if !strings.HasPrefix(p, incKey) {
				dropped = append(dropped, p)
			}
		}
		ix.reset()
		return dropped
	}
	for p := range changed {
		delete(ix.tables, p)
		delete(ix.exists, p)
	}
	for p, e := range ix.units {
		u := e.unit
		hit := changed[p]
		if u != nil {
			for _, d := range u.Deps {
				if changed[d] {
					hit = true
					break
				}
			}
		}
		if hit {
			delete(ix.units, p)
			if !strings.HasPrefix(p, incKey) {
				dropped = append(dropped, p)
			}
		}
	}
	ix.chains = map[string]*Chain{}
	ix.users, ix.usersOK = nil, false
	ix.allOnce = &sync.Once{}
	ix.autoOnce = &sync.Once{}
	return dropped
}

// ---------------------------------------------------------------- paths

func (ix *Index) fsPath(libPath string) string {
	return filepath.Join(ix.Root, filepath.FromSlash(strings.TrimPrefix(libPath, "/")))
}

// LibPath maps an fs path to its lib path ("/std/room" for std/room.c),
// or "" when the file is not under the root.
func (ix *Index) LibPath(fsPath string) string {
	if ix.Root == "" {
		return ""
	}
	rel, err := filepath.Rel(ix.Root, fsPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return fileset.LibPath(filepath.ToSlash(rel))
}

// Excluded reports whether fsPath matches the config's exclude globs.
func (ix *Index) Excluded(fsPath string) bool {
	if ix.Root == "" {
		return false
	}
	rel, err := filepath.Rel(ix.Root, fsPath)
	if err != nil {
		return false
	}
	return ix.exclude.Match(filepath.ToSlash(rel))
}

// ObjectExists reports whether libPath is backed by a file.
func (ix *Index) ObjectExists(libPath string) bool {
	p := fileset.FSPath(ix.Root, libPath)
	ix.mu.Lock()
	v, ok := ix.exists[p]
	ix.mu.Unlock()
	if ok {
		return v
	}
	v = ix.FS.Exists(p)
	ix.mu.Lock()
	ix.exists[p] = v
	ix.mu.Unlock()
	return v
}

// IsVirtual reports whether libPath is served by a virtual-object daemon.
func (ix *Index) IsVirtual(libPath string) bool {
	return ix.virtual.Match(strings.TrimPrefix(libPath, "/"))
}

// ObjectPath turns an object path as written in fromLib into an absolute
// lib path. A path without a leading slash is resolved from the lib root,
// as the driver does, unless the config says paths are file-relative.
func (ix *Index) ObjectPath(p, fromLib string) string {
	if ix.Cfg.Lint.RelativeObjectPaths == "file" {
		return NormalizeLibPath(p, fromLib)
	}
	return NormalizeLibPath(p, "/")
}

// NormalizeLibPath strips .c and makes p absolute relative to fromLib's
// directory.
func NormalizeLibPath(p, fromLib string) string {
	p = strings.TrimSuffix(strings.TrimSpace(p), ".c")
	if !strings.HasPrefix(p, "/") {
		p = path.Join(path.Dir(fromLib), p)
	}
	return path.Clean(p)
}

// Walk visits every non-excluded .c file under dir (an fs path).
func (ix *Index) Walk(dir string, visit func(fsPath string)) error {
	return ix.FS.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if ix.Root != "" && p != dir {
				if rel, rerr := filepath.Rel(ix.Root, p); rerr == nil {
					rel = filepath.ToSlash(rel)
					if ix.exclude.Match(rel) || ix.exclude.MatchDir(rel) {
						return filepath.SkipDir
					}
				}
			}
			return nil
		}
		if filepath.Ext(p) != ".c" || ix.Excluded(p) {
			return nil
		}
		visit(p)
		return nil
	})
}

// ------------------------------------------------------- macro tables

func (ix *Index) table(fsPath string) (*pp.Table, bool) {
	ix.mu.Lock()
	e := ix.tables[fsPath]
	if e == nil {
		e = &tableEntry{}
		ix.tables[fsPath] = e
	}
	ix.mu.Unlock()
	e.once.Do(func() {
		src, err := ix.FS.ReadFile(fsPath)
		if err != nil {
			return
		}
		f := lexer.Lex(fsPath, src, ix.dialect)
		e.table = pp.Scan(f, pp.Select(f, ix.defined))
		e.ok = true
	})
	return e.table, e.ok
}

// ResolveInclude maps an include to the fs path the driver would read:
// "name" searches the including file's directory then the include dirs,
// <name> the include dirs only. The second result is false when nothing
// was found.
func (ix *Index) ResolveInclude(fromFS string, inc pp.Include) (string, bool) {
	var cands []string
	if !inc.Angle {
		if strings.HasPrefix(inc.Name, "/") {
			if ix.Root != "" {
				cands = append(cands, ix.fsPath(inc.Name))
			}
		} else {
			cands = append(cands, filepath.Join(filepath.Dir(fromFS), filepath.FromSlash(inc.Name)))
		}
	}
	if ix.Root != "" {
		for _, dir := range ix.Cfg.Lint.IncludeDirs {
			cands = append(cands, ix.fsPath(path.Join(dir, inc.Name)))
		}
	}
	for _, c := range cands {
		if ix.FS.Exists(c) && !ix.FS.IsDir(c) {
			return filepath.Clean(c), true
		}
	}
	return "", false
}

const maxIncludeDepth = 12

// incKey prefixes the unit-map keys of included-code units, which are
// parsed standalone and must not be confused with objects.
const incKey = "\x00inc:"

// closure walks the include graph below t in preprocessing order.
type closure struct {
	order    []*pp.Table // tables in the order their definitions take effect
	deps     []string
	complete bool
	seen     map[string]bool
}

func (ix *Index) walkIncludes(c *closure, fsPath string, t *pp.Table, depth int) {
	for _, inc := range t.Includes {
		p, ok := ix.ResolveInclude(fsPath, inc)
		if !ok {
			c.complete = false
			continue
		}
		if c.seen[p] {
			continue
		}
		c.seen[p] = true
		c.deps = append(c.deps, p)
		sub, ok := ix.table(p)
		if !ok {
			c.complete = false
			continue
		}
		if depth < maxIncludeDepth {
			ix.walkIncludes(c, p, sub, depth+1)
		} else {
			c.complete = false
		}
		c.order = append(c.order, sub)
	}
}

// base returns the tables of the driver's force-included header.
func (ix *Index) base() ([]*pp.Table, bool) {
	ix.mu.Lock()
	once := ix.baseOnce
	ix.mu.Unlock()
	once.Do(func() {
		ix.baseOK = true
		inc := ix.Cfg.Lint.IncludeFile
		if ix.Root == "" {
			ix.baseOK = false
			return
		}
		if inc == "" {
			return
		}
		p := ix.fsPath(inc)
		t, ok := ix.table(p)
		if !ok {
			ix.baseOK = false
			return
		}
		c := &closure{complete: true, seen: map[string]bool{p: true}, deps: []string{p}}
		ix.walkIncludes(c, p, t, 0)
		c.order = append(c.order, t)
		ix.baseOK = c.complete
		ix.baseDeps = c.deps
		for i := len(c.order) - 1; i >= 0; i-- {
			ix.baseLayers = append(ix.baseLayers, c.order[i])
		}
	})
	return ix.baseLayers, ix.baseOK
}

// view builds the macro view of a file from its own table.
func (ix *Index) view(fsPath string, own *pp.Table) (*pp.View, *closure) {
	c := &closure{complete: true, seen: map[string]bool{fsPath: true}, deps: []string{fsPath}}
	ix.walkIncludes(c, fsPath, own, 0)
	base, baseOK := ix.base()
	v := &pp.View{Override: ix.override, Complete: c.complete && baseOK}
	v.Layers = append(v.Layers, own)
	for i := len(c.order) - 1; i >= 0; i-- {
		v.Layers = append(v.Layers, c.order[i])
	}
	v.Layers = append(v.Layers, base...)
	c.deps = append(c.deps, ix.baseDeps...)
	return v, c
}

// ---------------------------------------------------------------- units

// Unit returns the indexed facts for a lib path, or nil when no file
// backs it.
func (ix *Index) Unit(libPath string) *Unit {
	if ix.Root == "" || libPath == "" {
		return nil
	}
	return ix.UnitFS(fileset.FSPath(ix.Root, libPath))
}

// UnitFS returns the indexed facts for an fs path, or nil if unreadable.
func (ix *Index) UnitFS(fsPath string) *Unit {
	fsPath = filepath.Clean(fsPath)
	ix.mu.Lock()
	e := ix.units[fsPath]
	if e == nil {
		e = &unitEntry{}
		ix.units[fsPath] = e
	}
	ix.mu.Unlock()
	e.once.Do(func() {
		if p := ix.build(fsPath); p != nil {
			e.unit = p.Unit
		}
	})
	return e.unit
}

// Parse lexes and parses fsPath for linting and returns its tree together
// with its facts. The tree is not retained by the index.
func (ix *Index) Parse(fsPath string) *Parsed {
	fsPath = filepath.Clean(fsPath)
	var parsed *Parsed
	ix.mu.Lock()
	e := ix.units[fsPath]
	if e == nil {
		e = &unitEntry{}
		ix.units[fsPath] = e
	}
	ix.mu.Unlock()
	e.once.Do(func() {
		if parsed = ix.build(fsPath); parsed != nil {
			e.unit = parsed.Unit
		}
	})
	if parsed != nil || e.unit == nil {
		return parsed
	}
	// Facts already existed (the file was loaded as someone's parent):
	// parse again for the tree.
	if p := ix.build(fsPath); p != nil {
		p.Unit = e.unit
		return p
	}
	return nil
}

func (ix *Index) build(fsPath string) (parsed *Parsed) {
	src, err := ix.FS.ReadFile(fsPath)
	if err != nil {
		return nil
	}
	// A bug in the analysis of one file must not take down a run over a
	// hundred thousand: the file is reported as unreadable instead.
	defer func() {
		if r := recover(); r != nil {
			ix.mu.Lock()
			ix.Notes = append(ix.Notes, fmt.Sprintf("internal error analyzing %s (please report): %v", fsPath, r))
			ix.mu.Unlock()
			parsed = nil
		}
	}()
	f := lexer.Lex(fsPath, src, ix.dialect)
	sel := pp.Select(f, ix.defined)
	own := pp.Scan(f, sel)
	view, cl := ix.view(fsPath, own)
	tree := syntax.Parse(f, syntax.Options{Macros: view, Sel: sel})

	u := &Unit{
		FSPath: fsPath, LibPath: ix.LibPath(fsPath),
		Funcs: map[string]FuncInfo{}, Vars: map[string]VarInfo{},
		Partial: tree.Partial, MacrosComplete: view.Complete, Deps: cl.deps,
	}
	for _, e := range tree.Errors {
		if e.Severe {
			u.Partial = true
		}
	}

	// Includes: provably missing only when the lib is complete.
	for _, inc := range own.Includes {
		if _, ok := ix.ResolveInclude(fsPath, inc); ok {
			continue
		}
		u.Partial = true
		if ix.Complete {
			t := f.Tokens[inc.Tok]
			u.BadIncludes = append(u.BadIncludes, BadInclude{Raw: inc.Raw(), Off: t.Off, End: t.End})
		} else {
			u.UnknownIncludes++
		}
	}
	if !view.Complete && len(u.BadIncludes) == 0 && u.UnknownIncludes == 0 {
		u.UnknownIncludes = 1 // an include of an include failed
	}

	collectDecls(u, tree, fsPath)
	for _, d := range tree.Decls {
		if inh, ok := d.(*syntax.InheritDecl); ok {
			u.Inherits = append(u.Inherits, ix.resolveInherit(tree, inh, u.LibPath, view))
		}
	}

	// Code in included files belongs to the including program.
	for _, dep := range cl.deps[1:] {
		code := ix.includedCode(dep)
		if code == nil {
			continue
		}
		if len(code.Funcs) > 0 && u.LibPath != "" {
			if lp := ix.LibPath(dep); lp != "" {
				ix.mu.Lock()
				m := ix.incUsers[lp]
				if m == nil {
					m = map[string]bool{}
					ix.incUsers[lp] = m
				}
				m[u.LibPath] = true
				ix.mu.Unlock()
			}
		}
		for name, fi := range code.Funcs {
			if own, exists := u.Funcs[name]; !exists || (own.PrototypeOnly && !fi.PrototypeOnly) {
				u.Funcs[name] = fi
			}
		}
		for name, vi := range code.Vars {
			if _, exists := u.Vars[name]; !exists {
				u.Vars[name] = vi
			}
		}
		if code.Partial {
			u.Partial = true
		}
	}

	for _, i := range sel.Sig {
		if t := f.Tokens[i]; t.Kind == token.Ident && ix.markers[string(f.Text(t))] {
			u.AutoSave = true
			break
		}
	}

	ix.Stats.Units.Add(1)
	if u.Partial {
		ix.Stats.PartialUnits.Add(1)
	}
	ix.Stats.UnknownIncludes.Add(int64(u.UnknownIncludes))
	for _, r := range u.Inherits {
		if !r.Resolved {
			ix.Stats.UnresolvedInherit.Add(1)
		}
	}
	return &Parsed{Unit: u, Tok: f, Tree: tree, View: view}
}

// collectDecls records the functions and variables declared in tree.
func collectDecls(u *Unit, tree *syntax.File, fsPath string) {
	tree.Funcs(func(fn *syntax.FuncDecl) bool {
		if fn.Name == nil {
			return true // operator functions are not name-callable
		}
		minA, maxA := ParamRange(fn)
		fi := FuncInfo{
			Static: fn.Has(token.KwStatic), Private: fn.Has(token.KwPrivate),
			PrototypeOnly: fn.Body == nil,
			MinArgs:       minA, MaxArgs: maxA,
			Path: fsPath, Off: tree.Off(fn.Name),
		}
		if old, ok := u.Funcs[fn.Name.Name]; !ok || (old.PrototypeOnly && !fi.PrototypeOnly) {
			u.Funcs[fn.Name.Name] = fi
		}
		return true
	})
	for _, d := range tree.Decls {
		if vd, ok := d.(*syntax.VarDecl); ok {
			for _, v := range vd.Vars {
				u.Vars[v.Name.Name] = VarInfo{Static: vd.Has(token.KwStatic), Off: tree.Off(v.Name)}
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

// IncludeDeclares reports whether the file an #include resolves to, or
// anything it includes in turn, declares a function or a variable. It
// returns the fs path of the first such file.
func (ix *Index) IncludeDeclares(fromFS string, inc pp.Include) (string, bool) {
	p, ok := ix.ResolveInclude(fromFS, inc)
	if !ok {
		return "", false
	}
	t, ok := ix.table(p)
	if !ok {
		return "", false
	}
	c := &closure{complete: true, seen: map[string]bool{p: true}, deps: []string{p}}
	ix.walkIncludes(c, p, t, 0)
	for _, dep := range c.deps {
		if u := ix.includedCode(dep); u != nil && u.Declares {
			return dep, true
		}
	}
	return "", false
}

// includedCode returns the declarations a #included file contributes, or
// nil when it is a pure header.
func (ix *Index) includedCode(fsPath string) *Unit {
	t, ok := ix.table(fsPath)
	if !ok {
		return nil
	}
	_ = t
	ix.mu.Lock()
	key := incKey + fsPath
	e := ix.units[key]
	if e == nil {
		e = &unitEntry{}
		ix.units[key] = e
	}
	ix.mu.Unlock()
	e.once.Do(func() {
		src, err := ix.FS.ReadFile(fsPath)
		if err != nil {
			return
		}
		f := lexer.Lex(fsPath, src, ix.dialect)
		sel := pp.Select(f, ix.defined)
		if len(sel.Sig) == 0 {
			e.unit = &Unit{FSPath: fsPath, Deps: []string{fsPath}}
			return
		}
		view := &pp.View{Layers: []*pp.Table{pp.Scan(f, sel)}, Override: ix.override}
		tree := syntax.Parse(f, syntax.Options{Macros: view, Sel: sel})
		u := &Unit{
			FSPath: fsPath, Funcs: map[string]FuncInfo{}, Vars: map[string]VarInfo{},
			Partial: tree.Partial, Deps: []string{fsPath},
		}
		collectDecls(u, tree, fsPath)
		for _, d := range tree.Decls {
			switch d.(type) {
			case *syntax.FuncDecl, *syntax.VarDecl:
				if first, _ := d.Span(); !sel.Guessed(first) {
					u.Declares = true
				}
			}
		}
		e.unit = u
	})
	return e.unit
}

func (ix *Index) resolveInherit(tree *syntax.File, d *syntax.InheritDecl, libPath string, view *pp.View) InheritRef {
	ref := InheritRef{Raw: tree.Text(d.Path), Off: tree.Off(d.Path), End: tree.End(d.Path), Private: d.Private}
	if d.Label != nil {
		ref.Label = d.Label.Name
	}
	if libPath == "" {
		return ref
	}
	val, ok := EvalString(ref.Raw, view, ix.dialect)
	if !ok || val == "" {
		return ref
	}
	ref.Path = ix.ObjectPath(val, libPath)
	ref.Resolved = true
	return ref
}

// LoadAll indexes every non-excluded .c file under the root, in parallel.
// It is needed only for reverse lookups (UsersOf). It reports false when
// the scan did not complete (no root, or the context was cancelled).
func (ix *Index) LoadAll(ctx context.Context) bool {
	if ix.Root == "" {
		return false
	}
	ix.mu.Lock()
	once := ix.allOnce
	ix.mu.Unlock()
	done := true
	once.Do(func() {
		var paths []string
		_ = ix.Walk(ix.Root, func(p string) { paths = append(paths, p) })
		ch := make(chan string, 256)
		var wg sync.WaitGroup
		for range runtime.NumCPU() {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for p := range ch {
					if ctx.Err() == nil {
						ix.UnitFS(p)
					}
				}
			}()
		}
		for _, p := range paths {
			ch <- p
		}
		close(ch)
		wg.Wait()
		if ctx.Err() != nil {
			done = false
			ix.mu.Lock()
			if ix.allOnce == once {
				ix.allOnce = &sync.Once{} // retry next time
			}
			ix.mu.Unlock()
		}
	})
	return done && ctx.Err() == nil
}
