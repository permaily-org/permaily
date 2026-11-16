package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/connections/connectionstest"
)

// The routes of connections (ADR 0030, pull request 6), against
// connectionstest.Fake and connectionstest.Lease, mounted as the composition
// mounts them: the app's host inside AppHost, the api and mcp hosts, all
// behind the router (its security headers).

const (
	apiHost = "api.permaily.test"
	mcpHost = "mcp.permaily.test"
	appHost = "app.permaily.test"
)

var testOAuth = oauth.Config{
	Issuer: "https://" + apiHost, AppOrigin: "https://" + appHost,
	APIResource: "https://" + apiHost + "/v1", MCPResource: "https://" + mcpHost + "/mcp",
}

// The documents the image publishes are internal/oauth's (pull request 1);
// these stand for them, written from ADR 0030's "Metadata" and "Errors and
// challenges", so the goldens below are what the routes serve of them.
func testMetadata(c oauth.Config) oauth.ServerMetadata {
	return oauth.ServerMetadata{
		Issuer: c.Issuer, AuthorizationEndpoint: c.AppOrigin + oauth.PathAuthorize, TokenEndpoint: c.Issuer + oauth.PathToken,
		RevocationEndpoint: c.Issuer + oauth.PathRevoke, PushedAuthorizationRequestEndpoint: c.Issuer + oauth.PathPushed,
		ResponseTypesSupported: []string{"code"}, ResponseModesSupported: []string{"query"},
		GrantTypesSupported: []string{"authorization_code", "refresh_token"}, CodeChallengeMethodsSupported: []string{"S256"},
		TokenEndpointAuthMethodsSupported: []string{oauth.AuthNone, oauth.AuthPrivateKeyJWT}, TokenEndpointAuthSigningAlgs: []string{"RS256", "ES256"},
		RevocationEndpointAuthMethods:     []string{oauth.AuthNone, oauth.AuthPrivateKeyJWT},
		ScopesSupported:                   []string{oauth.ScopeSender, oauth.ScopeSubject, oauth.ScopeBody, oauth.ScopeAttachments},
		ClientIDMetadataDocumentSupported: true, AuthorizationResponseISSSupported: true,
		ServiceDocumentation: "https://permaily.com/developers/",
	}
}

func testResourceMetadata(c oauth.Config, r contracts.Resource) oauth.ResourceMetadata {
	res := c.APIResource
	if r == contracts.ResourceMCP {
		res = c.MCPResource
	}
	return oauth.ResourceMetadata{
		Resource: res, AuthorizationServers: []string{c.Issuer}, ScopesSupported: oauth.DefaultScopes,
		BearerMethodsSupported: []string{"header"}, ResourceName: "Permaily",
	}
}

func testChallenge(c oauth.Config, r contracts.Resource, code oauth.ErrorCode, scopes []string) string {
	u, err := url.Parse(testResourceMetadata(c, r).Resource)
	if err != nil {
		panic(err)
	}
	out := "Bearer "
	if code != "" {
		out += `error="` + string(code) + `", `
	}
	return out + `resource_metadata="` + u.Scheme + "://" + u.Host + oauth.PathResourceMetadata + u.Path + `", scope="` + strings.Join(scopes, " ") + `"`
}

// connApp is the three hosts with the routes mounted, the fake behind them,
// the web app's page that counts what it was given, and the log.
type connApp struct {
	t    *testing.T
	fake *connectionstest.Fake
	h    http.Handler
	logs *bytes.Buffer
	now  time.Time

	mu  sync.Mutex
	spa []*http.Request
}

// newConnApp is the hosts with two sessions: "fresh", whose proof is fresh
// for five more minutes, and "stale", whose proof is not.
func newConnApp(t *testing.T) *connApp {
	t.Helper()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	fresh := &accounts.Authenticated{AccountID: "acc_fresh", Session: contracts.Session{ID: "ses_fresh", ConfirmedUntil: now.Add(5 * time.Minute)}}
	stale := &accounts.Authenticated{AccountID: "acc_stale", Session: contracts.Session{ID: "ses_stale", ConfirmedUntil: now.Add(-time.Second)}}
	a := &connApp{t: t, fake: &connectionstest.Fake{}, logs: &bytes.Buffer{}, now: now}
	cfg := httpapi.ConnectionsConfig{
		Service: a.fake, Sessions: sessions{"fresh": fresh, "stale": stale}, OAuth: testOAuth,
		Metadata: testMetadata, ResourceMetadata: testResourceMetadata, Challenge: testChallenge,
		Log: slog.New(slog.NewJSONHandler(a.logs, nil)), Now: func() time.Time { return now },
	}
	app, api, mcp := httpapi.NewAppMux(), http.NewServeMux(), http.NewServeMux()
	api.Handle("/", router.Hello("api"))
	mcp.Handle("/", router.Hello("mcp"))
	spa := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.spa = append(a.spa, r)
		a.mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>app</html>")
	})
	httpapi.MountConnections(httpapi.ConnectionsMounts{App: app, API: api, MCP: mcp, SPA: spa}, cfg)
	app.Handle("/", spa)
	a.h = router.New(nil, map[string]http.Handler{appHost: httpapi.AppHost(app, nil), apiHost: api, mcpHost: mcp})
	return a
}

