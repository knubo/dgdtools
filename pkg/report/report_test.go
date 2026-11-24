package report_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/report"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// result is a run with one of everything a report can hold: findings of
// each severity, in files under the base directory and outside it, with a
// related location in the same file, in another file and in a profile's
// stub, a fix, a tag, a note, and files that were not linted.
func result() *lint.Result {
	return &lint.Result{
		Generation: 3,
		Diagnostics: []diag.Diagnostic{
			{
				Path: "/work/mud/lib/obj/key.c", Line: 9, Col: 19, EndLine: 9, EndCol: 29, Start: 140, End: 150,
				Severity: diag.Error, Rule: "callable-not-found", Code: "R001", Fingerprint: "00000000000000a1",
				Message: "function 'do_unlok' is not defined in /obj/key (this_object) (called via add_action)",
			},
			{
				Path: "/work/mud/lib/obj/key.c", Line: 12, Col: 9, EndLine: 12, EndCol: 12, Start: 201, End: 204,
				Severity: diag.Error, Rule: "adjacent-strings", Code: "E002", Fingerprint: "00000000000000b2",
				Message: "adjacent string literals are a syntax error in DGD; join them with '+'",
				Fixes: []diag.Fix{{
					Title: "insert '+' between the strings", Applicability: diag.Safe,
					Edits: []diag.TextEdit{{Start: 200, End: 200, NewText: " +"}},
				}},
			},
			{
				Path: "/work/mud/lib/obj/key.c", Line: 20, Col: 5, EndLine: 21, EndCol: 14, Start: 300, End: 331,
				Severity: diag.Warning, Rule: "unreachable-code", Code: "S004", Fingerprint: "00000000000000c3",
				Tags:    []diag.Tag{diag.Unnecessary},
				Message: "unreachable code",
				Related: []diag.Related{{Path: "/work/mud/lib/obj/key.c", Line: 19, Col: 5, Start: 280, End: 289, Message: "control leaves here"}},
			},
			{
				Path: "/work/mud/lib/room/a b.c", Line: 3, Col: 1, EndLine: 3, EndCol: 8, Start: 40, End: 47,
				Severity: diag.Error, Rule: "static-crossobj", Code: "R002", Fingerprint: "00000000000000d4",
				Message: "'hidden' in /std/room is static — call_other from another object returns nil",
				Related: []diag.Related{
					{Path: "dgdtools-profile://my lib/std/room.c", Line: 5, Col: 12, Start: 77, End: 77, Message: "defined here"},
					{Path: "/work/mud/lib/std/base.c", Line: 2, Col: 1, Message: "inherited through here"},
				},
			},
			{
				Path: "/elsewhere/loose.c", Line: 1, Col: 1, EndLine: 1, EndCol: 1,
				Severity: diag.Info, Rule: "unformatted", Code: "C003", Fingerprint: "00000000000000e5",
				Message: "file is not formatted (run `dgdtools format`)\nsecond line, 100% of it",
			},
		},
		Stats: lint.Stats{
			Files: 41, Suppressed: 2, Baselined: 7, Capped: 3,
			UnresolvedInherits: 1, UnknownIncludes: 2, PartialFiles: 3, CallSites: 120, UnknownReceivers: 80,
			TaintedSyntax: 4, GuessedBranch: 5,
		},
		Notes: []diag.Note{{Kind: diag.NoteProfile, Path: "/work/my lib.profile", Message: "profile my lib (12 stubs) from /work/my lib.profile"}},
		Errors: []*lint.FileError{
			{Path: "/work/mud/lib/obj/bug.c", Kind: lint.Internal, Message: "internal error in rule no-effect-statement (please report): index out of range"},
			{Path: "/work/mud/lib/obj/locked.c", Kind: lint.Unreadable, Message: "cannot be read: permission denied"},
		},
	}
}

// rules is the rule table the result is reported with: the rules that
// occur and one that does not, with texts of their own, so that the
// golden files do not change with the wording of the real rules.
func rules() []*lint.Rule {
	return []*lint.Rule{
		{Code: "E002", Name: "adjacent-strings", Group: lint.GroupSyntax, Severity: diag.Error, Doc: "adjacent strings", Explain: "Two strings side by side."},
		{Code: "R001", Name: "callable-not-found", Group: lint.GroupRuntime, Severity: diag.Error, Doc: "no such function", Explain: "The function is not there."},
		{Code: "R002", Name: "static-crossobj", Group: lint.GroupRuntime, Severity: diag.Error, Doc: "out of reach", Explain: "The function cannot be called from there."},
		{Code: "S004", Name: "unreachable-code", Group: lint.GroupSuspicious, Severity: diag.Warning, Doc: "never runs", Explain: "Nothing gets here."},
		{Code: "C003", Name: "unformatted", Group: lint.GroupConvention, Severity: diag.Info, Doc: "not formatted", Explain: "The formatter would change it."},
		{Code: "C002", Name: "missing-visibility", Group: lint.GroupConvention, Severity: diag.Warning, Doc: "no specifier", Explain: "Not among the findings."},
	}
}

