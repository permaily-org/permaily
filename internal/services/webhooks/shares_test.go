package webhooks_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/webhook"
)

// K45 through the service: sixteen calls at once in an enclave and two to
// a host; a hostile host whose receiver holds every call holds two slots,
// and a healthy host's notice due meanwhile is called in the same pass.
func TestK45SixteenAtOnceTwoAHost(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	daily := &contracts.WebhookSchedule{Every: contracts.EveryDay, At: "09:15"}
	for i := range 10 {
		for range 3 {
			w.make(s, made{schedule: daily, host: fmt.Sprintf("hostile%d.example", i)})
		}
	}
	healthy := w.make(s, made{schedule: daily, host: "healthy.example"})
	release := make(chan struct{})
	entered := make(chan string, 64)
	var mu sync.Mutex
	at, most, hostMost := map[string]int{}, 0, 0
	w.sender.set(func(x *sender) {
		x.before = func(c webhook.Call) {
			host := hostOf(c.URL)
			mu.Lock()
			at[host]++
			n := 0
			for _, k := range at {
				n += k
			}
			most, hostMost = max(most, n), max(hostMost, at[host])
			mu.Unlock()
			entered <- c.URL
			if c.URL != healthy.URL {
				<-release
			}
			mu.Lock()
			at[host]--
			mu.Unlock()
		}
	})
	w.clock.add(15 * time.Minute)
	webhooks.Scan(ctx, s)
	webhooks.Dispatch(ctx, s)
	sawHealthy := false
	for range webhook.InFlightMax {
		if <-entered == healthy.URL {
			sawHealthy = true
		}
	}
	if !sawHealthy {
		t.Fatal("the healthy host waits behind the hostile ones")
	}
	mu.Lock()
	m, hm := most, hostMost
	mu.Unlock()
	close(release)
	webhooks.Wait(s)
	if m > webhook.InFlightMax || m < webhook.InFlightMax-1 || hm != webhook.HostInFlightMax {
		t.Fatalf("%d at once, %d to one host", m, hm)
	}
	w.run(s)
	if len(w.sender.all()) != 31 || len(healthy.vendor.Taken()) != 1 {
		t.Fatalf("%d calls, not one each", len(w.sender.all()))
	}
}

// K45 through the service: 600 calls started a minute in an enclave, 60 to
// a host; the ready queues never above their bounds; what is past them is
// called later, every notice once, none dropped.
func TestK45SixHundredAMinuteAndTheReadyBounds(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	daily := &contracts.WebhookSchedule{Every: contracts.EveryDay, At: "09:15"}
	const hosts, each = 20, 70
	for i := range hosts {
		for range each {
			w.make(s, made{schedule: daily, host: fmt.Sprintf("h%02d.example", i)})
		}
	}
	w.clock.add(15 * time.Minute)
	// One pass reads at most twice ReadyMax entries; the next goes on.
	webhooks.Scan(ctx, s)
	webhooks.Scan(ctx, s)
	if total, most := webhooks.Ready(s); total != webhook.ReadyMax || most != webhook.HostReadyMax {
		t.Fatalf("ready %d, %d of one host", total, most)
	}
	webhooks.Drain(ctx, s)
	perHost := map[string]int{}
	for _, c := range w.sender.all() {
		perHost[hostOf(c.URL)]++
	}
	if n := len(w.sender.all()); n != webhook.AttemptsPerMinute {
		t.Fatalf("%d calls in the first minute", n)
	}
	for h, n := range perHost {
		if n > webhook.HostAttemptsPerMinute {
			t.Fatalf("%s: %d calls in a minute", h, n)
		}
	}
	for range 4 {
		w.clock.add(time.Minute)
		w.run(s)
		if total, most := webhooks.Ready(s); total > webhook.ReadyMax || most > webhook.HostReadyMax {
			t.Fatal("past the ready bounds")
		}
	}
	seen := map[string]int{}
	for _, c := range w.sender.all() {
		seen[c.ID]++
	}
	if len(seen) != hosts*each || len(w.sender.all()) != hosts*each {
		t.Fatalf("%d notices of %d, %d calls", len(seen), hosts*each, len(w.sender.all()))
	}
}

// blockingPending holds every evaluation until released, counting how many
// run at once, in all and per account.
type blockingPending struct {
	webhooks.Connections
	release chan struct{}
	entered chan struct{}

	mu       sync.Mutex
	accounts map[string]string // connection: account
	now      map[string]int
	all      int
	most     int
	acctMost int
}

func (b *blockingPending) Pending(ctx context.Context, c string, after webhooks.Progress) (*webhooks.Evaluation, error) {
	b.mu.Lock()
	a := b.accounts[c]
	b.now[a]++
	b.all++
	b.most, b.acctMost = max(b.most, b.all), max(b.acctMost, b.now[a])
	b.mu.Unlock()
	b.entered <- struct{}{}
	<-b.release
	b.mu.Lock()
	b.now[a]--
	b.all--
	b.mu.Unlock()
	return b.Connections.Pending(ctx, c, after)
}

// K45's evaluations: four at once in an enclave, one an account; the
// others wait for the next pass.
func TestK45FourEvaluationsAtOnceOneAnAccount(t *testing.T) {
	w := newWorld(t)
	b := &blockingPending{Connections: w.fake, release: make(chan struct{}), entered: make(chan struct{}, 64), accounts: map[string]string{}, now: map[string]int{}}
	w.conns = b
	s := w.enclave()
	var all []*conn
	for i := range 8 {
		c := w.make(s, made{newMail: true, host: fmt.Sprintf("h%d.example", i), account: fmt.Sprintf("acc_%d", i%6)})
		b.accounts[c.Connection] = c.Account
		all = append(all, c)
	}
	w.clock.add(time.Minute)
	for _, c := range all {
		w.deliver(s, c, 0, true)
	}
	webhooks.Hints(ctx, s)
	webhooks.Scan(ctx, s)
	webhooks.Dispatch(ctx, s)
	for range webhook.EvaluationsInFlightMax {
		<-b.entered
	}
	close(b.release)
	webhooks.Wait(s)
	if b.most != webhook.EvaluationsInFlightMax || b.acctMost != webhook.AccountEvaluationsInFlightMax {
		t.Fatalf("%d at once, %d of one account", b.most, b.acctMost)
	}
	w.every(5*time.Second, 2*time.Minute, s)
	if len(w.sender.all()) != len(all) {
		t.Fatalf("%d told of %d", len(w.sender.all()), len(all))
	}
}

func hostOf(url string) string {
	host, _, _ := strings.Cut(strings.TrimPrefix(url, "https://"), "/")
	return host
}
