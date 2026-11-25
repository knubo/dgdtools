package rename

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mmcdole/dgdtools/internal/par"
	"github.com/mmcdole/dgdtools/pkg/fileset"
	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/resolve"
	"github.com/mmcdole/dgdtools/pkg/syntax"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// Move is a file or a directory of the lib that moves (is renamed), by
// its paths on disk.
type Move struct {
	From, To string
}

// Files plans what moving files of the lib changes in the files that
// name them: the path of every inherit statement, #include and object
// path handed to a function that takes one (clone_object, a call's
// receiver, a registrar's) that names a moved file, and those of the moved
// files themselves that name a file from where they were (a relative
// #include), written so that they name the same file from where it is
// to be. A directory moves with every file under it that the index knows.
//
// A path is rewritten as it was written: a path from the lib's root as
// one, a relative one relative to the new place when the new place is
// below the file, an #include <...> from the include directory that has
// the new place; else from the lib's root. Each new path is resolved as
// the driver will once the files have moved, and is the moved file there
// or is not made. A path that is not one string as written (made by a
// macro, ROOM; or a sum, DIR + "room") is Possible, and listed without
// an edit: its macro names what other files name too.
//
// The edits are of the files as they are now, before the move: an edit
// of a moved file is at its old path. The plan's Symbol is the file moved
// when one file moves (resolve.Object for an object's .c, resolve.Include
// for another file), with Name and New its lib paths; Moves are the files,
// the directories expanded. Files is nil, nil when no move is of a file of
// the lib. It needs the whole-lib scan, which it runs for as long as ctx
// allows.
func Files(ctx context.Context, snap *index.Snapshot, moves []Move) (*Plan, error) {
	if snap == nil || !snap.HasLib() {
		return nil, nil
	}
	snap.Scan(ctx)
	units, complete := snap.UseUnits(ctx)
	if !complete || ctx.Err() != nil {
		if !snap.Current() {
			return nil, refuse("files changed while the move was being planned: try again")
		}
		return nil, refuse("the mudlib is not indexed in full yet, so it cannot be told which files name the moved ones: try again in a moment")
	}
	fm := &fileMover{snap: snap, to: map[string]string{}, dest: map[string]bool{}, units: map[string]*index.Unit{}}
	for _, u := range units {
		fm.units[u.Path] = u
	}
	for _, m := range moves {
		from, to := filepath.Clean(m.From), filepath.Clean(m.To)
		if snap.LibFile(from) == "" {
			continue
		}
		if _, ok := fm.units[from]; ok {
			fm.add(from, to)
			continue
		}
		// A directory: the files under it the index knows, and the
		// objects' files on disk; else a file.
		dir := false
		under := func(p string) {
			if rel, ok := fileset.Under(from, p); ok && rel != "." {
				fm.add(p, filepath.Join(to, rel))
				dir = true
			}
		}
		for _, u := range units {
			under(u.Path)
		}
		if files, err := snap.Files(ctx, from); err == nil {
			for _, p := range files {
				under(p)
			}
		}
		if !dir {
			fm.add(from, to)
		}
	}
	if len(fm.moves) == 0 {
		return nil, nil
	}
	plan := &Plan{Files: len(units), Partial: snap.Partial()}
	first := fm.moves[0]
	if len(moves) == 1 && len(fm.moves) == 1 {
		plan.Symbol = Symbol{Kind: resolve.Include, Name: snap.LibFile(first.From), Def: resolve.Def{Path: first.From}}
		if filepath.Ext(first.From) == ".c" {
			plan.Symbol.Kind = resolve.Object
		}
		plan.New = cmp.Or(snap.LibFile(first.To), first.To)
	} else {
		plan.Symbol = Symbol{Name: cmp.Or(snap.LibFile(filepath.Clean(moves[0].From)), moves[0].From)}
		plan.New = cmp.Or(snap.LibFile(filepath.Clean(moves[0].To)), moves[0].To)
	}
	plan.Moves = fm.moves

	// The places that name a moved file, and what the moved files name.
	type ref struct {
		unit *index.Unit
		use  index.Use
	}
	var refs []ref
	for _, m := range fm.moves {
		names := []string{m.From}
		if lp := snap.LibPath(m.From); lp != "" && filepath.Ext(m.From) == ".c" {
			names = append(names, lp)
		}
		for _, u := range units {
			for _, name := range names {
				for _, use := range u.Uses(name) {
					refs = append(refs, ref{u, use})
				}
			}
		}
		// The calls made in a moved object by its path: the receiver.
		if len(names) > 1 {
			for _, u := range units {
				for _, use := range u.UsesWith(names[1]) {
					if use.Kind == index.UseFunction && use.Form == index.FormPath {
						refs = append(refs, ref{u, use})
					}
				}
			}
		}
		u := fm.units[m.From]
		if u == nil {
			u = snap.UnitAt(m.From)
		}
		if u != nil {
			for _, use := range u.AllUses() {
				if use.Kind == index.UseObject || use.Kind == index.UseInclude || use.Kind == index.UseFunction && use.Form == index.FormPath {
					refs = append(refs, ref{u, use})
				}
			}
		}
	}
	srcs := map[string][]byte{}
	seen := map[[2]any]bool{}
	trees := map[string]*index.Parsed{}
	for _, r := range refs {
		if r.use.Kind == index.UseFunction {
			// The object of a call, by the path it is made in.
			p, ok := trees[r.unit.Path]
			if !ok {
				p, _ = snap.Parse(r.unit.Path)
				trees[r.unit.Path] = p
			}
			use, ok := receiver(p, r.use)
			if !ok {
				o := Occurrence{Path: r.unit.Path, Start: r.use.Start, End: r.use.End, Line: r.use.Line, Col: r.use.Col, Class: Possible,
					Reason: fmt.Sprintf("calls %s in the object %s, whose path is not written here as one string: not rewritten", r.use.Name, r.use.Aux)}
				if k := [2]any{r.unit.Path, r.use.Start}; !seen[k] {
					seen[k] = true
					plan.Occurrences = append(plan.Occurrences, o)
				}
				continue
			}
			r.use = use
		}
		if r.use.Kind != index.UseObject && r.use.Kind != index.UseInclude {
			continue
		}
		if k := [2]any{r.unit.Path, r.use.Start}; seen[k] {
			continue
		} else {
			seen[k] = true
		}
		src, ok := srcs[r.unit.Path]
		if !ok {
			var err error
			if src, err = snap.ReadFile(r.unit.Path); err != nil {
				return nil, refuse("%s cannot be read: %v", fm.name(r.unit.Path), err)
			}
			srcs[r.unit.Path] = src
		}
		if int(r.use.End) > len(src) || r.use.Start > r.use.End {
			return nil, refuse("files changed while the move was being planned: try again")
		}
		o, e := fm.rewrite(r.unit, r.use, src)
		if o == nil {
			continue
		}
		plan.Occurrences = append(plan.Occurrences, *o)
		if e != nil {
			plan.Edits = append(plan.Edits, *e)
		}
	}
	if err := fm.strings(ctx, plan, units); err != nil {
		return nil, err
	}
	slices.SortFunc(plan.Occurrences, func(x, y Occurrence) int {
		return cmp.Or(cmp.Compare(x.Path, y.Path), cmp.Compare(x.Start, y.Start))
	})
	byPlace := func(x, y Edit) int {
		return cmp.Or(cmp.Compare(x.Path, y.Path), cmp.Compare(x.Start, y.Start))
	}
	slices.SortFunc(plan.Edits, byPlace)
	slices.SortFunc(plan.PossibleEdits, byPlace)
	fm.warn(ctx, plan)
	if !snap.Current() {
		return nil, refuse("files changed while the move was being planned: try again")
	}
	return plan, nil
}

