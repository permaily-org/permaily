package connections

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// A renewal (PR #136's third review, findings 1 and 2; ruling 1 of its
// fourth rulings): decided at its last check with RenewWriteMax to spare on
// the proof and on the connection's existing end, then one version-checked
// write of the record under RenewWriteMax, and nothing checked or undone
// after it. Every schedule below reads the record through cold enclaves
// at each barrier: only the state before the renewal, or the one its
// valid decision approved, is ever observable.

// renewalWorld is a connection approved under the notice before, ten
// minutes old, and an enclave whose record writes a test can stop.
type renewalWorld struct {
	*world
	x       *ask
	tok     *TokenAnswer
	s       *Service
	f       *failing
	old     renewState
	id      string
	days    int
	version uint64
}

// renewState is what a cold enclave reads of the connection.
type renewState struct {
	end     time.Time
	notice  string
	state   contracts.ConnectionState
	version uint64
}

func newRenewal(t *testing.T) *renewalWorld {
	t.Helper()
	w := newWorld(t)
	w.notice = "2026-10-01" // approved under the notice before
	x := w.ask(claudeCode)
	tok := w.connectAll(w.enclave(), w.person(accountA), x)
	w.notice = notice
	w.clock.add(10 * time.Minute)
	s, f := w.hooked()
	d := w.connectionOf(s, accountA, claudeCode)
	r := &renewalWorld{world: w, x: x, tok: tok, s: s, f: f, id: d.ID, days: d.Days}
	r.old = r.observe()
	r.version = r.old.version
	return r
}

// observe is the connection's record as a new enclave reads it, strictly.
func (r *renewalWorld) observe() renewState {
	r.t.Helper()
	d, at, err := r.enclave().readConn(context.Background(), r.id)
	if err != nil || d == nil {
		r.t.Fatalf("a cold read: %v", err)
	}
	return renewState{end: d.EndsAt, notice: d.ModelNotice, state: d.State, version: at.Version()}
}

// approved is the state a renewal decided at decided makes.
func (r *renewalWorld) approved(decided time.Time) renewState {
	return renewState{end: endsAt(decided, r.days), notice: notice, state: contracts.ConnectionActive, version: r.version + 1}
}

// oldOrApproved fails unless got is the state before the renewal or one of
// those its valid decisions approved.
func (r *renewalWorld) oldOrApproved(where string, got renewState, approved ...renewState) {
	r.t.Helper()
	if got == r.old {
		return
	}
	for _, a := range approved {
		if got == a {
			return
		}
	}
	r.t.Fatalf("%s: a state neither old nor approved: %+v (old %+v, approved %+v)", where, got, r.old, approved)
}

// delivers says a cold enclave starts an answer for the vendor's token.
func (r *renewalWorld) delivers() bool {
	r.t.Helper()
	b := r.enclave()
	g, err := r.check(b, r.x, r.tok.AccessToken)
	if err != nil {
		return false
	}
	_, rel, err := b.Search(context.Background(), g, contracts.VendorSearch{})
	if err != nil {
		return false
	}
	c, err := rel.Start(context.Background())
	ok := err == nil && c.Err() == nil
	rel.Finish(ok)
	return ok
}

func (r *renewalWorld) renew(p Person) (*contracts.Connection, error) {
	return r.s.Renew(context.Background(), p, r.id, contracts.ConnectionRenewal{Version: r.version, ModelNotice: notice})
}

// renewals are the renewal consent receipts written.
func (r *renewalWorld) renewals() int {
	n := 0
	for _, rc := range r.rcpt.of(receipts.KindConsent) {
		if rc.Consent.Renewal {
			n++
		}
	}
	return n
}

// V25: a proof with one second left renews nothing: it is refused at the
// decision, before any receipt or write.
func TestV25RenewMustStillHaveFreshProofAfterReceipt(t *testing.T) {
	r := newRenewal(t)
	p := r.person(accountA)
	p.ConfirmedUntil = r.clock.now().Add(time.Second)
	if _, err := r.renew(p); !errors.Is(err, contracts.ErrConfirmationNeeded) {
		t.Fatal(err)
	}
	if got := r.observe(); got != r.old || r.renewals() != 0 {
		t.Fatalf("an expired proof renewed: %+v, %d receipts", got, r.renewals())
	}
}

