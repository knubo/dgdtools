// Package lsp is the language server of dgdtools: the Language Server
// Protocol over a stream, as `dgdtools lsp` speaks it on its standard
// input and output.
//
// It is a front end and no more. What it holds is what the protocol
// makes a server hold: the connection, the versions of the documents the
// editor has open, when to lint which of them, and the conversion of
// positions. The texts, the projects and the linters are a
// session.Session's, and every answer is the library's: diagnostics are
// lint.Document's, fixes are diag.Fix, suppression comments are
// lint.Suppress, formatting is format.Source, what a name refers to is
// resolve.At. docs/design/lsp.md has the design.
//
// Notifications that change something (a document, a file on disk, the
// workspace) are applied one at a time, in the order they arrive, each
// as one call on the session. Linting happens beside them: a changed
// document is linted a moment after its last change, a newer change
// stops the lint of an older text, and nothing is published that is not
// of the document's current version and the lib as it is now. Requests
// are answered concurrently.
package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"github.com/sourcegraph/jsonrpc2"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/session"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// Options are what a server is started with: what the process knows that
// the protocol does not say. Like the libraries, the server reads neither
// the environment nor the working directory.
type Options struct {
	// FS is the file system the tools read; nil is the disk.
	FS vfs.FS
	// ProfileDirs and UserConfig are config.DiscoverOptions' fields of
	// the same names: where a profile given by name is looked for
	// (profile.UserDirs), and the user-level configuration
	// (config.UserConfigPath).
	ProfileDirs []string
	UserConfig  string
	// Version is the tool's version, for the client to show.
	Version string
	// Log receives the debug log; nil discards it. With Trace every
	// message of the protocol is logged too, in both directions.
	Log   *slog.Logger
	Trace bool
	// Debounce is how long after its last change a document is linted
	// (default 200 ms), and DependentDebounce how long after a change to
	// something else that it depends on (default 1 s).
	Debounce, DependentDebounce time.Duration
}

// ErrNoShutdown is what Serve returns when the client went away without
// the shutdown request the protocol asks for: the connection closed, or
// exit came first.
var ErrNoShutdown = errors.New("lsp: the client left without shutdown")

// timeout is how long a request of the editor's may take. One that takes
// longer is answered with nothing, which an editor passes over, rather
// than with an error it would show.
const timeout = 5 * time.Second

