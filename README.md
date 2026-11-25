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
- **`dgdtools rename`** renames a function, a variable or a macro across
  the mudlib, with its redefinitions and every call that reaches it,
  and says which places may be the name and nobody can tell; and moves
  a file, with every `inherit`, `#include` and object path that names it.

Other LPC tooling targets LDMud or FluffOS and mishandles DGD syntax:
`({ })` / `([ ])` literals, the `<-` operator, `..` ranges, labeled
inherits, K&R-style typeless functions. dgdtools implements the DGD 1.7
grammar, with driver build variants (`SLASHSLASH` line comments,
`CLOSURES`) available as configuration.

```
dgdtools lint [path ...]        lint files and directories (default: the project)
dgdtools format path ... | -    format files in place; --check and --diff only report
dgdtools rules                  list the lint rules
dgdtools explain RULE           what a rule means and why it fires
dgdtools config [path]          check the configuration of a path and show what it comes to
dgdtools profile build | show   mudlib profiles
dgdtools rename SYMBOL NEW      rename across the mudlib; --write writes, else diffs
dgdtools rename FILE NEW        move a file and rewrite the paths that name it
dgdtools lsp                    the language server, for an editor to start
dgdtools version
```

Every command exits 0 on success, 1 when what it looks for was found
(lint findings at or above `fail_on`; files `format --check` would
change; possible places `rename` left), and 2 on a usage, configuration or I/O error. `dgdtools
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
root: lib                    # filesystem directory that is the mudlib's "/"
exclude:                     # glob patterns, ** crosses directories
  - "**/attic/**"            # retired code kept for reference
  - "**/old/**"
  - "**/*.pre*"              # parked file copies
  - "**/var/**"              # generated runtime data
dialect:
  slash_slash: true          # // comments (driver SLASHSLASH build flag)
  closures: false            # `function` keyword (driver CLOSURES flag)
conventions:                 # the mudlib's own habits; none is built in
  # The mudlib's functions that take the name of a function to call later,
  # and which argument (0-based) is what. The driver's call_other and
  # call_out are built in.
  call_registry:
    add_action: { function: 0 }            # add_action("do_pull", "pull")
    bind: [{ object: 0, function: 1 }, { function: 0 }]   # bind(ob, "fn"), bind("fn")
profile_build:               # what `dgdtools profile build` captures
  name: mymud
  driver_config: etc/mud.dgd # include_dirs, include_file, auto_object, create, driver_object
                             # and typechecking are read from the driver's config
  share: ["kernel/**", "lib/**"]             # code every wizard builds on
  checkouts: ["usr/*"]                       # where wizards' own trees live

# How files are formatted and linted.
format:
  indent_style: space        # space | tab
  indent_width: 4
  line_endings: preserve     # preserve | lf | crlf
lint:
  rules:                     # name, code, or group -> off|info|warning|error
    unreachable-code: warning
    raw-inherit-path: { severity: warning, deny: ["/lib/"] }
  overrides:
    - paths: ["usr/**"]
      rules: { runtime: off }
  fail_on: error             # exit 1 at this severity or above
```

`dgdtools.example.yml` documents every key, and
`examples/viking/dgdtools.yml` is the configuration of a real mudlib.
Unknown keys and values are errors: a misspelled setting is reported,
not ignored. `extends: other.yml` starts from another file (scalars and
lists replace, maps merge).

The top-level keys say what the project **is**; they are the facts a
mudlib profile also carries, under the same names, and what the file
sets wins over the profile. `format:` and `lint:` are **settings**, and
only settings can differ from one directory to the next.

### Project keys

