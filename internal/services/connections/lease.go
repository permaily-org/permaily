package connections

import (
	"context"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// The service's lease (ADR 0030, "Release"; N2, N3 and the owner's decision
// 14): registered under its connection before the answer is returned, cut
// by a revocation in this enclave before the record is written, and, once
// started, reading the authority again every oauth.ConnectionFresh while
// the answer is sent.

// lease implements Release.
type lease struct {
	s       *Service
	grant   oauth.Grant
	r       *reading
	receipt string

	mu       sync.Mutex
	isCut    bool
	started  bool
	finished bool
	cancel   context.CancelFunc
	timer    *time.Timer
	// cutoff ends a started answer when its verified authority's freshness
	// passes (PR #136's second review, finding 3): armed at Start, moved
	// later only by a refresh that arrived before it fired.
	cutoff *time.Timer
}

// errLeaseStarted is a second Start, or a Start after Finish.
var errLeaseStarted error = &kind{"connections: the answer was started or finished already"}

// newLease registers a lease for r's answer, whose receipt is receipt.
func (s *Service) newLease(r *reading, receipt string) *lease {
	l := &lease{s: s, grant: *r.g, r: r, receipt: receipt}
	s.mu.Lock()
	if s.leases[l.grant.Connection] == nil {
		s.leases[l.grant.Connection] = map[*lease]struct{}{}
	}
	s.leases[l.grant.Connection][l] = struct{}{}
	s.mu.Unlock()
	// A lease never finished ends at TransferMax.
	l.timer = time.AfterFunc(TransferMax, func() { l.Finish(false) })
	return l
}

// cut is a revocation in this enclave: a lease not started never starts,
// and a started one's context ends.
func (l *lease) cut() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.isCut = true
	if l.cancel != nil {
		l.cancel()
	}
}

// Start implements Release.
func (l *lease) Start(ctx context.Context) (context.Context, error) {
	l.mu.Lock()
	if l.started || l.finished {
		l.mu.Unlock()
		return nil, errLeaseStarted
	}
	cut := l.isCut
	l.mu.Unlock()
	s := l.s
	if cut || s.again(ctx, &l.grant) != nil {
		return nil, l.tooLate(ctx)
	}
	l.mu.Lock()
	if l.isCut || l.finished {
		l.mu.Unlock()
		return nil, l.tooLate(ctx)
	}
	deadline := s.now().Add(TransferMax)
	if e := l.grant.Expires(); !e.IsZero() && e.Before(deadline) {
		deadline = e
	}
	now := s.now()
	c, cancel := context.WithDeadline(ctx, time.Now().Add(deadline.Sub(now)))
	l.started, l.cancel = true, cancel
	l.cutoff = time.AfterFunc(l.grant.FreshUntil().Sub(now), cancel)
	l.timer.Stop()
	l.mu.Unlock()
	go l.watch(c, deadline)
	return c, nil
}

// refreshed extends a started answer's authority to a read that began at
// checked, only while the answer is still within the authority it has: its
// context not ended and its cutoff not fired. A read that returns after
// the freshness passed revives nothing.
func (l *lease) refreshed(c context.Context, checked time.Time) {
	now := l.s.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if c.Err() != nil || !checked.After(l.grant.CheckedAt) || !now.Before(l.grant.FreshUntil()) || l.cutoff == nil || !l.cutoff.Stop() {
		return
	}
	l.grant.CheckedAt = checked
	l.cutoff = time.AfterFunc(l.grant.FreshUntil().Sub(now), l.cancel)
}

// watch keeps a started answer within its verified authority (the owner's
// decisions 6 and 14, N2): it reads the authority again leaseAhead before
// its freshness passes, in a read of its own, from its first look on (an
// answer started with less left reads it at once), and ends the answer's
// context when a read no longer allows the grant or cannot be made. The
// freshness passing before a newer verified read arrived, whatever that
// read is doing, is ended by the lease's cutoff, and the deadline by the
// context's; each tick looks at the service's clock too.
func (l *lease) watch(c context.Context, deadline time.Time) {
	t := time.NewTicker(leaseTick)
	defer t.Stop()
	type result struct {
		checked time.Time
		err     error
	}
	done := make(chan result, 1)
	reading := false
	end := func() {
		l.mu.Lock()
		if l.cancel != nil {
			l.cancel()
		}
		l.mu.Unlock()
	}
	// look ends the answer past its deadline or freshness, and begins a read
	// leaseAhead before the freshness passes; false once it ended.
	look := func() bool {
		now := l.s.now()
		l.mu.Lock()
		g := l.grant
		l.mu.Unlock()
		if !now.Before(deadline) || !now.Before(g.FreshUntil()) {
			end()
			return false
		}
		if !reading && !now.Before(g.FreshUntil().Add(-leaseAhead)) {
			reading = true
			go func() {
				checked, err := l.s.verify(c, &g)
				done <- result{checked, err}
			}()
		}
		return true
	}
	for look() {
		select {
		case <-c.Done():
			return
		case r := <-done:
			reading = false
			if r.err != nil {
				end()
				return
			}
			l.refreshed(c, r.checked)
		case <-t.C:
		}
	}
}

// leaseTick is how often a started lease looks at the service's clock.
var leaseTick = 250 * time.Millisecond

// leaseAhead is how long before a started lease's freshness passes it reads
// the authority again, so that a read of ordinary latency renews it in
// time; a slower one does not, and the answer ends.
const leaseAhead = 2 * time.Second

// tooLate writes the too_late receipt naming the answer's, and answers
// ErrTooLate.
func (l *lease) tooLate(ctx context.Context) error {
	rc := l.s.receipt(l.r, receipts.KindRefused)
	rc.Refused = &receipts.Refused{Reason: receipts.RefusedTooLate, Receipt: l.receipt}
	if err := l.s.writeReceipt(context.WithoutCancel(ctx), l.grant.Account, rc, time.Time{}); err != nil {
		l.s.count("receipt_failed")
	}
	l.s.count("too_late")
	return ErrTooLate
}

// Finish implements Release.
func (l *lease) Finish(sent bool) {
	l.mu.Lock()
	if l.finished {
		l.mu.Unlock()
		return
	}
	l.finished = true
	if l.cancel != nil {
		l.cancel()
	}
	if l.cutoff != nil {
		l.cutoff.Stop()
	}
	l.timer.Stop()
	if !sent || !l.started {
		l.s.count("cut")
	}
	l.mu.Unlock()
	l.s.mu.Lock()
	delete(l.s.leases[l.grant.Connection], l)
	if len(l.s.leases[l.grant.Connection]) == 0 {
		delete(l.s.leases, l.grant.Connection)
	}
	l.s.mu.Unlock()
}
