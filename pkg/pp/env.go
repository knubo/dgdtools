package pp

import (
	"slices"
	"strings"
	"sync"

	"github.com/mmcdole/dgdtools/pkg/token"
)

// Env is the #define environment of one translation unit, in the order
// the preprocessor builds it: the names defined before the first line,
// then the tables of the files read, in the order their definitions take
// effect, and last the table of the file itself.
//
// An Env is built by one goroutine: Add the tables of what is read before
// the file (the driver's force-included header), then Scan the file.
// Once Scan has returned it may be read from any number of goroutines;
// the Tables it points at are immutable once their own Scan has returned,
// and shared.
type Env struct {
	// Complete is true when every file that contributes definitions was
	// read, so that "not defined here" means "not a macro". While a Scan
	// is running it speaks for the part of the unit read so far.
	Complete bool
	// Header says the file is not compiled by itself, only as part of the
	// files that include it: what they define before it is not known, so
	// no name is certainly undefined in it.
	Header bool

	defined map[string]bool  // names defined before the first line
	classes map[string]Class // configured classes; they override bodies
	layers  []*Table         // the files read before and included, in order of effect
	own     *Table           // the file itself; nil until Scan

	mu    sync.Mutex
	cache map[string]*Macro // what Lookup has answered
}

// NewEnv returns the environment before the first line of a unit: defined
// are the names the driver and the project predefine, classes the macro
// classes the project configures. Neither map is modified.
func NewEnv(defined map[string]bool, classes map[string]Class) *Env {
	return &Env{defined: defined, classes: classes}
}

// Add appends the tables of files whose definitions take effect next: a
// header and what it includes, in the order they were read.
func (e *Env) Add(tables ...*Table) {
	e.layers = append(e.layers, tables...)
	e.forget()
}

// forget drops Lookup's answers: the environment changed.
func (e *Env) forget() {
	e.mu.Lock()
	e.cache = nil
	e.mu.Unlock()
}

// Own returns the table of the file the environment was scanned for, nil
// before Scan.
func (e *Env) Own() *Table { return e.own }

// tables calls yield for every table from the latest to take effect to
// the earliest, until yield returns false.
func (e *Env) tables(yield func(*Table) bool) {
	if e.own != nil && !yield(e.own) {
		return
	}
	for i := len(e.layers) - 1; i >= 0; i-- {
		if !yield(e.layers[i]) {
			return
		}
	}
}

// Lookup returns the definition of name as the file's code sees it, or
// nil when name is not a macro. The answer is about the whole file, not a
// point in it: a name with conflicting definitions comes back as
// ClassFragment, as does one the file #undefs and leaves undefined.
func (e *Env) Lookup(name string) *Macro {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if m, ok := e.cache[name]; ok {
		return m
	}
	var found *Macro
	if c, ok := e.classes[name]; ok {
		found = &Macro{Name: name, Class: c}
	} else {
		e.tables(func(t *Table) bool {
			m := t.Macros[name]
			if m == nil {
				return true
			}
			if found == nil {
				found = m
				// A file that #undef'd the name defined it anew on purpose.
				return !t.Undefs[name]
			}
			if m.Class != found.Class || m.FuncLike != found.FuncLike {
				amb := *found
				amb.Class = ClassFragment
				found = &amb
				return false
			}
			return true
		})
	}
	if e.cache == nil {
		e.cache = map[string]*Macro{}
	}
	e.cache[name] = found
	return found
}

// Definition returns the #define that name has at the end of the
// environment: the latest one read, the file's own before those of the
// files it includes and of the files read before it. Unlike Lookup it
// says where the name is defined and not what the parser is to take it
// for: a class the project configures for the name does not hide the
// definition, and two definitions that disagree are not made one. It is
// nil when no file read defines the name: it is no macro, or one the
// driver or the project defines.
func (e *Env) Definition(name string) *Macro {
	if e == nil {
		return nil
	}
	var found *Macro
	e.tables(func(t *Table) bool {
		found = t.Macros[name]
		return found == nil
	})
	return found
}

