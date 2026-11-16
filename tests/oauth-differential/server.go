// Package differential drives internal/oauth's functions with clients
// written by others (ADR 0030, V12): golang.org/x/oauth2 with PKCE and the
// MCP Go SDK's auth client, through a minimal authorization server made of
// those functions alone over a played store, and internal/jws against
// go-jose on the same assertions. It is its own module, so none of these
// libraries reaches the image. The same clients against the composed
// service are V60, at pull request 9.
package differential

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/jws"
	"github.com/permaily-org/permaily/internal/oauth"
)

// Server is a minimal authorization server and resource made of
// internal/oauth's functions: the person always allows, the store is maps
// in memory, and a client's document and keys are given to it (the played
// resolver), never fetched.
type Server struct {
	Config oauth.Config
	// Documents and Keys are the clients' documents, as oauth.Document read
	// them, and their key sets, by client_id.
	Documents map[string]*oauth.ClientDocument
	Keys      map[string][]jws.Key
	Now       func() time.Time

	key []byte

	mu         sync.Mutex
	codes      map[string]*code
	families   map[string]*oauth.FamilyState
	refresh    map[string]generation
	access     map[string]accessToken
	assertions map[string]*oauth.AssertionRecord
	versions   map[string]uint64
	made       int
}

type code struct {
	state  oauth.CodeState
	params oauth.AuthorizeParams
}

type generation struct {
	family string
	n      uint64
}

type accessToken struct {
	family   string
	resource string
	scopes   []string
	expires  time.Time
}

// NewServer is a server for c, with a family key of its own.
func NewServer(c oauth.Config, now func() time.Time) *Server {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return &Server{
		Config: c, Documents: map[string]*oauth.ClientDocument{}, Keys: map[string][]jws.Key{}, Now: now, key: key,
		codes: map[string]*code{}, families: map[string]*oauth.FamilyState{}, refresh: map[string]generation{},
		access: map[string]accessToken{}, assertions: map[string]*oauth.AssertionRecord{}, versions: map[string]uint64{},
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == oauth.PathServerMetadata:
		answer(w, http.StatusOK, oauth.Metadata(s.Config))
	case r.URL.Path == oauth.PathResourceMetadata+oauth.PathMCP:
		answer(w, http.StatusOK, oauth.ResourceMetadataFor(s.Config, contracts.ResourceMCP))
	case r.URL.Path == oauth.PathResourceMetadata+oauth.PathAPI:
		answer(w, http.StatusOK, oauth.ResourceMetadataFor(s.Config, contracts.ResourceAPI))
	case r.URL.Path == oauth.PathAuthorize:
		s.authorize(w, r)
	case r.URL.Path == oauth.PathToken && r.Method == http.MethodPost:
		s.token(w, r)
	case r.URL.Path == oauth.PathMCP:
		s.resource(w, r, contracts.ResourceMCP)
	case r.URL.Path == oauth.PathAPI:
		s.resource(w, r, contracts.ResourceAPI)
	default:
		http.NotFound(w, r)
	}
}

