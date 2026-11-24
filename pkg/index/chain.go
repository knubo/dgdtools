package index

import (
	"context"
	"fmt"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/token"
)

const maxMacroDepth = 8

// EvalString evaluates a string-valued constant expression as the
// preprocessor and compiler would: string literals, '+', parentheses,
// adjacent literals, and object-like macros that themselves evaluate.
// Anything else is "unknown" (false).
func EvalString(expr string, view *pp.View, d token.Dialect) (string, bool) {
	return evalString(expr, view, d, 0)
}

func evalString(expr string, view *pp.View, d token.Dialect, depth int) (string, bool) {
	if depth > maxMacroDepth {
		return "", false
	}
	// Fast path: a single plain literal.
	if n := len(expr); n >= 2 && expr[0] == '"' && expr[n-1] == '"' && !strings.ContainsAny(expr[1:n-1], `"\`) {
		return expr[1 : n-1], true
	}
	f := lexer.Lex("", []byte(expr), d)
	var b strings.Builder
	operand := false // an operand was just read
	any := false
	for _, t := range f.Significant() {
		switch t.Kind {
		case token.LParen, token.RParen:
		case token.StringLit:
			s, ok := Unquote(string(f.Text(t)))
			if !ok {
				return "", false
			}
			b.WriteString(s)
			operand, any = true, true
		case token.Ident:
			if operand {
				return "", false
			}
			m := view.Lookup(string(f.Text(t)))
			if m == nil || m.FuncLike || m.Class == pp.ClassFragment {
				return "", false
			}
			s, ok := evalString(m.Body, view, d, depth+1)
			if !ok {
				return "", false
			}
			b.WriteString(s)
			operand, any = true, true
		case token.Plus:
			if !operand {
				return "", false
			}
			operand = false
		default:
			return "", false
		}
	}
	if !operand || !any {
		return "", false
	}
	return b.String(), true
}

// Unquote extracts a string literal's value. Literals with escapes beyond
// \\ and \" are rejected (never seen in function names or object paths).
func Unquote(lit string) (string, bool) {
	if len(lit) < 2 || lit[0] != '"' || lit[len(lit)-1] != '"' {
		return "", false
	}
	body := lit[1 : len(lit)-1]
	if !strings.ContainsRune(body, '\\') {
		return body, true
	}
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			b.WriteByte(body[i])
			continue
		}
		i++
		if i >= len(body) {
			return "", false
		}
		switch body[i] {
		case '\\', '"':
			b.WriteByte(body[i])
		default:
			return "", false
		}
	}
	return b.String(), true
}

// ---------------------------------------------------------------- chains

// Chain is the flattened inherit view of one object.
type Chain struct {
	Funcs map[string]ChainFunc
	// Partial: some inherit could not be resolved or loaded, or some
	// program in the chain has an incomplete symbol table. A name absent
	// from a partial chain is Unknown, not Missing.
	Partial bool
	Objects []string // lib paths in the chain, leaf last
}

type ChainFunc struct {
	Static, Private  bool
	PrototypeOnly    bool
	MinArgs, MaxArgs int
	Path             string // fs path of the defining file
	Off              uint32
	DefinedIn        string // lib path of the object that provides it
}

// Chain returns the flattened chain for a lib path.
func (ix *Index) Chain(libPath string) *Chain {
	ch, _ := ix.chain(libPath, map[string]bool{})
	return ch
}

// chain also reports whether an inheritance cycle was met below libPath;
// results computed inside a cycle are not memoized.
func (ix *Index) chain(libPath string, visiting map[string]bool) (*Chain, bool) {
	ix.mu.Lock()
	ch := ix.chains[libPath]
	ix.mu.Unlock()
	if ch != nil {
		return ch, false
	}
	ch = &Chain{Funcs: map[string]ChainFunc{}}
	if visiting[libPath] {
		ch.Partial = true // inheritance cycle; the driver rejects it
		return ch, true
	}
	u := ix.Unit(libPath)
	if u == nil {
		ch.Partial = true
		return ix.storeChain(libPath, ch), false
	}
	cyc := false
	if u.Partial {
		ch.Partial = true
	}
	visiting[libPath] = true
	for _, ref := range u.Inherits {
		if !ref.Resolved {
			ch.Partial = true
			continue
		}
		sub, c := ix.chain(ref.Path, visiting)
		cyc = cyc || c
		if sub.Partial {
			ch.Partial = true
		}
		// Private functions stay in the chain flagged Private so rules
		// can explain why a call fails. Everything below a `private
		// inherit` is call_other-invisible to inheritors too. A prototype
		// from one sibling never clobbers another sibling's definition.
		for name, fn := range sub.Funcs {
			if ref.Private {
				fn.Private = true
			}
			if fn.PrototypeOnly {
				if old, ok := ch.Funcs[name]; ok && !old.PrototypeOnly {
					continue
				}
			}
			ch.Funcs[name] = fn
		}
		ch.Objects = append(ch.Objects, sub.Objects...)
	}
	delete(visiting, libPath)
	for name, fi := range u.Funcs {
		if fi.PrototypeOnly {
			if old, ok := ch.Funcs[name]; ok && !old.PrototypeOnly {
				continue
			}
		}
		ch.Funcs[name] = ChainFunc{
			Static: fi.Static, Private: fi.Private, PrototypeOnly: fi.PrototypeOnly,
			MinArgs: fi.MinArgs, MaxArgs: fi.MaxArgs,
			Path: fi.Path, Off: fi.Off, DefinedIn: libPath,
		}
	}
	ch.Objects = append(ch.Objects, libPath)
	if cyc && len(visiting) > 0 {
		return ch, true
	}
	return ix.storeChain(libPath, ch), false
}

