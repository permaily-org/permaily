package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/permaily-org/permaily/internal/store"
)

// meanwhile is a store of commits that, after it answers, lets something else
// happen before the answer reaches the enclave: what another read or write of
// the same enclave does when it runs at once and gets there first. What that
// something does through it is answered at once.
type meanwhile struct {
	*store.MemoryCommits
	// afterRead runs after Head or History answered, afterAppend after Append
	// did, while they are set.
	afterRead, afterAppend func()
	busy                   bool
}

func (m *meanwhile) between(then func()) {
	if then == nil || m.busy {
		return
	}
	m.busy = true
	defer func() { m.busy = false }()
	then()
}

func (m *meanwhile) Head(ctx context.Context, name string) (*store.Commit, uint64, error) {
	head, next, err := m.MemoryCommits.Head(ctx, name)
	m.between(m.afterRead)
	return head, next, err
}

func (m *meanwhile) History(ctx context.Context, name string) (*store.History, error) {
	h, err := m.MemoryCommits.History(ctx, name)
	m.between(m.afterRead)
	return h, err
}

func (m *meanwhile) Append(ctx context.Context, c store.Commit) (*store.Commit, error) {
	stored, err := m.MemoryCommits.Append(ctx, c)
	m.between(m.afterAppend)
	return stored, err
}

// overOneStore is enclave a, whose store of commits lets something happen
// between its answers and a, and enclave b over the same table and commits.
func overOneStore(strict, onlyNewest bool) (a, b *store.Anchored, m *meanwhile) {
	records, commits := store.NewMemory(), store.NewMemoryCommits()
	m = &meanwhile{MemoryCommits: commits}
	a = &store.Anchored{Records: records, Commits: m, OnlyNewest: onlyNewest, Strict: strict}
	b = &store.Anchored{Records: records, Commits: commits, OnlyNewest: onlyNewest, Strict: strict}
	return a, b, m
}

// makeRecord stores a new record the way its kind is made.
func makeRecord(ctx context.Context, e *store.Anchored, r *store.Record) error {
	if e.Strict {
		return e.CreateNew(ctx, r)
	}
	return e.PutRecord(ctx, r, true)
}

// Two reads of one record at once on one enclave: the first gets the store's
// answer (no commit yet), then another enclave makes the record and the
// second read sees it, and only then does the first answer reach the enclave.
// That answer is older than what the enclave knows now, but it is not older
// than what it knew when it asked: the store did not go back, and the read is
// not refused as if it had. A store that does go back still is.
func TestReadsAtOnceAreNoRollback(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(map[bool]string{false: "not strict", true: "strict"}[strict], func(t *testing.T) {
			ctx := context.Background()
			a, b, m := overOneStore(strict, true)
			m.afterRead = func() {
				m.afterRead = nil
				if err := makeRecord(ctx, b, rec("mailbox-1", "made")); err != nil {
					t.Fatal(err)
				}
				if _, err := a.GetRecord(ctx, "mailbox-1"); err != nil {
					t.Fatalf("the read that got there first: %v", err)
				}
			}
			got, _, err := a.GetRecordAt(ctx, "mailbox-1")
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("a read that started before the record was made: %v", err)
			}
			if err == nil && string(got.Value) != "made" {
				t.Fatalf("read %q", got.Value)
			}
			if r := a.Regressions(); len(r) != 0 {
				t.Fatalf("regressions %+v", r)
			}
			if seen := a.Seen(); len(seen) != 1 || seen[0].Version != 1 {
				t.Fatalf("seen %+v", seen)
			}
			// An earlier copy is still refused: the store forgets version 1.
			m.Forget("mailbox-1")
			if _, err := a.GetRecord(ctx, "mailbox-1"); !errors.Is(err, store.ErrRolledBack) {
				t.Fatalf("after the store went back: %v", err)
			}
		})
	}
}

