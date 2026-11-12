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

// OperatorService is everything the operator's routes, and the admin panel's,
// can ask of the service about accounts and invitations: look, block and
// unblock, ask to delete and cancel that, sign devices out, list and revoke
// invitations, and read the counts, the admins and the admin log. Nothing here
// changes a contact address or a way to sign in, turns "passkey only" off or
// signs anyone in, and the handlers of those routes are given nothing else:
// what is not in this list they cannot call.
type OperatorService interface {
	AccountRecord(ctx context.Context, id string) (*contracts.AccountRecord, error)
	AccountRecords(ctx context.Context, after string, limit int) (*contracts.AccountList, error)
	AccountRecordByAddress(ctx context.Context, address string) (*contracts.AccountList, error)
	BlockAccount(ctx context.Context, id string) (*contracts.AccountRecord, error)
	UnblockAccount(ctx context.Context, id string) (*contracts.AccountRecord, error)
	DeleteAccount(ctx context.Context, id string) (*contracts.AccountRecord, error)
	KeepAccount(ctx context.Context, id string) (*contracts.AccountRecord, error)
	CloseAccountSessions(ctx context.Context, id string) (*contracts.AccountRecord, error)

	CreateInvitations(ctx context.Context, count int) (*contracts.Invitations, error)
	Invitations(ctx context.Context) (*contracts.InvitationList, error)
	RevokeInvitation(ctx context.Context, id string) error

	Stats(ctx context.Context) (*contracts.Stats, error)
	Admins(ctx context.Context) (*contracts.AdminList, error)
	AdminLog(ctx context.Context, after string, limit int) (*contracts.AdminLog, error)
}

// ProviderAppService is the applications registered at the providers: which
// are set, and setting and removing one.
type ProviderAppService interface {
	ProviderApps(ctx context.Context) (*contracts.ProviderApps, error)
	SetProviderApp(ctx context.Context, provider string, in contracts.ProviderAppSet) (*contracts.ProviderApp, error)
	RemoveProviderApp(ctx context.Context, provider string) error
}

// AdminOperator is everything the admin panel's routes can ask of the
// service, as one admin: what the operator's routes can, the providers'
// applications, and making and removing admins. It holds nothing that
// changes an address or a way into an account (accounts.Admin is it).
type AdminOperator interface {
	OperatorService
	ProviderAppService
	MakeAdmin(ctx context.Context, id string) (*contracts.AdminList, error)
	RemoveAdmin(ctx context.Context, id string) (*contracts.AdminList, error)
}

