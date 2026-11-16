package connections

import (
	"context"
	"net/url"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
)

// API is what the routes (internal/httpapi, ADR 0030's pull request 6) and
// the MCP tools (internal/mcpapi, pull request 7) are given by the service;
// pull request 3's *Service implements it, with its compile-time check, and
// connectionstest.Fake stands for it until then (finding 16).
type API interface {
	// The consent screen, in the person's session.
	Start(ctx context.Context, p Person, query string) (*contracts.AuthorizationStarted, error)
	Preview(ctx context.Context, p Person, id string, in contracts.ConsentPreview) (*contracts.ConsentPreviewPage, error)
	Decide(ctx context.Context, p Person, id string, in contracts.AuthorizationDecision) (*contracts.AuthorizationAnswer, error)
	// The person's connections. List, Get, Revoke and RevokeDevice work
	// while the account has not agreed to new terms (finding 17); Renew
	// and Decide's Allow do not (contracts.ErrTermsChanged).
	List(ctx context.Context, p Person) (*contracts.ConnectionList, error)
	Get(ctx context.Context, p Person, id string) (*contracts.Connection, error)
	Revoke(ctx context.Context, p Person, id string) (*contracts.Connection, error)
	RevokeDevice(ctx context.Context, p Person, id, device string) (*contracts.Connection, error)
	Renew(ctx context.Context, p Person, id string, in contracts.ConnectionRenewal) (*contracts.Connection, error)
	// The endpoints: form values in, an OAuth answer out (*OAuthError for a
	// refusal).
	Push(ctx context.Context, form url.Values) (requestURI string, err error)
	Token(ctx context.Context, form url.Values) (*TokenAnswer, error)
	RevokeToken(ctx context.Context, form url.Values) error
	// Check is the check of every vendor request: ErrInvalidToken,
	// ErrWrongResource or ErrUnavailable, or the grant.
	Check(ctx context.Context, r contracts.Resource, bearer string) (*oauth.Grant, error)
	// What a vendor reads. Each prepares its answer, writes its receipt,
	// checks g's authority again (the local revocations, the deadline) and
	// only then answers with the Release its first byte must go out by
	// (finding 5); a vendor is answered nothing of it once that has passed.
	Info(ctx context.Context, g *oauth.Grant) (*contracts.VendorConnection, error)
	Search(ctx context.Context, g *oauth.Grant, in contracts.VendorSearch) (*contracts.VendorPage, Release, error)
	Read(ctx context.Context, g *oauth.Grant, id string) (*contracts.VendorMessage, Release, error)
	Attachment(ctx context.Context, g *oauth.Grant, id string, index int) (*inbox.Attachment, Release, error)
	// Refused is the refusals' part of /status, by fixed reason.
	Refused() map[string]int64
}

// Lifecycle is what accounts calls of the service (pull request 3 wires
// it; accounts.Service holds it like its mailboxes): EndAll when a deletion
// is asked (by the person or the operator) and on every pass of accounts'
// sweep while the account is closing; Reopen when a deletion is cancelled,
// which first finishes ending every connection of the closing generation
// and only then lifts the fence, so a cancelled deletion never brings a
// connection back (finding 6). Both are retried until they succeed; a
// cancellation whose Reopen fails is not done.
type Lifecycle interface {
	EndAll(ctx context.Context, account string) error
	Reopen(ctx context.Context, account string) error
}

// Person is who acts in the app: the account, the session that opened a
// request, and until when its proof is fresh (contracts.Session), as
// mailboxes.Caller is.
type Person struct {
	Account        string
	Session        string
	ConfirmedUntil time.Time
}

// TokenAnswer is the token endpoint's answer (RFC 6749 section 5.1);
// TokenType is always "Bearer". ExpiresIn is the access token's seconds
// left, under the connection's end; Scope is the access token's own.
type TokenAnswer struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// Release is when the answer of a vendor read may still start: its first
// byte goes out before Deadline (oauth.Grant.Deadline, checked again after
// the receipt was written), or the vendor is answered 503 and nothing of
// it; once started it is sent within TransferMax, and a revocation in this
// enclave cuts it.
type Release struct {
	Deadline time.Time
}
