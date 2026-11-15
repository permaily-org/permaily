package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// The query routes of the web app's API (ADR 0027, "The routes"): the check
// of a formula here, and a mailbox's matches in mailboxes.go. Both are the
// owner's own, in their session: what the rule editor (step 7) and the
// consent screen (step 5) show sentences and previews from. Neither logs
// anything of a formula: not a value, a sentence, an identifier or a number
// of matches; a refusal is answered with a path and a fixed reason, and
// nothing of it is logged.

// MailboxLister is the account's mailboxes, which a formula may name and
// its sentence names; MailboxesService implements it (List, allowed in every
// standing).
type MailboxLister interface {
	List(ctx context.Context, c mailboxes.Caller) (*contracts.MailboxList, error)
}

// AccountLanguage is the account a sentence is written for, for its language;
// *accounts.Service implements it.
type AccountLanguage interface {
	Me(ctx context.Context, who *accounts.Authenticated) (*contracts.Me, error)
}

// QueriesConfig is what the check route is made of.
type QueriesConfig struct {
	Sessions  Sessions
	Mailboxes MailboxLister
	// Accounts gives the account's language; without one, every sentence is
	// in English, as it is today for every account (ADR 0027, "A second
	// language").
	Accounts AccountLanguage
	Log      *slog.Logger
	// Deadline bounds every request from its start (mailboxes.RequestDeadline).
	Deadline time.Duration
}

// maxQueryBody bounds the body of the query routes: a formula of
// contracts.MaxFormulaBytes and room to say it is too large, with a cursor
// and a limit. A formula past it is refused there and then (invalid_body):
// nothing of it is read.
const maxQueryBody = 2 * contracts.MaxFormulaBytes

// QueriesAPI returns POST /api/v1/queries/check, on the app's host, answering
// only to the app (the session cookie, same origin): a formula's canonical
// form, its identifier and the sentence the person reads, in the account's
// language (English while English is the only table), naming the account's
// mailboxes. In order: 401 without a session or for an account blocked or
// gone; then the body (invalid_body: not one JSON object of the one member
// formula); then the formula (invalid_formula with where and why, its
// mailboxes held to the account's: unknown_mailbox for any other).
func QueriesAPI(cfg QueriesConfig) http.Handler {
	if cfg.Deadline <= 0 {
		cfg.Deadline = mailboxes.RequestDeadline
	}
	q := &queriesAPI{cfg: cfg}
	mux := NewAppMux()
	const base = "/api/v1/queries"
	mux.HandleFunc("POST "+base+"/check", withSession(cfg.Sessions, cfg.Log, q.check))
	mux.HandleFunc(base+"/check", refuse(http.MethodPost))
	mux.HandleFunc(base+"/", noSuchEndpoint)
	mux.HandleFunc(base, noSuchEndpoint)
	app := fromTheApp(mux)
	return RequireCanonicalPath(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), cfg.Deadline)
		defer cancel()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		app.ServeHTTP(w, r.WithContext(ctx))
	}))
}

type queriesAPI struct{ cfg QueriesConfig }

func (q *queriesAPI) check(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
	ctx := r.Context()
	c := mailboxes.Caller{Account: who.AccountID, ConfirmedUntil: who.Session.ConfirmedUntil}
	list, err := q.cfg.Mailboxes.List(ctx, c)
	if err != nil {
		failed(w, r, q.cfg.Log, err)
		return
	}
	lang := ""
	if q.cfg.Accounts != nil {
		me, err := q.cfg.Accounts.Me(ctx, who)
		if err != nil {
			failed(w, r, q.cfg.Log, err)
			return
		}
		lang = me.Account.Language
	}
	members, ok := readObject(w, r, "formula")
	if !ok {
		return
	}
	raw, ok := members["formula"]
	if !ok {
		writeInvalidBody(w)
		return
	}
	f, err := query.Parse(raw, mailboxIDs(list))
	if err != nil {
		failed(w, r, q.cfg.Log, err)
		return
	}
	writeJSON(w, contracts.FormulaCheck{
		Formula: f.Formula, ID: f.ID, Sentence: query.Render(f, sentenceContext(list, lang)), Model: decidedByAModel(f),
	})
}

// mailboxIDs are the identifiers a formula of the account may name: every
// mailbox of its list, an unreadable one included. Never nil, so an account
// without mailboxes refuses every mailbox (query.Parse takes nil for "any").
func mailboxIDs(list *contracts.MailboxList) []string {
	ids := make([]string, 0, len(list.Mailboxes))
	for _, m := range list.Mailboxes {
		ids = append(ids, m.ID)
	}
	return ids
}

// sentenceContext is what a sentence knows of the account: its language and
// what each of its mailboxes is called (an unreadable one has neither label
// nor address, and reads by its identifier).
func sentenceContext(list *contracts.MailboxList, lang string) query.Context {
	names := make(map[string]query.MailboxName, len(list.Mailboxes))
	for _, m := range list.Mailboxes {
		names[m.ID] = query.MailboxName{Label: m.Label, Address: m.Address}
	}
	return query.Context{Lang: lang, Mailboxes: names}
}

// decidedByAModel reports whether a model decides any condition of f.
func decidedByAModel(f *query.Canonical) bool {
	for _, g := range f.Formula.All {
		if slices.ContainsFunc(g.Any, func(c contracts.Condition) bool {
			return c.Op == contracts.OpAbout || c.Op == contracts.OpMentions
		}) {
			return true
		}
	}
	return false
}

func writeInvalidBody(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, "invalid_body", "the request is not the JSON this endpoint takes")
}

// readObject reads a request's body as one JSON object of at most
// maxQueryBody bytes whose members are among names, each at most once, with
// nothing after it, and returns each member's value as it was sent: a
// formula is read again, strictly, by query.Parse, from its own bytes. A
// member twice is refused, as Parse refuses one inside a formula: two
// readers would read it differently. Anything else is answered invalid_body.
func readObject(w http.ResponseWriter, r *http.Request, names ...string) (map[string]json.RawMessage, bool) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxQueryBody))
	out := map[string]json.RawMessage{}
	ok := func() bool {
		if t, err := dec.Token(); err != nil || t != json.Delim('{') {
			return false
		}
		for dec.More() {
			t, err := dec.Token()
			name, isName := t.(string)
			if err != nil || !isName || !slices.Contains(names, name) {
				return false
			}
			if _, twice := out[name]; twice {
				return false
			}
			var v json.RawMessage
			if dec.Decode(&v) != nil {
				return false
			}
			out[name] = v
		}
		if t, err := dec.Token(); err != nil || t != json.Delim('}') {
			return false
		}
		_, err := dec.Token()
		return errors.Is(err, io.EOF)
	}()
	if !ok {
		writeInvalidBody(w)
		return nil, false
	}
	return out, true
}
