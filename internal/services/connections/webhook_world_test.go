package connections

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/accountrules"
	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/services/webhooks/webhookstest"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/vendors"
)

// The webhooks' world (ADR 0039's pull request 2): the service's world
// with an app whose client_id is on trip-app.com, the queue played by a
// recorder, and the account's rules kept by the real accountrules service
// over the same store, one per enclave, behind a switch that makes an
// account's rules unreadable.

const tripApp = "https://trip-app.com/oauth/client.json"

// queue records what the service tells the queue.
type queue struct {
	mu         sync.Mutex
	subscribed []webhooks.Subscription
	ended      []string
	fail       error
}

func (q *queue) Subscribe(_ context.Context, s webhooks.Subscription) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.fail != nil {
		return q.fail
	}
	q.subscribed = append(q.subscribed, s)
	return nil
}

func (q *queue) Ended(connection string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ended = append(q.ended, connection)
}

func (q *queue) subs() []webhooks.Subscription {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.subscribed)
}

func (q *queue) endedOnes() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.ended)
}

// states is the queue's state of webhooks, for the person's page.
type states struct {
	at map[string]*contracts.ConnectionWebhook
}

func (s states) States(_ context.Context, c string) (*contracts.ConnectionWebhook, error) {
	if w, ok := s.at[c]; ok {
		return w, nil
	}
	return nil, webhooks.ErrNoWebhook
}

// hookWorld is the world with webhooks.
type hookWorld struct {
	*world
	q        *queue
	unreadMu sync.Mutex
	unread   map[string]bool
	n        int
}

// hookEnclave is one enclave: the connections service and its own rules.
type hookEnclave struct {
	s     *Service
	rules *accountrules.Service
}

func newHookWorld(t *testing.T) *hookWorld {
	t.Helper()
	w := &hookWorld{world: newWorld(t), q: &queue{}, unread: map[string]bool{}}
	w.vendors.clients[tripApp] = &vendors.Client{ID: tripApp, Domain: "trip-app.com", Document: oauth.ClientDocument{
		ClientID: tripApp, ClientName: "Trip", RedirectURIs: []string{"https://trip-app.com/back"}, TokenEndpointAuthMethod: "none",
	}}
	return w
}

func (w *hookWorld) hookEnclave() *hookEnclave {
	w.t.Helper()
	a := &store.Anchored{Records: w.mem, Commits: w.commits, Strict: true, Now: w.clock.now}
	ar, err := accountrules.New(accountrules.Config{Records: a, Keys: w.keys, Receipts: w.rcpt, Mailboxes: w.mbx, Now: w.clock.now})
	if err != nil {
		w.t.Fatal(err)
	}
	cfg := w.config(a)
	cfg.Webhooks = w.q
	cfg.Source = &unreadable{w: w, Source: ar}
	s, err := New(cfg)
	if err != nil {
		w.t.Fatal(err)
	}
	return &hookEnclave{s: s, rules: ar}
}

// unreadable is the world's switch over one enclave's rules.
type unreadable struct {
	accountrules.Source
	w *hookWorld
}

func (u *unreadable) Applying(ctx context.Context, account, client string) (accountrules.Applying, error) {
	u.w.unreadMu.Lock()
	off := u.w.unread[account]
	u.w.unreadMu.Unlock()
	if off {
		return accountrules.Applying{}, accountrules.ErrUnavailable
	}
	return u.Source.Applying(ctx, account, client)
}

func (w *hookWorld) setUnreadable(account string, off bool) {
	w.unreadMu.Lock()
	w.unread[account] = off
	w.unreadMu.Unlock()
}

// name is a well-formed identifier of kind for n: 26 base32 characters.
func name(kind string, n int) string {
	h := sha256.Sum256([]byte(kind + strconv.Itoa(n)))
	return kind + "_" + idEncoding.EncodeToString(h[:16])
}

// tripAsk is trip-app's request.
func (w *hookWorld) tripAsk() *ask {
	a := w.ask(tripApp)
	a.redirect = "https://trip-app.com/back"
	a.resource = apiResource
	return a
}

// push pushes a's request, for mail from example.com, with webhook (raw
// JSON, "" for none) and starts it: the request as the screen shows it, or
// the push's or the start's error.
func (w *hookWorld) push(s *Service, p Person, a *ask, hook string) (*contracts.AuthorizationRequest, error) {
	form := url.Values{"client_id": {a.client}, "redirect_uri": {a.redirect}, "response_type": {"code"}, "code_challenge": {a.challenge},
		"code_challenge_method": {"S256"}, "resource": {a.resource}, "state": {a.state}, "query": {fromExample}}
	if hook != "" {
		form.Set("webhook", hook)
	}
	uri, err := s.Push(context.Background(), form)
	if err != nil {
		return nil, err
	}
	got, err := s.Start(context.Background(), p, url.Values{"client_id": {a.client}, "request_uri": {uri}}.Encode())
	if err != nil {
		return nil, err
	}
	if got.Request == nil {
		w.t.Fatalf("start redirected: %s", got.Redirect)
	}
	return got.Request, nil
}

