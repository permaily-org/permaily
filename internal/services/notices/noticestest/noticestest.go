// Package noticestest is the fake queue of notices the callers are built
// against (ADR 0041: the connections service, the receipts' chain and
// accounts), a Deliverer that records what it is given, a played clock,
// the contract the fake and the real queue both keep (Contract) and the
// contract every Deliverer keeps (DelivererContract). Tests only.
package noticestest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/notices"
)

// Fake is a Queue in memory: each Key once, delivered only by Drain, one
// attempt each, tried again on the next Drain after an error that is not
// notices.ErrSkip or notices.ErrInvalid, while the notice is Sendable. Fail,
// when set, is what Enqueue answers, storing nothing (a storage that cannot
// be written). Now is its clock (time.Now when nil): Enqueue refuses a
// notice that is not Fresh by it, as the real queue does.
type Fake struct {
	Deliverer notices.Deliverer
	Fail      error
	Now       func() time.Time

	mu     sync.Mutex
	queued []notices.Notice
	keys   map[string]bool
	done   map[string]bool
	trying map[string]bool // the attempts under way: the fake's lease
}

var _ notices.Queue = (*Fake)(nil)

// Enqueue implements notices.Queue.
func (f *Fake) Enqueue(_ context.Context, n notices.Notice) error {
	if err := n.Check(); err != nil {
		return err
	}
	if !n.Fresh(f.now()) {
		return notices.ErrInvalid
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return f.Fail
	}
	if f.keys == nil {
		f.keys, f.done, f.trying = map[string]bool{}, map[string]bool{}, map[string]bool{}
	}
	if f.keys[n.Key] {
		return nil
	}
	f.keys[n.Key] = true
	f.queued = append(f.queued, n)
	return nil
}

func (f *Fake) now() time.Time {
	if f.Now == nil {
		return time.Now()
	}
	return f.Now()
}

// Queued returns every notice queued, in order, each Key once.
func (f *Fake) Queued() []notices.Notice {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]notices.Notice(nil), f.queued...)
}

