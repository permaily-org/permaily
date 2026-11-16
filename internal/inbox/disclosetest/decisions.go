// Package disclosetest is the shared stand-in for what ADR 0030's pull
// request 4 builds of disclosure, for the pull requests written at the same
// time against its seam (ADR 0030, "Implementation pull requests"): an
// in-memory inbox.Decisions over the record and transitions of
// inbox/rule_decisions.go, and the contract every implementation passes,
// services/mailboxes' on strict anchored records included. Never imported
// outside tests (depguard).
package disclosetest

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
)

// ItemMax is the largest sealed item the fake's store takes, as DynamoDB's
// 400 KiB: a block whose JSON (sealing adds a fixed overhead) is past it is
// a write that fails, so a transition that did not keep its bounds is
// caught here as it would be in the store.
const ItemMax = 400 << 10

// Decisions is an in-memory inbox.Decisions: each mailbox-day a chain of
// blocks (inbox.DecisionBlocks), each with its count of versions, changed
// under one lock as strict records are changed at the version read, and
// each written block checked against ItemMax.
type Decisions struct {
	Now func() time.Time
	// Fail, when set, is returned by every call instead of reading.
	Fail error

	mu   sync.Mutex
	days map[string]*inbox.DecisionBlocks
}

// NewDecisions is an empty Decisions on the clock now.
func NewDecisions(now func() time.Time) *Decisions {
	return &Decisions{Now: now, days: map[string]*inbox.DecisionBlocks{}}
}

// errTooLarge is a block past ItemMax.
var errTooLarge = errors.New("disclosetest: a block is larger than an item")

func (d *Decisions) day(b inbox.Box, id string) (string, error) {
	day, err := inbox.DecisionDay(id)
	if err != nil {
		return "", err
	}
	key := b.MailboxID + "-" + day
	if d.days[key] == nil {
		d.days[key] = &inbox.DecisionBlocks{Mailbox: b.MailboxID, Day: day}
	}
	return key, nil
}

// snapshot and restore keep a day as it was when a write fails.
func snapshot(day *inbox.DecisionBlocks) []byte {
	b, _ := json.Marshal(day)
	return b
}

func restore(day *inbox.DecisionBlocks, b []byte) {
	*day = inbox.DecisionBlocks{}
	_ = json.Unmarshal(b, day)
}

// commit counts a version of every changed block, or fails the call, the
// day restored, when one is past ItemMax.
func commit(day *inbox.DecisionBlocks, before []byte, changed []int) error {
	for _, i := range changed {
		b, err := json.Marshal(day.Blocks[i])
		if err != nil || len(b) > ItemMax {
			restore(day, before)
			return errTooLarge
		}
	}
	for _, i := range changed {
		day.Versions[i]++
	}
	return nil
}

// Begin implements inbox.Decisions.
func (d *Decisions) Begin(_ context.Context, b inbox.Box, rule rules.Ref, ids []inbox.DecisionKey) (map[string]inbox.Decision, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.Fail != nil {
		return nil, d.Fail
	}
	byDay := map[string][]inbox.DecisionKey{}
	for _, k := range ids {
		key, err := d.day(b, k.ID)
		if err != nil {
			return nil, err
		}
		byDay[key] = append(byDay[key], k)
	}
	out := map[string]inbox.Decision{}
	for key, keys := range byDay {
		day := d.days[key]
		before := snapshot(day)
		got, changed := day.Begin(rule, keys, d.Now())
		if err := commit(day, before, changed); err != nil {
			return nil, err
		}
		for id, dec := range got {
			out[id] = dec
		}
	}
	return out, nil
}

// Settle implements inbox.Decisions.
func (d *Decisions) Settle(_ context.Context, b inbox.Box, rule rules.Ref, settle map[string]inbox.Settlement) (map[string]inbox.Decision, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.Fail != nil {
		return nil, d.Fail
	}
	byDay := map[string]map[string]inbox.Settlement{}
	for id, s := range settle {
		key, err := d.day(b, id)
		if err != nil {
			return nil, err
		}
		if byDay[key] == nil {
			byDay[key] = map[string]inbox.Settlement{}
		}
		byDay[key][id] = s
	}
	out := map[string]inbox.Decision{}
	for key, s := range byDay {
		day := d.days[key]
		before := snapshot(day)
		got, changed := day.Settle(rule, s, d.Now())
		if err := commit(day, before, changed); err != nil {
			return nil, err
		}
		for id, dec := range got {
			out[id] = dec
		}
	}
	return out, nil
}

// Blocks is how many blocks the day of id has, and Versions how many
// versions its block has.
func (d *Decisions) Blocks(b inbox.Box, id string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	key, err := d.day(b, id)
	if err != nil {
		return 0
	}
	return len(d.days[key].Blocks)
}

