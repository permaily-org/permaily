package cimd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/vendors"
)

// fetches resolves clientURL at each offset from the start (cumulative on
// the played clock) and answers how many requests the vendor had after
// each.
func (p *played) fetches(t *testing.T, r *Resolver, offsets ...time.Duration) []int {
	t.Helper()
	var counts []int
	for _, d := range offsets {
		p.clk.add(d)
		if _, err := r.Client(context.Background(), clientURL); err != nil {
			t.Fatalf("at +%v: %v", d, err)
		}
		counts = append(counts, p.v.count())
	}
	return counts
}

// V14 (ADR 0030, choice 18): max-age is honoured as it is: 30 seconds kept
// 30 seconds; a day at most; max-age=0, no-cache, no-store and none fetched
// again for the next use.
func TestV14MaxAgeIsHonouredAsItIs(t *testing.T) {
	p := newPlayed(t, documents("Cache-Control", "max-age=30"))
	if got := p.fetches(t, p.r, 0, 29*time.Second, time.Second, 29*time.Second, time.Second); fmt.Sprint(got) != "[1 1 2 2 3]" {
		t.Errorf("max-age=30: %v", got)
	}
	p = newPlayed(t, documents("Cache-Control", "public, max-age=604800"))
	if got := p.fetches(t, p.r, 0, 24*time.Hour-time.Second, time.Second); fmt.Sprint(got) != "[1 1 2]" {
		t.Errorf("a week is a day: %v", got)
	}
	for _, cc := range []string{"max-age=0", "no-cache", "no-store", "", "max-age=-1", "max-age=x", "max-age=30, max-age=60"} {
		header := []string{"Cache-Control", cc}
		if cc == "" {
			header = nil
		}
		p = newPlayed(t, documents(header...))
		if got := p.fetches(t, p.r, 0, 0, time.Second); fmt.Sprint(got) != "[1 2 3]" {
			t.Errorf("%q: %v", cc, got)
		}
	}
}

// V14: a stale document is revalidated with its validators, a weak ETag
// included; a document kept with no-store has none to send.
func TestV14RevalidationSendsTheValidators(t *testing.T) {
	const modified = "Mon, 16 Nov 2026 10:00:00 GMT"
	p := newPlayed(t, documents("Cache-Control", "max-age=60", "ETag", `W/"v1"`, "Last-Modified", modified))
	p.fetches(t, p.r, 0, time.Minute)
	got := p.v.requests()
	if len(got) != 2 || got[0].Header.Get("If-None-Match") != "" || got[1].Header.Get("If-None-Match") != `W/"v1"` || got[1].Header.Get("If-Modified-Since") != modified {
		t.Fatalf("requests %+v", got)
	}
	p = newPlayed(t, documents("Cache-Control", "no-cache", "ETag", `"v1"`))
	p.fetches(t, p.r, 0, 0)
	if h := p.v.requests()[1].Header.Get("If-None-Match"); h != `"v1"` {
		t.Errorf("no-cache: If-None-Match %q", h)
	}
	p = newPlayed(t, documents("Cache-Control", "no-store", "ETag", `"v1"`))
	p.fetches(t, p.r, 0, 0)
	if h := p.v.requests()[1].Header.Get("If-None-Match"); h != "" {
		t.Errorf("no-store kept: If-None-Match %q", h)
	}
}

// notModified answers 304 to a request whose If-None-Match is etag, with
// the headers given, and otherwise the document with etag.
func notModified(etag string, header ...string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			for i := 0; i+1 < len(header); i += 2 {
				w.Header().Set(header[i], header[i+1])
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		documents("Cache-Control", "max-age=300", "ETag", etag)(w, r)
	}
}

