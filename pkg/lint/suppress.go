package lint

import (
	"strings"

	"github.com/mmcdole/dgdtools/pkg/token"
)

// suppressions holds the dgdlint: directives found in one file.
//
// Supported forms, inside any comment:
//
//	dgdlint:disable rule-a,rule-b     — from this line to EOF (or enable)
//	dgdlint:enable [rule-a,...]       — re-enable (all rules if none named)
//	dgdlint:disable-line [rules]      — this line only (all rules if bare)
//	dgdlint:disable-next-line [rules] — the following line
//
// Rules may be given by name or code. A bare file-scoped disable (no rule
// list) is ignored: suppress-all is a footgun.
type suppressions struct {
	entries []*supEntry
}

type supEntry struct {
	rule       string // rule name; "" = all rules
	start, end int    // lines, inclusive; end 0 = EOF
	off, end2  uint32 // the comment's byte range
	what       string
	used       bool
}

type unusedSup struct {
	off, end uint32
	what     string
}

func scanSuppressions(f *token.File, l *Linter) *suppressions {
	s := &suppressions{}
	open := map[string]*supEntry{}

	for _, t := range f.Tokens {
		if t.Kind != token.LineComment && t.Kind != token.BlockComment {
			continue
		}
		text := string(f.Text(t))
		i := strings.Index(text, "dgdlint:")
		if i < 0 {
			continue
		}
		line := f.Pos(t.Off).Line
		directive := strings.TrimPrefix(text[i:], "dgdlint:")
		directive = strings.TrimSuffix(directive, "*/")
		fields := strings.Fields(directive)
		if len(fields) == 0 {
			continue
		}
		var rules []string
		prose := false
		if len(fields) > 1 {
			for _, r := range strings.Split(fields[1], ",") {
				if r = strings.TrimSpace(r); r != "" {
					if l != nil {
						rule := l.RuleByName(r)
						if rule == nil {
							prose = true // not a rule: the comment is talking about the linter
							break
						}
						r = rule.Name
					}
					rules = append(rules, r)
				}
			}
		}
		if prose {
			continue
		}
		add := func(rule string, start, end int) *supEntry {
			e := &supEntry{rule: rule, start: start, end: end, off: t.Off, end2: t.End,
				what: fields[0] + " " + rule}
			s.entries = append(s.entries, e)
			return e
		}
		switch fields[0] {
		case "disable":
			for _, r := range rules { // bare disable ignored deliberately
				if _, dup := open[r]; !dup {
					open[r] = add(r, line, 0)
				}
			}
		case "enable":
			if len(rules) == 0 {
				for r, e := range open {
					e.end = line
					delete(open, r)
				}
			} else {
				for _, r := range rules {
					if e, ok := open[r]; ok {
						e.end = line
						delete(open, r)
					}
				}
			}
		case "disable-line", "disable-next-line":
			target := line
			if fields[0] == "disable-next-line" {
				target = f.Pos(t.End).Line + 1
			}
			if len(rules) == 0 {
				add("", target, target)
			}
			for _, r := range rules {
				add(r, target, target)
			}
		}
	}
	return s
}

func (s *suppressions) suppressed(rule string, line int) bool {
	hit := false
	for _, e := range s.entries {
		if (e.rule == "" || e.rule == rule) && line >= e.start && (e.end == 0 || line <= e.end) {
			e.used = true
			hit = true
		}
	}
	return hit
}

func (s *suppressions) unused() []unusedSup {
	var out []unusedSup
	for _, e := range s.entries {
		if !e.used {
			out = append(out, unusedSup{e.off, e.end2, strings.TrimSpace(e.what)})
		}
	}
	return out
}
