// Package mailhtml makes a received message's HTML into a document the web
// app's frame shows (ADR 0026, "Showing a message's HTML"): an allowlist of
// elements, attributes, URL schemes and CSS on golang.org/x/net/html, links
// rewritten to the app's link page with a token bound to their address,
// images that travel in the message rebuilt as data: URIs after their
// structure is read without decoding a pixel, and every budget enforced
// while the work happens.
//
// It is pure: no storage, no network, no clock but the context's deadline.
// It sits beside internal/domain (it needs x/net/html, which domain may not
// import) and below internal/inbox, which finds the HTML part of a message
// and calls it; nothing it is given or makes is ever logged.
//
// The sanitiser is the second line for running and fetching anything: the
// frame's sandbox and its Content-Security-Policy are the first. It is the
// first line for navigation and for how links look, which no sandbox flag
// or CSP directive controls.
package mailhtml

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/permaily-org/permaily/internal/contracts"
)

// InlinePart is a part of the message's multipart/related, named by its
// Content-ID: the type it declared and its decoded bytes. TooLarge says it
// was larger than an image may be, and Data was not read whole.
type InlinePart struct {
	Type     string
	Data     []byte
	TooLarge bool
}

// Input is what a render needs: the HTML part in UTF-8 (DecodeCharset), the
// parts it may name with cid: (keyed by NormaliseContentID), the mailbox and
// message the links lead back to, and the key their tags are made with
// (inbox.LinkKey).
type Input struct {
	HTML    []byte
	Inline  map[string]InlinePart
	Mailbox string
	Message string
	LinkKey []byte
}

// ErrNotShown is HTML that the frame does not show (too large or
// unreadable): it has no links.
var ErrNotShown = errors.New("mailhtml: the HTML is not shown")

// frameStyle is the frame's own style sheet, after the sender's (ADR 0026,
// "CSS"): a white sheet, system fonts, images and tables that fit.
const frameStyle = "html{color-scheme:light;background:#fff;color:#111}" +
	"body{margin:16px;font:16px/1.5 system-ui,sans-serif;overflow-wrap:anywhere}" +
	"img{max-width:100% !important;height:auto}table{max-width:100% !important}a img{border:0}"

// canvasStyle is written on the document's <html> element too: the sheet
// is white whatever the sender's rules say, and html holds every link.
const canvasStyle = "color-scheme:light !important;background:#fff !important;" + holderForced

// Render writes the frame's document for in into w and says what was taken
// out of it. A message whose HTML is over a limit or does not parse back to
// itself has Status HTMLTooLarge or HTMLUnreadable, and nothing is written.
// It fails only with ctx's error (the deadline stops the work, and the
// caller says the view is unavailable) or w's.
func Render(ctx context.Context, in Input, w io.Writer) (contracts.MessageHTML, error) {
	doc, stats, err := render(ctx, in)
	switch {
	case errors.Is(err, errTooLarge):
		return contracts.MessageHTML{Status: contracts.HTMLTooLarge}, nil
	case errors.Is(err, errUnreadable):
		return contracts.MessageHTML{Status: contracts.HTMLUnreadable}, nil
	case err != nil:
		return contracts.MessageHTML{}, err
	}
	if w != nil {
		if _, err := w.Write(doc); err != nil {
			return contracts.MessageHTML{}, err
		}
	}
	return stats, nil
}

// Links are the links of in's HTML, numbered as Render numbers them: the
// token pass, the parse and the walk run with images, CSS and output
// skipped. ErrNotShown when the HTML is over a limit.
func Links(ctx context.Context, in Input) ([]Link, error) {
	w, _, err := walkInput(ctx, in, true)
	if errors.Is(err, errTooLarge) {
		return nil, ErrNotShown
	}
	if err != nil {
		return nil, err
	}
	return w.links, nil
}

func walkInput(ctx context.Context, in Input, linksOnly bool) (*walker, *html.Node, error) {
	if len(in.HTML) > contracts.MessageHTMLMaxBytes {
		return nil, nil, errTooLarge
	}
	if !utf8.Valid(in.HTML) {
		in.HTML = bytes.ToValidUTF8(in.HTML, []byte("�"))
		if len(in.HTML) > contracts.MessageHTMLMaxBytes {
			return nil, nil, errTooLarge
		}
	}
	doc, err := parseBounded(ctx, in.HTML, true)
	if err != nil {
		return nil, nil, err
	}
	wk := &walker{ctx: ctx, in: in, linksOnly: linksOnly}
	body, err := wk.document(doc)
	if err != nil {
		return nil, nil, err
	}
	return wk, body, nil
}

