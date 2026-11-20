package inbox_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/disclosetest"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/redact"
	"github.com/permaily-org/permaily/internal/rules"
)

// The removals' failure paths: every way a decision, an ask or a read
// fails removes more, never less, and a page ends before what it cannot
// afford.

func TestU14APartRemovedWholeIsUnknownToTheQuery(t *testing.T) {
	w := newDWorld(t)
	q := &qMsg{at: qNow.Add(-time.Hour), from: []string{"no-reply@example.com"}, subject: "Plans", body: "Hello there.",
		attach: []contracts.MessageAttachment{{Index: 0, Name: "a.pdf", Type: "application/pdf", Size: 3}}}
	id := seedQ(t, w.st, w.b, q)
	notZ := cond(contracts.VarBody, contracts.OpContains, "zzz")
	notZ.Not = true
	asker := &viewAsker{p: map[string]float64{}}
	for _, c := range []struct {
		name string
		rm   *rules.Removal
		f    *query.Canonical
	}{
		{"body", parts(contracts.PartBody), formula(group(cond(contracts.VarBody, contracts.OpContains, "hello")))},
		{"body negated", parts(contracts.PartBody), formula(group(notZ))},
		{"attachments", parts(contracts.PartAttachments), formula(group(cond(contracts.VarAttachments, contracts.OpExists, "")))},
		{"attachment names", parts(contracts.PartAttachments), formula(group(cond(contracts.VarAttachmentName, contracts.OpContains, "a")))},
		{"subject under the model", parts(contracts.PartSubject), formula(group(cond(contracts.VarMessage, contracts.OpAbout, "plans")))},
		{"subject", parts(contracts.PartSubject), formula(group(cond(contracts.VarSubject, contracts.OpContains, "plans")))},
	} {
		if _, err := w.discA.Message(on(asker), w.b, with(allParts, []rules.Rule{removing("rul_x", c.rm)}, every, c.f), id); !errors.Is(err, contracts.ErrNotFound) {
			t.Fatalf("%s: a condition on a part removed whole matched: %v", c.name, err)
		}
	}
	if len(asker.seen) != 0 {
		t.Fatalf("a hidden atom was asked: %+v", asker.seen)
	}
	// A group that holds another condition still matches on it.
	either := formula(group(cond(contracts.VarBody, contracts.OpContains, "hello"), cond(contracts.VarFromDomain, contracts.OpIs, "example.com")))
	if got := w.read(on(asker), w.discA, with(allParts, []rules.Rule{removing("rul_x", parts(contracts.PartBody))}, every, either), id); textOf(got) != "[removed]" {
		t.Fatalf("%q", textOf(got))
	}
}

// sentencesWith is a SentenceAsker answering with outcome, or failing with
// err, or waiting for its deadline.
type sentencesWith struct {
	outcome inbox.AskOutcome
	err     error
	wait    bool
}

func (s *sentencesWith) AskSentences(ctx context.Context, q *inbox.SentenceQuestion) (*inbox.SentencesAsked, error) {
	if s.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if s.err != nil {
		return nil, s.err
	}
	return &inbox.SentencesAsked{Outcome: s.outcome}, nil
}

func TestU19WhatBecameOfAskingForMeaning(t *testing.T) {
	w := newDWorld(t)
	id := w.seed(qNow.Add(-time.Hour), 0, "Hi", "Some text.")
	d := with(allParts, []rules.Rule{removing("rul_home", topics(topicHome))})
	for _, c := range []struct {
		s    *sentencesWith
		want contracts.MatchModel
	}{{&sentencesWith{outcome: inbox.AskLimit}, contracts.MatchModelLimit}, {&sentencesWith{outcome: inbox.AskOff}, contracts.MatchModelOff}} {
		p := w.page(onSentences(w.askA, c.s), w.discA, d, "", 10)
		if p.Model != c.want || len(p.Items) != 1 || p.Items[0].Disclosed == nil || textOf(p.Items[0].Disclosed) != "<none>" || subjectOf(p.Items[0].Disclosed) != "[removed]" {
			t.Fatalf("%v: %+v", c.want, p)
		}
	}
	// An ask that outlives its message's deadline answered nothing.
	inbox.AskWithin(w.discA, 20*time.Millisecond)
	if got := w.read(onSentences(w.askA, &sentencesWith{wait: true}), w.discA, d, id); textOf(got) != "[removed]" {
		t.Fatalf("%q", textOf(got))
	}
	// An error is the request's.
	errAsk := errors.New("the guard failed")
	if _, err := w.discA.Message(onSentences(w.askA, &sentencesWith{err: errAsk}), w.b, d, id); !errors.Is(err, errAsk) {
		t.Fatalf("%v", err)
	}
	if _, err := w.discA.Page(onSentences(w.askA, &sentencesWith{err: errAsk}), w.b, d, "", 10); !errors.Is(err, errAsk) {
		t.Fatalf("%v", err)
	}
}

