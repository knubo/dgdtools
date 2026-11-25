// Package kfun is the table of a driver's kernel functions: the
// functions every program can call that no file of a lib declares.
//
// DGD is the table of stock DGD, read from the driver's own source
// (dgd.kfuns, which TestDriverSource holds to it and -update writes): the
// name of each kfun a program can call by name, its prototype as the
// driver declares it, and a line that says what it does. A driver's
// declarations name no parameters, so neither does the table.
//
// What a driver has beyond that is the project's to say: a mud that
// loads extension modules lists their kfuns as prototypes in its
// configuration (dialect.kfuns), a profile carries them to whoever has
// part of the lib, and Table.With adds them. Nothing of one mud is in
// the table itself.
package kfun

import (
	_ "embed"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Param is one parameter of a kfun.
type Param struct {
	// Type is the type as LPC writes it, with its '*'s: "string",
	// "mixed *". A parameter the driver declares as an lvalue (sscanf's
	// targets) has the type "lvalue".
	Type string
	// Name is the parameter's name, "" where the declaration gives none
	// (the driver's own give none).
	Name string
	// Optional says the argument may be left out.
	Optional bool
}

// Func is one kernel function.
type Func struct {
	Name string
	// Decl is the prototype: "int call_out(string, mixed, mixed...)".
	Decl string
	// Type is the return type, as Param.Type.
	Type   string
	Params []Param
	// Ellipsis says the last parameter takes any number of arguments.
	Ellipsis bool
	// MinArgs and MaxArgs bound the number of arguments; MaxArgs is -1
	// when there is no bound.
	MinArgs, MaxArgs int
	// Doc says in a line what the function does, "" when nothing says.
	Doc string
	// Origin says whose the declaration is: the driver and version the
	// table was read from ("DGD 1.7.9"), or "" for a kfun the project
	// declares.
	Origin string
}

// Table is a set of kfuns by name. It is immutable.
type Table struct {
	funcs map[string]*Func
}

// Lookup returns the kfun called name, nil when there is none. A nil
// table has none.
func (t *Table) Lookup(name string) *Func {
	if t == nil {
		return nil
	}
	return t.funcs[name]
}

// Names returns the names of the kfuns, sorted.
func (t *Table) Names() []string {
	if t == nil {
		return nil
	}
	names := make([]string, 0, len(t.funcs))
	for name := range t.funcs {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// With returns the table with the kfuns the prototypes declare added,
// each as Parse reads it. One that has the name of a kfun of the table
// replaces it: the project knows its driver.
func (t *Table) With(protos []string) (*Table, error) {
	if len(protos) == 0 {
		return t, nil
	}
	out := &Table{funcs: make(map[string]*Func, len(protos))}
	if t != nil {
		for name, f := range t.funcs {
			out.funcs[name] = f
		}
	}
	for _, p := range protos {
		f, err := Parse(p)
		if err != nil {
			return nil, err
		}
		out.funcs[f.Name] = f
	}
	return out, nil
}

//go:embed dgd.kfuns
var dgdSource string

var dgd = sync.OnceValue(func() *Table {
	t, err := read(dgdSource)
	if err != nil {
		panic("kfun: dgd.kfuns: " + err.Error()) // TestDriverSource reads it
	}
	return t
})

// DGD returns the kfuns of stock DGD.
func DGD() *Table { return dgd() }

// read reads a table as dgd.kfuns has it: a prototype a line, the line
// before it, when it is a // comment, saying what the function does;
// lines that begin with '#' are about the file, and the one that begins
// "# source: " names the driver the table was read from.
func read(text string) (*Table, error) {
	t := &Table{funcs: map[string]*Func{}}
	origin, doc := "", ""
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			doc = ""
		case strings.HasPrefix(line, "# source: "):
			origin = strings.TrimPrefix(line, "# source: ")
		case strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "//"):
			doc = strings.TrimSpace(strings.TrimPrefix(line, "//"))
		default:
			f, err := Parse(line)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", n+1, err)
			}
			if t.funcs[f.Name] != nil {
				return nil, fmt.Errorf("line %d: %s is declared twice", n+1, f.Name)
			}
			f.Doc, f.Origin, doc = doc, origin, ""
			t.funcs[f.Name] = f
		}
	}
	return t, nil
}

// types are the words a type is written with; "lvalue" is the driver's
// for an argument it assigns to.
var types = []string{"int", "float", "string", "object", "mapping", "mixed", "void", "lvalue"}

