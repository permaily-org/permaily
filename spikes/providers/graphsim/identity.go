package graphsim

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PersonalTenant is the tenant of every personal Microsoft account
// (id-token-personal-tid).
const PersonalTenant = "9188040d-6c67-4c5b-b112-36a304b66dad"

// Account is one Microsoft account with one mailbox.
type Account struct {
	OID               string
	TID               string
	Sub               string
	Email             string // empty: the account has no email address
	PreferredUsername string
	Locale            string // "en" or "es" for the folders' display names

	// Conditions the person or their administrator set.
	AdminApprovalNeeded bool // a work tenant whose users may not consent
	RequireMFA          bool // interaction-required at the token endpoint
	BlockedByCA         bool
	RequireClaims       bool // Graph answers 403 insufficient_claims

	tokensValidFrom time.Time
	box             *mailbox
}

type grant struct {
	account   *Account
	scopes    []string
	nonce     string
	clientID  string
	issued    time.Time
	lastUsed  time.Time
	expires   time.Time
	redeemed  bool
	codeOwner string // tenant path the code was issued for
}

// AddAccount registers an account and makes its mailbox.
func (s *Sim) AddAccount(a *Account) *Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.Locale == "" {
		a.Locale = "en"
	}
	a.box = newMailbox(a.Locale)
	s.accounts[a.OID] = a
	return a
}

// AuthorizeRequest is what the browser carries to /authorize.
type AuthorizeRequest struct {
	ClientID string
	Scopes   []string
	Nonce    string
	// Decline makes the person refuse consent.
	Decline bool
}

// Authorize plays the person's trip through /authorize and returns either
// a code or the error query sent to the redirect URI.
func (s *Sim) Authorize(a *Account, req AuthorizeRequest) (code string, redirectError url.Values) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case req.Decline:
		// authorize-error-shape: access_denied, the code only in the text.
		return "", url.Values{
			"error":             {"access_denied"},
			"error_description": {"AADSTS65004: User declined to consent to access the app."},
		}
	case a.AdminApprovalNeeded && a.TID != PersonalTenant:
		// admin-approval-redirect (not documented): assumed to arrive as
		// access_denied with the code inside error_description only.
		return "", url.Values{
			"error":             {"access_denied"},
			"error_description": {"AADSTS90094: Administrator consent is required."},
		}
	}
	s.seq++
	code = fmt.Sprintf("sim-code-%d", s.seq)
	s.codes[code] = &grant{
		account:  a,
		scopes:   append([]string(nil), req.Scopes...),
		nonce:    req.Nonce,
		clientID: req.ClientID,
		issued:   s.now,
		lastUsed: s.now,
	}
	return code, nil
}

// UserChangesPassword is the person changing their own password: a
// confidential client's refresh token stays alive (refresh-revocation-table).
func (s *Sim) UserChangesPassword(*Account) {}

// AdminResetsPassword is an administrator resetting the password in the
// Entra admin center: confidential clients' refresh tokens are revoked.
func (s *Sim) AdminResetsPassword(a *Account) { s.RevokeAll(a) }

// RevokeAll revokes every refresh token of the account.
func (s *Sim) RevokeAll(a *Account) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a.tokensValidFrom = s.now.Add(time.Second)
}

func (s *Sim) identity(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case len(parts) == 4 && parts[1] == "oauth2" && parts[2] == "v2.0" && parts[3] == "token" && r.Method == http.MethodPost:
		s.token(w, r, parts[0])
	case len(parts) == 4 && parts[1] == "discovery" && parts[2] == "v2.0" && parts[3] == "keys":
		s.keys(w)
	case len(parts) == 4 && parts[1] == "v2.0" && parts[2] == ".well-known" && parts[3] == "openid-configuration":
		s.configuration(w, parts[0])
	default:
		http.NotFound(w, r)
	}
}

var oidcScopes = map[string]bool{"openid": true, "email": true, "profile": true, "offline_access": true}

