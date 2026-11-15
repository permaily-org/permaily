package decisions

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
)

const month = "2026-11"

var keyFP = fingerprint(testKey)

const block = 100_000_000

// reserveOnce reserves est for account and settles it at est.
func (e *enclave) reserveOnce(est int64, account string) hold {
	r, h := e.reserve(context.Background(), est, account, keyFP)
	if r != nil {
		r.settle(est)
	}
	return h
}

// A14. The month, in a strict anchored record.
func TestA14TheMonthInAStrictAnchoredRecord(t *testing.T) {
	ctx := context.Background()
	t.Run("grants at the version read, made with CreateNew, never past Month", func(t *testing.T) {
		w := newWorld(t)
		limits := Limits{Month: 20 * block, DayAlarm: 1000 * block, AccountDay: 1000 * block, Block: block}
		a, b := w.keyed(withLimits(limits)), w.keyed(withLimits(limits))
		var wg sync.WaitGroup
		var mu sync.Mutex
		reserved := int64(0)
		for i := range 40 {
			e := a
			if i%2 == 1 {
				e = b
			}
			wg.Go(func() {
				r, h := e.reserve(ctx, block, "", keyFP)
				if h == holdNone {
					mu.Lock()
					reserved += block
					mu.Unlock()
					r.settle(block)
				}
			})
		}
		wg.Wait()
		d := w.spend(month)
		if d.total()*block > limits.Month || reserved > limits.Month || reserved == 0 {
			t.Fatalf("record %d blocks, reserved %d", d.total(), reserved)
		}
		ids := map[string]bool{}
		for _, g := range d.Grants {
			if ids[g.ID] || len(g.ID) != 32 {
				t.Fatalf("grant identities %+v", d.Grants)
			}
			ids[g.ID] = true
		}
	})
	t.Run("read again on ErrChanged at most RecordTries times", func(t *testing.T) {
		w := newWorld(t)
		a, other := w.keyed(), w.keyed()
		writes := 0
		a.records.onPut(func(name string) error {
			if name != RecordSpend+month {
				return nil
			}
			writes++
			other.reserveOnce(block, "") // another enclave writes first, every time
			other.guard.mu.Lock()
			other.guard.grants = nil // so it makes a grant again
			other.guard.mu.Unlock()
			return nil
		})
		if h := a.reserveOnce(block, ""); h != holdLimit {
			t.Fatalf("hold %v", h)
		}
		if writes != RecordTries {
			t.Fatalf("%d writes", writes)
		}
	})
	t.Run("nothing is sent from a grant before a fresh read after its write lists it", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.records.onGet(func(name string, n int) (bool, *store.Record, store.Mark, error) {
			if name == RecordSpend+month && n == 2 { // the confirming read
				return true, nil, store.Mark{}, errors.New("storage")
			}
			return false, nil, store.Mark{}, nil
		})
		out := e.ask(newQuestion("m1", "Hi", "", subj("x")))
		if e.model.count() != 0 || out.Outcome != inbox.AskLimit {
			t.Fatalf("%+v, %d calls", out, e.model.count())
		}
		// That grant is never spent from, even now that a read lists it.
		e.records.onGet(nil)
		if d := w.spend(month); len(d.Grants) != 1 {
			t.Fatalf("grants %+v", d.Grants)
		}
		e.ask(newQuestion("m1", "Hi", "", subj("x")))
		if d := w.spend(month); len(d.Grants) != 2 || e.model.count() != 1 {
			t.Fatalf("grants %+v, %d calls", d.Grants, e.model.count())
		}
	})
	t.Run("a confirming read that does not list the grant", func(t *testing.T) {
		w := newWorld(t)
		w.seedSpend(month, "2026-11-01", 1)
		e := w.keyed()
		var before *store.Record
		var beforeAt store.Mark
		e.records.onGet(func(name string, n int) (bool, *store.Record, store.Mark, error) {
			if name != RecordSpend+month {
				return false, nil, store.Mark{}, nil
			}
			if n == 1 {
				before, beforeAt, _ = e.anchored.GetRecordAt(ctx, name)
				return true, before, beforeAt, nil
			}
			return true, before, beforeAt, nil // an earlier version, without the grant
		})
		if h := e.reserveOnce(block, ""); h != holdLimit {
			t.Fatalf("hold %v", h)
		}
	})
	t.Run("a write that fails after its append landed is burnt", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.commits.onAppend(func(c store.Commit, inner func(store.Commit) (*store.Commit, error)) (*store.Commit, error) {
			if _, err := inner(c); err != nil {
				return nil, err
			}
			return nil, context.DeadlineExceeded
		})
		if h := e.reserveOnce(block, ""); h != holdLimit {
			t.Fatalf("hold %v", h)
		}
		if d := w.spend(month); len(d.Grants) != 1 {
			t.Fatalf("it landed: %+v", d.Grants)
		}
		if h := e.reserveOnce(block, ""); h != holdNone {
			t.Fatalf("hold %v", h)
		}
		if d := w.spend(month); len(d.Grants) != 2 {
			t.Fatalf("a new grant, the landed one never spent from: %+v", d.Grants)
		}
	})
	t.Run("a grant confirmed is in every later version another service writes", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.keyed(), w.keyed()
		a.reserveOnce(block, "")
		first := w.spend(month).Grants[0]
		b.reserveOnce(block, "")
		a.guard.mu.Lock()
		a.guard.grants = nil
		a.guard.mu.Unlock()
		a.reserveOnce(block, "")
		d := w.spend(month)
		if len(d.Grants) != 3 || d.Grants[0] != first {
			t.Fatalf("%+v", d.Grants)
		}
	})
	t.Run("the month's last block, forked", func(t *testing.T) {
		w := newWorld(t)
		w.seedSpend(month, "2026-11-01", 499) // 49.90 USD reserved
		a, b := w.keyed(), w.keyed()
		// B has made its strict check and appends; meanwhile A writes its
		// grant and confirms it, and whoever writes the bucket hides A's
		// commit so that B's append lands at the same version.
		b.commits.onAppend(func(c store.Commit, inner func(store.Commit) (*store.Commit, error)) (*store.Commit, error) {
			if h := a.reserveOnce(block, ""); h != holdNone {
				t.Errorf("A's grant: %v", h)
			}
			w.commits.Fork(c.Name, c.Version, c.Digest)
			return &c, nil
		})
		if h := b.reserveOnce(block, ""); h != holdLimit {
			t.Fatalf("B spent from a forked grant: %v", h)
		}
		if a.model.count()+b.model.count() != 0 {
			t.Fatal("sent")
		}
		// Only A's grant was spent from: what was authorised is 50.00 USD.
		a.guard.mu.Lock()
		spentA := len(a.guard.grants)
		a.guard.mu.Unlock()
		b.guard.mu.Lock()
		spentB := len(b.guard.grants)
		b.guard.mu.Unlock()
		if spentA != 1 || spentB != 0 {
			t.Fatalf("grants held: A %d, B %d", spentA, spentB)
		}
		c := w.keyed()
		out := c.ask(newQuestion("m1", "Hi", "", subj("x")))
		if out.Outcome != inbox.AskLimit || c.Status(ctx).Guard != attestation.ModelGuardUnreadable {
			t.Fatalf("a third service: %+v, %s", out, c.Status(ctx).Guard)
		}
		// From then on A and B too, the rest of A's grant burnt.
		w.advance(spendCheck)
		for name, e := range map[string]*enclave{"A": a, "B": b} {
			if h := e.reserveOnce(1, ""); h != holdLimit || e.Status(ctx).Guard != attestation.ModelGuardUnreadable {
				t.Fatalf("%s: %v, %s", name, h, e.Status(ctx).Guard)
			}
		}
		a.guard.mu.Lock()
		defer a.guard.mu.Unlock()
		if len(a.guard.grants) != 0 {
			t.Fatal("A's grant was not burnt")
		}
	})
	t.Run("the month's last block, A's confirming read after B's append", func(t *testing.T) {
		w := newWorld(t)
		w.seedSpend(month, "2026-11-01", 499)
		a, b := w.keyed(), w.keyed()
		appended, release := make(chan struct{}), make(chan struct{})
		a.commits.onAppend(func(c store.Commit, inner func(store.Commit) (*store.Commit, error)) (*store.Commit, error) {
			stored, err := inner(c)
			close(appended)
			<-release
			return stored, err
		})
		aHold := make(chan hold)
		b.commits.onAppend(func(c store.Commit, inner func(store.Commit) (*store.Commit, error)) (*store.Commit, error) {
			go func() { aHold <- a.reserveOnce(block, "") }()
			<-appended
			w.commits.Fork(c.Name, c.Version, c.Digest)
			return &c, nil
		})
		if h := b.reserveOnce(block, ""); h != holdLimit {
			t.Fatalf("B: %v", h)
		}
		close(release)
		if h := <-aHold; h != holdLimit {
			t.Fatalf("A: %v", h)
		}
	})
	t.Run("at the limit nothing new is asked, kept answers still used", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.ask(newQuestion("m1", "Hi", "", subj("kept")))
		w.seedSpend(month, "2026-11-01", 499)
		e.guard.mu.Lock()
		e.guard.grants = nil
		e.guard.mu.Unlock()
		out := e.ask(newQuestion("m1", "Hi", "", subj("kept"), subj("new")))
		if out.Outcome != inbox.AskLimit || len(out.Held) != 1 || out.Held[0] != subj("new") {
			t.Fatalf("%+v", out)
		}
		if _, ok := out.Answers[subj("kept")]; !ok {
			t.Fatal("a kept answer was not used")
		}
		if e.Status(ctx).Guard != attestation.ModelGuardMonthLimit {
			t.Fatalf("guard %s", e.Status(ctx).Guard)
		}
		if _, ok := w.alarms().has(AlarmMonthLimit, "2026-11-16"); !ok {
			t.Fatal("the month's alarm is not due")
		}
		// The next month asks again.
		w.set(time.Date(2026, 12, 1, 0, 0, 1, 0, time.UTC))
		out = e.ask(newQuestion("m1", "Hi", "", subj("new")))
		if out.Outcome != inbox.AskAnswered || out.Called != 1 || e.Status(ctx).Guard != attestation.ModelGuardOK {
			t.Fatalf("the next month: %+v", out)
		}
	})
	unreadable := func(t *testing.T, w *world, e *enclave) {
		t.Helper()
		out := e.ask(newQuestion("m1", "Hi", "", subj("x")))
		if out.Outcome != inbox.AskLimit || e.model.count() != 0 || e.Status(ctx).Guard != attestation.ModelGuardUnreadable {
			t.Fatalf("%+v, %d calls, %s", out, e.model.count(), e.Status(ctx).Guard)
		}
	}
	t.Run("an earlier copy put back", func(t *testing.T) {
		w := newWorld(t)
		w.seedSpend(month, "2026-11-01", 1)
		earlier, _ := w.records.GetRecord(ctx, w.versions(RecordSpend + month)[0])
		w.seedSpend(month, "2026-11-01", 1)
		current := w.versions(RecordSpend + month)[0]
		w.records.SetRecord(store.Record{Name: current, Value: earlier.Value, WrappedKey: earlier.WrappedKey})
		unreadable(t, w, w.keyed())
	})
	t.Run("a record deleted", func(t *testing.T) {
		w := newWorld(t)
		w.seedSpend(month, "2026-11-01", 1)
		for _, n := range w.versions(RecordSpend + month) {
			_ = w.records.DeleteRecord(ctx, n)
		}
		unreadable(t, w, w.keyed())
	})
	t.Run("a record forked", func(t *testing.T) {
		w := newWorld(t)
		w.seedSpend(month, "2026-11-01", 1)
		w.commits.Fork(RecordSpend+month, 1, sha256.Sum256([]byte("another")))
		e := w.keyed()
		unreadable(t, w, e)
		w.set(time.Date(2026, 11, 30, 23, 0, 0, 0, time.UTC))
		unreadable(t, w, e) // the rest of the month
	})
	t.Run("a record that does not open", func(t *testing.T) {
		w := newWorld(t)
		w.seedSpend(month, "2026-11-01", 1)
		e := w.enclave(withKeys(testKeys{root: "another root secret"}))
		e.records.onGet(nil)
		if _, err := e.SetKey(ctx, keySet()); err != nil {
			t.Fatal(err)
		}
		unreadable(t, w, e)
	})
	t.Run("a grant's rest when the day changes is spent; a late settlement gives nothing back", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		r, h := e.reserve(ctx, 30_000_000, "acc", keyFP)
		if h != holdNone {
			t.Fatal(h)
		}
		w.set(time.Date(2026, 11, 17, 0, 0, 1, 0, time.UTC))
		if h := e.reserveOnce(30_000_000, "acc"); h != holdNone {
			t.Fatal(h)
		}
		r.settle(1_000_000) // late, for the day before
		d := w.spend(month)
		if len(d.Grants) != 2 || d.Grants[0].Day != "2026-11-16" || d.Grants[1].Day != "2026-11-17" {
			t.Fatalf("%+v", d.Grants)
		}
		e.guard.mu.Lock()
		defer e.guard.mu.Unlock()
		if len(e.guard.grants) != 1 || e.guard.grants[0].used != 30_000_000 {
			t.Fatalf("the new grant got something back: %+v", e.guard.grants[0])
		}
	})
	t.Run("a grant's rest when the service stops", func(t *testing.T) {
		w := newWorld(t)
		w.keyed().reserveOnce(1, "")
		w.keyed().reserveOnce(1, "")
		if d := w.spend(month); len(d.Grants) != 2 {
			t.Fatalf("%+v", d.Grants)
		}
	})
}

