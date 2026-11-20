package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rulewords/rulewordstest"
	"github.com/permaily-org/permaily/internal/services/accountrules"
	"github.com/permaily-org/permaily/internal/services/accountrules/accountrulestest"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// The rules routes (ADR 0040, pull request 6), against accountrulestest.Fake,
// rulewordstest.Fake and the mailboxes' fake and matcher over an in-memory
// store, mounted as the composition mounts them (MountRules inside AppHost,
// beside a catch-all of /api/ standing for AccountsAPI).

// rulesNotice is the rules' notice in force in these tests.
const rulesNotice = "2099-01-02"

type rulesApp struct {
	*queryApp
	rules *accountrulestest.Fake
	words *rulewordstest.Fake
	app   http.Handler
	// caught counts the requests the catch-all of /api/ was given.
	caught int
}

func newRulesApp(t *testing.T, edit func(*httpapi.RulesConfig)) *rulesApp {
	t.Helper()
	a := &rulesApp{queryApp: newQueryApp(t, nil), words: &rulewordstest.Fake{}}
	a.rules = &accountrulestest.Fake{Notice: rulesNotice, Now: func() time.Time { return a.now }}
	mine, previews := httpapi.MailboxesWithRulePreviews(a.cfg)
	cfg := httpapi.RulesConfig{
		Rules: a.rules, Words: a.words, Standing: a.fake.Accounts, Previews: previews, Sessions: a.cfg.Sessions,
		Log: a.cfg.Log, Notice: rulesNotice, Now: func() time.Time { return a.now },
	}
	if edit != nil {
		edit(&cfg)
	}
	app := httpapi.NewAppMux()
	app.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		a.caught++
		w.WriteHeader(http.StatusTeapot)
	}))
	app.Handle("/api/v1/mailboxes/", mine)
	httpapi.MountRules(app, cfg)
	a.app = httpapi.AppHost(app, mine)
	return a
}

func (a *rulesApp) do(cookie, method, path, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	return serve(a.app, cookie, method, path, body)
}

// draft is a hold of mail whose subject holds value, named name.
func draft(name, value string) string {
	return `{"name":"` + name + `","criteria":[` + formulaOf(cond("subject", "contains", value)) + `],"effect":"hold","scope":{"to":"all"},"on":true}`
}

// checked is the fake's check of a draft's JSON.
func (a *rulesApp) checked(d string) contracts.RuleCheck {
	a.t.Helper()
	var c contracts.RuleCheck
	read(a.t, a.do("alice", "POST", "/api/v1/rules/check", `{"draft":`+d+`}`), 200, &c)
	return c
}

// saveBody is a RuleSave of a draft's JSON at the rules' version.
func saveBody(c contracts.RuleCheck, d string, version uint64, notice string) string {
	return fmt.Sprintf(`{"draft_id":%q,"draft":%s,"version":%d,"notice":%q}`, c.DraftID, d, version, notice)
}

func (a *rulesApp) version(account string) uint64 {
	a.t.Helper()
	l, err := a.rules.List(context.Background(), account)
	if err != nil {
		a.t.Fatal(err)
	}
	return l.Version
}

// rulesRoute is one route of the rules, with a body it takes.
type rulesRoute struct{ method, path, body string }

func (a *rulesApp) routes(id string) []rulesRoute {
	d := draft("Bank", marker)
	return []rulesRoute{
		{"GET", "/api/v1/rules", ""},
		{"POST", "/api/v1/rules/check", `{"draft":` + d + `}`},
		{"POST", "/api/v1/rules/words", `{"words":"hold mail about ` + marker + `","notice":"` + rulesNotice + `"}`},
		{"POST", "/api/v1/rules", `{"draft_id":"rdr_x","draft":` + d + `,"version":0}`},
		{"PUT", "/api/v1/rules/" + id, `{"draft_id":"rdr_x","draft":` + d + `,"version":0}`},
		{"POST", "/api/v1/rules/" + id + "/delete", `{"version":0}`},
		{"POST", "/api/v1/rules/default", `{"on":false,"version":0}`},
		{"POST", "/api/v1/rules/preview", `{"draft":` + d + `,"mailbox":"mbx_x"}`},
		{"POST", "/api/v1/account/rules-notice", `{"version":"` + rulesNotice + `"}`},
	}
}

