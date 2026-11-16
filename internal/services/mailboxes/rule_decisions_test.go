package mailboxes_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/disclosetest"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/services/mailboxes/mailboxestest"
	"github.com/permaily-org/permaily/internal/store"
)

// The rules' decisions kept on the service's sealed strict records (ADR
// 0031, "Holds that cannot be undone"; ADR 0030's pull request 4).

// sealedEnclaves is two services' sealed stores of blocks over one new
// storage and store of commits, each with its own strict Anchored, on the
// clock now.
func sealedEnclaves(t *testing.T) func(now func() time.Time) (inbox.DecisionStore, inbox.DecisionStore) {
	return func(now func() time.Time) (inbox.DecisionStore, inbox.DecisionStore) {
		k, err := keys.NewLocal()
		if err != nil {
			t.Fatal(err)
		}
		mem, commits := store.NewMemory(), store.NewMemoryCommits()
		one := func() inbox.DecisionStore {
			s, err := mailboxes.New(mailboxes.Config{
				Records: &store.Anchored{Records: mem, Commits: commits, OnlyNewest: true, Strict: true, Now: now},
				Storage: mem, Keys: k, Accounts: mailboxestest.NewAccounts(), Domain: domain, Now: now,
				Log: slog.New(slog.DiscardHandler),
			})
			if err != nil {
				t.Fatal(err)
			}
			return s.DecisionStore()
		}
		return one(), one()
	}
}

// H19: disclosetest.Contract on the sealed store, through two enclaves: its
// evidence, blocks and interleaved schedules, H25's, H28's and H29's cases
// included.
func TestH19TheSealedStoreKeepsTheContract(t *testing.T) {
	disclosetest.Contract(t, sealedEnclaves(t))
}

var codeAtom = query.Atom{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: "a sign-in, login or verification code, or a one-time password"}

func evidence(p float64, at time.Time) []inbox.Decided {
	return []inbox.Decided{{Atom: codeAtom, Evidence: inbox.Evidence{P: p, Model: "gpt-test", Prompt: "p-test", At: at, CanaryChecked: "2026-11-11"}}}
}

// settleOne begins and settles a decision of the default rule for id.
func settleOne(t *testing.T, d inbox.Decisions, b inbox.Box, id string, state inbox.DecisionState, at time.Time) {
	t.Helper()
	rule := rules.Default().Ref()
	got, err := d.Begin(ctx, b, rule, []inbox.DecisionKey{{ID: id, ExpiresAt: at.Add(30 * 24 * time.Hour)}})
	if err != nil || got[id].State != inbox.DecisionBegun {
		t.Fatal(got, err)
	}
	s := inbox.Settlement{State: state, Evidence: evidence(0.05, at), Attempt: got[id].Attempt}
	if after, err := d.Settle(ctx, b, rule, map[string]inbox.Settlement{id: s}); err != nil || after[id].State != state {
		t.Fatal(after, err)
	}
}

