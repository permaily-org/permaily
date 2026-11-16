package mailboxes_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// H12, a lost record: a block of decisions has no expiry, and a quiet day
// is never written again, so a mailbox whose record is lost has blocks far
// older than any retention. Its removal walks every day from the trusted
// lower bound its disconnect entry carries (its account's list said when
// it was added), when it is disconnected by its person or with its
// account, and a last pass stopped after its record was removed (the
// terminal commit) goes on from the same bound in the next sweep.
func TestH12ALostMailboxRemovesEveryOldDecisionDay(t *testing.T) {
	for _, tc := range []struct {
		name        string
		interrupted bool
		account     bool
	}{
		{"disconnected", false, false},
		{"the account removed", false, true},
		{"disconnected, its last pass stopped", true, false},
		{"the account removed, its last pass stopped", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			e := w.enclave()
			me := caller("acc_me", w.clock)
			m := e.mustCreate(t, me)
			b := inbox.Box{MailboxID: m.ID}
			old := inbox.NewMessageID(w.clock.now())
			settleOne(t, e.Decisions(), b, old, inbox.DecisionHeld, w.clock.now())
			w.clock.add(400 * 24 * time.Hour)
			me = caller(me.Account, w.clock)
			w.lose(m.ID)
			if tc.account {
				if err := e.RemoveAccount(ctx, me.Account); err != nil {
					t.Fatal(err)
				}
			} else if err := e.Disconnect(ctx, me, m.ID); err != nil {
				t.Fatal(err)
			}
			sweepAll(t, e)
			w.clock.add(mailboxes.SweepGrace)
			if tc.interrupted {
				errStop := errors.New("stopped")
				e.records.set(func(op, name string) error {
					if op == "remove" && strings.HasPrefix(name, inbox.RecordRuleDecisions) {
						return errStop
					}
					return nil
				})
				if err := e.Sweep(ctx); !errors.Is(err, errStop) {
					t.Fatalf("the stopped pass: %v", err)
				}
				e.records.set(nil)
				e = w.enclave() // a restart: nothing in memory
			}
			sweepAll(t, e)
			sweepAll(t, e)
			if names := w.blockNames(m.ID); len(names) != 0 {
				t.Fatalf("the removal left %d old sealed block(s)", len(names))
			}
		})
	}
}

// H12, an entry an earlier image wrote, without the bound: the walk starts
// at the mailbox's creation while its record reads, and at the first day
// any block can be of once it is gone.
func TestH12AnEntryWithoutItsBound(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "its record readable", true: "its record lost"}[lost], func(t *testing.T) {
			w := newWorld(t)
			e := w.enclave()
			me := caller("acc_me", w.clock)
			m := e.mustCreate(t, me)
			old := inbox.NewMessageID(w.clock.now())
			settleOne(t, e.Decisions(), inbox.Box{MailboxID: m.ID}, old, inbox.DecisionHeld, w.clock.now())
			w.clock.add(400 * 24 * time.Hour)
			me = caller(me.Account, w.clock)
			if lost {
				w.lose(m.ID)
			}
			if err := e.Disconnect(ctx, me, m.ID); err != nil {
				t.Fatal(err)
			}
			if err := mailboxes.WithoutBounds(ctx, e.Service); err != nil {
				t.Fatal(err)
			}
			sweepAll(t, e)
			w.clock.add(mailboxes.SweepGrace)
			sweepAll(t, e)
			if names := w.blockNames(m.ID); len(names) != 0 {
				t.Fatalf("the removal left %d old sealed block(s)", len(names))
			}
		})
	}
}
