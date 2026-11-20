package connections

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/services/webhooks/webhookstest"
	"github.com/permaily-org/permaily/internal/webhook"
)

// ADR 0039's tests of the connections' half (K23 to K34, K32b), with the
// re-review's N2 on this side.

var (
	weekly   = &contracts.WebhookSchedule{Every: contracts.EveryWeek, On: "monday", At: "08:00"}
	hookURL  = "https://trip-app.com/hooks/permaily"
	newMail  = contracts.WebhookRequest{URL: hookURL, NewMail: true}
	withWeek = contracts.WebhookRequest{URL: hookURL, NewMail: true, Schedule: weekly}
)

// K23. What webhook.ParseRequest refuses is invalid_request with no
// details, at the pushed request, before any screen; the parameter in the
// address bar is ignored; a request without it has no webhook; a service
// with no queue takes none.
func TestK23TheWebhookIsAskedOnlyInAPushedRequest(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	account := w.account()
	p := w.person(account)
	for name, hook := range map[string]string{
		"another host":   `{"url":"https://evil.example.com/hooks","new_mail":true}`,
		"a subdomain":    `{"url":"https://hooks.trip-app.com/x","new_mail":true}`,
		"nothing asked":  `{"url":"https://trip-app.com/x"}`,
		"an unknown key": `{"url":"https://trip-app.com/x","new_mail":true,"secret":"x"}`,
		"cron":           `{"url":"https://trip-app.com/x","schedule":{"every":"minute","at":"08:00"}}`,
		"not JSON":       `url=https://trip-app.com/x`,
	} {
		_, err := w.push(e.s, p, w.tripAsk(), hook)
		var oe *OAuthError
		if !errors.As(err, &oe) || oe.Code != oauth.ErrInvalidRequest || strings.Contains(err.Error(), "webhook") {
			t.Errorf("%s: %v", name, err)
		}
	}
	req, err := w.push(e.s, p, w.tripAsk(), "")
	if err != nil || req.Webhook != nil {
		t.Fatalf("a request without a webhook: %+v %v", req, err)
	}
	// In the address bar: ignored, as the pushed request's other parameters.
	a := w.tripAsk()
	a.extra = url.Values{"webhook": {hookJSON(newMail)}}
	got, err := e.s.Start(context.Background(), p, a.query())
	if err != nil || got.Request == nil || got.Request.Webhook != nil {
		t.Fatalf("taken from the address: %+v %v", got, err)
	}
	// With no queue, a webhook is never taken.
	cfg := w.config(e.s.cfg.Records)
	bare, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.push(bare, p, w.tripAsk(), hookJSON(newMail)); oauthCode(err) != oauth.ErrInvalidRequest {
		t.Fatalf("taken without a queue: %v", err)
	}
}

// K24. AuthorizationRequest.Webhook as asked; an approval of a request with
// a schedule needs a zone the image knows; any other's zone is ignored.
func TestK24TheScreenShowsTheWebhookAndAScheduleNeedsAZone(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	account := w.account()
	p := w.person(account)
	req, err := w.push(e.s, p, w.tripAsk(), hookJSON(withWeek))
	if err != nil {
		t.Fatal(err)
	}
	if req.Webhook == nil || req.Webhook.Host != "trip-app.com" || !req.Webhook.NewMail || req.Webhook.Schedule == nil || *req.Webhook.Schedule != *weekly {
		t.Fatalf("%+v", req.Webhook)
	}
	for _, zone := range []string{"", "Local", "Mars/Olympus_Mons", "../etc/passwd", "UTC+2"} {
		if _, err := w.decide(e.s, p, req, zone); !errors.Is(err, contracts.ErrTimeZoneInvalid) {
			t.Errorf("zone %q: %v", zone, err)
		}
	}
	if _, err := w.decide(e.s, p, req, "Europe/Madrid"); err != nil {
		t.Fatal(err)
	}
	d := w.connectionOf(e.s, account, tripApp)
	if d.Webhook.private.TimeZone != "Europe/Madrid" {
		t.Fatalf("%+v", d.Webhook.private)
	}
	// New mail only: the zone sent is ignored, and none is kept.
	other := w.account()
	q := w.person(other)
	req, err = w.push(e.s, q, w.tripAsk(), hookJSON(newMail))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.decide(e.s, q, req, "not a zone"); err != nil {
		t.Fatal(err)
	}
	if d := w.connectionOf(e.s, other, tripApp); d.Webhook.private.TimeZone != "" || d.Webhook.Schedule != nil {
		t.Fatalf("%+v", d.Webhook.private)
	}
}

