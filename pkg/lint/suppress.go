package lint

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/directive"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// Suppression comments (pkg/directive has the grammar):
//
//	x = f();   /* dgdtools-ignore lint/rule-a */       this line
//	/* dgdtools-ignore lint/rule-a, lint/E002 */       the next line
//	/* dgdtools-ignore lint: generated */              every rule, one line
//	/* dgdtools-ignore-start lint/rule-a */  ...  /* dgdtools-ignore-end lint/rule-a */
//
// The plain form is about the line of code it stands on, and about the
// next line when it stands alone. A range names its rules: there is no
// way to turn every rule off from here to the end of the file. Rules are
// given by name or by code.

// ignore is one thing a comment asks of the linter: to say nothing of a
// rule (or of any) on some lines.
type ignore struct {
	rule        string // the rule's name; "" is every rule
	first, last int    // lines, inclusive; last 0 is the end of the file
	target      directive.Target
	used        bool
}

// problem is something wrong with a directive.
type problem struct {
	off, end uint32
	msg      string
}

// suppressions are the directives of one file as the linter reads them.
type suppressions struct {
	ignores  []*ignore
	problems []problem
}

// suppressions reads the directives of f.
func (l *Linter) suppressions(f *token.File) *suppressions {
	s := &suppressions{}
	if !bytes.Contains(f.Src, []byte(directive.Prefix)) {
		return s
	}
	code := codeLines(f)
	open := map[string]*ignore{} // the ranges not yet ended, by rule
	formatOff := 0               // the line the formatter was turned off at
	bad := func(off, end uint32, format string, args ...any) {
		s.problems = append(s.problems, problem{off, end, fmt.Sprintf(format, args...)})
	}

	for _, d := range directive.Scan(f) {
		from, to := f.Pos(d.Off).Line, f.Pos(d.End).Line
		if len(d.Targets) == 0 && d.Form != directive.End {
			bad(d.Off, d.End, "%s names nothing to ignore: say lint/RULE or format", d.Form)
			continue
		}
		line := lineOf(code, from, to)
		if d.Form == directive.End && len(d.Targets) == 0 {
			if len(open) == 0 && formatOff == 0 {
				bad(d.Off, d.End, "nothing to end: no %s above", directive.Start)
			}
			for name, ig := range open {
				ig.last = from
				delete(open, name)
			}
			formatOff = 0
		}
		for _, t := range d.Targets {
			rule := ""
			switch {
			case t.Tool == directive.Format && t.Rule == "":
				switch {
				case d.Form == directive.Line:
					bad(t.Off, t.End, "the formatter is turned off for a range of lines: %s format ... %s format", directive.Start, directive.End)
				case d.Form == directive.Start && formatOff != 0:
					bad(t.Off, t.End, "the formatter is already off, since line %d", formatOff)
				case d.Form == directive.Start:
					formatOff = from
				case formatOff == 0:
					bad(t.Off, t.End, "the formatter is not off here: no %s format above", directive.Start)
				default:
					formatOff = 0
				}
				continue
			case t.Tool != directive.Lint:
				bad(t.Off, t.End, "unknown target %q: want lint/RULE, lint or format", t)
				continue
			case t.Rule != "":
				r := l.rule(t.Rule)
				switch {
				case r == nil:
					bad(t.Off, t.End, "unknown rule %q (see `dgdtools rules`)", t.Rule)
					continue
				case r.Late:
					bad(t.Off, t.End, "%s is about these comments themselves and is not ignored by one: turn the rule off, or mend the comment", r.Name)
					continue
				}
				rule = r.Name
			}
			switch d.Form {
			case directive.Line:
				s.ignores = append(s.ignores, &ignore{rule: rule, first: line, last: line, target: t})
			case directive.Start:
				switch was := open[rule]; {
				case rule == "":
					bad(t.Off, t.End, "a range names the rules it is for (lint/RULE): every rule at once would hide whatever comes later")
				case was != nil:
					bad(t.Off, t.End, "%s is already ignored, since line %d", t, was.first)
				default:
					open[rule] = &ignore{rule: rule, first: from, target: t}
					s.ignores = append(s.ignores, open[rule])
				}
			case directive.End:
				switch was := open[rule]; {
				case rule == "" && len(open) == 0:
					bad(t.Off, t.End, "no rule is being ignored here: no %s above", directive.Start)
				case rule == "":
					for name, ig := range open {
						ig.last = from
						delete(open, name)
					}
				case was == nil:
					bad(t.Off, t.End, "%s is not being ignored here: no %s %s above", t, directive.Start, t)
				default:
					was.last = from
					delete(open, rule)
				}
			}
		}
	}
	return s
}

// codeLines returns the lines of f that have code on them.
func codeLines(f *token.File) map[int]bool {
	code := map[int]bool{}
	for _, t := range f.Significant() {
		code[f.Pos(t.Off).Line] = true
	}
	return code
}

// lineOf returns the line a plain directive is about, whose comment
// begins on line from and ends on line to: the line of code it stands
// on, or the next line when it stands alone.
func lineOf(code map[int]bool, from, to int) int {
	switch {
	case code[from]:
		return from
	case code[to]:
		return to
	}
	return to + 1
}