| Key | Purpose |
|---|---|
| `root` | The directory that is `/` to the driver, relative to the config file (`root: .` for its own directory). It is never guessed: a config that does not say where the mudlib is (`root`, `profile` or `lib_path`) gets the file-local rules only, and says so. With `profile` or `lib_path` and no `root`, the config's directory is the checkout. |
| `profile` | The mudlib profile to work against: a name, found as `NAME.profile` next to the config file, in a directory listed in `$DGDTOOLS_PROFILE_PATH`, or in the user config directory (`~/.config/dgdtools/profiles/`); or a path to a profile file. No profile is built in. It supplies the driver settings, the shared headers and the signatures of shared objects, so a wizard needs nothing else. Files on disk always win over the profile. |
| `lib_path` | The mudlib path of the directory a partial checkout is (`/usr/ann`), when its path on disk does not show it (the profile lists the mudlib's checkout patterns, such as `usr/*`). |
| `exclude` | Globs, relative to the mudlib root, that directory walks skip. A file named on the command line is always taken. |
| `dialect` | `slash_slash`, `closures`, `typechecking` (the driver's level; the rules that depend on it are silent while it is unknown), and `kfuns`: the kernel functions the driver has beyond stock DGD's, those of the extension modules it loads, each as a prototype (`"int *rusage()"`, `"string sha256(string text, varargs int rounds)"`). The editor shows them like the driver's own, which are built in. |
| `include_dirs`, `include_file`, `auto_objects`, `create`, `driver_object` | What the driver's config says: the include search path, the header included into every file, the object every object inherits, the function a new object is initialized by, and the driver object (whose functions the driver calls by name, which a rename refuses). An include directory without a leading `/` is relative to each including file, as DGD resolves it by default; one that starts with `~` (the kernel library's `~/include`) is the lib's driver object's to place, so an include that may be there is unknown rather than missing. Usually read from `profile_build.driver_config` or the profile; set here they override both (a mudlib whose auto object receives the driver's `create` call and passes it on as another function names that function). |
| `relative_object_paths` | What `"std/room"` (no leading slash) is relative to. DGD hands every `inherit` and every `call_other("path", …)` to the mudlib's driver object to resolve, so this is the mudlib's choice: `root` (the default, and what the driver's own functions such as `compile_object` do) or `file` (the directory of the file that says it, as in the kernel library). |
| `conventions` | The mudlib's own habits; nothing here has a default. `specifier_macros: [public]` names empty macros that stand where a specifier does, and `macros: {FOREACH: statement}` says how to read a macro that cannot be classified from its `#define` (`expression`, `empty`, `specifier`, `type`, `string`, `statement`, `opaque`); `defines:` lists names to treat as defined in `#ifdef`. `call_registry`: the mudlib's functions that take the name of a function to call later, each with the argument that names the `function`, optionally the argument that says in which `object` (without it: `this_object()`) and the first of the `args` passed on; a function called in several ways lists its forms (`call_other` and `call_out` are built in). `object_registry`: functions whose string argument is an object path (`compile_object`, `find_object`, `clone_object` are built in). `format_registry`: scanf-style functions and the position of their format string (`sscanf` is built in). `autosave_markers`: names whose mention marks an object as one that saves itself with `save_object`. `virtual_paths`: lib-path globs served by virtual-object daemons. |
| `profile_build` | For whoever has the whole mudlib: `driver_config` (the DGD driver config), `share` (globs of shared code), `omit` (never put in a profile), `checkouts` (per-wizard tree patterns), `name`. Used by `dgdtools profile build`, and directly when linting the whole mudlib. |

### Settings

| Key | Purpose |
|---|---|
| `format.indent_style` | `space` (default) or `tab`. |
| `format.indent_width` | Spaces per level, 1 to 16 (default 4). |
| `format.line_endings` | `preserve` (default: every line break keeps its bytes), `lf` or `crlf`. |
| `lint.rules` | Maps a rule name, code, or group (`all`, `syntax`, `runtime`, `suspicious`, `convention`) to `off`, `info`, `warning` or `error`, or to a mapping: `severity`, `max_per_file` (report at most that many findings of a rule in one file; the rest are counted), and, for one rule, its own options (`dgdtools explain RULE` lists them). Giving a severity turns the rule on. An option the rule does not have is an error, and so is setting one rule twice, by name and by code. |
| `lint.overrides` | `[{paths: [globs], rules: {…}}]`: different rule settings for parts of the mudlib. |
| `lint.baseline` | Baseline file name (default `dgdtools-baseline.json` next to the config). Root config only. |
| `lint.fail_on` | Exit-1 threshold: `info`, `warning` or `error` (default). Root config only. |

### Settings per directory

A `dgdtools.yml` further down the tree that holds nothing but `format:`
and `lint:` settings, or says `root: false`, is a **settings scope**. It
adjusts `format:` and `lint:` for the files under its directory, belongs
to the project above it, and may hold nothing else (a project key in a
file that says `root: false` is an error that names the key):

```yaml
# usr/ann/dgdtools.yml
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
settings scope below, and exits 2 with the file, the line and what is
wrong when a key, a value, a rule name or a rule's option is not one the
tools know.

A `dgdtools.yml` below a project that has a project key is a project of
its own (a wizard's folder with `profile: NAME` in it, inside the whole
mudlib). Its files are formatted by its own settings, and linted
by naming that folder; a lint of the project above passes over them and
says so.

Without any project config, a `dgdtools.yml` in the user configuration
directory (`~/.config/dgdtools/dgdtools.yml`) is used: the place for
`profile: NAME` when you work on bare checkouts. It is a fallback
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
dgdtools format --stdin-filename usr/ann/room.c      # ... by the settings of that path
dgdtools format --lines 120-180 file.c               # only these lines
dgdtools format --changed dir/      # only the lines that differ from git HEAD
```

A path is required (`.` is the working directory). A directory stands
for the `.c` and `.h` files under it, minus the `exclude` globs of its
project. A file is formatted from its own text and its settings alone;
no other file is read, so the result is the same in an editor buffer, in
a wizard's checkout and in the whole mudlib.

There is one style and three settings: indentation with spaces or tabs,
its width, and line endings (`format:` above; `--indent-style`,
`--indent-width` and `--line-endings` on the command line). Everything
else is decided:

- Function headers keep their lines: a return type on a line of its
  own stays there, `int f() {` stays on one line, and a definition
  written on one line stays one line (`string query_code() { return code; }`);
  only the spacing in them is normalized.
- Braces and `else` stay on the line they are on: `if (x) {` and
  `if (x)` with `{` on the next line both come out as written, only
  indented. The formatter never joins or splits a line, except to drop
  blank lines beyond two in a row.
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

You do not need the whole mud. Download your own directory, put a
one-line `dgdtools.yml` in it that names your mud's profile, and lint
there. On a mudlib that keeps its wizards under `/usr`, for a wizard called
ann:

```
$ cat usr/ann/dgdtools.yml
profile: mymud
$ cd usr/ann && dgdtools lint
room/hall.c:14:21: error: missing ';' before 'add_exit' [E001 syntax-error]
obj/key.c:9:19: error: function 'do_unlok' is not defined in /usr/ann/obj/key (this_object) ... [R001 callable-not-found]
```

`profile: mymud` names a **mudlib profile**, a small file your mud
publishes as `mymud.profile`. Get it once and put it where profiles are
looked for by name, in this order:

1. next to the `dgdtools.yml` that names it;
2. in any directory listed in `$DGDTOOLS_PROFILE_PATH`;
3. in your user config directory: `~/.config/dgdtools/profiles/`
   (on macOS also `~/Library/Application Support/dgdtools/profiles/`,
   on Windows `%AppData%\dgdtools\profiles\`).

`profile: path/to/mymud.profile` works too, and `dgdtools profile show`
prints the directories searched. A profile holds the mudlib's driver
settings, its shared headers (so `#include <std.h>` and the macros in it
resolve) and the names and argument counts of the functions in the
shared objects you inherit and call. With it the linter checks your code
the way it would inside the full mudlib. Anything that is neither in your
folder nor in the profile (another wizard's files, a part of the mudlib you
did not download) is *unknown*: nothing is said about it rather than
guess.

The linter has to know where in the mudlib your folder belongs. The profile
lists the places the mudlib keeps such folders (here `usr/*`), so a folder
downloaded with its place in the path (`…/usr/ann/`) is recognized;
anywhere else, add `lib_path: /usr/ann`. `dgdtools lint --profile mymud
file.c` works without any config file (with `--lib-path /usr/ann`, run
from the folder: the mudlib path is the working directory's), and so does `profile: mymud` in
`~/.config/dgdtools/dgdtools.yml`, the user-level configuration that
applies wherever no project has its own.

No profile is ever assumed: without `profile:` (or `--profile`) only
the file-local rules run. A profile makes claims about a specific
mud, and the wrong one would produce confident nonsense.

VikingMUD is one mud set up this way: its wizards' trees are
`players/<name>` and `d/<Domain>`, a wizard's file says `profile:
viking`, and `examples/viking/dgdtools.yml` is the configuration its
profile is built from.

**For mudlib maintainers:** `dgdtools profile build` (with the whole mudlib and a
`profile_build:` section, above) writes `dgdtools.profile`: 114 KB for
VikingMUD's 723 shared files. It contains each shared file's public
interface, like a C header or a `.d.ts`: `#define`s and `#include`s of
shared headers, inherit statements, and every non-private function and
global variable declaration exactly as written (types, specifiers,
parameter names) with bodies replaced by `{}`. No function bodies, no
comments, no private functions or variables, no object-local macros the
declarations do not need, nothing matching `omit:`. Which files are
shared is found by use (referenced from at least `share_min_owners`
different owners) on top of the `share:` globs. `dgdtools profile show
mymud` summarizes a profile and `dgdtools profile show mymud
/std/room.c` prints one stub, so you can see exactly what you would be
publishing. Publish the file (renamed `<mud>.profile`) wherever your
wizards can fetch it; dgdtools itself ships no profiles.

### Usage

```
dgdtools lint                          # the project found from the working directory
dgdtools lint dir/ file.c ...          # these; the project is found from the paths
dgdtools lint --output-format json ... # also: sarif, github
dgdtools lint --stdin-filename obj/key.c   # standard input as that file: an unsaved buffer
dgdtools lint -                        # standard input, nameless: file-local rules
dgdtools lint --select syntax ...      # run only these rules (names, codes or groups)
dgdtools lint --enable RULES --ignore RULES ...   # adjust the configured set
dgdtools lint --write-baseline         # accept current findings; later runs report only new ones
dgdtools lint --stats ...              # per-rule counts and what could not be verified
dgdtools rules                         # list rules: code, name, default, scope
dgdtools explain RULE                  # what a rule means and why it fires
dgdtools config [path]                 # the resolved project, and the rules that run on a file
```

A directory stands for the `.c` files under it, each with the headers
it includes; a header named by itself is linted on its own.

Exit codes: `0` no findings at or above `fail_on`, `1` findings, `2`
usage or configuration error, or an internal error of the linter on
some file (which is reported, with the file), `130` interrupted. A file
that cannot be read is a warning: the rest of the run stands.

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
Cross-file rules need the mudlib root, which comes from the `root:` of
the nearest `dgdtools.yml` above the linted path (or `--root`); a root is
never guessed. A file outside the root gets the file-local rules only.

**The rules** are in four groups, and a rule's code says which:

| Group | Codes | What it finds |
|---|---|---|
| `syntax` | `E…` | what the DGD compiler rejects: an object with one does not load |
| `runtime` | `R…` | code that compiles and then fails or silently returns nil: calls to functions and objects that do not exist |
| `suspicious` | `S…` | legal constructs that are probably not what was meant |
| `convention` | `C…` | a mudlib's own policy; off until asked for |

[docs/rules.md](docs/rules.md) describes every rule: what it finds,
whether it is on by default, its options. It is written from the rule
table, as are `dgdtools rules` (the list) and `dgdtools explain RULE`
(one rule). Rule names and codes are stable.

The `E` rules were checked against DGD 1.7.9 compiling a 108,000-file
mudlib: of 30,747 files flagged by an `E` rule, none compiles.

### Adopting it on an old mudlib

A mudlib that predates DGD will have tens of thousands of findings in code
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
dgdtools-ignore` finds every exemption in a mudlib.

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

The lifecycle function is the project's `create` function: the driver
config's `create` parameter (read from `profile_build.driver_config` or
the profile), and `create` when nothing says. A mudlib whose auto object
receives the driver's call and passes it on under another name sets the
project key (`create: create`), so that its profile carries it; other
functions to hold to the same rule are the rule's option:
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

Findings are written one per line, compiler style, with the rule's code
and name at the end:

```
obj/key.c:9:19: error: function 'do_unlok' is not defined in /obj/key (this_object) (called via add_action) [R001 callable-not-found]
```

Paths are written from the working directory: relative for the files
under it, absolute for the rest. A declaration that comes from a profile
is located by the stub's URI (`dgdtools-profile://NAME/std/room.c`).

`--output-format json` emits a versioned document: each diagnostic has
its rule name and code, severity, line/column, byte range, a fingerprint
of its source line, tags (`unnecessary`, `deprecated`), related
locations, and any fix as text edits with its applicability (`safe` or
`unsafe`); `rules` describes the rules that occur, with the address of
their documentation; `errors` lists the files that were not linted
(`unreadable`, `internal`, or `stale` for one that changed while it was
read); the summary includes the "not verified"
counters. `--output-format sarif` is SARIF 2.1.0 for code-scanning
upload: locations are URIs (relative to `%SRCROOT%`, the working
directory, or `file:` for files outside it), rules carry `helpUri`,
results a partial fingerprint. `--output-format github` writes GitHub
Actions annotations. Columns count bytes. The linter never modifies
files.

Everything is a Go library, and `cmd/dgdtools` is a thin front end over
it. `pkg/lint`: a `Linter` takes a project and a file system (`pkg/vfs`),
can be told which files changed, honours `context` cancellation, and
returns diagnostics as data. `pkg/format`: `Source` formats a file's
text, or some of its lines, by that file's settings, and `Edits` gives
the minimal replacements an editor applies. `pkg/config`:
`Project.Settings(path)` is the per-file settings resolution described
above. `pkg/session`: what an editor server holds (unsaved buffers,
projects, a linter each), with every change applied in one step. See `docs/design/`:
[architecture.md](docs/design/architecture.md) (the library, the linter
and the project model), [formatter.md](docs/design/formatter.md) (the
formatter), [config.md](docs/design/config.md) (configuration and the
command line) and [lsp.md](docs/design/lsp.md) (the language server and
the rename command).

## dgdtools rename

Renames a function, a global variable or a macro across the whole mudlib,
the way a mud renames: in one sweep, the old name gone that day. What it
adds to grep and an editor macro is the judgment: every place the old
name stands is **certain** (the symbol), **possible** (it may be, and
nobody can tell) or **excluded** (provably something else).

```
dgdtools rename /std/living::query_name query_short_name       # the diffs, nothing written
dgdtools rename --write /std/living::query_name query_short_name
dgdtools rename --write --include-possible /std/living::query_name query_short_name
dgdtools rename /include/std.h::ROOM ROOM_OB                   # a header's macro
dgdtools rename obj/sword.c:42:9 weight                        # what is named there
dgdtools rename --output-format json /std/room::add_item add_detail
```

`SYMBOL` is one of:

| Form | What it names |
|---|---|
| `/std/living::query_name` | a function or a global variable of an object, by the object's mudlib path (`.c` may be written): its own, or one it inherits |
| `/include/std.h::ROOM` | a macro, by the mudlib path of the file that `#define`s it |
| `FILE:LINE:COL` | what is named at that place of a file on disk, from the working directory, line and column from 1 as `dgdtools lint` reports them: how a local, a parameter or an inherit label is named |

What is renamed:

- **A function** with its redefinitions in the objects that inherit it
  and in those it inherits: by the driver's rules they are one function
  (a call in the inherited program reaches the redefinition), so they are
  renamed together, as an IDE renames a virtual method with its
  overrides; and every call that reaches one of them: `fn()`, `::fn()`,
  `label::fn()`, `ob->fn()` and `call_other` on an object the source
  names (a path, a path macro, `this_object()`), and the callbacks of
  `call_out` and of the registrars in `conventions.call_registry`. The
  same name in an object outside the hierarchy is another function, and
  is left.
- **A global variable** in its object and the objects that inherit it.
- **A macro** in every file that includes its header.
- A local, a parameter, a private function or variable, an inherit
  label, a macro a file defines for itself: in its file.

Possible are a call on an object the tools cannot tell (`ob->fn()` with
`ob` a parameter), a callback in `this_object()` that only an inheritor
may have, the name in a macro's body, code in an `#if` branch that is not
compiled or that does not parse. They are left unless
`--include-possible`. A call whose function's name is made when it runs
(`call_other(ob, "do_" + verb)`) cannot be searched at all; the summary
lists those whose constant part the old name begins or ends with.

By default nothing is written: the edits are printed as unified diffs,
grouped by owner when the project has checkout patterns (a wizard's or a
domain's tree, by `profile_build`'s `checkouts` or the profile's; `other`
for the rest of the lib), and a summary goes to standard error: the
counts of each class, the files and edits of each owner, the possible
places, the warnings. `--write` writes the files, each replaced whole.
`--output-format json` prints the plan.

It refuses, with the reason, a new name that would change what another
name means (a function, variable or macro already visible where an edit
lands, a local that would hide the renamed variable, a kfun, a call that
would reach the renamed function), the function the driver creates
objects with (`create`) and the driver object's functions the driver
calls by name (`path_read`, `compile_error`, ...), whether renamed from
or to such a name, a symbol defined in a mudlib profile, and a
variable that `save_object` writes: renamed, the values saved under the
old name are not restored at the next reboot. `--force` renames that one
all the same. It warns when an edited file is inherited or included by
other objects (they keep the program they were compiled with, and must be
recompiled for the rename to take effect) and, in a wizard's
checkout with a profile, that the rest of the mudlib is not renamed.

With a file or a directory instead of a symbol, it moves it. The file is
a path on disk from the working directory when there is one, else a
mudlib path, and the new place is of the same kind (or a name, for the
same directory):

```
dgdtools rename /std/room.c /std/base_room.c       # the diffs, nothing written
dgdtools rename --write /std/room.c base_room.c    # a name: the same directory
dgdtools rename lib/std/room.c lib/std/place.c     # paths on disk
dgdtools rename /d/Ruins /d/Old                    # a directory, every file in it
```

Every `inherit`, `#include` and object path that names the file
(`clone_object("/std/room")`, `"/std/room"->fn()`) is rewritten as it
was written: from the mudlib root, relative to the file, from an include
directory; and so are the moved file's own paths that name a file from
where it was. Each new path is resolved as the driver will once the file
has moved. A path made by a macro (`inherit ROOM;`) is possible and not
changed, since the macro is every user's, and so is a string that is the
old path where nothing says it is one (`add_exit("/std/room", ...)`, a
`#define`): the summary lists those places, and `--include-possible`
rewrites the strings too. `--write` writes the files and moves the file
or directory last. The diffs are grouped by owner when the project has
checkout patterns (`profile_build.checkouts`, or the profile's). In an
editor, renaming or moving a file or a folder does the same.

Exit codes: `0` the rename is complete (no possible place left), `1`
possible places were left, `2` usage or configuration error or a refusal,
`130` interrupted. On the VikingMUD lib (110,086 files), renaming
`add_item`, called 141,153 times in 25,776 files, plans in under a
second after the index is built; the whole command takes about 5 s.

## In an editor

`dgdtools lsp` is a language server: an editor starts it and gets, for
the files that are open, the findings of `dgdtools lint` as they are
typed, the fixes and `dgdtools-ignore` comments for them, and `dgdtools
format` for a file or a selection. It also answers what a name is: go
to definition (the header of an `#include`, the object of an `inherit`,
the function of a call, a macro's `#define`, a variable or parameter), a
hover with the declaration, the comment above it and where it is, and
signature help in the arguments of a call, for the mudlib's functions and
for the driver's kfuns alike. It completes what can be written where
you type: the locals in scope, the object's variables and functions,
macros, kfuns and keywords; after `::` and `->` the functions that can
be called there; the paths of an `#include` and an `inherit`; the name
in a `call_out("...")`. It lists the outline of a file (inherits,
functions, variables, macros) and finds a function, a variable or a
header's macro anywhere in the mudlib by a few letters of its name. It
finds the references of a name: every call of a function in the mudlib
(direct, `::`, `->` and callbacks on objects it can tell), every use of
a variable, a macro or a local, every `inherit` of an object and
`#include` of a header, and highlights a name's places in the file, a
variable read or written. Renaming or moving a file or a folder in the
editor rewrites the `inherit` and `#include` lines and object paths that
name it. It offers to add the prototype of a function called before the
file defines it, and the path of the one file a missing `#include` or
`inherit` may have meant. It renames a name everywhere it is meant and
nowhere else, in the file and across the mudlib, as `dgdtools rename`
does: the edits to every file in one preview, the places that may be
the name (a call on an object it cannot tell) shown unticked, and why
not where the rename would change what a name means. It colours what a grammar cannot tell
(semantic tokens): which names are macros, and of which kind, which
functions are the object's own, inherited, the auto object's or the
driver's, which variables are global, local or parameters. Where
the tools cannot know (a call on an
object held in a variable) they say so, or nothing, and never guess. It
reads the same `dgdtools.yml` as the commands, so the editor and the
command line cannot disagree.
Each editor has its own page, under the language name `dgdlpc`:

- [VS Code](editors/vscode/README.md): the extension, with syntax
  highlighting and the server (the package for each platform has
  `dgdtools` inside).
- [Vim and Neovim](editors/vim/README.md): a plugin with syntax
  highlighting, indent and filetype settings; Neovim 0.11 and later
  start the server by themselves, Vim through vim-lsp, ALE or coc.nvim.
- [Helix](editors/helix/README.md): the server, with C's highlighting.

A `.c` file is LPC there only inside a mudlib: nothing claims the
extension on a machine that has C sources too.
[`editors/`](editors/README.md) has what they share: what the server
does, and how the grammars are tested.

## License note

The DGD dialect facts implemented here (keywords, operators, literal and
escape forms, preprocessor and call semantics) were taken as facts from
DGD's grammar, documentation, and observable behavior. No DGD source code
(AGPLv3) has been copied or translated into this repository.
