package accounts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
)

// lifecycle is the connections service as accounts calls it (ADR 0030's
// pull request 3): it records each call, and fails when a test says so.
type lifecycle struct {
	mu        sync.Mutex
	calls     []string
	endAllErr error
	reopenErr error
}

func (l *lifecycle) EndAll(_ context.Context, account string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, "EndAll "+account)
	return l.endAllErr
}

func (l *lifecycle) Reopen(_ context.Context, account string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, "Reopen "+account)
	return l.reopenErr
}

func (l *lifecycle) set(endAll, reopen error) {
	l.mu.Lock()
	l.endAllErr, l.reopenErr = endAll, reopen
	l.mu.Unlock()
}

func (l *lifecycle) count(call string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, c := range l.calls {
		if c == call {
			n++
		}
	}
	return n
}

// An account's connections are fenced and ended when its deletion is asked,
// by its person or the operator, and on every pass while it is closing, so
// an EndAll that failed (a crash between the account's closing and the
// fence) is finished by the next pass; the removal ends them once more, and
// fails while it cannot (ADR 0030, finding 6, V30).
func TestDeletionEndsTheConnections(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	l := &lifecycle{}
	w.svc.SetConnections(l)
	me, cookie := w.signUp("ana@example.org")
	id := me.Account.ID
	who := w.who(cookie)

	// Asked, and the fence failing: the deletion stands, and is logged by
	// its kind.
	l.set(errors.New("the storage is down"), nil)
	if _, err := w.svc.StartDeletion(ctx, w.fresh(who)); err != nil {
		t.Fatal(err)
	}
	if l.count("EndAll "+id) != 1 || !strings.Contains(w.logs.String(), "are not all ended yet") {
		t.Fatal(l.calls, w.logs.String())
	}
	// The next pass, while it is closing, tries again and says it failed.
	if err := w.svc.RemoveDue(ctx); err == nil || l.count("EndAll "+id) != 2 {
		t.Fatal(err, l.calls)
	}
	l.set(nil, nil)
	if err := w.svc.RemoveDue(ctx); err != nil || l.count("EndAll "+id) != 3 {
		t.Fatal(err, l.calls)
	}
	// The removal ends them once more, first, and fails while it cannot.
	w.now = w.now.Add(domain.AccountClosing)
	l.set(errors.New("down again"), nil)
	if err := w.svc.RemoveDue(ctx); err == nil {
		t.Fatal("removed with its connections not ended")
	}
	if _, err := w.svc.AccountRecord(ctx, id); err != nil {
		t.Fatal(err)
	}
	l.set(nil, nil)
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.AccountRecord(ctx, id); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatal(err)
	}

	// The operator's deletion the same.
	other, _ := w.signUp("eve@example.org")
	if _, err := w.svc.DeleteAccount(ctx, other.Account.ID); err != nil {
		t.Fatal(err)
	}
	if l.count("EndAll "+other.Account.ID) != 1 {
		t.Fatal(l.calls)
	}
}

// A cancelled deletion is done only once Reopen has ended what of its
// connections was still active; a cancellation whose Reopen fails is not
// done (ADR 0030, finding 6).
func TestCancellingADeletionReopensTheConnectionsFirst(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	l := &lifecycle{}
	w.svc.SetConnections(l)
	me, cookie := w.signUp("ana@example.org")
	id := me.Account.ID
	who := w.who(cookie)
	if _, err := w.svc.StartDeletion(ctx, w.fresh(who)); err != nil {
		t.Fatal(err)
	}
	l.set(nil, errors.New("down"))
	if _, err := w.svc.CancelDeletion(ctx, who); err == nil {
		t.Fatal("cancelled with its connections not reopened")
	}
	if rec, _ := w.svc.AccountRecord(ctx, id); rec.State != contracts.AccountClosing {
		t.Fatal(rec.State)
	}
	l.set(nil, nil)
	got, err := w.svc.CancelDeletion(ctx, who)
	if err != nil || got.State != contracts.AccountActive || l.count("Reopen "+id) != 2 {
		t.Fatal(got, err, l.calls)
	}
	// A pass over an entry the record no longer bears out fences nothing.
	before := l.count("EndAll " + id)
	if err := w.svc.RemoveDue(ctx); err != nil || l.count("EndAll "+id) != before {
		t.Fatal(err, l.calls)
	}
	// The operator's cancellation the same.
	other, _ := w.signUp("eve@example.org")
	if _, err := w.svc.DeleteAccount(ctx, other.Account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.KeepAccount(ctx, other.Account.ID); err != nil || l.count("Reopen "+other.Account.ID) != 1 {
		t.Fatal(err, l.calls)
	}
}

// Without the connections service, nothing of it is called, and a pass
// stops at the first account that is not due, as before.
func TestNoConnectionsNoCalls(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	if w.svc.connectionsOf() != nil {
		t.Fatal("a connections service out of nothing")
	}
	_, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)
	if _, err := w.svc.StartDeletion(ctx, w.fresh(who)); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.CancelDeletion(ctx, who); err != nil {
		t.Fatal(err)
	}
	// Config.Connections is used when nothing was set.
	l := &lifecycle{}
	w.svc.cfg.Connections = l
	if w.svc.connectionsOf() != l {
		t.Fatal("Config.Connections not used")
	}
}

