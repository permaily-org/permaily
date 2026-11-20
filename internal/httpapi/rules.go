package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rulewords"
	"github.com/permaily-org/permaily/internal/services/accountrules"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// The routes of the person's own rules (ADR 0040, pull request 6), adapters
// that decide nothing of a rule: they read what was sent strictly, check the
// account's standing and the session's proof, hand it to the rules service
// (accountrules.Rules), the words' writer (rulewords.Writer) or a mailbox's
// matcher, and answer what they said. Nothing of a rule is logged: not its
// name, a criteria's value, a topic, the person's words, a sentence or a
// count of an account's rules (U28); a failure of a service's own is logged
// by its kind alone.

// RulesStanding is the account's standing as the rules routes read it
// (*accounts.Service, Standing): contracts.ErrUnauthorized for an account
// blocked, gone or due to be removed; Closing while it is being deleted;
// TermsAgreed when it agreed to the terms in force.
type RulesStanding interface {
	Standing(ctx context.Context, account string) (mailboxes.Standing, error)
}

// RulesConfig is what the rules routes are made of.
type RulesConfig struct {
	// Rules is the rules service (*accountrules.Service; the fake in tests).
	Rules accountrules.Rules
	// Words is what turns words into a draft (*decisions.Service, Write).
	// Without one, words are answered contracts.ErrModelUnavailable, and
	// the editor builds the rule from its conditions.
	Words rulewords.Writer
	// Standing is the account's standing (*accounts.Service). Required.
	Standing RulesStanding
	// Previews are a mailbox's previews of a draft, sharing the mailboxes
	// routes' admission (MailboxesWithRulePreviews). Without them
	// POST /api/v1/rules/preview is not served (not_found).
	Previews *RulePreviews
	Sessions Sessions
	Log      *slog.Logger
	// Notice is the rules' notice in force: domain.RulesNoticeVersion when
	// empty (the service's own, which tests change as a release would).
	Notice string
	// Deadline bounds every request from its start
	// (mailboxes.RequestDeadline); Now is the clock (time.Now).
	Deadline time.Duration
	Now      func() time.Time
}

func (c RulesConfig) withDefaults() RulesConfig {
	if c.Deadline <= 0 {
		c.Deadline = mailboxes.RequestDeadline
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Notice == "" {
		c.Notice = domain.RulesNoticeVersion
	}
	return c
}

// maxRuleBody bounds a body of the rules routes: a draft at its largest
// (contracts.MaxRuleBytes of canonical JSON, which a person's escapes and
// white space may double) with the members around it, or the words at
// their largest (contracts.MaxRuleWordsRunes runes of up to four bytes,
// each escaped as up to twelve). A body past it is invalid_body, and
// nothing of it is read.
const maxRuleBody = 4 * contracts.MaxRuleBytes

// rulesNoticePath is the route agreeing to the rules' notice, beside the
// model's notice under /api/v1/account.
const rulesNoticePath = "/api/v1/account/rules-notice"

// MountRules mounts the rules routes on the app's host (RulesAPI): the
// composition calls it with the app's mux, before AccountsAPI's /api/
// catches the rest of /api/v1/account.
func MountRules(app *AppMux, cfg RulesConfig) {
	h := RulesAPI(cfg)
	app.Handle("/api/v1/rules", h)
	app.Handle("/api/v1/rules/", h)
	app.Handle(rulesNoticePath, h)
}

// RulesAPI returns the person's rules routes, on the app's host and
// answering only to the app (the session cookie, same origin for changes,
// behind AppHost's cross-site rule as every route under /api/ is):
//
//	GET  /api/v1/rules                   RuleList
//	POST /api/v1/rules/check             {"draft"} → RuleCheck
//	POST /api/v1/rules/words             RuleWords → RuleProposal
//	POST /api/v1/rules                   RuleSave → Rule (a new rule)
//	PUT  /api/v1/rules/{id}              RuleSave → Rule (a change, the switch included)
//	POST /api/v1/rules/{id}/delete       RuleDelete → 204
//	POST /api/v1/rules/default           RuleDefaultSet → RuleDefault
//	POST /api/v1/rules/preview           RulePreview → RulePreviewPage (rules_preview.go)
//	POST /api/v1/account/rules-notice    {"version"} → RuleNoticeAgreement
//
// In order, each answers the first failure: 401 without a session, or for
// an account blocked, gone or due to be removed; the account's standing (a
// change, words and the notice are account_closing while it closes, then
// terms_needed while the terms in force are not agreed: the person's rules
// are read-only then; the list and a check are allowed in every standing;
// a preview is a search's, mailboxes.AccessSearch); the body, read strictly
// (invalid_body); a fresh proof for every change (confirmation_needed,
// before the service is asked, which checks it again when it commits); then
// the service's answer.
func RulesAPI(cfg RulesConfig) http.Handler {
	cfg = cfg.withDefaults()
	a := &rulesAPI{cfg: cfg}
	mux := NewAppMux()
	in := func(next func(http.ResponseWriter, *http.Request, accountrules.Person)) http.HandlerFunc {
		return withSession(cfg.Sessions, cfg.Log, func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
			next(w, r, accountrules.Person{Account: who.AccountID, ConfirmedUntil: who.Session.ConfirmedUntil})
		})
	}
	const base = "/api/v1/rules"
	mux.HandleFunc("GET "+base, in(a.list))
	mux.HandleFunc("POST "+base, in(a.create))
	mux.HandleFunc(base, refuse("GET, POST"))
	mux.HandleFunc("POST "+base+"/check", in(a.check))
	mux.HandleFunc(base+"/check", refuse(http.MethodPost))
	mux.HandleFunc("POST "+base+"/words", in(a.words))
	mux.HandleFunc(base+"/words", refuse(http.MethodPost))
	mux.HandleFunc("POST "+base+"/default", in(a.setDefault))
	mux.HandleFunc(base+"/default", refuse(http.MethodPost))
	if cfg.Previews != nil {
		mux.HandleFunc("POST "+base+"/preview", in(a.preview))
		mux.HandleFunc(base+"/preview", refuse(http.MethodPost))
	}
	mux.HandleFunc("PUT "+base+"/{id}", in(a.update))
	mux.HandleFunc(base+"/{id}", refuse(http.MethodPut))
	mux.HandleFunc("POST "+base+"/{id}/delete", in(a.remove))
	mux.HandleFunc(base+"/{id}/delete", refuse(http.MethodPost))
	mux.HandleFunc("POST "+rulesNoticePath, in(a.agree))
	mux.HandleFunc(rulesNoticePath, refuse(http.MethodPost))
	mux.HandleFunc(base+"/", noSuchEndpoint)
	app := fromTheApp(mux)
	return RequireCanonicalPath(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), cfg.Deadline)
		defer cancel()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		app.ServeHTTP(w, r.WithContext(ctx))
	}))
}

