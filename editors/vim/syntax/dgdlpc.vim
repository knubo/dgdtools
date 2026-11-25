" Vim syntax file
" Language:	DGD LPC (LPC as the DGD driver compiles it)
" Maintainer:	dgdtools, https://github.com/mmcdole/dgdtools (editors/vim)
"
" The lexical layer, as pkg/lexer of dgdtools reads it. The keyword lists
" and directive names are checked against editors/common/dgdlpc.json by
" `go test ./editors`; change them there first.
"
" The dialect, as the lib's dgdtools.yml has it (b: is asked before g:):
"   dgdlpc_slash_slash  1 (default): // begins a comment (dialect.slash_slash)
"   dgdlpc_closures     0 (default): 'function' is a name like any other;
"                       1: it is a type keyword (dialect.closures)
" Taste:
"   dgdlpc_no_functions 1: do not highlight a name before '('
"   dgdlpc_minlines     N: synchronize on comments within N lines instead
"                       of reading the file from its start

if exists('b:current_syntax')
  finish
endif

let s:cpo_save = &cpo
set cpo&vim

let s:slash_slash = get(b:, 'dgdlpc_slash_slash', get(g:, 'dgdlpc_slash_slash', 1))
let s:closures = get(b:, 'dgdlpc_closures', get(g:, 'dgdlpc_closures', 0))

syn case match

" Reserved words: the driver's, no more.
syn keyword dgdlpcConditional	if else switch
syn keyword dgdlpcRepeat	for while do
syn keyword dgdlpcLabel		case default
syn keyword dgdlpcStatement	break continue return goto rlimits
syn keyword dgdlpcException	try catch
syn keyword dgdlpcInherit	inherit
syn keyword dgdlpcKeyword	new operator
syn keyword dgdlpcConstant	nil
syn keyword dgdlpcType		int float string object mapping mixed void
syn keyword dgdlpcSpecifier	private static atomic nomask varargs
if s:closures
  syn keyword dgdlpcType	function
endif

" Names, by pattern: a name before '(' is a function, a name in capitals
" a macro by convention, a name before '::' or between inherit and a path
" the label of an inherited object. Later ones win where two match.
if !get(b:, 'dgdlpc_no_functions', get(g:, 'dgdlpc_no_functions', 0))
  syn match dgdlpcFunction	"\<\h\w*\ze\s*("
endif
syn match dgdlpcMacro		"\<_*\u[A-Z0-9_]\+\>"
syn match dgdlpcInheritLabel	"\<\h\w*\ze\s*::"
syn match dgdlpcInheritLabel	"\%(\<inherit\s\+\)\@<=\h\w*\ze\s\+[\"A-Za-z_]"

" Numbers. 1..2 is a range of two integers, never a float; the operator
" below takes the dots before a float could.
syn match dgdlpcNumber		"\<\d\+\>"
syn match dgdlpcNumber		"\<0[xX]\x*\>"
syn match dgdlpcFloat		"\<\d\+\.\.\@!\d*\%([eE][+-]\=\d\+\)\="
syn match dgdlpcFloat		"\<\d\+[eE][+-]\=\d\+\>"
syn match dgdlpcFloat		"\.\d\+\%([eE][+-]\=\d\+\)\="

" The operators C does not have.
syn match dgdlpcOperator	"\.\.\.\=\|->\|<-\|::"

" Strings and character constants. The driver's escapes: up to three
" octal digits, \x and up to two hex digits, any other character for
" itself. A string ends with its line unless a backslash continues it;
" that backslash is no dgdlpcEscape, or a string continued inside a
" directive would end with the directive's first line.
syn match dgdlpcEscape		contained "\\\%(\o\{1,3}\|x\x\{,2}\|.\)"
syn region dgdlpcString		start=+"+ skip=+\\.\|\\$+ excludenl end=+"+ end=+$+ contains=dgdlpcEscape,@Spell
syn match dgdlpcCharacter	"'\%([^\\']\|\\\%(\o\{1,3}\|x\x\{,2}\|.\)\)'" contains=dgdlpcEscape

" Array and mapping literals: ({ }) and ([ ]) are brackets of their own.
" '])' closes a mapping only where '([' opened one: in f(a[i]) the
" subscript has taken the ']'.
syn region dgdlpcArray		matchgroup=dgdlpcArrayDelim start="({" end="})" transparent fold
syn region dgdlpcMapping	matchgroup=dgdlpcMappingDelim start="(\[" end="\])" transparent fold
syn region dgdlpcSubscript	start="\[" end="\]" transparent

