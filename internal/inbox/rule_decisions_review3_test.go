package inbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/store"
)

// H25, R1 of the third design review (ADR 0031, "Changed after the third
// design review"), the review's schedule on the real strict store: two
// enclaves, each a strict Anchored over one Memory and one MemoryCommits,
// apply the transitions and write the blocks they say changed at the
// versions read. Block 0 is full and written Next, and its decisions expire;
// block 1 is the tail. A and B both read the day without M. A writes M,
// asks and commits it Held. B's allocation, read before A's write, must be
// refused, wherever it was going: only the tail takes a new decision, so it
// was going to the record A changed. B reads again and finds M Held; a cold
// reader too, and M is in one block. At 7bf1077 B's write to block 0
// succeeded, and a cold Begin answered Cleared.
func TestReview3R1CompetingAllocationsCannotHideAHold(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	records, commits := store.NewMemory(), store.NewMemoryCommits()
	enclave := func() *store.Anchored {
		return &store.Anchored{Records: records, Commits: commits, Strict: true, OnlyNewest: true, Now: func() time.Time { return at }}
	}
	a, b := enclave(), enclave()
	name := func(i int) string { return fmt.Sprintf("review3-day-%d", i) }
	record := func(i int, r *inbox.DecisionRecord) *store.Record {
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return &store.Record{Name: name(i), Value: data}
	}
	r0 := &inbox.DecisionRecord{V: 1, Mailbox: "mbx_a", Day: "20261116", Next: true}
	for i := range inbox.DecisionsMax {
		r0.Entries = append(r0.Entries, inbox.DecisionEntry{Message: inbox.NewMessageID(at.Add(time.Duration(i) * time.Millisecond)), Rule: decisionRule.ID, Version: 1, State: inbox.DecisionHeld, Recovered: true, At: at, Expires: at.Add(time.Second)})
	}
	r1 := &inbox.DecisionRecord{V: 1, Mailbox: "mbx_a", Day: "20261116", Block: 1}
	for i, r := range []*inbox.DecisionRecord{r0, r1} {
		if err := a.CreateNew(ctx, record(i, r)); err != nil {
			t.Fatal(err)
		}
	}
	read := func(e *store.Anchored) (*inbox.DecisionBlocks, []store.Mark) {
		d := &inbox.DecisionBlocks{Mailbox: "mbx_a", Day: "20261116"}
		var marks []store.Mark
		for i := range 2 {
			v, m, err := e.GetRecordAt(ctx, name(i))
			if err != nil {
				t.Fatal(err)
			}
			var r inbox.DecisionRecord
			if err = json.Unmarshal(v.Value, &r); err != nil {
				t.Fatal(err)
			}
			h, err := commits.History(ctx, name(i))
			if err != nil {
				t.Fatal(err)
			}
			d.Blocks = append(d.Blocks, &r)
			d.Versions = append(d.Versions, h.Head.Version)
			marks = append(marks, m)
		}
		return d, marks
	}
	write := func(e *store.Anchored, d *inbox.DecisionBlocks, marks []store.Mark, changed []int) error {
		for _, i := range changed {
			if err := e.PutRecordAt(ctx, record(i, d.Blocks[i]), marks[i]); err != nil {
				return err
			}
		}
		return nil
	}
	id := inbox.NewMessageID(at.Add(time.Hour))
	keys := []inbox.DecisionKey{{ID: id, ExpiresAt: at.Add(48 * time.Hour)}}
	da, ma := read(a)
	ga, ca := da.Begin(decisionRule, keys, at)
	if ga[id].State != inbox.DecisionBegun || len(ca) != 1 || ca[0] != 1 {
		t.Fatal(ga, ca)
	}
	// B reads the same absence of M once block 0's decisions have expired
	// past any margin, so that block 0 has room.
	later := at.Add(2 * time.Hour)
	db, mb := read(b)
	gb, cb := db.Begin(decisionRule, keys, later)
	if gb[id].State != inbox.DecisionBegun {
		t.Fatal(gb, cb)
	}
	if err := write(a, da, ma, ca); err != nil {
		t.Fatal(err)
	}
	da, ma = read(a)
	high := defaultEvidence(at)
	high[0].Evidence.P = .90
	held, ca := da.Settle(decisionRule, map[string]inbox.Settlement{id: {State: inbox.DecisionHeld, Evidence: high}}, at.Add(2*time.Second))
	if held[id].State != inbox.DecisionHeld {
		t.Fatal(held)
	}
	if err := write(a, da, ma, ca); err != nil {
		t.Fatal(err)
	}
	if err := write(b, db, mb, cb); !errors.Is(err, store.ErrChanged) {
		t.Fatalf("B's allocation, read before A's write, was committed to blocks %v: %v", cb, err)
	}
	db, _ = read(b)
	again, cb := db.Begin(decisionRule, keys, later)
	if again[id].State != inbox.DecisionHeld || len(cb) != 0 {
		t.Fatalf("B read again: %+v %v", again[id], cb)
	}
	clear, _ := db.Settle(decisionRule, map[string]inbox.Settlement{id: {State: inbox.DecisionCleared, Evidence: defaultEvidence(at)}}, later)
	d, _ := read(enclave())
	cold, _ := d.Begin(decisionRule, keys, later)
	if clear[id].Leaves() || cold[id].Leaves() || cold[id].State != inbox.DecisionHeld {
		t.Fatalf("cleared: %+v, cold %+v", clear[id], cold[id])
	}
	in := 0
	for _, r := range d.Blocks {
		for _, e := range r.Entries {
			if e.Message == id {
				in++
			}
		}
	}
	if in != 1 {
		t.Fatalf("M in %d blocks", in)
	}
}

