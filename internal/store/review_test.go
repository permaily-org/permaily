package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/permaily-org/permaily/internal/store"
)

// The cases of the code review of pull request 87 (findings 1 to 7, and
// decisions A to E), each a sequence that failed before its fix.

// hookedCommits is MemoryCommits with a hook before each Append, which may
// answer in its place, and one after each History, with how many there were.
type hookedCommits struct {
	*store.MemoryCommits
	append    func(c store.Commit) (handled bool, err error)
	history   func(n int)
	histories int
}

func (h *hookedCommits) Append(ctx context.Context, c store.Commit) (*store.Commit, error) {
	if h.append != nil {
		if handled, err := h.append(c); handled {
			return nil, err
		}
	}
	return h.MemoryCommits.Append(ctx, c)
}

func (h *hookedCommits) History(ctx context.Context, name string) (*store.History, error) {
	hist, err := h.MemoryCommits.History(ctx, name)
	h.histories++
	if h.history != nil {
		h.history(h.histories)
	}
	return hist, err
}

// gettingBucket is recordingBucket with a hook before each GetObject, as
// another read of the store of commits running at that moment.
type gettingBucket struct {
	*recordingBucket
	onGet func(key string)
}

func (g *gettingBucket) GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if g.onGet != nil {
		g.onGet(aws.ToString(in.Key))
	}
	return g.recordingBucket.GetObject(ctx, in, opts...)
}

// Review 1: a fork read once, then pushed out of S3Commits' memory of what
// it read while a later read decides, is still a fork. A record has two
// commits at version 1 (the history reports the fork and keeps both); the
// memory is filled with other records' objects, before the read or by
// another read during it; version 2 is appended; a strict read fetches it.
// Before the fix the read left the dropped commit out and answered version
// 2's value.
func TestReview1AForkIsNotLostToTheCache(t *testing.T) {
	ctx := context.Background()
	for _, how := range []string{"the memory is full before the read", "another read fills the memory during it"} {
		t.Run(how, func(t *testing.T) {
			w := newStrictWorld()
			g := &gettingBucket{recordingBucket: w.b}
			c := w.commits()
			c.S3 = g
			a := &store.Anchored{Records: w.records, Commits: c, OnlyNewest: true, Strict: true, Now: func() time.Time { return w.now }}
			if err := a.CreateNew(ctx, rec("mailbox-1", "v1")); err != nil {
				t.Fatal(err)
			}
			if _, err := a.GetRecord(ctx, "mailbox-1"); err != nil {
				t.Fatal(err)
			}
			w.b.hide(w.last().key)
			if _, err := w.commits().Append(ctx, commitOf("mailbox-1", 1, "another")); err != nil {
				t.Fatal(err)
			}
			if h, err := c.History(ctx, "mailbox-1"); err != nil || len(h.Forks) != 1 {
				t.Fatalf("history %+v, %v", h, err)
			}
			// Version 2, by a writer that is not strict (it reads the smaller digest).
			loose := &store.Anchored{Records: w.records, Commits: w.commits(), Now: func() time.Time { return w.now }}
			if err := loose.PutRecord(ctx, rec("mailbox-1", "v2"), false); err != nil {
				t.Fatal(err)
			}
			if how == "the memory is full before the read" {
				store.FillKnown(c, store.MaxKnownObjects-store.KnownObjects(c))
			} else {
				var once sync.Once
				g.onGet = func(string) { once.Do(func() { store.FillKnown(c, store.MaxKnownObjects) }) }
			}
			if got, err := a.GetRecord(ctx, "mailbox-1"); !errors.Is(err, store.ErrForked) {
				t.Fatalf("read %+v, %v", got, err)
			}
		})
	}
}

