package webhooks_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webhook"
)

// failOn fails the requests to the store of one step whose partition starts
// with prefix and whose key is listed (a listing's key is "") or not, from
// the nth such request on (1 for the first), at most times times.
func (w *world) failOn(step store.Step, prefix string, listing bool, nth, times int) {
	var mu sync.Mutex
	seen, failed := 0, 0
	w.setInterrupt(func(_ context.Context, st store.Step, partition, key string) error {
		if st != step || !strings.HasPrefix(partition, prefix) || (key == "") != listing {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		if seen++; seen >= nth && failed < times {
			failed++
			return errStopped
		}
		return nil
	})
}

// Each request of the store failing alone, at the step it stops: nothing
// is called that should not be, and once it answers, what is due is told.
func TestEachRequestOfTheStoreFailingAlone(t *testing.T) {
	type world2 = world
	for _, tc := range []struct {
		name string
		do   func(t *testing.T, w *world2, s *webhooks.Service, c *conn)
	}{
		{"the repair's lease", func(t *testing.T, w *world2, s *webhooks.Service, c *conn) {
			w.failOn(store.StepEntry, "webhooks#repair", false, 1, 1)
			webhooks.Repair(ctx, s)
		}},
		{"the repair's read of a state", func(t *testing.T, w *world2, s *webhooks.Service, c *conn) {
			w.failOn(store.StepRead, webhooks.PartitionState, false, 1, 1)
			webhooks.Repair(ctx, s)
		}},
		{"the repair's index", func(t *testing.T, w *world2, s *webhooks.Service, c *conn) {
			w.failOn(store.StepEntry, webhooks.PartitionMailbox, false, 1, 1)
			webhooks.Repair(ctx, s)
		}},
		{"the repair's due entry", func(t *testing.T, w *world2, s *webhooks.Service, c *conn) {
			w.failOn(store.StepEntry, webhooks.PartitionDue, false, 1, 1)
			webhooks.Repair(ctx, s)
		}},
		{"the repair's listing of states", func(t *testing.T, w *world2, s *webhooks.Service, c *conn) {
			w.failOn(store.StepRead, webhooks.PartitionState, true, 1, 1)
			webhooks.Repair(ctx, s)
		}},
		{"the person's page", func(t *testing.T, w *world2, s *webhooks.Service, c *conn) {
			w.failOn(store.StepRead, webhooks.PartitionState, false, 1, 1)
			if _, err := s.States(ctx, c.Connection); err == nil {
				t.Fatal("no error")
			}
		}},
		{"Subscribe's index", func(t *testing.T, w *world2, s *webhooks.Service, c *conn) {
			w.failOn(store.StepEntry, webhooks.PartitionMailbox, false, 1, 1)
			if err := s.Subscribe(ctx, c.Subscription); err == nil {
				t.Fatal("no error")
			}
		}},
		{"an end's hint", func(t *testing.T, w *world2, s *webhooks.Service, c *conn) {
			w.failOn(store.StepRead, webhooks.PartitionState, false, 1, 1)
			s.Ended(c.Connection)
			webhooks.Hints(ctx, s)
		}},
		{"an arrival's hint", func(t *testing.T, w *world2, s *webhooks.Service, c *conn) {
			w.failOn(store.StepRead, webhooks.PartitionMailbox, true, 1, 1)
			webhooks.Hinted(ctx, s, c.Mailboxes[0])
		}},
		{"an arrival's due entry", func(t *testing.T, w *world2, s *webhooks.Service, c *conn) {
			w.failOn(store.StepEntry, webhooks.PartitionDue, false, 1, 1)
			webhooks.Hinted(ctx, s, c.Mailboxes[0])
		}},
		{"the sweep's listing", func(t *testing.T, w *world2, s *webhooks.Service, c *conn) {
			w.failOn(store.StepRead, webhooks.PartitionDue, true, 2, 1)
			webhooks.Scan(ctx, s)
		}},
		{"a touch's read of its state", func(t *testing.T, w *world2, s *webhooks.Service, c *conn) {
			w.failOn(store.StepRead, webhooks.PartitionState, false, 1, 1)
			webhooks.Drain(ctx, s)
		}},
		{"the write before a call", func(t *testing.T, w *world2, s *webhooks.Service, c *conn) {
			w.failOn(store.StepEntry, webhooks.PartitionState, false, 2, 1)
			webhooks.Drain(ctx, s)
			if len(w.sender.all()) != 0 {
				t.Fatal("called although its notice was not written down")
			}
			w.clock.add(webhook.LeaseFor)
		}},
		{"the next due entry", func(t *testing.T, w *world2, s *webhooks.Service, c *conn) {
			w.sender.set(func(x *sender) { x.script = []webhook.Outcome{webhook.OutcomeNoAnswer} })
			w.failOn(store.StepEntry, webhooks.PartitionDue, false, 1, 1)
			webhooks.Drain(ctx, s)
		}},
		{"the due entry's deletion", func(t *testing.T, w *world2, s *webhooks.Service, c *conn) {
			w.failOn(store.StepDeleteEntry, webhooks.PartitionDue, false, 1, 1)
			webhooks.Drain(ctx, s)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			s := w.enclave()
			c := w.make(s, made{newMail: true})
			w.clock.add(time.Minute)
			w.deliver(s, c, 0, true)
			webhooks.Hints(ctx, s)
			tc.do(t, w, s, c)
			w.setInterrupt(nil)
			w.clock.add(webhook.RepairEvery)
			webhooks.Repair(ctx, s)
			w.every(time.Minute, 5*time.Minute, s)
			if len(c.vendor.Taken()) != 1 {
				t.Fatalf("told %d times", len(c.vendor.Taken()))
			}
		})
	}
}

// A connection dropped while the store fails to delete its entries: what
// stays calls nothing, and goes with the next repair.
func TestADropWhoseDeletesFail(t *testing.T) {
	for _, prefix := range []string{webhooks.PartitionState, webhooks.PartitionMailbox} {
		w := newWorld(t)
		s := w.enclave()
		c := w.make(s, made{newMail: true})
		w.run(s)
		w.fake.Revoke(c.Connection)
		s.Ended(c.Connection)
		w.failOn(store.StepDeleteEntry, prefix, false, 1, 1)
		w.run(s)
		w.setInterrupt(nil)
		w.clock.add(webhook.RepairEvery)
		webhooks.Repair(ctx, s)
		w.run(s)
		if len(w.entries(webhooks.PartitionState)) != 0 || len(w.entries(webhooks.PartitionMailbox+c.Mailboxes[0])) != 0 || len(w.sender.all()) != 0 {
			t.Fatal("entries stay, or a call")
		}
	}
}

// More than EvaluationsPerMinute connections with new mail in a minute: the
// rest are evaluated the next minute, none lost.
func TestEvaluationsPastTheMinutesBound(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	var all []*conn
	for i := range webhook.EvaluationsPerMinute + 1 {
		// Each on a host and in an account of its own: only the
		// evaluations' bound binds.
		all = append(all, w.make(s, made{newMail: true, host: fmt.Sprintf("h%03d.example", i)}))
	}
	w.clock.add(time.Minute)
	for _, c := range all {
		w.deliver(s, c, 0, true)
	}
	w.run(s)
	if n := len(w.sender.all()); n > webhook.EvaluationsPerMinute || n == 0 {
		t.Fatalf("%d told in the first minute", n)
	}
	w.every(time.Minute, 3*time.Minute, s)
	if n := len(w.sender.all()); n != len(all) {
		t.Fatalf("%d told of %d", n, len(all))
	}
}
