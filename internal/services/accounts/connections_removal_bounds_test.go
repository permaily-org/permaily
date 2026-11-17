package accounts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/store"
)

// V65, after the second review of PR #152, against the real connections
// service: the hourly pass between two tries of a blocked removal keeps it
// blocked, and a value a write under way stages after the removal is
// deleted by the removal's last pass.

// TestV65TheSweepBetweenRetriesKeepsTheRemovalBlocked: a removal blocked by
// a lost connection stays blocked across the connections' hourly pass, 30
// days on: the pass does not remove the lost connection from the list, so
// the next try still finds it, and the account, its index entry and the
// families stay (review 2, finding 1).
func TestV65TheSweepBetweenRetriesKeepsTheRemovalBlocked(t *testing.T) {
	ctx := context.Background()
	w, id, k := dueWithConnections(t)
	families := 0
	for _, name := range w.mem.RecordNames() {
		if strings.HasPrefix(name, connections.RecordConnection) {
			if err := w.mem.DeleteRecord(ctx, name); err != nil {
				t.Fatal(err)
			}
		}
		if strings.HasPrefix(name, connections.RecordFamily) {
			families++
		}
	}
	if err := w.svc.RemoveDue(ctx); !errors.Is(err, store.ErrLost) {
		t.Fatalf("the removal went on past a lost connection: %v", err)
	}
	w.now = w.now.Add(connections.EndedKept + time.Hour)
	if err := realEnclave(t, w, k).Sweep(ctx); !errors.Is(err, store.ErrLost) {
		t.Fatalf("the sweep passed over the lost connections: %v", err)
	}
	if err := w.svc.RemoveDue(ctx); !errors.Is(err, store.ErrLost) {
		t.Fatalf("the removal went on after the sweep: %v", err)
	}
	if _, err := w.svc.AccountRecord(ctx, id); err != nil {
		t.Fatalf("the account went: %v", err)
	}
	if _, err := w.mem.GetEntry(ctx, indexPartition, id); err != nil {
		t.Fatalf("the sweep's index lost the account: %v", err)
	}
	left := 0
	for _, name := range w.mem.RecordNames() {
		if strings.HasPrefix(name, connections.RecordFamily) {
			left++
		}
	}
	if families == 0 || left != families {
		t.Fatalf("families before %d, after %d", families, left)
	}
}

// lateTable is the world's table, whose write of a value under target's
// name runs before first, then stores it and answers an error, as a write
// whose answer was lost: a value staged by a writer that passed CreateNew's
// or PutRecordAt's check before the removal, landing after it.
type lateTable struct {
	store.RecordTable
	mu     sync.Mutex
	target string
	before func()
}

func (l *lateTable) PutRecord(ctx context.Context, rec *store.Record, create bool) error {
	l.mu.Lock()
	f := l.before
	if !strings.HasPrefix(rec.Name, l.target+"@") {
		f = nil
	}
	if f != nil {
		l.before = nil
	}
	l.mu.Unlock()
	if f == nil {
		return l.RecordTable.PutRecord(ctx, rec, create)
	}
	f()
	if err := l.RecordTable.PutRecord(ctx, rec, create); err != nil {
		return err
	}
	return errors.New("stored, and the answer lost")
}

// TestV65AValueStagedAfterTheRemovalGoesWithTheLastPass: another enclave
// rolls the closing list to its next epoch; its CreateNew passed its
// check, and the account's whole removal runs before its value lands. The
// value lands after, and its answer is lost. The account is gone, and the
// removal's last pass, after the longest a write stays under way, deletes
// the value (review 2, finding 2).
func TestV65AValueStagedAfterTheRemovalGoesWithTheLastPass(t *testing.T) {
	ctx := context.Background()
	w, id, k := dueWithConnections(t)
	a := realEnclave(t, w, k)
	for {
		head, _, err := w.commits.Head(ctx, connections.ListName(id, 0))
		if err != nil || head == nil {
			t.Fatal(head, err)
		}
		if head.Version == connections.ListEpochVersions-1 {
			break
		}
		if err := a.EndAll(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	table := &lateTable{RecordTable: w.mem, target: connections.ListName(id, 1)}
	fired := false
	table.before = func() {
		fired = true
		if err := w.svc.RemoveDue(ctx); err != nil {
			t.Error(err)
		}
		if _, err := w.svc.AccountRecord(ctx, id); !errors.Is(err, contracts.ErrNotFound) {
			t.Error("the account was not removed:", err)
		}
	}
	clock := func() time.Time { return w.now }
	b, err := connections.New(connections.Config{
		OAuth:   oauth.Config{Issuer: "https://api.permaily.test", AppOrigin: "https://app.permaily.test", APIResource: "https://api.permaily.test/v1", MCPResource: "https://mcp.permaily.test/mcp"},
		Records: &store.Anchored{Records: table, Commits: w.commits, Strict: true, OnlyNewest: true, Now: clock},
		Entries: w.mem, Keys: k, Vendors: realVendors{}, Accounts: w.svc, Mailboxes: realMailboxes{}, Receipts: realReceipts{}, ModelNotice: w.notice, Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EndAll(ctx, id); err == nil {
		t.Fatal("the late write's lost answer was not seen")
	}
	if !fired {
		t.Fatal("the late write was not reached")
	}
	if left := connectionRecords(w); len(left) == 0 {
		t.Fatal("the late value did not land")
	}
	if err := a.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(time.Hour)
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	removedWhole(t, w, id)
}
