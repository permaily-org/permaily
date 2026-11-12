package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webauthn/webauthntest"
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
	// trip is the cookie of a trip to a provider, and idp plays the providers
	// (providers_test.go).
	trip *http.Cookie
	idp  *providers
	// device is the cookie that names this browser to the accounts it signs
	// in to.
	device *http.Cookie
	// mem is the storage, for a test to write to by hand, and ahead is how
	// far the service's clock runs ahead of the real one.
	mem   *store.Memory
	ahead time.Duration
}

func newApp(t *testing.T) *app {
	t.Helper()
	k, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	mem := store.NewMemory()
	a := &app{t: t, out: &accounts.Outbox{}, logs: &bytes.Buffer{}, mem: mem, idp: &providers{who: map[string]idp.Identity{}}}
	log := slog.New(slog.NewTextHandler(a.logs, nil))
	a.svc, err = accounts.New(accounts.Config{
		// As the enclave sets it up: only the newest version of a record is kept.
		Records: &store.Anchored{Records: mem, Commits: store.NewMemoryCommits(), OnlyNewest: true}, Entries: mem,
		Keys: k, Mailer: a.out, Providers: a.idp, AppURL: appOrigin, SuperAdmin: superAdmin, Log: log,
		Now: func() time.Time { return time.Now().Add(a.ahead) },
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
	if a.trip != nil {
		req.AddCookie(a.trip)
	}
	if a.device != nil {
		req.AddCookie(a.device)
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
		if c.Name == httpapi.ProviderCookie {
			if a.trip = c; c.MaxAge < 0 {
				a.trip = nil
			}
		}
		if c.Name == httpapi.DeviceCookie {
			a.device = c
		}
	}
	return rec
}

var sentCode = regexp.MustCompile(`\n    (\d{6})\n`)

// mailOf waits for what the service is sending and returns the sign-in
// message of attempt, found by the link it carries. Not the newest message:
// the service sends its messages in the background and Wait waits for all of
// them, so a message a test started earlier and did not wait for can be sent
// after this one.
func (a *app) mailOf(attempt string) accounts.Mail {
	a.t.Helper()
	a.svc.Wait()
	var found []accounts.Mail
	for _, m := range a.out.Sent() {
		if g := sentLink.FindStringSubmatch(m.Text); g != nil && g[1] == attempt {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		a.t.Fatalf("%d sign-in messages for the attempt %s", len(found), attempt)
	}
	return found[0]
}

// mailTo waits for what the service is sending and returns the message to
// address with subject sent since there were n, nil when none was; it fails
// when there are more. Not the newest message, for the reason mailOf gives.
func (a *app) mailTo(n int, address, subject string) *accounts.Mail {
	a.t.Helper()
	a.svc.Wait()
	var found *accounts.Mail
	for _, m := range a.out.Sent()[n:] {
		if m.To == address && m.Subject == subject {
			if found != nil {
				a.t.Fatalf("two messages %q to %s", subject, address)
			}
			found = &m
		}
	}
	return found
}

// signIn signs the browser in to address, making the account if it is new.
func (a *app) signIn(address string) contracts.Me {
	a.t.Helper()
	inv, err := a.svc.CreateInvitations(context.Background(), 1)
	if err != nil {
		a.t.Fatal(err)
	}
	rec := a.call("POST", "/api/v1/auth/email/start", `{"address":"`+address+`","invitation":"`+inv.Invitations[0]+`","terms":"`+domain.TermsVersion+`"}`, nil)
	var started contracts.EmailStarted
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &started) != nil || started.Attempt == "" || started.ExpiresIn != 600 {
		a.t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	code := sentCode.FindStringSubmatch(a.mailOf(started.Attempt).Text)[1]
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
	var started contracts.EmailStarted
	if rec := a.call("POST", "/api/v1/auth/email/start", `{"address":"ana@example.org"}`, nil); rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &started) != nil {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	link := sentLink.FindStringSubmatch(a.mailOf(started.Attempt).Text)
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
	if rec := do(h, "PUT", "/v1/admin/invitations", token, ""); rec.Code != 405 || rec.Header().Get("Allow") != "GET, POST" {
		t.Fatalf("PUT: %d", rec.Code)
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
	rec = a.call("POST", "/api/v1/auth/email/start", `{"address":"ana@example.org","invitation":"`+out.Invitations[0]+`","terms":"`+domain.TermsVersion+`"}`, nil)
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

func (b broken) StartPasskeySignIn(context.Context) (*contracts.PasskeyRequest, error) {
	return nil, b.err
}

func (b broken) FinishPasskeySignIn(context.Context, contracts.PasskeySignIn, accounts.Browser) (*contracts.Me, string, error) {
	return nil, "", b.err
}

func (b broken) Recover(context.Context, contracts.Recovery, accounts.Browser) (*contracts.Me, string, error) {
	return nil, "", b.err
}

func (b broken) AccountRecord(context.Context, string) (*contracts.AccountRecord, error) {
	return nil, b.err
}

func (b broken) AccountRecords(context.Context, string, int) (*contracts.AccountList, error) {
	return nil, b.err
}

func TestAccountsAPIInternalErrors(t *testing.T) {
	a := newApp(t)
	a.signIn("ana@example.org")
	id := "acc_" + strings.Repeat("a", 26)
	a.h = httpapi.AccountsAPI(broken{err: errors.New("store: commits of account-" + id + ": down")}, slog.New(slog.NewTextHandler(a.logs, nil)))
	for _, c := range [][3]string{
		{"POST", "/api/v1/auth/email/start", `{"address":"ana@example.org"}`}, {"GET", "/api/v1/me", ""},
		{"POST", "/api/v1/auth/passkey/start", ""}, {"POST", "/api/v1/auth/passkey/finish", `{"ceremony":"cer_x","credential":{}}`},
		{"POST", "/api/v1/auth/recovery", `{"address":"ana@example.org","code":"x"}`}, {"GET", "/api/v1/passkeys", ""},
		{"POST", "/api/v1/account/passkey-only", `{"on":false}`},
	} {
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

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("%q is not base64url without padding: %v", s, err)
	}
	return b
}

// read decodes the body of an answer the way the page does, and fails on
// anything but the status wanted.
func read(t *testing.T, rec *httptest.ResponseRecorder, status int, v any) {
	t.Helper()
	if rec.Code != status || (v != nil && json.Unmarshal(rec.Body.Bytes(), v) != nil) {
		t.Fatalf("%d %s, want %d", rec.Code, rec.Body, status)
	}
	if status == 200 && rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("an answer about an account may be cached")
	}
}

// makePasskey makes a passkey for the signed-in browser, sending what
// PublicKeyCredential.toJSON() gives, and returns its identifier.
func (a *app) makePasskey(auth *webauthntest.Authenticator, name string) string {
	a.t.Helper()
	var started struct {
		Ceremony string `json:"ceremony"`
		Options  struct {
			RP        struct{ ID, Name string } `json:"rp"`
			User      struct{ ID string }       `json:"user"`
			Challenge string                    `json:"challenge"`
		} `json:"options"`
	}
	read(a.t, a.call("POST", "/api/v1/passkeys/start", "", nil), 200, &started)
	o := started.Options
	id, clientData, attestation := auth.Create(o.RP.ID, appOrigin, unb64(a.t, o.Challenge), unb64(a.t, o.User.ID))
	body := fmt.Sprintf(`{"ceremony":%q,"name":%q,"credential":{"id":%q,"rawId":%q,"type":"public-key","authenticatorAttachment":"platform",
		"response":{"clientDataJSON":%q,"attestationObject":%q,"transports":["internal","hybrid"],"publicKeyAlgorithm":-7},
		"clientExtensionResults":{"credProps":{"rk":true}}}}`, started.Ceremony, name, b64(id), b64(id), b64(clientData), b64(attestation))
	var made contracts.Passkey
	read(a.t, a.call("POST", "/api/v1/passkeys/finish", body, nil), 200, &made)
	if made.ID.String() != b64(id) || made.Name != name || made.CreatedAt.IsZero() {
		a.t.Fatalf("the passkey made: %+v", made)
	}
	return b64(id)
}

// passkeySignIn signs the browser in with a passkey and returns the answer.
func (a *app) passkeySignIn(auth *webauthntest.Authenticator, id string) *httptest.ResponseRecorder {
	a.t.Helper()
	var started struct {
		Ceremony string `json:"ceremony"`
		Options  struct {
			Challenge        string          `json:"challenge"`
			RPID             string          `json:"rpId"`
			UserVerification string          `json:"userVerification"`
			AllowCredentials json.RawMessage `json:"allowCredentials"`
		} `json:"options"`
	}
	read(a.t, a.call("POST", "/api/v1/auth/passkey/start", "", nil), 200, &started)
	o := started.Options
	if o.RPID != "app.permaily.test" || o.UserVerification != "required" || o.AllowCredentials != nil {
		a.t.Fatalf("request options: %+v", o)
	}
	clientData, authData, signature, handle := auth.Get(o.RPID, appOrigin, unb64(a.t, o.Challenge), unb64(a.t, id))
	body := fmt.Sprintf(`{"ceremony":%q,"credential":{"id":%q,"rawId":%q,"type":"public-key","authenticatorAttachment":"platform",
		"response":{"clientDataJSON":%q,"authenticatorData":%q,"signature":%q,"userHandle":%q},"clientExtensionResults":{}}}`,
		started.Ceremony, id, id, b64(clientData), b64(authData), b64(signature), b64(handle))
	return a.call("POST", "/api/v1/auth/passkey/finish", body, nil)
}

// A passkey is made, signs in, becomes the only way in, and a recovery code
// stands in for it once: all of it as the page asks for it.
func TestPasskeysAPI(t *testing.T) {
	a := newApp(t)
	me := a.signIn("ana@example.org")
	var list contracts.PasskeyList
	rec := a.call("GET", "/api/v1/passkeys", "", nil)
	read(t, rec, 200, &list)
	if strings.TrimSpace(rec.Body.String()) != `{"passkeys":[],"passkey_only":false,"recovery_codes_left":0}` {
		t.Fatalf("no passkeys: %s", rec.Body)
	}

	auth := webauthntest.New(webauthntest.ES256)
	id := a.makePasskey(auth, "Laptop")
	read(t, a.call("GET", "/api/v1/passkeys", "", nil), 200, &list)
	if len(list.Passkeys) != 1 || list.Passkeys[0].ID.String() != id || list.Passkeys[0].Name != "Laptop" || list.Passkeys[0].Synced {
		t.Fatalf("passkeys: %+v", list)
	}
	var renamed contracts.Passkey
	read(t, a.call("PATCH", "/api/v1/passkeys/"+id, `{"name":"Work laptop"}`, nil), 200, &renamed)
	if renamed.Name != "Work laptop" || renamed.ID.String() != id {
		t.Fatalf("renamed: %+v", renamed)
	}

	// Signed out, the passkey signs in: no address asked, the same cookie.
	read(t, a.call("POST", "/api/v1/auth/sign-out", "", nil), 204, nil)
	var signed contracts.Me
	read(t, a.passkeySignIn(auth, id), 200, &signed)
	c := a.cookie
	if signed.Account != me.Account || !signed.Session.Current || signed.Session.ID == me.Session.ID || signed.Session.Device != "test browser" {
		t.Fatalf("signed in with a passkey: %+v", signed)
	}
	if c == nil || c.Name != "__Host-permaily-session" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" || c.MaxAge != 30*24*3600 {
		t.Fatalf("cookie: %+v", c)
	}
	read(t, a.call("GET", "/api/v1/me", "", nil), 200, nil)

	// "Passkey only": ten codes, shown this once.
	var state contracts.PasskeyOnlyState
	read(t, a.call("POST", "/api/v1/account/passkey-only", `{"on":true}`, nil), 200, &state)
	if !state.PasskeyOnly || len(state.RecoveryCodes) != 10 {
		t.Fatalf("passkey only: %+v", state)
	}
	read(t, a.call("GET", "/api/v1/passkeys", "", nil), 200, &list)
	if !list.PasskeyOnly || list.RecoveryCodesLeft != 10 {
		t.Fatalf("after passkey only: %+v", list)
	}
	rec = a.call("POST", "/api/v1/account/passkey-only", `{"on":true}`, nil)
	if read(t, rec, 200, nil); strings.TrimSpace(rec.Body.String()) != `{"passkey_only":true,"recovery_codes":[]}` {
		t.Fatalf("turning on what is on: %s", rec.Body)
	}
	var e httpapi.APIError
	if read(t, a.call("DELETE", "/api/v1/passkeys/"+id, "", nil), 409, &e); e.Error.Code != "last_passkey" {
		t.Fatalf("removing the last passkey: %+v", e)
	}
	// Signing in by mail answers as it always does, and sends nothing.
	sent := len(a.out.Sent())
	var started contracts.EmailStarted
	read(t, a.call("POST", "/api/v1/auth/email/start", `{"address":"ana@example.org"}`, nil), 200, &started)
	a.svc.Wait()
	if started.Attempt == "" || started.ExpiresIn != 600 || len(a.out.Sent()) != sent {
		t.Fatalf("a sign-in by mail with passkey only: %+v, %d messages", started, len(a.out.Sent())-sent)
	}
	var codes contracts.RecoveryCodes
	read(t, a.call("POST", "/api/v1/account/recovery-codes", "", nil), 200, &codes)
	if len(codes.RecoveryCodes) != 10 || codes.RecoveryCodes[0] == state.RecoveryCodes[0] {
		t.Fatalf("new codes: %+v", codes)
	}

	// Signed out, a recovery code and the address sign in, once.
	read(t, a.call("POST", "/api/v1/auth/sign-out", "", nil), 204, nil)
	body := `{"address":"ana@example.org","code":"` + codes.RecoveryCodes[0] + `"}`
	if read(t, a.call("POST", "/api/v1/auth/recovery", `{"address":"ana@example.org","code":"`+state.RecoveryCodes[0]+`"}`, nil), 400, &e); e.Error.Code != "invalid_code" || a.cookie != nil {
		t.Fatalf("a code from before the new ones: %+v", e)
	}
	read(t, a.call("POST", "/api/v1/auth/recovery", body, nil), 200, &signed)
	want := me.Account
	want.PasskeyOnly = true // turned on above
	if signed.Account != want || a.cookie == nil || a.cookie.MaxAge != 30*24*3600 || !a.cookie.HttpOnly {
		t.Fatalf("recovered: %+v, cookie %+v", signed, a.cookie)
	}
	old := a.cookie
	read(t, a.call("POST", "/api/v1/auth/sign-out", "", nil), 204, nil)
	rec = a.call("POST", "/api/v1/auth/recovery", body, nil)
	if read(t, rec, 400, &e); e.Error.Code != "invalid_code" || a.cookie != nil || rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("the same code again: %+v", e)
	}
	// The answer is the same for an address without an account.
	nobody := a.call("POST", "/api/v1/auth/recovery", `{"address":"nobody@example.org","code":"`+codes.RecoveryCodes[0]+`"}`, nil)
	if nobody.Code != rec.Code || nobody.Body.String() != rec.Body.String() {
		t.Fatalf("an address without an account: %d %s", nobody.Code, nobody.Body)
	}

	// Turned off again, the passkey can go, and there are no codes to ask for.
	read(t, a.passkeySignIn(auth, id), 200, nil)
	if old.Value == a.cookie.Value {
		t.Fatal("a sign-in gave the cookie of another")
	}
	read(t, a.call("POST", "/api/v1/account/passkey-only", `{"on":false}`, nil), 200, &state)
	if state.PasskeyOnly || len(state.RecoveryCodes) != 0 {
		t.Fatalf("turned off: %+v", state)
	}
	if read(t, a.call("POST", "/api/v1/account/recovery-codes", "", nil), 409, &e); e.Error.Code != "passkey_only_off" {
		t.Fatalf("codes without passkey only: %+v", e)
	}
	read(t, a.call("DELETE", "/api/v1/passkeys/"+id, "", nil), 204, nil)
	if read(t, a.call("DELETE", "/api/v1/passkeys/"+id, "", nil), 404, &e); e.Error.Code != "not_found" {
		t.Fatalf("removing it again: %+v", e)
	}
	if read(t, a.call("POST", "/api/v1/account/passkey-only", `{"on":true}`, nil), 409, &e); e.Error.Code != "passkey_needed" {
		t.Fatalf("passkey only without a passkey: %+v", e)
	}
	// The passkey that was removed signs nobody in.
	read(t, a.call("POST", "/api/v1/auth/sign-out", "", nil), 204, nil)
	if read(t, a.passkeySignIn(auth, id), 400, &e); e.Error.Code != "invalid_passkey" || a.cookie != nil {
		t.Fatalf("a removed passkey: %+v", e)
	}

	// Nothing of it is in the logs.
	secrets := append([]string{"ana@", "example.org", me.Account.ID, id, "Laptop"}, append(state.RecoveryCodes, codes.RecoveryCodes...)...)
	for _, secret := range secrets {
		if secret != "" && strings.Contains(a.logs.String(), secret) {
			t.Fatalf("the logs hold %q:\n%s", secret, a.logs)
		}
	}
}

func TestPasskeysAPIRefusals(t *testing.T) {
	a := newApp(t)
	evil := func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }
	id := b64([]byte("some passkey"))
	// Without a session, and from another site's page.
	for _, c := range [][3]string{
		{"GET", "/api/v1/passkeys", ""}, {"POST", "/api/v1/passkeys/start", ""},
		{"POST", "/api/v1/passkeys/finish", `{"ceremony":"cer_x","name":"x","credential":{}}`},
		{"PATCH", "/api/v1/passkeys/" + id, `{"name":"x"}`}, {"DELETE", "/api/v1/passkeys/" + id, ""},
		{"POST", "/api/v1/account/passkey-only", `{"on":false}`}, {"POST", "/api/v1/account/recovery-codes", ""},
	} {
		var e httpapi.APIError
		if read(t, a.call(c[0], c[1], c[2], nil), 401, &e); e.Error.Code != "unauthorized" {
			t.Errorf("%s %s without a session: %+v", c[0], c[1], e)
		}
	}
	a.signIn("ana@example.org")
	for _, c := range [][3]string{
		{"POST", "/api/v1/auth/passkey/start", ""}, {"POST", "/api/v1/auth/passkey/finish", `{"ceremony":"cer_x","credential":{}}`},
		{"POST", "/api/v1/auth/recovery", `{"address":"ana@example.org","code":"x"}`}, {"POST", "/api/v1/passkeys/start", ""},
		{"POST", "/api/v1/passkeys/finish", `{"ceremony":"cer_x","name":"x","credential":{}}`},
		{"PATCH", "/api/v1/passkeys/" + id, `{"name":"x"}`}, {"DELETE", "/api/v1/passkeys/" + id, ""},
		{"POST", "/api/v1/account/passkey-only", `{"on":true}`}, {"POST", "/api/v1/account/recovery-codes", ""},
	} {
		var e httpapi.APIError
		if read(t, a.call(c[0], c[1], c[2], evil), 403, &e); e.Error.Code != "forbidden" {
			t.Errorf("%s %s from another origin: %+v", c[0], c[1], e)
		}
	}

	// A credential fits: an identifier of a kilobyte, twice, and what comes
	// with it. More than a credential needs does not.
	fits := `{"ceremony":"cer_x","name":"x","credential":{"id":"` + strings.Repeat("A", 1364) + `","rawId":"` + strings.Repeat("A", 1364) +
		`","response":{"clientDataJSON":"` + strings.Repeat("A", 1000) + `","attestationObject":"` + strings.Repeat("A", 8000) + `"}}}`
	large := strings.Replace(fits, `"attestationObject":"`, `"attestationObject":"`+strings.Repeat("A", 32<<10), 1)
	cases := []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"a ceremony that is not one", "POST", "/api/v1/passkeys/finish", `{"ceremony":"cer_x","name":"x","credential":{}}`, 400, "invalid_passkey"},
		{"a large credential", "POST", "/api/v1/passkeys/finish", fits, 400, "invalid_passkey"},
		{"more than a credential", "POST", "/api/v1/passkeys/finish", large, 400, "invalid_body"},
		{"a name that cannot be kept", "POST", "/api/v1/passkeys/finish", `{"ceremony":"cer_x","name":"","credential":{}}`, 400, "invalid_name"},
		{"a field nobody asked for", "POST", "/api/v1/passkeys/finish", `{"ceremony":"cer_x","name":"x","credential":{},"account":"acc_x"}`, 400, "invalid_body"},
		{"a credential that is not one", "POST", "/api/v1/passkeys/finish", `{"ceremony":"cer_x","name":"x","credential":{"rawId":"not base64url!"}}`, 400, "invalid_body"},
		{"a padded identifier", "POST", "/api/v1/passkeys/finish", `{"ceremony":"cer_x","name":"x","credential":{"rawId":"AQ=="}}`, 400, "invalid_body"},
		{"not JSON", "POST", "/api/v1/passkeys/finish", `ceremony=cer_x`, 400, "invalid_body"},
		{"a sign-in that was not started", "POST", "/api/v1/auth/passkey/finish", `{"ceremony":"cer_x","credential":{"rawId":"AQID","response":{"userHandle":"YWNj"}}}`, 400, "invalid_passkey"},
		{"a large sign-in", "POST", "/api/v1/auth/passkey/finish", strings.Replace(fits, `"name":"x",`, "", 1), 400, "invalid_passkey"},
		{"more than a sign-in", "POST", "/api/v1/auth/passkey/finish", strings.Replace(large, `"name":"x",`, "", 1), 400, "invalid_body"},
		{"a sign-in that is not JSON", "POST", "/api/v1/auth/passkey/finish", `[]`, 400, "invalid_body"},
		{"a wrong recovery code", "POST", "/api/v1/auth/recovery", `{"address":"ana@example.org","code":"2222-2222-2222-2222"}`, 400, "invalid_code"},
		{"a recovery of nothing", "POST", "/api/v1/auth/recovery", `{}`, 400, "invalid_code"},
		{"a recovery that is not JSON", "POST", "/api/v1/auth/recovery", `address=ana`, 400, "invalid_body"},
		{"a recovery with more", "POST", "/api/v1/auth/recovery", `{"address":"ana@example.org","code":"x","on":false}`, 400, "invalid_body"},
		{"renaming a passkey that is not there", "PATCH", "/api/v1/passkeys/" + id, `{"name":"x"}`, 404, "not_found"},
		{"renaming to nothing", "PATCH", "/api/v1/passkeys/" + id, `{"name":" "}`, 400, "invalid_name"},
		{"renaming without a name", "PATCH", "/api/v1/passkeys/" + id, `{"title":"x"}`, 400, "invalid_body"},
		{"removing a passkey that is not there", "DELETE", "/api/v1/passkeys/" + id, "", 404, "not_found"},
		{"removing something that is not a passkey", "DELETE", "/api/v1/passkeys/not%20base64url", "", 404, "not_found"},
		{"passkey only without a passkey", "POST", "/api/v1/account/passkey-only", `{"on":true}`, 409, "passkey_needed"},
		{"passkey only without saying which", "POST", "/api/v1/account/passkey-only", `{"passkey_only":true}`, 400, "invalid_body"},
		{"passkey only without a body", "POST", "/api/v1/account/passkey-only", "", 400, "invalid_body"},
		{"recovery codes without passkey only", "POST", "/api/v1/account/recovery-codes", "", 409, "passkey_only_off"},
		{"a passkey by GET", "GET", "/api/v1/passkeys/" + id, "", 404, "not_found"},
		{"starting a sign-in by GET", "GET", "/api/v1/auth/passkey/start", "", 404, "not_found"},
	}
	for _, c := range cases {
		rec := a.call(c.method, c.path, c.body, nil)
		var e httpapi.APIError
		if rec.Code != c.status || json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Error.Code != c.code {
			t.Errorf("%s: %d %.200s, want %d %s", c.name, rec.Code, rec.Body, c.status, c.code)
		}
	}
	if a.cookie == nil {
		t.Fatal("a refusal dropped the session's cookie")
	}

	// An account with as many passkeys as one keeps.
	who, err := a.svc.Authenticate(context.Background(), a.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		a.makePasskey(webauthntest.New(webauthntest.EdDSA), fmt.Sprintf("key %d", i))
	}
	var e httpapi.APIError
	if read(t, a.call("POST", "/api/v1/passkeys/start", "", nil), 409, &e); e.Error.Code != "too_many_passkeys" {
		t.Fatalf("passkey twenty-one: %+v", e)
	}
	if list, _ := a.svc.Passkeys(context.Background(), who); len(list.Passkeys) != 20 {
		t.Fatalf("%d passkeys", len(list.Passkeys))
	}
}

