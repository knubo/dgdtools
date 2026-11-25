package lsp

import (
	"net/url"
	"slices"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/profile"
)

// MethodTextDocumentContent is the request by which a client gets the
// text of a document that is the server's and no file's (LSP 3.18). The
// server has one kind: the stubs of a profile, which a related location
// names by a URI (profile.Location) when a declaration comes from one.
const MethodTextDocumentContent = "workspace/textDocumentContent"

// textDocumentContent answers workspace/textDocumentContent with the
// text of the stub at uri: that of the profile so named which a project
// in use has, a project of an open document before one of a folder's.
// The text is the profile's own, which is what positions in it count.
func (s *server) textDocumentContent(uri string) (*TextDocumentContentResult, error) {
	name, file, ok := stubOf(uri)
	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: "no document of the server's: " + uri}
	}
	s.mu.Lock()
	sess := s.session
	var docs, folders []string
	for _, doc := range s.docs {
		if doc.path != "" {
			docs = append(docs, doc.path)
		}
	}
	for _, f := range s.folders {
		if f.dir != "" {
			folders = append(folders, f.dir)
		}
	}
	s.mu.Unlock()
	slices.Sort(docs)

	// An editor may give the name back in lower case, as the host of a
	// URI: the profile of exactly that name first, then one that
	// differs in case only.
	var p *profile.Profile
	for _, path := range append(docs, folders...) {
		prj, err := sess.Project(path)
		if err != nil || prj.Profile == nil {
			continue
		}
		if prj.Profile.Name == name {
			p = prj.Profile
			break
		}
		if p == nil && strings.EqualFold(prj.Profile.Name, name) {
			p = prj.Profile
		}
	}
	if p == nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "no profile " + name + " is in use"}
	}
	text, ok := p.Files[file]
	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: "profile " + p.Name + " has no " + file}
	}
	return &TextDocumentContentResult{Text: text}, nil
}

// stubOf splits the URI of a stub into the profile's name and the lib
// file, as profile.SplitLocation splits the location it was made from.
func stubOf(uri string) (name, file string, ok bool) {
	rest, ok := strings.CutPrefix(uri, profile.Scheme+"://")
	if !ok {
		return "", "", false
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	host, path, _ := strings.Cut(rest, "/")
	name, err := url.PathUnescape(host)
	if err != nil || name == "" {
		return "", "", false
	}
	if file, err = url.PathUnescape("/" + path); err != nil {
		return "", "", false
	}
	return name, file, true
}

// stubURI returns the URI of a stub: its location, with what a URI
// cannot have as it is escaped.
func stubURI(name, file string) string {
	return profile.Scheme + "://" + url.PathEscape(name) + (&url.URL{Path: file}).EscapedPath()
}
