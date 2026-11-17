//go:build dev

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide/decidetest"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// e2eBrowser is a person's browser in the tests of the whole service: its
// own cookies, its requests sent to one enclave or the other.
type e2eBrowser struct{ jar http.CookieJar }

// playedVendor serves vendors' client documents over TLS with HTTP/2, under
// certificates of its own authority, which only the client it gives
// trusts; that client dials it for every name, so no real vendor is ever
// reached (dev mode's vendors, V57).
type playedVendor struct {
	srv   *httptest.Server
	pool  *x509.CertPool
	caDER []byte
	mu    sync.Mutex
	docs  map[string]string // by host and path
	got   []string          // what was asked, host and path, with its protocol
}

func newPlayedVendor(t *testing.T, names ...string) *playedVendor {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "played vendors"}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(400 * 24 * time.Hour), KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(400 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	v := &playedVendor{pool: x509.NewCertPool(), docs: map[string]string{}, caDER: caDER}
	v.pool.AddCert(ca)
	v.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		v.got = append(v.got, r.Proto+" "+r.Host+r.URL.Path)
		doc, ok := v.docs[r.Host+r.URL.Path]
		v.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = io.WriteString(w, doc)
	}))
	v.srv.EnableHTTP2 = true
	v.srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	v.srv.StartTLS()
	t.Cleanup(v.srv.Close)
	return v
}

// client is what dev mode fetches vendors with: the image's vendor client,
// dialling the played vendor whatever the name.
func (v *playedVendor) client() *http.Client {
	return playedVendorClient(v.srv.Listener.Addr().String(), v.pool)
}

// serve publishes a client's document at its client_id.
func (v *playedVendor) serve(t *testing.T, clientID, doc string) {
	t.Helper()
	u, err := url.Parse(clientID)
	if err != nil {
		t.Fatal(err)
	}
	v.mu.Lock()
	v.docs[u.Host+u.Path] = doc
	v.mu.Unlock()
}

func (v *playedVendor) asked() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.got...)
}

// e2eWorld is what TestMailEndToEnd gives its connections' half (V47).
type e2eWorld struct {
	enclaves   []config
	call       func(b e2eBrowser, enclave int, method, path, body string) mailAnswer
	newBrowser func() e2eBrowser
	outboxCode func(enclave int, to, subject, attempt string) string
	send       func(enclave int, raw string, rcpts ...string) error
	modelTab   *decidetest.Table
	modelKey   string
	vendor     *playedVendor
	// bea is signed in, agreed to the model's notice, and owns mailbox,
	// whose address is address and which holds the quarterly news.
	bea              e2eBrowser
	mailbox, address string
	// platforms are the two enclaves' platforms.
	platforms []*platform
	// auditTicks and incidentsTicks tick each enclave's audit and its reads
	// of the record of incidents (runReceipts).
	auditTicks, incidentsTicks []chan time.Time
}

// The clients the played vendor publishes: an app for the API and an agent
// for MCP, neither on the verified list, both returning to the person's own
// computer.
const (
	e2eAppClient   = "https://apps.vendor-e2e.org/permaily/client.json"
	e2eAgentClient = "https://agents.vendor-e2e.org/mcp/client.json"
	e2eRedirect    = "http://127.0.0.1:49152/callback"
)

func e2eClientDocument(clientID, name string) string {
	return `{"client_id":"` + clientID + `","client_name":"` + name + `","redirect_uris":["http://127.0.0.1/callback"],` +
		`"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"none"}`
}

// vendorAnswer is an answer to the vendor's own client.
type vendorAnswer struct {
	code   int
	proto  string
	header http.Header
	body   string
}

