" Vim indent file
" Language:	DGD LPC (LPC as the DGD driver compiles it)
" Maintainer:	dgdtools, https://github.com/mmcdole/dgdtools (editors/vim)
"
" C indenting, set to what `dgdtools format` writes, so that a line comes
" out where the formatter would put it:
"   :0  case labels at the level of their switch
"   l1  what follows `case X: {` is indented from the label
"   t0  a function's return type at the margin, its name on the next line
"   (s  a line inside an open parenthesis one level in, as every line that
"   u0  continues a statement is, however deep the parentheses; this is
"       also the inside of ([ ])
"   m1  a line that begins with ')' at the level of the line that opened it
"   J1  `key: value` is an entry of a mapping, not a label
"   j1  ({ }) as an argument is a block, and its '})' closes it at the
"       level of the line that opened it
" A line that begins with ']' goes to the level of the line with its '['
" without being told.
"
" Three shapes come out differently from the formatter's, which settles
" them when it runs: a block after a case label on the same line, ({ })
" inside ([ ]) over several lines, and the statement after a goto label
" (the price of J1: there are more mappings than labels).

if exists('b:did_indent')
  finish
endif
let b:did_indent = 1

setlocal cindent
setlocal cinoptions=:0,l1,t0,(s,u0,m1,J1,j1
setlocal cinkeys=0{,0},0),0],:,0#,!^F,o,O,e

let b:undo_indent = 'setlocal cindent< cinoptions< cinkeys<'
