package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/report"
)

const lintUsage = `usage: dgdtools lint [flags] [path ...]

Lints the named files and the .c and .h files under the named
directories. With no path, the whole project: the lib root of the nearest
dgdtools.yml at or above the working directory. Without a dgdtools.yml
only file-local rules run. A wizard with only their own folder puts
"profile: NAME" in a dgdtools.yml there (or passes --profile).

Exit codes: 0 no findings at or above the fail-on severity, 1 findings,
2 usage, configuration or I/O error.
`

// lintCmd is `dgdtools lint`: pkg/lint run over the paths, pkg/report
// for the output.
func lintCmd(ctx context.Context, args []string, _ io.Reader, stdout, stderr io.Writer) int {
	var (
		project                    projectFlags
		format, output             string
		sel, ignore, enable        string
		failOn, baseline           string
		writeBaseline, noBaseline  bool
		local, stats, quiet, exit0 bool
		maxPerFile                 int
	)
	fs := newFlags("lint", lintUsage, stderr)
	project.register(fs)
	fs.StringVar(&format, "output-format", "human", "how findings are written: "+strings.Join(report.Formats, ", ")+" (a `format`)")
	fs.StringVar(&output, "output", "", "write findings to `file` instead of standard output")
	fs.StringVar(&output, "o", "", "short for --output")
	fs.StringVar(&sel, "select", "", "run only these `rules` (names, codes or groups, comma-separated)")
	fs.StringVar(&enable, "enable", "", "also run these `rules`")
	fs.StringVar(&ignore, "ignore", "", "do not run these `rules`")
	fs.StringVar(&failOn, "fail-on", "", "exit 1 at this `severity` or above: info, warning, error (default error)")
	fs.StringVar(&baseline, "baseline", "", "baseline `file` (default "+lint.BaselineFileName+" next to the config)")
	fs.BoolVar(&writeBaseline, "write-baseline", false, "record the current findings as the baseline and exit 0")
	fs.BoolVar(&noBaseline, "no-baseline", false, "report findings that are in the baseline too")
	fs.BoolVar(&local, "local", false, "file-local rules only; do not use the lib index")
	fs.BoolVar(&stats, "stats", false, "print per-rule counts and what could not be verified")
	fs.IntVar(&maxPerFile, "max-per-file", 0, "report at most `n` findings per rule per file")
	fs.BoolVar(&quiet, "quiet", false, "no summary on standard error")
	fs.BoolVar(&quiet, "q", false, "short for --quiet")
	fs.BoolVar(&exit0, "exit-zero", false, "exit 0 even when there are findings")
	paths, code, done := parse(fs, args)
	if done {
		return code
	}
	fail := func(err error) int { return failure(stderr, "lint", err) }

	p, err := environment()
	if err != nil {
		return fail(err)
	}
	for i, path := range paths {
		paths[i] = p.abs(path)
	}
	l, err := newLinter(p, &project, paths, failOn, baseline)
	if err != nil {
		return fail(err)
	}
	res, err := l.Run(ctx, lint.Request{Paths: paths, Options: lint.Options{
		Select:     split(sel),
		Enable:     split(enable),
		Disable:    split(ignore),
		Local:      local,
		NoBaseline: noBaseline || writeBaseline,
		MaxPerFile: maxPerFile,
	}})
	if err != nil {
		return fail(err)
	}

	if writeBaseline {
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
		if !quiet {
			fmt.Fprintf(stderr, "wrote %d finding(s) to %s\n", len(res.Diagnostics), path)
		}
		return 0
	}

	out := stdout
	if output != "" {
		f, err := os.Create(output)
		if err != nil {
			return fail(err)
		}
		defer f.Close()
		out = f
	}
	if err := report.Write(out, format, res, l.Rules(), report.Options{Version: version}); err != nil {
		return fail(err)
	}
	if !quiet {
		report.Summary(stderr, res, stats)
	}
	if exit0 {
		return 0
	}
	threshold := lint.Threshold(l.Project().Config)
	for _, d := range res.Diagnostics {
		if d.Severity >= threshold {
			return 1
		}
	}
	return 0
}

// newLinter discovers the one project of the paths and makes its linter.
// failOn and baseline, when given, are the command line's word on what
// the config also says.
func newLinter(p process, project *projectFlags, paths []string, failOn, baseline string) (*lint.Linter, error) {
	prj, err := config.Discover(p.fsys, paths, project.discover(p))
	if err != nil {
		return nil, err
	}
	if failOn != "" {
		if _, ok := diag.ParseSeverity(failOn); !ok {
			return nil, fmt.Errorf("unknown severity %q for --fail-on", failOn)
		}
		prj.Config.Lint.FailOn = failOn
	}
	if baseline != "" {
		prj.Config.Lint.Baseline = p.abs(baseline)
	}
	return lint.New(prj, p.fsys)
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
