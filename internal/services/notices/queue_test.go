package notices_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/services/notices"
	"github.com/permaily-org/permaily/internal/services/notices/noticestest"
	"github.com/permaily-org/permaily/internal/store"
)

// E20: the real queue keeps the contract, as two enclaves over one store
// and a played clock.
func TestE20TheQueueKeepsTheContract(t *testing.T) {
	noticestest.Contract(t, func(t *testing.T, a, b noticestest.Enclave, clock *noticestest.Clock) (qa, qb noticestest.Subject) {
		w := newWorld(t)
		w.clock = clock
		w.mem.Now = clock.Now
		played := func(e noticestest.Enclave) func(context.Context, time.Time) error {
			return func(ctx context.Context, _ time.Time) error {
				if e.Admit == nil {
					return nil
				}
				return e.Admit(ctx)
			}
		}
		return w.enclave(a.Deliverer, played(a)), w.enclave(b.Deliverer, played(b))
	})
}

// E21: the first attempt is made at once in the enclave that queued,
// through the same admission as the runner's, and delivers without a pass.
func TestE21TheFirstAttemptIsAdmittedInTheEnclaveThatQueued(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	var admitted atomic.Int32
	q := w.enclave(d, func(context.Context, time.Time) error { admitted.Add(1); return nil })
	n := noticestest.Sample("rcp_1")
	must(t, q.Enqueue(ctx, n))
	must(t, notices.Wait(ctx, q))
	if got := d.Delivered(); len(got) != 1 || got[0].Key != n.Key || admitted.Load() != 1 {
		t.Fatalf("delivered %v after %d admissions, want the notice once after one", got, admitted.Load())
	}
	if st := q.Status(); st.Queued != 1 || st.Sent != 1 || !st.Since.Equal(noticestest.SampleAt) {
		t.Fatalf("status %+v, want one queued and sent since the start", st)
	}
	if w.entry(notices.PartitionQueue, n.Key) != nil || w.markOf(n.Key) != "sent" {
		t.Fatal("a notice sent left its entry, or no mark")
	}
}

// E21: an entry holds no lease until its attempt is admitted.
func TestE21AnEntryHoldsNoLeaseUntilItsAttemptIsAdmitted(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	hold, waiting := make(chan struct{}), make(chan struct{}, 1)
	q := w.enclave(d, func(context.Context, time.Time) error { waiting <- struct{}{}; <-hold; return nil })
	n := noticestest.Sample("rcp_1")
	must(t, q.Enqueue(ctx, n))
	<-waiting
	got, ok := notices.Open(signKey, w.entry(notices.PartitionQueue, n.Key))
	if !ok || !got.LeaseUntil.IsZero() || got.By != "" || got.Attempts != 0 {
		t.Fatalf("entry while waiting for admission %+v (%v), want no lease", got, ok)
	}
	close(hold)
	must(t, notices.Wait(ctx, q))
	if len(d.Delivered()) != 1 {
		t.Fatal("not delivered once admitted")
	}
}

// E21: a failure is left for the runner, which tries again after
// RetryFirst, doubling to RetryMax, and only once due.
func TestE21AFailureIsTriedAgainAfterRetryFirstDoublingToRetryMax(t *testing.T) {
	for i, want := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour, time.Hour} {
		if got := notices.Backoff(i + 1); got != want {
			t.Fatalf("Backoff(%d) = %v, want %v", i+1, got, want)
		}
	}
	w := newWorld(t)
	d := &noticestest.Recorder{Fail: func(notices.Notice) error { return errMail }}
	q := w.enclave(d, nil)
	n := noticestest.Sample("rcp_1")
	must(t, q.Enqueue(ctx, n))
	must(t, notices.Wait(ctx, q))
	for attempt := 1; attempt <= 8; attempt++ {
		if d.Tries() != attempt {
			t.Fatalf("tries %d, want %d", d.Tries(), attempt)
		}
		e, ok := notices.Open(signKey, w.entry(notices.PartitionQueue, n.Key))
		wait := notices.Backoff(attempt)
		if !ok || e.Attempts != attempt || !e.NextAt.Equal(w.clock.Now().Add(wait)) || !e.LeaseUntil.IsZero() {
			t.Fatalf("after attempt %d the entry is %+v, want the next due in %v and no lease", attempt, e, wait)
		}
		w.clock.Advance(wait - time.Second)
		must(t, notices.Pass(ctx, q))
		if d.Tries() != attempt {
			t.Fatalf("tried before it was due (attempt %d)", attempt)
		}
		w.clock.Advance(time.Second)
		must(t, notices.Pass(ctx, q))
	}
	if st := q.Status(); st.Failed != 9 || st.Sent != 0 {
		t.Fatalf("status %+v, want nine failed", st)
	}
}