// V25: the margin's boundary. With RenewWriteMax left on the proof, or on
// the connection's existing end, the renewal is decided; just below it,
// nothing is written, not even its receipt.
func TestV25RenewalDecidedWithRenewWriteMaxLeft(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ending bool // the margin is the connection's end's, not the proof's
		left   time.Duration
		want   error
	}{
		{"the proof just below", false, RenewWriteMax - time.Nanosecond, contracts.ErrConfirmationNeeded},
		{"the proof at", false, RenewWriteMax, nil},
		{"the connection's end just below", true, RenewWriteMax - time.Nanosecond, contracts.ErrConnectionEnded},
		{"the connection's end at", true, RenewWriteMax, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRenewal(t)
			p := r.person(accountA)
			if tc.ending {
				r.clock.add(r.old.end.Sub(r.clock.now()) - tc.left)
				p = r.person(accountA)
			} else {
				p.ConfirmedUntil = r.clock.now().Add(tc.left)
			}
			decided := r.clock.now()
			_, err := r.renew(p)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Renew: %v, want %v", err, tc.want)
			}
			got := r.observe()
			if tc.want != nil {
				if got != r.old || r.renewals() != 0 {
					t.Fatalf("refused at the decision, yet written: %+v, %d receipts", got, r.renewals())
				}
				return
			}
			if got != r.approved(decided) || r.renewals() != 1 {
				t.Fatalf("decided in time, not renewed: %+v, %d receipts", got, r.renewals())
			}
		})
	}
}

// V25: the reviewer's intermediate reader. A renewal decided with its
// margin whose proof then passes entirely while its write is in flight:
// before the write lands every cold enclave reads the old state, which
// delivers nothing under the old notice; after it, the decided one, which
// delivers. The reviewer's own schedule, with 100ms of proof left, is
// refused at its decision, and no reader ever sees it.
func TestV25RenewalIsOldOrDecidedToEveryReader(t *testing.T) {
	t.Run("decided in time, the proof passing during its write", func(t *testing.T) {
		r := newRenewal(t)
		p := r.person(accountA)
		p.ConfirmedUntil = r.clock.now().Add(RenewWriteMax + 100*time.Millisecond)
		decided := r.clock.now()
		atReceipt, beforeWrite, afterWrite := false, false, false
		r.rcpt.mu.Lock()
		r.rcpt.onWrite = func(rc *receipts.Receipt) {
			if rc.Kind == receipts.KindConsent && rc.Consent.Renewal {
				atReceipt = true
				if got := r.observe(); got != r.old {
					t.Errorf("at the receipt: %+v", got)
				}
			}
		}
		r.rcpt.mu.Unlock()
		r.f.set(func(op, name string) error {
			if op == "put" && name == connName(r.id) && !beforeWrite {
				beforeWrite = true
				r.clock.add(RenewWriteMax + 200*time.Millisecond) // the proof passed
				if got := r.observe(); got != r.old {
					t.Errorf("before the write: %+v", got)
				}
				if r.delivers() {
					t.Error("the old state delivered under the old notice")
				}
			}
			return nil
		})
		r.f.setAfter(func(_ context.Context, name string) error {
			if name == connName(r.id) && !afterWrite {
				afterWrite = true
				r.oldOrApproved("after the write", r.observe(), r.approved(decided))
			}
			return nil
		})
		c, err := r.renew(p)
		r.f.set(nil)
		r.f.setAfter(nil)
		if err != nil || !atReceipt || !beforeWrite || !afterWrite {
			t.Fatalf("Renew %v; barriers %v %v %v", err, atReceipt, beforeWrite, afterWrite)
		}
		if got := r.observe(); got != r.approved(decided) || !c.EndsAt.Equal(got.end) {
			t.Fatalf("decided %+v, answered %v, durable %+v", r.approved(decided), c.EndsAt, got)
		}
		if !r.delivers() {
			t.Fatal("the decided renewal does not deliver")
		}
	})
	t.Run("the reviewer's 100ms of proof", func(t *testing.T) {
		r := newRenewal(t)
		p := r.person(accountA)
		p.ConfirmedUntil = r.clock.now().Add(100 * time.Millisecond)
		wrote := false
		r.f.set(func(op, name string) error {
			if op == "put" && name == connName(r.id) {
				wrote = true
			}
			return nil
		})
		_, err := r.renew(p)
		r.f.set(nil)
		if !errors.Is(err, contracts.ErrConfirmationNeeded) || wrote || r.renewals() != 0 {
			t.Fatalf("Renew %v, written %v, %d receipts", err, wrote, r.renewals())
		}
		if got := r.observe(); got != r.old || r.delivers() {
			t.Fatalf("refused, yet %+v delivers %v", got, r.delivers())
		}
	})
}

