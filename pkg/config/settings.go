package config

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/mmcdole/dgdtools/pkg/editorconfig"
)

// Format are the formatter's settings (docs/design/dgdfmt.md). A zero
// field is not set: the value comes from an outer configuration, from
// .editorconfig, or is the default.
type Format struct {
	// IndentStyle is what a level of indentation is written with.
	IndentStyle IndentStyle `yaml:"indent_style"`
	// IndentWidth is the number of spaces per level, 1 to MaxIndentWidth.
	IndentWidth int `yaml:"indent_width"`
	// LineEndings is the newline policy.
	LineEndings LineEndings `yaml:"line_endings"`
}

// IndentStyle is "space" or "tab".
type IndentStyle string

const (
	IndentSpace IndentStyle = "space"
	IndentTab   IndentStyle = "tab"
)

// LineEndings is "preserve" (each line break keeps its bytes), "lf" or
// "crlf".
type LineEndings string

const (
	LineEndingsPreserve LineEndings = "preserve"
	LineEndingsLF       LineEndings = "lf"
	LineEndingsCRLF     LineEndings = "crlf"
)

// The defaults, and the widest indentation accepted.
const (
	DefaultIndentStyle = IndentSpace
	DefaultIndentWidth = 4
	DefaultLineEndings = LineEndingsPreserve
	MaxIndentWidth     = 16
)

// ParseIndentStyle maps a configuration or command-line spelling.
func ParseIndentStyle(s string) (IndentStyle, error) {
	switch v := IndentStyle(s); v {
	case IndentSpace, IndentTab:
		return v, nil
	}
	return "", fmt.Errorf("unknown indent style %q (want space or tab)", s)
}

// ParseLineEndings maps a configuration or command-line spelling.
func ParseLineEndings(s string) (LineEndings, error) {
	switch v := LineEndings(s); v {
	case LineEndingsPreserve, LineEndingsLF, LineEndingsCRLF:
		return v, nil
	}
	return "", fmt.Errorf("unknown line endings %q (want preserve, lf or crlf)", s)
}

// CheckIndentWidth reports whether n is an indentation width.
func CheckIndentWidth(n int) error {
	if n < 1 || n > MaxIndentWidth {
		return fmt.Errorf("indent width %d is not between 1 and %d", n, MaxIndentWidth)
	}
	return nil
}

func (v *IndentStyle) UnmarshalYAML(n *yaml.Node) (err error) {
	*v, err = ParseIndentStyle(n.Value)
	return at(n, err)
}

func (v *LineEndings) UnmarshalYAML(n *yaml.Node) (err error) {
	*v, err = ParseLineEndings(n.Value)
	return at(n, err)
}

// at puts the line of a YAML node in front of an error about it.
func at(n *yaml.Node, err error) error {
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	return nil
}

// Settings are what one file is formatted and linted by: the project's
// own settings as adjusted by the configuration files between the
// project and the file (docs/design/config.md).
type Settings struct {
	// Format has every key set. For each key the first of these that
	// sets it wins: the caller's overrides, the nearest settings scope
	// and then outer ones, the project's config, the file's
	// .editorconfig properties, the default.
	Format Format
	// From says where each key of Format came from: a file, or one of
	// FromFlag and FromDefault.
	From FormatSources
	// Scopes are the configuration files below the project's that apply
	// to the file, outermost first. The linter applies their rules in
	// that order after the project's own.
	Scopes []*Scope
}

// FormatSources names the source of each format key.
type FormatSources struct {
	IndentStyle, IndentWidth, LineEndings string
}

// Where a setting comes from when not from a file.
const (
	FromFlag    = "command line"
	FromDefault = "default"
)

// Scope is the settings one nested dgdtools.yml gives the files under
// its directory. It is usually a settings scope (`root: false`); a root
// config found below the project being worked on counts the same way,
// its project keys unused.
type Scope struct {
	// Path is the file and Dir its directory, which the globs of
	// Overrides are relative to.
	Path, Dir string
	Format    Format
	Rules     map[string]RuleSettings
	Overrides []Override
}

// settingsCache is what a project has read of the per-directory files.
type settingsCache struct {
	mu      sync.Mutex
	scopes  map[string]*scopeList // by directory
	editors map[string][]*editorconfig.File
}

type scopeList struct {
	list []*Scope
	err  error
}

// ResetSettings forgets the nested configuration and .editorconfig files
// read so far. A long-lived caller calls it when one of them changes.
func (prj *Project) ResetSettings() {
	prj.mu.Lock()
	defer prj.mu.Unlock()
	prj.scopes, prj.editors = nil, nil
}

