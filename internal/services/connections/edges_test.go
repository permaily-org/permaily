package connections

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/vendors"
)

// The service's edges: what it is given, the stand-ins of pull request 1,
// and the storage failing at each step.

type badKeys struct{}

func (badKeys) Derive(context.Context, string) ([]byte, error) { return nil, errors.New("no root") }

func TestNewNeedsWhatItIsGiven(t *testing.T) {
	w := newWorld(t)
	if _, err := New(Config{}); !errors.Is(err, errMissing) {
		t.Fatal(err)
	}
	c := w.config(&store.Anchored{Records: w.mem, Commits: w.commits, Strict: true})
	c.Keys = badKeys{}
	if _, err := New(c); err == nil {
		t.Fatal("made without its keys")
	}
	c = w.config(&store.Anchored{Records: w.mem, Commits: w.commits, Strict: true})
	c.Now, c.Random = nil, nil
	s, err := New(c)
	if err != nil || s.now().IsZero() {
		t.Fatal(err)
	}
	if id, err := s.newID("con"); err != nil || len(id) != len("con_")+26 {
		t.Fatal(id, err)
	}
	s.cfg.Random = func([]byte) error { return errors.New("no randomness") }
	if _, err := s.newID("con"); err == nil {
		t.Fatal("an identifier without randomness")
	}
	if _, err := newSecret("pmc_", s.cfg.Random); err == nil {
		t.Fatal("a secret without randomness")
	}
}

