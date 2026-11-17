package connections

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// The review of pull request 161 and its rulings: a summary has one
// identity and is stored once; a failed write is tried again on one
// schedule; a refused receipt that failed is never counted as recorded; a
// refusal never overtakes the summary before it; a stop waits and writes
// everything; a removed account's counts are dropped; the lanes are
// bounded for the enclave.

// losing wraps a writer: while lose says so of a receipt, it is stored and
// an error is answered, as storage whose answer never came back does.
type losing struct {
	receipts.Writer
	mu   sync.Mutex
	lose func(*receipts.Receipt) bool
}

func (l *losing) Write(ctx context.Context, account string, r *receipts.Receipt) error {
	if err := l.Writer.Write(ctx, account, r); err != nil {
		return err
	}
	l.mu.Lock()
	lose := l.lose
	l.mu.Unlock()
	if lose != nil && lose(r) {
		return errors.New("the storage's answer was lost")
	}
	return nil
}

func (l *losing) set(lose func(*receipts.Receipt) bool) {
	l.mu.Lock()
	l.lose = lose
	l.mu.Unlock()
}

func ofKind(k receipts.Kind) func(*receipts.Receipt) bool {
	return func(r *receipts.Receipt) bool { return r.Kind == k }
}

// stored is every receipt of account in the sealed store, by kind.
func stored(t *testing.T, real *receipts.Service, account string) map[receipts.Kind][]receipts.Receipt {
	t.Helper()
	rs, err := real.List(context.Background(), account, "", 10000)
	if err != nil {
		t.Fatal(err)
	}
	out := map[receipts.Kind][]receipts.Receipt{}
	for _, r := range rs {
		out[r.Kind] = append(out[r.Kind], r)
	}
	return out
}

// failing sets what w's writer answers.
func (w *world) failing(err error) {
	w.rcpt.mu.Lock()
	w.rcpt.fail = err
	w.rcpt.mu.Unlock()
}

// attempts counts the writes of each kind w's writer is asked for.
func (w *world) attempts() func(receipts.Kind) int64 {
	var mu sync.Mutex
	n := map[receipts.Kind]int64{}
	w.rcpt.mu.Lock()
	w.rcpt.onWrite = func(r *receipts.Receipt) {
		mu.Lock()
		n[r.Kind]++
		mu.Unlock()
	}
	w.rcpt.mu.Unlock()
	return func(k receipts.Kind) int64 {
		mu.Lock()
		defer mu.Unlock()
		return n[k]
	}
}

// lanes is what s keeps of its connections' refusals: how many lanes, and
// the most writes not yet stored one holds.
func lanes(s *Service) (n, writes int) {
	s.refusals.mu.Lock()
	defer s.refusals.mu.Unlock()
	for _, l := range s.refusals.by {
		writes = max(writes, len(l.queue))
	}
	return len(s.refusals.by), writes
}