// U27. Every route answers the app alone: another site, another origin and
// a form are refused before anything (AppHost and fromTheApp); without a
// session, with one that stands for nothing, or for an account blocked or
// gone, 401; none of them reaches the service, the writer or the catch-all
// of /api/ (MountRules takes the rules' notice from AccountsAPI's).
func TestU27TheRulesRoutesAnswerTheAppAlone(t *testing.T) {
	a := newRulesApp(t, nil)
	a.fake.Accounts.Block(bob)
	for _, rt := range a.routes("rul_1") {
		if rt.method != "GET" {
			for _, site := range []string{"cross-site", "same-site"} {
				r := httptest.NewRequest(rt.method, "https://"+appHost+rt.path, strings.NewReader(rt.body))
				r.Header.Set("Origin", appOrigin)
				r.Header.Set("Sec-Fetch-Site", site)
				r.Header.Set("Content-Type", "application/json")
				r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "alice"})
				rec := httptest.NewRecorder()
				a.app.ServeHTTP(rec, r)
				refusedAsNotTheApp(t, rt.path+" "+site, rec)
			}
			r := httptest.NewRequest(rt.method, "https://"+appHost+rt.path, strings.NewReader(rt.body))
			r.Header.Set("Origin", "https://vendor.example")
			r.Header.Set("Content-Type", "application/json")
			r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "alice"})
			rec := httptest.NewRecorder()
			a.app.ServeHTTP(rec, r)
			want(t, rec, 403, "forbidden")
			r = httptest.NewRequest(rt.method, "https://"+appHost+rt.path, strings.NewReader(rt.body))
			r.Header.Set("Origin", appOrigin)
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "alice"})
			rec = httptest.NewRecorder()
			a.app.ServeHTTP(rec, r)
			want(t, rec, 415, "invalid_body")
		}
		for _, cookie := range []string{"", "nobody", "bob"} {
			want(t, a.do(cookie, rt.method, rt.path, rt.body), 401, "unauthorized")
		}
	}
	if len(a.rules.Changes) != 0 || len(a.words.Calls) != 0 || a.caught != 0 || len(a.matcher.asked()) != 0 {
		t.Fatalf("changes %v, words %v, caught %d, matched %v", a.rules.Changes, a.words.Calls, a.caught, a.matcher.asked())
	}
	// Methods and paths of the rules: anything else is refused, never the
	// catch-all's.
	for _, c := range []struct {
		method, path string
		status       int
		allow        string
	}{
		{"DELETE", "/api/v1/rules", 405, "GET, POST"}, {"GET", "/api/v1/rules/check", 405, "POST"},
		{"GET", "/api/v1/rules/words", 405, "POST"}, {"GET", "/api/v1/rules/default", 405, "POST"},
		{"GET", "/api/v1/rules/preview", 405, "POST"}, {"GET", "/api/v1/rules/rul_1", 405, "PUT"},
		{"DELETE", "/api/v1/rules/rul_1", 405, "PUT"}, {"GET", "/api/v1/rules/rul_1/delete", 405, "POST"},
		{"GET", "/api/v1/account/rules-notice", 405, "POST"},
		{"POST", "/api/v1/rules/rul_1/other", 404, ""}, {"POST", "/api/v1/rules/rul_1/delete/x", 404, ""},
	} {
		rec := a.do("alice", c.method, c.path, `{}`)
		if rec.Code != c.status || rec.Header().Get("Allow") != c.allow {
			t.Errorf("%s %s: %d, Allow %q", c.method, c.path, rec.Code, rec.Header().Get("Allow"))
		}
	}
	if a.caught != 0 {
		t.Fatalf("the catch-all was given %d", a.caught)
	}
}

// U27. The list and a check are allowed in every standing (the person's
// rules are read-only under unagreed terms, and readable while the account
// closes); every change, words and the notice are account_closing while it
// closes and terms_needed while the terms in force are not agreed, before
// the body or the proof are read, and nothing is changed.
func TestU27TheRulesUnderEachStanding(t *testing.T) {
	a := newRulesApp(t, nil)
	box := a.fake.Add(alice, "Mine")
	d := draft("Bank", "invoice")
	for _, c := range []struct {
		st   mailboxes.Standing
		code string
	}{
		{mailboxes.Standing{Closing: true, TermsAgreed: true}, "account_closing"},
		{mailboxes.Standing{Closing: true}, "account_closing"},
		{mailboxes.Standing{}, "terms_needed"},
	} {
		a.fake.Accounts.Set(alice, c.st)
		var l contracts.RuleList
		read(t, a.do("alice", "GET", "/api/v1/rules", ""), 200, &l)
		var ch contracts.RuleCheck
		read(t, a.do("alice", "POST", "/api/v1/rules/check", `{"draft":`+d+`}`), 200, &ch)
		for _, rt := range a.routes("rul_1") {
			if rt.method == "GET" || strings.HasSuffix(rt.path, "/check") || strings.HasSuffix(rt.path, "/preview") {
				continue
			}
			status := 400
			if c.code == "account_closing" {
				status = 409
			}
			want(t, a.do("alice", rt.method, rt.path, "nonsense"), status, c.code)
			want(t, a.do("alice-stale", rt.method, rt.path, rt.body), status, c.code)
		}
		// A preview is a search's standing: a closing account may, one
		// that has not agreed may not.
		rec := a.do("alice", "POST", "/api/v1/rules/preview", `{"draft":`+d+`,"mailbox":"`+box.ID+`"}`)
		if c.st.TermsAgreed {
			want(t, rec, 200, "")
		} else {
			want(t, rec, 400, "terms_needed")
		}
	}
	if len(a.rules.Changes) != 0 || len(a.words.Calls) != 0 {
		t.Fatalf("changes %v, words %v", a.rules.Changes, a.words.Calls)
	}
}