func answer(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func refuse(w http.ResponseWriter, c oauth.ErrorCode) {
	status := http.StatusBadRequest
	if c == oauth.ErrInvalidClient {
		status = http.StatusUnauthorized
	}
	answer(w, status, map[string]string{"error": string(c)})
}

func codeOf(err error) oauth.ErrorCode {
	if e, ok := err.(*oauth.Error); ok {
		return e.Code
	}
	return oauth.ErrServerError
}

// authorize is the authorization endpoint, with a person who allows.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if _, err := oauth.ClientID(q.Get("client_id"), "permaily.com"); err != nil {
		refuse(w, codeOf(err))
		return
	}
	doc := s.Documents[q.Get("client_id")]
	if doc == nil {
		refuse(w, oauth.ErrInvalidClient)
		return
	}
	p, proven, err := oauth.Authorize(q, doc, s.Config)
	switch {
	case err != nil && proven:
		http.Redirect(w, r, oauth.Redirect(p.RedirectURI, url.Values{"error": {string(codeOf(err))}, "state": {p.State}}, s.Config.Issuer), http.StatusFound)
		return
	case err != nil:
		refuse(w, codeOf(err)) // shown, never redirected
		return
	case p.RequestURI != "":
		refuse(w, oauth.ErrInvalidRequest) // no pushed requests here
		return
	}
	c, err := oauth.NewSecret(oauth.PrefixCode, func(b []byte) error { _, err := rand.Read(b); return err })
	if err != nil {
		refuse(w, oauth.ErrServerError)
		return
	}
	s.mu.Lock()
	s.made++
	family := "fam_" + strings.Repeat("A", 25) + string(rune('A'+s.made%26))
	s.codes[oauth.Hash(oauth.KindCode, c)] = &code{state: oauth.CodeState{Family: family, ExpiresAt: s.Now().Add(oauth.CodeTTL), Status: oauth.CodeIssued}, params: *p}
	s.mu.Unlock()
	params := url.Values{"code": {c}}
	if p.State != "" {
		params.Set("state", p.State)
	}
	http.Redirect(w, r, oauth.Redirect(p.RedirectURI, params, s.Config.Issuer), http.StatusFound)
}

// resourceOf is a resource's identifier as a connection names it.
func (s *Server) resourceOf(id string) contracts.Resource {
	if id == s.Config.APIResource {
		return contracts.ResourceAPI
	}
	return contracts.ResourceMCP
}

// token is the token endpoint.
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, oauth.FormMaxBytes)
	if err := r.ParseForm(); err != nil {
		refuse(w, oauth.ErrInvalidRequest)
		return
	}
	f := r.PostForm
	doc := s.Documents[f.Get("client_id")]
	if doc == nil {
		refuse(w, oauth.ErrInvalidClient)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	if doc.TokenEndpointAuthMethod == oauth.AuthPrivateKeyJWT {
		if f.Get("client_assertion_type") != oauth.AssertionType {
			refuse(w, oauth.ErrInvalidClient)
			return
		}
		jti, exp, err := oauth.VerifyAssertion(doc, s.Keys[doc.ClientID], f.Get("client_assertion"), []string{s.Config.Issuer + oauth.PathToken, s.Config.Issuer}, now)
		if err != nil {
			refuse(w, codeOf(err))
			return
		}
		name, use := oauth.AssertionRecordName(doc.ClientID, jti, exp)
		rec := s.assertions[name]
		if rec == nil {
			rec = &oauth.AssertionRecord{V: 1}
			s.assertions[name] = rec
		}
		switch rec.Accept(use, exp, now, s.versions[name]) {
		case oauth.AssertionAccepted:
			s.versions[name]++
		case oauth.AssertionFull:
			refuse(w, oauth.ErrTemporarily)
			return
		default:
			refuse(w, oauth.ErrInvalidClient)
			return
		}
	}
	switch f.Get("grant_type") {
	case "authorization_code":
		s.exchange(w, f, doc, now)
	case "refresh_token":
		s.rotate(w, f, doc, now)
	default:
		refuse(w, oauth.ErrUnsupportedGrantType)
	}
}

func (s *Server) exchange(w http.ResponseWriter, f url.Values, doc *oauth.ClientDocument, now time.Time) {
	c := s.codes[oauth.Hash(oauth.KindCode, f.Get("code"))]
	if c == nil {
		refuse(w, oauth.ErrInvalidGrant)
		return
	}
	p := c.params
	matches := f.Get("client_id") == p.ClientID && f.Get("redirect_uri") == p.RedirectURI && oauth.VerifyPKCE(p.CodeChallenge, f.Get("code_verifier")) &&
		(f.Get("resource") == "" || f.Get("resource") == p.Resource)
	switch result, _ := c.state.Exchange(matches, now); result {
	case oauth.CodeOK:
		fam := &oauth.FamilyState{V: 1, Family: c.state.Family, ClientID: doc.ClientID, Resource: s.resourceOf(p.Resource), Scopes: p.Scope, Status: oauth.FamilyActive, RotatedAt: now}
		s.families[fam.Family] = fam
		s.issue(w, fam, oauth.Issued{Generation: 0, Scope: fam.Scopes}, p.Resource, now)
	case oauth.CodeReused:
		s.families[c.state.Family].End(contracts.EndedByReuse, now)
		refuse(w, oauth.ErrInvalidGrant)
	default:
		refuse(w, oauth.ErrInvalidGrant)
	}
}

