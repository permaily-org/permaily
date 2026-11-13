package httpapi

import (
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
)

// The app's host makes one decision about a request's path (ADR 0026, "The
// app's host: one decision about a path", the second code review of pull
// request 5, N5 and N7), once, at its outermost handler: before the
// cross-site rule, before any session is looked up and before any routing.
// A path that is not in canonical form is refused there with a fixed answer;
// everything after classifies the canonical path with plain prefix and
// suffix tests, and no handler ever sees a path that reads differently
// escaped and decoded.

// canonicalPath says whether p, a request's path as it was sent (escaped),
// is in canonical form: it starts with "/", every byte is a letter, a digit
// or one of "-._~/", and no segment but the last is empty and none is "."
// or "..". So it holds no percent-encoding at all, no backslash, no control
// or non-ASCII byte, no "//", "/./" or "/../", and reads the same escaped and
// decoded. A trailing slash is kept: ServeMux answers a path that ends in
// one as it is, with no redirect.
//
// Every route of the app's host is made of such characters
// (TestEveryRouteOfTheAppHostIsCanonical walks them): identifiers are
// letters, digits, "_" and "-", a link's token adds ".", the web app's
// assets are named by Vite with letters, digits, "_", "-" and ".".
func canonicalPath(p string) bool {
	if p == "" || p[0] != '/' {
		return false
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' {
			continue
		}
		switch c {
		case '-', '.', '_', '~', '/':
			continue
		}
		return false
	}
	segments := strings.Split(p[1:], "/")
	for i, s := range segments {
		if s == "." || s == ".." || s == "" && i != len(segments)-1 {
			return false
		}
	}
	return true
}

// CanonicalPath says whether p, a request's path as sent, is in the
// canonical form the app's host requires (canonicalPath).
func CanonicalPath(p string) bool { return canonicalPath(p) }

// requestCanonical says whether r's path is canonical, as sent and as
// decoded: Go keeps the spelling of a path whose escaping is not its own
// in RawPath, so EscapedPath is what came, and Path is what it decodes to.
func requestCanonical(r *http.Request) bool {
	escaped := r.URL.EscapedPath()
	return r.URL.Opaque == "" && canonicalPath(escaped) && r.URL.Path == escaped
}

// RequireCanonicalPath is the outermost handler of the app's host: a
// request whose path is not canonical is answered here, 404, and goes no
// further. The answer is fixed and depends on nothing the request carries
// but its path: the frame's headers and fixed body when the path would
// have been one of the frame's route however it is decoded
// (wouldBeFramePath), the API's fixed JSON otherwise. No cookie is read or
// set, no session looked up, nothing counted.
func RequireCanonicalPath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestCanonical(r) {
			next.ServeHTTP(w, r)
			return
		}
		if wouldBeFramePath(r) {
			SetFrameHeaders(w.Header(), "https://"+r.Host)
			frameFail(w, http.StatusNotFound)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		noSuchEndpoint(w, r)
	})
}

// wouldBeFramePath says whether a path that is not canonical could be read,
// by a server or a browser, as one of the frame's route: decoded up to three
// times, a backslash read as a slash, cleaned, or with each segment decoded
// on its own (ServeMux's escaped routing), and in any case.
func wouldBeFramePath(r *http.Request) bool {
	candidates := []string{r.URL.Path, r.URL.EscapedPath()}
	p := r.URL.EscapedPath()
	for range 3 {
		d, err := url.PathUnescape(p)
		if err != nil || d == p {
			break
		}
		candidates = append(candidates, d)
		p = d
	}
	var bySegment []string
	for _, s := range strings.Split(r.URL.EscapedPath(), "/") {
		if d, err := url.PathUnescape(s); err == nil {
			s = d
		}
		bySegment = append(bySegment, strings.ReplaceAll(s, "/", "_"))
	}
	candidates = append(candidates, strings.Join(bySegment, "/"))
	for _, c := range candidates {
		c = strings.ToLower(strings.ReplaceAll(c, `\`, "/"))
		if isFramePath(c) || isFramePath(cleanPath(c)) || isFramePath(strings.TrimSuffix(cleanPath(c), "/")) {
			return true
		}
	}
	return false
}

// isFramePath says whether p, a canonical path, is one of the frame's route,
// /api/v1/mailboxes/{id}/messages/{message}/html, with or without a trailing
// slash.
func isFramePath(p string) bool {
	s := strings.Split(strings.TrimSuffix(p, "/"), "/")
	return len(s) == 8 && s[0] == "" && s[1] == "api" && s[2] == "v1" && s[3] == "mailboxes" && s[4] != "" &&
		s[5] == "messages" && s[6] != "" && s[7] == "html"
}

// cleanPath is p as ServeMux would clean it, its trailing slash kept: used
// only to recognise, among paths already refused, one that would have been
// the frame's.
func cleanPath(p string) string {
	c := path.Clean("/" + p)
	if strings.HasSuffix(p, "/") && c != "/" {
		c += "/"
	}
	return c
}

// AppMux is a ServeMux of the app's host that records every pattern
// registered on it, so a test can walk every route of the host and check
// that its own path passes the gate.
type AppMux struct {
	*http.ServeMux
}

// NewAppMux returns an empty AppMux.
func NewAppMux() *AppMux { return &AppMux{http.NewServeMux()} }

// Handle registers pattern, and records it.
func (m *AppMux) Handle(pattern string, h http.Handler) {
	record(pattern)
	m.ServeMux.Handle(pattern, h)
}

// HandleFunc registers pattern, and records it.
func (m *AppMux) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	record(pattern)
	m.ServeMux.HandleFunc(pattern, h)
}

var registered struct {
	sync.Mutex
	patterns map[string]bool
}

func record(pattern string) {
	registered.Lock()
	defer registered.Unlock()
	if registered.patterns == nil {
		registered.patterns = map[string]bool{}
	}
	registered.patterns[pattern] = true
}

// RegisteredPatterns are the patterns registered on any AppMux of this
// process, in no order.
func RegisteredPatterns() []string {
	registered.Lock()
	defer registered.Unlock()
	out := make([]string, 0, len(registered.patterns))
	for p := range registered.patterns {
		out = append(out, p)
	}
	return out
}
