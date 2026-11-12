package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/store"
)

// twoEnclaves returns two Anchored over the same storage and the same store
// of commits, as two enclaves of one deployment have them.
func twoEnclaves(onlyNewest bool) (a, b *store.Anchored, records *store.Memory, commits *store.MemoryCommits) {
	records, commits = store.NewMemory(), store.NewMemoryCommits()
	a = &store.Anchored{Records: records, Commits: commits, OnlyNewest: onlyNewest}
	b = &store.Anchored{Records: records, Commits: commits, OnlyNewest: onlyNewest}
	return a, b, records, commits
}

// value reads a record through an enclave that was just started.
func value(t *testing.T, records *store.Memory, commits store.Commits, name string) string {
	t.Helper()
	fresh := &store.Anchored{Records: records, Commits: commits}
	got, err := fresh.GetRecord(context.Background(), name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(got.Value)
}

// A change is a read, an edit and a write. Of two enclaves that read the same
// version, one writes; the other is told the record changed, and nothing of
// what it tried is the newest version or stays in the storage.
func TestAnchoredWritesOnlyWhatWasNotChanged(t *testing.T) {
	ctx := context.Background()
	for _, onlyNewest := range []bool{true, false} {
		a, b, records, commits := twoEnclaves(onlyNewest)
		if err := a.PutRecord(ctx, rec("account", "one"), true); err != nil {
			t.Fatal(err)
		}
		_, atA, err := a.GetRecordAt(ctx, "account")
		if err != nil {
			t.Fatal(err)
		}
		got, atB, err := b.GetRecordAt(ctx, "account")
		if err != nil || string(got.Value) != "one" || got.Name != "account" {
			t.Fatalf("only newest %v: read %+v, %v", onlyNewest, got, err)
		}
		if err := a.PutRecordAt(ctx, rec("account", "one, and a passkey"), atA); err != nil {
			t.Fatalf("only newest %v: the first write: %v", onlyNewest, err)
		}
		before := names(t, records, "account")
		if err := b.PutRecordAt(ctx, rec("account", "one, renamed"), atB); !errors.Is(err, store.ErrChanged) {
			t.Fatalf("only newest %v: a write over a change it did not read: %v", onlyNewest, err)
		}
		if got := value(t, records, commits, "account"); got != "one, and a passkey" {
			t.Fatalf("only newest %v: the record is %q", onlyNewest, got)
		}
		if head, next, _ := commits.Head(ctx, "account"); head.Version != 2 || next != 3 {
			t.Fatalf("only newest %v: head %+v, next %d", onlyNewest, head, next)
		}
		if after := names(t, records, "account"); strings.Join(after, " ") != strings.Join(before, " ") {
			t.Fatalf("only newest %v: the refused write left %q where there was %q", onlyNewest, after, before)
		}
		// The mark it read at is no good a second time either.
		if err := b.PutRecordAt(ctx, rec("account", "one, renamed"), atB); !errors.Is(err, store.ErrChanged) {
			t.Fatalf("only newest %v: the same mark again: %v", onlyNewest, err)
		}

		// It reads again, makes its change to what is there now, and both stay.
		got, atB, err = b.GetRecordAt(ctx, "account")
		if err != nil || string(got.Value) != "one, and a passkey" {
			t.Fatalf("only newest %v: read again %+v, %v", onlyNewest, got, err)
		}
		if err := b.PutRecordAt(ctx, rec("account", string(got.Value)+", renamed"), atB); err != nil {
			t.Fatal(err)
		}
		if got := value(t, records, commits, "account"); got != "one, and a passkey, renamed" {
			t.Fatalf("only newest %v: the record is %q", onlyNewest, got)
		}
		want := 3
		if onlyNewest {
			want = 1 // the versions before went as each was replaced
		}
		if stored := names(t, records, "account"); len(stored) != want || !strings.HasPrefix(stored[want-1], "account@00000000000000000003-") {
			t.Fatalf("only newest %v: stored %q", onlyNewest, stored)
		}
	}
}

// change is what a caller does with the two: read, edit, write, and again
// when the record changed in between. between runs after each read.
func change(t *testing.T, a *store.Anchored, name, add string, between func()) int {
	t.Helper()
	ctx := context.Background()
	for try := 1; try <= 10; try++ {
		got, at, err := a.GetRecordAt(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if between != nil {
			between()
		}
		err = a.PutRecordAt(ctx, rec(name, string(got.Value)+add), at)
		if err == nil {
			return try
		}
		if !errors.Is(err, store.ErrChanged) {
			t.Fatal(err)
		}
	}
	t.Fatal("the record never stopped changing")
	return 0
}

// Two enclaves that keep changing one record, each between the other's read
// and write: every change of both is in the record at the end.
func TestAnchoredKeepsTheChangesOfTwoEnclaves(t *testing.T) {
	ctx := context.Background()
	a, b, records, commits := twoEnclaves(true)
	if err := a.PutRecord(ctx, rec("account", ""), true); err != nil {
		t.Fatal(err)
	}
	for _, letter := range []string{"a", "b", "c", "d"} {
		theirs, done := strings.ToUpper(letter), false
		tries := change(t, a, "account", letter, func() {
			if !done {
				done = true
				// The other enclave is itself overtaken once by a third change.
				inner := false
				change(t, b, "account", theirs, func() {
					if !inner {
						inner = true
						change(t, a, "account", ".", nil)
					}
				})
			}
		})
		if tries != 2 {
			t.Fatalf("%s took %d tries", letter, tries)
		}
	}
	if got := value(t, records, commits, "account"); got != ".Aa.Bb.Cc.Dd" {
		t.Fatalf("the record is %q", got)
	}
	if head, _, _ := commits.Head(ctx, "account"); head.Version != 13 {
		t.Fatalf("head %+v", head)
	}
	if stored := names(t, records, "account"); len(stored) != 1 {
		t.Fatalf("stored %q", stored)
	}
}

// A removal is a change: what was read before it is not written after it,
// and the record does not come back.
func TestAnchoredDoesNotWriteOverARemoval(t *testing.T) {
	ctx := context.Background()
	for _, onlyNewest := range []bool{true, false} {
		a, b, records, commits := twoEnclaves(onlyNewest)
		for _, v := range []string{"one", "two"} {
			if err := a.PutRecord(ctx, rec("account", v), false); err != nil {
				t.Fatal(err)
			}
		}
		_, at, err := a.GetRecordAt(ctx, "account")
		if err != nil {
			t.Fatal(err)
		}
		if err := b.RemoveRecord(ctx, "account"); err != nil {
			t.Fatal(err)
		}
		if err := a.PutRecordAt(ctx, rec("account", "two, renamed"), at); !errors.Is(err, store.ErrChanged) {
			t.Fatalf("only newest %v: a write over a removal: %v", onlyNewest, err)
		}
		for _, enclave := range []*store.Anchored{a, b, {Records: records, Commits: commits}} {
			if _, err := enclave.GetRecord(ctx, "account"); !errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrLost) {
				t.Fatalf("only newest %v: the removed record reads %v", onlyNewest, err)
			}
		}
		if stored := names(t, records, "account"); len(stored) != 0 {
			t.Fatalf("only newest %v: left of a removed record: %q", onlyNewest, stored)
		}
		if head, _, _ := commits.Head(ctx, "account"); !head.Gone() || head.Version != 3 {
			t.Fatalf("only newest %v: head %+v", onlyNewest, head)
		}

		// Whoever reads that it is gone can make it again, as a later version,
		// and of two that do, one does.
		_, atA, err := a.GetRecordAt(ctx, "account")
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("got %v", err)
		}
		_, atB, err := b.GetRecordAt(ctx, "account")
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("got %v", err)
		}
		if err := b.PutRecordAt(ctx, rec("account", "theirs"), atB); err != nil {
			t.Fatal(err)
		}
		if err := a.PutRecordAt(ctx, rec("account", "mine"), atA); !errors.Is(err, store.ErrChanged) {
			t.Fatalf("only newest %v: made twice: %v", onlyNewest, err)
		}
		if got := value(t, records, commits, "account"); got != "theirs" {
			t.Fatalf("only newest %v: the record is %q", onlyNewest, got)
		}
		if stored := names(t, records, "account"); len(stored) != 1 || !strings.HasPrefix(stored[0], "account@00000000000000000004-") {
			t.Fatalf("only newest %v: stored %q", onlyNewest, stored)
		}
	}
}

