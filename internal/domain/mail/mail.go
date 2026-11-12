// Package mail writes the messages the service sends, from one structure: a
// short list of blocks (a lead, paragraphs, a code, a link, facts, small
// print) that holds the words once. From it come both parts of a message: the
// plain text, and the HTML that mail clients show (ADR 0025).
//
// The HTML is the templates in templates/, which packages/mail writes from its
// React Email components and which are committed: what is in the image is
// that plain HTML, readable in the repository, and no JavaScript. html/template
// escapes every value in its place. Nothing in a template is fetched by the
// mail client: no image, font, stylesheet or script. The only link is the one
// of an action block, whose address the plain text shows too.
//
// It needs nothing but its arguments, like the rest of domain.
package mail

import (
	"embed"
	"html/template"
	"strings"
)

// Kind is a kind of block.
type Kind string

// The kinds of block, each with how its plain text is written.
const (
	// KindLead is the first sentence, which says what the message is: the
	// text as it is.
	KindLead Kind = "lead"
	// KindParagraph is the text as it is.
	KindParagraph Kind = "paragraph"
	// KindNote is small print: the text as it is.
	KindNote Kind = "note"
	// KindCode is a code to type, indented on a line of its own.
	KindCode Kind = "code"
	// KindAction is a link, indented on a line of its own. In the HTML it is
	// a button labelled "Sign in", fixed in the template, with the address
	// under it as text.
	KindAction Kind = "action"
	// KindFacts is a list of facts, one a line, indented: "Label: value".
	KindFacts Kind = "facts"
)

// Block is a part of a message.
type Block struct {
	Kind  Kind   `json:"kind"`
	Text  string `json:"text,omitempty"`
	URL   string `json:"url,omitempty"`
	Facts []Fact `json:"facts,omitempty"`
}

// Fact is one fact of a notice: when, how, which browser. A machine fact,
// such as a time, is set in monospace.
type Fact struct {
	Label   string `json:"label"`
	Value   string `json:"value"`
	Machine bool   `json:"machine,omitempty"`
}

// Lead is the first sentence of a message.
func Lead(text string) Block { return Block{Kind: KindLead, Text: text} }

// Paragraph is a paragraph.
func Paragraph(text string) Block { return Block{Kind: KindParagraph, Text: text} }

// Note is a paragraph of small print.
func Note(text string) Block { return Block{Kind: KindNote, Text: text} }

// Code is a code to type.
func Code(code string) Block { return Block{Kind: KindCode, Text: code} }

// Action is a link to open.
func Action(url string) Block { return Block{Kind: KindAction, URL: url} }

// Facts is a list of facts.
func Facts(facts ...Fact) Block { return Block{Kind: KindFacts, Facts: facts} }

// Text writes the plain text of a message: its blocks, a blank line between
// two, and a line break at the end.
func Text(blocks []Block) string {
	parts := make([]string, len(blocks))
	for i, b := range blocks {
		switch b.Kind {
		case KindCode:
			parts[i] = "    " + b.Text
		case KindAction:
			parts[i] = "    " + b.URL
		case KindFacts:
			lines := make([]string, len(b.Facts))
			for j, f := range b.Facts {
				lines[j] = "    " + f.Label + ": " + f.Value
			}
			parts[i] = strings.Join(lines, "\n")
		default:
			parts[i] = b.Text
		}
	}
	return strings.Join(parts, "\n\n") + "\n"
}

//go:embed templates/*.html
var files embed.FS

var page = template.Must(template.New("").ParseFS(files, "templates/*.html"))

// execute fills a template; a test makes it fail.
var execute = page.ExecuteTemplate

var known = map[Kind]bool{KindLead: true, KindParagraph: true, KindNote: true, KindCode: true, KindAction: true, KindFacts: true}

// HTML writes the HTML of a message, with its subject as the page's title.
// It returns "" when it cannot: a block of a kind it does not know, or a
// template that fails. The message then goes as plain text alone, which says
// everything.
func HTML(subject string, blocks []Block) string {
	for _, b := range blocks {
		if !known[b.Kind] {
			return ""
		}
	}
	var out strings.Builder
	if err := execute(&out, "message", struct {
		Subject string
		Blocks  []Block
	}{subject, blocks}); err != nil {
		return ""
	}
	return out.String()
}
