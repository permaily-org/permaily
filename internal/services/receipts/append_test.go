package receipts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/store"
)

// hookedCommits is the world's store of commits with what a test does
// before an append, after one lands, and to a listing.
type hookedCommits struct {
	*store.MemoryCommits
	before func(c store.Commit) error
	after  func(c store.Commit) error
	ranged func() error
}

func (h *hookedCommits) Append(ctx context.Context, c store.Commit) (*store.Commit, error) {
	if h.before != nil {
		if err := h.before(c); err != nil {
			return nil, err
		}
	}
	stored, err := h.MemoryCommits.Append(ctx, c)
	if err == nil && h.after != nil {
		return nil, h.after(c)
	}
	return stored, err
}

func (h *hookedCommits) HistoryRange(ctx context.Context, from, to string, maxListed int, each func(store.Member) error) error {
	if h.ranged != nil {
		if err := h.ranged(); err != nil {
			return err
		}
	}
	return h.MemoryCommits.HistoryRange(ctx, from, to, maxListed, each)
}

func (w *chainWorld) hooked() *hookedCommits {
	h := &hookedCommits{MemoryCommits: w.mc}
	w.commits = h
	return h
}

// linkKey is the key of account's link n's commit at version 1.
func linkKey(account string, n uint64) string {
	sum := sha256.Sum256([]byte("receipt-link-" + account))
	return "commits/g/" + hex.EncodeToString(sum[:16]) + memberKey(n)
}

// L3: Write stores the entry, then the link, and returns only once the
// link is committed, with the receipt's document what it was before the
// chain; a write that fails at any step after its entry (the chain not
// read, the link's value not staged, its commit refused, its deadline
// ended while its group is in flight) fails, and sets nothing of r.
func TestL3WriteReturnsOnlyOnceItsLinkIsCommitted(t *testing.T) {
	ctx := context.Background()
	// The document is the same chained or not.
	plainWorld, chainedWorld := newChainWorld(t), newChainWorld(t)
	plain := New(plainWorld.mem, testKeys{secret: "root"}, plainWorld.clock.now, plainWorld.random.read)
	chained := chainedWorld.enclave()
	for kind, r := range samples(t) {
		a, b := *r, *r
		if err := plain.Write(ctx, account, &a); err != nil {
			t.Fatal(err)
		}
		if err := chained.Write(ctx, account, &b); err != nil {
			t.Fatal(err)
		}
		if a.ID != b.ID || string(stored(t, &world{mem: plainWorld.mem}, &a)) != string(stored(t, &world{mem: chainedWorld.mem}, &b)) {
			t.Fatalf("%s: a receipt's document changed with the chain", kind)
		}
	}
	links := walkAll(t, chained, account, 100)
	if len(links) != 6 || shape(links) != "6:ok[ok] 5:ok[ok] 4:ok[ok] 3:ok[ok] 2:ok[ok] 1:ok[ok]" {
		t.Fatalf("%s", shape(links))
	}

	failing := map[string]func(w *chainWorld, h *hookedCommits){
		"the chain not read": func(_ *chainWorld, h *hookedCommits) { h.ranged = func() error { return errBoom } },
		"the value not staged": func(w *chainWorld, _ *hookedCommits) {
			w.mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
				if step == store.StepEntry && strings.HasPrefix(key, LinkPrefix) {
					return errBoom
				}
				return nil
			}
		},
		"the commit refused": func(_ *chainWorld, h *hookedCommits) {
			h.before = func(c store.Commit) error {
				if strings.HasPrefix(c.Name, "receipt-link-") {
					return errBoom
				}
				return nil
			}
		},
		"the chain's key": func(w *chainWorld, _ *hookedCommits) {},
	}
	for what, breakIt := range failing {
		t.Run(what, func(t *testing.T) {
			w := newChainWorld(t)
			h := w.hooked()
			w.write(w.enclave(), account)
			breakIt(w, h)
			s := w.enclave()
			if what == "the chain's key" {
				s.keys = chainKeyFails{}
			}
			r := receipt()
			if err := s.Write(ctx, account, r); err == nil || r.ID != "" || !r.At.IsZero() || r.V != 0 {
				t.Fatalf("a write whose link failed: %+v %v", r, err)
			}
			if what == "the chain's key" {
				return
			}
			// Mended, the next write links normally after the first.
			h.ranged, h.before, w.mem.Interrupt = nil, nil, nil
			w.write(s, account)
			if got := shape(walkAll(t, w.enclave(), account, 100)); got != "2:ok[ok] 1:ok[ok]" {
				t.Fatal(got)
			}
		})
	}

	// A waiter whose deadline ends while its group is in flight fails.
	w := newChainWorld(t)
	h := w.hooked()
	s := w.enclave()
	w.write(s, account)
	held, release := make(chan struct{}), make(chan struct{})
	h.before = func(c store.Commit) error {
		if c.Name == LinkName(account, 2) {
			close(held)
			<-release
		}
		return nil
	}
	short, cancel := context.WithTimeout(ctx, time.Hour)
	done := make(chan error)
	go func() { done <- s.Write(short, account, receipt()) }()
	<-held
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
}

