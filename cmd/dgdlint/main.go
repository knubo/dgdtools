// dgdlint lints DGD LPC source.
//
// It is a thin frontend over pkg/lint (the embeddable linter) and
// pkg/report (the output writers); all analysis lives there.
//
// Exit codes: 0 no findings at or above the fail-on severity, 1 findings,
// 2 usage, configuration or I/O error.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/report"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `usage: dgdlint [flags] [path ...]     lint files and directories
       dgdlint rules                   list rules
       dgdlint explain RULE            describe one rule
       dgdlint config [path]           show the resolved project
       dgdlint profile build [-o FILE] capture the whole lib as a profile
       dgdlint profile show [NAME|FILE] [LIBPATH]
                                       summarize a profile, or print one stub

With no path, the whole project (the lib root of the nearest dgdtools.yml
at or above the working directory) is linted. The project for a path is
found by walking up from that path. Without a dgdtools.yml only
file-local rules run. A wizard with only their own folder puts
"profile: NAME" in a dgdtools.yml there (or passes --profile).

flags:
`

type options struct {
	configPath, root         string
	profile, libPath         string
	noConfig                 bool
	format, output           string
	sel, ignore, enable, dis string
	failOn                   string
	baseline                 string
	writeBaseline, noBase    bool
	local, stats, quiet      bool
	maxPerFile               int
	listRules, showVersion   bool
	exitZero                 bool
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var o options
	fs := flag.NewFlagSet("dgdlint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.configPath, "config", "", "use this `file` instead of searching for dgdtools.yml")
	fs.StringVar(&o.root, "root", "", "lib root `dir` (the driver's \"/\"); overrides the config")
	fs.BoolVar(&o.noConfig, "no-config", false, "do not look for a config file")
	fs.StringVar(&o.profile, "profile", "", "mudlib profile: a `name` (see `dgdlint profile show`) or a profile file")
	fs.StringVar(&o.libPath, "lib-path", "", "where this checkout sits in the lib, e.g. /players/you (a `path`)")
	fs.StringVar(&o.format, "format", "human", "output `format`: "+strings.Join(report.Formats, ", "))
	fs.StringVar(&o.format, "f", "human", "shorthand for --format")
	fs.StringVar(&o.output, "output", "", "write findings to `file` instead of stdout")
	fs.StringVar(&o.output, "o", "", "shorthand for --output")
	fs.StringVar(&o.sel, "select", "", "run only these `rules` (names, codes or groups, comma-separated)")
	fs.StringVar(&o.ignore, "ignore", "", "do not run these `rules`")
	fs.StringVar(&o.enable, "enable", "", "also run these `rules`")
	fs.StringVar(&o.dis, "disable", "", "same as --ignore")
	fs.StringVar(&o.failOn, "fail-on", "", "exit 1 at this `severity` or above: info, warning, error (default error)")
	fs.StringVar(&o.baseline, "baseline", "", "baseline `file` (default dgdlint-baseline.json next to the config)")
	fs.BoolVar(&o.writeBaseline, "write-baseline", false, "record the current findings as the baseline and exit 0")
	fs.BoolVar(&o.noBase, "no-baseline", false, "report findings that are in the baseline too")
	fs.BoolVar(&o.local, "local", false, "file-local rules only; do not use the lib index")
	fs.BoolVar(&o.stats, "stats", false, "print per-rule counts and what could not be verified")
	fs.IntVar(&o.maxPerFile, "max-per-file", 0, "report at most `n` findings per rule per file")
	fs.BoolVar(&o.quiet, "quiet", false, "no summary on stderr")
	fs.BoolVar(&o.quiet, "q", false, "shorthand for --quiet")
	fs.BoolVar(&o.exitZero, "exit-zero", false, "exit 0 even when there are findings")
	fs.BoolVar(&o.listRules, "rules", false, "list rules (same as `dgdlint rules`)")
	fs.BoolVar(&o.showVersion, "version", false, "print version")
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}

	// Flags may appear before, between and after paths.
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if err == flag.ErrHelp {
				return 0
			}
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}

	fail := func(err error) int {
		fmt.Fprintln(stderr, "dgdlint:", err)
		return 2
	}
	if o.showVersion {
		fmt.Fprintln(stdout, "dgdlint", version)
		return 0
	}
	if o.listRules || (len(pos) > 0 && pos[0] == "rules") {
		printRules(stdout)
		return 0
	}
	if len(pos) > 0 && pos[0] == "explain" {
		if len(pos) != 2 {
			return fail(fmt.Errorf("usage: dgdlint explain RULE"))
		}
		return explain(stdout, stderr, pos[1])
	}
	showConfig := len(pos) > 0 && pos[0] == "config"
	if showConfig {
		pos = pos[1:]
	}
	// The library takes absolute paths and is told where profiles may be
	// installed; the working directory and the environment are read here.
	wd, err := os.Getwd()
	if err != nil {
		return fail(err)
	}
	userConfig, _ := os.UserConfigDir()
	home, _ := os.UserHomeDir()
	profileDirs := profile.UserDirs(os.Getenv(profile.PathEnv), userConfig, home)
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return filepath.Clean(p)
		}
		return filepath.Join(wd, p)
	}
	fsys := vfs.OS{}
	profileCmd := ""
	if len(pos) > 0 && pos[0] == "profile" {
		if len(pos) < 2 || (pos[1] != "build" && pos[1] != "show") {
			return fail(fmt.Errorf("usage: dgdlint profile build [-o FILE] | dgdlint profile show [NAME|FILE] [LIBPATH]"))
		}
		profileCmd, pos = pos[1], pos[2:]
	}
	if profileCmd == "show" {
		return profileShow(stdout, stderr, fsys, o, pos, wd, profileDirs)
	}
	for i, p := range pos {
		pos[i] = abs(p)
	}

	prj, err := config.Discover(fsys, pos, config.DiscoverOptions{
		ConfigPath: o.configPath, Root: o.root, NoConfig: o.noConfig,
		Profile: o.profile, LibPath: o.libPath,
		WorkDir: wd, ProfileDirs: profileDirs,
	})
	if err != nil {
		return fail(err)
	}
	if o.failOn != "" {
		if _, ok := diag.ParseSeverity(o.failOn); !ok {
			return fail(fmt.Errorf("unknown severity %q for --fail-on", o.failOn))
		}
		prj.Config.Lint.FailOn = o.failOn
	}
	if o.baseline != "" {
		file := abs(o.baseline)
		prj.Config.Lint.Baseline = file
		if prj.Config.Path == "" {
			prj.Config.Path = file // anchor for relative baseline paths
			prj.Config.Dir = filepath.Dir(file)
		}
	}
	l, err := lint.New(prj, fsys)
	if err != nil {
		return fail(err)
	}
	if showConfig {
		printConfig(stdout, l)
		return 0
	}
	if profileCmd == "build" {
		p, err := l.BuildProfile(ctx)
		if err != nil {
			return fail(err)
		}
		data, err := p.Marshal()
		if err != nil {
			return fail(err)
		}
		out := o.output
		if out == "" {
			out = filepath.Join(prj.Config.Dir, profile.FileName)
		}
		if err := os.WriteFile(out, data, 0o644); err != nil {
			return fail(err)
		}
		if !o.quiet {
			fmt.Fprintf(stderr, "wrote %s: profile %q, %d stubs, %d bytes\n", out, p.Name, len(p.Files), len(data))
		}
		return 0
	}

	req := lint.Request{Paths: pos, Options: lint.Options{
		Select:     split(o.sel),
		Enable:     split(o.enable),
		Disable:    append(split(o.ignore), split(o.dis)...),
		Local:      o.local,
		NoBaseline: o.noBase || o.writeBaseline,
		MaxPerFile: o.maxPerFile,
	}}
	res, err := l.Run(ctx, req)
	if err != nil {
		return fail(err)
	}

	if o.writeBaseline {
		path := l.BaselinePath()
		if path == "" {
			return fail(fmt.Errorf("--write-baseline needs a dgdtools.yml (or --baseline FILE) to anchor the baseline"))
		}
		data, err := l.NewBaseline(res.Diagnostics).Marshal()
		if err != nil {
			return fail(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return fail(err)
		}
		if !o.quiet {
			fmt.Fprintf(stderr, "wrote %d finding(s) to %s\n", len(res.Diagnostics), path)
		}
		return 0
	}

	out := stdout
	if o.output != "" {
		f, err := os.Create(o.output)
		if err != nil {
			return fail(err)
		}
		defer f.Close()
		out = f
	}
	if err := report.Write(out, o.format, res, l.Rules(), report.Options{Version: version}); err != nil {
		return fail(err)
	}
	if !o.quiet {
		report.Summary(stderr, res, o.stats)
	}

	if o.exitZero {
		return 0
	}
	threshold := lint.Threshold(prj.Config)
	for _, d := range res.Diagnostics {
		if d.Severity >= threshold {
			return 1
		}
	}
	return 0
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func printRules(w io.Writer) {
	group := ""
	for _, r := range lint.Rules() {
		if r.Group != group {
			group = r.Group
			fmt.Fprintf(w, "%s:\n", group)
		}
		def := "off"
		if r.Default {
			def = "on "
		}
		scope := "file "
		if r.CrossFile {
			scope = "lib  "
		}
		fmt.Fprintf(w, "  %s %-28s %s %-7s %s %s\n", r.Code, r.Name, def, r.Severity, scope, r.Doc)
	}
}

func explain(stdout, stderr io.Writer, key string) int {
	for _, r := range lint.Rules() {
		if r.Name == key || strings.EqualFold(r.Code, key) {
			def := "off by default"
			if r.Default {
				def = "on by default"
			}
			scope := "file-local"
			if r.CrossFile {
				scope = "needs the lib index"
			}
			fmt.Fprintf(stdout, "%s  %s\n%s group, %s, %s, %s\n\n%s\n\n%s\n\nSuppress one occurrence with a comment on the line above:\n    /* dgdlint:disable-next-line %s */\n",
				r.Code, r.Name, r.Group, r.Severity, def, scope, r.Doc, r.Explain, r.Name)
			return 0
		}
	}
	fmt.Fprintf(stderr, "dgdlint: unknown rule %q (see `dgdlint rules`)\n", key)
	return 2
}

// profileShow summarizes a profile, or prints one of its stubs.
func profileShow(stdout, stderr io.Writer, fsys vfs.FS, o options, args []string, wd string, profileDirs []string) int {
	ref := o.profile
	if ref == "" && len(args) > 0 {
		ref, args = args[0], args[1:]
	}
	if ref == "" {
		fmt.Fprintf(stdout, "`profile: NAME` is looked up as NAME.profile in:\n  %s\n",
			strings.Join(append([]string{wd}, profileDirs...), "\n  "))
		return 0
	}
	p, err := profile.Load(fsys, ref, wd, profileDirs)
	if err != nil {
		fmt.Fprintln(stderr, "dgdlint:", err)
		return 2
	}
	if len(args) > 0 {
		stub, ok := p.Files[args[0]]
		if !ok {
			fmt.Fprintf(stderr, "dgdlint: profile %s has no stub for %s\n", p.Name, args[0])
			return 2
		}
		fmt.Fprint(stdout, stub)
		return 0
	}
	headers, objects, bytes := 0, 0, 0
	for name, s := range p.Files {
		bytes += len(s)
		if strings.HasSuffix(name, ".c") {
			objects++
		} else {
			headers++
		}
	}
	tc := "unknown"
	if p.Dialect.Typechecking != nil {
		tc = fmt.Sprint(*p.Dialect.Typechecking)
	}
	fmt.Fprintf(stdout, "profile:      %s (format %d) %s\n", p.Name, p.Format, p.Path)
	fmt.Fprintf(stdout, "dialect:      %s %s %s, typechecking %s, // comments %v, closures %v\n",
		p.Dialect.Language, p.Dialect.Driver, p.Dialect.Version, tc, p.Dialect.SlashSlash, p.Dialect.Closures)
	fmt.Fprintf(stdout, "include dirs: %s\n", strings.Join(p.IncludeDirs, ", "))
	fmt.Fprintf(stdout, "include file: %s\n", p.IncludeFile)
	fmt.Fprintf(stdout, "auto objects: %s\n", strings.Join(p.AutoObjects, ", "))
	fmt.Fprintf(stdout, "complete for: %s\n", strings.Join(p.Covered, " "))
	fmt.Fprintf(stdout, "checkouts:    %s\n", strings.Join(p.Checkouts, ", "))
	fmt.Fprintf(stdout, "stubs:        %d objects, %d headers, %d bytes of declarations\n", objects, headers, bytes)
	return 0
}

func printConfig(w io.Writer, l *lint.Linter) {
	prj := l.Project()
	orNone := func(s string) string {
		if s == "" {
			return "(none)"
		}
		return s
	}
	cfg := prj.Config
	fmt.Fprintf(w, "config file:   %s\n", orNone(prj.ConfigPath))
	fmt.Fprintf(w, "lib root:      %s\n", orNone(prj.Root))
	if !prj.HasLib() {
		fmt.Fprintf(w, "mode:          file-local (cross-file rules need a lib root)\n")
	} else if prj.Partial {
		fmt.Fprintf(w, "mode:          partial checkout (what is not in the checkout or the profile is unknown)\n")
	} else if l.Index().Complete() {
		fmt.Fprintf(w, "mode:          whole lib\n")
	} else {
		fmt.Fprintf(w, "mode:          whole lib, incomplete\n")
	}
	if prj.Profile != nil {
		fmt.Fprintf(w, "profile:       %s (%d stubs)\n", prj.Profile.Name, len(prj.Profile.Files))
	} else {
		fmt.Fprintf(w, "profile:       (none)\n")
	}
	if prj.CheckoutLib != "" {
		fmt.Fprintf(w, "checkout:      %s is %s\n", prj.CheckoutDir, prj.CheckoutLib)
	}
	fmt.Fprintf(w, "include dirs:  %s\n", orNone(strings.Join(cfg.IncludeDirs, ", ")))
	fmt.Fprintf(w, "include file:  %s\n", orNone(cfg.IncludeFile))
	fmt.Fprintf(w, "auto objects:  %s\n", orNone(strings.Join(cfg.AutoObjects, ", ")))
	tc := "(unknown: typechecking-dependent rules are silent)"
	if cfg.Dialect.Typechecking != nil {
		tc = fmt.Sprint(*cfg.Dialect.Typechecking)
	}
	fmt.Fprintf(w, "typechecking:  %s\n", tc)
	fmt.Fprintf(w, "baseline:      %s\n", orNone(l.BaselinePath()))
	fmt.Fprintf(w, "fail on:       %s\n", lint.Threshold(cfg))
	for _, n := range slices.Concat(prj.Notes, l.Index().Notes()) {
		fmt.Fprintf(w, "note: %s\n", n.Message)
	}
	fmt.Fprintf(w, "rules:\n")
	rules, _ := l.EffectiveRules("")
	for _, r := range rules {
		fmt.Fprintf(w, "  %s %-28s %s\n", r.Code, r.Name, r.Severity)
	}
}
