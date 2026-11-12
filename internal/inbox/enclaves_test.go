package inbox

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/store"
)

// together is the storage as two enclaves that start at once see it: each
// reads that the mailbox is not there before either writes it.
type together struct {
	*store.Memory
	mu      sync.Mutex
	waiting int
	both    chan struct{}
}

func (s *together) GetMailbox(ctx context.Context, id string) (*store.Mailbox, error) {
	mb, err := s.Memory.GetMailbox(ctx, id)
	s.mu.Lock()
	s.waiting++
	if s.waiting == 2 {
		close(s.both)
	}
	s.mu.Unlock()
	<-s.both
	return mb, err
}

func TestTheMailboxMadeByTwoEnclavesAtOnceIsOne(t *testing.T) {
	ctx := context.Background()
	k, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	mem := store.NewMemory()
	shared := &together{Memory: mem, both: make(chan struct{})}
	start := func() *Service {
		return New(Config{MailboxID: "test", Address: address}, shared, k, nil, slog.New(slog.DiscardHandler))
	}
	a, b := start(), start()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Go(func() { errs[0] = a.Ensure(ctx) })
	wg.Go(func() { errs[1] = b.Ensure(ctx) })
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("ensure: %v, %v", errs[0], errs[1])
	}
	// Mail that each received opens in the other, and after a restart.
	idA, err := a.Ingest(ctx, "x@example.org", []string{address}, []byte(plainMessage))
	if err != nil {
		t.Fatal(err)
	}
	idB, err := b.Ingest(ctx, "x@example.org", []string{address}, []byte(plainMessage))
	if err != nil {
		t.Fatal(err)
	}
	later := New(Config{MailboxID: "test", Address: address}, mem, k, nil, slog.New(slog.DiscardHandler))
	for _, s := range []*Service{a, b, later} {
		for _, id := range []string{idA, idB} {
			if _, err := s.Get(ctx, id); err != nil {
				t.Fatalf("a message the other enclave stored does not open: %v", err)
			}
		}
	}
}

// interleave runs other just before the first write of the mailbox: the
// moment between a read and a write of one enclave, when another writes.
type interleave struct {
	*store.Memory
	other func()
}

func (s *interleave) PutMailbox(ctx context.Context, m *store.Mailbox) error {
	if other := s.other; other != nil {
		s.other = nil
		other()
	}
	return s.Memory.PutMailbox(ctx, m)
}

func TestTwoEnclavesChangingTheMailboxDoNotUndoEachOther(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	shared := &interleave{Memory: f.store}
	start := func() *Service {
		s := New(Config{MailboxID: "test", Address: address}, shared, f.keys, nil, slog.New(slog.DiscardHandler))
		s.SetClock(func() time.Time { return f.now })
		return s
	}
	a, b := start(), start()

	// A records a forwarding confirmation while B changes the retention.
	shared.other = func() {
		if err := b.SetRetention(ctx, 7); err != nil {
			t.Error(err)
		}
	}
	if _, err := a.Ingest(ctx, "forwarding-noreply@google.com", []string{address}, []byte(gmailConfirmation)); err != nil {
		t.Fatal(err)
	}
	view, err := b.Mailbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.RetentionDays != 7 || len(view.ForwardingConfirmations) != 1 {
		t.Fatalf("one change undid the other: retention %d, %d confirmations", view.RetentionDays, len(view.ForwardingConfirmations))
	}

	// And the other way round.
	shared.other = func() {
		if _, err := b.Ingest(ctx, "forwarding-noreply@google.com", []string{address}, []byte(gmailConfirmation)); err != nil {
			t.Error(err)
		}
	}
	if err := a.SetRetention(ctx, 30); err != nil {
		t.Fatal(err)
	}
	view, _ = a.Mailbox(ctx)
	if view.RetentionDays != 30 || len(view.ForwardingConfirmations) != 2 {
		t.Fatalf("one change undid the other: retention %d, %d confirmations", view.RetentionDays, len(view.ForwardingConfirmations))
	}
}

// busy is a mailbox that another enclave writes before every write of this one.
type busy struct{ *store.Memory }

func (s busy) PutMailbox(ctx context.Context, m *store.Mailbox) error {
	if m.Version == 0 {
		return s.Memory.PutMailbox(ctx, m)
	}
	return store.ErrConflict
}

func TestAMailboxThatKeepsChangingIsAnError(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	s := New(Config{MailboxID: "test", Address: address}, busy{f.store}, f.keys, nil, slog.New(slog.DiscardHandler))
	if err := s.SetRetention(ctx, 7); err == nil {
		t.Fatal("no error when every write is refused")
	}
	// A store that keeps saying the mailbox exists, and then that it does not.
	ghost := New(Config{MailboxID: "ghost", Address: address}, ghostStore{store.NewMemory()}, f.keys, nil, slog.New(slog.DiscardHandler))
	if err := ghost.Ensure(ctx); err == nil {
		t.Fatal("no error when the mailbox can be neither made nor read")
	}
	down := errors.New("dynamodb down")
	failing := New(Config{MailboxID: "test", Address: address}, failingPut{store.NewMemory(), down}, f.keys, nil, slog.New(slog.DiscardHandler))
	if err := failing.Ensure(ctx); !errors.Is(err, down) {
		t.Fatalf("a failed write: %v", err)
	}
	failing = New(Config{MailboxID: "test", Address: address}, failingPut{f.store, down}, f.keys, nil, slog.New(slog.DiscardHandler))
	if err := failing.SetRetention(ctx, 7); !errors.Is(err, down) {
		t.Fatalf("a failed write: %v", err)
	}
}

type ghostStore struct{ *store.Memory }

func (ghostStore) PutMailbox(context.Context, *store.Mailbox) error { return store.ErrExists }

type failingPut struct {
	*store.Memory
	err error
}

func (f failingPut) PutMailbox(context.Context, *store.Mailbox) error { return f.err }
