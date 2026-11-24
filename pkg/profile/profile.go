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
	"path"
	"path/filepath"
	"slices"
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

// Conventions are the lib's own habits that the tools need to be told.
// The same keys are the `conventions:` section of dgdtools.yml.
type Conventions struct {
	// SpecifierMacros are empty-macro visibility markers ("public").
	SpecifierMacros []string `json:"specifier_macros,omitempty" yaml:"specifier_macros"`
	// Macros overrides the class the parser derives for a macro:
	// type | specifier | statement | expression | opaque.
	Macros map[string]string `json:"macros,omitempty" yaml:"macros"`
	// Defines are names treated as defined when choosing #ifdef branches.
	Defines []string `json:"defines,omitempty" yaml:"defines"`
	// CallRegistry maps function names to the argument index (0-based)
	// holding a callback function name targeting this_object().
	CallRegistry map[string]int `json:"call_registry,omitempty" yaml:"call_registry"`
	// ObjectRegistry maps path-taking functions to the argument index.
	ObjectRegistry map[string]int `json:"object_registry,omitempty" yaml:"object_registry"`
	// FormatRegistry maps scanf-style functions to their format argument.
	FormatRegistry map[string]int `json:"format_registry,omitempty" yaml:"format_registry"`
	// AutosaveMarkers are function or macro names whose presence marks an
	// object as persisting via save_object/restore_object.
	AutosaveMarkers []string `json:"autosave_markers,omitempty" yaml:"autosave_markers"`
	// VirtualPaths are lib-path globs served by virtual-object daemons.
	VirtualPaths []string `json:"virtual_paths,omitempty" yaml:"virtual_paths"`
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

// PathEnv names the environment variable that lists extra directories to
// search for profiles by name (separated like PATH). A front end reads it
// and passes its value to UserDirs.
const PathEnv = "DGDTOOLS_PROFILE_PATH"

// UserDirs returns the directories, besides the one of the dgdtools.yml
// that names it, in which `profile: NAME` is looked up as NAME.profile:
//
//  1. each entry of pathList, the value of $DGDTOOLS_PROFILE_PATH;
//  2. userConfigDir/dgdtools/profiles, userConfigDir being
//     os.UserConfigDir() (~/.config on Linux,
//     ~/Library/Application Support on macOS, %AppData% on Windows);
//  3. home/.config/dgdtools/profiles, where that is a different
//     directory.
//
// The caller supplies all three (an empty one is skipped): this package
// reads no environment. No profile is built into dgdlint: a profile
// describes one particular mud and is published by that mud.
func UserDirs(pathList, userConfigDir, home string) []string {
	var dirs []string
	add := func(d string) {
		if d != "" && !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	for _, d := range filepath.SplitList(pathList) {
		add(d)
	}
	if userConfigDir != "" {
		add(filepath.Join(userConfigDir, "dgdtools", "profiles"))
	}
	if home != "" {
		add(filepath.Join(home, ".config", "dgdtools", "profiles"))
	}
	return dirs
}

// Resolve turns a profile reference into a file path. A reference with a
// path separator or ending in ".profile" is a path (relative to
// configDir); anything else is a name, looked up as NAME.profile in
// configDir and then in dirs (see UserDirs). configDir is absolute.
func Resolve(fsys vfs.FS, ref, configDir string, dirs []string) (string, error) {
	if strings.ContainsAny(ref, `/\\`) || strings.HasSuffix(ref, ".profile") {
		if !filepath.IsAbs(ref) {
			ref = filepath.Join(configDir, ref)
		}
		if !fsys.Exists(ref) {
			return "", fmt.Errorf("profile file %s does not exist", ref)
		}
		return ref, nil
	}
	search := []string{configDir}
	for _, d := range dirs {
		if !slices.Contains(search, d) {
			search = append(search, d)
		}
	}
	for _, d := range search {
		if p := filepath.Join(d, ref+".profile"); fsys.Exists(p) && !fsys.IsDir(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("profile %q not found: no %s.profile in\n  %s\n(get the profile from your mud and put it in one of these, list its directory in $%s, or give its path)",
		ref, ref, strings.Join(search, "\n  "), PathEnv)
}

// Load resolves a profile reference (see Resolve) and reads the profile.
func Load(fsys vfs.FS, ref, configDir string, dirs []string) (*Profile, error) {
	file, err := Resolve(fsys, ref, configDir, dirs)
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

// Scheme is the URI scheme of stub locations.
const Scheme = "dgdlint-profile"

// Location returns the location prefix of a profile's stubs. A stub is
// not a file on disk and is not named like one: the stub for lib file
// "/std/room.c" of profile "viking" is at
// "dgdlint-profile://viking/std/room.c". That is the path FS answers
// for, the path diagnostics name when a declaration comes from a stub,
// and the URI of the read-only document an editor shows for it.
func Location(name string) string { return Scheme + "://" + name }

// SplitLocation splits a stub location into the profile's name and the
// lib file; false when p is not a stub location.
func SplitLocation(p string) (name, libFile string, ok bool) {
	rest, ok := strings.CutPrefix(p, Scheme+"://")
	if !ok {
		return "", "", false
	}
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		return rest, "/", true
	}
	return rest[:i], rest[i:], true
}

// FS adds a profile's stubs to a file system: paths under the profile's
// Location are answered from the profile, everything else by the base.
type FS struct {
	base   vfs.FS
	prefix string
	p      *Profile
	dirs   map[string]bool
}

// NewFS returns base extended with the profile's stubs.
func NewFS(base vfs.FS, p *Profile) *FS {
	f := &FS{base: base, prefix: Location(p.Name), p: p, dirs: map[string]bool{"/": true}}
	for file := range p.Files {
		for d := path.Dir(file); d != "/" && !f.dirs[d]; d = path.Dir(d) {
			f.dirs[d] = true
		}
	}
	return f
}

// Location returns the location prefix the stubs are answered under.
func (f *FS) Location() string { return f.prefix }

// stub returns the lib file a stub location names.
func (f *FS) stub(p string) (string, bool) {
	rest, ok := strings.CutPrefix(p, f.prefix)
	switch {
	case !ok:
		return "", false
	case rest == "":
		return "/", true
	case rest[0] == '/':
		return rest, true
	}
	return "", false
}

func (f *FS) ReadFile(p string) ([]byte, error) {
	lib, ok := f.stub(p)
	if !ok {
		return f.base.ReadFile(p)
	}
	if s, ok := f.p.Files[lib]; ok {
		return []byte(s), nil
	}
	return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
}

func (f *FS) Exists(p string) bool {
	lib, ok := f.stub(p)
	if !ok {
		return f.base.Exists(p)
	}
	_, isFile := f.p.Files[lib]
	return isFile || f.dirs[lib]
}

func (f *FS) IsDir(p string) bool {
	lib, ok := f.stub(p)
	if !ok {
		return f.base.IsDir(p)
	}
	return f.dirs[lib]
}

// WalkDir walks the base: stubs are consulted, never linted.
func (f *FS) WalkDir(root string, fn fs.WalkDirFunc) error { return f.base.WalkDir(root, fn) }