// U27. Changes: each needs a fresh proof, asked before the service; then
// the service's answers and refusals as the contract says (a rule made,
// changed, its switch, deleted with 204, the default turned off, the
// notice agreed; rules_changed and rule_changed 409, not_found 404,
// rules_notice_needed and invalid_rule 400 with its reason).
func TestU27ChangingRules(t *testing.T) {
	a := newRulesApp(t, nil)
	d := draft("Bank", "invoice")
	c := a.checked(d)
	if !strings.HasPrefix(c.DraftID, "rdr_") || c.Draft.Name != "Bank" {
		t.Fatalf("%+v", c)
	}
	// Without a fresh proof: refused here, before the service.
	for _, rt := range []rulesRoute{
		{"POST", "/api/v1/rules", saveBody(c, d, 0, "")}, {"PUT", "/api/v1/rules/rul_1", saveBody(c, d, 0, "")},
		{"POST", "/api/v1/rules/rul_1/delete", `{"version":0}`}, {"POST", "/api/v1/rules/default", `{"on":false,"version":0}`},
		{"POST", "/api/v1/account/rules-notice", `{"version":"` + rulesNotice + `"}`},
	} {
		want(t, a.do("alice-stale", rt.method, rt.path, rt.body), 403, "confirmation_needed")
	}
	if len(a.rules.Changes) != 0 {
		t.Fatalf("changes %v", a.rules.Changes)
	}
	// Made, at the version read.
	var made contracts.Rule
	read(t, a.do("alice", "POST", "/api/v1/rules", saveBody(c, d, a.version(alice), "")), 200, &made)
	if made.Check.DraftID != c.DraftID || made.Version != 1 || !strings.HasPrefix(made.ID, "rul_") {
		t.Fatalf("%+v", made)
	}
	// At a version read before: rules_changed; a draft not the one checked:
	// rule_changed; a rule not the account's: not_found.
	want(t, a.do("alice", "POST", "/api/v1/rules", saveBody(c, d, 0, "")), 409, "rules_changed")
	other := c
	other.DraftID = "rdr_other"
	want(t, a.do("alice", "POST", "/api/v1/rules", saveBody(other, d, a.version(alice), "")), 409, "rule_changed")
	want(t, a.do("alice", "PUT", "/api/v1/rules/rul_none", saveBody(c, d, a.version(alice), "")), 404, "not_found")
	want(t, a.do("bob", "PUT", "/api/v1/rules/"+made.ID, saveBody(c, d, 0, "")), 404, "not_found")
	// Its switch off: the same version.
	off := strings.Replace(d, `"on":true`, `"on":false`, 1)
	co := a.checked(off)
	var changed contracts.Rule
	read(t, a.do("alice", "PUT", "/api/v1/rules/"+made.ID, saveBody(co, off, a.version(alice), "")), 200, &changed)
	if changed.ID != made.ID || changed.Version != 1 || changed.Check.Draft.On {
		t.Fatalf("%+v", changed)
	}
	// A draft a model decides any of, before the notice is agreed.
	about := `{"name":"Health","criteria":[` + formulaOf(cond("message", "about", "health")) + `],"effect":"hold","scope":{"to":"all"},"on":true}`
	ca := a.checked(about)
	if !ca.Model {
		t.Fatalf("%+v", ca)
	}
	want(t, a.do("alice", "POST", "/api/v1/rules", saveBody(ca, about, a.version(alice), rulesNotice)), 400, "rules_notice_needed")
	// The notice: another version refused; the one in force agreed.
	want(t, a.do("alice", "POST", "/api/v1/account/rules-notice", `{"version":"2020-01-01"}`), 400, "rules_notice_needed")
	var agreed contracts.RuleNoticeAgreement
	read(t, a.do("alice", "POST", "/api/v1/account/rules-notice", `{"version":"`+rulesNotice+`"}`), 200, &agreed)
	if agreed.Version != rulesNotice || !agreed.At.Equal(a.now) {
		t.Fatalf("%+v", agreed)
	}
	read(t, a.do("alice", "POST", "/api/v1/rules", saveBody(ca, about, a.version(alice), rulesNotice)), 200, &made)
	// The default off.
	var def contracts.RuleDefault
	read(t, a.do("alice", "POST", "/api/v1/rules/default", fmt.Sprintf(`{"on":false,"version":%d}`, a.version(alice))), 200, &def)
	if def.On || def.Rule.ID == "" {
		t.Fatalf("%+v", def)
	}
	// Deleted: 204, no body; then not_found.
	rec := a.do("alice", "POST", "/api/v1/rules/"+made.ID+"/delete", fmt.Sprintf(`{"version":%d}`, a.version(alice)))
	if rec.Code != 204 || rec.Body.Len() != 0 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %q %v", rec.Code, rec.Body, rec.Header())
	}
	want(t, a.do("alice", "POST", "/api/v1/rules/"+made.ID+"/delete", fmt.Sprintf(`{"version":%d}`, a.version(alice))), 404, "not_found")
	// The list says it all.
	var l contracts.RuleList
	read(t, a.do("alice", "GET", "/api/v1/rules", ""), 200, &l)
	if len(l.Rules) != 1 || l.Default.On || l.Notice == nil || l.Notice.Version != rulesNotice || l.Version != a.version(alice) || l.Max != contracts.MaxRules {
		t.Fatalf("%+v", l)
	}
	kinds := []string{}
	for _, ch := range a.rules.Changes {
		kinds = append(kinds, ch.Kind)
	}
	if got := strings.Join(kinds, " "); got != "made changed notice_agreed made default_off deleted" {
		t.Fatalf("changes %s", got)
	}
	// An invalid rule: its reason, alone, in the details.
	long := draft(strings.Repeat("n", contracts.MaxRuleNameRunes+1), "invoice")
	rec = a.do("alice", "POST", "/api/v1/rules/check", `{"draft":`+long+`}`)
	want(t, rec, 400, "invalid_rule")
	if !strings.Contains(rec.Body.String(), `"details":{"rule":"name_too_long"}`) {
		t.Fatalf("%s", rec.Body)
	}
	unscoped := strings.Replace(d, `"scope":{"to":"all"}`, `"scope":{"to":"nobody"}`, 1)
	rec = a.do("alice", "POST", "/api/v1/rules", saveBody(c, unscoped, a.version(alice), ""))
	want(t, rec, 400, "invalid_rule")
	if !strings.Contains(rec.Body.String(), `"details":{"rule":"unknown_scope"}`) {
		t.Fatalf("%s", rec.Body)
	}
}

