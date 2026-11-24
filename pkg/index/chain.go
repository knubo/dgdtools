package index

import (
	"context"
	"fmt"
	"slices"
)

// Chain is the flattened inherit view of one object.
type Chain struct {
	Funcs map[string]ChainFunc
	Vars  map[string]ChainVar
	// Partial: some inherit could not be resolved or loaded, or some
	// program in the chain has an incomplete symbol table. A name absent
	// from a partial chain is Unknown, not Missing.
	Partial bool
	Objects []string // lib paths in the chain, leaf last
}

// ChainFunc is a function as an object's chain provides it.
type ChainFunc struct {
	FuncInfo
	DefinedIn string // lib path of the object that provides it
}

// ChainVar is a global variable as an object's chain provides it.
type ChainVar struct {
	VarInfo
	DefinedIn string // lib path of the object that declares it
}

// Chain returns the flattened chain for a lib path.
func (s *Snapshot) Chain(libPath string) *Chain {
	ch, _ := s.chain(libPath, map[string]bool{})
	return ch
}

// chain also reports whether an inheritance cycle was met below libPath;
// results computed inside a cycle are not memoized.
func (s *Snapshot) chain(libPath string, visiting map[string]bool) (*Chain, bool) {
	if ch, ok := s.chains.get(libPath); ok {
		return ch, false
	}
	ch := &Chain{Funcs: map[string]ChainFunc{}, Vars: map[string]ChainVar{}}
	if visiting[libPath] {
		ch.Partial = true // inheritance cycle; the driver rejects it
		return ch, true
	}
	u := s.Unit(libPath)
	if u == nil {
		ch.Partial = true
		return s.storeChain(libPath, ch), false
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
		sub, c := s.chain(ref.Path, visiting)
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
		for name, v := range sub.Vars {
			if ref.Private {
				v.Private = true
			}
			ch.Vars[name] = v
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
		ch.Funcs[name] = ChainFunc{FuncInfo: fi, DefinedIn: libPath}
	}
	for name, vi := range u.Vars {
		ch.Vars[name] = ChainVar{VarInfo: vi, DefinedIn: libPath}
	}
	ch.Objects = append(ch.Objects, libPath)
	if cyc && len(visiting) > 0 {
		return ch, true
	}
	return s.storeChain(libPath, ch), false
}

// storeChain keeps ch as libPath's chain unless one was stored meanwhile.
func (s *Snapshot) storeChain(libPath string, ch *Chain) *Chain {
	return s.chains.getOrPut(libPath, func() *Chain { return ch })
}

// autoState is what the configured auto objects give every object.
type autoState struct {
	funcs map[string]ChainFunc
	// members are the lib paths of the auto objects, of the programs they
	// inherit, and of the files those include as code.
	members map[string]bool
}

func (s *Snapshot) autoState() *autoState {
	return s.auto.load(func() *autoState {
		a := &autoState{funcs: map[string]ChainFunc{}, members: map[string]bool{}}
		for _, auto := range s.cfg.AutoObjects {
			ch := s.Chain(NormalizeLibPath(auto, "/"))
			for name, fn := range ch.Funcs {
				fn.DefinedIn = "(auto object)"
				a.funcs[name] = fn
			}
			for _, o := range ch.Objects {
				a.members[o] = true
				if u := s.Unit(o); u != nil {
					for _, c := range u.code {
						a.members[c] = true
					}
				}
			}
		}
		return a
	})
}

// AutoFuncs returns the functions every object gets from the configured
// auto objects.
func (s *Snapshot) AutoFuncs() map[string]ChainFunc { return s.autoState().funcs }

// UsersOf returns the objects whose program includes libPath's: those
// that inherit it, however indirectly, and those that #include it as
// code. It needs the whole-lib scan and runs it if it has not completed.
// The second result is false when the answer cannot be had: the scan was
// cancelled, or the index has moved on to a newer generation.
func (s *Snapshot) UsersOf(ctx context.Context, libPath string) ([]string, bool) {
	if !s.Scan(ctx) {
		return nil, false
	}
	return s.users(libPath)
}

// users answers UsersOf from the index's reverse edges, which are those
// of this generation only while it is the current one.
func (s *Snapshot) users(libPath string) ([]string, bool) {
	ix := s.ix
	ix.mu.RLock()
	if ix.cur != s || !ix.walked || len(ix.todo) > 0 {
		ix.mu.RUnlock()
		return nil, false
	}
	var out []string
	seen := map[string]bool{libPath: true}
	for queue := []string{libPath}; len(queue) > 0; queue = queue[1:] {
		for key := range ix.children[queue[0]] {
			if u, _ := s.units.get(key); u != nil && !seen[u.LibPath] {
				seen[u.LibPath] = true
				out = append(out, u.LibPath)
				queue = append(queue, u.LibPath)
			}
		}
	}
	ix.mu.RUnlock()
	slices.Sort(out)
	return out, true
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

// usersFunc answers UsersOf, or declines to.
type usersFunc func(ctx context.Context, libPath string) ([]string, bool)

// LookupCallable resolves function name fn in the object at target.
//
// self says the receiver is this_object() in code that lives in target:
// the running object may then be any inheritor (or includer) of target,
// so a name missing from target's own chain is looked for in its users
// too — the callback a module registers is routinely provided by a
// sibling module or the leaf. When the receiver is the named object
// itself ("/path"->fn()), only its own chain counts.
func (s *Snapshot) LookupCallable(ctx context.Context, target, fn string, self bool) Lookup {
	return s.lookupCallable(ctx, target, fn, self, s.UsersOf)
}

func (s *Snapshot) lookupCallable(ctx context.Context, target, fn string, self bool, usersOf usersFunc) Lookup {
	if !s.HasLib() || target == "" {
		return Lookup{State: LookupUnknown}
	}
	ch := s.Chain(target)
	if f, ok := ch.Funcs[fn]; ok {
		return Lookup{State: LookupFound, Fn: f}
	}
	auto := s.autoState()
	if f, ok := auto.funcs[fn]; ok {
		return Lookup{State: LookupFound, Fn: f}
	}
	if ch.Partial {
		return Lookup{State: LookupUnknown}
	}
	if !self {
		// A profile stub lists an object's public interface only.
		if u := s.Unit(target); u != nil && u.Stub {
			return Lookup{State: LookupMissing, Note: " or is private there (a profile lists public functions only)"}
		}
		return Lookup{State: LookupMissing}
	}
	// Code in the auto object's own chain runs as every object in the
	// lib: this_object() there can be anything.
	if auto.members[target] {
		return Lookup{State: LookupUnknown}
	}
	users, ok := usersOf(ctx, target)
	if !ok {
		return Lookup{State: LookupUnknown}
	}
	if len(users) == 0 {
		if s.partial {
			// Only the checkout could be searched for inheritors.
			return Lookup{State: LookupMissing, Note: " (nor in this checkout; inheritors elsewhere in the lib cannot be seen)"}
		}
		return Lookup{State: LookupMissing}
	}
	for _, u := range users {
		if unit := s.Unit(u); unit != nil {
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
		uch := s.Chain(u)
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
func (s *Snapshot) ProvidedByUsers(ctx context.Context, libPath, fn string) bool {
	return s.providedByUsers(ctx, libPath, fn, s.UsersOf)
}

func (s *Snapshot) providedByUsers(ctx context.Context, libPath, fn string, usersOf usersFunc) bool {
	users, ok := usersOf(ctx, libPath)
	if !ok {
		return true
	}
	if len(users) == 0 {
		return false
	}
	for _, u := range users {
		if unit := s.Unit(u); unit != nil {
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
		uch := s.Chain(u)
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