// AccountsService is what the accounts API needs from the service.
type AccountsService interface {
	OperatorService
	ProviderAppService

	StartEmail(ctx context.Context, in contracts.EmailStart) (*contracts.EmailStarted, error)
	LookEmail(ctx context.Context, in contracts.EmailLook) (*contracts.EmailPending, error)
	FinishEmail(ctx context.Context, in contracts.EmailFinish, from accounts.Browser) (*contracts.Me, string, error)
	Authenticate(ctx context.Context, cookie string) (*accounts.Authenticated, error)
	Me(ctx context.Context, who *accounts.Authenticated) (*contracts.Me, error)
	Sessions(ctx context.Context, who *accounts.Authenticated) (*contracts.SessionList, error)
	CloseSession(ctx context.Context, who *accounts.Authenticated, id string) error

	StartPasskeySignIn(ctx context.Context) (*contracts.PasskeyRequest, error)
	FinishPasskeySignIn(ctx context.Context, in contracts.PasskeySignIn, from accounts.Browser) (*contracts.Me, string, error)
	Recover(ctx context.Context, in contracts.Recovery, from accounts.Browser) (*contracts.Me, string, error)
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
	StartAddressChange(ctx context.Context, who *accounts.Authenticated, in contracts.AddressStart) (*contracts.EmailStarted, error)
	FinishAddressChange(ctx context.Context, who *accounts.Authenticated, in contracts.AddressFinish) (*contracts.Account, error)

	ProvidersOn(ctx context.Context) (*contracts.ProvidersOn, error)
	StartProvider(ctx context.Context, who *accounts.Authenticated, in contracts.ProviderStart) (*contracts.ProviderStarted, string, error)
	FinishProvider(ctx context.Context, in accounts.ProviderAnswer) (*accounts.ProviderDone, error)
	PendingProvider(ctx context.Context, browser string) (*contracts.ProviderPending, error)
	StartProviderConfirm(ctx context.Context, browser string, in contracts.ProviderConfirmStart) (*contracts.EmailStarted, error)
	FinishProviderConfirm(ctx context.Context, browser string, in contracts.ProviderConfirm, from accounts.Browser) (*contracts.Me, string, error)
	LinkedProviders(ctx context.Context, who *accounts.Authenticated) (*contracts.LinkedProviders, error)
	UnlinkProvider(ctx context.Context, who *accounts.Authenticated, provider string) error

	ConfirmWays(ctx context.Context, who *accounts.Authenticated) (*contracts.ConfirmWays, error)
	StartConfirmPasskey(ctx context.Context, who *accounts.Authenticated) (*contracts.PasskeyRequest, error)
	FinishConfirmPasskey(ctx context.Context, who *accounts.Authenticated, in contracts.PasskeySignIn) (*contracts.Me, error)
	StartConfirmEmail(ctx context.Context, who *accounts.Authenticated) (*contracts.EmailStarted, error)
	FinishConfirmEmail(ctx context.Context, who *accounts.Authenticated, in contracts.ConfirmFinish) (*contracts.Me, error)

	// Admin is what the admin behind a session can do: not_found for anyone
	// else.
	Admin(ctx context.Context, who *accounts.Authenticated) (*accounts.Admin, error)
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

// DeviceCookie names the browser to the accounts it signs in to, so that a
// person is told by mail of a sign-in from a device that had not signed in
// to their account before. It is random, stands for nothing and signs nobody
// in. It is Lax, like the provider's cookie: it has to come along when a
// provider sends the browser back. It is never logged.
const DeviceCookie = "__Host-permaily-device"

// identifiers are taken out of what is logged: logs leave the enclave, and
// would say which account did what, and when.
var identifiers = regexp.MustCompile(`\b(acc|ses|att|cer|inv)_[a-z2-7]{26}`)

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
	case contracts.ErrAccountBlocked.Code, contracts.ErrConfirmationNeeded.Code, contracts.ErrPasskeyOnlyNeeded.Code, contracts.ErrSuperAdminOnly.Code:
		// Who it is was proved, and the account lets nobody in; or who it is
		// was not proved lately, or is not enough for what was asked.
		status = http.StatusForbidden
	case contracts.ErrLastPasskey.Code, contracts.ErrPasskeyNeeded.Code, contracts.ErrPasskeyOnlyOff.Code, contracts.ErrTooManyPasskeys.Code,
		contracts.ErrAccountClosing.Code, contracts.ErrProviderOff.Code, contracts.ErrProviderInUse.Code, contracts.ErrProviderLinked.Code,
		contracts.ErrLastWayIn.Code, contracts.ErrAddressInUse.Code, contracts.ErrNotCancellable.Code, contracts.ErrPasskeyRequired.Code:
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

// browser is what a sign-in is told of the browser it ends in: what it says
// of itself, and the value of its device cookie. A browser without one, or
// with one that cannot be one, is given a new value, which setDevice makes
// its cookie when the sign-in works.
func browser(r *http.Request) accounts.Browser {
	from := accounts.Browser{UserAgent: r.UserAgent()}
	if c, err := r.Cookie(DeviceCookie); err == nil && domain.DeviceToken(c.Value) {
		from.Device = c.Value
	} else {
		from.Device = accounts.NewDevice()
	}
	return from
}

// setDevice gives the browser its device cookie, for as long as an account
// knows a device. It is set again at each sign-in, so it lasts while the
// browser keeps signing in.
func setDevice(w http.ResponseWriter, from accounts.Browser) {
	http.SetCookie(w, &http.Cookie{
		Name: DeviceCookie, Value: from.Device, Path: "/", MaxAge: int(domain.DeviceLifetime / time.Second),
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
		from := browser(r)
		me, cookie, err := svc.FinishEmail(r.Context(), in, from)
		if err != nil {
			failed(w, log, err)
			return
		}
		setSession(w, cookie, int(domain.SessionMax/time.Second))
		setDevice(w, from)
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
	signedInWith := func(w http.ResponseWriter, from accounts.Browser, me *contracts.Me, cookie string, err error) {
		if err != nil {
			failed(w, log, err)
			return
		}
		setSession(w, cookie, int(domain.SessionMax/time.Second))
		setDevice(w, from)
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
		from := browser(r)
		me, cookie, err := svc.FinishPasskeySignIn(r.Context(), in, from)
		signedInWith(w, from, me, cookie, err)
	})
	mux.HandleFunc("POST /api/v1/auth/recovery", func(w http.ResponseWriter, r *http.Request) {
		var in contracts.Recovery
		if !decode(w, r, &in) {
			return
		}
		from := browser(r)
		me, cookie, err := svc.Recover(r.Context(), in, from)
		signedInWith(w, from, me, cookie, err)
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
	// Changing the contact address: a code goes to the new one, and proves it.
	mux.HandleFunc("POST /api/v1/account/address/start", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		var in contracts.AddressStart
		if !decode(w, r, &in) {
			return
		}
		out, err := svc.StartAddressChange(r.Context(), who, in)
		answer(w, out, err)
	}))
	mux.HandleFunc("POST /api/v1/account/address/finish", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		var in contracts.AddressFinish
		if !decode(w, r, &in) {
			return
		}
		out, err := svc.FinishAddressChange(r.Context(), who, in)
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
		q, from := r.URL.Query(), browser(r)
		done, err := svc.FinishProvider(r.Context(), accounts.ProviderAnswer{
			State: q.Get("state"), Code: q.Get("code"), Refused: q.Has("error"),
			Browser: provider(r), From: from,
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
			setDevice(w, from)
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
		from := browser(r)
		me, cookie, err := svc.FinishProviderConfirm(r.Context(), provider(r), in, from)
		if err == nil {
			setProvider(w, "", -time.Second) // it waited for this
		}
		signedInWith(w, from, me, cookie, err)
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
	// Confirming it is them again, for the changes that need a fresh proof.
	mux.HandleFunc("GET /api/v1/auth/confirm", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		out, err := svc.ConfirmWays(r.Context(), who)
		answer(w, out, err)
	}))
	mux.HandleFunc("POST /api/v1/auth/confirm/passkey/start", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		out, err := svc.StartConfirmPasskey(r.Context(), who)
		answer(w, out, err)
	}))
	mux.HandleFunc("POST /api/v1/auth/confirm/passkey/finish", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		var in contracts.PasskeySignIn
		if !decodeUpTo(w, r, &in, maxPasskeyBody) {
			return
		}
		out, err := svc.FinishConfirmPasskey(r.Context(), who, in)
		answer(w, out, err)
	}))
	mux.HandleFunc("POST /api/v1/auth/confirm/email/start", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		out, err := svc.StartConfirmEmail(r.Context(), who)
		answer(w, out, err)
	}))
	mux.HandleFunc("POST /api/v1/auth/confirm/email/finish", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		var in contracts.ConfirmFinish
		if !decode(w, r, &in) {
			return
		}
		out, err := svc.FinishConfirmEmail(r.Context(), who, in)
		answer(w, out, err)
	}))
	// The admin panel: a session of an admin, or nothing at all. Who is not
	// an admin is answered not_found, whatever path and method they ask.
	panel := adminPanel(log)
	mux.Handle("/api/v1/admin/", signedIn(func(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) {
		admin, err := svc.Admin(r.Context(), who)
		if err != nil {
			failed(w, log, err)
			return
		}
		var op AdminOperator = admin
		panel.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), adminKey{}, op)))
	}))
	// Anything else under /api/ is not the web app's to answer with a page.
	mux.HandleFunc("/api/", noSuchEndpoint)
	return fromTheApp(mux)
}

