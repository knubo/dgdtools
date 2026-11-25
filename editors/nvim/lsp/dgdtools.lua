-- Neovim 0.11 and later: the language server of dgdtools, for the files
-- editors/vim gives the filetype dgdlpc. With this directory on the
-- runtime path:
--
--   vim.lsp.enable('dgdtools')
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
