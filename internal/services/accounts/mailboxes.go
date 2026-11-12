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
	// RemoveAccount always leaves the account's list closed: it closes the
	// list at its version, or, when the account never had one, makes it
	// closed (CreateNew), so a first creation still in flight loses that
	// race (store.ErrExists, then it reads the closed list and refuses) or
	// wins it and is then closed and enumerated here on the next try. Then
	// it ends every mailbox the list names in a terminal commit (no notice;
	// by "account_removed"): a readable one disconnected, a lost one
	// removed, a reservation fenced by its tombstone. A forked one takes no
	// commit and stays named in the closed list. The closed list is kept for
	// good: it names the account and, at most, mailboxes whose records were
	// forked. Every step can be done twice and by two enclaves at once; a
	// failure is returned and the account's removal is tried again.
	RemoveAccount(ctx context.Context, accountID string) error
	// Count is how many mailboxes the account has (AccountRecord.Mailboxes):
	// the ones the list names that are not disconnecting, cancelled or gone.
	Count(ctx context.Context, accountID string) (int, error)
	// Total is how many mailboxes the deployment has (Stats.Mailboxes), from
	// the index that proves nothing (ADR 0026): it may be off by a creation
	// or a disconnect that failed in between.
	Total(ctx context.Context) (int, error)
	// Records lists the account's mailboxes as the operator sees them, the
	// oldest first, without the leaving ones. It does not know accounts: an
	// account with no list, or an identifier that is no account, is an empty
	// list. The operator's and the admin's wrappers (pull request 4) answer
	// contracts.ErrNotFound for an identifier that is not an account before
	// asking.
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
	// changed says whether this call committed the change: what the answer
	// needs is read before the commit, so a call that committed never fails,
	// and only it is counted and logged. contracts.ErrNotFound for one that
	// is not there or is disconnecting; contracts.ErrTooManyRequests past
	// MaxStateChangesPerPeriod.
	Pause(ctx context.Context, id string) (rec *contracts.MailboxRecord, changed bool, err error)
	// Resume undoes Pause, with the same answers.
	Resume(ctx context.Context, id string) (rec *contracts.MailboxRecord, changed bool, err error)
}
