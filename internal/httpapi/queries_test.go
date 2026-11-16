package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/inboxtest"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/services/mailboxes/mailboxestest"
	"github.com/permaily-org/permaily/internal/store"
)

// The query routes (ADR 0027, "The routes", pull request 4): the check of a
// formula and a mailbox's matches, on the fake mailboxes service and inbox's
// matcher over an in-memory store.

// marker is in the values of the formulas these tests send: no answer and
// no log may hold it.
const marker = "zq7f3a"

// languages is the account's language, as accounts.Service's Me says it.
type languages map[string]string

func (l languages) Me(_ context.Context, who *accounts.Authenticated) (*contracts.Me, error) {
	lang, ok := l[who.AccountID]
	if !ok {
		return nil, contracts.ErrUnauthorized
	}
	return &contracts.Me{Account: contracts.Account{ID: who.AccountID, Language: lang}}, nil
}

// watchedMatcher records which mailboxes the matcher was asked about.
type watchedMatcher struct {
	inbox.Matcher
	mu   sync.Mutex
	read []string
}

func (w *watchedMatcher) Match(ctx context.Context, b inbox.Box, fs []*query.Canonical, before string, limit int) (*contracts.MatchPage, error) {
	w.mu.Lock()
	w.read = append(w.read, b.MailboxID)
	w.mu.Unlock()
	return w.Matcher.Match(ctx, b, fs, before, limit)
}

func (w *watchedMatcher) asked() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.read)
}

// queryApp is inboxApp with the matcher on its store, and the check route on
// its fake mailboxes.
type queryApp struct {
	*inboxApp
	matcher *watchedMatcher
	state   httpapi.APIState
	check   http.Handler
}

func newQueryApp(t *testing.T, edit func(*httpapi.MailboxesConfig)) *queryApp {
	t.Helper()
	a := &queryApp{inboxApp: newInboxApp(t, edit)}
	a.matcher = &watchedMatcher{Matcher: inbox.NewMatcher(a.mem, query.Eval)}
	a.cfg.Matcher = a.matcher
	a.h, a.state = httpapi.MailboxesAPIWithState(a.cfg)
	a.check = httpapi.QueriesAPI(httpapi.QueriesConfig{
		Sessions: a.cfg.Sessions, Mailboxes: a.svc, Accounts: languages{alice: "es", bob: "en"}, Log: a.cfg.Log,
	})
	return a
}

func (a *queryApp) checkAs(cookie, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	return serve(a.check, cookie, "POST", "/api/v1/queries/check", body)
}

func (a *queryApp) matches(cookie, mailbox, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	return a.call(cookie, "POST", "/api/v1/mailboxes/"+mailbox+"/matches", body)
}

// seedFrom stores raw in a mailbox as delivery does (inbox.Parse's meta and
// entry), received minutes ago.
func (a *queryApp) seedFrom(mailbox, raw string) string {
	a.t.Helper()
	a.n++
	at := a.now.Add(-time.Duration(1000-a.n) * time.Minute)
	id := fmt.Sprintf("%016x%016x", at.UnixNano(), a.n)
	m := inboxtest.FromRaw(context.Background(), id, at, 30, []byte(raw))
	if err := inboxtest.Seed(context.Background(), a.mem, a.mem, inbox.Box{MailboxID: mailbox, Key: mailboxestest.Key(mailbox)}, m); err != nil {
		a.t.Fatal(err)
	}
	return id
}

func formulaOf(conditions ...string) string {
	return `{"v":1,"all":[{"any":[` + strings.Join(conditions, ",") + `]}]}`
}

func cond(v, op, value string) string {
	return `{"var":"` + v + `","op":"` + op + `","value":"` + value + `"}`
}

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) contracts.FormulaProblem {
	t.Helper()
	var e httpapi.APIError
	if rec.Code != 400 || json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Error.Code != "invalid_formula" || e.Error.Message != contracts.ErrInvalidFormula.Message {
		t.Fatalf("%d %s is not invalid_formula", rec.Code, rec.Body)
	}
	return e.Error.Details.FormulaProblem
}

