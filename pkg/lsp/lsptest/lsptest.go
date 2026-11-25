// Package lsptest is a client for testing the language server: it runs
// pkg/lsp in the test's own process, talks to it over a pipe as an
// editor would, and keeps what an editor keeps (the texts it has open,
// the diagnostics it was last sent, the file watchers it was asked for).
//
// Its positions are computed by itself, naively, from the text, so that
// a test holds the server's conversions to something that is not the
// server's.
package lsptest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/mmcdole/dgdtools/pkg/lsp"
)

// Client is one editor talking to one server.
type Client struct {
	t    testing.TB
	conn *jsonrpc2.Conn
	done chan error // what Serve returned

	// Encoding is the position encoding the server chose, set by
	// Initialize.
	Encoding string
	// Settings answers workspace/configuration: what the section
	// "dgdtools" is for the folder with the given URI; nil is null.
	Settings func(folderURI string) any

	mu       sync.Mutex
	received []Message
	latest   map[string]*lsp.PublishDiagnosticsParams
	watchers map[string]lsp.Registration
	docs     map[string]*Document
}

// Message is a notification or a request the server sent.
type Message struct {
	Method string
	Params json.RawMessage
}

// Document is a document as the client has it open.
type Document struct {
	URI     string
	Version int
	Text    string
}

// Start runs a server with the given options and returns a client
// connected to it. The server is stopped when the test ends.
func Start(t testing.TB, opt lsp.Options) *Client {
	t.Helper()
	near, far := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- lsp.Serve(ctx, far, opt) }()
	t.Cleanup(cancel)
	return Connect(t, near, done)
}

// Connect returns a client of the server at the other end of rwc, which
// somebody else runs. done receives what running it came to: nil for an
// orderly end.
func Connect(t testing.TB, rwc io.ReadWriteCloser, done chan error) *Client {
	t.Helper()
	c := &Client{
		t: t, done: done,
		latest:   map[string]*lsp.PublishDiagnosticsParams{},
		watchers: map[string]lsp.Registration{},
		docs:     map[string]*Document{},
	}
	c.conn = jsonrpc2.NewConn(context.Background(), jsonrpc2.NewBufferedStream(rwc, jsonrpc2.VSCodeObjectCodec{}),
		jsonrpc2.HandlerWithError(c.handle).SuppressErrClosed(), jsonrpc2.SetLogger(quiet{}))
	t.Cleanup(func() {
		c.conn.Close()
		select {
		case <-c.done:
		case <-time.After(10 * time.Second):
			t.Error("the server did not stop")
		}
	})
	return c
}

type quiet struct{}

func (quiet) Printf(string, ...any) {}

