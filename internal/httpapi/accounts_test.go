package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/store"
)

const appOrigin = "https://app.permaily.test"

// app is the accounts API over a real service, and a browser's cookie jar.
type app struct {
	t      *testing.T
	h      http.Handler
	svc    *accounts.Service
	out    *accounts.Outbox
	logs   *bytes.Buffer
	cookie *http.Cookie
}

func newApp(t *testing.T) *app {
	t.Helper()
	k, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	mem := store.NewMemory()
	a := &app{t: t, out: &accounts.Outbox{}, logs: &bytes.Buffer{}}
	log := slog.New(slog.NewTextHandler(a.logs, nil))
	a.svc, err = accounts.New(accounts.Config{
		Records: &store.Anchored{Records: mem, Commits: store.NewMemoryCommits()}, Entries: mem,
		Keys: k, Mailer: a.out, AppURL: appOrigin, Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	a.h = httpapi.AccountsAPI(a.svc, log)
	return a
}

// call makes a request the way the app's page does: same origin, JSON, with
// the cookie the browser holds. edit changes it before it is sent.
func (a *app) call(method, path, body string, edit func(*http.Request)) *httptest.ResponseRecorder {
	a.t.Helper()
	req := httptest.NewRequest(method, appOrigin+path, strings.NewReader(body))
	req.Header.Set("Origin", appOrigin)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("User-Agent", "test browser")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.cookie != nil {
		req.AddCookie(a.cookie)
	}
	if edit != nil {
		edit(req)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.Name == httpapi.SessionCookie {
			if a.cookie = c; c.MaxAge < 0 {
				a.cookie = nil
			}
		}
	}
	return rec
}

var sentCode = regexp.MustCompile(`\n    (\d{6})\n`)

// signIn signs the browser in to address, making the account if it is new.
func (a *app) signIn(address string) contracts.Me {
	a.t.Helper()
	inv, err := a.svc.CreateInvitations(context.Background(), 1)
	if err != nil {
		a.t.Fatal(err)
	}
	rec := a.call("POST", "/api/v1/auth/email/start", `{"address":"`+address+`","invitation":"`+inv.Invitations[0]+`"}`, nil)
	var started contracts.EmailStarted
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &started) != nil || started.Attempt == "" || started.ExpiresIn != 600 {
		a.t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	a.svc.Wait()
	sent := a.out.Sent()
	code := sentCode.FindStringSubmatch(sent[len(sent)-1].Text)[1]
	rec = a.call("POST", "/api/v1/auth/email/finish", `{"attempt":"`+started.Attempt+`","code":"`+code+`"}`, nil)
	var me contracts.Me
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &me) != nil || me.Account.Address != address {
		a.t.Fatalf("finish: %d %s", rec.Code, rec.Body)
	}
	return me
}

var sentLink = regexp.MustCompile(`#attempt=(att_[a-z2-7]{26})&token=([A-Za-z0-9_-]{43})\n`)

// The page that opens the link of a message asks whose sign-in it is before
// it signs in, and asking signs nobody in.
func TestLookAtALinkAPI(t *testing.T) {
	a := newApp(t)
	a.signIn("ana@example.org")
	a.call("POST", "/api/v1/auth/sign-out", "", nil)
	if rec := a.call("POST", "/api/v1/auth/email/start", `{"address":"ana@example.org"}`, nil); rec.Code != 200 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	a.svc.Wait()
	sent := a.out.Sent()
	link := sentLink.FindStringSubmatch(sent[len(sent)-1].Text)
	body := `{"attempt":"` + link[1] + `","token":"` + link[2] + `"}`

	rec := a.call("POST", "/api/v1/auth/email/look", body, nil)
	var pending contracts.EmailPending
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &pending) != nil || pending.Address != "ana@example.org" {
		t.Fatalf("look: %d %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Set-Cookie"); got != "" {
		t.Errorf("looking set a cookie: %s", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("cache control %q", got)
	}
	if rec := a.call("GET", "/api/v1/me", "", nil); rec.Code != 401 {
		t.Errorf("signed in by looking: %d", rec.Code)
	}
	// Another site's page cannot ask.
	if rec := a.call("POST", "/api/v1/auth/email/look", body, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }); rec.Code != 403 {
		t.Errorf("look from another origin: %d %s", rec.Code, rec.Body)
	}
	// The link still signs in, once.
	if rec := a.call("POST", "/api/v1/auth/email/finish", body, nil); rec.Code != 200 {
		t.Fatalf("finish after looking: %d %s", rec.Code, rec.Body)
	}
	if rec := a.call("POST", "/api/v1/auth/email/look", body, nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), `"invalid_code"`) {
		t.Errorf("look at a used link: %d %s", rec.Code, rec.Body)
	}
}

