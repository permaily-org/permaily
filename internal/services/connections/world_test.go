package connections

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/vendors"
)

// The world the service's tests run in (ADR 0030, "Tests": "two enclaves"
// means two services over one store, every flow started through one and
// ended through the other): one store.Memory and one store of commits, each
// enclave with its own strict Anchored and its own memory; played vendors,
// accounts, mailboxes, a discloser over messages kept here and evaluated by
// query.Eval, the model's gate and a receipts writer that enforces
// receipts.MaxDocumentBytes. Nothing reaches a real vendor, AWS or OpenAI.

var start = time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)

const (
	issuer      = "https://api.permaily.test"
	apiResource = "https://api.permaily.test/v1"
	mcpResource = "https://mcp.permaily.test/mcp"
	claudeCode  = "https://claude.ai/oauth/claude-code-client-metadata"
	chatgpt     = "https://chatgpt.com/oauth/client.json"
	otherApp    = "https://tools.example.org/permaily/client.json"
	loopback    = "http://localhost/callback"
	notice      = "2026-11-01"
	accountA    = "acc_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	accountB    = "acc_bbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var oauthConfig = oauth.Config{Issuer: issuer, AppOrigin: "https://app.permaily.test", APIResource: apiResource, MCPResource: mcpResource}

// mailboxID is a well-formed mailbox identifier: base32, lower case.
func mailboxID(n int) string { return "mbx_" + strings.Repeat(string(rune('a'+n)), 26) }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// ---- vendors ----

type resolver struct {
	mu      sync.Mutex
	clients map[string]*vendors.Client
	keys    map[string][]vendors.Key
	fail    error
	// clientErr is the error a client_id is answered with, when set.
	clientErr map[string]error
	// fresh are keys a client's set holds only when fetched again.
	fresh map[string][]vendors.Key
	// onClient runs on every Client, for a test's barrier.
	onClient func()
	fetches  int
}

func (r *resolver) Client(_ context.Context, id string) (*vendors.Client, error) {
	r.mu.Lock()
	f, c, fail := r.onClient, r.clients[id], r.fail
	if e, ok := r.clientErr[id]; ok {
		fail = e
	}
	r.fetches++
	r.mu.Unlock()
	if f != nil {
		f()
	}
	if fail != nil {
		return nil, fail
	}
	if c == nil {
		return nil, vendors.ErrClientID
	}
	cc := *c
	return &cc, nil
}

func (r *resolver) Keys(_ context.Context, c *vendors.Client, refresh bool) ([]vendors.Key, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.fresh[c.ID]; ok && refresh {
		r.keys[c.ID] = append(r.keys[c.ID], f...)
		delete(r.fresh, c.ID)
	}
	k, ok := r.keys[c.ID]
	if !ok {
		return nil, vendors.ErrUnreachable
	}
	return k, nil
}

// What the resolver answers of a client_id or a document it cannot use.
var (
	errClientIDForTest = fmt.Errorf("resolve: %w", vendors.ErrClientID)
	errDocumentForTest = fmt.Errorf("resolve: %w", vendors.ErrDocument)
)

// ---- accounts ----

type accounts struct {
	mu   sync.Mutex
	st   map[string]Standing
	fail error
	hook func(account string)
}

func (a *accounts) ConnectionStanding(_ context.Context, account string) (Standing, error) {
	a.mu.Lock()
	st, ok := a.st[account]
	fail, hook := a.fail, a.hook
	a.mu.Unlock()
	if hook != nil {
		hook(account)
	}
	if fail != nil {
		return 0, fail
	}
	if !ok {
		return StandingGone, nil
	}
	return st, nil
}

func (a *accounts) set(account string, st Standing) {
	a.mu.Lock()
	a.st[account] = st
	a.mu.Unlock()
}

// ---- mail ----

type mail struct {
	ID          string
	Mailbox     string
	Received    time.Time
	From        string
	Subject     string
	Body        string
	Attachments []inbox.Attachment
	// Held: the default rule holds it.
	Held bool
}

type mailboxes struct {
	mu        sync.Mutex
	catalogue map[string][]string
	msgs      map[string][]mail // by mailbox, oldest first
	gone      map[string]bool
	opened    []string
	// fail fails every OpenForConnection.
	fail error
}

func (m *mailboxes) Catalogue(_ context.Context, account string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.catalogue[account]), nil
}

