package inbox_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/services/mailboxes/mailboxestest"
	"github.com/permaily-org/permaily/internal/store"
)

// sealedStores are two enclaves' sealed DecisionStores (services/mailboxes)
// over independent strict Anchored instances of one table and its commits.
func sealedStores(t *testing.T) func(now func() time.Time) (inbox.DecisionStore, inbox.DecisionStore) {
	return func(now func() time.Time) (inbox.DecisionStore, inbox.DecisionStore) {
		k, err := keys.NewLocal()
		if err != nil {
			t.Fatal(err)
		}
		mem, commits := store.NewMemory(), store.NewMemoryCommits()
		one := func() inbox.DecisionStore {
			s, err := mailboxes.New(mailboxes.Config{Records: &store.Anchored{Records: mem, Commits: commits, Strict: true, OnlyNewest: true, Now: now}, Storage: mem, Keys: k, Accounts: mailboxestest.NewAccounts(), Domain: "inbox.permaily.test", Now: now, Log: slog.New(slog.DiscardHandler)})
			if err != nil {
				t.Fatal(err)
			}
			return s.DecisionStore()
		}
		return one(), one()
	}
}

// H28, one lifetime, against whoever writes the table: a message's item
// expiry is not sealed, so changing it to a minute from now must not make
// its committed hold expire. A held decision is kept for the lifetime the
// message's identifier proves (its arrival plus the longest retention),
// whatever the item says; after a write that would prune an expired
// decision, the original item put back, and the kept answer erased, the
// other enclave still finds it held and asks nothing.
func TestH28AnUntrustedExpiryCannotEraseACommittedHold(t *testing.T) {
	w := newStrictDWorld(t, sealedStores(t))
	w.st.Now = w.clock.now
	ctx := context.Background()
	id := w.seed(qNow.Add(-time.Hour), 1, "Hello", "Here is your number: 482913.")
	original, err := w.st.Item(ctx, w.b.MailboxID, id)
	if err != nil {
		t.Fatal(err)
	}
	// The table's writer changes only the cleartext expiry, no ciphertext.
	forged := *original
	forged.ExpiresAt = w.clock.now().Add(time.Minute)
	if err := w.st.PutItem(ctx, &forged); err != nil {
		t.Fatal(err)
	}
	w.askA.p = byMessage(map[string]float64{id: 0.90})
	first, err := w.discA.Message(on(w.askA), w.b, disclosure(allParts), id)
	if err != nil || first.Withheld == nil || first.Withheld.Why != inbox.HeldDecided {
		t.Fatalf("first hold: %+v %v", first, err)
	}
	w.clock.add(time.Minute + inbox.DecisionKeptAfter + time.Second)
	// A write for another message of the same day prunes what has expired.
	other := w.seed(qNow.Add(-time.Hour), 2, "Lunch", "See you")
	if _, err := w.discB.Message(on(w.askB), w.b, disclosure(allParts), other); err != nil {
		t.Fatal(err)
	}
	if err := w.st.PutItem(ctx, original); err != nil {
		t.Fatal(err)
	}
	w.kept.erase(id)
	w.askB.p = func(string, query.Atom) (float64, bool) { return 0.05, true }
	after, err := w.discB.Message(on(w.askB), w.b, disclosure(allParts), id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Disclosed != nil || w.askB.asked(id) != 0 {
		t.Fatalf("a table-only expiry edit erased the hold: %+v, asked %d", after, w.askB.asked(id))
	}
}

// H28: the lifetime that keeps a decision is the one every disclosure
// enforces. A message past its arrival plus the longest retention is
// neither read nor listed, however far its item's expiry was moved.
func TestH28AMessagePastItsLifetimeIsNotDisclosed(t *testing.T) {
	w := newStrictDWorld(t, nil)
	ctx := context.Background()
	old := w.seed(qNow.Add(-366*24*time.Hour), 1, "Lunch", "See you")
	m, err := w.st.Item(ctx, w.b.MailboxID, old)
	if err != nil {
		t.Fatal(err)
	}
	m.ExpiresAt = qNow.Add(30 * 24 * time.Hour)
	if err := w.st.PutItem(ctx, m); err != nil {
		t.Fatal(err)
	}
	w.askA.p = func(string, query.Atom) (float64, bool) { return 0.05, true }
	if o, err := w.discA.Message(on(w.askA), w.b, disclosure(allParts), old); !errors.Is(err, contracts.ErrNotFound) || o.Disclosed != nil {
		t.Fatalf("read past its lifetime: %+v %v", o, err)
	}
	if p := w.page(on(w.askA), w.discA, disclosure(allParts), "", 10); len(p.Items) != 0 {
		t.Fatalf("listed past its lifetime: %+v", p.Items)
	}
	if _, _, err := w.discA.Attachment(on(w.askA), w.b, disclosure(allParts), old, 0); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatal("an attachment past its lifetime")
	}
	if w.askA.asked(old) != 0 {
		t.Fatal("asked about a message past its lifetime")
	}
}