// handle takes what the server sends: notifications are kept, and the
// requests a server makes of a client are answered as an editor would.
func (c *Client) handle(_ context.Context, _ *jsonrpc2.Conn, req *jsonrpc2.Request) (any, error) {
	var params json.RawMessage
	if req.Params != nil {
		params = *req.Params
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.received = append(c.received, Message{req.Method, params})
	switch req.Method {
	case "textDocument/publishDiagnostics":
		var p lsp.PublishDiagnosticsParams
		if err := json.Unmarshal(params, &p); err != nil {
			c.t.Errorf("publishDiagnostics: %v", err)
		}
		c.latest[p.URI] = &p
	case "workspace/configuration":
		var p lsp.ConfigurationParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		out := make([]any, len(p.Items))
		for i, item := range p.Items {
			if c.Settings != nil && item.Section == "dgdtools" {
				out[i] = c.Settings(item.ScopeURI)
			}
		}
		return out, nil
	case "client/registerCapability":
		var p lsp.RegistrationParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		for _, r := range p.Registrations {
			c.watchers[r.ID] = r
		}
	case "client/unregisterCapability":
		var p lsp.UnregistrationParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		for _, r := range p.Unregisterations {
			if _, ok := c.watchers[r.ID]; !ok {
				c.t.Errorf("unregistered %s, which was never registered", r.ID)
			}
			delete(c.watchers, r.ID)
		}
	case "window/workDoneProgress/create":
	default:
		if !req.Notif {
			return nil, &jsonrpc2.Error{Code: jsonrpc2.CodeMethodNotFound, Message: "no method " + req.Method}
		}
	}
	return nil, nil
}

// Call makes a request and waits for its answer, which is decoded into
// result when that is not nil.
func (c *Client) Call(method string, params, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.conn.Call(ctx, method, params, result)
}

// CallCancelled makes a request, cancels it at once, and returns the
// error it is answered with.
func (c *Client) CallCancelled(method string, params any) error {
	id := jsonrpc2.ID{Str: "to-be-cancelled", IsString: true}
	call, err := c.conn.DispatchCall(context.Background(), method, params, jsonrpc2.PickID(id))
	if err != nil {
		return err
	}
	c.Notify("$/cancelRequest", map[string]any{"id": id.Str})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return call.Wait(ctx, nil)
}

// Notify sends a notification.
func (c *Client) Notify(method string, params any) {
	c.t.Helper()
	if err := c.conn.Notify(context.Background(), method, params); err != nil {
		c.t.Fatalf("%s: %v", method, err)
	}
}

// Capabilities are a client's that a test chooses; everything else the
// server can use is on.
type Capabilities struct {
	// Encodings are general.positionEncodings; none leaves UTF-16.
	Encodings []string
	// Plain is a client that has nothing beyond the protocol's core: no
	// settings to ask for, no file watchers, no related information,
	// tags or rule links on diagnostics, edits without versions,
	// definitions as locations and not as links, hovers and signatures
	// as plain text, a signature's parameters by their text, an outline
	// without its hierarchy.
	Plain bool
	// Options are the initializationOptions.
	Options any
}

// Initialize sends initialize for the given workspace folders (paths) and
// then initialized, and returns the server's answer.
func (c *Client) Initialize(caps Capabilities, folders ...string) json.RawMessage {
	c.t.Helper()
	rich := !caps.Plain
	params := map[string]any{
		"processId":             nil,
		"initializationOptions": caps.Options,
		"capabilities": map[string]any{
			"general": map[string]any{"positionEncodings": caps.Encodings},
			"workspace": map[string]any{
				"configuration":         rich,
				"didChangeWatchedFiles": map[string]any{"dynamicRegistration": rich, "relativePatternSupport": rich},
				"workspaceEdit":         map[string]any{"documentChanges": rich},
			},
			"textDocument": map[string]any{
				"publishDiagnostics": map[string]any{"relatedInformation": rich, "codeDescriptionSupport": rich},
				"codeAction":         map[string]any{"codeActionLiteralSupport": map[string]any{}},
				"definition":         map[string]any{"linkSupport": rich},
			},
			"window": map[string]any{"workDoneProgress": rich},
		},
	}
	if rich {
		td := params["capabilities"].(map[string]any)["textDocument"].(map[string]any)
		td["publishDiagnostics"].(map[string]any)["tagSupport"] = map[string]any{"valueSet": []int{1, 2}}
		td["hover"] = map[string]any{"contentFormat": []string{lsp.Markdown, lsp.PlainText}}
		td["documentSymbol"] = map[string]any{"hierarchicalDocumentSymbolSupport": true}
		td["signatureHelp"] = map[string]any{"contextSupport": true, "signatureInformation": map[string]any{
			"documentationFormat":    []string{lsp.Markdown, lsp.PlainText},
			"parameterInformation":   map[string]any{"labelOffsetSupport": true},
			"activeParameterSupport": true,
		}}
	}
	var ws []lsp.WorkspaceFolder
	for _, f := range folders {
		ws = append(ws, lsp.WorkspaceFolder{URI: URI(f), Name: filepath.Base(f)})
	}
	params["workspaceFolders"] = ws
	var raw json.RawMessage
	if err := c.Call("initialize", params, &raw); err != nil {
		c.t.Fatalf("initialize: %v", err)
	}
	var res lsp.InitializeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		c.t.Fatalf("initialize: %v", err)
	}
	c.Encoding = res.Capabilities.PositionEncoding
	c.Notify("initialized", struct{}{})
	return raw
}

