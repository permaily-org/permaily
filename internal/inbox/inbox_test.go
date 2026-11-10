package inbox

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/store"
)

const address = "u-test@inbox.permaily.test"

const plainMessage = "From: Alice <alice@example.org>\r\n" +
	"To: someone@gmail.com\r\n" +
	"Subject: Your flight to Lisbon\r\n" +
	"Date: Mon, 09 Nov 2026 10:00:00 +0000\r\n" +
	"Message-ID: <abc@example.org>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=b\r\n" +
	"\r\n" +
	"--b\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nBooking ref ABC123. Departure 08:15.\r\n" +
	"--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=ticket.pdf\r\n\r\n%PDF-1.7 fake\r\n" +
	"--b--\r\n"

const gmailConfirmation = "From: Gmail Team <forwarding-noreply@google.com>\r\n" +
	"To: " + address + "\r\n" +
	"Subject: (#123456789) Gmail Forwarding Confirmation - Receive Mail from someone@gmail.com\r\n" +
	"\r\n" +
	"someone@gmail.com has requested to automatically forward mail to your email address.\r\nConfirmation code: 123456789\r\n"

type fixture struct {
	svc   *Service
	store *store.Memory
	keys  keys.Service
	now   time.Time
	auth  []AuthResult
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	k, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{store: store.NewMemory(), keys: k, now: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	f.store.Now = func() time.Time { return f.now }
	f.svc = f.newService()
	if err := f.svc.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) newService() *Service {
	auth := func(context.Context, []byte) []AuthResult { return f.auth }
	s := New(Config{MailboxID: "test", Address: address}, f.store, f.keys, auth, slog.New(slog.DiscardHandler))
	s.SetClock(func() time.Time { return f.now })
	return s
}

func TestIngestAndRead(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id, err := f.svc.Ingest(ctx, "bounce@example.org", []string{"<U-TEST@inbox.permaily.test>"}, []byte(plainMessage))
	if err != nil {
		t.Fatal(err)
	}

	list, err := f.svc.List(ctx, 10, "")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	s := list[0]
	if s.ID != id || s.Subject != "Your flight to Lisbon" || s.From[0] != "alice@example.org" || s.Attachments != 1 ||
		!strings.HasPrefix(s.Snippet, "Booking ref ABC123") || !s.ExpiresAt.Equal(f.now.AddDate(0, 0, 90)) {
		t.Fatalf("summary: %+v", s)
	}

	m, err := f.svc.Get(ctx, id)
	if err != nil || m.Meta.MessageID != "abc@example.org" || m.Meta.Attachments[0].Filename != "ticket.pdf" || m.Meta.EnvelopeFrom != "bounce@example.org" {
		t.Fatalf("get: %+v %v", m, err)
	}
	raw, err := f.svc.Raw(ctx, id)
	if err != nil || string(raw) != plainMessage {
		t.Fatalf("raw differs: %v", err)
	}

	if err := f.svc.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted message: %v", err)
	}
}

func TestStoreNeverSeesPlaintext(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id, err := f.svc.Ingest(ctx, "a@example.org", []string{address}, []byte(plainMessage))
	if err != nil {
		t.Fatal(err)
	}
	rec, raw, err := f.store.GetMessage(ctx, "test", id)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"Lisbon", "ABC123", "alice@example.org", "ticket.pdf"} {
		if bytes.Contains(rec.Meta, []byte(secret)) || bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("store holds plaintext %q", secret)
		}
	}
}

func TestUnknownRecipient(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Ingest(context.Background(), "a@example.org", []string{"other@inbox.permaily.test"}, []byte(plainMessage)); !errors.Is(err, ErrUnknownRecipient) {
		t.Fatalf("err = %v", err)
	}
	if f.svc.Accepts("u-test@other.example") {
		t.Fatal("accepted another domain")
	}
}

func TestForwardingConfirmation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Unsigned: recorded but not verified (anyone can send this mail).
	if _, err := f.svc.Ingest(ctx, "forwarding-noreply@google.com", []string{address}, []byte(gmailConfirmation)); err != nil {
		t.Fatal(err)
	}
	// Signed by google.com.
	f.auth = []AuthResult{{Method: "dkim", Domain: "google.com", Result: "pass"}}
	f.now = f.now.Add(time.Minute)
	if _, err := f.svc.Ingest(ctx, "forwarding-noreply@google.com", []string{address}, []byte(gmailConfirmation)); err != nil {
		t.Fatal(err)
	}
	view, err := f.svc.Mailbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := view.ForwardingConfirmations
	if len(c) != 2 || c[0].Code != "123456789" || c[0].Requester != "someone@gmail.com" || c[0].Verified || !c[1].Verified {
		t.Fatalf("confirmations: %+v", c)
	}

	// A normal message mentioning a code is not a confirmation.
	f.auth = nil
	if _, err := f.svc.Ingest(ctx, "x@example.org", []string{address}, []byte(strings.Replace(gmailConfirmation, "forwarding-noreply@google.com", "x@example.org", 1))); err != nil {
		t.Fatal(err)
	}
	view, _ = f.svc.Mailbox(ctx)
	if len(view.ForwardingConfirmations) != 2 {
		t.Fatalf("spoof from another sender recorded: %+v", view.ForwardingConfirmations)
	}
}