// app asks the app's host as the app's own page does, in session (a
// cookie of sessions, "" for none).
func (a *connApp) app(session, method, path, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	r := httptest.NewRequest(method, "https://"+appHost+path, strings.NewReader(body))
	r.Header.Set("Origin", "https://"+appHost)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if session != "" {
		r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: session})
	}
	return a.serve(r)
}

func (a *connApp) serve(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, r)
	return rec
}

// form posts a form to an endpoint of the issuer.
func (a *connApp) form(path, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	r := httptest.NewRequest(http.MethodPost, "https://"+apiHost+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return a.serve(r)
}

// vendor asks the API with a bearer token ("" for none).
func (a *connApp) vendor(token, method, path, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	r := httptest.NewRequest(method, "https://"+apiHost+path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	return a.serve(r)
}

func (a *connApp) spaRequests() []*http.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.spa)
}

func get(t *testing.T, a *connApp, host, path string) *httptest.ResponseRecorder {
	t.Helper()
	return a.serve(httptest.NewRequest(http.MethodGet, "https://"+host+path, nil))
}

// V41: the server's metadata and the API's resource metadata on the api
// host, the MCP's at both places on the mcp host, each where only its host
// serves it; no OpenID configuration; CORS on the metadata and on the token
// and revocation endpoints, and not on the app's routes.
func TestV41TheMetadataOnTheirHostsAndPaths(t *testing.T) {
	a := newConnApp(t)
	const server = `{"issuer":"https://api.permaily.test","authorization_endpoint":"https://app.permaily.test/oauth/authorize",` +
		`"token_endpoint":"https://api.permaily.test/oauth/token","revocation_endpoint":"https://api.permaily.test/oauth/revoke",` +
		`"pushed_authorization_request_endpoint":"https://api.permaily.test/oauth/par","response_types_supported":["code"],` +
		`"response_modes_supported":["query"],"grant_types_supported":["authorization_code","refresh_token"],` +
		`"code_challenge_methods_supported":["S256"],"token_endpoint_auth_methods_supported":["none","private_key_jwt"],` +
		`"token_endpoint_auth_signing_alg_values_supported":["RS256","ES256"],"revocation_endpoint_auth_methods_supported":["none","private_key_jwt"],` +
		`"scopes_supported":["mail.sender","mail.subject","mail.body","mail.attachments"],"client_id_metadata_document_supported":true,` +
		`"authorization_response_iss_parameter_supported":true,"require_pushed_authorization_requests":false,` +
		`"service_documentation":"https://permaily.com/developers/"}`
	resource := func(r string) string {
		return `{"resource":"` + r + `","authorization_servers":["https://api.permaily.test"],` +
			`"scopes_supported":["mail.sender","mail.subject","mail.body"],"bearer_methods_supported":["header"],"resource_name":"Permaily"}`
	}
	for _, c := range []struct{ host, path, body string }{
		{apiHost, "/.well-known/oauth-authorization-server", server},
		{apiHost, "/.well-known/oauth-protected-resource/v1", resource("https://api.permaily.test/v1")},
		{mcpHost, "/.well-known/oauth-protected-resource/mcp", resource("https://mcp.permaily.test/mcp")},
		{mcpHost, "/.well-known/oauth-protected-resource", resource("https://mcp.permaily.test/mcp")},
	} {
		rec := get(t, a, c.host, c.path)
		h := rec.Header()
		if rec.Code != 200 || rec.Body.String() != c.body || h.Get("Content-Type") != "application/json" ||
			h.Get("Cache-Control") != "public, max-age=3600" || h.Get("Access-Control-Allow-Origin") != "*" ||
			h.Get("Access-Control-Allow-Credentials") != "" || h.Get("Set-Cookie") != "" {
			t.Errorf("%s%s: %d %v\n%s", c.host, c.path, rec.Code, h, rec.Body)
		}
		head := a.serve(httptest.NewRequest(http.MethodHead, "https://"+c.host+c.path, nil))
		if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != fmt.Sprint(len(c.body)) {
			t.Errorf("HEAD %s%s: %d %v", c.host, c.path, head.Code, head.Header())
		}
		pre := a.serve(httptest.NewRequest(http.MethodOptions, "https://"+c.host+c.path, nil))
		if pre.Code != 204 || pre.Header().Get("Access-Control-Allow-Origin") != "*" || pre.Header().Get("Access-Control-Allow-Methods") != "GET, HEAD" {
			t.Errorf("OPTIONS %s%s: %d %v", c.host, c.path, pre.Code, pre.Header())
		}
		if post := a.serve(httptest.NewRequest(http.MethodPost, "https://"+c.host+c.path, nil)); post.Code != 405 || post.Header().Get("Allow") != "GET, HEAD, OPTIONS" {
			t.Errorf("POST %s%s: %d", c.host, c.path, post.Code)
		}
	}
	// Each document on its own host only; no OpenID provider; nothing else
	// under the well-known paths.
	for _, c := range []struct{ host, path string }{
		{apiHost, "/.well-known/openid-configuration"}, {mcpHost, "/.well-known/openid-configuration"},
		{apiHost, "/.well-known/oauth-protected-resource/mcp"}, {apiHost, "/.well-known/oauth-protected-resource"},
		{mcpHost, "/.well-known/oauth-authorization-server"}, {mcpHost, "/.well-known/oauth-protected-resource/v1"},
		{apiHost, "/.well-known/oauth-authorization-server/x"}, {mcpHost, "/.well-known/oauth-protected-resource/mcp/x"},
		{apiHost, "/oauth/authorize"}, {apiHost, "/oauth/other"},
	} {
		if rec := get(t, a, c.host, c.path); rec.Code != 404 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s%s: %d %v", c.host, c.path, rec.Code, rec.Header())
		}
	}
	// The token and revocation endpoints answer a browser's preflight, for
	// any origin, without credentials; the pushed request endpoint does not.
	for _, path := range []string{oauth.PathToken, oauth.PathRevoke} {
		r := httptest.NewRequest(http.MethodOptions, "https://"+apiHost+path, nil)
		r.Header.Set("Origin", "https://vendor.example")
		rec := a.serve(r)
		if h := rec.Header(); rec.Code != 204 || h.Get("Access-Control-Allow-Origin") != "*" || h.Get("Access-Control-Allow-Methods") != "POST" ||
			h.Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("OPTIONS %s: %d %v", path, rec.Code, h)
		}
		if rec := a.form(path, "grant_type=x"); rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s answers without CORS: %v", path, rec.Header())
		}
	}
	a.fake.PushFunc = func(context.Context, url.Values) (string, error) { return oauth.RequestURIPrefix + "pmp_x", nil }
	if rec := a.form(oauth.PathPushed, "client_id=x"); rec.Code != 201 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("par: %d %v", rec.Code, rec.Header())
	}
	// The app's routes answer no other origin.
	a.fake.ListFunc = func(context.Context, connections.Person) (*contracts.ConnectionList, error) {
		return &contracts.ConnectionList{}, nil
	}
	r := httptest.NewRequest(http.MethodOptions, "https://"+appHost+"/api/v1/connections", nil)
	r.Header.Set("Origin", "https://vendor.example")
	for _, rec := range []*httptest.ResponseRecorder{a.serve(r), a.app("fresh", "GET", "/api/v1/connections", "")} {
		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("the app's routes answer CORS: %v", rec.Header())
		}
	}
}