// fileMover is a plan of moved files in the making.
type fileMover struct {
	snap  *index.Snapshot
	moves []Move
	// to maps a moved file to where it moves; dest has the places moved to.
	to    map[string]string
	dest  map[string]bool
	units map[string]*index.Unit
}

func (fm *fileMover) add(from, to string) {
	if _, ok := fm.to[from]; ok || from == to {
		return
	}
	fm.to[from], fm.dest[to] = to, true
	fm.moves = append(fm.moves, Move{From: from, To: to})
}

// moved returns where the file at p is once the files have moved.
func (fm *fileMover) moved(p string) string {
	if to, ok := fm.to[p]; ok {
		return to
	}
	return p
}

// exists says whether there is a file at p once the files have moved,
// where the move decides it.
func (fm *fileMover) exists(p string) (is, decided bool) {
	p = filepath.Clean(p)
	if fm.dest[p] {
		return true, true
	}
	if _, gone := fm.to[p]; gone {
		return false, true
	}
	return false, false
}

func (fm *fileMover) name(p string) string { return cmp.Or(fm.snap.LibFile(p), p) }

// literal returns the bytes of the string a path is written as, between
// its quotes or brackets: [cs, ce) of src, the opening delimiter; false
// when the path is not one string as written.
func literal(src []byte, start, end uint32) (cs, ce uint32, open byte, ok bool) {
	text := src[start:end]
	switch {
	case len(text) >= 2 && (text[0] == '"' && text[len(text)-1] == '"' || text[0] == '<' && text[len(text)-1] == '>'):
		cs, ce, open = start+1, end-1, text[0]
	case start > 0 && int(end) < len(src) && src[start-1] == '"' && src[end] == '"':
		cs, ce, open = start, end, '"'
	default:
		return 0, 0, 0, false
	}
	if bytes.ContainsAny(src[cs:ce], "\"\\<>\n") {
		return 0, 0, 0, false
	}
	return cs, ce, open, true
}

