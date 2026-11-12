package accounts

import (
	"context"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// What the mailboxes service asks of accounts, and what accounts asks of it
// (ADR 0026).

// Standing implements mailboxes.Accounts: what an account may do with its
// mailboxes now, read from its signed record. A blocked account, one that is
// gone and one that is due to be removed are contracts.ErrUnauthorized: their
// sessions stand for nothing. One that cannot be read is an error.
func (s *Service) Standing(ctx context.Context, accountID string) (mailboxes.Standing, error) {
	if !validID("acc", accountID) {
		return mailboxes.Standing{}, contracts.ErrUnauthorized
	}
	doc, err := s.read(ctx, accountID)
	if err != nil {
		return mailboxes.Standing{}, err
	}
	if doc == nil || doc.blocked() || doc.due(s.now()) {
		return mailboxes.Standing{}, contracts.ErrUnauthorized
	}
	return mailboxes.Standing{Closing: doc.State == contracts.AccountClosing, TermsAgreed: doc.agreed(s.terms())}, nil
}

type mailboxesHolder struct{ m Mailboxes }

// SetMailboxes gives the service the mailboxes of accounts, once, before it
// serves: the mailboxes service is made with this one as its accounts, so
// Config.Mailboxes cannot hold it when this one is made.
func (s *Service) SetMailboxes(m Mailboxes) {
	s.mailboxes.Store(&mailboxesHolder{m: m})
}

// mailboxesOf is what SetMailboxes gave, else Config.Mailboxes; nil for none.
func (s *Service) mailboxesOf() Mailboxes {
	if h := s.mailboxes.Load(); h != nil {
		return h.m
	}
	return s.cfg.Mailboxes
}
