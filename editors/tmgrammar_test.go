package editors

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A small interpreter of the TextMate grammar format, enough to run the
// grammar of this repository in a Go test: match rules, begin/end rules
// with name and contentName, captures (a scope name, or patterns of their
// own), and includes of repository rules. What it leaves out the grammar
// may not use, and loadGrammar says so: $self and $base, while,
// applyEndPatternLast, injections, backreferences from end to begin.
//
// The patterns are Oniguruma's in an editor and Go's here. Both agree on
// the syntax the grammar is held to (compilePattern), with one addition
// that Go lacks and this file supplies: (?=...) at the end of a top-level
// alternative.

type tmGrammar struct {
	Schema     string             `json:"$schema"`
	Name       string             `json:"name"`
	ScopeName  string             `json:"scopeName"`
	Comment    string             `json:"comment"`
	Patterns   []*tmRule          `json:"patterns"`
	Repository map[string]*tmRule `json:"repository"`
}

type tmRule struct {
	Comment       string             `json:"comment"`
	Name          string             `json:"name"`
	ContentName   string             `json:"contentName"`
	Match         string             `json:"match"`
	Begin         string             `json:"begin"`
	End           string             `json:"end"`
	Captures      map[string]*tmRule `json:"captures"`
	BeginCaptures map[string]*tmRule `json:"beginCaptures"`
	EndCaptures   map[string]*tmRule `json:"endCaptures"`
	Patterns      []*tmRule          `json:"patterns"`
	Include       string             `json:"include"`

	match, begin, end *tmPattern
}

// loadGrammar reads a grammar and compiles its patterns. An error names
// the rule: a key the format does not have, a rule of no kind or of two,
// an include of nothing, a pattern outside the common syntax, a capture
// of a group the pattern does not have.
func loadGrammar(path string) (*tmGrammar, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	g := &tmGrammar{}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(g); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(g.Patterns) == 0 {
		return nil, fmt.Errorf("%s: no patterns", path)
	}
	for i, r := range g.Patterns {
		if err := g.prepare(r, fmt.Sprintf("patterns[%d]", i), false); err != nil {
			return nil, err
		}
	}
	for key, r := range g.Repository {
		if err := g.prepare(r, "#"+key, false); err != nil {
			return nil, err
		}
	}
	return g, nil
}

