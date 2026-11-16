// Package connections is the authorization server's state and the
// connections it makes (ADR 0019, how in ADR 0030): authorization requests
// waiting for the person, the person's decision, each connection's record
// and the account's list of them, codes and tokens, checking a token on
// every request, revoking, and what a connection reads, through
// inbox.Discloser, with the default rule (ADR 0031) applied first and a
// receipt written before anything leaves.
//
// Everything a later request needs is in the storage, so any request may land
// on either of two enclaves (ADR 0017, "Two enclaves at once"): a connection
// and the account's list are strict anchored records, so a revocation cannot
// be undone by putting back an earlier copy, and a fork refuses them; a
// request, a code, a token, a pushed request and a used assertion are
// entries sealed with a key from the root secret, bound to where they are
// stored, named by the hash of their secret and ended once by a mark at
// version 0 (the pattern of services/accounts' pending.go). Memory holds only
// caches, read again after oauth.ConnectionFresh (a connection's record, an
// account's standing), what this enclave revoked since it started, and
// limits.
//
// This file is the seam, committed with the design: what the service is
// given, the names of its records and entries, and its errors. ADR 0030's
// pull request 3 writes the service; "The contract and the seams" there
// gives its methods. It knows nothing of HTTP.
package connections

import (
	"context"
	"errors"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/rules"
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
	// Rules are the rules every connection's reads apply first: in step 5,
	// rules.Default() alone.
	Rules []rules.Rule
	// Receipts writes the record of what left, before it leaves.
	Receipts receipts.Writer
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

// Accounts is what the service reads of an account (services/accounts
// implements it): whether its connections may read, whether it may approve,
// and the proof of a session.
type Accounts interface {
	// Standing is the account's, read from its record (cached at most
	// oauth.ConnectionFresh by the caller): open, blocked, closing, or not
	// agreed to the terms in force.
	Standing(ctx context.Context, account string) (Standing, error)
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
	// RecordList + the account's identifier is the account's connections:
	// for each its identifier, client and state, and which one is each
	// client's current one; readable and signed.
	RecordList = "connections-"
	// RecordTries bounds the reads again after store.ErrChanged.
	RecordTries = 5
)

// Partitions of the sealed entries, each followed by the hash of its secret
// (oauth.Kind's domain separation), with the entry at key "v" and its mark
// at key "used".
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

// Limits held in each enclave's memory (N times these with N enclaves).
const (
	// SearchesPerMinute and ReadsPerMinute bound one connection's searches
	// and reads (a read of a message or an attachment).
	SearchesPerMinute = 20
	ReadsPerMinute    = 120
	// TokenRequestsPerMinute bounds one client's requests to the token
	// endpoint.
	TokenRequestsPerMinute = 60
	// CursorTTL is how long a vendor's cursor is taken.
	CursorTTL = time.Hour
	// EndedKept is how long an ended connection's record stays, to be
	// shown, before it is removed (RemoveRecord).
	EndedKept = 30 * 24 * time.Hour
)

// Errors of the service, besides contracts' ones. A token that is not valid,
// whatever the reason, is ErrInvalidToken, and the reason is only counted
// (ADR 0030, "Checking a token").
var (
	ErrInvalidToken = errors.New("connections: the token is not valid")
	// ErrUnavailable is a check that could not read the storage: answered
	// 503, never 401, so a client keeps its tokens.
	ErrUnavailable = errors.New("connections: the storage could not be read")
	// ErrWrongResource is a valid token for the other resource.
	ErrWrongResource = errors.New("connections: the token is for another resource")
)

// OAuthError is a refusal of the token, revocation or pushed request
// endpoints, or of an authorization request after its redirect URI was
// proven: its code and nothing of what was sent.
type OAuthError struct {
	Code oauth.ErrorCode
}

func (e *OAuthError) Error() string { return "connections: " + string(e.Code) }

// What the routes and the MCP tools are given by the service (ADR 0030's pull
// request 3 writes the methods): Start, Preview and Decide for the consent
// screen; List, Get, Revoke and Renew for the person; Push, Token and
// RevokeToken for the endpoints; Check for every request of a vendor; and
// Info, Search, Read and Attachment for what it reads. Their signatures are
// in ADR 0030, "The functions each pull request writes".
