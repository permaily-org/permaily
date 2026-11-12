package inbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/emersion/go-message"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The one MIME walk and the one text extraction of the tree (ADR 0026, "What
// a message returns"): delivery lists attachments and takes the text for
// search with them (Parse), and reading takes a message's text and finds an
// attachment by its index with them, so what is listed and what is
// downloaded are the same parts. The text of HTML is taken out with
// x/net/html's tokenizer, linear in the input, bounded in what it keeps, and
// stopped by its context between tokens.

// MessageText is the text of a message as the text view and search show it:
// its first text/plain part, decoded; with none, the text taken out of its
// first text/html part; with neither, nothing. At most max bytes; truncated
// when the output's bound, the HTML read for text (contracts.
// MessageHTMLMaxBytes) or ctx cut anything. from is contracts.TextFromPlain,
// TextFromHTML or TextFromNone.
func MessageText(ctx context.Context, raw []byte, max int) (text, from string, truncated bool) {
	w := walkMessage(ctx, raw, max, false)
	return w.text(ctx, max)
}

// errLimit ends a walk at a limit of "What a message may hold".
var errLimit = errors.New("inbox: a limit of what a message may hold")

// errWalkStop ends a walk early because the visitor asked; it is not an
// error of the message.
var errWalkStop = errors.New("inbox: walk stopped")

// walkParts visits the leaf parts of a message in order, depth first. A part
// is an attachment unless its disposition is inline, or it is text/* (a part
// with no Content-Type is text/plain) without disposition attachment, as
// go-message's mail.Reader decides. The walk keeps the limits of ADR 0026
// ("What a message may hold"): MaxHeaderBytes for the header block of every
// entity, MaxMIMEDepth levels of nesting and MaxMIMEParts entities, the
// message itself included; past one it returns errLimit. visit returns false
// to stop (the walk then returns nil). It checks ctx before every entity.
func walkParts(ctx context.Context, raw []byte, visit func(p *message.Entity, attachment bool) (bool, error)) error {
	e, err := readEntity(raw)
	if e == nil {
		return err // a header that is not one: go-message says why
	}
	parts := 0
	var walk func(e *message.Entity, depth int) error
	walk = func(e *message.Entity, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		parts++
		if parts > MaxMIMEParts || headerSize(e.Header) > MaxHeaderBytes {
			return errLimit
		}
		if mr := e.MultipartReader(); mr != nil {
			if depth >= MaxMIMEDepth {
				return errLimit
			}
			for {
				p, err := mr.NextPart()
				if errors.Is(err, io.EOF) {
					return nil
				}
				if p == nil || (err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err)) {
					return err
				}
				if err := walk(p, depth+1); err != nil {
					return err
				}
			}
		}
		more, err := visit(e, isAttachment(e))
		if err != nil {
			return err
		}
		if !more {
			return errWalkStop
		}
		return nil
	}
	err = walk(e, 0)
	if errors.Is(err, errWalkStop) {
		return nil
	}
	return err
}

// contentType is a part's media type, lower case: text/plain when it has
// none.
func contentType(e *message.Entity) string {
	t, _, _ := e.Header.ContentType()
	if t == "" && e.Header.Get("Content-Type") == "" {
		return "text/plain"
	}
	return t
}

func isAttachment(e *message.Entity) bool {
	disp, _, _ := e.Header.ContentDisposition()
	return disp != "inline" && (disp == "attachment" || !strings.HasPrefix(contentType(e), "text/"))
}

// headerSize is what an entity's header block holds: each field's name and
// value, and its separator and line end.
func headerSize(h message.Header) int {
	n := 0
	for f := h.Fields(); f.Next(); {
		n += len(f.Key()) + len(f.Value()) + 4
	}
	return n
}

// walked is what one walk of a message found: its first text/plain and
// text/html parts, and, when asked, its attachments.
type walked struct {
	partial     bool
	stopped     bool
	plain       []byte
	plainSeen   bool
	plainCut    bool
	html        []byte
	htmlSeen    bool
	htmlCut     bool
	attachments []contracts.MessageAttachment
}