// Versions is how many versions block of the day of id has.
func (d *Decisions) Versions(b inbox.Box, id string, block int) uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	key, err := d.day(b, id)
	if err != nil || block >= len(d.days[key].Versions) {
		return 0
	}
	return d.days[key].Versions[block]
}

// Contract is what every inbox.Decisions does (ADR 0031, "Decisions, begun
// before anything is asked", revised after its second design review); make
// returns a new, empty one on the clock now. services/mailboxes runs it on
// its strict anchored records, through two enclaves.
func Contract(t *testing.T, make func(now func() time.Time) inbox.Decisions) {
	t.Helper()
	ctx := context.Background()
	box := inbox.Box{MailboxID: "mbx_contract"}
	rule := rules.Ref{ID: "default-sign-in-and-security", Version: 1}
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
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
	state := func(t *testing.T, got map[string]inbox.Decision, err error, id string, want inbox.DecisionState) {
		t.Helper()
		if err != nil || got[id].State != want {
			t.Fatalf("%s: %+v %v, want %s", id[:8], got[id], err, want)
		}
	}

	t.Run("begun once, then pending to everyone else", func(t *testing.T) {
		now := at
		d := make(func() time.Time { return now })
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
		d := make(func() time.Time { return now })
		_, _ = d.Begin(ctx, box, rule, key(m1, m2))
		got, err := d.Settle(ctx, box, rule, map[string]inbox.Settlement{m1: held(0.9), m2: cleared(0.05)})
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
		d := make(func() time.Time { return now })
		_, _ = d.Begin(ctx, box, rule, key(m1))
		got, err := d.Settle(ctx, box, rule, map[string]inbox.Settlement{m1: {State: inbox.DecisionAbandoned}})
		state(t, got, err, m1, inbox.DecisionAbandoned)
		got, err = d.Begin(ctx, box, rule, key(m1))
		state(t, got, err, m1, inbox.DecisionBegun)
	})
	t.Run("a pending past its grace is recovered held, and the late settlement loses", func(t *testing.T) {
		now := at
		d := make(func() time.Time { return now })
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
		d := make(func() time.Time { return now })
		_, _ = d.Begin(ctx, box, rule, key(m1))
		var many []inbox.Decided
		for i := range inbox.DecisionEvidenceMax + 1 {
			many = append(many, inbox.Decided{Atom: query.Atom{Var: "message", Op: "about", Topic: string(rune('a' + i))}})
		}
		got, err := d.Settle(ctx, box, rule, map[string]inbox.Settlement{m1: {State: inbox.DecisionCleared, Evidence: many}})
		state(t, got, err, m1, inbox.DecisionPending)
		if got[m1].Leaves() {
			t.Fatal("cleared without its evidence")
		}
	})
	t.Run("held with nothing begun is stored held; cleared is not", func(t *testing.T) {
		now := at
		d := make(func() time.Time { return now })
		got, err := d.Settle(ctx, box, rule, map[string]inbox.Settlement{m1: cleared(0.05), m2: {State: inbox.DecisionHeld}})
		state(t, got, err, m1, inbox.DecisionAbandoned)
		state(t, got, err, m2, inbox.DecisionHeld)
		got, err = d.Begin(ctx, box, rule, key(m1, m2))
		state(t, got, err, m1, inbox.DecisionBegun)
		state(t, got, err, m2, inbox.DecisionHeld)
	})
	t.Run("another version of the rule decides again", func(t *testing.T) {
		now := at
		d := make(func() time.Time { return now })
		_, _ = d.Begin(ctx, box, rule, key(m1))
		_, _ = d.Settle(ctx, box, rule, map[string]inbox.Settlement{m1: held(0.9)})
		got, err := d.Begin(ctx, box, rules.Ref{ID: rule.ID, Version: rule.Version + 1}, key(m1))
		state(t, got, err, m1, inbox.DecisionBegun)
	})
	t.Run("a day past one block opens the next, and finds every decision in either", func(t *testing.T) {
		now := at
		d := make(func() time.Time { return now })
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
			got, err = d.Settle(ctx, box, rule, settle)
			for _, id := range page {
				state(t, got, err, id, inbox.DecisionCleared)
			}
		}
		got, err := d.Begin(ctx, box, rule, key(ids...))
		for _, id := range ids {
			state(t, got, err, id, inbox.DecisionCleared)
		}
	})
	t.Run("two at once: one begins", func(t *testing.T) {
		now := at
		d := make(func() time.Time { return now })
		var wg sync.WaitGroup
		var mu sync.Mutex
		begun := 0
		for range 8 {
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
}
