// Package profile is a mudlib profile: everything dgdlint needs to know
// about a lib that a wizard's own checkout does not contain.
//
// A profile is to a mudlib what lib.d.ts is to a JavaScript runtime or
// typeshed is to Python's standard library: declarations without
// implementations. It holds
//
//   - a manifest: the dialect (driver, build flags, typechecking), the
//     driver's include dirs / force-included header / auto object, the
//     lib's conventions (registries, macro classes, per-wizard layout),
//     and which lib directories the profile captures completely;
//   - stub files: for each shared header its #includes and #defines, and
//     for each shared object its inherit statements and the signatures of
//     its functions and variables. No function bodies, no comments.
//
// Stubs are ordinary source in the profile's dialect, so the same lexer,
// parser and index that read real files read them; a real file on disk
// always wins over a stub.
//
// The file format is deterministic (same lib, same bytes) and compact: a
// gzip stream of a header line, the manifest as JSON, and the stubs in
// path order.
package profile

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// Format is the profile file format version.
const Format = 1

// FileName is the default name of a locally built profile.
const FileName = "dgdlint.profile"

const magic = "dgdlint-profile"

// Dialect describes the language a lib is written in. Only driver "dgd"
// is implemented; the field exists so a profile says what it is, and so
// other LPC drivers can be added without changing the format.
type Dialect struct {
	Language     string   `json:"language"`               // "lpc"
	Driver       string   `json:"driver"`                 // "dgd"
	Version      string   `json:"version,omitempty"`      // informational, e.g. "1.7"
	SlashSlash   bool     `json:"slash_slash"`            // // comments
	Closures     bool     `json:"closures"`               // function-pointer syntax
	Typechecking *int     `json:"typechecking,omitempty"` // driver typechecking level
	Predefined   []string `json:"predefined,omitempty"`   // macros the driver defines
}

// Conventions are the lib's own habits that rules need to be told.
type Conventions struct {
	SpecifierMacros []string          `json:"specifier_macros,omitempty"`
	Macros          map[string]string `json:"macros,omitempty"`
	Defines         []string          `json:"defines,omitempty"`
	CallRegistry    map[string]int    `json:"call_registry,omitempty"`
	ObjectRegistry  map[string]int    `json:"object_registry,omitempty"`
	FormatRegistry  map[string]int    `json:"format_registry,omitempty"`
	AutosaveMarkers []string          `json:"autosave_markers,omitempty"`
	VirtualPaths    []string          `json:"virtual_paths,omitempty"`
}

// Manifest is the non-source part of a profile.
type Manifest struct {
	Format  int     `json:"format"`
	Name    string  `json:"name"`
	Dialect Dialect `json:"dialect"`

	IncludeDirs         []string `json:"include_dirs,omitempty"`
	IncludeFile         string   `json:"include_file,omitempty"`
	AutoObjects         []string `json:"auto_objects,omitempty"`
	RelativeObjectPaths string   `json:"relative_object_paths,omitempty"`

	// Covered are lib directory prefixes ("/std/") the profile captures
	// completely: a path under one that has no stub does not exist.
	Covered []string `json:"covered,omitempty"`
	// Holes are globs (lib-relative) of files that exist in the lib but
	// were left out of the profile on purpose (the build's omit and
	// exclude patterns). Nothing is known about a path matching one, even
	// under a covered directory.
	Holes []string `json:"holes,omitempty"`
	// Checkouts are the lib's per-wizard trees as path patterns
	// ("players/*"), used to recognize where a partial checkout sits.
	Checkouts []string `json:"checkouts,omitempty"`

	Conventions Conventions `json:"conventions"`
}

// Profile is a loaded profile.
type Profile struct {
	Manifest
	// Files maps a lib path with extension ("/std/room.c") to its stub.
	Files map[string]string
	// Path is the file the profile was loaded from, "" when built in
	// memory.
	Path string
}

