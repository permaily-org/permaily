package cimd

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/vendors"
)

// A valid HTTP Date is controlled by the authenticated vendor. No clock
// jump or enormous Age header is needed to reach duration saturation.
func TestV14ReviewAncientDateCannotMakeCopyFresh(t *testing.T) {
	for _, keys := range []bool{false, true} {
		for _, status := range []int{200, 304} {
			t.Run(fmt.Sprintf("keys=%v/status=%d", keys, status), func(t *testing.T) {
				body := document(clientURL)
				if keys {
					body = keySet(rsaJWK(t, "old"))
				}
				p := newPlayed(t, answer(body, "Cache-Control", "max-age=0", "ETag", `"old"`))
				c := &vendors.Client{Document: oauth.ClientDocument{TokenEndpointAuthMethod: oauth.AuthPrivateKeyJWT, JWKSURI: jwksURL}}
				fetch := func() error {
					if keys {
						_, err := p.r.Keys(context.Background(), c, false)
						return err
					}
					_, err := p.r.Client(context.Background(), clientURL)
					return err
				}
				if status == 304 {
					if err := fetch(); err != nil {
						t.Fatal(err)
					}
				}
				p.v.set(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Date", "Sat, 01 Jan 1600 00:00:00 GMT")
					w.Header().Set("Cache-Control", "max-age=60, must-revalidate")
					if status == 304 {
						w.WriteHeader(status)
						return
					}
					answer(body)(w, r)
				})
				if err := fetch(); err != nil {
					t.Fatal(err)
				}
				p.v.set(failing)
				before := p.v.count()
				p.clk.add(25 * time.Hour)
				p.r.mu.Lock()
				e := p.r.docs.get("doc " + clientURL)
				if keys {
					e = p.r.keys.get("keys " + jwksURL)
				}
				age := e.currentAge(p.clk.now())
				t.Logf("initialAge=%v currentAge=%v fresh=%v", e.age, age, e.policy.Fresh(age))
				p.r.mu.Unlock()
				if err := fetch(); !errors.Is(err, vendors.ErrUnreachable) || p.v.count() != before+1 {
					t.Errorf("25h later, ancient Date copy bypasses revalidation: err=%v requests=%d want=%d", err, p.v.count(), before+1)
				}
			})
		}
	}
}

func TestV14ReviewAncientDateCannotExtendStaleHour(t *testing.T) {
	p := newPlayed(t, answer(keySet(rsaJWK(t, "old")), "Cache-Control", "max-age=0", "Date", "Sat, 01 Jan 1600 00:00:00 GMT"))
	c := &vendors.Client{Document: oauth.ClientDocument{TokenEndpointAuthMethod: oauth.AuthPrivateKeyJWT, JWKSURI: jwksURL}}
	if _, err := p.r.Keys(context.Background(), c, false); err != nil {
		t.Fatal(err)
	}
	p.v.set(failing)
	p.clk.add(25 * time.Hour)
	// Force the network path even when the wrapped age incorrectly says fresh.
	if _, err := p.r.Keys(context.Background(), c, true); !errors.Is(err, vendors.ErrUnreachable) {
		t.Errorf("25h later old keys served after failed forced refresh: %v (fetches=%d)", err, p.v.count())
	}
}

func TestV14ReviewAgeInputsAndClockRollback(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		v    []string
		want time.Duration
	}{
		{nil, 0}, {[]string{"0"}, 0}, {[]string{"00059"}, 59 * time.Second}, {[]string{" 59 "}, 59 * time.Second},
		{[]string{"59", "059"}, 59 * time.Second}, {[]string{"59", "60"}, ageUnreadable},
		{[]string{"2147483647"}, 2147483647 * time.Second}, {[]string{"2147483648"}, ageUnreadable},
		{[]string{"9223372036854775807"}, ageUnreadable}, {[]string{strings.Repeat("9", 4000)}, ageUnreadable},
		{[]string{"+1"}, ageUnreadable}, {[]string{"-1"}, ageUnreadable}, {[]string{"1, 1"}, ageUnreadable}, {[]string{""}, ageUnreadable},
	} {
		if got := ageValue(tc.v); got != tc.want {
			t.Errorf("Age %q: %v want %v", tc.v, got, tc.want)
		}
	}
	h := http.Header{"Age": {"59"}, "Date": {now.Add(100 * 365 * 24 * time.Hour).Format(http.TimeFormat)}}
	if got := initialAge(h, now, now.Add(time.Second)); got != 60*time.Second {
		t.Errorf("future Date reduces age: %v", got)
	}
	if got := initialAge(h, now, now.Add(-time.Second)); got != 59*time.Second {
		t.Errorf("backwards response delay subtracts age: %v", got)
	}
	e := &entry{age: 59 * time.Second, validated: now}
	if got := e.currentAge(now.Add(-time.Minute)); got != 59*time.Second {
		t.Errorf("backwards residence subtracts initial age: %v", got)
	}
}

