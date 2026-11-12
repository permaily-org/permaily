package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/services/accounts"
)

// providers plays Google, GitHub, Meta and Microsoft: it sends the browser
// to an address of its own, and for a code it gave it says who signed in.
type providers struct {
	mu    sync.Mutex
	who   map[string]idp.Identity
	codes int
	// secret is the secret Finish was last asked with.
	secret string
}

func (p *providers) Begin(provider idp.Provider, _ idp.App, state string) (*idp.Started, error) {
	return &idp.Started{URL: "https://" + string(provider) + ".test/authorize?state=" + url.QueryEscape(state), Verifier: "verifier"}, nil
}

func (p *providers) Finish(_ context.Context, provider idp.Provider, app idp.App, code string, _ idp.Started) (*idp.Identity, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.secret = app.ClientSecret
	who, ok := p.who[code]
	delete(p.who, code)
	if !ok || who.Provider != provider {
		return nil, fmt.Errorf("%w: a code that was not given", idp.ErrInvalid)
	}
	return &who, nil
}

// approve is the person signing in at the provider: the code that the
// browser comes back with.
func (p *providers) approve(who idp.Identity) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.codes++
	code := fmt.Sprintf("code-%d-of-the-provider", p.codes)
	p.who[code] = who
	return code
}

const providerSecret = "GOCSPX-test~Secret_0001"

// on turns providers on, the way the operator does.
func (a *app) on(names ...string) {
	a.t.Helper()
	for _, name := range names {
		if _, err := a.svc.SetProviderApp(context.Background(), name, contracts.ProviderAppSet{ClientID: "client-of-" + name, ClientSecret: providerSecret}); err != nil {
			a.t.Fatal(err)
		}
	}
}

// leave starts a trip to a provider the way the page does, and returns the
// state the provider will send back. The browser holds the trip's cookie.
func (a *app) leave(path, body string) string {
	a.t.Helper()
	rec := a.call("POST", path, body, nil)
	var started contracts.ProviderStarted
	read(a.t, rec, 200, &started)
	u, err := url.Parse(started.URL)
	if err != nil || !strings.HasSuffix(u.Host, ".test") || started.ExpiresIn != 600 || u.Query().Get("state") == "" {
		a.t.Fatalf("started: %s", rec.Body)
	}
	// The cookie is for this host alone and out of reach of scripts, and it
	// comes along when the provider sends the browser back.
	c := a.trip
	if c == nil || c.Name != "__Host-permaily-provider" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" || c.MaxAge != 600 || len(c.Value) != 43 {
		a.t.Fatalf("the cookie of a trip: %+v", c)
	}
	if strings.Contains(rec.Body.String(), c.Value) {
		a.t.Fatal("the value of the cookie is in the answer")
	}
	return u.Query().Get("state")
}

