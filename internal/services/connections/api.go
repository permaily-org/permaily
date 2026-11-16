package connections

import (
	"context"
	"errors"
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
	// What a vendor reads, in "Release"'s order (finding 5, N3): the
	// model's gate; the disclosure, prepared in memory; the authority again
	// when the grant's freshness passed during the preparation (a strict
	// read; revoked or ended: nothing is written but a refusal); the
	// receipt, under ReceiptWriteMax and the grant's hard end; then the
	// answer and its Release, through which alone it may be sent.
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

// Release is the lease under which one prepared answer of a vendor read is
// sent (ADR 0030, "Release"; revised after the second design review, N2 and
// N3, and the owner's decision 14). The service makes it, registered under
// the connection and the device before the answer is returned, and owns
// it: its order with a revocation in this enclave is the lease's lock
// (Revoke cuts every lease of the connection, or of the device, before it
// writes the record, so no first byte follows a revocation here and a
// started answer is cut). The routes and MCP call Start immediately before
// the first byte and Finish when the transfer ended, and write every byte
// only under Start's context: a write when it has ended is not made, and
// when it ends during a write the adapter aborts the response (HTTP:
// http.ResponseController.SetWriteDeadline to now, so a blocked write
// returns, then panic(http.ErrAbortHandler); MCP: the same around the
// SDK's writer). An adapter that cannot start (the context its own request
// ended, a write that failed before the first byte) calls Finish(false)
// without Start.
type Release interface {
	// Start checks the authority again, once: this enclave's revocations,
	// the token's expiry and the connection's end (oauth.Grant.Expires,
	// hard: never released after them), and, when the grant's freshness
	// (oauth.Grant.FreshUntil) has passed, the connection's record, the
	// account's list and standing read again, strictly. It answers the
	// context every write of the answer runs under, which ends when this
	// enclave revokes the connection or the device, when the authority,
	// read again every oauth.ConnectionFresh while the answer is sent, is
	// no longer active or cannot be read, when the token expires or the
	// connection ends, or at TransferMax: so no byte leaves another enclave
	// more than ConnectionFresh after a read that found the connection
	// active (the owner's decision 14). ErrTooLate when the answer may not
	// start: the adapter answers 503 with nothing of it, and the lease has
	// written the too_late receipt naming the receipt of the answer.
	Start(ctx context.Context) (context.Context, error)
	// Finish ends the lease: sent says every byte was written. It is
	// called once, after Start or instead of it; a lease never finished is
	// ended at TransferMax.
	Finish(sent bool)
}

// ErrTooLate is Release.Start's answer when the answer prepared may no
// longer start: revoked, expired, ended, or its authority could not be
// read again. The adapter answers 503 with Retry-After (never 401, so a
// client keeps its tokens when the storage failed; a revoked one is told
// invalid_token on its next request).
var ErrTooLate = errors.New("connections: the answer can no longer be released")

// ReceiptWriteMax bounds writing one receipt (receipts.Writer), on its own
// deadline, not the grant's freshness (N3): a preparation that outlasts the
// freshness is checked again by Release.Start, after the receipt, never
// skipped to meet a deadline. The receipt's context also ends at the
// grant's hard end (oauth.Grant.Expires).
const ReceiptWriteMax = 5 * time.Second
