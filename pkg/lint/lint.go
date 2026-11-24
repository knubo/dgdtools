// Package lint is the linter as a library: give it a project and a file
// system, ask it to lint, get diagnostics back.
//
//	fs := vfs.NewOverlay(vfs.OS{})
//	prj, err := config.Discover(fs, paths, config.DiscoverOptions{WorkDir: cwd})
//	l, err := lint.New(prj, fs)
//	res, err := l.Run(ctx, lint.Request{Paths: paths})   // many files
//	doc, err := l.Lint(ctx, path, lint.Options{})        // one document
//
// The package never prints, never exits, reads no environment or working
// directory (paths are absolute), and keeps no package-level mutable
// state. A Linter can live as long as the process (an editor server): it
// is told about changed files with Invalidate, which starts a new
// generation of its index, and every Run or Lint reads one generation
// from start to end, so linting and editing never wait for each other.
// Fixes are returned as text edits on diagnostics and are never applied
// here.
package lint

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/mmcdole/dgdtools/internal/par"
	"github.com/mmcdole/dgdtools/pkg/calls"
	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/fileset"
	"github.com/mmcdole/dgdtools/pkg/format"
	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/syntax"
	"github.com/mmcdole/dgdtools/pkg/token"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// Rule is one lint rule. Name and Code are stable identifiers: either may
// be used in configuration, on the command line and in suppression
// comments.
type Rule struct {
	Code  string // "E001"
	Name  string // "syntax-error"
	Group string // syntax | runtime | suspicious | convention
	// Doc is a one-line summary; Explain is the long form shown by
	// `dgdtools explain`.
	Doc     string
	Explain string
	Default bool
	// Severity is the default severity.
	Severity diag.Severity
	// CrossFile rules need the lib index and do not run in file-local
	// mode.
	CrossFile bool
	// CompiledOnly says the rule's findings are claims about code the
	// driver compiles: this does not compile, this fails when it runs.
	// In a conditional branch that was chosen by guess, where it is not
	// known whether the code is compiled at all, they are withheld. A
	// rule about the text as it stands says false.
	CompiledOnly bool
	// Tags are put on every finding of the rule.
	Tags []diag.Tag
	// Options returns the rule's options at their defaults: a pointer to
	// a struct whose fields carry the option's key as yaml tag and what
	// it is for as doc tag. Those are the keys the rule's mapping in
	// lint.rules may have besides severity and max_per_file; a rule
	// without Options has none. Run finds the file's options in
	// Pass.Options.
	Options func() any
	Run     func(*Pass)
}

// DocsURL is where the rules are documented, one section each.
const DocsURL = "https://github.com/mmcdole/dgdtools/blob/main/docs/rules.md"

// URL returns the address of the rule's documentation.
func (r *Rule) URL() string { return DocsURL + "#" + r.Name }

// Pass is the per-file, per-rule context handed to Rule.Run.
type Pass struct {
	Ctx  context.Context
	File *token.File
	Tree *syntax.File
	// Env is the file's #define environment.
	Env  *pp.Env
	Unit *index.Unit
	// Index answers cross-file questions on the file's behalf.
	Index   *index.Query
	LibPath string // "" when the file has no lib path
	Config  *config.Config
	// Options are the rule's options for this file: what Rule.Options
	// returned, with what the configuration says set.
	Options any
	// Format are the formatter's options for this file.
	Format format.Options

	shared   *fileShared
	registry *calls.Registry
	rule     *Rule
	severity diag.Severity
	out      *[]*diag.Diagnostic
}

// fileShared caches per-file derived facts across rules. One file is
// linted by one goroutine.
type fileShared struct {
	calls *callFacts
	stats fileStats
}

// Report records a finding covering the byte range [start, end) and
// returns it, for the rule to add related locations and fixes to.
func (p *Pass) Report(start, end uint32, format string, args ...any) *diag.Diagnostic {
	if end < start {
		end = start
	}
	a, b := p.File.Pos(start), p.File.Pos(end)
	d := &diag.Diagnostic{
		Path: p.File.Path, Line: a.Line, Col: a.Col, EndLine: b.Line, EndCol: b.Col,
		Start: start, End: end,
		Severity: p.severity, Rule: p.rule.Name, Code: p.rule.Code, Tags: p.rule.Tags,
		Message: fmt.Sprintf(format, args...),
	}
	*p.out = append(*p.out, d)
	return d
}

