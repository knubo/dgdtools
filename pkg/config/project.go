package config

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/token"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// Project is a resolved lint target: a config (possibly the default),
// optionally a profile, and where the lib is.
//
// Three shapes exist:
//
//   - whole lib: Root is the lib on disk.
//   - partial checkout: the wizard has one subtree. Partial is set, Root
//     is a virtual directory that only the profile's stubs live under,
//     and CheckoutDir on disk is mounted at lib path CheckoutLib.
//   - no lib: Root is empty; only file-local analysis is possible.
type Project struct {
	Config *Config
	// ConfigPath is the config file used, "" when none.
	ConfigPath string
	// Root is the absolute lib root, or "" when no root is known.
	Root string
	// Profile is the mudlib profile in effect, or nil.
	Profile *profile.Profile
	// Partial, CheckoutDir and CheckoutLib describe a partial checkout.
	Partial                  bool
	CheckoutDir, CheckoutLib string
	// Base is the directory the project is anchored at: the config's
	// root, or the first linted path's directory. It is what a run with
	// no path arguments lints when there is no whole lib.
	Base string
	// Notes are human-readable remarks about how the project was resolved.
	Notes []string
}

// DiscoverOptions are the explicit overrides (CLI flags).
type DiscoverOptions struct {
	ConfigPath string // use this config file
	Root       string // use this lib root
	NoConfig   bool   // do not look for a config file
	Profile    string // use this profile (name or file)
	LibPath    string // the checkout's lib path
}

// Discover resolves the project for the given paths. The nearest config
// file at or above each path is used (the working directory when there are
// no paths); paths that resolve to different config files are an error,
// because a lint run has exactly one lib. A root is never guessed, and a
// profile is never assumed: without a config file or flags the project has
// neither.
func Discover(fs vfs.FS, paths []string, opt DiscoverOptions) (*Project, error) {
	prj := &Project{}
	starts := paths
	if len(starts) == 0 {
		starts = []string{"."}
	}
	startDir := func(p string) string {
		if !fs.IsDir(p) {
			p = filepath.Dir(p)
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return p
		}
		return abs
	}
	switch {
	case opt.ConfigPath != "":
		prj.ConfigPath = opt.ConfigPath
	case opt.NoConfig:
	default:
		for _, p := range starts {
			found, err := FindPath(fs, startDir(p))
			if err != nil {
				return nil, err
			}
			if found == "" {
				continue
			}
			if prj.ConfigPath != "" && prj.ConfigPath != found {
				return nil, fmt.Errorf("paths belong to different projects (%s and %s); lint them separately or pass --config",
					prj.ConfigPath, found)
			}
			prj.ConfigPath = found
		}
	}

	base := startDir(starts[0]) // where a config-less project is anchored
	if prj.ConfigPath != "" {
		c, err := LoadFS(fs, prj.ConfigPath)
		if err != nil {
			return nil, err
		}
		prj.Config = c
		prj.Root = c.AbsRoot()
		base = prj.Root
	} else {
		prj.Config = Default()
		prj.Config.Root = ""
	}
	cfg := prj.Config
	prj.Base = base

	// Profile: from the flag, else from the config.
	ref, rel := opt.Profile, "."
	if ref == "" {
		ref, rel = cfg.Profile, cfg.Dir
	}
	if ref != "" {
		p, err := profile.Load(fs, ref, rel)
		if err != nil {
			return nil, err
		}
		prj.Profile = p
		applyProfile(cfg, p)
	} else if dc := cfg.ProfileBuild.DriverConfig; dc != "" {
		// A whole lib with its build inputs at hand needs no profile.
		if !filepath.IsAbs(dc) {
			dc = filepath.Join(cfg.Dir, dc)
		}
		d, err := ReadDriverConfig(fs, dc)
		if err != nil {
			return nil, fmt.Errorf("profile_build.driver_config: %w", err)
		}
		d.Apply(cfg)
	}

	libPath := opt.LibPath
	if libPath == "" {
		libPath = cfg.LibPath
	}
	switch {
	case opt.Root != "":
		abs, err := filepath.Abs(opt.Root)
		if err != nil {
			return nil, err
		}
		prj.Root = abs
	case libPath != "":
		prj.mount(base, libPath)
	case prj.Profile != nil && !prj.wholeLib(fs):
		// A profile and something that is not the whole lib: a partial
		// checkout. Where it sits is read off its path when the lib's
		// per-wizard layout shows there (…/players/<name>/…).
		if dir, lib, ok := InferCheckout(base, prj.Profile.Checkouts); ok {
			prj.mount(dir, lib)
		} else {
			prj.Partial = true
			prj.Root = profile.VirtualRoot(prj.Profile.Name)
			prj.Notes = append(prj.Notes, fmt.Sprintf(
				"cannot tell where %s sits in the lib (no %s in its path): add `lib_path: /players/<you>` to dgdtools.yml to check references between your own files",
				base, strings.Join(prj.Profile.Checkouts, " or ")))
		}
	}
	if prj.Profile != nil {
		what := fmt.Sprintf("profile %s (%d stubs) from %s", prj.Profile.Name, len(prj.Profile.Files), prj.Profile.Path)
		if prj.CheckoutLib != "" {
			what += fmt.Sprintf("; %s is %s", prj.CheckoutDir, prj.CheckoutLib)
		}
		prj.Notes = append(prj.Notes, what)
	}
	if prj.Root != "" && !prj.Partial {
		if !fs.IsDir(prj.Root) {
			return nil, fmt.Errorf("lib root %s is not a directory", prj.Root)
		}
		cfg.Root = prj.Root
	}
	return prj, nil
}

