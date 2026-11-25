package editors

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/pp"
	"github.com/mmcdole/dgdtools/pkg/token"
)

var update = flag.Bool("update", false, "rewrite common/dgdlpc.json and the snapshots beside the samples")

const wordListPath = "common/dgdlpc.json"

// wordList is the shared word list: the words of the language as the
// tool's own lexer knows them, for the grammars and their tests.
type wordList struct {
	Comment string `json:"comment"`
	// Keywords are the reserved words that are neither types nor
	// specifiers.
	Keywords []string `json:"keywords"`
	// Types and Specifiers are the type and the class specifier keywords.
	Types      []string `json:"types"`
	Specifiers []string `json:"specifiers"`
	// Closures are the words reserved only when dialect.closures is set,
	// and plain identifiers otherwise. They are type keywords.
	Closures []string `json:"closures"`
	// Operators are the operator and punctuation tokens.
	Operators []string `json:"operators"`
	// Directives are the preprocessor directive names.
	Directives []string `json:"directives"`
}

// reserved returns the words a default-dialect lib cannot use as names:
// keywords, types and specifiers together.
func (w wordList) reserved() []string {
	return sorted(w.Keywords, w.Types, w.Specifiers)
}

func sorted(lists ...[]string) []string {
	out := []string{}
	for _, l := range lists {
		out = append(out, l...)
	}
	sort.Strings(out)
	return out
}

// only lexes src where a token of an expression can stand (not at the
// start of a line, where '#' begins a directive) and returns the kind of
// its one token, or Illegal when it is not exactly one.
func only(src string, d token.Dialect) token.Kind {
	f := lexer.Lex("word", []byte("a "+src), d)
	if len(f.Tokens) != 4 { // a, the space, the token, EOF
		return token.Illegal
	}
	return f.Tokens[2].Kind
}

// wordsFromSource builds the word list from the packages that define the
// language for the tool. Nothing in it is written by hand.
func wordsFromSource(t *testing.T) wordList {
	t.Helper()
	w := wordList{
		Comment: "Written by `go test ./editors -update` from pkg/token, pkg/lexer and pkg/pp. Do not edit: the grammars under editors/ are tested against it.",
	}
	off := token.Dialect{SlashSlash: true}
	on := token.Dialect{SlashSlash: true, Closures: true}
	for word, kind := range token.Keywords {
		require.True(t, kind.IsKeyword(), word)
		require.Equal(t, word, kind.String())
		require.Equal(t, kind, only(word, on), "%s is not lexed as the keyword it is", word)
		switch {
		case only(word, off) != kind:
			// Reserved by a dialect switch only.
			require.Equal(t, token.Ident, only(word, off), word)
			require.True(t, kind.IsType(), "%s: a dialect keyword that is not a type needs a class in the word list", word)
			w.Closures = append(w.Closures, word)
		case kind.IsType():
			w.Types = append(w.Types, word)
		case kind.IsSpecifier():
			w.Specifiers = append(w.Specifiers, word)
		default:
			w.Keywords = append(w.Keywords, word)
		}
	}
	// Every kind with a spelling of its own is a keyword of the table or
	// an operator; the operators stay in the order pkg/token gives them.
	for k := token.Kind(0); ; k++ {
		s := k.String()
		switch {
		case strings.HasPrefix(s, "Kind("):
		case k.IsKeyword():
			require.Contains(t, token.Keywords, s, "a keyword kind that token.Keywords does not have")
		case !token.IsIdentByte(s[0]) && only(s, off) == k:
			w.Operators = append(w.Operators, s)
		}
		if k == 255 {
			break
		}
	}
	sort.Strings(w.Keywords)
	sort.Strings(w.Types)
	sort.Strings(w.Specifiers)
	sort.Strings(w.Closures)
	w.Directives = sorted(pp.Directives)
	return w
}

// TestWordList: the shared word list is what the tool's packages say.
func TestWordList(t *testing.T) {
	w := wordsFromSource(t)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	require.NoError(t, enc.Encode(w))
	if *update {
		require.NoError(t, os.WriteFile(wordListPath, buf.Bytes(), 0o644))
	}
	have, err := os.ReadFile(wordListPath)
	require.NoError(t, err)
	assert.Equal(t, buf.String(), string(have), "%s is stale: run go test ./editors -update", wordListPath)

	// What the grammars are held to, said once more in plain words, so a
	// change to the language shows up here as a decision to make.
	assert.Equal(t, []string{"function"}, w.Closures)
	assert.Contains(t, w.Keywords, "nil")
	assert.Contains(t, w.Operators, "->")
	assert.Contains(t, w.Operators, "::")
	assert.Len(t, w.reserved(), len(token.Keywords)-len(w.Closures))
}