// V14: 304 keeps the document (the chatgpt.com case: through the other
// enclave's resolver, after both the token and the document expired) and
// takes the 304's validators and Cache-Control for the next period.
func TestV14ANotModifiedKeepsTheDocument(t *testing.T) {
	p := newPlayed(t, notModified(`"v1"`))
	a, b := p.r, p.other()
	for _, r := range []*Resolver{a, b} {
		if _, err := r.Client(context.Background(), clientURL); err != nil {
			t.Fatal(err)
		}
	}
	// The token's hour and the document's five minutes both pass; the
	// vendor now changes nothing and answers 304 with new validators.
	p.clk.add(time.Hour)
	p.v.set(notModified(`"v1"`, "ETag", `"v2"`, "Cache-Control", "max-age=600"))
	c, err := b.Client(context.Background(), clientURL)
	if err != nil {
		t.Fatalf("304: %v", err)
	}
	if c.Document.ClientID != clientURL || !c.FetchedAt.Equal(p.clk.now()) {
		t.Fatalf("client %+v", c)
	}
	got := p.v.requests()
	if len(got) != 3 || got[2].Header.Get("If-None-Match") != `"v1"` {
		t.Fatalf("requests %+v", got)
	}
	// The 304's max-age holds: fresh for 600 seconds, then revalidated
	// with the 304's ETag.
	p.v.set(notModified(`"v2"`))
	p.clk.add(599 * time.Second)
	if _, err := b.Client(context.Background(), clientURL); err != nil || p.v.count() != 3 {
		t.Fatalf("within the 304's max-age: %v, %d requests", err, p.v.count())
	}
	p.clk.add(time.Second)
	if _, err := b.Client(context.Background(), clientURL); err != nil {
		t.Fatal(err)
	}
	if got := p.v.requests(); len(got) != 4 || got[3].Header.Get("If-None-Match") != `"v2"` {
		t.Fatalf("after the 304's max-age: %+v", got)
	}
	// A 304 with no Cache-Control keeps the policy the document had.
	p.clk.add(299 * time.Second)
	if _, err := b.Client(context.Background(), clientURL); err != nil || p.v.count() != 4 {
		t.Fatalf("a 304 with no Cache-Control: %v, %d requests", err, p.v.count())
	}
}

// V14: a 304 for a document not held is unreachable.
func TestV14ANotModifiedForADocumentNotHeldIsUnreachable(t *testing.T) {
	p := newPlayed(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotModified) })
	_, err := p.r.Client(context.Background(), clientURL)
	if !errors.Is(err, vendors.ErrUnreachable) || reason(err) != ReasonNotHeld {
		t.Fatalf("%v", err)
	}
	// Nor for one kept with no-store, which is not held.
	p = newPlayed(t, documents("Cache-Control", "no-store", "ETag", `"v1"`))
	p.fetches(t, p.r, 0)
	p.v.set(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotModified) })
	if _, err := p.r.Client(context.Background(), clientURL); !errors.Is(err, vendors.ErrUnreachable) {
		t.Fatalf("no-store: %v", err)
	}
}

func failing(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }

// V14: a failed revalidation serves the stale document for one hour after
// it went stale, counted from the last successful fetch, never extended by
// further failures; then it is unreachable until it can be fetched.
func TestV14AFailedRevalidationServesStaleForOneHour(t *testing.T) {
	p := newPlayed(t, documents("Cache-Control", "max-age=60"))
	p.fetches(t, p.r, 0)
	fetchedAt := p.clk.now()
	p.v.set(failing)
	for _, d := range []time.Duration{61 * time.Second, 30 * time.Minute, 29*time.Minute + 58*time.Second} {
		p.clk.add(d)
		c, err := p.r.Client(context.Background(), clientURL)
		if err != nil || !c.FetchedAt.Equal(fetchedAt) {
			t.Fatalf("stale at %v: %v", p.clk.now().Sub(fetchedAt), err)
		}
	}
	p.clk.add(time.Second) // an hour after it went stale
	if _, err := p.r.Client(context.Background(), clientURL); !errors.Is(err, vendors.ErrUnreachable) {
		t.Fatalf("past the hour: %v", err)
	}
	// Fetched again, it is fresh again.
	p.v.set(documents("Cache-Control", "max-age=60"))
	if _, err := p.r.Client(context.Background(), clientURL); err != nil {
		t.Fatal(err)
	}
	// A vendor whose answer has become a document the rules refuse is not
	// served stale: its own answer says what it is.
	p.clk.add(time.Minute)
	p.v.set(answer([]byte(`{"client_id":"https://elsewhere.dev/x"}`)))
	if _, err := p.r.Client(context.Background(), clientURL); !errors.Is(err, vendors.ErrDocument) {
		t.Fatalf("refused document: %v", err)
	}
	p.v.set(failing)
	if _, err := p.r.Client(context.Background(), clientURL); !errors.Is(err, vendors.ErrUnreachable) {
		t.Fatalf("after a refused document: %v", err)
	}
}

// keySet is a key set of the given RSA keys' JWKs.
func keySet(keys ...string) []byte {
	b := []byte(`{"keys":[`)
	for i, k := range keys {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, k...)
	}
	return append(b, "]}"...)
}

