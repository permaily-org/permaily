package webhooks_test

import (
	"context"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/webhook"
)

// hooks runs a test's code around the reads the queue makes before a call.
type hooks struct {
	webhooks.Connections
	afterRecheck func()
	beforeStart  func()
}

func (h *hooks) Recheck(ctx context.Context, c string, m webhooks.Match) (bool, error) {
	ok, err := h.Connections.Recheck(ctx, c, m)
	if h.afterRecheck != nil {
		h.afterRecheck()
	}
	return ok, err
}

func (h *hooks) Admit(ctx context.Context, c string) (*webhooks.Subscription, webhooks.Permit, error) {
	s, p, err := h.Connections.Admit(ctx, c)
	if p != nil && h.beforeStart != nil {
		p = &hookedPermit{Permit: p, before: h.beforeStart}
	}
	return s, p, err
}

type hookedPermit struct {
	webhooks.Permit
	before func()
}

func (p *hookedPermit) Start(ctx context.Context) (context.Context, error) {
	p.before()
	return p.Permit.Start(ctx)
}

// Re-review N2: a match is sent only under the basis of the read that
// admits its call. A rule saved after Recheck said the match stands and
// before Admit read again (here through the other enclave, while this
// worker was delayed twenty seconds) makes the two bases differ: the match
// is discarded (changed), evaluated again under the new rule, which holds
// it, and nothing is ever called for it.
func TestN2ARuleSavedBetweenTheRecheckAndTheAdmissionIsNeverTold(t *testing.T) {
	for _, delay := range []time.Duration{0, 20 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			w := newWorld(t)
			h := &hooks{Connections: w.fake}
			w.conns = h
			s := w.enclave()
			c := w.make(s, made{newMail: true})
			w.clock.add(time.Minute)
			w.deliver(s, c, 0, true)
			saved := false
			h.afterRecheck = func() {
				if !saved {
					saved = true
					w.clock.add(delay)
					w.fake.HoldAll(c.Account)
				}
			}
			w.every(5*time.Second, 10*time.Minute, s)
			if !saved {
				t.Fatal("never rechecked")
			}
			if n := len(w.sender.all()); n != 0 {
				t.Fatalf("%d calls for a match a rule saved before its admission holds", n)
			}
			if skipped(s, webhooks.SkipChanged) == 0 {
				t.Fatal("not discarded as changed")
			}
		})
	}
}

// Re-review N2, when an attempt begins: at the read that admits it. A rule
// saved after that read lets that attempt finish (an answer being prepared,
// ADR 0040), and applies to the next: its retry is discarded. A worker
// delayed past the permit's WriteBy writes nothing (cut), and its next
// attempt is admitted and rechecked afresh.
func TestN2AnAttemptBeginsAtTheReadThatAdmitsIt(t *testing.T) {
	t.Run("a rule saved after the admitting read", func(t *testing.T) {
		w := newWorld(t)
		h := &hooks{Connections: w.fake}
		w.conns = h
		s := w.enclave()
		c := w.make(s, made{newMail: true})
		w.clock.add(time.Minute)
		w.deliver(s, c, 0, true)
		h.beforeStart = func() {
			h.beforeStart = nil
			w.fake.HoldAll(c.Account)
		}
		w.sender.set(func(x *sender) { x.script = []webhook.Outcome{webhook.OutcomeNoAnswer} })
		w.every(time.Minute, time.Hour, s)
		if n := len(w.sender.all()); n != 1 {
			t.Fatalf("%d calls: the admitted attempt not written, or retried under the new rule", n)
		}
		if skipped(s, webhooks.SkipChanged) != 1 {
			t.Fatal("its retry not discarded")
		}
	})

	t.Run("a worker delayed past WriteBy", func(t *testing.T) {
		w := newWorld(t)
		h := &hooks{Connections: w.fake}
		w.conns = h
		s := w.enclave()
		c := w.make(s, made{newMail: true})
		w.clock.add(time.Minute)
		w.deliver(s, c, 0, true)
		h.beforeStart = func() {
			h.beforeStart = nil
			w.clock.add(6 * time.Second)
		}
		w.run(s)
		if len(w.sender.all()) != 0 || outcomes(s, webhook.OutcomeCut) != 1 {
			t.Fatal("written past WriteBy")
		}
		w.clock.add(5 * time.Second)
		w.run(s)
		if calls := w.sender.all(); len(calls) != 1 || len(c.vendor.Taken()) != 1 {
			t.Fatal("not attempted again")
		}
	})
}