// V42: the authorization page is the web app's, on the app's host and
// outside /api/, reached from another site without the session's cookie;
// the server reads nothing of the request (the app is given neither its
// query string nor its cookies, and the service is asked nothing); the
// cross-site exceptions are as they were; the app keeps frame-ancestors
// 'none'.
func TestV42TheAuthorizationPageServesTheAppAndReadsNothing(t *testing.T) {
	a := newConnApp(t)
	if strings.HasPrefix(oauth.PathAuthorize, "/api/") {
		t.Fatal("the authorization page is under /api/")
	}
	if got := httpapi.CrossSiteExceptions(); len(got) != 1 || got[0].Path != httpapi.ProviderCallback {
		t.Fatalf("the cross-site exceptions changed: %v", got)
	}
	const query = "?response_type=code&client_id=https%3A%2F%2Fclaude.ai%2Foauth%2Fclaude-code-client-metadata&state=st4te&code_challenge=c"
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r := httptest.NewRequest(method, "https://"+appHost+oauth.PathAuthorize+query, nil)
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.Header.Set("Sec-Fetch-Mode", "navigate")
		r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "fresh"})
		rec := a.serve(r)
		h := rec.Header()
		if rec.Code != 200 || h.Get("Set-Cookie") != "" || !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
			h.Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("%s: %d %v", method, rec.Code, h)
		}
	}
	got := a.spaRequests()
	if len(got) != 2 {
		t.Fatalf("the app was given %d requests", len(got))
	}
	for _, r := range got {
		if r.URL.Path != oauth.PathAuthorize || r.URL.RawQuery != "" || r.URL.ForceQuery || r.RequestURI != oauth.PathAuthorize ||
			r.Header.Get("Cookie") != "" || len(r.Cookies()) != 0 {
			t.Fatalf("the app was given %v %q %v", r.URL, r.RequestURI, r.Header)
		}
	}
	if calls := a.fake.Calls(); len(calls) != 0 {
		t.Fatalf("the service was asked %v", calls)
	}
	r := httptest.NewRequest(http.MethodPost, "https://"+appHost+oauth.PathAuthorize+query, nil)
	if rec := a.serve(r); rec.Code != 405 || rec.Header().Get("Allow") != "GET, HEAD" || len(a.spaRequests()) != 2 {
		t.Fatalf("POST: %d %v", rec.Code, rec.Header())
	}
}