// V14 and V15 (N7): never served stale when its directives forbid it, for
// a document and for a key set: fetched successfully by both enclaves, a
// key removed by the vendor, the revalidation blocked by the host, the
// next token request through the other enclave refused, never accepting
// the removed key.
func TestV14RestrictiveDirectivesAreNeverServedStale(t *testing.T) {
	k1, k2 := rsaJWK(t, "k1"), rsaJWK(t, "k2")
	for _, cc := range []string{"no-store", "no-cache", "max-age=30, must-revalidate", "max-age=30, proxy-revalidate", "max-age=30, s-maxage=30"} {
		var mu sync.Mutex
		blocked, removed := false, false
		p := newPlayed(t, func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			b, rm := blocked, removed
			mu.Unlock()
			if b {
				failing(w, r)
				return
			}
			switch r.URL.Path {
			case "/oauth/jwks.json":
				set := keySet(k1, k2)
				if rm {
					set = keySet(k1)
				}
				answer(set, "Cache-Control", cc)(w, r)
			default:
				answer(jwtDocument(clientURL), "Cache-Control", cc)(w, r)
			}
		})
		a, b := p.r, p.other()
		for _, r := range []*Resolver{a, b} {
			c, err := r.Client(context.Background(), clientURL)
			if err != nil {
				t.Fatalf("%s: %v", cc, err)
			}
			if keys, err := r.Keys(context.Background(), c, false); err != nil || len(keys) != 2 {
				t.Fatalf("%s: keys %v %v", cc, keys, err)
			}
		}
		c, _ := b.Client(context.Background(), clientURL)
		mu.Lock()
		removed, blocked = true, true
		mu.Unlock()
		p.clk.add(31 * time.Second)
		if _, err := b.Client(context.Background(), clientURL); !errors.Is(err, vendors.ErrUnreachable) {
			t.Errorf("%s: the document was served stale: %v", cc, err)
		}
		if keys, err := b.Keys(context.Background(), c, false); !errors.Is(err, vendors.ErrUnreachable) {
			t.Errorf("%s: the key set was served stale: %v %v", cc, keys, err)
		}
		// The host lets it through again: the removed key is gone.
		mu.Lock()
		blocked = false
		mu.Unlock()
		keys, err := b.Keys(context.Background(), c, false)
		if err != nil || len(keys) != 1 || keys[0].ID != "k1" {
			t.Errorf("%s: after the block %v %v", cc, keys, err)
		}
	}
	// Without such a directive, the same block serves the stale set.
	p := newPlayed(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/jwks.json" {
			answer(keySet(k1), "Cache-Control", "max-age=30")(w, r)
			return
		}
		answer(jwtDocument(clientURL), "Cache-Control", "max-age=30")(w, r)
	})
	c, err := p.r.Client(context.Background(), clientURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.r.Keys(context.Background(), c, false); err != nil {
		t.Fatal(err)
	}
	p.v.set(failing)
	p.clk.add(31 * time.Second)
	if _, err := p.r.Keys(context.Background(), c, false); err != nil {
		t.Errorf("max-age=30 not served stale: %v", err)
	}
}

// V14: past vendors.FetchesPerMinute (60) fetches of clients not held in a
// minute, a new client is unreachable until the minute ends; a cached one
// is still served, and revalidating one is not counted.
func TestV14SixtyFetchesAMinute(t *testing.T) {
	p := newPlayed(t, documents("Cache-Control", "max-age=10"))
	id := func(i int) string { return fmt.Sprintf("https://app.vendor.dev/oauth/client-%d.json", i) }
	for i := range vendors.FetchesPerMinute {
		if _, err := p.r.Client(context.Background(), id(i)); err != nil {
			t.Fatal(err)
		}
	}
	_, err := p.r.Client(context.Background(), id(60))
	if !errors.Is(err, vendors.ErrUnreachable) || reason(err) != ReasonRateLimited {
		t.Fatalf("the 61st: %v", err)
	}
	if _, err := p.r.Client(context.Background(), id(0)); err != nil {
		t.Fatalf("a cached one: %v", err)
	}
	p.clk.add(30 * time.Second)
	for i := range 5 {
		if _, err := p.r.Client(context.Background(), id(i)); err != nil {
			t.Fatalf("a revalidation: %v", err)
		}
	}
	if _, err := p.r.Client(context.Background(), id(60)); !errors.Is(err, vendors.ErrUnreachable) {
		t.Fatalf("the 61st, still in the minute: %v", err)
	}
	p.clk.add(30 * time.Second)
	if _, err := p.r.Client(context.Background(), id(60)); err != nil {
		t.Fatalf("the 61st, the next minute: %v", err)
	}
	if n := p.v.count(); n != 66 {
		t.Errorf("%d fetches", n)
	}
}

