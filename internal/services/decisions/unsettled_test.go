package decisions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/disclosetest"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/store"
)

// goneFails is a store whose gone-marker read fails: Keep writes the
// answer, then fails the read it needs to confirm it (the second design
// review's N1).
type goneFails struct {
	*store.Memory
	fail bool
}

func (s *goneFails) GetEntry(ctx context.Context, p, k string) (*store.Entry, error) {
	if s.fail && p == inbox.GonePartition {
		return nil, errors.New("synthetic gone-marker read failure")
	}
	return s.Memory.GetEntry(ctx, p, k)
}

// N1 (ADR 0031, "Changed after the second design review"): the review's
// schedule against the real Asker. Keep stores 0.90 and 0.05, then fails;
// Ask answers both atoms Unsettled, so the decision is never Abandoned: it
// stays Pending, the message is held, and after PendingGrace it is
// recovered Held. The kept answer erased and a second enclave's 0.05 then
// change nothing: its late Cleared loses.
func TestReview2N1AKeepThatFailsAfterWritingStaysPending(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	st := &goneFails{Memory: store.NewMemory(), fail: true}
	kept := inbox.NewKeptStore(st)
	e := w.keyed(func(c *Config, _ *enclave) { c.Kept = kept })
	atoms := []query.Atom{
		about("a sign-in, login or verification code, or a one-time password"),
		about("resetting a password or recovering an account"),
	}
	id := inbox.NewMessageID(w.clock())
	q := newQuestion(id, "Hello", "Here is your number: 482913.", atoms...)
	e.model.set(atoms[0], 0.9)
	e.model.set(atoms[1], 0.05)
	d := disclosetest.NewDecisions(w.clock)
	rule := rules.Ref{ID: rules.DefaultID, Version: rules.DefaultVersion}
	keys := []inbox.DecisionKey{{ID: id, ExpiresAt: w.clock().Add(time.Hour)}}
	if begun, err := d.Begin(ctx, q.Box, rule, keys); err != nil || begun[id].State != inbox.DecisionBegun {
		t.Fatal(begun, err)
	}
	out := e.ask(q)
	if out.Called == 0 || len(out.Evidence) != 0 || len(out.Answers) != 0 || len(out.Unsettled) != 2 {
		t.Fatalf("the ask: %+v", out)
	}
	if saved, err := kept.Kept(ctx, q.Box, id); err != nil || saved == nil || len(saved.Answers) != 2 {
		t.Fatalf("the answers were not stored: %+v %v", saved, err)
	}
	s := inbox.SettleAfterAsk(query.Result{Value: query.Unknown, Model: atoms}, atoms, out, nil)
	if s.State != inbox.DecisionPending {
		t.Fatalf("settled %s on an uncertain ask", s.State)
	}

	// The table's writer erases the kept answer; a second enclave asks
	// within the grace: the decision is pending, the message held.
	if err := st.DeleteEntry(ctx, inbox.Partition(q.Box.MailboxID), inbox.KeyDecision+id); err != nil {
		t.Fatal(err)
	}
	st.fail = false
	if got, err := d.Begin(ctx, q.Box, rule, keys); err != nil || got[id].State != inbox.DecisionPending || got[id].Leaves() {
		t.Fatal(got, err)
	}
	// Past the grace whoever finds it recovers it Held.
	w.advance(inbox.PendingGrace)
	got, err := d.Begin(ctx, q.Box, rule, keys)
	if err != nil || got[id].State != inbox.DecisionPending || !inbox.OverdueAt(got[id].At, w.clock()) {
		t.Fatal(got, err)
	}
	if got, err := d.Settle(ctx, q.Box, rule, map[string]inbox.Settlement{id: {State: inbox.DecisionHeld}}); err != nil || got[id].State != inbox.DecisionHeld || !got[id].Recovered {
		t.Fatal(got, err)
	}
	e2 := w.keyed(func(c *Config, _ *enclave) { c.Kept = kept })
	e2.model.set(atoms[0], 0.05)
	e2.model.set(atoms[1], 0.05)
	out2 := e2.ask(newQuestion(id, q.Subject, "Here is your number: 482913.", atoms...))
	late := inbox.SettleAfterAsk(query.Result{Value: query.False}, atoms, out2, nil)
	if late.State != inbox.DecisionCleared {
		t.Fatalf("the second ask: %+v", late)
	}
	settled, err := d.Settle(ctx, q.Box, rule, map[string]inbox.Settlement{id: late})
	if err != nil || settled[id].Leaves() || settled[id].State != inbox.DecisionHeld {
		t.Fatalf("the late clear won: %+v %v", settled[id], err)
	}
}

// A failure before anything is asked is an error (the Asker's contract),
// with no call to the model; one after it, the kept entry failing, is
// Unsettled, never an error.
func TestAskFailsOnlyBeforeAsking(t *testing.T) {
	w := newWorld(t)
	e := w.keyed()
	atom := about("invoices")
	e.model.set(atom, 0.9)
	q := newQuestion("m1", "Invoice", "Pay it.", atom)
	q.Text = func(context.Context) (string, inbox.TextState, error) {
		return "", 0, errors.New("synthetic text failure")
	}
	out, err := e.Ask(context.Background(), q.Question)
	e.model.mu.Lock()
	calls := len(e.model.calls)
	e.model.mu.Unlock()
	if err == nil || out != nil || calls != 0 {
		t.Fatalf("%+v %v, %d calls", out, err, calls)
	}

	f := &failingEntries{Memory: store.NewMemory(), put: errors.New("synthetic write failure")}
	e = w.keyed(func(c *Config, _ *enclave) { c.Kept = inbox.NewKeptStore(f) })
	e.model.set(atom, 0.9)
	out, err = e.Ask(context.Background(), newQuestion("m2", "Invoice", "Pay it.", atom).Question)
	if err != nil || len(out.Unsettled) != 1 || len(out.Evidence) != 0 {
		t.Fatalf("%+v %v", out, err)
	}
}
