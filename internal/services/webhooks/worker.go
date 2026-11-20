package webhooks

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"errors"
	"slices"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webhook"
)

// scanCursor is where the workers' listing of the due minutes is: a sweep
// goes from its first minute to now and starts again from the earliest
// minute it found an entry in (scanBack before now at the latest), each
// pass going on where the last stopped,
// so every due entry is reached within a bounded number of passes however
// large the backlog in front of it (ADR 0039, "Fair shares").
type scanCursor struct {
	started  bool
	from     time.Time
	minute   time.Time
	after    string
	earliest time.Time
}

// The bounds of one pass's listing: the entries it reads and the minutes it
// lists, and how far back the first sweep after a start begins (the repair
// covers older ones).
const (
	scanEntriesMax = 2 * webhook.ReadyMax
	scanListsMax   = 128
	scanBack       = 10 * time.Minute
)

// pass lists the due minutes and starts what may start.
func (s *Service) pass(ctx context.Context) {
	s.scanDue(ctx)
	s.dispatch(ctx)
}

// scanDue reads the due entries of the minute now from its start (so what
// becomes due is seen at every pass however long the sweep behind it), then
// goes on with the sweep from where the last pass stopped, at most
// scanEntriesMax in all, and holds each ready (the scheduler's admission):
// an entry that does not fit stays where it is; one that does not open
// (moved, or not written by this service) is deleted.
func (s *Service) scanDue(ctx context.Context) {
	seal, _, err := s.keys(ctx)
	if err != nil {
		s.fail("scan", err)
		return
	}
	now := s.now().Truncate(time.Minute)
	head, err := s.cfg.Entries.ListEntries(ctx, minuteKey(now), "", "", scanEntriesMax/2)
	if err != nil {
		s.fail("scan", err)
		return
	}
	for _, e := range head {
		s.ready(ctx, seal, e)
	}
	s.mu.Lock()
	c := s.scan
	s.mu.Unlock()
	if !c.started {
		c = scanCursor{started: true, from: now.Add(-scanBack), minute: now.Add(-scanBack)}
	}
	wrapped := false
	for lists, read := 0, len(head); lists < scanListsMax && read < scanEntriesMax; lists++ {
		if c.minute.After(now) {
			if wrapped {
				break
			}
			wrapped = true
			// Never later than scanBack: an entry written into a minute
			// a sweep had passed (late in that minute, or by an enclave
			// whose clock is behind) is reached by the next one.
			c.from = sooner(cmp.Or(c.earliest, now), now.Add(-scanBack))
			c.minute, c.after, c.earliest = c.from, "", time.Time{}
		}
		limit := scanEntriesMax - read
		entries, err := s.cfg.Entries.ListEntries(ctx, minuteKey(c.minute), "", c.after, limit)
		if err != nil {
			s.fail("scan", err)
			break
		}
		read += len(entries)
		for _, e := range entries {
			c.after = e.Key
			c.earliest = cmp.Or(c.earliest, c.minute)
			s.ready(ctx, seal, e)
		}
		if len(entries) < limit {
			c.minute, c.after = c.minute.Add(time.Minute), ""
		}
	}
	s.mu.Lock()
	s.scan = c
	s.mu.Unlock()
}

// ready holds a due entry's connection ready, if it opens and fits.
func (s *Service) ready(ctx context.Context, seal []byte, e store.Entry) {
	var r route
	if openFrom(seal, &e, &r) != nil {
		s.unlist(ctx, item{due: e})
		return
	}
	s.mu.Lock()
	s.sched.admit(item{connection: e.Key, host: r.Host, account: r.Account, due: e})
	s.mu.Unlock()
}

