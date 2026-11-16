package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// The routes of connections (ADR 0030, pull request 6): the person's on the
// app's host (connections.go), the authorization server's on the api host
// and the resources' metadata (oauth_server.go), and the API for apps
// (vendor_api.go). Each is an adapter of connections.API: it reads what was
// sent, hands it to the service and answers what the service said; who may
// do what, and every state, are the service's. Nothing of a token, a code, a
// request, a formula, an account, a connection or a message is logged.

// ConnectionsConfig is what the routes of connections are made of.
type ConnectionsConfig struct {
	// Service is the connections service (connectionstest.Fake in tests).
	Service  connections.API
	Sessions Sessions
	// OAuth is the hosts the image compiles in.
	OAuth oauth.Config
	// Metadata, ResourceMetadata and Challenge are what the image publishes
	// and answers in WWW-Authenticate, which internal/oauth writes (ADR
	// 0030's pull request 1: oauth.Metadata, oauth.ResourceMetadataFor and
	// oauth.Challenge, which the composition sets here). Each is required.
	Metadata         func(oauth.Config) oauth.ServerMetadata
	ResourceMetadata func(oauth.Config, contracts.Resource) oauth.ResourceMetadata
	Challenge        func(oauth.Config, contracts.Resource, oauth.ErrorCode, []string) string
	Log              *slog.Logger
	// Deadline bounds every request of the person's routes from its start
	// (mailboxes.RequestDeadline); Now is the clock (time.Now).
	Deadline time.Duration
	Now      func() time.Time
}

func (c ConnectionsConfig) withDefaults() ConnectionsConfig {
	if c.Deadline <= 0 {
		c.Deadline = mailboxes.RequestDeadline
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// maxConsentBody bounds a body of the person's routes: the query string of an
// authorization request (a client_id and a redirect URI of at most a few
// hundred bytes each, a state of 512, a challenge of 128) or a draft of the
// account's mailboxes, with room.
const maxConsentBody = 16 << 10

// ConnectionsAPI returns the person's routes of connections, on the app's
// host and answering only to the app (the session cookie, same origin for
// changes), behind AppHost's cross-site rule as every route under /api/ is,
// none of them an exception (ADR 0030, "The hand-off"):
//
//	POST /api/v1/oauth/requests                         AuthorizationStart → AuthorizationStarted
//	POST /api/v1/oauth/requests/{id}/matches            ConsentPreview → ConsentPreviewPage
//	POST /api/v1/oauth/requests/{id}/decision           AuthorizationDecision → AuthorizationAnswer
//	GET  /api/v1/connections                            ConnectionList
//	GET  /api/v1/connections/{id}                       Connection
//	POST /api/v1/connections/{id}/revoke                Connection
//	POST /api/v1/connections/{id}/devices/{device}/revoke  Connection
//	POST /api/v1/connections/{id}/renew                 ConnectionRenewal → Connection
//
// Under terms the account has not agreed to, the list, a connection and
// both revocations work (revoking only protects) and allowing and renewing
// are the service's terms_needed (finding 17): the routes add no rule of
// standing of their own. Allowing and renewing are refused
// confirmation_needed here already when the session's proof is not fresh,
// before the service is asked; the service checks it again when it commits.
func ConnectionsAPI(cfg ConnectionsConfig) http.Handler {
	cfg = cfg.withDefaults()
	a := &consentAPI{cfg: cfg}
	mux := NewAppMux()
	in := func(next func(http.ResponseWriter, *http.Request, connections.Person)) http.HandlerFunc {
		return withSession(cfg.Sessions, cfg.Log, func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
			next(w, r, connections.Person{Account: who.AccountID, Session: who.Session.ID, ConfirmedUntil: who.Session.ConfirmedUntil})
		})
	}
	const requests = "/api/v1/oauth/requests"
	mux.HandleFunc("POST "+requests, in(a.start))
	mux.HandleFunc(requests, refuse(http.MethodPost))
	mux.HandleFunc("POST "+requests+"/{id}/matches", in(a.preview))
	mux.HandleFunc(requests+"/{id}/matches", refuse(http.MethodPost))
	mux.HandleFunc("POST "+requests+"/{id}/decision", in(a.decide))
	mux.HandleFunc(requests+"/{id}/decision", refuse(http.MethodPost))
	mux.HandleFunc("/api/v1/oauth/", noSuchEndpoint)
	const list = "/api/v1/connections"
	mux.HandleFunc("GET "+list, in(a.list))
	mux.HandleFunc(list, refuse(http.MethodGet))
	mux.HandleFunc("GET "+list+"/{id}", in(a.get))
	mux.HandleFunc(list+"/{id}", refuse(http.MethodGet))
	mux.HandleFunc("POST "+list+"/{id}/revoke", in(a.revoke))
	mux.HandleFunc(list+"/{id}/revoke", refuse(http.MethodPost))
	mux.HandleFunc("POST "+list+"/{id}/devices/{device}/revoke", in(a.revokeDevice))
	mux.HandleFunc(list+"/{id}/devices/{device}/revoke", refuse(http.MethodPost))
	mux.HandleFunc("POST "+list+"/{id}/renew", in(a.renew))
	mux.HandleFunc(list+"/{id}/renew", refuse(http.MethodPost))
	mux.HandleFunc(list+"/", noSuchEndpoint)
	app := fromTheApp(mux)
	return RequireCanonicalPath(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), cfg.Deadline)
		defer cancel()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		app.ServeHTTP(w, r.WithContext(ctx))
	}))
}

