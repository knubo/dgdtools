package lsp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/rename"
	"github.com/mmcdole/dgdtools/pkg/resolve"
)

// prepareRename answers textDocument/prepareRename: the name at the
// position and its range, which the client selects for the new name, or
// an error that says why it cannot be renamed (rename.Prepare, through
// lint.Document.PrepareRename). Nothing named there is null.
func (s *server) prepareRename(ctx context.Context, p *PrepareRenameParams) (any, error) {
	d, err := s.asked(ctx, p.TextDocument.URI, "prepareRename")
	if d == nil || err != nil {
		return nil, err
	}
	m := NewPositionMapper(d.File.Src, s.caps.encoding())
	off := m.Offset(p.Position)
	scanFor(ctx, d, off)
	t, err := d.PrepareRename(ctx, off)
	if err != nil {
		return nil, renameError(ctx, err)
	}
	if t == nil {
		return nil, nil
	}
	return &PrepareRenameResult{Range: m.Range(t.Start, t.End), Placeholder: t.Name}, nil
}

// rename answers textDocument/rename: the edits of rename.Rename's plan
// (lint.Document.Rename), in the document and every other file that
// names the symbol, each file's for the version of it they were made from
// (a file the client has not open: null) when the client reads that
// (documentChanges); or an error that says why the name cannot be
// renamed, or not to the new name.
//
// The certain edits are made. The possible ones (a call on an object
// nobody can tell, a name in a macro's body, code that does not parse)
// go to a client that can confirm edits (changeAnnotationSupport) under
// annotations that need confirmation, one for each reason, so that its
// preview shows them unticked; another client gets the certain edits
// only, and is told so. What the plan warns of, the possible occurrences
// left and the calls whose function's name is made when they run are
// shown to the user (window/showMessage).
func (s *server) rename(ctx context.Context, p *RenameParams) (any, error) {
	uri := p.TextDocument.URI
	d, version, _, err := s.linted(ctx, uri)
	if d == nil || err != nil {
		var modified *rpcError
		if err != nil && !errors.As(err, &modified) {
			s.log.Warn("no rename", "uri", uri, "err", err)
			err = nil
		}
		return nil, err
	}
	m := NewPositionMapper(d.File.Src, s.caps.encoding())
	off := m.Offset(p.Position)
	scanFor(ctx, d, off)
	plan, err := d.Rename(ctx, off, p.NewName, false)
	if err != nil {
		return nil, renameError(ctx, err)
	}
	annotate := s.caps.annotations && len(plan.PossibleEdits) > 0
	type edit struct {
		rename.Edit
		annotation string
	}
	byPath := map[string][]edit{}
	for _, e := range plan.Edits {
		byPath[e.Path] = append(byPath[e.Path], edit{Edit: e})
	}
	var annotations map[string]ChangeAnnotation
	if annotate {
		annotations = map[string]ChangeAnnotation{"certain": {Label: "Certain: the renamed " + what(plan.Symbol.Kind)}}
		for k := range byPath {
			for i := range byPath[k] {
				byPath[k][i].annotation = "certain"
			}
		}
		ids := map[string]string{}
		reasons := map[[2]any]string{}
		for _, o := range plan.Occurrences {
			if o.Class == rename.Possible {
				reasons[[2]any{o.Path, o.Start}] = o.Reason
			}
		}
		for _, e := range plan.PossibleEdits {
			reason := reasons[[2]any{e.Path, e.Start}]
			id, ok := ids[reason]
			if !ok {
				id = fmt.Sprintf("possible-%d", len(ids)+1)
				ids[reason] = id
				annotations[id] = ChangeAnnotation{Label: "Possible: " + reason, NeedsConfirmation: true,
					Description: "may be the renamed " + what(plan.Symbol.Kind) + ", and nobody can tell"}
			}
			byPath[e.Path] = append(byPath[e.Path], edit{Edit: e, annotation: id})
		}
	}
	// The texts the edits were made in, which are the snapshot's: an open
	// document's buffer, else the disk's.
	paths := slices.Sorted(maps.Keys(byPath))
	result := &WorkspaceEdit{ChangeAnnotations: annotations}
	if !s.caps.documentChanges {
		result.Changes = map[string][]TextEdit{}
	}
	s.mu.Lock()
	versions := map[string]int{}
	for _, path := range append(paths, d.Path) {
		if doc := s.docs[uriOf(path)]; doc != nil && doc.path == path {
			versions[path] = doc.version
		}
	}
	s.mu.Unlock()
	if v, open := versions[d.Path]; !open || v != version {
		return nil, errContentModified
	}
	for _, path := range paths {
		src := d.File.Src
		if path != d.Path {
			if src, err = d.Snapshot.ReadFile(path); err != nil {
				return nil, &rpcError{Code: codeRequestFailed, Message: "the rename reaches " + path + ", which cannot be read: " + err.Error()}
			}
		}
		pm := NewPositionMapper(src, s.caps.encoding())
		es := byPath[path]
		slices.SortFunc(es, func(a, b edit) int { return cmp.Compare(a.Start, b.Start) })
		out := make([]TextEdit, 0, len(es))
		for _, e := range es {
			out = append(out, TextEdit{Range: pm.Range(e.Start, e.End), NewText: e.NewText, AnnotationID: e.annotation})
		}
		u := uriOf(path)
		if !s.caps.documentChanges {
			result.Changes[u] = out
			continue
		}
		td := OptionalVersionedTextDocumentIdentifier{URI: u}
		if v, open := versions[path]; open {
			td.Version = &v
		}
		result.DocumentChanges = append(result.DocumentChanges, TextDocumentEdit{TextDocument: td, Edits: out})
	}
	if !d.Snapshot.Current() {
		return nil, errContentModified
	}
	name := func(p string) string { return cmp.Or(d.Snapshot.LibFile(p), p) }
	if msg := renameMessage(plan, annotate, name); msg != "" {
		s.mu.Lock()
		s.tell(MessageWarning, msg)
		s.mu.Unlock()
	}
	return result, nil
}