// dispatch starts every item the scheduler lets start, each in its own
// goroutine, and says how many; one that ends asks for more.
func (s *Service) dispatch(ctx context.Context) int {
	for started := 0; ; started++ {
		s.mu.Lock()
		it, ok := s.sched.pick(s.now())
		s.mu.Unlock()
		if !ok {
			return started
		}
		s.work.Go(func() {
			called := s.touch(ctx, it)
			s.mu.Lock()
			s.sched.done(it, called)
			s.mu.Unlock()
			select {
			case s.wake <- struct{}{}:
			default:
			}
		})
	}
}

// evaluations are the evaluations of new mail under way and started within
// the last minute (EvaluationsInFlightMax, AccountEvaluationsInFlightMax,
// EvaluationsPerMinute).
type evaluations struct {
	inFlight int
	accounts map[string]int
	starts   []time.Time
}

func (s *Service) evaluationStarts(account string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := &s.evals
	v.starts = within(v.starts, now)
	if v.inFlight >= webhook.EvaluationsInFlightMax || v.accounts[account] >= webhook.AccountEvaluationsInFlightMax || len(v.starts) >= webhook.EvaluationsPerMinute {
		return false
	}
	if v.accounts == nil {
		v.accounts = map[string]int{}
	}
	v.inFlight++
	v.accounts[account]++
	v.starts = append(v.starts, now)
	return true
}

func (s *Service) evaluationEnds(account string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evals.inFlight--
	if s.evals.accounts[account]--; s.evals.accounts[account] == 0 {
		delete(s.evals.accounts, account)
	}
}

// touch works on one connection's entry under its lease, and says whether
// a call was made (its reservation is then spent).
func (s *Service) touch(ctx context.Context, it item) bool {
	// Its item was found by a scan, which derived the keys.
	s.keyMu.Lock()
	seal, sign := s.seal, s.sign
	s.keyMu.Unlock()
	e, err := s.cfg.Entries.GetEntry(ctx, shard(it.connection), it.connection)
	if errors.Is(err, store.ErrNotFound) {
		// Its state is gone: the repair makes it again from the
		// connection's record, or drops what is left of it.
		s.unlist(ctx, it)
		return false
	}
	if err != nil {
		s.fail("state", err)
		return false
	}
	st := &state{}
	if err := openFrom(seal, e, st); err != nil {
		// Not one this service wrote: nothing is called for it, and the
		// repair makes it again from the connection's record.
		s.fail("state", err)
		s.unlist(ctx, it)
		return false
	}
	now := s.now()
	if st.Lease.Holder != s.name && now.Before(st.Lease.Until) {
		return false
	}
	st.Lease = lease{Holder: s.name, Until: now.Add(webhook.LeaseFor)}
	if err := s.put(ctx, seal, e, st); err != nil {
		s.fail("lease", err)
		return false
	}
	t := &turn{s: s, seal: seal, sign: sign, it: it, e: e, st: st, now: now}
	called := t.work(ctx)
	t.finish(ctx)
	return called
}

// turn is one touch of a connection's entry, its lease held.
type turn struct {
	s    *Service
	seal []byte
	sign ed25519.PrivateKey
	it   item
	e    *store.Entry
	st   *state
	now  time.Time
	// drop: the connection no longer calls, and its entries go; abandon:
	// its state could not be written before a call, and is left to its
	// lease; soon: it is touched again by then.
	drop    bool
	abandon bool
	soon    time.Time
}

func (t *turn) retry()  { t.again(t.now.Add(time.Minute)) }
func (t *turn) dueNow() { t.again(t.now) }
func (t *turn) again(at time.Time) {
	if t.soon.IsZero() || at.Before(t.soon) {
		t.soon = at
	}
}

// work is what is due: the connection read, its new mail evaluated, its
// schedule's runs, and an attempt of its notice.
func (t *turn) work(ctx context.Context) bool {
	sub, err := t.s.cfg.Connections.Subscription(ctx, t.st.Connection)
	if t.gone(err) {
		return false
	}
	if err != nil {
		t.s.fail("subscription", err)
		t.retry()
		return false
	}
	if t.stopped(sub) {
		return false
	}
	t.mailboxes(ctx, sub)
	if sub.NewMail {
		t.evaluate(ctx)
	}
	if sub.Schedule != nil {
		t.schedule(sub)
	}
	return !t.drop && t.attempt(ctx)
}

