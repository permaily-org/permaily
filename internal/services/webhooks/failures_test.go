package webhooks_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webhook"
)

// scenario is most of what the queue does: two connections (new mail, and
// a schedule), arrivals, retries, a mailbox taken out, a revocation, the
// repair, the person's page and the key set.
func scenario(w *world, s *webhooks.Service) (*conn, *conn) {
	c := w.make(s, made{newMail: true})
	d := w.make(s, made{newMail: true, schedule: &contracts.WebhookSchedule{Every: contracts.EveryDay, At: "09:15"}})
	gone := w.make(s, made{newMail: true})
	w.clock.add(time.Minute)
	w.deliver(s, c, 0, true)
	w.deliver(s, d, 1, true)
	w.sender.set(func(x *sender) { x.script = []webhook.Outcome{webhook.OutcomeNoAnswer} })
	w.run(s)
	w.deliver(s, c, 1, true)
	w.fake.DropMailbox(d.Connection)
	w.fake.Revoke(gone.Connection)
	s.Ended(gone.Connection)
	w.every(time.Minute, 20*time.Minute, s)
	webhooks.Repair(ctx, s)
	_, _ = s.States(ctx, c.Connection)
	_, _ = s.KeySet(ctx)
	return c, d
}

// Every request to the store failing in turn, one at a time, along the
// scenario: never a call to a URL that is not the connection's, never a
// body that does not open, never a panic; and once the store answers again,
// the repair and the workers tell what is left to tell.
func TestEveryFailureOfTheStoreIsAnErrorNeverACall(t *testing.T) {
	w0 := newWorld(t)
	var count int
	var mu sync.Mutex
	w0.setInterrupt(func(context.Context, store.Step, string, string) error {
		mu.Lock()
		count++
		mu.Unlock()
		return nil
	})
	scenario(w0, w0.enclave())
	total := count
	if total < 100 {
		t.Fatalf("only %d requests", total)
	}
	for n := 1; n <= total; n += max(1, total/400) {
		w := newWorld(t)
		w.tolerate = true
		k := 0
		w.setInterrupt(func(_ context.Context, step store.Step, _, _ string) error {
			mu.Lock()
			defer mu.Unlock()
			if k++; k == n {
				return fmt.Errorf("request %d (%s) failed", n, step)
			}
			return nil
		})
		s := w.enclave()
		c, _ := scenario(w, s)
		w.setInterrupt(nil)
		w.clock.add(webhook.RepairEvery)
		webhooks.Repair(ctx, s)
		w.every(time.Minute, 30*time.Minute, s)
		for _, call := range w.sender.all() {
			if !strings.HasPrefix(call.URL, "https://trip-app.example/hooks/permaily/") {
				t.Fatalf("request %d: a call to %s", n, call.URL)
			}
		}
		w.sender.notices(t)
		if len(c.vendor.Taken()) == 0 {
			t.Fatalf("request %d failing: nothing told after the store came back", n)
		}
	}
}

// faulty is the connections with a test's failures in front of them.
type faulty struct {
	webhooks.Connections
	mu    sync.Mutex
	fails map[string][]error // by method, in turn
}

func (f *faulty) next(method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if errs := f.fails[method]; len(errs) > 0 {
		f.fails[method] = errs[1:]
		return errs[0]
	}
	return nil
}

func (f *faulty) set(method string, errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fails[method] = errs
}

func (f *faulty) Subscription(ctx context.Context, c string) (*webhooks.Subscription, error) {
	if err := f.next("Subscription"); err != nil {
		return nil, err
	}
	return f.Connections.Subscription(ctx, c)
}

func (f *faulty) Admit(ctx context.Context, c string) (*webhooks.Subscription, webhooks.Permit, error) {
	if err := f.next("Admit"); err != nil {
		return nil, nil, err
	}
	return f.Connections.Admit(ctx, c)
}

func (f *faulty) Pending(ctx context.Context, c string, p webhooks.Progress) (*webhooks.Evaluation, error) {
	if err := f.next("Pending"); err != nil {
		return nil, err
	}
	return f.Connections.Pending(ctx, c, p)
}

func (f *faulty) Recheck(ctx context.Context, c string, m webhooks.Match) (bool, error) {
	if err := f.next("Recheck"); err != nil {
		return false, err
	}
	return f.Connections.Recheck(ctx, c, m)
}

func (f *faulty) Webhooks(ctx context.Context, after string, limit int) ([]string, error) {
	if err := f.next("Webhooks"); err != nil {
		return nil, err
	}
	return f.Connections.Webhooks(ctx, after, limit)
}

