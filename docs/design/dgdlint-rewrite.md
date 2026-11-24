# The linter and the project model: design

Status: implemented on branch `dgdlint-rewrite`. Section 11 lists what is
done and what is deferred; section 9 has the measurements against the
VikingMUD lib and a DGD 1.7.9 compile oracle. Two companion documents
cover what is shared with the other tools: [config.md](config.md)
(the configuration file, per-file settings, the `dgdtools` command) and
[dgdfmt.md](dgdfmt.md) (the formatter).

## 1. Purpose and non-negotiables

The linter exists to find real bugs in a very large, very old DGD LPC mudlib
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
   the linter of the time passed the file clean because it had no parser.

The old design (lexer + top-level "structure" scanner + per-rule token
heuristics) could not satisfy (3), and satisfied (1) only by each rule
re-deriving expression structure by bracket counting. The rewrite keeps
the purpose, the lossless lexer, the formatter's token-equality gate, and
the three-valued "unknown is not missing" index philosophy. Everything
else is new.

## 2. Architecture

```
pkg/diag     the diagnostics model: finding, severity, tags, related
             locations, fixes as text edits, notes
pkg/vfs      file access: OS, in-memory overlay (unsaved editor buffers)
pkg/token    lossless token model; the one table of keywords, specifiers,
             types and operator precedence; lexical errors by offset
pkg/lexer    byte-lossless lexer
pkg/pp       the preprocessor model: the ordered #define environment of
             a translation unit, macro classes, conditional-branch
             selection, the constant evaluator
pkg/syntax   AST + hand-written error-tolerant recursive-descent parser,
             its error pass (ordering, cascades, macro taint), and
             declarations rendered as text (signatures, types)
pkg/directive  the comments addressed to the tool: dgdtools-ignore ...
pkg/tokcmp   significant-token comparison of two files
pkg/profile  a mudlib profile: manifest, stubs, the file format, lookup
pkg/editorconfig  .editorconfig files
pkg/fileset  where a lib is on disk: path mapping, globs, the walk
pkg/config   dgdtools.yml, its validation, project discovery, per-file
             settings (nested scopes, .editorconfig), DGD driver-config
             reader
pkg/index    lazy cross-file index over syntax trees: symbols, inherit
             chains, #define environments; three-valued lookups;
             invalidation
pkg/calls    the late-bound call sites of one file (obj->fn(),
             call_other, call_out, the lib's registrars) and the
             registry of functions that take a function name
pkg/format   the formatter: a layout plan from the syntax tree, a
             token-stream printer, the token-equality gate (section 8)
pkg/lint     the embeddable linter: rules with typed options, the layers
             of rule settings, suppressions, baseline, diagnostics
pkg/profilebuild  a profile from a whole lib: which files are shared, and
             each one's stub
pkg/report   output writers: human, JSON, SARIF 2.1.0, GitHub annotations
cmd/dgdtools the one binary: a thin front end per subcommand (lint,
             format, rules, explain, config, profile) over the packages
```

Each package imports only packages above it in this list. Three of the boundaries are worth stating, because they were
once elsewhere. `pkg/token` knows nothing of diagnostics: a lexical
error is an offset and a message (`token.Error`), and the parser turns
it into a syntax error. `pkg/calls` is a function of one file's tree,
its #define environment and the registry (`calls.Extract`), with no rule
or pass in it, so the profile builder asks it what the linter asks it.
And `pkg/index` answers lookups with facts, not sentences: a function
that is missing comes back with where it was searched
(`Lookup.Searched`: the chain, a stub that lists public functions only,
the inheritors, the checkout) and the rule that reports it words the
message.

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

`pkg/pp` is the one model of the preprocessor. A `pp.Env` is the
`#define` environment of a translation unit in the order the driver's
preprocessor builds it: the names defined before the first line
(`__DGD__` and the other predefined names, plus `defines:`), the tables
of the force-included header, and then the file's own directives, with
the table of each `#include` (recursively) taking effect where the
directive stands. `Env.Scan` reads a file's directives into it once; it
is the only reader of `#define`. Every consumer of a file works from the
same `Env`: the parser asks it what a name is (`Lookup`), the rules read
macro bodies from it, and the constant evaluator reads values from it.
Each macro gets a class derived from its body:

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

An environment is **complete** only when every `#include` reachable from
the file resolved. Otherwise it is *partial* (missing header, zero-config
run, file outside the root) and the parser both assumes more (an unknown
identifier before a type keyword is taken as a specifier macro, `status
x;` as a declaration) and reports less (section 5).

**Conditionals.** One configuration is linted, as clang-tidy does, and
every consumer sees the same one: `Env.Scan` selects the branches while
it reads the directives, so a condition is evaluated in the environment
as it stands at that line. A condition is *decided* when its value is
certain there:

- its names are defined before the first line, by the force-included
  header, by a header included above it, or earlier in the file, and the
  one evaluator (integer arithmetic, comparison, logic, `defined`, macros
  that name macros) gives the expression a value;
- or a name is defined nowhere and the environment is complete: then it
  is not defined. This holds for an object's source only. A header is
  not compiled by itself, and whoever includes it may define the name
  first, so a header decides nothing from absence;
- or it is a leading `#ifndef X`/`#define X` include guard.

Anything else takes the first branch **and that branch is marked
guessed**: it is parsed, so the tree stays whole, but syntax- and
runtime-group rules report nothing inside it, because whether the driver
compiles that code at all is unknown. A name that a file defines or
undefines inside such a branch (either arm) is recorded as *unsure* in
that file's table, and decides no condition further down or in the files
that include it.

A header's own table is built once per generation, from the header alone
(the predefined names and its own earlier directives), and shared by
every file that includes it; that is what keeps a hundred thousand files
from re-reading the same headers. The price is that a condition inside a
header that depends on its includer stays a guess.

The same evaluator gives the string constants the index needs their
value: inherit paths and call targets written with literals, `+`,
adjacent literals and object-like macros (`Env.EvalString`).

On the Viking lib this decides most conditionals: findings withheld
inside guessed branches fell from 1,025 to 106 (all rules on, driver
config), and the syntax-group rules flag 104 more files, every one of
which the driver also rejects (section 9).

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

The linter is both: syntax and vet rules are file-local, cross-file rules
need the lib root (the directory that is `/` to the driver).

