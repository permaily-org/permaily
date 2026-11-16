package accounts

import (
	"context"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// One service is the accounts of both mailboxes and connections (ADR 0030,
// "Changed after the design review", 16): two methods, two answers.
var (
	_ mailboxes.Accounts   = (*Service)(nil)
	_ connections.Accounts = (*Service)(nil)
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

// ConnectionStanding implements connections.Accounts: what an account's
// state allows its connections now, read from its signed record. Blocked
// comes first (its connections stay and read again once unblocked), then
// gone or due to be removed, then closing, then terms not agreed. One that
// cannot be read is an error.
func (s *Service) ConnectionStanding(ctx context.Context, accountID string) (connections.Standing, error) {
	if !validID("acc", accountID) {
		return connections.StandingGone, nil
	}
	doc, err := s.read(ctx, accountID)
	switch {
	case err != nil:
		return 0, err
	case doc == nil || doc.due(s.now()):
		return connections.StandingGone, nil
	case doc.blocked():
		return connections.StandingBlocked, nil
	case doc.State == contracts.AccountClosing:
		return connections.StandingClosing, nil
	case !doc.agreed(s.terms()):
		return connections.StandingNotAgreed, nil
	}
	return connections.StandingOpen, nil
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
