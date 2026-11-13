package mailhtml

import (
	"context"
	"encoding/base64"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The walk of ADR 0026 ("The sanitiser"): a new tree built from nothing,
// copying only what the allowlist names; no node of the input ever moves
// into the output. Render and Links share it, so a link's number is the
// frame's by construction.

var keptElements = setAtoms(
	atom.A, atom.Abbr, atom.Address, atom.Article, atom.Aside, atom.B, atom.Bdi, atom.Bdo, atom.Big,
	atom.Blockquote, atom.Br, atom.Caption, atom.Center, atom.Cite, atom.Code, atom.Col, atom.Colgroup,
	atom.Dd, atom.Del, atom.Details, atom.Dfn, atom.Div, atom.Dl, atom.Dt, atom.Em, atom.Figcaption,
	atom.Figure, atom.Font, atom.Footer, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Header,
	atom.Hr, atom.I, atom.Img, atom.Ins, atom.Kbd, atom.Li, atom.Main, atom.Mark, atom.Nav, atom.Ol, atom.P,
	atom.Pre, atom.Q, atom.Rp, atom.Rt, atom.Ruby, atom.S, atom.Samp, atom.Section, atom.Small, atom.Span,
	atom.Strike, atom.Strong, atom.Sub, atom.Summary, atom.Sup, atom.Table, atom.Tbody, atom.Td, atom.Tfoot,
	atom.Th, atom.Thead, atom.Time, atom.Tr, atom.Tt, atom.U, atom.Ul, atom.Var, atom.Wbr,
)

// droppedElements go with everything inside them. Comments, doctypes and
// every element outside the HTML namespace go too.
var droppedElements = setAtoms(
	atom.Script, atom.Noscript, atom.Template, atom.Style, atom.Title, atom.Head, atom.Iframe, atom.Frame,
	atom.Frameset, atom.Object, atom.Embed, atom.Applet, atom.Param, atom.Svg, atom.Math, atom.Canvas,
	atom.Audio, atom.Video, atom.Source, atom.Track, atom.Picture, atom.Map, atom.Area, atom.Link, atom.Meta,
	atom.Base, atom.Input, atom.Select, atom.Option, atom.Optgroup, atom.Textarea, atom.Button,
	atom.Datalist, atom.Output, atom.Progress, atom.Meter, atom.Dialog, atom.Slot, atom.Xmp, atom.Plaintext,
	atom.Noembed, atom.Noframes, atom.Listing,
)

// droppedNames are elements dropped with everything inside that x/net/html's
// atom table does not know, by name: <portal> and <fencedframe> embed
// another document, <permission> and <geolocation> ask the browser for a
// capability.
var droppedNames = set("portal", "fencedframe", "permission", "geolocation")

// dropped says whether an element of the HTML namespace goes with
// everything inside it.
func dropped(n *html.Node) bool {
	if n.DataAtom != 0 {
		return droppedElements[n.DataAtom]
	}
	return droppedNames[strings.ToLower(n.Data)]
}

func setAtoms(as ...atom.Atom) map[atom.Atom]bool {
	m := make(map[atom.Atom]bool, len(as))
	for _, a := range as {
		m[a] = true
	}
	return m
}

// Link is a kept link of a message's HTML: the path of the app's link page
// it is written as (with its number and tag), its canonical address, its
// host as the WHATWG URL's host attribute writes it, and the host its text
// named when that is another one.
type Link struct {
	Path  string
	URL   string
	Host  string
	Shown string
}

// imageBudget is what the images of one message have used.
type imageBudget struct {
	shown, bytes, pixels int
}

type walker struct {
	ctx       context.Context
	in        Input
	linksOnly bool

	ticks    int
	elements int
	stats    contracts.MessageHTML
	links    []Link
	css      cssBudget
	styleIn  int // bytes of <style> read
	sheets   []string
	images   imageBudget
	uriBytes int // bytes of data: URIs written

	lang, dir string
	body      []html.Attribute
	bodySeen  bool

	// holds are the elements of the input that hold a link (at any depth):
	// their style is written so that nothing hides or moves it.
	holds map[*html.Node]bool
}

// tick counts a node visited and checks the context every ctxEvery nodes.
func (w *walker) tick() error {
	if w.css.err != nil {
		return w.css.err // the CSS work stopped with the context
	}
	w.ticks++
	if w.ticks%ctxEvery == 0 {
		return w.ctx.Err()
	}
	return nil
}

// document walks a parsed document into the children of a new body.
func (w *walker) document(doc *html.Node) (*html.Node, error) {
	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	w.css.ctx = w.ctx
	if !w.linksOnly {
		w.holds = map[*html.Node]bool{}
		if _, err := w.markHolders(doc); err != nil {
			return nil, err
		}
	}
	if err := w.walk(doc, body, 0, false); err != nil {
		return nil, err
	}
	if w.css.err != nil {
		return nil, w.css.err
	}
	w.stats.RemoteImages += w.css.remoteImages
	w.stats.RemoteOther += w.css.remoteOther
	w.stats.Links = len(w.links)
	return body, nil
}

// markHolders marks, in w.holds, every element under n that holds an a
// whose href is http or https (a link the walk may keep: a few more than it
// keeps, which only writes the style of an ancestor on a few more), and
// says whether n holds one. What the walk drops is not looked into.
func (w *walker) markHolders(n *html.Node) (bool, error) {
	found := false
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if err := w.tick(); err != nil {
			return false, err
		}
		if c.Type != html.ElementNode || c.Namespace != "" || dropped(c) {
			continue
		}
		if c.DataAtom == atom.A {
			href, _ := attr(c, "href")
			if h := strings.ToLower(whatwgTrim(href)); strings.HasPrefix(h, "http:") || strings.HasPrefix(h, "https:") {
				found = true
			}
		}
		inner, err := w.markHolders(c)
		if err != nil {
			return false, err
		}
		if inner {
			w.holds[c] = true
			found = true
		}
	}
	return found, nil
}

