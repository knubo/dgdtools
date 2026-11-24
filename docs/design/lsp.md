# `dgdlint lsp`: implementation plan

Status: plan only. Nothing here is implemented. It builds on the library
described in `dgdlint-rewrite.md` (sections 6 and 10): `pkg/lint`
(`Linter.Run`, `Invalidate`), `pkg/vfs` (`Overlay`), `pkg/index`
(chains, macro views, declarations with types), `pkg/profile`,
`pkg/format`, `pkg/config` (`Discover`).

The server is a thin layer: transport, document state, scheduling and
position conversion. Analysis stays in the library; any logic the server
needs that a script or the CLI could also use goes into the library first.

## 1. Shape

- `dgdlint lsp` subcommand (one binary to install and version; the server
  must always match the linter). stdio transport only; `--log FILE` and
  `--trace` flags. stdout is the protocol, stderr is never written to
  except on startup failure.
- New package `pkg/lsp` (server, document store, scheduler, protocol
  subset) and `pkg/lsp/lsptest` (in-process client for tests).
  `cmd/dgdlint` only wires stdin/stdout to it.

## 2. Transport and protocol types

**JSON-RPC:** use `github.com/sourcegraph/jsonrpc2` (with its
`VSCodeObjectCodec` for `Content-Length` framing).

- Hand-rolling the framing is 100 lines; the part that goes wrong is the
  rest: request/response correlation in both directions (the server also
  *sends* requests: `workspace/configuration`, `client/registerCapability`,
  `workspace/applyEdit`), `$/cancelRequest` mapped to context
  cancellation, concurrent writers, and orderly shutdown. That is exactly
  what the library does, in ~1,500 lines with no dependencies of its own,
  and it has years of production use (Sourcegraph, many Go language
  servers).
- `golang.org/x/tools/internal/jsonrpc2` (gopls) cannot be imported;
  `golang.org/x/exp/jsonrpc2` is its experimental copy with no stability
  promise. `go.lsp.dev/jsonrpc2` is fine but comes paired with
  `go.lsp.dev/protocol`, which is what we should avoid (next point).
- If the project's dependency policy is "none beyond yaml", hand-rolling
  is acceptable and bounded (2 days including cancellation); the handler
  layer below is written against a 3-method interface (`Notify`, `Call`,
  `Reply`) so the choice is swappable.

**Protocol types:** hand-written structs for the subset used (about 40
types), in `pkg/lsp/protocol.go`. Generated full-spec packages lag the
spec (position encoding negotiation, pull diagnostics), are large, and
make every optional field a pointer; we use perhaps 5% of the spec.

**Dispatch:** notifications that change state (`didOpen`, `didChange`,
`didClose`, `didChangeWatchedFiles`, `didChangeConfiguration`) are handled
strictly in arrival order on one goroutine. Requests are handled
concurrently against an immutable snapshot (document version + linter),
each with a context cancelled by `$/cancelRequest` or by a newer version
of its document.

## 3. Workspaces, projects and profiles

- `initialize` gives `workspaceFolders` (fallback `rootUri`).
  `workspace/didChangeWorkspaceFolders` is supported.
- A **project** is what `config.Discover` returns: config file, lib root
  or partial-checkout mount, profile. Projects are keyed by
  (config path, profile, lib path) and each owns one `vfs.Overlay` view
  and one `lint.Linter`.
- A **document** finds its project by running discovery from its own
  directory, exactly like the CLI (nearest `dgdtools.yml` upward), cached
  per directory. Two workspace folders under one lib share one project;
  one folder containing two libs gets two. A file with no project gets
  the shared "file-local" linter (no root).
- **Zero-config wizards:** client settings `dgdlint.profile` and
  `dgdlint.libPath` (read with `workspace/configuration`, per folder) are
  passed as `DiscoverOptions.Profile/LibPath` for folders that have no
  `dgdtools.yml`. A config file always wins over editor settings. No
  profile is ever assumed (design doc 10.4); the extension's settings UI
  is where a wizard names their mud's profile (resolved with
  `profile.Resolve`, so a profile installed in the user config directory
  works for every folder).
- **Watching:** register `workspace/didChangeWatchedFiles` for
  `**/*.{c,h}`, `**/dgdtools.yml`, `**/.dgdtools.yml`, `**/*.profile`,
  `**/dgdlint-baseline.json`. Source changes go to `Linter.Invalidate`;
  a config, profile or baseline change rebuilds that project's linter and
  re-lints its open documents.