// E21: the other enclave's runner skips a lease held and takes one that
// ended.
func TestE21TheOtherEnclaveSkipsAHeldLeaseAndTakesAnEndedOne(t *testing.T) {
	w := newWorld(t)
	g := newGate(errMail)
	a := w.enclave(g, nil)
	db := &noticestest.Recorder{}
	b := w.enclave(db, nil)
	n := noticestest.Sample("rcp_1")
	must(t, a.Enqueue(ctx, n))
	<-g.started
	must(t, b.Drain(ctx))
	w.clock.Advance(notices.LeaseFor - time.Second)
	must(t, b.Drain(ctx))
	if db.Tries() != 0 {
		t.Fatal("the other enclave took a lease that was held")
	}
	w.clock.Advance(time.Second)
	must(t, b.Drain(ctx))
	close(g.open)
	must(t, notices.Wait(ctx, a))
	if got := db.Delivered(); len(got) != 1 || len(g.d.Delivered()) != 0 {
		t.Fatalf("b delivered %d and a %d, want b once", len(got), len(g.d.Delivered()))
	}
	if w.entry(notices.PartitionQueue, n.Key) != nil || w.markOf(n.Key) != "sent" {
		t.Fatal("the late failure of the first lease's holder undid the send")
	}
}

// E21: an attempt whose AttemptFor passes sends nothing; it is a failure
// tried again.
func TestE21AnAttemptPastItsDeadlineSendsNothing(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	slow := deliverFunc(func(c context.Context, n notices.Notice) error {
		deadline, ok := c.Deadline()
		if !ok || time.Until(deadline) > 50*time.Millisecond {
			t.Errorf("the attempt's deadline is %v (%v), want within its AttemptFor", deadline, ok)
		}
		<-c.Done()
		return c.Err() // the message was not handed over
	})
	q := w.enclave(slow, nil)
	notices.SetAttemptFor(q, 50*time.Millisecond)
	n := noticestest.Sample("rcp_1")
	must(t, q.Enqueue(ctx, n))
	must(t, notices.Wait(ctx, q))
	e, ok := notices.Open(signKey, w.entry(notices.PartitionQueue, n.Key))
	if st := q.Status(); st.Failed != 1 || st.Sent != 0 || !ok || e.Attempts != 1 || len(d.Delivered()) != 0 {
		t.Fatalf("status %+v, entry %+v: want one failure, tried again", st, e)
	}
}

type deliverFunc func(context.Context, notices.Notice) error

func (f deliverFunc) Deliver(c context.Context, n notices.Notice) error { return f(c, n) }

// E21: an attempt whose entry changed between its read and its lease
// sends nothing.
func TestE21AnAttemptWhoseEntryChangedUnderItSendsNothing(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	hold := make(chan struct{})
	q := w.enclave(d, func(context.Context, time.Time) error { <-hold; return nil })
	n := noticestest.Sample("rcp_1")
	must(t, q.Enqueue(ctx, n))
	w.at(store.StepEntry, notices.PartitionQueue, 1, func() {
		e := w.entry(notices.PartitionQueue, n.Key)
		e.Version += 5 // the other enclave wrote it meanwhile
		w.mem.SetEntry(*e)
	})
	close(hold)
	must(t, notices.Wait(ctx, q))
	if st := q.Status(); d.Tries() != 0 || st.Failed != 0 || st.Sent != 0 {
		t.Fatalf("tries %d, status %+v: want nothing sent and nothing counted", d.Tries(), st)
	}
}

