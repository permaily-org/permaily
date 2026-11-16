package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// The seams #137 added for the review of pull request 6 (#131), findings 2,
// 3 and 5 (ADR 0030, "Seams added while building"), consumed by the routes.

// V45 (review 1, finding 2): a refusal the API makes itself of a request
// whose token Check accepted (a body it cannot read, a formula it cannot
// parse, an attachment's index it cannot read) is recorded through Refuse,
// with the grant Check returned, before it is answered; the answer is the
// one it always was. When Refuse fails (the receipt not written), the
// answer is 503 with Retry-After and nothing else. A request refused before
// a grant (no token) is never one.
func TestV45TheAPIsOwnRefusalsAreRecordedBeforeTheyAreAnswered(t *testing.T) {
	a := newConnApp(t)
	grants(a.fake, apiGrant)
	var given []*oauth.Grant
	var fail error
	a.fake.RefuseFunc = func(_ context.Context, g *oauth.Grant, _ connections.Refusal) error {
		given = append(given, g)
		return fail
	}
	a.fake.SearchFunc = func(context.Context, *oauth.Grant, contracts.VendorSearch) (*contracts.VendorPage, connections.Release, error) {
		t.Fatal("the service was asked a search the route refused")
		return nil, nil, nil
	}
	long := "mbx_1." + strings.Repeat("a", connections.RefusalMessageMax)
	cases := []struct {
		method, path, body string
		status             int
		code               string
		want               connections.Refusal
	}{
		{"POST", "/v1/messages/search", `{"limit":`, 400, "invalid_body", connections.Refusal{Operation: connections.OperationSearch, Reason: receipts.RefusedInvalidBody}},
		{"POST", "/v1/messages/search", `{"other":1}`, 400, "invalid_body", connections.Refusal{Operation: connections.OperationSearch, Reason: receipts.RefusedInvalidBody}},
		{"POST", "/v1/messages/search", `{"limit":"ten"}`, 400, "invalid_body", connections.Refusal{Operation: connections.OperationSearch, Reason: receipts.RefusedInvalidBody}},
		{"POST", "/v1/messages/search", `{"cursor":7}`, 400, "invalid_body", connections.Refusal{Operation: connections.OperationSearch, Reason: receipts.RefusedInvalidBody}},
		{"POST", "/v1/messages/search", `{"query":{"all":[]}}`, 400, "invalid_formula", connections.Refusal{Operation: connections.OperationSearch, Reason: receipts.RefusedFormula}},
		{"GET", "/v1/messages/mbx_1.18e7/attachments/x", "", 404, "not_found", connections.Refusal{Operation: connections.OperationAttachment, Reason: receipts.RefusedOutsideScope, Message: "mbx_1.18e7"}},
		{"GET", "/v1/messages/mbx_1.18e7/attachments/01", "", 404, "not_found", connections.Refusal{Operation: connections.OperationAttachment, Reason: receipts.RefusedOutsideScope, Message: "mbx_1.18e7"}},
		{"GET", "/v1/messages/" + long + "/attachments/-1", "", 404, "not_found", connections.Refusal{Operation: connections.OperationAttachment, Reason: receipts.RefusedOutsideScope}},
	}
	for _, c := range cases {
		before := len(a.fake.Refusals())
		given = nil
		fail = nil
		want(t, a.vendor(apiToken, c.method, c.path, c.body), c.status, c.code)
		if got := a.fake.Refusals()[before:]; len(got) != 1 || got[0] != c.want || len(given) != 1 || given[0] != apiGrant {
			t.Fatalf("%s %s: refused %+v with %v", c.path, c.body, got, given)
		}
		// The receipt could not be written: 503, nothing of the refusal.
		fail = connections.ErrUnavailable
		rec := a.vendor(apiToken, c.method, c.path, c.body)
		want(t, rec, 503, "unavailable")
		if rec.Header().Get("Retry-After") != "5" || strings.Contains(rec.Body.String(), c.code) {
			t.Fatalf("%s: %v %s", c.path, rec.Header(), rec.Body)
		}
	}
	// Without a token, or with one not valid, nothing is recorded here.
	before := len(a.fake.Refusals())
	want(t, a.vendor("", "POST", "/v1/messages/search", `{"limit":`), 401, "unauthorized")
	want(t, a.vendor("pma_other", "GET", "/v1/messages/mbx_1.1/attachments/x", ""), 401, "invalid_token")
	if n := len(a.fake.Refusals()); n != before {
		t.Fatalf("%d refusals recorded without a grant", n-before)
	}
}

