" DGD LPC (filetype dgdlpc) for *.c and *.h, by where the file lies and
" never by its name alone: under a directory that has a dgdtools.yml or a
" .dgdtools.yml, or under a directory listed in g:dgdlpc_roots. Everywhere
" else these files stay what Vim takes them for.
"
" This runs after Vim's own detection and replaces its answer. In Neovim
" ftdetect/dgdlpc.lua has answered first, and there is nothing to replace.

autocmd BufNewFile,BufRead *.c,*.h
      \ if &filetype !=# 'dgdlpc' && dgdlpc#Detect(expand('<afile>:p')) |
      \   set filetype=dgdlpc |
      \ endif
