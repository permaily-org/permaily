package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/webauthn/webauthntest"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// admin is the operator's routes as the enclave serves them: accounts,
// invitations and the providers' applications, each behind the admin token.
func (a *app) admin() http.Handler {
	log := slog.New(slog.NewTextHandler(a.logs, nil))
	mux := http.NewServeMux()
	for prefix, h := range map[string]http.Handler{
		"/v1/admin/accounts": httpapi.Accounts(a.svc, log), "/v1/admin/invitations": httpapi.Invitations(a.svc, log),
		"/v1/admin/providers": httpapi.ProviderApps(a.svc, log),
	} {
		mux.Handle(prefix, httpapi.TokenAuth(tokenHash(), h))
		mux.Handle(prefix+"/", httpapi.TokenAuth(tokenHash(), h))
	}
	return mux
}

// stored is what the storage holds of an account's record: every version of
// it, as written.
func (a *app) stored(id string) map[string]string {
	a.t.Helper()
	out := map[string]string{}
	for _, name := range a.mem.RecordNames() {
		if strings.Contains(name, id) {
			rec, err := a.mem.GetRecord(context.Background(), name)
			if err != nil {
				a.t.Fatal(err)
			}
			out[name] = string(rec.Value)
		}
	}
	return out
}

// sessions is how many devices are signed in to an account, as the operator's
// tool counts them.
func (a *app) record(id string) contracts.AccountRecord {
	a.t.Helper()
	rec, err := a.svc.AccountRecord(context.Background(), id)
	if err != nil {
		a.t.Fatal(err)
	}
	return *rec
}

func TestOperatorAPI(t *testing.T) {
	a := newApp(t)
	h := a.admin()
	a.on("google")
	me := a.signIn("ana@example.org")
	state := a.leave("/api/v1/providers/start", `{"provider":"google"}`)
	if rec, to := a.comeBack(state, idp.Identity{Provider: idp.Google, Subject: "the-subject", Address: "ana@example.org", Verified: true}); to != "/settings" {
		t.Fatalf("linking: %d %s", rec.Code, to)
	}
	id := me.Account.ID
	one := "/v1/admin/accounts/" + id
	actions := []string{"/block", "/unblock", "/delete", "/delete/cancel", "/sessions/close"}

	// Every one of them needs the operator's token, and is a POST.
	var e httpapi.APIError
	for _, action := range actions {
		for _, auth := range []string{"", "not the token", a.cookie.Value} {
			rec := do(h, "POST", one+action, auth, "")
			if read(t, rec, 401, &e); e.Error.Code != "unauthorized" || strings.Contains(rec.Body.String(), "example.org") {
				t.Errorf("%s with %q: %s", action, auth, rec.Body)
			}
		}
		for _, method := range []string{"GET", "PUT", "PATCH", "DELETE"} {
			rec := do(h, method, one+action, token, "")
			if read(t, rec, 405, &e); e.Error.Code != "method_not_allowed" || rec.Header().Get("Allow") != "POST" {
				t.Errorf("%s %s: %d %s", method, action, rec.Code, rec.Body)
			}
		}
		for _, other := range []string{"acc_" + strings.Repeat("a", 26), "x", me.Session.ID} {
			if read(t, do(h, "POST", "/v1/admin/accounts/"+other+action, token, ""), 404, &e); e.Error.Code != "not_found" {
				t.Errorf("%s of %s: %+v", action, other, e)
			}
		}
	}
	if rec := a.record(id); !rec.BlockedAt.IsZero() || rec.State != contracts.AccountActive || rec.Sessions != 1 {
		t.Fatalf("what was refused changed the account: %+v", rec)
	}
	// The app's own API knows nothing of them, with a session or with the token.
	for _, action := range actions {
		if rec := a.call("POST", one+action, "", nil); rec.Code != 404 {
			t.Errorf("%s through the app's API: %d", action, rec.Code)
		}
	}

	act := func(action string) (contracts.AccountRecord, string) {
		t.Helper()
		rec := do(h, "POST", one+action, token, "")
		var got contracts.AccountRecord
		read(t, rec, 200, &got)
		if got.ID != id || got.Address != "ana@example.org" {
			t.Fatalf("%s answered %s", action, rec.Body)
		}
		return got, rec.Body.String()
	}
	// The account as the operator sees it: which providers, never who there.
	got, body := act("/sessions/close")
	if got.Sessions != 0 || !reflect.DeepEqual(got.Providers, []string{"google"}) || strings.Contains(body, "the-subject") || strings.Contains(body, "blocked_at") || strings.Contains(body, "closing_by") {
		t.Fatalf("after closing the sessions: %s", body)
	}
	if rec := a.call("GET", "/api/v1/me", "", nil); rec.Code != 401 || a.cookie != nil {
		t.Fatalf("a session the operator closed: %d", rec.Code)
	}
	if got, body = act("/block"); got.BlockedAt.IsZero() || !strings.Contains(body, `"blocked_at":"`) {
		t.Fatalf("after blocking: %s", body)
	}
	if got, body = act("/delete"); got.State != contracts.AccountClosing || got.ClosingBy != "operator" || got.ClosesAt.IsZero() || got.BlockedAt.IsZero() || !strings.Contains(body, `"closing_by":"operator"`) {
		t.Fatalf("after asking to delete: %s", body)
	}
	if got, body = act("/delete/cancel"); got.State != contracts.AccountActive || got.ClosingBy != "" || !got.ClosesAt.IsZero() || strings.Contains(body, "closing_by") || strings.Contains(body, "closes_at") {
		t.Fatalf("after cancelling: %s", body)
	}
	if got, body = act("/unblock"); !got.BlockedAt.IsZero() || strings.Contains(body, "blocked_at") {
		t.Fatalf("after unblocking: %s", body)
	}
	// A body is not read: there is nothing to say but which account.
	rec := do(h, "POST", one+"/block", token, `{"address":"eve@example.org","blocked_at":"2001-01-01T00:00:00Z"}`)
	read(t, rec, 200, &got)
	if got.Address != "ana@example.org" || got.BlockedAt.Year() == 2001 {
		t.Fatalf("a body was read: %s", rec.Body)
	}
	act("/unblock")

	// A deletion the person asked for is theirs to cancel, not the operator's.
	a.signIn("ana@example.org")
	read(t, a.call("POST", "/api/v1/account/delete", "", nil), 200, nil)
	if read(t, do(h, "POST", one+"/delete/cancel", token, ""), 409, &e); e.Error.Code != "not_cancellable" || e.Error.Message != "only the person can cancel a deletion they asked for" {
		t.Fatalf("the operator cancels what the person asked: %+v", e)
	}
	if rec := a.record(id); rec.State != contracts.AccountClosing || rec.ClosingBy != contracts.ClosingByPerson {
		t.Fatalf("after the refusal: %+v", rec)
	}
	read(t, a.call("POST", "/api/v1/account/delete/cancel", "", nil), 200, nil)

	// Finding the account of an address.
	var list contracts.AccountList
	for _, q := range []string{"ana@example.org", " ANA@Example.org "} {
		rec := do(h, "GET", "/v1/admin/accounts?address="+url.QueryEscape(q), token, "")
		if read(t, rec, 200, &list); len(list.Accounts) != 1 || list.Accounts[0].ID != id || list.Next != "" || !strings.Contains(rec.Body.String(), `"unreadable":0`) {
			t.Fatalf("finding %q: %s", q, rec.Body)
		}
	}
	rec = do(h, "GET", "/v1/admin/accounts?address=nobody%40example.org", token, "")
	if read(t, rec, 200, &list); len(list.Accounts) != 0 || !strings.Contains(rec.Body.String(), `"accounts":[]`) {
		t.Fatalf("finding nobody: %s", rec.Body)
	}
	for _, q := range []string{"", "ana", "ana%40"} {
		if read(t, do(h, "GET", "/v1/admin/accounts?address="+q, token, ""), 400, &e); e.Error.Code != "invalid_address" {
			t.Errorf("finding %q: %+v", q, e)
		}
	}
	if rec := do(h, "GET", "/v1/admin/accounts?address=ana%40example.org", "", ""); rec.Code != 401 || strings.Contains(rec.Body.String(), id) {
		t.Fatalf("finding without the token: %d %s", rec.Code, rec.Body)
	}

	// The storage fails: nothing of it is answered, and no account is logged.
	down := httpapi.TokenAuth(tokenHash(), httpapi.Accounts(broken{err: errors.New("store: commits of account-" + id + ": down")}, slog.New(slog.NewTextHandler(a.logs, nil))))
	for _, path := range append([]string{"/v1/admin/accounts?address=ana%40example.org"}, func() (out []string) {
		for _, action := range actions {
			out = append(out, one+action)
		}
		return out
	}()...) {
		method := "POST"
		if strings.Contains(path, "?") {
			method = "GET"
		}
		rec := do(down, method, path, token, "")
		if rec.Code != 500 || strings.Contains(rec.Body.String(), "store") || !strings.Contains(rec.Body.String(), `"internal"`) {
			t.Fatalf("%s with the storage down: %d %s", path, rec.Code, rec.Body)
		}
	}
	for _, secret := range []string{"ana@", "example.org", id, token, "the-subject"} {
		if strings.Contains(a.logs.String(), secret) {
			t.Fatalf("the logs hold %q:\n%s", secret, a.logs)
		}
	}
}

