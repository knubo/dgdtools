package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/rename"
	"github.com/mmcdole/dgdtools/pkg/resolve"
	"github.com/mmcdole/dgdtools/pkg/token"
)

const renameUsage = `usage: dgdtools rename [flags] SYMBOL NEW
       dgdtools rename [flags] FILE NEW

Renames SYMBOL to NEW in every file of the mudlib that names it: a
function with its redefinitions in the objects that inherit it and those
it inherits (one function by the driver's rules) and every call that
reaches one of them; a global variable in its object and the objects
that inherit it; a header's macro in every file that includes the header;
a local, a parameter, a private function or variable in its file.

SYMBOL is one of:

  /std/living::query_name   a function or a global variable of an object,
                            by the object's mudlib path (".c" may be
                            written): its own, or one it inherits
  /include/std.h::ROOM      a macro, by the mudlib path of the file that
                            #defines it (a header, or an object's file)
  FILE:LINE:COL             what is named at that place of a file on disk
                            (from the working directory; line and column
                            from 1, the column counting bytes, as lint
                            reports them): how a local, a parameter or a
                            label is named

FILE is a file of the mudlib by its mudlib path, /std/room.c or
/include/std.h, and NEW its new mudlib path (or a name, for the same
directory): the file is moved, and every inherit statement, #include and
object path handed to a function (clone_object, a call's receiver) that
names it is rewritten as it was written, as are the moved file's own
paths that name a file from where it was. A path made by a macro or an
expression is possible, and not changed.

Every place the old name stands is certain (the symbol), possible (it may
be, and nobody can tell: a call on an object that cannot be told, a name
in a macro's body, code that is not compiled or does not parse) or
excluded (provably something else). The certain places are renamed; the
possible ones with --include-possible.

By default nothing is written: the edits are printed as unified diffs,
grouped by owner (a wizard's or a domain's tree, by the checkout patterns
of profile_build, players/* and d/* when none are set; "other" for the
rest), and a summary goes to standard error. --write writes the files,
each replaced as a whole.

--write writes the files, and moves the FILE last.

A variable that save_object writes is refused: renamed, the values saved
under the old name are no longer restored. --force renames it all the
same.

Exit codes: 0 the rename is complete (no possible place left unrenamed);
1 possible places were left unrenamed; 2 usage or configuration error, or
the rename is refused (the reason is printed); 130 interrupted.
`

// renameCmd is `dgdtools rename`: pkg/rename's plan for a symbol written
// out, as a diff, a summary, JSON, or written to the files.
func renameCmd(ctx context.Context, args []string, _ io.Reader, stdout, stderr io.Writer) int {
	var (
		project              projectFlags
		format               string
		write, diff, quiet   bool
		includePossible, frc bool
	)
	fs := newFlags("rename", renameUsage, stderr)
	project.register(fs)
	fs.BoolVar(&write, "write", false, "write the edits to the files")
	fs.BoolVar(&diff, "diff", false, "print the edits as unified diffs and write nothing (the default)")
	fs.BoolVar(&includePossible, "include-possible", false, "also rename the places that may be the symbol")
	fs.BoolVar(&frc, "force", false, "rename a variable that save_object writes")
	fs.StringVar(&format, "output-format", "human", "how the plan is written: human (diffs and a summary) or json (a `format`)")
	fs.BoolVar(&quiet, "quiet", false, "no summary on standard error")
	fs.BoolVar(&quiet, "q", false, "short for --quiet")
	pos, code, done := parse(fs, args)
	if done {
		return code
	}
	fail := func(err error) int { return failure(stderr, "rename", err) }
	switch {
	case len(pos) != 2:
		fmt.Fprint(stderr, "dgdtools rename: want SYMBOL and NEW\n\n")
		fs.Usage()
		return 2
	case write && diff:
		return fail(errors.New("--write and --diff cannot be combined"))
	case format != "human" && format != "json":
		return fail(fmt.Errorf("--output-format: unknown format %q (want human or json)", format))
	}
	p, err := environment()
	if err != nil {
		return fail(err)
	}
	sym, newName := pos[0], pos[1]
	if isFileArg(sym) {
		return moveCmd(ctx, p, &project, sym, newName, write, includePossible, quiet, format, stdout, stderr)
	}
	where := symbolPlace(p, sym)
	if where.err != nil {
		return fail(where.err)
	}
	l, err := newLinter(p, &project, where.anchor())
	if err != nil {
		return fail(err)
	}
	if !l.Project().HasLib() {
		return fail(errors.New("no mudlib: a rename needs a dgdtools.yml with the root of the lib (or --root, or --profile and --lib-path for a checkout)"))
	}
	if !l.Scan(ctx) {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		return fail(errors.New("the mudlib could not be indexed"))
	}
	path, off, err := where.locate(l)
	if err != nil {
		return refused(stderr, err)
	}
	doc, err := l.Lint(ctx, path, lint.Options{})
	if err != nil {
		return fail(err)
	}
	plan, err := rename.Rename(ctx, doc.RenameFile(frc), off, newName)
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}
	if err != nil {
		return refused(stderr, err)
	}
	return newRenamer(p, l, plan, includePossible, stdout, stderr).finish(write, quiet, format)
}

