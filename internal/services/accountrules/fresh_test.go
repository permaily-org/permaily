package accountrules

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// U4: freshness through two enclaves on strict records (ADR 0040, "The
// rules' record", "Changes, and what they apply to").

func TestU4ARuleSavedThroughAnEnclaveIsAppliedByItsNextRequest(t *testing.T) {
	w := NewTestWorld(t)
	a := w.Enclave()
	if _, err := a.Applying(context.Background(), accountA, claude); err != nil {
		t.Fatal(err)
	}
	r := save(t, w, a, accountA, "", hold("a", "statement"))
	got, err := a.Applying(context.Background(), accountA, claude)
	if err != nil || !slices.Contains(ids(got), r.ID) {
		t.Fatalf("the enclave that saved it: %v, %v", ids(got), err)
	}
}

func TestU4ARuleSavedThroughOneEnclaveIsAppliedByTheOtherWithinFresh(t *testing.T) {
	ctx := context.Background()
	w := NewTestWorld(t)
	a, b := w.Enclave(), w.Enclave()
	if _, err := b.Applying(ctx, accountA, claude); err != nil {
		t.Fatal(err)
	}
	w.Clock.Move(time.Second)
	r := save(t, w, a, accountA, "", hold("a", "statement"))
	w.Clock.Move(Fresh - time.Second - time.Nanosecond)
	if got, _ := b.Applying(ctx, accountA, claude); slices.Contains(ids(got), r.ID) {
		t.Fatal("the other enclave read again before its cache was Fresh old")
	}
	w.Clock.Move(time.Nanosecond)
	if got, err := b.Applying(ctx, accountA, claude); err != nil || !slices.Contains(ids(got), r.ID) {
		t.Fatalf("Fresh after its read, the other enclave: %v, %v", ids(got), err)
	}
}

// A read that began before a save and answered after it is as old as when
// it began: Fresh after it began, the enclave reads again and applies the
// rule saved meanwhile.
func TestU4AReadDelayedAcrossASaveIsAsOldAsWhenItBegan(t *testing.T) {
	ctx := context.Background()
	w := NewTestWorld(t)
	a := w.Enclave()
	b, hb := w.enclave()
	began := w.Clock.now()
	var saved string
	hb.afterRead = func(string) {
		hb.afterRead = nil
		saved = save(t, w, a, accountA, "", hold("a", "statement")).ID
		w.Clock.Move(Fresh - time.Second)
	}
	got, err := b.Applying(ctx, accountA, claude)
	if err != nil || slices.Contains(ids(got), saved) {
		t.Fatalf("the delayed read: %v, %v", ids(got), err)
	}
	if got, _ := b.Applying(ctx, accountA, claude); slices.Contains(ids(got), saved) {
		t.Fatal("read again before Fresh")
	}
	w.Clock.Move(began.Add(Fresh).Sub(w.Clock.now()))
	if got, err := b.Applying(ctx, accountA, claude); err != nil || !slices.Contains(ids(got), saved) {
		t.Fatalf("Fresh after the delayed read began: %v, %v", ids(got), err)
	}
}

// A read that began before a change made through this enclave and answered
// after it never replaces the change in the cache.
func TestU4AnOlderReadNeverReplacesAChangeMadeHere(t *testing.T) {
	ctx := context.Background()
	w := NewTestWorld(t)
	a, ha := w.enclave()
	var saved string
	ha.afterRead = func(string) {
		ha.afterRead = nil
		saved = save(t, w, a, accountA, "", hold("a", "statement")).ID
	}
	if _, err := a.Applying(ctx, accountA, claude); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Applying(ctx, accountA, claude); err != nil || !slices.Contains(ids(got), saved) {
		t.Fatalf("an older read replaced the change: %v, %v", ids(got), err)
	}
}

// A refresh that fails never makes a cached read younger: past Fresh the
// account gives nothing until a read succeeds.
func TestU4AFailedRefreshGivesNothingPastFresh(t *testing.T) {
	ctx := context.Background()
	w := NewTestWorld(t)
	b, hb := w.enclave()
	save(t, w, w.Enclave(), accountA, "", hold("a", "statement"))
	if _, err := b.Applying(ctx, accountA, claude); err != nil {
		t.Fatal(err)
	}
	w.Clock.Move(Fresh)
	failing := errors.New("the table does not answer")
	hb.failRead = func(string) error { return failing }
	for range 3 {
		if got, err := b.Applying(ctx, accountA, claude); !errors.Is(err, ErrUnavailable) || !errors.Is(err, failing) || got.Rules != nil {
			t.Fatalf("past Fresh with a failing read: %v, %v", ids(got), err)
		}
		w.Clock.Move(time.Second)
	}
	hb.failRead = nil
	if got, err := b.Applying(ctx, accountA, claude); err != nil || len(got.Rules) != 2 {
		t.Fatalf("once a read succeeds: %v, %v", ids(got), err)
	}
}

// A request that began before a save finishes under the rules it read,
// which its receipt names by the record's version; the next names the new.
func TestU4ARequestBegunBeforeASaveKeepsItsRules(t *testing.T) {
	ctx := context.Background()
	w := NewTestWorld(t)
	a := w.Enclave()
	first := save(t, w, a, accountA, "", hold("a", "statement"))
	before, err := a.Applying(ctx, accountA, claude)
	if err != nil {
		t.Fatal(err)
	}
	second := save(t, w, a, accountA, "", hold("b", "invoice"))
	after, err := a.Applying(ctx, accountA, claude)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ids(before), first.ID) || slices.Contains(ids(before), second.ID) || before.Version != 1 {
		t.Fatalf("the request begun before: %v at %d", ids(before), before.Version)
	}
	if !slices.Contains(ids(after), second.ID) || after.Version != 2 {
		t.Fatalf("the request begun after: %v at %d", ids(after), after.Version)
	}
}

// A clock that went back never makes a cached read fresh for longer.
func TestU4AClockThatWentBackReadsAgain(t *testing.T) {
	ctx := context.Background()
	w := NewTestWorld(t)
	b := w.Enclave()
	if _, err := b.Applying(ctx, accountA, claude); err != nil {
		t.Fatal(err)
	}
	r := save(t, w, w.Enclave(), accountA, "", hold("a", "statement"))
	w.Clock.Move(-time.Second)
	if got, err := b.Applying(ctx, accountA, claude); err != nil || !slices.Contains(ids(got), r.ID) {
		t.Fatalf("with the clock back: %v, %v", ids(got), err)
	}
}
