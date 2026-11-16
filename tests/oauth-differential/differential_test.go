package differential

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"

	"github.com/permaily-org/permaily/internal/jws"
	"github.com/permaily-org/permaily/internal/oauth"
)

const (
	publicClient  = "https://vendor.example.com/oauth/client.json"
	privateClient = "https://agent.example.com/oauth/client.json"
	loopback      = "http://127.0.0.1/callback"
)

// clock is the played time every party is told.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

type world struct {
	t      *testing.T
	clock  *clock
	server *Server
	http   *httptest.Server
	client *http.Client
	signer *rsa.PrivateKey
}

// newWorld starts the minimal server over TLS with a public client (Claude
// Code's shape: loopback, none) and a private_key_jwt one (ChatGPT's shape).
func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, clock: &clock{now: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}}
	ts := httptest.NewUnstartedServer(nil)
	ts.StartTLS()
	t.Cleanup(ts.Close)
	c := oauth.Config{Issuer: ts.URL, AppOrigin: ts.URL, APIResource: ts.URL + oauth.PathAPI, MCPResource: ts.URL + oauth.PathMCP}
	w.server = NewServer(c, w.clock.Now)
	ts.Config.Handler = w.server
	w.http = ts
	w.client = ts.Client()
	w.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	pub, err := oauth.Document(publicClient, []byte(`{"client_id":"`+publicClient+`","client_name":"Vendor","redirect_uris":["`+loopback+`"],"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"none"}`))
	if err != nil {
		t.Fatal(err)
	}
	w.server.Documents[publicClient] = pub
	priv, err := oauth.Document(privateClient, []byte(`{"client_id":"`+privateClient+`","redirect_uris":["https://agent.example.com/callback"],"token_endpoint_auth_method":"private_key_jwt","token_endpoint_auth_signing_alg":"RS256","jwks_uri":"https://agent.example.com/oauth/jwks.json"}`))
	if err != nil {
		t.Fatal(err)
	}
	w.server.Documents[privateClient] = priv
	if w.signer, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	w.server.Keys[privateClient] = []jws.Key{{ID: "k1", Algorithm: jws.RS256, Public: &w.signer.PublicKey}}
	return w
}

func (w *world) ctx() context.Context {
	return context.WithValue(context.Background(), oauth2.HTTPClient, w.client)
}

func (w *world) config(clientID, redirect string, scopes ...string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:    clientID,
		RedirectURL: redirect,
		Scopes:      scopes,
		Endpoint:    oauth2.Endpoint{AuthURL: w.http.URL + oauth.PathAuthorize, TokenURL: w.http.URL + oauth.PathToken, AuthStyle: oauth2.AuthStyleInParams},
	}
}

// follow sends the browser to an authorization URL and reads where the
// server sends it back.
func (w *world) follow(authURL string) url.Values {
	w.t.Helper()
	resp, err := w.client.Get(authURL)
	if err != nil {
		w.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		w.t.Fatalf("authorize answered %d", resp.StatusCode)
	}
	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		w.t.Fatal(err)
	}
	return back.Query()
}

func errorCode(err error) string {
	var r *oauth2.RetrieveError
	if errors.As(err, &r) {
		return r.ErrorCode
	}
	return ""
}

// get asks a resource with a token and returns the status and body.
func (w *world) get(path, token string) (int, string, string) {
	w.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, w.http.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := w.client.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("WWW-Authenticate"), string(b)
}

