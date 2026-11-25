package lsp

import (
	"context"
	"errors"
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
// (lint.Document.Rename), all in the document, for the version they were
// made from when the client reads that (documentChanges); or an error
// that says why the name cannot be renamed, or not to the new name. What
// the plan warns of (a call that names a private function as a string,
// which DGD does not make) is shown to the user.
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
	plan, err := d.Rename(ctx, off, p.NewName)
	if err != nil {
		return nil, renameError(ctx, err)
	}
	edits := make([]TextEdit, 0, len(plan.Edits))
	for _, e := range plan.Edits {
		if e.Path != d.Path {
			// Step 10 renames within the document; a plan across files
			// is the next step's.
			return nil, &rpcError{Code: codeRequestFailed, Message: "the rename reaches other files: " + rename.Later}
		}
		edits = append(edits, TextEdit{Range: m.Range(e.Start, e.End), NewText: e.NewText})
	}
	if len(plan.Warnings) > 0 {
		s.mu.Lock()
		s.tell(MessageWarning, "rename "+plan.Symbol.Name+" to "+plan.New+": "+strings.Join(plan.Warnings, "; "))
		s.mu.Unlock()
	}
	if s.caps.documentChanges {
		return &WorkspaceEdit{DocumentChanges: []TextDocumentEdit{{
			TextDocument: VersionedTextDocumentIdentifier{URI: uri, Version: version}, Edits: edits}}}, nil
	}
	return &WorkspaceEdit{Changes: map[string][]TextEdit{uri: edits}}, nil
}

// renameError is the protocol's error for a rename's: a refusal as a
// request that failed, with its reason for the user to read.
func renameError(ctx context.Context, err error) error {
	var no *rename.Refusal
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
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
