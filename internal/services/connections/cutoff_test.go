package connections

import (
	"context"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
)

// The second review of pull request 136, finding 3 (ruling 3 of its third
// rulings): a started answer ends at its verified authority's freshness by
// a cutoff of its own, armed at Start, never only at the watcher's next
// tick; a refresh extends it only when it arrives while the answer is still
// within it.

// advancing makes s's clock run from the world's, as a machine's does.
func advancing(w *world, s *Service) {
	base, wall := w.clock.now(), time.Now()
	s.cfg.Now = func() time.Time { return base.Add(time.Since(wall)) }
}

// V59: the reviewer's reproduction: revoked through another enclave right
// after the verified read, an answer started 60ms before that read's
// freshness passes ends when it passes, between the watcher's ticks.
func TestV59LeaseEndsAtFreshnessBetweenTicks(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	_, rel, err := s.Search(context.Background(), g, contracts.VendorSearch{})
	if err != nil {
		t.Fatal(err)
	}
	defer rel.Finish(false)
	if _, err := w.enclave().Revoke(context.Background(), w.person(accountA), g.Connection); err != nil {
		t.Fatal(err)
	}
	w.clock.add(oauth.ConnectionFresh - 60*time.Millisecond)
	advancing(w, s)
	c, err := rel.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	left := g.FreshUntil().Sub(s.now())
	if left < 0 {
		t.Fatal("setup took the remaining freshness")
	}
	<-time.After(left + 60*time.Millisecond)
	if c.Err() == nil {
		t.Fatalf("transport remains writable %v after the last verified authority expired; tick=%v", s.now().Sub(g.FreshUntil()), leaseTick)
	}
}

// V59: an answer started with less than leaseAhead of its freshness left,
// its connection still active, reads its authority again at once and goes
// on past the freshness it started with.
func TestV59LeaseStartedNearItsFreshnessRefreshesAtOnce(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	_, rel, err := s.Search(context.Background(), g, contracts.VendorSearch{})
	if err != nil {
		t.Fatal(err)
	}
	defer rel.Finish(true)
	w.clock.add(oauth.ConnectionFresh - time.Second)
	advancing(w, s)
	c, err := rel.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	<-time.After(g.FreshUntil().Sub(s.now()) + 300*time.Millisecond)
	if c.Err() != nil {
		t.Fatal("an answer whose authority was read again in time ended at the freshness it started with")
	}
}

// V59: a refresh that returns after the freshness passed revives nothing:
// the answer ended at the freshness, and the late read leaves it ended.
func TestV59LateRefreshDoesNotReviveTheLease(t *testing.T) {
	w := newWorld(t)
	s, f := w.hooked()
	x := w.ask(claudeCode)
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	_, rel, err := s.Search(context.Background(), g, contracts.VendorSearch{})
	if err != nil {
		t.Fatal(err)
	}
	defer rel.Finish(false)
	w.clock.add(oauth.ConnectionFresh - time.Second)
	entered, unblock := make(chan struct{}), make(chan struct{})
	once := false
	f.set(func(op, name string) error {
		if op == "get" && name == connName(g.Connection) && !once {
			once = true
			close(entered)
			<-unblock
		}
		return nil
	})
	advancing(w, s)
	c, err := rel.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the watcher did not read the authority again")
	}
	select {
	case <-c.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the answer outlived its freshness while its refresh was blocked")
	}
	close(unblock)
	time.Sleep(3 * leaseTick)
	if c.Err() == nil {
		t.Fatal("a late refresh revived the answer")
	}
	l := rel.(*lease)
	l.mu.Lock()
	checked := l.grant.CheckedAt
	l.mu.Unlock()
	if !checked.Equal(g.CheckedAt) {
		t.Fatal("a late refresh extended the lease's authority")
	}
}