// renameMessage is what the user is told of a plan beside its edits:
// the possible occurrences, the warnings, the calls whose function's
// name is made when they run; "" when there is nothing to tell. name
// says how a file is named.
func renameMessage(plan *rename.Plan, annotated bool, name func(string) string) string {
	var parts []string
	if n := len(plan.PossibleEdits); n > 0 {
		if annotated {
			they := "they are"
			if n == 1 {
				they = "it is"
			}
			parts = append(parts, fmt.Sprintf("%s may be the renamed %s and nobody can tell: %s in the preview, not ticked", count(n, "other place"), what(plan.Symbol.Kind), they))
		} else {
			var at []string
			for _, o := range plan.Occurrences {
				if o.Class == rename.Possible && len(at) < 5 {
					at = append(at, fmt.Sprintf("%s:%d", name(o.Path), o.Line))
				}
			}
			more := ""
			if n > len(at) {
				more = fmt.Sprintf(" and %d more", n-len(at))
			}
			were, them := "were", "them"
			if n == 1 {
				were, them = "was", "it"
			}
			parts = append(parts, fmt.Sprintf("%s that may be the renamed %s %s not renamed (%s%s): `dgdtools rename --include-possible` renames %s too",
				count(n, "place"), what(plan.Symbol.Kind), were, strings.Join(at, ", "), more, them))
		}
	}
	parts = append(parts, plan.Warnings...)
	matches, others := 0, 0
	var at []string
	for _, dy := range plan.Dynamic {
		if dy.Matches {
			matches++
			if len(at) < 5 {
				at = append(at, fmt.Sprintf("%s:%d (%s)", name(dy.Path), dy.Line, dy.Pattern))
			}
		} else {
			others++
		}
	}
	if matches > 0 {
		parts = append(parts, fmt.Sprintf("%s make the name of the function they call when they run, with a part the old name has, and may call it: %s",
			count(matches, "call"), strings.Join(at, ", ")))
	}
	if len(parts) == 0 {
		return ""
	}
	if others > 0 {
		parts = append(parts, fmt.Sprintf("%s in the mudlib make the name of the function they call when they run, and cannot be searched", count(others, "other call")))
	}
	return "rename " + plan.Symbol.Name + " to " + plan.New + ": " + strings.Join(parts, "; ")
}

// count says how many of a thing there are, in words.
func count(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return fmt.Sprintf("%d %ss", n, thing)
}

// what names a kind of symbol in a message.
func what(k resolve.Kind) string {
	switch k {
	case resolve.Variable:
		return "global variable"
	case resolve.Local:
		return "local variable"
	case resolve.Label:
		return "inherit label"
	}
	return k.String()
}

// renameError is the protocol's error for a rename's: a refusal as a
// request that failed, with its reason for the user to read.
func renameError(ctx context.Context, err error) error {
	var no *rename.Refusal
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.As(err, &no) && no.Forcible:
		return &rpcError{Code: codeRequestFailed, Message: no.Reason + " (`dgdtools rename --force` renames it all the same)"}
	case errors.As(err, &no):
		return &rpcError{Code: codeRequestFailed, Message: no.Reason}
	}
	return err
}

// scanFor makes the whole-lib scan for a question about the place at off
// of the document that needs it, when nothing has made it yet, for as
// long as scanBudget allows: what a local, a parameter or a label is
// named, the document alone says.
func scanFor(ctx context.Context, d *lint.Document, off uint32) {
	ref := d.Resolve(off)
	if ref == nil {
		return
	}
	switch ref.Kind {
	case resolve.Local, resolve.Parameter, resolve.Label:
		return
	}
	if snap := d.Snapshot; !snap.Scanned() {
		scanning, cancel := context.WithTimeout(ctx, scanBudget)
		snap.Scan(scanning)
		cancel()
	}
}