// consentRoute is one of the person's routes with a body it takes.
type consentRoute struct{ method, path, body string }

var consentRoutes = []consentRoute{
	{"POST", "/api/v1/oauth/requests", `{"query":"response_type=code"}`},
	{"POST", "/api/v1/oauth/requests/pmq_1/matches", `{"draft":{"mailboxes":["mbx_1"],"since":{"new":true}},"mailbox":"mbx_1"}`},
	{"POST", "/api/v1/oauth/requests/pmq_1/decision", `{"allow":false}`},
	{"GET", "/api/v1/connections", ""},
	{"GET", "/api/v1/connections/con_1", ""},
	{"POST", "/api/v1/connections/con_1/revoke", ""},
	{"POST", "/api/v1/connections/con_1/devices/fam_1/revoke", ""},
	{"POST", "/api/v1/connections/con_1/renew", `{"model_notice":"2026-11-01","version":3}`},
}

// whatTheFakeWasGiven is what the fake's methods were called with.
type whatTheFakeWasGiven struct {
	mu      sync.Mutex
	persons []connections.Person
	ids     []string
	bodies  []any
}

func (g *whatTheFakeWasGiven) add(p connections.Person, id string, body any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.persons, g.ids, g.bodies = append(g.persons, p), append(g.ids, id), append(g.bodies, body)
}

// answerEverything sets every person's method of the fake to answer, and
// records what it was given.
func answerEverything(f *connectionstest.Fake) *whatTheFakeWasGiven {
	g := &whatTheFakeWasGiven{}
	conn := &contracts.Connection{ID: "con_1", State: contracts.ConnectionActive, Version: 3}
	f.StartFunc = func(_ context.Context, p connections.Person, q string) (*contracts.AuthorizationStarted, error) {
		g.add(p, "", q)
		return &contracts.AuthorizationStarted{Request: &contracts.AuthorizationRequest{ID: "pmq_1", Change: contracts.ChangeNew}}, nil
	}
	f.PreviewFunc = func(_ context.Context, p connections.Person, id string, in contracts.ConsentPreview) (*contracts.ConsentPreviewPage, error) {
		g.add(p, id, in)
		return &contracts.ConsentPreviewPage{DraftID: "qry_1", Change: contracts.ChangeNew}, nil
	}
	f.DecideFunc = func(_ context.Context, p connections.Person, id string, in contracts.AuthorizationDecision) (*contracts.AuthorizationAnswer, error) {
		g.add(p, id, in)
		return &contracts.AuthorizationAnswer{Redirect: "http://127.0.0.1:4711/callback?error=access_denied"}, nil
	}
	f.ListFunc = func(_ context.Context, p connections.Person) (*contracts.ConnectionList, error) {
		g.add(p, "", nil)
		return &contracts.ConnectionList{Connections: []contracts.Connection{*conn}}, nil
	}
	f.GetFunc = func(_ context.Context, p connections.Person, id string) (*contracts.Connection, error) {
		g.add(p, id, nil)
		return conn, nil
	}
	f.RevokeFunc = func(_ context.Context, p connections.Person, id string) (*contracts.Connection, error) {
		g.add(p, id, nil)
		return conn, nil
	}
	f.RevokeDeviceFunc = func(_ context.Context, p connections.Person, id, device string) (*contracts.Connection, error) {
		g.add(p, id+"/"+device, nil)
		return conn, nil
	}
	f.RenewFunc = func(_ context.Context, p connections.Person, id string, in contracts.ConnectionRenewal) (*contracts.Connection, error) {
		g.add(p, id, in)
		return conn, nil
	}
	return g
}

