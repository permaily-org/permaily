// Package idptest plays Google, GitHub, Meta and Microsoft: one local server
// with the authorization, token, keys and user endpoints of each, as their
// documentation describes them, and a signing key of its own. It exists for
// tests and for the enclave's dev mode; it signs anybody in as whoever it is
// told, and asks nobody for a password.
//
// It is as strict as the real ones where a client could go wrong: a token
// request without the secret, the verifier or the redirect address is
// refused, a code works once, Meta wants its proof and GitHub its scope.
package idptest

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/idp"
)

// Person is who a provider says signed in.
//
// Subject is what the provider calls the account: Google's and Microsoft's
// "sub", GitHub's and Meta's numeric "id" (so digits only, for those two).
// With the zero values of Tenant and Object, idp.Identity.Subject is Subject
// for every provider.
type Person struct {
	Subject  string
	Address  string // empty for an account that shows none
	Verified bool   // the provider vouches for Address, each in its own way
	Name     string

	// Microsoft only. Tenant is the "tid" claim; when empty it is the tenant
	// of personal accounts for a verified person and WorkTenant otherwise.
	// A verified person in a tenant of their own gets "xms_edov": true.
	// Object is the "oid" claim; when set, the identity's subject is
	// Tenant + "." + Object.
	Tenant string
	Object string
}

const (
	// PersonalTenant is the tenant of every personal Microsoft account.
	PersonalTenant = "9188040d-6c67-4c5b-b112-36a304b66dad"
	// WorkTenant is the made-up organisation of a person who is not verified.
	WorkTenant = "11111111-2222-4333-8444-555555555555"

	// The issuers are the real ones: an issuer is a name, not an address.
	GoogleIssuer    = "https://accounts.google.com"
	MicrosoftIssuer = "https://login.microsoftonline.com/{tenantid}/v2.0"
)

// The endpoints of a provider, as Seen and Count name them.
const (
	Authorize = "authorize"
	Token     = "token"
	Keys      = "keys"
	User      = "user"
	Emails    = "emails"
)

// Answer replaces what an endpoint answers.
type Answer struct {
	Status   int    // 200 when zero
	Body     []byte // written as it is
	Redirect string // when set, the answer sends the client to this address (302)
	Hang     bool   // no answer at all until the client gives up
}

// Faults make a provider answer wrongly. The zero value answers rightly.
type Faults struct {
	// Each replaces a whole answer.
	Token, Keys, User, Emails *Answer

	// EditTokenAnswer changes the members of the token endpoint's answer.
	EditTokenAnswer func(members map[string]any)

	// The ID token (Google, Microsoft). EditHeader and EditClaims change what
	// is signed; RawHeader and RawClaims replace it with bytes no map makes (a
	// member twice); Sign replaces the signature over the two encoded parts;
	// EditIDToken changes the finished token.
	EditHeader  func(header map[string]any)
	EditClaims  func(claims map[string]any)
	RawHeader   []byte
	RawClaims   []byte
	Sign        func(signed string) []byte
	EditIDToken func(token string) string

	// EditKeys changes the keys that are published.
	EditKeys func(keys []map[string]any) []map[string]any

	// EditUser changes GitHub's /user or Meta's /me, EditEmails GitHub's /user/emails.
	EditUser   func(user map[string]any)
	EditEmails func(emails []map[string]any) []map[string]any
}

// Request is what an endpoint was asked.
type Request struct {
	Method string
	Header http.Header
	Query  url.Values
	Form   url.Values // the body of a POST with a form
}

// Server is the four providers. Make one with New and Close it.
type Server struct {
	// URL is where it listens, e.g. http://127.0.0.1:41234.
	URL string
	// Now is the clock the tokens are dated with; time.Now when nil. Set it
	// before the first request.
	Now func() time.Time

	server *httptest.Server
	mu     sync.Mutex
	people map[idp.Provider]Person
	faults map[idp.Provider]Faults
	seen   map[string][]Request
	codes  map[string]*grant // by code
	tokens map[string]*grant // by access token
	key    *rsa.PrivateKey
	kid    string
	turns  int
}