// walkMessage walks a message once: the first text/plain part is read up to
// plainMax bytes, the first text/html part up to contracts.MessageHTMLMaxBytes,
// and with list every attachment is listed, within the limits. A header
// block longer than MaxHeaderBytes leaves the message without a body.
func walkMessage(ctx context.Context, raw []byte, plainMax int, list bool) *walked {
	w := &walked{}
	head, cut := boundHeader(raw)
	if cut {
		w.partial = true
		return w
	}
	err := walkParts(ctx, head, func(p *message.Entity, attachment bool) (bool, error) {
		if attachment {
			if list {
				w.attach(p)
			}
			return true, nil
		}
		switch t := contentType(p); {
		case t == "text/plain" && !w.plainSeen:
			w.plainSeen = true
			w.plain, w.plainCut = readUpTo(p.Body, plainMax)
		case t == "text/html" && !w.htmlSeen:
			w.htmlSeen = true
			w.html, w.htmlCut = readUpTo(p.Body, contracts.MessageHTMLMaxBytes)
		}
		// Without a list, the walk ends at the first text/plain part: nothing
		// after it is read.
		return list || !w.plainSeen, nil
	})
	if err != nil {
		w.partial = true
	}
	if ctx.Err() != nil {
		w.partial, w.stopped = true, true
	}
	return w
}

// readUpTo reads at most n bytes of r, and whether there was more.
func readUpTo(r io.Reader, n int) ([]byte, bool) {
	b, _ := io.ReadAll(io.LimitReader(r, int64(n)+1))
	if len(b) > n {
		return b[:n], true
	}
	return b, false
}

// MaxContentTypeBytes bounds the content type kept of an attachment: past it
// the type is application/octet-stream and the parse partial.
const MaxContentTypeBytes = 255

