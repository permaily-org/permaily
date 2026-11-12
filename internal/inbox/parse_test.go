package inbox_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/emersion/go-msgauth/dkim"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/msgauth"
)

const address = "abcdefghijkmnpqrstuvw@inbox.permaily.test"

const plainMessage = "From: Alice <alice@example.org>\r\n" +
	"To: someone@gmail.com, Bob <bob@example.org>\r\n" +
	"Cc: carol@example.org\r\n" +
	"Reply-To: replies@example.org\r\n" +
	"Subject: Your flight to Lisbon\r\n" +
	"Date: Mon, 09 Nov 2026 10:00:00 +0000\r\n" +
	"Message-ID: <abc@example.org>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=b\r\n" +
	"\r\n" +
	"--b\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nBooking ref ABC123. Departure 08:15.\r\n" +
	"--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=ticket.pdf\r\n\r\n%PDF-1.7 fake\r\n" +
	"--b--\r\n"

func TestParse(t *testing.T) {
	p := inbox.Parse([]byte(plainMessage))
	m := p.Meta
	if m.Subject != "Your flight to Lisbon" || m.MessageID != "abc@example.org" || m.Date == "" || m.Partial || m.Unparsed {
		t.Fatalf("%+v", m)
	}
	if len(m.From) != 1 || m.From[0] != (contracts.MailAddress{Name: "Alice", Address: "alice@example.org"}) ||
		len(m.To) != 2 || len(m.Cc) != 1 || len(m.ReplyTo) != 1 {
		t.Fatalf("addresses %+v", m)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].Name != "ticket.pdf" || m.Attachments[0].Type != "application/pdf" || m.Attachments[0].Size == 0 {
		t.Fatalf("attachments %+v", m.Attachments)
	}
	if !strings.HasPrefix(p.Text, "Booking ref ABC123") || m.Snippet != "Booking ref ABC123. Departure 08:15." || p.Confirmation != nil || m.Confirmation {
		t.Fatalf("text %q %q", p.Text, m.Snippet)
	}
}

func TestParseKeepsWhatCannotBeRead(t *testing.T) {
	p := inbox.Parse([]byte("this is not mail at all\r\nno header here\r\n\r\n"))
	if !p.Meta.Unparsed || p.Meta.From == nil || p.Meta.Attachments == nil {
		t.Fatalf("%+v", p.Meta)
	}
	p = inbox.Parse([]byte("From: not an address <<<\r\nSubject: x\r\n\r\nbody\r\n"))
	if p.Meta.Unparsed || len(p.Meta.From) != 1 || p.Meta.From[0].Address == "" {
		t.Fatalf("an address kept as written: %+v", p.Meta.From)
	}
	long := strings.Repeat("word ", 100)
	if got := inbox.Parse([]byte("Subject: x\r\n\r\n" + long)).Meta.Snippet; len([]rune(got)) != 201 || !strings.HasSuffix(got, "…") {
		t.Fatalf("snippet %q", got)
	}
}

