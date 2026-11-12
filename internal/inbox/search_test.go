package inbox_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/inboxtest"
	"github.com/permaily-org/permaily/internal/store"
)

// searchAll asks every page of a search, and returns the identifiers found
// and the pages.
func searchAll(t *testing.T, r inbox.Reader, b inbox.Box, q string, limit int) ([]string, []*contracts.MessagePage) {
	t.Helper()
	var ids []string
	var pages []*contracts.MessagePage
	after := ""
	for range 10_000 {
		page, err := r.Search(context.Background(), b, q, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		if page.Messages == nil {
			t.Fatal("messages is null")
		}
		if len(page.Messages) > limit {
			t.Fatalf("%d found for a limit of %d", len(page.Messages), limit)
		}
		pages = append(pages, page)
		for _, m := range page.Messages {
			ids = append(ids, m.ID)
		}
		if page.Next == "" {
			return ids, pages
		}
		after = page.Next
	}
	t.Fatal("the search never ended")
	return nil, nil
}

// R5: what a search matches: every term, in a sender's name or address, the
// subject or the text, without regard to case.
func TestR5WhatASearchFinds(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	at := now.Add(-time.Hour)
	seed := func(from contracts.MailAddress, subject, text string) string {
		w.n++
		mid := id(at.Add(time.Duration(w.n)*time.Second), w.n)
		e := &inbox.SearchEntry{From: []contracts.MailAddress{from}, Subject: subject, Text: text, ReceivedAt: at.Add(time.Duration(w.n) * time.Second)}
		if err := inboxtest.Seed(ctx, w.st, w.st, w.b, inboxtest.Message{ID: mid, ReceivedAt: e.ReceivedAt, RetentionDays: 30, Raw: []byte(plainMail), Meta: inbox.StoredMeta{Subject: subject}, Search: e}); err != nil {
			t.Fatal(err)
		}
		return mid
	}
	invoice := seed(contracts.MailAddress{Name: "Billing", Address: "billing@shop.example"}, "Your Invoice", "Total: 12 EUR")
	trip := seed(contracts.MailAddress{Name: "Ann Lee", Address: "ann@example.org"}, "Trip", "The train leaves at nine")
	both := seed(contracts.MailAddress{Address: "bob@example.org"}, "invoice for the train", "")
	for _, c := range []struct {
		q    string
		want []string
	}{
		{"invoice", []string{both, invoice}},
		{"INVOICE train", []string{both}},
		{"lee", []string{trip}},
		{"shop.example", []string{invoice}},
		{"12 eur", []string{invoice}},
		{"nine train", []string{trip}},
		{"nothing", nil},
		{"invoice nothing", nil},
	} {
		got, _ := searchAll(t, w.r, w.b, c.q, 50)
		if !slices.Equal(got, c.want) {
			t.Errorf("%q: %v, want %v", c.q, got, c.want)
		}
	}

	t.Run("a match whose item is gone is left out; one that does not open is not a match", func(t *testing.T) {
		if err := w.mem.DeleteMessage(ctx, w.b.MailboxID, invoice); err != nil {
			t.Fatal(err)
		}
		other := newBox(t, w.b.MailboxID)
		if err := inboxtest.Seed(ctx, w.st, w.st, other, inboxtest.Message{ID: id(at, 777), ReceivedAt: at, RetentionDays: 30, Raw: []byte(plainMail), Meta: inbox.StoredMeta{Subject: "invoice"}}); err != nil {
			t.Fatal(err)
		}
		got, _ := searchAll(t, w.r, w.b, "invoice", 50)
		if !slices.Equal(got, []string{both}) {
			t.Errorf("%v", got)
		}
	})
}

// The code review of PR #90: search matched lower case, not case: "ος"
// (final sigma) did not find "ΟΣ". Terms and fields are compared by Unicode
// simple case folding, rune by rune, without normalisation.
func TestR5SearchIsCaselessForUnicode(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	at := now.Add(-time.Hour)
	seed := func(subject string) string {
		w.n++
		when := at.Add(time.Duration(w.n) * time.Second)
		mid := id(when, w.n)
		e := &inbox.SearchEntry{From: []contracts.MailAddress{{Address: "a@example.org"}}, Subject: subject, ReceivedAt: when}
		if err := inboxtest.Seed(ctx, w.st, w.st, w.b, inboxtest.Message{ID: mid, ReceivedAt: when, RetentionDays: 30, Raw: []byte(plainMail), Meta: inbox.StoredMeta{Subject: subject}, Search: e}); err != nil {
			t.Fatal(err)
		}
		return mid
	}
	sigma := seed("ΟΣ")
	dotted := seed("İzmir")
	plainI := seed("izmir")
	dotless := seed("ızmir")
	sharp := seed("Straße")
	ascii := seed("Invoice KELVIN")
	accent := seed("café")
	for _, c := range []struct {
		q    string
		want []string
	}{
		{"ΟΣ", []string{sigma}},
		{"οσ", []string{sigma}},
		{"ος", []string{sigma}},
		{"Ος", []string{sigma}},
		// i and I are one; İ and ı each fold only to themselves.
		{"izmir", []string{plainI}},
		{"IZMIR", []string{plainI}},
		{"İZMIR", []string{dotted}},
		{"izmİr", nil},
		{"ızmır", nil},
		{"ızmir", []string{dotless}},
		// ß and ẞ are one; ss is not (that would be full folding).
		{"STRAẞE", []string{sharp}},
		{"straße", []string{sharp}},
		{"strasse", nil},
		{"iNVOICE", []string{ascii}},
		{"Kelvin", []string{ascii}},
		// Accents stay significant: no normalisation.
		{"cafe", nil},
		{"CAFÉ", []string{accent}},
		{"café", nil},
	} {
		got, _ := searchAll(t, w.r, w.b, c.q, 50)
		if !slices.Equal(got, c.want) {
			t.Errorf("%q: %v, want %v", c.q, got, c.want)
		}
	}
}

// R5: a page reads at most 2,000 entries, expired ones and ones that do not
// open counted; a page that found nothing has next and searched_to, and the
// next page goes on below.
func TestR5APageReadsAtMost2000Entries(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	base := now.Add(-20 * 24 * time.Hour)
	match := w.seedSearch(base, 30, "the needle")
	other := newBox(t, w.b.MailboxID)
	var above []string // the newest first, once reversed
	for i := range 2050 {
		at := base.Add(time.Duration(i+1) * time.Minute)
		w.n++
		mid := id(at, w.n)
		m := inboxtest.Message{ID: mid, ReceivedAt: at, RetentionDays: 30, Raw: []byte(plainMail), Search: &inbox.SearchEntry{Subject: "hay", ReceivedAt: at}}
		b := w.b
		switch i % 3 {
		case 0:
			m.ReceivedAt, m.RetentionDays = at.AddDate(0, 0, -30), 7 // expired
		case 1:
			b = other // does not open under the box's key
		}
		if err := inboxtest.Seed(ctx, w.st, w.st, b, m); err != nil {
			t.Fatal(err)
		}
		above = append(above, mid)
	}
	slices.Reverse(above)
	// Whole pages of a hundred, and pages cut short (DynamoDB's 1 MB) that
	// do not divide 2,000: the page stops at 2,000 read exactly.
	for _, pageItems := range []int{0, 33} {
		w.mem.PageItems = pageItems
		page, err := w.r.Search(ctx, w.b, "needle", "", 50)
		if err != nil {
			t.Fatal(err)
		}
		last := above[inbox.SearchMaxRead-1]
		if len(page.Messages) != 0 || page.Next != last {
			t.Fatalf("page items %d: %d found, next %q, want %q", pageItems, len(page.Messages), page.Next, last)
		}
		if want := base.Add(time.Duration(2050-inbox.SearchMaxRead+1) * time.Minute); !page.SearchedTo.Equal(want) {
			t.Fatalf("searched to %v, want %v", page.SearchedTo, want)
		}
		page, err = w.r.Search(ctx, w.b, "needle", page.Next, 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Messages) != 1 || page.Messages[0].ID != match || page.Next != "" || !page.SearchedTo.Equal(base) {
			t.Fatalf("second page %+v", page)
		}
	}
}

func (w *world) seedSearch(at time.Time, days int, text string) string {
	w.t.Helper()
	w.n++
	mid := id(at, w.n)
	if err := inboxtest.Seed(context.Background(), w.st, w.st, w.b, inboxtest.Message{ID: mid, ReceivedAt: at, RetentionDays: days, Raw: []byte(plainMail),
		Search: &inbox.SearchEntry{Subject: "s", Text: text, ReceivedAt: at}}); err != nil {
		w.t.Fatal(err)
	}
	return mid
}

// R5: a last page of expired entries only says how far back it looked, from
// the identifier of the last entry read.
func TestR5ALastPageOfExpiredEntriesSaysHowFarItLooked(t *testing.T) {
	w := newWorld(t)
	oldest := now.AddDate(0, 0, -40)
	var first string
	for i := range 30 {
		mid := w.seedSearch(oldest.Add(time.Duration(i)*time.Hour), 7, "x")
		if i == 0 {
			first = mid
		}
	}
	page, err := w.r.Search(context.Background(), w.b, "x", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 0 || page.Next != "" || !page.SearchedTo.Equal(oldest) {
		t.Fatalf("%+v (first %s)", page, first)
	}

	t.Run("an empty mailbox looked at nothing", func(t *testing.T) {
		empty := newWorld(t)
		page, err := empty.r.Search(context.Background(), empty.b, "x", "", 50)
		if err != nil || len(page.Messages) != 0 || page.Next != "" || !page.SearchedTo.IsZero() {
			t.Fatalf("%+v %v", page, err)
		}
	})
}

// R5: pages of every shape, and short pages (DynamoDB's 1 MB, played by
// PageItems), find every match once, newest first; a page that stops inside
// a batch goes on right after its last match.
func TestR5SearchPagesOfEveryShape(t *testing.T) {
	w := newWorld(t)
	var want []string
	for i := range 120 {
		at := now.Add(-time.Duration(500-i) * time.Minute)
		switch i % 4 {
		case 0:
			want = append(want, w.seedSearch(at, 30, "match"))
		case 1:
			w.seedSearch(at.AddDate(0, 0, -60), 30, "match") // expired
		default:
			w.seedSearch(at, 30, "other")
		}
	}
	slices.Reverse(want)
	for _, pageItems := range []int{0, 1, 3, 33, 100} {
		for _, limit := range []int{1, 2, 7, 30, 100} {
			w.mem.PageItems = pageItems
			got, pages := searchAll(t, w.r, w.b, "match", limit)
			if !slices.Equal(got, want) {
				t.Fatalf("page items %d, limit %d: %d found, want %d", pageItems, limit, len(got), len(want))
			}
			for _, p := range pages[:len(pages)-1] {
				if p.SearchedTo.IsZero() {
					t.Fatalf("a page without searched_to")
				}
			}
		}
	}
}

// R5 on DynamoDB Local: entries of 30 KiB cut a scan at 1 MB, near the
// thirty-third, with More and fewer than a hundred read; the search goes on
// from Last, in the same page or the next, and skips nothing.
func TestR5APageCutAtOneMegabyteOnDynamoDB(t *testing.T) {
	w := emulatedWorld(t)
	big := strings.Repeat("y", 30<<10)
	var want []string
	for i := range 45 {
		at := now.Add(-time.Duration(100-i) * time.Minute)
		text := big
		if i%2 == 0 {
			text = "needle " + big
		}
		mid := w.seedSearch(at, 30, text)
		if i%2 == 0 {
			want = append(want, mid)
		}
	}
	slices.Reverse(want)
	scans := &scanCounter{ReadStore: w.st}
	r := inbox.NewReader(scans)
	for _, limit := range []int{100, 1, 7, 23} {
		scans.scans = nil
		got, _ := searchAll(t, r, w.b, "needle", limit)
		if !slices.Equal(got, want) {
			t.Fatalf("limit %d: %d found, want %d", limit, len(got), len(want))
		}
		if limit == 100 && (len(scans.scans) < 2 || !scans.scans[0].More || scans.scans[0].Read >= 100) {
			t.Fatalf("no page was cut at 1 MB: %+v", scans.scans)
		}
	}
	// And the reader's other paths on the emulators.
	m, err := w.r.Get(context.Background(), w.b, want[0])
	if err != nil || m.Text != "Hello there\n" {
		t.Fatalf("%+v %v", m, err)
	}
	if n, err := w.r.Delete(context.Background(), w.b, want[:2]); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	list, err := w.r.List(context.Background(), w.b, "", 100)
	if err != nil || len(list.Messages) != 43 {
		t.Fatalf("%d listed, %v", len(list.Messages), err)
	}
}

// scanCounter keeps what each scan of entries answered.
type scanCounter struct {
	inbox.ReadStore
	scans []store.EntryScan
}

func (s *scanCounter) ScanEntriesDown(ctx context.Context, partition, prefix, before string, maxRead int) (*store.EntryScan, error) {
	scan, err := s.ReadStore.ScanEntriesDown(ctx, partition, prefix, before, maxRead)
	if err == nil {
		s.scans = append(s.scans, store.EntryScan{Read: scan.Read, Last: scan.Last, More: scan.More})
	}
	return scan, err
}