// E22: a crash after Deliver returned and before the mark is sent again
// once the lease ends (at least once).
func TestE22ACrashBeforeTheMarkIsSentAgainAfterTheLease(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	q := w.enclave(d, nil)
	n := noticestest.Sample("rcp_1")
	w.fail(store.StepEntry, notices.PartitionSent, 1, errors.New("crashed"))
	must(t, q.Enqueue(ctx, n))
	must(t, notices.Wait(ctx, q))
	must(t, q.Drain(ctx))
	if len(d.Delivered()) != 1 || w.entry(notices.PartitionQueue, n.Key) == nil {
		t.Fatalf("delivered %d, want once and the entry left under its lease", len(d.Delivered()))
	}
	w.clock.Advance(notices.LeaseFor)
	must(t, q.Drain(ctx))
	if len(d.Delivered()) != 2 || w.entry(notices.PartitionQueue, n.Key) != nil || w.markOf(n.Key) != "sent" {
		t.Fatalf("delivered %d, want twice, then marked and deleted", len(d.Delivered()))
	}
}

// E22: a crash after the mark and before the entry's deletion is not sent
// again; the entry is deleted.
func TestE22ACrashAfterTheMarkIsNotSentAgain(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	q := w.enclave(d, nil)
	n := noticestest.Sample("rcp_1")
	w.fail(store.StepDeleteEntry, notices.PartitionQueue, 1, errors.New("crashed"))
	must(t, q.Enqueue(ctx, n))
	must(t, notices.Wait(ctx, q))
	if w.entry(notices.PartitionQueue, n.Key) == nil || w.markOf(n.Key) != "sent" {
		t.Fatal("want the mark written and the entry left")
	}
	w.clock.Advance(notices.LeaseFor)
	w.fail(store.StepDeleteEntry, notices.PartitionQueue, 1, errors.New("crashed again"))
	must(t, q.Drain(ctx))
	w.clock.Advance(notices.LeaseFor)
	must(t, q.Drain(ctx))
	if len(d.Delivered()) != 1 || w.entry(notices.PartitionQueue, n.Key) != nil {
		t.Fatalf("delivered %d, want once and the entry deleted", len(d.Delivered()))
	}
	// And one past its window whose mark holds is deleted, not counted.
	m := noticestest.Sample("rcp_2")
	w.fail(store.StepDeleteEntry, notices.PartitionQueue, 1, errors.New("crashed"))
	w.clock.Advance(-2 * notices.LeaseFor)
	must(t, q.Enqueue(ctx, m))
	must(t, notices.Wait(ctx, q))
	w.clock.Advance(notices.RetryFor)
	must(t, notices.Pass(ctx, q))
	if w.entry(notices.PartitionQueue, m.Key) != nil || q.Status().Dropped != 0 || len(d.Delivered()) != 2 {
		t.Fatalf("status %+v: want the sent notice's entry deleted and nothing dropped", q.Status())
	}
}

// E23: after RetryFor a notice is dropped, marked, counted, and not queued
// again by the same key.
func TestE23AfterRetryForANoticeIsDroppedAndNotQueuedAgain(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{Fail: func(notices.Notice) error { return errMail }}
	q := w.enclave(d, nil)
	n := noticestest.Sample("rcp_1")
	must(t, q.Enqueue(ctx, n))
	must(t, notices.Wait(ctx, q))
	w.clock.Advance(notices.RetryFor - time.Second)
	must(t, q.Drain(ctx))
	tries := d.Tries()
	w.clock.Advance(time.Second)
	must(t, notices.Pass(ctx, q))
	if st := q.Status(); st.Dropped != 1 || d.Tries() != tries || w.markOf(n.Key) != "dropped" || w.entry(notices.PartitionQueue, n.Key) != nil {
		t.Fatalf("status %+v, mark %q: want dropped, marked and deleted, not tried", st, w.markOf(n.Key))
	}
	again := sampleNow(w, "rcp_1")
	must(t, q.Enqueue(ctx, again))
	must(t, notices.Wait(ctx, q))
	if st := q.Status(); st.Queued != 1 || d.Tries() != tries || len(w.queued()) != 0 {
		t.Fatalf("status %+v: the same event was queued again", st)
	}
	if !strings.Contains(w.logs.String(), `"outcome":"dropped"`) {
		t.Fatal("the drop was not logged")
	}
}