// Review 2: two writers create one name with the same bytes, so they stage
// their value under one name. A's append is answered ErrExists while B's is
// under way (S3's 409), A's reread finds no commit, and B's append lands
// then. A must not delete the value B's commit names.
func TestReview2CreateNewLeavesAValueItMayShare(t *testing.T) {
	ctx := context.Background()
	records := store.NewMemory()
	commits := &hookedCommits{MemoryCommits: store.NewMemoryCommits()}
	a := &store.Anchored{Records: records, Commits: commits, OnlyNewest: true, Strict: true}
	var b *store.Commit
	commits.append = func(c store.Commit) (bool, error) {
		if b == nil {
			b = &c // B's identical commit, on its way
			return true, store.ErrExists
		}
		return false, nil
	}
	commits.history = func(n int) {
		if n == 2 { // A's reread has answered: B's append lands now
			if _, err := commits.MemoryCommits.Append(ctx, *b); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := a.CreateNew(ctx, rec("mailbox-1", "same bytes")); !errors.Is(err, store.ErrExists) {
		t.Fatalf("A: %v", err)
	}
	fresh := &store.Anchored{Records: records, Commits: commits.MemoryCommits, Strict: true}
	if got, err := fresh.GetRecord(ctx, "mailbox-1"); err != nil || string(got.Value) != "same bytes" {
		t.Fatalf("B's record: %+v, %v", got, err)
	}
}

// Review 3: a strict writer reads a clean history; a fork appears at the
// version it appends at before its append lands. The conditional write
// fails, the reread finds the fork: ErrForked, not ErrChanged. The same for
// RemoveRecord and CreateNew; a writer that is not strict still answers
// ErrChanged.
func TestReview3AForkFoundAfterAFailedAppendIsErrForked(t *testing.T) {
	ctx := context.Background()
	forkAt := func(commits *hookedCommits, name string, version uint64) {
		commits.append = func(c store.Commit) (bool, error) {
			if c.Name == name && c.Version == version {
				commits.append = nil
				if _, err := commits.MemoryCommits.Append(ctx, commitOf(name, version, "B")); err != nil {
					t.Fatal(err)
				}
				commits.Fork(name, version, sha256.Sum256([]byte("C")))
			}
			return false, nil
		}
	}
	for _, strict := range []bool{true, false} {
		records := store.NewMemory()
		commits := &hookedCommits{MemoryCommits: store.NewMemoryCommits()}
		a := &store.Anchored{Records: records, Commits: commits, OnlyNewest: true, Strict: strict}
		if err := a.PutRecord(ctx, rec("mailbox-1", "one"), true); err != nil {
			t.Fatal(err)
		}
		_, mark, err := a.GetRecordAt(ctx, "mailbox-1")
		if err != nil {
			t.Fatal(err)
		}
		forkAt(commits, "mailbox-1", 2)
		err = a.PutRecordAt(ctx, rec("mailbox-1", "A"), mark)
		if strict && !errors.Is(err, store.ErrForked) {
			t.Fatalf("PutRecordAt, strict: %v", err)
		}
		if !strict && (!errors.Is(err, store.ErrChanged) || errors.Is(err, store.ErrForked)) {
			t.Fatalf("PutRecordAt, not strict: %v", err)
		}
	}
	records := store.NewMemory()
	commits := &hookedCommits{MemoryCommits: store.NewMemoryCommits()}
	a := &store.Anchored{Records: records, Commits: commits, OnlyNewest: true, Strict: true}
	if err := a.CreateNew(ctx, rec("mailbox-1", "one")); err != nil {
		t.Fatal(err)
	}
	forkAt(commits, "mailbox-1", 2)
	if err := a.RemoveRecord(ctx, "mailbox-1"); !errors.Is(err, store.ErrForked) {
		t.Fatalf("RemoveRecord: %v", err)
	}
	forkAt(commits, "mailbox-2", 1)
	if err := a.CreateNew(ctx, rec("mailbox-2", "A")); !errors.Is(err, store.ErrForked) {
		t.Fatalf("CreateNew: %v", err)
	}
}

// Review 4: in memory, a copy of a commit holds its version as in S3. With
// the original forgotten and an unlocked copy left, the history has its head
// at version 1, the next version is 2, version 1 is unlocked, nothing can be
// appended at 1 and the name cannot be made again.
func TestReview4ACopyHoldsItsVersionInMemory(t *testing.T) {
	ctx := context.Background()
	records, commits := store.NewMemory(), store.NewMemoryCommits()
	a := &store.Anchored{Records: records, Commits: commits, OnlyNewest: true, Strict: true}
	if err := a.CreateNew(ctx, rec("mailbox-1", "made")); err != nil {
		t.Fatal(err)
	}
	commits.Copy("mailbox-1", 1, false)
	commits.Forget("mailbox-1")
	h, err := commits.History(ctx, "mailbox-1")
	if err != nil || h.Head == nil || h.Head.Version != 1 || h.Next != 2 || fmt.Sprint(h.Unlocked) != "[1]" || len(h.Forks) != 0 {
		t.Fatalf("history %+v, %v", h, err)
	}
	if head, next, err := commits.Head(ctx, "mailbox-1"); err != nil || head == nil || head.Version != 1 || next != 2 {
		t.Fatalf("head %+v, %d, %v", head, next, err)
	}
	if _, err := commits.Append(ctx, commitOf("mailbox-1", 1, "other")); !errors.Is(err, store.ErrExists) {
		t.Fatalf("appended over a copy: %v", err)
	}
	fresh := &store.Anchored{Records: records, Commits: commits, OnlyNewest: true, Strict: true}
	if err := fresh.CreateNew(ctx, rec("mailbox-1", "again")); !errors.Is(err, store.ErrExists) {
		t.Fatalf("made again over a copy: %v", err)
	}
	if got, err := fresh.GetRecord(ctx, "mailbox-1"); err != nil || string(got.Value) != "made" {
		t.Fatalf("read %+v, %v", got, err)
	}
}

// stuckListing is a bucket whose listing says there is more and never moves
// on.
type stuckListing struct {
	*recordingBucket
	lists int
}

func (s *stuckListing) ListObjectVersions(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	s.lists++
	if s.lists > 1000 {
		return nil, errors.New("listed a thousand times")
	}
	return &s3.ListObjectVersionsOutput{IsTruncated: aws.Bool(true), NextKeyMarker: aws.String("k"), NextVersionIdMarker: aws.String("v")}, nil
}

// Review 5: what a history lists is bounded, whatever it is. 11,000 keys
// that are not versions, or 11,000 delete markers, under a record's prefix
// fail the history (and leave Head as it was); so do more pages than the
// bound, and a listing whose next marker does not move.
func TestReview5TheHistoryListsABoundedNumberOfEntries(t *testing.T) {
	ctx := context.Background()
	for _, what := range []string{"keys that are not versions", "delete markers", "pages of one entry"} {
		t.Run(what, func(t *testing.T) {
			w := newStrictWorld()
			c := w.commits()
			if _, err := c.Append(ctx, commitOf("mailbox-1", 1, "one")); err != nil {
				t.Fatal(err)
			}
			prefix := strings.TrimSuffix(w.last().key, "00000000000000000001")
			switch what {
			case "keys that are not versions":
				for i := range 11000 {
					w.b.add(bucketVersion{key: fmt.Sprintf("%sjunk-%d", prefix, i), body: []byte("junk")})
				}
			case "delete markers":
				for range 11000 {
					w.b.hide(prefix + "00000000000000000002")
				}
			case "pages of one entry":
				for i := range 150 {
					w.b.add(bucketVersion{key: fmt.Sprintf("%sjunk-%d", prefix, i), body: []byte("junk")})
				}
				w.b.pageSize = 1
			}
			if h, err := c.History(ctx, "mailbox-1"); err == nil || errors.Is(err, store.ErrForked) || !strings.Contains(err.Error(), "too long") {
				t.Fatalf("history %+v, %v", h, err)
			}
			if what != "pages of one entry" {
				if head, _, err := c.Head(ctx, "mailbox-1"); err != nil || head == nil || head.Version != 1 {
					t.Fatalf("Head as before: %+v, %v", head, err)
				}
			}
		})
	}
	t.Run("a listing that does not move", func(t *testing.T) {
		w := newStrictWorld()
		stuck := &stuckListing{recordingBucket: w.b}
		c := w.commits()
		c.S3 = stuck
		if _, err := c.History(ctx, "mailbox-1"); err == nil || stuck.lists > 2 {
			t.Fatalf("%d listings, %v", stuck.lists, err)
		}
	})
}

// Review A: a strict read never takes a value under the record's plain name.
// A record is made and removed, both commits vanish, and its old value is
// put back under its plain name: an enclave that starts reads ErrRolledBack
// (not ErrNotFound, so nobody makes the record over it without noticing),
// and CreateNew answers ErrExists.
func TestReviewAAStrictReadDoesNotAdoptAPlainValue(t *testing.T) {
	ctx := context.Background()
	records, commits := store.NewMemory(), store.NewMemoryCommits()
	a := &store.Anchored{Records: records, Commits: commits, OnlyNewest: true, Strict: true}
	if err := a.CreateNew(ctx, rec("mailbox-1", "old")); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveRecord(ctx, "mailbox-1"); err != nil {
		t.Fatal(err)
	}
	commits.Forget("mailbox-1")
	commits.Forget("mailbox-1")
	records.SetRecord(*rec("mailbox-1", "old"))
	b := &store.Anchored{Records: records, Commits: commits, OnlyNewest: true, Strict: true}
	if got, err := b.GetRecord(ctx, "mailbox-1"); !errors.Is(err, store.ErrRolledBack) || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("read %+v, %v", got, err)
	}
	if _, _, err := b.GetRecordAt(ctx, "mailbox-1"); !errors.Is(err, store.ErrRolledBack) {
		t.Fatalf("read at: %v", err)
	}
	if err := b.CreateNew(ctx, rec("mailbox-1", "new")); !errors.Is(err, store.ErrExists) {
		t.Fatalf("made over it: %v", err)
	}
	if h, _ := commits.History(ctx, "mailbox-1"); h.Head != nil {
		t.Fatalf("committed %+v", h.Head)
	}
	// A record that is not strict still adopts it, as before.
	loose := &store.Anchored{Records: records, Commits: store.NewMemoryCommits()}
	if got, err := loose.GetRecord(ctx, "mailbox-1"); err != nil || string(got.Value) != "old" {
		t.Fatalf("not strict: %+v, %v", got, err)
	}
}

// Review B: the errors of a strict record do not hold its name, which holds
// identifiers, and still match what they are.
func TestReviewBStrictErrorsHoldNoName(t *testing.T) {
	ctx := context.Background()
	const name = "mbx_secretsecretsecretsecretse"
	clean := func(what string, err error, want error) {
		t.Helper()
		if err == nil || strings.Contains(err.Error(), name) || strings.Contains(err.Error(), "secret") || (want != nil && !errors.Is(err, want)) {
			t.Fatalf("%s: %v", what, err)
		}
	}
	records, commits := store.NewMemory(), store.NewMemoryCommits()
	a := &store.Anchored{Records: records, Commits: commits, Strict: true}
	if err := a.CreateNew(ctx, rec(name, "one")); err != nil {
		t.Fatal(err)
	}
	if err := a.PutRecord(ctx, rec(name, "two"), false); err != nil {
		t.Fatal(err)
	}
	commits.Forget(name)
	_, err := a.GetRecord(ctx, name)
	clean("gone back", err, store.ErrRolledBack)
	commits.Fork(name, 1, sha256.Sum256([]byte("other")))
	_, err = a.GetRecord(ctx, name)
	clean("forked", err, store.ErrForked)

	blind := &store.Anchored{Records: records, Commits: failingCommits{}, Strict: true}
	_, err = blind.GetRecord(ctx, name)
	clean("no history", err, nil)

	down := errors.New("s3 unreachable")
	w := newStrictWorld()
	w.b.failList = down
	_, err = (&store.Anchored{Records: w.records, Commits: w.commits(), Strict: true}).GetRecord(ctx, name)
	clean("a history that fails", err, down)

	plain := store.NewMemory()
	plain.SetRecord(*rec(name, "from before"))
	_, err = (&store.Anchored{Records: plain, Commits: store.NewMemoryCommits(), Strict: true}).GetRecord(ctx, name)
	clean("a plain value", err, store.ErrRolledBack)

	taking := &hookedCommits{MemoryCommits: store.NewMemoryCommits()}
	taking.append = func(store.Commit) (bool, error) { return true, store.ErrExists }
	err = (&store.Anchored{Records: store.NewMemory(), Commits: taking, Strict: true}).CreateNew(ctx, rec(name, "one"))
	clean("keeps changing", err, nil)
}
