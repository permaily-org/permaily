package decisions

import (
	"context"
	"crypto/tls"
	"errors"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/decide/decidetest"
	"github.com/permaily-org/permaily/internal/decide/openai"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// The service through the real client (internal/decide/openai) against
// decidetest's played provider, over HTTPS and HTTP/2 and never OpenAI:
// A20's canary and A37's attempts, each attempt reserved through the guard
// and settled from what reached the provider (ADR 0028, "Tests").

// played is a played provider answering from its table, which accepts only
// the key the world sets (testKey).
type played struct {
	tab *decidetest.Table
	p   *decidetest.Provider
	// hc, when set, is the egress client in place of the provider's own.
	hc *http.Client

	mu sync.Mutex
	// afterKey runs once the client has the key of an attempt, before it
	// sends anything.
	afterKey func()
}

func newPlayed(t *testing.T) *played {
	t.Helper()
	tab := decidetest.NewTable()
	tab.RequireKey(testKey)
	return &played{tab: tab, p: decidetest.NewProvider(t, tab)}
}

// withPlayed makes the enclave's model the real client, built as the
// composition builds it (openai.New with the service's credential), talking
// to pl's provider.
func withPlayed(pl *played) option {
	return func(c *Config, _ *enclave) {
		c.NewModel = func(key decide.Credential) decide.Model {
			hc := pl.hc
			if hc == nil {
				hc = pl.p.Client()
			}
			return openai.New(hc, func(ctx context.Context) (string, bool) {
				k, ok := key(ctx)
				pl.mu.Lock()
				f := pl.afterKey
				pl.mu.Unlock()
				if f != nil {
					f()
				}
				return k, ok
			}, "", "")
		}
	}
}

// spent is what e's grants were charged.
func spent(e *enclave) int64 {
	e.guard.mu.Lock()
	defer e.guard.mu.Unlock()
	used := int64(0)
	for _, g := range e.guard.grants {
		used += g.used
	}
	return used
}

// owed is what the attempts that reached the provider cost: the usage it
// reported for those it answered, the estimate for the others.
func owed(calls []decide.Call, failed func(i int) bool) int64 {
	n := int64(0)
	for i, c := range calls {
		if failed(i) {
			n += decide.Estimate(c)
		} else {
			n += decide.Cost(decidetest.Usage(c))
		}
	}
	return n
}

func first(i int) bool { return i == 0 }

// A37. Attempts and their spend, through the real client against a played
// provider.
func TestA37AttemptsThroughTheRealClient(t *testing.T) {
	t.Parallel()
	x := subj("x")
	hi := func(id string) *question { return newQuestion(id, "Hi", "", x) }
	t.Run("a request processed and its answer dropped is charged its estimate, tried again and reserved again", func(t *testing.T) {
		t.Parallel()
		pl := newPlayed(t)
		pl.tab.Set(x, 0.9)
		pl.tab.Fail(decidetest.Drop)
		e := newWorld(t).keyed(withPlayed(pl))
		out := e.ask(hi("m1"))
		calls := pl.tab.Calls()
		if out.Called != 2 || out.Answers[x] != 0.9 || len(calls) != 2 || len(pl.p.Requests()) != 2 {
			t.Fatalf("%+v, %d calls", out, len(calls))
		}
		if got, want := spent(e), owed(calls, first); got != want {
			t.Fatalf("charged %d, owed %d", got, want)
		}
	})
	t.Run("an answer after the client's timeout is charged its estimate and never used", func(t *testing.T) {
		t.Parallel()
		pl := newPlayed(t)
		pl.tab.Set(x, 0.1)
		pl.tab.Fail(decidetest.Late)
		e := newWorld(t).keyed(withPlayed(pl))
		go func() {
			for pl.tab.Attempts() == 0 {
				time.Sleep(time.Millisecond)
			}
			pl.tab.Set(x, 0.9) // the late attempt's answer, 0.1, is already its own
		}()
		start := time.Now()
		out := e.ask(hi("m1"))
		calls := pl.tab.Calls()
		if out.Called != 2 || out.Answers[x] != 0.9 || len(calls) != 2 || time.Since(start) < decide.CallTimeout {
			t.Fatalf("%+v after %v", out, time.Since(start))
		}
		if got, want := spent(e), owed(calls, first); got != want {
			t.Fatalf("charged %d, owed %d", got, want)
		}
	})
	for _, f := range []struct {
		name string
		f    decidetest.Failure
	}{{"a refused stream", decidetest.RefusedStream}, {"a reset stream", decidetest.ResetStream}, {"a GOAWAY", decidetest.GoAway}} {
		t.Run(f.name+" is sent once by the transport, charged its estimate and tried again by the service", func(t *testing.T) {
			t.Parallel()
			pl := newPlayed(t)
			pl.tab.Set(x, 0.9)
			pl.tab.Fail(f.f)
			e := newWorld(t).keyed(withPlayed(pl))
			out := e.ask(hi("m1"))
			reqs := pl.p.Requests()
			if out.Called != 2 || out.Answers[x] != 0.9 || len(reqs) != 2 || reqs[0].Proto != "HTTP/2.0" {
				t.Fatalf("%+v, %d requests", out, len(reqs))
			}
			if got, want := spent(e), owed(pl.tab.Calls(), first); got != want {
				t.Fatalf("charged %d, owed %d", got, want)
			}
		})
	}
	for _, f := range []struct {
		name, kind string
		f          decidetest.Failure
	}{
		{"an answer naming another model", attestation.ModelCallOtherModel, decidetest.OtherModel},
		{"an answer that is not the call's", attestation.ModelCallInvalid, decidetest.Invalid},
	} {
		t.Run(f.name+" is unknown, not tried again, charged its estimate", func(t *testing.T) {
			t.Parallel()
			pl := newPlayed(t)
			pl.tab.Fail(f.f)
			e := newWorld(t).keyed(withPlayed(pl))
			out := e.ask(hi("m1"))
			if out.Called != 1 || len(out.Answers) != 0 || e.Status(context.Background()).Calls[f.kind] != 1 {
				t.Fatalf("%+v", out)
			}
			if got, want := spent(e), owed(pl.tab.Calls(), first); got != want {
				t.Fatalf("charged %d, owed %d", got, want)
			}
		})
	}
	t.Run("a probability that is not one is unknown for that atom alone, and never kept", func(t *testing.T) {
		t.Parallel()
		pl := newPlayed(t)
		bad := []query.Atom{subj("above"), subj("nan"), subj("inf"), subj("missing")}
		pl.tab.Set(bad[0], 1.5)
		pl.tab.Set(bad[1], math.NaN())
		pl.tab.Set(bad[2], math.Inf(1))
		pl.tab.Unknown(bad[3])
		pl.tab.Set(x, 0.7)
		e := newWorld(t).keyed(withPlayed(pl))
		q := newQuestion("m1", "Hi", "", append([]query.Atom{x}, bad...)...)
		out := e.ask(q)
		if out.Called != 1 || len(out.Answers) != 1 || out.Answers[x] != 0.7 {
			t.Fatalf("%+v", out)
		}
		if got, want := spent(e), owed(pl.tab.Calls(), func(int) bool { return false }); got != want {
			t.Fatalf("charged %d, its reported usage %d", got, want)
		}
		e.ask(q)
		calls := pl.tab.Calls()
		if len(calls) != 2 || !slices.Equal(calls[1].Statements, bad) {
			t.Fatalf("asked again %v", calls[len(calls)-1].Statements)
		}
	})
	long := strings.Repeat("a", decide.LongContent+1) // the 8 s timeout
	for _, third := range []string{"answers", "is late too"} {
		t.Run("after two timeouts of 8 s and their waits, the third attempt "+third+" within what is left of the message's 20 s", func(t *testing.T) {
			t.Parallel()
			pl := newPlayed(t)
			pl.tab.Set(about("x"), 0.9)
			pl.tab.Fail(decidetest.Late, decidetest.Late)
			if third != "answers" {
				pl.tab.Fail(decidetest.Late)
			}
			e := newWorld(t).keyed(withPlayed(pl))
			e.sleep = sleepCtx
			e.jitter = func(d time.Duration) time.Duration { return d } // the longest waits
			start := time.Now()
			out := e.ask(newQuestion("m1", "Hi", long, about("x")))
			took := time.Since(start)
			calls := pl.tab.Calls()
			if out.Called != 3 || len(calls) != 3 || took < 2*decide.LongCallTimeout || took > decide.MessageDeadline+time.Second {
				t.Fatalf("%+v, %d calls in %v", out, len(calls), took)
			}
			failed := func(i int) bool { return i < 2 }
			if third == "answers" {
				if out.Answers[about("x")] != 0.9 {
					t.Fatalf("%+v", out)
				}
			} else {
				failed = func(int) bool { return true }
				if len(out.Answers) != 0 || e.Status(context.Background()).Calls[attestation.ModelCallTimeout] != 1 {
					t.Fatalf("%+v", out)
				}
			}
			if got, want := spent(e), owed(calls, failed); got != want {
				t.Fatalf("charged %d, owed %d", got, want)
			}
		})
	}
	t.Run("Retry-After honoured up to 5 s, and no wait past the deadline", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			f     decidetest.Failure
			after time.Duration
			want  time.Duration
		}{{decidetest.Unavailable, 9 * time.Second, decide.MaxRetryAfter}, {decidetest.SlowDown, 2 * time.Second, 2 * time.Second}} {
			pl := newPlayed(t)
			pl.tab.SetRetryAfter(tc.after)
			pl.tab.Fail(tc.f)
			e := newWorld(t).keyed(withPlayed(pl))
			var waits []time.Duration
			e.sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
			out := e.ask(hi("m1"))
			if out.Called != 2 || len(out.Answers) != 1 || !slices.Equal(waits, []time.Duration{tc.want}) {
				t.Fatalf("%+v, waits %v", out, waits)
			}
			if got, want := spent(e), owed(pl.tab.Calls(), first); got != want {
				t.Fatalf("charged %d, owed %d", got, want)
			}
		}
		pl := newPlayed(t)
		pl.tab.SetRetryAfter(decide.MaxRetryAfter)
		pl.tab.Fail(decidetest.Unavailable)
		e := newWorld(t).keyed(withPlayed(pl))
		var waits []time.Duration
		e.sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, _ := e.Ask(ctx, hi("m2").Question)
		if out.Called != 1 || len(waits) != 0 || len(pl.p.Requests()) != 1 {
			t.Fatalf("%+v, waits %v", out, waits)
		}
	})
	t.Run("a month with room for one attempt's grant only: the retry is not sent", func(t *testing.T) {
		t.Parallel()
		pl := newPlayed(t)
		pl.tab.FailAlways(decidetest.Drop)
		w := newWorld(t)
		w.seedSpend(month, "2026-11-01", 499)
		e := w.keyed(withPlayed(pl))
		out := e.ask(newQuestion("m1", "Hi", strings.Repeat(`"`, 175_000), about("x"))) // 0.07 USD an attempt
		if out.Called != 1 || len(out.Answers) != 0 || len(pl.p.Requests()) != 1 {
			t.Fatalf("%+v, %d requests", out, len(pl.p.Requests()))
		}
		if got, want := spent(e), owed(pl.tab.Calls(), first); got != want {
			t.Fatalf("charged %d, owed %d", got, want)
		}
	})
	t.Run("cancelled before the request was written is not charged; after, its estimate", func(t *testing.T) {
		t.Parallel()
		pl := newPlayed(t)
		e := newWorld(t).keyed(withPlayed(pl))
		ctx, cancel := context.WithCancel(context.Background())
		pl.afterKey = cancel
		_, _ = e.Ask(ctx, hi("m1").Question)
		if len(pl.p.Requests()) != 0 || spent(e) != 0 {
			t.Fatalf("%d requests, charged %d", len(pl.p.Requests()), spent(e))
		}

		pl.mu.Lock()
		pl.afterKey = nil
		pl.mu.Unlock()
		pl.tab.Fail(decidetest.Late)
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
		go func() {
			for pl.tab.Attempts() == 0 {
				time.Sleep(time.Millisecond)
			}
			cancel()
		}()
		out, _ := e.Ask(ctx, hi("m2").Question)
		if out.Called != 1 || len(out.Answers) != 0 || len(pl.p.Requests()) != 1 {
			t.Fatalf("%+v, %d requests", out, len(pl.p.Requests()))
		}
		if got, want := spent(e), owed(pl.tab.Calls(), first); got != want {
			t.Fatalf("charged %d, owed %d", got, want)
		}
	})
	t.Run("two services near the month's limit, every attempt failing once sent, never pass Month; each settled once", func(t *testing.T) {
		t.Parallel()
		pl := newPlayed(t)
		pl.tab.FailAlways(decidetest.Drop)
		w := newWorld(t)
		w.seedSpend(month, "2026-11-01", 495)
		a, b := w.keyed(withPlayed(pl)), w.keyed(withPlayed(pl))
		var wg sync.WaitGroup
		for i := range 30 {
			e := a
			if i%2 == 0 {
				e = b
			}
			wg.Go(func() { e.ask(newQuestion("m"+itoa(i), "Hi", strings.Repeat("a", 50_000), about("x"))) })
		}
		wg.Wait()
		if d := w.spend(month); d.total()*block > DefaultLimits.Month {
			t.Fatalf("%d blocks", d.total())
		}
		both := spent(a) + spent(b)
		if both > 5*block || both != owed(pl.tab.Calls(), func(int) bool { return true }) {
			t.Fatalf("charged %d together for %d attempts", both, len(pl.tab.Calls()))
		}
	})
	t.Run("at most Concurrent attempts open", func(t *testing.T) {
		t.Parallel()
		pl := newPlayed(t)
		pl.tab.SetDelay(200 * time.Millisecond)
		open := &openCount{rt: &http.Transport{
			DialContext:       pl.p.DialContext,
			TLSClientConfig:   &tls.Config{RootCAs: pl.p.RootCAs(), MinVersion: tls.VersionTLS12},
			ForceAttemptHTTP2: true,
		}}
		pl.hc = &http.Client{Transport: open}
		e := newWorld(t).keyed(withPlayed(pl))
		var wg sync.WaitGroup
		for i := range decide.Concurrent + 4 {
			wg.Go(func() { e.ask(hi("m" + itoa(i))) })
		}
		wg.Wait()
		if open.most != decide.Concurrent || len(pl.p.Requests()) != decide.Concurrent+4 {
			t.Fatalf("%d open at once, %d requests", open.most, len(pl.p.Requests()))
		}
		if got, want := spent(e), owed(pl.tab.Calls(), func(int) bool { return false }); got != want {
			t.Fatalf("charged %d, owed %d", got, want)
		}
	})
}

