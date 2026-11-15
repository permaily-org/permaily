package decisions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// The canary (ADR 0028, "The canary"): once a day (UTC) the fixed
// synthetic statements of decide.Canary are put to the model with the
// enclave's own client, key, prompt and guard, and compared with what was
// measured. Which enclave runs the day's check is a lease, a hint between
// honest enclaves and not a fence; a check counts only once its completion
// is in the canary's state, a strict anchored record per model, prompt
// version and baseline, which a completion only adds to.

// leasePartition is where the canary's leases are kept.
const leasePartition = "model-canary"

// maxChecks bounds the completed checks the state keeps; what a check
// observed of a change stays in Changed and ChangeSeen.
const maxChecks = 64

// staleDays is how many days (UTC) without a completed check make the
// canary stale and its failure alarm due.
const staleDays = 2

// canaryFresh is how long /status uses the state it read.
const canaryFresh = time.Minute

// errCanaryFailed is a run that did not complete.
var errCanaryFailed = errors.New("decisions: the canary's check did not complete")

// canaryDoc is the canary's state for one model, prompt version and
// baseline: observations only, never an alarm.
type canaryDoc struct {
	V        int    `json:"v"`
	Model    string `json:"model"`
	Prompt   string `json:"prompt"`
	Baseline string `json:"baseline"`
	// Since is when the first enclave of this baseline made the state.
	Since  time.Time     `json:"since"`
	Checks []canaryCheck `json:"checks"`
	// Changed is set once any completed check observed a change, and stays
	// until a new baseline; ChangeSeen is the latest day one did, and never
	// goes back.
	Changed    bool   `json:"changed,omitempty"`
	ChangeSeen string `json:"change_seen,omitempty"`
}

// canaryCheck is one completed check.
type canaryCheck struct {
	At      time.Time `json:"at"`
	Day     string    `json:"day"`
	Changed bool      `json:"changed,omitempty"`
}

// last is the last completed check, nil before one.
func (d *canaryDoc) last() *canaryCheck {
	var out *canaryCheck
	for i := range d.Checks {
		if out == nil || d.Checks[i].At.After(out.At) {
			out = &d.Checks[i]
		}
	}
	return out
}

// checkedOn reports whether a check completed on day.
func (d *canaryDoc) checkedOn(day string) bool {
	for _, c := range d.Checks {
		if c.Day == day {
			return true
		}
	}
	return false
}

// stale reports whether no check completed for staleDays days (UTC),
// counted from the last one, or before any from Since.
func (d *canaryDoc) stale(now time.Time) bool {
	from := d.Since
	if l := d.last(); l != nil {
		from = l.At
	}
	fromDay, _ := time.Parse(time.DateOnly, dayOf(from))
	today, _ := time.Parse(time.DateOnly, dayOf(now))
	return today.Sub(fromDay) >= staleDays*24*time.Hour
}

// canaryRun is this enclave's memory of the canary.
type canaryRun struct {
	running bool
	// doc is the state as last read, at readAt; unreadable says the last
	// read could not trust it.
	doc              *canaryDoc
	readAt           time.Time
	canaryUnreadable bool
	// failedDay is the day this enclave's latest run did not complete, ""
	// when it did.
	failedDay string
	// staleSeen says canary_failed is in the record for the stale state
	// this enclave read: /status says stale only then.
	staleSeen bool
	// lease is the lease entry this enclave holds, and until when.
	lease      *store.Entry
	leaseUntil time.Time
	// retryAt is when the lease of this enclave's latest run that did not
	// complete ends: no run starts here before it, so a run that keeps
	// failing costs one run a lease, not one a tick (CanaryLease).
	retryAt time.Time
}

// canaryName is the state's record name for the compiled model, prompt
// version and baseline.
func (s *Service) canaryName() string {
	sum := sha256.Sum256([]byte(s.model.Name() + "\n" + decide.PromptVersion + "\n" + s.canary.Baseline))
	return RecordCanary + hex.EncodeToString(sum[:])[:16]
}