// adminKey carries, in a request of the admin panel, what its admin can do.
type adminKey struct{}

func adminOf(r *http.Request) AdminOperator { return r.Context().Value(adminKey{}).(AdminOperator) }

// adminPanel returns the routes of the admin panel, under /api/v1/admin/, for
// a request that carries its admin (adminKey): the operator's routes, the
// providers' applications, and the admins, the log and the counts.
func adminPanel(log *slog.Logger) http.Handler {
	const prefix = "/api/v1/admin"
	op := func(r *http.Request) OperatorService { return adminOf(r) }
	mux := http.NewServeMux()
	accountRoutes(mux, prefix, op, log)
	invitationRoutes(mux, prefix, op, log)
	providerAppRoutes(mux, prefix, func(r *http.Request) ProviderAppService { return adminOf(r) }, log)
	mux.HandleFunc("GET "+prefix+"/admins", func(w http.ResponseWriter, r *http.Request) {
		out, err := adminOf(r).Admins(r.Context())
		answerWith(w, log, out, err)
	})
	mux.HandleFunc("PUT "+prefix+"/admins/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, err := adminOf(r).MakeAdmin(r.Context(), r.PathValue("id"))
		answerWith(w, log, out, err)
	})
	mux.HandleFunc("DELETE "+prefix+"/admins/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, err := adminOf(r).RemoveAdmin(r.Context(), r.PathValue("id"))
		answerWith(w, log, out, err)
	})
	mux.HandleFunc("GET "+prefix+"/log", func(w http.ResponseWriter, r *http.Request) {
		limit, ok := pageLimit(w, r, log)
		if !ok {
			return
		}
		out, err := adminOf(r).AdminLog(r.Context(), r.URL.Query().Get("after"), limit)
		answerWith(w, log, out, err)
	})
	mux.HandleFunc("GET "+prefix+"/stats", func(w http.ResponseWriter, r *http.Request) {
		out, err := adminOf(r).Stats(r.Context())
		answerWith(w, log, out, err)
	})
	mux.HandleFunc(prefix+"/admins", refuse(http.MethodGet))
	mux.HandleFunc(prefix+"/admins/{id}", refuse("PUT, DELETE"))
	mux.HandleFunc(prefix+"/log", refuse(http.MethodGet))
	mux.HandleFunc(prefix+"/stats", refuse(http.MethodGet))
	mux.HandleFunc("/", noSuchEndpoint)
	return mux
}

