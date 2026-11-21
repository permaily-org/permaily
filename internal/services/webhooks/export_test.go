package webhooks

import (
	"context"
	"time"
)

// Drain is a pass, then every item that may start next, until none may:
// what the workers do in a pass and the slots freed during it, waited for.
func Drain(ctx context.Context, s *Service) {
	s.scanDue(ctx)
	for s.dispatch(ctx) > 0 {
		s.work.Wait()
	}
}

// Scan is one pass's listing alone; Dispatch starts what may start without
// waiting; Wait waits for every touch under way.
func Scan(ctx context.Context, s *Service)         { s.scanDue(ctx) }
func Dispatch(ctx context.Context, s *Service) int { return s.dispatch(ctx) }
func Wait(s *Service)                              { s.work.Wait() }
func Repair(ctx context.Context, s *Service)       { s.repair(ctx) }
func Hinted(ctx context.Context, s *Service, mailbox string) {
	s.hinted(ctx, hint{mailbox: mailbox})
}

// Hints works on every hint waiting.
func Hints(ctx context.Context, s *Service) {
	for {
		select {
		case h := <-s.hints:
			s.hinted(ctx, h)
		default:
			return
		}
	}
}

// Ready is how many items are held ready, every host's and the most of one.
func Ready(s *Service) (total, most int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sched.ready()
}

// Name is the enclave's name in its leases.
func Name(s *Service) string { return s.name }

// SetTimings sets Run's timings for a test, and puts them back after.
func SetTimings(pass, repair time.Duration) func() {
	p, r := passEvery, repairCheckEvery
	passEvery, repairCheckEvery = pass, repair
	return func() { passEvery, repairCheckEvery = p, r }
}

// Busy says whether a hint or a wake waits for Run.
func Busy(s *Service) bool { return len(s.hints) > 0 || len(s.wake) > 0 }

// Wake asks Run for a dispatch.
func Wake(s *Service) { s.wake <- struct{}{} }
