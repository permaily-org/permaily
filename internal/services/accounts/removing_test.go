package accounts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
)

// ADR 0037, R3 and L16: Removing answers live or removed from the
// account's record alone, and an error for anything else, never taken for
// either: a live account and a closing one before its day are live; one
// whose day came, one whose record says it is being removed and one
// removed by its terminal commit are removed; a record never made, a
// record lost and a storage that fails are errors.
func TestRemovingAnswersFromTheRecord(t *testing.T) {
	ctx := context.Background()
	c := newClosing(t)
	ana, eve := c.ana.Account.ID, c.eve.Account.ID
	for name, want := range map[string]bool{ana: false, eve: false} {
		if removed, err := c.svc.Removing(ctx, name); err != nil || removed != want {
			t.Fatalf("%s: %v %v", name, removed, err)
		}
	}
	c.now = c.day
	if removed, err := c.svc.Removing(ctx, ana); err != nil || !removed {
		t.Fatalf("its day come: %v %v", removed, err)
	}
	// Its record says it is being removed, the removal stopped there.
	c.now = c.day.Add(-time.Hour)
	doc, at, err := c.svc.readAt(ctx, ana)
	if err != nil {
		t.Fatal(err)
	}
	doc.Removing = true
	if err := c.svc.putAccountAt(ctx, doc, at); err != nil {
		t.Fatal(err)
	}
	if removed, err := c.svc.Removing(ctx, ana); err != nil || !removed {
		t.Fatalf("being removed: %v %v", removed, err)
	}
	c.now = c.day
	if err := c.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if removed, err := c.svc.Removing(ctx, ana); err != nil || !removed {
		t.Fatalf("removed: %v %v", removed, err)
	}
	if _, err := c.svc.Removing(ctx, "acc_zzzzzzzzzzzzzzzzzzzzzzzzzz"); err == nil {
		t.Fatal("an account never made is removed or live")
	}
	// Eve's record's value deleted: lost, an error.
	for _, name := range c.mem.RecordNames() {
		if len(name) > len(recordName(eve)) && name[:len(recordName(eve))+1] == recordName(eve)+"@" {
			if err := c.mem.DeleteRecord(ctx, name); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := c.svc.Removing(ctx, eve); !errors.Is(err, store.ErrLost) {
		t.Fatalf("a lost record: %v", err)
	}
}

// The accounts service is what the chain's audit asks (ADR 0037).
var _ receipts.Accounts = (*Service)(nil)
