# dgdtools

Source tooling for the [DGD](https://github.com/dworkin/dgd) dialect of
LPC, in one command:

- **`dgdtools lint`** finds what the driver will reject at load time
  (syntax errors, constructs DGD does not accept, missing includes) and
  what it will not notice at all (calls to functions and objects that do
  not exist).
- **`dgdtools format`** is a code formatter in the gofmt tradition: one
  style, no line wrapping, and a proof for every file that only
  whitespace changed.

Existing LPC tooling targets LDMud or FluffOS and mishandles DGD syntax:
`({ })` / `([ ])` literals, the `<-` operator, `..` ranges, labeled
inherits, K&R-style typeless functions. These tools implement the DGD 1.7
grammar, with driver build variants (`SLASHSLASH` line comments,
`CLOSURES`) available as configuration.

```
dgdtools lint [path ...]        lint files and directories (default: the project)
dgdtools format path ... | -    format files in place; --check and --diff only report
dgdtools rules                  list the lint rules
dgdtools explain RULE           what a rule means and why it fires
dgdtools config [path]          check the configuration of a path and show what it comes to
dgdtools profile build | show   mudlib profiles
dgdtools version
```

Every command exits 0 on success, 1 when what it looks for was found
(lint findings at or above `fail_on`; files `format --check` would
change), and 2 on a usage, configuration or I/O error. `dgdtools
<command> -h` lists a command's flags; flags may stand before or after
paths.

## Installation

With Go 1.25 or later:

```
go install github.com/mmcdole/dgdtools/cmd/dgdtools@latest
```

Or from a clone:

```
git clone https://github.com/mmcdole/dgdtools
cd dgdtools
make build          # bin/dgdtools
make test
```

`make corpus LPC_CORPUS=/path/to/lib` runs the full-tree invariants
against a real mudlib: byte-exact lexer round-trip, comparator
self-compare, and the formatter gate on every file.

## Configuration

One file, `dgdtools.yml` (or `.dgdtools.yml`), found by searching upward
from each path the tools are given, so they work from anywhere on disk.
Every key is optional and the tools work with no config file at all. A
natural setup is one file next to the mudlib:

```
mud/
├── dgdtools.yml
├── etc/mud.dgd     ← the driver's own config file
└── lib/            ← root: lib
```

```yaml
# What the project is.
root: lib                    # filesystem directory that is the lib's "/"
exclude:                     # glob patterns, ** crosses directories
  - "**/attic/**"            # retired code kept for reference
  - "**/old/**"
  - "**/*.pre*"              # parked file copies
  - "**/var/**"              # generated runtime data
dialect:
  slash_slash: true          # // comments (driver SLASHSLASH build flag)
  closures: false            # `function` keyword (driver CLOSURES flag)
conventions:                 # the lib's own habits; none is built in
  # The lib's functions that take the name of a function to call later,
  # and which argument (0-based) is what. The driver's call_other and
  # call_out are built in.
  call_registry:
    add_action: { function: 0 }            # add_action("do_pull", "pull")
    bind: [{ object: 0, function: 1 }, { function: 0 }]   # bind(ob, "fn"), bind("fn")
profile_build:               # what `dgdtools profile build` captures
  name: mymud
  driver_config: etc/mud.dgd # include_dirs, include_file, auto_object, create
                             # and typechecking are read from the driver's config
  share: ["std/**", "obj/**", "secure/**"]   # code every wizard builds on
  checkouts: ["players/*", "d/*"]            # where wizards' own trees live

# How files are formatted and linted.
format:
  indent_style: space        # space | tab
  indent_width: 4
  line_endings: preserve     # preserve | lf | crlf
lint:
  rules:                     # name, code, or group -> off|info|warning|error
    unreachable-code: warning
    raw-inherit-path: { severity: warning, deny: ["/std/", "/obj/"] }
  overrides:
    - paths: ["players/**"]
      rules: { runtime: off }
  fail_on: error             # exit 1 at this severity or above
```

`dgdtools.example.yml` documents every key, and
`examples/viking/dgdtools.yml` is the configuration of a real mudlib.
Unknown keys and values are errors: a misspelled setting is reported, not ignored. `extends:
other.yml` starts from another file (scalars and lists replace, maps
merge).

The top-level keys say what the project **is**; they are the facts a
mudlib profile also carries, under the same names, and what the file
sets wins over the profile. `format:` and `lint:` are **settings**, and
only settings can differ from one directory to the next.

### Project keys

| Key | Purpose |
|---|---|
| `root` | The directory that is `/` to the driver; relative to the config file, which is the default. |
| `profile` | The mudlib profile to work against: a name, found as `NAME.profile` next to the config file, in a directory listed in `$DGDTOOLS_PROFILE_PATH`, or in the user config directory (`~/.config/dgdtools/profiles/`); or a path to a profile file. No profile is built in. It supplies the driver settings, the shared headers and the signatures of shared objects, so a wizard needs nothing else. Files on disk always win over the profile. |
| `lib_path` | The lib path of the directory a partial checkout is (`/players/ann`), when its path on disk does not show it (the profile lists the lib's checkout patterns, such as `players/*`). |
| `exclude` | Globs, relative to the lib root, that directory walks skip. A file named on the command line is always taken. |
| `dialect` | `slash_slash`, `closures`, and `typechecking` (the driver's level; the rules that depend on it are silent while it is unknown). |
| `include_dirs`, `include_file`, `auto_objects`, `create` | What the driver's config says: the include search path, the header included into every file, the object every object inherits, and the function the driver calls in a new object. Usually read from `profile_build.driver_config` or the profile; set here they override both. |
| `relative_object_paths` | What `"std/room"` (no leading slash) is relative to. DGD hands every `inherit` and every `call_other("path", …)` to the lib's driver object to resolve, so this is the lib's choice: `root` (the default, and what the driver's own functions such as `compile_object` do) or `file` (the directory of the file that says it, as in the kernel library). |
| `conventions` | The lib's own habits; nothing here has a default. `specifier_macros: [public]` names empty macros that stand where a specifier does, and `macros: {FOREACH: statement}` says how to read a macro that cannot be classified from its `#define` (`expression`, `empty`, `specifier`, `type`, `string`, `statement`, `opaque`); `defines:` lists names to treat as defined in `#ifdef`. `call_registry`: the lib's functions that take the name of a function to call later, each with the argument that names the `function`, optionally the argument that says in which `object` (without it: `this_object()`) and the first of the `args` passed on; a function called in several ways lists its forms. `object_registry`: functions whose string argument is an object path (`compile_object`, `find_object`, `clone_object` are built in). `format_registry`: scanf-style functions and the position of their format string (`sscanf` is built in). `autosave_markers`: names whose mention marks an object as one that saves itself with `save_object`. `virtual_paths`: lib-path globs served by virtual-object daemons. |
| `profile_build` | For whoever has the whole lib: `driver_config` (the DGD driver config), `share` (globs of shared code), `omit` (never put in a profile), `checkouts` (per-wizard tree patterns), `name`. Used by `dgdtools profile build`, and directly when linting the whole lib. |

### Settings

| Key | Purpose |
|---|---|
| `format.indent_style` | `space` (default) or `tab`. |
| `format.indent_width` | Spaces per level, 1 to 16 (default 4). |
| `format.line_endings` | `preserve` (default: every line break keeps its bytes), `lf` or `crlf`. |
| `lint.rules` | Maps a rule name, code, or group (`all`, `syntax`, `runtime`, `suspicious`, `convention`) to `off`, `info`, `warning` or `error`, or to a mapping: `severity`, `max_per_file` (report at most that many findings of a rule in one file; the rest are counted), and, for one rule, its own options (`dgdtools explain RULE` lists them). Giving a severity turns the rule on. An option the rule does not have is an error, and so is setting one rule twice, by name and by code. |
| `lint.overrides` | `[{paths: [globs], rules: {…}}]`: different rule settings for parts of the lib. |
| `lint.baseline` | Baseline file name (default `dgdtools-baseline.json` next to the config). Root config only. |
| `lint.fail_on` | Exit-1 threshold: `info`, `warning` or `error` (default). Root config only. |

### Settings per directory

A `dgdtools.yml` further down the tree that says `root: false` is a
**settings scope**. It adjusts `format:` and `lint:` for the files under
its directory, belongs to the project above it, and may hold nothing
else (a project key in it is an error that names the key):

```yaml
# players/drake/dgdtools.yml
root: false
format: { indent_style: tab }
lint:
  rules: { missing-visibility: warning }
  overrides:
    - paths: ["attic/**"]          # relative to this directory
      rules: { all: off }
```

Each file is formatted and linted by its own settings. For one key, the
first of these that sets it wins:

1. a command-line flag;
2. the nearest settings scope above the file, then the ones above that;
3. the project's `dgdtools.yml`;
4. for the three `format` keys, the file's `.editorconfig`
   (`indent_style`, `indent_size`, `end_of_line`);
5. the default.

Rule settings add up in the other direction: the project's rules, its
overrides, then each scope's from the outermost in, so the nearest file
has the last word.

`dgdtools config path/to/file.c` shows the result for a file and where
each value comes from. It is also how a configuration is checked:
`dgdtools config` in a project verifies its `dgdtools.yml` and every
nested one below, and exits 2 with the file, the line and what is wrong
when a key, a value, a rule name or a rule's option is not one the tools
know.

Without any project config, a `dgdtools.yml` in the user configuration
directory (`~/.config/dgdtools/dgdtools.yml`) is used: the place for
`profile: viking` when you work on bare checkouts. It is a fallback
only; a project's configuration is never mixed with it.

`--config FILE` makes that file the configuration of every path and
looks for no other `dgdtools.yml`; `--no-config` reads no configuration
file of any kind.

## dgdtools format

```
dgdtools format dir/ file.c         # rewrite in place
dgdtools format --check dir/        # list files that would change; exit 1 if any
dgdtools format --diff file.c       # show what would change as a unified diff
dgdtools format -                   # standard input to standard output
dgdtools format --stdin-filename players/x/room.c    # ... by the settings of that path
dgdtools format --lines 120-180 file.c               # only these lines
dgdtools format --changed dir/      # only the lines that differ from git HEAD
```

A path is required (`.` is the working directory). A directory stands
for the `.c` and `.h` files under it, minus the `exclude` globs of its
project. A file is formatted from its own text and its settings alone;
no other file is read, so the result is the same in an editor buffer, in
a wizard's checkout and in the whole lib.

There is one style and three settings: indentation with spaces or tabs,
its width, and line endings (`format:` above; `--indent-style`,
`--indent-width` and `--line-endings` on the command line). Everything
else is decided:

- Function definitions in KNF: specifiers and return type on one line,
  the name at column 0 (so `grep '^name('` finds a definition), braces
  on their own lines.
- Definitions written entirely on one line stay on one line
  (`string query_code() { return code; }`).
- Control-flow braces are cuddled (`if (x) {`, `} else {`) when the
  source uses Allman layout; one-line brace blocks keep their own lines.
- Statements are indented by their nesting, with or without braces: the
  body of a braceless `if`, `else`, `for`, `while` or `do` is one level
  in. A line that continues a statement is one level in from it.
- `case` labels at the `switch` level, bodies one deeper.
- Horizontal spacing is normalized (`a+b` becomes `a + b`, `f( a,b )`
  becomes `f(a, b)`); the author's line breaks are preserved and long
  lines are never re-wrapped.
- Every branch of an `#if` is formatted, by the same rules, as if it were
  the one compiled.
- Code that does not parse keeps its line breaks and is indented by its
  braces; the code around it is formatted as usual.
- Comments are never reflowed. Multi-line block comments shift as a unit,
  and function headers containing comments keep their original layout.
- Trailing whitespace is removed, at most two blank lines in a row are
  kept, files end with exactly one newline.

### Leaving code alone

```c
/* dgdtools-ignore-start format */
mapping exits = ([ "north" : "/room/hall",
                   "south" : "/room/yard",   ]);
/* dgdtools-ignore-end format */
```

Every byte between the two comments stays as written; without the
second, to the end of the file. Either comment form works, and a reason
may follow a colon (`/* dgdtools-ignore-start format: a table */`).
[Comments for the tools](#comments-for-the-tools) has the whole
grammar.

### Formatting part of a file

`--lines A-B` (repeatable, one file) formats only those lines.
`--changed` asks git which lines of each file differ from `HEAD` (or
from `--base REV`) and formats only those; a file git does not track yet
is formatted whole, and a file without changes is not read. Both combine
with `--check` and `--diff`, so CI can require that the lines a branch
touched are formatted without requiring it of a whole legacy file.

Everything outside the chosen lines stays byte for byte as it was. The
chosen lines are indented like the lines around them: in a file indented
with tabs or two spaces, a reformatted line follows its neighbours
rather than the configured width.

### Safety

The formatter is built to be provably behavior-preserving. The lexer is
byte-lossless (whitespace and comments are tokens; nothing is ever
transcoded), and the formatter only rewrites the whitespace between
tokens. Before any output is accepted it is re-lexed, required to be
token-identical to the input, and required to be idempotent; a file that
fails the gate, or does not fully tokenize, is left untouched and
reported (exit 2).

## dgdtools lint

### Quickstart for wizards: download your folder, run the linter

You do not need the whole mud. Download your own directory keeping its
place in the lib (`…/players/yourname/` or `…/d/YourDomain/`), put a
one-line `dgdtools.yml` in it, and lint there:

```
$ cat players/yourname/dgdtools.yml
profile: viking
$ cd players/yourname && dgdtools lint
room/hall.c:14:21: error: missing ';' before 'add_exit' [syntax-error]
obj/key.c:9:19: error: function 'do_unlok' is not defined in /players/yourname/obj/key (this_object) ... [callable-not-found]
```

`profile: viking` names a **mudlib profile**, a small file your mud
publishes (VikingMUD's is `viking.profile`). Get it once and put it where
profiles are looked for by name, in this order:

1. next to the `dgdtools.yml` that names it;
2. in any directory listed in `$DGDTOOLS_PROFILE_PATH`;
3. in your user config directory: `~/.config/dgdtools/profiles/`
   (on macOS also `~/Library/Application Support/dgdtools/profiles/`,
   on Windows `%AppData%\dgdtools\profiles\`).

`profile: path/to/viking.profile` works too, and `dgdtools profile show`
prints the directories searched. A profile holds the
lib's driver settings, its shared headers (so `#include <mudlib.h>` and
macros like `I_ROOM` resolve) and the names and argument counts of the
functions in the shared objects you inherit and call. With it the linter
checks your code the way it would inside the full lib. Anything that is
neither in your folder nor in the profile (another wizard's files, a
domain you did not download) is *unknown*: nothing is said about it
rather than guess. If your folder is not under a `players/<name>` or
`d/<Domain>` path, add `lib_path: /players/yourname`. `dgdtools lint
--profile viking file.c` works without any config file, and so does
`profile: viking` in `~/.config/dgdtools/dgdtools.yml`, the user-level
configuration that applies wherever no project has its own.

No profile is ever assumed: without `profile:` (or `--profile`) only
the file-local rules run. A profile makes claims about a specific
mud, and the wrong one would produce confident nonsense.

**For lib maintainers:** `dgdtools profile build` (with the whole lib and a
`profile_build:` section, above) writes `dgdtools.profile`: about 47 KB for
VikingMUD's 465 shared files. It contains each shared file's public
interface, like a C header or a `.d.ts`: `#define`s and `#include`s of
shared headers, inherit statements, and every non-private function and
global variable declaration exactly as written (types, specifiers,
parameter names) with bodies replaced by `{}`. No function bodies, no
comments, no private functions or variables, no object-local macros the
declarations do not need, nothing matching `omit:`. `dgdtools profile
show viking` summarizes a profile and `dgdtools profile show viking
/std/room.c` prints one stub, so you can see exactly what you would be
publishing. Publish the file (renamed `<mud>.profile`) wherever your
wizards can fetch it; dgdtools itself ships no profiles.

### Usage

```
dgdtools lint                          # the project found from the working directory
dgdtools lint dir/ file.c ...          # these; the project is found from the paths
dgdtools lint --output-format json ... # also: sarif, github
dgdtools lint --select syntax ...      # run only these rules (names, codes or groups)
dgdtools lint --enable RULES --ignore RULES ...   # adjust the configured set
dgdtools lint --write-baseline         # accept current findings; later runs report only new ones
dgdtools lint --stats ...              # per-rule counts and what could not be verified
dgdtools rules                         # list rules: code, name, default, scope
dgdtools explain RULE                  # what a rule means and why it fires
dgdtools config [path]                 # the resolved project, and the rules that run on a file
```

Exit codes: `0` no findings at or above `fail_on`, `1` findings, `2`
usage or configuration error, or an internal error of the linter on
some file (which is reported, with the file). A file that cannot be read
is a warning: the rest of the run stands.

The linter parses every file with its own error-tolerant parser for the DGD
1.7 grammar, on source as written: macros are classified from their
`#define`s (type, specifier, statement, expression) rather than expanded.
Its rule is **prove it or stay silent**: a finding is reported only when
it can be proven from what the linter can see. An unresolved `#include`, an
inherit path it cannot evaluate, a receiver it cannot identify, code in an
`#ifdef` branch it cannot decide, a statement that uses a macro it cannot
see through: each makes the affected facts *unknown*, and nothing is
concluded from an unknown. `--stats` shows how much was unknown.

**What it needs.** Nothing, for the file-local rules: `dgdtools lint castle.c`
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
| R006 | `lifecycle-chain` | warning | lifecycle function (the driver's create function) omits calls to inherited implementations |
| S001 | `assignment-in-condition` | warning | condition assigns a bare constant or an unparenthesized `&&`/`||` result |
| S002 | `no-effect-statement` | warning | comparison used as a statement (`x == 1;`) |
| S003 | `sscanf-format` | warning | more variables than `%`-conversions |
| S004 | `unreachable-code` | off | statement directly after `return`/`break`/`continue`/`goto` |
| S005 | `invalid-suppression` | warning | a `dgdtools-ignore` comment that cannot be followed (unknown rule, nothing named, an end without a start) |
| C001 | `raw-inherit-path` | off | inherit using a literal path where the lib mandates macros |
| C002 | `missing-visibility` | off | function with no visibility specifier |
| C003 | `unformatted` | off | file that `dgdtools format` would change |
| C004 | `static-autosave-var` | off | `static` global in an object that saves itself (`conventions.autosave_markers`) |
| C005 | `unresolved-inherit` | off | inherit path the macro evaluator could not resolve (info) |
| C006 | `unused-suppression` | off | a `dgdtools-ignore` comment for a finding that is not there |

The `E` rules are things the DGD compiler rejects: an object with one does
not load. They were checked against DGD 1.7.9 compiling a 108,000-file
lib: of 30,643 files flagged by an `E` rule, none compiles. The `R` rules
are code that compiles and then fails or silently returns nil. Rule names
and codes are stable.

### Adopting it on an old lib

A lib that predates DGD will have tens of thousands of findings in code
nobody loads. Record them once and gate only on new ones:

```
dgdtools lint --write-baseline   # writes dgdtools-baseline.json next to dgdtools.yml
dgdtools lint                    # from now on: exit 1 only for findings not in the baseline
```

Baseline entries are matched by file, rule and the text of the offending
line, not by line number, so editing elsewhere in a file does not bring
old findings back. Rules can also be tuned per path with `overrides`,
and per directory with a settings scope (`root: false`, above).

### Suppressing a finding

By rule name or code, in a comment on the line of the finding or on the
line above it:

```c
x = "a" "b";                 /* dgdtools-ignore lint/adjacent-strings */

/* dgdtools-ignore lint/callable-not-found: the daemon defines it at boot */
"/sys/d"->late();

/* dgdtools-ignore-start lint/lifecycle-chain, lint/R001 */
...
/* dgdtools-ignore-end lint/lifecycle-chain, lint/R001 */
```

`lint` alone, without a rule, is every rule on that one line. A range
names its rules: there is no comment that turns everything off from
here on. A comment that cannot be followed (a misspelled rule, a range
without rules, an end without a start) is itself a finding,
`invalid-suppression`, on by default; `unused-suppression` (off by
default) reports the ones that are no longer needed.

### Comments for the tools

One prefix, `dgdtools-ignore`, addresses every part of the tool, and what
is to be ignored follows it:

```
dgdtools-ignore TARGETS [: reason]         one line of code
dgdtools-ignore-start TARGETS [: reason]   from here ...
dgdtools-ignore-end [TARGETS]              ... to here (no targets: everything)

TARGETS   lint/RULE, ...   a lint rule, by name or code
          lint             every lint rule (one line only)
          format           the formatter (ranges only)
```

A comment is a directive when it begins with the prefix; a comment that
merely mentions it is prose. The plain form is about the line of code it
stands on, or the next line when it stands alone. `grep -rn
dgdtools-ignore` finds every exemption in a lib.

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

The lifecycle function is the one the driver calls in a new object: the
driver config's `create` parameter (read from `profile_build.driver_config`
or the profile, or set as the project key `create`), and `create` when that
is not known. A lib whose auto object receives the driver's call and passes
it on under another name, or that has more such functions, lists them:
`lifecycle-chain: { names: [create, reset] }`.

The rule is proof-based: files with no explicit inherit, parents without the
lifecycle function, and unresolved parents with no known definition are
skipped. With multiple defining parents, label and chain each one:

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

`--output-format json` emits a versioned document: each diagnostic has
its rule name and code, severity, line/column, byte range, related
locations, and any fix as text edits; the summary includes the "not
verified" counters. `--output-format sarif` is SARIF 2.1.0 for
code-scanning upload; `--output-format github` writes GitHub Actions
annotations. The linter never modifies files.

Everything is a Go library, and `cmd/dgdtools` is a thin front end over
it. `pkg/lint`: a `Linter` takes a project and a file system (`pkg/vfs`,
with an overlay for unsaved editor buffers), can be told which files
changed, honours `context` cancellation, and returns diagnostics as
data. `pkg/format`: `Format` and `Lines` (a range of lines) take a lexed
file and return text, `Edits` the minimal replacements an editor
applies. `pkg/config`: `Project.Settings(path)` is the per-file
settings resolution described above. See `docs/design/`:
`dgdlint-rewrite.md` (the linter and the project model), `dgdfmt.md`
(the formatter), `config.md` (configuration and the command line) and
`lsp.md` (the language server that is planned on top).

## License note

The DGD dialect facts implemented here (keywords, operators, literal and
escape forms, preprocessor and call semantics) were taken as facts from
DGD's grammar, documentation, and observable behavior. No DGD source code
(AGPLv3) has been copied or translated into this repository.