// E24: entries moved, edited, written by hand, past EntryMax, signed over
// a notice Check refuses or under another key are refused, counted once
// by each enclave, and never sent.
func TestE24EntriesThatDoNotVerifyAreRefusedAndNeverSent(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	q := w.enclave(d, nil)
	n := noticestest.Sample("rcp_1")
	w.put(q, notices.Entry{Notice: n})
	good := *w.entry(notices.PartitionQueue, n.Key)
	other := noticestest.Sample("rcp_2")
	moved := good
	moved.Key = other.Key
	edited := good
	edited.Value = []byte(strings.Replace(string(good.Value), "claude.ai", "claude.ia", 1))
	edited.Key = noticestest.Sample("rcp_3").Key
	byHand := store.Entry{Partition: notices.PartitionQueue, Key: noticestest.Sample("rcp_4").Key, Value: []byte(`{"doc":{"notice":{}},"mac":"AA=="}`), Version: 1}
	big := store.Entry{Partition: notices.PartitionQueue, Key: noticestest.Sample("rcp_5").Key, Value: []byte(strings.Repeat(" ", notices.EntryMax+1)), Version: 1}
	w.mem.DeleteEntry(ctx, notices.PartitionQueue, n.Key) //nolint:errcheck // the good one goes
	for _, e := range []store.Entry{moved, edited, byHand, big} {
		w.mem.SetEntry(e)
	}
	// A mark's signature moved into the queue.
	w.signMark(noticestest.Sample("rcp_6").Key, "sent", w.clock.Now())
	mark := *w.entry(notices.PartitionSent, noticestest.Sample("rcp_6").Key)
	mark.Partition = notices.PartitionQueue
	w.mem.SetEntry(mark)
	// Signed, but of a notice Check refuses, of another key, or with
	// attempts below zero.
	bad := noticestest.Sample("rcp_7")
	bad.Facts.Vendor = "Claude.AI"
	w.put(q, notices.Entry{Notice: bad})
	elsewhere := noticestest.Sample("rcp_8")
	must(t, notices.Write(ctx, q, signKey, &store.Entry{Partition: notices.PartitionQueue, Key: noticestest.Sample("rcp_9").Key}, notices.Entry{Notice: elsewhere}))
	w.put(q, notices.Entry{Notice: noticestest.Sample("rcp_10"), Attempts: -1})
	// Past EntryMax as written: refused before it is written.
	huge := noticestest.Sample("rcp_11")
	huge.Facts.Vendor = strings.Repeat("a", notices.EntryMax) + ".ai"
	if err := notices.Write(ctx, q, signKey, &store.Entry{Partition: notices.PartitionQueue, Key: huge.Key}, notices.Entry{Notice: huge}); !errors.Is(err, notices.ErrInvalid) {
		t.Fatalf("an entry past EntryMax written: %v", err)
	}
	const refused = 8
	for range 3 {
		must(t, q.Drain(ctx))
	}
	b := w.enclave(d, nil)
	must(t, b.Drain(ctx))
	if d.Tries() != 0 || q.Status().Refused != refused || b.Status().Refused != refused || len(w.queued()) != refused {
		t.Fatalf("tries %d, refused %d and %d, left %d: want none sent, each counted once by each and left", d.Tries(), q.Status().Refused, b.Status().Refused, len(w.queued()))
	}
	// One that stops verifying while its first attempt waits is refused
	// by that attempt.
	hold := make(chan struct{})
	c := w.enclave(d, func(context.Context, time.Time) error { <-hold; return nil })
	m := noticestest.Sample("rcp_12")
	must(t, c.Enqueue(ctx, m))
	e := w.entry(notices.PartitionQueue, m.Key)
	e.Value = edited.Value
	w.mem.SetEntry(*e)
	close(hold)
	must(t, notices.Wait(ctx, c))
	if d.Tries() != 0 || c.Status().Refused != 1 {
		t.Fatalf("tries %d, status %+v: want it refused by its attempt", d.Tries(), c.Status())
	}
}