// back is the provider sending the browser back: a navigation from another
// site, which carries the trip's cookie (Lax) and not the session's
// (Strict). It returns where the browser is sent, which must be in the app.
func (a *app) back(query string) (*httptest.ResponseRecorder, string) {
	a.t.Helper()
	session := a.cookie
	rec := a.call("GET", "/api/v1/auth/provider/callback?"+query, "", func(r *http.Request) {
		r.Header.Del("Origin")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.Header.Del("Cookie")
		if a.trip != nil {
			r.AddCookie(a.trip)
		}
	})
	if a.cookie == nil {
		a.cookie = session // a navigation drops no session it did not carry
	}
	to := rec.Header().Get("Location")
	u, err := url.Parse(to)
	if rec.Code != http.StatusSeeOther || err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(to, "/") || strings.HasPrefix(to, "//") || strings.Contains(to, `\`) {
		a.t.Fatalf("the callback sends the browser to %q (%d)", to, rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		a.t.Fatal("the answer of the callback may be cached")
	}
	return rec, to
}

// comeBack ends a trip as the person who signed in at the provider.
func (a *app) comeBack(state string, who idp.Identity) (*httptest.ResponseRecorder, string) {
	a.t.Helper()
	return a.back("state=" + url.QueryEscape(state) + "&code=" + url.QueryEscape(a.idp.approve(who)))
}

// set returns the cookie an answer sets under a name, nil when it sets none.
func set(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestProvidersAPI(t *testing.T) {
	a := newApp(t)
	var on contracts.ProvidersOn
	rec := a.call("GET", "/api/v1/auth/providers", "", nil)
	if read(t, rec, 200, &on); len(on.Providers) != 0 || !strings.Contains(rec.Body.String(), `"providers":[]`) {
		t.Fatalf("none on: %s", rec.Body)
	}
	a.on("google", "meta")
	if read(t, a.call("GET", "/api/v1/auth/providers", "", nil), 200, &on); len(on.Providers) != 2 || on.Providers[0] != "google" || on.Providers[1] != "meta" {
		t.Fatalf("on: %+v", on)
	}
	inv, err := a.svc.CreateInvitations(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	ana := idp.Identity{Provider: idp.Google, Subject: "g-sub-ana-1081", Address: "ana@example.org", Verified: true, Name: "Ana At Google"}

	// A sign-up: the page asks where to send the browser, the provider sends
	// it back, and the callback sends it on, signed in.
	state := a.leave("/api/v1/auth/provider/start", `{"provider":"google","invitation":"`+inv.Invitations[0]+`","return_to":"/connections?new=1"}`)
	rec, to := a.comeBack(state, ana)
	if to != "/connections?new=1" {
		t.Fatalf("sent to %q", to)
	}
	c := set(rec, "__Host-permaily-session")
	if c == nil || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge != 30*24*3600 {
		t.Fatalf("the session's cookie: %+v", c)
	}
	// The trip is over: its cookie goes.
	if gone := set(rec, "__Host-permaily-provider"); gone == nil || gone.MaxAge >= 0 || gone.Value != "" || a.trip != nil {
		t.Fatalf("the trip's cookie after the callback: %+v", gone)
	}
	var me contracts.Me
	if read(t, a.call("GET", "/api/v1/me", "", nil), 200, &me); me.Account.Address != "ana@example.org" || me.Session.Device != "test browser" {
		t.Fatalf("me: %+v", me)
	}
	if a.idp.secret != providerSecret {
		t.Fatalf("the provider was asked with %q", a.idp.secret)
	}
	// The answer worked once.
	if _, to := a.comeBack(state, ana); to != "/sign-in#provider_error=provider_failed" {
		t.Fatalf("the same state again: %q", to)
	}

	// Signing in again, with nothing to say where to: home.
	read(t, a.call("POST", "/api/v1/auth/sign-out", "", nil), 204, nil)
	state = a.leave("/api/v1/auth/provider/start", `{"provider":"google"}`)
	if _, to := a.comeBack(state, ana); to != "/" || a.cookie == nil {
		t.Fatalf("signing in again: %q", to)
	}

	// The answer of the provider in a browser that did not start the trip:
	// one with no cookie, and one on a trip of its own. Nobody is signed in.
	read(t, a.call("POST", "/api/v1/auth/sign-out", "", nil), 204, nil)
	state = a.leave("/api/v1/auth/provider/start", `{"provider":"google"}`)
	started := a.trip
	other := newApp(t)
	other.h, other.svc, other.out, other.idp = a.h, a.svc, a.out, a.idp
	for range 2 {
		rec, to = other.comeBack(state, ana)
		if to != "/sign-in#provider_error=provider_failed" || set(rec, "__Host-permaily-session") != nil || other.cookie != nil {
			t.Fatalf("in another browser: %q, cookies %v", to, rec.Result().Cookies())
		}
		if rec := other.call("GET", "/api/v1/me", "", nil); rec.Code != 401 {
			t.Fatalf("signed in by an answer carried over: %d", rec.Code)
		}
		// The second time it is on a trip of its own, with a cookie of its own.
		state = a.leave("/api/v1/auth/provider/start", `{"provider":"google"}`)
		other.leave("/api/v1/auth/provider/start", `{"provider":"google"}`)
	}
	// And the first trip was over when its answer was tried elsewhere.
	a.trip = started
	if _, to := a.comeBack(state, ana); to != "/sign-in#provider_error=provider_failed" {
		t.Fatalf("a trip whose answer was tried elsewhere: %q", to)
	}

	// The provider says the person refused, in words of its own: they are
	// not read, and not sent on.
	state = a.leave("/api/v1/auth/provider/start", `{"provider":"google"}`)
	rec, to = a.back("state=" + state + "&error=access_denied&error_description=" + url.QueryEscape(`<script>alert("ana@example.org")</script>`) + "&code=" + a.idp.approve(ana))
	// The trip is one the server still knows, so the screen is told which
	// provider it was.
	if to != "/sign-in#provider_error=provider_failed&provider=google" || set(rec, "__Host-permaily-session") != nil || strings.Contains(rec.Body.String()+fmt.Sprint(rec.Header()), "access_denied") || strings.Contains(rec.Body.String(), "script") {
		t.Fatalf("refused at the provider: %q %s", to, rec.Body)
	}
	for _, query := range []string{"", "state=" + state, "code=x", "state=&code=", "state=" + strings.Repeat("s", 5000) + "&code=x", "error=server_error"} {
		if rec, to := a.back(query); to != "/sign-in#provider_error=provider_failed" || set(rec, "__Host-permaily-session") != nil {
			t.Errorf("?%s: %q", query, to)
		}
	}

	// The callback cannot be made to send the browser out of the app.
	for _, leaving := range []string{"https://evil.example", "//evil.example", `/\\evil.example`, "/ok\\r\\nLocation: https://evil.example", "evil.example", "javascript:alert(1)"} {
		state = a.leave("/api/v1/auth/provider/start", `{"provider":"google","return_to":"`+leaving+`"}`)
		if rec, to := a.comeBack(state, ana); to != "/" || len(rec.Header().Values("Location")) != 1 {
			t.Errorf("come back to %q: sent to %q", leaving, to)
		}
	}

	// What the page cannot ask.
	var e httpapi.APIError
	for _, c := range []struct {
		name, body string
		edit       func(*http.Request)
		status     int
		code       string
	}{
		{"a provider that is off", `{"provider":"github"}`, nil, 409, "provider_off"},
		{"no provider", `{}`, nil, 409, "provider_off"},
		{"an invitation that is not one", `{"provider":"google","invitation":"abcd-2345-wxyz"}`, nil, 400, "invalid_invitation"},
		{"an address", `{"provider":"google","address":"ana@example.org"}`, nil, 400, "invalid_body"},
		{"not JSON", `provider=google`, nil, 400, "invalid_body"},
		{"another origin", `{"provider":"google"}`, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, 403, "forbidden"},
		{"cross site", `{"provider":"google"}`, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403, "forbidden"},
	} {
		rec := a.call("POST", "/api/v1/auth/provider/start", c.body, c.edit)
		if read(t, rec, c.status, &e); e.Error.Code != c.code || set(rec, "__Host-permaily-provider") != nil {
			t.Errorf("%s: %s", c.name, rec.Body)
		}
	}
	if rec := a.call("POST", "/api/v1/auth/provider/callback?state=x&code=y", "", nil); rec.Code != 404 {
		t.Errorf("the callback by POST: %d", rec.Code)
	}

	for _, secret := range []string{"ana@", "example.org", "g-sub-ana-1081", "Ana At Google", me.Account.ID, providerSecret, "code-", "access_denied", "script", inv.Invitations[0], started.Value} {
		if strings.Contains(a.logs.String(), secret) {
			t.Fatalf("the logs hold %q:\n%s", secret, a.logs)
		}
	}
}

// A provider that does not vouch for an address leaves the browser on the
// screen that asks for one, and the code of a message ends it.
func TestProviderConfirmAPI(t *testing.T) {
	a := newApp(t)
	a.on("meta")
	me := a.signIn("ana@example.org")
	read(t, a.call("POST", "/api/v1/auth/sign-out", "", nil), 204, nil)
	who := idp.Identity{Provider: idp.Meta, Subject: "880022", Address: "Ana@Example.org"}

	var e httpapi.APIError
	if read(t, a.call("GET", "/api/v1/auth/provider/pending", "", nil), 400, &e); e.Error.Code != "provider_failed" {
		t.Fatalf("nothing waits: %+v", e)
	}
	state := a.leave("/api/v1/auth/provider/start", `{"provider":"meta","return_to":"/rules"}`)
	rec, to := a.comeBack(state, who)
	if to != "/sign-in#provider_confirm" || set(rec, "__Host-permaily-session") != nil || a.cookie != nil {
		t.Fatalf("an address that is not vouched for: %q", to)
	}
	// The cookie is another one now, for what waits.
	c := a.trip
	if c == nil || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != 1200 || len(c.Value) != 43 {
		t.Fatalf("the cookie of what waits: %+v", c)
	}
	rec = a.call("GET", "/api/v1/auth/provider/pending", "", nil)
	var pending contracts.ProviderPending
	if read(t, rec, 200, &pending); pending.Provider != "meta" || pending.Address != "ana@example.org" || pending.ReturnTo != "/rules" || pending.ExpiresIn < 1190 || pending.ExpiresIn > 1200 {
		t.Fatalf("what waits: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "880022") {
		t.Fatalf("the answer names the provider account: %s", rec.Body)
	}

	// Another browser, and another site's page, get nothing of it.
	other := newApp(t)
	other.h, other.svc = a.h, a.svc
	for _, c := range [][3]string{
		{"GET", "/api/v1/auth/provider/pending", ""}, {"POST", "/api/v1/auth/provider/confirm/start", `{"address":"ana@example.org"}`},
		{"POST", "/api/v1/auth/provider/confirm/finish", `{"attempt":"att_x","code":"123456"}`},
	} {
		if read(t, other.call(c[0], c[1], c[2], nil), 400, &e); e.Error.Code != "provider_failed" {
			t.Errorf("%s in another browser: %+v", c[1], e)
		}
		if c[0] == "POST" {
			evil := func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }
			if read(t, a.call(c[0], c[1], c[2], evil), 403, &e); e.Error.Code != "forbidden" {
				t.Errorf("%s from another origin: %+v", c[1], e)
			}
		}
	}

	var started contracts.EmailStarted
	read(t, a.call("POST", "/api/v1/auth/provider/confirm/start", `{"address":"ana@example.org"}`, nil), 200, &started)
	a.svc.Wait()
	sent := a.out.Sent()
	code := sentCode.FindStringSubmatch(sent[len(sent)-1].Text)[1]
	wrong := "000000"
	if code == wrong {
		wrong = "000001"
	}
	for body, want := range map[string]string{
		`{"attempt":"` + started.Attempt + `","code":"` + wrong + `"}`: "invalid_code",
		`{"attempt":"att_x","code":"` + code + `"}`:                    "invalid_code",
		`{"attempt":"` + started.Attempt + `","token":"x"}`:            "invalid_body",
		`{"address":"nope"}`: "invalid_body",
	} {
		rec := a.call("POST", "/api/v1/auth/provider/confirm/finish", body, nil)
		if read(t, rec, 400, &e); e.Error.Code != want || a.cookie != nil || a.trip == nil {
			t.Errorf("%s: %s", body, rec.Body)
		}
	}
	if read(t, a.call("POST", "/api/v1/auth/provider/confirm/start", `{"address":"nope"}`, nil), 400, &e); e.Error.Code != "invalid_address" {
		t.Fatalf("not an address: %+v", e)
	}
	rec = a.call("POST", "/api/v1/auth/provider/confirm/finish", `{"attempt":"`+started.Attempt+`","code":"`+code+`"}`, nil)
	var in contracts.Me
	if read(t, rec, 200, &in); in.Account.ID != me.Account.ID || a.cookie == nil || a.trip != nil {
		t.Fatalf("confirmed: %s, session %v, trip %v", rec.Body, a.cookie != nil, a.trip != nil)
	}
	if gone := set(rec, "__Host-permaily-provider"); gone == nil || gone.MaxAge >= 0 {
		t.Fatalf("the cookie of what waited: %+v", gone)
	}
	var linked contracts.LinkedProviders
	rec = a.call("GET", "/api/v1/providers", "", nil)
	if read(t, rec, 200, &linked); len(linked.Providers) != 1 || linked.Providers[0].Provider != "meta" || time.Since(linked.Providers[0].LinkedAt) > time.Minute {
		t.Fatalf("the providers of the account: %s", rec.Body)
	}
	// What is shown of a linked provider: which, and since when.
	if !strings.Contains(rec.Body.String(), `{"providers":[{"provider":"meta","linked_at":"`) || strings.Contains(rec.Body.String(), "880022") || strings.Contains(rec.Body.String(), "example.org") {
		t.Fatalf("the list: %s", rec.Body)
	}
	for _, secret := range []string{"ana@", "example.org", "880022", me.Account.ID, c.Value, code} {
		if strings.Contains(a.logs.String(), secret) {
			t.Fatalf("the logs hold %q:\n%s", secret, a.logs)
		}
	}
}

// Linking and unlinking from Settings.
func TestLinkedProvidersAPI(t *testing.T) {
	a := newApp(t)
	a.on("google", "github")
	var e httpapi.APIError
	for _, c := range [][3]string{{"GET", "/api/v1/providers", ""}, {"POST", "/api/v1/providers/start", `{"provider":"google"}`}, {"DELETE", "/api/v1/providers/google", ""}} {
		if read(t, a.call(c[0], c[1], c[2], nil), 401, &e); e.Error.Code != "unauthorized" {
			t.Errorf("%s %s without a session: %+v", c[0], c[1], e)
		}
	}
	ana := a.signIn("ana@example.org")
	eve := newApp(t)
	eve.h, eve.svc, eve.out, eve.idp = a.h, a.svc, a.out, a.idp
	eve.signIn("eve@example.org")

	rec := a.call("GET", "/api/v1/providers", "", nil)
	var linked contracts.LinkedProviders
	if read(t, rec, 200, &linked); len(linked.Providers) != 0 || !strings.Contains(rec.Body.String(), `"providers":[]`) {
		t.Fatalf("none linked: %s", rec.Body)
	}
	hers := idp.Identity{Provider: idp.Google, Subject: "g-sub-ana", Address: "work@example.net", Verified: true}
	state := a.leave("/api/v1/providers/start", `{"provider":"google"}`)
	// The provider sends the browser back without the session's cookie, and
	// the callback gives it no other: it is linked to the account of the
	// session that started the trip.
	session := a.cookie
	rec, to := a.comeBack(state, hers)
	if to != "/settings" || set(rec, "__Host-permaily-session") != nil || a.cookie != session || a.trip != nil {
		t.Fatalf("linked: %q, cookies %v", to, rec.Result().Cookies())
	}
	if read(t, a.call("GET", "/api/v1/providers", "", nil), 200, &linked); len(linked.Providers) != 1 || linked.Providers[0].Provider != "google" {
		t.Fatalf("after linking: %+v", linked)
	}
	// Eve's Settings cannot take it, and an address that is Ana's is not
	// linked from Eve's either.
	state = eve.leave("/api/v1/providers/start", `{"provider":"google","return_to":"/settings/sign-in"}`)
	if _, to := eve.comeBack(state, hers); to != "/settings#provider_error=provider_in_use&provider=google" {
		t.Fatalf("a provider account of another account: %q", to)
	}
	state = eve.leave("/api/v1/providers/start", `{"provider":"github"}`)
	if _, to := eve.comeBack(state, idp.Identity{Provider: idp.GitHub, Subject: "5150", Address: "ana@example.org", Verified: true}); to != "/settings#provider_error=provider_in_use&provider=github" {
		t.Fatalf("an address that is another account's: %q", to)
	}
	if read(t, eve.call("GET", "/api/v1/providers", "", nil), 200, &linked); len(linked.Providers) != 0 {
		t.Fatalf("eve's providers: %+v", linked)
	}
	// Ana's answer opened in Eve's browser, who is signed in and on a trip
	// of her own: nothing is linked to either.
	state = a.leave("/api/v1/providers/start", `{"provider":"github"}`)
	eve.leave("/api/v1/providers/start", `{"provider":"github"}`)
	if _, to := eve.comeBack(state, idp.Identity{Provider: idp.GitHub, Subject: "6160"}); to != "/sign-in#provider_error=provider_failed" {
		t.Fatalf("an answer in another browser: %q", to)
	}
	if read(t, a.call("GET", "/api/v1/providers", "", nil), 200, &linked); len(linked.Providers) != 1 {
		t.Fatalf("ana's providers: %+v", linked)
	}

	for _, c := range []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"a second account of one provider", "POST", "/api/v1/providers/start", `{"provider":"google"}`, 409, "provider_linked"},
		{"a provider that is off", "POST", "/api/v1/providers/start", `{"provider":"meta"}`, 409, "provider_off"},
		{"not JSON", "POST", "/api/v1/providers/start", `provider=github`, 400, "invalid_body"},
		{"unlinking what is not linked", "DELETE", "/api/v1/providers/github", "", 404, "not_found"},
		{"unlinking what is no provider", "DELETE", "/api/v1/providers/nope", "", 404, "not_found"},
		{"unlinking nothing", "DELETE", "/api/v1/providers/", "", 404, "not_found"},
	} {
		if read(t, a.call(c.method, c.path, c.body, nil), c.status, &e); e.Error.Code != c.code {
			t.Errorf("%s: %+v", c.name, e)
		}
	}
	evil := func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }
	if read(t, a.call("DELETE", "/api/v1/providers/google", "", evil), 403, &e); e.Error.Code != "forbidden" {
		t.Fatalf("unlinking from another origin: %+v", e)
	}
	read(t, a.call("DELETE", "/api/v1/providers/google", "", nil), 204, nil)
	if read(t, a.call("GET", "/api/v1/providers", "", nil), 200, &linked); len(linked.Providers) != 0 {
		t.Fatalf("after unlinking: %+v", linked)
	}
	// While the account is closing nothing of it changes.
	state = a.leave("/api/v1/providers/start", `{"provider":"google"}`)
	read(t, a.call("POST", "/api/v1/account/delete", "", nil), 200, nil)
	if _, to := a.comeBack(state, hers); to != "/settings#provider_error=account_closing&provider=google" {
		t.Fatalf("linking to a closing account: %q", to)
	}
	if read(t, a.call("POST", "/api/v1/providers/start", `{"provider":"google"}`, nil), 409, &e); e.Error.Code != "account_closing" {
		t.Fatalf("starting from a closing account: %+v", e)
	}
	for _, secret := range []string{"ana@", "example.org", "example.net", "g-sub-ana", "5150", "6160", ana.Account.ID} {
		if strings.Contains(a.logs.String(), secret) {
			t.Fatalf("the logs hold %q:\n%s", secret, a.logs)
		}
	}
}

