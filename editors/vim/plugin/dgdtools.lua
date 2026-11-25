-- Neovim 0.11 and later: start the language server for dgdlpc buffers
-- (lsp/dgdtools.lua) when its command can be run. Vim does not load this
-- file. To start it yourself, or not at all, set before the first LPC
-- file opens:
--
--   vim.g.dgdtools_lsp = false
if vim.g.loaded_dgdtools or vim.lsp.enable == nil then
  return
end
vim.g.loaded_dgdtools = true

-- Decided when the first dgdlpc buffer opens rather than now, so that a
-- vim.lsp.config('dgdtools', { cmd = ... }) or a g:dgdtools_lsp set after
-- plugins load (in a plugin manager's config function) is what counts.
vim.api.nvim_create_autocmd('FileType', {
  group = vim.api.nvim_create_augroup('dgdtools', {}),
  pattern = 'dgdlpc',
  once = true,
  callback = function()
    if vim.g.dgdtools_lsp == false or vim.g.dgdtools_lsp == 0 then
      return
    end
    local cmd = (vim.lsp.config.dgdtools or {}).cmd
    if type(cmd) == 'function' or (type(cmd) == 'table' and vim.fn.executable(cmd[1]) == 1) then
      vim.lsp.enable('dgdtools')
    end
  end,
})