// Q34: the check answers the canonical form, its identifier and its
// sentence, naming the account's mailboxes; English for an account of
// another language while English is the only table.
func TestQ34TheCheck(t *testing.T) {
	a := newQueryApp(t, nil)
	work := a.fake.Add(alice, `Work ”, or that arrived at`)
	a.fake.Add(alice, "")
	f := `{"v":1,"all":[{"any":[` + cond("mailbox", "is", work.ID) + `]},{"any":[` + cond("subject", "contains", "INVOICE") + "," + cond("subject", "contains", "invoices") + `]}]}`
	rec := a.checkAs("alice", `{"formula":`+f+`}`)
	var got contracts.FormulaCheck
	read(t, rec, 200, &got)
	want, err := query.Parse([]byte(f), []string{work.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || !strings.HasPrefix(got.ID, "qry_") || got.Model {
		t.Fatalf("%+v", got)
	}
	canonical, _ := json.Marshal(got.Formula)
	wanted, _ := json.Marshal(want.Formula)
	if string(canonical) != string(wanted) || !strings.Contains(string(canonical), `"value":"invoice"`) || strings.Contains(string(canonical), "invoices") {
		t.Fatalf("canonical %s, want %s", canonical, wanted)
	}
	// Alice's account says Spanish: the sentence is English, as Render
	// writes it for her mailboxes.
	list, _ := a.fake.List(context.Background(), mailboxes.Caller{Account: alice})
	names := map[string]query.MailboxName{}
	for _, m := range list.Mailboxes {
		names[m.ID] = query.MailboxName{Label: m.Label, Address: m.Address}
	}
	sentence := query.Render(want, query.Context{Lang: "es", Mailboxes: names})
	if got.Sentence.Lang != "en" || got.Sentence.Text != sentence.Text || !strings.Contains(got.Sentence.Text, `“Work \”, or that arrived at”`) {
		t.Fatalf("sentence %+v\nwant %+v", got.Sentence, sentence)
	}
	if h := rec.Header(); h.Get("Content-Type") != "application/json" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("%v", h)
	}

	// A model's condition says so; a disconnected mailbox of hers is not one
	// of hers any more; one of Bob's is no mailbox of hers.
	read(t, a.checkAs("alice", `{"formula":`+formulaOf(cond("message", "about", "trips"))+`}`), 200, &got)
	if !got.Model || !slices.ContainsFunc(got.Sentence.Lines[0].Parts, func(p contracts.SentencePart) bool { return p.Model }) {
		t.Fatalf("%+v", got)
	}
	bobs := a.fake.Add(bob, "Bob's")
	if p := problemOf(t, a.checkAs("alice", `{"formula":`+formulaOf(cond("mailbox", "is", bobs.ID))+`}`)); p.Reason != contracts.FormulaUnknownMailbox || p.At != "all[0].any[0].value" {
		t.Fatalf("%+v", p)
	}
	read(t, a.checkAs("bob", `{"formula":`+formulaOf(cond("mailbox", "is", bobs.ID))+`}`), 200, &got)
	// An account without mailboxes names none: every mailbox is unknown.
	none := newQueryApp(t, nil)
	if p := problemOf(t, none.checkAs("alice", `{"formula":`+formulaOf(cond("mailbox", "is", work.ID))+`}`)); p.Reason != contracts.FormulaUnknownMailbox {
		t.Fatalf("%+v", p)
	}
	// Every message, and every standing an account may be in.
	for _, standing := range []mailboxes.Standing{{TermsAgreed: true}, {Closing: true, TermsAgreed: true}, {}, {Closing: true}} {
		a.fake.Accounts.Set(alice, standing)
		read(t, a.checkAs("alice", `{"formula":{"v":1,"all":[]}}`), 200, &got)
		if got.Sentence.Text != "All mail." || len(got.Formula.All) != 0 {
			t.Fatalf("%+v: %+v", standing, got)
		}
	}
}

// Q34: every reason a formula is refused for, with its details: the path
// and the reason query.Parse gives, the fixed message, nothing of a value.
func TestQ34EveryReasonWithItsDetails(t *testing.T) {
	a := newQueryApp(t, nil)
	mine := a.fake.Add(alice, "Mine")
	theirs := a.fake.Add(bob, "Theirs")
	many := func(n int, c string) string { return strings.TrimSuffix(strings.Repeat(c+",", n), ",") }
	groups := func(n int) string {
		return `{"v":1,"all":[` + many(n, `{"any":[`+cond("subject", "contains", marker)+`]}`) + `]}`
	}
	cases := map[contracts.FormulaReason]string{
		contracts.FormulaNotJSON:            formulaOf(cond("subject", "contains", marker+"\xff")),
		contracts.FormulaTooLarge:           `{"v":1,` + strings.Repeat(" ", contracts.MaxFormulaBytes) + `"all":[]}`,
		contracts.FormulaUnknownField:       `{"v":1,"all":[],"` + marker + `":1}`,
		contracts.FormulaDuplicateField:     `{"v":1,"v":1,"all":[]}`,
		contracts.FormulaWrongType:          formulaOf(`{"var":"subject","op":"contains","value":7}`),
		contracts.FormulaUnsupportedVersion: `{"v":2,"all":[]}`,
		contracts.FormulaTooManyGroups:      groups(contracts.MaxGroups + 1),
		contracts.FormulaTooManyConditions:  formulaOf(many(contracts.MaxGroupConditions+1, cond("subject", "contains", marker))),
		contracts.FormulaTooManyModel:       `{"v":1,"all":[` + many(contracts.MaxModelConditions+1, `{"any":[`+cond("message", "about", marker)+`]}`) + `]}`,
		contracts.FormulaEmptyGroup:         `{"v":1,"all":[{"any":[]}]}`,
		contracts.FormulaUnknownVariable:    formulaOf(cond("date", "is", marker)),
		contracts.FormulaUnknownOperator:    formulaOf(cond("subject", "like", marker)),
		contracts.FormulaOperatorNotAllowed: formulaOf(cond("subject", "is", marker)),
		contracts.FormulaMissingValue:       formulaOf(`{"var":"subject","op":"contains"}`),
		contracts.FormulaUnexpectedValue:    formulaOf(cond("attachments", "exists", marker)),
		contracts.FormulaInvalidValue:       formulaOf(cond("received", "since", marker)),
		contracts.FormulaValueTooLong:       formulaOf(cond("subject", "contains", marker+strings.Repeat("x", contracts.MaxTextRunes))),
		contracts.FormulaConfusableValue:    formulaOf(cond("subject", "contains", "invоice"+marker)),
		contracts.FormulaUnknownMailbox:     formulaOf(cond("mailbox", "is", theirs.ID)),
		contracts.FormulaNeverMatches: `{"v":1,"all":[{"any":[` + cond("received", "since", "2026-03-01T00:00:00Z") + `]},{"any":[` +
			cond("received", "before", "2026-02-01T00:00:00Z") + `]}]}`,
	}
	reasons := []contracts.FormulaReason{
		contracts.FormulaNotJSON, contracts.FormulaTooLarge, contracts.FormulaUnknownField, contracts.FormulaDuplicateField,
		contracts.FormulaWrongType, contracts.FormulaUnsupportedVersion, contracts.FormulaTooManyGroups, contracts.FormulaTooManyConditions,
		contracts.FormulaTooManyModel, contracts.FormulaEmptyGroup, contracts.FormulaUnknownVariable, contracts.FormulaUnknownOperator,
		contracts.FormulaOperatorNotAllowed, contracts.FormulaMissingValue, contracts.FormulaUnexpectedValue, contracts.FormulaInvalidValue,
		contracts.FormulaValueTooLong, contracts.FormulaConfusableValue, contracts.FormulaUnknownMailbox, contracts.FormulaNeverMatches,
	}
	if len(cases) != len(reasons) {
		t.Fatalf("%d cases for %d reasons", len(cases), len(reasons))
	}
	for _, reason := range reasons {
		f := cases[reason]
		_, err := query.Parse([]byte(f), []string{mine.ID})
		parsed, ok := err.(*query.Problem) //nolint:errorlint // Parse answers a *Problem itself
		if !ok || parsed.Reason != reason {
			t.Fatalf("%s: the case gives %v", reason, err)
		}
		for name, rec := range map[string]*httptest.ResponseRecorder{
			"check":   a.checkAs("alice", `{"formula":`+f+`}`),
			"matches": a.matches("alice", mine.ID, `{"formula":`+f+`,"limit":20}`),
		} {
			if p := problemOf(t, rec); p != parsed.FormulaProblem {
				t.Errorf("%s, %s: %+v, want %+v", name, reason, p, parsed.FormulaProblem)
			}
			if strings.Contains(rec.Body.String(), marker) || strings.Contains(rec.Body.String(), theirs.ID) {
				t.Errorf("%s, %s: the answer echoes a value: %s", name, reason, rec.Body)
			}
		}
	}
	if strings.Contains(a.logs.String(), marker) || a.logs.Len() != 0 {
		t.Fatalf("a refusal is logged: %s", a.logs)
	}
	if got := a.matcher.asked(); len(got) != 0 {
		t.Fatalf("a refused formula was matched: %v", got)
	}
}

// Q34: the check's order of failures, and its body.
func TestQ34TheChecksOrderAndBody(t *testing.T) {
	a := newQueryApp(t, nil)
	a.fake.Add(alice, "Mine")
	bad := `{"formula":{"v":2}}`
	want(t, a.checkAs("", bad), 401, "unauthorized")
	want(t, a.checkAs("nobody", "nonsense"), 401, "unauthorized")
	a.fake.Accounts.Block(bob)
	want(t, a.checkAs("bob", "nonsense"), 401, "unauthorized")
	for name, body := range map[string]string{
		"not JSON":             "nonsense",
		"not an object":        `[{"formula":{"v":1,"all":[]}}]`,
		"no formula":           `{}`,
		"another member":       `{"formula":{"v":1,"all":[]},"limit":3}`,
		"the formula twice":    `{"formula":{"v":1,"all":[]},"formula":{"v":2,"all":[]}}`,
		"spelled twice":        `{"formula":{"v":1,"all":[]},"formula":{"v":1,"all":[]}}`,
		"something after it":   `{"formula":{"v":1,"all":[]}} {}`,
		"cut":                  `{"formula":{"v":1,"all":[]}`,
		"a member that is not": `{"formula":}`,
		"past its bound":       `{"formula":"` + strings.Repeat("a", 2*contracts.MaxFormulaBytes) + `"}`,
		// encoding/json reads a name in any case: these would be one member there.
		"twice in another case": `{"formula":{"v":1,"all":[]},"Formula":{"v":2,"all":[]}}`,
		"in another case":       `{"FORMULA":{"v":1,"all":[]}}`,
	} {
		if rec := a.checkAs("alice", body); rec.Code != 400 || errorCode(t, rec) != "invalid_body" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// A null formula is the formula's to refuse, as Parse refuses it.
	_, err := query.Parse([]byte("null"), nil)
	if p := problemOf(t, a.checkAs("alice", `{"formula":null}`)); p != err.(*query.Problem).FormulaProblem { //nolint:errorlint // Parse answers a *Problem itself
		t.Fatalf("%+v", p)
	}
	rec := serve(a.check, "alice", "GET", "/api/v1/queries/check", "")
	want(t, rec, 405, "method_not_allowed")
	if rec.Header().Get("Allow") != "POST" {
		t.Fatalf("%v", rec.Header())
	}
	want(t, serve(a.check, "alice", "POST", "/api/v1/queries/other", "{}"), 404, "not_found")
	want(t, serve(a.check, "alice", "POST", "/api/v1/queries", "{}"), 404, "not_found")
	// Another site's form, and an account Me cannot read.
	r := httptest.NewRequest("POST", "https://app.permaily.test/api/v1/queries/check", strings.NewReader(`{"formula":{"v":1,"all":[]}}`))
	r.Header.Set("Origin", "https://evil.example")
	r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "alice"})
	w := httptest.NewRecorder()
	a.check.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("another origin: %d %s", w.Code, w.Body)
	}
	noLanguage := httpapi.QueriesAPI(httpapi.QueriesConfig{Sessions: a.cfg.Sessions, Mailboxes: a.svc, Accounts: languages{}, Log: a.cfg.Log})
	want(t, serve(noLanguage, "alice", "POST", "/api/v1/queries/check", `{"formula":{"v":1,"all":[]}}`), 401, "unauthorized")
	english := httpapi.QueriesAPI(httpapi.QueriesConfig{Sessions: a.cfg.Sessions, Mailboxes: a.svc, Log: a.cfg.Log})
	var got contracts.FormulaCheck
	read(t, serve(english, "alice", "POST", "/api/v1/queries/check", `{"formula":{"v":1,"all":[]}}`), 200, &got)
	if got.Sentence.Lang != "en" {
		t.Fatalf("%+v", got)
	}
	if a.logs.Len() != 0 {
		t.Fatalf("logged: %s", a.logs)
	}
}