type rulesAPI struct{ cfg RulesConfig }

// draftBody is the body of POST /api/v1/rules/check.
type draftBody struct {
	Draft contracts.RuleDraft `json:"draft"`
}

// noticeBody is the body of POST /api/v1/account/rules-notice: the version
// of the rules' notice the screen showed.
type noticeBody struct {
	Version string `json:"version"`
}

func (a *rulesAPI) list(w http.ResponseWriter, r *http.Request, p accountrules.Person) {
	if !a.standing(w, r, p, false) {
		return
	}
	out, err := a.cfg.Rules.List(r.Context(), p.Account)
	a.answer(w, r, out, err)
}

func (a *rulesAPI) check(w http.ResponseWriter, r *http.Request, p accountrules.Person) {
	var in draftBody
	if !a.standing(w, r, p, false) || !decodeRule(w, r, &in) {
		return
	}
	out, err := a.cfg.Rules.Check(r.Context(), p.Account, in.Draft)
	a.answer(w, r, out, err)
}

// words sends the person's words to the model and answers the draft it
// proposed as the service checked it (ADR 0040, "Plain words"): the rules'
// notice in force, the one the screen showed, agreed by the account
// (rules_notice_needed otherwise, before anything is sent); then the
// writer, whose refusals are answered as they are (model_unavailable,
// too_many_requests, rule_words_unusable); then the account's own check of
// the draft, for every connection and on (the model is told nothing of the
// account's connections), any refusal of which is rule_words_unusable:
// nothing is proposed that the person could not save. Nothing of the words
// is kept or logged.
func (a *rulesAPI) words(w http.ResponseWriter, r *http.Request, p accountrules.Person) {
	var in contracts.RuleWords
	if !a.standing(w, r, p, true) || !decodeRule(w, r, &in) {
		return
	}
	ctx := r.Context()
	if !a.noticeAgreed(w, r, p, in.Notice) {
		return
	}
	if a.cfg.Words == nil {
		a.fail(w, r, contracts.ErrModelUnavailable)
		return
	}
	written, err := a.cfg.Words.Write(ctx, p.Account, in.Words)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	d := written.Draft
	d.Scope, d.On = contracts.RuleScope{To: contracts.ScopeAll}, true
	c, err := a.cfg.Rules.Check(ctx, p.Account, d)
	if err != nil {
		if refusedDraft(err) {
			err = contracts.ErrRuleWordsUnusable
		}
		a.fail(w, r, err)
		return
	}
	writeJSON(w, contracts.RuleProposal{Check: *c, Model: written.Model, Prompt: written.Prompt})
}

