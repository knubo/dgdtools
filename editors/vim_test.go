package editors

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/format"
	"github.com/mmcdole/dgdtools/pkg/lexer"
)

const (
	vimDir       = "vim"
	vimSyntax    = vimDir + "/syntax/dgdlpc.vim"
	indentSample = "common/samples/object.c"
)

// vimKeywordGroups says which class of the word list each group of
// `syn keyword` lines in the syntax file belongs to.
var vimKeywordGroups = map[string]byteClass{
	"dgdlpcConditional": clKeyword,
	"dgdlpcRepeat":      clKeyword,
	"dgdlpcLabel":       clKeyword,
	"dgdlpcStatement":   clKeyword,
	"dgdlpcException":   clKeyword,
	"dgdlpcInherit":     clKeyword,
	"dgdlpcKeyword":     clKeyword,
	"dgdlpcConstant":    clKeyword,
	"dgdlpcType":        clType,
	"dgdlpcSpecifier":   clSpecifier,
}

// TestVimWords: the syntax file's keyword lists and directive names are
// the shared word list's. 'function' is a keyword there only inside the
// closures switch, and // a comment only inside the slash_slash one.
func TestVimWords(t *testing.T) {
	w := loadWordList(t)
	f, err := os.Open(vimSyntax)
	require.NoError(t, err)
	defer f.Close()

	words := map[byteClass][]string{}
	var closures, directives []string
	var inside []string // the conditions of the open if blocks
	directive := regexp.MustCompile(`start="\^\\s\*\\zs#(?:\\s\*(?:\\%\(([a-z\\|]+)\\\)|([a-z]+))\\>)?"`)
	lineComment := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		switch {
		case len(fields) == 0:
		case fields[0] == "if":
			inside = append(inside, strings.Join(fields[1:], " "))
		case fields[0] == "endif":
			require.NotEmpty(t, inside)
			inside = inside[:len(inside)-1]
		case len(fields) > 3 && fields[0] == "syn" && fields[1] == "keyword" && fields[3] != "contained":
			class, ok := vimKeywordGroups[fields[2]]
			require.True(t, ok, "a keyword group this test does not know: %s", fields[2])
			switch strings.Join(inside, " && ") {
			case "":
				words[class] = append(words[class], fields[3:]...)
			case "s:closures":
				assert.Equal(t, clType, class, sc.Text())
				closures = append(closures, fields[3:]...)
			default:
				t.Errorf("keywords under a condition this test does not know: %s", sc.Text())
			}
		case len(fields) > 2 && fields[0] == "syn" && fields[2] == "dgdlpcLineComment":
			lineComment = true
			assert.Equal(t, []string{"s:slash_slash"}, inside, "// is a comment only where the dialect has it")
		}
		if m := directive.FindStringSubmatch(sc.Text()); m != nil && m[1]+m[2] != "" {
			directives = append(directives, strings.Split(m[1]+m[2], `\|`)...)
		}
	}
	require.NoError(t, sc.Err())
	assert.Equal(t, w.Keywords, sorted(words[clKeyword]), "the syntax file's keywords")
	assert.Equal(t, w.Types, sorted(words[clType]), "the syntax file's types")
	assert.Equal(t, w.Specifiers, sorted(words[clSpecifier]), "the syntax file's specifiers")
	assert.Equal(t, w.Closures, sorted(closures), "the syntax file's closures keywords")
	assert.Equal(t, w.Directives, sorted(directives), "the syntax file's directive names")
	assert.True(t, lineComment, "no line comment rule found")
}

