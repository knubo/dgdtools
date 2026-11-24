// Package lint is dgdlint as a library: give it a project and a file
// system, ask it to lint paths, get diagnostics back.
//
//	fs := vfs.NewOverlay(vfs.OS{})
//	prj, err := config.Discover(fs, paths, config.DiscoverOptions{})
//	l, err := lint.New(prj, fs)
//	res, err := l.Run(ctx, lint.Request{Paths: paths})
//
// The package never prints, never exits, and keeps no package-level
// mutable state; a Linter can live as long as the process (an editor
// server) and be told about changed files with Invalidate. Fixes are
// returned as text edits on diagnostics and are never applied here.
package lint

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/fileset"
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
	// `dgdlint explain`.
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
	Ctx      context.Context
	File     *token.File
	Tree     *syntax.File
	View     *pp.View
	Unit     *index.Unit
	Index    *index.Index
	LibPath  string // "" when the file has no lib path
	Config   *config.Config
	Settings config.RuleSettings

	shared   *fileShared
	rule     *Rule
	severity diag.Severity
	out      *[]*diag.Diagnostic
}

// fileShared caches per-file derived facts across rules.
type fileShared struct {
	calls     *callFacts
	callsOnce sync.Once
	stats     *runStats
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
		p.shared.stats.guessed.Add(1)
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

// Request describes one lint run.
type Request struct {
	// Paths are files and directories to lint. Empty means the whole lib
	// root.
	Paths []string
	// Select, when non-empty, replaces the configured rule set: only the
	// named rules (names, codes or groups) run.
	Select []string
	// Enable and Disable adjust the configured set.
	Enable, Disable []string
	// Local restricts the run to file-local rules; the index is not used.
	Local bool
	// NoBaseline reports findings recorded in the baseline too.
	NoBaseline bool
	// MaxPerFile caps findings per rule per file (0 = each rule's default).
	MaxPerFile int
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

type runStats struct {
	suppressed, callSites, unknownReceivers, tainted atomic.Int64
	guessed                                          atomic.Int64
	unresolvedInherits, unknownIncludes, partial     atomic.Int64
}

// Result is the outcome of a run.
type Result struct {
	Diagnostics []diag.Diagnostic
	Stats       Stats
	// Notes are project-level remarks (no root, incomplete lib, ...).
	Notes []string
	// Skipped lists paths that could not be read.
	Skipped []string
}

// Linter lints one project. It is safe for concurrent use.
type Linter struct {
	Project *config.Project
	FS      vfs.FS
	Index   *index.Index

	rules    []*Rule
	byKey    map[string][]*Rule // name, code, group, "all" -> rules
	override []compiledOverride
}

type compiledOverride struct {
	match *fileset.Matcher
	rules map[string]config.RuleSettings
}

// New creates a linter for the project. It fails on configuration that
// names unknown rules or severities.
func New(prj *config.Project, fsys vfs.FS) (*Linter, error) {
	l := &Linter{Project: prj, FS: fsys, rules: Rules(), byKey: map[string][]*Rule{}}
	for _, r := range l.rules {
		l.byKey[r.Name] = []*Rule{r}
		l.byKey[strings.ToUpper(r.Code)] = []*Rule{r}
		l.byKey[r.Group] = append(l.byKey[r.Group], r)
		l.byKey["all"] = append(l.byKey["all"], r)
	}
	cfg := prj.Config
	check := func(where string, names ...string) error {
		for _, n := range names {
			if n = strings.TrimSpace(n); n != "" && l.lookup(n) == nil {
				return fmt.Errorf("%s: unknown rule %q (see `dgdlint rules`)", where, n)
			}
		}
		return nil
	}
	checkSettings := func(where string, m map[string]config.RuleSettings) error {
		for name, rs := range m {
			if err := check(where, name); err != nil {
				return err
			}
			if rs.Severity != "" && rs.Severity != "off" {
				if _, ok := diag.ParseSeverity(strings.ToLower(rs.Severity)); !ok {
					return fmt.Errorf("%s: rule %q: unknown severity %q", where, name, rs.Severity)
				}
			}
		}
		return nil
	}
	if err := checkSettings("lint.rules", cfg.Lint.Rules); err != nil {
		return nil, err
	}
	if err := check("lint.enable", cfg.Lint.Enable...); err != nil {
		return nil, err
	}
	if err := check("lint.disable", cfg.Lint.Disable...); err != nil {
		return nil, err
	}
	for _, o := range cfg.Lint.Overrides {
		if err := checkSettings("lint.overrides", o.Rules); err != nil {
			return nil, err
		}
		l.override = append(l.override, compiledOverride{fileset.NewMatcher(o.Paths), o.Rules})
	}
	for _, pr := range cfg.Lint.PathRules {
		if err := check("lint.path_rules", pr.Disable...); err != nil {
			return nil, err
		}
		m := map[string]config.RuleSettings{}
		for _, d := range pr.Disable {
			m[d] = config.RuleSettings{Severity: "off"}
		}
		l.override = append(l.override, compiledOverride{fileset.NewMatcher(pr.Paths), m})
	}
	l.Index = index.New(cfg, prj.Root, fsys)
	return l, nil
}

func (l *Linter) lookup(key string) []*Rule {
	if rs := l.byKey[key]; rs != nil {
		return rs
	}
	return l.byKey[strings.ToUpper(key)]
}

// Invalidate tells the linter that files changed. It returns the files
// whose diagnostics may now differ (the changed files and everything
// built from them).
func (l *Linter) Invalidate(paths ...string) []string { return l.Index.Invalidate(paths...) }

// active is the resolved state of one rule for one file.
type active struct {
	on       bool
	severity diag.Severity
	settings config.RuleSettings
}

// resolve computes which rules run on a file (rel is lib-relative, "" when
// the file is outside the root) and with what severity.
func (l *Linter) resolve(rel string, req *Request) (map[*Rule]*active, error) {
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
	set := func(names []string, on bool) error {
		for _, n := range names {
			if n = strings.TrimSpace(n); n == "" {
				continue
			}
			rules := l.lookup(n)
			if rules == nil {
				return fmt.Errorf("unknown rule %q (see `dgdlint rules`)", n)
			}
			for _, r := range rules {
				st[r].on = on
			}
		}
		return nil
	}
	cfg := l.Project.Config
	apply(cfg.Lint.Rules)
	if err := set(cfg.Lint.Enable, true); err != nil {
		return nil, err
	}
	if err := set(cfg.Lint.Disable, false); err != nil {
		return nil, err
	}
	// Command line: --select names the candidate set, --enable adds to
	// it. Per-path overrides are applied after, so a path that turns a
	// rule off keeps it off; --disable/--ignore has the last word.
	selected := map[*Rule]bool{}
	if len(req.Select) > 0 {
		if err := set(req.Select, true); err != nil {
			return nil, err
		}
		for _, n := range req.Select {
			for _, r := range l.lookup(strings.TrimSpace(n)) {
				selected[r] = true
			}
		}
	}
	if err := set(req.Enable, true); err != nil {
		return nil, err
	}
	for _, n := range req.Enable {
		for _, r := range l.lookup(strings.TrimSpace(n)) {
			selected[r] = true
		}
	}
	if rel != "" {
		for _, o := range l.override {
			if o.match.Match(rel) {
				apply(o.rules)
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
	if err := set(req.Disable, false); err != nil {
		return nil, err
	}
	return st, nil
}

// Run lints the requested paths. Cancelling ctx stops the run early; the
// partial result is returned with ctx.Err().
func (l *Linter) Run(ctx context.Context, req Request) (*Result, error) {
	res := &Result{}
	res.Notes = append(res.Notes, l.Project.Notes...)
	local := req.Local || l.Project.Root == ""
	if l.Project.Root == "" {
		res.Notes = append(res.Notes,
			"no dgdtools.yml and no --root: running file-local rules only (cross-file rules need a lib root)")
	} else if !req.Local {
		res.Notes = append(res.Notes, l.Index.Notes...)
	}
	// Validate rule names once, up front.
	if _, err := l.resolve("", &req); err != nil {
		return nil, err
	}

	paths := req.Paths
	if len(paths) == 0 {
		if l.Project.Root == "" {
			return nil, fmt.Errorf("nothing to lint: give a path, or run inside a project with dgdtools.yml")
		}
		paths = []string{l.Project.Root}
	}
	var files []string
	seen := map[string]bool{}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		if !l.FS.Exists(abs) {
			return nil, fmt.Errorf("%s: no such file or directory", p)
		}
		add := func(f string) {
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
		if l.FS.IsDir(abs) {
			if err := l.Index.Walk(abs, add); err != nil {
				return nil, err
			}
		} else {
			add(abs) // a file named explicitly is linted even if excluded
		}
	}
	if l.Project.Root != "" {
		outside := 0
		for _, f := range files {
			if l.Index.LibPath(f) == "" {
				outside++
			}
		}
		if outside > 0 {
			res.Notes = append(res.Notes, fmt.Sprintf(
				"%d file(s) are outside the lib root %s: file-local rules only for those", outside, l.Project.Root))
		}
	}

	rs := &runStats{}
	var mu sync.Mutex
	var all []diag.Diagnostic
	var skipped []string
	ch := make(chan string, 256)
	var wg sync.WaitGroup
	workers := runtime.NumCPU()
	if len(files) < workers {
		workers = len(files)
	}
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range ch {
				if ctx.Err() != nil {
					continue
				}
				ds, ok := l.lintFile(ctx, f, &req, local, rs)
				mu.Lock()
				if !ok {
					skipped = append(skipped, f)
				}
				all = append(all, ds...)
				mu.Unlock()
			}
		}()
	}
	for _, f := range files {
		ch <- f
	}
	close(ch)
	wg.Wait()

	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		return a.Rule < b.Rule
	})
	sort.Strings(skipped)
	res.Skipped = skipped
	res.Stats = Stats{
		Files:              len(files) - len(skipped),
		Suppressed:         int(rs.suppressed.Load()),
		CallSites:          int(rs.callSites.Load()),
		UnknownReceivers:   int(rs.unknownReceivers.Load()),
		TaintedSyntax:      int(rs.tainted.Load()),
		GuessedBranch:      int(rs.guessed.Load()),
		UnresolvedInherits: int(rs.unresolvedInherits.Load()),
		UnknownIncludes:    int(rs.unknownIncludes.Load()),
		PartialFiles:       int(rs.partial.Load()),
	}

	if !req.NoBaseline {
		if bl, err := l.LoadBaseline(); err != nil {
			return nil, err
		} else if bl != nil {
			all, res.Stats.Baselined = bl.Filter(l, all)
		}
	}
	res.Diagnostics = all
	return res, ctx.Err()
}

func (l *Linter) lintFile(ctx context.Context, fsPath string, req *Request, local bool, rs *runStats) ([]diag.Diagnostic, bool) {
	parsed := l.Index.Parse(fsPath)
	if parsed == nil {
		return nil, false
	}
	u := parsed.Unit
	rel := strings.TrimPrefix(u.LibPath, "/")
	if u.LibPath != "" {
		rel += ".c"
	}
	st, err := l.resolve(rel, req)
	if err != nil {
		return nil, true
	}
	if u.Partial {
		rs.partial.Add(1)
	}
	rs.unknownIncludes.Add(int64(u.UnknownIncludes))
	for _, r := range u.Inherits {
		if !r.Resolved {
			rs.unresolvedInherits.Add(1)
		}
	}
	for _, e := range parsed.Tree.Errors {
		if e.Tainted {
			rs.tainted.Add(1)
		}
	}

	sup := scanSuppressions(parsed.Tok, l)
	shared := &fileShared{stats: rs}
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
		if r.CrossFile && (local || u.LibPath == "") {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		var out []*diag.Diagnostic
		pass := &Pass{
			Ctx: ctx, File: parsed.Tok, Tree: parsed.Tree, View: parsed.View,
			Unit: u, Index: l.Index, LibPath: u.LibPath,
			Config: l.Project.Config, Settings: a.settings,
			shared: shared, rule: r, severity: a.severity, out: &out,
		}
		r.Run(pass)
		limit := req.MaxPerFile
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
			rs.suppressed.Add(1)
			continue
		}
		ds = append(ds, *d)
	}
	if unusedSup != nil {
		r := l.byKey["unused-suppression"][0]
		for _, s := range sup.unused() {
			pos := parsed.Tok.Pos(s.off)
			ds = append(ds, diag.Diagnostic{
				Path: fsPath, Line: pos.Line, Col: pos.Col, EndLine: pos.Line, EndCol: pos.Col,
				Start: s.off, End: s.end, Severity: unusedSup.severity,
				Rule: r.Name, Code: r.Code,
				Message: "suppression comment does not suppress anything (" + s.what + ")",
			})
		}
	}
	return ds, true
}

// Rules returns the linter's rule table.
func (l *Linter) Rules() []*Rule { return l.rules }

// EffectiveRules returns the rules that would run on a lib-relative path
// ("" for a path-independent answer), with their effective severity.
func (l *Linter) EffectiveRules(rel string) []*Rule {
	st, err := l.resolve(rel, &Request{})
	if err != nil {
		return nil
	}
	var out []*Rule
	for _, r := range l.rules {
		if a := st[r]; a.on {
			c := *r
			c.Severity = a.severity
			out = append(out, &c)
		}
	}
	return out
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
