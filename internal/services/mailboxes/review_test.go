package mailboxes_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/store"
)

// The findings of PR #91's code review on the mailboxes service: each test is
// the sequence the reviewer described.

// loseRecord deletes every stored value of a strict record, as whoever runs
// the table can: the record is lost (store.ErrLost).
func (w *world) loseRecord(name string) {
	for _, n := range w.mem.RecordNames() {
		if strings.HasPrefix(n, name+"@") {
			_ = w.mem.DeleteRecord(context.Background(), n)
		}
	}
}

// Review 1: A removes the account and its list is closed, but the write of
// the mailbox's record fails: the mailbox is still active. B, the other
// enclave, must refuse its mail at RCPT TO (550) and before storing (451),
// because the account's list no longer names it as open. A lost list does
// the same, and shows on /status as a lost strict record.
func TestReview1AClosedOrLostListStopsDelivery(t *testing.T) {
	t.Run("closed", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.enclave(), w.enclave()
		me := caller("acc_me", w.clock)
		m := a.mustCreate(t, me)
		id, err := b.Accept(ctx, m.Address) // RCPT TO before the removal
		if err != nil {
			t.Fatal(err)
		}
		a.records.set(func(op, name string) error {
			if op == "put" && isMailbox(name) {
				return errStopped
			}
			return nil
		})
		if err := a.RemoveAccount(ctx, me.Account); err == nil {
			t.Fatal("the removal did not fail")
		}
		a.records.set(nil)
		if _, err := b.Accept(ctx, m.Address); !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
			t.Fatalf("RCPT TO after the list closed: %v", err)
		}
		if err := b.Deliver(ctx, "alice@example.org", []string{id}, message("late")); !errors.Is(err, mailboxes.ErrNotAllStored) {
			t.Fatalf("DATA after the list closed: %v", err)
		}
		if n := len(w.messages(m.ID)); n != 0 {
			t.Fatalf("%d messages stored", n)
		}
	})
	t.Run("lost", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.enclave(), w.enclave()
		me := caller("acc_me", w.clock)
		m := a.mustCreate(t, me)
		id, err := b.Accept(ctx, m.Address)
		if err != nil {
			t.Fatal(err)
		}
		w.loseRecord("mailboxes-" + me.Account)
		if _, err := b.Accept(ctx, m.Address); !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
			t.Fatalf("RCPT TO with the list lost: %v", err)
		}
		if err := b.Deliver(ctx, "alice@example.org", []string{id}, message("lost")); !errors.Is(err, mailboxes.ErrNotAllStored) {
			t.Fatalf("DATA with the list lost: %v", err)
		}
		if n := len(w.messages(m.ID)); n != 0 {
			t.Fatalf("%d messages stored", n)
		}
		lost := false
		for _, r := range b.records.Regressions() {
			lost = lost || (r.Lost && r.Name == "mailboxes-"+me.Account)
		}
		if !lost {
			t.Fatalf("a lost list is not reported: %+v", b.records.Regressions())
		}
	})
	t.Run("missing or forked", func(t *testing.T) {
		w := newWorld(t)
		a := w.enclave()
		me := caller("acc_me", w.clock)
		m := a.mustCreate(t, me)
		h := w.history("mailboxes-" + me.Account)
		w.commits.Fork("mailboxes-"+me.Account, h.Head.Version, [32]byte{1})
		if _, err := a.Accept(ctx, m.Address); !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
			t.Fatalf("RCPT TO with the list forked: %v", err)
		}
	})
	t.Run("blocked or closing still receive", func(t *testing.T) {
		w := newWorld(t)
		a := w.enclave()
		me, other := caller("acc_me", w.clock), caller("acc_other", w.clock)
		m, n := a.mustCreate(t, me), a.mustCreate(t, other)
		w.accounts.Block(me.Account)
		w.accounts.Set(other.Account, mailboxes.Standing{Closing: true, TermsAgreed: true})
		if err := a.deliver(t, message("still"), m.Address, n.Address); err != nil {
			t.Fatal(err)
		}
	})
}

// Review 4: the sweep ran out of its pages and said it was done: after the
// grace the record, the index entry and the work entry went with messages
// left. On AWS a listing of entries without a prefix also reads the
// partition's message rows (store.AWS.ListEntries; the store's emulator test
// TestReview4ListingEntriesWithoutAPrefixReadsMessages shows it), which the
// generic entry path then deleted without their raw objects. awsLike makes
// the memory store list them the same way.
type awsLike struct {
	*store.Memory
	mu      sync.Mutex
	deleted []string // keys deleted through the generic entry path
	// bare counts the listings of a mailbox's partition without a prefix:
	// on AWS each would read its message rows.
	bare int
}

func (s *awsLike) ListEntries(ctx context.Context, partition, prefix, after string, limit int) ([]store.Entry, error) {
	if prefix == "" && strings.HasPrefix(partition, "mailbox#") {
		s.mu.Lock()
		s.bare++
		s.mu.Unlock()
	}
	out, err := s.Memory.ListEntries(ctx, partition, prefix, after, limit)
	if err != nil || prefix != "" || !strings.HasPrefix(partition, "mailbox#") {
		return out, err
	}
	msgs, err := s.ListMessages(ctx, strings.TrimPrefix(partition, "mailbox#"), limit, "")
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		out = append(out, store.Entry{Partition: partition, Key: "msg#" + m.ID})
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *awsLike) DeleteEntry(ctx context.Context, partition, key string) error {
	s.mu.Lock()
	s.deleted = append(s.deleted, key)
	s.mu.Unlock()
	return s.Memory.DeleteEntry(ctx, partition, key)
}