// V25: the write fails, or lands with its answer lost (a timeout), or is
// held by the table's writer and lands after the proof and the old end
// passed: the record is old or decided, never a third state, and the end
// is the decision's, never one worked out when the write landed. The write
// runs under RenewWriteMax.
func TestV25RenewalWriteFailedUncertainOrHeld(t *testing.T) {
	for _, mode := range []string{"fails", "answer lost", "held past the proof and the old end"} {
		t.Run(mode, func(t *testing.T) {
			r := newRenewal(t)
			p := r.person(accountA)
			p.ConfirmedUntil = r.clock.now().Add(RenewWriteMax + time.Second)
			decided := r.clock.now()
			put := false
			r.f.set(func(op, name string) error {
				if op != "put" || name != connName(r.id) {
					return nil
				}
				put = true
				switch mode {
				case "fails":
					return errDown
				case "held past the proof and the old end":
					r.clock.add(r.old.end.Sub(r.clock.now()) + time.Second)
					if got := r.observe(); got != r.old {
						t.Errorf("while held: %+v", got)
					}
				}
				return nil
			})
			var bound time.Duration
			r.f.setAfter(func(ctx context.Context, name string) error {
				if name != connName(r.id) {
					return nil
				}
				if dl, ok := ctx.Deadline(); ok {
					bound = time.Until(dl)
				}
				if mode == "answer lost" {
					return context.DeadlineExceeded
				}
				return nil
			})
			_, err := r.renew(p)
			r.f.set(nil)
			r.f.setAfter(nil)
			if !put {
				t.Fatal("the barrier was not reached")
			}
			got := r.observe()
			switch mode {
			case "fails":
				if err == nil || got != r.old {
					t.Fatalf("a failed write: %v, %+v", err, got)
				}
			case "answer lost":
				if err == nil || got != r.approved(decided) {
					t.Fatalf("a write whose answer was lost: %v, %+v", err, got)
				}
			default:
				if err != nil || got != r.approved(decided) {
					t.Fatalf("a held write: %v, %+v, decided %+v", err, got, r.approved(decided))
				}
			}
			if mode != "fails" && (bound <= 0 || bound > RenewWriteMax) {
				t.Fatalf("the write ran under %v, not RenewWriteMax", bound)
			}
		})
	}
}

// V25: the reviewer's old end. A one-day connection refreshed just before
// its end: a renewal with 100ms of that end left is refused at its decision
// and, the end passed, no cold enclave refreshes; one decided with its
// margin whose write is held past the old end leaves it ended until the
// write lands, then renewed as decided.
func TestV25RenewalNearTheOldEnd(t *testing.T) {
	for _, left := range []time.Duration{100 * time.Millisecond, RenewWriteMax + 100*time.Millisecond} {
		t.Run(left.String(), func(t *testing.T) {
			ctx := context.Background()
			w := newWorld(t)
			a := w.enclave()
			x := w.ask(claudeCode)
			p := w.person(accountA)
			req := w.pushed(a, p, x, "", map[string][]string{"ends_in_days": {"1"}})
			initial, err := w.exchange(a, x, w.allow(a, p, req))
			if err != nil {
				t.Fatal(err)
			}
			old := w.connectionOf(a, accountA, claudeCode)
			w.clock.add(old.EndsAt.Sub(w.clock.now()) - left)
			current, err := w.refresh(w.enclave(), x, initial.RefreshToken)
			if err != nil {
				t.Fatal(err)
			}
			s, f := w.hooked()
			p = w.person(accountA)
			shown, err := s.Get(ctx, p, old.ID)
			if err != nil {
				t.Fatal(err)
			}
			decided := w.clock.now()
			put, refreshedEnded := false, false
			f.set(func(op, name string) error {
				if op == "put" && name == connName(old.ID) && !put {
					put = true
					w.clock.add(left + time.Second) // the old end passed
					d, _, e := w.enclave().readConn(ctx, old.ID)
					refreshedEnded = e != nil || d.live(w.clock.now()) || !d.EndsAt.Equal(old.EndsAt)
				}
				return nil
			})
			_, err = s.Renew(ctx, p, old.ID, contracts.ConnectionRenewal{Version: shown.Version, ModelNotice: notice})
			f.set(nil)
			got := w.connectionOf(w.enclave(), accountA, claudeCode)
			if left < RenewWriteMax {
				if !errors.Is(err, contracts.ErrConnectionEnded) || put || !got.EndsAt.Equal(old.EndsAt) {
					t.Fatalf("Renew %v, written %v, end %v (was %v)", err, put, got.EndsAt, old.EndsAt)
				}
				w.clock.add(time.Second)
				if _, e := w.refresh(w.enclave(), x, current.RefreshToken); e == nil {
					t.Fatal("a cold enclave refreshed after the old end")
				}
				return
			}
			if err != nil || !put {
				t.Fatalf("Renew %v, written %v", err, put)
			}
			if refreshedEnded {
				t.Fatal("before the held write landed, a cold enclave read the connection other than ended at its old end")
			}
			if !got.EndsAt.Equal(endsAt(decided, old.Days)) || got.State != contracts.ConnectionActive {
				t.Fatalf("end %v, want the decision's %v", got.EndsAt, endsAt(decided, old.Days))
			}
			if _, e := w.refresh(w.enclave(), x, current.RefreshToken); e != nil {
				t.Fatalf("the decided renewal does not refresh: %v", e)
			}
		})
	}
}