// Marshal renders the profile. The output depends only on the profile's
// content.
func (p *Profile) Marshal() ([]byte, error) {
	var raw bytes.Buffer
	fmt.Fprintf(&raw, "%s %d\n", magic, Format)
	m := p.Manifest
	m.Format = Format
	js, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return nil, err
	}
	raw.Write(js)
	raw.WriteByte('\n')
	paths := make([]string, 0, len(p.Files))
	for f := range p.Files {
		paths = append(paths, f)
	}
	sort.Strings(paths)
	for _, f := range paths {
		fmt.Fprintf(&raw, "=== %s\n%s", f, p.Files[f])
		if !strings.HasSuffix(p.Files[f], "\n") {
			raw.WriteByte('\n')
		}
	}
	var out bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if _, err := zw.Write(raw.Bytes()); err != nil { // no name, no mtime: deterministic
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Parse reads a profile (gzip-compressed or plain).
func Parse(data []byte) (*Profile, error) {
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if data, err = io.ReadAll(zr); err != nil {
			return nil, err
		}
	}
	head, rest, _ := bytes.Cut(data, []byte("\n"))
	var format int
	if _, err := fmt.Sscanf(string(head), magic+" %d", &format); err != nil {
		return nil, fmt.Errorf("not a dgdlint profile")
	}
	if format != Format {
		return nil, fmt.Errorf("profile format %d is not supported by this dgdlint (wants %d); rebuild the profile", format, Format)
	}
	p := &Profile{Files: map[string]string{}}
	body := rest
	sep := []byte("\n=== ")
	if i := bytes.Index(rest, sep); i >= 0 {
		body, rest = rest[:i+1], rest[i+1:]
	} else {
		rest = nil
	}
	if err := json.Unmarshal(body, &p.Manifest); err != nil {
		return nil, fmt.Errorf("profile manifest: %w", err)
	}
	if d := p.Dialect.Driver; d != "dgd" {
		return nil, fmt.Errorf("profile %q is for driver %q; only the dgd dialect is implemented", p.Name, d)
	}
	for len(rest) > 0 {
		rest = bytes.TrimPrefix(rest, []byte("=== "))
		name, after, _ := bytes.Cut(rest, []byte("\n"))
		content := after
		if i := bytes.Index(after, sep); i >= 0 {
			content, rest = after[:i+1], after[i+1:]
		} else {
			rest = nil
		}
		p.Files[string(name)] = string(content)
	}
	return p, nil
}

// PathEnv names the environment variable listing extra directories to
// search for profiles by name (separated like PATH).
const PathEnv = "DGDTOOLS_PROFILE_PATH"

// SearchDirs returns, in order, the directories in which `profile: NAME`
// is looked up as NAME.profile:
//
//  1. configDir, the directory of the dgdtools.yml that names the profile
//     (the working directory when the name comes from a flag);
//  2. each entry of $DGDTOOLS_PROFILE_PATH;
//  3. the user configuration directory, os.UserConfigDir()/dgdtools/profiles
//     (~/.config/dgdtools/profiles on Linux,
//     ~/Library/Application Support/dgdtools/profiles on macOS,
//     %AppData%\dgdtools\profiles on Windows);
//  4. ~/.config/dgdtools/profiles, where that is a different directory.
//
// No profile is built into dgdlint: a profile describes one particular
// mud and is published by that mud.
func SearchDirs(configDir string) []string {
	var dirs []string
	add := func(d string) {
		if d == "" {
			return
		}
		for _, have := range dirs {
			if have == d {
				return
			}
		}
		dirs = append(dirs, d)
	}
	if configDir == "" {
		configDir = "."
	}
	if abs, err := filepath.Abs(configDir); err == nil {
		configDir = abs
	}
	add(configDir)
	for _, d := range filepath.SplitList(os.Getenv(PathEnv)) {
		add(d)
	}
	if d, err := os.UserConfigDir(); err == nil {
		add(filepath.Join(d, "dgdtools", "profiles"))
	}
	if h, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(h, ".config", "dgdtools", "profiles"))
	}
	return dirs
}