func (f *faulty) Stop(ctx context.Context, c string) error {
	if err := f.next("Stop"); err != nil {
		return err
	}
	return f.Connections.Stop(ctx, c)
}

var errRead = errors.New("connections: the record cannot be read now")

// What the connections answer that is not a webhook that calls: an error
// is retried a minute later and calls nothing; ErrNoWebhook at any read
// drops the connection's entries; the vendor's 410 not recorded is called
// again, and recorded then.
func TestEveryAnswerOfTheConnections(t *testing.T) {
	for _, tc := range []struct {
		method string
		errs   []error
		told   bool
	}{
		{"Subscription", []error{errRead, webhooks.ErrRulesUnavailable}, true},
		{"Pending", []error{errRead}, true},
		{"Pending", []error{webhooks.ErrNoWebhook}, false},
		{"Recheck", []error{webhooks.ErrNoWebhook}, false},
		{"Recheck", []error{errRead}, true},
		{"Admit", []error{errRead}, true},
		{"Admit", []error{webhooks.ErrNoWebhook}, false},
	} {
		t.Run(tc.method+" "+tc.errs[0].Error(), func(t *testing.T) {
			w := newWorld(t)
			f := &faulty{Connections: w.fake, fails: map[string][]error{}}
			s := w.enclaveWith(f)
			c := w.make(s, made{newMail: true})
			w.clock.add(time.Minute)
			w.deliver(s, c, 0, true)
			f.set(tc.method, tc.errs...)
			w.run(s)
			if len(w.sender.all()) != 0 {
				t.Fatal("called despite the failure")
			}
			w.every(time.Minute, 5*time.Minute, s)
			if told := len(c.vendor.Taken()) == 1; told != tc.told {
				t.Fatalf("told %v", told)
			}
		})
	}

	t.Run("Webhooks failing: the repair drops nothing, and runs again", func(t *testing.T) {
		w := newWorld(t)
		f := &faulty{Connections: w.fake, fails: map[string][]error{}}
		s := w.enclaveWith(f)
		w.make(s, made{newMail: true})
		f.set("Webhooks", errRead)
		webhooks.Repair(ctx, s)
		if len(w.entries(webhooks.PartitionState)) != 1 {
			t.Fatal("dropped on a failed listing")
		}
		f.set("Subscription", errRead)
		webhooks.Repair(ctx, s)
		if len(w.entries(webhooks.PartitionState)) != 1 {
			t.Fatal("dropped on a failed read")
		}
	})

	t.Run("Stop failing", func(t *testing.T) {
		w := newWorld(t)
		f := &faulty{Connections: w.fake, fails: map[string][]error{}}
		s := w.enclaveWith(f)
		c := w.make(s, made{newMail: true})
		w.clock.add(time.Minute)
		w.deliver(s, c, 0, true)
		c.vendor.Status = 410
		f.set("Stop", errRead)
		w.run(s)
		if len(w.fake.Stopped()) != 0 {
			t.Fatal("stopped")
		}
		w.every(time.Minute, 5*time.Minute, s)
		if len(w.fake.Stopped()) != 1 || len(w.sender.all()) != 2 {
			t.Fatalf("the 410 not recorded on the retry: %d calls", len(w.sender.all()))
		}
	})
}

// shortKeys derives keys of the wrong size for the purposes it names.
type shortKeys struct{ short string }

func (k shortKeys) Derive(_ context.Context, purpose string) ([]byte, error) {
	h := sha256.Sum256([]byte(purpose))
	if purpose == k.short {
		return h[:16], nil
	}
	return h[:], nil
}