// newRenamer is the run of `dgdtools rename` of a plan.
func newRenamer(p process, l *lint.Linter, plan *rename.Plan, possible bool, stdout, stderr io.Writer) *renamer {
	r := &renamer{process: p, l: l, plan: plan, possible: possible, stdout: stdout, stderr: stderr}
	r.checkouts = l.Project().Config.ProfileBuild.Checkouts
	if len(r.checkouts) == 0 && l.Project().Profile != nil {
		r.checkouts = l.Project().Profile.Checkouts
	}
	if len(r.checkouts) == 0 {
		r.checkouts = []string{"players/*", "d/*"}
	}
	return r
}

// finish shows the plan, or writes it, and returns the exit code: the
// files edited, and for a plan of moved files the files moved, last.
func (r *renamer) finish(write, quiet bool, format string) int {
	plan, stderr := r.plan, r.stderr
	fail := func(err error) int { return failure(stderr, "rename", err) }
	files, err := r.files()
	if err != nil {
		return fail(err)
	}
	written := 0
	if write {
		for _, f := range files {
			if err := writeFile(f.path, f.out); err != nil {
				if !quiet && format == "human" {
					r.summary(files, written)
				}
				return fail(fmt.Errorf("%s: %w (%d of %d files written)", f.name, err, written, len(files)))
			}
			written++
		}
		for _, m := range plan.Moves {
			if err := moveFile(m.From, m.To); err != nil {
				return fail(fmt.Errorf("%s written, and %s not moved: %w", plural(written, "file"), r.name(m.From), err))
			}
		}
		r.moved = plan.Moves != nil
	}
	switch {
	case format == "json":
		if err := r.json(files, write); err != nil {
			return fail(err)
		}
	case !write:
		if err := r.diffs(files); err != nil {
			return fail(err)
		}
	}
	if !quiet && format == "human" {
		r.summary(files, written)
	}
	if len(plan.PossibleEdits) > 0 && !r.possible || plan.Moves != nil && plan.Count(rename.Possible) > 0 {
		return 1
	}
	return 0
}

// isFileArg reports whether SYMBOL is a FILE: a path of the mudlib with
// the extension of a source file, and nothing after it.
func isFileArg(sym string) bool {
	if strings.Contains(sym, "::") || placeRE.MatchString(sym) {
		return false
	}
	ext := path.Ext(sym)
	return ext == ".c" || ext == ".h"
}

// moveCmd is `dgdtools rename FILE NEW`: pkg/rename's plan of the file
// moved (rename.Files).
func moveCmd(ctx context.Context, p process, project *projectFlags, file, to string, write, possible, quiet bool, format string, stdout, stderr io.Writer) int {
	fail := func(err error) int { return failure(stderr, "rename", err) }
	from := "/" + strings.TrimPrefix(filepath.ToSlash(file), "/")
	if !strings.Contains(to, "/") {
		to = path.Join(path.Dir(from), to)
	}
	to = path.Clean("/" + strings.TrimPrefix(filepath.ToSlash(to), "/"))
	l, err := newLinter(p, project, nil)
	if err != nil {
		return fail(err)
	}
	if !l.Project().HasLib() {
		return fail(errors.New("no mudlib: moving a file needs a dgdtools.yml with the root of the lib (or --root, or --profile and --lib-path for a checkout)"))
	}
	tree := l.Project().Tree()
	fromPath, ok := tree.Path(from)
	if !ok {
		return fail(fmt.Errorf("%s is not a file of this checkout", from))
	}
	toPath, ok := tree.Path(to)
	if !ok {
		return fail(fmt.Errorf("%s is not a place in this checkout", to))
	}
	if info, err := os.Stat(fromPath); err != nil || info.IsDir() {
		return fail(fmt.Errorf("%s: no such file in the mudlib", from))
	}
	if _, err := os.Stat(toPath); err == nil {
		return fail(fmt.Errorf("%s exists already", to))
	}
	if !l.Scan(ctx) {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		return fail(errors.New("the mudlib could not be indexed"))
	}
	plan, err := rename.Files(ctx, l.Index().Snapshot(), []rename.Move{{From: fromPath, To: toPath}})
	switch {
	case ctx.Err() != nil:
		return fail(ctx.Err())
	case err != nil:
		return refused(stderr, err)
	case plan == nil:
		return fail(fmt.Errorf("%s is not a file of the mudlib", from))
	}
	return newRenamer(p, l, plan, possible, stdout, stderr).finish(write, quiet, format)
}

