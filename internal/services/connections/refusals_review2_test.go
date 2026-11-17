package connections

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"weak"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// The second review of pull request 161: a stored receipt is let go as
// soon as its write is confirmed; a request waiting for its connection's
// write holds its part of the enclave's budget, or is answered 503, and
// waits a bounded time; and a refusal that waited reads its account's
// standing before it writes its own receipt.

// writerFunc is a receipts.Writer of a function.
type writerFunc func(context.Context, string, *receipts.Receipt) error

func (f writerFunc) Write(ctx context.Context, a string, r *receipts.Receipt) error {
	return f(ctx, a, r)
}

// heldPage is a refusal's held page as large as a page gets: 50 messages,
// each with the default rule's two model answers.
func heldPage() []receipts.Held {
	held := make([]receipts.Held, 50)
	for i := range held {
		held[i] = receipts.Held{Mailbox: "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa", Message: strings.Clone("0000018a0b0c0d0f2122232425262728"), Rule: rules.Ref{ID: rules.DefaultID, Version: rules.DefaultVersion}, Why: "decided", Model: []receipts.Model{
			{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: strings.Clone("a sign-in, login or verification code, or a one-time password"), P: 0.9, Name: "model", Prompt: "v1", CanaryChecked: "2026-11-17", CanaryChange: "2026-11-16"},
			{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: strings.Clone("resetting a password or recovering an account"), P: 0.8, Name: "model", Prompt: "v1", CanaryChecked: "2026-11-17", CanaryChange: "2026-11-16"},
		}}
	}
	return held
}

// refuseHeld is a refusal of g's search that holds a whole page.
func refuseHeld(ctx context.Context, s *Service, g *oauth.Grant) error {
	r := &reading{g: g, op: OperationSearch, conn: &connDoc{}}
	rc := s.receipt(r, receipts.KindRefused)
	rc.Held = heldPage()
	rc.Refused = &receipts.Refused{Reason: receipts.RefusedModelUnavailable}
	return s.refuseWith(ctx, r, rc, nil)
}

// refuseWeak is refuseHeld, keeping nothing of its page but a weak
// pointer.
func refuseWeak(t *testing.T, s *Service, g *oauth.Grant) weak.Pointer[receipts.Held] {
	t.Helper()
	r := &reading{g: g, op: OperationSearch, conn: &connDoc{}}
	rc := s.receipt(r, receipts.KindRefused)
	rc.Held = heldPage()
	rc.Refused = &receipts.Refused{Reason: receipts.RefusedModelUnavailable}
	page := weak.Make(&rc.Held[0])
	if err := s.refuseWith(context.Background(), r, rc, nil); err != nil {
		t.Fatal(err)
	}
	return page
}

// heap is the live heap after a collection.
func heap() int64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.HeapAlloc)
}

// waiting is how many requests wait on conn's lane in s, and the bytes
// its lanes and waiters hold.
func waiting(s *Service, conn string) (n, bytes int) {
	s.refusals.mu.Lock()
	defer s.refusals.mu.Unlock()
	if l := s.refusals.by[conn]; l != nil {
		n = l.waiting
	}
	return n, s.refusals.bytes
}

// eventually waits until ok, which reads a state that stays once it is
// reached. It has no bound of its own: under -race on a loaded runner the
// time a few thousand requests take to reach their lanes has none worth
// writing (a ten-second bound failed CI after 12.8 s), and a state never
// reached is a test that runs out of go test's -timeout, which shows every
// goroutine. It sleeps between reads so that it does not take the lanes'
// lock from the requests it waits for.
func eventually(ok func() bool) {
	for !ok() {
		time.Sleep(time.Millisecond)
	}
}

// waitForRelease makes the requests that wait on s's lanes wait until the
// test releases their writers, however long it takes them all to arrive,
// so that what a test reads of them while they wait stays as it is.
func waitForRelease(s *Service) {
	s.refusals.mu.Lock()
	s.refusals.waitMax = time.Hour
	s.refusals.mu.Unlock()
}

// pausing makes w's writer hold the first refused receipt of conn until
// the returned release, and says when it began.
func pausing(w *world, conn string) (entered <-chan struct{}, release func()) {
	in, out := make(chan struct{}), make(chan struct{})
	var first, freed sync.Once
	w.rcpt.mu.Lock()
	w.rcpt.onWrite = func(r *receipts.Receipt) {
		if r.Kind != receipts.KindRefused || r.Connection != conn {
			return
		}
		mine := false
		first.Do(func() { mine = true })
		if mine {
			close(in)
			<-out
		}
	}
	w.rcpt.mu.Unlock()
	return in, func() { freed.Do(func() { close(out) }) }
}