// brokenProviders is a service whose storage is down while a trip to a
// provider ends.
type brokenProviders struct {
	httpapi.AccountsService
	err error
}

func (b brokenProviders) ProvidersOn(context.Context) (*contracts.ProvidersOn, error) {
	return nil, b.err
}

func (b brokenProviders) StartProvider(context.Context, *accounts.Authenticated, contracts.ProviderStart) (*contracts.ProviderStarted, string, error) {
	return nil, "", b.err
}

func (b brokenProviders) FinishProvider(context.Context, accounts.ProviderAnswer) (*accounts.ProviderDone, error) {
	return &accounts.ProviderDone{Path: "/sign-in#provider_error=provider_unavailable"}, b.err
}

// namedProviders is a service whose trips end as a test says.
type namedProviders struct {
	httpapi.AccountsService
	done accounts.ProviderDone
	err  error
}

func (n namedProviders) FinishProvider(context.Context, accounts.ProviderAnswer) (*accounts.ProviderDone, error) {
	return &n.done, n.err
}

// The callback names the provider of a trip that failed next to the reason,
// and only ever one of the four fixed identifiers: the fragment holds
// nothing else, whatever the service answers.
func TestProviderCallbackNamesTheProvider(t *testing.T) {
	a := newApp(t)
	log := slog.New(slog.NewTextHandler(a.logs, nil))
	failed := "/settings#provider_error=provider_in_use"
	for _, c := range []struct {
		name     string
		provider idp.Provider
		path     string
		err      error
		want     string
	}{
		{"google", idp.Google, failed, contracts.ErrProviderInUse, failed + "&provider=google"},
		{"github", idp.GitHub, failed, contracts.ErrProviderInUse, failed + "&provider=github"},
		{"meta", idp.Meta, failed, contracts.ErrProviderInUse, failed + "&provider=meta"},
		{"microsoft", idp.Microsoft, failed, contracts.ErrProviderInUse, failed + "&provider=microsoft"},
		{"the service's own failure", idp.Google, "/sign-in#provider_error=provider_unavailable", errors.New("down"), "/sign-in#provider_error=provider_unavailable&provider=google"},
		{"no trip the service knows", "", failed, contracts.ErrProviderFailed, failed},
		{"not a provider", "apple", failed, contracts.ErrProviderFailed, failed},
		{"words in its place", "google&linked=google", failed, contracts.ErrProviderFailed, failed},
		{"an address in its place", "ana@example.org", failed, contracts.ErrProviderFailed, failed},
		{"another case", "Google", failed, contracts.ErrProviderFailed, failed},
		// A trip that worked says nothing of its provider: the path is where
		// the person was going, as it was.
		{"a trip that worked", idp.Google, "/connections?new=1", nil, "/connections?new=1"},
		{"linked from Settings", idp.Google, "/settings#linked=google", nil, "/settings#linked=google"},
	} {
		a.h = httpapi.AccountsAPI(namedProviders{done: accounts.ProviderDone{Path: c.path, Provider: c.provider}, err: c.err}, log)
		if _, to := a.back("state=some-state&code=some-code"); to != c.want {
			t.Errorf("%s: sent to %q, want %q", c.name, to, c.want)
		}
	}
}