// moveFile moves the file at from to to, making the directories to it.
func moveFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.Rename(from, to)
}

// refused reports a refusal of the rename, or another error of it.
func refused(stderr io.Writer, err error) int {
	var no *rename.Refusal
	if errors.As(err, &no) {
		msg := no.Reason
		if no.Forcible {
			msg += " (--force renames it all the same)"
		}
		fmt.Fprintf(stderr, "dgdtools rename: refused: %s\n", msg)
		return 2
	}
	return failure(stderr, "rename", err)
}

// place is a symbol as the command line writes it.
type place struct {
	// path and line, col: FILE:LINE:COL, the file made absolute.
	path      string
	line, col int
	// lib and name: PATH::NAME.
	lib, name string
	err       error
}

var placeRE = regexp.MustCompile(`^(.+):(\d+):(\d+)$`)

// symbolPlace reads SYMBOL.
func symbolPlace(p process, sym string) place {
	if lib, name, ok := cutLast(sym, "::"); ok {
		if !token.IsIdent(name) || lib == "" {
			return place{err: fmt.Errorf("%q: want PATH::NAME, NAME a name", sym)}
		}
		return place{lib: "/" + strings.TrimPrefix(filepath.ToSlash(lib), "/"), name: name}
	}
	if m := placeRE.FindStringSubmatch(sym); m != nil {
		line, _ := strconv.Atoi(m[2])
		col, _ := strconv.Atoi(m[3])
		if line < 1 || col < 1 {
			return place{err: fmt.Errorf("%q: lines and columns count from 1", sym)}
		}
		return place{path: p.abs(m[1]), line: line, col: col}
	}
	return place{err: fmt.Errorf("%q is no symbol: want /lib/path::NAME or FILE:LINE:COL", sym)}
}

