# `dgdtools lsp`: implementation plan

Status: a plan, of which the first three steps of section 12 are built:
the editor-side grammars of section 10.1, under `editors/`; the server
itself for the steps of section 8 (live diagnostics, code actions,
formatting, go to definition, hover and signature help, the outline of a
document and the search for a name in the lib, completion, references
and highlights, semantic tokens, rename in a file and across the lib,
the refactorings of step 12: a file moved, a missing prototype, a
missing file's path), as `dgdtools lsp` and `pkg/lsp`, with the rename
command `dgdtools rename`; and the
VS Code client of
section 11, in `editors/vscode`, which has been built and checked but
not yet run in a VS Code. "What is built" below says exactly what, and
where it differs from the plan. What section 8 lists as "later" is
still a plan.
The server builds on the library described in
[architecture.md](architecture.md) (sections 6 and 10),
[config.md](config.md) and [formatter.md](formatter.md), and on one type
above all: `session.Session` (`pkg/session`), which holds what a server
holds (the open documents' texts, the projects, a linter for each) and
applies every change to all of it in one step. Beside it: `pkg/lint`
(`Document`, `Options.NoScan`, `Linter.Scan`), `pkg/index` (generations,
chains, `#define` environments, declarations with types and positions),
`pkg/calls`, `pkg/profile`, `pkg/format` (`Source`, `Edits`),
`pkg/config` (`Project.Settings`, `Project.Inputs`), `pkg/diag`
(`UTF16Col`, `ByteCol`).

The server is a thin layer: transport, document versions, scheduling and
position conversion. Analysis stays in the library; any logic the server
needs that a script or the CLI could also use goes into the library first.

### The session

```go
s := session.New(vfs.OS{}, config.DiscoverOptions{WorkDir: folder, ProfileDirs: dirs, UserConfig: user})

a := s.Change(path, text)          // didOpen, didChange: the buffer is the file
a  = s.Change(path, nil)           // didClose: the disk is the file again
a  = s.DiskChanged(paths...)       // didChangeWatchedFiles: files and directories
// a.Files: whose diagnostics may differ; a.All: configuration changed, anybody's may

doc, err := s.Lint(ctx, path, lint.Options{NoScan: true})
if err == nil && s.Current(doc) { /* publish doc.Diagnostics */ }

prj, _ := s.Project(path)          // settings, format.Source(prj, path, text, lines)
l, _ := s.Linter(path)             // l.Scan(ctx), l.BaselinePath(), l.Rules()
s.FS()                             // the files as the tools read them
```

- **One step.** A change sets or drops the buffer, tells the workspace
  (a `dgdtools.yml`, an `.editorconfig`, a profile, a driver
  configuration: `config.Workspace.Invalidate` forgets the projects
  resolved from it, and their linters go with them), tells every linter
  (a baseline it has read is read again; `Linter.Invalidate` starts the
  index's next generation), and returns the union of what the linters
  say is affected. Changes are applied one at a time.
- **No lint mixes two texts.** A lint reads one generation of the index
  from start to end, and a unit of the index knows the text it was built
  from: `Snapshot.Parse` refuses to pair it with another
  (`lint.Stale`). A lint that a change overtook either fails that way or
  is not `Current`; the server makes it again in both cases, which it
  was going to do anyway, since the changed file is in `Affected.Files`.
- **A document is self-contained.** `lint.Document` has the diagnostics,
  the tokens, the tree, the `#define` environment (safe to read from any
  goroutine), the index unit, and the `Snapshot` it was linted against,
  for the questions hover and definition ask about the lib as the
  diagnostics saw it. `Document.Errors` names the rules that crashed on
  the file; everything else in the document stands.
- **Configuration is not special-cased by the server.** It passes every
  watched path to `DiskChanged` and acts on `Affected`. A project that
  changed is resolved again, and indexes its lib again, when one of its
  documents is next linted (seconds on a big lib, and rare); the other
  projects keep their linters. `Project.Inputs()` and
  `Linter.BaselinePath()` are the files to watch that the glob patterns
  of section 3 may not reach (an `extends:` base, a profile in the user's
  configuration directory, the driver's configuration).

### What is built

`dgdtools lsp` speaks LSP 3.17 on standard input and output. `pkg/lsp`
is some 3,200 lines over `session.Session`, the protocol's types among
them. It makes one session, with `DiscoverOptions{ProfileDirs,
UserConfig, Folders}` and no working directory: every path it is given
is absolute.

| file | |
|---|---|
| `server.go` | `Serve`, the connection (the only file that knows `sourcegraph/jsonrpc2`), the order of things: notifications one at a time in the goroutine that reads, a goroutine for each request and for each answer; `initialize`, `shutdown`, `exit`; `$/cancelRequest`; panics; the queue of outgoing notifications; what "idle" is |
| `sync.go` | the open documents and their incremental changes; watched files; workspace folders and the two settings; the file watchers asked of the client |
| `diagnostics.go` | when a document is linted, and what is published: the timers, the lint that a newer change stops, the whole-lib scan, `diag.Diagnostic` to the protocol's |
| `actions.go` | `textDocument/codeAction` (a finding's fixes, `lint.Document.PathFixes`, `lint.Suppress`, `Document.Prototype`), `formatting`, `rangeFormatting` |
| `definition.go` | `textDocument/definition`: `lint.Document.Resolve`'s answer as links or locations |
| `hover.go` | `textDocument/hover` and `textDocument/signatureHelp`: `lint.Document.Hover`'s and `Signature`'s answers laid out as Markdown or plain text |
| `completion.go` | `textDocument/completion` and `completionItem/resolve`: `lint.Document.Complete`'s items as the protocol's, and the comment above a declaration when an item is asked for |
| `symbols.go` | `textDocument/documentSymbol` and `workspace/symbol`: `lint.Document.Symbols`' outline and `index.Snapshot.FindSymbols`' matches as the protocol's symbols |
| `files.go` | `workspace/willRenameFiles`: `rename.Files`' plan of the files moved as a `WorkspaceEdit` of the files as they are, and what was not changed shown |
| `rename.go` | `textDocument/prepareRename` and `textDocument/rename`: `lint.Document.PrepareRename`'s target as a range and a placeholder, `Rename`'s plan as a `WorkspaceEdit` for every file it edits, the possible edits under change annotations to confirm, a refusal as an error with its reason, and what the plan says beside its edits shown |
| `references.go` | `textDocument/references` and `textDocument/documentHighlight`: `lint.Document.References`' places as locations, with a note in the log where the answer is not all there is; `Document.Highlights`' as highlights, read and written |
| `tokens.go` | `textDocument/semanticTokens/full`, `full/delta` and `range`: `lint.Document.Tokens` encoded in the legend (`TokenLegend`), the last answer kept for a delta, and `workspace/semanticTokens/refresh` when a change elsewhere changed a document's tokens |
| `content.go` | `workspace/textDocumentContent`: the text of a profile's stub |
| `position.go` | `PositionMapper`, URIs and paths |
| `protocol.go` | the protocol's types, as far as used (some 60) |
| `lsptest/` | a client in the test's own process, over a pipe |

Capabilities announced: `positionEncoding` (`utf-8` when the client
offers it, else `utf-16`), `textDocumentSync` (open and close,
incremental changes), `codeActionProvider` (`quickfix`,
`refactor.rewrite`, `source.fixAll.dgdtools`; only to a client with code
action literals),
`completionProvider` (asked for at `>`, `:`, `"`, `/` and `<`, with
`completionItem/resolve`), `definitionProvider`, `referencesProvider`,
`documentHighlightProvider`, `hoverProvider`,
`signatureHelpProvider` (asked for at `(` and `,`, and again at `)`),
`documentSymbolProvider`,
`workspaceSymbolProvider`,
`documentFormattingProvider`, `documentRangeFormattingProvider`,
`semanticTokensProvider` (the legend of section 8, "As built, step 9";
`full` with `delta`, and `range`), `renameProvider` (with
`prepareProvider` to a client that says `prepareSupport`),
`workspace.workspaceFolders` (with change notifications),
`workspace.textDocumentContent` for the scheme `dgdtools-profile`,
`workspace.fileOperations.willRename` for `**/*.{c,h}` files and `**`
folders. Asked
of the client when it can: `workspace/configuration`,
`client/registerCapability` for `workspace/didChangeWatchedFiles`,
`window/workDoneProgress/create`, `workspace/semanticTokens/refresh`.

What the library got for it, since both are a script's or a command's as
much as the server's: `config.DiscoverOptions.Folders` (section 3) and
`lint.Suppress` (the suppression comments of section 8, step 2). For
step 4 it got `pkg/resolve`, what a place in a file refers to, which the
server only asks (section 8, "As built, step 4"); for step 5 `pkg/kfun`,
the driver's kernel functions, and in `pkg/resolve` what there is to
tell about a place and what a call takes ("As built, step 5"); for step
6 the outline of a file (`resolve.Resolver.Symbols`), the search of a
generation of the index by name (`index.Snapshot.FindSymbols`) and the
matcher under it (`internal/fuzzy`) ("As built, step 6"); for step 7
the scopes of a function (`syntax.File.Scopes`), what can be written at
a place (`resolve.Resolver.Complete`) and the entries of a directory of
the lib, on disk and in a profile's stubs (`index.Snapshot.IncludeEntries`,
`ObjectEntries`) ("As built, step 7"); for step 8 the places a file
names other files' declarations at, kept with its unit (`index.Use`,
`Unit.Uses`, `index.WithUses`, `Snapshot.UseUnits`), the search over
them (`resolve.Resolver.References`, `Highlights`, `Lookup`,
`resolve.Extractor`), the lookups in a chain without flattening it
(`index.Snapshot.ChainFunc`, `ChainUnseen`) and one walk for the paths
of all the names of a file (`syntax.PathsAt`) ("As built, step 8"); for
step 9 the semantic tokens of a file (`resolve.Resolver.Tokens`,
`lint.Document.Tokens`) ("As built, step 9"); for step 10 `pkg/rename`
(`rename.Prepare`, `rename.Rename`, `lint.Document.PrepareRename` and
`Rename`), who includes a file and who inherits an object
(`index.Snapshot.Includers`, `Users`), whether a profile has a stub of a
checkout's file (`Snapshot.Shared`), and whether anything but a file
itself knows a macro (`pp.Env.Outside`) ("As built, step 10"); for step
11 the rename across files (`rename.Plan.PossibleEdits`, `Dynamic`,
`rename.File.Force`, `Refusal.Forcible`), the calls whose function's
name is made at run time (`calls.Facts.Dynamic`, `index.UseDynamic`)
and a chain's variable without flattening it (`index.Snapshot.ChainVar`)
("As built, step 11"); for step 12 the plan of moved files
(`rename.Files`, `rename.Move`, `Plan.Moves`), an `#include` resolved in
the lib as it will be (`index.Snapshot.ResolveIncludeIf`), the files of a
name (`Snapshot.FilesNamed`), a unit's Uses by their Aux
(`index.Unit.UsesWith`), the two code actions
(`lint.Document.Prototype`, `PathFixes`) and the driver object of the
configuration (`config.Config.DriverObject`) ("As built, step 12").

Where what is built is not what the sections below planned, the section
says so in a paragraph that begins **As built**. In short:

- A closed document's diagnostics are always cleared (section 4).
- One timer for each document, not a scheduler for each project; and
  only the first scan of a lib is made beside the lints (section 5).
- The lines of a position are counted by the server, not taken from the
  lexer (section 6).
- `dgdtools.showBaselined` and the "explain rule" command are not built
  (sections 3 and 8).
- The server has one request of its own, `dgdtools/idle`, for tests and
  scripts (section 13).
- A profile's stub is read by `workspace/textDocumentContent`, a request
  of LSP 3.18, which came with the VS Code client (section 11).
- The VS Code extension downloads no binary: a release has it as a
  package for each platform, with the binary inside (section 11.2).

## 1. Shape

- `dgdtools lsp` subcommand (one binary to install and version; the
  server must always match the linter and the formatter). stdio
  transport only; `--log FILE` and `--trace` flags. stdout is the
  protocol, stderr is never written to except on startup failure.
- New package `pkg/lsp` (server, document store, scheduler, protocol
  subset) and `pkg/lsp/lsptest` (in-process client for tests).
  `cmd/dgdtools` only wires stdin/stdout to it.

## 2. Transport and protocol types

**JSON-RPC:** use `github.com/sourcegraph/jsonrpc2` (with its
`VSCodeObjectCodec` for `Content-Length` framing).

- Hand-rolling the framing is 100 lines; the part that goes wrong is the
  rest: request/response correlation in both directions (the server also
  *sends* requests: `workspace/configuration`, `client/registerCapability`,
  `workspace/applyEdit`), concurrent writers, and orderly shutdown. That
  is what the library does, in ~1,500 lines, and it has years of
  production use (Sourcegraph, many Go language servers). It has a
  websocket transport in a package of its own, which is not imported:
  `go.sum` names `gorilla/websocket` for it and the binary has none of
  it. `$/cancelRequest` it leaves to its user: the server keeps the
  cancel function of each request it is answering.
- `golang.org/x/tools/internal/jsonrpc2` (gopls) cannot be imported;
  `golang.org/x/exp/jsonrpc2` is its experimental copy with no stability
  promise. `go.lsp.dev/jsonrpc2` is fine but comes paired with
  `go.lsp.dev/protocol`, which is what we should avoid (next point).
- If the project's dependency policy is "none beyond yaml", hand-rolling
  is acceptable and bounded (2 days including cancellation); the server
  is written against a small interface (`Notify`, `Call`, `Close`; a
  handler returns its answer) and names the library in one file, so the
  choice is swappable.

**Protocol types:** hand-written structs for the subset used (about 40
types), in `pkg/lsp/protocol.go`. Generated full-spec packages lag the
spec (position encoding negotiation, pull diagnostics), are large, and
make every optional field a pointer; we use perhaps 5% of the spec.

**Dispatch:** notifications that change state (`didOpen`, `didChange`,
`didClose`, `didChangeWatchedFiles`, `didChangeConfiguration`) are handled
strictly in arrival order on one goroutine, each as one call on the
session. Requests are handled concurrently against what a lint returned
(a `lint.Document`: one document version, one generation of the index,
which no later change disturbs), each with a context cancelled by
`$/cancelRequest`.

**As built:** a request is not cancelled by a newer version of its
document; it is answered `ContentModified` when the document changed
while it was being answered, which a client passes over. The goroutine
that reads never writes: every answer is a goroutine's, since a client
may be waiting for its own message to be read before it reads the
server's. Notifications to the client go through one queue, so what was
published last for a document is what the client has.

## 3. Workspaces, projects and profiles

- `initialize` gives `workspaceFolders` (fallback `rootUri`).
  `workspace/didChangeWorkspaceFolders` is supported.
- A **project** is what `Session.Project(path)` returns: config file,
  lib root or partial-checkout mount, profile. The session resolves each
  root config once, and again when what it was resolved from changes;
  each project has one `lint.Linter`, made when first needed, and all of
  them read the one overlay of open buffers.
- A **document** finds its project from its own path, exactly like the
  CLI: the nearest root config upward; settings scopes are passed over.
  A root config below another project's is a project of its own, for the
  server as for every command. Two workspace folders under one lib share
  one project; one folder containing two libs gets two. A file with no
  project gets the shared one without a config (file-local rules; the
  user-level configuration when there is one).
- A document's **settings** are `Project.Settings(path)`: the format
  settings and the lint layers of that file (scopes, `.editorconfig`).
  The server asks the library and adds nothing.
- **Zero-config wizards:** client settings `dgdtools.profile` and
  `dgdtools.libPath` (read with `workspace/configuration`, per folder) are
  passed as `DiscoverOptions.Profile/LibPath` for folders that have no
  `dgdtools.yml` (the lib path is that of `DiscoverOptions.WorkDir`, the
  workspace folder). They carry only what a wizard without any file needs;
  nothing in an editor's settings overrides a project. The server also
  passes what the library does not read itself:
  `DiscoverOptions.ProfileDirs` from `profile.UserDirs` (the
  `$DGDTOOLS_PROFILE_PATH` value and the user's config and home
  directories), `DiscoverOptions.UserConfig` from
  `config.UserConfigPath`, and absolute paths throughout. A config file
  always wins over editor settings. No profile is ever assumed
  ([architecture.md](architecture.md) 10.4); the extension's settings UI
  is where a wizard names their mud's profile (resolved with
  `profile.Resolve`, so a profile installed in the user config directory
  works for every folder).

  **As built:** the settings are not passed as
  `DiscoverOptions.Profile/LibPath`, which are a command's flags and win
  over every config file in the workspace. They are
  `DiscoverOptions.Folders`: for each workspace folder the settings say
  something of, its directory, profile and lib path, which hold for the
  paths under it that no config file claims and make them a project of
  the folder's, anchored there. The one session serves every folder; it
  is made again (the open documents its buffers again, all of them
  linted) when the folders' settings are no longer those it was made
  with, and only then. A client without `workspace/configuration` says
  the same two keys in `initializationOptions` and in the `dgdtools`
  section of `workspace/didChangeConfiguration`, for all its folders.
- **Watching:** register `workspace/didChangeWatchedFiles` for
  `**/*.{c,h}`, `**/dgdtools.yml`, `**/.dgdtools.yml`,
  `**/.editorconfig`, `**/*.profile`, `**/dgdtools-baseline.json`, and
  for the `Project.Inputs()` and `Linter.BaselinePath()` of the projects
  in use that lie outside the folders. Every event goes to
  `Session.DiskChanged`, a deleted or renamed directory as the directory;
  events for a document that is open change nothing the tools read. The
  open documents in `Affected.Files` are linted again, all of them when
  `Affected.All` is set.

  **As built:** the patterns are one registration; the files of
  `Project.Inputs()` and `Linter.BaselinePath()` are added to it, each as
  a pattern relative to its directory (`relativePatternSupport`; an
  absolute path as the pattern otherwise), when the patterns do not have
  them: outside every folder, or under a name no pattern has (a config
  that is extended, the driver's configuration). The registration is made
  again when that set changes. A client without dynamic registration is
  not asked; the server then sees what the buffers say and nothing of the
  disk.
- The baseline applies in the editor as on the command line. A setting
  `dgdtools.showBaselined`, to show baselined findings as hints, is not
  built: the editor's settings say the profile and the lib path of a
  folder without a config, and nothing else.

## 4. Documents and text synchronization

- Sync kind **Incremental**. Per document: URI, fs path, version, text as
  `[]byte` (UTF-8, section 6), line-start table.
- `didChange`: apply each range edit to the buffer (positions converted
  with the negotiated encoding against the *pre-edit* line table), rebuild
  the line table from the first changed line, then
  `affected := session.Change(path, text)`, and schedule diagnostics for
  the document and for every *open* document in `affected.Files`.
- `didOpen` is the same call even if the text equals the disk file (the
  editor is now the source of truth); `didClose` is
  `session.Change(path, nil)`, and clears the document's diagnostics.
  (The plan was to compute them once more from disk for a file in a
  project. Nothing would have kept them true after that: diagnostics are
  of open documents, section 14.)
- `didSave` needs nothing (the watcher covers other processes), is not
  asked for, and is passed over when it comes.

**As built:** the line table is made again whole after a change, not
from the first changed line: the change copies the text anyway, and a
table made from the text has no way to be wrong (a line break can be
made or unmade across the edge of an edit). A document whose URI names
no file (`untitled:`) is kept in step and nothing is said about it.
- A full-document fallback (`TextDocumentSyncKind.Full`) is kept for
  clients that do not do incremental sync; the fuzz test (section 13)
  proves both paths produce the same buffer.

## 5. Scheduling: debounce, cancellation, invalidation

- One scheduler per project. A change marks the document dirty and
  (re)starts a 200 ms timer; when it fires, the document is linted at its
  then-current version with a fresh context.
- A newer change to the same document cancels the running lint
  (`context.CancelFunc`); the library checks the context between files
  and during the whole-lib scan, and a cancelled reverse lookup answers
  "unknown", so a cancelled run can never publish a wrong "missing".
- Results are published only if the document's version still equals the
  version the lint started with and `Session.Current(doc)` holds;
  otherwise the document is linted again. (A lint that was in flight when
  another file changed describes the lib before that change, and a change
  reports the dependents of lints against the current generation only.)
  A lint that fails as `lint.Stale` was overtaken by a change to its own
  file and is made again the same way. Diagnostics carry the document
  version.
- Dependents (open files that include or inherit the changed file) are
  queued behind the edited document at lower priority, with a longer
  debounce (1 s), so typing in a header does not re-lint twenty rooms per
  keystroke.
- Documents are linted with `Options.NoScan`. The first lint that
  comes back with `NeedsScan` (a self-call the module's own chain does
  not provide) makes the server run `Linter.Scan` on a background
  goroutine with `$/progress` ("indexing the mudlib", a few seconds on
  VikingMUD); until it completes those lookups are unknown, and the
  documents that needed it are linted again when it finishes. The scan
  survives edits: after the first one, `Scan` reads only the files that
  changed, so the server simply calls it again after each change that
  leaves a document with `NeedsScan`; a scan that a change overtakes
  stops and gives the scan slot to the next. In a partial checkout the
  scan covers only the checkout and takes milliseconds.

**As built:** there is no scheduler object. A document has at most one
timer and one running lint; a change to it restarts the timer and stops
the lint, and a lint that is scheduled for the document's own change is
not put off by one for something else. Lints of different documents run
beside each other. What is published is compared with what was
published last for the document, version included, and not sent twice.
A lint that fails on the configuration is said to the user once
(`window/showMessage`) and clears the document's diagnostics.

The scan differs from the plan in one way that was found by a test: a
document linted with `NoScan` right after a change to another file says
less than it said before (the scan is not complete for a moment), and
publishing that makes findings blink. So only the first scan of a lib is
made beside the lints, with `$/progress` when it takes longer than
300 ms, and its end logged to the client (`window/logMessage`) and to
the `--log` file: `mudlib scanned: 5205 files in 2.1 s (/path/to/lib)`,
or a warning when it does not complete. Once a document has been linted with the scan complete, its
lints are made without `NoScan` and complete it themselves, which is a
file or two after a change.

## 6. Positions and encodings

Two separate questions.

**What a column counts.** LSP's default is UTF-16 code units. LSP 3.17
lets the client offer `general.positionEncodings`; if it offers `utf-8`
the server selects it and a column is simply a byte offset in the line,
which is what the lexer counts: no conversion at all. Otherwise UTF-16
via `diag.UTF16Col` and its inverse `diag.ByteCol(line, utf16)`,
computed on the single line involved. Both directions live in
one small type, `lsp.PositionMapper`, used for every incoming position
and every outgoing range.

**As built:** the mapper counts lines itself, as the protocol does
("\n", "\r\n", a "\r" alone), and does not use the lexer's line table.
The two agree except about a lone "\r" inside a token (a string that is
never closed), which the random sessions of section 13 found: an editor
counts a line there and the lexer does not.

**What bytes the server analyzes.** The lexer is byte-oriented and never
transcodes; the corpus has Latin-1 files. The editor, however, sends
document text as a JSON string, i.e. as Unicode.

- For an **open document** the server analyzes the text the editor sent,
  encoded as UTF-8. It does not matter what the file on disk is: the
  buffer is valid UTF-8 by construction, positions computed on it map
  exactly to what the editor shows, and edits (fixes, formatting) are
  returned as ranges plus Unicode text which the editor writes back in
  whatever encoding it opened the file with. To the lexer a non-ASCII
  character inside a string or comment is just more high bytes, as legal
  in UTF-8 as in Latin-1; outside strings and comments both are the same
  "illegal character" error.
- For a file that is **not open** (a parent, a header, a dependent) the
  server reads disk bytes, Latin-1 or not. Nothing is published for those
  except through related locations, whose columns use the "invalid UTF-8
  byte = one unit" rule: exact when the editor decodes the file as a
  single-byte encoding, and exact for all-ASCII lines regardless.
- The one real hazard is the editor, not the server: opening a Latin-1
  file as UTF-8 shows replacement characters and **saving corrupts it**.
  The VS Code extension therefore contributes
  `"[dgdlpc]": {"files.autoGuessEncoding": true}` and documents
  `"files.encoding": "iso88591"` for libs that are Latin-1; the server
  logs a one-time warning (`window/showMessage`) when a file on disk is
  not valid UTF-8 and the opened text contains U+FFFD.
- The server never writes source files.

## 7. Robustness

- Every handler runs under `recover`: a panic becomes an `InternalError`
  response (or is swallowed for a notification), is logged with a stack,
  and the server keeps running. The library already isolates a panic
  while analyzing one file (`lint.Internal`) and a panic in one rule
  (`Document.Errors`: the document keeps its tree and the other rules'
  findings); both are logged, neither is shown as a diagnostic.
- Requests have a deadline (5 s for interactive ones); on expiry they
  return an empty result, not an error dialog.
- Memory: the index keeps facts, not trees; trees exist for the duration
  of a request. Open documents keep one buffer each.
- `shutdown`/`exit` per spec; exit code 1 if `exit` arrives without
  `shutdown`. If stdin closes, the server exits.
- **Logging:** `window/logMessage` for user-relevant events (project
  resolved, profile loaded, notes from `Result.Notes`), `--log FILE` for
  a structured debug log, `$/setTrace` + `$/logTrace` for per-request
  timing, `--trace` to dump the JSON-RPC stream (this is also how golden
  transcripts are recorded).

**As built:** `--log FILE` is a `log/slog` text log at debug level
(each lint and request with its duration, panics with their stacks), and
`--trace` adds every message to it and needs `--log`. `window/logMessage`
gets, once for each project: what project a document was found to belong
to, and the notes about it; and a line for a text the formatter refuses.
`$/logTrace` gets the time each request took.

## 8. Features, in order

Each step is shippable. Estimates are working days for one person who
knows the code base.

| # | Feature | Maps onto | Needs first | Days |
|---|---|---|---|---|
| 1 **built** | Skeleton: initialize/shutdown, projects, incremental sync, `publishDiagnostics` with code, `codeDescription` (`Rule.URL`), related information, tags (`Diagnostic.Tags`: `unnecessary` for unreachable-code and unused suppressions) | `Session.Change`, `DiskChanged`, `Lint`, `Current`; `Linter.Scan`; `diag` | `PositionMapper` | 5 |
| 2 **built**, but for "explain rule" | `codeAction`: quick fixes from `Diagnostic.Fixes` (a `safe` fix is `isPreferred` and joins "fix all"; an `unsafe` one is offered alone); "suppress with `dgdtools-ignore lint/RULE`" and "…for this file" (a `dgdtools-ignore-start lint/RULE` at its top) generated per diagnostic; "explain rule" as a command | fixes as text edits with their applicability (already data) | a helper that renders a suppression comment in the file's comment style and indentation | 2 |
| 3 **built** | `formatting` and `rangeFormatting`; format-on-save of modified lines is the client's use of the latter | `format.Source(prj, path, text, lines)`: a function of the buffer and the file's settings, behind the gate (the same tokens, the same comments, a fixed point; a refusal returns no edits and a log line); `format.Edits(file, out)` gives one text edit per changed gap, so cursors and folds survive | LSP range to line range (a range that ends at column 0 does not include that line) | 2 |
| 4 **built** | `definition`: `#include` targets, `inherit` paths, function calls (direct, `::`, `label::`, `->` with a known receiver, string callbacks in registrars), macros | `Document.Snapshot` and a `Query` on it: `ResolveInclude`, `ObjectPath`, `Chain`, `LookupCallable`; the file's `pp.Env` (`pp.Macro.Path/Off`) | `syntax.PathAt(file, offset)` (innermost node chain at a position); a stub target is already located by its virtual document URI (`dgdtools-profile://NAME/std/room.c`, see `profile.Location`), which the server answers with the stub's text | 4 |
| 5 **built** | `hover` and `signatureHelp` | `ChainFunc.Decl/Type/DefinedIn`, `index.Params`, macro body and class, variable types | active-parameter computation from the argument list around the cursor; kfun table for built-ins (new data file) | 3 |
| 6 **built** | `documentSymbol`, `workspace/symbol` | `syntax.File.Decls`; units' symbol tables (whole-lib scan, cached) | fuzzy matcher | 2 |
| 7 **built** | `completion`: functions callable here (own chain, auto object, kfuns), `->` members for a known receiver, macros in view, `#include` and inherit paths, locals | chain + view + profile | local scopes (block-level symbol tables in `pkg/syntax`); tolerant parse at the cursor (the parser already recovers; completion context comes from tokens left of the cursor) | 5 |
| 8 **built** | `references` (and `documentHighlight`) | new: a reverse index of call sites and macro uses per name, built with the whole-lib scan and invalidated per file | reverse index in `pkg/index`; in a partial checkout results are labelled as "in this checkout" | 4 |
| 9 **built** | `semanticTokens` (section 10.2): what a grammar cannot know. Ships in two parts: 9a macros by class, declarations, inherit labels, calls resolved through the chain (no scopes needed); 9b parameters/locals and unresolved names | tree + view + chain | 9b needs scopes (from 7) | 9a: 2, 9b: 2 |
| 10 **built** | `prepareRename` and `rename` for the symbols one file decides (section 9): locals and parameters, private functions and variables, macros a `.c` file defines for itself | `pkg/rename` (new) over the tree, the scopes and the file's `pp.Env` | scopes (from 7); `syntax.PathAt` (from 4) | 3 |
| 11 **built** | Rename across files, in the editor and as `dgdtools rename SYMBOL NEW` (section 9): functions, object globals, header macros; every occurrence classified certain, possible or excluded; edits previewed with change annotations, or written as a patch | `pkg/rename` over the reverse index, who inherits a file (a `Query` on the snapshot), `Chain`, `LookupCallable`, `calls.Extract` | the reverse index (from 8); the whole-lib scan; step 10 | 9 |
| 12 **built** | Refactorings as code actions (section 9.6): add the missing prototype, fix an `#include` path; renaming a file updates the `inherit` and `#include` lines that name it (`workspace/willRenameFiles`) | `ResolveInclude`, `ObjectPath`, the include and inherit edges `Invalidate` already follows | the reverse index (from 8) for the file rename | 3 |
| later | inlay hints for parameter names, pull diagnostics (`textDocument/diagnostic`, `workspace/diagnostic`), call hierarchy over inherit chains | | | |

**As built, steps 1 to 3.** A diagnostic's `code` is the rule's code
(`R001`), its `codeDescription` the rule's section of `docs/rules.md`,
its `source` `dgdtools`; related locations, tags and the link are sent
to a client that says it reads them. A fix is a `quickfix` with the
finding it is for, preferred when safe; all safe fixes of the document
are `source.fixAll.dgdtools`, which is what an editor's "fix all on
save" asks for by kind. The two suppression actions are
`lint.Suppress`: the line form goes above the finding's line at its
indentation, or into the comment that is already about that line (a
second comment above would be about the first), and is not offered where
no line can be put (inside a comment or a macro's continued line); the
file form is not offered in a file that already has a range for the
rule. Edits name the document's version for a client that reads
`documentChanges`. "Explain rule" as a command is not built: the rule's
link is on the diagnostic, and a command would need `executeCommand` and
a place to show text, for the same words. Formatting takes the project's
settings and passes over the request's `tabSize` and `insertSpaces`.

**As built, step 4.** The answer is the library's, `pkg/resolve`, and
`pkg/lsp/definition.go` is its 80 lines of protocol: the position to an
offset, `lint.Document.Resolve(off)`, each declaration to a range in the
text of its file. A client that says `linkSupport` gets `LocationLink`s,
with the range of what was asked about (`originSelectionRange`: the
name, the callback's name inside its quotes, the whole path of an
inherit statement, the `<file>` of an `#include`) and the declared name
as both target ranges; another gets `Location`s. Nothing to say is
`null`. `declaration` and `typeDefinition` are not offered.

```go
doc, _ := s.Lint(ctx, path, lint.Options{NoScan: true})
ref := doc.Resolve(off)        // *resolve.Ref, nil when the place names nothing
ref.Kind, ref.Name             // include, object, label, function, variable, macro
ref.Start, ref.End             // the text that names it
ref.Decl                       // the place is itself a declaration of the name
ref.Call                       // *calls.Named for ->fn, call_other, a registrar's callback
for _, d := range ref.Defs {   // none: named, and declared nowhere the tools see
    d.Path, d.Start, d.End     // file or stub location; the declared name in it
    d.Object, d.Auto           // the object that has it; the auto object's
    d.Func, d.Var, d.Macro     // index.FuncInfo, index.VarInfo, pp.Macro
}
```

`resolve.New(resolve.File{...})` is the same for a caller without a
lint; a `Resolver` is safe for concurrent use and reads the file's call
sites (`calls.Extract`) once. Hover (step 5) reads the facts of a `Def`
and, for a `Ref` without one, knows what was named (a function of the
driver's for the kfun table); references and rename (steps 8, 10, 11)
start from the same classification.

What resolves:

| at the cursor | goes to |
|---|---|
| an `#include` line, anywhere on it | the file `Snapshot.ResolveInclude` finds: beside the file, in the include directories, in a profile's stubs |
| an `inherit` statement, any token of it but the label (the keywords, the path, the `;`) | the object's file. `inherit ROOM;` is about the room: the macro's `#define` is not offered there (in the shared directories of the lib measured, 95% of the inherit statements are written with a macro) |
| an inherit label, in `label::fn()` or where it is given | the label in its inherit statement |
| `fn(...)`, `&fn(...)` | the file's own function (one a header it includes as code declares, too), else the inherited one, else the auto object's; a definition before a prototype |
| `::fn(...)`, `label::fn(...)` | the function of the inherited objects (of that label), and the auto object's for `::fn` when none has it. Two that are not each other's redefinition are both given: the call is ambiguous |
| `x->fn(...)`, `call_other(x, "fn")`, a callback named by a string in `call_out` or a `conventions.call_registry` entry | with `x` being `this_object()` (or absent): as `fn(...)`. With `x` a string constant or a macro that is one: the function in that object's chain, else the auto object's |
| the name of a function at its definition or prototype | the definition (a prototype's name goes to the definition) |
| a macro's name, in code or in a directive (`#ifdef`, a `#define` body) | the name in its `#define`: in the file, a header, the force-included header, a stub (`pp.Env.Definition`, `pp.Macro.NameOff`) |
| a global variable | its declarator: the file's, an inherited object's, one a header declares |
| a string constant handed to `clone_object`, `compile_object`, `find_object` or a `conventions.object_registry` function | the object's file |

What deliberately does not, each "prove it or stay silent":

- **An unknown receiver.** `ob->fn()` is a `Ref` with `Call.Target ==
  calls.TargetUnknown` and no `Def`: the server answers `null`. The same
  for an object that is not there, and for one that is in neither the
  checkout nor the profile.
- **A function only an inheritor has.** `this_object()->fn()` and a
  callback resolve in the file's own chain; when the name is provided
  by whatever inherits the file (what the linter calls
  `LookupFoundInUser`), which inheritor is running is nobody's to say,
  and it would be a list. That is `implementation`, later.
- **A function of the driver's.** A kfun has no source; the `Ref` is
  there for hover's table.
- **The auto object's function under an unseen object.** When the chain
  has an object the index cannot see (an inherit that does not resolve,
  an object that is not there: `index.Chain.Unseen`), that object may
  redefine the name, as a lib's own objects do for many of the auto
  object's functions, so the auto object's is given only when it is
  `nomask` (`index.FuncInfo.Nomask`). The corpus found this: a wizard's
  room whose `inherit` macro came from a header outside the checkout
  went to the auto object's `set_light` where the whole lib goes to
  `/std/object`'s (26 of 224,000 references; none now).
- **What is private elsewhere.** A function or variable that is
  `private` in an inherited object, or lies below a `private inherit` of
  one, is not the file's; and it may hide one that is, so nothing is
  said.
- **Locals and parameters.** A name is the parameter or the local
  variable in scope where it stands (`syntax.Scope`, step 7), and a
  global only where none is. Where a statement before it in a scope
  around it could not be parsed and mentions the name, nothing is said:
  it may be a declaration.
- **Code that is not compiled or was not read.** A branch of a
  conditional that is skipped, a comment, a statement that did not
  parse and the first one after it (`BadStmt.Resumed` is not part of the
  tree), the arguments of a macro that are no expressions, except for a
  macro's own name there.
- **Positions inside a file.** A macro is what the file's environment
  has at its end (`Env.Definition`): of two `#define`s of a name the
  later, wherever the use stands.
- **From inside a stub.** A stub is shown, not analyzed: a definition
  asked for in a `dgdtools-profile:` document is `null`. (It would take
  the stub's project from the profile's name and `Snapshot.Parse` of the
  stub's location; not built.)

A place in another file is counted in the text the document's
generation of the index reads (`Snapshot.ReadFile`): the editor's buffer
for a file that is open, so the answer follows unsaved changes there;
the disk; a stub's text, which is what `workspace/textDocumentContent`
gives for its URI.

**As built, step 5.** Both answers are the library's again, and
`pkg/lsp/hover.go` lays them out: `lint.Document.Hover(off)` and
`Document.Signature(off)`, which are `pkg/resolve` on the document.

```go
h := doc.Hover(off)            // *resolve.Hover, nil when there is nothing to say
h.Ref                          // what Resolve(off) gives: the name, and its text
for _, info := range h.Infos { // one for each declaration; one for a kfun, an unknown receiver
    info.Kind, info.Name
    info.Code                  // the declaration as LPC
    info.Doc                   // the comment above it, or the kfun table's line
    info.Class                 // of a macro: what the parser takes it for
    info.Note                  // what is not known: resolve.UnknownReceiver
    info.Origin                // Kfun, Driver, Auto, Configured, File, Line, Profile; String() says it
}

s := doc.Signature(off)        // *resolve.SignatureHelp, nil outside a call or when nothing is known
s.Name, s.Arg                  // the function called, the argument the cursor is in
for _, sig := range s.Signatures {
    sig.Label                  // the declaration
    sig.Params                 // [start, end) of each parameter in Label
    sig.Active                 // the parameter the argument is for; -1: none
    sig.Doc, sig.Origin        // as a hover's
}

kfun.DGD()                     // the driver's kernel functions
prj.Config.Kfuns()             // and the project's: dialect.kfuns
```

What a hover says, by what is named:

| at the cursor | code | and |
|---|---|---|
| a function, in a call of any form that resolves, or at its declaration | its declaration as written, without the body: specifiers, return type, name, parameters (`index.FuncInfo.Decl`) | the comment directly above it; the file, by its name in the lib, and the line; `auto object` for one the auto object gives; `profile NAME` for one a stub declares. An ambiguous `::fn()` has each |
| a kfun | its prototype from the table | the table's line of what it does; `kfun, DGD 1.7.9`, or `kfun, declared by the project (dialect.kfuns)` |
| `x->fn()`, `call_other(x, "fn")` with `x` not known | none: `function fn` | "the receiver is not known: which object's function this calls cannot be told". Nothing is guessed |
| a global variable | `private static int weight`: specifiers, type, name | comment, file and line |
| a macro | `#define NAME(params) body`, the body on one line and cut at 400 bytes | `macro (CLASS)`, the class being what the parser takes its uses for; comment, file and line. A name the configuration gives a class and no file read defines says that |
| an `#include` line | `#include <std.h>` | the file it resolves to |
| the path of an `inherit` statement, a path handed to `clone_object` | none: `object /std/room` | the object's file |
| an inherit label | `inherit side "/std/side";` | the file of the object it stands for |

A name declared nowhere the tools see has no hover (`null`): a function
nobody declares, a call on a known object that has no such function, an
`#include` that leads nowhere, an object that is not there. So has
everything `Resolve` says nothing of: locals, code that is not compiled.

The comment is the one directly above the declaration
(`resolve.Comment`): a block comment, or a run of `//` lines, that ends
on the line before the declaration begins (or on that line, before it),
with nothing but space before it on its own lines, so that the comment
at the end of the line above is not taken. It is read in the text the
document's generation of the index reads (`Snapshot.ReadFile`: an
editor's buffer for a file that is open), given without its marks, the
`*` that lead its lines and the indentation they share, and read as
Latin-1 when it is not UTF-8. A stub has no comments, and no lines that
mean anything: a declaration in one has neither.

For a client that says it shows Markdown
(`textDocument.hover.contentFormat`) the declaration is a `dgdlpc` code
block, the name the editors' grammars have, and the comment is written
so that it shows as it was typed: what Markdown reads as marks is
escaped, a line break stays one, indentation is kept. Another client
gets the same text plain. Several declarations are parted by a rule. The
range is that of the name asked about.

**Signature help** is found in the tokens to the left of the cursor,
not in the tree: a call that is being written does not parse, and the
statement around it is a `BadStmt` with nothing inside. From the cursor
back over the compiled tokens (`pp.Selection.Sig`) of the function's
body, counting brackets, to the `(` that is open and follows a name; the
commas passed at that depth are the arguments before the cursor's. The
commas of `({ })`, `([ ])`, an index and parentheses are their own; a
`;` or a block's `{` ends the search; a string or a comment is one
token, whatever is in it. The innermost call wins: in `f(g(1, |), 2)`
it is `g`'s, and after the `)` it is `f`'s again. Outside the body of a
function there are declarations and no calls.

What the callee is, is asked as `Resolve` asks it:

| the call | its signatures |
|---|---|
| `fn(` | the file's own function, else the inherited one, else the auto object's; else the kfun |
| `::fn(`, `label::fn(` | the inherited function (of that label); two for an ambiguous `::fn`, the first active; for `::fn` the auto object's, then the kfun, when none has it |
| `x->fn(` with `x` being `this_object()`, a string constant or a macro that is one | the function in that object's chain. The receiver is read from the tokens before the `->` as `calls.Extract` reads the tree of one, and the tests hold the one to the other |
| `call_other(x, "fn", ` , `call_out("fn", delay, `, a `conventions.call_registry` entry with `args:` | in the arguments that are passed on (from `args` onward): the function named, with `x` as for `->`. In the arguments before them: the registrar's own |
| `MACRO(` | the macro's name and parameters, when it has parameters |
| `x->fn(` with `x` not known, a function nobody declares, `(*f)(` | nothing: `null` |

The active parameter is the argument's own; the last one for every
argument a parameter with `...` takes (`mixed args...`); none when the
argument is past the end, sent as one past the last parameter, which is
how every client reads "none". `varargs` changes nothing: an optional
parameter is at its place. A client with `labelOffsetSupport` gets each
parameter as its offsets in the label, another as its text; one with
`activeParameterSupport` gets an active parameter for each signature
beside the one of the whole. Asked again while it shows signatures
(`context.isRetrigger`), the server keeps the one the user turned to
when the call is still the same. The documentation of a signature is
the comment and where the declaration is, as a hover has them.

**The kfun table** is `pkg/kfun`: the 106 functions of DGD 1.7.9 that a
program can call by name, in `dgd.kfuns`, a prototype a line under the
line that says what the function does. It is read out of the driver's
source and not written by hand: `TestDriverSource`, given a checkout of
the driver in `$DGD_SRC`, reads the `FUNCDEF` tables of `src/kfun` in
the order `table.cpp` includes them, takes what `KFun::kfunc` finds
(the entries past the `KF_BUILTINS` operators whose names are
identifiers, and the four names it maps back among those: `call_other`,
`call_trace`, `status`, `strlen`), decodes each prototype's bytes
(class, number of arguments that must and that may be given, types;
`C_ELLIPSIS`), takes the first sentence of the comment above the C
function, and holds the file to that; `-update` writes it. The driver
declares types and no parameter names, so the table has none: `int
call_out(string, mixed, mixed...)`. An argument the driver assigns to
(`sscanf`'s) has the type `lvalue`. One comment of the driver's says
what another function does (`asn_cmp` has `asn_sub`'s) and is left out.
An entry under a build condition would fail the test: there is none
among the functions a program can name.

What a driver has beyond stock DGD is the project's to say, not the
table's: `dialect.kfuns` in `dgdtools.yml` lists the kfuns of the
driver's extension modules as prototypes, with parameter names when the
project wants them shown; `dgdtools profile build` puts them into the
profile's manifest, so a wizard's checkout has them; a checkout's own
word of a kfun stands before the profile's, and the project's before
the table's. `examples/viking/dgdtools.yml` declares the three of that
mud's module (`rusage`, `argon2_hash`, `argon2_verify`), held to the
module's source in `$DGD_EXT_SRC` by `TestExtensionExample`.

A kfun is the meaning of a name only where that can be proved, as a
definition is: the file is in a lib, no object of its chain is unseen,
the auto object and what it inherits are seen, and the auto object has
no function of the name (a lib's auto object redefines the driver's
`clone_object` and the like, with other parameters; when it is seen and
has one, that is what the name resolves to). In the auto object itself,
and in what it is made of, the name is the kfun: `::find_object()`
there no longer resolves to the auto object's own function.

Not built: parameter names for the driver's kfuns (the driver's manual
pages have them, in a repository of their own that the driver's
checkout only links to); `->fn` on a receiver the code makes known a
line earlier (`ob = clone_object("/obj/x")`), which waits for receiver
flow as the linter does; `signatureHelp` in the arguments of a
function pointer. (A hover and a definition for locals and parameters
came with the scopes of step 7.)

**As built, step 6.** Two answers of the library's, and
`pkg/lsp/symbols.go` their protocol.

```go
for _, sym := range doc.Symbols() {   // []resolve.Symbol, in the order of the file
    sym.Kind, sym.Name                // Object (an inherit), Function, Variable, Macro
    sym.Detail                        // "static int (string what)", "private int *", "inherit side", "#define (x)"
    sym.Start, sym.End                // the whole declaration
    sym.NameStart, sym.NameEnd        // its name
}

snap := l.Index().Snapshot()
snap.Scan(ctx)                                  // the whole lib; without it, what was read so far
syms, more := snap.FindSymbols("set_sh", 200)   // []index.Symbol, the best first
sym.Kind, sym.Name, sym.Loc                     // function, variable, macro; the file and the offset of the name
sym.In, sym.Stub, sym.Score                     // "/std/room" or "/include/std.h"; from a profile's stub
```

**The outline** (`textDocument/documentSymbol`) is the declarations of
the document as the tree and the preprocessor have them, in the order
they stand:

| declaration | name | detail | kind |
|---|---|---|---|
| an `inherit` statement | the object inherited, by its lib path however the statement writes it (`inherit ROOM;` is `/std/room`); the text as written when it does not resolve | `inherit`, `private inherit`, with the label | `Class`: the inherited program is the nearest thing LPC has to one |
| a function, its definition and each prototype | its name; `operator+` for an operator | specifiers, return type and parameters: `static varargs int (int when, string why...)` | `Function` |
| a global variable, each declarator | its name | specifiers and type: `private static int *` | `Variable` |
| a `#define` the preprocessor reads | its name | `#define`, with the parameters of a macro that has them | `Constant` |

The range of a symbol is the whole declaration (a function's with its
body, a directive's line and its continuations), the selection range
the name, of an inherit statement the path. Of several declarators in
one declaration each has its own. There is one level: LPC nests no
declarations, and locals and parameters are not listed (clangd,
gopls and rust-analyzer list none either). What an included file
declares is that file's; a `#define` in a branch that is skipped is not
read and not listed; what did not parse is not there. A client that
says `hierarchicalDocumentSymbolSupport` gets `DocumentSymbol`s;
another gets `SymbolInformation`, which has no detail and no selection
range. A document with nothing in it has `[]`, one nothing is said about
`null`.

**The search** (`workspace/symbol`) is over the functions and global
variables of the lib's objects, from the units' symbol tables, and the
macros of the headers the generation has read; a macro a `.c` file
defines for itself is that file's and not searched. It needs the units
of the whole lib, which is the scan the linter's "who inherits this"
makes: `FindSymbols` searches what the generation has, and the server
runs `Snapshot.Scan` first when the lib is not scanned yet, for two
seconds at most; a scan cut short keeps what it read, so the next query
goes on from there, and a query in between is answered from what is
indexed. Once the scan is complete the list of units and header tables
is kept with the generation (and made again by the next one; the units
themselves are shared across generations as ever). There is no table of
names beside the units: a search walks their tables, 50 ms on a lib of
108,000 files.

Matching is `internal/fuzzy`, some 60 lines: the letters typed, in
their order, in either case. A name scores by how they are found: it is
what was typed, begins with it, has it, or has its letters, and one
more when the case is the case typed, so that `ROOM` finds the macro
before the function. Within a score a shorter name comes first, then
the name, then the file. That is gopls' and rust-analyzer's behavior
seen from outside (a subsequence, smart about case, the tighter match
first) without their scoring tables. An empty query finds nothing, as
in gopls: the first two hundred names of a lib are nobody's question.
The answer has at most 200 (`MaxWorkspaceSymbols`), the best.

Each symbol is a `SymbolInformation` with the range of the name, in the
text the generation reads (the buffer of an open file: a name being
typed is found as typed), and as `containerName` the object that
declares it (`/std/room`) or the header (`/include/std.h`). In a
partial checkout the scan covers the checkout, and the profile's stubs
are searched with it (`index.Layout.StubFiles`; a file of the checkout
stands before the stub of its name): what a stub declares is at the
stub's URI, and its container says `/std/room (profile NAME)`. The
libs searched are those of the open documents and of the workspace
folders, each once.

Not built: `workspaceSymbol/resolve` (a location has its range at
once); objects as symbols (an editor finds a file by its name);
several words in a query.

**As built, step 7.** Scopes in `pkg/syntax`, the answer in
`pkg/resolve`, and `pkg/lsp/completion.go` its protocol.

```go
for _, sc := range tree.Scopes() {     // []*syntax.Scope, one for each function with a body
    sc.Func, sc.Start, sc.End          // the function; the bytes of its body
    sc.Locals                          // the parameters, then the outermost block's variables
    sc.Blocks                          // the scopes of the blocks inside, each with its Locals
}
l := sc.Lookup("count", off)           // *syntax.Local: Name, Type ("string *"), Param, Catch, Ident, From
vis := sc.Visible(off)                 // what can be named at off, the innermost first, none hidden

c := doc.Complete(off)                 // *resolve.Completion, nil where there is nothing to say
c.Context                              // InCode, AfterArrow, AfterScope, InInclude, InInherit, InCallback, InDirective
c.Start, c.End                         // what an item replaces: the word, the last part of a path
c.Called                               // a '(' follows already
for _, it := range c.Items {           // the nearest first, then by name
    it.Kind, it.Name, it.Detail        // Function, "set_weight", "varargs void set_weight(int grams, ...)"
    it.Doc, it.Origin                  // a kfun's line of the table; where it is, as a hover says it
    it.Path, it.Off                    // where the declaration is: doc.Comment(path, off) is its comment
    it.Call, it.Args, it.Rank
}
```

**The scopes** are DGD's (`comp/compile.cpp`): the parameters and the
outermost block of a body are one scope, every other block one inside
it, a variable is there from the end of its name in its declarator on,
and `try { } catch (e) { }` declares `e`, a string, for its handler. The
tree has what was written, so a declaration that DGD would refuse (after
a statement, the second of a name in one scope) is in scope from where
it stands. A statement that did not parse is kept as a range of the
scope (`Scope.Unread`): what it may declare is not known, and a name it
mentions after it is not resolved. With them `Resolve` and `Hover` know
locals and parameters: the name goes to its declarator, the hover says
`int count` and "local variable, /obj/a.c:12" or "parameter, ...", and
the heuristic that kept a name a function declares anywhere from being
taken for a global is gone. A local's own declarator is a `Decl`.

**Completion** is read from the compiled tokens left of the cursor, as
signature help is, so it is there while the statement does not parse:

| where | what is offered |
|---|---|
| in a function's body | the parameters and locals in scope, the global variables (the file's and those it inherits and may name), the functions it can call (its own, those it inherits, the auto object's), the macros in view, the kfuns where `Resolver.kfun`'s rule says a name means one, and the keywords: statements and types where a statement begins, types after `(`, `object` after `new` and `<-`, `nil`, `new` and `catch` anywhere |
| where declarations stand | the types, the specifiers, `inherit`, and the macros the project or their body make a type or a specifier; after `inherit`, the macros that are a path; in the value of a global variable, what an expression can name |
| after `::`, `label::` | the functions of the inherited objects (through that label) that are not private there; after `::`, the auto object's; in the auto object and what it is made of, the kfuns |
| after `->` | for `this_object()` (or a macro of it), the object's own functions, static ones too; for a constant path or a macro of one, what a call from outside reaches: the chain's functions that are neither private nor static, and the auto object's that are not static. Anything else, nothing: an empty list, not a guess |
| in a callback's string | for `call_out`, `call_other` and the project's `call_registry`, the functions of the object it is called in, known as for `->`; the name is not called there |
| in `#include "..."`, `<...>` | the files and directories of the directory written so far, where `ResolveInclude` looks: beside the file (not for `<...>`), then in each include directory; a header first |
| in `inherit "..."` | the objects (without `.c`) and directories of that lib directory |
| in `#ifdef`, `#if`, `#elif`, `#undef` | the macros |
| in a comment, a number, a string that names nothing, code that is not compiled | `null` |

The directories are listed on disk and from a profile's stubs
(`Snapshot.IncludeEntries`, `ObjectEntries`; a stub the checkout speaks
for is not listed), so a wizard's checkout completes `/std/` from the
profile. A name is offered once: a function and a variable may share
one, a macro's name is the macro.

An item is a `CompletionItem` with the name as label and as the text of
an edit over `Start..End`, the declaration as detail, and as
documentation (Markdown when the client reads it) a kfun's line and
where it is, as the hover says it. Kinds: `Function`, `Variable` (a
global, a local, a parameter), `Constant` (a macro), `Keyword`, `File`
and `Folder`. `sortText` is the rank and the name: a local, the file's
own, what it inherits, a macro, the auto object's, a kfun, a keyword;
in a path a header, a directory, another file. The comment above a
declaration is not read for a list of hundreds: the item carries where
it is in `data`, and `completionItem/resolve` puts the comment before
the rest. To a client with `snippetSupport` a function's name comes with
its parentheses, the cursor between them when it takes arguments
(`foo($0)`, `foo()$0`), unless a `(` follows already; nothing else is a
snippet. The list is all there is (`isIncomplete: false`), which the
client filters as more is typed, unless it has more than 2,000 items
(`resolve.MaxItems`): then it is the 2,000 that `internal/fuzzy` matches
best to what is written, and incomplete. Asked for because a trigger
character was typed, the server answers only where that character
begins something (`->`, `::`, a path, a callback's string); a `>` that
compares, a `:` of `?:` or a `case`, a `/` that divides get `null`.

Not built: members after `->` on a receiver the code makes known a line
earlier (receiver flow, as for definition); a macro after `->` (`ob->QP(x)`
with `QP` expanding to a function's name); the arguments of a call
(gopls' and rust-analyzer's postfix and signature-driven completion);
labels for `goto`.

**As built, step 8.** A reverse index kept with the units of the index,
the search in `pkg/resolve`, and `pkg/lsp/references.go` its protocol.

```go
ix := index.Open(prj, fsys, index.WithUses(resolve.Extractor(registry)))  // what lint.New does
u.Uses("set_short")              // []index.Use: the places the file names it at, the form of each
units, complete := snap.UseUnits(ctx)  // the files a search looks at; complete once the scan is

refs := doc.References(ctx, off, decl)  // *resolve.References, nil where nothing declared is named
refs.Ref                         // what was asked about: At's Ref
refs.Sites                       // []resolve.Site: Path, Start, End, Line, Col, Decl
refs.Files, refs.Complete        // files searched; all the lib's (the scan was complete)
refs.Partial                     // a checkout: complete for the checkout only
refs.Possible, refs.PossibleFiles  // calls of the name on objects nobody can tell: not listed
doc.Highlights(off)              // []resolve.Site of the document, each with Access: Text, Read, Write
r.Lookup(use)                    // the declarations a Use names, in the resolver's generation
```

**The reverse index is the units' own.** `Resolver.At` was split in
two: what the file alone says of a place (`classify`: the name, the form
of a call and its receiver, the macro's `#define`, the local in scope)
and the lookup in the lib (`Lookup`: the file's function, the inherited
one, the auto object's, the chain of an object named by a path, the
global variable an inherited object declares). What the file alone says
of every place that names something another file can name too (a
function, a global variable, a macro, an object, an included file) is a
`Use`, and the index reads a unit's Uses with the unit
(`index.WithUses(resolve.Extractor(registry))`, the extractor being
`classify` over every name, string and directive word of the file), so
they are made by the whole-lib scan and go with the unit: shared by
every generation the unit is valid in, dropped with it when its file or
a header it includes changes (`Invalidate`'s `builtFrom`), and nothing
else to invalidate. What a Use names is not kept, because it depends on
other files (a function an inherited object gains or loses): a search
looks every candidate Use up in the generation that asks, which is what
At does, and is the same answer by construction. Locals, parameters and
labels are no Uses: a search for one asks At of the identifiers of its
name in the file.

A search for a name looks in every unit for the Uses of the name (a
binary search in its sorted names), looks those up, and keeps those
that name one of the declarations asked about; the files are those of
the scan (the lib's `.c` files, or the checkout's) and the headers they
include that lie in the same tree and are not excluded, each read as a
file on its own, as an editor that opens it reads it
(`Snapshot.UseUnits`, built once per generation after the scan, 130 ms
on the VikingMUD lib). An `#include` is searched by the file it
resolves to, however each writes its name. The name of a macro in an
`inherit` statement is a Use of the macro as well as of the object:
At gives `inherit ROOM;` to the room, and the references of `ROOM` would
otherwise miss most of its uses. A Use is a record of 16 bytes (28 as
first built), its name a `unique` handle and its line and column kept,
so that the server answers 60,000 places without reading their files (a
line that is not ASCII up to the place is read for its columns). On the
VikingMUD lib (110,086 files and headers) that is 4.47 million Uses; the
scan took 3.0 s instead of 2.2 s and the heap at its end was 607 MB
instead of 405, before the memory work of architecture.md, section 4.1
("Memory"), which brought what a server holds after the scan to 322 MB.

**What is found.** For a function: its definition and prototypes (with
`includeDeclaration`), and every call that reaches it: `fn()`, `&fn`,
`::fn()`, `label::fn()`, and `->fn`, `call_other` and the callbacks of
`call_out` and the registry on `this_object()` or on an object whose
path the source says, in the object, those that inherit it and anywhere
else. A redefinition in an inheritor is another declaration, and the
calls that reach it are its own. For a global variable: its uses in its
object and those that inherit it, a local of the name hiding it. For a
macro: its uses in code, in directives, in the bodies of other macros,
in headers, and in `inherit` statements. For an object: the `inherit`
statements of it and the paths handed to `clone_object` and the like.
For a header: the `#include`s of it. For a local or a parameter: its
uses in its scope. With `includeDeclaration` the declarations are among
the places, the one in a profile's stub at the stub's URI.

**What is not.** A call on an object nobody can tell (`ob->fn()`,
`call_other(ob, "fn")`, a callback whose object is unknown) proves
nothing: it is not listed, and is counted (`Possible`, by name, in how
many files) for the server's log and for rename, which reports such
calls as "possible" (section 9.3). Nothing at all where At has no
declaration: a kfun, a function nobody declares, a macro only the
configuration defines, an unknown receiver; the answer is `null`. And
whatever At says nothing of: code in a branch that is not compiled, a
statement that does not parse and the one after it.

**Partial checkout.** The scan covers the checkout, so the references
are those of the checkout's files; a declaration in a stub is found (and
listed, with `includeDeclaration`), and calls of it in the checkout are
references. `References.Partial` says that the rest of the lib was not
searched, and the server says so in its log, each time: `references to
function set_short: 3; ... complete for this checkout only (2 files
searched): the rest of the mudlib is known from a profile, which has no
code to search`. It says too how many calls on objects that cannot be
told there were, and, before the scan is complete, how many files were
searched: a first request runs the scan for as long as `scanBudget`
(2 s) allows and answers from what is indexed.

**Highlights** are the references in the document, the declarations
among them. A global variable, a local and a parameter are written
(`DocumentHighlightKind.Write`) where declared, assigned (`x = `, `x +=`,
in parentheses too), incremented or decremented, and read elsewhere;
anything else is `Text`. An element written through an index (`x[i] =`)
is a read of `x`, as gopls has it.

**Lookups that every file asks.** A search looks a name up in every
file that has it, which the lookups of At did through the flattened
chain of the file itself (`Chain(own).Unseen` before the auto object's
function, `Chain(target)` for `->fn` on a path): one chain for each
object of the lib, 850 MB after 51 searches of the VikingMUD lib. They
now ask `Snapshot.ChainFunc` and `ChainUnseen`, which say what the
flattened chain says from the object's own functions and the chains of
its parents, which a lib shares (`TestChainFuncIsChain`): 60 MB after
the same searches.

**Measured** (the VikingMUD testmud lib, 12 CPUs). `TestCorpusReferences`:
every one of 62 declarations of a sample (functions, macros, objects,
headers, a global) has exactly the places that asking At of the 10.6
million names of the 110,086 files finds (2.5 s for that); a search
takes 12 ms to 87 ms. The function called most, `add_item`: 140,161
places in 25,627 files, 4,466 calls on unknown receivers, 60 ms;
`add_exit` 62,641 places, 36 ms; `#include <mudlib.h>` 65,325 places,
36 ms; `/std/room` 25,685 `inherit`s, 22 ms. The first search of a
generation builds the list of files (130 ms) and the chains of the
parents it needs. `TestCorpusReferencesPartial` does the same in three
wizards' checkouts with the lib's profile (113, 573 and 418 files; 68
declarations, every search partial): no difference either, 2 ms at
most. Through the server (`TestCorpusSession`), the answer is the
library's for 51 functions, 171 ms at the median and 712 ms for the
140,161 places of `add_item`, most of it the JSON of the answer.

Not built: references from inside a profile's stub (a stub is shown, not
analyzed, as for definition); the places of a name in code the parser
gave up on, which rename will list as "possible"; `implementation` (the
redefinitions of a function in the objects that inherit it), which the
same index answers; a cap on the number of locations.

**For rename (steps 10 and 11).** What rename can take as it is: `At`
and `classify` for what the cursor is on; `References` for every
certain occurrence of a function, a variable, a macro, an object or a
header in the lib, with the declarations, and of a local or a parameter
in its scope; `Possible` and the Uses of form `FormUnknown` (with their
places) for the possible occurrences; `Partial` for "within the
checkout, with a warning" (9.5); `Site.Decl` to tell the declarations,
which a rename of a function must also find in the inheritors that
redefine it (the Uses of the name whose `Lookup` is the inheritor's own
definition, in objects whose chain has the symbol's object); `Lookup`
to classify any Use of the name as certain or excluded; and the
invalidation, which needs nothing more. What it needs besides: the
occurrences in code that does not parse and in strings that are no
callback (text, not Uses), the names built at run time (calls whose
function is not a constant), and the collision checks of 9.4.

**As built, step 9.** The tokens are the library's, `pkg/resolve`
(`Resolver.Tokens`, kept with the document as `lint.Document.Tokens`),
and `pkg/lsp/tokens.go` their protocol. 9a and 9b were built together,
since the scopes of step 7 were there.

```go
for _, tk := range doc.Tokens() {   // []resolve.SemanticToken, in the order of the file, none overlapping
    tk.Start, tk.End                // the bytes of the name, on one line
    tk.Type                         // resolve.TokenFunction, ...: TokenTypes[tk.Type] is the protocol's name
    tk.Mods.Names()                 // ["definition", "static"]: TokenModNames, a bit each
}
```

**The legend.** Standard types only, so that a theme colours them
unconfigured; the standard modifiers where one fits, and three of
dgdtools' own:

| Token | Type | Modifiers |
|---|---|---|
| inherit label, where declared and in `label::fn()` | `namespace` | `declaration` where declared |
| macro that the parser takes for a type (`status`) | `type` | `macro` |
| macro that it takes for specifiers, or an empty one (`public`) | `modifier` | `macro` |
| any other macro: an expression, a statement, a string, in code and in `#if`, `#ifdef`, `#undef` and other macros' bodies; and the name at its `#define` | `macro` | `declaration` at the `#define` |
| function at its prototype / its definition | `function` | `declaration` / `definition`; `static` |
| call of the object's own function (`fn()`, `&fn()`) | `function` | |
| call of an inherited one (`fn()`, `::fn()`, `label::fn()`) | `function` | `inherited`; `static` |
| call of the auto object's function, or of a kfun | `function` | `defaultLibrary`; `static` |
| `->fn` (and `call_other`'s receiver known or not) | `method` | as a call, by what the receiver's lookup finds; `defaultLibrary` for the auto object's; none for an unknown receiver |
| global variable | `variable` | `declaration` where declared; `inherited`; `static` |
| local variable, a `catch` variable among them | `variable` | `local`; `declaration` |
| parameter | `parameter` | `declaration` |
| `function` where the lib has closures | `keyword` | |

A token says what At says at its offset, and nothing where At knows
nothing of a declaration: a call of a function no file declares and no
kfun is (or a kfun the auto object may hide, as hover has it), a name
no variable of the file is, a private function of a parent, code that
is not compiled, a statement that did not parse and the names it may
declare after it. "Unresolved" is not a token: a finding says that, and
a name the tools know nothing of keeps the grammar's colour. Nor are
strings tokens (an `inherit` path, a callback's name in `call_out("fn")`,
an object path: the grammar has them as strings), nor comments,
numbers, operators and keywords but `function`. The custom modifiers
`private` and `controlFlow` of section 10.2's plan are not built (a
private function of the file is one of its own; a statement macro is a
macro), nor `deprecated` (nothing in the library knows it). Of several
declarations a name may have, a modifier is given when all have it.

**One walk.** `Tokens` is `uses` of step 8 with tokens for every name
of the compiled code: `syntax.PathsAt` once, `classify` each name, and
a `Lookup` for each name and form once (memoized in the walk); the
directives' words as in step 8. On the VikingMUD lib (108,115 files,
`TestCorpusTokens`): 5,972,708 tokens, 7 µs at the median for a file
once it is linted, 91 µs at the 95th percentile, 6 ms the slowest.

**The server.** Tokens are made in the lint that publishes the
diagnostics, for a client that says it reads them
(`textDocument.semanticTokens`), so a request finds them made (a
`sync.Once` on the document); a request whose document is not linted
in its current version lints it, as hover does, and the scheduled lint
that follows takes that lint as its own when it is of the same version,
the same session and the same scan, with no questions left for the scan
(`reuse`): an editor asks for tokens at once after each change, and the
document is not linted twice for it. `full` answers the encoded tokens
and an id, and keeps them with the document; `full/delta` from that id
answers one edit, what lies between the tokens both arrays begin with
and those both end with, whole tokens of five integers (a delta from
any other id answers all of them); `range` answers the tokens that
begin in the range, the first line counted from the top, and keeps
nothing. When the lint of a document after a change to another file (a
header's macros, a parent's functions) makes other tokens than the
client has for the same version, the client is asked to ask again
(`workspace/semanticTokens/refresh`, one at a time, to a client that
says it can be). The 20,000-line budget of 10.2 is not built: the
slowest file of the VikingMUD lib takes milliseconds. Through the
server (`TestCorpusSession`, 2,003 files of that lib): 118,875 tokens,
every one within its line; a `full` request answered in 60 µs at the
median, 330 µs at the 95th percentile, 8 ms at most; heap at the end
635 MB (709 MB in the run of step 8).

**Editors.** VS Code maps the types and modifiers to the grammar's
scopes for a theme with no rules for semantic tokens
(`semanticTokenScopes`: `entity.name.namespace.dgdlpc`,
`storage.type.dgdlpc` for a type macro and `function`,
`storage.modifier.dgdlpc`, `entity.name.function.preprocessor.dgdlpc`
for a macro, `entity.name.function.call.dgdlpc`,
`entity.name.function.definition.dgdlpc` where declared or defined,
`support.function.dgdlpc` for `defaultLibrary`,
`entity.name.function.member.dgdlpc` for a method,
`variable.other.dgdlpc`, `variable.other.local.dgdlpc`,
`variable.parameter.dgdlpc`), and declares the three custom modifiers
(`semanticTokenModifiers`). Neovim's client asks for the tokens by
itself and links the standard types to its groups; editors/README.md
has links for the modifiers. The tokens claim nothing the grammars
claim otherwise: a macro is the grammars' macro, a type macro and
`function` their type, a label their label, and where a grammar colours
by pattern (a name before `(`, an upper-case name as a macro in Vim)
the token says what the name is, and nothing where nobody knows.

**As built, step 10.** The engine is `pkg/rename`, over `pkg/resolve`,
and `pkg/lsp/rename.go` its protocol.

```go
f := rename.File{Resolver: r, AutosaveMarkers: markers}  // what lint.Document passes
t, err := rename.Prepare(ctx, f, off)  // *rename.Target, or a *rename.Refusal; nil, nil: nothing named there
t.Kind, t.Name, t.Def                  // the symbol: resolve.Local, Parameter, Function, Variable, Macro, Label
t.Start, t.End                         // the name at off, which the editor selects
plan, err := rename.Rename(ctx, f, off, "new_name")  // or a *rename.Refusal; the old name: no edits
plan.Occurrences   // every place of the old name: Path, Start, End, Class (Certain, Possible, Excluded), Decl, Reason
plan.Edits         // the Certain ones: Path, Start, End, NewText
plan.Warnings      // what does not stop the rename
doc.PrepareRename(ctx, off); doc.Rename(ctx, off, name)  // the same on a lint.Document
```

Where it is not section 9.1's plan: a symbol is named by a place in a
file (step 11 adds the forms of the command line, which come to a
`Target` the same way); an occurrence that is `Possible` makes the plan
a refusal rather than an edit to confirm, since the occurrences of
this step are those of one file and one that cannot be read is one the
rename would get wrong; there are no change annotations. Nothing else
of the shape changes for step 11: occurrences in other files, the
`Possible` ones of late-bound calls, warnings for inherited base files.

**What is renamed, and where.**

| Symbol | Occurrences |
|---|---|
| local, parameter, `catch` variable | the identifiers of its name from its declarator to the end of its scope that At gives to it (`syntax.Scope`) |
| private function of the file | its definition and prototypes, and the calls At gives to it: `fn()`, `&fn()` |
| private global variable of the file | its declaration and the uses At gives to it, a local of the name hiding it |
| inherit label | where it is given and every `label::fn()` |
| macro the file defines for itself | every identifier of the name in the file: in code compiled or not, and in `#define`, `#undef`, `#if`, `#ifdef`, `#ifndef`, `#elif` (other macros' bodies and parameters among them) |

Each identifier of the old name where the symbol may be is asked of At:
the symbol (certain); something else, another local, `::fn()`, a
variable named like a function (excluded); in a branch of `#if` that is
not compiled, in a statement that does not parse, in a statement read
after one that did not (`BadStmt.Resumed`), in the arguments of a macro
that are no expressions (possible: refused). A macro is renamed as a
whole: neither name is known outside the file (no header it includes
names either, nothing predefines or configures them, neither is a
function, a variable or a kfun of the object), the new one is nowhere
in the file, and every identifier of the old one in the file's text
becomes the new one, so the file means after the rename what it meant
before, whatever branch is taken.

**DGD and private functions.** call_other, `->`, call_out and every
callback a lib's registrar makes find a function by its name in the
object's table of functions (`Frame::call`, `ctrl->symb`), and that
table has no private function (`Control::makeSymbols`: `if (f->sclass &
C_PRIVATE) continue; /* not in symbol table */`; a private
redefinition of an inherited function leaves the inherited one there).
call_out reaches static functions of its own object (`call_static`),
never private ones. So `call_out("fn")`, `this_object()->fn()` and
`store_fp("fn")` do not call a private `fn`: they are excluded from its
rename, left as they are, and named in a warning (the server shows it),
since such a call fails today; `prepareRename` on one of them says so.
At (and so definition, hover and references) still goes from such a
string to the private function, which tells the reader why the call
fails; the linter reports the `->` and `call_other` forms
(`static-crossobj`) and not yet the callbacks (step 11, or a rule of its
own). On the VikingMUD lib the corpus test found such callbacks, among them
`store_fp("wield_check")` in `/players/bladerunner/obj/weapon/str_sword`
and `call_out("wander_again")` in `/players/harald/wander`: they name
private functions, and never call them.

**Refusals.** Of the place: a name in code not compiled, in code that
does not parse, a name nothing is known of. Of the symbol (Prepare):

- a file (an `#include`, an `inherit` path, an object path): "renaming
  files comes in a later version";
- a public or static function, a global variable that is not private,
  what is inherited, the auto object's function, a declaration in a
  header the file includes as code, a header's macro: "rename across
  files comes in a later version";
- a kfun; a function no file declares; a call on an object that cannot
  be told; a name two inherited objects have;
- a declaration in a profile's stub: "defined in the mudlib profile,
  not in your checkout";
- a macro the driver or the configuration defines, or that a header
  defines too, or that a header the file includes names, or that is
  `#undef`'d or used before its `#define` where the name is also an
  inherited function or variable, the auto object's or a kfun;
- the string of a late-bound call naming a private function (above);
- a file another file includes (`Snapshot.Includers`): what it
  declares is theirs too;
- a private global variable that is not static, when `save_object`
  writes it: the file, an object it inherits, an object that inherits
  it or one of those objects' parents names `save_object`,
  `restore_object`, one of `conventions.autosave_markers`, or a macro
  of the file that expands to one (the units' Uses, so the index
  answers without reading the files; the auto object is taken to wrap
  them, as the VikingMUD lib's does); in a partial checkout, when the
  profile has a stub of the file, whose inheritors are out of sight;
- the lib not indexed in full yet (the scan runs for as long as the
  request allows, `scanBudget` in the server), or files changed while
  the plan was made.

Of the new name (Rename): not a name; a keyword; a macro here (defined
by a file, predefined, configured). For a local: declared in the same
scope; a declaration between would hide the renamed one at a use; a
name the renamed variable would capture where it is in scope (a global,
a local of an outer scope, a name nothing declares). For a private
global: the object has a variable of the name, or inherits one; a local
of the name where the variable is used; a use of the name the variable
would capture. For a private function: the object has a function of the
name, inherits one, has one from the auto object, a kfun of the name, a
call of the name in the file. For a label: another inherit has it. For
a macro: the name in the file already, in a header it includes, or a
function, variable or kfun of the object. For all of them: the new name
in code around the symbol that is not compiled or does not parse; a
macro used where the symbol is that names the old name (the macro
would have to change) or the new one (it would mean the renamed symbol)
in its body; an occurrence that is an argument of a macro that makes a
string of its arguments or pastes them (`#`, `##`).

**The server.** `renameProvider` is `{prepareProvider: true}` to a
client that says `prepareSupport`, else `true`. `prepareRename` answers
the range of the name and the placeholder, `null` where nothing is
named, and an error of code `RequestFailed` (-32803) with the reason,
which VS Code and Neovim show as it is. `rename` answers one
`TextDocumentEdit` for the version it was made from (a client that does
not read `documentChanges` gets `changes`), or the error, and shows the
plan's warnings (`window/showMessage`). Both make the whole-lib scan
first for a symbol other files could name, as references does.

**Measured** (the VikingMUD testmud lib, `TestCorpusRename`, 842 files:
400 of the lib, evenly, and 442 of the 1,766 that declare a private
function or variable). 38,324 symbols asked about; 2,734 plans made,
84 to 90 µs each on average; renamed 668 locals, 1,001 parameters,
249 private functions, 431 private variables, 205 macros and 139
labels, and left 6 strings of callbacks that name a private function.
Each was linted renamed: its diagnostics are those of the file, but for
the name, and for what the linter says of a string left that names a
private function; the references of the new name are those of the old
one but for those strings. Refused, of the symbol: 6,327 inherited,
6,097 public functions, 4,737 the auto object's, 2,656 header macros,
941 static functions, 770 global variables that are not private, 712
files, 70 saved variables, 16 in files another includes, 14 declared in
an included file, 13 macros defined outside the file too, 1 macro a
header uses; of the plan, 35 for code that is not compiled or does not
parse, 5 for a macro whose body names the old name, 1 for a stringizing
macro.

Tests: `pkg/rename` (each kind; each refusal; the clashes of each
kind; code not compiled, not parsed, opaque macro arguments; strings
of callbacks; a partial checkout; a lib not scanned),
`TestRenameIsTheLibrarys` (every offset of a lib, in both encodings:
prepareRename and rename answer what the library does), the transcripts
`rename` and `rename-plain`, `TestNeovimRename` (`vim.lsp.buf.rename`
on a local and a private function, and the reason for a public one),
and `TestCorpusRename`.

**For step 11.** What it adds to the engine: the occurrences in other
files, from the reverse index (`References` and the Uses of the name,
`Lookup` to classify each); the late-bound calls as `Possible` when the
receiver is not known, and so a plan that has them, with change
annotations in the editor; the redefinitions of a function in the
inheritors, which move with it; the strings of callbacks for a
function that is not private, certain when the receiver is known;
warnings for inherited base files and for a partial checkout; the
command line (`dgdtools rename SYMBOL NEW`, a symbol written out as
`/std/living::query_name`, `/include/std.h::ROOM` or `FILE:LINE:COL`)
and `--force` for a saved variable; a header's macro, whose files are
the includers (`Snapshot.Includers`). The checks of the new name stay
those of this step, made in each file an edit lands in.

**As built, step 11.** The engine is still `pkg/rename` (`across.go`
beside step 10's files), the command line `cmd/dgdtools/rename.go`, the
server `pkg/lsp/rename.go`.

```go
f := doc.RenameFile(force)             // rename.File{Resolver, AutosaveMarkers, Create, Force}
t, err := rename.Prepare(ctx, f, off)  // t.Across: other files name it
plan, err := rename.Rename(ctx, f, off, "new_name")
plan.Occurrences   // in every file now: Path, Start, End, Line, Col, Class, Decl, Reason
plan.Edits         // the Certain ones
plan.PossibleEdits // the Possible ones, made only when asked for
plan.Warnings      // forced saved variable, base files and the reboot, partial checkout, private callbacks
plan.Dynamic       // calls whose function's name is made at run time: Path, Line, Col, Pattern ("do_*"), Matches
plan.Files, plan.Partial
plan.Count(rename.Possible)
doc.Rename(ctx, off, name, force)      // lint.Document's
```

Where it is not section 9's plan: a symbol is still named by a place in
a file, and the command line comes to one (below); a plan with possible
occurrences is a plan, not a refusal, for a symbol across files (one
file of 110,000 that does not parse would otherwise stop every rename
of the lib), and stays a refusal for what one file decides, as step 10
made it; the command's flags are `--write` (not `--apply`) and
`--include-possible` (not `--possible`), and its diff goes to standard
output with the summary on standard error, as 9.6 says.

**What is renamed across files.** Prepare decides it: a function that
is not private, a private function of a file other files include, a
global variable that is not private or is declared in such a file, a
header's macro or a macro of a file another includes. Everything of
step 10 stays as it was, and its refusals of the symbol that said "a
later version" are gone but for files and labels.

| Symbol | Declarations renamed | Occurrences |
|---|---|---|
| function | its family: the declarations joined where an object declares the name and what it inherits (the chain of each inherit statement) has a function of it, or nothing does and the auto object has; and the declarations of the name in the files a program is made of (a prototype in a header, the definition in a file included as code, each file read on its own) | every Use of the name in every unit of the lib (`Snapshot.UseUnits`) whose `Lookup` is one of them |
| private function of an included file | its declaration | the Uses that look up to it; `call_out("fn")`, `->fn` on `this_object()` excluded, as in step 10 |
| global variable | its declaration | the Uses that look up to it, in its object and its inheritors |
| header's macro | its `#define` | the Uses that look up to it in every file; the files that include the header, and what they include, read again for the rest |

**The family.** A call in an inherited program reaches the
redefinition (DGD calls a function by name in the object's table, which
has the latest definition), so a function and the functions that
redefine it are one function by the driver's rules, and renaming one
of them alone changes what the lib does. The rename takes all of them,
up the hierarchy to where the name is first declared and down to every
redefinition, siblings' included, as IntelliJ renames a method with
its overrides after asking; there is no question here, the family is
the symbol. A private function redefines nothing (it is in no table),
and joins nothing. A family with a declaration in a profile's stub is
refused: "redefines, or is redefined by, the function of
/std/room.c, which is defined in the mudlib profile". The auto object's
function is a family like another: every object that defines one of
the name joins it.

**Certain, possible, excluded.** Each Use of the old name is looked up
where it is (`Resolver.Lookup`, the unit's own resolver):

- **certain**: it looks up to one of the declarations renamed, and to
  nothing else: definitions and prototypes, `fn()`, `&fn`, `::fn()`,
  `label::fn()`, `->fn` and `call_other` on `this_object()` or on an
  object whose path the source says, and the callbacks of `call_out`
  and of `conventions.call_registry`, with a known object;
- **possible**: a call on an object nobody can tell (`FormUnknown`); a
  callback or `->fn` in `this_object()` where the object has no
  function of the name (an inheritor may have it); a call in an object
  whose chain has a file that cannot be found, or in a virtual object;
  a Use in a header read on its own that names nothing there; a name
  more than one inherited object has; and in the files read again,
  the name in code that is not compiled or does not parse and in the
  body of a `#define`, where what it names depends on where the macro
  is used;
- **excluded**: it looks up to another declaration ("names the function
  'query_name' (/obj/other.c)"), or to nothing in an object whose chain
  is complete, or is a call in an object that does not exist; a local
  where the index has no Use is said nothing of.

Which files are read again (`Snapshot.Parse`, a resolver and a renamer
each, in parallel): those with a certain occurrence; the headers, the
objects that have the symbol and those that inherit them whose text has
either name; for a macro, the header's includers and their includes
whose text has either name. There the old name where the index has no
Use is classified as above, and the new name is checked: not a macro
there; no call of it that names nothing and would reach the renamed
function, in an object that sees it; no local that would hide the
renamed variable where it is used, no name it would capture; not an
argument of a macro that stringizes; for a macro, the new name nowhere
in a file it reaches. Over the lib: no object that has a declaration of
the family, or inherits one, has a function (a variable) of the new
name already (`ChainFunc`, and `index.Snapshot.ChainVar`, which finds a
variable as ChainFunc finds a function); not a kfun; not the auto
object's.

**Refusals and warnings.** Of the symbol, besides step 10's: the create
function (`create` in the configuration, which the driver, or the auto
object, calls by name); a function whose name is also a kfun (a call
the rename cannot see would reach the driver's); a family with a stub.
A variable `save_object` writes is refused with `Refusal.Forcible`:
`File.Force` (`--force`) makes it a warning instead ("renamed although
...: convert the save files"); the editor's error says that `dgdtools
rename --force` renames it. The programs looked at for `save_object`
are found by the inherit statements now, not by flattening every
inheritor's chain. Warnings: the edited files other objects inherit or
include, the three most used by count ("edits in base files:
/std/mod/desc.c (86625 objects inherit it), ...; an object keeps the
program it was compiled with, so the rename needs the scheduled reboot
to take effect everywhere"); a partial checkout ("the places outside it
that name 'x' are not renamed, and cannot be seen from here"); the
strings and `->` that name a private function, as in step 10.

**Names made at run time.** `calls.Facts.Dynamic` are the calls of the
registry whose function argument is no string constant, with the
constant parts of a sum of strings (`"do_" + verb`: prefix `do_`).
The extractor keeps each as a Use of kind `index.UseDynamic`, under the
name `index.DynamicName` (no identifier, so no search for a name meets
one), its `Aux` the pattern `do_*`. A function's plan lists them all,
those whose prefix the old name begins with or whose suffix it ends with
first; the command and the server show those and count the rest. On the
VikingMUD lib: 1,218 such calls, 8 of them `add_*`.

**The command line.** `dgdtools rename [flags] SYMBOL NEW`, `SYMBOL`
one of three forms that cannot be taken for each other:
`/std/living::query_name` (a function or a global variable of the
object, its own or inherited, by `ChainFunc` and `ChainVar`; the path a
mudlib path, `.c` optional; a macro of the object's file when it has
neither), `/include/std.h::ROOM` (a macro of a header by its mudlib
path, found in the header's own `#define`s), `FILE:LINE:COL` (a path on
disk, line and byte column from 1 as `dgdtools lint` writes them). A
function and a variable of one name in one object are an error that
asks for the third form. The plan is printed as unified diffs (the
format command's), each owner's under a line `# players/ann: 3 files`:
an owner is the tree of a checkout pattern (`profile_build.checkouts`,
or the profile's, else `players/*` and `d/*`), "other" for the rest,
last. The summary on standard error: the symbol and where it is
declared, the counts of each class and the files searched, files and
edits by owner, the first 20 possible places with their reasons, the
warnings, the dynamic calls. `--write` writes each file as the format
command does (beside it, renamed over it); `--include-possible`;
`--force`; `--output-format json` (the symbol, the counts, `complete`,
`written`, the files with their owners and edits, every occurrence with
its class and reason, the warnings, the dynamic calls); `-q`. Exit 0
complete, 1 possible places left, 2 usage or refusal, 130 interrupted.
The command reserved the name `rename` until now; the mechanism of
reserved names went with it.

**The server.** `rename` answers one `TextDocumentEdit` per file, in
`documentChanges` (`OptionalVersionedTextDocumentIdentifier`: an open
document's version, null for a file the client has not open, whose text
is the disk's), or `changes`. The possible edits go to a client that
says `changeAnnotationSupport` with `documentChanges`: every edit is
then an `AnnotatedTextEdit`, the certain ones under `certain`, the
possible ones under one annotation per reason ("Possible: a call on an
object nobody can tell, which may be one that has it"), with
`needsConfirmation`, so that VS Code's preview shows them by reason,
unticked. Another client (Neovim's) gets the certain edits only. One
`window/showMessage` then says what is to know: the possible places
(in the preview, not ticked; or not renamed, the first five by file and
line, and `dgdtools rename --include-possible`), the warnings, the
dynamic calls that may call the function and how many others there
are; nothing when there is nothing to say. A document that changed
while the plan was made is `ContentModified`. `prepareRename` answers
for a symbol across files as for one in a file, after the whole-lib
scan (`scanFor`).

**Measured** (the VikingMUD testmud lib, 110,086 files, 12 CPUs).
`TestCorpusRenameAcross` asks for 35 symbols other files name (the eight
functions called most, every so many of the lib's public functions,
global variables that are not private and header macros) and renames 31, each to a
name of the same length the lib does not use; makes the certain edits
in an overlay of the lib and lints the whole lib again (34,140 files
with diagnostics): every file's diagnostics are those of before, but
for the new name in their messages and the lines of the possible places
left; the references of the renamed declarations are the certain
places, before and after (for families of up to 40 declarations). The
largest plans, after the scan (3 s):

| symbol | certain | possible | excluded | files edited | plan |
|---|---:|---:|---:|---:|---:|
| `add_item` (`/std/mod/desc.c`, its family) | 141,153 | 9,840 | 360 | 25,776 | 0.85 s |
| `write` (the auto object's and every redefinition) | 117,263 | 14,185 | 23 | 20,653 | 1.17 s |
| `this_player` (`/dgd/lib/runtime.c`) | 94,117 | 15,581 | 47 | 18,079 | 1.02 s |
| `set_short` (46 declarations) | 58,970 | 10,878 | 194 | 57,058 | 0.89 s |
| `set_long` (60 declarations) | 58,556 | 11,282 | 193 | 56,251 | 2.6 s |
| `environment` | 51,248 | 5,388 | 11 | 13,495 | 1.06 s |
| `extra_look` (559 declarations) | 586 | 56 | 912 | 536 | 0.20 s |

A global variable or a macro of a header plans in 15 to 120 ms.
Refused: `random` (a kfun too), `create` twice, `_tuned_cost` (saved:
an object that inherits it calls `restore_object`). The test found one
thing to mend: in the auto object, made of files included as code,
`this_player` is declared in `runtime.h`, defined in `runtime.c` and
called in `override.c`, each file read on its own; the declarations of
the files a program is made of are now one function
(`TestRenameProgramOfFiles`). `dgdtools rename /std/room::add_item
add_detail` on the same lib: 5.1 s from start to the end of 634,000
lines of diff (the scan most of it), 1.2 GB resident. Step 10's
`TestCorpusRename` renames what one file decides as before (the same
2,693 symbols, 67 µs a plan), and leaves the 15,033 symbols across files
it meets to this test; `Prepare` of one of those takes 7 ms on average
(the family is found from every object's own functions).

Tests: `across_test.go` (`TestRenameFunctionAcross`: the family up and
down, every form of call, another function of the name, the possible
calls, the macro's body, the code not compiled, the dynamic calls, the
possible edits; `TestRenameVariableAcross`: inheritors, a local that
hides, a stringizing macro, a saved variable refused and forced;
`TestClashesAcross`: each check of the new name across files;
`TestRenameMacroAcross`; `TestRenamePrivateIncluded`;
`TestRenamePartialAcross`: a checkout's public function with the
warning, a redefinition of the profile's refused), step 10's tests with
what is now renamed, `TestDynamic` (`pkg/calls`), `TestDynamicUses`
(`pkg/resolve`), `ChainVar` in `TestChainFuncIsChain`; in `pkg/lsp`
`TestRenameIsTheLibrarys` (every offset, both encodings, the edits of
every file and the annotations what the library says) and the
transcripts `rename-across` (versions, annotations, the message, a
refusal that can be forced) and `rename-across-plain`; in
`cmd/dgdtools` `TestRenameDiff`, `TestRenameWrite`, `TestRenameForce`,
`TestRenameJSON`, `TestRenameErrors`; in `editors` `TestNeovimRename`
(a public function, and the other file's buffer edited); on the corpus
`TestCorpusRenameAcross`.

**For step 12.** What is not built: renaming a file
(`workspace/willRenameFiles`, and the refusal "renaming files comes in
a later version" with it); an inherit label of a file others include;
the occurrences in strings that are no callback and in comments, which
are not listed (neither renamed nor said); the new name's checks in the
bodies of macros used where an edit lands; files that have the old name
only in dead code and neither see the symbol nor are read again for
another reason; the driver object's functions, which the driver calls
by name like `create`, are not refused; a function of the auto object
renamed reaches every object, which is correct and slow to check.

**As built, step 12.** The refactorings: a file moved, in the editor
(`workspace/willRenameFiles`) and on the command line (`dgdtools rename
FILE NEW`), whose engine is `rename.Files` (`pkg/rename/files.go`); two
code actions, `lint.Document.Prototype` and `Document.PathFixes`
(`pkg/lint/actions.go`); and what was left of step 11 that is in
proportion.

```go
plan, err := rename.Files(ctx, snap, []rename.Move{{From: old, To: new}})  // paths on disk; a directory moves with its files
plan.Moves        // the files moved, a directory's each
plan.Edits        // of the files as they are, before the move (an edit of a moved file is at its old path)
plan.Occurrences  // Certain: an edit; Possible: a path made by a macro or an expression, not edited
plan.Warnings     // moved base files other objects inherit, a partial checkout
fix := doc.Prototype(off)        // *diag.Fix, Safe; nil where there is nothing to declare
fixes := doc.PathFixes(ctx, f)   // []diag.Fix, Unsafe, for include-not-found and target-object-missing
```

**A file moved.** The places that name a moved file are the Uses of
kind `UseObject` and `UseInclude` the reverse index already has: every
`inherit` path, every `#include`, every object path handed to a
function that takes one (`clone_object`, a registrar's) in every unit
of the lib; the calls made in a moved object by its path
(`"/std/room"->fn()`, `call_other("/std/room", "fn")`: the Uses of kind
`UseFunction`, form `FormPath`, whose Aux is the object,
`index.Unit.UsesWith`), whose receiver is found in the file's tree; and
the moved files' own Uses of those kinds, which name a file from where
they were. Each is rewritten
as it was written, and the new text is resolved as the driver will
once the files have moved (`index.Snapshot.ResolveIncludeIf`, which is
`ResolveInclude` in a lib whose files a function says, and
`ObjectPath`); a text that does not resolve to the moved file is not
made. A text that still names the file after the move (a relative
`#include` of two files moved together) is left alone.

| As written | Rewritten |
|---|---|
| `"/std/room"`, `"/std/room.c"` | from the lib's root, `.c` kept or not |
| `"std/room"` | relative as the lib reads it (`relative_object_paths`): from the root, else from the file |
| `#include "room.h"` beside the file | relative when the new place is below the file's directory; else from an include directory that has it; else from the root |
| `#include "../std/room.h"` | as above, and up the tree when no shorter way resolves, as it climbed |
| `#include <sub/x.h>` | from the include directory that has the new place; else `"/path"` from the root |
| `"/std/room"->fn()`, `call_other("/std/room", ...)` | the receiver, as an object's path |
| `ROOM`, `DIR + "room"` | not edited: Possible, "names /std/room.c by the macro ROOM, which other files may use too" |

A macro's `#define` is not edited: its string is every user's, and a
user that adds to it (`ROOM + "_extra"`) names another file. A path in
code that is not compiled, in a string no function takes as a path, or
in a comment is not a Use and is not seen, nor is a header the
configuration excludes (an attic's) that a file of the lib includes: it
is no unit of the search. A file moved out of the lib
leaves its places Possible. Warnings: the moved files other objects
inherit, with their counts (an object keeps the program it was compiled
with), and a partial checkout's places outside it.

**The server.** `workspace.fileOperations.willRename` with two filters,
`**/*.{c,h}` for files and `**` for folders (scheme `file`). The answer
is a `WorkspaceEdit` of the files as they are, in `documentChanges` with
an open document's version (the WorkspaceEdit of rename, which both now
make with `workspaceEdit`); the possible places, the warnings, or why
nothing could be planned (the scan not done within `scanBudget`) go to
`window/showMessage`. A refusal is null and a message, never an error:
the editor's rename goes ahead whatever the server says, so an error
would only be noise. VS Code's client sends the request for a file or a
folder renamed in its explorer; Neovim's core does not (its
`vim.lsp.util.rename` leaves `willRename` off), and file explorer
plugins (oil.nvim, nvim-tree) do, as `TestNeovimRefactor` does by
hand. `prepareRename` on an `#include` or an `inherit` path says to
rename the file itself.

**The command line.** `dgdtools rename FILE NEW`, `FILE` a mudlib path
with the extension of a source file (`/std/room.c`, `/include/std.h`;
no `::` and no `:LINE:COL`, so it cannot be taken for the other forms),
`NEW` a mudlib path or a name for the same directory. The diffs and the
summary of the symbol forms ("move /std/room.c to /std/place.c", the
possible places with their reasons); `--write` writes the edits and
moves the file last, making its directory; exit 1 when a possible place
is left (there is no `--include-possible` text to make for one), 2 when
`FILE` is not there or `NEW` is. Moving a directory from the command
line is not offered: `git mv` and an editor do that, and the editor
asks the server.

**Add the prototype.** DGD compiles a call to a function it knows
nothing of yet as a call to an undefined one (`Control::funCall`: an
implicit prototype), which typechecking, on wherever `typechecking` is
1 or 2, refuses ("undefined function"); a prototype before the call
makes it a call to the function. `Document.Prototype(off)` at a call
`fn()` of a function the file defines after the call, when nothing
declares the name before it (no inherited object, the auto object, a
header the file includes, a kfun; an unresolved inherit or a header
that does not parse is taken to), is the definition's declaration as
written (`FuncDeclText`) and `;`, at the start of the line of the
top-level declaration the first call is in, above the comments right
over it (no blank line between). Not offered for a function without a
return type or with an untyped parameter (no prototype can be written),
one whose declaration names a macro defined after that place, or has a
directive in it. It is `Safe` (the code means what it meant), and a
`refactor.rewrite` action at the start of the range asked about, not a
quick fix: the linter has no rule for a call before the definition, and
none was added (it would be a part of the undefined-function rule of
section 12, item 5).

**Fix an #include path.** `Document.PathFixes(ctx, f)` for a finding of
`include-not-found`, and of `target-object-missing` whose path is one
string as written (an `inherit`, a path handed to `clone_object`): the
one file of the lib with the missing file's base name
(`index.Snapshot.FilesNamed`: under the include directories and the
file's own tree, its directory's parent or its directory at the lib's
top, on disk and in a profile's stubs; and among the files the index
has read, the scan's), written as the path was (as for a file moved)
and resolved to that file. None when no file, or more than one, has the
name: a choice is the person's. `Unsafe` (the file found may not be the
one meant), so a quick fix that is not preferred and not in "fix all".
It is made when asked for, as the suppression comments are, not with
the lint: the search reads directories, and a lint of the whole lib has
thousands of missing files.

**What was left of step 11.** Done: the driver object's functions that
DGD calls by name (`DGD::callDriver`, `Frame::callCritical`:
`initialize`, `path_read`, `inherit_program`, `compile_error`,
`runtime_error`, `touch` and the rest), with the redefinitions that are
one function with them, are refused as `create` is; the driver object
is the new `driver_object` of the configuration (read from the driver
config's `driver_object`, carried by a profile). The functions the
driver calls by name in a connection's object (`open`, `close`,
`receive_message`, `message_done`, `unconnected`, `receive_datagram`,
`datagram_attach`) are renamed with a warning: which objects are
connections, the lib decides, and refusing `open` and `close` in every
object would refuse a door's. Across files, a file an edit lands in
that sees the symbol is checked for the macros its code uses whose
bodies name the new name, as step 10 checks its one file. Not done, and
why:

- Occurrences in comments and in strings that are no callback or path:
  not renamed and not listed, as rust-analyzer and gopls do by default;
  a comment's word is no reference, and listing every one would bury
  the possible places that matter.
- An inherit label of a file other files include: still refused. On the
  VikingMUD lib no header has a labelled inherit.
- Files that have the old name only in code that is not compiled, and
  neither see the symbol nor are read again for another reason: the
  index keeps no Uses of such code, and finding them means reading the
  whole lib's text for every rename.
- The auto object's function renamed reaches every object; it is
  correct, and as slow as it was.
- A private callback named in `call_out` whose function is private: no
  rule yet (the owner's decision is pending).

**Measured** (the VikingMUD testmud lib, 110,086 files, 12 CPUs).
`TestCorpusRenameFiles` moves 13 files and directories, each in turn
renamed in its directory and moved to another (`/qq_moved/...`): the
three objects most inherited, the three headers most included, three
objects that `#include` a file beside them, three small directories. It
makes the edits and the moves in an overlay that can hide a file, lints
the whole lib again (34,140 files with diagnostics) and holds every
file's diagnostics to what they were, but for the moved paths in their
messages and the columns of the edited lines; the files with a possible
place, and what inherits or includes them, are left out and counted.
Every file compared is unchanged.

| moved | edits | possible | left out | plan |
|---|---:|---:|---:|---:|
| `/std/include/mudlib.h` to `/qq_moved/...` | 65,325 | 0 | 1 | 1.07 s |
| `/std/include/levels.h` to `q_levels.h` | 4,776 | 0 | 1 | 0.20 s |
| `/std/include/std.h` to `/qq_moved/...` | 3,190 | 0 | 1 | 0.16 s |
| `/std/mod/move.c` to `q_move.c` | 8 | 4 | 4 | 0.14 s |
| `/std/mod/destroy.c` to `/qq_moved/...` | 5 | 7 | 86 | 0.14 s |
| `/std/mod/hook.c` to `q_hook.c` | 5 | 12 | 86,746 | 0.09 s |
| a directory of 3 objects | 0 to 3 | 0 to 10 | 0 to 4 | 0.22 to 0.26 s |

The base objects are mostly inherited by macro (`inherit HOOK;`), so
their places are possible and listed; `hook.c`'s possible places include
a call by macro in a file most objects inherit, which leaves most of the
lib out of the comparison. The one file left out for a header includes
a header in an attic the configuration excludes, whose own `#include`
nothing searches. The test found two things to mend: a call made in an
object by its path (`"/obj/x"->fn()`) names the file, and is now
rewritten (`Unit.UsesWith`); an edit that lengthens a path moves the
columns of what follows on its line, which the test now allows. With the check of the macros used where an edit lands,
`TestCorpusRenameAcross` renames the same 31 symbols of 35 as before
and refuses the same four, the plans as fast (`add_item` 0.78 s).

Tests: `TestFiles` (`pkg/rename`: each way of writing a path, a header
to an include directory, a header in one, a directory, out of the lib,
not of the lib), `TestRenameDriverCalls`, `TestClashThroughMacroAcross`;
`TestPrototype` and `TestPathFixes` (`pkg/lint`); the transcripts
`move` (an open document and files not open, a folder moved after a
file, the message) and `refactor` (the path fix and the prototype), and
both kinds in every transcript's capabilities; `TestRenameMove`
(`cmd/dgdtools`); `TestNeovimRefactor` (`editors`: the prototype by
`vim.lsp.buf.code_action`, a file moved as a file explorer plugin moves
one); `TestCorpusRenameFiles` on the corpus.

Steps 1 to 3 (9 days) are the "useful every day" milestone: live
diagnostics, fixes, formatting. Steps 4 to 6 (9 days) make it a
navigation tool. Steps 7 to 9 (13 days) need two pieces of library work
that are also on the linter's own roadmap: local scopes and a kfun table.
Steps 10 to 12 (15 days) are the editing tools, and stand on the scopes
of step 7 and the reverse index of step 8.

Library additions, all testable without a server: `syntax.PathAt`
(built, with `token.File.TokenAt`),
block scopes (built, `syntax.Scope`), kfun table (name, parameters, return type; built,
`pkg/kfun`), reverse
reference index (built, `index.Use`), `pkg/rename` (built, in a file and across
files). A lint already returns the parsed tree,
tokens, `#define` environment, index unit and snapshot with the
diagnostics.

## 9. Rename and refactorings

A mud renames a function the way it always has: mud-wide, in one sweep,
with grep and an editor macro, and the old name is gone that day. There
is no deprecation cycle in that idiom, and none is planned here: no
forwarding stubs, no list of deprecated names, no two-step migration.
What the tool adds is the judgment grep lacks: for every place the name
occurs, whether it is the symbol being renamed, might be, or is not.

### 9.1 One engine, two front ends

`pkg/rename` is a library like the rest: given a snapshot of the index,
a symbol and a new name, it returns a plan. Nothing in it writes a file.

```go
plan, err := rename.Plan(ctx, snapshot, rename.Symbol{...}, "query_short_name")
plan.Occurrences   // every place the old name stands: location, class, reason
plan.Edits         // the text edits, each tied to its occurrence
plan.Warnings      // saved variable, inherited base file, names built at run time
plan.Refusal       // set when the rename must not be made as asked
```

- **The editor**: `textDocument/prepareRename` and `textDocument/rename`
  call it with the symbol under the cursor.
- **The command line**: `dgdtools rename SYMBOL NEW`, the subcommand
  whose name the dispatcher reserves, calls it with a symbol written
  out: `/std/living::query_name` (a function or a global variable of
  that object), `/include/std.h::ROOM` (a macro of that header), or
  `FILE:LINE:COL` (whatever stands there, which is how a local is
  named).

Both show the same plan, so a rename previewed in an editor and one made
by an arch from a shell cannot differ.

### 9.2 What can be renamed, and how exactly

| Symbol | Occurrences | Exact when |
|---|---|---|
| local variable, parameter | its block scope | always (needs the scopes of step 7) |
| private function, private variable | the declaring file, and files that `#include` it as code | always |
| macro | the `#define`, and every use in a file whose `pp.Env` takes the name from that table, uses inside other macros' bodies included | the files that include the header are all present |
| object global (not private) | the declaring file and every object that inherits it | the inheritors are all present (the whole-lib scan) |
| function (not private) | the definition and its prototypes; redefinitions in inheritors, which are the same name by the driver's rules and move with it; direct calls and `::fn()`, `label::fn()` calls in the object and its inheritors; late-bound calls (below) | the inheritors are all present and no late-bound call has an unknown receiver |

Late-bound calls are where grep and proof part ways. `ob->fn()`,
`call_other(ob, "fn")`, and the string callbacks of `call_out`,
`add_action` and whatever else `conventions.call_registry` declares name
a function by text. `pkg/calls` already reads them, and the linter's
receiver resolution already says which object each one reaches, or that
it cannot tell. The engine renames what is provable and never claims
more: a rename is called complete only when no occurrence is left
`possible`.

### 9.3 Certain, possible, excluded

Every textual occurrence of the old name in the files the snapshot has,
as an identifier or as a string constant, gets one of three classes and
a reason:

- **certain**: resolved to the symbol. The definition and prototypes;
  direct calls and `::` calls in the object and its inheritors;
  redefinitions in inheritors; `->fn` and `call_other` with a receiver
  the linter knows (a string constant, a path macro, `this_object()`)
  whose chain takes the function from the symbol's object; a string
  callback whose registry form says in which object, when that object is
  known.
- **possible**: could be the symbol, and nobody can say. `ob->fn()` and
  `call_other(ob, "fn")` with an unknown receiver; a string callback
  whose object is unknown; an occurrence inside a statement the parser
  could not read or in a conditional branch chosen by guess.
- **excluded**: provably something else. A function of the same name in
  an object whose chain does not contain the symbol's object; a call
  whose known receiver resolves elsewhere; a local or parameter that
  shadows the name; the name inside a comment, inside a longer
  identifier, or in a string that is no callback.

Two things are reported beside the classes, because they are not
occurrences at all:

- **Names built at run time.** A late-bound call whose function argument
  is not a constant (`"do_" + verb`, `call_other(ob, fn)`) cannot be
  searched for. The plan lists those call sites as unresolvable, the
  ones whose constant part the old name begins or ends with first.
- **Receivers nobody resolved.** The count of `possible` occurrences by
  file, so the person renaming sees how much of the answer is unknown.

### 9.4 Warnings and refusals

- **A variable that `save_object` writes is refused.** A global that is
  not `static` is stored under its name in every save file of an object
  that saves itself; rename it and `restore_object` no longer finds the
  value, which is data lost at the next reboot with nothing in any log.
  The engine refuses when the variable's object or one of its inheritors
  calls `save_object` or carries one of `conventions.autosave_markers`,
  and also when the inheritors cannot all be seen. `--force` on the
  command line overrides it; the editor says why and points there.
- **An edit to an inherited base file is flagged.** An object keeps the
  program it was compiled with until it is compiled again, and on a
  running mud the objects that inherit a base file are compiled again at
  the scheduled reboot. A rename that touches a file other objects
  inherit therefore takes effect in two steps unless it goes in with the
  reboot: the plan says which edited files are inherited, by how many
  objects, and that callers and callee change together only then.
- **A symbol defined in a profile is refused** (9.5).
- **A new name that collides** with a function, variable or macro
  already visible where an edit lands (the chain, the scope, the
  `#define` environment) is refused, naming the collision.

### 9.5 Partial checkouts

A wizard with their own folder and a profile has declarations of the
rest of the lib and none of its call sites. The engine says what that
means for each symbol rather than pretending:

- **Exact**: locals and parameters; private functions and variables;
  macros defined in the checkout's own files; every reference among the
  checkout's own files.
- **Within the checkout, with a warning**: a function or global that is
  not private, and any symbol of a file that code outside the checkout
  may inherit. The rename is made in the checkout and the result says
  that code elsewhere in the lib may refer to the old name and cannot be
  seen from here.
- **Refused**: a symbol whose definition is a profile stub.
  `prepareRename` answers "defined in the mudlib profile, not in your
  checkout": the definition is not there to edit.

In a whole-lib workspace a rename of a public symbol is complete except
for the `possible` occurrences, which is the most anyone can prove.

### 9.6 The two front ends

**`dgdtools rename SYMBOL NEW`.**

```
dgdtools rename /std/living::query_name query_short_name
```

prints a unified diff of the certain edits on standard output and a
summary on standard error; `--apply` writes the files instead, and
`--possible` includes the possible edits too. Both the diff and the
summary are grouped by owner (the lib's checkout patterns: one group per
wizard tree or domain, one for the shared lib), because a mud-wide
rename is handed out or announced that way. The summary counts
occurrences by class and files by owner, and repeats the warnings. Exit codes follow the other commands: 0 the rename is complete
as made; 1 possible occurrences were left unrenamed; 2 usage error or a
refusal.

**The editor.** `prepareRename` returns the name's range, or the reason
it cannot be renamed. `rename` returns a `WorkspaceEdit` with
`changeAnnotations`: certain edits under an annotation that needs no
confirmation, possible ones under one with `needsConfirmation: true`, so
the client's preview shows the first ticked and the second unticked, by
file. A client without `changeAnnotationSupport` gets the certain edits
only. Either way a message reports the possible occurrences that were
not renamed, the warnings, and the names built at run time; it never
says "renamed everywhere" while a receiver is unknown.

**Refactorings as code actions**, small ones that reuse the same
lookups:

- *Add the missing prototype*: for a function called before its
  definition in the file, insert its declaration (`File.FuncDeclText`)
  above the first use.
- *Add a suppression comment*: step 2 of section 8.
- *Fix the `#include` path*: on `include-not-found`, offer the paths
  under the include directories and the file's own tree that end in the
  missing name.
- *Rename a file*: on `workspace/willRenameFiles` return the edits to
  every `inherit` and `#include` that names the file (the edges
  `Invalidate` follows, read the other way), with the same
  certain/possible split for paths built from macros.

## 10. Syntax highlighting: two layers

Highlighting is split the way mature language tooling splits it: a
grammar in the editor for the lexical layer, and semantic tokens from the
server for what only analysis knows. The first must work with no server,
offline, on a file that does not parse. The second is an overlay that may
arrive late or not at all.

### 10.1 Layer 1: editor-side grammars

Built. Everything an editor needs lives in this repository under
`editors/`, with a README for whoever installs it. The grammars are
checked against one shared word list (`editors/common/dgdlpc.json`:
keywords, type keywords, specifiers, the words a dialect switch
reserves, operators, directive names) and against the same sample files
(`editors/common/samples/`, each with a snapshot per grammar of what the
grammar makes of it). The word list is not written by hand:
`go test ./editors -update` writes it from `pkg/token` (the keyword
table and the kinds), `pkg/lexer` (which words a dialect switch
reserves) and `pkg/pp` (`pp.Directives`), and the test fails when the
file is stale or a grammar's lists differ from it.

```
editors/
  README.md              installing the VS Code extension from the folder
                         and the Vim files; what Helix and Zed get (the
                         language server only, until there is a
                         tree-sitter grammar)
  common/
    dgdlpc.json          the shared word list
    samples/             sample files and their snapshots (*.tm.snap,
                         *.vim.snap)
  vscode/                the extension: TextMate grammar now, LSP client
                         later (section 11)
    package.json
    syntaxes/dgdlpc.tmLanguage.json
    language-configuration.json
  vim/                   standard runtime layout, installable by any plugin
                         manager in Vim and Neovim
    ftdetect/dgdlpc.vim    workspace-scoped detection (dgdtools.yml upward)
    ftdetect/dgdlpc.lua    the same rule for Neovim, asked before its own
    autoload/dgdlpc.vim    the functions the others call
    syntax/dgdlpc.vim
    ftplugin/dgdlpc.vim
    indent/dgdlpc.vim
  *_test.go              the tests of all of it (package editors)
```

Beside them now: `editors/nvim/lsp/dgdtools.lua`, the Neovim 0.11+
`vim.lsp.config` entry (section 11), with a test that runs the server
under the `nvim` on `PATH`.

**TextMate grammar** (VS Code; also usable by other editors such as
Sublime and by GitHub's Linguist if submitted), scope name
`source.dgdlpc`. It covers: comments (`/* */` and `//`); preprocessor
directives with their own name set (`#include <…>`/`"…"`,
`#define NAME(params)` with the macro name and its parameters scoped,
`#if/#ifdef/#else/#endif` with `defined`), to the end of the logical
line, continuation lines and comments that run on included; strings and
character literals with DGD's escapes (`\ooo`, `\xHH`, any other
character); numbers (decimal, hex, floats, and `1..2` *not* as a float);
DGD keywords, exactly the driver's reserved set (`nil`, `atomic`,
`rlimits`, `catch`, `nomask`, `varargs`, `operator`, `inherit`, `new`,
`goto`, `try`); type keywords; the `({ … })` and `([ … ])` literal
brackets as their own punctuation scopes; `::`, `->`, `<-`, `..`, `...`
and every other operator the lexer has; function names at definition
(`name(` at column 0, alone or after its type) and at call sites, the
label of an inherited object (`label::`, `inherit label "…"`), by
pattern. It deliberately does **not** try to tell macros from
identifiers beyond the ALL_CAPS convention (scoped `constant.other`),
and does not list kfuns: both are layer 2's job, where they are known
rather than guessed.

What a grammar, one fixed file for every lib, cannot take from the
project's dialect:

- **`//` is always a comment.** It is a rule of its own
  (`comment-line`), but nothing an extension does can switch a rule of a
  contributed grammar on or off, so it is on, which is the default of
  `dialect.slash_slash` and is harmless where the dialect has it off:
  there `//` outside a string is a syntax error, which the linter
  reports, and no valid file has one to be miscoloured.
- **`function` is never a keyword.** Without `dialect.closures`, the
  default and the stock driver, it is a plain name that a lib is free to
  give a variable or a parameter, and colouring it would be wrong there.
  In a lib with closures the grammar leaves the word alone, and layer 2
  makes up for it (10.2).

`'])'` is ambiguous to anything that reads tokens one at a time: it
closes a mapping, and it ends `f(a[i])`. The grammar opens a mapping at
`([` and a subscript at `[` and lets each close its own, so `])` is a
mapping's only where one is open. The language configuration cannot do
that: its `brackets` are matched by text, so it lists `({ })` (no other
construct ends in `})`) and leaves `([ ])` to the `(`…`)` and `[`…`]`
pairs it is made of, which nest correctly; as a pair of its own it would
unbalance every `f(a[i])`.

Tests: `go test ./editors`, which runs the grammar itself rather than
`vscode-tmgrammar-test`, so that nothing but Go is needed. A small
interpreter of the grammar format (match and begin/end rules, captures,
includes) tokenizes the samples; what it scopes is compared byte for
byte with the tokens of `pkg/lexer` (same classes, same token
boundaries, a directive ending where the lexer's does), with the word
list, and with the snapshots. The price is that the grammar keeps to the
regular expression syntax Go and Oniguruma read alike (no `\d \w \s`,
no `$`, no lookbehind, no backreferences), plus `(?=…)` at the end of a
top-level alternative, which the test emulates exactly; the test rejects
a pattern outside it. That subset turned out to be enough. What it does
not buy is a run under Oniguruma itself: the first time the grammar is
changed with VS Code at hand, the snapshots should be compared once with
`vscode-tmgrammar-test`'s.

**Vim.** Vim and Neovim ship `syntax/lpc.vim` (447 lines, last changed
2016). It targets MudOS/FluffOS LPC: it highlights `foreach`, `class`,
`array`, `buffer`, `ref`, `nosave`, `protected` and `(: … :)` function
pointers as valid, lists MudOS efuns and applies, and knows none of
DGD's `nil`, `atomic` or `rlimits`. On DGD code it is therefore wrong in
both directions: it colours as keywords words that are plain identifiers
in DGD and presents `(: :)` as legal, while DGD's own keywords stay
uncoloured. Its switches (`lpc_pre_v22`, `lpc_compat_32`) select MudOS
versions, not drivers. Filetype detection treats `*.c` as C unless
`g:lpc_syntax_for_c` is set.

Decision: ship a separate, small `syntax/dgdlpc.vim` (filetype `dgdlpc`)
rather than patch the stock file. A DGD mode inside `lpc.vim` would have
to subtract most of what the file is, the upstream maintainer has been
inactive since 2016, and a separate filetype lets `ftdetect` be precise:
set `dgdlpc` for `*.c`/`*.h` only when a `dgdtools.yml` or
`.dgdtools.yml` is found upward from the file, or the file lies under a
directory of `g:dgdlpc_roots` (never globally), with
`g:dgdlpc_slash_slash` and `g:dgdlpc_closures` (and their `b:` forms)
mirroring the dialect; they are set by hand, the runtime files do not
read the configuration. In Vim the rule runs after Vim's own and
replaces its answer, so a lib's file is `c` for a moment and anything
hooked to `FileType c` sees it; in Neovim the rule is registered with
`vim.filetype.add` ahead of the built-in detection, so a C language
server is never started on an LPC file. `ftplugin` sets `commentstring`,
`comments`, `matchpairs` and `include`/`define` patterns so `gf` and
`[i` work without the server (a path that begins with `/` is looked for
under each directory above the file, which finds it without knowing
where the lib begins); `indent` is `cindent` with the options that give
the formatter's indentation while typing. Three shapes come out
differently and are left to the formatter: a block after a case label on
the same line, `({ })` inside `([ ])` over several lines (where the
formatter's own layout is its brace count, not the nesting), and the
statement after a `goto` label. The syntax file's word lists are checked
against the shared word list. Tests, when a `vim` or `nvim` is on `PATH`
(both, when both are): detection, the options, the syntax groups of the
samples against the lexer's tokens and the snapshots, and the indent
script against `pkg/format` on a sample that is a fixed point of the
formatter.

**tree-sitter grammar** (highlighting in Neovim, Helix and Zed, which use
tree-sitter rather than TextMate or Vim syntax; in Neovim also folds and
text objects). This is *for highlighting only*: it is not, and must not
become, the linter's parser. The reasons that ruled tree-sitter out for
the linter ([architecture.md](architecture.md) 2.1: cgo, coarse error
nodes, no macro tables) do not matter for colouring. Cost: grammar plus external scanner work for
directives embedded anywhere, `highlights.scm`, `injections.scm`,
`locals.scm`, corpus tests against real lib files (the grammar must stay
useful on unpreprocessed code where macros appear in type and statement
position), and the packaging each editor wants (nvim-treesitter parser
registration, Helix `languages.toml` + grammar source, a Zed extension
compiling the grammar to WASM): **6 to 8 days**, plus ongoing
maintenance of a second grammar. **Deferred**, and `editors/` has no
directory for it until then. Meanwhile Neovim uses the
Vim syntax file (which Neovim supports natively) plus semantic tokens,
and Helix and Zed fall back to their C grammar, which is tolerable for
LPC and gets `({ })` and `::` wrong. To be taken up when there are Helix
or Zed users asking, or when step 9 shows semantic tokens alone are not
enough in Neovim.

### 10.2 Layer 2: LSP semantic tokens

Semantic tokens add only what analysis knows; they never restate what
the grammar already colours (no tokens for keywords, strings, comments or
operators), which keeps payloads small and avoids flicker when the server
is slower than the grammar. One exception follows from 10.1: in a lib
with `dialect.closures`, `function` is a keyword the grammar cannot
colour, and the server sends it (`keyword`).

**Legend** (standard types and modifiers wherever one fits, so themes
work unconfigured):

| Token | Type | Modifiers |
|---|---|---|
| macro used as an expression or call | `macro` | |
| type-class macro (`status`, `function`) | `type` | `macro` (custom) |
| specifier-class macro (`public`) | `modifier` | `macro` |
| statement-class macro | `macro` | `controlFlow` (custom) |
| macro name at its `#define` | `macro` | `declaration` |
| function at its definition / prototype | `function` | `definition` / `declaration`; `static`; `private` (custom) |
| call to a function defined in this file | `function` | |
| call to an inherited function | `function` | `inherited` (custom) |
| call to an auto-object function or a kfun | `function` | `defaultLibrary` |
| `->name` with a resolved receiver | `method` | `static` when the target is static |
| name that provably does not resolve (the finding exists) | `function` | `unresolved` (custom) |
| function documented as deprecated (a `@deprecated` tag or a config list) | as above | `deprecated` |
| inherit label (`room::create`) | `namespace` | |
| global variable | `variable` | `static` when so declared; `readonly` never |
| parameter | `parameter` | `declaration` at its declaration |
| local variable | `variable` | `local` (custom); `declaration` |

Custom modifiers (`macro`, `inherited`, `unresolved`, `local`, `private`,
`controlFlow`) are declared in the legend; the VS Code extension maps
them to default styles through `semanticTokenScopes` so they are visible
without theme support, and other editors ignore what they do not know.
"Unresolved" is emitted only where a diagnostic would be: a name that is
merely unknown gets no token at all, in keeping with prove-it-or-stay-
silent.

**Requests.** `textDocument/semanticTokens/full` and `full/delta` are
supported; `range` is supported for large files (editors request the
viewport first). The server keeps the last token array per open document
with its `resultId` and answers delta requests with the standard
prefix/suffix diff, so a typical edit sends a few dozen integers.
`workspace/semanticTokens/refresh` is sent when something a document
depends on changes (a header's macros, a parent's functions), using the
dependents `Invalidate` already returns.

**Performance.** Tokens come from one walk of the tree the diagnostics
lint already built (`Document.Tree`), plus chain lookups that are memoized; they are
computed in the same scheduled job as diagnostics for that document
version and cached, so a tokens request never triggers its own parse.
Target: under 10 ms for a 1,000-line file once its chain is loaded.
Macro class and chain membership are per-identifier map lookups. The one
expensive input, the whole-lib scan, is never needed for tokens.
Budget: a file over 20,000 lines answers `full` with a `range`-sized
window and logs it.

**As built** (section 8, "As built, step 9"): the legend has standard
types only, and three custom modifiers (`inherited`, `local`,
`macro`); a specifier-class or empty macro is `modifier`, a statement
macro a `macro` with no modifier, `->name` a `method` whatever the
receiver, and nothing is `unresolved`. Tokens are computed with the
diagnostics and kept with the document; the refresh is sent when a
lint after a change elsewhere makes other tokens; there is no budget
for big files.

**Split.** 9a (macros by class, declarations, inherit labels, calls
resolved through the chain, kfuns once the table exists) needs nothing
beyond today's library and is worth shipping right after hover, because
"is this name a macro or a function, and where does it come from" is the
question LPC code most often leaves open. 9b (parameters, locals,
unresolved names) waits for nothing more: the scopes are built (step 7).

## 11. Editor clients

The language is `dgdlpc` in every editor: the VS Code language id, the
Vim filetype, the name in a Helix or Zed configuration. `lpc` is taken
in both ecosystems by tooling for other drivers (Vim's own `lpc`
filetype is MudOS, 10.1), and a file handed to two grammars and two
servers under one id is worse than a new word.

- **VS Code extension** (`editors/vscode`, TypeScript, about 4 days
  on top of the grammar from 10.1):
  language id `dgdlpc` for `*.c`/`*.h` *inside a workspace that has a
  `dgdtools.yml` or the `dgdtools.profile` setting* (activation must not
  hijack C files elsewhere: activate on `workspaceContains:**/dgdtools.yml`
  and offer a command "Treat this folder as an LPC lib"); the TextMate
  grammar from 10.1; `semanticTokenScopes` for the custom modifiers of
  10.2; `language-configuration.json` (brackets, comments,
  indentation rules); `vscode-languageclient` starting `dgdtools lsp`;
  settings `dgdtools.path`, `dgdtools.profile`, `dgdtools.libPath`,
  `dgdtools.showBaselined`, `dgdtools.trace.server`; a content provider for
  `dgdtools-profile:` documents. Binary acquisition: use `dgdtools.path` or
  PATH first; otherwise download the release asset for the platform
  (GoReleaser already builds them) with a checksum, after asking.
  Built, but for the last sentence and what waits for the server
  (`semanticTokenScopes`, `dgdtools.showBaselined`): 11.1 says what is
  there, and 11.2 how a release carries the binary, so that nothing
  is downloaded.
- **Vim and Neovim:** the `editors/vim` runtime files from 10.1 (install
  as a plugin directory) set the filetype. For Neovim 0.11 and later,
  `editors/nvim/lsp/dgdtools.lua` is the `vim.lsp.config` entry (`cmd =
  {"dgdtools", "lsp"}`, `root_markers = {"dgdtools.yml",
  ".dgdtools.yml"}`, `filetypes = {"dgdlpc"}`); submit it to
  `nvim-lspconfig` once stable. For Vim, `editors/README.md` has the
  lines for vim-lsp and coc.nvim. The Neovim entry is there and tested
  (`TestNeovimClient`); the lines for the others are written down and
  were not run.
- **Helix:** a `languages.toml` entry in `editors/README.md` (language
  `dgdlpc`, roots `dgdtools.yml`, `language-servers = ["dgdtools"]`).
  Helix highlights only through tree-sitter and does not use semantic
  tokens, so until the deferred grammar of 10.1 exists Helix users get
  diagnostics and navigation with C highlighting.
- **Zed:** the settings in `editors/README.md` declare the language
  server and reuse the C grammar until the tree-sitter grammar exists;
  Zed does apply semantic tokens on top.
- Emacs (`eglot`), Sublime (`LSP`), Kate: a line of configuration each,
  documented, not maintained.

### 11.1 The VS Code client, as built

`editors/vscode` is the whole extension: the manifest, the grammar and
the language configuration of 10.1, and `src/`, some 240 lines of
TypeScript over `vscode-languageclient` and 260 that know nothing of VS
Code. `dist/extension.js` is built from it and is not in the
repository. (`scripts/` is how a release packs it: 11.2.)

- **TypeScript, bundled into one file** (by esbuild). That is what
  rust-analyzer, vscode-go, Biome and Ruff do, and for the same two
  reasons: the VS Code API and the client library are large and typed,
  so the compiler is the test that an extension with no VS Code at hand
  can have of its calls; and one bundled file is what a packaged
  extension should be (the client library and what it needs are some
  180 files otherwise). The price is a build step, `npm ci && npm run
  build`, which plain JavaScript would not have spared: the client
  library has to be fetched either way. One dependency when installed
  (`vscode-languageclient`), four to build (`typescript`, `esbuild`, the
  types of VS Code and of Node) and two to pack and publish
  (`@vscode/vsce`, `ovsx`: 11.2), all pinned by `package-lock.json`;
  nothing is installed outside the folder. No linter, no formatter, no
  test framework: `tsc` with `strict`, and Node's own test runner on the
  TypeScript as it is.
- **`vscode-languageclient` 11**, the current release, which speaks LSP
  3.18 and wants VS Code 1.91 (June 2024) or later; the manifest says
  so. The server is the executable itself with `lsp`, and `--log FILE`
  from `dgdtools.logFile`; the library adds `--stdio`. The document
  selector is the language `dgdlpc`, whatever the scheme: a document
  that is no file (a stub, an untitled one) is synchronized and the
  server says nothing of it, which is what steps 4 and later will build
  on for the stubs.
- **Activation** is as planned (`workspaceContains` for the two config
  file names) and what VS Code adds by itself: a document of the
  language, one of the commands. The server is started then, not at the
  first document: an idle server is a process that has read nothing.
- **Settings.** `dgdtools.path` (empty: the program of the extension's
  package, else `dgdtools` on `PATH`, as 11.2 has it; a bare name: that
  command on `PATH`; else a file, absolute once `~` is the home
  directory), `dgdtools.profile` and `dgdtools.libPath` (resource
  scope: the server asks for the section `dgdtools` of each folder, and
  the client tells it when the section changed, with
  `synchronize.configurationSection`), `dgdtools.logFile`,
  `dgdtools.trace.server`. A change of the path or the log file starts
  the server again. Both are `machine-overridable` and listed as
  `restrictedConfigurations`: in a workspace that is not trusted, a
  workspace cannot say which program is run or which file is written.
  That is vscode-go's arrangement; the extension is otherwise allowed
  there, since the server reads and never runs what a workspace has.
- **`dgdtools.trace.server` is not the three-valued switch it used to
  be** in this library. In the version used a trace is made while the
  log level of the client's output channel is *Trace*, which is VS
  Code's own control, and the setting chooses between `messages` and
  `verbose`; `off` would be read as `messages`. The manifest offers the
  two values and says where the switch is. Either way the library sends
  `$/setTrace`, and the server's `$/logTrace` lines appear in the
  channel.
- **No executable: one message**, saying what was looked for (the
  command on `PATH`, or the file the setting names) and what to do, with
  two buttons: the setting, and the installation section of the README.
  Nothing is looked for in other places (`~/go/bin`): without a program
  in the package, what a shell would run is what the editor runs.
- **Commands:** `dgdtools.restartServer`; `dgdtools.associateFolder`
  ("Use DGD LPC for This Folder's .c/.h Files") and its inverse,
  `dgdtools.dissociateFolder` ("Use C for This Folder's .c/.h Files"),
  which also list Microsoft's C extensions as unwanted recommendations
  in `.vscode/extensions.json` and take them out again. The two are in the command palette, for
  the workspace's folder (asked for when there are several), and in the
  explorer's menu of a folder once the extension is active: a menu of
  every folder of every workspace is no place for them otherwise.
- **What "a folder of DGD LPC" is written as.** `files.associations` in the
  workspace's settings (`ConfigurationTarget.Workspace`), never the
  user's. The setting has window scope in VS Code, not resource scope:
  a folder's own settings file is not read for it in a workspace of
  several folders, and one pattern list serves the window. So the
  patterns say which folder they mean. For the one folder of a
  workspace they are `*.c` and `*.h`, in its `.vscode/settings.json`,
  as the README always said. For a folder below it, or a folder of a
  multi-root workspace, they are `**/DIR/**/*.c` and `**/DIR/**/*.h`,
  with the folder's path from the workspace folder (led by the
  workspace folder's own name when there are several): VS Code matches
  a pattern that has a `/` against the whole path of a file. Relative,
  so that committed settings mean the same in another checkout; the
  price is that a directory of the same name and place elsewhere in the
  window matches too. The inverse removes exactly those keys, and only
  where their value is `dgdlpc`.
- **The offer.** On activation the extension looks for config files in
  the workspace, and for the outermost directory of each nest (a
  project's files are everything below its config; a config further
  down is a scope or a project inside it) whose files are not
  associated yet it asks once whether they should be: *Yes* runs the
  command, *Never* is remembered for the workspace, closing the message
  asks again next time. Without it, a lib opened for the first time
  shows C and a server with nothing to do.
- **Stubs** (`dgdtools-profile://NAME/std/room.c`). The plan had a
  content provider in the extension. What is built is the request LSP
  3.18 has for exactly this, `workspace/textDocumentContent`: the server
  names the scheme in its capabilities, and the client library registers
  the `TextDocumentContentProvider` and asks the server for the text, so
  the extension has no code for it and any other client of 3.18 has the
  same. The server (`content.go`) answers from the profile of that name
  which a project in use has (`Project.Profile.Files`), a project of an
  open document before one of a workspace folder; the name is compared
  again without regard to case when nothing matches exactly, since VS
  Code writes the host of a URI in lower case. A stub's URI is escaped
  where its location was not one (`uriOf`). Such a document is
  read-only in VS Code by its kind. The extension adds one thing: a
  stub is given the language `dgdlpc` when the workspace's associations
  do not (they are about the workspace's folders). Not built:
  `workspace/textDocumentContent/refresh` when a profile changes; a stub
  that is shown stays as it was until it is opened again.
- **Checked without VS Code**, which is not on the machine this was
  written on. `npm run check` (`tsc`: every call of the VS Code API and
  of the client library against their types), `npm test` (Node's test
  runner over `server.ts` and `associations.ts`: the lookup on `PATH`
  on both kinds of system, what each value of `dgdtools.path` comes to,
  the order of the setting, the package's program and `PATH`, the
  messages, the arguments, the patterns for each kind of folder, adding
  and removing them), `npm run build`; all three in CI. `go test
  ./editors` (`TestVSCodeClient`) holds the manifest to the code:
  commands contributed and registered, settings contributed and read,
  the scope and the trust of each, the scheme of a stub as
  `profile.Scheme` has it. `TestStubContent` in `pkg/lsp` is the
  request. **Not checked:** the client in a running VS Code. Nothing
  above replaces opening a lib in it once; the integration test of
  section 13 is how that would be kept.

### 11.2 How a release carries the binary

The extension had no binary and downloaded none: `dgdtools.path`, else
`PATH`, else the message. That is right for whoever has a checkout, and
wrong for whoever installs from a marketplace and has never heard of
`go install`.

How the extensions this one is modelled on do it:

| | |
|---|---|
| rust-analyzer | a package for each platform (`vsce package --target`), the server inside it; a setting to use another |
| Ruff | the same: the binary bundled, a package for each platform; settings to prefer the environment's own |
| Biome | the project's own copy first (a JavaScript project pins its tools), then `PATH` |
| vscode-go | `go install` of gopls, which needs the Go toolchain: every one of its users has it |

A wizard has no toolchain and no project that pins tools, so the first
two are the model. **Decided, and built: platform-specific packages
with the binary inside.** The plan was this:

- **The packages.** One `.vsix` for each of the six binaries GoReleaser
  already builds (`darwin-arm64`, `darwin-x64`, `linux-arm64`,
  `linux-x64`, `win32-arm64`, `win32-x64` are VS Code's names for
  them), with the binary at `bin/dgdtools` (`bin/dgdtools.exe`), some
  5 MB each; and one package without a target and without a binary,
  which is what every other platform gets and behaves as before. The
  Marketplace and Open VSX both serve the package of the user's
  platform. `bin/` is not in the repository, and `.vscodeignore` lets
  it into the package.
- **Which binary runs.** `dgdtools.path` when it is set; else the one in
  the package; else `PATH`. The package's own before `PATH` because a
  grammar, a client and a server are one commit here (section 14) and
  then one release: the client never meets a server older than itself.
  Whoever wants the editor to run what their shell runs says so with
  `dgdtools.path: dgdtools`. The client makes the packaged file
  executable before it starts it, a zip not being a reliable keeper of
  that bit, and writes the path and `dgdtools version` to its output
  channel.
- **The version** of the extension is the version of the tool: tag
  `v0.4.0` publishes extension `0.4.0`, set in `package.json` by the
  release job and not by hand.
- **The release job**, after GoReleaser's in `release.yml`, once for
  each target: take that target's binary from the release just made,
  compare it with `checksums.txt`, put it in `editors/vscode/bin/`,
  `npm ci`, `vsce package --target T --no-dependencies` (the
  prepublish script checks the types and builds minified), then
  `vsce publish --packagePath` and `ovsx publish --packagePath` with
  the owner's two tokens as repository secrets. `@vscode/vsce` and
  `ovsx` are installed by that job at pinned versions; they are not
  dependencies of the extension. The darwin binaries need nothing more:
  the Go linker signs an arm64 binary ad hoc wherever it runs, and a
  file VS Code unpacks is not quarantined.
- **Before the first release**, what the Marketplace asks of a listing
  and the folder does not have: a `README.md` and a `LICENSE` in
  `editors/vscode`, an icon, the real publisher, and `private` gone.

**As built.** The packages and the order are as planned; the release
job is simpler than planned, and nothing is published yet.

- **The client** (`server.ts`, `resolveServer`): the setting, then
  `<extension>/bin/dgdtools` (`dgdtools.exe` on Windows) when it is a
  file, then `PATH`, then the message, which is the one there was. A
  setting that names nothing is still the mistake reported: no other
  program runs in its place. The packaged file is given the bit when
  it lacks it (`makeRunnable`; vsce and VS Code both keep it, so this
  is for a package that went through other hands). The output channel
  has the path, which of the three named it, and the server's version;
  the version is the one the server gives in its answer to
  `initialize` (`serverInfo`), so `dgdtools version` is not run for
  it.
- **The script**, `editors/vscode/scripts/vsix.mts`, run by Node as it
  is, like the tests. `--dist DIR` reads GoReleaser's
  `DIR/artifacts.json`, and for each binary whose `goos`/`goarch` is one
  of the six puts the file in `bin/`, executable, and runs `vsce
  package VERSION --target T --no-dependencies`; then once more without
  a target and with no `bin/`. `--binary FILE [--target T]` makes one
  package from one program (`make vsix`: the one just built, for this
  machine). The six are a table in the script, which `go test
  ./editors` (`TestVSCodePackages`) compares with the `goos` and
  `goarch` of `.goreleaser.yaml`: a platform added there and not here
  fails a test, not a release. The files are
  `dgdtools-T-VERSION.vsix` and `dgdtools-VERSION.vsix`, 2.1 to 2.4 MB
  with a program (it is 5 MB before the zip) and 110 kB without.
- **The version** is given to vsce (`--version v0.4.0` to the script,
  `--no-update-package-json` to vsce): the package says 0.4.0 and
  `package.json` is not written to, in the release job or anywhere. It
  keeps `0.0.1`, which is what a package made from a checkout says.
- **The tools** are `@vscode/vsce` and `ovsx` as `devDependencies`, at
  one version each in `package-lock.json` like the compiler and the
  bundler, and run from `node_modules`: `npm ci` is all the job
  installs, and it fetches nothing by name. That is how rust-analyzer
  has them. They are not in a package (`node_modules` is not).
- **The release job** (`vscode` in `release.yml`, after `goreleaser`).
  The binaries are GoReleaser's own `dist/`, handed from the one job to
  the other as a workflow artifact, not downloaded from the release and
  compared with `checksums.txt`: they are the same files of the same
  run and never leave it. (An artifact loses the executable bit, which
  is why the script sets it.) The job runs the script with the tag as
  the version, and attaches the seven packages to the GitHub release,
  beside the archives.
- **Publishing** is the job's last steps, `vsce publish --packagePath`
  and `ovsx publish --packagePath` over the seven files, each run only
  when its token is a repository secret (`VSCE_PAT`, `OVSX_PAT`) and
  the tag is no pre-release (`v1.0.0-rc.1` is packed and attached, and
  no marketplace takes its version). Neither secret exists, so neither
  step has run. With a token, `vsix.mts --publishable` runs first and
  fails the job, saying why, while `package.json` is `private`, has no
  publisher, or the folder has no `LICENSE`: a manifest nobody made
  ready is not published by the arrival of a token.
- **In CI** (`vsix` in `ci.yml`): `goreleaser build --snapshot`, the
  script over its `dist/`, and a look at the result: seven packages,
  the one for the runner has a `dgdtools` that runs, the one for the
  rest has none. So the release job's packing is run on every push,
  not first on the day of a release.
- **What a listing needs.** `editors/vscode` has a `README.md` (the
  Marketplace's page), a `CHANGELOG.md` that points at the releases,
  and `.vscodeignore` keeps `src/`, `scripts/` and the tools out.
  The repository is MIT-licensed; `editors/vscode/LICENSE` is a copy of
  the root `LICENSE`, which `TestVSCodeClient` holds byte for byte, and
  the manifest says `"license": "MIT"`. The publisher is `mmcdole`, so
  the extension is `mmcdole.dgdtools`, and the manifest is no longer
  `private`. **Still the owner's:** an icon, and the two accounts'
  tokens as the secrets `VSCE_PAT` and `OVSX_PAT`.
- **Not checked:** a package installed in a VS Code, which is not on
  the machine this was written on, so neither is the program started
  from `bin/` by the client; the release job, which runs on a tag (its
  packing is what CI runs, its attaching and publishing are not).

**Not built, by decision: downloading.** The first plan had the
extension fetch the release asset when it finds no binary, with a
checksum, after asking. It is what a single package for all platforms
would force, and it costs what the packages avoid: network code in an
editor extension (proxies, firewalls, GitHub's rate limits, a first run
that can fail); a checksum that proves little when it comes from where
the binary came from, so signing to do it properly; a place to keep the
binary, to update it and to clean it up; and an extension that fetches
and runs programs, which is what a careful user and a marketplace look
at hardest. A package for each platform is delivered, verified and
updated by the marketplace like any other extension, and works with no
network.

## 12. Order of work

1. **Grammars first** (3.5 days, no server needed, useful on day one and
   independent of everything else): the shared word list, the TextMate
   grammar and the Vim runtime files, with their tests. **Done**
   (10.1).
2. The prerequisite of step 1 that is left (`PositionMapper`),
   `pkg/lsp` skeleton, transcript test harness. Ship steps 1 to 3 behind
   `dgdtools lsp` and test with Neovim (no extension needed) (9 days).
   **Done** ("What is built", above).
3. VS Code extension (client, settings, grammar packaging) against steps
   1 to 3 (4 days). **Written** (11.1), and not yet run in a VS Code;
   a release packs it for each platform with the binary inside (11.2),
   and publishes it nowhere until the owner says where.
4. `syntax.PathAt`; steps 4 to 6 (9 days); then semantic tokens 9a (2
   days), which needs only what hover needs. **Steps 4 to 6 are done**
   (section 8), and the kfun table of the next item with them; 9a was
   built with 9b (section 8, "As built, step 9").
5. Scopes and kfun table in the library (also unlock linter rules:
   undeclared variable, undefined function, argument counts); step 7 and
   semantic tokens 9b (7 days), **both done**; then step 10, rename within a file (3
   days), which needs the scopes and nothing else, **done** (section 8,
   "As built, step 10").
6. Reverse index; step 8 (4 days). **Done** (section 8, "As built,
   step 8").
7. The rename engine across files with `dgdtools rename` (step 11, 9
   days), **done** (section 8, "As built, step 11"), and the
   refactorings of step 12 (3 days), **done** (section 8, "As built,
   step 12"). The command line
   comes first: it needs no editor, and it is the form an arch uses for
   a mud-wide rename.
8. Deferred until asked for: the tree-sitter grammar (6 to 8 days) with
   Helix and Zed packaging.

Total: about 46 days of server and library work (31 for steps 1 to 9,
15 for rename and refactorings), 3.5 for grammars and 4 to 5 for
clients; the first usable release (grammars, live diagnostics, fixes,
formatting, VS Code and Neovim) is roughly three and a half weeks, and
grammars alone can ship in the first.

## 13. Testing

- **Under the server.** What the server relies on is tested without it,
  in `pkg/session`: a buffer change is one step while lints run beside
  it; a configuration, profile, baseline or `.editorconfig` change
  reaches the files it is for and no other project's linter; a deleted
  directory; and `TestDifferential`, which after each of a random
  sequence of edits, creations and deletions holds what a front end
  would be showing to what a session made afresh finds (30 sequences of
  25 steps by default, `-seeds N` for more).
- **Golden transcripts.** A test is a `.txtar`: the lib's files, then a
  script of client messages (`open a.c`, `change a.c 3:5-3:9 "x"`,
  `request hover a.c 10:4`, `wait diagnostics a.c`) and the expected
  server messages as JSON with paths and versions normalized. An
  in-process client over `io.Pipe` runs them; `-update` rewrites the
  expectations. `--trace` output from a real editor session converts to
  a transcript, so every bug report becomes a test.

  **As built:** `pkg/lsp/testdata/*.txt`, played by `TestTranscripts`
  (its comment lists the script's commands; `go test ./pkg/lsp -run
  TestTranscripts -update` rewrites them): the life of a session, code
  actions for a rich and for a plain client, formatting, both position
  encodings, and what changes around the documents (watched files,
  configuration, settings). There is no `wait`: a script asks for what
  is published once the server is idle, which it learns from the
  server's one request of its own, `dgdtools/idle`, answered when
  nothing is scheduled, running or unsent. That is what makes a
  transcript the same on every run; an editor has no use for it.
  Converting a `--trace` log to a script is not built.
- **As built, the rest of this list.** `TestDiagnosticsAreTheLinters`
  (every file of a fixture lib, both encodings, against a fresh lint;
  after an edit and its undo) and, through the command itself,
  `TestLSPMatchesLint` in `cmd/dgdtools` (against `dgdtools lint
  --output-format json`); `TestIncrementalChanges` and `FuzzChange`
  (ranges in both encodings against a plain string, and the mapper
  against positions worked out the long way); `TestRandomSession` (edits,
  opens and closes, files changing on disk; after each step what is
  published is what a fresh lint finds; `-seeds N`, 600 sessions clean);
  `TestDependents`, `TestWatchedFiles`, `TestSettings` (invalidation,
  rebuild on a configuration change, files and directories created and
  deleted, a baseline, the two settings); `TestBurst` (200 changes, each
  publication of the text it names, the last of the last);
  `TestCancel`, `TestPanics`, `TestEndings`, `TestLatin1`,
  `TestScanProgress`, `TestLogAndTrace`; `TestNeovimClient` in
  `editors`. All under `-race`. `TestCorpusSession` (`-tags corpus`,
  `$DGDTOOLS_CONFIG`) is the corpus smoke test: on the VikingMUD lib it
  opens 2,003 of the 108,117 files (9 of them not UTF-8, opened as
  Latin-1), types a character into each, takes it out and closes it; what
  is published equals a whole-lib linter's findings for every file
  (1,334), before and after; no panic; open to diagnostics 4 ms median,
  9 ms at the 95th percentile (1 ms of it debounce), 5.4 s for the one
  document that waited for the first scan of the lib; 357 MB of heap at
  the end, 709 MB since the reverse index of step 8 and the searches the
  test now makes (as many places as the library finds for 51
  functions), 371 MB since the memory work of architecture.md, section
  4.1 (638 MB just before it). `TestCorpusMemory` measures what the
  server holds and its latencies, with heap profiles, and the peak
  resident size after the scan, after the session's files and at the
  end (same section; the whole-lib `dgdtools lint`'s memory is there
  too).
  Not built: the VS Code integration test (the client is the
  next step).
- **As built, definitions (step 4).** In `pkg/resolve`: every kind of
  reference on a whole lib, on a checkout with a profile (declarations
  in stubs) and on a file with no lib, with what must stay silent
  (`TestWholeLib`, `TestInherited`, `TestPartialCheckout`, `TestNoLib`);
  under it `TestPathAt` (`pkg/syntax`), `TestDefinition` (`pkg/pp`),
  `TestChain` and `TestAutoFuncAndReadFile` (`pkg/index`). In `pkg/lsp`:
  `TestDefinitionIsResolve` holds the server to the library at every
  place of every file of a lib, in both encodings, with characters of
  several bytes and UTF-16 units before the names and a Latin-1 file on
  disk; `TestDefinitionInStub` (the range is of the text
  `workspace/textDocumentContent` gives); `TestDefinitionOfNothing`; the
  transcripts `definition.txt` and `definition-plain.txt` (links;
  locations and UTF-8; a target that is an unsaved buffer). In
  `editors`: `TestNeovimDefinition`, the `nvim` on `PATH` going to
  definitions with `vim.lsp.buf.definition()`. On the corpus (`-tags
  corpus`, in `pkg/resolve`):
  - `TestCorpusResolve` asks what every identifier, string constant and
    directive word of 2,004 of the VikingMUD lib's 108,117 files refers
    to: 189,105 places, 79,429 references, 65,798 of them resolved
    (82.8%), about a microsecond a question. Every one of the 65,799
    declarations was held to its file: the name stands at the place
    given and reading the file again finds a declaration of that kind
    and name there. By form: direct calls 31,314 of 36,950 (84.7%; the
    rest are the driver's `this_object`, `sizeof`, `strlen`, ..., and
    calls in files whose chain has an unseen object); `::fn()` 1,649 of
    1,751 and `label::fn()` 425 of 427; `->fn` and `call_other` on a
    known object 1,035 of 1,077 (96.1%) and on `this_object()` 253 of
    282; string callbacks 1,066 of 1,074 (99.3%); `#include` 2,499 of
    2,618; inherit paths 1,864 of 1,924 (96.9%); path arguments 130 of
    159; macros 8,098 and globals 9,147, all. The 7,603 `->fn` and
    `call_other` on an unknown receiver are references without a
    declaration, by design.
  - `TestCorpusResolvePartial` builds the lib's profile (723 stubs) and
    resolves every name of ten wizard trees (4,206 files) twice, in a
    copy with nothing but the profile and in place with the whole lib:
    219,421 references resolve to the same lib file on both sides,
    114,814 of the checkout's declarations being in stubs; 1,949 are
    lost (code in neither the checkout nor the profile); none resolves
    in the checkout only, and none to something else. All 219,423
    declarations of the checkout side were held to their files and
    stubs.
- **As built, hover and signature help (step 5).** In `pkg/kfun`:
  `TestDriverSource` and `TestExtensionExample`, which hold the table
  and the example's `dialect.kfuns` to the driver's and the module's
  source (`$DGD_SRC`, `$DGD_EXT_SRC`; skipped without them),
  `TestDGD`, `TestParse`, `TestWith`. In `pkg/resolve`: `TestHover`
  (every kind on a whole lib, with what has no hover),
  `TestHoverKfunProved` (an unseen object in the chain, an unseen auto
  object, a file with no lib), `TestHoverPartialCheckout` (stubs, and
  the profile's kfuns), `TestComment`; `TestSignature` (the argument at
  every place of a call, nested calls, commas that are not the call's,
  `varargs` and `...`, each form of callee, the arguments a registrar
  passes on, declarations, unknown receivers), `TestSignatureWhileTyping`
  (a call half written, the statement not parsing),
  `TestSignatureReceiverIsExtracts` (the receiver as read from the
  tokens against `calls.Extract`), `TestEveryPrefix` (a file as it is
  typed, a byte at a time). Under them `TestAutoMembers` and
  `TestLibFileAndParamSpans` (`pkg/index`), `TestForms` (`pkg/calls`).
  In `pkg/lsp`: `TestHoverIsResolve` holds the server to the library at
  every place of every file of a lib, for hover and signature help
  both, in both encodings and for a client with and without Markdown
  and label offsets; `TestHoverContent` and `TestMarkdownText` (what
  the two kinds of client get); `TestHoverInStub`;
  `TestSignatureRetrigger`; `TestHoverOfNothing`; the transcripts
  `hover.txt` and `hover-plain.txt`. In `editors`: `TestNeovimHover`,
  the `nvim` on `PATH` showing hovers and signatures with
  `vim.lsp.buf.hover()` and `vim.lsp.buf.signature_help()`, the active
  parameter marked. On the corpus (`-tags corpus`, in `pkg/resolve`),
  `TestCorpusHover` asks for the hover of every place that names
  something in 2,000 of the VikingMUD lib's 108,117 files, and for the
  signature just inside the `(` of every call of their code:
  - 76,876 places, 74,484 with a hover (96.9%): all of the 7,373 `->fn`
    on an unknown receiver, which say so; direct calls 34,045 of 36,051
    (94.4%: 30,714 with a declaration and 3,331 that are kfuns);
    21,321 of the declarations told have a comment above them. A hover
    takes 24 µs on average, a signature 34 µs.
  - 47,920 calls, 38,198 with a signature (79.7%; 94.2% of those whose
    receiver is not unknown): direct calls of the lib's functions
    30,714 of 32,720, kfuns 3,331 of 3,331, `::fn` 1,627 of 1,729,
    `label::fn` 417 of 419, `->fn` on a known object 695 of 730 and on
    `this_object()` 183 of 195, macros 1,231 of 1,423; on an unknown
    receiver 0 of 7,373, by design.
  - Held, for each of them: a hover has something for every
    declaration the place resolves to, and for a function the
    declaration the index has; the signatures of a call are the
    declarations its name resolves to, or the kfun's, which holds the
    reading of the tokens to the reading of the tree at every call; an
    unknown receiver has no signature and a hover that says so. No
    exception in the sample.
  - Kfuns: 3,331 calls of 22 of the project's 109 (the table's 106 and
    the three of the mud's extension module), `this_object`, `sizeof`,
    `previous_object`, `strlen` and `sscanf` before the rest. In a
    program all of which is known, 519 calls of 139 names are neither
    a function of the lib nor in the table: none of them is a kfun of
    DGD's. They are other drivers' functions that code brought along
    (`pointerp`, `replace_program`, `extract`, `strsrch`,
    `first_inventory`, `m_indices`, the `socket_` family), macros of
    headers the file does not include, and the lib's functions called
    in files that do not inherit them: what the driver answers
    "undefined function" to. 1,591 calls are not judged, the program
    having an object nobody sees.
  `TestCorpusResolve` and `TestCorpusResolvePartial` are as they were,
  but for one `::destruct_object()` in what the auto object is made
  of, which is the driver's and no longer the auto object's own.
- **As built, symbols (step 6).** `TestScore` (`internal/fuzzy`). In
  `pkg/index`: `TestFindSymbols` (what is found before and after the
  scan, the order of the matches, a function a header gives two
  objects, the cap, the next generation) and `TestFindSymbolsInStubs`
  (a checkout with stubs, a file of the checkout before the stub of its
  name, no lib). In `pkg/resolve`: `TestSymbols` (every kind of
  declaration, prototypes, several declarators, a macro over two lines
  and one defined twice, what is skipped or did not parse, a header)
  and `TestSymbolsOperator`. In `pkg/lsp`:
  `TestDocumentSymbolIsResolve` holds the server's outline to the
  library's for every file of a lib, in both encodings and both forms;
  `TestWorkspaceSymbol` (positions in both encodings behind characters
  of several bytes, an open document searched as the editor has it,
  the cap), `TestWorkspaceSymbolInStubs`, `TestSymbolsOfNothing`; the
  transcript `symbols.txt`. In `editors`: `TestNeovimSymbols`, the
  `nvim` on `PATH` listing `vim.lsp.buf.document_symbol()` and
  `workspace_symbol()`. On the corpus, `TestCorpusSymbols` lists the
  outline of 2,004 files of the VikingMUD lib (7,472 functions, 1,972
  inherit statements, 1,184 variables, 585 macros; 1.8 µs a file, 0.2
  ms the slowest) and holds each to the file and to the index: every
  function and variable the unit has of the file itself is in the
  outline at its place. Then it scans the lib (4.7 s) and searches it:
  41 to 119 ms a query, 48 ms on average over 400 names of the sample,
  each of which is the first of its results; every symbol found is
  held to the text at its place.
- **As built, completion (step 7).** In `pkg/syntax`: `TestScopes`
  (parameters and the outermost block as one scope, nested blocks and
  shadowing, a variable from its declarator on, `catch (e)`, an
  unclosed block) and `TestScopeUnsure`. In `pkg/index`:
  `TestDirEntries`, `TestDirEntriesInStubs`, `TestDirEntriesOnDisk`. In
  `pkg/resolve`: `TestLocals` (definition and hover of parameters and
  locals, shadowing, a statement that did not parse) and, for
  completion, `TestCompleteInCode` (every kind of item, its detail,
  origin, rank and comment), `TestCompletePlaces` (what the cursor is
  in or behind, where statements, expressions, declarations and the
  value of a global stand, and where nothing is said),
  `TestCompleteMembers` (`::`, `label::`, `->` on `this_object()`, a
  path and a path macro, and nothing on any other receiver; callbacks),
  `TestCompletePaths`, `TestCompletePartialCheckout` (stubs, the auto
  object and the kfuns of a profile, paths through both),
  `TestCompleteNoLib`, `TestCompleteCapped` and
  `TestCompleteEveryPrefix` (a file typed a byte at a time). In
  `pkg/lsp`: `TestCompletionIsResolve` holds the server's answer to the
  library's at every offset of every file of a lib, in both encodings,
  with Markdown and snippets and without; `TestCompletionContent`
  (kinds, sort text, snippets, `completionItem/resolve`) and
  `TestCompletionTrigger`; the transcript `completion.txt`, and the
  capability in the others. In `editors`: `TestNeovimCompletion`, the
  `nvim` on `PATH` completing with `CTRL-X CTRL-O` behind characters of
  several bytes. On the corpus, `TestCorpusComplete` takes a name out
  at 22,202 sampled places of 2,000 files of the VikingMUD lib and asks
  for a completion where it stood: the name is offered at 20,112 of the
  20,126 places where the tools know it (99.93%; locals, globals,
  kfuns, callbacks, `::fn` and `label::fn` 100%, macros 99.1%, `->` on
  a known object 99.6%), and nothing at the 1,118 calls on a receiver
  nobody knows. 0.47 ms a completion on average, 14 ms the slowest;
  570 items for the median, 990 at the 95th percentile, 1,481 at most,
  never the cap. The misses are code that does not parse around the
  place, a label macro after `inherit`, and a macro after `->`.
  `TestCorpusCompletePartial` does the same in ten checkouts with the
  profile: 11,910 of 11,915 (99.96%), 0.73 ms on average.
  `TestCorpusResolve` and `TestCorpusHover` count locals and parameters
  now (20,983 uses and 3,932 declarations of locals, 10,094 and 3,612
  of parameters in the sample, every one resolved and held to its
  declarator).
- **As built, references and highlights (step 8).** In `pkg/syntax`:
  `TestPathsAt` (one walk for the paths of every token, against
  `PathAt`), and in `TestCorpusParse` the same for every file of up to
  5,000 tokens. In `pkg/resolve`: `TestReferences` (each kind; every
  form of call; a redefinition and its callers apart; unknown receivers
  counted and not listed; a local hiding a global; locals and
  parameters in their scope; macros in directives, other macros,
  headers and `inherit` statements; objects; headers; with and without
  the declarations; nothing for what has no declaration),
  `TestHighlights` (read and written), `TestReferencesAreAt` (at every
  offset of every file of a lib, the references are the places where
  At, asked at every offset of every file, names one of the same
  declarations, and the highlights those of the file),
  `TestReferencesPartialCheckout` (a stub's declaration, the checkout's
  places, `Partial`), `TestReferencesAfterChange` (a file and a header
  changed). In `pkg/lsp`: `TestReferencesAreTheLibrarys` holds both
  answers to the library's at every offset of every file of a lib, in
  both encodings, with and without the declarations;
  `TestReferencesInCheckout` (the stub's URI, the log's note); the
  transcript `references.txt`, and the two capabilities in the others.
  In `editors`: `TestNeovimReferences`, the `nvim` on `PATH` listing
  references (`vim.lsp.buf.references`) and highlighting a local read
  and written (`vim.lsp.buf.document_highlight`). On the corpus,
  `TestCorpusReferences` (above, "As built, step 8").
- **As built, semantic tokens (step 9).** In `pkg/resolve`:
  `TestTokens` and `TestTokensOfEachKind` (every row of the legend, and
  what has no token: strings, registrars, private functions of a
  parent, code not compiled, `function` without closures),
  `TestTokensAreAt` (at every offset of every file of two libs, the
  token is what At says there, and none where At knows of no
  declaration; every token At's). In `pkg/lsp`: `TestTokensAreTheLibrarys`
  (the tokens of every file of a lib through the server, decoded by the
  test from the text, are `Document.Tokens`, in both encodings; every
  range of lines is the tokens that begin in it), `TestTokensDelta` (200
  random edits in each encoding: the delta applied to what the client
  has is what `full` answers; a delta from an old id is all of them),
  `TestTokenLegend`; the transcript `tokens.txt` (full, range, delta,
  the refresh after a parent changed), and the capability in the others
  (`references.txt` waits for the first diagnostics now, which orders
  the log). In `editors`: `TestNeovimTokens`, the
  `nvim` on `PATH` showing the tokens of a file and asking for a delta
  after an edit; `TestVSCodeClient` holds `semanticTokenScopes` and
  `semanticTokenModifiers` to the legend. On the corpus:
  `TestCorpusTokens` (`pkg/resolve`: every file of the lib, each token
  in order and on its line, one file in 20 held to At at every offset)
  and `TestCorpusSession` (the tokens of every file it opens, through
  the server, each within its line).
- **As built, rename within a file (step 10).** In `pkg/rename`:
  `TestRenameKinds` (a local, a parameter, an inner block's local, a
  `catch` variable, a private function with its prototype and the
  strings left with a warning, a private variable static or not, a
  macro, a label, the old name again), `TestPrepare` (what is renamed
  at each kind of place, and the refusals of the symbol),
  `TestRefusals` (a saved variable, by the file and by an inheritor;
  a file another includes; code not compiled, not parsed, a macro's
  opaque arguments; a header that names the macro; a macro's body
  naming the old or the new name), `TestClashes` (each check of the new
  name, for each kind), `TestRefusalsPartialAndUnscanned` (a stub's
  function and macro; a lib not scanned). In `pkg/lsp`:
  `TestRenameIsTheLibrarys` (at every offset of every file of a lib, in
  both encodings, prepareRename and rename answer what
  `Document.PrepareRename` and `Rename` say); the transcripts
  `rename.txt` and `rename-plain.txt`, and the capability in the
  others. In `editors`: `TestNeovimRename`. On the corpus:
  `TestCorpusRename` (above, "As built, step 10").
- **As built, rename across files (step 11).** In `pkg/rename`:
  `across_test.go` (each kind of symbol across files, the family of
  redefinitions, every form of call, the classes and their reasons, a
  saved variable refused and forced, the clashes across files, a
  private function of an included file, a partial checkout with its
  warning and the profile's function refused), and step 10's tests with
  what is renamed now. `TestDynamic` (`pkg/calls`), `TestDynamicUses`
  (`pkg/resolve`), `ChainVar` in `TestChainFuncIsChain` (`pkg/index`).
  In `pkg/lsp`: `TestRenameIsTheLibrarys` (every offset, both
  encodings: the edits of every file, their versions and annotations
  are the library's plan), the transcripts `rename-across.txt` and
  `rename-across-plain.txt`; the transcript player makes a rename's
  edits in every file, open or not, and leaves those that need
  confirmation. In `cmd/dgdtools`: `TestRenameDiff`, `TestRenameWrite`,
  `TestRenameForce`, `TestRenameJSON`, `TestRenameErrors`. In
  `editors`: `TestNeovimRename`. On the corpus:
  `TestCorpusRenameAcross` (section 8, "As built, step 11").
- **As built, the refactorings (step 12).** In `pkg/rename`: `TestFiles`
  (every way a path is written, a header to and within an include
  directory, a directory, out of the lib), `TestRenameDriverCalls`,
  `TestClashThroughMacroAcross`. In `pkg/lint`: `TestPrototype`,
  `TestPathFixes`. In `pkg/lsp`: the transcripts `move.txt` (the player's
  `move` sends `workspace/willRenameFiles`, makes and saves the edits,
  and moves the file or folder on disk) and `refactor.txt`. In
  `cmd/dgdtools`: `TestRenameMove`. In `editors`: `TestNeovimRefactor`.
  On the corpus: `TestCorpusRenameFiles` (section 8, "As built, step
  12").
- **Edit fuzzing** (`go test -fuzz`): start from a corpus file, apply a
  random sequence of range edits through incremental sync while applying
  the same edits to a plain string; after each step the server's buffer
  must equal the string, no handler may panic, every published range must
  lie inside the document, and positions must round-trip through
  `PositionMapper` in both encodings (including lines with non-ASCII and
  astral characters).
- **Equivalence with the CLI:** for every file of a fixture lib, the
  diagnostics published after `didOpen` equal `dgdtools lint
  --output-format json` for that file; after an edit and its undo they
  are equal again (catches stale invalidation).
- **Invalidation:** change a header and assert that exactly the open
  dependents are re-published; change `dgdtools.yml` and assert a rebuild.
- **Cancellation and ordering:** a burst of 200 `didChange` notifications
  must produce diagnostics only for the final version; run under `-race`.
- **Corpus smoke test** (`-tags corpus`): open, edit one character in,
  and close a sample of several thousand real files; no panics, bounded
  memory, latency percentiles logged (target: diagnostics for a
  1,000-line file within 50 ms of the debounce firing once its chain is
  loaded).
- **Encoding fixtures:** a Latin-1 file opened as UTF-8 text with
  replacement characters, and the same file opened as correctly decoded
  text: both must produce in-range diagnostics and the same findings.
- **Rename.** `pkg/rename` on fixture libs, as golden plans: one
  occurrence of every class and every reason, each warning and each
  refusal, the same symbol in a whole lib and in a checkout with a
  profile. The command and the server are held to the same plan (the
  diff of `dgdtools rename` equals the certain edits of the
  `WorkspaceEdit`). On the corpus (`-tags corpus`, on a copy): for a
  sample of functions, apply the certain edits and lint again: the
  findings are those of before under the new name, the token streams
  differ only in the renamed tokens, and renaming back restores every
  byte.
- Client side: the VS Code extension gets one integration test
  (`@vscode/test-electron`) that opens the fixture lib and waits for a
  known diagnostic. The grammars are tested from Go (10.1).

  **As built:** the integration test is not there. `@vscode/test-electron`
  is a small package that downloads VS Code itself (well over 100 MB,
  at each run in CI) and needs a display, which there is one more job
  with `xvfb`;
  and a test of that kind written where it cannot be run once would be
  a guess. It is the next thing to add once the client has been seen to
  work, as the one test that starts the real editor. Until then the
  client has the checks of 11.1, none of which starts VS Code.

## 14. Decisions

Each with the alternative it was chosen over.

- **JSON-RPC: `github.com/sourcegraph/jsonrpc2`**, not a hand-rolled
  transport (section 2).
- **`dgdtools lsp`, a subcommand of the one binary**, not a separate
  server binary: one thing to install, and the server cannot be a
  different version from the linter and the formatter.
- **Diagnostics for open documents only**, the default of gopls and
  rust-analyzer. Whole-project diagnostics (pull diagnostics) on a lib
  with hundreds of thousands of baselined findings are a later step.
- **Latin-1 files stay as they are; editors are configured for them**
  (section 6). Converting a lib to UTF-8 is the lib owner's decision,
  and the server works either way.
- **File association is workspace-scoped:** `*.c`/`*.h` are `dgdlpc`
  only under a `dgdtools.yml`, or in a folder the user said is a lib;
  never claimed globally.
- **The language is called `dgdlpc`** in VS Code, Vim and the other
  editors, not `lpc` (section 11).
- **Profiles come from the search path** (`profile.Resolve` finds
  `NAME.profile` next to the config, in `$DGDTOOLS_PROFILE_PATH`, or in
  the user config directory, and the server uses exactly that). The
  extension documents where to put the file. A command that downloads a
  profile from a URL the user configures would live in the extension,
  not in dgdtools, which has no network code; it is not planned.
- **The tree-sitter grammar is deferred** (10.1): 6 to 8 days and a
  second grammar to maintain, for Helix and Zed highlighting.
- **A separate `dgdlpc` Vim filetype**, not a DGD mode in the stock
  `lpc.vim` (10.1).
- **Stubs open as read-only virtual documents**, rather than a
  definition in the profile resolving to nothing. Their text is asked of
  the server with the protocol's own request
  (`workspace/textDocumentContent`, LSP 3.18), not with one of ours and
  not by a provider the extension would write: the client library has
  it, and so will any other client (11.1).
- **Editor support lives in this repository**, under `editors/` (10.1),
  so a grammar, a client and the server they talk to are one commit.
- **The grammars are tested by `go test`**, by running them, not by a
  Node tool: the repository needs nothing but Go to keep them right, at
  the price of a regular expression syntax both engines share (10.1).
- **One rename engine for the editor and the command line; no
  deprecation machinery** (section 9).
- **A diagnostic's code is the rule's code** (`R001`), as in every
  other output; the name is in the link and in the suppression actions.
- **The editor's settings say two things**, the profile and the lib path
  of a folder without a config file, and a config file wins over them.
  Everything else is the project's: an editor cannot make the server
  disagree with the command line.
- **A closed document has no diagnostics**: what nothing keeps true is
  not shown.
- **A definition is proved or not given**, as a finding is: an unknown
  receiver, a function only an inheritor has, and the auto object's
  function below an object the index cannot see all answer nothing,
  rather than the likeliest place (section 8, "As built, step 4").
- **An `inherit` statement goes to the object**, whatever its path is
  written with; a macro elsewhere goes to its `#define`.
- **The kfun table is read out of the driver's source**, by a test
  that also writes it, not typed in from the manual: the prototypes are
  the driver's own bytes, and a new driver version is a diff of one
  file. The price is that parameters have no names, the driver having
  none (section 8, "As built, step 5").
- **A driver's extension kfuns are the project's data**
  (`dialect.kfuns`, carried by the profile), never the table's: no
  mud's module is in the tool.
- **A kfun is told only where the program is all known**, as the auto
  object's function is: a lib's auto object redefines the driver's
  functions, and to show the driver's `clone_object(object)` for the
  lib's `clone_object(string)` would be worse than to show nothing.
- **Signature help reads tokens, hover reads the tree.** A call being
  written does not parse; a name being read does. The two are held to
  each other where both can speak.
- **The arguments that `call_other` and `call_out` pass on show the
  called function's signature**, not the registrar's `mixed...`: that
  is the function being written to. The registrar's own arguments show
  its own.
- **An outline has one level**, and an inherit statement is in it
  under the object it inherits: that is what the file is made of.
- **A search by name walks the units**, with no table of names of its
  own: the units are there, shared across generations, and a table
  would be a second copy of every name to rebuild at each change.
- **Fuzzy matching is a subsequence and four ranks**, not a scoring
  model: what gopls and rust-analyzer do as far as a user can tell.

- **The VS Code extension is published under the repository owner's
  account**, to both the Visual Studio Marketplace and Open VSX (so
  VSCodium and Cursor users get it too): publisher `mmcdole`, extension
  `mmcdole.dgdtools`.
- **The VS Code client is TypeScript bundled into one file**, with one
  runtime dependency, not plain JavaScript without a build: the types
  are its first test, and the client library has to be fetched either
  way (11.1).
- **Which files are LPC is a workspace setting the user is asked
  about**, `files.associations` with patterns for that folder; the
  extension never sets a document's language behind the user's back
  (but for a profile's stub), and never writes user settings (11.1).
- **A release carries the binary in a package of the extension for
  each platform**, as rust-analyzer and Ruff do, and the extension
  downloads nothing (11.2). The package's program runs before the one
  on `PATH`, and `dgdtools.path` before both.