func (b brokenProviders) PendingProvider(context.Context, string) (*contracts.ProviderPending, error) {
	return nil, b.err
}

func (b brokenProviders) StartProviderConfirm(context.Context, string, contracts.ProviderConfirmStart) (*contracts.EmailStarted, error) {
	return nil, b.err
}

func (b brokenProviders) FinishProviderConfirm(context.Context, string, contracts.ProviderConfirm, string) (*contracts.Me, string, error) {
	return nil, "", b.err
}

func (b brokenProviders) ProviderApps(context.Context) (*contracts.ProviderApps, error) {
	return nil, b.err
}

func (b brokenProviders) SetProviderApp(context.Context, string, contracts.ProviderAppSet) (*contracts.ProviderApp, error) {
	return nil, b.err
}

func (b brokenProviders) RemoveProviderApp(context.Context, string) error { return b.err }

func TestProvidersAPIInternalErrors(t *testing.T) {
	a := newApp(t)
	id := "acc_" + strings.Repeat("a", 26)
	log := slog.New(slog.NewTextHandler(a.logs, nil))
	down := brokenProviders{err: errors.New("store: commits of account-" + id + ": down")}
	a.h = httpapi.AccountsAPI(down, log)
	a.trip = &http.Cookie{Name: httpapi.ProviderCookie, Value: "a-cookie-of-a-trip"}

	// The callback still answers with where to go, and says nothing of what failed.
	rec, to := a.back("state=some-state&code=some-code")
	if to != "/sign-in#provider_error=provider_unavailable" || set(rec, "__Host-permaily-session") != nil || strings.Contains(rec.Body.String(), "store") {
		t.Fatalf("the callback with the storage down: %q %s", to, rec.Body)
	}
	for _, c := range [][3]string{
		{"GET", "/api/v1/auth/providers", ""}, {"POST", "/api/v1/auth/provider/start", `{"provider":"google"}`},
		{"GET", "/api/v1/auth/provider/pending", ""}, {"POST", "/api/v1/auth/provider/confirm/start", `{"address":"ana@example.org"}`},
		{"POST", "/api/v1/auth/provider/confirm/finish", `{"attempt":"att_x","code":"123456"}`},
	} {
		rec := a.call(c[0], c[1], c[2], nil)
		if rec.Code != 500 || strings.Contains(rec.Body.String(), "store") || !strings.Contains(rec.Body.String(), `"internal"`) || set(rec, "__Host-permaily-session") != nil {
			t.Fatalf("%s: %d %s", c[1], rec.Code, rec.Body)
		}
	}
	h := httpapi.TokenAuth(tokenHash(), httpapi.ProviderApps(down, log))
	for _, c := range [][3]string{{"GET", "/v1/admin/providers", ""}, {"PUT", "/v1/admin/providers/google", `{"client_id":"id","client_secret":"` + providerSecret + `"}`}, {"DELETE", "/v1/admin/providers/google", ""}} {
		rec := do(h, c[0], c[1], token, c[2])
		if rec.Code != 500 || strings.Contains(rec.Body.String(), "store") || strings.Contains(rec.Body.String(), "Secret") {
			t.Fatalf("%s %s: %d %s", c[0], c[1], rec.Code, rec.Body)
		}
	}
	// A refusal of the callback is not a failure, and is not logged as one.
	failures := strings.Count(a.logs.String(), "api: internal error")
	a.h = httpapi.AccountsAPI(brokenProviders{err: contracts.ErrProviderFailed}, log)
	if _, to := a.back("state=x&code=y"); to != "/sign-in#provider_error=provider_unavailable" || strings.Count(a.logs.String(), "api: internal error") != failures {
		t.Fatalf("a refusal: %q\n%s", to, a.logs)
	}
	if logs := a.logs.String(); !strings.Contains(logs, "commits of account-acc_…: down") || strings.Contains(logs, id) || strings.Contains(logs, "some-state") || strings.Contains(logs, "some-code") || strings.Contains(logs, "a-cookie-of-a-trip") || strings.Contains(logs, "Secret") {
		t.Fatalf("logs:\n%s", logs)
	}
}