// canaryDays is what a kept answer says of the canary when it was asked:
// the day of the last completed check and the last day a check observed a
// change, as this enclave last read them.
func (s *Service) canaryDays() (checked, change string) {
	s.canaryMu.Lock()
	defer s.canaryMu.Unlock()
	if s.doc == nil {
		return "", ""
	}
	if l := s.doc.last(); l != nil {
		checked = l.Day
	}
	return checked, s.doc.ChangeSeen
}

// readCanary reads the state, and makes it when make is set and no
// enclave of this baseline has.
func (s *Service) readCanary(ctx context.Context, makeIt bool) (*canaryDoc, error) {
	name := s.canaryName()
	now := s.now()
	doc, err := changeRecord(ctx, s, name, purposeCanary, func(d *canaryDoc, found bool) bool {
		if found || !makeIt {
			return false
		}
		*d = canaryDoc{V: 1, Model: s.model.Name(), Prompt: decide.PromptVersion, Baseline: s.canary.Baseline, Since: now}
		return true
	})
	s.canaryMu.Lock()
	defer s.canaryMu.Unlock()
	if err != nil {
		// Whatever failed (the record lost, forked or not opening, the
		// storage, the key), /status cannot say the state is what this
		// enclave read before: it is unreadable until a read succeeds.
		s.unreadableCanary(err)
		return nil, err
	}
	s.canaryUnreadable = false
	if doc.V == 0 {
		return nil, nil // not made yet
	}
	s.doc, s.readAt = doc, now
	// An alarm accepted for an earlier stale episode is not this one's: a
	// read that finds the state no longer stale ends that episode, and a
	// later one is shown only once its own alarm is in the record.
	if !doc.stale(now) {
		s.staleSeen = false
	}
	return doc, nil
}

// unreadableCanary notes a state that could not be read, logged by its
// kind once. s.canaryMu is held.
func (s *Service) unreadableCanary(err error) {
	if !s.canaryUnreadable {
		if errors.Is(err, errUnreadable) {
			s.logKind(slog.LevelError, "decisions: the canary's state cannot be read", "canary_unreadable")
		} else {
			s.logKind(slog.LevelWarn, "decisions: the canary's state could not be read", "canary_read_failed")
		}
	}
	s.canaryUnreadable, s.readAt = true, s.now()
}

// RunCanary runs the day's canary if no check completed today; the
// composition calls it when it starts and on a ticker. It makes the
// canary's state when no enclave of this baseline has, makes canary_failed
// due when no check completed for two days, and runs at most one check at a
// time in this enclave.
func (s *Service) RunCanary(ctx context.Context) error {
	if s.canary == nil || s.model == nil {
		return nil
	}
	s.canaryMu.Lock()
	if s.running {
		s.canaryMu.Unlock()
		return nil
	}
	s.running = true
	s.canaryMu.Unlock()
	defer func() {
		s.canaryMu.Lock()
		s.running = false
		s.canaryMu.Unlock()
	}()
	now := s.now()
	today := dayOf(now)
	doc, err := s.readCanary(ctx, true)
	if err != nil {
		return err
	}
	if doc.stale(now) {
		// The alarm first, then the state as /status says it.
		if err := s.addAlarm(ctx, AlarmCanaryFailed, today, s.canary.Baseline); err != nil {
			s.logKind(slog.LevelWarn, "decisions: an alarm could not be added; it is kept to add again", string(AlarmCanaryFailed))
		} else {
			s.publishStale(alarmEntry{Kind: AlarmCanaryFailed, Baseline: s.canary.Baseline})
		}
	} else {
		s.canaryMu.Lock()
		s.staleSeen = false
		s.canaryMu.Unlock()
	}
	if doc.checkedOn(today) {
		return nil
	}
	s.canaryMu.Lock()
	wait := now.Before(s.retryAt)
	s.canaryMu.Unlock()
	if wait {
		return nil
	}
	if ok, err := s.takeLease(ctx); !ok {
		return err
	}
	// Another enclave may have completed the day's check before this one
	// took the lease over.
	if doc, err = s.readCanary(ctx, false); err != nil || doc == nil || doc.checkedOn(today) {
		return err
	}
	if err := s.check(ctx, today); err != nil {
		s.canaryMu.Lock()
		s.failedDay, s.retryAt = today, s.leaseUntil
		s.canaryMu.Unlock()
		s.logKind(slog.LevelWarn, "decisions: the canary's check did not complete", "canary_failed")
		return err
	}
	s.canaryMu.Lock()
	s.failedDay = ""
	s.canaryMu.Unlock()
	return nil
}

