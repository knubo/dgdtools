# Editor support for DGD LPC

What an editor needs to show DGD LPC, for the editors this repository
supports. The language is called **`dgdlpc`** everywhere: the VS Code
language id, the Vim filetype. (`lpc` is taken in both by tooling for
other drivers, which colours as keywords words DGD does not have and
knows none of `nil`, `atomic` and `rlimits`.)

| | |
|---|---|
| `vscode/` | the VS Code extension: a TextMate grammar and the language configuration (comments, brackets, indentation) |
| `vim/` | runtime files for Vim and Neovim: filetype detection, syntax, filetype plugin, indent |
| `common/` | what the two share: the word list (`dgdlpc.json`) and the sample files the tests run over |

This is the first of two layers ([lsp.md](../docs/design/lsp.md),
section 10): highlighting that works with no server, offline, on a file
that does not parse. It knows the tokens of the language, exactly as the
tool's own lexer reads them. It does not know which name is a macro, a
kfun or an inherited function; that is for the language server
(`dgdtools lsp`, planned), which these files will be the clients of.

## A `.c` file is LPC only where you say so

Nothing here claims `*.c` or `*.h` by name: on a machine that has C
sources too, they stay C. A file is `dgdlpc` because of where it lies:

- **Vim, Neovim:** under a directory that has a `dgdtools.yml` or a
  `.dgdtools.yml` (the file's own directory or any above it), or under a
  directory listed in `g:dgdlpc_roots`.
- **VS Code:** in a workspace whose settings associate the files (below).

## VS Code

The extension is not published yet. From a checkout of this repository:

- **To try it:** `code --extensionDevelopmentPath=/path/to/dgdtools/editors/vscode /path/to/your/lib`
- **To keep it:** copy or link the folder into the extensions directory
  under the name VS Code expects, and restart VS Code:

  ```
  ln -s /path/to/dgdtools/editors/vscode ~/.vscode/extensions/dgdtools.dgdtools-0.0.1
  ```

Then say which files are LPC, in the lib's own `.vscode/settings.json`
(workspace settings, so that it holds for this lib and nowhere else):

```json
{
  "files.associations": { "*.c": "dgdlpc", "*.h": "dgdlpc" }
}
```

A single file can be switched with *Change Language Mode* → *DGD LPC*.
(When the extension gets its language client, it will offer to write
that setting for a folder that has a `dgdtools.yml`.)

A lib whose files are Latin-1 should also say so, or saving a file
opened as UTF-8 damages it:

```json
{
  "[dgdlpc]": { "files.encoding": "iso88591" }
}
```

What the grammar cannot do, being one fixed file for every lib:

- `//` is always shown as a comment, whatever `dialect.slash_slash`
  says. Where the driver was built without it, `//` outside a string is
  a syntax error (the linter reports it), so no valid file is coloured
  wrongly.
- `function` is always a plain name, as it is unless `dialect.closures`
  is set. In a lib that sets it, the word is not coloured as a type.

## Vim and Neovim

`editors/vim` is a runtime directory: `ftdetect/`, `syntax/`,
`ftplugin/`, `indent/`, `autoload/`. Put it on the runtime path any way
you like.

As a package, with no plugin manager (Vim 8 and later; for Neovim the
directory is `~/.local/share/nvim/site/pack/...`):

```
git clone https://github.com/mmcdole/dgdtools ~/src/dgdtools
mkdir -p ~/.vim/pack/dgdtools/start
ln -s ~/src/dgdtools/editors/vim ~/.vim/pack/dgdtools/start/dgdlpc
```

With a plugin manager, name the subdirectory:

```vim
" vim-plug
Plug 'mmcdole/dgdtools', { 'rtp': 'editors/vim' }
```

```lua
-- lazy.nvim, from a checkout
{ dir = '~/src/dgdtools/editors/vim' }
```

or simply, before `filetype plugin indent on`:

```vim
set runtimepath^=~/src/dgdtools/editors/vim
```

It needs `filetype plugin indent on` and `syntax on`, which Neovim has
by default.

What you get in a `dgdlpc` buffer:

- **Syntax:** the driver's reserved words and no others, `({ })` and
  `([ ])` as brackets of their own, DGD's escapes, `1..2` as a range,
  `->`, `<-` and `::`, directives to the end of their logical line.
- **Filetype plugin:** `commentstring` and `comments` for `/* */` (and
  `//` where the dialect has it); `gf`, `[i` and `[d` on `#include`,
  `inherit` and `#define` lines, where a path that begins with `/` is
  looked up from the directories above the file; `%` between `#if`,
  `#else` and `#endif` with matchit.
- **Indent:** C indenting set to what `dgdtools format` writes: case
  labels at the level of their `switch`, a function's name at the margin
  under its return type, one level for a continued line and for the
  inside of `({ })` and `([ ])`. `shiftwidth` and `expandtab` are yours
  (or your `.editorconfig`'s); the formatter's default is four spaces.

Variables (`b:` is asked before `g:`, so a lib can differ from the rest):

| | default | |
|---|---|---|
| `g:dgdlpc_slash_slash` | `1` | `//` begins a comment, as with `dialect.slash_slash: true`. `0`: it is two operators |
| `g:dgdlpc_closures` | `0` | `1`: `function` is a type keyword, as with `dialect.closures: true` |
| `g:dgdlpc_roots` | `[]` | directories whose `*.c` and `*.h` are LPC without a `dgdtools.yml` above them |
| `g:dgdlpc_no_functions` | `0` | `1`: do not highlight a name before `(` |
| `g:dgdlpc_minlines` | unset | synchronize the syntax on comments within this many lines, instead of reading the file from its start |

The two dialect variables are set by hand to what the lib's
`dgdtools.yml` (or its profile) says; the runtime files do not read it.

In Vim a lib's `.c` file is given the filetype `c` first and `dgdlpc`
right after, because Vim's own detection runs first; an autocommand on
`FileType c` sees it for that moment. In Neovim (0.8 and later) the
answer is `dgdlpc` from the start.

## Helix, Zed

Both highlight through tree-sitter, and there is no tree-sitter grammar
for DGD LPC yet (deferred: [lsp.md](../docs/design/lsp.md), 10.1). Until
there is, they have nothing to gain from this directory: LPC files keep
their C highlighting, which is tolerable and gets `({ })` and `::`
wrong. What they will get first is the language server (diagnostics,
formatting, navigation), configured as a language named `dgdlpc` with
`dgdtools.yml` as its root marker; the lines for their configuration
come with the server.

## Changing a grammar

The grammars are tested by `go test ./editors`, which needs nothing
installed:

- `common/dgdlpc.json` is written from the tool's own tables
  (`pkg/token`, `pkg/lexer`, `pkg/pp`), and the keyword lists and
  directive names of both grammars are compared with it. A keyword added
  to the language fails the test until both grammars have it.
- The TextMate grammar is run, by an interpreter of the grammar format
  that is part of the test, over `common/samples/`. Its tokens are
  compared with the lexer's, byte for byte, and with the snapshot beside
  each sample (`*.tm.snap`). Because the test runs it with Go's regular
  expressions and an editor with Oniguruma, the grammar keeps to the
  syntax the two share (no `\d \w \s`, no `$`, no lookbehind), plus a
  lookahead at the end of a pattern; the test turns away anything else.
- The Vim files are run by the `vim` and the `nvim` on `PATH`, when
  there are any: detection, the options, the syntax groups of the samples
  (compared with the lexer's tokens and with `*.vim.snap`), and the
  indent script, which must lay out `samples/object.c` as the formatter
  does.

After a change that is meant: `go test ./editors -update`, and read the
diff of the snapshots.
