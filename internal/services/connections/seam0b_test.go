package connections

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// What the service does with the seams of #137 ("0b"; ADR 0030, "Seams
// added while building"), as its row (pull request 3) consumes them.

// The person's invalid_client is a contracts.ClientError with its reason,
// a fixed kind in a log, whichever check refused the client.
func TestSeam0bStartAnswersTheClientsReason(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	p := w.person(accountA)
	reasonOf := func(t *testing.T, query string) contracts.ClientProblem {
		t.Helper()
		_, err := s.Start(context.Background(), p, query)
		var ce *contracts.ClientError
		if !errors.As(err, &ce) || !errors.Is(err, contracts.ErrInvalidClient) {
			t.Fatalf("not a client error: %v", err)
		}
		if got := domain.Redacted(fmt.Errorf("start: %w", err)); got != ce.LogKind() || !strings.Contains(got, string(ce.Problem)) {
			t.Fatalf("logged as %q", got)
		}
		return ce.Problem
	}
	if got := reasonOf(t, "client_id=a&client_id=b"); got != contracts.ClientIDInvalid {
		t.Fatal(got)
	}
	w.vendors.clientErr = map[string]error{"https://unknown.example/c": errClientIDForTest}
	if got := reasonOf(t, url.Values{"client_id": {"https://unknown.example/c"}}.Encode()); got != contracts.ClientIDInvalid {
		t.Fatal(got)
	}
	w.vendors.clientErr = map[string]error{"https://broken.example/c": errDocumentForTest}
	if got := reasonOf(t, url.Values{"client_id": {"https://broken.example/c"}}.Encode()); got != contracts.ClientDocumentInvalid {
		t.Fatal(got)
	}
	x := w.ask(claudeCode)
	x.redirect = "https://evil.example/cb"
	if got := reasonOf(t, x.query()); got != contracts.RedirectURIInvalid {
		t.Fatal(got)
	}
}

// Check fills the connection's scopes, as its record lists them, whatever a
// refresh narrowed the token to: a step-up names them (Grant.StepUp).
func TestSeam0bCheckGivesTheConnectionsScopes(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	x.scope = "mail.subject mail.body"
	tok, _ := w.connect(s, w.person(accountA), x)
	narrow, err := w.refresh(s, x, tok.RefreshToken, oauth.ScopeSender, oauth.ScopeSubject)
	if err != nil {
		t.Fatal(err)
	}
	g := w.grant(s, x, narrow.AccessToken)
	if want := []string{oauth.ScopeSender, oauth.ScopeSubject, oauth.ScopeBody}; !slices.Equal(g.ConnectionScopes, want) {
		t.Fatalf("connection scopes %v, want %v", g.ConnectionScopes, want)
	}
	if slices.Contains(g.Scopes, oauth.ScopeBody) {
		t.Fatalf("the token was not narrowed: %v", g.Scopes)
	}
	if got, want := g.StepUp(oauth.ScopeAttachments), []string{oauth.ScopeSender, oauth.ScopeSubject, oauth.ScopeBody, oauth.ScopeAttachments}; !slices.Equal(got, want) {
		t.Fatalf("step-up %v, want %v", got, want)
	}
	// The grant's copy is its own.
	g.ConnectionScopes[0] = "changed"
	if again := w.grant(s, x, narrow.AccessToken); again.ConnectionScopes[0] != oauth.ScopeSender {
		t.Fatal("the grant shares the record's scopes")
	}
}

// V32: a refusal an adapter makes of an attributed request is recorded
// before it answers: the refused receipt with its fixed reason, the message
// only when it is one of the connection's mailboxes, and its count.
func TestV32AdapterRefusalsAreRecorded(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	in := mailboxID(1) + ".m001"
	for _, tc := range []struct {
		r                 Refusal
		mailbox, messages string
	}{
		{Refusal{Operation: OperationSearch, Reason: receipts.RefusedInvalidBody}, "", ""},
		{Refusal{Operation: OperationSearch, Reason: receipts.RefusedFormula}, "", ""},
		{Refusal{Operation: OperationRead, Reason: receipts.RefusedOutsideScope, Message: in}, mailboxID(1), "m001"},
		{Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody, Message: mailboxID(3) + ".m001"}, "", ""}, // another account's mailbox
		{Refusal{Operation: OperationAttachment, Reason: receipts.RefusedPartNotGiven, Message: in}, mailboxID(1), "m001"},
		{Refusal{Operation: OperationAttachment, Reason: receipts.RefusedOutsideScope, Message: "the vendor's words"}, "", ""},
	} {
		before := len(w.rcpt.of(receipts.KindRefused))
		counted := s.Refused()[string(tc.r.Reason)]
		if err := s.Refuse(ctx, g, tc.r); err != nil {
			t.Fatalf("%+v: %v", tc.r, err)
		}
		got := w.rcpt.of(receipts.KindRefused)
		if len(got) != before+1 {
			t.Fatalf("%+v: no receipt", tc.r)
		}
		last := got[len(got)-1]
		if last.Refused == nil || last.Refused.Reason != tc.r.Reason || last.Refused.Mailbox != tc.mailbox || last.Refused.Message != tc.messages {
			t.Fatalf("%+v: recorded %+v", tc.r, last.Refused)
		}
		if last.Connection != g.Connection || last.Device != g.Family || last.Resource != g.Resource || last.ClientID != g.ClientID || len(last.Delivered) != 0 {
			t.Fatalf("%+v: the receipt is not the grant's: %+v", tc.r, last)
		}
		if s.Refused()[string(tc.r.Reason)] != counted+1 {
			t.Fatalf("%+v: not counted", tc.r)
		}
	}
}