func has(scopes []string, want string) bool {
	for _, s := range scopes {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

func (s *Sim) token(w http.ResponseWriter, r *http.Request, tenant string) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, "invalid_request", 9002313, "AADSTS9002313: Request is malformed or invalid.")
		return
	}
	if r.PostForm.Get("client_secret") == "" && r.PostForm.Get("client_assertion") == "" {
		tokenError(w, "invalid_client", 7000218, "AADSTS7000218: The request body must contain the following parameter: 'client_assertion' or 'client_secret'.")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		g := s.codes[r.PostForm.Get("code")]
		if g == nil {
			tokenError(w, "invalid_grant", 70000, "AADSTS70000: The provided value for the 'code' parameter is not valid.")
			return
		}
		if g.redeemed {
			tokenError(w, "invalid_grant", 54005, "AADSTS54005: OAuth2 Authorization code was already redeemed.")
			return
		}
		if tenant != "common" && tenant != g.account.TID &&
			!(tenant == "consumers" && g.account.TID == PersonalTenant) &&
			!(tenant == "organizations" && g.account.TID != PersonalTenant) {
			tokenError(w, "invalid_grant", 700005, "AADSTS700005: Provided Authorization Code is intended to use against other tenant.")
			return
		}
		g.redeemed = true
		if s.conditionError(w, g.account) {
			return
		}
		s.issue(w, g, g.scopes, true)
	case "refresh_token":
		g := s.refresh[r.PostForm.Get("refresh_token")]
		if g == nil {
			tokenError(w, "invalid_grant", 70000, "AADSTS70000: The refresh token isn't valid.")
			return
		}
		// refresh-tenant-path (not documented): a tenant path naming another
		// tenant is refused; the account's own tid, personal included, works.
		if tenant != "common" && tenant != g.account.TID {
			tokenError(w, "invalid_grant", 700005, "AADSTS700005: The grant was issued for another tenant.")
			return
		}
		if !g.account.tokensValidFrom.IsZero() && g.issued.Before(g.account.tokensValidFrom) {
			tokenError(w, "invalid_grant", 50173, "AADSTS50173: The provided grant has expired due to it being revoked, a fresh auth token is needed.")
			return
		}
		if s.now.Sub(g.lastUsed) > 90*24*time.Hour {
			tokenError(w, "invalid_grant", 700082, "AADSTS700082: The refresh token has expired due to inactivity.")
			return
		}
		if s.conditionError(w, g.account) {
			return
		}
		scopes := strings.Fields(r.PostForm.Get("scope"))
		if len(scopes) == 0 {
			scopes = g.scopes
		}
		// The old token stays valid (refresh-rotates); the new one starts a
		// fresh inactivity window.
		g.lastUsed = s.now
		s.issue(w, g, scopes, false)
	default:
		tokenError(w, "unsupported_grant_type", 70003, "AADSTS70003: The app requested an unsupported grant type.")
	}
}

func (s *Sim) conditionError(w http.ResponseWriter, a *Account) bool {
	switch {
	case a.BlockedByCA:
		// blocked-by-ca (not documented which `error` carries 53003).
		tokenError(w, "invalid_grant", 53003, "AADSTS53003: Access has been blocked by Conditional Access policies.")
		return true
	case a.RequireMFA:
		tokenError(w, "interaction_required", 50076, "AADSTS50076: Due to a configuration change made by your administrator, you must use multi-factor authentication.")
		return true
	}
	return false
}