// noticeAgreed says the screen showed the rules' notice in force and the
// account agreed to it; otherwise it answers rules_notice_needed.
func (a *rulesAPI) noticeAgreed(w http.ResponseWriter, r *http.Request, p accountrules.Person, shown string) bool {
	if shown != a.cfg.Notice {
		a.fail(w, r, contracts.ErrRulesNoticeNeeded)
		return false
	}
	list, err := a.cfg.Rules.List(r.Context(), p.Account)
	if err != nil {
		a.fail(w, r, err)
		return false
	}
	if list.Notice == nil || list.Notice.Version != a.cfg.Notice {
		a.fail(w, r, contracts.ErrRulesNoticeNeeded)
		return false
	}
	return true
}

func (a *rulesAPI) create(w http.ResponseWriter, r *http.Request, p accountrules.Person) {
	a.save(w, r, p, "")
}

func (a *rulesAPI) update(w http.ResponseWriter, r *http.Request, p accountrules.Person) {
	a.save(w, r, p, r.PathValue("id"))
}

func (a *rulesAPI) save(w http.ResponseWriter, r *http.Request, p accountrules.Person, id string) {
	var in contracts.RuleSave
	if !a.standing(w, r, p, true) || !decodeRule(w, r, &in) || !a.fresh(w, r, p) {
		return
	}
	out, err := a.cfg.Rules.Save(r.Context(), p, id, in)
	a.answer(w, r, out, err)
}

func (a *rulesAPI) remove(w http.ResponseWriter, r *http.Request, p accountrules.Person) {
	var in contracts.RuleDelete
	if !a.standing(w, r, p, true) || !decodeRule(w, r, &in) || !a.fresh(w, r, p) {
		return
	}
	if err := a.cfg.Rules.Delete(r.Context(), p, r.PathValue("id"), in); err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (a *rulesAPI) setDefault(w http.ResponseWriter, r *http.Request, p accountrules.Person) {
	var in contracts.RuleDefaultSet
	if !a.standing(w, r, p, true) || !decodeRule(w, r, &in) || !a.fresh(w, r, p) {
		return
	}
	out, err := a.cfg.Rules.SetDefault(r.Context(), p, in)
	a.answer(w, r, out, err)
}

// agree records the rules' notice in force agreed, by the version the
// screen showed (any other is rules_notice_needed, the service's). It is a
// change of the account's rules: a fresh proof, and refused while the
// account closes or has not agreed to the terms.
func (a *rulesAPI) agree(w http.ResponseWriter, r *http.Request, p accountrules.Person) {
	var in noticeBody
	if !a.standing(w, r, p, true) || !decodeRule(w, r, &in) || !a.fresh(w, r, p) {
		return
	}
	out, err := a.cfg.Rules.AgreeNotice(r.Context(), p, in.Version)
	a.answer(w, r, out, err)
}

// standing reads the account's standing: an account blocked, gone or due
// to be removed is 401 whatever is asked; for a change (change true) one
// closing is account_closing, then one that has not agreed to the terms in
// force is terms_needed.
func (a *rulesAPI) standing(w http.ResponseWriter, r *http.Request, p accountrules.Person, change bool) bool {
	if a.cfg.Standing == nil {
		a.fail(w, r, errNoStanding)
		return false
	}
	st, err := a.cfg.Standing.Standing(r.Context(), p.Account)
	if err != nil {
		a.fail(w, r, err)
		return false
	}
	if !change {
		return true
	}
	switch {
	case st.Closing:
		a.fail(w, r, contracts.ErrAccountClosing)
		return false
	case !st.TermsAgreed:
		a.fail(w, r, contracts.ErrTermsChanged)
		return false
	}
	return true
}

// errNoStanding is a composition without the accounts' standing: nothing is
// answered on a standing nobody read.
var errNoStanding = errors.New("httpapi: the rules routes have no standing to read")

// fresh says the session's proof is fresh (ADR 0022), or answers
// confirmation_needed.
func (a *rulesAPI) fresh(w http.ResponseWriter, r *http.Request, p accountrules.Person) bool {
	if !a.cfg.Now().Before(p.ConfirmedUntil) {
		a.fail(w, r, contracts.ErrConfirmationNeeded)
		return false
	}
	return true
}

// answer writes the service's answer, or its refusal.
func (a *rulesAPI) answer(w http.ResponseWriter, r *http.Request, out any, err error) {
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, out)
}

// rulesStatus is the status of a refusal of the rules routes that failed
// does not know: 409 for one that says how things are now (the screen reads
// them again, or the person deletes a rule first), 503 for the model.
var rulesStatus = map[string]int{
	contracts.ErrRuleChanged.Code:      http.StatusConflict,
	contracts.ErrRulesChanged.Code:     http.StatusConflict,
	contracts.ErrTooManyRules.Code:     http.StatusConflict,
	contracts.ErrModelUnavailable.Code: http.StatusServiceUnavailable,
}