// Suppress returns the comments that would make the linter say nothing
// of the finding d of the file f, each as a fix: one for the finding's
// line, and one for the rule in the whole file. They change what is
// reported and not what the code does, so nobody applies them unasked
// (diag.Unsafe). A finding no comment suppresses (Rule.Late) has none,
// and a line a comment cannot be put above has only the second.
//
// A comment that is already about the line is given the rule too, since
// a second comment above it would be about the first.
func Suppress(f *token.File, d diag.Diagnostic) []diag.Fix {
	r := RuleByName(d.Rule)
	if r == nil || r.Late || d.Line < 1 || d.Line > len(f.Lines) {
		return nil
	}
	target := directive.Lint + "/" + r.Name
	var fixes []diag.Fix
	line := func(e diag.TextEdit) {
		fixes = append(fixes, diag.Fix{Title: "Suppress " + r.Name + " for this line", Edits: []diag.TextEdit{e}})
	}
	code, start, ranged := codeLines(f), f.Lines[d.Line-1], false
	var about *directive.Directive
	for _, dir := range directive.Scan(f) {
		for _, t := range dir.Targets {
			if x := RuleByName(t.Rule); dir.Form == directive.Start && t.Tool == directive.Lint && x != nil && x.Name == r.Name {
				ranged = true
			}
		}
		if dir.Form == directive.Line && len(dir.Targets) > 0 && lineOf(code, f.Pos(dir.Off).Line, f.Pos(dir.End).Line) == d.Line {
			about = &dir
		}
	}
	nl := newlineOf(f, d.Line)
	switch {
	case about != nil:
		line(diag.TextEdit{Start: about.Targets[len(about.Targets)-1].End, End: about.Targets[len(about.Targets)-1].End, NewText: ", " + target})
	case start == 0 || endsLine(f, start):
		indent := start
		for int(indent) < len(f.Src) && (f.Src[indent] == ' ' || f.Src[indent] == '\t') {
			indent++
		}
		line(diag.TextEdit{Start: start, End: start,
			NewText: string(f.Src[start:indent]) + "/* " + directive.Prefix + " " + target + " */" + nl})
	}
	// A range that is open twice is an error of its own, so a file that
	// has one for the rule anywhere is left to whoever wrote it.
	if !ranged {
		fixes = append(fixes, diag.Fix{Title: "Suppress " + r.Name + " for this file", Edits: []diag.TextEdit{{
			NewText: "/* " + directive.Prefix + "-start " + target + " */" + nl}}})
	}
	return fixes
}

// endsLine reports whether a line break of the code ends just before
// off: one that is no part of a comment, a string or a directive's
// continued line, so that a line put in there is a line of its own.
func endsLine(f *token.File, off uint32) bool {
	i := sort.Search(len(f.Tokens), func(i int) bool { return f.Tokens[i].End >= off })
	return i < len(f.Tokens) && f.Tokens[i].End == off && f.Tokens[i].Kind == token.Newline
}

// newlineOf returns the line break of the given line of f, that of the
// line before when it is the last and has none, "\n" in a file of one
// line.
func newlineOf(f *token.File, line int) string {
	for ; line >= 1; line-- {
		if line >= len(f.Lines) {
			continue
		}
		end := f.Lines[line]
		if end >= 2 && f.Src[end-2] == '\r' && f.Src[end-1] == '\n' {
			return "\r\n"
		}
		return string(f.Src[end-1 : end])
	}
	return "\n"
}

// suppressed reports whether a comment covers the rule's finding on the
// line, and remembers the comments that did.
func (s *suppressions) suppressed(rule string, line int) bool {
	hit := false
	for _, ig := range s.ignores {
		if (ig.rule == "" || ig.rule == rule) && line >= ig.first && (ig.last == 0 || line <= ig.last) {
			ig.used, hit = true, true
		}
	}
	return hit
}

// The rules about the comments themselves. They run after the others
// (Rule.Late), on what the others and the comments left.

var ruleInvalidSuppression = &Rule{
	Code: "S005", Name: "invalid-suppression", Group: GroupSuspicious,
	Doc: "a dgdtools-ignore comment that does not do what it was written to do",
	Explain: "A comment that begins with dgdtools-ignore is an instruction, and " +
		"one that cannot be followed protects nothing while looking as if it " +
		"did: a rule name that does not exist (a typo), a target that is neither " +
		"lint, lint/RULE nor format, a comment that names nothing, a range " +
		"(dgdtools-ignore-start) that names no rule, an end with no start. " +
		"The plain form is about one line: the line of code it stands on, or the " +
		"next line when it stands alone; -start and -end are about what is " +
		"between them; the formatter is turned off by range only. A comment " +
		"that merely mentions dgdtools-ignore is not a directive.",
	Default: true, Severity: diag.Warning, Late: true,
	Run: func(p *Pass) {
		for _, pr := range p.shared.sup.problems {
			p.Report(pr.off, pr.end, "%s", pr.msg)
		}
	},
}

var ruleUnusedSuppression = &Rule{
	Code: "C006", Name: "unused-suppression", Group: GroupConvention,
	Doc: "a dgdtools-ignore comment for a finding that is not there",
	Explain: "Reported for each rule a dgdtools-ignore comment names that has " +
		"no finding where the comment says. A suppression that is not needed " +
		"hides the next finding that turns up there. A comment for a rule that " +
		"did not run on the file (it is off, or needs a mudlib that is not known) " +
		"is not reported: whether it is needed cannot be told.",
	Default: false, Severity: diag.Warning, Late: true, Tags: []diag.Tag{diag.Unnecessary},
	Run: func(p *Pass) {
		for _, ig := range p.shared.sup.ignores {
			if ig.used || (ig.rule != "" && !p.shared.ran[ig.rule]) {
				continue
			}
			what, where := "finding", fmt.Sprintf("on line %d", ig.first)
			if ig.rule != "" {
				what = ig.rule + " finding"
			}
			switch {
			case ig.last == 0:
				where = fmt.Sprintf("from line %d on", ig.first)
			case ig.last != ig.first:
				where = fmt.Sprintf("in lines %d to %d", ig.first, ig.last)
			}
			p.Report(ig.target.Off, ig.target.End, "nothing to ignore: no %s %s", what, where)
		}
	},
}
