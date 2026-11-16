package connections

import (
	"context"
	"errors"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The second review of pull request 136, finding 1 (ruling 1 of its third
// rulings): an approval is bound to the list's closing generation it read
// before it checked the account's standing, and its reservation and
// activation commit only in that generation, so an approval in flight
// while a deletion is asked and cancelled never lands in the reopened
// generation.

// V30: a deletion asked and cancelled while an approval fetches the
// vendor's document (after the standing was read open, before anything of
// the approval is in the list): the approval commits nothing usable, with
// no list, with a list, and through enclaves started after it began.
func TestV30FirstApprovalCannotCrossDeletionAndCancellation(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		existing string // a client already connected, so the account has a list
		device   bool   // the approval adds a device to that client's connection
		restart  bool   // the deciding and the cancelling enclaves are new ones
	}{
		{name: "no list"},
		{name: "an existing list", existing: chatgpt},
		{name: "an existing list, adding a device", existing: claudeCode, device: true},
		{name: "restarted enclaves", existing: chatgpt, restart: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			a, b := w.enclave(), w.enclave()
			p := w.person(accountA)
			if tc.existing != "" {
				w.connectAll(a, p, w.ask(tc.existing))
			}
			x := w.ask(claudeCode)
			req := w.start(a, p, x)
			if tc.device != (req.Snapshot.Change == contracts.ChangeAddDevice) {
				t.Fatal(req.Snapshot.Change)
			}
			if tc.restart {
				a, b = w.enclave(), w.enclave()
			}
			fenced := false
			w.vendors.onClient = func() {
				if fenced {
					return
				}
				fenced = true
				w.accts.set(accountA, StandingClosing)
				if err := b.EndAll(ctx, accountA); err != nil {
					t.Error(err)
				}
				if err := b.Reopen(ctx, accountA); err != nil {
					t.Error(err)
				}
				w.accts.set(accountA, StandingOpen)
			}
			ans, err := a.Decide(ctx, p, req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &req.Draft, Snapshot: &req.Snapshot, ModelNotice: notice})
			w.vendors.onClient = nil
			if !fenced {
				t.Fatal("barrier never reached")
			}
			if err == nil {
				tok, xerr := w.exchange(w.enclave(), x, codeOf(t, ans.Redirect))
				if xerr == nil {
					if _, cerr := w.check(w.enclave(), x, tok.AccessToken); cerr == nil {
						t.Fatal("an approval begun before the deletion committed in the reopened generation and issued a usable token")
					}
				}
				t.Fatal("an approval begun before the deletion was answered allowed")
			}
			if !errors.Is(err, contracts.ErrAccountClosing) && !errors.Is(err, contracts.ErrConsentChanged) {
				t.Fatal(err)
			}
			// Nothing of the approval is active in the list, and the request
			// may be approved again now, in the reopened generation.
			l, _, err := w.enclave().readList(ctx, accountA)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range l.Members {
				if m.State != contracts.ConnectionEnded && m.Generation < l.Generation {
					t.Fatalf("a member of the closed generation is active: %+v", m)
				}
				if m.Pending {
					t.Fatalf("a pending member is left: %+v", m)
				}
			}
			y := w.ask(claudeCode)
			again := w.start(w.enclave(), p, y)
			tok, err := w.exchange(w.enclave(), y, w.allow(w.enclave(), p, again))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.check(w.enclave(), y, tok.AccessToken); err != nil {
				t.Fatalf("an approval after the cancellation does not work: %v", err)
			}
		})
	}
}
