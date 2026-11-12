package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The mailboxes of the operator's tool and of the admin panel (ADR 0026,
// "What the operator and admins see and do"): an account's mailboxes, the
// mailbox an address leads to, and pausing and resuming one. Their handlers
// are given a MailboxOperator and nothing else, so no route here could read
// mail, show a mailbox's address or label, disconnect a mailbox or delete
// mail, whatever is added later.
//
//	GET  <prefix>/accounts/{id}/mailboxes      MailboxRecordList
//	GET  <prefix>/mailboxes?address=<address>  MailboxRecordList of none or one
//	POST <prefix>/mailboxes/{id}/pause         MailboxRecord after it
//	POST <prefix>/mailboxes/{id}/resume        MailboxRecord after it
//
// <prefix> is /v1/admin for the operator with the admin token, on the API
// host, and /api/v1/admin for an admin, on the app's host, where whoever is
// not an admin is answered not_found before any of this (AccountsAPI).

// Mailboxes returns the operator's routes of mailboxes under /v1/admin:
// finding one by its address, and pausing and resuming one. An account's
// mailboxes are under /v1/admin/accounts, served by Accounts. Put it behind
// TokenAuth with the operator's token.
func Mailboxes(svc MailboxOperator, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mailboxRoutes(mux, "/v1/admin", func(*http.Request) MailboxOperator { return svc }, log)
	mux.HandleFunc("/", noSuchEndpoint)
	return mux
}

// operatorMailboxRoutes adds an account's mailboxes to the operator's
// accounts routes, when the service it is given has mailboxes.
func operatorMailboxRoutes(mux *http.ServeMux, prefix string, svc OperatorService, log *slog.Logger) {
	if m, ok := svc.(MailboxOperator); ok {
		accountMailboxRoutes(mux, prefix, func(*http.Request) MailboxOperator { return m }, log)
	}
}

// adminMailboxRoutes adds every route of mailboxes to the admin panel, for
// the admin a request carries (adminKey).
func adminMailboxRoutes(mux *http.ServeMux, prefix string, log *slog.Logger) {
	op := func(r *http.Request) MailboxOperator {
		m, _ := adminOf(r).(MailboxOperator)
		return m
	}
	accountMailboxRoutes(mux, prefix, op, log)
	mailboxRoutes(mux, prefix, op, log)
}

// accountMailboxRoutes is the route of an account's mailboxes.
func accountMailboxRoutes(mux *http.ServeMux, prefix string, op func(*http.Request) MailboxOperator, log *slog.Logger) {
	mux.HandleFunc("GET "+prefix+"/accounts/{id}/mailboxes", func(w http.ResponseWriter, r *http.Request) {
		m := op(r)
		if m == nil {
			noSuchEndpoint(w, r)
			return
		}
		out, err := m.MailboxRecords(r.Context(), r.PathValue("id"))
		mailboxAnswer(w, log, out, err)
	})
	mux.HandleFunc(prefix+"/accounts/{id}/mailboxes", refuse(http.MethodGet))
}

// mailboxRoutes are the routes of mailboxes by themselves.
func mailboxRoutes(mux *http.ServeMux, prefix string, op func(*http.Request) MailboxOperator, log *slog.Logger) {
	mux.HandleFunc("GET "+prefix+"/mailboxes", func(w http.ResponseWriter, r *http.Request) {
		m := op(r)
		if m == nil {
			noSuchEndpoint(w, r)
			return
		}
		// One exact address a request: a request that names none, or more
		// than one, is refused for what it is, and says nothing of any.
		addresses := r.URL.Query()["address"]
		if len(addresses) != 1 {
			failed(w, log, contracts.ErrInvalidAddress)
			return
		}
		out, err := m.MailboxRecordByAddress(r.Context(), addresses[0])
		mailboxAnswer(w, log, out, err)
	})
	for path, pause := range map[string]bool{"pause": true, "resume": false} {
		mux.HandleFunc("POST "+prefix+"/mailboxes/{id}/"+path, func(w http.ResponseWriter, r *http.Request) {
			m := op(r)
			if m == nil {
				noSuchEndpoint(w, r)
				return
			}
			act := m.ResumeMailbox
			if pause {
				act = m.PauseMailbox
			}
			out, err := act(r.Context(), r.PathValue("id"))
			mailboxAnswer(w, log, out, err)
		})
		mux.HandleFunc(prefix+"/mailboxes/{id}/"+path, refuse(http.MethodPost))
	}
	mux.HandleFunc(prefix+"/mailboxes", refuse(http.MethodGet))
}

// mailboxAnswer writes what the service gave, or its error. An error of the
// service's own is logged with nothing of what it says, which may name a
// mailbox, its address or an account, and answered with nothing of its own.
func mailboxAnswer(w http.ResponseWriter, log *slog.Logger, out any, err error) {
	var known *contracts.Error
	if err != nil && !errors.As(err, &known) {
		log.Error("api: internal error in a route of mailboxes")
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	answerWith(w, log, out, err)
}