// A record that was never stored is made by one of two enclaves that both
// read that it is not there.
func TestAnchoredMakesARecordOnce(t *testing.T) {
	ctx := context.Background()
	a, b, records, commits := twoEnclaves(true)
	_, atA, err := a.GetRecordAt(ctx, "account")
	if !errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrLost) {
		t.Fatalf("got %v", err)
	}
	_, atB, err := b.GetRecordAt(ctx, "account")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if err := a.PutRecordAt(ctx, rec("account", "mine"), atA); err != nil {
		t.Fatal(err)
	}
	if err := b.PutRecordAt(ctx, rec("account", "theirs"), atB); !errors.Is(err, store.ErrChanged) {
		t.Fatalf("got %v", err)
	}
	if got := value(t, records, commits, "account"); got != "mine" {
		t.Fatalf("the record is %q", got)
	}
	if stored := names(t, records, "account"); len(stored) != 1 {
		t.Fatalf("stored %q", stored)
	}

	// A record whose newest value is gone is lost, and has a mark all the same.
	_ = records.DeleteRecord(ctx, names(t, records, "account")[0])
	_, at, err := b.GetRecordAt(ctx, "account")
	if !errors.Is(err, store.ErrLost) {
		t.Fatalf("got %v", err)
	}
	if err := b.PutRecordAt(ctx, rec("account", "made again"), at); err != nil {
		t.Fatal(err)
	}
	if got := value(t, records, commits, "account"); got != "made again" {
		t.Fatalf("the record is %q", got)
	}
}

