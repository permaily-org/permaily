package webhooks_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webhook"
)

var errStopped = errors.New("stopped")

// K38: two enclaves make one call per notice under the lease; one that
// stops mid-call is followed by the other's call with the same identifier
// and body once its lease ends, which the vendor takes once; with the 2xx
// lost, a vendor keeping identifiers webhook.DedupKeepFor takes every
// retry once, and one keeping them five minutes takes a retry again.
func TestK38TwoEnclavesTheLeaseAndALostAcknowledgement(t *testing.T) {
	t.Run("one call per notice", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.enclave(), w.enclave()
		c := w.make(a, made{newMail: true})
		w.clock.add(time.Minute)
		w.deliver(a, c, 0, true)
		webhooks.Hinted(ctx, b, c.Mailboxes[0])
		webhooks.Scan(ctx, a)
		webhooks.Scan(ctx, b)
		webhooks.Dispatch(ctx, a)
		webhooks.Dispatch(ctx, b)
		webhooks.Wait(a)
		webhooks.Wait(b)
		w.run(a, b)
		if len(w.sender.all()) != 1 || len(c.vendor.Taken()) != 1 {
			t.Fatalf("%d calls", len(w.sender.all()))
		}
	})

	t.Run("an enclave stopped mid-call", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.enclave(), w.enclave()
		c := w.make(a, made{newMail: true})
		w.clock.add(time.Minute)
		w.deliver(a, c, 0, true)
		// A stops after its call: nothing it would write next is written.
		w.sender.set(func(x *sender) {
			x.after = func(webhook.Call) {
				w.setInterrupt(func(context.Context, store.Step, string, string) error { return errStopped })
			}
		})
		w.run(a)
		w.setInterrupt(nil)
		w.sender.set(func(x *sender) { x.after = nil })
		w.clock.add(webhook.LeaseFor - time.Second)
		w.run(b)
		if len(w.sender.all()) != 1 {
			t.Fatal("called again under the lease")
		}
		w.clock.add(time.Second)
		w.run(b)
		calls := w.sender.all()
		if len(calls) != 2 || calls[1].ID != calls[0].ID || !bytes.Equal(calls[1].Body, calls[0].Body) || calls[1].Signature == calls[0].Signature {
			t.Fatal("not the same notice, freshly signed")
		}
		if len(c.vendor.Taken()) != 1 {
			t.Fatal("the vendor took it twice")
		}
		w.every(time.Minute, 10*time.Minute, a, b)
		if len(w.sender.all()) != 2 {
			t.Fatal("called after it was taken")
		}
	})

	for _, keep := range []time.Duration{webhook.DedupKeepFor, 5 * time.Minute} {
		t.Run("the 2xx lost, identifiers kept "+keep.String(), func(t *testing.T) {
			w := newWorld(t)
			a, b := w.enclave(), w.enclave()
			c := w.make(a, made{newMail: true})
			c.vendor.DedupFor = keep
			w.clock.add(time.Minute)
			w.deliver(a, c, 0, true)
			w.sender.set(func(x *sender) { x.lost = 1000 })
			w.every(time.Minute, webhook.GiveUpAfter+time.Hour, a, b)
			calls := w.sender.all()
			if len(calls) < 10 || len(calls) > 14 {
				t.Fatalf("%d calls", len(calls))
			}
			for _, call := range calls {
				if call.ID != calls[0].ID {
					t.Fatal("a retry with another identifier")
				}
			}
			taken := len(c.vendor.Taken())
			if keep == webhook.DedupKeepFor && taken != 1 || keep != webhook.DedupKeepFor && taken < 2 {
				t.Fatalf("taken %d times keeping identifiers %v", taken, keep)
			}
			if skipped(a, webhooks.SkipGivenUp)+skipped(b, webhooks.SkipGivenUp) != 1 {
				t.Fatal("not given up once")
			}
		})
	}
}