// The name, asking to delete the account and cancelling, as the page asks
// for them.
func TestSettingsAPI(t *testing.T) {
	a := newApp(t)
	// Without a session, and from another site's page.
	requests := [][3]string{
		{"PATCH", "/api/v1/account", `{"name":"Ana"}`}, {"POST", "/api/v1/account/delete", ""}, {"POST", "/api/v1/account/delete/cancel", ""},
	}
	var e httpapi.APIError
	for _, c := range requests {
		if read(t, a.call(c[0], c[1], c[2], nil), 401, &e); e.Error.Code != "unauthorized" {
			t.Errorf("%s %s without a session: %+v", c[0], c[1], e)
		}
	}
	me := a.signIn("ana@example.org")
	for _, c := range requests {
		evil := func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }
		if read(t, a.call(c[0], c[1], c[2], evil), 403, &e); e.Error.Code != "forbidden" {
			t.Errorf("%s %s from another origin: %+v", c[0], c[1], e)
		}
	}
	phone := newApp(t)
	phone.h, phone.svc, phone.out = a.h, a.svc, a.out
	phone.signIn("ana@example.org")

	rec := a.call("PATCH", "/api/v1/account", `{"name":"  Ana Pérez "}`, nil)
	var named contracts.Account
	read(t, rec, 200, &named)
	want := me.Account
	want.Name = "Ana Pérez"
	if named != want || strings.Contains(rec.Body.String(), "closes_at") || !strings.Contains(rec.Body.String(), `"state":"active"`) {
		t.Fatalf("named: %s", rec.Body)
	}
	var seen contracts.Me
	if read(t, a.call("GET", "/api/v1/me", "", nil), 200, &seen); seen.Account != want {
		t.Fatalf("me: %+v", seen)
	}
	// What is not said stays as it is.
	for _, body := range []string{`{}`, `{"name":null}`} {
		if read(t, a.call("PATCH", "/api/v1/account", body, nil), 200, &named); named != want {
			t.Fatalf("%s: %+v", body, named)
		}
	}
	for body, code := range map[string]string{
		`{"name":"two\nlines"}`:                      "invalid_name",
		`{"name":"` + strings.Repeat("a", 81) + `"}`: "invalid_name",
		`{"name":7}`: "invalid_body",
		`{"name":"Ana","address":"eve@example.org"}`: "invalid_body",
		`name=Ana`: "invalid_body",
		``:         "invalid_body",
	} {
		if read(t, a.call("PATCH", "/api/v1/account", body, nil), 400, &e); e.Error.Code != code {
			t.Errorf("%q: %+v, want %s", body, e, code)
		}
	}
	if read(t, a.call("GET", "/api/v1/account", "", nil), 404, &e); e.Error.Code != "not_found" {
		t.Fatalf("the account by GET: %+v", e)
	}

	// Asking to delete: closing, with its day, and the other device is out.
	rec = a.call("POST", "/api/v1/account/delete", "", nil)
	var closing contracts.Account
	read(t, rec, 200, &closing)
	if closing.State != contracts.AccountClosing || closing.ID != me.Account.ID || !strings.Contains(rec.Body.String(), `"state":"closing"`) || !strings.Contains(rec.Body.String(), `"closes_at":"`) {
		t.Fatalf("asked to delete: %s", rec.Body)
	}
	if left := time.Until(closing.ClosesAt); left < 7*24*time.Hour-time.Minute || left > 7*24*time.Hour {
		t.Fatalf("closes at %s", closing.ClosesAt)
	}
	if read(t, a.call("GET", "/api/v1/me", "", nil), 200, &seen); seen.Account != closing {
		t.Fatalf("me while closing: %+v", seen)
	}
	if rec := phone.call("GET", "/api/v1/me", "", nil); rec.Code != 401 || phone.cookie != nil {
		t.Fatalf("the other device of a closing account: %d", rec.Code)
	}
	// Nothing changes while it is closing.
	for _, c := range [][3]string{
		{"PATCH", "/api/v1/account", `{"name":"Ana"}`}, {"PATCH", "/api/v1/account", `{}`}, {"POST", "/api/v1/passkeys/start", ""},
		{"POST", "/api/v1/account/passkey-only", `{"on":true}`}, {"POST", "/api/v1/account/recovery-codes", ""},
		{"DELETE", "/api/v1/passkeys/" + b64([]byte("some passkey")), ""},
	} {
		if read(t, a.call(c[0], c[1], c[2], nil), 409, &e); e.Error.Code != "account_closing" {
			t.Errorf("%s %s while closing: %+v", c[0], c[1], e)
		}
	}
	var again contracts.Account
	if read(t, a.call("POST", "/api/v1/account/delete", "", nil), 200, &again); again != closing {
		t.Fatalf("asked again: %+v", again)
	}
	// The person signs out and in again, and still sees it closing.
	read(t, a.call("POST", "/api/v1/auth/sign-out", "", nil), 204, nil)
	if back := a.signIn("ana@example.org"); back.Account != closing {
		t.Fatalf("signed in while closing: %+v", back.Account)
	}

	// Cancelling makes it what it was.
	rec = a.call("POST", "/api/v1/account/delete/cancel", "", nil)
	var kept contracts.Account
	if read(t, rec, 200, &kept); kept != want || strings.Contains(rec.Body.String(), "closes_at") {
		t.Fatalf("cancelled: %s", rec.Body)
	}
	if read(t, a.call("POST", "/api/v1/account/delete/cancel", "", nil), 200, &kept); kept != want {
		t.Fatalf("cancelled again: %+v", kept)
	}
	if read(t, a.call("PATCH", "/api/v1/account", `{"name":""}`, nil), 200, &named); named.Name != "" {
		t.Fatalf("a change after cancelling: %+v", named)
	}

	for _, secret := range []string{"ana@", "example.org", me.Account.ID, "Ana", "Pérez"} {
		if strings.Contains(a.logs.String(), secret) {
			t.Fatalf("the logs hold %q:\n%s", secret, a.logs)
		}
	}
}

