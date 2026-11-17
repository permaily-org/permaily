package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/store"
)

// V65, after the third review of PR #152, finding 1: a write's time
// (WriteWithin) starts before its read of the record's commits, so a cold
// read of a long, honest history that outlasts the account's last pass
// stores nothing.

// slowClock is the writer's clock, which the bucket moves on.
type slowClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *slowClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *slowClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// coldBucket is the bucket a cold reader sees: listed runs once, after the
// first listing; the object reads go 16 at a time, and each batch takes 80
// seconds (a try cut at the client's 60 seconds and one that answers, within
// the SDK's tries), on the writer's clock, with no sleep. final runs once the
// batches took RemoveAgainAfter.
type coldBucket struct {
	store.S3CommitsAPI
	clock   *slowClock
	listed  func()
	final   func()
	mu      sync.Mutex
	batch   []chan struct{}
	count   int
	total   int
	elapsed time.Duration
}

func (b *coldBucket) ListObjectVersions(ctx context.Context, in *s3.ListObjectVersionsInput, opts ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	out, err := b.S3CommitsAPI.ListObjectVersions(ctx, in, opts...)
	b.mu.Lock()
	f := b.listed
	b.listed = nil
	b.mu.Unlock()
	if err == nil && f != nil {
		f()
	}
	return out, err
}

func (b *coldBucket) GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	done := make(chan struct{})
	b.mu.Lock()
	b.count++
	b.batch = append(b.batch, done)
	if len(b.batch) == 16 || b.count == b.total {
		b.elapsed += 80 * time.Second
		b.clock.add(80 * time.Second)
		if b.elapsed >= connections.RemoveAgainAfter && b.final != nil {
			b.final()
			b.final = nil
		}
		for _, c := range b.batch {
			close(c)
		}
		b.batch = nil
	}
	b.mu.Unlock()
	<-done
	return b.S3CommitsAPI.GetObject(ctx, in, opts...)
}

// answerLost stores a value under target's name and answers an error.
type answerLost struct {
	store.RecordTable
	target string
}

func (a *answerLost) PutRecord(ctx context.Context, r *store.Record, create bool) error {
	if err := a.RecordTable.PutRecord(ctx, r, create); err != nil {
		return err
	}
	if strings.HasPrefix(r.Name, a.target+"@") {
		return errors.New("stored, its answer lost")
	}
	return nil
}

