package receipts

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

// L8: finding the head. Chains of every size around a page, found after a
// restart with a true hint in one listing, and with no hint, a hint
// lowered by values deleted or raised by junk in at most
// ceil(objects/1,000)+1 listings. Durations are logged, never asserted: under
// -race on a loaded runner they have no bound worth writing (docs/testing.md).
func TestL8TheHeadIsFoundByListing(t *testing.T) {
	ctx := context.Background()
	for _, size := range []int{0, 1, 2, 63, 64, 65, 1000, 1001, 10000} {
		w, b := newS3World(t)
		s := w.enclave()
		ids := w.writes(s, account, size)
		listings := func(s *Service) (Head, int) {
			before := b.listings()
			h, err := s.Head(ctx, account)
			if err != nil {
				t.Fatal(err)
			}
			return h, b.listings() - before
		}
		bound := (size+999)/1000 + 1
		h, n := listings(w.enclave())
		if h.N != uint64(size) || n > 1 || (size > 0 && (h.Verdict != LinkOK || h.At.IsZero() || h.Digest == (Digest{}))) {
			t.Fatalf("%d links, a true hint: %+v in %d listings", size, h, n)
		}
		if size == 0 {
			if h != (Head{}) {
				t.Fatalf("%+v", h)
			}
			continue
		}
		// Raised by junk under the links' keys.
		w.mem.SetEntry(store.Entry{Partition: Partition(account), Key: LinkKeyPrefix(math.MaxUint64-1) + "@junk", Value: []byte("junk")})
		began := time.Now()
		h, n = listings(w.enclave())
		if h.N != uint64(size) || n > bound+1 {
			t.Fatalf("%d links, a raised hint: %+v in %d listings", size, h, n)
		}
		t.Logf("%d links, a raised hint: %d listings in %v", size, n, time.Since(began))
		w.mem.SetEntry(store.Entry{Partition: Partition(account), Key: LinkPrefix + "zz", Value: []byte("junk")})
		if h, _ = listings(w.enclave()); h.N != uint64(size) {
			t.Fatalf("a junk key: %+v", h)
		}
		if err := w.mem.DeleteEntry(ctx, Partition(account), LinkPrefix+"zz"); err != nil {
			t.Fatal(err)
		}
		if err := w.mem.DeleteEntry(ctx, Partition(account), LinkKeyPrefix(math.MaxUint64-1)+"@junk"); err != nil {
			t.Fatal(err)
		}
		// No hint: the scan fails.
		w.mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
			if step == store.StepRead && key == LinkPrefix {
				return errBoom
			}
			return nil
		}
		h, n = listings(w.enclave())
		w.mem.Interrupt = nil
		if h.N != uint64(size) || n > bound {
			t.Fatalf("%d links, no hint: %+v in %d listings", size, h, n)
		}
		// Lowered: the newest value deleted. The head is the newest number
		// still, lost, and the next write goes on after a break.
		w.deleteLink(account, uint64(size))
		h, n = listings(w.enclave())
		if h.N != uint64(size) || h.Verdict != LinkLost || n > bound+1 {
			t.Fatalf("%d links, the newest value deleted: %+v in %d listings", size, h, n)
		}
		s = w.enclave()
		w.write(s, account)
		page, err := s.Walk(ctx, account, Position{}, 1)
		if err != nil || page.Links[0].Link.Break != BreakLost {
			t.Fatalf("after a lost head: %+v %v", page, err)
		}
		if page, err = s.Walk(ctx, account, page.Next, 1); err != nil || page.Links[0].Verdict != LinkLost {
			t.Fatalf("after a lost head: %+v %v", page, err)
		}
		if size <= 1001 && len(linked(walkAll(t, s, account, 1000))) != size {
			t.Fatalf("%d links: not every receipt is still linked", size)
		}
		_ = ids
	}
}

