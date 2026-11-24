package index

import (
	"context"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/fileset"
	"github.com/mmcdole/dgdtools/pkg/pp"
)

// A file has two names. Its lib path is what LPC code calls it:
// "/std/room" for an object, "/include/std.h" for anything else. Its
// path is where its text is read from: a path on disk (under the lib
// root, or under the checkout for the lib directory the checkout holds)
// or the location of a profile's stub.

// withC is the lib path of an object's source file.
func withC(libPath string) string {
	if path.Ext(libPath) == "" {
		return libPath + ".c"
	}
	return libPath
}

// isStub reports whether p is the location of a profile stub.
func (l *lib) isStub(p string) bool {
	return l.lay.Stubs != "" && strings.HasPrefix(p, l.lay.Stubs+"/")
}

// clean normalizes a path given by a caller.
func (l *lib) clean(p string) string {
	if l.isStub(p) {
		return p
	}
	return filepath.Clean(p)
}

// diskPath is where libFile (a lib path with its extension) would be on
// disk; false when no directory on disk holds that part of the lib.
func (l *lib) diskPath(libFile string) (string, bool) {
	if l.lay.CheckoutDir != "" {
		if libFile == l.lay.CheckoutLib {
			return l.lay.CheckoutDir, true
		}
		if rest, ok := strings.CutPrefix(libFile, l.lay.CheckoutLib+"/"); ok {
			return filepath.Join(l.lay.CheckoutDir, filepath.FromSlash(rest)), true
		}
	}
	if l.lay.Root == "" {
		return "", false
	}
	return filepath.Join(l.lay.Root, filepath.FromSlash(strings.TrimPrefix(libFile, "/"))), true
}

// inCheckout reports whether libFile lies in the lib directory the
// checkout holds; nothing but the checkout speaks for those.
func (l *lib) inCheckout(libFile string) bool {
	return l.lay.CheckoutDir != "" &&
		(libFile == l.lay.CheckoutLib || strings.HasPrefix(libFile, l.lay.CheckoutLib+"/"))
}

// rel returns p relative to the lib root, slash-separated and with its
// extension ("players/x/room.c"); false when p is not a file of the lib.
func (l *lib) rel(p string) (string, bool) {
	if l.isStub(p) {
		return strings.TrimPrefix(p, l.lay.Stubs+"/"), true
	}
	under := func(dir string) (string, bool) {
		r, err := filepath.Rel(dir, p)
		if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return "", false
		}
		return filepath.ToSlash(r), true
	}
	if l.lay.CheckoutDir != "" {
		if r, ok := under(l.lay.CheckoutDir); ok {
			return strings.TrimPrefix(path.Join(l.lay.CheckoutLib, r), "/"), true
		}
	}
	if l.lay.Root == "" {
		return "", false
	}
	return under(l.lay.Root)
}

// LibPath maps a file's path to its lib path ("/std/room" for
// std/room.c), or "" when the file is not part of the lib.
func (l *lib) LibPath(p string) string {
	rel, ok := l.rel(p)
	if !ok {
		return ""
	}
	return fileset.LibPath(rel)
}

// Covers reports whether the index knows everything under libPath's
// location, so that "not found" there means "does not exist". With a whole
// lib that is the lib's completeness; with a partial checkout it is the
// checkout itself and the directories the profile captured completely.
func (l *lib) Covers(libPath string) bool {
	if !l.partial {
		return l.complete
	}
	if l.inCheckout(libPath) {
		return true
	}
	for _, c := range l.lay.Covered {
		if strings.HasPrefix(libPath, c) {
			// An object path has no extension; holes are file globs.
			rel := strings.TrimPrefix(libPath, "/")
			return !l.holes.Match(rel) && !l.holes.Match(rel+".c")
		}
	}
	return false
}

// IsVirtual reports whether libPath is served by a virtual-object daemon.
func (l *lib) IsVirtual(libPath string) bool {
	return l.virtual.Match(strings.TrimPrefix(libPath, "/"))
}

// ObjectPath turns an object path as written in fromLib into an absolute
// lib path. A path without a leading slash is resolved from the lib root,
// as the driver does, unless the config says paths are file-relative.
func (l *lib) ObjectPath(p, fromLib string) string {
	if l.cfg.Lint.RelativeObjectPaths == "file" {
		return NormalizeLibPath(p, fromLib)
	}
	return NormalizeLibPath(p, "/")
}

// NormalizeLibPath strips .c and makes p absolute relative to fromLib's
// directory.
func NormalizeLibPath(p, fromLib string) string {
	p = strings.TrimSuffix(strings.TrimSpace(p), ".c")
	if !strings.HasPrefix(p, "/") {
		p = path.Join(path.Dir(fromLib), p)
	}
	return path.Clean(p)
}

// excluded reports whether the config's exclude globs take the file or
// directory rel (lib-relative).
func (l *lib) excluded(rel string, dir bool) bool {
	return l.exclude.Match(rel) || (dir && l.exclude.MatchDir(rel))
}

// Walk visits every .c file under dir (a directory on disk) that the
// configuration does not exclude. It stops with ctx's error when ctx is
// cancelled.
func (l *lib) Walk(ctx context.Context, dir string, visit func(path string)) error {
	err := l.fs.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return fs.SkipAll
		}
		if err != nil {
			return nil
		}
		rel, inLib := l.rel(p)
		if d.IsDir() {
			if p != dir && inLib && l.excluded(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(p) == ".c" && !(inLib && l.excluded(rel, false)) {
			visit(p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return ctx.Err()
}

// scanRoot is the directory the whole-lib scan covers. With a partial
// checkout the checkout is all there is to scan: who else in the lib
// inherits these files cannot be known, and a wizard's own tree is taken
// to be self-contained.
func (l *lib) scanRoot() string {
	if l.partial {
		return l.lay.CheckoutDir
	}
	return l.lay.Root
}

// inScope reports whether Walk(scanRoot) visits p.
func (l *lib) inScope(p string) bool {
	root := l.scanRoot()
	if root == "" || l.isStub(p) || filepath.Ext(p) != ".c" {
		return false
	}
	below, err := filepath.Rel(root, p)
	if err != nil || below == ".." || strings.HasPrefix(below, ".."+string(filepath.Separator)) {
		return false
	}
	rel, ok := l.rel(p)
	if !ok {
		return false
	}
	if l.excluded(rel, false) {
		return false
	}
	// The directories between the scan root and the file.
	for n := strings.Count(filepath.ToSlash(below), "/"); n > 0; n-- {
		rel = path.Dir(rel)
		if l.excluded(rel, true) {
			return false
		}
	}
	return true
}

// includeProvablyMissing reports whether an unresolved include is known
// not to exist: every place the driver would look is covered.
func (l *lib) includeProvablyMissing(from string, inc pp.Include) bool {
	if !l.partial {
		return l.complete
	}
	var cands []string
	if !inc.Angle {
		if strings.HasPrefix(inc.Name, "/") {
			cands = append(cands, inc.Name)
		} else {
			rel, ok := l.rel(from)
			if !ok {
				return false
			}
			cands = append(cands, path.Join("/", path.Dir(rel), inc.Name))
		}
	}
	for _, dir := range l.cfg.Lint.IncludeDirs {
		cands = append(cands, path.Join(dir, inc.Name))
	}
	for _, c := range cands {
		if !l.Covers(path.Clean(c)) {
			return false
		}
	}
	return len(cands) > 0
}
