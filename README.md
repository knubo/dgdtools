# dgdtools

CLI tooling and a Language Server Protocol (LSP) server for LPC as compiled by the [DGD](https://github.com/dworkin/dgd) driver, shipped as a single binary (`dgdtools`) with editor clients for VS Code, Vim/Neovim, and Helix.

Most LPC tooling targets LDMud or FluffOS and trips over DGD syntax (`({ })` and `([ ])` literals, `<-`, `..` ranges, labeled inherits, K&R-style typeless functions). `dgdtools` implements the DGD 1.7 grammar directly, with configurable support for driver build options like `SLASHSLASH` (`//` comments) and `CLOSURES`.

Everything is built around two main pieces:

## CLI Tooling

Command-line tools for linting, formatting, and refactoring DGD mudlibs—usable on a full mudlib, in CI, or inside a single wizard checkout via a mudlib profile:

- **`dgdtools lint`** — Catches errors DGD rejects at compile time (syntax errors, unsupported constructs, missing includes) and runtime bugs the driver won't catch until execution (calls by name via `->`, `call_other`, `call_out`, or custom registrars to missing functions or objects). Follows a *prove it or stay silent* approach to avoid false positives, with support for baselines, auto-fixes, and JSON/SARIF/GitHub Actions output.
- **`dgdtools format`** — An opinionated, whitespace-only formatter in the spirit of `gofmt`. Preserves your line breaks, supports partial formatting (`--lines`, `--changed` via git), and verifies token identity before writing any file.
- **`dgdtools rename`** — Renames functions (including overrides across an inheritance hierarchy), global variables, and macros across the mudlib, or moves files/directories while rewriting every `inherit`, `#include`, and object path. Previews diffs by default and separates confirmed edits from ambiguous ones.
- **`dgdtools profile`** — Builds compact `.profile` interface files (headers and public signatures, no function bodies) so wizards can lint and navigate their own directory without cloning the entire mudlib.

→ See the **[CLI Guide](docs/cli.md)**, **[Configuration & Profiles](docs/configuration.md)**, and **[Lint Rules Reference](docs/rules.md)**.

## Language Server & Editor Support

`dgdtools lsp` exposes the same parser, linter, formatter, and refactoring engine over stdio using the Language Server Protocol. Because it reads the same `dgdtools.yml` as the CLI, editor diagnostics and formatting always match the command line.

Features include live diagnostics and quick fixes, formatting, go to definition, hover and signature help (including built-in and custom kfuns), completion, document/workspace symbols, find all references, cross-mudlib rename, automatic path updates on file moves, and semantic highlighting.

Editor integrations use the language ID / filetype **`dgdlpc`** and never claim `.c`/`.h` files globally, so regular C projects stay unaffected:

- **[VS Code](editors/vscode/README.md)** — Extension with syntax highlighting and LSP integration (release packages bundle the `dgdtools` binary for macOS, Linux, and Windows).
- **[Vim & Neovim](editors/vim/README.md)** — Plugin providing filetype detection, syntax highlighting, indent, and automatic LSP setup in Neovim 0.11+ (plus configs for `vim-lsp`, ALE, and `coc.nvim`).
- **[Helix](editors/helix/README.md)** — Configuration for running `dgdtools lsp` with C highlighting.

→ See **[Editor Support & LSP Guide](editors/README.md)** for full server capabilities, generic LSP setup, building the VS Code extension, and grammar testing.

## Installation

Install with Go 1.25 or later:

```sh
go install github.com/mmcdole/dgdtools/cmd/dgdtools@latest
```

Prebuilt binaries (and VS Code `.vsix` packages) are also available on the [Releases](https://github.com/mmcdole/dgdtools/releases) page.

To build and test from source:

```sh
git clone https://github.com/mmcdole/dgdtools
cd dgdtools
make build          # Outputs bin/dgdtools
make test
```

To run the full-tree corpus checks (byte-exact lexer round-trip, token comparator, and formatter safety gate) against a real mudlib:

```sh
make corpus LPC_CORPUS=/path/to/lib
```

## Quick Start

`dgdtools` works out of the box with no configuration for file-local linting and formatting:

```sh
dgdtools lint path/to/file.c
dgdtools format --diff path/to/file.c
```

For cross-file checks, navigation, and refactoring, add a `dgdtools.yml` at the root of your mudlib:

```yaml
root: lib                     # Directory corresponding to mudlib "/"
dialect:
  slash_slash: true           # Allow // comments
  closures: false
profile_build:
  name: mymud
  driver_config: etc/mud.dgd  # Reads include_dirs, include_file, auto_object, create, typechecking
  share: ["kernel/**", "lib/**"]
  checkouts: ["usr/*"]
```

Or, if you are a wizard working in a partial checkout, point `dgdtools.yml` at your mud's published profile:

```yaml
profile: mymud
```

Then run `dgdtools lint` or open the folder in your editor. See [dgdtools.example.yml](dgdtools.example.yml) and [examples/viking/dgdtools.yml](examples/viking/dgdtools.yml) for full examples.

## Documentation

| Document | Contents |
|---|---|
| [CLI Guide](docs/cli.md) | `lint`, `format`, `rename`, exit codes, suppressions, output formats, and Go library overview |
| [Configuration & Profiles](docs/configuration.md) | `dgdtools.yml` reference, per-directory scopes, `.editorconfig`, and mudlib profiles |
| [Lint Rules](docs/rules.md) | Complete list of all lint rules, codes, defaults, and options |
| [Editor Support & LSP](editors/README.md) | `dgdtools lsp` features, editor setup overview, VSIX builds, and grammar tests |
| [VS Code](editors/vscode/README.md) · [Vim / Neovim](editors/vim/README.md) · [Helix](editors/helix/README.md) | Per-editor installation and configuration |
| [Design Docs](docs/design/architecture.md) | Architecture ([architecture.md](docs/design/architecture.md), [config.md](docs/design/config.md), [formatter.md](docs/design/formatter.md), [lsp.md](docs/design/lsp.md)) |

## License Note

The DGD dialect details implemented here (keywords, operators, literal and escape forms, preprocessor behavior, and call semantics) are based on DGD's grammar, documentation, and observable behavior. No DGD source code (AGPLv3) has been copied or translated into this repository.