func (b broken) AccountRecordByAddress(context.Context, string) (*contracts.AccountList, error) {
	return nil, b.err
}

func (b broken) BlockAccount(context.Context, string) (*contracts.AccountRecord, error) {
	return nil, b.err
}

func (b broken) UnblockAccount(context.Context, string) (*contracts.AccountRecord, error) {
	return nil, b.err
}

func (b broken) DeleteAccount(context.Context, string) (*contracts.AccountRecord, error) {
	return nil, b.err
}

func (b broken) KeepAccount(context.Context, string) (*contracts.AccountRecord, error) {
	return nil, b.err
}

func (b broken) CloseAccountSessions(context.Context, string) (*contracts.AccountRecord, error) {
	return nil, b.err
}

func (b broken) Invitations(context.Context) (*contracts.InvitationList, error) { return nil, b.err }

func (b broken) RevokeInvitation(context.Context, string) error { return b.err }

func (b broken) StartAddressChange(context.Context, *accounts.Authenticated, contracts.AddressStart) (*contracts.EmailStarted, error) {
	return nil, b.err
}

// The operator's routes cannot change a contact address or a way to sign in,
// turn "passkey only" off or sign in as someone: there is no route that does
// it, whatever is asked, and the service the routes are given could not.
func TestTheOperatorsRoutesCannotChangeAnAddressOrAWayIn(t *testing.T) {
	a := newApp(t)
	h := a.admin()
	a.on("google")
	me := a.signIn("ana@example.org")
	id := me.Account.ID
	key := webauthntest.New(webauthntest.ES256)
	passkey := a.makePasskey(key, "laptop")
	state := a.leave("/api/v1/providers/start", `{"provider":"google"}`)
	a.comeBack(state, idp.Identity{Provider: idp.Google, Subject: "the-subject", Address: "ana@example.org", Verified: true})
	var only contracts.PasskeyOnlyState
	read(t, a.call("POST", "/api/v1/account/passkey-only", `{"on":true}`, nil), 200, &only)

	// What the routes can be asked of the service is this, and nothing else.
	// A method that changes an address or a way in, or signs someone in, is
	// in none of it: adding one here is adding a way into every account.
	var names []string
	service := reflect.TypeOf((*httpapi.OperatorService)(nil)).Elem()
	for i := range service.NumMethod() {
		names = append(names, service.Method(i).Name)
	}
	want := []string{
		"AccountRecord", "AccountRecordByAddress", "AccountRecords", "AdminLog", "Admins", "BlockAccount", "CloseAccountSessions",
		"CreateInvitations", "DeleteAccount", "Invitations", "KeepAccount", "RevokeInvitation", "Stats", "UnblockAccount",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("what the operator's routes can ask of the service:\n%v\nwant\n%v", names, want)
	}
	// None of them takes anything but which account, an address to look up,
	// a place in the list or a count: nothing to write into an account.
	for i := range service.NumMethod() {
		m := service.Method(i).Type
		for j := 1; j < m.NumIn(); j++ {
			if k := m.In(j).Kind(); k != reflect.String && k != reflect.Int {
				t.Errorf("%s takes a %s", service.Method(i).Name, m.In(j))
			}
		}
	}

	before, record := a.stored(id), a.record(id)
	bodies := []string{
		"", `{"address":"eve@example.org"}`, `{"name":"x","address":"eve@example.org","address_verified_at":"2026-01-01T00:00:00Z"}`,
		`{"passkey_only":false}`, `{"on":false}`, `{"passkeys":[],"providers":[],"recovery_codes":[]}`, `{"provider":"github","subject":"eve"}`,
		`{"attempt":"att_x","code":"000000"}`,
	}
	one := "/v1/admin/accounts/" + id
	paths := []string{
		one, one + "/", one + "/address", one + "/address/start", one + "/address/finish", one + "/email", one + "/passkeys", one + "/passkeys/" + passkey,
		one + "/passkeys/start", one + "/passkeys/finish", one + "/passkey-only", one + "/recovery-codes", one + "/providers", one + "/providers/google",
		one + "/providers/start", one + "/sessions", one + "/session", one + "/sign-in", one + "/impersonate", one + "/token", one + "/recover",
		one + "?address=eve%40example.org", "/v1/admin/accounts?address=ana%40example.org", "/v1/admin/accounts",
		"/v1/admin/account", "/v1/admin/account/address/start", "/v1/admin/auth/email/finish", "/v1/admin/auth/recovery", "/v1/admin/me",
		"/v1/admin/passkeys/" + passkey, "/v1/admin/invitations/" + id,
	}
	for _, path := range paths {
		for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
			for _, body := range bodies {
				rec := do(h, method, path, token, body)
				if rec.Code != 404 && rec.Code != 405 {
					t.Errorf("%s %s %s: %d %s", method, path, body, rec.Code, rec.Body)
				}
				if c := set(rec, httpapi.SessionCookie); c != nil {
					t.Errorf("%s %s set a session", method, path)
				}
			}
		}
	}
	// What the routes do take changes none of it either, whatever comes along.
	for _, action := range []string{"/block", "/delete", "/unblock", "/delete/cancel"} {
		for _, body := range bodies {
			rec := do(h, "POST", one+action+"?address=eve%40example.org&passkey_only=false", token, body)
			if rec.Code != 200 || set(rec, httpapi.SessionCookie) != nil {
				t.Fatalf("%s %s: %d %s", action, body, rec.Code, rec.Body)
			}
		}
	}
	after := a.record(id)
	// Blocking signed its devices out, which is all that changed.
	if after.Address != record.Address || !after.AddressVerifiedAt.Equal(record.AddressVerifiedAt) || after.Passkeys != 1 || !after.PasskeyOnly || !reflect.DeepEqual(after.Providers, []string{"google"}) || after.Sessions != 0 || record.Sessions != 1 {
		t.Fatalf("the account after all of it: %+v, was %+v", after, record)
	}
	// The record itself, apart from what blocking and deleting wrote and
	// took back: the same address, passkeys, switch, codes and providers.
	var was, is map[string]json.RawMessage
	for name, value := range before {
		if json.Unmarshal([]byte(value[strings.Index(value, "{"):]), &was) != nil {
			t.Fatalf("the record %s as it was", name)
		}
	}
	for name, value := range a.stored(id) {
		if json.Unmarshal([]byte(value[strings.Index(value, "{"):]), &is) != nil {
			t.Fatalf("the record %s as it is", name)
		}
	}
	if len(was) == 0 || !reflect.DeepEqual(was, is) {
		t.Fatalf("the record changed:\n%s\n%s", was, is)
	}
	// And the ways in still work, for their person: the passkey and a
	// recovery code.
	a.cookie = nil
	read(t, a.passkeySignIn(key, passkey), 200, nil)
	read(t, a.call("GET", "/api/v1/me", "", nil), 200, nil)
	read(t, a.call("POST", "/api/v1/auth/recovery", `{"address":"ana@example.org","code":"`+only.RecoveryCodes[0]+`"}`, nil), 200, nil)

	// The operator's token is no session: the app's API, where an address and
	// the ways in are changed, does not take it.
	a.cookie = nil
	for _, c := range [][2]string{
		{"GET", "/api/v1/me"}, {"POST", "/api/v1/account/address/start"}, {"POST", "/api/v1/account/address/finish"}, {"POST", "/api/v1/passkeys/start"},
		{"POST", "/api/v1/account/passkey-only"}, {"POST", "/api/v1/account/recovery-codes"}, {"DELETE", "/api/v1/providers/google"}, {"DELETE", "/api/v1/passkeys/" + passkey},
	} {
		rec := a.call(c[0], c[1], `{"on":false,"address":"eve@example.org"}`, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
			r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: token})
		})
		if rec.Code != 401 {
			t.Errorf("%s %s with the operator's token: %d %s", c[0], c[1], rec.Code, rec.Body)
		}
	}
	if got := a.record(id); !got.PasskeyOnly || got.Address != "ana@example.org" || got.Passkeys != 1 {
		t.Fatalf("the account: %+v", got)
	}
}

