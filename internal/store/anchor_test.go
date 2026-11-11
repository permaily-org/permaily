package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/store"
)

func anchored() (*store.Anchored, *store.Memory, *store.MemoryCommits) {
	records, commits := store.NewMemory(), store.NewMemoryCommits()
	return &store.Anchored{Records: records, Commits: commits}, records, commits
}

func rec(name, value string) *store.Record {
	return &store.Record{Name: name, WrappedKey: []byte("wrapped"), Value: []byte(value), UpdatedAt: time.Unix(1, 0).UTC()}
}

// names returns the names of everything in the plain storage that belongs to a record.
func names(t *testing.T, m *store.Memory, name string) []string {
	t.Helper()
	var out []string
	for _, n := range m.RecordNames() {
		if n == name || strings.HasPrefix(n, name+"@") {
			out = append(out, n)
		}
	}
	return out
}

func TestAnchoredReadsTheNewestVersion(t *testing.T) {
	a, records, commits := anchored()
	ctx := context.Background()

	if _, err := a.GetRecord(ctx, "tls"); !errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrLost) {
		t.Fatalf("a record never stored: %v", err)
	}
	if err := a.PutRecord(ctx, rec("tls", "one"), true); err != nil {
		t.Fatal(err)
	}
	if err := a.PutRecord(ctx, rec("tls", "again"), true); !errors.Is(err, store.ErrExists) {
		t.Fatalf("creating what exists: %v", err)
	}
	if err := a.PutRecord(ctx, rec("tls", "two"), false); err != nil {
		t.Fatal(err)
	}
	got, err := a.GetRecord(ctx, "tls")
	if err != nil || string(got.Value) != "two" || got.Name != "tls" || string(got.WrappedKey) != "wrapped" {
		t.Fatalf("got %+v, %v", got, err)
	}
	head, next, err := commits.Head(ctx, "tls")
	if err != nil || head.Version != 2 || next != 3 {
		t.Fatalf("head %+v, next %d, %v", head, next, err)
	}
	// Each version is stored under a name of its own, and nothing under the plain one.
	if stored := names(t, records, "tls"); len(stored) != 2 || !strings.HasPrefix(stored[0], "tls@00000000000000000001-") || !strings.HasPrefix(stored[1], "tls@00000000000000000002-") {
		t.Fatalf("stored %q", stored)
	}

	// Another enclave of the same deployment, started later, reads the same.
	other := &store.Anchored{Records: records, Commits: commits}
	if got, err := other.GetRecord(ctx, "tls"); err != nil || string(got.Value) != "two" {
		t.Fatalf("a second enclave: %+v, %v", got, err)
	}
	// Records do not mix.
	if err := a.PutRecord(ctx, rec("mailbox", "m"), true); err != nil {
		t.Fatal(err)
	}
	if got, err := a.GetRecord(ctx, "tls"); err != nil || string(got.Value) != "two" {
		t.Fatalf("after another record: %+v, %v", got, err)
	}
	if seen := a.Seen(); len(seen) != 2 {
		t.Fatalf("seen %+v", seen)
	}
}

