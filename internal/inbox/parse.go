// Package inbox is what is stored of a mailbox's mail and how it gets there
// (ADR 0026): parsing a message within bounds (parse.go), the text it shows
// and search reads (text.go), the claim and the order of writes of a delivery
// (deliver.go), and the stored format that reading uses (box.go). Everything
// here runs inside the enclave; the store only ever sees sealed bytes plus
// identifiers, sizes and times.
package inbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // decode non-UTF-8 charsets
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"

	"github.com/permaily-org/permaily/internal/contracts"
)

// MaxMessageBytes is the largest message accepted (SMTP SIZE).
const MaxMessageBytes = 25 << 20

// AuthResult is the outcome of one sender authentication check.
type AuthResult struct {
	Method string `json:"method"` // "dkim"
	Domain string `json:"domain"`
	Result string `json:"result"` // "pass" or "fail: <reason>"
	// Headers are the header fields the signature covers (its h=), lower
	// case, and BodyLimited says it covers only part of the body (l=). Both
	// are read by the check of Gmail's confirmation and never stored.
	Headers     []string `json:"-"`
	BodyLimited bool     `json:"-"`
}

// Authenticator checks a raw message's signatures (DKIM).
type Authenticator func(ctx context.Context, raw []byte) []AuthResult

// VerifyGmail turns on the mark that Google confirmed a forwarding
// confirmation (ADR 0026, "Gmail's forwarding confirmation"). It is on since
// two real confirmations, captured in production on 2026-11-14 (Gmail in
// English and in Spanish, testdata/gmail), pass the predicate against the DNS
// records of their keys at capture time and their altered copies fail it
// (gmail_test.go). Tests may turn it off to see the product without it.
var VerifyGmail = true

// snippetRunes is how much of a message's text its summary shows.
const snippetRunes = 200

// Parsed is what the enclave reads of a message once, at delivery: what is
// sealed in its item, the start of its text for search, and a forwarding
// confirmation when it is one.
type Parsed struct {
	Meta StoredMeta
	// Text is the message's text, at most SearchTextMax bytes.
	Text string
	// Confirmation is set when the message is recognised as Gmail's
	// forwarding confirmation.
	Confirmation *Confirmation
}

// Confirmation is what a message recognised as Gmail's forwarding
// confirmation says, and what the check that Google sent it needs.
type Confirmation struct {
	// Code is the code the subject or the body carries; Gmail's message as
	// it is sent since 2026 carries none.
	Code      string
	Requester string
	// Link is the confirmation link of the message's text: an https URL on
	// mail-settings.google.com whose path starts /mail/vf-, or "".
	Link string
	// What the check reads: how many of each header field there are, the
	// From address, the addresses of To, and the address the body names as
	// where mail will be forwarded ("" when the body is not the template).
	froms, tos, subjects int
	from                 string
	to                   []string
	named                string
	// template is the language of the forwarding template the Subject is
	// ("" when it is none), and the requester comes from that Subject. Only
	// such a confirmation may be verified, and only with a body in the same
	// template: the body is what recognition may read, never what
	// verification decides by.
	template string
}

const gmailForwarding = "forwarding-noreply@google.com"