// vimScript drives a Vim or Neovim with no configuration but the runtime
// files of this repository, and writes what it finds under $DGDLPC_DIR/out:
// log.txt (one fact a line), a .syn file per sample (line, column, length
// and syntax groups of every run of characters with the same groups), and
// the indent sample as the indent script lays it out.
const vimScript = `
set nocompatible hidden
let s:dir = $DGDLPC_DIR
let s:log = []

function! s:Groups(lnum, col) abort
  return join(map(synstack(a:lnum, a:col), 'synIDattr(v:val, "name")'), ' ')
endfunction

function! s:Dump(file) abort
  execute 'edit' fnameescape(s:dir . '/' . a:file)
  let out = []
  for lnum in range(1, line('$'))
    let len = strlen(getline(lnum))
    let col = 1
    while col <= len
      let groups = s:Groups(lnum, col)
      let start = col
      while col <= len && s:Groups(lnum, col) ==# groups
        let col += 1
      endwhile
      call add(out, printf("%d\t%d\t%d\t%s", lnum, start, col - start, groups))
    endwhile
  endfor
  call writefile(out, s:dir . '/out/' . fnamemodify(a:file, ':t') . '.syn')
endfunction

try
  let &runtimepath = $DGDLPC_RTP . ',' . &runtimepath
  let g:dgdlpc_roots = [s:dir . '/listed']
  filetype plugin indent on
  syntax on

  for s:file in split($DGDLPC_DETECT, ',')
    execute 'edit' fnameescape(s:dir . '/' . s:file)
    call add(s:log, 'filetype ' . s:file . ' ' . &filetype)
  endfor
  " A buffer named by a URL is not decided by the working directory,
  " which here is in a lib.
  execute 'cd' fnameescape(s:dir . '/lib/std')
  execute 'edit' fnameescape('fugitive:///elsewhere/.git//0/x.c')
  call add(s:log, 'url ' . &filetype)
  execute 'cd' fnameescape(s:dir)

  execute 'edit' fnameescape(s:dir . '/lib/std/object.c')
  call add(s:log, 'syntax ' . get(b:, 'current_syntax', ''))
  call add(s:log, 'commentstring ' . &commentstring)
  call add(s:log, 'comments ' . &comments)
  call add(s:log, 'matchpairs ' . &matchpairs)
  call add(s:log, 'indentexpr ' . &indentexpr)
  call add(s:log, 'gf ' . dgdlpc#IncludeExpr('/include/local.h'))
  call add(s:log, 'gf ' . dgdlpc#IncludeExpr('/std/tokens'))
  call add(s:log, 'gf ' . dgdlpc#IncludeExpr('/no/such'))
  call add(s:log, 'gf ' . dgdlpc#IncludeExpr('relative.h'))
  for s:line in ['#include "/a.h"', '  # include <a.h>', 'inherit "/std/room";', 'inherit room "/std/room";', 'private inherit "/std/room";', 'inherited = 1;', 'x = include;']
    call add(s:log, 'include ' . (s:line =~# &include ? matchstr(s:line, &include) : '-') . '|')
  endfor

  for s:file in split($DGDLPC_SAMPLES, ',')
    call s:Dump('lib/std/' . s:file)
  endfor

  execute 'edit' fnameescape(s:dir . '/lib/std/flat.c')
  setlocal shiftwidth=4 expandtab
  normal! gg=G
  execute 'write!' fnameescape(s:dir . '/out/indented.c')

  " The other dialect: // is no comment, function is a type.
  let g:dgdlpc_slash_slash = 0
  let g:dgdlpc_closures = 1
  call s:Dump('lib/std/dialect.c')
  call add(s:log, 'comments ' . &comments)
catch
  call add(s:log, 'error ' . v:exception . ' at ' . v:throwpoint)
endtry
call writefile(s:log, s:dir . '/out/log.txt')
qall!
`

