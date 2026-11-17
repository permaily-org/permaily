package accounts

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/store"
)

// L16, F2 of the review of #170: Removing reads the account's record with
// its whole history, so a fork at its newest version, or at an earlier
// one, is an error (store.ErrForked), never taken for live or removed,
// whichever value a read of the newest commit alone would choose.
func TestL16RemovingRefusesAForkedRecord(t *testing.T) {
	ctx := context.Background()
	for name, at := range map[string]func(head uint64) uint64{
		"newest":   func(head uint64) uint64 { return head },
		"interior": func(uint64) uint64 { return 1 },
	} {
		t.Run(name, func(t *testing.T) {
			c := newClosing(t)
			id := c.eve.Account.ID
			if _, err := c.svc.UpdateAccount(ctx, c.world.who(c.eveCookie), contracts.AccountUpdate{Name: text("Eve")}); err != nil {
				t.Fatal(err)
			}
			head, _, err := c.commits.Head(ctx, recordName(id))
			if err != nil || head.Version < 2 {
				t.Fatalf("%+v %v", head, err)
			}
			// Both orders of the digests: the value a read of the newest
			// commit chooses is the original, or the other.
			for _, first := range []byte{0, 255} {
				other := sha256.Sum256([]byte("another committed value"))
				other[0] = first
				c.commits.Fork(recordName(id), at(head.Version), other)
			}
			removed, err := c.svc.Removing(ctx, id)
			if !errors.Is(err, store.ErrForked) {
				t.Fatalf("removed=%v, err=%v", removed, err)
			}
		})
	}
}

// Removing over a store that cannot read a record's whole history is an
// error, never an answer.
func TestRemovingNeedsTheWholeHistory(t *testing.T) {
	c := newClosing(t)
	c.svc.cfg.Records = noHistory{c.svc.cfg.Records}
	if _, err := c.svc.Removing(context.Background(), c.eve.Account.ID); !errors.Is(err, errNoHistory) {
		t.Fatal(err)
	}
}

// noHistory is a store of records that does not read histories.
type noHistory struct{ Records }
