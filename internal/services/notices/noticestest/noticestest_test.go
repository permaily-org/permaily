package noticestest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/services/notices"
	"github.com/permaily-org/permaily/internal/services/notices/noticestest"
)

// The fake keeps the contract the real queue keeps (ADR 0041, E20).
func TestE20TheFakeKeepsTheContract(t *testing.T) {
	noticestest.Contract(t, func(_ *testing.T, a, _ noticestest.Enclave, clock *noticestest.Clock) (qa, qb noticestest.Subject) {
		f := &noticestest.Fake{Deliverer: a.Deliverer, Now: clock.Now}
		return f, f
	})
}

// A Deliverer as the contract wants one, played: what accounts' Deliver
// keeps in ADR 0041's pull request 2. It shows the contract can be kept,
// and that it refuses one that skips what it could not read.
type played struct {
	standing noticestest.Standing
	sent     int
}

func (p *played) Deliver(_ context.Context, n notices.Notice) error {
	if n.Check() != nil {
		return notices.ErrInvalid
	}
	switch p.standing {
	case noticestest.StandingOpen, noticestest.StandingNotAgreed:
		p.sent++
		return nil
	case noticestest.StandingGone, noticestest.StandingClosing, noticestest.StandingBlocked:
		return notices.ErrSkip
	}
	return errors.New("the account's standing is not known")
}

func TestE25TheDelivererContractCanBeKept(t *testing.T) {
	noticestest.DelivererContract(t, func(*testing.T) noticestest.DelivererSubject {
		p := &played{}
		return noticestest.DelivererSubject{Deliverer: p, Set: func(s noticestest.Standing) { p.standing = s }, Sent: func() int { return p.sent }}
	})
	if len(noticestest.Standings()) != 12 {
		t.Fatalf("%d standings, want 12", len(noticestest.Standings()))
	}
}

func TestTheClockIsPlayed(t *testing.T) {
	c := noticestest.NewClock(noticestest.SampleAt)
	c.Advance(time.Hour)
	if !c.Now().Equal(noticestest.SampleAt.Add(time.Hour)) {
		t.Fatal("the clock did not move by an hour")
	}
}

// E36: a queue that cannot be written answers its error and stores
// nothing; the caller fails nothing (the callers' tests say so).
func TestE36TheFakeFailsWhenToldTo(t *testing.T) {
	broken := errors.New("the storage cannot be written")
	f := &noticestest.Fake{Fail: broken, Now: noticestest.NewClock(noticestest.SampleAt).Now}
	if err := f.Enqueue(context.Background(), noticestest.Sample("rcp_1")); !errors.Is(err, broken) {
		t.Fatalf("Enqueue = %v, want the storage's error", err)
	}
	if len(f.Queued()) != 0 {
		t.Fatal("a notice was queued by a queue that cannot be written")
	}
}