type consentAPI struct{ cfg ConnectionsConfig }

func (a *consentAPI) start(w http.ResponseWriter, r *http.Request, p connections.Person) {
	var in contracts.AuthorizationStart
	if !decodeWhole(w, r, &in) {
		return
	}
	out, err := a.cfg.Service.Start(r.Context(), p, in.Query)
	a.answer(w, r, out, err)
}

func (a *consentAPI) preview(w http.ResponseWriter, r *http.Request, p connections.Person) {
	var in contracts.ConsentPreview
	if !decodeWhole(w, r, &in) {
		return
	}
	out, err := a.cfg.Service.Preview(r.Context(), p, r.PathValue("id"), in)
	a.answer(w, r, out, err)
}

func (a *consentAPI) decide(w http.ResponseWriter, r *http.Request, p connections.Person) {
	var in contracts.AuthorizationDecision
	if !decodeWhole(w, r, &in) {
		return
	}
	if in.Allow && !a.fresh(p) {
		failed(w, r, a.cfg.Log, contracts.ErrConfirmationNeeded)
		return
	}
	out, err := a.cfg.Service.Decide(r.Context(), p, r.PathValue("id"), in)
	a.answer(w, r, out, err)
}

func (a *consentAPI) list(w http.ResponseWriter, r *http.Request, p connections.Person) {
	out, err := a.cfg.Service.List(r.Context(), p)
	a.answer(w, r, out, err)
}

func (a *consentAPI) get(w http.ResponseWriter, r *http.Request, p connections.Person) {
	out, err := a.cfg.Service.Get(r.Context(), p, r.PathValue("id"))
	a.answer(w, r, out, err)
}

func (a *consentAPI) revoke(w http.ResponseWriter, r *http.Request, p connections.Person) {
	out, err := a.cfg.Service.Revoke(r.Context(), p, r.PathValue("id"))
	a.answer(w, r, out, err)
}

func (a *consentAPI) revokeDevice(w http.ResponseWriter, r *http.Request, p connections.Person) {
	out, err := a.cfg.Service.RevokeDevice(r.Context(), p, r.PathValue("id"), r.PathValue("device"))
	a.answer(w, r, out, err)
}