// gone: the connection has no webhook any more (removed, or never had
// one): every entry of it goes.
func (t *turn) gone(err error) bool {
	if errors.Is(err, ErrNoWebhook) {
		t.dropAll(SkipEnded)
		return true
	}
	return false
}

// stopped: the connection no longer calls for good (revoked, ended, its
// account closing, every device ended, turned off or stopped): every entry
// of it goes. The model or the rules not usable now is not that.
func (t *turn) stopped(sub *Subscription) bool {
	switch sub.Why {
	case SkipEnded, SkipNoDevice, SkipOff:
		t.dropAll(sub.Why)
		return true
	}
	return false
}

// dropAll drops the connection's entries, counting every notice that
// waited.
func (t *turn) dropAll(why Skip) {
	for _, pending := range []bool{t.st.InFlight != nil, t.st.Waiting.Match != nil, !t.st.Waiting.Run.IsZero()} {
		if pending {
			t.s.skip(why)
		}
	}
	t.drop = true
}

// mailboxes keeps the index entries of new mail to the mailboxes the
// connection reaches now.
func (t *turn) mailboxes(ctx context.Context, sub *Subscription) {
	var want []string
	if sub.NewMail {
		want = sub.Mailboxes
	}
	if slices.Equal(t.st.Mailboxes, want) {
		return
	}
	r := route{Host: t.st.Host, Account: t.st.Account}
	for _, m := range want {
		if err := t.s.index(ctx, t.seal, m, t.st.Connection, r); err != nil {
			t.s.fail("index", err)
			return
		}
	}
	for _, m := range t.st.Mailboxes {
		if !slices.Contains(want, m) {
			t.s.unindex(ctx, m, t.st.Connection)
		}
	}
	t.st.Mailboxes = slices.Clone(want)
}

// evaluate evaluates the connection's new mail after its progress, within
// the evaluations' bounds (else it is due again at once), and adds what
// would leave to the generation waiting. While the model or the rules
// cannot be used nothing is evaluated and the progress stays.
func (t *turn) evaluate(ctx context.Context) {
	s := t.s
	if !s.evaluationStarts(t.st.Account, t.now) {
		t.dueNow()
		return
	}
	defer s.evaluationEnds(t.st.Account)
	e, err := s.cfg.Connections.Pending(ctx, t.st.Connection, t.st.Progress)
	if t.gone(err) {
		return
	}
	if err != nil {
		if !errors.Is(err, contracts.ErrModelUnavailable) && !errors.Is(err, ErrRulesUnavailable) {
			s.fail("evaluate", err)
		}
		t.retry()
		return
	}
	t.st.Progress = e.Through
	if e.More {
		t.dueNow()
	}
	if e.Match != nil && len(e.Match.Messages) > 0 {
		t.merge(*e.Match)
	}
}

// earlier is the progress from which more is evaluated.
func earlier(a, b Progress) Progress {
	if b.Cursor.Compare(a.Cursor) < 0 {
		return b
	}
	return a
}

// merge adds a match to the generation waiting: the earliest From, the
// earliest messages, at most webhook.MatchMessagesMax. A match found under
// another basis than the one waiting is not merged: both are evaluated
// again, from the earlier, under the current basis.
func (t *turn) merge(m Match) {
	w := &t.st.Waiting
	switch {
	case w.Match == nil:
		w.Match = &m
	case w.Match.Basis != m.Basis:
		t.s.skip(SkipChanged)
		t.st.Progress = earlier(t.st.Progress, w.Match.From)
		w.Match = nil
		t.dueNow()
	default:
		t.s.skip(SkipCoalesced)
		msgs := append(slices.Clone(w.Match.Messages), m.Messages...)
		slices.SortFunc(msgs, Cursor.Compare)
		msgs = slices.Compact(msgs)
		w.Match.Messages = msgs[:min(len(msgs), webhook.MatchMessagesMax)]
	}
}