// check runs one check and writes its completion.
func (s *Service) check(ctx context.Context, today string) error {
	changed := false
	for _, m := range s.canary.Messages {
		expected := map[query.Atom]decide.CanaryAtom{}
		atoms := make([]query.Atom, 0, len(m.Atoms))
		for _, ca := range m.Atoms {
			expected[ca.Atom()] = ca
			atoms = append(atoms, ca.Atom())
		}
		calls, _ := decide.Plan(atoms, m.Subject, m.Body, true)
		for _, c := range calls {
			s.renewLease(ctx)
			r := s.call(ctx, c, "", true)
			switch {
			case r.changed:
				changed = true
				continue
			case r.res == nil:
				return errCanaryFailed
			}
			for i, ans := range r.res.Answers {
				switch {
				case ans.Refused:
					changed = true // a refusal where an answer was measured
				case !ans.OK:
					return errCanaryFailed // an answer that does not read is a failed run
				case expected[c.Statements[i]].Moved(ans.P, s.canary.Tolerance):
					changed = true
				}
			}
		}
	}
	if changed {
		// The alarm first, then the completion that observed it.
		if err := s.addAlarm(ctx, AlarmCanaryChanged, today, s.canary.Baseline); err != nil {
			return errCanaryFailed
		}
		s.logKind(slog.LevelWarn, "decisions: the canary saw an answer of the model move", "canary_changed")
	}
	now := s.now()
	doc, err := changeRecord(ctx, s, s.canaryName(), purposeCanary, func(d *canaryDoc, found bool) bool {
		if !found {
			return false
		}
		d.Checks = append(d.Checks, canaryCheck{At: now, Day: today, Changed: changed})
		if len(d.Checks) > maxChecks {
			d.Checks = d.Checks[len(d.Checks)-maxChecks:]
		}
		if changed {
			d.Changed = true
			if today > d.ChangeSeen {
				d.ChangeSeen = today
			}
		}
		return true
	})
	if err != nil || doc == nil || doc.V == 0 {
		// The completion's own read or write failing leaves /status
		// unreadable until a read succeeds, as any other failed read.
		if err != nil {
			s.canaryMu.Lock()
			s.unreadableCanary(err)
			s.canaryMu.Unlock()
		}
		return errCanaryFailed
	}
	s.canaryMu.Lock()
	s.doc, s.readAt, s.canaryUnreadable, s.staleSeen = doc, now, false, false
	s.canaryMu.Unlock()
	return nil
}

// publishStale says on /status that the canary is stale once its
// canary_failed alarm, of this baseline, is in the record. s.canaryMu is
// not held.
func (s *Service) publishStale(e alarmEntry) {
	if e.Kind != AlarmCanaryFailed || s.canary == nil || e.Baseline != s.canary.Baseline {
		return
	}
	s.canaryMu.Lock()
	s.staleSeen = true
	s.canaryMu.Unlock()
}

// leaseDoc is a lease, sealed.
type leaseDoc struct {
	Holder string    `json:"holder"`
	Until  time.Time `json:"until"`
}

func leaseAAD(key string) []byte { return envelope.AAD("entry", leasePartition, key) }

// sealLease writes the value of a lease held by this enclave from now.
func (s *Service) sealLease(ctx context.Context, key string, until time.Time) ([]byte, error) {
	k, err := s.sealingKey(ctx, purposeCanary)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(leaseDoc{Holder: s.id, Until: until})
	return envelope.SealText(k, raw, leaseAAD(key))
}

