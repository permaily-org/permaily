package connections

import (
	"context"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// A connection from start to a read, through two enclaves.
func TestAConnectionFromStartToARead(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	p := w.person(accountA)
	ask := w.ask(claudeCode)
	tok, req := w.connect(a, p, ask)
	if req.Change != contracts.ChangeNew || req.Vendor.Verified != "Anthropic" || !req.Loopback || req.RedirectHost != "localhost" {
		t.Fatalf("%+v", req)
	}
	g := w.grant(b, ask, tok.AccessToken)
	page, rel, err := b.Search(context.Background(), g, contracts.VendorSearch{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 0 {
		t.Fatalf("new mail only gave %d", len(page.Messages))
	}
	ctx, err := rel.Start(context.Background())
	if err != nil || ctx.Err() != nil {
		t.Fatal(err)
	}
	rel.Finish(true)
	next, err := w.refresh(b, ask, tok.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.check(a, ask, next.AccessToken); err != nil {
		t.Fatal(err)
	}
}
