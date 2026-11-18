package connections

import (
	"context"
	"sync"
	"testing"

	"github.com/permaily-org/permaily/internal/services/receipts"
)

// H45 (a review's follow-up on pull request 161): a refusal measures what
// it holds of the budget (receipts.Size serializes it) with the lanes'
// enclave-wide lock let go, so no refusal of the enclave waits behind
// another's serialization; and it measures each receipt once: its lane's
// attribution when it makes the lane, its own receipt when it waits for its
// lane's writer, the same measure when it then writes it.
func TestH45AReceiptIsMeasuredOutsideTheLanesLock(t *testing.T) {
	w, s, g := refusalsWorld(t)
	var mu sync.Mutex
	measured := map[receipts.Kind]int{}
	// Nothing else takes the lock while a request measures: the first
	// writer is held in its write, outside it, and no pass runs. A lock
	// that cannot be taken here is held by the request that measures.
	s.refusals.size = func(rc *receipts.Receipt) (int, error) {
		if !s.refusals.mu.TryLock() {
			t.Errorf("a %s receipt was measured with the lanes' lock held", rc.Kind)
		} else {
			s.refusals.mu.Unlock()
		}
		mu.Lock()
		measured[rc.Kind]++
		mu.Unlock()
		return receipts.Size(rc)
	}
	waitForRelease(s)
	entered, release := pausing(w, g.Connection)
	defer release()
	first := make(chan error, 1)
	go func() {
		first <- s.Refuse(context.Background(), g, Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody})
	}()
	<-entered
	second := make(chan error, 1)
	go func() {
		second <- s.Refuse(context.Background(), g, Refusal{Operation: OperationRead, Reason: receipts.RefusedOutsideScope})
	}()
	eventually(func() bool {
		n, _ := waiting(s, g.Connection)
		return n == 1
	})
	release()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if n := len(w.rcpt.of(receipts.KindRefused)); n != 2 {
		t.Fatalf("%d refused receipts written, want 2", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if measured[receipts.KindRefusals] != 1 || measured[receipts.KindRefused] != 2 {
		t.Fatalf("measured %v, want the lane's attribution once and each refused receipt once", measured)
	}
}
