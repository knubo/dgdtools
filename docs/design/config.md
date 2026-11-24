# Configuration and command line: design

One binary, `dgdtools`; one configuration file, `dgdtools.yml`. This
document says how the file is laid out, how the settings of one source
file are found, and what the command looks like. The linter's rules and
the project model they need are in
[dgdlint-rewrite.md](dgdlint-rewrite.md); the formatter is in
[dgdfmt.md](dgdfmt.md).

## 1. What the others do

| Tool | File | Found | Nested files | Editor settings |
|---|---|---|---|---|
| ruff | `ruff.toml` / `pyproject.toml`, sections `lint` and `format` | nearest, walking up from each file | the nearest one is the whole configuration of the files under it (no merge unless it says `extend`) | reads none; `~/.config/ruff/ruff.toml` only when no project file is found |
| Biome | `biome.json`, sections `formatter`, `linter` | walking up from the working directory | a nested file must say `"root": false`; it then adjusts settings for its subtree and inherits the rest; a second root is an error | `.editorconfig` opt-in |
| Prettier | `.prettierrc` | nearest per file, no merge; `overrides` by glob inside one file | nearest wins whole | `.editorconfig` read for indent, width, end of line unless the Prettier file says otherwise |
| clang-format / clang-tidy | `.clang-format`, `.clang-tidy` | nearest per file | nearest wins whole, unless it says `BasedOnStyle: InheritParentConfig` (`InheritParentConfig: true`), which merges it over the next one up | none |
| rustfmt | `rustfmt.toml` | nearest per file | nearest wins whole | none |
| EditorConfig | `.editorconfig` | every file up to one with `root = true` | all apply; nearer and later sections win per property | it is the editor setting |

What to take from them:

- **One file for all tools of a language** (ruff, Biome, Deno): the
  linter and the formatter share what a project *is* (here: where the
  lib root is, the dialect, the profile), so two files would state it
  twice.
- **Per-file discovery** is what makes a tool work from any directory
  and in an editor.
- **Nested files are wanted, and whole-file replacement is wrong for a
  tool with a project model.** A wizard's directory legitimately wants
  different lint severities or its own indentation. It cannot
  legitimately have a different lib root: there is one `/`. Biome's
  split, a root file that owns the project and nested files that only
  adjust settings, is the one that fits; clang's `InheritParentConfig`
  is the same idea spelled as an opt-in.
- **`.editorconfig` is the lowest common denominator for indentation and
  line endings**, already in repositories and already obeyed by editors.
  Reading it means a project that has one needs no `format:` section.
- **A user-level file is a fallback, never an override** (ruff). A
  project's configuration is the same for everyone who works on it.

## 2. The file

`dgdtools.yml` (also `.dgdtools.yml`), YAML. Unknown keys and values are
errors.

```yaml
# Project keys: what the project is. Root config only.
root: lib                  # the directory that is "/" to the driver (default: this file's directory)
profile: viking            # the mudlib profile, for a partial checkout
lib_path: /players/drake   # where a partial checkout sits in the lib
exclude: ["attic/**"]
dialect: {slash_slash: true, closures: false, typechecking: 2}
include_dirs: ["/include"] # what the driver's config says
include_file: /include/std.h
auto_objects: [/kernel/lib/auto]
conventions:               # the lib's habits: macros, registries
  specifier_macros: [public]
profile_build: {driver_config: etc/mud.dgd}

# Settings: how files are formatted and linted. Any scope.
format:
  indent_style: space
  indent_width: 4
  line_endings: preserve
lint:
  rules: {raw-inherit-path: warning}
  overrides:
    - paths: ["legacy/**"]
      rules: {lifecycle-chain: off}
  fail_on: error           # root config only: one threshold per run
  baseline: lint-baseline.json   # root config only
```

The top level holds the project keys; they are the same facts a profile's
manifest carries, under the same names, and what the file sets wins over
the profile. `lint:` and `format:` hold settings and nothing else. The
split is the point: everything under the two sections can differ per
directory, nothing above them can.

`extends:` names files to start from (resolved relative to the extending
file; scalars and lists replace, maps merge). It is explicit sharing and
works at any level.

## 3. Projects and settings scopes

A `dgdtools.yml` is one of two things.

- A **root config** (the default) defines a project. Its directory, or
  its `root:`, is the lib root.
- A **settings scope** says `root: false`. It adjusts `lint:` and
  `format:` for the files under its directory, inside whatever project
  those files belong to. Any other key in it is an error naming the key
  (`players/drake/dgdtools.yml: exclude belongs in the project's root
  config; this file is a settings scope (root: false)`), and so are
  `lint.fail_on` and `lint.baseline`, which describe a run, not a file.

```yaml
# players/drake/dgdtools.yml
root: false
format: {indent_style: tab}
lint:
  rules: {missing-visibility: warning}
  overrides:
    - paths: ["attic/**"]        # relative to this file's directory
      rules: {all: off}
```

**Finding the project of a path.** Walk up from the path; the first
`dgdtools.yml` that is not a settings scope is the project's root
config. Scopes on the way are skipped, so files under different scopes
of one lib are one project, and "one project per invocation" (a lint run
has exactly one lib) is about root configs only.

**Finding the settings of a file.** The project's root config first,
then every `dgdtools.yml` between the project and the file, outermost
first. For each key the nearest file that sets it wins:

