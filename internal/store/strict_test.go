package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/permaily-org/permaily/internal/store"
)

// strictWorld is enclaves over one table and one bucket like S3's, each with
// a store of commits of its own (its own memory of what it read), as two
// enclaves of a deployment and a third that starts later are.
type strictWorld struct {
	b       *recordingBucket
	records *store.Memory
	now     time.Time
}

func newStrictWorld() *strictWorld {
	return &strictWorld{
		b:       &recordingBucket{lockedBucket: &lockedBucket{objectLock: true}},
		records: store.NewMemory(),
		now:     time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC),
	}
}

func (w *strictWorld) commits() *store.S3Commits {
	return &store.S3Commits{
		S3: w.b, Bucket: "anchors", Retention: store.MinCommitLock,
		Key: func(context.Context) ([]byte, error) { return commitKey, nil },
		Now: func() time.Time { return w.now },
	}
}

func (w *strictWorld) enclave() *store.Anchored {
	return &store.Anchored{Records: w.records, Commits: w.commits(), OnlyNewest: true, Strict: true, Now: func() time.Time { return w.now }}
}

// last is the newest object version written to the bucket.
func (w *strictWorld) last() bucketVersion { return w.b.versions[len(w.b.versions)-1] }

// forked asserts that every read and write of name through each enclave
// fails with ErrForked, and that none of them appends anything.
func forked(t *testing.T, w *strictWorld, name string, mark store.Mark, enclaves ...*store.Anchored) {
	t.Helper()
	ctx := context.Background()
	objects := len(w.b.versions)
	for i, e := range enclaves {
		_, err := e.GetRecord(ctx, name)
		_, _, errAt := e.GetRecordAt(ctx, name)
		for what, err := range map[string]error{
			"GetRecord":    err,
			"GetRecordAt":  errAt,
			"PutRecordAt":  e.PutRecordAt(ctx, rec(name, "after the fork"), mark),
			"PutRecord":    e.PutRecord(ctx, rec(name, "after the fork"), false),
			"create":       e.PutRecord(ctx, rec(name, "after the fork"), true),
			"RemoveRecord": e.RemoveRecord(ctx, name),
			"CreateNew":    e.CreateNew(ctx, rec(name, "after the fork")),
		} {
			if !errors.Is(err, store.ErrForked) || errors.Is(err, store.ErrNotFound) {
				t.Fatalf("enclave %d, %s of a forked record: %v", i, what, err)
			}
		}
	}
	if len(w.b.versions) != objects {
		t.Fatalf("%d objects appended to a forked record", len(w.b.versions)-objects)
	}
}

// forks counts the regressions marked fork an enclave recorded.
func forks(e *store.Anchored) int {
	n := 0
	for _, r := range e.Regressions() {
		if r.Fork {
			n++
		}
	}
	return n
}

// S1 (ADR 0026): A and B read a strict record at one version. A writes the
// next version; a delete marker hides A's commit while B's write of the same
// version is under way, and S3 takes B's. Every later read and write of the
// record through A, B and C (started afterwards) fails with ErrForked,
// RemoveRecord and CreateNew included, and A and B each record one
// regression marked fork.
func TestS1AForkMakesAStrictRecordFailOnEveryEnclave(t *testing.T) {
	ctx := context.Background()
	w := newStrictWorld()
	a, b := w.enclave(), w.enclave()
	if err := a.CreateNew(ctx, rec("mailbox-1", "made")); err != nil {
		t.Fatal(err)
	}
	_, first, err := a.GetRecordAt(ctx, "mailbox-1")
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := b.GetRecordAt(ctx, "mailbox-1")
	if err != nil {
		t.Fatal(err)
	}
	// B has read the history (version 2 is free) and is writing when A
	// writes version 2 and whoever writes to the bucket hides A's commit.
	w.b.before = func(key string) {
		if err := a.PutRecordAt(ctx, rec("mailbox-1", "A's change"), first); err != nil {
			t.Fatal(err)
		}
		w.b.hide(key)
	}
	if err := b.PutRecordAt(ctx, rec("mailbox-1", "B's change"), second); err != nil {
		t.Fatalf("S3 takes a write over a delete marker: %v", err)
	}
	c := w.enclave()
	forked(t, w, "mailbox-1", second, a, b, c)
	forked(t, w, "mailbox-1", first, a, b, c) // and again: nothing changes it
	for i, e := range []*store.Anchored{a, b, c} {
		if forks(e) != 1 {
			t.Fatalf("enclave %d recorded %+v", i, e.Regressions())
		}
	}
	for _, r := range a.Regressions() {
		if r.Name != "mailbox-1" || r.Found != 2 || r.Unlocked {
			t.Fatalf("regression %+v", r)
		}
	}
}

