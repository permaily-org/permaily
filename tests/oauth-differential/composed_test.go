package differential

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/oauth"
)

// The composed service (ADR 0030, V60): the enclave's own composition,
// cmd/permaily-enclave built in dev mode from this checkout, run as a
// process with its vendors played here (-dev-vendors) and nothing else
// reached, and driven by golang.org/x/oauth2 and the MCP Go SDK's auth
// client through the code flow, a refresh and its retry, and a
// revocation. V12 drives the same clients against a minimal server made of
// internal/oauth's functions; this drives them against the image's.

const (
	composedDomain = "permaily.test"
	composedApp    = "https://apps.differential-vendor.dev/permaily/client.json"
	composedAgent  = "https://agents.differential-vendor.dev/mcp/client.json"
	composedAdmin  = "pma_differential_admin"
)

// composed is the running service and the clients that talk to it.
type composed struct {
	t        *testing.T
	port     int
	cfg      oauth.Config
	client   *http.Client // a vendor's: no cookie, h2 offered
	browser  *http.Client // the person's: cookies
	mailbox  string
	logs     *lockedBuffer
	vendorMu sync.Mutex
	asked    []string
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// playVendors serves the clients' documents over TLS with HTTP/2 under an
// authority of this test, and writes that authority where -dev-vendors-ca
// reads it.
func (c *composed) playVendors(dir string) string {
	t := c.t
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "played vendors"}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "differential-vendor.dev"},
		DNSNames:  []string{"apps.differential-vendor.dev", "agents.differential-vendor.dev"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	docs := map[string]string{}
	for id, name := range map[string]string{composedApp: "Differential App", composedAgent: "Differential Agent"} {
		u, _ := url.Parse(id)
		docs[u.Host+u.Path] = `{"client_id":"` + id + `","client_name":"` + name + `","redirect_uris":["http://127.0.0.1/callback"],"token_endpoint_auth_method":"none"}`
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.vendorMu.Lock()
		c.asked = append(c.asked, r.Proto+" "+r.Host+r.URL.Path)
		c.vendorMu.Unlock()
		doc, ok := docs[r.Host+r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = io.WriteString(w, doc)
	}))
	srv.EnableHTTP2 = true
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	caFile := filepath.Join(dir, "vendors-ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return srv.Listener.Addr().String() + " " + caFile
}