// Keys that cannot be derived, or are not keys, and randomness that fails:
// nothing is kept, signed or called, every failure an error.
func TestKeysAndRandomnessThatFail(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	c := w.make(s, made{newMail: true})
	w.clock.add(time.Minute)
	w.deliver(s, c, 0, true)
	w.keys.fail = errRead
	other := w.enclave()
	webhooks.Hints(ctx, other)
	webhooks.Drain(ctx, other)
	webhooks.Hinted(ctx, other, c.Mailboxes[0])
	webhooks.Repair(ctx, other)
	other.Ended(c.Connection)
	webhooks.Hints(ctx, other)
	if err := other.Subscribe(ctx, c.Subscription); err == nil {
		t.Fatal("subscribed without keys")
	}
	if _, err := other.States(ctx, c.Connection); err == nil {
		t.Fatal("states without keys")
	}
	if _, err := other.KeySet(ctx); err == nil {
		t.Fatal("a key set without keys")
	}
	if st := other.Status(); len(st.Keys) != 0 {
		t.Fatal("keys shown that cannot be derived")
	}
	w.keys.fail = nil

	for _, short := range []string{webhooks.PurposeSeal, webhook.KeyPurpose} {
		x, err := webhooks.New(webhooks.Config{Entries: w.mem, Keys: shortKeys{short: short}, Connections: w.fake, Sender: w.sender})
		if err != nil {
			t.Fatal(err)
		}
		if err := x.Subscribe(ctx, c.Subscription); err == nil {
			t.Fatalf("subscribed with a key of %s that is not one", short)
		}
	}
	seedFails := &keysFailingOn{purpose: webhook.KeyPurpose}
	if _, err := (mustNew(t, w, seedFails)).KeySet(ctx); err == nil {
		t.Fatal("a key set without the signing key")
	}

	if _, err := webhooks.New(webhooks.Config{Entries: w.mem, Keys: w.keys, Connections: w.fake, Sender: w.sender, Random: func([]byte) error { return errRead }}); err == nil {
		t.Fatal("made without randomness")
	}
	calls := 0
	r, err := webhooks.New(webhooks.Config{Entries: w.mem, Keys: w.keys, Connections: w.fake, Sender: w.sender, Now: w.clock.now, Random: func(b []byte) error {
		if calls++; calls > 1 {
			return errRead
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	webhooks.Hinted(ctx, r, c.Mailboxes[0])
	webhooks.Drain(ctx, r)
	if len(w.sender.all()) != 0 {
		t.Fatal("a notice with no identifier")
	}
	w.clock.add(time.Minute)
	w.run(s)
	if len(c.vendor.Taken()) != 1 {
		t.Fatal("not told once the keys are back")
	}
	for _, cfg := range []webhooks.Config{{}, {Entries: w.mem}, {Entries: w.mem, Keys: w.keys}, {Entries: w.mem, Keys: w.keys, Connections: w.fake}} {
		if _, err := webhooks.New(cfg); !errors.Is(err, webhooks.ErrConfig) {
			t.Fatal("made without what it needs")
		}
	}
}

type keysFailingOn struct{ purpose string }

func (k *keysFailingOn) Derive(_ context.Context, purpose string) ([]byte, error) {
	if purpose == k.purpose {
		return nil, errRead
	}
	h := sha256.Sum256([]byte(purpose))
	return h[:], nil
}

func mustNew(t *testing.T, w *world, k webhooks.Keys) *webhooks.Service {
	t.Helper()
	s, err := webhooks.New(webhooks.Config{Entries: w.mem, Keys: k, Connections: w.fake, Sender: w.sender})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A subscription the notice cannot be made of (no client, an endpoint with
// a dot), a zone that is not one, a mailbox taken out, matches found under
// two bases: nothing wrong is called, and what still leaves is told.
func TestCornersOfTheNoticeAndTheConnection(t *testing.T) {
	t.Run("no client, an endpoint with a dot", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		a := w.make(s, made{newMail: true, clientID: "-"})
		b := w.make(s, made{newMail: true, id: "sub_with.dot"})
		bad := a.Subscription
		bad.ClientID = ""
		w.fake.Make(bad)
		w.clock.add(time.Minute)
		w.deliver(s, a, 0, true)
		w.deliver(s, b, 0, true)
		w.every(time.Minute, 3*time.Minute, s)
		if len(w.sender.all()) != 0 || skipped(s, webhooks.SkipChanged) == 0 {
			t.Fatal("a notice that cannot be made was called")
		}
	})

	t.Run("a zone that is not one", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		c := w.make(s, made{schedule: &contracts.WebhookSchedule{Every: contracts.EveryDay, At: "09:15"}})
		bad := c.Subscription
		bad.TimeZone = "Mars/Olympus"
		w.fake.Make(bad)
		w.every(5*time.Minute, 2*time.Hour, s)
		webhooks.Repair(ctx, s)
		if len(w.sender.all()) != 0 || len(w.entries(webhooks.PartitionState)) != 0 {
			t.Fatal("a schedule in no zone ran, or kept its entries")
		}
	})

	t.Run("a mailbox taken out, and matches under two bases", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		c := w.make(s, made{newMail: true})
		w.clock.add(time.Minute)
		w.deliver(s, c, 0, true)
		w.sender.set(func(x *sender) { x.script = []webhook.Outcome{webhook.OutcomeNoAnswer} })
		w.run(s)
		first := w.deliver(s, c, 0, true)
		w.run(s)
		w.fake.DropMailbox(c.Connection)
		w.clock.add(time.Second)
		second := w.deliver(s, c, 0, true)
		w.run(s)
		if len(w.entries(webhooks.PartitionMailbox+c.Mailboxes[1])) != 0 || len(w.entries(webhooks.PartitionMailbox+c.Mailboxes[0])) != 1 {
			t.Fatal("the index not kept to the mailboxes reached")
		}
		w.every(time.Minute, 5*time.Minute, s)
		if skipped(s, webhooks.SkipChanged) == 0 {
			t.Fatal("matches of two bases merged")
		}
		taken := c.vendor.Taken()
		last := taken[len(taken)-1]
		if got := w.fetch(c, last.Since); !contains2(got, first.Message, second.Message) {
			t.Fatalf("what leaves under the new basis not told: %v", got)
		}
	})

	t.Run("the index failing when a mailbox is added", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		c := w.make(s, made{newMail: true})
		more := c.Subscription
		more.Mailboxes = append(more.Mailboxes, "mbx_added")
		w.fake.Make(more)
		w.setInterrupt(func(_ context.Context, step store.Step, partition, _ string) error {
			if step == store.StepEntry && partition == webhooks.PartitionMailbox+"mbx_added" {
				return errStopped
			}
			return nil
		})
		w.clock.add(time.Minute)
		w.deliver(s, c, 0, true)
		w.run(s)
		w.setInterrupt(nil)
		w.clock.add(time.Minute)
		w.deliver(nil, c, 0, true)
		w.run(s)
		webhooks.Hinted(ctx, s, c.Mailboxes[0])
		w.every(time.Minute, 3*time.Minute, s)
		if len(w.entries(webhooks.PartitionMailbox+"mbx_added")) != 1 {
			t.Fatal("the added mailbox not indexed once the store answers")
		}
	})
}

func contains2(got []string, want ...string) bool {
	for _, x := range want {
		found := false
		for _, g := range got {
			found = found || g == x
		}
		if !found {
			return false
		}
	}
	return true
}

// More than a page: a mailbox's index, the listing of webhooks and a
// shard's states are read a page at a time; hints past the buffer are lost
// (the repair finds their mail), never waited for; an index entry that does
// not open is skipped.
func TestPagesAndBuffers(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	shared := []string{"mbx_shared", "mbx_other"}
	for range webhooks.RepairPage + 1 {
		w.make(s, made{newMail: true, mailboxes: shared})
	}
	for range 16 * webhooks.RepairPage {
		w.make(s, made{schedule: &contracts.WebhookSchedule{Every: contracts.EveryDay, At: "09:15"}})
	}
	w.mem.SetEntry(store.Entry{Partition: webhooks.PartitionMailbox + "mbx_shared", Key: "con_forged", Value: []byte(`{"sealed":"AAAA"}`)})
	w.clock.add(time.Minute)
	webhooks.Hinted(ctx, s, "mbx_shared")
	due := 0
	for _, keys := range w.dueEntries(0, 0) {
		due += len(keys)
	}
	if due != webhooks.RepairPage+1 {
		t.Fatalf("%d due of a mailbox's %d", due, webhooks.RepairPage+1)
	}
	webhooks.Repair(ctx, s)
	if n := len(w.entries(webhooks.PartitionState)); n != 17*webhooks.RepairPage+1 {
		t.Fatalf("%d states after the repair", n)
	}
	w.fake.Remove("con_00000000000000000000000002")
	w.clock.add(webhook.RepairEvery)
	webhooks.Repair(ctx, s)
	if n := len(w.entries(webhooks.PartitionState)); n != 17*webhooks.RepairPage {
		t.Fatalf("%d states after a removal", n)
	}
	for range 1100 {
		s.Arrived("mbx_shared", w.clock.now())
		s.Ended("con_none")
	}
	webhooks.Hints(ctx, s)
}

// Run: the hints, a pass every passEvery, a dispatch when a slot frees,
// the repair; it ends with its context, waiting for its calls.
func TestRunWorksUntilItsContextEnds(t *testing.T) {
	defer webhooks.SetTimings(time.Millisecond, time.Millisecond)()
	w := newWorld(t)
	s := w.enclave()
	c := w.make(s, made{newMail: true})
	w.clock.add(time.Minute)
	called := make(chan struct{}, 8)
	w.sender.set(func(x *sender) { x.after = func(webhook.Call) { called <- struct{}{} } })
	w.deliver(s, c, 0, true)
	webhooks.Wake(s)
	repaired := make(chan struct{}, 1)
	w.setInterrupt(func(_ context.Context, step store.Step, partition, _ string) error {
		if step == store.StepEntry && partition == "webhooks#repair" {
			select {
			case repaired <- struct{}{}:
			default:
			}
		}
		return nil
	})
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		s.Run(rctx)
		close(done)
	}()
	<-called
	<-repaired
	for webhooks.Busy(s) {
		runtime.Gosched()
	}
	cancel()
	<-done
	if len(c.vendor.Taken()) != 1 {
		t.Fatal("not told")
	}
}

// The scheduler's corners through Ready: nothing is lost when a host's
// accounts take turns.
func TestAHostsAccountsTakeTurns(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	var mu sync.Mutex
	var order []string
	w.sender.set(func(x *sender) {
		x.before = func(c webhook.Call) {
			mu.Lock()
			defer mu.Unlock()
			order = append(order, c.URL)
		}
	})
	daily := &contracts.WebhookSchedule{Every: contracts.EveryDay, At: "09:15"}
	var mine []*conn
	for i := range 4 {
		mine = append(mine, w.make(s, made{schedule: daily, account: fmt.Sprintf("acc_%d", i%2)}))
	}
	w.clock.add(15 * time.Minute)
	webhooks.Scan(ctx, s)
	for webhooks.Dispatch(ctx, s) > 0 {
		webhooks.Wait(s)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != len(mine) {
		t.Fatalf("%d calls", len(order))
	}
}

// The last corners of the store: a due entry there already that cannot be
// read again, a state that does not open on the person's page, the
// repair's own writes failing, a due entry that cannot be deleted with its
// connection, a connection gone between the listing and its read.
func TestTheLastCornersOfTheStore(t *testing.T) {
	w := newWorld(t)
	f := &faulty{Connections: w.fake, fails: map[string][]error{}}
	s := w.enclaveWith(f)
	c := w.make(s, made{newMail: true})
	failing := func(step store.Step, prefix string) {
		w.setInterrupt(func(_ context.Context, st store.Step, partition, _ string) error {
			if st == step && strings.HasPrefix(partition, prefix) {
				return errStopped
			}
			return nil
		})
	}
	failing(store.StepRead, webhooks.PartitionDue)
	if err := s.Subscribe(ctx, c.Subscription); err == nil {
		t.Fatal("a due entry not made again")
	}
	w.setInterrupt(nil)

	forged := w.stateOf(c.Connection)
	forged.Value = []byte(`{"sealed":"AAAA"}`)
	w.mem.SetEntry(forged)
	if _, err := s.States(ctx, c.Connection); err == nil {
		t.Fatal("a state that does not open shown")
	}
	// The repair makes it again, and its write of the state fails.
	failing(store.StepEntry, webhooks.PartitionState)
	webhooks.Repair(ctx, s)
	w.setInterrupt(nil)
	if _, err := s.States(ctx, c.Connection); err == nil {
		t.Fatal("made again although its write failed")
	}
	// The repair's lease cannot be given back: the next one waits for it.
	w.clock.add(webhook.RepairEvery)
	w.setInterrupt(func(_ context.Context, st store.Step, partition, _ string) error {
		if st == store.StepEntry && partition == "webhooks#repair" {
			w.setInterrupt(func(_ context.Context, st store.Step, partition, _ string) error {
				if st == store.StepEntry && partition == "webhooks#repair" {
					return errStopped
				}
				return nil
			})
		}
		return nil
	})
	webhooks.Repair(ctx, s)
	w.setInterrupt(nil)
	if _, err := s.States(ctx, c.Connection); err != nil {
		t.Fatal("not made again", err)
	}
	// Gone at the listing's read, there again at the sweep's: it stays.
	w.clock.add(webhook.RepairEvery + 20*time.Minute)
	f.set("Subscription", webhooks.ErrNoWebhook)
	webhooks.Repair(ctx, s)
	if len(w.entries(webhooks.PartitionState)) != 1 {
		t.Fatal("dropped on the listing's read alone")
	}
	// Gone between the listing and its read, and at the sweep's.
	w.clock.add(webhook.RepairEvery + 20*time.Minute)
	f.set("Subscription", webhooks.ErrNoWebhook, webhooks.ErrNoWebhook)
	webhooks.Repair(ctx, s)
	if len(w.entries(webhooks.PartitionState)) != 0 {
		t.Fatal("its entries stay")
	}

	// A connection revoked whose due entry cannot be deleted.
	d := w.make(s, made{schedule: &contracts.WebhookSchedule{Every: contracts.EveryDay, At: "09:15"}})
	w.run(s)
	w.fake.Revoke(d.Connection)
	w.clock.add(webhook.RepairEvery)
	failing(store.StepDeleteEntry, webhooks.PartitionDue)
	webhooks.Repair(ctx, s)
	w.setInterrupt(nil)
	for _, e := range w.entries(webhooks.PartitionState) {
		if e.Key == d.Connection {
			t.Fatal("its state stays")
		}
	}
}
