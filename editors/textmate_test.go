package editors

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/token"
)

const (
	vscodeDir   = "vscode"
	grammarPath = vscodeDir + "/syntaxes/dgdlpc.tmLanguage.json"
	samplesGlob = "common/samples/*.[ch]"
)

func grammar(t *testing.T) *tmGrammar {
	t.Helper()
	g, err := loadGrammar(grammarPath)
	require.NoError(t, err)
	return g
}

// loadWordList reads the shared word list as the grammars' tests use it.
func loadWordList(t *testing.T) wordList {
	t.Helper()
	data, err := os.ReadFile(wordListPath)
	require.NoError(t, err)
	var w wordList
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&w))
	return w
}

// samples returns the sample files, by path.
func samples(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(samplesGlob)
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	out := map[string]string{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		out[p] = string(data)
	}
	return out
}

// TestGrammarStructure: the grammar loads (every rule is of one kind,
// every pattern is in the syntax Go and Oniguruma share, every include
// and capture refers to something), is the grammar of dgdlpc, and has no
// rule nothing reaches.
func TestGrammarStructure(t *testing.T) {
	g := grammar(t)
	assert.Equal(t, "source.dgdlpc", g.ScopeName)

	reached := map[string]bool{}
	var reach func(r *tmRule)
	reach = func(r *tmRule) {
		if r.Include != "" {
			key := r.Include[1:]
			if !reached[key] {
				reached[key] = true
				reach(g.Repository[key])
			}
			return
		}
		for _, p := range r.Patterns {
			reach(p)
		}
		for _, caps := range []map[string]*tmRule{r.Captures, r.BeginCaptures, r.EndCaptures} {
			for _, c := range caps {
				reach(c)
			}
		}
	}
	for _, r := range g.Patterns {
		reach(r)
	}
	for key := range g.Repository {
		assert.True(t, reached[key], "repository rule %q is never included", key)
	}

	// A begin/end rule that can stay open must be one that is meant to:
	// everything inside a directive ends with the directive's line.
	g.walk(func(where string, r *tmRule) {
		if r.begin == nil || !strings.Contains(where, "directive") {
			return
		}
		assert.Contains(t, r.End, `(?=\n)`, "%s: a rule of a directive that does not end with its line", where)
	})
}