// Two enclaves that store the very same value at the same version store it
// under the same name. The one that is refused must not delete it: it is the
// value of the commit that was made.
func TestAnchoredKeepsAValueTheOtherEnclaveCommitted(t *testing.T) {
	ctx := context.Background()
	for _, onlyNewest := range []bool{true, false} {
		a, b, records, commits := twoEnclaves(onlyNewest)
		if err := a.PutRecord(ctx, rec("account", "one"), true); err != nil {
			t.Fatal(err)
		}
		_, atA, _ := a.GetRecordAt(ctx, "account")
		_, atB, _ := b.GetRecordAt(ctx, "account")
		if err := a.PutRecordAt(ctx, rec("account", "the same"), atA); err != nil {
			t.Fatal(err)
		}
		if err := b.PutRecordAt(ctx, rec("account", "the same"), atB); !errors.Is(err, store.ErrChanged) {
			t.Fatalf("got %v", err)
		}
		if got := value(t, records, commits, "account"); got != "the same" {
			t.Fatalf("only newest %v: the record is %q", onlyNewest, got)
		}
		// And when the record moved on since: where every version is kept, that
		// value stays, because the commit of its version may name it.
		if err := a.PutRecord(ctx, rec("account", "three"), false); err != nil {
			t.Fatal(err)
		}
		if err := b.PutRecordAt(ctx, rec("account", "the same"), atB); !errors.Is(err, store.ErrChanged) {
			t.Fatalf("got %v", err)
		}
		kept := 0
		for _, n := range names(t, records, "account") {
			if strings.HasPrefix(n, "account@00000000000000000002-") {
				kept++
			}
		}
		if want := map[bool]int{true: 0, false: 1}[onlyNewest]; kept != want {
			t.Fatalf("only newest %v: %d values of version 2 stored, want %d: %q", onlyNewest, kept, want, names(t, records, "account"))
		}
		if got := value(t, records, commits, "account"); got != "three" {
			t.Fatalf("only newest %v: the record is %q", onlyNewest, got)
		}
	}
}

