package editorconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseAndProperties(t *testing.T) {
	outer := Parse("/proj/.editorconfig", []byte(`# top
root = true

[*]
indent_style = space
indent_size = 4
end_of_line = LF
charset = utf-8

[*.{c,h}]
Indent_Style = Tab
indent_size = tab
tab_width = 8

[Makefile]
indent_style = tab

[/docs/**.md]
indent_size = 2

[lib/*/old/*.c]
indent_size = 3
`))
	inner := Parse("/proj/lib/players/.editorconfig", []byte("[*.c]\nindent_style = space\nindent_size = 2\ntab_width = unset\n\n[x[0-9].c]\nindent_size = 5\n; comment\nnonsense line\n"))
	assert.True(t, outer.Root)
	assert.False(t, inner.Root)

	val := func(files []*File, path, key string) string { return Properties(files, path)[key].Value }

	// One file: later sections win; values and names are lower case.
	one := []*File{outer}
	assert.Equal(t, "tab", val(one, "/proj/lib/std/room.c", "indent_style"))
	assert.Equal(t, "tab", val(one, "/proj/lib/std/room.c", "indent_size"))
	assert.Equal(t, "8", val(one, "/proj/lib/std/room.c", "tab_width"))
	assert.Equal(t, "lf", val(one, "/proj/lib/std/room.c", "end_of_line"))
	assert.Equal(t, "space", val(one, "/proj/README", "indent_style"))
	assert.Equal(t, "tab", val(one, "/proj/sub/Makefile", "indent_style"))
	// A glob with a slash is anchored at the file's directory.
	assert.Equal(t, "2", val(one, "/proj/docs/a/b.md", "indent_size"))
	assert.Equal(t, "4", val(one, "/proj/other/docs/b.md", "indent_size"))
	assert.Equal(t, "3", val(one, "/proj/lib/d/old/x.c", "indent_size"))
	assert.Equal(t, "tab", val(one, "/proj/lib/d/e/old/x.c", "indent_size"))

	// Two files: the nearer wins per property, and says where it is from.
	two := []*File{inner, outer}
	props := Properties(two, "/proj/lib/players/drake/room.c")
	assert.Equal(t, Property{"space", "/proj/lib/players/.editorconfig"}, props["indent_style"])
	assert.Equal(t, "2", props["indent_size"].Value)
	assert.Equal(t, Property{"lf", "/proj/.editorconfig"}, props["end_of_line"])
	assert.NotContains(t, props, "tab_width")
	assert.Equal(t, "tab", val(two, "/proj/lib/players/drake/room.h", "indent_style"))
	assert.Equal(t, "5", val(two, "/proj/lib/players/x7.c", "indent_size"))
	assert.Equal(t, "2", val(two, "/proj/lib/players/xy.c", "indent_size"))
}

func TestGlobs(t *testing.T) {
	match := func(glob, path string) bool {
		f := Parse("/d/.editorconfig", []byte("["+glob+"]\nk = v\n"))
		_, ok := Properties([]*File{f}, "/d/"+path)["k"]
		return ok
	}
	for _, c := range []struct {
		glob, path string
		want       bool
	}{
		{"*", "a.c", true},
		{"*", "sub/a.c", true},
		{"*.c", "sub/a.c", true},
		{"*.c", "a.h", false},
		{"a?.c", "ab.c", true},
		{"a?.c", "a/.c", false},
		{"**.c", "x/y/z.c", true},
		{"/*.c", "a.c", true},
		{"/*.c", "sub/a.c", false},
		{"sub/**", "sub/x/y.c", true},
		{"sub/*", "sub/x/y.c", false},
		{"*.{c,h}", "a.h", true},
		{"*.{c,h}", "a.cc", false},
		{"{a,b{1,2}}.c", "b2.c", true},
		{"{a,b{1,2}}.c", "b3.c", false},
		{"file{1..3}.c", "file2.c", true},
		{"file{1..3}.c", "file4.c", false},
		{"[!abc]x.c", "dx.c", true},
		{"[!abc]x.c", "ax.c", false},
		{"a\\*.c", "a*.c", true},
		{"a\\*.c", "ab.c", false},
		{"{single}.c", "single.c", true},
		{"a,b.c", "a,b.c", true},
		{"[unclosed.c", "[unclosed.c", true},
	} {
		assert.Equal(t, c.want, match(c.glob, c.path), "%s ~ %s", c.glob, c.path)
	}
}
