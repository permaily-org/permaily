package connections

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/store"
)

// V65, after the second review of PR #152: while the account is closing or
// being removed, no path removes a connection that cannot be read (its
// families are known only from its record); a removal retried after a
// partial failure adds no commit to the epochs it removes; and the sweep
// says when it could not tell whether an index entry's list was removed.

// lose deletes every stored value of connection id, as whoever writes the
// table can: its commits stay, and it reads as lost.
func lose(t *testing.T, w *world, id string) {
	t.Helper()
	ctx := context.Background()
	for _, name := range w.mem.RecordNames() {
		if strings.HasPrefix(name, connName(id)+"@") {
			if err := w.mem.DeleteRecord(ctx, name); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, _, err := w.enclave().readConn(ctx, id); !errors.Is(err, store.ErrLost) {
		t.Fatalf("the connection is not lost: %v", err)
	}
}

// stillFound checks that the list names connection id and that what is
// stored of family is still there: what finds the families of a lost
// connection is kept.
func stillFound(t *testing.T, w *world, id, family string) {
	t.Helper()
	ctx := context.Background()
	l, _, err := w.enclave().readList(ctx, accountA)
	if err != nil || l == nil {
		t.Fatalf("the list went: %+v %v", l, err)
	}
	named := false
	for _, m := range l.Members {
		named = named || m.ID == id
	}
	if !named {
		t.Fatal("the list no longer names the lost connection: its families are found by nothing")
	}
	stored := false
	for _, name := range w.mem.RecordNames() {
		stored = stored || strings.HasPrefix(name, RecordFamily+family+"-")
	}
	if !stored {
		t.Fatal("the lost connection's family went with nothing to say it did")
	}
}

// TestV65TheSweepKeepsALostConnectionOfAClosingList: the hourly pass, 30
// days after a connection of a closing list ended, finds it lost; it
// fails, and keeps the connection in the list, so the account's removal
// stays blocked rather than certify its families gone (review 2,
// finding 1).
func TestV65TheSweepKeepsALostConnectionOfAClosingList(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a := w.enclave()
	p := w.person(accountA)
	w.connect(a, p, w.ask(otherApp))
	ended := w.connectionOf(a, accountA, otherApp)
	if _, err := a.Revoke(ctx, p, ended.ID); err != nil {
		t.Fatal(err)
	}
	w.connect(a, p, w.ask(claudeCode))
	if err := a.EndAll(ctx, accountA); err != nil {
		t.Fatal(err)
	}
	lose(t, w, ended.ID)
	w.clock.add(EndedKept + time.Hour)
	if err := w.enclave().Sweep(ctx); !errors.Is(err, store.ErrLost) {
		t.Fatalf("the sweep passed over a lost connection of a closing list: %v", err)
	}
	stillFound(t, w, ended.ID, ended.Devices[0].ID)
	w.accts.set(accountA, StandingGone)
	if err := w.enclave().RemoveAll(ctx, accountA); !errors.Is(err, store.ErrLost) {
		t.Fatalf("the removal went on after the sweep: %v", err)
	}
	stillFound(t, w, ended.ID, ended.Devices[0].ID)
}

// TestV65TrimmingAClosingListKeepsALostConnection: a closing list past
// ListEndedMax drops its oldest ended members; one that cannot be read is
// kept, and the next oldest goes in its place, so the list stays within
// its bounds, EndAll and a cancellation still work, and the removal stays
// blocked (review 2, finding 1).
func TestV65TrimmingAClosingListKeepsALostConnection(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a := w.enclave()
	p := w.person(accountA)
	w.connect(a, p, w.ask(otherApp))
	oldest := w.connectionOf(a, accountA, otherApp)
	if _, err := a.Revoke(ctx, p, oldest.ID); err != nil {
		t.Fatal(err)
	}
	w.clock.add(time.Minute)
	w.connect(a, p, w.ask(claudeCode))
	// Ended members up to ListEndedMax, all later than the oldest: their
	// records were never made, so each removes as one that is gone.
	var filler []string
	if _, err := a.updateList(ctx, accountA, func(l *ListState) error {
		for i := range ListEndedMax - 1 {
			id, err := a.newID("con")
			if err != nil {
				return err
			}
			filler = append(filler, id)
			at := w.clock.now().Add(time.Duration(i) * time.Second)
			l.Members = append(l.Members, ListMember{ID: id, ClientID: "filler", State: contracts.ConnectionEnded, MadeAt: at, EndedAt: at})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	lose(t, w, oldest.ID)
	w.clock.add(time.Hour)
	if err := w.enclave().EndAll(ctx, accountA); err != nil {
		t.Fatalf("a lost connection stopped the fence: %v", err)
	}
	stillFound(t, w, oldest.ID, oldest.Devices[0].ID)
	l, _, err := w.enclave().readList(ctx, accountA)
	if err != nil || !l.Fits() {
		t.Fatalf("the list is past its bounds: %v", err)
	}
	for _, m := range l.Members {
		if m.ID == filler[0] {
			t.Fatal("the next oldest ended member was not dropped in the lost one's place")
		}
	}
	w.accts.set(accountA, StandingGone)
	if err := w.enclave().RemoveAll(ctx, accountA); !errors.Is(err, store.ErrLost) {
		t.Fatalf("the removal went on past the lost connection: %v", err)
	}
	stillFound(t, w, oldest.ID, oldest.Devices[0].ID)
}

// TestV65ASweepReportsAFailedRemovalCommitRead: the pass that finds no
// list for an index entry reads epoch 0 again to tell a removed list from
// one never made; that read failing fails the pass, and the entry stays
// for the next, which deletes it (review 2, finding 4).
func TestV65ASweepReportsAFailedRemovalCommitRead(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a := w.enclave()
	if err := a.EndAll(ctx, accountA); err != nil {
		t.Fatal(err)
	}
	index, err := w.mem.GetEntry(ctx, partitionAccounts, accountA)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveAll(ctx, accountA); err != nil {
		t.Fatal(err)
	}
	w.mem.SetEntry(*index)
	b, h := w.hooked()
	reads := 0
	down := errors.New("the anchors are down at the second read")
	h.set(func(op, name string) error {
		if op == "get" && name == ListName(accountA, 0) {
			if reads++; reads == 2 {
				return down
			}
		}
		return nil
	})
	err = b.Sweep(ctx)
	h.set(nil)
	if reads != 2 {
		t.Fatalf("%d reads of epoch 0, not 2", reads)
	}
	if !errors.Is(err, down) {
		t.Fatalf("the sweep hid the failed read: %v", err)
	}
	if _, err := w.mem.GetEntry(ctx, partitionAccounts, accountA); err != nil {
		t.Fatalf("the entry went on a pass that could not read: %v", err)
	}
	if err := b.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := w.mem.GetEntry(ctx, partitionAccounts, accountA); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the next pass left the entry: %v", err)
	}
}

// storedThenFailing are the world's commits, failing after storing every
// commit of removal of a name that fail says: the commit landed, and its
// caller is told it did not, at every try.
type storedThenFailing struct {
	*store.MemoryCommits
	mu   sync.Mutex
	fail func(name string) bool
}

func (c *storedThenFailing) Append(ctx context.Context, cm store.Commit) (*store.Commit, error) {
	stored, err := c.MemoryCommits.Append(ctx, cm)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil && stored.Gone() && c.fail != nil && c.fail(cm.Name) {
		return nil, errCrash
	}
	return stored, err
}

// TestV65RetriesAddNoFence: a removal interrupted again and again, at the
// fence (its commit stored, then an error), at the removal of an older
// epoch, or at the deletion of a value after its removal committed, of the
// list and of a family, leaves one commit more than the epochs it had, the
// fence, after every try and once a try succeeds (review 2, finding 3).
func TestV65RetriesAddNoFence(t *testing.T) {
	type target struct {
		name   func(r *removalWorld) func(uint64) string
		epochs uint64 // with a value, before the removal
	}
	targets := map[string]target{
		"the list": {func(*removalWorld) func(uint64) string {
			return func(e uint64) string { return ListName(accountA, e) }
		}, 3},
		"a family": {func(r *removalWorld) func(uint64) string {
			return func(e uint64) string { return familyName(r.fams[1], e) }
		}, 2},
	}
	faults := []string{"the fence, its commit stored", "an older epoch's removal", "a value's deletion after the commit"}
	for what, tg := range targets {
		for _, fault := range faults {
			t.Run(what+", "+fault, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				r := newRemovalWorld(t)
				name := tg.name(r)
				commits := &storedThenFailing{MemoryCommits: r.commits}
				if fault == "the fence, its commit stored" {
					// Every name past the epochs: a fence, and any added after it.
					commits.fail = func(n string) bool {
						for e := range uint64(8) {
							if n == name(tg.epochs+e) {
								return true
							}
						}
						return false
					}
				}
				r.mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
					if fault == "a value's deletion after the commit" && step == store.StepRecord && strings.HasPrefix(key, "record#"+name(0)+"@") {
						if head, _, err := r.commits.Head(ctx, name(0)); err == nil && head != nil && head.Gone() {
							return errCrash
						}
					}
					return nil
				}
				count := func() int {
					t.Helper()
					heads, err := r.enclave().epochHeads(ctx, name)
					if err != nil {
						t.Fatal(err)
					}
					return len(heads)
				}
				r.accts.set(accountA, StandingClosing)
				if err := r.enclave().EndAll(ctx, accountA); err != nil {
					t.Fatal(err)
				}
				r.accts.set(accountA, StandingGone)
				for try := 1; try <= 6; try++ {
					s, err := New(r.config(&store.Anchored{Records: r.mem, Commits: commits, Strict: true, Now: r.clock.now}))
					if err != nil {
						t.Fatal(err)
					}
					h := &failing{Records: s.cfg.Records}
					s.cfg.Records = h
					if fault == "an older epoch's removal" {
						h.set(func(op, n string) error {
							if op == "remove" && n == name(0) {
								return errCrash
							}
							return nil
						})
					}
					err = s.RemoveAll(ctx, accountA)
					if n := count(); n != int(tg.epochs)+1 {
						t.Fatalf("try %d: %d names committed for %d epochs and one fence", try, n, tg.epochs)
					}
					if err == nil {
						break
					}
					if !errors.Is(err, errCrash) {
						t.Fatalf("try %d: %v", try, err)
					}
				}
				r.mem.Interrupt = nil
				commits.mu.Lock()
				commits.fail = nil
				commits.mu.Unlock()
				if err := r.enclave().RemoveAll(ctx, accountA); err != nil {
					t.Fatal(err)
				}
				if n := count(); n != int(tg.epochs)+1 {
					t.Fatalf("%d names committed once the removal succeeded, for %d epochs and one fence", n, tg.epochs)
				}
				r.nothingLeft(t)
			})
		}
	}
}
