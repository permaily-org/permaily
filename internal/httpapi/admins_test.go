package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
)

// superAdmin is the address compiled into the app's image.
const superAdmin = "boss@example.org"

// later moves the app's clock past the proof of the last sign-in.
func (a *app) later() { a.ahead += domain.ConfirmationLifetime + time.Second }

// confirmByMail confirms the browser's session with the code sent to the
// account's address.
func (a *app) confirmByMail() contracts.Me {
	a.t.Helper()
	address := a.me().Account.Address
	before := len(a.out.Sent())
	var started contracts.EmailStarted
	read(a.t, a.call("POST", "/api/v1/auth/confirm/email/start", "", nil), 200, &started)
	mail := a.mailTo(before, address, "Confirm it is you on Permaily")
	if mail == nil {
		a.t.Fatal("no message to confirm")
	}
	code := sentCode.FindStringSubmatch(mail.Text)[1]
	var me contracts.Me
	read(a.t, a.call("POST", "/api/v1/auth/confirm/email/finish", `{"attempt":"`+started.Attempt+`","code":"`+code+`"}`, nil), 200, &me)
	return me
}

// confirmWith confirms the browser's session with a passkey, and returns
// the answer.
func (a *app) confirmWith(auth *webauthntest.Authenticator, id string) *http.Response {
	a.t.Helper()
	var started struct {
		Ceremony string `json:"ceremony"`
		Options  struct {
			Challenge        string `json:"challenge"`
			RPID             string `json:"rpId"`
			AllowCredentials []struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"allowCredentials"`
		} `json:"options"`
	}
	read(a.t, a.call("POST", "/api/v1/auth/confirm/passkey/start", "", nil), 200, &started)
	o := started.Options
	if len(o.AllowCredentials) == 0 || o.AllowCredentials[0].Type != "public-key" {
		a.t.Fatalf("the options name no passkey: %+v", o)
	}
	clientData, authData, signature, handle := auth.Get(o.RPID, appOrigin, unb64(a.t, o.Challenge), unb64(a.t, id))
	body := fmt.Sprintf(`{"ceremony":%q,"credential":{"id":%q,"rawId":%q,"type":"public-key",
		"response":{"clientDataJSON":%q,"authenticatorData":%q,"signature":%q,"userHandle":%q},"clientExtensionResults":{}}}`,
		started.Ceremony, id, id, b64(clientData), b64(authData), b64(signature), b64(handle))
	return a.call("POST", "/api/v1/auth/confirm/passkey/finish", body, nil).Result()
}

// browserJar is a person's cookies, to be put back in the app's jar.
type browserJar struct{ cookie, device *http.Cookie }

func (a *app) jar() browserJar  { return browserJar{a.cookie, a.device} }
func (a *app) use(b browserJar) { a.cookie, a.device = b.cookie, b.device }
func (a *app) me() contracts.Me {
	var me contracts.Me
	read(a.t, a.call("GET", "/api/v1/me", "", nil), 200, &me)
	return me
}

// refusal reads the error of an answer that must have that status.
func refusal(t *testing.T, rec *httptest.ResponseRecorder, status int) httpapi.APIError {
	t.Helper()
	var e httpapi.APIError
	read(t, rec, status, &e)
	return e
}

// The routes that decide who gets into an account answer 403
// confirmation_needed and do nothing once the sign-in's proof is ten minutes
// old; confirming by mail or with a passkey lets them through again.
func TestConfirmAPI(t *testing.T) {
	a := newApp(t)
	a.on("google")
	me := a.signIn("ana@example.org")
	if me.Session.ConfirmedUntil.IsZero() || !strings.Contains(a.call("GET", "/api/v1/me", "", nil).Body.String(), `"confirmed_until":"`) {
		t.Fatalf("a sign-in is no proof: %+v", me.Session)
	}
	// The account says whether it signs in with passkeys only, also when not.
	if body := a.call("GET", "/api/v1/me", "", nil).Body.String(); !strings.Contains(body, `"passkey_only":false`) {
		t.Fatalf("me does not say passkey only: %s", body)
	}
	var ways contracts.ConfirmWays
	read(t, a.call("GET", "/api/v1/auth/confirm", "", nil), 200, &ways)
	if !slices.Equal(ways.Ways, []string{"email"}) || ways.ConfirmedUntil.IsZero() {
		t.Fatalf("ways: %+v", ways)
	}
	auth := webauthntest.New(webauthntest.ES256)
	passkey := a.makePasskey(auth, "laptop")
	state := a.leave("/api/v1/providers/start", `{"provider":"google"}`)
	a.comeBack(state, idp.Identity{Provider: idp.Google, Subject: "g-ana", Address: "ana@example.org", Verified: true})

	a.later()
	if body := a.call("GET", "/api/v1/me", "", nil).Body.String(); strings.Contains(body, "confirmed_until") {
		t.Fatalf("still fresh: %s", body)
	}
	ways = contracts.ConfirmWays{}
	read(t, a.call("GET", "/api/v1/auth/confirm", "", nil), 200, &ways)
	if !slices.Equal(ways.Ways, []string{"passkey", "email"}) || !ways.ConfirmedUntil.IsZero() {
		t.Fatalf("ways after: %+v", ways)
	}
	before := a.record(me.Account.ID)
	var e httpapi.APIError
	for _, r := range []struct{ method, path, body string }{
		{"POST", "/api/v1/passkeys/start", ""},
		{"DELETE", "/api/v1/passkeys/" + passkey, ""},
		{"POST", "/api/v1/account/passkey-only", `{"on":true}`},
		{"POST", "/api/v1/account/recovery-codes", ""},
		{"POST", "/api/v1/providers/start", `{"provider":"google"}`},
		{"DELETE", "/api/v1/providers/google", ""},
		{"POST", "/api/v1/account/address/start", `{"address":"new@example.org"}`},
		{"POST", "/api/v1/account/delete", ""},
	} {
		rec := a.call(r.method, r.path, r.body, nil)
		if read(t, rec, 403, &e); e.Error.Code != "confirmation_needed" || e.Error.Message != "confirm it is you to continue" || set(rec, httpapi.ProviderCookie) != nil {
			t.Errorf("%s %s: %s", r.method, r.path, rec.Body)
		}
	}
	if after := a.record(me.Account.ID); !reflect.DeepEqual(after, before) {
		t.Fatalf("the account changed: %+v", after)
	}
	// What only protects needs no proof.
	read(t, a.call("PATCH", "/api/v1/account", `{"name":"Ana"}`, nil), 200, nil)

	// By mail.
	confirmed := a.confirmByMail()
	if !confirmed.Session.ConfirmedUntil.After(time.Now().Add(a.ahead+9*time.Minute)) || !confirmed.Session.Current {
		t.Fatalf("confirmed: %+v", confirmed.Session)
	}
	read(t, a.call("POST", "/api/v1/passkeys/start", "", nil), 200, nil)
	// With a passkey.
	a.later()
	if res := a.confirmWith(auth, passkey); res.StatusCode != 200 {
		t.Fatalf("with a passkey: %d", res.StatusCode)
	}
	var only contracts.PasskeyOnlyState
	read(t, a.call("POST", "/api/v1/account/passkey-only", `{"on":true}`, nil), 200, &only)
	if e := refusal(t, a.call("POST", "/api/v1/auth/confirm/email/start", "", nil), 409); e.Error.Code != "passkey_required" || e.Error.Message != "this account confirms with a passkey" {
		t.Fatalf("by mail with passkey only: %s", e)
	}

	// Refusals: a wrong code, a wrong passkey, a body that is not one, and
	// nobody signed in.
	b := newApp(t)
	b.signIn("bea@example.org")
	if e := refusal(t, b.call("POST", "/api/v1/auth/confirm/passkey/start", "", nil), 409); e.Error.Code != "passkey_needed" {
		t.Fatalf("a passkey to confirm with, without one: %s", e)
	}
	var started contracts.EmailStarted
	read(t, b.call("POST", "/api/v1/auth/confirm/email/start", "", nil), 200, &started)
	if e := refusal(t, b.call("POST", "/api/v1/auth/confirm/email/finish", `{"attempt":"`+started.Attempt+`","code":"000000x"}`, nil), 400); e.Error.Code != "invalid_code" {
		t.Fatalf("a wrong code: %s", e)
	}
	if e := refusal(t, b.call("POST", "/api/v1/auth/confirm/passkey/finish", `{"ceremony":"cer_x","credential":{}}`, nil), 400); e.Error.Code != "invalid_passkey" {
		t.Fatalf("a wrong passkey: %s", e)
	}
	for _, path := range []string{"/api/v1/auth/confirm/email/finish", "/api/v1/auth/confirm/passkey/finish"} {
		if e := refusal(t, b.call("POST", path, `{"attempt":1}`, nil), 400); e.Error.Code != "invalid_body" {
			t.Fatalf("%s with a body that is not one: %s", path, e)
		}
	}
	b.cookie = nil
	for _, r := range [][2]string{{"GET", "/api/v1/auth/confirm"}, {"POST", "/api/v1/auth/confirm/passkey/start"}, {"POST", "/api/v1/auth/confirm/passkey/finish"},
		{"POST", "/api/v1/auth/confirm/email/start"}, {"POST", "/api/v1/auth/confirm/email/finish"}} {
		if e := refusal(t, b.call(r[0], r[1], "", nil), 401); e.Error.Code != "unauthorized" {
			t.Errorf("%s without a session: %s", r[1], e)
		}
	}
}

// adminPaths are every route of the admin panel, with what each takes.
func adminPaths(id string) []struct{ method, path, body string } {
	one := "/api/v1/admin/accounts/" + id
	return []struct{ method, path, body string }{
		{"GET", "/api/v1/admin/accounts", ""}, {"GET", "/api/v1/admin/accounts?address=ana%40example.org", ""}, {"GET", one, ""},
		{"POST", one + "/block", ""}, {"POST", one + "/unblock", ""}, {"POST", one + "/delete", ""}, {"POST", one + "/delete/cancel", ""},
		{"POST", one + "/sessions/close", ""}, {"GET", "/api/v1/admin/invitations", ""}, {"POST", "/api/v1/admin/invitations", `{"count":1}`},
		{"DELETE", "/api/v1/admin/invitations/inv_" + strings.Repeat("a", 26), ""}, {"GET", "/api/v1/admin/providers", ""},
		{"PUT", "/api/v1/admin/providers/google", `{"client_id":"a","client_secret":"b"}`}, {"DELETE", "/api/v1/admin/providers/google", ""},
		{"GET", "/api/v1/admin/admins", ""}, {"PUT", "/api/v1/admin/admins/" + id, ""}, {"DELETE", "/api/v1/admin/admins/" + id, ""},
		{"GET", "/api/v1/admin/log", ""}, {"GET", "/api/v1/admin/stats", ""},
		// Mailboxes (ADR 0026).
		{"GET", one + "/mailboxes", ""}, {"GET", "/api/v1/admin/mailboxes?address=x%40inbox.permaily.test", ""},
		{"POST", "/api/v1/admin/mailboxes/mbx_" + strings.Repeat("a", 26) + "/pause", ""},
		{"POST", "/api/v1/admin/mailboxes/mbx_" + strings.Repeat("a", 26) + "/resume", ""},
		{"GET", "/api/v1/admin/nothing", ""}, {"PATCH", "/api/v1/admin/stats", ""}, {"POST", "/api/v1/admin/accounts", ""},
	}
}

// Someone who is not an admin learns nothing from the admin routes: every
// one of them, and every path under them, answers the same 404, signed in or
// with a session of an admin's account that is not "passkey only" yet; and
// signed out, the same 401 as every route.
func TestSomeoneWhoIsNotAnAdminLearnsNothingFromTheAdminRoutes(t *testing.T) {
	a := newApp(t)
	ana := a.signIn("ana@example.org")
	before := a.record(ana.Account.ID)
	want := ""
	for _, r := range adminPaths(ana.Account.ID) {
		rec := a.call(r.method, r.path, r.body, nil)
		if rec.Code != 404 || (want != "" && rec.Body.String() != want) || strings.Contains(rec.Body.String(), "example.org") {
			t.Errorf("%s %s: %d %s", r.method, r.path, rec.Code, rec.Body)
		}
		want = rec.Body.String()
	}
	if want != `{"error":{"code":"not_found","message":"not found"}}`+"\n" && want != `{"error":{"code":"not_found","message":"not found"}}` {
		t.Fatalf("the answer: %q", want)
	}
	if after := a.record(ana.Account.ID); !reflect.DeepEqual(after, before) {
		t.Fatalf("ana's account changed: %+v", after)
	}
	a.cookie = nil
	for _, r := range adminPaths(ana.Account.ID) {
		if rec := a.call(r.method, r.path, r.body, nil); rec.Code != 401 {
			t.Errorf("%s %s signed out: %d %s", r.method, r.path, rec.Code, rec.Body)
		}
	}
	// The super admin, before "passkey only": told what is missing, on every
	// route.
	a.signIn(superAdmin)
	if me := a.me(); me.Account.Admin != "super" {
		t.Fatalf("the super admin's me: %+v", me.Account)
	}
	for _, r := range adminPaths(ana.Account.ID)[:23] {
		if e := refusal(t, a.call(r.method, r.path, r.body, nil), 403); e.Error.Code != "passkey_only_needed" || e.Error.Message != "turn on passkey only to use the admin panel" {
			t.Errorf("%s %s: %s", r.method, r.path, e)
		}
	}
}

// The admin panel, end to end: the super admin makes an admin, the admin
// blocks an account, the log and the counts say so; what is the super
// admin's alone is refused to the admin, and every change needs a fresh
// proof.
func TestAdminPanelAPI(t *testing.T) {
	a := newApp(t)
	carl := a.signIn("carl@example.org")
	carlJar := a.jar()
	ana := a.signIn("ana@example.org")
	anaAuth := webauthntest.New(webauthntest.ES256)
	anaKey := a.makePasskey(anaAuth, "ana's")
	read(t, a.call("POST", "/api/v1/account/passkey-only", `{"on":true}`, nil), 200, nil)
	anaJar := a.jar()
	boss := a.signIn(superAdmin)
	bossAuth := webauthntest.New(webauthntest.ES256)
	bossKey := a.makePasskey(bossAuth, "boss's")
	read(t, a.call("POST", "/api/v1/account/passkey-only", `{"on":true}`, nil), 200, nil)
	bossJar := a.jar()

	var admins contracts.AdminList
	read(t, a.call("GET", "/api/v1/admin/admins", "", nil), 200, &admins)
	if len(admins.Admins) != 1 || admins.Admins[0].ID != boss.Account.ID || !admins.Admins[0].Super || strings.Contains(a.call("GET", "/api/v1/admin/admins", "", nil).Body.String(), "since") {
		t.Fatalf("the admins: %+v", admins)
	}
	read(t, a.call("PUT", "/api/v1/admin/admins/"+ana.Account.ID, "", nil), 200, &admins)
	if len(admins.Admins) != 2 || admins.Admins[1].ID != ana.Account.ID || admins.Admins[1].Super || admins.Admins[1].Since.IsZero() {
		t.Fatalf("after making ana an admin: %+v", admins)
	}
	if e := refusal(t, a.call("PUT", "/api/v1/admin/admins/acc_"+strings.Repeat("a", 26), "", nil), 404); e.Error.Code != "not_found" {
		t.Fatalf("an account that is not there: %s", e)
	}
	if e := refusal(t, a.call("DELETE", "/api/v1/admin/admins/"+boss.Account.ID, "", nil), 403); e.Error.Code != "super_admin_only" {
		t.Fatalf("removing the super admin: %s", e)
	}
	var rec contracts.AccountRecord
	read(t, a.call("GET", "/api/v1/admin/accounts/"+ana.Account.ID, "", nil), 200, &rec)
	if rec.Admin != "admin" {
		t.Fatalf("ana's record: %+v", rec)
	}

	// Ana, an admin now, through her own browser.
	a.use(anaJar)
	if me := a.me(); me.Account.Admin != "admin" {
		t.Fatalf("ana's me: %+v", me.Account)
	}
	a.later()
	if e := refusal(t, a.call("POST", "/api/v1/admin/accounts/"+carl.Account.ID+"/block", "", nil), 403); e.Error.Code != "confirmation_needed" {
		t.Fatalf("blocking without a fresh proof: %s", e)
	}
	if res := a.confirmWith(anaAuth, anaKey); res.StatusCode != 200 {
		t.Fatalf("ana confirms: %d", res.StatusCode)
	}
	read(t, a.call("POST", "/api/v1/admin/accounts/"+carl.Account.ID+"/block", "", nil), 200, &rec)
	if rec.BlockedAt.IsZero() {
		t.Fatalf("carl: %+v", rec)
	}
	for _, r := range []struct{ method, path, body string }{
		{"PUT", "/api/v1/admin/providers/google", `{"client_id":"a","client_secret":"b"}`}, {"DELETE", "/api/v1/admin/providers/google", ""},
		{"PUT", "/api/v1/admin/admins/" + carl.Account.ID, ""}, {"DELETE", "/api/v1/admin/admins/" + ana.Account.ID, ""},
		{"POST", "/api/v1/admin/accounts/" + boss.Account.ID + "/sessions/close", ""},
	} {
		if e := refusal(t, a.call(r.method, r.path, r.body, nil), 403); e.Error.Code != "super_admin_only" {
			t.Errorf("ana: %s %s: %s", r.method, r.path, e)
		}
	}
	var made contracts.Invitations
	read(t, a.call("POST", "/api/v1/admin/invitations", `{"count":2}`, nil), 200, &made)
	if rec := a.call("DELETE", "/api/v1/admin/invitations/"+made.IDs[0], "", nil); rec.Code != 204 {
		t.Fatalf("revoking: %d %s", rec.Code, rec.Body)
	}
	var invitations contracts.InvitationList
	read(t, a.call("GET", "/api/v1/admin/invitations", "", nil), 200, &invitations)
	var list contracts.AccountList
	read(t, a.call("GET", "/api/v1/admin/accounts?limit=2", "", nil), 200, &list)
	if len(list.Accounts) != 2 || list.Next == "" {
		t.Fatalf("a page of accounts: %+v", list)
	}
	read(t, a.call("GET", "/api/v1/admin/accounts?address="+superAdmin, "", nil), 200, &list)
	if len(list.Accounts) != 1 || list.Accounts[0].Admin != "super" {
		t.Fatalf("finding the super admin: %+v", list)
	}
	for _, q := range []string{"limit=x", "limit=0"} {
		for _, path := range []string{"/api/v1/admin/accounts", "/api/v1/admin/log"} {
			if e := refusal(t, a.call("GET", path+"?"+q, "", nil), 400); e.Error.Code != "invalid_limit" {
				t.Errorf("%s?%s: %s", path, q, e)
			}
		}
	}
	if e := refusal(t, a.call("GET", "/api/v1/admin/log?after=x", "", nil), 400); e.Error.Code != "invalid_cursor" {
		t.Fatalf("a cursor that is not one: %s", e)
	}
	var log contracts.AdminLog
	body := a.call("GET", "/api/v1/admin/log", "", nil).Body.String()
	if json.Unmarshal([]byte(body), &log) != nil || len(log.Entries) != 5 {
		t.Fatalf("the log: %s", body)
	}
	kinds := []string{}
	for _, e := range log.Entries {
		kinds = append(kinds, e.Kind)
	}
	if !slices.Equal(kinds, []string{"invitation_revoked", "invitations_made", "invitations_made", "block", "admin_made"}) ||
		log.Entries[3].Account.Address != "carl@example.org" || log.Entries[3].Admin.Address != "ana@example.org" || log.Entries[0].Invitation != made.IDs[0] {
		t.Fatalf("the log: %s", body)
	}
	var stats contracts.Stats
	read(t, a.call("GET", "/api/v1/admin/stats", "", nil), 200, &stats)
	if stats.Accounts.Total != 3 || stats.Accounts.Blocked != 1 || stats.Invitations.Unused != 1 || !stats.Complete {
		t.Fatalf("the counts: %+v", stats)
	}
	var apps contracts.ProviderApps
	read(t, a.call("GET", "/api/v1/admin/providers", "", nil), 200, &apps)
	// A body that is not JSON, and a method a route does not take.
	if e := refusal(t, a.call("POST", "/api/v1/admin/invitations", `{"count":"x"}`, nil), 400); e.Error.Code != "invalid_body" {
		t.Fatalf("a body that is not one: %s", e)
	}
	for _, r := range [][2]string{{"PATCH", "/api/v1/admin/admins"}, {"POST", "/api/v1/admin/admins/x"}, {"POST", "/api/v1/admin/log"}, {"POST", "/api/v1/admin/stats"}} {
		if rec := a.call(r[0], r[1], "", nil); rec.Code != 405 {
			t.Errorf("%s %s: %d", r[0], r[1], rec.Code)
		}
	}

	// A deletion carl asked for is carl's to cancel.
	read(t, a.call("POST", "/api/v1/admin/accounts/"+carl.Account.ID+"/unblock", "", nil), 200, nil)
	a.use(carlJar)
	a.signIn("carl@example.org")
	read(t, a.call("POST", "/api/v1/account/delete", "", nil), 200, nil)
	a.use(anaJar)
	if e := refusal(t, a.call("POST", "/api/v1/admin/accounts/"+carl.Account.ID+"/delete/cancel", "", nil), 409); e.Error.Code != "not_cancellable" {
		t.Fatalf("ana cancels what carl asked: %+v", e)
	}
	// The super admin, back: removes ana, who is no admin from then on.
	a.use(bossJar)
	if res := a.confirmWith(bossAuth, bossKey); res.StatusCode != 200 {
		t.Fatalf("the super admin confirms: %d", res.StatusCode)
	}
	read(t, a.call("DELETE", "/api/v1/admin/admins/"+ana.Account.ID, "", nil), 200, &admins)
	if len(admins.Admins) != 1 {
		t.Fatalf("after removing ana: %+v", admins)
	}
	read(t, a.call("PUT", "/api/v1/admin/providers/google", `{"client_id":"a","client_secret":"b"}`, nil), 200, nil)
	if rec := a.call("DELETE", "/api/v1/admin/providers/google", "", nil); rec.Code != 204 {
		t.Fatalf("removing a provider: %d", rec.Code)
	}
	a.use(anaJar)
	if rec := a.call("GET", "/api/v1/admin/stats", "", nil); rec.Code != 404 {
		t.Fatalf("ana after: %d %s", rec.Code, rec.Body)
	}
	for _, secret := range []string{"ana@", "carl@", "example.org", ana.Account.ID, carl.Account.ID, made.IDs[0]} {
		if strings.Contains(a.logs.String(), secret) {
			t.Fatalf("the logs hold %q:\n%s", secret, a.logs)
		}
	}
}

// What the admin panel's routes can ask of the service is what an admin can
// do (accounts.Admin), and none of it changes an address or a way in: each
// method takes which account, an address to look up, a place in a list, a
// count, a provider or a provider's application, and nothing else.
func TestAnAdminsRoutesCannotChangeAnAddressOrAWayIn(t *testing.T) {
	ops := reflect.TypeOf((*httpapi.AdminOperator)(nil)).Elem()
	admin := reflect.TypeOf(&accounts.Admin{})
	var names, has []string
	for i := range ops.NumMethod() {
		names = append(names, ops.Method(i).Name)
	}
	for i := range admin.NumMethod() {
		has = append(has, admin.Method(i).Name)
	}
	want := []string{
		"AccountRecord", "AccountRecordByAddress", "AccountRecords", "AdminLog", "Admins", "BlockAccount", "CloseAccountSessions",
		"CreateInvitations", "DeleteAccount", "Invitations", "KeepAccount", "MakeAdmin", "ProviderApps", "RemoveAdmin",
		"RemoveProviderApp", "RevokeInvitation", "SetProviderApp", "Stats", "UnblockAccount",
	}
	// An admin is also what the mailbox routes are given (MailboxOperator,
	// TestTheMailboxRoutesCannotReadMailOrDisconnect): its four, and no more.
	withMailboxes := slices.Sorted(slices.Values(append(slices.Clone(want), "MailboxRecordByAddress", "MailboxRecords", "PauseMailbox", "ResumeMailbox")))
	if !slices.Equal(names, want) || !slices.Equal(has, withMailboxes) {
		t.Fatalf("what the admin panel can ask:\n%v\nwhat an admin can do:\n%v\nwant\n%v", names, has, want)
	}
	for i := range ops.NumMethod() {
		m := ops.Method(i).Type
		for j := 1; j < m.NumIn(); j++ {
			if k := m.In(j); k.Kind() != reflect.String && k.Kind() != reflect.Int && k != reflect.TypeOf(contracts.ProviderAppSet{}) {
				t.Errorf("%s takes a %s", ops.Method(i).Name, k)
			}
		}
	}

	// And what the routes take changes none of it, whatever comes along.
	a := newApp(t)
	ana := a.signIn("ana@example.org")
	key := webauthntest.New(webauthntest.ES256)
	a.makePasskey(key, "laptop")
	anaJar := a.jar()
	a.signIn(superAdmin)
	a.makePasskey(webauthntest.New(webauthntest.ES256), "boss's")
	read(t, a.call("POST", "/api/v1/account/passkey-only", `{"on":true}`, nil), 200, nil)
	before := a.record(ana.Account.ID)
	one := "/api/v1/admin/accounts/" + ana.Account.ID
	for _, path := range []string{one + "/address", one + "/passkeys", one + "/passkey-only", one + "/recovery-codes", one + "/providers/google",
		one + "/sign-in", one + "/session", "/api/v1/admin/me", "/api/v1/admin/account/address/start", "/api/v1/admin/admins/" + ana.Account.ID + "/passkeys"} {
		for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
			if rec := a.call(method, path, `{"address":"eve@example.org","on":false,"passkey_only":false}`, nil); rec.Code != 404 && rec.Code != 405 {
				t.Errorf("%s %s: %d %s", method, path, rec.Code, rec.Body)
			}
		}
	}
	for _, action := range []string{"/block", "/unblock", "/delete", "/delete/cancel", "/sessions/close"} {
		a.call("POST", one+action+"?address=eve%40example.org", `{"address":"eve@example.org","passkeys":[]}`, nil)
	}
	after := a.record(ana.Account.ID)
	if after.Address != before.Address || after.Passkeys != before.Passkeys || after.PasskeyOnly != before.PasskeyOnly || !after.AddressVerifiedAt.Equal(before.AddressVerifiedAt) {
		t.Fatalf("ana's ways in changed: %+v, was %+v", after, before)
	}
	a.use(anaJar)
	if rec := a.call("GET", "/api/v1/me", "", nil); rec.Code != 401 {
		t.Fatalf("ana's session after her devices were signed out: %d", rec.Code)
	}
}