func TestRetention(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.svc.SetRetention(ctx, 45); !errors.Is(err, ErrInvalidRetention) {
		t.Fatalf("invalid retention: %v", err)
	}
	if err := f.svc.SetRetention(ctx, 7); err != nil {
		t.Fatal(err)
	}
	id, err := f.svc.Ingest(ctx, "a@example.org", []string{address}, []byte(plainMessage))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := f.svc.Get(ctx, id)
	if !m.ExpiresAt.Equal(f.now.AddDate(0, 0, 7)) {
		t.Fatalf("expires at %v", m.ExpiresAt)
	}
	f.now = f.now.AddDate(0, 0, 8)
	if _, err := f.svc.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired message readable: %v", err)
	}
}

func TestRestartKeepsAccess(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id, err := f.svc.Ingest(ctx, "a@example.org", []string{address}, []byte(plainMessage))
	if err != nil {
		t.Fatal(err)
	}
	// A new enclave (same KMS key, same store) unwraps the stored data key.
	restarted := f.newService()
	if err := restarted.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if m, err := restarted.Get(ctx, id); err != nil || m.Subject != "Your flight to Lisbon" {
		t.Fatalf("after restart: %+v %v", m, err)
	}
}

// migrating is a key service that wraps keys a new way and still opens the
// ones the old service wrapped, like keys.Root with a legacy service.
type migrating struct {
	old  keys.Service
	fail error
}

func (m migrating) NewDataKey(context.Context, map[string]string) (keys.DataKey, error) {
	dk := bytes.Repeat([]byte{9}, 32)
	return keys.DataKey{Plaintext: dk, Wrapped: append([]byte("NEW:"), dk...)}, nil
}

func (m migrating) Unwrap(ctx context.Context, wrapped []byte, c map[string]string) ([]byte, error) {
	if dk, ok := bytes.CutPrefix(wrapped, []byte("NEW:")); ok {
		return dk, nil
	}
	return m.old.Unwrap(ctx, wrapped, c)
}

func (m migrating) Rewrap(ctx context.Context, wrapped []byte, c map[string]string) ([]byte, bool, error) {
	if m.fail != nil {
		return nil, false, m.fail
	}
	if bytes.HasPrefix(wrapped, []byte("NEW:")) {
		return wrapped, false, nil
	}
	dk, err := m.old.Unwrap(ctx, wrapped, c)
	return append([]byte("NEW:"), dk...), err == nil, err
}

// The release that introduces the root secret finds a mailbox whose key an
// older image wrapped with KMS, and moves it without losing mail.
func TestMailboxKeyMovesUnderTheRootSecret(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id, err := f.svc.Ingest(ctx, "sender@example.org", []string{address}, []byte("Subject: before\r\n\r\nold mail\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.store.GetMailbox(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}

	// A failure to move must not lose the old wrapping.
	old := f.keys
	f.keys = migrating{old: old, fail: errors.New("root secret unavailable")}
	if _, err := f.newService().Get(ctx, id); err == nil {
		t.Fatal("served mail without moving the key")
	}
	if mb, _ := f.store.GetMailbox(ctx, "test"); !bytes.Equal(mb.WrappedKey, before.WrappedKey) {
		t.Fatal("the record changed after a failed move")
	}

	f.keys = migrating{old: old}
	svc := f.newService()
	if _, err := svc.Get(ctx, id); err != nil {
		t.Fatalf("mail stored before the move: %v", err)
	}
	after, err := f.store.GetMailbox(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(after.WrappedKey, []byte("NEW:")) || !bytes.Equal(after.State, before.State) || after.RetentionDays != before.RetentionDays {
		t.Fatalf("mailbox after the move: %q", after.WrappedKey)
	}
	// From here on the old key service is not needed.
	f.keys = migrating{old: nil}
	if _, err := f.newService().Get(ctx, id); err != nil {
		t.Fatalf("after the move, without the old service: %v", err)
	}
}

func TestUnparseableMessageIsKept(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id, err := f.svc.Ingest(ctx, "a@example.org", []string{address}, []byte("\x00\x01 not a message"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.svc.Raw(ctx, id)
	if err != nil || string(raw) != "\x00\x01 not a message" {
		t.Fatalf("raw: %q %v", raw, err)
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(plainMessage))
	f.Add([]byte(gmailConfirmation))
	f.Fuzz(func(_ *testing.T, raw []byte) {
		m, text := parse(raw)
		_, _, _ = forwardingConfirmation(m, text)
	})
}
