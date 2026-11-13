package httpapi_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/services/accounts"
)

// The second code review of pull request 5, N5 and N7 (ADR 0026, "The
// app's host: one decision about a path"): a request whose path is not in
// canonical form is refused at the outermost handler of the app's host,
// before the cross-site rule, before any session work and before routing,
// with a fixed answer that depends on nothing it carries.

// noSessions fails the test on any session read.
type noSessions struct{ t *testing.T }

func (s noSessions) Authenticate(context.Context, string) (*accounts.Authenticated, error) {
	s.t.Error("a session was read")
	return nil, contracts.ErrUnauthorized
}

// appHostWithTraps is the app's host as the enclave mounts it, whose every
// handler counts when it is reached and writes a cookie, as a session's
// handler would.
func appHostWithTraps(t *testing.T) (http.Handler, *atomic.Int32) {
	var reached atomic.Int32
	trap := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		http.SetCookie(w, &http.Cookie{Name: httpapi.SessionCookie, Value: "", MaxAge: -1})
		_, _ = w.Write([]byte("the app"))
	})
	app := http.NewServeMux()
	app.Handle("/api/", trap)
	app.Handle("/api/v1/mailboxes/", httpapi.MailboxesAPI(httpapi.MailboxesConfig{Sessions: noSessions{t}, AppOrigin: appOrigin}))
	app.Handle("/", trap)
	return router.New(nil, map[string]http.Handler{"app.permaily.test": httpapi.RefuseCrossSite(app)}), &reached
}

// raw asks h with the request line written as given, read as the server
// reads it, with the session cookie and the headers given.
func raw(t *testing.T, h http.Handler, method, target string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := method + " " + target + " HTTP/1.1\r\nHost: app.permaily.test\r\nCookie: " + httpapi.SessionCookie + "=alice\r\n"
	for i := 0; i+1 < len(header); i += 2 {
		req += header[i] + ": " + header[i+1] + "\r\n"
	}
	r, err := http.ReadRequest(bufio.NewReader(strings.NewReader(req + "\r\n")))
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	r.RemoteAddr = "192.0.2.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

const notCanonicalBody = `{"error":{"code":"not_found","message":"no such endpoint"}}` + "\n"

// refusedAsNotCanonical is the fixed refusal of a path not in canonical
// form: as the frame's, or as the app's JSON; no cookie set or cleared.
func refusedAsNotCanonical(t *testing.T, name string, rec *httptest.ResponseRecorder, frame bool) {
	t.Helper()
	if rec.Header().Get("Set-Cookie") != "" {
		t.Errorf("%s: a cookie was set: %q", name, rec.Header().Get("Set-Cookie"))
	}
	if frame {
		wantFrameHeaders(t, name, rec.Header())
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "This message can't be shown here.") {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body)
		}
		return
	}
	if rec.Code != http.StatusNotFound || rec.Body.String() != notCanonicalBody ||
		rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("%s: %d %v %q", name, rec.Code, rec.Header(), rec.Body)
	}
}

var everyMethod = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

// N5: the cross-site rule cleaned the fully decoded path, while ServeMux
// routes by escaped segments: GET /api/v1/admin/..%2f..%2f..%2foutside from
// another site, with a session cookie, was seen as /outside, let through,
// and dispatched into the API, where the session was looked up and written.
// It is refused before anything, whatever the method and the cookie.
func TestReviewN5AnEncodedPathIsRefusedBeforeAnything(t *testing.T) {
	h, reached := appHostWithTraps(t)
	for _, method := range everyMethod {
		for _, site := range []string{"cross-site", "same-origin", ""} {
			name := method + " from " + site
			refusedAsNotCanonical(t, name, raw(t, h, method, "/api/v1/admin/..%2f..%2f..%2foutside", "Sec-Fetch-Site", site), false)
		}
	}
	if n := reached.Load(); n != 0 {
		t.Fatalf("a handler ran %d times", n)
	}
}