// attach lists an attachment, its size its decoded bytes, within the limits
// of how many are listed, the length of a name and of a content type.
func (w *walked) attach(p *message.Entity) {
	n, _ := io.Copy(io.Discard, io.LimitReader(p.Body, MaxMessageBytes+1))
	if len(w.attachments) == MaxAttachmentsListed {
		w.partial = true
		return
	}
	ct, ctParams, _ := p.Header.ContentType()
	_, params, _ := p.Header.ContentDisposition()
	name := params["filename"]
	if name == "" {
		name = ctParams["name"]
	}
	name = cut(name, MaxAttachmentName, &w.partial)
	if len(ct) > MaxContentTypeBytes {
		w.partial = true
		ct = ""
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.attachments = append(w.attachments, contracts.MessageAttachment{Index: len(w.attachments), Name: name, Type: ct, Size: n})
}

// text is the message's text, at most max bytes: its text/plain part, else
// what is taken out of its text/html part, else nothing.
func (w *walked) text(ctx context.Context, max int) (text, from string, truncated bool) {
	switch {
	case w.plainSeen:
		text, truncated = readPlain(w.plain, max)
		return text, contracts.TextFromPlain, truncated || w.plainCut || w.stopped
	case w.htmlSeen:
		text, truncated = readHTMLText(ctx, w.html, max)
		return text, contracts.TextFromHTML, truncated || w.htmlCut || w.stopped
	}
	return "", contracts.TextFromNone, w.stopped
}

// readPlain is a text part as text: valid UTF-8, LF line ends, at most max
// bytes cut at a character.
func readPlain(b []byte, max int) (string, bool) {
	s := strings.ToValidUTF8(string(b), "�")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return cutText(s, max)
}

// cutText keeps at most limit bytes of s, ending on a whole character.
func cutText(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	s = s[:max(0, limit)]
	for len(s) > 0 {
		if r, size := utf8.DecodeLastRuneInString(s); r != utf8.RuneError || size != 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s, true
}

// The elements whose content is never text, and the ones that start a line.
var (
	textSkipped = map[atom.Atom]bool{atom.Script: true, atom.Style: true, atom.Head: true, atom.Title: true, atom.Template: true, atom.Noscript: true}
	textBlocks  = map[atom.Atom]bool{
		atom.Address: true, atom.Article: true, atom.Aside: true, atom.Blockquote: true, atom.Br: true, atom.Caption: true,
		atom.Dd: true, atom.Details: true, atom.Div: true, atom.Dl: true, atom.Dt: true, atom.Fieldset: true,
		atom.Figcaption: true, atom.Figure: true, atom.Footer: true, atom.Form: true, atom.H1: true, atom.H2: true,
		atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true, atom.Header: true, atom.Hr: true, atom.Li: true,
		atom.Main: true, atom.Nav: true, atom.Ol: true, atom.P: true, atom.Pre: true, atom.Section: true,
		atom.Summary: true, atom.Table: true, atom.Tbody: true, atom.Td: true, atom.Tfoot: true, atom.Th: true,
		atom.Thead: true, atom.Tr: true, atom.Ul: true,
	}
)

// What the extraction keeps while it scans, whatever the input: the targets
// of at most maxOpenLinks open links (deeper ones are counted, not kept),
// each of at most maxLinkTarget bytes (a longer one is not written), and the
// context is checked every ctxEvery tokens.
const (
	maxOpenLinks  = 32
	maxLinkTarget = 2 << 10
	ctxEvery      = 1 << 10
)

// readHTMLText takes the text out of an HTML part: its text nodes, a line
// break for each block element, nothing of script, style, head or noscript,
// entities decoded, and each link's target after its text in brackets. The
// HTML is only tokenized, never built into a tree or shown: what it says
// becomes text and nothing else. It stops at max bytes of text, or when ctx
// ends, and says it was cut.
func readHTMLText(ctx context.Context, b []byte, max int) (string, bool) {
	z := html.NewTokenizer(bytes.NewReader(b))
	z.SetMaxBuf(len(b) + 1)
	w := htmlTextWriter{max: max}
	skip, pre, deeper := 0, 0, 0
	var links []string // the target of each open <a> kept, "" for none
	for tokens := 0; !w.full(); tokens++ {
		if tokens%ctxEvery == 0 && ctx.Err() != nil {
			w.truncated = true
			break
		}
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return w.done()
		case html.TextToken:
			if skip == 0 {
				w.text(string(z.Text()), pre > 0)
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			a := atom.Lookup(name)
			if textSkipped[a] {
				if tt == html.StartTagToken {
					skip++
				}
				continue
			}
			if skip > 0 {
				continue
			}
			if textBlocks[a] {
				w.line(a == atom.Br)
			}
			if a == atom.Pre && tt == html.StartTagToken {
				pre++
			}
			if a == atom.A && tt == html.StartTagToken {
				if len(links) == maxOpenLinks {
					deeper++
					continue
				}
				href := ""
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					if string(k) == "href" && len(v) <= maxLinkTarget {
						href = strings.TrimSpace(string(v))
					}
				}
				links = append(links, href)
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			a := atom.Lookup(name)
			if textSkipped[a] {
				if skip > 0 {
					skip--
				}
				continue
			}
			if skip > 0 {
				continue
			}
			switch {
			case a == atom.A && deeper > 0:
				deeper--
			case a == atom.A && len(links) > 0:
				href := links[len(links)-1]
				links = links[:len(links)-1]
				if href != "" && !strings.HasPrefix(href, "#") && !strings.Contains(w.lastWords(len(href)), href) {
					w.text(" ["+href+"]", false)
				}
			case a == atom.Pre && pre > 0:
				pre--
			}
			if textBlocks[a] && a != atom.Br {
				w.line(false)
			}
		}
	}
	return w.done()
}

// htmlTextWriter writes the text of an HTML part: runs of white space become
// one space outside <pre>, blank lines are at most one, and it stops at max
// bytes.
type htmlTextWriter struct {
	b         strings.Builder
	max       int
	space     bool // a space is owed before the next word
	newlines  int  // line breaks owed before the next word
	truncated bool
}

func (w *htmlTextWriter) full() bool { return w.truncated }

// line ends the line, once however many blocks end or start there; br, which
// is a line break of its own, ends one more (a blank line at most).
func (w *htmlTextWriter) line(br bool) {
	if w.b.Len() > 0 {
		if br {
			w.newlines = min(w.newlines+1, 2)
		} else {
			w.newlines = max(w.newlines, 1)
		}
	}
	w.space = false
}

func (w *htmlTextWriter) text(s string, pre bool) {
	// Made valid before it is counted, so what is written stays within max.
	s = strings.ToValidUTF8(s, "�")
	if pre {
		w.flush()
		w.write(strings.ReplaceAll(s, "\r\n", "\n"))
		return
	}
	first, _ := utf8.DecodeRuneInString(s)
	last, _ := utf8.DecodeLastRuneInString(s)
	for i, word := range strings.Fields(s) {
		if i > 0 || unicode.IsSpace(first) {
			w.space = true
		}
		w.flush()
		w.write(word)
		if w.truncated {
			return
		}
	}
	if s != "" && unicode.IsSpace(last) {
		w.space = true
	}
}

func (w *htmlTextWriter) flush() {
	switch {
	case w.newlines > 0:
		w.write(strings.Repeat("\n", w.newlines))
	case w.space && w.b.Len() > 0:
		w.write(" ")
	}
	w.newlines, w.space = 0, false
}

func (w *htmlTextWriter) write(s string) {
	if w.truncated {
		return
	}
	if w.b.Len()+len(s) > w.max {
		s, _ = cutText(s, w.max-w.b.Len())
		w.b.WriteString(s)
		w.truncated = true
		return
	}
	w.b.WriteString(s)
}

// lastWords is the end of what was written, n bytes at most.
func (w *htmlTextWriter) lastWords(n int) string {
	s := w.b.String()
	return s[max(0, len(s)-n):]
}

func (w *htmlTextWriter) done() (string, bool) {
	return w.b.String(), w.truncated
}
