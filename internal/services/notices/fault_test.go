package notices_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/services/notices"
	"github.com/permaily-org/permaily/internal/services/notices/noticestest"
	"github.com/permaily-org/permaily/internal/store"
)

func TestNewNeedsItsStorageKeysAndDeliverer(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	for _, cfg := range []notices.Config{
		{Keys: w.keys, Deliverer: d}, {Entries: w.mem, Deliverer: d}, {Entries: w.mem, Keys: w.keys},
	} {
		if _, err := notices.New(cfg); !errors.Is(err, notices.ErrConfig) {
			t.Fatalf("New(%+v) = %v, want ErrConfig", cfg, err)
		}
	}
	broken := errors.New("no randomness")
	if _, err := notices.New(notices.Config{Entries: w.mem, Keys: w.keys, Deliverer: d, Random: func([]byte) error { return broken }}); !errors.Is(err, broken) {
		t.Fatalf("New with no randomness = %v", err)
	}
	// The defaults: the real clock, randomness and no log.
	q, err := notices.New(notices.Config{Entries: store.NewMemory(), Keys: w.keys, Deliverer: d})
	if err != nil {
		t.Fatal(err)
	}
	notices.Play(q, nil)
	defer stop(q)
	n := noticestest.Sample("rcp_1")
	n.At = time.Now()
	n.Facts.EndsAt = n.At.AddDate(0, 0, 90)
	must(t, q.Enqueue(ctx, n))
	must(t, notices.Wait(ctx, q))
	if len(d.Delivered()) != 1 {
		t.Fatal("the queue with its defaults did not deliver")
	}
}

func TestAKeyThatCannotBeDerivedFailsEnqueueAndPasses(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	q := w.enclave(d, nil)
	w.keys.set(nil, errors.New("the root is not there"))
	w.put(q, notices.Entry{Notice: noticestest.Sample("rcp_0")})
	if err := q.Enqueue(ctx, noticestest.Sample("rcp_1")); err == nil {
		t.Fatal("queued without a key")
	}
	must(t, q.Drain(ctx))
	w.keys.set(signKey[:16], nil)
	if err := q.Enqueue(ctx, noticestest.Sample("rcp_1")); err == nil {
		t.Fatal("queued with a key of the wrong size")
	}
	if d.Tries() != 0 || len(w.queued()) != 1 {
		t.Fatal("something was sent or queued without a key")
	}
}

func TestEnqueueFailsWhenTheStorageDoes(t *testing.T) {
	w := newWorld(t)
	q := w.enclave(&noticestest.Recorder{}, nil)
	w.fail(store.StepRead, notices.PartitionSent, 1, errors.New("read failed"))
	if err := q.Enqueue(ctx, noticestest.Sample("rcp_1")); err == nil {
		t.Fatal("queued without reading its mark")
	}
	w.fail(store.StepEntry, notices.PartitionQueue, 1, errors.New("write failed"))
	if err := q.Enqueue(ctx, noticestest.Sample("rcp_1")); err == nil {
		t.Fatal("Enqueue hid a write that failed")
	}
	if len(w.queued()) != 0 || q.Status().Queued != 0 {
		t.Fatal("a notice that was not written was counted")
	}
}

// One key is one entry: queued again while it waits, nothing more is
// written or counted.
func TestEnqueueOfAKeyQueuedIsNil(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	hold := make(chan struct{})
	q := w.enclave(d, func(context.Context, time.Time) error { <-hold; return nil })
	n := noticestest.Sample("rcp_1")
	must(t, q.Enqueue(ctx, n))
	must(t, q.Enqueue(ctx, n))
	close(hold)
	must(t, notices.Wait(ctx, q))
	if q.Status().Queued != 1 || len(d.Delivered()) != 1 {
		t.Fatalf("status %+v, delivered %d: want one", q.Status(), len(d.Delivered()))
	}
}

// Past WaitingMax first attempts waiting, a notice queued is left to the
// runners.
func TestPastWaitingMaxANoticeIsLeftToTheRunner(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	hold := make(chan struct{})
	q := w.enclave(d, func(context.Context, time.Time) error { <-hold; return nil })
	for i := range notices.WaitingMax + 1 {
		must(t, q.Enqueue(ctx, noticestest.Sample(fmt.Sprintf("rcp_%d", i))))
	}
	close(hold)
	must(t, notices.Wait(ctx, q))
	if got := len(d.Delivered()); got != notices.WaitingMax {
		t.Fatalf("delivered %d at once, want %d", got, notices.WaitingMax)
	}
	must(t, q.Drain(ctx))
	if got := len(d.Delivered()); got != notices.WaitingMax+1 {
		t.Fatalf("delivered %d, want the last one by the runner", got)
	}
}