func (ix *Index) storeChain(libPath string, ch *Chain) *Chain {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if old := ix.chains[libPath]; old != nil {
		return old
	}
	ix.chains[libPath] = ch
	return ch
}

// AutoFuncs returns the functions every object gets from the configured
// auto objects.
func (ix *Index) AutoFuncs() map[string]ChainFunc {
	ix.mu.Lock()
	once := ix.autoOnce
	ix.mu.Unlock()
	once.Do(func() {
		m := map[string]ChainFunc{}
		for _, auto := range ix.Cfg.Lint.AutoObjects {
			for name, fn := range ix.Chain(NormalizeLibPath(auto, "/")).Funcs {
				fn.DefinedIn = "(auto object)"
				m[name] = fn
			}
		}
		ix.mu.Lock()
		ix.autoFuncs = m
		ix.mu.Unlock()
	})
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.autoFuncs
}

// UsersOf returns the objects whose inherit closure includes libPath, plus
// the objects that #include it as code. The second result is false when
// the whole-lib scan could not complete (cancelled); the answer is then
// unknown.
func (ix *Index) UsersOf(ctx context.Context, libPath string) ([]string, bool) {
	if !ix.LoadAll(ctx) {
		return nil, false
	}
	ix.mu.Lock()
	if !ix.usersOK {
		units := make(map[string]*Unit, len(ix.units))
		for _, e := range ix.units {
			if e.unit != nil && e.unit.LibPath != "" && e.unit.Funcs != nil {
				units[e.unit.LibPath] = e.unit
			}
		}
		users := map[string][]string{}
		closures := map[string][]string{}
		var closure func(p string, visiting map[string]bool) []string
		closure = func(p string, visiting map[string]bool) []string {
			if c, ok := closures[p]; ok {
				return c
			}
			if visiting[p] {
				return nil
			}
			visiting[p] = true
			var out []string
			if u := units[p]; u != nil {
				for _, ref := range u.Inherits {
					if !ref.Resolved {
						continue
					}
					out = append(out, ref.Path)
					out = append(out, closure(ref.Path, visiting)...)
				}
			}
			delete(visiting, p)
			closures[p] = out
			return out
		}
		for p := range units {
			seen := map[string]bool{}
			for _, anc := range closure(p, map[string]bool{}) {
				if anc != p && !seen[anc] {
					seen[anc] = true
					users[anc] = append(users[anc], p)
				}
			}
		}
		ix.users, ix.usersOK = users, true
	}
	out := append([]string(nil), ix.users[libPath]...)
	for u := range ix.incUsers[libPath] {
		out = append(out, u)
	}
	ix.mu.Unlock()
	return out, true
}