// openCount counts the requests open at once.
type openCount struct {
	rt         http.RoundTripper
	mu         sync.Mutex
	open, most int
}

func (o *openCount) RoundTrip(r *http.Request) (*http.Response, error) {
	o.mu.Lock()
	o.open++
	o.most = max(o.most, o.open)
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		o.open--
		o.mu.Unlock()
	}()
	return o.rt.RoundTrip(r)
}

// measured makes tab answer e's canary as it was measured, each
// probability passed through move.
func measured(e *enclave, tab *decidetest.Table, move func(id string, a query.Atom, p float64) float64) {
	for _, m := range e.canary.Messages {
		for _, ca := range m.Atoms {
			p := ca.P
			if move != nil {
				p = move(m.ID, ca.Atom(), p)
			}
			tab.SetFor(m.Subject, m.Body, ca.Atom(), p)
		}
	}
}

// A20. The canary, through the real client against a played provider, its
// attempts reserved through the guard.
func TestA20TheCanaryThroughTheRealClient(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	today := "2026-11-16"
	canaryOf := func(t *testing.T) (*world, *enclave, *played) {
		t.Helper()
		pl := newPlayed(t)
		w := newWorld(t)
		return w, w.keyed(withPlayed(pl)), pl
	}
	t.Run("the same answers are same, each attempt reserved", func(t *testing.T) {
		w, e, pl := canaryOf(t)
		measured(e, pl.tab, nil)
		if err := e.RunCanary(ctx); err != nil {
			t.Fatal(err)
		}
		calls := pl.tab.Calls()
		st := e.Status(ctx)
		if st.Canary.State != attestation.CanarySame || len(calls) == 0 || st.Calls[attestation.ModelCallCanary] != len(calls) || len(w.spend(month).Grants) != 1 {
			t.Fatalf("%+v, %d calls", st.Canary, len(calls))
		}
		if got, want := spent(e), owed(calls, func(int) bool { return false }); got != want {
			t.Fatalf("charged %d, owed %d", got, want)
		}
	})
	t.Run("a dropped answer is tried again, reserved again, and the run is same", func(t *testing.T) {
		_, e, pl := canaryOf(t)
		measured(e, pl.tab, nil)
		pl.tab.Fail(decidetest.Drop)
		if err := e.RunCanary(ctx); err != nil {
			t.Fatal(err)
		}
		calls := pl.tab.Calls()
		if e.Status(ctx).Canary.State != attestation.CanarySame || e.Status(ctx).Calls[attestation.ModelCallCanary] != len(calls) {
			t.Fatalf("%+v", e.Status(ctx).Canary)
		}
		if got, want := spent(e), owed(calls, first); got != want {
			t.Fatalf("charged %d, owed %d", got, want)
		}
	})
	changes := map[string]func(e *enclave, tab *decidetest.Table) (failed func(int) bool){
		"another model": func(e *enclave, tab *decidetest.Table) func(int) bool {
			measured(e, tab, nil)
			tab.Fail(decidetest.OtherModel)
			return first
		},
		"one answer moved by 0.03": func(e *enclave, tab *decidetest.Table) func(int) bool {
			measured(e, tab, func(m string, a query.Atom, p float64) float64 {
				if m == "inv08" && a == about("invoices") {
					return max(p-0.03, 0)
				}
				return p
			})
			return func(int) bool { return false }
		},
		"a refusal where an answer was measured": func(e *enclave, tab *decidetest.Table) func(int) bool {
			measured(e, tab, nil)
			tab.Refuse(e.canary.Messages[0].Atoms[0].Atom())
			return func(int) bool { return false }
		},
	}
	for _, step := range [][2]float64{{0.28, 0.30}, {0.20, 0.21}, {0.79, 0.80}, {0.69, 0.70}} {
		for _, back := range []bool{false, true} {
			from, to := step[0], step[1]
			if back {
				from, to = to, from
			}
			changes["across a threshold "+ftoa(from)+" to "+ftoa(to)] = func(e *enclave, tab *decidetest.Table) func(int) bool {
				c := *e.canary
				c.Messages = slices.Clone(c.Messages)
				m := c.Messages[0]
				m.Atoms = slices.Clone(m.Atoms)
				m.Atoms[0].P = from
				c.Messages[0] = m
				e.canary = &c
				measured(e, tab, func(id string, a query.Atom, p float64) float64 {
					if id == m.ID && a == m.Atoms[0].Atom() {
						return to
					}
					return p
				})
				return func(int) bool { return false }
			}
		}
	}
	for name, change := range changes {
		t.Run("changed: "+name, func(t *testing.T) {
			t.Parallel()
			w, e, pl := canaryOf(t)
			failed := change(e, pl.tab)
			if err := e.RunCanary(ctx); err != nil {
				t.Fatal(err)
			}
			st := e.Status(ctx).Canary
			if st.State != attestation.CanaryChanged || st.ChangeSeenAt.Format(time.DateOnly) != today {
				t.Fatalf("%+v", st)
			}
			a, ok := w.alarms().has(AlarmCanaryChanged, today)
			if !ok || a.Baseline != e.canary.Baseline || w.alarms().count(AlarmCanaryChanged) != 1 {
				t.Fatalf("%+v", w.alarms().Entries)
			}
			if got, want := spent(e), owed(pl.tab.Calls(), failed); got != want {
				t.Fatalf("charged %d, owed %d", got, want)
			}
		})
	}
	failures := map[string]func(e *enclave, tab *decidetest.Table) (failed func(int) bool){
		"a malformed answer": func(e *enclave, tab *decidetest.Table) func(int) bool {
			measured(e, tab, nil)
			tab.Fail(decidetest.Invalid)
			return first
		},
		"a probability that is not one": func(e *enclave, tab *decidetest.Table) func(int) bool {
			measured(e, tab, func(id string, a query.Atom, p float64) float64 {
				if id == e.canary.Messages[0].ID && a == e.canary.Messages[0].Atoms[0].Atom() {
					return 1.5
				}
				return p
			})
			return func(int) bool { return false }
		},
	}
	for name, fail := range failures {
		t.Run(name+" is failed, never changed", func(t *testing.T) {
			t.Parallel()
			w, e, pl := canaryOf(t)
			failed := fail(e, pl.tab)
			if err := e.RunCanary(ctx); !errors.Is(err, errCanaryFailed) {
				t.Fatal(err)
			}
			if st := e.Status(ctx).Canary; st.State != attestation.CanaryFailed {
				t.Fatalf("%+v", st)
			}
			if w.alarms().count(AlarmCanaryChanged) != 0 {
				t.Fatal("an alarm for a failed run")
			}
			if got, want := spent(e), owed(pl.tab.Calls(), failed); got != want {
				t.Fatalf("charged %d, owed %d", got, want)
			}
		})
	}
}
