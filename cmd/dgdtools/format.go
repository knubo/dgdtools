package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/mmcdole/dgdtools/internal/par"
	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/fileset"
	"github.com/mmcdole/dgdtools/pkg/format"
)

// sourceExts are the files a directory stands for.
var sourceExts = []string{".c", ".h"}

const formatUsage = `usage: dgdtools format [flags] path ...
       dgdtools format [flags] -

Formats the named files and the .c and .h files under the named
directories, in place. "-" formats standard input to standard output.
Each file is formatted by its own settings: the format: section of the
dgdtools.yml files above it, then .editorconfig. With --check or --diff
nothing is written.

A comment "dgdtools-ignore-start format" keeps everything as it is until
"dgdtools-ignore-end format". --lines and --changed format only part of
a file.

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
	c := &formatter{ctx: ctx, stdout: stdout, stderr: stderr}
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
	fs.StringVar(&lineEndings, "line-endings", "", "write `line endings` as they are (preserve), lf or crlf; overrides every configuration")
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
	// A file is formatted by its settings: what only lint needs of the
	// project (a profile not fetched yet) is gone without.
	opt.SettingsOnly = true
	c.projects = config.NewWorkspace(c.fsys, opt)

	stdinPath, fromStdin, err := c.standardInput(paths, stdinName)
	switch {
	case err != nil:
		return c.fail(err)
	case len(paths) == 0 && !fromStdin:
		return c.fail(errors.New("nothing to format: give a path (\".\" is the working directory) or \"-\" for standard input"))
	case c.gitBase != "" && !c.changed:
		return c.fail(errors.New("--base goes with --changed"))
	case c.changed && (fromStdin || len(c.lines) > 0):
		return c.fail(errors.New("--changed cannot be combined with --lines or standard input"))
	case len(c.lines) > 0 && !fromStdin && (len(paths) != 1 || c.fsys.IsDir(c.abs(paths[0]))):
		return c.fail(errors.New("--lines takes exactly one file: line numbers belong to a file"))
	}

	if fromStdin {
		if stdinName == "" {
			stdinName = "<stdin>"
		}
		return c.stdin(stdin, stdinPath, stdinName)
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

	reformatted, unchanged, failed int
	errored                        bool
}

// job is one file to format: its path, how the user named it, and the
// project whose settings it is formatted by.
type job struct {
	path, name string
	prj        *config.Project
	// lines, when not empty, are the only lines to format.
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

// stdin formats standard input, as the file at path, to standard output.
func (c *formatter) stdin(stdin io.Reader, path, name string) int {
	src, err := io.ReadAll(stdin)
	if err != nil {
		return c.fail(err)
	}
	prj, err := c.projects.Project(filepath.Dir(path))
	if err != nil {
		return c.fail(err)
	}
	r := c.format(job{path: path, name: name, prj: prj, lines: c.lines}, src)
	if r.err == nil && !c.check && !c.diff {
		c.stdout.Write(r.out)
		return 0
	}
	c.report(r)
	return c.exit()
}

// collect returns the jobs for the paths given on the command line. A
// file is formatted even if its project excludes it; a directory stands
// for the .c and .h files under it that their project does not exclude.
// The project of a file is that of the nearest root config above it, so
// a directory with a root config of its own, below the one named, is
// walked as the project it is.
func (c *formatter) collect(paths []string) []job {
	var jobs []job
	var changed *changes
	if c.changed {
		changed = &changes{base: c.gitBase, lines: map[string][]format.LineRange{}, whole: map[string]bool{}}
	}
	told := map[*config.Project]bool{}
	add := func(prj *config.Project, path, name string) {
		if !told[prj] && !c.quiet {
			told[prj] = true
			for _, n := range prj.Notes {
				if n.Kind == diag.NoteUnread {
					fmt.Fprintf(c.stderr, "note: formatting by the settings without it: %s\n", n.Message)
				}
			}
		}
		j := job{path: path, name: name, prj: prj, lines: c.lines}
		if changed != nil && !changed.whole[path] {
			if j.lines = changed.lines[path]; len(j.lines) == 0 {
				return // nothing in it changed
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
		if changed != nil {
			if err := changed.read(c.ctx, path, c.fsys.IsDir(path)); err != nil {
				c.fail(err)
				continue
			}
		}
		if !c.fsys.IsDir(path) {
			prj, err := c.projects.Project(path)
			if err != nil {
				c.fail(err)
				continue
			}
			add(prj, path, arg)
			continue
		}
		first := len(jobs)
		var walk func(dir string)
		walk = func(dir string) {
			prj, err := c.projects.Project(dir)
			if err != nil {
				c.fail(err)
				return
			}
			nested := map[string]bool{} // the directories below that are projects of their own
			err = prj.Tree().Walk(c.ctx, c.fsys, dir, sourceExts, fileset.NewMatcher(prj.Config.Exclude), func(file string) {
				if other := prj.Nested(file); other != "" {
					nested[filepath.Dir(other)] = true
					return
				}
				shown := file
				if rel, err := filepath.Rel(path, file); err == nil {
					shown = filepath.Join(arg, rel)
				}
				add(prj, file, shown)
			})
			if err != nil && c.ctx.Err() == nil {
				c.fail(err)
			}
			for _, d := range slices.Sorted(maps.Keys(nested)) {
				walk(d)
			}
		}
		walk(path)
		slices.SortFunc(jobs[first:], func(a, b job) int { return strings.Compare(a.path, b.path) })
	}
	return jobs
}

// format formats one file's source.
func (c *formatter) format(j job, src []byte) result {
	out, err := format.Source(j.prj, j.path, src, j.lines)
	return result{job: j, src: src, out: out, err: err}
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

// writeFile replaces the contents of the file at path, keeping its
// permission bits. The text is written beside the file and renamed over
// it, so that an interrupted run leaves either the old file or the new
// one. A symbolic link stays one: what it points to is replaced.
func writeFile(path string, out []byte) (err error) {
	if path, err = filepath.EvalSymlinks(path); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.Remove(tmp.Name())
		}
	}()
	_, err = tmp.Write(out)
	if err == nil {
		err = tmp.Chmod(info.Mode().Perm())
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
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