func (w *walker) newElement(parent *html.Node, a atom.Atom, name string, depth int) (*html.Node, error) {
	w.elements++
	if w.elements > contracts.MessageHTMLMaxElements || depth >= contracts.MessageHTMLMaxDepth {
		return nil, errTooLarge
	}
	n := &html.Node{Type: html.ElementNode, DataAtom: a, Data: name}
	parent.AppendChild(n)
	return n, nil
}

func appendText(parent *html.Node, s string) {
	if s == "" {
		return
	}
	if last := parent.LastChild; last != nil && last.Type == html.TextNode {
		last.Data += s
		return
	}
	parent.AppendChild(&html.Node{Type: html.TextNode, Data: s})
}

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// walk copies the children of src that the allowlist keeps into dst. depth
// is dst's depth in the message's content; inLink says dst is inside a kept
// link.
func (w *walker) walk(src, dst *html.Node, depth int, inLink bool) error {
	for c := src.FirstChild; c != nil; c = c.NextSibling {
		if err := w.tick(); err != nil {
			return err
		}
		switch c.Type {
		case html.TextNode:
			appendText(dst, c.Data)
		case html.ElementNode:
			if err := w.element(c, dst, depth, inLink); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *walker) element(c, dst *html.Node, depth int, inLink bool) error {
	if c.Namespace != "" {
		return w.countDropped(c)
	}
	a := c.DataAtom
	switch {
	case a == atom.Style:
		w.style(c)
		return w.css.err
	case a == atom.Head:
		// Of the message's head, only its style sheets are read.
		for h := c.FirstChild; h != nil; h = h.NextSibling {
			if h.Type == html.ElementNode && h.Namespace == "" && h.DataAtom == atom.Style {
				w.style(h)
				if w.css.err != nil {
					return w.css.err
				}
			} else if err := w.countDropped(h); err != nil {
				return err
			}
		}
		return nil
	case dropped(c):
		return w.countDropped(c)
	case a == atom.Html:
		if v, ok := attr(c, "lang"); ok && validLang(v) {
			w.lang = v
		}
		if v, ok := attr(c, "dir"); ok && validDir(v) {
			w.dir = strings.ToLower(v)
		}
		return w.walk(c, dst, depth, inLink)
	case a == atom.Body:
		if !w.bodySeen {
			w.bodySeen = true
			for _, k := range []string{"bgcolor", "text"} {
				if v, ok := attr(c, k); ok {
					if col, ok := colour(v); ok {
						w.body = append(w.body, html.Attribute{Key: k, Val: col})
					}
				}
			}
			w.countAttributes(c)
		}
		return w.walk(c, dst, depth, inLink)
	case a == atom.A:
		return w.link(c, dst, depth, inLink)
	case a == atom.Img:
		return w.image(c, dst, depth, inLink)
	case !keptElements[a]:
		w.countAttributes(c)
		return w.walk(c, dst, depth, inLink) // unwrapped
	}
	name := c.Data
	switch {
	case a == atom.P && needsDiv(c):
		a, name = atom.Div, "div"
	case a == atom.Details || a == atom.Summary:
		// Plain containers: what they hold is always shown, whatever open
		// says.
		a, name = atom.Div, "div"
	}
	n, err := w.newElement(dst, a, name, depth)
	if err != nil {
		return err
	}
	w.countAttributes(c)
	n.Attr = w.attributes(c, c.DataAtom, inLink)
	if name == "div" && c.DataAtom == atom.P {
		n.Attr = prependStyle(n.Attr, "margin-top:1em;margin-bottom:1em")
	}
	return w.walk(c, n, depth+1, inLink)
}

// closesP are the elements whose start tag closes an open <p> in button
// scope in a document without quirks (the frame's), table included.
var closesP = setAtoms(
	atom.Address, atom.Article, atom.Aside, atom.Blockquote, atom.Center, atom.Details, atom.Dialog, atom.Dir,
	atom.Div, atom.Dl, atom.Fieldset, atom.Figcaption, atom.Figure, atom.Footer, atom.Header, atom.Hgroup,
	atom.Main, atom.Menu, atom.Nav, atom.Ol, atom.P, atom.Search, atom.Section, atom.Summary, atom.Ul,
	atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Pre, atom.Listing, atom.Form, atom.Plaintext,
	atom.Table, atom.Hr, atom.Xmp, atom.Li, atom.Dd, atom.Dt,
)

// buttonScope are the elements a <p> is not closed across.
var buttonScope = setAtoms(atom.Applet, atom.Caption, atom.Html, atom.Table, atom.Td, atom.Th, atom.Marquee, atom.Object, atom.Template, atom.Button)

// needsDiv says whether a <p> holds, once the walk has unwrapped what it
// unwraps, an element that would close it when the output is parsed again:
// a table, from a message parsed in quirks mode, or a block inside an
// element the walk unwraps (<p><marquee><div>). Such a <p> is written as a
// <div> with a paragraph's margins, so the output parses back to itself.
func needsDiv(p *html.Node) bool {
	budget := 4096
	var look func(n *html.Node) bool
	look = func(n *html.Node) bool {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if budget--; budget < 0 {
				return true
			}
			if c.Type != html.ElementNode || c.Namespace != "" || dropped(c) {
				continue
			}
			kept := keptElements[c.DataAtom]
			if kept && closesP[c.DataAtom] {
				return true
			}
			if kept && buttonScope[c.DataAtom] {
				continue
			}
			if look(c) {
				return true
			}
		}
		return false
	}
	return look(p)
}

// style reads a <style> element through the CSS filter, within the budget
// of all of them together; past it the CSS goes and only its addresses are
// counted, by a pass that keeps nothing. Links reads no CSS at all.
func (w *walker) style(c *html.Node) {
	if w.linksOnly {
		return
	}
	var b strings.Builder
	for t := c.FirstChild; t != nil; t = t.NextSibling {
		if t.Type == html.TextNode {
			b.WriteString(t.Data)
		}
	}
	text := b.String()
	w.styleIn += len(text)
	if w.styleIn > contracts.MessageHTMLMaxStyleBytes {
		w.css.countDiscarded(text)
		return
	}
	if out := w.css.filterStylesheet(text); out != "" {
		w.sheets = append(w.sheets, out)
	}
}

// countDropped counts the remote references of a subtree that goes.
func (w *walker) countDropped(n *html.Node) error {
	if err := w.tick(); err != nil {
		return err
	}
	if n.Type != html.ElementNode {
		return nil
	}
	w.countAttributes(n)
	if n.DataAtom == atom.Img && n.Namespace == "" {
		if v, ok := attr(n, "src"); ok && looksRemote(v) {
			w.stats.RemoteImages++
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if err := w.countDropped(c); err != nil {
			return err
		}
	}
	return nil
}

// countAttributes counts the addresses an element's attributes would have
// fetched (none is kept): images in RemoteImages, the rest in RemoteOther.
func (w *walker) countAttributes(n *html.Node) {
	for _, at := range n.Attr {
		v := at.Val
		switch strings.ToLower(at.Key) {
		case "src":
			if !looksRemote(v) || n.DataAtom == atom.Img && n.Namespace == "" {
				continue
			}
			if n.DataAtom == atom.Input || n.DataAtom == atom.Img {
				w.stats.RemoteImages++
			} else {
				w.stats.RemoteOther++
			}
		case "srcset":
			if strings.Contains(strings.ToLower(v), "http") || strings.Contains(v, "//") {
				w.stats.RemoteImages++
			}
		case "poster", "background", "lowsrc", "dynsrc":
			if looksRemote(v) {
				w.stats.RemoteImages++
			}
		case "href", "xlink:href":
			if (n.DataAtom == atom.Link || n.DataAtom == atom.Base || n.Namespace != "") && looksRemote(v) {
				w.stats.RemoteOther++
			}
		case "data":
			if n.DataAtom == atom.Object && looksRemote(v) {
				w.stats.RemoteOther++
			}
		}
	}
}

// The patterns attribute values must match.
var (
	reLang   = regexp.MustCompile(`^[A-Za-z0-9-]{1,35}$`)
	reToken  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	reNumber = regexp.MustCompile(`^([0-9]{1,5}(\.[0-9]{1,3})?)(%|px)?$`)
	reInt    = regexp.MustCompile(`^[0-9]{1,4}$`)
	reHex    = regexp.MustCompile(`^#?([0-9A-Fa-f]{3}|[0-9A-Fa-f]{4}|[0-9A-Fa-f]{6}|[0-9A-Fa-f]{8})$`)
	reName   = regexp.MustCompile(`^[A-Za-z]{1,20}$`)
	reFace   = regexp.MustCompile(`^[A-Za-z0-9 ,'_-]{1,200}$`)
	reSize   = regexp.MustCompile(`^[+-]?[1-7]$`)
	reDate   = regexp.MustCompile(`^[0-9TZ:+. -]{1,40}$`)
	reAlign  = regexp.MustCompile(`^[A-Za-z]{1,12}$`)
	reOLType = regexp.MustCompile(`^[1aAiI]$`)
	reScope  = regexp.MustCompile(`^(?i:row|col|rowgroup|colgroup)$`)
)

func validLang(v string) bool { return reLang.MatchString(v) }

func validDir(v string) bool {
	switch strings.ToLower(v) {
	case "ltr", "rtl", "auto":
		return true
	}
	return false
}

// colour is a colour attribute written again: # and hexadecimal digits, or
// a name of letters.
func colour(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if m := reHex.FindStringSubmatch(v); m != nil && (strings.HasPrefix(v, "#") || len(m[1]) == 6) {
		return "#" + strings.ToLower(m[1]), true
	}
	if reName.MatchString(v) {
		return strings.ToLower(v), true
	}
	return "", false
}

// tokens keeps the [A-Za-z0-9_-] tokens of a list.
func tokens(v string, max int) (string, bool) {
	var kept []string
	for _, t := range strings.Fields(v) {
		if reToken.MatchString(t) && len(kept) < max {
			kept = append(kept, t)
		}
	}
	return strings.Join(kept, " "), len(kept) > 0
}

// number is a non-negative number or percentage, "px" dropped.
func number(v string) (string, bool) {
	m := reNumber.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return "", false
	}
	if m[3] == "%" {
		return m[1] + "%", true
	}
	return m[1], true
}

// attributeRule checks and writes one attribute of a kept element.
type attributeRule func(v string) (string, bool)

func matching(re *regexp.Regexp) attributeRule {
	return func(v string) (string, bool) {
		v = strings.TrimSpace(v)
		return v, re.MatchString(v)
	}
}

func anyText(v string) (string, bool) { return v, true }

func boolean(string) (string, bool) { return "", true }

var commonAttributes = map[string]attributeRule{
	"dir":    func(v string) (string, bool) { return strings.ToLower(v), validDir(v) },
	"lang":   matching(reLang),
	"title":  anyText,
	"align":  matching(reAlign),
	"valign": matching(reAlign),
	"class":  func(v string) (string, bool) { return tokens(v, 32) },
	"id":     func(v string) (string, bool) { v = strings.TrimSpace(v); return v, reToken.MatchString(v) },
}

var tableCellAttributes = map[string]attributeRule{
	"width": number, "height": number, "border": number, "cellpadding": number, "cellspacing": number,
	"bgcolor": colour, "colspan": matching(reInt), "rowspan": matching(reInt), "nowrap": boolean,
	"scope": matching(reScope), "headers": func(v string) (string, bool) { return tokens(v, 32) },
}

var elementAttributes = map[atom.Atom]map[string]attributeRule{
	atom.Table:    tableCellAttributes,
	atom.Tr:       tableCellAttributes,
	atom.Td:       tableCellAttributes,
	atom.Th:       tableCellAttributes,
	atom.Col:      {"span": matching(reInt), "width": number},
	atom.Colgroup: {"span": matching(reInt), "width": number},
	atom.Ol:       {"start": matching(reInt), "type": matching(reOLType), "reversed": boolean},
	atom.Li:       {"value": matching(reInt)},
	atom.Font:     {"color": colour, "face": matching(reFace), "size": matching(reSize)},
	atom.Time:     {"datetime": matching(reDate)},
	atom.Img: {"alt": anyText, "width": number, "height": number, "border": number,
		"hspace": number, "vspace": number},
}

// attributes filters the attributes of a kept element (not a link's own
// href, nor an image's src, which have rules of their own). A style is
// filtered as CSS, and inside a link written with what keeps the link
// visible.
func (w *walker) attributes(c *html.Node, a atom.Atom, inLink bool) []html.Attribute {
	var out []html.Attribute
	var style []cssDecl
	hasStyle := false
	for _, at := range c.Attr {
		if at.Namespace != "" {
			continue
		}
		key := strings.ToLower(at.Key)
		if key == "style" {
			if len(at.Val) <= contracts.MessageHTMLMaxStyleAttributeBytes && !w.linksOnly {
				style = w.css.filterStyleAttribute(at.Val, c.Data)
				hasStyle = true
			}
			continue
		}
		if len(at.Val) > contracts.MessageHTMLMaxAttributeBytes {
			continue
		}
		rule, ok := commonAttributes[key]
		if !ok {
			rule, ok = elementAttributes[a][key]
		}
		if !ok {
			continue
		}
		if v, ok := rule(at.Val); ok {
			out = append(out, html.Attribute{Key: key, Val: v})
		}
	}
	if w.linksOnly {
		return out
	}
	var s strings.Builder
	switch {
	case inLink:
		s.WriteString(descendantStyle(style))
	case w.holds[c]:
		s.WriteString(holderStyle(style))
	default:
		writeDecls(&s, style, false)
		if keepsHeight(style) {
			if s.Len() > 0 {
				s.WriteByte(';')
			}
			s.WriteString("overflow:visible!important")
		}
	}
	if s.Len() > 0 || hasStyle && inLink {
		out = append(out, html.Attribute{Key: "style", Val: s.String()})
	}
	if w.holds[c] && !inLink {
		out = slices.DeleteFunc(out, func(a html.Attribute) bool { return a.Key == "height" })
	}
	return out
}

// holderOwned are the declarations an element that holds a link does not
// keep of its own: a display of none, an overflow, and a height or a
// maximum height (which, on a box whose display a rule may change, would
// hold it smaller than what it holds). holderForced says what holds
// instead.
func holderOwned(d cssDecl) bool {
	switch d.prop {
	case "height", "max-height", "max-width", "overflow", "overflow-x", "overflow-y":
		return true
	}
	return d.prop == "display" && d.value == "none"
}

// holderStyle is the style of an element that holds a link (ADR 0026,
// "CSS", "The principle, on a link's ancestors"): its own declarations but
// those, then holderForced.
func holderStyle(decls []cssDecl) string {
	var s strings.Builder
	writeDecls(&s, slices.DeleteFunc(decls, holderOwned), false)
	if s.Len() > 0 {
		s.WriteByte(';')
	}
	s.WriteString(holderForced)
	return s.String()
}

// holderForced is attached to every element that holds a link, html and
// body included, with !important, which wins over every rule of a style
// sheet whatever its selector: nothing clips what the element holds, no
// height or maximum height holds it shorter than its content, no maximum
// width at all, and no width narrower than the content's least width
// (min-width wins over width), so a link inside keeps its box and lays
// over nothing.
const holderForced = "overflow:visible!important;height:auto!important;max-height:none!important;" +
	"max-width:none!important;min-width:min-content!important"

// keepsHeight says whether a style keeps a height as a length: the box then
// shows what overflows it, whatever a rule says, so a height cannot hide
// what is inside (a cell made a block, with a height of 0).
func keepsHeight(decls []cssDecl) bool {
	for _, d := range decls {
		if (d.prop == "height" || d.prop == "max-height" || d.prop == "min-height") && d.value != "auto" && d.value != "none" {
			return true
		}
	}
	return false
}

func prependStyle(attrs []html.Attribute, decls string) []html.Attribute {
	for i, a := range attrs {
		if a.Key == "style" {
			if a.Val != "" {
				decls += ";" + a.Val
			}
			attrs[i].Val = decls
			return attrs
		}
	}
	return append(attrs, html.Attribute{Key: "style", Val: decls})
}

// linkStyle is what every kept link carries, after its own declarations, so
// nothing the sender wrote changes how a link looks: underlined, blue on
// white, in its own box that holds what it draws and fits its container,
// and positioned with the largest z-index, so it is painted above
// everything the message draws (which can neither position a box nor make
// a stacking context) and takes the click at every point of its box (ADR
// 0026, "What each link carries").
const linkStyle = "display:inline-block!important;max-width:100%!important;overflow:hidden!important;" +
	"color:#1a4fd6!important;background-color:#fff!important;text-decoration:underline!important;" +
	"font-size:max(1em, 12px)!important;line-height:normal!important;letter-spacing:normal!important;" +
	"word-spacing:normal!important;text-indent:0!important;vertical-align:baseline!important;" +
	"width:auto!important;height:auto!important;min-width:0!important;min-height:0!important;" +
	"max-height:none!important;margin:0!important;padding:0!important;border:0!important;" +
	"position:relative!important;z-index:2147483647!important"

// linkOwned are the properties linkStyle sets: the link's own are dropped.
var linkOwned = set("display", "max-width", "overflow", "overflow-x", "overflow-y", "color", "background",
	"background-color", "text-decoration", "text-decoration-line", "text-decoration-color", "text-decoration-style",
	"font", "font-size", "line-height", "letter-spacing", "word-spacing", "text-indent", "vertical-align",
	"width", "height", "min-width", "min-height", "max-height", "margin", "margin-top", "margin-right",
	"margin-bottom", "margin-left", "padding", "padding-top", "padding-right", "padding-bottom", "padding-left",
	"border", "border-top", "border-right", "border-bottom", "border-left", "border-width", "border-top-width",
	"border-right-width", "border-bottom-width", "border-left-width")

// descendantOwned are the properties written on everything inside a link:
// its text in the link's colour and size, on no colour of its own.
var descendantOwned = set("color", "background", "background-color", "font-size", "font", "line-height",
	"display", "letter-spacing", "word-spacing", "text-indent", "vertical-align")

// descendantStyle is the style of an element inside a link: its own
// declarations but those the link decides, then the link's colour, size and
// a display of its own that a rule cannot set to none.
func descendantStyle(decls []cssDecl) string {
	var kept []cssDecl
	display := "revert"
	for _, d := range decls {
		if d.prop == "display" && d.value != "none" {
			display = d.value
		}
		if !descendantOwned[d.prop] {
			kept = append(kept, d)
		}
	}
	var s strings.Builder
	writeDecls(&s, kept, false)
	if s.Len() > 0 {
		s.WriteByte(';')
	}
	s.WriteString("color:inherit!important;background-color:transparent!important;font-size:inherit!important;" +
		"line-height:inherit!important;letter-spacing:normal!important;word-spacing:normal!important;" +
		"text-indent:0!important;vertical-align:baseline!important;display:")
	s.WriteString(display)
	s.WriteString("!important")
	return s.String()
}

// maxLinkText bounds the text of a link read for the host it names.
const maxLinkText = 2 << 10

// textOf is the text inside n, at most max bytes.
func textOf(n *html.Node, max int) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil && b.Len() < max; c = c.NextSibling {
			switch {
			case c.Type == html.TextNode:
				b.WriteString(c.Data)
			case c.Type == html.ElementNode && c.Namespace == "" && !dropped(c):
				walk(c)
			}
		}
	}
	walk(n)
	s := b.String()
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// link keeps an http or https link to a DNS name as a link to the app's
// link page, opened in a new tab; writes a mailto: or tel: link as its text
// with the address after it when the text does not say it; and writes every
// other one, a link inside a link, and links past the thousandth, as text.
func (w *walker) link(c, dst *html.Node, depth int, inLink bool) error {
	w.countAttributes(c)
	href, _ := attr(c, "href")
	href = whatwgTrim(href)
	if inLink {
		return w.walk(c, dst, depth, inLink)
	}
	lower := strings.ToLower(href)
	if strings.HasPrefix(lower, "mailto:") || strings.HasPrefix(lower, "tel:") {
		if err := w.walk(c, dst, depth, inLink); err != nil {
			return err
		}
		addr := href[strings.IndexByte(href, ':')+1:]
		if i := strings.IndexByte(addr, '?'); i >= 0 {
			addr = addr[:i]
		}
		if u, err := url.PathUnescape(addr); err == nil {
			addr = u
		}
		addr = strings.TrimSpace(addr)
		if addr != "" && len(addr) <= contracts.MessageHTMLMaxURLBytes && !strings.Contains(textOf(c, maxLinkText), addr) {
			appendText(dst, " ["+addr+"]")
		}
		return nil
	}
	target, ok := parseLink(href)
	if !ok || len(w.links) >= contracts.MessageHTMLMaxLinks {
		return w.walk(c, dst, depth, inLink)
	}
	n := len(w.links)
	tag := linkTag(w.in.LinkKey, w.in.Message, n, target.URL)
	link := Link{
		Path: "/inbox/" + url.PathEscape(w.in.Mailbox) + "/" + url.PathEscape(w.in.Message) + "/links/" + strconv.Itoa(n) + "." + tag,
		URL:  target.URL, Host: target.Host,
	}
	if shown := textHost(textOf(c, maxLinkText)); shown != "" && shown != target.Name {
		link.Shown = shown
	}
	w.links = append(w.links, link)
	a, err := w.newElement(dst, atom.A, "a", depth)
	if err != nil {
		return err
	}
	attrs := []html.Attribute{
		{Key: "href", Val: link.Path}, {Key: "target", Val: "_blank"},
		{Key: "rel", Val: "noopener noreferrer"}, {Key: "title", Val: target.URL},
	}
	for _, at := range c.Attr {
		key := strings.ToLower(at.Key)
		if at.Namespace != "" || key == "title" || len(at.Val) > contracts.MessageHTMLMaxAttributeBytes {
			continue
		}
		if rule, ok := commonAttributes[key]; ok {
			if v, ok := rule(at.Val); ok {
				attrs = append(attrs, html.Attribute{Key: key, Val: v})
			}
		}
	}
	if !w.linksOnly {
		var s strings.Builder
		if v, ok := attr(c, "style"); ok {
			var own []cssDecl
			for _, d := range w.css.filterStyleAttribute(v, "a") {
				if !linkOwned[d.prop] {
					own = append(own, d)
				}
			}
			writeDecls(&s, own, false)
		}
		if s.Len() > 0 {
			s.WriteByte(';')
		}
		s.WriteString(linkStyle)
		attrs = append(attrs, html.Attribute{Key: "style", Val: s.String()})
	}
	a.Attr = attrs
	return w.walk(c, a, depth+1, true)
}

