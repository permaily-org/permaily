package receipts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

// L9: a receipt deleted is missing at its position, through either
// enclave, a fresh reader and after a restart; another receipt's value
// under its key, and a changed byte, are unreadable; the same receipt put
// back is ok, not a new receipt.
func TestL9AReceiptDeletedOrChangedIsShownAtItsPosition(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	a, b := w.enclave(), w.enclave()
	ids := w.writes(a, account, 4)
	kept := w.entry(Partition(account), ids[1])
	w.deleteReceipt(account, ids[1])
	moved := w.entry(Partition(account), ids[0])
	moved.Key = ids[2]
	w.mem.SetEntry(moved)
	changed := w.entry(Partition(account), ids[3])
	changed.Value = bytes.Replace(changed.Value, changed.Value[20:21], []byte{changed.Value[20] ^ 1}, 1)
	w.mem.SetEntry(changed)
	for i, s := range []*Service{a, b, w.enclave()} {
		if got := shape(walkAll(t, s, account, 10)); got != "4:ok[unreadable] 3:ok[unreadable] 2:ok[missing] 1:ok[ok]" {
			t.Fatalf("enclave %d: %s", i, got)
		}
	}
	w.mem.SetEntry(kept)
	links := walkAll(t, w.enclave(), account, 10)
	if got := shape(links); got != "4:ok[unreadable] 3:ok[unreadable] 2:ok[ok] 1:ok[ok]" || links[2].Receipts[0].Receipt.ID != ids[1] {
		t.Fatal(got)
	}
	if got := kinds(uniq(w.findings())); got != "receipt_missing:1 receipt_unreadable:2" {
		t.Fatal(got)
	}
	// A receipt that opens with other bytes is changed: its digest is not
	// its ref's.
	w2 := newChainWorld(t)
	s := w2.enclave()
	id := w2.write(s, account)
	key, _ := testKeys{secret: "root"}.Derive(ctx, Purpose)
	doc := []byte(`{"v":1,"id":"` + id + `","kind":"read"}`)
	sealed, _ := envelope.SealText(key, doc, aad(Partition(account), id))
	e := w2.entry(Partition(account), id)
	e.Value = sealed
	w2.mem.SetEntry(e)
	page, err := s.Export(ctx, account, Position{})
	if err != nil || page.Links[0].Receipts[0].Verdict != ReceiptChanged || !bytes.Equal(page.Links[0].Receipts[0].Doc, doc) {
		t.Fatalf("%+v %v", page, err)
	}
}

// L10: a link's value deleted, inside the chain and at the head, is lost
// at its number, the next link down authenticated by its own commit; the
// next append, through the other enclave, writes a break and goes on; the
// audit counts link_lost and reports none of its receipts missing.
func TestL10ALinkDeletedIsLostAtItsNumber(t *testing.T) {
	w := newChainWorld(t)
	a, b := w.enclave(), w.enclave()
	w.writes(a, account, 4)
	w.deleteLink(account, 2)
	w.deleteLink(account, 4)
	if got := shape(walkAll(t, w.enclave(), account, 10)); got != "4:lost 3:ok[ok] 2:lost 1:ok[ok]" {
		t.Fatal(got)
	}
	w.write(b, account)
	links := walkAll(t, w.enclave(), account, 10)
	if got := shape(links); got != "5:ok[ok] 4:lost 3:ok[ok] 2:lost 1:ok[ok]" || links[0].Link.Break != BreakLost {
		t.Fatal(got)
	}
	w.clock.set(start.Add(AuditEvery))
	p := w.audit(w.enclave())
	if p.Audit.Broken["link_lost"] != 2 || p.Audit.Broken["receipt_missing"] != 0 || p.Audit.Unlinked != 0 || p.Audit.Recovered != 2 {
		t.Fatalf("%+v", p.Audit)
	}
}

