package connections

import (
	"context"
	"net/url"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/receipts"
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
	// Refuse records a refusal the route or the MCP adapter made itself of a
	// request whose token Check accepted, before calling Search, Read or
	// Attachment (PR #131's review, finding 2; ADR 0031: once a grant is
	// attributed, every refusal of a request that asked for mail is
	// recorded): the refused receipt, written as the service writes its own
	// refusals' (under a fresh ReceiptWriteMax, detached from ctx's
	// cancellation, and not bound by the grant's hard end, which bounds only
	// a receipt of mail released: a refusal after it keeps its attribution;
	// ruling 6 on PR #136's second review), and its count in Refused,
	// before the adapter answers. Nil once stored; then the adapter answers
	// its refusal. Any error (the receipt not written within
	// ReceiptWriteMax, ErrInvalidRefusal): the adapter answers 503 with
	// Retry-After and nothing else. Only what the adapter alone can see is refused here
	// (Refusal.Valid); everything the service can decide (a held message,
	// the model's gate, a search on a part not given, the API's attachment
	// without its part, a size, a cursor, a limit) is passed to the
	// service, which records it.
	// Requests refused before a grant (no token, Check refusing it) and
	// the endpoints' refusals are Check's, Token's, RevokeToken's and
	// Push's to count, never pre-empted by the route.
	//
	// The grant is trusted service output, not a capability Refuse can
	// verify (PR #137's review, "Refuse"): the adapter passes exactly the
	// grant Check returned for this request, unchanged, never one of
	// another request nor one built from what the request says (its
	// account, connection or family). An adapter never turns an error the
	// service returned into a Refuse: a service decision stays the
	// service's, recorded by it with its evidence. MCP refuses a known
	// mail tool whose arguments the SDK rejected before the tool ran here
	// too; a JSON-RPC call naming no known mail operation has no operation
	// to record and is not invented one. The service checks what it can
	// (Refusal.ValidOn the grant's resource; Message against the
	// connection's mailboxes); the rest is the adapter's contract, shown
	// by its own tests.
	Refuse(ctx context.Context, g *oauth.Grant, r Refusal) error
	// Refused is the refusals' part of /status, by fixed reason.
	Refused() map[string]int64
}

// Operation is what an attributed vendor request asked for.
type Operation string

const (
	OperationSearch     Operation = "search"     // POST /v1/messages/search, search_mail
	OperationRead       Operation = "read"       // GET /v1/messages/{id}, read_message
	OperationAttachment Operation = "attachment" // GET /v1/messages/{id}/attachments/{index}, get_attachment
)

// Refusal is what an adapter refused of an attributed request, in fixed
// values only (API.Refuse). Message is the message identifier as the
// request named it ("<mailbox>.<message>", from the path or the tool's
// arguments) for a read or an attachment, "" when it named none or one
// past RefusalMessageMax; the service records it only when it is a
// message identifier of one of the connection's mailboxes, and otherwise
// records none, so nothing a vendor writes reaches a receipt.
type Refusal struct {
	Operation Operation
	Reason    receipts.RefusedReason
	Message   string
}

// RefusalMessageMax bounds Refusal.Message: a mailbox's identifier, a dot
// and a message's (32 hexadecimal characters), with room to spare.
const RefusalMessageMax = 128

// ErrInvalidRefusal is Refuse's answer to a Refusal that is not ValidOn
// its grant's resource, or to one without a grant.
var ErrInvalidRefusal error = &kind{"connections: not a refusal an adapter makes"}

// Valid says whether r is one an adapter may make: a known operation and a
// reason only the adapter sees. A body or tool arguments it cannot read
// (receipts.RefusedInvalidBody), for any operation; a search's formula it
// cannot parse (RefusedFormula; whether a parsed formula looks at a part
// the grant does not give is the service's to decide); a message
// identifier or an attachment's index it cannot read (RefusedOutsideScope,
// answered not_found, as one the connection does not read); and, for an
// attachment, MCP's step-up before the call reaches the SDK
// (RefusedPartNotGiven; the API passes the request to Attachment, which
// refuses it). Message only for a read or an attachment, within
// RefusalMessageMax.
func (r Refusal) Valid() bool {
	if len(r.Message) > RefusalMessageMax {
		return false
	}
	switch r.Operation {
	case OperationSearch:
		return r.Message == "" && (r.Reason == receipts.RefusedInvalidBody || r.Reason == receipts.RefusedFormula)
	case OperationRead:
		return r.Reason == receipts.RefusedInvalidBody || r.Reason == receipts.RefusedOutsideScope
	case OperationAttachment:
		return r.Reason == receipts.RefusedInvalidBody || r.Reason == receipts.RefusedOutsideScope || r.Reason == receipts.RefusedPartNotGiven
	}
	return false
}

// ValidOn says whether r is Valid and one the adapter of resource res
// makes: MCP's step-up for an attachment is MCP's alone (the API passes
// that request to Attachment, which refuses it with its evidence).
func (r Refusal) ValidOn(res contracts.Resource) bool {
	return r.Valid() && (r.Reason != receipts.RefusedPartNotGiven || res == contracts.ResourceMCP)
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
var ErrTooLate error = &kind{"connections: the answer can no longer be released"}

// ReceiptWriteMax bounds writing one receipt (receipts.Writer), on its own
// deadline, not the grant's freshness (N3): a preparation that outlasts the
// freshness is checked again by Release.Start, after the receipt, never
// skipped to meet a deadline. The receipt of mail to be released also ends
// at the grant's hard end (oauth.Grant.Expires): nothing is released past
// it. A refused receipt (Refuse's, and the service's own refusals) is
// written under a fresh ReceiptWriteMax alone, detached from the request's
// cancellation: it releases nothing, so it is recorded after the hard end
// too, with the grant's attribution and no message identifier the service
// did not check (ruling 6 on PR #136's second review).
const ReceiptWriteMax = 5 * time.Second