// Parse reads the prototype of a kfun:
//
//	TYPE NAME(PARAMETER, ...)
//
// with TYPE one of int, float, string, object, mapping, mixed and void,
// followed by any number of '*'; a parameter a type (lvalue too, and not
// void) and optionally a name; "varargs" before the first parameter that
// may be left out; and "..." after the last when it takes the rest of
// the arguments. A ';' at the end is allowed. That is how LPC declares a
// function, less the parameters' names where a driver gives none.
func Parse(proto string) (*Func, error) {
	bad := func(format string, args ...any) (*Func, error) {
		return nil, fmt.Errorf("kfun prototype %q: %s", proto, fmt.Sprintf(format, args...))
	}
	s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(proto), ";"))
	open, shut := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || shut != len(s)-1 {
		return bad("want TYPE NAME(PARAMETERS)")
	}
	typ, name, ok := declarator(s[:open])
	switch {
	case !ok || typ == "" || name == "":
		return bad("want a return type and a name before '('")
	case strings.HasPrefix(typ, "lvalue"):
		return bad("lvalue is no return type")
	}
	f := &Func{Name: name, Type: typ}
	inner := strings.TrimSpace(s[open+1 : shut])
	if inner == "void" {
		inner = ""
	}
	var parts []string
	optional := false
	if inner != "" {
		for i, p := range strings.Split(inner, ",") {
			p = strings.TrimSpace(p)
			if rest, ok := cutWord(p, "varargs"); ok {
				if optional {
					return bad("varargs stands once, before the first parameter that may be left out")
				}
				p, optional = rest, true
			}
			ellipsis := false
			if rest, ok := strings.CutSuffix(p, "..."); ok {
				p, ellipsis = strings.TrimSpace(rest), true
			}
			typ, name, ok := declarator(p)
			if !ok || typ == "" || strings.HasPrefix(typ, "void") {
				return bad("parameter %d: want a type, and a name or none", i+1)
			}
			if f.Ellipsis {
				return bad("'...' stands after the last parameter only")
			}
			f.Ellipsis = ellipsis
			f.Params = append(f.Params, Param{Type: typ, Name: name, Optional: optional || ellipsis})
			text := typ
			if name != "" {
				text = join(typ, name)
			}
			if optional && !slices.ContainsFunc(f.Params[:len(f.Params)-1], func(p Param) bool { return p.Optional }) {
				text = "varargs " + text
			}
			if ellipsis {
				text += "..."
			}
			parts = append(parts, text)
		}
	}
	f.MaxArgs = len(f.Params)
	f.MinArgs = slices.IndexFunc(f.Params, func(p Param) bool { return p.Optional })
	if f.MinArgs < 0 {
		f.MinArgs = f.MaxArgs
	}
	if f.Ellipsis {
		f.MaxArgs = -1
	}
	f.Decl = join(f.Type, f.Name) + "(" + strings.Join(parts, ", ") + ")"
	return f, nil
}

// join writes a type before a name: "int x", "string *x".
func join(typ, name string) string {
	if strings.HasSuffix(typ, "*") {
		return typ + name
	}
	return typ + " " + name
}

// cutWord cuts the word w from the start of s.
func cutWord(s, w string) (rest string, ok bool) {
	rest, ok = strings.CutPrefix(s, w)
	if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t' && rest[0] != '*') {
		return s, false
	}
	return strings.TrimSpace(rest), true
}

// declarator reads "TYPE", "TYPE NAME", "TYPE *" or "TYPE *NAME" into
// the type with its stars ("string *") and the name.
func declarator(s string) (typ, name string, ok bool) {
	s = strings.TrimSpace(s)
	for _, t := range types {
		rest, found := cutWord(s, t)
		if !found {
			continue
		}
		stars := 0
		for rest != "" && (rest[0] == '*' || rest[0] == ' ' || rest[0] == '\t') {
			if rest[0] == '*' {
				stars++
			}
			rest = rest[1:]
		}
		if rest != "" && !isName(rest) {
			return "", "", false
		}
		if stars > 0 {
			t += " " + strings.Repeat("*", stars)
		}
		return t, rest, true
	}
	return "", "", false
}

// isName reports whether s is an identifier.
func isName(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return s != "" && !slices.Contains(types, s) && s != "varargs"
}
