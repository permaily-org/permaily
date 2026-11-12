package mailboxes_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/store"
)

// The interleavings of ADR 0026 that are the mailboxes service's (M1 to
// M29), each a test with two enclaves, A and B, over one store; C starts
// later on the same store when a case says so.

func isList(name string) bool    { return strings.HasPrefix(name, "mailboxes-") }
func isMailbox(name string) bool { return strings.HasPrefix(name, "mailbox-") }

func listed(t *testing.T, e *enclave, c mailboxes.Caller) []contracts.Mailbox {
	t.Helper()
	l, err := e.List(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	return l.Mailboxes
}

// M1: an account with four mailboxes makes one through A and one through B
// at once: exactly one succeeds, the other is too_many_mailboxes.
func TestM1TheFifthMadeTwiceAtOnce(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	for range 4 {
		a.mustCreate(t, me)
	}
	var errB error
	a.records.set(func(op, name string) error {
		if op == "put" && isList(name) {
			a.records.set(nil)
			_, errB = b.Create(ctx, me, contracts.MailboxCreate{})
		}
		return nil
	})
	_, errA := a.Create(ctx, me, contracts.MailboxCreate{})
	if errB != nil || !errors.Is(errA, contracts.ErrTooManyMailboxes) {
		t.Fatalf("A %v, B %v", errA, errB)
	}
	if n, _ := a.Count(ctx, me.Account); n != mailboxes.MaxMailboxes {
		t.Fatalf("count %d", n)
	}
}

// pointers counts the pointers from addresses in the table.
func (w *world) pointers() int {
	n := 0
	for _, e := range w.mem.AllEntries() {
		if strings.HasPrefix(e.Partition, "rcpt#") {
			n++
		}
	}
	return n
}

// reserveAndStop makes a mailbox through e in the background, stopped just
// before its record is made: go lets it go on.
func reserveAndStop(t *testing.T, e *enclave, c mailboxes.Caller) (stopped chan struct{}, goOn chan struct{}, done chan error) {
	t.Helper()
	stopped, goOn, done = make(chan struct{}), make(chan struct{}), make(chan error, 1)
	e.records.set(func(op, name string) error {
		if op == "create" && isMailbox(name) {
			e.records.set(nil)
			close(stopped)
			<-goOn
		}
		return nil
	})
	go func() {
		_, err := e.Create(ctx, c, contracts.MailboxCreate{})
		done <- err
	}()
	<-stopped
	return stopped, goOn, done
}

// M2: A reserves an identifier and stops before its record. Eleven minutes
// later B lists the mailboxes: it writes the tombstone, frees the slot and
// makes another. A resumes: its CreateNew fails, its pointer is deleted, and
// it answers an error. Five mailboxes, all listed, only listed ones accept.
func TestM2AStaleReservationIsCancelled(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	for range 4 {
		a.mustCreate(t, me)
	}
	_, goOn, done := reserveAndStop(t, a, me)
	w.clock.add(11 * time.Minute)
	me = caller("acc_me", w.clock)
	if l := listed(t, b, me); len(l) != 4 {
		t.Fatalf("listed %d", len(l))
	}
	b.mustCreate(t, me)
	close(goOn)
	if err := <-done; err == nil {
		t.Fatal("A's creation succeeded over a tombstone")
	}
	l := listed(t, a, me)
	if len(l) != 5 || w.pointers() != 5 {
		t.Fatalf("listed %d, pointers %d", len(l), w.pointers())
	}
	for _, m := range l {
		if _, err := a.Accept(ctx, m.Address); err != nil {
			t.Fatalf("a listed mailbox refuses mail: %v", err)
		}
	}
}

// M3: M2, but A's CreateNew lands just before B's tombstone: B's is
// ErrExists, B reads the record and keeps the identifier.
func TestM3TheRecordLandsBeforeTheTombstone(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	_, goOn, done := reserveAndStop(t, a, me)
	w.clock.add(11 * time.Minute)
	me = caller("acc_me", w.clock)
	b.records.set(func(op, name string) error {
		if op == "create" && isMailbox(name) {
			b.records.set(nil)
			close(goOn)
			if err := <-done; err != nil {
				t.Errorf("A: %v", err)
			}
		}
		return nil
	})
	if l := listed(t, b, me); len(l) != 1 || l[0].State != contracts.MailboxWaiting {
		t.Fatalf("listed %+v", l)
	}
}

// M4: A reserves and stops; the account is removed through B (the list
// closed, the reservation fenced); A resumes: its CreateNew fails.
func TestM4RemovedWhileReserving(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	_, goOn, done := reserveAndStop(t, a, me)
	if err := b.RemoveAccount(ctx, me.Account); err != nil {
		t.Fatal(err)
	}
	close(goOn)
	if err := <-done; !errors.Is(err, store.ErrExists) {
		t.Fatalf("A: %v", err)
	}
	if w.pointers() != 0 {
		t.Fatal("a pointer of a fenced reservation stays")
	}
	if _, err := a.Create(ctx, me, contracts.MailboxCreate{}); !errors.Is(err, contracts.ErrAccountClosing) {
		t.Fatal(err)
	}
}

// M5: A reads the list to make a mailbox; B closes it to remove the account;
// A's write is ErrChanged, and A's reread answers account_closing.
func TestM5ClosedBetweenTheReadAndTheWrite(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	a.mustCreate(t, me)
	a.records.set(func(op, name string) error {
		if op == "put" && isList(name) {
			a.records.set(nil)
			if err := b.RemoveAccount(ctx, me.Account); err != nil {
				t.Error(err)
			}
		}
		return nil
	})
	if _, err := a.Create(ctx, me, contracts.MailboxCreate{}); !errors.Is(err, contracts.ErrAccountClosing) {
		t.Fatal(err)
	}
}

// M6: A has accepted a recipient at RCPT TO; B disconnects the mailbox; A's
// DATA reads the record again: not stored, 451; the retry is refused at
// RCPT TO with 550.
func TestM6DisconnectedBetweenRcptAndData(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	id, err := a.Accept(ctx, m.Address)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Disconnect(ctx, me, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.Deliver(ctx, "alice@example.org", []string{id}, message("late")); !errors.Is(err, mailboxes.ErrNotAllStored) {
		t.Fatal(err)
	}
	if _, err := a.Accept(ctx, m.Address); !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
		t.Fatal(err)
	}
	if len(w.messages(m.ID)) != 0 {
		t.Fatal("stored in a disconnected mailbox")
	}
}

// sweepAll runs a sweep through e and fails on an error.
func sweepAll(t *testing.T, e *enclave) {
	t.Helper()
	if err := e.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
}

// M7: A has read the record for delivery and is writing; B disconnects and
// its sweep runs: the sweep's last pass waits SweepGrace after
// disconnected_at, and deletes what A wrote.
func TestM7ADeliveryWritingWhileTheSweepRuns(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	id, _ := a.Accept(ctx, m.Address)
	held, goOn := make(chan struct{}), make(chan struct{})
	var once onlyOnce
	w.setInterrupt(func(_ context.Context, step store.Step, _, _ string) error {
		if step == store.StepItem {
			once.Do(func() { close(held); <-goOn })
		}
		return nil
	})
	done := make(chan error, 1)
	go func() { done <- a.Deliver(ctx, "alice@example.org", []string{id}, message("racing")) }()
	<-held
	if err := b.Disconnect(ctx, me, m.ID); err != nil {
		t.Fatal(err)
	}
	sweepAll(t, b) // within SweepGrace: deletes what is there, keeps the record
	close(goOn)
	if err := <-done; err == nil {
		t.Fatal("answered 250 for a mailbox disconnected meanwhile")
	}
	w.setInterrupt(nil)
	if len(w.messages(m.ID)) != 1 {
		t.Fatal("A's late write is not there to be swept")
	}
	if h := w.history("mailbox-" + m.ID); h.Head.Gone() {
		t.Fatal("removed before SweepGrace")
	}
	w.clock.add(mailboxes.SweepGrace)
	sweepAll(t, b)
	if len(w.messages(m.ID)) != 0 || !w.history("mailbox-"+m.ID).Head.Gone() {
		t.Fatal("the last pass left the message or the record")
	}
	if left, _ := w.mem.ListEntries(ctx, "mailboxes-disconnecting", "", "", 10); len(left) != 0 {
		t.Fatal("the disconnect entry stays")
	}
	if left, _ := w.mem.ListEntries(ctx, inbox.Partition(m.ID), "", "", 10); len(left) != 0 {
		t.Fatalf("entries left: %d", len(left))
	}
}

// M8: A and B disconnect one mailbox at once: one write of disconnecting
// wins; both answer 204; one notice.
func TestM8DisconnectedTwiceAtOnce(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	var errB error
	a.records.set(func(op, name string) error {
		if op == "put" && isMailbox(name) {
			a.records.set(nil)
			errB = b.Disconnect(ctx, me, m.ID)
		}
		return nil
	})
	if err := a.Disconnect(ctx, me, m.ID); err != nil || errB != nil {
		t.Fatalf("A %v, B %v", err, errB)
	}
	if n := count(w.kinds(), mailboxes.NoticeDisconnected); n != 1 {
		t.Fatalf("%d notices", n)
	}
	if v := w.history("mailbox-" + m.ID).Head.Version; v != 2 {
		t.Fatalf("version %d", v)
	}
}

// M9: A disconnects while B pauses: whichever writes second reads again; a
// pause of a disconnecting mailbox is not_found, a disconnect of a paused one
// goes on.
func TestM9DisconnectAndPause(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	var errB error
	b.records.set(func(op, name string) error {
		if op == "put" && isMailbox(name) {
			b.records.set(nil)
			if err := a.Disconnect(ctx, me, m.ID); err != nil {
				t.Error(err)
			}
		}
		return nil
	})
	_, errB = b.Pause(ctx, m.ID)
	if !errors.Is(errB, contracts.ErrNotFound) {
		t.Fatalf("pause of a disconnecting mailbox: %v", errB)
	}
	n := a.mustCreate(t, me)
	a.records.set(func(op, name string) error {
		if op == "put" && isMailbox(name) {
			a.records.set(nil)
			if _, err := b.Pause(ctx, n.ID); err != nil {
				t.Error(err)
			}
		}
		return nil
	})
	if err := a.Disconnect(ctx, me, n.ID); err != nil {
		t.Fatalf("disconnect of a paused one: %v", err)
	}
	if _, err := a.Record(ctx, n.ID); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatal(err)
	}
}

