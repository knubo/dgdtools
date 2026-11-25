package lsp_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/lsp"
	"github.com/mmcdole/dgdtools/pkg/lsp/lsptest"
)

var update = flag.Bool("update", false, "rewrite the transcripts in testdata")

// TestTranscripts runs the sessions in testdata/*.txt. Each is an
// archive in the txtar layout: the files of a lib, a script, and the
// transcript the script is to produce, which -update writes.
//
// A script line is something an editor does; the transcript repeats it
// after ">" and puts under it what the server said to it, as JSON, with
// the lib's directory written $ROOT.
//
//	initialize [plain] [utf-8] [options=JSON] [FOLDER ...]
//	settings FOLDER JSON         what workspace/configuration answers
//	configuration                workspace/didChangeConfiguration
//	open FILE ["TEXT"]           TEXT is a Go string; without, the file's
//	change FILE L:C-L:C "TEXT"   an incremental change, positions as sent
//	replace FILE "TEXT"          a change of the whole text
//	save FILE | close FILE
//	diagnostics FILE             what is published for it, once idle
//	actions FILE L:C-L:C [KIND]  textDocument/codeAction
//	apply N                      make the edit of the Nth action of those
//	format FILE [L:C-L:C]        (range) formatting, and make the edits
//	definition FILE L:C          textDocument/definition
//	hover FILE L:C               textDocument/hover
//	signature FILE L:C           textDocument/signatureHelp
//	complete FILE L:C [CHAR]     textDocument/completion, asked for by
//	                             typing CHAR when there is one
//	resolve N                    completionItem/resolve of the Nth item
//	tokens FILE [L:C-L:C]        textDocument/semanticTokens/full (range),
//	                             decoded: a token a line
//	delta FILE [ID]              textDocument/semanticTokens/full/delta
//	                             from the last tokens of the file (or
//	                             those of ID), the edits made to them
//	prepare FILE L:C             textDocument/prepareRename
//	rename FILE L:C NAME         textDocument/rename, and make the edits
//	symbols FILE                 textDocument/documentSymbol
//	find QUERY                   workspace/symbol
//	text FILE                    the editor's text
//	write FILE "TEXT" | remove FILE         on disk
//	watched created|changed|deleted FILE ...
//	received METHOD              what the server sent with that method
//	watchers                     what it asked to have watched
//	request METHOD JSON | notify METHOD JSON
//	shutdown | exit
func TestTranscripts(t *testing.T) {
	files, err := filepath.Glob("testdata/*.txt")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, file := range files {
		t.Run(strings.TrimSuffix(filepath.Base(file), ".txt"), func(t *testing.T) {
			data, err := os.ReadFile(file)
			require.NoError(t, err)
			head, sections := archive(string(data))
			lib := map[string]string{}
			var script, want string
			for _, s := range sections {
				switch s.name {
				case "script":
					script = s.body
				case "transcript":
					want = s.body
				default:
					lib[s.name] = s.body
				}
			}
			got := (&player{t: t, root: tree(t, lib), c: start(t), seen: map[string]int{}}).play(script)
			if *update {
				var b strings.Builder
				b.WriteString(head)
				for _, s := range sections {
					if s.name != "transcript" {
						fmt.Fprintf(&b, "-- %s --\n%s", s.name, s.body)
					}
				}
				fmt.Fprintf(&b, "-- transcript --\n%s", got)
				require.NoError(t, os.WriteFile(file, []byte(b.String()), 0o644))
				return
			}
			assert.Equal(t, want, got, "go test ./pkg/lsp -run TestTranscripts -update rewrites the transcripts")
		})
	}
}

type section struct{ name, body string }

// archive splits a txtar archive into what stands before its first file
// and its files.
func archive(data string) (head string, out []section) {
	for _, line := range strings.SplitAfter(data, "\n") {
		name, ok := strings.CutPrefix(strings.TrimSpace(line), "-- ")
		name, ok2 := strings.CutSuffix(name, " --")
		switch {
		case ok && ok2:
			out = append(out, section{name: name})
		case len(out) == 0:
			head += line
		default:
			out[len(out)-1].body += line
		}
	}
	return head, out
}