// Settings returns the settings of the file at path (absolute; the file
// need not exist). It fails when a configuration file that applies to
// the file is not valid.
func (prj *Project) Settings(path string) (*Settings, error) {
	dir := filepath.Dir(path)
	prj.mu.Lock()
	scopes := prj.scopesAt(dir)
	var editors []*editorconfig.File
	if scopes.err == nil {
		editors = prj.editorsAt(dir)
	}
	prj.mu.Unlock()
	if scopes.err != nil {
		return nil, scopes.err
	}

	s := &Settings{Scopes: scopes.list}
	fill := func(f Format, from string) {
		if s.Format.IndentStyle == "" && f.IndentStyle != "" {
			s.Format.IndentStyle, s.From.IndentStyle = f.IndentStyle, from
		}
		if s.Format.IndentWidth == 0 && f.IndentWidth != 0 {
			s.Format.IndentWidth, s.From.IndentWidth = f.IndentWidth, from
		}
		if s.Format.LineEndings == "" && f.LineEndings != "" {
			s.Format.LineEndings, s.From.LineEndings = f.LineEndings, from
		}
	}
	fill(prj.format, FromFlag)
	for i := len(s.Scopes) - 1; i >= 0; i-- {
		fill(s.Scopes[i].Format, s.Scopes[i].Path)
	}
	fill(prj.Config.Format, prj.ConfigPath)
	if set := s.Format; len(editors) > 0 && (set.IndentStyle == "" || set.IndentWidth == 0 || set.LineEndings == "") {
		// The properties are asked for per key: each may come from a
		// different file. Values that mean nothing here are skipped, as
		// the format asks.
		props := editorconfig.Properties(editors, path)
		if p, ok := props["indent_style"]; ok {
			if v, err := ParseIndentStyle(p.Value); err == nil {
				fill(Format{IndentStyle: v}, p.Source)
			}
		}
		size := props["indent_size"]
		if size.Value == "tab" {
			size = props["tab_width"]
		}
		if n, err := strconv.Atoi(size.Value); err == nil && CheckIndentWidth(n) == nil {
			fill(Format{IndentWidth: n}, size.Source)
		}
		if p, ok := props["end_of_line"]; ok && p.Value != string(LineEndingsPreserve) {
			if v, err := ParseLineEndings(p.Value); err == nil {
				fill(Format{LineEndings: v}, p.Source)
			}
		}
	}
	fill(Format{DefaultIndentStyle, DefaultIndentWidth, DefaultLineEndings}, FromDefault)
	return s, nil
}

// scopesAt returns the nested configuration files that apply to the
// files in dir: those in dir and the directories above it, as far as the
// project reaches. Called with prj.mu held.
func (prj *Project) scopesAt(dir string) *scopeList {
	if l, ok := prj.scopes[dir]; ok {
		return l
	}
	l := &scopeList{}
	if top, ok := prj.top(dir); ok {
		parent := filepath.Dir(dir)
		if dir != top && parent != dir {
			up := prj.scopesAt(parent)
			l.list, l.err = up.list, up.err
		}
		if p := fileIn(prj.fs, dir); p != "" && p != prj.ConfigPath && l.err == nil {
			if c, err := Load(prj.fs, p); err != nil {
				l.err = err
			} else {
				l.list = append(l.list[:len(l.list):len(l.list)], &Scope{
					Path: p, Dir: dir, Format: c.Format, Rules: c.Lint.Rules, Overrides: c.Lint.Overrides,
				})
			}
		}
	}
	if prj.scopes == nil {
		prj.scopes = map[string]*scopeList{}
	}
	prj.scopes[dir] = l
	return l
}

// top returns the highest directory whose configuration file can apply
// to the files in dir; false when none can. Nested files are looked for
// from the file's directory up to the project: below the directory of
// the project's config, or, when the lib is somewhere else, down from
// the lib root. A project without a config of its own has no top but the
// file system's, and one whose config was named by the caller has no
// nested files.
func (prj *Project) top(dir string) (string, bool) {
	if prj.fs == nil || prj.explicit || prj.noFiles {
		return "", false
	}
	if prj.ConfigPath == "" || prj.UserConfig {
		return "", true
	}
	// The project's own directory holds its config, not a nested one,
	// and scopesAt passes over that file by name.
	for _, t := range []string{prj.Config.Dir, prj.Root, prj.CheckoutDir} {
		if t != "" && under(t, dir) {
			return t, true
		}
	}
	return "", false
}

// under reports whether dir is top or below it.
func under(top, dir string) bool {
	return dir == top || strings.HasPrefix(dir, strings.TrimSuffix(top, string(filepath.Separator))+string(filepath.Separator))
}

// editorsAt returns the .editorconfig files that apply to the files in
// dir, nearest first. Called with prj.mu held.
func (prj *Project) editorsAt(dir string) []*editorconfig.File {
	if prj.fs == nil || prj.noFiles {
		return nil
	}
	if l, ok := prj.editors[dir]; ok {
		return l
	}
	var own *editorconfig.File
	if p := filepath.Join(dir, editorconfig.FileName); prj.fs.Exists(p) && !prj.fs.IsDir(p) {
		if data, err := prj.fs.ReadFile(p); err == nil {
			own = editorconfig.Parse(p, data)
		}
	}
	var l []*editorconfig.File
	if parent := filepath.Dir(dir); parent != dir && (own == nil || !own.Root) {
		l = prj.editorsAt(parent)
	}
	if own != nil {
		l = append([]*editorconfig.File{own}, l...)
	}
	if prj.editors == nil {
		prj.editors = map[string][]*editorconfig.File{}
	}
	prj.editors[dir] = l
	return l
}