// A16. The account's share.
func TestA16TheAccountsShare(t *testing.T) {
	w := newWorld(t)
	limits := DefaultLimits
	limits.AccountDay = 10 * decide.Estimate(decide.Call{Content: decide.Content{Kind: decide.ContentSubject, Subject: "Hi"}, Statements: atomsN(1)})
	e := w.keyed(withLimits(limits))
	e.model.reported = false // charged at the estimate
	for i := range 10 {
		q := newQuestion("m"+itoa(i), "Hi", "", subj("x"))
		if out := e.ask(q); out.Outcome != inbox.AskAnswered {
			t.Fatalf("ask %d: %+v", i, out)
		}
	}
	q := newQuestion("m10", "Hi", "", subj("x"))
	if out := e.ask(q); out.Outcome != inbox.AskLimit || len(out.Held) != 1 {
		t.Fatalf("past the share: %+v", out)
	}
	other := newQuestion("m10", "Hi", "", subj("x"))
	other.Account = "acc_two"
	if out := e.ask(other); out.Outcome != inbox.AskAnswered {
		t.Fatalf("another account: %+v", out)
	}
	t.Run("concurrent attempts never pass the share together", func(t *testing.T) {
		w := newWorld(t)
		limits := DefaultLimits
		limits.AccountDay = 5_000_000
		e := w.keyed(withLimits(limits))
		var wg sync.WaitGroup
		var mu sync.Mutex
		total := int64(0)
		for range 20 {
			wg.Go(func() {
				r, h := e.reserve(context.Background(), 1_000_000, "acc", keyFP)
				if h == holdNone {
					mu.Lock()
					total += 1_000_000
					mu.Unlock()
				}
				_ = r
			})
		}
		wg.Wait()
		if total != 5_000_000 {
			t.Fatalf("reserved %d of a 5,000,000 share", total)
		}
	})
}