// The limits of ADR 0026, "What a message may hold": past each, the message
// is read as far as the limit and marked Partial.
func TestParseLimits(t *testing.T) {
	addrs := make([]string, inbox.MaxAddressesPerField+1)
	for i := range addrs {
		addrs[i] = fmt.Sprintf("a%d@example.org", i)
	}
	nested := "Content-Type: text/plain\r\n\r\ndeep\r\n"
	for i := range inbox.MaxMIMEDepth + 2 {
		nested = fmt.Sprintf("Content-Type: multipart/mixed; boundary=b%d\r\n\r\n--b%d\r\n%s\r\n--b%d--\r\n", i, i, nested, i)
	}
	var parts strings.Builder
	for range inbox.MaxMIMEParts + 1 {
		parts.WriteString("--p\r\nContent-Type: text/plain\r\n\r\nx\r\n")
	}
	var attachments strings.Builder
	for i := range inbox.MaxAttachmentsListed + 1 {
		fmt.Fprintf(&attachments, "--p\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=f%d.bin\r\n\r\nx\r\n", i)
	}
	cases := map[string]string{
		"the header block": "Subject: x\r\n" + strings.Repeat("X-Pad: "+strings.Repeat("p", 990)+"\r\n", inbox.MaxHeaderBytes/1000+1) + "\r\nbody\r\n",
		"one header field": "To: " + strings.Repeat("x", inbox.MaxHeaderFieldBytes+10) + "@example.org\r\n\r\nbody\r\n",
		"addresses":        "To: " + strings.Join(addrs, ", ") + "\r\n\r\nbody\r\n",
		"the subject":      "Subject: " + strings.Repeat("é", inbox.MaxSubjectBytes) + "\r\n\r\nbody\r\n",
		"nesting":          nested,
		"parts":            "Content-Type: multipart/mixed; boundary=p\r\n\r\n" + parts.String() + "--p--\r\n",
		"attachments":      "Content-Type: multipart/mixed; boundary=p\r\n\r\n" + attachments.String() + "--p--\r\n",
		"an attachment's name": "Content-Type: multipart/mixed; boundary=p\r\n\r\n--p\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"" +
			strings.Repeat("n", inbox.MaxAttachmentName+5) + "\"\r\n\r\nx\r\n--p--\r\n",
	}
	for name, raw := range cases {
		p := inbox.Parse([]byte(raw))
		if !p.Meta.Partial {
			t.Errorf("%s: not marked partial", name)
		}
		switch name {
		case "addresses":
			if len(p.Meta.To) != inbox.MaxAddressesPerField {
				t.Errorf("addresses kept: %d", len(p.Meta.To))
			}
		case "the subject":
			if len(p.Meta.Subject) > inbox.MaxSubjectBytes || !strings.HasPrefix(p.Meta.Subject, "é") {
				t.Errorf("subject of %d bytes", len(p.Meta.Subject))
			}
		case "attachments":
			if len(p.Meta.Attachments) != inbox.MaxAttachmentsListed {
				t.Errorf("attachments listed: %d", len(p.Meta.Attachments))
			}
		case "an attachment's name":
			if len(p.Meta.Attachments[0].Name) != inbox.MaxAttachmentName {
				t.Errorf("name of %d bytes", len(p.Meta.Attachments[0].Name))
			}
		case "the header block":
			if p.Meta.Subject != "x" {
				t.Errorf("the start of a long header is read: %q", p.Meta.Subject)
			}
		}
	}
}

func TestMessageText(t *testing.T) {
	html := "From: a@example.org\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		"<html><head><title>T</title><style>p{}</style></head><body><p>Hello&nbsp;<b>you</b> &amp; me</p>" +
		"<script>alert(1)</script><!-- hidden --><div>Read <a href=\"https://example.org/x?a=1&amp;b=2\">this</a> and " +
		"<a href='https://example.org/'>https://example.org/</a><br>bye</div><a>no target</a></body></html>"
	text, from, truncated := inbox.MessageText([]byte(html), 1<<10)
	want := "Hello you & me\n\nRead this [https://example.org/x?a=1&b=2] and https://example.org/\nbye\nno target"
	if text != want || from != contracts.TextFromHTML || truncated {
		t.Fatalf("%q %s %v", text, from, truncated)
	}
	text, from, truncated = inbox.MessageText([]byte(plainMessage), 10)
	if text != "Booking re" || from != contracts.TextFromPlain || !truncated {
		t.Fatalf("%q %s %v", text, from, truncated)
	}
	if text, from, _ := inbox.MessageText([]byte("Subject: x\r\nContent-Type: image/png\r\n\r\nxx"), 10); text != "" || from != contracts.TextFromNone {
		t.Fatalf("%q %s", text, from)
	}
	for _, odd := range []string{"<p", "a < b", "<!-- open", "<script>never closed", "<a href=>x</a>", "<a href=\"open>x", "<a hreff=x href=y>z</a>", "<a href=", "<!DOCTYPE html>x"} {
		_, _, _ = inbox.MessageText([]byte("Content-Type: text/html\r\n\r\n"+odd), 100)
	}
	if got, _, _ := inbox.MessageText([]byte("Content-Type: text/html\r\n\r\n<a hreff=x href=y>z</a>"), 100); got != "z [y]" {
		t.Fatalf("%q", got)
	}
}

