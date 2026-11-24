// Package vfs abstracts file access so that every dgdtools analysis can run
// against something other than the disk: an editor's unsaved buffers, a
// test fixture held in memory, or a snapshot. Analyses never call the os
// package directly.
package vfs

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// FS is the read-only file access the tools need. Paths are native
// filesystem paths (absolute paths are recommended).
type FS interface {
	ReadFile(path string) ([]byte, error)
	// Exists reports whether path names a file or directory.
	Exists(path string) bool
	// IsDir reports whether path names a directory.
	IsDir(path string) bool
	// WalkDir walks the tree rooted at root like filepath.WalkDir.
	WalkDir(root string, fn fs.WalkDirFunc) error
}

// OS is the real filesystem.
type OS struct{}

func (OS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (OS) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (OS) IsDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func (OS) WalkDir(root string, fn fs.WalkDirFunc) error { return filepath.WalkDir(root, fn) }

// Overlay shadows a base FS with in-memory file contents. It is safe for
// concurrent use. Set and Delete are how an editor integration feeds
// unsaved buffers to the linter; the caller is responsible for telling the
// linter which paths changed (lint.Linter.Invalidate).
type Overlay struct {
	base FS
	mu   sync.RWMutex
	m    map[string][]byte
}

// NewOverlay returns an overlay over base.
func NewOverlay(base FS) *Overlay {
	return &Overlay{base: base, m: map[string][]byte{}}
}

// Set makes path read as content, whether or not it exists in the base.
func (o *Overlay) Set(path string, content []byte) {
	o.mu.Lock()
	o.m[filepath.Clean(path)] = content
	o.mu.Unlock()
}

// Delete removes the shadow for path; the base content shows through again.
func (o *Overlay) Delete(path string) {
	o.mu.Lock()
	delete(o.m, filepath.Clean(path))
	o.mu.Unlock()
}

func (o *Overlay) ReadFile(path string) ([]byte, error) {
	o.mu.RLock()
	b, ok := o.m[filepath.Clean(path)]
	o.mu.RUnlock()
	if ok {
		return b, nil
	}
	return o.base.ReadFile(path)
}

func (o *Overlay) Exists(path string) bool {
	o.mu.RLock()
	_, ok := o.m[filepath.Clean(path)]
	o.mu.RUnlock()
	return ok || o.base.Exists(path) || o.shadowDir(path)
}

// IsDir reports whether path is a directory in the base or holds a
// shadowed file.
func (o *Overlay) IsDir(path string) bool { return o.base.IsDir(path) || o.shadowDir(path) }

// shadowDir reports whether some shadowed file lies below path.
func (o *Overlay) shadowDir(path string) bool {
	prefix := strings.TrimSuffix(filepath.Clean(path), string(filepath.Separator)) + string(filepath.Separator)
	o.mu.RLock()
	defer o.mu.RUnlock()
	for p := range o.m {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// WalkDir walks the base tree and then visits the shadowed files the base
// does not have (an unsaved new file), each after the directories that
// lead to it and do not exist in the base either. A directory fn skipped
// is skipped for shadowed files too.
func (o *Overlay) WalkDir(root string, fn fs.WalkDirFunc) error {
	root = filepath.Clean(root)
	var extra []string
	o.mu.RLock()
	for p := range o.m {
		if p == root || strings.HasPrefix(p, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator)) {
			extra = append(extra, p)
		}
	}
	o.mu.RUnlock()
	if len(extra) == 0 {
		return o.base.WalkDir(root, fn)
	}
	sort.Strings(extra)

	skipped := map[string]bool{} // directories fn asked to skip
	stop := false
	visit := func(p string, d fs.DirEntry, err error) error {
		switch r := fn(p, d, err); {
		case r == fs.SkipAll:
			stop = true
		case r == fs.SkipDir && d != nil && d.IsDir():
			skipped[filepath.Clean(p)] = true
		case r == fs.SkipDir:
			skipped[filepath.Dir(filepath.Clean(p))] = true
		case r != nil:
			return r
		}
		return nil
	}
	if o.base.Exists(root) {
		err := o.base.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err := visit(p, d, err); err != nil {
				return err
			}
			switch {
			case stop:
				return fs.SkipAll
			case skipped[filepath.Clean(p)] || (d != nil && !d.IsDir() && skipped[filepath.Dir(filepath.Clean(p))]):
				return fs.SkipDir
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	announced := map[string]bool{} // directories only the overlay has, already visited
	for _, p := range extra {
		if o.base.Exists(p) {
			continue // the base walk visited it
		}
		// The directories from root down to p, outermost first.
		var dirs []string
		for d := p; d != root; {
			d = filepath.Dir(d)
			dirs = append([]string{d}, dirs...)
		}
		pruned := false
		for _, d := range dirs {
			if !skipped[d] && !announced[d] && !o.base.Exists(d) {
				announced[d] = true
				if err := visit(d, entry{name: filepath.Base(d), dir: true}, nil); err != nil {
					return err
				}
			}
			if pruned = skipped[d]; pruned || stop {
				break
			}
		}
		if stop {
			return nil
		}
		if pruned {
			continue
		}
		if err := visit(p, entry{name: filepath.Base(p)}, nil); err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	return nil
}

// Mem is an in-memory FS for tests and embedding: a map from slash-separated
// absolute path to content.
type Mem map[string]string

func (m Mem) ReadFile(path string) ([]byte, error) {
	if s, ok := m[filepath.ToSlash(path)]; ok {
		return []byte(s), nil
	}
	return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
}

func (m Mem) Exists(path string) bool {
	path = filepath.ToSlash(path)
	if _, ok := m[path]; ok {
		return true
	}
	return m.IsDir(path)
}

func (m Mem) IsDir(path string) bool {
	prefix := strings.TrimSuffix(filepath.ToSlash(path), "/") + "/"
	for p := range m {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

func (m Mem) WalkDir(root string, fn fs.WalkDirFunc) error {
	root = strings.TrimSuffix(filepath.ToSlash(root), "/")
	var paths []string
	for p := range m {
		if p == root || strings.HasPrefix(p, root+"/") {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := fn(p, entry{name: filepath.Base(p)}, nil); err != nil && err != fs.SkipDir {
			return err
		}
	}
	return nil
}

// entry is a directory entry of a file system that has no real ones.
type entry struct {
	name string
	dir  bool
}

func (e entry) Name() string { return e.name }
func (e entry) IsDir() bool  { return e.dir }
func (e entry) Type() fs.FileMode {
	if e.dir {
		return fs.ModeDir
	}
	return 0
}
func (e entry) Info() (fs.FileInfo, error) { return nil, fs.ErrInvalid }