// TestV65AColdHistoryReadStagesNothingPastItsTime: a record of 399 honest
// commits (within the list's epoch); the writer read its mark, then other
// records' reads dropped its history from memory. Its PutRecordAt lists the
// versions, another enclave removes the record, and the writer's reads of
// the 399 versions take 33 minutes, past the account's last pass (the
// removal again, RemoveAgainAfter after the first). With connections'
// WriteMax the write stores nothing (ErrTooLate); without a bound it stores
// its value after both removals, which nothing would delete (review 3,
// finding 1).
func TestV65AColdHistoryReadStagesNothingPastItsTime(t *testing.T) {
	for what, bounded := range map[string]bool{"within WriteMax": true, "without a bound": false} {
		t.Run(what, func(t *testing.T) {
			ctx := context.Background()
			w := newStrictWorld()
			a := w.enclave()
			name := "connections-acc-test-0"
			if err := a.CreateNew(ctx, rec(name, "initial")); err != nil {
				t.Fatal(err)
			}
			for range 398 {
				_, at, err := a.GetRecordAt(ctx, name)
				if err != nil {
					t.Fatal(err)
				}
				if err := a.PutRecordAt(ctx, rec(name, "next"), at); err != nil {
					t.Fatal(err)
				}
			}
			clock := &slowClock{t: w.now}
			bkt := &coldBucket{S3CommitsAPI: w.b, clock: clock, total: 399}
			first, final := false, false
			bkt.listed = func() {
				if err := a.RemoveRecord(ctx, name); err != nil {
					t.Error(err)
				}
				first = true
			}
			bkt.final = func() {
				if err := a.RemoveRecord(ctx, name); err != nil {
					t.Error(err)
				}
				final = true
			}
			commits := w.commits()
			b := &store.Anchored{Records: &answerLost{RecordTable: w.records, target: name}, Commits: commits, Strict: true, OnlyNewest: true, Now: clock.now}
			_, mark, err := b.GetRecordAt(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			store.FillKnown(commits, store.MaxKnownObjects)
			commits.S3 = bkt
			wctx := ctx
			if bounded {
				wctx = store.WriteWithin(ctx, connections.WriteMax)
			}
			err = b.PutRecordAt(wctx, rec(name, "late"), mark)
			if err == nil || !first || !final {
				t.Fatalf("the schedule did not run: %v %v %v", err, first, final)
			}
			if bounded && (!errors.Is(err, store.ErrTooLate) || !errors.Is(err, context.DeadlineExceeded)) {
				t.Fatalf("the write did not run out of time: %v", err)
			}
			if _, _, err := w.enclave().GetRecordAt(ctx, name); !errors.Is(err, store.ErrNotFound) {
				t.Fatal("the removed record reads:", err)
			}
			staged := false
			for _, n := range w.records.RecordNames() {
				staged = staged || strings.HasPrefix(n, name+"@")
			}
			if staged == bounded {
				t.Fatalf("staged after both removals: %v, bounded: %v (%d reads, %v)", staged, bounded, bkt.count, bkt.elapsed)
			}
		})
	}
}

// TestWriteWithinEndsTheWriteItBounds: a write whose time ran out by the
// Anchored's clock, or whose context's deadline passed, before it staged,
// stores nothing; a zero bound bounds nothing, and one write's bound does
// not hold the next.
func TestWriteWithinEndsTheWriteItBounds(t *testing.T) {
	ctx := context.Background()
	clock := &slowClock{t: time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)}
	mem := store.NewMemory()
	moving := &movingCommits{MemoryCommits: store.NewMemoryCommits(), clock: clock}
	a := &store.Anchored{Records: mem, Commits: moving, Strict: true, Now: clock.now}
	if err := a.CreateNew(store.WriteWithin(ctx, time.Minute), rec("r", "v")); err != nil {
		t.Fatal(err)
	}
	before := len(mem.RecordNames())
	moving.by = time.Minute
	if err := a.PutRecord(store.WriteWithin(ctx, time.Minute), rec("r", "late"), false); !errors.Is(err, store.ErrTooLate) {
		t.Fatalf("PutRecord: %v", err)
	}
	_, at, err := a.GetRecordAt(ctx, "r")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.PutRecordAt(store.WriteWithin(ctx, time.Minute), rec("r", "late"), at); !errors.Is(err, store.ErrTooLate) {
		t.Fatalf("PutRecordAt: %v", err)
	}
	if err := a.CreateNew(store.WriteWithin(ctx, time.Minute), rec("s", "late")); !errors.Is(err, store.ErrTooLate) {
		t.Fatalf("CreateNew: %v", err)
	}
	if after := len(mem.RecordNames()); after != before {
		t.Fatalf("a late write staged a value: %d names, %d before", after, before)
	}
	if err := a.PutRecordAt(store.WriteWithin(ctx, 0), rec("r", "unbounded"), at); err != nil {
		t.Fatalf("a zero bound: %v", err)
	}
	moving.by = 0
	expired, cancel := context.WithDeadline(store.WriteWithin(ctx, time.Hour), time.Now().Add(-time.Second))
	defer cancel()
	if err := a.PutRecord(expired, rec("r", "past its deadline"), false); !errors.Is(err, store.ErrTooLate) {
		t.Fatalf("past the context's deadline: %v", err)
	}
	bounded := store.WriteWithin(ctx, time.Minute)
	clock.add(time.Hour)
	if err := a.PutRecord(bounded, rec("r", "a later write"), false); err != nil {
		t.Fatalf("a write's bound held the next: %v", err)
	}
}

// movingCommits moves the clock by by at each read of a history, as a slow
// read does.
type movingCommits struct {
	*store.MemoryCommits
	clock *slowClock
	by    time.Duration
}

func (m *movingCommits) History(ctx context.Context, name string) (*store.History, error) {
	m.clock.add(m.by)
	return m.MemoryCommits.History(ctx, name)
}