// U27. Bodies are read strictly, before the service is asked: one JSON
// object of the route's members, each once and written as the contract
// writes it, at every depth, with nothing after it, within the bound.
func TestU27RulesBodiesAreReadStrictly(t *testing.T) {
	a := newRulesApp(t, nil)
	d := draft("Bank", "invoice")
	dup := `{"name":"Bank","name":"Other","criteria":[` + formulaOf(cond("subject", "contains", "x")) + `],"effect":"hold","scope":{"to":"all"},"on":true}`
	deepDup := strings.Replace(d, `"op":"contains"`, `"op":"contains","op":"is"`, 1)
	cased := strings.Replace(d, `"effect"`, `"Effect"`, 1)
	kelvin := strings.Replace(`{"draft":`+d+`}`, `"draft"`, "\"Kdraft\"", 1)
	for _, body := range []string{
		``, `[]`, `null`, `{"draft":` + d + `}{}`, `{"draft":` + d + `,"other":1}`, `{"draft":` + d + `,"draft":` + d + `}`,
		`{"draft":` + dup + `}`, `{"draft":` + deepDup + `}`, `{"draft":` + cased + `}`, kelvin, `{"Draft":` + d + `}`,
		`{"draft":` + strings.Replace(d, `"on":true`, `"on":"yes"`, 1) + `}`,
		`{"draft":` + d + `,"pad":"` + strings.Repeat("x", 4*contracts.MaxRuleBytes) + `"}`,
		`{"draft":` + strings.Repeat(`{"a":`, 20) + `1` + strings.Repeat(`}`, 20) + `}`,
	} {
		want(t, a.do("alice", "POST", "/api/v1/rules/check", body), 400, "invalid_body")
	}
	for _, rt := range []rulesRoute{
		{"POST", "/api/v1/rules", `{"draft_id":"rdr_x","draft":` + d + `,"version":-1}`},
		{"POST", "/api/v1/rules", `{"draft_id":"rdr_x","draft":` + d + `,"version":0,"version":1}`},
		{"PUT", "/api/v1/rules/rul_1", `{"draft_id":1,"draft":` + d + `,"version":0}`},
		{"POST", "/api/v1/rules/rul_1/delete", `{"version":"0"}`},
		{"POST", "/api/v1/rules/default", `{"on":1,"version":0}`},
		{"POST", "/api/v1/rules/words", `{"words":"x","notice":"` + rulesNotice + `","model":"other"}`},
		{"POST", "/api/v1/account/rules-notice", `{"version":"` + rulesNotice + `","at":"now"}`},
		{"POST", "/api/v1/rules/preview", `{"draft":` + d + `,"mailbox":"mbx_x","limit":"5"}`},
	} {
		want(t, a.do("alice", rt.method, rt.path, rt.body), 400, "invalid_body")
	}
	if len(a.rules.Changes) != 0 || len(a.words.Calls) != 0 || len(a.matcher.asked()) != 0 {
		t.Fatalf("changes %v, words %v, matched %v", a.rules.Changes, a.words.Calls, a.matcher.asked())
	}
}

