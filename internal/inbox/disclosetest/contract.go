package disclosetest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/redact"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/store"
)

// pause is a store of blocks that runs between, once, before its first
// write: what another enclave does after this one read and before it
// writes.
type pause struct {
	inbox.DecisionStore
	between func()
}

func (p *pause) WriteBlock(ctx context.Context, name string, r *inbox.DecisionRecord, at inbox.DecisionMark) error {
	if f := p.between; f != nil {
		p.between = nil
		f()
	}
	return p.DecisionStore.WriteBlock(ctx, name, r, at)
}

// before is a store of blocks that runs between, once, just before it
// writes the block named name, and counts the writes refused with
// store.ErrChanged: what another enclave does after this one committed its
// earlier blocks and before it writes that one.
type before struct {
	inbox.DecisionStore
	name    string
	between func()
	refused int
}

func (b *before) WriteBlock(ctx context.Context, name string, r *inbox.DecisionRecord, at inbox.DecisionMark) error {
	if f := b.between; f != nil && name == b.name {
		b.between = nil
		f()
	}
	err := b.DecisionStore.WriteBlock(ctx, name, r, at)
	if errors.Is(err, store.ErrChanged) {
		b.refused++
	}
	return err
}

// Contract is what inbox.BlockDecisions does over every store of blocks
// (ADR 0031, "Decisions, begun before anything is asked", revised after its
// third design review); make returns two enclaves' stores over one new,
// empty storage on the clock now. It runs on Records and on strict anchored
// records here (Shared, Enclaves), and services/mailboxes runs it on its
// sealed ones.
func Contract(t *testing.T, make func(now func() time.Time) (inbox.DecisionStore, inbox.DecisionStore)) {
	t.Helper()
	ctx := context.Background()
	box := inbox.Box{MailboxID: "mbx_contract"}
	rule := rules.Ref{ID: "default-sign-in-and-security", Version: 1}
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	day := at.Format("20060102")
	m1, m2 := inbox.NewMessageID(at), inbox.NewMessageID(at.Add(time.Second))
	key := func(ids ...string) []inbox.DecisionKey {
		var ks []inbox.DecisionKey
		for _, id := range ids {
			ks = append(ks, inbox.DecisionKey{ID: id, ExpiresAt: at.Add(30 * 24 * time.Hour)})
		}
		return ks
	}
	atom := query.Atom{Var: "message", Op: "about", Topic: "a sign-in, login or verification code, or a one-time password"}
	evidence := func(p float64) []inbox.Decided {
		return []inbox.Decided{{Atom: atom, Evidence: inbox.Evidence{P: p, Model: "gpt-test", Prompt: "p-test", At: at, CanaryChecked: "2026-11-15", CanaryChange: "2026-11-01"}}}
	}
	held := func(p float64) inbox.Settlement {
		return inbox.Settlement{State: inbox.DecisionHeld, Evidence: evidence(p)}
	}
	cleared := func(p float64) inbox.Settlement {
		return inbox.Settlement{State: inbox.DecisionCleared, Evidence: evidence(p)}
	}
	// of gives each settlement the attempt Begin answered for its id, as a
	// discloser settles what it began.
	of := func(begun map[string]inbox.Decision, settle map[string]inbox.Settlement) map[string]inbox.Settlement {
		for id, s := range settle {
			s.Attempt = begun[id].Attempt
			settle[id] = s
		}
		return settle
	}
	state := func(t *testing.T, got map[string]inbox.Decision, err error, id string, want inbox.DecisionState) {
		t.Helper()
		if err != nil || got[id].State != want {
			t.Fatalf("%s: %+v %v, want %s", id[:8], got[id], err, want)
		}
	}
	one := func(now func() time.Time) inbox.Decisions {
		s, _ := make(now)
		return &inbox.BlockDecisions{Store: s, Now: now}
	}
	// seed writes block i of the day as r says, through the store.
	seed := func(t *testing.T, s inbox.DecisionStore, i int, r *inbox.DecisionRecord) {
		t.Helper()
		name := blockName(box.MailboxID, day, i)
		_, mark, err := s.ReadBlock(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		r.V, r.Mailbox, r.Day, r.Block = 1, box.MailboxID, day, i
		if err := s.WriteBlock(ctx, name, r, mark); err != nil {
			t.Fatal(err)
		}
	}
	// holding says which blocks of the day hold id.
	holding := func(t *testing.T, s inbox.DecisionStore, id string) []int {
		t.Helper()
		var in []int
		for i := range inbox.DecisionBlocksMax {
			r, _, err := s.ReadBlock(ctx, blockName(box.MailboxID, day, i))
			if err != nil {
				t.Fatal(err)
			}
			if r == nil {
				break
			}
			for _, e := range r.Entries {
				if e.Message == id {
					in = append(in, i)
				}
			}
		}
		return in
	}
	entries := func(n int, state inbox.DecisionState, expires time.Time) []inbox.DecisionEntry {
		var es []inbox.DecisionEntry
		for i := range n {
			es = append(es, inbox.DecisionEntry{Message: inbox.NewMessageID(at.Add(time.Duration(i) * time.Millisecond)), Rule: rule.ID, Version: rule.Version, State: state, At: at, Expires: expires, Recovered: state == inbox.DecisionHeld})
		}
		return es
	}

	t.Run("begun once, then pending to everyone else", func(t *testing.T) {
		now := at
		d := one(func() time.Time { return now })
		got, err := d.Begin(ctx, box, rule, key(m1))
		state(t, got, err, m1, inbox.DecisionBegun)
		now = now.Add(time.Second)
		got, err = d.Begin(ctx, box, rule, key(m1))
		state(t, got, err, m1, inbox.DecisionPending)
		if !got[m1].At.Equal(at) {
			t.Fatalf("pending since %v, want %v", got[m1].At, at)
		}
	})
	t.Run("settled, final for ever, with its evidence", func(t *testing.T) {
		now := at
		d := one(func() time.Time { return now })
		begun, _ := d.Begin(ctx, box, rule, key(m1, m2))
		got, err := d.Settle(ctx, box, rule, of(begun, map[string]inbox.Settlement{m1: held(0.9), m2: cleared(0.05)}))
		state(t, got, err, m1, inbox.DecisionHeld)
		state(t, got, err, m2, inbox.DecisionCleared)
		got, err = d.Settle(ctx, box, rule, map[string]inbox.Settlement{m1: cleared(0.01), m2: held(0.99)})
		state(t, got, err, m1, inbox.DecisionHeld)
		state(t, got, err, m2, inbox.DecisionCleared)
		got, err = d.Begin(ctx, box, rule, key(m1, m2))
		state(t, got, err, m1, inbox.DecisionHeld)
		state(t, got, err, m2, inbox.DecisionCleared)
		if !got[m2].Leaves() || got[m1].Leaves() {
			t.Fatal("only Cleared leaves")
		}
		// N4: what a later request, a vendor's read after a consent
		// preview, the other enclave, is answered names what the decision
		// rested on, as the settlement kept it, whatever the kept answers
		// became since.
		for id, p := range map[string]float64{m1: 0.9, m2: 0.05} {
			ev := got[id].Evidence
			if len(ev) != 1 || ev[0].Atom != atom || ev[0].Evidence.P != p || ev[0].Evidence.Model != "gpt-test" || !ev[0].Evidence.At.Equal(at) || got[id].Recovered {
				t.Fatalf("%s: evidence %+v", id[:8], got[id])
			}
		}
	})
	t.Run("abandoned, begun again", func(t *testing.T) {
		now := at
		d := one(func() time.Time { return now })
		begun, _ := d.Begin(ctx, box, rule, key(m1))
		got, err := d.Settle(ctx, box, rule, of(begun, map[string]inbox.Settlement{m1: {State: inbox.DecisionAbandoned}}))
		state(t, got, err, m1, inbox.DecisionAbandoned)
		got, err = d.Begin(ctx, box, rule, key(m1))
		state(t, got, err, m1, inbox.DecisionBegun)
	})
	t.Run("a pending past its grace is recovered held, and the late settlement loses", func(t *testing.T) {
		now := at
		d := one(func() time.Time { return now })
		_, _ = d.Begin(ctx, box, rule, key(m1))
		now = at.Add(inbox.PendingGrace)
		got, _ := d.Begin(ctx, box, rule, key(m1))
		if got[m1].State != inbox.DecisionPending || !inbox.OverdueAt(got[m1].At, now) {
			t.Fatalf("%+v", got[m1])
		}
		got, err := d.Settle(ctx, box, rule, map[string]inbox.Settlement{m1: {State: inbox.DecisionHeld}})
		state(t, got, err, m1, inbox.DecisionHeld)
		if !got[m1].Recovered || len(got[m1].Evidence) != 0 {
			t.Fatalf("recovered: %+v", got[m1])
		}
		got, err = d.Settle(ctx, box, rule, map[string]inbox.Settlement{m1: cleared(0.05)})
		state(t, got, err, m1, inbox.DecisionHeld)
		if got, _ := d.Begin(ctx, box, rule, key(m1)); !got[m1].Recovered {
			t.Fatalf("recovered, read again: %+v", got[m1])
		}
	})
	t.Run("evidence past its bounds is refused: the decision stays pending", func(t *testing.T) {
		now := at
		d := one(func() time.Time { return now })
		begun, _ := d.Begin(ctx, box, rule, key(m1))
		var many []inbox.Decided
		for i := range inbox.DecisionEvidenceMax + 1 {
			many = append(many, inbox.Decided{Atom: query.Atom{Var: "message", Op: "about", Topic: string(rune('a' + i))}})
		}
		got, err := d.Settle(ctx, box, rule, of(begun, map[string]inbox.Settlement{m1: {State: inbox.DecisionCleared, Evidence: many}, m2: {State: inbox.DecisionHeld, Evidence: many}}))
		state(t, got, err, m1, inbox.DecisionPending)
		state(t, got, err, m2, inbox.DecisionFull)
		if got[m1].Leaves() {
			t.Fatal("cleared without its evidence")
		}
	})
	t.Run("held with nothing begun is stored held; cleared is not", func(t *testing.T) {
		now := at
		d := one(func() time.Time { return now })
		got, err := d.Settle(ctx, box, rule, map[string]inbox.Settlement{m1: cleared(0.05), m2: {State: inbox.DecisionHeld}})
		state(t, got, err, m1, inbox.DecisionAbandoned)
		state(t, got, err, m2, inbox.DecisionHeld)
		got, err = d.Begin(ctx, box, rule, key(m1, m2))
		state(t, got, err, m1, inbox.DecisionBegun)
		state(t, got, err, m2, inbox.DecisionHeld)
	})
	t.Run("another version of the rule decides again", func(t *testing.T) {
		now := at
		d := one(func() time.Time { return now })
		_, _ = d.Begin(ctx, box, rule, key(m1))
		_, _ = d.Settle(ctx, box, rule, map[string]inbox.Settlement{m1: held(0.9)})
		got, err := d.Begin(ctx, box, rules.Ref{ID: rule.ID, Version: rule.Version + 1}, key(m1))
		state(t, got, err, m1, inbox.DecisionBegun)
	})
	t.Run("an expired message is expired, not full, and nothing is stored", func(t *testing.T) {
		now := at.Add(31 * 24 * time.Hour)
		s, _ := make(func() time.Time { return now })
		d := &inbox.BlockDecisions{Store: s, Now: func() time.Time { return now }}
		got, err := d.Begin(ctx, box, rule, key(m1))
		state(t, got, err, m1, inbox.DecisionExpired)
		if got[m1].Leaves() || len(holding(t, s, m1)) != 0 {
			t.Fatal("an expired message decided")
		}
	})
	t.Run("a day past one block opens the next, and finds every decision in either", func(t *testing.T) {
		now := at
		d := one(func() time.Time { return now })
		var ids []string
		for i := range inbox.DecisionsMax + 10 {
			ids = append(ids, inbox.NewMessageID(at.Add(time.Duration(i)*time.Millisecond)))
		}
		for start := 0; start < len(ids); start += inbox.DiscloseMaxLimit {
			page := ids[start:min(start+inbox.DiscloseMaxLimit, len(ids))]
			got, err := d.Begin(ctx, box, rule, key(page...))
			settle := map[string]inbox.Settlement{}
			for _, id := range page {
				state(t, got, err, id, inbox.DecisionBegun)
				settle[id] = cleared(0.05)
			}
			got, err = d.Settle(ctx, box, rule, of(got, settle))
			for _, id := range page {
				state(t, got, err, id, inbox.DecisionCleared)
			}
		}
		got, err := d.Begin(ctx, box, rule, key(ids...))
		for _, id := range ids {
			state(t, got, err, id, inbox.DecisionCleared)
		}
	})
	t.Run("two at once, through two enclaves: one begins", func(t *testing.T) {
		now := at
		clock := func() time.Time { return now }
		sa, sb := make(clock)
		var wg sync.WaitGroup
		var mu sync.Mutex
		begun := 0
		for i := range 8 {
			s := sa
			if i%2 == 1 {
				s = sb
			}
			d := &inbox.BlockDecisions{Store: s, Now: clock}
			wg.Go(func() {
				got, err := d.Begin(ctx, box, rule, key(m1))
				if err != nil {
					t.Error(err)
					return
				}
				if got[m1].State == inbox.DecisionBegun {
					mu.Lock()
					begun++
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		if begun != 1 {
			t.Fatalf("%d began", begun)
		}
	})
	t.Run("H10: two enclaves beginning different messages of a new day: both kept", func(t *testing.T) {
		now := at
		clock := func() time.Time { return now }
		sa, sb := make(clock)
		a := &inbox.BlockDecisions{Store: sa, Now: clock}
		var first map[string]inbox.Decision
		b := &inbox.BlockDecisions{Store: &pause{DecisionStore: sb, between: func() {
			var err error
			first, err = a.Begin(ctx, box, rule, key(m1))
			state(t, first, err, m1, inbox.DecisionBegun)
		}}, Now: clock}
		got, err := b.Begin(ctx, box, rule, key(m2))
		state(t, got, err, m2, inbox.DecisionBegun)
		got, err = a.Begin(ctx, box, rule, key(m1, m2))
		state(t, got, err, m1, inbox.DecisionPending)
		state(t, got, err, m2, inbox.DecisionPending)
	})

	// H25, R1 of the third design review: block 0 is written Next and its
	// decisions have expired, so it has room; block 1 is the tail. B reads
	// the day without M and is about to write; A begins M, asks, and
	// commits it Held. B's write was read before A's: it is refused, and B
	// reads again and finds M Held. M is in one block only, and no later
	// settlement or reader clears it.
	t.Run("H25: one block owns a message: a stale allocation is refused and a hold is never hidden", func(t *testing.T) {
		now := at.Add(2 * time.Hour)
		clock := func() time.Time { return now }
		sa, sb := make(clock)
		seed(t, sa, 0, &inbox.DecisionRecord{Next: true, Entries: entries(inbox.DecisionsMax, inbox.DecisionHeld, at.Add(time.Second))})
		seed(t, sa, 1, &inbox.DecisionRecord{})
		m := inbox.NewMessageID(at.Add(time.Hour))
		a := &inbox.BlockDecisions{Store: sa, Now: clock}
		b := &inbox.BlockDecisions{Store: &pause{DecisionStore: sb, between: func() {
			got, err := a.Begin(ctx, box, rule, key(m))
			state(t, got, err, m, inbox.DecisionBegun)
			got, err = a.Settle(ctx, box, rule, map[string]inbox.Settlement{m: held(0.9)})
			state(t, got, err, m, inbox.DecisionHeld)
		}}, Now: clock}
		got, err := b.Begin(ctx, box, rule, key(m))
		state(t, got, err, m, inbox.DecisionHeld)
		got, err = b.Settle(ctx, box, rule, map[string]inbox.Settlement{m: cleared(0.05)})
		state(t, got, err, m, inbox.DecisionHeld)
		// Another request, through B's enclave, holding nothing of either.
		got, err = (&inbox.BlockDecisions{Store: sb, Now: clock}).Begin(ctx, box, rule, key(m))
		state(t, got, err, m, inbox.DecisionHeld)
		if in := holding(t, sa, m); len(in) != 1 || in[0] != 1 {
			t.Fatalf("held in blocks %v", in)
		}
		// Room freed in an earlier block (its decisions expired or
		// abandoned) is never where a new decision goes: only the tail.
		got, err = b.Begin(ctx, box, rule, key(m2))
		state(t, got, err, m2, inbox.DecisionBegun)
		if in := holding(t, sa, m2); len(in) != 1 || in[0] != 1 {
			t.Fatalf("begun in blocks %v", in)
		}
	})
	t.Run("H25: a held with nothing begun goes to the tail, and loses to nothing", func(t *testing.T) {
		now := at.Add(2 * time.Hour)
		clock := func() time.Time { return now }
		sa, sb := make(clock)
		seed(t, sa, 0, &inbox.DecisionRecord{Next: true, Entries: entries(3, inbox.DecisionCleared, at.Add(time.Second))})
		m := inbox.NewMessageID(at.Add(time.Hour))
		a := &inbox.BlockDecisions{Store: sa, Now: clock}
		b := &inbox.BlockDecisions{Store: &pause{DecisionStore: sb, between: func() {
			got, err := a.Settle(ctx, box, rule, map[string]inbox.Settlement{m: {State: inbox.DecisionHeld}})
			state(t, got, err, m, inbox.DecisionHeld)
		}}, Now: clock}
		got, err := b.Begin(ctx, box, rule, key(m))
		state(t, got, err, m, inbox.DecisionHeld)
		if in := holding(t, sa, m); len(in) != 1 || in[0] != 1 {
			t.Fatalf("held in blocks %v", in)
		}
	})
	// H28: should one message ever be found in two blocks (no write of
	// these transitions makes it), its most protective decision is answered
	// and neither is written.
	t.Run("H28: a message in two blocks answers its most protective decision", func(t *testing.T) {
		now := at.Add(time.Minute)
		clock := func() time.Time { return now }
		s, _ := make(clock)
		e := func(id string, st inbox.DecisionState) inbox.DecisionEntry {
			return inbox.DecisionEntry{Message: id, Rule: rule.ID, Version: rule.Version, State: st, At: at, Expires: at.Add(30 * 24 * time.Hour)}
		}
		seed(t, s, 0, &inbox.DecisionRecord{Next: true, Entries: []inbox.DecisionEntry{e(m1, inbox.DecisionCleared), e(m2, inbox.DecisionCleared)}})
		seed(t, s, 1, &inbox.DecisionRecord{Entries: []inbox.DecisionEntry{e(m1, inbox.DecisionHeld), e(m2, inbox.DecisionPending)}})
		d := &inbox.BlockDecisions{Store: s, Now: clock}
		got, err := d.Begin(ctx, box, rule, key(m1, m2))
		state(t, got, err, m1, inbox.DecisionHeld)
		state(t, got, err, m2, inbox.DecisionPending)
		got, err = d.Settle(ctx, box, rule, map[string]inbox.Settlement{m1: cleared(0.01), m2: cleared(0.01)})
		state(t, got, err, m1, inbox.DecisionHeld)
		state(t, got, err, m2, inbox.DecisionPending)
	})
	// H28: a decision outlives its message by DecisionKeptAfter, so an
	// enclave whose clock is behind never finds the message there and its
	// decision gone.
	t.Run("H28: a decision outlives its message by more than any clock's lag", func(t *testing.T) {
		expires := at.Add(time.Hour)
		now := at
		clock := func() time.Time { return now }
		d := one(clock)
		k := []inbox.DecisionKey{{ID: m1, ExpiresAt: expires}}
		_, _ = d.Begin(ctx, box, rule, k)
		_, _ = d.Settle(ctx, box, rule, map[string]inbox.Settlement{m1: held(0.9)})
		now = expires.Add(inbox.DecisionKeptAfter / 2)
		second, _ := d.Begin(ctx, box, rule, key(m2)) // a write of the same block
		now = expires.Add(-time.Second)               // a clock behind
		got, err := d.Begin(ctx, box, rule, k)
		state(t, got, err, m1, inbox.DecisionHeld)
		now = expires.Add(inbox.DecisionKeptAfter)
		_, _ = d.Settle(ctx, box, rule, of(second, map[string]inbox.Settlement{m2: cleared(0.05)}))
		got, err = d.Begin(ctx, box, rule, k)
		state(t, got, err, m1, inbox.DecisionExpired)
	})

	// H29, F1 of the fourth design review (ADR 0031, "Changed after the
	// fourth design review"), its exact schedule: A begins a page split
	// between blocks 0 and 1 (through Begin and Settle only), M pending in
	// block 0 and N in block 1, and abandons both, the model having answered
	// no part of either. A's write of block 0 commits and drops M; before A
	// writes block 1, B begins M again in the tail, block 1, and receives a
	// protecting 0.90. A's write of block 1 is refused, naturally, and A
	// reads again. What A's block 0 committed is kept (M Abandoned), and
	// only N is settled again; B's Pending, another attempt's, is never
	// dropped, not by that retry, nor by A's whole settlement given again
	// (a retry after a write whose outcome was unknown), nor by a Cleared
	// of A's attempt. B settles M Held, and nothing clears it. At afcc460
	// the retry dropped B's Pending, a later low answer committed Cleared
	// and B's Held lost to it.
	t.Run("H29: a retry keeps what it committed and never drops another attempt's pending", func(t *testing.T) {
		now := at
		clock := func() time.Time { return now }
		sa, sb := make(clock)
		a := &inbox.BlockDecisions{Store: sa, Now: clock}
		b := &inbox.BlockDecisions{Store: sb, Now: clock}
		var page []string
		var begun map[string]inbox.Decision
		for n, size := 0, 20; n < 4*inbox.DecisionsMax; n, size = n+size, inbox.DiscloseMaxLimit {
			page = page[:0]
			for i := range size {
				page = append(page, inbox.NewMessageID(at.Add(time.Duration(n+i)*time.Millisecond)))
			}
			got, err := a.Begin(ctx, box, rule, key(page...))
			if err != nil {
				t.Fatal(err)
			}
			var in []int
			settle := map[string]inbox.Settlement{}
			for _, id := range page {
				if got[id].State == inbox.DecisionBegun {
					in = append(in, holding(t, sa, id)...)
					settle[id] = cleared(0.05)
				}
			}
			if slices.Contains(in, 0) && slices.Contains(in, 1) {
				begun = got
				break
			}
			if _, err := a.Settle(ctx, box, rule, of(got, settle)); err != nil {
				t.Fatal(err)
			}
		}
		if begun == nil {
			t.Fatal("no page was split between blocks 0 and 1")
		}
		var m, n string
		for _, id := range page {
			if begun[id].State != inbox.DecisionBegun {
				continue
			}
			switch in := holding(t, sa, id); {
			case m == "" && slices.Equal(in, []int{0}):
				m = id
			case n == "" && slices.Equal(in, []int{1}):
				n = id
			}
		}
		if m == "" || n == "" {
			t.Fatal("no pending in each block")
		}
		var bBegun map[string]inbox.Decision
		hook := &before{DecisionStore: sa, name: blockName(box.MailboxID, day, 1), between: func() {
			if in := holding(t, sb, m); len(in) != 0 {
				t.Fatalf("A's abandonment of M not committed before block 1: %v", in)
			}
			now = at.Add(time.Second)
			var err error
			bBegun, err = b.Begin(ctx, box, rule, key(m))
			state(t, bBegun, err, m, inbox.DecisionBegun)
		}}
		abandon := of(begun, map[string]inbox.Settlement{m: {State: inbox.DecisionAbandoned}, n: {State: inbox.DecisionAbandoned}})
		retrying := &inbox.BlockDecisions{Store: hook, Now: clock}
		got, err := retrying.Settle(ctx, box, rule, abandon)
		state(t, got, err, m, inbox.DecisionAbandoned)
		state(t, got, err, n, inbox.DecisionAbandoned)
		if hook.refused != 1 {
			t.Fatalf("%d writes refused, want the one natural conflict", hook.refused)
		}
		if in := holding(t, sb, m); !slices.Equal(in, []int{1}) {
			t.Fatalf("B's pending for M is in blocks %v", in)
		}
		// A's whole settlement given again, and a Cleared of A's attempt:
		// neither changes B's Pending.
		got, err = a.Settle(ctx, box, rule, abandon)
		state(t, got, err, m, inbox.DecisionPending)
		state(t, got, err, n, inbox.DecisionAbandoned)
		got, err = a.Settle(ctx, box, rule, of(begun, map[string]inbox.Settlement{m: cleared(0.05)}))
		state(t, got, err, m, inbox.DecisionPending)
		// Another request through A finds B's Pending and begins nothing.
		now = at.Add(2 * time.Second)
		got, err = a.Begin(ctx, box, rule, key(m))
		state(t, got, err, m, inbox.DecisionPending)
		// B settles what it received; a low answer settled afterwards loses.
		got, err = b.Settle(ctx, box, rule, of(bBegun, map[string]inbox.Settlement{m: held(0.9)}))
		state(t, got, err, m, inbox.DecisionHeld)
		got, err = a.Settle(ctx, box, rule, of(bBegun, map[string]inbox.Settlement{m: cleared(0.05)}))
		state(t, got, err, m, inbox.DecisionHeld)
		got, err = (&inbox.BlockDecisions{Store: sb, Now: clock}).Begin(ctx, box, rule, key(m))
		state(t, got, err, m, inbox.DecisionHeld)
		if got[m].Leaves() || len(got[m].Evidence) != 1 || got[m].Evidence[0].Evidence.P != 0.9 {
			t.Fatalf("M: %+v", got[m])
		}
	})
	// ADR 0040, "Its decision": a meaning decision's sentences are kept with
	// it in the blocks and answered with it ever after, through either
	// enclave; a meaning settlement the Pending did not keep room for
	// changes nothing; another split or extraction decides again.
	meaning := rules.MeaningRef(rules.Ref{ID: "rul_contract", Version: 3}, inbox.TextVersion, redact.SplitVersion)
	kept := func(n int) *inbox.MeaningKept {
		k := &inbox.MeaningKept{Map: "0123456789abcdef0123456789abcdef"}
		for i := range n {
			k.Kept = append(k.Kept, i%3 == 1)
		}
		return k
	}
	t.Run("U18: a meaning decision keeps its sentences, for ever, through either enclave", func(t *testing.T) {
		now := at
		clock := func() time.Time { return now }
		sa, sb := make(clock)
		a, b := &inbox.BlockDecisions{Store: sa, Now: clock}, &inbox.BlockDecisions{Store: sb, Now: clock}
		begun, err := a.Begin(ctx, box, meaning, key(m1, m2))
		state(t, begun, err, m1, inbox.DecisionBegun)
		want := kept(redact.MeaningSentencesMax)
		got, err := a.Settle(ctx, box, meaning, of(begun, map[string]inbox.Settlement{
			m1: {State: inbox.DecisionCleared, Evidence: evidence(0.05), Meaning: want},
			m2: {State: inbox.DecisionCleared, Evidence: evidence(0.05)},
		}))
		state(t, got, err, m1, inbox.DecisionCleared)
		for _, d := range []inbox.Decisions{a, b} {
			got, err := d.Begin(ctx, box, meaning, key(m1, m2))
			state(t, got, err, m1, inbox.DecisionCleared)
			if m := got[m1].Meaning; m == nil || m.Map != want.Map || !slices.Equal(m.Kept, want.Kept) {
				t.Fatalf("its sentences read back as %+v", m)
			}
			// Cleared without its sentences keeps none: the whole text goes.
			state(t, got, err, m2, inbox.DecisionCleared)
			if got[m2].Meaning != nil {
				t.Fatalf("sentences for a decision settled without them: %+v", got[m2].Meaning)
			}
		}
		// A later settlement, of other sentences, changes nothing.
		got, err = b.Settle(ctx, box, meaning, map[string]inbox.Settlement{m1: {State: inbox.DecisionCleared, Meaning: kept(3)}})
		state(t, got, err, m1, inbox.DecisionCleared)
		if !slices.Equal(got[m1].Meaning.Kept, want.Kept) {
			t.Fatal("a final meaning decision changed")
		}
		for _, other := range []rules.Ref{
			rules.MeaningRef(rules.Ref{ID: "rul_contract", Version: 3}, inbox.TextVersion+1, redact.SplitVersion),
			rules.MeaningRef(rules.Ref{ID: "rul_contract", Version: 3}, inbox.TextVersion, redact.SplitVersion+1),
			rules.MeaningRef(rules.Ref{ID: "rul_contract", Version: 4}, inbox.TextVersion, redact.SplitVersion),
		} {
			got, err := b.Begin(ctx, box, other, key(m1))
			state(t, got, err, m1, inbox.DecisionBegun)
		}
	})
	t.Run("U18: meaning a pending did not keep room for changes nothing", func(t *testing.T) {
		now := at
		d := one(func() time.Time { return now })
		begun, _ := d.Begin(ctx, box, meaning, key(m1))
		long := kept(1)
		long.Map = strings.Repeat("d", 65)
		for _, bad := range []*inbox.MeaningKept{kept(redact.MeaningSentencesMax + 1), {Kept: []bool{true}}, long} {
			got, err := d.Settle(ctx, box, meaning, of(begun, map[string]inbox.Settlement{m1: {State: inbox.DecisionCleared, Meaning: bad}}))
			state(t, got, err, m1, inbox.DecisionPending)
		}
		// Sentences of a decision that is not a meaning one: refused too.
		begun, _ = d.Begin(ctx, box, rule, key(m2))
		got, err := d.Settle(ctx, box, rule, of(begun, map[string]inbox.Settlement{m2: {State: inbox.DecisionCleared, Evidence: evidence(0.05), Meaning: kept(2)}}))
		state(t, got, err, m2, inbox.DecisionPending)
		// Held keeps no sentences: it removes the whole text.
		got, err = d.Settle(ctx, box, meaning, map[string]inbox.Settlement{m1: {State: inbox.DecisionHeld, Meaning: kept(2)}})
		state(t, got, err, m1, inbox.DecisionHeld)
		if got[m1].Meaning != nil || got[m1].Leaves() {
			t.Fatalf("held with sentences: %+v", got[m1])
		}
	})
	t.Run("U18: a meaning decision's settlement lost is recovered held", func(t *testing.T) {
		now := at
		d := one(func() time.Time { return now })
		_, _ = d.Begin(ctx, box, meaning, key(m1))
		now = at.Add(inbox.PendingGrace)
		got, err := d.Settle(ctx, box, meaning, map[string]inbox.Settlement{m1: {State: inbox.DecisionHeld}})
		state(t, got, err, m1, inbox.DecisionHeld)
		got, err = d.Settle(ctx, box, meaning, map[string]inbox.Settlement{m1: {State: inbox.DecisionCleared, Meaning: kept(2)}})
		state(t, got, err, m1, inbox.DecisionHeld)
		if !got[m1].Recovered || got[m1].Meaning != nil {
			t.Fatalf("%+v", got[m1])
		}
	})
}