// guessed reports whether byte offset off lies in a conditional branch
// chosen by the first-branch rule.
func guessed(tree *syntax.File, off uint32) bool {
	toks := tree.Tok.Tokens
	i := sort.Search(len(toks), func(i int) bool { return toks[i].End > off })
	return i < len(toks) && tree.Sel.Guessed(i)
}

// ReportNode records a finding covering a syntax node.
func (p *Pass) ReportNode(n syntax.Node, format string, args ...any) *diag.Diagnostic {
	return p.Report(p.Tree.Off(n), p.Tree.End(n), format, args...)
}

// Related builds a related location in the file being linted.
func (p *Pass) Related(start, end uint32, msg string) diag.Related {
	pos := p.File.Pos(start)
	return diag.Related{Path: p.File.Path, Line: pos.Line, Col: pos.Col, Start: start, End: end, Message: msg}
}

// Options select what a lint does. The zero value runs the configured
// rules, applies the baseline, and reads whatever it needs.
type Options struct {
	// Select, when non-empty, replaces the configured rule set: only the
	// named rules (names, codes or groups) run.
	Select []string
	// Enable and Disable adjust the configured set.
	Enable, Disable []string
	// Local restricts the lint to file-local rules; the index is not used.
	Local bool
	// Baseline is the baseline file to subtract, an absolute path,
	// instead of the project's (Linter.BaselinePath).
	Baseline string
	// NoBaseline reports findings recorded in the baseline too.
	NoBaseline bool
	// MaxPerFile reports at most that many findings of one rule in one
	// file, whatever the configuration says; 0 leaves it to the
	// configuration (max_per_file), and a negative number lifts every
	// limit. What a limit holds back is counted (Stats.Capped).
	MaxPerFile int
	// NoScan keeps the lint from starting the whole-lib scan, which the
	// first question about who inherits a file needs and which takes
	// seconds on a big lib. While the scan has not completed such
	// questions are unknown and nothing is concluded from them; NeedsScan
	// on the result says there were some. A server sets this, runs
	// Linter.Scan in the background, and lints again when it is done.
	NoScan bool
}

// Request describes one lint run.
type Request struct {
	// Paths are the files and directories to lint, as absolute paths.
	// Empty means the whole lib (or the whole checkout).
	Paths []string
	Options
}

// Stats summarizes a run, including what could not be verified.
type Stats struct {
	Files      int `json:"files"`
	Suppressed int `json:"suppressed"` // by dgdlint: comments
	Baselined  int `json:"baselined"`
	Capped     int `json:"capped"` // over a max_per_file limit; not reported

	// Unknowns: facts the linter could not establish and therefore drew
	// no conclusions from.
	UnresolvedInherits int `json:"unresolved_inherits"`
	UnknownIncludes    int `json:"unknown_includes"`
	PartialFiles       int `json:"partial_files"` // symbol table incomplete
	CallSites          int `json:"call_sites"`
	UnknownReceivers   int `json:"unknown_receivers"`
	TaintedSyntax      int `json:"tainted_syntax_errors"`   // possibly macro artifacts; not reported
	GuessedBranch      int `json:"guessed_branch_findings"` // in #if branches that could not be decided; not reported
}

// fileStats are one file's contribution to Stats.
type fileStats struct {
	suppressed, baselined, capped                int
	callSites, unknownReceivers                  int
	tainted, guessed                             int
	unresolvedInherits, unknownIncludes, partial int
}

func (st *Stats) add(f fileStats) {
	st.Files++
	st.Suppressed += f.suppressed
	st.Baselined += f.baselined
	st.Capped += f.capped
	st.CallSites += f.callSites
	st.UnknownReceivers += f.unknownReceivers
	st.TaintedSyntax += f.tainted
	st.GuessedBranch += f.guessed
	st.UnresolvedInherits += f.unresolvedInherits
	st.UnknownIncludes += f.unknownIncludes
	st.PartialFiles += f.partial
}

// Result is the outcome of a run.
type Result struct {
	Diagnostics []diag.Diagnostic
	Stats       Stats
	// Notes are remarks about the run as a whole (no lib, incomplete lib,
	// ...).
	Notes []diag.Note
	// Errors are the files that were not linted, and the files whose
	// analysis crashed while another was being linted, in path order.
	Errors []*FileError
	// Generation is the index generation the run read; see
	// Linter.Generation.
	Generation uint64
	// NeedsScan is set when Options.NoScan left questions unanswered.
	NeedsScan bool
}