// L11: a link forked, before and after a later link names it, is forked in
// the walk and the audit, reported as a regression with Fork; the next
// link names it by a break; nothing is written at the forked number.
func TestL11AForkedLink(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	s := w.enclave()
	w.writes(s, account, 3)
	w.mc.Fork(LinkName(account, 2), 1, sha256.Sum256([]byte("other")))
	reader := w.enclave()
	if got := shape(walkAll(t, reader, account, 10)); got != "3:ok[ok] 2:forked 1:ok[ok]" {
		t.Fatal(got)
	}
	fork := false
	for _, r := range reader.chain.anchored.Regressions() {
		fork = fork || (r.Fork && r.Name == LinkName(account, 2))
	}
	if !fork {
		t.Fatalf("%+v", reader.chain.anchored.Regressions())
	}
	w.clock.set(start.Add(AuditEvery))
	if p := w.audit(w.enclave()); p.Audit.Broken["link_forked"] != 1 {
		t.Fatalf("%+v", p.Audit.Broken)
	}
	_ = ctx
}

// L12: a rollback, an earlier copy of the whole partition put back after
// more links, the hint gone with it: the head is still the newest number,
// the links written since are lost, and the chain is never shown whole and
// shorter.
func TestL12ARollbackLeavesTheHeadWhereItWas(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	s := w.enclave()
	w.writes(s, account, 3)
	var copied []store.Entry
	for _, e := range w.mem.AllEntries() {
		if e.Partition == Partition(account) {
			copied = append(copied, e)
		}
	}
	w.writes(s, account, 30)
	if err := w.mem.DeletePartition(ctx, Partition(account)); err != nil {
		t.Fatal(err)
	}
	for _, e := range copied {
		w.mem.SetEntry(e)
	}
	r := w.enclave()
	h, err := r.Head(ctx, account)
	if err != nil || h.N != 33 || h.Verdict != LinkLost {
		t.Fatalf("%+v %v", h, err)
	}
	links := walkAll(t, r, account, 100)
	if len(links) != 33 || shape(links[30:]) != "3:ok[ok] 2:ok[ok] 1:ok[ok]" {
		t.Fatal(shape(links))
	}
	for _, l := range links[:30] {
		if l.Verdict != LinkLost {
			t.Fatal(shape(links))
		}
	}
}

// L13: a reordering. Two receipts swapped between their keys are both
// unreadable. Two links' values swapped between their numbers, and a
// link's value from another account, are not what their commits name:
// lost (ADR 0037's table of verdicts; "Changed while building", the PR's
// note). A value staged and never committed, and one moved from another
// number with a key that looks right, are never read.
func TestL13AReorderingIsShown(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	s := w.enclave()
	ids := w.writes(s, account, 4)
	w.writes(s, other, 1)
	x, y := w.entry(Partition(account), ids[0]), w.entry(Partition(account), ids[1])
	x.Key, y.Key = y.Key, x.Key
	w.mem.SetEntry(x)
	w.mem.SetEntry(y)
	k3, k4 := w.valueKeys(account, 3)[0], w.valueKeys(account, 4)[0]
	v3, v4 := w.entry(Partition(account), k3), w.entry(Partition(account), k4)
	v3.Value, v4.Value = v4.Value, v3.Value
	w.mem.SetEntry(v3)
	w.mem.SetEntry(v4)
	if got := shape(walkAll(t, w.enclave(), account, 10)); got != "4:lost 3:lost 2:ok[unreadable] 1:ok[unreadable]" {
		t.Fatal(got)
	}
	// Another account's link 1 under this account's link 1's key.
	k1 := w.valueKeys(account, 1)[0]
	theirs := w.entry(Partition(other), w.valueKeys(other, 1)[0])
	theirs.Partition, theirs.Key = Partition(account), k1
	w.mem.SetEntry(theirs)
	// A staged value never committed, and one moved from link 2 to a key
	// of link 1 that looks right.
	moved := w.entry(Partition(account), w.valueKeys(account, 2)[0])
	moved.Key = LinkKeyPrefix(1) + "@00000000000000000001-0000000000000000"
	w.mem.SetEntry(moved)
	reads := map[string]int{}
	w.mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
		if step == store.StepRead {
			reads[key]++
		}
		return nil
	}
	if got := shape(walkAll(t, w.enclave(), account, 10)); got != "4:lost 3:lost 2:ok[unreadable] 1:lost" {
		t.Fatal(got)
	}
	if reads[moved.Key] != 0 {
		t.Fatal("a value no commit names was read")
	}
	_ = ctx
}

