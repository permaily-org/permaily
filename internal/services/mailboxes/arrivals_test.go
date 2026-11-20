package mailboxes_test

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/store"
)

// arrivals records the hints a delivery gives (ADR 0039's inbox.Arrivals).
type arrivals struct {
	mu   sync.Mutex
	got  []string
	when []time.Time
}

func (a *arrivals) Arrived(mailbox string, received time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, mailbox)
	a.when = append(a.when, received)
}

func (a *arrivals) seen() ([]string, []time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.got...), append([]time.Time(nil), a.when...)
}

func (w *world) enclaveTelling(a inbox.Arrivals) *enclave {
	w.t.Helper()
	records := &hooked{Anchored: &store.Anchored{Records: w.mem, Commits: w.commits, OnlyNewest: true, Strict: true, Now: w.clock.now}}
	s, err := mailboxes.New(mailboxes.Config{
		Records: records, Storage: w.mem, Keys: w.keys, Accounts: w.accounts, Notifier: w.notices,
		Domain: domain, Now: w.clock.now, Arrivals: a,
		Log: slog.New(slog.NewJSONHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return &enclave{Service: s, records: records}
}

// ADR 0039, "New mail": a message stored is told to the webhooks once its
// mark is written, with its mailbox and when it was received, and only
// then: a delivery stopped before the mark tells nothing, a copy delivered
// again tells nothing more.
func TestADeliveryTellsTheWebhooksAfterTheMark(t *testing.T) {
	w := newWorld(t)
	told := &arrivals{}
	a := w.enclaveTelling(told)
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	raw := message("told")
	w.setInterrupt(func(_ context.Context, step store.Step, _, key string) error {
		if step == store.StepEntry && strings.HasPrefix(key, inbox.KeySeen) && w.messagesNow(m.ID) > 0 {
			return errStopped
		}
		return nil
	})
	if err := a.deliver(t, raw, m.Address); err == nil {
		t.Fatal("not stopped")
	}
	if got, _ := told.seen(); len(got) != 0 {
		t.Fatal("told before the mark")
	}
	w.setInterrupt(nil)
	w.clock.add(mailboxes.ClaimStale)
	if err := a.deliver(t, raw, m.Address); err != nil {
		t.Fatal(err)
	}
	if err := a.deliver(t, raw, m.Address); err != nil {
		t.Fatal(err)
	}
	got, when := told.seen()
	msgs := w.messages(m.ID)
	if len(got) != 1 || got[0] != m.ID || len(msgs) != 1 || !when[0].Equal(msgs[0].ReceivedAt) {
		t.Fatalf("told %v at %v", got, when)
	}
	// No Arrivals: nobody is told, and delivery is as before.
	b := w.enclave()
	n := b.mustCreate(t, caller("acc_other", w.clock))
	if err := b.deliver(t, message("untold"), n.Address); err != nil {
		t.Fatal(err)
	}
	if got, _ := told.seen(); len(got) != 1 {
		t.Fatal("told by another enclave's delivery")
	}
}
