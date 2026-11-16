package accounts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/store"
)

// V65, after the review of PR #152, against the real connections service:
// an account's removal stopped at any step, its connections' or its own,
// is finished by the next pass, through another enclave; one whose
// connection cannot be read is not done.

// dueWithConnections is a world whose account has an ended connection
// and an active one, and whose deletion is due; k is the connections'
// keys, for other enclaves.
func dueWithConnections(t *testing.T) (*world, string, *keys.Local) {
	t.Helper()
	ctx := context.Background()
	w := newWorld(t)
	k, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	conns := realEnclave(t, w, k)
	w.svc.SetConnections(conns)
	me, cookie := w.signUp("ana@example.org")
	id := me.Account.ID
	p := connections.Person{Account: id, Session: "ses_test", ConfirmedUntil: w.now.Add(10 * time.Minute)}
	realConnect(t, conns, p)
	list, err := conns.List(ctx, p)
	if err != nil || len(list.Connections) != 1 {
		t.Fatal(list, err)
	}
	if _, err := conns.Revoke(ctx, p, list.Connections[0].ID); err != nil {
		t.Fatal(err)
	}
	realConnect(t, conns, p)
	if _, err := w.svc.StartDeletion(ctx, w.fresh(w.who(cookie))); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(domain.AccountClosing)
	return w, id, k
}

// removedWhole checks that the account and everything of its connections
// are gone.
func removedWhole(t *testing.T, w *world, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := w.svc.AccountRecord(ctx, id); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("the account stays: %v", err)
	}
	if left := connectionRecords(w); len(left) != 0 {
		t.Fatalf("records of its connections stay: %v", left)
	}
	if _, err := w.mem.GetEntry(ctx, indexPartition, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("its entry in the sweep's index stays: %v", err)
	}
}

// TestV65RetryAfterConnectionsRemoved: the account's cleanup failing after
// its connections went (its receipts' entry, here) is finished by the next
// passes, the same enclave's and a new one's.
func TestV65RetryAfterConnectionsRemoved(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	k, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	w.svc.SetConnections(realEnclave(t, w, k))
	me, cookie := w.signUp("ana@example.org")
	if _, err := w.svc.StartDeletion(ctx, w.fresh(w.who(cookie))); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(domain.AccountClosing)
	down := errors.New("down after the connections went")
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
	if _, err := w.svc.AccountRecord(ctx, me.Account.ID); err != nil {
		t.Fatalf("the account went before its cleanup: %v", err)
	}
	for i := range 2 {
		if err := w.svc.RemoveDue(ctx); err != nil {
			t.Fatalf("pass %d cannot finish the removal: %v", i+1, err)
		}
	}
	b := w.enclave()
	b.svc.SetConnections(realEnclave(t, w, k))
	if err := b.svc.RemoveDue(ctx); err != nil {
		t.Fatalf("a new enclave cannot finish the removal: %v", err)
	}
	removedWhole(t, w, me.Account.ID)
}

// TestV65AnAccountRemovalStoppedAtAnyStepIsFinished: the removal failing
// at each write or deletion it makes, of the connections' list, records,
// families and index as of the account's own cleanup, is finished by the
// next pass through a new enclave.
func TestV65AnAccountRemovalStoppedAtAnyStepIsFinished(t *testing.T) {
	ctx := context.Background()
	writes := map[store.Step]bool{store.StepRecord: true, store.StepEntry: true, store.StepDeleteEntry: true, store.StepItem: true, store.StepRaw: true, store.StepDeleteItem: true, store.StepDeleteRaw: true}
	down := errors.New("down")
	for stop := 1; ; stop++ {
		w, id, k := dueWithConnections(t)
		var mu sync.Mutex
		n := 0
		where := ""
		w.mem.Interrupt = func(_ context.Context, step store.Step, partition, key string) error {
			if !writes[step] {
				return nil
			}
			mu.Lock()
			defer mu.Unlock()
			if n++; n == stop {
				where = string(step) + " " + partition + " " + strings.SplitN(key, "-", 2)[0]
				return down
			}
			return nil
		}
		err := w.svc.RemoveDue(ctx)
		w.mem.Interrupt = nil
		if n < stop {
			if err != nil {
				t.Fatal(err)
			}
			if stop < 10 {
				t.Fatalf("only %d steps", n)
			}
			removedWhole(t, w, id)
			t.Logf("stopped at each of %d steps", n)
			return
		}
		if err != nil && !errors.Is(err, down) {
			t.Fatalf("stopped at step %d (%s): %v", stop, where, err)
		}
		// A step whose failure the store's or the service's retries took
		// in, or that is tried again later (the receipts' second deletion),
		// leaves the pass done or the next one to finish it.
		b := w.enclave()
		b.svc.SetConnections(realEnclave(t, w, k))
		if err := b.svc.RemoveDue(ctx); err != nil {
			t.Fatalf("stopped at step %d (%s), not finished: %v", stop, where, err)
		}
		removedWhole(t, w, id)
		if err := b.svc.RemoveDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

// TestV65AnAccountWithALostConnectionIsNotRemoved: a connection whose value
// whoever writes the table deleted fails the removal, which keeps the
// account and what finds the connection's families, pass after pass.
func TestV65AnAccountWithALostConnectionIsNotRemoved(t *testing.T) {
	ctx := context.Background()
	w, id, _ := dueWithConnections(t)
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
	for range 2 {
		if err := w.svc.RemoveDue(ctx); !errors.Is(err, store.ErrLost) {
			t.Fatalf("the removal went on past a lost connection: %v", err)
		}
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
