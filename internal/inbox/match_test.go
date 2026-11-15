package inbox_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
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

func pick[T any](rng *rand.Rand, vs ...T) T { return vs[rng.IntN(len(vs))] }

// qAt is the time of the i-th message of a generated mailbox: one an hour,
// newest first, at seconds that vary.
func qAt(i int) time.Time {
	return qNow.Add(-time.Duration(i+1)*time.Hour - time.Duration(i%60)*time.Second)
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

var qLong = strings.Repeat("filler ", 2400) // 16,800 bytes: past what an entry keeps

// genMessage is a message over a universe chosen so conditions collide.
func genMessage(rng *rand.Rand, i int, spoil bool) *qMsg {
	q := &qMsg{at: qAt(i), n: i}
	q.from = pick(rng, []string{"alice@example.com"}, []string{"bob@other.example"}, []string{"carol@sub.example.com", "bob@other.example"})
	q.to = pick(rng, nil, []string{"team@example.com"})
	q.subject = pick(rng, "Invoice 2026", "hello", "Re: invoices")
	q.body = pick(rng, "pay the invoice", "nothing here", "", qLong+"invoices at the end", qLong+"café", strings.Repeat(" ", 16377)+"invoices")
	q.list = pick(rng, "", "news.example.com")
	q.attach = pick(rng, nil, []contracts.MessageAttachment{{Index: 0, Name: "bill.pdf", Type: "application/pdf", Size: 3}})
	q.old = rng.IntN(3) == 0
	q.partial = rng.IntN(8) == 0
	if spoil {
		q.unparsed = rng.IntN(15) == 0
		q.confirmation = rng.IntN(15) == 0
		q.expired = rng.IntN(12) == 0
		q.badMeta = rng.IntN(20) == 0
		q.badEntry = rng.IntN(15) == 0
		q.noEntry = !q.badEntry && rng.IntN(15) == 0
		q.rawTooLarge = rng.IntN(20) == 0
		if q.expired {
			q.badMeta, q.rawTooLarge = false, false // an expired item is not read again to spoil it
		}
	}
	return q
}

// genFormulas is one or two formulas evaluated together (a scope and a
// request), each of one to three groups of one or two conditions.
func genFormulas(rng *rand.Rand, b inbox.Box, size bool) []*query.Canonical {
	pool := []contracts.Condition{
		cond(contracts.VarFromAddress, contracts.OpIs, "alice@example.com"),
		cond(contracts.VarFromDomain, contracts.OpIs, "example.com"),
		cond(contracts.VarRecipientDomain, contracts.OpIs, "example.com"),
		cond(contracts.VarSubject, contracts.OpContains, "invoice"),
		cond(contracts.VarBody, contracts.OpContains, "invoices"),
		cond(contracts.VarBody, contracts.OpContains, "cafe"),
		cond(contracts.VarBody, contracts.OpContains, "pay"),
		cond(contracts.VarAttachments, contracts.OpExists, ""),
		cond(contracts.VarAttachmentName, contracts.OpContains, ".pdf"),
		cond(contracts.VarList, contracts.OpExists, ""),
		cond(contracts.VarList, contracts.OpIs, "news.example.com"),
		cond(contracts.VarMailbox, contracts.OpIs, b.MailboxID),
		cond(contracts.VarMessage, contracts.OpAbout, "invoices"),
		cond(contracts.VarReceived, contracts.OpSince, rfc(qAt(rng.IntN(150)))),
		cond(contracts.VarReceived, contracts.OpBefore, rfc(qAt(rng.IntN(150)))),
	}
	if size {
		pool = append(pool, cond(contracts.VarSize, contracts.OpAtLeast, "10000"), cond(contracts.VarSize, contracts.OpBelow, "10000"))
	}
	var fs []*query.Canonical
	for range 1 + rng.IntN(2) {
		var groups [][]contracts.Condition
		for range 1 + rng.IntN(3) {
			var g []contracts.Condition
			for range 1 + rng.IntN(2) {
				c := pool[rng.IntN(len(pool))]
				if c.Var != contracts.VarReceived && rng.IntN(3) == 0 {
					c = not(c)
				}
				g = append(g, c)
			}
			groups = append(groups, g)
		}
		fs = append(fs, formula(groups...))
	}
	return fs
}

// Q27 (ADR 0027): staged equals whole. What Match lists as a match, needing
// a model, unknown or nothing equals the evaluation of every source the
// acquisition policy reads, read at once; and no raw message is read for a
// formula without a body condition. The one difference allowed is "the
// exception of old messages" seen from the other side: a message stored
// before ADR 0027 whose raw message the whole read takes for a body
// condition that the staged read never needed (the value settled without
// it) has its size known only in the whole read; then the staged listing is
// the whole read's without that size.
func TestQ27StagedEqualsWhole(t *testing.T) {
	st := newQStore()
	b := qBox(t)
	rng := rand.New(rand.NewPCG(27, 1))
	var msgs []*qMsg
	for i := range 120 {
		q := genMessage(rng, i, true)
		msgs = append(msgs, q)
		seedQ(t, st, b, q)
	}
	mt := inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{})
	exceptions := 0
	for trial := range 120 {
		fs := genFormulas(rng, b, true)
		st.reset()
		got, _ := pages(t, mt, b, fs, func() int { return 100 }, 100)
		if !hasBody(fs) && len(st.raws)+len(st.entries) > 0 {
			t.Fatalf("trial %d: %d raw reads and %d entries read without a body condition", trial, len(st.raws), len(st.entries))
		}
		for _, q := range msgs {
			l, listed := got[q.id()]
			if q.expired {
				if listed {
					t.Fatalf("trial %d: an expired message listed", trial)
				}
				continue
			}
			want, withoutRawSize, readRaw := reference(b, fs, q)
			if l == want {
				continue
			}
			if readRaw && q.old && l == withoutRawSize {
				// The exception holds only where the staged read did not read
				// that raw message: it settled the value without it.
				if slices.Contains(st.raws, q.id()) {
					t.Fatalf("trial %d, message %d: the exception taken, but the staged read read its raw message", trial, q.n)
				}
				exceptions++
				continue
			}
			t.Fatalf("trial %d, message %d (%+v): staged %v, whole %v", trial, q.n, *q, l, want)
		}
	}
	t.Logf("%d listings where the whole read knew an old message's size from a raw read the staged one did not need", exceptions)
}

