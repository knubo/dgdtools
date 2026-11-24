package lint

import (
	"bytes"
	"fmt"

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
	// The lines that have code on them.
	code := map[int]bool{}
	for _, t := range f.Significant() {
		code[f.Pos(t.Off).Line] = true
	}
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
		// The line a plain directive is about.
		line := to + 1
		if code[from] {
			line = from
		} else if code[to] {
			line = to
		}
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
				r := l.RuleByName(t.Rule)
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
		"did not run on the file (it is off, or needs a lib that is not known) " +
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
