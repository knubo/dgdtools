-- Neovim 0.11 and later: the language server of dgdtools, for the files
-- of the filetype dgdlpc. plugin/dgdtools.lua enables it when dgdtools
-- can be run; vim.lsp.config('dgdtools', {...}) changes any of it.
--
-- The server finds a file's project from the file, as the command does;
-- the root markers only say which files share one server. A wizard whose
-- folder has no dgdtools.yml says what the tools cannot know otherwise:
--
--   vim.lsp.config('dgdtools', {
--     root_dir = vim.fs.normalize('~/mud/players/me'),
--     settings = { dgdtools = { profile = 'mylib', libPath = '/players/me' } },
--   })
return {
  cmd = { 'dgdtools', 'lsp' },
  filetypes = { 'dgdlpc' },
  root_markers = { 'dgdtools.yml', '.dgdtools.yml' },
  settings = { dgdtools = vim.empty_dict() },
}
