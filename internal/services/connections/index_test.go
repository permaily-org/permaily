package connections

import (
	"context"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Ruling 8 of the third rulings on pull request 136: the signed index of
// accounts Sweep walks is written again whenever the account's list is
// written, so an entry that was lost (its first write failed, or whoever
// writes the table deleted it) comes back; it stays discovery only.

// V35: an index entry lost after the list was made comes back with the
// list's next write, through any enclave, and Sweep then ends the account's
// connection past its end.
func TestV35ALostIndexEntryComesBack(t *testing.T) {
	ctx := context.Background()
	for _, through := range []string{"a revocation", "an approval"} {
		t.Run(through, func(t *testing.T) {
			w := newWorld(t)
			s := w.enclave()
			p := w.person(accountA)
			x := w.ask(otherApp)
			req := w.pushed(s, p, x, "", map[string][]string{"ends_in_days": {"1"}})
			if _, err := w.exchange(s, x, w.allow(s, p, req)); err != nil {
				t.Fatal(err)
			}
			id := w.connectionOf(s, accountA, otherApp).ID
			lost := func() bool {
				e, err := w.mem.GetEntry(ctx, partitionAccounts, accountA)
				return err != nil || e == nil
			}
			if lost() {
				t.Fatal("the list's first write did not index the account")
			}
			_ = w.mem.DeleteEntry(ctx, partitionAccounts, accountA)
			if !lost() {
				t.Fatal("the entry was not deleted")
			}
			b := w.enclave()
			switch through {
			case "a revocation":
				w.connect(b, p, w.ask(claudeCode))
				_ = w.mem.DeleteEntry(ctx, partitionAccounts, accountA)
				if _, err := b.Revoke(ctx, p, w.connectionOf(b, accountA, claudeCode).ID); err != nil {
					t.Fatal(err)
				}
			case "an approval":
				w.connect(b, p, w.ask(chatgpt))
			}
			if lost() {
				t.Fatal("the list was written and the account's index entry did not come back")
			}
			w.clock.add(25 * time.Hour)
			if err := w.enclave().Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			if c, _, _ := s.readConn(ctx, id); c == nil || c.State != contracts.ConnectionEnded || c.EndReason != contracts.EndedByTime {
				t.Fatalf("the sweep did not find the account: %+v", c)
			}
		})
	}
}
