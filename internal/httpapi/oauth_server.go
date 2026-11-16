package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
)

// metadataCache is how long a client may keep the server's and the
// resources' metadata (ADR 0030, "Hosts, issuer and endpoints").
const metadataCache = "public, max-age=3600"

// ConnectionsMounts are where MountConnections puts the routes: the app's
// host (inside AppHost), the api host and the mcp host, and the web app's
// handler (webui.Handler()), which the authorization page serves.
type ConnectionsMounts struct {
	App *AppMux
	API *http.ServeMux
	MCP *http.ServeMux
	SPA http.Handler
}

// MountConnections puts every route of connections on its host (ADR 0030,
// "Hosts, issuer and endpoints"):
//
//   - the app's: the authorization page (AuthorizePage) and the person's
//     routes (ConnectionsAPI);
//   - the api host, the issuer, which has no cookie: the server's metadata,
//     the API's resource metadata, the token, revocation and pushed request
//     endpoints (AuthorizationServer) and the API for apps (VendorAPI);
//   - the mcp host: its resource metadata at both of RFC 9728's places
//     (ResourceMetadataMCP). The MCP endpoint itself is internal/mcpapi's.
//
// Nothing of it is under /v1/admin/, and /.well-known/openid-configuration
// is not served: this is not an OpenID provider.
func MountConnections(m ConnectionsMounts, cfg ConnectionsConfig) {
	cfg = cfg.withDefaults()
	person := ConnectionsAPI(cfg)
	m.App.Handle("/api/v1/oauth/", person)
	m.App.Handle("/api/v1/connections", person)
	m.App.Handle("/api/v1/connections/", person)
	m.App.Handle(oauth.PathAuthorize, AuthorizePage(m.SPA))
	server := AuthorizationServer(cfg)
	m.API.Handle(oauth.PathServerMetadata, server)
	m.API.Handle(oauth.PathResourceMetadata+oauth.PathAPI, server)
	m.API.Handle("/oauth/", server)
	vendor := VendorAPI(cfg)
	m.API.Handle(oauth.PathAPI+"/connection", vendor)
	m.API.Handle(oauth.PathAPI+"/messages/", vendor)
	mcp := ResourceMetadataMCP(cfg)
	m.MCP.Handle(oauth.PathResourceMetadata, mcp)
	m.MCP.Handle(oauth.PathResourceMetadata+oauth.PathMCP, mcp)
}

// AuthorizePage serves the web app at the authorization endpoint (GET
// https://app.<domain>/oauth/authorize), outside /api/: a vendor sends the
// browser there by a navigation from another site, which carries no session
// cookie (SameSite=Strict) and which the cross-site rule of /api/ would
// refuse (the spike's second finding). The server reads nothing of the
// request: the app is given it without its query string and without its
// cookies, and takes the query string out of the address bar itself before
// it posts it, same origin, to POST /api/v1/oauth/requests (ADR 0030, "The
// hand-off"). The app's CSP (frame-ancestors 'none') is the router's, as on
// every page of the app.
func AuthorizePage(spa http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		bare := r.Clone(r.Context())
		u := *r.URL
		u.RawQuery, u.ForceQuery = "", false
		bare.URL, bare.RequestURI = &u, u.RequestURI()
		bare.Header.Del("Cookie")
		spa.ServeHTTP(w, bare)
	})
}

// AuthorizationServer returns the issuer's routes on the api host (ADR 0030,
// "Hosts, issuer and endpoints", "Codes and tokens"): the server's metadata
// (RFC 8414) and the API's resource metadata (RFC 9728), each with
// Cache-Control public for an hour and CORS for any origin; POST
// /oauth/token, /oauth/revoke and /oauth/par, each taking a form body of at
// most oauth.FormMaxBytes and nothing in its address, answering no-store,
// RFC 6749's error JSON with the code alone, and never a redirect. The token
// and revocation endpoints answer any origin (CORS without credentials, for
// apps in a browser); the pushed request endpoint is for apps' servers. A
// client authenticates only as its document says, in the form
// (private_key_jwt or none): an Authorization header (client_secret_basic)
// is invalid_client for everyone.
func AuthorizationServer(cfg ConnectionsConfig) http.Handler {
	cfg = cfg.withDefaults()
	s := &oauthServer{cfg: cfg}
	mux := http.NewServeMux()
	mux.Handle(oauth.PathServerMetadata, metadata(func() any { return cfg.Metadata(cfg.OAuth) }))
	mux.Handle(oauth.PathResourceMetadata+oauth.PathAPI, metadata(func() any { return cfg.ResourceMetadata(cfg.OAuth, contracts.ResourceAPI) }))
	mux.Handle(oauth.PathToken, s.endpoint("token", true, s.token))
	mux.Handle(oauth.PathRevoke, s.endpoint("revoke", true, s.revoke))
	mux.Handle(oauth.PathPushed, s.endpoint("par", false, s.push))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "not found", http.StatusNotFound)
	})
	return logRequests(cfg.Log, "oauth: request", ResourceHost(mux))
}

