package accountrules

import (
	"context"
	"errors"
	"testing"

	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/store"
)

// U30: removing the account removes every epoch of its record (ADR 0040,
// "Removed with the account").

func TestU30RemovingTheAccountRemovesEveryEpoch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := NewTestWorld(t)
	s := w.Enclave()
	save(t, w, s, accountA, "", hold("a", "x"))
	// Epoch 0 rolled over and left with its value (its removal failed), and
	// epoch 1 the newest.
	failRemove := &removeFailing{Records: s.cfg.Records}
	s.cfg.Records = failRemove
	failRemove.fail = errors.New("the table does not answer")
	for range rules.RecordVersionsMax - 1 {
		toggle(t, w, s, accountA)
	}
	failRemove.fail = nil
	if got := w.storedOf(accountA); got["0"] == 0 || got["1"] == 0 {
		t.Fatalf("before the removal: %v", got)
	}
	save(t, w, s, accountB, "", hold("b", "x"))

	if err := s.Remove(ctx, accountA); err != nil {
		t.Fatal(err)
	}
	if got := w.storedOf(accountA); len(got) != 0 {
		t.Fatalf("after the removal, stored: %v", got)
	}
	for e := range uint64(3) {
		_, at, err := w.Enclave().cfg.Records.GetRecordAt(ctx, rules.RecordName(accountA, e))
		if !errors.Is(err, store.ErrNotFound) || at.Version() == 0 {
			t.Fatalf("epoch %d is not removed with its fence: %d, %v", e, at.Version(), err)
		}
	}
	// Removed again: nothing more to do. Another account's are as they were.
	if err := s.Remove(ctx, accountA); err != nil {
		t.Fatal(err)
	}
	if got := w.storedOf(accountB); len(got) != 1 {
		t.Fatalf("another account's record: %v", got)
	}
	// What is left reads as no rules, and no change can make the record again.
	a, err := w.Enclave().Applying(ctx, accountA, claude)
	if err != nil || len(a.Rules) != 1 || a.Rules[0].ID != rules.DefaultID {
		t.Fatalf("after the removal: %+v, %v", a, err)
	}
	if _, err := trySave(w, w.Enclave(), accountA, "", hold("c", "x")); err == nil {
		t.Fatal("a removed account's record made again")
	}

	// A new account starts with none.
	a, err = s.Applying(ctx, "acc_newnewnewnewnewnewnewnewne", claude)
	if err != nil || len(a.Rules) != 1 || a.Version != 0 {
		t.Fatalf("a new account: %+v, %v", a, err)
	}
	// An account that never had rules is fenced too.
	if err := s.Remove(ctx, "acc_neverneverneverneverneverne"); err != nil {
		t.Fatal(err)
	}
	if _, at, err := w.Enclave().cfg.Records.GetRecordAt(ctx, rules.RecordName("acc_neverneverneverneverneverne", 0)); !errors.Is(err, store.ErrNotFound) || at.Version() != 1 {
		t.Fatalf("an account with no rules: %d, %v", at.Version(), err)
	}
}

// A removal that cannot read or remove an epoch fails, and the account's
// removal tries it again.
func TestU30ARemovalThatFailsIsAnError(t *testing.T) {
	ctx := context.Background()
	w := NewTestWorld(t)
	s := w.Enclave()
	save(t, w, s, accountA, "", hold("a", "x"))
	w.Break(t, accountA)
	if err := s.Remove(ctx, accountA); !errors.Is(err, store.ErrForked) {
		t.Fatalf("removed a forked record: %v", err)
	}
	w = NewTestWorld(t)
	s = w.Enclave()
	save(t, w, s, accountA, "", hold("a", "x"))
	f := &removeFailing{Records: s.cfg.Records, fail: errors.New("the table does not answer")}
	s.cfg.Records = f
	if err := s.Remove(ctx, accountA); !errors.Is(err, f.fail) {
		t.Fatalf("a removal that failed: %v", err)
	}
}

type removeFailing struct {
	Records
	fail error
}

func (r *removeFailing) RemoveRecord(ctx context.Context, name string) error {
	if r.fail != nil {
		return r.fail
	}
	return r.Records.RemoveRecord(ctx, name)
}
