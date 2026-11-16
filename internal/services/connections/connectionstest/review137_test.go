package connectionstest

import (
	"context"
	"errors"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// PR #137's review, "Refuse", notes 1, 2 and 4: an adapter passes Refuse
// exactly the grant Check returned for this request, and the fake hands
// that grant on unchanged; the step-up refusal of an attachment is MCP's
// alone (the API passes the request to Attachment, which decides it), so
// the fake, like the service, refuses it on an API grant; and what the
// service decides is never a refusal an adapter records.
func TestReview137RefuseTakesTheRequestsGrantAndLeavesTheServiceItsDecisions(t *testing.T) {
	ctx := context.Background()
	mcp := &oauth.Grant{Connection: "con_a", Family: "fam_a", Account: "acc_a", Resource: contracts.ResourceMCP}
	api := &oauth.Grant{Connection: "con_b", Family: "fam_b", Account: "acc_b", Resource: contracts.ResourceAPI}
	var seen []*oauth.Grant
	f := &Fake{RefuseFunc: func(_ context.Context, g *oauth.Grant, _ connections.Refusal) error {
		seen = append(seen, g)
		return nil
	}}
	part := connections.Refusal{Operation: connections.OperationAttachment, Reason: receipts.RefusedPartNotGiven, Message: "mbx_a.m1"}
	if err := f.Refuse(ctx, mcp, part); err != nil {
		t.Fatal(err)
	}
	if err := f.Refuse(ctx, api, part); !errors.Is(err, connections.ErrInvalidRefusal) {
		t.Fatal("the API's attachment without its part is Attachment's to refuse:", err)
	}
	body := connections.Refusal{Operation: connections.OperationRead, Reason: receipts.RefusedInvalidBody}
	if err := f.Refuse(ctx, api, body); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != mcp || seen[1] != api || seen[1].Connection != "con_b" || seen[1].Account != "acc_b" || seen[1].Resource != contracts.ResourceAPI {
		t.Fatalf("not the request's grant, unchanged: %+v", seen)
	}
	// Every decision the service makes with its evidence is refused here,
	// for any operation and either resource.
	for _, reason := range []receipts.RefusedReason{receipts.RefusedHeld, receipts.RefusedFormulaPart, receipts.RefusedModelUnavailable, receipts.RefusedTooLarge, receipts.RefusedTooLate} {
		for _, op := range []connections.Operation{connections.OperationSearch, connections.OperationRead, connections.OperationAttachment} {
			for _, g := range []*oauth.Grant{mcp, api} {
				if err := f.Refuse(ctx, g, connections.Refusal{Operation: op, Reason: reason}); !errors.Is(err, connections.ErrInvalidRefusal) {
					t.Errorf("%s %s on %s: %v", op, reason, g.Resource, err)
				}
			}
		}
	}
}