// schedule adds the latest run due since the last one to the generation
// waiting (late when it stands for more than one), and keeps the next.
func (t *turn) schedule(sub *Subscription) {
	loc, err := webhook.Zone(sub.TimeZone)
	if err != nil {
		t.s.fail("schedule", err)
		return
	}
	if at, late, ok := webhook.Due(*sub.Schedule, loc, t.st.LastRun, t.now); ok {
		w := &t.st.Waiting
		if !w.Run.IsZero() {
			t.s.skip(SkipCoalesced)
			late = true
		}
		w.Run, w.Late, t.st.LastRun = at, late, at
	}
	t.st.NextRun = webhook.Next(*sub.Schedule, loc, t.now)
}

// candidate is the notice an attempt is for: the one in flight, or the
// next of the generation waiting (its scheduled run first) once
// webhook.MinInterval has passed since the last one taken.
func (t *turn) candidate() *notice {
	st := t.st
	if n := st.InFlight; n != nil && !t.now.Before(n.CreatedAt.Add(webhook.GiveUpAfter)) {
		t.s.skip(SkipGivenUp)
		st.InFlight = nil
	}
	switch {
	case st.InFlight != nil:
		if t.now.Before(st.InFlight.NextAt) {
			return nil
		}
		return st.InFlight
	case st.Waiting.empty() || t.now.Before(st.LastTaken.Add(webhook.MinInterval)):
		return nil
	case !st.Waiting.Run.IsZero():
		return &notice{Type: contracts.NoticeScheduleDue, ScheduledAt: st.Waiting.Run, late: st.Waiting.Late}
	}
	return &notice{Type: contracts.NoticeMailMatched, Match: st.Waiting.Match}
}

// attempt makes one attempt of the candidate, if there is one.
//
// When an attempt begins (re-review N2): at the read Connections.Admit
// makes, and only when the basis that read found is the one its match was
// evaluated and rechecked under. Recheck says the match still stands under
// the basis it names; Admit reads again, registers the permit and answers
// the basis of that read; a rule saved or a connection changed between the
// two (or while this worker was delayed) makes them differ, and the match
// is discarded and evaluated again under the current basis, never sent as
// if admitted under it. From that read on, the attempt is an answer being
// prepared (ADR 0040): a rule saved after it applies to the next attempt,
// and this one is written by the permit's WriteBy or not at all. A notice
// is frozen (its identifier and body) when its first attempt begins, and
// written down before its call starts.
func (t *turn) attempt(ctx context.Context) bool {
	n := t.candidate()
	if n == nil {
		return false
	}
	c := t.s.cfg.Connections
	if n.Match != nil {
		ok, err := c.Recheck(ctx, t.st.Connection, *n.Match)
		if t.gone(err) {
			return false
		}
		if err != nil || !ok {
			why := SkipChanged
			if errors.Is(err, ErrRulesUnavailable) {
				why = SkipRules
			}
			t.forget(n, why)
			return false
		}
	}
	sub, permit, err := c.Admit(ctx, t.st.Connection)
	if t.gone(err) {
		return false
	}
	if err != nil {
		t.s.fail("admit", err)
		t.retry()
		return false
	}
	if !sub.Calls {
		if !t.stopped(sub) {
			t.forget(n, sub.Why)
			t.retry()
		}
		return false
	}
	defer permit.Finish()
	if (n.Match != nil && sub.Basis != n.Match.Basis) || (n.ID != "" && (n.Audience != sub.URL || n.Subscription != sub.ID)) {
		t.forget(n, SkipChanged)
		return false
	}
	if n.ID == "" && !t.freeze(n, sub) {
		return false
	}
	ts, sig, err := webhook.Sign([]ed25519.PrivateKey{t.sign}, n.Subscription, n.ID, t.s.now(), n.Body)
	if err != nil {
		t.s.fail("sign", err)
		t.forget(n, SkipChanged)
		return false
	}
	n.Calls++
	// Should this enclave stop here, the other calls again with the same
	// notice once the lease ends.
	n.NextAt = t.now
	if err := t.s.put(ctx, t.seal, t.e, t.st); err != nil {
		t.s.fail("attempt", err)
		t.abandon = true
		return false
	}
	pctx, err := permit.Start(ctx)
	if err != nil {
		t.outcome(ctx, n, webhook.OutcomeCut)
		return false
	}
	t.outcome(ctx, n, t.s.cfg.Sender.Send(pctx, webhook.Call{URL: sub.URL, ID: n.ID, Timestamp: ts, Signature: sig, Body: n.Body, WriteBy: permit.WriteBy()}))
	return true
}