// grant is one sign-in that the person approved.
type grant struct {
	provider    idp.Provider
	who         Person
	redirect    string
	challenge   string
	nonce       string
	scope       []string
	accessToken string
}

// keys are made once for every Server of a test binary: making an RSA key
// takes longer than everything else here.
var keys = sync.OnceValue(func() [2]*rsa.PrivateKey {
	var pair [2]*rsa.PrivateKey
	for i := range pair {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic("idptest: " + err.Error())
		}
		pair[i] = key
	}
	return pair
})

// New starts the four providers on a local port.
func New() *Server {
	s := &Server{
		people: map[idp.Provider]Person{},
		faults: map[idp.Provider]Faults{},
		seen:   map[string][]Request{},
		codes:  map[string]*grant{},
		tokens: map[string]*grant{},
		key:    keys()[0],
		kid:    "idptest-key-0",
	}
	mux := http.NewServeMux()
	for _, p := range idp.Providers {
		mux.HandleFunc("GET /"+string(p)+"/authorize", s.authorize(p))
		mux.HandleFunc("/"+string(p)+"/token", s.token(p))
	}
	for _, p := range []idp.Provider{idp.Google, idp.Microsoft} {
		mux.HandleFunc("GET /"+string(p)+"/keys", s.keys(p))
	}
	mux.HandleFunc("GET /github/user", s.githubUser)
	mux.HandleFunc("GET /github/user/emails", s.githubEmails)
	mux.HandleFunc("GET /meta/me", s.metaUser)
	s.server = httptest.NewServer(mux)
	s.URL = s.server.URL
	return s
}

// Close stops it.
func (s *Server) Close() { s.server.Close() }

// Endpoints is where the providers are, to give idp.Client.
func (s *Server) Endpoints() map[idp.Provider]idp.Endpoints {
	at := func(p idp.Provider, path string) string { return s.URL + "/" + string(p) + "/" + path }
	return map[idp.Provider]idp.Endpoints{
		idp.Google: {
			Authorize: at(idp.Google, "authorize"), Token: at(idp.Google, "token"),
			Keys: at(idp.Google, "keys"), Issuer: GoogleIssuer,
		},
		idp.GitHub: {
			Authorize: at(idp.GitHub, "authorize"), Token: at(idp.GitHub, "token"),
			User: at(idp.GitHub, "user"), Emails: at(idp.GitHub, "user/emails"),
		},
		idp.Meta: {
			Authorize: at(idp.Meta, "authorize"), Token: at(idp.Meta, "token"),
			User: at(idp.Meta, "me"),
		},
		idp.Microsoft: {
			Authorize: at(idp.Microsoft, "authorize"), Token: at(idp.Microsoft, "token"),
			Keys: at(idp.Microsoft, "keys"), Issuer: MicrosoftIssuer,
		},
	}
}

// App is the application registered with a provider: the only client
// identifier and secret it accepts.
func (s *Server) App(p idp.Provider) idp.App {
	return idp.App{ClientID: "client-of-" + string(p), ClientSecret: "secret-of-" + string(p)}
}

// Default is who signs in at a provider when SignInAs was not called.
func Default(p idp.Provider) Person {
	return Person{Subject: "1001", Address: "person@" + string(p) + ".example", Verified: true, Name: "Test Person"}
}

// SignInAs sets who approves the sign-ins that begin at a provider from now
// on, until it is called again. A sign-in is whoever was set when the browser
// went to the provider (Approve), not when the code is exchanged.
func (s *Server) SignInAs(p idp.Provider, who Person) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.people[p] = who
}

// Misbehave makes a provider answer wrongly from now on; Faults{} makes it
// answer rightly again.
func (s *Server) Misbehave(p idp.Provider, f Faults) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults[p] = f
}