func TestV14ReviewAgeAdditionSaturates(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	e := &entry{validated: now, age: math.MaxInt64}
	if got := e.currentAge(now.Add(time.Nanosecond)); got < 0 {
		t.Errorf("old Date plus one ns wraps to %v", got)
	}
	// Synthetic large forward clock jump also must not make an unreadable Age young.
	h := http.Header{"Age": {"unreadable"}}
	if got := initialAge(h, now, now.Add(time.Duration(math.MaxInt64))); got < ageUnreadable {
		t.Errorf("unreadable Age plus delay becomes %v", got)
	}
}

func TestV14ReviewRefreshQueueConcurrentLastSlot(t *testing.T) {
	p := newPlayed(t, answer(keySet(rsaJWK(t, "new")), "Cache-Control", "max-age=300"))
	const n = vendors.CacheMax
	p.r.mu.Lock()
	for i := 0; i < n-1; i++ {
		p.r.forced.mark(fmt.Sprintf("https://app.vendor.dev/seed/%d", i), p.clk.now())
	}
	clients := make([]*vendors.Client, 2)
	for i := range clients {
		uri := fmt.Sprintf("https://app.vendor.dev/keys/%d", i)
		clients[i] = &vendors.Client{Document: oauth.ClientDocument{TokenEndpointAuthMethod: oauth.AuthPrivateKeyJWT, JWKSURI: uri}}
		p.r.keys.put(&entry{key: "keys " + uri, value: []vendors.Key{{ID: "old"}}, validated: p.clk.now(), policy: vendors.CachePolicy{MaxAge: 300 * time.Second}})
	}
	p.r.mu.Unlock()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 128; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := p.r.Keys(context.Background(), clients[i%2], true)
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	p.r.mu.Lock()
	if got := len(p.r.forced.byURL); got != n || p.r.forced.order.Len() != n {
		t.Errorf("markers=%d queue=%d", got, p.r.forced.order.Len())
	}
	p.r.mu.Unlock()
	if got := p.v.count(); got != 1 {
		t.Errorf("last refresh slot caused %d GETs, want 1", got)
	}
	p.clk.add(time.Minute)
	for _, c := range clients {
		if _, err := p.r.Keys(context.Background(), c, true); err != nil {
			t.Fatal(err)
		}
	}
	if got := p.v.count(); got != 3 {
		t.Errorf("expired markers block new refreshes: %d GETs want 3", got)
	}
}

type v14WaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *v14WaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestV14ReviewOtherKindWaiterRechecksCurrentAge(t *testing.T) {
	body := keySet(rsaJWK(t, "old"))
	p := newPlayed(t, answer(body, "Cache-Control", "max-age=0", "Age", "0"))
	c := &vendors.Client{Document: oauth.ClientDocument{TokenEndpointAuthMethod: oauth.AuthPrivateKeyJWT, JWKSURI: clientURL}}
	if _, err := p.r.Keys(context.Background(), c, false); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	p.v.set(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started); <-release })
		failing(w, r)
	})
	docErr := make(chan error, 1)
	go func() { _, err := p.r.Client(context.Background(), clientURL); docErr <- err }()
	<-started
	ctx := &v14WaitContext{Context: context.Background(), entered: make(chan struct{})}
	keysErr := make(chan error, 1)
	go func() { _, err := p.r.Keys(ctx, c, false); keysErr <- err }()
	<-ctx.entered
	p.clk.add(vendors.StaleMax + time.Second)
	close(release)
	if err := <-docErr; !errors.Is(err, vendors.ErrUnreachable) {
		t.Errorf("doc: %v", err)
	}
	if err := <-keysErr; !errors.Is(err, vendors.ErrUnreachable) {
		t.Errorf("waiter served keys beyond stale hour: %v", err)
	}
	if p.v.count() != 3 {
		t.Errorf("requests=%d want 3", p.v.count())
	}
}

