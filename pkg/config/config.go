// Package config loads dgdtools.yml, the one configuration file of the
// dgdtools, discovers the project a path belongs to, and resolves the
// settings each file is formatted and linted by. Every field is optional;
// the zero config gives working defaults. Everything mudlib-specific
// (include dirs, the auto object, call registries, conventions) lives
// here or in a profile, never in the tools.
//
// A file is one of two things (docs/design/config.md). A root config
// defines a project: its top-level keys say what the project is, and its
// lint: and format: sections are the project's settings. A settings scope
// (`root: false`) holds only lint: and format:, and adjusts them for the
// files under its directory.
package config

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/token"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// FileName is the canonical config file name; AltFileName is also found.
const (
	FileName    = "dgdtools.yml"
	AltFileName = ".dgdtools.yml"
)

type Config struct {
	// Extends names config files to start from, resolved relative to
	// this file. Later entries win; this file wins over all of them.
	// Scalars and lists replace, maps merge.
	Extends StringList `yaml:"extends"`

	// Project keys: what the project is. They are the facts a profile's
	// manifest carries, under the same names; what is set here wins over
	// the profile. A settings scope has none of them.

	// Root is the filesystem directory that is the lib's "/" (default:
	// the config file's directory). Relative paths are resolved against
	// the config file's directory. In YAML the key also takes the value
	// false, which makes the file a settings scope; see Scope.
	Root string `yaml:"root"`
	// Profile names the mudlib profile to work against: a name, looked
	// up as NAME.profile next to this file, in $DGDTOOLS_PROFILE_PATH and
	// in the user config directory (profile.UserDirs), or a file path. A
	// profile supplies what the checkout does not contain: the driver's
	// settings, the shared headers, and the signatures of the shared
	// objects.
	Profile string `yaml:"profile"`
	// LibPath says where this checkout sits in the lib when it is not
	// the whole lib: the lib path of Root, e.g. "/users/name".
	LibPath string `yaml:"lib_path"`
	// Exclude are globs (lib-relative) of files directory walks skip.
	Exclude []string `yaml:"exclude"`

	Dialect DialectCfg `yaml:"dialect"`
	// IncludeDirs are lib-absolute search paths for #include <...> and
	// "..." resolution (the driver config's include_dirs).
	IncludeDirs []string `yaml:"include_dirs"`
	// IncludeFile is force-included into every compile by the driver.
	IncludeFile string `yaml:"include_file"`
	// AutoObjects are lib paths implicitly inherited by every object.
	AutoObjects []string `yaml:"auto_objects"`
	// Create is the function a new object is initialized by: the one the
	// driver calls (the driver config's create), or, in a lib whose auto
	// object receives that call and passes it on under another name, the
	// one the lib's objects define.
	Create string `yaml:"create"`
	// RelativeObjectPaths says what an object path without a leading
	// slash is relative to. The driver leaves that to the lib (its driver
	// object resolves every inherit and every call_other by path):
	// ObjectPathsFromRoot, the default and what the driver's own
	// functions do, or ObjectPathsFromFile, the directory of the file
	// that says it.
	RelativeObjectPaths string `yaml:"relative_object_paths"`
	// Conventions are the lib's own habits: macro classes, registries.
	Conventions profile.Conventions `yaml:"conventions"`
	// ProfileBuild are the inputs of `dgdtools profile build`, for
	// whoever has the whole lib. They also serve a whole-lib lint
	// directly.
	ProfileBuild ProfileBuild `yaml:"profile_build"`

	// Settings: how files are formatted and linted. A settings scope may
	// set them for its directory; see Project.Settings.

	Format Format `yaml:"format"`
	Lint   Lint   `yaml:"lint"`

	// Scope is set by `root: false`: the file is a settings scope, not a
	// project's root config.
	Scope bool `yaml:"-"`
	// Dir is the directory the config was loaded from, and Path the config
	// file; both are empty for a project without one.
	Dir  string `yaml:"-"`
	Path string `yaml:"-"`
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
	// include_file, auto_object, create and typechecking are read from it.
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
	// Typechecking is the driver's typechecking level (0, 1, 2). Rules
	// that only hold under typechecking stay silent when it is unknown.
	Typechecking *int `yaml:"typechecking"`
}

