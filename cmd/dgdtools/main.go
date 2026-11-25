// dgdtools is the command for DGD LPC source: it lints and formats it.
//
//	dgdtools lint [flags] [path ...]      lint files and directories
//	dgdtools format [flags] path ... | -  format files in place
//	dgdtools rules                        list the lint rules
//	dgdtools explain RULE                 describe one rule
//	dgdtools config [path]                show the project and settings of a path
//	dgdtools profile build|show           mudlib profiles
//	dgdtools version
//
// Every subcommand is a thin front end: it parses its flags, reads the
// working directory and the environment (which the libraries never do),
// calls pkg/config, pkg/lint, pkg/format and pkg/report, and prints.
//
// Exit codes, for every subcommand: 0 success; 1 what was looked for was
// found (lint findings at or above the fail-on severity, files that
// `format --check` would change); 2 usage, configuration or I/O error;
// 130 interrupted.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

var version = "dev"

func main() {
	// The first interrupt stops the command in an orderly way; after it
	// the signal has its usual effect again.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	go func() {
		<-ctx.Done()
		stop()
	}()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

const usage = `usage: dgdtools <command> [flags] [arguments]

commands:
  lint [path ...]       lint files and directories (default: the project)
  format path ... | -   format files in place; --check and --diff only report
  rules                 list the lint rules
  explain RULE          describe one rule
  config [path]         show the project and the settings of a path
  profile build         capture the whole lib as a profile
  profile show          summarize a profile, or print one of its stubs
  version               print the version

"dgdtools <command> -h" lists a command's flags.

The project of a path is the nearest dgdtools.yml at or above it; without
one the tools still work, on what a single file can tell.
`

// A command is one subcommand: a front end over the libraries.
type command func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int

var commands = map[string]command{
	"lint":    lintCmd,
	"format":  formatCmd,
	"rules":   rulesCmd,
	"explain": explainCmd,
	"config":  configCmd,
	"profile": profileCmd,
}

// reserved are the commands of later versions. They are not in the help,
// which lists what works; they are known here so that whoever asks for
// one is told what is the matter, and so that the names stay free.
var reserved = map[string]string{
	"lsp":    "the language server",
	"rename": "renaming a symbol across the lib",
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	name, rest := args[0], args[1:]
	switch name {
	case "help", "-h", "-help", "--help":
		if len(rest) > 0 && commands[rest[0]] != nil {
			return commands[rest[0]](ctx, []string{"-h"}, stdin, stdout, stderr)
		}
		fmt.Fprint(stdout, usage)
		return 0
	case "version", "-version", "--version":
		fmt.Fprintln(stdout, "dgdtools", version)
		return 0
	}
	if cmd := commands[name]; cmd != nil {
		return cmd(ctx, rest, stdin, stdout, stderr)
	}
	if what, ok := reserved[name]; ok {
		fmt.Fprintf(stderr, "dgdtools %s: %s is not part of this version (%s)\n", name, what, version)
		return 2
	}
	fmt.Fprintf(stderr, "dgdtools: unknown command %q\n\n%s", name, usage)
	return 2
}

// failure reports an error of the named command and returns the exit code
// for it: 2, or 130 (128 + SIGINT, as a shell reports it) when the
// command was interrupted.
func failure(stderr io.Writer, cmd string, err error) int {
	if errors.Is(err, context.Canceled) {
		fmt.Fprintf(stderr, "dgdtools %s: interrupted\n", cmd)
		return 130
	}
	fmt.Fprintf(stderr, "dgdtools %s: %v\n", cmd, err)
	return 2
}

// process is what a command knows from the process it runs in. The
// libraries take absolute paths and are told where profiles and the
// user's configuration may be; the working directory and the environment
// are read here and nowhere else.
type process struct {
	fsys        vfs.FS
	wd          string
	profileDirs []string
	userConfig  string
}

func environment() (process, error) {
	wd, err := os.Getwd()
	if err != nil {
		return process{}, err
	}
	p := process{fsys: vfs.OS{}, wd: wd}
	userConfig, _ := os.UserConfigDir()
	home, _ := os.UserHomeDir()
	p.profileDirs = profile.UserDirs(os.Getenv(profile.PathEnv), userConfig, home)
	p.userConfig = config.UserConfigPath(p.fsys, userConfig, home)
	return p, nil
}

// abs makes a path given on the command line absolute.
func (p process) abs(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(p.wd, path)
}

// standardInput reports whether a command's arguments ask for standard
// input: "-" alone, or --stdin-filename with no path. It then returns
// where the text stands: at the file named, or at a nameless one in the
// working directory. Asking for it beside paths is an error.
func (p process) standardInput(paths []string, name string) (path string, ok bool, err error) {
	switch {
	case len(paths) == 1 && paths[0] == "-" || len(paths) == 0 && name != "":
		if name == "" {
			name = "<stdin>"
		}
		return p.abs(name), true, nil
	case name != "" || slices.Contains(paths, "-"):
		return "", false, errors.New("standard input (\"-\", --stdin-filename) cannot be combined with paths")
	}
	return "", false, nil
}

// projectFlags are the flags that say where the project is, the same for
// every command that needs one.
type projectFlags struct {
	config, root     string
	profile, libPath string
	noConfig         bool
}

func (f *projectFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.config, "config", "", "use this `file` as the configuration of every path, and no other")
	fs.BoolVar(&f.noConfig, "no-config", false, "read no configuration file (dgdtools.yml, user-level, .editorconfig)")
	fs.StringVar(&f.root, "root", "", "lib root `dir` (the driver's \"/\"); overrides the config")
	fs.StringVar(&f.profile, "profile", "", "mudlib profile: a `name` (see `dgdtools profile show`) or a profile file")
	fs.StringVar(&f.libPath, "lib-path", "", "the lib `path` of the directory this checkout is, when it is not the whole lib")
}

// discover returns the options project discovery runs with.
func (f *projectFlags) discover(p process) config.DiscoverOptions {
	return config.DiscoverOptions{
		ConfigPath: f.config, Root: f.root, NoConfig: f.noConfig,
		Profile: f.profile, LibPath: f.libPath,
		WorkDir: p.wd, ProfileDirs: p.profileDirs, UserConfig: p.userConfig,
	}
}

// newFlags returns the flag set of a command, with its usage text.
func newFlags(name, synopsis string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("dgdtools "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, synopsis)
		any := false
		fs.VisitAll(func(*flag.Flag) { any = true })
		if any {
			fmt.Fprint(stderr, "\nflags:\n")
			fs.PrintDefaults()
		}
	}
	return fs
}

// parse reads a command's arguments. Flags may stand before, between and
// after the other arguments; everything after "--" is an argument. done
// is set when there is nothing more to do: help was asked for (code 0)
// or the flags are wrong (code 2).
func parse(fs *flag.FlagSet, args []string) (pos []string, code int, done bool) {
	var literal []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, literal = args[:i], args[i+1:]
	}
	for {
		if err := fs.Parse(args); err != nil {
			if err == flag.ErrHelp {
				return nil, 0, true
			}
			return nil, 2, true
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	return append(pos, literal...), 0, false
}
