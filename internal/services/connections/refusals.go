package connections

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/services/receipts"
)

// A connection's refusals in a window (ADR 0031, "Refusals past a window's
// bound"; the owner's decision 1 of 2026-11-17). Every refusal of an
// attributed request was one receipt write, at whatever rate a vendor with
// a valid token asked; now each enclave writes at most RefusalsPerWindow
// refused receipts a connection and window, and counts the others, by
// operation and reason, in one refusals receipt for the window, written
// when the window closes (FlushRefusals, every RefusalWindow and at a stop)
// or on the connection's next refusal after it, before that refusal's own.
// A refusal past the bound is answered as it would be with its receipt.
//
// The count is each enclave's, in its memory: with two enclaves a
// connection has at most twice the bound, and two summaries, a window. What
// a crash loses is what was counted and not yet written: the window still
// open (at most RefusalWindow of refusals past the bound), and a closed
// window's while the storage refuses its summary; the requests it counted
// were refused, so nothing of the mail is unrecorded, only how often a
// vendor was refused past the bound. A too_late refusal is never counted:
// it names the receipt of what was then not released, and follows one.

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
)

// refusalKey is what a summary counts by.
type refusalKey struct {
	op     Operation
	reason receipts.RefusedReason
}

// refusalWindow is one connection's window in this enclave.
type refusalWindow struct {
	account  string
	head     receipts.Receipt // the summary's attribution: connection, client, vendor, resource
	start    time.Time
	recorded int
	counts   map[refusalKey]int
	// due is what closed windows counted and is not written yet.
	due *receipts.Refusals
}

// refusalWindows are this enclave's windows, by connection.
type refusalWindows struct {
	mu sync.Mutex
	by map[string]*refusalWindow
}

// summary is a summary to write for account.
type summary struct {
	conn    string
	account string
	head    receipts.Receipt
	r       *receipts.Refusals
}

// close moves what w counted into its due summary, which then ends at
// until. A window that counted nothing makes none, and adds its recorded
// refusals only to one already due (whose span it then is part of).
func (w *refusalWindow) close(until time.Time) {
	if len(w.counts) == 0 && w.due == nil {
		w.recorded = 0
		return
	}
	if w.due == nil {
		w.due = &receipts.Refusals{From: w.start}
	}
	w.due.Until = until
	w.due.Recorded += w.recorded
	for k, n := range w.counts {
		w.due.Counts = addCount(w.due.Counts, k, n)
	}
	w.recorded, w.counts = 0, nil
}

// take is w's due summary, which w no longer holds; nil for none.
func (w *refusalWindow) take(conn string) *summary {
	if w.due == nil {
		return nil
	}
	sm := &summary{conn: conn, account: w.account, head: w.head, r: w.due}
	w.due = nil
	return sm
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

// admitRefusal counts one refusal of r's request: whether it is written in its own
// receipt, and a summary of the connection's earlier windows to write
// before it.
func (s *Service) admitRefusal(r *reading, reason receipts.RefusedReason) (bool, *summary) {
	now := s.now()
	ws := &s.refusals
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.by == nil {
		ws.by = map[string]*refusalWindow{}
	}
	conn := r.g.Connection
	w := ws.by[conn]
	if w == nil {
		head := s.receipt(r, receipts.KindRefusals)
		head.Device, head.Formulas, head.Rules = "", nil, nil
		w = &refusalWindow{account: r.g.Account, head: *head, start: now.Truncate(RefusalWindow)}
		ws.by[conn] = w
	}
	if end := w.start.Add(RefusalWindow); !now.Before(end) {
		w.close(end)
		w.start = now.Truncate(RefusalWindow)
	}
	own := w.recorded < RefusalsPerWindow
	if own {
		w.recorded++
	} else {
		if w.counts == nil {
			w.counts = map[refusalKey]int{}
		}
		w.counts[refusalKey{r.op, reason}]++
	}
	return own, w.take(conn)
}

// putBack keeps sm, whose write failed, for the connection's next try: it
// is merged with what became due since, so nothing it counted is lost.
func (s *Service) putBack(sm *summary) {
	ws := &s.refusals
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.by == nil {
		ws.by = map[string]*refusalWindow{}
	}
	w := ws.by[sm.conn]
	if w == nil {
		w = &refusalWindow{account: sm.account, head: sm.head, start: sm.r.Until}
		ws.by[sm.conn] = w
	}
	if w.due == nil {
		w.due = sm.r
		return
	}
	d := w.due
	if sm.r.From.Before(d.From) {
		d.From = sm.r.From
	}
	if sm.r.Until.After(d.Until) {
		d.Until = sm.r.Until
	}
	d.Recorded += sm.r.Recorded
	for _, c := range sm.r.Counts {
		d.Counts = addCount(d.Counts, refusalKey{Operation(c.Operation), c.Reason}, c.Count)
	}
}

// writeSummary writes sm under ReceiptWriteMax, and keeps it when it
// cannot be written.
func (s *Service) writeSummary(ctx context.Context, sm *summary) error {
	rc := sm.head
	rc.Refusals = sm.r
	if err := s.writeReceipt(ctx, sm.account, &rc, time.Time{}); err != nil {
		s.count("receipt_failed")
		s.putBack(sm)
		return err
	}
	return nil
}

// FlushRefusals writes the summary of every connection's window that has
// closed, and of those whose write failed before; with final (the enclave
// stopping), of the windows still open too, up to now. The composition
// calls it every RefusalWindow and once at a stop, after the last request.
// A summary that cannot be written is kept for the next try, and the
// error is returned.
func (s *Service) FlushRefusals(ctx context.Context, final bool) error {
	now := s.now()
	ws := &s.refusals
	ws.mu.Lock()
	var due []*summary
	for conn, w := range ws.by {
		if end := w.start.Add(RefusalWindow); final || !now.Before(end) {
			if final && now.Before(end) {
				end = now
			}
			w.close(end)
			if sm := w.take(conn); sm != nil {
				due = append(due, sm)
			}
			delete(ws.by, conn)
		} else if sm := w.take(conn); sm != nil {
			due = append(due, sm)
		}
	}
	ws.mu.Unlock()
	var errs []error
	for _, sm := range due {
		if err := s.writeSummary(ctx, sm); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
