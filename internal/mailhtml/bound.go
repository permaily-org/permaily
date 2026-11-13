package mailhtml

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The budgets of a render (ADR 0026, "Budgets, enforced while the work
// happens"): a tokeniser pass that writes the stream the parser reads and
// bounds the tree the parser will build from it, a reader that stops the
// parse once the deadline has passed, a walk that counts and checks its
// context, and a writer that fails at its cap.

var (
	// errTooLarge is HTML over a limit: the text is shown instead.
	errTooLarge = errors.New("mailhtml: over a limit")
	// errUnreadable is HTML that does not parse back to itself.
	errUnreadable = errors.New("mailhtml: does not parse back to itself")
)

// formattingAtoms are the formatting elements of the HTML standard: the
// parser keeps them in its list of active formatting elements, clones them
// at every new block ("reconstruct the active formatting elements") and
// moves and clones them at a misnested end tag ("adoption agency"). The
// stream renames them (formattingPrefix) so that the parser builds them as
// ordinary elements and clones nothing.
var formattingAtoms = setAtoms(atom.A, atom.B, atom.Big, atom.Code, atom.Em, atom.Font, atom.I,
	atom.Nobr, atom.S, atom.Small, atom.Strike, atom.Strong, atom.Tt, atom.U)

// formattingPrefix names a formatting element in the stream: <b> is written
// <permaily-b>, a name the parser builds as any unknown element.
// restoreFormatting gives the parsed elements their names back. A sender's
// own <permaily-b> becomes a <b>, which it could have written.
const formattingPrefix = "permaily-"

// rawText are the elements whose content x/net/html's tokenizer reads as
// text unless told otherwise.
var rawText = map[atom.Atom]bool{
	atom.Iframe: true, atom.Noembed: true, atom.Noframes: true, atom.Noscript: true, atom.Plaintext: true,
	atom.Script: true, atom.Style: true, atom.Textarea: true, atom.Title: true, atom.Xmp: true,
}

// prepared is the stream the parser reads and the bounds of what it builds
// from it: nodes (the document's node included) and attrBytes, the bytes of
// the keys, values and namespaces its elements' attributes hold, counted
// once per element that holds them.
type prepared struct {
	stream    []byte
	nodes     int
	attrBytes int
}

// documentNodes are what the parser makes at most once a document, charged
// once: the document, its html, head and body (implied or not), and the
// one text token split between the head and the body (its leading white
// space in the head, the rest in the body it implies).
const documentNodes = 5

// prepare is the tokeniser pass over a decoded part (ADR 0026, "Before any
// tree"). It writes the stream the parser reads: every token of b as the
// tokenizer read it, with these changes, none of which changes an element
// or text the walk keeps:
//
//   - comments are left out (the walk drops them);
//   - the formatting elements are renamed (formattingPrefix), so the parser
//     never clones one; a second <a> while one is open is preceded by the
//     end tag of the first, as the parser would end it;
//   - a start tag is written again from its name and attributes, values
//     escaped, attributes whose key does not read back as itself left out;
//   - "</" in the content of an element read as text (<style>, <title>) is
//     written "<\/" (an escape of "/" in CSS) or "&lt;/" (<title>,
//     <textarea>), and "<" in a doctype is left out, so that no byte of the
//     stream but an end tag the pass wrote can end such an element.
//
// So the parser's tokenizer reads the stream as this pass did, except that
// it may read as one text token a run that this pass read as markup: after
// a <noscript>, which the parser reads as text in some insertion modes, and
// after a <style>, <script>, <title> and the like once an <svg> or <math>
// has opened (this pass cannot tell where foreign content ends, so it reads
// them as markup). Such a run ends at an end tag the pass wrote, and is
// charged at least one for each token in it.
//
// The bound is a sum over the stream's tokens, read off the places in
// x/net/html v0.62.0's parse.go that create a node (TestTheBoundsChecklist
// lists them): one for each start tag, text and doctype, two more for a <td>
// or <th> (the tbody and tr it may imply), one more for a <tr> (its tbody),
// two more for a <col> (its colgroup, and the white space a following text
// may leave in it) and one more for a <colgroup>; one for </p> and </br>
// (each makes an element); documentNodes once. No node is cloned: the
// parser's list of active formatting elements holds markers only. The
// attributes: each element holds its start tag's (an <html> or <body> start
// tag past the first copies them onto the first: counted twice), and a
// doctype node its public and system identifiers, within its bytes.
//
// limits applies the limits of a message's part: start tags, attributes per
// element and in all, and the bound of the tree; past any, errTooLarge. The
// output's own pass runs without them. It fails with ctx's error.
func prepare(ctx context.Context, b []byte, limits bool) (prepared, error) {
	z := html.NewTokenizer(bytes.NewReader(b))
	z.SetMaxBuf(len(b) + 1)
	var out bytes.Buffer
	out.Grow(len(b) + len(b)/8)
	p := prepared{nodes: documentNodes}
	starts, attrs, openA := 0, 0, 0
	// foreign is sticky: once an <svg> or <math> has opened, the parser may
	// be in foreign content anywhere after, where <script>, <style> and the
	// other raw text elements are markup; the pass reads them as markup.
	foreign := false
	// raw is the element whose content the next token is, read as text.
	var raw atom.Atom
	for tokens := 0; ; tokens++ {
		if tokens%ctxEvery == 0 {
			if err := ctx.Err(); err != nil {
				return prepared{}, err
			}
		}
		tt := z.Next()
		inRaw := raw
		raw = 0
		switch tt {
		case html.ErrorToken:
			if errors.Is(z.Err(), io.EOF) {
				p.stream = out.Bytes()
				return p, nil
			}
			return prepared{}, errTooLarge
		case html.TextToken:
			p.nodes++
			text := z.Raw()
			switch {
			case inRaw == atom.Title || inRaw == atom.Textarea:
				out.Write(bytes.ReplaceAll(text, []byte("</"), []byte("&lt;/")))
			case inRaw != 0:
				out.Write(bytes.ReplaceAll(text, []byte("</"), []byte(`<\/`)))
			default:
				// Text read in the data state: "<" in it is never followed by
				// "/" and a letter, "!" or "?", which start a token.
				out.Write(text)
			}
		case html.CommentToken:
			// Left out.
		case html.DoctypeToken:
			p.nodes++
			d := z.Raw()
			d = bytes.TrimPrefix(d, []byte("<!"))
			d = bytes.TrimSuffix(d, []byte(">"))
			d = bytes.ReplaceAll(d, []byte("<"), nil)
			p.attrBytes += len(d) // its public and system identifiers are attributes
			out.WriteString("<!")
			out.Write(d)
			out.WriteByte('>')
		case html.StartTagToken, html.SelfClosingTagToken:
			starts++
			name, hasAttr := z.TagName()
			tag := string(name)
			a := atom.Lookup(name)
			var kept []html.Attribute
			n := 0
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				n++
				if keyReadsBack(k) {
					kept = append(kept, html.Attribute{Key: string(k), Val: string(v)})
				}
			}
			attrs += n
			if limits && (starts > contracts.MessageHTMLMaxStartTags || n > contracts.MessageHTMLMaxAttributesPerElement || attrs > contracts.MessageHTMLMaxAttributes) {
				return prepared{}, errTooLarge
			}
			switch a {
			case atom.Svg, atom.Math:
				foreign = true
			}
			if rawText[a] {
				// The parse runs with scripting off, where <noscript> holds
				// markup; in foreign content every raw text element does.
				if foreign || a == atom.Noscript {
					z.NextIsNotRawText()
				} else {
					raw = a
				}
			}
			if formattingAtoms[a] {
				if a == atom.A {
					if openA > 0 {
						out.WriteString("</" + formattingPrefix + "a>")
						openA--
					}
					openA++
				}
				tag = formattingPrefix + tag
			}
			p.nodes += 1 + impliedBy(a)
			held := 0
			out.WriteString("<" + tag)
			for _, at := range kept {
				held += len(at.Key) + len(at.Val)
				out.WriteString(" " + at.Key + `="` + html.EscapeString(at.Val) + `"`)
			}
			if a == atom.Html || a == atom.Body {
				held *= 2
			}
			p.attrBytes += held
			if tt == html.SelfClosingTagToken {
				out.WriteString(" />")
			} else {
				out.WriteByte('>')
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)
			a := atom.Lookup(name)
			switch a {
			case atom.P, atom.Br:
				p.nodes++
			}
			if formattingAtoms[a] {
				if a == atom.A && openA > 0 {
					openA--
				}
				tag = formattingPrefix + tag
			}
			out.WriteString("</" + tag + ">")
		}
		if limits && p.nodes > contracts.MessageHTMLMaxTreeBound {
			return prepared{}, errTooLarge
		}
	}
}

