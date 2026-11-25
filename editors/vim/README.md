# LPC (DGD) for Vim and Neovim

A single plugin for Vim and Neovim providing the `dgdlpc` filetype (detection, syntax highlighting, ftplugin, and indentation) plus automatic `dgdtools lsp` integration in Neovim 0.11+ when `dgdtools` is on `PATH` ([installation instructions](../../README.md#installation)).

## Installation

The plugin lives in the `editors/vim` subdirectory of this repository.

**vim-plug:**

```vim
Plug 'mmcdole/dgdtools', { 'rtp': 'editors/vim' }
```

**lazy.nvim:**

```lua
{
  'mmcdole/dgdtools',
  config = function(plugin)
    vim.opt.rtp:append(plugin.dir .. '/editors/vim')
    -- lazy.nvim has already sourced runtimepath plugins by the time config runs.
    vim.cmd.runtime({ 'ftdetect/dgdlpc.lua', 'plugin/dgdtools.lua', bang = true })
  end,
}
```

**vim.pack (Neovim 0.12+):**

```lua
vim.pack.add({ 'https://github.com/mmcdole/dgdtools' })
vim.opt.rtp:append(vim.pack.get({ 'dgdtools' })[1].path .. '/editors/vim')
```

**Manual (native packages):**

```sh
git clone https://github.com/mmcdole/dgdtools ~/src/dgdtools
mkdir -p ~/.vim/pack/dgdtools/start
ln -s ~/src/dgdtools/editors/vim ~/.vim/pack/dgdtools/start/dgdtools
```

For Neovim, use `~/.local/share/nvim/site/pack/dgdtools/start` instead.

Vim users should ensure `filetype plugin indent on` and `syntax on` are enabled in their `vimrc` (both are on by default in Neovim).

## Filetype detection

The plugin does not claim `*.c` or `*.h` files globally—normal C files stay `c`. A `.c` or `.h` file is detected as `dgdlpc` only if:

- Its directory or any ancestor directory contains `dgdtools.yml` or `.dgdtools.yml`, or
- It is inside a directory listed in `g:dgdlpc_roots`:

```lua
vim.g.dgdlpc_roots = { vim.fs.normalize('~/mud/players/me') }
```

In Neovim, matching files start directly as `dgdlpc`. In Vim, built-in detection assigns `c` first and this plugin switches it to `dgdlpc` immediately afterward.

## Filetype features & options

- **Syntax:** Highlights DGD keywords only, `({ })` and `([ ])` literals, DGD string escapes, `..`, `->`, `<-`, `::`, and multi-line preprocessor directives.
- **Ftplugin:** Configures `commentstring` and `comments`; supports `gf`, `[i`, and `[d` on `#include`, `inherit`, and `#define` lines (resolving leading `/` paths from ancestor directories); and configures `matchit` (`%`) for `#if`/`#else`/`#endif`.
- **Indentation:** C-style indentation tuned to match `dgdtools format`, including nested array and mapping literals. A few edge cases (blocks on the same line as a `case` label, multi-line literal elements, statements after `goto` labels) are handled cleanly by `dgdtools format`. `shiftwidth` and `expandtab` follow your own Vim/Neovim settings or `.editorconfig` (the formatter defaults to 4 spaces).

Buffer-local (`b:`) variables override global (`g:`) variables so different mudlibs can use different settings:

| Variable | Default | Description |
|---|---|---|
| `g:dgdlpc_slash_slash` | `1` | Treat `//` as a line comment (`1`) or as two `/` operators (`0`) |
| `g:dgdlpc_closures` | `0` | Highlight `function` as a type keyword (`1`) when DGD `CLOSURES` is enabled |
| `g:dgdlpc_roots` | `[]` | Directories whose `.c`/`.h` files should be treated as `dgdlpc` without a `dgdtools.yml` |
| `g:dgdlpc_no_functions` | `0` | Disable highlighting identifiers before `(` as function calls (`1`) |
| `g:dgdlpc_minlines` | unset | Limit comment syntax sync lookback to this many lines instead of syncing from start of file |

Note: `g:dgdlpc_slash_slash` and `g:dgdlpc_closures` are configured manually here; the Vim syntax files do not parse `dgdtools.yml`.

## Language server in Neovim (0.11+)

With the plugin installed and `dgdtools` on `PATH`, opening a `dgdlpc` buffer starts `dgdtools lsp` automatically. Check `:checkhealth vim.lsp` to verify it is attached.

Standard Neovim LSP mappings work out of the box: `gra` (code actions), `grn` (rename), `grr` (references), `K` (hover), `CTRL-]` (go to definition), `gO` (document symbols), `CTRL-X CTRL-O` (completion), and `gq` or `:lua vim.lsp.buf.format()` (format). See [editors/README.md](../README.md#the-language-server-dgdtools-lsp) for the full list of server features.

### Customizing the LSP config

The plugin ships `lsp/dgdtools.lua` under the server name `dgdtools` (no `nvim-lspconfig` entry is needed, and `vim.lsp.config` works whether or not `nvim-lspconfig` is installed). Override settings in your `init.lua`—for example, for a partial checkout without a `dgdtools.yml`:

```lua
vim.lsp.config('dgdtools', {
  root_dir = vim.fs.normalize('~/mud/players/me'),
  settings = { dgdtools = { profile = 'mylib', libPath = '/players/me' } },
})
```

You can also override `cmd` with `vim.lsp.config` before opening an LPC file if `dgdtools` isn't on `PATH`. To disable automatic startup and manage enabling manually:

```lua
vim.g.dgdtools_lsp = false
vim.lsp.enable('dgdtools') -- call when you want to enable it
```

### Semantic token highlights

Standard LSP token types inherit your colorscheme automatically. You can customize the `dgdtools`-specific modifiers if desired:

```lua
vim.api.nvim_set_hl(0, '@lsp.typemod.function.defaultLibrary.dgdlpc', { link = '@function.builtin' })
vim.api.nvim_set_hl(0, '@lsp.typemod.method.defaultLibrary.dgdlpc', { link = '@function.builtin' })
vim.api.nvim_set_hl(0, '@lsp.typemod.function.inherited.dgdlpc', { link = '@function.call' })
vim.api.nvim_set_hl(0, '@lsp.typemod.type.macro.dgdlpc', { link = 'Type' })
vim.api.nvim_set_hl(0, '@lsp.typemod.modifier.macro.dgdlpc', { link = 'StorageClass' })
```

### Format on save

```lua
vim.api.nvim_create_autocmd('BufWritePre', {
  pattern = { '*.c', '*.h' },
  callback = function(ev)
    if vim.bo[ev.buf].filetype == 'dgdlpc' then
      vim.lsp.buf.format({ bufnr = ev.buf })
    end
  end,
})
```

### Trying it without changing your config

```sh
make build
PATH=$PWD/bin:$PATH nvim --clean --cmd 'set rtp^=editors/vim' /path/to/your/lib/std/room.c
```

## Language server in Vim

Vim doesn't include a built-in LSP client, so register `dgdtools lsp` with your LSP plugin of choice. The helper `dgdlpc#Root()` finds the nearest `dgdtools.yml` directory.

**[vim-lsp](https://github.com/prabirshrestha/vim-lsp):**

```vim
if executable('dgdtools')
  autocmd User lsp_setup call lsp#register_server({
      \ 'name': 'dgdtools',
      \ 'cmd': ['dgdtools', 'lsp'],
      \ 'allowlist': ['dgdlpc'],
      \ 'root_uri': {server_info -> lsp#utils#path_to_uri(dgdlpc#Root(expand('%:p:h')) ?? expand('%:p:h'))},
      \ })
endif
```

**[ALE](https://github.com/dense-analysis/ale)** (place after `plug#end()` if installed with vim-plug):

```vim
call ale#linter#Define('dgdlpc', {
    \ 'name': 'dgdtools',
    \ 'lsp': 'stdio',
    \ 'executable': 'dgdtools',
    \ 'command': '%e lsp',
    \ 'project_root': {buffer -> dgdlpc#Root(fnamemodify(bufname(buffer), ':p:h')) ?? fnamemodify(bufname(buffer), ':p:h')},
    \ })
```

**[coc.nvim](https://github.com/neoclide/coc.nvim)** (`coc-settings.json`):

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

### Partial checkouts without `dgdtools.yml`

If you don't have a `dgdtools.yml` in a checkout folder, pass `profile` and `libPath` via initialization options (and make sure your client's root points to the checkout directory):

- **vim-lsp / ALE:** add `'initialization_options': {'profile': 'mylib', 'libPath': '/players/me'}` to the registration dictionary.
- **coc.nvim:** add `"initializationOptions": { "profile": "mylib", "libPath": "/players/me" }` to the `dgdtools` entry.

Clients that support `workspace/configuration` can also pass these per folder under the `dgdtools` settings section (`workspace_config` in vim-lsp, `settings` in coc.nvim).

### External file changes

`dgdtools lsp` relies on the editor client to report files changed outside the editor (for example, by `git pull` or `dgdtools rename --write`). `vim-lsp` and ALE don't watch files; `coc.nvim` does when Watchman is installed, and Neovim's file watcher varies by platform (disabled by default on Linux). If your client doesn't watch files, open the changed file or restart the server to refresh diagnostics.

## Tests

Run `go test ./editors` to test filetype detection, options, syntax, indentation, and (with Neovim 0.11+ on `PATH`) LSP integration (`TestNeovim*`).
