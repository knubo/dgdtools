" Vim filetype plugin
" Language:	DGD LPC (LPC as the DGD driver compiles it)
" Maintainer:	dgdtools, https://github.com/mmcdole/dgdtools (editors/vim)

if exists('b:did_ftplugin')
  finish
endif
let b:did_ftplugin = 1

let s:cpo_save = &cpo
set cpo&vim

" Comments: /* */ in every dialect, // where the dialect has it (see
" syntax/dgdlpc.vim for the variable). Commenting a line out uses the form
" every driver compiles.
setlocal commentstring=/*\ %s\ */
if get(b:, 'dgdlpc_slash_slash', get(g:, 'dgdlpc_slash_slash', 1))
  setlocal comments=sO:*\ -,mO:*\ \ ,exO:*/,s1:/*,mb:*,ex:*/,://
else
  setlocal comments=sO:*\ -,mO:*\ \ ,exO:*/,s1:/*,mb:*,ex:*/
endif
setlocal formatoptions-=t formatoptions+=croql

" The pairs % jumps between. ({ }) and ([ ]) need none of their own: each
" is two of these, nested. '<' and '>' are operators here, never a pair.
setlocal matchpairs=(:),{:},[:]

" Enough for gf, [i and [d without a language server: #include and
" inherit name a file, with an optional label after inherit; a path that
" begins with '/' is a lib path (dgdlpc#IncludeExpr); an inherited object
" is named without its .c.
setlocal include=^\\s*\\%(#\\s*include\\>\\\|\\%(private\\s\\+\\)\\=inherit\\>\\%(\\s\\+\\h\\w*\\s\\@=\\)\\=\\)
setlocal includeexpr=dgdlpc#IncludeExpr(v:fname)
setlocal define=^\\s*#\\s*define
setlocal suffixesadd=.c,.h

let b:undo_ftplugin = 'setlocal commentstring< comments< formatoptions< matchpairs<'
      \ . ' include< includeexpr< define< suffixesadd<'

" matchit: % also goes from #if to #else to #endif.
if exists('g:loaded_matchit') && !exists('b:match_words')
  let b:match_words = '^\s*#\s*if\%(def\|ndef\)\=\>:^\s*#\s*elif\>:^\s*#\s*else\>:^\s*#\s*endif\>'
  let b:undo_ftplugin .= ' | unlet! b:match_words'
endif

let &cpo = s:cpo_save
unlet s:cpo_save