// render makes the document and checks that it parses back to itself.
func render(ctx context.Context, in Input) ([]byte, contracts.MessageHTML, error) {
	wk, body, err := walkInput(ctx, in, false)
	if err != nil {
		return nil, contracts.MessageHTML{}, err
	}
	doc := document(wk, body)
	limit := contracts.MessageHTMLMaxMarkupBytes + wk.uriBytes
	first := &cappedWriter{max: limit}
	if err := html.Render(first, doc); err != nil {
		if errors.Is(err, errOutputCap) {
			return nil, contracts.MessageHTML{}, errTooLarge
		}
		return nil, contracts.MessageHTML{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, contracts.MessageHTML{}, err
	}
	// The output, parsed again with the same options and rendered again,
	// must be the same bytes: a tree x/net/html would itself read otherwise
	// is not sent. It is parsed first through its prepared stream, whose
	// tree is bounded; once that reads back, every formatting element of the
	// output ends at its own end tag as the current node, so the plain
	// parse, the one a browser makes, clones nothing either (ADR 0026,
	// "Budgets").
	for _, bounded := range []bool{true, false} {
		var again *html.Node
		if bounded {
			again, err = parseBounded(ctx, first.buf.Bytes(), false)
		} else {
			again, err = parse(ctx, first.buf.Bytes())
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, contracts.MessageHTML{}, ctx.Err()
			}
			return nil, contracts.MessageHTML{}, errUnreadable
		}
		second := &cappedWriter{max: limit}
		if err := html.Render(second, again); err != nil || !bytes.Equal(first.buf.Bytes(), second.buf.Bytes()) {
			return nil, contracts.MessageHTML{}, errUnreadable
		}
	}
	stats := wk.stats
	stats.Status = contracts.HTMLShown
	return first.buf.Bytes(), stats, nil
}

func element(a atom.Atom, attrs ...html.Attribute) *html.Node {
	return &html.Node{Type: html.ElementNode, DataAtom: a, Data: a.String(), Attr: attrs}
}

// document writes the frame's document around the walked body: the
// doctype, <html lang dir translate="no">, a head with the charset, the
// referrer policy, no translation, the sender's CSS and the frame's own.
func document(wk *walker, body *html.Node) *html.Node {
	doc := &html.Node{Type: html.DocumentNode}
	doc.AppendChild(&html.Node{Type: html.DoctypeNode, Data: "html"})
	var rootAttrs []html.Attribute
	if wk.lang != "" {
		rootAttrs = append(rootAttrs, html.Attribute{Key: "lang", Val: wk.lang})
	}
	if wk.dir != "" {
		rootAttrs = append(rootAttrs, html.Attribute{Key: "dir", Val: wk.dir})
	}
	rootAttrs = append(rootAttrs, html.Attribute{Key: "translate", Val: "no"}, html.Attribute{Key: "style", Val: canvasStyle})
	root := element(atom.Html, rootAttrs...)
	doc.AppendChild(root)
	head := element(atom.Head)
	root.AppendChild(head)
	head.AppendChild(element(atom.Meta, html.Attribute{Key: "charset", Val: "utf-8"}))
	head.AppendChild(element(atom.Meta, html.Attribute{Key: "name", Val: "referrer"}, html.Attribute{Key: "content", Val: "no-referrer"}))
	head.AppendChild(element(atom.Meta, html.Attribute{Key: "name", Val: "google"}, html.Attribute{Key: "content", Val: "notranslate"}))
	if len(wk.sheets) > 0 {
		sender := element(atom.Style)
		sender.AppendChild(&html.Node{Type: html.TextNode, Data: strings.Join(wk.sheets, "\n")})
		head.AppendChild(sender)
	}
	own := element(atom.Style)
	own.AppendChild(&html.Node{Type: html.TextNode, Data: frameStyle})
	head.AppendChild(own)
	body.Attr = append(wk.body, html.Attribute{Key: "style", Val: holderForced}) // body holds every link
	root.AppendChild(body)
	sortAttributes(doc)
	return doc
}

// sortAttributes writes every element's attributes in the order of their
// names: x/net/html sorts the attributes of a formatting element it keeps
// in its list (the Noah's Ark clause), so an <a> written in another order
// would not parse back to itself.
func sortAttributes(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			slices.SortStableFunc(c.Attr, func(a, b html.Attribute) int { return strings.Compare(a.Key, b.Key) })
		}
		sortAttributes(c)
	}
}