// TestPatternSyntax: what compilePattern accepts and what it turns away.
func TestPatternSyntax(t *testing.T) {
	for _, src := range []string{
		`\d+`, `\w`, `[ \s]`, `a$`, `a^b`, `(?<!a)b`, `(?!a)b`, `(?<n>a)`, `(?i)a`, `(a)\1`, `(?>a)`,
		`(?=a)b`, `(a(?=b))`, `a(?=b)(c)`, `a(?=(b))`, `[[:alpha:]]`, `[\b]`, `a\`, `(a`, `a)`, `[a`,
	} {
		_, err := compilePattern(src)
		assert.Error(t, err, src)
	}
	find := func(src, line string, pos int) []int {
		p, err := compilePattern(src)
		require.NoError(t, err, src)
		return p.find(line, pos)
	}
	// Leftmost, from pos on.
	assert.Equal(t, []int{4, 5}, find(`a`, "a bca", 1))
	// \b sees the character before pos; ^ is the start of the line only.
	assert.Equal(t, []int{5, 7}, find(`\bif`, "elif if", 2))
	assert.Equal(t, []int{0, 1}, find(`^#`, "##", 0))
	assert.Nil(t, find(`^#`, "##", 1))
	// A lookahead ends the match where it begins, in the alternative
	// that has it; groups keep their numbers.
	assert.Equal(t, []int{0, 3, 0, 3}, find(`([a-z]+)(?=\()`, "foo(", 0))
	assert.Nil(t, find(`([a-z]+)(?=\()`, "foo", 0))
	assert.Equal(t, []int{1, 1, -1, -1}, find(`(")|(?=\n)`, "a\n", 0))
	assert.Equal(t, []int{1, 2, 1, 2}, find(`(")|(?=\n)`, "a\"\n", 0))
	// Bytes that are not UTF-8 are characters like any other.
	assert.Equal(t, []int{2, 3}, find(`\bx`, "\xe9 x", 1))
}

// scopesOf tokenizes one line and returns the scopes of the token that
// covers exactly [start, end), or nil with false when no token does.
func scopesOf(t *testing.T, g *tmGrammar, line string, start, end int) ([]string, bool) {
	t.Helper()
	toks, err := newTokenizer(g).line(line + "\n")
	require.NoError(t, err)
	for _, tok := range toks {
		if tok.start == start && tok.end == end {
			return tok.scopes, true
		}
	}
	return nil, false
}

// alternation reads the words of a pattern written \b(?:a|b|c)\b.
func alternation(t *testing.T, pattern string) []string {
	t.Helper()
	inner, ok := strings.CutPrefix(pattern, `\b(?:`)
	require.True(t, ok, pattern)
	inner, ok = strings.CutSuffix(inner, `)\b`)
	require.True(t, ok, pattern)
	words := strings.Split(inner, "|")
	for _, w := range words {
		require.True(t, token.IsIdent(w), "%s: %q", pattern, w)
	}
	return words
}

// TestGrammarWords: the grammar's word lists are the shared word list's,
// and each word gets the scope of its class when the grammar runs.
func TestGrammarWords(t *testing.T) {
	g := grammar(t)
	w := loadWordList(t)

	// The lists as written in the grammar.
	byScope := map[string][]string{}
	var all []string
	for _, r := range g.Repository["keywords"].Patterns {
		if strings.HasPrefix(r.Match, `\b(?:`) {
			words := alternation(t, r.Match)
			class, _, _ := strings.Cut(r.Name, ".")
			if strings.HasPrefix(r.Name, "storage.") {
				class = strings.TrimSuffix(r.Name, ".dgdlpc")
			}
			byScope[class] = append(byScope[class], words...)
			all = append(all, words...)
			continue
		}
		// The one rule of another shape names a keyword too.
		assert.True(t, strings.HasPrefix(r.Match, `\b(inherit)[`), r.Match)
	}
	sort.Strings(all)
	assert.Equal(t, w.reserved(), all, "the grammar's reserved words")
	assert.Equal(t, w.Types, sorted(byScope["storage.type"]))
	assert.Equal(t, w.Specifiers, sorted(byScope["storage.modifier"]))
	assert.Equal(t, w.Keywords, sorted(byScope["keyword"], byScope["constant"]))

	var directives []string
	name := regexp.MustCompile(`^\^\[ \\t\]\*\(\(#\)\[ \\t\]\*(?:\(\?:([a-z|]+)\)|([a-z]+))\)`)
	for key, r := range g.Repository {
		if !strings.HasPrefix(key, "directive-") || r.begin == nil || !strings.HasPrefix(r.Begin, "^") {
			continue
		}
		m := name.FindStringSubmatch(r.Begin)
		if m == nil {
			assert.Equal(t, "directive-unknown", key, "a directive rule whose names cannot be read: %s", r.Begin)
			continue
		}
		directives = append(directives, strings.Split(m[1]+m[2], "|")...)
	}
	sort.Strings(directives)
	// #define has two rules, with and without parameters.
	assert.Equal(t, sorted(w.Directives, []string{"define"}), directives, "the grammar's directive names")

	// The words when the grammar runs.
	class := map[string]string{}
	for _, word := range w.Keywords {
		class[word] = "keyword"
	}
	for _, word := range w.Types {
		class[word] = "storage.type"
	}
	for _, word := range w.Specifiers {
		class[word] = "storage.modifier"
	}
	for word, want := range class {
		scopes, ok := scopesOf(t, g, " "+word+" ", 1, 1+len(word))
		if assert.True(t, ok, word) && assert.Len(t, scopes, 1, word) {
			if want == "keyword" {
				assert.Regexp(t, `^(keyword\.|constant\.language\.)`, scopes[0], word)
			} else {
				assert.True(t, strings.HasPrefix(scopes[0], want+"."), "%s: %s", word, scopes[0])
			}
		}
		// Inside a longer name it is no keyword.
		scopes, ok = scopesOf(t, g, " "+word+"_x ", 1, 3+len(word))
		assert.True(t, ok, word)
		assert.Empty(t, scopes, word)
	}
	for _, word := range w.Closures {
		scopes, ok := scopesOf(t, g, " "+word+" ", 1, 1+len(word))
		assert.True(t, ok, word)
		assert.Empty(t, scopes, "%s is a name unless dialect.closures is set", word)
	}
	for _, op := range w.Operators {
		scopes, ok := scopesOf(t, g, "a "+op+" b", 2, 2+len(op))
		if assert.True(t, ok, "%q is not one token", op) && assert.NotEmpty(t, scopes, op) {
			assert.Regexp(t, `^(keyword\.operator\.|punctuation\.)`, scopes[len(scopes)-1], op)
		}
	}
	for _, d := range w.Directives {
		for _, line := range []string{"#" + d, "  # " + d + " X"} {
			at := strings.Index(line, "#")
			scopes, ok := scopesOf(t, g, line, at+1, strings.Index(line, d)+len(d))
			if assert.True(t, ok, line) && assert.Len(t, scopes, 2, line) {
				assert.True(t, strings.HasPrefix(scopes[0], "meta.preprocessor."), line)
				assert.True(t, strings.HasPrefix(scopes[1], "keyword.control.directive."), line)
			}
		}
	}
}

// byteClass says what kind of token a byte belongs to, in the words both
// the lexer's kinds and a grammar's scopes or groups can be put in.
type byteClass uint8

const (
	clOther     byteClass = iota // names, white space, and what is nothing
	clAny                        // an Illegal token: a grammar may say anything
	clComment                    // a comment
	clString                     // a string literal
	clChar                       // a character constant
	clInt                        // an integer
	clFloat                      // a float
	clKeyword                    // a reserved word that is no type or specifier
	clType                       // a type keyword
	clSpecifier                  // a specifier keyword
	clOperator                   // an operator or punctuation
	clDirective                  // a preprocessor directive, all of it
)

var classNames = [...]string{"other", "any", "comment", "string", "char", "int", "float", "keyword", "type", "specifier", "operator", "directive"}

func (c byteClass) String() string { return classNames[c] }

func kindClass(k token.Kind) byteClass {
	switch {
	case k == token.Illegal:
		return clAny
	case k == token.Directive:
		return clDirective
	case k == token.LineComment, k == token.BlockComment:
		return clComment
	case k == token.StringLit:
		return clString
	case k == token.CharLit:
		return clChar
	case k == token.IntLit:
		return clInt
	case k == token.FloatLit:
		return clFloat
	case k.IsType():
		return clType
	case k.IsSpecifier():
		return clSpecifier
	case k.IsKeyword():
		return clKeyword
	case k >= token.LParen:
		return clOperator
	}
	return clOther
}

// lexed is a sample as pkg/lexer reads it: the class of every byte, and
// where the tokens that a grammar must not split or join begin.
type lexed struct {
	src    string
	class  []byteClass
	starts map[int]token.Token
	kinds  map[token.Kind]bool
}

func lexSample(t *testing.T, path, src string) lexed {
	t.Helper()
	f := lexer.Lex(path, []byte(src), lexer.Default)
	require.NoError(t, f.CheckRoundTrip())
	l := lexed{src: src, class: make([]byteClass, len(src)), starts: map[int]token.Token{}, kinds: map[token.Kind]bool{}}
	for _, tok := range f.Tokens {
		l.kinds[tok.Kind] = true
		c := kindClass(tok.Kind)
		for i := tok.Off; i < tok.End; i++ {
			l.class[i] = c
		}
		switch c {
		case clInt, clFloat, clKeyword, clType, clSpecifier, clOperator:
			l.starts[int(tok.Off)] = tok
		case clOther:
			if tok.Kind == token.Ident {
				l.starts[int(tok.Off)] = tok
			}
		}
	}
	return l
}

// agree compares the lexer's reading of a sample with a grammar's: class
// is what the grammar makes of each byte, and boundary whether something
// of the grammar's begins at a byte. White space between tokens is not
// compared (a grammar counts the blanks before a '#' to the directive).
// A grammar that is not strict may leave operators and names alone: it
// is held to the literals, the reserved words, the comments and the
// directives. Up to ten differences are reported.
func (l lexed) agree(t *testing.T, path string, class []byteClass, boundary []bool, strict bool) {
	t.Helper()
	line, col, bad := 1, 1, 0
	report := func(format string, args ...any) {
		if bad++; bad <= 10 {
			t.Errorf("%s:%d:%d: "+format, append([]any{path, line, col}, args...)...)
		}
	}
	for i := 0; i < len(l.src); i++ {
		if l.src[i] == '\n' {
			line, col = line+1, 1
			continue
		}
		want, have := l.class[i], class[i]
		blank := want == clOther && strings.IndexByte(" \t\r", l.src[i]) >= 0
		left := !strict && want == clOperator && have == clOther
		if want != have && want != clAny && !blank && !left {
			report("%q is of a %s token, and %s to the grammar", l.src[i], want, have)
		}
		if tok, ok := l.starts[i]; ok && (strict || (want != clOperator && want != clOther)) {
			text := l.src[tok.Off:tok.End]
			if !boundary[i] {
				report("the grammar does not begin anything at the token %q", text)
			}
			for j := i + 1; j < int(tok.End); j++ {
				if boundary[j] {
					report("the grammar splits the token %q", text)
					break
				}
			}
		}
		col++
	}
}

// scopeClass puts a TextMate token's scopes in the lexer's words.
func scopeClass(scopes []string) byteClass {
	has := func(prefix string) bool {
		for _, s := range scopes {
			if strings.HasPrefix(s, prefix) {
				return true
			}
		}
		return false
	}
	switch {
	case has("meta.preprocessor."):
		return clDirective
	case has("comment."):
		return clComment
	case has("string.quoted.double."):
		return clString
	case has("string.quoted.single."):
		return clChar
	case len(scopes) == 0:
		return clOther
	}
	switch s := scopes[len(scopes)-1]; {
	case strings.HasPrefix(s, "constant.numeric.integer."):
		return clInt
	case strings.HasPrefix(s, "constant.numeric.float."):
		return clFloat
	case strings.HasPrefix(s, "storage.type."):
		return clType
	case strings.HasPrefix(s, "storage.modifier."):
		return clSpecifier
	case strings.HasPrefix(s, "keyword.operator.new."):
		return clKeyword
	case strings.HasPrefix(s, "keyword.operator."), strings.HasPrefix(s, "punctuation."):
		return clOperator
	case strings.HasPrefix(s, "keyword."), strings.HasPrefix(s, "constant.language."):
		return clKeyword
	}
	return clOther
}

// TestGrammarAgreesWithLexer: over the samples, the grammar and pkg/lexer
// find the same tokens: every byte is of the same class to both, a
// directive ends where the lexer's does, and no number, word or operator
// is split or joined, except that the grammar reads "({" and "([" and
// their closers as one bracket each.
func TestGrammarAgreesWithLexer(t *testing.T) {
	g := grammar(t)
	for path, src := range samples(t) {
		l := lexSample(t, path, src)
		lines, err := tokenizeSource(g, src)
		require.NoError(t, err, path)
		class := make([]byteClass, len(src)+1)
		boundary := make([]bool, len(src)+1)
		off := 0
		for i, text := range strings.SplitAfter(src, "\n") {
			if i == len(lines) {
				break
			}
			for _, tok := range lines[i] {
				c := scopeClass(tok.scopes)
				for j := tok.start; j < min(tok.end, len(text)); j++ {
					class[off+j] = c
				}
				boundary[off+tok.start] = true
				// The two brackets of an array or mapping delimiter.
				if n := len(tok.scopes); n > 0 && tok.end-tok.start == 2 &&
					(strings.HasPrefix(tok.scopes[n-1], "punctuation.definition.array.") || strings.HasPrefix(tok.scopes[n-1], "punctuation.definition.mapping.")) {
					boundary[off+tok.start+1] = true
				}
			}
			off += len(text)
		}
		l.agree(t, path, class, boundary, true)
	}
}

// TestSamplesCoverTheTokens: between them the samples have a token of
// every kind the lexer knows, so the comparison above and the snapshots
// leave none out. A kind added to pkg/token fails here until a sample
// has it.
func TestSamplesCoverTheTokens(t *testing.T) {
	seen := map[token.Kind]bool{}
	for path, src := range samples(t) {
		l := lexSample(t, path, src)
		for k := range l.kinds {
			seen[k] = true
		}
		assert.False(t, l.kinds[token.Illegal], "%s has a token the lexer calls illegal", path)
	}
	for k := token.Kind(0); !strings.HasPrefix(k.String(), "Kind("); k++ {
		switch k {
		case token.Illegal:
		case token.KwFunction:
			// Not a token of the default dialect; a sample has the word
			// as the name it is there.
		default:
			assert.True(t, seen[k], "no sample has a %v token", k)
		}
	}
}

// TestGrammarSnapshots: the scopes the grammar gives every token of the
// samples, as recorded beside them (go test ./editors -update).
func TestGrammarSnapshots(t *testing.T) {
	g := grammar(t)
	for path, src := range samples(t) {
		lines, err := tokenizeSource(g, src)
		require.NoError(t, err, path)
		spans := make([][]span, len(lines))
		for i, toks := range lines {
			for _, tok := range toks {
				spans[i] = append(spans[i], span{tok.start, tok.end, strings.Join(tok.scopes, " ")})
			}
		}
		compareSnapshot(t, path+".tm.snap", renderSnapshot(sourceLines(src), spans))
	}
}

func compareSnapshot(t *testing.T, path, have string) {
	t.Helper()
	if *update {
		require.NoError(t, os.WriteFile(path, []byte(have), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "no snapshot: run go test ./editors -update, and read what it wrote")
	assert.Equal(t, string(want), have, "%s differs: if the change is meant, run go test ./editors -update", path)
}

// TestGrammarOnBrokenText: the grammar gets through what is not a
// program: bytes that are not UTF-8, carriage returns, literals and
// comments left open. Nothing loops and every byte gets a token.
func TestGrammarOnBrokenText(t *testing.T) {
	g := grammar(t)
	for _, src := range []string{
		"string s = \"caf\xe9\"; /* na\xefve */ x\xff = 1;\n",
		"int a;\r\n#define X \\\r\n  1\r\nint b;\r\n",
		"x = \"unterminated\ny = 1;\n",
		"x = 'a\ny = '';\nz = ''';\n",
		"/* never closed\nint a;\n",
		"x = ({ 1, ([ 2: 3\nint b;\n",
		"#define OPEN ({ ([ a[\nint b;\n",
		"#define S \"open\nint b;\n",
		"#if 0 /* open\nstill */ junk\nint b;\n",
		"\\ @ $ ` \x01\n",
		"#\n##\n# #\n",
		"",
		"no newline at the end",
	} {
		lines, err := tokenizeSource(g, src)
		if !assert.NoError(t, err, "%q", src) {
			continue
		}
		for i, l := range sourceLines(src) {
			at := 0
			for _, tok := range lines[i] {
				assert.Equal(t, at, tok.start, "%q line %d", src, i+1)
				at = tok.end
			}
			assert.Equal(t, len(l)+1, at, "%q line %d", src, i+1)
		}
	}
	// What follows a directive is code again, whatever the directive
	// left open.
	for _, src := range []string{"#define OPEN ({ ([ a[\nint b;\n", "#define S \"open\nint b;\n", "#include <never\nint b;\n"} {
		lines, err := tokenizeSource(g, src)
		require.NoError(t, err)
		assert.Equal(t, []string{"storage.type.dgdlpc"}, lines[1][0].scopes, "%q", src)
	}
}

// TestVSCodeExtension: the extension contributes the language dgdlpc with
// this grammar and configuration, and claims no file names: a .c file is
// LPC only where a workspace says so.
func TestVSCodeExtension(t *testing.T) {
	var pkg struct {
		Name             string         `json:"name"`
		Engines          map[string]any `json:"engines"`
		ActivationEvents []string       `json:"activationEvents"`
		Contributes      struct {
			Languages []map[string]any `json:"languages"`
			Grammars  []struct {
				Language  string `json:"language"`
				ScopeName string `json:"scopeName"`
				Path      string `json:"path"`
			} `json:"grammars"`
		} `json:"contributes"`
	}
	data, err := os.ReadFile(vscodeDir + "/package.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &pkg))

	require.Len(t, pkg.Contributes.Languages, 1)
	lang := pkg.Contributes.Languages[0]
	assert.Equal(t, "dgdlpc", lang["id"])
	for _, key := range []string{"extensions", "filenames", "filenamePatterns", "firstLine", "mimetypes"} {
		assert.NotContains(t, lang, key, "the language must not claim files: *.c and *.h are C everywhere else")
	}
	conf, _ := lang["configuration"].(string)
	require.NotEmpty(t, conf)
	assert.FileExists(t, filepath.Join(vscodeDir, conf))

	require.Len(t, pkg.Contributes.Grammars, 1)
	gr := pkg.Contributes.Grammars[0]
	assert.Equal(t, "dgdlpc", gr.Language)
	assert.Equal(t, grammar(t).ScopeName, gr.ScopeName)
	assert.Equal(t, grammarPath, filepath.ToSlash(filepath.Join(vscodeDir, gr.Path)))

	assert.Contains(t, pkg.ActivationEvents, "workspaceContains:**/dgdtools.yml")
	assert.Contains(t, pkg.ActivationEvents, "workspaceContains:**/.dgdtools.yml")

	// The language configuration: the brackets, and patterns VS Code
	// compiles as JavaScript, which reads everything Go does.
	var lc struct {
		Comments struct {
			LineComment  string   `json:"lineComment"`
			BlockComment []string `json:"blockComment"`
		} `json:"comments"`
		Brackets         [][]string `json:"brackets"`
		IndentationRules struct {
			Increase string `json:"increaseIndentPattern"`
			Decrease string `json:"decreaseIndentPattern"`
		} `json:"indentationRules"`
		OnEnterRules []struct {
			BeforeText string `json:"beforeText"`
			AfterText  string `json:"afterText"`
		} `json:"onEnterRules"`
	}
	data, err = os.ReadFile(filepath.Join(vscodeDir, conf))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &lc))
	assert.Equal(t, []string{"/*", "*/"}, lc.Comments.BlockComment)
	assert.Contains(t, lc.Brackets, []string{"({", "})"})
	// "])" also ends f(a[i]): as a bracket of its own it would unbalance
	// every such line.
	assert.NotContains(t, lc.Brackets, []string{"([", "])"})
	for _, b := range lc.Brackets {
		assert.Len(t, b, 2)
	}
	patterns := []string{lc.IndentationRules.Increase, lc.IndentationRules.Decrease}
	for _, r := range lc.OnEnterRules {
		patterns = append(patterns, r.BeforeText)
		if r.AfterText != "" {
			patterns = append(patterns, r.AfterText)
		}
	}
	for _, p := range patterns {
		require.NotEmpty(t, p)
		_, err := regexp.Compile(p)
		assert.NoError(t, err, p)
	}
	increase := regexp.MustCompile(lc.IndentationRules.Increase)
	decrease := regexp.MustCompile(lc.IndentationRules.Decrease)
	for _, line := range []string{"if (x) {", "x = ({", "m = ([", "foo(a,", "{"} {
		assert.True(t, increase.MatchString(line), line)
	}
	for _, line := range []string{"x = ({ 1 });", "foo(a[1]);", "return \"{\";", "x = 1;"} {
		assert.False(t, increase.MatchString(line), line)
	}
	for _, line := range []string{"}", "    });", "\t]);", "  )"} {
		assert.True(t, decrease.MatchString(line), line)
	}
	assert.False(t, decrease.MatchString("x = a[1];"))
}
