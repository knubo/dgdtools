package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/format"
)

// changes is what git says has changed in the working tree since a
// revision: per file the lines that are new or different, and the files
// git does not track yet, which are new as a whole. Running git is this
// front end's business; the formatter takes line ranges.
type changes struct {
	base  string // the revision; "" is HEAD
	lines map[string][]format.LineRange
	whole map[string]bool
}

// read adds the changes under path, a file or a directory.
func (c *changes) read(ctx context.Context, path string, isDir bool) error {
	dir, spec := path, "."
	if !isDir {
		dir, spec = filepath.Dir(path), filepath.Base(path)
	}
	base := c.base
	if base == "" {
		base = "HEAD"
	}
	// --relative: names are relative to dir, and nothing outside it is
	// listed. -U0: a hunk is exactly the lines that changed.
	diff, err := git(ctx, dir, "diff", "--no-color", "--no-ext-diff", "--relative", "-U0", base, "--", spec)
	if err != nil {
		return fmt.Errorf("--changed: %w", err)
	}
	file := ""
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			file = ""
			if name, ok := strings.CutPrefix(strings.TrimRight(line[4:], "\t"), "b/"); ok {
				file = filepath.Join(dir, filepath.FromSlash(name))
			}
		case strings.HasPrefix(line, "@@ ") && file != "":
			if r, ok := hunkLines(line); ok {
				c.lines[file] = append(c.lines[file], r)
			}
		}
	}
	untracked, err := git(ctx, dir, "ls-files", "--others", "--exclude-standard", "-z", "--", spec)
	if err != nil {
		return fmt.Errorf("--changed: %w", err)
	}
	for _, name := range strings.Split(untracked, "\x00") {
		if name != "" {
			c.whole[filepath.Join(dir, filepath.FromSlash(name))] = true
		}
	}
	return nil
}

// hunkLines reads the new side of a hunk header, "@@ -a,b +c,d @@": the
// lines c to c+d-1. A hunk that only removes lines has none.
func hunkLines(header string) (format.LineRange, bool) {
	fields := strings.Fields(header)
	if len(fields) < 3 || !strings.HasPrefix(fields[2], "+") {
		return format.LineRange{}, false
	}
	start, count, counted := strings.Cut(fields[2][1:], ",")
	first, err := strconv.Atoi(start)
	n := 1
	if err == nil && counted {
		n, err = strconv.Atoi(count)
	}
	if err != nil || n == 0 {
		return format.LineRange{}, false
	}
	return format.LineRange{First: first, Last: first + n - 1}, true
}

// git runs git in dir and returns what it prints.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "-c", "core.quotePath=false"}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return "", errors.New(msg)
		}
		return "", err
	}
	return out.String(), nil
}