func (m *mailboxes) OpenForConnection(_ context.Context, account, mailbox string) (inbox.Box, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return inbox.Box{}, m.fail
	}
	if !slices.Contains(m.catalogue[account], mailbox) || m.gone[mailbox] {
		return inbox.Box{}, contracts.ErrNotFound
	}
	m.opened = append(m.opened, mailbox)
	return inbox.Box{MailboxID: mailbox, Key: []byte(account)}, nil
}

func (m *mailboxes) add(ml mail) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs[ml.Mailbox] = append(m.msgs[ml.Mailbox], ml)
	sort.SliceStable(m.msgs[ml.Mailbox], func(i, j int) bool { return m.msgs[ml.Mailbox][i].ID < m.msgs[ml.Mailbox][j].ID })
}

// discloser evaluates the formulas with query.Eval and the default rule as
// each message's Held says; it records the disclosures it was given.
type discloser struct {
	m     *mailboxes
	mu    sync.Mutex
	seen  []inbox.Disclosure
	pause func()
	// pageMax bounds a page's items, as the matcher's budgets would.
	pageMax int
	// fail fails every call; model, when set, is what a page says of the
	// model.
	fail  error
	model contracts.MatchModel
	// askings is the Asker each call's context carried (inbox.AskerFrom),
	// beside seen.
	askings []askingSeen
}

// askingSeen is what a call's context carried of the model's Asker.
type askingSeen struct {
	inbox.Asking
	ok bool
}

func (d *discloser) record(ctx context.Context, ds inbox.Disclosure) {
	a, ok := inbox.AskerFrom(ctx)
	d.mu.Lock()
	d.seen = append(d.seen, ds)
	d.askings = append(d.askings, askingSeen{Asking: a, ok: ok})
	p := d.pause
	d.mu.Unlock()
	if p != nil {
		p()
	}
}

func (d *discloser) last() inbox.Disclosure {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.seen[len(d.seen)-1]
}

func evalMessage(ml mail) *query.Message {
	qm := &query.Message{
		Have: query.FromItem | query.FromMeta | query.FromEntry, Mailbox: ml.Mailbox, Received: ml.Received,
		From: []string{ml.From}, Subject: ml.Subject, Body: ml.Body,
	}
	for _, a := range ml.Attachments {
		qm.Attachments = append(qm.Attachments, query.Attachment{Name: a.Name, Type: a.Type})
	}
	return qm
}

func (d *discloser) outcome(ml mail, ds inbox.Disclosure) (inbox.Outcome, bool) {
	if query.Eval(ds.Formulas, evalMessage(ml), nil, query.ForQuery).Value != query.True {
		return inbox.Outcome{}, false
	}
	if i := slices.IndexFunc(ds.Rules, func(r rules.Rule) bool { return r.ID == rules.Default().ID }); ml.Held && i >= 0 {
		return inbox.Outcome{Withheld: &inbox.Withheld{ID: ml.ID, Rule: ds.Rules[i].Ref(), Why: inbox.HeldExact}}, true
	}
	// The person's own rules (ADR 0040) hold what they apply to, read for
	// a rule: True or Unknown.
	for _, r := range ds.Rules {
		if r.ID != rules.Default().ID && r.Effect == rules.Hold && query.Eval(r.Formulas, evalMessage(ml), nil, query.ForRule).Value != query.False {
			return inbox.Outcome{Withheld: &inbox.Withheld{ID: ml.ID, Rule: r.Ref(), Why: inbox.HeldExact}}, true
		}
	}
	subject, body := ml.Subject, ml.Body
	vm := contracts.VendorMessage{ReceivedAt: ml.Received, From: []contracts.MailAddress{{Address: ml.From}}, Subject: &subject, Text: &body}
	for i, a := range ml.Attachments {
		vm.Attachments = append(vm.Attachments, contracts.VendorAttachment{Index: i, Name: a.Name, Type: a.Type, Size: len(a.Data)})
	}
	return inbox.Outcome{Disclosed: &inbox.Disclosed{ID: ml.ID, Message: vm}}, true
}