// A17. The provider's limit and a refused key.
func TestA17TheProvidersLimitAndARefusedKey(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		err     error
		guard   string
		outcome inbox.AskOutcome
		alarm   AlarmKind
	}{
		{"provider limit", decide.ErrProviderLimit, attestation.ModelGuardProviderLimit, inbox.AskLimit, AlarmProviderLimit},
		{"key refused", decide.ErrKeyRefused, attestation.ModelGuardKeyRefused, inbox.AskOff, AlarmKeyRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			e := w.keyed()
			refusing := true
			e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) {
				if refusing {
					return nil, &decide.AttemptError{Err: tc.err, Sent: true}
				}
				return nil, nil
			})
			out := e.ask(newQuestion("m1", "Hi", "", subj("x")))
			if out.Outcome != tc.outcome || out.Called != 1 || e.Status(ctx).Guard != tc.guard {
				t.Fatalf("no retry, the guard's state: %+v, %s", out, e.Status(ctx).Guard)
			}
			if w.alarms().count(tc.alarm) != 1 {
				t.Fatal("no alarm")
			}
			// Nothing new until a probe, one every ten minutes.
			w.advance(5 * time.Minute)
			if out := e.ask(newQuestion("m2", "Hi", "", subj("x"))); out.Called != 0 || out.Outcome != tc.outcome {
				t.Fatalf("asked before the probe: %+v", out)
			}
			w.advance(5 * time.Minute)
			if out := e.ask(newQuestion("m2", "Hi", "", subj("x"))); out.Called != 1 {
				t.Fatalf("no probe: %+v", out)
			}
			if w.alarms().count(tc.alarm) != 1 {
				t.Fatal("a second alarm the same day")
			}
			refusing = false
			w.advance(probeEvery)
			if out := e.ask(newQuestion("m3", "Hi", "", subj("x"))); out.Called != 1 || out.Outcome != inbox.AskAnswered {
				t.Fatalf("the probe that succeeds: %+v", out)
			}
			if out := e.ask(newQuestion("m4", "Hi", "", subj("x"))); out.Called != 1 || e.Status(ctx).Guard != attestation.ModelGuardOK {
				t.Fatalf("asking again: %+v, %s", out, e.Status(ctx).Guard)
			}
		})
	}
	t.Run("a new key is tried at once", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.model.onCall(func(_ context.Context, n int, _ decide.Call) (*decide.Result, error) {
			if n == 1 {
				return nil, &decide.AttemptError{Err: decide.ErrKeyRefused, Sent: true}
			}
			return nil, nil
		})
		e.ask(newQuestion("m1", "Hi", "", subj("x")))
		if _, err := e.SetKey(ctx, keySetOf("sk-another-key")); err != nil {
			t.Fatal(err)
		}
		if out := e.ask(newQuestion("m1", "Hi", "", subj("x"))); out.Called != 1 || out.Outcome != inbox.AskAnswered {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("no key set is AskOff", func(t *testing.T) {
		w := newWorld(t)
		e := w.enclave()
		if out := e.ask(newQuestion("m1", "Hi", "", subj("x"))); out.Outcome != inbox.AskOff {
			t.Fatalf("%+v", out)
		}
		if e.Status(ctx).Guard != attestation.ModelGuardKeyRefused {
			t.Fatal(e.Status(ctx).Guard)
		}
	})
	t.Run("a key removed between the check and the attempt is not sent", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.model.onCall(func(ctx context.Context, n int, c decide.Call) (*decide.Result, error) { return nil, nil })
		e.model.key = func(context.Context) (string, bool) { return "", false }
		out := e.ask(newQuestion("m1", "Hi", "", subj("x")))
		if out.Outcome != inbox.AskOff || e.model.count() != 0 {
			t.Fatalf("%+v", out)
		}
	})
}

