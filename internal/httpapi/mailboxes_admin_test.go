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

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/services/mailboxes/mailboxestest"
	"github.com/permaily-org/permaily/internal/webauthn/webauthntest"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// The mailboxes of the operator's tool and of the admin panel (ADR 0026,
// "What the operator and admins see and do"), cases O2 to O5 of its fourth
// pull request, against mailboxestest.Fake under a real accounts service.

// Both wrappers of accounts are what the routes are given.
var (
	_ httpapi.MailboxOperator = (*accounts.Service)(nil)
	_ httpapi.MailboxOperator = (*accounts.Admin)(nil)
)

// withMailboxes gives the app's service the fake mailboxes.
func (a *app) withMailboxes() *mailboxestest.Fake {
	f := mailboxestest.New()
	a.svc.SetMailboxes(f)
	return f
}

// addressOf is a fake mailbox's address, which only its person's routes show.
func addressOf(t *testing.T, f *mailboxestest.Fake, account, id string) string {
	t.Helper()
	m, err := f.Get(context.Background(), mailboxes.Caller{Account: account}, id)
	if err != nil {
		t.Fatal(err)
	}
	return m.Address
}

// served is a handler as the enclave serves it: behind the router, which
// sets the security headers of every answer.
func served(app, api http.Handler) http.Handler {
	return router.New(nil, map[string]http.Handler{"app.permaily.test": app, "api.permaily.test": api})
}

// answered checks what every answer of a mailbox route must be (O5): JSON,
// nosniff, never cached, and nothing of a mailbox but what the operator may
// see: no address, no label.
func answered(t *testing.T, what string, rec *httptest.ResponseRecorder, secrets ...string) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s: Content-Type %q", what, ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("%s: no nosniff", what)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("%s: may be cached", what)
	}
	body := strings.ToLower(rec.Body.String())
	if strings.Contains(body, "@inbox.") {
		t.Errorf("%s: an address in %s", what, rec.Body)
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(body, strings.ToLower(s)) {
			t.Errorf("%s: %q in %s", what, s, rec.Body)
		}
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Errorf("%s: not JSON: %s", what, rec.Body)
	}
}

// through calls the app's routes as the browser does, through the router.
func (a *app) through(method, path, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	h := a.h
	a.h = served(h, nil)
	defer func() { a.h = h }()
	return a.call(method, path, body, nil)
}