1. **A root is never required and never guessed.** Lib-absolute paths
   resolve against the root; a wrong root would turn every `inherit` into
   a false error. No VCS-marker or directory-name inference.
2. **Discovery walks up from the linted path, not the cwd.**
   `dgdtools lint /any/where/lib/d/Foo/castle.c` works from any
   directory, as with ruff and clang-tidy. The nearest root config
   (`dgdtools.yml` or `.dgdtools.yml` that is not a settings scope,
   [config.md](config.md) section 3) is the project; `root:` in it
   (default: the config's directory) is the lib root. With no path
   arguments, discovery starts at the cwd and the whole root is linted.
3. **One project per invocation.** If the arguments discover different
   root configs `dgdtools lint` exits 2 naming both, like `go` and
   `cargo` when packages span modules. A whole-lib index has exactly one
   `/`. Settings scopes below a root config are not projects: files
   under different scopes of one lib are one run, each linted by its own
   settings.
4. **Explicit overrides:** `--config FILE`; `--root DIR`; `--no-config`.
5. **Zero-config is useful, not noisy.** With no config and no `--root`,
   the linter runs in *file-local mode*: file-local rules run with a partial
   `#define` environment, cross-file rules do not run, and one note on stderr says
   so. The real pre-fix castle.c is caught in this mode.
6. **The driver config is the compile database, and it is a build
   input.** DGD's own config file already states `include_dirs`,
   `include_file`, `auto_object` and `typechecking`.
   `profile_build: {driver_config: etc/mud.dgd}` reads them instead of
   asking anyone to copy them; `dgdtools profile build` bakes them into a
   profile (section 10), and a whole-lib lint uses them directly. The
   same keys set in `dgdtools.yml` still win. The driver's absolute `directory` is *not*
   used as the root: it names a path on the production host.
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

The file, how a file's settings are found (nested settings scopes,
`.editorconfig`, the user-level fallback, precedence) and the command
line are designed in [config.md](config.md). What the linter adds:

- **Unknown keys are errors**, as in ruff, Biome and golangci-lint: a
  misspelled setting must not be silently ignored.
- **Project keys and settings.** What the linter resolves references
  against is the project: `root`, `profile`, `lib_path`, `exclude`,
  `dialect` (with `typechecking`), `include_dirs`, `include_file`,
  `auto_objects`, `create`, `relative_object_paths` and `conventions`
  (`specifier_macros`,
  `macros: {NAME: expression|empty|specifier|type|string|statement|opaque}`,
  `defines`, the call/object/format registries, `autosave_markers`,
  `virtual_paths`). They are top-level keys, the same ones a profile's
  manifest has, and there is one set per run. How a file is linted is
  `lint:`, and that can differ per directory.
- **Nothing about one lib is in the code.** The tool knows the driver:
  its keywords, its predefined macros, `call_other`, `call_out`,
  `compile_object`, `find_object`, `clone_object`, `sscanf`,
  `this_object`, and the parameters of its config file. Everything else
  is a convention and has no default: which names are empty specifier
  macros (`public` is some lib's `#define`, not a keyword), what marks
  an object that saves itself, which of the lib's functions take a
  function name. The function a new object is initialized by is the
  project key `create`, read from the driver config's `create`
  parameter; `lifecycle-chain` checks that one, and falls back to the
  customary `create` when nothing says. A lib whose auto object receives
  the driver's call under one name and passes it on under another (the
  kernel library: `_F_create`, then `create`; the corpus lib:
  `__create`, then `create`) sets the key to what its objects define. It
  is a project key and not an option of the rule because it is a fact
  about the lib that a profile must carry: a wizard's checkout has no
  root config, and with the name only in the root's rule options the
  partial-checkout corpus test lost the rule's findings in every
  checkout. The rule's option `names` is for further functions held to
  the same rule.
- **The call registry declares call shapes.** An entry of
  `conventions.call_registry` is one or more forms
  `{function: N, object: M, args: K}`: the argument that names the
  function, optionally the argument that says in which object (without
  it the function is called in `this_object()`, where static functions
  can be reached; with it the call is a `call_other`), and optionally
  the first of the arguments passed on to the function (without it the
  argument count is not known and `callback-arity` is silent). A call is
  read by the first form it fits: one whose function argument is a
  string constant that names a function. The driver's two are entries of
  the same table, `call_other: {object: 0, function: 1, args: 2}` and
  `call_out: {function: 0, args: 2}`; no call shape is guessed from what
  a string looks like.
