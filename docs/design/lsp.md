# `dgdtools lsp`: implementation plan

Status: plan only. Nothing here is implemented; the command reserves
the names `lsp` and `rename`. It builds on the library described in
[architecture.md](architecture.md) (sections 6 and 10),
[config.md](config.md) and [formatter.md](formatter.md): `pkg/lint`
(`Linter.Lint`, `Invalidate`, `Scan`, `Generation`), `pkg/vfs`
(`Overlay`), `pkg/index` (generations, chains, `#define` environments,
declarations with types and positions), `pkg/calls`, `pkg/profile`,
`pkg/format` (`Format`, `Lines`, `Edits`), `pkg/config` (`Workspace`,
`Discover`, `Project.Settings`).

The server is a thin layer: transport, document state, scheduling and
position conversion. Analysis stays in the library; any logic the server
needs that a script or the CLI could also use goes into the library first.

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
concurrently against an immutable snapshot (document version + the
linter's index generation: every `Lint` reads one `index.Snapshot` from
start to end, and `Invalidate` never disturbs one in use), each with a
context cancelled by `$/cancelRequest` or by a newer version of its
document.

## 3. Workspaces, projects and profiles

- `initialize` gives `workspaceFolders` (fallback `rootUri`).
  `workspace/didChangeWorkspaceFolders` is supported.
- A **project** is what `config.Workspace.Project(path)` returns:
  config file, lib root or partial-checkout mount, profile. The
  workspace resolves each root config once; each project owns one
  `vfs.Overlay` view and one `lint.Linter`.
- A **document** finds its project from its own path, exactly like the
  CLI (nearest root config upward; settings scopes are passed over).
  Two workspace folders under one lib share one project; one folder
  containing two libs gets two. A file with no project gets the shared
  one without a config (file-local rules; the user-level configuration
  when there is one).
- A document's **settings** are `Project.Settings(path)`: the format
  settings and the lint layers of that file (scopes, `.editorconfig`).
  The server asks the library and adds nothing.
- **Zero-config wizards:** client settings `dgdtools.profile` and
  `dgdtools.libPath` (read with `workspace/configuration`, per folder) are
  passed as `DiscoverOptions.Profile/LibPath` for folders that have no
  `dgdtools.yml`. They carry only what a wizard without any file needs;
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
- **Watching:** register `workspace/didChangeWatchedFiles` for
  `**/*.{c,h}`, `**/dgdtools.yml`, `**/.dgdtools.yml`,
  `**/.editorconfig`, `**/*.profile`, `**/dgdtools-baseline.json`.
  Source changes go to `Linter.Invalidate`; a change to a settings scope
  or an `.editorconfig` goes to `Project.ResetSettings` and re-lints the
  open documents under it; a root config, profile or baseline change
  rebuilds that project's linter and re-lints its open documents.
- The baseline applies in the editor as on the command line; a setting
  `dgdtools.showBaselined` shows baselined findings as hints.

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
  version the lint started with and `Document.Generation` is still
  `Linter.Generation()`; otherwise the document is linted again. (A lint
  that was in flight when another file changed describes the lib before
  that change, and `Invalidate` reports dependents of lints against the
  current generation only.) Diagnostics carry the document version.
- Dependents (open files that include or inherit the changed file) are
  queued behind the edited document at lower priority, with a longer
  debounce (1 s), so typing in a header does not re-lint twenty rooms per
  keystroke.