// V43: the person's routes answer the app alone (a request that says
// another site, or another host of the domain, made it is refused before
// anything, by AppHost), need a session, and answer the contract with what
// the service answered for that session; allowing without a fresh proof is
// confirmation_needed before the service is asked.
func TestV43TheConsentRoutesAnswerTheAppAlone(t *testing.T) {
	a := newConnApp(t)
	given := answerEverything(a.fake)
	for _, c := range consentRoutes {
		for _, site := range []string{"cross-site", "same-site"} {
			r := httptest.NewRequest(c.method, "https://"+appHost+c.path, strings.NewReader(c.body))
			r.Header.Set("Origin", "https://"+appHost)
			r.Header.Set("Sec-Fetch-Site", site)
			r.Header.Set("Content-Type", "application/json")
			r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "fresh"})
			refusedAsNotTheApp(t, c.path+" "+site, a.serve(r))
		}
		// Another origin, without Fetch Metadata: fromTheApp's refusal.
		if c.method == "POST" {
			r := httptest.NewRequest(c.method, "https://"+appHost+c.path, strings.NewReader(c.body))
			r.Header.Set("Origin", "https://vendor.example")
			r.Header.Set("Content-Type", "application/json")
			r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "fresh"})
			want(t, a.serve(r), 403, "forbidden")
		}
		want(t, a.app("", c.method, c.path, c.body), 401, "unauthorized")
		want(t, a.app("nobody", c.method, c.path, c.body), 401, "unauthorized")
	}
	if calls := a.fake.Calls(); len(calls) != 0 {
		t.Fatalf("the service was asked %v", calls)
	}
	// In session: the contract, for this session's person.
	for _, c := range consentRoutes {
		rec := a.app("fresh", c.method, c.path, c.body)
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Content-Type") != "application/json" ||
			rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s %s: %d %v %s", c.method, c.path, rec.Code, rec.Header(), rec.Body)
		}
	}
	want := connections.Person{Account: "acc_fresh", Session: "ses_fresh", ConfirmedUntil: a.now.Add(5 * time.Minute)}
	for _, p := range given.persons {
		if p != want {
			t.Fatalf("the service was given %+v", p)
		}
	}
	if !slices.Equal(given.ids, []string{"", "pmq_1", "pmq_1", "", "con_1", "con_1", "con_1/fam_1", "con_1"}) {
		t.Fatalf("ids %v", given.ids)
	}
	if q := given.bodies[0]; q != "response_type=code" {
		t.Fatalf("start was given %v", q)
	}
	if p := given.bodies[1].(contracts.ConsentPreview); p.Mailbox != "mbx_1" || !slices.Equal(p.Draft.Mailboxes, []string{"mbx_1"}) || !p.Draft.Since.New {
		t.Fatalf("preview was given %+v", p)
	}
	if r := given.bodies[7].(contracts.ConnectionRenewal); r.ModelNotice != "2026-11-01" || r.Version != 3 {
		t.Fatalf("renew was given %+v", r)
	}
	var started contracts.AuthorizationStarted
	if json.Unmarshal(a.app("fresh", "POST", "/api/v1/oauth/requests", `{"query":""}`).Body.Bytes(), &started) != nil || started.Request.ID != "pmq_1" {
		t.Fatalf("started %+v", started)
	}
}

// V43: allowing, and renewing, need the session's proof to be fresh (ADR
// 0022): without it, confirmation_needed, and the service is not asked;
// denying needs none. The service checks it again when it commits.
func TestV43AllowingWithoutAFreshProofIsConfirmationNeeded(t *testing.T) {
	a := newConnApp(t)
	answerEverything(a.fake)
	const allow = `{"allow":true,"draft":{"mailboxes":["mbx_1"],"since":{"new":true}},"model_notice":"2026-11-01",` +
		`"snapshot":{"change":"new","ends_at":"2027-02-14T12:00:00Z","draft_id":"qry_1"}}`
	want(t, a.app("stale", "POST", "/api/v1/oauth/requests/pmq_1/decision", allow), 403, "confirmation_needed")
	want(t, a.app("stale", "POST", "/api/v1/connections/con_1/renew", `{"model_notice":"2026-11-01","version":3}`), 403, "confirmation_needed")
	if calls := a.fake.Calls(); len(calls) != 0 {
		t.Fatalf("the service was asked %v", calls)
	}
	want(t, a.app("stale", "POST", "/api/v1/oauth/requests/pmq_1/decision", `{"allow":false}`), 200, "")
	want(t, a.app("fresh", "POST", "/api/v1/oauth/requests/pmq_1/decision", allow), 200, "")
	if calls := a.fake.Calls(); !slices.Equal(calls, []string{"Decide", "Decide"}) {
		t.Fatalf("calls %v", calls)
	}
	// The service's own refusal at the commit is answered as such.
	a.fake.DecideFunc = func(context.Context, connections.Person, string, contracts.AuthorizationDecision) (*contracts.AuthorizationAnswer, error) {
		return nil, contracts.ErrConfirmationNeeded
	}
	want(t, a.app("fresh", "POST", "/api/v1/oauth/requests/pmq_1/decision", allow), 403, "confirmation_needed")
}