var scopeName = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z0-9-]+)*$`)

func (g *tmGrammar) prepare(r *tmRule, where string, capture bool) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%s: %s", where, fmt.Sprintf(format, args...))
	}
	for _, name := range []string{r.Name, r.ContentName} {
		if name == "" {
			continue
		}
		if !scopeName.MatchString(name) || !strings.HasSuffix(name, "."+strings.TrimPrefix(g.ScopeName, "source.")) {
			return fail("scope name %q: one dotted name ending in the language's", name)
		}
	}
	if capture {
		// A capture is a name, or patterns for the text it captured.
		if r.Match != "" || r.Begin != "" || r.End != "" || r.Include != "" || r.ContentName != "" ||
			r.Captures != nil || r.BeginCaptures != nil || r.EndCaptures != nil {
			return fail("a capture has a name or patterns, nothing else")
		}
		if r.Name == "" && len(r.Patterns) == 0 {
			return fail("a capture with nothing in it")
		}
		for i, p := range r.Patterns {
			if err := g.prepare(p, fmt.Sprintf("%s patterns[%d]", where, i), false); err != nil {
				return err
			}
		}
		return nil
	}
	var err error
	compile := func(src string) *tmPattern {
		if err != nil {
			return nil
		}
		var p *tmPattern
		if p, err = compilePattern(src); err != nil {
			err = fail("%v", err)
		}
		return p
	}
	captures := func(key string, caps map[string]*tmRule, ps ...*tmPattern) error {
		for n, c := range caps {
			i, convErr := strconv.Atoi(n)
			if convErr != nil || i < 0 || strconv.Itoa(i) != n {
				return fail("%s: %q is not a group number", key, n)
			}
			for _, p := range ps {
				if i > p.groups {
					return fail("%s: group %d of a pattern with %d: %s", key, i, p.groups, p.src)
				}
			}
			if err := g.prepare(c, fmt.Sprintf("%s %s.%s", where, key, n), true); err != nil {
				return err
			}
		}
		return nil
	}
	switch {
	case r.Include != "":
		if r.Match != "" || r.Begin != "" || r.End != "" || r.Name != "" || r.ContentName != "" || r.Patterns != nil ||
			r.Captures != nil || r.BeginCaptures != nil || r.EndCaptures != nil {
			return fail("an include with something beside it")
		}
		if !strings.HasPrefix(r.Include, "#") || g.Repository[r.Include[1:]] == nil {
			return fail("include %q: not a rule of the repository", r.Include)
		}
		return nil
	case r.Match != "":
		if r.Begin != "" || r.End != "" || r.ContentName != "" || r.Patterns != nil || r.BeginCaptures != nil || r.EndCaptures != nil {
			return fail("a match rule with the keys of a begin/end rule")
		}
		r.match = compile(r.Match)
		if err != nil {
			return err
		}
		if err := captures("captures", r.Captures, r.match); err != nil {
			return err
		}
	case r.Begin != "":
		if r.End == "" {
			return fail("begin without end")
		}
		r.begin, r.end = compile(r.Begin), compile(r.End)
		if err != nil {
			return err
		}
		if r.Captures != nil && (r.BeginCaptures != nil || r.EndCaptures != nil) {
			return fail("captures beside beginCaptures or endCaptures")
		}
		if err := captures("captures", r.Captures, r.begin, r.end); err != nil {
			return err
		}
		if err := captures("beginCaptures", r.BeginCaptures, r.begin); err != nil {
			return err
		}
		if err := captures("endCaptures", r.EndCaptures, r.end); err != nil {
			return err
		}
	default:
		if r.End != "" || r.ContentName != "" || r.Captures != nil || r.BeginCaptures != nil || r.EndCaptures != nil {
			return fail("neither match, begin nor include, yet not a plain list of patterns")
		}
		if len(r.Patterns) == 0 {
			return fail("a rule of no kind")
		}
	}
	for i, p := range r.Patterns {
		if err := g.prepare(p, fmt.Sprintf("%s patterns[%d]", where, i), false); err != nil {
			return err
		}
	}
	return nil
}

// walk calls fn for every rule of the grammar that is written out in it
// (not for what an include refers to), captures included.
func (g *tmGrammar) walk(fn func(where string, r *tmRule)) {
	var visit func(where string, r *tmRule)
	visit = func(where string, r *tmRule) {
		fn(where, r)
		for i, p := range r.Patterns {
			visit(fmt.Sprintf("%s patterns[%d]", where, i), p)
		}
		for _, caps := range []map[string]*tmRule{r.Captures, r.BeginCaptures, r.EndCaptures} {
			for n, c := range caps {
				visit(where+" capture "+n, c)
			}
		}
	}
	for i, r := range g.Patterns {
		visit(fmt.Sprintf("patterns[%d]", i), r)
	}
	for key, r := range g.Repository {
		visit("#"+key, r)
	}
}

// leaves returns the rules that can match where the patterns ps apply,
// in order: includes followed, lists of patterns flattened.
func (g *tmGrammar) leaves(ps []*tmRule, out []*tmRule) []*tmRule {
	for _, p := range ps {
		switch {
		case p.Include != "":
			out = g.leaves([]*tmRule{g.Repository[p.Include[1:]]}, out)
		case p.match != nil || p.begin != nil:
			out = append(out, p)
		default:
			out = g.leaves(p.Patterns, out)
		}
	}
	return out
}

// tmPattern is one pattern of the grammar, compiled for Go.
type tmPattern struct {
	src string
	// first finds the leftmost match in a line searched from its start;
	// later, in a line searched from further in, takes the character
	// before the search as context, so that \b sees it and ^ does not
	// match.
	first, later *regexp.Regexp
	groups       int // the pattern's own capturing groups
	looks        int // its lookaheads, compiled as groups after them
}

// compilePattern accepts the syntax Go's regexp and Oniguruma read alike,
// and nothing either reads differently:
//
//   - no \d \w \s \h and their negations (ASCII in one, Unicode in the
//     other): character classes are written out;
//   - no $ (Oniguruma's also matches before a final newline): a line ends
//     in \n, and a pattern says so;
//   - ^ only as a pattern's first character;
//   - groups are (...) and (?:...); no names, no flags, no lookbehind, no
//     negative lookahead, no atomic groups, no backreferences;
//   - (?=...) only at the end of a top-level alternative, with no
//     capturing group in or after it. Go has no lookahead: it is compiled
//     as a trailing group, and the match ends where that group begins,
//     which is what a lookahead in that position means.
//
// \b is allowed and is ASCII here: the two differ only beside a letter
// that is not ASCII, which no token of the language has.
func compilePattern(src string) (*tmPattern, error) {
	p := &tmPattern{src: src}
	fail := func(i int, format string, args ...any) (*tmPattern, error) {
		return nil, fmt.Errorf("pattern %q, offset %d: %s", src, i, fmt.Sprintf(format, args...))
	}
	var out strings.Builder
	depth, look := 0, -1 // look: the depth a lookahead was opened at
	inClass := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '\\':
			if i+1 == len(src) {
				return fail(i, "a backslash at the end")
			}
			i++
			e := src[i]
			switch {
			case strings.IndexByte(`dDwWsShHGAZzKRXkgpPCQE`, e) >= 0, e >= '1' && e <= '9', e == 'b' && inClass, e == 'B':
				return fail(i-1, `\%c is not in the common syntax`, e)
			}
			out.WriteByte('\\')
			out.WriteByte(e)
			continue
		case inClass:
			if c == '[' {
				return fail(i, "'[' inside a character class: escape it")
			}
			if c == ']' {
				inClass = false
			}
		case c == '[':
			inClass = true
			if i+1 < len(src) && src[i+1] == '^' {
				out.WriteByte(c)
				i++
				c = src[i]
			}
			if i+1 < len(src) && src[i+1] == ']' {
				return fail(i, "']' first in a character class: escape it")
			}
		case c == '(':
			switch {
			case strings.HasPrefix(src[i:], "(?:"):
			case strings.HasPrefix(src[i:], "(?="):
				if depth != 0 || look >= 0 {
					return fail(i, "(?=...) is allowed only at the end of a top-level alternative")
				}
				look = depth
				p.looks++
				out.WriteByte('(')
				i += 2
				depth++
				continue
			case strings.HasPrefix(src[i:], "(?"):
				return fail(i, "this kind of group is not in the common syntax")
			default:
				if p.looks > 0 {
					return fail(i, "a capturing group in or after a lookahead")
				}
				p.groups++
			}
			depth++
		case c == ')':
			depth--
			if depth < 0 {
				return fail(i, "unbalanced ')'")
			}
			if depth == look {
				look = -1
				if i+1 < len(src) && src[i+1] != '|' {
					return fail(i, "(?=...) is allowed only at the end of a top-level alternative")
				}
			}
		case c == '$':
			return fail(i, "'$' is not in the common syntax: match \\n")
		case c == '^' && i != 0:
			return fail(i, "'^' anywhere but first")
		}
		out.WriteByte(c)
	}
	if depth != 0 || inClass {
		return fail(len(src), "unbalanced")
	}
	var err error
	if p.first, err = regexp.Compile(`\A(?s:.*?)(` + out.String() + `)`); err != nil {
		return nil, fmt.Errorf("pattern %q: %w", src, err)
	}
	p.later = regexp.MustCompile(`\A(?s:.)(?s:.*?)(` + out.String() + `)`)
	return p, nil
}

// find returns the leftmost match of the pattern in line that begins at
// pos or after it, as the offsets of the match and of its groups (-1 for
// a group that took no part), or nil.
func (p *tmPattern) find(line string, pos int) []int {
	var m []int
	base := 0
	if pos == 0 {
		m = p.first.FindStringSubmatchIndex(line)
	} else {
		_, size := utf8.DecodeLastRuneInString(line[:pos])
		base = pos - size
		m = p.later.FindStringSubmatchIndex(line[base:])
	}
	if m == nil {
		return nil
	}
	// m[0:2] is the wrapper's match, m[2:4] the pattern's, then its
	// groups, then the lookaheads.
	out := make([]int, 2*(p.groups+1))
	for i := range out {
		if out[i] = m[i+2]; out[i] >= 0 {
			out[i] += base
		}
	}
	for i := 0; i < p.looks; i++ {
		if s := m[2*(2+p.groups+i)]; s >= 0 {
			out[1] = s + base
		}
	}
	return out
}

// tmToken is a stretch of a line and the scopes it has, outermost first.
// The grammar's own scope, which everything has, is left out.
type tmToken struct {
	start, end int
	scopes     []string
}

// tmFrame is one open rule on the stack.
type tmFrame struct {
	rule     *tmRule // nil for the grammar itself and inside a capture
	patterns []*tmRule
	outer    []string // the scopes of the rule's begin and end
	inner    []string // and of what is between them
}

type tmTokenizer struct {
	g     *tmGrammar
	stack []tmFrame
	toks  []tmToken
	last  int
}

func newTokenizer(g *tmGrammar) *tmTokenizer {
	return &tmTokenizer{g: g, stack: []tmFrame{{patterns: g.Patterns}}}
}

func with(scopes []string, name string) []string {
	if name == "" {
		return scopes
	}
	return append(scopes[:len(scopes):len(scopes)], name)
}

// produce closes the token that ends at end.
func (t *tmTokenizer) produce(end int, scopes []string) {
	if end > t.last {
		t.toks = append(t.toks, tmToken{t.last, end, scopes})
		t.last = end
	}
}

// line tokenizes one line, which ends in "\n" as an editor hands it to a
// grammar, and returns its tokens. The open rules carry over to the next
// call.
func (t *tmTokenizer) line(line string) ([]tmToken, error) {
	t.toks, t.last = nil, 0
	var err error
	t.stack, err = t.run(line, 0, t.stack)
	return t.toks, err
}

func (t *tmTokenizer) run(line string, pos int, stack []tmFrame) ([]tmFrame, error) {
	for pos < len(line) {
		top := stack[len(stack)-1]
		var best []int
		var rule *tmRule // nil when the best match is top's end
		if top.rule != nil {
			best = top.rule.end.find(line, pos)
		}
		for _, r := range t.g.leaves(top.patterns, nil) {
			p := r.match
			if p == nil {
				p = r.begin
			}
			if m := p.find(line, pos); m != nil && (best == nil || m[0] < best[0]) {
				best, rule = m, r
			}
		}
		if best == nil {
			break
		}
		t.produce(best[0], top.inner)
		var err error
		switch {
		case rule == nil:
			caps := top.rule.EndCaptures
			if caps == nil {
				caps = top.rule.Captures
			}
			err = t.captures(line, best, top.outer, caps)
			t.produce(best[1], top.outer)
			stack = stack[:len(stack)-1]
		case best[1] == best[0]:
			// A rule may end without taking a character; one that
			// matched or began without would never get past it.
			return nil, fmt.Errorf("%q: an empty match at offset %d: %s%s", line, pos, rule.Match, rule.Begin)
		case rule.match != nil:
			scopes := with(top.inner, rule.Name)
			err = t.captures(line, best, scopes, rule.Captures)
			t.produce(best[1], scopes)
		default:
			outer := with(top.inner, rule.Name)
			caps := rule.BeginCaptures
			if caps == nil {
				caps = rule.Captures
			}
			err = t.captures(line, best, outer, caps)
			t.produce(best[1], outer)
			stack = append(stack, tmFrame{rule: rule, patterns: rule.Patterns, outer: outer, inner: with(outer, rule.ContentName)})
		}
		if err != nil {
			return nil, err
		}
		pos = best[1]
	}
	t.produce(len(line), stack[len(stack)-1].inner)
	return stack, nil
}

// captures gives the groups of the match m their scopes. A capture that
// has patterns is tokenized by them, as the text before its end and with
// nothing after it.
func (t *tmTokenizer) captures(line string, m []int, base []string, caps map[string]*tmRule) error {
	type open struct {
		scopes []string
		end    int
	}
	var stack []open
	for i := 0; 2*i < len(m); i++ {
		c := caps[strconv.Itoa(i)]
		s, e := m[2*i], m[2*i+1]
		if c == nil || s < 0 || s == e {
			continue
		}
		for len(stack) > 0 && stack[len(stack)-1].end <= s {
			t.produce(stack[len(stack)-1].end, stack[len(stack)-1].scopes)
			stack = stack[:len(stack)-1]
		}
		cur := base
		if len(stack) > 0 {
			cur = stack[len(stack)-1].scopes
		}
		t.produce(s, cur)
		scopes := with(cur, c.Name)
		if len(c.Patterns) > 0 {
			if _, err := t.run(line[:e], s, []tmFrame{{patterns: c.Patterns, outer: scopes, inner: scopes}}); err != nil {
				return err
			}
			continue
		}
		stack = append(stack, open{scopes, e})
	}
	for len(stack) > 0 {
		t.produce(stack[len(stack)-1].end, stack[len(stack)-1].scopes)
		stack = stack[:len(stack)-1]
	}
	return nil
}

// sourceLines splits a file into the lines an editor hands a grammar:
// without their line ends, which may be \n or \r\n.
func sourceLines(src string) []string {
	lines := strings.Split(src, "\n")
	if n := len(lines); lines[n-1] == "" {
		lines = lines[:n-1]
	}
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

// span is a stretch of a line with something to say about it: the scopes
// of a TextMate token, the syntax groups of Vim.
type span struct {
	start, end int
	label      string
}

// tokenizeSource runs the grammar over a whole file and returns each
// line's tokens.
func tokenizeSource(g *tmGrammar, src string) ([][]tmToken, error) {
	t := newTokenizer(g)
	var out [][]tmToken
	for n, l := range sourceLines(src) {
		toks, err := t.line(l + "\n")
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n+1, err)
		}
		out = append(out, toks)
	}
	return out, nil
}

// renderSnapshot writes lines and what was found in them the way
// vscode-tmgrammar-test does: each line after '>', and under it one '#'
// line per span, with carets below the characters it covers. A span with
// no label, or of nothing but blanks, is not written.
func renderSnapshot(lines []string, spans [][]span) string {
	var b strings.Builder
	for i, l := range lines {
		b.WriteString(">" + l + "\n")
		for _, s := range spans[i] {
			end := min(s.end, len(l))
			if s.label == "" || end <= s.start || strings.Trim(l[s.start:end], " \t") == "" {
				continue
			}
			b.WriteByte('#')
			for _, c := range []byte(l[:s.start]) {
				if c != '\t' {
					c = ' '
				}
				b.WriteByte(c)
			}
			b.WriteString(strings.Repeat("^", end-s.start) + " " + s.label + "\n")
		}
	}
	return b.String()
}
