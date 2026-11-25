package lint_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/token"
)

// suppress lints src with no-effect-statement, sscanf-format and the two
// rules about suppression comments, and returns "line rule message".
func suppress(t *testing.T, src string, opt lint.Options) (*lint.Result, []string) {
	t.Helper()
	prj, m := memProject(map[string]string{"obj/t.c": src})
	if opt.Select == nil {
		opt.Select = []string{"no-effect-statement", "sscanf-format", "invalid-suppression", "unused-suppression"}
	}
	res := run(t, prj, m, lint.Request{Paths: []string{"/lib/obj/t.c"}, Options: opt})
	var out []string
	for _, d := range res.Diagnostics {
		out = append(out, fmt.Sprintf("%d %s %s", d.Line, d.Rule, d.Message))
	}
	return res, out
}

func TestSuppressionComments(t *testing.T) {
	res, got := suppress(t, `void f() {
    a == 1;                            /* dgdtools-ignore lint/no-effect-statement */
    /* dgdtools-ignore lint/S002: by code, with a reason */
    b == 2;
    /* dgdtools-ignore lint */ c == 3;
    // dgdtools-ignore lint/sscanf-format, lint/no-effect-statement
    d == 4;
    e == 5;
    /* dgdtools-ignore-start lint/no-effect-statement */
    g == 6;
    h == 7;
    /* dgdtools-ignore-end lint/no-effect-statement */
    i == 8;
    /* dgdtools-ignore lint/no-effect-statement:
     * the reason may take more than one line */
    j == 9;
    /* see dgdtools-ignore lint/no-effect-statement in the manual */
    k == 10;
}
`, lint.Options{})
	assert.Equal(t, []string{
		"6 unused-suppression nothing to ignore: no sscanf-format finding on line 7",
		"8 no-effect-statement comparison has no effect as a statement (did you mean =?)",
		"13 no-effect-statement comparison has no effect as a statement (did you mean =?)",
		"18 no-effect-statement comparison has no effect as a statement (did you mean =?)",
	}, got, "a comment that only mentions the directive is prose")
	assert.Equal(t, 7, res.Stats.Suppressed)

	// The unused one is placed on what it names, as a range.
	d := res.Diagnostics[0]
	assert.Equal(t, []diag.Tag{diag.Unnecessary}, d.Tags)
	assert.Equal(t, 6, d.EndLine)
	assert.Equal(t, int(d.End-d.Start), d.EndCol-d.Col, "the columns span what the offsets span")
	assert.Equal(t, len("lint/sscanf-format"), d.EndCol-d.Col)
}

// A directive that cannot be followed is reported: it protects nothing
// while looking as if it did.
func TestInvalidSuppressions(t *testing.T) {
	_, got := suppress(t, `void f() {
    /* dgdtools-ignore lint/no-efect-statement */
    a == 1;
    /* dgdtools-ignore */
    /* dgdtools-ignore-start */
    /* dgdtools-ignore-start lint */
    /* dgdtools-ignore format */
    /* dgdtools-ignore fmt, lint/nope, lint/no-effect-statement */
    b == 2;
    /* dgdtools-ignore-end lint/no-effect-statement */
    /* dgdtools-ignore-end lint */
    /* dgdtools-ignore-end format */
    /* dgdtools-ignore-end */
    /* dgdtools-ignore-start lint/no-effect-statement */
    /* dgdtools-ignore-start lint/S002, format */
    /* dgdtools-ignore-start format */
    c == 3;
    /* dgdtools-ignore-end */
    d == 4;
}
`, lint.Options{})
	assert.Equal(t, []string{
		`2 invalid-suppression unknown rule "no-efect-statement" (see ` + "`dgdtools rules`)",
		"3 no-effect-statement comparison has no effect as a statement (did you mean =?)",
		"4 invalid-suppression dgdtools-ignore names nothing to ignore: say lint/RULE or format",
		"5 invalid-suppression dgdtools-ignore-start names nothing to ignore: say lint/RULE or format",
		"6 invalid-suppression a range names the rules it is for (lint/RULE): every rule at once would hide whatever comes later",
		"7 invalid-suppression the formatter is turned off for a range of lines: dgdtools-ignore-start format ... dgdtools-ignore-end format",
		`8 invalid-suppression unknown target "fmt": want lint/RULE, lint or format`,
		`8 invalid-suppression unknown rule "nope" (see ` + "`dgdtools rules`)",
		"10 invalid-suppression lint/no-effect-statement is not being ignored here: no dgdtools-ignore-start lint/no-effect-statement above",
		"11 invalid-suppression no rule is being ignored here: no dgdtools-ignore-start above",
		"12 invalid-suppression the formatter is not off here: no dgdtools-ignore-start format above",
		"13 invalid-suppression nothing to end: no dgdtools-ignore-start above",
		"15 invalid-suppression lint/S002 is already ignored, since line 14",
		"16 invalid-suppression the formatter is already off, since line 15",
		"19 no-effect-statement comparison has no effect as a statement (did you mean =?)",
	}, got, "the valid part of a directive still holds: line 9 is suppressed, and the range of line 14 ends at line 18")

	// The rule is on by default, and can be turned off like any other.
	_, got = suppress(t, "void f() {\n    /* dgdtools-ignore lint/nope */\n    a == 1;\n}\n", lint.Options{Select: []string{}})
	require.Len(t, got, 2)
	assert.Contains(t, got[0], "invalid-suppression")
	_, got = suppress(t, "void f() {\n    /* dgdtools-ignore lint/nope */\n    a == 1;\n}\n", lint.Options{Select: []string{}, Disable: []string{"invalid-suppression"}})
	assert.Len(t, got, 1)
}

