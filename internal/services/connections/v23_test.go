package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/store"
)

// preview is the snapshot a draft's preview answers.
func (w *world) preview(s *Service, p Person, req *contracts.AuthorizationRequest, d contracts.ConsentDraft) contracts.ConsentPreviewPage {
	w.t.Helper()
	page, err := s.Preview(context.Background(), p, req.ID, contracts.ConsentPreview{Draft: d, Mailbox: d.Mailboxes[0]})
	if err != nil {
		w.t.Fatalf("preview: %v", err)
	}
	return *page
}

// pushed starts a pushed request of otherApp with a query.
func (w *world) pushed(s *Service, p Person, a *ask, q string, extra url.Values) *contracts.AuthorizationRequest {
	w.t.Helper()
	form := url.Values{"client_id": {a.client}, "redirect_uri": {a.redirect}, "response_type": {"code"}, "code_challenge": {a.challenge},
		"code_challenge_method": {"S256"}, "resource": {a.resource}, "state": {a.state}}
	if q != "" {
		form.Set("query", q)
	}
	if a.scope != "" {
		form.Set("scope", a.scope)
	}
	for k, v := range extra {
		form[k] = v
	}
	w.sign(form, a.client, oauth.PathPushed)
	uri, err := s.Push(context.Background(), form)
	if err != nil {
		w.t.Fatalf("push: %v", err)
	}
	got, err := s.Start(context.Background(), p, url.Values{"client_id": {a.client}, "request_uri": {uri}}.Encode())
	if err != nil || got.Request == nil {
		w.t.Fatalf("start: %v %v", got, err)
	}
	return got.Request
}

const fromExample = `{"v":1,"all":[{"any":[{"var":"from_domain","op":"is","value":"example.com"}]}]}`