// An account's receipts are deleted with it, and once more
// receipts.RemoveAgainAfter later, for a receipt whose write began before
// the removal and ended after it (ADR 0031, choice 32).
func TestTheReceiptsGoWithTheAccountAndOnceMore(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	me, cookie := w.signUp("ana@example.org")
	id := me.Account.ID
	receipt := func(key string) store.Entry {
		return store.Entry{Partition: receipts.Partition(id), Key: key, Value: []byte(`{"sealed":"x"}`)}
	}
	w.mem.SetEntry(receipt("rcp_1"))
	if _, err := w.svc.StartDeletion(ctx, w.fresh(w.who(cookie))); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(domain.AccountClosing)
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if left, _ := w.mem.ListEntries(ctx, receipts.Partition(id), "", "", 10); len(left) != 0 {
		t.Fatal("its receipts stayed", left)
	}
	// A receipt written after the removal, and a forged entry of the list.
	w.mem.SetEntry(receipt("rcp_2"))
	w.mem.SetEntry(store.Entry{Partition: receiptsAgainPartition, Key: timedKey(w.now, newID("acc")), Value: []byte("{}")})
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if left, _ := w.mem.ListEntries(ctx, receipts.Partition(id), "", "", 10); len(left) != 1 {
		t.Fatal("deleted before its time", left)
	}
	w.now = w.now.Add(receipts.RemoveAgainAfter)
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if left, _ := w.mem.ListEntries(ctx, receipts.Partition(id), "", "", 10); len(left) != 0 {
		t.Fatal("its late receipt stayed", left)
	}
	again, _ := w.mem.ListEntries(ctx, receiptsAgainPartition, "", "", 10)
	if len(again) != 1 { // the forged one, which deletes nothing
		t.Fatal(again)
	}
}

// The storage failing while the receipts are deleted again fails the pass,
// which tries again.
func TestTheReceiptsDeletedAgainFailing(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	me, cookie := w.signUp("ana@example.org")
	if _, err := w.svc.StartDeletion(ctx, w.fresh(w.who(cookie))); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(domain.AccountClosing)
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(receipts.RemoveAgainAfter)
	down := errors.New("down")
	w.mem.Interrupt = func(_ context.Context, step store.Step, partition, _ string) error {
		if partition == receipts.Partition(me.Account.ID) || partition == receiptsAgainPartition {
			return down
		}
		return nil
	}
	if err := w.svc.RemoveDue(ctx); !errors.Is(err, down) {
		t.Fatal(err)
	}
	w.mem.Interrupt = nil
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
}

// The receipts' deletion failing at the removal fails the removal, which
// the next pass finishes; reading a closing account's record failing fails
// the pass that fences it.
func TestTheRemovalWaitsForTheReceiptsAndTheRecord(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	me, cookie := w.signUp("ana@example.org")
	id := me.Account.ID
	w.svc.SetConnections(&lifecycle{})
	if _, err := w.svc.StartDeletion(ctx, w.fresh(w.who(cookie))); err != nil {
		t.Fatal(err)
	}
	down := errors.New("down")
	w.mem.Interrupt = func(_ context.Context, _ store.Step, partition, key string) error {
		if strings.Contains(key, id) && partition != closingPartition {
			return down
		}
		return nil
	}
	if err := w.svc.RemoveDue(ctx); !errors.Is(err, down) {
		t.Fatal("a pass that could not read a closing account", err)
	}
	w.now = w.now.Add(domain.AccountClosing)
	w.mem.Interrupt = func(_ context.Context, _ store.Step, partition, _ string) error {
		if partition == receiptsAgainPartition {
			return down
		}
		return nil
	}
	if err := w.svc.RemoveDue(ctx); !errors.Is(err, down) {
		t.Fatal(err)
	}
	w.mem.Interrupt = nil
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.AccountRecord(ctx, id); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatal(err)
	}
}