// FileError says why a file was not linted.
type FileError struct {
	Path string
	Kind ErrorKind
	// Message says what happened, without the path.
	Message string
}

func (e *FileError) Error() string { return e.Path + ": " + e.Message }

// ErrorKind tells what went wrong with a file.
type ErrorKind uint8

const (
	// Unreadable: the file could not be read. The run is otherwise
	// good.
	Unreadable ErrorKind = iota
	// Internal: the tools crashed on the file. That is a bug in them;
	// what the run says about the rest of the lib may be missing
	// something.
	Internal
)

func (k ErrorKind) String() string {
	if k == Internal {
		return "internal"
	}
	return "unreadable"
}

// fileError makes the FileError of an error met while linting file.
func fileError(file string, err error) *FileError {
	var crash *index.InternalError
	if errors.As(err, &crash) {
		return &FileError{Path: crash.Path, Kind: Internal, Message: "internal error (please report): " + crash.Panic}
	}
	msg := err.Error()
	var pe *fs.PathError
	if errors.As(err, &pe) {
		msg = pe.Err.Error()
	}
	return &FileError{Path: file, Kind: Unreadable, Message: "cannot be read: " + msg}
}

// Document is one linted file: its diagnostics together with what they
// were computed from, so a caller that wants the tree (an editor server
// answering hover, symbols, semantic tokens) need not parse again.
type Document struct {
	Path string
	// Generation is the index generation the document was linted
	// against; see Linter.Generation.
	Generation uint64
	File       *token.File
	Tree       *syntax.File
	// Env is the file's #define environment: the macros visible in it.
	Env *pp.Env
	// Unit is the file's entry in the index.
	Unit        *index.Unit
	Diagnostics []diag.Diagnostic
	// NeedsScan is set when Options.NoScan left questions unanswered:
	// lint again after Linter.Scan.
	NeedsScan bool

	stats fileStats
}

// Linter lints one project. It is safe for concurrent use.
type Linter struct {
	project *config.Project
	fs      vfs.FS
	index   *index.Index

	rules     []*Rule
	byKey     map[string][]*Rule // name, code, group, "all" -> rules
	base      *layer             // the project's lint.rules
	overrides []*layer           // the project's lint.overrides
	scopes    sync.Map           // *config.Scope -> *scopeLayers
	layerIDs  atomic.Int32
	format    format.Options // the project's part of the formatter's options
	registry  *calls.Registry
	failOn    diag.Severity

	baselines sync.Map // path -> *baselineFile
}

// New creates a linter for the project. It fails on a configuration that
// is not valid: what Config.Validate reports, and rule settings that name
// unknown rules or options.
func New(prj *config.Project, fsys vfs.FS) (*Linter, error) {
	l := &Linter{project: prj, fs: fsys, rules: Rules(), byKey: map[string][]*Rule{}, failOn: diag.Error}
	for _, r := range l.rules {
		l.byKey[r.Name] = []*Rule{r}
		l.byKey[strings.ToUpper(r.Code)] = []*Rule{r}
		l.byKey[r.Group] = append(l.byKey[r.Group], r)
		l.byKey["all"] = append(l.byKey["all"], r)
	}
	cfg := prj.Config
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	l.format = format.OptionsFor(cfg, config.Format{})
	if cfg.Lint.FailOn != "" {
		l.failOn, _ = diag.ParseSeverity(cfg.Lint.FailOn)
	}
	var err error
	if l.base, err = l.newLayer("lint.rules", nil, cfg.Lint.Rules); err != nil {
		return nil, err
	}
	for i, o := range cfg.Lint.Overrides {
		lay, err := l.newLayer(fmt.Sprintf("lint.overrides[%d].rules", i), fileset.NewMatcher(o.Paths), o.Rules)
		if err != nil {
			return nil, err
		}
		l.overrides = append(l.overrides, lay)
	}
	l.registry = calls.NewRegistry(cfg.Conventions)
	l.index = index.Open(prj, fsys)
	return l, nil
}

// Project returns the project the linter was made for.
func (l *Linter) Project() *config.Project { return l.project }

// Index returns the linter's index of the lib.
func (l *Linter) Index() *index.Index { return l.index }

func (l *Linter) lookup(key string) []*Rule {
	if rs := l.byKey[key]; rs != nil {
		return rs
	}
	return l.byKey[strings.ToUpper(key)]
}

