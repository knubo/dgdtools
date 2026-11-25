// Package index is the cross-file view of an LPC lib that the linter's
// whole-lib rules need: every object's functions and variables, its
// resolved inherit chain, and the macros visible in each file.
//
// In DGD every cross-object call is late-bound by name and a missing
// function silently returns nil; the compiler only checks names within a
// file's own inherit chain. The index is what lets a linter give LPC the
// missing-function check other languages get from their compiler.
//
// Four properties shape the design:
//
//   - Lazy. Nothing is read until asked for. Linting one file loads its
//     include closure, its inherit chain, and the targets of its calls.
//     Only "who inherits me" needs the whole lib, and that scan runs on
//     first use.
//   - Three-valued. Lookups answer Found, Missing or Unknown. An inherit
//     that cannot be resolved, a parent that could not be fully parsed, a
//     missing header, a cancelled scan: each makes the affected answer
//     Unknown, never Missing.
//   - Generational. All questions are asked of a Snapshot, one generation
//     of the index. A snapshot only ever fills in what it has not read
//     yet; nothing it has answered changes. Invalidate does not touch the
//     snapshots in use: it makes the next generation, which shares every
//     unit the change did not reach. A lint run holds one snapshot from
//     start to end, so runs and edits never wait for each other.
//   - Incremental. The index knows who was built from which file, who
//     inherits whom and who asked about whom, so a change drops exactly
//     what depends on it, names the files whose diagnostics it can
//     affect, and leaves a completed whole-lib scan complete but for the
//     files that changed.
//
// All file access goes through vfs.FS; the package reads no environment,
// working directory or other process state. Paths given to it are
// absolute.
package index

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/fileset"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/token"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// Layout says where the lib is.
//
//   - A whole lib on disk: Root.
//   - A partial checkout: no Root; CheckoutDir on disk is the lib
//     directory CheckoutLib, and the rest of the lib is known only as far
//     as a profile's stubs describe it.
//   - Neither: only file-local facts are available.
type Layout struct {
	// Tree is where the lib is on disk: Root, or CheckoutDir holding the
	// lib directory CheckoutLib.
	fileset.Tree
	// Stubs is the location prefix of a profile's stubs: the stub for lib
	// file "/std/room.c" is read from the file system as
	// Stubs+"/std/room.c". Empty without a profile. A file on disk always
	// wins over its stub.
	Stubs string
	// StubFiles are the lib files the stubs are of ("/std/room.c"), for
	// what looks through all of them: a search by name, the listing of a
	// directory. Nothing else needs them: a stub is found by its name.
	StubFiles []string
	// Covered are lib directory prefixes ("/std/") the stubs describe
	// completely, except for paths matching the Holes globs.
	Covered []string
	Holes   []string
}

// lib is what an index knows for its whole life: the file system, the
// configuration and the layout. It is immutable.
type lib struct {
	fs  vfs.FS
	cfg *config.Config
	lay Layout

	// partial says there is no whole lib on disk: what exists is decided
	// per path by Covers.
	partial bool
	// required are the places on disk of what the configuration says a
	// whole lib has: its include dirs, the force-included header, the
	// auto objects.
	required []requirement

	dialect  token.Dialect
	exclude  *fileset.Matcher
	virtual  *fileset.Matcher
	holes    *fileset.Matcher
	defined  map[string]bool
	override map[string]pp.Class
	// uses reads the Uses of a unit; nil: units have none.
	uses Extractor
	// covered remembers what covers answered, which only the
	// configuration decides.
	covered *table[bool]
}

// Index is the lib's index across generations. It hands out the current
// Snapshot and makes the next one when files change. An Index is safe for
// concurrent use and holds no global state.
type Index struct {
	*lib

	// mu guards everything below. A snapshot's own tables have their own
	// locks, but a unit is added to one only while mu is held, so holding
	// mu is holding the set of units still.
	mu  sync.RWMutex
	cur *Snapshot

	// The reverse edges and the scan state describe the current
	// generation only: they are updated as its units are built and as
	// Invalidate replaces it. A snapshot that has been replaced neither
	// reads nor writes them.

	// builtFrom maps a file to the units whose facts depend on it: the
	// units that #include it, directly or not, and the units that looked
	// for an #include there and did not find it.
	builtFrom map[string]map[string]struct{}
	// children maps a lib path to the units that inherit it directly or
	// #include it as code.
	children map[string]map[string]struct{}
	// refs maps a lib path to the units that asked about that object
	// while being linted (a call target, an object path), with the
	// generation of the question.
	refs map[string]map[string]uint64
	// asked are the units whose lint asked who inherits them, with the
	// generation of the question.
	asked map[string]uint64

	// walked says the scan scope has been listed; todo then holds the
	// files in it that have no unit in the current generation yet. The
	// whole-lib scan is complete when todo is empty.
	walked bool
	todo   map[string]struct{}
	// scanning admits one scan at a time.
	scanning chan struct{}
}

