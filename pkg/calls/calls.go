// Package calls finds, in one file's syntax tree, the calls whose target
// is named rather than bound by the compiler: obj->fn(),
// call_other(obj, "fn"), call_out("fn", ...), the callbacks handed to the
// lib's own registrars, and the object paths handed to path-taking
// functions. In DGD every one of these is resolved when it runs; they are
// what cross-file analysis (the linter's runtime rules, the selection of
// shared code for a profile) is about.
//
// Extract is a function of the tree, the file's #define environment and
// the registry of the project. It reads nothing else.
package calls

import (
	"strings"

	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/syntax"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// Kind says how a named call reaches its function.
type Kind uint8

const (
	// CrossObject calls use call_other semantics: static and private
	// functions are invisible and yield nil.
	CrossObject Kind = iota
	// SelfCallback calls (call_out, registered handlers) can reach static
	// functions of this_object().
	SelfCallback
)

// Target says what is known of the object a named call is made in.
type Target uint8

const (
	TargetUnknown Target = iota
	TargetSelf           // this_object()
	TargetPath           // a lib path known from the source
)

// Named is one call site that names a function late-bound: obj->fn(),
// call_other(obj, "fn"), call_out("fn"), or a registrar of the lib.
type Named struct {
	Func string
	// Via is how the call is made: "->" or the name of the function the
	// name was handed to.
	Via        string
	Kind       Kind
	Target     Target
	TargetPath string // when Target is TargetPath
	// NArgs is the number of arguments the function receives when the
	// call site determines it; -1 when unknown.
	NArgs      int
	Start, End uint32 // of the function name
}

// PathRef is a literal object path handed to a path-taking function.
type PathRef struct {
	Path, Via  string
	Start, End uint32
}

// Direct is name(args), ::name(args) or label::name(args).
type Direct struct {
	Call *syntax.CallExpr
	In   *syntax.FuncDecl // the enclosing function; nil at top level
}

// Facts are the calls of one file.
type Facts struct {
	Named  []Named
	Paths  []PathRef
	Direct []Direct
}

// registrar is a function that takes the name of a function to call.
type registrar struct {
	arg        int
	kind       Kind
	targetArg0 bool
}

// Registry knows which functions take a function name or an object path:
// the driver's own and those of the lib's conventions. It is immutable.
type Registry struct {
	named   map[string]registrar
	objects map[string]int
}

// NewRegistry returns the registry of a lib with the given conventions.
func NewRegistry(conv profile.Conventions) *Registry {
	r := &Registry{
		named: map[string]registrar{
			"call_other": {arg: 1, kind: CrossObject, targetArg0: true},
			"call_out":   {arg: 0, kind: SelfCallback},
		},
		objects: map[string]int{"clone_object": 0, "compile_object": 0, "find_object": 0},
	}
	for name, arg := range conv.CallRegistry {
		r.named[name] = registrar{arg: arg, kind: SelfCallback}
	}
	for name, arg := range conv.ObjectRegistry {
		r.objects[name] = arg
	}
	return r
}

// File is the file whose calls are extracted.
type File struct {
	Tree *syntax.File
	// Env is the file's #define environment: receivers and paths written
	// as macros are read through it.
	Env *pp.Env
	// LibPath is the file's lib path, "" when it has none: nothing is
	// then known about any object path.
	LibPath string
	// ObjectPath turns an object path as written in the file into an
	// absolute lib path (index.Query.ObjectPath).
	ObjectPath func(written string) string
}

// Extract finds every call site of the file.
func Extract(f File, reg *Registry) *Facts {
	cf := &Facts{}
	t := f.Tree
	strLit := func(e syntax.Expr) (string, bool) {
		lit, ok := e.(*syntax.BasicLit)
		if !ok || lit.Kind != token.StringLit {
			return "", false
		}
		return pp.Unquote(t.Text(lit))
	}
	nargs := func(args []syntax.Expr, spread bool) int {
		if spread {
			return -1
		}
		return len(args)
	}

	var fn *syntax.FuncDecl
	for _, d := range t.Decls {
		fn, _ = d.(*syntax.FuncDecl)
		syntax.Inspect(d, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.ArrowExpr:
				c := Named{
					Func: n.Name.Name, Via: "->", Kind: CrossObject,
					NArgs: nargs(n.Args, n.Spread),
					Start: t.Off(n.Name), End: t.End(n.Name),
				}
				c.Target, c.TargetPath = f.target(n.X)
				cf.Named = append(cf.Named, c)

			case *syntax.CallExpr:
				if n.Opaque {
					return true
				}
				cf.Direct = append(cf.Direct, Direct{Call: n, In: fn})
				if n.Scoped {
					return true
				}
				name := n.Fun.Name
				if argIdx, ok := reg.objects[name]; ok && argIdx < len(n.Args) {
					if s, ok := strLit(n.Args[argIdx]); ok && strings.Contains(s, "/") {
						cf.Paths = append(cf.Paths, PathRef{
							Path: f.ObjectPath(s), Via: name,
							Start: t.Off(n.Args[argIdx]), End: t.End(n.Args[argIdx]),
						})
					}
				}
				r, ok := reg.named[name]
				if !ok || r.arg >= len(n.Args) {
					return true
				}
				fnArg := n.Args[r.arg]
				fname, ok := strLit(fnArg)
				if !ok {
					return true
				}
				c := Named{
					Func: fname, Via: name, Kind: r.kind,
					Target: TargetSelf, NArgs: -1,
					Start: t.Off(fnArg), End: t.End(fnArg),
				}
				if r.targetArg0 {
					c.Target, c.TargetPath = f.target(n.Args[0])
				}
				// call_other(obj, "fn", rest...) and call_out("fn", delay,
				// rest...) hand exactly the rest to the callee.
				if name == "call_other" || name == "call_out" {
					if n.Spread {
						c.NArgs = -1
					} else if len(n.Args) >= 2 {
						c.NArgs = len(n.Args) - 2
					} else {
						c.NArgs = 0
					}
				}
				if !token.IsIdent(fname) {
					// Two-argument registrar form: fp("/obj/path", "fn")
					// binds fn on another object.
					if strings.Contains(fname, "/") && r.arg+1 < len(n.Args) {
						if fn2, ok := strLit(n.Args[r.arg+1]); ok && token.IsIdent(fn2) {
							cf.Named = append(cf.Named, Named{
								Func: fn2, Via: name, Kind: CrossObject,
								Target: TargetPath, NArgs: -1,
								TargetPath: f.ObjectPath(fname),
								Start:      t.Off(n.Args[r.arg+1]), End: t.End(n.Args[r.arg+1]),
							})
						}
					}
					return true
				}
				cf.Named = append(cf.Named, c)
			}
			return true
		})
	}
	return cf
}