func (s *Server) rotate(w http.ResponseWriter, f url.Values, doc *oauth.ClientDocument, now time.Time) {
	g, ok := s.refresh[oauth.Hash(oauth.KindRefresh, f.Get("refresh_token"))]
	if !ok {
		refuse(w, oauth.ErrInvalidGrant)
		return
	}
	fam := s.families[g.family]
	resource := s.Config.MCPResource
	if fam.Resource == contracts.ResourceAPI {
		resource = s.Config.APIResource
	}
	if sent := f.Get("resource"); sent != "" && sent != resource {
		refuse(w, oauth.ErrInvalidTarget)
		return
	}
	var scope []string
	if _, sent := f["scope"]; sent {
		var err error
		if scope, err = oauth.Scopes(f.Get("scope"), true); err != nil {
			refuse(w, oauth.ErrInvalidScope)
			return
		}
	}
	result, issued := fam.Refresh(oauth.RefreshAsk{ClientID: doc.ClientID, Resource: fam.Resource, Generation: g.n, Scope: scope, Now: now})
	switch result {
	case oauth.RefreshRotated, oauth.RefreshRetry:
		s.issue(w, fam, issued, resource, now)
	case oauth.RefreshWider:
		refuse(w, oauth.ErrInvalidScope)
	default:
		refuse(w, oauth.ErrInvalidGrant)
	}
}

// issue answers the pair of a family's generation, made again the same way
// for a retry.
func (s *Server) issue(w http.ResponseWriter, fam *oauth.FamilyState, i oauth.Issued, resource string, now time.Time) {
	a := oauth.FamilySecret(s.key, oauth.KindAccess, fam.Family, i.Generation)
	r := oauth.FamilySecret(s.key, oauth.KindRefresh, fam.Family, i.Generation)
	s.access[oauth.Hash(oauth.KindAccess, a)] = accessToken{family: fam.Family, resource: resource, scopes: i.Scope, expires: now.Add(oauth.AccessTokenTTL)}
	s.refresh[oauth.Hash(oauth.KindRefresh, r)] = generation{fam.Family, i.Generation}
	answer(w, http.StatusOK, map[string]any{
		"access_token": a, "token_type": "Bearer", "expires_in": int(oauth.AccessTokenTTL / time.Second),
		"refresh_token": r, "scope": strings.Join(i.Scope, " "),
	})
}

// resource answers a request to a resource: 401 with the challenge, or the
// scopes the token gives.
func (s *Server) resource(w http.ResponseWriter, r *http.Request, res contracts.Resource) {
	id := s.Config.MCPResource
	if res == contracts.ResourceAPI {
		id = s.Config.APIResource
	}
	bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		w.Header().Set("WWW-Authenticate", oauth.Challenge(s.Config, res, "", nil))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	t, found := s.access[oauth.Hash(oauth.KindAccess, bearer)]
	fam := s.families[t.family]
	s.mu.Unlock()
	if !found || t.resource != id || !s.Now().Before(t.expires) || fam.Status == oauth.FamilyEnded {
		w.Header().Set("WWW-Authenticate", oauth.Challenge(s.Config, res, oauth.ErrInvalidToken, nil))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	answer(w, http.StatusOK, map[string]any{"scopes": t.scopes, "parts": oauth.Parts(t.scopes)})
}