func (d *discloser) Page(ctx context.Context, b inbox.Box, ds inbox.Disclosure, before string, limit int) (*inbox.DisclosePage, error) {
	d.record(ctx, ds)
	d.mu.Lock()
	fail, model := d.fail, d.model
	d.mu.Unlock()
	if fail != nil {
		return nil, fail
	}

	d.m.mu.Lock()
	msgs := slices.Clone(d.m.msgs[b.MailboxID])
	d.m.mu.Unlock()
	page := &inbox.DisclosePage{Model: contracts.MatchModelAsked}
	if !ds.Model {
		page.Model = contracts.MatchModelOff
	}
	if model != "" {
		page.Model = model
	}
	considered := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		ml := msgs[i]
		if before != "" && ml.ID >= before {
			continue
		}
		if considered == limit || (d.pageMax > 0 && considered == d.pageMax) {
			return page, nil
		}
		considered++
		page.Next = ml.ID
		if o, ok := d.outcome(ml, ds); ok {
			page.Items = append(page.Items, o)
		}
	}
	page.Next = ""
	return page, nil
}

func (d *discloser) find(b inbox.Box, id string) (mail, bool) {
	d.m.mu.Lock()
	defer d.m.mu.Unlock()
	for _, ml := range d.m.msgs[b.MailboxID] {
		if ml.ID == id {
			return ml, true
		}
	}
	return mail{}, false
}

func (d *discloser) Message(ctx context.Context, b inbox.Box, ds inbox.Disclosure, id string) (inbox.Outcome, error) {
	d.record(ctx, ds)
	d.mu.Lock()
	fail := d.fail
	d.mu.Unlock()
	if fail != nil {
		return inbox.Outcome{}, fail
	}
	ml, ok := d.find(b, id)
	if !ok {
		return inbox.Outcome{}, contracts.ErrNotFound
	}
	o, ok := d.outcome(ml, ds)
	if !ok {
		return inbox.Outcome{}, contracts.ErrNotFound
	}
	return o, nil
}

func (d *discloser) Attachment(ctx context.Context, b inbox.Box, ds inbox.Disclosure, id string, index int) (inbox.Outcome, *inbox.Attachment, error) {
	d.record(ctx, ds)
	if !slices.Contains(ds.Parts, contracts.PartAttachments) {
		return inbox.Outcome{}, nil, contracts.ErrPartNotApproved
	}
	ml, ok := d.find(b, id)
	if !ok || index >= len(ml.Attachments) {
		return inbox.Outcome{}, nil, contracts.ErrNotFound
	}
	o, ok := d.outcome(ml, ds)
	if !ok {
		return inbox.Outcome{}, nil, contracts.ErrNotFound
	}
	if o.Withheld != nil {
		return o, nil, nil
	}
	a := ml.Attachments[index]
	return o, &a, nil
}

// ---- the model's gate ----

type gate struct {
	mu sync.Mutex
	o  inbox.AskOutcome
}

func (g *gate) Gate(context.Context, string) inbox.AskOutcome {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.o
}

func (g *gate) set(o inbox.AskOutcome) {
	g.mu.Lock()
	g.o = o
	g.mu.Unlock()
}

// ---- receipts ----

type writer struct {
	mu    sync.Mutex
	all   []receipts.Receipt
	fail  error
	block chan struct{}
	n     int
	// onWrite runs before a receipt is kept, for a test's barrier.
	onWrite func(r *receipts.Receipt)
	// lose, when it says so of a receipt, keeps it and answers an error:
	// the storage's answer lost.
	lose func(r *receipts.Receipt) bool
	// bounds are, by receipt, what its context's deadline left when its
	// write began.
	bounds map[string]time.Duration
}

