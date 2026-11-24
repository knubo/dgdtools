package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/mmcdole/dgdtools/internal/par"
	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/fileset"
	"github.com/mmcdole/dgdtools/pkg/format"
	"github.com/mmcdole/dgdtools/pkg/lexer"
)

const formatUsage = `usage: dgdtools format [flags] path ...
       dgdtools format [flags] -

Formats the named files and the .c and .h files under the named
directories, in place. "-" formats standard input to standard output.
Each file is formatted by its own settings: the format: section of the
dgdtools.yml files above it, then .editorconfig. With --check or --diff
nothing is written.

A comment "dgdfmt: off" keeps everything as it is until "dgdfmt: on".
--lines and --changed format only part of a file.

Every result is proven before it is used: it has the same tokens as the
source, and formatting it again changes nothing. A file that does not
tokenize is reported and left alone.

Exit codes: 0 done, or nothing to do; 1 --check or --diff found files
that would change; 2 usage, configuration or I/O error, or a file that
could not be formatted.
`

// formatCmd is `dgdtools format`: pkg/format applied to each file with
// the settings pkg/config resolves for it.
func formatCmd(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := &formatter{ctx: ctx, stdout: stdout, stderr: stderr, base: map[*config.Project]format.Options{}}
	var (
		project                  projectFlags
		stdinName                string
		indentStyle, lineEndings string
		indentWidth              int
	)
	fs := newFlags("format", formatUsage, stderr)
	project.register(fs)
	fs.BoolVar(&c.check, "check", false, "write nothing; list the files that would change, and exit 1 if there are any")
	fs.BoolVar(&c.diff, "diff", false, "write nothing; print what would change as unified diffs, and exit 1 if anything would")
	fs.StringVar(&stdinName, "stdin-filename", "", "format standard input as the file `path` (which need not exist): its settings, its name in messages")
	fs.Var(&c.lines, "lines", "format only lines `A-B` (1-based, inclusive) of the one file given; may be repeated")
	fs.BoolVar(&c.changed, "changed", false, "format only the lines that differ from the git revision --base, and files git does not know yet")
	fs.StringVar(&c.gitBase, "base", "", "with --changed: the git `revision` to compare the working tree with (default HEAD)")
	fs.StringVar(&indentStyle, "indent-style", "", "indent with `space` or tab; overrides every configuration")
	fs.IntVar(&indentWidth, "indent-width", 0, "spaces per level, a `number`; overrides every configuration")
	fs.StringVar(&lineEndings, "line-endings", "", "line endings: preserve, lf or crlf (a `policy`); overrides every configuration")
	fs.BoolVar(&c.quiet, "quiet", false, "no summary on standard error")
	fs.BoolVar(&c.quiet, "q", false, "short for --quiet")
	paths, code, done := parse(fs, args)
	if done {
		return code
	}

	var err error
	if c.process, err = environment(); err != nil {
		return c.fail(err)
	}
	opt := project.discover(c.process)
	if indentStyle != "" {
		if opt.Format.IndentStyle, err = config.ParseIndentStyle(indentStyle); err != nil {
			return c.fail(fmt.Errorf("--indent-style: %w", err))
		}
	}
	if indentWidth != 0 {
		if err = config.CheckIndentWidth(indentWidth); err != nil {
			return c.fail(fmt.Errorf("--indent-width: %w", err))
		}
		opt.Format.IndentWidth = indentWidth
	}
	if lineEndings != "" {
		if opt.Format.LineEndings, err = config.ParseLineEndings(lineEndings); err != nil {
			return c.fail(fmt.Errorf("--line-endings: %w", err))
		}
	}
	c.projects = config.NewWorkspace(c.fsys, opt)

	fromStdin := len(paths) == 1 && paths[0] == "-" || len(paths) == 0 && stdinName != ""
	switch {
	case len(paths) == 0 && stdinName == "":
		return c.fail(errors.New("nothing to format: give a path (\".\" is the working directory) or \"-\" for standard input"))
	case !fromStdin && (stdinName != "" || slices.Contains(paths, "-")):
		return c.fail(errors.New("standard input (\"-\", --stdin-filename) cannot be combined with paths"))
	case c.gitBase != "" && !c.changed:
		return c.fail(errors.New("--base goes with --changed"))
	case c.changed && (fromStdin || len(c.lines) > 0):
		return c.fail(errors.New("--changed cannot be combined with --lines or standard input"))
	case len(c.lines) > 0 && !fromStdin && (len(paths) != 1 || c.fsys.IsDir(c.abs(paths[0]))):
		return c.fail(errors.New("--lines takes exactly one file: line numbers belong to a file"))
	}

	if fromStdin {
		return c.stdin(stdin, stdinName)
	}
	jobs := c.collect(paths)
	// Format on all CPUs, report in order.
	const batch = 256
	for len(jobs) > 0 && ctx.Err() == nil {
		n := min(batch, len(jobs))
		results := make([]result, n)
		order := make([]int, n)
		for i := range order {
			order[i] = i
		}
		par.Each(ctx, order, func(i int) {
			src, err := c.fsys.ReadFile(jobs[i].path)
			if err != nil {
				results[i] = result{job: jobs[i], err: err}
				return
			}
			results[i] = c.format(jobs[i], src)
		})
		if ctx.Err() != nil {
			break
		}
		for _, r := range results {
			c.report(r)
		}
		jobs = jobs[n:]
	}
	if err := ctx.Err(); err != nil {
		return c.fail(err)
	}
	c.summary()
	return c.exit()
}