// V23. Asking again (the owner's decisions 7, 10 and 13; N5, N6).
func TestV23AskingAgain(t *testing.T) {
	ctx := context.Background()
	all := []string{mailboxID(1), mailboxID(2)}

	type transition struct {
		name   string
		client string
		query  string // "" for none
		draft  func(req *contracts.AuthorizationRequest) contracts.ConsentDraft
		ask    func(a *ask)
		want   contracts.ConnectionChange
		first  string // the connection's query
	}
	same := func(req *contracts.AuthorizationRequest) contracts.ConsentDraft { return req.Draft }
	transitions := []transition{
		{name: "all mailboxes again", client: claudeCode, draft: same, want: contracts.ChangeAddDevice},
		{name: "some mailboxes", client: claudeCode, draft: func(*contracts.AuthorizationRequest) contracts.ConsentDraft {
			return contracts.ConsentDraft{Mailboxes: all[:1], Since: contracts.MailSince{New: true}}
		}, want: contracts.ChangeAddDevice},
		{name: "mail from before", client: claudeCode, draft: func(*contracts.AuthorizationRequest) contracts.ConsentDraft {
			return contracts.ConsentDraft{Mailboxes: all, Since: contracts.MailSince{At: start.Add(-30 * 24 * time.Hour)}}
		}, want: contracts.ChangeReplace},
		{name: "all mail kept", client: claudeCode, draft: func(*contracts.AuthorizationRequest) contracts.ConsentDraft {
			return contracts.ConsentDraft{Mailboxes: all}
		}, want: contracts.ChangeReplace},
		{name: "a part more", client: claudeCode, draft: same, ask: func(a *ask) { a.scope = "mail.sender mail.subject mail.body mail.attachments" }, want: contracts.ChangeReplace},
		{name: "fewer parts", client: claudeCode, draft: same, ask: func(a *ask) { a.scope = "mail.sender" }, want: contracts.ChangeAddDevice},
		{name: "another resource", client: claudeCode, draft: same, ask: func(a *ask) { a.resource = apiResource }, want: contracts.ChangeReplace},
		{name: "the same query", client: otherApp, first: fromExample, query: fromExample, draft: same, want: contracts.ChangeAddDevice},
		{name: "a narrower query", client: otherApp, first: fromExample,
			query: `{"v":1,"all":[{"any":[{"var":"from_domain","op":"is","value":"example.com"}]},{"any":[{"var":"subject","op":"contains","value":"subject"}]}]}`,
			draft: same, want: contracts.ChangeAddDevice},
		{name: "a wider query", client: otherApp, first: fromExample, query: `{"v":1,"all":[{"any":[{"var":"from_domain","op":"is","value":"example.org"},{"var":"from_domain","op":"is","value":"example.com"}]}]}`,
			draft: same, want: contracts.ChangeReplace},
		{name: "no query after one", client: otherApp, first: fromExample, draft: same, want: contracts.ChangeReplace},
	}
	for _, tr := range transitions {
		t.Run(tr.name, func(t *testing.T) {
			w := newWorld(t)
			a, b := w.enclave(), w.enclave()
			p := w.person(accountA)
			first := w.ask(tr.client)
			var oldTok *TokenAnswer
			if tr.client == otherApp {
				req := w.pushed(a, p, first, tr.first, nil)
				code := w.allow(a, p, req)
				var err error
				if oldTok, err = w.exchange(a, first, code); err != nil {
					t.Fatal(err)
				}
			} else {
				oldTok, _ = w.connect(a, p, first)
			}
			// B reads the old connection, so its cache holds it.
			w.grant(b, first, oldTok.AccessToken)
			again := w.ask(tr.client)
			if tr.ask != nil {
				tr.ask(again)
			}
			var req *contracts.AuthorizationRequest
			if tr.client == otherApp {
				req = w.pushed(a, p, again, tr.query, nil)
			} else {
				req = w.start(a, p, again)
				// Without a query, the screen starts from what was approved: new
				// mail only from when it was (finding 11 of the review of pull
				// request 136).
				if !slices.Equal(req.Draft.Mailboxes, all) || req.Draft.Since.New || !req.Draft.Since.At.Equal(start) {
					t.Fatalf("%+v", req.Draft)
				}
			}
			d := tr.draft(req)
			page := w.preview(a, p, req, d)
			if page.Change != tr.want || page.Snapshot.Change != tr.want {
				t.Fatalf("shown %s, want %s", page.Change, tr.want)
			}
			code := w.allowDraft(a, p, req, d, page.Snapshot)
			tok, err := w.exchange(a, again, code)
			if err != nil {
				t.Fatal(err)
			}
			w.grant(b, again, tok.AccessToken)
			switch tr.want {
			case contracts.ChangeAddDevice:
				for _, s := range []*Service{a, b} {
					w.grant(s, first, oldTok.AccessToken)
				}
				if _, err := w.refresh(b, first, oldTok.RefreshToken); err != nil {
					t.Fatal(err)
				}
				c := w.connectionOf(a, accountA, tr.client)
				if c.activeDevices() != 2 {
					t.Fatalf("%d devices", c.activeDevices())
				}
			case contracts.ChangeReplace:
				if _, err := w.check(a, first, oldTok.AccessToken); !errors.Is(err, ErrInvalidToken) {
					t.Fatalf("the enclave that approved: %v", err)
				}
				if _, err := w.check(b, first, oldTok.AccessToken); err != nil {
					t.Fatalf("the other, within its five seconds: %v", err)
				}
				w.clock.add(oauth.ConnectionFresh)
				if _, err := w.check(b, first, oldTok.AccessToken); !errors.Is(err, ErrInvalidToken) {
					t.Fatalf("the other, after: %v", err)
				}
				if _, err := w.refresh(b, first, oldTok.RefreshToken); oauthCode(err) != oauth.ErrInvalidGrant {
					t.Fatal(err)
				}
			}
		})
	}

	t.Run("denied, the old one stays", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		p := w.person(accountA)
		tok, _ := w.connect(s, p, w.ask(claudeCode))
		again := w.ask(claudeCode)
		again.scope = "mail.sender mail.subject mail.body mail.attachments"
		req := w.start(s, p, again)
		if req.Change != contracts.ChangeReplace || req.Connection == nil {
			t.Fatalf("%+v", req)
		}
		if _, err := s.Decide(ctx, p, req.ID, contracts.AuthorizationDecision{}); err != nil {
			t.Fatal(err)
		}
		w.grant(s, w.ask(claudeCode), tok.AccessToken)
	})

	t.Run("without a query, the starting draft is the old connection's", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		p := w.person(accountA)
		first := w.ask(claudeCode)
		req := w.start(s, p, first)
		d := contracts.ConsentDraft{Mailboxes: all[1:], Since: contracts.MailSince{At: start.Add(-24 * time.Hour)}}
		page := w.preview(s, p, req, d)
		w.allowDraft(s, p, req, d, page.Snapshot)
		req = w.start(s, p, w.ask(claudeCode))
		if !slices.Equal(req.Draft.Mailboxes, d.Mailboxes) || !req.Draft.Since.At.Equal(d.Since.At) || req.Change != contracts.ChangeAddDevice {
			t.Fatalf("%+v", req)
		}
	})

	t.Run("N5: a stale screen commits nothing", func(t *testing.T) {
		for _, meanwhile := range []string{"replaced", "device added", "device revoked", "renewed"} {
			t.Run(meanwhile, func(t *testing.T) {
				w := newWorld(t)
				a, b := w.enclave(), w.enclave()
				p := w.person(accountA)
				first := w.ask(claudeCode)
				w.connect(a, p, first)
				req := w.start(a, p, w.ask(claudeCode))
				page := w.preview(a, p, req, req.Draft)
				if page.Change != contracts.ChangeAddDevice {
					t.Fatal(page.Change)
				}
				// Through B, meanwhile.
				p2 := p
				p2.Session = "ses_b"
				c := w.connectionOf(b, accountA, claudeCode)
				switch meanwhile {
				case "replaced":
					x := w.ask(claudeCode)
					x.resource = apiResource
					w.allow(b, p2, w.start(b, p2, x))
				case "device added":
					w.allow(b, p2, w.start(b, p2, w.ask(claudeCode)))
				case "device revoked":
					if _, err := b.RevokeDevice(ctx, p2, c.ID, c.Devices[0].ID); err != nil {
						t.Fatal(err)
					}
				case "renewed":
					v, _ := b.Get(ctx, p2, c.ID)
					if _, err := b.Renew(ctx, p2, c.ID, contracts.ConnectionRenewal{ModelNotice: notice, Version: v.Version}); err != nil {
						t.Fatal(err)
					}
				}
				before := w.connectionOf(a, accountA, claudeCode)
				_, beforeAt, _ := a.readConn(ctx, before.ID)
				d := req.Draft
				_, err := a.Decide(ctx, p, req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: notice, Snapshot: &page.Snapshot})
				if !errors.Is(err, contracts.ErrConsentChanged) {
					t.Fatalf("%v", err)
				}
				after := w.connectionOf(a, accountA, claudeCode)
				_, afterAt, _ := a.readConn(ctx, after.ID)
				if after.ID != before.ID || afterAt.Version() != beforeAt.Version() || len(after.Devices) != len(before.Devices) || !after.EndsAt.Equal(before.EndsAt) {
					t.Fatal("committed on a stale screen")
				}
			})
		}
		t.Run("a renewal with a stale version", func(t *testing.T) {
			w := newWorld(t)
			s := w.enclave()
			p := w.person(accountA)
			w.connect(s, p, w.ask(claudeCode))
			c := w.connectionOf(s, accountA, claudeCode)
			v, _ := s.Get(ctx, p, c.ID)
			w.allow(s, p, w.start(s, p, w.ask(claudeCode))) // a device added
			if _, err := s.Renew(ctx, p, c.ID, contracts.ConnectionRenewal{ModelNotice: notice, Version: v.Version}); !errors.Is(err, contracts.ErrConsentChanged) {
				t.Fatal(err)
			}
		})
	})
}