func cutLast(s, sep string) (before, after string, ok bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

// anchor is where the project is discovered from: the file, else the
// working directory.
func (pl place) anchor() []string {
	if pl.path != "" {
		return []string{pl.path}
	}
	return nil
}

// locate returns the file and the byte offset of the symbol.
func (pl place) locate(l *lint.Linter) (string, uint32, error) {
	if pl.err != nil {
		return "", 0, pl.err
	}
	snap := l.Index().Snapshot()
	if pl.path != "" {
		src, err := snap.ReadFile(pl.path)
		if err != nil {
			return "", 0, err
		}
		off, ok := offsetOf(src, pl.line, pl.col)
		if !ok {
			return "", 0, fmt.Errorf("%s has no line %d, column %d", pl.path, pl.line, pl.col)
		}
		return pl.path, off, nil
	}
	if strings.HasSuffix(pl.lib, ".h") {
		return pl.macro(l, pl.lib)
	}
	lp := strings.TrimSuffix(pl.lib, ".c")
	if !snap.ObjectExists(lp) {
		return "", 0, fmt.Errorf("%s: no such object in the mudlib", pl.lib)
	}
	fn, isFn := snap.ChainFunc(lp, pl.name)
	if isFn && fn.Private && fn.DefinedIn != lp {
		isFn = false
	}
	v, isVar := snap.ChainVar(lp, pl.name)
	if isVar && v.Private && v.DefinedIn != lp {
		isVar = false
	}
	var loc index.Loc
	switch {
	case isFn && isVar:
		return "", 0, fmt.Errorf("%s has a function and a global variable %s: name the one meant as FILE:LINE:COL", lp, pl.name)
	case isFn:
		loc = fn.Loc
	case isVar:
		loc = v.Loc
	default:
		return pl.macro(l, lp+".c")
	}
	if _, file, ok := profile.SplitLocation(loc.Path); ok {
		return "", 0, &rename.Refusal{Reason: fmt.Sprintf("'%s' is declared in %s in the mudlib profile, not in your checkout", pl.name, file)}
	}
	return loc.Path, loc.Off, nil
}

// macro locates the #define of the name in the lib file.
func (pl place) macro(l *lint.Linter, libFile string) (string, uint32, error) {
	path, ok := l.Project().Tree().Path(libFile)
	if !ok {
		return "", 0, fmt.Errorf("%s is not a file of this checkout", libFile)
	}
	parsed, err := l.Index().Snapshot().Parse(path)
	if err != nil {
		return "", 0, err
	}
	if parsed.Env != nil && parsed.Env.Own() != nil {
		if m := parsed.Env.Own().Macros[pl.name]; m != nil {
			return path, m.NameOff, nil
		}
	}
	if strings.HasSuffix(libFile, ".h") {
		return "", 0, fmt.Errorf("%s has no #define of %s", libFile, pl.name)
	}
	return "", 0, fmt.Errorf("%s has no function, global variable or macro %s", strings.TrimSuffix(libFile, ".c"), pl.name)
}

// offsetOf returns the byte offset of a line and a column, both from 1,
// the column counting bytes; a column past the end of its line is not
// there.
func offsetOf(src []byte, line, col int) (uint32, bool) {
	start := 0
	for n := 1; n < line; n++ {
		i := slices.Index(src[start:], '\n')
		if i < 0 {
			return 0, false
		}
		start += i + 1
	}
	end := len(src)
	if i := slices.Index(src[start:], '\n'); i >= 0 {
		end = start + i
	}
	if start+col-1 > end {
		return 0, false
	}
	return uint32(start + col - 1), true
}

// renamer is one run of `dgdtools rename` after the plan.
type renamer struct {
	process
	l              *lint.Linter
	plan           *rename.Plan
	possible       bool
	moved          bool // the files of a plan of moved files were moved
	checkouts      []string
	stdout, stderr io.Writer
}

// changed is a file the rename changes.
type changed struct {
	path, name, owner string
	src, out          []byte
	edits             int
}

// edits returns the edits made: the certain ones, and the possible ones
// when asked for.
func (r *renamer) edits() []rename.Edit {
	es := slices.Clone(r.plan.Edits)
	if r.possible {
		es = append(es, r.plan.PossibleEdits...)
	}
	return es
}

// files returns the files changed, with their texts before and after,
// by owner and then path.
func (r *renamer) files() ([]changed, error) {
	byPath := map[string][]rename.Edit{}
	for _, e := range r.edits() {
		byPath[e.Path] = append(byPath[e.Path], e)
	}
	snap := r.l.Index().Snapshot()
	var out []changed
	for _, path := range slices.Sorted(maps.Keys(byPath)) {
		src, err := snap.ReadFile(path)
		if err != nil {
			return nil, err
		}
		es := byPath[path]
		slices.SortFunc(es, func(a, b rename.Edit) int { return cmp.Compare(a.Start, b.Start) })
		var b []byte
		last := uint32(0)
		for _, e := range es {
			if e.Start < last || int(e.End) > len(src) {
				return nil, fmt.Errorf("%s: the edits overlap (please report)", path)
			}
			b = append(append(b, src[last:e.Start]...), e.NewText...)
			last = e.End
		}
		b = append(b, src[last:]...)
		out = append(out, changed{path: path, name: r.name(path), owner: r.owner(path), src: src, out: b, edits: len(es)})
	}
	slices.SortStableFunc(out, func(a, b changed) int { return compareOwners(a.owner, b.owner) })
	return out, nil
}

// name is how a file is shown: from the working directory when it is
// under it, else as it is.
func (r *renamer) name(path string) string {
	if rel, err := filepath.Rel(r.wd, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return path
}

// libName is a file's name in the mudlib, or its path.
func (r *renamer) libName(path string) string {
	return cmp.Or(r.l.Index().Snapshot().LibFile(path), path)
}

const otherOwner = "other"

// owner returns the owner of a file: the tree of the checkout patterns it
// is in ("players/harald"), else "other".
func (r *renamer) owner(path string) string {
	lf := r.l.Index().Snapshot().LibFile(path)
	if lf == "" {
		return otherOwner
	}
	segs := strings.Split(strings.Trim(lf, "/"), "/")
	for _, pat := range r.checkouts {
		want := strings.Split(strings.Trim(pat, "/"), "/")
		if len(segs) <= len(want) {
			continue
		}
		ok := true
		for i, w := range want {
			if ok, _ = filepath.Match(w, segs[i]); !ok {
				break
			}
		}
		if ok {
			return strings.Join(segs[:len(want)], "/")
		}
	}
	return otherOwner
}

// compareOwners orders the owners by name, "other" last.
func compareOwners(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == otherOwner:
		return 1
	case b == otherOwner:
		return -1
	}
	return strings.Compare(a, b)
}

// diffs prints the unified diffs of the files, each owner's under a line
// that names it.
func (r *renamer) diffs(files []changed) error {
	owner := ""
	for i, f := range files {
		if i == 0 || f.owner != owner {
			owner = f.owner
			n := 0
			for _, g := range files {
				if g.owner == owner {
					n++
				}
			}
			fmt.Fprintf(r.stdout, "# %s: %s\n", owner, plural(n, "file"))
		}
		text, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A: diffLines(f.src), FromFile: f.name,
			B: diffLines(f.out), ToFile: f.name,
			Context: 3,
		})
		if err != nil {
			return err
		}
		io.WriteString(r.stdout, text)
	}
	return nil
}