// sent are semantic tokens and their id.
type sent struct {
	id   string
	data []uint32
}

// player plays a script against a server.
type player struct {
	t    *testing.T
	root string
	c    *lsptest.Client
	out  strings.Builder
	// actions are those last returned, of the file named; seen how many
	// of the server's messages of each method were shown.
	actions    []lsp.CodeAction
	actionFile string
	// items are those of the last completion, as they came.
	items    []json.RawMessage
	settings map[string]json.RawMessage
	// tokens are the last semantic tokens of each file, as the client
	// has them.
	tokens map[string]sent
	seen   map[string]int
}

func (p *player) path(rel string) string { return filepath.Join(p.root, filepath.FromSlash(rel)) }

// show writes v to the transcript as JSON.
func (p *player) show(v any) {
	data, err := json.Marshal(v)
	require.NoError(p.t, err)
	dec := json.NewDecoder(bytes.NewReader(bytes.ReplaceAll(data, []byte(p.root), []byte("$ROOT"))))
	dec.UseNumber()
	p.out.WriteString("< " + layout(p.t, dec, "< ") + "\n")
}

// layout reads one JSON value from dec and lays it out for reading: on
// one line when that fits in a hundred columns, else a member a line.
func layout(t *testing.T, dec *json.Decoder, indent string) string {
	tok, err := dec.Token()
	require.NoError(t, err)
	open, isOpen := tok.(json.Delim)
	if !isOpen {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false) // "->" as it reads
		require.NoError(t, enc.Encode(tok))
		return strings.TrimSuffix(b.String(), "\n")
	}
	var members []string
	for dec.More() {
		m := ""
		if open == '{' {
			m = layout(t, dec, indent) + ": "
		}
		members = append(members, m+layout(t, dec, indent+"  "))
	}
	_, err = dec.Token() // the closing bracket
	require.NoError(t, err)
	shut := map[json.Delim]string{'{': "}", '[': "]"}[open]
	if one := string(open) + strings.Join(members, ", ") + shut; len(members) == 0 || len(indent)+len(one) <= 100 && !strings.Contains(one, "\n") {
		return one
	}
	return string(open) + "\n" + indent + "  " + strings.Join(members, ",\n"+indent+"  ") + "\n" + indent + shut
}

// showText writes a text to the transcript, a line each.
func (p *player) showText(text string) {
	for _, line := range strings.SplitAfter(text, "\n") {
		if line != "" {
			p.out.WriteString("| " + strings.TrimSuffix(strconv.Quote(line), "\"")[1:] + "\n")
		}
	}
}

// span reads "L:C-L:C".
func (p *player) span(s string) lsp.Range {
	var r lsp.Range
	_, err := fmt.Sscanf(s, "%d:%d-%d:%d", &r.Start.Line, &r.Start.Character, &r.End.Line, &r.End.Character)
	require.NoError(p.t, err, s)
	return r
}

