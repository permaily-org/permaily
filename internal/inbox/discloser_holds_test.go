package inbox_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/disclosetest"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
)

// heldBy says how a page held id, "" when it did not.
func heldBy(t *testing.T, p *inbox.DisclosePage, id string) inbox.HeldWhy {
	t.Helper()
	_, held := sortOut(t, p.Items)
	if h := held[id]; h != nil {
		return h.Why
	}
	return ""
}

// leftIn says the page gave id.
func leftIn(t *testing.T, p *inbox.DisclosePage, id string) bool {
	t.Helper()
	left, _ := sortOut(t, p.Items)
	return slices.Contains(left, id)
}

// H5, finding 1's schedules through either enclave: a decision begun, the
// model answering 0.90 (or refusing), then every Settle refused (a crash
// before it is the same), the kept answer deleted and the provider
// answering 0.05: the message is held for that request and every one
// within the grace, recovered Held by the first after it, and the late
// Cleared of the first attempt loses.
func TestH5ALostSettlementIsRecoveredHeld(t *testing.T) {
	for _, refusal := range []bool{false, true} {
		t.Run(map[bool]string{false: "an answer", true: "a refusal"}[refusal], func(t *testing.T) {
			var parts strictParts
			fa := &failWrites{}
			w := newStrictDWorld(t, func(now func() time.Time) (inbox.DecisionStore, inbox.DecisionStore) {
				a, b := strictWith(&parts)(now)
				fa.DecisionStore = a
				return fa, b
			})
			id := w.seed(qNow.Add(-time.Hour), 1, "Hello", "Here is your number: 482913.")
			w.askA.p = byMessage(map[string]float64{id: 0.9})
			if refusal {
				w.askA.refuse = func(string, query.Atom) bool { return true }
			}
			var attempt inbox.Attempt
			w.askA.before = func(q *inbox.Question) error {
				got, err := w.decB.Begin(context.Background(), w.b, rules.Default().Ref(), []inbox.DecisionKey{{ID: q.MessageID}})
				if err != nil || got[q.MessageID].State != inbox.DecisionPending {
					t.Errorf("not pending when asked: %+v %v", got, err)
				}
				fa.set(0, errWrites) // the settlement is refused at every try
				return nil
			}
			p := w.page(on(w.askA), w.discA, disclosure(allParts), "", 10)
			if heldBy(t, p, id) != inbox.HeldUndecided {
				t.Fatalf("the first request: %+v", p.Items)
			}
			fa.set(-1, nil)
			w.askA.before = nil
			w.kept.erase(id)
			w.askB.p = byMessage(nil)
			for _, enclave := range []inbox.Discloser{w.discA, w.discB} {
				if heldBy(t, w.page(on(w.askB), enclave, disclosure(allParts), "", 10), id) != inbox.HeldUndecided {
					t.Fatal("released within the grace")
				}
			}
			w.clock.add(inbox.PendingGrace)
			if why := heldBy(t, w.page(on(w.askB), w.discB, disclosure(allParts), "", 10), id); why != inbox.HeldRecovered {
				t.Fatalf("after the grace: %q", why)
			}
			got, err := w.decA.Settle(context.Background(), w.b, rules.Default().Ref(), map[string]inbox.Settlement{id: {State: inbox.DecisionCleared, Attempt: attempt}})
			if err != nil || got[id].State != inbox.DecisionHeld || !got[id].Recovered {
				t.Fatalf("the late settlement: %+v %v", got, err)
			}
			if w.askB.asked(id) != 0 {
				t.Fatal("a pending decision's message was asked about again")
			}
		})
	}
}