// The operator's tool: only with the operator's token, only to look, and
// only what the accounts' signed records say.
func TestAccountsAdminAPI(t *testing.T) {
	a := newApp(t)
	log := slog.New(slog.NewTextHandler(a.logs, nil))
	h := httpapi.TokenAuth(tokenHash(), httpapi.Accounts(a.svc, log))
	var made []contracts.Me
	for _, address := range []string{"ana@example.org", "bea@example.org", "cho@example.org"} {
		made = append(made, a.signIn(address))
		a.ahead += time.Minute // the list is in the order of when each was made
	}
	read(t, a.call("PATCH", "/api/v1/account", `{"name":"Cho"}`, nil), 200, nil)
	read(t, a.call("POST", "/api/v1/account/delete", "", nil), 200, nil)
	one := "/v1/admin/accounts/" + made[0].Account.ID

	var e httpapi.APIError
	for _, path := range []string{"/v1/admin/accounts", one} {
		for _, auth := range []string{"", "not the token", a.cookie.Value} {
			rec := do(h, "GET", path, auth, "")
			if read(t, rec, 401, &e); e.Error.Code != "unauthorized" || strings.Contains(rec.Body.String(), "example.org") {
				t.Errorf("%s with %q: %s", path, auth, rec.Body)
			}
		}
		// It looks, and changes nothing.
		for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
			rec := do(h, method, path, token, `{"name":"x"}`)
			if read(t, rec, 405, &e); e.Error.Code != "method_not_allowed" || rec.Header().Get("Allow") != "GET" {
				t.Errorf("%s %s: %d %s", method, path, rec.Code, rec.Body)
			}
		}
	}
	// The session of an account is not the operator's token, and the app's
	// own API knows nothing of the tool.
	if rec := a.call("GET", "/v1/admin/accounts", "", nil); rec.Code != 404 {
		t.Fatalf("the tool through the app's API: %d", rec.Code)
	}

	rec := do(h, "GET", "/v1/admin/accounts", token, "")
	var list contracts.AccountList
	read(t, rec, 200, &list)
	if len(list.Accounts) != 3 || list.Unreadable != 0 || list.Next != "" || strings.Contains(rec.Body.String(), `"next"`) || !strings.Contains(rec.Body.String(), `"unreadable":0`) {
		t.Fatalf("the list: %s", rec.Body)
	}
	for i, r := range list.Accounts {
		if r.ID != made[i].Account.ID || r.Address != made[i].Account.Address || !r.CreatedAt.Equal(made[i].Account.CreatedAt) || !r.AddressVerifiedAt.Equal(r.CreatedAt) || r.Sessions != 1 || r.Language != "en" {
			t.Fatalf("account %d of the list: %+v", i, r)
		}
	}
	if !strings.Contains(rec.Body.String(), `"passkeys":0,"passkey_only":false,"providers":[],"sessions":1,"terms":{"version":"`+domain.TermsVersion+`","at":"`) || strings.Count(rec.Body.String(), "closes_at") != 1 {
		t.Fatalf("the list: %s", rec.Body)
	}
	if cho := list.Accounts[2]; cho.State != contracts.AccountClosing || cho.Name != "Cho" || cho.ClosesAt.IsZero() || list.Accounts[0].State != contracts.AccountActive {
		t.Fatalf("a closing account: %+v", cho)
	}

	// A page at a time.
	var page contracts.AccountList
	read(t, do(h, "GET", "/v1/admin/accounts?limit=2", token, ""), 200, &page)
	if len(page.Accounts) != 2 || page.Next == "" || page.Accounts[1].ID != made[1].Account.ID {
		t.Fatalf("the first page: %+v", page)
	}
	var last contracts.AccountList
	read(t, do(h, "GET", "/v1/admin/accounts?limit=2&after="+url.QueryEscape(page.Next), token, ""), 200, &last)
	if len(last.Accounts) != 1 || last.Next != "" || last.Accounts[0].ID != made[2].Account.ID {
		t.Fatalf("the second page: %+v", last)
	}
	for query, code := range map[string]string{
		"limit=0": "invalid_limit", "limit=101": "invalid_limit", "limit=many": "invalid_limit", "limit=-1": "invalid_limit",
		"after=zzz": "invalid_cursor", "after=" + made[0].Account.ID: "invalid_cursor",
	} {
		if read(t, do(h, "GET", "/v1/admin/accounts?"+query, token, ""), 400, &e); e.Error.Code != code {
			t.Errorf("%s: %+v, want %s", query, e, code)
		}
	}

	// One account.
	rec = do(h, "GET", one, token, "")
	var got contracts.AccountRecord
	if read(t, rec, 200, &got); !reflect.DeepEqual(got, list.Accounts[0]) {
		t.Fatalf("an account: %s", rec.Body)
	}
	for _, path := range []string{"/v1/admin/accounts/acc_" + strings.Repeat("a", 26), "/v1/admin/accounts/x", "/v1/admin/accounts/", "/v1/admin/accounts/" + made[0].Account.ID + "/sessions", "/v1/admin/nothing"} {
		if read(t, do(h, "GET", path, token, ""), 404, &e); e.Error.Code != "not_found" {
			t.Errorf("%s: %+v", path, e)
		}
	}

	// The storage fails: nothing of it is answered, and what is logged names
	// no account.
	id := made[0].Account.ID
	down := httpapi.TokenAuth(tokenHash(), httpapi.Accounts(broken{err: errors.New("store: commits of account-" + id + ": down")}, log))
	for _, path := range []string{"/v1/admin/accounts", one} {
		rec := do(down, "GET", path, token, "")
		if rec.Code != 500 || strings.Contains(rec.Body.String(), "store") || !strings.Contains(rec.Body.String(), `"internal"`) {
			t.Fatalf("%s with the storage down: %d %s", path, rec.Code, rec.Body)
		}
	}
	if logs := a.logs.String(); !strings.Contains(logs, "commits of account-acc_…: down") {
		t.Fatalf("logs:\n%s", logs)
	}
	for _, secret := range []string{"ana@", "example.org", made[0].Account.ID, made[2].Account.ID, "Cho", token} {
		if strings.Contains(a.logs.String(), secret) {
			t.Fatalf("the logs hold %q:\n%s", secret, a.logs)
		}
	}
}

