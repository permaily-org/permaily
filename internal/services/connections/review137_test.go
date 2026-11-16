package connections

import (
	"fmt"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// PR #137's review, finding 2 ("arbitrary implementation of FixedKind"): an
// OAuth refusal is logged as its code only when the code is one of the
// OAuth codes; anything else put in Code is logged as a fixed word, never
// verbatim, whatever wraps it.
func TestReview137AnOAuthRefusalLogsOnlyAKnownCode(t *testing.T) {
	for _, code := range []oauth.ErrorCode{"pma_PrivateMarkAbZ_x-", "invalid_grant state=PrivateMark", "", "INVALID_GRANT"} {
		err := fmt.Errorf("refresh with PrivateMark: %w", &OAuthError{Code: code})
		got := domain.Redacted(err)
		if strings.Contains(got, "PrivateMark") || got != "connections: an OAuth refusal" {
			t.Errorf("%q: %q", code, got)
		}
	}
	for _, code := range []oauth.ErrorCode{oauth.ErrInvalidGrant, oauth.ErrInvalidClient, oauth.ErrInsufficientScope, oauth.ErrTemporarily} {
		if got := domain.Redacted(fmt.Errorf("x: %w", &OAuthError{Code: code})); got != "connections: "+string(code) {
			t.Errorf("%q: %q", code, got)
		}
	}
}

// PR #137's review, "Refuse", note 2: the attachment's step-up is a refusal
// MCP alone makes; every other valid refusal is either adapter's, and what
// is not Valid is valid on neither.
func TestReview137TheStepUpRefusalIsMCPs(t *testing.T) {
	part := Refusal{Operation: OperationAttachment, Reason: receipts.RefusedPartNotGiven}
	if !part.ValidOn(contracts.ResourceMCP) || part.ValidOn(contracts.ResourceAPI) || part.ValidOn("") {
		t.Fatal("the step-up refusal")
	}
	body := Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody}
	if !body.ValidOn(contracts.ResourceMCP) || !body.ValidOn(contracts.ResourceAPI) {
		t.Fatal("a body either adapter cannot read")
	}
	held := Refusal{Operation: OperationRead, Reason: receipts.RefusedHeld}
	if held.ValidOn(contracts.ResourceMCP) || held.ValidOn(contracts.ResourceAPI) {
		t.Fatal("the service's decision")
	}
}
