package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/services/accounts"
)

// ADR 0026, "Corrected while building pull request 5": every route under
// /api/ on the app's host refuses a request that says another site made it,
// before a session is looked up and before a handler runs, and the answer is
// the same with the session cookie or without it.

const forbiddenBody = `{"error":{"code":"forbidden","message":"this request did not come from the app"}}` + "\n"

// counted counts every session looked up through it.
type counted struct {
	sessions
	n atomic.Int32
}

func (c *counted) Authenticate(ctx context.Context, cookie string) (*accounts.Authenticated, error) {
	c.n.Add(1)
	return c.sessions.Authenticate(ctx, cookie)
}

// crossSite asks h as a document of another site would: the header values
// given (none for a browser that sends no Fetch Metadata), and the session
// cookie when cookie is set.
func crossSite(h http.Handler, method, path, cookie string, site ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://app.permaily.test"+path, nil)
	for _, s := range site {
		r.Header.Add("Sec-Fetch-Site", s)
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: cookie})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func refusedAsNotTheApp(t *testing.T, name string, rec *httptest.ResponseRecorder) {
	t.Helper()
	h := rec.Header()
	if rec.Code != http.StatusForbidden || rec.Body.String() != forbiddenBody || h.Get("Content-Type") != "application/json" ||
		h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "no-store" || h.Get("Set-Cookie") != "" {
		t.Errorf("%s: %d %v %q", name, rec.Code, h, rec.Body)
	}
}

// refusedAsTheFrame is the same refusal on a path of the frame's route: the
// frame's headers and its fixed body, never the app's JSON (finding 9 of
// pull request 5's review).
func refusedAsTheFrame(t *testing.T, name string, rec *httptest.ResponseRecorder) {
	t.Helper()
	wantFrameHeaders(t, name, rec.Header())
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "This message can't be shown here.") {
		t.Errorf("%s: %d %q", name, rec.Code, rec.Body)
	}
}

func TestRefuseCrossSiteBeforeAnything(t *testing.T) {
	var ran atomic.Int32
	h := httpapi.RefuseCrossSite(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ran.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	paths := []string{
		"/api/v1/me", "/api", "/api/", "/api/v1/nothing", "/api/v1/admin/stats", "/api/v1/mailboxes",
		"/api/v1/mailboxes/mbx_x/messages/m/html", "/api/v1/mailboxes/mbx_x/messages/m/raw",
		"/api/v1/mailboxes/mbx_x/messages/m/attachments/0", "/api/v1/mailboxes/mbx_x/messages/m/links/0.AAAAAAAAAAAAAAAA",
		// Not clean: checked as the path ServeMux would send it on to.
		"/x/../api/v1/me", "/./api/v1/me", "//api/v1/me",
		// A path next to the callback's, which is not it.
		httpapi.ProviderCallback + "/",
	}
	sites := [][]string{{"cross-site"}, {"same-site"}, {""}, {"Same-Origin"}, {"cross-site", "same-origin"}, {"same-origin", "same-origin"}, {"nonsense"}}
	for _, method := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		for _, p := range paths {
			for _, site := range sites {
				name := method + " " + p + " " + strings.Join(site, ",")
				with := crossSite(h, method, p, "alice", site...)
				without := crossSite(h, method, p, "", site...)
				refused := refusedAsNotTheApp
				if strings.HasSuffix(p, "/html") {
					refused = refusedAsTheFrame
				}
				refused(t, name+" with a session", with)
				refused(t, name+" without one", without)
			}
		}
	}
	// The callback itself, by any method but GET.
	for _, method := range []string{"HEAD", "POST", "PUT", "DELETE"} {
		refusedAsNotTheApp(t, method+" callback", crossSite(h, method, httpapi.ProviderCallback, "alice", "cross-site"))
	}
	if n := ran.Load(); n != 0 {
		t.Fatalf("a handler ran %d times for a cross-site request", n)
	}

	// What goes on: no header, same-origin, none; and the app's pages from
	// anywhere.
	for _, c := range []struct {
		path string
		site []string
	}{
		{"/api/v1/me", nil}, {"/api/v1/me", []string{"same-origin"}}, {"/api/v1/me", []string{"none"}},
		{"/api/v1/mailboxes/mbx_x/messages/m/raw", []string{"none"}},
		{"/", []string{"cross-site"}}, {"/sign-in", []string{"same-site"}}, {"/inbox/mbx_x/m/links/0.AAAAAAAAAAAAAAAA", []string{"cross-site"}},
		{"/apiary", []string{"cross-site"}}, {"/api/../sign-in", []string{"cross-site"}},
		{httpapi.ProviderCallback + "?state=x&code=y", []string{"cross-site"}}, {httpapi.ProviderCallback, []string{"same-site"}},
	} {
		before := ran.Load()
		if rec := crossSite(h, "GET", c.path, "alice", c.site...); rec.Code != http.StatusTeapot || ran.Load() != before+1 {
			t.Errorf("GET %s %v: %d, want it handled", c.path, c.site, rec.Code)
		}
	}
}

// The exceptions are named, each with its reason, and each is a route under
// /api/ that reads no session.
func TestCrossSiteExceptionsAreNamed(t *testing.T) {
	list := httpapi.CrossSiteExceptions()
	if len(list) != 1 || list[0].Method != http.MethodGet || list[0].Path != httpapi.ProviderCallback ||
		httpapi.ProviderCallback != "/api/v1/auth/provider/callback" {
		t.Fatalf("the exceptions changed: %+v; each new one needs its reason and ADR 0026's list", list)
	}
	for _, e := range list {
		if !strings.HasPrefix(e.Path, "/api/") || len(e.Why) < 40 {
			t.Errorf("%+v: an exception is a route of the API, with its reason", e)
		}
	}
	list[0].Path = "/api/v1/me"
	if httpapi.CrossSiteExceptions()[0].Path != httpapi.ProviderCallback {
		t.Fatal("the list can be changed from outside")
	}
}

