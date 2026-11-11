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
		records.DeleteRecord(stored[2])
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
			records.DeleteRecord(n)
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
	store.Records
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

func (r *racing) Append(ctx context.Context, c store.Commit) error {
	if r.times > 0 {
		r.times--
		if err := r.other.PutRecord(ctx, rec(c.Name, r.value), false); err != nil {
			return err
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

func (r *adopting) Append(ctx context.Context, c store.Commit) error {
	if !r.done {
		r.done = true
		if _, err := r.other.GetRecord(ctx, c.Name); err != nil {
			return err
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
func (f failingCommits) Append(context.Context, store.Commit) error { return f.err }
