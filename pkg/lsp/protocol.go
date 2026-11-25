package lsp

import "encoding/json"

// The part of the Language Server Protocol (3.17, and one request of
// 3.18) the server speaks, written out by hand: a few dozen types, of a
// specification of several hundred. Fields the server neither reads nor sets are left out.

// Position is a place in a document: a 0-based line and an offset in it,
// counted in the unit the two sides agreed on (PositionMapper).
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is the text from Start to End, the end not included.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type TextDocumentItem struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
	Text    string `json:"text"`
}

// The position encodings of general.positionEncodings.
const (
	UTF8  = "utf-8"
	UTF16 = "utf-16"
)

type InitializeParams struct {
	RootURI               string            `json:"rootUri"`
	RootPath              string            `json:"rootPath"`
	WorkspaceFolders      []WorkspaceFolder `json:"workspaceFolders"`
	InitializationOptions json.RawMessage   `json:"initializationOptions"`
	Trace                 string            `json:"trace"`
	Capabilities          struct {
		General struct {
			PositionEncodings []string `json:"positionEncodings"`
		} `json:"general"`
		Workspace struct {
			Configuration         bool `json:"configuration"`
			DidChangeWatchedFiles struct {
				DynamicRegistration    bool `json:"dynamicRegistration"`
				RelativePatternSupport bool `json:"relativePatternSupport"`
			} `json:"didChangeWatchedFiles"`
			WorkspaceEdit struct {
				DocumentChanges bool `json:"documentChanges"`
			} `json:"workspaceEdit"`
		} `json:"workspace"`
		TextDocument struct {
			PublishDiagnostics struct {
				RelatedInformation     bool      `json:"relatedInformation"`
				TagSupport             *struct{} `json:"tagSupport"`
				CodeDescriptionSupport bool      `json:"codeDescriptionSupport"`
			} `json:"publishDiagnostics"`
			CodeAction struct {
				CodeActionLiteralSupport *struct{} `json:"codeActionLiteralSupport"`
			} `json:"codeAction"`
			Definition struct {
				LinkSupport bool `json:"linkSupport"`
			} `json:"definition"`
			Hover struct {
				ContentFormat []string `json:"contentFormat"`
			} `json:"hover"`
			SignatureHelp struct {
				SignatureInformation struct {
					DocumentationFormat  []string `json:"documentationFormat"`
					ParameterInformation struct {
						LabelOffsetSupport bool `json:"labelOffsetSupport"`
					} `json:"parameterInformation"`
					ActiveParameterSupport bool `json:"activeParameterSupport"`
				} `json:"signatureInformation"`
			} `json:"signatureHelp"`
		} `json:"textDocument"`
		Window struct {
			WorkDoneProgress bool `json:"workDoneProgress"`
		} `json:"window"`
	} `json:"capabilities"`
}

type WorkspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   ServerInfo         `json:"serverInfo"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type ServerCapabilities struct {
	PositionEncoding                string                  `json:"positionEncoding"`
	TextDocumentSync                TextDocumentSyncOptions `json:"textDocumentSync"`
	CodeActionProvider              *CodeActionOptions      `json:"codeActionProvider,omitempty"`
	DefinitionProvider              bool                    `json:"definitionProvider"`
	HoverProvider                   bool                    `json:"hoverProvider"`
	SignatureHelpProvider           *SignatureHelpOptions   `json:"signatureHelpProvider,omitempty"`
	DocumentFormattingProvider      bool                    `json:"documentFormattingProvider"`
	DocumentRangeFormattingProvider bool                    `json:"documentRangeFormattingProvider"`
	Workspace                       struct {
		WorkspaceFolders struct {
			Supported           bool `json:"supported"`
			ChangeNotifications bool `json:"changeNotifications"`
		} `json:"workspaceFolders"`
		TextDocumentContent TextDocumentContentOptions `json:"textDocumentContent"`
	} `json:"workspace"`
}

// TextDocumentContentOptions names the URI schemes of the documents
// whose text the server gives (LSP 3.18).
type TextDocumentContentOptions struct {
	Schemes []string `json:"schemes"`
}

type TextDocumentContentParams struct {
	URI string `json:"uri"`
}

type TextDocumentContentResult struct {
	Text string `json:"text"`
}

// SyncIncremental is TextDocumentSyncKind.Incremental: a change is sent
// as the ranges that changed.
const SyncIncremental = 2

type TextDocumentSyncOptions struct {
	OpenClose bool `json:"openClose"`
	Change    int  `json:"change"`
}

type CodeActionOptions struct {
	CodeActionKinds []string `json:"codeActionKinds"`
}

type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

type DidChangeTextDocumentParams struct {
	TextDocument   VersionedTextDocumentIdentifier  `json:"textDocument"`
	ContentChanges []TextDocumentContentChangeEvent `json:"contentChanges"`
}

// TextDocumentContentChangeEvent replaces Range by Text, or, without a
// range, the whole document.
type TextDocumentContentChangeEvent struct {
	Range *Range `json:"range,omitempty"`
	Text  string `json:"text"`
}

type DidCloseTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type DidChangeWatchedFilesParams struct {
	Changes []FileEvent `json:"changes"`
}

type FileEvent struct {
	URI  string `json:"uri"`
	Type int    `json:"type"` // 1 created, 2 changed, 3 deleted
}

type DidChangeWorkspaceFoldersParams struct {
	Event struct {
		Added   []WorkspaceFolder `json:"added"`
		Removed []WorkspaceFolder `json:"removed"`
	} `json:"event"`
}

type DidChangeConfigurationParams struct {
	Settings json.RawMessage `json:"settings"`
}

type ConfigurationParams struct {
	Items []ConfigurationItem `json:"items"`
}

type ConfigurationItem struct {
	ScopeURI string `json:"scopeUri,omitempty"`
	Section  string `json:"section"`
}

type RegistrationParams struct {
	Registrations []Registration `json:"registrations"`
}

type Registration struct {
	ID              string `json:"id"`
	Method          string `json:"method"`
	RegisterOptions any    `json:"registerOptions,omitempty"`
}

type UnregistrationParams struct {
	// The name is the protocol's.
	Unregisterations []Unregistration `json:"unregisterations"`
}

type Unregistration struct {
	ID     string `json:"id"`
	Method string `json:"method"`
}

type DidChangeWatchedFilesRegistrationOptions struct {
	Watchers []FileSystemWatcher `json:"watchers"`
}

// FileSystemWatcher watches what GlobPattern matches: a string, or a
// RelativePattern.
type FileSystemWatcher struct {
	GlobPattern any `json:"globPattern"`
}

type RelativePattern struct {
	BaseURI string `json:"baseUri"`
	Pattern string `json:"pattern"`
}

// The severities of a Diagnostic.
const (
	SeverityError       = 1
	SeverityWarning     = 2
	SeverityInformation = 3
)

type Diagnostic struct {
	Range              Range                          `json:"range"`
	Severity           int                            `json:"severity"`
	Code               string                         `json:"code,omitempty"`
	CodeDescription    *CodeDescription               `json:"codeDescription,omitempty"`
	Source             string                         `json:"source,omitempty"`
	Message            string                         `json:"message"`
	Tags               []int                          `json:"tags,omitempty"`
	RelatedInformation []DiagnosticRelatedInformation `json:"relatedInformation,omitempty"`
}

type CodeDescription struct {
	Href string `json:"href"`
}

type DiagnosticRelatedInformation struct {
	Location Location `json:"location"`
	Message  string   `json:"message"`
}

type PublishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Version     *int         `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// The kinds of code action the server has.
const (
	KindQuickFix = "quickfix"
	KindFixAll   = "source.fixAll.dgdtools"
)

type CodeActionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
	Context      struct {
		Only []string `json:"only"`
	} `json:"context"`
}

type CodeAction struct {
	Title       string         `json:"title"`
	Kind        string         `json:"kind"`
	Diagnostics []Diagnostic   `json:"diagnostics,omitempty"`
	IsPreferred bool           `json:"isPreferred,omitempty"`
	Edit        *WorkspaceEdit `json:"edit,omitempty"`
}

// WorkspaceEdit has its edits in one of two forms: DocumentChanges, which
// names the version of the document they are for, when the client reads
// it, else Changes.
type WorkspaceEdit struct {
	Changes         map[string][]TextEdit `json:"changes,omitempty"`
	DocumentChanges []TextDocumentEdit    `json:"documentChanges,omitempty"`
}