// Names returns the names that are macros to the file's code, sorted:
// those a file read defines, but for one the file itself #undefs and
// leaves undefined, and those the project configures a class for. A
// name that is only predefined (a name for #ifdef) is not among them.
func (e *Env) Names() []string {
	if e == nil {
		return nil
	}
	set := map[string]bool{}
	for name := range e.classes {
		set[name] = true
	}
	e.tables(func(t *Table) bool {
		for name := range t.Macros {
			set[name] = true
		}
		return true
	})
	names := make([]string, 0, len(set))
	for name := range set {
		if def := e.Definition(name); def != nil && def.Undefined {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// state reports what name is at the end of the environment as it stands,
// which during a Scan is the directive being read: whether it is defined,
// the definition when a file gave one (nil for a predefined or configured
// name), and whether all of that is certain.
func (e *Env) state(name string) (m *Macro, defined, known bool) {
	if _, ok := e.classes[name]; ok {
		return nil, true, true
	}
	decided := false
	e.tables(func(t *Table) bool {
		m = t.Macros[name]
		if m != nil && m.Undefined {
			m = nil
		}
		switch {
		case t.Unsure[name]:
			defined, known = m != nil, false
		case m != nil:
			defined, known = true, true
		case t.Undefs[name]:
			defined, known = false, true
		default:
			return true
		}
		decided = true
		return false
	})
	switch {
	case decided:
		return m, defined, known
	case e.defined[name]:
		return nil, true, true
	}
	return nil, false, e.Complete && !e.Header
}

// Defined reports whether name is defined at the end of the environment
// as it stands, and whether that is certain. A name no file defines is
// certainly undefined only in the complete environment of a file that is
// compiled by itself.
func (e *Env) Defined(name string) (defined, known bool) {
	_, defined, known = e.state(name)
	return defined, known
}

// Selection is the single preprocessor configuration every consumer of a
// file agrees on.
type Selection struct {
	// Sig are the indexes of active significant tokens (no trivia, no
	// directives, no EOF).
	Sig []int
	// Dirs are the indexes of active directive tokens.
	Dirs []int
	// Conds are the file's conditionals, in the order of their #if.
	Conds []Cond

	guessed [][2]int // token ranges inside branches chosen by guess
}

// Cond is one #if ... #endif.
type Cond struct {
	// Arms are the branches: the #if, #ifdef or #ifndef, then each #elif
	// and #else. An arm reaches from its directive to the next arm's, the
	// last one to End.
	Arms []Arm
	// End is the token index of the #endif, or of the EOF token when the
	// conditional is never closed.
	End int
}

// Arm is one branch of a conditional.
type Arm struct {
	Dir   int  // token index of the directive that opens the branch
	Taken bool // the branch is the active one
}

// Guessed reports whether the token at index tok lies in a conditional
// branch that was selected by the first-branch rule rather than decided.
// Whether such code is compiled at all is unknown.
func (s *Selection) Guessed(tok int) bool {
	lo, hi := 0, len(s.guessed)
	for lo < hi {
		m := (lo + hi) / 2
		switch r := s.guessed[m]; {
		case tok < r[0]:
			hi = m
		case tok > r[1]:
			lo = m + 1
		default:
			return true
		}
	}
	return false
}

// certainty says whether the code at a point of a file is compiled.
type certainty uint8

const (
	live  certainty = iota // it is
	maybe                  // it depends on a condition that could not be decided
	dead                   // it is not
)

// frame is one open conditional while scanning.
type frame struct {
	cond        int // index in Selection.Conds
	outerActive bool
	outer       certainty // of the code around the conditional
	arm         certainty // of the current arm
	// taken says an earlier arm (or this one) was selected; takenFor is
	// how certain it is that the arms after it are not compiled.
	taken    bool
	takenFor certainty // live: certainly taken; maybe: by guess; dead: no arm taken yet
}

// Scan reads f's directives into the environment, in order, and selects
// the active branch of every conditional. f's own table is the
// environment's latest from the first directive on (see Own).
//
// A condition is decided when its value is certain in the environment at
// that point: `#if 0`, a name defined before the first line, by a file
// read before, or earlier in f, and, in the complete environment of a
// file that is not a header, a name defined nowhere. Anything else takes
// the first branch, and the tokens
// of that branch are marked Guessed: they are parsed, but nothing is
// concluded from errors in them.
//
// include is called for each active #include as it is met, before the
// directives after it are read; it adds the tables of the included files
// (Add) and clears Complete when they cannot all be read. With a nil
// include the file is read on its own.
func (e *Env) Scan(f *token.File, include func(Include)) *Selection {
	return e.scan(f, include, nil)
}

// ScanArms is Scan with the conditionals decided by the caller: an arm
// whose directive token is in take is selected or not as take says, and
// the others are decided as Scan decides them. It is how a tool that
// must understand every branch of a file (the formatter) reads the ones
// the preprocessor would skip.
func (e *Env) ScanArms(f *token.File, take map[int]bool) *Selection {
	return e.scan(f, nil, take)
}

func (e *Env) scan(f *token.File, include func(Include), take map[int]bool) *Selection {
	sel := &Selection{}
	t := &Table{Path: f.Path, Macros: map[string]*Macro{}, Undefs: map[string]bool{}}
	e.own = t
	e.forget()
	defer e.forget()

	var stack []frame
	active, here := true, live
	gstart := -1
	mark := func(i int) {
		g := active && here == maybe
		if g && gstart < 0 {
			gstart = i
		} else if !g && gstart >= 0 {
			sel.guessed = append(sel.guessed, [2]int{gstart, i})
			gstart = -1
		}
	}
	// enter starts the arm that directive i opens in the innermost
	// conditional. taken is the decision on its condition and sure
	// whether that is certain; (false, false) is a condition that was not
	// evaluated.
	enter := func(i int, taken, sure bool) {
		fr := &stack[len(stack)-1]
		c := &sel.Conds[fr.cond]
		switch {
		case fr.outer == dead || fr.takenFor == live:
			fr.arm = dead
		case sure && !taken:
			fr.arm = dead
		case sure && fr.takenFor == dead:
			fr.arm = fr.outer
		default:
			fr.arm = maybe
		}
		taken = taken && fr.outerActive && !fr.taken
		if taken {
			fr.taken = true
			fr.takenFor = live
			if !sure {
				fr.takenFor = maybe
			}
		}
		c.Arms = append(c.Arms, Arm{Dir: i, Taken: taken})
		active, here = taken, fr.arm
		mark(i)
	}
	firstDirective := true
	for i, tok := range f.Tokens {
		if tok.Kind != token.Directive {
			if active && !tok.Kind.IsTrivia() && tok.Kind != token.EOF {
				sel.Sig = append(sel.Sig, i)
			}
			continue
		}
		name, rest := SplitDirective(f.Text(tok))
		first := firstDirective && len(sel.Sig) == 0
		firstDirective = false
		switch name {
		case "if", "ifdef", "ifndef":
			sel.Conds = append(sel.Conds, Cond{End: len(f.Tokens) - 1})
			stack = append(stack, frame{cond: len(sel.Conds) - 1, outerActive: active, outer: here, takenFor: dead})
			taken, sure := false, false // not evaluated inside skipped code
			if v, forced := take[i]; forced {
				taken, sure = v, true
			} else if active {
				taken, sure = e.decide(name, rest, f.Dialect)
				if name == "ifndef" && first && !sure && isGuard(f, i, firstWord(strings.TrimSpace(rest))) {
					sure = true // an include guard: true the first time through
				}
			}
			enter(i, taken, sure)
			continue
		case "elif", "else":
			if len(stack) == 0 {
				continue
			}
			fr := &stack[len(stack)-1]
			taken, sure := false, false
			switch v, forced := take[i]; {
			case forced:
				taken, sure = v, true
			case !fr.outerActive:
			case fr.takenFor == live:
				sure = true
			case name == "else":
				taken, sure = true, true
			default:
				taken, sure = e.decide("if", rest, f.Dialect)
			}
			enter(i, taken, sure)
			continue
		case "endif":
			if n := len(stack); n > 0 {
				fr := stack[n-1]
				sel.Conds[fr.cond].End = i
				active, here = fr.outerActive, fr.outer
				stack = stack[:n-1]
			}
			mark(i)
			continue
		}

		// Definitions in a branch that may or may not be compiled leave
		// the name's state unknown, whichever branch was selected.
		if here == maybe && (name == "define" || name == "undef") {
			if n := firstWord(strings.TrimLeft(rest, " \t")); n != "" {
				if t.Unsure == nil {
					t.Unsure = map[string]bool{}
				}
				t.Unsure[n] = true
			}
		}
		if !active {
			continue
		}
		sel.Dirs = append(sel.Dirs, i)
		switch name {
		case "define":
			m := parseDefine(rest, f.Dialect)
			if m == nil {
				continue
			}
			m.Path, m.Off = f.Path, tok.Off
			m.NameOff = tok.Off + uint32(ArgOffset(f.Text(tok)))
			if old := t.Macros[m.Name]; old != nil && !old.Undefined &&
				(old.Class != m.Class || old.FuncLike != m.FuncLike) {
				m.Class = ClassFragment // conflicting definitions: trust neither
			}
			t.Macros[m.Name] = m
		case "undef":
			// `#undef X` then `#define X` is the ordinary way to
			// redefine; an #undef that is never followed by a new
			// definition leaves X a macro for only part of the file.
			n := firstWord(strings.TrimSpace(rest))
			if n == "" {
				continue
			}
			if old := t.Macros[n]; old != nil {
				old.Class, old.Undefined = ClassFragment, true
			}
			t.Undefs[n] = true
		case "pragma":
			if strings.Join(strings.Fields(rest), " ") == "dgdtools partial" {
				t.Partial = true
			}
		case "include":
			inc, ok := parseInclude(rest)
			if !ok {
				continue
			}
			inc.Tok, inc.Off = i, tok.Off
			t.Includes = append(t.Includes, inc)
			if include != nil {
				include(inc)
			}
		}
	}
	if gstart >= 0 {
		sel.guessed = append(sel.guessed, [2]int{gstart, len(f.Tokens)})
	}
	return sel
}

// isGuard reports whether the directive after token i is `#define name`:
// with `#ifndef name` as the first thing in the file, an include guard.
func isGuard(f *token.File, i int, name string) bool {
	for j := i + 1; j < len(f.Tokens); j++ {
		t := f.Tokens[j]
		if t.Kind.IsTrivia() {
			continue
		}
		if t.Kind != token.Directive {
			return false
		}
		n, rest := SplitDirective(f.Text(t))
		return n == "define" && name != "" && firstWord(strings.TrimLeft(rest, " \t")) == name
	}
	return false
}

// decide evaluates the condition of an #if, #ifdef or #ifndef line. sure
// is false when the answer is the first-branch guess rather than a
// certainty.
func (e *Env) decide(name, rest string, d token.Dialect) (taken, sure bool) {
	switch name {
	case "ifdef", "ifndef":
		defined, known := e.Defined(firstWord(strings.TrimSpace(rest)))
		if !known {
			return true, false
		}
		return defined == (name == "ifdef"), true
	}
	v, known := (&eval{env: e, toks: Tokens(rest, d), directive: true}).constant()
	if !known || v.isString {
		return true, false
	}
	return v.n != 0, true
}
