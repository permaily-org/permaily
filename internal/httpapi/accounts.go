package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/idp"
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

	StartPasskeySignIn(ctx context.Context) (*contracts.PasskeyRequest, error)
	FinishPasskeySignIn(ctx context.Context, in contracts.PasskeySignIn, userAgent string) (*contracts.Me, string, error)
	Recover(ctx context.Context, in contracts.Recovery, userAgent string) (*contracts.Me, string, error)
	Passkeys(ctx context.Context, who *accounts.Authenticated) (*contracts.PasskeyList, error)
	StartPasskey(ctx context.Context, who *accounts.Authenticated) (*contracts.PasskeyCreation, error)
	FinishPasskey(ctx context.Context, who *accounts.Authenticated, in contracts.PasskeyFinish) (*contracts.Passkey, error)
	RenamePasskey(ctx context.Context, who *accounts.Authenticated, id string, in contracts.PasskeyRename) (*contracts.Passkey, error)
	RemovePasskey(ctx context.Context, who *accounts.Authenticated, id string) error
	SetPasskeyOnly(ctx context.Context, who *accounts.Authenticated, in contracts.PasskeyOnly) (*contracts.PasskeyOnlyState, error)
	NewRecoveryCodes(ctx context.Context, who *accounts.Authenticated) (*contracts.RecoveryCodes, error)

	UpdateAccount(ctx context.Context, who *accounts.Authenticated, in contracts.AccountUpdate) (*contracts.Account, error)
	StartDeletion(ctx context.Context, who *accounts.Authenticated) (*contracts.Account, error)
	CancelDeletion(ctx context.Context, who *accounts.Authenticated) (*contracts.Account, error)
	AccountRecord(ctx context.Context, id string) (*contracts.AccountRecord, error)
	AccountRecords(ctx context.Context, after string, limit int) (*contracts.AccountList, error)

	ProvidersOn(ctx context.Context) (*contracts.ProvidersOn, error)
	StartProvider(ctx context.Context, who *accounts.Authenticated, in contracts.ProviderStart) (*contracts.ProviderStarted, string, error)
	FinishProvider(ctx context.Context, in accounts.ProviderAnswer) (*accounts.ProviderDone, error)
	PendingProvider(ctx context.Context, browser string) (*contracts.ProviderPending, error)
	StartProviderConfirm(ctx context.Context, browser string, in contracts.ProviderConfirmStart) (*contracts.EmailStarted, error)
	FinishProviderConfirm(ctx context.Context, browser string, in contracts.ProviderConfirm, userAgent string) (*contracts.Me, string, error)
	LinkedProviders(ctx context.Context, who *accounts.Authenticated) (*contracts.LinkedProviders, error)
	UnlinkProvider(ctx context.Context, who *accounts.Authenticated, provider string) error
	ProviderApps(ctx context.Context) (*contracts.ProviderApps, error)
	SetProviderApp(ctx context.Context, provider string, in contracts.ProviderAppSet) (*contracts.ProviderApp, error)
	RemoveProviderApp(ctx context.Context, provider string) error
}

// SessionCookie is the cookie of a signed-in browser. The __Host- prefix
// makes the browser keep it for this host alone, over TLS, so the public
// site on the parent domain can neither read it nor put another in its place.
const SessionCookie = "__Host-permaily-session"

// ProviderCookie ties a trip to a provider, and a sign-in that waits for its
// address to be confirmed, to the browser that started it. The provider
// sends the browser back from another site, where a SameSite=Strict cookie
// would not come along: this one is Lax, and stands for nothing but the trip.
const ProviderCookie = "__Host-permaily-provider"

// identifiers are taken out of what is logged: logs leave the enclave, and
// would say which account did what, and when.
var identifiers = regexp.MustCompile(`\b(acc|ses|att|cer)_[a-z2-7]{26}`)

