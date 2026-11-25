package diag

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSafeEdits(t *testing.T) {
	fix := func(a Applicability, edits ...TextEdit) Diagnostic {
		return Diagnostic{Fixes: []Fix{{Applicability: a, Edits: edits}}}
	}
	got, n := SafeEdits([]Diagnostic{
		fix(Safe, TextEdit{Start: 20, End: 22, NewText: "c"}),
		fix(Unsafe, TextEdit{Start: 1, End: 2, NewText: "never"}),
		fix(Safe, TextEdit{Start: 4, End: 8, NewText: "a"}, TextEdit{Start: 30, End: 30, NewText: "a2"}),
		// Overlaps the one before it, and one that inserts where another does.
		fix(Safe, TextEdit{Start: 6, End: 9, NewText: "b"}),
		fix(Safe, TextEdit{Start: 30, End: 30, NewText: "d"}),
	})
	assert.Equal(t, 2, n)
	assert.Equal(t, []TextEdit{{Start: 4, End: 8, NewText: "a"}, {Start: 20, End: 22, NewText: "c"}, {Start: 30, End: 30, NewText: "a2"}}, got)
}

func TestApply(t *testing.T) {
	out, ok := Apply([]byte("abcdef"), []TextEdit{{Start: 1, End: 2, NewText: "X"}, {Start: 4, End: 4, NewText: "Y"}})
	assert.True(t, ok)
	assert.Equal(t, "aXcdYef", string(out))
	_, ok = Apply([]byte("abc"), []TextEdit{{Start: 2, End: 3}, {Start: 1, End: 2}})
	assert.False(t, ok, "out of order")
	_, ok = Apply([]byte("abc"), []TextEdit{{Start: 2, End: 9}})
	assert.False(t, ok, "past the end")
}