// V25: two renewals, in one second or the next, and a revocation, against
// one renewal held at its write, and the reviewer's same-second renewal
// made right after that write landed: the version check decides, so
// whatever committed first stands whole, the other loses with
// consent_changed, and nothing undoes another's renewal or revives a
// revocation.
func TestV25ConcurrentRenewalsAndARevocation(t *testing.T) {
	for _, mode := range []string{"another renewal in the same second", "another renewal the next second", "a revocation", "the other read before, renewed after", "another renewal in the same second, after it landed"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			r := newRenewal(t)
			p := r.person(accountA)
			decided := r.clock.now()
			var other renewState
			var stale uint64
			if mode == "the other read before, renewed after" {
				c, err := r.enclave().Get(ctx, p, r.id)
				if err != nil {
					t.Fatal(err)
				}
				stale = c.Version
			}
			held := false
			r.f.set(func(op, name string) error {
				if op != "put" || name != connName(r.id) || held {
					return nil
				}
				held = true
				b := r.enclave()
				switch mode {
				case "another renewal in the same second, after it landed":
					return nil
				case "another renewal the next second":
					r.clock.add(time.Second)
					fallthrough
				case "another renewal in the same second":
					at := r.clock.now()
					if _, err := b.Renew(ctx, r.person(accountA), r.id, contracts.ConnectionRenewal{Version: r.version, ModelNotice: notice}); err != nil {
						t.Fatal(err)
					}
					other = r.approved(at)
				case "a revocation":
					if _, err := b.Revoke(ctx, r.person(accountA), r.id); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			})
			landed := false
			r.f.setAfter(func(_ context.Context, name string) error {
				if mode != "another renewal in the same second, after it landed" || name != connName(r.id) || landed {
					return nil
				}
				landed = true
				b := r.enclave()
				cur, err := b.Get(ctx, r.person(accountA), r.id)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := b.Renew(ctx, r.person(accountA), r.id, contracts.ConnectionRenewal{Version: cur.Version, ModelNotice: notice}); err != nil {
					t.Fatal(err)
				}
				other = r.approved(r.clock.now())
				other.version++
				return nil
			})
			_, err := r.renew(p)
			r.f.set(nil)
			r.f.setAfter(nil)
			if !held {
				t.Fatal("the barrier was not reached")
			}
			got := r.observe()
			switch mode {
			case "another renewal in the same second, after it landed":
				if err != nil || !landed || got != other {
					t.Fatalf("the first renewal %v; durable %+v, the second's %+v", err, got, other)
				}
			case "the other read before, renewed after":
				if err != nil || got != r.approved(decided) {
					t.Fatalf("the first renewal: %v, %+v", err, got)
				}
				_, err := r.enclave().Renew(ctx, r.person(accountA), r.id, contracts.ConnectionRenewal{Version: stale, ModelNotice: notice})
				if !errors.Is(err, contracts.ErrConsentChanged) || r.observe() != r.approved(decided) {
					t.Fatalf("a renewal at a stale version: %v, %+v", err, r.observe())
				}
			case "a revocation":
				if !errors.Is(err, contracts.ErrConsentChanged) || got.state == contracts.ConnectionActive || got.end != r.old.end {
					t.Fatalf("a renewal over a revocation: %v, %+v", err, got)
				}
				if r.delivers() {
					t.Fatal("a revoked connection delivers")
				}
			default:
				if !errors.Is(err, contracts.ErrConsentChanged) || got != other {
					t.Fatalf("the held renewal %v; durable %+v, the other's %+v", err, got, other)
				}
			}
		})
	}
}