// E24: Enqueue refuses what Check refuses (the review's counterexamples)
// and a notice that is not Fresh, before anything is written.
func TestE24EnqueueRefusesWhatIsNotANoticeOfItsKind(t *testing.T) {
	w := newWorld(t)
	q := w.enclave(&noticestest.Recorder{}, nil)
	for _, change := range []func(*notices.Notice){
		func(n *notices.Notice) { n.Facts.Parts = nil },
		func(n *notices.Notice) { n.Facts.Resource = "" },
		func(n *notices.Notice) { n.Facts.Resource = "smtp" },
		func(n *notices.Notice) { n.Facts.Parts = append(n.Facts.Parts, "everything") },
		func(n *notices.Notice) { n.Facts.By = notices.ByAdmin },
		func(n *notices.Notice) { n.Facts.Vendor = strings.Repeat("a.", 1<<20) + "ai" },
		func(n *notices.Notice) { n.Facts.Vendor = "permaily-security.example/login" },
		func(n *notices.Notice) { n.At = n.At.Add(-notices.AtSkew - time.Second) },
	} {
		n := noticestest.Sample("rcp_1")
		change(&n)
		if err := q.Enqueue(ctx, n); !errors.Is(err, notices.ErrInvalid) {
			t.Fatalf("Enqueue(%+v) = %v, want ErrInvalid", n.Facts, err)
		}
	}
	if len(w.mem.AllEntries()) != 0 {
		t.Fatal("a refused notice was written")
	}
}

// E24: replays. Whoever writes the table can put back an authentic entry
// and delete its mark, its expiry and version altered: it is sent again
// only while Sendable by its signed time, never after, and a mark past
// MarkHolds, or one that does not verify, stops nothing.
func TestE24ReplaysSendOnlyWithinTheSignedWindow(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	hold := make(chan struct{})
	q := w.enclave(d, func(context.Context, time.Time) error { <-hold; return nil })
	n := noticestest.Sample("rcp_1")
	must(t, q.Enqueue(ctx, n))
	saved := *w.entry(notices.PartitionQueue, n.Key)
	close(hold)
	must(t, notices.Wait(ctx, q))
	savedMark := *w.entry(notices.PartitionSent, n.Key)
	replay := func(version uint64, expires time.Time) {
		e := saved
		e.Version, e.ExpiresAt = version, expires
		w.mem.SetEntry(e)
		must(t, w.mem.DeleteEntry(ctx, notices.PartitionSent, n.Key))
	}
	// Within the window: sent again, as often as it is put back.
	w.clock.Advance(time.Hour)
	replay(saved.Version, saved.ExpiresAt)
	must(t, q.Drain(ctx))
	replay(99, saved.ExpiresAt)
	must(t, q.Drain(ctx))
	if got := count(d)[n.Key]; got != 3 {
		t.Fatalf("delivered %d times, want three: once and two replays", got)
	}
	// The old mark restored holds: the entry put back is deleted unsent.
	w.clock.Advance(notices.LeaseFor)
	e := saved
	w.mem.SetEntry(e)
	w.mem.SetEntry(savedMark)
	must(t, q.Drain(ctx))
	if got := count(d)[n.Key]; got != 3 || w.entry(notices.PartitionQueue, n.Key) != nil {
		t.Fatalf("delivered %d times, want the restored mark to stop it", got)
	}
	// Past its window, its expiry stretched: never sent, dropped.
	w.clock.Advance(notices.RetryFor)
	replay(saved.Version, w.clock.Now().Add(365*24*time.Hour))
	must(t, q.Drain(ctx))
	if got := count(d)[n.Key]; got != 3 || q.Status().Dropped != 1 {
		t.Fatalf("delivered %d times, status %+v: want it dropped unsent", got, q.Status())
	}
	// A mark past MarkHolds, one from the future and one that does not
	// verify stop nothing; each is written over when the notice is sent.
	for i, mark := range []func(k string){
		func(k string) { w.signMark(k, "sent", w.clock.Now().Add(-notices.KeptFor-time.Hour)) },
		func(k string) { w.signMark(k, "sent", w.clock.Now().Add(notices.AtSkew+time.Hour)) },
		func(k string) {
			w.mem.SetEntry(store.Entry{Partition: notices.PartitionSent, Key: k, Value: []byte("{}"), Version: 3})
		},
	} {
		m := sampleNow(w, fmt.Sprintf("rcp_old_%d", i))
		mark(m.Key)
		must(t, q.Enqueue(ctx, m))
		must(t, notices.Wait(ctx, q))
		if count(d)[m.Key] != 1 || w.markOf(m.Key) != "sent" {
			t.Fatalf("mark %d stopped a notice, or was not written over", i)
		}
	}
}

