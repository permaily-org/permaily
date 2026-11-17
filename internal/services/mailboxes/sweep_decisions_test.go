package mailboxes_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/inboxtest"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/store"
)

// The sweep and kept decisions (ADR 0028, "Kept decisions", pull request
// 3): A24's sweep of a disconnected mailbox and of a removed account, and
// A41's pass over the marks of deleted messages, through the real sweeper
// over the real store, its page budget, its grace and its cursor.

var errSweepTest = errors.New("the store refused it")

func boxOf(t *testing.T, id string) inbox.Box {
	t.Helper()
	key := make([]byte, envelope.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return inbox.Box{MailboxID: id, Key: key}
}

func decisionsFor(topic string) *inbox.KeptDecisions {
	return &inbox.KeptDecisions{V: 1, Views: map[string]inbox.KeptView{}, Answers: []inbox.KeptAnswer{{
		Content: strings.Repeat("a", 64), Var: "message", Op: "about", Topic: topic,
		Model: "gpt-6-luna", Prompt: decide.PromptVersion, P: 0.9, At: start,
	}}}
}

func msgID(n int) string { return fmt.Sprintf("%016x%016x", start.UnixNano(), n) }

// keepFor writes a kept entry for each of n messages of b.
func (w *world) keepFor(b inbox.Box, from, n int) {
	w.t.Helper()
	ks := inbox.NewKeptStore(w.mem)
	for i := from; i < from+n; i++ {
		if _, err := ks.Keep(ctx, b, msgID(i), start.AddDate(0, 0, 90), decisionsFor("invoices")); err != nil {
			w.t.Fatal(err)
		}
	}
}

func (w *world) decisions(mailbox string) int {
	w.t.Helper()
	n := 0
	for _, e := range w.mem.AllEntries() {
		if e.Partition == inbox.Partition(mailbox) && strings.HasPrefix(e.Key, inbox.KeyDecision) {
			n++
		}
	}
	return n
}

func (w *world) marks() map[string]store.Entry {
	out := map[string]store.Entry{}
	for _, e := range w.mem.AllEntries() {
		if e.Partition == inbox.GonePartition {
			out[e.Key] = e
		}
	}
	return out
}

// sweepUntil sweeps until done says so, at most max times.
func sweepUntil(t *testing.T, s *enclave, max int, done func() bool) int {
	t.Helper()
	for n := 1; n <= max; n++ {
		if err := s.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		if done() {
			return n
		}
	}
	t.Fatalf("not done after %d sweeps", max)
	return 0
}

// A24 (the reviewer's TestReviewSweepRemovesKeptDecisions): the real sweep
// of a disconnected mailbox, and of a removed account's, leaves no dec#
// entry: more than a page of them, within the sweep's page budget, and one
// kept after the disconnect by a request that read the mailbox before it,
// deleted by the grace pass.
func TestA24ReviewSweepRemovesKeptDecisions(t *testing.T) {
	w := newWorld(t)
	e := w.enclave()
	me := caller("acc_me", w.clock)
	m := e.mustCreate(t, me)
	b := boxOf(t, m.ID)
	w.keepFor(b, 0, 250)
	if err := e.Disconnect(ctx, me, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if n := w.decisions(m.ID); n != 0 {
		t.Fatalf("%d kept entries left by the first pass", n)
	}
	// A request that opened the mailbox before the disconnect keeps its
	// answers after the first pass.
	w.keepFor(b, 1000, 1)
	work := func() int {
		l, _ := w.mem.ListEntries(ctx, "mailboxes-disconnecting", "", "", 100)
		return len(l)
	}
	w.clock.add(mailboxes.SweepGrace)
	sweepUntil(t, e, 5, func() bool { return work() == 0 })
	if n := w.decisions(m.ID); n != 0 {
		t.Fatalf("%d kept entries left after the grace pass", n)
	}

	// An account removed: the same sweep.
	other := caller("acc_other", w.clock)
	m2 := e.mustCreate(t, other)
	w.keepFor(boxOf(t, m2.ID), 0, 120)
	if err := e.RemoveAccount(ctx, other.Account); err != nil {
		t.Fatal(err)
	}
	w.clock.add(mailboxes.SweepGrace)
	sweepUntil(t, e, 5, func() bool { return work() == 0 })
	if n := w.decisions(m2.ID); n != 0 {
		t.Fatalf("%d kept entries left of a removed account", n)
	}
}

// seed stores a message in b as delivery does.
func (w *world) seed(b inbox.Box, n int) string {
	w.t.Helper()
	id := msgID(n)
	m := inboxtest.Message{ID: id, ReceivedAt: start, RetentionDays: 30, Raw: []byte("Subject: hi\r\n\r\nhello"), Meta: inbox.StoredMeta{Subject: "hi"}}
	if err := inboxtest.Seed(ctx, w.mem, w.mem, b, m); err != nil {
		w.t.Fatal(err)
	}
	return id
}

// A41 through the sweep: A reads a message and waits on the model; B deletes
// it; A writes its decisions and stops before it reads the mark. The entry
// it left is deleted by the sweep's pass over the marks once SweepGrace has
// passed, and then the mark; the same after no sweep for 25 hours over the
// store that hides expired entries (the mark is still there, its expiry the
// message's); a cleanup that fails keeps the mark, and a later sweep's
// succeeds and only then deletes it.
func TestA41TheSweepCleansAKeepThatStoppedBeforeItsRead(t *testing.T) {
	for _, outage := range []time.Duration{0, 25 * time.Hour} {
		t.Run(fmt.Sprintf("outage %v", outage), func(t *testing.T) {
			w := newWorld(t)
			e := w.enclave()
			b := boxOf(t, "mbx_"+strings.Repeat("a", 26))
			id := w.seed(b, 1)
			r := inbox.NewReaderAt(w.mem, w.clock.now)
			ks := inbox.NewKeptStore(w.mem)
			mark := inbox.GoneKey(b.MailboxID, id)
			var once onlyOnce
			w.setInterrupt(func(_ context.Context, step store.Step, partition, key string) error {
				switch {
				case step == store.StepEntry && key == inbox.KeyDecision+id:
					once.Do(func() {
						if n, err := r.Delete(ctx, b, []string{id}); n != 1 || err != nil {
							t.Errorf("B: %d, %v", n, err)
						}
					})
				case step == store.StepRead && partition == inbox.GonePartition:
					return errStopped // A stops before it reads the mark
				}
				return nil
			})
			if _, err := ks.Keep(ctx, b, id, start.AddDate(0, 0, 30), decisionsFor("invoices")); !errors.Is(err, errStopped) {
				t.Fatalf("A: %v", err)
			}
			w.setInterrupt(nil)
			if w.decisions(b.MailboxID) != 1 {
				t.Fatal("the test did not leave A's entry")
			}
			// Before the grace, the sweep leaves both: a Keep that read the
			// message may still write.
			if err := e.Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			if w.decisions(b.MailboxID) != 1 || len(w.marks()) != 1 {
				t.Fatal("cleaned before the grace")
			}
			w.clock.add(mailboxes.SweepGrace + outage)
			got, ok := w.marks()[mark]
			if !ok || !got.ExpiresAt.Equal(start.AddDate(0, 0, 30)) {
				t.Fatalf("the mark after %v: %+v, %v", outage, got, ok)
			}
			if _, err := w.mem.GetEntry(ctx, inbox.GonePartition, mark); err != nil {
				t.Fatalf("the store hides the mark: %v", err)
			}
			// A cleanup that fails keeps the mark.
			w.setInterrupt(func(_ context.Context, step store.Step, _, key string) error {
				if step == store.StepDeleteEntry && key == inbox.KeyDecision+id {
					return errSweepTest
				}
				return nil
			})
			if err := e.Sweep(ctx); !errors.Is(err, errSweepTest) {
				t.Fatalf("Sweep = %v", err)
			}
			if _, ok := w.marks()[mark]; !ok || w.decisions(b.MailboxID) != 1 {
				t.Fatal("the mark went with its work undone")
			}
			// Deleting the mark fails: its work is done, it stays for the next.
			w.setInterrupt(func(_ context.Context, step store.Step, partition, _ string) error {
				if step == store.StepDeleteEntry && partition == inbox.GonePartition {
					return errSweepTest
				}
				return nil
			})
			if err := e.Sweep(ctx); !errors.Is(err, errSweepTest) {
				t.Fatalf("Sweep = %v", err)
			}
			if _, ok := w.marks()[mark]; !ok || w.decisions(b.MailboxID) != 0 {
				t.Fatal("the decision stays, or the mark went")
			}
			w.setInterrupt(nil)
			if err := e.Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			if _, ok := w.marks()[mark]; ok || w.decisions(b.MailboxID) != 0 {
				t.Fatal("the mark or the decision stays")
			}
		})
	}
}

// A41: more marks past their grace than one sweep's budget, behind as many
// that are not yet: each sweep goes on from where the last stopped, so the
// old ones are cleaned within as many sweeps as budgets it takes, and the
// young ones are reached again once the cursor starts over.
func TestA41TheSweepGoesOnThroughTheMarks(t *testing.T) {
	w := newWorld(t)
	e := w.enclave()
	// Young marks sort first (mailbox "mbx_a…"), old ones after ("mbx_b…").
	young, old := boxOf(t, "mbx_"+strings.Repeat("a", 26)), boxOf(t, "mbx_"+strings.Repeat("b", 26))
	write := func(b inbox.Box, n int, at time.Time) {
		w.keepFor(b, 0, n) // kept before the deletion's mark, or Keep would undo it
		for i := range n {
			w.mem.SetEntry(store.Entry{Partition: inbox.GonePartition, Key: inbox.GoneKey(b.MailboxID, msgID(i)), Value: []byte(`{"gone":true}`), Version: 1, ExpiresAt: start.AddDate(0, 0, 30), UpdatedAt: at})
		}
	}
	write(old, 150, start.Add(-mailboxes.SweepGrace))
	write(young, 100, start)
	sweeps := sweepUntil(t, e, 10, func() bool { return w.decisions(old.MailboxID) == 0 })
	if sweeps > 3 || w.decisions(young.MailboxID) != 100 {
		t.Fatalf("%d sweeps; %d young decisions left", sweeps, w.decisions(young.MailboxID))
	}
	if n := len(w.marks()); n != 100 {
		t.Fatalf("%d marks left", n)
	}
	w.clock.add(mailboxes.SweepGrace)
	sweepUntil(t, e, 3, func() bool { return len(w.marks()) == 0 })
	if w.decisions(young.MailboxID) != 0 {
		t.Fatal("young decisions stay")
	}
	// After a restart the cursor starts from the first mark again: a new
	// enclave reaches the first ones.
	write(young, 3, start)
	if err := w.enclave().Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(w.marks()) != 0 {
		t.Fatal("a new enclave did not start from the first mark")
	}
}

// A41: a mark never makes a message that still exists lose more than its
// kept answers: one put in the table by hand for a message that is there
// takes its dec# entry and nothing else; marks this deployment does not
// write are left alone, and a listing of the marks that fails is the
// sweep's error.
func TestA41AMarkTakesNothingButKeptAnswers(t *testing.T) {
	w := newWorld(t)
	e := w.enclave()
	b := boxOf(t, "mbx_"+strings.Repeat("c", 26))
	id := w.seed(b, 1)
	w.keepFor(b, 1, 1)
	before := len(w.mem.AllEntries())
	w.mem.SetEntry(store.Entry{Partition: inbox.GonePartition, Key: inbox.GoneKey(b.MailboxID, id), Value: []byte("x"), Version: 1, UpdatedAt: start.Add(-time.Hour)})
	for _, junk := range []string{"no-hash", "mbx_short#" + id, b.MailboxID + "#not-an-id"} {
		w.mem.SetEntry(store.Entry{Partition: inbox.GonePartition, Key: junk, Value: []byte("x"), Version: 1})
	}
	if err := e.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if w.decisions(b.MailboxID) != 0 {
		t.Fatal("the decision stays")
	}
	if _, err := w.mem.Item(ctx, b.MailboxID, id); err != nil {
		t.Fatalf("the message went: %v", err)
	}
	if _, err := w.mem.GetEntry(ctx, inbox.Partition(b.MailboxID), inbox.KeySearch+id); err != nil {
		t.Fatalf("its search entry went: %v", err)
	}
	if got := len(w.mem.AllEntries()); got != before-1+3 {
		t.Fatalf("%d entries, want %d", got, before-1+3)
	}
	if n := len(w.marks()); n != 3 {
		t.Fatalf("%d marks left, want the three junk ones", n)
	}
	w.setInterrupt(func(_ context.Context, step store.Step, partition, _ string) error {
		if step == store.StepRead && partition == inbox.GonePartition {
			return errSweepTest
		}
		return nil
	})
	if err := e.Sweep(ctx); !errors.Is(err, errSweepTest) {
		t.Fatalf("Sweep = %v", err)
	}
}

// interruptedDelete is a deletion whose raw deletion failed after its mark:
// the message's item and the mark are both still there, and SweepGrace has
// passed since the mark was written.
func (w *world) interruptedDelete(b inbox.Box, id string) inbox.Reader {
	w.t.Helper()
	r := inbox.NewReaderAt(w.mem, w.clock.now)
	w.setInterrupt(func(_ context.Context, step store.Step, _, _ string) error {
		if step == store.StepDeleteRaw {
			return errStopped
		}
		return nil
	})
	if _, err := r.Delete(ctx, b, []string{id}); !errors.Is(err, errStopped) {
		w.t.Fatal(err)
	}
	w.setInterrupt(nil)
	w.clock.add(mailboxes.SweepGrace)
	if _, err := w.mem.Item(ctx, b.MailboxID, id); err != nil {
		w.t.Fatal(err)
	}
	return r
}

// keptAfterTheDeletion is the Keep of a request that read the message
// before the deletion that just ended and waited on the model: it must find
// a mark, and leave no decision.
func (w *world) keptAfterTheDeletion(b inbox.Box, id string) {
	w.t.Helper()
	d, err := inbox.NewKeptStore(w.mem).Keep(ctx, b, id, start.AddDate(0, 0, 30), decisionsFor("invoices"))
	if !errors.Is(err, inbox.ErrGone) || d != nil || w.decisions(b.MailboxID) != 0 {
		w.t.Fatalf("a late Keep kept answers of a deleted message: err=%v returned=%v decisions=%d marks=%d", err, d != nil, w.decisions(b.MailboxID), len(w.marks()))
	}
}

// A24 and A41 (the review of pull request 116, its first finding): a
// deletion tried again reads the mark of the first, the sweep then deletes
// that mark (its grace passed) before the deletion writes it again at the
// version read: the deletion writes it anew before it deletes the message,
// and a Keep that read the message before finds it.
func TestA41AMarkSweptAfterTheRetrysReadIsWrittenAnew(t *testing.T) {
	w := newWorld(t)
	e := w.enclave()
	b := boxOf(t, "mbx_"+strings.Repeat("a", 26))
	id := w.seed(b, 1)
	r := w.interruptedDelete(b, id)
	writes := 0
	w.setInterrupt(func(_ context.Context, step store.Step, part, _ string) error {
		if step == store.StepEntry && part == inbox.GonePartition {
			if writes++; writes == 2 { // the write at the version read
				w.setInterrupt(nil)
				if err := e.Sweep(ctx); err != nil {
					t.Error(err)
				}
				if n := len(w.marks()); n != 0 {
					t.Errorf("the sweep left %d marks", n)
				}
			}
		}
		return nil
	})
	n, err := r.Delete(ctx, b, []string{id})
	w.setInterrupt(nil)
	if err != nil || n != 1 {
		t.Fatalf("the deletion tried again = %d, %v", n, err)
	}
	w.keptAfterTheDeletion(b, id)
	mark, ok := w.marks()[inbox.GoneKey(b.MailboxID, id)]
	if !ok || !mark.UpdatedAt.Equal(w.clock.now()) {
		t.Fatalf("no mark of the deletion: %+v", mark)
	}
}

// A41 (the review of pull request 116, its second finding): two sweeps and
// a deletion tried again. Sweep A reads the old mark and deletes its dec#;
// before A deletes the mark, sweep B deletes it, and the deletion tried
// again writes a new one, with the same version, and deletes the message.
// A's deletion of the mark as it read it leaves the new one, whose grace has
// not passed; a Keep that read the message before finds it, and a sweep
// after its grace deletes it.
func TestA41AnOldSweepLeavesANewMark(t *testing.T) {
	w := newWorld(t)
	a, other := w.enclave(), w.enclave()
	b := boxOf(t, "mbx_"+strings.Repeat("b", 26))
	id := w.seed(b, 1)
	r := w.interruptedDelete(b, id)
	key := inbox.GoneKey(b.MailboxID, id)
	old := w.marks()[key]
	w.setInterrupt(func(_ context.Context, step store.Step, part, _ string) error {
		if step == store.StepDeleteEntry && part == inbox.GonePartition {
			w.setInterrupt(nil)
			if err := other.Sweep(ctx); err != nil {
				t.Error(err)
			}
			if n, err := r.Delete(ctx, b, []string{id}); err != nil || n != 1 {
				t.Errorf("the deletion tried again = %d, %v", n, err)
			}
			if mark := w.marks()[key]; mark.Version != old.Version || !mark.UpdatedAt.Equal(w.clock.now()) {
				t.Errorf("the schedule needs a new mark at the old version: %+v, old %+v", mark, old)
			}
		}
		return nil
	})
	if err := a.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	w.setInterrupt(nil)
	w.keptAfterTheDeletion(b, id)
	if mark, ok := w.marks()[key]; !ok || !mark.UpdatedAt.Equal(w.clock.now()) {
		t.Fatalf("an old sweep deleted the new mark: %+v", mark)
	}
	w.clock.add(mailboxes.SweepGrace)
	sweepUntil(t, a, 1, func() bool { return len(w.marks()) == 0 })
}

// A41 (the review of pull request 116's control): the cursor over the marks
// lives as long as its Service, through a collection, and a new Service
// starts again from the first mark and still reaches every one.
func TestA41TheCursorSurvivesACollectionAndARestart(t *testing.T) {
	w := newWorld(t)
	e := w.enclave()
	young, old := boxOf(t, "mbx_"+strings.Repeat("a", 26)), boxOf(t, "mbx_"+strings.Repeat("z", 26))
	w.keepFor(young, 0, 100)
	w.keepFor(old, 0, 150)
	for _, b := range []inbox.Box{young, old} {
		n, at := 100, start
		if b.MailboxID == old.MailboxID {
			n, at = 150, start.Add(-mailboxes.SweepGrace)
		}
		for i := range n {
			w.mem.SetEntry(store.Entry{Partition: inbox.GonePartition, Key: inbox.GoneKey(b.MailboxID, msgID(i)), Value: []byte(`{"gone":true}`), Version: 1, ExpiresAt: start.AddDate(0, 0, 30), UpdatedAt: at})
		}
	}
	if err := e.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	if err := e.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.decisions(old.MailboxID); got != 50 {
		t.Fatalf("a collection lost the cursor: %d old decisions left", got)
	}
	e = w.enclave()
	runtime.GC()
	if err := e.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.decisions(old.MailboxID); got != 50 {
		t.Fatalf("a new Service did not start from the first mark: %d old decisions left", got)
	}
	if err := e.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.decisions(old.MailboxID); got != 0 {
		t.Fatalf("a new Service left marks: %d old decisions left", got)
	}
	runtime.KeepAlive(e)
}