// vims returns the editors on PATH that can run vimScript, by name, with
// the arguments that start each without the user's configuration.
func vims() map[string][]string {
	out := map[string][]string{}
	if path, err := exec.LookPath("vim"); err == nil {
		// A vim built without the expression evaluator or syntax
		// highlighting (vim.tiny) has nothing to test.
		if v, err := exec.Command(path, "--version").Output(); err == nil &&
			strings.Contains(string(v), "+eval") && strings.Contains(string(v), "+syntax") {
			out["vim"] = []string{path, "-u", "NONE", "-i", "NONE", "-N", "-n", "-es"}
		}
	}
	if path, err := exec.LookPath("nvim"); err == nil {
		out["nvim"] = []string{path, "--headless", "-u", "NONE", "-i", "NONE", "-n"}
	}
	return out
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// flatten takes the indentation off every line.
func flatten(src string) string {
	lines := strings.SplitAfter(src, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimLeft(l, " \t")
	}
	return strings.Join(lines, "")
}

// vimClass puts the syntax groups at a character in the lexer's words.
func vimClass(groups []string) byteClass {
	has := func(names ...string) bool {
		for _, g := range groups {
			for _, n := range names {
				if g == n {
					return true
				}
			}
		}
		return false
	}
	switch {
	case has("dgdlpcPreProc", "dgdlpcPreError", "dgdlpcPreCondit", "dgdlpcDefine", "dgdlpcInclude"):
		return clDirective
	case has("dgdlpcComment", "dgdlpcLineComment"):
		return clComment
	case has("dgdlpcString"):
		return clString
	case has("dgdlpcCharacter"):
		return clChar
	case len(groups) == 0:
		return clOther
	}
	switch g := groups[len(groups)-1]; g {
	case "dgdlpcNumber":
		return clInt
	case "dgdlpcFloat":
		return clFloat
	case "dgdlpcOperator", "dgdlpcArrayDelim", "dgdlpcMappingDelim":
		return clOperator
	default:
		if c, ok := vimKeywordGroups[g]; ok {
			return c
		}
	}
	return clOther
}

// readDump reads a .syn file of vimScript as spans by line.
func readDump(t *testing.T, path string, lines int) [][]span {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	out := make([][]span, lines)
	for _, l := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if l == "" {
			continue
		}
		f := strings.SplitN(l, "\t", 4)
		require.Len(t, f, 4, l)
		lnum, err1 := strconv.Atoi(f[0])
		col, err2 := strconv.Atoi(f[1])
		n, err3 := strconv.Atoi(f[2])
		require.True(t, err1 == nil && err2 == nil && err3 == nil && lnum >= 1 && lnum <= lines, l)
		out[lnum-1] = append(out[lnum-1], span{col - 1, col - 1 + n, f[3]})
	}
	return out
}

