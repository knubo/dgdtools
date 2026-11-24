package lint

import (
	"strings"

	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/syntax"
	"github.com/mmcdole/dgdtools/pkg/token"
)

type callKind uint8

const (
	// crossObject calls use call_other semantics: static and private
	// functions are invisible and yield nil.
	crossObject callKind = iota
	// selfCallback calls (call_out, registered handlers) can reach static
	// functions of this_object().
	selfCallback
)

type targetKind uint8

const (
	targetUnknown targetKind = iota
	targetSelf               // this_object()
	targetPath               // a lib path known at lint time
)

// stringCall is one call site that names a function late-bound: obj->fn(),
// call_other(obj, "fn"), call_out("fn"), or a configured registrar.
type stringCall struct {
	Func       string
	Registrar  string // "->", "call_other", "call_out", or a config name
	Kind       callKind
	Target     targetKind
	TargetPath string
	// NArgs is the number of arguments the callee receives when the call
	// site determines it; -1 when unknown.
	NArgs      int
	Start, End uint32 // of the function name
}

// pathRef is a literal object path handed to a path-taking function.
type pathRef struct {
	Path, Via  string
	Start, End uint32
}

// directCall is name(args), ::name(args) or label::name(args).
type directCall struct {
	Call *syntax.CallExpr
	In   *syntax.FuncDecl
}

type callFacts struct {
	strings []stringCall
	paths   []pathRef
	direct  []directCall
	// lookups[i] is the resolution of strings[i], made on first need.
	lookups []*lookedUp
}

type lookedUp struct {
	index.Lookup
	target string // the object looked in, for messages
}

type registrar struct {
	arg        int
	kind       callKind
	targetArg0 bool
}

// calls extracts (once per file) every call site from the tree.
func (p *Pass) calls() *callFacts {
	if p.shared.calls == nil {
		p.shared.calls = p.extractCalls()
	}
	return p.shared.calls
}

func (p *Pass) extractCalls() *callFacts {
	cf := &callFacts{}
	regs := map[string]registrar{
		"call_other": {arg: 1, kind: crossObject, targetArg0: true},
		"call_out":   {arg: 0, kind: selfCallback},
	}
	for name, arg := range p.Config.Conventions.CallRegistry {
		regs[name] = registrar{arg: arg, kind: selfCallback}
	}
	objRegs := map[string]int{"clone_object": 0, "compile_object": 0, "find_object": 0}
	for name, arg := range p.Config.Conventions.ObjectRegistry {
		objRegs[name] = arg
	}
	t := p.Tree
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
				c := stringCall{
					Func: n.Name.Name, Registrar: "->", Kind: crossObject,
					NArgs: nargs(n.Args, n.Spread),
					Start: t.Off(n.Name), End: t.End(n.Name),
				}
				c.Target, c.TargetPath = p.classifyTarget(n.X)
				cf.strings = append(cf.strings, c)

			case *syntax.CallExpr:
				if n.Opaque {
					return true
				}
				cf.direct = append(cf.direct, directCall{Call: n, In: fn})
				if n.Scoped {
					return true
				}
				name := n.Fun.Name
				if argIdx, ok := objRegs[name]; ok && argIdx < len(n.Args) {
					if s, ok := strLit(n.Args[argIdx]); ok && strings.Contains(s, "/") {
						cf.paths = append(cf.paths, pathRef{
							Path: p.Index.ObjectPath(s), Via: name,
							Start: t.Off(n.Args[argIdx]), End: t.End(n.Args[argIdx]),
						})
					}
				}
				reg, ok := regs[name]
				if !ok || reg.arg >= len(n.Args) {
					return true
				}
				fnArg := n.Args[reg.arg]
				fname, ok := strLit(fnArg)
				if !ok {
					return true
				}
				c := stringCall{
					Func: fname, Registrar: name, Kind: reg.kind,
					Target: targetSelf, NArgs: -1,
					Start: t.Off(fnArg), End: t.End(fnArg),
				}
				if reg.targetArg0 {
					c.Target, c.TargetPath = p.classifyTarget(n.Args[0])
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
					if strings.Contains(fname, "/") && reg.arg+1 < len(n.Args) {
						if fn2, ok := strLit(n.Args[reg.arg+1]); ok && token.IsIdent(fn2) {
							cf.strings = append(cf.strings, stringCall{
								Func: fn2, Registrar: name, Kind: crossObject,
								Target: targetPath, NArgs: -1,
								TargetPath: p.Index.ObjectPath(fname),
								Start:      t.Off(n.Args[reg.arg+1]), End: t.End(n.Args[reg.arg+1]),
							})
						}
					}
					return true
				}
				cf.strings = append(cf.strings, c)
			}
			return true
		})
	}
	cf.lookups = make([]*lookedUp, len(cf.strings))
	st := &p.shared.stats
	st.callSites += len(cf.strings)
	for _, c := range cf.strings {
		if c.Target == targetUnknown {
			st.unknownReceivers++
		}
	}
	return cf
}

// classifyTarget decides what object a receiver expression denotes:
// this_object() (directly or through a macro), a string constant (a
// literal, a concatenation, or a macro that evaluates to one), or unknown.
func (p *Pass) classifyTarget(e syntax.Expr) (targetKind, string) {
	e = syntax.Unparen(e)
	switch x := e.(type) {
	case *syntax.CallExpr:
		if !x.Scoped && x.Fun.Name == "this_object" && len(x.Args) == 0 && !x.Opaque {
			return targetSelf, ""
		}
		return targetUnknown, ""
	case *syntax.Ident:
		body := p.Env.Expansion(x.Name)
		if body == nil {
			return targetUnknown, ""
		}
		if len(body) == 3 && body[0].Kind == token.Ident && body[0].Text == "this_object" &&
			body[1].Kind == token.LParen && body[2].Kind == token.RParen {
			return targetSelf, ""
		}
	case *syntax.BasicLit, *syntax.BinaryExpr, *syntax.AdjacentStrings:
	default:
		return targetUnknown, ""
	}
	if p.LibPath == "" {
		return targetUnknown, ""
	}
	first, last := e.Span()
	s, ok := p.Env.EvalString(pp.FileTokens(p.File, first, last))
	if !ok || s == "" {
		return targetUnknown, ""
	}
	return targetPath, p.Index.ObjectPath(s)
}

// lookup resolves the i'th string call through the index, once for all
// the rules that ask.
func (p *Pass) lookup(i int) (index.Lookup, string) {
	cf := p.calls()
	if cf.lookups[i] == nil {
		c, lk := cf.strings[i], &lookedUp{}
		switch c.Target {
		case targetSelf:
			lk.Lookup, lk.target = p.Index.LookupCallable(p.Ctx, p.LibPath, c.Func, true), p.LibPath+" (this_object)"
		case targetPath:
			lk.Lookup, lk.target = p.Index.LookupCallable(p.Ctx, c.TargetPath, c.Func, false), c.TargetPath
		}
		cf.lookups[i] = lk
	}
	return cf.lookups[i].Lookup, cf.lookups[i].target
}