// impliedBy is how many nodes besides its own a start tag can make the
// parser create: the elements it implies, and the text a column group may
// hold.
func impliedBy(a atom.Atom) int {
	switch a {
	case atom.Td, atom.Th:
		return 2 // tbody and tr, in a table
	case atom.Tr:
		return 1 // tbody
	case atom.Col:
		return 2 // colgroup, and the white space a text then leaves in it
	case atom.Colgroup:
		return 1 // the white space a text then leaves in it
	}
	return 0
}

// keyReadsBack says whether an attribute key written as key="…" reads back
// as the same key: no white space, "/", ">", "=", quote or "<", and not
// empty.
func keyReadsBack(k []byte) bool {
	return len(k) > 0 && !bytes.ContainsAny(k, " \t\n\f\r/>=\"'<\x00")
}

// restoreFormatting gives the formatting elements of a parsed stream their
// names back, without recursion.
func restoreFormatting(doc *html.Node) {
	for n := doc; n != nil; n = next(n, doc) {
		if n.Type != html.ElementNode || n.Namespace != "" || !strings.HasPrefix(n.Data, formattingPrefix) {
			continue
		}
		if a := atom.Lookup([]byte(n.Data[len(formattingPrefix):])); formattingAtoms[a] {
			n.DataAtom, n.Data = a, a.String()
		}
	}
}