// ---- Gmail's forwarding confirmation (ADR 0026) ----

// googleKey signs messages as google.com in these tests; resolver answers its
// record, as DNS would.
var googlePub, googleKey, _ = ed25519.GenerateKey(rand.Reader)

func resolver(_ context.Context, name string) ([]string, error) {
	if name != "test._domainkey.google.com" {
		return nil, fmt.Errorf("no such record %s", name)
	}
	return []string{"v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(googlePub)}, nil
}

func confirmation(from, to, body string) string {
	return "From: Gmail Team <" + from + ">\r\n" +
		"To: " + to + "\r\n" +
		"Subject: (#123456789) Gmail Forwarding Confirmation - Receive Mail from someone@gmail.com\r\n" +
		"\r\n" + body
}

func template(named string) string {
	return "someone@gmail.com has requested to automatically forward mail to your email\r\naddress " + named + ".\r\n" +
		"Confirmation code: 123456789\r\n"
}

func sign(t *testing.T, raw string, headers ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := dkim.Sign(&out, strings.NewReader(raw), &dkim.SignOptions{Domain: "google.com", Selector: "test", Signer: googleKey, HeaderKeys: headers}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func verdict(t *testing.T, raw string) (recognised, verified, other bool) {
	t.Helper()
	auth := msgauth.DKIM(resolver)(context.Background(), []byte(raw))
	p := inbox.Parse([]byte(raw))
	if p.Confirmation == nil {
		return false, false, false
	}
	verified, other = p.Confirmation.Verdict(auth, address)
	return true, verified, other
}

func withVerify(t *testing.T) {
	t.Helper()
	inbox.VerifyGmail = true
	t.Cleanup(func() { inbox.VerifyGmail = false })
}

func TestGmailConfirmationIsRecognised(t *testing.T) {
	raw := confirmation("forwarding-noreply@google.com", address, template(address))
	p := inbox.Parse([]byte(raw))
	if p.Confirmation == nil || p.Confirmation.Code != "123456789" || p.Confirmation.Requester != "someone@gmail.com" || !p.Meta.Confirmation {
		t.Fatalf("%+v", p.Confirmation)
	}
	// A code in the body alone is enough to recognise one.
	p = inbox.Parse([]byte("From: forwarding-noreply@google.com\r\nSubject: hi\r\n\r\nConfirmation code: 987654321\r\n"))
	if p.Confirmation == nil || p.Confirmation.Code != "987654321" || p.Confirmation.Requester != "" {
		t.Fatalf("%+v", p.Confirmation)
	}
	// No code: an ordinary message.
	if inbox.Parse([]byte("From: forwarding-noreply@google.com\r\nSubject: hi\r\n\r\nhello\r\n")).Confirmation != nil {
		t.Fatal("recognised without a code")
	}
}

// The mark is off: every confirmation is stored not verified.
func TestGmailVerifiedIsOff(t *testing.T) {
	if recognised, verified, other := verdict(t, sign(t, confirmation("forwarding-noreply@google.com", address, template(address)), "From", "To", "Subject")); !recognised || verified || other {
		t.Fatal(recognised, verified, other)
	}
}

// The predicate, on messages signed in the test with a test key: it passes
// on Google's message for this mailbox, and each check fails alone.
func TestGmailPredicate(t *testing.T) {
	withVerify(t)
	good := confirmation("forwarding-noreply@google.com", address, template(address))
	if recognised, verified, other := verdict(t, sign(t, good, "From", "To", "Subject")); !recognised || !verified || other {
		t.Fatal("Google's own message for this mailbox", recognised, verified, other)
	}
	another := "someone.else@inbox.permaily.test"
	if _, verified, other := verdict(t, sign(t, confirmation("forwarding-noreply@google.com", another, template(another)), "From", "To", "Subject")); verified || !other {
		t.Fatal("a genuine confirmation for another address", verified, other)
	}
	cases := map[string]string{
		"two From":        sign(t, "From: forwarding-noreply@google.com\r\n"+good, "From", "To", "Subject"),
		"two To":          sign(t, "To: "+address+"\r\n"+good, "From", "To", "Subject"),
		"two Subject":     sign(t, "Subject: (#123456789) Gmail Forwarding Confirmation - Receive Mail from someone@gmail.com\r\n"+good, "From", "To", "Subject"),
		"To not signed":   sign(t, good, "From", "Subject"),
		"not signed":      good,
		"another To":      sign(t, confirmation("forwarding-noreply@google.com", "x@inbox.permaily.test", template(address)), "From", "To", "Subject"),
		"two in To":       sign(t, confirmation("forwarding-noreply@google.com", address+", x@example.org", template(address)), "From", "To", "Subject"),
		"another named":   sign(t, confirmation("forwarding-noreply@google.com", address, template("x@inbox.permaily.test")), "From", "To", "Subject"),
		"not a template":  sign(t, confirmation("forwarding-noreply@google.com", address, "Confirmation code: 123456789\r\n"), "From", "To", "Subject"),
		"body changed":    strings.Replace(sign(t, good, "From", "To", "Subject"), "Confirmation code", "Confirmation  code", 1),
		"another service": sign(t, confirmation("calendar-notification@google.com", address, template(address)), "From", "To", "Subject"),
		"envelope only":   sign(t, confirmation("someone@example.org", address, template(address)), "From", "To", "Subject"),
	}
	for name, raw := range cases {
		if _, verified, _ := verdict(t, raw); verified {
			t.Errorf("%s: verified", name)
		}
	}
	// A signature that covers part of the body (l=) is never enough, whatever
	// the verifier says of it; nor one of another domain.
	p := inbox.Parse([]byte(good))
	for name, auth := range map[string][]inbox.AuthResult{
		"l=":             {{Method: "dkim", Domain: "google.com", Result: "pass", Headers: []string{"from", "to", "subject"}, BodyLimited: true}},
		"another domain": {{Method: "dkim", Domain: "gmail.com", Result: "pass", Headers: []string{"from", "to", "subject"}}},
		"a failure":      {{Method: "dkim", Domain: "google.com", Result: "fail: x", Headers: []string{"from", "to", "subject"}}},
	} {
		if verified, other := p.Confirmation.Verdict(auth, address); verified || other {
			t.Errorf("%s: %v %v", name, verified, other)
		}
	}
	if verified, _ := p.Confirmation.Verdict([]inbox.AuthResult{{Method: "dkim", Domain: "google.com", Result: "pass", Headers: []string{"from", "to", "subject"}}}, address); !verified {
		t.Fatal("the predicate by itself")
	}
}

// A Calendar invitation signed by google.com is not a confirmation at all.
func TestCalendarInvitationIsNoConfirmation(t *testing.T) {
	withVerify(t)
	raw := sign(t, "From: Google Calendar <calendar-notification@google.com>\r\nTo: "+address+"\r\nSubject: Invitation\r\n\r\n"+template(address), "From", "To", "Subject")
	if recognised, verified, _ := verdict(t, raw); recognised || verified {
		t.Fatal("a Calendar invitation")
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(plainMessage))
	f.Add([]byte(confirmation("forwarding-noreply@google.com", address, template(address))))
	f.Add([]byte("Content-Type: text/html\r\n\r\n<p>x<a href=y>z</a>"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		p := inbox.Parse(raw)
		if p.Meta.From == nil || p.Meta.To == nil || p.Meta.Attachments == nil || len(p.Text) > inbox.SearchTextMax {
			t.Fatalf("%+v", p.Meta)
		}
		_, _, _ = inbox.MessageText(raw, 100)
	})
}
