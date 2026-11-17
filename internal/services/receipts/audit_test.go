package receipts

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/store"
)

const third = "acc_cccccccccccccccccccccccccc"

// day is the n-th day after start, when a pass is due again.
func day(n int) time.Time { return start.Add(time.Duration(n) * AuditEvery) }

// L18: one pass across the catalogue finds each kind and counts it; a
// catalogue entry lost is counted and its accounts are not taken as
// checked; an account missing from the accounts' list is audited all the
// same.
func TestL18OnePassFindsEveryKind(t *testing.T) {
	w := newChainWorld(t)
	s := w.enclave()
	a := w.writes(s, account, 3)
	b := w.writes(s, other, 3)
	w.writes(s, third, 2)
	w.writes(s, "acc_dddddddddddddddddddddddddd", 2)
	w.deleteReceipt(account, a[0])
	changed := w.entry(Partition(other), b[1])
	changed.Value = append(changed.Value[:len(changed.Value)-3:len(changed.Value)-3], '"', '}')
	w.mem.SetEntry(changed)
	w.deleteLink(third, 1)
	w.mc.Fork(LinkName(third, 2), 1, sha256.Sum256([]byte("x")))
	// The fourth account's catalogue entry is lost: its chain is not
	// checked, and that is counted.
	for _, e := range w.mem.AllEntries() {
		if e.Partition == CataloguePartition && strings.HasPrefix(e.Key, fmt.Sprintf("%s%016x@", CatalogueKeyPrefix, 4)) {
			if err := w.mem.DeleteEntry(context.Background(), e.Partition, e.Key); err != nil {
				t.Fatal(err)
			}
		}
	}
	w.clock.set(day(1))
	p := w.audit(w.enclave())
	want := map[string]int64{"receipt_missing": 1, "receipt_unreadable": 1, "link_lost": 1, "link_forked": 1, "catalogue_lost": 1}
	for k, n := range p.Audit.Broken {
		if n != want[k] {
			t.Fatalf("%s: %d (%+v)", k, n, p.Audit.Broken)
		}
	}
	if p.Audit.Chains != 3 || p.Audit.Catalogue != 4 || p.Audit.Removed != 0 {
		t.Fatalf("%+v", p.Audit)
	}
	if err := w.status(s); err == nil {
		t.Fatal("a status with findings passed")
	}
	// Both enclaves report the same newest pass.
	got, err := w.enclave().Audited(context.Background())
	if err != nil || got.P != p.P || !got.Audit.Began.Equal(p.Audit.Began) {
		t.Fatalf("%+v %v", got, err)
	}
}

