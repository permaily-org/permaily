package httpapi

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Test 36 with the production numbers, which MailboxesAPI takes when its
// configuration names none: two renders at once, sixteen waiting for five
// seconds, one running and two waiting per account (ADR 0026, "Limits").
func TestHTML36TheProductionRenderSlots(t *testing.T) {
	s := newRenderSlots(contracts.MessageHTMLRenders, contracts.MessageHTMLRenderQueue)
	ctx := context.Background()
	var releases []func()
	for i := range contracts.MessageHTMLRenders {
		release, err := s.take(ctx, fmt.Sprintf("acc_running_%d", i), contracts.MessageHTMLRenderWait)
		if err != nil {
			t.Fatalf("running %d: %v", i, err)
		}
		releases = append(releases, release)
	}
	// One account's share: one running and two waiting, the next refused.
	type result struct {
		err  error
		took time.Duration
	}
	waits := make(chan result, contracts.MessageHTMLRenderQueue)
	wait := func(account string) {
		start := time.Now()
		release, err := s.take(ctx, account, contracts.MessageHTMLRenderWait)
		if release != nil {
			release()
		}
		waits <- result{err, time.Since(start)}
	}
	waiting := func() int {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.waiting
	}
	settle := func(n int) {
		for deadline := time.Now().Add(5 * time.Second); waiting() != n; {
			if time.Now().After(deadline) {
				t.Fatalf("%d waiting, want %d", waiting(), n)
			}
			time.Sleep(time.Millisecond)
		}
	}
	go wait("acc_running_0")
	go wait("acc_running_0")
	settle(2)
	if _, err := s.take(ctx, "acc_running_0", contracts.MessageHTMLRenderWait); !errors.Is(err, contracts.ErrTooManyRequests) {
		t.Fatalf("past the account's share: %v", err)
	}
	// The queue fills at sixteen, from accounts within their share.
	for i := 2; i < contracts.MessageHTMLRenderQueue; i += 2 {
		go wait(fmt.Sprintf("acc_waiting_%d", i))
		go wait(fmt.Sprintf("acc_waiting_%d", i))
	}
	settle(contracts.MessageHTMLRenderQueue)
	if _, err := s.take(ctx, "acc_one_more", contracts.MessageHTMLRenderWait); !errors.Is(err, contracts.ErrBusy) {
		t.Fatalf("past the queue of %d: %v", contracts.MessageHTMLRenderQueue, err)
	}
	// Nobody gives a slot back: every wait ends busy after five seconds.
	for range contracts.MessageHTMLRenderQueue {
		r := <-waits
		if !errors.Is(r.err, contracts.ErrBusy) || r.took < contracts.MessageHTMLRenderWait || r.took > contracts.MessageHTMLRenderWait+2*time.Second {
			t.Fatalf("a wait: %v after %v", r.err, r.took)
		}
	}
	for _, release := range releases {
		release()
	}
	if release, err := s.take(ctx, "acc_one_more", contracts.MessageHTMLRenderWait); err != nil {
		t.Fatalf("after: %v", err)
	} else {
		release()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != 0 || s.waiting != 0 || len(s.accounts) != 0 {
		t.Fatalf("left: %d running, %d waiting, %d accounts", s.active, s.waiting, len(s.accounts))
	}
}