// IsIncluded reports whether some object #includes libPath's file as code.
func (ix *Index) IsIncluded(ctx context.Context, libPath string) bool {
	if !ix.LoadAll(ctx) {
		return true // unknown: assume the conservative answer
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return len(ix.incUsers[libPath]) > 0
}

// LookupState classifies a callable lookup.
type LookupState uint8

const (
	LookupUnknown     LookupState = iota // cannot verify: never report
	LookupFound                          // in the target's own chain (Fn valid)
	LookupFoundInUser                    // provided by an inheriting object
	LookupMissing                        // provably absent
)

// Lookup is the result of resolving one name in one object.
type Lookup struct {
	State LookupState
	Fn    ChainFunc // valid when State == LookupFound
	Note  string    // extra context for LookupMissing
}

// maxUserChains bounds the fallback search through inheritors' chains.
const maxUserChains = 20

// LookupCallable resolves function name fn in the object at target.
//
// self says the receiver is this_object() in code that lives in target:
// the running object may then be any inheritor (or includer) of target,
// so a name missing from target's own chain is looked for in its users
// too — the callback a module registers is routinely provided by a
// sibling module or the leaf. When the receiver is the named object
// itself ("/path"->fn()), only its own chain counts.
func (ix *Index) LookupCallable(ctx context.Context, target, fn string, self bool) Lookup {
	if ix.Root == "" || target == "" {
		return Lookup{State: LookupUnknown}
	}
	ch := ix.Chain(target)
	if f, ok := ch.Funcs[fn]; ok {
		return Lookup{State: LookupFound, Fn: f}
	}
	if f, ok := ix.AutoFuncs()[fn]; ok {
		return Lookup{State: LookupFound, Fn: f}
	}
	if ch.Partial {
		return Lookup{State: LookupUnknown}
	}
	if !self {
		return Lookup{State: LookupMissing}
	}
	users, ok := ix.UsersOf(ctx, target)
	if !ok {
		return Lookup{State: LookupUnknown}
	}
	if len(users) == 0 {
		return Lookup{State: LookupMissing}
	}
	for _, u := range users {
		if unit := ix.Unit(u); unit != nil {
			if _, ok := unit.Funcs[fn]; ok {
				return Lookup{State: LookupFoundInUser}
			}
		}
	}
	checked := 0
	for _, u := range users {
		if checked >= maxUserChains {
			return Lookup{State: LookupUnknown} // bounded: cannot verify
		}
		uch := ix.Chain(u)
		checked++
		if uch.Partial {
			return Lookup{State: LookupUnknown}
		}
		if _, ok := uch.Funcs[fn]; ok {
			return Lookup{State: LookupFoundInUser}
		}
	}
	return Lookup{State: LookupMissing,
		Note: fmt.Sprintf(" (nor in any of its %d inheritors)", len(users))}
}

// ProvidedByUsers reports whether any inheritor/includer of libPath
// supplies a real definition of fn. It returns true (assume provided)
// whenever the bounded search cannot decide.
func (ix *Index) ProvidedByUsers(ctx context.Context, libPath, fn string) bool {
	users, ok := ix.UsersOf(ctx, libPath)
	if !ok {
		return true
	}
	if len(users) == 0 {
		return false
	}
	for _, u := range users {
		if unit := ix.Unit(u); unit != nil {
			if f, ok := unit.Funcs[fn]; ok && !f.PrototypeOnly {
				return true
			}
		}
	}
	checked := 0
	for _, u := range users {
		if checked >= maxUserChains {
			return true
		}
		uch := ix.Chain(u)
		checked++
		if uch.Partial {
			return true
		}
		if f, ok := uch.Funcs[fn]; ok && !f.PrototypeOnly {
			return true
		}
	}
	return false
}