// answerWith writes what a service gave, or its error.
func answerWith(w http.ResponseWriter, log *slog.Logger, out any, err error) {
	if err != nil {
		failed(w, log, err)
		return
	}
	writeJSON(w, out)
}

// pageLimit reads the limit of a page, domain.DefaultAccountsPage without one.
func pageLimit(w http.ResponseWriter, r *http.Request, log *slog.Logger) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return domain.DefaultAccountsPage, true
	}
	limit, err := strconv.Atoi(v)
	if err != nil {
		failed(w, log, contracts.ErrInvalidLimit)
		return 0, false
	}
	return limit, true
}

// refuse answers a method an endpoint does not take.
func refuse(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allow)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use "+allow)
	}
}

func noSuchEndpoint(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
}

// Invitations returns the handler of the operator's invitations: POST
// /v1/admin/invitations makes single-use sign-up codes, GET lists the ones
// not used yet (when made, when they end, and an identifier that is not the
// code), and DELETE /v1/admin/invitations/{id} revokes one. Put it behind
// TokenAuth with the operator's token: whoever holds it decides who may sign
// up.
func Invitations(svc OperatorService, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	invitationRoutes(mux, "/v1/admin", func(*http.Request) OperatorService { return svc }, log)
	mux.HandleFunc("/", noSuchEndpoint)
	return mux
}

