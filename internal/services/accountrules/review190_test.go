package accountrules

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/store"
)

// The review of pull request #190: finding 1 (a removal whose commit landed
// and whose deletes failed, or whose epoch had a value staged after it, is
// finished by the next pass, across a restart) and finding 2 (a cold read
// reaches the current epoch however many epochs came before it).

// failDeletes fails the deletion of every value already stored under epoch e
// of account's record while it is on: the removal's commit lands, its
// deletes fail.
func failDeletes(w *TestWorld, account string, e uint64) *atomic.Bool {
	on := &atomic.Bool{}
	on.Store(true)
	prefix := "record#" + rules.RecordName(account, e) + "@"
	w.Mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
		if !on.Load() || step != store.StepRecord || !strings.HasPrefix(key, prefix) {
			return nil
		}
		for _, name := range w.Mem.RecordNames() {
			if "record#"+name == key {
				return errors.New("the table does not answer")
			}
		}
		return nil
	}
	return on
}

// stage stores a value under epoch e of account's record as a write under
// way at its removal would have staged it after the removal.
func stage(t *testing.T, w *TestWorld, account string, e uint64) {
	t.Helper()
	name := rules.RecordName(account, e) + "@late"
	if err := w.Mem.PutRecord(context.Background(), &store.Record{Name: name, Value: []byte("sealed"), UpdatedAt: w.Clock.now()}, true); err != nil {
		t.Fatal(err)
	}
}

func TestReview190F1AnAccountRemovalRetriedFinishesAnEpochWhoseDeletesFailed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := NewTestWorld(t)
	save(t, w, w.Enclave(), accountA, "", hold("a", "x"))
	on := failDeletes(w, accountA, 0)
	if err := w.Enclave().Remove(ctx, accountA); err == nil {
		t.Fatal("a removal whose deletes failed succeeded")
	}
	on.Store(false)
	if err := w.Enclave().Remove(ctx, accountA); err != nil {
		t.Fatal(err)
	}
	if got := w.storedOf(accountA); len(got) != 0 {
		t.Fatalf("after the removal was tried again, stored: %v", got)
	}
	// A value staged after the removal, under an epoch or the fence, goes
	// with the next pass (accounts calls Remove again RemoveAgainAfter later).
	stage(t, w, accountA, 0)
	stage(t, w, accountA, 1)
	if err := w.Enclave().Remove(ctx, accountA); err != nil {
		t.Fatal(err)
	}
	if got := w.storedOf(accountA); len(got) != 0 {
		t.Fatalf("after the removal's last pass, stored: %v", got)
	}
}

func TestReview190F1ARolloverWhoseDeletesFailedIsFinishedAfterARestart(t *testing.T) {
	t.Parallel()
	w := NewTestWorld(t)
	s := w.Enclave()
	save(t, w, s, accountA, "", hold("a", "x"))
	on := failDeletes(w, accountA, 0)
	for range rules.RecordVersionsMax - 1 {
		toggle(t, w, s, accountA)
	}
	if got := w.storedOf(accountA); got["0"] == 0 || got["1"] == 0 {
		t.Fatalf("before the restart, stored: %v", got)
	}
	on.Store(false)
	toggle(t, w, w.Enclave(), accountA)
	if got := w.storedOf(accountA); len(got) != 1 || got["1"] == 0 {
		t.Fatalf("after a write past the restart, stored: %v", got)
	}
}

func TestReview190F1AValueStagedAfterAnEpochsRemovalGoesWithItsLastRemoval(t *testing.T) {
	t.Parallel()
	w := NewTestWorld(t)
	s := w.Enclave()
	save(t, w, s, accountA, "", hold("a", "x"))
	for range rules.RecordVersionsMax - 1 {
		toggle(t, w, s, accountA)
	}
	stage(t, w, accountA, 0)
	// Before RemoveAgainAfter, a write leaves it; after, a write removes it.
	toggle(t, w, s, accountA)
	if got := w.storedOf(accountA); got["0"] != 1 {
		t.Fatalf("before its last removal, stored: %v", got)
	}
	w.Clock.Move(RemoveAgainAfter)
	toggle(t, w, s, accountA)
	if got := w.storedOf(accountA); len(got) != 1 || got["1"] == 0 {
		t.Fatalf("after its last removal, stored: %v", got)
	}
	// The next rollover removes the epoch it closes, and once more the one
	// before it, whatever this enclave remembers.
	stage(t, w, accountA, 0)
	next := w.Enclave()
	for range rules.RecordVersionsMax - 1 {
		toggle(t, w, next, accountA)
	}
	if got := w.storedOf(accountA); len(got) != 1 || got["2"] == 0 {
		t.Fatalf("after the next rollover, stored: %v", got)
	}
}

// epochs makes epochs 0 to last of account's record as a rollover leaves
// them: each written Next and removed but the last.
func epochs(t *testing.T, w *TestWorld, account string, last uint64) {
	t.Helper()
	ctx := context.Background()
	s := w.Enclave()
	for e := range last + 1 {
		rec := &rules.Record{V: rules.RecordFormat, Account: account, Epoch: e, Next: e < last, Rules: []rules.Stored{}}
		if err := s.seal(ctx, rec, store.Mark{}, true); err != nil {
			t.Fatal(err)
		}
		if e < last {
			if err := s.cfg.Records.RemoveRecord(ctx, rules.RecordName(account, e)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestReview190F2AColdReadReachesTheCurrentEpochPastItsVersionsBound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, last := range []uint64{rules.RecordVersionsMax, 3 * rules.RecordVersionsMax} {
		w := NewTestWorld(t)
		epochs(t, w, accountA, last)
		reads := &atomic.Int64{}
		s, h := w.enclave()
		h.afterRead = func(string) { reads.Add(1) }
		a, err := s.Applying(ctx, accountA, claude)
		if err != nil || a.Epoch != last || a.Version != 1 {
			t.Fatalf("a cold read of epoch %d: %+v, %v", last, a, err)
		}
		if n := reads.Load(); n > 64 {
			t.Fatalf("a cold read of epoch %d read %d epochs", last, n)
		}
		// Its changes go on there.
		toggle(t, w, s, accountA)
		if a, err := w.Enclave().Applying(ctx, accountA, claude); err != nil || a.Epoch != last || a.Version != 2 {
			t.Fatalf("after a change: %+v, %v", a, err)
		}
	}
}

// A search for the newest epoch that cannot read one of the epochs it
// probes (the first, one while doubling, one while halving) answers
// ErrUnavailable, never an earlier epoch or no rules.
func TestReview190F2ASearchThatCannotReadAnEpochIsUnavailable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := NewTestWorld(t)
	epochs(t, w, accountA, 5)
	for _, e := range []uint64{1, 4, 6} {
		s, h := w.enclave()
		h.failRead = func(name string) error {
			if name == rules.RecordName(accountA, e) {
				return errors.New("the table does not answer")
			}
			return nil
		}
		unavailable(t, s, accountA)
	}
	if a, err := w.Enclave().Applying(ctx, accountA, claude); err != nil || a.Epoch != 5 {
		t.Fatalf("read again: %+v, %v", a, err)
	}
}