// K25. The record: whether it has a webhook, what it is told and its state
// readable and signed; the URL, the subscription and the zone sealed under
// PurposeWebhookSeal: a reader of the table finds none of them, and a
// sealed part moved to another record does not open.
func TestK25TheWebhookIsSealedInTheRecord(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	c, _ := w.make(e, withWeek)
	c2, _ := w.make(e, newMail)
	ctx := context.Background()
	d, _, err := e.s.readConn(ctx, c)
	if err != nil || d.Webhook == nil || d.Webhook.State != contracts.WebhookOn || !d.Webhook.NewMail || *d.Webhook.Schedule != *weekly {
		t.Fatalf("%+v %v", d.Webhook, err)
	}
	sub := d.Webhook.private.Subscription
	if !strings.HasPrefix(sub, webhook.SubscriptionPrefix) || len(sub) != len(webhook.SubscriptionPrefix)+26 || strings.Contains(sub, c[4:]) {
		t.Fatalf("subscription %q", sub)
	}
	for _, n := range w.mem.RecordNames() {
		rec, _ := w.mem.GetRecord(ctx, n)
		for _, secret := range []string{"hooks/permaily", sub, "Europe/Madrid"} {
			if rec != nil && bytes.Contains(rec.Value, []byte(secret)) {
				t.Errorf("the table's %s holds %q", n, secret)
			}
		}
	}
	for _, en := range w.mem.AllEntries() {
		for _, secret := range []string{"hooks/permaily", sub, "Europe/Madrid"} {
			if bytes.Contains(en.Value, []byte(secret)) {
				t.Errorf("the entry %s holds %q", en.Partition, secret)
			}
		}
	}
	// The sealed part is bound to its connection.
	d2, at2, _ := e.s.readConn(ctx, c2)
	d2.Webhook.Sealed = d.Webhook.Sealed
	if err := e.s.writeSigned(ctx, connName(c2), d2, at2, false); err != nil {
		t.Fatal(err)
	}
	e.s.mu.Lock()
	delete(e.s.conns, c2)
	e.s.mu.Unlock()
	if _, _, err := e.s.readConn(ctx, c2); !errors.Is(err, errDocument) {
		t.Fatalf("a webhook moved from another record opened: %v", err)
	}
}

// K26. Subscribe is called after the record and before the code; when it
// fails, no code is issued and what was made is fenced.
func TestK26SubscribeComesBeforeTheCode(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	account := w.account()
	p := w.person(account)
	req, err := w.push(e.s, p, w.tripAsk(), hookJSON(withWeek))
	if err != nil {
		t.Fatal(err)
	}
	w.q.fail = errQueue
	if _, err := w.decide(e.s, p, req, "Europe/Madrid"); !errors.Is(err, errQueue) {
		t.Fatalf("approved without its queue entry: %v", err)
	}
	if n := len(w.rcpt.of(receipts.KindConsent)); n != 0 {
		t.Fatalf("%d consent receipts for an approval that failed", n)
	}
	l, _, _ := e.s.readList(context.Background(), account)
	for _, m := range l.Members {
		if m.State == contracts.ConnectionActive {
			t.Fatalf("an active connection left: %+v", m)
		}
		d, _, _ := e.s.readConn(context.Background(), m.ID)
		if d != nil && d.live(w.clock.now()) {
			t.Fatal("its record is live")
		}
	}
	if l.Current[tripApp] != "" {
		t.Fatal("made current")
	}
	w.q.fail = nil
	c, _ := w.make(e, withWeek)
	subs := w.q.subs()
	s := subs[len(subs)-1]
	if s.Connection != c || s.URL != hookURL || s.TimeZone != "Europe/Madrid" || *s.Schedule != *weekly || len(s.Mailboxes) != 2 || !strings.HasPrefix(s.ID, webhook.SubscriptionPrefix) {
		t.Fatalf("%+v", s)
	}
}

