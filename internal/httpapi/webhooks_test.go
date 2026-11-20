package httpapi_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/mcpapi"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/webhook"
)

// The routes of connections' webhooks (ADR 0039, pull request 4), against
// connectionstest.Fake (its WebhookOff) and a webhooks.KeySet, mounted as
// the composition mounts them (newConnApp).

const webhookOffPath = "/api/v1/connections/con_1/webhook/off"

// K51. POST /api/v1/connections/{id}/webhook/off calls WebhookOff with the
// person, the connection and the version sent, and answers the connection
// as the service gave it; it answers the app alone (refused cross-site and
// same-site, from another origin, without a session, before the service is
// asked); it needs no fresh proof and adds no rule of standing, so it works
// under unagreed terms, as revoking does; the service's refusals are
// answered with their codes; the body is strict.
func TestK51TurningAWebhookOffAnswersTheAppAlone(t *testing.T) {
	a := newConnApp(t)
	type call struct {
		p       connections.Person
		id      string
		version uint64
	}
	var calls []call
	off := &contracts.Connection{ID: "con_1", State: contracts.ConnectionActive, Version: 4,
		Webhook: &contracts.ConnectionWebhook{Host: "trip-app.com", NewMail: true, State: contracts.WebhookOff}}
	var refusal error
	a.fake.WebhookOffFunc = func(_ context.Context, p connections.Person, id string, version uint64) (*contracts.Connection, error) {
		calls = append(calls, call{p, id, version})
		if refusal != nil {
			return nil, refusal
		}
		return off, nil
	}

	// Not the app: refused before anything.
	for _, site := range []string{"cross-site", "same-site"} {
		r := httptest.NewRequest("POST", "https://"+appHost+webhookOffPath, strings.NewReader(`{"version":3}`))
		r.Header.Set("Origin", "https://"+appHost)
		r.Header.Set("Sec-Fetch-Site", site)
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "fresh"})
		refusedAsNotTheApp(t, site, a.serve(r))
	}
	r := httptest.NewRequest("POST", "https://"+appHost+webhookOffPath, strings.NewReader(`{"version":3}`))
	r.Header.Set("Origin", "https://vendor.example")
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "fresh"})
	want(t, a.serve(r), 403, "forbidden")
	want(t, a.app("", "POST", webhookOffPath, `{"version":3}`), 401, "unauthorized")
	want(t, a.app("nobody", "POST", webhookOffPath, `{"version":3}`), 401, "unauthorized")
	if got := a.fake.Calls(); len(got) != 0 {
		t.Fatalf("the service was asked %v", got)
	}

	// In session, fresh or not (no fresh proof: it only protects; and no
	// rule of standing, so under unagreed terms too): the connection as the
	// service answered it, for this session's person and the version sent.
	for _, who := range []string{"fresh", "stale"} {
		rec := a.app(who, "POST", webhookOffPath, `{"version":3}`)
		want(t, rec, 200, "")
		if h := rec.Header(); h.Get("Cache-Control") != "no-store" || h.Get("Content-Type") != "application/json" ||
			h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("%s: %v", who, h)
		}
		b, _ := json.Marshal(off)
		if strings.TrimSpace(rec.Body.String()) != string(b) {
			t.Fatalf("%s: %s, want %s", who, rec.Body, b)
		}
	}
	wantCalls := []call{
		{connections.Person{Account: "acc_fresh", Session: "ses_fresh", ConfirmedUntil: a.now.Add(5 * time.Minute)}, "con_1", 3},
		{connections.Person{Account: "acc_stale", Session: "ses_stale", ConfirmedUntil: a.now.Add(-time.Second)}, "con_1", 3},
	}
	if !slices.Equal(calls, wantCalls) {
		t.Fatalf("WebhookOff was given %+v", calls)
	}

	// The service's refusals: a page that is stale (the screen loads the
	// connection again), a connection without a webhook or of another
	// account, one ended; a failure of its own is 500, logged by its kind.
	for err, status := range map[error]int{
		contracts.ErrConsentChanged: 409, contracts.ErrConnectionEnded: 409, contracts.ErrNotFound: 404,
		fmt.Errorf("wrapped: %w", contracts.ErrConsentChanged): 409,
	} {
		refusal = err
		known, _ := errors.AsType[*contracts.Error](err)
		want(t, a.app("fresh", "POST", webhookOffPath, `{"version":3}`), status, known.Code)
	}
	refusal = errors.New("the record of con_1 for https://trip-app.com/hooks/permaily/u1")
	want(t, a.app("fresh", "POST", webhookOffPath, `{"version":3}`), 500, "internal")
	if out := a.logs.String(); !strings.Contains(out, "internal error") || strings.Contains(out, "trip-app.com") || strings.Contains(out, "con_1") {
		t.Fatalf("log: %s", out)
	}
	refusal = nil

	// The body: one object of the version alone.
	n := len(calls)
	for _, body := range []string{``, `[]`, `{"version":-1}`, `{"version":"3"}`, `{"version":3,"other":1}`, `{"version":3}{}`} {
		want(t, a.app("fresh", "POST", webhookOffPath, body), 400, "invalid_body")
	}
	if len(calls) != n {
		t.Fatalf("the service was asked with a body it should not take")
	}
	// Methods and paths.
	for _, c := range []struct {
		method, path string
		status       int
	}{
		{"GET", webhookOffPath, 405}, {"DELETE", webhookOffPath, 405},
		{"POST", "/api/v1/connections/con_1/webhook", 404}, {"POST", "/api/v1/connections/con_1/webhook/on", 404},
	} {
		if rec := a.app("fresh", c.method, c.path, `{"version":3}`); rec.Code != c.status {
			t.Errorf("%s %s: %d", c.method, c.path, rec.Code)
		}
	}
	if rec := a.app("fresh", "GET", webhookOffPath, ""); rec.Header().Get("Allow") != "POST" {
		t.Errorf("GET: Allow %q", rec.Header().Get("Allow"))
	}

	// Without the service's WebhookAPI, the route is not served.
	b := newConnApp(t, func(c *httpapi.ConnectionsConfig) { c.Webhooks = nil })
	want(t, b.app("fresh", "POST", webhookOffPath, `{"version":3}`), 404, "not_found")
	if got := b.fake.Calls(); len(got) != 0 {
		t.Fatalf("the service was asked %v", got)
	}
}

