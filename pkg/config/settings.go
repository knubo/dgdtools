package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
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
