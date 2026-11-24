# dgdlint rewrite: design

Status: implemented on branch `dgdlint-rewrite`. Section 10 lists what is
done and what is deferred; section 9 has the measurements against the
VikingMUD lib and a DGD 1.7.9 compile oracle.

## 1. Purpose and non-negotiables

dgdlint exists to find real bugs in a very large, very old DGD LPC mudlib
before the driver finds them at load or boot time in production. Three
properties follow from that and are treated as constraints, not goals:

1. **Prove it or stay silent.** A finding is emitted only when the tool can
   prove it from what it can see. Anything it cannot see is *unknown*, and
   unknown never turns into an error. Wizards stop reading a linter that
   cries wolf.
2. **Broken input is normal input.** A third of the corpus does not
   compile. The tool must always produce a tree, never panic, and never
   let one broken region poison findings elsewhere.
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
pkg/lexer    byte-lossless lexer             (one fix: bare CR in strings)
pkg/pp       directives: #define tables, macro classes, #include targets,
             conditional-branch selection
pkg/syntax   AST + hand-written error-tolerant recursive-descent parser
pkg/config   dgdtools.yml, project discovery, DGD driver-config reader
pkg/index    lazy cross-file index over syntax trees: symbols, inherit
             chains, macro views; three-valued lookups; invalidation
pkg/lint     the embeddable linter: rules, suppressions, baseline,
             diagnostics with ranges/related locations/fix edits
pkg/report   output writers: human, JSON, SARIF 2.1.0, GitHub annotations
cmd/dgdlint  thin CLI over pkg/lint + pkg/report

pkg/structure, pkg/format, pkg/tokcmp, cmd/dgdfmt, cmd/dgdcmp: unchanged.
```

### 2.1 Why a hand-written recursive-descent parser

| Option | Verdict |
|---|---|
| goyacc port of DGD's `parser.y` | Rejected. AGPL source must not be transcribed; LALR error recovery (`error ';'`) is exactly what we need to do better than; and a yacc grammar cannot consult macro tables mid-parse. |
| tree-sitter | Rejected. Needs cgo (the tools ship as static Go binaries), produces coarse `ERROR` nodes, and its lexer cannot be told "this identifier is a type here". |
| rowan / Roslyn red-green trees | Not needed. They pay for incremental reparse in an IDE; a whole-lib lint is an 8 s batch job and one file reparses in microseconds. Nodes instead hold token-index ranges into the lossless `token.File`, which gives the same "tree addresses every byte" property by construction. |
| Hand-written RD over the existing token array | Chosen. Same choice as ruff, Biome, rust-analyzer, Roslyn, Go, and clang: full control over recovery and messages, and the only shape that can treat macros as first-class tokens. |

The grammar is written from DGD 1.7.9 grammar *facts* (which productions
exist, precedence, which keywords are reserved); no driver source is
copied.

### 2.2 Unpreprocessed parsing with classified macros

The linter parses source as written, like clang-format, Coccinelle and
Erlang's `epp_dodger`, not post-preprocessor text like clang-tidy.
Diagnostics and fixes must map to bytes the author wrote, and a real
preprocessor would need location mapping through expansions.

`pkg/pp` builds, per file, a layered **macro view**: the file's own
`#define`s, every resolved `#include` (recursively), and the driver's
force-included header. Each macro gets a class derived from its body:

| Class | Example | Parser treatment |
|---|---|---|
| empty / specifier | `#define public` | skipped in specifier position; as a statement it needs no `;` |
| type | `#define function mixed*`, `#define status int` | accepted as a type keyword |
| string | `#define IW "/d/Wild/"` | expression; counts as a string for adjacency and path evaluation |
| expression | `#define MAX(a,b) ((a)>(b)?(a):(b))` | ordinary identifier / call; arguments that are not expressions are kept opaque |
| statement | `#define FAIL(s) { write(s); return 0; }` | may stand as a statement without `;` |
| fragment | `#define TP this_player()->`, `#define BEGIN {`, conflicting definitions | anything touching it is opaque: no syntax errors are reported from that statement |