func (w *writer) Write(ctx context.Context, account string, r *receipts.Receipt) error {
	var bound time.Duration
	if dl, ok := ctx.Deadline(); ok {
		bound = time.Until(dl)
	}
	// As the real writer, it honours its context.
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	block, on := w.block, w.onWrite
	w.mu.Unlock()
	if on != nil {
		on(r)
	}
	w.mu.Lock()
	fail := w.fail
	w.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if fail != nil {
		return fail
	}
	// The writer's own fields counted at their largest (receipts.Size), as
	// the real one stores them.
	if n, err := receipts.Size(r); err != nil || n > receipts.MaxDocumentBytes {
		return receipts.ErrTooLarge
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// A receipt with its own identity is stored once, as the real writer
	// stores it (receipts.Writer).
	if r.ID != "" {
		for _, old := range w.all {
			if old.ID == r.ID {
				return nil
			}
		}
	} else {
		w.n++
		r.ID = fmt.Sprintf("rcp_%06d", w.n)
	}
	if w.bounds == nil {
		w.bounds = map[string]time.Duration{}
	}
	w.bounds[r.ID] = bound
	_ = account
	w.all = append(w.all, *r)
	if w.lose != nil && w.lose(r) {
		return errors.New("the storage's answer was lost")
	}
	return nil
}

func (w *writer) of(kind receipts.Kind) []receipts.Receipt {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []receipts.Receipt
	for _, r := range w.all {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

func (w *writer) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.all)
}

// ---- the world ----

type world struct {
	t       *testing.T
	mem     *store.Memory
	commits *store.MemoryCommits
	keys    *keys.Local
	clock   *clock
	vendors *resolver
	accts   *accounts
	mbx     *mailboxes
	disc    *discloser
	gate    *gate
	rcpt    *writer
	notice  string
	jwtKey  *rsa.PrivateKey
	ecKey   *ecdsa.PrivateKey
}

func newWorld(t *testing.T) *world {
	t.Helper()
	k, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	w := &world{
		t: t, mem: store.NewMemory(), commits: store.NewMemoryCommits(), keys: k, clock: &clock{t: start},
		accts: &accounts{st: map[string]Standing{accountA: StandingOpen, accountB: StandingOpen}},
		mbx:   &mailboxes{catalogue: map[string][]string{accountA: {mailboxID(1), mailboxID(2)}, accountB: {mailboxID(3)}}, msgs: map[string][]mail{}, gone: map[string]bool{}},
		gate:  &gate{o: inbox.AskAnswered}, rcpt: &writer{}, notice: notice,
	}
	w.mem.Now = w.clock.now
	w.disc = &discloser{m: w.mbx}
	w.jwtKey, _ = rsa.GenerateKey(rand.Reader, 2048)
	w.ecKey, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	w.vendors = &resolver{clients: map[string]*vendors.Client{
		claudeCode: {ID: claudeCode, Domain: "claude.ai", Document: oauth.ClientDocument{ClientID: claudeCode, ClientName: "Claude Code", RedirectURIs: []string{loopback, "http://127.0.0.1/callback"}, TokenEndpointAuthMethod: "none"}},
		otherApp:   {ID: otherApp, Domain: "tools.example.org", Document: oauth.ClientDocument{ClientID: otherApp, ClientName: "Tools", RedirectURIs: []string{"https://tools.example.org/back"}}},
		chatgpt: {ID: chatgpt, Domain: "chatgpt.com", Document: oauth.ClientDocument{ClientID: chatgpt, ClientName: "ChatGPT", RedirectURIs: []string{"https://chatgpt.com/connector/oauth/back"},
			TokenEndpointAuthMethod: oauth.AuthPrivateKeyJWT, JWKSURI: "https://chatgpt.com/oauth/jwks.json"}},
	}, keys: map[string][]vendors.Key{chatgpt: {{ID: "k1", Algorithm: "RS256", Public: &w.jwtKey.PublicKey}, {ID: "e1", Algorithm: "ES256", Public: &w.ecKey.PublicKey}}}}
	for i := 1; i <= 6; i++ {
		w.mbx.add(mail{ID: fmt.Sprintf("m%03d", i), Mailbox: mailboxID(1), Received: start.Add(-time.Duration(10-i) * 24 * time.Hour),
			From: fmt.Sprintf("sender%d@example.com", i), Subject: fmt.Sprintf("subject %d", i), Body: "hello",
			Attachments: []inbox.Attachment{{Name: "a.pdf", Type: "application/pdf", Data: []byte("pdf")}}})
	}
	w.mbx.add(mail{ID: "m100", Mailbox: mailboxID(2), Received: start.Add(-48 * time.Hour), From: "x@example.com", Subject: "in two", Body: "two"})
	return w
}

// enclave is one enclave of the deployment: its own memory and Anchored.
func (w *world) enclave() *Service {
	w.t.Helper()
	a := &store.Anchored{Records: w.mem, Commits: w.commits, Strict: true, Now: w.clock.now}
	s, err := New(w.config(a))
	if err != nil {
		w.t.Fatal(err)
	}
	return s
}

func (w *world) config(r Records) Config {
	return Config{
		OAuth: oauthConfig, Records: r, Entries: w.mem, Keys: w.keys, Vendors: w.vendors, Accounts: w.accts, Mailboxes: w.mbx,
		Discloser: w.disc, Model: w.gate, ModelNotice: w.notice, Rules: []rules.Rule{rules.Default()}, Receipts: w.rcpt,
		Now: w.clock.now,
	}
}

// person is someone signed in to account, with a fresh proof.
func (w *world) person(account string) Person {
	return Person{Account: account, Session: "ses_" + account, ConfirmedUntil: w.clock.now().Add(10 * time.Minute)}
}

// pkce is a verifier and its S256 challenge.
func pkce(t *testing.T) (string, string) {
	t.Helper()
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	v := b64.EncodeToString(b)
	sum := sha256.Sum256([]byte(v))
	return v, b64.EncodeToString(sum[:])
}

// ask is a vendor's authorization request.
type ask struct {
	client, redirect, resource, scope, state string
	verifier, challenge                      string
	extra                                    url.Values
}

func (w *world) ask(client string) *ask {
	v, c := pkce(w.t)
	redirect := loopback
	switch client {
	case otherApp:
		redirect = "https://tools.example.org/back"
	case chatgpt:
		redirect = "https://chatgpt.com/connector/oauth/back"
	}
	return &ask{client: client, redirect: redirect, resource: mcpResource, state: "st-1", verifier: v, challenge: c}
}

func (a *ask) query() string {
	q := url.Values{"client_id": {a.client}, "redirect_uri": {a.redirect}, "response_type": {"code"}, "code_challenge": {a.challenge},
		"code_challenge_method": {"S256"}, "resource": {a.resource}, "state": {a.state}}
	if a.scope != "" {
		q.Set("scope", a.scope)
	}
	for k, v := range a.extra {
		q[k] = v
	}
	return q.Encode()
}

// start starts a request and answers what the screen shows.
func (w *world) start(s *Service, p Person, a *ask) *contracts.AuthorizationRequest {
	w.t.Helper()
	got, err := s.Start(context.Background(), p, a.query())
	if err != nil {
		w.t.Fatalf("start: %v", err)
	}
	if got.Request == nil {
		w.t.Fatalf("start redirected: %s", got.Redirect)
	}
	return got.Request
}

// allow decides a request as shown, with its starting draft, and answers
// the code.
func (w *world) allow(s *Service, p Person, req *contracts.AuthorizationRequest) string {
	w.t.Helper()
	return w.allowDraft(s, p, req, req.Draft, req.Snapshot)
}

func (w *world) allowDraft(s *Service, p Person, req *contracts.AuthorizationRequest, d contracts.ConsentDraft, snap contracts.ConsentSnapshot) string {
	w.t.Helper()
	ans, err := s.Decide(context.Background(), p, req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: req.ModelNotice, Snapshot: &snap})
	if err != nil {
		w.t.Fatalf("allow: %v", err)
	}
	return codeOf(w.t, ans.Redirect)
}