// O2, O5: the admin panel's routes of mailboxes, end to end. An admin looks at
// an account's mailboxes and finds one by its address without a fresh proof;
// pausing and resuming need one, are refused on an admin's mailbox to anyone
// but the super admin, are limited, and are written in the admin log with the
// account and the mailbox. No answer holds an address or a label.
func TestTheAdminPanelsMailboxes(t *testing.T) {
	a := newApp(t)
	f := a.withMailboxes()
	carl := a.signIn("carl@example.org")
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
	read(t, a.call("PUT", "/api/v1/admin/admins/"+ana.Account.ID, "", nil), 200, nil)

	carlBox := f.Add(carl.Account.ID, "Carl's Gmail")
	bossBox := f.Add(boss.Account.ID, "Boss's Gmail")
	carlAddress, bossAddress := addressOf(t, f, carl.Account.ID, carlBox.ID), addressOf(t, f, boss.Account.ID, bossBox.ID)
	secrets := []string{carlAddress, bossAddress, "Carl's Gmail", "Boss's Gmail"}

	// ana, an admin, looking: no proof needed.
	a.use(anaJar)
	a.later()
	rec := a.through("GET", "/api/v1/admin/accounts/"+carl.Account.ID+"/mailboxes", "")
	answered(t, "an account's mailboxes", rec, secrets...)
	var list contracts.MailboxRecordList
	read(t, rec, 200, &list)
	if len(list.Mailboxes) != 1 || list.Mailboxes[0].ID != carlBox.ID || list.Mailboxes[0].Account != carl.Account.ID {
		t.Fatalf("records: %+v", list)
	}
	rec = a.through("GET", "/api/v1/admin/mailboxes?address="+url.QueryEscape(strings.ToUpper(carlAddress)), "")
	answered(t, "the mailbox of an address", rec, secrets...)
	read(t, rec, 200, &list)
	if len(list.Mailboxes) != 1 || list.Mailboxes[0].ID != carlBox.ID {
		t.Fatalf("found: %+v", list)
	}
	// Every address that leads to no mailbox answers the same.
	var none string
	for _, q := range []string{"address=nobody%40inbox.permaily.test", "address=carl%40example.org", "address=" + url.QueryEscape(carlAddress+"x"), "address=not+an+address"} {
		rec := a.through("GET", "/api/v1/admin/mailboxes?"+q, "")
		answered(t, q, rec, secrets...)
		if rec.Code != 200 || (none != "" && rec.Body.String() != none) {
			t.Fatalf("%s: %d %s", q, rec.Code, rec.Body)
		}
		none = rec.Body.String()
	}
	if none != `{"mailboxes":[]}`+"\n" {
		t.Fatalf("none: %q", none)
	}
	// A request that names no address, or two, is refused for what it is.
	for _, q := range []string{"", "?address=", "?address=%20", "?address=" + url.QueryEscape(carlAddress) + "&address=" + url.QueryEscape(bossAddress)} {
		rec := a.through("GET", "/api/v1/admin/mailboxes"+q, "")
		answered(t, "no address "+q, rec, secrets...)
		if e := refusal(t, rec, 400); e.Error.Code != "invalid_address" {
			t.Fatalf("%q: %+v", q, e)
		}
	}
	// Not an account: not_found, before the mailboxes are asked.
	for _, id := range []string{"acc_" + strings.Repeat("a", 26), "nobody", carlBox.ID} {
		rec := a.through("GET", "/api/v1/admin/accounts/"+id+"/mailboxes", "")
		answered(t, "not an account", rec)
		if e := refusal(t, rec, 404); e.Error.Code != "not_found" {
			t.Fatalf("%s: %+v", id, e)
		}
	}

	// Changing needs the fresh proof; the guard is told first.
	rec = a.through("POST", "/api/v1/admin/mailboxes/"+carlBox.ID+"/pause", "")
	answered(t, "pause without a proof", rec, secrets...)
	if e := refusal(t, rec, 403); e.Error.Code != "confirmation_needed" {
		t.Fatalf("pause without a proof: %+v", e)
	}
	if e := refusal(t, a.through("POST", "/api/v1/admin/mailboxes/"+bossBox.ID+"/pause", ""), 403); e.Error.Code != "super_admin_only" {
		t.Fatalf("the super admin's mailbox, by an admin: %+v", e)
	}
	if res := a.confirmWith(anaAuth, anaKey); res.StatusCode != 200 {
		t.Fatalf("ana confirms: %d", res.StatusCode)
	}
	rec = a.through("POST", "/api/v1/admin/mailboxes/"+carlBox.ID+"/pause", "")
	answered(t, "pause", rec, secrets...)
	var paused contracts.MailboxRecord
	read(t, rec, 200, &paused)
	if paused.State != contracts.MailboxPaused || paused.ID != carlBox.ID {
		t.Fatalf("paused: %+v", paused)
	}
	if e := refusal(t, a.through("POST", "/api/v1/admin/mailboxes/"+bossBox.ID+"/resume", ""), 403); e.Error.Code != "super_admin_only" {
		t.Fatalf("the super admin's mailbox, confirmed: %+v", e)
	}
	if e := refusal(t, a.through("POST", "/api/v1/admin/mailboxes/mbx_"+strings.Repeat("z", 26)+"/pause", ""), 404); e.Error.Code != "not_found" {
		t.Fatalf("no such mailbox: %+v", e)
	}
	rec = a.through("POST", "/api/v1/admin/mailboxes/"+carlBox.ID+"/resume", "")
	answered(t, "resume", rec, secrets...)
	read(t, rec, 200, &paused)
	if paused.State != contracts.MailboxWaiting {
		t.Fatalf("resumed: %+v", paused)
	}
	// Ten changes in thirty days, then too_many_requests.
	for i := 2; i < mailboxes.MaxStateChangesPerPeriod; i++ {
		action := []string{"pause", "resume"}[i%2]
		read(t, a.through("POST", "/api/v1/admin/mailboxes/"+carlBox.ID+"/"+action, ""), 200, nil)
	}
	rec = a.through("POST", "/api/v1/admin/mailboxes/"+carlBox.ID+"/pause", "")
	answered(t, "the eleventh", rec, secrets...)
	if e := refusal(t, rec, 429); e.Error.Code != "too_many_requests" {
		t.Fatalf("the eleventh: %+v", e)
	}
	// Only POST changes; looking is only GET.
	for _, r := range []struct{ method, path, allow string }{
		{"GET", "/api/v1/admin/mailboxes/" + carlBox.ID + "/pause", "POST"}, {"DELETE", "/api/v1/admin/mailboxes/" + carlBox.ID + "/resume", "POST"},
		{"POST", "/api/v1/admin/mailboxes?address=x", "GET"}, {"DELETE", "/api/v1/admin/accounts/" + carl.Account.ID + "/mailboxes", "GET"},
	} {
		rec := a.through(r.method, r.path, "")
		if rec.Code != 405 || rec.Header().Get("Allow") != r.allow {
			t.Errorf("%s %s: %d %s", r.method, r.path, rec.Code, rec.Header().Get("Allow"))
		}
	}
	// Nothing else about a mailbox is there: no disconnecting, reading or
	// renaming.
	for _, r := range [][2]string{
		{"DELETE", "/api/v1/admin/mailboxes/" + carlBox.ID}, {"GET", "/api/v1/admin/mailboxes/" + carlBox.ID + "/messages"},
		{"PATCH", "/api/v1/admin/mailboxes/" + carlBox.ID}, {"POST", "/api/v1/admin/mailboxes/" + carlBox.ID + "/disconnect"},
		{"GET", "/api/v1/admin/mailboxes/" + carlBox.ID},
	} {
		if rec := a.through(r[0], r[1], `{"label":"x"}`); rec.Code != 404 && rec.Code != 405 {
			t.Errorf("%s %s: %d %s", r[0], r[1], rec.Code, rec.Body)
		}
	}

	// The super admin acts on its own mailbox, and on an admin's.
	a.use(bossJar)
	if res := a.confirmWith(bossAuth, bossKey); res.StatusCode != 200 {
		t.Fatalf("the super admin confirms: %d", res.StatusCode)
	}
	read(t, a.through("POST", "/api/v1/admin/mailboxes/"+bossBox.ID+"/pause", ""), 200, nil)

	// The log names the account and the mailbox by identifier, and nothing
	// of the mailbox else; the counts say how many there are.
	rec = a.through("GET", "/api/v1/admin/log", "")
	answered(t, "the log", rec, secrets...)
	var log contracts.AdminLog
	read(t, rec, 200, &log)
	pauses := 0
	for _, e := range log.Entries {
		if e.Kind == "mailbox_paused" || e.Kind == "mailbox_resumed" {
			pauses++
			if e.Mailbox == "" || e.Account == nil || (e.Mailbox == carlBox.ID) != (e.Account.ID == carl.Account.ID) {
				t.Fatalf("an entry: %+v", e)
			}
		}
	}
	if pauses != mailboxes.MaxStateChangesPerPeriod+1 {
		t.Fatalf("%d entries of mailboxes:\n%s", pauses, rec.Body)
	}
	rec = a.through("GET", "/api/v1/admin/stats", "")
	var stats contracts.Stats
	read(t, rec, 200, &stats)
	if stats.Mailboxes.Total != 2 {
		t.Fatalf("stats: %s", rec.Body)
	}
	rec = a.through("GET", "/api/v1/admin/accounts/"+carl.Account.ID, "")
	var record contracts.AccountRecord
	read(t, rec, 200, &record)
	if record.Mailboxes != 1 || !strings.Contains(rec.Body.String(), `"mailboxes":1`) {
		t.Fatalf("the account: %s", rec.Body)
	}
	// The enclave's log holds no address, label or identifier.
	for _, s := range append(secrets, carlBox.ID, bossBox.ID, carl.Account.ID, "@inbox.") {
		if strings.Contains(a.logs.String(), s) {
			t.Fatalf("the logs hold %q:\n%s", s, a.logs)
		}
	}
}