// L15: kind-agnostic. A receipt of a kind this build does not know, one of
// a version it does not know with fields it does not know, and one near
// 256 KiB are linked, walked and exported the same, their bytes kept
// exactly; a link's size does not depend on its receipts'.
func TestL15EveryReceiptIsLinkedTheSame(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	s := w.enclave()
	summary := receipt()
	summary.Kind = "refusals_summary"
	if err := s.Write(ctx, account, summary); err != nil {
		t.Fatal(err)
	}
	large := receipt()
	large.ClientID = strings.Repeat("x", MaxDocumentBytes-1024)
	if err := s.Write(ctx, account, large); err != nil {
		t.Fatal(err)
	}
	// A later version's, written as a later release would.
	key, _ := testKeys{secret: "root"}.Derive(ctx, Purpose)
	id := "rcp_" + strings.Repeat("0", 15) + "1" + strings.Repeat("ab", idRandomBytes)
	doc := []byte(`{"v":2,"id":"` + id + `","kind":"later","new":{"field":[1,2,3]}}`)
	sealed, _ := envelope.SealText(key, doc, aad(Partition(account), id))
	if err := w.mem.PutEntry(ctx, &store.Entry{Partition: Partition(account), Key: id, Value: sealed}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Link(ctx, account, Ref{ID: id, Digest: DigestOf(doc)}); err != nil {
		t.Fatal(err)
	}
	page, err := s.Export(ctx, account, Position{})
	if err != nil || page.Check() != nil || len(page.Links) != 3 {
		t.Fatalf("%+v %v", page, err)
	}
	if !bytes.Equal(page.Links[0].Receipts[0].Doc, doc) {
		t.Fatalf("an unknown version's bytes changed: %s", page.Links[0].Receipts[0].Doc)
	}
	sizes := map[int]bool{}
	for _, l := range page.Links {
		var link Link
		if err := json.Unmarshal(l.Doc, &link); err != nil || DigestOf(l.Receipts[0].Doc) != link.Receipts[0].Digest {
			t.Fatalf("link %d: %v", l.N, err)
		}
		sizes[len(l.Doc)] = true
	}
	if len(sizes) != 1 {
		t.Fatalf("links of sizes %v", sizes)
	}
	walked, err := s.Walk(ctx, account, Position{}, 10)
	if err != nil || shape(walked.Links) != "3:ok[ok] 2:ok[ok] 1:ok[ok]" || walked.Links[2].Receipts[0].Receipt.Kind != "refusals_summary" {
		t.Fatalf("%s %v", shape(walked.Links), err)
	}
}

// exportAll exports account's chain through s, page by page, each page
// checked.
func exportAll(t *testing.T, s *Service, account string) []*ExportPage {
	t.Helper()
	var out []*ExportPage
	at := Position{}
	for range 10000 {
		p, err := s.Export(context.Background(), account, at)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Check(); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
		if p.Done {
			return out
		}
		at = p.Next
	}
	t.Fatal("the export never ends")
	return nil
}

// L17: the walk's pages are bounded by limit, WalkBytesMax, WalkLinksMax
// and WalkListedMax, end inside a link or at one, and continue at Next with
// nothing skipped or repeated, through either enclave; every export page
// passes ExportPage.Check.
func TestL17PagesAreBoundedAndContinue(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	h := w.hooked()
	a, b := w.enclave(), w.enclave()
	// Forty receipts of about 256 KiB in one link, then 300 small links.
	held, release := make(chan struct{}), make(chan struct{})
	first := true
	h.before = func(c store.Commit) error {
		if c.Name == LinkName(account, 1) && first {
			first = false
			close(held)
			<-release
		}
		return nil
	}
	done := make(chan error, 41)
	go func() { done <- a.Write(ctx, account, receipt()) }()
	<-held
	for range 40 {
		go func() {
			r := receipt()
			r.ClientID = strings.Repeat("y", MaxDocumentBytes-1024)
			done <- a.Write(ctx, account, r)
		}()
	}
	until(t, func() bool { return waiting(a, account) == 40 })
	close(release)
	for range 41 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	h.before = nil
	w.writes(a, account, 300)
	// Walked by pages of 7, alternating enclaves: every receipt once,
	// newest first.
	var seen []string
	at := Position{}
	for i := 0; ; i++ {
		s := []*Service{a, b}[i%2]
		p, err := s.Walk(ctx, account, at, 7)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, l := range p.Links {
			count += len(l.Receipts)
			for _, r := range l.Receipts {
				seen = append(seen, r.Ref.ID)
			}
		}
		if count > 7 || len(p.Links) > WalkLinksMax || len(p.Links) == 0 {
			t.Fatalf("a page of %d receipts and %d links", count, len(p.Links))
		}
		if p.Done {
			break
		}
		at = p.Next
	}
	if len(seen) != 341 {
		t.Fatalf("%d receipts walked", len(seen))
	}
	unique := map[string]bool{}
	for i, id := range seen {
		if unique[id] || (i > 0 && id >= seen[i-1] && i < 300) {
			t.Fatalf("receipt %d repeated or out of order", i)
		}
		unique[id] = true
	}
	// Exported: a page of at most WalkLinksMax links, and the large link
	// over pages that end inside it within WalkBytesMax.
	pages := exportAll(t, b, account)
	if len(pages[0].Links) != WalkLinksMax || pages[0].Next != (Position{N: 47}) {
		t.Fatalf("the first page: %d links, next %+v", len(pages[0].Links), pages[0].Next)
	}
	inside := false
	total := 0
	for _, p := range pages {
		inside = inside || p.Next.Index > 0
		for _, l := range p.Links {
			total += len(l.Receipts)
		}
	}
	if !inside || total != 341 || len(pages) != 3 {
		t.Fatalf("%d pages, %d receipts, ended inside a link %v", len(pages), total, inside)
	}
	// The receipts' bytes alone end a page with no limit on receipts.
	p, err := a.Walk(ctx, account, Position{N: 2, Index: 40}, 1000)
	if err != nil || p.Done || p.Next.N != 2 || p.Next.Index == 0 {
		t.Fatalf("%+v %v", p.Next, err)
	}
}

// L17: positions a chain does not have are refused; a page asked inside a
// link that cannot be read is refused; a page of broken links only is a
// page with its Next.
func TestL17PositionsAndBrokenPages(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	s := w.enclave()
	if p, err := s.Walk(ctx, account, Position{}, 1); err != nil || !p.Done || len(p.Links) != 0 || p.Head != (Head{}) {
		t.Fatalf("a chain not begun: %+v %v", p, err)
	}
	w.writes(s, account, 3)
	for _, at := range []Position{{N: 4}, {N: 1, Index: -1}, {Index: 1}, {N: 2, Index: 2}} {
		if _, err := s.Walk(ctx, account, at, 1); !errors.Is(err, ErrBefore) {
			t.Fatalf("%+v: %v", at, err)
		}
	}
	if _, err := s.Walk(ctx, account, Position{}, 0); err == nil {
		t.Fatal("a page of no receipt")
	}
	if _, err := s.Walk(ctx, "nobody", Position{}, 1); err == nil {
		t.Fatal("a page of no account")
	}
	if _, err := s.Head(ctx, "nobody"); err == nil {
		t.Fatal("a head of no account")
	}
	if p, err := s.Walk(ctx, account, Position{N: 1}, 1); err != nil || !p.Done || len(p.Links) != 0 {
		t.Fatalf("before link 1: %+v %v", p, err)
	}
	w.deleteLink(account, 2)
	if _, err := s.Walk(ctx, account, Position{N: 2, Index: 1}, 1); !errors.Is(err, ErrBefore) {
		t.Fatal(err)
	}
	// Two hundred and sixty lost links make pages of lost links only.
	w.writes(s, account, 260)
	for n := uint64(3); n <= 263; n++ {
		w.deleteLink(account, n)
	}
	p, err := w.enclave().Walk(ctx, account, Position{}, 1)
	if err != nil || p.Done || len(p.Links) != WalkLinksMax || p.Next != (Position{N: 8}) {
		t.Fatalf("%d links, next %+v: %v", len(p.Links), p.Next, err)
	}
	// Storage that cannot be read fails the page.
	w.mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
		if step == store.StepRead && strings.HasPrefix(key, IDPrefix) {
			return errBoom
		}
		return nil
	}
	if _, err := s.Walk(ctx, account, Position{N: 2}, 1); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	w.mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
		if step == store.StepRead && strings.HasPrefix(key, LinkPrefix) && strings.Contains(key, "@") {
			return errBoom
		}
		return nil
	}
	if _, err := s.Walk(ctx, account, Position{N: 2}, 1); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	w.mem.Interrupt = nil
}

