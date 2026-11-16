package inbox_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/disclosetest"
)

// pageKeys is n messages of one day from the n-th millisecond after at.
func pageKeys(at time.Time, from, n int) []inbox.DecisionKey {
	var ks []inbox.DecisionKey
	for i := from; i < from+n; i++ {
		ks = append(ks, inbox.DecisionKey{ID: inbox.NewMessageID(at.Add(time.Duration(i) * time.Millisecond)), ExpiresAt: at.Add(24 * time.Hour)})
	}
	return ks
}

// clearBegun settles Cleared, with the default rule's two answers, what got
// answers Begun of keys, and says how many.
func clearBegun(d *inbox.DecisionBlocks, keys []inbox.DecisionKey, got map[string]inbox.Decision, now time.Time) (int, []int) {
	settle := map[string]inbox.Settlement{}
	for _, k := range keys {
		if g := got[k.ID]; g.State == inbox.DecisionBegun {
			settle[k.ID] = inbox.Settlement{State: inbox.DecisionCleared, Evidence: defaultEvidence(now), Attempt: g.Attempt}
		}
	}
	_, changed := d.Settle(decisionRule, settle, now)
	return len(settle), changed
}

// H30, F2 of the fourth design review (ADR 0031, "Changed after the fourth
// design review"), the review's probe as a test of the whole of
// DecisionBlocks: sequential pages of DiscloseMaxLimit distinct messages of
// one day, each settled Cleared with the default rule's two answers before
// the next, every write committed. A message the tail cannot take now is
// held (Full) and not offered again. The day takes at least DecisionsPerDay
// (28,800) settled decisions before it refuses a whole page, every block
// retired holds at least DecisionsMax - DiscloseMaxLimit of them, and every
// block stays within its bytes, the item and its versions. At afcc460 the
// tail was retired as soon as a page's reservations did not fit beside its
// settled decisions: the first block held 397, and the day refused new
// messages at 25,696.
func TestReview4F2SequentialPagesFillADay(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	d := &inbox.DecisionBlocks{Mailbox: "mbx_a", Day: "20261116"}
	commit := func(changed []int) {
		for _, i := range changed {
			d.Versions[i]++
		}
	}
	total := 0
	for from := 0; ; from += inbox.DiscloseMaxLimit {
		if from > 2*inbox.DecisionBlocksMax*inbox.DecisionsMax {
			t.Fatal("the day never refused a page")
		}
		keys := pageKeys(at, from, inbox.DiscloseMaxLimit)
		got, changed := d.Begin(decisionRule, keys, at)
		commit(changed)
		for _, k := range keys {
			if s := got[k.ID].State; s != inbox.DecisionBegun && s != inbox.DecisionFull {
				t.Fatalf("%s: %+v", k.ID[:8], got[k.ID])
			}
		}
		n, changed := clearBegun(d, keys, got, at)
		commit(changed)
		if total += n; n == 0 {
			break
		}
	}
	if total < inbox.DecisionsPerDay || len(d.Blocks) != inbox.DecisionBlocksMax {
		t.Fatalf("%d settled in %d blocks, want at least %d in %d", total, len(d.Blocks), inbox.DecisionsPerDay, inbox.DecisionBlocksMax)
	}
	for i, r := range d.Blocks {
		if i < len(d.Blocks)-1 && (!r.Next || len(r.Entries) < inbox.DecisionsMax-inbox.DiscloseMaxLimit) {
			t.Fatalf("block %d retired with %d settled (next %v)", i, len(r.Entries), r.Next)
		}
		b, err := json.Marshal(r)
		if err != nil || len(b) > inbox.DecisionsBytesMax || len(b) > disclosetest.ItemMax || d.Versions[i] > inbox.DecisionsVersionsMax {
			t.Fatalf("block %d: %d bytes, %d versions, %v", i, len(b), d.Versions[i], err)
		}
		for _, e := range r.Entries {
			if e.State != inbox.DecisionCleared || len(e.Evidence) != 2 {
				t.Fatalf("block %d: %+v", i, e)
			}
		}
	}
	t.Logf("%d settled decisions in %d blocks (at least %d promised)", total, len(d.Blocks), inbox.DecisionsPerDay)
}