// Serve speaks the protocol on rwc until the client says exit or the
// stream ends, and closes it. It returns nil after an orderly shutdown
// and ErrNoShutdown otherwise.
func Serve(ctx context.Context, rwc io.ReadWriteCloser, opt Options) error {
	if opt.FS == nil {
		opt.FS = vfs.OS{}
	}
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	if opt.Debounce <= 0 {
		opt.Debounce = 200 * time.Millisecond
	}
	if opt.DependentDebounce <= 0 {
		opt.DependentDebounce = time.Second
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &server{
		opt: opt, log: opt.Log, ctx: ctx,
		docs:     map[string]*document{},
		requests: map[jsonrpc2.ID]context.CancelFunc{},
		scans:    map[*lint.Linter]*scan{},
		said:     map[string]bool{},
		wake:     make(chan struct{}, 1),
		ready:    make(chan struct{}),
	}
	// The connection's own remarks (a message it cannot read) go to the
	// log, never to standard error.
	opts := []jsonrpc2.ConnOpt{jsonrpc2.SetLogger(logf(func(msg string) { s.log.Warn(msg) }))}
	if opt.Trace {
		opts = append(opts, jsonrpc2.LogMessages(logf(func(msg string) { s.log.Debug(msg) })))
	}
	conn := jsonrpc2.NewConn(ctx, jsonrpc2.NewBufferedStream(rwc, jsonrpc2.VSCodeObjectCodec{}), s, opts...)
	s.conn = conn
	close(s.ready)
	s.jobs.Add(1)
	go s.send()

	<-conn.DisconnectNotify()
	cancel()
	s.mu.Lock()
	s.closed = true
	for _, doc := range s.docs {
		s.unschedule(doc)
	}
	orderly := s.state == exited
	s.mu.Unlock()
	s.jobs.Wait()
	if !orderly {
		return ErrNoShutdown
	}
	return nil
}

// logf is a jsonrpc2.Logger.
type logf func(msg string)

func (f logf) Printf(format string, v ...any) { f(fmt.Sprintf(format, v...)) }

// peer is what the server needs of the connection beyond answering what
// it is asked: to tell the client something, and to ask it something.
// The transport (sourcegraph/jsonrpc2) is used in this file only.
type peer interface {
	Notify(ctx context.Context, method string, params any, opts ...jsonrpc2.CallOption) error
	Call(ctx context.Context, method string, params, result any, opts ...jsonrpc2.CallOption) error
	Close() error
}

// The states of a server, in the order the protocol goes through them.
const (
	created = iota
	initialized
	shutdown
	exited
)

type server struct {
	opt Options
	log *slog.Logger
	// conn is the connection, there once ready is closed: a message can
	// arrive before whoever made the connection has said which it is.
	conn  peer
	ready chan struct{}
	// ctx ends with the connection, and everything the server started
	// with it; jobs are the goroutines to wait for then.
	ctx  context.Context
	jobs sync.WaitGroup

	// mu guards everything below. It is never held while something is
	// written to the client or the tools run: a slow client or a slow
	// lint holds up nobody.
	mu    sync.Mutex
	state int
	// closed is set when the connection has ended: nothing is started
	// any more.
	closed bool
	caps   capabilities
	// trace is the level the client set with $/setTrace.
	trace string

	// folders are the workspace folders with what the editor's settings
	// say of each, and session what the tools read through: replaced as
	// a whole when the folders' settings change.
	folders  []folder
	defaults settings
	session  *session.Session
	resolved []config.Folder
	// docs are the open documents, by URI.
	docs map[string]*document
	// requests are the requests being answered, each with what stops it.
	requests map[jsonrpc2.ID]context.CancelFunc
	// scans are the whole-lib scans that are running.
	scans map[*lint.Linter]*scan
	watch watch
	// said are the things the user was told and is not told again until
	// the configuration changes.
	said map[string]bool

	// out are the notifications to send, in order, and wake tells the
	// sender there are some.
	out  []notification
	wake chan struct{}
	// work counts what is scheduled, running or not yet sent; idle are
	// those waiting for it to be nothing.
	work int
	idle []chan struct{}
}

// capabilities are the client's, as far as the server acts on them.
type capabilities struct {
	utf8                bool // positions count bytes
	configuration       bool // workspace/configuration
	watch, relativeGlob bool // dynamic file watchers, and with a base
	documentChanges     bool // edits that name a document's version
	related, tags, href bool // what a diagnostic may carry
	progress            bool // window/workDoneProgress
	links               bool // a definition as LocationLinks
	hoverMarkdown       bool // a hover as Markdown
	// What a signature may carry: its documentation as Markdown, its
	// parameters as offsets in the label, its own active parameter.
	signatureMarkdown, labelOffsets, signatureActive bool
}

// encoding names the position encoding in use.
func (c capabilities) encoding() string {
	if c.utf8 {
		return UTF8
	}
	return UTF16
}

type notification struct {
	method string
	params any
}

// An rpcError is an error of the protocol, answered as it is.
type rpcError = jsonrpc2.Error

var (
	errContentModified = &rpcError{Code: codeContentModified, Message: "the document changed"}
	errCancelled       = &rpcError{Code: codeRequestCancelled, Message: "cancelled"}
)

// Handle takes one message from the client. It is called for each in the
// order they arrive, and the next is not read before it returns.
// Notifications are dealt with here, which keeps their order, and so is
// what initialize and shutdown do to the server's state. Everything a
// request needs beyond that, and every answer, is a goroutine's: this
// one must always be free to read, since the client may be waiting for
// its own message to be read before it reads ours.
func (s *server) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	<-s.ready
	var params json.RawMessage
	if req.Params != nil {
		params = *req.Params
	}
	if req.Notif {
		s.guard(req.Method, func() { s.notification(req.Method, params) })
		return
	}
	s.mu.Lock()
	state := s.state
	if req.Method == "shutdown" && state == initialized {
		s.state = shutdown
	}
	s.mu.Unlock()
	var answer func(ctx context.Context) (any, error)
	switch {
	case req.Method == "initialize":
		result, err := s.guarded(req.Method, func() (any, error) { return s.initialize(params) })
		answer = func(context.Context) (any, error) { return result, err }
	case state == created:
		answer = func(context.Context) (any, error) {
			return nil, &rpcError{Code: codeServerNotInitialized, Message: "initialize first"}
		}
	case state != initialized:
		answer = func(context.Context) (any, error) {
			return nil, &rpcError{Code: codeInvalidRequest, Message: "the server is shut down"}
		}
	case req.Method == "shutdown":
		answer = func(context.Context) (any, error) { return nil, nil }
	default:
		answer = func(ctx context.Context) (any, error) {
			return s.guarded(req.Method, func() (any, error) { return s.request(ctx, req.Method, params) })
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if !s.job() {
		s.mu.Unlock()
		cancel()
		return
	}
	s.requests[req.ID] = cancel
	s.mu.Unlock()
	go func() {
		defer s.jobs.Done()
		start := time.Now()
		result, err := answer(ctx)
		cancelled := ctx.Err() != nil
		s.mu.Lock()
		delete(s.requests, req.ID)
		if s.trace != "" && s.trace != "off" {
			s.notify("$/logTrace", LogTraceParams{Message: fmt.Sprintf("%s: %s", req.Method, time.Since(start).Round(time.Microsecond))})
		}
		s.mu.Unlock()
		cancel()
		if err != nil && cancelled {
			err = errCancelled
		}
		s.log.Debug("request", "method", req.Method, "took", time.Since(start), "err", err)
		var e *rpcError
		switch {
		case err == nil:
			err = conn.Reply(ctx, req.ID, result)
		case errors.As(err, &e):
			err = conn.ReplyWithError(ctx, req.ID, e)
		default:
			err = conn.ReplyWithError(ctx, req.ID, &rpcError{Code: codeInternalError, Message: err.Error()})
		}
		if err != nil && !errors.Is(err, jsonrpc2.ErrClosed) {
			s.log.Warn("reply not sent", "method", req.Method, "err", err)
		}
	}()
}

// guard runs f, which handles the named message. A panic costs that one
// message: it is logged with its stack and the server goes on.
func (s *server) guard(method string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("panic", "method", method, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		}
	}()
	f()
}

// guarded is guard for a request, whose panic is its answer too.
func (s *server) guarded(method string, f func() (any, error)) (result any, err error) {
	err = &rpcError{Code: codeInternalError, Message: "internal error in " + method + " (please report)"}
	s.guard(method, func() { result, err = f() })
	return result, err
}

// request answers a request of the editor's.
func (s *server) request(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method == MethodIdle {
		return nil, s.waitIdle(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var (
		result any
		err    error
	)
	switch method {
	case MethodTextDocumentContent:
		var p TextDocumentContentParams
		if err := unmarshal(params, &p); err != nil {
			return nil, err
		}
		result, err = s.textDocumentContent(p.URI)
	case "textDocument/codeAction":
		var p CodeActionParams
		if err := unmarshal(params, &p); err != nil {
			return nil, err
		}
		result, err = s.codeAction(ctx, &p)
	case "textDocument/definition":
		var p DefinitionParams
		if err := unmarshal(params, &p); err != nil {
			return nil, err
		}
		result, err = s.definition(ctx, &p)
	case "textDocument/hover":
		var p HoverParams
		if err := unmarshal(params, &p); err != nil {
			return nil, err
		}
		result, err = s.hover(ctx, &p)
	case "textDocument/signatureHelp":
		var p SignatureHelpParams
		if err := unmarshal(params, &p); err != nil {
			return nil, err
		}
		result, err = s.signatureHelp(ctx, &p)
	case "textDocument/formatting":
		var p DocumentFormattingParams
		if err := unmarshal(params, &p); err != nil {
			return nil, err
		}
		result, err = s.formatting(p.TextDocument.URI, nil)
	case "textDocument/rangeFormatting":
		var p DocumentRangeFormattingParams
		if err := unmarshal(params, &p); err != nil {
			return nil, err
		}
		result, err = s.formatting(p.TextDocument.URI, &p.Range)
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "no method " + method}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		s.log.Warn("request took too long", "method", method)
		return nil, nil
	}
	return result, err
}

// notification takes a notification of the editor's. Before initialize
// and after shutdown only exit means anything.
func (s *server) notification(method string, params json.RawMessage) {
	if method == "exit" {
		s.mu.Lock()
		if s.state == shutdown {
			s.state = exited
		}
		s.mu.Unlock()
		s.conn.Close()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != initialized {
		return
	}
	read := func(v any) bool {
		if err := unmarshal(params, v); err != nil {
			s.log.Warn("notification not understood", "method", method, "err", err)
			return false
		}
		return true
	}
	switch method {
	case "initialized":
		s.settle()
	case "textDocument/didOpen":
		var p DidOpenTextDocumentParams
		if read(&p) {
			s.didOpen(&p)
		}
	case "textDocument/didChange":
		var p DidChangeTextDocumentParams
		if read(&p) {
			s.didChange(&p)
		}
	case "textDocument/didClose":
		var p DidCloseTextDocumentParams
		if read(&p) {
			s.didClose(&p)
		}
	case "workspace/didChangeWatchedFiles":
		var p DidChangeWatchedFilesParams
		if read(&p) {
			s.didChangeWatchedFiles(&p)
		}
	case "workspace/didChangeWorkspaceFolders":
		var p DidChangeWorkspaceFoldersParams
		if read(&p) {
			s.didChangeWorkspaceFolders(&p)
		}
	case "workspace/didChangeConfiguration":
		var p DidChangeConfigurationParams
		if read(&p) {
			s.didChangeConfiguration(&p)
		}
	case "$/cancelRequest":
		var p CancelParams
		var id jsonrpc2.ID
		if read(&p) && json.Unmarshal(p.ID, &id) == nil {
			if cancel := s.requests[id]; cancel != nil {
				cancel()
			}
		}
	case "$/setTrace":
		var p SetTraceParams
		if read(&p) {
			s.trace = p.Value
		}
	}
	// Anything else (textDocument/didSave, which needs nothing: the
	// buffer is the file while it is open) is passed over.
}

func unmarshal(params json.RawMessage, v any) error {
	if err := json.Unmarshal(params, v); err != nil {
		return &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	return nil
}

// initialize answers the first request: it reads what the client can do,
// settles the position encoding, and says what the server can do.
func (s *server) initialize(params json.RawMessage) (any, error) {
	var p InitializeParams
	if err := unmarshal(params, &p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != created {
		return nil, &rpcError{Code: codeInvalidRequest, Message: "initialize was sent already"}
	}
	c := &p.Capabilities
	s.caps = capabilities{
		configuration:   c.Workspace.Configuration,
		watch:           c.Workspace.DidChangeWatchedFiles.DynamicRegistration,
		relativeGlob:    c.Workspace.DidChangeWatchedFiles.RelativePatternSupport,
		documentChanges: c.Workspace.WorkspaceEdit.DocumentChanges,
		related:         c.TextDocument.PublishDiagnostics.RelatedInformation,
		tags:            c.TextDocument.PublishDiagnostics.TagSupport != nil,
		href:            c.TextDocument.PublishDiagnostics.CodeDescriptionSupport,
		progress:        c.Window.WorkDoneProgress,
		links:           c.TextDocument.Definition.LinkSupport,
		hoverMarkdown:   slices.Contains(c.TextDocument.Hover.ContentFormat, Markdown),
	}
	si := &c.TextDocument.SignatureHelp.SignatureInformation
	s.caps.signatureMarkdown = slices.Contains(si.DocumentationFormat, Markdown)
	s.caps.labelOffsets = si.ParameterInformation.LabelOffsetSupport
	s.caps.signatureActive = si.ActiveParameterSupport
	// A column is a byte offset wherever the client can count that way;
	// UTF-16 is what every client can.
	for _, e := range c.General.PositionEncodings {
		s.caps.utf8 = s.caps.utf8 || e == UTF8
	}
	s.trace = p.Trace

	// What the client says here of the settings holds for every folder
	// until it is asked, and for good when it cannot be asked.
	if len(p.InitializationOptions) > 0 {
		if err := json.Unmarshal(p.InitializationOptions, &s.defaults); err != nil {
			s.log.Warn("initializationOptions not understood", "err", err)
		}
	}
	folders := p.WorkspaceFolders
	if len(folders) == 0 && p.RootURI != "" {
		folders = []WorkspaceFolder{{URI: p.RootURI}}
	}
	if len(folders) == 0 && p.RootPath != "" {
		folders = []WorkspaceFolder{{URI: uriOf(p.RootPath)}}
	}
	for _, f := range folders {
		s.addFolder(f.URI)
	}
	opt := s.discover()
	s.session, s.resolved = session.New(s.opt.FS, opt), opt.Folders
	s.state = initialized

	res := &InitializeResult{ServerInfo: ServerInfo{Name: "dgdtools", Version: s.opt.Version}}
	res.Capabilities.PositionEncoding = s.caps.encoding()
	res.Capabilities.TextDocumentSync = TextDocumentSyncOptions{OpenClose: true, Change: SyncIncremental}
	res.Capabilities.DefinitionProvider = true
	res.Capabilities.HoverProvider = true
	// Asked for when a call is opened and at each argument; asked again
	// when one is closed, which may leave the cursor in another's.
	res.Capabilities.SignatureHelpProvider = &SignatureHelpOptions{
		TriggerCharacters: []string{"(", ","}, RetriggerCharacters: []string{")"}}
	res.Capabilities.DocumentFormattingProvider = true
	res.Capabilities.DocumentRangeFormattingProvider = true
	if c.TextDocument.CodeAction.CodeActionLiteralSupport != nil {
		res.Capabilities.CodeActionProvider = &CodeActionOptions{CodeActionKinds: []string{KindQuickFix, KindFixAll}}
	}
	res.Capabilities.Workspace.WorkspaceFolders.Supported = true
	res.Capabilities.Workspace.WorkspaceFolders.ChangeNotifications = true
	// The stubs of a profile are documents of the server's: a client
	// that knows the request shows them, read-only, when asked to go to
	// one.
	res.Capabilities.Workspace.TextDocumentContent.Schemes = []string{profile.Scheme}
	s.log.Info("initialized", "encoding", s.caps.encoding(), "folders", len(s.folders))
	return res, nil
}

// discover returns the options projects are resolved with: what the
// process knows, and what the editor's settings say of the folders that
// they say anything of.
func (s *server) discover() config.DiscoverOptions {
	opt := config.DiscoverOptions{ProfileDirs: s.opt.ProfileDirs, UserConfig: s.opt.UserConfig}
	for _, f := range s.folders {
		if f.dir != "" && f.set != (settings{}) {
			opt.Folders = append(opt.Folders, config.Folder{Dir: f.dir, Profile: f.set.Profile, LibPath: f.set.LibPath})
		}
	}
	return opt
}

// job counts a goroutine about to be started among those Serve waits
// for; false when the connection has ended and none may be.
func (s *server) job() bool {
	if s.closed {
		return false
	}
	s.jobs.Add(1)
	return true
}

// Work is whatever the server still has to do without being asked again:
// a lint that is scheduled or running, a scan, a question to the client,
// a notification not yet sent. begin and end count it.

func (s *server) begin() { s.work++ }

func (s *server) end() {
	if s.work--; s.work == 0 {
		for _, c := range s.idle {
			close(c)
		}
		s.idle = nil
	}
}

// waitIdle returns when there is no work.
func (s *server) waitIdle(ctx context.Context) error {
	s.mu.Lock()
	if s.work == 0 {
		s.mu.Unlock()
		return nil
	}
	c := make(chan struct{})
	s.idle = append(s.idle, c)
	s.mu.Unlock()
	select {
	case <-c:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// spawn runs f beside everything else, as work. It is how the server
// asks the client something: an answer is read by the goroutine that
// reads every message, which therefore must not wait for one.
func (s *server) spawn(what string, f func()) {
	if !s.job() {
		return
	}
	s.begin()
	go func() {
		defer s.jobs.Done()
		s.guard(what, f)
		s.mu.Lock()
		s.end()
		s.mu.Unlock()
	}()
}

// notify queues a notification to the client. They are sent in the order
// they are queued, by one goroutine, so that what is published last for
// a document is what the client has.
func (s *server) notify(method string, params any) {
	s.begin()
	s.out = append(s.out, notification{method, params})
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// send sends what notify queued, until the connection ends.
func (s *server) send() {
	defer s.jobs.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		}
		for {
			s.mu.Lock()
			if len(s.out) == 0 {
				s.mu.Unlock()
				break
			}
			n := s.out[0]
			s.out = s.out[1:]
			s.mu.Unlock()
			if err := s.conn.Notify(s.ctx, n.method, n.params); err != nil && !errors.Is(err, jsonrpc2.ErrClosed) {
				s.log.Warn("notification not sent", "method", n.method, "err", err)
			}
			s.mu.Lock()
			s.end()
			s.mu.Unlock()
		}
	}
}

// tell shows the user a message, and logs it.
func (s *server) tell(kind int, msg string) {
	s.notify("window/showMessage", ShowMessageParams{Type: kind, Message: msg})
	s.logMessage(kind, msg)
}

// tellOnce is tell for what need not be said twice.
func (s *server) tellOnce(kind int, msg string) {
	if !s.said[msg] {
		s.said[msg] = true
		s.tell(kind, msg)
	}
}

// logMessage writes to the client's log of the server, and to the
// server's own.
func (s *server) logMessage(kind int, msg string) {
	s.notify("window/logMessage", LogMessageParams{Type: kind, Message: msg})
	s.log.Info("message", "type", kind, "message", msg)
}