// sizedTable is the records' table of a store that refuses an item past
// DynamoDB's 400 KiB, as the real one does.
type sizedTable struct{ *store.Memory }

const itemMax = 400 << 10

var errItemTooLarge = errors.New("the item is larger than 400 KiB")

func (s sizedTable) PutRecord(ctx context.Context, r *store.Record, create bool) error {
	if len(r.Name)+len(r.Value)+len(r.WrappedKey) > itemMax {
		return errItemTooLarge
	}
	return s.Memory.PutRecord(ctx, r, create)
}

// longQuery is a formula with 64 subject conditions of 100 characters, as
// the review's narrower scopes were: one of 39 KB once sealed in an entry.
func longQuery(n int) string {
	var groups []string
	for g := range 2 {
		var conds []string
		for c := range 32 {
			v := fmt.Sprintf("%02d%02d%02d", n, g, c) + strings.Repeat("w", 94)
			conds = append(conds, fmt.Sprintf(`{"var":"subject","op":"contains","value":%q}`, v))
		}
		groups = append(groups, `{"any":[`+strings.Join(conds, ",")+`]}`)
	}
	return `{"v":1,"all":[` + strings.Join(groups, ",") + `]}`
}

// V23, N6 and the owner's decision 13: sixteen narrower devices fit, each
// reading its own scope; the 17th is refused; a device never exchanged frees
// its slot; the 17th ended device drops the oldest ended one.
func TestV23N6SixteenNarrowerDevices(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	sized := func() *Service {
		s, err := New(w.config(&store.Anchored{Records: sizedTable{w.mem}, Commits: w.commits, Strict: true, Now: w.clock.now}))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	a, b := sized(), sized()
	p := w.person(accountA)
	first := w.ask(otherApp)
	w.allow(a, p, w.pushed(a, p, first, "", nil))
	type device struct {
		ask   *ask
		tok   *TokenAnswer
		query string
	}
	var devices []device
	for n := 1; n < oauth.FamiliesMax; n++ { // the first device is the connection's own
		x := w.ask(otherApp)
		q := longQuery(n)
		req := w.pushed(a, p, x, q, nil)
		if req.Change != contracts.ChangeAddDevice {
			t.Fatalf("device %d: %s", n, req.Change)
		}
		tok, err := w.exchange(b, x, w.allow(a, p, req))
		if err != nil {
			t.Fatal(n, err)
		}
		devices = append(devices, device{x, tok, q})
	}
	c := w.connectionOf(a, accountA, otherApp)
	raw, _ := json.Marshal(c)
	if c.activeDevices() != oauth.FamiliesMax || len(raw) > ConnectionRecordBytesMax/4 {
		t.Fatalf("%d devices, %d bytes", c.activeDevices(), len(raw))
	}
	// Each token reads its own scope, beside the connection's.
	for i, d := range devices {
		g := w.grant(b, d.ask, d.tok.AccessToken)
		if _, rel, err := b.Search(ctx, g, contracts.VendorSearch{}); err != nil {
			t.Fatal(i, err)
		} else {
			rel.Finish(false)
		}
		ds := w.disc.last()
		own := ds.Formulas[len(ds.Formulas)-1]
		if !strings.Contains(string(mustJSON(own.Formula)), fmt.Sprintf("%02d0000", i+1)) {
			t.Fatalf("device %d read another scope", i+1)
		}
	}
	// The 17th: refused with its words, and nothing ended.
	req := w.pushed(a, p, w.ask(otherApp), longQuery(40), nil)
	d := req.Draft
	if _, err := a.Decide(ctx, p, req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: notice, Snapshot: &req.Snapshot}); !errors.Is(err, contracts.ErrTooManyDevices) {
		t.Fatal(err)
	}
	if c := w.connectionOf(a, accountA, otherApp); c.activeDevices() != oauth.FamiliesMax {
		t.Fatal("a device ended to make room")
	}

	// A token entry whose formulas are not the ones its ScopeID names.
	var e accessEntry
	if err := a.getEntry(ctx, PartitionAccess+hashOf(oauth.KindAccess, devices[0].tok.AccessToken), &e); err != nil {
		t.Fatal(err)
	}
	var other accessEntry
	_ = a.getEntry(ctx, PartitionAccess+hashOf(oauth.KindAccess, devices[1].tok.AccessToken), &other)
	e.Scope = other.Scope
	forged := oauth.PrefixAccess + strings.Repeat("f", 43)
	if err := a.putEntry(ctx, PartitionAccess+hashOf(oauth.KindAccess, forged), e, e.Expires); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Check(ctx, contracts.ResourceMCP, forged); err == nil {
		t.Fatal("another scope's formulas under this one's identifier")
	}
	e.ScopeID = ""
	forged2 := oauth.PrefixAccess + strings.Repeat("g", 43)
	_ = a.putEntry(ctx, PartitionAccess+hashOf(oauth.KindAccess, forged2), e, e.Expires)
	if _, err := a.Check(ctx, contracts.ResourceMCP, forged2); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}

	// The 17th ended device drops the oldest ended one, with its family's
	// records; an active one is never dropped.
	c = w.connectionOf(a, accountA, otherApp)
	var ended []string
	for _, dev := range c.Devices[:oauth.FamiliesMax] {
		w.clock.add(time.Second)
		if _, err := a.RevokeDevice(ctx, p, c.ID, dev.ID); err != nil {
			t.Fatal(err)
		}
		ended = append(ended, dev.ID)
	}
	for range 2 {
		x := w.ask(otherApp)
		req := w.pushed(a, p, x, "", nil)
		if _, err := w.exchange(a, x, w.allow(a, p, req)); err != nil {
			t.Fatal(err)
		}
	}
	c = w.connectionOf(a, accountA, otherApp)
	w.clock.add(time.Second)
	if _, err := a.RevokeDevice(ctx, p, c.ID, c.Devices[len(c.Devices)-1].ID); err != nil {
		t.Fatal(err)
	}
	c = w.connectionOf(a, accountA, otherApp)
	if c.device(ended[0]) != nil || c.device(ended[1]) == nil || c.activeDevices() != 1 {
		t.Fatalf("%d devices, the oldest kept: %v", len(c.Devices), c.device(ended[0]) != nil)
	}
	if f, _, err := a.readFamily(ctx, ended[0], 0); f != nil || err != nil {
		t.Fatal("its family's records stayed", err)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// V23: a device whose code was never exchanged frees its slot after its
// code's 60 seconds.
func TestV23ADeviceNeverExchangedFreesItsSlot(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	ctx := context.Background()
	p := w.person(accountA)
	w.connect(s, p, w.ask(claudeCode))
	for range oauth.FamiliesMax - 1 {
		w.allow(s, p, w.start(s, p, w.ask(claudeCode)))
	}
	req := w.start(s, p, w.ask(claudeCode))
	d := req.Draft
	in := contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: notice, Snapshot: &req.Snapshot}
	if _, err := s.Decide(ctx, p, req.ID, in); !errors.Is(err, contracts.ErrTooManyDevices) {
		t.Fatal(err)
	}
	w.clock.add(oauth.CodeTTL)
	p = w.person(accountA)
	req = w.start(s, p, w.ask(claudeCode))
	w.allow(s, p, req)
	c := w.connectionOf(s, accountA, claudeCode)
	if c.activeDevices() != 2 {
		t.Fatalf("%d active", c.activeDevices())
	}
}

// V23: a request whose record could not hold every device it may have is
// request_too_large at its start, before the person sees a screen.
func TestV23ARequestTooLargeAtItsStart(t *testing.T) {
	w := newWorld(t)
	var many []string
	for i := range 10000 {
		many = append(many, "mbx_"+fmt.Sprintf("%026d", i))
	}
	w.mbx.catalogue[accountA] = many
	s := w.enclave()
	if _, err := s.Start(context.Background(), w.person(accountA), w.ask(claudeCode).query()); !errors.Is(err, contracts.ErrRequestTooLarge) {
		t.Fatal(err)
	}
	if s.Refused()["request_too_large"] != 1 {
		t.Fatal(s.Refused())
	}
}