// keySet is a webhooks.KeySet that answers what the test sets.
type keySet struct {
	set   []byte
	err   error
	asked int
}

func (k *keySet) KeySet(context.Context) ([]byte, error) {
	k.asked++
	return k.set, k.err
}

// testKeys is the JWK set of two keys, as during a rotation.
func testKeys(t *testing.T) ([]byte, map[string]ed25519.PublicKey) {
	t.Helper()
	keys := map[string]ed25519.PublicKey{}
	for i, id := range []string{"wk1", "wk2"} {
		priv, err := webhook.KeyFromSeed(bytes.Repeat([]byte{byte(i + 1)}, ed25519.SeedSize))
		if err != nil {
			t.Fatal(err)
		}
		keys[id] = priv.Public().(ed25519.PublicKey)
	}
	set, err := webhook.KeySet(keys)
	if err != nil {
		t.Fatal(err)
	}
	return set, keys
}

// K52. GET https://api.<domain>/.well-known/permaily-webhook-keys: the
// service's key set byte for byte, which a vendor (webhook.ParseKeySet)
// reads back to the same keys; public for an hour, any origin without
// credentials, HEAD and the preflight; other methods refused; on the api
// host only (the app's and the mcp host's never answer it); a set the
// service cannot give is 503, no-store, logged by its kind alone; nothing
// published without a KeySet.
func TestK52TheKeySetIsPublishedOnTheAPIHostOnly(t *testing.T) {
	set, keys := testKeys(t)
	ks := &keySet{set: set}
	a := newConnApp(t, func(c *httpapi.ConnectionsConfig) { c.WebhookKeys = ks })
	if webhook.KeysPath != "/.well-known/permaily-webhook-keys" {
		t.Fatal(webhook.KeysPath)
	}

	rec := get(t, a, apiHost, webhook.KeysPath)
	h := rec.Header()
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), set) || h.Get("Content-Type") != "application/json" ||
		h.Get("Cache-Control") != "public, max-age=3600" || h.Get("Access-Control-Allow-Origin") != "*" ||
		h.Get("Access-Control-Allow-Credentials") != "" || h.Get("Set-Cookie") != "" || h.Get("Content-Length") != fmt.Sprint(len(set)) ||
		h.Get("Strict-Transport-Security") == "" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("%d %v\n%s", rec.Code, h, rec.Body)
	}
	got, err := webhook.ParseKeySet(rec.Body.Bytes())
	if err != nil || len(got) != 2 || !got["wk1"].Equal(keys["wk1"]) || !got["wk2"].Equal(keys["wk2"]) {
		t.Fatalf("a vendor reads %v %v", got, err)
	}
	head := a.serve(httptest.NewRequest(http.MethodHead, "https://"+apiHost+webhook.KeysPath, nil))
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != fmt.Sprint(len(set)) {
		t.Fatalf("HEAD: %d %v", head.Code, head.Header())
	}
	pre := httptest.NewRequest(http.MethodOptions, "https://"+apiHost+webhook.KeysPath, nil)
	pre.Header.Set("Origin", "https://vendor.example")
	if rec := a.serve(pre); rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != "*" ||
		rec.Header().Get("Access-Control-Allow-Methods") != "GET, HEAD" || rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("OPTIONS: %d %v", rec.Code, rec.Header())
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if rec := a.serve(httptest.NewRequest(m, "https://"+apiHost+webhook.KeysPath, nil)); rec.Code != 405 ||
			rec.Header().Get("Allow") != "GET, HEAD, OPTIONS" || bytes.Contains(rec.Body.Bytes(), set) {
			t.Fatalf("%s: %d %v", m, rec.Code, rec.Header())
		}
	}
	asked := ks.asked

	// Only on the api host, only at its path.
	for _, c := range []struct{ host, path string }{
		{appHost, webhook.KeysPath}, {mcpHost, webhook.KeysPath},
		{apiHost, webhook.KeysPath + "/"}, {apiHost, webhook.KeysPath + "/wk1"}, {apiHost, "/.well-known/jwks.json"},
		{apiHost, "/.well-known/%70ermaily-webhook-keys"}, {apiHost, "/.well-known//permaily-webhook-keys"},
	} {
		rec := get(t, a, c.host, c.path)
		if rec.Code == 200 && c.host != appHost || bytes.Contains(rec.Body.Bytes(), set) || rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s%s: %d %v %s", c.host, c.path, rec.Code, rec.Header(), rec.Body)
		}
	}
	if ks.asked != asked {
		t.Fatalf("another host or path asked for the keys")
	}

	// The service cannot give the set: 503, never cached, nothing of it.
	ks.err = errors.New("the root secret of wk1 is not unlocked")
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		rec := a.serve(httptest.NewRequest(m, "https://"+apiHost+webhook.KeysPath, nil))
		if rec.Code != 503 || rec.Header().Get("Cache-Control") != "no-store" || bytes.Contains(rec.Body.Bytes(), set) {
			t.Fatalf("%s unavailable: %d %v", m, rec.Code, rec.Header())
		}
	}
	ks.err, ks.set = nil, nil
	if rec := get(t, a, apiHost, webhook.KeysPath); rec.Code != 503 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("empty: %d %v", rec.Code, rec.Header())
	}
	out := a.logs.String()
	if !strings.Contains(out, `"route":"webhook_keys"`) || !strings.Contains(out, `"kind":"unavailable"`) || strings.Contains(out, "root secret") {
		t.Fatalf("log: %s", out)
	}
	for _, k := range keys {
		if strings.Contains(out, webhook.LibraryKey(k)) || strings.Contains(out, string(set)) {
			t.Fatalf("a key in the log: %s", out)
		}
	}

	// A client that left is not an error of the service's: not logged as one.
	ks.err = context.Canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.logs.Reset()
	if rec := a.serve(httptest.NewRequestWithContext(ctx, http.MethodGet, "https://"+apiHost+webhook.KeysPath, nil)); rec.Code != 503 {
		t.Fatalf("left: %d", rec.Code)
	}
	if out := a.logs.String(); strings.Contains(out, "unavailable") || !strings.Contains(out, `"outcome":"client_left"`) {
		t.Fatalf("log: %s", out)
	}

	// Without a KeySet, nothing is published.
	b := newConnApp(t)
	if rec := get(t, b, apiHost, webhook.KeysPath); rec.Code != 404 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("published without keys: %d %v", rec.Code, rec.Header())
	}
}

