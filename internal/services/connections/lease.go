package connections

import (
	"context"
	"errors"
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
}

// errLeaseStarted is a second Start, or a Start after Finish.
var errLeaseStarted = errors.New("connections: the answer was started or finished already")

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
	c, cancel := context.WithDeadline(ctx, time.Now().Add(deadline.Sub(s.now())))
	l.started, l.cancel = true, cancel
	l.timer.Stop()
	l.mu.Unlock()
	go l.watch(c, deadline)
	return c, nil
}

// watch reads the authority again every ConnectionFresh while the answer is
// sent, and ends its context when it no longer allows it or cannot be read,
// or at its deadline by the service's clock.
func (l *lease) watch(c context.Context, deadline time.Time) {
	t := time.NewTicker(leaseTick)
	defer t.Stop()
	for {
		select {
		case <-c.Done():
			return
		case <-t.C:
		}
		g := l.grant
		if err := l.s.again(c, &g); err != nil || !l.s.now().Before(deadline) {
			l.mu.Lock()
			if l.cancel != nil {
				l.cancel()
			}
			l.mu.Unlock()
			return
		}
		l.mu.Lock()
		l.grant.CheckedAt = g.CheckedAt
		l.mu.Unlock()
	}
}

// leaseTick is how often a started lease looks at the service's clock: it
// reads the authority again only once its freshness passed (again), so
// every oauth.ConnectionFresh.
var leaseTick = 250 * time.Millisecond

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