// S1 in memory: MemoryCommits.Fork makes the second commit a delete marker
// allows.
func TestS1AForkInMemory(t *testing.T) {
	ctx := context.Background()
	records, commits := store.NewMemory(), store.NewMemoryCommits()
	a := &store.Anchored{Records: records, Commits: commits, OnlyNewest: true, Strict: true}
	if err := a.CreateNew(ctx, rec("mailbox-1", "made")); err != nil {
		t.Fatal(err)
	}
	_, mark, err := a.GetRecordAt(ctx, "mailbox-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.PutRecordAt(ctx, rec("mailbox-1", "changed"), mark); err != nil {
		t.Fatal(err)
	}
	commits.Fork("mailbox-1", 1, sha256.Sum256([]byte("another value")))
	c := &store.Anchored{Records: records, Commits: commits, OnlyNewest: true, Strict: true}
	for i, e := range []*store.Anchored{a, c} {
		if _, err := e.GetRecord(ctx, "mailbox-1"); !errors.Is(err, store.ErrForked) {
			t.Fatalf("enclave %d: %v", i, err)
		}
		if err := e.RemoveRecord(ctx, "mailbox-1"); !errors.Is(err, store.ErrForked) {
			t.Fatalf("enclave %d removes: %v", i, err)
		}
		if forks(e) != 1 {
			t.Fatalf("enclave %d: %+v", i, e.Regressions())
		}
	}
	h, err := commits.History(ctx, "mailbox-1")
	if err != nil || len(h.Forks) != 1 || h.Forks[0].Version != 1 || len(h.Forks[0].Digests) != 2 || h.Head == nil || h.Head.Version != 2 || h.Next != 3 {
		t.Fatalf("history %+v, %v", h, err)
	}
	if d := h.Forks[0].Digests; bytes.Compare(d[0][:], d[1][:]) >= 0 {
		t.Fatalf("digests not in order: %x", d)
	}
}

// S3 (ADR 0026): a delete marker over the newest commit, and nothing
// written: the record reads as before, strict or not.
func TestS3ADeleteMarkerAloneChangesNothing(t *testing.T) {
	ctx := context.Background()
	for _, strict := range []bool{true, false} {
		w := newStrictWorld()
		a := w.enclave()
		a.Strict = strict
		if err := a.PutRecord(ctx, rec("mailbox-1", "one"), true); err != nil {
			t.Fatal(err)
		}
		if err := a.PutRecord(ctx, rec("mailbox-1", "two"), false); err != nil {
			t.Fatal(err)
		}
		w.b.hide(w.last().key)
		c := w.enclave()
		c.Strict = strict
		for i, e := range []*store.Anchored{a, c} {
			got, err := e.GetRecord(ctx, "mailbox-1")
			if err != nil || string(got.Value) != "two" || len(e.Regressions()) != 0 {
				t.Fatalf("strict %v, enclave %d: %+v, %v, %+v", strict, i, got, err, e.Regressions())
			}
		}
	}
}

