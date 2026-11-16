package inbox_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/disclosetest"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
)

var decisionRule = rules.Ref{ID: "default-sign-in-and-security", Version: 1}

// The default rule's two atoms, and evidence of them as the real Asker
// returns it.
var (
	codeAtom  = query.Atom{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: "a sign-in, login or verification code, or a one-time password"}
	resetAtom = query.Atom{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: "resetting a password or recovering an account"}
)

func answer(p float64, at time.Time) inbox.Evidence {
	return inbox.Evidence{P: p, Model: "gpt-6-luna-2026-10-01", Prompt: "p2-2026-11-15", At: at, CanaryChecked: "2026-11-15", CanaryChange: "2026-11-01"}
}

func defaultEvidence(at time.Time) []inbox.Decided {
	return []inbox.Decided{{Atom: codeAtom, Evidence: answer(0.05, at)}, {Atom: resetAtom, Evidence: answer(0.04, at)}}
}

// A decision's block is named by the day in the message's identifier and
// its number (ADR 0031, "Holds that cannot be undone").
func TestADecisionsRecordIsTheMessagesDay(t *testing.T) {
	at := time.Date(2026, 11, 16, 23, 59, 59, 0, time.UTC)
	name, err := inbox.RuleDecisionsName("mbx_a", inbox.NewMessageID(at), 3)
	if err != nil || name != "rule-decisions-mbx_a-20261116-3" {
		t.Fatalf("%q %v", name, err)
	}
	if _, err := inbox.RuleDecisionsName("mbx_a", "not-an-id", 0); err == nil {
		t.Fatal("a name for no message")
	}
}

// The bounds of one block (findings 11 and N4): a decision past the count,
// the bytes (every Pending counted at its largest settlement) or the
// versions (one kept for each Pending's settlement and one for Next) is
// Full and nothing is stored for it; expired decisions are dropped from the
// next version written, and only from a write made anyway.
func TestADecisionsRecordIsBounded(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	keys := func(n int, from time.Time) []inbox.DecisionKey {
		var ks []inbox.DecisionKey
		for i := range n {
			ks = append(ks, inbox.DecisionKey{ID: inbox.NewMessageID(from.Add(time.Duration(i) * time.Millisecond)), ExpiresAt: at.Add(time.Hour)})
		}
		return ks
	}
	// Pending at their largest settlement: about a hundred and forty fit.
	r := &inbox.DecisionRecord{V: 1, Mailbox: "mbx_a", Day: "20261116"}
	ks := keys(inbox.DiscloseMaxLimit*4, at)
	got, changed := r.Begin(decisionRule, ks, at, 0)
	begun := 0
	for _, k := range ks {
		if got[k.ID].State == inbox.DecisionBegun {
			begun++
		}
	}
	if !changed || begun < 100 || begun == len(ks) || len(r.Entries) != begun {
		t.Fatalf("changed %v, %d begun of %d", changed, begun, len(ks))
	}
	b, _ := json.Marshal(r)
	if len(b)+begun*inbox.DecisionEvidenceBytesMax > inbox.DecisionsBytesMax {
		t.Fatalf("%d bytes with %d pending", len(b), begun)
	}

	// Settled with the default rule's evidence, pages of 50 fill a block to
	// within a page of its count (the last page's pending reserve their
	// largest settlement); the rest goes to the next block.
	settled := &inbox.DecisionRecord{V: 1}
	for i := 0; i < inbox.DecisionsMax+inbox.DiscloseMaxLimit; i += inbox.DiscloseMaxLimit {
		page := keys(inbox.DiscloseMaxLimit, at.Add(time.Duration(i)*time.Second))
		got, _ := settled.Begin(decisionRule, page, at, uint64(i/inbox.DiscloseMaxLimit*2))
		s := map[string]inbox.Settlement{}
		for _, k := range page {
			if got[k.ID].State == inbox.DecisionBegun {
				s[k.ID] = inbox.Settlement{State: inbox.DecisionCleared, Evidence: defaultEvidence(at)}
			}
		}
		settled.Settle(decisionRule, s, at, uint64(i/inbox.DiscloseMaxLimit*2+1))
	}
	if len(settled.Entries) < inbox.DecisionsMax-inbox.DiscloseMaxLimit || len(settled.Entries) > inbox.DecisionsMax {
		t.Fatalf("%d settled", len(settled.Entries))
	}
	if b, _ := json.Marshal(settled); len(b) > inbox.DecisionsBytesMax || len(b) > disclosetest.ItemMax {
		t.Fatalf("%d settled bytes", len(b))
	}

	// Versions: none left for a settlement and Next.
	full := &inbox.DecisionRecord{V: 1}
	if got, changed := full.Begin(decisionRule, ks[:1], at, inbox.DecisionsVersionsMax-2); changed || got[ks[0].ID].State != inbox.DecisionFull {
		t.Fatalf("past its versions: %v %+v", changed, got)
	}
	if got, changed := full.Begin(decisionRule, ks[:1], at, inbox.DecisionsVersionsMax-3); !changed || got[ks[0].ID].State != inbox.DecisionBegun {
		t.Fatalf("within its versions: %v %+v", changed, got)
	}

	// A rule whose name is long takes more of the bytes.
	heavy := &inbox.DecisionRecord{V: 1}
	long := rules.Ref{ID: strings.Repeat("r", 200), Version: 1}
	heavy.Begin(long, ks, at, 0)
	b, _ = json.Marshal(heavy)
	if len(b)+len(heavy.Entries)*inbox.DecisionEvidenceBytesMax > inbox.DecisionsBytesMax {
		t.Fatalf("bytes %d", len(b))
	}

	// Expired decisions go with the next write DecisionKeptAfter after their
	// message, never on their own.
	later := at.Add(2 * time.Hour)
	if got, changed := r.Begin(decisionRule, ks[:1], later, 1); changed || got[ks[0].ID].State != inbox.DecisionPending || len(r.Entries) != begun {
		t.Fatalf("pruned without a write: %d entries", len(r.Entries))
	}
	fresh := inbox.DecisionKey{ID: inbox.NewMessageID(later), ExpiresAt: later.Add(time.Hour)}
	if got, changed := r.Begin(decisionRule, []inbox.DecisionKey{fresh}, later, 1); !changed || got[fresh.ID].State != inbox.DecisionBegun || len(r.Entries) != 1 {
		t.Fatalf("expired kept: %d entries", len(r.Entries))
	}
	gone := inbox.DecisionKey{ID: inbox.NewMessageID(at), ExpiresAt: at}
	if got, changed := r.Begin(decisionRule, []inbox.DecisionKey{gone}, later, 2); changed || got[gone.ID].State != inbox.DecisionExpired || got[gone.ID].Leaves() {
		t.Fatalf("an expired key begun: %v %+v", changed, got)
	}
}

