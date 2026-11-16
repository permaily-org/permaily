package decisions

import (
	"context"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/disclosetest"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/store"
)

// H26, R2 of the third design review (ADR 0031, "Changed after the third
// design review"), the review's schedule against the real Asker, KeptStore,
// query.Eval, SettleAfterAsk and the shared Decisions: the model answers
// 0.50 for the sign-in code and nothing usable for the password reset. Eval
// is Unknown, the default rule applies, and the decision is settled Held
// with the 0.50 it rests on, never Abandoned. The kept answer erased and
// another enclave's 0.05 and 0.05 then change nothing: the message never
// leaves. At 7bf1077 the decision was Abandoned and the second ask cleared
// it.
func TestReview3R2PartialDoubtIsHeldThroughTheRealAsker(t *testing.T) {
	partialDoubt(t, func(now func() time.Time) (inbox.Decisions, inbox.Decisions) {
		d := disclosetest.NewDecisions(now)
		return d, d
	})
}

// The same on the real strict store: the erasure and the second ask are
// the other enclave's.
func TestReview3R2PartialDoubtIsHeldOnStrictRecords(t *testing.T) {
	partialDoubt(t, func(now func() time.Time) (inbox.Decisions, inbox.Decisions) {
		a, b := disclosetest.Enclaves(now)
		return &inbox.BlockDecisions{Store: a, Now: now}, &inbox.BlockDecisions{Store: b, Now: now}
	})
}

func partialDoubt(t *testing.T, decisions func(now func() time.Time) (inbox.Decisions, inbox.Decisions)) {
	t.Helper()
	ctx := context.Background()
	w := newWorld(t)
	st := store.NewMemory()
	kept := inbox.NewKeptStore(st)
	e := w.keyed(func(c *Config, _ *enclave) { c.Kept = kept })
	atoms := []query.Atom{about("a sign-in, login or verification code, or a one-time password"), about("resetting a password or recovering an account")}
	e.model.onCall(func(_ context.Context, _ int, c decide.Call) (*decide.Result, error) {
		r := &decide.Result{Model: e.model.Name(), Answers: make([]decide.Answer, len(c.Statements))}
		for i, a := range c.Statements {
			if a == atoms[0] {
				r.Answers[i] = decide.Answer{P: .50, OK: true}
			}
		}
		return r, nil
	})
	id := inbox.NewMessageID(w.clock())
	q := newQuestion(id, "Hello", "Here is your number: 482913.", atoms...)
	d, other := decisions(w.clock)
	rule := rules.Default()
	keys := []inbox.DecisionKey{{ID: id, ExpiresAt: w.clock().Add(time.Hour)}}
	if got, err := d.Begin(ctx, q.Box, rule.Ref(), keys); err != nil || got[id].State != inbox.DecisionBegun {
		t.Fatal(got, err)
	}
	out := e.ask(q)
	if len(out.Evidence) != 1 || out.Answers[atoms[0]] != .5 || len(out.Unsettled) != 0 {
		t.Fatalf("unexpected ask %+v", out)
	}
	eval := func(a *inbox.Asked) query.Result {
		return query.Eval([]*query.Canonical{rule.Formulas[2]}, &query.Message{Have: query.FromItem | query.FromMeta}, a.Answers, query.ForRule)
	}
	r := eval(out)
	s := inbox.SettleAfterAsk(r, atoms, out, nil)
	if r.Value != query.Unknown || s.State != inbox.DecisionHeld || len(s.Evidence) != 1 || s.Evidence[0].Evidence.P != .5 {
		t.Fatalf("result=%+v settlement=%+v", r, s)
	}
	if got, err := d.Settle(ctx, q.Box, rule.Ref(), map[string]inbox.Settlement{id: s}); err != nil || got[id].State != inbox.DecisionHeld || got[id].Recovered {
		t.Fatal(got, err)
	}
	if err := st.DeleteEntry(ctx, inbox.Partition(q.Box.MailboxID), inbox.KeyDecision+id); err != nil {
		t.Fatal(err)
	}
	e2 := w.keyed(func(c *Config, _ *enclave) { c.Kept = kept })
	e2.model.set(atoms[0], .05)
	e2.model.set(atoms[1], .05)
	got, err := other.Begin(ctx, q.Box, rule.Ref(), keys)
	if err != nil || got[id].State != inbox.DecisionHeld || got[id].Leaves() || len(got[id].Evidence) != 1 || got[id].Evidence[0].Evidence.P != .5 {
		t.Fatal(got, err)
	}
	out2 := e2.ask(newQuestion(id, q.Subject, "Here is your number: 482913.", atoms...))
	final, err := other.Settle(ctx, q.Box, rule.Ref(), map[string]inbox.Settlement{id: inbox.SettleAfterAsk(eval(out2), atoms, out2, nil)})
	if err != nil || final[id].Leaves() || final[id].State != inbox.DecisionHeld {
		t.Fatal(final, err)
	}
}