// Gmail's forwarding confirmation, in the templates it is known in (ADR 0026,
// "Gmail's forwarding confirmation"). In English the subject is
// "(#123456789) Gmail Forwarding Confirmation - Receive Mail from
// someone@gmail.com", or, as Google sends it since 2026, the same with no code
// and a "(" left of its prefix, and the body says "someone@gmail.com has
// requested to automatically forward mail to your email address
// you@example.com". In Spanish the subject is "Confirmación de reenvío de
// Gmail: recibir correo de someone@gmail.com" and the body says
// "someone@gmail.com ha solicitado reenviar automáticamente el correo a tu
// dirección you@example.com". Either body then carries the link that
// confirms the request (https://mail-settings.google.com/mail/vf-…), and once
// carried "Confirmation code: 123456789". Another language is recognised only
// by a code in its subject or its body, and is never verified.
var (
	// gmailTemplates: each subject's first group is the code (always empty
	// in Spanish, whose template never carried one) and its second the
	// requester; each body's second group is the address it names.
	gmailTemplates = []struct {
		lang          string
		subject, body *regexp.Regexp
	}{
		{
			"en",
			regexp.MustCompile(`^(?:\(#(\d{6,12})\) |\()?Gmail Forwarding Confirmation - Receive Mail from (\S+@\S+)$`),
			regexp.MustCompile(`(?i)(\S+@\S+)\s+has\s+requested\s+to\s+automatically\s+forward\s+mail\s+to\s+your\s+email\s+address\s+<?([^\s<>]+@[^\s<>]+?)>?[.,]?(?:\s|$)`),
		},
		{
			"es",
			regexp.MustCompile(`^()Confirmación de reenvío de Gmail: recibir correo de (\S+@\S+)$`),
			regexp.MustCompile(`(?i)(\S+@\S+)\s+ha\s+solicitado\s+reenviar\s+automáticamente\s+el\s+correo\s+a\s+tu\s+dirección\s+<?([^\s<>]+@[^\s<>]+?)>?[.,]?(?:\s|$)`),
		},
	}
	reSubjectCode = regexp.MustCompile(`^\(#(\d{6,12})\) `)
	reForwardCode = regexp.MustCompile(`(?i)confirmation code:\s*(\d{6,12})`)
	// reForwardLink finds the confirmation link in the text; gmailLink then
	// checks what it found.
	reForwardLink = regexp.MustCompile(`https://mail-settings\.google\.com/mail/vf-[A-Za-z0-9%._~-]*`)
)

// MaxConfirmationLink bounds the confirmation link kept (Google's are about
// 200 bytes): a longer one is not kept at all.
const MaxConfirmationLink = 1024

// gmailLink is the first confirmation link of text, or "": an https URL whose
// host is exactly mail-settings.google.com and whose path starts /mail/vf-,
// with no user, port, query or fragment, at most MaxConfirmationLink bytes,
// written only with letters, digits, "%", ".", "_", "~" and "-". The
// expression stops at the first character outside them, so a link written in
// a sentence loses its final "." too.
func gmailLink(text string) string {
	l := strings.TrimRight(reForwardLink.FindString(text), ".")
	if len(l) > MaxConfirmationLink {
		return ""
	}
	u, err := url.Parse(l)
	if l == "" || err != nil || u.Scheme != "https" || u.Host != "mail-settings.google.com" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.EscapedPath(), "/mail/vf-") || len(u.EscapedPath()) == len("/mail/vf-") {
		return ""
	}
	return l
}

// Verdict says what the enclave may say of a confirmation that arrived for
// the mailbox at address, with the signatures checked at delivery: verified
// when Google signed exactly this message for this mailbox, and for another
// address when Google signed it for someone else's (ADR 0026). Both are false
// while VerifyGmail is off. A code plays no part.
func (c *Confirmation) Verdict(auth []AuthResult, address string) (verified, forAnother bool) {
	if !VerifyGmail || c.template == "" || !c.signed(auth) || len(c.to) != 1 || c.named == "" {
		return false, false
	}
	to, named := strings.ToLower(c.to[0]), strings.ToLower(c.named)
	switch address = strings.ToLower(address); {
	case to == address && named == address:
		return true, false
	case to != address && named == to:
		return false, true
	}
	return false, false
}

// signed is every check but which address: one From, To and Subject; From
// exactly Gmail's forwarding service; and a passing signature of google.com
// over From, To and Subject and the whole body. Verdict also needs the
// signed Subject to be a template, which the requester comes from, and the
// body to be the same template.
func (c *Confirmation) signed(auth []AuthResult) bool {
	if c.froms != 1 || c.tos != 1 || c.subjects != 1 || c.from != gmailForwarding {
		return false
	}
	return slices.ContainsFunc(auth, func(a AuthResult) bool {
		return a.Method == "dkim" && a.Result == "pass" && a.Domain == "google.com" && !a.BodyLimited &&
			slices.Contains(a.Headers, "from") && slices.Contains(a.Headers, "to") && slices.Contains(a.Headers, "subject")
	})
}

