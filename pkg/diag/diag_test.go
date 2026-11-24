package diag

import (
	"testing"

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

	s, ok := ParseSeverity("warn")
	assert.True(t, ok)
	assert.Equal(t, Warning, s)
	_, ok = ParseSeverity("fatal")
	assert.False(t, ok)
}