// V32: what is not a refusal an adapter makes on its grant's resource is
// refused, with nothing recorded; a receipt that cannot be written is
// ErrUnavailable (503).
func TestV32AdapterRefusalsTheServiceRefuses(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	x.resource = apiResource
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	before := w.rcpt.count()
	for _, r := range []Refusal{
		{Operation: OperationAttachment, Reason: receipts.RefusedPartNotGiven}, // MCP's alone
		{Operation: OperationRead, Reason: receipts.RefusedHeld},
		{Operation: "delete", Reason: receipts.RefusedInvalidBody},
	} {
		if err := s.Refuse(ctx, g, r); !errors.Is(err, ErrInvalidRefusal) {
			t.Fatalf("%+v: %v", r, err)
		}
	}
	if err := s.Refuse(ctx, nil, Refusal{Operation: OperationSearch, Reason: receipts.RefusedInvalidBody}); !errors.Is(err, ErrInvalidRefusal) {
		t.Fatal(err)
	}
	if w.rcpt.count() != before {
		t.Fatal("an invalid refusal was recorded")
	}
	w.rcpt.mu.Lock()
	w.rcpt.fail = errors.New("the receipts' storage is down")
	w.rcpt.mu.Unlock()
	if err := s.Refuse(ctx, g, Refusal{Operation: OperationSearch, Reason: receipts.RefusedInvalidBody}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

// The refusals the service records with reasons of its own use the
// receipts seam's constants (ruling 3 on PR #136).
func TestV32ServiceRefusalsUseTheReceiptsReasons(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	if _, _, err := s.Search(ctx, g, contracts.VendorSearch{Cursor: "bad"}); !errors.Is(err, contracts.ErrInvalidCursor) {
		t.Fatal(err)
	}
	if _, _, err := s.Search(ctx, g, contracts.VendorSearch{Limit: -1}); err == nil {
		t.Fatal("a negative limit")
	}
	var reasons []receipts.RefusedReason
	for _, r := range w.rcpt.of(receipts.KindRefused) {
		reasons = append(reasons, r.Refused.Reason)
	}
	if !slices.Contains(reasons, receipts.RefusedInvalidCursor) || !slices.Contains(reasons, receipts.RefusedInvalidLimit) {
		t.Fatalf("%v", reasons)
	}
}

// An error the operator must tell apart is a fixed kind, logged as its
// words whatever wraps it; an OAuth refusal of internal/oauth is converted
// at its call site, failing closed.
func TestSeam0bTheServicesErrorsAreFixedKinds(t *testing.T) {
	for _, err := range []error{ErrInvalidToken, ErrUnavailable, ErrWrongResource, ErrTooLate, ErrInvalidRefusal, errDocument, errBusy, errListFull, errScope, errRefusedAuthority, errLeaseStarted, errMissing} {
		if got := domain.Redacted(fmt.Errorf("pma_secret %s: %w", "state", err)); got != err.Error() {
			t.Errorf("%q logged as %q", err.Error(), got)
		}
	}
	other := &oauth.Error{Code: "pma_PrivateMark", Reason: "x"}
	if oauthCode(endpointError(other)) != oauth.ErrInvalidRequest || oauthCode(endpointError(errors.New("x"))) != oauth.ErrInvalidRequest ||
		oauthCode(endpointError(&oauth.Error{Code: oauth.ErrInvalidScope})) != oauth.ErrInvalidScope {
		t.Fatal("endpoint")
	}
	if redirectCode(other) != oauth.ErrServerError || redirectCode(errors.New("x")) != oauth.ErrServerError || redirectCode(&oauth.Error{Code: oauth.ErrInvalidScope}) != oauth.ErrInvalidScope {
		t.Fatal("redirect")
	}
	for err, want := range map[error]contracts.ClientProblem{
		errors.New("x"): contracts.RedirectURIInvalid,
		&oauth.Error{Code: oauth.ErrInvalidRequest}:                                           contracts.RedirectURIInvalid,
		&oauth.Error{Code: oauth.ErrInvalidClient, Reason: "the vendor's words"}:              contracts.RedirectURIInvalid,
		&oauth.Error{Code: oauth.ErrInvalidClient, Reason: string(contracts.ClientIDInvalid)}: contracts.ClientIDInvalid,
	} {
		if got := shownClient(err).Problem; got != want {
			t.Errorf("%v: %q", err, got)
		}
	}
}