// What a blocked person is told, and when: account_blocked, with 403, by
// every way of signing in, once they proved who they are and never before;
// and nobody is signed in.
func TestBlockedAPI(t *testing.T) {
	a := newApp(t)
	h := a.admin()
	a.on("google", "meta")
	me := a.signIn("ana@example.org")
	id := me.Account.ID
	key := webauthntest.New(webauthntest.ES256)
	passkey := a.makePasskey(key, "laptop")
	google := idp.Identity{Provider: idp.Google, Subject: "g-ana", Address: "ana@example.org", Verified: true}
	state := a.leave("/api/v1/providers/start", `{"provider":"google"}`)
	a.comeBack(state, google)
	// Bea signs in with passkeys only.
	a.cookie, a.device = nil, nil
	bea := a.signIn("bea@example.org")
	beaKey := webauthntest.New(webauthntest.ES256)
	beaPasskey := a.makePasskey(beaKey, "phone")
	var only contracts.PasskeyOnlyState
	read(t, a.call("POST", "/api/v1/account/passkey-only", `{"on":true}`, nil), 200, &only)
	old := a.cookie

	for _, account := range []string{id, bea.Account.ID} {
		read(t, do(h, "POST", "/v1/admin/accounts/"+account+"/block", token, ""), 200, nil)
	}
	// The session it had is nothing, and the browser drops its cookie.
	rec := a.call("GET", "/api/v1/me", "", nil)
	var e httpapi.APIError
	if read(t, rec, 401, &e); e.Error.Code != "unauthorized" || a.cookie != nil {
		t.Fatalf("a session of a blocked account: %s", rec.Body)
	}

	blocked := func(what string, rec *httptest.ResponseRecorder) {
		t.Helper()
		var e httpapi.APIError
		read(t, rec, 403, &e)
		if e.Error.Code != "account_blocked" || e.Error.Message != "this account is blocked; write to support@permaily.com" {
			t.Fatalf("%s: %s", what, rec.Body)
		}
		if set(rec, httpapi.SessionCookie) != nil || set(rec, httpapi.DeviceCookie) != nil || a.cookie != nil {
			t.Fatalf("%s: a cookie was set for a blocked account", what)
		}
	}
	start := func(address string) (string, string, string) {
		t.Helper()
		rec := a.call("POST", "/api/v1/auth/email/start", `{"address":"`+address+`"}`, nil)
		var started contracts.EmailStarted
		read(t, rec, 200, &started)
		last := a.mailOf(started.Attempt)
		if last.To != address || last.Subject != "Sign in to Permaily" {
			t.Fatalf("no sign-in message for %s: %+v", address, last)
		}
		return started.Attempt, sentCode.FindStringSubmatch(last.Text)[1], sentLink.FindStringSubmatch(last.Text)[2]
	}

	// Before the proof nothing says it: starting answers as for anyone, a
	// wrong code is a wrong code, and so is a passkey that is not the account's.
	attempt, code, _ := start("ana@example.org")
	wrong := "000000"
	if code == wrong {
		wrong = "000001"
	}
	if read(t, a.call("POST", "/api/v1/auth/email/finish", `{"attempt":"`+attempt+`","code":"`+wrong+`"}`, nil), 400, &e); e.Error.Code != "invalid_code" {
		t.Fatalf("a wrong code for a blocked address: %+v", e)
	}
	if read(t, a.passkeySignIn(webauthntest.New(webauthntest.ES256), passkey), 400, &e); e.Error.Code != "invalid_passkey" {
		t.Fatalf("a stranger's passkey for a blocked account: %+v", e)
	}
	if read(t, a.call("POST", "/api/v1/auth/recovery", `{"address":"bea@example.org","code":"2222-2222-2222-2222"}`, nil), 400, &e); e.Error.Code != "invalid_code" {
		t.Fatalf("a wrong recovery code for a blocked account: %+v", e)
	}

	// With the proof: the code, the link, the passkey, the recovery code.
	blocked("the code of a sign-in by mail", a.call("POST", "/api/v1/auth/email/finish", `{"attempt":"`+attempt+`","code":"`+code+`"}`, nil))
	attempt, _, link := start("ana@example.org")
	var pending contracts.EmailPending
	if read(t, a.call("POST", "/api/v1/auth/email/look", `{"attempt":"`+attempt+`","token":"`+link+`"}`, nil), 200, &pending); pending.Address != "ana@example.org" {
		t.Fatalf("looking at the link: %+v", pending)
	}
	blocked("the link of a sign-in by mail", a.call("POST", "/api/v1/auth/email/finish", `{"attempt":"`+attempt+`","token":"`+link+`"}`, nil))
	blocked("a passkey", a.passkeySignIn(key, passkey))
	blocked("a passkey of a passkey-only account", a.passkeySignIn(beaKey, beaPasskey))
	blocked("a recovery code", a.call("POST", "/api/v1/auth/recovery", `{"address":"bea@example.org","code":"`+only.RecoveryCodes[0]+`"}`, nil))
	// The provider: the reason is in the fragment, next to the provider.
	state = a.leave("/api/v1/auth/provider/start", `{"provider":"google"}`)
	rec, to := a.comeBack(state, google)
	if to != "/sign-in#provider_error=account_blocked&provider=google" || set(rec, httpapi.SessionCookie) != nil || set(rec, httpapi.DeviceCookie) != nil {
		t.Fatalf("a provider for a blocked account: %s", to)
	}
	// A provider that does not vouch for the address, confirmed by mail.
	state = a.leave("/api/v1/auth/provider/start", `{"provider":"meta"}`)
	if _, to := a.comeBack(state, idp.Identity{Provider: idp.Meta, Subject: "fb-ana", Address: "ana@example.org"}); to != "/sign-in#provider_confirm" {
		t.Fatalf("a provider that does not vouch: %s", to)
	}
	var started contracts.EmailStarted
	read(t, a.call("POST", "/api/v1/auth/provider/confirm/start", `{"address":"ana@example.org"}`, nil), 200, &started)
	blocked("confirming an address for a provider", a.call("POST", "/api/v1/auth/provider/confirm/finish", `{"attempt":"`+started.Attempt+`","code":"`+sentCode.FindStringSubmatch(a.mailOf(started.Attempt).Text)[1]+`"}`, nil))

	// Nobody is signed in to either, and the cookie from before stays nothing.
	for _, account := range []string{id, bea.Account.ID} {
		if got := a.record(account); got.Sessions != 0 || got.BlockedAt.IsZero() || len(got.Providers) > 1 {
			t.Fatalf("a blocked account: %+v", got)
		}
	}
	a.cookie = old
	if rec := a.call("GET", "/api/v1/me", "", nil); rec.Code != 401 {
		t.Fatalf("the session from before: %d", rec.Code)
	}
	for _, secret := range []string{"ana@", "bea@", "example.org", id, bea.Account.ID} {
		if strings.Contains(a.logs.String(), secret) {
			t.Fatalf("the logs hold %q:\n%s", secret, a.logs)
		}
	}
}