// S4 (ADR 0026): CreateNew through A and B at once makes the record once:
// one succeeds and the other is told it exists, and what the loser stored is
// not left behind. A name that was removed, or that holds a value from
// before commits existed, is never made again.
func TestS4CreateNewMakesARecordOnce(t *testing.T) {
	ctx := context.Background()
	t.Run("over S3", func(t *testing.T) {
		w := newStrictWorld()
		a, b := w.enclave(), w.enclave()
		var errB error
		// B makes the record while A's commit is on its way.
		w.b.before = func(string) { errB = b.CreateNew(ctx, rec("mailbox-1", "B's")) }
		errA := a.CreateNew(ctx, rec("mailbox-1", "A's"))
		if errB != nil || !errors.Is(errA, store.ErrExists) {
			t.Fatalf("A %v, B %v", errA, errB)
		}
		for _, e := range []*store.Anchored{a, b, w.enclave()} {
			if got, err := e.GetRecord(ctx, "mailbox-1"); err != nil || string(got.Value) != "B's" {
				t.Fatalf("read %+v, %v", got, err)
			}
		}
		if stored := names(t, w.records, "mailbox-1"); len(stored) != 1 {
			t.Fatalf("stored %q", stored)
		}
	})
	t.Run("in memory, at once", func(t *testing.T) {
		for range 20 {
			records, commits := store.NewMemory(), store.NewMemoryCommits()
			var wg sync.WaitGroup
			errs := make([]error, 2)
			for i := range errs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					e := &store.Anchored{Records: records, Commits: commits, OnlyNewest: true, Strict: true}
					errs[i] = e.CreateNew(ctx, rec("mailbox-1", fmt.Sprint("enclave ", i)))
				}()
			}
			wg.Wait()
			if (errs[0] == nil) == (errs[1] == nil) || (!errors.Is(errs[0], store.ErrExists) && !errors.Is(errs[1], store.ErrExists)) {
				t.Fatalf("errors %v", errs)
			}
		}
	})
	t.Run("after a removal, and over a record from before commits", func(t *testing.T) {
		records, commits := store.NewMemory(), store.NewMemoryCommits()
		a := &store.Anchored{Records: records, Commits: commits, OnlyNewest: true, Strict: true}
		if err := a.CreateNew(ctx, rec("mailbox-1", "made")); err != nil {
			t.Fatal(err)
		}
		if err := a.RemoveRecord(ctx, "mailbox-1"); err != nil {
			t.Fatal(err)
		}
		if err := a.CreateNew(ctx, rec("mailbox-1", "again")); !errors.Is(err, store.ErrExists) {
			t.Fatalf("made again after its removal: %v", err)
		}
		records.SetRecord(*rec("mailbox-2", "from before commits"))
		if err := a.CreateNew(ctx, rec("mailbox-2", "new")); !errors.Is(err, store.ErrExists) {
			t.Fatalf("made over a record from before commits: %v", err)
		}
	})
}

// S6 (ADR 0026): a write pending above a fork does not bury it. Every
// version of the history is looked at, so the fork is found whichever order
// the writes land in.
func TestS6AWritePendingAboveAForkDoesNotBuryIt(t *testing.T) {
	ctx := context.Background()
	for _, order := range []string{"B stops after its read", "B's next commit lands first"} {
		t.Run(order, func(t *testing.T) {
			w := newStrictWorld()
			a, b := w.enclave(), w.enclave()
			if err := a.CreateNew(ctx, rec("mailbox-1", "v1")); err != nil {
				t.Fatal(err)
			}
			_, markA, err := a.GetRecordAt(ctx, "mailbox-1")
			if err != nil {
				t.Fatal(err)
			}
			var markB store.Mark
			// A has read the history and is writing version 2 when B commits
			// version 2, reads again, and a delete marker hides B's commit.
			w.b.before = func(key string) {
				_, m, err := b.GetRecordAt(ctx, "mailbox-1")
				if err != nil {
					t.Fatal(err)
				}
				if err := b.PutRecordAt(ctx, rec("mailbox-1", "B's v2"), m); err != nil {
					t.Fatal(err)
				}
				if _, markB, err = b.GetRecordAt(ctx, "mailbox-1"); err != nil {
					t.Fatal(err)
				}
				if order == "B's next commit lands first" {
					if err := b.PutRecordAt(ctx, rec("mailbox-1", "B's v3"), markB); err != nil {
						t.Fatal(err)
					}
				}
				w.b.hide(key)
			}
			if err := a.PutRecordAt(ctx, rec("mailbox-1", "A's v2"), markA); err != nil {
				t.Fatalf("A's second version 2: %v", err)
			}
			if order == "B stops after its read" {
				objects := len(w.b.versions)
				if err := b.PutRecordAt(ctx, rec("mailbox-1", "B's v3"), markB); !errors.Is(err, store.ErrForked) {
					t.Fatalf("B's pending write: %v", err)
				}
				if len(w.b.versions) != objects {
					t.Fatal("B appended above a fork")
				}
			}
			c := w.enclave()
			forked(t, w, "mailbox-1", markB, a, b, c)
			h, err := w.commits().History(ctx, "mailbox-1")
			if err != nil || len(h.Forks) != 1 || h.Forks[0].Version != 2 {
				t.Fatalf("history %+v, %v", h, err)
			}
		})
	}
}

