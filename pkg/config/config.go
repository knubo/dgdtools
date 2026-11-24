// Package config loads dgdtools.yml, the shared configuration for dgdfmt
// and dgdlint, and discovers the project a path belongs to. Every field is
// optional; the zero config gives working defaults. Everything
// mudlib-specific — include dirs, the auto object, call registries, lint
// conventions — lives here, never in the tools.
package config

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/token"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// FileName is the canonical config file name; AltFileName is also found.
const (
	FileName    = "dgdtools.yml"
	AltFileName = ".dgdtools.yml"
)

type Config struct {
	// Extends names config files to inherit from, resolved relative to
	// this file. Later entries win; this file wins over all of them.
	// Scalars and lists replace, maps merge.
	Extends StringList `yaml:"extends"`

	Dialect DialectCfg `yaml:"dialect"`
	// Root is the filesystem directory that is the lib's "/". Relative
	// paths are resolved against the config file's directory.
	Root string `yaml:"root"`
	// Profile names the mudlib profile to lint against: a name, looked
	// up as NAME.profile next to this file, in $DGDTOOLS_PROFILE_PATH and
	// in the user config directory (profile.UserDirs), or a file path. A profile supplies what
	// the checkout does not contain: the driver's settings, the shared
	// headers, and the signatures of the shared objects.
	Profile string `yaml:"profile"`
	// LibPath says where this checkout sits in the lib when it is not
	// the whole lib: the lib path of Root, e.g. "/players/drake".
	LibPath string `yaml:"lib_path"`
	// ProfileBuild are the inputs of `dgdlint profile build`, for whoever
	// has the whole lib. They also serve a whole-lib lint directly.
	ProfileBuild ProfileBuild `yaml:"profile_build"`

	Exclude []string `yaml:"exclude"`
	Format  Format   `yaml:"format"`
	Lint    Lint     `yaml:"lint"`

	// Dir is the directory the config was loaded from ("." for defaults).
	Dir string `yaml:"-"`
	// Path is the config file path ("" for defaults).
	Path string `yaml:"-"`
	// RootSet records whether the file gave a root.
	RootSet bool `yaml:"-"`
}

// StringList decodes from either a scalar or a sequence.
type StringList []string

func (s *StringList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*s = []string{n.Value}
		return nil
	}
	var l []string
	if err := n.Decode(&l); err != nil {
		return err
	}
	*s = l
	return nil
}

// ProfileBuild describes how to turn a whole lib into a profile.
type ProfileBuild struct {
	// Name of the profile (default: the lib root's directory name).
	Name string `yaml:"name"`
	// DriverConfig is the DGD driver's config file: include_dirs,
	// include_file, auto_object and typechecking are read from it.
	DriverConfig string `yaml:"driver_config"`
	// ShareMinOwners selects shared code by use: a file referenced (by
	// inherit, #include, or as a call/clone/find target) from at least
	// this many distinct owners goes into the profile. An owner is one
	// per-wizard tree (see Checkouts) or one other top-level directory.
	// Default 5; -1 turns inference off.
	ShareMinOwners int `yaml:"share_min_owners"`
	// Share are globs (lib-relative) added to what inference finds; a
	// glob taking a whole directory ("std/**") also marks it complete,
	// so that a path missing under it is known not to exist. The include
	// dirs, the force-included header, the auto object, and everything
	// shared files inherit or include are always added.
	Share []string `yaml:"share"`
	// Omit are globs never to put in a profile, even when referenced.
	Omit []string `yaml:"omit"`
	// Checkouts are the per-wizard trees as path patterns ("players/*").
	Checkouts []string `yaml:"checkouts"`
}

// DefaultShareMinOwners is the inference threshold when none is set.
const DefaultShareMinOwners = 5

type DialectCfg struct {
	SlashSlash *bool `yaml:"slash_slash"` // default true
	Closures   bool  `yaml:"closures"`    // default false
}

type Format struct {
	Indent        int    `yaml:"indent"`
	LineEndings   string `yaml:"line_endings"` // preserve | lf | crlf
	MaxBlankLines int    `yaml:"max_blank_lines"`
	// FunctionHeaders: "split" (KNF: name at column 0, the default) or
	// "joined" (whole header on one line; brace still on its own line).
	FunctionHeaders string `yaml:"function_headers"`
}