// A store that goes back while a read is under way, from what the enclave
// knew before it asked, is refused at once, whatever happens meanwhile.
func TestAReadUnderWayStillNoticesTheStoreGoingBack(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(map[bool]string{false: "not strict", true: "strict"}[strict], func(t *testing.T) {
			ctx := context.Background()
			a, b, m := overOneStore(strict, true)
			if err := makeRecord(ctx, a, rec("mailbox-1", "made")); err != nil {
				t.Fatal(err)
			}
			if _, err := a.GetRecord(ctx, "mailbox-1"); err != nil {
				t.Fatal(err)
			}
			m.Forget("mailbox-1")
			m.afterRead = func() {
				m.afterRead = nil
				// Another enclave writes meanwhile; this one knows nothing of it.
				if err := makeRecord(ctx, b, rec("mailbox-2", "other")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.GetRecord(ctx, "mailbox-1"); !errors.Is(err, store.ErrRolledBack) {
				t.Fatalf("got %v", err)
			}
			if r := a.Regressions(); len(r) != 1 || r[0].Had != 1 || r[0].Found != 0 {
				t.Fatalf("regressions %+v", r)
			}
		})
	}
}

// A read that keeps being overtaken by newer commits gives up, as reads and
// writes do when a record keeps changing, and does not call it a rollback.
func TestAReadAlwaysOvertakenKeepsChanging(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(map[bool]string{false: "not strict", true: "strict"}[strict], func(t *testing.T) {
			ctx := context.Background()
			a, b, m := overOneStore(strict, true)
			if err := makeRecord(ctx, b, rec("mailbox-1", "made")); err != nil {
				t.Fatal(err)
			}
			n := 0
			m.afterRead = func() {
				n++
				_, mark, err := b.GetRecordAt(ctx, "mailbox-1")
				if err != nil {
					t.Fatal(err)
				}
				if err := b.PutRecordAt(ctx, rec("mailbox-1", "changed"), mark); err != nil {
					t.Fatal(err)
				}
				if _, err := a.GetRecord(ctx, "mailbox-1"); err != nil {
					t.Fatalf("the read that got there first: %v", err)
				}
			}
			_, err := a.GetRecord(ctx, "mailbox-1")
			if err == nil || errors.Is(err, store.ErrRolledBack) || errors.Is(err, store.ErrNotFound) {
				t.Fatalf("got %v", err)
			}
			if r := a.Regressions(); len(r) != 0 {
				t.Fatalf("regressions %+v after %d reads", r, n)
			}
		})
	}
}

// A write's commit is appended, then another enclave commits the next
// version and a read of this one sees it, before the write remembers its own
// commit. The enclave goes on knowing the newer one: if the store forgets it,
// the earlier copy is refused, not read.
func TestAWriteDoesNotForgetANewerCommitSeenMeanwhile(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(map[bool]string{false: "not strict", true: "strict"}[strict], func(t *testing.T) {
			ctx := context.Background()
			a, b, m := overOneStore(strict, false)
			if err := makeRecord(ctx, a, rec("mailbox-1", "one")); err != nil {
				t.Fatal(err)
			}
			_, mark, err := a.GetRecordAt(ctx, "mailbox-1")
			if err != nil {
				t.Fatal(err)
			}
			m.afterAppend = func() {
				m.afterAppend = nil
				_, bMark, err := b.GetRecordAt(ctx, "mailbox-1")
				if err != nil {
					t.Fatal(err)
				}
				if err := b.PutRecordAt(ctx, rec("mailbox-1", "three"), bMark); err != nil {
					t.Fatal(err)
				}
				if got, err := a.GetRecord(ctx, "mailbox-1"); err != nil || string(got.Value) != "three" {
					t.Fatalf("the read meanwhile: %v", err)
				}
			}
			if err := a.PutRecordAt(ctx, rec("mailbox-1", "two"), mark); err != nil {
				t.Fatal(err)
			}
			if seen := a.Seen(); len(seen) != 1 || seen[0].Version != 3 {
				t.Fatalf("seen %+v", seen)
			}
			m.Forget("mailbox-1")
			if got, err := a.GetRecord(ctx, "mailbox-1"); !errors.Is(err, store.ErrRolledBack) {
				t.Fatalf("read %+v, %v after the store went back", got, err)
			}
		})
	}
}