// What waits for admission gives up when Run ends, holding nothing, and
// a pass whose context ends while it waits for a worker stops there.
func TestWaitingForAdmissionEndsWithItsContext(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	waiting := make(chan struct{}, notices.InFlightMax)
	q := w.enclave(d, func(c context.Context, _ time.Time) error {
		waiting <- struct{}{}
		<-c.Done()
		return c.Err()
	})
	for i := range notices.InFlightMax + 2 {
		must(t, q.Enqueue(ctx, noticestest.Sample(fmt.Sprintf("rcp_%d", i))))
	}
	for range notices.InFlightMax {
		<-waiting
	}
	c, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := notices.Pass(c, q); err == nil {
		t.Fatal("a pass that could not be admitted said nothing")
	}
	c2, cancel2 := context.WithCancel(ctx)
	cancel2()
	if err := q.Drain(c2); err == nil {
		t.Fatal("Drain did not end with its context")
	}
	stop(q)
	if d.Tries() != 0 || len(w.queued()) != notices.InFlightMax+2 {
		t.Fatalf("tries %d, queued %d: want nothing sent and every notice left", d.Tries(), len(w.queued()))
	}
	for _, e := range w.queued() {
		if got, ok := notices.Open(signKey, &e); !ok || !got.LeaseUntil.IsZero() {
			t.Fatalf("an attempt that gave up left %+v", got)
		}
	}
}

// Every step of an attempt that fails leaves the notice to be tried
// again, sends nothing it should not, and says only which step failed.
func TestAnAttemptWhoseStorageFailsLeavesItsNotice(t *testing.T) {
	type step struct {
		name      string
		step      store.Step
		partition string
		nth       int
		answer    error
	}
	for _, s := range []step{
		{name: "the entry cannot be read", step: store.StepRead, partition: notices.PartitionQueue, nth: 1},
		{name: "the lease cannot be written", step: store.StepEntry, partition: notices.PartitionQueue, nth: 1},
		{name: "the mark cannot be read", step: store.StepRead, partition: notices.PartitionSent, nth: 1},
		{name: "a failure cannot be written", step: store.StepEntry, partition: notices.PartitionQueue, nth: 2, answer: errMail},
	} {
		t.Run(s.name, func(t *testing.T) {
			w := newWorld(t)
			d := &noticestest.Recorder{Fail: func(notices.Notice) error { return s.answer }}
			hold := make(chan struct{})
			q := w.enclave(d, func(context.Context, time.Time) error { <-hold; return nil })
			n := noticestest.Sample("rcp_1")
			must(t, q.Enqueue(ctx, n))
			w.fail(s.step, s.partition, s.nth, fmt.Errorf("the storage failed for %s", n.Key))
			close(hold)
			must(t, notices.Wait(ctx, q))
			if len(d.Delivered()) != 0 || w.entry(notices.PartitionQueue, n.Key) == nil {
				t.Fatal("want nothing sent and the entry left")
			}
			d.Fail = nil
			w.clock.Advance(notices.LeaseFor)
			must(t, q.Drain(ctx))
			if len(d.Delivered()) != 1 {
				t.Fatalf("delivered %d once the storage works, want 1", len(d.Delivered()))
			}
			if !strings.Contains(w.logs.String(), `"step":`) || strings.Contains(w.logs.String(), n.Key) {
				t.Fatalf("the log says no step, or names the key:\n%s", w.logs.String())
			}
		})
	}
}

// A first attempt admitted after its notice's window is a drop, as the
// runner's is.
func TestAnAttemptPastTheWindowDropsTheNotice(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	hold := make(chan struct{})
	q := w.enclave(d, func(context.Context, time.Time) error { <-hold; return nil })
	n := noticestest.Sample("rcp_1")
	must(t, q.Enqueue(ctx, n))
	w.clock.Advance(notices.RetryFor)
	close(hold)
	must(t, notices.Wait(ctx, q))
	if d.Tries() != 0 || q.Status().Dropped != 1 || w.markOf(n.Key) != "dropped" {
		t.Fatalf("status %+v: want it dropped unsent", q.Status())
	}
}