func codeOf(t *testing.T, redirect string) string {
	t.Helper()
	u, err := url.Parse(redirect)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("iss") != issuer {
		t.Fatalf("no iss in %q", redirect)
	}
	return u.Query().Get("code")
}

// exchange exchanges a code.
func (w *world) exchange(s *Service, a *ask, code string) (*TokenAnswer, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {a.verifier}, "redirect_uri": {a.redirect},
		"client_id": {a.client}, "resource": {a.resource}}
	w.sign(form, a.client, oauth.PathToken)
	return s.Token(context.Background(), form)
}

func (w *world) refresh(s *Service, a *ask, token string, scope ...string) (*TokenAnswer, error) {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}, "client_id": {a.client}, "resource": {a.resource}}
	if len(scope) > 0 {
		form.Set("scope", strings.Join(scope, " "))
	}
	w.sign(form, a.client, oauth.PathToken)
	return s.Token(context.Background(), form)
}

// sign adds a fresh assertion for a private_key_jwt client.
func (w *world) sign(form url.Values, client, path string) {
	if client != chatgpt {
		return
	}
	form.Set("client_assertion_type", oauth.AssertionType)
	form.Set("client_assertion", w.assertion(client, issuer+path, newJTI(), w.clock.now().Add(5*time.Minute)))
}