func TestU18AMeaningDecisionThatCannotBeUsedRemovesTheWholeText(t *testing.T) {
	var parts strictParts
	fa := &failWrites{}
	w := newStrictDWorld(t, func(now func() time.Time) (inbox.DecisionStore, inbox.DecisionStore) {
		a, b := strictWith(&parts)(now)
		fa.DecisionStore = a
		return fa, b
	})
	meaning := removing("rul_home", topics(topicHome))
	d := with(allParts, []rules.Rule{meaning})
	ref := rules.MeaningRef(meaning.Ref(), inbox.TextVersion, redact.SplitVersion)
	ctx := context.Background()
	whole := func(id, why string) {
		t.Helper()
		got := w.read(onSentences(w.askA, &disclosetest.Sentences{}), w.discA, d, id)
		if subjectOf(got) != "[removed]" || textOf(got) != "[removed]" || !got.Removed[0].Removed.Whole || got.Removed[0].Removed.Why != why {
			t.Fatalf("%s: %q / %q %+v", id, subjectOf(got), textOf(got), got.Removed)
		}
	}
	key := func(id string) []inbox.DecisionKey {
		return []inbox.DecisionKey{{ID: id, ExpiresAt: qNow.AddDate(0, 0, 30)}}
	}
	// Settled Held: removed for good.
	held := w.seed(qNow.Add(-time.Hour), 0, "Hi", "Some text.")
	b, _ := w.decB.Begin(ctx, w.b, ref, key(held))
	if _, err := w.decB.Settle(ctx, w.b, ref, map[string]inbox.Settlement{held: {State: inbox.DecisionHeld, Attempt: b[held].Attempt,
		Evidence: []inbox.Decided{{Atom: topicHome, Evidence: inbox.Evidence{P: 0.9, Model: playedModel, At: qNow}}}}}); err != nil {
		t.Fatal(err)
	}
	whole(held, string(inbox.HeldDecided))
	// Cleared on another map: never read against this one.
	other := w.seed(qNow.Add(-2*time.Hour), 0, "Hi", "Some text.")
	b, _ = w.decB.Begin(ctx, w.b, ref, key(other))
	wrong := &inbox.MeaningKept{Map: strings.Repeat("0", 32), Kept: []bool{true, true}}
	if _, err := w.decB.Settle(ctx, w.b, ref, map[string]inbox.Settlement{other: {State: inbox.DecisionCleared, Meaning: wrong, Attempt: b[other].Attempt}}); err != nil {
		t.Fatal(err)
	}
	whole(other, string(inbox.HeldUndecided))
	// Its blocks cannot be written: nothing begun, nothing asked.
	fa.set(0, errWrites)
	failed := w.seed(qNow.Add(-3*time.Hour), 0, "Hi", "Some text.")
	whole(failed, string(inbox.HeldUndecided))
	// Begun, and its settlement lost: removed now, recovered Held later.
	fa.set(1, errWrites)
	lost := w.seed(qNow.Add(-4*time.Hour), 0, "Hi", "Some text.")
	whole(lost, string(inbox.HeldUndecided))
}

// clockAsker advances the clock past a page's soft bound on every ask, and
// answers AskStop for stop.
type clockAsker struct {
	viewAsker
	w    *dWorld
	by   time.Duration
	stop string
	err  error
}

func (a *clockAsker) Ask(ctx context.Context, q *inbox.Question) (*inbox.Asked, error) {
	a.w.clock.add(a.by)
	if a.err != nil {
		return nil, a.err
	}
	if q.MessageID == a.stop {
		return &inbox.Asked{Outcome: inbox.AskStop}, nil
	}
	return a.viewAsker.Ask(ctx, q)
}