// Approve does what the person's browser does with the address idp.Begin
// gave: it goes there, the person agrees, and the provider sends the browser
// back with a code and the state. It returns both, for the callback.
func (s *Server) Approve(address string) (code, state string, err error) {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(address)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		return "", "", fmt.Errorf("idptest: the provider refused to begin (HTTP %d)", resp.StatusCode)
	}
	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		return "", "", err
	}
	return back.Query().Get("code"), back.Query().Get("state"), nil
}

// Seen is every request an endpoint of a provider got, oldest first.
func (s *Server) Seen(p idp.Provider, endpoint string) []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen[string(p)+"/"+endpoint])
}

// Count is how many requests an endpoint of a provider got.
func (s *Server) Count(p idp.Provider, endpoint string) int { return len(s.Seen(p, endpoint)) }

// SigningKey is the key ID tokens are signed with now, and its identifier.
func (s *Server) SigningKey() (kid string, key *rsa.PrivateKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kid, s.key
}

// Rotate changes the signing key, as a provider does every few weeks: tokens
// are signed with a new key under a new identifier, and the old one is no
// longer published.
func (s *Server) Rotate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turns++
	s.key = keys()[s.turns%2]
	s.kid = "idptest-key-" + strconv.Itoa(s.turns)
}

// IDToken is the ID token a provider (Google or Microsoft) gives its
// application for the person set with SignInAs and a nonce, with the faults
// set with Misbehave. It asks nothing of the server.
func (s *Server) IDToken(p idp.Provider, nonce string) string {
	s.mu.Lock()
	who, f := s.person(p), s.faults[p]
	s.mu.Unlock()
	return s.idToken(p, f, &grant{provider: p, who: who, nonce: nonce, scope: []string{"openid", "email", "profile"}})
}

// SignToken makes a token of a header and claims as they are given, signed
// with the signing key.
func (s *Server) SignToken(header, claims []byte) string {
	_, key := s.SigningKey()
	signed := b64(header) + "." + b64(claims)
	return signed + "." + b64(sign(key, signed))
}

// KeySet is what a provider's keys endpoint answers.
func (s *Server) KeySet(p idp.Provider) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keySet(p, s.faults[p])
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func random() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic("idptest: " + err.Error())
	}
	return b64(b)
}

func sign(key *rsa.PrivateKey, signed string) []byte {
	hash := sha256.Sum256([]byte(signed))
	signature, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, hash[:])
	if err != nil {
		panic("idptest: " + err.Error())
	}
	return signature
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// person is who signs in at a provider. The caller holds the lock.
func (s *Server) person(p idp.Provider) Person {
	if who, ok := s.people[p]; ok {
		return who
	}
	return Default(p)
}

// record keeps a request and returns the faults of its provider.
func (s *Server) record(p idp.Provider, endpoint string, r *http.Request) Faults {
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	name := string(p) + "/" + endpoint
	s.seen[name] = append(s.seen[name], Request{
		Method: r.Method, Header: r.Header.Clone(), Query: r.URL.Query(), Form: maps.Clone(r.PostForm),
	})
	return s.faults[p]
}