// Parse reads a message within the limits of ADR 0026 ("What a message may
// hold"). Past one it stops reading and sets Partial; a message that cannot
// be read as mail at all is Unparsed. Mail is never refused for its content.
// ctx is the delivery's: when it ends, parsing stops between parts and
// between the tokens of HTML, and what was read is Partial.
func Parse(ctx context.Context, raw []byte) Parsed {
	var p Parsed
	head, partial := boundHeader(raw)
	// An unknown charset or transfer encoding still gives a message to read.
	e, _ := readEntity(head)
	if e == nil {
		p.Meta = StoredMeta{Unparsed: true, From: []contracts.MailAddress{}, To: []contracts.MailAddress{}, Cc: []contracts.MailAddress{}, ReplyTo: []contracts.MailAddress{}, Attachments: []contracts.MessageAttachment{}, Auth: []contracts.AuthCheck{}}
		return p
	}
	h := mail.Header{Header: e.Header}
	m := &p.Meta
	m.MessageID, _ = h.MessageID()
	m.MessageID = cut(m.MessageID, MaxHeaderFieldBytes, &partial)
	m.Subject, _ = h.Subject()
	m.Subject = cut(m.Subject, MaxSubjectBytes, &partial)
	m.Date = cut(h.Get("Date"), MaxHeaderFieldBytes, &partial)
	m.From = addressList(h, "From", &partial)
	m.To = addressList(h, "To", &partial)
	m.Cc = addressList(h, "Cc", &partial)
	m.ReplyTo = addressList(h, "Reply-To", &partial)
	m.Auth = []contracts.AuthCheck{}

	w := walkMessage(ctx, raw, MessageTextMax, true, false)
	partial = partial || w.partial
	text, _, _ := w.text(ctx, SearchTextMax)
	p.Text = text
	m.Snippet = snippet(text)
	m.Attachments = w.attachments
	if m.Attachments == nil {
		m.Attachments = []contracts.MessageAttachment{}
	}
	m.Partial = partial
	plain := ""
	if w.plainSeen {
		plain, _ = readPlain(w.plain, MessageTextMax)
	}
	p.Confirmation = recognise(h, m, plain, text)
	m.Confirmation = p.Confirmation != nil
	boundMeta(m)
	return p
}

// MaxMetaBytes bounds a message's metadata as it is serialised (StoredMeta,
// in JSON), before it is sealed into the message's item: every field within
// its own limit can still add up, with JSON's escapes, to more than the
// 400 KB a DynamoDB item may hold. 256 KiB leaves more than 130 KB for the
// seal and the rest of the item (identifiers, times, sizes).
const MaxMetaBytes = 256 << 10

func metaSize(m *StoredMeta) int {
	b, _ := json.Marshal(m)
	return len(b)
}

// boundMeta keeps a message's metadata within MaxMetaBytes, checked before
// it is written: past it the message is Partial and its fields are emptied
// in this order until it fits: the lists of recipients, the Message-ID and
// the Date, the attachments' names, the senders and the snippet, and last
// what remains is cut to a fixed size. The raw message is stored whole.
func boundMeta(m *StoredMeta) {
	if metaSize(m) <= MaxMetaBytes {
		return
	}
	m.Partial = true
	for _, shrink := range []func(){
		func() {
			m.To, m.Cc, m.ReplyTo = []contracts.MailAddress{}, []contracts.MailAddress{}, []contracts.MailAddress{}
		},
		func() { m.MessageID, m.Date = "", "" },
		func() {
			for i := range m.Attachments {
				m.Attachments[i].Name = ""
			}
		},
		func() { m.From, m.Snippet = []contracts.MailAddress{}, "" },
		func() {
			var partial bool
			m.EnvelopeFrom = cut(m.EnvelopeFrom, 255, &partial)
			for i := range m.Auth {
				m.Auth[i].Domain = cut(m.Auth[i].Domain, 255, &partial)
				m.Auth[i].Result = cut(m.Auth[i].Result, 255, &partial)
			}
		},
	} {
		shrink()
		if metaSize(m) <= MaxMetaBytes {
			return
		}
	}
}

// readEntity reads a message's header, within MaxHeaderBytes, and gives its
// body decoded (transfer encoding and charset). An unknown charset or
// encoding still gives an entity, with an error that says so.
func readEntity(b []byte) (*message.Entity, error) {
	h, body, err := readHeader(b)
	if err != nil {
		return nil, err
	}
	return message.New(h, body)
}

