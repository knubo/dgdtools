# LPC (DGD) for Helix

Helix uses tree-sitter for syntax highlighting, and there is no tree-sitter grammar for DGD LPC yet. As a workaround, you can reuse Helix's C grammar for basic highlighting (which won't understand DGD-specific syntax like `({ })` or `::`) while getting full diagnostics, quick fixes, formatting, navigation, completion, and rename from `dgdtools lsp`.

## Setup

With `dgdtools` on `PATH` ([installation instructions](../../README.md#installation)), add `.helix/languages.toml` inside your mudlib so only that project's `.c` and `.h` files are treated as `dgdlpc`:

```toml
[language-server.dgdtools]
command = "dgdtools"
args = ["lsp"]

[[language]]
name = "dgdlpc"
scope = "source.dgdlpc"
file-types = ["c", "h"]
roots = ["dgdtools.yml", ".dgdtools.yml"]
comment-tokens = "//"
block-comment-tokens = { start = "/*", end = "*/" }
indent = { tab-width = 4, unit = "    " }
grammar = "c"
language-servers = ["dgdtools"]
```

Because Helix looks up tree-sitter queries under the language's own name (`dgdlpc`), add user-level query shims that inherit from C (run once per machine):

```sh
dir=~/.config/helix/runtime/queries/dgdlpc   # %AppData%\helix\runtime\queries\dgdlpc on Windows
mkdir -p "$dir"
for q in highlights indents injections locals textobjects; do
  echo '; inherits: c' > "$dir/$q.scm"
done
```

Run `hx --health dgdlpc` to verify that Helix finds both `dgdtools` and the query files. Note that this Helix configuration has not been verified interactively yet.