// V14 (choice 18): 1,025 documents keep 1,024, the least recently used
// dropped first.
func TestV14TheCacheKeeps1024(t *testing.T) {
	p := newPlayed(t, documents("Cache-Control", "max-age=86400"))
	id := func(i int) string { return fmt.Sprintf("https://app.vendor.dev/oauth/client-%d.json", i) }
	for i := range vendors.CacheMax + 1 {
		if i > 0 && i%vendors.FetchesPerMinute == 0 {
			p.clk.add(time.Minute)
		}
		if i == vendors.CacheMax {
			// Used again just before the last arrives: kept, and the
			// second is now the least recently used.
			if _, err := p.r.Client(context.Background(), id(0)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := p.r.Client(context.Background(), id(i)); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	if n := p.r.docs.len(); n != vendors.CacheMax {
		t.Fatalf("%d held", n)
	}
	before := p.v.count()
	p.clk.add(time.Minute)
	for _, i := range []int{0, vendors.CacheMax, 1} {
		if _, err := p.r.Client(context.Background(), id(i)); err != nil {
			t.Fatal(err)
		}
	}
	if n := p.v.count() - before; n != 1 {
		t.Errorf("%d fetched again, want only the least recently used", n)
	}
}

// V14 (review of pull request 130, finding 3): a 304 that says no-store
// answers the request that revalidated, and drops the stored copy and its
// validators, for a document and for a key set.
func TestV14ANotModifiedThatSaysNoStoreDropsTheCopy(t *testing.T) {
	for _, keys := range []bool{false, true} {
		t.Run(fmt.Sprintf("keys=%v", keys), func(t *testing.T) {
			body := document(clientURL)
			if keys {
				body = keySet(rsaJWK(t, "old"))
			}
			p := newPlayed(t, answer(body, "Cache-Control", "max-age=0", "ETag", `"v1"`))
			c := &vendors.Client{Document: oauth.ClientDocument{TokenEndpointAuthMethod: oauth.AuthPrivateKeyJWT, JWKSURI: jwksURL}}
			fetch := func() error {
				if keys {
					_, err := p.r.Keys(context.Background(), c, false)
					return err
				}
				_, err := p.r.Client(context.Background(), clientURL)
				return err
			}
			if err := fetch(); err != nil {
				t.Fatal(err)
			}
			p.v.set(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusNotModified)
			})
			if err := fetch(); err != nil {
				t.Fatalf("the 304 that says no-store: %v", err)
			}
			p.r.mu.Lock()
			n := p.r.docs.len()
			if keys {
				n = p.r.keys.len()
			}
			p.r.mu.Unlock()
			if n != 0 {
				t.Errorf("a 304 that says no-store left %d copies", n)
			}
			p.v.set(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotModified) })
			if err := fetch(); !errors.Is(err, vendors.ErrUnreachable) {
				t.Errorf("a 304 after it was answered from a copy kept against no-store: %v", err)
			}
			if got := p.v.requests()[2].Header.Get("If-None-Match"); got != "" {
				t.Errorf("sent the validator %s of a copy that is not kept", got)
			}
		})
	}
}