// The words of the box drawn instead of an image not shown.
var omittedWords = map[string]string{
	"remote":        "Remote image not loaded",
	omitTooLarge:    "Image too large",
	omitAnimated:    "Animated image not shown",
	omitUnsupported: "Image not shown",
}

const boxStyle = "display:inline-block!important;max-width:100%!important;box-sizing:border-box!important;" +
	"border:1px solid #9a9a9a!important;padding:2px 6px!important;margin:2px 0!important;" +
	"background:#f4f4f4!important;color:#444!important;font:13px/1.4 system-ui, sans-serif!important;" +
	"text-decoration:none!important;overflow-wrap:anywhere!important"

// image shows an image that travels in the message as a data: URI rebuilt
// from its bytes, when its structure reads and it is within the limits, and
// draws a box saying why otherwise. Remote images are counted, never
// loaded: a box, or nothing for one made to track (0 to 2 pixels).
func (w *walker) image(c, dst *html.Node, depth int, inLink bool) error {
	if w.linksOnly {
		return nil
	}
	w.countAttributes(c) // srcset, lowsrc, dynsrc: never kept
	src, _ := attr(c, "src")
	src = whatwgTrim(src)
	alt, _ := attr(c, "alt")
	if len(alt) > contracts.MessageHTMLMaxAttributeBytes {
		alt = ""
	}
	lower := strings.ToLower(src)
	var data []byte
	switch {
	case strings.HasPrefix(lower, "cid:"):
		part, ok := w.in.Inline[contentID(src[4:])]
		switch {
		case !ok:
			return w.omitted(dst, depth, omitUnsupported, alt)
		case part.TooLarge:
			return w.omitted(dst, depth, omitTooLarge, alt)
		}
		data = part.Data
	case strings.HasPrefix(lower, "data:"):
		mediaType, encoded, ok := splitDataURI(src)
		switch {
		case !strings.HasPrefix(mediaType, "image/"):
			return nil // removed
		case !ok || !shownTypes[mediaType]:
			return w.omitted(dst, depth, omitUnsupported, alt)
		case base64.StdEncoding.DecodedLen(len(encoded)) > contracts.MessageInlineImageMaxBytes+3:
			return w.omitted(dst, depth, omitTooLarge, alt)
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return w.omitted(dst, depth, omitUnsupported, alt)
		}
		data = decoded
	case looksRemote(src):
		w.stats.RemoteImages++
		if trackingPixel(c) {
			return nil
		}
		return w.box(dst, depth, "remote", alt)
	default:
		return nil // removed
	}
	if len(data) > contracts.MessageInlineImageMaxBytes {
		return w.omitted(dst, depth, omitTooLarge, alt)
	}
	info, ok, err := readImage(w.ctx, data)
	switch {
	case err != nil:
		return err
	case !ok:
		return w.omitted(dst, depth, omitUnsupported, alt)
	case info.Animated:
		return w.omitted(dst, depth, omitAnimated, alt)
	}
	pixels := info.Width * info.Height
	if info.Width > contracts.MessageInlineImageMaxSide || info.Height > contracts.MessageInlineImageMaxSide ||
		pixels > contracts.MessageInlineImageMaxPixels || w.images.shown >= contracts.MessageInlineImagesMax ||
		w.images.bytes+len(data) > contracts.MessageInlineImagesMaxBytes || w.images.pixels+pixels > contracts.MessageInlineImagesMaxPixels {
		return w.omitted(dst, depth, omitTooLarge, alt)
	}
	w.images.shown++
	w.images.bytes += len(data)
	w.images.pixels += pixels
	w.stats.InlineImages++
	img, err := w.newElement(dst, atom.Img, "img", depth)
	if err != nil {
		return err
	}
	uri := "data:" + info.Type + ";base64," + base64.StdEncoding.EncodeToString(data)
	w.uriBytes += len(uri)
	img.Attr = append([]html.Attribute{{Key: "src", Val: uri}}, w.attributes(c, atom.Img, inLink)...)
	return nil
}