- Documents are linted with `Options.NoScan`. The first lint that
  comes back with `NeedsScan` (a self-call the module's own chain does
  not provide) makes the server run `Linter.Scan` on a background
  goroutine with `$/progress` ("indexing lib", a few seconds on
  VikingMUD); until it completes those lookups are unknown, and the
  documents that needed it are linted again when it finishes. The scan
  survives edits: after the first one, `Scan` reads only the files that
  changed, so the server simply calls it again after each `Invalidate`
  that leaves a document with `NeedsScan`. In a partial checkout the
  scan covers only the checkout and takes milliseconds.

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
  `"[dgdlpc]": {"files.autoGuessEncoding": true}` and documents
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
| 1 | Skeleton: initialize/shutdown, projects, incremental sync, `publishDiagnostics` with code, `codeDescription` (`Rule.URL`), related information, tags (`Diagnostic.Tags`: `unnecessary` for unreachable-code and unused suppressions) | `Linter.Lint`, `Invalidate`, `Scan`, `diag` | `PositionMapper`; `diag.ByteCol` | 5 |
| 2 | `codeAction`: quick fixes from `Diagnostic.Fixes` (a `safe` fix is `isPreferred` and joins "fix all"; an `unsafe` one is offered alone); "suppress with `dgdtools-ignore lint/RULE`" and "…for this file" (a `dgdtools-ignore-start lint/RULE` at its top) generated per diagnostic; "explain rule" as a command | fixes as text edits with their applicability (already data) | a helper that renders a suppression comment in the file's comment style and indentation | 2 |
| 3 | `formatting` and `rangeFormatting`; format-on-save of modified lines is the client's use of the latter | `format.Format(file, opts)` and `format.Lines(file, opts, ranges)` with `opts = format.OptionsFor(cfg, settings.Format)`: functions of the buffer and the file's settings, with the token-equality gate (a failed gate returns no edits and a log line); `format.Edits(file, out)` gives one text edit per changed gap, so cursors and folds survive | LSP range to line range (a range that ends at column 0 does not include that line) | 2 |
| 4 | `definition`: `#include` targets, `inherit` paths, function calls (direct, `::`, `label::`, `->` with a known receiver, string callbacks in registrars), macros | `ResolveInclude`, `ObjectPath`, `Chain`, `LookupCallable`, the file's `pp.Env` (`pp.Macro.Path/Off`) | `syntax.PathAt(file, offset)` (innermost node chain at a position); a stub target is already located by its virtual document URI (`dgdtools-profile://NAME/std/room.c`, see `profile.Location`), which the server answers with the stub's text | 4 |
| 5 | `hover` and `signatureHelp` | `ChainFunc.Decl/Type/DefinedIn`, `index.Params`, macro body and class, variable types | active-parameter computation from the argument list around the cursor; kfun table for built-ins (new data file) | 3 |
| 6 | `documentSymbol`, `workspace/symbol` | `syntax.File.Decls`; units' symbol tables (whole-lib scan, cached) | fuzzy matcher | 2 |
| 7 | `completion`: functions callable here (own chain, auto object, kfuns), `->` members for a known receiver, macros in view, `#include` and inherit paths, locals | chain + view + profile | local scopes (block-level symbol tables in `pkg/syntax`); tolerant parse at the cursor (the parser already recovers; completion context comes from tokens left of the cursor) | 5 |
| 8 | `references` (and `documentHighlight`) | new: a reverse index of call sites and macro uses per name, built with the whole-lib scan and invalidated per file | reverse index in `pkg/index`; in a partial checkout results are labelled as "in this checkout" | 4 |
| 9 | `semanticTokens` (section 10.2): what a grammar cannot know. Ships in two parts: 9a macros by class, declarations, inherit labels, calls resolved through the chain (no scopes needed); 9b parameters/locals and unresolved names | tree + view + chain | 9b needs scopes (from 7) | 9a: 2, 9b: 2 |
| 10 | `prepareRename` and `rename` for the symbols one file decides (section 9): locals and parameters, private functions and variables, macros a `.c` file defines for itself | `pkg/rename` (new) over the tree, the scopes and the file's `pp.Env` | scopes (from 7); `syntax.PathAt` (from 4) | 3 |
| 11 | Rename across files, in the editor and as `dgdtools rename SYMBOL NEW` (section 9): functions, object globals, header macros; every occurrence classified certain, possible or excluded; edits previewed with change annotations, or written as a patch | `pkg/rename` over the reverse index, `Snapshot.UsersOf`, `Chain`, `LookupCallable`, `calls.Extract` | the reverse index (from 8); the whole-lib scan; step 10 | 9 |
| 12 | Refactorings as code actions (section 9.6): add the missing prototype, fix an `#include` path; renaming a file updates the `inherit` and `#include` lines that name it (`workspace/willRenameFiles`) | `ResolveInclude`, `ObjectPath`, the include and inherit edges `Invalidate` already follows | the reverse index (from 8) for the file rename | 3 |
| later | inlay hints for parameter names, pull diagnostics (`textDocument/diagnostic`, `workspace/diagnostic`), call hierarchy over inherit chains | | | |