// The operator gives the enclave each application's secret, and is never
// given it back.
func TestProviderAppsAPI(t *testing.T) {
	a := newApp(t)
	log := slog.New(slog.NewTextHandler(a.logs, nil))
	h := httpapi.TokenAuth(tokenHash(), httpapi.ProviderApps(a.svc, log))
	body := `{"client_id":"1234-abc.apps.googleusercontent.com","client_secret":"` + providerSecret + `"}`
	var answers []string
	ask := func(method, path, auth, body string) *httptest.ResponseRecorder {
		rec := do(h, method, path, auth, body)
		answers = append(answers, rec.Body.String()+fmt.Sprint(rec.Header()))
		return rec
	}

	var e httpapi.APIError
	for _, c := range [][3]string{{"GET", "/v1/admin/providers", ""}, {"PUT", "/v1/admin/providers/google", body}, {"DELETE", "/v1/admin/providers/google", ""}} {
		for _, auth := range []string{"", "not the token"} {
			if read(t, ask(c[0], c[1], auth, c[2]), 401, &e); e.Error.Code != "unauthorized" {
				t.Errorf("%s %s with %q: %+v", c[0], c[1], auth, e)
			}
		}
	}
	// The app's own API knows nothing of it, with a session or without.
	a.signIn("ana@example.org")
	if rec := a.call("PUT", "/v1/admin/providers/google", body, nil); rec.Code != 404 {
		t.Fatalf("through the app's API: %d", rec.Code)
	}
	var list contracts.ProviderApps
	rec := ask("GET", "/v1/admin/providers", token, "")
	if read(t, rec, 200, &list); len(list.Providers) != 0 || !strings.Contains(rec.Body.String(), `{"providers":[]}`) {
		t.Fatalf("none set: %s", rec.Body)
	}

	rec = ask("PUT", "/v1/admin/providers/google", token, body)
	var set contracts.ProviderApp
	if read(t, rec, 200, &set); set.Provider != "google" || set.ClientID != "1234-abc.apps.googleusercontent.com" || time.Since(set.SetAt) > time.Minute {
		t.Fatalf("set: %s", rec.Body)
	}
	var keys map[string]any
	if json.Unmarshal(rec.Body.Bytes(), &keys) != nil || len(keys) != 3 {
		t.Fatalf("the answer holds more than the provider, the client id and when: %s", rec.Body)
	}
	read(t, ask("PUT", "/v1/admin/providers/github", token, `{"client_id":"Iv1.0123456789abcdef","client_secret":"`+providerSecret+`-2"}`), 200, nil)
	rec = ask("GET", "/v1/admin/providers", token, "")
	if read(t, rec, 200, &list); len(list.Providers) != 2 || list.Providers[0] != set || list.Providers[1].Provider != "github" || list.Providers[1].ClientID != "Iv1.0123456789abcdef" {
		t.Fatalf("the list: %s", rec.Body)
	}
	// It is on for the app from that moment.
	var on contracts.ProvidersOn
	if read(t, a.call("GET", "/api/v1/auth/providers", "", nil), 200, &on); len(on.Providers) != 2 {
		t.Fatalf("on: %+v", on)
	}

	for _, c := range []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"no provider by that name", "PUT", "/v1/admin/providers/nope", body, 404, "not_found"},
		{"removing no provider by that name", "DELETE", "/v1/admin/providers/nope", "", 404, "not_found"},
		{"no secret", "PUT", "/v1/admin/providers/meta", `{"client_id":"1234"}`, 400, "invalid_provider_app"},
		{"a secret with a line break", "PUT", "/v1/admin/providers/meta", `{"client_id":"1234","client_secret":"` + providerSecret + `\n"}`, 400, "invalid_provider_app"},
		{"a field that is not one", "PUT", "/v1/admin/providers/meta", `{"client_id":"1234","client_secret":"` + providerSecret + `","tenant":"x"}`, 400, "invalid_body"},
		{"not JSON", "PUT", "/v1/admin/providers/meta", `client_secret=` + providerSecret, 400, "invalid_body"},
		{"too large", "PUT", "/v1/admin/providers/meta", `{"client_id":"1234","client_secret":"` + strings.Repeat("S", 5000) + `"}`, 400, "invalid_body"},
		{"nothing", "PUT", "/v1/admin/providers/meta", ``, 400, "invalid_body"},
		{"a secret to the list", "POST", "/v1/admin/providers", body, 405, "method_not_allowed"},
		{"a secret by POST", "POST", "/v1/admin/providers/google", body, 405, "method_not_allowed"},
		{"one provider by GET", "GET", "/v1/admin/providers/google", "", 405, "method_not_allowed"},
		{"removing them all", "DELETE", "/v1/admin/providers", "", 405, "method_not_allowed"},
		{"under a provider", "GET", "/v1/admin/providers/google/secret", "", 404, "not_found"},
		{"elsewhere", "GET", "/v1/admin/nothing", "", 404, "not_found"},
	} {
		rec := ask(c.method, c.path, token, c.body)
		if read(t, rec, c.status, &e); e.Error.Code != c.code {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
		if c.status == 405 && rec.Header().Get("Allow") == "" {
			t.Errorf("%s: no Allow", c.name)
		}
	}
	if read(t, ask("GET", "/v1/admin/providers", token, ""), 200, &list); len(list.Providers) != 2 {
		t.Fatalf("after refusals: %+v", list)
	}

	read(t, ask("DELETE", "/v1/admin/providers/github", token, ""), 204, nil)
	read(t, ask("DELETE", "/v1/admin/providers/github", token, ""), 204, nil)
	if read(t, ask("GET", "/v1/admin/providers", token, ""), 200, &list); len(list.Providers) != 1 || list.Providers[0].Provider != "google" {
		t.Fatalf("after removing: %+v", list)
	}
	if read(t, a.call("POST", "/api/v1/auth/provider/start", `{"provider":"github"}`, nil), 409, &e); e.Error.Code != "provider_off" {
		t.Fatalf("starting with a provider that was removed: %+v", e)
	}

	// No answer, no log line and nothing readable in the storage holds a secret.
	for _, answer := range answers {
		if strings.Contains(answer, "Secret") || strings.Contains(answer, "client_secret\":") {
			t.Fatalf("an answer holds a secret: %s", answer)
		}
	}
	if strings.Contains(a.logs.String(), "Secret") || strings.Contains(a.logs.String(), "googleusercontent") {
		t.Fatalf("the logs hold a secret:\n%s", a.logs)
	}
	for _, name := range a.mem.RecordNames() {
		rec, _ := a.mem.GetRecord(context.Background(), name)
		if strings.Contains(string(rec.Value), "Secret") || strings.Contains(string(rec.Value), "googleusercontent") || strings.Contains(string(rec.WrappedKey), "Secret") {
			t.Fatalf("record %s holds a secret in the clear", name)
		}
	}
	for _, e := range a.mem.AllEntries() {
		if strings.Contains(string(e.Value)+e.Partition+e.Key, "Secret") {
			t.Fatalf("entry %s %s holds a secret", e.Partition, e.Key)
		}
	}
}