// issue writes a token response for grant g and the scopes of this leg.
func (s *Sim) issue(w http.ResponseWriter, g *grant, legScopes []string, first bool) {
	s.seq++
	at := fmt.Sprintf("sim-access-%d", s.seq)
	s.access[at] = &grant{account: g.account, scopes: legScopes, issued: s.now, expires: s.now.Add(3599 * time.Second)}
	resp := map[string]any{
		"access_token": at,
		"token_type":   "Bearer",
		"expires_in":   3599,
	}
	var resource []string
	for _, sc := range legScopes {
		if !oidcScopes[strings.ToLower(sc)] {
			resource = append(resource, canonicalScope(sc))
		}
	}
	switch s.ScopeMode {
	case ScopeResourceOnly:
		resp["scope"] = strings.Join(resource, " ")
	case ScopeDocExample:
		low := make([]string, len(resource))
		for i, r := range resource {
			low[i] = url.QueryEscape("https://graph.microsoft.com/" + strings.ToLower(r))
		}
		resp["scope"] = strings.Join(low, " ")
	case ScopeWithOIDC:
		all := append([]string(nil), resource...)
		for _, sc := range []string{"openid", "profile", "email", "offline_access"} {
			if has(g.scopes, sc) {
				all = append(all, sc)
			}
		}
		resp["scope"] = strings.Join(all, " ")
	case ScopeOmitted:
	}
	// refresh-needs-offline-access: the grant's first leg decides.
	if has(g.scopes, "offline_access") {
		s.seq++
		rt := fmt.Sprintf("sim-refresh-%d", s.seq)
		s.refresh[rt] = &grant{account: g.account, scopes: g.scopes, nonce: g.nonce, clientID: g.clientID, issued: s.now, lastUsed: s.now}
		resp["refresh_token"] = rt
	}
	// id-token-needs-openid: this leg's scopes decide.
	if has(legScopes, "openid") {
		nonce := ""
		if first {
			nonce = g.nonce
		}
		resp["id_token"] = s.idToken(g.account, g.clientID, legScopes, g.scopes, nonce)
	}
	writeJSON(w, http.StatusOK, resp)
}

// canonicalScope maps a Graph scope to its short canonical form.
func canonicalScope(sc string) string {
	sc = strings.TrimPrefix(sc, "https://graph.microsoft.com/")
	if strings.EqualFold(sc, "mail.read") {
		return "Mail.Read"
	}
	if strings.EqualFold(sc, "user.read") {
		return "User.Read"
	}
	return sc
}

func (s *Sim) idToken(a *Account, clientID string, legScopes, grantScopes []string, nonce string) string {
	claims := map[string]any{
		"aud": clientID,
		"iss": "https://" + LoginHost + "/" + a.TID + "/v2.0",
		"iat": s.now.Unix(),
		"nbf": s.now.Unix(),
		"exp": s.now.Add(time.Hour).Unix(),
		"sub": a.Sub,
		"tid": a.TID,
		"ver": "2.0",
	}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	consented := append(append([]string(nil), grantScopes...), legScopes...)
	// id-token-oid-needs-profile.
	if has(consented, "profile") {
		claims["oid"] = a.OID
		if a.PreferredUsername != "" {
			claims["preferred_username"] = a.PreferredUsername
		}
	}
	// id-token-email.
	if has(consented, "email") && a.Email != "" {
		claims["email"] = a.Email
	}
	header := map[string]string{"typ": "JWT", "alg": "RS256", "kid": s.kid}
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	signing := b64(hb) + "." + b64(cb)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.signer, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return signing + "." + b64(sig)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (s *Sim) keys(w http.ResponseWriter) {
	pub := s.signer.PublicKey
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "RSA",
		"use": "sig",
		"kid": s.kid,
		"n":   b64(pub.N.Bytes()),
		"e":   b64(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

func (s *Sim) configuration(w http.ResponseWriter, tenant string) {
	base := "https://" + LoginHost + "/" + tenant
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                 base + "/v2.0",
		"authorization_endpoint": base + "/oauth2/v2.0/authorize",
		"token_endpoint":         base + "/oauth2/v2.0/token",
		"jwks_uri":               base + "/discovery/v2.0/keys",
	})
}

// tokenError writes the documented error body (token-error-shape).
func tokenError(w http.ResponseWriter, code string, aadsts int, description string) {
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	trace := hex.EncodeToString(id)
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"error":             code,
		"error_description": description + "\r\nTrace ID: " + trace + "\r\nCorrelation ID: " + trace + "\r\nTimestamp: 2026-11-27 12:00:00Z",
		"error_codes":       []int{aadsts},
		"timestamp":         "2026-11-27 12:00:00Z",
		"trace_id":          trace,
		"correlation_id":    trace,
		"error_uri":         fmt.Sprintf("https://%s/error?code=%d", LoginHost, aadsts),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
