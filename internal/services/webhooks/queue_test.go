package webhooks_test

import (
	"bytes"
	"slices"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/webhook"
)

// K35: Subscribe makes the state entry, the mailbox index and the first due
// minute, once however often it is called; a schedule alone is due at its
// first run and indexes no mailbox; a zone the image does not know is
// refused.
func TestK35SubscribeMakesItsEntriesOnce(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	c := w.make(s, made{newMail: true})
	states := w.entries(webhooks.PartitionState)
	for range 3 {
		if err := s.Subscribe(ctx, c.Subscription); err != nil {
			t.Fatal(err)
		}
	}
	again := w.entries(webhooks.PartitionState)
	if len(states) != 1 || len(again) != 1 || states[0].Version != again[0].Version || states[0].Key != c.Connection {
		t.Fatalf("not one state, made once: %d %d", len(states), len(again))
	}
	for _, m := range c.Mailboxes {
		if got := w.entries(webhooks.PartitionMailbox + m); len(got) != 1 || got[0].Key != c.Connection {
			t.Fatalf("mailbox index: %v", got)
		}
	}
	if due := w.dueEntries(0, 0); len(due[c.Connection]) != 1 {
		t.Fatalf("not due now: %v", due)
	}
	for _, e := range w.entries(webhooks.PartitionState) {
		if bytes.Contains(e.Value, []byte(c.Connection)) || bytes.Contains(e.Value, []byte(c.URL)) || bytes.Contains(e.Value, []byte("trip-app")) {
			t.Fatal("the state is not sealed")
		}
	}

	daily := &contracts.WebhookSchedule{Every: contracts.EveryDay, At: "09:15"}
	d := w.make(s, made{schedule: daily})
	if got := w.entries(webhooks.PartitionMailbox + d.Mailboxes[0]); len(got) != 0 {
		t.Fatal("a schedule alone indexes a mailbox")
	}
	if due := w.dueEntries(0, 2*time.Hour); !slices.Equal(due[d.Connection], []string{"08:15"}) {
		t.Fatalf("not due at its first run (09:15 in Madrid): %v", due[d.Connection])
	}
	bad := d.Subscription
	bad.Connection, bad.TimeZone = "con_badzone", "Mars/Olympus"
	if err := s.Subscribe(ctx, bad); err == nil {
		t.Fatal("a zone the image does not know is taken")
	}
}

// K36: an arrival makes its notice within the worker's pass; a hint lost is
// found by the repair within the hour.
func TestK36AnArrivalIsToldAndALostHintIsFoundByTheRepair(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	c := w.make(s, made{newMail: true})
	w.run(s)
	if len(w.sender.all()) != 0 {
		t.Fatal("a notice with no mail")
	}
	w.clock.add(time.Minute)
	m := w.deliver(s, c, 1, true)
	w.clock.add(5 * time.Second)
	w.run(s)
	ns := w.sender.notices(t)
	if len(ns) != 1 || ns[0].Type != contracts.NoticeMailMatched || !ns[0].Since.Equal(m.Received.Truncate(time.Minute)) || len(c.vendor.Taken()) != 1 {
		t.Fatalf("no notice within the pass: %+v", ns)
	}

	// A hint lost: nothing until the repair, which finds it within the hour.
	w.clock.add(10 * time.Minute)
	w.deliver(nil, c, 0, true)
	w.every(time.Minute, 30*time.Minute, s)
	if len(w.sender.all()) != 1 {
		t.Fatal("told without a hint or a repair")
	}
	w.clock.add(30 * time.Minute)
	webhooks.Repair(ctx, s)
	w.run(s)
	if ns := w.sender.notices(t); len(ns) != 2 || ns[1].ID == ns[0].ID {
		t.Fatalf("the repair did not find the lost hint: %d", len(ns))
	}
}