func TestAccountsAPI(t *testing.T) {
	a := newApp(t)
	if rec := a.call("GET", "/api/v1/me", "", nil); rec.Code != 401 || !strings.Contains(rec.Body.String(), `"unauthorized"`) {
		t.Fatalf("me without a session: %d %s", rec.Code, rec.Body)
	}
	me := a.signIn("ana@example.org")

	// The cookie is for this host alone, out of reach of scripts and of
	// requests that other sites make.
	c := a.cookie
	if c == nil || c.Name != "__Host-permaily-session" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" || c.MaxAge != 30*24*3600 {
		t.Fatalf("cookie: %+v", c)
	}

	rec := a.call("GET", "/api/v1/me", "", nil)
	var got contracts.Me
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Account != me.Account || !got.Session.Current || got.Session.Device != "test browser" {
		t.Fatalf("me: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("an answer about an account may be cached")
	}

	// A second device, listed and closed from the first.
	phone := newApp(t)
	phone.h, phone.svc, phone.out = a.h, a.svc, a.out
	phone.signIn("ana@example.org")
	rec = a.call("GET", "/api/v1/sessions", "", nil)
	var list contracts.SessionList
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Sessions) != 2 {
		t.Fatalf("sessions: %d %s", rec.Code, rec.Body)
	}
	other := list.Sessions[0]
	if other.Current {
		other = list.Sessions[1]
	}
	if rec := a.call("DELETE", "/api/v1/sessions/"+other.ID, "", nil); rec.Code != 204 || a.cookie == nil {
		t.Fatalf("closing the other device: %d %s", rec.Code, rec.Body)
	}
	if rec := phone.call("GET", "/api/v1/me", "", nil); rec.Code != 401 || phone.cookie != nil {
		t.Fatalf("the closed device: %d, cookie kept: %v", rec.Code, phone.cookie != nil)
	}
	if rec := a.call("DELETE", "/api/v1/sessions/"+other.ID, "", nil); rec.Code != 404 {
		t.Fatalf("closing it again: %d", rec.Code)
	}

	// Signing out closes the session and drops the cookie.
	old := a.cookie
	if rec := a.call("POST", "/api/v1/auth/sign-out", "", nil); rec.Code != 204 || a.cookie != nil {
		t.Fatalf("sign-out: %d %s", rec.Code, rec.Body)
	}
	a.cookie = old
	if rec := a.call("GET", "/api/v1/me", "", nil); rec.Code != 401 {
		t.Fatalf("the cookie of a closed session: %d", rec.Code)
	}
	if rec := a.call("POST", "/api/v1/auth/sign-out", "", nil); rec.Code != 401 {
		t.Fatalf("signing out without a session: %d", rec.Code)
	}

	// Closing one's own session from the list drops the cookie too.
	me = a.signIn("ana@example.org")
	if rec := a.call("DELETE", "/api/v1/sessions/"+me.Session.ID, "", nil); rec.Code != 204 || a.cookie != nil {
		t.Fatalf("closing one's own session: %d", rec.Code)
	}
}

func TestAccountsAPIRefusals(t *testing.T) {
	a := newApp(t)
	cases := []struct {
		name, method, path, body string
		edit                     func(*http.Request)
		status                   int
		code                     string
	}{
		{"bad address", "POST", "/api/v1/auth/email/start", `{"address":"nope"}`, nil, 400, "invalid_address"},
		{"bad invitation", "POST", "/api/v1/auth/email/start", `{"address":"ana@example.org","invitation":"abcd-2345-wxyz"}`, nil, 400, "invalid_invitation"},
		{"wrong code", "POST", "/api/v1/auth/email/finish", `{"attempt":"att_x","code":"123456"}`, nil, 400, "invalid_code"},
		{"wrong link", "POST", "/api/v1/auth/email/look", `{"attempt":"att_x","token":"nope"}`, nil, 400, "invalid_code"},
		{"a link that is not JSON", "POST", "/api/v1/auth/email/look", `attempt=att_x`, nil, 400, "invalid_body"},
		{"a link in the address", "GET", "/api/v1/auth/email/look?attempt=att_x&token=nope", "", nil, 404, "not_found"},
		{"not JSON", "POST", "/api/v1/auth/email/start", `address=ana`, nil, 400, "invalid_body"},
		{"unknown field", "POST", "/api/v1/auth/email/start", `{"address":"ana@example.org","admin":true}`, nil, 400, "invalid_body"},
		{"too large", "POST", "/api/v1/auth/email/start", `{"address":"` + strings.Repeat("a", 5000) + `"}`, nil, 400, "invalid_body"},
		{"unknown endpoint", "GET", "/api/v1/nothing", "", nil, 404, "not_found"},
		{"wrong method", "GET", "/api/v1/auth/email/start", "", nil, 404, "not_found"},
		// Requests that another site's page can make a browser send.
		{"another origin", "POST", "/api/v1/auth/email/start", `{"address":"ana@example.org"}`, func(r *http.Request) { r.Header.Set("Origin", "https://permaily.test") }, 403, "forbidden"},
		{"no origin", "POST", "/api/v1/auth/email/start", `{"address":"ana@example.org"}`, func(r *http.Request) { r.Header.Del("Origin") }, 403, "forbidden"},
		{"same site, another origin", "POST", "/api/v1/auth/email/start", `{"address":"ana@example.org"}`, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }, 403, "forbidden"},
		{"cross site", "DELETE", "/api/v1/sessions/ses_x", "", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403, "forbidden"},
		{"a form", "POST", "/api/v1/auth/email/start", `{"address":"ana@example.org"}`, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415, "invalid_body"},
	}
	for _, c := range cases {
		rec := a.call(c.method, c.path, c.body, c.edit)
		var e httpapi.APIError
		if rec.Code != c.status || json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Error.Code != c.code {
			t.Errorf("%s: %d %s, want %d %s", c.name, rec.Code, rec.Body, c.status, c.code)
		}
	}
	a.svc.Wait()
	if len(a.out.Sent()) != 0 {
		t.Fatal("a refused request sent a message")
	}
	// An older browser sends no Sec-Fetch-Site: the origin decides.
	if rec := a.call("POST", "/api/v1/auth/email/start", `{"address":"ana@example.org"}`, func(r *http.Request) { r.Header.Del("Sec-Fetch-Site") }); rec.Code != 200 {
		t.Fatalf("without Sec-Fetch-Site: %d %s", rec.Code, rec.Body)
	}
	a.svc.Wait()
}