// H30: a tail whose room is held only by what its live Pending reserve for
// their settlements is not retired: what it cannot take now is held (Full)
// and its successor is not opened, and once those settle it takes more. Its
// successor opens when the tail is out of count or versions, or when what
// holds its room is settled or overdue (PendingGrace), so a lost settlement
// never keeps the day from going on. At afcc460 the first page that did not
// fit opened the successor.
func TestReview4F2ATailWaitsOnlyForItsLivePending(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	d := &inbox.DecisionBlocks{Mailbox: "mbx_a", Day: "20261116"}
	commit := func(changed []int) {
		for _, i := range changed {
			d.Versions[i]++
		}
	}
	from := 0
	for from < 440 {
		keys := pageKeys(at, from, 10)
		got, changed := d.Begin(decisionRule, keys, at)
		commit(changed)
		_, changed = clearBegun(d, keys, got, at)
		commit(changed)
		from += 10
	}
	if len(d.Blocks) != 1 || len(d.Blocks[0].Entries) != 440 {
		t.Fatalf("%d blocks, %d entries", len(d.Blocks), len(d.Blocks[0].Entries))
	}
	page := pageKeys(at, from, inbox.DiscloseMaxLimit)
	got, changed := d.Begin(decisionRule, page, at)
	commit(changed)
	var begun []inbox.DecisionKey
	var left []inbox.DecisionKey
	for _, k := range page {
		switch got[k.ID].State {
		case inbox.DecisionBegun:
			begun = append(begun, k)
		case inbox.DecisionFull:
			left = append(left, k)
		default:
			t.Fatalf("%+v", got[k.ID])
		}
	}
	if len(begun) == 0 || len(left) == 0 || len(d.Blocks) != 1 || d.Blocks[0].Next {
		t.Fatalf("%d begun, %d left, %d blocks", len(begun), len(left), len(d.Blocks))
	}
	// Asked again while they are pending: still held, still one block.
	again, changed := d.Begin(decisionRule, left, at.Add(time.Second))
	commit(changed)
	if again[left[0].ID].State != inbox.DecisionFull || len(d.Blocks) != 1 || d.Blocks[0].Next {
		t.Fatalf("%+v, %d blocks", again[left[0].ID], len(d.Blocks))
	}
	// Once they settle, the tail takes more.
	_, changed = clearBegun(d, begun, got, at.Add(2*time.Second))
	commit(changed)
	again, changed = d.Begin(decisionRule, left, at.Add(3*time.Second))
	commit(changed)
	var still []inbox.DecisionKey
	for _, k := range left {
		if again[k.ID].State == inbox.DecisionFull {
			still = append(still, k)
		}
	}
	if len(still) == len(left) || len(d.Blocks) != 1 {
		t.Fatalf("%d of %d still held, %d blocks", len(still), len(left), len(d.Blocks))
	}
	if len(still) == 0 {
		t.Fatal("the probe needs some left past the tail")
	}
	// Those begun now are never settled: past the grace, what they reserve
	// no longer holds the tail, and the rest go to its successor.
	late := at.Add(3*time.Second + inbox.PendingGrace)
	got, changed = d.Begin(decisionRule, still, late)
	commit(changed)
	if len(d.Blocks) != 2 || !d.Blocks[0].Next {
		t.Fatalf("%d blocks after the grace", len(d.Blocks))
	}
	for _, k := range still {
		if got[k.ID].State != inbox.DecisionBegun || got[k.ID].Attempt.Block != 1 {
			t.Fatalf("%+v", got[k.ID])
		}
	}
	// The tail out of its count is retired whatever it holds pending.
	full := &inbox.DecisionBlocks{Mailbox: "mbx_a", Day: "20261116"}
	keys := pageKeys(at, 10_000, inbox.DecisionsMax)
	for i := 0; i < inbox.DecisionsMax; i += 5 {
		got, changed := full.Begin(decisionRule, keys[i:i+5], at)
		for _, c := range changed {
			full.Versions[c]++
		}
		if i+5 < inbox.DecisionsMax {
			_, changed = clearBegun(full, keys[i:i+5], got, at)
			for _, c := range changed {
				full.Versions[c]++
			}
		}
	}
	if len(full.Blocks) != 1 || len(full.Blocks[0].Entries) != inbox.DecisionsMax {
		t.Fatalf("%d blocks, %d entries", len(full.Blocks), len(full.Blocks[0].Entries))
	}
	next := pageKeys(at, 20_000, 1)
	if got, _ := full.Begin(decisionRule, next, at); got[next[0].ID].State != inbox.DecisionBegun || len(full.Blocks) != 2 {
		t.Fatalf("a tail out of its count: %+v, %d blocks", got[next[0].ID], len(full.Blocks))
	}
}