// Changing the contact address, as the page asks for it.
func TestAddressAPI(t *testing.T) {
	a := newApp(t)
	h := a.admin()
	me := a.signIn("ana@example.org")
	session := a.cookie
	a.cookie, a.device = nil, nil
	bea := a.signIn("bea@example.org")
	beaSession := a.cookie
	a.cookie = session
	const start, finish = "/api/v1/account/address/start", "/api/v1/account/address/finish"

	ask := func(address string) (contracts.EmailStarted, string, *accounts.Mail) {
		t.Helper()
		before := len(a.out.Sent())
		rec := a.call("POST", start, `{"address":"`+address+`"}`, nil)
		var started contracts.EmailStarted
		read(t, rec, 200, &started)
		if started.ExpiresIn != 600 || !strings.HasPrefix(started.Attempt, "att_") {
			t.Fatalf("the answer: %s", rec.Body)
		}
		return started, rec.Body.String(), a.mailTo(before, address, "Confirm your new address for Permaily")
	}
	// The answer is the same whether the address is another account's or
	// nobody's, and the message is sent either way.
	free, freeBody, freeMail := ask("new@example.org")
	taken, takenBody, takenMail := ask("bea@example.org")
	if freeMail == nil || takenMail == nil || freeMail.To != "new@example.org" || takenMail.To != "bea@example.org" || freeMail.Subject != takenMail.Subject {
		t.Fatalf("the messages: %+v %+v", freeMail, takenMail)
	}
	if strings.ReplaceAll(freeBody, free.Attempt, "") != strings.ReplaceAll(takenBody, taken.Attempt, "") {
		t.Fatalf("the answers differ:\n%s\n%s", freeBody, takenBody)
	}
	code := func(m *accounts.Mail) string { return sentCode.FindStringSubmatch(m.Text)[1] }

	var e httpapi.APIError
	refused := func(what string, rec *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		var e httpapi.APIError
		if read(t, rec, status, &e); e.Error.Code != code {
			t.Fatalf("%s: %d %s, want %s", what, rec.Code, rec.Body, code)
		}
	}
	// The refusals of starting.
	for _, address := range []string{"", "new", "ana@example.org", " ANA@example.org"} {
		refused("starting with "+address, a.call("POST", start, `{"address":"`+address+`"}`, nil), 400, "invalid_address")
	}
	refused("an unknown field", a.call("POST", start, `{"address":"x@example.org","invitation":"x"}`, nil), 400, "invalid_body")
	refused("no body", a.call("POST", start, "", nil), 400, "invalid_body")
	for _, path := range []string{start, finish} {
		refused("another site", a.call("POST", path, `{}`, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }), 403, "forbidden")
		rec := a.call("POST", path, `{"address":"x@example.org","attempt":"att_x","code":"1"}`, func(r *http.Request) { r.Header.Del("Cookie") })
		refused("no session", rec, 401, "unauthorized")
		refused("a GET", a.call("GET", path, "", nil), 404, "not_found")
	}
	a.cookie = session

	// The refusals of ending: a wrong code, an attempt that is not one, the
	// attempt of another account, a sign-in's attempt.
	wrong := "000000"
	if code(freeMail) == wrong {
		wrong = "000001"
	}
	refused("a wrong code", a.call("POST", finish, `{"attempt":"`+free.Attempt+`","code":"`+wrong+`"}`, nil), 400, "invalid_code")
	refused("no such attempt", a.call("POST", finish, `{"attempt":"att_x","code":"123456"}`, nil), 400, "invalid_code")
	refused("a token", a.call("POST", finish, `{"attempt":"`+free.Attempt+`","token":"x"}`, nil), 400, "invalid_body")
	a.cookie = beaSession
	refused("another account's attempt", a.call("POST", finish, `{"attempt":"`+free.Attempt+`","code":"`+code(freeMail)+`"}`, nil), 400, "invalid_code")
	a.cookie = session
	// An address attempt signs nobody in.
	a.cookie = nil
	refused("an address attempt as a sign-in", a.call("POST", "/api/v1/auth/email/finish", `{"attempt":"`+free.Attempt+`","code":"`+code(freeMail)+`"}`, nil), 400, "invalid_code")
	if a.cookie != nil {
		t.Fatal("an address attempt signed someone in")
	}
	a.cookie = session
	// An address that is another account's: said now, with the code.
	refused("an address that is taken", a.call("POST", finish, `{"attempt":"`+taken.Attempt+`","code":"`+code(takenMail)+`"}`, nil), 409, "address_in_use")
	if read(t, a.call("POST", finish, `{"attempt":"`+taken.Attempt+`","code":"`+code(takenMail)+`"}`, nil), 400, &e); e.Error.Code != "invalid_code" {
		t.Fatalf("the attempt of a refused change a second time: %+v", e)
	}

	// The change: the account comes back with its new address.
	rec := a.call("POST", finish, `{"attempt":"`+free.Attempt+`","code":"`+code(freeMail)+`"}`, nil)
	var account contracts.Account
	read(t, rec, 200, &account)
	if account.ID != me.Account.ID || account.Address != "new@example.org" || account.State != contracts.AccountActive || strings.Contains(rec.Body.String(), "closing_by") {
		t.Fatalf("the account: %s", rec.Body)
	}
	if set(rec, httpapi.SessionCookie) != nil {
		t.Fatal("changing an address set a session")
	}
	var now contracts.Me
	if read(t, a.call("GET", "/api/v1/me", "", nil), 200, &now); now.Account.Address != "new@example.org" || now.Session.ID != me.Session.ID {
		t.Fatalf("me: %+v", now)
	}
	if got := a.record(me.Account.ID); got.Address != "new@example.org" || !got.AddressVerifiedAt.After(got.CreatedAt) {
		t.Fatalf("the record: %+v", got)
	}
	// Both addresses are told.
	sent := a.out.Sent()
	if n := len(sent); sent[n-2].To != "ana@example.org" || sent[n-1].To != "new@example.org" || sent[n-1].Subject != "The contact address of your Permaily account changed" {
		t.Fatalf("the notices: %+v", sent[n-2:])
	}

	// Too many in an hour, and a closing account.
	for i := range 3 {
		ask("more" + string(rune('a'+i)) + "@example.org")
	}
	refused("a sixth request", a.call("POST", start, `{"address":"sixth@example.org"}`, nil), 429, "too_many_requests")
	a.cookie = beaSession
	started, _, mail := ask("bea.new@example.org")
	var closing contracts.Account
	read(t, a.call("POST", "/api/v1/account/delete", "", nil), 200, &closing)
	if closing.ClosingBy != "person" || closing.State != contracts.AccountClosing {
		t.Fatalf("who asked to delete: %+v", closing)
	}
	refused("starting while closing", a.call("POST", start, `{"address":"other@example.org"}`, nil), 409, "account_closing")
	refused("ending while closing", a.call("POST", finish, `{"attempt":"`+started.Attempt+`","code":"`+code(mail)+`"}`, nil), 409, "account_closing")
	// Who asked is told wherever the account is answered.
	var kept contracts.Account
	read(t, a.call("POST", "/api/v1/account/delete/cancel", "", nil), 200, &kept)
	if kept.ClosingBy != "" || kept.State != contracts.AccountActive {
		t.Fatalf("after cancelling: %+v", kept)
	}
	read(t, do(h, "POST", "/v1/admin/accounts/"+bea.Account.ID+"/delete", token, ""), 200, nil)
	rec = a.call("GET", "/api/v1/me", "", nil)
	var seen contracts.Me
	if read(t, rec, 200, &seen); seen.Account.ClosingBy != "operator" || seen.Account.State != contracts.AccountClosing || !strings.Contains(rec.Body.String(), `"closing_by":"operator"`) {
		t.Fatalf("me while the operator's deletion runs: %s", rec.Body)
	}
	// The person cancels what the operator asked.
	rec = a.call("POST", "/api/v1/account/delete/cancel", "", nil)
	var again contracts.Account
	if read(t, rec, 200, &again); again.State != contracts.AccountActive || strings.Contains(rec.Body.String(), "closing_by") {
		t.Fatalf("the person cancels: %s", rec.Body)
	}

	// The storage fails.
	a.h = httpapi.AccountsAPI(&failingAddress{AccountsService: a.svc, err: errors.New("store: down")}, slog.New(slog.NewTextHandler(a.logs, nil)))
	for _, c := range [][2]string{{start, `{"address":"x@example.org"}`}, {finish, `{"attempt":"att_x","code":"1"}`}} {
		if rec := a.call("POST", c[0], c[1], nil); rec.Code != 500 || strings.Contains(rec.Body.String(), "store") {
			t.Fatalf("%s with the storage down: %d %s", c[0], rec.Code, rec.Body)
		}
	}
	for _, secret := range []string{"ana@", "bea@", "new@", "example.org", me.Account.ID, free.Attempt, taken.Attempt} {
		if strings.Contains(a.logs.String(), secret) {
			t.Fatalf("the logs hold %q:\n%s", secret, a.logs)
		}
	}
}