// L18: a pass stopped by an error is not recorded and begins again; a chain
// whose window holds more than AuditWindowRefsMax refs fails the pass.
func TestL18APassStoppedIsNotRecorded(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	s := w.enclave()
	w.writes(s, account, 2)
	w.clock.set(day(1))
	w.mem.Interrupt = func(_ context.Context, step store.Step, partition, key string) error {
		if step == store.StepRead && partition == Partition(account) && key == IDPrefix {
			return errBoom
		}
		return nil
	}
	if p, err := s.Audit(ctx); !errors.Is(err, errBoom) || p != nil {
		t.Fatalf("%+v %v", p, err)
	}
	if p, err := s.Audited(ctx); err != nil || p != nil {
		t.Fatalf("a pass stopped was recorded: %+v %v", p, err)
	}
	w.mem.Interrupt = nil
	if p := w.audit(s); p.P != 1 {
		t.Fatal(p.P)
	}
	// Not due again within a day.
	w.clock.set(day(2).Add(-time.Second))
	if p, err := s.Audit(ctx); err != nil || p != nil {
		t.Fatalf("%+v %v", p, err)
	}

	// A chain whose window holds too many refs.
	w = newChainWorld(t)
	s = w.enclave()
	w.write(s, account)
	l := s.chain.appender(s, account)
	key, _ := s.chainKey(ctx)
	next := 0
	for range AuditWindowRefsMax/LinkReceiptsMax + 1 {
		l.write.Lock()
		_, err := l.putLocked(ctx, key, func(n uint64, at time.Time, prev *Digest, br BreakReason) []byte {
			refs := make([]Ref, LinkReceiptsMax)
			for i := range refs {
				next++
				refs[i] = Ref{ID: fmt.Sprintf("rcp_%016x%020x", start.UnixNano()+int64(next), next)}
			}
			return mustJSON(&Link{V: LinkVersion, N: n, Kind: LinkReceipts, At: at, Prev: prev, Break: br, Receipts: refs})
		}, nil)
		l.write.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	w.clock.set(day(1))
	if _, err := w.enclave().Audit(ctx); !errors.Is(err, errWindow) {
		t.Fatal(err)
	}
}

// L18, R1: what a pass found outlives it: a receipt deleted, found and
// recorded, then put back with a clean pass after; an enclave restarted;
// the account removed. In each the record of incidents stays above zero
// and the status fails.
func TestL18AnIncidentOutlivesTheReceiptPutBack(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	s := w.enclave()
	ids := w.writes(s, account, 3)
	w.clock.set(day(1))
	clean := w.audit(s)
	if err := w.status(s); err != nil {
		t.Fatal(err)
	}
	kept := w.entry(Partition(account), ids[1])
	w.deleteReceipt(account, ids[1])
	w.clock.set(day(2))
	found := w.audit(s)
	if found.Audit.Broken["receipt_missing"] != 1 || w.incidents(s) != 1 {
		t.Fatalf("%+v", found.Audit)
	}
	w.mem.SetEntry(kept)
	w.clock.set(day(3))
	if p := w.audit(s); p.Audit.Broken["receipt_missing"] != 0 {
		t.Fatalf("%+v", p.Audit)
	}
	r := w.enclave()
	if err := w.status(r); err == nil || w.incidents(r) != 1 {
		t.Fatal("a clean pass took an incident back")
	}
	if err := w.mem.DeletePartition(ctx, Partition(account)); err != nil {
		t.Fatal(err)
	}
	w.accounts.remove(account)
	if err := w.status(w.enclave()); err == nil {
		t.Fatal("removing the account took an incident back")
	}
	_ = clean
}

// readsOf counts the reads of receipts by their keys, one at a time.
func readsOf(w *chainWorld) *atomic.Int64 {
	var n atomic.Int64
	w.mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
		if step == store.StepRead && ValidID(key) {
			n.Add(1)
		}
		return nil
	}
	return &n
}

// L18, R6: a healthy pass reads no receipt by its key; a receipt deleted
// from the table costs one read before it is missing; a receipt stored
// behind the cursor is found by that read and is not missing.
func TestL18WhatAPassReads(t *testing.T) {
	w := newChainWorld(t)
	s := w.enclave()
	ids := w.writes(s, account, 150)
	reads := readsOf(w)
	w.clock.set(day(1))
	w.audit(s)
	if reads.Load() != 0 {
		t.Fatalf("a healthy pass read %d receipts by key", reads.Load())
	}
	w.deleteReceipt(account, ids[3])
	w.clock.set(day(2))
	reads = readsOf(w)
	if p := w.audit(s); p.Audit.Broken["receipt_missing"] != 1 || reads.Load() != 1 {
		t.Fatalf("%d reads: %+v", reads.Load(), p.Audit.Broken)
	}
	// Deleted before the pass and stored again while its second page is
	// read: behind the cursor, found by its read.
	kept := w.entry(Partition(account), ids[140])
	w.deleteReceipt(account, ids[140])
	w.mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
		if step == store.StepRead && key == ids[140] {
			w.mem.SetEntry(kept)
		}
		return nil
	}
	w.clock.set(day(3))
	if p := w.audit(w.enclave()); p.Audit.Broken["receipt_missing"] != 1 {
		t.Fatalf("%+v", p.Audit.Broken)
	}
}

