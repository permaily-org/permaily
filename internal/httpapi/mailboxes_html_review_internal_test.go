package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The code review of pull request 5 (#92), the findings in httpapi's own
// state: each test failed before its fix, and fails again when the fix is
// broken on purpose.

// Finding 5: an account with nothing running can have two renders waiting,
// not three: the share bounds what waits, whatever is running.
func TestReview5AnAccountWithNothingRunningWaitsTwice(t *testing.T) {
	s := newRenderSlots(2, 16)
	const wait = 2 * time.Second
	ctx := context.Background()
	// Both slots, held by two other accounts.
	for _, other := range []string{"acc_other_1", "acc_other_2"} {
		release, err := s.take(ctx, other, wait)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	waits := make(chan error, 3)
	for range 2 {
		go func() {
			release, err := s.take(ctx, "acc_mine", wait)
			if release != nil {
				release()
			}
			waits <- err
		}()
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		s.mu.Lock()
		n := s.waiting
		s.mu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d waiting", n)
		}
	}
	start := time.Now()
	if _, err := s.take(ctx, "acc_mine", wait); !errors.Is(err, contracts.ErrTooManyRequests) || time.Since(start) > time.Second {
		t.Fatalf("a third wait of an account with nothing running: %v after %v", err, time.Since(start))
	}
	for range 2 {
		if err := <-waits; !errors.Is(err, contracts.ErrBusy) {
			t.Fatalf("a wait: %v", err)
		}
	}
}
