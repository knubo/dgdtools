package lsp

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/resolve"
)

// The kinds of completion item the server has (CompletionItemKind).
const (
	ItemFunction = 3
	ItemVariable = 6
	ItemKeyword  = 14
	ItemFile     = 17
	ItemFolder   = 19
	ItemConstant = 21
)

// ItemKindOf returns the protocol's kind for an item: a function, a
// variable for a global, a local and a parameter, a constant for a
// macro, a keyword, and in a path a file or a folder.
func ItemKindOf(k resolve.Kind) int {
	switch k {
	case resolve.Function:
		return ItemFunction
	case resolve.Macro:
		return ItemConstant
	case resolve.Keyword:
		return ItemKeyword
	case resolve.Include, resolve.Object:
		return ItemFile
	case resolve.Directory:
		return ItemFolder
	}
	return ItemVariable
}

// TriggerCharacters are the characters that, typed, make a client ask
// for completions without a word begun: the '>' of "->", the ':' of
// "::", and in a path its opening '"' or '<' and each '/'.
var TriggerCharacters = []string{">", ":", "\"", "/", "<"}

// completion answers textDocument/completion with what can be written
// at the position, which is lint.Document.Complete's answer
// (pkg/resolve) as the protocol's items: each replaces the word, or the
// part of a path, that the cursor is in, and is ordered by how near what
// it names is. Null where the tools have nothing to say. The list is
// complete, so that the client chooses from it as more is typed, unless
// the library had to cut it.
//
// Asked because one of the trigger characters was typed, the server
// answers only where that character begins something: a '>' that is a
// comparison, a ':' of a conditional and a '/' that divides get null,
// not everything that could follow them.
func (s *server) completion(ctx context.Context, p *CompletionParams) (any, error) {
	d, err := s.asked(ctx, p.TextDocument.URI, "completion")
	if d == nil || err != nil {
		return nil, err
	}
	m := NewPositionMapper(d.File.Src, s.caps.encoding())
	c := d.Complete(m.Offset(p.Position))
	if c == nil {
		return nil, nil
	}
	if p.Context != nil && p.Context.TriggerKind == TriggerCharacter && (c.Context == resolve.InCode || c.Context == resolve.InDirective) {
		return nil, nil
	}
	return CompletionContent(c, p.TextDocument.URI, m.Range(c.Start, c.End), CompletionFormat{Markdown: s.caps.completionMarkdown, Snippets: s.caps.snippets}), nil
}

// CompletionFormat is what a client reads of a completion item: its
// documentation as Markdown, and its text as a snippet, which says
// where the cursor is to be afterwards.
type CompletionFormat struct {
	Markdown, Snippets bool
}

// itemData is what an item carries for completionItem/resolve: the
// document it was offered in, and where its declaration is.
type itemData struct {
	URI  string `json:"uri"`
	Path string `json:"path"`
	Off  uint32 `json:"off"`
}

// CompletionContent lays out a completion for a client. An item's label
// is the name, its detail the declaration, its edit the name in place
// of rng, which is what is written of it already. Its documentation is
// what the library has at hand: the kfun table's line, and where the
// declaration is (as HoverContent says it); the comment above a
// declaration is read when the client asks for the item
// (completionItem/resolve), and the item says in its data where. A
// function's name is followed by its parentheses for a client that
// reads snippets, the cursor between them when it takes arguments,
// unless a '(' is there already.
func CompletionContent(c *resolve.Completion, uri string, rng Range, f CompletionFormat) *CompletionList {
	list := &CompletionList{IsIncomplete: c.Incomplete, Items: make([]CompletionItem, 0, len(c.Items))}
	for i := range c.Items {
		it := &c.Items[i]
		item := CompletionItem{Label: it.Name, Kind: ItemKindOf(it.Kind), Detail: it.Detail,
			SortText: strconv.Itoa(it.Rank) + it.Name, TextEdit: &TextEdit{Range: rng, NewText: it.Name}}
		if f.Snippets && it.Call && !c.Called {
			item.InsertTextFormat = FormatSnippet
			if it.Args {
				item.TextEdit.NewText += "($0)"
			} else {
				item.TextEdit.NewText += "()$0"
			}
		}
		item.Documentation = documentation(f.Markdown, it.Doc, it.Origin.String())
		if it.Path != "" {
			item.Data, _ = json.Marshal(itemData{URI: uri, Path: it.Path, Off: it.Off})
		}
		list.Items = append(list.Items, item)
	}
	return list
}

// documentation joins the texts that are there into the documentation
// of an item: a MarkupContent for a client that shows Markdown, else a
// string; nil when there is none.
func documentation(markdown bool, texts ...string) any {
	var parts []string
	for _, text := range texts {
		if text != "" && markdown {
			text = markdownText(text)
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	switch doc := strings.Join(parts, "\n\n"); {
	case doc == "":
		return nil
	case markdown:
		return MarkupContent{Kind: Markdown, Value: doc}
	default:
		return doc
	}
}

// resolveCompletion answers completionItem/resolve: the item, with the
// comment above its declaration before what its documentation says
// already. The comment is read in the text the document's lint reads,
// as a hover's is. An item that names no declaration, or whose document
// is gone, comes back as it was.
func (s *server) resolveCompletion(ctx context.Context, item *CompletionItem) (any, error) {
	var data itemData
	if len(item.Data) == 0 || json.Unmarshal(item.Data, &data) != nil || data.Path == "" {
		return item, nil
	}
	d, err := s.asked(ctx, data.URI, "completion item")
	if d == nil || err != nil {
		return item, nil
	}
	comment := d.Comment(data.Path, data.Off)
	if comment == "" {
		return item, nil
	}
	// What is there already, in the form it was sent in.
	where := ""
	switch doc := item.Documentation.(type) {
	case string:
		where = doc
	case MarkupContent:
		where = doc.Value
	}
	if s.caps.completionMarkdown {
		comment = markdownText(comment)
	}
	doc := strings.TrimSuffix(comment+"\n\n"+where, "\n\n")
	if s.caps.completionMarkdown {
		item.Documentation = MarkupContent{Kind: Markdown, Value: doc}
	} else {
		item.Documentation = doc
	}
	return item, nil
}
