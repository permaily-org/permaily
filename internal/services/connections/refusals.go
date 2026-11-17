package connections

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/services/receipts"
)

// A connection's refusals in a window (ADR 0031, "Refusals past a window's
// bound"; the owner's decision 1 of 2026-11-17, and the rulings on the
// review of pull request 161). Every refusal of an attributed request was
// one receipt write, at whatever rate a vendor with a valid token asked; now
// each enclave writes at most RefusalsPerWindow refused receipts a
// connection and window, and counts the others, by operation and reason, in
// one refusals receipt for the window.
//
// Each connection refused in this enclave has a lane: its open window (the
// refused receipts it reserved, those stored, and the counts past them) and
// its writes not yet known to be stored, in order. A write gets its identity
// (receipts.NewID) before its first try, so a try whose outcome is unknown
// (the storage's answer lost, a deadline) is made again as the same receipt
// and stored once; nothing in it changes once tried. One goroutine at a time
// writes a lane's receipts (its busy), never under the enclave-wide lock, and
// in order: a refused receipt is written only once every earlier write of its
// lane is stored, so a refusal never overtakes the summary before it. While
// one is not stored, the lane's later refusals are counted, not written each
// in its own receipt, and answered as any refusal past the bound. A write that
// failed is tried again on one schedule (RefusalRetry) that the minute pass
// and the requests share, never at a vendor's request rate. Before a summary
// or a write tried again, the account's standing is read now: a removed
// account's lane is dropped, on every enclave, and one whose standing cannot
// be read is not written (tried again later).
//
// What the lanes hold is bounded for the enclave (RefusalLanesMax lanes,
// RefusalBytesMax bytes, at most two writes a lane): a refusal that would need
// a lane past them is answered ErrUnavailable (503 with Retry-After) and
// counted nowhere, as a refusal whose receipt cannot be written always was;
// nothing already counted is evicted. A refused receipt that would pass the
// bytes is counted instead.
//
// The counts are each enclave's, in its memory: with two enclaves a connection
// has at most twice the bound, and two summaries, a window. What a crash loses
// is every count not yet stored: the open window's, a closed window's until
// the minute pass a moment after it ends writes it, and any the storage has
// not stored yet. The requests it counted were refused, so nothing of the mail
// is unrecorded, only how often a vendor was refused past the bound. A stop
// (Stop) writes them all. A too_late refusal is never counted: it names the
// receipt of what was then not released, and follows one.

const (
	// RefusalsPerWindow is how many refused receipts an enclave writes for
	// one connection in one window. Above what a client within its limits
	// is refused in a minute (a read of a held message is one refusal, and
	// a client told 429 waits), and a flood's writes stay at 31 a minute a
	// connection and enclave, where they were at the vendor's request rate.
	RefusalsPerWindow = 30
	// RefusalWindow is the window: aligned to the clock's minutes, so a
	// summary names a whole minute.
	RefusalWindow = time.Minute
	// RefusalRetry is how soon a lane's write that failed is tried again,
	// by the minute pass or a refusal of its connection, whichever comes
	// first after it: one schedule for both.
	RefusalRetry = RefusalWindow
	// RefusalLanesMax is how many connections an enclave keeps refusals of
	// at once.
	RefusalLanesMax = 10_000
	// RefusalBytesMax bounds the bytes the lanes hold: their attribution,
	// their summaries counted at their largest, and the refused receipts
	// not yet stored.
	RefusalBytesMax = 64 << 20
	// refusalPassWorkers is how many lanes a pass writes at once.
	refusalPassWorkers = 8
	// refusalCountsMax is the bytes of a summary's counts at their largest:
	// every operation with every reason (receipts' H24), with room.
	refusalCountsMax = 4 << 10
)

// refusalKey is what a summary counts by.
type refusalKey struct {
	op     Operation
	reason receipts.RefusedReason
}