// An entry made due late in a minute that ends before the next pass is
// read by that pass however far behind the sweep is (the clock moved two
// days ahead, as dev mode's does, or the passes stopped): every pass reads
// the last ten minutes while the sweep is behind them, as a sweep that is
// not behind does.
func TestAMinuteJustEndedIsReadWhileTheSweepIsBehind(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	c := w.make(s, made{newMail: true})
	w.run(s)
	w.clock.add(48*time.Hour + 50*time.Second)
	w.run(s)
	m := w.deliver(s, c, 0, true)
	webhooks.Hints(ctx, s)
	w.clock.add(15 * time.Second)
	webhooks.Drain(ctx, s)
	if ns := w.sender.notices(t); len(ns) != 1 || !ns[0].Since.Equal(m.Received.Truncate(time.Minute)) {
		t.Fatalf("the minute just ended was not read: %+v, due %v", ns, w.dueEntries(10*time.Minute, 0))
	}
	// Ten minutes back at most: one older is the sweep's, which goes on
	// from where it stopped and reaches it.
	w.clock.add(48*time.Hour + 50*time.Second)
	w.run(s)
	w.clock.add(time.Minute)
	m = w.deliver(s, c, 1, true)
	webhooks.Hints(ctx, s)
	w.clock.add(11 * time.Minute)
	webhooks.Drain(ctx, s)
	if len(w.sender.all()) != 1 {
		t.Fatal("an entry older than ten minutes was read before the sweep reached it")
	}
	for range 4 * 24 * 60 / 128 {
		webhooks.Drain(ctx, s)
	}
	if ns := w.sender.notices(t); len(ns) != 2 || !ns[1].Since.Equal(m.Received.Truncate(time.Minute)) {
		t.Fatalf("the sweep did not reach an older entry: %+v", ns)
	}
}

// K37: coalescing and frozen notices.
func TestK37CoalescingAndFrozenNotices(t *testing.T) {
	t.Run("a hundred matches in a minute make one notice, then one a minute", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		c := w.make(s, made{newMail: true})
		for i := range 100 {
			w.deliver(s, c, i%2, true)
			if i%5 == 4 {
				w.clock.add(3 * time.Second)
				w.run(s)
			}
		}
		if n := len(w.sender.all()); n != 1 {
			t.Fatalf("%d notices in the first minute", n)
		}
		w.every(5*time.Second, 2*time.Minute, s)
		calls := w.sender.all()
		if len(calls) != 2 || calls[0].ID == calls[1].ID {
			t.Fatalf("not one more notice for what arrived since: %d", len(calls))
		}
		if taken := c.vendor.Taken(); len(taken) != 2 || taken[1].CreatedAt.Sub(taken[0].CreatedAt) < webhook.MinInterval {
			t.Fatal("two notices less than a minute apart")
		}
		w.every(5*time.Second, 5*time.Minute, s)
		if len(w.sender.all()) != 2 {
			t.Fatal("a notice for nothing new")
		}
	})

	t.Run("a message during the vendor's fetch and after its 2xx makes a distinct notice", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		c := w.make(s, made{newMail: true})
		w.clock.add(time.Minute)
		a := w.deliver(s, c, 0, true)
		var b, after webhooks.Cursor
		var fetchedForN []string
		c.vendor.Taking = func(n contracts.WebhookNotice) {
			if b.Message == "" {
				// The vendor fetches for N, and B arrives meanwhile.
				fetchedForN = w.fetch(c, n.Since)
				b = w.deliver(s, c, 1, true)
			}
		}
		w.sender.set(func(x *sender) {
			x.after = func(webhook.Call) {
				if after.Message == "" {
					// N's 2xx left the vendor; the worker has not written it.
					after = w.deliver(s, c, 0, true)
				}
			}
		})
		w.run(s)
		if len(c.vendor.Taken()) != 1 || !slices.Equal(fetchedForN, []string{a.Message}) {
			t.Fatalf("N: %v", fetchedForN)
		}
		w.every(5*time.Second, 2*time.Minute, s)
		taken := c.vendor.Taken()
		if len(taken) != 2 || taken[1].ID == taken[0].ID {
			t.Fatalf("no distinct notice for B: %d", len(taken))
		}
		got := w.fetch(c, taken[1].Since)
		if !slices.Contains(got, b.Message) || !slices.Contains(got, after.Message) {
			t.Fatalf("N+1's fetch does not find B and the later one: %v", got)
		}
	})

	t.Run("every retry of a notice carries its identifier and body byte for byte", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		c := w.make(s, made{newMail: true})
		w.clock.add(time.Minute)
		w.deliver(s, c, 0, true)
		w.sender.set(func(x *sender) {
			x.script = []webhook.Outcome{webhook.OutcomeNoAnswer, webhook.OutcomeStatus, webhook.OutcomeRedirect, webhook.OutcomeRefused}
		})
		w.run(s)
		// Matches arriving meanwhile wait behind it.
		for range 8 {
			w.deliver(s, c, 1, true)
			w.every(time.Minute, 15*time.Minute, s)
		}
		calls := w.sender.all()
		if len(calls) < 5 {
			t.Fatalf("%d calls", len(calls))
		}
		for _, call := range calls[1:5] {
			if call.ID != calls[0].ID || !bytes.Equal(call.Body, calls[0].Body) {
				t.Fatal("a retry changed its notice")
			}
		}
		if taken := c.vendor.Taken(); len(taken) < 2 || taken[0].ID != calls[0].ID || taken[1].ID == taken[0].ID {
			t.Fatal("what waited is not a notice of its own")
		}
	})

	t.Run("a scheduled run and a match waiting go as two notices, the scheduled one first", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		daily := &contracts.WebhookSchedule{Every: contracts.EveryDay, At: "09:15"}
		c := w.make(s, made{newMail: true, schedule: daily})
		// Down from 08:00 to 08:20 UTC (09:20 in Madrid): a match and a run wait.
		w.clock.add(10 * time.Minute)
		w.deliver(s, c, 0, true)
		w.clock.add(10 * time.Minute)
		w.run(s)
		w.every(5*time.Second, 2*time.Minute, s)
		ns := w.sender.notices(t)
		if len(ns) != 2 || ns[0].Type != contracts.NoticeScheduleDue || ns[1].Type != contracts.NoticeMailMatched ||
			ns[1].CreatedAt.Sub(ns[0].CreatedAt) < webhook.MinInterval {
			t.Fatalf("%+v", ns)
		}
		if !ns[0].ScheduledAt.Equal(time.Date(2026, 11, 20, 8, 15, 0, 0, time.UTC)) || !ns[0].Since.Equal(start) || ns[0].Late {
			t.Fatalf("the run: %+v", ns[0])
		}
	})
}

