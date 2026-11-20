package notices

import (
	"context"
	"sync"
	"time"
)

// Run is the runner: a pass every RunEvery until ctx ends, when the first
// attempts still waiting for admission give up too (the next runner, in
// either enclave, finds their notices).
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.stop()
			return
		case <-t.C:
			s.pass(ctx, false)
		}
	}
}

// Drain waits for the attempts under way, makes one pass that tries every
// notice whose lease ended, due or not, and waits for its attempts: what a
// test runs instead of the runner's ticker (noticestest.Subject). The pass
// keeps its bounds: at most ListMax entries listed, PassSendsMax admitted.
func (s *Service) Drain(ctx context.Context) error {
	if err := s.busy.wait(ctx); err != nil {
		return err
	}
	s.pass(ctx, true)
	return s.busy.wait(ctx)
}

// pass lists at most ListMax entries of the queue, from after where the
// last pass stopped and round to it, and admits at most PassSendsMax
// attempts of those that verify, are Sendable, due (any, with force) and
// whose lease ended. One that does not verify is counted and left; one no
// longer Sendable is dropped; the attempt reads each admitted one again.
// The next pass goes on after the last entry this one examined, whatever
// became of it, so entries that are never sent cannot keep the runners
// from the ones after them (ADR 0041's re-review, N1).
func (s *Service) pass(ctx context.Context, force bool) {
	key, err := s.signKey(ctx)
	if err != nil {
		s.failed("key")
		return
	}
	s.mu.Lock()
	start := s.cursor
	s.mu.Unlock()
	// after is the last key examined; wrapped, that the listing went
	// round from the start of the partition back towards start.
	after, wrapped := start, start == ""
	examined, sends := 0, 0
	for examined < ListMax {
		want := ListMax - examined
		page, err := s.cfg.Entries.ListEntries(ctx, PartitionQueue, "", after, want)
		if err != nil {
			s.failed("list")
			s.setCursor(after)
			return
		}
		for i := range page {
			e := &page[i]
			if wrapped && start != "" && e.Key > start {
				s.setCursor("") // round once: from the start next time
				return
			}
			d, ok := open(key, e)
			now := s.now()
			switch {
			case !ok:
				s.refuse(e)
			case !d.Notice.Sendable(now):
				s.drop(ctx, key, e, d, now)
			case now.Before(d.LeaseUntil), !force && now.Before(d.NextAt):
			default:
				release, err := s.admit(ctx)
				if err != nil {
					s.setCursor(after) // this one is examined again next
					return
				}
				s.busy.add()
				go func(k string) {
					defer s.busy.done()
					defer release()
					s.attempt(ctx, key, k, force)
				}(e.Key)
				sends++
			}
			examined++
			after = e.Key
			if sends == PassSendsMax {
				s.setCursor(after)
				return
			}
		}
		if len(page) < want {
			if wrapped {
				s.setCursor("")
				return
			}
			wrapped, after = true, ""
		}
	}
	s.setCursor(after)
}

func (s *Service) setCursor(k string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursor = k
}

// pacer spaces admissions SendsPerSecond a second.
type pacer struct {
	mu   sync.Mutex
	next time.Time
	now  func() time.Time
	// until waits until at, or for ctx.
	until func(ctx context.Context, at time.Time) error
}

func newPacer() *pacer {
	return &pacer{now: time.Now, until: func(ctx context.Context, at time.Time) error { return sleep(ctx, time.Until(at)) }}
}

// paceEvery is the time between two admissions.
const paceEvery = time.Second / SendsPerSecond

// wait takes the next admission's time and waits for it.
func (p *pacer) wait(ctx context.Context) error {
	p.mu.Lock()
	at := p.now()
	if at.Before(p.next) {
		at = p.next
	}
	p.next = at.Add(paceEvery)
	p.mu.Unlock()
	return p.until(ctx, at)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// tracker counts the attempts under way, and says when there are none.
type tracker struct {
	mu   sync.Mutex
	n    int
	idle chan struct{}
}

func (t *tracker) add() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.n == 0 {
		t.idle = make(chan struct{})
	}
	t.n++
}

func (t *tracker) done() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.n--; t.n == 0 {
		close(t.idle)
	}
}

func (t *tracker) wait(ctx context.Context) error {
	t.mu.Lock()
	if t.n == 0 {
		t.mu.Unlock()
		return nil
	}
	idle := t.idle
	t.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
