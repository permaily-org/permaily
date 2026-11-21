package notices

import (
	"context"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/store"
)

// Run is the runner: a pass every RunEvery until ctx ends or Stop is
// called, when nothing more is admitted and the first attempts still
// waiting for admission give up too (the next runner, in either enclave,
// finds their notices). The attempts it admitted are not cut when ctx
// ends: Stop waits for them.
func (s *Service) Run(ctx context.Context) {
	s.runs.add()
	defer s.runs.done()
	t := time.NewTicker(s.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.stop()
			return
		case <-s.life.Done():
			return
		case <-t.C:
			s.pass(ctx, s.work, false)
		}
	}
}

// Stop ends the queue's work at a stop (the code review of ADR 0041's
// pull request 7, 1): it admits nothing more, waits for Run to return and
// for the attempts under way to finish, a send SES accepted marked as sent,
// and returns nil. When ctx ends first, it abandons the attempts still
// under way and returns ctx's error: the stop did not drain, and a notice
// whose send was accepted but not marked is sent again once its lease ends
// (at least once).
func (s *Service) Stop(ctx context.Context) error {
	s.stop()
	err := s.runs.wait(ctx)
	if err == nil {
		err = s.busy.wait(ctx)
	}
	if err != nil {
		s.abandon()
	}
	return err
}

// Drain waits for the attempts under way, makes one pass that tries every
// notice whose lease ended, due or not, and waits for its attempts: what a
// test runs instead of the runner's ticker (noticestest.Subject). The pass
// keeps its bounds: at most ListMax rows read, PassSendsMax admitted.
func (s *Service) Drain(ctx context.Context) error {
	if err := s.busy.wait(ctx); err != nil {
		return err
	}
	s.pass(ctx, ctx, true)
	return s.busy.wait(ctx)
}

// pass reads at most ListMax rows of the queue, a page of at most
// store.MaxScanRead at a time (and DynamoDB's 1 MB), from the greatest key
// down, from below where the last pass stopped and round to it, and admits
// at most PassSendsMax attempts of the entries that verify, are Sendable,
// due (any, with force) and whose lease ended. Every row read counts, the
// expired ones the storage has not deleted yet included, and a pass holds
// one page at a time. One that does not verify, or is past EntryMax, is
// counted and left; one no longer Sendable is dropped; the attempt reads
// each admitted one again. The next pass goes on below the last row this
// one read, whatever became of it, so rows that are never sent cannot
// keep the runners from the ones after them (ADR 0041's re-review, N1, and
// its code review, 2 and 3). Its attempts run in work, which Run's
// ending does not cut (Stop).
func (s *Service) pass(ctx, work context.Context, force bool) {
	key, err := s.signKey(ctx)
	if err != nil {
		s.failed("key")
		return
	}
	s.mu.Lock()
	start := s.cursor
	s.mu.Unlock()
	// before is the last key read; wrapped, that the scan went round from
	// the greatest key of the partition back down towards start.
	before, wrapped := start, start == ""
	read, sends := 0, 0
	for read < ListMax {
		page, err := s.cfg.Entries.ScanEntriesDown(ctx, PartitionQueue, "", before, min(store.MaxScanRead, ListMax-read))
		if err != nil {
			s.failed("list")
			s.setCursor(before)
			return
		}
		read += max(page.Read, 1) // a page that read nothing still counts
		for i := range page.Entries {
			e := &page.Entries[i]
			if wrapped && start != "" && e.Key < start {
				s.setCursor("") // round once: from the greatest next time
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
					s.setCursor(before) // this one is read again next
					return
				}
				s.busy.add()
				go func(k string) {
					defer s.busy.done()
					defer release()
					s.attempt(work, key, k, force)
				}(e.Key)
				sends++
			}
			before = e.Key
			if sends == PassSendsMax {
				s.setCursor(before)
				return
			}
		}
		if page.Last != "" {
			before = page.Last // past the expired rows at the page's end too
		}
		switch {
		case wrapped && start != "" && before != "" && before < start, !page.More && wrapped:
			s.setCursor("")
			return
		case !page.More:
			wrapped, before = true, ""
		}
	}
	s.setCursor(before)
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
