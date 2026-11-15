package inbox_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/inboxtest"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/store"
)

// The code review of pull request #103 (ADR 0027's pull request 3, stored
// mail): each finding reproduced, named TestReviewM<n>.

// listedFor is where one page over the box lists the message id, for fs.
func listedFor(t *testing.T, mt inbox.Matcher, b inbox.Box, fs []*query.Canonical, id string) listing {
	t.Helper()
	got, _ := pages(t, mt, b, fs, func() int { return 100 }, 10)
	return got[id]
}

// TestReviewM1AListIDNotReadIsUnknown: a header past 256 KiB before its
// List-Id is stored Partial with no list; whether the message is on a list
// is then unknown, for list exists and for not list exists alike.
func TestReviewM1AListIDNotReadIsUnknown(t *testing.T) {
	ctx := context.Background()
	st := newQStore()
	b := qBox(t)
	var raw strings.Builder
	raw.WriteString("From: a@example.com\r\nSubject: x\r\n")
	for raw.Len() <= inbox.MaxHeaderBytes {
		raw.WriteString("X-Filler: " + strings.Repeat("f", 1000) + "\r\n")
	}
	raw.WriteString("List-Id: <news.example.com>\r\n\r\nhello\r\n")
	id := (&qMsg{at: qAt(0)}).id()
	m := inboxtest.FromRaw(ctx, id, qAt(0), 30, []byte(raw.String()))
	if !m.Meta.Partial || m.Meta.List != "" || m.Meta.Format != inbox.MetaFormat {
		t.Fatalf("partial %v, list %q, format %d", m.Meta.Partial, m.Meta.List, m.Meta.Format)
	}
	if qm := inbox.QueryMessage(&store.Message{MailboxID: b.MailboxID, ID: id}, &m.Meta, nil, nil); qm.ListRecorded {
		t.Fatalf("a list not read is recorded: %+v", qm.List)
	}
	if err := inboxtest.Seed(ctx, st.Memory, st.Memory, b, m); err != nil {
		t.Fatal(err)
	}
	mt := inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{})
	exists := cond(contracts.VarList, contracts.OpExists, "")
	for _, c := range []contracts.Condition{exists, not(exists)} {
		if l := listedFor(t, mt, b, []*query.Canonical{formula(group(c))}, id); l != listedUnknown {
			t.Errorf("%+v: listed %v, want unknown", c, l)
		}
	}
	// A Partial meta whose List-Id was read keeps it known, whatever else
	// was cut; with no Partial, no List-Id is a known absence.
	known := inbox.QueryMessage(&store.Message{ID: id}, &inbox.StoredMeta{Format: inbox.MetaFormat, Partial: true, List: "news.example.com"}, nil, nil)
	none := inbox.QueryMessage(&store.Message{ID: id}, &inbox.StoredMeta{Format: inbox.MetaFormat}, nil, nil)
	if !known.ListRecorded || len(known.List) != 1 || !none.ListRecorded || none.List != nil {
		t.Fatalf("known %+v, none %+v", known, none)
	}
}

// TestReviewM2AListIDAsRFC2919ReadsIt: the identifier is the first <…>
// outside quoted strings and comments, without the whitespace inside it, in
// lower case, of at most 255 bytes of what RFC 2919 allows; a header that is
// not that makes the list unknown, never a known absence.
func TestReviewM2AListIDAsRFC2919ReadsIt(t *testing.T) {
	ctx := context.Background()
	const unknown = "?"
	cases := [][2]string{
		{"List-Id: <News.\r\n Example.COM>\r\n", "news.example.com"},
		{"List-Id: <real.example> (see <decoy.example>)\r\n", "real.example"},
		{"List-Id: \"Weekly <x.example>\" <real.example>\r\n", "real.example"},
		{"List-Id: \"a \\\" <x.example>\" <real.example>\r\n", "real.example"},
		{"List-Id: (a (b <decoy.example>) c) <real.example>\r\n", "real.example"},
		{"List-Id: (a \\) <decoy.example>) <real.example>\r\n", "real.example"},
		{"List-Id: Weekly News <News.Example.COM>\r\n", "news.example.com"},
		{"List-Id: bare.example.org\r\n", "bare.example.org"},
		{"List-Id: bare.example.org (a comment)\r\n", "bare.example.org"},
		{"List-Id: <" + strings.Repeat("a", 250) + ".example>\r\n", unknown},
		{"List-Id: <" + strings.Repeat("a", 240) + ".example>\r\n", strings.Repeat("a", 240) + ".example"},
		{"List-Id: Two Words\r\n", unknown},
		{"List-Id: Name <unclosed.example\r\n", unknown},
		{"List-Id: (unterminated <decoy.example>\r\n", unknown},
		{"List-Id: \"unterminated <decoy.example>\r\n", unknown},
		{"List-Id: <>\r\n", unknown},
		{"List-Id: <a,b.example>\r\n", unknown},
		{"List-Id: \"only a phrase\"\r\n", unknown},
		{"List-Id: stray) <x.example>\r\n", unknown},
		{"List-Id:\r\n", unknown},
		{"List-Id: <first.example>\r\nList-Id: <second.example>\r\n", "first.example"},
		{"X-Other: <not.a.list>\r\n", ""},
		{"List-Id: <" + strings.Repeat("b", inbox.MaxHeaderFieldBytes) + ">\r\n", unknown},
	}
	for _, c := range cases {
		header, want := c[0], c[1]
		raw := "From: a@example.com\r\n" + header + "Subject: Hi\r\n\r\nHello\r\n"
		meta := inbox.Parse(ctx, []byte(raw)).Meta
		m := inbox.QueryMessage(&store.Message{ID: (&qMsg{at: qAt(0)}).id()}, &meta, nil, nil)
		switch {
		case want == unknown:
			if m.ListRecorded || meta.List != "" {
				t.Errorf("%.80q: list %.80q recorded %v, want unknown", header, meta.List, m.ListRecorded)
			}
		case !m.ListRecorded || meta.List != want:
			t.Errorf("%.80q: list %.80q recorded %v, want %.80q", header, meta.List, m.ListRecorded, want)
		}
	}
}