// failingAddress is a service whose changes of address fail as storage that
// is down makes them.
type failingAddress struct {
	httpapi.AccountsService
	err error
}

func (f *failingAddress) StartAddressChange(context.Context, *accounts.Authenticated, contracts.AddressStart) (*contracts.EmailStarted, error) {
	return nil, f.err
}

func (f *failingAddress) FinishAddressChange(context.Context, *accounts.Authenticated, contracts.AddressFinish) (*contracts.Account, error) {
	return nil, f.err
}

// A browser that signs in is given a cookie of its own, which says only that
// it is the same browser the next time.
func TestDeviceCookie(t *testing.T) {
	a := newApp(t)
	a.on("google")
	told := func() int {
		a.svc.Wait()
		n := 0
		for _, m := range a.out.Sent() {
			if m.Subject == "A new device signed in to your Permaily account" {
				n++
			}
		}
		return n
	}
	me := a.signIn("ana@example.org")
	first := a.device
	if first == nil || first.Name != "__Host-permaily-device" || !first.Secure || !first.HttpOnly || first.SameSite != http.SameSiteLaxMode || first.Path != "/" || first.Domain != "" || first.MaxAge != 365*24*60*60 || len(first.Value) != 43 {
		t.Fatalf("the device's cookie: %+v", first)
	}
	if told() != 0 {
		t.Fatal("the sign-up was told as a new device")
	}
	// It is in no answer, and is not the session.
	rec := a.call("GET", "/api/v1/me", "", nil)
	if strings.Contains(rec.Body.String(), first.Value) || strings.Contains(a.cookie.Value, first.Value) || set(rec, httpapi.DeviceCookie) != nil {
		t.Fatal("the device's cookie is told, or set where nobody signed in")
	}
	// The same browser again: the same cookie, kept a year more, and no notice.
	a.cookie = nil
	a.signIn("ana@example.org")
	if a.device.Value != first.Value || a.device.MaxAge != first.MaxAge || told() != 0 {
		t.Fatalf("the same browser a second time: %+v, %d notices", a.device, told())
	}
	// A browser without it is given one, and its person is told.
	a.cookie, a.device = nil, nil
	a.signIn("ana@example.org")
	second := a.device
	if second == nil || second.Value == first.Value || told() != 1 {
		t.Fatalf("another browser: %+v, %d notices", second, told())
	}
	// A cookie that cannot be one is replaced, and told.
	a.cookie, a.device = nil, &http.Cookie{Name: httpapi.DeviceCookie, Value: "not-a-device"}
	a.signIn("ana@example.org")
	if a.device.Value == "not-a-device" || len(a.device.Value) != 43 || told() != 2 {
		t.Fatalf("a cookie that cannot be one: %+v, %d notices", a.device, told())
	}

	// A sign-in that fails sets none.
	a.cookie, a.device = nil, nil
	for _, c := range [][2]string{
		{"/api/v1/auth/email/finish", `{"attempt":"att_x","code":"000000"}`}, {"/api/v1/auth/recovery", `{"address":"ana@example.org","code":"x"}`},
		{"/api/v1/auth/passkey/finish", `{"ceremony":"cer_x","credential":{}}`}, {"/api/v1/auth/provider/confirm/finish", `{"attempt":"att_x","code":"1"}`},
		{"/api/v1/auth/email/start", `{"address":"nobody@example.org"}`}, {"/api/v1/auth/passkey/start", ""},
	} {
		if rec := a.call("POST", c[0], c[1], nil); set(rec, httpapi.DeviceCookie) != nil || a.device != nil {
			t.Fatalf("%s set a device's cookie", c[0])
		}
	}
	a.svc.Wait()
	a.ahead += 2 * time.Hour // an address is sent five messages an hour
	if _, to := a.back("state=x&code=y"); a.device != nil || !strings.Contains(to, "provider_error") {
		t.Fatalf("a trip that failed set a device's cookie: %s", to)
	}

	// With a passkey, a provider and a recovery code it is the same cookie.
	a.device = first
	a.signIn("ana@example.org")
	key := webauthntest.New(webauthntest.ES256)
	passkey := a.makePasskey(key, "laptop")
	google := idp.Identity{Provider: idp.Google, Subject: "g-ana", Address: "ana@example.org", Verified: true}
	state := a.leave("/api/v1/providers/start", `{"provider":"google"}`)
	a.comeBack(state, google)
	before := told()
	a.cookie = nil
	if rec := a.passkeySignIn(key, passkey); rec.Code != 200 || set(rec, httpapi.DeviceCookie).Value != first.Value {
		t.Fatalf("a passkey: %d", rec.Code)
	}
	a.cookie = nil
	state = a.leave("/api/v1/auth/provider/start", `{"provider":"google"}`)
	if rec, to := a.comeBack(state, google); to != "/" || set(rec, httpapi.DeviceCookie).Value != first.Value || set(rec, httpapi.SessionCookie) == nil {
		t.Fatalf("a provider: %s", to)
	}
	if told() != before {
		t.Fatal("a known browser was told as new by another way of signing in")
	}
	// A provider's trip from a browser nobody knows: its cookie is set with
	// the redirect, and its person is told.
	a.cookie, a.device = nil, nil
	state = a.leave("/api/v1/auth/provider/start", `{"provider":"google"}`)
	if rec, to := a.comeBack(state, google); to != "/" || set(rec, httpapi.DeviceCookie) == nil || told() != before+1 {
		t.Fatalf("a provider from a new browser: %s, %d notices", to, told())
	}
	var only contracts.PasskeyOnlyState
	read(t, a.call("POST", "/api/v1/account/passkey-only", `{"on":true}`, nil), 200, &only)
	a.cookie, a.device = nil, nil
	rec = a.call("POST", "/api/v1/auth/recovery", `{"address":"ana@example.org","code":"`+only.RecoveryCodes[0]+`"}`, nil)
	if rec.Code != 200 || set(rec, httpapi.DeviceCookie) == nil || told() != before+2 {
		t.Fatalf("a recovery code from a new browser: %d, %d notices", rec.Code, told())
	}
	// It is never logged, and never stored as it is.
	for _, c := range []*http.Cookie{first, second, a.device} {
		if strings.Contains(a.logs.String(), c.Value) {
			t.Fatal("a device's cookie is logged")
		}
		for _, e := range a.mem.AllEntries() {
			if strings.Contains(e.Key+string(e.Value), c.Value) {
				t.Fatal("a device's cookie is stored")
			}
		}
	}
	_ = me
}