// refusalWrite is a lane's receipt not yet known to be stored.
type refusalWrite struct {
	rc receipts.Receipt
	// tried: rc has its identity and was written at least once; nothing
	// in it changes now.
	tried bool
	// single is a refused receipt of window, not a summary; size is what
	// it holds of the budget.
	single bool
	window time.Time
	size   int
}

// refusalLane is one connection's refusals in this enclave.
type refusalLane struct {
	account string
	head    receipts.Receipt // a summary's attribution: connection, client, vendor, resource
	size    int              // what the lane holds of the budget, its writes' own sizes apart
	// The open window: the refused receipts it reserved, those stored, and
	// the refusals counted past them.
	start    time.Time
	reserved int
	recorded int
	counts   map[refusalKey]int
	// queue are its writes not yet stored, in order: a tried one first, then
	// at most a summary not yet tried, which closed windows add to.
	queue []*refusalWrite
	// retry is when queue[0], having failed, may be tried again.
	retry time.Time
	// busy is closed when the goroutine writing the lane's receipts is done;
	// nil when none is.
	busy chan struct{}
}

// refusalLanes are this enclave's lanes, by connection, within its budget.
type refusalLanes struct {
	mu    sync.Mutex
	by    map[string]*refusalLane
	bytes int
	// The budget: RefusalLanesMax and RefusalBytesMax, smaller in tests.
	lanesMax, bytesMax int
}

// admission is the attributed work under way: Stop closes it and waits for
// what is under way, so nothing is counted after the last write.
type admission struct {
	mu     sync.Mutex
	n      int
	closed bool
	idle   chan struct{}
}

// enter admits one attributed request; false once Stop began.
func (a *admission) enter() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false
	}
	a.n++
	return true
}

func (a *admission) leave() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n--
	if a.n == 0 && a.idle != nil {
		close(a.idle)
		a.idle = nil
	}
}