// H35 (finding 1, P1): a summary whose write's answer is lost is stored
// once, whatever the requests after it: it is tried again under the same
// identity, and only on the schedule the minute pass and the requests share
// (RefusalRetry), never at the request rate; an outright failure is tried
// again on the same schedule.
func TestH35ASummaryIsStoredOnceAndTriedOnOneSchedule(t *testing.T) {
	t.Run("its answer lost", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		real := receipts.New(w.mem, w.keys, w.clock.now, nil)
		lost := &losing{Writer: real}
		s.cfg.Receipts = lost
		for range 50 {
			refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		}
		toTheNextWindow(w)
		lost.set(ofKind(receipts.KindRefusals))
		for range 1000 {
			refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		}
		if got := stored(t, real, g.Account)[receipts.KindRefusals]; len(got) != 1 {
			t.Fatalf("%d summaries stored of one window, want 1", len(got))
		}
		// Due again, it is the same receipt: stored once, then the next.
		lost.set(nil)
		w.clock.add(RefusalRetry)
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		got := stored(t, real, g.Account)
		sum := got[receipts.KindRefusals]
		if len(sum) != 2 || sum[0].ID == sum[1].ID {
			t.Fatalf("%d summaries, want the first window's and the second's", len(sum))
		}
		if n, rec := counted(sum[:1]); n != 20 || rec != RefusalsPerWindow {
			t.Fatalf("the first window's summary counts %d, recorded %d", n, rec)
		}
		// The second window's refusals waited behind it: counted, none in
		// its own receipt.
		if n, rec := counted(sum[1:]); n != 1000 || rec != 0 {
			t.Fatalf("the second window's summary counts %d, recorded %d", n, rec)
		}
		if n := len(got[receipts.KindRefused]); n != RefusalsPerWindow {
			t.Fatalf("%d refused receipts, want %d", n, RefusalsPerWindow)
		}
		if n, w := lanes(s); w != 0 {
			t.Fatalf("%d lanes keep %d writes", n, w)
		}
	})
	t.Run("an outright failure", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		for range 50 {
			refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		}
		toTheNextWindow(w)
		tries := w.attempts()
		w.failing(errors.New("the storage refuses"))
		if err := s.FlushRefusals(context.Background(), false); err == nil {
			t.Fatal("a pass the storage refused succeeded")
		}
		for range 1000 {
			_ = s.Refuse(context.Background(), g, Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody})
		}
		if n := tries(receipts.KindRefusals); n != 1 {
			t.Fatalf("%d tries of one summary, want 1 until it is due again", n)
		}
		if n := tries(receipts.KindRefused); n != 0 {
			t.Fatalf("%d refused receipts tried behind a summary not stored", n)
		}
		// Due again: the first request after RefusalRetry tries it, once,
		// and a pass right after does not.
		w.clock.add(RefusalRetry - time.Second)
		refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		w.clock.add(time.Second)
		refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err) // nothing due
		}
		if n := tries(receipts.KindRefusals); n != 2 {
			t.Fatalf("%d tries, want 2", n)
		}
		w.failing(nil)
		w.clock.add(RefusalRetry)
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		sum := w.rcpt.of(receipts.KindRefusals)
		n, rec := counted(sum)
		if len(sum) != 2 || n+rec != 50+1003 || rec != RefusalsPerWindow {
			t.Fatalf("%d summaries count %d and recorded %d, want 1053 refusals", len(sum), n, rec)
		}
	})
}

// H36 (finding 2): a refused receipt whose write failed is not counted as
// recorded, nor lost: it is kept under its identity and written again on
// the schedule, and the summary says as recorded only those stored; the
// slot it took stays taken, so failures never reopen the bound into more
// writes. Before, at and after the bound.
func TestH36AFailedRefusedReceiptIsKeptNotCountedAsRecorded(t *testing.T) {
	check := func(t *testing.T, w *world, total int) {
		t.Helper()
		single := len(w.rcpt.of(receipts.KindRefused))
		n, rec := counted(w.rcpt.of(receipts.KindRefusals))
		if rec != single || single+n != total {
			t.Fatalf("%d refused receipts, summaries recorded %d and counted %d, want %d in all", single, rec, n, total)
		}
	}
	t.Run("before the bound", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		tries := w.attempts()
		w.failing(errors.New("the storage refuses"))
		if err := s.Refuse(context.Background(), g, Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("a refusal whose receipt failed: %v", err)
		}
		for range 29 {
			refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		}
		w.failing(nil)
		refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		if n := tries(receipts.KindRefused); n != 1 {
			t.Fatalf("%d refused receipts tried, want 1", n)
		}
		w.clock.add(RefusalRetry)
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		check(t, w, 31)
	})
	t.Run("at the bound", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		for range RefusalsPerWindow - 1 {
			refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		}
		w.failing(errors.New("the storage refuses"))
		if err := s.Refuse(context.Background(), g, Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("a refusal whose receipt failed: %v", err)
		}
		w.failing(nil)
		for range 40 {
			refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		}
		w.clock.add(RefusalRetry)
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		check(t, w, 70)
		if got := len(w.rcpt.of(receipts.KindRefused)); got != RefusalsPerWindow {
			t.Fatalf("%d refused receipts, want %d", got, RefusalsPerWindow)
		}
	})
	t.Run("past the bound", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		for range 35 {
			refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		}
		tries := w.attempts()
		w.failing(errors.New("the storage refuses"))
		for range 100 {
			refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		}
		if n := tries(receipts.KindRefused); n != 0 {
			t.Fatalf("%d refused receipts tried past the bound", n)
		}
		w.failing(nil)
		toTheNextWindow(w)
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		check(t, w, 135)
	})
	t.Run("its answer lost", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		real := receipts.New(w.mem, w.keys, w.clock.now, nil)
		lost := &losing{Writer: real, lose: ofKind(receipts.KindRefused)}
		s.cfg.Receipts = lost
		if err := s.Refuse(context.Background(), g, Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("a refusal whose receipt's answer was lost: %v", err)
		}
		lost.set(nil)
		for range 10 {
			refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		}
		w.clock.add(RefusalRetry)
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		got := stored(t, real, g.Account)
		n, rec := counted(got[receipts.KindRefusals])
		if len(got[receipts.KindRefused]) != 1 || rec != 1 || n != 10 {
			t.Fatalf("%d refused receipts stored, summaries recorded %d and counted %d", len(got[receipts.KindRefused]), rec, n)
		}
	})
}