// rewrite says what moving the files makes of a Use of the unit's that
// names a file: nothing when it names what it named, or names no moved
// file from no moved place; else an occurrence, and the edit when it can
// be written so that it does.
func (fm *fileMover) rewrite(u *index.Unit, use index.Use, src []byte) (*Occurrence, *Edit) {
	snap := fm.snap
	var target string // the file named: on disk, or a profile's stub
	if use.Kind == index.UseInclude {
		target = use.Name
	} else {
		target = snap.ObjectFile(use.Name) // a stub's is a URI, not to be cleaned
	}
	here, there := u.Path, fm.moved(u.Path)
	newTarget := fm.moved(target)
	if here == there && target == newTarget {
		return nil, nil
	}
	cs, ce, open, ok := literal(src, use.Start, use.End)
	if target == newTarget && use.Kind == index.UseObject {
		// Only the file that names it moves: an object path names the
		// same object from anywhere, unless the lib resolves one without
		// a leading slash from the file that says it.
		fromFile := snap.ObjectPath("x", "/d/f") != "/x"
		if !fromFile || ok && strings.HasPrefix(string(src[cs:ce]), "/") {
			return nil, nil
		}
	}
	o := &Occurrence{Path: here, Start: use.Start, End: use.End, Line: use.Line, Col: use.Col}
	possible := func(reason string) (*Occurrence, *Edit) {
		o.Class, o.Reason = Possible, reason
		return o, nil
	}
	newLib := snap.LibFile(newTarget)
	if newLib == "" {
		return possible(fmt.Sprintf("names %s, which moves out of the mudlib", fm.name(target)))
	}
	if !ok {
		what := "an expression"
		if text := strings.TrimSpace(string(src[use.Start:use.End])); isName(text) {
			what = "the macro " + text
		}
		return possible(fmt.Sprintf("names %s by %s, which other files may use too: not rewritten", fm.name(target), what))
	}
	written := string(src[cs:ce])
	thereLib := snap.LibFile(there)

	var candidates []string // with their delimiters for an #include
	if use.Kind == index.UseInclude {
		inc := pp.Include{Name: written, Angle: open == '<'}
		resolves := func(raw string) bool {
			c := pp.Include{Name: raw[1 : len(raw)-1], Angle: raw[0] == '<'}
			p, ok := snap.ResolveIncludeIf(there, c, fm.exists)
			return ok && p == newTarget // both clean, and a stub's URI must stay one
		}
		if resolves(inc.Raw()) {
			return nil, nil
		}
		dir := path.Dir(thereLib)
		if !inc.Angle {
			if strings.HasPrefix(written, "/") {
				candidates = append(candidates, `"`+newLib+`"`)
			} else if rel, ok := strings.CutPrefix(newLib, strings.TrimSuffix(dir, "/")+"/"); ok {
				candidates = append(candidates, `"`+rel+`"`)
			}
		}
		incDirs, _ := snap.IncludeDirs(thereLib)
		for _, d := range incDirs {
			if rel, ok := strings.CutPrefix(newLib, strings.TrimSuffix(d, "/")+"/"); ok {
				if inc.Angle {
					candidates = append(candidates, "<"+rel+">")
				} else {
					candidates = append(candidates, `"`+rel+`"`)
				}
			}
		}
		if !inc.Angle && slices.Contains(strings.Split(written, "/"), "..") {
			// Up the tree, as it was written.
			candidates = append(candidates, `"`+relPath(dir, newLib)+`"`)
		}
		candidates = append(candidates, `"`+newLib+`"`)
		for _, c := range candidates {
			if resolves(c) {
				if c[0] != open {
					cs, ce = cs-1, ce+1
				} else {
					c = c[1 : len(c)-1]
				}
				o.Class, o.Start, o.End = Certain, cs, ce
				return o, &Edit{Path: here, Start: cs, End: ce, NewText: c}
			}
		}
		return possible(fmt.Sprintf("no #include written as this one names %s from %s", newLib, fm.name(there)))
	}

	// An object's path.
	if filepath.Ext(newTarget) != ".c" {
		return possible(fmt.Sprintf("names the object %s, whose file moves to %s, which is no object's", use.Name, newLib))
	}
	newObj := strings.TrimSuffix(newLib, ".c")
	thereObj := strings.TrimSuffix(thereLib, ".c")
	if snap.ObjectPath(written, thereObj) == newObj {
		return nil, nil
	}
	ext := ""
	if strings.HasSuffix(written, ".c") {
		ext = ".c"
	}
	if !strings.HasPrefix(written, "/") {
		// Relative to the lib's root or to the file, as the lib has it.
		candidates = append(candidates, strings.TrimPrefix(newObj, "/")+ext)
		if rel, ok := strings.CutPrefix(newObj, strings.TrimSuffix(path.Dir(thereObj), "/")+"/"); ok {
			candidates = append(candidates, rel+ext)
		}
	}
	candidates = append(candidates, newObj+ext)
	for _, c := range candidates {
		if snap.ObjectPath(c, thereObj) == newObj {
			o.Class, o.Start, o.End = Certain, cs, ce
			return o, &Edit{Path: here, Start: cs, End: ce, NewText: c}
		}
	}
	return possible(fmt.Sprintf("no path written as this one names %s from %s", newObj, fm.name(there)))
}

