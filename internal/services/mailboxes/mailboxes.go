// Package mailboxes is the mailboxes of accounts (ADR 0018, how in ADR 0026):
// making one, its label and retention, Gmail's forwarding confirmation,
// disconnecting it and deleting its mail, pausing it, removing an account's
// mailboxes with the account, and taking mail in for it at RCPT TO and DATA.
// It knows nothing of HTTP or SMTP (depguard).
//
// This file is the seam, committed with the design: the interfaces this
// package needs from others, the types it shares, its errors and its limits.
// *Service, which implements httpapi.MailboxesService, accounts.Mailboxes and
// smtpd.Recipients, is written by pull request 1 of ADR 0026, and
// cmd/permaily-enclave composes them. mailboxestest has fakes of each side for
// the pull requests built at the same time.
package mailboxes

import (
	"context"
	"errors"
	"time"

	"github.com/permaily-org/permaily/internal/store"
)

// The limits of ADR 0026 ("Limits"). The ones kept in an anchored record hold
// exactly with any number of enclaves; the ones kept in memory are per enclave.
const (
	// MaxMailboxes is how many mailboxes an account has at most (decided by
	// the owner), counted from the account's list and the records it names.
	MaxMailboxes = 5
	// MaxMadePerPeriod mailboxes are made by an account in any Period, kept
	// in the account's list: it bounds the list's commits.
	MaxMadePerPeriod = 10
	Period           = 30 * 24 * time.Hour
	// MaxStateChangesPerPeriod pauses and resumes are made to one mailbox in
	// any Period, kept in its record: it bounds the record's commits.
	MaxStateChangesPerPeriod = 10
	// ReservationAge is how long an identifier added to the list may wait for
	// its record before a reader of the list cancels it.
	ReservationAge = 10 * time.Minute
	// IngestDeadline bounds a delivery from the end of DATA to the answer;
	// ReadDeadline (the HTTP server's write timeout) bounds a request that
	// opens mail. Neither opens mail after it.
	IngestDeadline = time.Minute
	ReadDeadline   = time.Minute
	// ClockSkew is how far the clocks of two enclaves may differ (ADR 0024).
	ClockSkew = time.Minute
	// SweepGrace is how long after disconnected_at the sweep makes its last
	// pass: an ingest that read the record just before the disconnect has
	// finished by then, whichever enclave's clock it ran on.
	SweepGrace = IngestDeadline + ClockSkew + time.Minute
	// SweepInterval is how often every enclave runs the sweep.
	SweepInterval = 10 * time.Minute
	// ClaimStale is how long a delivery's claim on a message stands before
	// another delivery of the same message takes it over, with the same
	// message identifier.
	ClaimStale = 2 * IngestDeadline
	// SeenFor is how long a message is remembered as stored: longer than a
	// sender retries (RFC 5321 suggests four to five days).
	SeenFor = 7 * 24 * time.Hour
)

// Caller is a person asking through a session. The adapter fills it in from
// the session it authenticated, and decides nothing.
type Caller struct {
	Account string
	// ConfirmedUntil is the session's (contracts.Session): Disconnect refuses
	// with contracts.ErrConfirmationNeeded once it has passed.
	ConfirmedUntil time.Time
}

// Standing is what decides which mailbox requests an account may make now
// (ADR 0026, "Access while an account is closing or has not agreed").
type Standing struct {
	// Closing: the person or the operator asked to delete the account, and
	// its seven days have not ended.
	Closing bool
	// TermsAgreed: the account agreed to the terms in force.
	TermsAgreed bool
}

// Accounts is what this package needs from accounts; *accounts.Service
// implements it (pull request 1 adds the method, in accounts/standing.go).
type Accounts interface {
	// Standing reads the account's signed record. A blocked account, or one
	// that is gone, is contracts.ErrUnauthorized: its sessions stand for
	// nothing. An account that cannot be read is an error, never "no account".
	Standing(ctx context.Context, accountID string) (Standing, error)
}