// operator is the operator's routes as the enclave mounts them, with the
// mailboxes' (cmd/permaily-enclave), behind the router.
func (a *app) operator() http.Handler {
	log := slog.New(slog.NewTextHandler(a.logs, nil))
	mux := http.NewServeMux()
	mux.Handle("/v1/admin/", a.admin())
	mailboxesOf := httpapi.TokenAuth(tokenHash(), httpapi.Mailboxes(a.svc, log))
	mux.Handle("/v1/admin/mailboxes", mailboxesOf)
	mux.Handle("/v1/admin/mailboxes/", mailboxesOf)
	return served(nil, mux)
}

// O2, O4, O5: the operator's routes of mailboxes, with the admin token: no
// proof, no guard, no admin log; every change counted on /status by its kind,
// and nothing of which mailbox or account there.
func TestTheOperatorsMailboxes(t *testing.T) {
	a := newApp(t)
	f := a.withMailboxes()
	h := a.operator()
	carl := a.signIn("carl@example.org")
	box := f.Add(carl.Account.ID, "Carl's Gmail")
	address := addressOf(t, f, carl.Account.ID, box.ID)
	secrets := []string{address, "Carl's Gmail"}
	call := func(method, path, auth string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "https://api.permaily.test"+path, nil)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, r := range [][2]string{
		{"GET", "/v1/admin/accounts/" + carl.Account.ID + "/mailboxes"}, {"GET", "/v1/admin/mailboxes?address=" + url.QueryEscape(address)},
		{"POST", "/v1/admin/mailboxes/" + box.ID + "/pause"}, {"POST", "/v1/admin/mailboxes/" + box.ID + "/resume"},
	} {
		for _, auth := range []string{"", "not the token"} {
			if rec := call(r[0], r[1], auth); rec.Code != 401 || strings.Contains(rec.Body.String(), box.ID) {
				t.Errorf("%s %s with %q: %d %s", r[0], r[1], auth, rec.Code, rec.Body)
			}
		}
	}
	if rec, _ := f.Record(context.Background(), box.ID); !rec.PausedAt.IsZero() {
		t.Fatal("paused without the token")
	}

	rec := call("GET", "/v1/admin/accounts/"+carl.Account.ID+"/mailboxes", token)
	answered(t, "records", rec, secrets...)
	var list contracts.MailboxRecordList
	read(t, rec, 200, &list)
	if len(list.Mailboxes) != 1 || list.Mailboxes[0].ID != box.ID {
		t.Fatalf("records: %s", rec.Body)
	}
	rec = call("GET", "/v1/admin/mailboxes?address="+url.QueryEscape(address), token)
	answered(t, "by address", rec, secrets...)
	read(t, rec, 200, &list)
	if len(list.Mailboxes) != 1 || list.Mailboxes[0].Account != carl.Account.ID {
		t.Fatalf("by address: %s", rec.Body)
	}
	for _, action := range []string{"pause", "pause", "resume"} {
		rec := call("POST", "/v1/admin/mailboxes/"+box.ID+"/"+action, token)
		answered(t, action, rec, secrets...)
		read(t, rec, 200, nil)
	}
	// What was refused is not counted.
	call("POST", "/v1/admin/mailboxes/mbx_"+strings.Repeat("z", 26)+"/pause", token)
	call("POST", "/v1/admin/mailboxes/"+box.ID+"/pause", "not the token")
	if rec := call("GET", "/v1/admin/accounts/acc_"+strings.Repeat("a", 26)+"/mailboxes", token); rec.Code != 404 {
		t.Fatalf("not an account: %d %s", rec.Code, rec.Body)
	}
	if rec := call("GET", "/v1/admin/mailboxes", token); rec.Code != 400 || !strings.Contains(rec.Body.String(), "invalid_address") {
		t.Fatalf("no address: %d %s", rec.Code, rec.Body)
	}
	// The operator is in no admin log.
	if l, err := a.svc.AdminLog(context.Background(), "", 50); err != nil || len(l.Entries) != 0 {
		t.Fatalf("admin log: %+v %v", l, err)
	}

	// O4: the counts on /status.
	status := httpapi.Status(httpapi.StatusSources{Admin: func() *attestation.AdminStatus {
		since, actions := a.svc.OperatorActions()
		return &attestation.AdminStatus{Since: since, Actions: actions}
	}})
	srec := httptest.NewRecorder()
	status.ServeHTTP(srec, httptest.NewRequest("GET", "/status", nil))
	var st attestation.Status
	if srec.Code != 200 || json.Unmarshal(srec.Body.Bytes(), &st) != nil || st.Admin == nil {
		t.Fatalf("status: %d %s", srec.Code, srec.Body)
	}
	// Pause, pause again, resume: the second pause changed nothing and is
	// not counted.
	if st.Admin.Actions[attestation.AdminMailboxPaused] != 1 || st.Admin.Actions[attestation.AdminMailboxResumed] != 1 {
		t.Fatalf("counted: %+v", st.Admin.Actions)
	}
	for _, s := range append(secrets, box.ID, carl.Account.ID, "mbx_", "acc_") {
		if strings.Contains(srec.Body.String(), s) {
			t.Fatalf("/status holds %q: %s", s, srec.Body)
		}
	}
	for _, s := range append(secrets, box.ID, carl.Account.ID) {
		if strings.Contains(a.logs.String(), s) {
			t.Fatalf("the logs hold %q:\n%s", s, a.logs)
		}
	}
}