// H37 (finding 3): a refusal never overtakes the summary before it, from a
// request or the minute pass, nor waits on the enclave's lock: it waits for
// its own connection's write. When that summary cannot be stored, the
// connection's refusals after it are counted, never written in their own
// receipt, until it is.
func TestH37ARefusalNeverOvertakesTheSummaryBeforeIt(t *testing.T) {
	for _, by := range []string{"a request", "the pass"} {
		t.Run(by, func(t *testing.T) {
			w, s, g := refusalsWorld(t)
			for range 50 {
				refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
			}
			toTheNextWindow(w)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			w.rcpt.mu.Lock()
			w.rcpt.onWrite = func(r *receipts.Receipt) {
				if r.Kind == receipts.KindRefusals {
					once.Do(func() { close(entered) })
					<-release
				}
			}
			w.rcpt.mu.Unlock()
			var wg sync.WaitGroup
			if by == "a request" {
				wg.Go(func() { refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody) })
			} else {
				wg.Go(func() {
					if err := s.FlushRefusals(context.Background(), false); err != nil {
						t.Error(err)
					}
				})
			}
			<-entered
			var released sync.Once
			free := func() { released.Do(func() { close(release) }) }
			defer func() { free(); wg.Wait() }()
			x := w.ask(otherApp)
			tok := w.connectAll(s, w.person(accountB), x)
			other := w.grant(s, x, tok.AccessToken)
			before := w.rcpt.count()
			wg.Go(func() { refuseOnce(t, s, g, OperationRead, receipts.RefusedOutsideScope) })
			// Another connection is not held back.
			refuseOnce(t, s, other, OperationRead, receipts.RefusedInvalidBody)
			time.Sleep(50 * time.Millisecond)
			if got := w.rcpt.count(); got != before+1 {
				t.Fatalf("%d receipts written while the summary was, want another connection's alone", got-before)
			}
			free()
			wg.Wait()
			w.rcpt.mu.Lock()
			all := w.rcpt.all
			w.rcpt.mu.Unlock()
			summary := -1
			for i, r := range all {
				if r.Connection != g.Connection {
					continue
				}
				switch {
				case r.Kind == receipts.KindRefusals:
					summary = i
				case summary < 0 && r.Refused != nil && r.Refused.Reason == receipts.RefusedOutsideScope:
					t.Fatal("the next refusal was stored before the summary")
				}
			}
			if summary < 0 {
				t.Fatal("no summary")
			}
		})
	}
	t.Run("a summary not stored", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		for range 50 {
			refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		}
		toTheNextWindow(w)
		w.failing(errors.New("the storage refuses"))
		refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		w.failing(nil)
		before := len(w.rcpt.of(receipts.KindRefused))
		for range 5 {
			refuseOnce(t, s, g, OperationSearch, receipts.RefusedInvalidBody)
		}
		if got := len(w.rcpt.of(receipts.KindRefused)); got != before {
			t.Fatalf("%d refused receipts written behind a summary not stored", got-before)
		}
		w.clock.add(RefusalRetry)
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		sum := w.rcpt.of(receipts.KindRefusals)
		if len(sum) != 2 {
			t.Fatalf("%d summaries", len(sum))
		}
		if n, rec := counted(sum[1:]); n != 6 || rec != 0 {
			t.Fatalf("the refusals behind it: counted %d, recorded %d", n, rec)
		}
	})
}

