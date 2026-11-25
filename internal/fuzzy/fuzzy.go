// Package fuzzy matches what somebody typed against the names it may
// have been meant for, the way an editor's symbol search does: the
// letters typed are to be found in the name in their order, in either
// case, and a name that has them closer together is the better match.
// There is no scoring beyond that. Nearest answers the other question:
// which name a misspelling was meant for.
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

// Nearest returns the name a misspelling was most likely meant for, the
// way a "did you mean" does: the one fewest edits away (a letter added,
// dropped, changed, or two swapped), when that is at most a third of the
// query's length (and at least one); "" when no name is that close. Of
// names equally close the first wins.
func Nearest(query string, names []string) string {
	best, bestD := "", max(1, len(query)/3)+1
	for _, n := range names {
		if d := distance(strings.ToLower(query), strings.ToLower(n)); d < bestD {
			best, bestD = n, d
		}
	}
	return best
}

// distance is the optimal string alignment distance of a and b.
func distance(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
}
