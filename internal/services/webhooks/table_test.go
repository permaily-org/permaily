package webhooks_test

import (
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webhook"
)

// stateOf is the connection's state entry as stored.
func (w *world) stateOf(connection string) store.Entry {
	w.t.Helper()
	for _, e := range w.entries(webhooks.PartitionState) {
		if e.Key == connection {
			return e
		}
	}
	w.t.Fatal("no state")
	return store.Entry{}
}

func (w *world) drop(partition, key string) {
	w.t.Helper()
	if err := w.mem.DeleteEntry(ctx, partition, key); err != nil {
		w.t.Fatal(err)
	}
}

// dropDue deletes every due entry of a connection around now.
func (w *world) dropDue(connection string) {
	now := w.clock.now().Truncate(time.Minute)
	for m := now.Add(-time.Hour); !m.After(now.Add(48 * time.Hour)); m = m.Add(time.Minute) {
		_ = w.mem.DeleteEntry(ctx, webhooks.PartitionDue+m.Format("200601021504"), connection)
	}
}

// K46: whoever writes the table can delay, drop or repeat a notice, and
// nothing more.
func TestK46WhoeverWritesTheTable(t *testing.T) {
	t.Run("a state or a due entry deleted delays a notice to the repair", func(t *testing.T) {
		for _, lose := range []string{"state", "due"} {
			w := newWorld(t)
			s := w.enclave()
			c := w.make(s, made{newMail: true})
			w.clock.add(time.Minute)
			w.deliver(nil, c, 0, true)
			if lose == "state" {
				w.drop(w.stateOf(c.Connection).Partition, c.Connection)
			} else {
				w.dropDue(c.Connection)
			}
			w.every(time.Minute, 30*time.Minute, s)
			if len(w.sender.all()) != 0 {
				t.Fatal("told before the repair")
			}
			webhooks.Repair(ctx, s)
			w.run(s)
			if len(c.vendor.Taken()) != 1 {
				t.Fatalf("%s lost: not told after the repair", lose)
			}
		}
	})

	t.Run("state, due entry and index deleted together are rebuilt from the records", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		mail := w.make(s, made{newMail: true})
		sched := w.make(s, made{schedule: &contracts.WebhookSchedule{Every: contracts.EveryDay, At: "09:30"}})
		for _, c := range []*conn{mail, sched} {
			w.drop(w.stateOf(c.Connection).Partition, c.Connection)
			w.dropDue(c.Connection)
			for _, m := range c.Mailboxes {
				_ = w.mem.DeleteEntry(ctx, webhooks.PartitionMailbox+m, c.Connection)
			}
		}
		w.clock.add(10 * time.Minute)
		w.deliver(s, mail, 1, true)
		w.every(time.Minute, 10*time.Minute, s)
		if len(w.sender.all()) != 0 {
			t.Fatal("told with nothing of it in the queue")
		}
		webhooks.Repair(ctx, s)
		if len(w.entries(webhooks.PartitionMailbox+mail.Mailboxes[0])) != 1 || len(w.entries(webhooks.PartitionState)) != 2 {
			t.Fatal("not rebuilt")
		}
		w.every(time.Minute, 30*time.Minute, s)
		if len(mail.vendor.Taken()) != 1 || len(sched.vendor.Taken()) != 1 || sched.vendor.Taken()[0].Type != contracts.NoticeScheduleDue {
			t.Fatalf("after the repair: %d %d", len(mail.vendor.Taken()), len(sched.vendor.Taken()))
		}
		// A repair within the hour changes nothing; a schedule's future due
		// entry stays where it is.
		webhooks.Repair(ctx, s)
		w.clock.add(webhook.RepairEvery)
		webhooks.Repair(ctx, s)
		w.every(time.Minute, 10*time.Minute, s)
		if len(w.sender.all()) != 2 {
			t.Fatal("the repair repeated a notice")
		}
	})

	t.Run("a state put back repeats at most a notice", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		c := w.make(s, made{newMail: true})
		w.clock.add(time.Minute)
		w.deliver(s, c, 0, true)
		w.sender.set(func(x *sender) { x.script = []webhook.Outcome{webhook.OutcomeNoAnswer} })
		w.run(s)
		inFlight := w.stateOf(c.Connection)
		w.every(time.Minute, 5*time.Minute, s)
		if len(c.vendor.Taken()) != 1 {
			t.Fatal("not taken")
		}
		now := w.stateOf(c.Connection)
		inFlight.Version = now.Version
		w.mem.SetEntry(inFlight)
		webhooks.Repair(ctx, s)
		w.every(time.Minute, 10*time.Minute, s)
		calls := w.sender.all()
		if len(calls) != 3 || calls[2].ID != calls[0].ID || len(c.vendor.Taken()) != 1 {
			t.Fatalf("%d calls", len(calls))
		}
	})

	t.Run("an entry moved to another connection does not open", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		a := w.make(s, made{newMail: true, host: "a.example"})
		b := w.make(s, made{newMail: true, host: "b.example"})
		w.run(s)
		moved := w.stateOf(a.Connection)
		moved.Key = b.Connection
		moved.Partition = w.stateOf(b.Connection).Partition
		w.mem.SetEntry(moved)
		w.clock.add(time.Minute)
		w.deliver(s, b, 0, true)
		webhooks.Hints(ctx, s)
		// A's due entry moved to a connection of its own name.
		due := webhooks.PartitionDue + w.clock.now().Truncate(time.Minute).Format("200601021504")
		w.deliver(s, a, 0, true)
		webhooks.Hints(ctx, s)
		for _, e := range w.entries(due) {
			if e.Key == a.Connection {
				w.drop(due, e.Key)
				e.Key = "con_elsewhere"
				w.mem.SetEntry(e)
			}
		}
		w.every(time.Minute, 30*time.Minute, s)
		if len(w.sender.all()) != 0 {
			t.Fatal("a moved entry opened")
		}
		if len(w.entries(due)) != 0 {
			t.Fatal("a moved due entry stays")
		}
		// The repair puts it right: it was not written by this service.
		webhooks.Repair(ctx, s)
		w.every(time.Minute, 5*time.Minute, s)
		if len(b.vendor.Taken()) != 1 || len(a.vendor.Taken()) != 1 {
			t.Fatal("not put right by the repair")
		}
	})

	t.Run("a notice in flight is never sent to another URL", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		c := w.make(s, made{newMail: true})
		w.clock.add(time.Minute)
		w.deliver(s, c, 0, true)
		w.sender.set(func(x *sender) { x.script = []webhook.Outcome{webhook.OutcomeNoAnswer} })
		w.run(s)
		other := c.Subscription
		other.URL = "https://trip-app.example/hooks/elsewhere"
		w.fake.Make(other)
		w.every(time.Minute, 5*time.Minute, s)
		for _, call := range w.sender.all()[1:] {
			if call.ID == w.sender.all()[0].ID {
				t.Fatal("the frozen notice sent to another URL")
			}
		}
		if skipped(s, webhooks.SkipChanged) == 0 {
			t.Fatal("not dropped as changed")
		}
	})

	t.Run("a match put back from before a rule was saved", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		c := w.make(s, made{newMail: true})
		w.clock.add(time.Minute)
		w.deliver(s, c, 0, true)
		w.sender.set(func(x *sender) { x.script = []webhook.Outcome{webhook.OutcomeNoAnswer} })
		w.run(s)
		pending := w.stateOf(c.Connection)
		w.fake.HoldAll(c.Account)
		w.every(time.Minute, 5*time.Minute, s)
		pending.Version = w.stateOf(c.Connection).Version
		w.mem.SetEntry(pending)
		webhooks.Repair(ctx, s)
		w.every(time.Minute, 30*time.Minute, s)
		if len(w.sender.all()) != 1 {
			t.Fatalf("%d calls for a match a rule holds", len(w.sender.all()))
		}
		if skipped(s, webhooks.SkipChanged) < 2 {
			t.Fatal("not discarded as changed, each time")
		}
	})

	t.Run("a match whose messages were deleted is evaluated again", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		c := w.make(s, made{newMail: true})
		w.clock.add(time.Minute)
		m := w.deliver(s, c, 0, true)
		w.sender.set(func(x *sender) { x.script = []webhook.Outcome{webhook.OutcomeNoAnswer} })
		w.run(s)
		w.fake.Delete(c.Connection, m)
		w.clock.add(time.Second)
		n := w.deliver(s, c, 1, true)
		w.every(time.Minute, 5*time.Minute, s)
		taken := c.vendor.Taken()
		if len(taken) != 1 || !taken[0].Since.Equal(n.Received.Truncate(time.Minute)) || w.fake.Evaluated(c.Connection)[n] < 1 {
			t.Fatalf("%+v", taken)
		}
	})

	t.Run("the rules unreadable", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		c := w.make(s, made{newMail: true})
		w.clock.add(time.Minute)
		w.deliver(s, c, 0, true)
		w.sender.set(func(x *sender) { x.script = []webhook.Outcome{webhook.OutcomeNoAnswer} })
		w.run(s)
		w.fake.RulesUnreadable(c.Account, true)
		w.every(time.Minute, 30*time.Minute, s)
		if len(w.sender.all()) != 1 || skipped(s, webhooks.SkipRules) == 0 {
			t.Fatal("called while the rules cannot be read")
		}
		w.fake.RulesUnreadable(c.Account, false)
		w.every(time.Minute, 5*time.Minute, s)
		if len(c.vendor.Taken()) != 1 {
			t.Fatal("not told once the rules are back")
		}
	})
}