// A suppression is unused when the rule it names ran and found nothing
// there; when the rule did not run, nothing is known.
func TestUnusedSuppressions(t *testing.T) {
	src := `void f() {
    a = 1;      /* dgdtools-ignore lint/no-effect-statement */
    b = 2;      /* dgdtools-ignore lint */
    /* dgdtools-ignore-start lint/no-effect-statement, lint/callable-not-found */
    c = 3;
    /* dgdtools-ignore-end lint */
    /* dgdtools-ignore-start lint/sscanf-format */
    d = 4;
}
`
	_, got := suppress(t, src, lint.Options{})
	assert.Equal(t, []string{
		"2 unused-suppression nothing to ignore: no no-effect-statement finding on line 2",
		"3 unused-suppression nothing to ignore: no finding on line 3",
		"4 unused-suppression nothing to ignore: no no-effect-statement finding in lines 4 to 6",
		"7 unused-suppression nothing to ignore: no sscanf-format finding from line 7 on",
	}, got, "callable-not-found did not run: not said to be unused")

	// Off by default.
	_, got = suppress(t, src, lint.Options{Select: []string{}})
	assert.Empty(t, got)
	// What these rules report is not itself suppressed by a comment.
	_, got = suppress(t, "void f() {\n    a = 1; /* dgdtools-ignore lint, lint/unused-suppression */\n}\n", lint.Options{})
	require.Len(t, got, 2)
	assert.True(t, strings.HasPrefix(got[0], "2 unused-suppression nothing to ignore: no finding on line 2"), got[0])
	assert.True(t, strings.HasPrefix(got[1], "2 invalid-suppression unused-suppression is about these comments themselves"), got[1])
}

// Suppress writes the comment a finding is ignored by: applied, the
// finding is gone, every other one stands, and no comment is wrong.
func TestSuppress(t *testing.T) {
	sources := map[string]string{
		"plain": "void f() {\n    a == 1;\n\tb == 2; c == 3;\n}\n",
		// A comment that is about the line already takes the rule too.
		"merge above":         "void f() {\n    /* dgdtools-ignore lint/sscanf-format: why */\n    a == 1;\n}\n",
		"merge beside":        "void f() {\n    a == 1; // dgdtools-ignore lint/sscanf-format\n}\n",
		"crlf":                "void f() {\r\n    a == 1;\r\n}\r\n",
		"no final line break": "void f() {\n    a == 1; }",
		// The line break before the finding's line is inside a comment
		// or a directive: no line can be put there.
		"in a comment": "void f() { /* one\n two */ a == 1;\n}\n",
		"in a macro":   "#define F void f() { \\\n    a == 1; }\nF\nvoid g() { b == 2; }\n",
		"a range ends above": "void f() {\n/* dgdtools-ignore-start lint/no-effect-statement */\n a == 1;\n" +
			"/* dgdtools-ignore-end lint/no-effect-statement */\n b == 2;\n}\n",
	}
	sel := lint.Options{Select: []string{"no-effect-statement", "invalid-suppression", "unused-suppression"}}
	for name, src := range sources {
		t.Run(name, func(t *testing.T) {
			res, before := suppress(t, src, sel)
			require.NotEmpty(t, before)
			for _, d := range res.Diagnostics {
				if d.Rule != "no-effect-statement" {
					continue
				}
				fixes := lint.Suppress(lex(src), d)
				var titles []string
				for _, fix := range fixes {
					titles = append(titles, fix.Title)
					assert.Equal(t, diag.Unsafe, fix.Applicability)
					out := src
					for _, e := range fix.Edits {
						out = out[:e.Start] + e.NewText + out[e.End:]
					}
					_, after := suppress(t, out, sel)
					byFile := strings.HasSuffix(fix.Title, "this file")
					var want []string
					for j, other := range res.Diagnostics {
						// A comment is about a rule on a line, or in the file.
						if other.Rule != d.Rule || !(byFile || other.Line == d.Line) {
							want = append(want, strings.SplitN(before[j], " ", 2)[1])
						}
					}
					var got []string
					for _, a := range after {
						got = append(got, strings.SplitN(a, " ", 2)[1])
					}
					assert.Equal(t, want, got, "%s:\n%s", fix.Title, out)
					if strings.Contains(src, "\r\n") {
						assert.NotContains(t, strings.ReplaceAll(out, "\r\n", ""), "\n", "the file's line breaks")
					}
				}
				switch name {
				case "in a comment":
					assert.Equal(t, []string{"Suppress no-effect-statement for this file"}, titles)
				case "in a macro":
					if d.Line == 2 {
						assert.Equal(t, []string{"Suppress no-effect-statement for this file"}, titles)
					}
				case "a range ends above":
					assert.Equal(t, []string{"Suppress no-effect-statement for this line"}, titles)
				default:
					assert.Equal(t, []string{"Suppress no-effect-statement for this line", "Suppress no-effect-statement for this file"}, titles)
				}
			}
		})
	}

	// The comment is indented as the line it is about.
	src := sources["plain"]
	res, _ := suppress(t, src, sel)
	fix := lint.Suppress(lex(src), res.Diagnostics[1])[0]
	assert.Equal(t, "\t/* dgdtools-ignore lint/no-effect-statement */\n", fix.Edits[0].NewText)
	// The rules about the comments themselves are not ignored by one.
	res, _ = suppress(t, "/* dgdtools-ignore lint/nope */\nint x;\n", sel)
	require.Len(t, res.Diagnostics, 1)
	assert.Empty(t, lint.Suppress(lex("/* dgdtools-ignore lint/nope */\nint x;\n"), res.Diagnostics[0]))
}

func lex(src string) *token.File { return lexer.Lex("/lib/obj/t.c", []byte(src), lexer.Default) }