// What whoever controls the storage can do, and what the enclave answers.
func TestAnchoredRefusesEarlierCopies(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*store.Anchored, *store.Memory, []string) {
		t.Helper()
		a, records, _ := anchored()
		for _, v := range []string{"one", "two", "three"} {
			if err := a.PutRecord(ctx, rec("tls", v), false); err != nil {
				t.Fatal(err)
			}
		}
		// A fresh enclave: nothing in memory, as after a restart.
		return &store.Anchored{Records: records, Commits: a.Commits}, records, names(t, records, "tls")
	}

	t.Run("the newest version is deleted", func(t *testing.T) {
		a, records, stored := setup(t)
		_ = records.DeleteRecord(ctx, stored[2])
		// Never the version before: the record is lost, which is a kind of not found.
		if _, err := a.GetRecord(ctx, "tls"); !errors.Is(err, store.ErrLost) || !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("got %v", err)
		}
		// It can be made again, as a later version, and that one is read.
		if err := a.PutRecord(ctx, rec("tls", "four"), true); err != nil {
			t.Fatal(err)
		}
		if got, err := a.GetRecord(ctx, "tls"); err != nil || string(got.Value) != "four" {
			t.Fatalf("after making it again: %+v, %v", got, err)
		}
	})
	t.Run("an earlier version is put in the place of the newest", func(t *testing.T) {
		a, records, stored := setup(t)
		old, err := records.GetRecord(ctx, stored[0])
		if err != nil {
			t.Fatal(err)
		}
		old.Name = stored[2]
		if err := records.PutRecord(ctx, old, false); err != nil {
			t.Fatal(err)
		}
		if _, err := a.GetRecord(ctx, "tls"); !errors.Is(err, store.ErrLost) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("an earlier version is put under the plain name", func(t *testing.T) {
		a, records, stored := setup(t)
		old, err := records.GetRecord(ctx, stored[0])
		if err != nil {
			t.Fatal(err)
		}
		old.Name = "tls"
		if err := records.PutRecord(ctx, old, false); err != nil {
			t.Fatal(err)
		}
		if got, err := a.GetRecord(ctx, "tls"); err != nil || string(got.Value) != "three" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("the wrapped key of the newest version is changed", func(t *testing.T) {
		a, records, stored := setup(t)
		newest, err := records.GetRecord(ctx, stored[2])
		if err != nil {
			t.Fatal(err)
		}
		newest.WrappedKey = []byte("another key")
		if err := records.PutRecord(ctx, newest, false); err != nil {
			t.Fatal(err)
		}
		if _, err := a.GetRecord(ctx, "tls"); !errors.Is(err, store.ErrLost) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("everything is deleted", func(t *testing.T) {
		a, records, stored := setup(t)
		for _, n := range stored {
			_ = records.DeleteRecord(ctx, n)
		}
		if _, err := a.GetRecord(ctx, "tls"); !errors.Is(err, store.ErrLost) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("the storage fails", func(t *testing.T) {
		a, records, _ := setup(t)
		broken := errors.New("storage down")
		a.Records = failing{records, broken}
		if _, err := a.GetRecord(ctx, "tls"); !errors.Is(err, broken) || errors.Is(err, store.ErrNotFound) {
			t.Fatalf("got %v", err)
		}
		if err := a.PutRecord(ctx, rec("tls", "four"), true); !errors.Is(err, broken) {
			t.Fatalf("got %v", err)
		}
		if err := a.PutRecord(ctx, rec("tls", "four"), false); !errors.Is(err, broken) {
			t.Fatalf("got %v", err)
		}
	})
}

type failing struct {
	store.RecordTable
	err error
}

func (f failing) GetRecord(context.Context, string) (*store.Record, error) { return nil, f.err }
func (f failing) PutRecord(context.Context, *store.Record, bool) error     { return f.err }

// A record written by a release from before commits existed is adopted as it
// is, once, and stays where it was for that release.
func TestAnchoredAdoptsAPlainRecord(t *testing.T) {
	a, records, commits := anchored()
	ctx := context.Background()
	if err := records.PutRecord(ctx, rec("tls", "from before"), true); err != nil {
		t.Fatal(err)
	}
	if err := a.PutRecord(ctx, rec("tls", "new"), true); !errors.Is(err, store.ErrExists) {
		t.Fatalf("creating over a plain record: %v", err)
	}
	got, err := a.GetRecord(ctx, "tls")
	if err != nil || string(got.Value) != "from before" {
		t.Fatalf("got %+v, %v", got, err)
	}
	head, _, err := commits.Head(ctx, "tls")
	if err != nil || head == nil || head.Version != 1 {
		t.Fatalf("head %+v, %v", head, err)
	}
	if stored := names(t, records, "tls"); len(stored) != 2 || stored[0] != "tls" {
		t.Fatalf("stored %q", stored)
	}

	// From then on the plain record is not read: changing it changes nothing,
	// and the old release still finds it if it runs again.
	if err := a.PutRecord(ctx, rec("tls", "renewed"), false); err != nil {
		t.Fatal(err)
	}
	if plain, err := records.GetRecord(ctx, "tls"); err != nil || string(plain.Value) != "from before" {
		t.Fatalf("the plain record: %+v, %v", plain, err)
	}
	if err := records.PutRecord(ctx, rec("tls", "an older copy"), false); err != nil {
		t.Fatal(err)
	}
	fresh := &store.Anchored{Records: records, Commits: commits}
	if got, err := fresh.GetRecord(ctx, "tls"); err != nil || string(got.Value) != "renewed" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// Whoever can write to the store of commits can leave things there. They are
// not commits: the enclave writes after them.
func TestAnchoredWritesAfterWhatIsNotACommit(t *testing.T) {
	a, _, commits := anchored()
	ctx := context.Background()
	commits.Take("tls", 1)
	commits.Take("tls", 2)
	if err := a.PutRecord(ctx, rec("tls", "one"), true); err != nil {
		t.Fatal(err)
	}
	if head, next, _ := commits.Head(ctx, "tls"); head.Version != 3 || next != 4 {
		t.Fatalf("head %+v, next %d", head, next)
	}
	commits.Take("tls", 7)
	if err := a.PutRecord(ctx, rec("tls", "two"), false); err != nil {
		t.Fatal(err)
	}
	if head, _, _ := commits.Head(ctx, "tls"); head.Version != 8 {
		t.Fatalf("head %+v", head)
	}
	if got, err := a.GetRecord(ctx, "tls"); err != nil || string(got.Value) != "two" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// racing is Commits where another enclave takes the version first, a number of times.
type racing struct {
	store.Commits
	other *store.Anchored
	times int
	value string
}

func (r *racing) Append(ctx context.Context, c store.Commit) (*store.Commit, error) {
	if r.times > 0 {
		r.times--
		if err := r.other.PutRecord(ctx, rec(c.Name, r.value), false); err != nil {
			return nil, err
		}
	}
	return r.Commits.Append(ctx, c)
}

func TestAnchoredWithTwoEnclaves(t *testing.T) {
	ctx := context.Background()
	t.Run("both create the record: one wins, the other reads it", func(t *testing.T) {
		a, records, commits := anchored()
		other := &store.Anchored{Records: records, Commits: commits}
		a.Commits = &racing{Commits: commits, other: other, times: 1, value: "theirs"}
		if err := a.PutRecord(ctx, rec("tls", "mine"), true); !errors.Is(err, store.ErrExists) {
			t.Fatalf("got %v", err)
		}
		if got, err := a.GetRecord(ctx, "tls"); err != nil || string(got.Value) != "theirs" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("both update it: the later write is the newest", func(t *testing.T) {
		a, records, commits := anchored()
		if err := a.PutRecord(ctx, rec("tls", "one"), true); err != nil {
			t.Fatal(err)
		}
		other := &store.Anchored{Records: records, Commits: commits}
		a.Commits = &racing{Commits: commits, other: other, times: 1, value: "theirs"}
		if err := a.PutRecord(ctx, rec("tls", "mine"), false); err != nil {
			t.Fatal(err)
		}
		if got, err := a.GetRecord(ctx, "tls"); err != nil || string(got.Value) != "mine" {
			t.Fatalf("got %+v, %v", got, err)
		}
		if head, _, _ := commits.Head(ctx, "tls"); head.Version != 3 {
			t.Fatalf("head %+v", head)
		}
	})
	t.Run("the other never stops", func(t *testing.T) {
		a, records, commits := anchored()
		other := &store.Anchored{Records: records, Commits: commits}
		a.Commits = &racing{Commits: commits, other: other, times: 10, value: "theirs"}
		if err := a.PutRecord(ctx, rec("tls", "mine"), false); err == nil || !strings.Contains(err.Error(), "keep changing") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a plain record is adopted by both at once", func(t *testing.T) {
		a, records, commits := anchored()
		if err := records.PutRecord(ctx, rec("tls", "from before"), true); err != nil {
			t.Fatal(err)
		}
		other := &store.Anchored{Records: records, Commits: commits}
		a.Commits = &adopting{Commits: commits, other: other}
		if got, err := a.GetRecord(ctx, "tls"); err != nil || string(got.Value) != "from before" {
			t.Fatalf("got %+v, %v", got, err)
		}
		if head, next, _ := commits.Head(ctx, "tls"); head.Version != 1 || next != 2 {
			t.Fatalf("head %+v, next %d", head, next)
		}
	})
}

// adopting is Commits where another enclave adopts the plain record first.
type adopting struct {
	store.Commits
	other *store.Anchored
	done  bool
}

func (r *adopting) Append(ctx context.Context, c store.Commit) (*store.Commit, error) {
	if !r.done {
		r.done = true
		if _, err := r.other.GetRecord(ctx, c.Name); err != nil {
			return nil, err
		}
	}
	return r.Commits.Append(ctx, c)
}

// The store of commits lets nobody remove one. If one goes anyway, an enclave
// that knew it refuses to go on with the record, and remembers.
func TestAnchoredNoticesCommitsGoingBack(t *testing.T) {
	a, _, commits := anchored()
	ctx := context.Background()
	at := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return at }
	for _, v := range []string{"one", "two"} {
		if err := a.PutRecord(ctx, rec("tls", v), false); err != nil {
			t.Fatal(err)
		}
	}
	if len(a.Regressions()) != 0 {
		t.Fatalf("regressions %+v", a.Regressions())
	}
	commits.Forget("tls")
	if _, err := a.GetRecord(ctx, "tls"); !errors.Is(err, store.ErrRolledBack) || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("read: %v", err)
	}
	if err := a.PutRecord(ctx, rec("tls", "three"), false); !errors.Is(err, store.ErrRolledBack) {
		t.Fatalf("write: %v", err)
	}
	commits.Forget("tls")
	if _, err := a.GetRecord(ctx, "tls"); !errors.Is(err, store.ErrRolledBack) {
		t.Fatalf("read with no commit left: %v", err)
	}
	got := a.Regressions()
	if len(got) != 3 || got[0] != (store.Regression{Name: "tls", Had: 2, Found: 1, At: at}) || got[2].Found != 0 {
		t.Fatalf("regressions %+v", got)
	}
	// What is remembered has a bound.
	for range 100 {
		_, _ = a.GetRecord(ctx, "tls")
	}
	if n := len(a.Regressions()); n != 32 {
		t.Fatalf("%d regressions remembered", n)
	}

	// An enclave that starts afterwards cannot know: it reads what the store
	// says, which is why the store must be one that only grows.
	fresh := &store.Anchored{Records: a.Records, Commits: commits}
	if _, err := fresh.GetRecord(ctx, "tls"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a fresh enclave: %v", err)
	}

	// A failure of the store of commits is not an answer.
	broken := errors.New("commits unreachable")
	fresh.Commits = failingCommits{broken}
	if _, err := fresh.GetRecord(ctx, "tls"); !errors.Is(err, broken) || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if err := fresh.PutRecord(ctx, rec("tls", "x"), true); !errors.Is(err, broken) {
		t.Fatalf("got %v", err)
	}
}

type failingCommits struct{ err error }

func (f failingCommits) Head(context.Context, string) (*store.Commit, uint64, error) {
	return nil, 0, f.err
}
func (f failingCommits) Append(context.Context, store.Commit) (*store.Commit, error) {
	return nil, f.err
}

// A record that was removed is not there: not lost, and not brought back by
// anything put in the storage. Its name can be stored again, as a later
// version.
func TestAnchoredRemovesARecord(t *testing.T) {
	ctx := context.Background()
	a, records, commits := anchored()
	for _, v := range []string{"one", "two"} {
		if err := a.PutRecord(ctx, rec("account", v), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.PutRecord(ctx, rec("other", "theirs"), true); err != nil {
		t.Fatal(err)
	}
	stored := names(t, records, "account")
	var copies []*store.Record
	for _, n := range stored {
		r, err := records.GetRecord(ctx, n)
		if err != nil {
			t.Fatal(err)
		}
		copies = append(copies, r)
	}

	if err := a.RemoveRecord(ctx, "account"); err != nil {
		t.Fatal(err)
	}
	gone := func(t *testing.T, a *store.Anchored) {
		t.Helper()
		if _, err := a.GetRecord(ctx, "account"); !errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrLost) {
			t.Fatalf("a removed record: %v", err)
		}
	}
	gone(t, a)
	if left := names(t, records, "account"); len(left) != 0 {
		t.Fatalf("left in the storage: %q", left)
	}
	head, next, err := commits.Head(ctx, "account")
	if err != nil || head == nil || !head.Gone() || head.Version != 3 || next != 4 {
		t.Fatalf("head %+v, next %d, %v", head, next, err)
	}
	// Another record is as it was.
	if got, err := a.GetRecord(ctx, "other"); err != nil || string(got.Value) != "theirs" {
		t.Fatalf("another record: %+v, %v", got, err)
	}

	// Whoever runs the storage puts earlier copies back: where they were,
	// under the plain name, and where the commit that says it is gone would
	// have its value. An enclave that just started reads none of them.
	fresh := &store.Anchored{Records: records, Commits: commits}
	for _, c := range copies {
		records.SetRecord(*c)
		plain := *c
		plain.Name = "account"
		records.SetRecord(plain)
		last := *c
		last.Name = "account@00000000000000000003-0000000000000000"
		records.SetRecord(last)
		gone(t, a)
		gone(t, fresh)
	}
	// Removing it again writes no commit, and deletes what was put back.
	if err := fresh.RemoveRecord(ctx, "account"); err != nil {
		t.Fatal(err)
	}
	if _, next, _ := commits.Head(ctx, "account"); next != 4 {
		t.Fatalf("a second removal wrote a commit: next %d", next)
	}
	if left := names(t, records, "account"); len(left) != 0 {
		t.Fatalf("left after the second removal: %q", left)
	}

	// The name can be stored again: a later version, read like any other.
	if err := a.PutRecord(ctx, rec("account", "again"), true); err != nil {
		t.Fatal(err)
	}
	if err := a.PutRecord(ctx, rec("account", "over it"), true); !errors.Is(err, store.ErrExists) {
		t.Fatalf("creating what exists again: %v", err)
	}
	if got, err := fresh.GetRecord(ctx, "account"); err != nil || string(got.Value) != "again" {
		t.Fatalf("stored again: %+v, %v", got, err)
	}
	if head, _, _ := commits.Head(ctx, "account"); head.Version != 4 || head.Gone() {
		t.Fatalf("head %+v", head)
	}
	// And removed again, and written without asking whether it was there.
	if err := a.RemoveRecord(ctx, "account"); err != nil {
		t.Fatal(err)
	}
	gone(t, fresh)
	if err := a.PutRecord(ctx, rec("account", "once more"), false); err != nil {
		t.Fatal(err)
	}
	if got, err := fresh.GetRecord(ctx, "account"); err != nil || string(got.Value) != "once more" {
		t.Fatalf("written after a removal: %+v, %v", got, err)
	}

	// A record that was never stored is removed all the same: what is put
	// under its name afterwards is not taken for a record from before.
	if err := a.RemoveRecord(ctx, "never"); err != nil {
		t.Fatal(err)
	}
	records.SetRecord(*rec("never", "by hand"))
	if _, err := fresh.GetRecord(ctx, "never"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a record put under a removed name: %v", err)
	}
	// A record from before commits existed goes with its plain copy.
	records.SetRecord(*rec("old", "from before"))
	if err := a.RemoveRecord(ctx, "old"); err != nil {
		t.Fatal(err)
	}
	if left := names(t, records, "old"); len(left) != 0 {
		t.Fatalf("left of a record from before: %q", left)
	}
}

// For records of which only the newest version is kept, the value of the one
// before is deleted once the new one is committed, and never before.
func TestAnchoredKeepsOnlyTheNewest(t *testing.T) {
	ctx := context.Background()
	a, records, commits := anchored()
	a.OnlyNewest = true
	for i, v := range []string{"one", "two", "three"} {
		if err := a.PutRecord(ctx, rec("account", v), false); err != nil {
			t.Fatal(err)
		}
		stored := names(t, records, "account")
		if len(stored) != 1 || !strings.HasPrefix(stored[0], "account@0000000000000000000"+string(rune('1'+i))+"-") {
			t.Fatalf("after %q: stored %q", v, stored)
		}
	}
	for _, name := range names(t, records, "account") {
		if r, _ := records.GetRecord(ctx, name); string(r.Value) != "three" {
			t.Fatalf("an earlier value is still stored: %q", r.Value)
		}
	}
	if got, err := (&store.Anchored{Records: records, Commits: commits}).GetRecord(ctx, "account"); err != nil || string(got.Value) != "three" {
		t.Fatalf("got %+v, %v", got, err)
	}

	// A commit that fails deletes nothing: the record is as it was.
	broken := errors.New("commits unreachable")
	a.Commits = &flakyCommits{Commits: commits, appendErr: broken}
	if err := a.PutRecord(ctx, rec("account", "four"), false); !errors.Is(err, broken) {
		t.Fatalf("got %v", err)
	}
	a.Commits = commits
	if got, err := a.GetRecord(ctx, "account"); err != nil || string(got.Value) != "three" {
		t.Fatalf("after a commit that failed: %+v, %v", got, err)
	}

	// A value that cannot be deleted does not fail the write that is done:
	// it stays, it is not the one read, and it goes with the record.
	down := errors.New("storage down")
	a.Records = &flakyTable{RecordTable: records, deleteErr: down}
	if err := a.PutRecord(ctx, rec("account", "five"), false); err != nil {
		t.Fatalf("a write whose clean-up failed: %v", err)
	}
	a.Records = records
	if got, err := a.GetRecord(ctx, "account"); err != nil || string(got.Value) != "five" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if stored := names(t, records, "account"); len(stored) < 2 {
		t.Fatalf("stored %q", stored)
	}
	if err := a.RemoveRecord(ctx, "account"); err != nil {
		t.Fatal(err)
	}
	if left := names(t, records, "account"); len(left) != 0 {
		t.Fatalf("left after the removal: %q", left)
	}
	// Stored again after a removal, there is no value before it to delete.
	if err := a.PutRecord(ctx, rec("account", "six"), true); err != nil {
		t.Fatal(err)
	}
	if stored := names(t, records, "account"); len(stored) != 1 {
		t.Fatalf("stored %q", stored)
	}
}

// flakyCommits is Commits that fail on demand, or answer a read with what
// was true a moment before.
type flakyCommits struct {
	store.Commits
	appendErr error
	// before runs after the newest commit was read and before it is
	// answered: what another request does in between.
	before func()
}

func (f *flakyCommits) Head(ctx context.Context, name string) (*store.Commit, uint64, error) {
	head, next, err := f.Commits.Head(ctx, name)
	if f.before != nil {
		f.before()
	}
	return head, next, err
}

func (f *flakyCommits) Append(ctx context.Context, c store.Commit) (*store.Commit, error) {
	if f.appendErr != nil {
		return nil, f.appendErr
	}
	return f.Commits.Append(ctx, c)
}

// flakyTable is storage that fails to list or to delete.
type flakyTable struct {
	store.RecordTable
	listErr, deleteErr error
	// listed is answered by every listing, whatever is stored.
	listed []string
}

func (f *flakyTable) ListRecords(ctx context.Context, prefix string, limit int) ([]string, error) {
	if f.listErr != nil || f.listed != nil {
		return f.listed, f.listErr
	}
	return f.RecordTable.ListRecords(ctx, prefix, limit)
}

func (f *flakyTable) DeleteRecord(ctx context.Context, name string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return f.RecordTable.DeleteRecord(ctx, name)
}

// A reader that took the newest commit just before a write or a removal
// finds the value it names deleted. That is not a lost record: it reads again.
func TestAnchoredReadsWhileARecordChanges(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (writer, reader *store.Anchored, slow *flakyCommits) {
		t.Helper()
		writer, records, commits := anchored()
		writer.OnlyNewest = true
		if err := writer.PutRecord(ctx, rec("account", "one"), true); err != nil {
			t.Fatal(err)
		}
		slow = &flakyCommits{Commits: commits}
		return writer, &store.Anchored{Records: records, Commits: slow}, slow
	}
	once := func(slow *flakyCommits, change func()) {
		slow.before = func() {
			slow.before = nil
			change()
		}
	}

	t.Run("a write in between", func(t *testing.T) {
		writer, reader, slow := setup(t)
		once(slow, func() {
			if err := writer.PutRecord(ctx, rec("account", "two"), false); err != nil {
				t.Fatal(err)
			}
		})
		if got, err := reader.GetRecord(ctx, "account"); err != nil || string(got.Value) != "two" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("a removal in between", func(t *testing.T) {
		writer, reader, slow := setup(t)
		once(slow, func() {
			if err := writer.RemoveRecord(ctx, "account"); err != nil {
				t.Fatal(err)
			}
		})
		if _, err := reader.GetRecord(ctx, "account"); !errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrLost) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("writes that never stop", func(t *testing.T) {
		writer, reader, slow := setup(t)
		slow.before = func() {
			if err := writer.PutRecord(ctx, rec("account", "another"), false); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := reader.GetRecord(ctx, "account"); err == nil || !strings.Contains(err.Error(), "keep changing") || errors.Is(err, store.ErrNotFound) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("the commits fail on the second read", func(t *testing.T) {
		writer, reader, slow := setup(t)
		broken := errors.New("commits unreachable")
		once(slow, func() {
			if err := writer.PutRecord(ctx, rec("account", "two"), false); err != nil {
				t.Fatal(err)
			}
			reader.Commits = failingCommits{broken}
		})
		if _, err := reader.GetRecord(ctx, "account"); !errors.Is(err, broken) {
			t.Fatalf("got %v", err)
		}
	})
}

// A removal that fails leaves the record there, or gone: never a record that
// reads as an earlier one, and what is left in the storage goes when the
// removal is asked again.
func TestAnchoredRemovalFailures(t *testing.T) {
	ctx := context.Background()
	down := errors.New("down")
	setup := func(t *testing.T) (*store.Anchored, *store.Memory, *store.MemoryCommits) {
		t.Helper()
		a, records, commits := anchored()
		for _, v := range []string{"one", "two"} {
			if err := a.PutRecord(ctx, rec("account", v), false); err != nil {
				t.Fatal(err)
			}
		}
		return a, records, commits
	}

	t.Run("the commits cannot be read", func(t *testing.T) {
		a, records, commits := setup(t)
		a.Commits = failingCommits{down}
		if err := a.RemoveRecord(ctx, "account"); !errors.Is(err, down) {
			t.Fatalf("got %v", err)
		}
		a.Commits = commits
		if got, err := a.GetRecord(ctx, "account"); err != nil || string(got.Value) != "two" || len(names(t, records, "account")) != 2 {
			t.Fatalf("after a removal that did not start: %+v, %v", got, err)
		}
	})
	t.Run("the commit cannot be written", func(t *testing.T) {
		a, records, commits := setup(t)
		a.Commits = &flakyCommits{Commits: commits, appendErr: down}
		if err := a.RemoveRecord(ctx, "account"); !errors.Is(err, down) {
			t.Fatalf("got %v", err)
		}
		a.Commits = commits
		// Nothing was deleted before the commit: the record is whole.
		if got, err := a.GetRecord(ctx, "account"); err != nil || string(got.Value) != "two" || len(names(t, records, "account")) != 2 {
			t.Fatalf("after a commit that failed: %+v, %v", got, err)
		}
	})
	for name, table := range map[string]*flakyTable{
		"the versions cannot be listed": {listErr: down},
		"a version cannot be deleted":   {deleteErr: down},
	} {
		t.Run(name, func(t *testing.T) {
			a, records, commits := setup(t)
			table.RecordTable = records
			a.Records = table
			if err := a.RemoveRecord(ctx, "account"); !errors.Is(err, down) {
				t.Fatalf("got %v", err)
			}
			// The commit is written: the record is gone for every reader,
			// with its values still in the storage.
			fresh := &store.Anchored{Records: records, Commits: commits}
			if _, err := fresh.GetRecord(ctx, "account"); !errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrLost) {
				t.Fatalf("after a removal that failed half way: %v", err)
			}
			if len(names(t, records, "account")) != 2 {
				t.Fatalf("stored %q", names(t, records, "account"))
			}
			// Asked again, what was left goes.
			if err := fresh.RemoveRecord(ctx, "account"); err != nil {
				t.Fatal(err)
			}
			if left := names(t, records, "account"); len(left) != 0 {
				t.Fatalf("left %q", left)
			}
		})
	}
	t.Run("only the plain copy cannot be deleted", func(t *testing.T) {
		a, records, _ := anchored()
		a.Records = &flakyTable{RecordTable: records, deleteErr: down, listed: []string{}}
		if err := a.RemoveRecord(ctx, "account"); !errors.Is(err, down) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("versions that keep coming back", func(t *testing.T) {
		a, records, commits := setup(t)
		a.Records = &flakyTable{RecordTable: records, listed: []string{"account@put-back"}}
		if err := a.RemoveRecord(ctx, "account"); err == nil || !strings.Contains(err.Error(), "keep coming back") {
			t.Fatalf("got %v", err)
		}
		if _, err := (&store.Anchored{Records: records, Commits: commits}).GetRecord(ctx, "account"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("the record all the same: %v", err)
		}
	})
	t.Run("another enclave writes while it is removed", func(t *testing.T) {
		a, records, commits := setup(t)
		other := &store.Anchored{Records: records, Commits: commits}
		a.Commits = &racing{Commits: commits, other: other, times: 1, value: "theirs"}
		if err := a.RemoveRecord(ctx, "account"); err != nil {
			t.Fatal(err)
		}
		// What the other wrote went too.
		if _, err := other.GetRecord(ctx, "account"); !errors.Is(err, store.ErrNotFound) || len(names(t, records, "account")) != 0 {
			t.Fatalf("got %v, stored %q", err, names(t, records, "account"))
		}
		if head, _, _ := commits.Head(ctx, "account"); head.Version != 4 || !head.Gone() {
			t.Fatalf("head %+v", head)
		}
	})
	t.Run("the other never stops", func(t *testing.T) {
		a, records, commits := setup(t)
		other := &store.Anchored{Records: records, Commits: commits}
		a.Commits = &racing{Commits: commits, other: other, times: 10, value: "theirs"}
		if err := a.RemoveRecord(ctx, "account"); err == nil || !strings.Contains(err.Error(), "keep changing") {
			t.Fatalf("got %v", err)
		}
		// Not removed, and not half removed.
		if got, err := other.GetRecord(ctx, "account"); err != nil || string(got.Value) != "theirs" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
}
