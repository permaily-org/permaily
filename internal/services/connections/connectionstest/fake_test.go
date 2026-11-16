package connectionstest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
)

// A method the test did not set answers ErrNotSet; one it set answers what
// the test says; every call is recorded.
func TestTheFakeAnswersWhatTheTestSets(t *testing.T) {
	f := &Fake{CheckFunc: func(context.Context, contracts.Resource, string) (*oauth.Grant, error) {
		return &oauth.Grant{Connection: "con_a"}, nil
	}}
	ctx := context.Background()
	if g, err := f.Check(ctx, contracts.ResourceMCP, "pma_x"); err != nil || g.Connection != "con_a" {
		t.Fatalf("%+v %v", g, err)
	}
	if _, _, err := f.Search(ctx, &oauth.Grant{}, contracts.VendorSearch{}); !errors.Is(err, ErrNotSet) {
		t.Fatal(err)
	}
	if got := f.Calls(); !slices.Equal(got, []string{"Check", "Search"}) {
		t.Fatal(got)
	}
}

// The fake lease keeps the contract every Release keeps (N2), and says what
// it saw.
func TestTheLeaseKeepsTheContract(t *testing.T) {
	ReleaseContract(t, func() (connections.Release, func()) {
		l := &Lease{}
		return l, l.Cut
	})
	l := &Lease{TooLate: true}
	if _, err := l.Start(context.Background()); !errors.Is(err, connections.ErrTooLate) {
		t.Fatal(err)
	}
	l.Finish(false)
	if started, finished, sent, tooLate := l.State(); started || !finished || sent || tooLate != 1 {
		t.Fatal(started, finished, sent, tooLate)
	}
}