`macros:` in the config overrides a class, which is the escape hatch
clang-format provides as `StatementMacros` / `TypenameMacros`. `#undef X`
followed by `#define X` is a plain redefinition and wins over included
definitions; two different definitions with no `#undef` make the name a
fragment.

A macro view is **complete** only when every `#include` reachable from the
file resolved. Otherwise it is *partial* (missing header, zero-config run,
file outside the root) and the parser both assumes more (an unknown
identifier before a type keyword is taken as a specifier macro, `status
x;` as a declaration) and reports less (section 5).

**Conditionals.** One configuration is linted, as clang-tidy does, and
every consumer sees the same one. A condition is *decided* when that is
certain: `#if 0`/`#if 1`; `#ifdef`/`#ifndef`/`#if [!]defined(X)`/`#if X`
of a name that is predefined (`__DGD__`, plus `defines:`) or `#define`d
earlier in the same file; and a leading `#ifndef X`/`#define X` include
guard. Anything else takes the first branch **and that branch is marked
guessed**: it is parsed, so the tree stays whole, but syntax- and
runtime-group rules report nothing inside it, because whether the driver
compiles that code at all is unknown. (Resolving conditions through
included headers, the remaining step to a full preprocessor environment,
is deferred; see section 10.)

### 2.3 Error tolerance

The parser never fails and always returns a tree. Errors are data
(`syntax.Error`), as in rust-analyzer and Roslyn.

- A missing `;` where the next token starts a new line and can start a
  statement is repaired by *virtual insertion*: one error, reported at the
  end of the previous token (where the `;` belongs), and parsing continues
  with nothing skipped. This is the castle case.
- Other statement-level errors skip to the end of the statement, knowing
  which brackets the statement had open when it failed (DGD's own
  `error ';'` recovery, made bracket-aware). The statement becomes a
  `BadStmt`; no rule looks inside one.
- **Cascades are suppressed, not reported.** The first construct after a
  skip is parsed but hidden from rules and its errors are dropped: it is
  either the tail of what just failed or evidence the resynchronization
  point was wrong. After an error that leaves the file's structure in
  doubt (EOF inside a block, a broken top-level declaration) later errors
  in the file are dropped. Parse errors after a lexical error are dropped.
- A typed function header (`[specifiers] type name(params) {`) in
  statement position is a certain error (LPC has no nested functions): the
  open function is closed with "function 'g' begins inside the body of
  'f': missing '}'" and top-level parsing resumes there. An unbalanced
  brace costs one function, not the rest of the file, which was the old
  scanner's failure that made 36% of its `callable-not-found` errors false.
- Anything the parser could not account for at top level marks the file's
  symbol table *partial*; the index propagates that to every inheritor.

### 2.4 Dialect

Stock DGD 1.7.9 (the production driver; `viking-dgd-extension` adds kfuns
only and builds upstream DGD unpatched) rejects three idioms that are
common in code imported from older LPmud drivers. This was **verified
against the production binary** (section 9). The parser accepts all three
so the rest of the file still gets a tree, and reports each under its own
rule so a lib can baseline or disable them independently:

| Rule | Idiom |
|---|---|
| `adjacent-strings` | `"foo" "bar"` implicit concatenation |
| `legacy-closure` | `(: ob, "fn" :)` function-pointer literal |
| `declaration-initializer` | `int x = 5;` |

`&f()` and `(*f)()` are syntax errors unless `dialect.closures` is set.

## 3. Project root and configuration discovery

### 3.1 Prior art

| Tool | How the project/config is found | No config |
|---|---|---|
| ruff | nearest `pyproject.toml`/`ruff.toml` walking up **from each file** | defaults; runs fine |
| ESLint (flat) | `eslint.config.js` walking up from the cwd (v9) or from each file (v10) | error |
| Biome | `biome.json` walking up from the cwd; stops at the VCS root | defaults |
| cargo / go | `Cargo.toml` / `go.mod` walking up from the cwd; the manifest directory *is* the root | error: not in a module |
| clang-tidy | `.clang-tidy` walking up from each file; compile flags from `compile_commands.json` found the same way; without it, guesses flags and drowns in include errors | degraded, noisy |
| golangci-lint | `.golangci.yml` from the cwd upward to the module root | defaults |

