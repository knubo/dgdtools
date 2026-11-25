package lsp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/index"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/rename"
)

// willRenameFiles answers workspace/willRenameFiles: before the editor
// renames or moves files and folders, the edits that keep the inherit
// statements, #includes and object paths of the lib naming them
// (rename.Files), as a WorkspaceEdit of the files as they are now. The
// places that may name a moved file and are not rewritten (a path made
// by a macro), the warnings, or why nothing could be planned are shown
// to the user; the rename itself goes ahead whatever is answered, so a
// refusal is null and a message, not an error.
func (s *server) willRenameFiles(ctx context.Context, p *RenameFilesParams) (*WorkspaceEdit, error) {
	s.mu.Lock()
	sess := s.session
	s.mu.Unlock()
	// The moves of each lib.
	var libs []*lint.Linter
	moves := map[*lint.Linter][]rename.Move{}
	for _, f := range p.Files {
		from, to := pathOf(f.OldURI), pathOf(f.NewURI)
		if from == "" || to == "" {
			continue
		}
		l, err := sess.Linter(from)
		if err != nil || l == nil || !l.Project().HasLib() {
			continue
		}
		if _, ok := moves[l]; !ok {
			libs = append(libs, l)
		}
		moves[l] = append(moves[l], rename.Move{From: from, To: to})
	}
	byPath := map[string][]edit{}
	var snaps []*index.Snapshot
	var msgs []string
	for _, l := range libs {
		snap := l.Index().Snapshot()
		if !snap.Scanned() {
			scanning, cancel := context.WithTimeout(ctx, scanBudget)
			snap.Scan(scanning)
			cancel()
		}
		plan, err := rename.Files(ctx, snap, moves[l])
		var no *rename.Refusal
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case errors.As(err, &no):
			msgs = append(msgs, "the inherit statements and #includes that name the moved files are not changed: "+no.Reason)
			continue
		case err != nil:
			return nil, err
		case plan == nil:
			continue
		}
		snaps = append(snaps, snap)
		for _, e := range plan.Edits {
			byPath[e.Path] = append(byPath[e.Path], edit{Edit: e})
		}
		name := func(p string) string { return cmp.Or(snap.LibFile(p), p) }
		if msg := filesMessage(plan, name); msg != "" {
			msgs = append(msgs, msg)
		}
	}
	var result *WorkspaceEdit
	if len(byPath) > 0 {
		var err error
		result, err = s.workspaceEdit(byPath, nil, func(path string) ([]byte, error) {
			return snaps[0].ReadFile(path) // every snapshot reads the session's files
		})
		if err != nil {
			return nil, err
		}
		for _, snap := range snaps {
			if !snap.Current() {
				return nil, errContentModified
			}
		}
	}
	if len(msgs) > 0 {
		s.mu.Lock()
		s.tell(MessageWarning, strings.Join(msgs, "; "))
		s.mu.Unlock()
	}
	return result, nil
}

// filesMessage is what the user is told of a plan of moved files beside
// its edits: the places that may name a moved file and were not
// rewritten, and the warnings; "" when there is nothing to tell. name
// says how a file is named.
func filesMessage(plan *rename.Plan, name func(string) string) string {
	var parts []string
	n := plan.Count(rename.Possible)
	if n > 0 {
		var at []string
		for _, o := range plan.Occurrences {
			if o.Class == rename.Possible && len(at) < 5 {
				at = append(at, fmt.Sprintf("%s:%d (%s)", name(o.Path), o.Line, o.Reason))
			}
		}
		more := ""
		if n > len(at) {
			more = fmt.Sprintf(" and %d more", n-len(at))
		}
		were := "were"
		if n == 1 {
			were = "was"
		}
		parts = append(parts, fmt.Sprintf("%s that may name a moved file %s not changed: %s%s", count(n, "place"), were, strings.Join(at, ", "), more))
	}
	parts = append(parts, plan.Warnings...)
	if len(parts) == 0 {
		return ""
	}
	return "move " + plan.Symbol.Name + " to " + plan.New + ": " + strings.Join(parts, "; ")
}