// The operator lists and revokes the invitations that were not used yet.
func TestInvitationsAdminAPI(t *testing.T) {
	a := newApp(t)
	h := a.admin()
	var made contracts.Invitations
	rec := do(h, "POST", "/v1/admin/invitations", token, `{"count":3}`)
	if read(t, rec, 200, &made); len(made.Invitations) != 3 || len(made.IDs) != 3 || !strings.HasPrefix(made.IDs[0], "inv_") {
		t.Fatalf("three invitations: %s", rec.Body)
	}
	var list contracts.InvitationList
	rec = do(h, "GET", "/v1/admin/invitations", token, "")
	if read(t, rec, 200, &list); len(list.Invitations) != 3 || list.Truncated || strings.Contains(rec.Body.String(), "truncated") {
		t.Fatalf("the list: %s", rec.Body)
	}
	ids := map[string]bool{}
	for _, inv := range list.Invitations {
		ids[inv.ID] = true
		if inv.CreatedAt.IsZero() || !inv.ExpiresAt.Equal(made.ExpiresAt) || inv.ExpiresAt.Sub(inv.CreatedAt) != 30*24*time.Hour {
			t.Fatalf("an invitation of the list: %+v", inv)
		}
	}
	// No code is in the list, whole or without its dashes.
	for i, code := range made.Invitations {
		if !ids[made.IDs[i]] || strings.Contains(rec.Body.String(), code) || strings.Contains(strings.ToLower(rec.Body.String()), strings.ToLower(strings.ReplaceAll(code, "-", ""))) {
			t.Fatalf("the list: %s", rec.Body)
		}
	}
	// Without the token: nothing.
	var e httpapi.APIError
	for _, c := range [][2]string{{"GET", "/v1/admin/invitations"}, {"DELETE", "/v1/admin/invitations/" + made.IDs[0]}} {
		for _, auth := range []string{"", "not the token"} {
			rec := do(h, c[0], c[1], auth, "")
			if read(t, rec, 401, &e); strings.Contains(rec.Body.String(), "inv_") {
				t.Errorf("%s %s with %q: %s", c[0], c[1], auth, rec.Body)
			}
		}
	}
	// Revoking one: it is gone from the list, and signs nobody up.
	if rec := do(h, "DELETE", "/v1/admin/invitations/"+made.IDs[0], token, ""); rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("revoking: %d %s", rec.Code, rec.Body)
	}
	if read(t, do(h, "GET", "/v1/admin/invitations", token, ""), 200, &list); len(list.Invitations) != 2 {
		t.Fatalf("after revoking: %+v", list)
	}
	if read(t, a.call("POST", "/api/v1/auth/email/start", `{"address":"ana@example.org","invitation":"`+made.Invitations[0]+`"}`, nil), 400, &e); e.Error.Code != "invalid_invitation" {
		t.Fatalf("a revoked invitation: %+v", e)
	}
	for _, id := range []string{made.IDs[0], "inv_" + strings.Repeat("a", 26), "x", made.Invitations[1]} {
		if read(t, do(h, "DELETE", "/v1/admin/invitations/"+id, token, ""), 404, &e); e.Error.Code != "not_found" {
			t.Errorf("revoking %s: %+v", id, e)
		}
	}
	for _, c := range [][3]string{
		{"PUT", "/v1/admin/invitations", "GET, POST"}, {"DELETE", "/v1/admin/invitations", "GET, POST"},
		{"GET", "/v1/admin/invitations/" + made.IDs[1], "DELETE"}, {"POST", "/v1/admin/invitations/" + made.IDs[1], "DELETE"},
	} {
		rec := do(h, c[0], c[1], token, "")
		if read(t, rec, 405, &e); rec.Header().Get("Allow") != c[2] {
			t.Errorf("%s %s: %d, Allow %q", c[0], c[1], rec.Code, rec.Header().Get("Allow"))
		}
	}
	if read(t, do(h, "GET", "/v1/admin/invitations/"+made.IDs[1]+"/code", token, ""), 404, &e); e.Error.Code != "not_found" {
		t.Fatalf("the code of an invitation: %+v", e)
	}
	// One that is used is not listed.
	a.signInWith("ana@example.org", made.Invitations[1])
	if read(t, do(h, "GET", "/v1/admin/invitations", token, ""), 200, &list); len(list.Invitations) != 1 || list.Invitations[0].ID != made.IDs[2] {
		t.Fatalf("after one was used: %+v", list)
	}
	down := httpapi.Invitations(broken{err: errors.New("store: down")}, slog.New(slog.DiscardHandler))
	for _, c := range [][2]string{{"GET", "/v1/admin/invitations"}, {"DELETE", "/v1/admin/invitations/" + made.IDs[2]}} {
		if rec := do(down, c[0], c[1], "", ""); rec.Code != 500 || strings.Contains(rec.Body.String(), "store") {
			t.Fatalf("%s with the storage down: %d %s", c[0], rec.Code, rec.Body)
		}
	}
	for _, secret := range append(append([]string{"inv_"}, made.IDs...), made.Invitations...) {
		if strings.Contains(a.logs.String(), secret) {
			t.Fatalf("the logs hold %q:\n%s", secret, a.logs)
		}
	}
}