// H42 (second review, finding 2): a lane lets go of a receipt as soon as
// its write is confirmed, so what it holds is what its budget says: no
// stored receipt stays reachable from a lane's queue, and lanes whose
// receipts were all stored hold far less than RefusalBytesMax, whatever
// their receipts held.
func TestH42AStoredReceiptIsLetGoAtOnce(t *testing.T) {
	t.Run("a stored page is unreachable", func(t *testing.T) {
		_, s, g := refusalsWorld(t)
		s.cfg.Receipts = writerFunc(func(context.Context, string, *receipts.Receipt) error { return nil })
		page := refuseWeak(t, s, g)
		runtime.GC()
		runtime.GC()
		if page.Value() != nil {
			t.Fatal("a lane still holds a stored receipt's page")
		}
		if n, _ := lanes(s); n != 1 {
			t.Fatalf("%d lanes", n)
		}
	})
	t.Run("many lanes of stored pages", func(t *testing.T) {
		_, s, g := refusalsWorld(t)
		s.cfg.Receipts = writerFunc(func(context.Context, string, *receipts.Receipt) error { return nil })
		before := heap()
		const conns = 3500
		for i := range conns {
			if err := refuseHeld(context.Background(), s, as(g, i)); err != nil {
				t.Fatal(err)
			}
		}
		grown := heap() - before
		_, queued := lanes(s)
		t.Logf("%d lanes: %d bytes of the heap", conns, grown)
		runtime.KeepAlive(s)
		if grown > RefusalBytesMax {
			t.Fatalf("the lanes hold %d bytes of the heap past RefusalBytesMax (%d), all their receipts stored (%d writes queued)", grown, RefusalBytesMax, queued)
		}
	})
}

