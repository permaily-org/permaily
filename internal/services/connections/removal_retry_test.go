package connections

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/store"
)

// V65, after the review of PR #152: a removal is finished by the next call
// wherever it stopped, a commit of removal whose values stayed included;
// no epoch made by a writer racing the removal outlives it; a connection
// that cannot be read keeps what finds its families; and an entry of
// Sweep's index written after the removal is deleted by the next pass.

// lateCommits are the world's commits, failing after storing a commit of
// removal that fail names: the commit landed, and its caller is told it
// did not.
type lateCommits struct {
	*store.MemoryCommits
	mu   sync.Mutex
	fail string
}

func (c *lateCommits) Append(ctx context.Context, cm store.Commit) (*store.Commit, error) {
	stored, err := c.MemoryCommits.Append(ctx, cm)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil && cm.Name == c.fail && stored.Gone() {
		c.fail = ""
		return nil, errCrash
	}
	return stored, err
}

// TestV65ARemovalWhoseCommitLandedIsFinished: RemoveRecord stopped after
// its commit (listing the values, deleting one, or the commit stored and
// then an error) leaves values that the name's reads no longer find; the
// next RemoveAll, through another enclave, deletes them.
func TestV65ARemovalWhoseCommitLandedIsFinished(t *testing.T) {
	targets := map[string]func(r *removalWorld) string{
		"the list's newest epoch": func(r *removalWorld) string { return ListName(accountA, r.listEpochTop) },
		"a family's moved epoch":  func(r *removalWorld) string { return familyName(r.fams[1], 0) },
		"a family's newest epoch": func(r *removalWorld) string { return familyName(r.fams[1], 1) },
		"a connection":            func(r *removalWorld) string { return connName(r.conns[1]) },
	}
	faults := []string{"listing its values", "deleting a value", "the commit stored, then an error"}
	for what, target := range targets {
		for _, fault := range faults {
			t.Run(what+", "+fault, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				r := newRemovalWorld(t)
				name := target(r)
				late := &lateCommits{MemoryCommits: r.commits}
				s, err := New(r.config(&store.Anchored{Records: r.mem, Commits: late, Strict: true, Now: r.clock.now}))
				if err != nil {
					t.Fatal(err)
				}
				var mu sync.Mutex
				fired := false
				switch fault {
				case "the commit stored, then an error":
					late.mu.Lock()
					late.fail = name
					late.mu.Unlock()
				default:
					r.mem.Interrupt = func(ctx context.Context, step store.Step, _, key string) error {
						match := step == store.StepRead && key == "record#"+name+"@"
						if fault == "deleting a value" {
							match = step == store.StepRecord && strings.HasPrefix(key, "record#"+name+"@")
						}
						mu.Lock()
						defer mu.Unlock()
						if fired || !match {
							return nil
						}
						if head, _, err := r.commits.Head(ctx, name); err != nil || head == nil || !head.Gone() {
							return err
						}
						fired = true
						return errCrash
					}
				}
				if err := r.remove(s); !errors.Is(err, errCrash) {
					t.Fatalf("the fault was not reached: %v", err)
				}
				r.mem.Interrupt = nil
				if fault != "the commit stored, then an error" && !fired {
					t.Fatal("the fault was not reached")
				}
				if left := r.stored(); len(left) == 0 {
					t.Fatal("the fault left nothing to finish")
				}
				if err := r.enclave().RemoveAll(ctx, accountA); err != nil {
					t.Fatal(err)
				}
				r.nothingLeft(t)
			})
		}
	}
}

// TestV65ALostConnectionKeepsWhatFindsItsFamilies: a connection whose value
// whoever writes the table deleted cannot say which families it had, so
// the removal fails, and keeps the list and the index that find it, rather
// than certify that its families went.
func TestV65ALostConnectionKeepsWhatFindsItsFamilies(t *testing.T) {
	ctx := context.Background()
	r := newRemovalWorld(t)
	id := r.conns[1]
	for _, name := range r.mem.RecordNames() {
		if strings.HasPrefix(name, connName(id)+"@") {
			if err := r.mem.DeleteRecord(ctx, name); err != nil {
				t.Fatal(err)
			}
		}
	}
	for range 2 {
		if err := r.remove(r.enclave()); !errors.Is(err, store.ErrLost) {
			t.Fatalf("a removal certified a lost connection's families gone: %v", err)
		}
	}
	family := false
	for _, name := range r.mem.RecordNames() {
		family = family || strings.HasPrefix(name, RecordFamily+r.fams[1]+"-")
	}
	if !family {
		t.Fatal("the lost connection's families went with nothing left to find them by")
	}
	if l, _, err := r.enclave().readList(ctx, accountA); err != nil || l == nil {
		t.Fatalf("the list that names the lost connection went: %+v %v", l, err)
	}
	if _, err := r.mem.GetEntry(ctx, partitionAccounts, accountA); err != nil {
		t.Fatalf("the sweep's index lost the account: %v", err)
	}
}

