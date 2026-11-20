// Package noticestest is the fake queue of notices the callers are built
// against (ADR 0041: the connections service, the receipts' chain and
// accounts), a Deliverer that records what it is given, and the contract
// the fake and the real queue both keep. Tests only.
package noticestest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/notices"
)

// Fake is a Queue in memory: each Key once, delivered only by Drain, one
// attempt each, tried again on the next Drain after an error that is not
// notices.ErrSkip. Fail, when set, is what Enqueue answers, storing
// nothing (a storage that cannot be written).
type Fake struct {
	Deliverer notices.Deliverer
	Fail      error

	mu     sync.Mutex
	queued []notices.Notice
	keys   map[string]bool
	done   map[string]bool
}

var _ notices.Queue = (*Fake)(nil)

// Enqueue implements notices.Queue.
func (f *Fake) Enqueue(_ context.Context, n notices.Notice) error {
	if err := n.Check(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return f.Fail
	}
	if f.keys == nil {
		f.keys, f.done = map[string]bool{}, map[string]bool{}
	}
	if f.keys[n.Key] {
		return nil
	}
	f.keys[n.Key] = true
	f.queued = append(f.queued, n)
	return nil
}

// Queued returns every notice queued, in order, each Key once.
func (f *Fake) Queued() []notices.Notice {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]notices.Notice(nil), f.queued...)
}

// Drain makes one attempt of every notice not yet delivered or skipped.
func (f *Fake) Drain(ctx context.Context) error {
	f.mu.Lock()
	var due []notices.Notice
	for _, n := range f.queued {
		if !f.done[n.Key] {
			due = append(due, n)
		}
	}
	f.mu.Unlock()
	for _, n := range due {
		err := f.Deliverer.Deliver(ctx, n)
		if err == nil || errors.Is(err, notices.ErrSkip) {
			f.mu.Lock()
			f.done[n.Key] = true
			f.mu.Unlock()
		}
	}
	return nil
}

// Recorder is a Deliverer that keeps what it is given. Fail, when it
// answers an error for a notice, is what Deliver answers instead.
type Recorder struct {
	Fail func(notices.Notice) error

	mu        sync.Mutex
	delivered []notices.Notice
	tries     int
}

var _ notices.Deliverer = (*Recorder)(nil)

// Deliver implements notices.Deliverer.
func (r *Recorder) Deliver(_ context.Context, n notices.Notice) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tries++
	if r.Fail != nil {
		if err := r.Fail(n); err != nil {
			return err
		}
	}
	r.delivered = append(r.delivered, n)
	return nil
}

// Delivered returns what was delivered, in order.
func (r *Recorder) Delivered() []notices.Notice {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notices.Notice(nil), r.delivered...)
}

// Tries is how many times Deliver was called.
func (r *Recorder) Tries() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tries
}

// Subject is a queue under the contract: Drain waits for the attempts the
// queue has under way, then makes one attempt of every notice queued whose
// lease no attempt holds, whatever its wait between attempts.
type Subject interface {
	notices.Queue
	Drain(ctx context.Context) error
}

// Sample is a valid notice of an approval, its key made from id.
func Sample(id string) notices.Notice {
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	return notices.Notice{
		Account: "acc_ana", Kind: notices.KindConnectionApproved, Key: notices.Key("acc_ana", notices.KindConnectionApproved, id), At: at,
		Facts: notices.Facts{Vendor: "claude.ai", Resource: contracts.ResourceMCP, Parts: []contracts.Part{contracts.PartSender, contracts.PartSubject}, EndsAt: at.AddDate(0, 0, 90)},
	}
}

// Contract runs what every queue keeps (ADR 0041, E20 to E23 in their
// part that needs no clock): newQueue makes a fresh queue over fresh
// storage that delivers through d. ADR 0041's pull request 1 runs it on
// the real queue, over one store and over two enclaves' queues sharing it.
func Contract(t *testing.T, newQueue func(t *testing.T, d notices.Deliverer) Subject) {
	ctx := context.Background()
	t.Run("a notice is delivered once", func(t *testing.T) {
		d := &Recorder{}
		q := newQueue(t, d)
		must(t, q.Enqueue(ctx, Sample("rcp_1")))
		drain(t, q, 2)
		if got := d.Delivered(); len(got) != 1 || got[0].Key != Sample("rcp_1").Key {
			t.Fatalf("delivered %v, want the one notice once", got)
		}
	})
	t.Run("one key is one notice", func(t *testing.T) {
		d := &Recorder{}
		q := newQueue(t, d)
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() { must(t, q.Enqueue(ctx, Sample("rcp_1"))) })
		}
		wg.Wait()
		must(t, q.Enqueue(ctx, Sample("rcp_2")))
		drain(t, q, 2)
		must(t, q.Enqueue(ctx, Sample("rcp_1"))) // after it was sent
		drain(t, q, 1)
		if got := d.Delivered(); len(got) != 2 {
			t.Fatalf("delivered %d notices, want 2", len(got))
		}
	})
	t.Run("an invalid notice is refused and nothing is delivered", func(t *testing.T) {
		d := &Recorder{}
		q := newQueue(t, d)
		bad := Sample("rcp_1")
		bad.Facts.Vendor = ""
		if err := q.Enqueue(ctx, bad); !errors.Is(err, notices.ErrInvalid) {
			t.Fatalf("Enqueue = %v, want ErrInvalid", err)
		}
		drain(t, q, 1)
		if d.Tries() != 0 {
			t.Fatal("an invalid notice was delivered")
		}
	})
	t.Run("a failed attempt is tried again, and a delivered one is not", func(t *testing.T) {
		failing := true
		var mu sync.Mutex
		d := &Recorder{Fail: func(notices.Notice) error {
			mu.Lock()
			defer mu.Unlock()
			if failing {
				return errors.New("the mail system did not answer")
			}
			return nil
		}}
		q := newQueue(t, d)
		must(t, q.Enqueue(ctx, Sample("rcp_1")))
		drain(t, q, 1)
		if len(d.Delivered()) != 0 || d.Tries() == 0 {
			t.Fatalf("tries %d, delivered %d: want tried and not delivered", d.Tries(), len(d.Delivered()))
		}
		mu.Lock()
		failing = false
		mu.Unlock()
		drain(t, q, 3)
		if got := d.Delivered(); len(got) != 1 {
			t.Fatalf("delivered %d, want 1 once it works", len(got))
		}
	})
	t.Run("a skipped notice is never tried again", func(t *testing.T) {
		d := &Recorder{Fail: func(notices.Notice) error { return notices.ErrSkip }}
		q := newQueue(t, d)
		must(t, q.Enqueue(ctx, Sample("rcp_1")))
		drain(t, q, 1)
		tries := d.Tries()
		drain(t, q, 2)
		must(t, q.Enqueue(ctx, Sample("rcp_1")))
		drain(t, q, 1)
		if tries != 1 || d.Tries() != 1 {
			t.Fatalf("tried %d then %d times, want once", tries, d.Tries())
		}
	})
}

func drain(t *testing.T, q Subject, times int) {
	t.Helper()
	for range times {
		must(t, q.Drain(context.Background()))
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