// V12, x/oauth2: the whole code flow with PKCE, the refresh and its retry,
// and the refusals, as the library reads each answer.
func TestV12XOAuth2ThroughTheCodeFlowAndRefresh(t *testing.T) {
	w := newWorld(t)
	ctx := w.ctx()
	conf := w.config(publicClient, "http://127.0.0.1:53682/callback", oauth.ScopeSubject, oauth.ScopeBody)
	verifier := oauth2.GenerateVerifier()
	resource := oauth2.SetAuthURLParam("resource", w.server.Config.MCPResource)
	back := w.follow(conf.AuthCodeURL("the-state", oauth2.S256ChallengeOption(verifier), resource))
	if back.Get("state") != "the-state" || back.Get("iss") != w.server.Config.Issuer || !strings.HasPrefix(back.Get("code"), oauth.PrefixCode) {
		t.Fatalf("%v", back)
	}

	// A wrong verifier spends the code: the library reads invalid_grant,
	// and the right one after it is refused too.
	code := back.Get("code")
	if _, err := conf.Exchange(ctx, code, oauth2.VerifierOption(oauth2.GenerateVerifier()), resource); errorCode(err) != "invalid_grant" {
		t.Fatalf("a wrong verifier: %v", err)
	}
	if _, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier), resource); errorCode(err) != "invalid_grant" {
		t.Fatalf("a spent code: %v", err)
	}

	back = w.follow(conf.AuthCodeURL("s2", oauth2.S256ChallengeOption(verifier), resource))
	tok, err := conf.Exchange(ctx, back.Get("code"), oauth2.VerifierOption(verifier), resource)
	if err != nil {
		t.Fatal(err)
	}
	fam := w.server.families
	var family string
	for f := range fam {
		family = f
	}
	if tok.AccessToken != oauth.FamilySecret(w.server.key, oauth.KindAccess, family, 0) || tok.RefreshToken != oauth.FamilySecret(w.server.key, oauth.KindRefresh, family, 0) ||
		tok.TokenType != "Bearer" || tok.ExpiresIn != 3600 || tok.Extra("scope") != "mail.sender mail.subject mail.body" {
		t.Fatalf("%+v %v", tok, tok.Extra("scope"))
	}
	// The code again: refused, and the family it made is ended.
	if _, err := conf.Exchange(ctx, back.Get("code"), oauth2.VerifierOption(verifier), resource); errorCode(err) != "invalid_grant" {
		t.Fatalf("a code used twice: %v", err)
	}
	if status, _, _ := w.get(oauth.PathMCP, tok.AccessToken); status != http.StatusUnauthorized {
		t.Fatalf("a code's reuse left its family: %d", status)
	}

	// A new grant; refresh through the library, then the retry within the
	// grace answers the same pair, and a reuse past it ends the family.
	back = w.follow(conf.AuthCodeURL("s3", oauth2.S256ChallengeOption(verifier), resource))
	tok, err = conf.Exchange(ctx, back.Get("code"), oauth2.VerifierOption(verifier), resource)
	if err != nil {
		t.Fatal(err)
	}
	if status, _, body := w.get(oauth.PathMCP, tok.AccessToken); status != http.StatusOK || !strings.Contains(body, `"parts":["sender","subject","body"]`) {
		t.Fatalf("%d %s", status, body)
	}
	if status, challenge, _ := w.get(oauth.PathAPI, tok.AccessToken); status != http.StatusUnauthorized || !strings.Contains(challenge, `error="invalid_token"`) {
		t.Fatalf("a token of the other resource: %d %s", status, challenge)
	}
	first := tok.RefreshToken
	expired := &oauth2.Token{RefreshToken: first, Expiry: w.clock.now.Add(-time.Minute)}
	w.clock.now = w.clock.now.Add(time.Second)
	rotated, err := conf.TokenSource(ctx, expired).Token()
	if err != nil {
		t.Fatal(err)
	}
	if rotated.RefreshToken == first || rotated.AccessToken == tok.AccessToken || rotated.Extra("scope") != "mail.sender mail.subject mail.body" {
		t.Fatalf("%+v", rotated)
	}
	w.clock.now = w.clock.now.Add(oauth.RefreshRetryGrace - 2*time.Second)
	again, err := conf.TokenSource(ctx, &oauth2.Token{RefreshToken: first, Expiry: expired.Expiry}).Token()
	if err != nil || again.AccessToken != rotated.AccessToken || again.RefreshToken != rotated.RefreshToken {
		t.Fatalf("the retry: %+v %v", again, err)
	}
	// A narrower scope on refresh (Codex sends one; x/oauth2 sends none) is
	// the access token's; a wider one is invalid_scope, and the family is
	// left as it was.
	narrowed := w.refresh(rotated.RefreshToken, oauth.ScopeSender)
	if narrowed.Scope != "mail.sender" {
		t.Fatalf("narrowed: %+v", narrowed)
	}
	if _, _, body := w.get(oauth.PathMCP, narrowed.AccessToken); !strings.Contains(body, `"parts":["sender"]`) {
		t.Fatal(body)
	}
	if wider := w.refresh(narrowed.RefreshToken, oauth.ScopeAttachments); wider.Error != "invalid_scope" {
		t.Fatalf("wider: %+v", wider)
	}
	// The first refresh token past the grace: a reuse, which ends the family.
	w.clock.now = w.clock.now.Add(time.Minute)
	if _, err := conf.TokenSource(ctx, &oauth2.Token{RefreshToken: first, Expiry: expired.Expiry}).Token(); errorCode(err) != "invalid_grant" {
		t.Fatalf("a reuse: %v", err)
	}
	if _, err := conf.TokenSource(ctx, &oauth2.Token{RefreshToken: narrowed.RefreshToken, Expiry: expired.Expiry}).Token(); errorCode(err) != "invalid_grant" {
		t.Fatalf("the family's newest after a reuse: %v", err)
	}

	// The refusals of the authorization request, as the library sends them.
	for name, c := range map[string]struct {
		url  string
		want string
	}{
		"plain PKCE":       {conf.AuthCodeURL("s", oauth2.SetAuthURLParam("code_challenge", verifier), oauth2.SetAuthURLParam("code_challenge_method", "plain"), resource), "invalid_request"},
		"no PKCE":          {conf.AuthCodeURL("s", resource), "invalid_request"},
		"no resource":      {conf.AuthCodeURL("s", oauth2.S256ChallengeOption(verifier)), "invalid_target"},
		"an unknown scope": {w.config(publicClient, conf.RedirectURL, "openid").AuthCodeURL("s", oauth2.S256ChallengeOption(verifier), resource), "invalid_scope"},
		"implicit":         {strings.Replace(conf.AuthCodeURL("s", oauth2.S256ChallengeOption(verifier), resource), "response_type=code", "response_type=token", 1), "unsupported_response_type"},
	} {
		back := w.follow(c.url)
		if back.Get("error") != c.want || back.Get("state") != "s" || back.Get("iss") != w.server.Config.Issuer || back.Get("code") != "" {
			t.Errorf("%s: %v", name, back)
		}
	}
	// A redirect URI not the document's is never redirected to.
	resp, err := w.client.Get(w.config(publicClient, "http://localhost:1/callback").AuthCodeURL("s", oauth2.S256ChallengeOption(verifier), resource))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("Location") != "" {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	// An unsupported grant, as the library reads it.
	if _, err := conf.Exchange(ctx, "x", oauth2.SetAuthURLParam("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")); errorCode(err) != "unsupported_grant_type" {
		t.Fatalf("jwt-bearer: %v", err)
	}
}

