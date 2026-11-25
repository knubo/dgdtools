package lsp

import (
	"context"
	"errors"
	"slices"
	"strconv"

	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/resolve"
)

// TokenLegend is the legend of the server's semantic tokens: the types
// and modifiers of pkg/resolve, by the protocol's names where it has
// one (all the types; declaration, definition, static, defaultLibrary)
// and dgdtools' own for the rest (inherited, local, macro).
var TokenLegend = SemanticTokensLegend{TokenTypes: resolve.TokenTypes, TokenModifiers: resolve.TokenModNames}

// sentTokens are the tokens of a document as last sent in answer to a
// request for all of them, which a request for a delta names.
type sentTokens struct {
	id      string
	version int
	data    []uint32
}

// EncodeTokens writes semantic tokens as the protocol has them: five
// integers each, the line relative to the token before, the column
// (relative to the token before on the same line), the length, the
// type and the modifiers, positions in the mapper's encoding.
func EncodeTokens(m *PositionMapper, toks []resolve.SemanticToken) []uint32 {
	data := make([]uint32, 0, 5*len(toks))
	line, col := 0, 0
	for _, tk := range toks {
		start, end := m.Position(tk.Start), m.Position(tk.End)
		if end.Line != start.Line {
			continue // never: a token is on one line
		}
		dl, dc := start.Line-line, start.Character
		if dl == 0 {
			dc -= col
		}
		data = append(data, uint32(dl), uint32(dc), uint32(end.Character-start.Character), uint32(tk.Type), uint32(tk.Mods))
		line, col = start.Line, start.Character
	}
	return data
}

// tokenEdits returns the edit that makes the tokens old the tokens new:
// what lies between the tokens both begin with and those both end with,
// a token being five integers. None when they are the same.
func tokenEdits(old, new []uint32) []SemanticTokensEdit {
	prefix := 0
	for prefix < len(old) && prefix < len(new) && old[prefix] == new[prefix] {
		prefix++
	}
	prefix -= prefix % 5
	suffix := 0
	for suffix < len(old)-prefix && suffix < len(new)-prefix && old[len(old)-1-suffix] == new[len(new)-1-suffix] {
		suffix++
	}
	suffix -= suffix % 5
	if prefix == len(old) && prefix == len(new) {
		return []SemanticTokensEdit{}
	}
	return []SemanticTokensEdit{{Start: prefix, DeleteCount: len(old) - prefix - suffix,
		Data: slices.Clone(new[prefix : len(new)-suffix])}}
}

// tokensOf returns the document at uri as last linted, its semantic
// tokens encoded for the client and its version.
func (s *server) tokensOf(ctx context.Context, uri string) (*lint.Document, []uint32, int, error) {
	d, version, _, err := s.linted(ctx, uri)
	if d == nil || err != nil {
		var modified *rpcError
		if err != nil && !errors.As(err, &modified) {
			s.log.Warn("no semantic tokens", "uri", uri, "err", err)
			err = nil
		}
		return nil, nil, 0, err
	}
	return d, EncodeTokens(NewPositionMapper(d.File.Src, s.caps.encoding()), d.Tokens()), version, nil
}

// semanticTokens answers textDocument/semanticTokens/full: the tokens of
// the whole document (lint.Document.Tokens), which are kept with an id
// for a request for a delta to name.
func (s *server) semanticTokens(ctx context.Context, p *SemanticTokensParams) (any, error) {
	_, data, version, err := s.tokensOf(ctx, p.TextDocument.URI)
	if data == nil || err != nil {
		return nil, err
	}
	return &SemanticTokens{ResultID: s.keepTokens(p.TextDocument.URI, version, data), Data: data}, nil
}

// semanticTokensDelta answers textDocument/semanticTokens/full/delta: how
// the tokens of the document differ from those of the answer named, or
// all of them when that is not the last one sent.
func (s *server) semanticTokensDelta(ctx context.Context, p *SemanticTokensDeltaParams) (any, error) {
	uri := p.TextDocument.URI
	_, data, version, err := s.tokensOf(ctx, uri)
	if data == nil || err != nil {
		return nil, err
	}
	s.mu.Lock()
	var old *sentTokens
	if doc := s.docs[uri]; doc != nil && doc.tokens != nil && doc.tokens.id == p.PreviousResultID {
		old = doc.tokens
	}
	s.mu.Unlock()
	id := s.keepTokens(uri, version, data)
	if old == nil {
		return &SemanticTokens{ResultID: id, Data: data}, nil
	}
	return &SemanticTokensDelta{ResultID: id, Edits: tokenEdits(old.data, data)}, nil
}

// semanticTokensRange answers textDocument/semanticTokens/range: the
// tokens that begin in the range, the first one's line counted from the
// top. Nothing is kept: a delta is of all the tokens.
func (s *server) semanticTokensRange(ctx context.Context, p *SemanticTokensRangeParams) (any, error) {
	d, err := s.asked(ctx, p.TextDocument.URI, "semantic tokens")
	if d == nil || err != nil {
		return nil, err
	}
	m := NewPositionMapper(d.File.Src, s.caps.encoding())
	start, end := m.Offset(p.Range.Start), m.Offset(p.Range.End)
	toks := d.Tokens()
	i, _ := slices.BinarySearchFunc(toks, start, func(tk resolve.SemanticToken, off uint32) int { return int(tk.Start) - int(off) })
	j := i
	for j < len(toks) && toks[j].Start < end {
		j++
	}
	return &SemanticTokens{Data: EncodeTokens(m, toks[i:j])}, nil
}

// keepTokens keeps the tokens sent for the document at uri, of the given
// version, and returns their id.
func (s *server) keepTokens(uri string, version int, data []uint32) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resultID++
	id := strconv.FormatUint(s.resultID, 10)
	if doc := s.docs[uri]; doc != nil {
		doc.tokens = &sentTokens{id: id, version: version, data: data}
	}
	return id
}

// tokensChanged is called, the server's lock held, with a document just
// linted again in the version the client has its tokens of: the client
// is asked to ask for them again (workspace/semanticTokens/refresh) when
// they changed, which a change to another file can do (a header's
// macros, a parent's functions). The client asks after its own changes
// by itself.
func (s *server) tokensChanged(doc *document, version int, data []uint32) {
	if !s.caps.tokensRefresh || doc.tokens == nil || doc.tokens.version != version || slices.Equal(doc.tokens.data, data) {
		return
	}
	if s.refreshing {
		s.refreshAgain = true
		return
	}
	s.refreshing = true
	s.spawn("workspace/semanticTokens/refresh", func() {
		for {
			if err := s.conn.Call(s.ctx, "workspace/semanticTokens/refresh", nil, nil); err != nil {
				s.log.Debug("semantic tokens refresh", "err", err)
			}
			s.mu.Lock()
			again := s.refreshAgain
			s.refreshAgain, s.refreshing = false, again
			s.mu.Unlock()
			if !again {
				return
			}
		}
	})
}