// formatter is one run of `dgdtools format`.
type formatter struct {
	process
	ctx            context.Context
	stdout, stderr io.Writer

	check, diff, quiet bool
	lines              lineRanges
	changed            bool
	gitBase            string

	projects *config.Workspace
	// base is each project's part of the formatter's options.
	base map[*config.Project]format.Options

	reformatted, unchanged, failed int
	errored                        bool
}

// job is one file to format: its path, how the user named it, and what
// it is formatted with.
type job struct {
	path, name string
	opts       format.Options
	dialect    lexer.Dialect
	// lines, when whole is not set, are the only lines to format.
	whole bool
	lines []format.LineRange
}

type result struct {
	job
	src, out []byte
	err      error
}

// fail reports an error; the run will exit 2.
func (c *formatter) fail(err error) int {
	c.errored = true
	return failure(c.stderr, "format", err)
}

// exit is the exit code of the run so far.
func (c *formatter) exit() int {
	switch {
	case c.errored:
		return 2
	case (c.check || c.diff) && c.reformatted > 0:
		return 1
	}
	return 0
}

// job makes the job for the file at path, which belongs to prj.
func (c *formatter) job(prj *config.Project, path, name string) (job, error) {
	settings, err := prj.Settings(path)
	if err != nil {
		return job{}, err
	}
	base, ok := c.base[prj]
	if !ok {
		base = format.OptionsFor(prj.Config, config.Format{})
		c.base[prj] = base
	}
	return job{
		path: path, name: name,
		opts: base.With(settings.Format), dialect: prj.Config.TokenDialect(),
		whole: len(c.lines) == 0, lines: c.lines,
	}, nil
}

// stdin formats standard input, named name when that is not empty, to
// standard output.
func (c *formatter) stdin(stdin io.Reader, name string) int {
	src, err := io.ReadAll(stdin)
	if err != nil {
		return c.fail(err)
	}
	path := filepath.Join(c.wd, "<stdin>")
	if name != "" {
		path = c.abs(name)
	} else {
		name = "<stdin>"
	}
	prj, err := c.projects.Project(filepath.Dir(path))
	if err != nil {
		return c.fail(err)
	}
	j, err := c.job(prj, path, name)
	if err != nil {
		return c.fail(err)
	}
	r := c.format(j, src)
	if r.err == nil && !c.check && !c.diff {
		c.stdout.Write(r.out)
		return 0
	}
	c.report(r)
	return c.exit()
}