// Q27: the exception of old messages, tested. On a message stored before
// ADR 0027, of 2 MiB, A = size at_least 1048576 is unknown alone, and no raw
// message is read for it; with B = A and a body condition over a cut entry,
// the raw message is read, A's size is known, and A and B are both true.
func TestQ27TheExceptionOfOldMessages(t *testing.T) {
	st := newQStore()
	b := qBox(t)
	q := &qMsg{at: qAt(0), from: []string{"a@example.com"}, subject: "big", body: "invoices " + strings.Repeat("words ", (2<<20)/6), old: true}
	seedQ(t, st, b, q)
	mt := inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{})
	sizeA := []contracts.Condition{cond(contracts.VarSize, contracts.OpAtLeast, "1048576")}
	a := formula(sizeA)
	page, err := mt.Match(context.Background(), b, []*query.Canonical{a}, "", 50)
	if err != nil || len(page.Unknown) != 1 || len(page.Messages) != 0 || len(st.raws) != 0 || len(st.entries) != 0 {
		t.Fatalf("A alone: %+v %v, raw reads %d", page, err, len(st.raws))
	}
	m := inbox.QueryMessage(&store.Message{MailboxID: b.MailboxID, ID: q.id()}, &inbox.StoredMeta{}, nil, nil)
	if res := standIn([]*query.Canonical{a}, m, nil, query.ForQuery); res.Value != query.Unknown || res.Needs&query.FromText != 0 {
		t.Fatalf("A alone needs %v", res)
	}
	bf := formula(sizeA, group(cond(contracts.VarBody, contracts.OpContains, "invoices")))
	for _, fs := range [][]*query.Canonical{{bf}, {a, bf}} {
		st.reset()
		page, err = mt.Match(context.Background(), b, fs, "", 50)
		if err != nil || len(page.Messages) != 1 || len(st.raws) != 1 {
			t.Fatalf("B: %+v %v, raw reads %d", page, err, len(st.raws))
		}
		if want, _, readRaw := reference(b, fs, q); want != listedMatch || !readRaw {
			t.Fatalf("the whole read: %v %v", want, readRaw)
		}
	}
}

// Q28 (ADR 0027): pages skip nothing. Over generated mailboxes of up to 2,000
// messages, with runs of expired items longer than a batch, pages of random
// limits under low bounds (7 items read, two raw reads, few undecided) that
// end inside batches and before a message's raw text: every message is
// listed where the whole read lists it, none twice, and next is absent only
// at the end.
func TestQ28PagesSkipNothing(t *testing.T) {
	rng := rand.New(rand.NewPCG(28, 1))
	for _, n := range []int{0, 1, 7, 37, 260, 2000} {
		st := newQStore()
		b := qBox(t)
		var msgs []*qMsg
		for i := range n {
			q := genMessage(rng, i, true)
			if (i/150)%4 == 3 {
				q.expired, q.badMeta, q.rawTooLarge = true, false, false
			}
			msgs = append(msgs, q)
			seedQ(t, st, b, q)
		}
		trials := 6
		if n == 2000 {
			trials = 2
		}
		for trial := range trials {
			fs := genFormulas(rng, b, false)
			st.PageItems = rng.IntN(4)
			mt := inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{Read: 7, TextReads: 2, Undecided: 1 + rng.IntN(5)})
			got, _ := pages(t, mt, b, fs, func() int { return 1 + rng.IntN(5) }, 3*n+10)
			for _, q := range msgs {
				l, listed := got[q.id()]
				if q.expired {
					if listed {
						t.Fatalf("%d messages, trial %d: an expired message listed", n, trial)
					}
					continue
				}
				if want, _, _ := reference(b, fs, q); l != want {
					t.Fatalf("%d messages, trial %d, message %d: listed %v, want %v", n, trial, q.n, l, want)
				}
			}
		}
	}
}