// H43 (second review, finding 1): a request that waits for its
// connection's write holds its part of the enclave's budget while it waits
// (its receipt and an allowance for itself), and is answered ErrUnavailable
// (503 with Retry-After) when the budget has no room, recording nothing; a
// connection has no more waiting than its window has refused receipts left,
// the others counted at once; and a request waits at most the lanes'
// waitMax, then is counted.
func TestH43WaitersAreBoundedAndHeldOfTheBudget(t *testing.T) {
	t.Run("a flood behind one write", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		waitForRelease(s)
		entered, release := pausing(w, g.Connection)
		defer release()
		first := make(chan error, 1)
		go func() {
			first <- s.Refuse(context.Background(), g, Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody})
		}()
		<-entered
		before := heap()
		const requests = 3500
		var wg sync.WaitGroup
		var done atomic.Int64
		for range requests {
			wg.Go(func() {
				// The request's own context ended: the refusal is still
				// recorded, but its wait is the service's to bound.
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if err := refuseHeld(ctx, s, g); err != nil {
					t.Error(err)
				}
				done.Add(1)
			})
		}
		eventually(func() bool {
			n, _ := waiting(s, g.Connection)
			return int(done.Load())+n == requests
		})
		grown := heap() - before
		n, _ := waiting(s, g.Connection)
		t.Logf("%d requests waiting: %d bytes of the heap", n, grown)
		if n > RefusalsPerWindow-1 {
			t.Errorf("%d requests wait on one connection with %d refused receipts left in its window", n, RefusalsPerWindow-1)
		}
		if grown > RefusalBytesMax {
			t.Errorf("the waiting requests hold %d bytes of the heap, past RefusalBytesMax", grown)
		}
		release()
		wg.Wait()
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		toTheNextWindow(w)
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		single := len(w.rcpt.of(receipts.KindRefused))
		c, rec := counted(w.rcpt.of(receipts.KindRefusals))
		if single+c != requests+1 || rec != single {
			t.Fatalf("%d refused receipts and %d counted (recorded %d), want %d refusals", single, c, rec, requests+1)
		}
	})
	t.Run("many lanes behind their writes", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		// Every request either waits, holding its part of the budget, or
		// is answered 503: none is counted while it waits, so the state
		// read below is the one the budget bounds, whatever the runner.
		waitForRelease(s)
		release := make(chan struct{})
		var entered sync.WaitGroup
		const conns = 3500
		entered.Add(conns)
		w.rcpt.mu.Lock()
		w.rcpt.onWrite = func(r *receipts.Receipt) {
			if r.Kind == receipts.KindRefused && r.Held == nil {
				entered.Done()
				<-release
			}
		}
		w.rcpt.mu.Unlock()
		var wg sync.WaitGroup
		for i := range conns {
			wg.Go(func() {
				_ = s.Refuse(context.Background(), as(g, i), Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody})
			})
		}
		entered.Wait()
		before := heap()
		var refused, done atomic.Int64
		for i := range conns {
			wg.Go(func() {
				err := refuseHeld(context.Background(), s, as(g, i))
				if errors.Is(err, ErrUnavailable) {
					refused.Add(1)
				}
				done.Add(1)
			})
		}
		// Each request has returned or waits on its lane, and none leaves
		// before release: what is read here stays as it is.
		var n int
		eventually(func() bool {
			s.refusals.mu.Lock()
			n = 0
			for _, l := range s.refusals.by {
				n += l.waiting
			}
			s.refusals.mu.Unlock()
			return int(done.Load())+n == conns
		})
		grown := heap() - before
		_, bytes := waiting(s, "")
		answered := int(done.Load())
		t.Logf("%d requests waiting, %d answered 503: %d bytes of the heap, %d of the budget", n, refused.Load(), grown, bytes)
		close(release)
		wg.Wait()
		if answered != int(refused.Load()) {
			t.Errorf("%d requests returned while their lanes were being written, %d of them answered 503: the others were not held", answered, refused.Load())
		}
		if grown > RefusalBytesMax {
			t.Errorf("%d requests waiting on their connections hold %d bytes of the heap, past RefusalBytesMax", n, grown)
		}
		if bytes > RefusalBytesMax {
			t.Errorf("the lanes and their waiting requests hold %d bytes of the budget", bytes)
		}
		if refused.Load() == 0 {
			t.Error("no waiting request was answered 503 past the budget")
		}
	})
	t.Run("the budget", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		entered, release := pausing(w, g.Connection)
		defer release()
		first := make(chan error, 1)
		go func() {
			first <- s.Refuse(context.Background(), g, Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody})
		}()
		<-entered
		r := &reading{g: g, op: OperationSearch, conn: &connDoc{}}
		rc := s.receipt(r, receipts.KindRefused)
		rc.Held = heldPage()
		rc.Refused = &receipts.Refused{Reason: receipts.RefusedModelUnavailable}
		size, err := receipts.Size(rc)
		if err != nil {
			t.Fatal(err)
		}
		size += refusalWaitSize
		_, held := waiting(s, g.Connection)
		s.refusals.mu.Lock()
		s.refusals.bytesMax = held + 2*size + size/2
		s.refusals.mu.Unlock()
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() {
				if err := refuseHeld(context.Background(), s, g); err != nil {
					t.Error(err)
				}
			})
		}
		eventually(func() bool { n, _ := waiting(s, g.Connection); return n == 2 })
		if _, b := waiting(s, g.Connection); b != held+2*size {
			t.Fatalf("two waiting requests hold %d bytes, want %d", b-held, 2*size)
		}
		if err := refuseHeld(context.Background(), s, g); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("a request past the budget: %v", err)
		}
		release()
		wg.Wait()
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		if got := len(w.rcpt.of(receipts.KindRefused)); got != 3 {
			t.Fatalf("%d refused receipts, want the first and the two that waited", got)
		}
		toTheNextWindow(w)
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		if got := w.rcpt.of(receipts.KindRefusals); len(got) != 0 {
			t.Fatal("the request answered 503 was counted")
		}
		if _, b := waiting(s, g.Connection); b != 0 {
			t.Fatalf("%d bytes held once nothing waits nor is left", b)
		}
	})
	t.Run("a deadline", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		s.refusals.mu.Lock()
		s.refusals.waitMax = 20 * time.Millisecond
		s.refusals.mu.Unlock()
		entered, release := pausing(w, g.Connection)
		defer release()
		first := make(chan error, 1)
		go func() {
			first <- s.Refuse(context.Background(), g, Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody})
		}()
		<-entered
		waited := make(chan error, 1)
		go func() {
			waited <- s.Refuse(context.Background(), g, Refusal{Operation: OperationSearch, Reason: receipts.RefusedInvalidBody})
		}()
		select {
		case err := <-waited:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a request waited past the lanes' waitMax")
		}
		release()
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		toTheNextWindow(w)
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		if n, rec := counted(w.rcpt.of(receipts.KindRefusals)); n != 1 || rec != 1 {
			t.Fatalf("counted %d, recorded %d; want the request that waited counted", n, rec)
		}
	})
}