// S6, the case it narrows: a strict writer reads the history again before it
// appends, so it never writes over a commit that a delete marker hides. A
// writer that read only before would take the hidden version for free, and
// make the fork itself.
func TestS6AStrictWriterDoesNotWriteOverAHiddenCommit(t *testing.T) {
	ctx := context.Background()
	w := newStrictWorld()
	a, b := w.enclave(), w.enclave()
	if err := a.CreateNew(ctx, rec("mailbox-1", "v1")); err != nil {
		t.Fatal(err)
	}
	_, markA, err := a.GetRecordAt(ctx, "mailbox-1")
	if err != nil {
		t.Fatal(err)
	}
	_, markB, err := b.GetRecordAt(ctx, "mailbox-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.PutRecordAt(ctx, rec("mailbox-1", "B's v2"), markB); err != nil {
		t.Fatal(err)
	}
	w.b.hide(w.last().key)
	objects, stored := len(w.b.versions), names(t, w.records, "mailbox-1")
	if err := a.PutRecordAt(ctx, rec("mailbox-1", "A's v2"), markA); !errors.Is(err, store.ErrChanged) {
		t.Fatalf("a write over a hidden commit: %v", err)
	}
	if len(w.b.versions) != objects || fmt.Sprint(names(t, w.records, "mailbox-1")) != fmt.Sprint(stored) {
		t.Fatal("something was written for a refused write")
	}
	for _, e := range []*store.Anchored{a, b, w.enclave()} {
		if got, err := e.GetRecord(ctx, "mailbox-1"); err != nil || string(got.Value) != "B's v2" {
			t.Fatalf("read %+v, %v", got, err)
		}
	}
}

// S7 (ADR 0026): a replayed commit. A valid commit's bytes written again at
// its key, once without a lock and once with a lock in governance mode, are
// the same commit: no fork, and nothing unlocked since the original is
// locked. The record goes on taking commits; once the copies are deleted, an
// enclave that starts reads it as it was last written.
func TestS7AReplayedCommitIsNoFork(t *testing.T) {
	ctx := context.Background()
	w := newStrictWorld()
	a := w.enclave()
	if err := a.CreateNew(ctx, rec("mailbox-1", "active")); err != nil {
		t.Fatal(err)
	}
	original := w.last()
	w.b.add(bucketVersion{key: original.key, body: original.body})
	w.b.add(bucketVersion{key: original.key, body: original.body, mode: s3types.ObjectLockModeGovernance, until: w.now.Add(24 * time.Hour)})
	h, err := w.commits().History(ctx, "mailbox-1")
	if err != nil || len(h.Forks) != 0 || len(h.Unlocked) != 0 || h.Head == nil || h.Head.Version != 1 || h.Next != 2 {
		t.Fatalf("history %+v, %v", h, err)
	}
	_, mark, err := a.GetRecordAt(ctx, "mailbox-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.PutRecordAt(ctx, rec("mailbox-1", "disconnected"), mark); err != nil {
		t.Fatal(err)
	}
	// The copies are deleted, as anyone can delete what is not locked.
	var kept []bucketVersion
	for _, v := range w.b.versions {
		if v.key != original.key || v.id == original.id {
			kept = append(kept, v)
		}
	}
	w.b.versions = kept
	c := w.enclave()
	if got, err := c.GetRecord(ctx, "mailbox-1"); err != nil || string(got.Value) != "disconnected" {
		t.Fatalf("read %+v, %v", got, err)
	}
	if len(a.Regressions()) != 0 || len(c.Regressions()) != 0 {
		t.Fatalf("regressions %+v %+v", a.Regressions(), c.Regressions())
	}

	// The same in memory.
	records, commits := store.NewMemory(), store.NewMemoryCommits()
	m := &store.Anchored{Records: records, Commits: commits, Strict: true}
	if err := m.CreateNew(ctx, rec("mailbox-1", "active")); err != nil {
		t.Fatal(err)
	}
	commits.Copy("mailbox-1", 1, false)
	commits.Copy("mailbox-1", 1, true)
	if h, err := commits.History(ctx, "mailbox-1"); err != nil || len(h.Forks) != 0 || len(h.Unlocked) != 0 || h.Next != 2 {
		t.Fatalf("history in memory %+v, %v", h, err)
	}
	if _, err := m.GetRecord(ctx, "mailbox-1"); err != nil {
		t.Fatal(err)
	}
}