// takeLease takes the canary's lease: made at version 0 (store.ErrExists
// when another enclave made it), or taken over at the version read once it
// expired (store.ErrConflict when another took it first). A lease that does
// not open is taken over as expired.
func (s *Service) takeLease(ctx context.Context) (bool, error) {
	key := "lease#" + s.canaryName()
	now := s.now()
	until := now.Add(CanaryLease)
	value, err := s.sealLease(ctx, key, until)
	if err != nil {
		return false, err
	}
	e, err := s.cfg.Entries.GetEntry(ctx, leasePartition, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		e = &store.Entry{Partition: leasePartition, Key: key}
	case err != nil:
		return false, errors.New("decisions: the canary's lease could not be read")
	default:
		var l leaseDoc
		if k, kerr := s.sealingKey(ctx, purposeCanary); kerr == nil {
			if plain, oerr := envelope.OpenText(k, e.Value, leaseAAD(key)); oerr == nil && json.Unmarshal(plain, &l) == nil &&
				l.Holder != s.id && l.Until.After(now) {
				return false, nil
			}
		}
	}
	e.Value, e.UpdatedAt = value, now
	switch err := s.cfg.Entries.PutEntry(ctx, e); {
	case errors.Is(err, store.ErrExists), errors.Is(err, store.ErrConflict):
		return false, nil
	case err != nil:
		return false, errors.New("decisions: the canary's lease could not be written")
	}
	s.canaryMu.Lock()
	s.lease, s.leaseUntil = e, until
	s.canaryMu.Unlock()
	return true, nil
}

// renewLease renews the lease this enclave holds once half of it passed.
// One lost to another enclave is not taken back: the run goes on and adds
// its completion.
func (s *Service) renewLease(ctx context.Context) {
	s.canaryMu.Lock()
	e, until := s.lease, s.leaseUntil
	s.canaryMu.Unlock()
	now := s.now()
	if e == nil || until.Sub(now) > CanaryLease/2 {
		return
	}
	next := now.Add(CanaryLease)
	value, err := s.sealLease(ctx, e.Key, next)
	if err != nil {
		return
	}
	renewed := *e
	renewed.Value, renewed.UpdatedAt = value, now
	s.canaryMu.Lock()
	defer s.canaryMu.Unlock()
	if err := s.cfg.Entries.PutEntry(ctx, &renewed); err != nil {
		s.lease = nil
		return
	}
	s.lease, s.leaseUntil = &renewed, next
}

// canaryStatus is the canary's part of /status: the first of unreadable,
// stale, changed, failed, same and none that applies.
func (s *Service) canaryStatus(ctx context.Context) *attestation.CanaryStatus {
	if s.canary == nil || s.model == nil {
		return nil
	}
	s.canaryMu.Lock()
	stale := s.doc == nil || s.now().Sub(s.readAt) >= canaryFresh
	s.canaryMu.Unlock()
	if stale {
		_, _ = s.readCanary(ctx, false)
	}
	s.canaryMu.Lock()
	defer s.canaryMu.Unlock()
	st := &attestation.CanaryStatus{Baseline: s.canary.Baseline}
	now := s.now()
	doc := s.doc
	if doc != nil {
		if l := doc.last(); l != nil {
			st.CheckedAt = l.At
		}
		if doc.ChangeSeen != "" {
			st.ChangeSeenAt, _ = time.Parse(time.DateOnly, doc.ChangeSeen)
		}
	}
	switch {
	case s.canaryUnreadable:
		st.State = attestation.CanaryUnreadable
	case doc == nil:
		st.State = attestation.CanaryNone
	case doc.stale(now) && s.staleSeen:
		st.State = attestation.CanaryStale
	case doc.Changed:
		st.State = attestation.CanaryChanged
	case s.failedDay == dayOf(now) && !doc.checkedOn(dayOf(now)):
		st.State = attestation.CanaryFailed
	case doc.last() != nil:
		st.State = attestation.CanarySame
	default:
		st.State = attestation.CanaryNone
	}
	return st
}
