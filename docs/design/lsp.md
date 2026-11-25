# `dgdtools lsp`: implementation plan

Status: a plan, of which the first three steps of section 12 are built:
the editor-side grammars of section 10.1, under `editors/`; the server
itself for steps 1 to 6 of section 8 (live diagnostics, code actions,
formatting, go to definition, hover and signature help, the outline of a
document and the search for a name in the lib), as
`dgdtools lsp` and `pkg/lsp`; and the
VS Code client of
section 11, in `editors/vscode`, which has been built and checked but
not yet run in a VS Code. "What is built" below says exactly what, and
where it differs from the plan. The rest of section 8 is still a plan,
and the command reserves the name `rename`.
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
| `actions.go` | `textDocument/codeAction`, `formatting`, `rangeFormatting` |
| `definition.go` | `textDocument/definition`: `lint.Document.Resolve`'s answer as links or locations |
| `hover.go` | `textDocument/hover` and `textDocument/signatureHelp`: `lint.Document.Hover`'s and `Signature`'s answers laid out as Markdown or plain text |
| `completion.go` | `textDocument/completion` and `completionItem/resolve`: `lint.Document.Complete`'s items as the protocol's, and the comment above a declaration when an item is asked for |
| `symbols.go` | `textDocument/documentSymbol` and `workspace/symbol`: `lint.Document.Symbols`' outline and `index.Snapshot.FindSymbols`' matches as the protocol's symbols |
| `content.go` | `workspace/textDocumentContent`: the text of a profile's stub |
| `position.go` | `PositionMapper`, URIs and paths |
| `protocol.go` | the protocol's types, as far as used (some 60) |
| `lsptest/` | a client in the test's own process, over a pipe |

Capabilities announced: `positionEncoding` (`utf-8` when the client
offers it, else `utf-16`), `textDocumentSync` (open and close,
incremental changes), `codeActionProvider` (`quickfix`,
`source.fixAll.dgdtools`; only to a client with code action literals),
`completionProvider` (asked for at `>`, `:`, `"`, `/` and `<`, with
`completionItem/resolve`), `definitionProvider`, `hoverProvider`,
`signatureHelpProvider` (asked for at `(` and `,`, and again at `)`),
`documentSymbolProvider`,
`workspaceSymbolProvider`,
`documentFormattingProvider`, `documentRangeFormattingProvider`,
`workspace.workspaceFolders` (with change notifications),
`workspace.textDocumentContent` for the scheme `dgdtools-profile`. Asked
of the client when it can: `workspace/configuration`,
`client/registerCapability` for `workspace/didChangeWatchedFiles`,
`window/workDoneProgress/create`.

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
`ObjectEntries`) ("As built, step 7").

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
| 8 | `references` (and `documentHighlight`) | new: a reverse index of call sites and macro uses per name, built with the whole-lib scan and invalidated per file | reverse index in `pkg/index`; in a partial checkout results are labelled as "in this checkout" | 4 |
| 9 | `semanticTokens` (section 10.2): what a grammar cannot know. Ships in two parts: 9a macros by class, declarations, inherit labels, calls resolved through the chain (no scopes needed); 9b parameters/locals and unresolved names | tree + view + chain | 9b needs scopes (from 7) | 9a: 2, 9b: 2 |
| 10 | `prepareRename` and `rename` for the symbols one file decides (section 9): locals and parameters, private functions and variables, macros a `.c` file defines for itself | `pkg/rename` (new) over the tree, the scopes and the file's `pp.Env` | scopes (from 7); `syntax.PathAt` (from 4) | 3 |
| 11 | Rename across files, in the editor and as `dgdtools rename SYMBOL NEW` (section 9): functions, object globals, header macros; every occurrence classified certain, possible or excluded; edits previewed with change annotations, or written as a patch | `pkg/rename` over the reverse index, who inherits a file (a `Query` on the snapshot), `Chain`, `LookupCallable`, `calls.Extract` | the reverse index (from 8); the whole-lib scan; step 10 | 9 |
| 12 | Refactorings as code actions (section 9.6): add the missing prototype, fix an `#include` path; renaming a file updates the `inherit` and `#include` lines that name it (`workspace/willRenameFiles`) | `ResolveInclude`, `ObjectPath`, the include and inherit edges `Invalidate` already follows | the reverse index (from 8) for the file rename | 3 |
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
| the path of an `inherit` statement, any token of it | the object's file. `inherit ROOM;` is about the room: the macro's `#define` is not offered there (in the shared directories of the lib measured, 95% of the inherit statements are written with a macro) |
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
reference index, `pkg/rename`. A lint already returns the parsed tree,
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
  ("Treat This Folder as an LPC Lib") and its inverse,
  `dgdtools.dissociateFolder`. The two are in the command palette, for
  the workspace's folder (asked for when there are several), and in the
  explorer's menu of a folder once the extension is active: a menu of
  every folder of every workspace is no place for them otherwise.
- **What "an LPC lib" is written as.** `files.associations` in the
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
   (section 8), and the kfun table of the next item with them.
5. Scopes and kfun table in the library (also unlock linter rules:
   undeclared variable, undefined function, argument counts); step 7 and
   semantic tokens 9b (7 days); then step 10, rename within a file (3
   days), which needs the scopes and nothing else.
6. Reverse index; step 8 (4 days).
7. The rename engine across files with `dgdtools rename` (step 11, 9
   days) and the refactorings of step 12 (3 days). The command line
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
  the end. Not built: the VS Code integration test (the client is the
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
