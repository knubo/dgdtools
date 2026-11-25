# DGD LPC for Helix

Helix highlights only through tree-sitter, and there is no tree-sitter
grammar for DGD LPC yet ([lsp.md](../../docs/design/lsp.md), 10.1), so
LPC files keep C's highlighting, which gets `({ })` and `::` wrong. The
language server, `dgdtools lsp`, works now: diagnostics, fixes,
formatting, navigation, completion, rename.

With `dgdtools` on `PATH` ([install it](../../README.md#installation)),
put this in the mudlib's own `.helix/languages.toml`, so that its `.c`
and `.h` files are LPC and those of every other project stay C:

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

`hx --health dgdlpc` says whether the server is found. These lines were
not run in Helix.