func (p *player) play(script string) string {
	t, c := p.t, p.c
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("the transcript so far:\n%s", p.out.String())
		}
	})
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			if line != "" {
				p.out.WriteString(line + "\n")
			}
			continue
		}
		p.out.WriteString("> " + line + "\n")
		cmd, rest, _ := strings.Cut(line, " ")
		arg := strings.Fields(rest)
		// The last argument, when it is a Go string.
		quoted := func() string {
			i := strings.Index(rest, "\"")
			require.GreaterOrEqual(t, i, 0, line)
			s, err := strconv.Unquote(rest[i:])
			require.NoError(t, err, line)
			return s
		}
		switch cmd {
		case "initialize":
			var caps lsptest.Capabilities
			var folders []string
			for _, a := range arg {
				switch {
				case a == "plain":
					caps.Plain = true
				case a == "utf-8":
					caps.Encodings = []string{lsp.UTF16, lsp.UTF8}
				case strings.HasPrefix(a, "options="):
					caps.Options = json.RawMessage(strings.TrimPrefix(a, "options="))
				default:
					folders = append(folders, p.path(a))
				}
			}
			c.Settings = func(uri string) any {
				if s, ok := p.settings[uri]; ok {
					return s
				}
				return nil
			}
			p.show(c.Initialize(caps, folders...))
		case "settings":
			if p.settings == nil {
				p.settings = map[string]json.RawMessage{}
			}
			p.settings[lsptest.URI(p.path(arg[0]))] = json.RawMessage(strings.TrimSpace(strings.TrimPrefix(rest, arg[0])))
		case "configuration":
			c.Notify("workspace/didChangeConfiguration", map[string]any{"settings": nil})
		case "open":
			text := ""
			if len(arg) > 1 {
				text = quoted()
			} else {
				data, err := os.ReadFile(p.path(arg[0]))
				require.NoError(t, err)
				text = string(data)
			}
			c.Open(p.path(arg[0]), text)
		case "change":
			d, r := c.Doc(p.path(arg[0])), p.span(arg[1])
			c.Change(p.path(arg[0]), lsptest.Edit{
				Start: lsptest.OffsetOf(d.Text, r.Start, c.Encoding), End: lsptest.OffsetOf(d.Text, r.End, c.Encoding), Text: quoted()})
		case "replace":
			c.Replace(p.path(arg[0]), quoted())
		case "save":
			c.Notify("textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": lsptest.URI(p.path(arg[0]))}})
		case "close":
			c.Close(p.path(arg[0]))
		case "diagnostics":
			p.show(c.Diagnostics(p.path(arg[0])))
		case "actions":
			params := map[string]any{
				"textDocument": map[string]any{"uri": lsptest.URI(p.path(arg[0]))},
				"range":        p.span(arg[1]),
				"context":      map[string]any{"diagnostics": []any{}, "only": arg[2:]},
			}
			p.actions, p.actionFile = nil, arg[0]
			if err := c.Call("textDocument/codeAction", params, &p.actions); err != nil {
				p.show(err.Error())
				break
			}
			p.show(p.actions)
		case "apply":
			n, err := strconv.Atoi(arg[0])
			require.NoError(t, err)
			require.Less(t, n, len(p.actions), line)
			var edits []lsp.TextEdit
			for _, e := range p.actions[n].Edit.Changes {
				edits = append(edits, e...)
			}
			for _, dc := range p.actions[n].Edit.DocumentChanges {
				require.Equal(t, c.Doc(p.path(p.actionFile)).Version, dc.TextDocument.Version, "an edit is for the version it was asked for")
				edits = append(edits, dc.Edits...)
			}
			c.Apply(p.path(p.actionFile), edits)
			p.showText(c.Doc(p.path(p.actionFile)).Text)
		case "format":
			params := map[string]any{
				"textDocument": map[string]any{"uri": lsptest.URI(p.path(arg[0]))},
				"options":      map[string]any{"tabSize": 8, "insertSpaces": false},
			}
			method := "textDocument/formatting"
			if len(arg) > 1 {
				method, params["range"] = "textDocument/rangeFormatting", p.span(arg[1])
			}
			var edits []lsp.TextEdit
			if err := c.Call(method, params, &edits); err != nil {
				p.show(err.Error())
				break
			}
			p.show(edits)
			if len(edits) > 0 {
				c.Apply(p.path(arg[0]), edits)
				p.showText(c.Doc(p.path(arg[0])).Text)
			}
		case "definition", "hover", "signature":
			var pos lsp.Position
			_, err := fmt.Sscanf(arg[1], "%d:%d", &pos.Line, &pos.Character)
			require.NoError(t, err, line)
			params := lsp.DefinitionParams{TextDocument: lsp.TextDocumentIdentifier{URI: lsptest.URI(p.path(arg[0]))}, Position: pos}
			var result json.RawMessage
			method := map[string]string{"definition": "textDocument/definition", "hover": "textDocument/hover", "signature": "textDocument/signatureHelp"}[cmd]
			if err := c.Call(method, params, &result); err != nil {
				p.show(err.Error())
				break
			}
			p.show(result)
		case "references", "highlight":
			var pos lsp.Position
			_, err := fmt.Sscanf(arg[1], "%d:%d", &pos.Line, &pos.Character)
			require.NoError(t, err, line)
			doc := lsp.TextDocumentIdentifier{URI: lsptest.URI(p.path(arg[0]))}
			method, params := "textDocument/documentHighlight", any(lsp.DocumentHighlightParams{TextDocument: doc, Position: pos})
			if cmd == "references" {
				rp := lsp.ReferenceParams{TextDocument: doc, Position: pos}
				rp.Context.IncludeDeclaration = len(arg) > 2 && arg[2] == "decl"
				method, params = "textDocument/references", rp
			}
			var result json.RawMessage
			if err := c.Call(method, params, &result); err != nil {
				p.show(err.Error())
				break
			}
			p.show(result)
		case "complete":
			var pos lsp.Position
			_, err := fmt.Sscanf(arg[1], "%d:%d", &pos.Line, &pos.Character)
			require.NoError(t, err, line)
			params := map[string]any{"textDocument": lsp.TextDocumentIdentifier{URI: lsptest.URI(p.path(arg[0]))}, "position": pos}
			if len(arg) > 2 {
				params["context"] = map[string]any{"triggerKind": lsp.TriggerCharacter, "triggerCharacter": arg[2]}
			}
			var result json.RawMessage
			if err := c.Call("textDocument/completion", params, &result); err != nil {
				p.show(err.Error())
				break
			}
			var list struct {
				Items []json.RawMessage `json:"items"`
			}
			require.NoError(t, json.Unmarshal(result, &list))
			p.items = list.Items
			p.show(result)
		case "resolve":
			n, err := strconv.Atoi(arg[0])
			require.NoError(t, err)
			require.Less(t, n, len(p.items), line)
			var result json.RawMessage
			if err := c.Call("completionItem/resolve", p.items[n], &result); err != nil {
				p.show(err.Error())
				break
			}
			p.show(result)
		case "tokens", "delta":
			doc := lsp.TextDocumentIdentifier{URI: lsptest.URI(p.path(arg[0]))}
			method, params := "textDocument/semanticTokens/full", any(lsp.SemanticTokensParams{TextDocument: doc})
			switch {
			case cmd == "delta":
				prev := p.tokens[arg[0]].id
				if len(arg) > 1 {
					prev = arg[1]
				}
				method, params = "textDocument/semanticTokens/full/delta", lsp.SemanticTokensDeltaParams{TextDocument: doc, PreviousResultID: prev}
			case len(arg) > 1:
				method, params = "textDocument/semanticTokens/range", lsp.SemanticTokensRangeParams{TextDocument: doc, Range: p.span(arg[1])}
			}
			var result json.RawMessage
			if err := c.Call(method, params, &result); err != nil {
				p.show(err.Error())
				break
			}
			var got struct {
				ResultID string                    `json:"resultId"`
				Data     *[]uint32                 `json:"data"`
				Edits    *[]lsp.SemanticTokensEdit `json:"edits"`
			}
			require.NoError(t, json.Unmarshal(result, &got))
			var data []uint32
			switch {
			case got.Data != nil:
				data = *got.Data
				if got.ResultID != "" {
					p.out.WriteString("< all, as " + got.ResultID + "\n")
				}
			case got.Edits != nil:
				// The edits made to the tokens the client has.
				data = slices.Clone(p.tokens[arg[0]].data)
				for i := len(*got.Edits) - 1; i >= 0; i-- {
					e := (*got.Edits)[i]
					p.out.WriteString(fmt.Sprintf("< edit at %d: %d deleted, %d inserted\n", e.Start, e.DeleteCount, len(e.Data)))
					data = slices.Replace(data, e.Start, e.Start+e.DeleteCount, e.Data...)
				}
				p.out.WriteString("< as " + got.ResultID + "\n")
			default:
				p.show(result) // null
				continue
			}
			if got.ResultID != "" {
				if p.tokens == nil {
					p.tokens = map[string]sent{}
				}
				p.tokens[arg[0]] = sent{got.ResultID, data}
			}
			p.out.WriteString(lsptest.DecodeTokens(c.Doc(p.path(arg[0])).Text, data, c.Encoding))
		case "prepare", "rename":
			var pos lsp.Position
			_, err := fmt.Sscanf(arg[1], "%d:%d", &pos.Line, &pos.Character)
			require.NoError(t, err, line)
			doc := lsp.TextDocumentIdentifier{URI: lsptest.URI(p.path(arg[0]))}
			if cmd == "prepare" {
				var result json.RawMessage
				if err := c.Call("textDocument/prepareRename", lsp.PrepareRenameParams{TextDocument: doc, Position: pos}, &result); err != nil {
					p.show(err.Error())
					break
				}
				p.show(result)
				break
			}
			require.Len(t, arg, 3, line)
			var edit *lsp.WorkspaceEdit
			if err := c.Call("textDocument/rename", lsp.RenameParams{TextDocument: doc, Position: pos, NewName: arg[2]}, &edit); err != nil {
				p.show(err.Error())
				break
			}
			p.show(edit)
			var edits []lsp.TextEdit
			for _, e := range edit.Changes {
				edits = append(edits, e...)
			}
			for _, dc := range edit.DocumentChanges {
				require.Equal(t, c.Doc(p.path(arg[0])).Version, dc.TextDocument.Version, "an edit is for the version it was asked for")
				edits = append(edits, dc.Edits...)
			}
			c.Apply(p.path(arg[0]), edits)
			p.showText(c.Doc(p.path(arg[0])).Text)
		case "symbols", "find":
			method, params := "workspace/symbol", any(lsp.WorkspaceSymbolParams{Query: rest})
			if cmd == "symbols" {
				method, params = "textDocument/documentSymbol", lsp.DocumentSymbolParams{TextDocument: lsp.TextDocumentIdentifier{URI: lsptest.URI(p.path(arg[0]))}}
			}
			var result json.RawMessage
			if err := c.Call(method, params, &result); err != nil {
				p.show(err.Error())
				break
			}
			p.show(result)
		case "text":
			p.showText(c.Doc(p.path(arg[0])).Text)
		case "write":
			c.Idle() // nobody is reading the disk now
			write(t, p.root, map[string]string{arg[0]: quoted()})
		case "remove":
			c.Idle()
			require.NoError(t, os.RemoveAll(p.path(arg[0])))
		case "watched":
			kind := map[string]int{"created": 1, "changed": 2, "deleted": 3}[arg[0]]
			require.NotZero(t, kind, line)
			var paths []string
			for _, a := range arg[1:] {
				paths = append(paths, p.path(a))
			}
			c.WatchedFiles(kind, paths...)
		case "received":
			c.Idle()
			all := c.Received(arg[0])
			for _, m := range all[p.seen[arg[0]]:] {
				p.show(m.Params)
			}
			p.seen[arg[0]] = len(all)
		case "watchers":
			c.Idle()
			p.show(c.Watchers())
		case "request":
			var result json.RawMessage
			if err := c.Call(arg[0], json.RawMessage(strings.TrimSpace(strings.TrimPrefix(rest, arg[0]))), &result); err != nil {
				p.show(err.Error())
				break
			}
			p.show(result)
		case "notify":
			c.Notify(arg[0], json.RawMessage(strings.TrimSpace(strings.TrimPrefix(rest, arg[0]))))
		case "shutdown":
			var result json.RawMessage
			require.NoError(t, c.Call("shutdown", nil, &result))
			p.show(result)
		case "exit":
			if err := c.Exit(); err != nil {
				p.show(err.Error())
			} else {
				p.show("the server exited after shutdown")
			}
		default:
			t.Fatalf("unknown script command: %s", line)
		}
	}
	return p.out.String()
}
