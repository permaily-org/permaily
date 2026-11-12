package httpapi_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dkim"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/msgauth"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/services/mailboxes/mailboxestest"
	"github.com/permaily-org/permaily/internal/store"
)

// The person's routes on the real thing: *mailboxes.Service and inbox's
// Reader over one store, as the enclave composes them, with mail written by
// the service's own delivery (ADR 0026, pull request 2: R3 against delivery,
// two instances over one store, and the frame's guarantees on the real
// routes).

// realWorld is one store, its commits and keys, shared by every instance
// (enclave) made on it.
type realWorld struct {
	t        *testing.T
	mem      *store.Memory
	commits  *store.MemoryCommits
	keys     *keys.Local
	accounts *mailboxestest.Accounts
	logs     *syncBuffer
	mu       sync.Mutex
	now      time.Time
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newRealWorld(t *testing.T) *realWorld {
	t.Helper()
	k, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	w := &realWorld{
		t: t, mem: store.NewMemory(), commits: store.NewMemoryCommits(), keys: k, accounts: mailboxestest.NewAccounts(),
		logs: &syncBuffer{}, now: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC),
	}
	w.mem.Now = w.clock
	return w
}

func (w *realWorld) clock() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.now
}

func (w *realWorld) add(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.now = w.now.Add(d)
}

// realInstance is one enclave: its own service, with its own strict records,
// and its own routes.
type realInstance struct {
	w      *realWorld
	svc    *mailboxes.Service
	opened *watched
	reader *watchedReader
	h      http.Handler
}

const realDomain = "inbox.permaily.test"

