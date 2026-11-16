package decisions

import (
	"context"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
)

// gateRead bounds the gate's own read of the month's spend record.
const gateRead = 5 * time.Second

// The gate's probe: one statement on a fixed subject, which is nobody's
// mail (decide.Content gets nothing else).
const probeSubject = "Lunch on Friday"

var probeAtom = query.Atom{Var: contracts.VarSubject, Op: contracts.OpAbout, Topic: "a meal"}

// Gate says whether mail may leave now for an account's connections
// (connections.ModelGate, ADR 0030's pull request 4; the owner's decision
// 9). Anything but AskAnswered means nothing leaves, a message whose
// decision is already a committed cleared included: such a message needs no
// ask, so the gate is all that stands between it and a connection, and it
// opens only on what it established itself, never on what the next ask
// would find.
//
// AskOff when the image asks no model, no key is set, or the provider
// refused the key in use; AskLimit while the provider's limit stops
// attempts, the month's record is forked, a grant of this month did not fit
// in its limit, the account's share of the day in this enclave is spent, or
// the month's spend record has not been read whole within spendCheck (lost,
// not opening, failing, or not read yet by this enclave); AskAnswered
// otherwise.
//
// A provider's stop is lifted only by an answer to a probe: when one is due
// the gate sends it (a fixed subject, from the canary's share), at most one
// every probeEvery between this and every other attempt, gates that find
// the same probe due included, and opens only once the provider answered a
// probe of that stop. After the probe it reads the stop as it is then
// (afterProbe), lifting nothing: the key the probe was sent with may not
// be the one the gate read, and a refusal of it stays. The spend record the gate reads again itself,
// within gateRead, at most once every spendCheck, so a record that reads
// again opens the gate without a restart, and one that does not keeps it
// closed.
func (s *Service) Gate(ctx context.Context, account string) inbox.AskOutcome {
	k := s.keyState(ctx)
	if s.model == nil || !k.set {
		return inbox.AskOff
	}
	h, due, gen := s.stopped(k.fp)
	if h != holdNone && due {
		s.probe(ctx, gen)
		h = s.afterProbe()
	}
	switch h {
	case holdOff:
		return inbox.AskOff
	case holdLimit:
		return inbox.AskLimit
	}
	g := &s.guard
	g.mu.Lock()
	defer g.mu.Unlock()
	now := s.now()
	month := monthOf(now)
	if g.limitStop == month {
		return inbox.AskLimit
	}
	if account != "" && g.shares[dayOf(now)][account] >= s.cfg.Limits.AccountDay {
		return inbox.AskLimit
	}
	if !s.spendKnown(ctx, month) {
		return inbox.AskLimit
	}
	return inbox.AskAnswered
}

// stopped is what stops every attempt now, whether or not a probe is due
// (held lets the probe through; the gate does not): the key in use refused,
// the provider's limit, a forked month. due says a probe may be sent, and
// gen is the stop it would be the probe of.
func (s *Service) stopped(fp string) (h hold, due bool, gen uint64) {
	g := &s.guard
	g.mu.Lock()
	defer g.mu.Unlock()
	now := s.now()
	if g.refusedStop && fp != g.refusedFP {
		g.refusedStop, g.refused = false, false // a new key: try it
	}
	due = g.probeDue(now)
	switch {
	case g.refusedStop:
		return holdOff, due, g.gen
	case g.providerStop:
		return holdLimit, due, g.gen
	case g.forkedMonth == monthOf(now):
		return holdLimit, false, g.gen
	}
	return holdNone, false, g.gen
}

// afterProbe is what stops every attempt once the gate's probe ended,
// read as it is and lifting nothing: only the answer to a probe of the
// stop lifts it (answered), so a refusal recorded since the gate read the
// key, of the key the probe was sent with or a later one, keeps the gate
// closed. The next gate reads the key again.
func (s *Service) afterProbe() hold {
	g := &s.guard
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case g.refusedStop:
		return holdOff
	case g.providerStop, g.forkedMonth == monthOf(s.now()):
		return holdLimit
	}
	return holdNone
}

// probe sends the gate's probe of the stop gen, within
// decide.MessageDeadline: only if it claims that stop's probe
// (reserveProbe), and its answer lifts the stop (answered).
func (s *Service) probe(ctx context.Context, gen uint64) {
	ctx, cancel := context.WithTimeout(ctx, decide.MessageDeadline)
	defer cancel()
	calls, _ := decide.Plan([]query.Atom{probeAtom}, probeSubject, "", false)
	for _, c := range calls {
		s.probeCall(ctx, c, gen)
	}
}

// spendKnown says the month's spend record was read whole within spendCheck
// and found neither lost, forked nor unopenable, reading it again (within
// gateRead) when not, at most once every spendCheck since the gate last
// tried. g.mu is held.
func (s *Service) spendKnown(ctx context.Context, month string) bool {
	g := &s.guard
	now := s.now()
	fresh := func(t time.Time) bool { return now.Sub(t) < spendCheck && !now.Before(t) }
	if g.checkedMonth == month && g.unreadableMonth != month && fresh(g.checkedAt) {
		return true
	}
	if !g.gateReadAt.IsZero() && fresh(g.gateReadAt) {
		return false
	}
	g.gateReadAt = now
	ctx, cancel := context.WithTimeout(ctx, gateRead)
	defer cancel()
	return s.checkSpend(ctx, month)
}
