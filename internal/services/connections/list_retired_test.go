package connections

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/store"
)

// V70 and V71 (fourth review of PR #164, finding 1): a write of the list
// that passed its check on an epoch before a rotation removed it stages its
// value after the removal and loses its answer (lateTable, as V65's). No
// read takes the value, and the last removal of the epochs rotations closed,
// RemoveAgainAfter after they were removed, deletes it, as RemoveAgain does
// for an account's removal (ADR 0030, "Changed after the owner's decision
// on old epochs (2026-11-17)").

// lateOnEpoch0 connects accountA through a, brings its list to the edge of
// epoch 0, and has another enclave write it there: that write passes its
// check, then during runs, then its value lands under epoch 0, its answer
// lost. It fails unless the value is stored and no read takes it.
func lateOnEpoch0(t *testing.T, w *world, a *Service, during func(l *ListState, at store.Mark)) {
	t.Helper()
	ctx := context.Background()
	w.connect(a, w.person(accountA), w.ask(claudeCode))
	l, at := toTheEdge(t, a, accountA)
	table := &lateTable{RecordTable: w.mem, target: ListName(accountA, 0)}
	table.before = func() { during(l, at) }
	b, err := New(w.config(&store.Anchored{Records: table, Commits: w.commits, Strict: true, OnlyNewest: true, Now: w.clock.now}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.updateList(ctx, accountA, func(l *ListState) error { return l.Decide("late", w.clock.now()) }); !errors.Is(err, errCrash) {
		t.Fatalf("the late write: %v", err)
	}
	table.mu.Lock()
	landed := table.landed
	table.mu.Unlock()
	if !landed {
		t.Fatal("the late value did not land")
	}
	if epochsStored(w, accountA)[0] == 0 {
		t.Fatal("the late value is not stored")
	}
	if _, _, err := w.enclave().cfg.Records.GetRecordAt(ctx, ListName(accountA, 0)); !errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrLost) {
		t.Fatalf("a read takes the late value: %v", err)
	}
}

// touch writes account's list through s once, unchanged.
func touch(t *testing.T, s *Service, account string) {
	t.Helper()
	if _, err := s.updateList(context.Background(), account, func(*ListState) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

// wholeAt fails unless account's list reads at epoch with its connection.
func wholeAt(t *testing.T, w *world, account string, epoch uint64) {
	t.Helper()
	n, _, err := w.enclave().readList(context.Background(), account)
	if err != nil || n == nil || n.Epoch != epoch || len(n.Members) != 1 || n.Current[claudeCode] == "" {
		t.Fatalf("the list reads %+v, %v", n, err)
	}
}

// V70: one rotation removes epoch 0 and the next write clears its mark;
// then the late value lands. The last removal deletes it, by the sweep or
// the list's next write, once RemoveAgainAfter has passed; a pass whose
// last removal fails fails, and the next finishes it.
func TestV70AValueStagedAfterARotationsRemovalGoesWithTheLastRemoval(t *testing.T) {
	ctx := context.Background()
	for _, by := range []string{"the sweep", "the next write", "a pass after a failed one"} {
		t.Run(by, func(t *testing.T) {
			w := newWorld(t)
			a := w.enclave()
			lateOnEpoch0(t, w, a, func(l *ListState, at store.Mark) {
				if err := a.writeList(ctx, l, at, false); err != nil {
					t.Fatal(err)
				}
				touch(t, a, accountA)
				onlyCurrent(t, w, accountA, 1)
			})
			w.clock.add(RemoveAgainAfter)
			switch by {
			case "the sweep":
				if err := w.enclave().Sweep(ctx); err != nil {
					t.Fatal(err)
				}
			case "the next write":
				touch(t, w.enclave(), accountA)
			case "a pass after a failed one":
				s, f := w.hooked()
				f.set(func(op, name string) error {
					if op == "remove" && name == ListName(accountA, 0) {
						return errCrash
					}
					return nil
				})
				if err := s.Sweep(ctx); err == nil {
					t.Fatal("a pass whose last removal failed succeeded")
				}
				if epochsStored(w, accountA)[0] == 0 {
					t.Fatal("the world does not have a failed last removal")
				}
				f.set(nil)
				if err := s.Sweep(ctx); err != nil {
					t.Fatal(err)
				}
			}
			onlyCurrent(t, w, accountA, 1)
			wholeAt(t, w, accountA, 1)
			// It is done once: a pass after it succeeds and removes nothing more.
			if err := w.enclave().Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			onlyCurrent(t, w, accountA, 1)
		})
	}
}

// V71: several rotations, minutes apart, each epoch removed and its mark
// cleared, all before the late value under epoch 0 lands. The later
// rotations do not forget epoch 0: the last removal of every epoch they
// closed deletes it, after one sweep; another rotation and sweep after it
// leave only the current epoch.
func TestV71SeveralRotationsBeforeTheLateValueLands(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a := w.enclave()
	lateOnEpoch0(t, w, a, func(l *ListState, at store.Mark) {
		if err := a.writeList(ctx, l, at, false); err != nil {
			t.Fatal(err)
		}
		touch(t, a, accountA)
		w.clock.add(15 * time.Minute)
		rotateTo(t, a, accountA, 2)
		touch(t, a, accountA)
		w.clock.add(15 * time.Minute)
		rotateTo(t, a, accountA, 3)
		touch(t, a, accountA)
		onlyCurrent(t, w, accountA, 3)
	})
	w.clock.add(2 * time.Hour)
	if err := w.enclave().Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	onlyCurrent(t, w, accountA, 3)
	wholeAt(t, w, accountA, 3)
	rotateTo(t, a, accountA, 4)
	touch(t, a, accountA)
	w.clock.add(RemoveAgainAfter)
	if err := w.enclave().Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	onlyCurrent(t, w, accountA, 4)
	for _, name := range w.mem.RecordNames() {
		if strings.HasPrefix(name, ListName(accountA, 0)+"@") {
			t.Fatalf("epoch 0 keeps %s", name)
		}
	}
}