// H38 (finding 4): a stop admits no more attributed requests, waits for
// those under way and for the minute pass, and writes everything counted;
// when it cannot within its deadline, it says so.
func TestH38AStopWaitsAndWritesEverything(t *testing.T) {
	t.Run("a pass under way", func(t *testing.T) {
		// The pass is writing a summary when the stop's signal ends it; the
		// write is stored, but the pass does not learn it. The stop waits
		// for the pass's write, then writes it again, stored once.
		w, s, g := refusalsWorld(t)
		for range 50 {
			refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		}
		toTheNextWindow(w)
		ctx, cancel := context.WithCancel(context.Background())
		entered, release := make(chan struct{}), make(chan struct{})
		var once, lost sync.Once
		w.rcpt.mu.Lock()
		w.rcpt.onWrite = func(r *receipts.Receipt) {
			if r.Kind == receipts.KindRefusals {
				once.Do(func() {
					close(entered)
					<-release
				})
			}
		}
		w.rcpt.lose = func(r *receipts.Receipt) bool {
			first := false
			if r.Kind == receipts.KindRefusals {
				lost.Do(func() { first = true })
			}
			return first
		}
		w.rcpt.mu.Unlock()
		passed := make(chan error, 1)
		go func() { passed <- s.FlushRefusals(ctx, false) }()
		<-entered
		cancel() // the stop's signal ends the pass
		stopped := make(chan error, 1)
		go func() { stopped <- s.Stop(context.Background()) }()
		time.Sleep(20 * time.Millisecond)
		select {
		case err := <-stopped:
			t.Fatalf("the stop ended while the pass was writing: %v", err)
		default:
		}
		close(release)
		if err := <-passed; err == nil {
			t.Fatal("a pass whose write's answer was lost succeeded")
		}
		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
		sum := w.rcpt.of(receipts.KindRefusals)
		if n, rec := counted(sum); len(sum) != 1 || n != 20 || rec != RefusalsPerWindow {
			t.Fatalf("%d summaries count %d and recorded %d", len(sum), n, rec)
		}
		if n, _ := lanes(s); n != 0 {
			t.Fatalf("%d lanes left after a stop", n)
		}
	})
	t.Run("no request after it", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		if err := s.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		before := w.rcpt.count()
		if err := s.Refuse(context.Background(), g, Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("a refusal after the stop: %v", err)
		}
		if _, _, err := s.Search(context.Background(), g, contracts.VendorSearch{}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("a search after the stop: %v", err)
		}
		if _, _, err := s.Read(context.Background(), g, "m001"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("a read after the stop: %v", err)
		}
		if _, _, err := s.Attachment(context.Background(), g, "m001", 0); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("an attachment after the stop: %v", err)
		}
		if w.rcpt.count() != before {
			t.Fatal("a request after the stop was recorded")
		}
	})
	t.Run("a request under way", func(t *testing.T) {
		// A refusal is writing the summary before its own receipt when the
		// stop comes: the stop waits for it, then writes the rest.
		w, s, g := refusalsWorld(t)
		for range 35 {
			refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		}
		toTheNextWindow(w)
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		w.rcpt.mu.Lock()
		w.rcpt.onWrite = func(r *receipts.Receipt) {
			if r.Kind == receipts.KindRefusals {
				once.Do(func() {
					close(entered)
					<-release
				})
			}
		}
		w.rcpt.mu.Unlock()
		var wg sync.WaitGroup
		wg.Go(func() { refuseOnce(t, s, g, OperationSearch, receipts.RefusedInvalidBody) })
		<-entered
		var released sync.Once
		free := func() { released.Do(func() { close(release) }) }
		defer func() { free(); wg.Wait() }()
		stopped := make(chan error, 1)
		go func() { stopped <- s.Stop(context.Background()) }()
		time.Sleep(20 * time.Millisecond)
		select {
		case err := <-stopped:
			t.Fatalf("the stop ended before the request under way: %v", err)
		default:
		}
		free()
		wg.Wait()
		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
		if n, rec := counted(w.rcpt.of(receipts.KindRefusals)); n != 5 || rec != RefusalsPerWindow {
			t.Fatalf("the summaries count %d and recorded %d", n, rec)
		}
		if got := len(w.rcpt.of(receipts.KindRefused)); got != RefusalsPerWindow+1 {
			t.Fatalf("%d refused receipts, want the request's under way too", got)
		}
	})
	t.Run("past its deadline", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		for range 40 {
			refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		}
		w.failing(errors.New("the storage refuses"))
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Stop(ctx); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("a stop that wrote nothing: %v", err)
		}
		// A request that does not end in time is said too.
		w2, s2, g2 := refusalsWorld(t)
		release := make(chan struct{})
		defer close(release)
		w2.rcpt.mu.Lock()
		w2.rcpt.block = release
		w2.rcpt.mu.Unlock()
		go func() {
			_ = s2.Refuse(context.Background(), g2, Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody})
		}()
		time.Sleep(20 * time.Millisecond)
		short, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel2()
		if err := s2.Stop(short); err == nil {
			t.Fatal("a stop with a request under way past its deadline succeeded")
		}
	})
}