func TestV14ReviewOtherKindWaiterRechecksCacheAndRefreshCapacity(t *testing.T) {
	p := newPlayed(t, answer(keySet(rsaJWK(t, "old")), "Cache-Control", "max-age=300"))
	c := &vendors.Client{Document: oauth.ClientDocument{TokenEndpointAuthMethod: oauth.AuthPrivateKeyJWT, JWKSURI: clientURL}}
	if _, err := p.r.Keys(context.Background(), c, false); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	p.v.set(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		documents("Cache-Control", "max-age=300")(w, r)
	})
	docErr := make(chan error, 1)
	go func() { _, err := p.r.Client(context.Background(), clientURL); docErr <- err }()
	<-started
	ctx := &v14WaitContext{Context: context.Background(), entered: make(chan struct{})}
	keysErr := make(chan error, 1)
	go func() { _, err := p.r.Keys(ctx, c, true); keysErr <- err }()
	<-ctx.entered
	p.r.mu.Lock()
	for i := 0; i < vendors.CacheMax; i++ {
		p.r.forced.mark(fmt.Sprintf("https://app.vendor.dev/seed/%d", i), p.clk.now())
	}
	p.r.mu.Unlock()
	close(release)
	if err := <-docErr; err != nil {
		t.Errorf("doc: %v", err)
	}
	if err := <-keysErr; err != nil {
		t.Errorf("cached keys: %v", err)
	}
	if p.v.count() != 2 {
		t.Errorf("waiter ignored fresh cache after refresh capacity filled: requests=%d want 2", p.v.count())
	}
}

func TestV14ReviewQueueRollbackStaysBounded(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	q := newRefreshes(2)
	if !q.may("a", now) {
		t.Fatal("first refused")
	}
	q.mark("a", now)
	earlier := now.Add(-30 * time.Second)
	if !q.may("b", earlier) {
		t.Fatal("second refused")
	}
	q.mark("b", earlier)
	if q.may("a", earlier) || q.may("c", earlier) {
		t.Fatal("backwards clock forgot live markers or exceeded cap")
	}
	// On a non-monotonic injected clock the queue may expire conservatively,
	// but no live marker is evicted early and capacity stays bounded.
	if q.may("a", now.Add(59*time.Second)) {
		t.Fatal("first marker expired early")
	}
	if !q.may("c", now.Add(time.Minute)) {
		t.Fatal("queue did not expire after clock caught up")
	}
	if len(q.byURL) != 0 || q.order.Len() != 0 {
		t.Fatal("queue and map diverged")
	}
}

func TestV14ReviewOtherKindWaiterCancellation(t *testing.T) {
	p := newPlayed(t, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	p.v.set(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		documents("Cache-Control", "max-age=300")(w, r)
	})
	docErr := make(chan error, 1)
	go func() { _, err := p.r.Client(context.Background(), clientURL); docErr <- err }()
	<-started
	base, cancel := context.WithCancel(context.Background())
	ctx := &v14WaitContext{Context: base, entered: make(chan struct{})}
	c := &vendors.Client{Document: oauth.ClientDocument{TokenEndpointAuthMethod: oauth.AuthPrivateKeyJWT, JWKSURI: clientURL}}
	keysErr := make(chan error, 1)
	go func() { _, err := p.r.Keys(ctx, c, true); keysErr <- err }()
	<-ctx.entered
	cancel()
	if err := <-keysErr; reason(err) != ReasonTransport {
		t.Errorf("cancelled waiter: %v", err)
	}
	p.r.mu.Lock()
	n := len(p.r.forced.byURL)
	p.r.mu.Unlock()
	if n != 0 {
		t.Errorf("cancelled cross-kind waiter remembered %d markers", n)
	}
	close(release)
	if err := <-docErr; err != nil {
		t.Fatal(err)
	}
	if p.v.count() != 1 {
		t.Errorf("cancelled waiter made another request: %d", p.v.count())
	}
}