func (a *consentAPI) renew(w http.ResponseWriter, r *http.Request, p connections.Person) {
	var in contracts.ConnectionRenewal
	if !decodeWhole(w, r, &in) {
		return
	}
	if !a.fresh(p) {
		failed(w, r, a.cfg.Log, contracts.ErrConfirmationNeeded)
		return
	}
	out, err := a.cfg.Service.Renew(r.Context(), p, r.PathValue("id"), in)
	a.answer(w, r, out, err)
}

// fresh says the session's proof of who the person is is still fresh (ADR
// 0022).
func (a *consentAPI) fresh(p connections.Person) bool {
	return a.cfg.Now().Before(p.ConfirmedUntil)
}

// decodeWhole reads a body of the person's routes: one JSON object of v's
// fields alone and nothing after it but white space, all of it within
// maxConsentBody. Anything else is invalid_body, before the service is
// asked. (decodeUpTo stops after its first value: a body could carry more
// after it, beyond the bound.)
func decodeWhole(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConsentBody))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil {
		writeInvalidBody(w)
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeInvalidBody(w)
		return false
	}
	return true
}

// answer writes the service's answer, or its refusal.
func (a *consentAPI) answer(w http.ResponseWriter, r *http.Request, out any, err error) {
	if err != nil {
		consentFailed(w, r, a.cfg.Log, err)
		return
	}
	writeJSON(w, out)
}

// consentStatus is the status of a refusal of connections that failed does
// not know: 409 for one that says how things are now (the screen loads them
// again, or the person removes a device or a connection first), 502 for a
// vendor's document that could not be read. Any other code is failed's.
var consentStatus = map[string]int{
	contracts.ErrConsentChanged.Code:     http.StatusConflict,
	contracts.ErrModelNoticeChanged.Code: http.StatusConflict,
	contracts.ErrTooManyDevices.Code:     http.StatusConflict,
	contracts.ErrTooManyConnections.Code: http.StatusConflict,
	contracts.ErrRequestEnded.Code:       http.StatusConflict,
	contracts.ErrConnectionEnded.Code:    http.StatusConflict,
	contracts.ErrVendorUnreachable.Code:  http.StatusBadGateway,
}

// consentFailed answers a refusal of the person's routes: a contract error
// with its code and fixed words (a formula's problem with its details, an
// invalid client with its reason, contracts.ClientError, when it is one of
// the three), any other failure as internalFailure answers it.
func consentFailed(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	if ce, ok := errors.AsType[*contracts.ClientError](err); ok {
		var details contracts.ErrorDetails
		if ce.Problem.Valid() {
			details = ce.Details()
		}
		writeErrorWith(w, http.StatusBadRequest, contracts.ErrInvalidClient.Code, contracts.ErrInvalidClient.Message, details)
		return
	}
	if known, ok := errors.AsType[*contracts.Error](err); ok {
		if status, ok := consentStatus[known.Code]; ok {
			writeError(w, status, known.Code, known.Message)
			return
		}
	}
	if !refusal(err) {
		internalFailure(w, r, log, "connections", err)
		return
	}
	failed(w, r, log, err)
}

// refusal says err is one a caller is answered with: a contract error or a
// formula's problem.
func refusal(err error) bool {
	_, known := errors.AsType[*contracts.Error](err)
	_, problem := errors.AsType[*query.Problem](err)
	return known || problem
}

// internalFailure answers 500 for a failure of the connections service's
// own, and logs it by its kind alone: never its text, which may hold a
// token, a code, a connection, a device, a vendor's URL or anything else
// domain.Redact does not know of (ADR 0030, "Logging"). A request its
// client left is not logged (clientLeft).
func internalFailure(w http.ResponseWriter, r *http.Request, log *slog.Logger, where string, err error) {
	if clientLeft(r, err) {
		setOutcome(r, outcomeClientLeft)
	} else {
		kind := "failed"
		if errors.Is(err, context.DeadlineExceeded) {
			kind = "deadline"
		}
		log.Error(where+": internal error", "kind", kind)
	}
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}