// Invalidate tells the linter that the files at the given absolute paths
// changed, were created or were deleted. It returns the files whose
// diagnostics may now differ; see index.Index.Invalidate for exactly
// which. Each call starts a new generation.
func (l *Linter) Invalidate(paths ...string) []string { return l.index.Invalidate(paths...) }

// Generation returns the current generation of the linter's index. A
// Result or Document carries the generation it was computed against: if
// that is no longer the current one, files changed while it was being
// computed, it describes the lib as it was before, and nothing watches
// the lib on its behalf (Invalidate reports dependents of the current
// generation's lints only). A server lints such a document again.
func (l *Linter) Generation() uint64 { return l.index.Generation() }

// Scan runs the whole-lib scan that questions about who inherits a file
// need, unless it is already complete. It reports false when the scan did
// not complete (ctx cancelled, files changed meanwhile, no lib); see
// Options.NoScan.
func (l *Linter) Scan(ctx context.Context) bool { return l.index.Snapshot().Scan(ctx) }

// Run lints the requested paths against one generation of the index.
// Cancelling ctx stops the run early; the partial result is returned with
// ctx.Err().
func (l *Linter) Run(ctx context.Context, req Request) (*Result, error) {
	prj := l.project
	s := l.index.Snapshot()
	res := &Result{Generation: s.Generation()}
	res.Notes = append(res.Notes, prj.Notes...)
	if !prj.HasLib() {
		res.Notes = append(res.Notes, diag.Note{Kind: diag.NoteNoLib,
			Message: "no dgdtools.yml and no --root: running file-local rules only (cross-file rules need a lib root)"})
	}
	// Rule names and the baseline are checked once, up front.
	sel, err := l.selection(&req.Options)
	if err != nil {
		return nil, err
	}
	if _, err := l.baselineFor(&req.Options); err != nil {
		return nil, err
	}

	paths := req.Paths
	if len(paths) == 0 {
		switch {
		case !prj.HasLib():
			return nil, fmt.Errorf("nothing to lint: give a path, or run inside a project with dgdtools.yml")
		case prj.Partial:
			paths = []string{prj.Base} // the checkout: the rest of the lib is not on disk
		default:
			paths = []string{prj.Root}
		}
	}
	var files []string
	seen := map[string]bool{}
	add := func(f string) {
		if !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("%s: not an absolute path", p)
		}
		p = filepath.Clean(p)
		switch {
		case !l.fs.Exists(p):
			return nil, fmt.Errorf("%s: no such file or directory", p)
		case l.fs.IsDir(p):
			list, err := s.Files(ctx, p)
			if err != nil {
				return res, err
			}
			for _, f := range list {
				add(f)
			}
		default:
			add(p) // a file named explicitly is linted even if excluded
		}
	}
	if place, what := prj.Root, "lib root"; prj.HasLib() && (place != "" || prj.CheckoutLib != "") {
		if place == "" {
			place, what = prj.CheckoutDir, "checkout"
		}
		outside := 0
		for _, f := range files {
			if l.index.LibPath(f) == "" {
				outside++
			}
		}
		if outside > 0 {
			res.Notes = append(res.Notes, diag.Note{Kind: diag.NoteOutsideLib, Path: place, Message: fmt.Sprintf(
				"%d file(s) are outside the %s %s: file-local rules only for those", outside, what, place)})
		}
	}

	// A question about who inherits a file needs every file of the lib
	// indexed. A run over the lib is about to index every file anyway, so
	// the first pass declines those questions instead of starting the
	// scan, and only the files that had one are linted again once the
	// scan is complete: each file is parsed once, a few of them twice.
	docs := make([]*Document, len(files))
	order := make([]int, len(files))
	for i := range order {
		order[i] = i
	}
	// A file that cannot be linted is the file's error, and the run goes
	// on. A nested configuration file that is not valid fails the run:
	// its files would otherwise be linted by settings nobody chose.
	errs := make([]*FileError, len(files))
	var failed atomic.Pointer[error]
	lintInto := func(i int, opt *Options) {
		d, err := l.lintFile(ctx, s, files[i], sel, opt)
		var fe *FileError
		switch {
		case errors.As(err, &fe):
			docs[i], errs[i] = nil, fe
		case err != nil:
			failed.CompareAndSwap(nil, &err)
		default:
			docs[i], errs[i] = d.release(), nil
		}
	}
	first := req.Options
	first.NoScan = true
	par.Each(ctx, order, func(i int) { lintInto(i, &first) })
	var again []int
	for i, d := range docs {
		if d != nil && d.NeedsScan {
			again = append(again, i)
		}
	}
	if len(again) > 0 && !req.NoScan && s.Scan(ctx) {
		par.Each(ctx, again, func(i int) { lintInto(i, &req.Options) })
		again = nil
	}
	if err := failed.Load(); err != nil {
		return nil, *err
	}
	res.NeedsScan = len(again) > 0

	reported := map[string]bool{}
	for i, d := range docs {
		if e := errs[i]; e != nil {
			res.Errors = append(res.Errors, e)
			reported[e.Path] = true
		}
		if d != nil {
			res.Diagnostics = append(res.Diagnostics, d.Diagnostics...)
			res.Stats.add(d.stats)
		}
	}
	// A file that crashed while it was read on another's behalf (a
	// parent, a call target) made that other's answers unknown.
	for _, crash := range s.Crashed() {
		if !reported[crash.Path] {
			res.Errors = append(res.Errors, fileError(crash.Path, crash))
		}
	}
	sort.SliceStable(res.Errors, func(i, j int) bool { return res.Errors[i].Path < res.Errors[j].Path })
	sort.SliceStable(res.Diagnostics, func(i, j int) bool {
		a, b := res.Diagnostics[i], res.Diagnostics[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		return a.Rule < b.Rule
	})
	if prj.HasLib() && !req.Local {
		res.Notes = append(res.Notes, s.Notes()...)
	}
	return res, ctx.Err()
}