// failed answers a request with the error of a service. An error a caller
// can act on goes out with its code; anything else is logged and answered
// with nothing of its own.
func failed(w http.ResponseWriter, log *slog.Logger, err error) {
	var known *contracts.Error
	if !errors.As(err, &known) {
		internal(log, err)
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
	case contracts.ErrLastPasskey.Code, contracts.ErrPasskeyNeeded.Code, contracts.ErrPasskeyOnlyOff.Code, contracts.ErrTooManyPasskeys.Code,
		contracts.ErrAccountClosing.Code, contracts.ErrProviderOff.Code, contracts.ErrProviderInUse.Code, contracts.ErrProviderLinked.Code,
		contracts.ErrLastWayIn.Code:
		// Well asked, and refused for how the account is now.
		status = http.StatusConflict
	}
	writeError(w, status, known.Code, known.Message)
}

// internal logs a failure that is the service's own, without the accounts it
// was about.
func internal(log *slog.Logger, err error) {
	log.Error("api: internal error", "err", identifiers.ReplaceAllString(err.Error(), "${1}_…"))
}

// maxAccountsBody bounds every request of the accounts API but the two that
// carry a browser's answer about a passkey, which maxPasskeyBody bounds: a
// credential's identifier may be a kilobyte, twice in the answer, next to a
// key and what the authenticator says of itself.
const (
	maxAccountsBody = 4 << 10
	maxPasskeyBody  = 32 << 10
)

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeUpTo(w, r, v, maxAccountsBody)
}