// Resolve turns a profile reference into a file path. A reference with a
// path separator or ending in ".profile" is a path (relative to
// configDir); anything else is a name looked up in SearchDirs.
func Resolve(fsys vfs.FS, ref, configDir string) (string, error) {
	if strings.ContainsAny(ref, `/\\`) || strings.HasSuffix(ref, ".profile") {
		if !filepath.IsAbs(ref) {
			ref = filepath.Join(configDir, ref)
		}
		if !fsys.Exists(ref) {
			return "", fmt.Errorf("profile file %s does not exist", ref)
		}
		return ref, nil
	}
	dirs := SearchDirs(configDir)
	for _, d := range dirs {
		if p := filepath.Join(d, ref+".profile"); fsys.Exists(p) && !fsys.IsDir(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("profile %q not found: no %s.profile in\n  %s\n(get the profile from your mud and put it in one of these, list its directory in $%s, or give its path)",
		ref, ref, strings.Join(dirs, "\n  "), PathEnv)
}

// Load resolves a profile reference (see Resolve) and reads the profile.
func Load(fsys vfs.FS, ref, configDir string) (*Profile, error) {
	file, err := Resolve(fsys, ref, configDir)
	if err != nil {
		return nil, err
	}
	data, err := fsys.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("profile: %w", err)
	}
	p, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	p.Path = file
	return p, nil
}

// VirtualRoot is the lib root used when there is no lib on disk: stubs
// live under it, and nothing real ever does.
func VirtualRoot(name string) string {
	return filepath.Join(string(filepath.Separator)+".dgdlint-profile", name)
}

// FS lays a profile's stubs under a lib root: a path below root that the
// base file system does not have is answered from the profile. Real files
// always win.
type FS struct {
	Base vfs.FS
	Root string
	P    *Profile
	dirs map[string]bool
}

// NewFS returns the union of base and the profile's stubs at root.
func NewFS(base vfs.FS, root string, p *Profile) *FS {
	f := &FS{Base: base, Root: root, P: p, dirs: map[string]bool{"/": true}}
	for file := range p.Files {
		for d := path.Dir(file); d != "/" && !f.dirs[d]; d = path.Dir(d) {
			f.dirs[d] = true
		}
	}
	return f
}

func (f *FS) lib(p string) (string, bool) {
	rel, err := filepath.Rel(f.Root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return "/", true
	}
	return "/" + filepath.ToSlash(rel), true
}

func (f *FS) ReadFile(p string) ([]byte, error) {
	if f.Base.Exists(p) {
		return f.Base.ReadFile(p)
	}
	if lib, ok := f.lib(p); ok {
		if s, ok := f.P.Files[lib]; ok {
			return []byte(s), nil
		}
	}
	return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
}

func (f *FS) Exists(p string) bool {
	if f.Base.Exists(p) {
		return true
	}
	lib, ok := f.lib(p)
	if !ok {
		return false
	}
	_, isFile := f.P.Files[lib]
	return isFile || f.dirs[lib]
}

func (f *FS) IsDir(p string) bool {
	if f.Base.IsDir(p) {
		return true
	}
	lib, ok := f.lib(p)
	return ok && f.dirs[lib]
}

// WalkDir walks real files only: stubs are consulted, never linted.
func (f *FS) WalkDir(root string, fn fs.WalkDirFunc) error { return f.Base.WalkDir(root, fn) }

// IsStub reports whether p is answered from the profile.
func (f *FS) IsStub(p string) bool {
	if f.Base.Exists(p) {
		return false
	}
	lib, ok := f.lib(p)
	if !ok {
		return false
	}
	_, isFile := f.P.Files[lib]
	return isFile
}