// Q28: a page that reads only expired items still advances next past them,
// as ADR 0026's Scan.Last does, and is not stuck.
func TestQ28APageOfExpiredItemsAdvancesNext(t *testing.T) {
	st := newQStore()
	b := qBox(t)
	var live []string
	for i := range 14 {
		q := &qMsg{at: qAt(i), n: i, from: []string{"a@example.com"}, subject: "x", expired: i < 12}
		if id := seedQ(t, st, b, q); !q.expired {
			live = append(live, id)
		}
	}
	mt := inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{Read: 7})
	got, all := pages(t, mt, b, []*query.Canonical{formula()}, func() int { return 10 }, 10)
	if len(all) != 3 || len(all[0].Messages)+len(all[1].Messages) != 0 || all[0].Next != (&qMsg{at: qAt(5), n: 5}).id() ||
		all[1].Next != (&qMsg{at: qAt(11), n: 11}).id() || !all[0].SearchedTo.Equal(qAt(5).Truncate(time.Nanosecond)) || len(got) != 2 ||
		got[live[0]] != listedMatch || got[live[1]] != listedMatch {
		t.Fatalf("%d pages: %+v", len(all), all)
	}
}

// recordingEval is standIn, recording which messages it evaluated.
type recordingEval struct {
	mu   sync.Mutex
	seen []time.Time
}

func (r *recordingEval) eval(fs []*query.Canonical, m *query.Message, a query.Answers, mode query.Mode) query.Result {
	r.mu.Lock()
	r.seen = append(r.seen, m.Received)
	r.mu.Unlock()
	return standIn(fs, m, a, mode)
}

// Q29 (ADR 0027): the received bounds are used. With received before t the
// scan reads no item at or after t; with received since s no message below s
// is evaluated or listed, the scan stops after the first batch that crosses
// s, and at most one batch is read below it.
func TestQ29TheReceivedBoundsAreUsed(t *testing.T) {
	st := newQStore()
	b := qBox(t)
	for i := range 300 {
		seedQ(t, st, b, &qMsg{at: qAt(i), n: i, from: []string{"a@example.com"}, subject: "x"})
	}
	before, since := qAt(100), qAt(250)
	upper, lower := inbox.IDAt(before), inbox.IDAt(since)
	for name, fs := range map[string][]*query.Canonical{
		"before":            {formula(group(cond(contracts.VarReceived, contracts.OpBefore, rfc(before))))},
		"since":             {formula(group(cond(contracts.VarReceived, contracts.OpSince, rfc(since))))},
		"both":              {formula(group(cond(contracts.VarReceived, contracts.OpSince, rfc(since))), group(cond(contracts.VarReceived, contracts.OpBefore, rfc(before))))},
		"scope and request": {formula(group(cond(contracts.VarReceived, contracts.OpBefore, rfc(before)))), formula(group(cond(contracts.VarReceived, contracts.OpSince, rfc(since))))},
		"neither": {formula(group(cond(contracts.VarReceived, contracts.OpBefore, rfc(before)), cond(contracts.VarSubject, contracts.OpContains, "x")),
			group(not(cond(contracts.VarReceived, contracts.OpSince, rfc(since)))), group(cond(contracts.VarReceived, contracts.OpBefore, "not a time")))},
	} {
		rec := &recordingEval{}
		mt := inbox.NewMatcherWith(st, rec.eval, inbox.MatchBounds{})
		hasBefore, hasSince := name != "since" && name != "neither", name != "before" && name != "neither"
		got := map[string]bool{}
		cursor, scans := "", 0
		for page := 0; ; page++ {
			if page > 10 {
				t.Fatalf("%s: too many pages", name)
			}
			st.reset()
			p, err := mt.Match(context.Background(), b, fs, cursor, 60)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range p.Messages {
				got[m.ID] = true
			}
			// Within a page: no item at or after before, no scan after the
			// batch that crossed since, and at most one batch below it.
			belowSince, crossed := 0, false
			for i, scan := range st.scans {
				if crossed {
					t.Fatalf("%s, page %d: scan %d after the batch that crossed since", name, page, i)
				}
				for _, m := range scan.Messages {
					if hasBefore && m.ID >= upper {
						t.Fatalf("%s: read %s, at or after before", name, m.ID)
					}
					if m.ID < lower {
						belowSince++
					}
				}
				crossed = hasSince && scan.Last < lower
			}
			if hasSince && belowSince > store.MaxScanRead {
				t.Fatalf("%s, page %d: %d read below since", name, page, belowSince)
			}
			if hasBefore && page == 0 && st.befores[0] != upper {
				t.Fatalf("%s: the first scan starts below %q", name, st.befores[0])
			}
			scans += len(st.scans)
			if p.Next == "" {
				if hasSince && !crossed {
					t.Fatalf("%s: the last page did not cross since", name)
				}
				break
			}
			cursor = p.Next
		}
		for i := range 300 {
			at := qAt(i)
			want := (!hasBefore || at.Before(before)) && (!hasSince || !at.Before(since)) && name != "neither"
			if got[(&qMsg{at: at, n: i}).id()] != want {
				t.Fatalf("%s: message %d listed %v", name, i, !want)
			}
		}
		for _, at := range rec.seen {
			if hasSince && at.Before(since) {
				t.Fatalf("%s: evaluated a message received before since", name)
			}
		}
		if name == "neither" && scans < 4 {
			t.Fatalf("neither bounds the scan: %d scans", scans)
		}
	}
	// A cursor below since, or since after before, is the end at once.
	st.reset()
	mt := inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{})
	for _, c := range []struct {
		fs     []*query.Canonical
		cursor string
	}{
		{[]*query.Canonical{formula(group(cond(contracts.VarReceived, contracts.OpSince, rfc(since))))}, (&qMsg{at: qAt(280), n: 280}).id()},
		{[]*query.Canonical{formula(group(cond(contracts.VarReceived, contracts.OpSince, rfc(before))), group(cond(contracts.VarReceived, contracts.OpBefore, rfc(since))))}, ""},
	} {
		page, err := mt.Match(context.Background(), b, c.fs, c.cursor, 10)
		if err != nil || page.Next != "" || len(page.Messages) != 0 || len(st.scans) != 0 {
			t.Fatalf("%+v %v %d", page, err, len(st.scans))
		}
	}
}