Two families: *style* tools are happy with zero config because every check
is file-local; *semantic* tools need a root because references resolve
against it. clang-tidy is the cautionary tale: when it guesses the build
configuration it emits confident nonsense.

### 3.2 Decision

dgdlint is both: syntax and vet rules are file-local, cross-file rules
need the lib root (the directory that is `/` to the driver).

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
   config files dgdlint exits 2 naming both, like `go` and `cargo` when
   packages span modules. A whole-lib index has exactly one `/`.
4. **Explicit overrides:** `--config FILE`; `--root DIR`; `--no-config`.
5. **Zero-config is useful, not noisy.** With no config and no `--root`,
   dgdlint runs in *file-local mode*: file-local rules run with a partial
   macro view, cross-file rules do not run, and one note on stderr says
   so. The real pre-fix castle.c is caught in this mode.
6. **The driver config is the compile database.** DGD's own config file
   already states `include_dirs`, `include_file`, `auto_object` and
   `typechecking`. `driver_config: etc/mud.dgd` in dgdtools.yml reads them
   instead of asking the user to copy them (explicit `lint:` values win).
   This is the role `compile_commands.json` plays for clang tooling. The
   driver's absolute `directory` is *not* used as the root: it names a
   path on the production host.
7. **Subdirectory and single-file runs** lint only the named files but
   resolve against the whole lib. The index is lazy (section 6): one file
   loads only its include and inherit closure (measured: 0.08 s for two
   files against 9.5 s for the old whole-lib index).
8. **A file outside the root** (or any path when there is no root) gets
   file-local rules only; it has no lib path, so nothing cross-file is
   claimed about it.
9. **A file named explicitly is linted even if `exclude` matches it**;
   excludes prune directory walks (ruff's default, and what a pre-commit
   hook needs).

## 4. Configuration