// Lint are the linter's settings.
type Lint struct {
	// Rules maps a rule name, code, or group to its settings.
	Rules map[string]RuleSettings `yaml:"rules"`
	// Overrides adjust rules for path globs: lib-relative in a root
	// config, relative to its directory in a settings scope.
	Overrides []Override `yaml:"overrides"`

	// FailOn and Baseline describe a run, not a file: root config only.
	FailOn string `yaml:"fail_on"` // info | warning | error
	// Baseline is the baseline file (default lint.BaselineFileName next
	// to the config).
	Baseline string `yaml:"baseline"`
}

// RuleSettings is what a configuration says about one rule, or about a
// group of rules. In YAML it is a severity, or a mapping that may give a
// severity, max_per_file, and the options of the rule.
type RuleSettings struct {
	// Severity is off, info, warning or error; a severity turns the rule
	// on. Empty: the entry says nothing about it.
	Severity string
	// MaxPerFile reports at most that many findings of a rule in one
	// file; 0 says nothing about it.
	MaxPerFile int
	// Options are the rule's own settings, by key. Which keys a rule has,
	// and what values they take, is the rule's to say: the linter checks
	// them (lint.Rule.Options).
	Options map[string]any
	// Line is the line of the entry in its file, 0 when it is not from
	// one.
	Line int
}

func (r *RuleSettings) UnmarshalYAML(n *yaml.Node) error {
	*r = RuleSettings{Line: n.Line}
	switch n.Kind {
	case yaml.ScalarNode:
		r.Severity = n.Value
		return nil
	case yaml.MappingNode:
	default:
		return fmt.Errorf("line %d: a rule is set to a severity (off, info, warning, error) or to a mapping", n.Line)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		switch key.Value {
		case "severity":
			if val.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: severity: want off, info, warning or error", val.Line)
			}
			r.Severity = val.Value
		case "max_per_file":
			if err := val.Decode(&r.MaxPerFile); err != nil || val.Tag != "!!int" {
				return fmt.Errorf("line %d: max_per_file: want a number", val.Line)
			}
		default:
			var v any
			if err := val.Decode(&v); err != nil {
				return at(val, err)
			}
			if r.Options == nil {
				r.Options = map[string]any{}
			}
			r.Options[key.Value] = v
		}
	}
	return nil
}

// Override is rule settings for the files that match some globs.
type Override struct {
	Paths []string                `yaml:"paths"`
	Rules map[string]RuleSettings `yaml:"rules"`
}

