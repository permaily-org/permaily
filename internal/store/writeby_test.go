package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/store"
)

// The review of #170, finding 1: a write made with WriteBy stores no value
// past its instant, however late it began, and with WriteWithin too the
// earlier of the two holds; the earliest of two WriteBy holds.
func TestWriteByBoundsTheWriteWheneverItBegins(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	mem := store.NewMemory()
	a := &store.Anchored{Records: mem, Commits: store.NewMemoryCommits(), Strict: true, Now: func() time.Time { return now }}
	write := func(ctx context.Context, name string) error {
		return a.CreateNew(ctx, &store.Record{Name: name, Value: []byte("v")})
	}
	// Its instant passed before the write began: nothing is stored.
	if err := write(store.WriteBy(store.WriteWithin(ctx, 5*time.Second), now.Add(-time.Second)), "late"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := mem.GetRecord(ctx, "late"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a value was stored")
	}
	// An earlier WriteBy is not moved later.
	if err := write(store.WriteBy(store.WriteBy(ctx, now.Add(-time.Second)), now.Add(time.Hour)), "later"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	// The instant ends at the stage, after the reads: ErrTooLate.
	mem.Interrupt = func(_ context.Context, step store.Step, _, _ string) error {
		if step == store.StepRead {
			now = now.Add(time.Minute)
		}
		return nil
	}
	if err := write(store.WriteBy(ctx, now.Add(30*time.Second)), "staged"); !errors.Is(err, store.ErrTooLate) {
		t.Fatal(err)
	}
	mem.Interrupt = nil
	// Within both, and WriteBy alone: made.
	if err := write(store.WriteBy(store.WriteWithin(ctx, 5*time.Second), now.Add(time.Hour)), "within"); err != nil {
		t.Fatal(err)
	}
	if err := write(store.WriteBy(ctx, now.Add(time.Second)), "by"); err != nil {
		t.Fatal(err)
	}
}

// The review of #170, finding 2: CheckHistory reads a record's whole
// history, strict or not, and refuses a fork at any version whichever
// value a read of its newest commit chooses; it needs a store of commits
// that reads histories, and holds no record name in its errors.
func TestCheckHistoryRefusesAForkAnywhere(t *testing.T) {
	ctx := context.Background()
	commits := store.NewMemoryCommits()
	a := &store.Anchored{Records: store.NewMemory(), Commits: commits, OnlyNewest: true}
	const name = "account-acc_x"
	for _, v := range []string{"1", "2", "3"} {
		if err := a.PutRecord(ctx, &store.Record{Name: name, Value: []byte(v)}, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.CheckHistory(ctx, name); err != nil {
		t.Fatal(err)
	}
	commits.Fork(name, 2, sha256.Sum256([]byte("other")))
	err := a.CheckHistory(ctx, name)
	if !errors.Is(err, store.ErrForked) {
		t.Fatal(err)
	}
	if err != nil && strings.Contains(err.Error(), name) {
		t.Fatalf("its error names the record: %v", err)
	}
	// A plain read of the newest commit still passes: the fork is interior.
	if _, err := a.GetRecord(ctx, name); err != nil {
		t.Fatal(err)
	}
	plain := &store.Anchored{Records: store.NewMemory(), Commits: headOnly{commits}}
	if err := plain.CheckHistory(ctx, name); err == nil || errors.Is(err, store.ErrForked) {
		t.Fatal(err)
	}
}

// headOnly is a store of commits that does not read histories.
type headOnly struct{ c store.Commits }

func (h headOnly) Head(ctx context.Context, name string) (*store.Commit, uint64, error) {
	return h.c.Head(ctx, name)
}

func (h headOnly) Append(ctx context.Context, c store.Commit) (*store.Commit, error) {
	return h.c.Append(ctx, c)
}