- The baseline applies in the editor as on the command line; a setting
  `dgdlint.showBaselined` shows baselined findings as hints.

## 4. Documents and text synchronization

- Sync kind **Incremental**. Per document: URI, fs path, version, text as
  `[]byte` (UTF-8, section 6), line-start table.
- `didChange`: apply each range edit to the buffer (positions converted
  with the negotiated encoding against the *pre-edit* line table), rebuild
  the line table from the first changed line, then
  `overlay.Set(path, text)`, `changed := linter.Invalidate(path)`, and
  schedule diagnostics for the document and for every *open* document in
  `changed`.
- `didOpen` sets the overlay even if the text equals the disk file (the
  editor is now the source of truth); `didClose` deletes the overlay
  entry, invalidates, and clears the document's diagnostics unless the
  file is in a project (then they are recomputed from disk once).
- `didSave` needs nothing (the watcher covers other processes).
- A full-document fallback (`TextDocumentSyncKind.Full`) is kept for
  clients that do not do incremental sync; the fuzz test (section 12)
  proves both paths produce the same buffer.

## 5. Scheduling: debounce, cancellation, invalidation

- One scheduler per project. A change marks the document dirty and
  (re)starts a 200 ms timer; when it fires, the document is linted at its
  then-current version with a fresh context.
- A newer change to the same document cancels the running lint
  (`context.CancelFunc`); the library already checks the context between
  files and during the whole-lib scan, and a cancelled reverse lookup
  answers "unknown", so a cancelled run can never publish a wrong
  "missing".
- Results are published only if the document's version still equals the
  version the run started with; diagnostics carry that version.
- Dependents (open files that include or inherit the changed file) are
  queued behind the edited document at lower priority, with a longer
  debounce (1 s), so typing in a header does not re-lint twenty rooms per
  keystroke.
- The first `callable-not-found` self-call in a module triggers the
  index's one-time whole-lib scan (8 s on VikingMUD). It runs on a
  background goroutine with `$/progress` ("indexing lib"); until it
  completes those lookups are unknown and the document is re-linted when
  it finishes. In a partial checkout the scan covers only the checkout
  and takes milliseconds.

## 6. Positions and encodings

Two separate questions.

**What a column counts.** LSP's default is UTF-16 code units. LSP 3.17
lets the client offer `general.positionEncodings`; if it offers `utf-8`
the server selects it and a column is simply a byte offset in the line,
which is what the lexer counts: no conversion at all. Otherwise UTF-16
via `diag.UTF16Col` and its inverse (to be added: `diag.ByteCol(line,
utf16)`), computed on the single line involved. Both directions live in
one small type, `lsp.PositionMapper`, used for every incoming position
and every outgoing range.

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
  `"[lpc]": {"files.autoGuessEncoding": true}` and documents
  `"files.encoding": "iso88591"` for libs that are Latin-1; the server
  logs a one-time warning (`window/showMessage`) when a file on disk is
  not valid UTF-8 and the opened text contains U+FFFD.
- The server never writes source files.

## 7. Robustness

- Every handler runs under `recover`: a panic becomes an `InternalError`
  response (or is swallowed for a notification), is logged with a stack,
  and the server keeps running. The index already isolates a panic while
  analyzing one file.
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

## 8. Features, in order

Each step is shippable. Estimates are working days for one person who
knows the code base.

