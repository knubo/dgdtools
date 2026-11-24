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
	"fmt"
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
	// AnyBranch rules also report inside conditional branches whose
	// selection was a guess (the default for syntax and runtime rules is
	// to stay silent there).
	AnyBranch bool
	Run       func(*Pass)
}

// Pass is the per-file, per-rule context handed to Rule.Run.
type Pass struct {
	Ctx  context.Context
	File *token.File
	Tree *syntax.File
	// Env is the file's #define environment.
	Env  *pp.Env
	Unit *index.Unit
	// Index answers cross-file questions on the file's behalf.
	Index    *index.Query
	LibPath  string // "" when the file has no lib path
	Config   *config.Config
	Settings config.RuleSettings
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

// Report records a finding covering the byte range [start, end). The
// returned diagnostic may be extended with related locations and fixes.
func (p *Pass) Report(start, end uint32, format string, args ...any) *diag.Diagnostic {
	if end < start {
		end = start
	}
	if !p.rule.AnyBranch && (p.rule.Group == GroupSyntax || p.rule.Group == GroupRuntime) && p.guessed(start) {
		// Code in a conditional branch we could not decide: whether it
		// is compiled at all is unknown, so nothing is claimed about it.
		p.shared.stats.guessed++
		return &diag.Diagnostic{}
	}
	a, b := p.File.Pos(start), p.File.Pos(end)
	d := &diag.Diagnostic{
		Path: p.File.Path, Line: a.Line, Col: a.Col, EndLine: b.Line, EndCol: b.Col,
		Start: start, End: end,
		Severity: p.severity, Rule: p.rule.Name, Code: p.rule.Code,
		Message: fmt.Sprintf(format, args...),
	}
	*p.out = append(*p.out, d)
	return d
}

// guessed reports whether byte offset off lies in a conditional branch
// chosen by the first-branch rule.
func (p *Pass) guessed(off uint32) bool {
	toks := p.File.Tokens
	i := sort.Search(len(toks), func(i int) bool { return toks[i].End > off })
	return i < len(toks) && p.Tree.Sel.Guessed(i)
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
	// NoBaseline reports findings recorded in the baseline too.
	NoBaseline bool
	// MaxPerFile caps findings per rule per file (0 = each rule's default).
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
	suppressed, baselined, callSites, unknownReceivers int
	tainted, guessed                                   int
	unresolvedInherits, unknownIncludes, partial       int
}

func (st *Stats) add(f fileStats) {
	st.Files++
	st.Suppressed += f.suppressed
	st.Baselined += f.baselined
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
	// Skipped lists paths that could not be read.
	Skipped []string
	// Generation is the index generation the run read; see
	// Linter.Generation.
	Generation uint64
	// NeedsScan is set when Options.NoScan left questions unanswered.
	NeedsScan bool
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

	rules    []*Rule
	byKey    map[string][]*Rule // name, code, group, "all" -> rules
	override []layer            // the project's lint.overrides
	scopes   sync.Map           // *config.Scope -> *scopeLayers
	format   format.Options     // the project's part of the formatter's options
	registry *calls.Registry

	baselineOnce sync.Once
	baseline     *Baseline
	baselineErr  error
}

// layer is a set of rule settings and the files it is for.
type layer struct {
	// match selects the files: by lib-relative name for the project's
	// overrides, by name relative to the scope's directory for a
	// scope's. nil is every file.
	match *fileset.Matcher
	rules map[string]config.RuleSettings
}

// scopeLayers are the layers of one nested configuration file: its rules,
// then its overrides.
type scopeLayers struct {
	layers []layer
	err    error
}

// New creates a linter for the project. It fails on configuration that
// names unknown rules or severities.
func New(prj *config.Project, fsys vfs.FS) (*Linter, error) {
	l := &Linter{project: prj, fs: fsys, rules: Rules(), byKey: map[string][]*Rule{}}
	for _, r := range l.rules {
		l.byKey[r.Name] = []*Rule{r}
		l.byKey[strings.ToUpper(r.Code)] = []*Rule{r}
		l.byKey[r.Group] = append(l.byKey[r.Group], r)
		l.byKey["all"] = append(l.byKey["all"], r)
	}
	cfg := prj.Config
	l.format = format.OptionsFor(cfg, config.Format{})
	if err := l.checkRules("lint.rules", cfg.Lint.Rules); err != nil {
		return nil, err
	}
	if fo := cfg.Lint.FailOn; fo != "" {
		if _, ok := diag.ParseSeverity(strings.ToLower(fo)); !ok {
			return nil, fmt.Errorf("lint.fail_on: unknown severity %q", fo)
		}
	}
	for _, o := range cfg.Lint.Overrides {
		if err := l.checkRules("lint.overrides", o.Rules); err != nil {
			return nil, err
		}
		l.override = append(l.override, layer{fileset.NewMatcher(o.Paths), o.Rules})
	}
	l.registry = calls.NewRegistry(cfg.Conventions)
	l.index = index.Open(prj, fsys)
	return l, nil
}

// checkRules reports a rule setting that names an unknown rule or
// severity.
func (l *Linter) checkRules(where string, m map[string]config.RuleSettings) error {
	for name, rs := range m {
		if l.lookup(strings.TrimSpace(name)) == nil {
			return fmt.Errorf("%s: unknown rule %q (see `dgdtools rules`)", where, name)
		}
		if rs.Severity != "" && rs.Severity != "off" {
			if _, ok := diag.ParseSeverity(strings.ToLower(rs.Severity)); !ok {
				return fmt.Errorf("%s: rule %q: unknown severity %q", where, name, rs.Severity)
			}
		}
	}
	return nil
}

// layersOf returns the layers of a nested configuration file, checked
// once.
func (l *Linter) layersOf(sc *config.Scope) ([]layer, error) {
	if v, ok := l.scopes.Load(sc); ok {
		sl := v.(*scopeLayers)
		return sl.layers, sl.err
	}
	sl := &scopeLayers{}
	if sl.err = l.checkRules(sc.Path+": lint.rules", sc.Rules); sl.err == nil {
		sl.layers = append(sl.layers, layer{nil, sc.Rules})
		for _, o := range sc.Overrides {
			if sl.err = l.checkRules(sc.Path+": lint.overrides", o.Rules); sl.err != nil {
				break
			}
			sl.layers = append(sl.layers, layer{fileset.NewMatcher(o.Paths), o.Rules})
		}
	}
	v, _ := l.scopes.LoadOrStore(sc, sl)
	sl = v.(*scopeLayers)
	return sl.layers, sl.err
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

// active is the resolved state of one rule for one file.
type active struct {
	on       bool
	severity diag.Severity
	settings config.RuleSettings
}

// resolve computes which rules run on a file and with what severity. rel
// is the file's lib-relative name ("" when it is outside the lib) and
// set its settings (nil: the project's own).
func (l *Linter) resolve(file, rel string, set *config.Settings, req *Options) (map[*Rule]*active, error) {
	st := make(map[*Rule]*active, len(l.rules))
	for _, r := range l.rules {
		st[r] = &active{on: r.Default, severity: r.Severity}
	}
	apply := func(m map[string]config.RuleSettings) {
		// Least specific first: all, groups, then individual rules.
		for pass := 0; pass < 3; pass++ {
			for key, rs := range m {
				rules := l.lookup(key)
				specificity := 2
				if key == "all" {
					specificity = 0
				} else if len(rules) > 0 && rules[0].Group == key {
					specificity = 1
				}
				if specificity != pass {
					continue
				}
				for _, r := range rules {
					a := st[r]
					switch sev := strings.ToLower(rs.Severity); sev {
					case "":
					case "off":
						a.on = false
					default:
						if s, ok := diag.ParseSeverity(sev); ok {
							a.on, a.severity = true, s
						}
					}
					if specificity == 2 {
						if len(rs.Deny) > 0 {
							a.settings.Deny = rs.Deny
						}
						if len(rs.Names) > 0 {
							a.settings.Names = rs.Names
						}
						if rs.MaxPerFile > 0 {
							a.settings.MaxPerFile = rs.MaxPerFile
						}
					}
				}
			}
		}
	}
	turn := func(names []string, on bool) error {
		for _, n := range names {
			if n = strings.TrimSpace(n); n == "" {
				continue
			}
			rules := l.lookup(n)
			if rules == nil {
				return fmt.Errorf("unknown rule %q (see `dgdtools rules`)", n)
			}
			for _, r := range rules {
				st[r].on = on
			}
		}
		return nil
	}
	apply(l.project.Config.Lint.Rules)
	// Command line: --select names the candidate set, --enable adds to
	// it. What is said about a path (the project's overrides, then each
	// nested configuration file from the outermost in) is applied after,
	// so a path that turns a rule off keeps it off; --ignore has the last
	// word.
	selected := map[*Rule]bool{}
	for _, names := range [][]string{req.Select, req.Enable} {
		if err := turn(names, true); err != nil {
			return nil, err
		}
		for _, n := range names {
			for _, r := range l.lookup(strings.TrimSpace(n)) {
				selected[r] = true
			}
		}
	}
	if rel != "" {
		for _, o := range l.override {
			if o.match.Match(rel) {
				apply(o.rules)
			}
		}
	}
	if set != nil {
		for _, sc := range set.Scopes {
			layers, err := l.layersOf(sc)
			if err != nil {
				return nil, err
			}
			in, _ := filepath.Rel(sc.Dir, file)
			for _, o := range layers {
				if o.match == nil || o.match.Match(filepath.ToSlash(in)) {
					apply(o.rules)
				}
			}
		}
	}
	if len(req.Select) > 0 {
		for r, a := range st {
			if !selected[r] {
				a.on = false
			}
		}
	}
	if err := turn(req.Disable, false); err != nil {
		return nil, err
	}
	return st, nil
}

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
	// Validate rule names and the baseline once, up front.
	if _, err := l.resolve("", "", nil, &req.Options); err != nil {
		return nil, err
	}
	if !req.NoBaseline {
		if _, err := l.Baseline(); err != nil {
			return nil, err
		}
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
	// A nested configuration file that is not valid fails the run: its
	// files would otherwise be linted by settings nobody chose.
	var failed atomic.Pointer[error]
	lintInto := func(i int, opt *Options) {
		d, err := l.lintFile(ctx, s, files[i], opt)
		switch {
		case err != nil:
			failed.CompareAndSwap(nil, &err)
		case d != nil:
			docs[i] = d.release()
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

	for i, d := range docs {
		if d == nil {
			if ctx.Err() == nil {
				res.Skipped = append(res.Skipped, files[i])
			}
			continue
		}
		res.Diagnostics = append(res.Diagnostics, d.Diagnostics...)
		res.Stats.add(d.stats)
	}
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
	sort.Strings(res.Skipped)
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
// fails when the file cannot be read. Cancelling ctx stops the lint
// early; what was found until then is returned with ctx.Err().
func (l *Linter) Lint(ctx context.Context, path string, opt Options) (*Document, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%s: not an absolute path", path)
	}
	if _, err := l.resolve("", "", nil, &opt); err != nil {
		return nil, err
	}
	if !opt.NoBaseline {
		if _, err := l.Baseline(); err != nil {
			return nil, err
		}
	}
	d, err := l.lintFile(ctx, l.index.Snapshot(), filepath.Clean(path), &opt)
	switch {
	case err != nil:
		return nil, err
	case d == nil:
		return nil, fmt.Errorf("%s: cannot be read", path)
	}
	return d, ctx.Err()
}

// lintFile lints one file against s, with the settings of that file; a
// nil document when the file cannot be read. It fails when the file's
// settings cannot be told: a configuration file above it is not valid.
func (l *Linter) lintFile(ctx context.Context, s *index.Snapshot, file string, opt *Options) (*Document, error) {
	parsed := s.Parse(file)
	if parsed == nil {
		return nil, nil
	}
	set, err := l.project.Settings(file)
	if err != nil {
		return nil, err
	}
	u := parsed.Unit
	doc := &Document{
		Path: file, Generation: s.Generation(),
		File: parsed.Tok, Tree: parsed.Tree, Env: parsed.Env, Unit: u,
	}
	rel := strings.TrimPrefix(u.LibPath, "/") // the file's lib-relative name
	if u.LibPath != "" && path.Ext(rel) == "" {
		rel += ".c"
	}
	st, err := l.resolve(file, rel, set, opt)
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

	local := opt.Local || !l.project.HasLib() || u.LibPath == ""
	sup := scanSuppressions(parsed.Tok, l)
	var found []*diag.Diagnostic
	var unusedSup *active
	for _, r := range l.rules {
		a := st[r]
		if !a.on {
			continue
		}
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
		var out []*diag.Diagnostic
		pass := &Pass{
			Ctx: ctx, File: parsed.Tok, Tree: parsed.Tree, Env: parsed.Env,
			Unit: u, Index: query, LibPath: u.LibPath,
			Config: l.project.Config, Settings: a.settings, Format: l.format.With(set.Format),
			shared: shared, registry: l.registry, rule: r, severity: a.severity, out: &out,
		}
		r.Run(pass)
		limit := opt.MaxPerFile
		if limit == 0 {
			limit = a.settings.MaxPerFile
		}
		if limit > 0 && len(out) > limit {
			out = out[:limit]
		}
		for _, d := range out {
			if d.Rule != "" { // dropped findings are zero values
				found = append(found, d)
			}
		}
	}

	ds := make([]diag.Diagnostic, 0, len(found))
	for _, d := range found {
		if sup.suppressed(d.Rule, d.Line) {
			stats.suppressed++
			continue
		}
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
	if bl, _ := l.Baseline(); bl != nil && !opt.NoBaseline {
		ds, stats.baselined = bl.Filter(ds)
	}
	doc.Diagnostics, doc.NeedsScan, doc.stats = ds, query.NeedsScan(), *stats
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
	st, err := l.resolve(path, rel, set, &Options{})
	if err != nil {
		return nil, err
	}
	var out []*Rule
	for _, r := range l.rules {
		if a := st[r]; a.on {
			c := *r
			c.Severity = a.severity
			out = append(out, &c)
		}
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

// Threshold returns the fail-on severity from config (default error).
func Threshold(cfg *config.Config) diag.Severity {
	if s, ok := diag.ParseSeverity(strings.ToLower(cfg.Lint.FailOn)); ok {
		return s
	}
	return diag.Error
}