type Lint struct {
	// IncludeDirs are lib-absolute search paths for #include <...> and
	// "..." resolution (the driver config's include_dirs).
	IncludeDirs []string `yaml:"include_dirs"`
	// IncludeFile is force-included into every compile by the driver.
	IncludeFile string `yaml:"include_file"`
	// AutoObjects are lib paths implicitly inherited by every object.
	AutoObjects []string `yaml:"auto_objects"`
	// Typechecking is the driver's typechecking level (0, 1, 2). Rules
	// that only hold under typechecking stay silent when it is unknown.
	Typechecking *int `yaml:"typechecking"`

	// SpecifierMacros are empty-macro visibility markers ("public").
	SpecifierMacros []string `yaml:"specifier_macros"`
	// Defines are names treated as defined when choosing #ifdef branches.
	Defines []string `yaml:"defines"`
	// Macros overrides the class the parser derives for a macro:
	// type | specifier | statement | expression | opaque.
	Macros map[string]string `yaml:"macros"`

	// CallRegistry maps function names to the argument index (0-based)
	// holding a callback function name targeting this_object().
	CallRegistry map[string]int `yaml:"call_registry"`
	// AutosaveMarkers are function or macro names whose presence marks an
	// object as persisting via save_object/restore_object.
	AutosaveMarkers []string `yaml:"autosave_markers"`
	// ObjectRegistry maps path-taking functions to the argument index.
	ObjectRegistry map[string]int `yaml:"object_registry"`
	// RelativeObjectPaths says what an object path without a leading
	// slash is relative to: "root" (the default; DGD resolves object
	// names from the lib root, so "std/room" is /std/room) or "file"
	// (the directory of the referencing file, for libs whose auto object
	// rewrites paths that way).
	RelativeObjectPaths string `yaml:"relative_object_paths"`
	// VirtualPaths are lib-path globs served by virtual-object daemons.
	VirtualPaths []string `yaml:"virtual_paths"`
	// FormatRegistry maps scanf-style functions to their format argument.
	FormatRegistry map[string]int `yaml:"format_registry"`

	// Rules maps a rule name, code, or group to a severity
	// (off|info|warning|error) or to a settings mapping.
	Rules map[string]RuleSettings `yaml:"rules"`
	// Enable/Disable are the legacy on/off lists (additive over defaults).
	Enable  []string `yaml:"enable"`
	Disable []string `yaml:"disable"`
	// Overrides adjust rules for lib-relative path globs.
	Overrides []Override `yaml:"overrides"`
	// PathRules is the legacy form of Overrides (disable lists only).
	PathRules []PathRule `yaml:"path_rules"`

	FailOn string `yaml:"fail_on"` // info | warning | error
	// Baseline is the baseline file (default dgdlint-baseline.json next
	// to the config).
	Baseline string `yaml:"baseline"`
}

// RuleSettings configures one rule (or group). In YAML it is either a bare
// severity string or a mapping.
type RuleSettings struct {
	Severity   string   `yaml:"severity"` // off | info | warning | error
	Deny       []string `yaml:"deny"`     // raw-inherit-path
	Names      []string `yaml:"names"`    // lifecycle-chain
	MaxPerFile int      `yaml:"max_per_file"`
}

func (r *RuleSettings) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		r.Severity = n.Value
		return nil
	}
	type plain RuleSettings
	return n.Decode((*plain)(r))
}

type Override struct {
	Paths []string                `yaml:"paths"`
	Rules map[string]RuleSettings `yaml:"rules"`
}

type PathRule struct {
	Paths   []string `yaml:"paths"`
	Disable []string `yaml:"disable"`
}

// Default returns the zero configuration with Dir set.
func Default() *Config {
	return &Config{Dir: ".", Root: "."}
}

// Load reads a config file from disk.
func Load(path string) (*Config, error) { return LoadFS(vfs.OS{}, path) }

// LoadFS reads a config file, applying extends.
func LoadFS(fs vfs.FS, path string) (*Config, error) {
	c := &Config{}
	if err := loadInto(fs, path, c, map[string]bool{}); err != nil {
		return nil, err
	}
	c.Dir = filepath.Dir(path)
	c.Path = path
	c.RootSet = c.Root != ""
	if c.Root == "" {
		c.Root = "."
	}
	return c, nil
}