// Load reads and validates a config file, applying extends.
func Load(fs vfs.FS, path string) (*Config, error) {
	c := &Config{}
	if err := loadInto(fs, path, c, map[string]bool{}); err != nil {
		return nil, err
	}
	c.Dir = filepath.Dir(path)
	c.Path = path
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// header is what is read of a config file before the rest: what it
// extends, and whether it is a settings scope.
type header struct {
	Extends StringList `yaml:"extends"`
	Root    yaml.Node  `yaml:"root"`
}

// scope reports whether the file says `root: false`. A boolean is told
// from a directory named like one by how it is written: `root: "false"`
// is a path.
func (h *header) scope() (bool, error) {
	if h.Root.Kind != yaml.ScalarNode || h.Root.Tag != "!!bool" {
		return false, nil
	}
	var v bool
	if err := h.Root.Decode(&v); err != nil {
		return false, err
	}
	if v {
		return false, fmt.Errorf("line %d: root: true is not a setting: a dgdtools.yml is a root config unless it says root: false; give the lib root's directory, or leave root out", h.Root.Line)
	}
	return true, nil
}

func readHeader(fs vfs.FS, path string) (*header, []byte, error) {
	data, err := fs.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	h := &header{}
	if err := yaml.Unmarshal(data, h); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return h, data, nil
}

func loadInto(fs vfs.FS, path string, c *Config, seen map[string]bool) error {
	key := filepath.Clean(path)
	if seen[key] {
		return fmt.Errorf("%s: extends cycle", path)
	}
	seen[key] = true
	head, data, err := readHeader(fs, path)
	if err != nil {
		return err
	}
	for _, base := range head.Extends {
		if !filepath.IsAbs(base) {
			base = filepath.Join(filepath.Dir(path), base)
		}
		if err := loadInto(fs, base, c, seen); err != nil {
			return fmt.Errorf("%s: extends: %w", path, err)
		}
		// Relative paths in a base are relative to the base. A base
		// that leaves root out says nothing about it.
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
	// What this file says about being a scope is what counts, whatever
	// the files it extends are.
	if c.Scope, err = head.scope(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if c.Scope || c.Root == "" {
		c.Root = root
	}
	if c.ProfileBuild.DriverConfig == "" {
		c.ProfileBuild.DriverConfig = drv
	}
	return nil
}

// Validate reports what is wrong with the configuration, as far as the
// configuration itself can tell: values that are not among the ones a key
// takes, and keys that do not belong in a settings scope. Load calls it;
// so does whoever is handed a Config that was not loaded. Rule names and
// rule options are the linter's to check (lint.New).
func (c *Config) Validate() error {
	if n := c.Format.IndentWidth; n != 0 {
		if err := CheckIndentWidth(n); err != nil {
			return fmt.Errorf("format.indent_width: %w", err)
		}
	}
	if v := c.Format.IndentStyle; v != "" {
		if _, err := ParseIndentStyle(string(v)); err != nil {
			return fmt.Errorf("format.indent_style: %w", err)
		}
	}
	if v := c.Format.LineEndings; v != "" {
		if _, err := ParseLineEndings(string(v)); err != nil {
			return fmt.Errorf("format.line_endings: %w", err)
		}
	}
	if err := checkRules("lint.rules", c.Lint.Rules); err != nil {
		return err
	}
	for i, o := range c.Lint.Overrides {
		where := fmt.Sprintf("lint.overrides[%d]", i)
		if len(o.Paths) == 0 {
			return fmt.Errorf("%s: no paths: an override says which files it is for", where)
		}
		if err := checkRules(where+".rules", o.Rules); err != nil {
			return err
		}
	}
	if c.Scope {
		return c.validateScope()
	}

	if v := c.Lint.FailOn; v != "" {
		if _, ok := diag.ParseSeverity(v); !ok {
			return fmt.Errorf("lint.fail_on: unknown severity %q (want info, warning or error)", v)
		}
	}
	switch c.RelativeObjectPaths {
	case "", ObjectPathsFromRoot, ObjectPathsFromFile:
	default:
		return fmt.Errorf("relative_object_paths: unknown value %q (want %s or %s)", c.RelativeObjectPaths, ObjectPathsFromRoot, ObjectPathsFromFile)
	}
	if tc := c.Dialect.Typechecking; tc != nil && (*tc < 0 || *tc > 2) {
		return fmt.Errorf("dialect.typechecking: %d is not a typechecking level (0, 1 or 2)", *tc)
	}
	if n := c.ProfileBuild.ShareMinOwners; n < -1 {
		return fmt.Errorf("profile_build.share_min_owners: %d: want a number of owners, or -1 for no inference", n)
	}
	conv := c.Conventions
	for _, name := range slices.Sorted(maps.Keys(conv.Macros)) {
		if _, ok := pp.ParseClass(conv.Macros[name]); !ok {
			return fmt.Errorf("conventions.macros: %s: unknown class %q (want %s)", name, conv.Macros[name], strings.Join(pp.ClassNames, ", "))
		}
	}
	for _, reg := range []struct {
		key string
		m   map[string]int
	}{{"object_registry", conv.ObjectRegistry}, {"format_registry", conv.FormatRegistry}} {
		for _, name := range slices.Sorted(maps.Keys(reg.m)) {
			if reg.m[name] < 0 {
				return fmt.Errorf("conventions.%s: %s: %d is not an argument index (0 is the first argument)", reg.key, name, reg.m[name])
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(conv.CallRegistry)) {
		forms := conv.CallRegistry[name]
		if len(forms) == 0 {
			return fmt.Errorf("conventions.call_registry: %s: no form", name)
		}
		for _, f := range forms {
			if f.Function < 0 || (f.Object != nil && *f.Object < 0) || (f.Args != nil && *f.Args < 0) {
				return fmt.Errorf("conventions.call_registry: %s: an argument index is 0 or more", name)
			}
		}
	}
	return nil
}

// The values of relative_object_paths.
const (
	ObjectPathsFromRoot = "root"
	ObjectPathsFromFile = "file"
)

// checkRules checks the severities of rule settings.
func checkRules(where string, rules map[string]RuleSettings) error {
	for _, name := range slices.Sorted(maps.Keys(rules)) {
		rs := rules[name]
		line := ""
		if rs.Line > 0 {
			line = fmt.Sprintf("line %d: ", rs.Line)
		}
		if _, ok := diag.ParseSeverity(rs.Severity); !ok && rs.Severity != "" && rs.Severity != SeverityOff {
			return fmt.Errorf("%s%s: %s: unknown severity %q (want off, info, warning or error)", line, where, name, rs.Severity)
		}
		if rs.MaxPerFile < 0 {
			return fmt.Errorf("%s%s: %s: max_per_file: %d is not a number of findings", line, where, name, rs.MaxPerFile)
		}
	}
	return nil
}

// SeverityOff is the rule severity that turns a rule off.
const SeverityOff = "off"

// validateScope reports a key that a settings scope may not have. A scope
// holds settings; everything else describes the project, of which there
// is one, or a run.
func (c *Config) validateScope() error {
	for _, k := range []struct {
		name string
		set  bool
	}{
		{"root", c.Root != ""},
		{"profile", c.Profile != ""},
		{"lib_path", c.LibPath != ""},
		{"exclude", len(c.Exclude) > 0},
		{"dialect", c.Dialect != DialectCfg{}},
		{"include_dirs", len(c.IncludeDirs) > 0},
		{"include_file", c.IncludeFile != ""},
		{"auto_objects", len(c.AutoObjects) > 0},
		{"create", c.Create != ""},
		{"relative_object_paths", c.RelativeObjectPaths != ""},
		{"conventions", !reflect.DeepEqual(c.Conventions, profile.Conventions{})},
		{"profile_build", !reflect.DeepEqual(c.ProfileBuild, ProfileBuild{})},
		{"lint.fail_on", c.Lint.FailOn != ""},
		{"lint.baseline", c.Lint.Baseline != ""},
	} {
		if k.set {
			return fmt.Errorf("%s belongs in the project's root config; this file is a settings scope (root: false) and holds only lint.rules, lint.overrides and format", k.name)
		}
	}
	return nil
}

// FindRoot returns the nearest root config at or above dir (an absolute
// path), or "". Settings scopes on the way are passed over: they belong
// to whatever project is above them.
func FindRoot(fs vfs.FS, dir string) (string, error) {
	for {
		if p, err := rootIn(fs, dir); p != "" || err != nil {
			return p, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// fileIn returns the config file in dir, or "".
func fileIn(fs vfs.FS, dir string) string {
	for _, name := range []string{FileName, AltFileName} {
		if p := filepath.Join(dir, name); fs.Exists(p) && !fs.IsDir(p) {
			return p
		}
	}
	return ""
}

// rootIn returns the root config in dir: its config file, unless that is
// a settings scope.
func rootIn(fs vfs.FS, dir string) (string, error) {
	p := fileIn(fs, dir)
	if p == "" {
		return "", nil
	}
	head, _, err := readHeader(fs, p)
	if err != nil {
		return "", err
	}
	if scope, err := head.scope(); err != nil {
		return "", fmt.Errorf("%s: %w", p, err)
	} else if scope {
		return "", nil
	}
	return p, nil
}

// AbsRoot returns the lib root: Root as given when it is absolute, else
// relative to the config file's directory, which is the default.
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
// every file: the preprocessor's own and conventions.defines.
func (c *Config) Predefined() map[string]bool {
	m := map[string]bool{}
	for _, d := range pp.Predefined {
		m[d] = true
	}
	for _, d := range c.Conventions.Defines {
		m[d] = true
	}
	return m
}

// MacroClasses returns the macro classes the configuration gives:
// conventions.specifier_macros are empty macros, conventions.macros
// names a class.
func (c *Config) MacroClasses() map[string]pp.Class {
	m := map[string]pp.Class{}
	for _, s := range c.Conventions.SpecifierMacros {
		m[s] = pp.ClassEmpty
	}
	for name, cls := range c.Conventions.Macros {
		m[name], _ = pp.ParseClass(cls) // Validate has seen to the spelling
	}
	return m
}