// M10: a disconnect stops after step 3; the disconnect entry is deleted by
// hand. B lists the mailboxes: the mailbox is not listed, the slot is free,
// the entry is back, and the sweep deletes the mail.
func TestM10ADisconnectStoppedAfterItsCommit(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	if err := a.deliver(t, message("kept"), m.Address); err != nil {
		t.Fatal(err)
	}
	committed := false
	a.records.set(func(op, name string) error {
		if op == "put" && isMailbox(name) {
			committed = true
		}
		if op == "put" && isList(name) && committed {
			return errStopped
		}
		return nil
	})
	if err := a.Disconnect(ctx, me, m.ID); !errors.Is(err, errStopped) {
		t.Fatal(err)
	}
	for _, e := range w.mem.AllEntries() {
		if e.Partition == "mailboxes-disconnecting" {
			if err := w.mem.DeleteEntry(ctx, e.Partition, e.Key); err != nil {
				t.Fatal(err)
			}
		}
	}
	if l := listed(t, b, me); len(l) != 0 {
		t.Fatalf("listed %+v", l)
	}
	if n, _ := b.Count(ctx, me.Account); n != 0 {
		t.Fatal("the slot is held")
	}
	if left, _ := w.mem.ListEntries(ctx, "mailboxes-disconnecting", "", "", 10); len(left) != 1 {
		t.Fatal("the entry is not back")
	}
	w.clock.add(mailboxes.SweepGrace)
	sweepAll(t, b)
	if len(w.messages(m.ID)) != 0 {
		t.Fatal("the mail stays")
	}
}

