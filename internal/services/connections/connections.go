// Package connections is the authorization server's state and the
// connections it makes (ADR 0019, how in ADR 0030): authorization requests
// waiting for the person, the person's decision, each connection's record
// and the account's list of them, codes and tokens, checking a token on
// every request, revoking, and what a connection reads, through
// inbox.Discloser, with the default rule (ADR 0031) applied first and a
// receipt written before anything leaves.
//
// Everything a later request needs is in the storage, so any request may land
// on either of two enclaves (ADR 0017, "Two enclaves at once"). What decides
// anything is in strict anchored records, which cannot be put back or hidden
// (ADR 0030, "Changed after the design review", 3 and 6, and after the
// second, C1, C2 and N6): a connection (its state, its devices, each one's
// code and the canonical identifier of its own scope), the account's list
// of them in epochs (with its closing fence, list.go), each token family's
// record (its current generation and the device's own scope,
// oauth.FamilyState), and the records of the client assertions accepted
// (oauth.AssertionRecord). A request, a pushed request, a code, an
// access token and a refresh token are also entries sealed with a key from
// the root secret, bound to where they are stored and named by the hash of
// their secret, for the lookup only: deleting one refuses it, putting back
// an older one is refused by the record that decides. A request and a
// pushed request are ended once by a mark at version 0 and in the enclave's
// memory: neither is an authority (a request's decision is the list's, and
// a pushed request replayed only shows the person the same request again).
// An assertion's jti is decided by its strict record, never a mark (C1).
// Memory holds only caches, read again after oauth.ConnectionFresh (a
// connection's record, an account's list and standing), what this enclave
// revoked since it started, the leases of the answers it is sending for
// each connection and device (Release, cut by a revocation here before its
// record is written), and limits.
//
// This file and api.go are the seam, committed with the design and revised
// after its review: what the service is given, what it gives (API,
// Lifecycle), the names of its records and entries, and its errors. ADR
// 0030's pull request 3 writes the service; connectionstest is the fake the
// routes and MCP are built against. It knows nothing of HTTP.
package connections

import (
	"context"
	"log/slog"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/notices"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/vendors"
)

// Config is what the service is given.
type Config struct {
	OAuth oauth.Config
	// Records keeps, in a store.Anchored with Strict set, read and written
	// at the version read (store.ErrChanged: read again, at most
	// RecordTries times), each made only with CreateNew: every connection
	// (RecordConnection) and every account's list of them (RecordList). A
	// record lost, forked or put back is refused, never read as none: its
	// tokens are refused, and the account's list cannot change until it
	// reads again.
	Records Records
	// Entries keeps the sealed entries (Partition*), each made at version 0
	// and ended by a mark at version 0 beside it.
	Entries store.Entries
	// Keys derives the keys that seal entries and sign records
	// (PurposeSeal, PurposeSign, PurposeCursor).
	Keys Keys
	// Vendors resolves client_id URLs and key sets (internal/vendors/cimd).
	Vendors vendors.Resolver
	// Accounts is what the service needs of accounts.
	Accounts Accounts
	// Mailboxes is what it needs of mailboxes.
	Mailboxes Mailboxes
	// Discloser gives what a connection reads of a mailbox; Asker, when set,
	// asks the model for it (the default rule and the query's own atoms).
	Discloser inbox.Discloser
	Asker     inbox.Asker
	// Model says whether the model may be asked now for an account; while it
	// may not, nothing leaves (the owner's decision 9).
	Model ModelGate
	// ModelNotice is the version of the model's notice in force (ADR 0028):
	// a connection approved under another asks nothing of the model and
	// gives nothing until renewed (finding 18).
	ModelNotice string
	// Rules are the rules every connection's reads apply first: in step 5,
	// rules.Default() alone.
	Rules []rules.Rule
	// Receipts writes the record of what left, before it leaves.
	Receipts receipts.Writer
	// Notices, when set, queues the notices about connections (ADR 0041,
	// notices.go): each after the change it tells of committed, under the
	// key its event names, holding the request at most
	// notices.EnqueueMax. Nil queues none (until the image gives it, ADR
	// 0041's pull request 7).
	Notices notices.Queue
	// Log logs what the service cannot do and goes on from: a notice not
	// queued, by its kind alone. Nil discards.
	Log *slog.Logger
	// Now is the clock; Random fills b with random bytes.
	Now    func() time.Time
	Random func(b []byte) error
}

