// Package fileset knows where a lib's files are: it maps between paths on
// disk and lib paths, matches the globs that select files, and walks a
// source tree with the project's excludes.
package fileset

import (
	"context"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// Matcher matches slash-separated relative paths against ** globs.
type Matcher struct {
	res []*regexp.Regexp
}

// NewMatcher compiles exclude patterns. Supported syntax: `*` (within one
// path segment), `?`, and `**` (across segments). A pattern matches if it
// matches the whole relative path or any prefix ending at a segment
// boundary (so "players/**" and "**/old/**" behave as expected).
func NewMatcher(patterns []string) *Matcher {
	m := &Matcher{}
	for _, p := range patterns {
		p = strings.Trim(p, "/")
		if p == "" {
			continue
		}
		m.res = append(m.res, regexp.MustCompile("^"+globToRe(p)+"$"))
	}
	return m
}

func globToRe(glob string) string {
	var b strings.Builder
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++
				// "**/" or trailing "**": any number of segments
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++
					b.WriteString(`([^/]+/)*`)
				} else {
					b.WriteString(`.*`)
				}
			} else {
				b.WriteString(`[^/]*`)
			}
		case '?':
			b.WriteString(`[^/]`)
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}

// Match reports whether rel (slash-separated, no leading /) is excluded.
func (m *Matcher) Match(rel string) bool {
	rel = strings.Trim(rel, "/")
	for _, re := range m.res {
		if re.MatchString(rel) {
			return true
		}
	}
	return false
}

// MatchDir reports whether the directory rel can be pruned: everything
// under it would be excluded (e.g. "players" under pattern "players/**").
func (m *Matcher) MatchDir(rel string) bool {
	rel = strings.Trim(rel, "/") + "/"
	for _, re := range m.res {
		if re.MatchString(rel) {
			return true
		}
	}
	return false
}

// Under returns the path of p from dir, when p is dir or lies below it.
func Under(dir, p string) (rel string, ok bool) {
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// Tree says where a lib is on disk: all of it under Root, or, for a
// partial checkout, its directory CheckoutLib ("/usr/name") under
// CheckoutDir. With neither there is no lib.
type Tree struct {
	Root                     string
	CheckoutDir, CheckoutLib string
}

// Rel returns the path p as a file of the lib: relative to the lib root,
// slash-separated and with its extension ("players/x/room.c"). It is what
// exclude and override globs are matched against. The second result is
// false when p is not in the lib.
func (t Tree) Rel(p string) (string, bool) {
	if t.CheckoutDir != "" {
		if r, ok := Under(t.CheckoutDir, p); ok {
			return strings.TrimPrefix(path.Join(t.CheckoutLib, filepath.ToSlash(r)), "/"), true
		}
	}
	if t.Root == "" {
		return "", false
	}
	r, ok := Under(t.Root, p)
	return filepath.ToSlash(r), ok
}

// Path is where libFile (a lib path with its extension) would be on disk;
// false when no directory on disk holds that part of the lib.
func (t Tree) Path(libFile string) (string, bool) {
	if t.CheckoutDir != "" {
		if libFile == t.CheckoutLib {
			return t.CheckoutDir, true
		}
		if rest, ok := strings.CutPrefix(libFile, t.CheckoutLib+"/"); ok {
			return filepath.Join(t.CheckoutDir, filepath.FromSlash(rest)), true
		}
	}
	if t.Root == "" {
		return "", false
	}
	return filepath.Join(t.Root, filepath.FromSlash(strings.TrimPrefix(libFile, "/"))), true
}

// InCheckout reports whether libFile lies in the lib directory the
// checkout holds.
func (t Tree) InCheckout(libFile string) bool {
	return t.CheckoutDir != "" &&
		(libFile == t.CheckoutLib || strings.HasPrefix(libFile, t.CheckoutLib+"/"))
}

// Walk visits every file under dir (a directory on disk) that has one of
// the extensions exts and that exclude does not take. The globs are
// matched against lib-relative names, so they mean the same wherever the
// walk starts; a file outside the lib is never excluded. Walk stops with
// ctx's error when ctx is cancelled.
func (t Tree) Walk(ctx context.Context, fsys vfs.FS, dir string, exts []string, exclude *Matcher, visit func(path string)) error {
	err := fsys.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return fs.SkipAll
		}
		if err != nil {
			return nil
		}
		rel, inLib := t.Rel(p)
		if d.IsDir() {
			if p != dir && inLib && (exclude.Match(rel) || exclude.MatchDir(rel)) {
				return filepath.SkipDir
			}
			return nil
		}
		if slices.Contains(exts, filepath.Ext(p)) && !(inLib && exclude.Match(rel)) {
			visit(p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return ctx.Err()
}

// LibPath converts a root-relative file path to a lib-absolute object path
// ("/std/room" for "std/room.c"). The .c extension is dropped; .h files
// keep theirs.
func LibPath(rel string) string {
	rel = "/" + strings.Trim(filepath.ToSlash(rel), "/")
	return strings.TrimSuffix(rel, ".c")
}