// K27. Asking again: no webhook or the same one adds a device and keeps
// the webhook; another replaces, with a new subscription, and the old one
// is ended (and the queue told).
func TestK27AskingAgainCountsTheWebhookInTheScope(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	account := w.account()
	c, _ := w.connect(e, account, &withWeek)
	sub := w.connectionOf(e.s, account, tripApp).Webhook.private.Subscription
	for _, again := range []*contracts.WebhookRequest{nil, &withWeek} {
		c2, _ := w.connect(e, account, again)
		d := w.connectionOf(e.s, account, tripApp)
		if c2 != c || d.Webhook == nil || d.Webhook.private.Subscription != sub || d.activeDevices() < 2 {
			t.Fatalf("asked again with %+v: %s %+v", again, c2, d.Webhook)
		}
	}
	for _, other := range []contracts.WebhookRequest{
		newMail,
		{URL: hookURL, NewMail: true, Schedule: &contracts.WebhookSchedule{Every: contracts.EveryDay, At: "08:00"}},
		{URL: "https://trip-app.com/hooks/other", NewMail: true, Schedule: weekly},
	} {
		before := w.connectionOf(e.s, account, tripApp)
		c3, _ := w.connect(e, account, &other)
		d := w.connectionOf(e.s, account, tripApp)
		if c3 == before.ID || d.Webhook.private.Subscription == before.Webhook.private.Subscription {
			t.Fatalf("%+v did not replace", other)
		}
		old, _, _ := e.s.readConn(context.Background(), before.ID)
		if old.State != contracts.ConnectionEnded || old.EndReason != contracts.EndedByReplaced {
			t.Fatalf("the old one: %+v", old)
		}
		if !slices.Contains(w.q.endedOnes(), before.ID) {
			t.Fatal("the queue was not told the old one ended")
		}
	}
	// A webhook turned off is turned on only by a new request, which
	// replaces the connection.
	cur := w.connectionOf(e.s, account, tripApp)
	got, _ := e.s.Get(context.Background(), w.person(account), cur.ID)
	if _, err := e.s.WebhookOff(context.Background(), w.person(account), cur.ID, got.Version); err != nil {
		t.Fatal(err)
	}
	same := cur.Webhook.asRequest()
	c4, _ := w.connect(e, account, same)
	if d := w.connectionOf(e.s, account, tripApp); c4 == cur.ID || d.Webhook.State != contracts.WebhookOn {
		t.Fatalf("the same webhook asked again after it was turned off: %s %+v", c4, d.Webhook)
	}
}