// Records is the strict store.Anchored the composition gives the service.
type Records interface {
	GetRecordAt(ctx context.Context, name string) (*store.Record, store.Mark, error)
	PutRecordAt(ctx context.Context, r *store.Record, at store.Mark) error
	CreateNew(ctx context.Context, r *store.Record) error
	RemoveRecord(ctx context.Context, name string) error
}

// Keys derives keys for one purpose from the root secret.
type Keys interface {
	Derive(ctx context.Context, purpose string) ([]byte, error)
}

// Accounts is what the service reads of an account; *accounts.Service
// implements it (accounts/standing.go, committed with this seam, with its
// compile-time check). Its method is not mailboxes.Accounts' Standing,
// whose answer is another type: one service implements both.
type Accounts interface {
	// ConnectionStanding is the account's, read from its record now (the
	// caller caches it at most oauth.ConnectionFresh, and reads it again,
	// uncached, before it commits an approval): open, blocked, closing, gone,
	// or not agreed to the terms in force. An account that cannot be read
	// is an error, never a standing.
	ConnectionStanding(ctx context.Context, account string) (Standing, error)
}

// ModelGate says whether the model may be asked now for an account
// (services/decisions implements it, ADR 0030's pull request 4): the key set
// and accepted, no spend limit reached, the account's share left. Anything
// but inbox.AskAnswered means nothing leaves for that account's
// connections: contracts.ErrModelUnavailable (the owner's decision 9).
type ModelGate interface {
	Gate(ctx context.Context, account string) inbox.AskOutcome
}

// Standing is what an account's state allows connections.
type Standing int

const (
	// StandingOpen: connections read; a request may be approved.
	StandingOpen Standing = iota + 1
	// StandingNotAgreed: connections read; nothing may be approved until the
	// person agrees to the terms in force (contracts.ErrTermsChanged).
	StandingNotAgreed
	// StandingBlocked: no token is taken and nothing is approved; the
	// connections stay, and read again once unblocked.
	StandingBlocked
	// StandingClosing: the account is being deleted: its connections are
	// ended (contracts.EndedByAccount) and nothing is approved.
	StandingClosing
	// StandingGone: there is no such account, or it is due to be removed:
	// its tokens are refused and nothing is approved.
	StandingGone
)

// Mailboxes is what the service needs of mailboxes (services/mailboxes
// implements it): the account's mailboxes a formula may name, and opening
// one for a connection's read (mailboxes.AccessConnection, added by ADR 0030's
// pull request 4).
type Mailboxes interface {
	Catalogue(ctx context.Context, account string) ([]string, error)
	OpenForConnection(ctx context.Context, account, mailbox string) (inbox.Box, error)
}

// Record names (ADR 0030, "Its record and the account's list").
const (
	// RecordConnection + the connection's identifier ("con_…") is a
	// connection: readable and signed (vendor, client, state, dates,
	// resource, parts, mailboxes) with its query, mail since and what the
	// consent showed sealed inside it.
	RecordConnection = "connection-"
	// RecordList + the account's identifier + "-" + its epoch (decimal,
	// ListName) is the account's connections (ListState): for each its
	// identifier, client and state, which one is each client's current
	// one, the authorization requests decided in the last oauth.RequestTTL
	// (a request is decided once), and its closing fence (Closing and the
	// generation EndAll ended through); readable and signed, bounded
	// (ListActiveMax, ListEndedMax, ListDecidedMax, ListBytesMax) and in
	// epochs of ListEpochVersions (C2). Every approval writes it at the
	// version read before and after it makes a connection, so an approval
	// racing a deletion is refused or ended (finding 6).
	RecordList = "connections-"
	// RecordFamily + the family's identifier + "-" + its epoch (decimal) is
	// a token family's record (oauth.FamilyState), sealed: one device of a
	// connection, the only authority of which refresh token is current,
	// and where its own narrower scope is kept (N6).
	RecordFamily = "oauth-family-"
	// ConnectionRecordBytesMax bounds a connection's record's JSON before it
	// is signed and sealed, checked when a request is started with every
	// device it may have at oauth.DeviceEntryBytesMax
	// (contracts.ErrRequestTooLarge past it) and again by every write: the
	// consented formulas (each at most oauth.FormMaxBytes as pushed), what
	// the consent showed, and oauth.FamiliesMax active and
	// oauth.DevicesEndedKept ended devices (N6).
	ConnectionRecordBytesMax = 300 << 10
	// RecordTries bounds the reads again after store.ErrChanged.
	RecordTries = 5
)

