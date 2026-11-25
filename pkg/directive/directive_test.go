package directive

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/lexer"
)

func TestParse(t *testing.T) {
	for src, want := range map[string]string{
		"// dgdtools-ignore lint/adjacent-strings":                  "dgdtools-ignore [lint/adjacent-strings]",
		"/* dgdtools-ignore lint/E002 */":                           "dgdtools-ignore [lint/E002]",
		"/* dgdtools-ignore lint/a, lint/b,lint/c */":               "dgdtools-ignore [lint/a lint/b lint/c]",
		"/* dgdtools-ignore lint: the whole line */":                "dgdtools-ignore [lint]",
		"/* dgdtools-ignore lint/x: because: it says so */":         "dgdtools-ignore [lint/x]",
		"/* dgdtools-ignore-start format */":                        "dgdtools-ignore-start [format]",
		"//dgdtools-ignore-start format, lint/unformatted: a table": "dgdtools-ignore-start [format lint/unformatted]",
		"/* dgdtools-ignore-end format */":                          "dgdtools-ignore-end [format]",
		"/* dgdtools-ignore-end */":                                 "dgdtools-ignore-end []",
		"/* dgdtools-ignore */":                                     "dgdtools-ignore []",
		"/* dgdtools-ignore: nothing named */":                      "dgdtools-ignore []",
		"/** dgdtools-ignore lint/x **/":                            "dgdtools-ignore [lint/x]",
		"/* dgdtools-ignore lint/x: a reason\n * that goes on */":   "dgdtools-ignore [lint/x]",
		"/* dgdtools-ignore-start lint/x\n   lint/y */":             "dgdtools-ignore-start [lint/x]",
		"/* dgdtools-ignore fmt, lint/ */":                          "dgdtools-ignore [fmt lint/]",
		"/*\n * dgdtools-ignore lint/x\n */":                        "", // not the first thing in its comment
		// Not directives: prose, and words that only begin the same.
		"/* use dgdtools-ignore lint/x to silence this */": "",
		"/* dgdtools-ignored lint/x */":                    "",
		"/* dgdtools-ignore-starting */":                   "",
		"/* dgdtools: ignore */":                           "",
		"// plain":                                         "",
		"//":                                               "",
		"/**/":                                             "",
		"/***/":                                            "",
		"/* */":                                            "",
		"/*":                                               "", // not closed
	} {
		f := lexer.Lex("t.c", []byte(src), lexer.Default)
		d, ok := Parse(f, f.Tokens[0])
		assert.Equal(t, want, render(d, ok), src)
		for _, tg := range d.Targets {
			assert.Equal(t, tg.String(), src[tg.Off:tg.End], src)
		}
	}
}

func render(d Directive, ok bool) string {
	if !ok {
		return ""
	}
	s := d.Form.String() + " ["
	for i, tg := range d.Targets {
		if i > 0 {
			s += " "
		}
		s += tg.String()
	}
	return s + "]"
}

func TestScan(t *testing.T) {
	src := "int a; /* dgdtools-ignore lint/x */\n\"dgdtools-ignore lint/y\";\n// dgdtools-ignore-start format\nint b;\n"
	f := lexer.Lex("t.c", []byte(src), lexer.Default)
	ds := Scan(f)
	require.Len(t, ds, 2, "a string is not a comment")
	assert.Equal(t, Line, ds[0].Form)
	assert.True(t, ds[0].Has(Lint))
	assert.False(t, ds[0].Has(Format))
	assert.Equal(t, "/* dgdtools-ignore lint/x */", src[ds[0].Off:ds[0].End])
	assert.Equal(t, Start, ds[1].Form)
	assert.True(t, ds[1].Has(Format))
	assert.Equal(t, "x", ds[0].Targets[0].Rule)
}