var contractsCreate = contracts.MailboxCreate{Label: "Work"}

func TestReview4TheSweepEndsOnlyWhenNothingIsLeft(t *testing.T) {
	w := newWorld(t)
	aws := &awsLike{Memory: w.mem}
	s, err := mailboxes.New(mailboxes.Config{
		Records: &store.Anchored{Records: w.mem, Commits: w.commits, OnlyNewest: true, Strict: true, Now: w.clock.now},
		Storage: aws, Keys: w.keys, Accounts: w.accounts, Notifier: w.notices, Domain: domain, Now: w.clock.now,
		Log: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	me := caller("acc_me", w.clock)
	m, err := s.Create(ctx, me, contractsCreate)
	if err != nil {
		t.Fatal(err)
	}
	const many = 10001
	for i := range many {
		msg := &store.Message{MailboxID: m.ID, ID: fmt.Sprintf("%016x%016x", start.UnixNano(), i), ReceivedAt: start, ExpiresAt: start.AddDate(0, 0, 90), Size: 1, RetentionDays: 90, Meta: []byte("x")}
		if err := w.mem.PutRaw(ctx, msg, []byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := w.mem.PutItem(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Disconnect(ctx, me, m.ID); err != nil {
		t.Fatal(err)
	}
	w.clock.add(mailboxes.SweepGrace)
	work := func() int {
		l, _ := w.mem.ListEntries(ctx, "mailboxes-disconnecting", "", "", 100)
		return len(l)
	}
	for pass := 0; work() > 0; pass++ {
		if pass == 10 {
			t.Fatal("the sweep never ends")
		}
		if err := s.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		if left := len(w.messages(m.ID)); work() == 0 && left > 0 {
			t.Fatalf("the work entry went with %d messages left", left)
		}
	}
	if left := len(w.messages(m.ID)); left != 0 {
		t.Fatalf("%d messages left", left)
	}
	for _, k := range aws.deleted {
		if strings.HasPrefix(k, "msg#") {
			t.Fatalf("a message row deleted as an entry: %s", k)
		}
	}
	if aws.bare != 0 {
		t.Fatalf("%d listings of a mailbox's partition without a prefix", aws.bare)
	}
	if h := w.history("mailbox-" + m.ID); !h.Head.Gone() {
		t.Fatal("the record stays")
	}
}

// Review 10: disconnecting twice answers success to its owner until the
// sweep's last pass removed the mailbox, and not_found to everyone after:
// the contract's C17, here on the service; the fake keeps it too.
// Review 14: the fake had no limit on settings changes: the contract's C18.

// M29, strengthened: the mark is read back and authenticated, and says what
// the claim fixed: the stored message's identifier, received time, expiry and
// retention.
func TestM29TheMarkIsStoredAndSaysWhatTheClaimFixed(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	raw := message("overlap")
	held, goOn := make(chan struct{}), make(chan struct{})
	var once onlyOnce
	w.setInterrupt(func(_ context.Context, step store.Step, _, _ string) error {
		if step == store.StepItem {
			once.Do(func() { close(held); <-goOn })
		}
		return nil
	})
	done := make(chan error, 1)
	go func() { done <- a.deliver(t, raw, m.Address) }()
	<-held
	w.clock.add(mailboxes.ClaimStale + time.Minute)
	if err := b.deliver(t, raw, m.Address); err != nil {
		t.Fatal(err)
	}
	close(goOn)
	if err := <-done; err != nil {
		t.Fatalf("A: %v", err)
	}
	w.setInterrupt(nil)
	msgs := w.messages(m.ID)
	if len(msgs) != 1 || !msgs[0].ReceivedAt.Equal(start) {
		t.Fatalf("%+v", msgs)
	}
	box, err := a.Open(ctx, me, m.ID, mailboxes.AccessReadMessage)
	if err != nil {
		t.Fatal(err)
	}
	seen, err := inbox.SeenKey(box.Key, "alice@example.org", raw)
	if err != nil {
		t.Fatal(err)
	}
	// Claim opens the entry under the mailbox's key and reads what it says:
	// stored, with the fields the first claim fixed.
	wr := inbox.Writer{Entries: w.mem, Parts: w.mem, Now: w.clock.now, SeenFor: mailboxes.SeenFor, ClaimStale: mailboxes.ClaimStale}
	c, err := wr.Claim(ctx, box, seen, 7)
	if err != nil || !c.Stored() {
		t.Fatalf("the mark is not stored: %v", err)
	}
	got := msgs[0]
	if c.Seen.Message != got.ID || !c.Seen.ReceivedAt.Equal(got.ReceivedAt) || !c.Seen.ExpiresAt.Equal(got.ExpiresAt) ||
		c.Seen.RetentionDays != got.RetentionDays || c.Seen.StoredAt.IsZero() {
		t.Fatalf("the mark says %+v of %+v", c.Seen, got)
	}
}
