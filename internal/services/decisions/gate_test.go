package decisions

import (
	"context"
	"testing"

	"github.com/permaily-org/permaily/internal/inbox"
)

// The model's gate (connections.ModelGate, the owner's decision 9): off
// without a model or a key and while the key is refused, at a limit while
// the provider's limit stops attempts, a grant of the month did not fit or
// the account's share of the day is spent; a probe due is sent by the gate,
// which opens on its answer; another account's share is its own.
func TestTheGateSaysWhetherTheModelMayBeAsked(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	if got := w.enclave(withoutModel()).Gate(ctx, "acc_a"); got != inbox.AskOff {
		t.Fatalf("no model: %v", got)
	}
	if got := w.enclave().Gate(ctx, "acc_a"); got != inbox.AskOff {
		t.Fatalf("no key: %v", got)
	}
	e := w.keyed()
	if got := e.Gate(ctx, "acc_a"); got != inbox.AskAnswered {
		t.Fatalf("a key: %v", got)
	}
	e.keyRefused(ctx, e.keyState(ctx).fp)
	if got := e.Gate(ctx, "acc_a"); got != inbox.AskOff {
		t.Fatalf("a refused key: %v", got)
	}
	w.advance(probeEvery)
	if got := e.Gate(ctx, "acc_a"); got != inbox.AskAnswered {
		t.Fatalf("a probe due, answered: %v", got)
	}
	e.answered(&reservation{probe: true, gen: e.guard.gen})
	e.providerLimit(ctx)
	if got := e.Gate(ctx, "acc_a"); got != inbox.AskLimit {
		t.Fatalf("the provider's limit: %v", got)
	}
	e.answered(&reservation{probe: true, gen: e.guard.gen})
	if got := e.Gate(ctx, "acc_a"); got != inbox.AskAnswered {
		t.Fatalf("the provider back: %v", got)
	}
	e.guard.mu.Lock()
	e.guard.shares[dayOf(w.clock())] = map[string]int64{"acc_a": e.cfg.Limits.AccountDay}
	e.guard.mu.Unlock()
	if got := e.Gate(ctx, "acc_a"); got != inbox.AskLimit {
		t.Fatalf("the account's share: %v", got)
	}
	if got := e.Gate(ctx, "acc_b"); got != inbox.AskAnswered {
		t.Fatalf("another account: %v", got)
	}
	e.guard.mu.Lock()
	e.guard.limitStop = monthOf(w.clock())
	e.guard.mu.Unlock()
	if got := e.Gate(ctx, "acc_b"); got != inbox.AskLimit {
		t.Fatalf("the month's limit: %v", got)
	}
	e.guard.mu.Lock()
	e.guard.limitStop, e.guard.forkedMonth = "", monthOf(w.clock())
	e.guard.mu.Unlock()
	if got := e.Gate(ctx, "acc_b"); got != inbox.AskLimit {
		t.Fatalf("a forked month: %v", got)
	}
}
