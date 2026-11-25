-- Neovim: the rule of ftdetect/dgdlpc.vim, asked before the built-in
-- detection instead of after it, so that a .c file of a lib is never
-- given the filetype c first (and a C language server with it).
if vim.filetype and vim.filetype.add then
  vim.filetype.add({
    pattern = {
      ['.*%.[ch]'] = {
        function(path, bufnr)
          -- A buffer named by a URL reaches here as a path under the
          -- working directory: its own name says what it is.
          local name = bufnr and vim.api.nvim_buf_get_name(bufnr) or ''
          if name:match('^%a[%w+.-]*://') == nil then
            name = path
          end
          if vim.fn['dgdlpc#Detect'](name) == 1 then
            return 'dgdlpc'
          end
        end,
        { priority = 10 },
      },
    },
  })
end
