package inbox_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/disclosetest"
	"github.com/permaily-org/permaily/internal/rules"
)

var decisionRule = rules.Ref{ID: "default-sign-in-and-security", Version: 1}

// A decision's record is named by the day in the message's identifier
// (ADR 0031, "Holds that cannot be undone").
func TestADecisionsRecordIsTheMessagesDay(t *testing.T) {
	at := time.Date(2026, 11, 16, 23, 59, 59, 0, time.UTC)
	name, err := inbox.RuleDecisionsName("mbx_a", inbox.NewMessageID(at))
	if err != nil || name != "rule-decisions-mbx_a-20261116" {
		t.Fatalf("%q %v", name, err)
	}
	if _, err := inbox.RuleDecisionsName("mbx_a", "not-an-id"); err == nil {
		t.Fatal("a name for no message")
	}
}

// The bounds of one record (finding 11): a decision past the count, the
// bytes or the versions is Full and nothing is stored for it; expired
// decisions are dropped from the next version written.
func TestADecisionsRecordIsBounded(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	r := &inbox.DecisionRecord{V: 1, Mailbox: "mbx_a", Day: "20261116"}
	var keys []inbox.DecisionKey
	for i := range inbox.DecisionsMax + 1 {
		keys = append(keys, inbox.DecisionKey{ID: inbox.NewMessageID(at.Add(time.Duration(i) * time.Millisecond)), ExpiresAt: at.Add(time.Hour)})
	}
	got, changed := r.Begin(decisionRule, keys, at, 0)
	if !changed || got[keys[0].ID].State != inbox.DecisionBegun || got[keys[inbox.DecisionsMax].ID].State != inbox.DecisionFull || len(r.Entries) != inbox.DecisionsMax {
		t.Fatalf("changed %v, %d entries, last %+v", changed, len(r.Entries), got[keys[inbox.DecisionsMax].ID])
	}
	b, _ := json.Marshal(r)
	if len(b) > inbox.DecisionsBytesMax {
		t.Fatalf("%d bytes", len(b))
	}

	full := &inbox.DecisionRecord{V: 1}
	if got, changed := full.Begin(decisionRule, keys[:1], at, inbox.DecisionsVersionsMax); changed || got[keys[0].ID].State != inbox.DecisionFull {
		t.Fatalf("past its versions: %v %+v", changed, got)
	}

	heavy := &inbox.DecisionRecord{V: 1}
	long := rules.Ref{ID: strings.Repeat("r", 200), Version: 1}
	got, _ = heavy.Begin(long, keys[:inbox.DecisionsMax], at, 0)
	b, _ = json.Marshal(heavy)
	if len(b) > inbox.DecisionsBytesMax || got[keys[inbox.DecisionsMax-1].ID].State != inbox.DecisionFull {
		t.Fatalf("bytes %d, last %+v", len(b), got[keys[inbox.DecisionsMax-1].ID])
	}

	later := at.Add(2 * time.Hour)
	fresh := inbox.DecisionKey{ID: inbox.NewMessageID(later), ExpiresAt: later.Add(time.Hour)}
	if got, changed := r.Begin(decisionRule, []inbox.DecisionKey{fresh}, later, 1); !changed || got[fresh.ID].State != inbox.DecisionBegun || len(r.Entries) != 1 {
		t.Fatalf("expired kept: %d entries", len(r.Entries))
	}
}

// Settling where the record has no version left changes nothing: a pending
// decision stays pending (held, undecided), never cleared.
func TestSettlingPastTheVersionsChangesNothing(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	id := inbox.NewMessageID(at)
	r := &inbox.DecisionRecord{V: 1}
	_, _ = r.Begin(decisionRule, []inbox.DecisionKey{{ID: id}}, at, 0)
	got, changed := r.Settle(decisionRule, map[string]inbox.DecisionState{id: inbox.DecisionCleared}, at, inbox.DecisionsVersionsMax)
	if changed || got[id].State != inbox.DecisionPending || got[id].Leaves() {
		t.Fatalf("%v %+v", changed, got[id])
	}
}

// The contract every Decisions keeps, run here on the fake over this
// package's transitions.
func TestTheTransitionsKeepTheContract(t *testing.T) {
	disclosetest.Contract(t, func(now func() time.Time) inbox.Decisions { return disclosetest.NewDecisions(now) })
}

// Every branch of the transitions, on one record.
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
	got, changed = r.Settle(decisionRule, map[string]inbox.DecisionState{
		a: inbox.DecisionCleared, b: inbox.DecisionBegun, c: inbox.DecisionAbandoned,
	}, at.Add(time.Second), 1)
	if !changed || got[a].State != inbox.DecisionCleared || got[b].State != inbox.DecisionPending || got[c].State != inbox.DecisionAbandoned {
		t.Fatalf("%+v", got)
	}
	if got, changed := r.Settle(decisionRule, map[string]inbox.DecisionState{b: inbox.DecisionAbandoned}, at, 2); !changed || got[b].State != inbox.DecisionAbandoned || len(r.Entries) != 1 {
		t.Fatalf("abandoned: %+v, %d entries", got, len(r.Entries))
	}
	if got, changed := r.Settle(decisionRule, map[string]inbox.DecisionState{c: inbox.DecisionHeld}, at, inbox.DecisionsVersionsMax); changed || got[c].State != inbox.DecisionFull {
		t.Fatalf("no version left: %+v", got)
	}
	if !inbox.OverdueAt(at, at.Add(inbox.PendingGrace)) || inbox.OverdueAt(at, at.Add(inbox.PendingGrace-time.Nanosecond)) {
		t.Fatal("the grace's end")
	}

	full := &inbox.DecisionRecord{V: 1}
	var keys []inbox.DecisionKey
	for i := range inbox.DecisionsMax {
		keys = append(keys, inbox.DecisionKey{ID: inbox.NewMessageID(at.Add(time.Duration(i) * time.Millisecond))})
	}
	full.Begin(decisionRule, keys, at, 0)
	if got, changed := full.Settle(decisionRule, map[string]inbox.DecisionState{c: inbox.DecisionHeld}, at, 1); changed || got[c].State != inbox.DecisionFull {
		t.Fatalf("held with no room: %+v", got)
	}
}

// An outcome names its message, whichever it is.
func TestAnOutcomesMessage(t *testing.T) {
	if (inbox.Outcome{Disclosed: &inbox.Disclosed{ID: "m1"}}).ID() != "m1" || (inbox.Outcome{Withheld: &inbox.Withheld{ID: "m2"}}).ID() != "m2" || (inbox.Outcome{}).ID() != "" {
		t.Fatal("ID")
	}
}