// startComposed builds cmd/permaily-enclave in dev mode and runs it.
func startComposed(t *testing.T) *composed {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and runs the enclave")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "permaily-enclave")
	build := exec.Command("go", "build", "-tags", "dev", "-o", bin, "./cmd/permaily-enclave")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the enclave: %v\n%s", err, out)
	}
	c := &composed{t: t, port: freePort(t), logs: &lockedBuffer{}}
	vendors := strings.Fields(c.playVendors(dir))
	sum := sha256.Sum256([]byte(composedAdmin))
	ctx, cancel := context.WithCancel(context.Background())
	run := exec.CommandContext(ctx, bin, "-domain", composedDomain, "-acme-directory", "unused", "-port", fmt.Sprint(c.port), "-smtp-port", fmt.Sprint(freePort(t)),
		"-admin-token-sha256", hex.EncodeToString(sum[:]), "-dev-vendors", vendors[0], "-dev-vendors-ca", vendors[1])
	run.Stdout, run.Stderr = c.logs, c.logs
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = run.Wait() })
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", c.port))
	}
	tlsCfg := &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2", "http/1.1"}} //nolint:gosec // dev mode's own certificate
	c.client = &http.Client{Transport: &http.Transport{DialContext: dial, TLSClientConfig: tlsCfg, ForceAttemptHTTP2: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	jar, _ := cookiejar.New(nil)
	c.browser = &http.Client{Jar: jar, Transport: &http.Transport{DialContext: dial, TLSClientConfig: tlsCfg.Clone(), ForceAttemptHTTP2: true}}
	host := func(sub string) string { return fmt.Sprintf("https://%s.%s:%d", sub, composedDomain, c.port) }
	c.cfg = oauth.Config{Issuer: host("api"), AppOrigin: host("app"), APIResource: host("api") + oauth.PathAPI, MCPResource: host("mcp") + oauth.PathMCP}
	for i := 0; ; i++ {
		if res, err := c.client.Get(c.cfg.Issuer + oauth.PathServerMetadata); err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				break
			}
		}
		if i == 300 {
			t.Fatalf("the enclave did not start:\n%s", c.logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.signUp()
	return c
}

// app asks the app's host as its own page does.
func (c *composed) app(method, path, body string) (int, string) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.cfg.AppOrigin+path, strings.NewReader(body))
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if method != http.MethodGet {
		req.Header.Set("Origin", c.cfg.AppOrigin)
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.browser.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// signUp makes a person, signed in by a code (a fresh proof), with one
// mailbox.
func (c *composed) signUp() {
	t := c.t
	req, _ := http.NewRequest(http.MethodPost, c.cfg.Issuer+"/v1/admin/invitations", strings.NewReader(`{"count":1}`))
	req.Header.Set("Authorization", "Bearer "+composedAdmin)
	res, err := c.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var made struct{ Invitations []string }
	_ = json.NewDecoder(res.Body).Decode(&made)
	res.Body.Close()
	if len(made.Invitations) != 1 {
		t.Fatalf("no invitation: %d", res.StatusCode)
	}
	const address = "differential@example.org"
	var started struct{ Attempt string }
	if code, body := c.app("POST", "/api/v1/auth/email/start", `{"address":"`+address+`","invitation":"`+made.Invitations[0]+`","terms":"`+domain.TermsVersion+`"}`); code != 200 || json.Unmarshal([]byte(body), &started) != nil {
		t.Fatalf("start: %d %s", code, body)
	}
	digits := regexp.MustCompile(`\n    (\d{6})\n`)
	var signInCode string
	for i := 0; signInCode == ""; i++ {
		if i == 100 {
			t.Fatal("no sign-in message")
		}
		time.Sleep(50 * time.Millisecond)
		_, body := c.app("GET", "/dev/outbox", "")
		var sent []struct{ To, Text string }
		_ = json.Unmarshal([]byte(body), &sent)
		for _, m := range sent {
			if g := digits.FindStringSubmatch(m.Text); g != nil && m.To == address && strings.Contains(m.Text, started.Attempt) {
				signInCode = g[1]
			}
		}
	}
	if code, body := c.app("POST", "/api/v1/auth/email/finish", `{"attempt":"`+started.Attempt+`","code":"`+signInCode+`"}`); code != 200 {
		t.Fatalf("finish: %d %s", code, body)
	}
	var box contracts.Mailbox
	if code, body := c.app("POST", "/api/v1/mailboxes", `{"label":"Differential"}`); code != 201 || json.Unmarshal([]byte(body), &box) != nil {
		t.Fatalf("a mailbox: %d %s", code, body)
	}
	c.mailbox = box.ID
}

// handOff is the person's side of an authorization request the vendor sent
// the browser to: the app posts its query string, shows the screen, and
// the person allows the draft it starts from; the answer is where the
// browser goes back to.
func (c *composed) handOff(authURL string) url.Values {
	t := c.t
	u, err := url.Parse(authURL)
	if err != nil || u.Scheme+"://"+u.Host+u.Path != c.cfg.AppOrigin+oauth.PathAuthorize {
		t.Fatalf("the vendor sent the browser to %q", authURL)
	}
	body, _ := json.Marshal(contracts.AuthorizationStart{Query: u.RawQuery})
	var started contracts.AuthorizationStarted
	if code, out := c.app("POST", "/api/v1/oauth/requests", string(body)); code != 200 || json.Unmarshal([]byte(out), &started) != nil || started.Request == nil {
		t.Fatalf("the request: %d %s", code, out)
	}
	r := started.Request
	decision, _ := json.Marshal(contracts.AuthorizationDecision{Allow: true, Draft: &r.Draft, ModelNotice: r.ModelNotice, Snapshot: &r.Snapshot})
	var answer contracts.AuthorizationAnswer
	if code, out := c.app("POST", "/api/v1/oauth/requests/"+r.ID+"/decision", string(decision)); code != 200 || json.Unmarshal([]byte(out), &answer) != nil {
		t.Fatalf("the decision: %d %s", code, out)
	}
	back, err := url.Parse(answer.Redirect)
	if err != nil {
		t.Fatal(err)
	}
	return back.Query()
}

func (c *composed) get(rawURL, token string) (int, string, string) {
	c.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := c.client.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header.Get("WWW-Authenticate"), string(b)
}

// V60, x/oauth2 against the composed service: the code flow with PKCE and
// the resource, the code once, the API with the token (and not MCP), a
// refresh and its retry within 30 seconds answering the same pair, the
// vendor's revocation of its refresh token ending its device and its
// access token at once; and the log holds no token, code or verifier.
func TestV60XOAuth2AgainstTheComposedService(t *testing.T) {
	c := startComposed(t)
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, c.client)
	conf := &oauth2.Config{
		ClientID: composedApp, RedirectURL: "http://127.0.0.1:53682/callback",
		Scopes:   []string{oauth.ScopeSender, oauth.ScopeSubject, oauth.ScopeBody},
		Endpoint: oauth2.Endpoint{AuthURL: c.cfg.AppOrigin + oauth.PathAuthorize, TokenURL: c.cfg.Issuer + oauth.PathToken, AuthStyle: oauth2.AuthStyleInParams},
	}
	verifier := oauth2.GenerateVerifier()
	resource := oauth2.SetAuthURLParam("resource", c.cfg.APIResource)
	back := c.handOff(conf.AuthCodeURL("the-state", oauth2.S256ChallengeOption(verifier), resource))
	if back.Get("state") != "the-state" || back.Get("iss") != c.cfg.Issuer || !strings.HasPrefix(back.Get("code"), oauth.PrefixCode) {
		t.Fatalf("%v", back)
	}
	tok, err := conf.Exchange(ctx, back.Get("code"), oauth2.VerifierOption(verifier), resource)
	if err != nil {
		t.Fatal(err)
	}
	if tok.TokenType != "Bearer" || tok.Extra("scope") != "mail.sender mail.subject mail.body" || !strings.HasPrefix(tok.AccessToken, oauth.PrefixAccess) || !strings.HasPrefix(tok.RefreshToken, oauth.PrefixRefresh) {
		t.Fatalf("%+v", tok)
	}
	if status, _, body := c.get(c.cfg.APIResource+"/connection", tok.AccessToken); status != http.StatusOK || !strings.Contains(body, `"parts":["sender","subject","body"]`) {
		t.Fatalf("the connection: %d %s", status, body)
	}
	if status, challenge, _ := c.get(c.cfg.MCPResource, tok.AccessToken); status != http.StatusUnauthorized || !strings.Contains(challenge, `error="invalid_token"`) {
		t.Fatalf("the API's token at MCP: %d %s", status, challenge)
	}
	// A refresh through the library, then its retry: the same pair.
	first := tok.RefreshToken
	expired := &oauth2.Token{RefreshToken: first, Expiry: time.Now().Add(-time.Minute)}
	rotated, err := conf.TokenSource(ctx, expired).Token()
	if err != nil || rotated.RefreshToken == first || rotated.AccessToken == tok.AccessToken {
		t.Fatalf("the refresh: %+v %v", rotated, err)
	}
	again, err := conf.TokenSource(ctx, &oauth2.Token{RefreshToken: first, Expiry: expired.Expiry}).Token()
	if err != nil || again.AccessToken != rotated.AccessToken || again.RefreshToken != rotated.RefreshToken {
		t.Fatalf("the retry: %+v %v", again, err)
	}
	if status, _, _ := c.get(c.cfg.APIResource+"/connection", rotated.AccessToken); status != http.StatusOK {
		t.Fatalf("the refreshed token: %d", status)
	}
	// The vendor revokes its refresh token (RFC 7009): 200, and its device
	// ends at once, its access token with it.
	form := url.Values{"token": {rotated.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {composedApp}}
	res, err := c.client.PostForm(c.cfg.Issuer+oauth.PathRevoke, form)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the revocation: %d", res.StatusCode)
	}
	if status, challenge, _ := c.get(c.cfg.APIResource+"/connection", rotated.AccessToken); status != http.StatusUnauthorized || !strings.Contains(challenge, `error="invalid_token"`) {
		t.Fatalf("a revoked device's token: %d %s", status, challenge)
	}
	if _, err := conf.TokenSource(ctx, &oauth2.Token{RefreshToken: rotated.RefreshToken, Expiry: expired.Expiry}).Token(); errorCode(err) != "invalid_grant" {
		t.Fatalf("a revoked device's refresh: %v", err)
	}
	// A token the service does not know is revoked with 200, as RFC 7009 says.
	if res, err := c.client.PostForm(c.cfg.Issuer+oauth.PathRevoke, url.Values{"token": {"pma_unknown"}, "client_id": {composedApp}}); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("an unknown token: %v", err)
	}
	c.logHoldsNone(tok.AccessToken, tok.RefreshToken, rotated.AccessToken, rotated.RefreshToken, back.Get("code"), verifier, "the-state", composedApp)
}