// K28. WebhookOff: at the version shown (consent_changed otherwise), final,
// without a fresh proof, under unagreed terms; its consent receipt; a
// connection with no webhook is not found.
func TestK28ThePersonTurnsTheWebhookOff(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	ctx := context.Background()
	c, account := w.make(e, withWeek)
	p := Person{Account: account, Session: "ses_" + account} // no fresh proof
	got, err := e.s.Get(ctx, p, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.WebhookOff(ctx, p, c, got.Version+1); !errors.Is(err, contracts.ErrConsentChanged) {
		t.Fatalf("another version: %v", err)
	}
	if s, _ := e.s.Subscription(ctx, c); !s.Calls {
		t.Fatal("a refused turning off took effect")
	}
	w.accts.set(account, StandingNotAgreed)
	before := len(w.rcpt.of(receipts.KindConsent))
	off, err := e.s.WebhookOff(ctx, p, c, got.Version)
	if err != nil || off.Webhook == nil || off.Webhook.State != contracts.WebhookOff {
		t.Fatalf("%+v %v", off, err)
	}
	rs := w.rcpt.of(receipts.KindConsent)
	if len(rs) != before+1 || rs[len(rs)-1].Consent.Webhook == nil || rs[len(rs)-1].Consent.Webhook.State != contracts.WebhookOff || rs[len(rs)-1].Connection != c {
		t.Fatalf("its receipt: %+v", rs[len(rs)-1].Consent)
	}
	// Final: turning it off again changes nothing and writes nothing.
	again, err := e.s.WebhookOff(ctx, p, c, off.Version)
	if err != nil || again.Version != off.Version || len(w.rcpt.of(receipts.KindConsent)) != before+1 {
		t.Fatalf("%+v %v", again, err)
	}
	if s, _ := e.s.Subscription(ctx, c); s.Calls || s.Why != webhooks.SkipOff {
		t.Fatalf("%+v", s)
	}
	// Without a webhook, there is nothing to turn off.
	plain := w.account()
	pc, _ := w.connect(e, plain, nil)
	g, _ := e.s.Get(ctx, w.person(plain), pc)
	if _, err := e.s.WebhookOff(ctx, w.person(plain), pc, g.Version); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if g.Webhook != nil {
		t.Fatal("a connection without a webhook shows one")
	}
	// Another account's: not found.
	if _, err := e.s.WebhookOff(ctx, w.person(plain), c, got.Version); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

// K29. Stop at the version read; a 410 for a connection replaced since
// changes nothing of the new one.
func TestK29TheVendorsStopIsWrittenAtTheVersionRead(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	ctx := context.Background()
	account := w.account()
	old, _ := w.connect(e, account, &withWeek)
	cur, _ := w.connect(e, account, &newMail)
	if cur == old {
		t.Fatal("not replaced")
	}
	hooks := e.s.WebhookConnections()
	if err := hooks.Stop(ctx, old); err != nil {
		t.Fatal(err)
	}
	if s, _ := hooks.Subscription(ctx, cur); !s.Calls || s.State != contracts.WebhookOn {
		t.Fatalf("the new one changed: %+v", s)
	}
	if err := hooks.Stop(ctx, cur); err != nil {
		t.Fatal(err)
	}
	if s, _ := hooks.Subscription(ctx, cur); s.Calls || s.State != contracts.WebhookStopped {
		t.Fatalf("%+v", s)
	}
	// Written at the version read: a write that lost is read again.
	c2, _ := w.make(e, newMail)
	d, at, _ := e.s.readConn(ctx, c2)
	d.Mailboxes = d.Mailboxes[:1]
	if err := e.s.writeConn(ctx, d, at, false); err != nil {
		t.Fatal(err)
	}
	if err := hooks.Stop(ctx, c2); err != nil {
		t.Fatal(err)
	}
	if d, _, _ := e.s.readConn(ctx, c2); d.Webhook.State != contracts.WebhookStopped || len(d.Mailboxes) != 1 {
		t.Fatalf("%+v", d)
	}
	if err := hooks.Stop(ctx, "con_"+strings.Repeat("z", 26)); !errors.Is(err, webhooks.ErrNoWebhook) {
		t.Fatalf("%v", err)
	}
}

// K30. Connection.Webhook and VendorConnection.Webhook as composed; the
// vendor is never told the zone.
func TestK30ThePersonAndTheVendorAreShownTheWebhook(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	ctx := context.Background()
	next := w.clock.now().Add(72 * time.Hour)
	account := w.account()
	c, a, tok := w.connectTok(e, account, &withWeek)
	e.s.cfg.WebhookStates = states{at: map[string]*contracts.ConnectionWebhook{c: {NextAt: next, LastSentAt: w.clock.now(), FailingSince: w.clock.now(), Failure: contracts.WebhookNoAnswer}}}
	got, err := e.s.Get(ctx, w.person(account), c)
	if err != nil {
		t.Fatal(err)
	}
	cw := got.Webhook
	if cw == nil || cw.Host != "trip-app.com" || !cw.NewMail || *cw.Schedule != *weekly || cw.TimeZone != "Europe/Madrid" || cw.State != contracts.WebhookOn ||
		!cw.NextAt.Equal(next) || cw.Failure != contracts.WebhookNoAnswer {
		t.Fatalf("%+v", cw)
	}
	// The vendor: its subscription, URL and state, never the zone.
	g := w.grant(e.s, a, tok.AccessToken)
	info, err := e.s.Info(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	d := w.connectionOf(e.s, account, tripApp)
	vw := info.Webhook
	if vw == nil || vw.Subscription != d.Webhook.private.Subscription || vw.URL != hookURL || vw.State != contracts.WebhookOn || *vw.Schedule != *weekly {
		t.Fatalf("%+v", vw)
	}
	if raw, _ := jsonMarshal(info); strings.Contains(raw, "Europe") || strings.Contains(raw, "time_zone") {
		t.Fatalf("the vendor told the zone: %s", raw)
	}
	// Turned off: no next run shown.
	if _, err := e.s.WebhookOff(ctx, w.person(account), c, got.Version); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.s.Get(ctx, w.person(account), c); !got.Webhook.NextAt.IsZero() || got.Webhook.State != contracts.WebhookOff {
		t.Fatalf("%+v", got.Webhook)
	}
}

// K31, two enclaves: a revocation through one is Calls false through the
// other within five seconds; a rule saved through one makes Recheck false
// through the other within five seconds; a permit of one is cut by a stop
// through it before the stop returns, and not by one through the other,
// which it cannot outlive past WriteBy.
func TestK31TwoEnclaves(t *testing.T) {
	w := newHookWorld(t)
	a, b := w.hookEnclave(), w.hookEnclave()
	ctx := context.Background()
	c, account := w.make(a, newMail)
	start := w.clock.now()
	w.deliver(a, c, 0, start.Add(time.Second), webhookstest.ArrivalLeaves)
	e, err := b.s.Pending(ctx, c, webhooks.Progress{Cursor: webhooks.Cursor{Received: start}})
	if err != nil || e.Match == nil {
		t.Fatalf("%+v %v", e, err)
	}
	if ok, err := b.s.Recheck(ctx, c, *e.Match); !ok || err != nil {
		t.Fatal("the match does not stand")
	}
	w.holdAll(a, account)
	w.clock.add(oauth.ConnectionFresh)
	if ok, _ := b.s.Recheck(ctx, c, *e.Match); ok {
		t.Fatal("a rule saved through the other enclave did not reach this one in five seconds")
	}
	sb, pb, err := b.s.Admit(ctx, c)
	if err != nil || pb == nil {
		t.Fatal(err)
	}
	call, err := pb.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.s.Revoke(ctx, w.person(account), c); err != nil {
		t.Fatal(err)
	}
	if context.Cause(call) != nil {
		t.Fatal("cut by a stop through the other enclave, which only reaches it by WriteBy")
	}
	if !pb.WriteBy().After(w.clock.now()) || pb.WriteBy().After(sb.ReadAt.Add(oauth.ConnectionFresh)) {
		t.Fatalf("written by %s", pb.WriteBy())
	}
	pb.Finish()
	w.clock.add(oauth.ConnectionFresh)
	if s, err := b.s.Subscription(ctx, c); err != nil || s.Calls || s.Why != webhooks.SkipEnded {
		t.Fatalf("a revocation through the other enclave, five seconds later: %+v %v", s, err)
	}
}

// K32. Pending through the discloser: a message with a sign-in code held
// with no notice; one a person's rule holds makes no match (ADR 0040's
// U26); the model off is ErrModelUnavailable, the rules unreadable
// ErrRulesUnavailable, and the progress stays; the evaluation is the
// connection's (its formulas, the account's rules first, the model asked
// through the Asker a read would carry).
//
// What the real discloser does with these (a model asked once at arrival,
// its decision kept and met again by the vendor's fetch) is ADR 0040's pull
// request 5's Service.Disclosure: webhookDisclosure builds the disclosure
// here until it is merged.
func TestK32NewMailIsEvaluatedAsARead(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	ctx := context.Background()
	c, account := w.make(e, newMail)
	start := w.clock.now()
	w.deliver(e, c, 0, start.Add(time.Second), webhookstest.ArrivalHeld)
	from := webhooks.Progress{Cursor: webhooks.Cursor{Received: start}}
	ev, err := e.s.Pending(ctx, c, from)
	if err != nil || ev.Match != nil {
		t.Fatalf("a sign-in code made a notice: %+v %v", ev, err)
	}
	ds := w.disc.last()
	d := w.connectionOf(e.s, account, tripApp)
	sc, _ := d.scope()
	if len(ds.Rules) == 0 || len(ds.Formulas) != len(sc.list()) || !ds.Model || ds.Account != account {
		t.Fatalf("not the connection's evaluation: %+v", ds)
	}
	w.deliver(e, c, 1, start.Add(2*time.Second), webhookstest.ArrivalLeaves)
	w.holdAll(e, account)
	if ev, err := e.s.Pending(ctx, c, ev.Through); err != nil || ev.Match != nil {
		t.Fatalf("a message the person's rule holds made a notice: %+v %v", ev, err)
	}
	w.gate.set(inbox.AskOff)
	if _, err := e.s.Pending(ctx, c, from); !errors.Is(err, contracts.ErrModelUnavailable) {
		t.Fatalf("%v", err)
	}
	w.gate.set(inbox.AskAnswered)
	w.setUnreadable(account, true)
	if _, err := e.s.Pending(ctx, c, from); !errors.Is(err, webhooks.ErrRulesUnavailable) {
		t.Fatalf("%v", err)
	}
	// A connection approved under another model's notice gets nothing.
	w.setUnreadable(account, false)
	e.s.cfg.ModelNotice = "2099-01-01"
	if _, err := e.s.Pending(ctx, c, from); !errors.Is(err, contracts.ErrModelUnavailable) {
		t.Fatalf("%v", err)
	}
}

// K32b. Webhooks from the strict records: with the queue's entries and
// every index the queue writes deleted, every connection with a webhook is
// listed; a connection removed, or one without a webhook, is not.
func TestK32bEveryWebhookIsListedFromTheRecords(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	ctx := context.Background()
	var made []string
	for range 3 {
		c, _ := w.make(e, newMail)
		made = append(made, c)
	}
	plain := w.account()
	without, _ := w.connect(e, plain, nil)
	gone, _ := w.make(e, withWeek)
	cw := &contractWorld{w: w, e: e}
	cw.Remove(t, gone)
	for _, en := range w.mem.AllEntries() {
		if strings.HasPrefix(en.Partition, "webhooks#") {
			_ = w.mem.DeleteEntry(ctx, en.Partition, en.Key)
		}
	}
	listed, err := e.s.Webhooks(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range made {
		if !slices.Contains(listed, c) {
			t.Errorf("%s not listed", c)
		}
	}
	if slices.Contains(listed, without) || slices.Contains(listed, gone) {
		t.Fatalf("listed what has no webhook: %v", listed)
	}
	// Pages after the first come from the same listing.
	page, _ := e.s.Webhooks(ctx, listed[0], 1)
	if len(page) != 1 || page[0] != listed[1] {
		t.Fatalf("%v", page)
	}
	if none, _ := e.s.Webhooks(ctx, "", 0); none != nil {
		t.Fatal("a page of no limit")
	}
	// A record that does not open gives no mail and is not listed; the
	// others still are.
	for _, n := range w.mem.RecordNames() {
		if rec, _ := w.mem.GetRecord(ctx, n); rec != nil && strings.Contains(n, made[0]) && len(rec.Value) > 0 {
			rec.Value = append([]byte(nil), rec.Value...)
			rec.Value[len(rec.Value)-1] ^= 1
			w.mem.SetRecord(*rec)
		}
	}
	fresh := w.hookEnclave()
	listed, err = fresh.s.Webhooks(ctx, "", 100)
	if err != nil || slices.Contains(listed, made[0]) || !slices.Contains(listed, made[1]) {
		t.Fatalf("%v %v", listed, err)
	}
}

// K33. The approval's consent receipt holds the webhook approved and the
// zone, never its URL's path or its subscription; a refusal's holds what
// was asked.
func TestK33TheConsentReceiptHoldsTheWebhook(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	account := w.account()
	w.connect(e, account, &withWeek)
	rs := w.rcpt.of(receipts.KindConsent)
	cw := rs[len(rs)-1].Consent.Webhook
	if cw == nil || cw.Host != "trip-app.com" || !cw.NewMail || *cw.Schedule != *weekly || cw.TimeZone != "Europe/Madrid" || cw.State != contracts.WebhookOn {
		t.Fatalf("%+v", cw)
	}
	raw, _ := jsonMarshal(rs[len(rs)-1])
	if strings.Contains(raw, "hooks/permaily") || strings.Contains(raw, webhook.SubscriptionPrefix) {
		t.Fatalf("the receipt holds the URL or the subscription: %s", raw)
	}
	other := w.account()
	p := w.person(other)
	req, _ := w.push(e.s, p, w.tripAsk(), hookJSON(newMail))
	if _, err := e.s.Decide(context.Background(), p, req.ID, contracts.AuthorizationDecision{Allow: false}); err != nil {
		t.Fatal(err)
	}
	rs = w.rcpt.of(receipts.KindConsent)
	if r := rs[len(rs)-1]; r.Consent.Allowed || r.Consent.Webhook == nil || r.Consent.Webhook.Host != "trip-app.com" {
		t.Fatalf("%+v", r.Consent)
	}
}

// K34. Renewing keeps the webhook; removing the account removes it with
// the record.
func TestK34RenewingKeepsTheWebhookAndRemovalTakesIt(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	ctx := context.Background()
	c, account := w.make(e, withWeek)
	before := w.connectionOf(e.s, account, tripApp)
	got, _ := e.s.Get(ctx, w.person(account), c)
	w.clock.add(time.Hour)
	if _, err := e.s.Renew(ctx, w.person(account), c, contracts.ConnectionRenewal{Version: got.Version, ModelNotice: notice}); err != nil {
		t.Fatal(err)
	}
	after := w.connectionOf(e.s, account, tripApp)
	if after.ID != c || after.Webhook == nil || after.Webhook.private.Subscription != before.Webhook.private.Subscription || after.Webhook.State != contracts.WebhookOn {
		t.Fatalf("%+v", after.Webhook)
	}
	(&contractWorld{w: w, e: e}).Remove(t, c)
	if _, err := e.s.Subscription(ctx, c); !errors.Is(err, webhooks.ErrNoWebhook) {
		t.Fatalf("%v", err)
	}
	for _, n := range w.mem.RecordNames() {
		if strings.Contains(n, c) {
			if rec, _ := w.mem.GetRecord(ctx, n); rec != nil && len(rec.Value) > 0 {
				t.Errorf("%s left", n)
			}
		}
	}
}

// The re-review's N2, the connections' side: Admit answers the basis its
// own read rested on, so a rule saved between a match's Recheck and its
// admission gives a basis the worker sees differ from the match's, and it
// does not use the permit.
func TestN2AdmitAnswersTheBasisOfItsOwnRead(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	ctx := context.Background()
	c, account := w.make(e, newMail)
	start := w.clock.now()
	w.deliver(e, c, 0, start.Add(time.Second), webhookstest.ArrivalLeaves)
	ev, err := e.s.Pending(ctx, c, webhooks.Progress{Cursor: webhooks.Cursor{Received: start}})
	if err != nil || ev.Match == nil {
		t.Fatal(err)
	}
	if ok, _ := e.s.Recheck(ctx, c, *ev.Match); !ok {
		t.Fatal("does not stand")
	}
	s, p, err := e.s.Admit(ctx, c)
	if err != nil || p == nil || s.Basis != ev.Match.Basis {
		t.Fatalf("unchanged, the admission's basis is not the match's: %+v %+v", s, ev.Match.Basis)
	}
	p.Finish()
	w.holdAll(e, account) // saved after the recheck, before the admission
	s, p, err = e.s.Admit(ctx, c)
	if err != nil || p == nil {
		t.Fatal(err)
	}
	defer p.Finish()
	if s.Basis == ev.Match.Basis {
		t.Fatal("a rule saved before the admission left its basis the match's")
	}
}

// An admission whose read began before a stop through this enclave
// registers no permit: the stop that returned left nothing a call could
// start with.
func TestAnAdmissionRacingAStopRegistersNoPermit(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	c, _ := w.make(e, newMail)
	gen := e.s.permitGen(c)
	e.s.cutPermits(c)
	if _, ok := e.s.registerPermit(c, gen, w.clock.now().Add(time.Second)); ok {
		t.Fatal("registered after a cut its read did not see")
	}
	// A permit is written by the connection's end at the latest.
	end := w.clock.now().Add(time.Second)
	if by := webhookWriteBy(w.clock.now(), end); !by.Equal(end) {
		t.Fatal(by)
	}
	// Started twice, or after it finished: cut.
	_, p, err := e.s.Admit(context.Background(), c)
	if err != nil || p == nil {
		t.Fatal(err)
	}
	if _, err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Start(context.Background()); !errors.Is(err, webhook.ErrCut) {
		t.Fatal("started twice")
	}
	p.Finish()
	p.Finish()
}

// Turning a webhook off that cannot be recorded, or whose record changed
// meanwhile, changes nothing: its mark in this enclave is taken back and it
// is called again.
func TestATurningOffThatFailsTakesBackItsMark(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	ctx := context.Background()
	c, account := w.make(e, newMail)
	p := w.person(account)
	got, _ := e.s.Get(ctx, p, c)
	w.rcpt.fail = errors.New("the receipts could not be written")
	if _, err := e.s.WebhookOff(ctx, p, c, got.Version); err == nil {
		t.Fatal("turned off without its receipt")
	}
	w.rcpt.fail = nil
	if s, _ := e.s.Subscription(ctx, c); !s.Calls {
		t.Fatalf("a turning off that failed left it off here: %+v", s)
	}
	// The record changes between the receipt and the write.
	w.rcpt.onWrite = func(*receipts.Receipt) {
		w.rcpt.onWrite = nil
		(&contractWorld{w: w, e: e}).DropMailbox(t, c)
	}
	if _, err := e.s.WebhookOff(ctx, p, c, got.Version); !errors.Is(err, contracts.ErrConsentChanged) {
		t.Fatalf("%v", err)
	}
	if s, _ := e.s.Subscription(ctx, c); !s.Calls {
		t.Fatalf("%+v", s)
	}
}

// A burst larger than one span of arrivals reads (arrivalWalkMax) in one
// mailbox is read in smaller spans: the oldest are evaluated first.
func TestABurstLargerThanASpanIsReadInSmallerOnes(t *testing.T) {
	w := newHookWorld(t)
	e := w.hookEnclave()
	c, _ := w.make(e, newMail)
	start := w.clock.now()
	var first webhooks.Cursor
	for i := range arrivalWalkMax + 20 {
		at := w.deliver(e, c, 0, start.Add(time.Second+time.Duration(i)*time.Millisecond), webhookstest.ArrivalLeaves)
		if i == 0 {
			first = at
		}
	}
	ev, err := e.s.Pending(context.Background(), c, webhooks.Progress{Cursor: webhooks.Cursor{Received: start}})
	if err != nil || ev.Match == nil || ev.Match.Messages[0] != first || !ev.More || len(ev.Match.Messages) != webhook.MatchMessagesMax {
		t.Fatalf("%+v %v", ev, err)
	}
}