// Open creates the index of a project: over its lib on disk, and, when
// the project has a profile, over the profile's stubs for whatever is not
// on disk.
func Open(prj *config.Project, fsys vfs.FS, opts ...Option) *Index {
	lay := Layout{Tree: prj.Tree()}
	if p := prj.Profile; p != nil {
		stubs := profile.NewFS(fsys, p)
		fsys = stubs
		lay.Stubs, lay.Covered, lay.Holes = stubs.Location(), p.Covered, p.Holes
		lay.StubFiles = slices.Sorted(maps.Keys(p.Files))
	}
	return New(prj.Config, lay, fsys, opts...)
}

// New creates an index over a lib.
func New(cfg *config.Config, lay Layout, fsys vfs.FS, opts ...Option) *Index {
	l := &lib{
		fs: fsys, cfg: cfg, lay: lay,
		partial:  lay.Root == "" && (lay.CheckoutDir != "" || lay.Stubs != ""),
		dialect:  cfg.TokenDialect(),
		exclude:  fileset.NewMatcher(cfg.Exclude),
		virtual:  fileset.NewMatcher(cfg.Conventions.VirtualPaths),
		holes:    fileset.NewMatcher(lay.Holes),
		defined:  cfg.Predefined(),
		override: cfg.MacroClasses(),
		covered:  &table[bool]{},
	}
	for _, o := range opts {
		o(l)
	}

	if lay.Root != "" {
		real := func(libFile string) string {
			return filepath.Join(lay.Root, filepath.FromSlash(strings.TrimPrefix(libFile, "/")))
		}
		// An include directory that is not absolute is a different one
		// for each file (IncludeDirs): no one directory is required.
		for _, d := range cfg.IncludeDirs {
			if strings.HasPrefix(d, "/") {
				l.required = append(l.required, requirement{name: d, path: real(d), dir: true})
			}
		}
		if f := cfg.IncludeFile; f != "" {
			l.required = append(l.required, requirement{name: f, path: real(f)})
		}
		for _, a := range cfg.AutoObjects {
			l.required = append(l.required, requirement{name: a, path: real(objectFile(NormalizeLibPath(a, "/")))})
		}
	}

	ix := &Index{lib: l, scanning: make(chan struct{}, 1)}
	ix.forget()
	ix.cur = newSnapshot(ix, 1, l.extent())
	return ix
}

// requirement is one thing a whole lib has when the configuration is
// right about it.
type requirement struct {
	name, path string
	dir        bool
}

// extent says how much of the lib is on disk, as one generation found
// it.
type extent struct {
	// complete is false when the lib on disk lacks things the config says
	// it has: a wrong root. Missing references are then Unknown.
	complete bool
	notes    []diag.Note
}

// extent looks at the disk for what a whole lib has.
func (l *lib) extent() *extent {
	e := &extent{complete: l.lay.Root != ""}
	var missing []string
	for _, r := range l.required {
		if r.dir && !l.fs.IsDir(r.path) || !r.dir && !l.fs.Exists(r.path) {
			missing = append(missing, r.name)
		}
	}
	if len(missing) > 0 {
		e.complete = false
		e.notes = []diag.Note{{
			Kind: diag.NoteIncompleteLib, Path: l.lay.Root,
			Message: "mudlib looks incomplete under " + l.lay.Root + " (missing " +
				strings.Join(missing, ", ") + "): missing-reference findings are disabled",
		}}
	}
	return e
}

// requires reports whether a change at p can add or remove something a
// whole lib has: p is such a place, lies in one, or holds one.
func (l *lib) requires(p string) bool {
	for _, r := range l.required {
		if _, in := fileset.Under(r.path, p); in {
			return true
		}
		if _, holds := fileset.Under(p, r.path); holds {
			return true
		}
	}
	return false
}

// forget empties the reverse edges. The caller holds mu (or is New).
func (ix *Index) forget() {
	ix.builtFrom = map[string]map[string]struct{}{}
	ix.children = map[string]map[string]struct{}{}
	ix.refs = map[string]map[string]uint64{}
	ix.asked = map[string]uint64{}
}