// close admits nothing more and waits for what is under way, until ctx
// ends.
func (a *admission) close(ctx context.Context) error {
	a.mu.Lock()
	a.closed = true
	if a.n == 0 {
		a.mu.Unlock()
		return nil
	}
	if a.idle == nil {
		a.idle = make(chan struct{})
	}
	idle := a.idle
	a.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// addCount adds n refusals of k to cs, kept in order of operation, then
// reason.
func addCount(cs []receipts.RefusalCount, k refusalKey, n int) []receipts.RefusalCount {
	c := receipts.RefusalCount{Operation: string(k.op), Reason: k.reason}
	i, found := slices.BinarySearchFunc(cs, c, compareCounts)
	if !found {
		cs = slices.Insert(cs, i, c)
	}
	cs[i].Count += n
	return cs
}

func compareCounts(a, b receipts.RefusalCount) int {
	return cmp.Or(cmp.Compare(a.Operation, b.Operation), cmp.Compare(a.Reason, b.Reason))
}

// roll closes l's window when now is past it, or, final, up to now.
func (l *refusalLane) roll(now time.Time, final bool) {
	end := l.start.Add(RefusalWindow)
	if now.Before(end) {
		if !final {
			return
		}
		end = now
	}
	l.closeWindow(end)
	l.start = now.Truncate(RefusalWindow)
	if final {
		l.start = now
	}
}

// closeWindow moves what the window counted into the summary not yet
// tried, or a new one, which then ends at until. A window that counted
// nothing past its bound makes none.
func (l *refusalLane) closeWindow(until time.Time) {
	counts, recorded := l.counts, l.recorded
	l.counts, l.recorded, l.reserved = nil, 0, 0
	if len(counts) == 0 {
		return
	}
	sum := l.open()
	if sum == nil {
		h := l.head
		h.Refusals = &receipts.Refusals{From: l.start}
		sum = &refusalWrite{rc: h}
		l.queue = append(l.queue, sum)
	}
	r := sum.rc.Refusals
	r.Until = until
	r.Recorded += recorded
	for k, n := range counts {
		r.Counts = addCount(r.Counts, k, n)
	}
}

// open is the lane's summary not yet tried, nil for none.
func (l *refusalLane) open() *refusalWrite {
	if n := len(l.queue); n > 0 && !l.queue[n-1].single && !l.queue[n-1].tried {
		return l.queue[n-1]
	}
	return nil
}

// stored is one refused receipt of window stored: it is counted as
// recorded by its window, or by the summary its window went into.
// A window that closed counting nothing past its bound has no summary to
// say so.
func (l *refusalLane) stored(window time.Time) {
	if l.start.Equal(window) {
		l.recorded++
		return
	}
	for _, q := range l.queue {
		if r := q.rc.Refusals; r != nil && !q.tried && !window.Before(r.From) && window.Before(r.Until) {
			r.Recorded++
			return
		}
	}
}

// lane is conn's lane, made for r when there is none and the budget has
// room; nil when it has none. ws.mu is held.
func (s *Service) lane(r *reading, now time.Time) *refusalLane {
	ws := &s.refusals
	if l := ws.by[r.g.Connection]; l != nil {
		return l
	}
	head := s.receipt(r, receipts.KindRefusals)
	head.Device, head.Formulas, head.Rules = "", nil, nil
	n, err := receipts.Size(head)
	if err != nil {
		return nil
	}
	// The lane's own attribution, and its two summaries at their largest.
	size := 3*n + 2*refusalCountsMax
	if len(ws.by) >= ws.lanesMax || ws.bytes+size > ws.bytesMax {
		return nil
	}
	l := &refusalLane{account: r.g.Account, head: *head, size: size, start: now.Truncate(RefusalWindow)}
	ws.by[r.g.Connection] = l
	ws.bytes += size
	return l
}

// drop forgets conn's lane and what it held. ws.mu is held.
func (ws *refusalLanes) drop(conn string, l *refusalLane) {
	if ws.by[conn] != l {
		return
	}
	delete(ws.by, conn)
	ws.bytes -= l.size
	for _, q := range l.queue {
		ws.bytes -= q.size
	}
}

// acquire makes the caller the one writing conn's lane, waiting for the
// one writing it unless wait is false or ctx ends; nil when it is not.
func (s *Service) acquire(ctx context.Context, conn string, wait bool) *refusalLane {
	ws := &s.refusals
	for {
		ws.mu.Lock()
		l := ws.by[conn]
		if l == nil {
			ws.mu.Unlock()
			return nil
		}
		if l.busy == nil {
			l.busy = make(chan struct{})
			ws.mu.Unlock()
			return l
		}
		busy := l.busy
		ws.mu.Unlock()
		if !wait {
			return nil
		}
		select {
		case <-busy:
		case <-ctx.Done():
			return nil
		}
	}
}

// release ends the caller's writing of l, dropped or not. ws.mu is held.
func (l *refusalLane) release() {
	close(l.busy)
	l.busy = nil
}

// refuseCounted is refuseWith for a refusal that may be counted: its own
// refused receipt when its window has room and its lane's earlier writes
// are stored, otherwise counted.
func (s *Service) refuseCounted(ctx context.Context, r *reading, rc *receipts.Receipt, err error) error {
	ws := &s.refusals
	key := refusalKey{r.op, refusedReason(rc)}
	count := func(l *refusalLane) error {
		if l.counts == nil {
			l.counts = map[refusalKey]int{}
		}
		l.counts[key]++
		return err
	}
	for {
		now := s.now()
		ws.mu.Lock()
		l := s.lane(r, now)
		if l == nil {
			ws.mu.Unlock()
			s.count("receipt_failed")
			return ErrUnavailable
		}
		l.roll(now, false)
		switch {
		case l.reserved >= RefusalsPerWindow:
			defer ws.mu.Unlock()
			return count(l)
		case l.busy != nil:
			// Another request or the pass is writing the lane: this one's
			// receipt comes after what it writes.
			busy := l.busy
			ws.mu.Unlock()
			<-busy
			continue
		case len(l.queue) > 0 && now.Before(l.retry):
			// An earlier write is not stored, and is not due again yet.
			defer ws.mu.Unlock()
			return count(l)
		}
		l.busy = make(chan struct{})
		ws.mu.Unlock()
		return s.refuseOwn(ctx, r.g.Connection, l, rc, err, count)
	}
}

// refuseOwn writes the lane's earlier writes, and then, when they are
// stored and the window still has room, the refusal's own receipt under
// its identity. The caller is writing l.
func (s *Service) refuseOwn(ctx context.Context, conn string, l *refusalLane, rc *receipts.Receipt, err error, count func(*refusalLane) error) error {
	ws := &s.refusals
	empty := s.writeLane(ctx, conn, l, false)
	now := s.now()
	ws.mu.Lock()
	if ws.by[conn] != l {
		// Dropped: the account is gone.
		defer ws.mu.Unlock()
		l.release()
		return err
	}
	l.roll(now, false)
	var size int
	// A window that closed meanwhile left a summary to write first: this
	// refusal is counted, after it.
	if empty && len(l.queue) == 0 && l.reserved < RefusalsPerWindow {
		if n, serr := receipts.Size(rc); serr == nil && ws.bytes+n <= ws.bytesMax {
			size = n
		}
	}
	if size == 0 {
		defer ws.mu.Unlock()
		l.release()
		return count(l)
	}
	id, ierr := receipts.NewID(now)
	if ierr != nil {
		defer ws.mu.Unlock()
		l.release()
		return count(l)
	}
	rc.ID, rc.At = id, now
	own := &refusalWrite{rc: *rc, tried: true, single: true, window: l.start, size: size}
	l.reserved++
	l.queue = append(l.queue, own)
	ws.bytes += size
	ws.mu.Unlock()
	werr := s.writeReceipt(ctx, l.account, &own.rc, time.Time{})
	ws.mu.Lock()
	defer ws.mu.Unlock()
	s.settle(l, own, werr)
	l.release()
	if werr != nil {
		s.count("receipt_failed")
		return ErrUnavailable
	}
	return err
}

// settle records the outcome of a try of w, l's first write. ws.mu is held.
func (s *Service) settle(l *refusalLane, w *refusalWrite, err error) {
	if err != nil {
		l.retry = s.now().Add(RefusalRetry)
		return
	}
	l.queue = l.queue[1:]
	l.retry = time.Time{}
	s.refusals.bytes -= w.size
	if w.single {
		l.stored(w.window)
	}
}

// writeLane writes l's writes in order, those due (all of them, final),
// until one fails; whether none is left. The caller is writing l. Before
// the first, the account's standing is read now: a gone account's lane is
// dropped, and one that cannot be read is not written.
func (s *Service) writeLane(ctx context.Context, conn string, l *refusalLane, final bool) bool {
	ws := &s.refusals
	checked := false
	for {
		now := s.now()
		ws.mu.Lock()
		if len(l.queue) == 0 {
			ws.mu.Unlock()
			return true
		}
		if !final && now.Before(l.retry) {
			ws.mu.Unlock()
			return false
		}
		w := l.queue[0]
		ws.mu.Unlock()
		if !checked {
			sctx, cancel := context.WithTimeout(ctx, ReceiptWriteMax)
			st, _, err := s.standing(sctx, l.account, true)
			cancel()
			ws.mu.Lock()
			switch {
			case err != nil:
				l.retry = s.now().Add(RefusalRetry)
				ws.mu.Unlock()
				return false
			case st == StandingGone:
				// Its receipts go with it: nothing is left to write.
				ws.drop(conn, l)
				ws.mu.Unlock()
				return true
			}
			ws.mu.Unlock()
			checked = true
		}
		if !w.tried {
			id, err := receipts.NewID(now)
			ws.mu.Lock()
			if err != nil {
				l.retry = s.now().Add(RefusalRetry)
				ws.mu.Unlock()
				return false
			}
			// From here, nothing in it changes: a window that closes
			// goes into a summary after it.
			w.rc.ID, w.rc.At, w.tried = id, now, true
			ws.mu.Unlock()
		}
		err := s.writeReceipt(ctx, l.account, &w.rc, time.Time{})
		if err != nil {
			s.count("receipt_failed")
		}
		ws.mu.Lock()
		s.settle(l, w, err)
		ws.mu.Unlock()
		if err != nil {
			return false
		}
	}
}

// FlushRefusals writes the summary of every connection's window that has
// closed, and every lane's write that failed and is due again; with final
// (Stop), every window still open too, up to now, and every write, due or
// not. The composition calls it a moment after each minute of the clock.
// A lane being written is left to whoever writes it, but final waits for
// it. What cannot be written is kept for the next try, and the error is
// returned; final reports what it leaves.
func (s *Service) FlushRefusals(ctx context.Context, final bool) error {
	now := s.now()
	ws := &s.refusals
	ws.mu.Lock()
	var todo []string
	for conn, l := range ws.by {
		if l.busy == nil {
			l.roll(now, final)
		}
		switch {
		case l.busy == nil && len(l.queue) == 0 && len(l.counts) == 0 && l.reserved == 0:
			// Nothing open nor left: a window with room starts a new lane.
			ws.drop(conn, l)
		case len(l.queue) > 0 && (final || !now.Before(l.retry)), final && l.busy != nil:
			todo = append(todo, conn)
		}
	}
	ws.mu.Unlock()
	slices.Sort(todo)
	work := make(chan string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failed int
	for range min(refusalPassWorkers, len(todo)) {
		wg.Go(func() {
			for conn := range work {
				l := s.acquire(ctx, conn, final)
				if l == nil {
					continue
				}
				if final {
					ws.mu.Lock()
					l.roll(s.now(), true)
					ws.mu.Unlock()
				}
				ok := s.writeLane(ctx, conn, l, final)
				ws.mu.Lock()
				l.release()
				ws.mu.Unlock()
				if !ok {
					mu.Lock()
					failed++
					mu.Unlock()
				}
			}
		})
	}
	for _, conn := range todo {
		select {
		case work <- conn:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(work)
	wg.Wait()
	if final {
		ws.mu.Lock()
		left := 0
		for conn, l := range ws.by {
			if l.busy == nil && len(l.queue) == 0 && len(l.counts) == 0 {
				ws.drop(conn, l)
				continue
			}
			left++
		}
		ws.mu.Unlock()
		if left > 0 {
			return fmt.Errorf("connections: the refusals of %d connections were not written: %w", left, errors.Join(ErrUnavailable, ctx.Err()))
		}
		return nil
	}
	if failed > 0 || ctx.Err() != nil {
		return fmt.Errorf("connections: the refusals of %d connections were not written: %w", failed, errors.Join(ErrUnavailable, ctx.Err()))
	}
	return nil
}

// Stop ends the attributed work of a stopping enclave: no request is
// attributed any more (Search, Read, Attachment and Refuse answer
// ErrUnavailable, recording nothing, as for a request that never came), the
// ones under way are waited for, and then every connection's refusals still
// in memory are written (FlushRefusals, final). The composition stops the
// minute pass first. It fails, and says so, when ctx ends first or anything
// is left unwritten: a stop that loses counts is not a clean one.
func (s *Service) Stop(ctx context.Context) error {
	if err := s.adm.close(ctx); err != nil {
		return fmt.Errorf("connections: requests still under way at the stop: %w", err)
	}
	return s.FlushRefusals(ctx, true)
}
