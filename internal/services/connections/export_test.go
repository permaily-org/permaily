package connections

import (
	"context"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
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
