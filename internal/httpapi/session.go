package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/accounts"
)

// The session and same-origin helper of the web app's own API, shared by the
// accounts API and the mailboxes API (ADR 0026, pull request 2, which took it
// out of AccountsAPI unchanged). Its surface is Sessions, withSession,
// fromTheApp and setSession, and nothing else.

// Sessions is what the helper needs of accounts: the session behind a
// cookie's value. *accounts.Service implements it.
type Sessions interface {
	Authenticate(ctx context.Context, cookie string) (*accounts.Authenticated, error)
}

// withSession runs next with the session of the request's cookie. Without a
// cookie, or with one that stands for nothing, it answers 401 and drops the
// cookie; any other failure is the service's, answered as failed answers it.
func withSession(auth Sessions, log *slog.Logger, next func(http.ResponseWriter, *http.Request, *accounts.Authenticated)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(SessionCookie)
		if err != nil {
			failed(w, log, contracts.ErrUnauthorized)
			return
		}
		who, err := auth.Authenticate(r.Context(), c.Value)
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

// setSession sets the session cookie: HttpOnly, Secure, SameSite=Strict, for
// this host alone (__Host-). A maxAge below zero drops it.
func setSession(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: value, Path: "/", MaxAge: maxAge,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}
