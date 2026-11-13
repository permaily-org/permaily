package inbox

import (
	"bytes"
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"io"
	"strings"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/textproto"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/mailhtml"
)

// The HTML part of a message, as the formatted view shows it (ADR 0026,
// "Showing a message's HTML", pull request 5): the part a mail client
// would show, its charset decided in the order mailhtml.DecodeCharset
// writes, and the parts of its multipart/related by Content-ID. Every render
// runs under a render slot and its deadline; nothing is stored or cached.

// LinkPurpose derives, from a mailbox's key, the key of its links' tags
// (ADR 0026, "The link's token").
const LinkPurpose = "permaily link v1"

// LinkKey is the key a mailbox's links are tagged with.
func LinkKey(mailboxKey []byte) []byte {
	k, _ := hkdf.Key(sha256.New, mailboxKey, nil, LinkPurpose, sha256.Size) // fails only for a length past 255 hashes
	return k
}

// RenderAdmission admits a render (ADR 0026, "Admission of a render"):
// Admit takes a render slot of the enclave and of the caller's account,
// and starts the render's deadline; the context it returns ends at that
// deadline (and with the request), and every step of the render runs under
// it: the MIME walk for the HTML part, its charset, the render. It fails
// with contracts.ErrBusy, contracts.ErrTooManyRequests or the request's
// error. The function it returns ends the context and gives the slot back;
// it is called once.
type RenderAdmission interface {
	Admit(ctx context.Context) (rctx context.Context, release func(), err error)
}

type admissionKey struct{}

// WithRenderAdmission makes Get admit the render of a message's HTML
// through a. Without one Get renders at once under RenderDeadline (tests of
// the reader alone).
func WithRenderAdmission(ctx context.Context, a RenderAdmission) context.Context {
	return context.WithValue(ctx, admissionKey{}, a)
}

// RenderDeadline is how long one render may run
// (contracts.MessageHTMLRenderDeadline); tests set less.
var RenderDeadline = contracts.MessageHTMLRenderDeadline

// htmlPart is what a message's walk found of its HTML: the part's bytes as
// its sender wrote them (the transfer encoding undone, no charset
// converted), the charset its header names, the inline parts, and whether
// the part was over the limit.
type htmlPart struct {
	html     []byte
	charset  string
	tooLarge bool
	inline   map[string]mailhtml.InlinePart
}

// inlineReadMax bounds the bytes read of a message's inline parts
// altogether: more than the images a message shows together, and the one
// over the limit that tells it.
const inlineReadMax = contracts.MessageInlineImagesMaxBytes + contracts.MessageInlineImageMaxBytes

var errNoHTML = errors.New("inbox: no HTML part")

// findHTML walks raw for its HTML part: the last text/html of a
// multipart/alternative, else the first inline text/html; and the parts of
// the multipart/related it is in, by Content-ID. It keeps the limits of
// "What a message may hold" and checks ctx before every entity.
func findHTML(ctx context.Context, raw []byte) (*htmlPart, error) {
	head, cut := boundHeader(raw)
	if cut {
		return nil, errNoHTML
	}
	h, body, err := readHeader(head)
	if err != nil {
		return nil, errNoHTML
	}
	f := &htmlFinder{ctx: ctx}
	part, err := f.entity(h, body, 0)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errNoHTML
	}
	if part == nil {
		return nil, errNoHTML
	}
	return part, nil
}

type htmlFinder struct {
	ctx        context.Context
	parts      int
	inlineRead int
}

// entity finds the HTML in one entity, or nil.
func (f *htmlFinder) entity(h message.Header, body io.Reader, depth int) (*htmlPart, error) {
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	f.parts++
	if f.parts > MaxMIMEParts || headerSize(h) > MaxHeaderBytes {
		return nil, errLimit
	}
	t, params, _ := h.ContentType()
	if t == "" && h.Get("Content-Type") == "" {
		t = "text/plain"
	}
	if !strings.HasPrefix(t, "multipart/") {
		disp, _, _ := h.ContentDisposition()
		if t != "text/html" || disp == "attachment" {
			return nil, nil
		}
		b, over := readUpTo(transferDecoded(h, body), contracts.MessageHTMLMaxBytes)
		return &htmlPart{html: b, charset: params["charset"], tooLarge: over}, nil
	}
	if depth >= MaxMIMEDepth {
		return nil, errLimit
	}
	mr := textproto.NewMultipartReader(body, params["boundary"])
	var found *htmlPart
	related := t == "multipart/related"
	alternative := t == "multipart/alternative"
	inline := map[string]mailhtml.InlinePart{}
	first := true
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if found != nil {
				break // what was found before the break stands
			}
			return nil, err
		}
		ph := message.Header{Header: p.Header}
		switch {
		case related && !first:
			if err := f.inlinePart(ph, p, inline); err != nil {
				return nil, err
			}
		case alternative || first && related || found == nil:
			part, err := f.entity(ph, p, depth+1)
			if err != nil {
				return nil, err
			}
			if part != nil && (alternative || found == nil) {
				found = part
			}
		}
		first = false
	}
	if found != nil && related {
		if found.inline == nil {
			found.inline = map[string]mailhtml.InlinePart{}
		}
		for k, v := range inline {
			if _, ok := found.inline[k]; !ok {
				found.inline[k] = v
			}
		}
	}
	return found, nil
}