func TestTheStandIns(t *testing.T) {
	t.Run("redirects", func(t *testing.T) {
		reg := []string{"http://localhost/callback", "http://127.0.0.1:8080/cb", "https://app.example/back"}
		for got, want := range map[string]bool{
			"http://localhost:53682/callback": true, "http://localhost/callback": true, "http://127.0.0.1/cb": true,
			"http://127.0.0.1:1/cb": true, "http://127.0.0.1/other": false, "http://localhost/callback#x": false,
			"https://localhost/callback": false, "https://app.example/back": true, "https://app.example/back/": false,
			"http://u@localhost/callback": false, "%zz": false, "http://[::1]/callback": false,
		} {
			if redirectAllowed(reg, got) != want {
				t.Error(got)
			}
		}
		if h, lo := redirectHost("http://[::1]:9/cb"); h != "[::1]" || !lo {
			t.Error(h, lo)
		}
		if h, lo := redirectHost("https://claude.ai/x"); h != "claude.ai" || lo {
			t.Error(h, lo)
		}
		if h, _ := redirectHost("%zz"); h != "" {
			t.Error(h)
		}
		if redirectWith("%zz", nil, issuer) != "" {
			t.Error("a redirect of nothing")
		}
	})
	t.Run("PKCE and scopes", func(t *testing.T) {
		v, c := pkce(t)
		if !verifyPKCE(c, v) || verifyPKCE(c, v+"x") || verifyPKCE(c, "short") || verifyPKCE("bad", v) {
			t.Error("pkce")
		}
		for s, ok := range map[string]bool{"": false, "mail.sender": true, "mail.body mail.body": false, "mail.x": false, "mail.sender mail.sender": false,
			"mail.sender\tmail.subject": false, " mail.subject": false, "mail.subject  mail.body": false, "mail.subject mail.body": true} {
			if _, err := parseScopes(s, true); (err == nil) != ok {
				t.Error(s)
			}
		}
		if got, _ := parseScopes("", false); strings.Join(got, " ") != "mail.sender mail.subject mail.body" {
			t.Error(got)
		}
	})
	t.Run("pushed forms", func(t *testing.T) {
		doc := &oauth.ClientDocument{ClientID: otherApp, RedirectURIs: []string{"https://tools.example.org/back"}}
		_, c := pkce(t)
		base := func() url.Values {
			return url.Values{"client_id": {otherApp}, "redirect_uri": {"https://tools.example.org/back"}, "response_type": {"code"}, "code_challenge": {c},
				"code_challenge_method": {"S256"}, "resource": {apiResource}}
		}
		for name, change := range map[string]func(url.Values){
			"no redirect":    func(v url.Values) { v.Del("redirect_uri") },
			"two queries":    func(v url.Values) { v["query"] = []string{"{}", "{}"} },
			"not JSON":       func(v url.Values) { v.Set("query", "{") },
			"a future since": func(v url.Values) { v.Set("mail_since", "yesterday") },
			"0 days":         func(v url.Values) { v.Set("ends_in_days", "0") },
			"366 days":       func(v url.Values) { v.Set("ends_in_days", "366") },
			"no response":    func(v url.Values) { v.Del("response_type") },
		} {
			v := base()
			change(v)
			if _, err := pushedParams(v, doc, oauthConfig); err == nil {
				t.Error(name)
			}
		}
		v := base()
		v.Set("mail_since", "new")
		if p, err := pushedParams(v, doc, oauthConfig); err != nil || p.Since != "new" {
			t.Error(p, err)
		}
	})
	t.Run("assertions", func(t *testing.T) {
		w := newWorld(t)
		doc := &oauth.ClientDocument{ClientID: chatgpt, TokenEndpointAuthMethod: oauth.AuthPrivateKeyJWT}
		keys := w.vendors.keys[chatgpt]
		now := w.clock.now()
		sign := func(header, claims map[string]any) string {
			h, _ := json.Marshal(header)
			c, _ := json.Marshal(claims)
			in := b64.EncodeToString(h) + "." + b64.EncodeToString(c)
			sum := sha256.Sum256([]byte(in))
			if header["alg"] == "ES256" {
				r, s, _ := ecdsa.Sign(rand.Reader, w.ecKey, sum[:])
				sig := make([]byte, 64)
				r.FillBytes(sig[:32])
				s.FillBytes(sig[32:])
				return in + "." + b64.EncodeToString(sig)
			}
			return w.assertion(chatgpt, issuer, "x", now.Add(time.Minute))[:0] + in + "." + b64.EncodeToString(rsaSign(t, w, sum[:]))
		}
		claims := func() map[string]any {
			return map[string]any{"iss": chatgpt, "sub": chatgpt, "aud": []string{"x", issuer}, "exp": now.Add(time.Minute).Unix(), "jti": "j"}
		}
		if jti, _, err := verifyAssertion(doc, keys, sign(map[string]any{"alg": "ES256", "kid": "e1"}, claims()), []string{issuer}, now); err != nil || jti != "j" {
			t.Fatal(jti, err)
		}
		bad := map[string]string{}
		c := claims()
		c["iat"] = now.Add(2 * time.Minute).Unix()
		bad["iat ahead"] = sign(map[string]any{"alg": "RS256", "kid": "k1"}, c)
		c = claims()
		c["jti"] = strings.Repeat("j", 257)
		bad["a long jti"] = sign(map[string]any{"alg": "RS256", "kid": "k1"}, c)
		c = claims()
		c["aud"] = 7
		bad["aud a number"] = sign(map[string]any{"alg": "RS256", "kid": "k1"}, c)
		c = claims()
		delete(c, "exp")
		bad["no exp"] = sign(map[string]any{"alg": "RS256", "kid": "k1"}, c)
		bad["HS256"] = sign(map[string]any{"alg": "HS256", "kid": "k1"}, claims())
		bad["RS256 on the EC key"] = sign(map[string]any{"alg": "RS256", "kid": "e1"}, claims())
		bad["a bad signature"] = sign(map[string]any{"alg": "RS256", "kid": "k1"}, claims())[:20] + "x" + sign(map[string]any{"alg": "RS256", "kid": "k1"}, claims())[21:]
		bad["duplicate members"] = b64.EncodeToString([]byte(`{"alg":"RS256","alg":"RS256","kid":"k1"}`)) + ".e30.e30"
		bad["not JSON"] = b64.EncodeToString([]byte(`[1]`)) + ".e30.e30"
		for name, j := range bad {
			if _, _, err := verifyAssertion(doc, keys, j, []string{issuer}, now); err == nil {
				t.Error(name)
			}
		}
		withAlg := *doc
		withAlg.TokenEndpointAuthAlg = "ES256"
		if _, _, err := verifyAssertion(&withAlg, keys, sign(map[string]any{"alg": "RS256", "kid": "k1"}, claims()), []string{issuer}, now); err == nil {
			t.Error("an alg other than the document's")
		}
		weird := []vendors.Key{{ID: "k1", Algorithm: "RS256", Public: "not a key"}}
		if _, _, err := verifyAssertion(doc, weird, sign(map[string]any{"alg": "RS256", "kid": "k1"}, claims()), []string{issuer}, now); err == nil {
			t.Error("an unknown key type")
		}
		if _, ok := jsonObject([]byte(`not json`)); ok {
			t.Error("not JSON read")
		}
		if _, ok := jsonObject([]byte(`{"a":1,"b":{"a":2}}`)); !ok {
			t.Error("a nested name taken for a duplicate")
		}
		// An unknown kid: the key set fetched once more.
		if _, _, err := verifyAssertion(doc, keys, sign(map[string]any{"alg": "RS256", "kid": "k9"}, claims()), []string{issuer}, now); !errors.Is(err, errUnknownKey) {
			t.Error(err)
		}
		_ = big.NewInt
	})
}