// rulesRetryAfter is what a 503 of the rules routes asks a client to wait,
// in seconds: the rules' cache's freshness (accountrules.Fresh), in which a
// record that could not be read is read again.
const rulesRetryAfter = "5"

// fail answers a refusal of the rules routes: an invalid rule with its
// reason ({"rule"}), a formula's problem with its path and reason, a
// contract error with its code and fixed words; a record that cannot be
// read now (accountrules.ErrUnavailable) is 503 busy, asked again later and
// logged by its kind; anything else is 500, logged by its kind alone. A
// request its client left is not logged.
func (a *rulesAPI) fail(w http.ResponseWriter, r *http.Request, err error) {
	if re, ok := errors.AsType[*contracts.RuleError](err); ok {
		var details contracts.ErrorDetails
		if re.Problem.Valid() {
			details = re.Details()
		}
		writeErrorWith(w, http.StatusBadRequest, contracts.ErrInvalidRule.Code, contracts.ErrInvalidRule.Message, details)
		return
	}
	if known, ok := errors.AsType[*contracts.Error](err); ok {
		if status, ok := rulesStatus[known.Code]; ok {
			if status == http.StatusServiceUnavailable {
				w.Header().Set("Retry-After", rulesRetryAfter)
			}
			writeError(w, status, known.Code, known.Message)
			return
		}
	}
	if refusal(err) {
		failed(w, r, a.cfg.Log, err)
		return
	}
	if errors.Is(err, accountrules.ErrUnavailable) {
		if clientLeft(r, err) {
			setOutcome(r, outcomeClientLeft)
		} else {
			a.cfg.Log.Error("rules: the account's rules could not be read", "kind", "unavailable")
		}
		w.Header().Set("Retry-After", rulesRetryAfter)
		writeError(w, http.StatusServiceUnavailable, contracts.ErrBusy.Code, contracts.ErrBusy.Message)
		return
	}
	internalFailure(w, r, a.cfg.Log, "rules", err)
}

// refusedDraft says err refuses a draft for what it holds: an invalid rule,
// a formula's problem, or a criteria naming a mailbox that is not the
// account's.
func refusedDraft(err error) bool {
	if errors.Is(err, contracts.ErrInvalidRule) || errors.Is(err, contracts.ErrInvalidFormula) {
		return true
	}
	_, problem := errors.AsType[*query.Problem](err)
	return problem
}

// decodeRule reads a body of the rules routes strictly: one JSON object of
// v's members, within maxRuleBody, with nothing after it; at every depth no
// member twice and every member's name exactly as the contract writes it
// (lowercase ASCII, so none that encoding/json would take for another in
// another case, or through the Kelvin sign or the long s), each one of v's
// fields (DisallowUnknownFields). Anything else is invalid_body, before the
// service is asked: two readers of a body with a member twice would read it
// differently.
func decodeRule(w http.ResponseWriter, r *http.Request, v any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRuleBody))
	if err != nil || !strictJSON(raw) {
		writeInvalidBody(w)
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil {
		writeInvalidBody(w)
		return false
	}
	return true
}

// strictJSON says raw is one JSON object, with nothing after it but white
// space, whose objects at every depth have no member twice and only names
// of lowercase ASCII letters and underscores.
func strictJSON(raw []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	t, err := dec.Token()
	if err != nil || t != json.Delim('{') {
		return false
	}
	if !strictObject(dec, 1) {
		return false
	}
	_, err = dec.Token()
	return errors.Is(err, io.EOF)
}

// strictDepthMax bounds how deep strictJSON reads: deeper than any body of
// the rules routes (a draft's removal's lists, at four), well within
// encoding/json's own bound.
const strictDepthMax = 16

// strictObject reads an object's members after its '{', and its '}'.
func strictObject(dec *json.Decoder, depth int) bool {
	seen := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		name, ok := t.(string)
		if err != nil || !ok || seen[name] || !contractName(name) {
			return false
		}
		seen[name] = true
		if !strictValue(dec, depth) {
			return false
		}
	}
	t, err := dec.Token()
	return err == nil && t == json.Delim('}')
}

// strictValue reads one value, an object or an array read strictly.
func strictValue(dec *json.Decoder, depth int) bool {
	t, err := dec.Token()
	if err != nil {
		return false
	}
	d, ok := t.(json.Delim)
	if !ok {
		return true
	}
	if depth >= strictDepthMax {
		return false
	}
	switch d {
	case '{':
		return strictObject(dec, depth+1)
	case '[':
		for dec.More() {
			if !strictValue(dec, depth+1) {
				return false
			}
		}
		t, err := dec.Token()
		return err == nil && t == json.Delim(']')
	}
	return false
}

// contractName says name is written as the contract writes its members:
// one or more lowercase ASCII letters and underscores.
func contractName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; (c < 'a' || c > 'z') && c != '_' {
			return false
		}
	}
	return true
}
