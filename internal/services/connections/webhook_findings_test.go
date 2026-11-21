package connections

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/accountrules"
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

// vendorGets says whether a vendor's read of m through g would give it:
// the disclosure prepare builds for the read, evaluated by the discloser.
func vendorGets(t *testing.T, w *hookWorld, e *hookEnclave, g *oauth.Grant, m webhooks.Cursor) bool {
	t.Helper()
	ctx := context.Background()
	r, err := e.s.prepare(ctx, g, OperationRead, nil)
	if err != nil {
		t.Fatalf("the read: %v", err)
	}
	box, err := w.mbx.OpenForConnection(ctx, g.Account, m.Mailbox)
	if err != nil {
		t.Fatal(err)
	}
	o, err := e.s.cfg.Discloser.Message(e.s.asking(ctx, r.d), box, r.d, m.Message)
	if err != nil {
		t.Fatalf("the read: %v", err)
	}
	return o.Disclosed != nil
}

// Finding 2: a notice of new mail is decided as a vendor's read of it is,
// with the same rules from the same source: a message the default rule
// holds, or a person's rule, never makes a match; with the default turned
// off by the person, the message both leaves and makes a match. In every
// case Pending matches a message exactly when the vendor's read gives it.
func TestReview200Finding2RulesAreTheReadsRules(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	ctx := context.Background()
	account := w.account()
	c, ask, tok := w.connectTok(e, account, &newMail)
	g := w.grant(e.s, ask, tok.AccessToken)
	start := w.clock.now()
	from := webhooks.Progress{Cursor: webhooks.Cursor{Received: start}}
	same := func(why string, m webhooks.Cursor, leaves bool) {
		t.Helper()
		ev, err := e.s.Pending(ctx, c, from)
		if err != nil {
			t.Fatalf("%s: %v", why, err)
		}
		matched := ev.Match != nil && slices.Contains(ev.Match.Messages, m)
		gets := vendorGets(t, w, e, g, m)
		if matched != gets || gets != leaves {
			t.Fatalf("%s: match %v, the vendor's read gives it %v, want %v", why, matched, gets, leaves)
		}
	}
	held := w.deliver(e, c, 0, start.Add(time.Second), webhookstest.ArrivalHeld)
	same("the default rule holds it", held, false)

	l, err := e.rules.List(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	p := accountrules.Person{Account: account, ConfirmedUntil: w.clock.now().Add(10 * time.Minute)}
	if _, err := e.rules.SetDefault(ctx, p, contracts.RuleDefaultSet{On: false, Version: l.Version}); err != nil {
		t.Fatal(err)
	}
	same("the default turned off", held, true)

	trip := w.deliver(e, c, 1, start.Add(2*time.Second), webhookstest.ArrivalLeaves)
	same("no rule holds it", trip, true)
	w.holdAll(e, account)
	same("a person's rule holds it", trip, false)
	same("a person's rule that does not apply", held, true)
}

// Finding 2 (N2): a rule saved between the recheck and the admission
// changes the basis Admit answers, so the queue discards the match.
func TestReview200Finding2RuleBetweenRecheckAndAdmit(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	ctx := context.Background()
	c, account := w.make(e, newMail)
	start := w.clock.now()
	w.deliver(e, c, 0, start.Add(time.Second), webhookstest.ArrivalLeaves)
	ev, err := e.s.Pending(ctx, c, webhooks.Progress{Cursor: webhooks.Cursor{Received: start}})
	if err != nil || ev.Match == nil {
		t.Fatalf("%+v %v", ev, err)
	}
	if ok, err := e.s.Recheck(ctx, c, *ev.Match); !ok || err != nil {
		t.Fatalf("recheck %v %v", ok, err)
	}
	w.holdAll(e, account)
	sub, p, err := e.s.Admit(ctx, c)
	if p != nil {
		defer p.Finish()
	}
	if err != nil || sub.Basis == ev.Match.Basis {
		t.Fatalf("the admission rests on the match's basis: %+v %v", sub, err)
	}
	if ok, _ := e.s.Recheck(ctx, c, *ev.Match); ok {
		t.Fatal("the match stands under the new rule")
	}
}

// Finding 2: a rule that only removes parts of a message changes the basis
// but not whether it leaves: it still makes a match, and the vendor's read
// still gives it.
func TestReview200Finding2RemovingRuleStillNotifies(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	ctx := context.Background()
	account := w.account()
	c, ask, tok := w.connectTok(e, account, &newMail)
	g := w.grant(e.s, ask, tok.AccessToken)
	start := w.clock.now()
	m := w.deliver(e, c, 0, start.Add(time.Second), webhookstest.ArrivalLeaves)
	draft := contracts.RuleDraft{Name: "no bodies", On: true, Effect: contracts.EffectRemove, Removal: &contracts.RuleRemoval{Parts: []contracts.Part{contracts.PartBody}},
		Scope: contracts.RuleScope{To: contracts.ScopeAll}, Criteria: []contracts.Formula{{V: 1, All: []contracts.Group{{Any: []contracts.Condition{
			{Var: contracts.VarSubject, Op: contracts.OpContains, Value: "trip"},
		}}}}}}
	chk, err := e.rules.Check(ctx, account, draft)
	if err != nil {
		t.Fatal(err)
	}
	l, err := e.rules.List(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	p := accountrules.Person{Account: account, ConfirmedUntil: w.clock.now().Add(10 * time.Minute)}
	if _, err := e.rules.Save(ctx, p, "", contracts.RuleSave{DraftID: chk.DraftID, Draft: chk.Draft, Version: l.Version}); err != nil {
		t.Fatal(err)
	}
	ev, err := e.s.Pending(ctx, c, webhooks.Progress{Cursor: webhooks.Cursor{Received: start}})
	if err != nil || ev.Match == nil || !slices.Contains(ev.Match.Messages, m) {
		t.Fatalf("a removing rule prevented the notice: %+v %v", ev, err)
	}
	if !vendorGets(t, w, e, g, m) {
		t.Fatal("the vendor's read does not give it")
	}
	ds := w.disc.last()
	if !slices.ContainsFunc(ds.Rules, func(r rules.Rule) bool { return r.Effect == rules.Remove }) {
		t.Fatalf("the evaluation did not apply the removing rule: %+v", ds.Rules)
	}
}
