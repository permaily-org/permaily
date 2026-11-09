package inbox

import (
	"bytes"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	_ "github.com/emersion/go-message/charset" // decode non-UTF-8 charsets
	"github.com/emersion/go-message/mail"
)

// Meta is what the inbox extracts from a message. It is stored sealed.
type Meta struct {
	EnvelopeFrom           string       `json:"envelope_from"`
	EnvelopeTo             []string     `json:"envelope_to"`
	MessageID              string       `json:"message_id,omitempty"`
	From                   []string     `json:"from,omitempty"`
	To                     []string     `json:"to,omitempty"`
	Cc                     []string     `json:"cc,omitempty"`
	Subject                string       `json:"subject,omitempty"`
	Date                   string       `json:"date,omitempty"`
	ListID                 string       `json:"list_id,omitempty"`
	Snippet                string       `json:"snippet,omitempty"`
	Attachments            []Attachment `json:"attachments,omitempty"`
	Auth                   []AuthResult `json:"auth,omitempty"`
	ForwardingConfirmation bool         `json:"forwarding_confirmation,omitempty"`
	ParseError             string       `json:"parse_error,omitempty"`
}

// Attachment describes an attachment (its bytes stay in the raw message).
type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

const snippetRunes = 200

// parse extracts metadata. A message that cannot be parsed is still stored,
// with ParseError set, so no mail is lost.
func parse(raw []byte) (meta Meta, text string) {
	r, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		meta.ParseError = err.Error()
		return meta, ""
	}
	h := r.Header
	meta.MessageID, _ = h.MessageID()
	meta.Subject, _ = h.Subject()
	meta.Date = h.Get("Date")
	meta.ListID = h.Get("List-Id")
	meta.From = addresses(h, "From")
	meta.To = addresses(h, "To")
	meta.Cc = addresses(h, "Cc")

	var body strings.Builder
	for {
		p, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			meta.ParseError = err.Error()
			break
		}
		switch ph := p.Header.(type) {
		case *mail.InlineHeader:
			ct, _, _ := ph.ContentType()
			if ct == "text/plain" && body.Len() < 64<<10 {
				b, _ := io.ReadAll(io.LimitReader(p.Body, 64<<10))
				body.Write(b)
			} else {
				_, _ = io.Copy(io.Discard, p.Body)
			}
		case *mail.AttachmentHeader:
			name, _ := ph.Filename()
			ct, _, _ := ph.ContentType()
			n, _ := io.Copy(io.Discard, p.Body)
			meta.Attachments = append(meta.Attachments, Attachment{Filename: name, ContentType: ct, Size: n})
		}
	}
	text = body.String()
	meta.Snippet = snippet(text)
	return meta, text
}

func addresses(h mail.Header, key string) []string {
	list, err := h.AddressList(key)
	if err != nil {
		if v := h.Get(key); v != "" {
			return []string{v}
		}
		return nil
	}
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.Address)
	}
	return out
}

func snippet(text string) string {
	s := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(s) <= snippetRunes {
		return s
	}
	return string([]rune(s)[:snippetRunes]) + "…"
}

// Gmail's forwarding confirmation: subject "(#123456789) Gmail Forwarding
// Confirmation - Receive Mail from someone@gmail.com", body "Confirmation code: 123456789".
var (
	reForwardSubject = regexp.MustCompile(`\(#(\d{6,12})\).*Forwarding Confirmation.*Receive Mail from (\S+@\S+)`)
	reForwardCode    = regexp.MustCompile(`(?i)confirmation code:\s*(\d{6,12})`)
)

const gmailForwarding = "forwarding-noreply@google.com"

// forwardingConfirmation returns the code and requesting address when meta is
// Gmail's forwarding confirmation.
func forwardingConfirmation(meta Meta, text string) (code, requester string, ok bool) {
	fromGoogle := strings.EqualFold(meta.EnvelopeFrom, gmailForwarding)
	for _, f := range meta.From {
		fromGoogle = fromGoogle || strings.EqualFold(f, gmailForwarding)
	}
	if !fromGoogle {
		return "", "", false
	}
	if m := reForwardSubject.FindStringSubmatch(meta.Subject); m != nil {
		return m[1], m[2], true
	}
	if m := reForwardCode.FindStringSubmatch(text); m != nil {
		return m[1], "", true
	}
	return "", "", false
}