// PutRecord writes whatever the record is, and counts as a change for whoever
// read before it.
func TestAnchoredUnconditionalWritesStillWork(t *testing.T) {
	ctx := context.Background()
	a, b, records, commits := twoEnclaves(true)
	if err := a.PutRecord(ctx, rec("tls", "one"), true); err != nil {
		t.Fatal(err)
	}
	_, at, _ := a.GetRecordAt(ctx, "tls")
	if err := b.PutRecord(ctx, rec("tls", "two"), false); err != nil {
		t.Fatal(err)
	}
	if err := a.PutRecordAt(ctx, rec("tls", "mine"), at); !errors.Is(err, store.ErrChanged) {
		t.Fatalf("got %v", err)
	}
	if err := a.PutRecord(ctx, rec("tls", "three"), false); err != nil {
		t.Fatal(err)
	}
	if got := value(t, records, commits, "tls"); got != "three" {
		t.Fatalf("the record is %q", got)
	}
	if stored := names(t, records, "tls"); len(stored) != 1 {
		t.Fatalf("stored %q", stored)
	}
}

// What is not a commit takes a version all the same. Left at the version a
// write was going to, it refuses the write. Left above a version that is
// free, it must not send one enclave past the version another is about to
// write: both would be told they wrote, and one change would be lost.
func TestAnchoredWithJunkAmongTheCommits(t *testing.T) {
	ctx := context.Background()
	t.Run("at the version a write goes to", func(t *testing.T) {
		a, _, records, commits := twoEnclaves(true)
		if err := a.PutRecord(ctx, rec("account", "one"), true); err != nil {
			t.Fatal(err)
		}
		_, at, _ := a.GetRecordAt(ctx, "account")
		commits.Take("account", 2)
		if err := a.PutRecordAt(ctx, rec("account", "two"), at); !errors.Is(err, store.ErrChanged) {
			t.Fatalf("got %v", err)
		}
		if got := value(t, records, commits, "account"); got != "one" {
			t.Fatalf("the record is %q", got)
		}
		if stored := names(t, records, "account"); len(stored) != 1 {
			t.Fatalf("stored %q", stored)
		}
		if tries := change(t, a, "account", ", two", nil); tries != 1 {
			t.Fatalf("%d tries", tries)
		}
		if head, _, _ := commits.Head(ctx, "account"); head.Version != 3 {
			t.Fatalf("head %+v", head)
		}
	})
	t.Run("above a version that is free", func(t *testing.T) {
		a, b, records, commits := twoEnclaves(true)
		if err := a.PutRecord(ctx, rec("account", "one"), true); err != nil {
			t.Fatal(err)
		}
		_, atA, _ := a.GetRecordAt(ctx, "account")
		commits.Take("account", 3)
		commits.Take("account", 9)
		_, atB, _ := b.GetRecordAt(ctx, "account")
		if err := b.PutRecordAt(ctx, rec("account", "theirs"), atB); err != nil {
			t.Fatal(err)
		}
		if err := a.PutRecordAt(ctx, rec("account", "mine"), atA); !errors.Is(err, store.ErrChanged) {
			t.Fatalf("a write beside the other enclave's: %v", err)
		}
		if got := value(t, records, commits, "account"); got != "theirs" {
			t.Fatalf("the record is %q", got)
		}
		// The next ones go on past what is in the way.
		for want, add := range map[uint64]string{4: ", a"} {
			change(t, a, "account", add, nil)
			if head, _, _ := commits.Head(ctx, "account"); head.Version != want {
				t.Fatalf("head %+v, want version %d", head, want)
			}
		}
		if got := value(t, records, commits, "account"); got != "theirs, a" {
			t.Fatalf("the record is %q", got)
		}
	})
}