// Settling where the block has no version left changes nothing: a pending
// decision stays pending (held, undecided), never cleared.
func TestSettlingPastTheVersionsChangesNothing(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	id := inbox.NewMessageID(at)
	r := &inbox.DecisionRecord{V: 1}
	_, _ = r.Begin(decisionRule, []inbox.DecisionKey{{ID: id}}, at, 0)
	got, changed := r.Settle(decisionRule, map[string]inbox.Settlement{id: {State: inbox.DecisionCleared, Evidence: defaultEvidence(at)}}, at, inbox.DecisionsVersionsMax)
	if changed || got[id].State != inbox.DecisionPending || got[id].Leaves() {
		t.Fatalf("%v %+v", changed, got[id])
	}
}

// The contract every Decisions keeps, run here on the fake over this
// package's transitions.
func TestTheTransitionsKeepTheContract(t *testing.T) {
	disclosetest.Contract(t, disclosetest.Shared)
}

// Every branch of the transitions, on one block.
func TestEveryTransition(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	a, b, c := inbox.NewMessageID(at), inbox.NewMessageID(at.Add(time.Second)), inbox.NewMessageID(at.Add(2*time.Second))
	r := &inbox.DecisionRecord{V: 1}
	got, changed := r.Begin(decisionRule, []inbox.DecisionKey{{ID: a}, {ID: a}, {ID: b}}, at, 0)
	if !changed || len(r.Entries) != 2 || got[a].State != inbox.DecisionBegun {
		t.Fatalf("a repeated id begun twice: %d entries", len(r.Entries))
	}
	if got, changed := r.Begin(decisionRule, []inbox.DecisionKey{{ID: a}}, at, 1); changed || got[a].State != inbox.DecisionPending {
		t.Fatalf("begun again: %v %+v", changed, got[a])
	}
	got, changed = r.Settle(decisionRule, map[string]inbox.Settlement{
		a: {State: inbox.DecisionCleared, Evidence: defaultEvidence(at)}, b: {State: inbox.DecisionBegun}, c: {State: inbox.DecisionAbandoned},
	}, at.Add(time.Second), 1)
	if !changed || got[a].State != inbox.DecisionCleared || len(got[a].Evidence) != 2 || got[b].State != inbox.DecisionPending || got[c].State != inbox.DecisionAbandoned {
		t.Fatalf("%+v", got)
	}
	if got, changed := r.Settle(decisionRule, map[string]inbox.Settlement{b: {State: inbox.DecisionAbandoned}}, at, 2); !changed || got[b].State != inbox.DecisionAbandoned || len(r.Entries) != 1 {
		t.Fatalf("abandoned: %+v, %d entries", got, len(r.Entries))
	}
	if got, changed := r.Settle(decisionRule, map[string]inbox.Settlement{c: {State: inbox.DecisionHeld}}, at, inbox.DecisionsVersionsMax); changed || got[c].State != inbox.DecisionFull {
		t.Fatalf("no version left: %+v", got)
	}
	if got, changed := r.Settle(decisionRule, map[string]inbox.Settlement{c: {State: inbox.DecisionHeld}}, at, 3); !changed || got[c].State != inbox.DecisionHeld || !got[c].Recovered {
		t.Fatalf("held with nothing begun: %+v", got)
	}
	if !inbox.OverdueAt(at, at.Add(inbox.PendingGrace)) || inbox.OverdueAt(at, at.Add(inbox.PendingGrace-time.Nanosecond)) {
		t.Fatal("the grace's end")
	}

	full := &inbox.DecisionRecord{V: 1}
	for i := range inbox.DecisionsMax {
		full.Entries = append(full.Entries, inbox.DecisionEntry{Message: inbox.NewMessageID(at.Add(time.Duration(i) * time.Millisecond)), Rule: decisionRule.ID, Version: 1, State: inbox.DecisionHeld, At: at})
	}
	d := inbox.NewMessageID(at.Add(time.Hour))
	if got, changed := full.Settle(decisionRule, map[string]inbox.Settlement{d: {State: inbox.DecisionHeld}}, at, 1); changed || got[d].State != inbox.DecisionFull {
		t.Fatalf("held with no room: %+v", got)
	}
}

