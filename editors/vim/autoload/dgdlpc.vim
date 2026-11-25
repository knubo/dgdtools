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
  " A buffer named by a URL (fugitive://, scp://, zipfile://) is not a
  " file under the working directory, which is all a lookup could ask.
  if a:file =~# '^\a[a-zA-Z0-9+.-]*://'
    return 0
  endif
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

" dgdlpc#Indent(): 'indentexpr'. cindent() with 'cinoptions', except for
" a line directly inside an array or mapping literal: one level in from
" the line that holds its '({' or '([', and a line that begins with '}'
" or ']' at that line's level, as the formatter puts them. The lines above are taken to be
" indented already (as they are when typing, and under '='), so the line
" that holds an open bracket is found by indentation: the nearest one
" above that is indented less. Matching every bracket in between is too
" slow in a long literal.
function! dgdlpc#Indent() abort
  let lnum = v:lnum
  let text = getline(lnum)
  let line = s:CodeAbove(lnum)
  if text =~# '^\s*#' || text =~# s:comment_line || line == 0
    return cindent(lnum)
  endif
  let [open, closes] = s:Brackets(getline(line))
  while len(open) <= closes
    " The line closes all it opens, and maybe more: what it is inside is
    " held by the nearest line above it indented less.
    let closes -= len(open)
    let n = indent(line)
    call cursor(line, 1)
    let line = n == 0 ? 0 : search('^\s*\%<' . (n + 1) . 'v[^ \t#*/]', 'bnW')
    if line == 0
      return cindent(lnum)
    endif
    let [open, more] = s:Brackets(getline(line))
    let closes += more
  endwhile
  if open[-1 - closes] !~# '^(.'
    return cindent(lnum)
  endif
  return indent(line) + (text =~# '^\s*[]}]' ? 0 : shiftwidth())
endfunction

" A line that continues a comment, or is one.
let s:comment_line = '^\s*\%(\*\|/\*\|//\)'

" The nearest line above {lnum} that is code: neither blank, a directive
" nor a comment; 0 when there is none.
function! s:CodeAbove(lnum) abort
  let lnum = prevnonblank(a:lnum - 1)
  while lnum > 0 && getline(lnum) =~# '^\s*#\|' . s:comment_line
    let lnum = prevnonblank(lnum - 1)
  endwhile
  return lnum
endfunction

" s:Brackets({line}): [open, closes]. open: the brackets {line} leaves
" open, innermost last, each '([', '({', '(', '[' or '{'; closes: how many
" it closes that it did not open. Closers that begin the line are left
" out: they close what a line indented as this one opened. Strings,
" character constants and comments are left out.
function! s:Brackets(line) abort
  let t = substitute(substitute(a:line, '^[])}[:space:]]*', '', ''), '"\%(\\.\|[^"\\]\)*"\|''\%(\\.\|[^''\\]\)*''\|/\*.\{-}\*/', '', 'g')
  let t = substitute(t, '//.*\|/\*.*', '', '')
  let t = substitute(t, '[^][(){}]', '', 'g')
  let open = []
  let closes = 0
  let i = 0
  while i < len(t)
    let c = t[i]
    if c =~# '[[{]' && i > 0 && t[i - 1] ==# '(' && get(open, -1, '') ==# '('
      let open[-1] = '(' . c
    elseif c =~# '[[({]'
      call add(open, c)
    else
      " ]) and }) close ([ and ({ together.
      if c =~# '[]}]' && t[i + 1] ==# ')' && get(open, -1, '(') =~# '^('
        let i += 1
      endif
      if empty(open)
        let closes += 1
      else
        call remove(open, -1)
      endif
    endif
    let i += 1
  endwhile
  return [open, closes]
endfunction

let &cpo = s:cpo_save
unlet s:cpo_save
