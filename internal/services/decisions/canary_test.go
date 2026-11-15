package decisions

import (
	"context"
	"crypto/sha256"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// answersAsMeasured makes e's model answer the canary as it was measured,
// each probability passed through move.
func answersAsMeasured(e *enclave, move func(m string, a query.Atom, p float64) float64) {
	expected := map[string]map[query.Atom]float64{}
	ids := map[string]string{}
	for _, m := range e.canary.Messages {
		expected[m.Subject] = map[query.Atom]float64{}
		ids[m.Subject] = m.ID
		for _, a := range m.Atoms {
			expected[m.Subject][a.Atom()] = a.P
		}
	}
	e.model.onCall(func(_ context.Context, _ int, c decide.Call) (*decide.Result, error) {
		exp, ok := expected[c.Content.Subject]
		if !ok {
			return nil, nil // not the canary's
		}
		res := &decide.Result{Model: "gpt-6-luna", Usage: decide.Usage{InputTokens: 900, Reported: true}}
		for _, a := range c.Statements {
			p := exp[a]
			if move != nil {
				p = move(ids[c.Content.Subject], a, p)
			}
			res.Answers = append(res.Answers, decide.Answer{P: p, OK: true})
		}
		return res, nil
	})
}

func (e *enclave) canaryState(t *testing.T) *canaryDoc {
	t.Helper()
	var d canaryDoc
	if _, found, err := readRecord(context.Background(), e.Service, e.canaryName(), purposeCanary, &d); err != nil || !found {
		t.Fatalf("the canary's state: %v, found %v", err, found)
	}
	return &d
}

func (e *enclave) canaryStateNow(ctx context.Context) string {
	e.canaryMu.Lock()
	e.doc, e.readAt = nil, time.Time{}
	e.canaryMu.Unlock()
	return e.Status(ctx).Canary.State
}

// A20. The canary.
func TestA20TheCanary(t *testing.T) {
	ctx := context.Background()
	today := "2026-11-16"
	t.Run("the same answers are same, each attempt reserved", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		answersAsMeasured(e, nil)
		if err := e.RunCanary(ctx); err != nil {
			t.Fatal(err)
		}
		if e.model.count() != 8 {
			t.Fatalf("%d calls", e.model.count())
		}
		st := e.Status(ctx).Canary
		if st.State != attestation.CanarySame || !st.CheckedAt.Equal(t0) || st.Baseline != e.canary.Baseline {
			t.Fatalf("%+v", st)
		}
		if e.Status(ctx).Calls["canary"] != 8 || len(w.spend(month).Grants) != 1 {
			t.Fatal("not reserved through the guard")
		}
		if checked, change := e.canaryDays(); checked != today || change != "" {
			t.Fatalf("%s %s", checked, change)
		}
	})
	changes := map[string]func(e *enclave){
		"another model": func(e *enclave) {
			answersAsMeasured(e, nil)
			prev := e.model.fail
			e.model.onCall(func(ctx context.Context, n int, c decide.Call) (*decide.Result, error) {
				if n == 3 {
					return nil, &decide.AttemptError{Err: decide.ErrModelChanged, Sent: true}
				}
				return prev(ctx, n, c)
			})
		},
		"one answer moved by 0.03": func(e *enclave) {
			answersAsMeasured(e, func(m string, a query.Atom, p float64) float64 {
				if m == "inv08" && a == about("invoices") {
					return max(p-0.03, 0)
				}
				return p
			})
		},
		"a refusal where an answer was measured": func(e *enclave) {
			answersAsMeasured(e, nil)
			prev := e.model.fail
			e.model.onCall(func(ctx context.Context, n int, c decide.Call) (*decide.Result, error) {
				r, err := prev(ctx, n, c)
				if n == 1 {
					r.Answers[0] = decide.Answer{Refused: true}
				}
				return r, err
			})
		},
	}
	for _, step := range [][2]float64{{0.28, 0.30}, {0.20, 0.21}, {0.79, 0.80}, {0.69, 0.70}} {
		for _, back := range []bool{false, true} {
			from, to := step[0], step[1]
			if back {
				from, to = to, from
			}
			changes["across a threshold "+ftoa(from)+" to "+ftoa(to)] = func(e *enclave) {
				c := *e.canary
				c.Messages = slices.Clone(c.Messages)
				m := c.Messages[0]
				m.Atoms = slices.Clone(m.Atoms)
				m.Atoms[0].P = from
				c.Messages[0] = m
				e.canary = &c
				answersAsMeasured(e, func(id string, a query.Atom, p float64) float64 {
					if id == m.ID && a == m.Atoms[0].Atom() {
						return to
					}
					return p
				})
			}
		}
	}
	for name, change := range changes {
		t.Run("changed: "+name, func(t *testing.T) {
			w := newWorld(t)
			e := w.keyed()
			change(e)
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
		})
	}
	t.Run("a malformed answer is failed, never changed; both charged their estimate", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		answersAsMeasured(e, nil)
		prev := e.model.fail
		e.model.onCall(func(ctx context.Context, n int, c decide.Call) (*decide.Result, error) {
			if n == 2 {
				return nil, &decide.AttemptError{Err: decide.ErrInvalidAnswer, Sent: true}
			}
			return prev(ctx, n, c)
		})
		if err := e.RunCanary(ctx); !errors.Is(err, errCanaryFailed) {
			t.Fatal(err)
		}
		if st := e.Status(ctx).Canary; st.State != attestation.CanaryFailed {
			t.Fatalf("%+v", st)
		}
		if w.alarms().count(AlarmCanaryChanged) != 0 {
			t.Fatal("an alarm for a failed run")
		}
		e.guard.mu.Lock()
		used := e.guard.grants[0].used
		e.guard.mu.Unlock()
		c1, c2 := canaryCalls(e)
		if used != cost(decide.Usage{InputTokens: 900})+estimate(c2) || c1.Content.Subject == "" {
			t.Fatalf("charged %d", used)
		}
		// An answer that is not a number between 0 and 1 is a failed run too.
		w2 := newWorld(t)
		e2 := w2.keyed()
		answersAsMeasured(e2, func(string, query.Atom, float64) float64 { return 0.5 })
		prev2 := e2.model.fail
		e2.model.onCall(func(ctx context.Context, n int, c decide.Call) (*decide.Result, error) {
			r, err := prev2(ctx, n, c)
			r.Answers[0] = decide.Answer{}
			return r, err
		})
		if err := e2.RunCanary(ctx); !errors.Is(err, errCanaryFailed) {
			t.Fatal(err)
		}
	})
	t.Run("the lease put back while A runs: B runs too, both complete", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.keyed(), w.keyed()
		answersAsMeasured(a, nil)
		answersAsMeasured(b, nil)
		paused, resume := make(chan struct{}), make(chan struct{})
		prev := a.model.fail
		a.model.onCall(func(ctx context.Context, n int, c decide.Call) (*decide.Result, error) {
			if n == 1 {
				close(paused)
				<-resume
			}
			return prev(ctx, n, c)
		})
		done := make(chan error, 1)
		go func() { done <- a.RunCanary(ctx) }()
		select {
		case <-paused:
		case err := <-done:
			t.Fatalf("A ended before its first call: %v", err)
		}
		// Whoever writes the table puts back an expired lease.
		key := "lease#" + a.canaryName()
		lease, err := w.records.GetEntry(ctx, leasePartition, key)
		if err != nil {
			t.Fatal(err)
		}
		expired, _ := b.sealLease(ctx, key, t0.Add(-time.Minute))
		lease.Value = expired
		w.records.SetEntry(*lease)
		if err := b.RunCanary(ctx); err != nil {
			t.Fatal(err)
		}
		w.advance(CanaryLease) // A resumes after B took the lease over, and renews
		close(resume)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if d := a.canaryState(t); len(d.Checks) != 2 {
			t.Fatalf("checks %+v", d.Checks)
		}
		if a.model.count() != 8 || b.model.count() != 8 {
			t.Fatalf("calls %d %d", a.model.count(), b.model.count())
		}
	})
	t.Run("changed then same, in both orders, stays changed", func(t *testing.T) {
		for _, changedFirst := range []bool{true, false} {
			w := newWorld(t)
			a, b := w.keyed(), w.keyed()
			moved := func(string, query.Atom, float64) float64 { return 0.5 }
			if changedFirst {
				answersAsMeasured(a, moved)
				answersAsMeasured(b, nil)
			} else {
				answersAsMeasured(a, nil)
				answersAsMeasured(b, moved)
			}
			// Both run the day's check, as under a replayed lease; B reads
			// the state after A's completion and adds its own to it. The
			// write that meets ErrChanged is
			// TestA20ACompletionRetriedAfterErrChanged.
			paused, resume := make(chan struct{}), make(chan struct{})
			prev := b.model.fail
			b.model.onCall(func(ctx context.Context, n int, c decide.Call) (*decide.Result, error) {
				if n == 8 {
					close(paused)
					<-resume
				}
				return prev(ctx, n, c)
			})
			done := make(chan error, 1)
			go func() { done <- b.RunCanary(ctx) }()
			select {
			case <-paused:
			case err := <-done:
				t.Fatalf("B ended before its last call: %v", err)
			}
			w.advance(CanaryLease) // B's lease expires while it waits: A takes it over
			if err := a.RunCanary(ctx); err != nil {
				t.Fatal(err)
			}
			close(resume)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			d := a.canaryState(t)
			if len(d.Checks) != 2 || !d.Changed || d.ChangeSeen != today || w.alarms().count(AlarmCanaryChanged) != 1 {
				t.Fatalf("changed first %v: %+v", changedFirst, d)
			}
			if a.canaryStateNow(ctx) != attestation.CanaryChanged {
				t.Fatal("not changed")
			}
		}
	})
	t.Run("once a check completed today no enclave starts another; honest enclaves run one a day", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.keyed(), w.keyed()
		answersAsMeasured(a, nil)
		answersAsMeasured(b, nil)
		_ = a.RunCanary(ctx)
		_ = b.RunCanary(ctx)
		_ = a.RunCanary(ctx)
		w.advance(CanaryLease + time.Minute)
		_ = b.RunCanary(ctx)
		if a.model.count()+b.model.count() != 8 || len(a.canaryState(t).Checks) != 1 {
			t.Fatalf("calls %d %d", a.model.count(), b.model.count())
		}
		w.advance(24 * time.Hour)
		_ = b.RunCanary(ctx)
		_ = a.RunCanary(ctx)
		if a.model.count()+b.model.count() != 16 || len(a.canaryState(t).Checks) != 2 {
			t.Fatalf("calls %d %d", a.model.count(), b.model.count())
		}
	})
	t.Run("an enclave that takes the lease and stops leaves the day to another once it expires", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.keyed(), w.keyed()
		answersAsMeasured(b, nil)
		a.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) {
			return nil, &decide.AttemptError{Err: decide.ErrRequest, Sent: true} // stands for the stop
		})
		_ = a.RunCanary(ctx)
		_ = b.RunCanary(ctx)
		if b.model.count() != 0 {
			t.Fatal("ran under another's lease")
		}
		w.advance(CanaryLease)
		if err := b.RunCanary(ctx); err != nil || b.model.count() != 8 {
			t.Fatalf("%v, %d calls", err, b.model.count())
		}
	})
	t.Run("no completed check for two days is stale and makes canary_failed due", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) {
			return nil, &decide.AttemptError{Err: decide.ErrRequest, Sent: true} // every run of the first days crashes
		})
		_ = e.RunCanary(ctx)
		w.advance(24 * time.Hour)
		_ = e.RunCanary(ctx)
		if e.canaryStateNow(ctx) == attestation.CanaryStale || w.alarms().count(AlarmCanaryFailed) != 0 {
			t.Fatal("stale after one day")
		}
		w.advance(24 * time.Hour)
		_ = e.RunCanary(ctx)
		if e.canaryStateNow(ctx) != attestation.CanaryStale {
			t.Fatal("not stale, counted from since")
		}
		if a, ok := w.alarms().has(AlarmCanaryFailed, "2026-11-18"); !ok || a.Baseline != e.canary.Baseline {
			t.Fatalf("%+v", w.alarms().Entries)
		}
		answersAsMeasured(e, nil)
		w.advance(CanaryLease) // the failed run's lease ends first
		_ = e.RunCanary(ctx)
		if e.canaryStateNow(ctx) != attestation.CanarySame {
			t.Fatal("not same after a check")
		}
		// Counted from the last completed check: every day's lease taken
		// and never completed.
		e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) {
			return nil, &decide.AttemptError{Err: decide.ErrRequest, Sent: true}
		})
		for range 2 {
			w.advance(24 * time.Hour)
			_ = e.RunCanary(ctx)
		}
		if e.canaryStateNow(ctx) != attestation.CanaryStale || w.alarms().count(AlarmCanaryFailed) != 2 {
			t.Fatalf("%+v", w.alarms().Entries)
		}
	})
	t.Run("two baselines at once each keep their own record, lease and alarm", func(t *testing.T) {
		w := newWorld(t)
		oldB, newB := w.keyed(), w.keyed()
		c := *newB.canary
		c.Baseline = strings.Repeat("ab", 32)
		newB.canary = &c
		answersAsMeasured(oldB, func(string, query.Atom, float64) float64 { return 0.5 })
		answersAsMeasured(newB, nil)
		if oldB.canaryName() == newB.canaryName() {
			t.Fatal("one record")
		}
		_ = oldB.RunCanary(ctx)
		_ = newB.RunCanary(ctx)
		if !oldB.canaryState(t).Changed || newB.canaryState(t).Changed || newB.model.count() != 8 {
			t.Fatal("one baseline's checks touched the other's")
		}
		if newB.canaryStateNow(ctx) != attestation.CanarySame || oldB.canaryStateNow(ctx) != attestation.CanaryChanged {
			t.Fatal("states")
		}
	})
	t.Run("the retired baseline's alarm is sent by the new baseline", func(t *testing.T) {
		w := newWorld(t)
		oldB := w.keyed()
		answersAsMeasured(oldB, func(string, query.Atom, float64) float64 { return 0.5 })
		w.alarm.fail(true)
		_ = oldB.RunCanary(ctx)
		_ = oldB.RunAlarms(ctx)
		// The old baseline's last enclave stops; only the new one runs.
		newB := w.keyed()
		c := *newB.canary
		c.Baseline = strings.Repeat("cd", 32)
		newB.canary = &c
		w.advance(26 * time.Hour)
		w.alarm.fail(false)
		if err := newB.RunAlarms(ctx); err != nil {
			t.Fatal(err)
		}
		if got := w.alarm.sent(); !slices.Equal(got, []string{"canary_changed " + today}) {
			t.Fatalf("sent %v", got)
		}
		if a, _ := w.alarms().has(AlarmCanaryChanged, today); a.SentAt.IsZero() {
			t.Fatal("the sent mark was not kept")
		}
	})
	t.Run("the crash between adding the alarm and writing the completion", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		answersAsMeasured(e, func(string, query.Atom, float64) float64 { return 0.5 })
		if _, err := e.readCanary(ctx, true); err != nil {
			t.Fatal(err)
		}
		e.records.onPut(func(name string) error {
			if name == e.canaryName() {
				return errors.New("the enclave stopped")
			}
			return nil
		})
		if err := e.RunCanary(ctx); !errors.Is(err, errCanaryFailed) {
			t.Fatal(err)
		}
		if w.alarms().count(AlarmCanaryChanged) != 1 || len(e.canaryState(t).Checks) != 0 {
			t.Fatal("the alarm first, and no completion")
		}
		restarted := w.keyed()
		answersAsMeasured(restarted, func(string, query.Atom, float64) float64 { return 0.5 })
		if err := restarted.RunAlarms(ctx); err != nil || len(w.alarm.sent()) != 1 {
			t.Fatalf("%v, %v", err, w.alarm.sent())
		}
		w.advance(CanaryLease)
		if err := restarted.RunCanary(ctx); err != nil {
			t.Fatal(err)
		}
		if w.alarms().count(AlarmCanaryChanged) != 1 || len(restarted.canaryState(t).Checks) != 1 || restarted.model.count() != 8 {
			t.Fatalf("%+v", w.alarms().Entries)
		}
	})
	t.Run("a run whose alarm cannot be added writes no completion and is failed", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		answersAsMeasured(e, func(string, query.Atom, float64) float64 { return 0.5 })
		e.records.onPut(func(name string) error {
			if name == RecordAlarms {
				return errors.New("storage")
			}
			return nil
		})
		if err := e.RunCanary(ctx); !errors.Is(err, errCanaryFailed) {
			t.Fatal(err)
		}
		if d := e.canaryState(t); len(d.Checks) != 0 {
			t.Fatalf("%+v", d)
		}
		if e.canaryStateNow(ctx) != attestation.CanaryFailed {
			t.Fatal("not failed")
		}
	})
	t.Run("an earlier copy of the state is refused; a state that cannot be read is unreadable", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		answersAsMeasured(e, nil)
		_ = e.RunCanary(ctx)
		same, _ := w.records.GetRecord(ctx, w.versions(e.canaryName())[0])
		w.advance(24 * time.Hour)
		answersAsMeasured(e, func(string, query.Atom, float64) float64 { return 0.5 })
		_ = e.RunCanary(ctx)
		current := w.versions(e.canaryName())[0]
		w.records.SetRecord(store.Record{Name: current, Value: same.Value})
		if e.canaryStateNow(ctx) != attestation.CanaryUnreadable {
			t.Fatal("an earlier copy was taken")
		}
		if err := e.RunCanary(ctx); err == nil {
			t.Fatal("ran over an unreadable state")
		}
		w2 := newWorld(t)
		e2 := w2.keyed()
		answersAsMeasured(e2, nil)
		_ = e2.RunCanary(ctx)
		w2.commits.Fork(e2.canaryName(), 1, sha256.Sum256([]byte("x")))
		if e2.canaryStateNow(ctx) != attestation.CanaryUnreadable {
			t.Fatal("forked")
		}
	})
	t.Run("asking goes on after a change", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		answersAsMeasured(e, func(string, query.Atom, float64) float64 { return 0.5 })
		_ = e.RunCanary(ctx)
		out := e.ask(newQuestion("m1", "Hi", "", subj("x")))
		if out.Outcome != inbox.AskAnswered || len(out.Answers) != 1 {
			t.Fatalf("%+v", out)
		}
		d, _ := w.kept.read(testBox.MailboxID + "/m1")
		if d.Answers[0].CanaryChecked != today || d.Answers[0].CanaryChange != today {
			t.Fatalf("%+v", d.Answers[0])
		}
	})
	t.Run("no canary without a model; one run at a time", func(t *testing.T) {
		w := newWorld(t)
		e := w.enclave(withoutModel())
		if err := e.RunCanary(ctx); err != nil || e.Status(ctx).Canary != nil {
			t.Fatal("ran")
		}
		k := w.keyed()
		k.canaryMu.Lock()
		k.running = true
		k.canaryMu.Unlock()
		if err := k.RunCanary(ctx); err != nil || k.model.count() != 0 {
			t.Fatal("ran twice at once")
		}
		var wg sync.WaitGroup
		wg.Wait()
	})
}