// Access is what a request does with a mailbox's mail, for Open. Which an
// account may do depends on its Standing:
//
//	                      agreed, open    closing            terms not agreed
//	AccessListMessages    yes             yes                yes (to find what to download)
//	AccessSearch          yes             yes                terms_needed
//	AccessReadMessage     yes             yes                terms_needed
//	AccessDownload        yes             yes                yes
//	AccessDeleteMessages  yes             account_closing    terms_needed
//
// An account both closing and not agreed may do what both columns allow; a
// refusal is account_closing when the closing column refuses, otherwise
// terms_needed.
type Access int

const (
	// AccessListMessages lists messages, the newest first, without a query.
	AccessListMessages Access = iota + 1
	// AccessSearch lists what a query finds.
	AccessSearch
	// AccessReadMessage opens one message: its text, its HTML in the frame,
	// its links.
	AccessReadMessage
	// AccessDownload downloads the raw message or one attachment, as a file.
	AccessDownload
	// AccessDeleteMessages deletes one message or up to a hundred.
	AccessDeleteMessages
)

// NoticeKind is a notice by mail about a mailbox (ADR 0026, "Notices by mail").
type NoticeKind string

const (
	NoticeAdded        NoticeKind = "mailbox_added"
	NoticeForwarding   NoticeKind = "forwarding_asked"
	NoticeDisconnected NoticeKind = "mailbox_disconnected"
	NoticePaused       NoticeKind = "mailbox_paused"
	NoticeResumed      NoticeKind = "mailbox_resumed"
)

// Notifier tells a person by mail. accounts implements it (pull request 4);
// until then the service is given none and tells nobody. It is called after
// the change is stored, returns nothing, and never takes longer than its own
// timeout (ADR 0017, notify): a notice never fails or holds up a change.
type Notifier interface {
	MailboxNotice(ctx context.Context, accountID string, kind NoticeKind, at time.Time)
}

// Records is what the service needs of the anchored records (a mailbox's, an
// account's list); *store.Anchored implements it once pull request 0 adds
// CreateNew.
type Records interface {
	// GetRecordAt reads a record and where it was read: store.ErrNotFound
	// when it was never made or was removed, store.ErrLost when its newest
	// value is gone, store.ErrForked when two commits are at its newest
	// version.
	GetRecordAt(ctx context.Context, name string) (*store.Record, store.Mark, error)
	// PutRecordAt writes a record only if nothing was committed for it since
	// the mark: store.ErrChanged otherwise.
	PutRecordAt(ctx context.Context, r *store.Record, at store.Mark) error
	// CreateNew makes a record only if no commit of this deployment was ever
	// made for its name, a removal included: store.ErrExists otherwise. It is
	// the fence of ADR 0026: a name made once, cancelled or removed is never
	// made again.
	CreateNew(ctx context.Context, r *store.Record) error
	// RemoveRecord commits that the record is gone and deletes every stored
	// version of it.
	RemoveRecord(ctx context.Context, name string) error
}

// The failures the SMTP adapter tells apart (smtpd.Recipients). Every other
// error of a delivery is a temporary failure.
var (
	// ErrNoSuchMailbox refuses a recipient: never made, disconnected, paused,
	// another domain, a +tag, a record that cannot be read. One answer for
	// all (550 5.1.1).
	ErrNoSuchMailbox = errors.New("mailboxes: no such mailbox")
	// ErrSlowDown refuses a recipient over its mailbox's limits for now
	// (451 4.7.1).
	ErrSlowDown = errors.New("mailboxes: too much mail for this mailbox now")
	// ErrNotAllStored is a delivery after which at least one accepted
	// recipient's mailbox does not hold the message: the transaction is
	// answered with a temporary failure (451 4.3.0), and the sender's retry
	// stores nothing twice (ADR 0026, "Delivery").
	ErrNotAllStored = errors.New("mailboxes: the message is not stored for every recipient")
)