- `format:` keys replace.
- `lint.rules` entries are applied in order: the root's rules, the
  root's `overrides` that match, then each scope's rules and matching
  overrides. A scope is, in effect, an override that lives in the
  directory it is about. Globs in a scope's `overrides` are relative to
  the scope's directory; the root's are relative to the lib root.

A root config found *below* the project that is being run (a wizard's
own checkout config, with `profile:` in it, sitting in the whole lib)
contributes its `lint:` and `format:` like a scope and its project keys
are not used: they describe how to find the lib when that directory is
all there is, and here the lib is known. Refusing it (Biome) would make
a whole-lib run fail because of a file one wizard added; ignoring it
would lint that wizard's files by settings they did not choose.

`--config FILE` uses that file for every path and looks for no other
`dgdtools.yml`. `--no-config` uses no configuration file of any kind.

## 4. `.editorconfig`

For `format.indent_style`, `format.indent_width` and
`format.line_endings`, when no `dgdtools.yml` that applies to the file
sets the key, the file's `.editorconfig` properties are used:
`indent_style`; `indent_size` (or `tab_width` when it says `tab`);
`end_of_line` (`lf`, `crlf`). The standard algorithm: every
`.editorconfig` from the file's directory up to one with `root = true`,
sections matched against the file's path, nearer files and later
sections winning.

A `format:` key in any applicable `dgdtools.yml` beats `.editorconfig`
however near the latter is: the tool's own configuration is the more
specific statement (Prettier's rule). Nothing else is read from
`.editorconfig`; in particular `max_line_length` has no meaning here.

## 5. User-level configuration

`dgdtools.yml` in the user configuration directory
(`$XDG_CONFIG_HOME/dgdtools/`, `~/.config/dgdtools/`) is used for a path
only when no project config is found for it. It is then that path's root
config, with one difference: it has no directory to be a lib root, so a
relative `root:` in it is an error. Its use is a wizard with a bare
checkout and no file in it: `profile: viking` there, once.

It never merges with a project config. What a project's files are
formatted and linted by must not depend on who runs the tool.

## 6. Precedence

Highest first:

1. command-line flags;
2. the nearest settings scope that sets the key, then outer ones;
3. the project's root config (or `--config FILE`; or, when there is
   neither, the user-level config);
4. the profile, for project keys the config leaves unset;
5. `.editorconfig`, for the three format keys;
6. built-in defaults.

`dgdtools config PATH` prints the project of a path and, for a file, its
resolved settings and where each came from.

An editor's own settings carry only what a wizard with no file at all
needs (a profile name, a lib path); they are the same fallback as the
user-level config and never override a project.

## 7. In the library

Per-file resolution is not a command-line feature; the linter, the
formatter and the language server need the same answer.

```go
prj, err := config.Discover(fs, paths, config.DiscoverOptions{...})  // one project (lint)
ws := config.NewWorkspace(fs, config.DiscoverOptions{...})           // many (format, a server)
prj, err = ws.Project(path)

s, err := prj.Settings(path)   // *config.Settings: Format, where each key came from, lint layers
```

`Discover` and `Workspace` read no environment: the front end passes
the working directory, the profile directories and the user-level config
path. A `Project` caches what it reads per directory;
`Project.ResetSettings` drops the cache when a `dgdtools.yml` or
`.editorconfig` changes under a long-lived server.

## 8. The command

```
dgdtools lint [flags] [path ...]      lint files and directories (default: the project)
dgdtools format [flags] path ... | -  format files in place; --check, --diff
dgdtools rules                        list the lint rules
dgdtools explain RULE                 describe one rule
dgdtools config [path]                show the project and settings of a path
dgdtools profile build [-o FILE]      capture the whole lib as a profile
dgdtools profile show [NAME|FILE] [LIBPATH]
dgdtools version
```

- **One binary** (ruff, Biome, Deno, `go`): one thing to install, one
  version, and the name of the binary, the repository and the
  configuration file are the same word.
- **Subcommands, no default command.** `dgdtools PATH` is an error, so a
  file can never be mistaken for a command or the reverse.
- **`lint` and `format`**, the plain verbs; no aliases (`fmt`, `check`):
  one spelling in every script and every answer on a forum.
- **Reserved:** `lsp` (the language server) and `rename` (semantic
  rename) are names the dispatcher knows and answers with "not available
  in this version" and exit 2. They are not in the help: help lists what
  works. Reserving them means an editor plugin configured for a newer
  version gets a precise message, and no later command has to work
  around a squatter.
- **`dgdcmp` is gone.** Its two functions were the token comparison,
  which is the formatter's gate and runs on every file anyway, and a
  token dump for lexer debugging, which a test or `go run` does better
  than a hidden subcommand that has to be kept working.
- **Flags** belong to their subcommand and may stand before, between or
  after paths; `--` ends them. The flags that say where the project is
  (`--config`, `--no-config`, `--root`, `--profile`, `--lib-path`) are
  the same wherever a project is needed.
- **Exit codes**, every subcommand: 0 success; 1 the thing looked for
  was found (lint findings at or above `fail_on`; files that
  `format --check` would change); 2 usage, configuration or I/O error.
- **Front ends are thin.** Each subcommand is
  `func(ctx, env, args) int` over the libraries: it parses flags, reads
  the working directory and environment, calls `pkg/config`, `pkg/lint`,
  `pkg/format`, `pkg/report`, and prints. No analysis, no layout and no
  configuration logic lives in `cmd/`.