// S8 (ADR 0026): CreateNew's precedence. A fork, then any commit (a removal
// only included), a lost value or a value under the plain name: ErrExists;
// nothing at all: made at version 1.
func TestS8CreateNewsPrecedence(t *testing.T) {
	ctx := context.Background()
	records, commits := store.NewMemory(), store.NewMemoryCommits()
	a := &store.Anchored{Records: records, Commits: commits, OnlyNewest: true, Strict: true}

	if err := a.CreateNew(ctx, rec("forked", "one")); err != nil {
		t.Fatal(err)
	}
	commits.Fork("forked", 1, sha256.Sum256([]byte("two")))
	if err := a.CreateNew(ctx, rec("forked", "again")); !errors.Is(err, store.ErrForked) {
		t.Fatalf("a fork: %v", err)
	}

	if err := a.RemoveRecord(ctx, "removed only"); err != nil {
		t.Fatal(err)
	}
	if err := a.CreateNew(ctx, rec("removed only", "made")); !errors.Is(err, store.ErrExists) {
		t.Fatalf("a removal only: %v", err)
	}

	if err := a.CreateNew(ctx, rec("lost", "made")); err != nil {
		t.Fatal(err)
	}
	for _, n := range names(t, records, "lost") {
		_ = records.DeleteRecord(ctx, n)
	}
	if _, err := a.GetRecord(ctx, "lost"); !errors.Is(err, store.ErrLost) {
		t.Fatalf("not lost: %v", err)
	}
	if err := a.CreateNew(ctx, rec("lost", "made again")); !errors.Is(err, store.ErrExists) {
		t.Fatalf("a lost value: %v", err)
	}

	records.SetRecord(*rec("plain", "from before commits"))
	if err := a.CreateNew(ctx, rec("plain", "made")); !errors.Is(err, store.ErrExists) {
		t.Fatalf("a value under the plain name: %v", err)
	}

	if err := a.CreateNew(ctx, rec("new", "made")); err != nil {
		t.Fatal(err)
	}
	if head, next, err := commits.Head(ctx, "new"); err != nil || head == nil || head.Version != 1 || next != 2 {
		t.Fatalf("made at %+v, next %d, %v", head, next, err)
	}

	// Only for strict records: PutRecord(create) stays what the others use.
	loose := &store.Anchored{Records: records, Commits: commits}
	if err := loose.CreateNew(ctx, rec("loose", "made")); err == nil || errors.Is(err, store.ErrExists) {
		t.Fatalf("CreateNew of a record that is not strict: %v", err)
	}
	// A strict record over a store of commits that cannot read a history.
	blind := &store.Anchored{Records: records, Commits: failingCommits{}, Strict: true}
	if _, err := blind.GetRecord(ctx, "new"); err == nil || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a strict read without a history: %v", err)
	}
}