// Q30 (ADR 0027), the part in inbox: a grant is taken before each raw read
// and given back before the next, never two held by one page; a grant
// refused fails the page with ErrTooManyRequests; the soft deadline ends a
// page with next; the request's deadline ends it with its error.
func TestQ30TheBudgetsOfAPage(t *testing.T) {
	st := newQStore()
	b := qBox(t)
	var msgIDs []string
	for i := range 5 {
		msgIDs = append(msgIDs, seedQ(t, st, b, &qMsg{at: qAt(i), n: i, from: []string{"a@example.com"}, subject: "x", body: qLong + "invoices"}))
	}
	body := []*query.Canonical{formula(group(cond(contracts.VarBody, contracts.OpContains, "invoices")))}
	g := &qGrants{}
	st.onRaw = func(string) error {
		if g.heldNow() != 1 {
			return fmt.Errorf("a raw read with %d grants held", g.heldNow())
		}
		return nil
	}
	mt := inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{})
	ctx := inbox.WithReadBudget(context.Background(), g)
	page, err := mt.Match(ctx, b, body, "", 50)
	if err != nil || len(page.Messages) != 5 || g.granted != 5 || g.given != 5 || g.maxHeld != 1 || g.heldNow() != 0 {
		t.Fatalf("%+v %v; granted %d given %d max held %d", page, err, g.granted, g.given, g.maxHeld)
	}
	g.refuse = contracts.ErrTooManyRequests
	if _, err := mt.Match(ctx, b, body, "", 50); !errors.Is(err, contracts.ErrTooManyRequests) {
		t.Fatalf("a grant refused: %v", err)
	}
	// The soft deadline: every evaluation takes 25 seconds.
	var mu sync.Mutex
	clock := qNow
	slow := func(fs []*query.Canonical, m *query.Message, a query.Answers, mode query.Mode) query.Result {
		mu.Lock()
		clock = clock.Add(25 * time.Second)
		mu.Unlock()
		return standIn(fs, m, a, mode)
	}
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	mt = inbox.NewMatcherWith(st, slow, inbox.MatchBounds{Now: now})
	page, err = mt.Match(context.Background(), b, []*query.Canonical{formula()}, "", 50)
	if err != nil || slices.Compare(ids(page.Messages), msgIDs[:1]) != 0 || page.Next != msgIDs[0] {
		t.Fatalf("the soft deadline: %+v %v", page, err)
	}
	// The request's deadline, before the page and in the middle of it.
	done, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{}).Match(done, b, body, "", 50); !errors.Is(err, context.Canceled) {
		t.Fatalf("an ended request: %v", err)
	}
	for _, at := range []string{"eval", "raw", "last eval"} {
		ctx, cancel := context.WithCancel(context.Background())
		eval, fs, st := standIn, body, st
		st.onRaw = nil
		if at != "raw" {
			// A formula of the meta alone: the page notices before its next
			// message, or before it answers.
			fs = []*query.Canonical{formula()}
			if at == "last eval" {
				st = newQStore()
				seedQ(t, st, b, &qMsg{at: qAt(0), from: []string{"a@example.com"}, subject: "x"})
			}
			eval = func(fs []*query.Canonical, m *query.Message, a query.Answers, mode query.Mode) query.Result {
				cancel()
				return standIn(fs, m, a, mode)
			}
		} else {
			st.onRaw = func(string) error { cancel(); return nil }
		}
		if _, err := inbox.NewMatcherWith(st, eval, inbox.MatchBounds{}).Match(ctx, b, fs, "", 50); !errors.Is(err, context.Canceled) {
			t.Fatalf("a request that ends at the %s: %v", at, err)
		}
		cancel()
	}
	st.onRaw = nil
}

