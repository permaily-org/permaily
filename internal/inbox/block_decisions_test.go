package inbox_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/disclosetest"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/store"
)

// flaky is a store of blocks whose write number fail (from 1) fails with
// err, once.
type flaky struct {
	inbox.DecisionStore
	writes, fail int
	err          error
}

func (f *flaky) WriteBlock(ctx context.Context, name string, r *inbox.DecisionRecord, at inbox.DecisionMark) error {
	if f.writes++; f.writes == f.fail {
		return f.err
	}
	return f.DecisionStore.WriteBlock(ctx, name, r, at)
}

// A page that opens the tail's successor writes two blocks. When the tail
// is written and the new block refused (another request made it), the try
// again answers Begun still for what the first write began, so its caller
// asks and settles them, and begins the rest; nothing is begun twice.
func TestATryAgainKeepsWhatItBegan(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	box := inbox.Box{MailboxID: "mbx_a"}
	r := disclosetest.NewRecords()
	d := &inbox.BlockDecisions{Store: r, Now: func() time.Time { return at }}
	var ids []inbox.DecisionKey
	for i := range inbox.DecisionsMax {
		ids = append(ids, inbox.DecisionKey{ID: inbox.NewMessageID(at.Add(time.Duration(i) * time.Millisecond)), ExpiresAt: at.Add(time.Hour)})
	}
	// Fill block 0 to within ten of its count, settled, in pages of ten (ten
	// pending fit beside them), so that the next page is split by the count.
	const small = 10
	for start := 0; start < inbox.DecisionsMax-small; start += small {
		page := ids[start : start+small]
		got, err := d.Begin(ctx, box, decisionRule, page)
		if err != nil {
			t.Fatal(err)
		}
		s := map[string]inbox.Settlement{}
		for _, k := range page {
			if got[k.ID].State != inbox.DecisionBegun {
				t.Fatalf("%+v", got[k.ID])
			}
			s[k.ID] = inbox.Settlement{State: inbox.DecisionCleared, Evidence: defaultEvidence(at), Attempt: got[k.ID].Attempt}
		}
		if _, err := d.Settle(ctx, box, decisionRule, s); err != nil {
			t.Fatal(err)
		}
	}
	var page []inbox.DecisionKey
	for i := range inbox.DiscloseMaxLimit {
		page = append(page, inbox.DecisionKey{ID: inbox.NewMessageID(at.Add(time.Minute + time.Duration(i)*time.Millisecond)), ExpiresAt: at.Add(time.Hour)})
	}
	f := &flaky{DecisionStore: r, fail: 2, err: store.ErrChanged}
	got, err := (&inbox.BlockDecisions{Store: f, Now: func() time.Time { return at }}).Begin(ctx, box, decisionRule, page)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range page {
		if got[k.ID].State != inbox.DecisionBegun {
			t.Fatalf("%+v", got[k.ID])
		}
	}
	name, _ := inbox.RuleDecisionsName(box.MailboxID, page[0].ID, 1)
	if b, _, err := r.ReadBlock(ctx, name); err != nil || b == nil || len(b.Entries) == 0 || len(b.Entries) == inbox.DiscloseMaxLimit {
		t.Fatalf("the page was not split over the two blocks: %v %v", b, err)
	}

	// A write that fails otherwise fails the call, a settlement's too.
	boom := errors.New("synthetic write failure")
	f = &flaky{DecisionStore: r, fail: 1, err: boom}
	late := inbox.DecisionKey{ID: inbox.NewMessageID(at.Add(2 * time.Minute)), ExpiresAt: at.Add(time.Hour)}
	if _, err := (&inbox.BlockDecisions{Store: f, Now: func() time.Time { return at }}).Begin(ctx, box, decisionRule, []inbox.DecisionKey{late}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	f = &flaky{DecisionStore: r, fail: 1, err: boom}
	if _, err := (&inbox.BlockDecisions{Store: f}).Settle(ctx, box, decisionRule, map[string]inbox.Settlement{late.ID: {State: inbox.DecisionHeld}}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	r.Fail(boom)
	if _, err := d.Settle(ctx, box, decisionRule, map[string]inbox.Settlement{late.ID: {State: inbox.DecisionHeld}}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
}

// A decision too large for an empty block is Full, held: the day opens no
// block it cannot use, and the call ends.
func TestADecisionNoBlockCanHold(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	box := inbox.Box{MailboxID: "mbx_a"}
	huge := rules.Ref{ID: strings.Repeat("r", inbox.DecisionsBytesMax), Version: 1}
	d := &inbox.BlockDecisions{Store: disclosetest.NewRecords(), Now: func() time.Time { return at }}
	id := inbox.NewMessageID(at)
	got, err := d.Begin(ctx, box, huge, []inbox.DecisionKey{{ID: id, ExpiresAt: at.Add(time.Hour)}})
	if err != nil || got[id].State != inbox.DecisionFull {
		t.Fatalf("%+v %v", got[id], err)
	}
	got, err = d.Settle(ctx, box, huge, map[string]inbox.Settlement{id: {State: inbox.DecisionHeld}})
	if err != nil || got[id].State != inbox.DecisionFull {
		t.Fatalf("%+v %v", got[id], err)
	}
}

// Each day is its own chain, and one that cannot be read fails the call:
// the caller holds every message of it.
func TestOneChainADay(t *testing.T) {
	at := time.Date(2026, 11, 16, 23, 59, 59, 0, time.UTC)
	d := disclosetest.NewDecisions(func() time.Time { return at })
	box := inbox.Box{MailboxID: "mbx_a"}
	rule := decisionRule
	late, early := inbox.NewMessageID(at), inbox.NewMessageID(at.Add(2*time.Second))
	if _, err := d.Begin(context.Background(), box, rule, []inbox.DecisionKey{{ID: late}, {ID: early}}); err != nil {
		t.Fatal(err)
	}
	if d.Versions(box, late, 0) != 1 || d.Versions(box, early, 0) != 1 || d.Blocks(box, late) != 1 {
		t.Fatalf("versions %d %d", d.Versions(box, late, 0), d.Versions(box, early, 0))
	}
	d.Records.Fail(errors.New("lost"))
	if _, err := d.Begin(context.Background(), box, rule, []inbox.DecisionKey{{ID: late}}); err == nil {
		t.Fatal("a record that cannot be read")
	}
	if _, err := d.Settle(context.Background(), box, rule, map[string]inbox.Settlement{late: {State: inbox.DecisionHeld}}); err == nil {
		t.Fatal("a record that cannot be written")
	}
	d.Records.Fail(nil)
	if _, err := d.Begin(context.Background(), box, rule, []inbox.DecisionKey{{ID: "not-an-id"}}); err == nil {
		t.Fatal("a message with no day")
	}
	if _, err := d.Settle(context.Background(), box, rule, map[string]inbox.Settlement{"not-an-id": {}}); err == nil {
		t.Fatal("a message with no day, settled")
	}
}

// H27, R3 of the third design review, through the fake's real writes:
// 4,499 begun and abandoned attempts spend block 0's versions with nothing
// left in it; the next decision opens block 1 instead of being refused.
func TestReview3R3SpentVersionsOpenTheSuccessorThroughTheFake(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	d := disclosetest.NewDecisions(func() time.Time { return at })
	box := inbox.Box{MailboxID: "mbx_a"}
	rule := decisionRule
	id := inbox.NewMessageID(at)
	keys := []inbox.DecisionKey{{ID: id, ExpiresAt: at.Add(time.Hour)}}
	for i := range (inbox.DecisionsVersionsMax - 2) / 2 {
		begun, err := d.Begin(ctx, box, rule, keys)
		if err != nil || begun[id].State != inbox.DecisionBegun {
			t.Fatalf("attempt %d: %+v %v", i, begun[id], err)
		}
		if got, err := d.Settle(ctx, box, rule, map[string]inbox.Settlement{id: {State: inbox.DecisionAbandoned, Attempt: begun[id].Attempt}}); err != nil || got[id].State != inbox.DecisionAbandoned {
			t.Fatalf("attempt %d: %+v %v", i, got[id], err)
		}
	}
	if v := d.Versions(box, id, 0); v != inbox.DecisionsVersionsMax-2 {
		t.Fatalf("%d versions", v)
	}
	got, err := d.Begin(ctx, box, rule, keys)
	if err != nil || got[id].State != inbox.DecisionBegun || d.Blocks(box, id) != 2 || d.Versions(box, id, 0) != inbox.DecisionsVersionsMax-1 {
		t.Fatalf("%+v %v, %d blocks", got[id], err, d.Blocks(box, id))
	}
	if got, err := d.Settle(ctx, box, rule, map[string]inbox.Settlement{id: {State: inbox.DecisionHeld}}); err != nil || got[id].State != inbox.DecisionHeld {
		t.Fatalf("%+v %v", got[id], err)
	}
}

// H27: a block whose versions are spent before a writable successor never
// stops the day: the successor takes the decision.
func TestReview3R3ASpentBlockBeforeAWritableOne(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	d := disclosetest.NewDecisions(func() time.Time { return at })
	box := inbox.Box{MailboxID: "mbx_a"}
	rule := decisionRule
	first, second := inbox.NewMessageID(at), inbox.NewMessageID(at.Add(time.Second))
	keys := func(id string) []inbox.DecisionKey {
		return []inbox.DecisionKey{{ID: id, ExpiresAt: at.Add(time.Hour)}}
	}
	begun, err := d.Begin(ctx, box, rule, keys(first))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Settle(ctx, box, rule, map[string]inbox.Settlement{first: {State: inbox.DecisionAbandoned, Attempt: begun[first].Attempt}}); err != nil {
		t.Fatal(err)
	}
	name, _ := inbox.RuleDecisionsName(box.MailboxID, first, 0)
	d.Records.Age(name, inbox.DecisionsVersionsMax-1)
	if got, err := d.Begin(ctx, box, rule, keys(first)); err != nil || got[first].State != inbox.DecisionBegun || d.Blocks(box, first) != 2 {
		t.Fatalf("%+v %v", got[first], err)
	}
	if got, err := d.Begin(ctx, box, rule, keys(second)); err != nil || got[second].State != inbox.DecisionBegun || d.Blocks(box, first) != 2 {
		t.Fatalf("%+v %v", got[second], err)
	}
	// A block at its last version and not written Next cannot open a
	// successor: the day is full, held and undecided, never cleared.
	d = disclosetest.NewDecisions(func() time.Time { return at })
	if _, err := d.Begin(ctx, box, rule, keys(first)); err != nil {
		t.Fatal(err)
	}
	d.Records.Age(name, inbox.DecisionsVersionsMax)
	if got, err := d.Begin(ctx, box, rule, keys(second)); err != nil || got[second].State != inbox.DecisionFull || got[second].Leaves() {
		t.Fatalf("%+v %v", got[second], err)
	}
}

// A write refused at every try fails the call: every message of its day is
// held, undecided.
type alwaysChanged struct{ *disclosetest.Records }

func (alwaysChanged) WriteBlock(context.Context, string, *inbox.DecisionRecord, inbox.DecisionMark) error {
	return store.ErrChanged
}

func TestAWriteRefusedAtEveryTry(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	d := &inbox.BlockDecisions{Store: alwaysChanged{disclosetest.NewRecords()}}
	box := inbox.Box{MailboxID: "mbx_a"}
	rule := decisionRule
	id := inbox.NewMessageID(at)
	if _, err := d.Begin(context.Background(), box, rule, []inbox.DecisionKey{{ID: id}}); !errors.Is(err, inbox.ErrDecisionsChanged) {
		t.Fatal(err)
	}
	if _, err := d.Settle(context.Background(), box, rule, map[string]inbox.Settlement{id: {State: inbox.DecisionHeld}}); !errors.Is(err, inbox.ErrDecisionsChanged) {
		t.Fatal(err)
	}
}

// A block whose document names another place is refused, and the day held.
func TestAMisfiledBlockIsRefused(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	r := disclosetest.NewRecords()
	box := inbox.Box{MailboxID: "mbx_a"}
	id := inbox.NewMessageID(at)
	name, _ := inbox.RuleDecisionsName(box.MailboxID, id, 0)
	if err := r.WriteBlock(ctx, name, &inbox.DecisionRecord{V: 1, Mailbox: "mbx_b", Day: "20261116"}, inbox.DecisionMark{}); err != nil {
		t.Fatal(err)
	}
	d := &inbox.BlockDecisions{Store: r, Now: func() time.Time { return at }}
	if _, err := d.Begin(ctx, box, decisionRule, []inbox.DecisionKey{{ID: id}}); err == nil {
		t.Fatal("a misfiled block read")
	}
}