| # | Feature | Maps onto | Needs first | Days |
|---|---|---|---|---|
| 1 | Skeleton: initialize/shutdown, projects, incremental sync, `publishDiagnostics` with code, `codeDescription`, related information, tags (`Unnecessary` for unreachable-code) | `Linter.Run`, `Invalidate`, `diag` | `PositionMapper`; `diag.ByteCol` | 5 |
| 2 | `codeAction`: quick fixes from `Diagnostic.Fixes`; "suppress with `dgdlint:disable-next-line RULE`" and "…for this file" generated per diagnostic; "explain rule" as a command | fixes as text edits (already data) | a helper that renders a suppression comment in the file's comment style and indentation | 2 |
| 3 | `formatting` (whole document) via dgdfmt; `rangeFormatting` deferred | `pkg/format` with its token-equality gate; a failed gate returns no edits and a log line | minimal-diff edit computation (line-based) so cursors and folds survive | 2 |
| 4 | `definition`: `#include` targets, `inherit` paths, function calls (direct, `::`, `label::`, `->` with a known receiver, string callbacks in registrars), macros | `ResolveInclude`, `ObjectPath`, `Chain`, `LookupCallable`, macro view (`pp.Macro.Path/Off`) | `syntax.PathAt(file, offset)` (innermost node chain at a position); for stub targets, answer with a read-only virtual document `dgdlint-profile:/std/room.c` showing the stub | 4 |
| 5 | `hover` and `signatureHelp` | `ChainFunc.Decl/Type/DefinedIn`, `index.Params`, macro body and class, variable types | active-parameter computation from the argument list around the cursor; kfun table for built-ins (new data file) | 3 |
| 6 | `documentSymbol`, `workspace/symbol` | `syntax.File.Decls`; units' symbol tables (whole-lib scan, cached) | fuzzy matcher | 2 |
| 7 | `completion`: functions callable here (own chain, auto object, kfuns), `->` members for a known receiver, macros in view, `#include` and inherit paths, locals | chain + view + profile | local scopes (block-level symbol tables in `pkg/syntax`); tolerant parse at the cursor (the parser already recovers; completion context comes from tokens left of the cursor) | 5 |
| 8 | `references` (and `documentHighlight`) | new: a reverse index of call sites and macro uses per name, built with the whole-lib scan and invalidated per file | reverse index in `pkg/index`; in a partial checkout results are labelled as "in this checkout" | 4 |
| 9 | `semanticTokens` (section 9.2): what a grammar cannot know. Ships in two parts: 9a macros by class, declarations, inherit labels, calls resolved through the chain (no scopes needed); 9b parameters/locals and unresolved names | tree + view + chain | 9b needs scopes (from 7) | 9a: 2, 9b: 2 |
| later | `rename` (needs references to be complete: only offered when the whole lib is present), inlay hints for parameter names, pull diagnostics (`textDocument/diagnostic`, `workspace/diagnostic`), call hierarchy over inherit chains | | | |

Steps 1 to 3 (9 days) are the "useful every day" milestone: live
diagnostics, fixes, formatting. Steps 4 to 6 (9 days) make it a
navigation tool. Steps 7 to 9 (13 days) need two pieces of library work
that are also on the linter's own roadmap: local scopes and a kfun table.

Library additions, all testable without a server: `syntax.PathAt`,
`diag.ByteCol`, block scopes, kfun table (name, parameters, return type),
reverse reference index, `Linter.Lint(ctx, path)` as a single-file
convenience returning the parsed tree with the diagnostics.

## 9. Syntax highlighting: two layers

Highlighting is split the way mature language tooling splits it: a
grammar in the editor for the lexical layer, and semantic tokens from the
server for what only analysis knows. The first must work with no server,
offline, on a file that does not parse. The second is an overlay that may
arrive late or not at all.

### 9.1 Layer 1: editor-side grammars

All grammar files live in this repository under `editors/`, are generated
from or checked against one shared word list
(`editors/common/dgd-lpc.json`: keywords, type keywords, specifiers,
operators, directive names, taken from `pkg/token`), and are tested
against the same sample files (`editors/testdata/*.c`).

```
editors/
  common/dgd-lpc.json          word lists shared by all grammars
  testdata/                    sample sources + expected scopes
  vscode/                      extension (section 10)
    syntaxes/dgd-lpc.tmLanguage.json
    language-configuration.json
  vim/
    syntax/dgdlpc.vim  ftdetect/dgdlpc.vim  ftplugin/dgdlpc.vim
  tree-sitter-dgd-lpc/         optional, deferred (below)
  neovim/ helix/ zed/          configuration snippets and queries
```

**TextMate grammar** (VS Code; also usable by other editors such as
Sublime and by GitHub's Linguist if submitted). Scope: comments (`/* */`,
and `//` as a separate rule the extension enables from
`dialect.slash_slash`, default on); preprocessor directives with their
own name set (`#include <…>`/`"…"`, `#define NAME(params)` with the macro
name scoped, `#if/#ifdef/#else/#endif`), continuation lines; strings and
character literals with DGD's escapes (`\ooo`, `\xHH`, any other
character); numbers (decimal, hex, floats, and `1..2` *not* as a float);
DGD keywords, exactly the driver's reserved set (`nil`, `atomic`,
`rlimits`, `catch`, `nomask`, `varargs`, `operator`, `inherit`, `new`,
`goto`, `try`; `function` only when `dialect.closures`); type keywords;
the `({ … })` and `([ … ])` literal brackets as their own punctuation
scopes so themes and bracket matching treat them as pairs; `::`, `->`,
`<-`, `..`, `...`; function names at definition (`name(` at column 0 or
after a type) and at call sites, by pattern. It deliberately does **not**
try to tell macros from identifiers beyond the ALL_CAPS convention
(scoped `constant.other`), and does not list kfuns: both are layer 2's
job, where they are known rather than guessed. Tests:
`vscode-tmgrammar-test` snapshots over `editors/testdata`. **2 days**
including tests.

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
set `dgdlpc` for `*.c`/`*.h` only when a `dgdtools.yml` is found upward
from the file (never globally), with `g:dgdlpc_slash_slash` and
`g:dgdlpc_closures` mirroring the dialect. `ftplugin` sets
`commentstring`, `comments`, `matchpairs` and `include`/`define` patterns
so `gf` and `[i` work without the server. The file is generated from the
shared word list. **1.5 days** including a scripted test (`vim -es`
dumping `synIDattr` for the sample files).