// vendorWebhook is a connection's webhook as a vendor is told it.
var vendorWebhook = &contracts.VendorWebhook{
	Subscription: "sub_ABCDEFGHIJKLMNOPQRSTUVWXYZ", URL: "https://trip-app.com/hooks/permaily/u1", NewMail: true,
	Schedule: &contracts.WebhookSchedule{Every: contracts.EveryWeek, On: "monday", At: "08:00"}, State: contracts.WebhookOn,
}

// K53. GET /v1/connection and the MCP tool connection_info say the webhook
// as the service gives it (the vendor's own: its subscription, its URL,
// what it is called for, its state; never the person's time zone, which the
// contract's VendorWebhook does not hold), and the person's GET
// /api/v1/connections/{id} the person's (with the zone, the next run, the
// failure).
func TestK53TheConnectionSaysItsWebhookAsTheServiceGivesIt(t *testing.T) {
	a := newConnApp(t)
	grants(a.fake, apiGrant)
	conn := contracts.VendorConnection{Parts: apiGrant.Parts, Since: contracts.MailSince{New: true},
		EndsAt: time.Date(2027, 2, 14, 0, 0, 0, 0, time.UTC), Delivery: contracts.DeliveryOn, Webhook: vendorWebhook}
	a.fake.InfoFunc = func(context.Context, *oauth.Grant) (*contracts.VendorConnection, error) {
		c := conn
		return &c, nil
	}
	rec := a.vendor(apiToken, "GET", "/v1/connection", "")
	b, _ := json.Marshal(conn)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != string(b) {
		t.Fatalf("%d %s, want %s", rec.Code, rec.Body, b)
	}
	const wantWebhook = `"webhook":{"subscription":"sub_ABCDEFGHIJKLMNOPQRSTUVWXYZ","url":"https://trip-app.com/hooks/permaily/u1",` +
		`"new_mail":true,"schedule":{"every":"week","on":"monday","at":"08:00"},"state":"on"}`
	if !strings.Contains(rec.Body.String(), wantWebhook) || strings.Contains(rec.Body.String(), "time_zone") {
		t.Fatalf("the vendor is told %s", rec.Body)
	}
	// Without a webhook, nothing of one (ADR 0030's shape).
	conn.Webhook = nil
	if rec := a.vendor(apiToken, "GET", "/v1/connection", ""); rec.Code != 200 || strings.Contains(rec.Body.String(), "webhook") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	conn.Webhook = vendorWebhook
	if out := a.logs.String(); strings.Contains(out, "trip-app.com") || strings.Contains(out, "sub_") {
		t.Fatalf("log: %s", out)
	}

	// connection_info, through the MCP server, says the same.
	a.fake.CheckFunc = func(_ context.Context, r contracts.Resource, bearer string) (*oauth.Grant, error) {
		if bearer != mcpToken || r != contracts.ResourceMCP {
			return nil, connections.ErrInvalidToken
		}
		g := *apiGrant
		g.Resource = contracts.ResourceMCP
		return &g, nil
	}
	mcp := mcpapi.New(a.fake, testOAuth)
	r := httptest.NewRequest(http.MethodPost, "https://"+mcpHost+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"connection_info","arguments":{}}}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("Authorization", "Bearer "+mcpToken)
	r.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	r = r.WithContext(mcpapi.ConnContext(r.Context(), memConn{}))
	out := httptest.NewRecorder()
	mcp.ServeHTTP(out, r)
	var res struct {
		Result struct {
			StructuredContent json.RawMessage `json:"structuredContent"`
			IsError           bool            `json:"isError"`
		} `json:"result"`
	}
	if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &res) != nil || res.Result.IsError {
		t.Fatalf("connection_info: %d %s", out.Code, out.Body)
	}
	var gotMCP, wantMCP any
	if json.Unmarshal(res.Result.StructuredContent, &gotMCP) != nil || json.Unmarshal(b, &wantMCP) != nil ||
		fmt.Sprint(gotMCP) != fmt.Sprint(wantMCP) || !bytes.Contains(res.Result.StructuredContent, []byte(`"subscription":"sub_ABCDEFGHIJKLMNOPQRSTUVWXYZ"`)) {
		t.Fatalf("connection_info says %s, the API %s", res.Result.StructuredContent, b)
	}

	// The person's page: the person's view of it, with the zone.
	next := time.Date(2026, 11, 23, 7, 0, 0, 0, time.UTC)
	person := &contracts.Connection{ID: "con_1", State: contracts.ConnectionActive, Version: 3,
		Webhook: &contracts.ConnectionWebhook{Host: "trip-app.com", NewMail: true, Schedule: vendorWebhook.Schedule, TimeZone: "Europe/Madrid",
			State: contracts.WebhookOn, NextAt: next, FailingSince: next.Add(-48 * time.Hour), Failure: contracts.WebhookNoAnswer}}
	a.fake.GetFunc = func(context.Context, connections.Person, string) (*contracts.Connection, error) { return person, nil }
	rec = a.app("fresh", "GET", "/api/v1/connections/con_1", "")
	pb, _ := json.Marshal(person)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != string(pb) || !strings.Contains(rec.Body.String(), `"time_zone":"Europe/Madrid"`) {
		t.Fatalf("%d %s, want %s", rec.Code, rec.Body, pb)
	}
}

// memConn is the connection of a request made in memory, as the server
// keeps it (mcpapi.ConnContext): a tool call is answered only then.
type memConn struct{ net.Conn }

func (memConn) SetWriteDeadline(time.Time) error { return nil }
func (memConn) Close() error                     { return nil }