// K39: retries at 0, 1, 6 and 21 minutes, 1 h 21, 4 h 21, 10 h 21, 22 h 21
// and every 12 hours after; given up at 72 hours, the person shown since
// when and why; the next match is a new identifier.
func TestK39RetriesBackOffAndGiveUp(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	c := w.make(s, made{newMail: true})
	w.clock.add(time.Minute)
	w.deliver(s, c, 0, true)
	w.sender.set(func(x *sender) { x.script = slices.Repeat([]webhook.Outcome{webhook.OutcomeNoAnswer}, 100) })
	first := w.clock.now()
	w.run(s)
	w.every(time.Minute, webhook.GiveUpAfter+time.Hour, s)
	var at []time.Duration
	for _, call := range w.sender.all() {
		at = append(at, time.Unix(mustInt(t, call.Timestamp), 0).Sub(first))
	}
	want := []time.Duration{0, time.Minute, 6 * time.Minute, 21 * time.Minute, 81 * time.Minute, 261 * time.Minute, 621 * time.Minute, 1341 * time.Minute}
	for x := 1341 * time.Minute; x+12*time.Hour < webhook.GiveUpAfter; x += 12 * time.Hour {
		want = append(want, x+12*time.Hour)
	}
	if !slices.Equal(at, want) {
		t.Fatalf("calls at %v, want %v", at, want)
	}
	if skipped(s, webhooks.SkipGivenUp) != 1 {
		t.Fatal("not given up")
	}
	st, err := s.States(ctx, c.Connection)
	if err != nil || !st.FailingSince.Equal(first) || st.Failure != contracts.WebhookNoAnswer || !st.LastSentAt.IsZero() {
		t.Fatalf("%+v %v", st, err)
	}
	w.sender.set(func(x *sender) { x.script = nil })
	w.deliver(s, c, 1, true)
	w.run(s)
	calls := w.sender.all()
	if last := calls[len(calls)-1]; last.ID == calls[0].ID || len(c.vendor.Taken()) != 1 {
		t.Fatal("the next match is not a notice of its own")
	}
	if st, _ := s.States(ctx, c.Connection); !st.FailingSince.IsZero() || st.Failure != "" || st.LastSentAt.IsZero() {
		t.Fatalf("taken, still failing: %+v", st)
	}
	if _, err := s.States(ctx, "con_none"); !errors.Is(err, webhooks.ErrNoWebhook) {
		t.Fatal(err)
	}
}

// K40: a 410 records that the vendor asked to stop, and nothing is called
// again.
func TestK40AGoneStopsTheWebhook(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	c := w.make(s, made{newMail: true, schedule: &contracts.WebhookSchedule{Every: contracts.EveryDay, At: "10:00"}})
	w.clock.add(time.Minute)
	w.deliver(s, c, 0, true)
	c.vendor.Status = 410
	w.run(s)
	if !slices.Equal(w.fake.Stopped(), []string{c.Connection}) {
		t.Fatal("not stopped")
	}
	c.vendor.Status = 0
	w.deliver(s, c, 1, true)
	w.every(5*time.Minute, 48*time.Hour, s)
	webhooks.Repair(ctx, s)
	w.run(s)
	if len(w.sender.all()) != 1 || len(w.entries(webhooks.PartitionState)) != 0 {
		t.Fatal("called after a 410")
	}
}

