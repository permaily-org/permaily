package receipts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The review of pull request 1 (#170) found four defects; each test here
// is its reproduction, failing before the fix.

// L16, F1: the audit's fence holds until its write stages its value, not
// only until the store's own timer starts. A pass that asked Removing,
// answered live, and is paused before the store begins its write, resumed
// after the removal began and both its sweeps ran, stores nothing.
func TestL16TheFenceHoldsUntilTheWriteStages(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	s := w.enclave()
	w.write(s, account)
	w.unchained(account, 1)
	w.clock.set(day(1))
	paused := false
	s.chain.anchored.Now = func() time.Time {
		if w.accounts.asked > 0 && !paused {
			paused = true
			w.accounts.remove(account)
			if err := w.mem.DeletePartition(ctx, Partition(account)); err != nil {
				t.Fatal(err)
			}
			w.clock.set(day(1).Add(RemoveAgainAfter + time.Minute))
			if err := w.mem.DeletePartition(ctx, Partition(account)); err != nil {
				t.Fatal(err)
			}
		}
		return w.clock.now()
	}
	_, err := s.Audit(ctx)
	if !paused {
		t.Fatal("the pause was not reached")
	}
	for _, e := range w.mem.AllEntries() {
		if e.Partition == Partition(account) {
			t.Fatalf("the audit stored %s after both sweeps (%v)", e.Key, err)
		}
	}
	if w.accounts.asked < 2 {
		t.Fatalf("the fence was not asked again: %d", w.accounts.asked)
	}
}

// L28, F3: a link whose commit expired is authenticated by the link above
// it whatever page it is read on: a continuation that begins at it is not
// lost, and records nothing.
func TestL28AnExpiredLinkAtAPageBoundary(t *testing.T) {
	ctx := context.Background()
	w, b := newS3World(t)
	s := w.enclave()
	w.writes(s, account, 3)
	b.expire(linkKey(account, 1))
	b.expire(linkKey(account, 2))
	s = w.enclaveOver(w.s3Commits(b))
	p, err := s.Walk(ctx, account, Position{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.Walk(ctx, account, p.Next, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Links) != 1 || p.Links[0].Verdict != LinkExpired || w.incidents(s) != 0 {
		t.Fatalf("%s; incidents %d", shape(p.Links), w.incidents(s))
	}
}

// L18, F4: the audit's window bounds the refs it holds, every occurrence
// counted, not only the distinct identifiers: a receipt named again and
// again (own-identity retries) is held once per link that names it.
func TestL18RepeatedRefsCountAgainstTheWindow(t *testing.T) {
	w := newChainWorld(t)
	id, _ := NewID(start)
	j := &joiner{r: &auditRun{s: w.enclave()}, refs: map[string][]held{}}
	for n := 1; n <= AuditWindowRefsMax+1; n++ {
		j.links = []judged{{n: uint64(n), verdict: LinkOK, p: parsed{link: &Link{Receipts: []Ref{{ID: id}}}}}}
		err := j.consume(context.Background())
		if (n > AuditWindowRefsMax) != errors.Is(err, errWindow) {
			t.Fatalf("holding %d refs of one receipt: %v", n, err)
		}
	}
	// A receipt met gives back what was held of it.
	j = &joiner{r: j.r, sr: links(account), partition: Partition(account), refs: map[string][]held{}}
	j.links = []judged{{n: 1, verdict: LinkOK, p: parsed{link: &Link{Receipts: []Ref{{ID: id}}}}}}
	if err := j.consume(context.Background()); err != nil || j.held != 1 {
		t.Fatal(err, j.held)
	}
	if err := j.resolve(context.Background(), ""); err != nil || j.held != 0 {
		t.Fatal(err, j.held)
	}
}

// L18, F4, through the real appender and audit: 1,024 links of 64 refs
// over the same 64 receipts hold the window's 65,536 refs at once, and
// one link more fails the pass.
func TestL18RepeatedIdentityLinksFailTheWindow(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	ids := w.unchained(account, LinkReceiptsMax)
	s := w.enclave()
	rkey, err := s.keys.Derive(ctx, Purpose)
	if err != nil {
		t.Fatal(err)
	}
	refs := make([]Ref, len(ids))
	for i, id := range ids {
		e := w.entry(Partition(account), id)
		raw, _, err := openRaw(rkey, &e)
		if err != nil {
			t.Fatal(err)
		}
		refs[i] = Ref{ID: id, Digest: DigestOf(raw)}
	}
	// At the bound exactly, 1,024 links of 64 refs: the pass finishes.
	for range AuditWindowRefsMax / LinkReceiptsMax {
		if _, err := s.chain.appender(s, account).link(ctx, refs); err != nil {
			t.Fatal(err)
		}
	}
	w.clock.set(day(1))
	if p := w.audit(w.enclave()); p.Audit.Chains != 1 || p.Audit.Broken["receipt_missing"] != 0 {
		t.Fatalf("%+v", p.Audit)
	}
	// One link more: 65,600 refs held at once, and the pass fails.
	if _, err := s.chain.appender(s, account).link(ctx, refs); err != nil {
		t.Fatal(err)
	}
	w.clock.set(day(2))
	if _, err := w.enclave().Audit(ctx); !errors.Is(err, errWindow) {
		t.Fatalf("the audit held 65,600 refs: %v", err)
	}
}

// L28, F3: an expired prefix longer than WalkLinksMax, walked a receipt a
// page through one enclave, every link expired and nothing recorded; a
// page asked of another enclave deep in it, which has to read more links
// above it than a page may, fails with ErrContinuation and goes on when
// asked again, never with a verdict.
func TestL28AnExpiredPrefixLongerThanAPage(t *testing.T) {
	ctx := context.Background()
	w, b := newS3World(t)
	s := w.enclave()
	const links, expired = WalkLinksMax + 44, WalkLinksMax + 40
	w.writes(s, account, links)
	for n := uint64(1); n <= expired; n++ {
		b.expire(linkKey(account, n))
	}
	r := w.enclaveOver(w.s3Commits(b))
	got := walkAll(t, r, account, 1)
	if len(got) != links {
		t.Fatalf("%d links", len(got))
	}
	for _, l := range got {
		want := LinkExpired
		if l.N > expired {
			want = LinkOK
		}
		if l.Verdict != want || len(l.Receipts) != 1 || l.Receipts[0].Verdict != ReceiptOK {
			t.Fatalf("%d: %s", l.N, l.Verdict)
		}
	}
	// Another enclave, at link 10: 286 links above it before one with a
	// commit, more than a page reads.
	o := w.enclaveOver(w.s3Commits(b))
	tries := 0
	for {
		tries++
		p, err := o.Walk(ctx, account, Position{N: 11}, 1)
		if errors.Is(err, ErrContinuation) && tries < 10 {
			continue
		}
		if err != nil || len(p.Links) != 1 || p.Links[0].N != 10 || p.Links[0].Verdict != LinkExpired {
			t.Fatalf("try %d: %v", tries, err)
		}
		break
	}
	if tries != 2 {
		t.Fatalf("%d tries", tries)
	}
	if n := w.incidents(o); n != 0 {
		t.Fatalf("%d incidents", n)
	}
}

// L28, F3: pages that end inside an expired link, by their bytes, walked
// and exported through either enclave: the link is expired on each page
// it is on, and every export page passes its check.
func TestL28PagesEndInsideAnExpiredLink(t *testing.T) {
	ctx := context.Background()
	w, b := newS3World(t)
	ids := w.unchained(account, 6)
	w.clock.set(start.Add(2 * AdoptAfter))
	s := w.enclave()
	w.writes(s, account, 3) // link 1 adopts the six, then 2 to 4
	for n := uint64(1); n <= 3; n++ {
		b.expire(linkKey(account, n))
	}
	enclaves := []*Service{w.enclaveOver(w.s3Commits(b)), w.enclaveOver(w.s3Commits(b))}
	one := SealedBytes(len(w.entry(Partition(account), ids[0]).Value))
	first := len(w.entry(Partition(account), w.valueKeys(account, 1)[0]).Value)
	for _, e := range enclaves {
		e.walkBytes = first + 2*one
	}
	// A continuation one enclave cannot authenticate within its page's
	// bytes is asked again, as the person's app does.
	var walked []string
	at := Position{}
	for i, tries := 0, 0; ; i++ {
		p, err := enclaves[i%2].Walk(ctx, account, at, 100)
		if errors.Is(err, ErrContinuation) && tries < 20 {
			tries++
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range p.Links {
			walked = append(walked, fmt.Sprintf("%d:%s/%d", l.N, l.Verdict, len(l.Receipts)))
		}
		if p.Done {
			break
		}
		at = p.Next
	}
	// Link 1, adopted with six receipts, is split across pages.
	if got := " " + strings.Join(walked, " "); strings.Count(got, " 1:expired/") < 2 || strings.Contains(got, "lost") {
		t.Fatalf("%q", got)
	}
	at = Position{}
	for i, tries := 0, 0; ; i++ {
		p, err := enclaves[(i+1)%2].Export(ctx, account, at)
		if errors.Is(err, ErrContinuation) && tries < 20 {
			tries++
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Check(); err != nil {
			t.Fatal(err)
		}
		for _, l := range p.Links {
			if l.N <= 3 && l.Verdict != LinkExpired {
				t.Fatalf("%d: %s", l.N, l.Verdict)
			}
		}
		if p.Done {
			break
		}
		at = p.Next
	}
	if n := w.incidents(enclaves[0]); n != 0 {
		t.Fatalf("%d incidents", n)
	}
}

// L17 and L28, F3: what a continuation reads above its expired link is
// bounded as a page's listing is. A delete marker where a link's commit
// was is no commit, and the link above authenticates it all the same;
// more objects there than a page lists fail the page (ErrHistoryTooLarge),
// never a verdict. What an enclave keeps of continuations is bounded.
func TestL17AContinuationsListingIsBounded(t *testing.T) {
	ctx := context.Background()
	w, b := newS3World(t)
	s := w.enclave()
	w.writes(s, account, 3)
	b.expire(linkKey(account, 1))
	b.expire(linkKey(account, 2))
	b.hide(linkKey(account, 2))
	p, err := w.enclaveOver(w.s3Commits(b)).Walk(ctx, account, Position{N: 2}, 1)
	if err != nil || shape(p.Links) != "1:expired[ok]" {
		t.Fatalf("%+v %v", p, err)
	}
	for range WalkListedMax {
		b.hide(linkKey(account, 2))
	}
	if _, err := w.enclaveOver(w.s3Commits(b)).Walk(ctx, account, Position{N: 2}, 1); !errors.Is(err, ErrHistoryTooLarge) {
		t.Fatal(err)
	}
	for i := range continuedMax + 1 {
		s.chain.authenticated(fmt.Sprintf("acc_%026d", i), 1, Digest{})
	}
	if len(s.chain.continued) != 1 {
		t.Fatal(len(s.chain.continued))
	}
}