// connectionsEndToEnd is V47 (ADR 0030, "The image"), the connections' half
// of TestMailEndToEnd: a vendor makes a connection through the app's
// routes (a fresh proof, the hand-off), exchanges its code and searches and
// reads over the API through the other enclave, offering h2 and given
// http/1.1, is held back from a message with a sign-in code, adds a second
// device, an agent searches over MCP the same way, both are refused while
// the model is off, and the connection is revoked: at once through the
// enclave that revoked it, within five seconds through the other. It
// returns what the log must not hold besides what TestMailEndToEnd checks
// (V34's list: tokens, a code, a verifier, a state, a redirect URI, an
// unverified client_id, a formula, an account, a connection, a device, a
// message, a request), and checks that every connections' record went
// through the connections' one strict Anchored and no other (V57).
func connectionsEndToEnd(t *testing.T, w e2eWorld) []string {
	w.vendor.serve(t, e2eAppClient, e2eClientDocument(e2eAppClient, "E2E Mail App"))
	w.vendor.serve(t, e2eAgentClient, e2eClientDocument(e2eAgentClient, "E2E Agent"))
	cfg := oauthConfig(router.HostsForDomain(w.enclaves[0].domain), w.enclaves[0].oauthPort())
	var secrets []string
	decode := func(a mailAnswer, status int, v any) {
		t.Helper()
		if a.code != status || (v != nil && json.Unmarshal([]byte(a.body), v) != nil) {
			t.Fatalf("%d %s, want %d", a.code, a.body, status)
		}
	}
	// The vendor's own client offers h2 and http/1.1, as Go's does.
	vendorDo := func(enclave int, method, rawURL, token, contentType, body string) vendorAnswer {
		t.Helper()
		client := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2", "http/1.1"}}, //nolint:gosec // dev mode self-signed certificate
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", w.enclaves[enclave].port))
				},
				ForceAttemptHTTP2: true,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		req, err := http.NewRequest(method, rawURL, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if strings.HasPrefix(rawURL, cfg.MCPResource) {
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		return vendorAnswer{code: res.StatusCode, proto: res.Proto, header: res.Header, body: string(out)}
	}
	// The api and mcp hosts negotiate http/1.1 with a client that offers
	// h2; the app's keeps h2.
	if a := vendorDo(0, "GET", cfg.AppOrigin+"/", "", "", ""); a.proto != "HTTP/2.0" {
		t.Fatalf("the app's host over %s", a.proto)
	}
	token := func(enclave int, form url.Values) (*connectionsTokens, vendorAnswer) {
		t.Helper()
		a := vendorDo(enclave, "POST", cfg.Issuer+oauth.PathToken, "", "application/x-www-form-urlencoded", form.Encode())
		if a.code != 200 {
			return nil, a
		}
		var tok connectionsTokens
		if err := json.Unmarshal([]byte(a.body), &tok); err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, tok.AccessToken, tok.RefreshToken, tok.AccessToken[len(oauth.PrefixAccess):], tok.RefreshToken[len(oauth.PrefixRefresh):])
		return &tok, a
	}

	// Mail for Bea: something to read, and a sign-in code the default rule
	// holds by its exact phrases. The model answers the default rule's
	// topics low for everything else.
	lunch := "From: Carol <carol@example.net>\r\nTo: " + w.address + "\r\nSubject: Lunch plans penguin-7e21\r\nContent-Type: text/plain\r\n\r\nShall we meet at the pier penguin-7e21?\r\n"
	code := "From: Bank <no-reply@bank.example.net>\r\nTo: " + w.address + "\r\nSubject: Your verification code\r\nContent-Type: text/plain\r\n\r\nYour verification code is 482913 heron-3c55.\r\n"
	for _, raw := range []string{lunch, code} {
		if err := w.send(0, raw, w.address); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range rules.Default().Formulas {
		for _, g := range f.Formula.All {
			for _, c := range g.Any {
				if c.Op == contracts.OpAbout {
					w.modelTab.Set(query.Atom{Var: c.Var, Op: c.Op, Topic: c.Value}, 0.02)
				}
			}
		}
	}
	var own contracts.MessagePage
	decode(w.call(w.bea, 1, "GET", "/api/v1/mailboxes/"+w.mailbox+"/messages", ""), 200, &own)
	var heldID string
	for _, m := range own.Messages {
		secrets = append(secrets, m.ID)
		if m.Subject == "Your verification code" {
			heldID = w.mailbox + "." + m.ID
		}
	}
	if heldID == "" || len(own.Messages) != 3 {
		t.Fatalf("Bea's mail: %+v", own)
	}

	// A fresh proof: a code by mail, through either enclave.
	var confirm struct{ Attempt string }
	decode(w.call(w.bea, 0, "POST", "/api/v1/auth/confirm/email/start", ""), 200, &confirm)
	decode(w.call(w.bea, 1, "POST", "/api/v1/auth/confirm/email/finish", `{"attempt":"`+confirm.Attempt+`","code":"`+w.outboxCode(0, "bea@example.org", "Confirm it is you on Permaily", "")+`"}`), 200, nil)

	// connect runs the hand-off for a client and a resource: the query
	// string as the vendor sent the browser, posted by the app; the screen;
	// a preview of the draft; Allow; the redirect with its code, state and
	// iss; the code exchanged through the other enclave, and, with reuse,
	// exchanged again through the first, which is refused and ends the
	// device it made.
	connect := func(clientID, resource string, want contracts.ConnectionChange, reuse bool) *connectionsTokens {
		verifier := randomURLSafe(t)
		sum := sha256.Sum256([]byte(verifier))
		state := "st-" + randomURLSafe(t)[:12]
		secrets = append(secrets, verifier, state, base64.RawURLEncoding.EncodeToString(sum[:]))
		q := url.Values{
			"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {e2eRedirect}, "state": {state},
			"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
			"resource": {resource}, "scope": {"mail.sender mail.subject mail.body"},
		}
		body, _ := json.Marshal(contracts.AuthorizationStart{Query: q.Encode()})
		var started contracts.AuthorizationStarted
		decode(w.call(w.bea, 0, "POST", "/api/v1/oauth/requests", string(body)), 200, &started)
		r := started.Request
		if r == nil || r.Vendor.Verified != "" || !r.Loopback || r.RedirectHost != "127.0.0.1" || r.Change != want || r.ModelNotice != domain.ModelNoticeVersion || len(r.Rules) != 1 || r.Rules[0].ID != rules.DefaultID {
			t.Fatalf("the screen: %+v", started)
		}
		secrets = append(secrets, r.ID, r.ID[len(oauth.PrefixRequest):])
		draft := contracts.ConsentDraft{Mailboxes: []string{w.mailbox}, Since: contracts.MailSince{At: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}}
		preview, _ := json.Marshal(contracts.ConsentPreview{Draft: draft, Mailbox: w.mailbox})
		var page contracts.ConsentPreviewPage
		decode(w.call(w.bea, 1, "POST", "/api/v1/oauth/requests/"+r.ID+"/matches", string(preview)), 200, &page)
		if page.Change != want || page.Snapshot.Change != want {
			t.Fatalf("the preview: %+v", page)
		}
		decision, _ := json.Marshal(contracts.AuthorizationDecision{Allow: true, Draft: &draft, ModelNotice: r.ModelNotice, Snapshot: &page.Snapshot})
		var answer contracts.AuthorizationAnswer
		decode(w.call(w.bea, 0, "POST", "/api/v1/oauth/requests/"+r.ID+"/decision", string(decision)), 200, &answer)
		back, err := url.Parse(answer.Redirect)
		if err != nil || back.Scheme+"://"+back.Host+back.Path != e2eRedirect || back.Query().Get("state") != state || back.Query().Get("iss") != cfg.Issuer {
			t.Fatalf("the redirect: %v", answer.Redirect)
		}
		c := back.Query().Get("code")
		secrets = append(secrets, c, c[len(oauth.PrefixCode):])
		tok, a := token(1, url.Values{
			"grant_type": {"authorization_code"}, "code": {c}, "code_verifier": {verifier}, "redirect_uri": {e2eRedirect},
			"client_id": {clientID}, "resource": {resource},
		})
		if tok == nil || a.proto != "HTTP/1.1" {
			t.Fatalf("the exchange: %+v", a)
		}
		if reuse {
			if _, again := token(0, url.Values{
				"grant_type": {"authorization_code"}, "code": {c}, "code_verifier": {verifier}, "redirect_uri": {e2eRedirect},
				"client_id": {clientID}, "resource": {resource},
			}); again.code != 400 || !strings.Contains(again.body, `"invalid_grant"`) {
				t.Fatalf("the code again: %+v", again)
			}
		}
		return tok
	}

	// The app: a connection to the API.
	app := connect(e2eAppClient, cfg.APIResource, contracts.ChangeNew, false)
	var conns contracts.ConnectionList
	decode(w.call(w.bea, 1, "GET", "/api/v1/connections", ""), 200, &conns)
	if len(conns.Connections) != 1 || conns.Connections[0].Vendor.Domain != "apps.vendor-e2e.org" || len(conns.Connections[0].Devices) != 1 {
		t.Fatalf("Bea's connections: %+v", conns)
	}
	connID := conns.Connections[0].ID
	secrets = append(secrets, connID, connID[len("con_"):], conns.Connections[0].Devices[0].ID)

	// The code was exchanged, then the codes in a refreshed family: the API
	// over http/1.1 though the client offered h2, through the other enclave.
	search := func(enclave int, tok string) vendorAnswer {
		return vendorDo(enclave, "POST", cfg.APIResource+"/messages/search", tok, "application/json", `{}`)
	}
	a := search(1, app.AccessToken)
	var vp contracts.VendorPage
	if a.code != 200 || a.proto != "HTTP/1.1" || json.Unmarshal([]byte(a.body), &vp) != nil {
		t.Fatalf("a search: %+v", a)
	}
	var subjects []string
	var lunchID string
	for _, m := range vp.Messages {
		secrets = append(secrets, m.ID)
		if m.Subject != nil {
			subjects = append(subjects, *m.Subject)
			if strings.HasPrefix(*m.Subject, "Lunch") {
				lunchID = m.ID
			}
		}
	}
	if strings.Join(subjects, "|") != "Lunch plans penguin-7e21|Quarterly news" || lunchID == "" {
		t.Fatalf("the search gave %q", subjects)
	}
	a = vendorDo(0, "GET", cfg.APIResource+"/messages/"+lunchID, app.AccessToken, "", "")
	var read contracts.VendorMessage
	if a.code != 200 || a.proto != "HTTP/1.1" || json.Unmarshal([]byte(a.body), &read) != nil || read.Text == nil || !strings.Contains(*read.Text, "pier penguin-7e21") {
		t.Fatalf("a read: %+v", a)
	}
	// The sign-in code is held, answered as what does not exist.
	held := vendorDo(1, "GET", cfg.APIResource+"/messages/"+heldID, app.AccessToken, "", "")
	none := vendorDo(1, "GET", cfg.APIResource+"/messages/"+w.mailbox+"."+strings.Repeat("0", 31)+"1", app.AccessToken, "", "")
	if held.code != 404 || held.body != none.body || strings.Contains(held.body, "verification") {
		t.Fatalf("the held message: %+v; one that does not exist: %+v", held, none)
	}
	// L21 (ADR 0037): every answer that gives mail or refuses it, every
	// consent and every end waits for its receipt to be linked in Bea's
	// chain. Behind a chain whose commits fail, nothing of the mail leaves;
	// behind one whose commit is held, nothing is answered until it lands,
	// and then the head of her chain has moved on.
	var me contracts.Me
	decode(w.call(w.bea, 0, "GET", "/api/v1/me", ""), 200, &me)
	beaAccount := me.Account
	secrets = append(secrets, beaAccount.ID, beaAccount.ID[len("acc_"):])
	chainHead := func() uint64 {
		t.Helper()
		h, err := w.platforms[1].receipts.svc.Head(context.Background(), beaAccount.ID)
		if err != nil || h.Verdict != receipts.LinkOK {
			t.Fatalf("Bea's chain's head: %+v %v", h, err)
		}
		return h.N
	}
	// behindFailingChain runs do with every commit of the chain refused:
	// what it answers holds none of leaks, and the head does not move.
	behindFailingChain := func(what string, do func() vendorAnswer, leaks ...string) vendorAnswer {
		t.Helper()
		before := chainHead()
		devChain.Store(&devChainHook{fail: true})
		a := do()
		devChain.Store(nil)
		for _, leak := range leaks {
			if strings.Contains(a.body, leak) {
				t.Fatalf("%s behind a failing chain gave %q: %+v", what, leak, a)
			}
		}
		if after := chainHead(); after != before {
			t.Fatalf("%s behind a failing chain moved the head from %d to %d", what, before, after)
		}
		return a
	}
	// behindHeldChain runs do with the chain's commits held: it answers
	// nothing until they are let go, and then the head has moved on.
	behindHeldChain := func(what string, do func()) {
		t.Helper()
		before := chainHead()
		hold, waiting, done := make(chan struct{}), make(chan struct{}, 1), make(chan struct{})
		devChain.Store(&devChainHook{hold: hold, waiting: waiting})
		go func() {
			defer close(done)
			do()
		}()
		select {
		case <-waiting:
		case <-done:
			devChain.Store(nil)
			t.Fatalf("%s answered and wrote no link", what)
		case <-time.After(30 * time.Second):
			t.Fatalf("%s wrote no link", what)
		}
		select {
		case <-done:
			t.Fatalf("%s answered before its receipt was linked", what)
		case <-time.After(300 * time.Millisecond):
		}
		devChain.Store(nil)
		close(hold)
		<-done
		if after := chainHead(); after <= before {
			t.Fatalf("%s did not extend the chain: %d, then %d", what, before, after)
		}
	}
	type released struct{ a vendorAnswer }
	var got released
	behindFailingChain("a search", func() vendorAnswer { return search(0, app.AccessToken) }, "penguin", "Quarterly")
	behindHeldChain("a search", func() { got.a = search(1, app.AccessToken) })
	if got.a.code != 200 || !strings.Contains(got.a.body, "penguin-7e21") {
		t.Fatalf("a search behind a held chain: %+v", got.a)
	}
	readLunch := func() vendorAnswer {
		return vendorDo(0, "GET", cfg.APIResource+"/messages/"+lunchID, app.AccessToken, "", "")
	}
	behindFailingChain("a read", readLunch, "pier", "penguin")
	behindHeldChain("a read", func() { got.a = readLunch() })
	if got.a.code != 200 || !strings.Contains(got.a.body, "pier penguin-7e21") {
		t.Fatalf("a read behind a held chain: %+v", got.a)
	}
	// A refusal of the API's own (a limit it does not take) and a read of
	// a message the default rule holds, each recorded before it is
	// answered; behind a failing chain, answered 503 (and the refusals'
	// lane tries its write again later, ADR 0031).
	refused := func() vendorAnswer {
		return vendorDo(0, "POST", cfg.APIResource+"/messages/search", app.AccessToken, "application/json", `{"limit":100000}`)
	}
	readHeld := func() vendorAnswer {
		return vendorDo(0, "GET", cfg.APIResource+"/messages/"+heldID, app.AccessToken, "", "")
	}
	behindHeldChain("a refusal", func() { got.a = refused() })
	if got.a.code != 400 {
		t.Fatalf("a refusal behind a held chain: %+v", got.a)
	}
	behindHeldChain("a message the rule holds", func() { got.a = readHeld() })
	if got.a.code != 404 || strings.Contains(got.a.body, "verification") {
		t.Fatalf("a held message behind a held chain: %+v", got.a)
	}
	if a := behindFailingChain("a refusal", refused); a.code != 503 {
		t.Fatalf("a refusal that cannot be recorded: %+v", a)
	}
	// The same token at the other resource is refused.
	if a := vendorDo(1, "POST", cfg.MCPResource, app.AccessToken, "application/json", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`); a.code != 401 || !strings.Contains(a.header.Get("WWW-Authenticate"), "invalid_token") {
		t.Fatalf("the API's token at MCP: %+v", a)
	}

	// A second device: the same client asks again for the same scope, and
	// the person confirms it; both devices' tokens work.
	var second *connectionsTokens
	behindHeldChain("a consent", func() { second = connect(e2eAppClient, cfg.APIResource, contracts.ChangeAddDevice, false) })
	decode(w.call(w.bea, 0, "GET", "/api/v1/connections/"+connID, ""), 200, &conns.Connections[0])
	if c := conns.Connections[0]; c.State != contracts.ConnectionActive || len(c.Devices) != 2 {
		t.Fatalf("after the second device: %+v", c)
	}
	secrets = append(secrets, conns.Connections[0].Devices[1].ID)
	for _, tok := range []string{app.AccessToken, second.AccessToken} {
		if a := vendorDo(0, "GET", cfg.APIResource+"/connection", tok, "", ""); a.code != 200 || !strings.Contains(a.body, `"delivery":"delivering"`) {
			t.Fatalf("a device's token: %+v", a)
		}
	}
	// A third device whose code is exchanged twice: the second exchange is
	// refused and ends that device alone (V24).
	third := connect(e2eAppClient, cfg.APIResource, contracts.ChangeAddDevice, true)
	if a := vendorDo(1, "GET", cfg.APIResource+"/connection", third.AccessToken, "", ""); a.code != 401 {
		t.Fatalf("the reused code's device: %+v", a)
	}
	decode(w.call(w.bea, 1, "GET", "/api/v1/connections/"+connID, ""), 200, &conns.Connections[0])
	if c := conns.Connections[0]; len(c.Devices) != 3 || c.Devices[2].State != contracts.ConnectionEnded || c.Devices[2].EndReason != contracts.EndedByReuse || c.Devices[0].State != contracts.ConnectionActive {
		t.Fatalf("after a code used twice: %+v", c)
	}
	secrets = append(secrets, conns.Connections[0].Devices[2].ID)

	// An agent over MCP: its own connection, a tool call over http/1.1
	// though it offered h2, through the other enclave.
	agent := connect(e2eAgentClient, cfg.MCPResource, contracts.ChangeNew, false)
	mcpCall := func(enclave int, tok, tool string) vendorAnswer {
		return vendorDo(enclave, "POST", cfg.MCPResource, tok, "application/json", `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"`+tool+`","arguments":{}}}`)
	}
	if a := mcpCall(1, agent.AccessToken, "search_mail"); a.code != 200 || a.proto != "HTTP/1.1" || !strings.Contains(a.body, "Lunch plans penguin-7e21") || strings.Contains(a.body, "verification") {
		t.Fatalf("search_mail: %+v", a)
	}
	behindFailingChain("search_mail", func() vendorAnswer { return mcpCall(0, agent.AccessToken, "search_mail") }, "penguin", "Quarterly")
	behindHeldChain("search_mail", func() { got.a = mcpCall(1, agent.AccessToken, "search_mail") })
	if got.a.code != 200 || !strings.Contains(got.a.body, "Lunch plans penguin-7e21") {
		t.Fatalf("search_mail behind a held chain: %+v", got.a)
	}

	// A refresh, and its retry within 30 seconds: the same pair.
	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {app.RefreshToken}, "client_id": {e2eAppClient}, "resource": {cfg.APIResource}}
	r1, a1 := token(0, refresh)
	r2, a2 := token(1, refresh)
	if r1 == nil || r2 == nil || r1.AccessToken != r2.AccessToken || r1.RefreshToken != r2.RefreshToken || r1.AccessToken == app.AccessToken {
		t.Fatalf("a refresh and its retry: %+v %+v", a1, a2)
	}

	// The model off: the operator removes its key; once the enclaves read
	// it again, nothing leaves, and the connection says why without words
	// of the mail.
	if a := w.call(w.newBrowser(), 0, "DELETE", "https://api.permaily.test/v1/admin/model-key", ""); a.code != 200 && a.code != 204 {
		t.Fatalf("removing the model's key: %d %s", a.code, a.body)
	}
	devAhead.Add(int64(2 * time.Minute))
	for enclave := range 2 {
		if a := search(enclave, r1.AccessToken); a.code != 503 || !strings.Contains(a.body, `"model_unavailable"`) || a.header.Get("Retry-After") == "" {
			t.Fatalf("a search while the model is off, through %d: %+v", enclave, a)
		}
		if a := vendorDo(enclave, "GET", cfg.APIResource+"/connection", r1.AccessToken, "", ""); a.code != 200 || !strings.Contains(a.body, `"delivery":"model_unavailable"`) {
			t.Fatalf("the connection while the model is off: %+v", a)
		}
		if a := mcpCall(enclave, agent.AccessToken, "search_mail"); a.code != 200 || !strings.Contains(a.body, "model_unavailable") || strings.Contains(a.body, "penguin") {
			t.Fatalf("search_mail while the model is off: %+v", a)
		}
	}
	// Back on: the next search gives mail again.
	if a := w.call(w.newBrowser(), 1, "PUT", "https://api.permaily.test/v1/admin/model-key", `{"key":"`+w.modelKey+`"}`); a.code != 200 {
		t.Fatalf("the key again: %d %s", a.code, a.body)
	}
	devAhead.Add(int64(2 * time.Minute))
	if a := search(0, second.AccessToken); a.code != 200 || !strings.Contains(a.body, "penguin-7e21") {
		t.Fatalf("a search with the model back: %+v", a)
	}

	// Revoked through the first enclave: refused there at once, and
	// through the other once its read is five seconds old.
	var revoked contracts.Connection
	var revokedAnswer mailAnswer
	behindHeldChain("an end", func() { revokedAnswer = w.call(w.bea, 0, "POST", "/api/v1/connections/"+connID+"/revoke", "") })
	decode(revokedAnswer, 200, &revoked)
	if revoked.State != contracts.ConnectionEnded || revoked.EndReason != contracts.EndedByPerson {
		t.Fatalf("revoked: %+v", revoked)
	}
	for _, tok := range []string{r1.AccessToken, second.AccessToken} {
		if a := vendorDo(0, "GET", cfg.APIResource+"/connection", tok, "", ""); a.code != 401 || !strings.Contains(a.header.Get("WWW-Authenticate"), `error="invalid_token"`) {
			t.Fatalf("a revoked token through the enclave that revoked it: %+v", a)
		}
	}
	devAhead.Add(int64(oauth.ConnectionFresh + time.Second))
	for _, tok := range []string{r1.AccessToken, second.AccessToken} {
		if a := search(1, tok); a.code != 401 {
			t.Fatalf("a revoked token through the other enclave after five seconds: %+v", a)
		}
	}
	if _, a := token(1, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {r1.RefreshToken}, "client_id": {e2eAppClient}}); a.code != 400 || !strings.Contains(a.body, `"invalid_grant"`) {
		t.Fatalf("a revoked connection's refresh: %+v", a)
	}
	// The agent's connection is untouched.
	if a := mcpCall(1, agent.AccessToken, "connection_info"); a.code != 200 || !strings.Contains(a.body, `"delivery":"delivering"`) {
		t.Fatalf("the agent after the app's revocation: %+v", a)
	}

	// /status counts by fixed reason, and names no client, account or
	// connection.
	st := w.call(w.newBrowser(), 0, "GET", "/status", "")
	var status struct {
		Connections *attestation.ConnectionsStatus `json:"connections"`
	}
	if st.code != 200 || json.Unmarshal([]byte(st.body), &status) != nil || status.Connections == nil || status.Connections.Refused["allowed"] < 2 ||
		status.Connections.Refused["model_unavailable"] == 0 || status.Connections.Issuer != cfg.Issuer {
		t.Fatalf("/status: %d %s", st.code, st.body)
	}
	for _, never := range []string{"vendor-e2e", connID, w.mailbox, app.AccessToken, beaAccount.ID, "receipt-"} {
		if strings.Contains(st.body, never) {
			t.Fatalf("/status holds %q", never)
		}
	}
	// L19: a day later, each enclave, once it ran its audit and read the
	// record of incidents on its ticks, reports receipts permaily-verify's
	// check passes, with the deployment's one newest pass: it checked Bea's
	// chain, found nothing broken, and linked late the receipts the failing
	// chain left without a link (recovered, a warning).
	devAhead.Add(int64(receipts.AuditEvery))
	ticked := devClock()
	var passes []attestation.ReceiptsAudit
	for enclave := range 2 {
		w.auditTicks[enclave] <- time.Now()
		w.incidentsTicks[enclave] <- time.Now()
		var last error
		for i := 0; ; i++ {
			st := w.call(w.newBrowser(), enclave, "GET", "/status", "")
			if json.Unmarshal([]byte(st.body), &status) != nil || status.Connections == nil {
				t.Fatalf("/status: %d %s", st.code, st.body)
			}
			rs := status.Connections.Receipts
			if last = rs.Check(devClock()); last == nil && rs.Audit.Began.Before(ticked) {
				last = errors.New("the newest pass is the one before the tick")
			}
			if last == nil {
				passes = append(passes, *rs.Audit)
				break
			}
			if i == 200 {
				t.Fatalf("enclave %d's receipts: %v\n%s", enclave, last, st.body)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !passes[0].Finished.Equal(passes[1].Finished) || passes[0].Chains < 1 || passes[0].Recovered < 3 || passes[0].Unlinked != 0 {
		t.Fatalf("the enclaves report passes %+v and %+v", passes[0], passes[1])
	}

	// The played vendor was asked for the documents over HTTP/2, and the
	// enclaves asked nothing else of anybody.
	for _, got := range w.vendor.asked() {
		if !strings.HasPrefix(got, "HTTP/2.0 ") || !strings.Contains(got, "vendor-e2e.org/") {
			t.Fatalf("the played vendor was asked %q", got)
		}
	}
	// Bea's chain, walked through either enclave from its head to its first
	// link: every link and every receipt as it was written.
	for _, p := range w.platforms {
		links := 0
		for before := (receipts.Position{}); ; {
			page, err := p.receipts.svc.Walk(context.Background(), beaAccount.ID, before, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range page.Links {
				links++
				if l.Verdict != receipts.LinkOK {
					t.Fatalf("link %d: %s", l.N, l.Verdict)
				}
				for _, r := range l.Receipts {
					if r.Verdict != receipts.ReceiptOK {
						t.Fatalf("link %d's receipt %d: %s", l.N, r.Index, r.Verdict)
					}
				}
			}
			if page.Done {
				break
			}
			before = page.Next
		}
		if links == 0 || uint64(links) != chainHead() {
			t.Fatalf("the walk read %d links of %d", links, chainHead())
		}
	}
	checkRecordsApart(t, w.platforms)
	return append(secrets, "vendor-e2e", e2eAppClient, e2eAgentClient, e2eRedirect, "49152/callback", "penguin", "heron", "482913", "carol@", "bank.example")
}

// connectionsTokens is a token endpoint's answer.
type connectionsTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

func randomURLSafe(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// connectionsPrefixes are the names of the connections' strict records.
var connectionsPrefixes = []string{"connection-", "connections-", "oauth-family-", "oauth-assertions-"}

// receiptsPrefixes are the names of the chain's strict records (ADR 0037).
var receiptsPrefixes = []string{"receipt-link-", "receipt-chains-", "receipt-audit-", "receipt-incidents-"}

// checkRecordsApart is V57's "one strict Anchored that no other reads the
// names of", and L21's for the chain of receipts (ADR 0037): every record
// the connections' Anchored read or made is one of theirs, every record
// the chain's Anchored read or made is one of the chain's, and no other
// Anchored of the enclave (accounts', mailboxes', the model's, each
// other's) read or made one of them. The chain's commits are grouped as
// receipts.CommitGroup says, in the one store of commits every Anchored
// shares.
func checkRecordsApart(t *testing.T, ps []*platform) {
	t.Helper()
	of := func(prefixes []string) func(string) bool {
		return func(name string) bool {
			for _, p := range prefixes {
				if strings.HasPrefix(name, p) {
					return true
				}
			}
			return false
		}
	}
	own := map[string]func(string) bool{"connections": of(connectionsPrefixes), "receipts": of(receiptsPrefixes)}
	for _, p := range ps {
		seen := map[string]int{}
		for kind, a := range p.records {
			for _, c := range a.Seen() {
				for owner, theirs := range own {
					switch {
					case kind == owner && !theirs(c.Name):
						t.Errorf("the %s' Anchored read another's record", kind)
					case kind == owner:
						seen[kind]++
					case theirs(c.Name):
						t.Errorf("the %s' Anchored read a record of the %s", kind, owner)
					}
				}
			}
		}
		// The chain's Anchored forgets each link once the next is known
		// (Anchored.Forget), so what it keeps proves nothing of what it
		// read; the walk of Bea's chain does.
		if seen["connections"] == 0 {
			t.Errorf("the connections' Anchored read nothing")
		}
		if p.records["connections"] != p.connectionRecords || !p.connectionRecords.Strict || !p.connectionRecords.OnlyNewest {
			t.Errorf("the connections' records are not their own strict Anchored")
		}
		chain := p.records["receipts"]
		if chain == nil || chain != p.receipts.chain || !chain.Strict || !chainGrouped(chain.Commits) {
			t.Errorf("the chain's records are not their own strict Anchored over grouped commits")
		}
		if _, ok := chain.Records.(receipts.ChainTable); !ok {
			t.Errorf("the chain's records are not kept in the chain's table")
		}
	}
}

// V57 (ADR 0030, "The image"): dev mode reaches vendors only through what
// it is given, a played vendor's address and roots, and never a real one:
// with nothing given every fetch fails before anything is dialled; given a
// played vendor, a real vendor's name (claude.ai) is dialled at the played
// address and checked against its roots alone, over HTTP/2; a played vendor
// with the image's roots trusted nowhere, or one that does not speak
// HTTP/2, is refused.
func TestV57DevModeReachesOnlyPlayedVendors(t *testing.T) {
	devVendors.Store(nil)
	none := devVendorHTTP(devConfig{})
	if _, err := none.Get("https://claude.ai/oauth/claude-code-client-metadata"); !errors.Is(err, errNoVendor) {
		t.Fatalf("dev mode with no played vendor: %v", err)
	}
	v := newPlayedVendor(t, "claude.ai")
	v.serve(t, "https://claude.ai/oauth/claude-code-client-metadata", `{"played":true}`)
	res, err := v.client().Get("https://claude.ai/oauth/claude-code-client-metadata")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.Proto != "HTTP/2.0" || string(body) != `{"played":true}` {
		t.Fatalf("the played claude.ai: %s %s", res.Proto, body)
	}
	// The played vendor through the flags, as a person running dev mode
	// gives it.
	pemFile := t.TempDir() + "/ca.pem"
	writePEM(t, pemFile, v)
	flagged := devVendorHTTP(devConfig{vendors: v.srv.Listener.Addr().String(), vendorsCA: pemFile})
	if res, err := flagged.Get("https://claude.ai/oauth/claude-code-client-metadata"); err != nil || res.Proto != "HTTP/2.0" {
		t.Fatalf("through the flags: %v", err)
	} else {
		res.Body.Close()
	}
	// The system's roots are not the played vendor's: a client without
	// them refuses it.
	if _, err := playedVendorClient(v.srv.Listener.Addr().String(), x509.NewCertPool()).Get("https://claude.ai/x"); err == nil {
		t.Fatal("a played vendor trusted without its roots")
	}
	// A played vendor that speaks only HTTP/1.1 is refused at the handshake.
	h1 := newPlayedVendor(t, "chatgpt.com")
	h1.srv.Close()
	old := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("an HTTP/1.1 vendor was asked") }))
	old.TLS = h1.srv.TLS.Clone()
	old.TLS.NextProtos = []string{"http/1.1"}
	old.StartTLS()
	t.Cleanup(old.Close)
	if _, err := playedVendorClient(old.Listener.Addr().String(), h1.pool).Get("https://chatgpt.com/oauth/client.json"); err == nil {
		t.Fatal("an HTTP/1.1 vendor reached")
	}
	// The test's client takes the place of the flags.
	devVendors.Store(v.client())
	t.Cleanup(func() { devVendors.Store(nil) })
	if devVendorHTTP(devConfig{vendors: "203.0.113.1:443"}) != devVendors.Load() {
		t.Fatal("the test's client is not the one used")
	}
}

// V57 (ADR 0030, "Revocation and two enclaves"): the image runs the
// connections' hourly pass on its own. run, the enclave's startup, with the
// configuration main reads (newConfig) and a ticker the test gives
// (config.sweepTick), over storage the test watches: the pass lists the
// connections' index of accounts once as the enclave starts, before any
// tick, and once more after each tick, and the server it started answers.
// Without runConnections in run it fails.
func TestV57TheImageRunsTheConnectionsPassOnItsOwn(t *testing.T) {
	shared, err := newDevStorage()
	if err != nil {
		t.Fatal(err)
	}
	var passes atomic.Int64
	shared.mem.Interrupt = func(_ context.Context, step store.Step, partition, _ string) error {
		if step == store.StepRead && partition == "connections#accounts" {
			passes.Add(1)
		}
		return nil
	}
	devShared.Store(shared)
	t.Cleanup(func() { devShared.Store(nil) })
	fs := flag.NewFlagSet("permaily-enclave", flag.ContinueOnError)
	cfg := newConfig(fs)
	port := freePort(t)
	if err := fs.Parse([]string{"-domain", "permaily.test", "-acme-directory", "unused", "-port", fmt.Sprint(port), "-smtp-port", fmt.Sprint(freePort(t))}); err != nil {
		t.Fatal(err)
	}
	sweep := make(chan time.Time)
	cfg.sweepTick = sweep
	go func() { _ = run(*cfg, slog.New(slog.DiscardHandler)) }()

	waitUntil(t, "the enclave started without the connections' pass", func() bool { return passes.Load() >= 1 })
	// The server run started answers: the pass is the running enclave's.
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev mode's self-signed certificate
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
		},
	}}
	var st attestation.Status
	waitUntil(t, "the enclave's /status", func() bool {
		res, err := client.Get("https://app.permaily.test/status")
		if err != nil {
			return false
		}
		defer res.Body.Close()
		return res.StatusCode == http.StatusOK && json.NewDecoder(res.Body).Decode(&st) == nil
	})
	if st.Connections == nil {
		t.Fatal("/status has no connections")
	}
	// No pass but the first runs until a tick: the pass is on the ticker
	// it is given, not on another. A pass may read the index more than
	// once (a strict read that looks again), so what counts is that the
	// reads stop until a tick, and start again after each.
	settled := settle(t, &passes)
	for i := 1; i <= 2; i++ {
		tick(t, "the connections' pass", sweep)
		waitUntil(t, fmt.Sprintf("no pass after tick %d", i), func() bool { return passes.Load() > settled })
		settled = settle(t, &passes)
	}
}

// settle waits until n has not moved for 200 ms and returns it; it fails
// when n keeps moving for ten seconds, as it would on a ticker the test did
// not give.
func settle(t *testing.T, n *atomic.Int64) int64 {
	t.Helper()
	last, since := n.Load(), time.Now()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if v := n.Load(); v != last {
			last, since = v, time.Now()
		} else if time.Since(since) >= 200*time.Millisecond {
			return last
		}
	}
	t.Fatal("the connections' pass kept running without a tick")
	return 0
}

// writePEM writes the played vendors' authority to file, as -dev-vendors-ca
// reads it.
func writePEM(t *testing.T, file string, v *playedVendor) {
	t.Helper()
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: v.caDER}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// V46 (ADR 0030, "Hosts, issuer and endpoints"; the composition's half of
// httpapi's V46): every route of connections is mounted on its host and on
// no other: the server's metadata, the token, revocation and pushed
// request endpoints and the API on api., the resource metadata at both of
// RFC 9728's places and MCP on mcp., the authorization page and the
// person's routes on the app's host; an odd path on api. or mcp. is 404
// before any mux could redirect it with its query; nothing answers
// /.well-known/openid-configuration.
func TestV46EveryConnectionsRouteIsOnItsHost(t *testing.T) {
	shared, err := newDevStorage()
	if err != nil {
		t.Fatal(err)
	}
	devShared.Store(shared)
	t.Cleanup(func() { devShared.Store(nil) })
	cfg := config{domain: "permaily.test", acmeDirectory: "unused", port: freePort(t), smtpPort: freePort(t), mtaSTSMode: "testing"}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	hosts := router.HostsForDomain(cfg.domain)
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	p, err := newPlatform(ctx, cfg, hosts, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.listener.Close(); _ = p.smtpListener.Close() })
	h := handler(cfg, hosts, p, log)
	oc := oauthConfig(hosts, cfg.oauthPort())
	ask := func(host, method, target, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, "https://"+host+target, strings.NewReader(body))
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Origin", "https://"+hosts.App)
		switch {
		case method == http.MethodPost && strings.HasPrefix(target, "/oauth/"):
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		case method == http.MethodPost:
			r.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	jsonOf := func(rec *httptest.ResponseRecorder) map[string]any {
		var v map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &v)
		return v
	}
	// The metadata, each on its host.
	if rec := ask(hosts.API, "GET", oauth.PathServerMetadata, ""); rec.Code != 200 || jsonOf(rec)["issuer"] != oc.Issuer || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("the server's metadata: %d %s", rec.Code, rec.Body)
	}
	if rec := ask(hosts.API, "GET", oauth.PathResourceMetadata+oauth.PathAPI, ""); rec.Code != 200 || jsonOf(rec)["resource"] != oc.APIResource {
		t.Fatalf("the API's resource metadata: %d %s", rec.Code, rec.Body)
	}
	for _, path := range []string{oauth.PathResourceMetadata + oauth.PathMCP, oauth.PathResourceMetadata} {
		if rec := ask(hosts.MCP, "GET", path, ""); rec.Code != 200 || jsonOf(rec)["resource"] != oc.MCPResource {
			t.Fatalf("MCP's resource metadata at %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	// The issuer's endpoints answer RFC 6749's JSON, no-store.
	for _, path := range []string{oauth.PathToken, oauth.PathRevoke, oauth.PathPushed} {
		rec := ask(hosts.API, "POST", path, "")
		if rec.Code < 400 || rec.Code >= 500 || jsonOf(rec)["error"] == nil || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %s %v", path, rec.Code, rec.Body, rec.Header())
		}
	}
	// The resources challenge a request without a token, each naming its
	// own metadata.
	for _, r := range []struct{ host, method, path, metadata string }{
		{hosts.API, "GET", oauth.PathAPI + "/connection", oc.Issuer + oauth.PathResourceMetadata + oauth.PathAPI},
		{hosts.API, "POST", oauth.PathAPI + "/messages/search", oc.Issuer + oauth.PathResourceMetadata + oauth.PathAPI},
		{hosts.MCP, "POST", oauth.PathMCP, strings.TrimSuffix(oc.MCPResource, oauth.PathMCP) + oauth.PathResourceMetadata + oauth.PathMCP},
	} {
		rec := ask(r.host, r.method, r.path, `{}`)
		if rec.Code != 401 || !strings.Contains(rec.Header().Get("WWW-Authenticate"), `resource_metadata="`+r.metadata+`"`) {
			t.Fatalf("%s%s without a token: %d %v", r.host, r.path, rec.Code, rec.Header())
		}
	}
	// The authorization page is the app's, outside /api/, and the person's
	// routes need a session.
	// (The web app's index.html, or, in a test binary built without the web
	// app, its fixed answer: the SPA's either way.)
	rec := ask(hosts.App, "GET", oauth.PathAuthorize+"?client_id=x", "")
	spa := rec.Code == 200 && strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || strings.Contains(rec.Body.String(), "web UI not built")
	if rec.Header().Get("Cache-Control") != "no-cache" || !spa {
		t.Fatalf("the authorization page: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	for _, r := range [][2]string{{"GET", "/api/v1/connections"}, {"POST", "/api/v1/oauth/requests"}} {
		if rec := ask(hosts.App, r[0], r[1], `{"query":""}`); rec.Code != 401 {
			t.Fatalf("%s %s without a session: %d %s", r[0], r[1], rec.Code, rec.Body)
		}
	}
	// On no other host.
	for _, r := range []struct{ host, method, path string }{
		{hosts.MCP, "POST", oauth.PathToken}, {hosts.MCP, "GET", oauth.PathServerMetadata}, {hosts.MCP, "GET", oauth.PathAPI + "/connection"},
		{hosts.API, "POST", oauth.PathMCP}, {hosts.API, "GET", oauth.PathResourceMetadata + oauth.PathMCP}, {hosts.API, "GET", "/api/v1/connections"},
		{hosts.Inbox, "POST", oauth.PathToken}, {hosts.Inbox, "POST", oauth.PathMCP},
		{hosts.API, "GET", "/.well-known/openid-configuration"}, {hosts.MCP, "GET", "/.well-known/openid-configuration"},
	} {
		if rec := ask(r.host, r.method, r.path, ""); rec.Code != 404 {
			t.Errorf("%s %s%s: %d %s", r.method, r.host, r.path, rec.Code, rec.Body)
		}
	}
	if rec := ask(hosts.App, "POST", oauth.PathToken, ""); jsonOf(rec)["error"] != nil {
		t.Errorf("the token endpoint answered on the app's host: %d %s", rec.Code, rec.Body)
	}
	// An odd path is 404 before any mux, never a redirect carrying its query.
	for _, r := range []struct{ host, method, target string }{
		{hosts.API, "POST", "/oauth//token?code=pmc_odd"}, {hosts.API, "GET", "/v1/./connection?x=pma_odd"},
		{hosts.MCP, "POST", "//mcp?token=pma_odd"}, {hosts.MCP, "GET", "/.well-known/../.well-known/oauth-protected-resource?x=1"},
		{hosts.API, "GET", "/v1/admin/../admin/accounts?x=1"},
	} {
		if rec := ask(r.host, r.method, r.target, ""); rec.Code != 404 || rec.Header().Get("Location") != "" || strings.Contains(rec.Body.String(), "odd") {
			t.Errorf("%s %s%s: %d %v", r.method, r.host, r.target, rec.Code, rec.Header())
		}
	}
}