func TestU14APageEndsBeforeWhatItCannotAfford(t *testing.T) {
	w := newDWorld(t)
	first := w.seed(qNow.Add(-time.Hour), 0, "One", "Call 555 123 4567.")
	second := w.seed(qNow.Add(-2*time.Hour), 0, "Two", "Call 555 765 4321.")
	disc := inbox.NewDiscloserWith(w.st, query.Eval, w.decA, inbox.MatchBounds{Now: w.clock.now, Soft: time.Minute}, 0)
	// Meaning: the second message's sentences are not asked; the page ends
	// before it, its decision abandoned for the next page.
	slow := &disclosetest.Sentences{P: func(string, int) (float64, bool) { w.clock.add(2 * time.Minute); return 0.01, true }}
	meaning := []rules.Rule{removing("rul_home", topics(topicHome))}
	p := w.page(onSentences(w.askA, slow), disc, with(allParts, meaning), "", 10)
	if len(p.Items) != 1 || p.Items[0].ID() != first || p.Next != first {
		t.Fatalf("meaning: %+v next %q", p.Items, p.Next)
	}
	// The formulas' atoms on what is left: the same.
	about := formula(group(cond(contracts.VarMessage, contracts.OpAbout, "a call")))
	phone := []rules.Rule{removing("rul_phone", shapes(contracts.ShapePhoneNumber))}
	ca := &clockAsker{viewAsker: viewAsker{p: map[string]float64{}}, w: w, by: 2 * time.Minute}
	p = w.page(on(ca), disc, with(allParts, phone, every, about), "", 10)
	if len(p.Items) != 0 || p.Next != first {
		t.Fatalf("reduced: %+v next %q", p.Items, p.Next)
	}
	// An Asker that stops at the second message ends the page before it;
	// at the first, the page goes on with nothing asked of it.
	ca = &clockAsker{viewAsker: viewAsker{p: map[string]float64{}}, w: w, stop: second}
	if p = w.page(on(ca), w.discA, with(allParts, phone, every, about), "", 10); p.Next != first {
		t.Fatalf("stopped: next %q", p.Next)
	}
	ca.stop = first
	if p = w.page(on(ca), w.discA, with(allParts, phone, every, about), "", 1); len(p.Items) != 0 {
		t.Fatalf("the first stopped: %+v", p.Items)
	}
	// An Asker's error is the request's.
	ca = &clockAsker{w: w, err: errors.New("no")}
	if _, err := w.discA.Page(on(ca), w.b, with(allParts, phone, every, about), "", 10); err == nil {
		t.Fatal("an Asker's error was not the page's")
	}
}

func TestU14ATextThatCannotBeReadIsNeverGiven(t *testing.T) {
	w := newDWorld(t)
	q := &qMsg{at: qNow.Add(-time.Hour), from: []string{"no-reply@example.com"}, subject: "Call 555 123 4567", body: strings.Repeat("x ", inbox.SearchTextMax) + "end", rawTooLarge: true}
	id := seedQ(t, w.st, w.b, q)
	rs := []rules.Rule{removing("rul_phone", shapes(contracts.ShapePhoneNumber)), removing("rul_home", topics(topicHome))}
	sa := &disclosetest.Sentences{}
	p := w.page(onSentences(w.askA, sa), w.discA, with(allParts, rs), "", 10)
	if len(p.Items) != 1 || p.Items[0].Disclosed == nil || subjectOf(p.Items[0].Disclosed) != "[removed]" || len(sa.Asked) != 0 {
		t.Fatalf("%+v, asked %d", p.Items, len(sa.Asked))
	}
	if _, err := w.discA.Message(onSentences(w.askA, sa), w.b, with(allParts, rs), id); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("a text a removal could not read was given: %v", err)
	}
}

func TestU16AHoldUndecidedWinsAndAModelOffRemovalApplies(t *testing.T) {
	w := newDWorld(t)
	id := w.seed(qNow.Add(-time.Hour), 0, "Hi", "Call 555 123 4567.")
	health := formula(group(cond(contracts.VarMessage, contracts.OpAbout, "health")))
	w.askA.p = func(string, query.Atom) (float64, bool) { return 0, false } // nothing received
	o, err := w.discA.Message(on(w.askA), w.b, with(allParts, []rules.Rule{holding("rul_h", health), removing("rul_phone", shapes(contracts.ShapePhoneNumber))}), id)
	if err != nil || o.Withheld == nil || o.Withheld.Rule.ID != "rul_h" {
		t.Fatalf("%+v, %v", o, err)
	}
	off := removing("rul_off", shapes(contracts.ShapePhoneNumber), health)
	off.ModelOff = true
	calls := w.askA.modelCalls()
	if got := w.read(on(w.askA), w.discA, with(allParts, []rules.Rule{off}), id); textOf(got) != "Call [removed]." || w.askA.modelCalls() != calls {
		t.Fatalf("%q", textOf(got))
	}
}
