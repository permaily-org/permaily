package decisions

import (
	"context"

	"github.com/permaily-org/permaily/internal/inbox"
)

// Gate says whether the model may be asked now for an account
// (connections.ModelGate, ADR 0030's pull request 4), from the guard Ask
// keeps, reading nothing Ask would not: AskOff when the image asks no
// model, no key is set, or the provider refused the key and no probe is
// due; AskLimit when the provider's limit stops every attempt and no probe
// is due, the month's record is forked, a grant of this month did not fit
// in its limit, or the account's share of the day in this enclave is spent;
// AskAnswered otherwise. Anything but AskAnswered means nothing leaves for
// the account's connections (the owner's decision 9). A probe due answers
// AskAnswered, so the next request's ask is the probe that lifts the stop;
// a month's record that could not be read is not a gate (the next ask reads
// it again, and holds what it cannot ask), so a failed read never stops
// connections until a restart.
func (s *Service) Gate(ctx context.Context, account string) inbox.AskOutcome {
	k := s.keyState(ctx)
	if s.model == nil || !k.set {
		return inbox.AskOff
	}
	switch s.held(k.fp) {
	case holdOff:
		return inbox.AskOff
	case holdLimit:
		return inbox.AskLimit
	}
	g := &s.guard
	g.mu.Lock()
	defer g.mu.Unlock()
	now := s.now()
	if g.limitStop == monthOf(now) {
		return inbox.AskLimit
	}
	if account != "" && g.shares[dayOf(now)][account] >= s.cfg.Limits.AccountDay {
		return inbox.AskLimit
	}
	return inbox.AskAnswered
}
