package inbox_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/disclosetest"
	"github.com/permaily-org/permaily/internal/inbox/inboxtest"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/store"
)

// The discloser's tests (ADR 0030's pull request 4: V37 to V40, with ADR
// 0031's H1 to H12, H19 to H22 and H25 to H30) run the real query.Eval,
// rules.Default(), inbox.BlockDecisions over the shared stores of
// disclosetest (one Records, or two enclaves' strict Anchored), and a
// played Asker that keeps its answers per message, as KeptStore does (the
// first kept is the one every enclave uses), and returns their Evidence as
// services/decisions does.

// dClock is a clock the tests move.
type dClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *dClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *dClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// evKept is the kept answers of every message, shared by the enclaves'
// Askers as the table's dec# entries are.
type evKept struct {
	mu sync.Mutex
	m  map[string]map[query.Atom]inbox.Evidence
}

func newEvKept() *evKept { return &evKept{m: map[string]map[query.Atom]inbox.Evidence{}} }

// erase deletes a message's kept answers, as whoever writes the table can.
func (k *evKept) erase(id string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, id)
}

// put keeps e for a message's atom, replacing what is kept: an earlier
// answer put back.
func (k *evKept) put(id string, a query.Atom, e inbox.Evidence) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m[id] == nil {
		k.m[id] = map[query.Atom]inbox.Evidence{}
	}
	k.m[id][a] = e
}

// evAsker is an Asker over a played model: what is kept is used, the rest
// asked (a message atom with its text), kept when it is first, and given
// with its evidence. A refusal is kept and in Evidence, not in Answers.
type evAsker struct {
	kept *evKept
	now  func() time.Time
	// p is the model's answer for an atom of a message; ok false is a
	// failure (nothing received). nil answers 0.05.
	p func(id string, a query.Atom) (float64, bool)
	// refuse says the model refuses the statement.
	refuse func(id string, a query.Atom) bool
	// unsettled says the answer was received and could not be confirmed
	// kept (Asked.Unsettled).
	unsettled func(id string, a query.Atom) bool
	// outcome is AskOff or AskLimit to hold a message's atoms.
	outcome func(id string) inbox.AskOutcome
	// before runs first, and fails Ask with what it returns.
	before func(q *inbox.Question) error

	mu    sync.Mutex
	calls int
	asks  map[string]int
	atoms map[string][]query.Atom
}

func newEvAsker(kept *evKept, now func() time.Time) *evAsker {
	return &evAsker{kept: kept, now: now, asks: map[string]int{}, atoms: map[string][]query.Atom{}}
}

func (a *evAsker) evidence(p float64) inbox.Evidence {
	return inbox.Evidence{P: p, Model: playedModel, Prompt: decide.PromptVersion, At: a.now(), CanaryChecked: "2026-11-13"}
}

func (a *evAsker) Ask(ctx context.Context, q *inbox.Question) (*inbox.Asked, error) {
	if a.before != nil {
		if err := a.before(q); err != nil {
			return nil, err
		}
	}
	a.mu.Lock()
	a.asks[q.MessageID]++
	a.atoms[q.MessageID] = slices.Clone(q.Atoms)
	a.mu.Unlock()
	out := &inbox.Asked{Answers: query.Answers{}, Outcome: inbox.AskAnswered, Evidence: map[query.Atom]inbox.Evidence{}}
	a.kept.mu.Lock()
	kept := a.kept.m[q.MessageID]
	var missing []query.Atom
	needText := false
	for _, at := range q.Atoms {
		if e, ok := kept[at]; ok {
			out.Evidence[at] = e
			if !e.Refused {
				out.Answers[at] = e.P
			}
			continue
		}
		missing = append(missing, at)
		needText = needText || decide.KindOf(at) == decide.ContentMessage
	}
	a.kept.mu.Unlock()
	if len(missing) == 0 {
		return out, nil
	}
	if a.outcome != nil {
		if o := a.outcome(q.MessageID); o == inbox.AskOff || o == inbox.AskLimit {
			out.Held, out.Outcome = missing, o
			return out, nil
		}
	}
	textOK := true
	if needText {
		_, st, err := q.Text(ctx)
		if err != nil {
			return nil, err
		}
		if st == inbox.TextUnaffordable {
			out.Outcome = inbox.AskStop
			return out, nil
		}
		textOK = st == inbox.TextRead
	}
	for _, at := range missing {
		if decide.KindOf(at) == decide.ContentMessage && !textOK {
			continue
		}
		a.mu.Lock()
		a.calls++
		a.mu.Unlock()
		var e inbox.Evidence
		switch {
		case a.refuse != nil && a.refuse(q.MessageID, at):
			e = a.evidence(0)
			e.P, e.Refused = 0, true
		default:
			p, ok := 0.05, true
			if a.p != nil {
				p, ok = a.p(q.MessageID, at)
			}
			if !ok {
				continue
			}
			e = a.evidence(p)
		}
		if a.unsettled != nil && a.unsettled(q.MessageID, at) {
			out.Unsettled = append(out.Unsettled, at)
			continue
		}
		a.kept.mu.Lock()
		if a.kept.m[q.MessageID] == nil {
			a.kept.m[q.MessageID] = map[query.Atom]inbox.Evidence{}
		}
		if first, ok := a.kept.m[q.MessageID][at]; ok {
			e = first // another kept first: that one is used
		} else {
			a.kept.m[q.MessageID][at] = e
		}
		a.kept.mu.Unlock()
		out.Evidence[at] = e
		if !e.Refused {
			out.Answers[at] = e.P
		}
	}
	return out, nil
}