// listFails is a mailboxes service whose list cannot be read: neither List
// nor the catalogue.
type listFails struct{ httpapi.MailboxesService }

func (listFails) List(context.Context, mailboxes.Caller) (*contracts.MailboxList, error) {
	return nil, fmt.Errorf("the table is away for %s", alice)
}

func (listFails) Catalogue(context.Context, mailboxes.Caller, bool) ([]mailboxes.Named, error) {
	return nil, fmt.Errorf("the table is away for %s", alice)
}

// A catalogue that cannot be read is the service's failure: logged without
// the account, and answered with nothing of its own.
func TestQueriesTheListFails(t *testing.T) {
	a := newQueryApp(t, func(c *httpapi.MailboxesConfig) {})
	box := a.fake.Add(alice, "Mine")
	h := httpapi.QueriesAPI(httpapi.QueriesConfig{Sessions: a.cfg.Sessions, Mailboxes: listFails{a.svc}, Log: a.cfg.Log})
	want(t, serve(h, "alice", "POST", "/api/v1/queries/check", `{"formula":{"v":1,"all":[]}}`), 500, "internal")
	a.cfg.Service = listFails{a.svc}
	a.cfg.Matcher = a.matcher
	matches := httpapi.MailboxesAPI(a.cfg)
	want(t, serve(matches, "alice", "POST", "/api/v1/mailboxes/"+box.ID+"/matches", `{"formula":{"v":1,"all":[]}}`), 500, "internal")
	if out := a.logs.String(); !strings.Contains(out, "internal error") || strings.Contains(out, alice) {
		t.Fatalf("%s", out)
	}
}