// strings adds the string literals of the lib that are a moved object's
// path and that no occurrence covers: the path from the lib's root, with
// or without ".c", and without its leading slash when the lib resolves
// such a path from its root. A lib names its rooms and objects that way
// in calls the index does not know take a path (add_exit("/room/church",
// ...)), and in the bodies of macros. Each is Possible, with the edit
// that names the new place in the same form.
func (fm *fileMover) strings(ctx context.Context, plan *Plan, units []*index.Unit) error {
	snap := fm.snap
	fromRoot := snap.ObjectPath("x", "/d/f") == "/x"
	type form struct{ old, new string }
	var forms []form
	var needles [][]byte
	for _, m := range plan.Moves {
		if filepath.Ext(m.From) != ".c" || filepath.Ext(m.To) != ".c" {
			continue
		}
		oldObj, newObj := snap.LibPath(m.From), snap.LibPath(m.To)
		if oldObj == "" || newObj == "" {
			continue
		}
		for _, ext := range []string{"", ".c"} {
			forms = append(forms, form{`"` + oldObj + ext + `"`, newObj + ext})
			if fromRoot {
				forms = append(forms, form{`"` + oldObj[1:] + ext + `"`, newObj[1:] + ext})
			}
		}
		needles = append(needles, []byte(oldObj[1:]))
	}
	if len(forms) == 0 {
		return nil
	}
	covered := map[string][][2]uint32{}
	for _, o := range plan.Occurrences {
		covered[o.Path] = append(covered[o.Path], [2]uint32{o.Start, o.End})
	}
	isCovered := func(path string, start, end uint32) bool {
		for _, r := range covered[path] {
			if r[0] < end && start < r[1] {
				return true
			}
		}
		return false
	}
	type found struct {
		occ  []Occurrence
		edit []Edit
	}
	results := make([]found, len(units))
	idx := make([]int, len(units))
	for i := range idx {
		idx[i] = i
	}
	dialect := snap.Dialect()
	par.Each(ctx, idx, func(i int) {
		u := units[i]
		src, err := snap.ReadFile(u.Path)
		if err != nil || !slices.ContainsFunc(needles, func(n []byte) bool { return bytes.Contains(src, n) }) {
			return
		}
		f := lexer.Lex(u.Path, src, dialect)
		for _, t := range f.Tokens {
			if t.Kind != token.StringLit && t.Kind != token.Directive {
				continue
			}
			text := f.Text(t)
			for _, fo := range forms {
				for k := 0; ; {
					j := bytes.Index(text[k:], []byte(fo.old))
					if j < 0 {
						break
					}
					start := t.Off + uint32(k+j) + 1 // inside the quotes
					end := start + uint32(len(fo.old)) - 2
					k += j + len(fo.old)
					if t.Kind == token.StringLit && len(text) != len(fo.old) || isCovered(u.Path, start, end) {
						continue
					}
					pos := f.Pos(start)
					where := "a string"
					if t.Kind == token.Directive {
						where = "a string in a directive"
					}
					results[i].occ = append(results[i].occ, Occurrence{Path: u.Path, Start: start, End: end,
						Line: uint32(pos.Line), Col: uint32(pos.Col), Class: Possible,
						Reason: fmt.Sprintf("%s equal to the old path, not known to be an object path", where)})
					results[i].edit = append(results[i].edit, Edit{Path: u.Path, Start: start, End: end, NewText: fo.new})
				}
			}
		}
	})
	if ctx.Err() != nil {
		return refuse("cancelled before every file was searched")
	}
	for _, r := range results {
		plan.Occurrences = append(plan.Occurrences, r.occ...)
		plan.PossibleEdits = append(plan.PossibleEdits, r.edit...)
	}
	return nil
}