// Shutdown ends the session as the protocol has it, with shutdown and
// exit, and returns what the server's Serve returned.
func (c *Client) Shutdown() error {
	c.t.Helper()
	if err := c.Call("shutdown", nil, nil); err != nil {
		c.t.Fatalf("shutdown: %v", err)
	}
	return c.Exit()
}

// Exit sends exit and returns what the server's Serve returned.
func (c *Client) Exit() error {
	c.t.Helper()
	c.conn.Notify(context.Background(), "exit", nil)
	select {
	case err := <-c.done:
		c.done <- err
		return err
	case <-time.After(10 * time.Second):
		c.t.Fatal("the server did not exit")
		return nil
	}
}

// Idle waits until the server has nothing left to do. Everything it sent
// until then has been received.
func (c *Client) Idle() {
	c.t.Helper()
	if err := c.Call(lsp.MethodIdle, nil, nil); err != nil {
		c.t.Fatalf("%s: %v", lsp.MethodIdle, err)
	}
}

// URI returns the URI of the file at path.
func URI(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// Doc returns the document the client has open for the file at path.
func (c *Client) Doc(path string) *Document {
	c.t.Helper()
	d := c.docs[URI(path)]
	if d == nil {
		c.t.Fatalf("%s is not open", path)
	}
	return d
}

// Open opens the file at path with the given text.
func (c *Client) Open(path, text string) {
	d := &Document{URI: URI(path), Version: 1, Text: text}
	c.docs[d.URI] = d
	c.Notify("textDocument/didOpen", lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{URI: d.URI, Version: d.Version, Text: text}})
}

// Close closes the document.
func (c *Client) Close(path string) {
	d := c.Doc(path)
	delete(c.docs, d.URI)
	c.Notify("textDocument/didClose", lsp.DidCloseTextDocumentParams{TextDocument: lsp.TextDocumentIdentifier{URI: d.URI}})
}

// Edit is a change to a document: the bytes [Start, End) of its text
// become Text.
type Edit struct {
	Start, End int
	Text       string
}

// Change makes the edits, each in the text the one before it left, and
// sends them as one incremental change.
func (c *Client) Change(path string, edits ...Edit) {
	d := c.Doc(path)
	var changes []lsp.TextDocumentContentChangeEvent
	for _, e := range edits {
		changes = append(changes, lsp.TextDocumentContentChangeEvent{
			Range: &lsp.Range{Start: PositionOf(d.Text, e.Start, c.Encoding), End: PositionOf(d.Text, e.End, c.Encoding)},
			Text:  e.Text,
		})
		d.Text = d.Text[:e.Start] + e.Text + d.Text[e.End:]
	}
	d.Version++
	c.Notify("textDocument/didChange", lsp.DidChangeTextDocumentParams{
		TextDocument: lsp.VersionedTextDocumentIdentifier{URI: d.URI, Version: d.Version}, ContentChanges: changes})
}

// Replace sends the whole new text of the document, as a client without
// incremental changes would.
func (c *Client) Replace(path, text string) {
	d := c.Doc(path)
	d.Text = text
	d.Version++
	c.Notify("textDocument/didChange", lsp.DidChangeTextDocumentParams{
		TextDocument:   lsp.VersionedTextDocumentIdentifier{URI: d.URI, Version: d.Version},
		ContentChanges: []lsp.TextDocumentContentChangeEvent{{Text: text}}})
}

