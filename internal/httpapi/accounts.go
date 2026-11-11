package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/services/accounts"
)

// AccountsService is what the accounts API needs from the service.
type AccountsService interface {
	StartEmail(ctx context.Context, in contracts.EmailStart) (*contracts.EmailStarted, error)
	LookEmail(ctx context.Context, in contracts.EmailLook) (*contracts.EmailPending, error)
	FinishEmail(ctx context.Context, in contracts.EmailFinish, userAgent string) (*contracts.Me, string, error)
	Authenticate(ctx context.Context, cookie string) (*accounts.Authenticated, error)
	Me(ctx context.Context, who *accounts.Authenticated) (*contracts.Me, error)
	Sessions(ctx context.Context, who *accounts.Authenticated) (*contracts.SessionList, error)
	CloseSession(ctx context.Context, who *accounts.Authenticated, id string) error
	CreateInvitations(ctx context.Context, count int) (*contracts.Invitations, error)
}

// SessionCookie is the cookie of a signed-in browser. The __Host- prefix
// makes the browser keep it for this host alone, over TLS, so the public
// site on the parent domain can neither read it nor put another in its place.
const SessionCookie = "__Host-permaily-session"

// identifiers are taken out of what is logged: logs leave the enclave, and
// would say which account did what, and when.
var identifiers = regexp.MustCompile(`\b(acc|ses|att)_[a-z2-7]{26}`)

// failed answers a request with the error of a service. An error a caller
// can act on goes out with its code; anything else is logged and answered
// with nothing of its own.
func failed(w http.ResponseWriter, log *slog.Logger, err error) {
	var known *contracts.Error
	if !errors.As(err, &known) {
		log.Error("api: internal error", "err", identifiers.ReplaceAllString(err.Error(), "${1}_…"))
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	status := http.StatusBadRequest
	switch known.Code {
	case contracts.ErrUnauthorized.Code:
		status = http.StatusUnauthorized
	case contracts.ErrNotFound.Code:
		status = http.StatusNotFound
	case contracts.ErrTooManyRequests.Code:
		status = http.StatusTooManyRequests
	}
	writeError(w, status, known.Code, known.Message)
}

// maxAccountsBody bounds every request of the accounts API.
const maxAccountsBody = 4 << 10

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAccountsBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "the request is not the JSON this endpoint takes")
		return false
	}
	return true
}

// fromTheApp lets a request that changes something through only when the
// page that made it is the app itself. The session cookie is SameSite=Strict
// already; this holds whatever a browser does with cookies, and for the
// requests that carry none (starting a sign-in).
func fromTheApp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			site := r.Header.Get("Sec-Fetch-Site")
			if r.Header.Get("Origin") != "https://"+r.Host || (site != "" && site != "same-origin") {
				writeError(w, http.StatusForbidden, "forbidden", "this request did not come from the app")
				return
			}
			// A form of another site cannot send JSON without asking first.
			if r.ContentLength != 0 && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				writeError(w, http.StatusUnsupportedMediaType, "invalid_body", "send JSON")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func setSession(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: value, Path: "/", MaxAge: maxAge,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

// AccountsAPI returns the routes of the web app's own API, under /api/v1/:
// signing in, the account and its sessions. It is served on the app's host
// only, and answers only to the app (cookies, same origin).
func AccountsAPI(svc AccountsService, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	// signedIn runs next with the session of the request's cookie.
	signedIn := func(next func(http.ResponseWriter, *http.Request, *accounts.Authenticated)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(SessionCookie)
			if err != nil {
				failed(w, log, contracts.ErrUnauthorized)
				return
			}
			who, err := svc.Authenticate(r.Context(), c.Value)
			if err != nil {
				if errors.Is(err, contracts.ErrUnauthorized) {
					setSession(w, "", -1) // a cookie that stands for nothing: drop it
				}
				failed(w, log, err)
				return
			}
			next(w, r, who)
		}
	}
	mux.HandleFunc("POST /api/v1/auth/email/start", func(w http.ResponseWriter, r *http.Request) {
		var in contracts.EmailStart
		if !decode(w, r, &in) {
			return
		}
		out, err := svc.StartEmail(r.Context(), in)
		if err != nil {
			failed(w, log, err)
			return
		}
		writeJSON(w, out)
	})
	// A POST, so the token of the link is never part of an address.
	mux.HandleFunc("POST /api/v1/auth/email/look", func(w http.ResponseWriter, r *http.Request) {
		var in contracts.EmailLook
		if !decode(w, r, &in) {
			return
		}
		out, err := svc.LookEmail(r.Context(), in)
		if err != nil {
			failed(w, log, err)
			return
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("POST /api/v1/auth/email/finish", func(w http.ResponseWriter, r *http.Request) {
		var in contracts.EmailFinish
		if !decode(w, r, &in) {
			return
		}
		me, cookie, err := svc.FinishEmail(r.Context(), in, r.UserAgent())
		if err != nil {
			failed(w, log, err)
			return
		}
		setSession(w, cookie, int(domain.SessionMax/time.Second))
		writeJSON(w, me)
	})
	mux.HandleFunc("POST /api/v1/auth/sign-out", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		if err := svc.CloseSession(r.Context(), who, who.Session.ID); err != nil && !errors.Is(err, contracts.ErrNotFound) {
			failed(w, log, err)
			return
		}
		setSession(w, "", -1)
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /api/v1/me", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		me, err := svc.Me(r.Context(), who)
		if err != nil {
			failed(w, log, err)
			return
		}
		writeJSON(w, me)
	}))
	mux.HandleFunc("GET /api/v1/sessions", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		list, err := svc.Sessions(r.Context(), who)
		if err != nil {
			failed(w, log, err)
			return
		}
		writeJSON(w, list)
	}))
	mux.HandleFunc("DELETE /api/v1/sessions/{id}", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		if err := svc.CloseSession(r.Context(), who, r.PathValue("id")); err != nil {
			failed(w, log, err)
			return
		}
		if r.PathValue("id") == who.Session.ID {
			setSession(w, "", -1)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	// Anything else under /api/ is not the web app's to answer with a page.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	return fromTheApp(mux)
}

// Invitations returns the handler for POST /v1/admin/invitations, which makes
// single-use sign-up codes. Put it behind TokenAuth with the operator's
// token: whoever holds it decides who may sign up, and nothing else of an
// account.
func Invitations(svc AccountsService, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
			return
		}
		in := contracts.InvitationRequest{Count: 1}
		if r.ContentLength != 0 && !decode(w, r, &in) {
			return
		}
		out, err := svc.CreateInvitations(r.Context(), in.Count)
		if err != nil {
			failed(w, log, err)
			return
		}
		writeJSON(w, out)
	})
}
