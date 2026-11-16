package decisions

import (
	"context"
	"errors"
	"log/slog"
	"strings"
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