// relPath returns the slash-separated path target relative to dir,
// both from the lib's root.
func relPath(dir, target string) string {
	d := strings.Split(strings.Trim(dir, "/"), "/")
	t := strings.Split(strings.Trim(target, "/"), "/")
	if d[0] == "" {
		d = nil
	}
	i := 0
	for i < len(d) && i < len(t)-1 && d[i] == t[i] {
		i++
	}
	return strings.Repeat("../", len(d)-i) + strings.Join(t[i:], "/")
}

// receiver returns the object a call of the Use (a call in an object by
// its path) is made in, as a Use of the object: the x of x->fn(), the
// first argument of call_other; false when that cannot be found in the
// file's tree.
func receiver(p *index.Parsed, use index.Use) (index.Use, bool) {
	if p == nil || p.Tree == nil {
		return index.Use{}, false
	}
	var x syntax.Expr
	syntax.Inspect(p.Tree, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.ArrowExpr:
			if n.Name != nil && p.Tree.Off(n.Name) == use.Start {
				x = n.X
			}
		case *syntax.CallExpr:
			if n.Fun != nil && n.Fun.Name == "call_other" && len(n.Args) > 1 && p.Tree.Off(n.Args[1])+1 == use.Start {
				x = n.Args[0]
			}
		}
		return x == nil
	})
	if x == nil {
		return index.Use{}, false
	}
	start, end := p.Tree.Off(x), p.Tree.End(x)
	pos := p.Tok.Pos(start)
	return index.Use{Name: use.Aux, Kind: index.UseObject, Start: start, End: end, Line: uint32(pos.Line), Col: uint32(pos.Col)}, true
}

// isName reports whether s is an identifier.
func isName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// warn adds what the person moving files should know: a partial
// checkout, the moved files other objects inherit.
func (fm *fileMover) warn(ctx context.Context, plan *Plan) {
	if plan.Partial {
		plan.Warnings = append(plan.Warnings, "this checkout is part of the mudlib: the places outside it that name the moved files are not changed, and cannot be seen from here")
	}
	var bases []string
	for _, m := range plan.Moves {
		lp := fm.snap.LibPath(m.From)
		if isHeader(m.From) || lp == "" {
			continue
		}
		if users, _ := fm.snap.Users(ctx, lp); len(users) > 0 {
			what := "objects inherit"
			if len(users) == 1 {
				what = "object inherits"
			}
			bases = append(bases, fmt.Sprintf("%s (%d %s it)", fm.name(m.From), len(users), what))
		}
	}
	if len(bases) > 3 {
		bases = append(bases[:3], fmt.Sprintf("and %d more", len(bases)-3))
	}
	if len(bases) > 0 {
		plan.Warnings = append(plan.Warnings, "moved base files: "+strings.Join(bases, ", ")+
			": an object keeps the program it was compiled with, so the objects that inherit them must be recompiled for the move to take effect")
	}
}