func (a *evAsker) asked(id string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.asks[id]
}

func (a *evAsker) modelCalls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// byMessage answers p for every atom of the messages named, low for the
// others.
func byMessage(ps map[string]float64) func(string, query.Atom) (float64, bool) {
	return func(id string, _ query.Atom) (float64, bool) {
		if p, ok := ps[id]; ok {
			return p, true
		}
		return 0.05, true
	}
}

// dWorld is one mailbox, its store, the decisions of one enclave or two,
// the kept answers both share, and a discloser for each.
type dWorld struct {
	t     *testing.T
	st    *qStore
	b     inbox.Box
	clock *dClock
	kept  *evKept
	// a and b are two enclaves: their decisions, Askers and disclosers.
	decA, decB inbox.Decisions
	askA, askB *evAsker
	discA      inbox.Discloser
	discB      inbox.Discloser
	// records is the one store of blocks when the world is not strict.
	records *disclosetest.Decisions
	// strict is the two enclaves' strict stores, when the world is.
	storeA, storeB inbox.DecisionStore
}

// newDWorld is a world over one shared Records.
func newDWorld(t *testing.T) *dWorld {
	t.Helper()
	w := &dWorld{t: t, st: newQStore(), b: qBox(t), clock: &dClock{t: qNow}, kept: newEvKept()}
	w.records = disclosetest.NewDecisions(w.clock.now)
	w.decA, w.decB = w.records, w.records
	w.storeA, w.storeB = w.records.Records, w.records.Records
	w.finish()
	return w
}

// newStrictDWorld is a world whose two enclaves keep their decisions on
// strict anchored records over one storage, through the stores given (nil
// for disclosetest's unsealed ones).
func newStrictDWorld(t *testing.T, stores func(now func() time.Time) (inbox.DecisionStore, inbox.DecisionStore)) *dWorld {
	t.Helper()
	w := &dWorld{t: t, st: newQStore(), b: qBox(t), clock: &dClock{t: qNow}, kept: newEvKept()}
	if stores == nil {
		stores = disclosetest.Enclaves
	}
	w.storeA, w.storeB = stores(w.clock.now)
	w.decA = &inbox.BlockDecisions{Store: w.storeA, Now: w.clock.now}
	w.decB = &inbox.BlockDecisions{Store: w.storeB, Now: w.clock.now}
	w.finish()
	return w
}

func (w *dWorld) finish() {
	w.askA, w.askB = newEvAsker(w.kept, w.clock.now), newEvAsker(w.kept, w.clock.now)
	w.discA = inbox.NewDiscloserWith(w.st, query.Eval, w.decA, inbox.MatchBounds{Now: w.clock.now}, 0)
	w.discB = inbox.NewDiscloserWith(w.st, query.Eval, w.decB, inbox.MatchBounds{Now: w.clock.now}, 0)
}

// seed stores a message of the box received at (the n-th of that
// nanosecond) with subject and text, and returns its identifier.
func (w *dWorld) seed(at time.Time, n int, subject, body string) string {
	w.t.Helper()
	return seedQ(w.t, w.st, w.b, &qMsg{at: at, n: n, from: []string{"no-reply@example.com"}, subject: subject, body: body})
}

