package notices

import (
	"context"
	"time"

	"github.com/permaily-org/permaily/internal/store"
)

// Play makes s's admission played: the rate is counted on s's clock and,
// instead of waiting, each admission calls admit with the time it was
// given (nil: none), which may block as a wait for the rate would.
func Play(s *Service, admit func(ctx context.Context, at time.Time) error) {
	s.pace.now = s.cfg.Now
	s.pace.until = func(ctx context.Context, at time.Time) error {
		if admit == nil {
			return nil
		}
		return admit(ctx, at)
	}
}

// SetAttemptFor and SetEvery shorten an attempt's deadline and the
// runner's period.
func SetAttemptFor(s *Service, d time.Duration) { s.attemptFor = d }
func SetEvery(s *Service, d time.Duration)      { s.every = d }

// Pass is one pass as the runner makes it, due entries only, and waits for
// its attempts.
func Pass(ctx context.Context, s *Service) error {
	s.pass(ctx, false)
	return s.busy.wait(ctx)
}

// Wait waits for the attempts under way.
func Wait(ctx context.Context, s *Service) error { return s.busy.wait(ctx) }

// Sleep is the real wait for the rate.
func Sleep(ctx context.Context, d time.Duration) error { return sleep(ctx, d) }

// Unpaced is the real pacer's wait, on the real clock.
func Unpaced(ctx context.Context) error { return newPacer().wait(ctx) }

// Entry is a queued notice as signed, for tests that write one by hand.
type Entry struct {
	Notice     Notice
	Attempts   int
	NextAt     time.Time
	LeaseUntil time.Time
	By         string
}

// Write stores d at e as the queue would, past EntryMax refused.
func Write(ctx context.Context, s *Service, key []byte, e *store.Entry, d Entry) error {
	return s.write(ctx, key, e, entryDoc(d))
}

// Open reads an entry as the queue does.
func Open(key []byte, e *store.Entry) (Entry, bool) {
	d, ok := open(key, e)
	return Entry(d), ok
}

// Backoff is the wait after the attempts-th failed attempt.
func Backoff(attempts int) time.Duration { return backoff(attempts) }

// WaitingMax bounds the first attempts waiting for admission.
const WaitingMax = waitingMax