// brokenMailboxes fails every call with an error that names a mailbox and
// its address, as a storage error might.
type brokenMailboxes struct{}

var errBrokenMailboxes = errors.New("mailboxes down: secret@inbox.permaily.test mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa acc_aaaaaaaaaaaaaaaaaaaaaaaaaa")

func (brokenMailboxes) MailboxRecords(context.Context, string) (*contracts.MailboxRecordList, error) {
	return nil, errBrokenMailboxes
}

func (brokenMailboxes) MailboxRecordByAddress(context.Context, string) (*contracts.MailboxRecordList, error) {
	return nil, errBrokenMailboxes
}

func (brokenMailboxes) PauseMailbox(context.Context, string) (*contracts.MailboxRecord, error) {
	return nil, errBrokenMailboxes
}

func (brokenMailboxes) ResumeMailbox(context.Context, string) (*contracts.MailboxRecord, error) {
	return nil, errBrokenMailboxes
}

// O5: an error of the service's own is answered with nothing of its own, and
// logged with nothing of what it says.
func TestTheMailboxRoutesInternalErrors(t *testing.T) {
	var logs strings.Builder
	h := served(nil, httpapi.Mailboxes(brokenMailboxes{}, slog.New(slog.NewTextHandler(&logs, nil))))
	for _, r := range [][2]string{
		{"GET", "/v1/admin/mailboxes?address=secret%40inbox.permaily.test"},
		{"POST", "/v1/admin/mailboxes/mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa/pause"}, {"POST", "/v1/admin/mailboxes/mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa/resume"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(r[0], "https://api.permaily.test"+r[1], nil))
		answered(t, r[1], rec, "mbx_", "acc_", "secret")
		if rec.Code != 500 || rec.Body.String() != `{"error":{"code":"internal","message":"internal error"}}`+"\n" {
			t.Errorf("%s %s: %d %s", r[0], r[1], rec.Code, rec.Body)
		}
	}
	if !strings.Contains(logs.String(), "api: internal error") {
		t.Fatalf("not logged:\n%s", logs.String())
	}
	// Logged through the one redactor: what kind of identifier, never which.
	for _, s := range []string{"secret", "inbox.permaily", "mbx_a", "acc_a"} {
		if strings.Contains(logs.String(), s) {
			t.Fatalf("logged %q:\n%s", s, logs.String())
		}
	}
	// And through the operator's account routes: an account's mailboxes.
	logs.Reset()
	accountsH := served(nil, httpapi.Accounts(struct {
		httpapi.OperatorService
		brokenMailboxes
	}{nil, brokenMailboxes{}}, slog.New(slog.NewTextHandler(&logs, nil))))
	rec := httptest.NewRecorder()
	accountsH.ServeHTTP(rec, httptest.NewRequest("GET", "https://api.permaily.test/v1/admin/accounts/acc_aaaaaaaaaaaaaaaaaaaaaaaaaa/mailboxes", nil))
	if rec.Code != 500 || strings.Contains(logs.String(), "mbx_a") || strings.Contains(logs.String(), "acc_a") || strings.Contains(logs.String(), "secret") {
		t.Fatalf("%d %s\n%s", rec.Code, rec.Body, logs.String())
	}
}