// Q31 (ADR 0027): a Gmail forwarding confirmation is never listed, whatever
// the formula, even one that holds everything; its entry and raw message
// are never read.
func TestQ31AGmailConfirmationIsNeverListed(t *testing.T) {
	st := newQStore()
	b := qBox(t)
	conf := seedQ(t, st, b, &qMsg{at: qAt(0), from: []string{"forwarding-noreply@google.com"}, subject: "Gmail Forwarding Confirmation", body: qLong + "invoices", confirmation: true})
	other := seedQ(t, st, b, &qMsg{at: qAt(1), n: 1, from: []string{"a@example.com"}, subject: "Gmail Forwarding Confirmation", body: "invoices"})
	always := func([]*query.Canonical, *query.Message, query.Answers, query.Mode) query.Result {
		return query.Result{Value: query.True}
	}
	for i, eval := range []inbox.Evaluate{standIn, always} {
		for _, fs := range [][]*query.Canonical{
			nil, {formula()},
			{formula(group(cond(contracts.VarBody, contracts.OpContains, "invoices")))},
			{formula(group(cond(contracts.VarSubject, contracts.OpContains, "gmail")))},
			{formula(group(not(cond(contracts.VarBody, contracts.OpContains, "invoices"))))},
		} {
			st.reset()
			page, err := inbox.NewMatcherWith(st, eval, inbox.MatchBounds{}).Match(context.Background(), b, fs, "", 50)
			if err != nil || slices.Contains(ids(page.Messages), conf) || slices.Contains(page.Unknown, conf) || slices.Contains(page.NeedsModel, conf) ||
				slices.Contains(st.entries, conf) || slices.Contains(st.raws, conf) {
				t.Fatalf("%v: %+v %v", fs, page, err)
			}
			if i == 1 && slices.Compare(ids(page.Messages), []string{other}) != 0 {
				t.Fatalf("the other message is not listed: %+v", page)
			}
		}
	}
}

// Q40 (ADR 0027), in inbox: no decision from a cut text. An entry of 16,377
// spaces then "invoice", cut, whose text goes on with "s", and an entry
// ending in "cafe", cut, whose text goes on with a combining acute: every
// body condition on them is unknown, needing FromText, before the raw
// message is read; after it, "invoices" holds and "cafe" does not.
func TestQ40NoDecisionFromACutText(t *testing.T) {
	ctx := context.Background()
	b := qBox(t)
	for _, old := range []bool{false, true} {
		st := newQStore()
		inv := &qMsg{at: qAt(0), from: []string{"a@example.com"}, subject: "x", body: strings.Repeat(" ", 16377) + "invoices", old: old}
		cafe := &qMsg{at: qAt(1), n: 1, from: []string{"a@example.com"}, subject: "x", body: strings.Repeat("y", inbox.SearchTextMax-4) + "café", old: old}
		seedQ(t, st, b, inv)
		seedQ(t, st, b, cafe)
		if e := inv.entry(); len(e.Text) != inbox.SearchTextMax || !strings.HasSuffix(e.Text, "invoice") {
			t.Fatalf("the entry ends %q", e.Text[len(e.Text)-10:])
		}
		if e := cafe.entry(); !strings.HasSuffix(e.Text, "cafe") {
			t.Fatalf("the entry ends %q", e.Text[len(e.Text)-10:])
		}
		item := func(q *qMsg) *store.Message { return &store.Message{MailboxID: b.MailboxID, ID: q.id()} }
		for _, c := range []struct {
			q    *qMsg
			text string
			want query.Value
		}{{inv, "invoices", query.True}, {cafe, "cafe", query.False}} {
			for _, neg := range []bool{false, true} {
				cd := cond(contracts.VarBody, contracts.OpContains, c.text)
				want := c.want
				if neg {
					cd, want = not(cd), query.True-want
				}
				fs := []*query.Canonical{formula(group(cd))}
				meta := c.q.meta()
				m := inbox.QueryMessage(item(c.q), &meta, c.q.entry(), nil)
				if res := standIn(fs, m, nil, query.ForQuery); res.Value != query.Unknown || res.Needs != query.FromText || !m.BodyCut {
					t.Fatalf("old %v, %q not %v, before the raw read: %+v", old, c.text, neg, res)
				}
				st.reset()
				page, err := inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{}).Match(ctx, b, fs, "", 50)
				if err != nil || !slices.Contains(st.raws, c.q.id()) {
					t.Fatalf("raw reads %v %v", st.raws, err)
				}
				if listed := slices.Contains(ids(page.Messages), c.q.id()); listed != (want == query.True) {
					t.Fatalf("old %v, %q not %v: listed %v", old, c.text, neg, listed)
				}
			}
		}
	}
}