// replaced writes the answer that replaces an endpoint's own, if there is one.
func replaced(w http.ResponseWriter, r *http.Request, a *Answer) bool {
	switch {
	case a == nil:
		return false
	case a.Hang:
		<-r.Context().Done()
	case a.Redirect != "":
		http.Redirect(w, r, a.Redirect, http.StatusFound)
	default:
		w.Header().Set("Content-Type", "application/json")
		if a.Status != 0 {
			w.WriteHeader(a.Status)
		}
		_, _ = w.Write(a.Body)
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		panic("idptest: " + err.Error())
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// needs are the scopes without which a provider gives no identity at all.
var needs = map[idp.Provider]string{idp.Google: "openid", idp.Microsoft: "openid"}

// authorize is where the browser is sent. The person agrees at once.
func (s *Server) authorize(p idp.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.record(p, Authorize, r)
		q := r.URL.Query()
		scope := strings.Fields(q.Get("scope"))
		back, err := url.Parse(q.Get("redirect_uri"))
		switch {
		case q.Get("client_id") != s.App(p).ClientID,
			q.Get("response_type") != "code",
			err != nil || !back.IsAbs(),
			q.Get("state") == "",
			// PKCE is asked of every sign-in here, so a client that forgets it is noticed.
			len(q.Get("code_challenge")) != 43 || q.Get("code_challenge_method") != "S256",
			needs[p] != "" && !slices.Contains(scope, needs[p]),
			// Without a nonce a real provider signs a token that fits any sign-in.
			needs[p] != "" && q.Get("nonce") == "",
			// Microsoft puts the code where it is told, and the callback reads the query.
			p == idp.Microsoft && q.Get("response_mode") != "query":
			http.Error(w, "idptest: a sign-in cannot begin like this", http.StatusBadRequest)
			return
		}
		code := random()
		s.mu.Lock()
		s.codes[code] = &grant{
			provider: p, who: s.person(p), redirect: q.Get("redirect_uri"),
			challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), scope: scope,
		}
		s.mu.Unlock()
		answer := back.Query()
		answer.Set("code", code)
		answer.Set("state", q.Get("state"))
		back.RawQuery = answer.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound) //nolint:gosec // sending the browser back where the client said is what a provider does; tests and dev mode only
	}
}

// refuseToken answers a token request that is wrong, each provider its way.
func refuseToken(w http.ResponseWriter, p idp.Provider) {
	switch p {
	case idp.GitHub: // a success that holds an error
		writeJSON(w, http.StatusOK, map[string]any{
			"error":             "bad_verification_code",
			"error_description": "The code passed is incorrect or expired.",
			"error_uri":         "https://docs.github.com/apps/managing-oauth-apps/troubleshooting-oauth-app-access-token-request-errors/#bad-verification-code",
		})
	case idp.Meta:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{
			"message": "This authorization code has been used.", "type": "OAuthException", "code": 100,
		}})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant", "error_description": "Bad Request"})
	}
}

// token exchanges a code, once, for the application that knows its secret
// and the verifier of the challenge the sign-in began with.
func (s *Server) token(p idp.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f := s.record(p, Token, r)
		if replaced(w, r, f.Token) {
			return
		}
		form := r.PostForm
		s.mu.Lock()
		g := s.codes[form.Get("code")]
		delete(s.codes, form.Get("code"))
		s.mu.Unlock()
		challenge := sha256.Sum256([]byte(form.Get("code_verifier")))
		app := s.App(p)
		switch {
		case r.Method != http.MethodPost,
			!strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded"),
			g == nil || g.provider != p,
			form.Get("grant_type") != "authorization_code",
			form.Get("client_id") != app.ClientID,
			subtle.ConstantTimeCompare([]byte(form.Get("client_secret")), []byte(app.ClientSecret)) != 1,
			form.Get("redirect_uri") != g.redirect,
			b64(challenge[:]) != g.challenge:
			refuseToken(w, p)
			return
		}
		g.accessToken = random()
		s.mu.Lock()
		s.tokens[g.accessToken] = g
		s.mu.Unlock()

		members := map[string]any{"access_token": g.accessToken, "token_type": "Bearer", "expires_in": 3599}
		switch p {
		case idp.GitHub:
			members = map[string]any{"access_token": g.accessToken, "token_type": "bearer", "scope": strings.Join(g.scope, ",")}
		case idp.Meta:
			members = map[string]any{"access_token": g.accessToken, "token_type": "bearer", "expires_in": 5183944}
		default:
			members["scope"] = strings.Join(g.scope, " ")
			members["id_token"] = s.idToken(p, f, g)
		}
		if f.EditTokenAnswer != nil {
			f.EditTokenAnswer(members)
		}
		// GitHub answers a form unless it is asked for JSON.
		if p == idp.GitHub && !strings.Contains(r.Header.Get("Accept"), "application/json") {
			form := url.Values{}
			for name, value := range members {
				form.Set(name, fmt.Sprint(value))
			}
			w.Header().Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
			_, _ = w.Write([]byte(form.Encode()))
			return
		}
		writeJSON(w, http.StatusOK, members)
	}
}

