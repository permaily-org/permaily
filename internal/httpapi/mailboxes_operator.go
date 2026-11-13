package httpapi

import (
	"context"

	"github.com/permaily-org/permaily/internal/contracts"
)

// MailboxOperator is everything the operator's routes and the admin panel's
// can ask of the service about mailboxes (ADR 0026, "What the operator and
// admins see and do"): look at an account's mailboxes or find the one an
// address leads to, and pause and resume one. Nothing here reads mail,
// shows an address, a label or a Gmail account, disconnects a mailbox or
// deletes mail, and the handlers of those routes are given nothing else.
//
// Two implement it, as two implement OperatorService (pull request 4):
//
//   - *accounts.Service, for the operator with the admin token: nothing
//     limits whom it acts on; each change is counted for /status by its kind
//     (mailbox_paused, mailbox_resumed) and logged by its kind alone.
//   - *accounts.Admin, for one admin of the panel: a change needs a fresh
//     proof (contracts.ErrConfirmationNeeded); a mailbox of an admin's
//     account is acted on by the super admin alone (contracts.ErrSuperAdminOnly),
//     whose own account's mailboxes no other admin acts on either; each
//     change is counted and logged by kind as above, and written in the
//     admin log with the account and the mailbox by identifier. Looking
//     needs neither.
//
// Each answers contracts.ErrNotFound for an identifier that is not a
// mailbox (or an account, for MailboxRecords), and the mailbox as it is
// after a change.
type MailboxOperator interface {
	MailboxRecords(ctx context.Context, accountID string) (*contracts.MailboxRecordList, error)
	// MailboxRecordByAddress is a list of none or one: an address that leads
	// to no mailbox is an empty list, never an error.
	MailboxRecordByAddress(ctx context.Context, address string) (*contracts.MailboxRecordList, error)
	// PauseMailbox and ResumeMailbox answer contracts.ErrTooManyRequests past
	// ten changes of one mailbox in thirty days (mailboxes.MaxStateChangesPerPeriod).
	PauseMailbox(ctx context.Context, id string) (*contracts.MailboxChange, error)
	ResumeMailbox(ctx context.Context, id string) (*contracts.MailboxChange, error)
}