// U27. Words: the rules' notice the screen showed must be the one in force
// and the account's, before anything is sent; then the writer's answer as
// the account's check made it, for every connection and on whatever the
// writer said; a draft the check refuses is rule_words_unusable; the
// writer's refusals as they are (model_unavailable 503 with Retry-After,
// too_many_requests 429 past the day's calls); no writer is
// model_unavailable.
func TestU27WordsToARule(t *testing.T) {
	a := newRulesApp(t, nil)
	words := func(w, notice string) *httptest.ResponseRecorder {
		return a.do("alice", "POST", "/api/v1/rules/words", fmt.Sprintf(`{"words":%q,"notice":%q}`, w, notice))
	}
	want(t, words("hold my bank mail", rulesNotice), 400, "rules_notice_needed")
	want(t, words("hold my bank mail", "2020-01-01"), 400, "rules_notice_needed")
	if len(a.words.Calls) != 0 {
		t.Fatalf("words sent without the notice: %v", a.words.Calls)
	}
	read(t, a.do("alice", "POST", "/api/v1/account/rules-notice", `{"version":"`+rulesNotice+`"}`), 200, &contracts.RuleNoticeAgreement{})
	want(t, words("hold my bank mail", "2020-01-01"), 400, "rules_notice_needed")
	// The writer scoped its draft and turned it off: the proposal is for
	// every connection and on.
	a.words.Answer = func(string) contracts.RuleDraft {
		var d contracts.RuleDraft
		if err := json.Unmarshal([]byte(draft("Bank", "bank")), &d); err != nil {
			t.Fatal(err)
		}
		d.Scope, d.On = contracts.RuleScope{To: contracts.ScopeOnly, Clients: []string{"https://vendor.example/c.json"}}, false
		return d
	}
	var p contracts.RuleProposal
	read(t, words("hold my bank mail", rulesNotice), 200, &p)
	if p.Model != "played" || p.Prompt != "w0-played" || p.Check.Draft.Scope.To != contracts.ScopeAll || len(p.Check.Draft.Scope.Clients) != 0 ||
		!p.Check.Draft.On || p.Check.Draft.Name != "Bank" || !strings.HasPrefix(p.Check.DraftID, "rdr_") {
		t.Fatalf("%+v", p)
	}
	// A draft the account's check refuses: unusable, nothing proposed.
	a.words.Answer = func(string) contracts.RuleDraft {
		return contracts.RuleDraft{Name: strings.Repeat("n", 61), Effect: contracts.EffectHold}
	}
	want(t, words("hold my bank mail", rulesNotice), 400, "rule_words_unusable")
	a.words.Answer = nil
	for err, status := range map[error]int{
		contracts.ErrModelUnavailable: 503, contracts.ErrRuleWordsUnusable: 400,
		fmt.Errorf("wrapped: %w", contracts.ErrModelUnavailable): 503,
	} {
		a.words.Err = err
		known, _ := errors.AsType[*contracts.Error](err)
		rec := words("hold my bank mail", rulesNotice)
		want(t, rec, status, known.Code)
		if status == 503 && rec.Header().Get("Retry-After") == "" {
			t.Fatalf("no Retry-After: %v", rec.Header())
		}
	}
	a.words.Err = nil
	for a.words.Calls[alice] < 30 {
		read(t, words("hold my bank mail", rulesNotice), 200, &p)
	}
	want(t, words("hold my bank mail", rulesNotice), 429, "too_many_requests")
	// Without a writer: the model is unavailable, and the editor builds the
	// rule from its conditions.
	b := newRulesApp(t, func(c *httpapi.RulesConfig) { c.Words = nil })
	read(t, b.do("alice", "POST", "/api/v1/account/rules-notice", `{"version":"`+rulesNotice+`"}`), 200, &contracts.RuleNoticeAgreement{})
	want(t, b.do("alice", "POST", "/api/v1/rules/words", `{"words":"x","notice":"`+rulesNotice+`"}`), 503, "model_unavailable")
}