// What the tool answers is what the enclave signed. An account whose record
// was changed in the table is not shown as the table says: it is counted as
// one that cannot be read, and asked for by itself it is an error.
func TestAccountsAdminAPIShowsTheSignedRecords(t *testing.T) {
	a := newApp(t)
	mem, logs := a.mem, a.logs
	h := httpapi.TokenAuth(tokenHash(), httpapi.Accounts(a.svc, slog.New(slog.NewTextHandler(logs, nil))))
	ana := a.signIn("ana@example.org")
	a.ahead += time.Minute
	eve := a.signIn("eve@example.org")

	// The operator writes a name and "passkey only" into Eve's record.
	names := mem.RecordNames()
	var forged *store.Record
	for _, name := range names {
		if strings.HasPrefix(name, "account-"+eve.Account.ID+"@") {
			forged, _ = mem.GetRecord(context.Background(), name)
		}
	}
	if forged == nil {
		t.Fatalf("no record of the account among %v", names)
	}
	forged.Value = bytes.Replace(forged.Value, []byte(`"name":""`), []byte(`"name":"Written by hand","passkey_only":true`), 1)
	mem.SetRecord(*forged)
	// And an entry into the list, for an account that is not there.
	ghost := "acc_" + strings.Repeat("a", 26)
	mem.SetEntry(store.Entry{Partition: "accounts", Key: "20200101T000000.000000000Z." + ghost, Value: []byte(`{"doc":{"account":"` + ghost + `"},"mac":"AAAA"}`), Version: 1})

	rec := do(h, "GET", "/v1/admin/accounts", token, "")
	var list contracts.AccountList
	read(t, rec, 200, &list)
	if len(list.Accounts) != 1 || list.Accounts[0].ID != ana.Account.ID || list.Unreadable != 1 || strings.Contains(rec.Body.String(), "Written by hand") || strings.Contains(rec.Body.String(), eve.Account.ID) || strings.Contains(rec.Body.String(), ghost) {
		t.Fatalf("the list with a record changed by hand: %s", rec.Body)
	}
	rec = do(h, "GET", "/v1/admin/accounts/"+eve.Account.ID, token, "")
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "Written by hand") || !strings.Contains(rec.Body.String(), `"internal"`) {
		t.Fatalf("a record changed by hand: %d %s", rec.Code, rec.Body)
	}
	var e httpapi.APIError
	if read(t, do(h, "GET", "/v1/admin/accounts/"+ghost, token, ""), 404, &e); e.Error.Code != "not_found" {
		t.Fatalf("an account that is not there: %+v", e)
	}
	for _, secret := range []string{"example.org", ana.Account.ID, eve.Account.ID, "Written by hand"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("the logs hold %q:\n%s", secret, logs)
		}
	}
}