func plural(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return strconv.Itoa(n) + " " + thing + "s"
}

// what names a kind of symbol.
func what(k resolve.Kind) string {
	switch k {
	case resolve.Variable:
		return "global variable"
	case resolve.Local:
		return "local variable"
	case resolve.Label:
		return "inherit label"
	}
	return k.String()
}

// summary says on standard error what the rename is: the symbol, the
// places by class, the files by owner, the possible places, the
// warnings, the calls whose function's name is made when they run.
func (r *renamer) summary(files []changed, written int) {
	plan, w := r.plan, r.stderr
	sym := plan.Symbol
	if plan.Moves != nil {
		fmt.Fprintf(w, "move %s to %s\n", sym.Name, plan.New)
	} else {
		fmt.Fprintf(w, "rename %s %s to %s (%s:%d)\n", what(sym.Kind), sym.Name, plan.New, r.libName(sym.Def.Path), r.line(sym.Def.Path, sym.Def.Start))
	}
	fmt.Fprintf(w, "  %d certain, %d possible, %d excluded; %s searched\n",
		plan.Count(rename.Certain), plan.Count(rename.Possible), plan.Count(rename.Excluded), plural(plan.Files, "file"))
	var owners []string
	byOwner := map[string][2]int{}
	for _, f := range files {
		if _, ok := byOwner[f.owner]; !ok {
			owners = append(owners, f.owner)
		}
		c := byOwner[f.owner]
		byOwner[f.owner] = [2]int{c[0] + 1, c[1] + f.edits}
	}
	for _, o := range owners {
		fmt.Fprintf(w, "  %-20s %s, %s\n", o, plural(byOwner[o][0], "file"), plural(byOwner[o][1], "edit"))
	}
	if n := plan.Count(rename.Possible); plan.Moves != nil && n > 0 {
		fmt.Fprintf(w, "  possible places not changed (a path made by a macro or an expression):\n")
		shown := 0
		for _, o := range plan.Occurrences {
			if o.Class != rename.Possible {
				continue
			}
			if shown == 20 {
				fmt.Fprintf(w, "    and %d more\n", n-shown)
				break
			}
			shown++
			fmt.Fprintf(w, "    %s:%d:%d: %s\n", r.name(o.Path), o.Line, o.Col, o.Reason)
		}
	}
	if n := len(plan.PossibleEdits); n > 0 {
		if r.possible {
			fmt.Fprintf(w, "  possible places renamed (--include-possible):\n")
		} else {
			fmt.Fprintf(w, "  possible places not renamed (--include-possible renames them):\n")
		}
		shown := 0
		for _, o := range plan.Occurrences {
			if o.Class != rename.Possible {
				continue
			}
			if shown == 20 {
				fmt.Fprintf(w, "    and %d more\n", n-shown)
				break
			}
			shown++
			fmt.Fprintf(w, "    %s:%d:%d: %s\n", r.name(o.Path), o.Line, o.Col, o.Reason)
		}
	}
	for _, warn := range plan.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warn)
	}
	matches, others := 0, 0
	for _, d := range plan.Dynamic {
		if d.Matches {
			if matches == 0 {
				fmt.Fprintf(w, "  calls that make the name of the function they call when they run, and may call it:\n")
			}
			matches++
			if matches <= 20 {
				fmt.Fprintf(w, "    %s:%d:%d: %s\n", r.name(d.Path), d.Line, d.Col, d.Pattern)
			}
		} else {
			others++
		}
	}
	if matches > 20 {
		fmt.Fprintf(w, "    and %d more\n", matches-20)
	}
	if others > 0 {
		fmt.Fprintf(w, "  %s in the mudlib make the name of the function they call when they run, and cannot be searched\n", plural(others, "other call"))
	}
	switch {
	case plan.Moves != nil && r.moved:
		fmt.Fprintf(w, "%s written, %s moved to %s\n", plural(written, "file"), sym.Name, plan.New)
	case plan.Moves != nil:
		fmt.Fprintf(w, "%s would change (--write writes them and moves %s)\n", plural(len(files), "file"), sym.Name)
	case written > 0:
		fmt.Fprintf(w, "%s written\n", plural(written, "file"))
	case len(files) > 0:
		fmt.Fprintf(w, "%s would change (--write writes them)\n", plural(len(files), "file"))
	default:
		fmt.Fprintln(w, "nothing to change")
	}
}

