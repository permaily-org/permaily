package httpapi

import (
	"net/http"
	"strings"
)

// The web app's API answers the web app, and nothing that says it was made
// by another site (ADR 0026, "Corrected while building pull request 5").
// RefuseCrossSite wraps the whole of the app's host, so every route under
// /api/ passes through it before anything else, whichever handler is mounted
// for it: before a session is looked up and before a handler runs. A
// sandboxed frame's document has an opaque origin, so whatever it asks the
// API for says Sec-Fetch-Site: cross-site; that is refused whether or not the
// browser attached the session cookie (Playwright's WebKit does).

// ProviderCallback is where a provider sends the browser back, on the app's
// host. It is registered with each provider, to the letter.
const ProviderCallback = "/api/v1/auth/provider/callback"

// CrossSiteException is a route under /api/ that a browser reaches by a
// navigation from another site, by design, and why that is safe.
type CrossSiteException struct {
	Method, Path, Why string
}

// crossSiteExceptions are the only routes under /api/ that answer a request
// that says it is cross-site. A route designed to be reached from another
// site is added here, with its reason, or it is refused.
var crossSiteExceptions = []CrossSiteException{
	{http.MethodGet, ProviderCallback, "the browser comes back from Google, GitHub, Meta or Microsoft (ADR 0017); " +
		"the trip's state and the provider cookie bind the answer to the browser that started it, and it answers only with where to go in the app"},
}

// CrossSiteExceptions returns the routes under /api/ that a cross-site
// request still reaches, each with its reason.
func CrossSiteExceptions() []CrossSiteException {
	return append([]CrossSiteException(nil), crossSiteExceptions...)
}

// RefuseCrossSite wraps the app's host: a request for a path under /api/
// whose Sec-Fetch-Site is present and is neither same-origin nor none is
// answered 403 forbidden, the same answer fromTheApp gives, unless it is one
// of the exceptions. same-site is refused too: the other hosts of the domain
// (the public site among them) are not the app. A request without the header
// (a browser that sends no Fetch Metadata, a client that is not a browser)
// goes on, to the checks it met before.
func RefuseCrossSite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if underAPI(r.URL.Path) && saysCrossSite(r) && !excepted(r) {
			notFromTheApp(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// underAPI is whether p, as ServeMux cleans it, is the API's: a path that is
// not clean is checked as the one it would be sent on to.
func underAPI(p string) bool {
	c := cleanPath(p)
	return c == "/api" || strings.HasPrefix(c, "/api/")
}

// saysCrossSite is whether the request carries Sec-Fetch-Site with anything
// other than one same-origin or none.
func saysCrossSite(r *http.Request) bool {
	v := r.Header.Values("Sec-Fetch-Site")
	if len(v) == 0 {
		return false
	}
	return len(v) != 1 || v[0] != "same-origin" && v[0] != "none"
}

func excepted(r *http.Request) bool {
	for _, e := range crossSiteExceptions {
		if r.Method == e.Method && r.URL.Path == e.Path {
			return true
		}
	}
	return false
}

// notFromTheApp is the one answer to a request that did not come from the
// app: fixed JSON, never different for who asked.
func notFromTheApp(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeError(w, http.StatusForbidden, "forbidden", "this request did not come from the app")
}
