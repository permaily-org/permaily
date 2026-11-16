package connections

import (
	"context"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// ReleaseMaker makes leases of the service's own, each of a device of its
// own whose revocation (RevokeDevice, the real one) is the cut, for
// connectionstest.ReleaseContract (V59), which this package's own tests
// cannot import.
func ReleaseMaker(t *testing.T) func() (Release, func()) {
	w := newWorld(t)
	s := w.enclave()
	ctx := context.Background()
	return func() (Release, func()) {
		x := w.ask(claudeCode)
		tok, err := w.exchange(s, x, w.allow(s, w.person(accountA), w.start(s, w.person(accountA), x)))
		if err != nil {
			t.Fatal(err)
		}
		g := w.grant(s, x, tok.AccessToken)
		_, rel, err := s.Search(ctx, g, contracts.VendorSearch{})
		if err != nil {
			t.Fatal(err)
		}
		return rel, func() {
			if _, err := s.RevokeDevice(ctx, w.person(accountA), g.Connection, g.Family); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// RefusalWrite is a refused receipt the service wrote, and what its
// context's deadline left when its write began.
type RefusalWrite struct {
	Reason           receipts.RefusedReason
	Mailbox, Message string
	Bound            time.Duration
}

// RefuseMaker is one enclave, the grant its Check answered for a connection
// to every mailbox, one of its mailboxes, what moves its clock past the
// grant's hard end, and the refused receipts written, for
// connectionstest.RefuseContract (V32).
func RefuseMaker(t *testing.T) (API, *oauth.Grant, string, func(), func() []RefusalWrite) {
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	written := func() []RefusalWrite {
		var out []RefusalWrite
		w.rcpt.mu.Lock()
		defer w.rcpt.mu.Unlock()
		for _, r := range w.rcpt.all {
			if r.Kind == receipts.KindRefused {
				out = append(out, RefusalWrite{Reason: r.Refused.Reason, Mailbox: r.Refused.Mailbox, Message: r.Refused.Message, Bound: w.rcpt.bounds[r.ID]})
			}
		}
		return out
	}
	return s, g, mailboxID(1), func() { w.clock.add(oauth.AccessTokenTTL) }, written
}