// readHeader reads a message's header block, within MaxHeaderBytes, and
// returns it and the rest of the message, its body as it is written.
func readHeader(b []byte) (message.Header, io.Reader, error) {
	lr := &headerLimit{r: bytes.NewReader(b), n: MaxHeaderBytes + 4}
	br := bufio.NewReader(lr)
	h, err := textproto.ReadHeader(br)
	if err != nil {
		return message.Header{}, nil, err
	}
	lr.n = math.MaxInt64
	return message.Header{Header: h}, br, nil
}

// errHeaderTooLong is a header block past MaxHeaderBytes.
var errHeaderTooLong = errors.New("inbox: a header longer than its limit")

// headerLimit is io.LimitedReader with an error of its own at the limit, so
// a header cut there is never read as a header that ended.
type headerLimit struct {
	r io.Reader
	n int64
}

func (l *headerLimit) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, errHeaderTooLong
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

// boundHeader returns raw, or, when its header block is longer than
// MaxHeaderBytes, the complete header lines within it and no body.
func boundHeader(raw []byte) ([]byte, bool) {
	end := bytes.Index(raw, []byte("\r\n\r\n"))
	if alt := bytes.Index(raw, []byte("\n\n")); end < 0 || (alt >= 0 && alt < end) {
		end = alt
	}
	if (end >= 0 && end <= MaxHeaderBytes) || (end < 0 && len(raw) <= MaxHeaderBytes) {
		return raw, false
	}
	head := raw[:MaxHeaderBytes]
	if i := bytes.LastIndexByte(head, '\n'); i >= 0 {
		head = head[:i+1]
	}
	out := append(bytes.Clone(head), "\r\n"...)
	return out, true
}

// cut shortens s to at most n bytes, on a character's boundary, and says so.
func cut(s string, n int, partial *bool) string {
	if len(s) <= n {
		return s
	}
	*partial = true
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func addressList(h mail.Header, key string, partial *bool) []contracts.MailAddress {
	out := []contracts.MailAddress{}
	v := h.Get(key)
	if v == "" {
		return out
	}
	v = cut(v, MaxHeaderFieldBytes, partial)
	list, err := mail.ParseAddressList(v)
	if err != nil {
		// What could not be read as addresses is kept as written.
		return append(out, contracts.MailAddress{Address: v})
	}
	for _, a := range list {
		if len(out) == MaxAddressesPerField {
			*partial = true
			break
		}
		out = append(out, contracts.MailAddress{Name: a.Name, Address: a.Address})
	}
	return out
}

// recognise returns the confirmation a message is, or nil: exactly one From
// header, its address exactly Gmail's forwarding service, and a subject that
// is one of Gmail's forwarding templates or a code in the subject or the body.
// The envelope sender plays no part: nobody signs it.
func recognise(h mail.Header, m *StoredMeta, plain, text string) *Confirmation {
	c := &Confirmation{
		froms:    len(h.Values("From")),
		tos:      len(h.Values("To")),
		subjects: len(h.Values("Subject")),
	}
	if c.froms != 1 || len(m.From) != 1 || strings.ToLower(m.From[0].Address) != gmailForwarding {
		return nil
	}
	c.from = gmailForwarding
	var body *regexp.Regexp
	for _, t := range gmailTemplates {
		if s := t.subject.FindStringSubmatch(m.Subject); s != nil {
			c.Code, c.Requester, c.template, body = s[1], s[2], t.lang, t.body
			break
		}
	}
	if c.template == "" {
		if s := reSubjectCode.FindStringSubmatch(m.Subject); s != nil {
			c.Code = s[1]
		} else if s := reForwardCode.FindStringSubmatch(text); s != nil {
			c.Code = s[1]
		} else {
			return nil
		}
	}
	for _, a := range m.To {
		c.to = append(c.to, a.Address)
	}
	if body != nil {
		if s := body.FindStringSubmatch(plain); s != nil {
			c.named = s[2]
		}
	}
	c.Link = gmailLink(plain)
	return c
}

func snippet(text string) string {
	s := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(s) <= snippetRunes {
		return s
	}
	return string([]rune(s)[:snippetRunes]) + "…"
}
