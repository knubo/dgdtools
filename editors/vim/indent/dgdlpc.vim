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
"   u0  continues a statement is, however deep the parentheses
"   m1  a line that begins with ')' at the level of the line that opened it
"   J1  `key: value` is an entry of a mapping, not a label
"   j1  ({ }) as an argument is a block, and its '})' closes it at the
"       level of the line that opened it
" cindent() indents the lines of a literal, ({ }) or ([ ]), from the
" statement, which is wrong for one nested in another; dgdlpc#Indent()
" puts them one level in from the line that holds the '({' or '([', and
" the line that closes it at that line's level, as the formatter does,
" and asks cindent() for every other line.
"
" Three shapes come out differently from the formatter's, which settles
" them when it runs: a block after a case label on the same line, an
" element of a literal continued over several lines (its lines after the
" first are not indented further), and the statement after a goto label
" (the price of J1: there are more mappings than labels).

if exists('b:did_indent')
  finish
endif
let b:did_indent = 1

setlocal cinoptions=:0,l1,t0,(s,u0,m1,J1,j1
setlocal indentexpr=dgdlpc#Indent()
setlocal indentkeys=0{,0},0),0],:,0#,!^F,o,O,e

let b:undo_indent = 'setlocal cinoptions< indentexpr< indentkeys<'