// mount makes the project a partial checkout: dir on disk is lib path lib.
func (prj *Project) mount(dir, lib string) {
	prj.Partial = true
	prj.CheckoutDir = dir
	prj.CheckoutLib = "/" + strings.Trim(filepath.ToSlash(lib), "/")
	name := "none"
	if prj.Profile != nil {
		name = prj.Profile.Name
	}
	prj.Root = profile.VirtualRoot(name)
}

// wholeLib reports whether the project root holds the lib itself: the
// profile's force-included header (or first include dir) exists there.
func (prj *Project) wholeLib(fs vfs.FS) bool {
	if prj.Root == "" {
		return false
	}
	p := prj.Profile
	probe := p.IncludeFile
	if probe == "" && len(p.IncludeDirs) > 0 {
		probe = p.IncludeDirs[0]
	}
	if probe == "" && len(p.AutoObjects) > 0 {
		probe = p.AutoObjects[0] + ".c"
	}
	return probe != "" && fs.Exists(filepath.Join(prj.Root, filepath.FromSlash(probe)))
}

// InferCheckout recognizes a per-wizard tree in dir's path: with pattern
// "players/*", /home/me/mud/players/drake/rooms is the checkout
// /home/me/mud/players/drake at lib path /players/drake. Patterns are
// tried in order; within one, the outermost match wins.
func InferCheckout(dir string, patterns []string) (checkoutDir, lib string, ok bool) {
	segs := strings.Split(filepath.ToSlash(dir), "/")
	for _, pat := range patterns {
		want := strings.Split(strings.Trim(pat, "/"), "/")
		for i := 0; i+len(want) <= len(segs); i++ {
			match := true
			for j, w := range want {
				if segs[i+j] == "" || (w != "*" && w != segs[i+j]) {
					match = false
					break
				}
			}
			if match {
				return filepath.FromSlash(strings.Join(segs[:i+len(want)], "/")),
					"/" + strings.Join(segs[i:i+len(want)], "/"), true
			}
		}
	}
	return "", "", false
}