// decide allows req as shown, with zone.
func (w *hookWorld) decide(s *Service, p Person, req *contracts.AuthorizationRequest, zone string) (*contracts.AuthorizationAnswer, error) {
	d, snap := req.Draft, req.Snapshot
	return s.Decide(context.Background(), p, req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &d, Snapshot: &snap, ModelNotice: req.ModelNotice, TimeZone: zone})
}

func hookJSON(r contracts.WebhookRequest) string {
	b, _ := json.Marshal(r)
	return string(b)
}

// account makes an account of its own, open, with two mailboxes.
func (w *hookWorld) account() string {
	w.n++
	account := name("acc", w.n)
	w.accts.set(account, StandingOpen)
	w.mbx.mu.Lock()
	w.mbx.catalogue[account] = []string{name("mbx", 2*w.n), name("mbx", 2*w.n+1)}
	w.mbx.mu.Unlock()
	return account
}

// connect approves, through e, trip-app's request with webhook r for
// account, and exchanges its code: the connection and the ask.
func (w *hookWorld) connect(e *hookEnclave, account string, r *contracts.WebhookRequest) (string, *ask) {
	w.t.Helper()
	c, a, _ := w.connectTok(e, account, r)
	return c, a
}

// connectTok is connect, with the tokens.
func (w *hookWorld) connectTok(e *hookEnclave, account string, r *contracts.WebhookRequest) (string, *ask, *TokenAnswer) {
	w.t.Helper()
	p := w.person(account)
	a := w.tripAsk()
	hook := ""
	if r != nil {
		hook = hookJSON(*r)
	}
	req, err := w.push(e.s, p, a, hook)
	if err != nil {
		w.t.Fatalf("push: %v", err)
	}
	ans, err := w.decide(e.s, p, req, "Europe/Madrid")
	if err != nil {
		w.t.Fatalf("allow: %v", err)
	}
	tok, err := w.exchange(e.s, a, codeOf(w.t, ans.Redirect))
	if err != nil {
		w.t.Fatalf("exchange: %v", err)
	}
	return w.connectionOf(e.s, account, tripApp).ID, a, tok
}

