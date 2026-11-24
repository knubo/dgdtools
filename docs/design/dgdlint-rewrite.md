# dgdlint rewrite: design

Status: implemented on branch `dgdlint-rewrite` (see "Implemented vs.
deferred" at the end). This document records the decisions, the reasoning,
and the prior art each decision was checked against.

## 1. Purpose and non-negotiables

dgdlint exists to find real bugs in a very large, very old DGD LPC mudlib
before the driver finds them at load or boot time in production. Three
properties follow from that and are treated as constraints, not goals:

1. **Prove it or stay silent.** A finding is emitted only when the tool can
   prove it from what it can see. Anything it cannot see is *unknown*, and
   unknown never turns into an error. Wizards stop reading a linter that
   cries wolf.
2. **Broken input is normal input.** Tens of thousands of files in the
   corpus do not compile. The tool must always produce a tree, never
   panic, and never let one broken region poison findings elsewhere.
3. **Syntax errors are first-class findings.** The motivating incident was
   a production `castle.c` with `})` followed by the next statement and no
   `;`. DGD refused it at boot, a whole domain's connections vanished, and
   the old dgdlint passed the file clean because it had no parser.

The old design (lexer + top-level "structure" scanner + per-rule token
heuristics) could not satisfy (3), and satisfied (1) only by each rule
re-deriving expression structure by bracket counting. The rewrite keeps
the purpose, the lossless lexer, the formatter's token-equality gate, and
the three-valued "unknown is not missing" index philosophy. Everything
else is new.

## 2. Architecture

```
pkg/vfs      file access: OS, in-memory overlay (unsaved editor buffers)
pkg/token    lossless token model            (unchanged)
pkg/lexer    byte-lossless lexer             (unchanged)
pkg/pp       directives: #define tables, macro classes, #include targets,
             conditional-branch selection
pkg/syntax   AST + hand-written error-tolerant recursive-descent parser
pkg/config   dgdtools.yml, project discovery, DGD driver-config reader
pkg/index    lazy cross-file index over syntax trees: symbols, inherit
             chains, call sites; three-valued lookups; invalidation
pkg/lint     the embeddable linter: rules, suppressions, baseline,
             diagnostics with ranges/related locations/fix edits
pkg/report   output writers: human, JSON, SARIF 2.1.0, GitHub annotations
cmd/dgdlint  thin CLI over pkg/lint + pkg/report

pkg/structure, pkg/format, pkg/tokcmp, cmd/dgdfmt, cmd/dgdcmp: unchanged.
```

### 2.1 Why a hand-written recursive-descent parser

Checked alternatives:

| Option | Verdict |
|---|---|
| goyacc port of DGD's `parser.y` | Rejected. AGPL source must not be transcribed; LALR error recovery (`error ';'`) is exactly what we need to do better than; and a yacc grammar cannot consult macro tables mid-parse. |
| tree-sitter | Rejected. Needs cgo (the tools ship as static Go binaries), produces coarse `ERROR` nodes, and its lexer cannot be told "this identifier is a type here". |
| rowan / Roslyn red-green trees | Not needed. They pay for incremental reparse in an IDE; whole-lib lint is a ~10 s batch job and one file reparses in microseconds. Nodes instead hold token-index ranges into the lossless `token.File`, which gives the same "tree covers every byte" property by construction. |
| Hand-written RD over the existing token array | Chosen. Same choice as ruff, Biome, rust-analyzer, Roslyn, Go, and clang: full control over recovery and messages, and the only shape that can treat macros as first-class tokens. |

The grammar is written from DGD 1.7.9 grammar *facts* (productions,
precedence, which constructs exist); no driver source is copied.

### 2.2 Unpreprocessed parsing with classified macros

The linter parses source as written, like clang-format, Coccinelle and
Erlang's `epp_dodger`, not post-preprocessor text like clang-tidy. Reasons:
diagnostics and (future) fixes must map to bytes the author wrote; a real
preprocessor would need location mapping through expansions; and >96% of
the corpus parses unpreprocessed once macros are classified.

`pkg/pp` builds, per file, a layered **macro view**: the driver's
force-included header, every resolved `#include` (recursively), and the
file's own `#define`s. Each macro gets a class derived from its body:

| Class | Example | Parser treatment |
|---|---|---|
| empty / specifier | `#define public` | skipped in specifier position |
| type | `#define function mixed*`, `#define status int` | accepted as a type keyword |
| string | `#define IW "/d/Wild/"` | expression; counts as a string for path evaluation |
| expression | `#define MAX(a,b) ((a)>(b)?(a):(b))` | ordinary identifier / call; arguments that are not expressions are kept opaque |
| statement | `#define FAIL(s) { write(s); return 0; }` | may stand as a statement without `;` |
| fragment | `#define TP this_player()->`, `#define BEGIN {` | anything touching it is opaque: no syntax errors are reported from that statement |

Config can override classes (`macros:`), which is the escape hatch
clang-format provides as `StatementMacros` / `TypenameMacros`.

A macro view is **complete** only when every `#include` reachable from the
file resolved. When it is not (missing header, zero-config run, file
outside the root, a `.c` fragment that other files `#include`), the view
is *partial* and syntax findings pass an extra filter: an error is kept
only when no identifier near it could be an unseen macro (section 5).

Preprocessor conditionals: one configuration is linted, as clang-tidy does.
`#if 0` selects the `#else` branch; `#ifdef`/`#ifndef` of a name listed in
`defines:` are decided; everything else takes the first branch. Every
consumer (parser, index, rules) sees the same selection, which removes the
old tool's three inconsistent views of `#if`.

### 2.3 Error tolerance

The parser never fails and always returns a tree. Errors are data
(`syntax.Error` with a token position and a kind), as in rust-analyzer and
Roslyn.

- A missing `;` where the next token starts a new line and can start a
  statement is repaired by *virtual insertion*: one error, reported at the
  end of the previous token (where the `;` belongs), and parsing continues
  with nothing skipped. This is the castle case.
- Other statement-level errors skip, bracket-aware, to the next `;` at the
  statement's own nesting depth or to the `}` that closes the enclosing
  block. This is DGD's own `error ';'` recovery, so findings line up with
  what the driver will say.
- A typed function header (`[specifiers] type name(params) {`) seen in
  statement position is a certain error (LPC has no nested functions): the
  open function is closed with a "missing `}`" error and top-level parsing
  resumes. This bounds the damage of an unbalanced brace to one function
  instead of the rest of the file, which was the old scanner's failure
  that turned 36% of `callable-not-found` errors into false positives.
- Top-level errors resynchronize at the next `;` or balanced `{...}`.
- A declaration the parser could not understand marks the file's symbol
  table *partial*; the index propagates that to every inheritor's chain so
  no "function not found" is derived from a file we could not read.

### 2.4 Dialect

Stock DGD 1.7.9 (the production driver; `viking-dgd-extension` adds kfuns
only and builds upstream DGD unpatched) rejects three idioms that are
common in code imported from older LPmud drivers. The parser accepts all
three so the rest of the file still gets a tree, and reports each under
its own rule so a lib can baseline or disable them independently of real
breakage:

| Rule | Idiom |
|---|---|
| `adjacent-strings` | `"foo" "bar"` implicit concatenation |
| `legacy-closure` | `(: ob, "fn" :)` function-pointer literal |
| `declaration-initializer` | `int x = 5;` |

(Section 9 records what was verified against the real driver.)

## 3. Project root and configuration discovery

### 3.1 Prior art

| Tool | How the project/config is found | No config |
|---|---|---|
| ruff | nearest `pyproject.toml`/`ruff.toml` walking up **from each file**; falls back to user-level config | defaults; runs fine |
| ESLint (flat) | `eslint.config.js` walking up from cwd (v9) or from each file (v10 flag) | error: no config |
| Biome | `biome.json` walking up from cwd; stops at VCS root | defaults |
| cargo / go | `Cargo.toml` / `go.mod` walking up from cwd; the manifest directory *is* the root | error: not in a module |
| clang-tidy | `.clang-tidy` walking up from each file; compile flags from `compile_commands.json` found by walking up; without it, guesses flags and usually drowns in include errors | degraded, noisy |
| golangci-lint | `.golangci.yml` from cwd upward to the VCS/module root | defaults |

Two families: *style* tools are happy with zero config because every check
is file-local; *semantic* tools need a root because references resolve
against it. clang-tidy is the cautionary tale: when it guesses the build
configuration it emits confident nonsense.

### 3.2 Decision

dgdlint is both: syntax and vet rules are file-local, cross-file rules
need the lib root (the directory that is `/` to the driver). So:

1. **A root is never required and never guessed.** Lib-absolute paths
   resolve against the root; a wrong root would turn every `inherit` into
   a false error. No VCS-marker or directory-name inference.
2. **Discovery walks up from the linted path, not the cwd.**
   `dgdlint /any/where/lib/d/Foo/castle.c` works from any directory, as
   with ruff and clang-tidy. The nearest `dgdtools.yml` (or
   `.dgdtools.yml`) is the project; `root:` in it (default: the config's
   directory) is the lib root. With no path arguments, discovery starts at
   the cwd and the whole root is linted.
3. **One project per invocation.** If the arguments discover different
   config files, dgdlint exits 2 naming both, like `go` and `cargo` when
   packages span modules. A whole-lib index has exactly one `/`.
4. **Explicit overrides:** `--config FILE`; `--root DIR` (with or without a
   config; a config's relative paths still resolve against the config
   file); `--no-config` for defaults.
5. **Zero-config is useful, not noisy.** With no config and no `--root`,
   dgdlint runs in *file-local mode*: syntax and vet rules run with a
   partial macro view, cross-file rules are not run, and one note on
   stderr says so. The castle bug is caught in this mode.
6. **The driver config is the compile database.** DGD's own config file
   already states `include_dirs`, `include_file`, `auto_object` and
   `directory`. `driver_config: etc/mud.dgd` in dgdtools.yml reads those
   instead of asking the user to copy them (explicit `lint:` values win;
   the driver's absolute `directory` is used as root only if no `root:` is
   given and it exists on this machine). This is the role
   `compile_commands.json` plays for clang tooling.
7. **Subdirectory and single-file runs** lint only the named files but
   resolve against the whole lib. The index is lazy (section 6), so a
   single file loads only its include and inherit closure.
8. **A file outside the root** (or any path when there is no root) gets
   file-local rules only; it has no lib path, so nothing cross-file is
   claimed about it.

## 4. Configuration

- **Name/format:** `dgdtools.yml` stays (shared with dgdfmt; YAML is what
  the lib's wizards already edit). `.dgdtools.yml` is accepted too.
- **Layering:** `extends: path` (single file or list, resolved relative to
  the extending file; later wins; lists replace, maps merge). There is no
  implicit cascade of nested config files: per-directory behaviour is
  expressed in one place with `overrides`, like ESLint flat config and
  ruff's `per-file-ignores`. Hierarchical cascades are the feature ESLint
  removed.
- **Rule selection:** every rule has a stable kebab-case **name**, a short
  stable **code** (`E…` driver-rejected, `R…` runtime failure, `S…`
  suspicious, `C…` convention), a group, a default severity and a default
  on/off. `rules:` maps a rule name, code, or group (`all`, `syntax`,
  `runtime`, `suspicious`, `convention`) to `off | info | warning | error`
  or to a mapping `{severity: …, <options>}`. The legacy `enable:` /
  `disable:` lists and `rules: {name: {severity: …}}` keep working.
- **Per-path overrides:** `overrides: [{paths: [globs], rules: {…}}]`
  (lib-relative globs, `**` supported). The legacy `path_rules` (disable
  lists) is still read.
- **Suppression comments:** `dgdlint:disable-next-line rule[,rule]`,
  `dgdlint:disable-line`, and `dgdlint:disable rule` … `dgdlint:enable`
  blocks, in any comment; rule names or codes. A file-scoped suppress-all
  is deliberately not offered. `unused-suppression` (off by default)
  reports directives that suppress nothing, as ESLint's
  `--report-unused-disable-directives` and ruff's `RUF100` do.
- **Baseline:** `dgdlint --write-baseline` records current findings in
  `dgdlint-baseline.json` next to the config (`baseline:` to rename);
  subsequent runs subtract them, so legacy noise does not block CI and
  only *new* breakage gates. Entries are keyed by (lib path, rule,
  message, fingerprint of the offending source line's text) with a count,
  not by line number, so unrelated edits above a finding do not resurrect
  it. This is the approach of PHPStan/Psalm baselines and of
  `golangci-lint --new-from-rev`'s intent, without needing VCS. `--no-baseline`
  shows everything.
- **Macro and dialect knobs:** `defines:` (names treated as defined for
  `#ifdef`), `macros: {NAME: type|specifier|statement|expression|opaque}`.

### 4.1 CLI

```
dgdlint [flags] [path ...]        lint files/directories (default: project)
dgdlint rules                     list rules (code, name, group, default)
dgdlint explain RULE              documentation for one rule
dgdlint config [path]             show the resolved project and settings
```

Flags: `--config`, `--root`, `--no-config`, `--format human|json|sarif|github`,
`--output FILE`, `--select/--ignore RULES`, `--fail-on SEVERITY`,
`--baseline FILE`, `--write-baseline`, `--no-baseline`, `--local`
(file-local rules only, no index), `--stats`, `--max-per-file N`,
`--quiet`, `--version`. Old single-dash spellings (`-f json`, `-enable`,
`-disable`, `-rules`, `-fail-on`, `-config`) still work.

**Exit codes:** `0` no findings at or above `fail_on`; `1` findings;
`2` usage, configuration or I/O error. Syntax errors in linted files are
findings, not tool errors (ruff/ESLint: same).

**Output:** human (`path:line:col: severity: message [rule]`, the format
editors and `grep` already parse, plus a one-line summary on stderr); JSON
(versioned object with full ranges, related locations, fixes, and a
summary including "unknown" counters); SARIF 2.1.0 for code-scanning
upload; GitHub workflow annotations.

**Fix posture:** the core never writes files. A rule may attach fixes as
text edits (byte ranges + replacement) to a diagnostic; they are emitted
in JSON/SARIF and are what an editor would apply. `adjacent-strings`
carries one (insert ` +`). A `--fix` writer is deferred until there is a
token-equality-style safety gate for semantic edits; fixes are for editors
and reviewed application first.

**Performance/caching:** whole-lib runs are parallel over files and take
on the order of ten seconds for 108k files; a persistent on-disk cache
would save little and adds invalidation risk, so there is none. The
in-process index is lazy and invalidatable, which is what a long-lived
server needs (section 6).

## 5. Missing references and how unknowns degrade

The rule everywhere: **Found / provably Missing / Unknown**, and only
"provably Missing" may produce a finding.

| Situation | Behaviour |
|---|---|
| `#include` target not found, lib complete | `include-not-found`, error, once at the directive. Like clang's *fatal* missing header, it stops syntax reporting for the rest of that file (later errors would be consequences) and makes the file's macro view and symbol table partial. |
| `#include` target not found, but the configured include dirs / force-include are themselves absent (partial checkout, wrong root) | Unknown. One project-level note ("lib looks incomplete: /std/include missing; cross-file findings limited"), no per-site findings. |
| Inherit path the macro evaluator cannot resolve | Unknown. Chain marked partial. Visible only via the opt-in info rule `unresolved-inherit` and the `--stats` counters. |
| Inherit / `clone_object` / call target resolves to a path with no file | `target-object-missing`, error, per site (each site breaks separately and needs its own fix), unless the path matches `virtual_paths` or the lib is incomplete (then unknown). |
| Function not found in a **complete** chain | `callable-not-found`, per site. |
| Function not found in a **partial** chain (unresolved inherit, parent with unparsed regions, parent missing, include missing) | Unknown, silent. |
| Receiver of `->` is not a literal, `this_object()`, or a string macro | Unknown, silent; counted in `--stats`. |
| File outside the lib root, or no root at all | File-local rules only; one note. |
| Macro view partial | Syntax errors are kept only if no identifier at or next to the error, at the head of the statement, or in ALL_CAPS form inside the failed statement is undeclared in the file. Otherwise the statement is treated as opaque. |
| Statement touches a `fragment`-class macro | Opaque: no syntax finding. |
| File whose top level could not be fully parsed | Its own syntax errors are reported; its symbol table is partial, so nothing is concluded *about* it or its inheritors. |

Report **once at the root cause, per site for independent breakage**.
Unknowns are never findings but they are not invisible: `--stats` prints
how many inherits, includes, chains and call receivers were unknown, so a
drop in coverage is observable (the old tool hid that 93.5% of call sites
had an unknown receiver).

## 6. Library API and the path to an LSP server

dgdlint is a library first; the CLI is one frontend.

```go
fs  := vfs.NewOverlay(vfs.OS{})            // editor buffers shadow disk
prj, _ := config.Discover(fs, path, config.DiscoverOptions{})
l   := lint.New(prj, fs)                   // no globals, safe to keep alive
res, _ := l.Run(ctx, lint.Request{Paths: []string{...}})
for _, d := range res.Diagnostics { ... }  // ranges, code, related, fixes

fs.Set(path, unsavedBytes)                 // editor change
l.Invalidate(path)                         // drops that file + dependents
res, _ = l.Run(ctx, lint.Request{Paths: []string{path}})
```

Properties the core guarantees:

- **No printing, no `os.Exit`, no `flag`**, no package-level mutable state.
  The rule table is an immutable slice; each `Linter` owns its index.
- **All file access goes through `vfs.FS`** (read, stat, walk), so an
  overlay can replace disk content with unsaved buffers.
- **Lazy index.** Objects are lexed/parsed/indexed on first demand and
  memoized. Linting one file touches its include closure, its inherit
  chain, and the targets of its literal calls. The only whole-lib
  operation is the reverse "who inherits/includes me" map, built on first
  need (a module's callback provided by its inheritors) and reused.
- **Invalidation by file and include dependency.** Every indexed unit
  records the files it read (itself + include closure). `Invalidate(p)`
  drops units that depend on `p`, all memoized chains (cheap to rebuild),
  and the reverse map entry set. Nothing else is recomputed.
- **`context.Context`** is checked between files and between index loads.
- **Diagnostics** carry byte offsets (`Start`, `End`), 1-based line/column
  for both ends, a stable rule `Code` and `Rule` name, `Related`
  locations (e.g. the unclosed `{`, the definition whose arity is
  violated), and `Fixes` as text edits. `diag.UTF16Col` converts a byte
  column to an LSP UTF-16 column (bytes that are not valid UTF-8, i.e. the
  corpus's Latin-1, count as one unit each, matching how editors decode
  them).

### 6.1 A future `dgdlint lsp`

A thin layer, either a `dgdlint lsp` subcommand or a separate `dgdls`
binary, holding one `vfs.Overlay` and one `lint.Linter` per discovered
project:

| LSP feature | Maps onto |
|---|---|
| `textDocument/didOpen`, `didChange`, `didClose` | `overlay.Set/Delete` + `Linter.Invalidate(path)` |
| `textDocument/publishDiagnostics` | `Linter.Run` for the changed file (debounced, previous run cancelled through the context); dependents that are open are re-run because invalidation reports which units were dropped. `Diagnostic.code` = rule code, `codeDescription` = `dgdlint explain` text, `relatedInformation` = `Related`. |
| `workspace/didChangeWatchedFiles` | `Invalidate` for disk changes; config change rebuilds the `Linter`. |
| `textDocument/codeAction` | `Fixes` on the diagnostics in range (quick-fix edits); plus generated "suppress with `dgdlint:disable-next-line`" actions. |
| `textDocument/definition` | index: resolve the identifier under the cursor through `Index.Chain` (functions, `::` and `label::` calls), `->` calls with a known receiver, `inherit`/`#include` paths, macros via the macro view (definitions keep file + offset). |
| `textDocument/hover` | same lookup: signature, defining object, specifiers, macro body. |
| `textDocument/documentSymbol`, `workspace/symbol` | `syntax.File.Decls`; index symbol tables. |
| `textDocument/formatting` | `pkg/format` (already a pure function). |

What the server adds itself: JSON-RPC transport, position conversion via
`diag.UTF16Col`, debouncing, and per-document versioning. No analysis
logic lives there.

## 7. Rules

Old rules keep their names (suppression comments already in the lib keep
working). Ported onto the tree; rules whose old form was a workaround are
redesigned:

| Code | Name | Group | Default | Change from old |
|---|---|---|---|---|
| E001 | `syntax-error` | syntax | error | **new**: parser + lexer errors |
| E002 | `adjacent-strings` | syntax | error | **new** (legacy idiom), carries a fix |
| E003 | `legacy-closure` | syntax | error | **new** (legacy idiom) |
| E004 | `declaration-initializer` | syntax | error | **new** (legacy idiom) |
| E005 | `declaration-after-statement` | syntax | error | **new**, tree-only |
| E006 | `include-not-found` | syntax | error | degrades when the lib is incomplete |
| R001 | `callable-not-found` | runtime | error | tree-based call extraction; macro receivers resolved; partial symbol tables respected |
| R002 | `static-crossobj` | runtime | error | same |
| R003 | `undefined-prototype` | runtime | error | direct calls found in the tree, not by `ident (` scanning |
| R004 | `target-object-missing` | runtime | error | degrades when the lib is incomplete |
| R005 | `callback-arity` | runtime | warning | argument counts from the tree |
| R006 | `lifecycle-chain` | runtime | warning | `::name()` calls found in the tree |
| S001 | `assignment-in-condition` | suspicious | warning | expression tree: handles nesting, ternaries, `do…while` |
| S002 | `no-effect-statement` | suspicious | warning | any top-level comparison statement, e.g. `x == f();` |
| S003 | `sscanf-format` | suspicious | warning | arguments from the tree |
| S004 | `unreachable-code` | suspicious | off | **new**, tree-only |
| C001 | `raw-inherit-path` | convention | off | |
| C002 | `missing-visibility` | convention | off | |
| C003 | `unformatted` | convention | off | |
| C004 | `static-autosave-var` | convention | off | |
| C005 | `unresolved-inherit` | convention | off (info) | |
| C006 | `unused-suppression` | convention | off | **new** |

Dropped workaround: the lifecycle rule's "labels from the other `#if`
branch" fallback is no longer needed in principle, but is kept because
one-branch selection still applies.

## 8. dgdfmt

dgdfmt keeps using `pkg/structure` and is not moved onto the parser: the
formatter must work on files that do not parse, its safety comes from the
token-equality gate rather than from structure, and nothing in this
rewrite requires changing it. Its tests and gate are untouched. Using the
tree for indentation (with the brace-stack as the fallback inside error
regions) is a follow-up, listed under deferred work.

## 9. Verification, results, deferred work

See the end of this document (filled in from measurements on the
VikingMUD lib).
