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
	return ok || o.base.Exists(path)
}

func (o *Overlay) IsDir(path string) bool { return o.base.IsDir(path) }

// WalkDir walks the base tree. Shadowed files that do not exist in the
// base are not visited: an unsaved new file is linted by naming it.
func (o *Overlay) WalkDir(root string, fn fs.WalkDirFunc) error { return o.base.WalkDir(root, fn) }

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
		if err := fn(p, memEntry(p), nil); err != nil && err != fs.SkipDir {
			return err
		}
	}
	return nil
}

type memEntry string

func (e memEntry) Name() string               { return filepath.Base(string(e)) }
func (e memEntry) IsDir() bool                { return false }
func (e memEntry) Type() fs.FileMode          { return 0 }
func (e memEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrInvalid }
