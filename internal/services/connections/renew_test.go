package connections

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// The second review of pull request 136, finding 2 (ruling 2 of its third
// rulings): a renewal's authorizing writes are bounded by the earlier of
// its proof's end and the connection's existing end, and checked again
// after its consent receipt and after its connection write.

// V25: the reviewer's reproduction: a proof with one second left that
// passes while the consent receipt is written renews nothing.
func TestV25RenewMustStillHaveFreshProofAfterReceipt(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	w.connectAll(s, w.person(accountA), x)
	d := w.connectionOf(s, accountA, claudeCode)
	w.clock.add(24 * time.Hour)
	p := w.person(accountA)
	p.ConfirmedUntil = w.clock.now().Add(time.Second)
	shown, err := s.Get(ctx, p, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	w.rcpt.onWrite = func(r *receipts.Receipt) {
		if r.Kind == receipts.KindConsent && r.Consent.Renewal {
			w.clock.add(2 * time.Second)
		}
	}
	_, err = s.Renew(ctx, p, d.ID, contracts.ConnectionRenewal{Version: shown.Version, ModelNotice: notice})
	got := w.connectionOf(w.enclave(), accountA, claudeCode)
	if !errors.Is(err, contracts.ErrConfirmationNeeded) || !got.EndsAt.Equal(d.EndsAt) {
		t.Fatalf("expired proof: Renew error=%v; durable end before=%v after=%v", err, d.EndsAt, got.EndsAt)
	}
}

// V25: at the consent receipt and at the connection's write, the proof
// passing or the connection's existing end passing renews nothing: the
// durable end and notice, and what it delivers, are as before (and its
// version too when nothing was written); a renewal in time renews.
func TestV25RenewalEndsWithItsProofOrTheConnection(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		at      string // "receipt" or "write"
		ending  bool   // the connection's end passes, not the proof
		want    error
		written bool // the connection's record was written (and set back)
	}{
		{name: "the proof at the receipt", at: "receipt", want: contracts.ErrConfirmationNeeded},
		{name: "the connection's end at the receipt", at: "receipt", ending: true, want: contracts.ErrConnectionEnded},
		{name: "the proof at the connection's write", at: "write", want: contracts.ErrConfirmationNeeded, written: true},
		{name: "the connection's end at the connection's write", at: "write", ending: true, want: contracts.ErrConnectionEnded, written: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.notice = "2026-10-01" // approved under the notice before
			x := w.ask(claudeCode)
			tok := w.connectAll(w.enclave(), w.person(accountA), x)
			w.notice = notice
			s, f := w.hooked()
			p := w.person(accountA)
			d := w.connectionOf(s, accountA, claudeCode)
			if tc.ending {
				w.clock.add(d.EndsAt.Sub(w.clock.now()) - time.Second)
				p.ConfirmedUntil = w.clock.now().Add(time.Hour)
			} else {
				w.clock.add(10 * time.Minute)
				p.ConfirmedUntil = w.clock.now().Add(time.Second)
			}
			before, err := s.Get(ctx, p, d.ID)
			if err != nil {
				t.Fatal(err)
			}
			jumped := false
			jump := func() {
				if !jumped {
					jumped = true
					w.clock.add(2 * time.Second)
				}
			}
			if tc.at == "receipt" {
				w.rcpt.mu.Lock()
				w.rcpt.onWrite = func(r *receipts.Receipt) {
					if r.Kind == receipts.KindConsent && r.Consent.Renewal {
						jump()
					}
				}
				w.rcpt.mu.Unlock()
			} else {
				f.set(func(op, name string) error {
					if op == "put" && strings.HasPrefix(name, RecordConnection) {
						jump()
					}
					return nil
				})
			}
			_, err = s.Renew(ctx, p, d.ID, contracts.ConnectionRenewal{Version: before.Version, ModelNotice: notice})
			f.set(nil)
			w.rcpt.mu.Lock()
			w.rcpt.onWrite = nil
			w.rcpt.mu.Unlock()
			if !jumped {
				t.Fatal("the barrier was not reached")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Renew: %v", err)
			}
			got := w.connectionOf(w.enclave(), accountA, claudeCode)
			if !got.EndsAt.Equal(d.EndsAt) || got.ModelNotice != d.ModelNotice {
				t.Fatalf("durable end %v (was %v), notice %q (was %q)", got.EndsAt, d.EndsAt, got.ModelNotice, d.ModelNotice)
			}
			if !tc.written {
				after, err := w.enclave().Get(ctx, w.person(accountA), d.ID)
				if err != nil || after.Version != before.Version {
					t.Fatalf("version %v, %v", after, err)
				}
			}
			if !tc.ending {
				after, err := w.enclave().Get(ctx, w.person(accountA), d.ID)
				if err != nil || after.Delivery != contracts.DeliveryNoticeChanged {
					t.Fatalf("delivery %v, %v", after, err)
				}
				if _, _, err := w.enclave().Search(ctx, w.grant(w.enclave(), x, tok.AccessToken), contracts.VendorSearch{}); !errors.Is(err, contracts.ErrModelUnavailable) {
					t.Fatalf("a renewal refused delivers: %v", err)
				}
				// A renewal with its proof fresh throughout renews.
				p := w.person(accountA)
				cur, err := s.Get(ctx, p, d.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.Renew(ctx, p, d.ID, contracts.ConnectionRenewal{Version: cur.Version, ModelNotice: notice}); err != nil {
					t.Fatal(err)
				}
				if got := w.connectionOf(w.enclave(), accountA, claudeCode); !got.EndsAt.After(d.EndsAt) || got.ModelNotice != notice {
					t.Fatal("a renewal in time did not renew")
				}
			}
		})
	}
}