// Apply makes the edits the server returned (of a fix, of formatting) in
// the client's text of the document and tells the server of the change.
// They are positions in the text as it is; none may overlap another.
func (c *Client) Apply(path string, edits []lsp.TextEdit) {
	c.t.Helper()
	d := c.Doc(path)
	type span struct {
		start, end int
		text       string
	}
	var spans []span
	for _, e := range edits {
		spans = append(spans, span{OffsetOf(d.Text, e.Range.Start, c.Encoding), OffsetOf(d.Text, e.Range.End, c.Encoding), e.NewText})
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	text, last := d.Text, len(d.Text)+1
	for i := len(spans) - 1; i >= 0; i-- {
		s := spans[i]
		if s.end < s.start || s.end > last {
			c.t.Fatalf("edits overlap or are out of order: %+v", edits)
		}
		text, last = text[:s.start]+s.text+text[s.end:], s.start
	}
	c.Replace(path, text)
}

// WatchedFiles tells the server that the files or directories at the
// given paths changed on disk; kind is 1 created, 2 changed, 3 deleted.
func (c *Client) WatchedFiles(kind int, paths ...string) {
	var p lsp.DidChangeWatchedFilesParams
	for _, path := range paths {
		p.Changes = append(p.Changes, lsp.FileEvent{URI: URI(path), Type: kind})
	}
	c.Notify("workspace/didChangeWatchedFiles", p)
}

// Diagnostics waits until the server is idle and returns what it last
// published for the file at path: nil when it never published anything.
func (c *Client) Diagnostics(path string) *lsp.PublishDiagnosticsParams {
	c.t.Helper()
	c.Idle()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latest[URI(path)]
}

// Received returns the messages with the given method the server has
// sent so far, in order.
func (c *Client) Received(method string) []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Message
	for _, m := range c.received {
		if m.Method == method {
			out = append(out, m)
		}
	}
	return out
}

// Watchers returns what the server has asked the client to watch: the
// glob patterns of its registrations in force, each a string or a
// "base-uri pattern" pair.
func (c *Client) Watchers() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, r := range c.watchers {
		data, _ := json.Marshal(r.RegisterOptions)
		var opt struct {
			Watchers []struct {
				GlobPattern json.RawMessage `json:"globPattern"`
			} `json:"watchers"`
		}
		json.Unmarshal(data, &opt)
		for _, w := range opt.Watchers {
			var s string
			var rel lsp.RelativePattern
			if json.Unmarshal(w.GlobPattern, &s) != nil && json.Unmarshal(w.GlobPattern, &rel) == nil {
				s = rel.BaseURI + " " + rel.Pattern
			}
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// IsCode reports whether err is the protocol's error with the code.
func IsCode(err error, code int64) bool {
	var e *jsonrpc2.Error
	return errors.As(err, &e) && e.Code == code
}

// The client's own arithmetic of positions: lines end at "\n", "\r\n" or
// a "\r" alone, and a column counts bytes ("utf-8") or UTF-16 code units,
// where a byte that is no UTF-8 is one unit.

// PositionOf returns the position of the byte offset off in text.
func PositionOf(text string, off int, encoding string) lsp.Position {
	var p lsp.Position
	for i := 0; i < off; {
		switch r, size := utf8.DecodeRuneInString(text[i:]); {
		case r == '\r' && i+1 < len(text) && text[i+1] == '\n':
			if i+1 == off {
				return p // inside the line break
			}
			p, i = lsp.Position{Line: p.Line + 1}, i+2
		case r == '\n' || r == '\r':
			p, i = lsp.Position{Line: p.Line + 1}, i+1
		case encoding == lsp.UTF8:
			p.Character += size
			i += size
		case r >= 0x10000:
			p.Character += 2
			i += size
		default:
			p.Character++
			i += size
		}
	}
	return p
}

// OffsetOf returns the byte offset of p in text.
func OffsetOf(text string, p lsp.Position, encoding string) int {
	i := 0
	for line := 0; line < p.Line; {
		if i == len(text) {
			return i
		}
		switch {
		case text[i] == '\r' && i+1 < len(text) && text[i+1] == '\n':
			i, line = i+2, line+1
		case text[i] == '\n' || text[i] == '\r':
			i, line = i+1, line+1
		default:
			i++
		}
	}
	for col := 0; col < p.Character && i < len(text) && text[i] != '\n' && text[i] != '\r'; {
		r, size := utf8.DecodeRuneInString(text[i:])
		switch {
		case encoding == lsp.UTF8:
			col += size
		case r >= 0x10000:
			col += 2
		default:
			col++
		}
		if col > p.Character {
			break // inside a character: its start
		}
		i += size
	}
	return i
}
