package receipts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/store"
)

// before writes n receipts of account without a chain, as an earlier
// release did, at the world's clock.
func (w *chainWorld) unchained(account string, n int) []string {
	w.t.Helper()
	s := New(w.mem, testKeys{secret: "root"}, w.clock.now, w.random.read)
	return w.writes(s, account, n)
}

// adoptedOf is what the adopted links of a walk name, in order, and their
// links' More.
func adoptedOf(links []WalkedLink) (ids []string, more []bool) {
	for i := len(links) - 1; i >= 0; i-- {
		if l := links[i].Link; l != nil && l.Kind == LinkAdopted {
			for _, r := range l.Receipts {
				ids = append(ids, r.ID)
			}
			more = append(more, l.More)
		}
	}
	return ids, more
}

// L14: adoption. Accounts with 0, 1, 64, 65 and 200 receipts from before
// the chain adopt them in key order, with before and more as designed,
// then link their own; one with none makes no adopted link.
func TestL14ReceiptsFromBeforeTheChainAreAdopted(t *testing.T) {
	for _, n := range []int{0, 1, 64, 65, 200} {
		w := newChainWorld(t)
		old := w.unchained(account, n)
		w.clock.set(start.Add(2 * time.Minute))
		s := w.enclave()
		mine := w.write(s, account)
		links := merged(walkAll(t, s, account, 1000))
		ids, more := adoptedOf(links)
		if strings.Join(ids, ",") != strings.Join(old, ",") {
			t.Fatalf("%d receipts: adopted %d", n, len(ids))
		}
		for i, m := range more {
			if m != (i < len(more)-1) {
				t.Fatalf("%d receipts: more %v", n, more)
			}
		}
		top := links[0].Link
		if top.Kind != LinkReceipts || top.Receipts[0].ID != mine || len(links) != (n+63)/64+1 {
			t.Fatalf("%d receipts: %s", n, shape(links))
		}
		for _, l := range links[1:] {
			if !l.Link.Before.Equal(start.Add(time.Minute)) {
				t.Fatalf("before %v", l.Link.Before)
			}
		}
		if n == 0 && (top.N != 1 || *top.Prev != Genesis(account)) {
			t.Fatalf("%+v", top)
		}
	}
}

// L14: two enclaves adopting at once make one adoption with no gap, the
// other continuing it; a receipt at before exactly is not adopted; an
// entry that does not open is not, and the audit counts it unreadable; an
// adoption cut is continued by the next write; one whose link is then lost
// leaves its receipts to recovery; an account that writes nothing after
// the release is entered and adopted by the audit from the accounts' list.
func TestL14AdoptionAtTheEdges(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	old := w.unchained(account, 200)
	w.clock.set(start.Add(time.Hour))
	a, b := w.enclave(), w.enclave()
	var wg sync.WaitGroup
	for _, s := range []*Service{a, b, a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Write(ctx, account, receipt()); err != nil && !errors.Is(err, errTries) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	links := merged(walkAll(t, w.enclave(), account, 1000))
	ids, _ := adoptedOf(links)
	if strings.Join(ids, ",") != strings.Join(old, ",") {
		t.Fatalf("two enclaves adopted %d of 200", len(ids))
	}
	for id, n := range linked(links) {
		if n != 1 {
			t.Fatalf("%s linked %d times", id, n)
		}
	}

	// A receipt at before exactly, and one that does not open.
	w = newChainWorld(t)
	at := w.unchained(account, 2)
	broken := w.entry(Partition(account), at[1])
	broken.Value = []byte("junk")
	w.mem.SetEntry(broken)
	w.clock.set(start.Add(AdoptAfter))
	s := w.enclave()
	w.write(s, account)
	links = walkAll(t, s, account, 10)
	if len(links) != 1 || links[0].Link.Kind != LinkReceipts {
		t.Fatalf("a receipt at before was adopted: %s", shape(links))
	}
	w.clock.set(start.Add(AuditEvery))
	p := w.audit(s)
	if p.Audit.Recovered != 1 || p.Audit.Broken["receipt_unreadable"] != 1 {
		t.Fatalf("%+v", p.Audit)
	}

	// An adoption cut by a failure is continued by the next write.
	w = newChainWorld(t)
	h := w.hooked()
	old = w.unchained(account, 130)
	w.clock.set(start.Add(time.Hour))
	h.before = func(c store.Commit) error {
		if c.Name == LinkName(account, 2) {
			return errBoom
		}
		return nil
	}
	s = w.enclave()
	if err := s.Write(ctx, account, receipt()); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	h.before = nil
	w.write(w.enclave(), account)
	links = merged(walkAll(t, w.enclave(), account, 1000))
	if ids, _ := adoptedOf(links); strings.Join(ids, ",") != strings.Join(old, ",") || len(links) != 4 {
		t.Fatalf("%s", shape(links))
	}

	// An adoption whose link is then lost leaves its receipts to recovery
	// within the window, unlinked beyond it: link 1's 64, and the receipt
	// of the write that failed, in two recovered links.
	w.deleteLink(account, 1)
	w.clock.set(start.Add(time.Hour + AuditEvery))
	p = w.audit(w.enclave())
	if p.Audit.Broken["link_lost"] != 1 || p.Audit.Recovered != 65 || p.Audit.Broken["receipt_missing"] != 0 {
		t.Fatalf("%+v", p.Audit)
	}
	w.deleteLink(account, 2)
	w.clock.set(start.Add(RecoverWithin + 2*AuditEvery))
	p = w.audit(w.enclave())
	if p.Audit.Broken["link_lost"] != 2 || p.Audit.Unlinked != 64 || p.Audit.Recovered != 0 {
		t.Fatalf("%+v", p.Audit)
	}

	// An account that writes nothing after the release.
	w = newChainWorld(t)
	old = w.unchained(account, 3)
	w.unchained(other, 2)
	w.accounts.list = []string{account, other, "acc_cccccccccccccccccccccccccc", "nobody"}
	w.clock.set(start.Add(AuditEvery))
	s = w.enclave()
	w.audit(s)
	for _, acc := range []string{account, other} {
		if h, err := s.Head(ctx, acc); err != nil || h.N != 1 {
			t.Fatalf("%s: %+v %v", acc, h, err)
		}
	}
	// The next pass audits them: they are in the catalogue.
	w.clock.set(start.Add(2 * AuditEvery))
	if p := w.audit(s); p.Audit.Chains != 2 || p.Audit.Catalogue != 2 {
		t.Fatalf("%+v", p.Audit)
	}
	_ = fmt.Sprint(old)
}