// signInWith signs the browser up with an invitation.
func (a *app) signInWith(address, invitation string) {
	a.t.Helper()
	var started contracts.EmailStarted
	read(a.t, a.call("POST", "/api/v1/auth/email/start", `{"address":"`+address+`","invitation":"`+invitation+`","terms":"`+domain.TermsVersion+`"}`, nil), 200, &started)
	read(a.t, a.call("POST", "/api/v1/auth/email/finish", `{"attempt":"`+started.Attempt+`","code":"`+sentCode.FindStringSubmatch(a.mailOf(started.Attempt).Text)[1]+`"}`, nil), 200, nil)
}

// What the operator did through the enclave is on /status: how many of each
// kind, since when, and never to which account.
func TestStatusSaysWhatTheOperatorDid(t *testing.T) {
	a := newApp(t)
	h := a.admin()
	me := a.signIn("ana@example.org")
	status := httpapi.Status(httpapi.StatusSources{Admin: func() *attestation.AdminStatus {
		since, actions := a.svc.OperatorActions()
		return &attestation.AdminStatus{Since: since, Actions: actions}
	}})
	get := func() (attestation.Status, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		status.ServeHTTP(rec, httptest.NewRequest("GET", "/status", nil))
		var st attestation.Status
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &st) != nil || st.Admin == nil {
			t.Fatalf("status: %d %s", rec.Code, rec.Body)
		}
		return st, rec.Body.String()
	}
	st, _ := get()
	if st.Admin.Since.IsZero() || len(st.Admin.Actions) != 11 || st.Admin.Actions["invitations_made"] != 1 || st.Admin.Actions["block"] != 0 {
		t.Fatalf("at the start: %+v", st.Admin)
	}
	one := "/v1/admin/accounts/" + me.Account.ID
	for _, action := range []string{"/block", "/block", "/unblock", "/delete", "/delete/cancel", "/sessions/close"} {
		read(t, do(h, "POST", one+action, token, ""), 200, nil)
	}
	var made contracts.Invitations
	read(t, do(h, "POST", "/v1/admin/invitations", token, `{"count":4}`), 200, &made)
	do(h, "DELETE", "/v1/admin/invitations/"+made.IDs[0], token, "")
	read(t, do(h, "PUT", "/v1/admin/providers/google", token, `{"client_id":"id","client_secret":"secret"}`), 200, nil)
	do(h, "DELETE", "/v1/admin/providers/google", token, "")
	// What was refused is not counted.
	do(h, "POST", "/v1/admin/accounts/acc_"+strings.Repeat("a", 26)+"/block", token, "")
	do(h, "POST", one+"/block", "not the token", "")

	st, body := get()
	want := map[string]int{
		"block": 2, "unblock": 1, "delete": 1, "delete_cancel": 1, "sessions_close": 1,
		"invitations_made": 5, "invitation_revoked": 1, "provider_app_set": 1, "provider_app_removed": 1,
		"admin_made": 0, "admin_removed": 0,
	}
	if !reflect.DeepEqual(st.Admin.Actions, want) {
		t.Fatalf("what the operator did: %+v, want %+v", st.Admin.Actions, want)
	}
	// Kinds and counts only: no account, no address, no identifier.
	for _, secret := range []string{"ana@", "example.org", me.Account.ID, "acc_", "inv_", made.IDs[0], me.Session.ID} {
		if strings.Contains(body, secret) {
			t.Fatalf("the status holds %q: %s", secret, body)
		}
	}
	// The kinds are the ones the verifier knows by name.
	if !slices.Equal(accounts.OperatorActionKinds, attestation.AdminActions) {
		t.Fatalf("the service counts %v, the verifier knows %v", accounts.OperatorActionKinds, attestation.AdminActions)
	}
	// An image without accounts has no section, not an empty one.
	for name, src := range map[string]httpapi.StatusSources{"no source": {}, "nothing to say": {Admin: func() *attestation.AdminStatus { return nil }}} {
		rec := httptest.NewRecorder()
		httpapi.Status(src).ServeHTTP(rec, httptest.NewRequest("GET", "/status", nil))
		if rec.Code != 200 || strings.Contains(rec.Body.String(), `"admin"`) {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}