// The owner's decision 12: a day opens a new block when its tail has no
// room, up to DecisionBlocksMax; past the last, a new decision is Full
// (held, undecided), and a day recovers as its last block's decisions
// expire (only the tail takes new decisions, R1).
func TestADayOpensBlocksUpToItsBound(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	fill := func(n int, expires time.Time) *inbox.DecisionBlocks {
		d := &inbox.DecisionBlocks{Mailbox: "mbx_a", Day: "20261116"}
		for b := range n {
			r := &inbox.DecisionRecord{V: 1, Mailbox: "mbx_a", Day: "20261116", Block: b, Next: b < n-1}
			for i := range inbox.DecisionsMax {
				r.Entries = append(r.Entries, inbox.DecisionEntry{Message: inbox.NewMessageID(at.Add(time.Duration(b*inbox.DecisionsMax+i) * time.Millisecond)), Rule: decisionRule.ID, Version: 1, State: inbox.DecisionCleared, At: at, Expires: expires})
			}
			d.Blocks, d.Versions = append(d.Blocks, r), append(d.Versions, 20)
		}
		return d
	}
	next := inbox.DecisionKey{ID: inbox.NewMessageID(at.Add(time.Hour)), ExpiresAt: at.Add(48 * time.Hour)}

	d := fill(2, at.Add(24*time.Hour))
	got, changed := d.Begin(decisionRule, []inbox.DecisionKey{next}, at)
	if got[next.ID].State != inbox.DecisionBegun || len(d.Blocks) != 3 || !d.Blocks[1].Next || len(changed) != 2 || changed[0] != 1 || changed[1] != 2 {
		t.Fatalf("a third block: %+v, %d blocks, changed %v", got[next.ID], len(d.Blocks), changed)
	}
	if got, _ := d.Begin(decisionRule, []inbox.DecisionKey{{ID: d.Blocks[0].Entries[7].Message}}, at); got[d.Blocks[0].Entries[7].Message].State != inbox.DecisionCleared {
		t.Fatal("a decision of the first block not found")
	}

	d = fill(inbox.DecisionBlocksMax, at.Add(24*time.Hour))
	got, changed = d.Begin(decisionRule, []inbox.DecisionKey{next}, at)
	if got[next.ID].State != inbox.DecisionFull || len(changed) != 0 || len(d.Blocks) != inbox.DecisionBlocksMax {
		t.Fatalf("past the last block: %+v changed %v", got[next.ID], changed)
	}
	got, _ = d.Settle(decisionRule, map[string]inbox.Settlement{next.ID: {State: inbox.DecisionHeld}}, at)
	if got[next.ID].State != inbox.DecisionFull {
		t.Fatalf("held past the last block: %+v", got[next.ID])
	}
	// Once its decisions expired, a begin's own write drops them: in the
	// tail, the only block that takes new decisions.
	got, changed = d.Begin(decisionRule, []inbox.DecisionKey{next}, at.Add(24*time.Hour+inbox.DecisionKeptAfter))
	last := inbox.DecisionBlocksMax - 1
	if got[next.ID].State != inbox.DecisionBegun || len(changed) != 1 || changed[0] != last || len(d.Blocks[last].Entries) != 1 || len(d.Blocks[0].Entries) != inbox.DecisionsMax {
		t.Fatalf("recovered: %+v changed %v", got[next.ID], changed)
	}
}

