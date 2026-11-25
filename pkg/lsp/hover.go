package lsp

import (
	"context"
	"errors"
	"strings"
	"unicode/utf16"

	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/resolve"
)

// CodeLanguage is the language a declaration in a hover is marked as,
// which is the name the editors' grammars have (editors/).
const CodeLanguage = "dgdlpc"

// asked returns the open document at uri as linted in its current
// version, for a question about a place in it; nil for a document
// nothing is said about, and for one that cannot be linted, of which
// the document's diagnostics say why and an editor is not told again at
// each move of the cursor.
func (s *server) asked(ctx context.Context, uri, what string) (*lint.Document, error) {
	d, _, _, err := s.linted(ctx, uri)
	if d == nil || err != nil {
		var modified *rpcError
		if err != nil && !errors.As(err, &modified) {
			s.log.Warn("no "+what, "uri", uri, "err", err)
			err = nil
		}
		return nil, err
	}
	return d, nil
}

// hover answers textDocument/hover: what is known of what the token at
// the position names, which is lint.Document.Hover's answer (pkg/resolve)
// laid out for reading: each declaration as code, the comment above it,
// and where it is. Markdown for a client that shows it, else plain
// text; null where there is nothing to say.
func (s *server) hover(ctx context.Context, p *HoverParams) (any, error) {
	d, err := s.asked(ctx, p.TextDocument.URI, "hover")
	if d == nil || err != nil {
		return nil, err
	}
	here := NewPositionMapper(d.File.Src, s.caps.encoding())
	h := d.Hover(here.Offset(p.Position))
	if h == nil {
		return nil, nil
	}
	rng := here.Range(h.Ref.Start, h.Ref.End)
	return &Hover{Contents: HoverContent(h, s.caps.hoverMarkdown), Range: &rng}, nil
}

// HoverContent lays out a hover for reading, as Markdown or as plain
// text. For each thing the name may be:
//
//	the declaration, as code (or "KIND NAME" where there is none)
//	what is not known (a receiver)
//	the comment above the declaration
//	where it is: "auto object, /sys/auto.c:12", "kfun, DGD 1.7.9",
//	"parameter, /obj/a.c:3"
func HoverContent(h *resolve.Hover, markdown bool) MarkupContent {
	var blocks []string
	for i := range h.Infos {
		info := &h.Infos[i]
		var parts []string
		switch {
		case info.Code != "" && markdown:
			parts = append(parts, "```"+CodeLanguage+"\n"+info.Code+"\n```")
		case info.Code != "":
			parts = append(parts, info.Code)
		case markdown:
			parts = append(parts, info.Kind.String()+" `"+info.Name+"`")
		default:
			parts = append(parts, info.Kind.String()+" "+info.Name)
		}
		where := info.Origin.String()
		switch {
		case info.Class != "":
			where = strings.TrimSuffix("macro ("+info.Class+"), "+where, ", ")
		case info.Kind == resolve.Local:
			where = strings.TrimSuffix("local variable, "+where, ", ")
		case info.Kind == resolve.Parameter:
			where = strings.TrimSuffix("parameter, "+where, ", ")
		}
		for _, text := range []string{info.Note, info.Doc, where} {
			if text != "" && markdown {
				text = markdownText(text)
			}
			if text != "" {
				parts = append(parts, text)
			}
		}
		blocks = append(blocks, strings.Join(parts, "\n\n"))
	}
	if markdown {
		return MarkupContent{Kind: Markdown, Value: strings.Join(blocks, "\n\n---\n\n")}
	}
	return MarkupContent{Kind: PlainText, Value: strings.Join(blocks, "\n\n")}
}