// K44: the queue over the contract's burst: more than RecentMax +
// ArrivalPage messages received in one instant across two mailboxes, the
// last alone leaving, with late arrivals, worked by two enclaves: the last
// is reached and told by one notice, each message evaluated once; one
// stored late within the window is told once; one stored past the
// window's bound is not, and never holds the cursor back.
func TestK44TheBurstThroughTheQueue(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	c := w.make(a, made{newMail: true})
	w.clock.add(time.Minute)
	at := w.clock.now()
	n := webhooks.RecentMax + webhook.ArrivalPage + 100
	for i := range n {
		w.deliverAt(nil, c, i%2, at, false)
	}
	last := w.deliverAt(a, c, 1, at.Add(time.Millisecond), true)
	for range 12 {
		w.clock.add(time.Second)
		w.run(a, b)
	}
	ns := w.sender.notices(t)
	if len(ns) != 1 || !ns[0].Since.Equal(last.Received.Truncate(time.Minute)) {
		t.Fatalf("%d notices", len(ns))
	}
	for m, k := range w.fake.Evaluated(c.Connection) {
		if k != 1 {
			t.Fatalf("%v evaluated %d times", m, k)
		}
	}
	if len(w.fake.Evaluated(c.Connection)) != n+1 {
		t.Fatal("not every message evaluated")
	}

	// One stored late, within the lag, after only a few messages: told
	// once, through either enclave.
	w.clock.add(2 * time.Minute)
	recent := w.deliverAt(a, c, 0, w.clock.now(), false)
	w.clock.add(time.Second)
	w.run(b, a)
	late := w.deliverAt(b, c, 1, recent.Received.Add(-time.Second), true)
	w.every(5*time.Second, 3*time.Minute, a, b)
	if ns := w.sender.notices(t); len(ns) != 2 || !ns[1].Since.Equal(late.Received.Truncate(time.Minute)) {
		t.Fatalf("the late one: %d notices", len(ns))
	}
	if w.fake.Evaluated(c.Connection)[late] != 1 {
		t.Fatal("the late one not evaluated once")
	}

	// Past the window's bound (more than RecentMax evaluated within the
	// lag), one stored late among the dropped is not told, and the cursor
	// still reaches what comes after.
	w.clock.add(5 * time.Minute)
	burst := w.clock.now()
	for i := range webhooks.RecentMax + 10 {
		w.deliverAt(a, c, i%2, burst.Add(time.Duration(i)*time.Millisecond), false)
	}
	w.run(a)
	w.clock.add(time.Second)
	w.run(b)
	dropped := w.deliverAt(a, c, 0, burst, true)
	w.clock.add(time.Second)
	after := w.deliverAt(a, c, 1, w.clock.now(), true)
	w.every(5*time.Second, 3*time.Minute, a, b)
	ev := w.fake.Evaluated(c.Connection)
	if ev[dropped] != 0 || ev[after] != 1 {
		t.Fatalf("dropped %d, after %d", ev[dropped], ev[after])
	}
	if ns := w.sender.notices(t); len(ns) != 3 || !ns[2].Since.Equal(after.Received.Truncate(time.Minute)) {
		t.Fatalf("after the window: %d notices", len(ns))
	}
}