// K41: a notice waiting when its connection is revoked, ended, every
// device revoked, turned off or its account closing is not called, and its
// entries are dropped.
func TestK41ANoticeOfAConnectionThatStoppedIsNotCalled(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(w *world, c string)
		why  webhooks.Skip
	}{
		{"revoked", func(w *world, c string) { w.fake.Revoke(c) }, webhooks.SkipEnded},
		{"ended", func(w *world, c string) { w.fake.Expire(c) }, webhooks.SkipEnded},
		{"closing", func(w *world, c string) { w.fake.Close(c) }, webhooks.SkipEnded},
		{"no device", func(w *world, c string) { w.fake.RevokeDevices(c) }, webhooks.SkipNoDevice},
		{"off", func(w *world, c string) { w.fake.Off(c) }, webhooks.SkipOff},
		{"replaced", func(w *world, c string) { w.fake.Remove(c) }, webhooks.SkipEnded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			s := w.enclave()
			c := w.make(s, made{newMail: true})
			w.clock.add(time.Minute)
			w.deliver(s, c, 0, true)
			w.sender.set(func(x *sender) { x.script = []webhook.Outcome{webhook.OutcomeNoAnswer} })
			w.run(s)
			w.deliver(s, c, 1, true)
			tc.stop(w, c.Connection)
			s.Ended(c.Connection)
			w.every(time.Minute, 10*time.Minute, s)
			if len(w.sender.all()) != 1 {
				t.Fatal("called after it stopped")
			}
			if skipped(s, tc.why) == 0 {
				t.Fatalf("not skipped %s", tc.why)
			}
			if len(w.entries(webhooks.PartitionState)) != 0 || len(w.entries(webhooks.PartitionMailbox+c.Mailboxes[0])) != 0 || len(w.dueEntries(time.Hour, time.Hour)) != 0 {
				t.Fatal("its entries stay")
			}
		})
	}
}

// K42: while the model cannot be asked, nothing is evaluated and no run is
// sent (model_unavailable); once back, what arrived meanwhile is told once.
func TestK42TheModelOff(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	c := w.make(s, made{newMail: true, schedule: &contracts.WebhookSchedule{Every: contracts.EveryDay, At: "09:30"}})
	w.fake.ModelOff(c.Account)
	w.clock.add(time.Minute)
	m := w.deliver(s, c, 0, true)
	w.every(time.Minute, 40*time.Minute, s)
	if len(w.sender.all()) != 0 || len(w.fake.Evaluated(c.Connection)) != 0 {
		t.Fatal("evaluated or called while the model is off")
	}
	if skipped(s, webhooks.SkipModel) != 1 {
		t.Fatal("the run not skipped")
	}
	w.fake.ModelOn(c.Account)
	w.every(time.Minute, 5*time.Minute, s)
	ns := w.sender.notices(t)
	if len(ns) != 1 || ns[0].Type != contracts.NoticeMailMatched || !ns[0].Since.Equal(m.Received.Truncate(time.Minute)) || w.fake.Evaluated(c.Connection)[m] != 1 {
		t.Fatalf("%+v", ns)
	}
	// The skipped run left the coverage boundary: the next run covers it.
	w.every(5*time.Minute, 24*time.Hour, s)
	ns = w.sender.notices(t)
	if last := ns[len(ns)-1]; last.Type != contracts.NoticeScheduleDue || !last.Since.Equal(start) {
		t.Fatalf("the next run: %+v", last)
	}

	// The model off at the moment of the call: the match is dropped and
	// evaluated again once it is back, told once.
	w.clock.add(time.Hour)
	n := w.deliver(s, c, 1, true)
	webhooks.Hints(ctx, s)
	webhooks.Scan(ctx, s)
	off := &modelOffAtAdmit{Connections: w.fake, w: w, account: c.Account}
	s2 := w.enclaveWith(off)
	webhooks.Hinted(ctx, s2, c.Mailboxes[1])
	webhooks.Drain(ctx, s2)
	before := len(w.sender.all())
	w.fake.ModelOn(c.Account)
	w.every(time.Minute, 5*time.Minute, s)
	ns = w.sender.notices(t)
	if len(ns) != before+1 || !ns[len(ns)-1].Since.Equal(n.Received.Truncate(time.Minute)) {
		t.Fatalf("not told once after the model came back: %d %d", len(ns), before)
	}
}

// modelOffAtAdmit turns the model off just before the read that admits a
// call.
type modelOffAtAdmit struct {
	webhooks.Connections
	w       *world
	account string
}