// M11: A and B sweep one mailbox at once: every item deleted once, the
// record removed once, the entry deleted once.
func TestM11SweptTwiceAtOnce(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	for _, s := range []string{"one", "two", "three"} {
		if err := a.deliver(t, message(s), m.Address); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Disconnect(ctx, me, m.ID); err != nil {
		t.Fatal(err)
	}
	w.clock.add(mailboxes.SweepGrace)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Go(func() { errs[0] = a.Sweep(ctx) })
	wg.Go(func() { errs[1] = b.Sweep(ctx) })
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatal(errs)
	}
	h := w.history("mailbox-" + m.ID)
	// Made, receiving since, disconnecting, gone: one removal.
	if !h.Head.Gone() || h.Head.Version != 4 {
		t.Fatalf("head %+v", h.Head)
	}
	if len(w.messages(m.ID)) != 0 {
		t.Fatal("messages left")
	}
	if left, _ := w.mem.ListEntries(ctx, "mailboxes-disconnecting", "", "", 10); len(left) != 0 {
		t.Fatal("entry left")
	}
}

// M12: one message delivered through A and B at once: one claim wins; the
// other answers 451; the sender's retry finds the mark and answers 250; one
// copy.
func TestM12OneMessageThroughBothAtOnce(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	raw := message("twice")
	var errB error
	var once onlyOnce
	w.setInterrupt(func(_ context.Context, step store.Step, _, _ string) error {
		if step == store.StepRaw {
			once.Do(func() { errB = b.deliver(t, raw, m.Address) })
		}
		return nil
	})
	if err := a.deliver(t, raw, m.Address); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(errB, mailboxes.ErrNotAllStored) {
		t.Fatalf("B: %v", errB)
	}
	w.setInterrupt(nil)
	if err := b.deliver(t, raw, m.Address); err != nil {
		t.Fatalf("the retry: %v", err)
	}
	if len(w.messages(m.ID)) != 1 {
		t.Fatal("not one copy")
	}
}