// applyProfile fills the config from a profile's manifest. Anything the
// config sets itself wins; registries are merged.
func applyProfile(cfg *Config, p *profile.Profile) {
	l := &cfg.Lint
	if len(l.IncludeDirs) == 0 {
		l.IncludeDirs = p.IncludeDirs
	}
	if l.IncludeFile == "" {
		l.IncludeFile = p.IncludeFile
	}
	if len(l.AutoObjects) == 0 {
		l.AutoObjects = p.AutoObjects
	}
	if l.Typechecking == nil {
		l.Typechecking = p.Dialect.Typechecking
	}
	if l.RelativeObjectPaths == "" {
		l.RelativeObjectPaths = p.RelativeObjectPaths
	}
	if cfg.Dialect.SlashSlash == nil {
		v := p.Dialect.SlashSlash
		cfg.Dialect.SlashSlash = &v
	}
	cfg.Dialect.Closures = cfg.Dialect.Closures || p.Dialect.Closures
	c := p.Conventions
	if len(l.SpecifierMacros) == 0 {
		l.SpecifierMacros = c.SpecifierMacros
	}
	if len(l.AutosaveMarkers) == 0 {
		l.AutosaveMarkers = c.AutosaveMarkers
	}
	if len(l.VirtualPaths) == 0 {
		l.VirtualPaths = c.VirtualPaths
	}
	l.Defines = append(append([]string{}, c.Defines...), l.Defines...)
	l.Defines = append(l.Defines, p.Dialect.Predefined...)
	mergeInt := func(dst *map[string]int, src map[string]int) {
		for k, v := range src {
			if *dst == nil {
				*dst = map[string]int{}
			}
			if _, set := (*dst)[k]; !set {
				(*dst)[k] = v
			}
		}
	}
	mergeInt(&l.CallRegistry, c.CallRegistry)
	mergeInt(&l.ObjectRegistry, c.ObjectRegistry)
	mergeInt(&l.FormatRegistry, c.FormatRegistry)
	for k, v := range c.Macros {
		if l.Macros == nil {
			l.Macros = map[string]string{}
		}
		if _, set := l.Macros[k]; !set {
			l.Macros[k] = v
		}
	}
}

// DriverConfig holds the values dgdlint needs from a DGD driver config.
type DriverConfig struct {
	Directory    string
	IncludeFile  string
	IncludeDirs  []string
	AutoObject   string
	Typechecking *int
}

// ReadDriverConfig parses a DGD driver configuration file
// (`name = value;` entries; values are numbers, strings, or ({ ... })).
func ReadDriverConfig(fs vfs.FS, path string) (*DriverConfig, error) {
	data, err := fs.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := lexer.Lex(path, data, lexer.Default)
	var sig []token.Token
	for _, t := range f.Tokens {
		if !t.Kind.IsTrivia() && t.Kind != token.EOF {
			sig = append(sig, t)
		}
	}
	str := func(t token.Token) string {
		s := string(f.Text(t))
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
		return strings.Trim(s, `"`)
	}
	dc := &DriverConfig{}
	for i := 0; i+2 < len(sig); i++ {
		if sig[i].Kind != token.Ident || sig[i+1].Kind != token.Assign {
			continue
		}
		name := string(f.Text(sig[i]))
		var vals []token.Token
		j := i + 2
		for ; j < len(sig) && sig[j].Kind != token.Semicolon; j++ {
			vals = append(vals, sig[j])
		}
		i = j
		if len(vals) == 0 {
			continue
		}
		switch name {
		case "directory":
			dc.Directory = str(vals[0])
		case "include_file":
			dc.IncludeFile = str(vals[0])
		case "auto_object":
			dc.AutoObject = str(vals[0])
		case "typechecking":
			if n, err := strconv.Atoi(string(f.Text(vals[0]))); err == nil {
				dc.Typechecking = &n
			}
		case "include_dirs":
			for _, v := range vals {
				if v.Kind == token.StringLit {
					dc.IncludeDirs = append(dc.IncludeDirs, str(v))
				}
			}
		}
	}
	return dc, nil
}

// Apply fills unset lint settings from the driver config. Explicit
// dgdtools.yml values always win.
func (dc *DriverConfig) Apply(cfg *Config) {
	l := &cfg.Lint
	if len(l.IncludeDirs) == 0 {
		l.IncludeDirs = dc.IncludeDirs
	}
	if l.IncludeFile == "" {
		l.IncludeFile = dc.IncludeFile
	}
	if len(l.AutoObjects) == 0 && dc.AutoObject != "" {
		l.AutoObjects = []string{dc.AutoObject}
	}
	if l.Typechecking == nil {
		l.Typechecking = dc.Typechecking
	}
}