// freeze makes the candidate the notice in flight: its identifier and its
// body, which every attempt carries from now on.
func (t *turn) freeze(n *notice, sub *Subscription) bool {
	id := make([]byte, 16)
	if err := t.s.cfg.Random(id); err != nil {
		t.s.fail("random", err)
		return false
	}
	n.ID = webhook.NoticePrefix + idEncoding.EncodeToString(id)
	n.Subscription, n.Audience, n.CreatedAt = sub.ID, sub.URL, t.now
	body := contracts.WebhookNotice{
		V: contracts.WebhookNoticeVersion, Type: n.Type, ID: n.ID, Subscription: sub.ID,
		ClientID: sub.ClientID, Audience: sub.URL, CreatedAt: t.now,
	}
	w := &t.st.Waiting
	if n.Match != nil {
		body.Since = n.Match.Messages[0].Received.UTC().Truncate(time.Minute)
		w.Match = nil
	} else {
		body.Since, body.ScheduledAt = t.st.Coverage.UTC(), n.ScheduledAt.UTC()
		body.Late = n.late || t.now.Sub(n.ScheduledAt) > webhook.LateAfter
		w.Run, w.Late = time.Time{}, false
	}
	b, err := webhook.Body(body)
	if err != nil {
		t.s.fail("notice", err)
		t.forget(n, SkipChanged)
		return false
	}
	n.Body = b
	t.st.InFlight = n
	return true
}

// forget drops the candidate, counted by why. A match dropped is evaluated
// again from where it began, under the basis current then, and so is the
// generation waiting with it; a scheduled run dropped leaves the coverage
// boundary where it was, so the next one covers it.
func (t *turn) forget(n *notice, why Skip) {
	t.s.skip(why)
	w := &t.st.Waiting
	switch {
	case n == t.st.InFlight:
		t.st.InFlight = nil
	case n.Type == contracts.NoticeScheduleDue:
		w.Run, w.Late = time.Time{}, false
	default:
		w.Match = nil
	}
	if n.Match != nil {
		from := n.Match.From
		if w.Match != nil {
			from = earlier(from, w.Match.From)
			w.Match = nil
		}
		t.st.Progress = earlier(t.st.Progress, from)
		t.dueNow()
	}
}

// outcome is what a call's outcome changes: taken, the vendor asked to
// stop, cut (called again at once, and the next read says why), or a
// failure retried after webhook.Wait, never past webhook.GiveUpAfter.
func (t *turn) outcome(ctx context.Context, n *notice, o webhook.Outcome) {
	s := t.s
	s.count(o)
	s.log.Info("webhook call", "outcome", string(o), "vendor", s.vendor(t.st.Host))
	switch {
	case o == webhook.OutcomeSent:
		t.st.InFlight, t.st.LastTaken = nil, s.now()
		if n.Type == contracts.NoticeScheduleDue {
			t.st.Coverage = n.ScheduledAt
		}
		t.st.FailingSince, t.st.Failure = time.Time{}, ""
	case o == webhook.OutcomeGone && s.stop(ctx, t.st.Connection):
		t.dropAll(SkipOff)
	case o == webhook.OutcomeCut:
	default:
		if t.st.FailingSince.IsZero() {
			t.st.FailingSince = t.now
		}
		if f := o.Failure(); f != "" {
			t.st.Failure = f
		}
		n.NextAt = sooner(t.now.Add(webhook.Wait(n.Calls)), n.CreatedAt.Add(webhook.GiveUpAfter))
	}
}