// L17 (R4): a page reports only links whose full history it listed: a
// range past WalkListedMax lists again over half as many links, and one
// link whose history alone passes it fails the page.
func TestL17CompleteHistoriesOnly(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	s := w.enclave()
	w.writes(s, account, 300)
	for n := uint64(1); n <= 300; n++ {
		for v := uint64(2); v <= 20; v++ {
			w.mc.Take(LinkName(account, n), v)
		}
	}
	p, err := w.enclave().Walk(ctx, account, Position{}, 10000)
	if err != nil || len(p.Links) != 128 || p.Next != (Position{N: 173}) {
		t.Fatalf("%d links, next %+v: %v", len(p.Links), p.Next, err)
	}
	for _, l := range p.Links {
		if l.Verdict != LinkOK {
			t.Fatalf("link %d %s", l.N, l.Verdict)
		}
	}
	for v := uint64(21); v <= WalkListedMax+1; v++ {
		w.mc.Take(LinkName(account, 299), v)
	}
	// Above it, a page halves down to link 300 alone and stops before it.
	p, err = w.enclave().Walk(ctx, account, Position{}, 10000)
	if err != nil || len(p.Links) != 1 || p.Next != (Position{N: 300}) {
		t.Fatalf("above it: %+v %v", p, err)
	}
	if _, err := w.enclave().Walk(ctx, account, p.Next, 10000); !errors.Is(err, ErrHistoryTooLarge) {
		t.Fatal(err)
	}
	if p, err := w.enclave().Walk(ctx, account, Position{N: 299}, 1); err != nil || p.Links[0].N != 298 {
		t.Fatalf("below it: %+v %v", p, err)
	}
	h := w.hooked()
	h.ranged = func() error { return errBoom }
	if _, err := w.enclave().Walk(ctx, account, Position{N: 299}, 1); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
}