// inlinePart keeps a part of a multipart/related by its Content-ID, read
// within inlineReadMax for all of them.
func (f *htmlFinder) inlinePart(h message.Header, body io.Reader, into map[string]mailhtml.InlinePart) error {
	if err := f.ctx.Err(); err != nil {
		return err
	}
	f.parts++
	if f.parts > MaxMIMEParts || headerSize(h) > MaxHeaderBytes {
		return errLimit
	}
	id := mailhtml.NormaliseContentID(h.Get("Content-Id"))
	t, _, _ := h.ContentType()
	if id == "" || strings.HasPrefix(t, "multipart/") {
		return nil
	}
	if _, ok := into[id]; ok {
		return nil // the first part of an identifier counts
	}
	room := min(contracts.MessageInlineImageMaxBytes, inlineReadMax-f.inlineRead)
	if room <= 0 {
		into[id] = mailhtml.InlinePart{Type: t, TooLarge: true}
		return nil
	}
	b, over := readUpTo(transferDecoded(h, body), room)
	f.inlineRead += len(b)
	into[id] = mailhtml.InlinePart{Type: t, Data: b, TooLarge: over}
	return nil
}

// htmlInput is the render's input for a message of a box: its HTML part in
// UTF-8 with its inline parts, or the status that says why there is none to
// render (contracts.HTMLTooLarge, contracts.HTMLUnreadable). errNoHTML when
// the message has no HTML part.
func htmlInput(ctx context.Context, raw []byte, b Box, id string) (mailhtml.Input, string, error) {
	part, err := findHTML(ctx, raw)
	if err != nil {
		return mailhtml.Input{}, "", err
	}
	if part.tooLarge {
		return mailhtml.Input{}, contracts.HTMLTooLarge, nil
	}
	decoded, status := mailhtml.DecodeCharset(part.html, part.charset)
	if status != "" {
		return mailhtml.Input{}, status, nil
	}
	return mailhtml.Input{HTML: decoded, Inline: part.inline, Mailbox: b.MailboxID, Message: id, LinkKey: LinkKey(b.Key)}, "", nil
}

// htmlInputFn is HTMLInput; tests see when Get calls it.
var htmlInputFn = HTMLInput

// HTMLInput is the render's input for one message of a box, read from its
// raw bytes: what the frame's route and the link route render. ok is false
// when the message has no HTML part; status is set when it has one that
// cannot be rendered.
func HTMLInput(ctx context.Context, raw []byte, b Box, id string) (in mailhtml.Input, status string, ok bool, err error) {
	in, status, err = htmlInput(ctx, raw, b, id)
	if errors.Is(err, errNoHTML) {
		return mailhtml.Input{}, "", false, nil
	}
	if err != nil {
		return mailhtml.Input{}, "", false, err
	}
	return in, status, true, nil
}

// messageHTML is what Get says of a message's HTML: its status and counts,
// rendered into nothing; nil when it has no HTML part. The text and the
// metadata Get answers need no slot: only this summary does. A message
// whose bytes name no HTML type (mayHoldHTML) asks for none, so a text
// message never waits behind renders. Otherwise the render is admitted
// first, and the HTML part is found, transcoded and rendered under the
// admitted context; an admission refused, or a deadline that passes, is
// contracts.HTMLUnavailable.
func messageHTML(ctx context.Context, raw []byte, b Box, id string) *contracts.MessageHTML {
	if !mayHoldHTML(raw) {
		return nil
	}
	var rctx context.Context
	var release func()
	if a, ok := ctx.Value(admissionKey{}).(RenderAdmission); ok {
		c, r, err := a.Admit(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // the request's own context ended: Get answers its error
			}
			return &contracts.MessageHTML{Status: contracts.HTMLUnavailable}
		}
		rctx, release = c, r
	} else {
		rctx, release = context.WithTimeout(ctx, RenderDeadline)
	}
	defer release()
	in, status, ok, err := htmlInputFn(rctx, raw, b, id)
	switch {
	case err != nil:
		if ctx.Err() != nil {
			return nil
		}
		return &contracts.MessageHTML{Status: contracts.HTMLUnavailable} // the render's deadline passed
	case !ok:
		return nil
	case status != "":
		return &contracts.MessageHTML{Status: status}
	}
	out, err := mailhtml.Render(rctx, in, nil)
	if err != nil {
		return &contracts.MessageHTML{Status: contracts.HTMLUnavailable}
	}
	return &out
}

// mayHoldHTML says whether raw names an HTML type anywhere ("/html" in any
// case), so that a part of it may be text/html. It is a pass over bytes
// that keeps nothing; a false yes costs a render slot, never a wrong
// answer, and a no is certain: a part whose type does not say "/html" is
// not HTML to the MIME walk.
func mayHoldHTML(raw []byte) bool {
	for i := 0; ; {
		j := bytes.IndexByte(raw[i:], '/')
		if j < 0 {
			return false
		}
		i += j + 1
		if len(raw)-i >= 4 && raw[i]|0x20 == 'h' && raw[i+1]|0x20 == 't' && raw[i+2]|0x20 == 'm' && raw[i+3]|0x20 == 'l' {
			return true
		}
	}
}