// Q41 (ADR 0027): no classification from an entry alone. A message with 101
// senders (Partial) is unknown for "not from alice and the body holds hello"
// although its text holds hello; a confirmation's entry is never read even
// by an evaluation that asks for it first; an entry whose item is not there
// is never read or listed; a meta that does not open is unknown and never
// evaluated.
func TestQ41NoClassificationFromAnEntryAlone(t *testing.T) {
	ctx := context.Background()
	st := newQStore()
	b := qBox(t)
	var senders []string
	for i := range 101 {
		senders = append(senders, fmt.Sprintf("s%d@example.org", i))
	}
	raw := "From: " + strings.Join(senders, ", ") + "\r\nSubject: hi\r\n\r\nhello there\r\n"
	many := inboxtest.FromRaw(ctx, (&qMsg{at: qAt(0)}).id(), qAt(0), 30, []byte(raw))
	if !many.Meta.Partial || len(many.Meta.From) != inbox.MaxAddressesPerField {
		t.Fatalf("parsed %d senders, partial %v", len(many.Meta.From), many.Meta.Partial)
	}
	if err := inboxtest.Seed(ctx, st.Memory, st.Memory, b, many); err != nil {
		t.Fatal(err)
	}
	conf := seedQ(t, st, b, &qMsg{at: qAt(1), n: 1, from: []string{"forwarding-noreply@google.com"}, subject: "x", body: "hello", confirmation: true})
	bad := seedQ(t, st, b, &qMsg{at: qAt(2), n: 2, from: []string{"a@example.com"}, subject: "x", body: "hello", badMeta: true})
	orphan := (&qMsg{at: qAt(3), n: 3}).id()
	st.SetEntry(store.Entry{Partition: inbox.Partition(b.MailboxID), Key: inbox.KeySearch + orphan, Value: []byte("x"), ExpiresAt: qNow.Add(time.Hour)})

	fs := []*query.Canonical{formula(group(not(cond(contracts.VarFromAddress, contracts.OpIs, "alice@example.com"))), group(cond(contracts.VarBody, contracts.OpContains, "hello")))}
	entryFirst := func(fs []*query.Canonical, m *query.Message, a query.Answers, mode query.Mode) query.Result {
		if m.Have&query.FromMeta == 0 {
			t.Errorf("evaluated without its meta")
		}
		if m.Have&query.FromEntry == 0 {
			return query.Result{Value: query.Unknown, Needs: query.FromEntry}
		}
		return standIn(fs, m, a, mode)
	}
	for _, eval := range []inbox.Evaluate{standIn, entryFirst} {
		st.reset()
		page, err := inbox.NewMatcherWith(st, eval, inbox.MatchBounds{}).Match(ctx, b, fs, "", 50)
		if err != nil || len(page.Messages) != 0 || slices.Compare(page.Unknown, []string{many.ID, bad}) != 0 ||
			slices.Contains(st.entries, conf) || slices.Contains(st.entries, orphan) || slices.Contains(st.entries, bad) {
			t.Fatalf("%+v %v; entries read %v", page, err, st.entries)
		}
	}
	if m := inbox.QueryMessage(&store.Message{MailboxID: b.MailboxID, ID: conf}, nil, &inbox.SearchEntry{Text: "hello"}, nil); m.Have&query.FromMeta != 0 || m.Confirmation {
		t.Fatalf("an entry without its meta: %+v", m)
	} else if res := standIn(fs, m, nil, query.ForQuery); res.Value != query.Unknown || res.Needs != query.FromItem|query.FromMeta {
		t.Fatalf("%+v", res)
	}
}

// Q42 (ADR 0027): size from the sealed meta. A 1 KiB message whose item's
// plain size says 1,048,576 is false for size at_least 1048576 and true for
// size below 2048; a meta of format 1 makes both unknown; a meta of format 1
// whose raw message is read for a cut body uses the raw length.
func TestQ42SizeFromTheSealedMeta(t *testing.T) {
	ctx := context.Background()
	b := qBox(t)
	big := formula(group(cond(contracts.VarSize, contracts.OpAtLeast, "1048576")))
	small := formula(group(cond(contracts.VarSize, contracts.OpBelow, "2048")))
	for _, old := range []bool{false, true} {
		st := newQStore()
		q := &qMsg{at: qAt(0), from: []string{"a@example.com"}, subject: "x", body: strings.Repeat("k", 1000), old: old}
		seedQ(t, st, b, q)
		item, _ := st.Item(ctx, b.MailboxID, q.id())
		item.Size = 1048576
		if err := st.PutItem(ctx, item); err != nil {
			t.Fatal(err)
		}
		mt := inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{})
		for _, c := range []struct {
			f    *query.Canonical
			want listing
		}{{big, listedNone}, {small, listedMatch}} {
			if old {
				c.want = listedUnknown
			}
			got, _ := pages(t, mt, b, []*query.Canonical{c.f}, func() int { return 50 }, 5)
			if got[q.id()] != c.want || len(st.raws) != 0 {
				t.Fatalf("old %v: listed %v, raw reads %d", old, got[q.id()], len(st.raws))
			}
		}
	}
	// Format 1, its raw message read for a cut body: the raw length, 20 KiB
	// and some, whatever the item says.
	st := newQStore()
	q := &qMsg{at: qAt(0), from: []string{"a@example.com"}, subject: "x", body: qLong + strings.Repeat("z", 3800), old: true}
	seedQ(t, st, b, q)
	item, _ := st.Item(ctx, b.MailboxID, q.id())
	item.Size = 100 << 10
	_ = st.PutItem(ctx, item)
	withBody := func(c contracts.Condition) []*query.Canonical {
		return []*query.Canonical{formula(group(c), group(cond(contracts.VarBody, contracts.OpContains, "zzz")))}
	}
	mt := inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{})
	for c, want := range map[contracts.Condition]listing{
		cond(contracts.VarSize, contracts.OpAtLeast, "20000"): listedMatch,
		cond(contracts.VarSize, contracts.OpBelow, "21000"):   listedMatch,
		cond(contracts.VarSize, contracts.OpAtLeast, "30000"): listedNone,
	} {
		st.reset()
		if got, _ := pages(t, mt, b, withBody(c), func() int { return 50 }, 5); got[q.id()] != want || len(st.raws) != 1 {
			t.Fatalf("%v: listed %v, raw reads %d", c, got[q.id()], len(st.raws))
		}
	}
}