// release drops what a run over many files must not keep in memory.
func (d *Document) release() *Document {
	d.File, d.Tree, d.Env = nil, nil, nil
	return d
}

// Lint lints the file at path (absolute) against the current generation
// of the index and returns its diagnostics together with its tree. It
// fails with a *FileError when the file cannot be read or the tools crash
// on it. Cancelling ctx stops the lint early; what was found until then
// is returned with ctx.Err().
func (l *Linter) Lint(ctx context.Context, path string, opt Options) (*Document, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%s: not an absolute path", path)
	}
	sel, err := l.selection(&opt)
	if err != nil {
		return nil, err
	}
	if _, err := l.baselineFor(&opt); err != nil {
		return nil, err
	}
	d, err := l.lintFile(ctx, l.index.Snapshot(), filepath.Clean(path), sel, &opt)
	if err != nil {
		return nil, err
	}
	return d, ctx.Err()
}

// lintFile lints one file against s, with the settings of that file. It
// fails with a *FileError when the file itself cannot be linted, and with
// another error when the file's settings cannot be told: a configuration
// file above it is not valid.
func (l *Linter) lintFile(ctx context.Context, s *index.Snapshot, file string, sel *selection, opt *Options) (doc *Document, err error) {
	// A bug met on one file must not take down a run over a hundred
	// thousand.
	running := ""
	defer func() {
		if r := recover(); r != nil {
			where := ""
			if running != "" {
				where = " in rule " + running
			}
			doc, err = nil, &FileError{Path: file, Kind: Internal, Message: fmt.Sprintf("internal error%s (please report): %v", where, r)}
		}
	}()
	parsed, err := s.Parse(file)
	if err != nil {
		return nil, fileError(file, err)
	}
	set, err := l.project.Settings(file)
	if err != nil {
		return nil, err
	}
	u := parsed.Unit
	doc = &Document{
		Path: file, Generation: s.Generation(),
		File: parsed.Tok, Tree: parsed.Tree, Env: parsed.Env, Unit: u,
	}
	rel := strings.TrimPrefix(u.LibPath, "/") // the file's lib-relative name
	if u.LibPath != "" && path.Ext(rel) == "" {
		rel += ".c"
	}
	rules, err := sel.rules(file, rel, set)
	if err != nil {
		return nil, err
	}
	query := s.Query(u, opt.NoScan)
	shared := &fileShared{}
	stats := &shared.stats
	if u.Partial {
		stats.partial++
	}
	stats.unknownIncludes += u.UnknownIncludes
	for _, r := range u.Inherits {
		if !r.Resolved {
			stats.unresolvedInherits++
		}
	}
	for _, e := range parsed.Tree.Errors {
		if e.Tainted {
			stats.tainted++
		}
	}

	// What a rule finds is not yet what is reported. In this order: a
	// finding about code that may not be compiled is withheld; one that a
	// comment suppresses is dropped; the rest is what the file has. From
	// that the baseline is subtracted, and only then is a rule's limit
	// per file applied, so that a limit counts what would be shown.
	local := opt.Local || !l.project.HasLib() || u.LibPath == ""
	sup := scanSuppressions(parsed.Tok, l)
	var found []*diag.Diagnostic
	var unusedSup *active
	limits := map[string]int{}
	for i := range rules {
		a, r := &rules[i], rules[i].rule
		if r.Name == "unused-suppression" {
			unusedSup = a
			continue
		}
		if r.CrossFile && local {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		if limits[r.Name] = opt.MaxPerFile; opt.MaxPerFile == 0 {
			limits[r.Name] = a.maxPerFile
		}
		var out []*diag.Diagnostic
		pass := &Pass{
			Ctx: ctx, File: parsed.Tok, Tree: parsed.Tree, Env: parsed.Env,
			Unit: u, Index: query, LibPath: u.LibPath,
			Config: l.project.Config, Options: a.options, Format: l.format.With(set.Format),
			shared: shared, registry: l.registry, rule: r, severity: a.severity, out: &out,
		}
		running = r.Name
		r.Run(pass)
		running = ""
		for _, d := range out {
			switch {
			case r.CompiledOnly && guessed(parsed.Tree, d.Start):
				stats.guessed++
			case sup.suppressed(d.Rule, d.Line):
				stats.suppressed++
			default:
				found = append(found, d)
			}
		}
	}

	ds := make([]diag.Diagnostic, 0, len(found))
	for _, d := range found {
		ds = append(ds, *d)
	}
	if unusedSup != nil {
		r := l.byKey["unused-suppression"][0]
		for _, s := range sup.unused() {
			pos := parsed.Tok.Pos(s.off)
			ds = append(ds, diag.Diagnostic{
				Path: file, Line: pos.Line, Col: pos.Col, EndLine: pos.Line, EndCol: pos.Col,
				Start: s.off, End: s.end, Severity: unusedSup.severity,
				Rule: r.Name, Code: r.Code,
				Message: "suppression comment does not suppress anything (" + s.what + ")",
			})
		}
	}
	for i := range ds {
		ds[i].Fingerprint = lineFingerprint(parsed.Tok.Src, ds[i].Start)
	}
	sort.SliceStable(ds, func(i, j int) bool {
		if ds[i].Start != ds[j].Start {
			return ds[i].Start < ds[j].Start
		}
		return ds[i].Rule < ds[j].Rule
	})
	if bl, _ := l.baselineFor(opt); bl != nil {
		ds, stats.baselined = bl.Filter(ds)
	}
	shown, kept := map[string]int{}, ds[:0]
	for _, d := range ds {
		if limit := limits[d.Rule]; limit > 0 && shown[d.Rule] == limit {
			stats.capped++
			continue
		}
		shown[d.Rule]++
		kept = append(kept, d)
	}
	doc.Diagnostics, doc.NeedsScan, doc.stats = kept, query.NeedsScan(), *stats
	return doc, nil
}

// Rules returns the linter's rule table.
func (l *Linter) Rules() []*Rule { return l.rules }

// EffectiveRules returns the rules that would run on the file at path
// (absolute; "" for the project's own answer, before any path is
// considered), with their effective severity.
func (l *Linter) EffectiveRules(path string) ([]*Rule, error) {
	var set *config.Settings
	rel := ""
	if path != "" {
		var err error
		if set, err = l.project.Settings(path); err != nil {
			return nil, err
		}
		rel, _ = l.project.Tree().Rel(path)
	}
	sel, err := l.selection(&Options{})
	if err != nil {
		return nil, err
	}
	rules, err := sel.rules(path, rel, set)
	if err != nil {
		return nil, err
	}
	var out []*Rule
	for _, a := range rules {
		c := *a.rule
		c.Severity = a.severity
		out = append(out, &c)
	}
	return out, nil
}

// RuleByName returns the rule with the given name or code, or nil.
func (l *Linter) RuleByName(key string) *Rule {
	rs := l.lookup(key)
	if len(rs) == 1 && (rs[0].Name == key || strings.EqualFold(rs[0].Code, key)) {
		return rs[0]
	}
	return nil
}

// FailOn returns the severity at and above which the project's findings
// fail a run: lint.fail_on, error when the configuration does not say.
func (l *Linter) FailOn() diag.Severity { return l.failOn }