// The order in which a strict method says what is wrong: a fork first, then
// a store of commits that went back, then a mark that is behind.
func TestStrictErrorsComeInOrder(t *testing.T) {
	ctx := context.Background()
	records, commits := store.NewMemory(), store.NewMemoryCommits()
	a := &store.Anchored{Records: records, Commits: commits, Strict: true}
	b := &store.Anchored{Records: records, Commits: commits, Strict: true}
	if err := a.CreateNew(ctx, rec("mailbox-1", "one")); err != nil {
		t.Fatal(err)
	}
	_, old, err := a.GetRecordAt(ctx, "mailbox-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.PutRecord(ctx, rec("mailbox-1", "two"), false); err != nil {
		t.Fatal(err)
	}
	// A mark that is behind: ErrChanged, and nothing written.
	if err := b.PutRecordAt(ctx, rec("mailbox-1", "late"), old); !errors.Is(err, store.ErrChanged) {
		t.Fatalf("a mark behind: %v", err)
	}
	if head, _, _ := commits.Head(ctx, "mailbox-1"); head.Version != 2 {
		t.Fatalf("written at %d", head.Version)
	}
	// The store goes back: ErrRolledBack for whoever saw version 2.
	commits.Forget("mailbox-1")
	if _, err := a.GetRecord(ctx, "mailbox-1"); !errors.Is(err, store.ErrRolledBack) {
		t.Fatalf("gone back: %v", err)
	}
	if err := a.PutRecordAt(ctx, rec("mailbox-1", "late"), old); !errors.Is(err, store.ErrRolledBack) {
		t.Fatalf("gone back, writing: %v", err)
	}
	// And forked as well: ErrForked first.
	commits.Fork("mailbox-1", 1, sha256.Sum256([]byte("other")))
	if _, err := a.GetRecord(ctx, "mailbox-1"); !errors.Is(err, store.ErrForked) || errors.Is(err, store.ErrRolledBack) {
		t.Fatalf("gone back and forked: %v", err)
	}
}

// S9 (ADR 0026): a commit whose only copy has no lock, or a lock shorter than
// MinCommitLock past its time, is counted all the same, and reported once
// per record and enclave (a regression marked unlocked). A locked copy of the
// same digest beside it is enough.
func TestS9AnUnlockedCommitIsCountedAndReported(t *testing.T) {
	ctx := context.Background()
	for _, how := range []struct {
		name  string
		mode  s3types.ObjectLockMode
		until time.Duration
	}{
		{"no lock", "", 0},
		{"governance", s3types.ObjectLockModeGovernance, 48 * time.Hour},
		{"too short", s3types.ObjectLockModeCompliance, store.MinCommitLock - time.Second},
	} {
		t.Run(how.name, func(t *testing.T) {
			w := newStrictWorld()
			a := w.enclave()
			if err := a.CreateNew(ctx, rec("mailbox-1", "made")); err != nil {
				t.Fatal(err)
			}
			v := &w.b.versions[len(w.b.versions)-1]
			v.mode, v.until = how.mode, time.Time{}
			if how.until != 0 {
				v.until = w.now.Add(how.until)
			}
			c := w.enclave()
			for range 3 {
				if got, err := c.GetRecord(ctx, "mailbox-1"); err != nil || string(got.Value) != "made" {
					t.Fatalf("read %+v, %v", got, err)
				}
			}
			if r := c.Regressions(); len(r) != 1 || !r[0].Unlocked || r[0].Fork || r[0].Name != "mailbox-1" {
				t.Fatalf("regressions %+v", r)
			}
			h, err := w.commits().History(ctx, "mailbox-1")
			if err != nil || fmt.Sprint(h.Unlocked) != "[1]" || len(h.Forks) != 0 {
				t.Fatalf("history %+v, %v", h, err)
			}
			// A locked copy of the same commit beside it.
			w.b.add(bucketVersion{key: v.key, body: v.body, mode: s3types.ObjectLockModeCompliance, until: w.now.Add(store.MinCommitLock)})
			fresh := w.enclave()
			if _, err := fresh.GetRecord(ctx, "mailbox-1"); err != nil || len(fresh.Regressions()) != 0 {
				t.Fatalf("with a locked copy: %v, %+v", err, fresh.Regressions())
			}
		})
	}
}

