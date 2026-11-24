package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/lint"
)

// rulesCmd is `dgdtools rules`: the rule table.
func rulesCmd(_ context.Context, args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := newFlags("rules", "usage: dgdtools rules\n\nLists the lint rules: code, name, on or off by default, default severity,\nwhether the rule needs the lib (lib) or one file is enough (file).\n", stderr)
	pos, code, done := parse(fs, args)
	if done {
		return code
	}
	if len(pos) > 0 {
		fs.Usage()
		return 2
	}
	group := ""
	for _, r := range lint.Rules() {
		if r.Group != group {
			group = r.Group
			fmt.Fprintf(stdout, "%s:\n", group)
		}
		def := "off"
		if r.Default {
			def = "on "
		}
		scope := "file "
		if r.CrossFile {
			scope = "lib  "
		}
		fmt.Fprintf(stdout, "  %s %-28s %s %-7s %s %s\n", r.Code, r.Name, def, r.Severity, scope, r.Doc)
	}
	return 0
}

// explainCmd is `dgdtools explain RULE`.
func explainCmd(_ context.Context, args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := newFlags("explain", "usage: dgdtools explain RULE\n\nDescribes one lint rule, given by name or code.\n", stderr)
	pos, code, done := parse(fs, args)
	if done {
		return code
	}
	if len(pos) != 1 {
		fs.Usage()
		return 2
	}
	for _, r := range lint.Rules() {
		if r.Name != pos[0] && !strings.EqualFold(r.Code, pos[0]) {
			continue
		}
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
	return failure(stderr, "explain", fmt.Errorf("unknown rule %q (see `dgdtools rules`)", pos[0]))
}

const configUsage = `usage: dgdtools config [flags] [path]

Shows the project of a path (default: the working directory) and the
settings a file there is formatted and linted by, with where each comes
from. For a directory the settings are those of a .c file in it.
`

// configCmd is `dgdtools config [path]`: what discovery and settings
// resolution make of a path.
func configCmd(_ context.Context, args []string, _ io.Reader, stdout, stderr io.Writer) int {
	var project projectFlags
	fs := newFlags("config", configUsage, stderr)
	project.register(fs)
	pos, code, done := parse(fs, args)
	if done {
		return code
	}
	fail := func(err error) int { return failure(stderr, "config", err) }
	if len(pos) > 1 {
		fs.Usage()
		return 2
	}
	p, err := environment()
	if err != nil {
		return fail(err)
	}
	target := p.wd
	if len(pos) == 1 {
		target = p.abs(pos[0])
	}
	l, err := newLinter(p, &project, []string{target}, "", "")
	if err != nil {
		return fail(err)
	}
	file, shown := target, target
	if p.fsys.IsDir(target) {
		file, shown = filepath.Join(target, "_.c"), filepath.Join(target, "*.c")
	}
	settings, err := l.Project().Settings(file)
	if err != nil {
		return fail(err)
	}
	rules, err := l.EffectiveRules(file)
	if err != nil {
		return fail(err)
	}

	w, prj := stdout, l.Project()
	cfg := prj.Config
	orNone := func(s string) string {
		if s == "" {
			return "(none)"
		}
		return s
	}
	switch {
	case prj.UserConfig:
		fmt.Fprintf(w, "config file:   %s (user-level: no project config found)\n", prj.ConfigPath)
	default:
		fmt.Fprintf(w, "config file:   %s\n", orNone(prj.ConfigPath))
	}
	fmt.Fprintf(w, "lib root:      %s\n", orNone(prj.Root))
	switch {
	case !prj.HasLib():
		fmt.Fprintf(w, "mode:          file-local (cross-file rules need a lib root)\n")
	case prj.Partial:
		fmt.Fprintf(w, "mode:          partial checkout (what is not in the checkout or the profile is unknown)\n")
	case l.Index().Complete():
		fmt.Fprintf(w, "mode:          whole lib\n")
	default:
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

	fmt.Fprintf(w, "\nsettings of %s\n", shown)
	for _, sc := range settings.Scopes {
		fmt.Fprintf(w, "scope:         %s\n", sc.Path)
	}
	f, from := settings.Format, settings.From
	fmt.Fprintf(w, "indent style:  %-9s (%s)\n", f.IndentStyle, from.IndentStyle)
	fmt.Fprintf(w, "indent width:  %-9d (%s)\n", f.IndentWidth, from.IndentWidth)
	fmt.Fprintf(w, "line endings:  %-9s (%s)\n", f.LineEndings, from.LineEndings)
	fmt.Fprintf(w, "rules:\n")
	for _, r := range rules {
		fmt.Fprintf(w, "  %s %-28s %s\n", r.Code, r.Name, r.Severity)
	}
	return 0
}