// L25, the store's half: between two exports a receipt is deleted from the
// table; the second names it missing, and the first's head is on the
// second, since deleting adds no link. With the link's value deleted too,
// the broken number is shown, and no identifier is made up.
func TestL25TwoExports(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	s := w.enclave()
	ids := w.writes(s, account, 5)
	first, err := s.Export(ctx, account, Position{})
	if err != nil {
		t.Fatal(err)
	}
	w.deleteReceipt(account, ids[2])
	second, err := s.Export(ctx, account, Position{})
	if err != nil || second.Head != first.Head {
		t.Fatalf("%+v %v", second.Head, err)
	}
	if second.Links[2].Receipts[0].Verdict != ReceiptMissing || second.Links[2].Receipts[0].ID != ids[2] || second.Links[2].Receipts[0].Doc != nil {
		t.Fatalf("%+v", second.Links[2])
	}
	w.deleteLink(account, 3)
	third, err := s.Export(ctx, account, Position{})
	if err != nil || third.Links[2].Verdict != LinkLost || third.Links[2].Receipts != nil || third.Links[2].Doc != nil {
		t.Fatalf("%+v %v", third.Links[2], err)
	}
	if !bytes.Equal(third.Links[0].Doc, first.Links[0].Doc) || DigestOf(first.Links[0].Doc) != first.Head.Digest {
		t.Fatal("the head's link changed")
	}
	_ = time.Second
}