func rsaSign(t *testing.T, w *world, digest []byte) []byte {
	t.Helper()
	sig, err := w.jwtKey.Sign(rand.Reader, digest, cryptoHash())
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

// A preview's refusals, and its page with the model.
func TestPreviewRefusals(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.mbx.add(mail{ID: "m009", Mailbox: mailboxID(1), Received: start.Add(-time.Hour), From: "codes@example.com", Subject: "code", Held: true})
	s := w.enclave()
	p := w.person(accountA)
	req := w.start(s, p, w.ask(claudeCode))
	all := contracts.ConsentDraft{Mailboxes: req.Mailboxes}
	for name, in := range map[string]contracts.ConsentPreview{
		"empty draft":     {Mailbox: mailboxID(1)},
		"another mailbox": {Draft: all, Mailbox: mailboxID(3)},
		"a draft twice":   {Draft: contracts.ConsentDraft{Mailboxes: []string{mailboxID(1), mailboxID(1)}}, Mailbox: mailboxID(1)},
		"a future since":  {Draft: contracts.ConsentDraft{Mailboxes: req.Mailboxes, Since: contracts.MailSince{At: start.Add(time.Hour)}}, Mailbox: mailboxID(1)},
		"new and a date":  {Draft: contracts.ConsentDraft{Mailboxes: req.Mailboxes, Since: contracts.MailSince{New: true, At: start.Add(-time.Hour)}}, Mailbox: mailboxID(1)},
	} {
		if _, err := s.Preview(ctx, p, req.ID, in); !errors.Is(err, ErrInvalidBody) {
			t.Error(name, err)
		}
	}
	if _, err := s.Preview(ctx, p, req.ID, contracts.ConsentPreview{Draft: all, Mailbox: mailboxID(1), Limit: 51}); !errors.Is(err, contracts.ErrInvalidLimit) {
		t.Error(err)
	}
	if _, err := s.Preview(ctx, p, req.ID, contracts.ConsentPreview{Draft: all, Mailbox: mailboxID(1), After: "m005", DraftID: "drf_old"}); !errors.Is(err, contracts.ErrInvalidCursor) {
		t.Error(err)
	}
	if _, err := s.Preview(ctx, w.person(accountB), req.ID, contracts.ConsentPreview{Draft: all, Mailbox: mailboxID(1)}); !errors.Is(err, contracts.ErrRequestEnded) {
		t.Error(err)
	}
	page, err := s.Preview(ctx, p, req.ID, contracts.ConsentPreview{Draft: all, Mailbox: mailboxID(1), AskModel: true, Limit: 3})
	if err != nil || len(page.Page.Held) != 1 || page.Page.Model != contracts.MatchModelAsked || page.Page.Next == "" || len(page.Page.Messages) != 2 {
		t.Fatalf("%+v %v", page, err)
	}
	next, err := s.Preview(ctx, p, req.ID, contracts.ConsentPreview{Draft: all, Mailbox: mailboxID(1), After: page.Page.Next, DraftID: page.DraftID})
	if err != nil || len(next.Page.Messages) == 0 || next.Page.Held != nil {
		t.Fatal(next, err)
	}
	// New mail only: empty.
	if page, _ := s.Preview(ctx, p, req.ID, contracts.ConsentPreview{Draft: req.Draft, Mailbox: mailboxID(1)}); len(page.Page.Messages) != 0 {
		t.Fatal(page.Page.Messages)
	}
	// A request with a query takes its own mailboxes and since only.
	x := w.ask(otherApp)
	qreq := w.pushed(s, p, x, fromExample, url.Values{"mail_since": {start.Add(-time.Hour).Format(time.RFC3339)}})
	for _, d := range []contracts.ConsentDraft{
		{Mailboxes: []string{mailboxID(1)}, Since: qreq.Since},
		{Mailboxes: qreq.Mailboxes, Since: contracts.MailSince{At: start.Add(-2 * time.Hour)}},
	} {
		if _, err := s.Preview(ctx, p, qreq.ID, contracts.ConsentPreview{Draft: d, Mailbox: mailboxID(1)}); !errors.Is(err, ErrInvalidBody) {
			t.Error(err)
		}
	}
	if _, err := s.Preview(ctx, p, qreq.ID, contracts.ConsentPreview{Draft: contracts.ConsentDraft{Mailboxes: qreq.Mailboxes, Since: contracts.MailSince{New: true}}, Mailbox: mailboxID(1)}); err != nil {
		t.Error(err)
	}
	// A mailbox the account no longer has.
	w.mbx.mu.Lock()
	w.mbx.gone[mailboxID(1)] = true
	w.mbx.mu.Unlock()
	if _, err := s.Preview(ctx, p, req.ID, contracts.ConsentPreview{Draft: all, Mailbox: mailboxID(1)}); !errors.Is(err, contracts.ErrNotFound) {
		t.Error(err)
	}
}

// A client whose name is empty is shown by its domain; a long one is cut.
func TestClientNames(t *testing.T) {
	if clientName(&vendors.Client{Domain: "x.example"}) != "x.example" {
		t.Error("an empty name")
	}
	if got := clientName(&vendors.Client{Document: oauth.ClientDocument{ClientName: strings.Repeat("é", 100)}}); len([]rune(got)) != oauth.ClientNameMaxRunes {
		t.Error(len([]rune(got)))
	}
}

// A narrower device read through an enclave that has not seen its scope:
// from its family's record.
func TestADevicesScopeFromItsFamily(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	p := w.person(accountA)
	w.connectAll(s, p, w.ask(claudeCode))
	y := w.ask(claudeCode)
	req := w.start(s, p, y)
	d := contracts.ConsentDraft{Mailboxes: []string{mailboxID(1)}}
	page := w.preview(s, p, req, d)
	tok, err := w.exchange(s, y, w.allowDraft(s, p, req, d, page.Snapshot))
	if err != nil {
		t.Fatal(err)
	}
	g := w.grant(s, y, tok.AccessToken)
	s.mu.Lock()
	clear(s.scopes)
	s.mu.Unlock()
	got, rel, err := s.Search(ctx, g, contracts.VendorSearch{})
	if err != nil {
		t.Fatal(err)
	}
	rel.Finish(true)
	for _, m := range got.Messages {
		if !strings.HasPrefix(m.ID, mailboxID(1)) {
			t.Fatal("read past its scope", m.ID)
		}
	}
	// Its family's record lost: nothing is read.
	s.mu.Lock()
	clear(s.scopes)
	s.mu.Unlock()
	_ = s.removeFamily(ctx, g.Family)
	if _, _, err := s.Search(ctx, g, contracts.VendorSearch{}); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
}

// A family whose epoch was written moved and whose next one a crash left
// unmade is made again by the next reader.
func TestAFamilysEpochMadeAgain(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	tok, _ := w.connect(s, w.person(accountA), x)
	fam := w.connectionOf(s, accountA, claudeCode).Devices[0].ID
	f, at, _ := s.readFamily(ctx, fam, 0)
	f.Status = oauth.FamilyMoved
	f.Generation = oauth.FamilyEpochRotations
	if err := s.writeSealed(ctx, familyName(fam, 0), f, at, false); err != nil {
		t.Fatal(err)
	}
	g, _, err := w.enclave().readFamily(ctx, fam, 0)
	if err != nil || g == nil || g.Epoch != 1 || g.Status != oauth.FamilyActive {
		t.Fatal(g, err)
	}
	// A family never made.
	if g, _, err := s.readFamily(ctx, "fam_nothing", 0); g != nil || err != nil {
		t.Fatal(g, err)
	}
	// An epoch missing whose one before was not moved.
	if g, _, err := w.enclave().readFamily(ctx, fam, 5); g != nil || err != nil {
		t.Fatal(g, err)
	}
	_ = tok
}

// The storage failing at each step of the token endpoint answers
// ErrUnavailable or a refusal, never a token.
func TestTheTokenEndpointWhenTheStorageFails(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s, f := w.hooked()
	x := w.ask(claudeCode)
	tok, _ := w.connect(s, w.person(accountA), x)
	down := errors.New("down")
	for _, step := range []string{"get " + RecordFamily, "put " + RecordFamily, "get " + RecordConnection} {
		f.set(func(op, name string) error {
			if strings.HasPrefix(op+" "+name, step) {
				return down
			}
			return nil
		})
		if got, err := w.refresh(s, x, tok.RefreshToken); got != nil || err == nil {
			t.Fatal(step, got, err)
		}
	}
	f.set(nil)
	// The access entry's storage down.
	w.mem.Interrupt = func(_ context.Context, _ store.Step, partition, _ string) error {
		if strings.HasPrefix(partition, "oauth#") {
			return down
		}
		return nil
	}
	if _, err := s.Check(ctx, contracts.ResourceMCP, tok.AccessToken); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := w.refresh(s, x, tok.RefreshToken); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if err := s.RevokeToken(ctx, url.Values{"token": {tok.AccessToken}, "client_id": {claudeCode}}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if err := s.RevokeToken(ctx, url.Values{"token": {tok.RefreshToken}, "client_id": {claudeCode}}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := w.exchange(s, x, "pmc_x"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	w.mem.Interrupt = nil
	// A refresh while the account's list is closing or the account blocked.
	w.accts.set(accountA, StandingBlocked)
	if _, err := w.refresh(w.enclave(), x, tok.RefreshToken); oauthCode(err) != oauth.ErrInvalidGrant {
		t.Fatal(err)
	}
	w.accts.set(accountA, StandingOpen)
	w.accts.mu.Lock()
	w.accts.fail = down
	w.accts.mu.Unlock()
	if _, err := w.refresh(w.enclave(), x, tok.RefreshToken); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	w.accts.mu.Lock()
	w.accts.fail = nil
	w.accts.mu.Unlock()
	// A vendor whose document cannot be fetched.
	w.vendors.mu.Lock()
	w.vendors.fail = vendors.ErrUnreachable
	w.vendors.mu.Unlock()
	if _, err := w.refresh(s, x, tok.RefreshToken); oauthCode(err) != oauth.ErrTemporarily {
		t.Fatal(err)
	}
	w.vendors.mu.Lock()
	w.vendors.fail = nil
	w.vendors.mu.Unlock()
	// Its resource, another.
	y := *x
	y.resource = apiResource
	if _, err := w.refresh(s, &y, tok.RefreshToken); oauthCode(err) != oauth.ErrInvalidGrant {
		t.Fatal(err)
	}
	// A refresh token of a device the person revoked here.
	c := w.connectionOf(s, accountA, claudeCode)
	if _, err := s.RevokeDevice(ctx, w.person(accountA), c.ID, c.Devices[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.refresh(s, x, tok.RefreshToken); oauthCode(err) != oauth.ErrInvalidGrant {
		t.Fatal(err)
	}
}

// The sweep ends devices whose codes were never exchanged.
func TestTheSweepTidiesDevices(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	p := w.person(accountA)
	w.connect(s, p, w.ask(claudeCode))
	w.allow(s, p, w.start(s, p, w.ask(claudeCode)))
	w.clock.add(oauth.CodeTTL)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	c := w.connectionOf(s, accountA, claudeCode)
	if c.activeDevices() != 1 || c.Devices[1].EndReason != contracts.EndedByTime {
		t.Fatalf("%+v", c.Devices)
	}
	// The index unreadable fails the sweep.
	w.mem.Interrupt = func(_ context.Context, _ store.Step, partition, _ string) error {
		if partition == partitionAccounts {
			return errors.New("down")
		}
		return nil
	}
	if err := s.Sweep(ctx); err == nil {
		t.Fatal("a sweep that read nothing succeeded")
	}
	w.mem.Interrupt = nil
}

// Limits keep their memory bounded; used requests are forgotten past their
// end.
func TestMemoryIsBounded(t *testing.T) {
	w := newWorld(t)
	s := w.enclave()
	for i := range cacheMax + 1 {
		s.remember(string(rune(i)), w.clock.now().Add(time.Second))
		s.limits.allow(string(rune(i)), 1, time.Minute, w.clock.now())
	}
	w.clock.add(time.Hour)
	s.remember("again", w.clock.now().Add(time.Second))
	s.limits.allow("again", 1, time.Minute, w.clock.now())
	if len(s.used) > 2 || len(s.limits.w) > 2 {
		t.Fatal(len(s.used), len(s.limits.w))
	}
	if s.usedHere("again") != true || s.usedHere("nothing") {
		t.Fatal("used")
	}
}

// Attachments: what a read of one refuses.
func TestAttachmentRefusals(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	x.scope = "mail.sender mail.subject mail.body mail.attachments"
	w.mbx.add(mail{ID: "m300", Mailbox: mailboxID(1), Received: start.Add(-time.Hour), From: "a@example.com", Held: true, Attachments: []inbox.Attachment{{Name: "x"}}})
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	for id, want := range map[string]error{
		mailboxID(1) + ".m300": contracts.ErrNotFound, // held
		mailboxID(1) + ".m999": contracts.ErrNotFound, // not there
		mailboxID(3) + ".m001": contracts.ErrNotFound, // another account's
	} {
		if _, _, err := s.Attachment(ctx, g, id, 0); !errors.Is(err, want) {
			t.Error(id, err)
		}
	}
	if _, _, err := s.Attachment(ctx, g, mailboxID(1)+".m001", -1); !errors.Is(err, contracts.ErrNotFound) {
		t.Error(err)
	}
	if _, _, err := s.Attachment(ctx, g, mailboxID(1)+".m001", 5); !errors.Is(err, contracts.ErrNotFound) {
		t.Error(err)
	}
	w.mbx.add(mail{ID: "m301", Mailbox: mailboxID(1), Received: start.Add(-time.Hour), From: "a@example.com", Attachments: []inbox.Attachment{{Name: "x", Data: make([]byte, contracts.VendorAttachmentMax+1)}}})
	if _, _, err := s.Attachment(ctx, g, mailboxID(1)+".m301", 0); !errors.Is(err, contracts.ErrAttachmentTooLarge) {
		t.Error(err)
	}
	att, rel, err := s.Attachment(ctx, g, mailboxID(1)+".m001", 0)
	if err != nil || string(att.Data) != "pdf" {
		t.Fatal(att, err)
	}
	rel.Finish(true)
	w.mbx.mu.Lock()
	w.mbx.gone[mailboxID(2)] = true
	w.mbx.mu.Unlock()
	if _, _, err := s.Read(ctx, g, mailboxID(2)+".m100"); !errors.Is(err, contracts.ErrNotFound) {
		t.Error(err)
	}
	// A search skips a mailbox no longer there.
	page, rel, err := s.Search(ctx, g, contracts.VendorSearch{})
	if err != nil {
		t.Fatal(err)
	}
	rel.Finish(true)
	for _, m := range page.Messages {
		if strings.HasPrefix(m.ID, mailboxID(2)) {
			t.Fatal("read a mailbox no longer there")
		}
	}
}

// The person's list skips a pending connection and survives one whose
// record is gone.
func TestTheListOfAPerson(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	p := w.person(accountA)
	w.connect(s, p, w.ask(claudeCode))
	if _, err := s.updateList(ctx, accountA, func(l *ListState) error { return l.AddPending("con_"+strings.Repeat("p", 26), otherApp, w.clock.now()) }); err != nil {
		t.Fatal(err)
	}
	l, err := s.List(ctx, p)
	if err != nil || len(l.Connections) != 1 {
		t.Fatal(l, err)
	}
	c := l.Connections[0]
	if !c.Since.New || !c.Since.At.Equal(start) || c.Delivery != contracts.DeliveryOn || c.Version == 0 {
		t.Fatalf("%+v", c)
	}
	// Past its end, it reads as ended before the sweep writes it so.
	w.clock.add(oauth.ConnectionDefaultDays * 24 * time.Hour)
	if got, _ := s.Get(ctx, p, c.ID); got.State != contracts.ConnectionEnded || got.EndReason != contracts.EndedByTime {
		t.Fatalf("%+v", got)
	}
}