// O3: what the mailbox routes can ask of the service is these four, each
// taking an identifier or an address to look up and nothing else: nothing
// that reads mail, shows an address, disconnects a mailbox or deletes mail.
func TestTheMailboxRoutesCannotReadMailOrDisconnect(t *testing.T) {
	ops := reflect.TypeOf((*httpapi.MailboxOperator)(nil)).Elem()
	var names []string
	for i := range ops.NumMethod() {
		names = append(names, ops.Method(i).Name)
		m := ops.Method(i).Type
		for j := 1; j < m.NumIn(); j++ {
			if k := m.In(j).Kind(); k != reflect.String {
				t.Errorf("%s takes a %s", ops.Method(i).Name, m.In(j))
			}
		}
		for j := 0; j < m.NumOut(); j++ {
			if out := m.Out(j); out != reflect.TypeOf(&contracts.MailboxRecordList{}) && out != reflect.TypeOf(&contracts.MailboxRecord{}) && out != reflect.TypeOf((*error)(nil)).Elem() {
				t.Errorf("%s answers a %s", ops.Method(i).Name, out)
			}
		}
	}
	if want := []string{"MailboxRecordByAddress", "MailboxRecords", "PauseMailbox", "ResumeMailbox"}; !slices.Equal(names, want) {
		t.Fatalf("what the mailbox routes can ask: %v, want %v", names, want)
	}
	// What the operator and the admins are shown of a mailbox has no field
	// for an address, a label or a provider's account.
	rec := reflect.TypeOf(contracts.MailboxRecord{})
	var fields []string
	for i := range rec.NumField() {
		fields = append(fields, rec.Field(i).Tag.Get("json"))
	}
	if want := []string{"id", "account", "state", "created_at", "retention_days", "receiving_since,omitzero", "paused_at,omitzero", "verified"}; !slices.Equal(fields, want) {
		t.Fatalf("a mailbox's record shows %v", fields)
	}
}