// On the real service: the formula's mailboxes come from the account's
// catalogue, which reads no mailbox's confirmations. Mailbox B's cannot be
// read (the list, which shows them, fails), yet A's matches and a check
// naming A answer, as A's search does.
func TestTheQueryRoutesDoNotReadAnotherMailboxsConfirmations(t *testing.T) {
	w := newRealWorld(t)
	i := w.instance()
	boxA, boxB := i.make("alice", "Alpha"), i.make("alice", "Beta")
	log := slog.New(slog.NewJSONHandler(w.logs, nil))
	until := w.clock().Add(time.Hour)
	who := sessions{"alice": {AccountID: alice, Session: contracts.Session{ID: "ses_a", ConfirmedUntil: until}}}
	routes := httpapi.MailboxesAPI(httpapi.MailboxesConfig{Service: i.svc, Reader: inbox.NewReader(w.mem), Matcher: inbox.NewMatcher(w.mem, query.Eval), Log: log, Now: w.clock, Sessions: who})
	check := httpapi.QueriesAPI(httpapi.QueriesConfig{Sessions: who, Mailboxes: i.svc, Log: log})
	w.mem.Interrupt = func(_ context.Context, step store.Step, partition, key string) error {
		if step == store.StepRead && partition == inbox.Partition(boxB.ID) && key == inbox.KeyConfirm {
			return errors.New("the table is away")
		}
		return nil
	}
	want(t, serve(routes, "alice", "GET", "/api/v1/mailboxes", ""), 500, "internal")
	read(t, serve(routes, "alice", "GET", "/api/v1/mailboxes/"+boxA.ID+"/messages?q=x", ""), 200, nil)
	read(t, serve(routes, "alice", "POST", "/api/v1/mailboxes/"+boxA.ID+"/matches", `{"formula":{"v":1,"all":[]}}`), 200, nil)
	var got contracts.FormulaCheck
	read(t, serve(check, "alice", "POST", "/api/v1/queries/check", `{"formula":`+formulaOf(cond("mailbox", "is", boxA.ID), cond("mailbox", "is", boxB.ID))+`}`), 200, &got)
	if !strings.Contains(got.Sentence.Text, "your mailbox “Alpha”") || !strings.Contains(got.Sentence.Text, "your mailbox “Beta”") {
		t.Fatalf("%+v", got.Sentence)
	}
	// Another account's mailbox is still unknown to her.
	bobs := i.make("bob", "Bob's")
	if p := problemOf(t, serve(check, "alice", "POST", "/api/v1/queries/check", `{"formula":`+formulaOf(cond("mailbox", "is", bobs.ID))+`}`)); p.Reason != contracts.FormulaUnknownMailbox {
		t.Fatalf("%+v", p)
	}
}

