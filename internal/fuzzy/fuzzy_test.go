package fuzzy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScore(t *testing.T) {
	for _, c := range []struct {
		query, name string
		want        int
	}{
		{"set_short", "set_short", Exact + 1},
		{"SET_SHORT", "set_short", Exact},
		{"set", "set_short", Prefix + 1},
		{"Set", "set_short", Prefix},
		{"short", "set_short", Substring + 1},
		{"SHORT", "set_short", Substring},
		{"sshort", "set_short", Subsequence},
		{"SSH", "set_short", Subsequence},
		{"stt", "set_short", Subsequence},
		{"tes", "set_short", 0},
		{"set_shorter", "set_short", 0},
		{"  set ", "set_short", Prefix + 1},
		{"", "set_short", Subsequence},
		{"room_d", "ROOM_D", Exact},
		{"ROOM", "ROOM_D", Prefix + 1},
		{"ø", "grøt", Substring + 1},
	} {
		assert.Equal(t, c.want, New(c.query).Score(c.name), "%q in %q", c.query, c.name)
	}
	assert.True(t, New(" ").Empty())
	assert.False(t, New("a").Empty())
}

func TestNearest(t *testing.T) {
	names := []string{"format", "lint", "root", "profile", "lib_path"}
	for q, want := range map[string]string{
		"fromat": "format", "lnt": "lint", "rot": "root", "profiel": "profile",
		"libpath": "lib_path", "xyz": "", "formatting": "",
	} {
		if got := Nearest(q, names); got != want {
			t.Errorf("Nearest(%q) = %q, want %q", q, got, want)
		}
	}
}