// H30: a Held for a message no block holds waits for the tail as a begin
// does: while the tail's live Pending hold its room it is Full (held,
// undecided) and no successor is opened; once they are overdue the tail is
// retired and the Held is stored in its successor.
func TestReview4F2AHeldWithNothingBegunWaitsToo(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	tail := &inbox.DecisionRecord{V: 1, Mailbox: "mbx_a", Day: "20261116"}
	entry := func(i int, s inbox.DecisionState) inbox.DecisionEntry {
		return inbox.DecisionEntry{Message: inbox.NewMessageID(at.Add(time.Duration(i) * time.Millisecond)), Rule: decisionRule.ID, Version: decisionRule.Version, State: s, At: at, Expires: at.Add(24 * time.Hour)}
	}
	for i := range 100 {
		tail.Entries = append(tail.Entries, entry(i, inbox.DecisionPending))
	}
	room := func() int {
		b, err := json.Marshal(tail)
		if err != nil {
			t.Fatal(err)
		}
		return inbox.DecisionsBytesMax - len(b) - 100*inbox.DecisionEvidenceBytesMax
	}
	for i := 100; room() > 300; i++ {
		e := entry(i, inbox.DecisionCleared)
		e.Evidence = make([]inbox.DecisionEvidence, 2)
		for j := range e.Evidence {
			e.Evidence[j] = inbox.DecisionEvidence{Var: "message", Op: "about", Topic: codeAtom.Topic, P: 0.05, Model: "gpt-6-luna-2026-10-01", Prompt: "p2-2026-11-15", At: at}
		}
		tail.Entries = append(tail.Entries, e)
	}
	d := &inbox.DecisionBlocks{Mailbox: "mbx_a", Day: "20261116", Blocks: []*inbox.DecisionRecord{tail}, Versions: []uint64{10}}
	id := inbox.NewMessageID(at.Add(time.Hour))
	held := map[string]inbox.Settlement{id: {State: inbox.DecisionHeld, Evidence: defaultEvidence(at)}}
	got, changed := d.Settle(decisionRule, held, at.Add(time.Second))
	if got[id].State != inbox.DecisionFull || len(changed) != 0 || len(d.Blocks) != 1 || tail.Next {
		t.Fatalf("%+v, changed %v, %d blocks", got[id], changed, len(d.Blocks))
	}
	got, changed = d.Settle(decisionRule, held, at.Add(inbox.PendingGrace))
	if got[id].State != inbox.DecisionHeld || len(d.Blocks) != 2 || !tail.Next || len(changed) != 2 {
		t.Fatalf("after the grace: %+v, changed %v, %d blocks", got[id], changed, len(d.Blocks))
	}
}
