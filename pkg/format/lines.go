package format

import (
	"bytes"
	"slices"

	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// selection says which gaps of a file are formatted. A gap is the trivia
// between two significant tokens; it is identified by the number of
// significant tokens before it, which is the same in a file and in any
// formatting of it.
//
// A gap is made of rows: the stretches between its line breaks. The first
// row ends the line the token before the gap is on, the last begins the
// line of the token after it, and a block comment can make a row of
// several lines. Only the rows whose lines are all selected are laid
// out; those at either end that are not are kept byte for byte, line
// breaks and all, so that nothing changes on a line that is not selected.
type selection struct {
	format []bool
	// head and tail are the numbers of rows kept as they are at the start
	// and at the end of a formatted gap. They count from the ends, which
	// stay as they are, and so are the same in a formatting of the file.
	head, tail []int
	// trim marks the gaps that are not formatted but begin on a selected
	// line: the blanks at the end of that line are removed.
	trim []bool
}

func (s *selection) has(ord int) bool   { return s == nil || s.format[ord] }
func (s *selection) trims(ord int) bool { return s != nil && s.trim[ord] }

// keepsLine reports whether the line the token after a gap begins on is
// kept as it is: the gap is not formatted, or its last row is kept.
func (s *selection) keepsLine(ord int) bool {
	return s != nil && (!s.format[ord] || s.tail[ord] > 0)
}

// kept returns the numbers of rows a formatted gap keeps at its start
// and at its end.
func (s *selection) kept(ord int) (head, tail int) {
	if s == nil {
		return 0, 0
	}
	return s.head[ord], s.tail[ord]
}

// selectLines selects the gaps that belong to the given lines: the rows
// of a gap whose lines are all selected, when they are one run. The gap
// at the end of the file is formatted only as a whole.
func selectLines(f *token.File, lines []LineRange) *selection {
	// picked[n] is the number of selected lines among lines 1..n.
	last := len(f.Lines) + 1
	picked := make([]int, last+1)
	for _, r := range lines {
		for n := max(r.First, 1); n <= r.Last && n <= last; n++ {
			picked[n] = 1
		}
	}
	for n := 1; n <= last; n++ {
		picked[n] += picked[n-1]
	}
	all := func(a, b int) bool { return picked[b]-picked[a-1] == b-a+1 }

	s := &selection{}
	above := 0 // the line the previous significant token ends on
	var rows []bool
	first, end := 1, 1 // the lines of the current row
	for i, t := range f.Tokens {
		if t.Kind.IsTrivia() {
			if t.End > t.Off {
				end = max(end, f.Pos(t.End-1).Line)
			}
			if t.Kind == token.Newline {
				rows = append(rows, all(first, end))
				first = end + 1
				end = first
			}
			continue
		}
		line := f.Pos(t.Off).Line
		end = max(end, line)
		if t.Kind != token.EOF || i == 0 || f.Tokens[i-1].Kind != token.Newline {
			rows = append(rows, all(first, end))
		} // else: the file ends with a line break, and the last row is empty
		head, tail, format := 0, 0, false
		if h := slices.Index(rows, true); h >= 0 {
			n := 0
			for !rows[len(rows)-1-n] {
				n++
			}
			format = !slices.Contains(rows[h:len(rows)-n], false)
			head, tail = h, n
		}
		if t.Kind == token.EOF && (head > 0 || tail > 0) {
			format = false
		}
		if !format {
			head, tail = 0, 0
		}
		s.format = append(s.format, format)
		s.head = append(s.head, head)
		s.tail = append(s.tail, tail)
		s.trim = append(s.trim, !format && above > 0 && all(above, above))
		if t.End > t.Off {
			above = f.Pos(t.End - 1).Line
		}
		rows = rows[:0]
		first, end = above, above
	}
	return s
}

// Edits returns the replacements that turn the source of f into out,
// which must be a formatting of f (the result of Format or Lines): one
// edit per stretch of whitespace that differs, in source order. It is
// what an editor applies, so that the cursor and everything unchanged
// stay where they are.
func Edits(f *token.File, out []byte) []diag.TextEdit {
	g := lexer.Lex(f.Path, out, f.Dialect)
	var edits []diag.TextEdit
	var a0, b0 uint32 // where the current gap begins in each text
	j := 0
	for _, ta := range f.Tokens {
		if ta.Kind.IsTrivia() {
			continue
		}
		for j < len(g.Tokens) && g.Tokens[j].Kind.IsTrivia() {
			j++
		}
		if j == len(g.Tokens) || g.Tokens[j].Kind != ta.Kind || !bytes.Equal(g.Text(g.Tokens[j]), f.Text(ta)) {
			// Not a formatting of f: all that can be said is "replace it".
			return []diag.TextEdit{{Start: 0, End: uint32(len(f.Src)), NewText: string(out)}}
		}
		tb := g.Tokens[j]
		j++
		if e, changed := gapEdit(f.Src[a0:ta.Off], out[b0:tb.Off], a0); changed {
			edits = append(edits, e)
		}
		a0, b0 = ta.End, tb.End
	}
	return edits
}

// gapEdit is the edit that turns the gap old, which begins at offset at,
// into new, without what the two have in common at either end.
func gapEdit(old, new []byte, at uint32) (diag.TextEdit, bool) {
	if bytes.Equal(old, new) {
		return diag.TextEdit{}, false
	}
	pre := 0
	for pre < len(old) && pre < len(new) && old[pre] == new[pre] {
		pre++
	}
	suf := 0
	for suf < len(old)-pre && suf < len(new)-pre && old[len(old)-1-suf] == new[len(new)-1-suf] {
		suf++
	}
	return diag.TextEdit{
		Start:   at + uint32(pre),
		End:     at + uint32(len(old)-suf),
		NewText: string(new[pre : len(new)-suf]),
	}, true
}
