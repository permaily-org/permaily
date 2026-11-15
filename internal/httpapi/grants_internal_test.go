package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
)

// The read budget as inbox.ReadGrants (ADR 0027, "Budgets of a page"): a
// grant goes back when it is released, once; what is left goes back with
// the response; a grant that waits too long is too_many_requests.
func TestGrantsGoBackOnce(t *testing.T) {
	var _ inbox.ReadGrants = (*heldBudget)(nil)
	b := newByteBudget(100)
	h := &heldBudget{budget: b, wait: 20 * time.Millisecond}
	ctx := context.Background()
	if err := h.Take(ctx, 10); err != nil {
		t.Fatal(err)
	}
	first, err := h.Grant(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.Grant(ctx, 70) // what is free now is 60: it waits, then is refused
	if !errors.Is(err, contracts.ErrTooManyRequests) || second != nil {
		t.Fatalf("%v", err)
	}
	// Past the whole budget: refused at once, nothing taken, nothing waited.
	start := time.Now()
	if past, err := h.Grant(ctx, 101); !errors.Is(err, contracts.ErrTooManyRequests) || past != nil || time.Since(start) >= h.wait || b.waiting != 0 {
		t.Fatalf("%v", err)
	}
	if b.free != 60 {
		t.Fatalf("free %d", b.free)
	}
	first()
	first() // a second call gives nothing back
	if b.free != 90 || h.granted != 0 || h.held != 10 {
		t.Fatalf("free %d, granted %d, held %d", b.free, h.granted, h.held)
	}
	third, err := h.Grant(ctx, 40)
	if err != nil {
		t.Fatal(err)
	}
	h.release() // the response: what Take took and the grant not released
	if b.free != 100 {
		t.Fatalf("free %d after the response", b.free)
	}
	third() // after the response: nothing more
	if b.free != 100 {
		t.Fatalf("free %d", b.free)
	}
	// A grant that was waiting when the response was released gives its
	// bytes straight back.
	if _, err := h.Grant(ctx, 5); !errors.Is(err, context.Canceled) || b.free != 100 {
		t.Fatalf("%v, free %d", err, b.free)
	}
}

// Every request waits AdmissionWait for room by default.
func TestTheDefaultWait(t *testing.T) {
	a, _ := newMailboxesAPI(MailboxesConfig{})
	if a.cfg.Wait != AdmissionWait || AdmissionWait != 10*time.Second {
		t.Fatalf("%v", a.cfg.Wait)
	}
}
