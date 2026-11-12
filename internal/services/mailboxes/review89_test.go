package mailboxes_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/store"
)

// The findings of PR #89's code review on the mailboxes service: each test is
// the sequence the reviewer described.

// failOnce makes the first request that match says fails, as a request to
// DynamoDB can, and lets every other one through.
func (w *world) failOnce(match func(step store.Step, partition, key string) bool) {
	var once onlyOnce
	w.setInterrupt(func(_ context.Context, step store.Step, partition, key string) error {
		if !match(step, partition, key) {
			return nil
		}
		var err error
		once.Do(func() { err = errStopped })
		return err
	})
}

// Review 89-2: a pause or a resume that committed keeps its notice. The read
// of the mailbox's settings, which the answer needs, fails once: whatever
// the call answers, the transition it committed is told exactly once, and a
// retry that finds it done tells nobody again.
func TestReview89APauseThatCommittedIsToldOnceWhateverItsAnswer(t *testing.T) {
	w := newWorld(t)
	e := w.enclave()
	me := caller("acc_me", w.clock)
	m := e.mustCreate(t, me)
	for _, step := range []struct {
		name string
		act  func(context.Context, string) error
		kind mailboxes.NoticeKind
	}{
		{"pause", func(ctx context.Context, id string) error { _, _, err := e.Pause(ctx, id); return err }, mailboxes.NoticePaused},
		{"resume", func(ctx context.Context, id string) error { _, _, err := e.Resume(ctx, id); return err }, mailboxes.NoticeResumed},
	} {
		w.clock.add(time.Minute)
		before := count(w.kinds(), step.kind)
		w.failOnce(func(s store.Step, partition, key string) bool {
			return s == store.StepRead && partition == inbox.Partition(m.ID) && key == inbox.KeySettings
		})
		first := step.act(ctx, m.ID)
		w.setInterrupt(nil)
		if !errors.Is(first, errStopped) {
			t.Fatalf("%s: the read of the settings did not fail the call: %v", step.name, first)
		}
		if err := step.act(ctx, m.ID); err != nil {
			t.Fatalf("%s, again: %v", step.name, err)
		}
		if got := count(w.kinds(), step.kind) - before; got != 1 {
			t.Fatalf("%s: told %d times", step.name, got)
		}
	}
}

// Review 89-3: a disconnect whose cleanup fails after it committed still
// tells its person, and the retry that finds it disconnected tells nobody.
// The same for a lost record, whose commit is its removal.
func TestReview89ADisconnectWhoseCleanupFailsIsStillTold(t *testing.T) {
	for _, lost := range []bool{false, true} {
		name := "readable"
		if lost {
			name = "lost"
		}
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			e := w.enclave()
			me := caller("acc_me", w.clock)
			m := e.mustCreate(t, me)
			if lost {
				w.lose(m.ID)
			}
			// The first deletion is the address's pointer, after the commit.
			w.failOnce(func(s store.Step, _, _ string) bool { return s == store.StepDeleteEntry })
			first := e.Disconnect(ctx, me, m.ID)
			w.setInterrupt(nil)
			if !errors.Is(first, errStopped) {
				t.Fatalf("the cleanup did not fail the call: %v", first)
			}
			if err := e.Disconnect(ctx, me, m.ID); err != nil {
				t.Fatalf("again: %v", err)
			}
			if got := count(w.kinds(), mailboxes.NoticeDisconnected); got != 1 {
				t.Fatalf("told %d times", got)
			}
		})
	}
}
