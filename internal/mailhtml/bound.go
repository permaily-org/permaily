package mailhtml

import (
	"bytes"
	"context"
	"errors"
	"io"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The budgets of a render (ADR 0026, "Budgets, enforced while the work
// happens"): a tokeniser pass that bounds the tree before one is built, a
// reader that stops the parse once the deadline has passed, a walk that
// counts and checks its context, and a writer that fails at its cap.

var (
	// errTooLarge is HTML over a limit: the text is shown instead.
	errTooLarge = errors.New("mailhtml: over a limit")
	// errUnreadable is HTML that does not parse back to itself.
	errUnreadable = errors.New("mailhtml: does not parse back to itself")
)

// formattingAtoms are the formatting elements of the HTML standard: the
// parser keeps them in its list of active formatting elements and clones
// them ("reconstruct the active formatting elements", "adoption agency").
var formattingAtoms = setAtoms(atom.A, atom.B, atom.Big, atom.Code, atom.Em, atom.Font, atom.I,
	atom.Nobr, atom.S, atom.Small, atom.Strike, atom.Strong, atom.Tt, atom.U)

// rawText are the elements whose content x/net/html's tokenizer reads as
// text unless told otherwise.
var rawText = map[atom.Atom]bool{
	atom.Iframe: true, atom.Noembed: true, atom.Noframes: true, atom.Noscript: true, atom.Plaintext: true,
	atom.Script: true, atom.Style: true, atom.Textarea: true, atom.Title: true, atom.Xmp: true,
}

// adoptionNodes bounds the elements one end tag of a formatting element can
// make the parser create (the adoption agency algorithm: an outer loop of
// at most eight rounds, each cloning at most three elements of its inner
// loop and the formatting element itself).
const adoptionNodes = 8 * 4

// boundScope is a part of the list of active formatting elements between two
// markers, as far as a tokeniser can tell: the root, or a table cell or
// caption (which push a marker and clear to it when they close).
type boundScope struct {
	entries []boundEntry
	cell    bool
	table   bool // the scope of an open <table>: it holds no entries of its own
}

// boundEntry is a formatting element the parser may hold as active. dirty
// says a block (a special element that is not void) has started since it
// opened: a furthest block may then be open when its end tag comes, and
// the adoption agency keeps a clone of it active and may create nodes.
type boundEntry struct {
	atom  atom.Atom
	dirty bool
}

// blocks are the special elements of the HTML standard that stay open on
// the stack (the void ones never do): only one of them can be the furthest
// block of the adoption agency, or end the scope of a formatting element.
var blocks = setAtoms(atom.Address, atom.Applet, atom.Article, atom.Aside, atom.Blockquote, atom.Body,
	atom.Button, atom.Caption, atom.Center, atom.Colgroup, atom.Dd, atom.Details, atom.Dir, atom.Div, atom.Dl,
	atom.Dt, atom.Fieldset, atom.Figcaption, atom.Figure, atom.Footer, atom.Form, atom.Frameset, atom.H1,
	atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Head, atom.Header, atom.Hgroup, atom.Html, atom.Iframe,
	atom.Li, atom.Listing, atom.Main, atom.Marquee, atom.Menu, atom.Nav, atom.Noembed, atom.Noframes,
	atom.Noscript, atom.Object, atom.Ol, atom.P, atom.Plaintext, atom.Pre, atom.Script, atom.Search,
	atom.Section, atom.Select, atom.Style, atom.Summary, atom.Table, atom.Tbody, atom.Td, atom.Template,
	atom.Textarea, atom.Tfoot, atom.Th, atom.Thead, atom.Title, atom.Tr, atom.Ul, atom.Xmp)

// treeBound is the tokeniser pass over a decoded part: start tags,
// attributes per element and in all, and an upper bound of the nodes the
// parser will create. It fails with errTooLarge past any limit, or ctx's
// error, and builds no tree.
//
// The bound adds one for each start tag and text token, and at each of them
// every formatting element the parser may hold as active and not open,
// which its "reconstruct the active formatting elements" step can clone
// there. A formatting element leaves that count only at its end tag, and
// only when no block has started since it opened (then the parser pops it
// and forgets it); otherwise it stays counted, and its end tag adds what
// the adoption agency can create (adoptionNodes). A cell's or a caption's
// end clears what it opened, as the parser clears to its marker.
func treeBound(ctx context.Context, b []byte) (int, error) {
	z := html.NewTokenizer(bytes.NewReader(b))
	z.SetMaxBuf(len(b) + 1)
	scopes := []boundScope{{}}
	starts, attrs, bound, active := 0, 0, 0, 0
	// foreign is sticky: once an <svg> or <math> has opened, the parser may
	// be in foreign content anywhere after (a tokeniser cannot tell where it
	// leaves), where <script>, <style> and the other raw text elements are
	// markup, and where elements of other names may be blocks; so from then
	// on the pass reads them as markup, takes every start tag for a block,
	// and pops no table scope. Each only raises the bound.
	foreign, templates := false, 0
	// own is the scope formatting elements opened now belong to: the
	// innermost that is not a table's (formatting elements in a table are
	// foster parented and stay active after it).
	own := func() *boundScope {
		for i := len(scopes) - 1; i > 0; i-- {
			if !scopes[i].table {
				return &scopes[i]
			}
		}
		return &scopes[0]
	}
	pop := func() {
		active -= len(scopes[len(scopes)-1].entries)
		scopes = scopes[:len(scopes)-1]
	}
	popCells := func() {
		for len(scopes) > 1 && scopes[len(scopes)-1].cell {
			pop()
		}
	}
	// endFormatting is the end tag of a formatting element (or the implied
	// one before a second <a>): the last entry of its name in the scope.
	endFormatting := func(a atom.Atom) {
		s := own()
		for k := len(s.entries) - 1; k >= 0; k-- {
			if s.entries[k].atom != a {
				continue
			}
			if s.entries[k].dirty {
				bound += adoptionNodes
				return
			}
			s.entries = append(s.entries[:k], s.entries[k+1:]...)
			active--
			return
		}
	}
	structural := func() bool { return !foreign && templates == 0 }
	for tokens := 0; ; tokens++ {
		if tokens%ctxEvery == 0 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			if errors.Is(z.Err(), io.EOF) {
				return bound, nil
			}
			return 0, errTooLarge
		case html.TextToken:
			bound += 1 + active
		case html.StartTagToken, html.SelfClosingTagToken:
			starts++
			name, hasAttr := z.TagName()
			n := 0
			for hasAttr {
				_, _, hasAttr = z.TagAttr()
				n++
			}
			attrs += n
			if starts > contracts.MessageHTMLMaxStartTags || n > contracts.MessageHTMLMaxAttributesPerElement || attrs > contracts.MessageHTMLMaxAttributes {
				return 0, errTooLarge
			}
			bound += 1 + active
			a := atom.Lookup(name)
			switch {
			case a == atom.Svg || a == atom.Math:
				foreign = true
			case a == atom.Template:
				templates++
			}
			// The parse runs with scripting off, where <noscript> holds
			// markup; in foreign content every raw text element does.
			if rawText[a] && (foreign || a == atom.Noscript) {
				z.NextIsNotRawText()
			}
			if blocks[a] || foreign {
				for i := range scopes {
					for k := range scopes[i].entries {
						scopes[i].entries[k].dirty = true
					}
				}
			}
			if _, ok := formattingAtoms[a]; ok {
				if a == atom.A || a == atom.Nobr {
					endFormatting(a) // a second <a> or <nobr> ends the first
				}
				s := own()
				s.entries = append(s.entries, boundEntry{atom: a, dirty: foreign})
				active++
			}
			if !structural() {
				break
			}
			switch a {
			case atom.Table:
				scopes = append(scopes, boundScope{table: true})
			case atom.Td, atom.Th, atom.Caption:
				if len(scopes) > 1 && scopes[len(scopes)-1].cell {
					pop()
				}
				if len(scopes) > 1 {
					scopes = append(scopes, boundScope{cell: true})
				}
			case atom.Tr, atom.Tbody, atom.Thead, atom.Tfoot:
				popCells()
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			a := atom.Lookup(name)
			if a == atom.Template && templates > 0 {
				templates--
			}
			if _, ok := formattingAtoms[a]; ok {
				endFormatting(a)
			}
			if !structural() {
				break
			}
			switch a {
			case atom.Td, atom.Th, atom.Caption:
				if len(scopes) > 1 && scopes[len(scopes)-1].cell {
					pop()
				}
			case atom.Tr, atom.Tbody, atom.Thead, atom.Tfoot:
				popCells()
			case atom.Table:
				for len(scopes) > 1 {
					table := scopes[len(scopes)-1].table
					pop()
					if table {
						break
					}
				}
			}
		}
		if bound > contracts.MessageHTMLMaxTreeBound {
			return 0, errTooLarge
		}
	}
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
