package diag

import (
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestUTF16Col(t *testing.T) {
	// ASCII: byte column n is UTF-16 offset n-1.
	assert.Equal(t, 0, UTF16Col([]byte("abc"), 1))
	assert.Equal(t, 2, UTF16Col([]byte("abc"), 3))
	// Past the end clamps.
	assert.Equal(t, 3, UTF16Col([]byte("abc"), 99))

	// UTF-8: "é" is two bytes, one UTF-16 unit; "😀" is four bytes, two units.
	line := []byte("é😀x")
	assert.Equal(t, 1, UTF16Col(line, 3)) // after é
	assert.Equal(t, 3, UTF16Col(line, 7)) // after the emoji
	assert.Equal(t, 4, UTF16Col(line, 8)) // after x

	// Latin-1 bytes are not valid UTF-8: one unit each, as an editor that
	// decodes the file as single-byte text shows them.
	latin := []byte{'b', 0xe6, 'r', 0xf8, 'x'}
	assert.Equal(t, 2, UTF16Col(latin, 3))
	assert.Equal(t, 4, UTF16Col(latin, 5))

	s, ok := ParseSeverity("warning")
	assert.True(t, ok)
	assert.Equal(t, Warning, s)
	for _, not := range []string{"fatal", "warn", "Warning", ""} {
		_, ok = ParseSeverity(not)
		assert.False(t, ok, not)
	}
}

func TestByteCol(t *testing.T) {
	assert.Equal(t, 1, ByteCol([]byte("abc"), 0))
	assert.Equal(t, 3, ByteCol([]byte("abc"), 2))
	assert.Equal(t, 4, ByteCol([]byte("abc"), 99), "past the end is the end")
	assert.Equal(t, 1, ByteCol(nil, 3))

	line := []byte("aé😀x") // 1 + 2 + 4 + 1 bytes; 1 + 1 + 2 + 1 units
	assert.Equal(t, 2, ByteCol(line, 1))
	assert.Equal(t, 4, ByteCol(line, 2))
	assert.Equal(t, 4, ByteCol(line, 3), "inside the surrogate pair: the character's start")
	assert.Equal(t, 8, ByteCol(line, 4))
	assert.Equal(t, 9, ByteCol(line, 5))
	// Bytes that are not UTF-8 are one unit each, as in UTF16Col.
	latin := []byte{'a', 0xe9, 'b'}
	assert.Equal(t, 3, ByteCol(latin, 2))
	// The two are inverses at every character boundary.
	for _, l := range [][]byte{line, latin, []byte("plain")} {
		for col := 1; col <= len(l)+1; col++ {
			if col <= len(l) && !utf8.RuneStart(l[col-1]) && utf8.Valid(l) {
				continue
			}
			assert.Equal(t, col, ByteCol(l, UTF16Col(l, col)), "%q col %d", l, col)
		}
	}
}