// M13: A claims a message, stores part of it and stops. Within ClaimStale
// B's retry answers 451; after, B takes the claim over with the same
// identifier, writes the same keys and marks it; one copy, complete.
func TestM13AStoppedDeliveryTakenOver(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	raw := message("stopped")
	w.setInterrupt(func(_ context.Context, step store.Step, _, _ string) error {
		if step == store.StepItem {
			return errStopped
		}
		return nil
	})
	if err := a.deliver(t, raw, m.Address); !errors.Is(err, mailboxes.ErrNotAllStored) {
		t.Fatal(err)
	}
	w.setInterrupt(nil)
	if err := b.deliver(t, raw, m.Address); !errors.Is(err, mailboxes.ErrNotAllStored) {
		t.Fatalf("within ClaimStale: %v", err)
	}
	w.clock.add(mailboxes.ClaimStale)
	if err := b.deliver(t, raw, m.Address); err != nil {
		t.Fatal(err)
	}
	msgs := w.messages(m.ID)
	if len(msgs) != 1 || !msgs[0].ReceivedAt.Equal(start) {
		t.Fatalf("%+v", msgs)
	}
	if _, err := w.mem.ReadRaw(ctx, m.ID, msgs[0].ID, 1<<20); err != nil {
		t.Fatal("no raw object", err)
	}
}

// M14: a message for X and Y; Y is paused between RCPT TO and the end of
// DATA: X stored, the answer 451; on the retry X is found stored and Y
// refused at RCPT TO; X holds one copy.
func TestM14OneRecipientPausedMeanwhile(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	x, y := a.mustCreate(t, me), a.mustCreate(t, me)
	ix, _ := a.Accept(ctx, x.Address)
	iy, _ := a.Accept(ctx, y.Address)
	if _, err := b.Pause(ctx, y.ID); err != nil {
		t.Fatal(err)
	}
	raw := message("both")
	if err := a.Deliver(ctx, "alice@example.org", []string{ix, iy}, raw); !errors.Is(err, mailboxes.ErrNotAllStored) {
		t.Fatal(err)
	}
	if _, err := a.Accept(ctx, y.Address); !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
		t.Fatal(err)
	}
	if err := a.deliver(t, raw, x.Address); err != nil {
		t.Fatal(err)
	}
	if len(w.messages(x.ID)) != 1 || len(w.messages(y.ID)) != 0 {
		t.Fatal("copies")
	}
}

// M16: the record put back by hand to its active version after a
// disconnect: refused; the mailbox stays disconnected.
func TestM16TheActiveRecordPutBack(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	var saved []store.Record
	for _, n := range w.mem.RecordNames() {
		if strings.HasPrefix(n, "mailbox-"+m.ID) {
			r, _ := w.mem.GetRecord(ctx, n)
			saved = append(saved, *r)
		}
	}
	if err := a.Disconnect(ctx, me, m.ID); err != nil {
		t.Fatal(err)
	}
	for _, r := range saved {
		w.mem.SetRecord(r)
	}
	for _, e := range []*enclave{a, b, w.enclave()} {
		if _, err := e.Accept(ctx, m.Address); !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
			t.Fatal(err)
		}
		if _, err := e.Open(ctx, me, m.ID, mailboxes.AccessDownload); !errors.Is(err, contracts.ErrNotFound) {
			t.Fatal(err)
		}
	}
}

