package decisions

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/inboxtest"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/services/mailboxes/mailboxestest"
	"github.com/permaily-org/permaily/internal/store"
)

// clearedWorld is a keyed enclave whose model cleared one benign message
// under the default rule, through the real Discloser and the sealed
// decisions of services/mailboxes: once cleared, reading it again asks
// nothing, so only the gate stands between it and a connection.
type clearedWorld struct {
	w    *world
	e    *enclave
	disc inbox.Discloser
	d    inbox.Disclosure
	id   string
	ctx  context.Context
}

func newClearedWorld(t *testing.T) *clearedWorld {
	t.Helper()
	w := newWorld(t)
	w.records.Now = w.clock
	e := w.keyed()
	for _, a := range []query.Atom{about("a sign-in, login or verification code, or a one-time password"), about("resetting a password or recovering an account")} {
		e.model.set(a, 0.05)
	}
	localKeys, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	mb, err := mailboxes.New(mailboxes.Config{Records: e.anchored, Storage: w.records, Keys: localKeys, Accounts: mailboxestest.NewAccounts(), Domain: "inbox.permaily.test", Now: w.clock, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	id := inbox.NewMessageID(w.clock().Add(-time.Hour))
	raw := []byte("From: sender@example.test\r\nSubject: Lunch\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nSee you")
	if err := inboxtest.Seed(context.Background(), w.records, w.records, testBox, inboxtest.FromRaw(context.Background(), id, w.clock().Add(-time.Hour), 30, raw)); err != nil {
		t.Fatal(err)
	}
	all, err := query.Canonicalise(contracts.Formula{V: contracts.FormulaVersion, All: []contracts.Group{{Any: []contracts.Condition{{Var: contracts.VarReceived, Op: contracts.OpSince, Value: "2020-01-01T00:00:00Z"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	c := &clearedWorld{w: w, e: e, disc: inbox.NewDiscloser(w.records, query.Eval, mb.Decisions()), id: id}
	c.d = inbox.Disclosure{Formulas: []*query.Canonical{all}, Rules: []rules.Rule{rules.Default()}, Parts: []contracts.Part{contracts.PartSender, contracts.PartBody}, Account: "acc_a", Model: true}
	c.ctx = inbox.WithAsker(context.Background(), inbox.Asking{Asker: e.Service, Account: c.d.Account})
	if first, err := c.disc.Message(c.ctx, testBox, c.d, id); err != nil || first.Disclosed == nil {
		t.Fatalf("first disclosure %+v %v", first, err)
	}
	return c
}

// released says whether the cleared message would leave now: the gate
// answers AskAnswered (the connections service answers model_unavailable
// otherwise) and the Discloser gives it.
func (c *clearedWorld) released(t *testing.T, e *enclave) bool {
	t.Helper()
	if e.Gate(c.ctx, c.d.Account) != inbox.AskAnswered {
		return false
	}
	o, err := c.disc.Message(c.ctx, testBox, c.d, c.id)
	if err != nil {
		t.Fatal(err)
	}
	return o.Disclosed != nil
}

// H3 with the model off through the gate: a message already cleared needs
// no ask, so the gate alone keeps it from leaving while the month's spend
// record is lost or cannot be read, in this enclave and in a fresh one,
// until a successful read of it; the gate itself reads it again, at most
// once every spendCheck, so a read that failed recovers without a restart.
func TestH3TheGateHoldsClearedMailWhileTheSpendRecordIsUnreadable(t *testing.T) {
	t.Run("lost", func(t *testing.T) {
		c := newClearedWorld(t)
		for _, n := range c.w.records.RecordNames() {
			if strings.HasPrefix(n, RecordSpend+month+"@") {
				if err := c.w.records.DeleteRecord(c.ctx, n); err != nil {
					t.Fatal(err)
				}
			}
		}
		c.w.advance(spendCheck)
		if stopped := c.e.ask(newQuestion("fresh-question", "New subject", "", subj("another topic"))); stopped.Outcome != inbox.AskLimit {
			t.Fatalf("expected the spend's refusal, got %+v", stopped)
		}
		calls := c.e.model.count()
		if c.released(t, c.e) {
			t.Fatalf("released while the spend record is lost; model calls %d", c.e.model.count()-calls)
		}
		if c.released(t, c.w.enclave()) {
			t.Fatal("a fresh enclave released while the spend record is lost")
		}
	})
	t.Run("unreadable, then read again", func(t *testing.T) {
		c := newClearedWorld(t)
		reads := 0
		c.e.records.onGet(func(name string, _ int) (bool, *store.Record, store.Mark, error) {
			if strings.HasPrefix(name, RecordSpend) {
				reads++
				return true, nil, store.Mark{}, errors.New("storage")
			}
			return false, nil, store.Mark{}, nil
		})
		c.w.advance(spendCheck)
		if c.released(t, c.e) {
			t.Fatal("released while the spend record cannot be read")
		}
		before := reads
		if c.released(t, c.e) || reads != before {
			t.Fatalf("released, or read again within spendCheck (%d reads)", reads-before)
		}
		c.e.records.onGet(nil)
		if c.released(t, c.e) {
			t.Fatal("released before the next read")
		}
		c.w.advance(spendCheck)
		if !c.released(t, c.e) {
			t.Fatal("not released once the record reads again")
		}
	})
}

// H3: a stop of the provider's (the key refused, its limit) is not lifted
// because a probe is due: the gate makes the probe itself, at most one
// every probeEvery, and opens only once the provider answered it; until
// then a cleared message stays.
func TestH3ADueProbeIsNotARecoveredModel(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(c *clearedWorld)
		err  error
	}{
		{"the key refused", func(c *clearedWorld) { c.e.keyRefused(c.ctx, c.e.keyState(c.ctx).fp) }, decide.ErrKeyRefused},
		{"the provider's limit", func(c *clearedWorld) { c.e.providerLimit(c.ctx) }, decide.ErrProviderLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newClearedWorld(t)
			tc.stop(c)
			c.e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) {
				return nil, &decide.AttemptError{Err: tc.err, Sent: true}
			})
			if c.released(t, c.e) {
				t.Fatal("released while stopped")
			}
			c.w.advance(probeEvery)
			calls := c.e.model.count()
			if c.released(t, c.e) {
				t.Fatalf("released on a probe due, the provider still refusing; probes %d", c.e.model.count()-calls)
			}
			if c.e.model.count() != calls+1 {
				t.Fatalf("the gate's probe: %d calls", c.e.model.count()-calls)
			}
			if c.released(t, c.e) || c.e.model.count() != calls+1 {
				t.Fatal("released, or probed again within probeEvery")
			}
			c.e.model.onCall(nil)
			c.w.advance(probeEvery)
			if !c.released(t, c.e) {
				t.Fatal("not released once the provider answered the probe")
			}
		})
	}
}

// H3: the gate's probe uses the key in use when it is sent, which the
// operator may have replaced since the gate read it. When the provider
// refuses that replacement, the refusal holds: the gate never lifts it on
// the key it read before, and a cleared message stays (the second review
// of pull request 134, its first finding). The clock orders the
// replacement after the gate read key A and before its probe reads the
// key; it returns the same time, so nothing relies on sleeps.
func TestH3TheGateKeepsTheRefusalOfTheKeyItsProbeUsed(t *testing.T) {
	c := newClearedWorld(t)
	e := c.e
	e.keyRefused(c.ctx, e.keyState(c.ctx).fp)
	c.w.advance(probeEvery)
	e.keyState(c.ctx) // A in the cache before the schedule is imposed
	const replacement = "sk-proj-test-rejectedreplacement"
	var ticks atomic.Int32
	e.cfg.Now = func() time.Time {
		if ticks.Add(1) == 2 { // stopped, after the gate's keyState
			if _, err := e.SetKey(context.Background(), keySetOf(replacement)); err != nil {
				t.Fatal(err)
			}
		}
		return c.w.clock()
	}
	e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) {
		return nil, &decide.AttemptError{Err: decide.ErrKeyRefused, Sent: true}
	})
	before := e.model.count()
	got := e.Gate(c.ctx, c.d.Account)
	e.cfg.Now = c.w.clock
	if e.model.count() != before+1 || e.keyState(c.ctx).fp != fingerprint(replacement) {
		t.Fatal("the replacement and the probe did not run as scheduled")
	}
	e.model.mu.Lock()
	sent := e.model.keys[len(e.model.keys)-1]
	e.model.mu.Unlock()
	if sent != replacement {
		t.Fatal("the probe did not use the replacement")
	}
	out, err := c.disc.Message(c.ctx, testBox, c.d, c.id)
	if err != nil {
		t.Fatal(err)
	}
	if got == inbox.AskAnswered && out.Disclosed != nil {
		t.Fatalf("released cleared mail after the provider refused the key the gate's probe used; refusedStop=%v", e.guard.refusedStop)
	}
}

// probePause pauses a gate at its probe's deadline, once: after it found a
// probe due and before the probe is reserved. The warm key cache and the
// guard's check in memory do not ask for the deadline.
type probePause struct {
	context.Context
	once            atomic.Bool
	reached, resume chan struct{}
}

func (c *probePause) Deadline() (time.Time, bool) {
	if c.once.CompareAndSwap(false, true) {
		close(c.reached)
		<-c.resume
	}
	return c.Context.Deadline()
}

// H3: gates that find the same probe due send one probe between them. One
// pauses after it found the probe due; another sends the probe, which the
// provider answers; the first, resumed, sends nothing and opens on the
// recovery it finds (the second review of pull request 134, its second
// finding).
func TestH3GatesThatFindAProbeDueSendOneProbe(t *testing.T) {
	w := newWorld(t)
	e := w.keyed()
	ctx := context.Background()
	e.keyRefused(ctx, e.keyState(ctx).fp)
	w.advance(probeEvery)
	e.keyState(ctx) // nothing read from storage before the probe's deadline
	slow := &probePause{Context: ctx, reached: make(chan struct{}), resume: make(chan struct{})}
	done := make(chan inbox.AskOutcome, 1)
	go func() { done <- e.Gate(slow, "acc_slow") }()
	<-slow.reached
	first := e.Gate(ctx, "acc_fast")
	close(slow.resume)
	second := <-done
	if first != inbox.AskAnswered || second != inbox.AskAnswered {
		t.Fatalf("gates: %v %v", first, second)
	}
	if calls := e.model.count(); calls != 1 {
		t.Fatalf("gates that found one probe due sent %d probes", calls)
	}
}