func decodeUpTo(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
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

func setProvider(w http.ResponseWriter, value string, life time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: ProviderCookie, Value: value, Path: "/", MaxAge: int(life / time.Second),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

// provider is the value of the browser's provider cookie, "" without one.
func provider(r *http.Request) string {
	c, err := r.Cookie(ProviderCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// AccountsAPI returns the routes of the web app's own API, under /api/v1/:
// signing in, the account, its sessions and its passkeys. It is served on the app's host
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
	// signedInWith answers a way of signing in that worked: the account, and
	// the session in its cookie.
	signedInWith := func(w http.ResponseWriter, me *contracts.Me, cookie string, err error) {
		if err != nil {
			failed(w, log, err)
			return
		}
		setSession(w, cookie, int(domain.SessionMax/time.Second))
		writeJSON(w, me)
	}
	// answer writes what a service gave, or its error.
	answer := func(w http.ResponseWriter, out any, err error) {
		if err != nil {
			failed(w, log, err)
			return
		}
		writeJSON(w, out)
	}
	mux.HandleFunc("POST /api/v1/auth/passkey/start", func(w http.ResponseWriter, r *http.Request) {
		out, err := svc.StartPasskeySignIn(r.Context())
		answer(w, out, err)
	})
	mux.HandleFunc("POST /api/v1/auth/passkey/finish", func(w http.ResponseWriter, r *http.Request) {
		var in contracts.PasskeySignIn
		if !decodeUpTo(w, r, &in, maxPasskeyBody) {
			return
		}
		me, cookie, err := svc.FinishPasskeySignIn(r.Context(), in, r.UserAgent())
		signedInWith(w, me, cookie, err)
	})
	mux.HandleFunc("POST /api/v1/auth/recovery", func(w http.ResponseWriter, r *http.Request) {
		var in contracts.Recovery
		if !decode(w, r, &in) {
			return
		}
		me, cookie, err := svc.Recover(r.Context(), in, r.UserAgent())
		signedInWith(w, me, cookie, err)
	})
	mux.HandleFunc("GET /api/v1/passkeys", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		out, err := svc.Passkeys(r.Context(), who)
		answer(w, out, err)
	}))
	mux.HandleFunc("POST /api/v1/passkeys/start", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		out, err := svc.StartPasskey(r.Context(), who)
		answer(w, out, err)
	}))
	mux.HandleFunc("POST /api/v1/passkeys/finish", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		var in contracts.PasskeyFinish
		if !decodeUpTo(w, r, &in, maxPasskeyBody) {
			return
		}
		out, err := svc.FinishPasskey(r.Context(), who, in)
		answer(w, out, err)
	}))
	mux.HandleFunc("PATCH /api/v1/passkeys/{id}", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		var in contracts.PasskeyRename
		if !decode(w, r, &in) {
			return
		}
		out, err := svc.RenamePasskey(r.Context(), who, r.PathValue("id"), in)
		answer(w, out, err)
	}))
	mux.HandleFunc("DELETE /api/v1/passkeys/{id}", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		if err := svc.RemovePasskey(r.Context(), who, r.PathValue("id")); err != nil {
			failed(w, log, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /api/v1/account/passkey-only", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		var in contracts.PasskeyOnly
		if !decode(w, r, &in) {
			return
		}
		out, err := svc.SetPasskeyOnly(r.Context(), who, in)
		answer(w, out, err)
	}))
	mux.HandleFunc("POST /api/v1/account/recovery-codes", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		out, err := svc.NewRecoveryCodes(r.Context(), who)
		answer(w, out, err)
	}))
	mux.HandleFunc("PATCH /api/v1/account", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		var in contracts.AccountUpdate
		if !decode(w, r, &in) {
			return
		}
		out, err := svc.UpdateAccount(r.Context(), who, in)
		answer(w, out, err)
	}))
	// Deleting takes no body: the session says whose account it is.
	mux.HandleFunc("POST /api/v1/account/delete", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		out, err := svc.StartDeletion(r.Context(), who)
		answer(w, out, err)
	}))
	mux.HandleFunc("POST /api/v1/account/delete/cancel", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		out, err := svc.CancelDeletion(r.Context(), who)
		answer(w, out, err)
	}))
	mux.HandleFunc("GET /api/v1/auth/providers", func(w http.ResponseWriter, r *http.Request) {
		out, err := svc.ProvidersOn(r.Context())
		answer(w, out, err)
	})
	// startProvider answers where to send the browser, and gives it the
	// cookie that the provider's answer is taken with.
	startProvider := func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		var in contracts.ProviderStart
		if !decode(w, r, &in) {
			return
		}
		out, cookie, err := svc.StartProvider(r.Context(), who, in)
		if err != nil {
			failed(w, log, err)
			return
		}
		setProvider(w, cookie, domain.ProviderLifetime)
		writeJSON(w, out)
	}
	mux.HandleFunc("POST /api/v1/auth/provider/start", func(w http.ResponseWriter, r *http.Request) { startProvider(w, r, nil) })
	// Where the provider sends the browser back. It is a navigation and not
	// a request of the app, so it answers with where to go: a path of the
	// app that the service chose, with the reason in the fragment when it
	// failed, and next to it the provider when the service still knew the
	// trip, for the screen to name it. What the provider said went wrong is
	// never read.
	mux.HandleFunc("GET /api/v1/auth/provider/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		done, err := svc.FinishProvider(r.Context(), accounts.ProviderAnswer{
			State: q.Get("state"), Code: q.Get("code"), Refused: q.Has("error"),
			Browser: provider(r), UserAgent: r.UserAgent(),
		})
		var known *contracts.Error
		if err != nil && !errors.As(err, &known) {
			internal(log, err)
		}
		// The trip is over. The cookie goes, unless a sign-in now waits in
		// this browser for its address.
		if done.Browser != "" {
			setProvider(w, done.Browser, domain.ProviderConfirmLifetime)
		} else {
			setProvider(w, "", -time.Second)
		}
		if done.Session != "" {
			setSession(w, done.Session, int(domain.SessionMax/time.Second))
		}
		to := done.Path
		// Only ever one of the fixed identifiers: the fragment holds nothing
		// a provider or a person wrote.
		if err != nil && slices.Contains(idp.Providers, done.Provider) {
			to += "&provider=" + string(done.Provider)
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, to, http.StatusSeeOther)
	})
	mux.HandleFunc("GET /api/v1/auth/provider/pending", func(w http.ResponseWriter, r *http.Request) {
		out, err := svc.PendingProvider(r.Context(), provider(r))
		answer(w, out, err)
	})
	mux.HandleFunc("POST /api/v1/auth/provider/confirm/start", func(w http.ResponseWriter, r *http.Request) {
		var in contracts.ProviderConfirmStart
		if !decode(w, r, &in) {
			return
		}
		out, err := svc.StartProviderConfirm(r.Context(), provider(r), in)
		answer(w, out, err)
	})
	mux.HandleFunc("POST /api/v1/auth/provider/confirm/finish", func(w http.ResponseWriter, r *http.Request) {
		var in contracts.ProviderConfirm
		if !decode(w, r, &in) {
			return
		}
		me, cookie, err := svc.FinishProviderConfirm(r.Context(), provider(r), in, r.UserAgent())
		if err == nil {
			setProvider(w, "", -time.Second) // it waited for this
		}
		signedInWith(w, me, cookie, err)
	})
	mux.HandleFunc("GET /api/v1/providers", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		out, err := svc.LinkedProviders(r.Context(), who)
		answer(w, out, err)
	}))
	// From Settings: the same trip, which links to the session's account.
	mux.HandleFunc("POST /api/v1/providers/start", signedIn(startProvider))
	mux.HandleFunc("DELETE /api/v1/providers/{provider}", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		if err := svc.UnlinkProvider(r.Context(), who, r.PathValue("provider")); err != nil {
			failed(w, log, err)
			return
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

// Accounts returns the handler of the operator's tool, which only looks:
// GET /v1/admin/accounts lists the accounts, the oldest first (limit, and
// after with the next of the page before), and GET /v1/admin/accounts/{id}
// shows one. What it answers is read from the accounts' signed records, so it
// is what the enclave vouches for and not what the table says. Put it behind
// TokenAuth with the operator's token.
func Accounts(svc AccountsService, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/admin/accounts", func(w http.ResponseWriter, r *http.Request) {
		limit := domain.DefaultAccountsPage
		if v := r.URL.Query().Get("limit"); v != "" {
			var err error
			if limit, err = strconv.Atoi(v); err != nil {
				failed(w, log, contracts.ErrInvalidLimit)
				return
			}
		}
		out, err := svc.AccountRecords(r.Context(), r.URL.Query().Get("after"), limit)
		if err != nil {
			failed(w, log, err)
			return
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /v1/admin/accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, err := svc.AccountRecord(r.Context(), r.PathValue("id"))
		if err != nil {
			failed(w, log, err)
			return
		}
		writeJSON(w, out)
	})
	// The tool changes nothing: a person deletes their own account.
	onlyLooks := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
	}
	mux.HandleFunc("/v1/admin/accounts", onlyLooks)
	mux.HandleFunc("/v1/admin/accounts/{id}", onlyLooks)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	return mux
}

