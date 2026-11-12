package accounts

import (
	"context"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Mailboxes is what accounts needs of the mailboxes of an account (ADR 0026);
// *mailboxes.Service implements it (pull request 1), and accounts is given it
// by cmd/permaily-enclave (Config.Mailboxes, or SetMailboxes once before
// serving, since the mailboxes service is made with accounts as its Accounts).
//
// None of these methods asks who is acting: they are capabilities, and
// accounts is the only caller. It wraps the operator's half twice, as it does
// for accounts (operator.go, admins.go): the operator with the admin token
// (counted, logged by kind) and an admin (a fresh proof for a change, never a
// mailbox of an admin's account unless the super admin acts, counted, logged
// by kind, and written in the admin log with the account and the mailbox).
// Those wrappers are httpapi.MailboxOperator, written by pull request 4.
// Never give this interface to an adapter.
type Mailboxes interface {
	// RemoveAccount closes the account's list, so that nothing is added to it
	// any more, then disconnects every mailbox it names (no notice; by
	// "account_removed"), cancels every reservation in it, and removes the
	// list. Every step can be done twice and by two enclaves at once; a
	// failure is returned and the account's removal is tried again. An
	// account without a list has nothing to remove.
	RemoveAccount(ctx context.Context, accountID string) error
	// Count is how many mailboxes the account has (AccountRecord.Mailboxes):
	// the ones the list names that are not disconnecting, cancelled or gone.
	Count(ctx context.Context, accountID string) (int, error)
	// Total is how many mailboxes the deployment has (Stats.Mailboxes), from
	// the index that proves nothing (ADR 0026): it may be off by a creation
	// or a disconnect that failed in between.
	Total(ctx context.Context) (int, error)
	// Records lists the account's mailboxes as the operator sees them, the
	// oldest first. contracts.ErrNotFound for an identifier that is not an
	// account's.
	Records(ctx context.Context, accountID string) (*contracts.MailboxRecordList, error)
	// ByAddress is the mailbox an address leads to, as a list of none or one:
	// the address is hashed as RCPT TO hashes it, the pointer followed and
	// the record must bear it out. An address that is not one is an empty
	// list, never an error that tells it apart.
	ByAddress(ctx context.Context, address string) (*contracts.MailboxRecordList, error)
	// Record is one mailbox as the operator sees it; its Account is whose
	// it is, read from its signed record (for the admin's guard).
	// contracts.ErrNotFound for one that is not there or is disconnecting.
	Record(ctx context.Context, id string) (*contracts.MailboxRecord, error)
	// Pause makes the mailbox refuse new mail, at the version read, and tells
	// its person; one paused already stays as it is and nobody is told.
	// contracts.ErrNotFound for one that is not there or is disconnecting;
	// contracts.ErrTooManyRequests past MaxStateChangesPerPeriod.
	Pause(ctx context.Context, id string) (*contracts.MailboxRecord, error)
	// Resume undoes Pause, with the same errors.
	Resume(ctx context.Context, id string) (*contracts.MailboxRecord, error)
}