// Q35: matches answers in ADR 0026's order of failures, JSON with nosniff,
// and the matcher is never asked about another account's mailbox.
func TestQ35TheMatchesOrder(t *testing.T) {
	a := newQueryApp(t, nil)
	box := a.fake.Add(alice, "Mine")
	theirs := a.fake.Add(bob, "Theirs")
	a.seedFrom(box.ID, plainRaw)
	good := `{"formula":{"v":1,"all":[]}}`
	worst := `{"formula":{"v":2},"limit":500,"after":"x"}`

	want(t, a.matches("", box.ID, good), 401, "unauthorized")
	want(t, a.matches("nobody", box.ID, worst), 401, "unauthorized")
	// Another account's mailbox, and one never made, answer alike, whatever
	// the body, before anything of it is read.
	never := "mbx_" + strings.Repeat("z", 26)
	for _, body := range []string{good, worst, "nonsense"} {
		theirsRec, neverRec := a.matches("alice", theirs.ID, body), a.matches("alice", never, body)
		want(t, theirsRec, 404, "not_found")
		if theirsRec.Body.String() != neverRec.Body.String() {
			t.Fatalf("%s and %s", theirsRec.Body, neverRec.Body)
		}
	}
	// The standing of a search: closing may, not agreed may not.
	a.fake.Accounts.Set(alice, mailboxes.Standing{})
	want(t, a.matches("alice", box.ID, worst), 400, "terms_needed")
	a.fake.Accounts.Set(alice, mailboxes.Standing{Closing: true})
	want(t, a.matches("alice", box.ID, worst), 400, "terms_needed")
	a.fake.Accounts.Set(alice, mailboxes.Standing{Closing: true, TermsAgreed: true})
	want(t, a.matches("alice", box.ID, good), 200, "")
	a.fake.Accounts.Set(alice, mailboxes.Standing{TermsAgreed: true})
	// The record, before the input.
	lost := a.fake.Add(alice, "Lost")
	a.fake.MakeLost(lost.ID)
	want(t, a.matches("alice", lost.ID, worst), 409, "mailbox_unreadable")
	// The input: the body, the formula, the limit, the cursor, in that order.
	f := `{"v":1,"all":[]}`
	for _, c := range []struct{ body, code string }{
		{"nonsense", "invalid_body"},
		{`{"formula":` + f + `,"q":"x"}`, "invalid_body"},
		{`{"limit":1}`, "invalid_body"},
		{`{"formula":` + f + `,"limit":1,"limit":2}`, "invalid_body"},
		{worst, "invalid_formula"},
		{`{"formula":{"v":2},"limit":0}`, "invalid_formula"},
		{`{"formula":` + f + `,"limit":101,"after":"x"}`, "invalid_limit"},
		{`{"formula":` + f + `,"limit":-1}`, "invalid_limit"},
		{`{"formula":` + f + `,"limit":1.5}`, "invalid_limit"},
		{`{"formula":` + f + `,"limit":"2"}`, "invalid_limit"},
		{`{"formula":` + f + `,"limit":null}`, "invalid_limit"},
		{`{"formula":` + f + `,"after":"x"}`, "invalid_cursor"},
		{`{"formula":` + f + `,"after":5}`, "invalid_cursor"},
		{`{"formula":` + f + `,"after":null}`, "invalid_cursor"},
		{`{"formula":` + f + `,"after":"` + strings.Repeat("A", 32) + `"}`, "invalid_cursor"},
	} {
		want(t, a.matches("alice", box.ID, c.body), 400, c.code)
	}
	if got := a.matcher.asked(); len(got) != 1 || got[0] != box.ID {
		t.Fatalf("the matcher was asked about %v", got)
	}
	if got := a.svc.opened; slices.Contains(got, theirs.ID) {
		t.Fatalf("opened %v", got)
	}
	rec := a.call("alice", "GET", "/api/v1/mailboxes/"+box.ID+"/matches", "")
	want(t, rec, 405, "method_not_allowed")
	if rec.Header().Get("Allow") != "POST" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("%v", rec.Header())
	}
	// Without a matcher, the route is not there.
	plain := newInboxApp(t, nil)
	other := plain.fake.Add(alice, "Mine")
	want(t, plain.call("alice", "POST", "/api/v1/mailboxes/"+other.ID+"/matches", good), 404, "not_found")
}