// V45 (review 1, finding 2): an attachment the grant has no part for is the
// service's to refuse (it records it with its evidence): the route passes
// it to Attachment and refuses nothing itself.
func TestV45AnAttachmentWithoutItsPartIsTheServicesToRefuse(t *testing.T) {
	a := newConnApp(t)
	narrow := *apiGrant
	narrow.Parts = []contracts.Part{contracts.PartSender, contracts.PartBody}
	narrow.Scopes = []string{oauth.ScopeSender, oauth.ScopeBody}
	grants(a.fake, &narrow)
	var asked *oauth.Grant
	a.fake.AttachmentFunc = func(_ context.Context, g *oauth.Grant, _ string, _ int) (*inbox.Attachment, connections.Release, error) {
		asked = g
		return nil, nil, contracts.ErrPartNotApproved
	}
	want(t, a.vendor(apiToken, "GET", "/v1/messages/mbx_1.18e7/attachments/0", ""), 403, "part_not_approved")
	if asked != &narrow || slices.Contains(a.fake.Calls(), "Refuse") {
		t.Fatalf("asked %v, calls %v", asked, a.fake.Calls())
	}
}

// V45 (review 1, finding 3): the step-up challenge names the connection's
// scopes and mail.attachments, even when a refresh narrowed the token: a
// client that follows it asks for what the connection had, and the
// attachments, never less.
func TestV45AStepUpNamesTheConnectionsScopes(t *testing.T) {
	a := newConnApp(t)
	narrowed := *apiGrant
	narrowed.Parts = []contracts.Part{contracts.PartSender, contracts.PartBody}
	narrowed.Scopes = []string{oauth.ScopeSender, oauth.ScopeBody}
	narrowed.ConnectionScopes = []string{oauth.ScopeSender, oauth.ScopeSubject, oauth.ScopeBody}
	grants(a.fake, &narrowed)
	a.fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return nil, nil, fmt.Errorf("refused: %w", contracts.ErrPartNotApproved)
	}
	rec := a.vendor(apiToken, "GET", "/v1/messages/mbx_1.18e7/attachments/0", "")
	want(t, rec, 403, "part_not_approved")
	const step = `Bearer error="insufficient_scope", resource_metadata="https://api.permaily.test/.well-known/oauth-protected-resource/v1", ` +
		`scope="mail.sender mail.subject mail.body mail.attachments"`
	if got := rec.Header().Get("WWW-Authenticate"); got != step {
		t.Fatalf("%s", got)
	}
}

// V43 (review 1, finding 5): the person's invalid_client says which of its
// three reasons, as its details and nothing else; the issuer's pushed
// request refused for its client stays the OAuth code alone.
func TestV43AnInvalidClientSaysItsReason(t *testing.T) {
	a := newConnApp(t)
	for _, p := range []contracts.ClientProblem{contracts.ClientIDInvalid, contracts.ClientDocumentInvalid, contracts.RedirectURIInvalid} {
		a.fake.StartFunc = func(context.Context, connections.Person, string) (*contracts.AuthorizationStarted, error) {
			return nil, fmt.Errorf("start: %w", contracts.InvalidClient(p))
		}
		rec := a.app("fresh", "POST", "/api/v1/oauth/requests", `{"query":"client_id=x"}`)
		want(t, rec, 400, "invalid_client")
		var body struct {
			Error struct {
				Details map[string]any `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Error.Details) != 1 || body.Error.Details["reason"] != string(p) {
			t.Fatalf("%s: %s", p, rec.Body)
		}
	}
	// An invalid_client with no reason it knows is answered without details.
	a.fake.StartFunc = func(context.Context, connections.Person, string) (*contracts.AuthorizationStarted, error) {
		return nil, contracts.InvalidClient("https://vendor.example/c")
	}
	rec := a.app("fresh", "POST", "/api/v1/oauth/requests", `{"query":"client_id=x"}`)
	want(t, rec, 400, "invalid_client")
	if strings.Contains(rec.Body.String(), "details") || strings.Contains(rec.Body.String(), "vendor.example") {
		t.Fatalf("%s", rec.Body)
	}
	// The issuer's endpoint: the code alone.
	a.fake.PushFunc = func(context.Context, url.Values) (string, error) {
		return "", fmt.Errorf("push: %w", contracts.InvalidClient(contracts.RedirectURIInvalid))
	}
	rec = a.form("/oauth/par", "client_id=https%3A%2F%2Fvendor.example%2Fc")
	if rec.Code != 401 || strings.TrimSpace(rec.Body.String()) != `{"error":"invalid_client"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if errors.Is(contracts.InvalidClient(contracts.RedirectURIInvalid), contracts.ErrInvalidClient) != true {
		t.Fatal("the seam changed")
	}
}