// collect returns the jobs for the paths given on the command line. A
// file is formatted even if its project excludes it; a directory stands
// for the .c and .h files under it that its project does not exclude,
// all of them files of that project.
func (c *formatter) collect(paths []string) []job {
	var jobs []job
	var changed *changes
	if c.changed {
		changed = &changes{base: c.gitBase, lines: map[string][]format.LineRange{}, whole: map[string]bool{}}
	}
	add := func(prj *config.Project, path, name string) {
		j, err := c.job(prj, path, name)
		if err != nil {
			c.fail(err)
			return
		}
		if changed != nil {
			if j.whole = changed.whole[path]; !j.whole {
				if j.lines = changed.lines[path]; len(j.lines) == 0 {
					return // nothing in it changed
				}
			}
		}
		jobs = append(jobs, j)
	}
	for _, arg := range paths {
		path := c.abs(arg)
		if !c.fsys.Exists(path) {
			c.fail(fmt.Errorf("%s: no such file or directory", arg))
			continue
		}
		prj, err := c.projects.Project(path)
		if err != nil {
			c.fail(err)
			continue
		}
		if changed != nil {
			if err := changed.read(c.ctx, path, c.fsys.IsDir(path)); err != nil {
				c.fail(err)
				continue
			}
		}
		if !c.fsys.IsDir(path) {
			add(prj, path, arg)
			continue
		}
		err = prj.Tree().Walk(c.ctx, c.fsys, path, []string{".c", ".h"}, fileset.NewMatcher(prj.Config.Exclude), func(file string) {
			shown := file
			if rel, err := filepath.Rel(path, file); err == nil {
				shown = filepath.Join(arg, rel)
			}
			add(prj, file, shown)
		})
		if err != nil && c.ctx.Err() == nil {
			c.fail(err)
		}
	}
	return jobs
}

// format formats one file's source.
func (c *formatter) format(j job, src []byte) result {
	r := result{job: j, src: src}
	f := lexer.Lex(j.path, src, j.dialect)
	if r.err = f.CheckRoundTrip(); r.err != nil {
		return r
	}
	if j.whole {
		r.out, r.err = format.Format(f, j.opts)
	} else {
		r.out, r.err = format.Lines(f, j.opts, j.lines)
	}
	return r
}

// report does what the mode asks with one file's result.
func (c *formatter) report(r result) {
	switch {
	case r.err != nil:
		c.failed++
		c.fail(r.err)
		return
	case bytes.Equal(r.src, r.out):
		c.unchanged++
		return
	}
	c.reformatted++
	switch {
	case c.diff:
		text, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A: diffLines(r.src), FromFile: r.name,
			B: diffLines(r.out), ToFile: r.name,
			Context: 3,
		})
		if err != nil {
			c.fail(err)
			return
		}
		io.WriteString(c.stdout, text)
	case c.check:
		fmt.Fprintln(c.stdout, r.name)
	default:
		if err := writeFile(r.path, r.out); err != nil {
			c.reformatted--
			c.failed++
			c.fail(err)
		}
	}
}

// diffLines splits text into the lines a unified diff shows, each with
// its line break; a last line without one is marked the way diff marks
// it.
func diffLines(text []byte) []string {
	lines := strings.SplitAfter(string(text), "\n")
	if n := len(lines) - 1; lines[n] == "" {
		lines = lines[:n]
	} else {
		lines[n] += "\n\\ No newline at end of file\n"
	}
	return lines
}

// summary says on standard error what the run did.
func (c *formatter) summary() {
	if c.quiet {
		return
	}
	files := func(n int) string {
		if n == 1 {
			return "1 file"
		}
		return strconv.Itoa(n) + " files"
	}
	did, not := "reformatted", "unchanged"
	if c.check || c.diff {
		did, not = "would be reformatted", "already formatted"
	}
	msg := fmt.Sprintf("%s %s, %s %s", files(c.reformatted), did, files(c.unchanged), not)
	if c.failed > 0 {
		msg += fmt.Sprintf(", %s could not be formatted", files(c.failed))
	}
	fmt.Fprintln(c.stderr, msg)
}

// writeFile replaces path's contents, preserving its permission bits.
func writeFile(path string, out []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, info.Mode().Perm())
}

// lineRanges is the value of the repeatable --lines flag.
type lineRanges []format.LineRange

func (l *lineRanges) String() string { return fmt.Sprint(*l) }

func (l *lineRanges) Set(s string) error {
	first, last, isRange := strings.Cut(s, "-")
	a, err := strconv.Atoi(first)
	b := a
	if err == nil && isRange {
		b, err = strconv.Atoi(last)
	}
	if err != nil || a < 1 || b < a {
		return errors.New("want A-B or A, lines counted from 1")
	}
	*l = append(*l, format.LineRange{First: a, Last: b})
	return nil
}

var _ flag.Value = (*lineRanges)(nil)
