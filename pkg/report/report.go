// Package report renders lint results: human-readable text, JSON, SARIF
// 2.1.0, and GitHub Actions workflow annotations. Writers are pure
// functions of the result; nothing here decides exit codes.
package report

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/diag"
	"github.com/mmcdole/dgdtools/pkg/fileset"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/profile"
)

// Formats lists the supported output formats.
var Formats = []string{"human", "json", "sarif", "github"}

// Options controls rendering.
type Options struct {
	// BaseDir is the directory paths are written relative to: a file
	// under it is named by its path from there, any other by its
	// absolute path. A front end gives its working directory. Empty:
	// every path is absolute.
	BaseDir string
	// Version is the tool version, for SARIF and JSON.
	Version string
}

// A location in a result is a file on disk or a stub of a profile, which
// is not a file and is named by a URI (profile.Location).

// name returns how a location is written for people and in JSON: a file
// by its path, relative to BaseDir when it is under it; a stub by its
// URI.
func (o Options) name(p string) string {
	if rel, ok := o.under(p); ok {
		return rel
	}
	return p
}

// under returns the path of the file p from BaseDir, when it is a file
// under BaseDir.
func (o Options) under(p string) (string, bool) {
	if _, _, stub := profile.SplitLocation(p); stub || o.BaseDir == "" {
		return "", false
	}
	return fileset.Under(o.BaseDir, p)
}

// Write renders res in the named format.
func Write(w io.Writer, format string, res *lint.Result, rules []*lint.Rule, o Options) error {
	var write func(io.Writer) error
	switch format {
	case "human", "":
		write = func(w io.Writer) error { return Human(w, res, o) }
	case "json":
		write = func(w io.Writer) error { return JSON(w, res, rules, o) }
	case "sarif":
		write = func(w io.Writer) error { return SARIF(w, res, rules, o) }
	case "github":
		write = func(w io.Writer) error { return GitHub(w, res, o) }
	default:
		return fmt.Errorf("unknown output format %q (want %s)", format, strings.Join(Formats, ", "))
	}
	// A finding is a line or a few: written as they come, they would be
	// a system call each.
	b := bufio.NewWriter(w)
	if err := write(b); err != nil {
		return err
	}
	return b.Flush()
}

// Human writes one line per finding in the classic compiler format, the
// rule's code and name in brackets at the end, with related locations
// indented beneath.
func Human(w io.Writer, res *lint.Result, o Options) error {
	for _, d := range res.Diagnostics {
		if _, err := fmt.Fprintf(w, "%s:%d:%d: %s: %s [%s %s]\n",
			o.name(d.Path), d.Line, d.Col, d.Severity, d.Message, d.Code, d.Rule); err != nil {
			return err
		}
		for _, r := range d.Related {
			fmt.Fprintf(w, "    %s:%d:%d: note: %s\n", o.name(r.Path), r.Line, r.Col, r.Message)
		}
	}
	return nil
}

// Summary writes the one-paragraph run summary (meant for stderr).
func Summary(w io.Writer, res *lint.Result, o Options, verbose bool) {
	counts := map[diag.Severity]int{}
	byRule := map[string]int{}
	for _, d := range res.Diagnostics {
		counts[d.Severity]++
		byRule[d.Rule]++
	}
	for _, n := range res.Notes {
		fmt.Fprintf(w, "note: %s\n", n.Message)
	}
	for _, e := range res.Errors {
		level := "warning"
		if e.Kind == lint.Internal {
			level = "error"
		}
		fmt.Fprintf(w, "%s: %s: %s\n", level, o.name(e.Path), e.Message)
	}
	st := res.Stats
	fmt.Fprintf(w, "%s: %s, %s, %d info", plural(st.Files, "file"),
		plural(counts[diag.Error], "error"), plural(counts[diag.Warning], "warning"), counts[diag.Info])
	if st.Baselined > 0 {
		fmt.Fprintf(w, "; %d in baseline", st.Baselined)
	}
	if st.Suppressed > 0 {
		fmt.Fprintf(w, "; %d suppressed by comments", st.Suppressed)
	}
	if st.Capped > 0 {
		fmt.Fprintf(w, "; %d more over a per-file limit", st.Capped)
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
	Title         string     `json:"title"`
	Applicability string     `json:"applicability"` // safe | unsafe
	Edits         []jsonEdit `json:"edits"`
}

type jsonDiag struct {
	Rule        string        `json:"rule"`
	Code        string        `json:"code"`
	Severity    string        `json:"severity"`
	Path        string        `json:"path"`
	Line        int           `json:"line"`
	Col         int           `json:"col"`
	Range       jsonRange     `json:"range"`
	Message     string        `json:"message"`
	Tags        []string      `json:"tags,omitempty"` // unnecessary | deprecated
	Fingerprint string        `json:"fingerprint,omitempty"`
	Related     []jsonRelated `json:"related,omitempty"`
	Fixes       []jsonFix     `json:"fixes,omitempty"`
}

type jsonOut struct {
	Version     int             `json:"version"`
	Tool        string          `json:"tool"`
	Diagnostics json.RawMessage `json:"diagnostics"` // written by encodeAround
	// Rules describes the rules that have a diagnostic above, by name.
	Rules   map[string]jsonRule `json:"rules,omitempty"`
	Summary jsonSum             `json:"summary"`
	Notes   []jsonNote          `json:"notes,omitempty"`
	Errors  []jsonError         `json:"errors,omitempty"`
}

type jsonRule struct {
	Code  string `json:"code"`
	Group string `json:"group"`
	Doc   string `json:"doc"`
	URL   string `json:"url"`
}

type jsonNote struct {
	Kind    diag.NoteKind `json:"kind"`
	Message string        `json:"message"`
	Path    string        `json:"path,omitempty"`
}

type jsonError struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"` // unreadable | internal | stale
	Message string `json:"message"`
}