func canaryCalls(e *enclave) (decide.Call, decide.Call) {
	e.model.mu.Lock()
	defer e.model.mu.Unlock()
	return e.model.calls[0], e.model.calls[1]
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }

// A20. A run that did not complete is not run again by its enclave inside
// the lease it took (the review of pull request 2, finding 5).
func TestA20AFailedRunIsNotRunAgainInsideItsLease(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	e := w.keyed()
	e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) {
		return nil, &decide.AttemptError{Err: decide.ErrRequest, Sent: true}
	})
	_ = e.RunCanary(ctx)
	_ = e.RunCanary(ctx)
	w.advance(CanaryLease - time.Second)
	_ = e.RunCanary(ctx)
	if e.model.count() != 1 {
		t.Fatalf("%d runs inside one lease", e.model.count())
	}
	if got := e.Status(ctx).Canary.State; got != attestation.CanaryFailed {
		t.Fatalf("state %s", got)
	}
	w.advance(time.Second)
	answersAsMeasured(e, nil)
	if err := e.RunCanary(ctx); err != nil || e.model.count() != 9 {
		t.Fatalf("not run once the lease ended: %v, %d calls", err, e.model.count())
	}
}

// failingPurpose derives every key but one purpose's, while fail is set.
type failingPurpose struct {
	testKeys
	purpose string
	fail    *bool
}

