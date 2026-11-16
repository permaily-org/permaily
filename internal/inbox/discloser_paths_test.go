package inbox_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/inboxtest"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/store"
)

// The discloser's paths the numbered tests do not take: its refusals, the
// bounds and budgets of a page, the store failing, and the text the Asker
// is given.

// travel is a formula a model decides, and its atom.
var (
	travel     = formula(group(contracts.Condition{Var: contracts.VarMessage, Op: contracts.OpAbout, Value: "travel"}))
	travelAtom = query.Atom{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: "travel"}
)

// noRules is a disclosure of fs with no rule.
func noRules(fs ...*query.Canonical) inbox.Disclosure {
	d := disclosure(allParts, fs...)
	d.Rules = nil
	return d
}

func TestADisclosurePageRefusesWhatIsNotOne(t *testing.T) {
	w := newDWorld(t)
	w.seed(qNow.Add(-time.Hour), 1, "Lunch", "See you")
	ctx := on(w.askA)
	for _, limit := range []int{0, inbox.DiscloseMaxLimit + 1} {
		if _, err := w.discA.Page(ctx, w.b, disclosure(allParts), "", limit); !errors.Is(err, contracts.ErrInvalidLimit) {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
	if _, err := w.discA.Page(ctx, w.b, disclosure(allParts), "not a cursor", 10); !errors.Is(err, contracts.ErrInvalidCursor) {
		t.Fatal(err)
	}
	never := &query.Canonical{Never: true}
	if p := w.page(ctx, w.discA, disclosure(allParts, never), "", 10); len(p.Items) != 0 || p.Next != "" {
		t.Fatalf("%+v", p)
	}
	if _, err := w.discA.Message(ctx, w.b, disclosure(allParts, never), (&qMsg{at: qNow.Add(-time.Hour), n: 1}).id()); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := w.discA.Page(cancelled, w.b, disclosure(allParts), "", 10); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := w.discA.Message(cancelled, w.b, disclosure(allParts), (&qMsg{at: qNow.Add(-time.Hour), n: 1}).id()); err == nil {
		t.Fatal("a cancelled read")
	}
}

func TestADisclosurePageFailsAsTheStoreFails(t *testing.T) {
	long := strings.Repeat("a long text ", 2000)
	for _, where := range []string{"scan", "entry", "raw", "item"} {
		t.Run(where, func(t *testing.T) {
			w := newDWorld(t)
			id := w.seed(qNow.Add(-time.Hour), 1, "Lunch", long)
			switch where {
			case "scan":
				w.st.onScan = func() error { return errQ }
			case "entry":
				w.st.onEntry = func(string) error { return errQ }
			case "raw":
				w.st.onRaw = func(string) error { return errQ }
			}
			if where != "item" {
				if _, err := w.discA.Page(on(w.askA), w.b, disclosure(allParts), "", 10); !errors.Is(err, errQ) {
					t.Fatalf("page: %v", err)
				}
			}
			if where == "scan" {
				return
			}
			st := &failingItem{qStore: w.st, fail: where == "item"}
			disc := inbox.NewDiscloserWith(st, query.Eval, w.decA, inbox.MatchBounds{Now: w.clock.now}, 0)
			if _, err := disc.Message(on(w.askA), w.b, disclosure(allParts), id); !errors.Is(err, errQ) {
				t.Fatalf("read: %v", err)
			}
		})
	}
}

// failingItem fails reading an item.
type failingItem struct {
	*qStore
	fail bool
}

func (f *failingItem) Item(ctx context.Context, mailboxID, id string) (*store.Message, error) {
	if f.fail {
		return nil, errQ
	}
	return f.qStore.Item(ctx, mailboxID, id)
}

// The scan's bounds by received conditions: nothing below since is
// evaluated, a cursor below it is the end, and before starts the scan.
func TestADisclosurePageKeepsToItsReceivedBounds(t *testing.T) {
	w := newDWorld(t)
	old := seedQ(t, w.st, w.b, &qMsg{at: time.Date(2019, 6, 1, 0, 0, 0, 0, time.UTC), n: 1, from: []string{"a@example.com"}, subject: "Old", body: "x"})
	recent := w.seed(qNow.Add(-time.Hour), 1, "Lunch", "See you")
	older := w.seed(qNow.Add(-48*time.Hour), 1, "Coffee", "See you")
	p := w.page(on(w.askA), w.discA, disclosure(allParts), "", 10)
	if left, _ := sortOut(t, p.Items); !slices.Equal(left, []string{recent, older}) || p.Next != "" {
		t.Fatalf("%v next %q", left, p.Next)
	}
	if slices.Contains(w.askA.atomsFor(old), codeAtom) {
		t.Fatal("a message below since was evaluated")
	}
	if p := w.page(on(w.askA), w.discA, disclosure(allParts), old, 10); len(p.Items) != 0 || p.Next != "" {
		t.Fatalf("below since: %+v", p)
	}
	before := formula(group(cond(contracts.VarReceived, contracts.OpBefore, qNow.Add(-24*time.Hour).Format(time.RFC3339))))
	p = w.page(on(w.askA), w.discA, disclosure(allParts, every, before), "", 10)
	if left, _ := sortOut(t, p.Items); !slices.Equal(left, []string{older}) {
		t.Fatalf("before: %v", left)
	}
}

func (a *evAsker) atomsFor(id string) []query.Atom {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.atoms[id]
}

// A page ends at its limit inside a batch or at its end, at its read
// bound, its soft deadline, and before the message past its asks; it skips
// nothing.
func TestADisclosurePageEndsAtItsBounds(t *testing.T) {
	w := newDWorld(t)
	var all []string
	for i := range 6 {
		all = append(all, w.seed(qNow.Add(-time.Duration(i+1)*time.Hour), i, "Lunch", "See you"))
	}
	walk := func(disc inbox.Discloser, limit int) (pages int, got []string) {
		before := ""
		for ; pages < 20; pages++ {
			p := w.page(on(w.askA), disc, disclosure(allParts), before, limit)
			left, _ := sortOut(t, p.Items)
			got = append(got, left...)
			if p.Next == "" {
				return pages + 1, got
			}
			before = p.Next
		}
		t.Fatal("too many pages")
		return 0, nil
	}
	for _, c := range []struct {
		name  string
		disc  inbox.Discloser
		limit int
	}{
		{"its limit", w.discA, 4},
		{"its limit at a batch's end", w.discA, 6},
		{"its reads", inbox.NewDiscloserWith(w.st, query.Eval, w.decA, inbox.MatchBounds{Read: 5, Now: w.clock.now}, 0), 10},
		{"its asks", inbox.NewDiscloserWith(w.st, query.Eval, w.decA, inbox.MatchBounds{Now: w.clock.now}, 2), 10},
	} {
		pages, got := walk(c.disc, c.limit)
		if !slices.Equal(got, all) || pages < 2 && c.limit != 6 && c.limit != 10 {
			t.Fatalf("%s: %d pages gave %v", c.name, pages, got)
		}
	}
	// The soft deadline: the clock moves a minute each time it is read.
	tick := &dClock{t: qNow}
	moving := func() time.Time { tick.add(time.Minute); return tick.now() }
	disc := inbox.NewDiscloserWith(w.st, query.Eval, w.decA, inbox.MatchBounds{Soft: 90 * time.Second, Now: moving}, 0)
	if pages, got := walk(disc, 10); !slices.Equal(got, all) || pages < 2 {
		t.Fatalf("the soft deadline: %d pages gave %v", pages, got)
	}
}

// What the Asker is given of the text: the entry's when it was not cut,
// the raw message's when it was, nothing it cannot afford (the page ends
// before the message, or, for the first, holds it undecided), unavailable
// when the raw message cannot be read; a read that fails fails the page.
func TestTheTextTheAskerIsGiven(t *testing.T) {
	long := strings.Repeat("travel plans ", 2000)
	t.Run("entry, raw, unavailable", func(t *testing.T) {
		w := newDWorld(t)
		short := w.seed(qNow.Add(-time.Hour), 1, "Trip", "Flights to Lisbon")
		cut := w.seed(qNow.Add(-2*time.Hour), 1, "Trip", long)
		gone := seedQ(t, w.st, w.b, &qMsg{at: qNow.Add(-3 * time.Hour), n: 1, from: []string{"a@example.com"}, subject: "Trip", body: long, rawTooLarge: true})
		noEntry := seedQ(t, w.st, w.b, &qMsg{at: qNow.Add(-4 * time.Hour), n: 1, from: []string{"a@example.com"}, subject: "Trip", body: "Trains", noEntry: true})
		w.askA.p = func(_ string, a query.Atom) (float64, bool) { return 0.95, a == travelAtom }
		p := w.page(on(w.askA), w.discA, noRules(travel), "", 10)
		left, _ := sortOut(t, p.Items)
		if !slices.Equal(left, []string{short, cut, noEntry}) || slices.Contains(left, gone) {
			t.Fatalf("left %v", left)
		}
		o, err := w.discA.Message(on(w.askA), w.b, noRules(travel), cut)
		if err != nil || o.Disclosed == nil || !strings.HasSuffix(*o.Disclosed.Message.Text, "travel plans ") {
			t.Fatalf("%+v %v", o, err)
		}
	})
	t.Run("unaffordable", func(t *testing.T) {
		w := newDWorld(t)
		first := w.seed(qNow.Add(-time.Hour), 1, "Trip", long)
		second := w.seed(qNow.Add(-2*time.Hour), 1, "Trip", long)
		w.askA.p = func(_ string, a query.Atom) (float64, bool) { return 0.95, a == travelAtom }
		disc := inbox.NewDiscloserWith(w.st, query.Eval, w.decA, inbox.MatchBounds{TextReads: 1, Now: w.clock.now}, 0)
		p := w.page(on(w.askA), disc, noRules(travel), "", 10)
		if left, _ := sortOut(t, p.Items); !slices.Equal(left, []string{first}) || p.Next != first {
			t.Fatalf("left %v next %q", left, p.Next)
		}
		// The first message of a page, unaffordable: held, undecided, with
		// the rule's decision abandoned.
		disc = inbox.NewDiscloserWith(w.st, query.Eval, w.decA, inbox.MatchBounds{TextReads: 1, Read: 3, Now: w.clock.now}, 0)
		d := disclosure(allParts, travel)
		p = w.page(on(w.askA), disc, d, first, 10)
		if heldBy(t, p, second) != inbox.HeldUndecided {
			t.Fatalf("%+v", p.Items)
		}
	})
	t.Run("a read that fails", func(t *testing.T) {
		w := newDWorld(t)
		w.seed(qNow.Add(-time.Hour), 1, "Trip", long)
		w.st.onRaw = func(string) error { return errQ }
		if _, err := w.discA.Page(on(w.askA), w.b, noRules(travel), "", 10); !errors.Is(err, errQ) {
			t.Fatal(err)
		}
	})
}

// The Asker: its own deadline running out is a message asked of which
// nothing came; the request's ending fails it; one that answers nothing is
// one that answered nothing.
func TestTheDisclosersAsk(t *testing.T) {
	w := newDWorld(t)
	id := w.seed(qNow.Add(-time.Hour), 1, "Trip", "Flights to Lisbon")
	slow := askerFunc(func(ctx context.Context, q *inbox.Question) (*inbox.Asked, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	disc := inbox.NewDiscloserWith(w.st, query.Eval, w.decA, inbox.MatchBounds{Now: w.clock.now}, 0)
	inbox.AskWithin(disc, 10*time.Millisecond)
	p := w.page(on(slow), disc, disclosure(allParts, travel), "", 10)
	if heldBy(t, p, id) != inbox.HeldUndecided {
		t.Fatalf("%+v", p.Items)
	}
	ctx, cancel := context.WithTimeout(on(slow), 20*time.Millisecond)
	defer cancel()
	inbox.AskWithin(disc, time.Minute)
	if _, err := disc.Page(ctx, w.b, disclosure(allParts, travel), "", 10); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	nothing := askerFunc(func(context.Context, *inbox.Question) (*inbox.Asked, error) { return nil, nil })
	if p := w.page(on(nothing), disc, disclosure(allParts, travel), "", 10); heldBy(t, p, id) != inbox.HeldUndecided {
		t.Fatalf("%+v", p.Items)
	}
}

// A read's text and attachments: the entry's text when the formulas and
// rules read none, the raw message's for a cut one; an attachment given
// with its bytes, one that is not there, one past the bound, and none of an
// unparsed message.
func TestAReadsTextAndAttachments(t *testing.T) {
	w := newDWorld(t)
	short := w.seed(qNow.Add(-time.Hour), 1, "Lunch", "See you")
	long := w.seed(qNow.Add(-2*time.Hour), 1, "Minutes", strings.Repeat("minutes ", 3000)+"END")
	gone := seedQ(t, w.st, w.b, &qMsg{at: qNow.Add(-3 * time.Hour), n: 1, from: []string{"a@example.com"}, subject: "Big", body: strings.Repeat("x", 20000), rawTooLarge: true})
	raw := []byte("From: a@example.com\r\nSubject: Report\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nSee attached\r\n--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=r.pdf\r\n\r\n%PDF\r\n--b--\r\n")
	file := inbox.NewMessageID(qNow.Add(-4 * time.Hour))
	if err := inboxtest.Seed(context.Background(), w.st.Memory, w.st.Memory, w.b, inboxtest.FromRaw(context.Background(), file, qNow.Add(-4*time.Hour), 30, raw)); err != nil {
		t.Fatal(err)
	}
	huge := seedQ(t, w.st, w.b, &qMsg{at: qNow.Add(-5 * time.Hour), n: 1, from: []string{"a@example.com"}, subject: "Huge", body: "x",
		attach: []contracts.MessageAttachment{{Index: 0, Name: "big.bin", Type: "application/octet-stream", Size: contracts.VendorAttachmentMax + 1}}})
	unparsed := seedQ(t, w.st, w.b, &qMsg{at: qNow.Add(-6 * time.Hour), n: 1, from: []string{"a@example.com"}, subject: "x", body: "x", unparsed: true,
		attach: []contracts.MessageAttachment{{Index: 0, Name: "a", Type: "a/b", Size: 1}}})
	ctx := on(w.askA)
	d := noRules(every)
	for id, want := range map[string]string{short: "See you", long: "END"} {
		o, err := w.discA.Message(ctx, w.b, d, id)
		if err != nil || o.Disclosed == nil || !strings.HasSuffix(*o.Disclosed.Message.Text, want) {
			t.Fatalf("%+v %v", o, err)
		}
	}
	if _, err := w.discA.Message(ctx, w.b, d, gone); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("a text that cannot be read: %v", err)
	}
	o, a, err := w.discA.Attachment(ctx, w.b, d, file, 0)
	if err != nil || o.Disclosed == nil || a == nil || a.Name != "r.pdf" || a.Type != "application/pdf" || string(a.Data) != "%PDF" {
		t.Fatalf("%+v %+v %v", o, a, err)
	}
	for _, c := range []struct {
		id    string
		index int
		want  error
	}{
		{file, 7, contracts.ErrNotFound},
		{huge, 0, contracts.ErrAttachmentTooLarge},
		{unparsed, 0, contracts.ErrNotFound},
		{huge, 1, contracts.ErrNotFound},
		{strings.Repeat("f", 32), 0, contracts.ErrNotFound},
	} {
		if _, _, err := w.discA.Attachment(ctx, w.b, d, c.id, c.index); !errors.Is(err, c.want) {
			t.Fatalf("%s %d: %v", c.id[:8], c.index, err)
		}
	}
	// An attachment whose raw message is gone.
	w.st.onRaw = func(id string) error {
		if id == file {
			return store.ErrNotFound
		}
		return nil
	}
	if _, _, err := w.discA.Attachment(ctx, w.b, d, file, 0); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatal(err)
	}
	// Of an unparsed message, no text.
	if o, err := w.discA.Message(ctx, w.b, d, unparsed); err != nil || o.Disclosed == nil || *o.Disclosed.Message.Text != "" {
		t.Fatalf("%+v %v", o, err)
	}
}

// Two rules: a message the first already holds (final at Begin) is not
// begun for the second; one decided by neither is begun for both and asked
// once, and leaves on both decisions.
func TestTwoRulesOneAsk(t *testing.T) {
	w := newDWorld(t)
	m := w.seed(qNow.Add(-time.Hour), 1, "Hello", "Here is your number: 482913.")
	w.askA.p = byMessage(map[string]float64{m: 0.9})
	if heldBy(t, w.page(on(w.askA), w.discA, disclosure(allParts), "", 10), m) != inbox.HeldDecided {
		t.Fatal("the first rule did not hold M")
	}
	n := w.seed(qNow.Add(-2*time.Hour), 1, "Lunch", "See you")
	second := rules.Default()
	second.ID = "second-rule"
	d := disclosure(allParts)
	d.Rules = append(d.Rules, second)
	p := w.page(on(w.askA), w.discA, d, "", 10)
	if heldBy(t, p, m) != inbox.HeldDecided || !leftIn(t, p, n) || w.askA.asked(n) != 1 {
		t.Fatalf("%+v", p.Items)
	}
	o := p.Items[slices.IndexFunc(p.Items, func(o inbox.Outcome) bool { return o.ID() == n })]
	if len(o.Disclosed.Rules) != 2 {
		t.Fatalf("rules %+v", o.Disclosed.Rules)
	}
	got, err := w.decA.Begin(context.Background(), w.b, second.Ref(), []inbox.DecisionKey{{ID: m, ExpiresAt: qNow.Add(-time.Hour)}})
	if err != nil || got[m].State != inbox.DecisionExpired {
		t.Fatalf("the second rule began M: %+v %v", got, err)
	}
	// An exact hold of the second rule.
	exact := w.seed(qNow.Add(-3*time.Hour), 1, "Your passcode", "")
	d.Rules = []rules.Rule{{ID: "none", Version: 1, Effect: rules.Hold, Formulas: []*query.Canonical{formula(group(cond(contracts.VarSubject, contracts.OpContains, "nothing at all")))}}, second}
	if heldBy(t, w.page(on(w.askA), w.discA, d, "", 10), exact) != inbox.HeldExact {
		t.Fatal("not held exactly by the second rule")
	}
}

// What the formulas or a rule decide on the first pass alone: a body that
// cannot be read is no match; a formula a model decides beside a rule's
// exact hold is held exactly; formulas no answer can make true are not
// asked; more atoms than are tried at once (two formulas of eight at most
// each) are asked.
func TestWhatTheFirstPassDecides(t *testing.T) {
	w := newDWorld(t)
	gone := seedQ(t, w.st, w.b, &qMsg{at: qNow.Add(-time.Hour), n: 1, from: []string{"a@example.com"}, subject: "Big", body: strings.Repeat("x", 20000), rawTooLarge: true})
	code := w.seed(qNow.Add(-2*time.Hour), 1, "Your passcode", "")
	plain := w.seed(qNow.Add(-3*time.Hour), 1, "Trip", "Flights")
	ctx := on(w.askA)
	body := formula(group(cond(contracts.VarBody, contracts.OpContains, "zzz")))
	if p := w.page(ctx, w.discA, noRules(body), "", 10); slices.ContainsFunc(p.Items, func(o inbox.Outcome) bool { return o.ID() == gone }) {
		t.Fatal("a body that cannot be read matched")
	}
	if heldBy(t, w.page(ctx, w.discA, disclosure(allParts, travel), "", 10), code) != inbox.HeldExact {
		t.Fatal("not held exactly")
	}
	about := contracts.Condition{Var: contracts.VarSubject, Op: contracts.OpAbout, Value: "trains"}
	no := about
	no.Not = true
	impossible := noRules(formula(group(about)), formula(group(no)))
	before := w.askA.asked(plain)
	if p := w.page(ctx, w.discA, impossible, "", 10); len(p.Items) != 0 || w.askA.asked(plain) != before {
		t.Fatalf("asked what no answer makes true: %+v", p.Items)
	}
	var many []contracts.Condition
	for _, topic := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
		many = append(many, contracts.Condition{Var: contracts.VarSubject, Op: contracts.OpAbout, Value: "topic " + topic})
	}
	w.askA.p = func(string, query.Atom) (float64, bool) { return 0.95, true }
	if p := w.page(ctx, w.discA, noRules(formula(many[:5]), formula(many[5:])), "", 10); !leftIn(t, p, plain) {
		t.Fatalf("nine atoms: %+v", p.Items)
	}
}

// The page's own ends: its text reads spent on the first pass, the read
// bound and the text reads spent before the first message is asked (held
// for this request, or none), a full batch with more behind it, the soft
// deadline between batches and before an ask, a read whose formulas the
// model answers no, and a request that ends while it is made.
func TestADisclosurePageEndsWhereItMust(t *testing.T) {
	long := strings.Repeat("a long text ", 2000)
	t.Run("text reads on the first pass", func(t *testing.T) {
		w := newDWorld(t)
		first := w.seed(qNow.Add(-time.Hour), 1, "Lunch", long)
		w.seed(qNow.Add(-2*time.Hour), 1, "Dinner", long)
		disc := inbox.NewDiscloserWith(w.st, query.Eval, w.decA, inbox.MatchBounds{TextReads: 1, Now: w.clock.now}, 0)
		if p := w.page(on(w.askA), disc, disclosure(allParts), "", 10); !leftIn(t, p, first) || p.Next != first {
			t.Fatalf("%+v next %q", p.Items, p.Next)
		}
	})
	t.Run("the first message unaffordable", func(t *testing.T) {
		w := newDWorld(t)
		trip := w.seed(qNow.Add(-time.Hour), 1, "Trip", long)
		w.seed(qNow.Add(-2*time.Hour), 1, "Other", "nothing")
		trainsOrTrip := formula(group(cond(contracts.VarSubject, contracts.OpContains, "trip"), cond(contracts.VarBody, contracts.OpContains, "zzz")))
		disc := inbox.NewDiscloserWith(w.st, query.Eval, w.decA, inbox.MatchBounds{Read: 3, Now: w.clock.now}, 0)
		p := w.page(on(w.askA), disc, noRules(travel, trainsOrTrip), "", 10)
		if len(p.Items) != 0 || w.askA.asked(trip) != 1 {
			t.Fatalf("%+v", p.Items)
		}
		w.st.onEntry = func(key string) error {
			if strings.HasSuffix(key, trip) {
				return errQ
			}
			return nil
		}
		disc = inbox.NewDiscloserWith(w.st, query.Eval, w.decA, inbox.MatchBounds{Now: w.clock.now}, 0)
		if _, err := disc.Page(on(w.askA), w.b, noRules(travel, trainsOrTrip), "", 10); !errors.Is(err, errQ) {
			t.Fatalf("an entry that fails: %v", err)
		}
	})
	t.Run("a full batch with more behind it", func(t *testing.T) {
		w := newDWorld(t)
		var all []string
		for i := range 6 {
			all = append(all, w.seed(qNow.Add(-time.Duration(i+1)*time.Hour), i, "Lunch", "See you"))
		}
		disc := inbox.NewDiscloserWith(w.st, query.Eval, w.decA, inbox.MatchBounds{Read: 5, Now: w.clock.now}, 0)
		p := w.page(on(w.askA), disc, noRules(every), "", 4)
		if left, _ := sortOut(t, p.Items); !slices.Equal(left, all[:4]) || p.Next != all[3] {
			t.Fatalf("%v next %q", left, p.Next)
		}
	})
	t.Run("the soft deadline between batches", func(t *testing.T) {
		w := newDWorld(t)
		first := w.seed(qNow.Add(-time.Hour), 1, "Lunch", "See you")
		w.seed(qNow.Add(-2*time.Hour), 1, "Dinner", "See you")
		w.st.onScan = func() error { w.clock.add(time.Hour); return nil }
		disc := inbox.NewDiscloserWith(w.st, query.Eval, w.decA, inbox.MatchBounds{Read: 2, Now: w.clock.now}, 0)
		if p := w.page(on(w.askA), disc, noRules(every), "", 10); !leftIn(t, p, first) || p.Next != first {
			t.Fatalf("%+v next %q", p.Items, p.Next)
		}
	})
	t.Run("the soft deadline before an ask", func(t *testing.T) {
		w := newDWorld(t)
		first := w.seed(qNow.Add(-time.Hour), 1, "Lunch", "See you")
		second := w.seed(qNow.Add(-2*time.Hour), 1, "Dinner", "See you")
		w.askA.before = func(*inbox.Question) error { w.clock.add(time.Hour); return nil }
		p := w.page(on(w.askA), w.discA, disclosure(allParts), "", 10)
		if !leftIn(t, p, first) || p.Next != first || w.askA.asked(second) != 0 {
			t.Fatalf("%+v next %q", p.Items, p.Next)
		}
		if got := decisionOf(t, w.decA, w.b, second, w.clock.now()); got.State != inbox.DecisionExpired {
			t.Fatalf("what was not asked was not abandoned: %+v", got)
		}
	})
	t.Run("a read the model answers no", func(t *testing.T) {
		w := newDWorld(t)
		id := w.seed(qNow.Add(-time.Hour), 1, "Trip", "Flights")
		d := disclosure(allParts, travel)
		d.Account = "" // the Asking's account
		if _, err := w.discA.Message(on(w.askA), w.b, d, id); !errors.Is(err, contracts.ErrNotFound) {
			t.Fatal(err)
		}
		if _, _, err := w.discA.Attachment(on(w.askA), w.b, d, id, 0); !errors.Is(err, contracts.ErrNotFound) {
			t.Fatal(err)
		}
		w.askA.before = func(*inbox.Question) error { return errQ }
		if _, err := w.discA.Message(on(w.askA), w.b, d, id); !errors.Is(err, errQ) {
			t.Fatal(err)
		}
	})
	t.Run("a request that ends while it is made", func(t *testing.T) {
		w := newDWorld(t)
		id := w.seed(qNow.Add(-time.Hour), 1, "Lunch", "See you")
		ctx, cancel := context.WithCancel(on(w.askA))
		defer cancel()
		w.askA.before = func(*inbox.Question) error { cancel(); return nil }
		if _, err := w.discA.Page(ctx, w.b, disclosure(allParts), "", 10); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if got := decisionOf(t, w.decA, w.b, id, w.clock.now()); got.State != inbox.DecisionCleared {
			t.Fatalf("not settled once the request ended: %+v", got)
		}
	})
}

// A read's text when nothing read it before: the entry failing, and the
// raw message failing.
func TestAReadsTextFailing(t *testing.T) {
	w := newDWorld(t)
	short := w.seed(qNow.Add(-time.Hour), 1, "Lunch", "See you")
	long := w.seed(qNow.Add(-2*time.Hour), 1, "Minutes", strings.Repeat("minutes ", 3000))
	w.st.onEntry = func(string) error { return errQ }
	if _, err := w.discA.Message(on(w.askA), w.b, noRules(every), short); !errors.Is(err, errQ) {
		t.Fatal(err)
	}
	w.st.onEntry, w.st.onRaw = nil, func(string) error { return errQ }
	if _, err := w.discA.Message(on(w.askA), w.b, noRules(every), long); !errors.Is(err, errQ) {
		t.Fatal(err)
	}
}

// An Asker that reads the text after it answered gets nothing.
func TestTheTextIsNotReadAfterTheAsk(t *testing.T) {
	w := newDWorld(t)
	w.seed(qNow.Add(-time.Hour), 1, "Trip", "Flights")
	var late func(context.Context) (string, inbox.TextState, error)
	asker := askerFunc(func(_ context.Context, q *inbox.Question) (*inbox.Asked, error) {
		late = q.Text
		return &inbox.Asked{Answers: query.Answers{}, Outcome: inbox.AskAnswered}, nil
	})
	w.page(on(asker), w.discA, noRules(travel), "", 10)
	if _, st, _ := late(context.Background()); st != inbox.TextUnaffordable {
		t.Fatalf("read after the ask: %v", st)
	}
}

// An attachment larger than its meta says is refused once read.
func TestAnAttachmentLargerThanItsMetaSays(t *testing.T) {
	w := newDWorld(t)
	big := strings.Repeat("a", contracts.VendorAttachmentMax+1)
	raw := []byte("From: a@example.com\r\nSubject: Big\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nSee attached\r\n--b\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=big.bin\r\n\r\n" + big + "\r\n--b--\r\n")
	id := inbox.NewMessageID(qNow.Add(-time.Hour))
	m := inboxtest.FromRaw(context.Background(), id, qNow.Add(-time.Hour), 30, raw)
	if len(m.Meta.Attachments) != 1 {
		t.Fatalf("%+v", m.Meta.Attachments)
	}
	m.Meta.Attachments[0].Size = 3
	if err := inboxtest.Seed(context.Background(), w.st.Memory, w.st.Memory, w.b, m); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.discA.Attachment(on(w.askA), w.b, noRules(every), id, 0); !errors.Is(err, contracts.ErrAttachmentTooLarge) {
		t.Fatal(err)
	}
}