// N1 (the second design review): a decision is Abandoned only on positive
// evidence that no answer of the model was received for it; an answer
// received and not confirmed kept leaves it Pending, recovered Held later.
func TestReview2N1OnlyPositiveEvidenceAbandons(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	atoms := []query.Atom{codeAtom, resetAtom}
	unknown := query.Result{Value: query.Unknown, Model: atoms}
	cases := []struct {
		name string
		r    query.Result
		a    *inbox.Asked
		err  error
		want inbox.DecisionState
		ev   int
	}{
		{"applies", query.Result{Value: query.True}, &inbox.Asked{Evidence: map[query.Atom]inbox.Evidence{codeAtom: answer(0.9, at)}}, nil, inbox.DecisionHeld, 1},
		{"does not apply", query.Result{Value: query.False}, &inbox.Asked{Evidence: map[query.Atom]inbox.Evidence{codeAtom: answer(0.05, at), resetAtom: answer(0.05, at)}}, nil, inbox.DecisionCleared, 2},
		{"in doubt", query.Result{Value: query.Unknown}, &inbox.Asked{Evidence: map[query.Atom]inbox.Evidence{codeAtom: answer(0.5, at)}}, nil, inbox.DecisionHeld, 1},
		{"a kept refusal", query.Result{Value: query.Unknown, Model: []query.Atom{codeAtom}}, &inbox.Asked{Evidence: map[query.Atom]inbox.Evidence{codeAtom: {Refused: true}}}, nil, inbox.DecisionHeld, 1},
		{"the guard held both", unknown, &inbox.Asked{Held: atoms, Outcome: inbox.AskOff, Evidence: map[query.Atom]inbox.Evidence{}}, nil, inbox.DecisionAbandoned, 0},
		{"the call failed", unknown, &inbox.Asked{Called: 2, Evidence: map[query.Atom]inbox.Evidence{}}, nil, inbox.DecisionAbandoned, 0},
		{"the storage failed before asking", unknown, nil, errTest, inbox.DecisionAbandoned, 0},
		{"the review's schedule: kept, then the cleanup read failed", unknown, &inbox.Asked{Called: 1, Evidence: map[query.Atom]inbox.Evidence{}, Unsettled: atoms}, nil, inbox.DecisionPending, 0},
		{"one answer unsettled", unknown, &inbox.Asked{Called: 1, Evidence: map[query.Atom]inbox.Evidence{codeAtom: answer(0.05, at)}, Unsettled: []query.Atom{resetAtom}}, nil, inbox.DecisionHeld, 1},
	}
	for _, c := range cases {
		model := c.r.Model
		if c.name == "one answer unsettled" {
			model = []query.Atom{resetAtom}
		}
		got := inbox.SettleAfterAsk(query.Result{Value: c.r.Value, Model: model}, atoms, c.a, c.err)
		if got.State != c.want || len(got.Evidence) != c.ev {
			t.Errorf("%s: %+v, want %s with %d", c.name, got, c.want, c.ev)
		}
	}
}

type testError string

func (e testError) Error() string { return string(e) }

const errTest = testError("synthetic storage failure")

// An outcome names its message, whichever it is.
func TestAnOutcomesMessage(t *testing.T) {
	if (inbox.Outcome{Disclosed: &inbox.Disclosed{ID: "m1"}}).ID() != "m1" || (inbox.Outcome{Withheld: &inbox.Withheld{ID: "m2"}}).ID() != "m2" || (inbox.Outcome{}).ID() != "" {
		t.Fatal("ID")
	}
}