// ProviderApps returns the handler of the applications registered at the
// providers: PUT /v1/admin/providers/{provider} gives the enclave one's
// client id and secret, DELETE takes them away, and GET /v1/admin/providers
// lists which are set, with their client ids and never a secret. Put it
// behind TokenAuth with the operator's token, on a connection the operator
// verified: the secret is sent to whoever answers.
func ProviderApps(svc AccountsService, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/admin/providers", func(w http.ResponseWriter, r *http.Request) {
		out, err := svc.ProviderApps(r.Context())
		if err != nil {
			failed(w, log, err)
			return
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("PUT /v1/admin/providers/{provider}", func(w http.ResponseWriter, r *http.Request) {
		var in contracts.ProviderAppSet
		if !decode(w, r, &in) {
			return
		}
		out, err := svc.SetProviderApp(r.Context(), r.PathValue("provider"), in)
		if err != nil {
			failed(w, log, err)
			return
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("DELETE /v1/admin/providers/{provider}", func(w http.ResponseWriter, r *http.Request) {
		if err := svc.RemoveProviderApp(r.Context(), r.PathValue("provider")); err != nil {
			failed(w, log, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	refuse := func(allow string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Allow", allow)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use "+allow)
		}
	}
	mux.HandleFunc("/v1/admin/providers", refuse(http.MethodGet))
	mux.HandleFunc("/v1/admin/providers/{provider}", refuse("PUT, DELETE"))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	return mux
}
