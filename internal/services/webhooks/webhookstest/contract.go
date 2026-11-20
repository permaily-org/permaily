package webhookstest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/webhook"
)

// World is what Contract drives: connections with webhooks in one enclave,
// a clock it moves, and the person's, the vendor's and the operator's
// actions on them. Fake's World is FakeWorld; services/connections gives its
// own (ADR 0039's pull request 2), with real records, accounts, mailboxes,
// the default rule and a played model.
type World interface {
	Connections() webhooks.Connections
	// Make makes an active connection whose webhook is w, for an account of
	// its own, and answers its identifier and account.
	Make(t *testing.T, w contracts.WebhookRequest) (connection, account string)
	Revoke(t *testing.T, connection string)
	RevokeDevices(t *testing.T, connection string)
	Expire(t *testing.T, connection string)
	Close(t *testing.T, connection string)
	Remove(t *testing.T, connection string)
	Off(t *testing.T, connection string)
	ModelOff(t *testing.T, account string)
	ModelOn(t *testing.T, account string)
	// Deliver stores a message in the connection's first mailbox, received
	// at received: one its formulas match and no rule holds (leaves), one
	// its formulas do not match, or one a rule holds (a sign-in code).
	Deliver(t *testing.T, connection string, received time.Time, kind Arrival) (message string)
	// Advance moves the clock.
	Advance(d time.Duration)
	Now() time.Time
}

// Arrival is what kind of message Deliver stores.
type Arrival int

const (
	ArrivalLeaves Arrival = iota + 1
	ArrivalNoMatch
	ArrivalHeld
)

