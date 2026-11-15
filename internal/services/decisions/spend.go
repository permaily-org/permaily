package decisions

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// The spend guard (ADR 0028, "Spend, and where each limit lives"). Every
// attempt is reserved at its estimate before it is sent: from the account's
// share, then from what is left of this enclave's grants of the day, and
// when that is not enough from one new grant of the fewest whole blocks that
// cover the estimate alone. A grant is written to the month's strict record
// at the version read and spent from only once a fresh strict read after
// that write lists it; a grant whose write or confirmation failed is burnt.

// probeEvery is how often one attempt is let through to see whether the
// provider takes the key, or has room again, once it said no.
const probeEvery = 10 * time.Minute

// spendCheck is how long this enclave spends from the grants it holds
// before it reads the month's record again, so a fork made after its last
// read stops it within that time.
const spendCheck = time.Minute

// spendDoc is a month's record of spend: every grant every enclave made in
// it. Observations only: an alarm is in RecordAlarms. Every version carries
// every grant before it.
type spendDoc struct {
	V      int        `json:"v"`
	Month  string     `json:"month"`
	Grants []grantDoc `json:"grants"`
}

// grantDoc is one grant: its identity of 128 random bits, its whole blocks
// of Limits.Block and its day (UTC).
type grantDoc struct {
	ID     string `json:"id"`
	Blocks int64  `json:"blocks"`
	Day    string `json:"day"`
}

func (d *spendDoc) total() (blocks int64) {
	for _, g := range d.Grants {
		blocks += g.Blocks
	}
	return blocks
}

func (d *spendDoc) onDay(day string) (blocks int64) {
	for _, g := range d.Grants {
		if g.Day == day {
			blocks += g.Blocks
		}
	}
	return blocks
}

func (d *spendDoc) lists(id string) bool {
	return slices.ContainsFunc(d.Grants, func(g grantDoc) bool { return g.ID == id })
}

// grant is a grant this enclave confirmed, and what it reserved from it.
type grant struct {
	id, month, day string
	size, used     int64
}

// guard is the spend guard's memory: limits and caches only.
type guard struct {
	mu     sync.Mutex
	grants []*grant
	// checked is when the month's record was last read whole, and for which
	// month.
	checkedAt    time.Time
	checkedMonth string
	// forkedMonth is a month whose record is forked: nothing new is asked in
	// it. unreadableMonth is one whose record was last found lost or not
	// opening. limitMonth is one a grant did not fit in.
	forkedMonth, unreadableMonth, limitMonth string
	// provider and refused are the provider's answers: its limit reached,
	// the key refused (refusedFP is that key's fingerprint, so a new key is
	// tried at once). probeAt is when the last probe was let through.
	provider, refused bool
	refusedFP         string
	probeAt           time.Time
	// shares is what each account reserved by day, in this enclave.
	shares map[string]map[string]int64
}

func (g *guard) init() { g.shares = map[string]map[string]int64{} }

// hold is why an attempt was not sent.
type hold int8

const (
	holdNone  hold = iota
	holdLimit      // inbox.AskLimit
	holdOff        // inbox.AskOff
)

// reservation is one attempt's estimate, taken from a grant and an
// account's share, settled once.
type reservation struct {
	s       *Service
	g       *grant
	account string
	day     string
	est     int64
	once    sync.Once
}

// settle settles the reservation exactly once, into the grant it was
// taken from and the account's share of its day: at what the attempt cost.
// A grant whose day or month ended is not used again, so nothing settled
// into it reaches a later grant.
func (r *reservation) settle(actual int64) {
	r.once.Do(func() {
		g := &r.s.guard
		g.mu.Lock()
		defer g.mu.Unlock()
		r.g.used += actual - r.est
		if day := g.shares[r.day]; r.account != "" && day != nil {
			day[r.account] += actual - r.est
		}
	})
}

// blocksFor is the fewest whole blocks that cover an estimate.
func (s *Service) blocksFor(est int64) int64 {
	return max((est+s.cfg.Limits.Block-1)/s.cfg.Limits.Block, 1)
}

// held says whether the guard holds every attempt now, before anything is
// read or reserved: no key, a refused key or the provider's limit with no
// probe due, or a forked month.
func (s *Service) held(fp string) hold {
	g := &s.guard
	g.mu.Lock()
	defer g.mu.Unlock()
	now := s.now()
	if g.refused && fp != g.refusedFP {
		g.refused = false // a new key: try it
	}
	probeDue := now.Sub(g.probeAt) >= probeEvery || now.Before(g.probeAt)
	switch {
	case g.refused && !probeDue:
		return holdOff
	case g.provider && !probeDue:
		return holdLimit
	case g.forkedMonth == monthOf(now):
		return holdLimit
	}
	return holdNone
}