// answered is a token endpoint's answer as RFC 6749 writes it.
type answered struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

// refresh posts a refresh with a scope, as Codex does.
func (w *world) refresh(token, scope string) answered {
	w.t.Helper()
	resp, err := w.client.PostForm(w.http.URL+oauth.PathToken, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}, "client_id": {publicClient}, "scope": {scope}})
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	var a answered
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		w.t.Fatal(err)
	}
	return a
}

// assertion signs a client assertion of privateClient.
func (w *world) assertion(jti string) string {
	w.t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"k1"}`))
	c, _ := json.Marshal(map[string]any{"iss": privateClient, "sub": privateClient, "aud": w.server.Config.Issuer + oauth.PathToken, "jti": jti, "iat": w.clock.now.Unix(), "exp": w.clock.now.Add(5 * time.Minute).Unix()})
	signed := header + "." + base64.RawURLEncoding.EncodeToString(c)
	h := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(nil, w.signer, crypto.SHA256, h[:])
	if err != nil {
		w.t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// V12, private_key_jwt through x/oauth2: the assertion is required, taken
// once, and a replay is invalid_client.
func TestV12XOAuth2WithAClientAssertion(t *testing.T) {
	w := newWorld(t)
	ctx := w.ctx()
	conf := w.config(privateClient, "https://agent.example.com/callback", oauth.ScopeBody)
	verifier := oauth2.GenerateVerifier()
	resource := oauth2.SetAuthURLParam("resource", w.server.Config.APIResource)
	assertionType := oauth2.SetAuthURLParam("client_assertion_type", oauth.AssertionType)

	code := w.follow(conf.AuthCodeURL("s", oauth2.S256ChallengeOption(verifier), resource)).Get("code")
	if _, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier), resource); errorCode(err) != "invalid_client" {
		t.Fatalf("no assertion: %v", err)
	}
	a := w.assertion("jti-1")
	tok, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier), resource, assertionType, oauth2.SetAuthURLParam("client_assertion", a))
	if err != nil {
		t.Fatal(err)
	}
	if status, _, body := w.get(oauth.PathAPI, tok.AccessToken); status != http.StatusOK || !strings.Contains(body, `"parts":["sender","body"]`) {
		t.Fatalf("%d %s", status, body)
	}
	code = w.follow(conf.AuthCodeURL("s", oauth2.S256ChallengeOption(verifier), resource)).Get("code")
	if _, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier), resource, assertionType, oauth2.SetAuthURLParam("client_assertion", a)); errorCode(err) != "invalid_client" {
		t.Fatalf("a replayed assertion: %v", err)
	}
	if _, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier), resource, assertionType, oauth2.SetAuthURLParam("client_assertion", w.assertion("jti-2"))); err != nil {
		t.Fatalf("a new assertion: %v", err)
	}
}

// V12, the MCP SDK's client: a 401 with the challenge leads it through the
// resource's metadata, the server's, the client's document (CIMD), PKCE,
// resource and iss to a token the resource takes; then the SDK's own token
// source refreshes it.
func TestV12TheMCPSDKsClientThroughTheCodeFlow(t *testing.T) {
	w := newWorld(t)
	var made *oauth2.Config
	var first *oauth2.Token
	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		ClientIDMetadataDocumentConfig: &auth.ClientIDMetadataDocumentConfig{URL: publicClient},
		RedirectURL:                    "http://127.0.0.1:4242/callback",
		Client:                         w.client,
		AuthorizationCodeFetcher: func(_ context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			u, _ := url.Parse(args.URL)
			q := u.Query()
			if q.Get("resource") != w.server.Config.MCPResource || q.Get("code_challenge_method") != "S256" || !sameScopes(q.Get("scope"), "mail.sender mail.subject mail.body") {
				t.Errorf("the SDK asked %v", q)
			}
			back := w.follow(args.URL)
			return &auth.AuthorizationResult{Code: back.Get("code"), State: back.Get("state"), Iss: back.Get("iss")}, nil
		},
		NewTokenSource: func(ctx context.Context, c *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			made, first = c, tok
			return c.TokenSource(ctx, tok), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, w.http.URL+oauth.PathMCP, bytes.NewReader([]byte(`{}`)))
	resp, err := w.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("%d", resp.StatusCode)
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
	if status, _, body := w.get(oauth.PathMCP, tok.AccessToken); status != http.StatusOK || !strings.Contains(body, `"scopes":["mail.sender","mail.subject","mail.body"]`) {
		t.Fatalf("%d %s", status, body)
	}
	if made.ClientID != publicClient || made.Endpoint.TokenURL != w.server.Config.Issuer+oauth.PathToken {
		t.Fatalf("%+v", made)
	}
	// The SDK's configuration refreshes as x/oauth2 does.
	refreshed, err := made.TokenSource(w.ctx(), &oauth2.Token{RefreshToken: first.RefreshToken, Expiry: w.clock.now.Add(-time.Hour)}).Token()
	if err != nil || refreshed.AccessToken == tok.AccessToken {
		t.Fatalf("%+v %v", refreshed, err)
	}
	if status, _, _ := w.get(oauth.PathMCP, refreshed.AccessToken); status != http.StatusOK {
		t.Fatal(status)
	}
}

// sameScopes says two scope parameters hold the same scopes, in any order
// (the SDK sends the union it keeps, in its own order).
func sameScopes(a, b string) bool {
	x, y := strings.Fields(a), strings.Fields(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// vector is one of internal/oauth's assertion vectors (V8).
type vector struct {
	Name        string `json:"name"`
	DocumentAlg string `json:"document_alg"`
	Header      string `json:"header"`
	Claims      string `json:"claims"`
	Signature   string `json:"signature"`
	Want        string `json:"want"`
}

func (v vector) compact() string {
	return base64.RawURLEncoding.EncodeToString([]byte(v.Header)) + "." + base64.RawURLEncoding.EncodeToString([]byte(v.Claims)) + "." + v.Signature
}

// differs names each case where internal/jws refuses what go-jose takes,
// with why; it never takes what go-jose refuses.
var differs = map[string]string{
	"rsa key of 1024 bits": "go-jose verifies with a key of any size; an RSA key under 2048 bits verifies nothing here",
}

// V12, internal/jws against go-jose on internal/oauth's vectors and on the
// same tokens broken byte by byte: jws never accepts what go-jose refuses,
// and where it refuses what go-jose accepts the case says why.
func TestV12JWSAgainstGoJose(t *testing.T) {
	dir := filepath.Join("..", "..", "internal", "oauth", "testdata", "assertions")
	keySet, err := os.ReadFile(filepath.Join(dir, "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	ours, err := jws.Keys(keySet)
	if err != nil {
		t.Fatal(err)
	}
	var theirs jose.JSONWebKeySet
	if err := json.Unmarshal(keySet, &theirs); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors []vector
	if err := json.Unmarshal(b, &vectors); err != nil {
		t.Fatal(err)
	}
	type tcase struct{ name, token string }
	var cases []tcase
	for _, v := range vectors {
		cases = append(cases, tcase{v.Name, v.compact()})
		if v.Want != "" {
			continue
		}
		// Every byte of a valid one changed, one at a time.
		c := v.compact()
		for i := 0; i < len(c); i++ {
			if c[i] == '.' {
				continue
			}
			mutated := []byte(c)
			mutated[i] ^= 0x01
			cases = append(cases, tcase{v.Name + " mutated", string(mutated)})
		}
		cases = append(cases, tcase{v.Name + " padded", c + "="}, tcase{v.Name + " cut", c[:len(c)-1]})
	}
	differed := map[string]bool{}
	for _, c := range cases {
		weAccept := verifyOurs(c.token, ours) == nil
		theyAccept := verifyTheirs(c.token, theirs) == nil
		switch {
		case weAccept && !theyAccept:
			t.Errorf("%s: accepted here, refused by go-jose", c.name)
		case !weAccept && theyAccept:
			if differs[c.name] == "" {
				t.Errorf("%s: refused here, accepted by go-jose, with no reason given", c.name)
			}
			differed[c.name] = true
		}
	}
	for name := range differs {
		if !differed[name] {
			t.Errorf("%s: named as a difference, but the two agree", name)
		}
	}
}

func verifyOurs(token string, keys []jws.Key) error {
	t, err := jws.Parse(token)
	if err != nil {
		return err
	}
	return jws.Verify(t, keys)
}

func verifyTheirs(token string, set jose.JSONWebKeySet) error {
	sig, err := jose.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
	if err != nil {
		return err
	}
	if len(sig.Signatures) != 1 {
		return errors.New("not one signature")
	}
	keys := set.Key(sig.Signatures[0].Header.KeyID)
	if len(keys) != 1 {
		return errors.New("no key")
	}
	_, err = sig.Verify(keys[0])
	return err
}