Steps 1 to 3 (9 days) are the "useful every day" milestone: live
diagnostics, fixes, formatting. Steps 4 to 6 (9 days) make it a
navigation tool. Steps 7 to 9 (13 days) need two pieces of library work
that are also on the linter's own roadmap: local scopes and a kfun table.
Steps 10 to 12 (15 days) are the editing tools, and stand on the scopes
of step 7 and the reverse index of step 8.

Library additions, all testable without a server: `syntax.PathAt`,
`diag.ByteCol`, block scopes, kfun table (name, parameters, return type),
reverse reference index, `pkg/rename`. `Linter.Lint(ctx, path, opts)`
already returns the parsed tree, tokens, `#define` environment and index
unit with the diagnostics.

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

Everything an editor needs lives in this repository under `editors/`.
The grammar files are generated from or checked against one shared word
list (`editors/common/dgdlpc.json`: keywords, type keywords, specifiers,
operators, directive names, taken from `pkg/token`), and are tested
against the same sample files (`editors/common/samples/*.c`, with the
scopes expected of them).

```
editors/
  README.md              LSP configuration for Vim (vim-lsp, ALE, coc.nvim),
                         Helix and Zed (LSP only until there is a
                         tree-sitter grammar)
  common/                the shared word list, sample files for grammar tests
  vscode/                the extension: TextMate grammar + LSP client (section 11)
    syntaxes/dgdlpc.tmLanguage.json
    language-configuration.json
  vim/                   standard runtime layout, installable by any plugin
                         manager in Vim and Neovim
    ftdetect/dgdlpc.vim    workspace-scoped detection (dgdtools.yml upward)
    syntax/dgdlpc.vim
    ftplugin/dgdlpc.vim
    indent/dgdlpc.vim
  nvim/lsp/dgdtools.lua  Neovim 0.11+ `vim.lsp.config` entry (candidate for
                         nvim-lspconfig once stable)
```

**TextMate grammar** (VS Code; also usable by other editors such as
Sublime and by GitHub's Linguist if submitted), scope name
`source.dgdlpc`. It covers: comments (`/* */`,
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
`vscode-tmgrammar-test` snapshots over the shared samples. **2 days**
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
so `gf` and `[i` work without the server; `indent` is `cindent` with the
options that give the formatter's indentation while typing. The syntax
file is generated from the shared word list. **1.5 days** including a
scripted test (`vim -es` dumping `synIDattr` for the sample files).

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
unresolved names) waits for block scopes.

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
- **Vim and Neovim:** the `editors/vim` runtime files from 10.1 (install
  as a plugin directory) set the filetype. For Neovim 0.11 and later,
  `editors/nvim/lsp/dgdtools.lua` is the `vim.lsp.config` entry (`cmd =
  {"dgdtools", "lsp"}`, `root_markers = {"dgdtools.yml",
  ".dgdtools.yml"}`, `filetypes = {"dgdlpc"}`); submit it to
  `nvim-lspconfig` once stable. For Vim, `editors/README.md` has the
  lines for vim-lsp, ALE and coc.nvim.
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

## 12. Order of work

1. **Grammars first** (3.5 days, no server needed, useful on day one and
   independent of everything else): the shared word list, the TextMate
   grammar and the Vim runtime files, with their tests.
2. Library prerequisites for step 1 (`diag.ByteCol`, `PositionMapper`),
   `pkg/lsp` skeleton, transcript test harness. Ship steps 1 to 3 behind
   `dgdtools lsp` and test with Neovim (no extension needed) (9 days).
3. VS Code extension (client, settings, grammar packaging) against steps
   1 to 3 (4 days).
4. `syntax.PathAt`; steps 4 to 6 (9 days); then semantic tokens 9a (2
   days), which needs only what hover needs.
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
  known diagnostic; grammar tests with `vscode-tmgrammar-test`.

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
  definition in the profile resolving to nothing.
- **Editor support lives in this repository**, under `editors/` (10.1),
  so a grammar, a client and the server they talk to are one commit.
- **One rename engine for the editor and the command line; no
  deprecation machinery** (section 9).

Still to decide: who publishes the VS Code extension to the Marketplace
and Open VSX.
