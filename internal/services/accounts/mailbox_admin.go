package accounts

import (
	"context"
	"regexp"
	"strings"

	"github.com/permaily-org/permaily/internal/contracts"
)

// What the operator and the admins see and do about mailboxes (ADR 0026,
// "What the operator and admins see and do"). They see how many mailboxes an
// account has and the deployment has, and the readable part of a mailbox's
// signed record: its identifier, whose it is, its state and dates, its
// retention and whether a provider vouched for it. Never its address, its
// label, the Gmail account behind it or its mail. They pause a mailbox and
// resume it, for abuse, and nothing else: they disconnect none, delete no
// mail, rename nothing and move nothing, since that would destroy or change
// what is the person's.
//
// The mailboxes service gives accounts its operator's half as capabilities
// with no one acting (Mailboxes, mailboxes.go). Here it is wrapped twice, as
// accounts' own operator's half is (operator.go, admins.go), and both
// wrappers are httpapi.MailboxOperator:
//
//   - *Service, for the operator with the admin token: nothing limits which
//     mailbox it acts on; each change is counted for /status and logged by
//     its kind alone.
//   - *Admin, for an admin of the panel: a change needs the fresh proof; the
//     owner of the mailbox is read from its record, then its account, and a
//     mailbox of an admin's account is acted on by the super admin alone,
//     whose own mailboxes no other admin acts on either (told before the
//     proof is asked for); each change is counted, logged by kind, and
//     written in the admin log with the account and the mailbox by their
//     identifiers. Looking needs no proof and writes no log.
//
// The person is told by mail of a pause and a resume by the mailboxes
// service, which accounts tells for it (MailboxNotice, notices.go).

// maxLookupAddress bounds the address a lookup passes on: longer than any
// address (RFC 5321 allows 254), it leads to no mailbox and is not asked.
const maxLookupAddress = 254

// mailboxIDRE is what a mailbox's identifier looks like ("mbx_" and 26
// letters and digits): anything else is no mailbox, and is not asked for.
var mailboxIDRE = regexp.MustCompile(`^mbx_[a-z0-9]{26}$`)

// mailboxCount is how many mailboxes an account has, none without mailboxes.
func (s *Service) mailboxCount(ctx context.Context, account string) (int, error) {
	if s.mailboxesOf() == nil {
		return 0, nil
	}
	return s.mailboxesOf().Count(ctx, account)
}

// countMailboxes puts the deployment's mailboxes in the counts, and says
// whether it could. What failed is logged through the one redactor.
func (s *Service) countMailboxes(ctx context.Context, out *contracts.Stats) bool {
	if s.mailboxesOf() == nil {
		return true
	}
	total, err := s.mailboxesOf().Total(ctx)
	if err != nil {
		s.cfg.Log.Error("accounts: the mailboxes could not be counted for the admin panel", "err", scrub(err))
		return false
	}
	out.Mailboxes.Total = total
	return true
}

// MailboxRecords lists an account's mailboxes as the operator sees them, the
// oldest first. An identifier that is not an account is ErrNotFound, before
// the mailboxes service is asked; an account with none is an empty list.
func (s *Service) MailboxRecords(ctx context.Context, accountID string) (*contracts.MailboxRecordList, error) {
	if !validID("acc", accountID) {
		return nil, contracts.ErrNotFound
	}
	doc, err := s.read(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, contracts.ErrNotFound
	}
	if s.mailboxesOf() == nil {
		return &contracts.MailboxRecordList{Mailboxes: []contracts.MailboxRecord{}}, nil
	}
	return s.mailboxesOf().Records(ctx, accountID)
}

// MailboxRecordByAddress is the mailbox an address leads to, as a list of
// none or one, for an abuse report that names an address: it tells which
// account owns an address the operator knows already. It is one exact
// address a request, hashed as RCPT TO hashes it (by the mailboxes service),
// and every address that leads to no mailbox, whatever it is, answers the
// same empty list. What bounds it as a way to test addresses is what bounds
// RCPT TO, which anyone on the internet can ask without a limit: an address
// is 100 random bits (ADR 0026, "The address"). Only a request that names no
// address at all is ErrInvalidAddress.
func (s *Service) MailboxRecordByAddress(ctx context.Context, address string) (*contracts.MailboxRecordList, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, contracts.ErrInvalidAddress
	}
	if s.mailboxesOf() == nil || len(address) > maxLookupAddress {
		return &contracts.MailboxRecordList{Mailboxes: []contracts.MailboxRecord{}}, nil
	}
	return s.mailboxesOf().ByAddress(ctx, address)
}

// PauseMailbox makes a mailbox refuse new mail, keeping what it holds; its
// person is told. One paused already stays as it is.
func (s *Service) PauseMailbox(ctx context.Context, id string) (*contracts.MailboxRecord, error) {
	return s.pauseMailbox(ctx, nil, id, true)
}

// ResumeMailbox undoes PauseMailbox.
func (s *Service) ResumeMailbox(ctx context.Context, id string) (*contracts.MailboxRecord, error) {
	return s.pauseMailbox(ctx, nil, id, false)
}

func (s *Service) pauseMailbox(ctx context.Context, by *acting, id string, pause bool) (*contracts.MailboxRecord, error) {
	if !mailboxIDRE.MatchString(id) || s.mailboxesOf() == nil {
		return nil, contracts.ErrNotFound
	}
	kind, act := ActionMailboxResumed, s.mailboxesOf().Resume
	if pause {
		kind, act = ActionMailboxPaused, s.mailboxesOf().Pause
	}
	if by != nil {
		// Whose it is, from its signed record, and whether this admin may act
		// on that account: told before being asked to confirm. A mailbox is
		// never moved to another account, so its owner does not change
		// between this and the change. Its role may, and is not checked
		// again: an owner made an admin in between has the change commit, as
		// the allowed order "change, then promotion" would (ADR 0026, "What
		// the operator and admins see and do").
		rec, err := s.mailboxesOf().Record(ctx, id)
		if err != nil {
			return nil, err
		}
		if validID("acc", rec.Account) {
			doc, err := s.read(ctx, rec.Account)
			if err != nil {
				return nil, err
			}
			if doc != nil {
				if err := by.guard(s, kind)(doc); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := by.changing(); err != nil {
		return nil, err
	}
	rec, changed, err := act(ctx, id)
	if err != nil {
		return nil, err
	}
	// Counted and logged by the call that committed the change, and by no
	// other: one that found it done already answers it as it is.
	if changed {
		s.acted(kind, 1)
		s.logged(ctx, by, logDoc{Kind: kind, Account: rec.Account, Mailbox: rec.ID})
	}
	return rec, nil
}

// What an admin looks at of mailboxes, without a proof and without the log.

func (a *Admin) MailboxRecords(ctx context.Context, accountID string) (*contracts.MailboxRecordList, error) {
	return a.s.MailboxRecords(ctx, accountID)
}

func (a *Admin) MailboxRecordByAddress(ctx context.Context, address string) (*contracts.MailboxRecordList, error) {
	return a.s.MailboxRecordByAddress(ctx, address)
}

// What an admin changes of mailboxes, each with a fresh proof.

func (a *Admin) PauseMailbox(ctx context.Context, id string) (*contracts.MailboxRecord, error) {
	return a.s.pauseMailbox(ctx, a.by, id, true)
}

func (a *Admin) ResumeMailbox(ctx context.Context, id string) (*contracts.MailboxRecord, error) {
	return a.s.pauseMailbox(ctx, a.by, id, false)
}