// H6, finding 2: A commits Held on 0.90; an older kept answer that would
// clear is put back; B, whose reads are strict and made now, holds it and
// asks nothing. An earlier copy of the day's block put back is refused:
// every model-dependent message of that day held, undecided.
func TestH6AHoldCommittedIsSeenByTheOtherEnclave(t *testing.T) {
	var parts strictParts
	w := newStrictDWorld(t, strictWith(&parts))
	m := w.seed(qNow.Add(-time.Hour), 1, "Hello", "Here is your number: 482913.")
	n := w.seed(qNow.Add(-2*time.Hour), 1, "Lunch", "See you")
	// B reads the day first: nothing of M yet.
	if p := w.page(on(w.askB), w.discB, disclosure(allParts, formula(group(cond(contracts.VarSubject, contracts.OpContains, "lunch")))), "", 10); !leftIn(t, p, n) {
		t.Fatal("N was not given")
	}
	// The block as B wrote it: an earlier copy whoever writes the table
	// keeps.
	name, _ := inbox.RuleDecisionsName(w.b.MailboxID, m, 0)
	earlier, err := parts.records.GetRecord(context.Background(), versionNames(t, &parts, name)[0])
	if err != nil {
		t.Fatal(err)
	}
	w.askA.p = byMessage(map[string]float64{m: 0.9})
	if heldBy(t, w.page(on(w.askA), w.discA, disclosure(allParts), "", 10), m) != inbox.HeldDecided {
		t.Fatal("A did not hold M")
	}
	w.kept.put(m, codeAtom, w.askB.evidence(0.01))
	w.kept.put(m, resetAtom, w.askB.evidence(0.01))
	w.clock.add(4 * time.Second)
	if heldBy(t, w.page(on(w.askB), w.discB, disclosure(allParts), "", 10), m) != inbox.HeldDecided || w.askB.asked(m) != 0 {
		t.Fatal("B released M on an older answer")
	}
	// The earlier copy put back where the newest is.
	versions := versionNames(t, &parts, name)
	earlier.Name = versions[len(versions)-1]
	if err := parts.records.PutRecord(context.Background(), earlier, false); err != nil {
		t.Fatal(err)
	}
	for _, enclave := range []inbox.Discloser{w.discA, w.discB} {
		p := w.page(on(w.askB), enclave, disclosure(allParts), "", 10)
		if heldBy(t, p, m) != inbox.HeldUndecided || heldBy(t, p, n) != inbox.HeldUndecided {
			t.Fatalf("a block put back: %+v", p.Items)
		}
	}
}