// chainKeyFails derives every key but the chain's.
type chainKeyFails struct{}

func (chainKeyFails) Derive(ctx context.Context, purpose string) ([]byte, error) {
	if purpose == ChainPurpose {
		return nil, errBoom
	}
	return testKeys{secret: "root"}.Derive(ctx, purpose)
}

// waiting is how many refs of account wait for a link in s.
func waiting(s *Service, account string) int {
	l := s.chain.appender(s, account)
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.waiting)
}

// until waits for cond, which another goroutine makes true.
func until(t *testing.T, cond func() bool) {
	t.Helper()
	for range 100000 {
		if cond() {
			return
		}
		runtime.Gosched()
		time.Sleep(time.Millisecond / 10)
	}
	t.Fatal("never")
}

// L4: grouping in one enclave, on a played schedule: the first of 200
// writes gets a link of its own while 199 wait, then links of 64, 64, 64
// and 7; past LinkWaitingMax waiting a write fails at once; a waiter
// cancelled before its group leaves fails alone, and one cancelled in a
// group in flight fails and is linked once.
func TestL4TwoHundredWritesAreFiveLinks(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	h := w.hooked()
	s := w.enclave()
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h.before = func(c store.Commit) error {
		if c.Name == LinkName(account, 1) {
			once.Do(func() { close(held) })
			<-release
		}
		return nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	write := func() {
		defer wg.Done()
		errs <- s.Write(ctx, account, receipt())
	}
	wg.Add(1)
	go write()
	<-held
	for range 199 {
		wg.Add(1)
		go write()
	}
	until(t, func() bool { return waiting(s, account) == 199 })
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	links := merged(walkAll(t, w.enclave(), account, 1000))
	var sizes []int
	for _, l := range links {
		if l.Verdict != LinkOK {
			t.Fatalf("link %d %s", l.N, l.Verdict)
		}
		sizes = append(sizes, len(l.Link.Receipts))
	}
	if fmt.Sprint(sizes) != "[7 64 64 64 1]" {
		t.Fatalf("links of %v", sizes)
	}
	for id, n := range linked(links) {
		if n != 1 {
			t.Fatalf("%s linked %d times", id, n)
		}
	}
	if len(linked(links)) != 200 {
		t.Fatal(len(linked(links)))
	}

	// Past LinkWaitingMax waiting, the group in flight not counted.
	w = newChainWorld(t)
	h = w.hooked()
	s = w.enclave()
	w.write(s, account)
	held, release = make(chan struct{}), make(chan struct{})
	once = sync.Once{}
	h.before = func(c store.Commit) error {
		if c.Name == LinkName(account, 2) {
			once.Do(func() { close(held) })
			<-release
		}
		return nil
	}
	results := make(chan error, LinkWaitingMax+2)
	go func() { results <- s.Write(ctx, account, receipt()) }()
	<-held
	for range LinkWaitingMax {
		go func() { results <- s.Write(ctx, account, receipt()) }()
	}
	until(t, func() bool { return waiting(s, account) == LinkWaitingMax })
	if err := s.Write(ctx, account, receipt()); !errors.Is(err, errWaiting) {
		t.Fatalf("past the bound: %v", err)
	}
	// One cancelled while it waits leaves alone.
	cancelled, cancel := context.WithCancel(ctx)
	waited := make(chan error)
	go func() { waited <- s.Write(cancelled, account, receipt()) }()
	until(t, func() bool { return waiting(s, account) == LinkWaitingMax })
	cancel()
	if err := <-waited; !errors.Is(err, errWaiting) && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	for range LinkWaitingMax + 1 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

// L4: a waiter cancelled before its group is taken leaves the queue, the
// others are linked; one cancelled in a group in flight fails, and its ref
// is linked once.
func TestL4ACancelledWaiterFailsAlone(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	h := w.hooked()
	s := w.enclave()
	w.write(s, account)
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h.before = func(c store.Commit) error {
		if c.Name == LinkName(account, 2) {
			once.Do(func() { close(held) })
			<-release
		}
		return nil
	}
	inFlight, cancelFlight := context.WithCancel(ctx)
	flight := make(chan error)
	go func() { flight <- s.Write(inFlight, account, receipt()) }()
	<-held
	queued, cancelQueued := context.WithCancel(ctx)
	left := make(chan error)
	go func() { left <- s.Write(queued, account, receipt()) }()
	stays := make(chan error)
	go func() { stays <- s.Write(ctx, account, receipt()) }()
	until(t, func() bool { return waiting(s, account) == 2 })
	cancelQueued()
	if err := <-left; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	until(t, func() bool { return waiting(s, account) == 1 })
	cancelFlight()
	if err := <-flight; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	if err := <-stays; err != nil {
		t.Fatal(err)
	}
	links := walkAll(t, w.enclave(), account, 100)
	if shape(links) != "3:ok[ok] 2:ok[ok] 1:ok[ok]" {
		t.Fatal(shape(links))
	}
	for id, n := range linked(links) {
		if n != 1 {
			t.Fatalf("%s linked %d times", id, n)
		}
	}
}

// L5: two enclaves over one table and one store of commits, 2,000
// interleaved writes of one account: every receipt in at most one link,
// numbers without gap, no fork, each race lost rebuilt on the next number.
// A write whose group lost LinkTries races fails closed (its answer is a
// 503), and its receipt is in no link.
func TestL5TwoEnclavesInterleaved(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	h := w.hooked()
	var appends atomic.Int64
	h.before = func(store.Commit) error {
		if appends.Add(1)%3 == 0 {
			runtime.Gosched()
		}
		return nil
	}
	enclaves := []*Service{w.enclave(), w.enclave()}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ok   = map[string]bool{}
		wins [2]int
	)
	for g := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				r := receipt()
				err := enclaves[g%2].Write(ctx, account, r)
				mu.Lock()
				switch {
				case err == nil:
					ok[r.ID] = true
					wins[g%2]++
				case !errors.Is(err, errTries):
					t.Error(err)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins[0] == 0 || wins[1] == 0 {
		t.Fatalf("an enclave wrote nothing: %v", wins)
	}
	links := walkAll(t, w.enclave(), account, 1000)
	ids := linked(links)
	stored := 0
	for _, e := range w.mem.AllEntries() {
		if e.Partition != Partition(account) || !strings.HasPrefix(e.Key, IDPrefix) {
			continue
		}
		stored++
		if ids[e.Key] > 1 || (ids[e.Key] == 1) != ok[e.Key] {
			t.Fatalf("%s linked %d times, written %v", e.Key, ids[e.Key], ok[e.Key])
		}
	}
	if stored != 2000 {
		t.Fatal(stored)
	}
	m := merged(links)
	for i, l := range m {
		if l.Verdict != LinkOK || l.N != uint64(len(m)-i) {
			t.Fatalf("link %d: %s", l.N, l.Verdict)
		}
	}
}

// L5 (F1): the late fork. B stages link 3 and its commit is held; A makes
// 3 and 4; a delete marker hides A's commit of 3; B's commit lands. Both
// are told success. B's receipt and B's value of 3 are deleted, and both
// enclaves restart: every walk and the audit say forked at 3, and nothing
// shows a whole, clean, shorter chain. Head stays the newest link's.
func TestL5ALateForkIsShownWhereverItIsRead(t *testing.T) {
	ctx := context.Background()
	w, b := newS3World(t)
	a := w.enclaveOver(w.s3Commits(b))
	w.writes(a, account, 2)
	bEnclave := w.enclaveOver(w.s3Commits(b))
	var first atomic.Bool
	held, release := make(chan struct{}), make(chan struct{})
	b.hold = func(key string) {
		if key == linkKey(account, 3) && first.CompareAndSwap(false, true) {
			close(held)
			<-release
		}
	}
	bDone := make(chan error)
	bReceipt := receipt()
	go func() { bDone <- bEnclave.Write(ctx, account, bReceipt) }()
	<-held
	bValues := w.valueKeys(account, 3)
	if len(bValues) != 1 {
		t.Fatalf("B staged %v", bValues)
	}
	w.writes(a, account, 2)
	b.hide(linkKey(account, 3))
	close(release)
	if err := <-bDone; err != nil {
		t.Fatalf("B was not told success: %v", err)
	}
	w.deleteReceipt(account, bReceipt.ID)
	if err := w.mem.DeleteEntry(ctx, Partition(account), bValues[0]); err != nil {
		t.Fatal(err)
	}
	for i, s := range []*Service{w.enclaveOver(w.s3Commits(b)), w.enclaveOver(w.s3Commits(b))} {
		got := shape(walkAll(t, s, account, 100))
		if got != "4:ok[ok] 3:forked 2:ok[ok] 1:ok[ok]" {
			t.Fatalf("enclave %d: %s", i, got)
		}
		head, err := s.Head(ctx, account)
		if err != nil || head.N != 4 || head.Verdict != LinkOK {
			t.Fatalf("head %+v %v", head, err)
		}
	}
	w.clock.set(start.Add(AuditEvery))
	p := w.audit(w.enclaveOver(w.s3Commits(b)))
	if p.Audit.Broken["link_forked"] != 1 {
		t.Fatalf("%+v", p.Audit.Broken)
	}
	// Each fresh enclave records what it found once; the same finding
	// recorded by several only adds entries.
	for _, f := range w.findings() {
		if f != (Finding{Kind: "link_forked", Account: account, N: 3}) {
			t.Fatalf("%+v", f)
		}
	}
}

// L6: a crash between the entry and the link fails the write; the next
// write links normally; the audit recovers the entry RecoverAfter later in
// a recovered link, counted recovered, and /status passes. A crash after
// the commit and before the answer fails the write, and the receipt is in
// that one link, found again with no second naming it.
func TestL6ACrashLeavesAReceiptToRecover(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	s := w.enclave()
	w.mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
		if step == store.StepEntry && strings.HasPrefix(key, LinkPrefix) {
			return errBoom
		}
		return nil
	}
	lost := receipt()
	if err := s.Write(ctx, account, lost); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	w.mem.Interrupt = nil
	w.write(s, account)
	var orphan string
	for _, e := range w.mem.AllEntries() {
		if e.Partition == Partition(account) && strings.HasPrefix(e.Key, IDPrefix) && linked(walkAll(t, s, account, 10))[e.Key] == 0 {
			orphan = e.Key
		}
	}
	if orphan == "" {
		t.Fatal("no entry left without a link")
	}
	// Before RecoverAfter, the pass leaves it: its write could still link it.
	w.clock.set(start.Add(RecoverAfter - time.Second))
	p := w.audit(s)
	if p.Audit.Recovered != 0 {
		t.Fatal("recovered too early")
	}
	w.clock.set(start.Add(RecoverAfter + AuditEvery))
	p = w.audit(s)
	if p.Audit.Recovered != 1 || p.Audit.Unlinked != 0 {
		t.Fatalf("%+v", p.Audit)
	}
	links := walkAll(t, w.enclave(), account, 10)
	if shape(links) != "2:ok[ok] 1:ok[ok]" && shape(links) != "3:ok[ok] 2:ok[ok] 1:ok[ok]" {
		t.Fatal(shape(links))
	}
	if links[0].Link.Kind != LinkRecovered || links[0].Link.Receipts[0].ID != orphan {
		t.Fatalf("%+v", links[0].Link)
	}
	if err := w.status(s); err != nil {
		t.Fatalf("%v: %+v", err, w.findings())
	}
	// Repeated passes recover nothing twice.
	w.clock.set(start.Add(RecoverAfter + 2*AuditEvery))
	if p := w.audit(s); p.Audit.Recovered != 0 {
		t.Fatal("recovered twice")
	}

	// A crash after the commit and before the answer.
	w = newChainWorld(t)
	h := w.hooked()
	s = w.enclave()
	w.write(s, account)
	h.after = func(c store.Commit) error {
		if c.Name == LinkName(account, 2) {
			return errBoom
		}
		return nil
	}
	answered := receipt()
	if err := s.Write(ctx, account, answered); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	h.after = nil
	w.write(s, account)
	links = walkAll(t, w.enclave(), account, 10)
	if shape(links) != "3:ok[ok] 2:ok[ok] 1:ok[ok]" {
		t.Fatal(shape(links))
	}
	for id, n := range linked(links) {
		if n != 1 {
			t.Fatalf("%s linked %d times", id, n)
		}
	}
}

// L7: on the S3 emulator, a commit whose PUT lands and whose answer is
// lost fails the group's writes, and the group is in that one link; a
// commit whose retry is refused after its first try landed is this
// group's own: the write succeeds at that number with the attempt's bytes,
// and nothing is linked at the next.
func TestL7ACommitWhoseOutcomeIsUnknown(t *testing.T) {
	ctx := context.Background()
	w, b := newS3World(t)
	s := w.enclave()
	w.write(s, account)
	b.lost = func(key string) bool { return key == linkKey(account, 2) }
	if err := s.Write(ctx, account, receipt()); err == nil {
		t.Fatal("a write whose commit's answer was lost succeeded")
	}
	b.lost = nil
	w.write(s, account)
	links := walkAll(t, w.enclave(), account, 10)
	if shape(links) != "3:ok[ok] 2:ok[ok] 1:ok[ok]" {
		t.Fatal(shape(links))
	}
	for id, n := range linked(links) {
		if n != 1 {
			t.Fatalf("%s linked %d times", id, n)
		}
	}

	// The retry refused after the first try landed.
	w = newChainWorld(t)
	h := w.hooked()
	s = w.enclave()
	w.write(s, account)
	h.after = func(c store.Commit) error {
		if c.Name == LinkName(account, 2) {
			return store.ErrExists
		}
		return nil
	}
	id := w.write(s, account)
	h.after = nil
	links = walkAll(t, w.enclave(), account, 10)
	if shape(links) != "2:ok[ok] 1:ok[ok]" || links[0].Link.Receipts[0].ID != id {
		t.Fatal(shape(links))
	}
}
