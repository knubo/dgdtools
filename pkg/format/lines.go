package format

import (
	"bytes"

	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// selection says which gaps of a file are formatted. A gap is the trivia
// between two significant tokens; it is identified by the number of
// significant tokens before it, which is the same in a file and in any
// formatting of it.
type selection struct {
	format []bool
	// trim marks the gaps that are not formatted but begin on a selected
	// line: the blanks at the end of that line are removed.
	trim []bool
}

func (s *selection) has(ord int) bool   { return s == nil || s.format[ord] }
func (s *selection) trims(ord int) bool { return s != nil && s.trim[ord] }

// selectLines selects the gaps that belong to the given lines: a gap
// belongs to the line the token after it starts on, and to the comment
// and blank lines inside it. The gap at the end of the file belongs to
// the last line of code as well.
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
	in := func(a, b int) bool { return a <= b && picked[b]-picked[a-1] > 0 }

	s := &selection{}
	above := 0 // the line the previous significant token ends on
	for _, t := range f.Tokens {
		if t.Kind.IsTrivia() {
			continue
		}
		line := f.Pos(t.Off).Line
		format := in(line, line) || in(above+1, line-1)
		if t.Kind == token.EOF {
			format = in(max(above, 1), line)
		}
		s.format = append(s.format, format)
		s.trim = append(s.trim, !format && above > 0 && in(above, above))
		if t.End > t.Off {
			above = f.Pos(t.End - 1).Line
		}
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