// versionNames are the stored versions of a strict record, oldest first.
func versionNames(t *testing.T, p *strictParts, name string) []string {
	t.Helper()
	var out []string
	for _, n := range p.records.RecordNames() {
		if strings.HasPrefix(n, name+"@") {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	if len(out) == 0 {
		t.Fatalf("no version of %s", name)
	}
	return out
}

// H7 and H8: a block forked, or lost, holds every model-dependent message of
// its day, undecided, through both enclaves; a message of another day is
// decided as before, and an exact hold stays exact.
func TestH7H8AForkedOrLostBlockHoldsItsDay(t *testing.T) {
	for _, how := range []string{"forked", "lost"} {
		t.Run(how, func(t *testing.T) {
			var parts strictParts
			w := newStrictDWorld(t, strictWith(&parts))
			m := w.seed(qNow.Add(-time.Hour), 1, "Lunch", "See you")
			n := w.seed(qNow.Add(-time.Hour-time.Minute), 1, "Dinner", "See you")
			yesterday := w.seed(qNow.Add(-26*time.Hour), 1, "Coffee", "See you")
			if p := w.page(on(w.askA), w.discA, disclosure(allParts), "", 10); !leftIn(t, p, m) || !leftIn(t, p, yesterday) {
				t.Fatalf("not given: %+v", p.Items)
			}
			name, _ := inbox.RuleDecisionsName(w.b.MailboxID, m, 0)
			h, err := parts.commits.History(context.Background(), name)
			if err != nil || h.Head == nil {
				t.Fatal(err)
			}
			switch how {
			case "forked":
				parts.commits.Fork(name, h.Head.Version, sha256.Sum256([]byte("another")))
			case "lost":
				versions := versionNames(t, &parts, name)
				if err := parts.records.DeleteRecord(context.Background(), versions[len(versions)-1]); err != nil {
					t.Fatal(err)
				}
			}
			for _, enclave := range []inbox.Discloser{w.discA, w.discB} {
				p := w.page(on(w.askB), enclave, disclosure(allParts), "", 10)
				if heldBy(t, p, m) != inbox.HeldUndecided || heldBy(t, p, n) != inbox.HeldUndecided || !leftIn(t, p, yesterday) {
					t.Fatalf("%s: %+v", how, p.Items)
				}
			}
		})
	}
}

// H9: a block that cannot be written at every try: the page holds the
// messages of its day the model would decide, undecided, asks nothing for
// them, and gives the rest (exact holds stay exact, another day's are
// decided); once the writes work, what it began and could not settle is
// recovered Held after the grace, never cleared.
func TestH9ABlockThatCannotBeWritten(t *testing.T) {
	fa := &failWrites{}
	w := newStrictDWorld(t, func(now func() time.Time) (inbox.DecisionStore, inbox.DecisionStore) {
		a, b := disclosetest.Enclaves(now)
		fa.DecisionStore = a
		return fa, b
	})
	m := w.seed(qNow.Add(-time.Hour), 1, "Lunch", "See you")
	code := w.seed(qNow.Add(-time.Hour-time.Minute), 1, "Your passcode", "")
	fa.set(0, errWrites)
	p := w.page(on(w.askA), w.discA, disclosure(allParts), "", 10)
	if heldBy(t, p, m) != inbox.HeldUndecided || heldBy(t, p, code) != inbox.HeldExact || w.askA.asked(m) != 0 {
		t.Fatalf("every write refused: %+v", p.Items)
	}
	// Begun, then the settlement refused at every try.
	fa.set(1, errWrites)
	if heldBy(t, w.page(on(w.askA), w.discA, disclosure(allParts), "", 10), m) != inbox.HeldUndecided || w.askA.asked(m) != 1 {
		t.Fatal("begun and not settled: not held, or not asked")
	}
	fa.set(-1, nil)
	w.clock.add(inbox.PendingGrace)
	if heldBy(t, w.page(on(w.askA), w.discA, disclosure(allParts), "", 10), m) != inbox.HeldRecovered {
		t.Fatal("not recovered held")
	}
}

// H10: two enclaves disclosing one message at once: one begins it and asks,
// the other finds it Pending and holds it, and both end held or both given
// on its one decision; two disclosing different messages of a new day at
// once (both making block 0): both kept in one block.
func TestH10TwoEnclavesAtOnce(t *testing.T) {
	t.Run("one message", func(t *testing.T) {
		w := newStrictDWorld(t, nil)
		m := w.seed(qNow.Add(-time.Hour), 1, "Hello", "Here is your number: 482913.")
		w.askA.p, w.askB.p = byMessage(map[string]float64{m: 0.9}), byMessage(map[string]float64{m: 0.9})
		var wg sync.WaitGroup
		pages := make([]*inbox.DisclosePage, 2)
		for i, disc := range []inbox.Discloser{w.discA, w.discB} {
			ask := []*evAsker{w.askA, w.askB}[i]
			wg.Go(func() { pages[i] = w.page(on(ask), disc, disclosure(allParts), "", 10) })
		}
		wg.Wait()
		for _, p := range pages {
			if why := heldBy(t, p, m); why != inbox.HeldDecided && why != inbox.HeldUndecided {
				t.Fatalf("given or not listed: %q", why)
			}
		}
		if w.askA.asked(m)+w.askB.asked(m) > 2 {
			t.Fatal("asked more than once by each")
		}
		if got := decisionOf(t, w.decB, w.b, m, w.clock.now()); got.State != inbox.DecisionHeld {
			t.Fatalf("decision %+v", got)
		}
	})
	t.Run("two messages of a new day", func(t *testing.T) {
		w := newStrictDWorld(t, nil)
		m := w.seed(qNow.Add(-time.Hour), 1, "Lunch", "See you")
		n := w.seed(qNow.Add(-2*time.Hour), 1, "Dinner", "See you")
		onlyM := formula(group(cond(contracts.VarSubject, contracts.OpContains, "lunch")))
		onlyN := formula(group(cond(contracts.VarSubject, contracts.OpContains, "dinner")))
		pa := &pauseFirst{DecisionStore: w.storeA}
		discA := inbox.NewDiscloserWith(w.st, query.Eval, &inbox.BlockDecisions{Store: pa, Now: w.clock.now}, inbox.MatchBounds{Now: w.clock.now}, 0)
		pa.between = func() {
			if !leftIn(t, w.page(on(w.askB), w.discB, disclosure(allParts, onlyN), "", 10), n) {
				t.Error("B did not give N")
			}
		}
		if !leftIn(t, w.page(on(w.askA), discA, disclosure(allParts, onlyM), "", 10), m) {
			t.Fatal("A did not give M after its write was refused once")
		}
		for _, id := range []string{m, n} {
			if got := decisionOf(t, w.decA, w.b, id, w.clock.now()); got.State != inbox.DecisionCleared {
				t.Fatalf("%s: %+v", id[:8], got)
			}
		}
	})
}

// pauseFirst runs between once, before its first write.
type pauseFirst struct {
	inbox.DecisionStore
	once    sync.Once
	between func()
}

func (p *pauseFirst) WriteBlock(ctx context.Context, name string, r *inbox.DecisionRecord, at inbox.DecisionMark) error {
	p.once.Do(func() {
		if p.between != nil {
			p.between()
		}
	})
	return p.DecisionStore.WriteBlock(ctx, name, r, at)
}

// fullDay writes a day's DecisionBlocksMax blocks, the last full of other
// messages' cleared decisions, as a day that took every decision it could.
func fullDay(t *testing.T, s inbox.DecisionStore, b inbox.Box, of string) {
	t.Helper()
	ctx := context.Background()
	day, _ := inbox.DecisionDay(of)
	for i := range inbox.DecisionBlocksMax {
		name, _ := inbox.RuleDecisionsName(b.MailboxID, of, i)
		r := &inbox.DecisionRecord{V: 1, Mailbox: b.MailboxID, Day: day, Block: i, Next: i < inbox.DecisionBlocksMax-1, Entries: []inbox.DecisionEntry{}}
		if i == inbox.DecisionBlocksMax-1 {
			at := qNow.Add(-12 * time.Hour)
			for j := range inbox.DecisionsMax {
				r.Entries = append(r.Entries, inbox.DecisionEntry{Message: inbox.NewMessageID(at.Add(time.Duration(j))), Rule: rules.DefaultID, Version: rules.DefaultVersion, State: inbox.DecisionCleared, At: at, Expires: qNow.Add(30 * 24 * time.Hour)})
			}
		}
		_, mark, err := s.ReadBlock(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.WriteBlock(ctx, name, r, mark); err != nil {
			t.Fatal(err)
		}
	}
}

// H11, finding 11 and the owner's decision 12 through the Discloser: a day
// whose 64 blocks are spent holds its next model-dependent message,
// undecided, on its first read and every later one, and counts it in
// decisions_full once per request (DecisionsFullCounter); a message past
// its expiry is not counted, nor given; a message of another day is
// decided as before.
func TestH11AFullDayHoldsAndCounts(t *testing.T) {
	w := newDWorld(t)
	m := w.seed(qNow.Add(-time.Hour), 1, "Lunch", "See you")
	fullDay(t, w.records.Records, w.b, m)
	yesterday := w.seed(qNow.Add(-26*time.Hour), 1, "Coffee", "See you")
	counter := w.discA.(inbox.DecisionsFullCounter)
	for i := range 2 {
		p := w.page(on(w.askA), w.discA, disclosure(allParts), "", 10)
		if heldBy(t, p, m) != inbox.HeldUndecided || !leftIn(t, p, yesterday) || w.askA.asked(m) != 0 {
			t.Fatalf("request %d: %+v", i, p.Items)
		}
		if got := counter.DecisionsFull(); got != int64(i+1) {
			t.Fatalf("decisions_full %d after %d requests", got, i+1)
		}
	}
	if o, err := w.discA.Message(on(w.askA), w.b, disclosure(allParts), m); err != nil || o.Withheld == nil || counter.DecisionsFull() != 3 {
		t.Fatalf("a read: %+v %v (%d)", o, err, counter.DecisionsFull())
	}
	// A message that has expired by the decisions' clock: expired, not full.
	w2 := newDWorld(t)
	at := qNow.Add(-24*time.Hour + time.Minute)
	if err := seedRetention(w2, at, 1); err != nil {
		t.Fatal(err)
	}
	gone := (&qMsg{at: at, n: 1}).id()
	fullDay(t, w2.records.Records, w2.b, gone)
	w2.clock.add(2 * time.Minute)
	c2 := w2.discA.(inbox.DecisionsFullCounter)
	p := w2.page(on(w2.askA), w2.discA, disclosure(allParts), "", 10)
	if len(p.Items) != 0 || c2.DecisionsFull() != 0 {
		t.Fatalf("an expired message: %+v, counted %d", p.Items, c2.DecisionsFull())
	}
}

// seedRetention stores a message received at that is kept days days.
func seedRetention(w *dWorld, at time.Time, days int) error {
	q := &qMsg{at: at, n: 1, from: []string{"a@example.com"}, subject: "Lunch", body: "See you"}
	return seedWith(w, q, days)
}

// H20: a new version of a rule decides again: its decision is its own, and
// a decision of the old version is not used for it.
func TestH20ANewVersionDecidesAgain(t *testing.T) {
	w := newDWorld(t)
	m := w.seed(qNow.Add(-time.Hour), 1, "Hello", "Here is your number: 482913.")
	w.askA.p = byMessage(map[string]float64{m: 0.9})
	if heldBy(t, w.page(on(w.askA), w.discA, disclosure(allParts), "", 10), m) != inbox.HeldDecided {
		t.Fatal("v1 did not hold")
	}
	v2 := rules.Default()
	v2.Version = 2
	w.kept.erase(m)
	w.askA.p = byMessage(nil)
	d := disclosure(allParts)
	d.Rules = []rules.Rule{v2}
	if !leftIn(t, w.page(on(w.askA), w.discA, d, "", 10), m) {
		t.Fatal("v2 used v1's decision")
	}
	got, err := w.decA.Begin(context.Background(), w.b, v2.Ref(), []inbox.DecisionKey{{ID: m}})
	if err != nil || got[m].State != inbox.DecisionCleared {
		t.Fatal(got, err)
	}
}

// H21: the guard changing during a page: the messages it holds back are
// held, undecided, their decisions abandoned, and the page says the model
// is off; the next request decides them.
func TestH21TheGuardChangingDuringAPage(t *testing.T) {
	w := newDWorld(t)
	first := w.seed(qNow.Add(-time.Hour), 1, "Lunch", "See you")
	second := w.seed(qNow.Add(-2*time.Hour), 1, "Dinner", "See you")
	w.askA.outcome = func(id string) inbox.AskOutcome {
		if id == second {
			return inbox.AskOff
		}
		return inbox.AskAnswered
	}
	p := w.page(on(w.askA), w.discA, disclosure(allParts), "", 10)
	if !leftIn(t, p, first) || heldBy(t, p, second) != inbox.HeldUndecided || p.Model != contracts.MatchModelOff {
		t.Fatalf("%+v (%q)", p.Items, p.Model)
	}
	if got := decisionOf(t, w.decA, w.b, second, w.clock.now()); got.State != inbox.DecisionExpired {
		t.Fatalf("not abandoned: %+v", got)
	}
	w.askA.outcome = nil
	if p := w.page(on(w.askA), w.discA, disclosure(allParts), "", 10); !leftIn(t, p, second) {
		t.Fatal("the next request did not decide it")
	}
}

// H22: a preview with the model through A, then a vendor's read through B
// after the kept answers were deleted, meet the same decision and the same
// evidence; nothing is asked again for the rule.
func TestH22APreviewAndARead(t *testing.T) {
	w := newStrictDWorld(t, nil)
	m := w.seed(qNow.Add(-time.Hour), 1, "Hello", "Here is your number: 482913.")
	n := w.seed(qNow.Add(-2*time.Hour), 1, "Lunch", "See you")
	w.askA.p = byMessage(map[string]float64{m: 0.9})
	preview := w.page(on(w.askA), w.discA, disclosure([]contracts.Part{contracts.PartSender, contracts.PartSubject}), "", 10)
	_, held := sortOut(t, preview.Items)
	if held[m] == nil || held[m].Why != inbox.HeldDecided || !leftIn(t, preview, n) {
		t.Fatalf("the preview: %+v", preview.Items)
	}
	w.kept.erase(m)
	w.kept.erase(n)
	w.askB.p = byMessage(map[string]float64{m: 0.01, n: 0.99})
	om, err := w.discB.Message(on(w.askB), w.b, disclosure(allParts), m)
	if err != nil || om.Withheld == nil || !equalDecided(om.Withheld.Decided, held[m].Decided) {
		t.Fatalf("the read of M: %+v %v", om, err)
	}
	on2, err := w.discB.Message(on(w.askB), w.b, disclosure(allParts), n)
	if err != nil || on2.Disclosed == nil || len(on2.Disclosed.Rules) != 1 || on2.Disclosed.Rules[0].Decided[0].Evidence.P != 0.05 {
		t.Fatalf("the read of N: %+v %v", on2, err)
	}
	if w.askB.asked(m) != 0 || w.askB.asked(n) != 0 {
		t.Fatal("asked again")
	}
}

func equalDecided(a, b []inbox.Decided) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return len(a) > 0 && string(ja) == string(jb)
}

// H25, R1 through the Discloser on strict records: A reads the day without
// M and pauses before it writes; B begins M, asks (0.90) and settles it
// Held; A's write is refused, A reads again and holds M without asking; a
// later low answer changes nothing; M is in one block.
func TestH25OneBlockOwnsAMessageThroughTheDiscloser(t *testing.T) {
	w := newStrictDWorld(t, nil)
	m := w.seed(qNow.Add(-time.Hour), 1, "Hello", "Here is your number: 482913.")
	w.askB.p = byMessage(map[string]float64{m: 0.9})
	pa := &pauseFirst{DecisionStore: w.storeA}
	discA := inbox.NewDiscloserWith(w.st, query.Eval, &inbox.BlockDecisions{Store: pa, Now: w.clock.now}, inbox.MatchBounds{Now: w.clock.now}, 0)
	pa.between = func() {
		if heldBy(t, w.page(on(w.askB), w.discB, disclosure(allParts), "", 10), m) != inbox.HeldDecided {
			t.Error("B did not hold M")
		}
		w.kept.erase(m)
	}
	w.askA.p = byMessage(nil)
	if heldBy(t, w.page(on(w.askA), discA, disclosure(allParts), "", 10), m) != inbox.HeldDecided || w.askA.asked(m) != 0 {
		t.Fatal("A released or asked about M")
	}
	blocks := 0
	for i := range 3 {
		name, _ := inbox.RuleDecisionsName(w.b.MailboxID, m, i)
		if r, _, err := w.storeB.ReadBlock(context.Background(), name); err == nil && r != nil {
			for _, e := range r.Entries {
				if e.Message == m {
					blocks++
				}
			}
		}
	}
	if blocks != 1 {
		t.Fatalf("M in %d blocks", blocks)
	}
}

// H26, R2 through the Discloser: 0.50 for the code and nothing for the
// reset: Held with the 0.50; the kept entry erased and the other enclave
// answering 0.05 and 0.05: still Held, with the 0.50 as its evidence.
func TestH26APartialAnswerIsHeldThroughTheDiscloser(t *testing.T) {
	w := newStrictDWorld(t, nil)
	m := w.seed(qNow.Add(-time.Hour), 1, "Hello", "Here is your number: 482913.")
	w.askA.p = func(_ string, a query.Atom) (float64, bool) { return 0.5, a == codeAtom }
	p := w.page(on(w.askA), w.discA, disclosure(allParts), "", 10)
	_, held := sortOut(t, p.Items)
	if held[m] == nil || held[m].Why != inbox.HeldDecided || len(held[m].Decided) != 1 || held[m].Decided[0].Evidence.P != 0.5 {
		t.Fatalf("%+v", held[m])
	}
	w.kept.erase(m)
	w.askB.p = byMessage(nil)
	o, err := w.discB.Message(on(w.askB), w.b, disclosure(allParts), m)
	if err != nil || o.Withheld == nil || len(o.Withheld.Decided) != 1 || o.Withheld.Decided[0].Evidence.P != 0.5 {
		t.Fatalf("%+v %v", o, err)
	}
}

// H27, R3 through the Discloser: a day whose only block's versions were
// spent by begun and abandoned attempts opens its successor and decides
// there; a message past its expiry is not counted as full.
func TestH27ASpentBlockOpensItsSuccessor(t *testing.T) {
	w := newDWorld(t)
	m := w.seed(qNow.Add(-time.Hour), 1, "Lunch", "See you")
	w.askA.p = func(string, query.Atom) (float64, bool) { return 0, false } // begun, then abandoned
	if heldBy(t, w.page(on(w.askA), w.discA, disclosure(allParts), "", 10), m) != inbox.HeldUndecided {
		t.Fatal("not held")
	}
	name, _ := inbox.RuleDecisionsName(w.b.MailboxID, m, 0)
	w.records.Records.Age(name, inbox.DecisionsVersionsMax-1)
	w.askA.p = nil
	if !leftIn(t, w.page(on(w.askA), w.discA, disclosure(allParts), "", 10), m) {
		t.Fatal("the day did not go on")
	}
	if w.records.Blocks(w.b, m) != 2 || w.discA.(inbox.DecisionsFullCounter).DecisionsFull() != 0 {
		t.Fatalf("%d blocks", w.records.Blocks(w.b, m))
	}
}

// recordingDecisions records the attempts Begin answered and the ones each
// Settle gave back.
type recordingDecisions struct {
	inbox.Decisions
	mu      sync.Mutex
	begun   map[string]inbox.Attempt
	settled map[string]inbox.Settlement
}

func (r *recordingDecisions) Begin(ctx context.Context, b inbox.Box, rule rules.Ref, ids []inbox.DecisionKey) (map[string]inbox.Decision, error) {
	got, err := r.Decisions.Begin(ctx, b, rule, ids)
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, d := range got {
		if d.State == inbox.DecisionBegun {
			r.begun[id] = d.Attempt
		}
	}
	return got, err
}

func (r *recordingDecisions) Settle(ctx context.Context, b inbox.Box, rule rules.Ref, settle map[string]inbox.Settlement) (map[string]inbox.Decision, error) {
	r.mu.Lock()
	for id, s := range settle {
		r.settled[id] = s
	}
	r.mu.Unlock()
	return r.Decisions.Settle(ctx, b, rule, settle)
}

// H29, F1 through the Discloser: every settlement it gives of what it
// began names the attempt Begin answered (Cleared, Held and Abandoned
// alike), so its retry or a late one never drops or clears another's
// Pending; the contract's H29 case runs on the sealed store
// (services/mailboxes).
func TestH29TheDiscloserGivesBackEachAttempt(t *testing.T) {
	w := newDWorld(t)
	ids := []string{
		w.seed(qNow.Add(-time.Hour), 1, "Lunch", "See you"),
		w.seed(qNow.Add(-2*time.Hour), 1, "Hello", "Here is your number: 482913."),
		w.seed(qNow.Add(-26*time.Hour), 1, "Dinner", "See you"),
	}
	w.askA.p = byMessage(map[string]float64{ids[1]: 0.9})
	w.askA.outcome = func(id string) inbox.AskOutcome {
		if id == ids[2] {
			return inbox.AskLimit
		}
		return inbox.AskAnswered
	}
	rec := &recordingDecisions{Decisions: w.decA, begun: map[string]inbox.Attempt{}, settled: map[string]inbox.Settlement{}}
	disc := inbox.NewDiscloserWith(w.st, query.Eval, rec, inbox.MatchBounds{Now: w.clock.now}, 0)
	w.page(on(w.askA), disc, disclosure(allParts), "", 10)
	want := map[string]inbox.DecisionState{ids[0]: inbox.DecisionCleared, ids[1]: inbox.DecisionHeld, ids[2]: inbox.DecisionAbandoned}
	for id, state := range want {
		s, ok := rec.settled[id]
		if !ok || s.State != state || s.Attempt != rec.begun[id] || s.Attempt.Version == 0 {
			t.Fatalf("%s: settled %+v, begun %+v", id[:8], s, rec.begun[id])
		}
	}
}

// H30, F2 through the Discloser: sequential pages (each settled before
// the next) fill a day's first block, retired at its DecisionsMax
// settled decisions and within its bytes; near a block's end, what the tail
// cannot take while its live Pending settle is held, undecided, for that
// request only and counted full (the cost F2 states), and a later search
// gives it from the same day.
func TestH30SequentialPagesFillTheBlocks(t *testing.T) {
	w := newDWorld(t)
	base := qNow.Add(-6 * time.Hour)
	n := inbox.DecisionsMax + 2*inbox.DiscloseMaxLimit
	for i := range n {
		w.seed(base.Add(-time.Duration(i)*time.Millisecond), i, "Lunch", "See you")
	}
	walk := func() (given, held int) {
		before := ""
		for {
			p := w.page(on(w.askA), w.discA, disclosure(allParts), before, inbox.DiscloseMaxLimit)
			left, out := sortOut(t, p.Items)
			for _, h := range out {
				if h.Why != inbox.HeldUndecided {
					t.Fatalf("held %q", h.Why)
				}
			}
			given, held = given+len(left), held+len(out)
			if p.Next == "" {
				return given, held
			}
			before = p.Next
		}
	}
	given, held := walk()
	full := w.discA.(inbox.DecisionsFullCounter).DecisionsFull()
	if given+held != n || int64(held) != full {
		t.Fatalf("%d given and %d held of %d; %d counted full", given, held, n, full)
	}
	if given, held := walk(); given != n || held != 0 {
		t.Fatalf("the second search: %d given, %d held", given, held)
	}
	id := (&qMsg{at: base, n: 0}).id()
	for i := range 1 {
		name, _ := inbox.RuleDecisionsName(w.b.MailboxID, id, i)
		r, _, err := w.records.Records.ReadBlock(context.Background(), name)
		if err != nil || r == nil || !r.Next || len(r.Entries) != inbox.DecisionsMax {
			t.Fatalf("block %d: %+v (%v)", i, r != nil && r.Next, err)
		}
		if b, _ := json.Marshal(r); len(b) > inbox.DecisionsBytesMax {
			t.Fatalf("block %d: %d bytes", i, len(b))
		}
	}
}