- **`relative_object_paths` stays a key because DGD leaves it to the
  lib.** The driver does not resolve an object path itself: for
  `inherit "x"` it calls the driver object's `inherit_program(from,
  path, priv)`, for `call_other("x", …)` its `call_object(path)`
  (`comp/compile.cpp`, `kfun/builtin.cpp`), and the lib's LPC code
  returns the object. What a path without a leading slash means is
  therefore a property of the lib: the driver's own functions
  (`compile_object`, `find_object`) take it from the root, since
  `Path::resolve` drops the leading slash and `"std/room"` is the same
  name as `"/std/room"`; the kernel library resolves it against the
  directory of the object that says it; and when no `inherit_program`
  answers, the driver's fallback is also the inheriting file's directory
  (`Path::from`). `root` is the default; `file` is the other. No third
  meaning was found, and a value other than those two is an error.
- **Rule selection:** every rule has a stable kebab-case **name**, a short
  stable **code** (`E…` driver-rejected, `R…` runtime failure, `S…`
  suspicious, `C…` convention), a group, a default severity and a default
  on/off. `lint.rules` maps a rule name, code, or group (`all`, `syntax`,
  `runtime`, `suspicious`, `convention`) to `off | info | warning | error`
  or to a mapping `{severity: …, max_per_file: …, <options>}`. Rule
  entries beat group entries beat `all`. Giving a severity enables the
  rule.
- **Rule options are typed.** A rule that has options declares them as a
  struct (`Rule.Options`); the keys of its mapping are checked against
  that struct when the linter is made, and a rule's `Run` reads them as
  that struct (`Pass.Options`). So `deny:` under `lifecycle-chain`, a
  misspelled `nmaes:`, a string where a list belongs, or any option
  under a group is an error that names the rule, the option and the
  rule's real options. Options set in several layers add up key by key.
- **A name and a code are one key.** `syntax-error` and `E001` in the
  same mapping set the same rule twice; like a duplicate key in YAML it
  is an error, not a precedence to remember.
- **Who validates what.** `Config.Validate` is the one check of what the
  configuration defines: enumerated values (`lint.fail_on`,
  `relative_object_paths`, `conventions.macros` classes, severities,
  the format keys), ranges (`dialect.typechecking`, argument indexes),
  and the keys a settings scope may not have. `config.Load` runs it, so
  every command refuses the same file. The rule table is the linter's,
  so rule names and rule options are checked by `lint.New` and, for a
  nested file, when the first file under it is linted. `dgdtools config`
  runs all of it, for the path's own configuration and for every
  `dgdtools.yml` below a directory, and exits 2 on the first error: it
  is the `config verify` of golangci-lint, in the command that also
  shows the result.
- **What the command line says about a run is not written into the
  configuration.** `--fail-on` is the front end's threshold over
  `Linter.FailOn()`; `--baseline FILE` is `Options.Baseline`, a file the
  linter reads once like the project's own.
- **Per-path settings, two forms.** `lint.overrides: [{paths: [globs],
  rules: {…}}]` in the root config (lib-relative globs, `**` supported),
  which keeps a lib's policy in one place; and a settings scope, a
  `dgdtools.yml` with `root: false` in the directory it is about, which
  lets the owner of a directory say it. For one file the layers apply in
  this order, each over the last: the root's `rules`; its matching
  `overrides`; then each scope from the outermost in, its `rules` and
  its matching `overrides` (globs relative to the scope). `--select` and
  `--enable` are applied before the per-path layers and `--ignore`
  after, so a path that turns a rule off keeps it off and the command
  line can still silence anything. `Linter.EffectiveRules(path)` and
  `dgdtools config PATH` answer "what runs here".
- **A nested file that is not valid fails the run** (a project key in a
  scope, an unknown rule): linting its files by other settings than the
  ones somebody wrote would be a silent wrong answer.
- **Comments addressed to the tool have one prefix and one grammar**
  (`pkg/directive`), for the linter and the formatter alike:

  ```
  dgdtools-ignore TARGETS [: reason]         one line of code
  dgdtools-ignore-start TARGETS [: reason]   from here ...
  dgdtools-ignore-end [TARGETS]              ... to here (no targets: everything)
  TARGETS = lint/RULE | lint | format, comma-separated
  ```

  The choice. The tool is one binary with one name, so there is one word
  to learn and one word to grep for: every exemption in a lib is a
  `grep -rn dgdtools-ignore`. Biome, the other tool that lints and
  formats from one binary with one name, does it this way
  (`biome-ignore lint/…`, `biome-ignore format`, `-start`/`-end` for
  ranges), and the shape was taken from it: tool prefix, then what is
  ignored as a path, then a reason after a colon. The alternatives were
  looked at and not followed. Ruff has two unrelated spellings (`# noqa:
  E501`, `# fmt: off`) because it had to read the comments flake8 and
  black had already put into every codebase; that is compatibility, not
  design, and there is nothing here to be compatible with. Deno and
  ESLint+Prettier name the component in the prefix (`deno-lint-ignore`,
  `deno-fmt-ignore`), which is two words to grep for and two names that
  are not commands of this tool. clang-tidy's `NOLINT` and
  clang-format's `off`/`on` are two tools.

  What the linter reads from it. The plain form is about one line: the
  line of code the comment stands on, or the next line when the comment
  stands alone, so a trailing comment and a comment above both work and
  there is one keyword instead of ESLint's `disable-line` and
  `disable-next-line`. Rules are named as `lint/NAME` or `lint/CODE`;
  `lint` alone is every rule, for one line only. A range names its
  rules: a comment that turns everything off from here on is
  deliberately not offered. A comment is a directive when it begins with
  the prefix and its targets are on that line; one that merely mentions
  the prefix is prose.
- **A directive that cannot be followed is a finding.** Two rules are
  about the comments themselves; they run after the others
  (`Rule.Late`) and see what the comments did. `invalid-suppression`
  (on by default): an unknown rule name, an unknown target, a comment
  that names nothing, a range without rules, an end without a start, a
  second start. A misspelled rule name is therefore reported instead of
  silently suppressing nothing. `unused-suppression` (off by default)
  reports each rule a comment names that had no finding there, as
  ESLint's `--report-unused-disable-directives` and ruff's `RUF100` do;
  a comment for a rule that did not run on the file is not judged. Both
  report the target they are about, not the whole comment, and neither
  can itself be suppressed by a comment. There is no special case for
  them in the engine: they are rules with `Late` set.
- **Baseline:** `dgdtools lint --write-baseline` records current
  findings in `dgdtools-baseline.json` next to the config
  (`lint.baseline` to rename); later runs subtract them, so legacy noise
  does not block CI and only *new* breakage gates. Entries are keyed by
  (path, rule, fingerprint of the text of the offending line) with a
  count, not by line number, so edits elsewhere in a file do not
  resurrect a finding. This is the PHPStan/Psalm baseline model; it
  needs no VCS, unlike golangci-lint's `--new-from-rev`. `--no-baseline`
  shows everything. The baseline and `lint.fail_on` describe a run, so
  they are root-config keys.

### 4.1 CLI

```
dgdtools lint [flags] [path ...]  lint files/directories (default: project)
dgdtools lint [flags] -           lint standard input
dgdtools rules                    list rules (code, name, default, scope)
dgdtools explain RULE             documentation for one rule and its options
dgdtools config [path]            check a path's configuration; show the project and the settings of a file
```

Flags of `lint`: `--config`, `--no-config`, `--root`, `--profile`,
`--lib-path` (where the project is; the same on every subcommand that
needs one), `--output-format human|json|sarif|github`, `--output FILE`,
`--select RULES` (run only these), `--enable`, `--ignore`,
`--fail-on SEVERITY`, `--baseline FILE`, `--write-baseline`,
`--no-baseline`, `--local` (file-local rules only), `--stats`,
`--max-per-file N`, `--stdin-filename PATH`, `--quiet`, `--exit-zero`.
Flags may follow paths; `--` ends them. `--output-format` rather than
`--format`: `format` is the name of the other subcommand (ruff made the
same choice).

