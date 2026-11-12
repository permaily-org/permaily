package mailboxes_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/services/mailboxes/mailboxestest"
	"github.com/permaily-org/permaily/internal/store"
)

// Two enclaves of one deployment run at once (ADR 0026, "Interleavings the
// tests cover"): services, each with its own memory and its own Anchored,
// over one store and one store of commits.

const domain = "inbox.permaily.test"

var start = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

type world struct {
	t        *testing.T
	mem      *store.Memory
	commits  *store.MemoryCommits
	keys     *keys.Local
	clock    *clock
	accounts *mailboxestest.Accounts
	notices  *mailboxestest.Notices
	logs     *lockedBuffer
	auth     inbox.Authenticator
	// interrupt, when set, is the store's Interrupt (store.Memory has one for
	// both enclaves).
	mu        sync.Mutex
	interrupt func(ctx context.Context, step store.Step, partition, key string) error
}

func newWorld(t *testing.T) *world {
	t.Helper()
	k, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	w := &world{
		t: t, mem: store.NewMemory(), commits: store.NewMemoryCommits(), keys: k, clock: &clock{t: start},
		accounts: mailboxestest.NewAccounts(), notices: &mailboxestest.Notices{}, logs: &lockedBuffer{},
	}
	w.mem.Now = w.clock.now
	w.mem.Interrupt = func(ctx context.Context, step store.Step, partition, key string) error {
		w.mu.Lock()
		f := w.interrupt
		w.mu.Unlock()
		if f == nil {
			return nil
		}
		return f(ctx, step, partition, key)
	}
	return w
}

func (w *world) setInterrupt(f func(ctx context.Context, step store.Step, partition, key string) error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.interrupt = f
}

// hooked is the records as one enclave has them, where a test says what
// happens before that enclave writes one.
type hooked struct {
	*store.Anchored
	mu     sync.Mutex
	before func(op, name string) error
}

func (h *hooked) hook(op, name string) error {
	h.mu.Lock()
	f := h.before
	h.mu.Unlock()
	if f == nil {
		return nil
	}
	return f(op, name)
}

func (h *hooked) set(f func(op, name string) error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.before = f
}

func (h *hooked) PutRecordAt(ctx context.Context, r *store.Record, at store.Mark) error {
	if err := h.hook("put", r.Name); err != nil {
		return err
	}
	return h.Anchored.PutRecordAt(ctx, r, at)
}

func (h *hooked) CreateNew(ctx context.Context, r *store.Record) error {
	if err := h.hook("create", r.Name); err != nil {
		return err
	}
	return h.Anchored.CreateNew(ctx, r)
}

func (h *hooked) RemoveRecord(ctx context.Context, name string) error {
	if err := h.hook("remove", name); err != nil {
		return err
	}
	return h.Anchored.RemoveRecord(ctx, name)
}

type enclave struct {
	*mailboxes.Service
	records *hooked
}

func (w *world) enclave() *enclave {
	w.t.Helper()
	records := &hooked{Anchored: &store.Anchored{Records: w.mem, Commits: w.commits, OnlyNewest: true, Strict: true, Now: w.clock.now}}
	s, err := mailboxes.New(mailboxes.Config{
		Records: records, Storage: w.mem, Keys: w.keys, Accounts: w.accounts, Notifier: w.notices,
		Auth: w.auth, Domain: domain, Now: w.clock.now,
		Log: slog.New(slog.NewJSONHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return &enclave{Service: s, records: records}
}

// lose deletes every stored value of a mailbox's record, as whoever runs the
// table can: the record is lost (store.ErrLost).
func (w *world) lose(id string) {
	for _, n := range w.mem.RecordNames() {
		if strings.HasPrefix(n, "mailbox-"+id+"@") {
			_ = w.mem.DeleteRecord(context.Background(), n)
		}
	}
}

// fork adds a second commit at the newest version of a mailbox's record, as
// a delete marker lets whoever writes to the bucket do (store.ErrForked).
func (w *world) fork(id string) {
	h, err := w.commits.History(context.Background(), "mailbox-"+id)
	if err != nil || h.Head == nil {
		w.t.Fatalf("no history: %v", err)
	}
	var d [sha256.Size]byte
	_, _ = rand.Read(d[:])
	w.commits.Fork("mailbox-"+id, h.Head.Version, d)
}

// history is the commits of a record.
func (w *world) history(name string) *store.History {
	h, err := w.commits.History(context.Background(), name)
	if err != nil {
		w.t.Fatal(err)
	}
	return h
}

var ctx = context.Background()

func caller(account string, c *clock) mailboxes.Caller {
	return mailboxes.Caller{Account: account, ConfirmedUntil: c.now().Add(time.Hour)}
}

func (e *enclave) mustCreate(t *testing.T, c mailboxes.Caller) *contracts.Mailbox {
	t.Helper()
	m, err := e.Create(ctx, c, contracts.MailboxCreate{Label: "Work"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return m
}

func message(subject string) []byte {
	return []byte("From: Alice <alice@example.org>\r\nTo: someone@gmail.com\r\nSubject: " + subject + "\r\n" +
		"Content-Type: text/plain\r\n\r\nHello, this is " + subject + ".\r\n")
}

// deliver is what the SMTP server does with one message.
func (e *enclave) deliver(t *testing.T, raw []byte, addresses ...string) error {
	t.Helper()
	var ids []string
	for _, a := range addresses {
		id, err := e.Accept(ctx, a)
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	return e.Deliver(ctx, "alice@example.org", ids, raw)
}

func (w *world) messages(id string) []store.Message {
	w.t.Helper()
	msgs, err := w.mem.ListMessages(ctx, id, 100, "")
	if err != nil {
		w.t.Fatal(err)
	}
	return msgs
}

func (w *world) kinds() []mailboxes.NoticeKind {
	var out []mailboxes.NoticeKind
	for _, n := range w.notices.Sent() {
		out = append(out, n.Kind)
	}
	return out
}

func count(kinds []mailboxes.NoticeKind, k mailboxes.NoticeKind) int {
	n := 0
	for _, x := range kinds {
		if x == k {
			n++
		}
	}
	return n
}

// onlyOnce runs f the first time only, and never waits: f may run what calls
// it again (sync.Once would wait for itself).
type onlyOnce struct{ done atomic.Bool }

func (o *onlyOnce) Do(f func()) {
	if o.done.CompareAndSwap(false, true) {
		f()
	}
}

var errStopped = errors.New("stopped by the test")

// The contract every implementation keeps (C1 to C18), on the service over
// store.Memory with its own strict Anchored.
func TestTheServiceKeepsTheContract(t *testing.T) {
	mailboxestest.Contract(t, func(t *testing.T) *mailboxestest.Subject {
		w := newWorld(t)
		e := w.enclave()
		return &mailboxestest.Subject{
			Service: e, Accounts: w.accounts, Notices: w.notices, SetNow: w.clock.set,
			Sweep: func(t *testing.T) {
				t.Helper()
				for range 2 {
					if err := e.Sweep(ctx); err != nil {
						t.Fatal(err)
					}
				}
			},
			MakeLost: func(_ *testing.T, id string) { w.lose(id) }, MakeForked: func(_ *testing.T, id string) { w.fork(id) },
		}
	})
}