// Q35: what a page of matches answers, and paging through it.
func TestQ35TheMatchesAnswer(t *testing.T) {
	a := newQueryApp(t, nil)
	box := a.fake.Add(alice, "Mine")
	home := a.fake.Add(alice, "Home")
	raw := func(subject string) string {
		return "From: Ana <ana@example.org>\r\nTo: x@example.org\r\nSubject: " + subject + "\r\nList-Id: Trips <trips.example.org>\r\nContent-Type: text/plain\r\n\r\nHello\r\n"
	}
	var invoices []string
	for i := range 3 {
		invoices = append([]string{a.seedFrom(box.ID, raw(fmt.Sprintf("Invoice %d", i)))}, invoices...)
		a.seedFrom(box.ID, raw("Something else"))
	}
	a.seedFrom(home.ID, raw("Invoice at home"))
	about := a.seedFrom(box.ID, "From: ana@example.org\r\nSubject: about\r\nContent-Type: text/plain\r\n\r\nWho knows\r\n")
	f := `{"v":1,"all":[{"any":[` + cond("subject", "contains", "invoice") + "," + cond("message", "about", "invoices") + `]},{"any":[` +
		cond("list", "is", "trips.example.org") + `,` + cond("mailbox", "is", home.ID) + `]}]}`
	rec := a.matches("alice", box.ID, `{"formula":`+f+`,"limit":2}`)
	var page contracts.MatchPage
	read(t, rec, 200, &page)
	if h := rec.Header(); h.Get("Content-Type") != "application/json" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("%v", h)
	}
	if len(page.Messages) != 2 || page.Messages[0].ID != invoices[0] || page.Messages[1].ID != invoices[1] || page.Next != invoices[1] || page.SearchedTo.IsZero() ||
		len(page.NeedsModel) != 2 || len(page.Unknown) != 0 || slices.Contains(page.NeedsModel, about) {
		t.Fatalf("%+v", page)
	}
	next := page.Next
	page = contracts.MatchPage{}
	read(t, a.matches("alice", box.ID, `{"formula":`+f+`,"limit":2,"after":"`+next+`"}`), 200, &page)
	if len(page.Messages) != 1 || page.Messages[0].ID != invoices[2] || page.Next != "" || len(page.NeedsModel) != 1 || len(page.Unknown) != 0 {
		t.Fatalf("%+v", page)
	}
	// Absent lists are lists: what the app reads.
	rec = a.matches("alice", box.ID, `{"formula":`+formulaOf(cond("subject", "contains", marker))+`}`)
	if !strings.Contains(rec.Body.String(), `"messages":[],"needs_model":[],"unknown":[]`) {
		t.Fatalf("%s", rec.Body)
	}
	if out := a.logs.String(); out != "" {
		t.Fatalf("logged: %s", out)
	}
}