**tree-sitter grammar** (highlighting in Neovim, Helix and Zed, which use
tree-sitter rather than TextMate or Vim syntax; in Neovim also folds and
text objects). This is *for highlighting only*: it is not, and must not
become, the linter's parser. The reasons that ruled tree-sitter out for
the linter (design doc 2.1: cgo, coarse error nodes, no macro tables) do
not matter for colouring. Cost: grammar plus external scanner work for
directives embedded anywhere, `highlights.scm`, `injections.scm`,
`locals.scm`, corpus tests against real lib files (the grammar must stay
useful on unpreprocessed code where macros appear in type and statement
position), and the packaging each editor wants (nvim-treesitter parser
registration, Helix `languages.toml` + grammar source, a Zed extension
compiling the grammar to WASM): **6 to 8 days**, plus ongoing
maintenance of a second grammar. **Defer it.** Until then Neovim uses the
Vim syntax file (which Neovim supports natively) plus semantic tokens,
and Helix and Zed fall back to their C grammar, which is tolerable for
LPC and gets `({ })` and `::` wrong. Revisit when there are Helix or Zed
users asking, or when step 9 shows semantic tokens alone are not enough
in Neovim.

### 9.2 Layer 2: LSP semantic tokens

Semantic tokens add only what analysis knows; they never restate what
the grammar already colours (no tokens for keywords, strings, comments or
operators), which keeps payloads small and avoids flicker when the server
is slower than the grammar.

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
run already built, plus chain lookups that are memoized; they are
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
unresolved names) waits for block scopes.

## 10. Editor clients

- **VS Code extension** (`editors/vscode`, TypeScript, about 4 days
  on top of the grammar from 9.1):
  language id `lpc` for `*.c`/`*.h` *inside a workspace that has a
  `dgdtools.yml` or the `dgdlint.profile` setting* (activation must not
  hijack C files elsewhere: activate on `workspaceContains:**/dgdtools.yml`
  and offer a command "Treat this folder as an LPC lib"); the TextMate
  grammar from 9.1; `semanticTokenScopes` for the custom modifiers of
  9.2; `language-configuration.json` (brackets, comments,
  indentation rules); `vscode-languageclient` starting `dgdlint lsp`;
  settings `dgdlint.path`, `dgdlint.profile`, `dgdlint.libPath`,
  `dgdlint.showBaselined`, `dgdlint.trace.server`; a content provider for
  `dgdlint-profile:` documents. Binary acquisition: use `dgdlint.path` or
  PATH first; otherwise download the release asset for the platform
  (GoReleaser already builds them) with a checksum, after asking.
- **Vim and Neovim:** the `editors/vim` runtime files from 9.1 (install
  as a plugin directory), and for Neovim a documented `vim.lsp.config` block (`cmd = {"dgdlint",
  "lsp"}`, `root_markers = {"dgdtools.yml", ".dgdtools.yml"}`,
  `filetypes = {"lpc"}`) plus a filetype rule; submit to
  `nvim-lspconfig` once stable.
- **Helix:** `languages.toml` entry (language `lpc`, roots
  `dgdtools.yml`, `language-servers = ["dgdlint"]`). Helix highlights only through
  tree-sitter and does not use semantic tokens, so until the deferred
  grammar of 9.1 exists Helix users get diagnostics and navigation with C
  highlighting.
- **Zed:** an extension declaring the language server and reusing the C
  grammar until the tree-sitter grammar exists; Zed does apply semantic
  tokens on top.