// M17: the pointer put back after a disconnect, or copied under the HMAC of
// another address: 550.
func TestM17PointersPutBackOrCopied(t *testing.T) {
	w := newWorld(t)
	a := w.enclave()
	me := caller("acc_me", w.clock)
	m, other := a.mustCreate(t, me), a.mustCreate(t, me)
	var saved, otherPointer store.Entry
	for _, e := range w.mem.AllEntries() {
		if strings.HasPrefix(e.Partition, "rcpt#") {
			if id, _ := a.Accept(ctx, m.Address); id != "" && saved.Partition == "" {
				saved = e
			}
		}
	}
	// Which pointer is m's: the one Accept follows.
	for _, e := range w.mem.AllEntries() {
		if strings.HasPrefix(e.Partition, "rcpt#") {
			otherPointer = e
		}
	}
	if err := a.Disconnect(ctx, me, m.ID); err != nil {
		t.Fatal(err)
	}
	w.mem.SetEntry(saved)
	if _, err := a.Accept(ctx, m.Address); !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
		t.Fatal("a pointer put back", err)
	}
	// Every pointer copied under the name of another address leads nowhere.
	for _, e := range w.mem.AllEntries() {
		if strings.HasPrefix(e.Partition, "rcpt#") && e.Partition != otherPointer.Partition {
			copied := otherPointer
			copied.Partition = e.Partition
			w.mem.SetEntry(copied)
		}
	}
	if _, err := a.Accept(ctx, m.Address); !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
		t.Fatal("a pointer copied", err)
	}
	if _, err := a.Accept(ctx, other.Address); err != nil && !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
		t.Fatal(err)
	}
}

// M18: A pauses; mail through B: 550. B resumes; mail through A: accepted.
func TestM18PausedOnOneEnclaveRefusedOnTheOther(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	if _, err := a.Pause(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.deliver(t, message("paused"), m.Address); !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
		t.Fatal(err)
	}
	if got, _ := a.Get(ctx, me, m.ID); got.State != contracts.MailboxPaused {
		t.Fatal(got.State)
	}
	if _, err := b.Resume(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.deliver(t, message("resumed"), m.Address); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.Get(ctx, me, m.ID); got.State != contracts.MailboxReceiving || got.ReceivingSince.IsZero() {
		t.Fatalf("%+v", got)
	}
}

// M19: A and B change the settings at once: neither undoes the other.
func TestM19SettingsChangedTwiceAtOnce(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	label, days := "Home", 30
	var once onlyOnce
	w.setInterrupt(func(_ context.Context, step store.Step, _, key string) error {
		if step == store.StepEntry && key == inbox.KeySettings {
			once.Do(func() {
				if _, err := b.Update(ctx, me, m.ID, contracts.MailboxUpdate{Label: &label}); err != nil {
					t.Error(err)
				}
			})
		}
		return nil
	})
	if _, err := a.Update(ctx, me, m.ID, contracts.MailboxUpdate{RetentionDays: &days}); err != nil {
		t.Fatal(err)
	}
	got, _ := a.Get(ctx, me, m.ID)
	if got.Label != label || got.RetentionDays != days {
		t.Fatalf("%+v", got)
	}
}

// M20: A and B remove one account at once: every mailbox ends once, the
// list ends closed.
func TestM20AnAccountRemovedTwiceAtOnce(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	var ids []string
	for range 3 {
		ids = append(ids, a.mustCreate(t, me).ID)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Go(func() { errs[0] = a.RemoveAccount(ctx, me.Account) })
	wg.Go(func() { errs[1] = b.RemoveAccount(ctx, me.Account) })
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatal(errs)
	}
	for _, id := range ids {
		if v := w.history("mailbox-" + id).Head.Version; v != 2 {
			t.Fatalf("version %d: ended more than once", v)
		}
	}
	if _, err := a.Create(ctx, me, contracts.MailboxCreate{}); !errors.Is(err, contracts.ErrAccountClosing) {
		t.Fatal(err)
	}
	if n, _ := b.Count(ctx, me.Account); n != 0 {
		t.Fatal(n)
	}
	if n := count(w.kinds(), mailboxes.NoticeDisconnected); n != 0 {
		t.Fatal("a removal told of disconnects")
	}
}

// M21: a mailbox's record forked: RCPT TO refuses it on A and B; the list
// shows it unreadable and counts it; disconnecting deletes its pointer and
// answers mailbox_unreadable with the words of ErrMailboxForked, freeing
// nothing; the account's removal leaves it named in the closed list.
func TestM21AForkedRecord(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	w.fork(m.ID)
	for _, e := range []*enclave{a, b} {
		if _, err := e.Accept(ctx, m.Address); !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
			t.Fatal(err)
		}
	}
	if l := listed(t, b, me); len(l) != 1 || l[0].State != contracts.MailboxUnreadable {
		t.Fatalf("%+v", l)
	}
	if err := a.Disconnect(ctx, me, m.ID); err != contracts.ErrMailboxForked { //nolint:errorlint // the very value: its words
		t.Fatal(err)
	}
	if w.pointers() != 0 {
		t.Fatal("the pointer stays")
	}
	if n, _ := a.Count(ctx, me.Account); n != 1 {
		t.Fatal("freed")
	}
	if r, err := a.Record(ctx, m.ID); err != nil || r.State != contracts.MailboxUnreadable || r.Account != me.Account {
		t.Fatalf("%+v %v", r, err)
	}
	if err := b.RemoveAccount(ctx, me.Account); err != nil {
		t.Fatal(err)
	}
	if r, _ := a.Records(ctx, me.Account); len(r.Mailboxes) != 1 {
		t.Fatal("not named in the closed list")
	}
	if err := a.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
}