// invitationRoutes are the routes of the invitations under prefix, for the
// operator or for an admin.
func invitationRoutes(mux *http.ServeMux, prefix string, op func(*http.Request) OperatorService, log *slog.Logger) {
	mux.HandleFunc("POST "+prefix+"/invitations", func(w http.ResponseWriter, r *http.Request) {
		in := contracts.InvitationRequest{Count: 1}
		if r.ContentLength != 0 && !decode(w, r, &in) {
			return
		}
		out, err := op(r).CreateInvitations(r.Context(), in.Count)
		answerWith(w, log, out, err)
	})
	mux.HandleFunc("GET "+prefix+"/invitations", func(w http.ResponseWriter, r *http.Request) {
		out, err := op(r).Invitations(r.Context())
		answerWith(w, log, out, err)
	})
	mux.HandleFunc("DELETE "+prefix+"/invitations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := op(r).RevokeInvitation(r.Context(), r.PathValue("id")); err != nil {
			failed(w, log, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(prefix+"/invitations", refuse("GET, POST"))
	mux.HandleFunc(prefix+"/invitations/{id}", refuse(http.MethodDelete))
}

// Accounts returns the handler of the operator's tool. GET /v1/admin/accounts
// lists the accounts, the oldest first (limit, and after with the next of the
// page before), or with address the account of exactly that address, as a
// list of none or one; GET /v1/admin/accounts/{id} shows one. What it answers
// is read from the accounts' signed records, so it is what the enclave
// vouches for and not what the table says.
//
// It changes what an operator may change, each with a POST that takes no body
// and answers the account after it: /block and /unblock, /delete and
// /delete/cancel, and /sessions/close. There is nothing else: no route here
// changes an address or a way to sign in, and the service it is given could
// not do it (OperatorService). Put it behind TokenAuth with the operator's
// token.
func Accounts(svc OperatorService, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	accountRoutes(mux, "/v1/admin", func(*http.Request) OperatorService { return svc }, log)
	mux.HandleFunc("/", noSuchEndpoint)
	return mux
}

// accountRoutes are the routes of the operator's tool under prefix, for the
// operator or for an admin.
func accountRoutes(mux *http.ServeMux, prefix string, op func(*http.Request) OperatorService, log *slog.Logger) {
	mux.HandleFunc("GET "+prefix+"/accounts", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Has("address") {
			out, err := op(r).AccountRecordByAddress(r.Context(), q.Get("address"))
			answerWith(w, log, out, err)
			return
		}
		limit, ok := pageLimit(w, r, log)
		if !ok {
			return
		}
		out, err := op(r).AccountRecords(r.Context(), q.Get("after"), limit)
		answerWith(w, log, out, err)
	})
	mux.HandleFunc("GET "+prefix+"/accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, err := op(r).AccountRecord(r.Context(), r.PathValue("id"))
		answerWith(w, log, out, err)
	})
	for path, act := range map[string]func(OperatorService) func(context.Context, string) (*contracts.AccountRecord, error){
		"block": func(s OperatorService) func(context.Context, string) (*contracts.AccountRecord, error) {
			return s.BlockAccount
		},
		"unblock": func(s OperatorService) func(context.Context, string) (*contracts.AccountRecord, error) {
			return s.UnblockAccount
		},
		"delete": func(s OperatorService) func(context.Context, string) (*contracts.AccountRecord, error) {
			return s.DeleteAccount
		},
		"delete/cancel": func(s OperatorService) func(context.Context, string) (*contracts.AccountRecord, error) {
			return s.KeepAccount
		},
		"sessions/close": func(s OperatorService) func(context.Context, string) (*contracts.AccountRecord, error) {
			return s.CloseAccountSessions
		},
	} {
		mux.HandleFunc("POST "+prefix+"/accounts/{id}/"+path, func(w http.ResponseWriter, r *http.Request) {
			out, err := act(op(r))(r.Context(), r.PathValue("id"))
			answerWith(w, log, out, err)
		})
		mux.HandleFunc(prefix+"/accounts/{id}/"+path, refuse(http.MethodPost))
	}
	// Looking is all these two take: an account is changed by what is above,
	// and by nothing else.
	mux.HandleFunc(prefix+"/accounts", refuse(http.MethodGet))
	mux.HandleFunc(prefix+"/accounts/{id}", refuse(http.MethodGet))
}

// ProviderApps returns the handler of the applications registered at the
// providers: PUT /v1/admin/providers/{provider} gives the enclave one's
// client id and secret, DELETE takes them away, and GET /v1/admin/providers
// lists which are set, with their client ids and never a secret. Put it
// behind TokenAuth with the operator's token, on a connection the operator
// verified: the secret is sent to whoever answers.
func ProviderApps(svc ProviderAppService, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	providerAppRoutes(mux, "/v1/admin", func(*http.Request) ProviderAppService { return svc }, log)
	mux.HandleFunc("/", noSuchEndpoint)
	return mux
}

// providerAppRoutes are the routes of the providers' applications under
// prefix, for the operator or for an admin.
func providerAppRoutes(mux *http.ServeMux, prefix string, op func(*http.Request) ProviderAppService, log *slog.Logger) {
	mux.HandleFunc("GET "+prefix+"/providers", func(w http.ResponseWriter, r *http.Request) {
		out, err := op(r).ProviderApps(r.Context())
		answerWith(w, log, out, err)
	})
	mux.HandleFunc("PUT "+prefix+"/providers/{provider}", func(w http.ResponseWriter, r *http.Request) {
		var in contracts.ProviderAppSet
		if !decode(w, r, &in) {
			return
		}
		out, err := op(r).SetProviderApp(r.Context(), r.PathValue("provider"), in)
		answerWith(w, log, out, err)
	})
	mux.HandleFunc("DELETE "+prefix+"/providers/{provider}", func(w http.ResponseWriter, r *http.Request) {
		if err := op(r).RemoveProviderApp(r.Context(), r.PathValue("provider")); err != nil {
			failed(w, log, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(prefix+"/providers", refuse(http.MethodGet))
	mux.HandleFunc(prefix+"/providers/{provider}", refuse("PUT, DELETE"))
}
