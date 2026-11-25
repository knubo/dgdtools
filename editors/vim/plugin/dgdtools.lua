-- Neovim 0.11 and later: start the language server for dgdlpc buffers
-- (lsp/dgdtools.lua) when its command can be run. Vim does not load this
-- file. To start it yourself, or not at all, set before plugins load:
--
--   vim.g.dgdtools_lsp = false
if vim.g.loaded_dgdtools or vim.lsp.enable == nil then
  return
end
vim.g.loaded_dgdtools = true

if vim.g.dgdtools_lsp == false or vim.g.dgdtools_lsp == 0 then
  return
end
-- The command as configured, so that a vim.lsp.config('dgdtools', { cmd
-- = ... }) made before this runs is what is looked for.
local cmd = (vim.lsp.config.dgdtools or {}).cmd
if type(cmd) == 'function' or (type(cmd) == 'table' and vim.fn.executable(cmd[1]) == 1) then
  vim.lsp.enable('dgdtools')
end