// every is a formula every message of the tests matches.
var every = formula(group(cond(contracts.VarReceived, contracts.OpSince, "2020-01-01T00:00:00Z")))

// allParts are the four parts.
var allParts = []contracts.Part{contracts.PartSender, contracts.PartSubject, contracts.PartBody, contracts.PartAttachments}

// disclosure is what a connection with fs and parts reads, the default
// rule first, the model on.
func disclosure(parts []contracts.Part, fs ...*query.Canonical) inbox.Disclosure {
	if len(fs) == 0 {
		fs = []*query.Canonical{every}
	}
	return inbox.Disclosure{Formulas: fs, Rules: []rules.Rule{rules.Default()}, Parts: parts, Account: "acc_me", Model: true}
}

// on is a request's context with an Asker.
func on(a inbox.Asker) context.Context {
	return inbox.WithAsker(context.Background(), inbox.Asking{Asker: a, Account: "acc_me"})
}

// sorted is a page's outcomes: what left, and what was held, by message.
func sortOut(t *testing.T, items []inbox.Outcome) (left []string, held map[string]*inbox.Withheld) {
	t.Helper()
	held = map[string]*inbox.Withheld{}
	for _, o := range items {
		switch {
		case o.Disclosed != nil && o.Withheld == nil:
			left = append(left, o.Disclosed.ID)
		case o.Withheld != nil && o.Disclosed == nil:
			held[o.Withheld.ID] = o.Withheld
		default:
			t.Fatalf("an outcome is neither or both: %+v", o)
		}
	}
	return left, held
}

// page is one page of disc, which must not fail.
func (w *dWorld) page(ctx context.Context, disc inbox.Discloser, d inbox.Disclosure, before string, limit int) *inbox.DisclosePage {
	w.t.Helper()
	p, err := disc.Page(ctx, w.b, d, before, limit)
	if err != nil {
		w.t.Fatal(err)
	}
	return p
}

// decisionOf reads, through a fresh Begin of an expired key (which stores
// nothing), what is decided of the default rule for id now: the state its
// blocks hold, or Expired when none does.
func decisionOf(t *testing.T, d inbox.Decisions, b inbox.Box, id string, now time.Time) inbox.Decision {
	t.Helper()
	got, err := d.Begin(context.Background(), b, rules.Default().Ref(), []inbox.DecisionKey{{ID: id, ExpiresAt: now.Add(-time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	return got[id]
}

// failWrites is a store of blocks whose writes fail with err once the
// first after writes have been made (writes < 0: none fails).
type failWrites struct {
	inbox.DecisionStore
	mu     sync.Mutex
	writes int
	err    error
}

func (f *failWrites) WriteBlock(ctx context.Context, name string, r *inbox.DecisionRecord, at inbox.DecisionMark) error {
	f.mu.Lock()
	fail := f.writes == 0 && f.err != nil
	if f.writes > 0 {
		f.writes--
	}
	f.mu.Unlock()
	if fail {
		return f.err
	}
	return f.DecisionStore.WriteBlock(ctx, name, r, at)
}

func (f *failWrites) set(writes int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes, f.err = writes, err
}

var errWrites = errors.New("the table refused the write")

// strictParts are the stores of commits two enclaves' strict anchored
// records share, for a test that forks, loses or rolls back a block.
type strictParts struct {
	records *store.Memory
	commits *store.MemoryCommits
}

// strictWith is disclosetest.Enclaves over parts the test keeps.
func strictWith(p *strictParts) func(now func() time.Time) (inbox.DecisionStore, inbox.DecisionStore) {
	p.records, p.commits = store.NewMemory(), store.NewMemoryCommits()
	return func(now func() time.Time) (inbox.DecisionStore, inbox.DecisionStore) {
		one := func() inbox.DecisionStore {
			return disclosetest.Anchored{Records: &store.Anchored{Records: p.records, Commits: p.commits, Strict: true, OnlyNewest: true, Now: now}}
		}
		return one(), one()
	}
}

// seedWith stores q in the world's box, kept days days from q.at.
func seedWith(w *dWorld, q *qMsg, days int) error {
	m := inboxtest.Message{ID: q.id(), ReceivedAt: q.at, RetentionDays: days, Raw: q.raw(), Meta: q.meta(), Search: q.entry()}
	return inboxtest.Seed(context.Background(), w.st.Memory, w.st.Memory, w.b, m)
}
