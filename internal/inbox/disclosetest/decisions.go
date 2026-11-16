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
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/rules"
)

// Decisions is an in-memory inbox.Decisions: one record a mailbox and day,
// each with its count of versions, changed under one lock as a strict
// record is changed at the version read.
type Decisions struct {
	Now func() time.Time
	// Fail, when set, is returned by every call instead of reading.
	Fail error

	mu       sync.Mutex
	records  map[string]*inbox.DecisionRecord
	versions map[string]uint64
}

// NewDecisions is an empty Decisions on the clock now.
func NewDecisions(now func() time.Time) *Decisions {
	return &Decisions{Now: now, records: map[string]*inbox.DecisionRecord{}, versions: map[string]uint64{}}
}

func (d *Decisions) record(b inbox.Box, id string) (string, *inbox.DecisionRecord, error) {
	name, err := inbox.RuleDecisionsName(b.MailboxID, id)
	if err != nil {
		return "", nil, err
	}
	r := d.records[name]
	if r == nil {
		day, _ := inbox.DecisionDay(id)
		r = &inbox.DecisionRecord{V: 1, Mailbox: b.MailboxID, Day: day}
		d.records[name] = r
	}
	return name, r, nil
}

// Begin implements inbox.Decisions.
func (d *Decisions) Begin(_ context.Context, b inbox.Box, rule rules.Ref, ids []inbox.DecisionKey) (map[string]inbox.Decision, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.Fail != nil {
		return nil, d.Fail
	}
	byRecord := map[string][]inbox.DecisionKey{}
	for _, k := range ids {
		name, _, err := d.record(b, k.ID)
		if err != nil {
			return nil, err
		}
		byRecord[name] = append(byRecord[name], k)
	}
	out := map[string]inbox.Decision{}
	for name, keys := range byRecord {
		got, changed := d.records[name].Begin(rule, keys, d.Now(), d.versions[name])
		if changed {
			d.versions[name]++
		}
		for id, dec := range got {
			out[id] = dec
		}
	}
	return out, nil
}

// Settle implements inbox.Decisions.
func (d *Decisions) Settle(_ context.Context, b inbox.Box, rule rules.Ref, settle map[string]inbox.DecisionState) (map[string]inbox.Decision, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.Fail != nil {
		return nil, d.Fail
	}
	byRecord := map[string]map[string]inbox.DecisionState{}
	for id, s := range settle {
		name, _, err := d.record(b, id)
		if err != nil {
			return nil, err
		}
		if byRecord[name] == nil {
			byRecord[name] = map[string]inbox.DecisionState{}
		}
		byRecord[name][id] = s
	}
	out := map[string]inbox.Decision{}
	for name, s := range byRecord {
		got, changed := d.records[name].Settle(rule, s, d.Now(), d.versions[name])
		if changed {
			d.versions[name]++
		}
		for id, dec := range got {
			out[id] = dec
		}
	}
	return out, nil
}

// Versions is how many versions the record of id's day has.
func (d *Decisions) Versions(b inbox.Box, id string) uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	name, err := inbox.RuleDecisionsName(b.MailboxID, id)
	if err != nil {
		return 0
	}
	return d.versions[name]
}

// Contract is what every inbox.Decisions does (ADR 0031, "Decisions that
// cannot be undone"); make returns a new, empty one on the clock now.
// services/mailboxes runs it on its strict anchored records, through two
// enclaves.
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
	t.Run("settled, final for ever", func(t *testing.T) {
		now := at
		d := make(func() time.Time { return now })
		_, _ = d.Begin(ctx, box, rule, key(m1, m2))
		got, err := d.Settle(ctx, box, rule, map[string]inbox.DecisionState{m1: inbox.DecisionHeld, m2: inbox.DecisionCleared})
		state(t, got, err, m1, inbox.DecisionHeld)
		state(t, got, err, m2, inbox.DecisionCleared)
		got, err = d.Settle(ctx, box, rule, map[string]inbox.DecisionState{m1: inbox.DecisionCleared, m2: inbox.DecisionHeld})
		state(t, got, err, m1, inbox.DecisionHeld)
		state(t, got, err, m2, inbox.DecisionCleared)
		got, err = d.Begin(ctx, box, rule, key(m1, m2))
		state(t, got, err, m1, inbox.DecisionHeld)
		state(t, got, err, m2, inbox.DecisionCleared)
		if !got[m2].Leaves() || got[m1].Leaves() {
			t.Fatal("only Cleared leaves")
		}
	})
	t.Run("abandoned, begun again", func(t *testing.T) {
		now := at
		d := make(func() time.Time { return now })
		_, _ = d.Begin(ctx, box, rule, key(m1))
		got, err := d.Settle(ctx, box, rule, map[string]inbox.DecisionState{m1: inbox.DecisionAbandoned})
		state(t, got, err, m1, inbox.DecisionAbandoned)
		got, err = d.Begin(ctx, box, rule, key(m1))
		state(t, got, err, m1, inbox.DecisionBegun)
	})
	t.Run("a pending past its grace is settled held, and the late settlement loses", func(t *testing.T) {
		now := at
		d := make(func() time.Time { return now })
		_, _ = d.Begin(ctx, box, rule, key(m1))
		now = at.Add(inbox.PendingGrace)
		got, _ := d.Begin(ctx, box, rule, key(m1))
		if got[m1].State != inbox.DecisionPending || !inbox.OverdueAt(got[m1].At, now) {
			t.Fatalf("%+v", got[m1])
		}
		got, err := d.Settle(ctx, box, rule, map[string]inbox.DecisionState{m1: inbox.DecisionHeld})
		state(t, got, err, m1, inbox.DecisionHeld)
		got, err = d.Settle(ctx, box, rule, map[string]inbox.DecisionState{m1: inbox.DecisionCleared})
		state(t, got, err, m1, inbox.DecisionHeld)
	})
	t.Run("held with nothing begun is stored held; cleared is not", func(t *testing.T) {
		now := at
		d := make(func() time.Time { return now })
		got, err := d.Settle(ctx, box, rule, map[string]inbox.DecisionState{m1: inbox.DecisionCleared, m2: inbox.DecisionHeld})
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
		_, _ = d.Settle(ctx, box, rule, map[string]inbox.DecisionState{m1: inbox.DecisionHeld})
		got, err := d.Begin(ctx, box, rules.Ref{ID: rule.ID, Version: rule.Version + 1}, key(m1))
		state(t, got, err, m1, inbox.DecisionBegun)
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
