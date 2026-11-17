package receipts

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

// failing fails the k-th request to the world's table, store of commits
// and keys (0 for none), and counts them.
type failing struct {
	mu   sync.Mutex
	k, n int
}

func (f *failing) step() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	if f.n == f.k {
		return errBoom
	}
	return nil
}

type failingKeys struct{ f *failing }

func (k failingKeys) Derive(ctx context.Context, purpose string) ([]byte, error) {
	if err := k.f.step(); err != nil {
		return nil, err
	}
	return testKeys{secret: "root"}.Derive(ctx, purpose)
}

// failureWorld is a world with a little of everything: chains with a
// receipt deleted, receipts from before the chain of an account that
// writes and of one that does not, a receipt left without a link, a junk
// key among receipts; and every request after it counted.
func failureWorld(t *testing.T) (*chainWorld, *failing) {
	w := newChainWorld(t)
	h := w.hooked()
	s := w.enclave()
	ids := w.writes(s, account, 3)
	w.deleteReceipt(account, ids[1])
	w.unchained(other, 2)
	w.unchained(third, 1)
	w.unchained(account, 1)
	w.mem.SetEntry(store.Entry{Partition: Partition(account), Key: IDPrefix + "junk", Value: []byte("junk")})
	// A chain whose receipts are all gone, and whose link 1's commit
	// expired.
	const fourth = "acc_dddddddddddddddddddddddddd"
	gone := w.writes(s, fourth, 2)
	w.deleteReceipt(fourth, gone[0])
	w.deleteReceipt(fourth, gone[1])
	w.mc.Forget(LinkName(fourth, 1))
	// An account entered and whose link 1 was never made.
	h.before = func(c store.Commit) error {
		if c.Name == LinkName("acc_eeeeeeeeeeeeeeeeeeeeeeeeee", 1) {
			return errBoom
		}
		return nil
	}
	_ = w.enclave().Write(context.Background(), "acc_eeeeeeeeeeeeeeeeeeeeeeeeee", receipt())
	// Two passes, the newest lost.
	key, _ := s.chainKey(context.Background())
	for p := uint64(1); p <= 2; p++ {
		doc := testPass()
		doc.P = p
		doc.Audit.Began = start.Add(-48 * time.Hour)
		if _, _, err := s.create(context.Background(), key, passSeriesOf, p, mustJSON(&doc)); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range w.mem.AllEntries() {
		if e.Partition == AuditPartition && strings.HasPrefix(e.Key, fmt.Sprintf("%s%016x@", AuditKeyPrefix, 2)) {
			w.mem.SetEntry(store.Entry{Partition: e.Partition, Key: e.Key, Value: []byte("gone")})
		}
	}
	w.accounts.list = []string{account, other, third}
	f := &failing{}
	w.mem.Interrupt = func(context.Context, store.Step, string, string) error { return f.step() }
	h.before = func(store.Commit) error { return f.step() }
	h.ranged = f.step
	return w, f
}

// TestEveryFailureIsAnError fails each request of the chain's main flows
// in turn, one at a time: every failure is an error of the call that made
// it (or a call that tolerates it, as a hint does), never a panic, a
// verdict made up or a pass recorded.
func TestEveryFailureIsAnError(t *testing.T) {
	ctx := context.Background()
	for k := 1; ; k++ {
		w, f := failureWorld(t)
		f.k = k
		s, err := NewChained(w.mem, failingKeys{f}, w.clock.now, w.anchored(), w.accounts)
		if err != nil {
			t.Fatal(err)
		}
		s.random = w.random.read
		_ = s.Write(ctx, account, receipt())
		w.clock.set(start.Add(time.Hour))
		_ = s.Write(ctx, other, receipt())
		_, _ = s.Walk(ctx, account, Position{}, 10)
		_, _ = s.Export(ctx, account, Position{})
		_, _ = s.Head(ctx, account)
		w.clock.set(day(1))
		p, err := s.Audit(ctx)
		if (err == nil) == (p == nil) && err == nil {
			t.Fatalf("k=%d: no pass and no error", k)
		}
		_, _ = s.Audited(ctx)
		_, _ = s.Incidents(ctx)
		_, _ = s.Walk(ctx, "acc_dddddddddddddddddddddddddd", Position{}, 10)
		// A continuation at its expired link 1, from link 2's prev.
		_, _ = s.Walk(ctx, "acc_dddddddddddddddddddddddddd", Position{N: 2}, 1)
		f.mu.Lock()
		done := f.n < k
		f.mu.Unlock()
		if done {
			if k < 100 {
				t.Fatalf("only %d requests", k)
			}
			return
		}
	}
}

// The paths left: a page's byte bound at a link and before a link's
// first receipt, junk where receipts and commits are, a link whose digest
// is not the one the link above names, documents of the catalogue, the
// record of incidents and the passes that are not ones, the end of the
// passes' series and races lost on it, the bounds of an enclave's memory.
func TestTheChainsEdges(t *testing.T) {
	ctx := context.Background()

	t.Run("a page's bytes end it at a link and before a link's first receipt", func(t *testing.T) {
		w := newChainWorld(t)
		s := w.enclave()
		ids := w.writes(s, account, 3)
		l := len(w.entry(Partition(account), w.valueKeys(account, 3)[0]).Value)
		r := len(w.entry(Partition(account), ids[2]).Value)
		s.walkBytes = 2*l + r - 1
		p, err := s.Walk(ctx, account, Position{}, 10)
		if err != nil || len(p.Links) != 1 || p.Next != (Position{N: 3}) {
			t.Fatalf("%+v %v", p, err)
		}
		s.walkBytes = 2*l + 2*r - 1
		p, err = s.Walk(ctx, account, Position{}, 10)
		if err != nil || len(p.Links) != 1 || p.Next != (Position{N: 3}) {
			t.Fatalf("%+v %v", p, err)
		}
	})

	t.Run("junk where receipts and commits are", func(t *testing.T) {
		w, b := newS3World(t)
		w.unchained(account, 1)
		w.mem.SetEntry(store.Entry{Partition: Partition(account), Key: IDPrefix + "junk", Value: []byte("junk")})
		w.mem.SetEntry(store.Entry{Partition: Partition(account), Key: IDPrefix + "0junk", Value: []byte("junk")})
		w.mem.SetEntry(store.Entry{Partition: Partition(account), Key: fmt.Sprintf("%s%016x%020d", IDPrefix, start.Add(-time.Hour).UnixNano(), 0), Value: []byte("junk")})
		w.clock.set(start.Add(time.Hour))
		s := w.enclave()
		w.write(s, account)
		b.mu.Lock()
		b.next++
		b.versions = append(b.versions, bver{key: strings.TrimSuffix(linkKey(account, 1), memberKey(1)) + "/zz/1", id: fmt.Sprintf("v%06d", b.next), body: []byte("junk")})
		b.mu.Unlock()
		links := walkAll(t, w.enclave(), account, 10)
		if shape(links) != "2:ok[ok] 1:ok[ok]" || links[1].Link.Kind != LinkAdopted {
			t.Fatal(shape(links))
		}
		w.clock.set(day(1))
		if p := w.audit(w.enclave()); p.Audit.Unlinked+p.Audit.Recovered != 0 || p.Audit.Broken["receipt_unreadable"] != 1 {
			t.Fatalf("%+v", p.Audit)
		}
	})

	t.Run("a link whose digest is not what the next names", func(t *testing.T) {
		w := newChainWorld(t)
		w.writes(w.enclave(), account, 1)
		key, _ := testKeys{secret: "root"}.Derive(ctx, ChainPurpose)
		other := sha256.Sum256([]byte("another link 1"))
		l := Link{V: LinkVersion, N: 2, Kind: LinkReceipts, At: start, Prev: (*Digest)(&other), Receipts: []Ref{{ID: "rcp_" + strings.Repeat("1", 36)}}}
		sealed, _ := envelope.SealText(key, mustJSON(&l), envelope.AAD(ChainPurpose, LinkName(account, 2)))
		if err := w.anchored().CreateNew(ctx, &store.Record{Name: LinkName(account, 2), Value: sealed}); err != nil {
			t.Fatal(err)
		}
		if got := shape(walkAll(t, w.enclave(), account, 10)); got != "2:ok[missing] 1:prev[ok]" {
			t.Fatal(got)
		}
		w.clock.set(day(1))
		if p := w.audit(w.enclave()); p.Audit.Broken["link_prev"] != 1 {
			t.Fatalf("%+v", p.Audit.Broken)
		}
	})

	t.Run("records of the other series that are not ones", func(t *testing.T) {
		w := newChainWorld(t)
		s := w.enclave()
		w.write(s, account)
		key, _ := testKeys{secret: "root"}.Derive(ctx, ChainPurpose)
		for _, name := range []string{CatalogueName(2), IncidentName(1), AuditName(1)} {
			sealed, _ := envelope.SealText(key, []byte(`{"v":1}`), envelope.AAD(ChainPurpose, name))
			if err := w.anchored().CreateNew(ctx, &store.Record{Name: name, Value: sealed}); err != nil {
				t.Fatal(err)
			}
		}
		w.clock.set(day(1))
		p := w.audit(w.enclave())
		if p.P != 2 || p.Audit.Broken["catalogue_unreadable"] != 1 || p.Audit.Catalogue != 2 {
			t.Fatalf("%+v", p)
		}
		if w.incidents(s) != 2 {
			t.Fatal(w.incidents(s))
		}
		// A document that is not one is never written.
		if _, _, err := s.create(ctx, key, catalogueSeriesOf, 3, []byte(`{}`)); err == nil {
			t.Fatal("a catalogue entry that is not one was written")
		}
	})

	t.Run("a catalogue entry expired", func(t *testing.T) {
		w, b := newS3World(t)
		s := w.enclave()
		w.write(s, account)
		w.write(s, other)
		sum := sha256.Sum256([]byte("receipt-chains"))
		b.expire(fmt.Sprintf("%x", sum[:16]) + memberKey(1))
		w.clock.set(day(1))
		if p := w.audit(w.enclave()); p.Audit.Expired != 1 || p.Audit.Chains != 2 {
			t.Fatalf("%+v", p.Audit)
		}
	})

	t.Run("the passes' last number, and every number taken", func(t *testing.T) {
		w := newChainWorld(t)
		s := w.enclave()
		w.write(s, account)
		key, _ := testKeys{secret: "root"}.Derive(ctx, ChainPurpose)
		pass := testPass()
		pass.P = math.MaxUint64
		pass.Audit.Began = start.Add(-48 * time.Hour)
		if _, own, err := s.create(ctx, key, passSeriesOf, math.MaxUint64, mustJSON(&pass)); err != nil || !own {
			t.Fatal(err)
		}
		w.clock.set(day(1))
		if _, err := s.Audit(ctx); !errors.Is(err, errFull) {
			t.Fatal(err)
		}

		w = newChainWorld(t)
		h := w.hooked()
		s = w.enclave()
		w.write(s, account)
		rival := w.enclaveOver(w.mc)
		h.before = func(c store.Commit) error {
			if _, n, ok := strings.Cut(c.Name, "receipt-audit-"); ok && n != "" {
				p, _ := strconv.ParseUint(n, 10, 64)
				doc := testPass()
				doc.P = p
				if _, _, err := rival.create(ctx, key, passSeriesOf, p, mustJSON(&doc)); err != nil {
					t.Error(err)
				}
			}
			return nil
		}
		w.clock.set(day(1))
		if _, err := s.Audit(ctx); !errors.Is(err, errTries) {
			t.Fatal(err)
		}
	})

	t.Run("a window that overflows after the receipts", func(t *testing.T) {
		w := newChainWorld(t)
		s := w.enclave()
		id := w.write(s, account)
		w.deleteReceipt(account, id)
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
	})

	t.Run("the chain's key cannot seal", func(t *testing.T) {
		w := newChainWorld(t)
		s := w.enclave()
		s.keys = shortChainKey{}
		if err := s.Write(ctx, account, receipt()); err == nil {
			t.Fatal("a link sealed with a key that is not one")
		}
	})

	t.Run("an enclave's memory is bounded", func(t *testing.T) {
		w := newChainWorld(t)
		s := w.enclave()
		busy := s.chain.appender(s, account)
		busy.write.Lock()
		for i := range appendersMax {
			s.chain.appender(s, fmt.Sprintf("acc_%026d", i))
		}
		s.chain.appender(s, other)
		if len(s.chain.appenders) != 3 {
			t.Fatalf("%d appenders kept", len(s.chain.appenders))
		}
		busy.write.Unlock()
		for i := range enteredMax {
			s.chain.entered[fmt.Sprint(i)] = true
		}
		for i := range recordedMax {
			s.chain.recorded[Finding{Kind: "link_lost", Account: third, N: uint64(i + 1)}] = true
		}
		w.write(s, other)
		if err := s.record(ctx, []Finding{{Kind: "link_lost", Account: other, N: 1}}); err != nil {
			t.Fatal(err)
		}
		if len(s.chain.entered) != 1 || len(s.chain.recorded) != 1 {
			t.Fatalf("%d entered, %d recorded", len(s.chain.entered), len(s.chain.recorded))
		}
	})

	t.Run("the accounts' list is read a page at a time", func(t *testing.T) {
		w := newChainWorld(t)
		for i := range accountsPage + 1 {
			w.accounts.list = append(w.accounts.list, fmt.Sprintf("acc_%026d", i))
		}
		w.unchained("acc_"+strings.Repeat("0", 25)+"1", 1)
		w.accounts.list = append(w.accounts.list, third)
		w.clock.set(day(1))
		w.audit(w.enclave())
		if h, err := w.enclave().Head(ctx, third); err != nil || h.N != 0 {
			t.Fatalf("%+v %v", h, err)
		}
	})
}

// shortChainKey derives the chain's key too short to seal with.
type shortChainKey struct{}

func (shortChainKey) Derive(ctx context.Context, purpose string) ([]byte, error) {
	if purpose == ChainPurpose {
		return []byte("short"), nil
	}
	return testKeys{secret: "root"}.Derive(ctx, purpose)
}

// The audit's own failures: accounts that cannot answer, a record that
// fails at the end of a pass, a recovery whose reads fail, and recoveries
// that lose their race to a link naming their receipts, or one that cannot
// be read.
func TestTheAuditsFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("the accounts cannot answer", func(t *testing.T) {
		w := newChainWorld(t)
		ids := w.writes(w.enclave(), account, 2)
		w.accounts.fail = errBoom
		w.clock.set(day(1))
		if _, err := w.enclave().Audit(ctx); !errors.Is(err, errBoom) {
			t.Fatal(err)
		}
		w.deleteReceipt(account, ids[0])
		if _, err := w.enclave().Audit(ctx); !errors.Is(err, errBoom) {
			t.Fatal(err)
		}
	})

	t.Run("the record fails at the end of a pass", func(t *testing.T) {
		w := newChainWorld(t)
		h := w.hooked()
		ids := w.writes(w.enclave(), account, 2)
		w.deleteReceipt(account, ids[0])
		failIncidents(h, true)
		w.clock.set(day(1))
		if p, err := w.enclave().Audit(ctx); !errors.Is(err, errBoom) || p != nil {
			t.Fatal(err)
		}
	})

	// recovering is a world where the pass found one receipt to recover
	// and, once it read the chain, another enclave linked: arm runs then.
	recovering := func(t *testing.T, arm func(w *chainWorld, h *hookedCommits)) (*chainWorld, *AuditPass, error) {
		w := newChainWorld(t)
		h := w.hooked()
		w.write(w.enclave(), account)
		w.unchained(account, 1)
		w.clock.set(day(1))
		w.accounts.removing = func(string) {
			w.accounts.removing = nil
			arm(w, h)
		}
		p, err := w.enclave().Audit(ctx)
		return w, p, err
	}

	t.Run("a recovery whose reads fail", func(t *testing.T) {
		_, _, err := recovering(t, func(w *chainWorld, _ *hookedCommits) {
			w.write(w.enclave(), account)
			w.mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
				if step == store.StepRead && strings.HasPrefix(key, LinkKeyPrefix(2)) {
					return errBoom
				}
				return nil
			}
		})
		if !errors.Is(err, errBoom) {
			t.Fatal(err)
		}
		// The head found, the link above the pass's head not read.
		_, _, err = recovering(t, func(w *chainWorld, _ *hookedCommits) {
			w.write(w.enclave(), account)
			reads := 0
			w.mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
				if step == store.StepRead && strings.HasPrefix(key, LinkKeyPrefix(2)+"@") {
					if reads++; reads == 2 {
						return errBoom
					}
				}
				return nil
			}
		})
		if !errors.Is(err, errBoom) {
			t.Fatal(err)
		}
		_, _, err = recovering(t, func(w *chainWorld, h *hookedCommits) {
			w.write(w.enclave(), account)
			calls := 0
			h.ranged = func() error {
				if calls++; calls == 2 {
					return errBoom
				}
				return nil
			}
		})
		if !errors.Is(err, errBoom) {
			t.Fatal(err)
		}
		_, _, err = recovering(t, func(_ *chainWorld, h *hookedCommits) {
			h.ranged = func() error { return errBoom }
		})
		if !errors.Is(err, errBoom) {
			t.Fatal(err)
		}
	})

	// race makes another enclave take the recovered link's number first,
	// with doc built over the head before it.
	race := func(w *chainWorld, h *hookedCommits, doc func(orphan string, n uint64, prev Digest) []byte) {
		rival := w.enclaveOver(w.mc)
		key, _ := rival.chainKey(ctx)
		h.before = func(c store.Commit) error {
			if c.Name != LinkName(account, 2) {
				return nil
			}
			h.before = nil
			head, err := rival.findHead(ctx, key, links(account))
			if err != nil {
				return err
			}
			var orphan string
			for _, e := range w.mem.AllEntries() {
				if e.Partition == Partition(account) && strings.HasPrefix(e.Key, IDPrefix) && e.Key != head.p.link.Receipts[0].ID {
					orphan = e.Key
				}
			}
			sealed, _ := envelope.SealText(key, doc(orphan, 2, head.digest), envelope.AAD(ChainPurpose, LinkName(account, 2)))
			return rival.chain.anchored.CreateNew(ctx, &store.Record{Name: LinkName(account, 2), Value: sealed})
		}
	}

	t.Run("a recovery that loses its race to a link naming its receipts", func(t *testing.T) {
		w, p, err := recovering(t, func(w *chainWorld, h *hookedCommits) {
			race(w, h, func(orphan string, n uint64, prev Digest) []byte {
				e := w.entry(Partition(account), orphan)
				raw, _, _ := openRaw(mustKey(Purpose), &e)
				return mustJSON(&Link{V: LinkVersion, N: n, Kind: LinkRecovered, At: day(1).Add(time.Second), Prev: &prev, Receipts: []Ref{{ID: orphan, Digest: DigestOf(raw)}}})
			})
		})
		if err != nil || p.Audit.Recovered != 0 {
			t.Fatalf("%+v %v", p, err)
		}
		if got := shape(walkAll(t, w.enclave(), account, 10)); got != "2:ok[ok] 1:ok[ok]" {
			t.Fatal(got)
		}
	})

	t.Run("a recovery that loses its race to a link that cannot be read", func(t *testing.T) {
		w, p, err := recovering(t, func(w *chainWorld, h *hookedCommits) {
			race(w, h, func(string, uint64, Digest) []byte { return []byte("not a link") })
		})
		if err != nil || p.Audit.Recovered != 0 {
			t.Fatalf("%+v %v", p, err)
		}
		if got := shape(walkAll(t, w.enclave(), account, 10)); got != "2:unreadable 1:ok[ok]" {
			t.Fatal(got)
		}
	})

	t.Run("a link's number taken and the taker not read", func(t *testing.T) {
		w := newChainWorld(t)
		h := w.hooked()
		s := w.enclave()
		w.write(s, account)
		rival := w.enclaveOver(w.mc)
		h.before = func(c store.Commit) error {
			if c.Name == LinkName(account, 2) {
				h.before = nil
				w.write(rival, account)
				h.ranged = func() error { return errBoom }
			}
			return nil
		}
		if err := s.Write(ctx, account, receipt()); !errors.Is(err, errBoom) {
			t.Fatal(err)
		}
	})
}

// mustKey is the root's key for purpose, as every enclave of the tests
// derives it.
func mustKey(purpose string) []byte {
	k, _ := testKeys{secret: "root"}.Derive(context.Background(), purpose)
	return k
}
