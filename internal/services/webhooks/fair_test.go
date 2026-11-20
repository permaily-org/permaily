package webhooks

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webhook"
)

type purposeKeys struct{}

func (purposeKeys) Derive(_ context.Context, purpose string) ([]byte, error) {
	h := sha256.Sum256([]byte(purpose))
	return h[:], nil
}

// noConnections and noSender are never reached by the simulation, which
// stands for the touches itself.
type noConnections struct{ Connections }
type noSender struct{ webhook.Sender }

// FairWaitBound is the wait ADR 0039's "Fair shares" states, under any
// load, for a host's notice found due: the next pass's listing, the
// enclave's minute of attempts if it is spent, and its turn among the
// hosts with notices ready, each turn starting within one call's timeout
// of a slot's freeing.
func fairWaitBound(hostsReady int) time.Duration {
	turns := math.Ceil(float64(hostsReady) / webhook.InFlightMax)
	return passEvery + time.Minute + time.Duration(turns)*webhook.AttemptTimeout
}

// K45 and re-review N3, under overload: ten fast hosts spend the enclave's
// 600 attempts of the minute in its first second; thirty hostile hosts whose receivers
// take their ten seconds for every call keep backlogs past every ready
// queue (64 a host, 1,024 in all), replenished faster than they are
// served; then a healthy host's notice becomes due. It is called within
// the stated bound, every ready queue stays within its bounds the whole
// time, no host has more than two calls at once or 60 a minute, and every
// backlog keeps being served (none is dropped, none starved).
func TestK45AHealthyHostIsCalledWithinTheBoundUnderOverload(t *testing.T) {
	now := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	mem := store.NewMemory()
	mem.Now = clock
	s, err := New(Config{Entries: mem, Keys: purposeKeys{}, Connections: noConnections{}, Sender: noSender{}, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	seal, _, err := s.keys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	due := func(host string) {
		n++
		c := fmt.Sprintf("con_%s_%06d", host, n)
		if err := s.markDue(context.Background(), seal, c, route{Host: host, Account: "acc_" + c}, now); err != nil {
			t.Fatal(err)
		}
	}
	duration := map[string]time.Duration{}
	for i := range 10 {
		h := fmt.Sprintf("fast%02d.example", i)
		duration[h] = 0
		for range 100 {
			due(h)
		}
	}
	slow := func() {
		for i := range 30 {
			h := fmt.Sprintf("slow%02d.example", i)
			duration[h] = webhook.AttemptTimeout
			for range 100 {
				due(h)
			}
		}
	}
	type running struct {
		it  item
		end time.Time
	}
	var inFlight []running
	served := map[string]int{}
	perHost := map[string][]time.Time{}
	const healthy = "healthy.example"
	var healthyDue, healthyCalled time.Time
	for step := 0; step < 600; step++ {
		// Calls that end: their due entries gone, as a touch deletes them;
		// a hostile host's backlog replenished, two for one.
		sort.Slice(inFlight, func(i, j int) bool { return inFlight[i].end.Before(inFlight[j].end) })
		for len(inFlight) > 0 && !inFlight[0].end.After(now) {
			r := inFlight[0]
			inFlight = inFlight[1:]
			s.mu.Lock()
			s.sched.done(r.it, true)
			s.mu.Unlock()
			s.unlist(context.Background(), r.it)
			if r.it.host != healthy {
				due(r.it.host)
				due(r.it.host)
			}
		}
		if step == 1 {
			// The fast hosts spent the minute's attempts; the hostile
			// backlogs come.
			slow()
		}
		if step == 20 {
			if len(s.sched.starts) != webhook.AttemptsPerMinute {
				t.Fatalf("the minute's attempts not spent: %d", len(s.sched.starts))
			}
			healthyDue = now
			due(healthy)
		}
		if step%int(passEvery/time.Second) == 0 {
			s.scanDue(context.Background())
		}
		total, most := s.sched.ready()
		if total > webhook.ReadyMax || most > webhook.HostReadyMax {
			t.Fatalf("ready %d, %d of one host", total, most)
		}
		for {
			s.mu.Lock()
			it, ok := s.sched.pick(now)
			s.mu.Unlock()
			if !ok {
				break
			}
			if it.host == healthy && healthyCalled.IsZero() {
				healthyCalled = now
			}
			served[it.host]++
			perHost[it.host] = append(within(perHost[it.host], now), now)
			if len(perHost[it.host]) > webhook.HostAttemptsPerMinute {
				t.Fatalf("%s: more than 60 calls a minute", it.host)
			}
			if duration[it.host] == 0 {
				// An answer at once: its slot is free again in this second.
				s.sched.done(it, true)
				s.unlist(context.Background(), it)
				due(it.host)
				continue
			}
			inFlight = append(inFlight, running{it: it, end: now.Add(duration[it.host])})
		}
		hosts := map[string]int{}
		for _, r := range inFlight {
			if hosts[r.it.host]++; hosts[r.it.host] > webhook.HostInFlightMax {
				t.Fatalf("%s holds more than two slots", r.it.host)
			}
		}
		if len(inFlight) > webhook.InFlightMax {
			t.Fatal("more than sixteen calls at once")
		}
		now = now.Add(time.Second)
	}
	if healthyCalled.IsZero() {
		t.Fatal("the healthy host was never called")
	}
	bound := fairWaitBound(len(duration) + 1)
	if wait := healthyCalled.Sub(healthyDue); wait > bound {
		t.Fatalf("the healthy host waited %v, past the bound %v", wait, bound)
	}
	for h := range duration {
		if served[h] == 0 {
			t.Fatalf("%s never served", h)
		}
	}
	t.Logf("the healthy host waited %v (bound %v)", healthyCalled.Sub(healthyDue), bound)
}

// The scheduler's own corners: an item given back after its minute ended,
// a host whose queue is full, eviction refused when every host holds one.
func TestTheSchedulersCorners(t *testing.T) {
	now := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	q := newScheduler()
	if !q.admit(item{connection: "a", host: "h", account: "x"}) || q.admit(item{connection: "a", host: "h", account: "x"}) {
		t.Fatal("admitted twice")
	}
	it, ok := q.pick(now)
	if !ok {
		t.Fatal("not picked")
	}
	q.done(it, false)
	if len(q.starts) != 0 {
		t.Fatal("a reservation not given back")
	}
	if !q.admit(item{connection: "b", host: "h", account: "x"}) {
		t.Fatal()
	}
	it, _ = q.pick(now)
	now = now.Add(2 * time.Minute)
	q.pick(now)
	q.done(it, false)
	for i := range webhook.HostReadyMax {
		q.admit(item{connection: fmt.Sprint("full", i), host: "full", account: fmt.Sprint(i % 3)})
	}
	if q.admit(item{connection: "past", host: "full", account: "x"}) {
		t.Fatal("past a host's bound")
	}
	// The host holding the most gives its newest, of its account holding
	// the most; an account emptied leaves the host's turn.
	q = newScheduler()
	for i := range webhook.ReadyMax - 3 {
		q.admit(item{connection: fmt.Sprint("one", i), host: fmt.Sprint("host", i), account: "x"})
	}
	q.admit(item{connection: "m1", host: "most", account: "a"})
	q.admit(item{connection: "m2", host: "most", account: "b"})
	q.admit(item{connection: "m3", host: "most", account: "b"})
	q.hosts["most"].next = 2
	if !q.admit(item{connection: "new", host: "new", account: "x"}) || q.queued["m3"] || q.hosts["most"].n != 2 {
		t.Fatal("not the newest of the account holding the most")
	}
	if !q.admit(item{connection: "new2", host: "new2", account: "x"}) || q.queued["m1"] || len(q.hosts["most"].order) != 1 || q.hosts["most"].next != 1 {
		t.Fatalf("an emptied account kept its turn: %v %d", q.hosts["most"].order, q.hosts["most"].next)
	}
	q = newScheduler()
	for i := range webhook.ReadyMax {
		q.admit(item{connection: fmt.Sprint("one", i), host: fmt.Sprint("host", i), account: "x"})
	}
	if q.admit(item{connection: "new", host: "new", account: "x"}) {
		t.Fatal("evicted the only item of a host")
	}
}