// Q30, in httpapi: a page of matches counts in the account's thirty search
// pages a minute and holds one of the four search slots, which the inbox's
// search shares; a grant not admitted within the wait is 429.
func TestQ30TheBudgetsMatchesShare(t *testing.T) {
	a := newQueryApp(t, nil)
	box := a.fake.Add(alice, "Mine")
	other := a.fake.Add(bob, "Theirs")
	a.seedFrom(box.ID, plainRaw)
	match := `{"formula":{"v":1,"all":[]}}`
	search := "/api/v1/mailboxes/" + box.ID + "/messages?q=hello"
	for i := range httpapi.SearchPagesPerMinute {
		if i%2 == 0 {
			want(t, a.matches("alice", box.ID, match), 200, "")
		} else {
			want(t, a.call("alice", "GET", search, ""), 200, "")
		}
	}
	want(t, a.matches("alice", box.ID, match), 429, "too_many_requests")
	want(t, a.call("alice", "GET", search, ""), 429, "too_many_requests")
	want(t, a.matches("bob", other.ID, match), 200, "")

	// The four slots: four pages of matches held in the store, then a search
	// waits and is refused, and a fifth page too; and the other way round.
	for _, first := range []string{"matches", "search"} {
		b := newQueryApp(t, nil)
		mine := b.fake.Add(alice, "Mine")
		b.seedFrom(mine.ID, plainRaw)
		release := make(chan struct{})
		var holding sync.WaitGroup
		holding.Add(httpapi.SearchesAtOnce)
		key := "msg#"
		if first == "search" {
			key = inbox.KeySearch
		}
		b.mem.Interrupt = func(_ context.Context, step store.Step, _, k string) error {
			if step == store.StepRead && k == key {
				holding.Done()
				<-release
			}
			return nil
		}
		done := make(chan int, httpapi.SearchesAtOnce)
		for range httpapi.SearchesAtOnce {
			go func() {
				if first == "search" {
					done <- b.call("alice", "GET", "/api/v1/mailboxes/"+mine.ID+"/messages?q=hello", "").Code
				} else {
					done <- b.matches("alice", mine.ID, match).Code
				}
			}()
		}
		holding.Wait()
		b.mem.Interrupt = nil
		want(t, b.call("alice", "GET", "/api/v1/mailboxes/"+mine.ID+"/messages?q=hello", ""), 429, "too_many_requests")
		want(t, b.matches("alice", mine.ID, match), 429, "too_many_requests")
		close(release)
		for range httpapi.SearchesAtOnce {
			if code := <-done; code != 200 {
				t.Fatalf("%s: %d", first, code)
			}
		}
		want(t, b.matches("alice", mine.ID, match), 200, "")
	}

	// The read budget held by a download: a page that needs a raw message
	// waits for its grant, then is refused; once the download ends, it is
	// answered.
	size := int64(len(longRaw(t))) + 1<<10
	c := newQueryApp(t, func(c *httpapi.MailboxesConfig) { c.ReadBudget = size })
	mine := c.fake.Add(alice, "Mine")
	msg := c.seedFrom(mine.ID, longRaw(t))
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	c.mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
		if step == store.StepRead && key == "msg#"+msg {
			if h, _ := c.state.Held(); h == size {
				once.Do(func() { close(held); <-release })
			}
		}
		return nil
	}
	first := make(chan *httptest.ResponseRecorder)
	go func() { first <- c.call("alice", "GET", "/api/v1/mailboxes/"+mine.ID+"/messages/"+msg+"/raw", "") }()
	<-held
	body := `{"formula":` + formulaOf(cond("body", "contains", "needle")) + `}`
	start := time.Now()
	want(t, c.matches("alice", mine.ID, body), 429, "too_many_requests")
	if waited := time.Since(start); waited < c.cfg.Wait {
		t.Fatalf("refused after %v, before the wait of %v", waited, c.cfg.Wait)
	}
	close(release)
	want(t, <-first, 200, "")
	var page contracts.MatchPage
	read(t, c.matches("alice", mine.ID, body), 200, &page)
	if len(page.Messages) != 1 || page.Messages[0].ID != msg {
		t.Fatalf("%+v", page)
	}
	if held, waiting := c.state.Held(); held != 0 || waiting != 0 {
		t.Fatalf("held %d, %d waiting", held, waiting)
	}
	if httpapi.AdmissionWait != 10*time.Second {
		t.Fatalf("the wait is %v", httpapi.AdmissionWait)
	}
}

// longRaw is a message whose text is past the 16 KiB its search entry keeps,
// with "needle" only after it.
func longRaw(t *testing.T) string {
	t.Helper()
	raw := "From: ana@example.org\r\nSubject: long\r\nContent-Type: text/plain\r\n\r\n" + strings.Repeat("hay ", 8<<10) + "needle\r\n"
	if p := inbox.Parse(context.Background(), []byte(raw)); !p.TextCut {
		t.Fatal("the text is not cut")
	}
	return raw
}

// Q44, in httpapi: two pages of matches at once, each over six messages of
// 25 MiB whose texts the entries cut, under the default budget of 128 MiB,
// both answer every message: each raw read takes its grant and gives it back
// before the next, long before the response ends, so the two pages never
// hold more than a grant each.
func TestQ44TwoPagesOfLargeMessagesAtOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("reads 300 MiB")
	}
	a := newQueryApp(t, func(c *httpapi.MailboxesConfig) { c.Wait = 2 * time.Second })
	box := a.fake.Add(alice, "Mine")
	header := "From: ana@example.org\r\nSubject: large\r\nContent-Type: text/plain\r\n\r\n"
	// The needle past the entry's 16 KiB and within the 256 KiB of text a
	// connection receives.
	body := strings.Repeat("0123456789abcde ", 2<<10) + "needle " + strings.Repeat("0123456789abcde ", (inbox.MaxMessageBytes-len(header))/16-(2<<10)-1)
	raw := []byte(header + body)
	if len(raw) > inbox.MaxMessageBytes || len(raw) < inbox.MaxMessageBytes-(1<<10) {
		t.Fatalf("a message of %d bytes", len(raw))
	}
	parsed := inboxtest.FromRaw(context.Background(), "", a.now, 30, raw)
	if !parsed.Search.Cut {
		t.Fatal("the entry is not cut")
	}
	var ids []string
	for i := range 6 {
		at := a.now.Add(-time.Duration(10-i) * time.Minute)
		m := parsed
		m.ID, m.ReceivedAt = fmt.Sprintf("%016x%016x", at.UnixNano(), i), at
		s := *parsed.Search
		s.ReceivedAt = at
		m.Search = &s
		if err := inboxtest.Seed(context.Background(), a.mem, a.mem, inbox.Box{MailboxID: box.ID, Key: mailboxestest.Key(box.ID)}, m); err != nil {
			t.Fatal(err)
		}
		ids = append([]string{m.ID}, ids...)
	}
	grant := int64(len(raw)) + 1<<10
	var mu sync.Mutex
	reads, most := 0, int64(0)
	a.mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
		if step != store.StepRead || !strings.HasPrefix(key, "msg#") || key == "msg#" {
			return nil
		}
		held, _ := a.state.Held()
		mu.Lock()
		reads++
		most = max(most, held)
		mu.Unlock()
		if held > 2*grant {
			return fmt.Errorf("%d bytes held at a raw read", held)
		}
		return nil
	}
	f := `{"formula":` + formulaOf(cond("body", "contains", "needle")) + `}`
	pages := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { pages <- a.matches("alice", box.ID, f) }()
	}
	for range 2 {
		var page contracts.MatchPage
		read(t, <-pages, 200, &page)
		var got []string
		for _, m := range page.Messages {
			got = append(got, m.ID)
		}
		if !slices.Equal(got, ids) {
			t.Fatalf("%v, want %v", got, ids)
		}
	}
	if reads != 12 || most > 2*grant || most < grant {
		t.Fatalf("%d raw reads, at most %d bytes held (a grant is %d)", reads, most, grant)
	}
	if held, waiting := a.state.Held(); held != 0 || waiting != 0 {
		t.Fatalf("held %d, %d waiting", held, waiting)
	}

	// One page alone holds one grant at each raw read: the one before was
	// given back, though the response is not written yet.
	a.mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
		if step == store.StepRead && strings.HasPrefix(key, "msg#") && key != "msg#" {
			if held, _ := a.state.Held(); held != grant {
				return fmt.Errorf("%d bytes held at a raw read, a grant is %d", held, grant)
			}
		}
		return nil
	}
	var page contracts.MatchPage
	read(t, a.matches("alice", box.ID, f), 200, &page)
	if len(page.Messages) != 6 {
		t.Fatalf("%+v", page)
	}
}