// shownTypes are the types an image may be declared as in a data: URI;
// what it is shown as is the type its bytes are.
var shownTypes = set("image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp")

// splitDataURI reads data:<type>[;params];base64,<data>: the media type in
// lower case, and the data with white space removed. ok is false when it is
// not base64.
func splitDataURI(s string) (string, string, bool) {
	comma := strings.IndexByte(s, ',')
	if comma < 0 {
		return "", "", false
	}
	meta := strings.ToLower(s[len("data:"):comma])
	parts := strings.Split(meta, ";")
	mediaType := strings.TrimSpace(parts[0])
	if parts[len(parts)-1] != "base64" {
		return mediaType, "", false
	}
	data := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' {
			return -1
		}
		return r
	}, s[comma+1:])
	return mediaType, data, true
}

// contentID is the Content-ID a cid: URL names (RFC 2392: percent-encoded,
// without the angle brackets), as InlinePart keys are written.
func contentID(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		s = u
	}
	return NormaliseContentID(s)
}

// NormaliseContentID is a Content-ID without white space or angle brackets,
// as Input.Inline is keyed.
func NormaliseContentID(s string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "<"), ">")
}

// trackingPixel says whether an image is made not to be seen: 0, 1 or 2
// pixels wide or high, or hidden by its style.
func trackingPixel(c *html.Node) bool {
	for _, k := range []string{"width", "height"} {
		if v, ok := attr(c, k); ok {
			switch strings.TrimSuffix(strings.TrimSpace(v), "px") {
			case "0", "1", "2":
				return true
			}
		}
	}
	if v, ok := attr(c, "style"); ok {
		s := strings.ToLower(strings.Join(strings.Fields(v), ""))
		for _, p := range []string{"display:none", "visibility:hidden", "width:0", "width:1px", "height:0", "height:1px", "opacity:0"} {
			if strings.Contains(s, p) {
				return true
			}
		}
	}
	return false
}

// omitted counts an image that travels in the message and is not shown, and
// draws its box.
func (w *walker) omitted(dst *html.Node, depth int, why, alt string) error {
	w.stats.InlineImagesOmitted++
	switch why {
	case omitTooLarge:
		w.stats.InlineImagesOmittedBy.TooLarge++
	case omitAnimated:
		w.stats.InlineImagesOmittedBy.Animated++
	default:
		w.stats.InlineImagesOmittedBy.Unsupported++
	}
	return w.box(dst, depth, why, alt)
}

// box draws the box of an image not shown: a bordered span saying why, and
// the image's alt text.
func (w *walker) box(dst *html.Node, depth int, why, alt string) error {
	span, err := w.newElement(dst, atom.Span, "span", depth)
	if err != nil {
		return err
	}
	span.Attr = []html.Attribute{{Key: "style", Val: boxStyle}}
	text := omittedWords[why]
	if alt = strings.TrimSpace(alt); alt != "" {
		text += ": " + alt
	}
	appendText(span, text)
	return nil
}
