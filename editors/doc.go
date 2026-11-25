// Package editors holds what an editor needs to show DGD LPC: the
// TextMate grammar and language configuration of the VS Code extension
// (vscode/), the Vim and Neovim runtime files (vim/), Neovim's entry for
// the language server (nvim/), and what the grammars share (common/):
// the word list and the sample files.
//
// There is no Go code here to import. The package exists for its tests,
// which keep the grammars from drifting away from the tool:
//
//   - common/dgdlpc.json is written from pkg/token, pkg/lexer and pkg/pp
//     (go test ./editors -update), and each grammar's word lists are
//     compared with it;
//   - the TextMate grammar is loaded, checked rule by rule, and run over
//     the samples by a small interpreter of the grammar format; what it
//     scopes is compared with the tokens pkg/lexer finds in the same
//     bytes, and with a snapshot beside each sample;
//   - the Vim files are run by the vim and nvim on PATH, when there are
//     any: filetype detection, the syntax groups of the samples (same
//     comparison, same kind of snapshot), and indentation;
//   - the language server is run under the nvim on PATH, when it is one
//     with vim.lsp.config (0.11 and later): the command is built, and
//     an editor session has it lint, fix and format a file.
//
// See docs/design/lsp.md, sections 10.1 and 11.
package editors