// golden compares got with the file of that name in testdata.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		require.NoError(t, os.WriteFile(path, got, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got), "%s (go test ./pkg/report -update rewrites it)", name)
}

func TestFormats(t *testing.T) {
	rules := rules()
	for _, base := range []struct{ name, dir string }{{"", "/work/mud"}, {"-absolute", ""}} {
		o := report.Options{BaseDir: base.dir, Version: "v1.2.3"}
		for _, f := range []struct{ format, file string }{
			{"human", "human" + base.name + ".txt"},
			{"json", "report" + base.name + ".json"},
			{"sarif", "report" + base.name + ".sarif"},
			{"github", "github" + base.name + ".txt"},
		} {
			var b bytes.Buffer
			require.NoError(t, report.Write(&b, f.format, result(), rules, o))
			golden(t, f.file, b.Bytes())
		}
		for verbose, file := range map[bool]string{false: "summary" + base.name + ".txt", true: "summary" + base.name + "-stats.txt"} {
			var b bytes.Buffer
			report.Summary(&b, result(), o, verbose)
			golden(t, file, b.Bytes())
		}
	}
	assert.Error(t, report.Write(&bytes.Buffer{}, "xml", result(), rules, report.Options{}))

	// Nothing found is still a document.
	var b bytes.Buffer
	require.NoError(t, report.Write(&b, "json", &lint.Result{}, rules, report.Options{Version: "v1.2.3"}))
	golden(t, "empty.json", b.Bytes())
	b.Reset()
	require.NoError(t, report.Write(&b, "", &lint.Result{}, rules, report.Options{}))
	assert.Empty(t, b.String(), "the default format is human, and silent when there is nothing")
}

// What the golden files cannot say for themselves: a SARIF location is a
// URI wherever the file is, and the machine formats are valid JSON.
func TestSARIFLocations(t *testing.T) {
	var b bytes.Buffer
	require.NoError(t, report.SARIF(&b, result(), rules(), report.Options{BaseDir: "/work/mud", Version: "v1"}))
	var doc struct {
		Runs []struct {
			OriginalURIBaseIDs map[string]struct{ URI string } `json:"originalUriBaseIds"`
			Tool               struct {
				Driver struct {
					Rules []struct{ ID, HelpURI string }
				}
			}
			Results []struct {
				RuleID    string
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct{ URI, URIBaseID string }
						Region           struct{ StartLine, StartColumn, ByteOffset, ByteLength int }
					}
				}
				RelatedLocations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct{ URI, URIBaseID string }
					}
				}
				PartialFingerprints map[string]string
			}
		}
	}
	require.NoError(t, json.Unmarshal(b.Bytes(), &doc))
	run := doc.Runs[0]
	assert.Equal(t, "file:///work/mud/", run.OriginalURIBaseIDs["%SRCROOT%"].URI)
	assert.Len(t, run.Tool.Driver.Rules, len(rules()), "every rule of the table, found or not")
	assert.Equal(t, "https://github.com/mmcdole/dgdtools/blob/main/docs/rules.md#adjacent-strings", run.Tool.Driver.Rules[0].HelpURI)

	loc := func(i int) (uri, base string) {
		a := run.Results[i].Locations[0].PhysicalLocation.ArtifactLocation
		return a.URI, a.URIBaseID
	}
	uri, base := loc(0)
	assert.Equal(t, "lib/obj/key.c", uri)
	assert.Equal(t, "%SRCROOT%", base)
	uri, _ = loc(3)
	assert.Equal(t, "lib/room/a%20b.c", uri, "a URI reference, not a path")
	uri, base = loc(4)
	assert.Equal(t, "file:///elsewhere/loose.c", uri, "outside the base: absolute")
	assert.Empty(t, base)
	rel := run.Results[3].RelatedLocations
	assert.Equal(t, "dgdtools-profile://my%20lib/std/room.c", rel[0].PhysicalLocation.ArtifactLocation.URI, "a stub is named by its own URI")
	assert.Empty(t, rel[0].PhysicalLocation.ArtifactLocation.URIBaseID)
	assert.Equal(t, "lib/std/base.c", rel[1].PhysicalLocation.ArtifactLocation.URI)

	r := run.Results[0].Locations[0].PhysicalLocation.Region
	assert.Equal(t, []int{9, 19, 140, 10}, []int{r.StartLine, r.StartColumn, r.ByteOffset, r.ByteLength})
	assert.Equal(t, map[string]string{"dgdtools/line/v1": "00000000000000a1"}, run.Results[0].PartialFingerprints)
}