// idToken makes and signs the ID token of a sign-in, with the faults asked.
func (s *Server) idToken(p idp.Provider, f Faults, g *grant) string {
	kid, key := s.SigningKey()
	now := s.now()
	who := g.who
	app := s.App(p)
	claims := map[string]any{
		"aud": app.ClientID,
		"sub": who.Subject,
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	}
	if g.nonce != "" {
		claims["nonce"] = g.nonce
	}
	email, profile := slices.Contains(g.scope, "email"), slices.Contains(g.scope, "profile")
	if profile && who.Name != "" {
		claims["name"] = who.Name
	}
	switch p {
	case idp.Microsoft:
		tenant := who.Tenant
		if tenant == "" {
			tenant = WorkTenant
			if who.Verified {
				tenant = PersonalTenant
			}
		}
		claims["ver"] = "2.0"
		claims["iss"] = strings.ReplaceAll(MicrosoftIssuer, "{tenantid}", tenant)
		claims["tid"] = tenant
		claims["nbf"] = now.Unix()
		if profile && who.Object != "" {
			claims["oid"] = who.Object
		}
		if profile && who.Address != "" {
			claims["preferred_username"] = who.Address
		}
		if email && who.Address != "" {
			claims["email"] = who.Address
			// The claim an application asks for in its registration; it comes with the address.
			if who.Verified && tenant != PersonalTenant {
				claims["xms_edov"] = true
			}
		}
	default:
		claims["iss"] = GoogleIssuer
		claims["azp"] = app.ClientID
		if email && who.Address != "" {
			claims["email"] = who.Address
			claims["email_verified"] = who.Verified
		}
	}
	if f.EditClaims != nil {
		f.EditClaims(claims)
	}
	header := map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"}
	if f.EditHeader != nil {
		f.EditHeader(header)
	}
	rawHeader, rawClaims := f.RawHeader, f.RawClaims
	if rawHeader == nil {
		rawHeader, _ = json.Marshal(header)
	}
	if rawClaims == nil {
		rawClaims, _ = json.Marshal(claims)
	}
	signed := b64(rawHeader) + "." + b64(rawClaims)
	var signature []byte
	if f.Sign != nil {
		signature = f.Sign(signed)
	} else {
		signature = sign(key, signed)
	}
	token := signed + "." + b64(signature)
	if f.EditIDToken != nil {
		token = f.EditIDToken(token)
	}
	return token
}

// keySet writes the keys a provider publishes. The caller holds the lock.
func (s *Server) keySet(p idp.Provider, f Faults) []byte {
	public := s.key.PublicKey
	key := map[string]any{
		"kty": "RSA",
		"use": "sig",
		"kid": s.kid,
		"n":   b64(public.N.Bytes()),
		"e":   b64(big.NewInt(int64(public.E)).Bytes()),
	}
	if p == idp.Microsoft {
		// Microsoft names no algorithm, and says who signs with each key. Its
		// certificate and thumbprint are left out: there is no certificate here.
		key["issuer"] = MicrosoftIssuer
	} else {
		key["alg"] = "RS256"
	}
	keys := []map[string]any{key}
	if f.EditKeys != nil {
		keys = f.EditKeys(keys)
	}
	body, err := json.Marshal(map[string]any{"keys": keys})
	if err != nil {
		panic("idptest: " + err.Error())
	}
	return body
}

func (s *Server) keys(p idp.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f := s.record(p, Keys, r)
		if replaced(w, r, f.Keys) {
			return
		}
		s.mu.Lock()
		body := s.keySet(p, f)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(body)
	}
}

// holder is the sign-in an access token of a provider belongs to.
func (s *Server) holder(p idp.Provider, r *http.Request) (*grant, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return nil, errors.New("no access token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.tokens[token]
	if g == nil || g.provider != p {
		return nil, errors.New("unknown access token")
	}
	return g, nil
}

func githubRefusal(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"message": message, "documentation_url": "https://docs.github.com/rest", "status": strconv.Itoa(status)})
}

