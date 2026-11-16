package connectionstest

import (
	"context"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// RefusedReceipt is a refused receipt as its writer was given it: the
// mailbox and message it names, and how its write was bounded (Cancelled:
// its context was done when the write began; Bound: its deadline from
// then, 0 for none).
type RefusedReceipt struct {
	Reason           receipts.RefusedReason
	Mailbox, Message string
	Cancelled        bool
	Bound            time.Duration
}

// RefuseSubject is one connections.API whose Refuse RefuseContract checks:
// Grant is what its Check answered, Mailbox one of the connection's
// mailboxes, PassHardEnd moves its clock past the grant's hard end, and
// Written lists the refused receipts written, in order.
type RefuseSubject struct {
	API         connections.API
	Grant       *oauth.Grant
	Mailbox     string
	PassHardEnd func()
	Written     func() []RefusedReceipt
}

// RefuseContract is what every API.Refuse keeps (ruling 6 on PR #136's
// second review; ruling 3 of its fourth rulings): exactly one refused
// receipt for each refusal it takes, written under a fresh ReceiptWriteMax,
// detached from the request's cancellation and deadline, also after the
// grant's hard end, and naming a message only when it is of one of the
// connection's mailboxes. The fake runs it, and the service, beside V32.
func RefuseContract(t *testing.T, subject func(t *testing.T) RefuseSubject) {
	t.Helper()
	refuse := func(t *testing.T, s RefuseSubject, ctx context.Context, message string) RefusedReceipt {
		t.Helper()
		before := len(s.Written())
		rf := connections.Refusal{Operation: connections.OperationRead, Reason: receipts.RefusedOutsideScope, Message: message}
		if err := s.API.Refuse(ctx, s.Grant, rf); err != nil {
			t.Fatalf("Refuse: %v", err)
		}
		got := s.Written()
		if len(got) != before+1 {
			t.Fatalf("%d receipts written for one refusal", len(got)-before)
		}
		r := got[len(got)-1]
		if r.Reason != rf.Reason {
			t.Fatalf("the receipt's reason %q", r.Reason)
		}
		return r
	}
	fresh := func(t *testing.T, r RefusedReceipt) {
		t.Helper()
		if r.Cancelled || r.Bound < freshBoundMin || r.Bound > connections.ReceiptWriteMax {
			t.Fatalf("written cancelled %v, under %v: not a fresh ReceiptWriteMax", r.Cancelled, r.Bound)
		}
	}
	t.Run("one receipt naming one of the connection's messages", func(t *testing.T) {
		s := subject(t)
		r := refuse(t, s, context.Background(), s.Mailbox+".m001")
		if r.Mailbox != s.Mailbox || r.Message != "m001" {
			t.Fatalf("the receipt names %q %q", r.Mailbox, r.Message)
		}
		fresh(t, r)
	})
	t.Run("no message identifier it did not check", func(t *testing.T) {
		s := subject(t)
		for _, id := range []string{"mbx_elsewhere.m001", "not-a-message", s.Mailbox + "."} {
			if r := refuse(t, s, context.Background(), id); r.Mailbox != "" || r.Message != "" {
				t.Fatalf("%q recorded as %q %q", id, r.Mailbox, r.Message)
			}
		}
	})
	t.Run("a cancelled request, or one at its deadline, under a fresh bound", func(t *testing.T) {
		s := subject(t)
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		fresh(t, refuse(t, s, cancelled, s.Mailbox+".m001"))
		short, cancel2 := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel2()
		<-short.Done()
		fresh(t, refuse(t, s, short, ""))
	})
	t.Run("after the grant's hard end, with its attribution", func(t *testing.T) {
		s := subject(t)
		s.PassHardEnd()
		fresh(t, refuse(t, s, context.Background(), s.Mailbox+".m001"))
	})
}

// freshBoundMin is the least a fresh ReceiptWriteMax may have left when its
// write begins.
const freshBoundMin = connections.ReceiptWriteMax - time.Second
