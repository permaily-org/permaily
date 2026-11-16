package connections

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/jws"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// The second review of pull request 136, open choice (b) (ruling 6 of its
// third rulings): a refusal's receipt, the adapter's (Refuse) and the
// service's own, is written under a fresh ReceiptWriteMax, detached from
// the request's cancellation, and keeps its attribution after the grant's
// hard end; only a receipt of mail released is bound by that end. The
// reviewer's positive evidence is kept beside it.

// V32 (seam 0b, API.Refuse): exactly one receipt for an adapter's refusal
// and one for the service's own, also after the grant's hard end and with
// the request's context cancelled, each with the grant's attribution; a
// message identifier only when it is one of the connection's mailboxes.
func TestV32RefuseBoundaryAndExactlyOneReceipt(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	rf := Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody, Message: mailboxID(1) + ".m001"}
	ctx := context.Background()
	refused := func() []receipts.Receipt { return w.rcpt.of(receipts.KindRefused) }
	before := len(refused())
	if err := s.Refuse(ctx, g, rf); err != nil {
		t.Fatal(err)
	}
	if len(refused()) != before+1 {
		t.Fatal("not exactly one adapter receipt")
	}
	if _, _, err := s.Read(ctx, g, "not-a-message"); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatal(err)
	}
	if len(refused()) != before+2 {
		t.Fatal("not exactly one service receipt")
	}
	w.clock.add(oauth.AccessTokenTTL)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	for i, c := range []context.Context{ctx, cancelled} {
		if err := s.Refuse(c, g, rf); err != nil {
			t.Fatalf("after the hard end (%d): %v", i, err)
		}
		got := refused()
		if len(got) != before+3+i {
			t.Fatalf("after the hard end (%d): %d receipts", i, len(got)-before)
		}
		r := got[len(got)-1]
		if r.Connection != g.Connection || r.Device != g.Family || r.ClientID != g.ClientID || r.Resource != g.Resource ||
			r.Refused.Reason != receipts.RefusedInvalidBody || r.Refused.Mailbox != mailboxID(1) || r.Refused.Message != "m001" {
			t.Fatalf("after the hard end (%d): %+v %+v", i, r, r.Refused)
		}
	}
	// The service's own refusal after the hard end, its context cancelled:
	// recorded too, and answered as before.
	if _, _, err := s.Read(cancelled, g, mailboxID(1)+".m001"); !errors.Is(err, ErrTooLate) {
		t.Fatalf("a read after the hard end: %v", err)
	}
	got := refused()
	if len(got) != before+5 {
		t.Fatalf("the service's refusal after the hard end: %d receipts", len(got)-before)
	}
	if r := got[len(got)-1]; r.Connection != g.Connection || r.Device != g.Family || r.Refused.Reason != receipts.RefusedTooLate {
		t.Fatalf("the service's refusal after the hard end: %+v %+v", r, r.Refused)
	}
}

// V32: a refusal's receipt is bounded by ReceiptWriteMax alone: one that
// cannot be written within it is ErrUnavailable (503), for Refuse and the
// service's own.
func TestV32RefusalReceiptIsBoundedByReceiptWriteMax(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	ctx := context.Background()
	block := make(chan struct{})
	defer close(block)
	w.rcpt.mu.Lock()
	w.rcpt.block = block
	w.rcpt.mu.Unlock()
	began := time.Now()
	if err := s.Refuse(ctx, g, Refusal{Operation: OperationSearch, Reason: receipts.RefusedInvalidBody}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if took := time.Since(began); took < ReceiptWriteMax-100*time.Millisecond || took > ReceiptWriteMax+2*time.Second {
		t.Fatalf("Refuse's receipt waited %v, not ReceiptWriteMax", took)
	}
}

// Positive evidence of the review: OAuth and JWS refusals convert failing
// closed, wrapped or not, nil and unknown codes included.
func TestSeam0bOAuthRefusalConversionsFailClosed(t *testing.T) {
	errs := append([]error{nil, errors.New("unexpected"), &oauth.Error{Code: ""}, &oauth.Error{Code: "unknown"}}, jws.Refusals...)
	for _, err := range errs {
		if got := oauthCode(endpointError(err)); got != oauth.ErrInvalidRequest {
			t.Fatalf("endpoint %v: %s", err, got)
		}
		if got := redirectCode(err); got != oauth.ErrServerError {
			t.Fatalf("redirect %v: %s", err, got)
		}
		if got := shownClient(err).Problem; got != contracts.RedirectURIInvalid {
			t.Fatalf("screen %v: %s", err, got)
		}
	}
	for _, code := range []oauth.ErrorCode{oauth.ErrInvalidRequest, oauth.ErrInvalidClient, oauth.ErrInvalidScope, oauth.ErrInvalidGrant, oauth.ErrUnsupportedGrantType, oauth.ErrServerError, oauth.ErrTemporarily, oauth.ErrAccessDenied, oauth.ErrUnauthorizedClient, oauth.ErrUnsupportedResponse, oauth.ErrInvalidTarget, oauth.ErrInvalidToken, oauth.ErrInsufficientScope} {
		err := fmt.Errorf("wrapper: %w", &oauth.Error{Code: code})
		if oauthCode(endpointError(err)) != code || redirectCode(err) != code {
			t.Fatal(code)
		}
	}
}

// Positive evidence of the review: a device narrower than its connection
// keeps the connection's scopes for a step-up, and reads only its own parts.
func TestSeam0bNarrowerDeviceKeepsConnectionScopes(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	x.scope = "mail.sender mail.subject mail.body"
	w.connectAll(s, w.person(accountA), x)
	y := w.ask(claudeCode)
	y.scope = "mail.sender"
	tok, req := w.connect(s, w.person(accountA), y)
	if req.Change != contracts.ChangeAddDevice {
		t.Fatal(req.Change)
	}
	g := w.grant(w.enclave(), y, tok.AccessToken)
	if !slices.Equal(g.ConnectionScopes, []string{oauth.ScopeSender, oauth.ScopeSubject, oauth.ScopeBody}) || !slices.Equal(g.Scopes, []string{oauth.ScopeSender}) || !slices.Equal(g.Parts, []contracts.Part{contracts.PartSender}) {
		t.Fatalf("connection scopes %v; token scopes %v; effective parts %v", g.ConnectionScopes, g.Scopes, g.Parts)
	}
	if !slices.Equal(g.StepUp(oauth.ScopeAttachments), []string{oauth.ScopeSender, oauth.ScopeSubject, oauth.ScopeBody, oauth.ScopeAttachments}) {
		t.Fatal(g.StepUp(oauth.ScopeAttachments))
	}
}
