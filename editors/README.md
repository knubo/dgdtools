# Editor support for DGD LPC

What an editor needs to show DGD LPC, for the editors this repository
supports. The language is called **`dgdlpc`** everywhere: the VS Code
language id, the Vim filetype. (`lpc` is taken in both by tooling for
other drivers, which colours as keywords words DGD does not have and
knows none of `nil`, `atomic` and `rlimits`.)

| | |
|---|---|
| `vscode/` | the VS Code extension: a TextMate grammar, the language configuration (comments, brackets, indentation), and the client that starts the language server |
| `vim/` | runtime files for Vim and Neovim: filetype detection, syntax, filetype plugin, indent |
| `nvim/` | for Neovim 0.11 and later: the entry that starts the language server (`lsp/dgdtools.lua`) |
| `common/` | what the two share: the word list (`dgdlpc.json`) and the sample files the tests run over |

There are two layers ([lsp.md](../docs/design/lsp.md), section 10). The
first is highlighting that works with no server, offline, on a file that
does not parse: it knows the tokens of the language, exactly as the
tool's own lexer reads them. The second is the language server,
`dgdtools lsp`, which knows what a grammar cannot: today it gives an
editor the linter's findings as you type, their fixes, and the
formatter ([below](#the-language-server)).

## A `.c` file is LPC only where you say so

Nothing here claims `*.c` or `*.h` by name: on a machine that has C
sources too, they stay C. A file is `dgdlpc` because of where it lies:

- **Vim, Neovim:** under a directory that has a `dgdtools.yml` or a
  `.dgdtools.yml` (the file's own directory or any above it), or under a
  directory listed in `g:dgdlpc_roots`.
- **VS Code:** in a workspace whose settings associate the files, which
  the extension writes when asked to (below).

## VS Code

The extension is the folder `vscode/`: the grammar, and a client that
starts `dgdtools lsp` ([below](#vs-code-1)). It is not published in a
marketplace yet. A release of dgdtools has it as files, one package for
each platform with that platform's `dgdtools` inside
(`dgdtools-darwin-arm64-0.4.0.vsix`, and so on for `darwin-x64`,
`linux-x64`, `linux-arm64`, `win32-x64` and `win32-arm64`) and one
without the program for every other platform (`dgdtools-0.4.0.vsix`);
`code --install-extension FILE` installs one.

From a checkout it is built before it is used, which needs Node.js
(22.18 or later) and installs nothing outside the folder. **As a
package**, for the machine you are on and with the `dgdtools` of the
checkout inside it, which needs Go too:

```
cd /path/to/dgdtools
make vsix        # go build, npm ci, and vsce package --target for this platform
code --install-extension editors/vscode/dgdtools-darwin-arm64-0.0.1.vsix
```

The file's name has the platform (`linux-x64`, `win32-arm64`, ...) and
the version of `package.json`, which a release replaces with its own.
`make vsix` is `scripts/vsix.mts --binary bin/dgdtools`, and the script
takes another program and another platform, a version, and a directory
to write to:

```
cd /path/to/dgdtools
GOOS=linux GOARCH=amd64 go build -o /tmp/dgdtools ./cmd/dgdtools
cd editors/vscode
npm ci
node scripts/vsix.mts --binary /tmp/dgdtools --target linux-x64 --version 0.4.0 --out /tmp
```

`--dist DIR` makes all seven packages of a release from what GoReleaser
wrote to `DIR` (`goreleaser build --snapshot --clean` writes `dist/`),
which is what the release workflow runs. The packing is `vsce`'s, the
one `package-lock.json` pins; nothing here publishes anything.

**From the folder as it is**, without a package and without a program
in it (the `dgdtools` on `PATH` is run):

```
cd /path/to/dgdtools/editors/vscode
npm ci           # the client library and the build tools, as package-lock.json pins them
npm run build    # writes dist/extension.js, the one file VS Code runs
```

- **To try it:** `code --extensionDevelopmentPath=/path/to/dgdtools/editors/vscode /path/to/your/lib`
- **To keep it:** link the folder into the extensions directory under
  the name VS Code expects, and restart VS Code:

  ```
  ln -s /path/to/dgdtools/editors/vscode ~/.vscode/extensions/dgdtools.dgdtools-0.0.1
  ```

(The packages were built here and looked into; none was installed:
there is no VS Code on the machine this was written on.)

Then say which files are LPC. The command **dgdtools: Treat This Folder
as an LPC Lib** does (the command palette, or a folder's menu in the
explorer once the extension is at work), and the extension offers it
when a folder with a `dgdtools.yml` is opened whose files are not LPC
yet. What it writes is `files.associations` in the workspace's settings,
so that it holds for this lib and nowhere else; by hand, in the lib's
own `.vscode/settings.json`:

```json
{
  "files.associations": { "*.c": "dgdlpc", "*.h": "dgdlpc" }
}
```

That is the form for the folder that is open. For a folder below it, or
for one folder of a workspace with several, the patterns name the
folder, since the setting is the whole window's: `**/lib/**/*.c` and
`**/lib/**/*.h` for a folder `lib`. **dgdtools: Stop Treating This
Folder as an LPC Lib** takes out what the first command put in. A single
file can be switched with *Change Language Mode* → *DGD LPC*.

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

## The language server

`dgdtools lsp` is the server: the one binary, started by the editor,
talking on its standard input and output. It needs no configuration of
its own. A file's project is the nearest `dgdtools.yml` above it, as for
every command, so what the editor shows is what `dgdtools lint` prints
and what it formats is what `dgdtools format` writes. What it does:

- **Diagnostics** for the files that are open, a moment after you stop
  typing, with the rule's code, a link to the rule's documentation and
  the related places in other files. A change to a header or an
  inherited file reaches the open files that depend on it; a change to
  a `dgdtools.yml`, an `.editorconfig`, a profile or a baseline reaches
  all of them.
- **Code actions** on a finding: its fix when it has one, a comment
  that suppresses it for the line (`/* dgdtools-ignore lint/RULE */`) or
  for the file, and "fix all" (`source.fixAll.dgdtools`) for every fix
  that is safe to apply unseen.
- **Formatting** of a file or a selection, with the project's settings
  (`format:` in `dgdtools.yml`, else `.editorconfig`), not the editor's.
- **Go to definition**: from an `#include` to the header, from an
  `inherit` statement to the object, from a call to the function (the
  file's, an inherited one, the auto object's; `::fn` and `label::fn`;
  `->fn`, `call_other` and string callbacks when the receiver is
  `this_object()` or a path), from a macro to its `#define`, from a
  global variable to its declaration. With a profile, what is not in
  your folder opens as the profile's read-only stub. Where the tools
  cannot know (a call on an object held in a variable, a local
  variable), nothing happens rather than a guess.
- **Hover** on any of those names: the declaration (a function's with
  its specifiers and types, a variable's type, a macro's `#define` and
  what the parser takes it for), the comment directly above it, and
  where it is: the file and line, `auto object`, the profile, or `kfun`
  for a function of the driver's, of which the server has DGD's table
  (the kfuns of your driver's extension modules are `dialect.kfuns` in
  `dgdtools.yml`). On `ob->fn()` with `ob` a variable it says that the
  receiver is not known, and no more.
- **Signature help** in the arguments of a call, as you type `(` and
  `,`: the function's declaration with the parameter you are at, for
  `fn()`, `::fn()`, `label::fn()`, `->fn()` on `this_object()` or a
  path, and kfuns; in `call_other(ob, "fn", ...)` and `call_out("fn",
  delay, ...)` the arguments that are passed on are `fn`'s. It works
  while the line is half written. A call on an unknown receiver has
  none.

It keeps a log when asked to: `dgdtools lsp --log /tmp/dgdtools-lsp.log`,
and with `--trace` every message in both directions. It never writes to
standard error.

**Settings.** There are two, for a wizard whose folder has no
`dgdtools.yml`: `dgdtools.profile` (the mudlib profile to work against:
a name or a file, as `profile:` in a config) and `dgdtools.libPath`
(where the folder sits in the lib, as `lib_path:`). They are per
workspace folder, a config file always wins over them, and nothing else
about the tools is set from an editor.

### Neovim (0.11 and later)

With `editors/vim` (the filetype) and `editors/nvim` (the server's
entry) on the runtime path, and `dgdtools` on `PATH`:

```lua
vim.opt.runtimepath:prepend('~/src/dgdtools/editors/vim')
vim.opt.runtimepath:prepend('~/src/dgdtools/editors/nvim')
vim.lsp.enable('dgdtools')
```

Open a `.c` file under a `dgdtools.yml`; `:checkhealth vim.lsp` shows
the client attached, `:lua vim.diagnostic.setqflist()` lists the
findings, `gra` (or `:lua vim.lsp.buf.code_action()`) offers the fixes
and the comments, `:lua vim.lsp.buf.format()` formats, and `gq` formats
a selection; `CTRL-]` goes to a definition, `K` shows the hover, and
`CTRL-S` in insert mode the signature of the call being written. To
format on save:

```lua
vim.api.nvim_create_autocmd('BufWritePre', {
  pattern = { '*.c', '*.h' },
  callback = function(ev)
    if vim.bo[ev.buf].filetype == 'dgdlpc' then vim.lsp.buf.format({ bufnr = ev.buf }) end
  end,
})
```

A folder with no `dgdtools.yml` has no root marker; say where it is and
what it is (and list it in `g:dgdlpc_roots`, so that its files are
`dgdlpc`):

```lua
vim.g.dgdlpc_roots = { vim.fs.normalize('~/mud/players/me') }
vim.lsp.config('dgdtools', {
  root_dir = vim.fs.normalize('~/mud/players/me'),
  settings = { dgdtools = { profile = 'mylib', libPath = '/players/me' } },
})
```

To try it without touching your configuration, from a checkout of this
repository (`go build -o /tmp/dgdtools ./cmd/dgdtools` first):

```
nvim -u NONE \
  --cmd 'set rtp^=editors/vim,editors/nvim' --cmd 'filetype plugin indent on' --cmd 'syntax on' \
  --cmd "lua vim.lsp.config('dgdtools', { cmd = { '/tmp/dgdtools', 'lsp', '--log', '/tmp/dgdtools-lsp.log' } })" \
  --cmd "lua vim.lsp.enable('dgdtools')" \
  /path/to/your/lib/std/room.c
```

`go test ./editors -run TestNeovimClient` does the same without a
screen: it runs the server under the `nvim` on `PATH`, waits for the
diagnostics of a file, mends one finding by typing and one by the
server's fix, and has the file formatted. `TestNeovimDefinition` goes
to definitions the same way, and `TestNeovimHover` reads what `K` and
`CTRL-S` show.

### Vim

Vim has no client of its own. With
[vim-lsp](https://github.com/prabirshrestha/vim-lsp):

```vim
if executable('dgdtools')
  autocmd User lsp_setup call lsp#register_server({
      \ 'name': 'dgdtools',
      \ 'cmd': ['dgdtools', 'lsp'],
      \ 'allowlist': ['dgdlpc'],
      \ })
endif
```

and with [coc.nvim](https://github.com/neoclide/coc.nvim), in
`coc-settings.json`:

```json
{
  "languageserver": {
    "dgdtools": {
      "command": "dgdtools",
      "args": ["lsp"],
      "filetypes": ["dgdlpc"],
      "rootPatterns": ["dgdtools.yml", ".dgdtools.yml"]
    }
  }
}
```

Neither was run here; they are each client's usual lines for a server on
standard input and output.

### VS Code

The extension (built and installed as [above](#vs-code)) starts the
server when a workspace has a `dgdtools.yml` or a file is `dgdlpc`.
Which `dgdtools` it runs: the program `dgdtools.path` names, when it is
set; else the one in the extension's own folder (`bin/dgdtools`, which a
package made for a platform has, and which is the `dgdtools` of the same
release); else the `dgdtools` on `PATH`. It says what to do when there
is none. To have the editor run what your shell runs although the
package has a program, set `dgdtools.path` to `dgdtools`. The *dgdtools*
channel of the Output panel says which program was started, and its
version.

| setting | |
|---|---|
| `dgdtools.path` | the executable: an absolute path (`~` is the home directory) or a command name on `PATH`. Empty: the one the extension came with, else `dgdtools` on `PATH` |
| `dgdtools.profile`, `dgdtools.libPath` | the two settings [above](#the-language-server), for a folder with no `dgdtools.yml`; each workspace folder can have its own |
| `dgdtools.logFile` | a file for the server's debug log (`--log`) |
| `dgdtools.trace.server` | `messages` or `verbose`: how much is traced while the log level of the *dgdtools* channel of the Output panel is *Trace* |

A change of the first or the third starts the server again, and so does
**dgdtools: Restart Server**. In a workspace that is not trusted, what
the workspace itself says of those two is not listened to. What the
server writes for a person to read (the project a file was found to
belong to, a text the formatter refuses) is in the *dgdtools* channel of
the Output panel.

Fixes are in the lightbulb, formatting is *Format Document* and *Format
Selection*, and both can be had on saving, in the lib's settings:

```json
{
  "[dgdlpc]": {
    "editor.formatOnSave": true,
    "editor.codeActionsOnSave": { "source.fixAll.dgdtools": "explicit" }
  }
}
```

A finding that points at a declaration which comes from a profile opens
the stub, read-only: a document `dgdtools-profile://NAME/std/room.c`,
whose text the server gives (`workspace/textDocumentContent`).

To work on the extension: `npm run check` checks the types, `npm test`
runs the tests of what needs no VS Code (finding the executable, the
association patterns, what the packaging script makes of a release),
and `go test ./editors` holds the manifest to the code and the
script's platforms to GoReleaser's. None of them starts VS Code: the client has not been run in
one here, which is the first thing to do with it.

## Helix, Zed

Both highlight through tree-sitter, and there is no tree-sitter grammar
for DGD LPC yet (deferred: [lsp.md](../docs/design/lsp.md), 10.1). Until
there is, LPC files keep their C highlighting there, which is tolerable
and gets `({ })` and `::` wrong. The language server they can use now.
For Helix, in the lib's own `.helix/languages.toml`, so that it holds
for this lib and nowhere else (not run here):

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

Zed needs an extension to name a language; none is written.

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
