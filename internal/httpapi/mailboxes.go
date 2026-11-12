package httpapi

import (
	"context"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// MailboxesService is what the person's routes under /api/v1/mailboxes ask of
// the service (ADR 0026, "The API"); *mailboxes.Service implements it (pull
// request 1), and pull request 2 writes the routes on it and on an
// inbox.Reader. The adapter authenticates the session, fills a
// mailboxes.Caller from it and decides nothing else: who owns a mailbox,
// what its state allows and what the account's standing allows are decided
// here, by the service.
//
// Every method checks in this order and answers the first failure
// (ADR 0026, "The API"), so a stranger learns nothing a missing mailbox
// would not tell them:
//
//  1. contracts.ErrUnauthorized: the account is blocked or gone.
//  2. contracts.ErrNotFound: the mailbox is not the caller's account's, was
//     never made, or is leaving (disconnecting, cancelled, gone), except
//     for Disconnect, below.
//  3. The account's standing: contracts.ErrAccountClosing, then
//     contracts.ErrTermsChanged (mailboxes.Access).
//  4. contracts.ErrConfirmationNeeded (Disconnect only).
//  5. The mailbox's record: contracts.ErrMailboxUnreadable (lost or
//     forked), contracts.ErrMailboxForked (Disconnect of a forked one).
//  6. The request's own input: contracts.ErrInvalidLabel,
//     contracts.ErrInvalidRetention.
//  7. Limits: contracts.ErrTooManyRequests, then
//     contracts.ErrTooManyMailboxes.
//
// mailboxestest.Contract runs these cases against mailboxestest.Fake and,
// from pull request 1, against *mailboxes.Service.
type MailboxesService interface {
	// List is the account's mailboxes, the oldest first, without the ones
	// being disconnected; one whose record cannot be read is listed with
	// state "unreadable" so it can be disconnected. Allowed in every standing.
	// Reading the list finishes what a disconnect or a creation left undone
	// (ADR 0026, "State machines").
	List(ctx context.Context, c mailboxes.Caller) (*contracts.MailboxList, error)
	// Create makes a mailbox. contracts.ErrAccountClosing while the account
	// is closing, contracts.ErrTermsChanged while the terms in force are not
	// agreed, contracts.ErrTooManyMailboxes at mailboxes.MaxMailboxes,
	// contracts.ErrTooManyRequests past mailboxes.MaxMadePerPeriod,
	// contracts.ErrInvalidLabel.
	Create(ctx context.Context, c mailboxes.Caller, in contracts.MailboxCreate) (*contracts.Mailbox, error)
	// Get is one mailbox. Allowed in every standing.
	Get(ctx context.Context, c mailboxes.Caller, id string) (*contracts.Mailbox, error)
	// Update changes the label or the retention: contracts.ErrAccountClosing,
	// contracts.ErrTermsChanged, contracts.ErrInvalidLabel,
	// contracts.ErrInvalidRetention, contracts.ErrMailboxUnreadable.
	Update(ctx context.Context, c mailboxes.Caller, id string, in contracts.MailboxUpdate) (*contracts.Mailbox, error)
	// Disconnect stops the mailbox and deletes its mail (ADR 0026,
	// "Disconnecting"). Allowed in every standing (leaving never needs the
	// terms); contracts.ErrConfirmationNeeded once c.ConfirmedUntil has
	// passed. It always ends in a terminal commit of the mailbox's record
	// before the slot is freed and before it answers: a readable record goes
	// through disconnecting; a lost one is removed directly. A mailbox of the
	// caller's that is disconnected already, by this request or another, is
	// nil: the answer is the same. One whose record is forked takes no
	// commit: contracts.ErrMailboxForked, and it stays listed and counted.
	// One that is not the caller's, or never existed, is
	// contracts.ErrNotFound, the same for both.
	Disconnect(ctx context.Context, c mailboxes.Caller, id string) error
	// Open checks that the mailbox is the caller's, that its record can be
	// read and that the account's standing allows a, and returns it opened
	// for one request (mailboxes.Access says which standing allows what):
	// contracts.ErrAccountClosing, contracts.ErrTermsChanged,
	// contracts.ErrMailboxUnreadable. A paused mailbox opens: pausing stops
	// new mail only.
	Open(ctx context.Context, c mailboxes.Caller, id string, a mailboxes.Access) (inbox.Box, error)
}