- **Name/format:** `dgdtools.yml` stays (shared with dgdfmt; YAML is what
  the lib's wizards already edit). `.dgdtools.yml` is accepted too.
  Unknown keys are errors, as in ruff, Biome and golangci-lint: a
  misspelled setting must not be silently ignored.
- **Layering:** `extends: path` (a file or a list, resolved relative to
  the extending file; later wins; scalars and lists replace, maps merge).
  There is no implicit cascade of nested config files: per-directory
  behaviour is expressed in one place with `overrides`, like ESLint flat
  config and ruff's `per-file-ignores`. Hierarchical cascades are the
  feature ESLint removed.
- **Rule selection:** every rule has a stable kebab-case **name**, a short
  stable **code** (`E…` driver-rejected, `R…` runtime failure, `S…`
  suspicious, `C…` convention), a group, a default severity and a default
  on/off. `rules:` maps a rule name, code, or group (`all`, `syntax`,
  `runtime`, `suspicious`, `convention`) to `off | info | warning | error`
  or to a mapping `{severity: …, <options>}`. Rule entries beat group
  entries beat `all`. Giving a severity enables the rule. The legacy
  `enable:` / `disable:` lists keep working.
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
  later runs subtract them, so legacy noise does not block CI and only
  *new* breakage gates. Entries are keyed by (path, rule, fingerprint of
  the text of the offending line) with a count, not by line number, so
  edits elsewhere in a file do not resurrect a finding. This is the
  PHPStan/Psalm baseline model; it needs no VCS, unlike golangci-lint's
  `--new-from-rev`. `--no-baseline` shows everything.
- **Lib facts:** `include_dirs`, `include_file`, `auto_objects`,
  `typechecking` (or `driver_config`), `relative_object_paths` (`root`,
  the DGD semantics and the default, or `file`), `virtual_paths`, the
  call/object/format registries, `autosave_markers`.
- **Macro and dialect knobs:** `defines:` (names treated as defined),
  `macros: {NAME: type|specifier|statement|expression|opaque}`,
  `specifier_macros`.

### 4.1 CLI

```
dgdlint [flags] [path ...]        lint files/directories (default: project)
dgdlint rules                     list rules (code, name, default, scope)
dgdlint explain RULE              documentation for one rule
dgdlint config [path]             show the resolved project and settings
```

Flags: `--config`, `--root`, `--no-config`, `--format human|json|sarif|github`,
`--output FILE`, `--select RULES` (run only these), `--enable`,
`--ignore`/`--disable`, `--fail-on SEVERITY`, `--baseline FILE`,
`--write-baseline`, `--no-baseline`, `--local` (file-local rules only),
`--stats`, `--max-per-file N`, `--quiet`, `--exit-zero`, `--version`.
Flags may follow paths. The old single-dash spellings (`-f json`,
`-enable`, `-disable`, `-rules`, `-fail-on`, `-config`) still work.

**Exit codes:** `0` no findings at or above `fail_on`; `1` findings;
`2` usage, configuration or I/O error (including an unknown rule name and
a path that does not exist). Syntax errors in linted files are findings,
not tool errors (ruff/ESLint: same).

**Output:** human (`path:line:col: severity: message [rule]`, the format
editors and `grep` already parse, related locations indented beneath, a
one-line summary on stderr); JSON (a versioned object with ranges, related
locations, fix edits, and a summary that includes the "not verified"
counters); SARIF 2.1.0 for code-scanning upload; GitHub workflow
annotations.

**Fix posture:** the core never writes files. A rule may attach fixes as
text edits (byte ranges + replacement) to a diagnostic; they are emitted
in JSON and are what an editor applies. `adjacent-strings` carries one
(insert ` +`). A `--fix` writer is deferred until semantic edits have a
safety gate comparable to dgdfmt's token equality.

**Performance/caching:** a whole-lib run is parallel over files and takes
7.9 s for 108,116 files with the default rules (old: 9.5 s). A persistent
on-disk cache would save little and adds invalidation risk, so there is
none. The in-process index is lazy and invalidatable, which is what a
long-lived server needs.

## 5. Missing references and how unknowns degrade

The rule everywhere: **Found / provably Missing / Unknown**, and only
"provably Missing" may produce a finding.

| Situation | Behaviour |
|---|---|
| `#include` target not found, lib complete | `include-not-found`, error, once at the directive. Like clang's *fatal* missing header it stops syntax reporting for the rest of that file (later errors would be consequences) and makes the file's symbol table partial. |
| `#include` target not found, but the configured include dirs / force-include / auto object are themselves absent (partial checkout, wrong root) | Unknown. One project-level note ("lib looks incomplete … missing-reference findings are disabled"), no per-site findings. |
| Inherit path the macro evaluator cannot resolve | Unknown; chain partial. Visible via the opt-in info rule `unresolved-inherit` and `--stats`. (An inherit naming an identifier that is not a macro at all, with a complete view, is a `syntax-error`: DGD rejects it.) |
| Inherit / `clone_object` / call target resolves to a path with no file | `target-object-missing`, error, per site (each site breaks separately and needs its own fix), unless the path matches `virtual_paths` or the lib is incomplete. |
| Function not found in a **complete** chain | `callable-not-found`, per site. For `this_object()` in a file others inherit or include, the users are searched first; for code in the auto object's chain the answer is always unknown. |
| Function not found in a **partial** chain (unresolved inherit, parent with unparsed regions, missing parent, missing include) | Unknown, silent. |
| Receiver of `->` is not a string constant, a macro evaluating to one, or `this_object()` | Unknown, silent; counted in `--stats`. |
| File outside the lib root, or no root at all | File-local rules only; one note. |
| Code in a conditional branch that could not be decided | Parsed; no syntax- or runtime-group findings inside it. |
| Macro view partial | A syntax error is kept only if no unseen macro could explain it: no undeclared ALL_CAPS identifier at the statement head, at either side of the failure point, or as the callee of the call ending there, and no `unknown-ident ident` adjacency. |
| Statement touches a `fragment`-class macro; lexical error inside a function-like macro's arguments | Opaque: no syntax finding. |
| File whose top level could not be fully parsed | Its own first error is reported; its symbol table is partial, so nothing is concluded *about* it or its inheritors. |

Report **once at the root cause, per site for independent breakage**.
Unknowns are never findings but they are not invisible: `--stats` (and the
JSON summary) print how many inherits, includes, files, call receivers and
withheld findings were unknown, so a drop in coverage is observable.

## 6. Library API and the path to an LSP server

dgdlint is a library first; the CLI is one frontend.

```go
fs  := vfs.NewOverlay(vfs.OS{})            // editor buffers shadow disk
prj, _ := config.Discover(fs, paths, config.DiscoverOptions{})
l, _ := lint.New(prj, fs)                  // no globals; safe to keep alive
res, _ := l.Run(ctx, lint.Request{Paths: paths})
for _, d := range res.Diagnostics { ... }  // ranges, code, related, fixes

fs.Set(path, unsavedBytes)                 // editor change
changed := l.Invalidate(path)              // files whose findings may differ
res, _ = l.Run(ctx, lint.Request{Paths: []string{path}})
```

Properties the core guarantees (each has a test in `pkg/lint`):

- **No printing, no `os.Exit`, no `flag`**, no package-level mutable
  state. `lint.Rules()` returns a fresh copy of an immutable table; each
  `Linter` owns its index. `cmd/dgdlint` is `run(ctx, args, stdout,
  stderr) int` over the library.
- **All file access goes through `vfs.FS`** (read, exists, walk), config
  loading included, so an overlay can replace disk content with unsaved
  buffers.
- **Lazy index.** Units are lexed/parsed/indexed on first demand and
  memoized as *facts only* (symbols, inherits, dependencies), not trees,
  so a whole lib stays small in memory. Linting one file touches its
  include closure, its inherit chain, and the targets of its calls. The
  only whole-lib operation is the reverse "who inherits/includes me" map,
  built on first need (a module's callback provided by its inheritors).
- **Invalidation by file and include dependency.** Every unit records the
  files it was built from. `Invalidate(p)` drops units that depend on `p`,
  all memoized chains (cheap to rebuild), and the reverse map; it returns
  the dropped files. A change to the force-included header drops
  everything.
- **`context.Context`** is checked between files and during the whole-lib
  scan; a cancelled reverse lookup answers "unknown", never "missing".
- **Diagnostics** carry byte offsets (`Start`, `End`), 1-based line/column
  for both ends, a stable `Code` and `Rule` name, `Related` locations
  (the unclosed `{`, the definition whose arity is violated, the include
  that breaks inherit order) and `Fixes` as text edits. `diag.UTF16Col`
  converts a byte column to an LSP UTF-16 column; bytes that are not valid
  UTF-8 (the corpus's Latin-1) count as one unit each, matching how
  editors decode them.
- **One bad file cannot take down a run:** a panic while analyzing a file
  is caught, reported as a note, and the file is skipped.

### 6.1 A future `dgdlint lsp`

A thin layer, either a `dgdlint lsp` subcommand or a separate `dgdls`
binary, holding one `vfs.Overlay` and one `lint.Linter` per discovered
project:

| LSP feature | Maps onto |
|---|---|
| `textDocument/didOpen`, `didChange`, `didClose` | `overlay.Set/Delete` + `Linter.Invalidate(path)` |
| `textDocument/publishDiagnostics` | `Linter.Run` for the changed file, debounced, with the previous run cancelled through its context. `Invalidate` returns the dependents; those that are open are re-run. `Diagnostic.code` = rule code, `codeDescription` = the `explain` text, `relatedInformation` = `Related`, positions via `diag.UTF16Col`. |
| `workspace/didChangeWatchedFiles` | `Invalidate` for disk changes; a config change rebuilds the `Linter`. |
| `textDocument/codeAction` | `Fixes` on the diagnostics in range (quick-fix edits), plus generated "suppress with `dgdlint:disable-next-line`" actions. |
| `textDocument/definition` | the index: resolve the identifier under the cursor through `Index.Chain` (functions; `::` and `label::` calls), `->` calls with a known receiver through `LookupCallable`, `inherit`/`#include` paths through `ObjectPath`/`ResolveInclude`, macros through the macro view. `ChainFunc` and `pp.Macro` already carry the defining file and offset. |
| `textDocument/hover` | the same lookups: specifiers and argument range of a function, the object that provides it, a macro's body and class. |
| `textDocument/documentSymbol`, `workspace/symbol` | `syntax.File.Decls`; the units' symbol tables. |
| `textDocument/formatting` | `pkg/format`, already a pure function. |

What the server adds itself: JSON-RPC transport, position conversion,
debouncing, and per-document versioning. No analysis logic lives there.
What the core still lacks for it: a "node at offset" helper and scopes for
local variables (section 10).

## 7. Rules

Old rules keep their names, so suppression comments already in a lib keep
working. All are ported onto the tree.

| Code | Name | Default | Notes |
|---|---|---|---|
| E001 | `syntax-error` | error | **new**: parser + lexer errors |
| E002 | `adjacent-strings` | error | **new** (legacy idiom), carries a fix |
| E003 | `legacy-closure` | error | **new** (legacy idiom) |
| E004 | `declaration-initializer` | error | **new** (legacy idiom) |
| E005 | `declaration-after-statement` | error | **new**; DGD "declaration after statement" |
| E006 | `include-not-found` | error | degrades when the lib is incomplete; no longer appends `.c` to extensionless include names |
| E007 | `bad-directive` | error | **new**: text after `#else`/`#endif`/`#include "x"`, unknown directives |
| E008 | `inherit-after-declaration` | error | **new**: also when the declaration arrives via an earlier `#include` |
| E009 | `missing-parameter-type` | error | **new**; needs `typechecking >= 1` |
| E010 | `return-value` | error | **new**: value from void/typeless function, bare return in typed |
| R001 | `callable-not-found` | error | tree-based; macro receivers; partial symbol tables respected; `"/path"->fn()` judged by that object's own chain |
| R002 | `static-crossobj` | error | |
| R003 | `undefined-prototype` | error | direct calls found in the tree |
| R004 | `target-object-missing` | error | object paths resolve from the lib root |
| R005 | `callback-arity` | warning | |
| R006 | `lifecycle-chain` | warning | silent when the body has unparsed or macro statements |
| S001 | `assignment-in-condition` | warning | expression tree; also `do … while` |
| S002 | `no-effect-statement` | warning | any top-level comparison statement (`x == f();`) |
| S003 | `sscanf-format` | warning | |
| S004 | `unreachable-code` | off | **new** |
| C001–C005 | `raw-inherit-path`, `missing-visibility`, `unformatted`, `static-autosave-var`, `unresolved-inherit` | off | |
| C006 | `unused-suppression` | off | **new** |

The E-rules exist because of what the oracle showed (section 9): the
driver's most common reasons for refusing a file are not all "syntax
error".

## 8. dgdfmt

dgdfmt keeps using `pkg/structure` and is not moved onto the parser: the
formatter must work on files that do not parse, its safety comes from the
token-equality gate rather than from structure, and nothing here requires
changing it. Its tests and gate are untouched and pass. The one shared
change is in the lexer: a bare carriage return inside a string literal is
string content (DGD accepts it) rather than an unterminated string; the
lexer stays byte-lossless and the lexer/format/tokcmp tests pass
unchanged.

## 9. Verification and results

### 9.1 The oracle

To measure precision against ground truth, the production driver binary
(DGD 1.7.9, `-DLARGENUM -DSLASHSLASH`) was run as a separate throwaway
process against a scratch *copy* of the lib sources in the test
container's `/tmp`, with a minimal driver object that calls
`compile_object` on each of the 108,116 non-excluded `.c` files and
records every `compile_error`. Nothing under the mud tree was written and
the copy was removed afterwards. Result: 72,027 files compile, 36,088 do
not (one crashes the driver: `/players/kniggit/xfo.c`, an `else` directly
before `}`).

Construct verdicts from 236 single-construct test files (the ones the
design depends on):

| Construct | DGD 1.7.9 |
|---|---|
| adjacent string literals; `(: a, b :)`; `int x = 5;` (local or global) | rejected, "syntax error" |
| missing `;` after `})` (the pre-fix castle.c: line 263) | rejected |
| declaration after a statement, after `;`, after a nested block | rejected, "declaration after statement" |
| declaration at the start of a nested block | accepted |
| `case 1: }`, top-level `;`, `foo(a,)`, `int f(int);`, `o->foo;`, `for (int i…)`, `&g()` | rejected |
| `({ a, })`, `([ a:b, ])`, `x == 1;`, `return 1; x = 2;`, unused locals | accepted |
| typeless `foo(a) {}` | rejected, "missing type for parameter a" (typechecking 1) |
| typeless `bar() { return 1; }` | rejected, "value returned from void function" |
| `{ … }` statement macro used without `;` | accepted |
| `status f() {}` with no such macro | rejected |
| garbage or an unterminated string inside a skipped `#if` branch | accepted |
| `#endif FOO`, `#else FOO` | rejected, "bad token in control" |
| unknown directive, even inside `#if 0` | rejected, "undefined control" |
| inherit after a declaration | rejected, "inherit must precede all declarations" |
| raw Latin-1 byte, `@`, `\` outside a string | rejected, "illegal character" |

Two claims in the review that motivated the rewrite were wrong and are
corrected here: `/secure/daemons/weard.c:44` is inside `#if 0` and the
file compiles; `/com/a/rename.c` compiles (a block macro needs no `;`).

Not verified: the oracle compiles each `.c` standalone, so files that
only make sense when `#include`d elsewhere show as failing; its
`object_type` hook is simplified.

### 9.2 Syntax-group precision and recall (whole lib, all rules on)

"Flagged" = files with at least one finding of the rule.

| Rule | Files flagged | Oracle: does not compile | Oracle: compiles (false positive) |
|---|---|---|---|
| syntax-error | 3,851 | 3,851 | 0 |
| adjacent-strings | 10,049 | 10,049 | 0 |
| legacy-closure | 6,329 | 6,329 | 0 |
| declaration-initializer | 2,102 | 2,102 | 0 |
| declaration-after-statement | 37 | 37 | 0 |
| include-not-found | 5,561 | 5,561 | 0 |
| bad-directive | 1,858 | 1,858 | 0 |
| inherit-after-declaration | 1,517 | 1,517 | 0 |
| missing-parameter-type | 11,415 | 11,415 | 0 |
| return-value | 13,601 | 13,601 | 0 |
| **any syntax-group rule** | **30,643** | **30,643** | **0** |

The first measurement of this parser had 103 such false-positive files;
every one was traced to a cause and fixed in the design rather than
special-cased (macro after `->`, typed-object macros, `#undef`/`#define`,
bracket-blind resynchronization, undecided `#if` branches, relative
object paths, …). The file-level check cannot prove each individual
finding: a file can fail to compile for a different reason than the one
dgdlint gives. A spot check of four findings whose kind the oracle's
messages for that file did not echo found all four real (`):`, a missing
`:` in a mapping, `reurn 0;`, a parameter named `new`).

Recall, by the driver's *first* error in the file:

| Driver's first error | Files | Flagged by a syntax-group rule |
|---|---|---|
| syntax error | 11,700 | 10,932 (93%) |
| cannot include | 5,468 | 5,466 |
| missing type for parameter | 3,631 | 3,622 |
| value returned from void function | 2,174 | 2,173 |
| bad token in control | 1,823 | 1,820 |
| inherit must precede all declarations | 849 | 848 |
| undefined function | 1,629 | 680 (other errors in the same file) |
| undeclared variable | 799 | 272 (same) |

The missed syntax errors are mostly statements made opaque by fragment
macros and code in undecided `#if` branches: deliberate silence.
"undefined function"/"undeclared variable" need full name resolution and
are deferred.

Core lib (`std`, `secure`, `dgd`, `com`, `obj`, `room`): zero syntax-group
findings, matching the oracle, which compiles all of it. This is asserted
by `TestCorpusLint`.

### 9.3 Old vs new, whole lib (findings; all rules on)

| Rule | Old | New | Why |
|---|---|---|---|
| syntax-error, adjacent-strings, legacy-closure, declaration-initializer, declaration-after-statement, bad-directive, inherit-after-declaration, missing-parameter-type, return-value | 0 | 5,781 / 31,176 / 13,468 / 5,274 / 74 / 1,995 / 1,533 / 34,245 / 98,757 | new |
| callable-not-found | 2,609 | 2,693 | −1,189: files the old scanner mis-read (function "missing" that is defined after a syntax error) are now unknown. +1,320: macro receivers (`D_FOO->fn()`), and `"/path"->fn()` no longer excused by the target's inheritors. |
| undefined-prototype | 1,724 | 857 | −819: old false positives of the same kind (`my_find_player` "never defined", defined 200 lines below a syntax error). |
| target-object-missing | 10,889 | 7,025 | −4,907: old resolved `"obj/x"` relative to the referencing file (`/obj/obj/diploma`); DGD resolves from the root. Also `inherit ROOM "/forest"` mis-read as a label. +1,052: macro receivers. |
| callback-arity | 1,362 | 2,479 | macro receivers and direct object targets |
| static-crossobj | 44 | 69 | macro receivers |
| include-not-found | 6,630 | 6,424 | −214: old appended `.c` to extensionless include names (`"…/DEFINES"`); includes in undecided branches. |
| assignment-in-condition | 301 | 280 | −21: statements that do not parse (`if (str! = "rose")`) are syntax errors now |
| no-effect-statement | 103 | 172 | −59 old false positives (`for (x=0; x==y; x++)`, `RET str == "ball" …;`); +130 real ones the old rule skipped whenever a statement contained a bracket (`dir == "in a flash" + f(x);`) |
| lifecycle-chain | 679 | 644 | silent when the body has unparsed or macro statements |
| sscanf-format, static-autosave-var, raw-inherit-path, unresolved-inherit, missing-visibility | 28 / 68 / 633 / 2,976 / 335,743 | 27 / 68 / 633 / 2,860 / 336,464 | essentially unchanged |

Late-bound call sites with a known receiver: 6.5% (old) → 22.5% (new;
126,200 of 562,067).

### 9.4 Tests

- `pkg/syntax`: valid programs for every construct, recovery cases with
  exact positions, macro tolerance, tree shape, never-panics seeds with
  span invariants.
- `pkg/lint`: every rule, positive and negative, on in-memory libs; the
  fixture lib; degraded modes (no root, incomplete lib, file outside the
  root); overlay + invalidation; cancellation; baseline; rule table
  stability.
- `pkg/config`, `pkg/pp`, `pkg/diag`, `cmd/dgdlint` (exit codes, formats,
  baseline workflow, discovery from anywhere).
- Corpus (`-tags corpus`): `TestCorpusParse` (182,126 `.c`/`.h` files
  parsed twice: no panics, span and ordering invariants) and
  `TestCorpusLint` (whole-lib run, no internal errors, core directories
  free of syntax-group findings). Both pass on the VikingMUD lib.

## 10. Implemented vs. deferred

Implemented: everything in sections 2–7 except the items below.

Deferred, in rough priority order:

1. **Conditions decided through includes.** `#ifdef X` where X comes from
   a header is currently a guess (and its branch is not judged). A small
   define-tracking environment across the include closure would decide
   most of them and recover recall inside `#if` blocks.
2. **Name resolution** ("undefined function", "undeclared variable",
   direct-call arity, redeclaration): the next 2,500 files the driver
   rejects. Needs the kfun table, scopes, and inherited variables.
3. **Receiver flow** for `ob->fn()` where `ob` is a local assigned from
   `clone_object("…")` and the like: 77% of late-bound call sites still
   have an unknown receiver.
4. **`--fix`** (see 4.1) and more fixes than `adjacent-strings`.
5. **Linting headers** (`.h` files with code) and `.c` fragments in the
   context of their includers.
6. **dgdfmt on the tree** for indentation of braceless bodies, with the
   brace stack as the fallback in error regions.
7. **`dgdlint lsp`** (6.1), plus `dgdlint init` to write a starter config
   from a driver config.
8. Backslash-newline outside strings is tolerated (not reported) but not
   modelled by the lexer.