// Contract is what every webhooks.Connections keeps (ADR 0039, tests K10 to
// K14): the webhook as approved; Calls false at once on every way a
// connection stops giving mail, and on none other; an evaluation of new mail
// that finds only what would leave, a held message never; nothing evaluated
// while the model cannot be asked; a watermark that evaluates each message
// once.
func Contract(t *testing.T, newWorld func(t *testing.T) World) {
	ctx := context.Background()
	weekly := &contracts.WebhookSchedule{Every: contracts.EveryWeek, On: "monday", At: "08:00"}
	ask := contracts.WebhookRequest{URL: "https://trip-app.com/hooks/permaily", NewMail: true, Schedule: weekly}
	sub := func(t *testing.T, w World, c string) *webhooks.Subscription {
		t.Helper()
		s, err := w.Connections().Subscription(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	t.Run("as approved", func(t *testing.T) {
		w := newWorld(t)
		c, account := w.Make(t, ask)
		s := sub(t, w, c)
		if !s.Calls || s.Connection != c || s.Account != account || s.URL != ask.URL || !s.NewMail ||
			s.Schedule == nil || *s.Schedule != *weekly || s.State != contracts.WebhookOn || s.Delivery != contracts.DeliveryOn ||
			!strings.HasPrefix(s.ID, webhook.SubscriptionPrefix) || s.ID == c || len(s.Mailboxes) == 0 || s.TimeZone == "" ||
			s.ReadAt.After(w.Now()) || !Fresh(s, w.Now()) {
			t.Fatalf("%+v", s)
		}
		if again := sub(t, w, c); again.ID != s.ID {
			t.Error("the subscription changed")
		}
	})

	stops := map[string]func(t *testing.T, w World, c, account string){
		"revoked":           func(t *testing.T, w World, c, _ string) { w.Revoke(t, c) },
		"every device":      func(t *testing.T, w World, c, _ string) { w.RevokeDevices(t, c) },
		"past its end":      func(t *testing.T, w World, c, _ string) { w.Expire(t, c) },
		"account closing":   func(t *testing.T, w World, c, _ string) { w.Close(t, c) },
		"turned off":        func(t *testing.T, w World, c, _ string) { w.Off(t, c) },
		"model unavailable": func(t *testing.T, w World, _, account string) { w.ModelOff(t, account) },
	}
	for name, stop := range stops {
		t.Run("stops: "+name, func(t *testing.T) {
			w := newWorld(t)
			c, account := w.Make(t, ask)
			stop(t, w, c, account)
			if s := sub(t, w, c); s.Calls {
				t.Fatalf("%s: still calls: %+v", name, s)
			}
		})
	}

	t.Run("removed", func(t *testing.T) {
		w := newWorld(t)
		c, _ := w.Make(t, ask)
		w.Remove(t, c)
		if _, err := w.Connections().Subscription(ctx, c); !errors.Is(err, webhooks.ErrNoWebhook) {
			t.Fatal(err)
		}
		if _, err := w.Connections().Pending(ctx, c, webhooks.Watermark{}); !errors.Is(err, webhooks.ErrNoWebhook) {
			t.Fatal(err)
		}
	})

	t.Run("the vendor stops it", func(t *testing.T) {
		w := newWorld(t)
		c, _ := w.Make(t, ask)
		if err := w.Connections().Stop(ctx, c); err != nil {
			t.Fatal(err)
		}
		if s := sub(t, w, c); s.Calls || s.State != contracts.WebhookStopped {
			t.Fatalf("%+v", s)
		}
	})

	t.Run("new mail: only what leaves", func(t *testing.T) {
		w := newWorld(t)
		c, account := w.Make(t, ask)
		start := w.Now()
		w.Deliver(t, c, start.Add(time.Second), ArrivalHeld)
		w.Deliver(t, c, start.Add(2*time.Second), ArrivalNoMatch)
		e, err := w.Connections().Pending(ctx, c, webhooks.Watermark{Received: start})
		if err != nil {
			t.Fatal(err)
		}
		if e.Leaves || !e.Since.IsZero() {
			t.Fatalf("a held message and one that does not match made a notice: %+v", e)
		}
		if e.Through.Received.Before(start.Add(2 * time.Second)) {
			t.Fatalf("the watermark did not pass what was evaluated: %+v", e.Through)
		}
		leaves := start.Add(3 * time.Second)
		w.Deliver(t, c, leaves, ArrivalLeaves)
		e, err = w.Connections().Pending(ctx, c, e.Through)
		if err != nil || !e.Leaves || !e.Since.Equal(leaves) {
			t.Fatalf("%+v %v", e, err)
		}
		// Evaluated once: the same watermark again finds nothing new.
		again, err := w.Connections().Pending(ctx, c, e.Through)
		if err != nil || again.Leaves {
			t.Fatalf("evaluated twice: %+v %v", again, err)
		}
		// Stored late, received before the watermark but within the lag: found once.
		late := w.Deliver(t, c, leaves.Add(-time.Second), ArrivalLeaves)
		e, err = w.Connections().Pending(ctx, c, again.Through)
		if err != nil || !e.Leaves {
			t.Fatalf("a message stored late: %+v %v", e, err)
		}
		if late == "" {
			t.Fatal("no identifier")
		}
		// The model off: nothing is evaluated and the watermark stays.
		w.ModelOff(t, account)
		w.Deliver(t, c, leaves.Add(time.Minute), ArrivalLeaves)
		if _, err := w.Connections().Pending(ctx, c, e.Through); !errors.Is(err, contracts.ErrModelUnavailable) {
			t.Fatalf("with the model off: %v", err)
		}
		w.ModelOn(t, account)
		if e, err := w.Connections().Pending(ctx, c, e.Through); err != nil || !e.Leaves {
			t.Fatalf("with the model back: %+v %v", e, err)
		}
	})
}

// FakeWorld is Fake's World, for the contract's own test and for pull
// request 3's.
type FakeWorld struct {
	Fake  *Fake
	clock time.Time
	n     int
}

// NewFakeWorld is a FakeWorld whose clock starts at start.
func NewFakeWorld(start time.Time) *FakeWorld {
	w := &FakeWorld{clock: start}
	w.Fake = &Fake{Now: w.Now}
	return w
}

func (w *FakeWorld) Connections() webhooks.Connections { return w.Fake }
func (w *FakeWorld) Now() time.Time                    { return w.clock }
func (w *FakeWorld) Advance(d time.Duration)           { w.clock = w.clock.Add(d) }

func (w *FakeWorld) next(prefix string) string {
	w.n++
	return fmt.Sprintf("%s%026d", prefix, w.n)
}

func (w *FakeWorld) Make(_ *testing.T, r contracts.WebhookRequest) (string, string) {
	c, account := w.next("con_"), w.next("acc_")
	w.Fake.Make(webhooks.Subscription{
		Connection: c, Account: account, ID: w.next(webhook.SubscriptionPrefix), ClientID: "https://trip-app.com/oauth/client.json",
		URL: r.URL, NewMail: r.NewMail, Mailboxes: []string{w.next("mbx_")}, Schedule: r.Schedule, TimeZone: "Europe/Madrid", MadeAt: w.clock,
	})
	return c, account
}

func (w *FakeWorld) Revoke(_ *testing.T, c string)        { w.Fake.Revoke(c) }
func (w *FakeWorld) RevokeDevices(_ *testing.T, c string) { w.Fake.RevokeDevices(c) }
func (w *FakeWorld) Expire(_ *testing.T, c string)        { w.Fake.Expire(c) }
func (w *FakeWorld) Close(_ *testing.T, c string)         { w.Fake.Close(c) }
func (w *FakeWorld) Remove(_ *testing.T, c string)        { w.Fake.Remove(c) }
func (w *FakeWorld) Off(_ *testing.T, c string)           { w.Fake.Off(c) }
func (w *FakeWorld) ModelOff(_ *testing.T, a string)      { w.Fake.ModelOff(a) }
func (w *FakeWorld) ModelOn(_ *testing.T, a string)       { w.Fake.ModelOn(a) }

func (w *FakeWorld) Deliver(_ *testing.T, c string, received time.Time, kind Arrival) string {
	id := w.next("msg_")
	w.Fake.Deliver(c, Message{ID: id, Received: received, Leaves: kind == ArrivalLeaves})
	return id
}