func loadInto(fs vfs.FS, path string, c *Config, seen map[string]bool) error {
	key := filepath.Clean(path)
	if seen[key] {
		return fmt.Errorf("%s: extends cycle", path)
	}
	seen[key] = true
	data, err := fs.ReadFile(path)
	if err != nil {
		return err
	}
	var head struct {
		Extends StringList `yaml:"extends"`
	}
	if err := yaml.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, base := range head.Extends {
		if !filepath.IsAbs(base) {
			base = filepath.Join(filepath.Dir(path), base)
		}
		if err := loadInto(fs, base, c, seen); err != nil {
			return fmt.Errorf("%s: extends: %w", path, err)
		}
		// Relative paths in a base are relative to the base.
		abs := func(p *string) {
			if *p != "" && !filepath.IsAbs(*p) {
				*p = filepath.Join(filepath.Dir(base), *p)
			}
		}
		abs(&c.Root)
		abs(&c.ProfileBuild.DriverConfig)
		if strings.ContainsAny(c.Profile, `/\`) || strings.HasSuffix(c.Profile, ".profile") {
			abs(&c.Profile)
		}
	}
	root, drv := c.Root, c.ProfileBuild.DriverConfig
	c.Root, c.ProfileBuild.DriverConfig = "", ""
	// Unknown keys are errors: a misspelled setting must not be silently
	// ignored (ruff, Biome and golangci-lint all reject them).
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && err != io.EOF {
		return fmt.Errorf("%s: %w", path, err)
	}
	if c.Root == "" {
		c.Root = root
	}
	if c.ProfileBuild.DriverConfig == "" {
		c.ProfileBuild.DriverConfig = drv
	}
	return nil
}

// Find searches dir (an absolute path) and its parents for a config file;
// returns Default() when none exists.
func Find(dir string) (*Config, error) {
	p := FindPath(vfs.OS{}, dir)
	if p == "" {
		return Default(), nil
	}
	return Load(p)
}

// FindPath returns the nearest config file at or above dir (an absolute
// path), or "".
func FindPath(fs vfs.FS, dir string) string {
	for {
		for _, name := range []string{FileName, AltFileName} {
			p := filepath.Join(dir, name)
			if fs.Exists(p) && !fs.IsDir(p) {
				return p
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// AbsRoot returns the lib root: Root as given when it is absolute, else
// relative to the config file's directory.
func (c *Config) AbsRoot() string {
	if filepath.IsAbs(c.Root) {
		return filepath.Clean(c.Root)
	}
	return filepath.Join(c.Dir, c.Root)
}

// TokenDialect converts to the lexer dialect.
func (c *Config) TokenDialect() token.Dialect {
	d := token.Dialect{SlashSlash: true, Closures: c.Dialect.Closures}
	if c.Dialect.SlashSlash != nil {
		d.SlashSlash = *c.Dialect.SlashSlash
	}
	return d
}

// Predefined returns the names that are defined before the first line of
// every file: the preprocessor's own and lint.defines.
func (c *Config) Predefined() map[string]bool {
	m := map[string]bool{}
	for _, d := range pp.Predefined {
		m[d] = true
	}
	for _, d := range c.Lint.Defines {
		m[d] = true
	}
	return m
}

// MacroClasses returns the macro classes the configuration gives:
// lint.specifier_macros (default: public) are empty macros, lint.macros
// names a class.
func (c *Config) MacroClasses() map[string]pp.Class {
	m := map[string]pp.Class{}
	specifiers := c.Lint.SpecifierMacros
	if len(specifiers) == 0 {
		specifiers = []string{"public"}
	}
	for _, s := range specifiers {
		m[s] = pp.ClassEmpty
	}
	for name, cls := range c.Lint.Macros {
		if class, ok := pp.ParseClass(cls); ok {
			m[name] = class
		}
	}
	return m
}

// RuleSettingsFor returns the per-rule settings (zero value if unset).
func (c *Config) RuleSettingsFor(rule string) RuleSettings {
	if c.Lint.Rules == nil {
		return RuleSettings{}
	}
	return c.Lint.Rules[rule]
}