// TestReviewM3NeedsAModelOnlyWhenAModelCanMakeItTrue: a message is listed as
// needing a model only when some answers to its model atoms make the
// formulas true; otherwise it is unknown, though no source is left to read.
func TestReviewM3NeedsAModelOnlyWhenAModelCanMakeItTrue(t *testing.T) {
	st := newQStore()
	b := qBox(t)
	old := &qMsg{at: qAt(0), from: []string{"a@example.com"}, subject: "x", body: "y", list: "news.example.com", old: true}
	id := seedQ(t, st, b, old)
	mt := inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{})
	list := cond(contracts.VarList, contracts.OpIs, "news.example.com")
	about := cond(contracts.VarMessage, contracts.OpAbout, "invoices")
	var many []contracts.Condition
	for i := range 9 {
		many = append(many, cond(contracts.VarMessage, contracts.OpAbout, fmt.Sprintf("topic %d", i)))
	}
	for name, c := range map[string]struct {
		fs   []*query.Canonical
		want listing
	}{
		"list and about":             {[]*query.Canonical{formula(group(list), group(about))}, listedUnknown},
		"list or about":              {[]*query.Canonical{formula(group(list, about))}, listedNeedsModel},
		"list and not about":         {[]*query.Canonical{formula(group(list), group(not(about)))}, listedUnknown},
		"not about":                  {[]*query.Canonical{formula(group(not(about)))}, listedNeedsModel},
		"about and not about":        {[]*query.Canonical{formula(group(about), group(not(about)))}, listedUnknown},
		"about, and not about apart": {[]*query.Canonical{formula(group(about)), formula(group(not(about)))}, listedUnknown},
		"list or about, and about":   {[]*query.Canonical{formula(group(list, about)), formula(group(about))}, listedNeedsModel},
		"nine atoms":                 {[]*query.Canonical{formula(group(many...))}, listedNeedsModel},
	} {
		if l := listedFor(t, mt, b, c.fs, id); l != c.want {
			t.Errorf("%s: listed %v, want %v", name, l, c.want)
		}
	}
}

// TestReviewM4RunsOfExpiredItemsKeepTheSoftDeadline: ten batches of expired
// items only, each scan taking five seconds, end the page with a next at
// the batch where the soft deadline passed, and the pages that follow go on.
func TestReviewM4RunsOfExpiredItemsKeepTheSoftDeadline(t *testing.T) {
	st := newQStore()
	b := qBox(t)
	var expired []string
	for i := range 100 {
		expired = append(expired, seedQ(t, st, b, &qMsg{at: qAt(i), n: i, from: []string{"a@example.com"}, subject: "x", expired: true}))
	}
	live := seedQ(t, st, b, &qMsg{at: qAt(100), n: 100, from: []string{"a@example.com"}, subject: "x"})
	st.PageItems = 10
	var mu sync.Mutex
	clock := qNow
	st.onScan = func() error {
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(5 * time.Second)
		return nil
	}
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	mt := inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{Now: now})
	page, err := mt.Match(context.Background(), b, []*query.Canonical{formula()}, "", 50)
	if err != nil || len(st.scans) != 4 || page.Next != expired[39] || len(page.Messages) != 0 || now().Sub(qNow) > inbox.MatchSoftDeadline {
		t.Fatalf("%d scans, %s elapsed: %+v %v", len(st.scans), now().Sub(qNow), page, err)
	}
	if got, all := pages(t, mt, b, []*query.Canonical{formula()}, func() int { return 50 }, 10); len(got) != 1 || got[live] != listedMatch || len(all) != 3 {
		t.Fatalf("%d pages: %v", len(all), got)
	}
}

// TestReviewM5TheLastExpiredBatchKeepsSearchedTo: a page whose last batch is
// expired items only, with nothing more below, says it searched to that
// batch's last item.
func TestReviewM5TheLastExpiredBatchKeepsSearchedTo(t *testing.T) {
	for _, c := range []struct {
		name      string
		live      bool
		pageItems int
	}{{"one expired item", false, 0}, {"a live message, then a batch of one expired item", true, 1}, {"both in one batch", true, 0}} {
		st := newQStore()
		b := qBox(t)
		if c.live {
			seedQ(t, st, b, &qMsg{at: qAt(0), from: []string{"a@example.com"}, subject: "x"})
		}
		seedQ(t, st, b, &qMsg{at: qAt(1), n: 1, from: []string{"a@example.com"}, subject: "x", expired: true})
		st.PageItems = c.pageItems
		page, err := inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{}).Match(context.Background(), b, []*query.Canonical{formula()}, "", 50)
		if err != nil || page.Next != "" || !page.SearchedTo.Equal(qAt(1)) {
			t.Errorf("%s: %+v %v", c.name, page, err)
		}
	}
}