// N7: the frame's guard missed aliases and covered another route: GET
// /api%2fv1%2fmailboxes/b/messages/m/html got the frame's headers over the
// app's page; POST /%61pi/v1/mailboxes/b%2fx/messages/m/html met neither
// spelling and was refused as JSON under the app's policy. A path that is
// not canonical and would have been the frame's is refused as the frame's;
// nothing else answers under the frame's headers.
func TestReviewN7FrameAliasesAreRefusedAsTheFrame(t *testing.T) {
	h, reached := appHostWithTraps(t)
	for _, method := range everyMethod {
		for _, target := range []string{
			"/api%2fv1%2fmailboxes/b/messages/m/html", "/%61pi/v1/mailboxes/b%2fx/messages/m/html",
			"/api/v1/mailboxes/b/messages/m/%68tml", "/api/v1/mailboxes/b/messages/m/HTML%2F", "/api/v1/mailboxes//b/messages/m/html",
			"/x/../api/v1/mailboxes/b/messages/m/html", "/api/v1/mailboxes/b/messages/m/./html", `/api\v1\mailboxes\b\messages\m\html`,
		} {
			refusedAsNotCanonical(t, method+" "+target, raw(t, h, method, target, "Sec-Fetch-Site", "same-origin", "Sec-Fetch-Dest", "iframe"), true)
		}
	}
	if n := reached.Load(); n != 0 {
		t.Fatalf("a handler ran %d times", n)
	}
}

// Spellings of a path that is not canonical: each is refused, by every
// method, before anything.
func TestNonCanonicalSpellingsAreRefused(t *testing.T) {
	h, reached := appHostWithTraps(t)
	targets := []string{
		"/api/v1/me%2F", "/api/v1/me%2f", "/api%252fv1/me", "/api/v1/%2e%2e/me", "/api/v1/%2E%2E%2Fme", "/api/v1/m%65",
		"/api/v1/me%00", "/api/v1/me%7f", "/api/v1/me%c3%a9", "/api/v1/me%5c", "/api/v1/me%25",
		"/api//v1/me", "/api/./v1/me", "/api/../api/v1/me", "/api/v1/me/.", "/api/v1/me/..", `/api/v1\me`,
		"/inbox/%2e%2e/accounts", "/sign-in%3fx", "/%73ign-in", "//sign-in", "/assets/../index.html",
		"/api/v1/me/" + strings.Repeat("%61", 20_000), "/" + strings.Repeat("a/", 10_000) + "..",
		"*", "/api/v1/me;x", "/api/v1/@me", "/api/v1/me:x", "/x/../api/v1/me", "/./api/v1/me", "//api/v1/me", "/api/../sign-in",
	}
	for _, method := range everyMethod {
		for _, target := range targets {
			refusedAsNotCanonical(t, method+" "+target[:min(len(target), 60)], raw(t, h, method, target, "Sec-Fetch-Site", "cross-site"), false)
		}
	}
	if n := reached.Load(); n != 0 {
		t.Fatalf("a handler ran %d times", n)
	}
	// Canonical paths go on, with their query untouched: the provider's
	// callback carries encoded characters in its query.
	for _, target := range []string{"/", "/sign-in", "/inbox/mbx_01ABC/18e7000000000000aaaaaaaaaaaaaaaa/links/0.Mhw80zyWKGnmqy03",
		"/assets/index-8j7W1H-8.js", "/favicon.ico", httpapi.ProviderCallback + "?state=a%2Fb&code=c%3D%3D"} {
		before := reached.Load()
		if rec := raw(t, h, "GET", target, "Sec-Fetch-Site", "same-origin"); rec.Code == http.StatusNotFound && rec.Body.String() == notCanonicalBody {
			t.Errorf("%s: refused", target)
		}
		if reached.Load() != before+1 {
			t.Errorf("%s: not served", target)
		}
	}
}

// The gate's own words: which paths are canonical, the patterns every
// AppMux recorded, and the frame's path on a host with no mailboxes.
func TestCanonicalPathsAndTheRecordedRoutes(t *testing.T) {
	for p, want := range map[string]bool{
		"/": true, "/inbox/mbx_1/m/links/0.AbC-_d": true, "/api/v1/mailboxes/": true, "/a/b~c": true,
		"": false, "a": false, "//": false, "/a//b": false, "/a/./b": false, "/a/..": false, "/a%2fb": false, `/a\b`: false, "/a b": false, "/é": false,
	} {
		if got := httpapi.CanonicalPath(p); got != want {
			t.Errorf("%q: %v", p, got)
		}
	}
	httpapi.MailboxesAPI(httpapi.MailboxesConfig{})
	if !slices.Contains(httpapi.RegisteredPatterns(), "GET /api/v1/mailboxes/{id}/messages/{message}/html") {
		t.Errorf("%v", httpapi.RegisteredPatterns())
	}
	reached := false
	h := httpapi.AppHost(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }), nil)
	rec := raw(t, h, "GET", "/api/v1/mailboxes/b/messages/m/html", "Sec-Fetch-Site", "same-origin", "Sec-Fetch-Dest", "iframe")
	wantFrameHeaders(t, "no mailboxes", rec.Header())
	if reached || rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "This message can't be shown here.") {
		t.Errorf("no mailboxes: %d %q, reached %v", rec.Code, rec.Body, reached)
	}
}