// A raw read whose bound is past the whole read budget is refused (429)
// before anything is taken or read: a grant never holds fewer bytes than the
// read it admits (ReadGrants promises n).
func TestAGrantPastTheWholeBudgetIsRefused(t *testing.T) {
	a := newQueryApp(t, func(c *httpapi.MailboxesConfig) { c.ReadBudget = 1024; c.Wait = 20 * time.Millisecond })
	box := a.fake.Add(alice, "Mine")
	raw := longRaw(t)
	if len(raw) != 32_842 {
		t.Fatalf("a message of %d bytes", len(raw))
	}
	a.seedFrom(box.ID, raw)
	reads := 0
	a.mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
		if step == store.StepRead && strings.HasPrefix(key, "msg#") && key != "msg#" {
			reads++
		}
		return nil
	}
	want(t, a.matches("alice", box.ID, `{"formula":`+formulaOf(cond("body", "contains", "needle"))+`}`), 429, "too_many_requests")
	if reads != 0 {
		t.Fatalf("%d raw reads", reads)
	}
	if held, waiting := a.state.Held(); held != 0 || waiting != 0 {
		t.Fatalf("held %d, %d waiting", held, waiting)
	}
}

// Q45, that no other route answers a MatchPage, is
// TestQ45OnlyTheMatchesRouteAsksTheMatcher in cmd/permaily-enclave: every
// route of every host the enclave serves, with a matcher that counts.

// A page whose request's deadline ends while it waits for a search slot
// answers the deadline's failure, and logs it without anything of the page.
func TestAPageOfMatchesWaitsNoLongerThanItsDeadline(t *testing.T) {
	a := newQueryApp(t, func(c *httpapi.MailboxesConfig) { c.Wait = 2 * time.Second })
	box := a.fake.Add(alice, "Mine")
	a.seedFrom(box.ID, plainRaw)
	release := make(chan struct{})
	var holding sync.WaitGroup
	holding.Add(httpapi.SearchesAtOnce)
	// The four holders keep their slots until released.
	a.mem.Interrupt = func(_ context.Context, step store.Step, _, k string) error {
		if step == store.StepRead && k == "msg#" {
			holding.Done()
			<-release
		}
		return nil
	}
	f := `{"formula":` + formulaOf(cond("subject", "contains", marker)) + `}`
	done := make(chan int, httpapi.SearchesAtOnce)
	for range httpapi.SearchesAtOnce {
		go func() { done <- a.matches("alice", box.ID, f).Code }()
	}
	holding.Wait()
	a.mem.Interrupt = nil
	// The fifth's own deadline ends long before the wait for a slot would.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r := httptest.NewRequestWithContext(ctx, "POST", "https://app.permaily.test/api/v1/mailboxes/"+box.ID+"/matches", strings.NewReader(f))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", appOrigin)
	r.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: "alice"})
	rec := httptest.NewRecorder()
	start := time.Now()
	a.h.ServeHTTP(rec, r)
	want(t, rec, 500, "internal")
	if waited := time.Since(start); waited >= a.cfg.Wait {
		t.Fatalf("waited %v", waited)
	}
	close(release)
	for range httpapi.SearchesAtOnce {
		<-done
	}
	if out := a.logs.String(); !strings.Contains(out, "past the request's deadline") || strings.Contains(out, marker) || strings.Contains(out, box.ID) {
		t.Fatalf("%s", out)
	}
}