// M22: a confirmation arrives through A while the set-up reads through B: B
// shows it.
func TestM22AConfirmationSeenThroughTheOther(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	raw := []byte("From: Gmail Team <forwarding-noreply@google.com>\r\nTo: " + m.Address + "\r\n" +
		"Subject: (#123456789) Gmail Forwarding Confirmation - Receive Mail from someone@gmail.com\r\n\r\n" +
		"someone@gmail.com has requested to automatically forward mail to your email address " + m.Address + ".\r\nConfirmation code: 123456789\r\n")
	if err := a.deliver(t, raw, m.Address); err != nil {
		t.Fatal(err)
	}
	got, err := b.Get(ctx, me, m.ID)
	if err != nil || len(got.Confirmations) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	c := got.Confirmations[0]
	if c.Code != "123456789" || c.Requester != "someone@gmail.com" || c.Verified || c.Message == "" || got.State != contracts.MailboxWaiting || got.Source != nil {
		t.Fatalf("%+v", got)
	}
	// Thirty days later it is no longer shown.
	w.clock.add(mailboxes.ConfirmFor)
	if got, _ := b.Get(ctx, caller("acc_me", w.clock), m.ID); len(got.Confirmations) != 0 {
		t.Fatal("shown after it ended")
	}
}

// M23: the newest value of an active record is lost; the person disconnects
// through A: the record is removed, the pointer deleted through the list's
// rcpt, the slot freed. The exact value is put back: through A, B and C the
// record is gone.
func TestM23ALostRecordDisconnected(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	var saved []store.Record
	for _, n := range w.mem.RecordNames() {
		if strings.HasPrefix(n, "mailbox-"+m.ID+"@") {
			r, _ := w.mem.GetRecord(ctx, n)
			saved = append(saved, *r)
		}
	}
	w.lose(m.ID)
	if _, err := b.Accept(ctx, m.Address); !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
		t.Fatal(err)
	}
	if err := a.Disconnect(ctx, me, m.ID); err != nil {
		t.Fatal(err)
	}
	if w.pointers() != 0 {
		t.Fatal("the pointer stays")
	}
	for _, r := range saved {
		w.mem.SetRecord(r)
	}
	for _, e := range []*enclave{a, b, w.enclave()} {
		if _, err := e.Accept(ctx, m.Address); !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
			t.Fatal(err)
		}
		if l := listed(t, e, me); len(l) != 0 {
			t.Fatalf("%+v", l)
		}
	}
	for range mailboxes.MaxMailboxes {
		a.mustCreate(t, me)
	}
	if _, err := a.Create(ctx, me, contracts.MailboxCreate{}); !errors.Is(err, contracts.ErrTooManyMailboxes) {
		t.Fatal(err)
	}
	w.clock.add(mailboxes.SweepGrace)
	sweepAll(t, b)
	if n, _ := a.Total(ctx); n != mailboxes.MaxMailboxes {
		t.Fatalf("total %d", n)
	}
}