// H27, R3 of the third design review, the review's schedule on the
// transitions: 4,499 begun and abandoned attempts for an unexpired message
// leave block 0 empty at 8,998 versions. The next Begin cannot reserve its
// own write, its settlement and Next there; it writes block 0 Next with the
// version kept for it and begins the message in block 1. At 7bf1077 the
// empty block stopped the day: Full, with 63 blocks unused.
func TestReview3R3AnEmptyVersionFullBlockOpensItsSuccessor(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	d := &inbox.DecisionBlocks{Mailbox: "mbx_a", Day: "20261116"}
	id := inbox.NewMessageID(at)
	keys := []inbox.DecisionKey{{ID: id, ExpiresAt: at.Add(time.Hour)}}
	commit := func(changed []int) {
		for _, i := range changed {
			d.Versions[i]++
		}
	}
	for i := range (inbox.DecisionsVersionsMax - 2) / 2 {
		got, changed := d.Begin(decisionRule, keys, at)
		if got[id].State != inbox.DecisionBegun {
			t.Fatalf("attempt %d: %v", i, got)
		}
		commit(changed)
		_, changed = d.Settle(decisionRule, map[string]inbox.Settlement{id: {State: inbox.DecisionAbandoned}}, at)
		commit(changed)
	}
	if d.Versions[0] != inbox.DecisionsVersionsMax-2 || len(d.Blocks[0].Entries) != 0 {
		t.Fatalf("versions %d, %d entries", d.Versions[0], len(d.Blocks[0].Entries))
	}
	got, changed := d.Begin(decisionRule, keys, at)
	if got[id].State != inbox.DecisionBegun || len(d.Blocks) != 2 || !d.Blocks[0].Next || len(changed) != 2 || changed[0] != 0 || changed[1] != 1 {
		t.Fatalf("%+v, %d blocks, changed %v", got[id], len(d.Blocks), changed)
	}
	commit(changed)
	if d.Versions[0] > inbox.DecisionsVersionsMax {
		t.Fatalf("block 0 past its versions: %d", d.Versions[0])
	}
	// Its settlement goes where it was begun; block 0 is never written
	// again for it.
	if got, changed := d.Settle(decisionRule, map[string]inbox.Settlement{id: {State: inbox.DecisionHeld}}, at); got[id].State != inbox.DecisionHeld || len(changed) != 1 || changed[0] != 1 {
		t.Fatalf("%+v %v", got[id], changed)
	}
}

// H26, R2 of the third design review, the mixed results the review asked
// for: an answer received, beside one missing, is never forgotten. Unknown
// on an answer in doubt, a refusal or any answer received is Held with its
// evidence; an unsettled answer with nothing received leaves it Pending;
// only a question the model answered no part of is Abandoned. At 7bf1077
// the first four were Abandoned.
func TestReview3R2APartialAnswerIsNeverAbandoned(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	atoms := []query.Atom{codeAtom, resetAtom}
	missingReset := query.Result{Value: query.Unknown, Model: []query.Atom{resetAtom}}
	cases := []struct {
		name string
		a    *inbox.Asked
		want inbox.DecisionState
		ev   int
	}{
		{"in doubt, the other absent", &inbox.Asked{Called: 1, Evidence: map[query.Atom]inbox.Evidence{codeAtom: answer(0.5, at)}}, inbox.DecisionHeld, 1},
		{"a refusal, the other absent", &inbox.Asked{Called: 1, Evidence: map[query.Atom]inbox.Evidence{codeAtom: {Refused: true, Model: "gpt-test", At: at}}}, inbox.DecisionHeld, 1},
		{"a low answer, the other absent", &inbox.Asked{Called: 1, Evidence: map[query.Atom]inbox.Evidence{codeAtom: answer(0.05, at)}}, inbox.DecisionHeld, 1},
		{"kept, the other unsettled", &inbox.Asked{Called: 1, Evidence: map[query.Atom]inbox.Evidence{codeAtom: answer(0.05, at)}, Unsettled: []query.Atom{resetAtom}}, inbox.DecisionHeld, 1},
		{"only unsettled", &inbox.Asked{Called: 1, Evidence: map[query.Atom]inbox.Evidence{}, Unsettled: []query.Atom{resetAtom}}, inbox.DecisionPending, 0},
		{"no part answered", &inbox.Asked{Called: 1, Evidence: map[query.Atom]inbox.Evidence{}}, inbox.DecisionAbandoned, 0},
	}
	for _, c := range cases {
		got := inbox.SettleAfterAsk(missingReset, atoms, c.a, nil)
		if got.State != c.want || len(got.Evidence) != c.ev {
			t.Errorf("%s: %+v, want %s with %d", c.name, got, c.want, c.ev)
		}
	}
}
