package webhookstest

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
// the default rule, the account's rules through ADR 0040's
// accountrules.Source and a played model.
type World interface {
	Connections() webhooks.Connections
	// Make makes an active connection whose webhook is w, reaching two
	// mailboxes, for an account of its own, and answers its identifier and
	// account.
	Make(t *testing.T, w contracts.WebhookRequest) (connection, account string)
	Revoke(t *testing.T, connection string)
	RevokeDevices(t *testing.T, connection string)
	Expire(t *testing.T, connection string)
	Close(t *testing.T, connection string)
	Remove(t *testing.T, connection string)
	Off(t *testing.T, connection string)
	ModelOff(t *testing.T, account string)
	ModelOn(t *testing.T, account string)
	// HoldAll saves, with the person's proof, a rule of the account that
	// holds every message (the rules' version moves); RulesUnreadable makes
	// the account's rules record unreadable, or readable again.
	HoldAll(t *testing.T, account string)
	RulesUnreadable(t *testing.T, account string, unreadable bool)
	// DropMailbox takes the connection's second mailbox out of it (a new
	// version of its record).
	DropMailbox(t *testing.T, connection string)
	// Deliver stores a message in the connection's mailbox number mailbox
	// (0 or 1), received at received: one its formulas match and no rule
	// holds (leaves), one its formulas do not match, or one a rule holds (a
	// sign-in code). It answers the message's position.
	Deliver(t *testing.T, connection string, mailbox int, received time.Time, kind Arrival) webhooks.Cursor
	// Delete deletes a message from its mailbox.
	Delete(t *testing.T, connection string, message webhooks.Cursor)
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
// K14, revised after the design review): the webhook as approved; Calls
// false at once on every way a connection stops giving mail, and on none
// other; a permit cut by every local stop before it returns; an evaluation
// of new mail that finds only what would leave, a held message never,
// whose cursor reaches the last message of any burst with bounded
// reprocessing; nothing evaluated while the model or the rules cannot be
// used; a match that no longer stands once its basis changed or its
// messages are gone; and every webhook listed from the records.
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
	pending := func(t *testing.T, w World, c string, after webhooks.Progress) *webhooks.Evaluation {
		t.Helper()
		e, err := w.Connections().Pending(ctx, c, after)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}

	t.Run("as approved", func(t *testing.T) {
		w := newWorld(t)
		c, account := w.Make(t, ask)
		s := sub(t, w, c)
		if !s.Calls || s.Why != "" || s.Connection != c || s.Account != account || s.URL != ask.URL || !s.NewMail ||
			s.Schedule == nil || *s.Schedule != *weekly || s.State != contracts.WebhookOn || s.Delivery != contracts.DeliveryOn ||
			!strings.HasPrefix(s.ID, webhook.SubscriptionPrefix) || s.ID == c || strings.Contains(s.ID, ".") || len(s.Mailboxes) != 2 || s.TimeZone == "" ||
			s.ReadAt.After(w.Now()) || !Fresh(s, w.Now()) || s.MadeAt.IsZero() {
			t.Fatalf("%+v", s)
		}
		if again := sub(t, w, c); again.ID != s.ID || again.Basis != s.Basis {
			t.Error("the subscription or its basis changed with nothing done")
		}
	})

	stops := map[string]struct {
		do  func(t *testing.T, w World, c, account string)
		why webhooks.Skip
		// cuts says the change cuts a permit already started.
		cuts bool
	}{
		"revoked":           {func(t *testing.T, w World, c, _ string) { w.Revoke(t, c) }, webhooks.SkipEnded, true},
		"every device":      {func(t *testing.T, w World, c, _ string) { w.RevokeDevices(t, c) }, webhooks.SkipNoDevice, true},
		"past its end":      {func(t *testing.T, w World, c, _ string) { w.Expire(t, c) }, webhooks.SkipEnded, false},
		"account closing":   {func(t *testing.T, w World, c, _ string) { w.Close(t, c) }, webhooks.SkipEnded, true},
		"turned off":        {func(t *testing.T, w World, c, _ string) { w.Off(t, c) }, webhooks.SkipOff, true},
		"model unavailable": {func(t *testing.T, w World, _, account string) { w.ModelOff(t, account) }, webhooks.SkipModel, false},
		"rules unreadable":  {func(t *testing.T, w World, _, account string) { w.RulesUnreadable(t, account, true) }, webhooks.SkipRules, false},
	}
	for name, stop := range stops {
		t.Run("stops: "+name, func(t *testing.T) {
			w := newWorld(t)
			c, account := w.Make(t, ask)
			_, p, err := w.Connections().Admit(ctx, c)
			if err != nil || p == nil {
				t.Fatalf("no permit: %v", err)
			}
			call, err := p.Start(ctx)
			if err != nil {
				t.Fatal(err)
			}
			stop.do(t, w, c, account)
			// A stop through this enclave cuts the call before it returns.
			if cut := errors.Is(context.Cause(call), webhook.ErrCut); cut != stop.cuts {
				t.Errorf("%s: the started call cut: %v, want %v", name, cut, stop.cuts)
			}
			p.Finish()
			if s := sub(t, w, c); s.Calls || s.Why != stop.why {
				t.Fatalf("%s: %+v", name, s)
			}
			if _, p, err := w.Connections().Admit(ctx, c); err != nil || p != nil {
				t.Fatalf("%s: admitted after it stopped: %v", name, err)
			}
		})
	}

	t.Run("a permit", func(t *testing.T) {
		w := newWorld(t)
		c, _ := w.Make(t, ask)
		s, p, err := w.Connections().Admit(ctx, c)
		if err != nil || p == nil || !s.Calls {
			t.Fatal(err)
		}
		if want := s.ReadAt.Add(5 * time.Second); p.WriteBy().After(want) {
			t.Errorf("written by %s, later than five seconds after the read (%s)", p.WriteBy(), want)
		}
		// Cut after Admit and before Start: the call never starts.
		w.Revoke(t, c)
		if _, err := p.Start(ctx); !errors.Is(err, webhook.ErrCut) {
			t.Errorf("a permit cut before it started: %v", err)
		}
		p.Finish()
		// A permit not started by WriteBy never starts.
		c2, _ := w.Make(t, ask)
		_, p2, err := w.Connections().Admit(ctx, c2)
		if err != nil || p2 == nil {
			t.Fatal(err)
		}
		w.Advance(5*time.Second + time.Millisecond)
		if _, err := p2.Start(ctx); !errors.Is(err, webhook.ErrCut) {
			t.Errorf("a permit started after WriteBy: %v", err)
		}
		p2.Finish()
	})

	t.Run("removed", func(t *testing.T) {
		w := newWorld(t)
		c, _ := w.Make(t, ask)
		e := pending(t, w, c, webhooks.Progress{})
		w.Remove(t, c)
		if _, err := w.Connections().Subscription(ctx, c); !errors.Is(err, webhooks.ErrNoWebhook) {
			t.Fatal(err)
		}
		if _, _, err := w.Connections().Admit(ctx, c); !errors.Is(err, webhooks.ErrNoWebhook) {
			t.Fatal(err)
		}
		if _, err := w.Connections().Pending(ctx, c, e.Through); !errors.Is(err, webhooks.ErrNoWebhook) {
			t.Fatal(err)
		}
		if ok, _ := w.Connections().Recheck(ctx, c, webhooks.Match{}); ok {
			t.Fatal("a removed connection's match stands")
		}
		if ids, err := w.Connections().Webhooks(ctx, "", 1000); err != nil || slices.Contains(ids, c) {
			t.Fatalf("listed after its removal: %v %v", ids, err)
		}
	})

	t.Run("the vendor stops it", func(t *testing.T) {
		w := newWorld(t)
		c, _ := w.Make(t, ask)
		_, p, _ := w.Connections().Admit(ctx, c)
		call, err := p.Start(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Connections().Stop(ctx, c); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(context.Cause(call), webhook.ErrCut) {
			t.Error("the vendor's stop did not cut a call started")
		}
		p.Finish()
		if s := sub(t, w, c); s.Calls || s.State != contracts.WebhookStopped || s.Why != webhooks.SkipOff {
			t.Fatalf("%+v", s)
		}
	})

	t.Run("new mail: only what leaves", func(t *testing.T) {
		w := newWorld(t)
		c, account := w.Make(t, ask)
		start := w.Now()
		w.Deliver(t, c, 0, start.Add(time.Second), ArrivalHeld)
		noMatch := w.Deliver(t, c, 1, start.Add(2*time.Second), ArrivalNoMatch)
		e := pending(t, w, c, webhooks.Progress{Cursor: webhooks.Cursor{Received: start}})
		if e.Match != nil {
			t.Fatalf("a held message and one that does not match made a notice: %+v", e.Match)
		}
		if e.Through.Cursor.Compare(noMatch) < 0 {
			t.Fatalf("the cursor did not pass what was evaluated: %+v", e.Through)
		}
		leaves := w.Deliver(t, c, 1, start.Add(3*time.Second), ArrivalLeaves)
		e = pending(t, w, c, e.Through)
		if e.Match == nil || len(e.Match.Messages) != 1 || e.Match.Messages[0] != leaves || e.Match.Basis != sub(t, w, c).Basis {
			t.Fatalf("%+v", e.Match)
		}
		// Evaluated once: the same progress again finds nothing new.
		again := pending(t, w, c, e.Through)
		if again.Match != nil {
			t.Fatalf("evaluated twice: %+v", again.Match)
		}
		// Stored late, received before the cursor but within the lag: found
		// once, by the late sweep, and the cursor does not move back.
		late := w.Deliver(t, c, 0, leaves.Received.Add(-time.Second), ArrivalLeaves)
		e = pending(t, w, c, again.Through)
		if e.Match == nil || e.Match.Messages[0] != late || e.Through.Cursor.Compare(again.Through.Cursor) != 0 {
			t.Fatalf("a message stored late: %+v %+v", e.Match, e.Through)
		}
		if twice := pending(t, w, c, e.Through); twice.Match != nil {
			t.Fatal("a message stored late was told twice")
		}
		// Stored later than the lag: not told (the vendor finds it when it
		// fetches).
		w.Deliver(t, c, 0, leaves.Received.Add(-webhook.ArrivalLag-time.Second), ArrivalLeaves)
		if old := pending(t, w, c, e.Through); old.Match != nil {
			t.Fatal("a message stored past the lag was told")
		}
		// The model off, or the rules unreadable: nothing is evaluated.
		w.ModelOff(t, account)
		w.Deliver(t, c, 0, leaves.Received.Add(time.Minute), ArrivalLeaves)
		if _, err := w.Connections().Pending(ctx, c, e.Through); !errors.Is(err, contracts.ErrModelUnavailable) {
			t.Fatalf("with the model off: %v", err)
		}
		w.ModelOn(t, account)
		w.RulesUnreadable(t, account, true)
		if _, err := w.Connections().Pending(ctx, c, e.Through); !errors.Is(err, webhooks.ErrRulesUnavailable) {
			t.Fatalf("with the rules unreadable: %v", err)
		}
		w.RulesUnreadable(t, account, false)
		if e := pending(t, w, c, e.Through); e.Match == nil {
			t.Fatal("what arrived meanwhile was not told once both are back")
		}
	})

	// The design review's finding 3: more than RecentMax + ArrivalPage
	// messages, many received in the same instant, in two mailboxes, with
	// messages stored late among them: the cursor reaches the last, a
	// message that leaves only at the end is found, and each message is
	// evaluated once, in a number of passes the page bounds.
	t.Run("a burst", func(t *testing.T) {
		w := newWorld(t)
		c, _ := w.Make(t, contracts.WebhookRequest{URL: ask.URL, NewMail: true})
		start := w.Now()
		const n = 600
		var last webhooks.Cursor
		for i := range n {
			kind := ArrivalNoMatch
			if i == n-1 {
				kind = ArrivalLeaves
			}
			// Ten at a time in the same millisecond, alternating mailboxes.
			if at := w.Deliver(t, c, i%2, start.Add(time.Duration(i/10)*time.Millisecond), kind); at.Compare(last) > 0 {
				last = at
			}
		}
		p := webhooks.Progress{Cursor: webhooks.Cursor{Received: start.Add(-time.Second)}}
		var found *webhooks.Match
		passes := 0
		for {
			passes++
			e := pending(t, w, c, p)
			if e.Match != nil {
				found = e.Match
			}
			if len(e.Through.Recent) > webhooks.RecentMax {
				t.Fatalf("recent holds %d", len(e.Through.Recent))
			}
			if e.Through.Cursor.Compare(p.Cursor) < 0 {
				t.Fatal("the cursor moved back")
			}
			p = e.Through
			if !e.More {
				break
			}
			if passes > n/webhook.ArrivalPage+2 {
				t.Fatalf("still not through after %d passes, at %+v", passes, p.Cursor)
			}
		}
		if p.Cursor != last || found == nil || found.Messages[0] != last {
			t.Fatalf("the last message not reached: cursor %+v, last %+v, match %+v", p.Cursor, last, found)
		}
		// Messages stored late within the burst's last two minutes, then
		// evaluated: told once, and nothing evaluated again.
		lateOne := w.Deliver(t, c, 1, last.Received.Add(-time.Millisecond), ArrivalLeaves)
		e := pending(t, w, c, p)
		if e.Match != nil && e.Match.Messages[0] != lateOne {
			t.Fatalf("%+v", e.Match)
		}
		if again := pending(t, w, c, e.Through); again.Match != nil {
			t.Fatal("told twice")
		}
		if f, ok := w.Connections().(*Fake); ok {
			for at, times := range f.Evaluated(c) {
				if times > 1 {
					t.Errorf("%+v evaluated %d times", at, times)
				}
			}
		}
	})

	// The design review's finding 1: a match no longer stands once a rule
	// that holds it is saved, the connection's record changed, its messages
	// are gone, or the rules cannot be read; a match restored from before
	// any of them names its old basis and is refused the same way; and
	// evaluating again under the current basis finds only what would leave
	// now.
	t.Run("a match under its basis", func(t *testing.T) {
		changes := map[string]func(t *testing.T, w World, c, account string, m *webhooks.Match){
			"a rule that holds it": func(t *testing.T, w World, _, account string, _ *webhooks.Match) { w.HoldAll(t, account) },
			"a mailbox taken out":  func(t *testing.T, w World, c, _ string, _ *webhooks.Match) { w.DropMailbox(t, c) },
			"its messages deleted": func(t *testing.T, w World, c, _ string, m *webhooks.Match) {
				for _, at := range m.Messages {
					w.Delete(t, c, at)
				}
			},
			"the rules unreadable": func(t *testing.T, w World, _, account string, _ *webhooks.Match) { w.RulesUnreadable(t, account, true) },
		}
		for name, change := range changes {
			t.Run(name, func(t *testing.T) {
				w := newWorld(t)
				c, account := w.Make(t, contracts.WebhookRequest{URL: ask.URL, NewMail: true})
				start := w.Now()
				w.Deliver(t, c, 1, start.Add(time.Second), ArrivalLeaves)
				e := pending(t, w, c, webhooks.Progress{Cursor: webhooks.Cursor{Received: start}})
				if e.Match == nil {
					t.Fatal("no match")
				}
				if ok, err := w.Connections().Recheck(ctx, c, *e.Match); !ok || err != nil {
					t.Fatalf("an unchanged match does not stand: %v", err)
				}
				restored := *e.Match // what a storage writer could put back
				change(t, w, c, account, e.Match)
				if ok, _ := w.Connections().Recheck(ctx, c, *e.Match); ok {
					t.Fatalf("%s: the match still stands", name)
				}
				if ok, _ := w.Connections().Recheck(ctx, c, restored); ok {
					t.Fatalf("%s: a restored match stands", name)
				}
				w.RulesUnreadable(t, account, false)
				// Rebuilt from where it began, under the current basis.
				again := pending(t, w, c, e.Match.From)
				if again.Match != nil {
					if again.Match.Basis != sub(t, w, c).Basis {
						t.Fatalf("%s: rebuilt under an old basis", name)
					}
					if name != "the rules unreadable" {
						t.Fatalf("%s: rebuilt to a match that should not leave: %+v", name, again.Match)
					}
				}
			})
		}
	})

	t.Run("every webhook is listed from the records", func(t *testing.T) {
		w := newWorld(t)
		var made []string
		for range 5 {
			c, _ := w.Make(t, ask)
			made = append(made, c)
		}
		w.Off(t, made[1]) // off is still a webhook of the record
		var listed []string
		after := ""
		for page := 0; ; page++ {
			ids, err := w.Connections().Webhooks(ctx, after, 2)
			if err != nil {
				t.Fatal(err)
			}
			listed = append(listed, ids...)
			if len(ids) < 2 {
				break
			}
			after = ids[len(ids)-1]
			if page > 10 {
				t.Fatal("the pages do not end")
			}
		}
		for _, c := range made {
			if !slices.Contains(listed, c) {
				t.Errorf("%s not listed: %v", c, listed)
			}
		}
		if !slices.IsSorted(listed) || len(slices.Compact(slices.Clone(listed))) != len(listed) {
			t.Errorf("not in a stable order once each: %v", listed)
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
		URL: r.URL, NewMail: r.NewMail, Mailboxes: []string{w.next("mbx_"), w.next("mbx_")}, Schedule: r.Schedule, TimeZone: "Europe/Madrid", MadeAt: w.clock,
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
func (w *FakeWorld) HoldAll(_ *testing.T, a string)       { w.Fake.HoldAll(a) }
func (w *FakeWorld) DropMailbox(_ *testing.T, c string)   { w.Fake.DropMailbox(c) }
func (w *FakeWorld) RulesUnreadable(_ *testing.T, a string, unreadable bool) {
	w.Fake.RulesUnreadable(a, unreadable)
}
func (w *FakeWorld) Delete(_ *testing.T, c string, m webhooks.Cursor) { w.Fake.Delete(c, m) }

func (w *FakeWorld) Deliver(t *testing.T, c string, mailbox int, received time.Time, kind Arrival) webhooks.Cursor {
	t.Helper()
	s, err := w.Fake.Subscription(context.Background(), c)
	if err != nil || mailbox >= len(s.Mailboxes) {
		t.Fatalf("no mailbox %d: %v", mailbox, err)
	}
	m := Message{Mailbox: s.Mailboxes[mailbox], ID: w.next("msg_"), Received: received, Leaves: kind == ArrivalLeaves}
	w.Fake.Deliver(c, m)
	return m.at()
}