// stop records the vendor's 410; when it cannot be recorded, the notice is
// retried, and its 410 recorded then.
func (s *Service) stop(ctx context.Context, connection string) bool {
	if err := s.cfg.Connections.Stop(ctx, connection); err != nil {
		s.fail("stop", err)
		return false
	}
	return true
}

// next is when the connection is due again: zero when nothing will be
// until a hint or the repair.
func (t *turn) next() time.Time {
	var at []time.Time
	if !t.soon.IsZero() {
		at = append(at, t.soon)
	}
	switch st := t.st; {
	case st.InFlight != nil:
		at = append(at, st.InFlight.NextAt)
	case !st.Waiting.empty():
		at = append(at, st.LastTaken.Add(webhook.MinInterval))
	}
	if !t.st.NextRun.IsZero() {
		at = append(at, t.st.NextRun)
	}
	if len(at) == 0 {
		return time.Time{}
	}
	return slices.MinFunc(at, time.Time.Compare)
}

// finish writes what the turn changed and the next due minute, gives the
// lease back and deletes the due entry it was found by (unless a hint wrote
// it again meanwhile, or it is the next one).
func (t *turn) finish(ctx context.Context) {
	s := t.s
	if t.abandon {
		return
	}
	if t.drop {
		s.remove(ctx, t.e, t.st)
		s.unlist(ctx, t.it)
		return
	}
	keep := false
	if next := t.next(); next.IsZero() {
		t.st.Due = time.Time{}
	} else {
		due := later(next, s.now()).Truncate(time.Minute)
		var err error
		if keep = minuteKey(due) == t.it.due.Partition; !keep {
			err = s.markDue(ctx, t.seal, t.st.Connection, route{Host: t.st.Host, Account: t.st.Account}, due)
		}
		if err != nil {
			// The due entry it was found by stays: it is touched again.
			s.fail("due", err)
			keep = true
		} else {
			t.st.Due = due
		}
	}
	t.st.Lease = lease{}
	if err := s.put(ctx, t.seal, t.e, t.st); err != nil {
		s.fail("state", err)
		return
	}
	if !keep {
		s.unlist(ctx, t.it)
	}
}

// unlist deletes the due entry an item was found by, as it was read: one
// a hint wrote again since stays.
func (s *Service) unlist(ctx context.Context, it item) {
	if err := s.cfg.Entries.DeleteEntryAt(ctx, &it.due); err != nil && !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrNotFound) {
		s.fail("unlist", err)
	}
}

// unindex deletes a mailbox's index entry of a connection, and says
// whether it is gone.
func (s *Service) unindex(ctx context.Context, mailbox, connection string) bool {
	if err := s.cfg.Entries.DeleteEntry(ctx, PartitionMailbox+mailbox, connection); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail("index", err)
		return false
	}
	return true
}

// remove deletes a connection's index entries and due entry, then its state
// as it was read: a state whose index entries could not all be deleted
// stays, so the repair, which finds index entries only through states,
// deletes them later.
func (s *Service) remove(ctx context.Context, e *store.Entry, st *state) {
	gone := true
	for _, m := range st.Mailboxes {
		gone = s.unindex(ctx, m, e.Key) && gone
	}
	if !st.Due.IsZero() {
		if err := s.cfg.Entries.DeleteEntry(ctx, minuteKey(st.Due), e.Key); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.fail("remove", err)
		}
	}
	if !gone {
		return
	}
	if err := s.cfg.Entries.DeleteEntryAt(ctx, e); err != nil && !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrNotFound) {
		s.fail("remove", err)
	}
}