// The person's routes answer the service's refusals with their codes and
// fixed words: what changed since the screen (409, the screen loads it
// again), the bounds the person acts on (409), a vendor's document not read
// (502), a request the app shows (400), a formula's problem with its
// details; a failure of the service's own is 500, logged with nothing of
// the request.
func TestTheConsentRoutesAnswerTheServicesRefusals(t *testing.T) {
	a := newConnApp(t)
	answerEverything(a.fake)
	var refusal error
	a.fake.StartFunc = func(context.Context, connections.Person, string) (*contracts.AuthorizationStarted, error) {
		return nil, refusal
	}
	for err, status := range map[error]int{
		contracts.ErrConsentChanged: 409, contracts.ErrModelNoticeChanged: 409, contracts.ErrTooManyDevices: 409,
		contracts.ErrTooManyConnections: 409, contracts.ErrRequestEnded: 409, contracts.ErrConnectionEnded: 409,
		contracts.ErrVendorUnreachable: 502, contracts.ErrInvalidClient: 400, contracts.ErrRequestTooLarge: 400,
		contracts.ErrTermsChanged: 400, contracts.ErrNotFound: 404, contracts.ErrTooManyRequests: 429,
		fmt.Errorf("wrapped: %w", contracts.ErrConsentChanged): 409,
	} {
		refusal = err
		var code string
		if known, ok := errors.AsType[*contracts.Error](err); ok {
			code = known.Code
		}
		want(t, a.app("fresh", "POST", "/api/v1/oauth/requests", `{"query":""}`), status, code)
	}
	refusal = fmt.Errorf("the storage of pmq_secret, acc_fresh: %w", errors.New("boom"))
	want(t, a.app("fresh", "POST", "/api/v1/oauth/requests", `{"query":"client_id=https%3A%2F%2Fvendor.example%2Fc"}`), 500, "internal")
	if out := a.logs.String(); !strings.Contains(out, "internal error") || strings.Contains(out, "pmq_secret") || strings.Contains(out, "acc_fresh") ||
		strings.Contains(out, "vendor.example") {
		t.Fatalf("log: %s", out)
	}
	// Bodies: strict JSON, of the route's own shape, from the app.
	want(t, a.app("fresh", "POST", "/api/v1/oauth/requests", `{"query":"x","other":1}`), 400, "invalid_body")
	want(t, a.app("fresh", "POST", "/api/v1/oauth/requests/pmq_1/matches", `[]`), 400, "invalid_body")
	want(t, a.app("fresh", "POST", "/api/v1/oauth/requests/pmq_1/decision", `{"allow":"yes"}`), 400, "invalid_body")
	want(t, a.app("fresh", "POST", "/api/v1/connections/con_1/renew", `{"version":-1}`), 400, "invalid_body")
	want(t, a.app("fresh", "POST", "/api/v1/oauth/requests", `{"query":"`+strings.Repeat("a", 16<<10)+`"}`), 400, "invalid_body")
	r := httptest.NewRequest("POST", "https://"+appHost+"/api/v1/oauth/requests", strings.NewReader("query=x"))
	r.Header.Set("Origin", "https://"+appHost)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "fresh"})
	want(t, a.serve(r), 415, "invalid_body")
	// Methods and paths.
	for _, c := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/api/v1/oauth/requests", 405}, {"GET", "/api/v1/oauth/requests/pmq_1/matches", 405},
		{"GET", "/api/v1/oauth/requests/pmq_1/decision", 405}, {"POST", "/api/v1/connections", 405},
		{"DELETE", "/api/v1/connections/con_1", 405}, {"GET", "/api/v1/connections/con_1/revoke", 405},
		{"GET", "/api/v1/connections/con_1/devices/fam_1/revoke", 405}, {"GET", "/api/v1/connections/con_1/renew", 405},
		{"GET", "/api/v1/oauth/requests/pmq_1", 404}, {"GET", "/api/v1/oauth/other", 404},
		{"GET", "/api/v1/connections/con_1/devices", 404}, {"POST", "/api/v1/connections/con_1/other", 404},
	} {
		if rec := a.app("fresh", c.method, c.path, ""); rec.Code != c.status {
			t.Errorf("%s %s: %d", c.method, c.path, rec.Code)
		}
	}
}