// markdownText writes a plain text as Markdown that shows as the text:
// the characters Markdown reads as marks are escaped, a line break stays
// one, an empty line parts paragraphs, and the indentation of a line is
// kept.
func markdownText(text string) string {
	var b strings.Builder
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		body := strings.TrimLeft(line, " ")
		// Spaces that do not break keep a line where it was written, and
		// keep four of them from making it code.
		b.WriteString(strings.Repeat(" ", len(line)-len(body)))
		for j, r := range body {
			// A mark anywhere; what makes a heading, a list or a rule of
			// a line at its start; the "1." and "1)" of a numbered list.
			if strings.ContainsRune("\\`*_[]<>&~|", r) ||
				j == 0 && strings.ContainsRune("#+-=", r) ||
				(r == '.' || r == ')') && j > 0 && strings.Trim(body[:j], "0123456789") == "" {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		if i < len(lines)-1 {
			if body != "" && lines[i+1] != "" {
				b.WriteString("  ") // a line break, not a new paragraph
			}
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// signatureHelp answers textDocument/signatureHelp: the declarations of
// the function whose call the position is in the arguments of, and the
// parameter the argument there is for, which is
// lint.Document.Signature's answer (pkg/resolve). Null outside a call,
// and where nothing is known of the function: a receiver that is not
// known has no signature.
func (s *server) signatureHelp(ctx context.Context, p *SignatureHelpParams) (any, error) {
	d, err := s.asked(ctx, p.TextDocument.URI, "signature help")
	if d == nil || err != nil {
		return nil, err
	}
	help := d.Signature(NewPositionMapper(d.File.Src, s.caps.encoding()).Offset(p.Position))
	if help == nil {
		return nil, nil
	}
	out := SignatureContent(help, SignatureFormat{Markdown: s.caps.signatureMarkdown, Offsets: s.caps.labelOffsets, Active: s.caps.signatureActive})
	// Asked again for the same call, the signature the user chose stays
	// the one shown.
	if c := p.Context; c != nil && c.IsRetrigger && c.ActiveSignatureHelp != nil {
		was, n := c.ActiveSignatureHelp, c.ActiveSignatureHelp.ActiveSignature
		if n > 0 && n < len(out.Signatures) && len(was.Signatures) == len(out.Signatures) && was.Signatures[n].Label == out.Signatures[n].Label {
			out.ActiveSignature, out.ActiveParameter = n, activeParameter(&help.Signatures[n])
		}
	}
	return out, nil
}

// SignatureFormat is what a client reads of a signature: its
// documentation as Markdown, its parameters as offsets in the label
// (else as their text), an active parameter of its own.
type SignatureFormat struct {
	Markdown, Offsets, Active bool
}

// SignatureContent lays out signature help for a client: each
// declaration as a label with its parameters in it, and under it the
// comment above the declaration and where it is (as HoverContent has
// them). The first is the active one.
func SignatureContent(help *resolve.SignatureHelp, f SignatureFormat) *SignatureHelp {
	out := &SignatureHelp{ActiveParameter: activeParameter(&help.Signatures[0])}
	for i := range help.Signatures {
		sig := &help.Signatures[i]
		info := SignatureInformation{Label: sig.Label, Parameters: []ParameterInformation{}}
		for _, span := range sig.Params {
			if f.Offsets {
				info.Parameters = append(info.Parameters, ParameterInformation{Label: [2]int{utf16Len(sig.Label[:span[0]]), utf16Len(sig.Label[:span[1]])}})
			} else {
				info.Parameters = append(info.Parameters, ParameterInformation{Label: sig.Label[span[0]:span[1]]})
			}
		}
		if f.Active {
			active := activeParameter(sig)
			info.ActiveParameter = &active
		}
		var parts []string
		for _, text := range []string{sig.Doc, sig.Origin.String()} {
			if text != "" && f.Markdown {
				text = markdownText(text)
			}
			if text != "" {
				parts = append(parts, text)
			}
		}
		switch doc := strings.Join(parts, "\n\n"); {
		case doc == "":
		case f.Markdown:
			info.Documentation = MarkupContent{Kind: Markdown, Value: doc}
		default:
			info.Documentation = doc
		}
		out.Signatures = append(out.Signatures, info)
	}
	return out
}

// activeParameter is the parameter of a signature to show as the one
// being written. None is one past the last: there is no other way to
// say it that every client reads.
func activeParameter(sig *resolve.Signature) int {
	if sig.Active < 0 {
		return len(sig.Params)
	}
	return sig.Active
}

// utf16Len returns the number of UTF-16 code units s is written in.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += max(utf16.RuneLen(r), 1)
	}
	return n
}