func (w *realWorld) instance() *realInstance {
	w.t.Helper()
	log := slog.New(slog.NewJSONHandler(w.logs, nil))
	svc, err := mailboxes.New(mailboxes.Config{
		Records: &store.Anchored{Records: w.mem, Commits: w.commits, OnlyNewest: true, Strict: true, Now: w.clock},
		Storage: w.mem, Keys: w.keys, Accounts: w.accounts, Domain: realDomain, Log: log, Now: w.clock,
		Auth: msgauth.DKIM(googleResolver),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	i := &realInstance{w: w, svc: svc, opened: &watched{MailboxesService: svc}, reader: &watchedReader{Reader: inbox.NewReader(w.mem)}}
	until := w.clock().Add(time.Hour)
	i.h = httpapi.MailboxesAPI(httpapi.MailboxesConfig{
		Service: i.opened, Reader: i.reader, Log: log, Now: w.clock,
		Sessions: sessions{
			"alice": {AccountID: alice, Session: contracts.Session{ID: "ses_a", ConfirmedUntil: until}},
			"bob":   {AccountID: bob, Session: contracts.Session{ID: "ses_c", ConfirmedUntil: until}},
		},
	})
	return i
}

func (i *realInstance) call(cookie, method, path, body string) *httptest.ResponseRecorder {
	i.w.t.Helper()
	return serve(i.h, cookie, method, path, body)
}

// make makes a mailbox through the routes.
func (i *realInstance) make(cookie, label string) contracts.Mailbox {
	i.w.t.Helper()
	var m contracts.Mailbox
	read(i.w.t, i.call(cookie, "POST", "/api/v1/mailboxes", `{"label":"`+label+`"}`), 201, &m)
	return m
}

// deliver hands raw to the instance as SMTP does: Accept for each address at
// RCPT TO, then Deliver.
func (i *realInstance) deliver(raw string, to ...string) {
	i.w.t.Helper()
	ctx := context.Background()
	var ids []string
	for _, a := range to {
		id, err := i.svc.Accept(ctx, a)
		if err != nil {
			i.w.t.Fatalf("accept: %v", err)
		}
		ids = append(ids, id)
	}
	if err := i.svc.Deliver(ctx, "sender@example.org", ids, []byte(raw)); err != nil {
		i.w.t.Fatalf("deliver: %v", err)
	}
	i.w.add(time.Second) // a message identifier carries its time
}

// page lists or searches a mailbox's messages through the routes.
func (i *realInstance) page(cookie, mailbox, query string) contracts.MessagePage {
	i.w.t.Helper()
	var p contracts.MessagePage
	read(i.w.t, i.call(cookie, "GET", "/api/v1/mailboxes/"+mailbox+"/messages"+query, ""), 200, &p)
	return p
}

// googleKey signs as google.com here; googleResolver answers its record.
var googlePub, googleKey, _ = ed25519.GenerateKey(rand.Reader)

func googleResolver(_ context.Context, name string) ([]string, error) {
	if name != "test._domainkey.google.com" {
		return nil, errors.New("no such record")
	}
	return []string{"v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(googlePub)}, nil
}

// gmailConfirmation is Gmail's forwarding confirmation for the address to,
// signed by google.com over From, To and Subject and the whole body.
func gmailConfirmation(t *testing.T, to, requester string) string {
	t.Helper()
	raw := "From: Gmail Team <forwarding-noreply@google.com>\r\nTo: " + to + "\r\n" +
		"Subject: (#123456789) Gmail Forwarding Confirmation - Receive Mail from " + requester + "\r\n\r\n" +
		requester + " has requested to automatically forward mail to your email address " + to + ".\r\nConfirmation code: 123456789\r\n"
	var out bytes.Buffer
	if err := dkim.Sign(&out, strings.NewReader(raw), &dkim.SignOptions{Domain: "google.com", Selector: "test", Signer: googleKey, HeaderKeys: []string{"From", "To", "Subject"}}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// partsRaw has every kind of part the walk decides on: text and HTML
// alternatives, an inline image (not an attachment), a text part sent as an
// attachment, base64, a message inside the message, base64 that breaks half
// way, an encoding nobody knows, and a part with no Content-Type (text).
func partsRaw(to string) string {
	return "From: Ana <ana@example.org>\r\nTo: " + to + "\r\nSubject: Parts\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: multipart/alternative; boundary=c\r\n\r\n" +
		"--c\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nThe plain text, r3-plain.\r\n" +
		"--c\r\nContent-Type: text/html\r\n\r\n<p>Not this one</p>\r\n--c--\r\n" +
		"--b\r\nContent-Type: image/png\r\nContent-Disposition: inline; filename=\"inline.png\"\r\n\r\nPNG\r\n" +
		"--b\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=\"notes.txt\"\r\n\r\nnotes\r\n" +
		"--b\r\nContent-Type: application/pdf\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=\"ticket.pdf\"\r\n\r\nJVBERi0xLjQK\r\n" +
		"--b\r\nContent-Type: message/rfc822\r\n\r\nFrom: x@example.org\r\nSubject: inner\r\n\r\ninner body\r\n" +
		"--b\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=\"broken.bin\"\r\n\r\naGVsbG8gd29ybGQh!!!!bm90\r\n" +
		"--b\r\nContent-Type: application/zip; name=\"weird.zip\"\r\nContent-Transfer-Encoding: x-unknown\r\n\r\nPK\r\n" +
		"--b\r\n\r\nno content type\r\n--b--\r\n"
}

func messageOf(t *testing.T, i *realInstance, cookie, mailbox, id string) contracts.Message {
	t.Helper()
	var m contracts.Message
	read(t, i.call(cookie, "GET", "/api/v1/mailboxes/"+mailbox+"/messages/"+id, ""), 200, &m)
	return m
}

// R3 against delivery: what the service's Deliver wrote is read back as it
// was listed. Every attachment delivery listed downloads, by the same index,
// with the size it listed; text and search read the same text.
func TestR3ReadingWhatDeliveryWrote(t *testing.T) {
	w := newRealWorld(t)
	a := w.instance()
	box := a.make("alice", "Work")
	base := "/api/v1/mailboxes/" + box.ID

	t.Run("parts", func(t *testing.T) {
		a.deliver(partsRaw(box.Address), box.Address)
		p := a.page("alice", box.ID, "")
		if len(p.Messages) != 1 || p.Messages[0].Subject != "Parts" {
			t.Fatalf("%+v", p)
		}
		id := p.Messages[0].ID
		m := messageOf(t, a, "alice", box.ID, id)
		if m.Text != "The plain text, r3-plain." || m.TextFrom != contracts.TextFromPlain || m.Unparsed {
			t.Fatalf("%+v", m)
		}
		var names []string
		for _, at := range m.Attachments {
			names = append(names, at.Name)
		}
		if !slices.Equal(names, []string{"notes.txt", "ticket.pdf", "", "broken.bin", "weird.zip"}) {
			t.Fatalf("listed %q", names)
		}
		want := map[int]string{0: "notes", 1: "%PDF-1.4\n", 4: "PK"}
		for _, at := range m.Attachments {
			rec := a.call("alice", "GET", fmt.Sprintf("%s/messages/%s/attachments/%d", base, id, at.Index), "")
			if rec.Code != 200 || int64(rec.Body.Len()) != at.Size {
				t.Fatalf("attachment %d (%s): %d, %d bytes, listed %d", at.Index, at.Name, rec.Code, rec.Body.Len(), at.Size)
			}
			if body, ok := want[at.Index]; ok && rec.Body.String() != body {
				t.Errorf("attachment %d: %q", at.Index, rec.Body)
			}
		}
		if rec := a.call("alice", "GET", base+"/messages/"+id+"/attachments/2", ""); !strings.Contains(rec.Body.String(), "inner body") {
			t.Errorf("the message inside: %q", rec.Body)
		}
		if rec := a.call("alice", "GET", fmt.Sprintf("%s/messages/%s/attachments/%d", base, id, len(m.Attachments)), ""); rec.Code != 404 {
			t.Fatalf("an index past the list: %d", rec.Code)
		}
		raw := a.call("alice", "GET", base+"/messages/"+id+"/raw", "")
		if raw.Code != 200 || raw.Body.String() != partsRaw(box.Address) {
			t.Fatalf("the raw message: %d %d bytes", raw.Code, raw.Body.Len())
		}
		if s := a.page("alice", box.ID, "?q=r3-plain"); len(s.Messages) != 1 || s.Messages[0].ID != id {
			t.Fatalf("search: %+v", s)
		}
		if s := a.page("alice", box.ID, "?q=Not+this+one"); len(s.Messages) != 0 {
			t.Fatal("search read the HTML alternative")
		}
	})

	t.Run("HTML only: the text and search read one text", func(t *testing.T) {
		raw := "From: b@example.org\r\nTo: " + box.Address + "\r\nSubject: Gate\r\nContent-Type: text/html\r\n\r\n" +
			"<html><head><title>no</title><style>p{}</style></head><body><p>Boarding at gate r3gate</p><script>hidden-r3</script>" +
			"<a href=\"https://example.org/x\">pass</a></body></html>\r\n"
		a.deliver(raw, box.Address)
		s := a.page("alice", box.ID, "?q=r3gate")
		if len(s.Messages) != 1 {
			t.Fatalf("%+v", s)
		}
		m := messageOf(t, a, "alice", box.ID, s.Messages[0].ID)
		text, from, _ := inbox.MessageText(context.Background(), []byte(raw), inbox.MessageTextMax)
		if m.Text != text || m.TextFrom != from || from != contracts.TextFromHTML || !strings.Contains(m.Text, "pass [https://example.org/x]") {
			t.Fatalf("%q %s, delivery's %q", m.Text, m.TextFrom, text)
		}
		for _, q := range []string{"hidden-r3", "no+r3gate", "example.org%2Fx"} {
			got := a.page("alice", box.ID, "?q="+q)
			if found := len(got.Messages) == 1; found != strings.Contains(m.Text, strings.ReplaceAll(strings.ReplaceAll(q, "+", " "), "%2F", "/")) {
				t.Errorf("search %q found %v, the text %q", q, found, m.Text)
			}
		}
	})

	t.Run("a partial parse: what was listed downloads, nothing past it", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("From: c@example.org\r\nTo: " + box.Address + "\r\nSubject: " + strings.Repeat("s", inbox.MaxSubjectBytes+10) +
			"\r\nContent-Type: multipart/mixed; boundary=p\r\n\r\n--p\r\nContent-Type: text/plain\r\n\r\nmany-r3\r\n")
		for n := range inbox.MaxAttachmentsListed + 2 {
			fmt.Fprintf(&b, "--p\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"f%d.bin\"\r\n\r\nbytes-%d\r\n", n, n)
		}
		b.WriteString("--p--\r\n")
		a.deliver(b.String(), box.Address)
		s := a.page("alice", box.ID, "?q=many-r3")
		if len(s.Messages) != 1 {
			t.Fatalf("%+v", s)
		}
		id := s.Messages[0].ID
		m := messageOf(t, a, "alice", box.ID, id)
		if len(m.Attachments) != inbox.MaxAttachmentsListed || len(m.Subject) != inbox.MaxSubjectBytes || m.Text != "many-r3" {
			t.Fatalf("%d attachments, subject of %d", len(m.Attachments), len(m.Subject))
		}
		last := inbox.MaxAttachmentsListed - 1
		rec := a.call("alice", "GET", fmt.Sprintf("%s/messages/%s/attachments/%d", base, id, last), "")
		if rec.Code != 200 || rec.Body.String() != fmt.Sprintf("bytes-%d", last) || !strings.Contains(rec.Header().Get("Content-Disposition"), fmt.Sprintf("f%d.bin", last)) {
			t.Fatalf("the last listed: %d %q", rec.Code, rec.Body)
		}
		for _, past := range []int{last + 1, last + 2} {
			if rec := a.call("alice", "GET", fmt.Sprintf("%s/messages/%s/attachments/%d", base, id, past), ""); rec.Code != 404 {
				t.Fatalf("attachment %d, not listed: %d", past, rec.Code)
			}
		}

		// Past the depth of nesting, after an attachment: the walk stops, and
		// what it listed before downloads.
		deep := "From: d@example.org\r\nTo: " + box.Address + "\r\nSubject: deep-r3\r\nContent-Type: multipart/mixed; boundary=x0\r\n\r\n" +
			"--x0\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"first.pdf\"\r\n\r\nfirst\r\n"
		for d := 1; d <= inbox.MaxMIMEDepth+1; d++ {
			deep += fmt.Sprintf("--x%d\r\nContent-Type: multipart/mixed; boundary=x%d\r\n\r\n", d-1, d)
		}
		deep += fmt.Sprintf("--x%d\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment\r\n\r\ntoo deep\r\n", inbox.MaxMIMEDepth+1)
		a.deliver(deep, box.Address)
		s = a.page("alice", box.ID, "?q=deep-r3")
		if len(s.Messages) != 1 {
			t.Fatalf("%+v", s)
		}
		m = messageOf(t, a, "alice", box.ID, s.Messages[0].ID)
		if len(m.Attachments) != 1 || m.Attachments[0].Name != "first.pdf" {
			t.Fatalf("%+v", m.Attachments)
		}
		if rec := a.call("alice", "GET", base+"/messages/"+m.ID+"/attachments/0", ""); rec.Code != 200 || rec.Body.String() != "first" {
			t.Fatalf("%d %q", rec.Code, rec.Body)
		}
	})

	t.Run("unparsed: only the raw message", func(t *testing.T) {
		raw := "this line is not a header\r\nnor this one\r\n\r\nunparsed-r3\r\n"
		before := len(a.page("alice", box.ID, "?limit=100").Messages)
		a.deliver(raw, box.Address)
		p := a.page("alice", box.ID, "?limit=100")
		if len(p.Messages) != before+1 {
			t.Fatalf("%d messages", len(p.Messages))
		}
		id := p.Messages[0].ID
		m := messageOf(t, a, "alice", box.ID, id)
		if !m.Unparsed || m.TextFrom != contracts.TextFromNone || m.Text != "" || len(m.Attachments) != 0 {
			t.Fatalf("%+v", m)
		}
		if rec := a.call("alice", "GET", base+"/messages/"+id+"/attachments/0", ""); rec.Code != 404 {
			t.Fatal(rec.Code)
		}
		if rec := a.call("alice", "GET", base+"/messages/"+id+"/raw", ""); rec.Code != 200 || rec.Body.String() != raw {
			t.Fatalf("%d %q", rec.Code, rec.Body)
		}
	})
}

// R3 against delivery: forwarding confirmations as delivery stores them, read
// through the mailbox and the inbox; one signed for another address keeps no
// code; deleting the message takes its confirmation away.
func TestR3ConfirmationsWrittenByDelivery(t *testing.T) {
	inbox.VerifyGmail = true // for this test only: the release keeps it off
	t.Cleanup(func() { inbox.VerifyGmail = false })
	w := newRealWorld(t)
	a := w.instance()
	box := a.make("alice", "Gmail")
	other := a.make("bob", "Someone else's")
	a.deliver(gmailConfirmation(t, other.Address, "thief@gmail.com"), box.Address)
	a.deliver(gmailConfirmation(t, box.Address, "ana@gmail.com"), box.Address)

	var got contracts.Mailbox
	read(t, a.call("alice", "GET", "/api/v1/mailboxes/"+box.ID, ""), 200, &got)
	if len(got.Confirmations) != 2 {
		t.Fatalf("%+v", got.Confirmations)
	}
	mine, theirs := got.Confirmations[0], got.Confirmations[1] // the newest first
	if mine.Code != "123456789" || !mine.Verified || mine.ForAnotherAddress || mine.Requester != "ana@gmail.com" {
		t.Fatalf("this mailbox's: %+v", mine)
	}
	if theirs.Code != "" || theirs.Verified || !theirs.ForAnotherAddress {
		t.Fatalf("another address's: %+v", theirs)
	}
	if got.Source == nil || got.Source.Address != "ana@gmail.com" {
		t.Fatalf("source %+v", got.Source)
	}
	p := a.page("alice", box.ID, "")
	if len(p.Messages) != 2 || !p.Messages[0].Confirmation || !p.Messages[1].Confirmation {
		t.Fatalf("%+v", p)
	}
	ids := map[string]bool{p.Messages[0].ID: true, p.Messages[1].ID: true}
	if !ids[theirs.Message] || !ids[mine.Message] {
		t.Fatalf("confirmations name %s and %s", mine.Message, theirs.Message)
	}
	if m := messageOf(t, a, "alice", box.ID, theirs.Message); !m.Confirmation {
		t.Fatalf("%+v", m)
	}
	want(t, a.call("alice", "DELETE", "/api/v1/mailboxes/"+box.ID+"/messages/"+theirs.Message, ""), 204, "")
	read(t, a.call("alice", "GET", "/api/v1/mailboxes/"+box.ID, ""), 200, &got)
	if len(got.Confirmations) != 1 || got.Confirmations[0].Message != mine.Message {
		t.Fatalf("after deleting its message: %+v", got.Confirmations)
	}
}

// Two instances over one store: a message delivered through one is listed,
// searched, opened, downloaded and deleted through the other, and the first
// then finds it gone.
func TestTwoInstancesReadOneInbox(t *testing.T) {
	w := newRealWorld(t)
	a, b := w.instance(), w.instance()
	box := a.make("alice", "Shared")
	a.deliver(partsRaw(box.Address), box.Address)
	base := "/api/v1/mailboxes/" + box.ID
	p := b.page("alice", box.ID, "")
	if len(p.Messages) != 1 {
		t.Fatalf("%+v", p)
	}
	id := p.Messages[0].ID
	if s := b.page("alice", box.ID, "?q=r3-plain"); len(s.Messages) != 1 || s.Messages[0].ID != id {
		t.Fatalf("%+v", s)
	}
	if m := messageOf(t, b, "alice", box.ID, id); m.Text != "The plain text, r3-plain." || len(m.Attachments) != 5 {
		t.Fatalf("%+v", m)
	}
	if rec := b.call("alice", "GET", base+"/messages/"+id+"/attachments/1", ""); rec.Code != 200 || rec.Body.String() != "%PDF-1.4\n" {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
	if rec := b.call("alice", "GET", base+"/messages/"+id+"/raw", ""); rec.Code != 200 || rec.Body.String() != partsRaw(box.Address) {
		t.Fatal(rec.Code)
	}
	a.deliver("From: e@example.org\r\nSubject: second\r\n\r\nsecond-r3\r\n", box.Address)
	var deleted contracts.MessagesDeleted
	second := a.page("alice", box.ID, "").Messages[0].ID
	want(t, b.call("alice", "DELETE", base+"/messages/"+id, ""), 204, "")
	read(t, b.call("alice", "POST", base+"/messages/delete", `{"ids":["`+second+`","`+id+`"]}`), 200, &deleted)
	if deleted.Deleted != 1 {
		t.Fatalf("%+v", deleted)
	}
	for _, m := range []string{id, second} {
		want(t, a.call("alice", "GET", base+"/messages/"+m, ""), 404, "not_found")
	}
	if p := a.page("alice", box.ID, ""); len(p.Messages) != 0 {
		t.Fatalf("%+v", p)
	}
	if s := a.page("alice", box.ID, "?q=r3-plain"); len(s.Messages) != 0 {
		t.Fatalf("%+v", s)
	}
}

// The frame's guarantees on the real routes (ADR 0026, "What pull requests 1
// to 4 keep true for the frame"), with hostile mail written by delivery:
// every answer, answered and refused, to the owner, a stranger and no session.
// Another account's mailbox or message answers exactly as one that does not
// exist, before anything of its mail is read.
func TestTheFrameOnTheRealRoutes(t *testing.T) {
	w := newRealWorld(t)
	a := w.instance()
	box := a.make("alice", "Mine")
	theirs := a.make("bob", "Bob's")
	a.deliver(hostileRaw(), box.Address)
	a.deliver(plainRaw, theirs.Address)
	msg := a.page("alice", box.ID, "").Messages[0].ID
	bobs := a.page("bob", theirs.ID, "").Messages[0].ID
	if m := messageOf(t, a, "alice", box.ID, msg); m.Subject != hostileSubject || len(m.Attachments) != 2 {
		t.Fatalf("%+v", m)
	}

	touched := false
	w.mem.Interrupt = func(_ context.Context, _ store.Step, partition, _ string) error {
		if partition == "mailbox#"+box.ID || partition == box.ID {
			touched = true
		}
		return nil
	}
	never := "mbx_" + strings.Repeat("2", len(box.ID)-len("mbx_"))
	neverMsg := strings.Repeat("0", 31) + "1"
	a.opened.opened, a.reader.read = nil, nil
	for _, r := range routes(box.ID, msg) {
		if r.path == "/api/v1/mailboxes" {
			continue
		}
		stranger := a.call("bob", r.method, r.path, r.body)
		none := a.call("bob", r.method, strings.Replace(r.path, box.ID, never, 1), r.body)
		frameCheck(t, "stranger: "+r.name, stranger)
		want(t, stranger, 404, "not_found")
		if stranger.Body.String() != none.Body.String() || stranger.Code != none.Code || !sameHeaders(stranger.Header(), none.Header()) {
			t.Errorf("%s: %d %q %v and %d %q %v", r.name, stranger.Code, stranger.Body, stranger.Header(), none.Code, none.Body, none.Header())
		}
		// Alice's message through Bob's own mailbox is a message that is not there.
		if strings.Contains(r.path, "/messages/"+msg) {
			through := a.call("bob", r.method, strings.Replace(r.path, box.ID, theirs.ID, 1), r.body)
			absent := a.call("bob", r.method, strings.Replace(strings.Replace(r.path, box.ID, theirs.ID, 1), msg, neverMsg, 1), r.body)
			frameCheck(t, "through Bob's: "+r.name, through)
			if through.Code != 404 || through.Body.String() != absent.Body.String() || !sameHeaders(through.Header(), absent.Header()) {
				t.Errorf("through Bob's mailbox, %s: %d %q and %d %q", r.name, through.Code, through.Body, absent.Code, absent.Body)
			}
		}
		frameCheck(t, "no session: "+r.name, a.call("", r.method, r.path, r.body))
	}
	if touched || slices.Contains(a.opened.opened, box.ID) || slices.Contains(a.reader.read, box.ID) {
		t.Fatalf("Alice's mail was read for Bob: store %v, opened %v, read %v", touched, a.opened.opened, a.reader.read)
	}
	w.mem.Interrupt = nil
	if m := messageOf(t, a, "bob", theirs.ID, bobs); m.Subject != "hello" {
		t.Fatalf("Bob's own: %+v", m)
	}

	// The owner, on every route and a few that are not routes.
	for _, r := range routes(box.ID, msg) {
		frameCheck(t, r.name, a.call("alice", r.method, r.path, r.body))
	}
	box = a.make("alice", "Again") // the routes above deleted the message and disconnected the mailbox
	a.deliver(hostileRaw(), box.Address)
	msg = a.page("alice", box.ID, "").Messages[0].ID
	for _, extra := range []string{"", "/raw", "/attachments/0", "/attachments/1", "/attachments/2", "/html", "/links/0", "/nothing"} {
		frameCheck(t, extra, a.call("alice", "GET", "/api/v1/mailboxes/"+box.ID+"/messages/"+msg+extra, ""))
	}
	rec := a.call("alice", "GET", "/api/v1/mailboxes/"+box.ID+"/messages/"+msg+"/attachments/0", "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/octet-stream" || strings.ContainsAny(rec.Header().Get("Content-Disposition"), "\r\n/") {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	var m contracts.Message
	read(t, a.call("alice", "GET", "/api/v1/mailboxes/"+box.ID+"/messages/"+msg, ""), 200, &m)
	if m.Subject != hostileSubject || m.TextFrom != contracts.TextFromHTML || strings.Contains(m.Text, "alert(\"") || m.HTML != nil {
		t.Fatalf("%+v", m)
	}
	// No GET changed anything: the message is still there, and the mailbox
	// as it was.
	if p := a.page("alice", box.ID, ""); len(p.Messages) != 1 {
		t.Fatalf("%+v", p)
	}

	out := w.logs.String()
	for _, secret := range []string{"7f3a", "attacker", "<script", alice, bob, box.ID, theirs.ID, msg, bobs, box.Address, theirs.Address, "Mine", "Bob's"} {
		if strings.Contains(out, secret) {
			t.Fatalf("the log holds %q:\n%s", secret, out)
		}
	}
}

// sameHeaders compares two answers' headers.
func sameHeaders(a, b http.Header) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}
	return true
}

// A disconnect through the real routes: refused without a fresh proof,
// answered 204 again until the sweep's last pass, 404 after it, to the owner
// as to everyone; the address refuses mail from the disconnect on.
func TestDisconnectOnTheRealRoutes(t *testing.T) {
	w := newRealWorld(t)
	a, b := w.instance(), w.instance()
	box := a.make("alice", "Leaving")
	a.deliver(plainRaw, box.Address)
	stale := httpapi.MailboxesAPI(httpapi.MailboxesConfig{Service: a.svc, Reader: inbox.NewReader(w.mem), Now: w.clock,
		Sessions: sessions{"alice": {AccountID: alice, Session: contracts.Session{ID: "ses_s"}}}})
	want(t, serve(stale, "alice", "DELETE", "/api/v1/mailboxes/"+box.ID, ""), 403, "confirmation_needed")
	want(t, a.call("alice", "DELETE", "/api/v1/mailboxes/"+box.ID, ""), 204, "")
	if _, err := b.svc.Accept(context.Background(), box.Address); !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
		t.Fatalf("the address after a disconnect: %v", err)
	}
	want(t, b.call("alice", "DELETE", "/api/v1/mailboxes/"+box.ID, ""), 204, "")
	want(t, b.call("alice", "GET", "/api/v1/mailboxes/"+box.ID+"/messages", ""), 404, "not_found")
	want(t, b.call("bob", "DELETE", "/api/v1/mailboxes/"+box.ID, ""), 404, "not_found")
	var list contracts.MailboxList
	read(t, b.call("alice", "GET", "/api/v1/mailboxes", ""), 200, &list)
	if len(list.Mailboxes) != 0 {
		t.Fatalf("%+v", list)
	}
	// The sweep's last pass, SweepGrace after the disconnect.
	w.add(mailboxes.SweepGrace + time.Minute)
	for range 5 {
		if err := a.svc.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	want(t, a.call("alice", "DELETE", "/api/v1/mailboxes/"+box.ID, ""), 404, "not_found")
	want(t, b.call("alice", "DELETE", "/api/v1/mailboxes/"+box.ID, ""), 404, "not_found")
	if msgs, _ := w.mem.ListMessages(context.Background(), box.ID, 10, ""); len(msgs) != 0 {
		t.Fatalf("%d messages left", len(msgs))
	}
}