// E24: a pass over replayed entries lists at most ListMax and admits at
// most PassSendsMax, however many are put back.
func TestE24APassKeepsItsBoundsOverReplayedEntries(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	var admitted atomic.Int32
	listed := &listCounter{Storage: w.mem}
	w.entries = listed
	q := w.enclave(d, func(context.Context, time.Time) error { admitted.Add(1); return nil })
	for i := range notices.ListMax + 500 {
		w.put(q, notices.Entry{Notice: noticestest.Sample(fmt.Sprintf("rcp_%d", i))})
	}
	must(t, q.Drain(ctx))
	if got := len(d.Delivered()); got != notices.PassSendsMax || int(admitted.Load()) != notices.PassSendsMax {
		t.Fatalf("one pass sent %d after %d admissions, want %d", got, admitted.Load(), notices.PassSendsMax)
	}
	if got := listed.listed.Load(); got > notices.ListMax {
		t.Fatalf("one pass listed %d entries, more than %d", got, notices.ListMax)
	}
}

// listCounter counts the rows the queue's scans read.
type listCounter struct {
	notices.Storage
	listed atomic.Int64
}

func (l *listCounter) ScanEntriesDown(c context.Context, partition, prefix, before string, maxRead int) (*store.EntryScan, error) {
	out, err := l.Storage.ScanEntriesDown(c, partition, prefix, before, maxRead)
	if partition == notices.PartitionQueue && err == nil {
		l.listed.Add(int64(out.Read))
	}
	return out, err
}

// N1: a static prefix of more than ListMax entries that are never sent
// cannot keep a later notice from being attempted; each pass keeps both
// bounds, goes on after the last entry it examined and wraps at the end.
func TestN1APassGoesOnPastWhatItCannotSend(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	var admitted atomic.Int32
	q := w.enclave(d, func(context.Context, time.Time) error { admitted.Add(1); return nil })
	// The prefix: entries that do not verify, under keys the scan, from the
	// greatest down, reads before any other.
	for i := range notices.ListMax + 10 {
		w.mem.SetEntry(store.Entry{Partition: notices.PartitionQueue, Key: fmt.Sprintf("ffff%028x", i), Value: []byte("{}"), Version: 1})
	}
	// Leased and not yet due entries among the rest.
	var leased, later notices.Notice
	for i := 0; ; i++ {
		n := noticestest.Sample(fmt.Sprintf("rcp_%d", i))
		if n.Key >= "f" {
			continue
		}
		switch {
		case leased.Key == "":
			leased = n
			w.put(q, notices.Entry{Notice: n, LeaseUntil: w.clock.Now().Add(notices.LeaseFor), By: "other"})
		case later.Key == "":
			later = n
			w.put(q, notices.Entry{Notice: n, Attempts: 1, NextAt: w.clock.Now().Add(time.Hour)})
		}
		if later.Key != "" {
			break
		}
	}
	due := noticestest.Sample("rcp_due")
	for i := 0; due.Key >= "f"; i++ {
		due = noticestest.Sample(fmt.Sprintf("rcp_due_%d", i))
	}
	w.put(q, notices.Entry{Notice: due})
	for pass := 1; pass <= 3 && len(d.Delivered()) == 0; pass++ {
		before := admitted.Load()
		must(t, notices.Pass(ctx, q))
		if got := int(admitted.Load() - before); got > notices.PassSendsMax {
			t.Fatalf("pass %d admitted %d", pass, got)
		}
	}
	if got := count(d); got[due.Key] != 1 || len(got) != 1 {
		t.Fatalf("delivered %v, want the due notice once, and neither the leased nor the later one", got)
	}
}

// N1: with more due than PassSendsMax, a pass stops at the last entry it
// examined and the next goes on from there, round to the start: every
// notice is sent once.
func TestN1PassesGoOnFromWhereTheSendCapStoppedThem(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	q := w.enclave(d, nil)
	total := 2*notices.PassSendsMax + 7
	for i := range total {
		w.put(q, notices.Entry{Notice: noticestest.Sample(fmt.Sprintf("rcp_%d", i))})
	}
	for pass := 1; pass <= 3; pass++ {
		must(t, notices.Pass(ctx, q))
		if got, want := len(d.Delivered()), min(pass*notices.PassSendsMax, total); got != want {
			t.Fatalf("after pass %d sent %d, want %d", pass, got, want)
		}
	}
	// A notice put before the cursor is found once the pass wraps.
	n := noticestest.Sample("rcp_late")
	w.put(q, notices.Entry{Notice: n})
	for range 2 {
		must(t, notices.Pass(ctx, q))
	}
	got := count(d)
	for k, c := range got {
		if c != 1 {
			t.Fatalf("%s sent %d times", k, c)
		}
	}
	if len(got) != total+1 {
		t.Fatalf("sent %d notices, want %d", len(got), total+1)
	}
}