// H39 (finding 5): a removed account's counts are dropped on every enclave:
// before a summary is written, or a write tried again, its standing is read
// now; gone, nothing is written, even after the last sweep of its receipts;
// unreadable, nothing is written until it is read.
func TestH39ARemovedAccountsCountsAreDropped(t *testing.T) {
	t.Run("gone", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		real := receipts.New(w.mem, w.keys, w.clock.now, nil)
		s.cfg.Receipts = real
		for range 50 {
			refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		}
		toTheNextWindow(w)
		// The account is removed through another enclave.
		if err := w.enclave().RemoveAll(context.Background(), g.Account); err != nil {
			t.Fatal(err)
		}
		w.accts.set(g.Account, StandingGone)
		for range 2 {
			if err := w.mem.DeletePartition(context.Background(), receipts.Partition(g.Account)); err != nil {
				t.Fatal(err)
			}
			w.clock.add(receipts.RemoveAgainAfter + time.Minute)
		}
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		if err := s.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := stored(t, real, g.Account); len(got) != 0 {
			t.Fatalf("receipts of a removed account written after its last sweep: %d kinds", len(got))
		}
		if n, _ := lanes(s); n != 0 {
			t.Fatalf("%d lanes of a removed account", n)
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		for range 50 {
			refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		}
		toTheNextWindow(w)
		w.accts.mu.Lock()
		w.accts.fail = errors.New("the accounts cannot be read")
		w.accts.mu.Unlock()
		if err := s.FlushRefusals(context.Background(), false); err == nil {
			t.Fatal("a pass that could not read the standing succeeded")
		}
		if got := w.rcpt.of(receipts.KindRefusals); len(got) != 0 {
			t.Fatal("a summary was written without the account's standing")
		}
		w.accts.mu.Lock()
		w.accts.fail = nil
		w.accts.mu.Unlock()
		w.clock.add(RefusalRetry)
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		if n, _ := counted(w.rcpt.of(receipts.KindRefusals)); n != 20 {
			t.Fatalf("the summary counts %d once the standing was read", n)
		}
	})
}

// as is g for another connection of its account.
func as(g *oauth.Grant, i int) *oauth.Grant {
	o := *g
	o.Connection = fmt.Sprintf("con_%026d", i)
	return &o
}

