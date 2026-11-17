package connections

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/store"
)

// Only the current epoch of an account's list keeps content (ADR 0030,
// "Changed after the owner's decision on old epochs (2026-11-17)"; the
// owner's decision 2 of 2026-11-17): once a rotation's successor is
// committed, the epoch it closed is removed (RemoveRecord), its removal's
// commit staying as the fence, so an earlier copy put back never reads and
// its name is never made again. A rotation stopped at any of its writes is
// finished by the next writer, the sweep or a fresh enclave.

// epochsStored are, by epoch, how many stored versions of account's list
// whoever runs the storage sees.
func epochsStored(w *world, account string) map[uint64]int {
	out := map[uint64]int{}
	prefix := RecordList + account + "-"
	for _, name := range w.mem.RecordNames() {
		rest, ok := strings.CutPrefix(name, prefix)
		if !ok {
			continue
		}
		n, _, _ := strings.Cut(rest, "@")
		if e, err := strconv.ParseUint(n, 10, 64); err == nil {
			out[e]++
		}
	}
	return out
}

// onlyCurrent fails unless the only epoch of account's list with a stored
// value is current, and every earlier one is removed with its fence (a
// removal's commit after its value: version above 1).
func onlyCurrent(t *testing.T, w *world, account string, current uint64) {
	t.Helper()
	for e, n := range epochsStored(w, account) {
		if e != current {
			t.Fatalf("epoch %d keeps %d stored versions; only epoch %d should", e, n, current)
		}
	}
	a := &store.Anchored{Records: w.mem, Commits: w.commits, Strict: true, Now: w.clock.now}
	for e := range current {
		_, at, err := a.GetRecordAt(context.Background(), ListName(account, e))
		if !errors.Is(err, store.ErrNotFound) || at.Version() < 2 {
			t.Fatalf("epoch %d is not removed with its fence: version %d, %v", e, at.Version(), err)
		}
	}
}

