package connections

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/store"
)

// RemoveAll (ADR 0030, "Changed while releasing step 5"): removing an
// account removes every epoch of its list, moved ones included, its entry in
// Sweep's index, and every connection's and token family's record, every
// epoch of each; what stays is the commit of each removal, which keeps the
// old tokens dead and the list from being made again whatever of the
// removed records is put back.

// removalWorld is accountA with an ended connection, an active one whose
// family moved to its second epoch, a decided request and a list in its
// third epoch; accountB with a connection of its own.
type removalWorld struct {
	*world
	a, b         *Service
	tokens       []*TokenAnswer
	asks         []*ask
	other        *TokenAnswer
	otherAsk     *ask
	otherConn    string
	conns, fams  []string
	listEpochTop uint64
}

func newRemovalWorld(t *testing.T) *removalWorld {
	t.Helper()
	ctx := context.Background()
	w := newWorld(t)
	r := &removalWorld{world: w, a: w.enclave(), b: w.enclave()}
	p := w.person(accountA)

	// An ended connection.
	x := w.ask(otherApp)
	tok, _ := w.connect(r.a, p, x)
	ended := w.connectionOf(r.a, accountA, otherApp)
	if _, err := r.a.Revoke(ctx, p, ended.ID); err != nil {
		t.Fatal(err)
	}
	r.tokens, r.asks = append(r.tokens, tok), append(r.asks, x)
	r.conns, r.fams = append(r.conns, ended.ID), append(r.fams, ended.Devices[0].ID)

	// An active one, whose family moved to its next epoch (as V24 moves it).
	y := w.ask(claudeCode)
	w.connect(r.b, p, y)
	c := w.connectionOf(r.b, accountA, claudeCode)
	fam := c.Devices[0].ID
	f, at, _ := r.b.readFamily(ctx, fam, 0)
	f.Generation = oauth.FamilyEpochRotations - 1
	if err := r.b.writeSealed(ctx, familyName(fam, 0), f, at, false); err != nil {
		t.Fatal(err)
	}
	last := oauth.FamilySecret(r.b.familyKey, oauth.KindRefresh, fam, f.Generation)
	if err := r.b.putEntry(ctx, PartitionRefresh+oauth.Hash(oauth.KindRefresh, last), refreshEntry{Connection: c.ID, Family: fam, Generation: f.Generation, Account: accountA, Expires: w.clock.now().Add(time.Hour)}, w.clock.now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	tok, err := w.refresh(r.b, y, last)
	if err != nil {
		t.Fatal(err)
	}
	if f1, _, err := w.enclave().readFamily(ctx, fam, 0); err != nil || f1.Epoch != 1 {
		t.Fatal(f1, err)
	}
	r.tokens, r.asks = append(r.tokens, tok), append(r.asks, y)
	r.conns, r.fams = append(r.conns, c.ID), append(r.fams, fam)

	// A decided request, and the list rolled to its third epoch.
	if _, err := r.a.updateList(ctx, accountA, func(l *ListState) error { return l.Decide("a-decided-request", w.clock.now()) }); err != nil {
		t.Fatal(err)
	}
	for {
		l, _, err := r.a.readList(ctx, accountA)
		if err != nil {
			t.Fatal(err)
		}
		if l.Epoch == 2 {
			break
		}
		if _, err := r.a.updateList(ctx, accountA, func(*ListState) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	r.listEpochTop = 2
	// b remembers the newest epoch.
	if l, _, err := r.b.readList(ctx, accountA); err != nil || l.Epoch != 2 {
		t.Fatal(l, err)
	}

	// Another account's, which nothing of this touches.
	r.otherAsk = w.ask(chatgpt)
	r.other, _ = w.connect(r.a, w.person(accountB), r.otherAsk)
	r.otherConn = w.connectionOf(r.a, accountB, chatgpt).ID
	r.grant(r.a, r.otherAsk, r.other.AccessToken)
	return r
}

// stored are the names of what is stored of accountA's connections, every
// version of each.
func (r *removalWorld) stored() []string {
	var out []string
	for _, name := range r.mem.RecordNames() {
		keep := strings.HasPrefix(name, RecordList+accountA+"-")
		for _, id := range r.conns {
			keep = keep || strings.HasPrefix(name, connName(id)+"@")
		}
		for _, f := range r.fams {
			keep = keep || strings.HasPrefix(name, RecordFamily+f+"-")
		}
		if keep {
			out = append(out, name)
		}
	}
	return out
}

// removed fences and removes accountA, as accounts does: closing, EndAll,
// then gone and RemoveAll.
func (r *removalWorld) remove(s *Service) error {
	ctx := context.Background()
	r.accts.set(accountA, StandingClosing)
	if err := s.EndAll(ctx, accountA); err != nil {
		return err
	}
	r.accts.set(accountA, StandingGone)
	return s.RemoveAll(ctx, accountA)
}

// nothingLeft checks that nothing of accountA's connections can be read or
// used, through a new enclave and through r.b, which remembers the list's
// newest epoch and read its connections; and, unless putBack, that none of
// their values is stored.
func (r *removalWorld) nothingLeft(t *testing.T, putBack ...bool) {
	t.Helper()
	ctx := context.Background()
	for _, s := range []*Service{r.enclave(), r.b} {
		if l, _, err := s.readList(ctx, accountA); err == nil && l != nil {
			t.Fatalf("the removed account's list reads: %+v", l)
		}
		for _, id := range r.conns {
			if d, _, err := s.readConn(ctx, id); err == nil && d != nil {
				t.Fatalf("a removed connection reads: %+v", d)
			}
		}
		for _, f := range r.fams {
			if fs, _, err := s.readFamily(ctx, f, 0); err == nil && fs != nil {
				t.Fatalf("a removed family reads: %+v", fs)
			}
		}
		for i, tok := range r.tokens {
			if _, err := r.check(s, r.asks[i], tok.AccessToken); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("a removed account's access token: %v", err)
			}
			if _, err := r.refresh(s, r.asks[i], tok.RefreshToken); oauthCode(err) != oauth.ErrInvalidGrant {
				t.Fatalf("a removed account's refresh token: %v", err)
			}
		}
		if l, err := s.List(ctx, r.person(accountA)); err == nil && len(l.Connections) != 0 {
			t.Fatalf("a removed account lists connections: %+v", l)
		}
		// Neither the fence nor an approval makes its list again.
		if err := s.EndAll(ctx, accountA); err == nil {
			t.Fatal("the removed account's list was made again")
		}
	}
	if left := r.stored(); len(left) != 0 && len(putBack) == 0 {
		t.Fatalf("records of the removed account's connections stay: %v", left)
	}
	// The other account's list and connection are untouched.
	if l, _, err := r.enclave().readList(ctx, accountB); err != nil || l == nil || len(l.Members) != 1 {
		t.Fatalf("the other account's list: %+v %v", l, err)
	}
	if d, _, err := r.enclave().readConn(ctx, r.otherConn); err != nil || d == nil {
		t.Fatalf("the other account's connection: %+v %v", d, err)
	}
}

func TestV65RemovingAnAccountRemovesItsConnections(t *testing.T) {
	ctx := context.Background()
	r := newRemovalWorld(t)

	// Everything stored of them, as whoever runs the storage could keep it.
	kept := map[string]store.Record{}
	for _, name := range r.stored() {
		rec, err := r.mem.GetRecord(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		kept[name] = *rec
	}
	var moved, families int
	for name := range kept {
		if strings.HasPrefix(name, ListName(accountA, 0)+"@") || strings.HasPrefix(name, ListName(accountA, 1)+"@") {
			moved++
		}
		if strings.HasPrefix(name, familyName(r.fams[1], 1)+"@") {
			families++
		}
	}
	if moved < 2 || families < 1 {
		t.Fatalf("the world does not have moved epochs: %d %d", moved, families)
	}
	index, err := r.mem.GetEntry(ctx, partitionAccounts, accountA)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.remove(r.a); err != nil {
		t.Fatal(err)
	}
	if _, err := r.mem.GetEntry(ctx, partitionAccounts, accountA); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the account's entry in the sweep's index stays: %v", err)
	}
	r.nothingLeft(t)
	// Again: nothing to do, and nothing made.
	if err := r.enclave().RemoveAll(ctx, accountA); err != nil {
		t.Fatal(err)
	}

	// The sweep, past every connection's 30 days.
	r.clock.add(EndedKept + time.Hour)
	if err := r.enclave().Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	r.nothingLeft(t)

	// Whoever runs the storage puts back everything removed.
	for _, rec := range kept {
		if err := r.mem.PutRecord(ctx, &rec, false); err != nil {
			t.Fatal(err)
		}
	}
	r.mem.SetEntry(*index)
	r.clock.add(-EndedKept)
	r.nothingLeft(t, true)
	if err := r.enclave().Sweep(ctx); err != nil {
		t.Fatal(err)
	}
}

// A removal stopped at any of its removals, or before deleting the index's
// entry, is finished by the next call, through another enclave.
func TestARemovalStoppedAnywhereIsFinished(t *testing.T) {
	ctx := context.Background()
	for stop := 1; ; stop++ {
		r := newRemovalWorld(t)
		s, f := r.hooked()
		n := 0
		f.set(func(op, name string) error {
			if op == "remove" {
				if n++; n == stop {
					return errCrash
				}
			}
			return nil
		})
		err := r.remove(s)
		f.set(nil)
		if err == nil {
			if stop < 4 {
				t.Fatalf("only %d removals", stop-1)
			}
			r.nothingLeft(t)
			return
		}
		if !errors.Is(err, errCrash) {
			t.Fatal(stop, err)
		}
		if err := r.enclave().RemoveAll(ctx, accountA); err != nil {
			t.Fatalf("stopped at removal %d, not finished: %v", stop, err)
		}
		r.nothingLeft(t)
	}
}

// The index's entry failing to go fails the removal, which the next call
// finishes.
func TestARemovalWhoseIndexEntryStaysIsFinished(t *testing.T) {
	ctx := context.Background()
	r := newRemovalWorld(t)
	down := errors.New("down")
	r.mem.Interrupt = func(_ context.Context, step store.Step, partition, _ string) error {
		if partition == partitionAccounts && step == store.StepDeleteEntry {
			return down
		}
		return nil
	}
	if err := r.remove(r.a); !errors.Is(err, down) {
		t.Fatal(err)
	}
	r.mem.Interrupt = nil
	if err := r.a.RemoveAll(ctx, accountA); err != nil {
		t.Fatal(err)
	}
	if _, err := r.mem.GetEntry(ctx, partitionAccounts, accountA); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	r.nothingLeft(t)
}

// A removal that finds the list whole fences it itself, and one that cannot
// fence it, or read it, fails.
func TestARemovalFencesAWholeList(t *testing.T) {
	ctx := context.Background()
	r := newRemovalWorld(t)
	r.accts.set(accountA, StandingGone)
	s, f := r.hooked()
	f.set(func(op, name string) error {
		if op == "put" && strings.HasPrefix(name, RecordList) {
			return errCrash
		}
		return nil
	})
	if err := s.RemoveAll(ctx, accountA); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	f.set(func(op, name string) error {
		if op == "get" && name == ListName(accountA, 1) {
			return errCrash
		}
		return nil
	})
	if err := s.RemoveAll(ctx, accountA); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	f.set(nil)
	if err := s.RemoveAll(ctx, accountA); err != nil {
		t.Fatal(err)
	}
	r.nothingLeft(t)
}

// An account with no list: its removal leaves epoch 0 removed, so an
// approval in flight, whose standing was read open before, can make
// nothing.
func TestRemovingAnAccountWithNoListFencesAnApprovalInFlight(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	p := w.person(accountA)
	x := w.ask(claudeCode)
	req := w.start(a, p, x)
	reached := false
	w.vendors.onClient = func() {
		if reached {
			return
		}
		reached = true
		w.accts.set(accountA, StandingGone)
		if err := b.RemoveAll(ctx, accountA); err != nil {
			t.Error(err)
		}
	}
	_, err := a.Decide(ctx, p, req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &req.Draft, Snapshot: &req.Snapshot, ModelNotice: notice})
	w.vendors.onClient = nil
	if !reached {
		t.Fatal("barrier never reached")
	}
	if err == nil {
		t.Fatal("an approval in flight was allowed after its account was removed")
	}
	for _, name := range w.mem.RecordNames() {
		if strings.HasPrefix(name, RecordList) || strings.HasPrefix(name, RecordConnection) || strings.HasPrefix(name, RecordFamily) {
			t.Fatalf("the approval made %s", name)
		}
	}
	if l, _, err := w.enclave().readList(ctx, accountA); err != nil || l != nil {
		t.Fatal(l, err)
	}
	if _, err := w.enclave().updateList(ctx, accountA, func(*ListState) error { return nil }); err == nil {
		t.Fatal("a list was made for a removed account")
	}
}

// Removing a family stopped halfway is finished from its oldest epoch; an
// epoch that cannot be read is removed and the walk goes on past it.
func TestRemovingAFamilyInEpochs(t *testing.T) {
	ctx := context.Background()
	r := newRemovalWorld(t)
	fam := r.fams[1]
	s, f := r.hooked()
	f.set(func(op, name string) error {
		if op == "remove" && name == familyName(fam, 0) {
			return errCrash
		}
		return nil
	})
	if err := s.removeFamily(ctx, fam); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	f.set(nil)
	if fs, _, err := r.enclave().readFamily(ctx, fam, 0); err == nil && fs != nil && fs.Epoch == 1 {
		t.Fatal("the newest epoch was not removed first")
	}
	if err := r.enclave().removeFamily(ctx, fam); err != nil {
		t.Fatal(err)
	}
	for _, name := range r.mem.RecordNames() {
		if strings.HasPrefix(name, RecordFamily+fam+"-") {
			t.Fatal("left", name)
		}
	}

	// An epoch whose value whoever writes the table deleted.
	other := r.fams[0]
	for _, name := range r.mem.RecordNames() {
		if strings.HasPrefix(name, familyName(other, 0)+"@") {
			_ = r.mem.DeleteRecord(ctx, name)
		}
	}
	if _, _, err := r.enclave().readFamily(ctx, other, 0); !errors.Is(err, store.ErrLost) {
		t.Fatal(err)
	}
	if err := r.enclave().removeFamily(ctx, other); err != nil {
		t.Fatal(err)
	}
	if fs, _, err := r.enclave().readFamily(ctx, other, 0); err != nil || fs != nil {
		t.Fatal(fs, err)
	}
}
