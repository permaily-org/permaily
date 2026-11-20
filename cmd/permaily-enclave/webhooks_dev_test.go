//go:build dev

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/services/accountrules"
	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webhook"
	"github.com/permaily-org/permaily/internal/webhook/webhooktest"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// The app with a webhook: its client_id and its receiver are on one host
// the played vendor serves (ADR 0039, "Same origin").
const (
	e2eHooksClient = "https://hooks.vendor-e2e.org/permaily/client.json"
	e2eHookURL     = "https://hooks.vendor-e2e.org/permaily/hooks/cleo-7d2f"
	e2eHookZone    = "Europe/Madrid"
)

// receiver is the app's receiver: the played vendor of webhooktest, which
// verifies every notice as a vendor must, behind a switch that answers
// 500 to every call while failing is set.
type receiver struct {
	vendor  atomic.Pointer[webhooktest.Vendor]
	failing atomic.Bool
	failed  atomic.Int64
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if r.failing.Load() {
		r.failed.Add(1)
		_, _ = io.Copy(io.Discard, req.Body)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if v := r.vendor.Load(); v != nil {
		v.ServeHTTP(w, req)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
}

func (r *receiver) taken() []contracts.WebhookNotice {
	if v := r.vendor.Load(); v != nil {
		return v.Taken()
	}
	return nil
}

// pollFor polls cond until it holds, for at most within.
func pollFor(within time.Duration, cond func() bool) bool {
	for end := time.Now().Add(within); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

// webhookPasses is how long the tests of the whole service wait for what
// the queues of both enclaves would do: three of their passes, each five
// seconds apart.
const webhookPasses = 16 * time.Second

// webhooksEndToEnd is K59 and K59b (ADR 0039, "The image"), the webhooks'
// half of TestMailEndToEnd, through two enclaves over one store, with the
// real connections service, the real rules (ADR 0040) and the real queue:
//
//   - K59: a played app pushes a request with a webhook (new mail, and
//     every day at a quarter hour in Madrid), Cleo approves it through one
//     enclave in her browser's zone, and the app reads its subscription and
//     the key set from the api host. A message arrives: a notice signed for
//     the subscription with the published key reaches the app, which
//     fetches the message with its token, and a receipt is written. A
//     sign-in code arrives: no notice. The schedule's time comes: a
//     scheduled notice, from the connection's minute. The connection is
//     revoked: no notice follows.
//   - K59b: a notice of a message is pending while the receiver fails; a
//     rule that holds the message is saved through the other enclave; the
//     retry once the rules are five seconds old calls nothing (changed);
//     the queue's state put back to its copy from before the rule: still
//     nothing; the rule deleted: nothing is told of that message, which
//     was evaluated under the rule, and the app's next fetch gets it.
//
// It returns what the log must not hold (K48's list: the URL, the
// subscription, notices' identifiers, the zone, the vendor that is not
// verified).
func webhooksEndToEnd(t *testing.T, w e2eWorld) []string {
	cfg := oauthConfig(router.HostsForDomain(w.enclaves[0].domain), w.enclaves[0].oauthPort())
	w.vendor.serve(t, e2eHooksClient, e2eClientDocument(e2eHooksClient, "E2E Hooks App"))
	rcv := &receiver{}
	w.vendor.hook.Store(&[]http.Handler{rcv}[0])
	secrets := []string{e2eHookURL, "cleo-7d2f", "hooks.vendor-e2e.org", e2eHookZone, "Madrid", "cleo@", "dora@", "ibis-41a0", "Lisbon", "Rosa", "Porto"}
	decode := func(a mailAnswer, status int, v any) {
		t.Helper()
		if a.code != status || (v != nil && json.Unmarshal([]byte(a.body), v) != nil) {
			t.Fatalf("%d %s, want %d", a.code, a.body, status)
		}
	}
	// The app's own client, to either enclave.
	appDo := func(enclave int, method, rawURL, token, contentType, body string) mailAnswer {
		t.Helper()
		client := &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev mode self-signed certificate
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", w.enclaves[enclave].port))
			},
		}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, err := http.NewRequest(method, rawURL, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		return mailAnswer{code: res.StatusCode, header: res.Header, body: string(out)}
	}
	// confirm is a fresh proof by a code by mail: the newest message of
	// its kind to Cleo, once it arrived.
	sixDigits := regexp.MustCompile(`\n    (\d{6})\n`)
	cleo := w.signUp("cleo@example.org")
	confirmCodes := func() []string {
		var out []struct{ To, Subject, Text string }
		_ = json.Unmarshal([]byte(w.call(w.newBrowser(), 0, "GET", "/dev/outbox", "").body), &out)
		var codes []string
		for _, m := range out {
			if g := sixDigits.FindStringSubmatch(m.Text); g != nil && m.To == "cleo@example.org" && m.Subject == "Confirm it is you on Permaily" {
				codes = append(codes, g[1])
			}
		}
		return codes
	}
	confirm := func() {
		t.Helper()
		before := len(confirmCodes())
		var c struct{ Attempt string }
		decode(w.call(cleo, 0, "POST", "/api/v1/auth/confirm/email/start", ""), 200, &c)
		var codes []string
		if !pollFor(5*time.Second, func() bool { codes = confirmCodes(); return len(codes) > before }) {
			t.Fatal("no code to confirm")
		}
		decode(w.call(cleo, 1, "POST", "/api/v1/auth/confirm/email/finish", `{"attempt":"`+c.Attempt+`","code":"`+codes[len(codes)-1]+`"}`), 200, nil)
	}
	var box contracts.Mailbox
	decode(w.call(cleo, 0, "POST", "/api/v1/mailboxes", `{"label":"Cleo"}`), 201, &box)
	decode(w.call(cleo, 1, "POST", "/api/v1/account/model-notice", `{"version":"`+domain.ModelNoticeVersion+`"}`), 200, nil)
	secrets = append(secrets, box.ID, box.Address)
	mail := func(subject, text string) {
		t.Helper()
		raw := "From: Dora <dora@example.net>\r\nTo: " + box.Address + "\r\nSubject: " + subject + "\r\nContent-Type: text/plain\r\n\r\n" + text + "\r\n"
		if err := w.send(0, raw, box.Address); err != nil {
			t.Fatal(err)
		}
	}

	// K57's zones and K59: the schedule at the next quarter hour at least
	// two minutes ahead, on Madrid's wall.
	madrid, err := webhook.Zone(e2eHookZone)
	if err != nil {
		t.Fatal(err)
	}
	soon := devClock().In(madrid).Add(2 * time.Minute)
	run := soon.Truncate(15 * time.Minute)
	if run.Before(soon) {
		run = run.Add(15 * time.Minute)
	}
	hook := contracts.WebhookRequest{URL: e2eHookURL, NewMail: true, Schedule: &contracts.WebhookSchedule{Every: contracts.EveryDay, At: run.Format("15:04")}}
	hookJSON, _ := json.Marshal(hook)

	// The pushed request, the screen, Allow in the browser's zone, the code
	// exchanged through the other enclave.
	confirm()
	verifier := randomURLSafe(t)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := "st-" + randomURLSafe(t)[:12]
	secrets = append(secrets, verifier, state, challenge)
	form := url.Values{
		"client_id": {e2eHooksClient}, "redirect_uri": {e2eRedirect}, "response_type": {"code"}, "state": {state},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "resource": {cfg.APIResource},
		"scope": {"mail.sender mail.subject mail.body"}, "webhook": {string(hookJSON)},
	}
	var pushed struct {
		RequestURI string `json:"request_uri"`
	}
	decode(appDo(1, "POST", cfg.Issuer+oauth.PathPushed, "", "application/x-www-form-urlencoded", form.Encode()), 201, &pushed)
	secrets = append(secrets, pushed.RequestURI)
	start, _ := json.Marshal(contracts.AuthorizationStart{Query: url.Values{"client_id": {e2eHooksClient}, "request_uri": {pushed.RequestURI}}.Encode()})
	var started contracts.AuthorizationStarted
	decode(w.call(cleo, 0, "POST", "/api/v1/oauth/requests", string(start)), 200, &started)
	r := started.Request
	if r == nil || r.Webhook == nil || r.Webhook.Host != "hooks.vendor-e2e.org" || !r.Webhook.NewMail || r.Webhook.Schedule == nil || *r.Webhook.Schedule != *hook.Schedule {
		t.Fatalf("the screen's webhook: %+v", started)
	}
	secrets = append(secrets, r.ID, r.ID[len(oauth.PrefixRequest):])
	draft := contracts.ConsentDraft{Mailboxes: []string{box.ID}, Since: contracts.MailSince{At: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}}
	preview, _ := json.Marshal(contracts.ConsentPreview{Draft: draft, Mailbox: box.ID})
	var page contracts.ConsentPreviewPage
	decode(w.call(cleo, 1, "POST", "/api/v1/oauth/requests/"+r.ID+"/matches", string(preview)), 200, &page)
	// A zone the image does not know is refused in words; the browser's is
	// taken.
	bad, _ := json.Marshal(contracts.AuthorizationDecision{Allow: true, Draft: &draft, ModelNotice: r.ModelNotice, Snapshot: &page.Snapshot, TimeZone: "Mars/Olympus_Mons"})
	if a := w.call(cleo, 0, "POST", "/api/v1/oauth/requests/"+r.ID+"/decision", string(bad)); a.code != 400 || !strings.Contains(a.body, `"time_zone_invalid"`) {
		t.Fatalf("a zone the image does not know: %d %s", a.code, a.body)
	}
	decision, _ := json.Marshal(contracts.AuthorizationDecision{Allow: true, Draft: &draft, ModelNotice: r.ModelNotice, Snapshot: &page.Snapshot, TimeZone: e2eHookZone})
	var answer contracts.AuthorizationAnswer
	decode(w.call(cleo, 0, "POST", "/api/v1/oauth/requests/"+r.ID+"/decision", string(decision)), 200, &answer)
	back, err := url.Parse(answer.Redirect)
	if err != nil || back.Query().Get("state") != state {
		t.Fatalf("the redirect: %v", answer.Redirect)
	}
	code := back.Query().Get("code")
	secrets = append(secrets, code, code[len(oauth.PrefixCode):])
	var tok connectionsTokens
	decode(appDo(1, "POST", cfg.Issuer+oauth.PathToken, "", "application/x-www-form-urlencoded", url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {e2eRedirect},
		"client_id": {e2eHooksClient}, "resource": {cfg.APIResource},
	}.Encode()), 200, &tok)
	secrets = append(secrets, tok.AccessToken, tok.RefreshToken, tok.AccessToken[len(oauth.PrefixAccess):], tok.RefreshToken[len(oauth.PrefixRefresh):])
	var conns contracts.ConnectionList
	decode(w.call(cleo, 1, "GET", "/api/v1/connections", ""), 200, &conns)
	if len(conns.Connections) != 1 || conns.Connections[0].Webhook == nil || conns.Connections[0].Webhook.State != contracts.WebhookOn {
		t.Fatalf("Cleo's connection: %+v", conns)
	}
	connID := conns.Connections[0].ID
	secrets = append(secrets, connID, connID[len("con_"):])

	// The app reads its subscription, and the key set from the api host;
	// its receiver verifies every notice with them, as a vendor must.
	var mine contracts.VendorConnection
	decode(appDo(0, "GET", cfg.APIResource+"/connection", tok.AccessToken, "", ""), 200, &mine)
	if mine.Webhook == nil || !strings.HasPrefix(mine.Webhook.Subscription, webhook.SubscriptionPrefix) || mine.Webhook.URL != e2eHookURL || strings.Contains(fmt.Sprint(mine), "Madrid") {
		t.Fatalf("the app's connection: %+v", mine)
	}
	sub := mine.Webhook.Subscription
	secrets = append(secrets, sub, sub[len(webhook.SubscriptionPrefix):])
	set := appDo(1, "GET", cfg.Issuer+webhook.KeysPath, "", "", "")
	keys, err := webhook.ParseKeySet([]byte(set.body))
	if set.code != 200 || err != nil || len(keys) != 1 {
		t.Fatalf("the key set: %d %s", set.code, set.body)
	}
	rcv.vendor.Store(&webhooktest.Vendor{Keys: []ed25519.PublicKey{keys[webhook.KeyID]}, Endpoint: sub, Audience: e2eHookURL, Now: devClock})
	taken := func() []contracts.WebhookNotice { return rcv.taken() }
	noticeIDs := func() {
		for _, n := range taken() {
			secrets = append(secrets, n.ID, n.ID[len(webhook.NoticePrefix):])
		}
	}
	// receiptsOf counts the links of Cleo's chain: one a receipt.
	receiptsOf := func() int {
		var p contracts.ReceiptsPage
		decode(w.call(cleo, 0, "GET", "/api/v1/receipts?limit=100", ""), 200, &p)
		return len(p.Links)
	}
	search := func(enclave int) []string {
		t.Helper()
		var vp contracts.VendorPage
		decode(appDo(enclave, "POST", cfg.APIResource+"/messages/search", tok.AccessToken, "application/json", `{}`), 200, &vp)
		var subjects []string
		for _, m := range vp.Messages {
			secrets = append(secrets, m.ID)
			if m.Subject != nil {
				subjects = append(subjects, *m.Subject)
			}
		}
		return subjects
	}

	// K59: a message arrives through one enclave; a signed notice of it
	// reaches the app within the queue's passes; the app fetches it with
	// its token, and a receipt is written.
	madeAt := devClock()
	mail("Trip to Lisbon ibis-41a0", "The train leaves at nine ibis-41a0.")
	if !pollFor(webhookPasses, func() bool { return len(taken()) == 1 }) {
		t.Fatalf("no notice of new mail: %+v (refused %d)", taken(), rcv.vendor.Load().Refused())
	}
	first := taken()[0]
	if first.Type != contracts.NoticeMailMatched || first.Subscription != sub || first.Audience != e2eHookURL || first.ClientID != e2eHooksClient ||
		first.Since.Before(madeAt.Truncate(time.Minute)) || !first.ScheduledAt.IsZero() {
		t.Fatalf("the notice: %+v", first)
	}
	before := receiptsOf()
	if got := search(1); len(got) != 1 || got[0] != "Trip to Lisbon ibis-41a0" {
		t.Fatalf("the app's fetch after the notice: %q", got)
	}
	if after := receiptsOf(); after <= before {
		t.Fatalf("the fetch wrote no receipt: %d then %d", before, after)
	}

	// A sign-in code arrives, a minute later: the default rule holds it,
	// and no notice is made.
	devAhead.Add(int64(webhook.MinInterval + time.Second))
	mail("Your verification code", "Your verification code is 482913 ibis-41a0.")
	if pollFor(webhookPasses, func() bool { return len(taken()) != 1 }) {
		t.Fatalf("a held message made a notice: %+v", taken())
	}

	// The schedule's time comes on Madrid's wall: a scheduled notice whose
	// since is the connection's minute (nothing scheduled was taken yet).
	devAhead.Add(int64(run.Sub(devClock()) + 2*time.Second))
	if !pollFor(webhookPasses, func() bool { return len(taken()) == 2 }) {
		t.Fatalf("no scheduled notice: %+v", taken())
	}
	scheduled := taken()[1]
	if scheduled.Type != contracts.NoticeScheduleDue || !scheduled.ScheduledAt.Equal(run) || scheduled.Late || scheduled.ID == first.ID ||
		!scheduled.Since.Equal(webhook.Coverage(conns.Connections[0].MadeAt)) {
		t.Fatalf("the scheduled notice: %+v (made at %s)", scheduled, conns.Connections[0].MadeAt)
	}
	noticeIDs()

	// K59b: the receiver fails, and a message arrives; its notice is
	// called and fails.
	ctx := context.Background()
	devAhead.Add(int64(webhook.MinInterval + time.Second))
	rcv.failing.Store(true)
	mail("Dinner at Rosa's ibis-41a0", "A table for two at eight ibis-41a0.")
	if !pollFor(webhookPasses, func() bool { return rcv.failed.Load() >= 1 }) {
		t.Fatal("the notice of the dinner was not called")
	}
	failedBefore := rcv.failed.Load()
	// What the queue keeps of the connection now, the pending match under
	// the rules as they are: put back later, as whoever writes the table
	// could.
	mem := w.shared.mem
	statePartition := webhooksStatePartition(connID)
	pending, err := mem.GetEntry(ctx, statePartition, connID)
	if err != nil {
		t.Fatal(err)
	}
	// A rule that holds the dinner, saved through the other enclave with a
	// fresh proof.
	confirm()
	ruleDraft := `{"name":"dinner-rule-5be","criteria":[{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"dinner"}]}]}],"effect":"hold","scope":{"to":"all"},"on":true}`
	secrets = append(secrets, "dinner-rule-5be")
	var rules contracts.RuleList
	decode(w.call(cleo, 1, "GET", "/api/v1/rules", ""), 200, &rules)
	var checked contracts.RuleCheck
	decode(w.call(cleo, 1, "POST", "/api/v1/rules/check", `{"draft":`+ruleDraft+`}`), 200, &checked)
	var saved contracts.Rule
	decode(w.call(cleo, 1, "POST", "/api/v1/rules", fmt.Sprintf(`{"draft_id":%q,"draft":%s,"version":%d}`, checked.DraftID, ruleDraft, rules.Version)), 200, &saved)
	secrets = append(secrets, saved.ID, saved.ID[len("rul_"):])
	// The receiver answers again. The retry comes, a minute after the
	// failure, once the rules are five seconds old in both enclaves: the
	// match is discarded (changed) and nothing is called.
	rcv.failing.Store(false)
	changedBefore := webhooksSkipped(t, w, "changed")
	devAhead.Add(int64(time.Minute + accountrules.Fresh + time.Second))
	if !pollFor(webhookPasses, func() bool { return webhooksSkipped(t, w, "changed") > changedBefore }) {
		t.Fatalf("the pending match was not discarded: %+v", taken())
	}
	if len(taken()) != 2 || rcv.failed.Load() != failedBefore || rcv.vendor.Load().Calls() != 2 {
		t.Fatalf("a match the rule now holds was called: %+v", taken())
	}
	// The queue's state put back to its copy from before the rule, and
	// the connection made due by another message the rule holds: still
	// nothing is called.
	now, err := mem.GetEntry(ctx, statePartition, connID)
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.PutEntry(ctx, &store.Entry{Partition: statePartition, Key: connID, Value: pending.Value, Version: now.Version, ExpiresAt: pending.ExpiresAt}); err != nil {
		t.Fatal(err)
	}
	changedBefore = webhooksSkipped(t, w, "changed")
	mail("Dinner again ibis-41a0", "Or on Friday ibis-41a0.")
	if !pollFor(webhookPasses, func() bool { return webhooksSkipped(t, w, "changed") > changedBefore }) {
		t.Fatal("the match put back was not discarded")
	}
	if len(taken()) != 2 || rcv.vendor.Load().Calls() != 2 {
		t.Fatalf("a match put back from before the rule was called: %+v", taken())
	}
	// The rule deleted: nothing is told of the dinners, which were
	// evaluated under it; a message after them is told, from its own
	// minute; and the app's next fetch gets the dinners.
	decode(w.call(cleo, 1, "GET", "/api/v1/rules", ""), 200, &rules)
	if a := w.call(cleo, 0, "POST", "/api/v1/rules/"+saved.ID+"/delete", fmt.Sprintf(`{"version":%d}`, rules.Version)); a.code != 204 {
		t.Fatalf("deleting the rule: %d %s", a.code, a.body)
	}
	devAhead.Add(int64(webhook.MinInterval + accountrules.Fresh + time.Second))
	teaAt := devClock()
	mail("Tea on Sunday ibis-41a0", "At five ibis-41a0.")
	if !pollFor(webhookPasses, func() bool { return len(taken()) == 3 }) {
		t.Fatalf("no notice of the tea: %+v", taken())
	}
	if tea := taken()[2]; tea.Type != contracts.NoticeMailMatched || !tea.Since.Equal(teaAt.Truncate(time.Minute)) && !tea.Since.Equal(teaAt.Truncate(time.Minute).Add(time.Minute)) {
		t.Fatalf("the tea's notice tells of earlier mail: %+v (the tea at %s)", tea, teaAt)
	}
	if got := strings.Join(search(0), "|"); !strings.Contains(got, "Dinner at Rosa's") || !strings.Contains(got, "Dinner again") || strings.Contains(got, "verification") {
		t.Fatalf("the app's fetch after the rule was deleted: %q", got)
	}
	noticeIDs()

	// Revoked through one enclave: a message after it makes no notice
	// through either.
	devAhead.Add(int64(webhook.MinInterval + time.Second))
	decode(w.call(cleo, 0, "POST", "/api/v1/connections/"+connID+"/revoke", ""), 200, nil)
	devAhead.Add(int64(oauth.ConnectionFresh + time.Second))
	calls := rcv.vendor.Load().Calls()
	mail("Trip to Porto ibis-41a0", "The bus leaves at ten ibis-41a0.")
	if pollFor(webhookPasses, func() bool { return rcv.vendor.Load().Calls() != calls }) {
		t.Fatalf("a revoked connection's webhook was called: %+v", taken())
	}
	// /status names no URL, vendor, subscription or zone.
	for enclave := range 2 {
		st := w.call(w.newBrowser(), enclave, "GET", "/status", "")
		for _, s := range []string{"hooks.vendor-e2e.org", sub, e2eHookZone, connID} {
			if strings.Contains(st.body, s) {
				t.Fatalf("/status of enclave %d names %q", enclave, s)
			}
		}
	}
	return secrets
}

// webhooksStatePartition is the queue's state partition of a connection
// (webhooks' shard: 16 by a hash of its identifier).
func webhooksStatePartition(connection string) string {
	h := sha256.Sum256([]byte(connection))
	return webhooks.PartitionState + fmt.Sprintf("%02x", h[0]%webhooks.Shards)
}

// webhooksSkipped is the notices not sent for why, both enclaves together,
// as their /status count them.
func webhooksSkipped(t *testing.T, w e2eWorld, why string) int64 {
	t.Helper()
	var n int64
	for enclave := range 2 {
		var st attestation.Status
		a := w.call(w.newBrowser(), enclave, "GET", "/status", "")
		if a.code != 200 || json.Unmarshal([]byte(a.body), &st) != nil || st.Webhooks == nil {
			t.Fatalf("/status of enclave %d: %d %s", enclave, a.code, a.body)
		}
		n += st.Webhooks.Skipped[why]
	}
	return n
}