// A drop whose storage fails leaves the entry for the next pass, which
// drops it once.
func TestADropWhoseStorageFailsIsMadeAgain(t *testing.T) {
	for _, f := range []struct {
		step      store.Step
		partition string
	}{
		{store.StepRead, notices.PartitionSent},
		{store.StepEntry, notices.PartitionSent},
		{store.StepDeleteEntry, notices.PartitionQueue},
	} {
		t.Run(string(f.step)+" "+f.partition, func(t *testing.T) {
			w := newWorld(t)
			q := w.enclave(&noticestest.Recorder{}, nil)
			n := noticestest.Sample("rcp_1")
			w.put(q, notices.Entry{Notice: n})
			w.clock.Advance(notices.RetryFor)
			w.fail(f.step, f.partition, 1, errors.New("failed"))
			must(t, notices.Pass(ctx, q))
			marked := w.markOf(n.Key) == "dropped"
			if w.entry(notices.PartitionQueue, n.Key) == nil || q.Status().Dropped != map[bool]int64{false: 0, true: 1}[marked] {
				t.Fatalf("status %+v, marked %v: a drop is counted when, and only when, its mark is written", q.Status(), marked)
			}
			must(t, notices.Pass(ctx, q))
			if w.entry(notices.PartitionQueue, n.Key) != nil || q.Status().Dropped != 1 || w.markOf(n.Key) != "dropped" {
				t.Fatalf("status %+v: want it dropped once", q.Status())
			}
		})
	}
}

func TestAPassWhoseListingFailsTriesAgainNextTime(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	q := w.enclave(d, nil)
	w.put(q, notices.Entry{Notice: noticestest.Sample("rcp_1")})
	w.fail(store.StepRead, notices.PartitionQueue, 1, errors.New("list failed"))
	must(t, notices.Pass(ctx, q))
	if d.Tries() != 0 {
		t.Fatal("sent without a listing")
	}
	must(t, notices.Pass(ctx, q))
	if len(d.Delivered()) != 1 {
		t.Fatal("not sent once the listing works")
	}
}

// Run makes a pass every period until its context ends.
func TestRunPassesEveryPeriod(t *testing.T) {
	w := newWorld(t)
	sent := make(chan struct{}, 1)
	d := deliverFunc(func(context.Context, notices.Notice) error { sent <- struct{}{}; return nil })
	q := w.enclave(d, nil)
	notices.SetEvery(q, 5*time.Millisecond)
	w.put(q, notices.Entry{Notice: noticestest.Sample("rcp_1")})
	c, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(c); close(done) }()
	<-sent
	cancel()
	<-done
}

// The real rate waits on the real clock, and ends with its context.
func TestTheRealRateWaits(t *testing.T) {
	must(t, notices.Unpaced(ctx))
	must(t, notices.Sleep(ctx, time.Millisecond))
	c, cancel := context.WithCancel(ctx)
	cancel()
	if err := notices.Sleep(c, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sleep = %v, want its context's end", err)
	}
}

// E28: the log carries the kind and the outcome alone: never an account,
// a key, an address, a vendor or a word of an error.
func TestE28TheLogHoldsNothingOfAnyone(t *testing.T) {
	w := newWorld(t)
	answers := map[string]error{"rcp_fail": errMail, "rcp_skip": fmt.Errorf("acc_ana is closing: %w", notices.ErrSkip), "rcp_invalid": fmt.Errorf("no words for it: %w", notices.ErrInvalid)}
	d := &noticestest.Recorder{Fail: func(n notices.Notice) error {
		for id, err := range answers {
			if n.Key == noticestest.Sample(id).Key {
				return err
			}
		}
		return nil
	}}
	q := w.enclave(d, nil)
	var keys []string
	for _, id := range []string{"rcp_sent", "rcp_fail", "rcp_skip", "rcp_invalid"} {
		n := noticestest.Sample(id)
		keys = append(keys, n.Key)
		must(t, q.Enqueue(ctx, n))
	}
	must(t, notices.Wait(ctx, q))
	w.mem.SetEntry(store.Entry{Partition: notices.PartitionQueue, Key: noticestest.Sample("rcp_hand").Key, Value: []byte(`{"doc":{"account":"acc_ana"},"mac":""}`), Version: 1})
	w.fail(store.StepRead, notices.PartitionSent, 1, fmt.Errorf("the storage failed for %s", keys[1]))
	w.clock.Advance(notices.RetryFor)
	must(t, q.Drain(ctx))
	must(t, q.Drain(ctx))
	logs := w.logs.String()
	for _, want := range []string{`"kind":"connection_approved"`, `"outcome":"queued"`, `"outcome":"sent"`, `"outcome":"failed"`, `"outcome":"skipped"`, `"outcome":"refused"`, `"outcome":"dropped"`, `"step":"read"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("the log does not say %s:\n%s", want, logs)
		}
	}
	secrets := append([]string{"acc_ana", "ana@example.org", "example.org", "claude.ai", "mcp", "did not answer", "closing", "no words", "storage failed", noticestest.Sample("rcp_hand").Key}, keys...)
	for _, s := range secrets {
		if strings.Contains(logs, s) {
			t.Errorf("the log holds %q:\n%s", s, logs)
		}
	}
	if st := q.Status(); st.Sent != 1 || st.Skipped != 1 || st.Refused != 2 || st.Dropped != 1 || st.Failed < 1 {
		t.Fatalf("status %+v", st)
	}
}