// next is n's successor in document order within root, nil after the last.
func next(n, root *html.Node) *html.Node {
	if n.FirstChild != nil {
		return n.FirstChild
	}
	for n != root {
		if n.NextSibling != nil {
			return n.NextSibling
		}
		n = n.Parent
	}
	return nil
}

// readChunk is how much the parser's reader gives at a time: the parse
// stops within one read of the deadline.
const readChunk = 4 << 10

// deadlineReader gives at most readChunk bytes a read, and fails once ctx
// has ended.
type deadlineReader struct {
	ctx context.Context
	r   io.Reader
}

func (d *deadlineReader) Read(p []byte) (int, error) {
	if err := d.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > readChunk {
		p = p[:readChunk]
	}
	return d.r.Read(p)
}

// parse builds the tree of b as the HTML standard does with scripting off,
// as it is in the frame, stopped by ctx.
func parse(ctx context.Context, b []byte) (*html.Node, error) {
	doc, err := html.ParseWithOptions(&deadlineReader{ctx: ctx, r: bytes.NewReader(b)}, html.ParseOptionEnableScripting(false))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errTooLarge // the parser's own cap of 512 open elements
	}
	return doc, nil
}

// parseBounded parses b through its prepared stream: what the parser builds
// is within the bounds prepare says, and the formatting elements have their
// names back.
func parseBounded(ctx context.Context, b []byte, limits bool) (*html.Node, error) {
	p, err := prepare(ctx, b, limits)
	if err != nil {
		return nil, err
	}
	doc, err := parse(ctx, p.stream)
	if err != nil {
		return nil, err
	}
	restoreFormatting(doc)
	return doc, nil
}

// cappedWriter keeps what is written up to max bytes, and fails past it.
type cappedWriter struct {
	buf bytes.Buffer
	max int
}

var errOutputCap = errors.New("mailhtml: output over its cap")

func (c *cappedWriter) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.max {
		return 0, errOutputCap
	}
	return c.buf.Write(p)
}

// ctxEvery is how often, in tokens or nodes, a pass checks its context.
const ctxEvery = 1 << 10