// S10 (ADR 0026): the bounds of a history. More than 10,000 objects is an
// error that is not ErrForked; 600 objects this enclave never read make the
// first read fail after 500 fetches and the next one complete; a record read
// once is read again with one listing and no fetch.
func TestS10TheHistorysBounds(t *testing.T) {
	ctx := context.Background()
	w := newStrictWorld()
	c := w.commits()
	if _, err := c.Append(ctx, commitOf("mailbox-1", 1, "one")); err != nil {
		t.Fatal(err)
	}
	prefix := strings.TrimSuffix(w.last().key, "00000000000000000001")
	for i := range 600 {
		w.b.add(bucketVersion{key: fmt.Sprintf("%s%020d", prefix, 2+i), body: []byte("junk")})
	}
	w.b.log = nil
	if _, err := c.History(ctx, "mailbox-1"); err == nil || errors.Is(err, store.ErrForked) || !strings.Contains(err.Error(), "500") {
		t.Fatalf("first read: %v", err)
	}
	if gets := fetches(w.b.log); gets != store.MaxHistoryReads {
		t.Fatalf("%d fetched", gets)
	}
	w.b.log = nil
	h, err := c.History(ctx, "mailbox-1")
	if err != nil || h.Head == nil || h.Head.Version != 1 || h.Next != 602 {
		t.Fatalf("second read: %+v, %v", h, err)
	}
	if gets := fetches(w.b.log); gets != 101 {
		t.Fatalf("%d fetched the second time", gets)
	}
	w.b.log = nil
	if _, err := c.History(ctx, "mailbox-1"); err != nil || fmt.Sprint(w.b.log) != "[list]" {
		t.Fatalf("read again: %v, %q", err, w.b.log)
	}

	for i := range 10000 {
		w.b.add(bucketVersion{key: fmt.Sprintf("%s%020d", prefix, 1000+i), body: []byte("junk")})
	}
	if _, err := c.History(ctx, "mailbox-1"); err == nil || errors.Is(err, store.ErrForked) || !strings.Contains(err.Error(), "more than 10000 objects") {
		t.Fatalf("too many objects: %v", err)
	}
}

func fetches(log []string) int {
	n := 0
	for _, l := range log {
		if strings.HasPrefix(l, "get ") {
			n++
		}
	}
	return n
}

// What the history says of a record with things that are not commits among
// its objects: the same head and the same next version as Head.
func TestHistoryAgreesWithHead(t *testing.T) {
	ctx := context.Background()
	w := newStrictWorld()
	c := w.commits()
	for v := uint64(1); v <= 3; v++ {
		if _, err := c.Append(ctx, commitOf("tls", v, fmt.Sprint("value ", v))); err != nil {
			t.Fatal(err)
		}
	}
	prefix := strings.TrimSuffix(w.b.versions[0].key, "00000000000000000001")
	for key, body := range map[string]string{
		prefix + "00000000000000000004": "not JSON",
		prefix + "00000000000000000005": `{"name":"tls","version":5,"digest":"00","mac":"00"}`,
		prefix + "00000000000000000007": strings.Repeat("x", 5000),
		prefix + "00000000000000000009": string(w.b.versions[2].body),
		prefix + "latest":               string(w.b.versions[2].body),
		prefix + "00000000000000000000": string(w.b.versions[2].body),
	} {
		w.b.add(bucketVersion{key: key, body: []byte(body)})
	}
	w.b.hide(prefix + "00000000000000000002")
	head, next, err := c.Head(ctx, "tls")
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.History(ctx, "tls")
	if err != nil || h.Head == nil || *h.Head != *head || h.Next != next || next != 6 || len(h.Forks) != 0 || len(h.Unlocked) != 0 {
		t.Fatalf("history %+v, %v; head %+v, next %d", h, err, head, next)
	}
	// An empty record.
	if h, err := c.History(ctx, "nothing"); err != nil || h.Head != nil || h.Next != 1 || h.Forks != nil {
		t.Fatalf("an empty history: %+v, %v", h, err)
	}
}

// Of two commits at one version, Head answers the one with the smaller
// digest, in memory as in the bucket.
func TestHeadOfAForkIsTheSmallerDigest(t *testing.T) {
	ctx := context.Background()
	x, y := commitOf("tls", 1, "x"), commitOf("tls", 1, "y")
	small := x
	if bytes.Compare(y.Digest[:], x.Digest[:]) < 0 {
		small = y
	}
	for _, order := range [][2]store.Commit{{x, y}, {y, x}} {
		w := newStrictWorld()
		c := w.commits()
		if _, err := c.Append(ctx, order[0]); err != nil {
			t.Fatal(err)
		}
		w.b.hide(w.last().key)
		if _, err := c.Append(ctx, order[1]); err != nil {
			t.Fatal(err)
		}
		inBucket, _, err := c.Head(ctx, "tls")
		if err != nil || inBucket.Digest != small.Digest {
			t.Fatalf("in the bucket: %+v, %v", inBucket, err)
		}
		m := store.NewMemoryCommits()
		if _, err := m.Append(ctx, order[0]); err != nil {
			t.Fatal(err)
		}
		m.Fork("tls", 1, order[1].Digest)
		inMemory, next, err := m.Head(ctx, "tls")
		if err != nil || inMemory.Digest != small.Digest || next != 2 {
			t.Fatalf("in memory: %+v, %d, %v", inMemory, next, err)
		}
	}
}