// U27. A record that cannot be read now is 503 busy, asked again later,
// logged by its kind alone; a failure of the service's own is 500, logged by
// its kind alone; a composition without the standing answers nothing.
func TestU27RulesThatCannotBeRead(t *testing.T) {
	a := newRulesApp(t, nil)
	a.rules.Fail(alice, errors.New("the record of "+marker+" is forked"))
	for _, rt := range []rulesRoute{
		{"GET", "/api/v1/rules", ""},
		{"POST", "/api/v1/rules/default", `{"on":false,"version":0}`},
		{"POST", "/api/v1/account/rules-notice", `{"version":"` + rulesNotice + `"}`},
	} {
		rec := a.do("alice", rt.method, rt.path, rt.body)
		want(t, rec, 503, "busy")
		if rec.Header().Get("Retry-After") != "5" {
			t.Fatalf("%s: %v", rt.path, rec.Header())
		}
	}
	if out := a.logs.String(); !strings.Contains(out, `"kind":"unavailable"`) || strings.Contains(out, marker) || strings.Contains(out, alice) {
		t.Fatalf("log: %s", out)
	}
	b := newRulesApp(t, func(c *httpapi.RulesConfig) { c.Standing = nil })
	want(t, b.do("alice", "GET", "/api/v1/rules", ""), 500, "internal")
	if b.rules.Calls != nil || strings.Contains(b.logs.String(), alice) {
		t.Fatalf("log: %s", b.logs)
	}
}