// E26: a thousand queued at once in one enclave drain at SendsPerSecond
// and InFlightMax at most in each enclave; with the clock past LeaseFor
// while most wait for admission and the other enclave draining at the
// same time, each is sent once.
func TestE26AThousandQueuedAtOnceAreEachSentOnceWithinTheBounds(t *testing.T) {
	w := newWorld(t)
	rec := &noticestest.Recorder{}
	slow := deliverFunc(func(c context.Context, n notices.Notice) error {
		time.Sleep(50 * time.Microsecond) // long enough for attempts to overlap
		return rec.Deliver(c, n)
	})
	da, db := &noticestest.Bounded{Deliverer: slow}, &noticestest.Bounded{Deliverer: slow}
	type admissions struct {
		mu  sync.Mutex
		ats []time.Time
	}
	var atA, atB admissions
	record := func(a *admissions, at time.Time) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.ats = append(a.ats, at)
	}
	const letThrough = 40
	hold, holding := make(chan struct{}), make(chan struct{})
	var once sync.Once
	a := w.enclave(da, func(c context.Context, at time.Time) error {
		record(&atA, at)
		atA.mu.Lock()
		n := len(atA.ats)
		atA.mu.Unlock()
		if n <= letThrough {
			return nil
		}
		once.Do(func() { close(holding) })
		select {
		case <-hold:
			return nil
		case <-c.Done():
			return c.Err()
		}
	})
	b := w.enclave(db, func(_ context.Context, at time.Time) error { record(&atB, at); return nil })
	const total = 1000
	keys := make([]string, total)
	for i := range total {
		n := noticestest.Sample(fmt.Sprintf("rcp_%d", i))
		keys[i] = n.Key
		must(t, a.Enqueue(ctx, n))
	}
	<-holding
	w.clock.Advance(notices.LeaseFor + time.Second)
	var passes []int
	for pass := 0; len(w.queued()) > 0 && pass < 20; pass++ {
		atB.mu.Lock()
		before := len(atB.ats)
		atB.mu.Unlock()
		must(t, b.Drain(ctx))
		atB.mu.Lock()
		passes = append(passes, len(atB.ats)-before)
		atB.mu.Unlock()
		if pass == 1 {
			close(hold) // let A go while B drains
		}
	}
	must(t, notices.Wait(ctx, a))
	for range 2 {
		must(t, a.Drain(ctx))
		must(t, b.Drain(ctx))
	}
	got := count(rec)
	for i, k := range keys {
		if got[k] != 1 {
			t.Fatalf("notice %d sent %d times, want once", i, got[k])
		}
	}
	for _, p := range passes {
		if p > notices.PassSendsMax {
			t.Fatalf("a pass of b admitted %d, more than %d", p, notices.PassSendsMax)
		}
	}
	for name, e := range map[string]*noticestest.Bounded{"a": da, "b": db} {
		if err := e.Err(); err != nil || e.Most() > notices.InFlightMax {
			t.Fatalf("enclave %s: %v, %d at once", name, err, e.Most())
		}
	}
	for name, adm := range map[string]*admissions{"a": &atA, "b": &atB} {
		slices.SortFunc(adm.ats, time.Time.Compare)
		for i := 1; i < len(adm.ats); i++ {
			if gap := adm.ats[i].Sub(adm.ats[i-1]); gap < time.Second/notices.SendsPerSecond {
				t.Fatalf("enclave %s admitted two attempts %v apart, faster than %d a second", name, gap, notices.SendsPerSecond)
			}
		}
	}
	if da.Most() < 2 && db.Most() < 2 {
		t.Log("no two attempts overlapped: the bound on attempts at once was not exercised")
	}
}