// H44 (second review, finding 3): a refusal that waited for its
// connection's write reads its account's standing now before it writes its
// own receipt. Gone, it writes nothing and the lane is dropped, so requests
// admitted before the account's removal write nothing after its last sweep;
// unreadable, it is counted, not written; open, it is written.
func TestH44AQueuedRefusalReadsTheStandingBeforeItsWrite(t *testing.T) {
	t.Run("gone, past the last sweep", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		real := receipts.New(w.mem, w.keys, w.clock.now, nil)
		entered, release := make(chan struct{}), make(chan struct{})
		var first sync.Once
		removedAt := w.clock.now()
		var after atomic.Int64
		swept := false
		var removed atomic.Bool
		s.cfg.Receipts = writerFunc(func(ctx context.Context, a string, r *receipts.Receipt) error {
			own := false
			first.Do(func() { own = true })
			if own {
				if err := real.Write(ctx, a, r); err != nil {
					return err
				}
				close(entered)
				<-release
				return nil
			}
			if removed.Load() {
				after.Add(1)
			}
			// Each write is bounded and stored, but takes four seconds of
			// the service's clock, less than ReceiptWriteMax.
			w.clock.add(4 * time.Second)
			if !swept && !w.clock.now().Before(removedAt.Add(receipts.RemoveAgainAfter)) {
				if err := w.mem.DeletePartition(ctx, receipts.Partition(a)); err != nil {
					return err
				}
				swept = true
			}
			return real.Write(ctx, a, r)
		})
		one := make(chan error, 1)
		go func() {
			one <- s.Refuse(context.Background(), g, Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody})
		}()
		<-entered
		const queued = 170
		var wg sync.WaitGroup
		var done atomic.Int64
		for range queued {
			wg.Go(func() {
				if err := s.Refuse(context.Background(), g, Refusal{Operation: OperationRead, Reason: receipts.RefusedOutsideScope}); err != nil {
					t.Error(err)
				}
				done.Add(1)
			})
		}
		eventually(func() bool {
			n, _ := waiting(s, g.Connection)
			return int(done.Load())+n == queued
		})
		// Every request was admitted while the account was open; it is
		// removed now.
		w.accts.set(g.Account, StandingGone)
		removed.Store(true)
		if err := w.mem.DeletePartition(context.Background(), receipts.Partition(g.Account)); err != nil {
			t.Fatal(err)
		}
		close(release)
		if err := <-one; err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		if err := s.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !swept {
			w.clock.add(removedAt.Add(receipts.RemoveAgainAfter).Sub(w.clock.now()))
			if err := w.mem.DeletePartition(context.Background(), receipts.Partition(g.Account)); err != nil {
				t.Fatal(err)
			}
		}
		if n := after.Load(); n != 0 {
			t.Fatalf("%d receipts begun after the account's removal", n)
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
		entered, release := pausing(w, g.Connection)
		defer release()
		first := make(chan error, 1)
		go func() {
			first <- s.Refuse(context.Background(), g, Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody})
		}()
		<-entered
		var wg sync.WaitGroup
		for range 3 {
			wg.Go(func() { refuseOnce(t, s, g, OperationSearch, receipts.RefusedInvalidBody) })
		}
		eventually(func() bool { n, _ := waiting(s, g.Connection); return n == 3 })
		w.accts.mu.Lock()
		w.accts.fail = errors.New("the accounts cannot be read")
		w.accts.mu.Unlock()
		release()
		wg.Wait()
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		if got := len(w.rcpt.of(receipts.KindRefused)); got != 1 {
			t.Fatalf("%d refused receipts written without the account's standing", got-1)
		}
		w.accts.mu.Lock()
		w.accts.fail = nil
		w.accts.mu.Unlock()
		toTheNextWindow(w)
		if err := s.FlushRefusals(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		if n, rec := counted(w.rcpt.of(receipts.KindRefusals)); n != 3 || rec != 1 {
			t.Fatalf("counted %d, recorded %d; want the three that waited counted", n, rec)
		}
	})
	t.Run("open", func(t *testing.T) {
		w, s, g := refusalsWorld(t)
		entered, release := pausing(w, g.Connection)
		defer release()
		first := make(chan error, 1)
		go func() {
			first <- s.Refuse(context.Background(), g, Refusal{Operation: OperationRead, Reason: receipts.RefusedInvalidBody})
		}()
		<-entered
		var reads atomic.Int64
		w.accts.mu.Lock()
		w.accts.hook = func(string) { reads.Add(1) }
		w.accts.mu.Unlock()
		var wg sync.WaitGroup
		for range 3 {
			wg.Go(func() { refuseOnce(t, s, g, OperationSearch, receipts.RefusedInvalidBody) })
		}
		eventually(func() bool { n, _ := waiting(s, g.Connection); return n == 3 })
		release()
		wg.Wait()
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		if got := len(w.rcpt.of(receipts.KindRefused)); got != 4 {
			t.Fatalf("%d refused receipts, want the first and the three that waited", got)
		}
		if got := reads.Load(); got != 3 {
			t.Fatalf("the standing was read %d times, want once for each request that waited", got)
		}
	})
}