// S3Commits.History fails as Head does when S3 or the key does.
func TestHistoryFailures(t *testing.T) {
	ctx := context.Background()
	down := errors.New("s3 unreachable")
	w := newStrictWorld()
	c := w.commits()
	if _, err := c.Append(ctx, commitOf("tls", 1, "v")); err != nil {
		t.Fatal(err)
	}
	w.b.failList = down
	if _, err := c.History(ctx, "tls"); !errors.Is(err, down) {
		t.Fatalf("list: %v", err)
	}
	w.b.failList, w.b.failGet = nil, down
	if _, err := c.History(ctx, "tls"); !errors.Is(err, down) {
		t.Fatalf("get: %v", err)
	}
	w.b.failGet = nil
	waiting := errors.New("no root secret yet")
	c.Key = func(context.Context) ([]byte, error) { return nil, waiting }
	if _, err := c.History(ctx, "tls"); !errors.Is(err, waiting) {
		t.Fatalf("key: %v", err)
	}
	// A strict record over a history that fails is an error, never "none".
	a := &store.Anchored{Records: store.NewMemory(), Commits: c, Strict: true}
	if _, err := a.GetRecord(ctx, "tls"); !errors.Is(err, waiting) || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("strict read: %v", err)
	}
}

// takingCommits is a store of commits where something that is not a commit
// lands at the version of the next append, once, just before it.
type takingCommits struct {
	*store.MemoryCommits
	taken bool
}

func (c *takingCommits) Append(ctx context.Context, commit store.Commit) (*store.Commit, error) {
	if !c.taken {
		c.taken = true
		c.Take(commit.Name, commit.Version)
	}
	return c.MemoryCommits.Append(ctx, commit)
}

// failingPlain is a table whose read of a record's plain name fails.
type failingPlain struct {
	store.RecordTable
	err error
}

func (f failingPlain) GetRecord(ctx context.Context, name string) (*store.Record, error) {
	if !strings.Contains(name, "@") {
		return nil, f.err
	}
	return f.RecordTable.GetRecord(ctx, name)
}

// CreateNew when something that is not a commit takes its version under it:
// it makes the record after it, and leaves nothing of the first try. When the
// plain name cannot be read, it makes nothing.
func TestCreateNewAroundWhatIsNotACommit(t *testing.T) {
	ctx := context.Background()
	records := store.NewMemory()
	commits := &takingCommits{MemoryCommits: store.NewMemoryCommits()}
	a := &store.Anchored{Records: records, Commits: commits, Strict: true}
	if err := a.CreateNew(ctx, rec("mailbox-1", "made")); err != nil {
		t.Fatal(err)
	}
	if got, err := a.GetRecord(ctx, "mailbox-1"); err != nil || string(got.Value) != "made" {
		t.Fatalf("read %+v, %v", got, err)
	}
	if stored := names(t, records, "mailbox-1"); len(stored) != 1 || !strings.Contains(stored[0], "@00000000000000000002-") {
		t.Fatalf("stored %q", stored)
	}

	down := errors.New("table unreachable")
	b := &store.Anchored{Records: failingPlain{RecordTable: records, err: down}, Commits: store.NewMemoryCommits(), Strict: true}
	if err := b.CreateNew(ctx, rec("mailbox-2", "made")); !errors.Is(err, down) {
		t.Fatalf("a plain name that cannot be read: %v", err)
	}
	if stored := names(t, records, "mailbox-2"); len(stored) != 0 {
		t.Fatalf("stored %q", stored)
	}

	// A copy of a version without a commit adds nothing.
	m := store.NewMemoryCommits()
	m.Copy("nothing", 1, false)
	if h, err := m.History(ctx, "nothing"); err != nil || h.Head != nil || len(h.Unlocked) != 0 {
		t.Fatalf("history %+v, %v", h, err)
	}
}