// The person's routes, behind the rule as the enclave mounts them: a request
// that says it is cross-site or same-site is refused before the session is
// looked up and before any mailbox is opened or read, whether the session
// came with it or not; the app's own requests (same-origin), the frame's first
// navigation, and a download opened from the address bar (none) work.
func TestTheMailboxRoutesRefuseAnotherSite(t *testing.T) {
	a := newInboxApp(t, nil)
	box := a.fake.Add(alice, "Mine")
	msg := a.seed(box.ID, hostileRaw(), hostileMeta())
	seen := &counted{sessions: a.cfg.Sessions.(sessions)}
	a.cfg.Sessions = seen
	h := httpapi.RefuseCrossSite(httpapi.MailboxesAPI(a.cfg))
	all := append(routes(box.ID, msg),
		route{"the frame", "GET", "/api/v1/mailboxes/" + box.ID + "/messages/" + msg + "/html", "", 200},
		route{"a link", "GET", "/api/v1/mailboxes/" + box.ID + "/messages/" + msg + "/links/0.AAAAAAAAAAAAAAAA", "", 404})
	for _, r := range all {
		refused := refusedAsNotTheApp
		if r.name == "the frame" {
			refused = refusedAsTheFrame
		}
		for _, site := range []string{"cross-site", "same-site"} {
			refused(t, r.name+" "+site+" with a session", crossSite(h, r.method, r.path, "alice", site))
			refused(t, r.name+" "+site+" without one", crossSite(h, r.method, r.path, "", site))
		}
	}
	if seen.n.Load() != 0 || len(a.svc.opened) != 0 || len(a.reader.read) != 0 {
		t.Fatalf("refused, yet %d sessions looked up, %v opened, %v read", seen.n.Load(), a.svc.opened, a.reader.read)
	}

	// The app's own calls, same-origin, each on a mailbox of its own as R1
	// has them.
	for _, r := range routes("", "") {
		b := newInboxApp(t, nil)
		mine := b.fake.Add(alice, "Mine")
		for _, x := range routes(mine.ID, b.seed(mine.ID, hostileRaw(), hostileMeta())) {
			if x.name == r.name {
				r = x
			}
		}
		if rec := sameOrigin(httpapi.RefuseCrossSite(b.h), r.method, r.path, r.body, nil); rec.Code != r.ok {
			t.Errorf("%s from the app: %d %s, want %d", r.name, rec.Code, rec.Body, r.ok)
		}
	}
	base := "/api/v1/mailboxes/" + box.ID + "/messages/" + msg
	// The frame's first navigation, from the app's page.
	if rec := sameOrigin(h, "GET", base+"/html", "", map[string]string{"Sec-Fetch-Dest": "iframe", "Sec-Fetch-Mode": "navigate"}); rec.Code != 200 {
		t.Errorf("the frame from the app: %d", rec.Code)
	}
	// Downloads: started from the app (a link with download: a same-origin
	// navigation), or opened by pasting the address (none).
	for _, site := range []string{"same-origin", "none"} {
		for _, p := range []string{base + "/raw", base + "/attachments/0"} {
			rec := crossSite(h, "GET", p, "alice", site)
			if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") {
				t.Errorf("download %s, %s: %d %v", p, site, rec.Code, rec.Header())
			}
		}
	}
}

// sameOrigin asks h as the app's own page does: fetch, same origin, JSON.
func sameOrigin(h http.Handler, method, path, body string, extra map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://app.permaily.test"+path, strings.NewReader(body))
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if method != http.MethodGet {
		r.Header.Set("Origin", appOrigin)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range extra {
		r.Header.Set(k, v)
	}
	r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "alice"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// The accounts' routes behind the rule: a session's own answer (me) is not
// given to another site, with the session or without; the app's call works.
// The callback answers cross-site in every test of providers_test.go (back).
func TestTheAccountsRoutesRefuseAnotherSite(t *testing.T) {
	a := newApp(t)
	a.signIn("ana@example.org")
	h := httpapi.RefuseCrossSite(a.h)
	for _, site := range []string{"cross-site", "same-site"} {
		for _, p := range []string{"/api/v1/me", "/api/v1/sessions", "/api/v1/passkeys", "/api/v1/providers", "/api/v1/auth/provider/pending", "/api/v1/admin/stats"} {
			refusedAsNotTheApp(t, p+" "+site+" with a session", crossSite(h, "GET", p, a.cookie.Value, site))
			refusedAsNotTheApp(t, p+" "+site+" without one", crossSite(h, "GET", p, "", site))
		}
		refusedAsNotTheApp(t, "sign-out "+site, crossSite(h, "POST", "/api/v1/auth/sign-out", a.cookie.Value, site))
	}
	for _, site := range [][]string{{"same-origin"}, nil} {
		rec := crossSite(h, "GET", "/api/v1/me", a.cookie.Value, site...)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ana@example.org") {
			t.Errorf("me, %v: %d %s", site, rec.Code, rec.Body)
		}
	}
	// What is refused is the request, not the session: it still stands.
	if rec := a.call("GET", "/api/v1/me", "", nil); rec.Code != 200 {
		t.Fatalf("the session after a refusal: %d", rec.Code)
	}
}
