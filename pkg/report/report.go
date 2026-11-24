// Package report renders lint results: human-readable text, JSON, SARIF
// 2.1.0, and GitHub Actions workflow annotations. Writers are pure
// functions of the result; nothing here decides exit codes.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/lint"
)

// Formats lists the supported output formats.
var Formats = []string{"human", "json", "sarif", "github"}

// Options controls rendering.
type Options struct {
	// BaseDir, when set, makes paths in the output relative to it (when
	// they are under it).
	BaseDir string
	// Version is the tool version, for SARIF and JSON.
	Version string
}

func (o Options) rel(p string) string {
	if o.BaseDir == "" {
		return p
	}
	if r, err := filepath.Rel(o.BaseDir, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

// Write renders res in the named format.
func Write(w io.Writer, format string, res *lint.Result, rules []*lint.Rule, o Options) error {
	switch format {
	case "human", "":
		return Human(w, res, o)
	case "json":
		return JSON(w, res, o)
	case "sarif":
		return SARIF(w, res, rules, o)
	case "github":
		return GitHub(w, res, o)
	}
	return fmt.Errorf("unknown output format %q (want %s)", format, strings.Join(Formats, ", "))
}

// Human writes one line per finding in the classic compiler format, with
// related locations indented beneath.
func Human(w io.Writer, res *lint.Result, o Options) error {
	for _, d := range res.Diagnostics {
		if _, err := fmt.Fprintf(w, "%s:%d:%d: %s: %s [%s]\n",
			o.rel(d.Path), d.Line, d.Col, d.Severity, d.Message, d.Rule); err != nil {
			return err
		}
		for _, r := range d.Related {
			fmt.Fprintf(w, "    %s:%d:%d: note: %s\n", o.rel(r.Path), r.Line, r.Col, r.Message)
		}
	}
	return nil
}

// Summary writes the one-paragraph run summary (meant for stderr).
func Summary(w io.Writer, res *lint.Result, verbose bool) {
	counts := map[diag.Severity]int{}
	byRule := map[string]int{}
	for _, d := range res.Diagnostics {
		counts[d.Severity]++
		byRule[d.Rule]++
	}
	for _, n := range res.Notes {
		fmt.Fprintf(w, "note: %s\n", n)
	}
	for _, s := range res.Skipped {
		fmt.Fprintf(w, "warning: could not read %s\n", s)
	}
	st := res.Stats
	fmt.Fprintf(w, "%d file(s): %d error(s), %d warning(s), %d info", st.Files,
		counts[diag.Error], counts[diag.Warning], counts[diag.Info])
	if st.Baselined > 0 {
		fmt.Fprintf(w, "; %d in baseline", st.Baselined)
	}
	if st.Suppressed > 0 {
		fmt.Fprintf(w, "; %d suppressed by comments", st.Suppressed)
	}
	fmt.Fprintln(w)
	if !verbose {
		return
	}
	var names []string
	for r := range byRule {
		names = append(names, r)
	}
	sort.Strings(names)
	for _, r := range names {
		fmt.Fprintf(w, "  %6d  %s\n", byRule[r], r)
	}
	fmt.Fprintf(w, "not verified (never reported as findings):\n")
	fmt.Fprintf(w, "  %6d  inherit path(s) the macro evaluator could not resolve\n", st.UnresolvedInherits)
	fmt.Fprintf(w, "  %6d  #include(s) that could not be resolved but are not provably missing\n", st.UnknownIncludes)
	fmt.Fprintf(w, "  %6d  file(s) with an incomplete symbol table\n", st.PartialFiles)
	fmt.Fprintf(w, "  %6d  of %d late-bound call sites with an unknown receiver\n", st.UnknownReceivers, st.CallSites)
	fmt.Fprintf(w, "  %6d  syntax error(s) withheld as possible macro artifacts\n", st.TaintedSyntax)
	fmt.Fprintf(w, "  %6d  finding(s) withheld inside #if branches that could not be decided\n", st.GuessedBranch)
}

// ------------------------------------------------------------------ JSON

type jsonRange struct {
	Line      int    `json:"line"`
	Col       int    `json:"col"`
	EndLine   int    `json:"end_line"`
	EndCol    int    `json:"end_col"`
	Offset    uint32 `json:"offset"`
	EndOffset uint32 `json:"end_offset"`
}

type jsonRelated struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Message string `json:"message"`
}

type jsonEdit struct {
	Offset    uint32 `json:"offset"`
	EndOffset uint32 `json:"end_offset"`
	NewText   string `json:"new_text"`
}

type jsonFix struct {
	Title string     `json:"title"`
	Edits []jsonEdit `json:"edits"`
}

type jsonDiag struct {
	Rule     string        `json:"rule"`
	Code     string        `json:"code"`
	Severity string        `json:"severity"`
	Path     string        `json:"path"`
	Line     int           `json:"line"`
	Col      int           `json:"col"`
	Range    jsonRange     `json:"range"`
	Message  string        `json:"message"`
	Related  []jsonRelated `json:"related,omitempty"`
	Fixes    []jsonFix     `json:"fixes,omitempty"`
}

type jsonOut struct {
	Version     int        `json:"version"`
	Tool        string     `json:"tool"`
	Diagnostics []jsonDiag `json:"diagnostics"`
	Summary     jsonSum    `json:"summary"`
	Notes       []string   `json:"notes,omitempty"`
	Skipped     []string   `json:"skipped,omitempty"`
}

type jsonSum struct {
	Errors   int        `json:"errors"`
	Warnings int        `json:"warnings"`
	Infos    int        `json:"infos"`
	Stats    lint.Stats `json:"stats"`
}

// JSON writes a versioned JSON document. line and col are 1-based byte
// positions; offsets are byte offsets into the file.
func JSON(w io.Writer, res *lint.Result, o Options) error {
	out := jsonOut{Version: 1, Tool: "dgdlint " + o.Version, Diagnostics: []jsonDiag{},
		Notes: res.Notes, Skipped: res.Skipped}
	out.Summary.Stats = res.Stats
	for _, d := range res.Diagnostics {
		switch d.Severity {
		case diag.Error:
			out.Summary.Errors++
		case diag.Warning:
			out.Summary.Warnings++
		default:
			out.Summary.Infos++
		}
		jd := jsonDiag{
			Rule: d.Rule, Code: d.Code, Severity: d.Severity.String(),
			Path: o.rel(d.Path), Line: d.Line, Col: d.Col, Message: d.Message,
			Range: jsonRange{d.Line, d.Col, d.EndLine, d.EndCol, d.Start, d.End},
		}
		for _, r := range d.Related {
			jd.Related = append(jd.Related, jsonRelated{o.rel(r.Path), r.Line, r.Col, r.Message})
		}
		for _, f := range d.Fixes {
			jf := jsonFix{Title: f.Title}
			for _, e := range f.Edits {
				jf.Edits = append(jf.Edits, jsonEdit{e.Start, e.End, e.NewText})
			}
			jd.Fixes = append(jd.Fixes, jf)
		}
		out.Diagnostics = append(out.Diagnostics, jd)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}

// ---------------------------------------------------------------- GitHub

// GitHub writes workflow commands that GitHub Actions turns into inline
// annotations on the pull request.
func GitHub(w io.Writer, res *lint.Result, o Options) error {
	esc := func(s string) string {
		s = strings.ReplaceAll(s, "%", "%25")
		s = strings.ReplaceAll(s, "\r", "%0D")
		return strings.ReplaceAll(s, "\n", "%0A")
	}
	prop := func(s string) string {
		s = esc(s)
		s = strings.ReplaceAll(s, ":", "%3A")
		return strings.ReplaceAll(s, ",", "%2C")
	}
	for _, d := range res.Diagnostics {
		level := "notice"
		switch d.Severity {
		case diag.Error:
			level = "error"
		case diag.Warning:
			level = "warning"
		}
		if _, err := fmt.Fprintf(w, "::%s file=%s,line=%d,col=%d,endLine=%d,title=%s::%s\n",
			level, prop(o.rel(d.Path)), d.Line, d.Col, d.EndLine, prop("dgdlint "+d.Rule), esc(d.Message)); err != nil {
			return err
		}
	}
	return nil
}

// ----------------------------------------------------------------- SARIF

type sarifMsg struct {
	Text string `json:"text"`
}

type sarifRegion struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn"`
	EndLine     int `json:"endLine,omitempty"`
	EndColumn   int `json:"endColumn,omitempty"`
}

type sarifLocation struct {
	PhysicalLocation struct {
		ArtifactLocation struct {
			URI string `json:"uri"`
		} `json:"artifactLocation"`
		Region sarifRegion `json:"region"`
	} `json:"physicalLocation"`
	Message *sarifMsg `json:"message,omitempty"`
}

type sarifResult struct {
	RuleID           string          `json:"ruleId"`
	Level            string          `json:"level"`
	Message          sarifMsg        `json:"message"`
	Locations        []sarifLocation `json:"locations"`
	RelatedLocations []sarifLocation `json:"relatedLocations,omitempty"`
}

type sarifRule struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	ShortDescription sarifMsg          `json:"shortDescription"`
	FullDescription  sarifMsg          `json:"fullDescription"`
	DefaultConfig    map[string]string `json:"defaultConfiguration"`
	Properties       map[string]any    `json:"properties"`
}