// M24: A passes Standing for an account with no list and stops; the account
// is removed through B, which makes the list closed; A's CreateNew of the
// list is ErrExists and it answers account_closing. In the other order, B's
// CreateNew is ErrExists, B closes the list and fences A's reservation.
func TestM24AFirstCreationAndARemovalRace(t *testing.T) {
	t.Run("the removal first", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.enclave(), w.enclave()
		me := caller("acc_me", w.clock)
		a.records.set(func(op, name string) error {
			if op == "create" && isList(name) {
				a.records.set(nil)
				if err := b.RemoveAccount(ctx, me.Account); err != nil {
					t.Error(err)
				}
			}
			return nil
		})
		if _, err := a.Create(ctx, me, contracts.MailboxCreate{}); !errors.Is(err, contracts.ErrAccountClosing) {
			t.Fatal(err)
		}
		if w.pointers() != 0 {
			t.Fatal("a pointer")
		}
	})
	t.Run("the creation first", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.enclave(), w.enclave()
		me := caller("acc_me", w.clock)
		_, goOn, done := reserveAndStop(t, a, me)
		b.records.set(func(op, name string) error {
			if op == "create" && isList(name) {
				t.Error("B made a list that A made")
			}
			return nil
		})
		if err := b.RemoveAccount(ctx, me.Account); err != nil {
			t.Fatal(err)
		}
		close(goOn)
		if err := <-done; err == nil {
			t.Fatal("A's next write succeeded")
		}
	})
}

// M25: A claims a message for a mailbox kept seven days, writes its raw
// object and stops. The person sets 365 days. After ClaimStale B takes the
// claim over: the item, the search entry and the raw object carry the claim's
// received time and seven-day expiry.
func TestM25TheClaimFixesTheRetention(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	seven, year := 7, 365
	if _, err := a.Update(ctx, me, m.ID, contracts.MailboxUpdate{RetentionDays: &seven}); err != nil {
		t.Fatal(err)
	}
	w.setInterrupt(func(_ context.Context, step store.Step, _, key string) error {
		if step == store.StepEntry && strings.HasPrefix(key, inbox.KeySearch) {
			return errStopped
		}
		return nil
	})
	raw := message("seven days")
	if err := a.deliver(t, raw, m.Address); err == nil {
		t.Fatal("not stopped")
	}
	w.setInterrupt(nil)
	if _, err := a.Update(ctx, me, m.ID, contracts.MailboxUpdate{RetentionDays: &year}); err != nil {
		t.Fatal(err)
	}
	w.clock.add(mailboxes.ClaimStale)
	if err := b.deliver(t, raw, m.Address); err != nil {
		t.Fatal(err)
	}
	msgs := w.messages(m.ID)
	if len(msgs) != 1 || msgs[0].RetentionDays != 7 || !msgs[0].ExpiresAt.Equal(start.AddDate(0, 0, 7)) || !msgs[0].ReceivedAt.Equal(start) {
		t.Fatalf("%+v", msgs)
	}
	search, err := w.mem.GetEntry(ctx, inbox.Partition(m.ID), inbox.KeySearch+msgs[0].ID)
	if err != nil || !search.ExpiresAt.Equal(start.AddDate(0, 0, 7)) {
		t.Fatalf("%+v %v", search, err)
	}
}