type TextDocumentEdit struct {
	TextDocument VersionedTextDocumentIdentifier `json:"textDocument"`
	Edits        []TextEdit                      `json:"edits"`
}

type DefinitionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// LocationLink is a Location that also says what at the place asked
// about names it (OriginSelectionRange). TargetSelectionRange is the
// name at the target and TargetRange the whole of what is declared
// there; the server gives the name for both.
type LocationLink struct {
	OriginSelectionRange Range  `json:"originSelectionRange"`
	TargetURI            string `json:"targetUri"`
	TargetRange          Range  `json:"targetRange"`
	TargetSelectionRange Range  `json:"targetSelectionRange"`
}

// The kinds of a MarkupContent.
const (
	PlainText = "plaintext"
	Markdown  = "markdown"
)

// MarkupContent is text for a reader, in the kind the client said it
// shows.
type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type HoverParams = DefinitionParams

// Hover is what is told about a place; Range is the text it is about.
type Hover struct {
	Contents MarkupContent `json:"contents"`
	Range    *Range        `json:"range,omitempty"`
}

// SignatureHelpOptions names the characters that, typed, make a client
// ask for signature help, and those that make it ask again while it is
// showing some.
type SignatureHelpOptions struct {
	TriggerCharacters   []string `json:"triggerCharacters"`
	RetriggerCharacters []string `json:"retriggerCharacters"`
}

type SignatureHelpParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      *struct {
		IsRetrigger         bool           `json:"isRetrigger"`
		ActiveSignatureHelp *SignatureHelp `json:"activeSignatureHelp"`
	} `json:"context"`
}

// SignatureHelp is the declarations a call may be of. ActiveSignature
// is the one shown first, and ActiveParameter the parameter of it that
// the cursor's argument is for; a value past the parameters says none.
type SignatureHelp struct {
	Signatures      []SignatureInformation `json:"signatures"`
	ActiveSignature int                    `json:"activeSignature"`
	ActiveParameter int                    `json:"activeParameter"`
}

type SignatureInformation struct {
	Label         string                 `json:"label"`
	Documentation any                    `json:"documentation,omitempty"` // a string, or a MarkupContent
	Parameters    []ParameterInformation `json:"parameters"`
	// ActiveParameter is the signature's own, for a client that reads it
	// (activeParameterSupport).
	ActiveParameter *int `json:"activeParameter,omitempty"`
}

// ParameterInformation says where in the signature's label a parameter
// stands: as its [start, end) in UTF-16 code units for a client that
// reads offsets (labelOffsetSupport), else as its text.
type ParameterInformation struct {
	Label any `json:"label"`
}

type DocumentFormattingParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type DocumentRangeFormattingParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
}

// The types of a ShowMessageParams and a LogMessageParams.
const (
	MessageError   = 1
	MessageWarning = 2
	MessageInfo    = 3
	MessageLog     = 4
)

type ShowMessageParams struct {
	Type    int    `json:"type"`
	Message string `json:"message"`
}

type LogMessageParams = ShowMessageParams

type CancelParams struct {
	ID json.RawMessage `json:"id"`
}

type SetTraceParams struct {
	Value string `json:"value"`
}

type LogTraceParams struct {
	Message string `json:"message"`
}

type WorkDoneProgressCreateParams struct {
	Token string `json:"token"`
}

type ProgressParams struct {
	Token string `json:"token"`
	Value any    `json:"value"`
}

// WorkDoneProgress is the value of a ProgressParams: Kind is "begin",
// with a title, or "end".
type WorkDoneProgress struct {
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"`
}

// The error codes of the protocol, beside those of JSON-RPC.
const (
	codeServerNotInitialized = -32002
	codeInvalidRequest       = -32600
	codeMethodNotFound       = -32601
	codeInvalidParams        = -32602
	codeInternalError        = -32603
	codeRequestCancelled     = -32800
	codeContentModified      = -32801
)

// MethodIdle is a request of this server's own, for whoever drives it
// from a test or a script: it is answered when the server has nothing
// left to do, which is when every open document's published diagnostics
// are those of its text and the lib as they are now. No editor needs it.
const MethodIdle = "dgdtools/idle"
