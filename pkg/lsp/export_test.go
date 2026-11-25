package lsp

// What the tests in package lsp_test reach of the insides.

// LineStarts is the server's own line table of a text.
var LineStarts = lineStarts

// Change applies the changes of one textDocument/didChange to text, as
// the server applies them to a document, and returns the result.
func Change(text []byte, encoding string, changes ...TextDocumentContentChangeEvent) []byte {
	d := &document{text: text, lines: lineStarts(text)}
	for i := range changes {
		d.change(&changes[i], encoding)
	}
	return d.text
}

// LineRange is the lines a range selects for the formatter.
func LineRange(r Range) (first, last int) {
	lr := lineRange(r)
	return lr.First, lr.Last
}

// PathOf and URIOf convert between URIs and paths, and StubOf splits the
// URI of a profile's stub.
var (
	PathOf = pathOf
	URIOf  = uriOf
	StubOf = stubOf
)

// ProgressDelay is how long a scan runs before the client is shown it.
var ProgressDelay = &progressDelay

// MarkdownText writes a plain text as Markdown that shows as the text.
var MarkdownText = markdownText