func (f failingPurpose) Derive(ctx context.Context, purpose string) ([]byte, error) {
	if *f.fail && purpose == f.purpose {
		return nil, errors.New("no root secret")
	}
	return f.testKeys.Derive(ctx, purpose)
}

// A20. A state whose read fails is unreadable on /status, whatever this
// enclave last read; a read that succeeds again clears it (the review of
// pull request 2, finding 6).
func TestA20AStateWhoseReadFailsIsUnreadable(t *testing.T) {
	ctx := context.Background()
	timedOut := func(e *enclave) func(string, int) (bool, *store.Record, store.Mark, error) {
		return func(name string, _ int) (bool, *store.Record, store.Mark, error) {
			if name == e.canaryName() {
				return true, nil, store.Mark{}, errors.New("storage timed out")
			}
			return false, nil, store.Mark{}, nil
		}
	}
	t.Run("a storage timeout after a check, then recovery", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		answersAsMeasured(e, nil)
		if err := e.RunCanary(ctx); err != nil {
			t.Fatal(err)
		}
		w.advance(canaryFresh)
		e.records.onGet(timedOut(e))
		if got := e.Status(ctx).Canary; got.State != attestation.CanaryUnreadable || !got.CheckedAt.Equal(t0) {
			t.Fatalf("a failed refresh still reports %+v", got)
		}
		e.records.onGet(nil)
		w.advance(canaryFresh)
		if got := e.Status(ctx).Canary.State; got != attestation.CanarySame {
			t.Fatalf("not cleared by a read that succeeds: %s", got)
		}
	})
	t.Run("a storage timeout with nothing read before", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		answersAsMeasured(e, nil)
		if err := e.RunCanary(ctx); err != nil {
			t.Fatal(err)
		}
		other := w.keyed()
		other.records.onGet(timedOut(other))
		if got := other.Status(ctx).Canary.State; got != attestation.CanaryUnreadable {
			t.Fatalf("a failed first read reports %s", got)
		}
	})
	t.Run("a key that cannot be derived", func(t *testing.T) {
		w := newWorld(t)
		fail := false
		e := w.keyed(withKeys(failingPurpose{purpose: purposeCanary, fail: &fail}))
		answersAsMeasured(e, nil)
		if err := e.RunCanary(ctx); err != nil {
			t.Fatal(err)
		}
		w.advance(canaryFresh)
		fail = true
		if got := e.Status(ctx).Canary.State; got != attestation.CanaryUnreadable {
			t.Fatalf("a failed derivation reports %s", got)
		}
		fail = false
		w.advance(canaryFresh)
		if got := e.Status(ctx).Canary.State; got != attestation.CanarySame {
			t.Fatalf("not cleared: %s", got)
		}
	})
}