func (m *modelOffAtAdmit) Admit(ctx context.Context, c string) (*webhooks.Subscription, webhooks.Permit, error) {
	m.w.fake.ModelOff(m.account)
	return m.Connections.Admit(ctx, c)
}

// K43: schedules across the changes of the clocks, at each run whatever
// the mail; missed runs make one late notice from the coverage boundary,
// whose fetch finds a message of every missed day; a run skipped and a
// notice given up leave the boundary; the first notice covers from the
// connection's minute.
func TestK43SchedulesAndTheirCoverage(t *testing.T) {
	t.Run("across the changes of the clocks", func(t *testing.T) {
		for _, tc := range []struct {
			zone     string
			schedule contracts.WebhookSchedule
			from     time.Time
			want     []time.Time
		}{
			// New York springs forward on 2027-03-14: 02:30 is skipped and
			// runs at 03:00 EDT (07:00 UTC).
			{"America/New_York", contracts.WebhookSchedule{Every: contracts.EveryDay, At: "02:30"}, time.Date(2027, 3, 12, 12, 0, 0, 0, time.UTC), []time.Time{
				time.Date(2027, 3, 13, 7, 30, 0, 0, time.UTC), time.Date(2027, 3, 14, 7, 0, 0, 0, time.UTC), time.Date(2027, 3, 15, 6, 30, 0, 0, time.UTC),
			}},
			// Madrid falls back on 2027-10-31: 02:30 happens twice and runs
			// the first time (00:30 UTC), weekly on Sundays.
			{"Europe/Madrid", contracts.WebhookSchedule{Every: contracts.EveryWeek, On: "sunday", At: "02:30"}, time.Date(2027, 10, 20, 12, 0, 0, 0, time.UTC), []time.Time{
				time.Date(2027, 10, 24, 0, 30, 0, 0, time.UTC), time.Date(2027, 10, 31, 0, 30, 0, 0, time.UTC), time.Date(2027, 11, 7, 1, 30, 0, 0, time.UTC),
			}},
		} {
			t.Run(tc.zone, func(t *testing.T) {
				w := newWorld(t)
				w.clock.set(tc.from)
				s := w.enclave()
				sch := tc.schedule
				c := w.make(s, made{schedule: &sch, zone: tc.zone})
				w.every(5*time.Minute, tc.want[len(tc.want)-1].Sub(tc.from)+time.Hour, s)
				var got []time.Time
				for _, n := range w.sender.notices(t) {
					got = append(got, n.ScheduledAt)
					if n.Late || n.CreatedAt.Sub(n.ScheduledAt) > 5*time.Minute {
						t.Fatalf("late: %+v", n)
					}
				}
				if !slices.EqualFunc(got, tc.want, time.Time.Equal) || len(c.vendor.Taken()) != len(tc.want) {
					t.Fatalf("runs %v, want %v", got, tc.want)
				}
			})
		}
	})

	t.Run("missed runs, a skipped run and a notice given up", func(t *testing.T) {
		w := newWorld(t)
		// Monday 2026-11-23, 07:00 UTC.
		w.clock.set(time.Date(2026, 11, 23, 7, 0, 0, 0, time.UTC))
		made0 := w.clock.now()
		s := w.enclave()
		c := w.make(s, made{schedule: &contracts.WebhookSchedule{Every: contracts.EveryDay, At: "09:00"}})
		w.every(5*time.Minute, 2*time.Hour, s)
		ns := w.sender.notices(t)
		monday := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
		if len(ns) != 1 || !ns[0].Since.Equal(made0) || !ns[0].ScheduledAt.Equal(monday) {
			t.Fatalf("the first: %+v", ns)
		}
		// Down from Monday's run to Friday morning; a message each day.
		var missed []string
		for d := 1; d <= 3; d++ {
			missed = append(missed, w.deliverAt(nil, c, 0, monday.AddDate(0, 0, d).Add(3*time.Hour), false).Message)
		}
		w.clock.set(time.Date(2026, 11, 27, 6, 0, 0, 0, time.UTC))
		// Started again: the repair finds the run its due minute stood for.
		s = w.enclave()
		w.run(s)
		if len(w.sender.all()) != 1 {
			t.Fatal("a due minute older than the first sweep is listed")
		}
		webhooks.Repair(ctx, s)
		w.run(s)
		ns = w.sender.notices(t)
		last := ns[len(ns)-1]
		if len(ns) != 2 || !last.Late || !last.Since.Equal(monday) || !last.ScheduledAt.Equal(time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)) {
			t.Fatalf("the late one: %+v", ns)
		}
		if got := w.fetch(c, last.Since); !slices.Equal(got, missed) {
			t.Fatalf("its fetch finds %v, not every missed day's %v", got, missed)
		}
		// Friday's run skipped (the model off) and Saturday's given up:
		// Sunday's covers from Thursday's, the last taken.
		thursday := last.ScheduledAt
		w.fake.ModelOff(c.Account)
		w.every(5*time.Minute, 4*time.Hour, s)
		w.fake.ModelOn(c.Account)
		if skipped(s, webhooks.SkipModel) != 1 {
			t.Fatal("Friday's run not skipped")
		}
		w.sender.set(func(x *sender) { x.script = slices.Repeat([]webhook.Outcome{webhook.OutcomeStatus}, 13) })
		w.clock.set(time.Date(2026, 11, 28, 7, 0, 0, 0, time.UTC))
		w.every(5*time.Minute, 2*time.Hour, s)
		w.every(time.Hour, webhook.GiveUpAfter, s)
		w.sender.set(func(x *sender) { x.script = nil })
		if skipped(s, webhooks.SkipGivenUp) != 1 {
			t.Fatal("Saturday's not given up")
		}
		w.every(5*time.Minute, 2*time.Hour, s)
		saturday := time.Date(2026, 11, 28, 8, 0, 0, 0, time.UTC)
		var next *contracts.WebhookNotice
		for _, n := range c.vendor.Taken() {
			if n.ScheduledAt.After(saturday) {
				next = &n
				break
			}
		}
		if next == nil || !next.Since.Equal(thursday) || !next.Late {
			t.Fatalf("the next taken covers from %+v, not Thursday's run", next)
		}
	})
}

