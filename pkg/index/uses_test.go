package index

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A unit keeps its Uses as they were given, sorted by name and place and
// each once: in records of 16 bytes when every one fits, with the line
// and column dropped where they do not (and the file's text then says),
// and in wide records when a length, a name or an Aux does not.
func TestUsesKept(t *testing.T) {
	in := []Use{
		{Name: "b", Start: 10, End: 11, Line: 2, Col: 3, Kind: UseFunction, Form: FormLabel, Aux: "lbl", ASCII: true},
		{Name: "a", Start: 20, End: 25, Line: 3, Col: 1, Kind: UseMacro, Decl: true, Aux: "/h.h", Off: 7},
		{Name: "a", Start: 5, End: 6, Line: 1, Col: 6, Kind: UseMacro, Aux: "/h.h", Off: 7, ASCII: true},
		{Name: "a", Start: 5, End: 6, Line: 1, Col: 6, Kind: UseMacro, Aux: "/h.h", Off: 7, ASCII: true},
		{Name: "far", Start: 1 << 20, End: 1<<20 + 3, Line: 70000, Col: 2, Kind: UseVariable, ASCII: true},
		{Name: "wide", Start: 30, End: 31, Line: 4, Col: 70000, Kind: UseObject, Form: FormPath, ASCII: true},
		{Name: "c", Start: 40, End: 41, Kind: UseInclude, Form: FormUnknown},
	}
	want := []Use{in[2], in[1], in[0], in[6], in[4], in[5]}
	want[4].Line, want[4].Col, want[4].ASCII = 0, 0, false // the line does not fit
	want[5].Line, want[5].Col, want[5].ASCII = 0, 0, false // the column does not

	u := &Unit{}
	u.setUses(append([]Use(nil), in...))
	require.NotNil(t, u.uses.recs)
	require.Nil(t, u.uses.wide)
	assert.Equal(t, want, u.AllUses())
	assert.Equal(t, want[:2], u.Uses("a"))
	assert.Equal(t, want[2:3], u.Uses("b"))
	assert.Nil(t, u.Uses("zz"))

	// A Use too long for 16 bits: every record of the file is wide, and
	// keeps what the narrow one would drop.
	d := Use{Name: "d", Start: 100, End: 100 + 70000, Line: 9, Col: 1, Kind: UseObject}
	u.setUses(append(append([]Use(nil), in...), d))
	require.Nil(t, u.uses.recs)
	want = []Use{in[2], in[1], in[0], in[6], d, in[4], in[5]}
	assert.Equal(t, want, u.AllUses())
	assert.Equal(t, want[4:5], u.Uses("d"))

	// More names than 16 bits number.
	var many []Use
	for i := range 70000 {
		many = append(many, Use{Name: fmt.Sprintf("n%05d", i), Start: uint32(i), End: uint32(i) + 1, Line: 1, Col: uint32(i%100) + 1, Kind: UseFunction})
	}
	u.setUses(many)
	require.Nil(t, u.uses.recs)
	assert.Equal(t, many[69999:], u.Uses("n69999"))
	assert.Len(t, u.AllUses(), 70000)
}