// reserve reserves an attempt's estimate before it is sent, or says why it
// may not be sent. fp is the fingerprint of the key it would be sent with;
// account is whose share it counts in ("" for the canary).
func (s *Service) reserve(ctx context.Context, est int64, account, fp string) (*reservation, hold) {
	g := &s.guard
	g.mu.Lock()
	defer g.mu.Unlock()
	now := s.now()
	day, month := dayOf(now), monthOf(now)
	if g.refused && fp != g.refusedFP {
		g.refused = false
	}
	probe := false
	if g.refused || g.provider {
		if now.Sub(g.probeAt) < probeEvery && !now.Before(g.probeAt) {
			if g.refused {
				return nil, holdOff
			}
			return nil, holdLimit
		}
		probe = true
	}
	if g.forkedMonth == month {
		return nil, holdLimit
	}
	g.prune(day)
	if account != "" && g.shares[day][account]+est > s.cfg.Limits.AccountDay {
		return nil, holdLimit
	}
	var from *grant
	for _, gr := range g.grants {
		if gr.day == day && gr.month == month && gr.size-gr.used >= est {
			from = gr
			break
		}
	}
	if from != nil && (g.checkedMonth != month || now.Sub(g.checkedAt) >= spendCheck || now.Before(g.checkedAt)) {
		if !s.checkSpend(ctx, month) {
			return nil, holdLimit
		}
	}
	if from == nil {
		var h hold
		if from, h = s.makeGrant(ctx, month, day, s.blocksFor(est)); h != holdNone {
			return nil, h
		}
	}
	from.used += est
	if account != "" {
		if g.shares[day] == nil {
			g.shares[day] = map[string]int64{}
		}
		g.shares[day][account] += est
	}
	if probe {
		g.probeAt = now
	}
	return &reservation{s: s, g: from, account: account, day: day, est: est}, holdNone
}

// prune forgets the grants and shares of other days: what is left of a
// grant when its day ends is counted as spent. g.mu is held.
func (g *guard) prune(day string) {
	g.grants = slices.DeleteFunc(g.grants, func(gr *grant) bool { return gr.day != day })
	for d := range g.shares {
		if d != day {
			delete(g.shares, d)
		}
	}
}

// burn counts what is left of this enclave's grants of a month as spent.
// g.mu is held.
func (g *guard) burn(month string) {
	g.grants = slices.DeleteFunc(g.grants, func(gr *grant) bool { return gr.month == month })
}

// unreadable notes a month's record that cannot be trusted, and burns this
// enclave's grants of it. g.mu is held.
func (s *Service) unreadable(err error, month string) {
	g := &s.guard
	g.burn(month)
	g.unreadableMonth = month
	if errors.Is(err, errForked) {
		if g.forkedMonth != month {
			s.logKind(slog.LevelError, "decisions: the month's spend record is forked; nothing new is asked this month", "spend_forked")
		}
		g.forkedMonth = month
		return
	}
	s.logKind(slog.LevelError, "decisions: the month's spend record is lost or does not open; nothing new is asked", "spend_unreadable")
}

// checkSpend reads the month's record again before this enclave spends
// more from the grants it holds. g.mu is held.
func (s *Service) checkSpend(ctx context.Context, month string) bool {
	var doc spendDoc
	_, _, err := readRecord(ctx, s, RecordSpend+month, purposeSpend, &doc)
	switch {
	case errors.Is(err, errUnreadable):
		s.unreadable(err, month)
		return false
	case err != nil:
		s.logKind(slog.LevelWarn, "decisions: the month's spend record could not be read", "spend_read_failed")
		return false
	}
	s.guard.checkedAt, s.guard.checkedMonth, s.guard.unreadableMonth = s.now(), month, ""
	return true
}