func jsonMarshal(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// make makes, through e, an active connection of trip-app with webhook r
// for an account of its own.
func (w *hookWorld) make(e *hookEnclave, r contracts.WebhookRequest) (string, string) {
	w.t.Helper()
	account := w.account()
	c, _ := w.connect(e, account, &r)
	return c, account
}

// accountOf is the account of a connection.
func (w *hookWorld) accountOf(e *hookEnclave, c string) string {
	w.t.Helper()
	d, _, err := e.s.readConn(context.Background(), c)
	if err != nil || d == nil {
		w.t.Fatalf("no connection %s: %v", c, err)
	}
	return d.Account
}

// holdAll saves, through e's rules with a fresh proof, a rule of account
// that holds every message.
func (w *hookWorld) holdAll(e *hookEnclave, account string) {
	w.t.Helper()
	ctx := context.Background()
	draft := contracts.RuleDraft{Name: "everything", On: true, Effect: contracts.EffectHold, Scope: contracts.RuleScope{To: contracts.ScopeAll},
		Criteria: []contracts.Formula{{V: 1, All: []contracts.Group{{Any: []contracts.Condition{
			{Var: contracts.VarSubject, Op: contracts.OpContains, Value: "trip"},
			{Var: contracts.VarSubject, Op: contracts.OpContains, Value: "news"},
		}}}}}}
	c, err := e.rules.Check(ctx, account, draft)
	if err != nil {
		w.t.Fatalf("check: %v", err)
	}
	l, err := e.rules.List(ctx, account)
	if err != nil {
		w.t.Fatal(err)
	}
	p := accountrules.Person{Account: account, ConfirmedUntil: w.clock.now().Add(10 * time.Minute)}
	if _, err := e.rules.Save(ctx, p, "", contracts.RuleSave{DraftID: c.DraftID, Draft: c.Draft, Version: l.Version}); err != nil {
		w.t.Fatalf("save: %v", err)
	}
}

// deliver stores a message in mailbox number i of connection c's account:
// from example.com (the connection's query) unless it is not to match,
// held by the default rule when it is to be held.
func (w *hookWorld) deliver(e *hookEnclave, c string, i int, received time.Time, kind webhookstest.Arrival) webhooks.Cursor {
	w.t.Helper()
	account := w.accountOf(e, c)
	w.mbx.mu.Lock()
	all := slices.Clone(w.mbx.catalogue[account])
	w.mbx.mu.Unlock()
	w.n++
	// inbox.NewMessageID's shape, its random half a counter, so that the
	// order of delivery within an instant is the identifiers' order.
	id := fmt.Sprintf("%016x%016x", uint64(received.UnixNano()), uint64(w.n)) //nolint:gosec // after 1970
	ml := mail{ID: id, Mailbox: all[i], Received: received, From: "trips@example.com", Subject: "your trip", Body: "hello"}
	switch kind {
	case webhookstest.ArrivalNoMatch:
		ml.From, ml.Subject = "news@other.example.org", "news"
	case webhookstest.ArrivalHeld:
		ml.Held, ml.Subject = true, "your sign-in code"
	}
	w.mbx.add(ml)
	return webhooks.Cursor{Received: received, Mailbox: ml.Mailbox, Message: ml.ID}
}

// contractWorld is webhookstest.World over one enclave of the real
// service.
type contractWorld struct {
	w *hookWorld
	e *hookEnclave
}

func (c *contractWorld) Connections() webhooks.Connections { return c.e.s.WebhookConnections() }
func (c *contractWorld) Make(_ *testing.T, r contracts.WebhookRequest) (string, string) {
	return c.w.make(c.e, r)
}

func (c *contractWorld) person(conn string) Person { return c.w.person(c.w.accountOf(c.e, conn)) }

func (c *contractWorld) Revoke(t *testing.T, conn string) {
	if _, err := c.e.s.Revoke(context.Background(), c.person(conn), conn); err != nil {
		t.Fatal(err)
	}
}

func (c *contractWorld) RevokeDevices(t *testing.T, conn string) {
	d, _, _ := c.e.s.readConn(context.Background(), conn)
	for _, dev := range d.Devices {
		if _, err := c.e.s.RevokeDevice(context.Background(), c.person(conn), conn, dev.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func (c *contractWorld) Expire(_ *testing.T, conn string) {
	d, _, _ := c.e.s.readConn(context.Background(), conn)
	c.w.clock.add(d.EndsAt.Sub(c.w.clock.now()) + time.Second)
}

func (c *contractWorld) Close(t *testing.T, conn string) {
	account := c.w.accountOf(c.e, conn)
	c.w.accts.set(account, StandingClosing)
	if err := c.e.s.EndAll(context.Background(), account); err != nil {
		t.Fatal(err)
	}
}

func (c *contractWorld) Remove(t *testing.T, conn string) {
	account := c.w.accountOf(c.e, conn)
	c.w.accts.set(account, StandingClosing)
	if err := c.e.s.EndAll(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	if err := c.e.s.RemoveAll(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	c.w.accts.set(account, StandingGone)
}

func (c *contractWorld) Off(t *testing.T, conn string) {
	got, err := c.e.s.Get(context.Background(), c.person(conn), conn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.e.s.WebhookOff(context.Background(), c.person(conn), conn, got.Version); err != nil {
		t.Fatal(err)
	}
}

func (c *contractWorld) ModelOff(_ *testing.T, _ string) { c.w.gate.set(inbox.AskOff) }
func (c *contractWorld) ModelOn(_ *testing.T, _ string)  { c.w.gate.set(inbox.AskAnswered) }
func (c *contractWorld) HoldAll(_ *testing.T, account string) {
	c.w.holdAll(c.e, account)
}

func (c *contractWorld) RulesUnreadable(_ *testing.T, account string, off bool) {
	c.w.setUnreadable(account, off)
}

func (c *contractWorld) DropMailbox(t *testing.T, conn string) {
	ctx := context.Background()
	d, at, err := c.e.s.readConn(ctx, conn)
	if err != nil || d == nil {
		t.Fatal(err)
	}
	d.Mailboxes = d.Mailboxes[:1]
	if err := c.e.s.writeConn(ctx, d, at, false); err != nil {
		t.Fatal(err)
	}
}

func (c *contractWorld) Deliver(_ *testing.T, conn string, mailbox int, received time.Time, kind webhookstest.Arrival) webhooks.Cursor {
	return c.w.deliver(c.e, conn, mailbox, received, kind)
}

func (c *contractWorld) Delete(_ *testing.T, _ string, m webhooks.Cursor) {
	c.w.mbx.mu.Lock()
	defer c.w.mbx.mu.Unlock()
	c.w.mbx.msgs[m.Mailbox] = slices.DeleteFunc(c.w.mbx.msgs[m.Mailbox], func(ml mail) bool { return ml.ID == m.Message })
}

func (c *contractWorld) Advance(d time.Duration) { c.w.clock.add(d) }
func (c *contractWorld) Now() time.Time          { return c.w.clock.now() }

// errQueue is a queue that could not be written.
var errQueue = errors.New("the queue could not be written")
