// dgdfmt formats DGD LPC source, gofmt-style.
//
// It is a thin front end over pkg/format (the layout engine) and
// pkg/config (project discovery); everything a file is formatted by lives
// there.
//
// Without -l, -d or -w it prints the formatted source of each named file
// (or of standard input) to standard output. A directory stands for the
// .c and .h files under it, minus the project's excludes. Each file is
// formatted with the settings of its own project: the nearest
// dgdtools.yml at or above it, found the way dgdlint finds it, which
// gives the dialect (the config's, or its profile's), the format options
// and the macro conventions. Flags given on the command line win.
//
// Every result is protected by the formatter's gate: the output is
// re-lexed, its significant token stream must be identical to the
// input's, and formatting it again must change nothing. Otherwise the
// file is left untouched and an error is reported. Files that do not
// fully tokenize (dead or non-LPC content) are refused.
//
// Exit codes: 0 success, 2 usage, configuration or I/O error, or a file
// that could not be formatted.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"

	"github.com/mmcdole/dgdtools/internal/par"
	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/fileset"
	"github.com/mmcdole/dgdtools/pkg/format"
	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/token"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

const usage = `usage: dgdfmt [flags] [path ...]

Formats the named files, the .c and .h files under the named directories,
or standard input. Each file is formatted with the settings of the nearest
dgdtools.yml at or above it.

flags:
`