// Partitions of the sealed entries, each followed by the hash of its secret
// (oauth.Kind's domain separation), with the entry at key "v". A request and
// a pushed request have their mark at key "used"; a code's and a refresh
// token's use is decided by their connection's and family's records, and a
// jti's by its record of assertions, never by an entry. PartitionJTI keeps
// the enclave's own list of the records of assertions it made, for the
// sweep to remove them once dead (oauth.RecordAssertions): it decides
// nothing.
const (
	PartitionRequest = "oauth#request#"
	PartitionPushed  = "oauth#pushed#"
	PartitionCode    = "oauth#code#"
	PartitionAccess  = "oauth#access#"
	PartitionRefresh = "oauth#refresh#"
	PartitionJTI     = "oauth#jti#"
)

// Purposes of the keys the service derives.
const (
	PurposeSeal   = "permaily oauth entries v1"
	PurposeSign   = "permaily connections v1"
	PurposeCursor = "permaily vendor cursors v1"
)

// Limits held in each enclave's memory (N times these with N enclaves; the
// deployment runs two).
const (
	// SearchesPerMinute and ReadsPerMinute bound one connection's searches
	// and reads (a read of a message or an attachment).
	SearchesPerMinute = 20
	ReadsPerMinute    = 120
	// AttachmentBytesPerHour bounds the attachments' bytes one connection
	// is given in an hour (before MCP's base64, which adds a third).
	AttachmentBytesPerHour = 200 << 20
	// TokenAdmissionPerMinute bounds the requests the token, revocation and
	// pushed request endpoints take before anything is authenticated, all
	// clients together: an abuse bound, never per client_id, which anyone
	// may name (finding 21 of the choices). Once a refresh token or a code
	// is found, RefreshesPerFamilyPerMinute bounds its family.
	TokenAdmissionPerMinute     = 600
	RefreshesPerFamilyPerMinute = 10
	// TransferMax bounds how long one answer may take to send once its
	// first byte is released (Release).
	TransferMax = 30 * time.Second
	// CursorTTL is how long a vendor's cursor is taken.
	CursorTTL = time.Hour
	// EndedKept is how long an ended connection's record stays, to be
	// shown, before it is removed (RemoveRecord).
	EndedKept = 30 * 24 * time.Hour
)

// kind is an error of the service of a fixed kind (domain.FixedKind): its
// words are written here and hold nothing of a request, so a log shows
// which it is however a caller wraps it with %w (ADR 0030, "Seams added
// while building": every other error is logged without its text).
type kind struct{ msg string }

func (k *kind) Error() string   { return k.msg }
func (k *kind) LogKind() string { return k.msg }

// Errors of the service, besides contracts' ones. A token that is not valid,
// whatever the reason, is ErrInvalidToken, and the reason is only counted
// (ADR 0030, "Checking a token").
var (
	ErrInvalidToken error = &kind{"connections: the token is not valid"}
	// ErrUnavailable is a check that could not read the storage: answered
	// 503, never 401, so a client keeps its tokens.
	ErrUnavailable error = &kind{"connections: the storage could not be read"}
	// ErrWrongResource is a valid token for the other resource.
	ErrWrongResource error = &kind{"connections: the token is for another resource"}
)

// OAuthError is a refusal of the token, revocation or pushed request
// endpoints, or of an authorization request after its redirect URI was
// proven: its code and nothing of what was sent.
type OAuthError struct {
	Code oauth.ErrorCode
}

func (e *OAuthError) Error() string { return "connections: " + string(e.Code) }

// LogKind is what a log says of it (domain.FixedKind): its code alone, so a
// refusal wrapped in text that carries a verifier, a state, an assertion or
// a jti is still logged as its code; a Code that is not one of the OAuth
// codes (oauth.ErrorCode.Valid) is never logged, only that it was a
// refusal (PR #137's review, finding 2).
func (e *OAuthError) LogKind() string {
	if !e.Code.Valid() {
		return "connections: an OAuth refusal"
	}
	return e.Error()
}