// closingAtTheLastVersion is a world whose accountA has a connection and a
// closing list at the last version of its epoch 0: its next write rolls it.
func closingAtTheLastVersion(t *testing.T) (*world, *Service) {
	t.Helper()
	ctx := context.Background()
	w := newWorld(t)
	a := w.enclave()
	w.connect(a, w.person(accountA), w.ask(claudeCode))
	if err := a.EndAll(ctx, accountA); err != nil {
		t.Fatal(err)
	}
	for {
		_, at, err := a.readList(ctx, accountA)
		if err != nil {
			t.Fatal(err)
		}
		if at.Version() == ListEpochVersions-1 {
			return w, a
		}
		if _, err := a.updateList(ctx, accountA, func(*ListState) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
}

// nothingOfAccountA checks that no value of accountA's list, connections
// or families is stored or readable, and that the list cannot be made
// again.
func nothingOfAccountA(t *testing.T, w *world) {
	t.Helper()
	ctx := context.Background()
	for _, name := range w.mem.RecordNames() {
		if strings.HasPrefix(name, RecordList+accountA+"-") || strings.HasPrefix(name, RecordConnection) || strings.HasPrefix(name, RecordFamily) {
			t.Errorf("stored after the removal: %s", name)
		}
	}
	s := w.enclave()
	for e := range uint64(3) {
		var l ListState
		if _, found, err := s.readSigned(ctx, ListName(accountA, e), &l); err != nil || found {
			t.Errorf("epoch %d reads after the removal: %+v %v", e, l, err)
		}
	}
	if err := s.EndAll(ctx, accountA); err == nil {
		t.Error("the removed account's list was made again")
	}
}

// TestV65ARotationCannotMakeAnEpochAfterTheRemoval: an EndAll that rolls
// the list to its next epoch, racing a removal at each of its writes (the
// epoch written moved, the next one made, after it), leaves nothing of the
// list once the removal and its own RemoveAll returned.
func TestV65ARotationCannotMakeAnEpochAfterTheRemoval(t *testing.T) {
	for _, at := range []struct{ name, op, record string }{
		{"before the epoch is written moved", "put", ListName(accountA, 0)},
		{"before the next epoch is made", "create", ListName(accountA, 1)},
		{"after the next epoch is made", "get", ListName(accountA, 1)},
	} {
		t.Run(at.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			w, a := closingAtTheLastVersion(t)
			b, h := w.hooked()
			fired := false
			h.set(func(op, name string) error {
				if op == at.op && name == at.record && !fired {
					fired = true
					w.accts.set(accountA, StandingGone)
					if err := a.RemoveAll(ctx, accountA); err != nil {
						t.Error(err)
					}
				}
				return nil
			})
			_ = b.EndAll(ctx, accountA)
			h.set(nil)
			if !fired {
				t.Fatal("the rotation's write was not reached")
			}
			if err := b.RemoveAll(ctx, accountA); err != nil {
				t.Fatal(err)
			}
			nothingOfAccountA(t, w)
		})
	}
}

// TestV65AFamilyRotationCannotMakeAnEpochAfterTheRemoval: a refresh that
// rolls a family to its next epoch, racing the removal of the account
// before and after it writes the epoch moved, leaves no epoch of it.
func TestV65AFamilyRotationCannotMakeAnEpochAfterTheRemoval(t *testing.T) {
	for _, at := range []struct{ name, op string }{
		{"before the epoch is written moved", "put"},
		{"before the next epoch is made", "create"},
	} {
		t.Run(at.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			w := newWorld(t)
			a := w.enclave()
			y := w.ask(claudeCode)
			w.connect(a, w.person(accountA), y)
			c := w.connectionOf(a, accountA, claudeCode)
			fam := c.Devices[0].ID
			f, mark, _ := a.readFamily(ctx, fam, 0)
			f.Generation = oauth.FamilyEpochRotations - 1
			if err := a.writeSealed(ctx, familyName(fam, 0), f, mark, false); err != nil {
				t.Fatal(err)
			}
			last := oauth.FamilySecret(a.familyKey, oauth.KindRefresh, fam, f.Generation)
			if err := a.putEntry(ctx, PartitionRefresh+oauth.Hash(oauth.KindRefresh, last), refreshEntry{Connection: c.ID, Family: fam, Generation: f.Generation, Account: accountA, Expires: w.clock.now().Add(time.Hour)}, w.clock.now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			record := familyName(fam, 0)
			if at.op == "create" {
				record = familyName(fam, 1)
			}
			b, h := w.hooked()
			fired := false
			h.set(func(op, name string) error {
				if op == at.op && name == record && !fired {
					fired = true
					w.accts.set(accountA, StandingGone)
					if err := a.RemoveAll(ctx, accountA); err != nil {
						t.Error(err)
					}
				}
				return nil
			})
			if tok, err := w.refresh(b, y, last); err == nil {
				if _, err := w.enclave().Check(ctx, contracts.ResourceMCP, tok.AccessToken); !errors.Is(err, ErrInvalidToken) {
					t.Fatalf("a token issued across the removal works: %v", err)
				}
			}
			h.set(nil)
			if !fired {
				t.Fatal("the family's rotation was not reached")
			}
			if err := w.enclave().RemoveAll(ctx, accountA); err != nil {
				t.Fatal(err)
			}
			nothingOfAccountA(t, w)
		})
	}
}

// TestV65TwoRemovalsAtOnce: a second removal, run whole at each removal
// the first makes, leaves both succeeding and nothing of the account.
func TestV65TwoRemovalsAtOnce(t *testing.T) {
	ctx := context.Background()
	for stop := 1; ; stop++ {
		w := newWorld(t)
		a := w.enclave()
		x := w.ask(claudeCode)
		w.connect(a, w.person(accountA), x)
		if err := a.EndAll(ctx, accountA); err != nil {
			t.Fatal(err)
		}
		w.accts.set(accountA, StandingGone)
		b, h := w.hooked()
		n := 0
		h.set(func(op, name string) error {
			if op == "remove" {
				if n++; n == stop {
					if err := w.enclave().RemoveAll(ctx, accountA); err != nil {
						t.Errorf("the second removal at the first's removal %d: %v", stop, err)
					}
				}
			}
			return nil
		})
		if err := b.RemoveAll(ctx, accountA); err != nil {
			t.Fatalf("the first removal, the second run at its removal %d: %v", stop, err)
		}
		nothingOfAccountA(t, w)
		if n < stop {
			if stop < 4 {
				t.Fatalf("only %d removals", n)
			}
			return
		}
	}
}

// TestV65ALateIndexWriteIsSweptAway: a write of the list that committed
// before the removal writes the account in Sweep's index after it; the
// next pass deletes the entry, as it deletes one whoever writes the table
// puts back, also through an enclave that remembers the list's newest
// epoch.
func TestV65ALateIndexWriteIsSweptAway(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a := w.enclave()
	e := &hookedEntries{Entries: w.mem}
	b := w.enclave()
	b.cfg.Entries = e
	reached := false
	e.before = func(row *store.Entry) {
		if row.Partition == partitionAccounts && !reached {
			reached = true
			w.accts.set(accountA, StandingGone)
			if err := a.RemoveAll(ctx, accountA); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := b.EndAll(ctx, accountA); err != nil {
		t.Fatal(err)
	}
	if !reached {
		t.Fatal("the index's write was not reached")
	}
	if _, err := w.mem.GetEntry(ctx, partitionAccounts, accountA); err != nil {
		t.Fatal("the late write did not land:", err)
	}
	if err := w.enclave().Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := w.mem.GetEntry(ctx, partitionAccounts, accountA); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the index's entry survived the removal and the pass: %v", err)
	}

	r := newRemovalWorld(t)
	index, err := r.mem.GetEntry(ctx, partitionAccounts, accountA)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.remove(r.a); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Service{r.b, r.enclave()} {
		r.mem.SetEntry(*index)
		if err := s.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := r.mem.GetEntry(ctx, partitionAccounts, accountA); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("an entry put back survived the pass: %v", err)
		}
	}
}

// TestV65ARefreshFinishingAfterTheRemovalIsUnusable: a refresh that
// committed its family's change before the removal may answer a pair after
// it; neither token works.
func TestV65ARefreshFinishingAfterTheRemovalIsUnusable(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a, h := w.hooked()
	x := w.ask(claudeCode)
	tok, _ := w.connect(a, w.person(accountA), x)
	reached := false
	h.setAfter(func(_ context.Context, name string) error {
		if strings.HasPrefix(name, RecordFamily) && !reached {
			reached = true
			w.accts.set(accountA, StandingGone)
			if err := w.enclave().RemoveAll(ctx, accountA); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	})
	issued, err := w.refresh(a, x, tok.RefreshToken)
	if !reached || err != nil {
		t.Fatal(reached, err)
	}
	if _, err := w.enclave().Check(ctx, contracts.ResourceMCP, issued.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
	if _, err := w.refresh(w.enclave(), x, issued.RefreshToken); err == nil {
		t.Fatal("the refresh token answered across the removal works")
	}
}

// TestV65ReopenAfterTheRemovalCannotLiftTheFence: a cancellation racing
// the removal between its two writes finds the list gone.
func TestV65ReopenAfterTheRemovalCannotLiftTheFence(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a, h := w.hooked()
	if err := a.EndAll(ctx, accountA); err != nil {
		t.Fatal(err)
	}
	writes := 0
	h.set(func(op, name string) error {
		if op == "put" && name == ListName(accountA, 0) {
			writes++
			if writes == 2 {
				w.accts.set(accountA, StandingGone)
				if err := w.enclave().RemoveAll(ctx, accountA); err != nil {
					t.Fatal(err)
				}
			}
		}
		return nil
	})
	if err := a.Reopen(ctx, accountA); err == nil {
		t.Fatal("reopened after the removal")
	}
	if writes < 2 {
		t.Fatal("the barrier was not reached")
	}
	if l, _, err := w.enclave().readList(ctx, accountA); err != nil || l != nil {
		t.Fatal(l, err)
	}
}