// HasLib reports whether there is a lib to index at all; without one only
// file-local facts are available.
func (l *lib) HasLib() bool { return l.lay.Root != "" || l.partial }

// Partial reports whether the lib is known only in part: a checkout of
// one subtree and/or a profile's stubs.
func (l *lib) Partial() bool { return l.partial }

// Snapshot returns the current generation.
func (ix *Index) Snapshot() *Snapshot {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.cur
}

// Generation returns the number of the current generation. It grows by
// one with every Invalidate.
func (ix *Index) Generation() uint64 { return ix.Snapshot().gen }

// Invalidate tells the index that the named files changed, were created
// or were deleted (on disk or in an overlay). A path may also be a
// directory that was created, deleted or renamed: it stands for the files
// under it. It makes a new generation that shares everything the change
// cannot have touched, and returns, in sorted order, the files whose
// diagnostics the change can affect: the named files; the files built
// from them; the objects that inherit one of those, however indirectly;
// the files that were linted and asked about one of those objects; and,
// when an object changed, the files whose lint asked who inherits them.
//
// A change that cannot matter (a path outside the lib that nothing was
// read from or looked for at) makes no generation and affects nothing
// but the path itself.
//
// Snapshots handed out before are not disturbed, and what they answer
// stays what it was; results computed from one are results about the
// generation before the change.
func (ix *Index) Invalidate(paths ...string) []string {
	changed := map[string]struct{}{}
	var dirs []string // the paths that are not a file now: a directory, or nothing
	checkExtent := false
	for _, p := range paths {
		p = ix.clean(p)
		changed[p] = struct{}{}
		if !ix.fs.Exists(p) || ix.fs.IsDir(p) {
			dirs = append(dirs, p)
		}
		checkExtent = checkExtent || ix.requires(p)
	}
	// The files of a directory that appeared.
	for _, d := range dirs {
		if ix.fs.IsDir(d) {
			ix.lay.Walk(context.Background(), ix.fs, d, []string{".c", ".h"}, ix.exclude, func(p string) {
				changed[ix.clean(p)] = struct{}{}
			})
		}
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()
	old := ix.cur
	// The files known under a directory that changed or went away.
	for _, d := range dirs {
		prefix := strings.TrimSuffix(d, string(filepath.Separator)) + string(filepath.Separator)
		under := func(p string) {
			if strings.HasPrefix(p, prefix) {
				changed[p] = struct{}{}
			}
		}
		for p := range old.units.keys() {
			under(p)
		}
		for p := range old.headers.keys() {
			under(p)
		}
		for p := range ix.builtFrom {
			under(p)
		}
		for p := range ix.todo {
			under(p)
		}
	}
	// A path that is no place in the lib and that nothing was read from
	// or looked for at changes nothing.
	known := func(p string) bool {
		_, built := ix.builtFrom[p]
		return built || ix.LibPath(p) != "" || old.units.has(p) || old.headers.has(p)
	}
	if !checkExtent && !slices.ContainsFunc(slices.Collect(maps.Keys(changed)), known) {
		return sorted(changed)
	}
	exists := map[string]bool{} // of the changed files the scan covers
	for p := range changed {
		if ix.inScope(p) {
			exists[p] = ix.fs.Exists(p) && !ix.fs.IsDir(p)
		}
	}

	ext := old.ext
	if checkExtent {
		ext = ix.extent()
	}
	next := newSnapshot(ix, old.gen+1, ext)
	affected := maps.Clone(changed)
	everyUnit := func() {
		for key, u := range old.units.complete() {
			if u != nil {
				affected[key] = struct{}{}
			}
		}
	}
	// rescan puts a dropped unit's file back on the scan's list; listed
	// then corrects the list for files that were created or deleted.
	rescan := func(key string) {
		if ix.walked && ix.inScope(key) {
			ix.todo[key] = struct{}{}
		}
	}
	listed := func() {
		for p, is := range exists {
			if is && ix.walked {
				ix.todo[p] = struct{}{}
			} else {
				delete(ix.todo, p)
			}
		}
	}

	// The force-included header is part of every file, and whether the
	// lib is all there decides what every file may conclude from a miss.
	if b, ok := old.base.get(); ok && b.touchedBy(changed) || ext.complete != old.ext.complete {
		everyUnit()
		for key := range old.units.keys() {
			rescan(key)
		}
		listed()
		next.headers = old.headers.carry(changed)
		ix.forget()
		ix.cur = next
		return sorted(affected)
	}

	// The units built from a changed file.
	stale := map[string]struct{}{}
	for p := range changed {
		if old.units.has(p) {
			stale[p] = struct{}{}
		}
		for key := range ix.builtFrom[p] {
			stale[key] = struct{}{}
		}
	}
	// The objects whose program changed with them: those units and the
	// changed files themselves (which may not have existed), then
	// everything that inherits one of those or includes it as code.
	objects := map[string]struct{}{}
	for p := range changed {
		if lp := ix.LibPath(p); lp != "" {
			objects[lp] = struct{}{}
		}
	}
	for key := range stale {
		if u, _ := old.units.get(key); u != nil && u.LibPath != "" {
			objects[u.LibPath] = struct{}{}
		}
	}
	objectChanged := len(objects) > 0
	for queue := slices.Collect(maps.Keys(objects)); len(queue) > 0; queue = queue[1:] {
		for key := range ix.children[queue[0]] {
			affected[key] = struct{}{}
			u, _ := old.units.get(key)
			if u == nil {
				continue
			}
			if _, seen := objects[u.LibPath]; !seen {
				objects[u.LibPath] = struct{}{}
				queue = append(queue, u.LibPath)
			}
		}
	}
	for key := range stale {
		affected[key] = struct{}{}
	}

	// The linted files that asked about one of those objects. A question
	// is void once the unit that asked has been rebuilt.
	asks := func(key string, gen uint64) bool {
		u, _ := old.units.get(key)
		_, gone := stale[key]
		return u != nil && !gone && u.gen <= gen
	}
	for lp := range objects {
		for key, gen := range ix.refs[lp] {
			if asks(key, gen) {
				affected[key] = struct{}{}
			} else {
				delete(ix.refs[lp], key)
			}
		}
	}
	// Who inherits a file can change with any object.
	if objectChanged {
		for key, gen := range ix.asked {
			if asks(key, gen) {
				affected[key] = struct{}{}
			} else {
				delete(ix.asked, key)
			}
		}
	}
	// Every object gets the auto object's functions.
	autoChanged := false
	for _, a := range ix.cfg.AutoObjects {
		if _, hit := objects[NormalizeLibPath(a, "/")]; hit {
			autoChanged = true
			everyUnit()
		}
	}

	for key := range stale {
		if u, _ := old.units.get(key); u != nil {
			ix.unregister(u)
		}
		rescan(key)
	}
	listed()

	next.units = old.units.carry(stale)
	next.headers = old.headers.carry(changed)
	next.base = old.base.carry()
	if !autoChanged {
		next.auto = old.auto.carry()
	}
	next.chains = without(old.chains, objects)
	for p, why := range old.failed {
		if _, gone := stale[p]; !gone {
			next.failed[p] = why
		}
	}
	ix.cur = next
	return sorted(affected)
}

// built records a unit the snapshot finished building. The caller holds
// mu.
func (ix *Index) built(s *Snapshot, key string, u *Unit) {
	if ix.cur != s {
		return
	}
	delete(ix.todo, key)
	if u != nil {
		ix.register(u)
	}
}

func (ix *Index) register(u *Unit) {
	add := func(m map[string]map[string]struct{}, k string) {
		set := m[k]
		if set == nil {
			set = map[string]struct{}{}
			m[k] = set
		}
		set[u.Path] = struct{}{}
	}
	for d := range u.watched {
		add(ix.builtFrom, d)
	}
	for _, parent := range u.parents() {
		add(ix.children, parent)
	}
}

func (ix *Index) unregister(u *Unit) {
	del := func(m map[string]map[string]struct{}, k string) {
		if set := m[k]; set != nil {
			delete(set, u.Path)
			if len(set) == 0 {
				delete(m, k)
			}
		}
	}
	for d := range u.watched {
		del(ix.builtFrom, d)
	}
	for _, parent := range u.parents() {
		del(ix.children, parent)
	}
}

// parents are the lib paths whose program is part of u's: what it
// inherits directly and what it includes as code.
func (u *Unit) parents() []string {
	if u.LibPath == "" {
		return nil
	}
	out := slices.Clone(u.code)
	for _, ref := range u.Inherits {
		if ref.Resolved {
			out = append(out, ref.Path)
		}
	}
	return out
}

func sorted(set map[string]struct{}) []string {
	return slices.Sorted(maps.Keys(set))
}
