" Functions of the dgdlpc filetype (DGD LPC), loaded when first called.
" Part of dgdtools: https://github.com/mmcdole/dgdtools (editors/vim)

let s:cpo_save = &cpo
set cpo&vim

" The directory of {dir} and each one above it, nearest first.
function! s:Upward(dir) abort
  let dirs = []
  let dir = substitute(fnamemodify(a:dir, ':p'), '[/\\]$', '', '')
  while index(dirs, dir) < 0
    call add(dirs, dir)
    let dir = fnamemodify(dir, ':h')
  endwhile
  return dirs
endfunction

" dgdlpc#Root({dir}): the nearest directory, {dir} or one above it, that
" has a dgdtools.yml or a .dgdtools.yml; '' when none has.
function! dgdlpc#Root(dir) abort
  for dir in s:Upward(a:dir)
    if filereadable(dir . '/dgdtools.yml') || filereadable(dir . '/.dgdtools.yml')
      return dir
    endif
  endfor
  return ''
endfunction

" dgdlpc#Detect({file}): whether {file} is DGD LPC by where it lies: under
" a directory that has a dgdtools configuration, or under one of
" g:dgdlpc_roots (directories the user says are libs).
function! dgdlpc#Detect(file) abort
  let dir = fnamemodify(a:file, ':p:h')
  for root in get(g:, 'dgdlpc_roots', [])
    let root = substitute(fnamemodify(expand(root), ':p'), '[/\\]$', '', '')
    if dir ==# root || stridx(dir, root . '/') == 0
      return 1
    endif
  endfor
  return dgdlpc#Root(dir) !=# ''
endfunction

" dgdlpc#IncludeExpr({fname}): 'includeexpr'. A path that begins with '/'
" is a lib path, as in #include "/include/std.h" and inherit "/std/room":
" it is looked for under the directory of the buffer and each one above
" it, so it is found from anywhere in a lib without knowing where the
" lib begins.
function! dgdlpc#IncludeExpr(fname) abort
  if a:fname !~# '^/'
    return a:fname
  endif
  for dir in s:Upward(expand('%:p:h'))
    for suffix in ['', '.c', '.h']
      if filereadable(dir . a:fname . suffix)
        return dir . a:fname . suffix
      endif
    endfor
  endfor
  return a:fname
endfunction

let &cpo = s:cpo_save
unlet s:cpo_save
