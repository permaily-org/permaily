package decisions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
)

func TestNewNeedsWhatItIsGiven(t *testing.T) {
	w := newWorld(t)
	good := Config{Kept: w.kept, Records: &store.Anchored{Records: w.records, Commits: w.commits, Strict: true}, Entries: w.records, Keys: testKeys{}, Limits: DefaultLimits}
	for name, mutate := range map[string]func(*Config){
		"no records":   func(c *Config) { c.Records = nil },
		"no entries":   func(c *Config) { c.Entries = nil },
		"no keys":      func(c *Config) { c.Keys = nil },
		"no kept":      func(c *Config) { c.Kept = nil },
		"no month":     func(c *Config) { c.Limits.Month = 0 },
		"no block":     func(c *Config) { c.Limits.Block = 0 },
		"no day alarm": func(c *Config) { c.Limits.DayAlarm = 0 },
		"no share":     func(c *Config) { c.Limits.AccountDay = 0 },
	} {
		c := good
		mutate(&c)
		if _, err := New(c); err == nil {
			t.Errorf("%s: made", name)
		}
	}
	s, err := New(good)
	if err != nil || s.cfg.Log == nil || s.cfg.Now == nil || s.model != nil {
		t.Fatalf("defaults: %v", err)
	}
	st := s.Status(context.Background())
	if st.Model != "" || st.Canary != nil || st.PromptCache != "off" || st.PromptVersion != decide.PromptVersion || st.MonthLimitUSD != 50 || st.DayAlarmUSD != 10 || st.AccountDayUSD != 0.5 {
		t.Fatalf("%+v", st)
	}
	for _, kind := range attestation.ModelCalls {
		if _, ok := st.Calls[kind]; !ok {
			t.Fatalf("%s is not on /status", kind)
		}
	}
}

func TestStatusOfAKeyedService(t *testing.T) {
	w := newWorld(t)
	e := w.keyed()
	st := e.Status(context.Background())
	if st.Model != "gpt-6-luna" || !st.KeySet || !st.KeySetAt.Equal(t0) || st.Guard != attestation.ModelGuardOK || st.Canary.State != attestation.CanaryNone {
		t.Fatalf("%+v %+v", st, st.Canary)
	}
}

func TestSleepAndJitter(t *testing.T) {
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if randomJitter(0) != 0 {
		t.Fatal("jitter of nothing")
	}
	for range 100 {
		if d := randomJitter(time.Second); d < 0 || d >= time.Second {
			t.Fatal(d)
		}
	}
}

func TestStorageFailuresAreNotAnswers(t *testing.T) {
	ctx := context.Background()
	t.Run("keys that cannot be derived", func(t *testing.T) {
		w := newWorld(t)
		w.seedSpend(month, "2026-11-01", 1)
		e := w.enclave(withKeys(failingKeys{}))
		var d spendDoc
		if _, _, err := readRecord(ctx, e.Service, RecordSpend+month, purposeSpend, &d); err == nil || errors.Is(err, errUnreadable) {
			t.Fatal(err)
		}
		if _, err := e.Key(ctx); err != nil {
			t.Fatal(err)
		}
		if err := e.RunAlarms(ctx); err == nil {
			t.Fatal("ran")
		}
	})
	t.Run("a spend record that cannot be read is held, and so is one the check cannot read", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.reserveOnce(1, "")
		e.records.onGet(func(name string, _ int) (bool, *store.Record, store.Mark, error) {
			if strings.HasPrefix(name, RecordSpend) {
				return true, nil, store.Mark{}, errors.New("storage")
			}
			return false, nil, store.Mark{}, nil
		})
		w.advance(spendCheck)
		if h := e.reserveOnce(1, ""); h != holdLimit {
			t.Fatal(h)
		}
		e.guard.mu.Lock()
		e.guard.grants = nil
		e.guard.mu.Unlock()
		if h := e.reserveOnce(1, ""); h != holdLimit {
			t.Fatal(h)
		}
		e.records.onGet(nil)
		if h := e.reserveOnce(1, ""); h != holdNone {
			t.Fatal(h)
		}
	})
	t.Run("a grant written over a fork", func(t *testing.T) {
		w := newWorld(t)
		w.seedSpend(month, "2026-11-01", 1)
		e := w.keyed()
		e.commits.onAppend(func(c store.Commit, inner func(store.Commit) (*store.Commit, error)) (*store.Commit, error) {
			w.commits.Fork(c.Name, c.Version-1, [32]byte{1})
			return nil, store.ErrExists
		})
		if h := e.reserveOnce(1, ""); h != holdLimit || e.Status(ctx).Guard != attestation.ModelGuardUnreadable {
			t.Fatal(h)
		}
	})
	t.Run("the canary's lease and state when the storage fails", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		answersAsMeasured(e, nil)
		if _, err := e.readCanary(ctx, true); err != nil {
			t.Fatal(err)
		}
		bad := &failingEntries{Memory: w.records}
		e.cfg.Entries = bad
		bad.get = errors.New("storage")
		if err := e.RunCanary(ctx); err == nil {
			t.Fatal("no error")
		}
		bad.get, bad.put = nil, errors.New("storage")
		if err := e.RunCanary(ctx); err == nil {
			t.Fatal("no error")
		}
		e.cfg.Entries = w.records
		e.records.onGet(func(name string, n int) (bool, *store.Record, store.Mark, error) {
			if name == e.canaryName() {
				return true, nil, store.Mark{}, errors.New("storage")
			}
			return false, nil, store.Mark{}, nil
		})
		if err := e.RunCanary(ctx); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("a question with no text function", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		q := newQuestion("m1", "Hi", "", subj("x"), about("x"))
		q.Text = nil
		out := e.ask(q)
		if len(out.Answers) != 1 || out.Outcome != inbox.AskAnswered {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("an answer that does not match the call", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) {
			return &decide.Result{Model: "gpt-6-luna"}, nil
		})
		if out := e.ask(newQuestion("m1", "Hi", "", subj("x"))); len(out.Answers) != 0 || e.Status(ctx).Calls["invalid"] != 1 {
			t.Fatalf("%+v", out)
		}
		e.model.onCall(func(_ context.Context, _ int, c decide.Call) (*decide.Result, error) {
			return &decide.Result{Model: "gpt-7", Answers: make([]decide.Answer, len(c.Statements))}, nil
		})
		if out := e.ask(newQuestion("m2", "Hi", "", subj("x"))); len(out.Answers) != 0 || e.Status(ctx).Calls["other_model"] != 1 {
			t.Fatalf("%+v", out)
		}
	})
}

// failingEntries is the table with its entries failing on demand.
type failingEntries struct {
	*store.Memory
	get, put error
}

func (f *failingEntries) GetEntry(ctx context.Context, p, k string) (*store.Entry, error) {
	if f.get != nil {
		return nil, f.get
	}
	return f.Memory.GetEntry(ctx, p, k)
}

func (f *failingEntries) PutEntry(ctx context.Context, e *store.Entry) error {
	if f.put != nil {
		return f.put
	}
	return f.Memory.PutEntry(ctx, e)
}