func TestInvitationsAPI(t *testing.T) {
	a := newApp(t)
	h := httpapi.TokenAuth(tokenHash(), httpapi.Invitations(a.svc, slog.New(slog.DiscardHandler)))
	if rec := do(h, "POST", "/v1/admin/invitations", "", `{"count":2}`); rec.Code != 401 {
		t.Fatalf("without the operator's token: %d", rec.Code)
	}
	if rec := do(h, "GET", "/v1/admin/invitations", token, ""); rec.Code != 405 || rec.Header().Get("Allow") != "POST" {
		t.Fatalf("GET: %d", rec.Code)
	}
	rec := do(h, "POST", "/v1/admin/invitations", token, `{"count":2}`)
	var out contracts.Invitations
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Invitations) != 2 || out.ExpiresAt.IsZero() {
		t.Fatalf("two invitations: %d %s", rec.Code, rec.Body)
	}
	// One when nothing is asked.
	rec = do(h, "POST", "/v1/admin/invitations", token, "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Invitations) != 1 {
		t.Fatalf("default: %d %s", rec.Code, rec.Body)
	}
	for _, body := range []string{`{"count":0}`, `{"count":101}`, `{"n":1}`} {
		if rec := do(h, "POST", "/v1/admin/invitations", token, body); rec.Code != 400 {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	// An invitation made here signs someone up.
	rec = a.call("POST", "/api/v1/auth/email/start", `{"address":"ana@example.org","invitation":"`+out.Invitations[0]+`"}`, nil)
	a.svc.Wait()
	if rec.Code != 200 || len(a.out.Sent()) != 1 {
		t.Fatalf("signing up with it: %d %s", rec.Code, rec.Body)
	}
}

// broken is a service that fails the way storage that is down makes it fail.
type broken struct {
	httpapi.AccountsService
	err error
}

func (b broken) StartEmail(context.Context, contracts.EmailStart) (*contracts.EmailStarted, error) {
	return nil, b.err
}

func (b broken) Authenticate(context.Context, string) (*accounts.Authenticated, error) {
	return nil, b.err
}

func (b broken) CreateInvitations(context.Context, int) (*contracts.Invitations, error) {
	return nil, b.err
}

func TestAccountsAPIInternalErrors(t *testing.T) {
	a := newApp(t)
	a.signIn("ana@example.org")
	id := "acc_" + strings.Repeat("a", 26)
	a.h = httpapi.AccountsAPI(broken{err: errors.New("store: commits of account-" + id + ": down")}, slog.New(slog.NewTextHandler(a.logs, nil)))
	for _, c := range [][3]string{{"POST", "/api/v1/auth/email/start", `{"address":"ana@example.org"}`}, {"GET", "/api/v1/me", ""}} {
		rec := a.call(c[0], c[1], c[2], nil)
		// What failed stays inside: the answer says nothing of it.
		if rec.Code != 500 || strings.Contains(rec.Body.String(), "store") || !strings.Contains(rec.Body.String(), `"internal"`) {
			t.Fatalf("%s: %d %s", c[1], rec.Code, rec.Body)
		}
	}
	if a.cookie == nil {
		t.Fatal("a failure dropped the session's cookie")
	}
	// It is logged, without the account it was about.
	if logs := a.logs.String(); !strings.Contains(logs, "commits of account-acc_…: down") || strings.Contains(logs, id) {
		t.Fatalf("logs:\n%s", logs)
	}
	h := httpapi.Invitations(broken{err: contracts.ErrTooManyRequests}, slog.New(slog.DiscardHandler))
	if rec := do(h, "POST", "/v1/admin/invitations", "", ""); rec.Code != 429 {
		t.Fatalf("too many: %d", rec.Code)
	}
}
