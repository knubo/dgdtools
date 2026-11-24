# dgdtools

Source tooling for the [DGD](https://github.com/dworkin/dgd) dialect of LPC:

- **dgdfmt**, a code formatter in the gofmt tradition
- **dgdlint**, a linter that finds what the driver will reject at load
  time (syntax errors, constructs DGD does not accept, missing includes)
  and what it will not notice at all (calls to functions and objects that
  do not exist)
- **dgdcmp**, which compares two files by significant token stream to
  prove a diff is formatting-only

Existing LPC tooling targets LDMud or FluffOS and mishandles DGD syntax:
`({ })` / `([ ])` literals, the `<-` operator, `..` ranges, labeled
inherits, K&R-style typeless functions. These tools implement the DGD 1.7
grammar, with driver build variants (`SLASHSLASH` line comments,
`CLOSURES`) available as configuration.

## Installation

With Go 1.25 or later:

```
go install github.com/mmcdole/dgdtools/cmd/...@latest
```

Or from a clone:

```
git clone https://github.com/mmcdole/dgdtools
cd dgdtools
make build          # binaries in ./bin
make test
```

`make corpus LPC_CORPUS=/path/to/lib` runs the full-tree invariants
against a real mudlib: byte-exact lexer round-trip, comparator
self-compare, and the formatter gate on every file.

## Configuration

All three tools read `dgdtools.yml` (or `.dgdtools.yml`). dgdfmt and
dgdcmp search upward from the working directory; dgdlint searches upward
from the path it is asked to lint, so it works from anywhere on disk.
Every field is optional and the tools work with no config file at all. A
natural setup is one file next to the mudlib:

```
mud/
├── dgdtools.yml
├── etc/mud.dgd     ← the driver's own config file
└── lib/            ← root: lib
```

```yaml
dialect:
  slash_slash: true          # // comments (driver SLASHSLASH build flag)
  closures: false            # `function` keyword (driver CLOSURES flag)

root: lib                    # filesystem directory that is the lib's "/"
driver_config: etc/mud.dgd   # read include_dirs, include_file, auto_object,
                             # typechecking from the driver's config

exclude:                     # glob patterns, ** crosses directories
  - "**/attic/**"            # retired code kept for reference
  - "**/old/**"
  - "**/*.pre*"              # parked file copies
  - "**/var/**"              # generated runtime data

format:
  indent: 4
  line_endings: preserve     # preserve | lf | crlf
  max_blank_lines: 2
  function_headers: split    # split (name at column 0) | joined

lint:
  # Functions whose string argument names a callback on this_object();
  # call_other and call_out are built in. Value = 0-based argument index.
  call_registry: { store_fp: 0, add_action: 0 }
  autosave_markers: [set_auto_save, D_STORE]

  rules:                     # name, code, or group -> off|info|warning|error
    unreachable-code: warning
    raw-inherit-path: { severity: warning, deny: ["/std/", "/obj/"] }
    lifecycle-chain: { names: [create] }
  overrides:
    - paths: ["players/**"]
      rules: { runtime: off }
  fail_on: error             # exit 1 at this severity or above
```

`dgdtools.example.yml` documents every setting. `extends: other.yml`
layers one config on another (scalars and lists replace, maps merge).

### Lint settings

| Setting | Purpose |
|---|---|
| `driver_config` | Path to the DGD driver config. `include_dirs`, `include_file`, `auto_object` and `typechecking` are read from it; set them under `lint:` instead (`include_dirs`, `include_file`, `auto_objects`, `typechecking`) to override or if there is no driver config at hand. Without them inherit macros do not resolve and cross-file rules stay quiet rather than guess; without `typechecking` the rules that depend on it are silent. |
| `relative_object_paths` | What `"std/room"` (no leading slash) is relative to: `root` (default; what DGD does) or `file`. |
| `specifier_macros`, `macros`, `defines` | How to read macros dgdlint cannot classify itself. `specifier_macros: [public]` is the default. `macros: {FOREACH: statement}` overrides the class derived from a `#define` (`type`, `specifier`, `statement`, `expression`, `opaque`). `defines:` lists names to treat as defined in `#ifdef`. |
| `call_registry` | Mudlib functions whose string argument names a callback on `this_object()`, e.g. `add_action("do_pull", "pull")`. |
| `object_registry` | Mudlib functions whose string argument is an object path; `clone_object`, `compile_object`, and `find_object` are built in. |
| `format_registry` | scanf-style functions and the position of their format string; `sscanf` is built in. |
| `autosave_markers` | Function or macro names whose presence marks an object as persisting via `save_object`. |
| `virtual_paths` | Lib-path globs served by virtual-object daemons (objects with no `.c` file). |
| `rules` | Maps a rule name, code, or group (`all`, `syntax`, `runtime`, `suspicious`, `convention`) to `off`, `info`, `warning` or `error`, or to `{severity: …, options}`. Giving a severity turns the rule on. |
| `overrides` | `[{paths: [globs], rules: {…}}]`: different rule settings for parts of the lib. |
| `baseline` | Baseline file name (default `dgdlint-baseline.json` next to the config). |
| `fail_on` | Exit-1 threshold: `info`, `warning` or `error` (default). |

## dgdfmt

```
dgdfmt file.c               # print formatted source to stdout
dgdfmt -l dir/              # list files whose formatting differs
dgdfmt -d file.c            # show a unified diff
dgdfmt -w dir/              # rewrite files in place
```

The style is fixed, with a small number of toggles (`-indent`,
`-line-endings`, `-max-blank-lines`, `-func-headers`); flags override the
config file. The default style:

- Function definitions in KNF: specifiers and return type on one line,
  the name at column 0, braces on their own lines. `-func-headers joined`
  keeps the header on one line instead.
- Definitions written entirely on one line stay on one line
  (`string query_code() { return code; }`).
- Control-flow braces are cuddled (`if (x) {`, `} else {`) when the
  source uses Allman layout; one-line brace blocks keep their own lines.
- `case` labels at the `switch` level, bodies one deeper.
- Horizontal spacing is normalized (`a+b` becomes `a + b`, `f( a,b )`
  becomes `f(a, b)`); the author's line breaks are preserved and long
  lines are never re-wrapped.
- Comments are never reflowed. Multi-line block comments shift as a unit,
  and function headers containing comments keep their original layout.
- Trailing whitespace is removed, blank-line runs are capped, files end
  with exactly one newline, and line endings are preserved per file
  unless a policy is set.

### Safety

dgdfmt is built to be provably behavior-preserving. The lexer is
byte-lossless (whitespace and comments are tokens; nothing is ever
transcoded), and the formatter only rewrites the whitespace between
tokens. Before any output is accepted it is re-lexed, required to be
token-identical to the input, and required to be idempotent; a file that
fails the gate, or does not fully tokenize, is left untouched and
reported.

`dgdcmp a.c b.c` exposes the same comparison directly: exit 0 means the
two files differ only in formatting. `dgdcmp -dump file.c` prints the
token stream.

## dgdlint

```
dgdlint                      # lint the project found from the working directory
dgdlint dir/ file.c ...      # lint these; the project is found from the paths
dgdlint rules                # list rules: code, name, default, scope
dgdlint explain RULE         # what a rule means and why it fires
dgdlint config [path]        # show the resolved project: root, driver settings, rules
dgdlint --format json ...    # also: sarif, github
dgdlint --select syntax ...  # run only these rules (names, codes or groups)
dgdlint --write-baseline     # accept current findings; later runs report only new ones
dgdlint --stats ...          # per-rule counts and what could not be verified
```

Exit codes: `0` no findings at or above `fail_on`, `1` findings, `2`
usage, configuration or I/O error.

dgdlint parses every file with its own error-tolerant parser for the DGD
1.7 grammar, on source as written: macros are classified from their
`#define`s (type, specifier, statement, expression) rather than expanded.
Its rule is **prove it or stay silent**: a finding is reported only when
it can be proven from what dgdlint can see. An unresolved `#include`, an
inherit path it cannot evaluate, a receiver it cannot identify, code in an
`#ifdef` branch it cannot decide, a statement that uses a macro it cannot
see through: each makes the affected facts *unknown*, and nothing is
concluded from an unknown. `--stats` shows how much was unknown.

**What it needs.** Nothing, for the file-local rules: `dgdlint castle.c`
with no config anywhere reports syntax errors and suspicious constructs.
Cross-file rules need the lib root, which comes from the nearest
`dgdtools.yml` above the linted path (or `--root`); a root is never
guessed. A file outside the root gets the file-local rules only.

| Code | Rule | Default | Finds |
|---|---|---|---|
| E001 | `syntax-error` | error | the file does not parse (missing `;`, unbalanced brackets, illegal characters, …) |
| E002 | `adjacent-strings` | error | `"a" "b"`: DGD has no implicit string concatenation |
| E003 | `legacy-closure` | error | `(: ob, "fn" :)`: not DGD syntax |
| E004 | `declaration-initializer` | error | `int x = 5;`: not DGD syntax |
| E005 | `declaration-after-statement` | error | local declaration after a statement in the same block |
| E006 | `include-not-found` | error | `#include` target missing |
| E007 | `bad-directive` | error | text after `#else`/`#endif`/`#include "x"`, unknown directives |
| E008 | `inherit-after-declaration` | error | `inherit` after a declaration, including one from an earlier `#include` |
| E009 | `missing-parameter-type` | error | untyped parameter in a typeless function, under `typechecking >= 1` |
| E010 | `return-value` | error | value returned from a void or typeless function; bare `return;` in a typed one |
| R001 | `callable-not-found` | error | string-referenced function (`obj->fn()`, `call_other`, `call_out`, registrars) missing from the target's inherit chain; the call silently returns nil |
| R002 | `static-crossobj` | error | call_other to an unreachable function: `private` always, `static` from another object |
| R003 | `undefined-prototype` | error | function declared but never defined in the chain, and called |
| R004 | `target-object-missing` | error | literal object path (inherit, `clone_object`, call target) with no backing file |
| R005 | `callback-arity` | warning | dispatched call passing an argument count the target cannot accept |
| R006 | `lifecycle-chain` | warning | lifecycle function (default `create`) omits calls to inherited implementations |
| S001 | `assignment-in-condition` | warning | condition assigns a bare constant or an unparenthesized `&&`/`||` result |
| S002 | `no-effect-statement` | warning | comparison used as a statement (`x == 1;`) |
| S003 | `sscanf-format` | warning | more variables than `%`-conversions |
| S004 | `unreachable-code` | off | statement directly after `return`/`break`/`continue`/`goto` |
| C001 | `raw-inherit-path` | off | inherit using a literal path where the lib mandates macros |
| C002 | `missing-visibility` | off | function with no visibility specifier |
| C003 | `unformatted` | off | file that is not dgdfmt-formatted |
| C004 | `static-autosave-var` | off | `static` global in an auto-saving object |
| C005 | `unresolved-inherit` | off | inherit path the macro evaluator could not resolve (info) |
| C006 | `unused-suppression` | off | a `dgdlint:` comment that suppresses nothing |

The `E` rules are things the DGD compiler rejects: an object with one does
not load. They were checked against DGD 1.7.9 compiling a 108,000-file
lib: of 30,643 files flagged by an `E` rule, none compiles. The `R` rules
are code that compiles and then fails or silently returns nil. Rule names
and codes are stable.

### Adopting it on an old lib

A lib that predates DGD will have tens of thousands of findings in code
nobody loads. Record them once and gate only on new ones:

```
dgdlint --write-baseline     # writes dgdlint-baseline.json next to dgdtools.yml
dgdlint                      # from now on: exit 1 only for findings not in the baseline
```

Baseline entries are matched by file, rule and the text of the offending
line, not by line number, so editing elsewhere in a file does not bring
old findings back. Rules can also be tuned per path with `overrides`.

### Suppressing a finding

From any comment, by rule name or code:

```
/* dgdlint:disable-next-line callable-not-found */
/* dgdlint:disable-line rule-a,rule-b */
/* dgdlint:disable rule-a */   ... /* dgdlint:enable */
```

File-scoped `disable` requires an explicit rule list.

### Conditional assignments

Assign-and-test is an established LPC idiom and is accepted:

```c
if (sz = sizeof(items)) {              /* accepted */
    process(items);
}
if (sz = 0) {                          /* warning: probably == */
    reset_items();
}
if (sz = load_items() && ready) {      /* warning: sz receives 0 or 1 */
    process(items);
}
if ((sz = load_items()) && ready) {    /* accepted: intent is explicit */
    process(items);
}
```

Compile-invalid assignment targets such as `!ob = present(...)` are left to
DGD rather than being duplicated as lint rules.

### Lifecycle chaining

The rule is proof-based: files with no explicit inherit, parents without the
configured lifecycle function, and unresolved parents with no known definition
are skipped. With multiple defining parents, label and chain each one:

```c
inherit room I_ROOM;
inherit commands I_COMMAND;

static void create() {
    room::create();
    commands::create();
}
```

This is a warning-level safety heuristic, not an absolute rule. In a diamond,
both direct parents may call the same shared grand-base initializer; DGD
deduplicates the inherited program, not explicit calls, so chaining both can
run that initializer twice. Use a targeted `lifecycle-chain` suppression with
a nearby rationale when omitting a call is intentional. The rule does not try
to detect diamonds or enforce call order.

### Machine-readable output and embedding

`--format json` emits a versioned document: each diagnostic has its rule
name and code, severity, line/column, byte range, related locations, and
any fix as text edits; the summary includes the "not verified" counters.
`--format sarif` is SARIF 2.1.0 for code-scanning upload; `--format
github` writes GitHub Actions annotations. dgdlint never modifies files.

The linter is a Go library (`pkg/lint`): a `Linter` takes a project and a
file system (`pkg/vfs`, with an overlay for unsaved editor buffers), can
be told which files changed, honours `context` cancellation, and returns
diagnostics as data. `cmd/dgdlint` is a thin frontend over it. See
`docs/design/dgdlint-rewrite.md`.

### Migrating from the previous dgdlint

An existing `dgdtools.yml` loads unchanged. Differences to know about:

- A `rules:` entry with a `severity` now turns the rule on (it used to
  only tune it). Remove the severity, or say `off`, to keep it disabled.
- Object paths without a leading slash (`"obj/thing"`) resolve from the
  lib root, as the driver does, not from the referencing file's
  directory. Set `relative_object_paths: file` for the old behaviour.
- Add `driver_config:` (or `lint.typechecking:`) to get the
  typechecking-dependent rules, which are silent otherwise.
- `-f json` output is now an object with a `diagnostics` array (it was a
  bare array), and has more fields per diagnostic.
- Many more findings: the `E` rules are new. Use `--write-baseline`.
- Tier numbers are gone; `dgdlint rules` shows `file` or `lib` scope.
- Unknown keys in `dgdtools.yml` are now errors (for all three tools), so
  a misspelled setting is reported instead of ignored.
- `exclude: ["attic/**"]` only ever matched a top-level `attic`; write
  `"**/attic/**"` to skip attics anywhere.

## License note

The DGD dialect facts implemented here (keywords, operators, literal and
escape forms, preprocessor and call semantics) were taken as facts from
DGD's grammar, documentation, and observable behavior. No DGD source code
(AGPLv3) has been copied or translated into this repository.