// A37. Attempts and their spend, against this package's fake model, whose
// failures return at once: the attempts share the message's deadline and
// are reserved and settled as the record says. The cases in real time
// (two timeouts of 8 s, a request processed and its answer dropped, an
// answer after the client's timeout) need pull request 1's played
// provider, and are added once it merges.
func TestA37AttemptsAndTheirSpend(t *testing.T) {
	ctx := context.Background()
	timeout := &decide.AttemptError{Err: decide.ErrUnavailable, Sent: true, Retry: true}
	t.Run("three attempts share the message's one deadline, the third with what is left of it", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) { return nil, timeout })
		start := time.Now()
		out := e.ask(newQuestion("m1", "Hi", "", subj("x")))
		if out.Called != 3 || len(e.model.ctxs) != 3 {
			t.Fatalf("%+v", out)
		}
		for i, d := range e.model.ctxs {
			if d.IsZero() || d.After(start.Add(decide.MessageDeadline+time.Second)) {
				t.Fatalf("attempt %d's deadline %v is past the message's", i, d)
			}
		}
		if e.model.ctxs[2] != e.model.ctxs[0] {
			t.Fatal("the last attempt does not run with what is left")
		}
	})
	t.Run("a wait that would pass the deadline is not taken", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) {
			return nil, &decide.AttemptError{Err: decide.ErrUnavailable, Sent: true, Retry: true, RetryAfter: decide.MaxRetryAfter}
		})
		var waits []time.Duration
		e.sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		out, _ := e.Ask(cctx, newQuestion("m1", "Hi", "", subj("x")).Question)
		if out.Called != 1 || len(waits) != 0 {
			t.Fatalf("%+v, waits %v", out, waits)
		}
	})
	t.Run("Retry-After honoured up to 5 s, full jitter otherwise", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.model.onCall(func(_ context.Context, n int, _ decide.Call) (*decide.Result, error) {
			if n == 1 {
				return nil, &decide.AttemptError{Err: decide.ErrUnavailable, Sent: true, Retry: true, RetryAfter: 9 * time.Second}
			}
			if n == 2 {
				return nil, timeout
			}
			return nil, nil
		})
		var waits []time.Duration
		e.sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
		e.jitter = func(d time.Duration) time.Duration { return d / 2 }
		out := e.ask(newQuestion("m1", "Hi", "", subj("x")))
		if out.Called != 3 || len(out.Answers) != 1 || len(waits) != 2 || waits[0] != decide.MaxRetryAfter || waits[1] != retryBase {
			t.Fatalf("%+v, waits %v", out, waits)
		}
	})
	charged := func(t *testing.T, e *enclave) int64 {
		t.Helper()
		e.guard.mu.Lock()
		defer e.guard.mu.Unlock()
		used := int64(0)
		for _, g := range e.guard.grants {
			used += g.used
		}
		return used
	}
	est := decide.Estimate(decide.Call{Content: decide.Content{Kind: decide.ContentSubject, Subject: "Hi"}, Statements: atomsN(1)})
	t.Run("an attempt that may have been sent and timed out is charged its estimate; its retry is reserved again", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.model.onCall(func(_ context.Context, n int, _ decide.Call) (*decide.Result, error) {
			if n == 1 {
				return nil, timeout
			}
			return nil, nil
		})
		out := e.ask(newQuestion("m1", "Hi", "", subj("x")))
		if out.Called != 2 || len(out.Answers) != 1 || charged(t, e) != est+decide.Cost(decide.Usage{InputTokens: 500}) {
			t.Fatalf("%+v, charged %d", out, charged(t, e))
		}
	})
	t.Run("an attempt that may have been sent and failed is charged its estimate, and gives no answer", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) {
			return nil, &decide.AttemptError{Err: decide.ErrUnavailable, Sent: true}
		})
		out := e.ask(newQuestion("m1", "Hi", "", subj("x")))
		if len(out.Answers) != 0 || charged(t, e) != est {
			t.Fatalf("%+v, charged %d", out, charged(t, e))
		}
	})
	t.Run("a month with room for one attempt's grant only", func(t *testing.T) {
		w := newWorld(t)
		w.seedSpend(month, "2026-11-01", 499)
		e := w.keyed()
		e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) { return nil, timeout })
		big := newQuestion("m1", "Hi", strings.Repeat(`"`, 175_000), about("x")) // 0.07 USD an attempt
		out := e.ask(big)
		if out.Called != 1 || len(out.Answers) != 0 {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("cancelled before the request was written is not charged; after, its estimate", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		e.model.onCall(func(_ context.Context, n int, _ decide.Call) (*decide.Result, error) {
			return nil, &decide.AttemptError{Err: decide.ErrUnavailable, Sent: n == 2}
		})
		e.ask(newQuestion("m1", "Hi", "", subj("x")))
		if charged(t, e) != 0 {
			t.Fatalf("charged %d for an attempt never sent", charged(t, e))
		}
		e.ask(newQuestion("m2", "Hi", "", subj("x")))
		if charged(t, e) != est {
			t.Fatalf("charged %d", charged(t, e))
		}
	})
	t.Run("two services near the limit, every attempt failing late, never pass Month", func(t *testing.T) {
		w := newWorld(t)
		w.seedSpend(month, "2026-11-01", 495)
		a, b := w.keyed(), w.keyed()
		for _, e := range []*enclave{a, b} {
			e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) { return nil, timeout })
		}
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
		if charged(t, a)+charged(t, b) > 5*block {
			t.Fatalf("the two services charged %d together, past the month's last 5 blocks", charged(t, a)+charged(t, b))
		}
	})
	t.Run("every reservation settled exactly once", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		r, _ := e.reserve(ctx, 1000, "acc", keyFP)
		r2, _ := e.reserve(ctx, 1000, "acc", keyFP)
		r.settle(10)
		r.settle(0)
		r.settle(1000)
		if got := charged(t, e); got != 10+1000 {
			t.Fatalf("charged %d", got)
		}
		r2.settle(1000)
		if got := charged(t, e); got != 10+1000 {
			t.Fatalf("another's settlement released: %d", got)
		}
	})
	t.Run("at most Concurrent attempts open, waiting counted in the deadline", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		var mu sync.Mutex
		open, most := 0, 0
		release := make(chan struct{})
		e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) {
			mu.Lock()
			open++
			most = max(most, open)
			mu.Unlock()
			<-release
			mu.Lock()
			open--
			mu.Unlock()
			return nil, nil
		})
		var wg sync.WaitGroup
		for i := range decide.Concurrent + 4 {
			wg.Go(func() { e.ask(newQuestion("m"+itoa(i), "Hi", "", subj("x"))) })
		}
		for {
			mu.Lock()
			n := open
			mu.Unlock()
			if n == decide.Concurrent {
				break
			}
			time.Sleep(time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
		close(release)
		wg.Wait()
		if most != decide.Concurrent {
			t.Fatalf("%d open at once", most)
		}
		// A slot that does not come in time is the deadline's.
		for range decide.Concurrent {
			e.slots <- struct{}{}
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
		defer cancel()
		out, _ := e.Ask(cctx, newQuestion("late", "Hi", "", subj("x")).Question)
		if out.Called != 0 || len(out.Answers) != 0 {
			t.Fatalf("%+v", out)
		}
	})
}