// makeGrant makes one grant of blocks for the day, and returns it only once
// a fresh strict read after its write lists it. g.mu is held.
func (s *Service) makeGrant(ctx context.Context, month, day string, blocks int64) (*grant, hold) {
	g := &s.guard
	name := RecordSpend + month
	id := randomID()
	block := s.cfg.Limits.Block
	for range RecordTries {
		var doc spendDoc
		at, found, err := readRecord(ctx, s, name, purposeSpend, &doc)
		switch {
		case errors.Is(err, errUnreadable):
			s.unreadable(err, month)
			return nil, holdLimit
		case err != nil:
			s.logKind(slog.LevelWarn, "decisions: the month's spend record could not be read", "spend_read_failed")
			return nil, holdLimit
		}
		g.unreadableMonth = ""
		if !found {
			doc = spendDoc{V: 1, Month: month}
		}
		if (doc.total()+blocks)*block > s.cfg.Limits.Month {
			// The month's limit: its alarm first, then the guard's state.
			if err := s.addAlarm(ctx, AlarmMonthLimit, day, ""); err != nil {
				s.logKind(slog.LevelWarn, "decisions: an alarm could not be added; it is kept to add again", string(AlarmMonthLimit))
			}
			if g.limitMonth != month {
				s.logKind(slog.LevelWarn, "decisions: the month's limit is reached", "month_limit")
			}
			g.limitMonth = month
			return nil, holdLimit
		}
		if (doc.onDay(day)+blocks)*block > s.cfg.Limits.DayAlarm {
			// The day crosses the alarm with this grant: the alarm is added
			// before the grant is written, or the grant is not made.
			if err := s.addAlarm(ctx, AlarmDaySpend, day, ""); err != nil {
				s.logKind(slog.LevelWarn, "decisions: an alarm could not be added; no grant is made", string(AlarmDaySpend))
				return nil, holdLimit
			}
		}
		doc.Grants = append(doc.Grants, grantDoc{ID: id, Blocks: blocks, Day: day})
		err = writeRecord(ctx, s, name, purposeSpend, &doc, at, found)
		switch {
		case errors.Is(err, store.ErrChanged), errors.Is(err, store.ErrExists):
			continue
		case errors.Is(err, store.ErrForked):
			s.unreadable(errForked, month)
			return nil, holdLimit
		case err != nil:
			// It may have landed: never spent from, counted as burnt.
			s.logKind(slog.LevelWarn, "decisions: a grant's write failed; it is burnt", "grant_write_failed")
			return nil, holdLimit
		}
		var after spendDoc
		_, found, err = readRecord(ctx, s, name, purposeSpend, &after)
		switch {
		case errors.Is(err, errUnreadable):
			s.unreadable(err, month)
			return nil, holdLimit
		case err != nil || !found || !after.lists(id):
			s.logKind(slog.LevelWarn, "decisions: a grant was not confirmed; it is burnt", "grant_unconfirmed")
			return nil, holdLimit
		}
		gr := &grant{id: id, month: month, day: day, size: blocks * block}
		g.grants = append(g.grants, gr)
		g.checkedAt, g.checkedMonth = s.now(), month
		if g.limitMonth == month {
			g.limitMonth = ""
		}
		return gr, holdNone
	}
	s.logKind(slog.LevelWarn, "decisions: the month's spend record kept changing; no grant is made", "spend_busy")
	return nil, holdLimit
}

// answered clears what the provider said once an attempt got an answer.
func (s *Service) answered() {
	g := &s.guard
	g.mu.Lock()
	g.provider, g.refused = false, false
	g.mu.Unlock()
}

// keyRefused notes that the provider refused the key in use: nothing new
// is asked with it, one probe every probeEvery. Its alarm is added; one
// that cannot be added now is added by RunAlarms.
func (s *Service) keyRefused(ctx context.Context, fp string) {
	g := &s.guard
	g.mu.Lock()
	was := g.refused && g.refusedFP == fp
	g.refused, g.refusedFP, g.probeAt = true, fp, s.now()
	g.mu.Unlock()
	if !was {
		s.logKind(slog.LevelError, "decisions: the provider refused the model's key", "key_refused")
	}
	if err := s.addAlarm(ctx, AlarmKeyRefused, dayOf(s.now()), ""); err != nil {
		s.logKind(slog.LevelWarn, "decisions: an alarm could not be added; it is kept to add again", string(AlarmKeyRefused))
	}
}

// providerLimit notes that the provider said its own limit was reached.
func (s *Service) providerLimit(ctx context.Context) {
	g := &s.guard
	g.mu.Lock()
	was := g.provider
	g.provider, g.probeAt = true, s.now()
	g.mu.Unlock()
	if !was {
		s.logKind(slog.LevelError, "decisions: the provider's limit was reached", "provider_limit")
	}
	if err := s.addAlarm(ctx, AlarmProviderLimit, dayOf(s.now()), ""); err != nil {
		s.logKind(slog.LevelWarn, "decisions: an alarm could not be added; it is kept to add again", string(AlarmProviderLimit))
	}
}

// guardState is what /status says of the guard: the first that applies of
// unreadable, month_limit, provider_limit, key_refused (none set, or
// refused) and ok.
func (s *Service) guardState(ctx context.Context) string {
	set := s.keyState(ctx).set
	g := &s.guard
	g.mu.Lock()
	defer g.mu.Unlock()
	month := monthOf(s.now())
	switch {
	case g.forkedMonth == month || g.unreadableMonth == month:
		return attestation.ModelGuardUnreadable
	case g.limitMonth == month:
		return attestation.ModelGuardMonthLimit
	case g.provider:
		return attestation.ModelGuardProviderLimit
	case g.refused || !set || s.model == nil:
		return attestation.ModelGuardKeyRefused
	}
	return attestation.ModelGuardOK
}