func sarifLoc(path string, line, col, endLine, endCol int, msg string) sarifLocation {
	var l sarifLocation
	l.PhysicalLocation.ArtifactLocation.URI = filepath.ToSlash(path)
	l.PhysicalLocation.Region = sarifRegion{line, col, endLine, endCol}
	if msg != "" {
		l.Message = &sarifMsg{msg}
	}
	return l
}

func sarifLevel(s diag.Severity) string {
	switch s {
	case diag.Error:
		return "error"
	case diag.Warning:
		return "warning"
	}
	return "note"
}

// SARIF writes a SARIF 2.1.0 log for code-scanning upload. ruleId is the
// rule's stable name; the short code is in the rule's properties.
func SARIF(w io.Writer, res *lint.Result, rules []*lint.Rule, o Options) error {
	var srules []sarifRule
	for _, r := range rules {
		srules = append(srules, sarifRule{
			ID: r.Name, Name: r.Name,
			ShortDescription: sarifMsg{r.Doc}, FullDescription: sarifMsg{r.Explain},
			DefaultConfig: map[string]string{"level": sarifLevel(r.Severity)},
			Properties:    map[string]any{"code": r.Code, "group": r.Group},
		})
	}
	results := []sarifResult{}
	for _, d := range res.Diagnostics {
		sr := sarifResult{
			RuleID: d.Rule, Level: sarifLevel(d.Severity), Message: sarifMsg{d.Message},
			Locations: []sarifLocation{sarifLoc(o.rel(d.Path), d.Line, d.Col, d.EndLine, d.EndCol, "")},
		}
		for _, r := range d.Related {
			sr.RelatedLocations = append(sr.RelatedLocations, sarifLoc(o.rel(r.Path), r.Line, r.Col, 0, 0, r.Message))
		}
		results = append(results, sr)
	}
	doc := map[string]any{
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json",
		"version": "2.1.0",
		"runs": []any{map[string]any{
			"tool": map[string]any{"driver": map[string]any{
				"name": "dgdlint", "version": o.Version,
				"informationUri": "https://github.com/mmcdole/dgdtools",
				"rules":          srules,
			}},
			"results": results,
		}},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(doc)
}