// U27. The rule editor's preview: a page of the mailbox's mail any of the
// draft's criteria apply to, newest first, its draft's identifier with it;
// the draft checked first, then the mailbox opened as a search opens it
// (another account's or none is not_found), the limit and the cursor; pages
// that follow one another skip nothing and repeat nothing; ask_model needs
// the rules' notice in force agreed; a removal's preview of one message is
// not served until the discloser's removals are (pull request 5).
func TestU27TheRulePreview(t *testing.T) {
	a := newRulesApp(t, nil)
	box := a.fake.Add(alice, "Mine")
	theirs := a.fake.Add(bob, "Theirs")
	raw := func(subject string) string {
		return "From: Ana <ana@example.org>\r\nTo: x@example.org\r\nSubject: " + subject + "\r\nContent-Type: text/plain\r\n\r\nHello\r\n"
	}
	var want3 []string // newest first
	for i := range 3 {
		inv := a.seedFrom(box.ID, raw(fmt.Sprintf("Invoice %d", i)))
		a.seedFrom(box.ID, raw("Something else"))
		rec := a.seedFrom(box.ID, raw(fmt.Sprintf("Receipt %d", i)))
		want3 = append([]string{rec, inv}, want3...)
	}
	two := `{"name":"Bills","criteria":[` + formulaOf(cond("subject", "contains", "invoice")) + `,` + formulaOf(cond("subject", "contains", "receipt")) +
		`],"effect":"hold","scope":{"to":"all"},"on":true}`
	c := a.checked(two)
	preview := func(cookie, body string) *httptest.ResponseRecorder {
		return a.do(cookie, "POST", "/api/v1/rules/preview", body)
	}
	var p contracts.RulePreviewPage
	read(t, preview("alice", `{"draft":`+two+`,"mailbox":"`+box.ID+`"}`), 200, &p)
	if p.DraftID != c.DraftID || p.Page.Next != "" || len(p.Page.Unknown) != 0 || len(p.Page.NeedsModel) != 0 || p.Page.Model != "" {
		t.Fatalf("%+v", p)
	}
	var got []string
	for _, m := range p.Page.Messages {
		got = append(got, m.ID)
	}
	if strings.Join(got, " ") != strings.Join(want3, " ") {
		t.Fatalf("matches %v, want %v", got, want3)
	}
	// Page by page, two at a time: the same messages, none twice.
	got, after := nil, ""
	for range 10 {
		body := `{"draft":` + two + `,"mailbox":"` + box.ID + `","limit":2`
		if after != "" {
			body += `,"after":"` + after + `"`
		}
		var page contracts.RulePreviewPage
		read(t, preview("alice", body+`}`), 200, &page)
		if len(page.Page.Messages) > 2 {
			t.Fatalf("%d on a page of 2", len(page.Page.Messages))
		}
		for _, m := range page.Page.Messages {
			got = append(got, m.ID)
		}
		if after = page.Page.Next; after == "" {
			break
		}
		if page.Page.SearchedTo.IsZero() {
			t.Fatalf("no searched_to: %+v", page.Page)
		}
	}
	if strings.Join(got, " ") != strings.Join(want3, " ") {
		t.Fatalf("paged %v, want %v", got, want3)
	}
	// Refusals, in order.
	want(t, preview("alice", `{"draft":`+draft(strings.Repeat("n", 61), "x")+`,"mailbox":"`+theirs.ID+`"}`), 400, "invalid_rule")
	want(t, preview("alice", `{"draft":`+two+`,"mailbox":"`+theirs.ID+`","limit":500}`), 404, "not_found")
	want(t, preview("alice", `{"draft":`+two+`,"mailbox":"mbx_`+strings.Repeat("z", 26)+`"}`), 404, "not_found")
	lost := a.fake.Add(alice, "Lost")
	a.fake.MakeLost(lost.ID)
	want(t, preview("alice", `{"draft":`+two+`,"mailbox":"`+lost.ID+`","limit":500}`), 409, "mailbox_unreadable")
	want(t, preview("alice", `{"draft":`+two+`,"mailbox":"`+box.ID+`","limit":101}`), 400, "invalid_limit")
	want(t, preview("alice", `{"draft":`+two+`,"mailbox":"`+box.ID+`","limit":-1}`), 400, "invalid_limit")
	want(t, preview("alice", `{"draft":`+two+`,"mailbox":"`+box.ID+`","after":"x"}`), 400, "invalid_cursor")
	// The model: the rules' notice first; agreed, it is asked as the
	// owner's previews ask it (here no key: off, and nothing needs it).
	want(t, preview("alice", `{"draft":`+two+`,"mailbox":"`+box.ID+`","ask_model":true}`), 400, "rules_notice_needed")
	read(t, a.do("alice", "POST", "/api/v1/account/rules-notice", `{"version":"`+rulesNotice+`"}`), 200, &contracts.RuleNoticeAgreement{})
	about := `{"name":"Bills","criteria":[` + formulaOf(cond("message", "about", "bills")) + `],"effect":"hold","scope":{"to":"all"},"on":true}`
	read(t, preview("alice", `{"draft":`+about+`,"mailbox":"`+box.ID+`","ask_model":true}`), 200, &p)
	if p.Page.Model != contracts.MatchModelOff || len(p.Page.NeedsModel) != 9 || len(p.Page.Messages) != 0 {
		t.Fatalf("%+v", p.Page)
	}
	// One message as the removal would leave it: not yet.
	want(t, preview("alice", `{"draft":`+two+`,"mailbox":"`+box.ID+`","message":"`+want3[0]+`"}`), 404, "not_found")
	// Without previews, the route is not there.
	b := newRulesApp(t, func(c *httpapi.RulesConfig) { c.Previews = nil })
	want(t, b.do("alice", "POST", "/api/v1/rules/preview", `{"draft":`+two+`,"mailbox":"`+box.ID+`"}`), 404, "not_found")
}