// Q44 (ADR 0027), in inbox: raw reads are given back one by one. Six
// messages of 25 MiB with cut texts on one page, under grants of at most
// 32 MiB, are all read, never with two grants held, and the page answers; a
// read budget that is not a ReadGrants is refused with ErrTooManyRequests
// before the first raw read.
func TestQ44RawReadsReleasedOneByOne(t *testing.T) {
	ctx := context.Background()
	st := newQStore()
	b := qBox(t)
	body := strings.Repeat("0123456789abcdef", (25<<20)/16-64) + " invoices"
	var want []string
	for i := range 6 {
		q := &qMsg{at: qAt(i), n: i, from: []string{"a@example.com"}, subject: "x", body: body}
		want = append(want, seedQ(t, st, b, q))
		if n := len(q.raw()); n > inbox.MaxMessageBytes || n < inbox.MaxMessageBytes-(4<<10) {
			t.Fatalf("a message of %d bytes", n)
		}
	}
	fs := []*query.Canonical{formula(group(cond(contracts.VarBody, contracts.OpContains, "0123456789abcdef0123")))}
	g := &qGrants{free: inbox.RawReadMax}
	st.onRaw = func(string) error {
		if g.heldNow() != 1 {
			return fmt.Errorf("%d grants held", g.heldNow())
		}
		return nil
	}
	page, err := inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{}).Match(inbox.WithReadBudget(ctx, g), b, fs, "", 50)
	if err != nil || slices.Compare(ids(page.Messages), want) != 0 || g.granted != 6 || g.maxHeld != 1 || g.heldNow() != 0 || g.free != inbox.RawReadMax {
		t.Fatalf("%v; granted %d, max held %d, free %d", err, g.granted, g.maxHeld, g.free)
	}
	st.reset()
	st.onRaw = nil
	if _, err := inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{}).Match(inbox.WithReadBudget(ctx, takeOnly{}), b, fs, "", 50); !errors.Is(err, contracts.ErrTooManyRequests) || len(st.raws) != 0 {
		t.Fatalf("a budget that is not a ReadGrants: %v, %d raw reads", err, len(st.raws))
	}
}