// H40 (finding 6): the lanes are bounded for the enclave, by count and
// bytes. A refusal that would need a lane past them is answered
// ErrUnavailable, counted nowhere, and nothing already counted is evicted;
// a refused receipt that would pass the bytes is counted instead. Many
// connections over failing, then slow, storage stay within it, a lane
// holding at most two writes, and the pass writes several lanes at once.
func TestH40TheLanesAreBoundedForTheEnclave(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		s.refusals.mu.Lock()
		s.refusals.lanesMax = 3
		s.refusals.mu.Unlock()
		for i := range 3 {
			for range 40 {
				refuseOnce(t, s, as(g, i), OperationRead, receipts.RefusedInvalidBody)
			}
		}
		before := w.rcpt.count()
		if err := s.Refuse(context.Background(), as(g, 3), Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("a refusal past the lanes: %v", err)
		}
		if w.rcpt.count() != before {
			t.Fatal("a refusal past the lanes was recorded")
		}
		toTheNextWindow(w)
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		if n, _ := counted(w.rcpt.of(receipts.KindRefusals)); n != 30 {
			t.Fatalf("the summaries count %d, want every lane's", n)
		}
		// Once written, the lanes make room.
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		refuseOnce(t, s, as(g, 3), OperationRead, receipts.RefusedInvalidBody)
	})
	t.Run("bytes", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		s.refusals.mu.Lock()
		s.refusals.bytesMax = s.refusals.bytes
		s.refusals.mu.Unlock()
		before := len(w.rcpt.of(receipts.KindRefused))
		for range 5 {
			refuseOnce(t, s, g, OperationRead, receipts.RefusedInvalidBody)
		}
		if got := len(w.rcpt.of(receipts.KindRefused)); got != before {
			t.Fatalf("%d refused receipts past the bytes", got-before)
		}
		if err := s.Refuse(context.Background(), as(g, 1), Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("a lane past the bytes: %v", err)
		}
		toTheNextWindow(w)
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		if n, rec := counted(w.rcpt.of(receipts.KindRefusals)); n != 5 || rec != 1 {
			t.Fatalf("counted %d, recorded %d", n, rec)
		}
	})
	t.Run("many connections over failing and slow storage", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		const conns, each = 200, 31
		w.failing(errors.New("the storage refuses"))
		var wg sync.WaitGroup
		for i := range conns {
			wg.Go(func() {
				for range each {
					_ = s.Refuse(context.Background(), as(g, i), Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody})
				}
			})
		}
		wg.Wait()
		toTheNextWindow(w)
		if err := s.FlushRefusals(context.Background(), false); err == nil {
			t.Fatal("a pass the storage refused succeeded")
		}
		if n, writes := lanes(s); n != conns || writes > 2 {
			t.Fatalf("%d lanes, one with %d writes", n, writes)
		}
		var at, most atomic.Int64
		w.rcpt.mu.Lock()
		w.rcpt.onWrite = func(*receipts.Receipt) {
			n := at.Add(1)
			for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
			}
			time.Sleep(time.Millisecond)
			at.Add(-1)
		}
		w.rcpt.fail = nil
		w.rcpt.mu.Unlock()
		w.clock.add(RefusalRetry)
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		if m := most.Load(); m < 2 || m > refusalPassWorkers {
			t.Fatalf("%d writes at once, want between 2 and %d", m, refusalPassWorkers)
		}
		single := len(w.rcpt.of(receipts.KindRefused))
		n, rec := counted(w.rcpt.of(receipts.KindRefusals))
		if single != conns || rec != conns || single+n != conns*each {
			t.Fatalf("%d refused receipts, recorded %d, counted %d, want %d", single, rec, n, conns*each)
		}
		if n, _ := lanes(s); n != conns {
			t.Fatalf("%d lanes before their windows are written", n)
		}
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		if n, _ := lanes(s); n != 0 {
			t.Fatalf("%d lanes left once written", n)
		}
	})
}