// TestVimRuntime runs the runtime files in the vim and the nvim on PATH:
// filetype detection by where a file lies, the options of the filetype
// plugin, the syntax groups of the samples (held to pkg/lexer's tokens
// and to the snapshots beside them), and the indent script against the
// formatter. It is skipped where there is neither editor, except in CI.
func TestVimRuntime(t *testing.T) {
	editors := vims()
	if len(editors) == 0 {
		noEditor(t, "no vim or nvim on PATH that can run the runtime files")
	}
	rtp, err := filepath.Abs(vimDir)
	require.NoError(t, err)

	// The indent sample is what the formatter writes: the indent script
	// is held to the formatter, not to a file somebody laid out.
	indentSrc, err := os.ReadFile(indentSample)
	require.NoError(t, err)
	formatted, err := format.Format(lexer.Lex(indentSample, indentSrc, lexer.Default), format.Options{})
	require.NoError(t, err)
	require.Equal(t, string(indentSrc), string(formatted), "%s must be a fixed point of the formatter", indentSample)

	// Where each file must be taken for what.
	detect := map[string]string{
		"lib/std/tokens.c":     "dgdlpc", // dgdtools.yml two directories up
		"lib/std/directives.h": "dgdlpc",
		"lib/std/new.c":        "dgdlpc", // a file that does not exist yet
		"alt/a/b/x.h":          "dgdlpc", // .dgdtools.yml
		"listed/x.c":           "dgdlpc", // g:dgdlpc_roots
		"plain/x.c":            "c",      // C everywhere else
		"plain/x.h":            "",       // filled in below: c or cpp, by the editor's own rule
		"lib/std/notes.txt":    "text",
	}

	for name, argv := range editors {
		t.Run(name, func(t *testing.T) {
			// Resolved, because that is the path the editor reports.
			dir, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			var names []string
			for path, src := range samples(t) {
				names = append(names, filepath.Base(path))
				writeFile(t, filepath.Join(dir, "lib/std", filepath.Base(path)), src)
			}
			writeFile(t, filepath.Join(dir, "lib/dgdtools.yml"), "")
			writeFile(t, filepath.Join(dir, "lib/include/local.h"), "")
			writeFile(t, filepath.Join(dir, "lib/std/flat.c"), flatten(string(indentSrc)))
			writeFile(t, filepath.Join(dir, "lib/std/dialect.c"), "x = 1; // not a comment\nfunction f;\n")
			writeFile(t, filepath.Join(dir, "lib/std/notes.txt"), "notes\n")
			writeFile(t, filepath.Join(dir, "alt/.dgdtools.yml"), "")
			writeFile(t, filepath.Join(dir, "alt/a/b/x.h"), "int x;\n")
			writeFile(t, filepath.Join(dir, "listed/x.c"), "int x;\n")
			writeFile(t, filepath.Join(dir, "plain/x.c"), "int x;\n")
			writeFile(t, filepath.Join(dir, "plain/x.h"), "int x;\n")
			writeFile(t, filepath.Join(dir, "script.vim"), vimScript)
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "out"), 0o755))
			var files []string
			for f := range detect {
				files = append(files, f)
			}

			cmd := exec.Command(argv[0], append(argv[1:], "-S", filepath.Join(dir, "script.vim"))...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"DGDLPC_RTP="+rtp, "DGDLPC_DIR="+dir,
				"DGDLPC_SAMPLES="+strings.Join(names, ","), "DGDLPC_DETECT="+strings.Join(files, ","))
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)

			data, err := os.ReadFile(filepath.Join(dir, "out/log.txt"))
			require.NoError(t, err, "%s", output)
			facts := map[string][]string{}
			for _, l := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
				key, value, _ := strings.Cut(l, " ")
				facts[key] = append(facts[key], value)
			}
			require.Empty(t, facts["error"], "the script failed")

			// Detection.
			require.Len(t, facts["filetype"], len(detect))
			for _, f := range facts["filetype"] {
				file, ft, _ := strings.Cut(f, " ")
				if want := detect[file]; want != "" {
					assert.Equal(t, want, ft, file)
				} else {
					assert.NotEqual(t, "dgdlpc", ft, file)
					assert.NotEmpty(t, ft, file)
				}
			}

			// The filetype plugin and the indent script.
			assert.Equal(t, []string{"dgdlpc"}, facts["syntax"])
			assert.Equal(t, []string{"/* %s */"}, facts["commentstring"])
			assert.Equal(t, []string{"(:),{:},[:]"}, facts["matchpairs"])
			assert.Equal(t, []string{"dgdlpc#Indent()"}, facts["indentexpr"])
			assert.Equal(t, []string{"c"}, facts["url"], "a buffer named by a URL")
			if c := facts["comments"]; assert.Len(t, c, 2) {
				assert.Contains(t, c[0], "://", "with dialect.slash_slash")
				assert.NotContains(t, c[1], "://", "without it")
			}
			assert.Equal(t, []string{
				filepath.Join(dir, "lib/include/local.h"),
				filepath.Join(dir, "lib/std/tokens.c"),
				"/no/such",
				"relative.h",
			}, facts["gf"])
			assert.Equal(t, []string{
				`#include|`, `  # include|`, `inherit|`, `inherit room|`, `private inherit|`, `-|`, `-|`,
			}, facts["include"])

			// The syntax groups of the samples.
			for path, src := range samples(t) {
				lines := sourceLines(src)
				spans := readDump(t, filepath.Join(dir, "out", filepath.Base(path)+".syn"), len(lines))
				class := make([]byteClass, len(src)+1)
				boundary := make([]bool, len(src)+1)
				off := 0
				for i, text := range strings.SplitAfter(src, "\n") {
					if i == len(lines) {
						break
					}
					for _, s := range spans[i] {
						c := vimClass(strings.Fields(s.label))
						for j := s.start; j < s.end; j++ {
							class[off+j] = c
						}
						boundary[off+s.start] = true
					}
					off += len(text)
				}
				lexSample(t, path, src).agree(t, path, class, boundary, false)
				compareSnapshot(t, path+".vim.snap", renderSnapshot(lines, spans))
			}

			// The other dialect.
			other := readDump(t, filepath.Join(dir, "out/dialect.c.syn"), 2)
			for _, s := range other[0] {
				assert.NotContains(t, s.label, "Comment", "// without dialect.slash_slash")
			}
			if assert.NotEmpty(t, other[1]) {
				assert.Equal(t, span{0, 8, "dgdlpcType"}, other[1][0], "function with dialect.closures")
			}

			// Indentation: the formatter's, from the same lines.
			indented, err := os.ReadFile(filepath.Join(dir, "out/indented.c"))
			require.NoError(t, err)
			want, have := sourceLines(string(indentSrc)), sourceLines(string(indented))
			if assert.Len(t, have, len(want)) {
				for i := range want {
					assert.Equal(t, want[i], have[i], "%s:%d: the formatter's line, and the indent script's", indentSample, i+1)
				}
			}
		})
	}
}