// Drain makes one attempt of every notice not yet delivered, skipped,
// refused or dropped and that no other Drain is trying, each within
// notices.AttemptFor; one no longer Sendable is dropped, not tried.
func (f *Fake) Drain(ctx context.Context) error {
	f.mu.Lock()
	var due []notices.Notice
	now := f.now()
	for _, n := range f.queued {
		switch {
		case f.done[n.Key], f.trying[n.Key]:
		case !n.Sendable(now):
			f.done[n.Key] = true
		default:
			f.trying[n.Key] = true
			due = append(due, n)
		}
	}
	f.mu.Unlock()
	for _, n := range due {
		actx, cancel := context.WithTimeout(ctx, notices.AttemptFor)
		err := f.Deliverer.Deliver(actx, n)
		cancel()
		f.mu.Lock()
		if err == nil || errors.Is(err, notices.ErrSkip) || errors.Is(err, notices.ErrInvalid) {
			f.done[n.Key] = true
		}
		delete(f.trying, n.Key)
		f.mu.Unlock()
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
// lease no attempt holds and that is Sendable, whatever its wait between
// attempts.
type Subject interface {
	notices.Queue
	Drain(ctx context.Context) error
}

// Clock is a played clock, safe for concurrent use.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock is a clock at now.
func NewClock(now time.Time) *Clock { return &Clock{now: now} }

// Now is the clock's time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock on by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// SampleAt is the time of Sample's notices.
var SampleAt = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

// Sample is a valid notice of an approval at SampleAt, its key made from
// id.
func Sample(id string) notices.Notice {
	return notices.Notice{
		Account: "acc_ana", Kind: notices.KindConnectionApproved, Key: notices.Key("acc_ana", notices.KindConnectionApproved, id), At: SampleAt,
		Facts: notices.Facts{Vendor: "claude.ai", Resource: contracts.ResourceMCP, Parts: []contracts.Part{contracts.PartSender, contracts.PartSubject}, EndsAt: SampleAt.AddDate(0, 0, 90)},
	}
}

// Enclave is what one enclave's queue is made with under the contract:
// the Deliverer it sends through and, when set, Admit, which every attempt
// of that enclave calls once the queue admitted it to its workers and its
// rate and before it reads its entry, and which may block as a wait for
// the rate would (the attempt holds nothing of a notice while it waits).
// A queue with no attempts of its own outside Drain (the fake) need not
// call it.
type Enclave struct {
	Deliverer notices.Deliverer
	Admit     func(ctx context.Context) error
}

// Contract runs what every queue keeps (ADR 0041, E20 to E23 in their
// part a played clock can show, and the re-review's N2). newQueues makes
// two enclaves' queues over one fresh storage, a and b, both reading
// clock, which starts at SampleAt; a queue that is one enclave's alone
// (the fake) returns itself twice, made with a. ADR 0041's pull request 1
// runs it on the real queue, two enclaves sharing one store.
func Contract(t *testing.T, newQueues func(t *testing.T, a, b Enclave, clock *Clock) (qa, qb Subject)) {
	ctx := context.Background()
	newQueue := func(t *testing.T, d notices.Deliverer) (Subject, *Clock) {
		clock := NewClock(SampleAt)
		a, _ := newQueues(t, Enclave{Deliverer: d}, Enclave{Deliverer: d}, clock)
		return a, clock
	}
	t.Run("a notice is delivered once", func(t *testing.T) {
		d := &Recorder{}
		q, _ := newQueue(t, d)
		must(t, q.Enqueue(ctx, Sample("rcp_1")))
		drain(t, q, 2)
		if got := d.Delivered(); len(got) != 1 || got[0].Key != Sample("rcp_1").Key {
			t.Fatalf("delivered %v, want the one notice once", got)
		}
	})
	t.Run("one key is one notice", func(t *testing.T) {
		d := &Recorder{}
		q, _ := newQueue(t, d)
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
		q, _ := newQueue(t, d)
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
	t.Run("a notice whose time is not the queue's is refused", func(t *testing.T) {
		d := &Recorder{}
		q, clock := newQueue(t, d)
		clock.Advance(notices.AtSkew + time.Second)
		if err := q.Enqueue(ctx, Sample("rcp_1")); !errors.Is(err, notices.ErrInvalid) {
			t.Fatalf("Enqueue of a notice older than AtSkew = %v, want ErrInvalid", err)
		}
		clock.Advance(-2 * (notices.AtSkew + time.Second))
		if err := q.Enqueue(ctx, Sample("rcp_2")); !errors.Is(err, notices.ErrInvalid) {
			t.Fatalf("Enqueue of a notice from the future = %v, want ErrInvalid", err)
		}
		drain(t, q, 1)
		if d.Tries() != 0 {
			t.Fatal("a notice not of now was delivered")
		}
	})
	t.Run("a failed attempt is tried again, and a delivered one is not", func(t *testing.T) {
		d, set := failing(errors.New("the mail system did not answer"))
		q, _ := newQueue(t, d)
		must(t, q.Enqueue(ctx, Sample("rcp_1")))
		drain(t, q, 1)
		if len(d.Delivered()) != 0 || d.Tries() == 0 {
			t.Fatalf("tries %d, delivered %d: want tried and not delivered", d.Tries(), len(d.Delivered()))
		}
		set(nil)
		drain(t, q, 3)
		if got := d.Delivered(); len(got) != 1 {
			t.Fatalf("delivered %d, want 1 once it works", len(got))
		}
	})
	t.Run("an account that cannot be read is tried again and told once when it can", func(t *testing.T) {
		d, set := failing(errors.New("the account's record cannot be read"))
		q, _ := newQueue(t, d)
		must(t, q.Enqueue(ctx, Sample("rcp_1")))
		drain(t, q, 2)
		set(nil)
		drain(t, q, 3)
		if got := d.Delivered(); len(got) != 1 || d.Tries() < 3 {
			t.Fatalf("tries %d, delivered %d: want tried while unreadable and delivered once", d.Tries(), len(got))
		}
	})
	t.Run("a skipped notice is never tried again", func(t *testing.T) {
		d := &Recorder{Fail: func(notices.Notice) error { return notices.ErrSkip }}
		q, _ := newQueue(t, d)
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
	t.Run("a notice the Deliverer cannot word is never tried again", func(t *testing.T) {
		d := &Recorder{Fail: func(notices.Notice) error { return notices.ErrInvalid }}
		q, _ := newQueue(t, d)
		must(t, q.Enqueue(ctx, Sample("rcp_1")))
		drain(t, q, 3)
		if d.Tries() != 1 || len(d.Delivered()) != 0 {
			t.Fatalf("tried %d times, delivered %d: want once and never", d.Tries(), len(d.Delivered()))
		}
	})
	t.Run("a notice not sent within RetryFor of its time is dropped, never sent", func(t *testing.T) {
		d, set := failing(errors.New("the mail system did not answer"))
		q, clock := newQueue(t, d)
		must(t, q.Enqueue(ctx, Sample("rcp_1")))
		drain(t, q, 1)
		clock.Advance(notices.RetryFor)
		set(nil)
		drain(t, q, 2)
		if len(d.Delivered()) != 0 {
			t.Fatal("a notice past its window was sent")
		}
	})
	t.Run("two enclaves draining a backlog past the lease send each notice once", func(t *testing.T) {
		// N2: the schedule is forced, not hoped for. A's attempts are held
		// at admission while the clock passes LeaseFor and B drains the
		// same storage; then A is let go. A queue that took a lease when it
		// queued, and sent on it once admitted, sends twice here.
		d := &Recorder{}
		da, db := &Bounded{Deliverer: d}, &Bounded{Deliverer: d}
		hold := make(chan struct{})
		admitA := func(ctx context.Context) error {
			select {
			case <-hold:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		clock := NewClock(SampleAt)
		a, b := newQueues(t, Enclave{Deliverer: da, Admit: admitA}, Enclave{Deliverer: db}, clock)
		n := 2*notices.InFlightMax + 1
		for i := range n {
			must(t, a.Enqueue(ctx, Sample(fmt.Sprintf("rcp_%d", i))))
		}
		clock.Advance(notices.LeaseFor + notices.RetryMax)
		drain(t, b, 2)
		close(hold)
		for range 2 {
			var wg sync.WaitGroup
			wg.Go(func() { must(t, a.Drain(ctx)) })
			wg.Go(func() { must(t, b.Drain(ctx)) })
			wg.Wait()
		}
		seen := map[string]int{}
		for _, got := range d.Delivered() {
			seen[got.Key]++
		}
		for i := range n {
			if k := Sample(fmt.Sprintf("rcp_%d", i)).Key; seen[k] != 1 {
				t.Fatalf("notice %d delivered %d times, want once", i, seen[k])
			}
		}
		for name, e := range map[string]*Bounded{"a": da, "b": db} {
			if err := e.Err(); err != nil {
				t.Fatalf("enclave %s: %v", name, err)
			}
		}
	})
}

// Bounded is a Deliverer that passes each notice to Deliverer and checks
// what the queue keeps of every attempt: a deadline at most
// notices.AttemptFor away, and at most notices.InFlightMax attempts at
// once. Err says what it found broken, nil when nothing.
type Bounded struct {
	Deliverer notices.Deliverer

	mu       sync.Mutex
	inFlight int
	most     int
	err      error
}

// Deliver implements notices.Deliverer.
func (b *Bounded) Deliver(ctx context.Context, n notices.Notice) error {
	b.mu.Lock()
	b.inFlight++
	b.most = max(b.most, b.inFlight)
	deadline, ok := ctx.Deadline()
	switch {
	case b.inFlight > notices.InFlightMax:
		b.err = fmt.Errorf("%d attempts at once, more than %d", b.inFlight, notices.InFlightMax)
	case !ok:
		b.err = errors.New("an attempt had no deadline")
	case time.Until(deadline) > notices.AttemptFor:
		b.err = fmt.Errorf("an attempt's deadline was %v away, more than %v", time.Until(deadline), notices.AttemptFor)
	}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.inFlight--
		b.mu.Unlock()
	}()
	return b.Deliverer.Deliver(ctx, n)
}

// Most is the most attempts it saw at once.
func (b *Bounded) Most() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.most
}

// Err is what it found broken, nil when nothing.
func (b *Bounded) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

// failing is a Recorder that answers err until set changes it (nil: it
// delivers).
func failing(err error) (*Recorder, func(error)) {
	var mu sync.Mutex
	d := &Recorder{Fail: func(notices.Notice) error {
		mu.Lock()
		defer mu.Unlock()
		return err
	}}
	return d, func(e error) {
		mu.Lock()
		defer mu.Unlock()
		err = e
	}
}

// Standing is an account as a Deliverer finds it, which each
// implementation plays in its own storage (accounts, ADR 0041's pull
// request 2).
type Standing int

const (
	// StandingOpen and StandingNotAgreed are told.
	StandingOpen Standing = iota
	StandingNotAgreed
	// StandingGone, StandingClosing and StandingBlocked are read and
	// found: ErrSkip.
	StandingGone
	StandingClosing
	StandingBlocked
	// The rest are not known: an error tried again, nothing sent.
	StandingUnreadable // the record cannot be read (the storage fails)
	StandingTimeout    // the read does not end before the attempt's deadline
	StandingRefused    // the storage refuses the read (its authentication fails)
	StandingLost       // the record's strict history says it was lost
	StandingForked     // the record's strict history says it was written twice
	StandingMalformed  // the record does not open as an account's
	StandingUnknown    // a standing the Deliverer does not know
	standingCount      // bounds Standings
)

// Standings is every standing, in order.
func Standings() []Standing {
	out := make([]Standing, standingCount)
	for i := range out {
		out[i] = Standing(i)
	}
	return out
}

// DelivererSubject is a Deliverer under the contract, over an account
// whose standing Set plays, and Sent, the messages it handed the mail
// system.
type DelivererSubject struct {
	Deliverer notices.Deliverer
	Set       func(Standing)
	Sent      func() int
}

// DelivererContract runs what every Deliverer keeps (ADR 0041, E25 in its
// part every Deliverer shares, and "Changed after the design review", 1
// and 6): newDeliverer makes one over a fresh account of Sample's, open.
func DelivererContract(t *testing.T, newDeliverer func(t *testing.T) DelivererSubject) {
	ctx := context.Background()
	for _, s := range Standings() {
		t.Run(fmt.Sprintf("standing %d", s), func(t *testing.T) {
			d := newDeliverer(t)
			d.Set(s)
			err := d.Deliverer.Deliver(ctx, Sample("rcp_1"))
			switch s {
			case StandingOpen, StandingNotAgreed:
				if err != nil || d.Sent() != 1 {
					t.Fatalf("Deliver = %v, sent %d: want told once", err, d.Sent())
				}
			case StandingGone, StandingClosing, StandingBlocked:
				if !errors.Is(err, notices.ErrSkip) || d.Sent() != 0 {
					t.Fatalf("Deliver = %v, sent %d: want ErrSkip and nothing sent", err, d.Sent())
				}
			default:
				if err == nil || errors.Is(err, notices.ErrSkip) || errors.Is(err, notices.ErrInvalid) || d.Sent() != 0 {
					t.Fatalf("Deliver = %v, sent %d: want an error tried again and nothing sent", err, d.Sent())
				}
			}
		})
	}
	t.Run("an account that cannot be read, then can, is told once", func(t *testing.T) {
		d := newDeliverer(t)
		d.Set(StandingUnreadable)
		if err := d.Deliverer.Deliver(ctx, Sample("rcp_1")); err == nil || errors.Is(err, notices.ErrSkip) {
			t.Fatalf("Deliver while unreadable = %v, want an error tried again", err)
		}
		d.Set(StandingOpen)
		if err := d.Deliverer.Deliver(ctx, Sample("rcp_1")); err != nil || d.Sent() != 1 {
			t.Fatalf("Deliver once readable = %v, sent %d: want told once", err, d.Sent())
		}
	})
	t.Run("a notice Check refuses is ErrInvalid, never worded", func(t *testing.T) {
		d := newDeliverer(t)
		for _, change := range []func(*notices.Notice){
			func(n *notices.Notice) { n.Kind = "webhook_failing" },
			func(n *notices.Notice) { n.Facts.Parts = []contracts.Part{"sender", "everything"} },
			func(n *notices.Notice) { n.Facts.Resource = "smtp" },
			func(n *notices.Notice) { n.Facts.By = "<a href=x>" },
			func(n *notices.Notice) { n.Facts.Vendor = "permaily-security.example/login" },
		} {
			n := Sample("rcp_1")
			change(&n)
			if err := d.Deliverer.Deliver(ctx, n); !errors.Is(err, notices.ErrInvalid) {
				t.Errorf("Deliver(%+v) = %v, want ErrInvalid", n, err)
			}
		}
		if d.Sent() != 0 {
			t.Fatalf("sent %d, want nothing", d.Sent())
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
