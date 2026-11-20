package notices_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/services/notices"
	"github.com/permaily-org/permaily/internal/services/notices/noticestest"
	"github.com/permaily-org/permaily/internal/store"
)

// cutByItsContext makes w's storage answer as DynamoDB's client does: a
// call whose context ended fails.
func cutByItsContext(w *world) {
	w.mem.Interrupt = func(c context.Context, _ store.Step, _, _ string) error { return c.Err() }
}

// The code review of ADR 0041's pull request 7, 1: the process is told to
// stop (Run's context ends) right after SES accepted a notice. The attempt
// under way is not cut: Stop waits for it, and its mark is written, so no
// enclave sends the notice again.
func TestStopKeepsTheMarkOfAnAcceptedSend(t *testing.T) {
	w := newWorld(t)
	cutByItsContext(w)
	life, signal := context.WithCancel(ctx)
	d := deliverFunc(func(c context.Context, _ notices.Notice) error {
		signal() // SIGTERM, once SES has accepted the message
		select {
		case <-c.Done(): // a cut attempt: SES's answer arrives after its end
		case <-time.After(50 * time.Millisecond): // SES's answer
		}
		return nil
	})
	q := w.enclave(d, nil)
	notices.SetEvery(q, time.Millisecond)
	n := noticestest.Sample("rcp_stop")
	w.put(q, notices.Entry{Notice: n})
	ran := make(chan struct{})
	go func() { q.Run(life); close(ran) }()
	<-life.Done()
	must(t, q.Stop(ctx))
	<-ran
	if got := w.markOf(n.Key); got != "sent" {
		t.Fatalf("the mark of the accepted send is %q, want sent", got)
	}
	if w.entry(notices.PartitionQueue, n.Key) != nil {
		t.Fatal("the sent notice is still queued, to be sent again")
	}
	if st := q.Status(); st.Sent != 1 || st.Failed != 0 {
		t.Fatalf("status = %+v", st)
	}
}

// The code review of ADR 0041's pull request 7, 1: an attempt that does
// not finish within Stop's deadline is abandoned, and Stop says the stop
// did not drain; the lease ends and another runner tries it again.
func TestStopPastItsDeadlineAbandonsTheAttemptAndSaysSo(t *testing.T) {
	w := newWorld(t)
	cutByItsContext(w)
	delivering := make(chan struct{})
	d := deliverFunc(func(c context.Context, _ notices.Notice) error {
		close(delivering)
		<-c.Done()
		return c.Err()
	})
	q := w.enclave(d, nil)
	n := noticestest.Sample("rcp_slow")
	w.put(q, notices.Entry{Notice: n})
	go func() { _ = notices.Pass(context.WithoutCancel(ctx), q) }()
	<-delivering
	c, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := q.Stop(c); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop = %v, want its deadline", err)
	}
	must(t, notices.Wait(ctx, q)) // abandoned: its context ended
	if got := w.markOf(n.Key); got != "" {
		t.Fatalf("an abandoned attempt marked %q", got)
	}
	if w.entry(notices.PartitionQueue, n.Key) == nil {
		t.Fatal("the abandoned notice is no longer queued")
	}
}

// The code review of ADR 0041's pull request 7, 1: once stopped, the queue
// admits no attempt more; what is queued meanwhile is left to the runners.
func TestStopAdmitsNoAttemptMore(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	q := w.enclave(d, nil)
	must(t, q.Stop(ctx))
	ran := make(chan struct{})
	go func() { q.Run(ctx); close(ran) }() // a runner started late ends at once
	<-ran
	n := noticestest.Sample("rcp_after")
	must(t, q.Enqueue(ctx, n))
	must(t, notices.Pass(ctx, q))
	if d.Tries() != 0 {
		t.Fatalf("a stopped queue attempted %d sends", d.Tries())
	}
	if w.entry(notices.PartitionQueue, n.Key) == nil {
		t.Fatal("the notice was not queued for the runners")
	}
}