// githubID is a person's GitHub identifier as GitHub writes it: a number.
func githubID(who Person) any {
	if id, err := strconv.ParseInt(who.Subject, 10, 64); err == nil {
		return id
	}
	return who.Subject // not a number: the client must refuse it
}

// githubUser is GET /user. Its "email" is the one the person shows on their
// profile, which is null here: not the one to sign in with.
func (s *Server) githubUser(w http.ResponseWriter, r *http.Request) {
	f := s.record(idp.GitHub, User, r)
	if replaced(w, r, f.User) {
		return
	}
	g, err := s.holder(idp.GitHub, r)
	if err != nil {
		githubRefusal(w, http.StatusUnauthorized, "Requires authentication")
		return
	}
	user := map[string]any{
		"login":      "person-" + g.who.Subject,
		"id":         githubID(g.who),
		"node_id":    "MDQ6VXNlcjE=",
		"type":       "User",
		"site_admin": false,
		"name":       nil,
		"email":      nil,
		"created_at": "2008-01-14T04:33:35Z",
	}
	if g.who.Name != "" {
		user["name"] = g.who.Name
	}
	if f.EditUser != nil {
		f.EditUser(user)
	}
	writeJSON(w, http.StatusOK, user)
}

// githubEmails is GET /user/emails, which needs the user:email scope.
func (s *Server) githubEmails(w http.ResponseWriter, r *http.Request) {
	f := s.record(idp.GitHub, Emails, r)
	if replaced(w, r, f.Emails) {
		return
	}
	g, err := s.holder(idp.GitHub, r)
	if err != nil {
		githubRefusal(w, http.StatusUnauthorized, "Requires authentication")
		return
	}
	if !slices.Contains(g.scope, "user:email") {
		githubRefusal(w, http.StatusNotFound, "Not Found")
		return
	}
	// The address GitHub makes for everyone comes first, to be passed over.
	emails := []map[string]any{{
		"email": g.who.Subject + "+person@users.noreply.github.com", "primary": false, "verified": true, "visibility": nil,
	}}
	if g.who.Address != "" {
		emails = append(emails, map[string]any{
			"email": g.who.Address, "primary": true, "verified": g.who.Verified, "visibility": "private",
		})
	}
	if f.EditEmails != nil {
		emails = f.EditEmails(emails)
	}
	writeJSON(w, http.StatusOK, emails)
}

// metaUser is GET /me, which wants the proof made with the application's secret.
func (s *Server) metaUser(w http.ResponseWriter, r *http.Request) {
	f := s.record(idp.Meta, User, r)
	if replaced(w, r, f.User) {
		return
	}
	refusal := func(message, kind string, code int) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": message, "type": kind, "code": code}})
	}
	g, err := s.holder(idp.Meta, r)
	if err != nil {
		refusal("An active access token must be used to query information about the current user.", "OAuthException", 2500)
		return
	}
	mac := hmac.New(sha256.New, []byte(s.App(idp.Meta).ClientSecret))
	mac.Write([]byte(g.accessToken))
	if !hmac.Equal([]byte(r.URL.Query().Get("appsecret_proof")), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		refusal("Invalid appsecret_proof provided in the API argument", "GraphMethodException", 100)
		return
	}
	// Meta says nothing of whether an address is verified, so Verified changes nothing here.
	all := map[string]any{"id": g.who.Subject}
	if g.who.Name != "" {
		all["name"] = g.who.Name
	}
	if g.who.Address != "" && slices.Contains(g.scope, "email") {
		all["email"] = g.who.Address
	}
	user := map[string]any{"id": all["id"]} // the identifier always comes
	for _, field := range strings.Split(r.URL.Query().Get("fields"), ",") {
		if value, ok := all[field]; ok {
			user[field] = value
		}
	}
	if f.EditUser != nil {
		f.EditUser(user)
	}
	writeJSON(w, http.StatusOK, user)
}