// line returns the line of a byte of a file, 0 when it cannot be read.
func (r *renamer) line(path string, off uint32) int {
	src, err := r.l.Index().Snapshot().ReadFile(path)
	if err != nil || int(off) > len(src) {
		return 0
	}
	return 1 + strings.Count(string(src[:off]), "\n")
}

// The JSON of a plan.
type (
	jsonPlan struct {
		Symbol   jsonSymbol `json:"symbol"`
		New      string     `json:"new"`
		Complete bool       `json:"complete"`
		Written  bool       `json:"written"`
		Counts   struct {
			Certain  int `json:"certain"`
			Possible int `json:"possible"`
			Excluded int `json:"excluded"`
		} `json:"counts"`
		FilesSearched int              `json:"files_searched"`
		Partial       bool             `json:"partial"`
		Files         []jsonFile       `json:"files"`
		Occurrences   []jsonOccurrence `json:"occurrences"`
		Warnings      []string         `json:"warnings"`
		Dynamic       []jsonDynamic    `json:"dynamic"`
	}
	jsonSymbol struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
		Path string `json:"path"`
		Line int    `json:"line"`
	}
	jsonFile struct {
		Path  string `json:"path"`
		Owner string `json:"owner"`
		Edits int    `json:"edits"`
	}
	jsonOccurrence struct {
		Path   string `json:"path"`
		Line   uint32 `json:"line"`
		Col    uint32 `json:"col"`
		Class  string `json:"class"`
		Decl   bool   `json:"decl,omitempty"`
		Reason string `json:"reason,omitempty"`
	}
	jsonDynamic struct {
		Path    string `json:"path"`
		Line    uint32 `json:"line"`
		Col     uint32 `json:"col"`
		Pattern string `json:"pattern"`
		Matches bool   `json:"matches"`
	}
)

// json writes the plan as JSON.
func (r *renamer) json(files []changed, written bool) error {
	plan := r.plan
	out := jsonPlan{
		Symbol: jsonSymbol{Kind: plan.Symbol.Kind.String(), Name: plan.Symbol.Name,
			Path: r.libName(plan.Symbol.Def.Path), Line: r.line(plan.Symbol.Def.Path, plan.Symbol.Def.Start)},
		New: plan.New, Complete: len(plan.PossibleEdits) == 0 || r.possible, Written: written,
		FilesSearched: plan.Files, Partial: plan.Partial,
		Files: []jsonFile{}, Occurrences: []jsonOccurrence{}, Warnings: plan.Warnings, Dynamic: []jsonDynamic{},
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	out.Counts.Certain, out.Counts.Possible, out.Counts.Excluded = plan.Count(rename.Certain), plan.Count(rename.Possible), plan.Count(rename.Excluded)
	for _, f := range files {
		out.Files = append(out.Files, jsonFile{Path: f.name, Owner: f.owner, Edits: f.edits})
	}
	for _, o := range plan.Occurrences {
		out.Occurrences = append(out.Occurrences, jsonOccurrence{Path: r.name(o.Path), Line: o.Line, Col: o.Col,
			Class: o.Class.String(), Decl: o.Decl, Reason: o.Reason})
	}
	for _, d := range plan.Dynamic {
		out.Dynamic = append(out.Dynamic, jsonDynamic{Path: r.name(d.Path), Line: d.Line, Col: d.Col, Pattern: d.Pattern, Matches: d.Matches})
	}
	enc := json.NewEncoder(r.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