// target decides what object a receiver expression denotes:
// this_object() (directly or through a macro), a string constant (a
// literal, a concatenation, or a macro that evaluates to one), or unknown.
func (f File) target(e syntax.Expr) (Target, string) {
	e = syntax.Unparen(e)
	switch x := e.(type) {
	case *syntax.CallExpr:
		if !x.Scoped && x.Fun.Name == "this_object" && len(x.Args) == 0 && !x.Opaque {
			return TargetSelf, ""
		}
		return TargetUnknown, ""
	case *syntax.Ident:
		body := f.Env.Expansion(x.Name)
		if body == nil {
			return TargetUnknown, ""
		}
		if len(body) == 3 && body[0].Kind == token.Ident && body[0].Text == "this_object" &&
			body[1].Kind == token.LParen && body[2].Kind == token.RParen {
			return TargetSelf, ""
		}
	case *syntax.BasicLit, *syntax.BinaryExpr, *syntax.AdjacentStrings:
	default:
		return TargetUnknown, ""
	}
	if f.LibPath == "" {
		return TargetUnknown, ""
	}
	first, last := e.Span()
	s, ok := f.Env.EvalString(pp.FileTokens(f.Tree.Tok, first, last))
	if !ok || s == "" {
		return TargetUnknown, ""
	}
	return TargetPath, f.ObjectPath(s)
}