// A20. A completion whose write meets another enclave's completion
// (store.ErrChanged) reads again and adds its check to what it finds, in
// both orders of changed and same (the review of pull request 2, finding
// 7: the earlier schedule never made the conflict).
func TestA20ACompletionRetriedAfterErrChanged(t *testing.T) {
	ctx := context.Background()
	today := "2026-11-16"
	for _, changedFirst := range []bool{true, false} {
		t.Run("changed first "+strconv.FormatBool(changedFirst), func(t *testing.T) {
			w := newWorld(t)
			a, b := w.keyed(), w.keyed()
			moved := func(string, query.Atom, float64) float64 { return 0.5 }
			if changedFirst {
				answersAsMeasured(a, moved)
				answersAsMeasured(b, nil)
			} else {
				answersAsMeasured(a, nil)
				answersAsMeasured(b, moved)
			}
			if _, err := b.readCanary(ctx, true); err != nil {
				t.Fatal(err)
			}
			// B has read the state for its completion; before its write, A's
			// completion lands, so B's write at the version it read fails.
			writes := 0
			b.records.onPut(func(name string) error {
				if name != b.canaryName() {
					return nil
				}
				writes++
				if writes == 1 {
					if err := a.check(ctx, today); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			})
			if err := b.check(ctx, today); err != nil {
				t.Fatalf("the completion was not retried: %v", err)
			}
			if writes != 2 {
				t.Fatalf("%d writes: no conflict was made, or it was not retried", writes)
			}
			d := a.canaryState(t)
			if len(d.Checks) != 2 || !d.Changed || d.ChangeSeen != today || w.alarms().count(AlarmCanaryChanged) != 1 {
				t.Fatalf("%+v, alarms %+v", d, w.alarms().Entries)
			}
		})
	}
}
