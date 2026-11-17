package connections

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/store"
)

// V65, after the third review of PR #152: the account's final removal
// notes the connections its own EndAll trims, so RemoveAgain finds a value
// staged for one of them late; and a list whose members' values were all
// deleted by whoever writes the table fails closed.

// TestV65AConnectionTrimmedByTheRemovalIsNoted: a revoke's write of a
// connection is paused at the table; meanwhile another enclave revokes it,
// the list fills with later ended members, and RemoveAll's EndAll ends the
// last active one, which trims the revoked connection. The late value lands,
// its answer lost; RemoveAgain finds the trimmed connection by its note and
// deletes it (review 3, finding 2).
func TestV65AConnectionTrimmedByTheRemovalIsNoted(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a := w.enclave()
	p := w.person(accountA)
	w.connect(a, p, w.ask(claudeCode))
	c := w.connectionOf(a, accountA, claudeCode)
	table := &lateTable{RecordTable: w.mem, target: connName(c.ID)}
	table.before = func() {
		if _, err := a.Revoke(ctx, p, c.ID); err != nil {
			t.Error(err)
			return
		}
		w.clock.add(time.Second)
		// Later ended members, up to ListEndedMax with the revoked one;
		// their records were never made, so each removes as one that is gone.
		if _, err := a.updateList(ctx, accountA, func(l *ListState) error {
			for i := range ListEndedMax - 1 {
				id, err := a.newID("con")
				if err != nil {
					return err
				}
				at := w.clock.now().Add(time.Duration(i) * time.Nanosecond)
				l.Members = append(l.Members, ListMember{ID: id, ClientID: "filler", State: contracts.ConnectionEnded, MadeAt: at, EndedAt: at})
			}
			return nil
		}); err != nil {
			t.Error(err)
			return
		}
		w.clock.add(time.Second)
		w.connect(a, p, w.ask(otherApp))
		w.accts.set(accountA, StandingGone)
		if err := a.RemoveAll(ctx, accountA); err != nil {
			t.Error(err)
		}
	}
	b, err := New(w.config(&store.Anchored{Records: table, Commits: w.commits, Strict: true, OnlyNewest: true, Now: w.clock.now}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Revoke(ctx, p, c.ID); err == nil {
		t.Fatal("the late write did not fail")
	}
	table.mu.Lock()
	landed := table.landed
	table.mu.Unlock()
	if !landed {
		t.Fatal("the late value did not land")
	}
	notes, err := w.mem.ListEntries(ctx, partitionRemoved+accountA, "", "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	noted := map[string]bool{}
	for _, n := range notes {
		noted[n.Key] = true
	}
	if !noted[c.ID] || !noted[c.Devices[0].ID] {
		t.Fatalf("the removal trimmed the connection without noting it and its family: %v %v", noted[c.ID], noted[c.Devices[0].ID])
	}
	w.clock.add(RemoveAgainAfter)
	if err := w.enclave().RemoveAgain(ctx, accountA); err != nil {
		t.Fatal(err)
	}
	for _, name := range w.mem.RecordNames() {
		if strings.HasPrefix(name, connName(c.ID)+"@") {
			t.Errorf("the last pass missed a connection trimmed inside RemoveAll: %s", name)
		}
	}
	nothingOfAccountA(t, w)
}

// TestV65AListOfLostMembersFailsClosed: every member of a full list (64
// ended and one active) has had its stored values deleted by whoever writes
// the table. EndAll fences the list but cannot end the active member within
// the list's bounds (none of the ended ones can be removed: their families
// are known only from their records), so it and a cancellation fail with the
// list full, on every try: nothing comes back and no token works. This is the
// documented exception to a cancellation always working (review 3,
// finding 3; docs/threat-model.md).
func TestV65AListOfLostMembersFailsClosed(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a := w.enclave()
	y := w.ask(claudeCode)
	tok, _ := w.connect(a, w.person(accountA), y)
	c := w.connectionOf(a, accountA, claudeCode)
	ids := []string{c.ID}
	if _, err := a.updateList(ctx, accountA, func(l *ListState) error {
		for i := range ListEndedMax {
			id, err := a.newID("con")
			if err != nil {
				return err
			}
			d := c.clone()
			d.ID = id
			d.State = contracts.ConnectionEnded
			d.Devices = nil
			if err := a.writeConn(ctx, d, store.Mark{}, true); err != nil {
				return err
			}
			ids = append(ids, id)
			at := w.clock.now().Add(time.Duration(i) * time.Second)
			l.Members = append(l.Members, ListMember{ID: id, ClientID: "filler", State: contracts.ConnectionEnded, MadeAt: at, EndedAt: at})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		lose(t, w, id)
	}
	for range 2 {
		if err := w.enclave().EndAll(ctx, accountA); !errors.Is(err, errListFull) {
			t.Fatalf("EndAll over a list of lost members: %v", err)
		}
		if err := w.enclave().Reopen(ctx, accountA); !errors.Is(err, errListFull) {
			t.Fatalf("a cancellation over a list of lost members: %v", err)
		}
	}
	l, _, err := w.enclave().readList(ctx, accountA)
	if err != nil || l == nil || !l.Closing {
		t.Fatalf("the fence did not hold: %+v %v", l, err)
	}
	if len(l.Members) != len(ids) {
		t.Fatalf("a lost member went from the list: %d of %d", len(l.Members), len(ids))
	}
	for _, id := range ids {
		if _, _, err := w.enclave().readConn(ctx, id); !errors.Is(err, store.ErrLost) {
			t.Fatalf("a lost connection came back: %v", err)
		}
	}
	if _, err := w.check(w.enclave(), y, tok.AccessToken); err == nil {
		t.Fatal("the lost connection's token works")
	}
}

// slowHistory is the world's commits, whose every read of a history takes
// WriteMax on the world's clock once slow is set.
type slowHistory struct {
	*store.MemoryCommits
	w    *world
	slow bool
}

func (s *slowHistory) History(ctx context.Context, name string) (*store.History, error) {
	if s.slow {
		s.w.clock.add(WriteMax)
	}
	return s.MemoryCommits.History(ctx, name)
}

// TestV65AWriteOfTheConnectionsStoresNothingPastWriteMax: a write of a
// connection, of the list and of a family whose read of the record's
// commits outlasts WriteMax stores nothing (store.ErrTooLate), so no value
// is staged later than RemoveAgainAfter allows for (review 3, finding 1).
func TestV65AWriteOfTheConnectionsStoresNothingPastWriteMax(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a := w.enclave()
	p := w.person(accountA)
	w.connect(a, p, w.ask(claudeCode))
	c := w.connectionOf(a, accountA, claudeCode)
	fam := c.Devices[0].ID
	f, mark, err := a.readFamily(ctx, fam, 0)
	if err != nil {
		t.Fatal(err)
	}
	commits := &slowHistory{MemoryCommits: w.commits, w: w}
	b, err := New(w.config(&store.Anchored{Records: w.mem, Commits: commits, Strict: true, OnlyNewest: true, Now: w.clock.now}))
	if err != nil {
		t.Fatal(err)
	}
	stored := len(w.mem.RecordNames())
	commits.slow = true
	for what, write := range map[string]func() error{
		"a connection": func() error { _, err := b.Revoke(ctx, p, c.ID); return err },
		"the list":     func() error { return b.EndAll(ctx, accountA) },
		"a family":     func() error { return b.writeSealed(ctx, familyName(fam, 0), f, mark, false) },
	} {
		if err := write(); !errors.Is(err, store.ErrTooLate) {
			t.Errorf("%s: %v", what, err)
		}
	}
	commits.slow = false
	if now := len(w.mem.RecordNames()); now != stored {
		t.Fatalf("a write past WriteMax stored a value: %d names, %d before", now, stored)
	}
	d, _, err := w.enclave().readConn(ctx, c.ID)
	if err != nil || d.State != contracts.ConnectionActive {
		t.Fatalf("the connection changed: %v", err)
	}
}