" Comments. // is a comment where the dialect has it; where it has not,
" it is two operators, and shown as that.
syn keyword dgdlpcTodo		contained TODO FIXME XXX
syn region dgdlpcComment	start="/\*" end="\*/" contains=dgdlpcTodo,@Spell extend fold
if s:slash_slash
  syn match dgdlpcLineComment	"//.*" contains=dgdlpcTodo,@Spell
endif

" Preprocessor directives: '#' first on its line, to the end of the
" logical line. A literal left open inside one ends with it (keepend); a
" comment does not (extend, above). The names are the driver's; any other
" is a directive still, and gets no more than that. A condition contains
" no keywords: the if of #if is not the statement.
syn region dgdlpcPreProc	start="^\s*\zs#" skip="\\$" end="$" keepend contains=ALLBUT,@dgdlpcPreProcGroup,@Spell
syn region dgdlpcPreProc	start="^\s*\zs#\s*\%(line\|pragma\)\>" skip="\\$" end="$" keepend contains=ALLBUT,@dgdlpcPreProcGroup,@Spell
syn region dgdlpcPreError	start="^\s*\zs#\s*error\>" skip="\\$" end="$" keepend contains=dgdlpcComment,dgdlpcLineComment
syn region dgdlpcPreCondit	start="^\s*\zs#\s*\%(if\|ifdef\|ifndef\|elif\|else\|endif\)\>" skip="\\$" end="$" keepend contains=dgdlpcComment,dgdlpcLineComment,dgdlpcString,dgdlpcCharacter,dgdlpcNumber,dgdlpcFloat,dgdlpcMacro
syn region dgdlpcDefine		start="^\s*\zs#\s*\%(define\|undef\)\>" skip="\\$" end="$" keepend contains=ALLBUT,@dgdlpcPreProcGroup,@Spell
syn region dgdlpcInclude	start="^\s*\zs#\s*include\>" skip="\\$" end="$" keepend contains=dgdlpcIncluded,dgdlpcMacro,dgdlpcComment,dgdlpcLineComment
syn region dgdlpcIncluded	contained start=+"+ skip=+\\.+ end=+"+ end=+$+
syn match dgdlpcIncluded	contained "<[^>]*>"
syn cluster dgdlpcPreProcGroup	contains=dgdlpcPreProc,dgdlpcPreError,dgdlpcPreCondit,dgdlpcDefine,dgdlpcInclude,dgdlpcIncluded,dgdlpcEscape,dgdlpcTodo

if exists('b:dgdlpc_minlines') || exists('g:dgdlpc_minlines')
  execute 'syn sync ccomment dgdlpcComment minlines=' . get(b:, 'dgdlpc_minlines', get(g:, 'dgdlpc_minlines', 0))
else
  syn sync fromstart
endif

hi def link dgdlpcConditional	Conditional
hi def link dgdlpcRepeat	Repeat
hi def link dgdlpcLabel		Label
hi def link dgdlpcStatement	Statement
hi def link dgdlpcException	Exception
hi def link dgdlpcInherit	Include
hi def link dgdlpcKeyword	Keyword
hi def link dgdlpcConstant	Constant
hi def link dgdlpcType		Type
hi def link dgdlpcSpecifier	StorageClass
hi def link dgdlpcFunction	Function
hi def link dgdlpcMacro		Macro
hi def link dgdlpcInheritLabel	Identifier
hi def link dgdlpcNumber	Number
hi def link dgdlpcFloat		Float
hi def link dgdlpcOperator	Operator
hi def link dgdlpcEscape	SpecialChar
hi def link dgdlpcString	String
hi def link dgdlpcCharacter	Character
hi def link dgdlpcArrayDelim	Delimiter
hi def link dgdlpcMappingDelim	Delimiter
hi def link dgdlpcTodo		Todo
hi def link dgdlpcComment	Comment
hi def link dgdlpcLineComment	Comment
hi def link dgdlpcPreProc	PreProc
hi def link dgdlpcPreError	PreProc
hi def link dgdlpcPreCondit	PreCondit
hi def link dgdlpcDefine	Macro
hi def link dgdlpcInclude	Include
hi def link dgdlpcIncluded	String

let b:current_syntax = 'dgdlpc'

let &cpo = s:cpo_save
unlet s:cpo_save
