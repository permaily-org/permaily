package connections

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/services/webhooks/webhookstest"
)

// The review of pull request 200's reproductions: a webhook's admission is
// decided by the same authority as a vendor's read (authority).

// Finding 1, two enclaves: a closing generation's fence committed through
// one enclave, its members not yet ended, refuses the webhook through the
// other once its own reads are ConnectionFresh old, as it refuses a
// vendor's read: no permit, no evaluation, no match standing.
func TestReview200Finding1ClosingFence(t *testing.T) {
	w := newHookWorld(t)
	a, b := w.hookEnclave(), w.hookEnclave()
	ctx := context.Background()
	account := w.account()
	c, ask, tok := w.connectTok(a, account, &newMail)
	g := w.grant(b.s, ask, tok.AccessToken)
	start := w.clock.now()
	w.deliver(a, c, 0, start.Add(time.Second), webhookstest.ArrivalLeaves)
	from := webhooks.Progress{Cursor: webhooks.Cursor{Received: start}}
	// b reads everything once, so what it keeps must age out.
	ev, err := b.s.Pending(ctx, c, from)
	if err != nil || ev.Match == nil {
		t.Fatalf("%+v %v", ev, err)
	}
	sub, p, err := b.s.Admit(ctx, c)
	if err != nil || p == nil || !sub.Calls {
		t.Fatalf("%+v %v", sub, err)
	}
	p.Finish()

	if _, err := a.s.updateList(ctx, account, func(l *ListState) error { l.Closing = true; l.Generation++; return nil }); err != nil {
		t.Fatal(err)
	}
	w.clock.add(oauth.ConnectionFresh)
	if _, _, err := b.s.authority(ctx, g, false); err == nil {
		t.Fatal("the vendor's read was allowed")
	}
	sub, p, err = b.s.Admit(ctx, c)
	if p != nil {
		p.Finish()
		t.Fatal("a permit past the fence")
	}
	if err != nil || sub.Calls || sub.Why != webhooks.SkipEnded {
		t.Fatalf("%+v %v", sub, err)
	}
	if sub, err := b.s.Subscription(ctx, c); err != nil || sub.Calls || sub.Why != webhooks.SkipEnded {
		t.Fatalf("%+v %v", sub, err)
	}
	if ev, err := b.s.Pending(ctx, c, from); err == nil {
		t.Fatalf("evaluated past the fence: %+v", ev)
	}
	if ok, _ := b.s.Recheck(ctx, c, *ev.Match); ok {
		t.Fatal("a match stands past the fence")
	}
}

// Finding 1: an account's list whose record is refused (here, altered)
// refuses the webhook as it refuses a vendor's read, with an error (the
// record cannot be read), never a subscription that calls.
func TestReview200Finding1RefusedList(t *testing.T) {
	w := newHookWorld(t)
	a := w.hookEnclave()
	ctx := context.Background()
	account := w.account()
	c, ask, tok := w.connectTok(a, account, &newMail)
	g := w.grant(a.s, ask, tok.AccessToken)
	start := w.clock.now()
	w.deliver(a, c, 0, start.Add(time.Second), webhookstest.ArrivalLeaves)
	from := webhooks.Progress{Cursor: webhooks.Cursor{Received: start}}
	ev, err := a.s.Pending(ctx, c, from)
	if err != nil || ev.Match == nil {
		t.Fatalf("%+v %v", ev, err)
	}
	changed := false
	for _, n := range w.mem.RecordNames() {
		if !strings.HasPrefix(n, ListName(account, a.s.listEpoch(account))+"@") {
			continue
		}
		rec, err := w.mem.GetRecord(ctx, n)
		if err != nil {
			t.Fatal(err)
		}
		rec.Value = append([]byte(nil), rec.Value...)
		rec.Value[len(rec.Value)-1] ^= 1
		w.mem.SetRecord(*rec)
		changed = true
	}
	if !changed {
		t.Fatal("no version of the list")
	}
	b := w.hookEnclave()
	if _, _, err := b.s.authority(ctx, g, true); err == nil {
		t.Fatal("the vendor's read was allowed")
	}
	sub, p, err := b.s.Admit(ctx, c)
	if p != nil {
		p.Finish()
		t.Fatal("a permit over a refused list")
	}
	if err == nil {
		t.Fatalf("admitted: %+v", sub)
	}
	if sub, err := b.s.Subscription(ctx, c); err == nil {
		t.Fatalf("read: %+v", sub)
	}
	if ev, err := b.s.Pending(ctx, c, from); err == nil {
		t.Fatalf("evaluated over a refused list: %+v", ev)
	}
	if ok, err := b.s.Recheck(ctx, c, *ev.Match); ok || err == nil {
		t.Fatalf("Recheck %v %v", ok, err)
	}
}

// Finding 1: what an admission rests on is as old as the oldest of its
// reads, the account's list included, so its write boundary is never later
// than that read's start plus ConnectionFresh.
func TestReview200Finding1WriteByFromList(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	ctx := context.Background()
	c, _ := w.make(e, newMail)
	account := w.accountOf(e, c)
	listRead := w.clock.now()
	if _, _, err := e.s.listClosing(ctx, account, true); err != nil {
		t.Fatal(err)
	}
	w.clock.add(time.Second)
	// The connection's record and the standing are read again now; the list
	// is still the one read a second ago.
	e.s.mu.Lock()
	delete(e.s.conns, c)
	clear(e.s.stand)
	e.s.mu.Unlock()
	sub, p, err := e.s.Admit(ctx, c)
	if err != nil || p == nil {
		t.Fatalf("%+v %v", sub, err)
	}
	defer p.Finish()
	if sub.ReadAt.After(listRead) || p.WriteBy().After(listRead.Add(oauth.ConnectionFresh)) {
		t.Fatalf("read at %v, write by %v, the list read at %v", sub.ReadAt, p.WriteBy(), listRead)
	}
}