// V60, the MCP SDK's auth client against the composed service: from the
// resource's 401, it reads the resource's and the server's metadata, sends
// the browser to the authorization page with PKCE, the resource and the
// default scopes, takes the code with its state and iss, and gets tokens
// that MCP takes over http/1.1 though h2 was offered; its configuration
// refreshes as x/oauth2 does, and the vendor's revocation ends the device.
func TestV60TheMCPSDKsClientAgainstTheComposedService(t *testing.T) {
	c := startComposed(t)
	var made *oauth2.Config
	var first *oauth2.Token
	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		ClientIDMetadataDocumentConfig: &auth.ClientIDMetadataDocumentConfig{URL: composedAgent},
		RedirectURL:                    "http://127.0.0.1:4242/callback",
		Client:                         c.client,
		AuthorizationCodeFetcher: func(_ context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			u, _ := url.Parse(args.URL)
			q := u.Query()
			if q.Get("resource") != c.cfg.MCPResource || q.Get("code_challenge_method") != "S256" || !sameScopes(q.Get("scope"), "mail.sender mail.subject mail.body") {
				t.Errorf("the SDK asked %v", q)
			}
			back := c.handOff(args.URL)
			return &auth.AuthorizationResult{Code: back.Get("code"), State: back.Get("state"), Iss: back.Get("iss")}, nil
		},
		NewTokenSource: func(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			made, first = cfg, tok
			return cfg.TokenSource(ctx, tok), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	call := func(token, body string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodPost, c.cfg.MCPResource, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := c.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, string(b)
	}
	const info = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"connection_info","arguments":{}}}`
	req, _ := http.NewRequest(http.MethodPost, c.cfg.MCPResource, strings.NewReader(info))
	resp, _ := call("", info)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a token: %d", resp.StatusCode)
	}
	if err := handler.Authorize(context.Background(), req, resp); err != nil {
		t.Fatal(err)
	}
	ts, err := handler.TokenSource(context.Background())
	if err != nil || ts == nil {
		t.Fatal(err)
	}
	tok, err := ts.Token()
	if err != nil {
		t.Fatal(err)
	}
	if made.ClientID != composedAgent || made.Endpoint.TokenURL != c.cfg.Issuer+oauth.PathToken {
		t.Fatalf("%+v", made)
	}
	res, body := call(tok.AccessToken, info)
	if res.StatusCode != http.StatusOK || res.Proto != "HTTP/1.1" || !strings.Contains(body, `\"parts\":[\"sender\",\"subject\",\"body\"]`) {
		t.Fatalf("connection_info: %s %d %s", res.Proto, res.StatusCode, body)
	}
	refreshed, err := made.TokenSource(context.WithValue(context.Background(), oauth2.HTTPClient, c.client), &oauth2.Token{RefreshToken: first.RefreshToken, Expiry: time.Now().Add(-time.Hour)}).Token()
	if err != nil || refreshed.AccessToken == tok.AccessToken {
		t.Fatalf("%+v %v", refreshed, err)
	}
	if res, _ := call(refreshed.AccessToken, info); res.StatusCode != http.StatusOK {
		t.Fatal(res.StatusCode)
	}
	revoke, err := c.client.PostForm(c.cfg.Issuer+oauth.PathRevoke, url.Values{"token": {refreshed.AccessToken}, "client_id": {composedAgent}})
	if err != nil || revoke.StatusCode != http.StatusOK {
		t.Fatalf("the revocation: %v", err)
	}
	revoke.Body.Close()
	if res, _ := call(refreshed.AccessToken, info); res.StatusCode != http.StatusUnauthorized || !strings.Contains(res.Header.Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatalf("after the revocation: %d", res.StatusCode)
	}
	c.logHoldsNone(tok.AccessToken, tok.RefreshToken, refreshed.AccessToken, first.RefreshToken, composedAgent)
}

// logHoldsNone checks the composed service's log holds none of secrets
// (ADR 0030, "Logging"), and that every vendor fetch was the played one's
// over HTTP/2.
func (c *composed) logHoldsNone(secrets ...string) {
	c.t.Helper()
	out := c.logs.String()
	for _, s := range append(secrets, "differential@", c.mailbox, "differential-vendor") {
		if s != "" && strings.Contains(out, s) {
			c.t.Errorf("the log holds %q", s)
		}
	}
	c.vendorMu.Lock()
	defer c.vendorMu.Unlock()
	if len(c.asked) == 0 {
		c.t.Error("no document was fetched")
	}
	for _, a := range c.asked {
		if !strings.HasPrefix(a, "HTTP/2.0 ") {
			c.t.Errorf("a vendor was asked %q", a)
		}
	}
}
