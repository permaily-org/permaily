package connections

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/webhook"
)

// The permits of webhook calls (ADR 0039, "Revocation, the end and
// removal"; design review, finding 4): registered under the connection
// before Admit returns, beside the leases of reads (lease.go), and cut by
// the same stops before they are acknowledged. revokeOnce, which every end
// of a connection or of a device in this enclave goes through before its
// record is written, cuts them; WebhookOff and Stop mark the webhook off
// in memory and cut them before writing the record. A permit is never
// renewed: it is written by WriteBy or not at all, and the next attempt
// is admitted afresh.

// hooks is what the service keeps of webhooks in this enclave's memory.
type hooks struct {
	// key seals a webhook's private part (PurposeWebhookSeal).
	key []byte
	// permits are each connection's permits not finished; gen counts each
	// connection's cuts, so a permit whose Admit read before a cut is never
	// registered after it.
	permits map[string]map[*permit]struct{}
	gen     map[string]uint64
	// off are the webhooks this enclave turned off or stopped since it
	// started (a mark per stop, taken back by one that lost to another
	// write): never called here again, whatever a cache says.
	off map[string]int
	// listed is the last listing of every webhook (Webhooks).
	listed     []string
	listedRead time.Time
}

// permit implements webhooks.Permit.
type permit struct {
	s       *Service
	conn    string
	writeBy time.Time

	mu       sync.Mutex
	isCut    bool
	started  bool
	finished bool
	cancel   context.CancelCauseFunc
	stop     context.CancelFunc
}

var _ webhooks.Permit = (*permit)(nil)

// errFinished is the cause a finished permit's context ends with.
var errFinished = errors.New("connections: the webhook's call finished")

// permitGen is the connection's count of cuts, read before an admission's
// read.
func (s *Service) permitGen(conn string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hooks.gen[conn]
}

// registerPermit registers a permit for conn written by writeBy, only if
// no cut came since gen was read; false otherwise (the caller reads again).
func (s *Service) registerPermit(conn string, gen uint64, writeBy time.Time) (*permit, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hooks.gen[conn] != gen {
		return nil, false
	}
	p := &permit{s: s, conn: conn, writeBy: writeBy}
	if s.hooks.permits == nil {
		s.hooks.permits = map[string]map[*permit]struct{}{}
	}
	if s.hooks.permits[conn] == nil {
		s.hooks.permits[conn] = map[*permit]struct{}{}
	}
	s.hooks.permits[conn][p] = struct{}{}
	return p, true
}

// cutPermits cuts every permit of conn and counts the cut, so that an
// admission whose read began before it registers none. Called with s.mu
// not held.
func (s *Service) cutPermits(conn string) {
	s.mu.Lock()
	if s.hooks.gen == nil {
		s.hooks.gen = map[string]uint64{}
	}
	s.hooks.gen[conn]++
	var cut []*permit
	for p := range s.hooks.permits[conn] {
		cut = append(cut, p)
	}
	s.mu.Unlock()
	for _, p := range cut {
		p.cut()
	}
}

// markOff notes conn's webhook turned off or stopped in this enclave and
// cuts its permits, before its record is written; undo takes back this one
// mark.
func (s *Service) markOff(conn string) (undo func()) {
	s.mu.Lock()
	if s.hooks.off == nil {
		s.hooks.off = map[string]int{}
	}
	s.hooks.off[conn]++
	delete(s.conns, conn)
	s.mu.Unlock()
	s.cutPermits(conn)
	return sync.OnceFunc(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.hooks.off[conn]--; s.hooks.off[conn] <= 0 {
			delete(s.hooks.off, conn)
		}
	})
}

// offHere says this enclave turned conn's webhook off or stopped it.
func (s *Service) offHere(conn string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hooks.off[conn] > 0
}

func (p *permit) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.isCut = true
	if p.cancel != nil {
		p.cancel(webhook.ErrCut)
	}
}

// Start implements webhooks.Permit.
func (p *permit) Start(ctx context.Context) (context.Context, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.s.now()
	if p.isCut || p.started || p.finished || !now.Before(p.writeBy) {
		return nil, webhook.ErrCut
	}
	c, cancel := context.WithCancelCause(ctx)
	// The service's clock says how long is left; the context's deadline is
	// the machine's.
	c, stop := context.WithDeadlineCause(c, time.Now().Add(p.writeBy.Sub(now)), webhook.ErrCut)
	p.started, p.cancel, p.stop = true, cancel, stop
	return c, nil
}

// WriteBy implements webhooks.Permit.
func (p *permit) WriteBy() time.Time { return p.writeBy }

// Finish implements webhooks.Permit.
func (p *permit) Finish() {
	p.mu.Lock()
	if p.finished {
		p.mu.Unlock()
		return
	}
	p.finished = true
	if p.cancel != nil {
		p.cancel(errFinished)
		p.stop()
	}
	p.mu.Unlock()
	s := p.s
	s.mu.Lock()
	delete(s.hooks.permits[p.conn], p)
	if len(s.hooks.permits[p.conn]) == 0 {
		delete(s.hooks.permits, p.conn)
	}
	s.mu.Unlock()
}
