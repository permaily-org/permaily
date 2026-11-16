package connections

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
)

// The second review of pull request 136, finding 4 (ruling 4 of its third
// rulings): once Check returned a grant, every refusal the service makes of
// that request is recorded, also before its disclosure is prepared, with a
// fixed reason, the grant's attribution and no message identifier; a
// receipt that cannot be written is 503.

// V32: the reviewer's reproduction: a grant whose connection is revoked or
// whose record cannot be read before Read prepares it is refused with one
// receipt; an expired one too.
func TestV32AttributedPrepareRefusalsNeedReceipts(t *testing.T) {
	for _, mode := range []string{"revoked", "unreadable", "expired"} {
		t.Run(mode, func(t *testing.T) {
			w := newWorld(t)
			s := w.enclave()
			x := w.ask(claudeCode)
			tok := w.connectAll(s, w.person(accountA), x)
			g := w.grant(s, x, tok.AccessToken)
			ctx := context.Background()
			switch mode {
			case "revoked":
				if _, err := s.Revoke(ctx, w.person(accountA), g.Connection); err != nil {
					t.Fatal(err)
				}
			case "unreadable":
				h, f := w.hooked()
				failGets(f, RecordConnection, errDown)
				s = h
			case "expired":
				w.clock.add(oauth.AccessTokenTTL)
			}
			before := len(w.rcpt.of(receipts.KindRefused))
			_, rel, err := s.Read(ctx, g, mailboxID(1)+".m001")
			if rel != nil {
				rel.Finish(false)
			}
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if after := len(w.rcpt.of(receipts.KindRefused)); after != before+1 {
				t.Fatalf("Check already attributed request: %s returned %v, refused receipts before=%d after=%d", mode, err, before, after)
			}
		})
	}
}

// V32: every way the authority or the scope of an attributed request is
// refused or cannot be read before its disclosure is prepared, for each
// operation: its answer, exactly one refused receipt of the grant with its
// fixed reason and no message, and 503 when that receipt cannot be written.
func TestV32EveryPrepareRefusalHasOneReceipt(t *testing.T) {
	ctx := context.Background()
	type setup func(w *world, s *Service, g *oauth.Grant) *Service
	cold := func(prefix string, err error) setup {
		return func(w *world, _ *Service, _ *oauth.Grant) *Service {
			h, f := w.hooked()
			failGets(f, prefix, err)
			return h
		}
	}
	for _, tc := range []struct {
		name   string
		narrow bool // the device has a narrower scope of its own
		setup  setup
		want   error
		reason receipts.RefusedReason
	}{
		{name: "revoked here", setup: func(w *world, s *Service, g *oauth.Grant) *Service {
			if _, err := s.Revoke(ctx, w.person(accountA), g.Connection); err != nil {
				w.t.Fatal(err)
			}
			return s
		}, want: ErrInvalidToken, reason: receipts.RefusedConnectionEnded},
		{name: "revoked through another enclave", setup: func(w *world, _ *Service, g *oauth.Grant) *Service {
			if _, err := w.enclave().Revoke(ctx, w.person(accountA), g.Connection); err != nil {
				w.t.Fatal(err)
			}
			return w.enclave()
		}, want: ErrInvalidToken, reason: receipts.RefusedConnectionEnded},
		{name: "the account closing", setup: func(w *world, _ *Service, _ *oauth.Grant) *Service {
			w.accts.set(accountA, StandingClosing)
			return w.enclave()
		}, want: ErrInvalidToken, reason: receipts.RefusedConnectionEnded},
		{name: "its record gone", setup: cold(RecordConnection, store.ErrNotFound), want: ErrInvalidToken, reason: receipts.RefusedConnectionEnded},
		{name: "its record malformed", setup: cold(RecordConnection, envelope.ErrOpen), want: ErrInvalidToken, reason: receipts.RefusedConnectionEnded},
		{name: "its record unreadable", setup: cold(RecordConnection, errDown), want: ErrUnavailable, reason: receipts.RefusedUnavailable},
		{name: "the list unreadable", setup: cold(RecordList, errDown), want: ErrUnavailable, reason: receipts.RefusedUnavailable},
		{name: "the device's scope gone", narrow: true, setup: cold(RecordFamily, store.ErrNotFound), want: ErrInvalidToken, reason: receipts.RefusedConnectionEnded},
		{name: "the device's scope malformed", narrow: true, setup: cold(RecordFamily, envelope.ErrOpen), want: ErrInvalidToken, reason: receipts.RefusedConnectionEnded},
		{name: "the device's scope unreadable", narrow: true, setup: cold(RecordFamily, errDown), want: ErrUnavailable, reason: receipts.RefusedUnavailable},
	} {
		for _, op := range []Operation{OperationSearch, OperationRead, OperationAttachment} {
			for _, failing := range []bool{false, true} {
				name := tc.name + ", " + string(op)
				if failing {
					name += ", its receipt not written"
				}
				t.Run(name, func(t *testing.T) {
					w := newWorld(t)
					s := w.enclave()
					p := w.person(accountA)
					x := w.ask(claudeCode)
					tok := w.connectAll(s, p, x)
					if tc.narrow {
						x = w.ask(claudeCode)
						req := w.pushed(s, p, x, `{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"invoice"}]}]}`, nil)
						if req.Change != contracts.ChangeAddDevice {
							t.Fatal(req.Change)
						}
						var err error
						if tok, err = w.exchange(s, x, w.allow(s, p, req)); err != nil {
							t.Fatal(err)
						}
					}
					g := w.grant(s, x, tok.AccessToken)
					if tc.narrow {
						d := w.connectionOf(s, accountA, claudeCode)
						if dev := d.device(g.Family); dev == nil || dev.ScopeID == "" {
							t.Fatal("the device has no narrower scope")
						}
					}
					w.clock.add(time.Second)
					s = tc.setup(w, s, g)
					if failing {
						w.rcpt.mu.Lock()
						w.rcpt.fail = errCrash
						w.rcpt.mu.Unlock()
					}
					before := w.rcpt.of(receipts.KindRefused)
					var rel Release
					var err error
					switch op {
					case OperationSearch:
						_, rel, err = s.Search(ctx, g, contracts.VendorSearch{})
					case OperationRead:
						_, rel, err = s.Read(ctx, g, mailboxID(1)+".m001")
					case OperationAttachment:
						_, rel, err = s.Attachment(ctx, g, mailboxID(1)+".m001", 0)
					}
					if rel != nil {
						rel.Finish(false)
						t.Fatal("a refused request was given a release")
					}
					after := w.rcpt.of(receipts.KindRefused)
					if failing {
						if !errors.Is(err, ErrUnavailable) || len(after) != len(before) {
							t.Fatalf("a refusal whose receipt failed: %v, %d receipts", err, len(after)-len(before))
						}
						return
					}
					if !errors.Is(err, tc.want) {
						t.Fatalf("answered %v", err)
					}
					if len(after) != len(before)+1 {
						t.Fatalf("%d refused receipts, not one", len(after)-len(before))
					}
					r := after[len(after)-1]
					if r.Refused == nil || r.Refused.Reason != tc.reason || r.Refused.Mailbox != "" || r.Refused.Message != "" || r.Refused.Receipt != "" {
						t.Fatalf("recorded %+v", r.Refused)
					}
					if r.Connection != g.Connection || r.Device != g.Family || r.ClientID != g.ClientID || r.Resource != g.Resource || r.Vendor != g.Vendor || len(r.Delivered) != 0 {
						t.Fatalf("not the grant's: %+v", r)
					}
				})
			}
		}
	}
}