// V61 (finding 17): under terms the account has not agreed to, the person
// still lists and reads connections and revokes a connection or a device:
// the routes add no rule of standing of their own; allowing and renewing
// are the service's terms_needed.
func TestV61ThePersonsRoutesUnderUnagreedTerms(t *testing.T) {
	a := newConnApp(t)
	answerEverything(a.fake)
	a.fake.RenewFunc = func(context.Context, connections.Person, string, contracts.ConnectionRenewal) (*contracts.Connection, error) {
		return nil, contracts.ErrTermsChanged
	}
	inner := a.fake.DecideFunc
	a.fake.DecideFunc = func(ctx context.Context, p connections.Person, id string, in contracts.AuthorizationDecision) (*contracts.AuthorizationAnswer, error) {
		if in.Allow {
			return nil, contracts.ErrTermsChanged
		}
		return inner(ctx, p, id, in)
	}
	for _, c := range []consentRoute{
		{"GET", "/api/v1/connections", ""}, {"GET", "/api/v1/connections/con_1", ""},
		{"POST", "/api/v1/connections/con_1/revoke", ""}, {"POST", "/api/v1/connections/con_1/devices/fam_1/revoke", ""},
		{"POST", "/api/v1/oauth/requests/pmq_1/decision", `{"allow":false}`},
	} {
		// Revoking needs no fresh proof: it only protects.
		for _, who := range []string{"fresh", "stale"} {
			want(t, a.app(who, c.method, c.path, c.body), 200, "")
		}
	}
	want(t, a.app("fresh", "POST", "/api/v1/connections/con_1/renew", `{"model_notice":"n","version":3}`), 400, "terms_needed")
	want(t, a.app("fresh", "POST", "/api/v1/oauth/requests/pmq_1/decision", `{"allow":true}`), 400, "terms_needed")
}