// A block is sealed and bound to its name: whoever reads the table reads
// nothing of it, and a changed byte, or a block put under another name,
// is refused, never read as one never written.
func TestABlockIsSealedAndBoundToItsName(t *testing.T) {
	w := newWorld(t)
	e := w.enclave()
	b := inbox.Box{MailboxID: "mbx_sealedsealedsealedsealed"}
	id := inbox.NewMessageID(start)
	settleOne(t, e.Decisions(), b, id, inbox.DecisionCleared, start)
	name, _ := inbox.RuleDecisionsName(b.MailboxID, id, 0)
	var stored []string
	for _, n := range w.mem.RecordNames() {
		if strings.HasPrefix(n, name+"@") {
			stored = append(stored, n)
		}
	}
	if len(stored) != 1 {
		t.Fatalf("stored %v", stored)
	}
	r, err := w.mem.GetRecord(ctx, stored[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{b.MailboxID, id, "gpt-test", "cleared", rules.DefaultID} {
		if bytes.Contains(r.Value, []byte(plain)) {
			t.Fatalf("%q is readable in the block", plain)
		}
	}
	// Under another name: refused.
	other, _ := inbox.RuleDecisionsName(b.MailboxID, inbox.NewMessageID(start.Add(-48*time.Hour)), 0)
	ds := e.DecisionStore()
	if err := (&store.Anchored{Records: w.mem, Commits: w.commits, Strict: true, OnlyNewest: true, Now: w.clock.now}).CreateNew(ctx, &store.Record{Name: other, Value: r.Value}); err != nil {
		t.Fatal(err)
	}
	if got, _, err := ds.ReadBlock(ctx, other); err == nil || got != nil {
		t.Fatalf("a block under another name: %+v %v", got, err)
	}
	// A changed byte: the value no longer what was committed, or not
	// opening.
	r.Value[len(r.Value)-1] ^= 1
	w.mem.SetRecord(*r)
	if got, _, err := w.enclave().DecisionStore().ReadBlock(ctx, name); err == nil || got != nil {
		t.Fatalf("a changed block: %+v %v", got, err)
	}
	if _, _, err := ds.ReadBlock(ctx, "mailbox-x"); err == nil {
		t.Fatal("read a record that is not a block")
	}
}

// V40: OpenForConnection refuses a closing account and a mailbox that is
// not the account's, reads a paused mailbox and one of an account that has
// not agreed to the terms in force, refuses a disconnected one, and a
// mailbox made after it under the same account is another identifier, not
// the connection's.
func TestV40OpenForConnection(t *testing.T) {
	w := newWorld(t)
	e := w.enclave()
	me, other := caller("acc_me", w.clock), caller("acc_other", w.clock)
	m := e.mustCreate(t, me)
	theirs := e.mustCreate(t, other)
	if b, err := e.OpenForConnection(ctx, me.Account, m.ID); err != nil || b.MailboxID != m.ID || len(b.Key) == 0 {
		t.Fatal(b, err)
	}
	if _, err := e.OpenForConnection(ctx, me.Account, theirs.ID); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("another account's mailbox: %v", err)
	}
	if _, _, err := e.Pause(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.OpenForConnection(ctx, me.Account, m.ID); err != nil {
		t.Fatalf("a paused mailbox: %v", err)
	}
	w.accounts.Set(me.Account, mailboxes.Standing{TermsAgreed: false})
	if _, err := e.OpenForConnection(ctx, me.Account, m.ID); err != nil {
		t.Fatalf("terms not agreed: %v", err)
	}
	w.accounts.Set(me.Account, mailboxes.Standing{Closing: true, TermsAgreed: true})
	if _, err := e.OpenForConnection(ctx, me.Account, m.ID); !errors.Is(err, contracts.ErrAccountClosing) {
		t.Fatalf("a closing account: %v", err)
	}
	w.accounts.Set(me.Account, mailboxes.Standing{TermsAgreed: true})
	if err := e.Disconnect(ctx, me, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.OpenForConnection(ctx, me.Account, m.ID); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("a disconnected mailbox: %v", err)
	}
	again := e.mustCreate(t, me)
	if again.ID == m.ID {
		t.Fatal("made again under the same identifier")
	}
	if _, err := e.OpenForConnection(ctx, me.Account, m.ID); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("the old identifier: %v", err)
	}
	w.accounts.Block(me.Account)
	if _, err := e.OpenForConnection(ctx, me.Account, again.ID); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("a blocked account: %v", err)
	}
}

// blockNames are the stored names of a mailbox's blocks of decisions.
func (w *world) blockNames(id string) []string {
	var out []string
	for _, n := range w.mem.RecordNames() {
		if strings.HasPrefix(n, inbox.RecordRuleDecisions+id+"-") {
			out = append(out, n)
		}
	}
	return out
}