// L26: the catalogue. A chain is never begun before its account's entry is
// committed: a crash between the two leaves an entry and no chain, which
// the audit begins. Two enclaves entering at once make entries without a
// gap. A forked entry is counted.
func TestL26TheCatalogue(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	h := w.hooked()
	h.before = func(c store.Commit) error {
		if c.Name == LinkName(account, 1) {
			return errBoom
		}
		return nil
	}
	if err := w.enclave().Write(ctx, account, receipt()); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	h.before = nil
	if err := w.enclave().Write(ctx, account, receipt()); err != nil {
		t.Fatal(err)
	}
	w.clock.set(day(1))
	if p := w.audit(w.enclave()); p.Audit.Chains != 1 || p.Audit.Catalogue != 2 || p.Audit.Recovered != 1 {
		t.Fatalf("%+v", p.Audit)
	}

	// A crash before link 1, and nothing more written: the audit adopts.
	w = newChainWorld(t)
	h = w.hooked()
	h.before = func(c store.Commit) error {
		if c.Name == LinkName(account, 1) {
			return errBoom
		}
		return nil
	}
	if err := w.enclave().Write(ctx, account, receipt()); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	h.before = nil
	w.clock.set(day(1))
	s := w.enclave()
	if p := w.audit(s); p.Audit.Chains != 1 {
		t.Fatalf("%+v", p.Audit)
	}
	if links := walkAll(t, s, account, 10); len(links) != 1 || links[0].Link.Kind != LinkAdopted {
		t.Fatal(shape(links))
	}

	// An account entered whose receipts are all gone is not entered again
	// by every pass.
	w = newChainWorld(t)
	h = w.hooked()
	h.before = func(c store.Commit) error {
		if c.Name == LinkName(account, 1) {
			return errBoom
		}
		return nil
	}
	lost := receipt()
	if err := w.enclave().Write(ctx, account, lost); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	h.before = nil
	for _, e := range w.mem.AllEntries() {
		if e.Partition == Partition(account) {
			w.deleteReceipt(account, e.Key)
		}
	}
	for d := 1; d <= 2; d++ {
		w.clock.set(day(d))
		if p := w.audit(w.enclave()); p.Audit.Catalogue != 1 || p.Audit.Chains != 1 {
			t.Fatalf("%+v", p.Audit)
		}
	}

	// Two enclaves entering at once.
	w = newChainWorld(t)
	a, b := w.enclave(), w.enclave()
	var wg sync.WaitGroup
	for i, acc := range []string{account, other, third, "acc_dddddddddddddddddddddddddd"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := []*Service{a, b}[i%2].Write(ctx, acc, receipt()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	w.clock.set(day(1))
	p := w.audit(w.enclave())
	if p.Audit.Chains != 4 || p.Audit.Catalogue != 4 {
		t.Fatalf("%+v", p.Audit)
	}
	w.mc.Fork(CatalogueName(2), 1, sha256.Sum256([]byte("x")))
	w.clock.set(day(2))
	if p := w.audit(w.enclave()); p.Audit.Broken["catalogue_forked"] != 1 || p.Audit.Chains != 3 {
		t.Fatalf("%+v", p.Audit)
	}
}

// L27: recovery's window. A receipt at RecoverWithin exactly and one a
// year old are unlinked and never recovered, on every pass; a receipt
// named, deleted and put back a year later is ok and never relinked; two
// audits recovering at once give one recovered link per receipt; a link
// above the pass's head that is lost stops recovery for the chain.
func TestL27RecoveryWithinItsWindow(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	s := w.enclave()
	named := w.write(s, account)
	kept := w.entry(Partition(account), named)
	w.clock.set(start.Add(time.Hour))
	exactly := w.unchained(account, 1)[0]
	w.clock.set(start.Add(time.Hour + time.Second))
	w.unchained(account, 1)
	w.clock.set(start.Add(time.Hour + RecoverWithin))
	p := w.audit(s)
	if p.Audit.Unlinked != 1 || p.Audit.Recovered != 1 {
		t.Fatalf("%+v", p.Audit)
	}
	w.deleteReceipt(account, named)
	w.mem.SetEntry(kept)
	w.clock.set(start.Add(365 * 24 * time.Hour))
	p = w.audit(s)
	if p.Audit.Unlinked != 1 || p.Audit.Recovered != 0 || p.Audit.Broken["receipt_missing"] != 0 {
		t.Fatalf("%+v", p.Audit)
	}
	if linked(walkAll(t, s, account, 10))[exactly] != 0 {
		t.Fatal("a receipt past the window was recovered")
	}

	// Two audits at once, their enclaves' clocks under a minute apart.
	w = newChainWorld(t)
	w.write(w.enclave(), account)
	orphans := w.unchained(account, 70)
	w.clock.set(day(1))
	ahead := &clock{at: day(1).Add(30 * time.Second)}
	a := w.enclave()
	b, err := NewChained(w.mem, testKeys{secret: "root"}, ahead.now, w.anchored(), w.accounts)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, s := range []*Service{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Audit(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	links := walkAll(t, w.enclave(), account, 1000)
	ids := linked(links)
	for _, id := range orphans {
		if ids[id] != 1 {
			t.Fatalf("%s linked %d times", id, ids[id])
		}
	}

	// A link above the pass's head that is lost stops recovery.
	w = newChainWorld(t)
	s = w.enclave()
	w.write(s, account)
	w.unchained(account, 1)
	h := w.hooked()
	w.clock.set(day(1))
	writer := w.enclave()
	h.ranged = func() error {
		// Once the pass has read the chain, another enclave links and its
		// link's value is lost.
		if w.accounts.asked > 0 {
			h.ranged = nil
			w.write(writer, account)
			w.deleteLink(account, 2)
		}
		return nil
	}
	if p := w.audit(w.enclave()); p.Audit.Recovered != 0 {
		t.Fatalf("%+v", p.Audit)
	}
}

// L28: the end of the lock. Links whose commits expired and were deleted
// are expired, never ok, their bytes authenticated from the link above,
// and counted expired; a value put back that differs is lost, and below
// it, nothing authenticates an expired link: lost. The head's own commit
// expired, the head found is lower.
func TestL28TheEndOfTheLock(t *testing.T) {
	w, b := newS3World(t)
	s := w.enclave()
	w.writes(s, account, 11)
	for n := uint64(1); n <= 10; n++ {
		b.expire(linkKey(account, n))
	}
	r := w.enclave()
	h, err := r.Head(context.Background(), account)
	if err != nil || h.N != 11 || h.Verdict != LinkOK {
		t.Fatalf("%+v %v", h, err)
	}
	if got := shape(walkAll(t, r, account, 100)); got != "11:ok[ok] 10:expired[ok] 9:expired[ok] 8:expired[ok] 7:expired[ok] 6:expired[ok] 5:expired[ok] 4:expired[ok] 3:expired[ok] 2:expired[ok] 1:expired[ok]" {
		t.Fatal(got)
	}
	w.clock.set(day(1))
	p := w.audit(r)
	if p.Audit.Expired != 10 || p.Audit.Recovered != 0 || p.Audit.Unlinked != 0 {
		t.Fatalf("%+v", p.Audit)
	}
	for _, k := range []string{"link_lost", "link_prev", "receipt_missing"} {
		if p.Audit.Broken[k] != 0 {
			t.Fatalf("%+v", p.Audit.Broken)
		}
	}
	// Link 5's value replaced by an old one that differs.
	k5 := w.valueKeys(account, 5)[0]
	v4 := w.entry(Partition(account), w.valueKeys(account, 4)[0])
	w.deleteLink(account, 5)
	v4.Key = k5
	w.mem.SetEntry(v4)
	if got := shape(walkAll(t, w.enclave(), account, 100)); !strings.HasPrefix(got, "11:ok[ok] 10:expired[ok] 9:expired[ok] 8:expired[ok] 7:expired[ok] 6:expired[ok] 5:lost 4:lost 3:lost") {
		t.Fatal(got)
	}
	// The head's own commit expired.
	b.expire(linkKey(account, 11))
	if h, err := w.enclave().Head(context.Background(), account); err != nil || h.N != 0 {
		t.Fatalf("%+v %v", h, err)
	}
}

// L29: the audit's record. A finished pass is made once, its number found
// by listing; two finishing at once make two; a pass whose value is
// deleted is lost at its number, and the newest one that can be read is
// reported.
func TestL29TheAuditsRecord(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	w.write(w.enclave(), account)
	w.clock.set(day(1))
	// The first pass's commit waits until the second's is under way.
	h := w.hooked()
	var passes atomic.Int64
	second := make(chan struct{})
	h.before = func(c store.Commit) error {
		if strings.HasPrefix(c.Name, "receipt-audit-") {
			switch passes.Add(1) {
			case 1:
				<-second
			case 2:
				close(second)
			}
		}
		return nil
	}
	// Two enclaves whose clocks differ by a second: their passes differ
	// (two that were byte for byte the same would be one record).
	a := w.enclave()
	ahead := &clock{at: day(1).Add(time.Second)}
	b, err := NewChained(w.mem, testKeys{secret: "root"}, ahead.now, w.anchored(), w.accounts)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, s := range []*Service{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Audit(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, err := w.enclave().Audited(ctx)
	if err != nil || got == nil || got.P != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	for _, e := range w.mem.AllEntries() {
		if e.Partition == AuditPartition && strings.HasPrefix(e.Key, fmt.Sprintf("%s%016x@", AuditKeyPrefix, 2)) {
			if err := w.mem.DeleteEntry(ctx, e.Partition, e.Key); err != nil {
				t.Fatal(err)
			}
		}
	}
	r := w.enclave()
	if got, err := r.Audited(ctx); err != nil || got == nil || got.P != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	lost := false
	for _, reg := range r.chain.anchored.Regressions() {
		lost = lost || (reg.Lost && reg.Name == AuditName(2))
	}
	if !lost {
		t.Fatal("a pass lost was not reported")
	}
	for _, e := range w.mem.AllEntries() {
		if e.Partition == AuditPartition {
			if err := w.mem.DeleteEntry(ctx, e.Partition, e.Key); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got, err := w.enclave().Audited(ctx); err != nil || got != nil {
		t.Fatalf("%+v %v", got, err)
	}
	// None readable, a pass is due and the next is 3.
	if p := w.audit(w.enclave()); p.P != 3 {
		t.Fatal(p.P)
	}
}

// failIncidents makes every write of the record of incidents fail.
func failIncidents(h *hookedCommits, fail bool) {
	h.before = func(c store.Commit) error {
		if fail && strings.HasPrefix(c.Name, "receipt-incidents-") {
			return errBoom
		}
		return nil
	}
}

// L31: every reporter records before it reports, and a record that fails
// fails the report and leaves the finding in that enclave's Broken; an
// enclave restarted before recording reports nothing of it; one enclave
// records a finding once since it started; an entry whose value is lost
// still counts.
func TestL31RecordedBeforeReported(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	h := w.hooked()
	s := w.enclave()
	ids := w.writes(s, account, 3)
	w.deleteReceipt(account, ids[0])
	w.deleteLink(account, 3)
	failIncidents(h, true)
	if _, err := s.Walk(ctx, account, Position{}, 10); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	if _, err := s.Export(ctx, account, Position{}); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	if _, err := s.Head(ctx, account); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	// An append about to write a break, in an enclave that did not make the
	// head it finds lost.
	if err := w.enclave().Write(ctx, account, receipt()); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	w.clock.set(day(1))
	if p, err := s.Audit(ctx); !errors.Is(err, errBoom) || p != nil {
		t.Fatal(err)
	}
	if b := s.Broken(); b["receipt_missing"] != 1 || b["link_lost"] != 1 {
		t.Fatalf("%+v", b)
	}
	if b := w.enclave().Broken(); b["receipt_missing"] != 0 {
		t.Fatal("a restarted enclave reports what it did not find")
	}
	failIncidents(h, false)
	if _, err := s.Walk(ctx, account, Position{}, 10); err != nil {
		t.Fatal(err)
	}
	if b := s.Broken(); b["receipt_missing"] != 0 || b["link_lost"] != 0 || w.incidents(s) != 1 {
		t.Fatalf("%+v %d", b, w.incidents(s))
	}
	// Found again by the same enclave: not recorded again.
	if _, err := s.Walk(ctx, account, Position{}, 10); err != nil || w.incidents(s) != 1 {
		t.Fatal(err)
	}
	// Enclaves recording at once: entries without a gap.
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.enclave().record(ctx, []Finding{{Kind: "link_lost", Account: third, N: uint64(i + 1)}}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	n := w.incidents(s)
	if n != 5 || len(w.findings()) != 6 {
		t.Fatalf("%d entries, %d findings", n, len(w.findings()))
	}
	// An entry whose value is lost still counts.
	for _, e := range w.mem.AllEntries() {
		if e.Partition == IncidentPartition {
			if err := w.mem.DeleteEntry(ctx, e.Partition, e.Key); err != nil {
				t.Fatal(err)
			}
		}
	}
	if w.incidents(w.enclave()) != n {
		t.Fatal("a lost entry stopped counting")
	}
	// More findings than one entry holds go in several.
	many := make([]Finding, IncidentFindingsMax+1)
	for i := range many {
		many[i] = Finding{Kind: "link_lost", Account: other, N: uint64(i + 1)}
	}
	if err := s.record(ctx, many); err != nil || w.incidents(s) != n+2 {
		t.Fatal(err)
	}
	s.keys = chainKeyFails{}
	if err := s.record(ctx, []Finding{{Kind: "link_lost", Account: third, N: 1}}); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	if _, err := s.Incidents(ctx); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
}

// L16: removing the account. DeletePartition, twice, removes receipts and
// links' values together; the commits, the catalogue's entry and the
// record of incidents' entries stay; the audit counts the account removed,
// never broken. The audit's fence: a pass that would recover for an
// account whose removal began after it found the orphan writes nothing;
// one whose write begins over a minute after the answer asks again;
// Removing failing fails the pass; old values put back start no write.
func TestL16RemovingTheAccount(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	s := w.enclave()
	ids := w.writes(s, account, 3)
	w.writes(s, other, 1)
	w.deleteReceipt(account, ids[0])
	if _, err := s.Walk(ctx, account, Position{}, 10); err != nil {
		t.Fatal(err)
	}
	var copied []store.Entry
	for _, e := range w.mem.AllEntries() {
		if e.Partition == Partition(account) {
			copied = append(copied, e)
		}
	}
	w.accounts.remove(account)
	for range 2 {
		if err := w.mem.DeletePartition(ctx, Partition(account)); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range w.mem.AllEntries() {
		if e.Partition == Partition(account) {
			t.Fatalf("left: %s", e.Key)
		}
	}
	w.clock.set(day(1))
	p := w.audit(w.enclave())
	if p.Audit.Removed != 1 || p.Audit.Chains != 2 || p.Audit.Broken["link_lost"] != 0 || w.incidents(s) != 1 {
		t.Fatalf("%+v", p.Audit)
	}
	// Old values put back after both sweeps start no write.
	for _, e := range copied {
		w.mem.SetEntry(e)
	}
	w.unchained(account, 1)
	w.clock.set(day(2))
	w.audit(w.enclave())
	if h, err := w.enclave().Head(ctx, account); err != nil || h.N != 3 {
		t.Fatalf("%+v %v", h, err)
	}

	// The fence: the removal begins after the pass found an orphan.
	w = newChainWorld(t)
	s = w.enclave()
	w.write(s, account)
	w.unchained(account, 1)
	w.clock.set(day(1))
	w.accounts.removing = func(acc string) { w.accounts.remove(acc) }
	if p := w.audit(w.enclave()); p.Audit.Recovered != 0 {
		t.Fatalf("%+v", p.Audit)
	}
	if h, _ := w.enclave().Head(ctx, account); h.N != 1 {
		t.Fatal("the audit wrote for an account being removed")
	}

	// A write that would begin over a minute after the answer asks again.
	w = newChainWorld(t)
	h := w.hooked()
	s = w.enclave()
	w.write(s, account)
	w.unchained(account, 1)
	w.clock.set(day(1))
	moved := false
	h.ranged = func() error {
		if w.accounts.asked == 1 && !moved {
			moved = true
			w.clock.set(day(1).Add(2 * AuditWriteAfterCheck))
		}
		return nil
	}
	if p := w.audit(w.enclave()); p.Audit.Recovered != 1 || w.accounts.asked != 2 {
		t.Fatalf("%+v, asked %d", p.Audit, w.accounts.asked)
	}

	// Removing failing fails the pass.
	w = newChainWorld(t)
	s = w.enclave()
	w.write(s, account)
	w.unchained(account, 1)
	w.accounts.fail = errBoom
	w.clock.set(day(1))
	if _, err := s.Audit(ctx); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	if p, _ := s.Audited(ctx); p != nil {
		t.Fatal("a pass was recorded")
	}
}

// L20: what the chain's errors say holds no account, receipt identifier,
// record name of the chain's or digest.
func TestL20ErrorsHoldNothingOfAnAccount(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	h := w.hooked()
	s := w.enclave()
	id := w.write(s, account)
	var errs []error
	h.ranged = func() error { return errBoom }
	_, err := w.enclave().Head(ctx, account)
	errs = append(errs, err, w.enclave().Write(ctx, account, receipt()))
	h.ranged = nil
	h.before = func(store.Commit) error { return errBoom }
	errs = append(errs, w.enclave().Write(ctx, account, receipt()))
	h.before = nil
	w.mem.Interrupt = func(context.Context, store.Step, string, string) error { return errBoom }
	_, err = s.Walk(ctx, account, Position{}, 1)
	errs = append(errs, err)
	w.mem.Interrupt = nil
	w.clock.set(day(1))
	w.accounts.fail = errBoom
	w.unchained(account, 1)
	_, err = s.Audit(ctx)
	errs = append(errs, err)
	for _, err := range errs {
		if err == nil {
			t.Fatal("no error")
		}
		for _, secret := range []string{account, id, "receipt-link", IDPrefix} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("%q holds %q", err, secret)
			}
		}
	}
}