**Standard input** (`-`, or `--stdin-filename PATH`, which implies it)
is what an editor without a language server, or a hook, pipes an unsaved
buffer through, as with `format`. Named, the text takes the file's place
in a `vfs.Overlay`: its project is discovered from the path, the linter
reads the buffer wherever it would have read the file, the lib around it
is the one on disk, and the output names the path. Nameless, it is no
object of any lib: the file-local rules run and a note says so.

**Exit codes:** `0` no findings at or above `fail_on`; `1` findings;
`2` usage or configuration error (including an unknown rule name and a
path that does not exist) or an internal error on some file. Syntax
errors in linted files are findings, not tool errors (ruff/ESLint:
same); a file that cannot be read is a warning.

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
safety gate comparable to the formatter's token equality.

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

Everything is a library first; the command is one front end.

```go
fs  := vfs.NewOverlay(vfs.OS{})            // editor buffers shadow disk
prj, _ := config.Discover(fs, paths, config.DiscoverOptions{WorkDir: cwd, ProfileDirs: dirs, UserConfig: user})
l, _ := lint.New(prj, fs)                  // no globals; safe to keep alive
set, _ := prj.Settings(path)               // one file's format settings and lint scopes

res, _ := l.Run(ctx, lint.Request{Paths: paths})   // many files
for _, d := range res.Diagnostics { ... }          // ranges, code, related, fixes

doc, _ := l.Lint(ctx, path, lint.Options{})        // one document:
doc.Diagnostics; doc.Tree; doc.File; doc.Env       // findings + what they came from

fs.Set(path, unsavedBytes)                 // editor change
affected := l.Invalidate(path)             // files whose findings may differ
doc, _ = l.Lint(ctx, path, lint.Options{})
```

Properties the core guarantees (each has a test in `pkg/lint` or
`pkg/index`):

- **No printing, no `os.Exit`, no `flag`**, no package-level mutable
  state. `lint.Rules()` returns a fresh copy of an immutable table; each
  `Linter` owns its index. Each subcommand of `cmd/dgdtools` is
  `func(ctx, args, stdin, stdout, stderr) int` over the library.
- **No process state.** The library reads no environment variable, no
  home directory and no working directory. Paths are absolute; a front
  end passes its working directory (`DiscoverOptions.WorkDir`) and the
  profile search directories (`profile.UserDirs(env, userConfigDir,
  home)`).
- **All file access goes through `vfs.FS`** (read, exists, walk), config
  loading included, so an overlay can replace disk content with unsaved
  buffers. An overlay's unsaved new files exist and are walked.
- **Generations.** The index is a sequence of immutable generations
  (`index.Snapshot`). A snapshot reads files on first need and never
  changes an answer it has given; `Invalidate` leaves the snapshots in
  use alone and makes the next one, which shares every unit the change
  did not reach. A `Run` or `Lint` holds one snapshot from start to end,
  so linting and editing never wait for each other and need no locking
  discipline from the caller. Results carry their generation
  (`Result.Generation`, `Document.Generation`); one that is no longer
  `Linter.Generation()` describes the lib before a change and is
  computed again.
- **Lazy index.** Units are lexed/parsed/indexed on first demand and
  memoized as *facts only* (symbols with their positions, inherits,
  dependencies), not trees, so a whole lib stays small in memory. Linting
  one file touches its include closure, its inherit chain, and the
  targets of its calls. A file is parsed once for both its tree and its
  facts, and a header is lexed once for both its macros and its
  declarations.
- **True dependents.** For the current generation the index keeps
  reverse edges: which units were built from a file (its include closure,
  and the places an `#include` was looked for and not found), who inherits
  or includes whom, which linted files asked about which object (a call
  target, an object path), and which asked who inherits them.
  `Invalidate(p)` follows them: it drops the units built from `p`, the
  chains of everything that inherits one of those, and returns the files
  whose diagnostics can differ: those, plus the linted files that asked
  about one of those objects, plus (when an object changed) the files
  that asked who inherits them. Creating a file that was looked for and
  missing is a change like any other. Edges are removed with the unit
  that owns them. A change to the force-included header (itself one
  immutable value of the generation) or to the auto object affects every
  file.
- **The whole-lib scan is incremental.** Only "who inherits or includes
  me" needs every file indexed (a module's callback provided by its
  inheritors). The scan runs on first need, one at a time, each caller
  waiting with its own context. Its state belongs to the index, not to a
  generation: a change puts the dropped and the created files back on its
  list, so after the first scan a reverse lookup costs the edits since.
  `Options.NoScan` keeps a lint from starting it (such questions are then
  unknown and `NeedsScan` is set); a server runs `Linter.Scan` in the
  background and lints again. `Run` uses the same mechanism to parse each
  file of a whole lib once.
- **`context.Context`** is checked between files and during the whole-lib
  scan; a cancelled or superseded reverse lookup answers "unknown", never
  "missing".
- **Diagnostics** carry byte offsets (`Start`, `End`), 1-based line/column
  for both ends, a stable `Code` and `Rule` name, a `Fingerprint` of the
  source line (what baselines match on), `Related` locations (the
  unclosed `{`, the definition whose arity is violated, the include that
  breaks inherit order; their positions come from the index, no file is
  read again) and `Fixes` as text edits. `diag.UTF16Col` converts a byte
  column to an LSP UTF-16 column; bytes that are not valid UTF-8 (the
  corpus's Latin-1) count as one unit each, matching how editors decode
  them.
- **The diagnostics model is the one editors and CI already have.** A
  fix has an `Applicability`: `safe` (keeps what the code means; a tool
  may apply every safe fix at once) or `unsafe` (offered, applied when
  asked for by name), the two levels of ruff and Biome; the zero value
  is `unsafe`, so a fix is safe only when its rule says so. A diagnostic
  has `Tags`, `unnecessary` and `deprecated`, the two an editor renders
  specially (LSP `DiagnosticTag`: faded, struck through); a rule states
  its tags once (`Rule.Tags`). Every rule has a documentation address
  (`Rule.URL`, a section of `docs/rules.md`, which is written from the
  rule table and checked by a test), for LSP `codeDescription`, SARIF
  `helpUri` and the JSON report.
- **What a rule finds is filtered in one place, in one order.**
  `Pass.Report` always records the finding and returns it. The engine
  then (1) withholds the findings of a `CompiledOnly` rule that lie in a
  conditional branch chosen by guess: such a rule claims that code does
  not compile or fails when it runs, which nobody can say of code that
  may not be compiled at all (a property each rule states; it does not
  follow from its group: `bad-directive` is a syntax rule that does
  report there, because the preprocessor reads directives in skipped
  branches too); (2) drops what a suppression comment covers; (3)
  subtracts the baseline; (4) applies `max_per_file`, last, so that a
  limit counts what would be shown and cannot hide a new finding behind
  suppressed or baselined ones. What (4) holds back is counted
  (`Stats.Capped`, in the summary); `--max-per-file -1` lifts the
  limits, and `--write-baseline` always does.
- **Reports name files the way the user did.** `pkg/report` writes a
  file under `Options.BaseDir` (the front end gives its working
  directory) by its path from there and any other by its absolute path;
  it reads no working directory itself. The human format ends each
  finding with `[CODE name]`. SARIF locations are URIs: a relative
  reference with `uriBaseId: %SRCROOT%` (and `originalUriBaseIds` saying
  where that is) under the base, a `file:` URI outside it. A location in
  a profile's stub is a URI already (`dgdtools-profile://NAME/…`) and is
  written as one in every format, never run through path arithmetic.
  JSON carries the model's new parts (`tags`, fix `applicability`,
  `fingerprint`), a `rules` table for the rules that occur (code, group,
  summary, documentation address) rather than a URL on each of half a
  million findings, and `errors`. SARIF regions give the byte range
  beside the columns, which count bytes; SARIF `fixes` are not written,
  because their offsets count characters.