type jsonSum struct {
	Errors   int        `json:"errors"`
	Warnings int        `json:"warnings"`
	Infos    int        `json:"infos"`
	Stats    lint.Stats `json:"stats"`
}

// JSON writes a versioned JSON document. line and col are 1-based byte
// positions; offsets are byte offsets into the file. Paths are
// slash-separated.
func JSON(w io.Writer, res *lint.Result, rules []*lint.Rule, o Options) error {
	byName := map[string]*lint.Rule{}
	for _, r := range rules {
		byName[r.Name] = r
	}
	name := func(p string) string { return filepath.ToSlash(o.name(p)) }
	out := jsonOut{Version: 1, Tool: "dgdtools " + o.Version, Diagnostics: hole, Rules: map[string]jsonRule{}}
	for _, n := range res.Notes {
		out.Notes = append(out.Notes, jsonNote{n.Kind, n.Message, n.Path})
	}
	for _, e := range res.Errors {
		out.Errors = append(out.Errors, jsonError{name(e.Path), e.Kind.String(), e.Message})
	}
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
		if r := byName[d.Rule]; r != nil {
			out.Rules[d.Rule] = jsonRule{r.Code, r.Group, r.Doc, r.URL(o.Version)}
		}
	}
	return encodeAround(w, out, len(res.Diagnostics), func(i int) any {
		d := &res.Diagnostics[i]
		jd := jsonDiag{
			Rule: d.Rule, Code: d.Code, Severity: d.Severity.String(),
			Path: name(d.Path), Line: d.Line, Col: d.Col, Message: d.Message,
			Range:       jsonRange{d.Line, d.Col, d.EndLine, d.EndCol, d.Start, d.End},
			Fingerprint: d.Fingerprint,
		}
		for _, t := range d.Tags {
			jd.Tags = append(jd.Tags, t.String())
		}
		for _, r := range d.Related {
			jd.Related = append(jd.Related, jsonRelated{name(r.Path), r.Line, r.Col, r.Message})
		}
		for _, f := range d.Fixes {
			jf := jsonFix{Title: f.Title, Applicability: f.Applicability.String()}
			for _, e := range f.Edits {
				jf.Edits = append(jf.Edits, jsonEdit{e.Start, e.End, e.NewText})
			}
			jd.Fixes = append(jd.Fixes, jf)
		}
		return jd
	})
}

// hole stands in a document for the array encodeAround writes there.
var hole = json.RawMessage(`"\u0000array\u0000"`)

// encodeAround writes doc as encoding/json writes it indented by two
// spaces, with an array of n elements, elem(i) each, where doc has hole.
// The elements are encoded one at a time: a whole lib's findings, as a
// document in memory, would be many times their own size.
func encodeAround(w io.Writer, doc any, n int, elem func(i int) any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	at := bytes.Index(buf.Bytes(), hole)
	if at < 0 {
		return fmt.Errorf("report: no place for the array in the document")
	}
	head, tail := slices.Clone(buf.Bytes()[:at]), slices.Clone(buf.Bytes()[at+len(hole):])
	if _, err := w.Write(head); err != nil {
		return err
	}
	if n == 0 {
		_, err := io.WriteString(w, "[]")
		if err == nil {
			_, err = w.Write(tail)
		}
		return err
	}
	// The array's elements are one level deeper than the line it is on.
	line := head[bytes.LastIndexByte(head, '\n')+1:]
	indent := line[:len(line)-len(bytes.TrimLeft(line, " "))]
	prefix := string(indent) + "  "
	enc.SetIndent(prefix, "  ")
	if _, err := io.WriteString(w, "[\n"); err != nil {
		return err
	}
	for i := range n {
		buf.Reset()
		if i > 0 {
			buf.WriteString(",\n")
		}
		buf.WriteString(prefix)
		if err := enc.Encode(elem(i)); err != nil {
			return err
		}
		buf.Truncate(buf.Len() - 1) // Encode's newline
		if _, err := w.Write(buf.Bytes()); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "\n%s]", indent); err != nil {
		return err
	}
	_, err := w.Write(tail)
	return err
}

// ---------------------------------------------------------------- GitHub