// rotateTo writes account's list through s until its epoch is epoch.
func rotateTo(t *testing.T, s *Service, account string, epoch uint64) {
	t.Helper()
	ctx := context.Background()
	for {
		l, _, err := s.readList(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		if l.Epoch >= epoch {
			return
		}
		if _, err := s.updateList(ctx, account, func(*ListState) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
}

// toTheEdge writes account's list through s until its next write rotates
// it, and answers the list and its mark as read.
func toTheEdge(t *testing.T, s *Service, account string) (*ListState, store.Mark) {
	t.Helper()
	ctx := context.Background()
	for {
		l, at, err := s.readList(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		if l.Rotate(at.Version()) {
			return l, at
		}
		if _, err := s.updateList(ctx, account, func(*ListState) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
}

// V66: after each rotation no previous epoch keeps content, only its
// fence; the current one carries everything; an earlier epoch put back by
// whoever runs the storage never reads, and its name is never made again;
// the list works on through a fresh enclave and one that remembers an older
// epoch; the sweep keeps the account's index entry.
func TestV66NoPreviousEpochKeepsContentAfterRotation(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	old := w.enclave() // it reads epoch 0 and remembers it
	p := w.person(accountA)
	w.connect(s, p, w.ask(claudeCode))
	if _, err := s.updateList(ctx, accountA, func(l *ListState) error { return l.Decide("a-decision", w.clock.now()) }); err != nil {
		t.Fatal(err)
	}
	if l, _, err := old.readList(ctx, accountA); err != nil || l.Epoch != 0 {
		t.Fatal(l, err)
	}
	kept := map[string]store.Record{}
	for _, name := range w.mem.RecordNames() {
		if strings.HasPrefix(name, ListName(accountA, 0)+"@") {
			rec, _ := w.mem.GetRecord(ctx, name)
			kept[name] = *rec
		}
	}
	for e := uint64(1); e <= 3; e++ {
		rotateTo(t, s, accountA, e)
		onlyCurrent(t, w, accountA, e)
	}
	// Whoever runs the storage puts epoch 0 back.
	for _, rec := range kept {
		if err := w.mem.PutRecord(ctx, &rec, false); err != nil {
			t.Fatal(err)
		}
	}
	for name, sv := range map[string]*Service{"a fresh enclave": w.enclave(), "one that remembers epoch 0": old, "the writer": s} {
		l, _, err := sv.readList(ctx, accountA)
		if err != nil || l.Epoch != 3 || len(l.Members) != 1 || len(l.Decided) != 2 || l.Current[claudeCode] == "" {
			t.Fatalf("%s reads %+v, %v", name, l, err)
		}
		if conns, err := sv.List(ctx, p); err != nil || len(conns.Connections) != 1 {
			t.Fatalf("%s lists %+v, %v", name, conns, err)
		}
	}
	if err := s.writeSigned(ctx, ListName(accountA, 0), &ListState{V: 1, Account: accountA}, store.Mark{}, true); !errors.Is(err, store.ErrExists) {
		t.Fatalf("an earlier epoch's name was made again: %v", err)
	}
	// The list goes on: a write through the enclave that remembered epoch 0.
	if _, err := old.updateList(ctx, accountA, func(l *ListState) error { return l.Decide("another", w.clock.now()) }); err != nil {
		t.Fatal(err)
	}
	// The sweep keeps a live account whose epoch 0 is removed.
	if err := w.enclave().Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := w.mem.GetEntry(ctx, partitionAccounts, accountA); err != nil {
		t.Fatalf("the sweep dropped a live account's index entry: %v", err)
	}
	// A pass that cannot read the current epoch (whoever runs the storage
	// deleted its values: lost) keeps the entry too, though epoch 0 is
	// removed.
	for _, name := range w.mem.RecordNames() {
		if strings.HasPrefix(name, ListName(accountA, 3)+"@") {
			if err := w.mem.DeleteRecord(ctx, name); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.enclave().Sweep(ctx); err == nil {
		t.Fatal("a pass that could not read a list succeeded")
	}
	if _, err := w.mem.GetEntry(ctx, partitionAccounts, accountA); err != nil {
		t.Fatalf("a failed read dropped a live account's index entry: %v", err)
	}
}

// V67: a rotation stopped at each of its writes (the closed epoch's
// write, the successor's creation, the closed epoch's removal) is finished
// by the next writer, through the same enclave or the other, or by the
// sweep: no previous epoch keeps content, and nothing is lost.
func TestV67ARotationInterruptedAtEachWrite(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		op     string
		epoch  uint64
		finish string // "writer", "other", "sweep"
	}{
		{"its write moved, by the writer", "put", 0, "writer"},
		{"the successor's creation, by the other enclave", "create", 1, "other"},
		{"the successor's creation, by the sweep", "create", 1, "sweep"},
		{"the closed epoch's removal, by the writer", "remove", 0, "writer"},
		{"the closed epoch's removal, by the other enclave", "remove", 0, "other"},
		{"the closed epoch's removal, by the sweep", "remove", 0, "sweep"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			s, f := w.hooked()
			other := w.enclave()
			p := w.person(accountA)
			w.connect(s, p, w.ask(claudeCode))
			l, at := toTheEdge(t, s, accountA)
			if _, _, err := other.readList(ctx, accountA); err != nil { // it remembers epoch 0
				t.Fatal(err)
			}
			f.set(func(op, name string) error {
				if op == c.op && name == ListName(accountA, c.epoch) {
					return errCrash
				}
				return nil
			})
			if err := s.writeList(ctx, l, at, false); !errors.Is(err, errCrash) && c.op != "remove" {
				t.Fatalf("the rotation was not stopped: %v", err)
			}
			f.set(nil)
			switch c.finish {
			case "writer":
				if _, err := s.updateList(ctx, accountA, func(l *ListState) error { return l.Decide("next", w.clock.now()) }); err != nil {
					t.Fatal(err)
				}
			case "other":
				if _, err := other.updateList(ctx, accountA, func(l *ListState) error { return l.Decide("next", w.clock.now()) }); err != nil {
					t.Fatal(err)
				}
			case "sweep":
				if err := other.Sweep(ctx); err != nil {
					t.Fatal(err)
				}
			}
			n, _, err := w.enclave().readList(ctx, accountA)
			if err != nil || len(n.Members) != 1 || n.Current[claudeCode] == "" {
				t.Fatalf("%+v %v", n, err)
			}
			onlyCurrent(t, w, accountA, n.Epoch)
			if c.op != "put" && n.Epoch != 1 {
				t.Fatalf("epoch %d", n.Epoch)
			}
		})
	}
}

// V68: a fresh enclave, after a rotation stopped before its successor was
// made or before the closed epoch was removed, reads the list whole and
// finishes the rotation: a stop before the successor was made is finished
// by its read alone, which makes the successor and removes the closed epoch
// (checked right after the read); a stop before the removal, by its first
// write.
func TestV68AFreshEnclaveFinishesARotation(t *testing.T) {
	ctx := context.Background()
	for _, op := range []string{"create", "remove"} {
		t.Run(op, func(t *testing.T) {
			w := newWorld(t)
			s, f := w.hooked()
			w.connect(s, w.person(accountA), w.ask(claudeCode))
			l, at := toTheEdge(t, s, accountA)
			f.set(func(o, name string) error {
				if o == op && (name == ListName(accountA, 1) || name == ListName(accountA, 0)) {
					return errCrash
				}
				return nil
			})
			_ = s.writeList(ctx, l, at, false)
			// The enclave stops: a fresh one takes over.
			fresh := w.enclave()
			n, _, err := fresh.readList(ctx, accountA)
			if err != nil || n.Epoch != 1 || len(n.Members) != 1 {
				t.Fatalf("%+v %v", n, err)
			}
			if op == "create" {
				// The reader that made the successor removed the closed
				// epoch itself, before any write (remakeEpoch).
				onlyCurrent(t, w, accountA, 1)
			}
			if _, err := fresh.updateList(ctx, accountA, func(l *ListState) error { return l.Decide("next", w.clock.now()) }); err != nil {
				t.Fatal(err)
			}
			onlyCurrent(t, w, accountA, 1)
		})
	}
}

// V69: removing an account after several rotations, one of them stopped
// before its closed epoch was removed, fences the whole list first (its
// earlier epochs' fences are not a removal under way), removes every epoch
// with a fence after the last, and nothing of it reads or is made again,
// through a fresh enclave and one that remembers the newest epoch; the
// sweep then drops the index entry put back.
func TestV69AccountRemovalAfterSeveralRotations(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s, f := w.hooked()
	p := w.person(accountA)
	x := w.ask(claudeCode)
	tok, _ := w.connect(s, p, x)
	rotateTo(t, s, accountA, 3)
	l, at := toTheEdge(t, s, accountA)
	f.set(func(op, name string) error {
		if op == "remove" && name == ListName(accountA, 3) {
			return errCrash
		}
		return nil
	})
	_ = s.writeList(ctx, l, at, false)
	f.set(nil)
	if got := epochsStored(w, accountA); got[3] == 0 || got[4] == 0 {
		t.Fatalf("the world does not have a stopped rotation: %v", got)
	}
	remembers := w.enclave()
	if l, _, err := remembers.readList(ctx, accountA); err != nil || l.Epoch != 4 {
		t.Fatal(l, err)
	}
	index, err := w.mem.GetEntry(ctx, partitionAccounts, accountA)
	if err != nil {
		t.Fatal(err)
	}
	// RemoveAll alone, as accounts calls it: the list is fenced and ended
	// first, so one that cannot write the list fails (the removed epochs
	// before it are not a removal under way).
	w.accts.set(accountA, StandingGone)
	f.set(func(op, name string) error {
		if op == "put" && strings.HasPrefix(name, RecordList) {
			return errCrash
		}
		return nil
	})
	if err := s.RemoveAll(ctx, accountA); !errors.Is(err, errCrash) {
		t.Fatalf("a removal that could not fence the list: %v", err)
	}
	f.set(nil)
	if err := w.enclave().RemoveAll(ctx, accountA); err != nil {
		t.Fatal(err)
	}
	if got := epochsStored(w, accountA); len(got) != 0 {
		t.Fatalf("epochs stored after the removal: %v", got)
	}
	a := &store.Anchored{Records: w.mem, Commits: w.commits, Strict: true, Now: w.clock.now}
	if _, at, err := a.GetRecordAt(ctx, ListName(accountA, 5)); !errors.Is(err, store.ErrNotFound) || at.Version() != 1 {
		t.Fatalf("no fence after the last epoch: %d %v", at.Version(), err)
	}
	for name, sv := range map[string]*Service{"a fresh enclave": w.enclave(), "one that remembers epoch 4": remembers} {
		if l, _, err := sv.readList(ctx, accountA); err == nil && l != nil {
			t.Fatalf("%s reads the removed list: %+v", name, l)
		}
		if _, err := w.check(sv, x, tok.AccessToken); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("%s takes the removed account's token: %v", name, err)
		}
		if err := sv.EndAll(ctx, accountA); err == nil {
			t.Fatalf("%s made the removed list again", name)
		}
	}
	if err := w.enclave().RemoveAll(ctx, accountA); err != nil {
		t.Fatal(err)
	}
	w.mem.SetEntry(*index)
	if err := w.enclave().Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := w.mem.GetEntry(ctx, partitionAccounts, accountA); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the sweep kept a removed account's index entry: %v", err)
	}
}

// V67: the closed epoch's removal whose commit landed and whose values
// stayed (the commit stored, then an error) is finished by the next write
// of the successor, as a removal of the account finishes one.
func TestV67AClosedEpochsRemovalWhoseCommitLandedIsFinished(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	late := &lateCommits{MemoryCommits: w.commits}
	s, err := New(w.config(&store.Anchored{Records: w.mem, Commits: late, Strict: true, Now: w.clock.now}))
	if err != nil {
		t.Fatal(err)
	}
	w.connect(s, w.person(accountA), w.ask(claudeCode))
	l, at := toTheEdge(t, s, accountA)
	late.mu.Lock()
	late.fail = ListName(accountA, 0)
	late.mu.Unlock()
	_ = s.writeList(ctx, l, at, false)
	late.mu.Lock()
	late.fail = ""
	late.mu.Unlock()
	if epochsStored(w, accountA)[0] == 0 {
		t.Fatal("the fault left nothing to finish")
	}
	if _, err := w.enclave().updateList(ctx, accountA, func(l *ListState) error { return l.Decide("next", w.clock.now()) }); err != nil {
		t.Fatal(err)
	}
	onlyCurrent(t, w, accountA, 1)
}