// A record from before commits existed is adopted by the read, and changed
// from there like any other.
func TestAnchoredChangesAnAdoptedRecord(t *testing.T) {
	ctx := context.Background()
	a, b, records, commits := twoEnclaves(false)
	if err := records.PutRecord(ctx, rec("tls", "from before"), true); err != nil {
		t.Fatal(err)
	}
	got, atA, err := a.GetRecordAt(ctx, "tls")
	if err != nil || string(got.Value) != "from before" || got.Name != "tls" {
		t.Fatalf("got %+v, %v", got, err)
	}
	_, atB, err := b.GetRecordAt(ctx, "tls")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.PutRecordAt(ctx, rec("tls", "mine"), atA); err != nil {
		t.Fatal(err)
	}
	if err := b.PutRecordAt(ctx, rec("tls", "theirs"), atB); !errors.Is(err, store.ErrChanged) {
		t.Fatalf("got %v", err)
	}
	if got := value(t, records, commits, "tls"); got != "mine" {
		t.Fatalf("the record is %q", got)
	}
	if head, _, _ := commits.Head(ctx, "tls"); head.Version != 2 {
		t.Fatalf("head %+v", head)
	}
	// The plain record cannot be read: that is an error, and no mark.
	broken := errors.New("table unreachable")
	c := &store.Anchored{Records: failing{RecordTable: records, err: broken}, Commits: store.NewMemoryCommits()}
	if _, at, err := c.GetRecordAt(ctx, "other"); !errors.Is(err, broken) {
		t.Fatalf("got %v", err)
	} else if err := c.PutRecordAt(ctx, rec("other", "x"), at); err == nil || errors.Is(err, store.ErrChanged) {
		t.Fatalf("a write at no mark: %v", err)
	}
}