// GitHub writes workflow commands that GitHub Actions turns into inline
// annotations on the pull request. A file is named from BaseDir, which
// in a workflow is the checkout.
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
		if _, err := fmt.Fprintf(w, "::%s file=%s,line=%d,col=%d,endLine=%d,endColumn=%d,title=%s::%s\n",
			level, prop(filepath.ToSlash(o.name(d.Path))), d.Line, d.Col, d.EndLine, d.EndCol,
			prop("dgdtools "+d.Code+" "+d.Rule), esc(d.Message)); err != nil {
			return err
		}
	}
	return nil
}

// ----------------------------------------------------------------- SARIF

// srcRoot is the uriBaseId that stands for BaseDir.
const srcRoot = "%SRCROOT%"

type sarifMsg struct {
	Text string `json:"text"`
}

type sarifRegion struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn"`
	EndLine     int `json:"endLine,omitempty"`
	EndColumn   int `json:"endColumn,omitempty"`
	// The byte range is exact where the columns, which count bytes, are
	// not what a consumer that counts UTF-16 units expects.
	ByteOffset *uint32 `json:"byteOffset,omitempty"`
	ByteLength *uint32 `json:"byteLength,omitempty"`
}

type sarifArtifactLocation struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId,omitempty"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           sarifRegion           `json:"region"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
	Message          *sarifMsg             `json:"message,omitempty"`
}

type sarifResult struct {
	RuleID              string            `json:"ruleId"`
	Level               string            `json:"level"`
	Message             sarifMsg          `json:"message"`
	Locations           []sarifLocation   `json:"locations"`
	RelatedLocations    []sarifLocation   `json:"relatedLocations,omitempty"`
	PartialFingerprints map[string]string `json:"partialFingerprints,omitempty"`
}

type sarifRule struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	ShortDescription sarifMsg          `json:"shortDescription"`
	FullDescription  sarifMsg          `json:"fullDescription"`
	HelpURI          string            `json:"helpUri"`
	DefaultConfig    map[string]string `json:"defaultConfiguration"`
	Properties       map[string]any    `json:"properties"`
}

// artifact returns the SARIF location of p: a file under BaseDir as a
// relative URI reference resolved against %SRCROOT%, another file as a
// file: URI, a profile's stub as its own URI.
func (o Options) artifact(p string) sarifArtifactLocation {
	if name, lib, stub := profile.SplitLocation(p); stub {
		return sarifArtifactLocation{URI: (&url.URL{Scheme: profile.Scheme, Host: name, Path: lib}).String()}
	}
	if rel, ok := o.under(p); ok {
		return sarifArtifactLocation{URI: (&url.URL{Path: filepath.ToSlash(rel)}).String(), URIBaseID: srcRoot}
	}
	return sarifArtifactLocation{URI: fileURI(p)}
}

// fileURI returns the file: URI of an absolute path.
func fileURI(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // a Windows drive: file:///C:/...
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
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
// Columns count bytes; regions carry the byte range too.
func SARIF(w io.Writer, res *lint.Result, rules []*lint.Rule, o Options) error {
	var srules []sarifRule
	for _, r := range rules {
		srules = append(srules, sarifRule{
			ID: r.Name, Name: r.Name,
			ShortDescription: sarifMsg{r.Doc}, FullDescription: sarifMsg{r.Explain},
			HelpURI:       r.URL(o.Version),
			DefaultConfig: map[string]string{"level": sarifLevel(r.Severity)},
			Properties:    map[string]any{"code": r.Code, "group": r.Group},
		})
	}
	run := map[string]any{
		"tool": map[string]any{"driver": map[string]any{
			"name": "dgdtools", "version": o.Version,
			"informationUri": "https://github.com/mmcdole/dgdtools",
			"rules":          srules,
		}},
		"results": hole,
	}
	if o.BaseDir != "" {
		run["originalUriBaseIds"] = map[string]any{
			srcRoot: map[string]string{"uri": strings.TrimSuffix(fileURI(o.BaseDir), "/") + "/"},
		}
	}
	doc := map[string]any{
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json",
		"version": "2.1.0",
		"runs":    []any{run},
	}
	return encodeAround(w, doc, len(res.Diagnostics), func(i int) any {
		d := &res.Diagnostics[i]
		start, length := d.Start, d.End-d.Start
		sr := sarifResult{
			RuleID: d.Rule, Level: sarifLevel(d.Severity), Message: sarifMsg{d.Message},
			Locations: []sarifLocation{{PhysicalLocation: sarifPhysicalLocation{
				ArtifactLocation: o.artifact(d.Path),
				Region:           sarifRegion{d.Line, d.Col, d.EndLine, d.EndCol, &start, &length},
			}}},
		}
		if d.Fingerprint != "" {
			sr.PartialFingerprints = map[string]string{"dgdtools/line/v1": d.Fingerprint}
		}
		for _, r := range d.Related {
			sr.RelatedLocations = append(sr.RelatedLocations, sarifLocation{
				PhysicalLocation: sarifPhysicalLocation{
					ArtifactLocation: o.artifact(r.Path),
					Region:           sarifRegion{StartLine: r.Line, StartColumn: r.Col},
				},
				Message: &sarifMsg{r.Message},
			})
		}
		return sr
	})
}

// plural says how many of a thing there are: "1 file", "2 files".
func plural(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return strconv.Itoa(n) + " " + thing + "s"
}