// maxsize are the re-measure's four largest requests
// (internal/decide/testdata/maxsize.jsonl.gz): a 2 KiB subject and a text
// of exactly 256 KiB, as questions.
func maxsize(t *testing.T) map[string]*question {
	t.Helper()
	var topics map[string]string
	raw, err := os.ReadFile("../../decide/testdata/topics.json")
	if err != nil || json.Unmarshal(raw, &topics) != nil {
		t.Fatal("topics.json", err)
	}
	f, err := os.Open("../../decide/testdata/maxsize.jsonl.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*question{}
	dec := json.NewDecoder(z)
	for dec.More() {
		var c struct {
			ID, Subject, Body string
			Atoms             [][]any
		}
		if err := dec.Decode(&c); err != nil {
			t.Fatal(err)
		}
		var atoms []query.Atom
		for _, a := range c.Atoms {
			if a[0] != "message_mentions" {
				t.Fatalf("%s: %v", c.ID, a)
			}
			atoms = append(atoms, mentions(topics[a[1].(string)]))
		}
		out[c.ID] = newQuestion(c.ID, c.Subject, c.Body, atoms...)
	}
	if len(out) != 4 {
		t.Fatalf("%d requests", len(out))
	}
	return out
}

func callOf(q *question, body string) decide.Call {
	return decide.Call{Content: decide.Content{Kind: decide.ContentMessage, Subject: q.Subject, Body: body}, Statements: q.Atoms}
}

// A42. The largest requests through the guard.
func TestA42TheLargestRequestsThroughTheGuard(t *testing.T) {
	bodies := map[string]string{}
	for id, q := range maxsize(t) {
		text, _, _ := q.Text(context.Background())
		bodies[id] = text
	}
	fresh := func(id string) *question {
		q := maxsize(t)[id]
		return q
	}
	if got := decide.Estimate(callOf(fresh("max04"), bodies["max04"])); got != 106_029_200 {
		t.Fatalf("max04's estimate %d, ADR 0028 says 0.1060292 USD", got)
	}
	t.Run("each sent once, max04 from one grant of two blocks, the others of one", func(t *testing.T) {
		for _, id := range []string{"max01", "max02", "max03", "max04"} {
			// A world of its own for each: a fresh service, month and
			// account, so each grant's size is its request's alone.
			w := newWorld(t)
			e := w.keyed()
			out := e.ask(fresh(id))
			if out.Called != 1 || e.model.count() != 1 || len(out.Answers) != len(fresh(id).Atoms) {
				t.Fatalf("%s: %+v", id, out)
			}
			d := w.spend(month)
			want := int64(1)
			if id == "max04" {
				want = 2
			}
			if len(d.Grants) != 1 || d.Grants[0].Blocks != want || d.Grants[0].Day != "2026-11-16" {
				t.Fatalf("%s: %+v", id, d.Grants)
			}
		}
	})
	t.Run("24 statements on 1,585,176 bytes from one grant of four blocks", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		ctl := strings.Repeat("\x01", 256<<10)
		list := atomsN(24)
		for i := range list {
			list[i] = about(list[i].Topic)
		}
		q := newQuestion("largest", strings.Repeat("\x01", 2048), ctl, list...)
		c := callOf(q, ctl)
		if n := len(decide.ContentJSON(c.Content)); n != 1_585_176 || decide.Estimate(c) != 319_155_200 {
			t.Fatalf("%d bytes, estimate %d", n, decide.Estimate(c))
		}
		if out := e.ask(q); out.Called != 1 {
			t.Fatalf("%+v", out)
		}
		if d := w.spend(month); len(d.Grants) != 1 || d.Grants[0].Blocks != 4 {
			t.Fatalf("%+v", d.Grants)
		}
	})
	t.Run("with 0.10 USD left in the month max04 is not sent", func(t *testing.T) {
		w := newWorld(t)
		w.seedSpend(month, "2026-11-01", 499)
		e := w.keyed()
		if out := e.ask(fresh("max04")); out.Outcome != inbox.AskLimit || out.Called != 0 {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("an account with 0.40 USD of its share spent", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		q := fresh("max04")
		if h := e.reserveOnce(400_000_000, q.Account); h != holdNone {
			t.Fatal(h)
		}
		if out := e.ask(q); out.Outcome != inbox.AskLimit || out.Called != 0 {
			t.Fatalf("%+v", out)
		}
		other := fresh("max04")
		other.Account = "acc_two"
		if out := e.ask(other); out.Called != 1 {
			t.Fatalf("another account: %+v", out)
		}
		// Every grant's blocks and day in the record; each attempt from one.
		d := w.spend(month)
		if len(d.Grants) != 2 || d.Grants[0].Blocks != 4 || d.Grants[1].Blocks != 2 {
			t.Fatalf("%+v", d.Grants)
		}
	})
}

func keySet() contracts.ModelKeySet           { return keySetOf(testKey) }
func keySetOf(k string) contracts.ModelKeySet { return contracts.ModelKeySet{Key: k} }

// atomsN is n subject atoms of topics t0, t1, ...
func atomsN(n int) []query.Atom {
	out := make([]query.Atom, n)
	for i := range out {
		out[i] = subj("t" + itoa(i))
	}
	return out
}

// A17. An answer to an attempt sent before a stop never clears it: only the
// probe admitted under that stop, or a new key, does; and a refusal of a
// key already replaced does not stand for the new one (the review of pull
// request 2, finding 2).
func TestA17AnOlderAnswerNeverClearsANewerStop(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		err   error
		guard string
	}{
		{decide.ErrKeyRefused, attestation.ModelGuardKeyRefused},
		{decide.ErrProviderLimit, attestation.ModelGuardProviderLimit},
	} {
		t.Run(tc.err.Error(), func(t *testing.T) {
			w := newWorld(t)
			e := w.keyed()
			entered, release := make(chan struct{}), make(chan struct{})
			e.model.onCall(func(_ context.Context, n int, _ decide.Call) (*decide.Result, error) {
				switch n {
				case 1: // sent before the stop, answered after it
					close(entered)
					<-release
				case 2:
					return nil, &decide.AttemptError{Err: tc.err, Sent: true}
				}
				return nil, nil
			})
			done := make(chan *inbox.Asked, 1)
			go func() { done <- e.ask(newQuestion("older", "Hi", "", subj("x"))) }()
			<-entered
			if stop := e.ask(newQuestion("stop", "Hi", "", subj("x"))); stop.Called != 1 {
				t.Fatalf("the stop was not reached: %+v", stop)
			}
			close(release)
			if older := <-done; len(older.Answers) != 1 {
				t.Fatalf("the older answer is still its own: %+v", older)
			}
			if next := e.ask(newQuestion("next", "Hi", "", subj("x"))); next.Called != 0 {
				t.Fatalf("the older answer cleared the stop: %+v", next)
			}
			if got := e.Status(ctx).Guard; got != tc.guard {
				t.Fatalf("guard %s", got)
			}
			w.advance(probeEvery)
			if probe := e.ask(newQuestion("probe", "Hi", "", subj("x"))); probe.Called != 1 || e.Status(ctx).Guard != attestation.ModelGuardOK {
				t.Fatalf("the probe that succeeds: %+v", probe)
			}
		})
	}
	t.Run("a refusal of a key already replaced does not stand for the new one", func(t *testing.T) {
		w := newWorld(t)
		e := w.keyed()
		entered, release := make(chan struct{}), make(chan struct{})
		e.model.onCall(func(_ context.Context, n int, _ decide.Call) (*decide.Result, error) {
			if n == 1 {
				close(entered)
				<-release
			}
			return nil, &decide.AttemptError{Err: decide.ErrKeyRefused, Sent: true}
		})
		done := make(chan *inbox.Asked, 1)
		go func() { done <- e.ask(newQuestion("old key", "Hi", "", subj("x"))) }()
		<-entered
		if _, err := e.SetKey(ctx, keySetOf("sk-the-new-key")); err != nil {
			t.Fatal(err)
		}
		if out := e.ask(newQuestion("new key", "Hi", "", subj("x"))); out.Called != 1 || out.Outcome != inbox.AskOff {
			t.Fatalf("the new key's refusal: %+v", out)
		}
		close(release) // the old key's refusal arrives after the new key's
		<-done
		if out := e.ask(newQuestion("next", "Hi", "", subj("x"))); out.Called != 0 || out.Outcome != inbox.AskOff {
			t.Fatalf("the new key, refused, sent again before its probe: %+v", out)
		}
	})
}

// A14. The boundary of the once-a-minute read (ADR 0028, "Choices made
// while building pull request 2"): a fork made after this enclave's last
// read of the month lets it go on spending from the credit it holds,
// already confirmed, until spendCheck has passed; then it stops and burns
// that credit. The stop is not instant; a new grant always reads the
// record first, so none is made over the fork (the forked last block,
// above).
func TestA14AForkIsFoundWithinSpendCheck(t *testing.T) {
	w := newWorld(t)
	e := w.keyed()
	if h := e.reserveOnce(1, ""); h != holdNone {
		t.Fatal(h)
	}
	w.commits.Fork(RecordSpend+month, 1, sha256.Sum256([]byte("another append at the same version")))
	w.advance(spendCheck - time.Second)
	if h := e.reserveOnce(1, ""); h != holdNone {
		t.Fatalf("held credit stopped before spendCheck: %v", h)
	}
	w.advance(time.Second)
	if h := e.reserveOnce(1, ""); h != holdLimit {
		t.Fatalf("the fork not found once spendCheck passed: %v", h)
	}
	e.guard.mu.Lock()
	defer e.guard.mu.Unlock()
	if len(e.guard.grants) != 0 {
		t.Fatal("the held credit was not burnt")
	}
}