// V14 (review of pull request 130, finding 2): a response's age counts
// toward its freshness and its stale hour as RFC 9111 section 4.2.3
// computes it: the larger of the Age it came with plus the time it took to
// come and the time since its Date, plus the time it has been kept. For a
// document and a key set, a 200 and a 304 alike: an aged answer from a
// CDN never starts its max-age again.
func TestV14AResponsesAgeCountsTowardItsFreshness(t *testing.T) {
	t.Run("near expiry", func(t *testing.T) {
		p := newPlayed(t, documents("Cache-Control", "max-age=60", "Age", "30"))
		if got := p.fetches(t, p.r, 0, 29*time.Second, time.Second); fmt.Sprint(got) != "[1 1 2]" {
			t.Errorf("max-age=60 at Age 30: %v", got)
		}
	})
	t.Run("restrictive", func(t *testing.T) {
		p := newPlayed(t, documents("Cache-Control", "max-age=60, must-revalidate", "Age", "59"))
		p.fetches(t, p.r, 0)
		p.v.set(failing)
		p.clk.add(2 * time.Second)
		if _, err := p.r.Client(context.Background(), clientURL); !errors.Is(err, vendors.ErrUnreachable) || p.v.count() != 2 {
			t.Errorf("61 seconds old with must-revalidate: %v, %d requests", err, p.v.count())
		}
	})
	t.Run("stale cutoff", func(t *testing.T) {
		p := newPlayed(t, documents("Cache-Control", "max-age=60", "Age", "30"))
		p.fetches(t, p.r, 0)
		fetchedAt := p.clk.now()
		p.v.set(failing)
		p.clk.add(30*time.Second + vendors.StaleMax - time.Second)
		if c, err := p.r.Client(context.Background(), clientURL); err != nil || !c.FetchedAt.Equal(fetchedAt) {
			t.Fatalf("within its stale hour: %v", err)
		}
		p.clk.add(time.Second)
		if _, err := p.r.Client(context.Background(), clientURL); !errors.Is(err, vendors.ErrUnreachable) {
			t.Errorf("an hour after it went stale, counting its Age: %v", err)
		}
	})
	t.Run("date", func(t *testing.T) {
		p := newPlayed(t, nil)
		p.v.set(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Date", p.clk.now().Add(-50*time.Second).UTC().Format(http.TimeFormat))
			documents("Cache-Control", "max-age=60")(w, r)
		})
		if got := p.fetches(t, p.r, 0, 9*time.Second, time.Second); fmt.Sprint(got) != "[1 1 2]" {
			t.Errorf("max-age=60 dated 50 seconds ago: %v", got)
		}
	})
	t.Run("delay", func(t *testing.T) {
		p := newPlayed(t, nil)
		p.v.set(func(w http.ResponseWriter, r *http.Request) {
			p.clk.add(10 * time.Second) // ten seconds on the way
			w.Header().Set("Date", p.clk.now().UTC().Format(http.TimeFormat))
			documents("Cache-Control", "max-age=60", "Age", "0")(w, r)
		})
		if got := p.fetches(t, p.r, 0, 49*time.Second, time.Second); fmt.Sprint(got) != "[1 1 2]" {
			t.Errorf("max-age=60 ten seconds on the way: %v", got)
		}
	})
	t.Run("invalid age", func(t *testing.T) {
		for _, age := range []string{"x", "-1", "1.5", "30, 31"} {
			p := newPlayed(t, documents("Cache-Control", "max-age=60", "Age", age))
			if got := p.fetches(t, p.r, 0, 0); fmt.Sprint(got) != "[1 2]" {
				t.Errorf("Age %q: %v", age, got)
			}
		}
	})
	t.Run("not modified", func(t *testing.T) {
		p := newPlayed(t, documents("Cache-Control", "max-age=60", "ETag", `"v1"`))
		p.fetches(t, p.r, 0)
		p.v.set(notModified(`"v1"`, "Age", "50", "Cache-Control", "max-age=60"))
		if got := p.fetches(t, p.r, time.Minute, 9*time.Second, time.Second); fmt.Sprint(got) != "[2 2 3]" {
			t.Errorf("a 304 of Age 50: %v", got)
		}
	})
	t.Run("keys", func(t *testing.T) {
		k1 := rsaJWK(t, "k1")
		var down atomic.Bool
		p := newPlayed(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/oauth/jwks.json" {
				answer(jwtDocument("https://"+r.Host+r.URL.Path), "Cache-Control", "max-age=3600")(w, r)
				return
			}
			if down.Load() {
				failing(w, r)
				return
			}
			answer(keySet(k1), "Cache-Control", "max-age=60, must-revalidate", "Age", "50")(w, r)
		})
		ctx := context.Background()
		c, err := p.r.Client(ctx, clientURL)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.r.Keys(ctx, c, false); err != nil {
			t.Fatal(err)
		}
		p.clk.add(9 * time.Second)
		if _, err := p.r.Keys(ctx, c, false); err != nil || p.jwksFetches() != 1 {
			t.Fatalf("at 59 seconds old: %v, %d fetches", err, p.jwksFetches())
		}
		down.Store(true)
		p.clk.add(time.Second)
		if _, err := p.r.Keys(ctx, c, false); !errors.Is(err, vendors.ErrUnreachable) || p.jwksFetches() != 2 {
			t.Errorf("60 seconds old with must-revalidate: %v, %d fetches", err, p.jwksFetches())
		}
	})
}
