package connections

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// PR #131's review, finding 2: a refusal an adapter makes of an attributed
// request is one of a fixed set, each what only the adapter can see and a
// reason a refused receipt records (ADR 0031); everything the service can
// decide stays the service's, and nothing else is accepted.
func TestARefusalIsOfAFixedSet(t *testing.T) {
	valid := []Refusal{
		{Operation: OperationSearch, Reason: receipts.RefusedInvalidBody},
		{Operation: OperationSearch, Reason: receipts.RefusedFormula},
		{Operation: OperationRead, Reason: receipts.RefusedInvalidBody},
		{Operation: OperationRead, Reason: receipts.RefusedOutsideScope, Message: "mbx_a.0123456789abcdef0123456789abcdef"},
		{Operation: OperationAttachment, Reason: receipts.RefusedInvalidBody},
		{Operation: OperationAttachment, Reason: receipts.RefusedOutsideScope},
		{Operation: OperationAttachment, Reason: receipts.RefusedPartNotGiven, Message: "mbx_a.m1"},
	}
	for _, r := range valid {
		if !r.Valid() {
			t.Errorf("%+v refused", r)
		}
	}
	for _, r := range []Refusal{
		{},
		{Operation: "delete", Reason: receipts.RefusedInvalidBody},
		{Operation: OperationRead, Reason: "the vendor's words"},
		{Operation: OperationSearch, Reason: receipts.RefusedFormulaPart},  // the service checks a parsed formula's parts
		{Operation: OperationSearch, Reason: receipts.RefusedOutsideScope}, // a search names no message
		{Operation: OperationRead, Reason: receipts.RefusedPartNotGiven},   // a read gives what it may; never refused for a part
		{Operation: OperationRead, Reason: receipts.RefusedHeld},           // the service's, with its evidence
		{Operation: OperationRead, Reason: receipts.RefusedModelUnavailable},
		{Operation: OperationRead, Reason: receipts.RefusedTooLate},
		{Operation: OperationAttachment, Reason: receipts.RefusedTooLarge},
		{Operation: OperationSearch, Reason: receipts.RefusedInvalidBody, Message: "mbx_a.m1"},
		{Operation: OperationRead, Reason: receipts.RefusedOutsideScope, Message: strings.Repeat("a", RefusalMessageMax+1)},
	} {
		if r.Valid() {
			t.Errorf("%+v accepted", r)
		}
	}
}

// PR #131's review, seam gap 2: an OAuth refusal is a fixed kind, so text
// wrapped around it (a verifier, a state, an assertion, a jti) never reaches
// a log.
func TestAnOAuthRefusalIsLoggedAsItsCode(t *testing.T) {
	err := fmt.Errorf("exchange with verifier dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk: %w", &OAuthError{Code: oauth.ErrInvalidGrant})
	if got := domain.Redacted(err); got != "connections: invalid_grant" {
		t.Fatalf("%q", got)
	}
	if !errors.Is(fmt.Errorf("x: %w", ErrInvalidRefusal), ErrInvalidRefusal) {
		t.Fatal("ErrInvalidRefusal")
	}
}