// K47: no identifier of an account, a mailbox, a message or a connection,
// no address and no zone is in any body the sender is given.
func TestK47NoticesHoldNoIdentifier(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	c := w.make(s, made{newMail: true, schedule: &contracts.WebhookSchedule{Every: contracts.EveryDay, At: "09:30"}, zone: "America/New_York"})
	w.clock.add(time.Minute)
	var secret []string
	for i := range 5 {
		m := w.deliver(s, c, i%2, true)
		secret = append(secret, m.Message)
	}
	w.every(time.Minute, 24*time.Hour, s)
	if len(w.sender.all()) < 2 {
		t.Fatal("too few notices")
	}
	secret = append(secret, c.Connection, c.Account, c.Mailboxes[0], c.Mailboxes[1], "America", "New_York", "@")
	for _, call := range w.sender.all() {
		if n := contains(string(call.Body), secret...); n != "" {
			t.Fatalf("a body holds %q", n)
		}
	}
}

// K48: the log holds kinds and outcomes only, and names a vendor only when
// it is verified: every other is "other".
func TestK48LogsHoldKindsAndOutcomesOnly(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	c := w.make(s, made{newMail: true, host: "verified.example"})
	d := w.make(s, made{newMail: true, host: "unverified.example"})
	w.clock.add(time.Minute)
	w.deliver(s, c, 0, true)
	w.deliver(s, d, 0, true)
	w.sender.set(func(x *sender) { x.script = []webhook.Outcome{webhook.OutcomeNoAnswer} })
	w.every(time.Minute, 5*time.Minute, s)
	w.setInterrupt(func(context.Context, store.Step, string, string) error { return errStopped })
	w.deliver(s, c, 1, true)
	w.every(time.Minute, 2*time.Minute, s)
	webhooks.Repair(ctx, s)
	w.setInterrupt(nil)
	logs := w.logs.String()
	if !strings.Contains(logs, `"vendor":"verified.example"`) || !strings.Contains(logs, `"vendor":"other"`) || !strings.Contains(logs, `"step"`) {
		t.Fatalf("the log: %s", logs)
	}
	for _, x := range []*conn{c, d} {
		if n := contains(logs, x.Connection, x.Account, x.ID, x.URL, x.Mailboxes[0], x.Mailboxes[1], "unverified", "ntc_", "Europe", "stopped"); n != "" {
			t.Fatalf("the log holds %q", n)
		}
	}
}