// V44: the token, revocation and pushed request endpoints take a form body
// alone, of at most 64 KiB, nothing in their address and no Authorization
// header; answer no-store, and refusals in RFC 6749's JSON with the code
// alone; a grant type other than a code's or a refresh token's is
// unsupported_grant_type before the service is asked.
func TestV44TheIssuersEndpoints(t *testing.T) {
	a := newConnApp(t)
	var forms []url.Values
	var refusal error
	a.fake.TokenFunc = func(_ context.Context, f url.Values) (*connections.TokenAnswer, error) {
		forms = append(forms, f)
		if refusal != nil {
			return nil, refusal
		}
		return &connections.TokenAnswer{AccessToken: "pma_a", TokenType: "Bearer", ExpiresIn: 3600, RefreshToken: "pmr_r", Scope: "mail.sender"}, nil
	}
	a.fake.RevokeTokenFunc = func(_ context.Context, f url.Values) error { forms = append(forms, f); return refusal }
	a.fake.PushFunc = func(_ context.Context, f url.Values) (string, error) {
		forms = append(forms, f)
		return oauth.RequestURIPrefix + "pmp_p", refusal
	}
	oauthError := func(rec *httptest.ResponseRecorder, status int, code oauth.ErrorCode) {
		t.Helper()
		if rec.Code != status || rec.Body.String() != `{"error":"`+string(code)+`"}`+"\n" || rec.Header().Get("Cache-Control") != "no-store" ||
			rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("%d %v %q, want %d %s", rec.Code, rec.Header(), rec.Body, status, code)
		}
	}
	const code = "grant_type=authorization_code&code=pmc_c&code_verifier=v&redirect_uri=http%3A%2F%2F127.0.0.1%3A4711%2Fcallback&client_id=https%3A%2F%2Fclaude.ai%2Fc"
	for _, path := range []string{oauth.PathToken, oauth.PathRevoke, oauth.PathPushed} {
		post := func(body string, set func(*http.Request)) *httptest.ResponseRecorder {
			r := httptest.NewRequest(http.MethodPost, "https://"+apiHost+path, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if set != nil {
				set(r)
			}
			return a.serve(r)
		}
		oauthError(a.serve(httptest.NewRequest(http.MethodGet, "https://"+apiHost+path+"?"+code, nil)), 405, oauth.ErrInvalidRequest)
		oauthError(post(`{"grant_type":"refresh_token"}`, func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }), 400, oauth.ErrInvalidRequest)
		oauthError(post(code, func(r *http.Request) { r.Header.Del("Content-Type") }), 400, oauth.ErrInvalidRequest)
		oauthError(post("", func(r *http.Request) { r.URL.RawQuery = code }), 400, oauth.ErrInvalidRequest)
		oauthError(post(code, func(r *http.Request) { r.URL.ForceQuery = true }), 400, oauth.ErrInvalidRequest)
		oauthError(post(code, func(r *http.Request) { r.SetBasicAuth("https://claude.ai/c", "secret") }), 401, oauth.ErrInvalidClient)
		oauthError(post(code+"&x=%zz", nil), 400, oauth.ErrInvalidRequest)
		oauthError(post(code+"&pad="+strings.Repeat("a", oauth.FormMaxBytes-len(code)-4), nil), 413, oauth.ErrInvalidRequest)
		if len(forms) != 0 {
			t.Fatalf("%s: the service was given %v", path, forms)
		}
		// Exactly 64 KiB is taken, its values as sent.
		full := code + "&pad=" + strings.Repeat("a", oauth.FormMaxBytes-len(code)-5)
		if rec := post(full, func(r *http.Request) {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
		}); rec.Code >= 300 ||
			rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Pragma") != "no-cache" {
			t.Fatalf("%s: %d %v %s", path, rec.Code, rec.Header(), rec.Body)
		}
		if len(forms) != 1 || forms[0].Get("code_verifier") != "v" || forms[0].Get("redirect_uri") != "http://127.0.0.1:4711/callback" || len(forms[0].Get("pad")) == 0 {
			t.Fatalf("%s: the service was given %v", path, forms)
		}
		forms = nil
	}
	// The token endpoint's grant types.
	for body, want := range map[string]oauth.ErrorCode{
		"code=pmc_c": oauth.ErrInvalidRequest, "grant_type=": oauth.ErrInvalidRequest,
		"grant_type=refresh_token&grant_type=refresh_token": oauth.ErrInvalidRequest,
		"grant_type=client_credentials":                     oauth.ErrUnsupportedGrantType, "grant_type=password": oauth.ErrUnsupportedGrantType,
		"grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Ajwt-bearer&assertion=x": oauth.ErrUnsupportedGrantType,
		"grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Adevice_code":            oauth.ErrUnsupportedGrantType,
	} {
		oauthError(a.form(oauth.PathToken, body), 400, want)
	}
	if len(forms) != 0 {
		t.Fatalf("the service was given %v", forms)
	}
	// The answers.
	rec := a.form(oauth.PathToken, "grant_type=refresh_token&refresh_token=pmr_r&client_id=c")
	if rec.Code != 200 || rec.Body.String() != `{"access_token":"pma_a","token_type":"Bearer","expires_in":3600,"refresh_token":"pmr_r","scope":"mail.sender"}`+"\n" {
		t.Fatalf("token: %d %s", rec.Code, rec.Body)
	}
	if rec := a.form(oauth.PathRevoke, "token=pma_a"); rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "0" {
		t.Fatalf("revoke: %d %q", rec.Code, rec.Body)
	}
	if rec := a.form(oauth.PathPushed, "client_id=c"); rec.Code != 201 ||
		rec.Body.String() != `{"request_uri":"urn:ietf:params:oauth:request_uri:pmp_p","expires_in":60}`+"\n" {
		t.Fatalf("par: %d %s", rec.Code, rec.Body)
	}
	// Refusals, with their code alone and never a redirect.
	for _, path := range []string{oauth.PathToken, oauth.PathRevoke, oauth.PathPushed} {
		body := "grant_type=authorization_code&client_id=c"
		for _, c := range []struct {
			err    error
			status int
			code   oauth.ErrorCode
			retry  bool
		}{
			{&connections.OAuthError{Code: oauth.ErrInvalidGrant}, 400, oauth.ErrInvalidGrant, false},
			{fmt.Errorf("x: %w", &connections.OAuthError{Code: oauth.ErrInvalidScope}), 400, oauth.ErrInvalidScope, false},
			{&connections.OAuthError{Code: oauth.ErrInvalidClient}, 401, oauth.ErrInvalidClient, false},
			{&connections.OAuthError{Code: oauth.ErrTemporarily}, 503, oauth.ErrTemporarily, true},
			{&connections.OAuthError{Code: oauth.ErrServerError}, 500, oauth.ErrServerError, false},
			{connections.ErrUnavailable, 503, oauth.ErrTemporarily, true},
			{contracts.ErrTooManyRequests, 429, oauth.ErrTemporarily, true},
			{errors.New("pmr_leaked and https://vendor.example/c"), 500, oauth.ErrServerError, false},
		} {
			refusal = c.err
			rec := a.form(path, body)
			oauthError(rec, c.status, c.code)
			if (rec.Header().Get("Retry-After") != "") != c.retry || rec.Header().Get("Location") != "" {
				t.Fatalf("%s %v: %v", path, c.err, rec.Header())
			}
		}
	}
	if out := a.logs.String(); !strings.Contains(out, "oauth: internal error") || strings.Contains(out, "pmr_leaked") || strings.Contains(out, "vendor.example") {
		t.Fatalf("log: %s", out)
	}
	// A request its client left is not the server's failure, and not logged.
	a.logs.Reset()
	ctx, cancel := context.WithCancel(context.Background())
	refusal = nil
	a.fake.TokenFunc = func(context.Context, url.Values) (*connections.TokenAnswer, error) {
		cancel()
		return nil, context.Canceled
	}
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "https://"+apiHost+oauth.PathToken, strings.NewReader("grant_type=refresh_token"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	oauthError(a.serve(r), 500, oauth.ErrServerError)
	if a.logs.Len() != 0 {
		t.Fatalf("log: %s", a.logs)
	}
}