// H12: disconnecting a mailbox, removing its account, and disconnecting
// one whose record was lost remove its blocks of decisions, every day and
// every block of each, by walking them; none is made again under its
// identifier, and a mailbox made afterwards starts with none.
func TestH12TheDecisionsGoWithTheMailbox(t *testing.T) {
	for _, how := range []string{"disconnected", "the account removed", "its record lost"} {
		t.Run(how, func(t *testing.T) {
			w := newWorld(t)
			e := w.enclave()
			me := caller("acc_me", w.clock)
			m := e.mustCreate(t, me)
			b := inbox.Box{MailboxID: m.ID}
			d := e.Decisions()
			today := inbox.NewMessageID(w.clock.now())
			settleOne(t, d, b, today, inbox.DecisionHeld, w.clock.now())
			w.clock.add(3 * 24 * time.Hour)
			later := inbox.NewMessageID(w.clock.now())
			settleOne(t, d, b, later, inbox.DecisionCleared, w.clock.now())
			// A second block of the later day, opened after the first.
			ds := e.DecisionStore()
			day, _ := inbox.DecisionDay(later)
			for i := range 2 {
				name, _ := inbox.RuleDecisionsName(m.ID, later, i)
				r, at, err := ds.ReadBlock(ctx, name)
				if err != nil {
					t.Fatal(err)
				}
				if r == nil {
					r = &inbox.DecisionRecord{V: 1, Mailbox: m.ID, Day: day, Block: i, Entries: []inbox.DecisionEntry{}}
				}
				r.Next = i == 0
				if err := ds.WriteBlock(ctx, name, r, at); err != nil {
					t.Fatal(err)
				}
			}
			if len(w.blockNames(m.ID)) != 3 {
				t.Fatalf("stored %v", w.blockNames(m.ID))
			}
			me = caller(me.Account, w.clock) // a fresh proof
			switch how {
			case "disconnected":
				if err := e.Disconnect(ctx, me, m.ID); err != nil {
					t.Fatal(err)
				}
			case "the account removed":
				if err := e.RemoveAccount(ctx, me.Account); err != nil {
					t.Fatal(err)
				}
			case "its record lost":
				w.lose(m.ID)
				if err := e.Disconnect(ctx, me, m.ID); err != nil {
					t.Fatal(err)
				}
			}
			sweepAll(t, e)
			if len(w.blockNames(m.ID)) != 3 {
				t.Fatal("removed before the grace")
			}
			w.clock.add(mailboxes.SweepGrace)
			sweepAll(t, e)
			if names := w.blockNames(m.ID); len(names) != 0 {
				t.Fatalf("left %v", names)
			}
			for _, id := range []string{today, later} {
				if _, err := d.Begin(ctx, b, rules.Default().Ref(), []inbox.DecisionKey{{ID: id}}); err == nil {
					t.Fatalf("a removed block was made again for %s", id[:8])
				}
			}
			if how != "the account removed" {
				n := e.mustCreate(t, me)
				if names := w.blockNames(n.ID); len(names) != 0 || n.ID == m.ID {
					t.Fatalf("a new mailbox starts with %v", names)
				}
			}
		})
	}
}

// H30 on the sealed store: sequential pages of DiscloseMaxLimit, each
// settled Cleared before the next, retire a block at its DecisionsMax
// decisions, every block within the item as sealed.
func TestH30TheSealedStoreRetiresBlocksFull(t *testing.T) {
	w := newWorld(t)
	e := w.enclave()
	d := e.Decisions()
	b := inbox.Box{MailboxID: "mbx_fullfullfullfullfullfullfu"}
	rule := rules.Default().Ref()
	at := start
	two := []inbox.Decided{evidence(0.05, at)[0], {Atom: query.Atom{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: "resetting a password or recovering an account"}, Evidence: evidence(0.04, at)[0].Evidence}}
	total := 0
	for page := 0; total < inbox.DecisionsMax+inbox.DiscloseMaxLimit; page++ {
		var keys []inbox.DecisionKey
		for i := range inbox.DiscloseMaxLimit {
			keys = append(keys, inbox.DecisionKey{ID: inbox.NewMessageID(at.Add(time.Duration(page*inbox.DiscloseMaxLimit+i) * time.Millisecond)), ExpiresAt: at.Add(24 * time.Hour)})
		}
		got, err := d.Begin(ctx, b, rule, keys)
		if err != nil {
			t.Fatal(err)
		}
		settle := map[string]inbox.Settlement{}
		for _, k := range keys {
			if g := got[k.ID]; g.State == inbox.DecisionBegun {
				settle[k.ID] = inbox.Settlement{State: inbox.DecisionCleared, Evidence: two, Attempt: g.Attempt}
			}
		}
		if _, err := d.Settle(ctx, b, rule, settle); err != nil {
			t.Fatal(err)
		}
		total += len(settle)
	}
	first := inbox.NewMessageID(at)
	name, _ := inbox.RuleDecisionsName(b.MailboxID, first, 0)
	r, _, err := e.DecisionStore().ReadBlock(ctx, name)
	if err != nil || r == nil || !r.Next || len(r.Entries) != inbox.DecisionsMax {
		t.Fatalf("block 0: %v", err)
	}
	for _, n := range w.blockNames(b.MailboxID) {
		v, err := w.mem.GetRecord(ctx, n)
		if err != nil || len(v.Value) > disclosetest.ItemMax {
			t.Fatalf("%s: %v", n, err)
		}
	}
}
