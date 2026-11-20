package notices_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/services/notices"
	"github.com/permaily-org/permaily/internal/services/notices/noticestest"
	"github.com/permaily-org/permaily/internal/store"
)

// The code review of the queue (PR #194): its three findings, each as the
// counterexample that showed it.

// slowMark answers the nth read of a mark as it was when it was read, but
// only after then ran: a response that arrived late.
type slowMark struct {
	*store.Memory
	mu    sync.Mutex
	reads int
	nth   int
	then  func()
}

func (s *slowMark) GetEntry(c context.Context, partition, key string) (*store.Entry, error) {
	e, err := s.Memory.GetEntry(c, partition, key)
	if partition != notices.PartitionSent {
		return e, err
	}
	s.mu.Lock()
	s.reads++
	late := s.reads == s.nth
	s.mu.Unlock()
	if late {
		s.then()
	}
	return e, err
}

// Review, finding 1: an attempt whose read of the mark answers only after
// its lease ended, once the other enclave took the lease, sent the notice,
// marked it and deleted it, sends nothing: the attempt's deadline runs from
// before its lease, and it checks it still holds the lease before it sends.
func TestReview1ASlowReadOfTheMarkPastTheLeaseSendsNothing(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	var b *notices.Service
	slow := &slowMark{Memory: w.mem, nth: 2} // the first is Enqueue's
	slow.then = func() {
		w.clock.Advance(notices.LeaseFor)
		must(t, b.Drain(ctx))
	}
	w.entries = slow
	a := w.enclave(d, nil)
	w.entries = w.mem
	b = w.enclave(d, nil)
	n := noticestest.Sample("rcp_1")
	must(t, a.Enqueue(ctx, n))
	must(t, notices.Wait(ctx, a))
	if got := count(d); got[n.Key] != 1 {
		t.Fatalf("delivered %d times, want once", got[n.Key])
	}
	if sa, sb := a.Status(), b.Status(); sa.Sent != 0 || sb.Sent != 1 || w.markOf(n.Key) != "sent" {
		t.Fatalf("status %+v and %+v, mark %q: want the other enclave's send alone", sa, sb, w.markOf(n.Key))
	}
}

// Review, finding 1: an attempt whose writes and reads took it past the
// notice's signed window sends nothing.
func TestReview1ASlowReadPastTheWindowSendsNothing(t *testing.T) {
	w := newWorld(t)
	d := &noticestest.Recorder{}
	// Sendable for 30 seconds more; the read answers 40 seconds later,
	// within AttemptFor and the lease.
	n := sampleNow(w, "rcp_1")
	n.At = n.At.Add(-notices.RetryFor + 30*time.Second)
	n.Facts.EndsAt = n.At.AddDate(0, 0, 90)
	slow := &slowMark{Memory: w.mem, nth: 1, then: func() { w.clock.Advance(40 * time.Second) }}
	w.entries = slow
	q := w.enclave(d, nil)
	w.put(q, notices.Entry{Notice: n})
	must(t, q.Drain(ctx))
	if d.Tries() != 0 {
		t.Fatal("an attempt past the notice's window sent it")
	}
}

// pageCounter keeps what one read of the queue costs: the rows the storage
// read for it, the expired ones included, and the bytes it returned.
type pageCounter struct {
	*store.Memory
	mu        sync.Mutex
	pass      int // rows read since reset
	maxRows   int // most rows returned by one read
	maxBytes  int // most value bytes returned by one read
	listCalls int
}

func (p *pageCounter) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pass = 0
}

func (p *pageCounter) note(read int, es []store.Entry) {
	n := 0
	for _, e := range es {
		n += len(e.Value)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pass += read
	p.maxRows = max(p.maxRows, len(es))
	p.maxBytes = max(p.maxBytes, n)
}

// ListEntries counts what DynamoDB reads for the listing: every row after
// after, expired or not, up to the last one returned when the listing was
// full.
func (p *pageCounter) ListEntries(c context.Context, partition, prefix, after string, limit int) ([]store.Entry, error) {
	out, err := p.Memory.ListEntries(c, partition, prefix, after, limit)
	if partition != notices.PartitionQueue || err != nil {
		return out, err
	}
	read := 0
	for _, e := range p.AllEntries() {
		if e.Partition == partition && strings.HasPrefix(e.Key, prefix) && e.Key > after && (len(out) < limit || e.Key <= out[len(out)-1].Key) {
			read++
		}
	}
	p.mu.Lock()
	p.listCalls++
	p.mu.Unlock()
	p.note(read, out)
	return out, nil
}

func (p *pageCounter) ScanEntriesDown(c context.Context, partition, prefix, before string, maxRead int) (*store.EntryScan, error) {
	out, err := p.Memory.ScanEntriesDown(c, partition, prefix, before, maxRead)
	if partition == notices.PartitionQueue && err == nil {
		p.note(out.Read, out.Entries)
	}
	return out, err
}

// Review, finding 2: a queue full of values far past EntryMax, as whoever
// writes the table can make it, is read a bounded page at a time: no read
// returns more than store.MaxScanRead rows, so a pass never holds more than
// one page of them.
func TestReview2APassHoldsOneBoundedPageOfOversizedValues(t *testing.T) {
	w := newWorld(t)
	p := &pageCounter{Memory: w.mem}
	w.entries = p
	d := &noticestest.Recorder{}
	q := w.enclave(d, nil)
	const size = 8 << 10
	for i := range 3 * store.MaxScanRead {
		w.mem.SetEntry(store.Entry{Partition: notices.PartitionQueue, Key: fmt.Sprintf("%032x", i), Value: bytes.Repeat([]byte{'x'}, size), Version: 1})
	}
	must(t, q.Drain(ctx))
	if p.maxRows > store.MaxScanRead || p.maxBytes > store.MaxScanRead*size {
		t.Fatalf("one read returned %d rows, %d bytes: want at most a page (%d rows)", p.maxRows, p.maxBytes, store.MaxScanRead)
	}
	if st := q.Status(); st.Refused != 3*store.MaxScanRead || d.Tries() != 0 {
		t.Fatalf("status %+v, want every oversized entry refused and none sent", st)
	}
}

// Review, finding 3: rows that expired but the storage has not deleted
// yet, more than ListMax of them before a live notice, count against a
// pass's bound as any row read does; the next pass goes on past them and
// finds the notice.
func TestReview3ExpiredRowsCountAgainstListMax(t *testing.T) {
	w := newWorld(t)
	p := &pageCounter{Memory: w.mem}
	w.entries = p
	d := &noticestest.Recorder{}
	q := w.enclave(d, nil)
	past := w.clock.Now().Add(-1)
	for i := range notices.ListMax + 904 {
		// Keys above every notice's, so the scan from the greatest key
		// reads them first; Memory.ListEntries hides them, as DynamoDB's
		// helper did, though it reads them.
		w.mem.SetEntry(store.Entry{Partition: notices.PartitionQueue, Key: fmt.Sprintf("g%031x", i), Value: []byte("{}"), Version: 1, ExpiresAt: past})
	}
	n := noticestest.Sample("rcp_live")
	w.put(q, notices.Entry{Notice: n})
	for pass := 1; pass <= 3 && d.Tries() == 0; pass++ {
		p.reset()
		must(t, notices.Pass(ctx, q))
		if p.pass > notices.ListMax {
			t.Fatalf("pass %d read %d rows, more than ListMax (%d)", pass, p.pass, notices.ListMax)
		}
	}
	if got := count(d); got[n.Key] != 1 {
		t.Fatalf("delivered %v, want the live notice once", got)
	}
}