// K49: an account removed: its connections' entries and its mailboxes'
// index entries are gone after the next repair.
func TestK49AnAccountRemovedLeavesNothing(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	c := w.make(s, made{newMail: true, schedule: &contracts.WebhookSchedule{Every: contracts.EveryDay, At: "09:30"}})
	keep := w.make(s, made{newMail: true})
	w.run(s)
	w.fake.Remove(c.Connection)
	webhooks.Repair(ctx, s)
	w.run(s)
	for _, e := range w.entries(webhooks.PartitionState) {
		if e.Key == c.Connection {
			t.Fatal("its state stays")
		}
	}
	for _, m := range c.Mailboxes {
		if len(w.entries(webhooks.PartitionMailbox+m)) != 0 {
			t.Fatal("its index stays")
		}
	}
	if due := w.dueEntries(time.Hour, 48*time.Hour); len(due[c.Connection]) != 0 || len(w.entries(webhooks.PartitionState)) != 1 {
		t.Fatalf("its due entry stays, or another's went: %v", due)
	}
	_ = keep
}

// K50: /status counts every outcome and every reason from the start, at
// zero, and what this enclave did since.
func TestK50StatusCountsFromZero(t *testing.T) {
	w := newWorld(t)
	s := w.enclave(webhooks.WithKeysURL("https://api.permaily.test" + webhook.KeysPath))
	st := s.Status()
	if len(st.Outcomes) != 8 || len(st.Skipped) != 8 || !st.Since.Equal(start) || st.KeysURL != "https://api.permaily.test/.well-known/permaily-webhook-keys" {
		t.Fatalf("%+v", st)
	}
	for k, v := range st.Outcomes {
		if v != 0 {
			t.Fatal(k)
		}
	}
	for k, v := range st.Skipped {
		if v != 0 {
			t.Fatal(k)
		}
	}
	if st.Signature != "standard-webhooks-v2a" || st.SpecRevision != webhook.SpecRevision || len(st.Keys) != 1 || st.Keys[0].ID != webhook.KeyID ||
		st.NoticeHoldsMail || !st.SameHost || st.Redirects != "none" || st.Transport != "h2" || st.NewMail != "after_rules" ||
		st.RevocationSeconds != 5 || st.MinIntervalSeconds != 60 || st.TimeoutSeconds != 10 || st.GiveUpSeconds != 72*3600 ||
		st.DedupSeconds != 72*3600+600 || st.HostInFlight != 2 || st.HostAttemptsPerMinute != 60 || !slices.Equal(st.Schedules, []string{"day", "week"}) {
		t.Fatalf("%+v", st)
	}
	set, err := s.KeySet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := webhook.ParseKeySet(set)
	if err != nil || !keys[webhook.KeyID].Equal(publicKey()) {
		t.Fatal("the key set is not the signing key's")
	}
	c := w.make(s, made{newMail: true})
	w.clock.add(time.Minute)
	w.deliver(s, c, 0, true)
	w.run(s)
	if outcomes(s, webhook.OutcomeSent) != 1 {
		t.Fatal("not counted")
	}
}
