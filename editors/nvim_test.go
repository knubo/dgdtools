package editors

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nvimSession is what the Neovim on PATH does with the language server:
// it opens a file of a lib, waits for its diagnostics, mends one finding
// by typing, another by the server's fix, has the file formatted, and
// writes down what it saw.
const nvimSession = `
local dir, out = vim.env.DGDLPC_DIR, {}
local ok, err = pcall(function()
  vim.opt.runtimepath:prepend(vim.env.DGDLPC_NVIM)
  vim.opt.runtimepath:prepend(vim.env.DGDLPC_VIM)
  vim.cmd('filetype plugin indent on')
  vim.lsp.config('dgdtools', { cmd = { vim.env.DGDLPC_BIN, 'lsp', '--log', dir .. '/lsp.log' } })
  vim.lsp.enable('dgdtools')
  vim.cmd('edit ' .. vim.fn.fnameescape(dir .. '/lib/obj/a.c'))
  out.filetype = vim.bo.filetype

  local function found()
    local list = {}
    for _, d in ipairs(vim.diagnostic.get(0)) do
      table.insert(list, string.format('%d:%d-%d %s %s', d.lnum, d.col, d.end_col, d.code, d.source))
    end
    table.sort(list)
    return list
  end
  local function wait(what, cond)
    assert(vim.wait(20000, cond, 20), 'waited in vain for ' .. what)
  end

  wait('diagnostics', function() return #found() == 2 end)
  out.first = found()
  local client = vim.lsp.get_clients({ bufnr = 0 })[1]
  out.encoding, out.root, out.server = client.offset_encoding, client.root_dir, client.server_info

  -- Typing: the comparison becomes an assignment, sent as a range.
  local row = 4
  local col = vim.api.nvim_buf_get_lines(0, row, row + 1, false)[1]:find('==', 1, true) - 1
  vim.api.nvim_buf_set_text(0, row, col, row, col + 2, { '=' })
  wait('one finding less', function() return #found() == 1 end)
  out.second = found()

  -- The server's fix for what is left.
  vim.lsp.buf.code_action({ context = { only = { 'source.fixAll' }, diagnostics = {} }, apply = true })
  wait('the fix', function() return vim.api.nvim_buf_get_lines(0, 5, 6, false)[1]:find('" + "', 1, true) ~= nil end)
  wait('no findings', function() return #found() == 0 end)

  -- Formatting, of a line that needs it.
  vim.api.nvim_buf_set_lines(0, 2, 3, false, { 'string     s;' })
  vim.lsp.buf.format({ timeout_ms = 10000 })
  out.text = vim.api.nvim_buf_get_lines(0, 0, -1, false)
end)
if not ok then out.error = tostring(err) end
vim.fn.writefile({ vim.json.encode(out) }, dir .. '/result.json')
vim.cmd('qall!')
`

// TestNeovimClient runs the language server under the Neovim on PATH,
// with the configuration in editors/nvim and the runtime files in
// editors/vim: the built-in client of Neovim 0.11 and later is the
// first editor the server is used from.
func TestNeovimClient(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the command and starts an editor")
	}
	nvim, err := exec.LookPath("nvim")
	if err != nil {
		t.Skip("no nvim on PATH")
	}
	if out, err := exec.Command(nvim, "--headless", "-u", "NONE", "-i", "NONE", "-n",
		"+lua io.stdout:write(tostring(vim.lsp.config ~= nil and vim.lsp.enable ~= nil))", "+qall!").Output(); err != nil || string(out) != "true" {
		t.Skipf("the nvim on PATH has no vim.lsp.config (0.11 and later): %s %v", out, err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	bin := filepath.Join(dir, "dgdtools")
	if out, err := exec.Command("go", "build", "-o", bin, "../cmd/dgdtools").CombinedOutput(); err != nil {
		t.Fatalf("building the command: %v\n%s", err, out)
	}
	const text = "void f()\n{\n    string s;\n\n    /* blåbær \U0001F600 */ s == \"x\";\n    s = \"one\" \"two\";\n}\n"
	writeFile(t, filepath.Join(dir, "lib/dgdtools.yml"), "")
	writeFile(t, filepath.Join(dir, "lib/obj/a.c"), text)
	writeFile(t, filepath.Join(dir, "session.lua"), nvimSession)
	vimDir, err := filepath.Abs(vimDir)
	require.NoError(t, err)
	nvimDir, err := filepath.Abs("nvim")
	require.NoError(t, err)

	cmd := exec.Command(nvim, "--headless", "-u", "NONE", "-i", "NONE", "-n", "-S", filepath.Join(dir, "session.lua"))
	cmd.Env = append(os.Environ(), "DGDLPC_DIR="+dir, "DGDLPC_BIN="+bin, "DGDLPC_VIM="+vimDir, "DGDLPC_NVIM="+nvimDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	require.NoError(t, err, "%s", out)
	var got struct {
		Error, Filetype, Encoding, Root string
		Server                          struct{ Name string }
		First, Second, Text             []string
	}
	require.NoError(t, json.Unmarshal(data, &got), "%s", data)
	log, _ := os.ReadFile(filepath.Join(dir, "lsp.log"))
	require.Empty(t, got.Error, "the server's log:\n%s", log)

	assert.Equal(t, "dgdlpc", got.Filetype)
	assert.Equal(t, "dgdtools", got.Server.Name)
	assert.Equal(t, filepath.Join(dir, "lib"), got.Root)
	// Neovim counts in bytes when the server lets it, which it does.
	assert.Equal(t, "utf-8", got.Encoding)
	col := strings.Index(strings.Split(text, "\n")[4], "==")
	require.Equal(t, 26, col, "bytes, where UTF-16 units would be 22")
	assert.Equal(t, []string{"4:26-28 S002 dgdtools", "5:14-19 E002 dgdtools"}, got.First)
	assert.Equal(t, []string{"5:14-19 E002 dgdtools"}, got.Second)
	assert.Equal(t, strings.Split("void\nf()\n{\n    string s;\n\n    /* blåbær \U0001F600 */ s = \"x\";\n    s = \"one\" + \"two\";\n}", "\n"), got.Text)
	assert.Contains(t, string(log), "msg=initialized encoding=utf-8")
}