// What a page lists, and where it stops: needing a model, unknown,
// matches, its limits, and the bounds of reads that leave a message whole
// for the next page.
func TestMatchListsAndStops(t *testing.T) {
	ctx := context.Background()
	st := newQStore()
	b := qBox(t)
	var all []string
	for i := range 10 {
		all = append(all, seedQ(t, st, b, &qMsg{at: qAt(i), n: i, from: []string{"a@example.com"}, subject: "x", body: qLong + "end"}))
	}
	mt := inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{})
	about := []*query.Canonical{formula(group(cond(contracts.VarMessage, contracts.OpAbout, "bills")))}
	page, err := mt.Match(ctx, b, about, "", 50)
	if err != nil || slices.Compare(page.NeedsModel, all) != 0 || len(page.Unknown)+len(page.Messages) != 0 || page.Next != "" || !page.SearchedTo.Equal(qAt(9)) {
		t.Fatalf("needs a model: %+v %v", page, err)
	}
	page, err = inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{Undecided: 3}).Match(ctx, b, about, "", 50)
	if err != nil || slices.Compare(page.NeedsModel, all[:3]) != 0 || page.Next != all[2] {
		t.Fatalf("at most undecided: %+v %v", page, err)
	}
	// Full at the end of a batch: next is the batch's last item while more
	// may be left, and nothing when the scan said there is none.
	page, err = inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{Read: 6}).Match(ctx, b, []*query.Canonical{formula()}, "", 5)
	if err != nil || slices.Compare(ids(page.Messages), all[:5]) != 0 || page.Next != all[4] {
		t.Fatalf("full at a batch's end: %+v %v", page, err)
	}
	page, err = mt.Match(ctx, b, []*query.Canonical{formula()}, all[4], 5)
	if err != nil || slices.Compare(ids(page.Messages), all[5:]) != 0 || page.Next != "" {
		t.Fatalf("full at the end: %+v %v", page, err)
	}
	// Full inside a batch whose next message is below since: the end.
	since := []*query.Canonical{formula(group(cond(contracts.VarReceived, contracts.OpSince, rfc(qAt(4)))))}
	page, err = mt.Match(ctx, b, since, "", 5)
	if err != nil || slices.Compare(ids(page.Messages), all[:5]) != 0 || page.Next != "" {
		t.Fatalf("full where since ends: %+v %v", page, err)
	}
	// A body condition over cut texts: the entries' reads and the raw reads
	// end pages before a message, which the next page starts with.
	body := []*query.Canonical{formula(group(cond(contracts.VarBody, contracts.OpContains, "end")))}
	page, err = inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{TextReads: 3}).Match(ctx, b, body, "", 50)
	if err != nil || slices.Compare(ids(page.Messages), all[:3]) != 0 || page.Next != all[2] {
		t.Fatalf("three raw reads: %+v %v", page, err)
	}
	page, err = inbox.NewMatcherWith(st, standIn, inbox.MatchBounds{Read: 8}).Match(ctx, b, body, "", 50)
	if err != nil || slices.Compare(ids(page.Messages), all[:1]) != 0 || page.Next != all[0] {
		t.Fatalf("eight items read: %+v %v", page, err)
	}
	// Limits and cursors the adapter checks are refused here too; a formula
	// that is never true reads nothing.
	for _, limit := range []int{0, 101} {
		if _, err := mt.Match(ctx, b, nil, "", limit); !errors.Is(err, contracts.ErrInvalidLimit) {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
	if _, err := mt.Match(ctx, b, nil, "zz", 10); !errors.Is(err, contracts.ErrInvalidCursor) {
		t.Fatalf("cursor: %v", err)
	}
	st.reset()
	page, err = mt.Match(ctx, b, []*query.Canonical{formula(), {Never: true}}, "", 10)
	if err != nil || len(page.Messages) != 0 || page.Next != "" || len(st.scans) != 0 {
		t.Fatalf("never: %+v %v", page, err)
	}
	// A nil formula among them is no bound.
	if got, _ := pages(t, mt, b, []*query.Canonical{nil, formula()}, func() int { return 100 }, 3); len(got) != 10 {
		t.Fatalf("with a nil formula: %d", len(got))
	}
}

// What cannot be read of a message makes it unknown and the page goes on;
// what fails in the store fails the page.
func TestMatchWhatCannotBeRead(t *testing.T) {
	ctx := context.Background()
	st := newQStore()
	b := qBox(t)
	tooLarge := seedQ(t, st, b, &qMsg{at: qAt(0), from: []string{"a@example.com"}, subject: "x", body: qLong + "end", rawTooLarge: true})
	noEntry := seedQ(t, st, b, &qMsg{at: qAt(1), n: 1, from: []string{"a@example.com"}, subject: "x", body: "end", noEntry: true})
	badEntry := seedQ(t, st, b, &qMsg{at: qAt(2), n: 2, from: []string{"a@example.com"}, subject: "x", body: "end", badEntry: true})
	unparsed := seedQ(t, st, b, &qMsg{at: qAt(3), n: 3, from: []string{"a@example.com"}, subject: "x", body: "end", unparsed: true})
	missing := seedQ(t, st, b, &qMsg{at: qAt(4), n: 4, from: []string{"a@example.com"}, subject: "x", body: qLong + "end"})
	// A raw object that does not open under the box's key.
	sealedElsewhere := seedQ(t, st, b, &qMsg{at: qAt(5), n: 5, from: []string{"a@example.com"}, subject: "x", body: qLong + "end"})
	item, _ := st.Item(ctx, b.MailboxID, sealedElsewhere)
	if err := st.DeleteMessage(ctx, b.MailboxID, sealedElsewhere); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRaw(ctx, item, []byte("not sealed under the box's key")); err != nil {
		t.Fatal(err)
	}
	if err := st.PutItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	body := []*query.Canonical{formula(group(cond(contracts.VarBody, contracts.OpContains, "end")))}
	st.onRaw = func(id string) error {
		if id == missing {
			return store.ErrNotFound
		}
		return nil
	}
	asksEverything := func(fs []*query.Canonical, m *query.Message, a query.Answers, mode query.Mode) query.Result {
		if m.Unparsed {
			return query.Result{Value: query.Unknown, Needs: query.FromEntry | query.FromText}
		}
		return standIn(fs, m, a, mode)
	}
	mt := inbox.NewMatcherWith(st, asksEverything, inbox.MatchBounds{})
	page, err := mt.Match(ctx, b, body, "", 50)
	if err != nil || slices.Compare(ids(page.Messages), []string{noEntry, badEntry}) != 0 || slices.Compare(page.Unknown, []string{tooLarge, unparsed, missing, sealedElsewhere}) != 0 ||
		slices.Contains(st.entries, unparsed) || slices.Contains(st.raws, unparsed) || !slices.Contains(st.raws, noEntry) || !slices.Contains(st.raws, badEntry) {
		t.Fatalf("%+v %v; entries %v raws %v", page, err, st.entries, st.raws)
	}
	for name, spoil := range map[string]func(){
		"scan":  func() { st.onScan = func() error { return errQ } },
		"entry": func() { st.onEntry = func(string) error { return errQ } },
		"raw":   func() { st.onRaw = func(string) error { return errQ } },
	} {
		st.onScan, st.onEntry, st.onRaw = nil, nil, nil
		spoil()
		if _, err := mt.Match(ctx, b, body, "", 50); !errors.Is(err, errQ) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// The matcher on DynamoDB Local and S3Mock, as CI runs them: entries read by
// key and raw messages read bounded through the AWS store.
func TestMatchOnTheEmulators(t *testing.T) {
	w := emulatedWorld(t)
	ctx := context.Background()
	var want []string
	for i, body := range []string{"short end", qLong + "end", "nothing", qLong + "nothing"} {
		q := &qMsg{at: qAt(i), n: i, from: []string{"a@example.com"}, subject: "x", body: body}
		m := inboxtest.Message{ID: q.id(), ReceivedAt: qNow, RetentionDays: 30, Raw: q.raw(), Meta: q.meta(), Search: q.entry()}
		if err := inboxtest.Seed(ctx, w.st, w.st, w.b, m); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(body, "end") {
			want = append(want, q.id())
		}
	}
	g := &qGrants{}
	page, err := inbox.NewMatcher(w.st, standIn).Match(inbox.WithReadBudget(ctx, g), w.b,
		[]*query.Canonical{formula(group(cond(contracts.VarBody, contracts.OpContains, "end")))}, "", 50)
	if err != nil || slices.Compare(ids(page.Messages), want) != 0 || g.granted != 2 || g.heldNow() != 0 {
		t.Fatalf("%+v %v, granted %d", page, err, g.granted)
	}
}
