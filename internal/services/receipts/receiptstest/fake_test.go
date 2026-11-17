package receiptstest

import (
	"context"
	"errors"
	"testing"

	"github.com/permaily-org/permaily/internal/services/receipts"
)

const account = "acc_abcdefghijklmnopqrstuvwxyz"

// The fake pages as the contract says: newest first, a page ending inside a
// link continues at Next with nothing skipped or repeated, a link that
// cannot be read holds no receipt and still counts, and only the page that
// reaches link 1's first receipt is Done.
func TestTheFakePagesAsTheContract(t *testing.T) {
	ctx := context.Background()
	f := &Fake{}
	if h, err := f.Head(ctx, account); err != nil || h != (receipts.Head{}) {
		t.Fatalf("%+v %v", h, err)
	}
	if w, err := f.Walk(ctx, account, receipts.Position{}, 5); err != nil || !w.Done || len(w.Links) != 0 {
		t.Fatalf("%+v %v", w, err)
	}
	f.Append(account, []byte(`{"v":1}`), []byte(`{"v":1,"kind":"refusal"}`), []byte(`not json`))
	if _, err := f.Link(ctx, account, receipts.Ref{ID: "rcp_x"}); err != nil {
		t.Fatal(err)
	}
	f.Append(account, []byte(`{"v":1}`))
	f.SetLink(account, 2, receipts.LinkLost)
	f.SetReceipt(account, receipts.Position{N: 1, Index: 1}, receipts.ReceiptMissing)

	var seen []receipts.Position
	before := receipts.Position{}
	for pages := 0; ; pages++ {
		w, err := f.Walk(ctx, account, before, 2)
		if err != nil || pages > 5 {
			t.Fatalf("%v after %d pages", err, pages)
		}
		if w.Head.N != 3 || w.Head.Verdict != receipts.LinkOK || w.Head.At.IsZero() {
			t.Fatalf("head %+v", w.Head)
		}
		for _, l := range w.Links {
			if l.Verdict != receipts.LinkOK && (l.Link != nil || len(l.Receipts) != 0) {
				t.Fatalf("link %d read though %s", l.N, l.Verdict)
			}
			for _, r := range l.Receipts {
				seen = append(seen, receipts.Position{N: l.N, Index: r.Index})
				if (r.Verdict == receipts.ReceiptOK && r.Index != 2) != (r.Receipt != nil) {
					t.Fatalf("receipt %d/%d %s %v", l.N, r.Index, r.Verdict, r.Receipt)
				}
			}
		}
		if w.Done {
			break
		}
		before = w.Next
	}
	want := []receipts.Position{{N: 3}, {N: 1, Index: 2}, {N: 1, Index: 1}, {N: 1}}
	if len(seen) != len(want) {
		t.Fatalf("seen %v", seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("seen %v", seen)
		}
	}

	// The export holds exact bytes, and the same pages.
	e, err := f.Export(ctx, account, receipts.Position{N: 1, Index: 2})
	if err != nil || !e.Done || len(e.Links) != 1 || len(e.Links[0].Receipts) != 2 || string(e.Links[0].Receipts[1].Doc) != `{"v":1}` || e.Links[0].Receipts[0].Doc != nil {
		t.Fatalf("%+v %v", e, err)
	}
	if e, err := f.Export(ctx, account, receipts.Position{N: 1}); err != nil || !e.Done || len(e.Links) != 0 {
		t.Fatalf("%+v %v", e, err)
	}
	for _, bad := range []receipts.Position{{N: 4}, {N: 1, Index: 4}, {N: 1, Index: -1}} {
		if _, err := f.Walk(ctx, account, bad, 1); !errors.Is(err, ErrBefore) {
			t.Fatalf("%+v: %v", bad, err)
		}
		if _, err := f.Export(ctx, account, bad); !errors.Is(err, ErrBefore) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	if _, err := f.Walk(ctx, account, receipts.Position{}, 0); err == nil {
		t.Fatal("a page of no receipt")
	}

	f.SetLink(account, 3, receipts.LinkForked)
	if h, _ := f.Head(ctx, account); h.Digest != (receipts.Digest{}) || !h.At.IsZero() {
		t.Fatalf("a forked head %+v", h)
	}
}

// A page visits at most WalkLinksMax links, broken ones included, and is
// then not Done.
func TestTheFakesPageIsBoundedByLinks(t *testing.T) {
	f := &Fake{}
	for range receipts.WalkLinksMax + 1 {
		f.Append(account, []byte(`{}`))
	}
	for n := uint64(1); n <= receipts.WalkLinksMax+1; n++ {
		f.SetLink(account, n, receipts.LinkUnreadable)
	}
	w, err := f.Walk(context.Background(), account, receipts.Position{}, 10)
	if err != nil || w.Done || len(w.Links) != receipts.WalkLinksMax || w.Next != (receipts.Position{N: 2}) {
		t.Fatalf("%d links, next %+v, done %t, %v", len(w.Links), w.Next, w.Done, err)
	}
}

// Err is every call's answer, and the auditor reports Pass and every kind.
func TestTheFakeFailsAndAudits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &Fake{}
	if _, err := f.Link(ctx, account, receipts.Ref{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	pass := &receipts.AuditPass{P: 1}
	f.Pass, f.Found = pass, map[string]int64{"link_lost": 2}
	if p, err := f.Audit(ctx); p != pass || err != nil {
		t.Fatal(err)
	}
	if p, err := f.Audited(ctx); p != pass || err != nil {
		t.Fatal(err)
	}
	if b := f.Broken(); b["link_lost"] != 2 || b["receipt_missing"] != 0 || len(b) < 11 {
		t.Fatalf("%v", b)
	}
	f.Err = errors.New("down")
	if _, err := f.Link(ctx, account, receipts.Ref{}); !errors.Is(err, f.Err) {
		t.Fatal(err)
	}
	if _, err := f.Head(ctx, account); !errors.Is(err, f.Err) {
		t.Fatal(err)
	}
	if _, err := f.Walk(ctx, account, receipts.Position{}, 1); !errors.Is(err, f.Err) {
		t.Fatal(err)
	}
	if _, err := f.Export(ctx, account, receipts.Position{}); !errors.Is(err, f.Err) {
		t.Fatal(err)
	}
	f.Now = nil
	g := &Fake{Now: f.now}
	if g.now() != f.now() {
		t.Fatal("now")
	}
}