// U28. No line any rules route logs holds the person's words, a rule's
// name, a criteria's value, a topic, a sentence, an account or a count of
// its rules: every route, its successes and its refusals, a service's
// failure whose words hold them, and a record that cannot be read.
func TestU28TheRulesRoutesLogNothingOfARule(t *testing.T) {
	a := newRulesApp(t, nil)
	box := a.fake.Add(alice, "Mine")
	a.seedFrom(box.ID, plainRaw)
	read(t, a.do("alice", "POST", "/api/v1/account/rules-notice", `{"version":"`+rulesNotice+`"}`), 200, &contracts.RuleNoticeAgreement{})
	topic := marker + "topic"
	d := `{"name":"` + marker + `name","criteria":[` + formulaOf(cond("subject", "contains", marker+"value")) +
		`],"effect":"remove","removal":{"topics":["` + topic + `"],"shapes":["phone_number"]},"scope":{"to":"all"},"on":true}`
	c := a.checked(d)
	var made contracts.Rule
	read(t, a.do("alice", "POST", "/api/v1/rules", saveBody(c, d, a.version(alice), rulesNotice)), 200, &made)
	a.words.Answer = func(w string) contracts.RuleDraft {
		var out contracts.RuleDraft
		_ = json.Unmarshal([]byte(d), &out)
		return out
	}
	calls := []rulesRoute{
		{"GET", "/api/v1/rules", ""},
		{"POST", "/api/v1/rules/check", `{"draft":` + d + `}`},
		{"POST", "/api/v1/rules/words", `{"words":"remove ` + marker + `words","notice":"` + rulesNotice + `"}`},
		{"PUT", "/api/v1/rules/" + made.ID, saveBody(c, d, 0, rulesNotice)},
		{"POST", "/api/v1/rules/preview", `{"draft":` + d + `,"mailbox":"` + box.ID + `","ask_model":true}`},
		{"POST", "/api/v1/rules/default", `{"on":false,"version":0}`},
		{"POST", "/api/v1/rules/" + made.ID + "/delete", `{"version":0}`},
		{"POST", "/api/v1/rules/check", `{"draft":` + strings.Replace(d, `"remove"`, `"`+marker+`effect"`, 1) + `}`},
		{"POST", "/api/v1/rules/check", `{"draft":` + d + `,"` + marker + `":1}`},
	}
	for _, rt := range calls {
		a.do("alice", rt.method, rt.path, rt.body)
		a.do("alice-stale", rt.method, rt.path, rt.body)
	}
	a.words.Err = errors.New("the model refused " + marker + "words")
	a.do("alice", "POST", "/api/v1/rules/words", `{"words":"remove `+marker+`words","notice":"`+rulesNotice+`"}`)
	a.rules.Fail(alice, errors.New("the record holding "+marker+"name is forked"))
	for _, rt := range calls {
		a.do("alice", rt.method, rt.path, rt.body)
	}
	out := a.logs.String()
	if out == "" {
		t.Fatal("nothing was logged: the failures above should have been, by their kind")
	}
	for _, secret := range []string{marker, alice, box.ID, made.ID, c.DraftID, "phone_number", rulesNotice} {
		if strings.Contains(out, secret) {
			t.Fatalf("a log line holds %q:\n%s", secret, out)
		}
	}
}

// problemRules is the fake whose check refuses every draft with a
// formula's problem, as the service's does for a criteria Parse refuses.
type problemRules struct{ *accountrulestest.Fake }

func (problemRules) Check(context.Context, string, contracts.RuleDraft) (*contracts.RuleCheck, error) {
	_, err := query.Parse([]byte(`{"v":2}`), nil)
	return nil, err
}

// U27. A proposal whose criteria the account's check refuses as a formula
// is rule_words_unusable too, never the formula's problem; without a
// matcher there are no previews; the defaults of a composition that sets
// neither a deadline nor a clock; a member without a name is not one.
func TestU27TheRulesRoutesEdges(t *testing.T) {
	a := newRulesApp(t, func(c *httpapi.RulesConfig) {
		c.Rules = problemRules{c.Rules.(*accountrulestest.Fake)}
		c.Now, c.Deadline = nil, 0
	})
	want(t, a.do("alice", "POST", "/api/v1/rules/check", `{"draft":`+draft("Bank", "x")+`}`), 400, "invalid_formula")
	want(t, a.do("alice", "POST", "/api/v1/rules/check", `{"":1}`), 400, "invalid_body")
	if _, err := a.rules.AgreeNotice(context.Background(), accountrules.Person{Account: alice, ConfirmedUntil: a.now.Add(time.Hour)}, rulesNotice); err != nil {
		t.Fatal(err)
	}
	want(t, a.do("alice", "POST", "/api/v1/rules/words", `{"words":"x","notice":"`+rulesNotice+`"}`), 400, "rule_words_unusable")
	cfg := a.cfg
	cfg.Matcher = nil
	if _, previews := httpapi.MailboxesWithRulePreviews(cfg); previews != nil {
		t.Fatal("previews without a matcher")
	}
}