// ResourceHost is the outermost handler of the api and the mcp hosts, as
// AppHost is the app's: a request whose path is not in canonical form
// (canonicalPath: no "//", "/./", "/../" or escape) is answered 404 here,
// fixed and no-store, before any ServeMux, which would answer it a redirect
// to the path cleaned with the query string it carried (a code, a token, a
// client's address). The composition mounts each of those hosts' ServeMux
// behind it; AuthorizationServer and VendorAPI refuse such a path on their
// own too.
func ResourceHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requestCanonical(r) {
			noSuchEndpoint(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ResourceMetadataMCP returns the mcp host's resource metadata, at
// /.well-known/oauth-protected-resource/mcp, which Claude Code reads before
// it asks anything, and at /.well-known/oauth-protected-resource, which the
// specification says to try too. It is mounted at those two paths alone.
func ResourceMetadataMCP(cfg ConnectionsConfig) http.Handler {
	return metadata(func() any { return cfg.ResourceMetadata(cfg.OAuth, contracts.ResourceMCP) })
}

// metadata serves a published document: GET or HEAD, any origin, an hour.
func metadata(doc func() any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setRoute(r, "metadata")
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		switch r.Method {
		case http.MethodGet, http.MethodHead:
		case http.MethodOptions:
			h.Set("Access-Control-Allow-Methods", "GET, HEAD")
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		default:
			h.Set("Allow", "GET, HEAD, OPTIONS")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		b, _ := json.Marshal(doc()) // a struct of strings, lists and booleans
		h.Set("Content-Type", "application/json")
		h.Set("Cache-Control", metadataCache)
		h.Set("Content-Length", strconv.Itoa(len(b)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(b)
		}
	})
}

type oauthServer struct{ cfg ConnectionsConfig }

// endpoint reads a form request of an endpoint of the issuer and hands its
// values to serve: POST only (and OPTIONS for a browser, with cors), the
// body a form of at most oauth.FormMaxBytes and nothing in the address,
// no Authorization header. Every refusal is RFC 6749's JSON. name is the
// endpoint in the request's line.
func (s *oauthServer) endpoint(name string, cors bool, serve func(http.ResponseWriter, *http.Request, url.Values)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setRoute(r, name)
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("Pragma", "no-cache")
		if cors {
			h.Set("Access-Control-Allow-Origin", "*")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "POST")
				h.Set("Access-Control-Allow-Headers", "Content-Type")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		if r.Method != http.MethodPost {
			h.Set("Allow", http.MethodPost)
			writeOAuthError(w, http.StatusMethodNotAllowed, oauth.ErrInvalidRequest)
			return
		}
		if r.Header.Get("Authorization") != "" {
			writeOAuthError(w, http.StatusUnauthorized, oauth.ErrInvalidClient)
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			writeOAuthError(w, http.StatusBadRequest, oauth.ErrInvalidRequest)
			return
		}
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/x-www-form-urlencoded" {
			writeOAuthError(w, http.StatusBadRequest, oauth.ErrInvalidRequest)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, oauth.FormMaxBytes))
		if err != nil {
			status := http.StatusBadRequest
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				status = http.StatusRequestEntityTooLarge
			}
			writeOAuthError(w, status, oauth.ErrInvalidRequest)
			return
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			writeOAuthError(w, http.StatusBadRequest, oauth.ErrInvalidRequest)
			return
		}
		serve(w, r, form)
	})
}