type options struct {
	list, diff, write      bool
	configPath             string
	noConfig               bool
	noSlashSlash, closures bool
	showVersion            bool
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var o options
	fs := flag.NewFlagSet("dgdfmt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&o.list, "l", false, "list the files whose formatting differs")
	fs.BoolVar(&o.diff, "d", false, "print diffs instead of the formatted source")
	fs.BoolVar(&o.write, "w", false, "write the result to the source file")
	fs.StringVar(&o.configPath, "config", "", "use this `file` for every path instead of searching for dgdtools.yml")
	fs.BoolVar(&o.noConfig, "no-config", false, "do not look for a config file")
	fs.BoolVar(&o.noSlashSlash, "no-slash-slash", false, "no // comments (a driver built without SLASHSLASH)")
	fs.BoolVar(&o.closures, "closures", false, "'function' is a keyword (a driver built with CLOSURES)")
	fs.BoolVar(&o.showVersion, "version", false, "print version")
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if o.showVersion {
		fmt.Fprintln(stdout, "dgdfmt", version)
		return 0
	}

	c := &command{
		ctx: ctx, o: o, given: map[string]bool{}, fsys: vfs.OS{},
		stdout: stdout, stderr: stderr,
		byDir: map[string]*settings{}, byConfig: map[string]*settings{},
	}
	fs.Visit(func(f *flag.Flag) { c.given[f.Name] = true })
	// The library takes absolute paths and is told where profiles may be
	// installed; the working directory and the environment are read here.
	wd, err := os.Getwd()
	if err != nil {
		return c.fail(err)
	}
	c.wd = wd
	userConfig, _ := os.UserConfigDir()
	home, _ := os.UserHomeDir()
	c.profileDirs = profile.UserDirs(os.Getenv(profile.PathEnv), userConfig, home)

	if fs.NArg() == 0 {
		if o.write {
			return c.fail(fmt.Errorf("cannot use -w with standard input"))
		}
		src, err := io.ReadAll(stdin)
		if err != nil {
			return c.fail(err)
		}
		st, err := c.settingsFor(wd)
		if err != nil {
			return c.fail(err)
		}
		c.report(c.format(job{path: "<stdin>", name: "<stdin>", settings: st}, src))
		return c.code
	}

	var jobs []job
	for _, arg := range fs.Args() {
		p := arg
		if !filepath.IsAbs(p) {
			p = filepath.Join(wd, p)
		}
		switch {
		case !c.fsys.Exists(p):
			c.fail(fmt.Errorf("%s: no such file or directory", arg))
		case c.fsys.IsDir(p):
			jobs = c.walk(jobs, p, arg)
		default:
			jobs = c.add(jobs, p, arg)
		}
	}
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
	return c.code
}

// command is one run of dgdfmt.
type command struct {
	ctx            context.Context
	o              options
	given          map[string]bool // the flags that were passed
	fsys           vfs.FS
	stdout, stderr io.Writer
	wd             string
	profileDirs    []string

	// byDir and byConfig remember the settings found for a directory and
	// for a config file ("" is no config).
	byDir, byConfig map[string]*settings

	code int
}

// settings are what a project says about formatting its files.
type settings struct {
	project *config.Project
	opts    format.Options
	dialect token.Dialect
	exclude *fileset.Matcher
}

// job is one file to format: its path, how the user named it, and its
// project's settings.
type job struct {
	path, name string
	settings   *settings
}

type result struct {
	job
	src, out []byte
	err      error
}

// fail reports an error and makes the run exit 2.
func (c *command) fail(err error) int {
	fmt.Fprintln(c.stderr, "dgdfmt:", err)
	c.code = 2
	return c.code
}

// settingsFor returns the settings for the files in dir: those of the
// nearest config file at or above it (config.FindPath, as dgdlint's
// discovery uses), resolved by config.Discover.
func (c *command) settingsFor(dir string) (*settings, error) {
	if st, ok := c.byDir[dir]; ok {
		return st, nil
	}
	found := c.o.configPath
	if found == "" && !c.o.noConfig {
		var err error
		if found, err = config.FindRoot(c.fsys, dir); err != nil {
			return nil, err
		}
	}
	st, ok := c.byConfig[found]
	if !ok {
		prj, err := config.Discover(c.fsys, []string{dir}, config.DiscoverOptions{
			ConfigPath: found, NoConfig: found == "",
			WorkDir: c.wd, ProfileDirs: c.profileDirs,
		})
		if err != nil {
			return nil, err
		}
		opts := format.OptionsFor(prj.Config, prj.Config.Format)
		st = &settings{
			project: prj, opts: opts,
			dialect: prj.Config.TokenDialect(),
			exclude: fileset.NewMatcher(prj.Config.Exclude),
		}
		if c.given["no-slash-slash"] {
			st.dialect.SlashSlash = !c.o.noSlashSlash
		}
		if c.given["closures"] {
			st.dialect.Closures = c.o.closures
		}
		c.byConfig[found] = st
	}
	c.byDir[dir] = st
	return st, nil
}

// add queues the file at path, which the user named name.
func (c *command) add(jobs []job, path, name string) []job {
	st, err := c.settingsFor(filepath.Dir(path))
	if err != nil {
		c.fail(err)
		return jobs
	}
	return append(jobs, job{path: path, name: name, settings: st})
}

// walk queues the .c and .h files under dir that its project does not
// exclude. The excludes are those of the project dir belongs to, matched
// from that project's lib root; each file is then formatted with the
// settings of its own nearest config.
func (c *command) walk(jobs []job, dir, name string) []job {
	st, err := c.settingsFor(dir)
	if err != nil {
		c.fail(err)
		return jobs
	}
	err = st.project.Tree().Walk(c.ctx, c.fsys, dir, []string{".c", ".h"}, st.exclude, func(p string) {
		shown := p
		if rel, err := filepath.Rel(dir, p); err == nil {
			shown = filepath.Join(name, rel)
		}
		jobs = c.add(jobs, p, shown)
	})
	if err != nil && c.ctx.Err() == nil {
		c.fail(err)
	}
	return jobs
}

// format formats one file's source.
func (c *command) format(j job, src []byte) result {
	r := result{job: j, src: src}
	f := lexer.Lex(j.path, src, j.settings.dialect)
	if r.err = f.CheckRoundTrip(); r.err == nil {
		r.out, r.err = format.Format(f, j.settings.opts)
	}
	return r
}

// report does what the mode asks with one file's result.
func (c *command) report(r result) {
	if r.err != nil {
		c.fail(r.err)
		return
	}
	changed := !bytes.Equal(r.src, r.out)
	switch {
	case c.o.list:
		if changed {
			fmt.Fprintln(c.stdout, r.name)
		}
	case c.o.diff:
		if changed {
			if err := c.printDiff(r); err != nil {
				c.fail(err)
			}
		}
	case c.o.write:
		if changed {
			if err := writeFile(r.path, r.out); err != nil {
				c.fail(err)
			}
		}
	default:
		c.stdout.Write(r.out)
	}
}

// writeFile replaces path's contents, preserving its permission bits.
func writeFile(path string, out []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, info.Mode().Perm())
}

// printDiff writes a unified diff of the file and its formatted form,
// made by diff(1).
func (c *command) printDiff(r result) error {
	var tmp [2]string
	for i, data := range [][]byte{r.src, r.out} {
		f, err := os.CreateTemp("", "dgdfmt-*")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		tmp[i] = f.Name()
	}
	cmd := exec.CommandContext(c.ctx, "diff", "-u",
		"--label", r.name+".orig", tmp[0],
		"--label", r.name, tmp[1])
	cmd.Stdout, cmd.Stderr = c.stdout, c.stderr
	// diff exits 1 when the files differ; that is the expected case.
	if err := cmd.Run(); err != nil {
		if ee, isExit := err.(*exec.ExitError); !isExit || ee.ExitCode() > 1 {
			return fmt.Errorf("diff: %v", err)
		}
	}
	return nil
}