- **Notes** about a run as a whole are `diag.Note` values with a stable
  `Kind` (`no-lib`, `incomplete-lib`, `outside-lib`, `profile`,
  `unplaced-checkout`).
- **One bad file cannot take down a run, and is not silent.** A file
  that could not be linted is a `FileError` in `Result.Errors` (the
  error of `Linter.Lint`) with its kind. `unreadable`: the file system
  refused; the rest of the run stands, the front end warns. `internal`:
  the tools crashed on it, in the analysis (`index.InternalError`,
  recorded per generation, also for a file read on another's behalf) or
  in a rule (the message names it); the run goes on, what depends on
  that file is unknown, and `dgdtools lint` exits 2 whatever else it
  found, because a result with a hole in it must not pass for a clean
  one. `dgdtools profile build` refuses to write a profile then.

Measured on the 108,116-file lib (`TestCorpusEdit`, 681 single-file
edits after a whole-lib run): `Invalidate` 1.5 ms median (2.9 ms at the
95th percentile; 156 ms for a file 89,000 others inherit), linting the
edited file 0.1 ms median, completing the scan again under 1 ms, one
file read per edit, and findings identical to the whole-lib run's.

### 6.1 A future `dgdtools lsp`

A thin layer, the reserved `dgdtools lsp` subcommand, holding one
`vfs.Overlay` and, per project a `config.Workspace` discovers, one
`lint.Linter`:

| LSP feature | Maps onto |
|---|---|
| `textDocument/didOpen`, `didChange`, `didClose` | `overlay.Set/Delete` + `Linter.Invalidate(path)` |
| `textDocument/publishDiagnostics` | `Linter.Lint` for the changed document, debounced, with the previous lint cancelled through its context. `Invalidate` returns the dependents; those that are open are linted again. `Diagnostic.code` = rule code, `codeDescription` = the `explain` text, `relatedInformation` = `Related`, positions via `diag.UTF16Col`. |
| `workspace/didChangeWatchedFiles` | `Invalidate` for disk changes; a config change rebuilds the `Linter`. |
| `textDocument/codeAction` | `Fixes` on the diagnostics in range (quick-fix edits), plus generated "suppress with `dgdtools-ignore lint/RULE`" actions. |
| `textDocument/definition` | the index: resolve the identifier under the cursor through `Snapshot.Chain` (functions and variables; `::` and `label::` calls), `->` calls with a known receiver through `LookupCallable`, `inherit`/`#include` paths through `ObjectPath`/`ResolveInclude`, macros through the file's `pp.Env`. `ChainFunc`, `ChainVar` and `pp.Macro` carry the defining file and position; a definition in a profile stub is at `dgdtools-profile://NAME/std/room.c`. |
| `textDocument/hover` | the same lookups: specifiers and argument range of a function, the object that provides it, a macro's body and class. |
| `textDocument/documentSymbol`, `workspace/symbol` | `syntax.File.Decls`; the units' symbol tables. |
| `textDocument/formatting` | `pkg/format`, already a pure function. |

What the server adds itself: JSON-RPC transport, position conversion,
debouncing, and per-document versioning. No analysis logic lives there.
What the core still lacks for it: a "node at offset" helper and scopes for
local variables (section 11).

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

## 8. The formatter

The formatter is a trivia-only rewriter with a gate: it writes every
significant token's bytes unchanged, synthesizes the whitespace between
them, and returns a result only if the output re-lexes to the same token
stream (`pkg/tokcmp`) and formatting it again changes nothing. That
design is right for a formatter that must also work on files that do not
parse, and it is kept. What it lays tokens out *by* is the syntax tree.

**Plan.** `pkg/format` reads the file as every other tool does
(`pp.Env.Scan`, `syntax.Parse`) and records two things per token: the
indentation level of the statement or declaration it belongs to, and its
role. Levels come from statement structure, so the body of a braceless
`if`/`else`/`for`/`while`/`do` is one level in, `else if` is a chain,
case labels stand with their `switch`, and any line that does not begin a
statement continues one. Roles are what a token stream cannot tell:
binary or unary `+ - * &`, prefix or postfix `++ --`, the `?` and `:` of
a conditional expression, the braces of a block, a `switch` or an array
literal, which `{` and `else` join the line before them, and where a
function's header ends.

**Printer.** One pass over the token stream writes each token with the
separator and indentation the plan gives. Inside a statement the printer
tracks only the brackets open in it (parentheses, `({`, `([`), for the
indentation of the lines that continue it.

**Every branch.** A formatter lays out all the text of a file, not one
configuration of it. The file is parsed as `pp` selects it, then again
with other arms of its conditionals selected (`Env.ScanArms`), until
every token has been read in some arm; a token is planned by the first
round that reads it. The printer starts each arm from the state the
conditional began with, so braces opened in both arms of an `#if`/`#else`
count once, and continues after `#endif` from the arm `pp` selected.

**Where the parser gives up.** A statement or declaration the parser
could not read is marked as not understood. There the printer falls back
to what the neighbouring tokens suggest (the previous token decides unary
or binary, braces are counted for indentation, relative to the level of
the region) and moves no line break, since the parser's recovery looks at
line breaks and the result must be a fixed point. The statement read
right after a skipped construct parsed, and is laid out by its shape
(`BadStmt.Resumed`), though the rules never see it.

**One file, one result.** A file is formatted from its own text and its
options (`format.OptionsFor`: the three settings resolved for that file,
the project's predefined names and configured macro classes). No other
file is read: the parser runs with a partial environment, so macros from
headers are met with the same tolerance as in a checkout with nothing
else in it, and the result is the same in an editor buffer, in a
wizard's folder and in the whole lib. `dgdtools format` finds each
file's project the way `dgdtools lint` does (the nearest root config at
or above it), which gives the dialect (the config's or its profile's);
the `unformatted` rule calls the same function with the same per-file
options.

**Gaps.** The printer's unit of work is the gap between two significant
tokens. A skip marker (`dgdtools-ignore-start format` … `dgdtools-ignore-end format`) and a line
selection (`format.Lines`) are both "copy these gaps as they are": the
tokens of a skipped gap still pass through the printer, so its bracket
and conditional state stay right for what follows. A selected line
whose neighbours keep their own indentation is indented from them: an
unselected line that begins a statement teaches the indentation of its
level, first the lines above in the same block, then the ones below.
`format.Edits` reports a result as one replacement per changed gap.

What the tool is (its one style, the three settings, skip markers,
check and diff, formatting part of a file) is in
[dgdfmt.md](dgdfmt.md).

Measured on the Viking lib (182,126 `.c`/`.h` files): 180,850 formatted,
1,276 refused because they do not tokenize, no gate failure, with spaces
and with tabs; a second pass over the formatted tree changes nothing;
and for every file, all of its lines as a range give what `Format`
gives, arbitrary ranges pass the gate, and a range of the formatted file
changes nothing.

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
the linter gives. A spot check of four findings whose kind the oracle's
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
- `pkg/config` (discovery, settings scopes, `.editorconfig`, the
  user-level fallback, validation), `pkg/editorconfig`, `pkg/pp`,
  `pkg/diag`, `pkg/directive`, `pkg/calls` (the driver's calls, declared
  registry forms), `cmd/dgdtools` (every subcommand: exit codes, output
  formats, baseline workflow, discovery from anywhere, check/diff/stdin,
  line ranges, git-changed lines, configuration verification).
- Golden files, rewritten with `-update`: `pkg/report` (a result with
  one of everything, in every format, with and without a base
  directory), `pkg/profile` (the uncompressed file format), and
  `docs/rules.md` itself, which `pkg/lint` checks against the rule
  table.
- `pkg/profilebuild`: a small whole lib built into a profile, then one
  of its trees linted alone against the profile and compared with the
  whole-lib run.
- Corpus (`-tags corpus`): `TestCorpusParse` (182,126 `.c`/`.h` files
  parsed twice: no panics, span and ordering invariants) and
  `TestCorpusLint` (whole-lib run, no internal errors, core directories
  free of syntax-group findings). Both pass on the VikingMUD lib.

## 10. Mudlib profiles

### 10.1 The problem and the prior art

Most wizards do not have the mud. They download `/players/<name>/` or
`/d/<Domain>/`, write their area, and upload it. Everything their code
leans on (the headers behind `#include <mudlib.h>`, `/std/room`, the
daemons) is somewhere they cannot see. Without it a linter can only run
file-local rules, and even those with a partial `#define` environment.

Every mature tool solves this the same way: ship *declarations* of the
platform separately from the user's code. TypeScript has `lib.d.ts` and
`@types`; Python type checkers have typeshed stubs; gopls and
rust-analyzer read the standard library's export data; clangd without a
`compile_commands.json` falls back to built-in system headers. A **mudlib
profile** is that for a mud.

### 10.2 What a profile is

A profile is a manifest plus stub files (`pkg/profile`):

- **Manifest (JSON):** the dialect (`language: lpc`, `driver: dgd`,
  version, `//` comments, closures, typechecking level, predefined
  macros); the driver's include dirs, force-included header, auto
  object and create function; how relative object paths resolve; the
  lib's conventions
  (call/object/format registries, autosave markers, virtual paths,
  specifier macros, macro class overrides); `covered` (directories the
  profile captures completely); `holes` (globs deliberately left out);
  `checkouts` (the per-wizard layout, `players/*`, `d/*`).
- **Stubs:** one per shared file, *as source in the lib's own dialect*,
  and they are the file's public interface verbatim minus bodies, as in a
  C header, a `.d.ts` or a typeshed `.pyi`. A header's stub is its active
  `#include`s and `#define`s. An object's stub is its `#include`s, its
  inherit statements, and every non-private function and global variable
  declaration exactly as written: specifiers (`static`, `nomask`,
  `varargs`, `atomic`), return type, parameter types and names, `varargs`
  parameters and `...`, with the body replaced by `{}`. A type written as
  a macro stays that token (`function`, `status`): the profile carries the
  headers that define it. A function written without a type stays without
  one, because under typechecking that is what makes it void.

  ```
  #include <std.h>
  inherit I_OBJECT;
  mapping negfuns;
  varargs string calc_obvious_exits(int brief_flag);
  static void create() {}
  void reset(int arg) {}
  ```

  The index keeps these declarations as facts, from real files and stubs
  alike: `FuncInfo`/`ChainFunc` carry `Decl` (the declaration as written)
  and `Type` (its return type), `VarInfo` carries `Type`, and
  `index.Params(decl)` splits out the parameters. That is what hover and
  signature help will show, and what type rules would read.

Stubs being source is the central decision. The lexer, the macro
classifier, the parser, include resolution, inherit-chain flattening and
every lookup already work on source; a profile needs **no second
representation of facts and no second code path**. A stub is not a file
on disk and is not named like one: the stub for `/std/room.c` of profile
`viking` is at `dgdtools-profile://viking/std/room.c`, `profile.FS`
answers for those locations as a `vfs.FS`, and the index reads a lib file
from disk when it is there and from its stub location otherwise. This
is the `.d.ts` model, and it is also what makes the format dialect-
neutral: a FluffOS profile would hold FluffOS-syntax stubs.

Details that keep stubs honest:

- A file that could not be fully read when the profile was built carries
  `#pragma dgdtools partial`; the index treats its symbol table as partial,
  so lookups through it are unknown, exactly as with the original.
- Declarations inside a conditional branch the builder could only guess at
  are emitted under `#ifdef __DGDLINT_UNDECIDED__`, so the consumer also
  sees them as guessed.
- Object-local macros are dropped unless the object's declarations use
  them (a type macro, a path macro in an inherit statement); a `.c` file
  that other files `#include` is treated as a header and keeps its macros.
- Function bodies, comments, private functions, private variables and
  anything matching `omit:` never enter a profile. Because private
  functions are absent, a call to one in a profiled object is reported as
  "not defined … or is private there" (`callable-not-found`) where the
  whole lib would say "is private" (`static-crossobj`): the same bug under
  another name. `omit` and `exclude` globs are recorded
  as `holes`, because "under a covered directory but deliberately absent"
  must read as unknown, not as missing. (This was a real false positive
  in the first version of the tests.)

**File format:** gzip of `dgdtools-profile 1`, the manifest, and the stubs
in path order (`=== /std/room.c` separators). No timestamps: the same lib
gives the same bytes. Versioned; a newer format is refused with a
"rebuild the profile" message.

**Selection** (`profile_build`) is by use, not by a hand-picked list: a
file referenced (inherit, `#include`, or a constant call/clone/find
target) from at least `share_min_owners` distinct owners is shared, where
an owner is one per-wizard tree (`checkouts`) or one other top-level
directory. Added to that: files matching `share`, everything under the
include dirs, the force-included header, the auto objects, and the
closure of what all of those include and inherit. `omit` always wins.
Inference finds files, not directories, so only a `share` glob that takes
a whole directory makes it `covered`.

Measured on VikingMUD (ten trees, 9,106 whole-lib findings; "extra" is 54
in every row, so precision does not move):

| Selection | Size | Stubs | Identical | Lost |
|---|---|---|---|---|
| core `share` globs only (previous) | 46,926 B | 465 | 8,989 | 117 |
| inference only, N=2 / 3 / 5 | 254,689 / 159,168 / 102,919 B | 2,012 / 923 / 451 | 8,972 / 8,962 / 8,951 | 134 / 144 / 155 |
| N=2 + core globs | 260,885 B | 2,218 | 9,045 | 61 |
| N=3 + core globs | 166,962 B | 1,161 | 9,036 | 70 |
| **N=5 + core globs** | 113,878 B | 723 | 9,026 | 80 |

Inference alone is worse than the core list: without covered directories
"`/std/nosuch` does not exist" cannot be proven, and core files with few
users drop out. Inference on top of the core globs recovers findings
against widely used code outside the core (parts of `/com`, domain
libraries such as `/d/*/std`, daemons). N=5 is the default: it recovers
37 of the 56 findings N=2 would, at under half the size, and pulls in the
fewest wizard files (55 under `/players`, 189 under `/d`, against 845 and
875 at N=2). Those files' public declarations become part of the profile;
`omit: ["players/**"]` keeps them out.

### 10.3 Using a profile

- **No profile is embedded in the tools.** A profile describes one mud and
  is published by that mud (VikingMUD's lives in its own repository).
  `profile: NAME` is looked up as `NAME.profile` in, in order: the
  directory of the config file that names it (the working directory for
  `--profile`); each entry of `$DGDTOOLS_PROFILE_PATH`; the user config
  directory `os.UserConfigDir()/dgdtools/profiles`; and
  `~/.config/dgdtools/profiles` where that differs (macOS). A reference
  with a path separator or ending in `.profile` is a path. When nothing
  is found the error lists every location searched. The same resolution
  is library API (`profile.UserDirs`, `profile.Resolve`, `profile.Load`),
  which is what a language server will call; the front end reads the
  environment and passes the values in.
- **Whole lib + profile:** if the profile's force-included header exists
  under the root, the project is a whole lib; the profile only supplies
  settings, and stubs fill nothing because every file is on disk. A whole
  lib with `profile_build.driver_config` needs no profile at all.
- **Partial checkout:** otherwise there is no lib root on disk: the
  checkout directory stands for its lib path and the profile's stubs for
  the rest.
  The lib path comes from `lib_path:`, or is read off the directory's
  path using the profile's `checkouts` patterns
  (`…/players/drake/rooms` is `/players/drake`).
- **What is provable in a partial checkout** is decided per path by
  `Index.Covers`: the checkout itself (a wizard has all of their own
  tree) and the profile's covered directories minus holes. A reference
  into a covered place that resolves to nothing is missing; a reference
  anywhere else (another wizard, a domain not downloaded, `/com`) is
  unknown and silent. An `#include` is provably missing only if every
  place the driver would look is covered.
- If the checkout's place in the lib cannot be determined, the profile's
  headers still give complete `#define` environments (so the syntax rules are
  exact), but nothing is claimed between the wizard's own files, and one
  note asks for `lib_path`.

### 10.4 Should a lone checkout pick a profile by itself?

No: one line is required. A profile asserts facts ("`/std/room` has no
function `set_shrot`"). Applied to the wrong mud those are confident
false errors, the clang-tidy-guessing-flags failure, and dgdtools is a
general DGD tool that happens to embed one mud's profile. The cost of
being explicit is a one-line file (`profile: viking`) or a flag; the cost
of guessing wrong is the tool's credibility. With no config at all the
behaviour is unchanged: file-local rules and a note.

The lib *path* of a checkout, by contrast, is inferred, because the
evidence is in the path and the mapping is printed in the run's notes;
`lib_path:` overrides it.

### 10.5 One assumption

For `this_object()->fn()` and callbacks, a function missing from a file's
own chain is looked for in the files that inherit or include it. In a
partial checkout only the checkout can be searched. The linter assumes a
wizard's own files are inherited by their own files, reports the miss,
and says so in the message ("nor in this checkout; inheritors elsewhere
in the lib cannot be seen"). The strictly safe alternative, treating
every such miss as unknown, would silence the single most useful runtime
check for a wizard (`add_action("do_x")` with no `do_x`). Measured cost:
1 false finding in 9,043 (below).

### 10.6 Measured: ten real trees, profile only vs. whole lib

`TestCorpusPartialCheckout` builds the profile from the whole lib,
copies each tree alone to a temp directory with a two-line config, lints
it, and compares with linting the same tree in place.

| | findings |
|---|---|
| whole lib, the ten trees | 9,106 |
| profile only | 9,043 |
| identical | 8,989 (98.7% of whole-lib) |
| lost (whole-lib only) | 117 |
| extra: syntax errors behind an include that can no longer be proven missing | 52 |
| extra: other | 2 |

Trees: players/drake, helgrim, iomi, crovax, klopf, knubo, mousepad,
caveral, okjuna and d/Valinor (6,437 of the findings).

- **Lost (116)** are all references to code in neither the checkout nor
  the profile, now unknown: `target-object-missing` for
  `/d/Odyssey/…`, `/players/graxius/…`; `callback-arity` and
  `callable-not-found` against `/d/Empire/daemons/weapond`, `/com/s/more`;
  `include-not-found` for `"../bob.h"` (above the checkout).
- **Extra syntax errors (52)** are real errors in files whose first
  problem is an `#include` reaching outside the checkout. With the whole
  lib that include is provably missing, which is fatal and withholds what
  follows; from a checkout it is only unknown, so the file's own errors
  show. 47 of the 52 are one non-LPC file (`mare.c`).
- **Extra other (2):** one is false: `store_fp("do_clippy")` in
  `/players/knubo/clippy/clippy.c`, whose `do_clippy` is provided by
  another wizard's file that inherits it (10.5). The other is a true
  finding renamed: `MONSTER_D->get_id_string()` calls a private function,
  which the whole lib reports as `static-crossobj` ("is private") and the
  profile, which lists no private functions, as `callable-not-found`.

### 10.7 The viking profile, and what it exposes

`viking.profile` (kept outside this repository, with its build inputs
and a rebuild script): 46,926 bytes compressed, 217 KB of declarations; 394 objects and 71 headers from `/std`, `/obj`, `/room`,
`/secure`, `/dgd` plus what they include and inherit; covered:
`/dgd/ /obj/ /room/ /secure/ /std/` and the include dirs. `/com` was left
out (wizard code rarely references commands; it stays unknown).

The owner has decided it may be public. **Publishing it gives anyone who
fetches it:** the paths of
those 465 files; the full declarations (names, types, specifiers,
parameter names) of their non-private functions and global variables,
including `/secure` daemons and the login objects; and the full text of
every `#define` in the shared headers (path constants, level constants,
prompt macros). It publishes no function bodies, comments, private
functions, private variables or data. A scan for credentials, e-mail addresses, IPs and URLs found only a
public URL and identifier names such as `password`. Whether that surface
may be public is the lib owner's decision; `omit:` removes anything from
the next build.

### 10.8 Other LPC drivers

The profile's `dialect` is data, and only `driver: dgd` is accepted
today. Generalizing to FluffOS or LDMud is mostly not profile work:

- **Grammar variants** in `pkg/lexer`/`pkg/syntax`, selected by dialect:
  FluffOS `(: … :)` closures, `foreach`, `class`/`struct`, `ref`,
  `function` types, array slicing `[<n]`, `efun::`; LDMud inline closures
  `#'f`, `({…})` quoted arrays, structs, `foreach`, string ranges with
  `<`. The three "legacy" rules become dialect-conditional (they are
  errors only in DGD).
- **Efun/kfun tables** per driver (name, arity, purity) for name
  resolution and for knowing that e.g. `call_other` and `call_out` exist
  with which argument positions; today the handful the linter needs are
  hard-coded as DGD kfuns.
- **Semantics the runtime rules encode:** visibility and `call_other`
  reachability (`static`/`private`/`protected`/`nosave` differ),
  `inherit` semantics (virtual inherit in LDMud, no labels in FluffOS),
  how relative paths and includes resolve (master-object hooks rather
  than a driver config file), simul-efuns, and the preprocessor's
  predefined macros and directives (`#pragma strict_types`).
- **Build input:** the driver's config format (FluffOS `config.<mud>`,
  LDMud command-line/master settings) in place of the DGD config reader.

The profile format itself needs nothing but new stub syntax.

## 11. Implemented vs. deferred

Implemented: everything in sections 2–7 except the items below.

Deferred, in rough priority order:

1. **Conditions inside headers that depend on the includer.** A header's
   table is built from the header alone and shared (2.2); such a
   condition is a guess. Deciding it means reading the header once per
   distinct environment it is included in.
2. **Name resolution** ("undefined function", "undeclared variable",
   direct-call arity, redeclaration): the next 2,500 files the driver
   rejects. Needs the kfun table, scopes, and inherited variables.
3. **Receiver flow** for `ob->fn()` where `ob` is a local assigned from
   `clone_object("…")` and the like: 77% of late-bound call sites still
   have an unknown receiver.
4. **`--fix`** (see 4.1) and more fixes than `adjacent-strings`.
5. **Linting headers** (`.h` files with code) and `.c` fragments in the
   context of their includers.
6. **The formatter in code the parser cannot read from the file alone**
   (`ob->QN` with `QN` a macro from a header, and the like): such a
   statement keeps its line breaks. Giving the formatter the project's
   headers would lay it out too, at the price of a result that depends
   on other files.
7. **`dgdtools lsp`** (6.1) and **`dgdtools rename`** (both names are
   reserved by the command), plus `dgdtools init` to write a starter
   config. Line wrapping in the formatter is not planned
   ([dgdfmt.md](dgdfmt.md) 2.2).
8. **Type rules** on the declarations the index now keeps. Candidates,
   by the driver errors they would catch (file counts from the oracle):
   wrong argument count in a direct call to a function declared in the
   chain ("too many/too few arguments", 289 files; needs the kfun table
   for the rest); using the value of a `void` function, including through
   `"/path"->fn()` ("void value not ignored", 46); definition that does
   not match its prototype ("declaration does not match prototype", 43);
   literal assigned or returned against a declared `int`/`string`
   ("incompatible types", 246, a subset). None is added yet: each needs
   macro-expanded types or the kfun table to be provable.
9. **Profiles:** recognizing a checkout by evidence in its files when its path says
   nothing; fetching or updating profiles is deliberately out of scope
   (no network access in the tool).
10. Backslash-newline outside strings is tolerated (not reported) but not
   modelled by the lexer.