// Grant types the token endpoint takes; any other, jwt-bearer included, is
// unsupported_grant_type (ADR 0030, choice 7).
const (
	grantCode    = "authorization_code"
	grantRefresh = "refresh_token"
)

func (s *oauthServer) token(w http.ResponseWriter, r *http.Request, form url.Values) {
	switch gt := form["grant_type"]; {
	case len(gt) != 1 || gt[0] == "":
		writeOAuthError(w, http.StatusBadRequest, oauth.ErrInvalidRequest)
		return
	case gt[0] != grantCode && gt[0] != grantRefresh:
		writeOAuthError(w, http.StatusBadRequest, oauth.ErrUnsupportedGrantType)
		return
	}
	out, err := s.cfg.Service.Token(r.Context(), form)
	if err != nil {
		s.failed(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, out)
}

func (s *oauthServer) revoke(w http.ResponseWriter, r *http.Request, form url.Values) {
	if err := s.cfg.Service.RevokeToken(r.Context(), form); err != nil {
		s.failed(w, r, err)
		return
	}
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

// pushed is the answer of POST /oauth/par (RFC 9126).
type pushed struct {
	RequestURI string `json:"request_uri"`
	ExpiresIn  int    `json:"expires_in"`
}

func (s *oauthServer) push(w http.ResponseWriter, r *http.Request, form url.Values) {
	uri, err := s.cfg.Service.Push(r.Context(), form)
	if err != nil {
		s.failed(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, pushed{RequestURI: uri, ExpiresIn: int(oauth.PushedRequestTTL.Seconds())})
}

// failed answers a refusal of an endpoint of the issuer with its code alone:
// invalid_client 401 (an OAuthError's, or contracts.ErrInvalidClient's with
// its reason never said), temporarily_unavailable 503 (and a storage that could
// not be read), too many requests 429, server_error 500 and any failure
// that is not a refusal (logged without what it was about), every other
// refusal 400.
func (s *oauthServer) failed(w http.ResponseWriter, r *http.Request, err error) {
	if e, ok := errors.AsType[*connections.OAuthError](err); ok {
		switch e.Code {
		case oauth.ErrInvalidClient:
			writeOAuthError(w, http.StatusUnauthorized, e.Code)
		case oauth.ErrTemporarily:
			w.Header().Set("Retry-After", retryAfterStorage)
			writeOAuthError(w, http.StatusServiceUnavailable, e.Code)
		case oauth.ErrServerError:
			writeOAuthError(w, http.StatusInternalServerError, e.Code)
		default:
			writeOAuthError(w, http.StatusBadRequest, e.Code)
		}
		return
	}
	switch {
	case errors.Is(err, contracts.ErrInvalidClient):
		// A pushed request's client refused for its reason
		// (contracts.ClientError): the issuer says the code alone.
		writeOAuthError(w, http.StatusUnauthorized, oauth.ErrInvalidClient)
	case errors.Is(err, connections.ErrUnavailable):
		w.Header().Set("Retry-After", retryAfterStorage)
		writeOAuthError(w, http.StatusServiceUnavailable, oauth.ErrTemporarily)
	case errors.Is(err, contracts.ErrTooManyRequests):
		w.Header().Set("Retry-After", retryAfterStorage)
		writeOAuthError(w, http.StatusTooManyRequests, oauth.ErrTemporarily)
	default:
		if clientLeft(r, err) {
			setOutcome(r, outcomeClientLeft)
		} else {
			// Its kind alone, as internalFailure logs (the form held a
			// code, a token or an assertion).
			s.cfg.Log.Error("oauth: internal error", "endpoint", strings.TrimPrefix(r.URL.Path, "/oauth/"))
		}
		writeOAuthError(w, http.StatusInternalServerError, oauth.ErrServerError)
	}
}

// oauthErrorBody is RFC 6749's error answer, with the code alone: no
// error_description, which could echo what was sent.
type oauthErrorBody struct {
	Error oauth.ErrorCode `json:"error"`
}

func writeOAuthError(w http.ResponseWriter, status int, code oauth.ErrorCode) {
	writeJSONStatus(w, status, oauthErrorBody{code})
}