var jtis struct {
	sync.Mutex
	n int
}

func newJTI() string {
	jtis.Lock()
	defer jtis.Unlock()
	jtis.n++
	return fmt.Sprintf("jti-%d", jtis.n)
}

func (w *world) assertion(client, aud, jti string, exp time.Time) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	c, _ := json.Marshal(map[string]any{"iss": client, "sub": client, "aud": aud, "exp": exp.Unix(), "iat": w.clock.now().Unix(), "jti": jti})
	signing := b64.EncodeToString(h) + "." + b64.EncodeToString(c)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, w.jwtKey, crypto.SHA256, sum[:])
	if err != nil {
		w.t.Fatal(err)
	}
	return signing + "." + b64.EncodeToString(sig)
}

// connect makes a connection for a's client through s, exchanges its code
// and answers the tokens and the screen it was approved on.
func (w *world) connect(s *Service, p Person, a *ask) (*TokenAnswer, *contracts.AuthorizationRequest) {
	w.t.Helper()
	req := w.start(s, p, a)
	code := w.allow(s, p, req)
	tok, err := w.exchange(s, a, code)
	if err != nil {
		w.t.Fatalf("exchange: %v", err)
	}
	return tok, req
}

func (w *world) check(s *Service, a *ask, token string) (*oauth.Grant, error) {
	r := contracts.ResourceMCP
	if a.resource == apiResource {
		r = contracts.ResourceAPI
	}
	return s.Check(context.Background(), r, token)
}

// grant is the grant of a token that must be valid.
func (w *world) grant(s *Service, a *ask, token string) *oauth.Grant {
	w.t.Helper()
	g, err := w.check(s, a, token)
	if err != nil {
		w.t.Fatalf("check: %v", err)
	}
	return g
}

func oauthCode(err error) oauth.ErrorCode {
	var oe *OAuthError
	if errors.As(err, &oe) {
		return oe.Code
	}
	return ""
}

// connectionOf is the account's connection for client, from the list.
func (w *world) connectionOf(s *Service, account, client string) *connDoc {
	w.t.Helper()
	l, _, err := s.readList(context.Background(), account)
	if err != nil || l == nil {
		w.t.Fatalf("list: %v", err)
	}
	d, _, err := s.readConn(context.Background(), l.Current[client])
	if err != nil || d == nil {
		w.t.Fatalf("connection: %v", err)
	}
	return d
}

// clientProblem is the reason of a contracts.ClientError, "" for none.
func clientProblem(err error) contracts.ClientProblem {
	var ce *contracts.ClientError
	if errors.As(err, &ce) {
		return ce.Problem
	}
	return ""
}