// Signing up takes the agreement to the terms in force; GET me says which the
// account agreed to and which are in force; agreeing names the version.
func TestTermsAPI(t *testing.T) {
	a := newApp(t)
	inv, err := a.svc.CreateInvitations(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	var e httpapi.APIError
	for _, terms := range []string{``, `,"terms":"2020-01-01"`} {
		if read(t, a.call("POST", "/api/v1/auth/email/start", `{"address":"ana@example.org","invitation":"`+inv.Invitations[0]+`"`+terms+`}`, nil), 400, &e); e.Error.Code != "terms_needed" {
			t.Fatalf("signing up with %q: %+v", terms, e)
		}
	}
	if read(t, a.call("POST", "/api/v1/account/terms", `{"version":"`+domain.TermsVersion+`"}`, nil), 401, &e); e.Error.Code != "unauthorized" {
		t.Fatalf("agreeing signed out: %+v", e)
	}
	a.signIn("ana@example.org")
	rec := a.call("GET", "/api/v1/me", "", nil)
	if read(t, rec, 200, nil); !strings.Contains(rec.Body.String(), `"terms":"`+domain.TermsVersion+`"}`) || !strings.Contains(rec.Body.String(), `"passkey_only":false,"terms":"`+domain.TermsVersion+`"}`) {
		t.Fatalf("me: %s", rec.Body)
	}
	for body, code := range map[string]string{
		`{"version":"2020-01-01"}`: "terms_needed",
		`{}`:                       "terms_needed",
		`{"version":"x","at":"y"}`: "invalid_body",
	} {
		if read(t, a.call("POST", "/api/v1/account/terms", body, nil), 400, &e); e.Error.Code != code {
			t.Errorf("%s: %+v", body, e)
		}
		// The account exists: nothing is said of making one.
		if strings.Contains(e.Error.Message, "make an account") {
			t.Errorf("%s: %q", body, e.Error.Message)
		}
	}
	var agreed contracts.Account
	if read(t, a.call("POST", "/api/v1/account/terms", `{"version":"`+domain.TermsVersion+`"}`, nil), 200, &agreed); agreed.Terms != domain.TermsVersion {
		t.Fatalf("agreeing: %+v", agreed)
	}
	evil := func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }
	if read(t, a.call("POST", "/api/v1/account/terms", `{"version":"`+domain.TermsVersion+`"}`, evil), 403, &e); e.Error.Code != "forbidden" {
		t.Fatalf("from another origin: %+v", e)
	}
}
