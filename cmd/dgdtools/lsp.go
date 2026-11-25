package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"

	"github.com/mmcdole/dgdtools/pkg/lsp"
)

const lspUsage = `usage: dgdtools lsp [flags]

Runs the language server: the Language Server Protocol on standard input
and standard output, for an editor to start and talk to. It gives the
editor what the other commands give a shell: the findings of "dgdtools
lint" for the files that are open, as they are typed; the fixes and the
dgdtools-ignore comments for them; and "dgdtools format" for a file or
a selection. A file's project is found from the file, as everywhere.

Nothing is written to standard error once the server runs; --log names
a file for what it has to say.

Exit codes: 0 after the editor's shutdown and exit, 1 when the editor
went away without, 2 usage error.
`

// lspCmd is `dgdtools lsp`: pkg/lsp on the process's standard streams.
func lspCmd(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var (
		logFile string
		trace   bool
	)
	fs := newFlags("lsp", lspUsage, stderr)
	fs.StringVar(&logFile, "log", "", "write a debug log to `file` (appended to)")
	fs.BoolVar(&trace, "trace", false, "log every message of the protocol too; needs --log")
	// What some clients add to the command of a server that is to use
	// its standard streams, which is the only way this one talks.
	fs.Bool("stdio", true, "talk on standard input and output (always)")
	rest, code, done := parse(fs, args)
	if done {
		return code
	}
	fail := func(err error) int { return failure(stderr, "lsp", err) }
	switch {
	case len(rest) > 0:
		return fail(errors.New("unexpected argument " + rest[0]))
	case trace && logFile == "":
		return fail(errors.New("--trace needs --log FILE to write to"))
	}
	p, err := environment()
	if err != nil {
		return fail(err)
	}
	opt := lsp.Options{FS: p.fsys, ProfileDirs: p.profileDirs, UserConfig: p.userConfig, Version: version, Trace: trace}
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fail(err)
		}
		defer f.Close()
		opt.Log = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	if err := lsp.Serve(ctx, stdio{stdin, stdout}, opt); err != nil {
		return 1
	}
	return 0
}

// stdio is the two standard streams as the one connection they are.
type stdio struct {
	io.Reader
	io.Writer
}

// Close closes the reading side, when it can be closed, which ends a
// read that is waiting.
func (s stdio) Close() error {
	if c, ok := s.Reader.(io.Closer); ok {
		return c.Close()
	}
	return nil
}
