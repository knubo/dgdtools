// Package editorconfig reads .editorconfig files (editorconfig.org): the
// properties an editor is told to use for a file, by the files named
// .editorconfig in its directory and the directories above.
package editorconfig

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// FileName is the name the files have.
const FileName = ".editorconfig"

// bom is the byte order mark a file may begin with.
var bom = string(rune(0xFEFF))

// File is one parsed .editorconfig.
type File struct {
	Path string
	// Root says the search for further files stops here (root = true).
	Root     bool
	sections []section
}

type section struct {
	match *regexp.Regexp // against the path relative to the file's directory
	props [][2]string    // in order of appearance
}

// Parse reads the content of the .editorconfig at path. The format has no
// errors: a line that is not understood is skipped, as the specification
// asks.
func Parse(path string, data []byte) *File {
	f := &File{Path: path}
	var cur *section
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, bom))
		switch {
		case line == "" || line[0] == '#' || line[0] == ';':
			continue
		case line[0] == '[' && line[len(line)-1] == ']':
			f.sections = append(f.sections, section{})
			cur = &f.sections[len(f.sections)-1]
			if re, err := regexp.Compile(globRegexp(line[1 : len(line)-1])); err == nil {
				cur.match = re
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.ToLower(strings.TrimSpace(value))
		if cur == nil {
			if key == "root" {
				f.Root = value == "true"
			}
			continue
		}
		cur.props = append(cur.props, [2]string{key, value})
	}
	return f
}

// Property is the value of one property and the file that gave it.
type Property struct {
	Value, Source string
}

// Properties returns the properties of the file at path. files are the
// .editorconfig files that apply to it, nearest first (the file in its
// own directory, then its parent's, up to and including the first with
// Root set). A nearer file wins over a farther one, and within a file a
// later section over an earlier; the value "unset" removes a property.
// Names and values are lower case.
func Properties(files []*File, path string) map[string]Property {
	props := map[string]Property{}
	for i := len(files) - 1; i >= 0; i-- {
		f := files[i]
		rel, err := filepath.Rel(filepath.Dir(f.Path), path)
		if err != nil {
			continue
		}
		rel = "/" + filepath.ToSlash(rel)
		for _, s := range f.sections {
			if s.match == nil || !s.match.MatchString(rel) {
				continue
			}
			for _, kv := range s.props {
				if kv[1] == "unset" {
					delete(props, kv[0])
				} else {
					props[kv[0]] = Property{kv[1], f.Path}
				}
			}
		}
	}
	return props
}

// globRegexp translates a section glob to a regular expression over a
// path that is relative to the file's directory and begins with "/". A
// glob without a slash matches a file name in any directory; one with a
// slash is anchored at the file's directory.
func globRegexp(glob string) string {
	var b strings.Builder
	b.WriteString("^")
	switch {
	case !strings.Contains(glob, "/"):
		b.WriteString("(.*/)?")
	case glob[0] != '/':
		b.WriteString("/")
	}
	braces := 0
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; c {
		case '\\':
			if i+1 < len(glob) {
				i++
				b.WriteString(regexp.QuoteMeta(glob[i : i+1]))
			}
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++
				b.WriteString(".*")
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(glob[i+1:], ']')
			if end < 0 || strings.Contains(glob[i+1:i+1+end], "/") {
				b.WriteString(`\[`)
				continue
			}
			set := glob[i+1 : i+1+end]
			b.WriteString("[")
			if strings.HasPrefix(set, "!") {
				b.WriteString("^")
				set = set[1:]
			}
			b.WriteString(strings.NewReplacer(`\`, `\\`, `^`, `\^`, `[`, `\[`).Replace(set))
			b.WriteString("]")
			i += end + 1
		case '{':
			end := matchingBrace(glob, i)
			if end < 0 {
				b.WriteString(`\{`)
				continue
			}
			if lo, hi, ok := numRange(glob[i+1 : end]); ok {
				b.WriteString(rangeRegexp(lo, hi))
				i = end
				continue
			}
			braces++
			b.WriteString("(?:")
		case '}':
			if braces == 0 {
				b.WriteString(`\}`)
				continue
			}
			braces--
			b.WriteString(")")
		case ',':
			if braces == 0 {
				b.WriteString(",")
				continue
			}
			b.WriteString("|")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return b.String()
}

// matchingBrace returns the index of the '}' that closes the '{' at i,
// or -1.
func matchingBrace(glob string, i int) int {
	depth := 0
	for j := i; j < len(glob); j++ {
		switch glob[j] {
		case '\\':
			j++
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return j
			}
		}
	}
	return -1
}

// numRange reads the inside of {lo..hi}.
func numRange(s string) (lo, hi int, ok bool) {
	a, b, found := strings.Cut(s, "..")
	if !found {
		return 0, 0, false
	}
	lo, err1 := strconv.Atoi(a)
	hi, err2 := strconv.Atoi(b)
	return lo, hi, err1 == nil && err2 == nil && lo <= hi && hi-lo <= 1000
}

// rangeRegexp matches the decimal numbers lo to hi.
func rangeRegexp(lo, hi int) string {
	nums := make([]string, 0, hi-lo+1)
	for n := lo; n <= hi; n++ {
		nums = append(nums, regexp.QuoteMeta(strconv.Itoa(n)))
	}
	return "(?:" + strings.Join(nums, "|") + ")"
}