// M26: a delivery stopped before each of its writes: at every point a
// listing shows no message whose search entry or raw object is missing, and
// the sender's retry completes it.
func TestM26ADeliveryStoppedBeforeEachWrite(t *testing.T) {
	for _, stop := range []string{"raw", "search", "item", "mark"} {
		t.Run(stop, func(t *testing.T) {
			w := newWorld(t)
			a := w.enclave()
			me := caller("acc_me", w.clock)
			m := a.mustCreate(t, me)
			raw := message("stopped at " + stop)
			w.setInterrupt(func(_ context.Context, step store.Step, _, key string) error {
				switch {
				case stop == "raw" && step == store.StepRaw,
					stop == "search" && step == store.StepEntry && strings.HasPrefix(key, inbox.KeySearch),
					stop == "item" && step == store.StepItem,
					stop == "mark" && step == store.StepEntry && strings.HasPrefix(key, inbox.KeySeen) && w.messagesNow(m.ID) > 0:
					return errStopped
				}
				return nil
			})
			if err := a.deliver(t, raw, m.Address); err == nil {
				t.Fatal("not stopped")
			}
			w.setInterrupt(nil)
			for _, msg := range w.messages(m.ID) {
				if _, err := w.mem.GetEntry(ctx, inbox.Partition(m.ID), inbox.KeySearch+msg.ID); err != nil {
					t.Fatal("listed without its search entry")
				}
				if _, err := w.mem.ReadRaw(ctx, m.ID, msg.ID, 1<<20); err != nil {
					t.Fatal("listed without its raw object")
				}
			}
			w.clock.add(mailboxes.ClaimStale)
			if err := a.deliver(t, raw, m.Address); err != nil {
				t.Fatal(err)
			}
			if len(w.messages(m.ID)) != 1 {
				t.Fatal("not one copy")
			}
			if err := a.deliver(t, raw, m.Address); err != nil {
				t.Fatal("the mark", err)
			}
		})
	}
}

func (w *world) messagesNow(id string) int {
	msgs, _ := w.mem.ListMessages(context.Background(), id, 100, "")
	return len(msgs)
}

// M27: a request opens a mailbox through A, and the store holds its next
// read. B disconnects. The request ends at its deadline with an error and
// writes nothing after; the sweep's last pass comes at least SweepGrace after
// disconnected_at.
func TestM27ARequestHeldPastItsDeadline(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	label := "late"
	held := make(chan struct{})
	var once onlyOnce
	w.setInterrupt(func(ctx context.Context, step store.Step, _, key string) error {
		if step == store.StepRead && key == inbox.KeySettings {
			once.Do(func() { close(held) })
			<-ctx.Done() // held, as a slow request would be
			return ctx.Err()
		}
		return nil
	})
	reqCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond) // RequestDeadline, shortened
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := a.Update(reqCtx, me, m.ID, contracts.MailboxUpdate{Label: &label})
		done <- err
	}()
	<-held
	if err := b.Disconnect(ctx, me, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the request: %v", err)
	}
	w.setInterrupt(nil)
	sweepAll(t, b)
	if w.history("mailbox-" + m.ID).Head.Gone() {
		t.Fatal("removed before SweepGrace")
	}
	w.clock.add(mailboxes.SweepGrace)
	sweepAll(t, b)
	if !w.history("mailbox-" + m.ID).Head.Gone() {
		t.Fatal("not removed after SweepGrace")
	}
}

// M28: a settings change through A reads the record before B disconnects;
// its write lands after the commit and before the sweep's last pass, which
// deletes it.
func TestM28ASettingsWriteAfterTheDisconnect(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	label := "late"
	var once onlyOnce
	w.setInterrupt(func(_ context.Context, step store.Step, _, key string) error {
		if step == store.StepEntry && key == inbox.KeySettings {
			once.Do(func() {
				if err := b.Disconnect(ctx, me, m.ID); err != nil {
					t.Error(err)
				}
				sweepAll(t, b)
			})
		}
		return nil
	})
	if _, err := a.Update(ctx, me, m.ID, contracts.MailboxUpdate{Label: &label}); err == nil {
		t.Log("the late write answered") // its view reads the record again, which is gone
	}
	w.setInterrupt(nil)
	if _, err := w.mem.GetEntry(ctx, inbox.Partition(m.ID), inbox.KeySettings); err != nil {
		t.Fatal("the late write did not land", err)
	}
	w.clock.add(mailboxes.SweepGrace)
	sweepAll(t, b)
	if _, err := w.mem.GetEntry(ctx, inbox.Partition(m.ID), inbox.KeySettings); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("the last pass left it")
	}
}

// M29: a claim's holder runs past ClaimStale while B takes the claim over:
// both write the same items with the same fixed fields; one message,
// complete, marked once.
func TestM29TwoDeliveriesOverlapAnyway(t *testing.T) {
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
}