// L8: what a head is when it cannot be read, and what is ignored above it.
func TestL8AHeadThatCannotBeRead(t *testing.T) {
	ctx := context.Background()
	w, b := newS3World(t)
	s := w.enclave()
	w.writes(s, account, 3)
	// Junk objects and delete markers above the head are not a head.
	b.mu.Lock()
	b.next++
	b.versions = append(b.versions, bver{key: linkKey(account, 9), id: fmt.Sprintf("v%06d", b.next), body: []byte("junk")})
	b.mu.Unlock()
	b.hide(linkKey(account, 7))
	if h, err := w.enclave().Head(ctx, account); err != nil || h.N != 3 || h.Verdict != LinkOK {
		t.Fatalf("%+v %v", h, err)
	}
	// A listing that fails is an error, never an absent number.
	b.fail = errBoom
	if _, err := w.enclave().Head(ctx, account); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	if err := w.enclave().Write(ctx, account, receipt()); err == nil {
		t.Fatal("a write over a listing that fails")
	}
	b.fail = nil

	// Every value deleted.
	for n := uint64(1); n <= 3; n++ {
		w.deleteLink(account, n)
	}
	if h, err := w.enclave().Head(ctx, account); err != nil || h.N != 3 || h.Verdict != LinkLost || h.Digest != (Digest{}) {
		t.Fatalf("%+v %v", h, err)
	}

	// Forked and unreadable heads, in memory.
	m := newChainWorld(t)
	s = m.enclave()
	m.writes(s, account, 2)
	m.mc.Fork(LinkName(account, 2), 1, sha256.Sum256([]byte("other")))
	h, err := m.enclave().Head(ctx, account)
	if err != nil || h.N != 2 || h.Verdict != LinkForked || h.Digest != (Digest{}) || !h.At.IsZero() {
		t.Fatalf("forked: %+v %v", h, err)
	}
	m.write(m.enclave(), account)
	links := walkAll(t, m.enclave(), account, 10)
	if shape(links) != "3:ok[ok] 2:forked 1:ok[ok]" || links[0].Link.Break != BreakForked {
		t.Fatalf("%s", shape(links))
	}
	if len(m.valueKeys(account, 2)) != 1 {
		t.Fatal("something was written at the forked number")
	}

	// A commit that names a value that is not a link (an enclave of the
	// deployment wrote it, as only one can).
	m = newChainWorld(t)
	m.writes(m.enclave(), account, 1)
	if err := m.anchored().CreateNew(ctx, &store.Record{Name: LinkName(account, 2), Value: []byte("not sealed")}); err != nil {
		t.Fatal(err)
	}
	if h, err := m.enclave().Head(ctx, account); err != nil || h.N != 2 || h.Verdict != LinkUnreadable {
		t.Fatalf("unreadable: %+v %v", h, err)
	}
	// Sealed, and not a link at its number.
	key, _ := testKeys{secret: "root"}.Derive(ctx, ChainPurpose)
	doc := mustJSON(testLink(9, 1))
	sealed, _ := envelope.SealText(key, doc, envelope.AAD(ChainPurpose, LinkName(account, 3)))
	if err := m.anchored().CreateNew(ctx, &store.Record{Name: LinkName(account, 3), Value: sealed}); err != nil {
		t.Fatal(err)
	}
	if h, err := m.enclave().Head(ctx, account); err != nil || h.N != 3 || h.Verdict != LinkUnreadable {
		t.Fatalf("not a link at its number: %+v %v", h, err)
	}
	m.write(m.enclave(), account)
	links = walkAll(t, m.enclave(), account, 10)
	if shape(links) != "4:ok[ok] 3:unreadable 2:unreadable 1:ok[ok]" || links[0].Link.Break != BreakUnreadable {
		t.Fatal(shape(links))
	}
	// Each of those was recorded before anything said so.
	if got := kinds(uniq(m.findings())); got != "link_unreadable:2" {
		t.Fatal(got)
	}
}

// L8: a chain at 2^64-1 takes no more link.
func TestL8AChainAtTheLastNumberTakesNoMoreLink(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	s := w.enclave()
	w.write(s, account)
	key, _ := testKeys{secret: "root"}.Derive(ctx, ChainPurpose)
	l := testLink(math.MaxUint64, 1)
	doc := mustJSON(&l)
	sealed, _ := envelope.SealText(key, doc, envelope.AAD(ChainPurpose, LinkName(account, math.MaxUint64)))
	if err := w.anchored().CreateNew(ctx, &store.Record{Name: LinkName(account, math.MaxUint64), Value: sealed}); err != nil {
		t.Fatal(err)
	}
	s = w.enclave()
	if h, err := s.Head(ctx, account); err != nil || h.N != math.MaxUint64 || h.Verdict != LinkOK {
		t.Fatalf("%+v %v", h, err)
	}
	if err := s.Write(ctx, account, receipt()); !errors.Is(err, errFull) {
		t.Fatal(err)
	}
	p, err := s.Export(ctx, account, Position{})
	if err != nil || p.Check() != nil || p.Links[0].N != math.MaxUint64 {
		t.Fatalf("%+v %v", p, err)
	}
}