- Emacs (`eglot`), Sublime (`LSP`), Kate: a line of configuration each,
  documented, not maintained.

## 11. Order of work

1. **Grammars first** (3.5 days, no server needed, useful on day one and
   independent of everything else): the shared word list, the TextMate
   grammar and the Vim syntax/ftdetect/ftplugin files, with their tests.
2. Library prerequisites for step 1 (`diag.ByteCol`, `PositionMapper`),
   `pkg/lsp` skeleton, transcript test harness. Ship steps 1 to 3 behind
   `dgdlint lsp` and test with Neovim (no extension needed) (9 days).
3. VS Code extension (client, settings, grammar packaging) against steps
   1 to 3 (4 days).
4. `syntax.PathAt`; steps 4 to 6 (9 days); then semantic tokens 9a (2
   days), which needs only what hover needs.
5. Scopes and kfun table in the library (also unlock linter rules:
   undeclared variable, undefined function, argument counts); step 7 and
   semantic tokens 9b (7 days).
6. Reverse index; step 8 (4 days).
7. Deferred until asked for: the tree-sitter grammar (6 to 8 days) with
   Helix and Zed packaging.

Total: about 31 days of server and library work, 3.5 for grammars and 4
to 5 for clients; the first usable release (grammars, live diagnostics,
fixes, formatting, VS Code and Neovim) is roughly three and a half
weeks, and grammars alone can ship in the first.

## 12. Testing

- **Golden transcripts.** A test is a `.txtar`: the lib's files, then a
  script of client messages (`open a.c`, `change a.c 3:5-3:9 "x"`,
  `request hover a.c 10:4`, `wait diagnostics a.c`) and the expected
  server messages as JSON with paths and versions normalized. An
  in-process client over `io.Pipe` runs them; `-update` rewrites the
  expectations. `--trace` output from a real editor session converts to
  a transcript, so every bug report becomes a test.
- **Edit fuzzing** (`go test -fuzz`): start from a corpus file, apply a
  random sequence of range edits through incremental sync while applying
  the same edits to a plain string; after each step the server's buffer
  must equal the string, no handler may panic, every published range must
  lie inside the document, and positions must round-trip through
  `PositionMapper` in both encodings (including lines with non-ASCII and
  astral characters).
- **Equivalence with the CLI:** for every file of a fixture lib, the
  diagnostics published after `didOpen` equal `dgdlint --format json` for
  that file; after an edit and its undo they are equal again (catches
  stale invalidation).
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
- Client side: the VS Code extension gets one integration test
  (`@vscode/test-electron`) that opens the fixture lib and waits for a
  known diagnostic; grammar tests with `vscode-tmgrammar-test`.

## 13. Open decisions

1. **Dependency:** take `sourcegraph/jsonrpc2` (recommended) or keep the
   module dependency-free and hand-roll the transport (+2 days).
2. **Binary:** `dgdlint lsp` subcommand (recommended) or a separate
   `dgdls`.
3. **Scope of diagnostics:** open documents only (recommended to start;
   matches gopls and rust-analyzer defaults) or also the whole
   project via pull diagnostics, which on a lib with 200,000 baseline
   findings needs the baseline to be in place first.
4. **Encoding policy for the lib:** leave Latin-1 files as they are and
   configure editors (recommended), or convert the lib to UTF-8 once.
   The server works either way; the risk is editors silently corrupting
   Latin-1 files on save.
5. **File association:** claim `*.c`/`*.h` only in workspaces that look
   like an LPC lib (recommended) or always.
6. **Where the VS Code extension lives** (this repository under
   `editors/vscode`, or its own) and who publishes it to the Marketplace
   and Open VSX.
7. **Profiles in editors** (decided for the tool: nothing is embedded;
   `profile.Resolve` finds `NAME.profile` next to the config, in
   `$DGDTOOLS_PROFILE_PATH`, or in the user config directory, and the
   server uses exactly that). Still open for the extension: only document
   where to put the file (recommended to start), or add a command that
   downloads a profile from a URL the user configures into the user
   config directory. The download would live in the extension, not in
   dgdlint, which has no network code.
8. **tree-sitter grammar:** defer (recommended) or build now for Helix
   and Zed highlighting (6 to 8 days and a second grammar to maintain).
9. **Vim:** ship a separate `dgdlpc` filetype (recommended) or try to
   upstream a DGD mode into the stock `lpc.vim`.
10. **Stub navigation:** show stubs as read-only virtual documents
   (recommended) or resolve to nothing when the target is not on disk.