// A mark is of one record and of one read.
func TestAnchoredRefusesAMarkThatIsNotOfTheRecord(t *testing.T) {
	ctx := context.Background()
	a, _, records, _ := twoEnclaves(true)
	if err := a.PutRecord(ctx, rec("account-a", "one"), true); err != nil {
		t.Fatal(err)
	}
	_, at, err := a.GetRecordAt(ctx, "account-a")
	if err != nil {
		t.Fatal(err)
	}
	for name, mark := range map[string]store.Mark{"account-a": {}, "account-b": at} {
		err := a.PutRecordAt(ctx, rec(name, "x"), mark)
		if err == nil || errors.Is(err, store.ErrChanged) || !strings.Contains(err.Error(), "mark") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if stored := names(t, records, "account-b"); len(stored) != 0 {
		t.Fatalf("stored %q", stored)
	}
}

// Storage that fails is an error, never ErrChanged: the caller must not take
// it for another enclave's change and try again for ever.
func TestAnchoredConditionalWriteFailures(t *testing.T) {
	ctx := context.Background()
	down := errors.New("down")
	setup := func(t *testing.T) (*store.Anchored, *store.Anchored, *store.Memory, *store.MemoryCommits, store.Mark) {
		t.Helper()
		a, b, records, commits := twoEnclaves(true)
		if err := a.PutRecord(ctx, rec("account", "one"), true); err != nil {
			t.Fatal(err)
		}
		_, at, err := a.GetRecordAt(ctx, "account")
		if err != nil {
			t.Fatal(err)
		}
		return a, b, records, commits, at
	}
	t.Run("the commits cannot be read", func(t *testing.T) {
		a, _, _, _, _ := setup(t)
		a.Commits = failingCommits{down}
		if _, _, err := a.GetRecordAt(ctx, "account"); !errors.Is(err, down) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("the value cannot be stored", func(t *testing.T) {
		a, _, records, commits, at := setup(t)
		a.Records = failing{RecordTable: records, err: down}
		if err := a.PutRecordAt(ctx, rec("account", "two"), at); !errors.Is(err, down) || errors.Is(err, store.ErrChanged) {
			t.Fatalf("got %v", err)
		}
		if got := value(t, records, commits, "account"); got != "one" {
			t.Fatalf("the record is %q", got)
		}
	})
	t.Run("the commit cannot be written", func(t *testing.T) {
		a, _, records, commits, at := setup(t)
		a.Commits = &flakyCommits{Commits: commits, appendErr: down}
		if err := a.PutRecordAt(ctx, rec("account", "two"), at); !errors.Is(err, down) || errors.Is(err, store.ErrChanged) {
			t.Fatalf("got %v", err)
		}
		if got := value(t, records, commits, "account"); got != "one" {
			t.Fatalf("the record is %q", got)
		}
		// The write may have reached the store all the same: the value stays, as
		// it would be the one read then. It goes when the record is removed.
		if stored := names(t, records, "account"); len(stored) != 2 {
			t.Fatalf("stored %q", stored)
		}
	})
	t.Run("the commits cannot be read after a refusal", func(t *testing.T) {
		a, b, records, commits, at := setup(t)
		if err := b.PutRecord(ctx, rec("account", "theirs"), false); err != nil {
			t.Fatal(err)
		}
		a.Commits = &refusing{Commits: commits, then: down}
		// It is still told that the record changed; what it stored stays, since
		// it cannot tell that nobody reads it.
		if err := a.PutRecordAt(ctx, rec("account", "mine"), at); !errors.Is(err, store.ErrChanged) {
			t.Fatalf("got %v", err)
		}
		if got := value(t, records, commits, "account"); got != "theirs" {
			t.Fatalf("the record is %q", got)
		}
		if stored := names(t, records, "account"); len(stored) != 2 {
			t.Fatalf("stored %q", stored)
		}
	})
	t.Run("a record from before cannot be committed", func(t *testing.T) {
		records := store.NewMemory()
		if err := records.PutRecord(ctx, rec("tls", "from before"), true); err != nil {
			t.Fatal(err)
		}
		a := &store.Anchored{Records: records, Commits: &flakyCommits{Commits: store.NewMemoryCommits(), appendErr: down}}
		if _, _, err := a.GetRecordAt(ctx, "tls"); !errors.Is(err, down) || errors.Is(err, store.ErrNotFound) {
			t.Fatalf("got %v", err)
		}
		if _, err := a.GetRecord(ctx, "tls"); !errors.Is(err, down) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("the value before cannot be deleted", func(t *testing.T) {
		a, _, records, commits, at := setup(t)
		a.Records = &flakyTable{RecordTable: records, deleteErr: down}
		// The write was committed: it is done.
		if err := a.PutRecordAt(ctx, rec("account", "two"), at); err != nil {
			t.Fatal(err)
		}
		if got := value(t, records, commits, "account"); got != "two" {
			t.Fatalf("the record is %q", got)
		}
	})
}

// refusing is Commits that fail to be read once an append was refused.
type refusing struct {
	store.Commits
	then    error
	refused bool
}

func (r *refusing) Head(ctx context.Context, name string) (*store.Commit, uint64, error) {
	if r.refused {
		return nil, 0, r.then
	}
	return r.Commits.Head(ctx, name)
}

func (r *refusing) Append(ctx context.Context, c store.Commit) (*store.Commit, error) {
	stored, err := r.Commits.Append(ctx, c)
	r.refused = errors.Is(err, store.ErrExists)
	return stored, err
}

// A store of commits that forgot everything after a write was refused: the
// value that was stored is left, since nothing says who reads it.
func TestAnchoredLeavesAValueItCannotPlace(t *testing.T) {
	ctx := context.Background()
	a, b, records, commits := twoEnclaves(true)
	if err := a.PutRecord(ctx, rec("account", "one"), true); err != nil {
		t.Fatal(err)
	}
	_, at, _ := a.GetRecordAt(ctx, "account")
	if err := b.PutRecord(ctx, rec("account", "theirs"), false); err != nil {
		t.Fatal(err)
	}
	fresh := &store.Anchored{Records: records, Commits: &emptied{Commits: commits}, OnlyNewest: true}
	if err := fresh.PutRecordAt(ctx, rec("account", "mine"), at); !errors.Is(err, store.ErrChanged) {
		t.Fatalf("got %v", err)
	}
	if stored := names(t, records, "account"); len(stored) != 2 {
		t.Fatalf("stored %q", stored)
	}
}

// emptied is Commits that answer that there are none.
type emptied struct{ store.Commits }

func (emptied) Head(context.Context, string) (*store.Commit, uint64, error) { return nil, 1, nil }
