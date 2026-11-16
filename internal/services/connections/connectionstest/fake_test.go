package connectionstest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/receipts"
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

// PR #131's review, finding 2: the fake takes an adapter's refusal of an
// attributed request as the service will (a fixed refusal, recorded with
// its grant before the adapter answers), refuses one outside the fixed set
// as the service will, and answers a failure the test sets (the adapter's
// 503).
func TestTheFakeRecordsAttributedRefusals(t *testing.T) {
	ctx := context.Background()
	g := &oauth.Grant{Connection: "con_a", Family: "fam_a", Resource: contracts.ResourceMCP}
	f := &Fake{}
	if err := f.Refuse(ctx, g, connections.Refusal{Operation: connections.OperationSearch, Reason: receipts.RefusedInvalidBody}); !errors.Is(err, ErrNotSet) {
		t.Fatal(err)
	}
	stored := errors.New("the receipt could not be written")
	var seen []*oauth.Grant
	f.RefuseFunc = func(_ context.Context, g *oauth.Grant, r connections.Refusal) error {
		seen = append(seen, g)
		if r.Operation == connections.OperationRead {
			return stored
		}
		return nil
	}
	part := connections.Refusal{Operation: connections.OperationAttachment, Reason: receipts.RefusedPartNotGiven, Message: "mbx_a.m1"}
	if err := f.Refuse(ctx, g, part); err != nil {
		t.Fatal(err)
	}
	if err := f.Refuse(ctx, g, connections.Refusal{Operation: connections.OperationRead, Reason: receipts.RefusedOutsideScope}); !errors.Is(err, stored) {
		t.Fatal(err)
	}
	if err := f.Refuse(ctx, g, connections.Refusal{Operation: connections.OperationRead, Reason: receipts.RefusedHeld}); !errors.Is(err, connections.ErrInvalidRefusal) {
		t.Fatal(err)
	}
	if err := f.Refuse(ctx, nil, part); !errors.Is(err, connections.ErrInvalidRefusal) {
		t.Fatal("a refusal without a grant is never recorded:", err)
	}
	if got := f.Refusals(); len(got) != 3 || got[1] != part || len(seen) != 2 || seen[0] != g || seen[1] != g {
		t.Fatalf("%+v %v", got, seen)
	}
	if got := f.Calls(); !slices.Equal(got, []string{"Refuse", "Refuse", "Refuse", "Refuse", "Refuse"}) {
		t.Fatal(got)
	}
}

// PR #131's review, finding 3: the grant a test's Check answers reaches the
// adapter whole, the connection's scopes with it, so a step-up built on the
// fake names what the service will.
func TestTheFakesGrantCarriesTheConnectionsScopes(t *testing.T) {
	f := &Fake{CheckFunc: func(context.Context, contracts.Resource, string) (*oauth.Grant, error) {
		return &oauth.Grant{Scopes: []string{oauth.ScopeSender, oauth.ScopeBody}, ConnectionScopes: []string{oauth.ScopeSender, oauth.ScopeSubject, oauth.ScopeBody}}, nil
	}}
	g, err := f.Check(context.Background(), contracts.ResourceAPI, "pma_x")
	if err != nil || !slices.Equal(g.StepUp(oauth.ScopeAttachments), []string{oauth.ScopeSender, oauth.ScopeSubject, oauth.ScopeBody, oauth.ScopeAttachments}) {
		t.Fatalf("%+v %v", g, err)
	}
}

// Ruling 3 of the fourth rulings on PR #136: the fake keeps the contract
// the service's Refuse keeps (ruling 6 on its second review), so the
// routes' and MCP's tests built on it see a refusal recorded as the
// service records it.
func TestTheFakesRefuseKeepsTheContract(t *testing.T) {
	RefuseContract(t, func(t *testing.T) RefuseSubject {
		g := &oauth.Grant{Connection: "con_a", Family: "fam_a", Account: "acc_a", Resource: contracts.ResourceMCP}
		f := &Fake{Mailboxes: []string{"mbx_a"}, RefuseFunc: func(ctx context.Context, _ *oauth.Grant, _ connections.Refusal) error {
			return ctx.Err() // as a writer that honours its context
		}}
		return RefuseSubject{API: f, Grant: g, Mailbox: "mbx_a", PassHardEnd: func() { g.TokenExpiresAt = time.Now().Add(-time.Second) }, Written: f.Receipts}
	})
}
