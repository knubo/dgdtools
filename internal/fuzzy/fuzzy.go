// Package fuzzy matches what somebody typed against the names it may
// have been meant for, the way an editor's symbol search does: the
// letters typed are to be found in the name in their order, in either
// case, and a name that has them closer together is the better match.
// There is no scoring beyond that.
package fuzzy

import "strings"

// Matcher matches names against one query.
type Matcher struct {
	query, lower string
}

// New returns the matcher of a query. The space around it says nothing.
func New(query string) *Matcher {
	query = strings.TrimSpace(query)
	return &Matcher{query: query, lower: strings.ToLower(query)}
}

// Empty reports whether nothing was typed: every name matches then, and
// none better than another.
func (m *Matcher) Empty() bool { return m.query == "" }

// The scores of a match, the best first. A match in the case that was
// typed scores one more than the same match in another case.
const (
	Exact       = 8 // the name is the query
	Prefix      = 6 // the name begins with it
	Substring   = 4 // the name has it
	Subsequence = 2 // the name has its letters, in order
)

// Score says how well name matches the query: 0 when it does not, and
// more the better it does.
func (m *Matcher) Score(name string) int {
	q := m.lower
	if len(q) > len(name) {
		return 0
	}
	if q == "" {
		return Subsequence
	}
	switch i := indexFold(name, q); {
	case i == 0 && len(name) == len(q):
		return Exact + same(name == m.query)
	case i == 0:
		return Prefix + same(strings.HasPrefix(name, m.query))
	case i > 0:
		return Substring + same(strings.Contains(name, m.query))
	}
	j := 0
	for i := 0; i < len(name) && j < len(q); i++ {
		if lower(name[i]) == q[j] {
			j++
		}
	}
	if j == len(q) {
		return Subsequence
	}
	return 0
}

func same(is bool) int {
	if is {
		return 1
	}
	return 0
}

// lower returns c in lower case when it is an ASCII letter. A name of
// LPC is ASCII; another byte is itself.
func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// indexFold returns where q, which is in lower case, first stands in
// name in either case; -1 when it does not.
func indexFold(name, q string) int {
	for i := 0; i+len(q) <= len(name); i++ {
		j := 0
		for j < len(q) && lower(name[i+j]) == q[j] {
			j++
		}
		if j == len(q) {
			return i
		}
	}
	return -1
}
