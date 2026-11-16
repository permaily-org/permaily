package connections

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/vendors"
)

// Stand-ins for the functions of internal/oauth that ADR 0030's pull request
// 1 writes ("The functions each pull request writes"), with its signatures
// and behaviour as that section and "The authorization request" say. This
// pull request is built at the same time and merges after pull request 1
// (ADR 0030, "Implementation pull requests"): once it has merged, each of
// these is replaced by the call to oauth's function of the same name and
// this file is deleted, with no other change to the service. They are
// unexported so nothing outside the service comes to rely on them.

// purposeFamilies derives the key of a family's tokens (oauth.FamilySecret).
const purposeFamilies = "permaily oauth families v1"

var b64 = base64.RawURLEncoding

// newSecret is oauth.NewSecret: prefix and SecretBytes random bytes.
func newSecret(prefix string, random func([]byte) error) (string, error) {
	b := make([]byte, oauth.SecretBytes)
	if err := random(b); err != nil {
		return "", err
	}
	return prefix + b64.EncodeToString(b), nil
}

// hashOf is oauth.Hash: the hex SHA-256 of "permaily oauth <kind> v1\x00"
// and the secret.
func hashOf(k oauth.Kind, secret string) string {
	h := sha256.Sum256([]byte("permaily oauth " + string(k) + " v1\x00" + secret))
	return hex.EncodeToString(h[:])
}

// familySecret is oauth.FamilySecret.
func familySecret(key []byte, k oauth.Kind, family string, generation uint64) string {
	prefix := oauth.PrefixAccess
	if k == oauth.KindRefresh {
		prefix = oauth.PrefixRefresh
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(string(k) + "\x00" + family + "\x00" + strconv.FormatUint(generation, 10)))
	return prefix + b64.EncodeToString(m.Sum(nil))
}

var pkceRE = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

// verifyPKCE is oauth.VerifyPKCE.
func verifyPKCE(challenge, verifier string) bool {
	if !pkceRE.MatchString(verifier) || !pkceRE.MatchString(challenge) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(b64.EncodeToString(sum[:])), []byte(challenge)) == 1
}

// parseScopes is oauth.Scopes: the default three when absent, mail.sender
// always first, in ScopeParts' order; an unknown or repeated one refused.
func parseScopes(s string, present bool) ([]string, error) {
	if !present {
		return slices.Clone(oauth.DefaultScopes), nil
	}
	asked := strings.Fields(s)
	seen := map[string]bool{oauth.ScopeSender: true}
	for _, a := range asked {
		known := slices.ContainsFunc(oauth.ScopeParts, func(p struct {
			Scope string
			Part  contracts.Part
		}) bool {
			return p.Scope == a
		})
		if !known || (seen[a] && a != oauth.ScopeSender) {
			return nil, errors.New("scope")
		}
		seen[a] = true
	}
	var out []string
	for _, p := range oauth.ScopeParts {
		if seen[p.Scope] {
			out = append(out, p.Scope)
		}
	}
	return out, nil
}

// partsOf is oauth.Parts.
func partsOf(scopes []string) []contracts.Part {
	out := []contracts.Part{contracts.PartSender}
	for _, p := range oauth.ScopeParts[1:] {
		if slices.Contains(scopes, p.Scope) {
			out = append(out, p.Part)
		}
	}
	return out
}

// loopbackHost says a redirect's host is the person's own computer.
func loopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "[::1]" || host == "::1"
}

// redirectAllowed is oauth.RedirectAllowed: exact, or a loopback one on any
// port (RFC 8252 section 7.3).
func redirectAllowed(registered []string, got string) bool {
	g, err := url.Parse(got)
	if err != nil || g.Fragment != "" || g.User != nil {
		return false
	}
	for _, r := range registered {
		if r == got {
			return true
		}
		u, err := url.Parse(r)
		if err != nil || u.Scheme != "http" || g.Scheme != "http" || !loopbackHost(u.Hostname()) {
			continue
		}
		if u.Hostname() == g.Hostname() && u.EscapedPath() == g.EscapedPath() && u.RawQuery == g.RawQuery {
			return true
		}
	}
	return false
}

// redirectWith is oauth.Redirect: the redirect URI with params and iss.
func redirectWith(redirectURI string, params url.Values, issuer string) string {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return ""
	}
	q := u.Query()
	for k, vs := range params {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	q.Set("iss", issuer)
	u.RawQuery = q.Encode()
	return u.String()
}

// resourceOf maps a resource identifier to the resource.
func resourceOf(c oauth.Config, uri string) (contracts.Resource, bool) {
	switch uri {
	case c.APIResource:
		return contracts.ResourceAPI, true
	case c.MCPResource:
		return contracts.ResourceMCP, true
	}
	return "", false
}

// authorizeParams is oauth.Authorize for a request whose client is known:
// each parameter at most once, checked in "The authorization request"'s
// order; proven says the redirect URI was, so that a refusal may be
// redirected (with the code of the *OAuthError).
func authorizeParams(q url.Values, doc *oauth.ClientDocument, c oauth.Config) (*oauth.AuthorizeParams, bool, error) {
	one := func(k string) (string, bool, bool) {
		v, ok := q[k]
		return strings.Join(v, ""), ok, len(v) <= 1
	}
	redirect, ok, single := one("redirect_uri")
	if !ok || !single || !redirectAllowed(doc.RedirectURIs, redirect) {
		return nil, false, &clientRefusal{contracts.RedirectURIInvalid}
	}
	p := &oauth.AuthorizeParams{ClientID: doc.ClientID, RedirectURI: redirect}
	for _, v := range q {
		if len(v) > 1 {
			p.State = q.Get("state")
			return p, true, &OAuthError{oauth.ErrInvalidRequest}
		}
	}
	p.State = q.Get("state")
	if len(p.State) > oauth.StateMaxBytes {
		p.State = ""
		return p, true, &OAuthError{oauth.ErrInvalidRequest}
	}
	if q.Get("response_type") != "code" {
		return p, true, &OAuthError{oauth.ErrUnsupportedResponse}
	}
	p.ResponseType = "code"
	if _, ok := q["request_uri"]; ok {
		return p, true, &OAuthError{oauth.ErrInvalidRequest}
	}
	for _, k := range []string{"query", "mail_since", "ends_in_days"} {
		if _, ok := q[k]; ok {
			return p, true, &OAuthError{oauth.ErrInvalidRequest}
		}
	}
	p.CodeChallenge, p.CodeChallengeMethod = q.Get("code_challenge"), q.Get("code_challenge_method")
	if !pkceRE.MatchString(p.CodeChallenge) || p.CodeChallengeMethod != "S256" {
		return p, true, &OAuthError{oauth.ErrInvalidRequest}
	}
	p.Resource = q.Get("resource")
	if _, ok := resourceOf(c, p.Resource); !ok {
		return p, true, &OAuthError{oauth.ErrInvalidTarget}
	}
	scope, present := q["scope"]
	scopes, err := parseScopes(strings.Join(scope, ""), present)
	if err != nil {
		return p, true, &OAuthError{oauth.ErrInvalidScope}
	}
	p.Scope = scopes
	return p, true, nil
}

// pushedParams is oauth.Pushed: authorizeParams of a form, with query,
// mail_since and ends_in_days, which are taken only here. A refusal is the
// *OAuthError of the endpoint's JSON answer, never a redirect.
func pushedParams(form url.Values, doc *oauth.ClientDocument, c oauth.Config) (*oauth.AuthorizeParams, error) {
	extra := url.Values{}
	plain := url.Values{}
	for k, v := range form {
		switch k {
		case "query", "mail_since", "ends_in_days":
			extra[k] = v
		case "client_id", "client_assertion", "client_assertion_type":
		default:
			plain[k] = v
		}
	}
	p, proven, err := authorizeParams(plain, doc, c)
	if !proven {
		return nil, &OAuthError{oauth.ErrInvalidRequest}
	}
	if err != nil {
		return nil, err
	}
	for _, v := range extra {
		if len(v) > 1 {
			return nil, &OAuthError{oauth.ErrInvalidRequest}
		}
	}
	if q, ok := extra["query"]; ok {
		if len(q[0]) > contracts.MaxFormulaBytes || !json.Valid([]byte(q[0])) {
			return nil, &OAuthError{oauth.ErrInvalidRequest}
		}
		p.Query = []byte(q[0])
	}
	if s, ok := extra["mail_since"]; ok {
		if s[0] != "new" {
			if _, err := time.Parse(time.RFC3339, s[0]); err != nil {
				return nil, &OAuthError{oauth.ErrInvalidRequest}
			}
		}
		p.Since = s[0]
	}
	if d, ok := extra["ends_in_days"]; ok {
		n, err := strconv.Atoi(d[0])
		if err != nil || n < 1 || n > oauth.ConnectionMaxDays {
			return nil, &OAuthError{oauth.ErrInvalidRequest}
		}
		p.EndsInDays = n
	}
	return p, nil
}

// errUnknownKey is an assertion signed with a kid the key set does not have:
// the caller fetches the key set once more and tries again.
var errUnknownKey = errors.New("connections: the assertion's key is not in the key set")

// errAssertion is an assertion that cannot be accepted, whatever the reason.
var errAssertion = errors.New("connections: the client assertion is not valid")

// verifyAssertion is oauth.VerifyAssertion over the resolver's keys: it
// checks the compact JWS, its signature and its claims for the document's
// client at one of audiences at now, and returns its jti and exp.
func verifyAssertion(doc *oauth.ClientDocument, keys []vendors.Key, assertion string, audiences []string, now time.Time) (string, time.Time, error) {
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return "", time.Time{}, errAssertion
	}
	rawHeader, err1 := b64.DecodeString(parts[0])
	rawClaims, err2 := b64.DecodeString(parts[1])
	sig, err3 := b64.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || duplicateMembers(rawHeader) || duplicateMembers(rawClaims) {
		return "", time.Time{}, errAssertion
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if json.Unmarshal(rawHeader, &header) != nil || (header.Alg != "RS256" && header.Alg != "ES256") {
		return "", time.Time{}, errAssertion
	}
	if doc.TokenEndpointAuthAlg != "" && doc.TokenEndpointAuthAlg != header.Alg {
		return "", time.Time{}, errAssertion
	}
	i := slices.IndexFunc(keys, func(k vendors.Key) bool { return k.ID == header.Kid && k.Algorithm == header.Alg })
	if i < 0 {
		return "", time.Time{}, errUnknownKey
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	switch pub := keys[i].Public.(type) {
	case *rsa.PublicKey:
		if header.Alg != "RS256" || pub.N.BitLen() < 2048 || rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig) != nil {
			return "", time.Time{}, errAssertion
		}
	case *ecdsa.PublicKey:
		if header.Alg != "ES256" || pub.Curve != elliptic.P256() || len(sig) != 64 ||
			!ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			return "", time.Time{}, errAssertion
		}
	default:
		return "", time.Time{}, errAssertion
	}
	var claims struct {
		Iss string          `json:"iss"`
		Sub string          `json:"sub"`
		Aud json.RawMessage `json:"aud"`
		Exp *float64        `json:"exp"`
		Iat *float64        `json:"iat"`
		JTI string          `json:"jti"`
	}
	if json.Unmarshal(rawClaims, &claims) != nil || claims.Iss != doc.ClientID || claims.Sub != doc.ClientID || claims.Exp == nil {
		return "", time.Time{}, errAssertion
	}
	var auds []string
	var one string
	if json.Unmarshal(claims.Aud, &one) == nil {
		auds = []string{one}
	} else if json.Unmarshal(claims.Aud, &auds) != nil {
		return "", time.Time{}, errAssertion
	}
	if !slices.ContainsFunc(auds, func(a string) bool { return slices.Contains(audiences, a) }) {
		return "", time.Time{}, errAssertion
	}
	exp := time.Unix(int64(*claims.Exp), 0)
	if !oauth.AssertionLive(exp, now) || exp.After(now.Add(oauth.AssertionMaxLife)) {
		return "", time.Time{}, errAssertion
	}
	if claims.Iat != nil && time.Unix(int64(*claims.Iat), 0).After(now.Add(oauth.ClockSkew)) {
		return "", time.Time{}, errAssertion
	}
	if len(claims.JTI) < 1 || len(claims.JTI) > 256 {
		return "", time.Time{}, errAssertion
	}
	return claims.JTI, exp, nil
}

// duplicateMembers says a JSON object names one member twice at its top.
func duplicateMembers(raw []byte) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return true
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	if _, err := d.Token(); err != nil {
		return true
	}
	seen := map[string]bool{}
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return true
		}
		k, _ := t.(string)
		if seen[k] {
			return true
		}
		seen[k] = true
		var skip json.RawMessage
		if d.Decode(&skip) != nil {
			return true
		}
	}
	return false
}

// redirectHost is what the screen shows of a redirect URI: its host without
// a port, and whether it is the person's own computer.
func redirectHost(redirectURI string) (string, bool) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return "", false
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		host = "[" + host + "]"
	}
	return host, u.Scheme == "http" && loopbackHost(u.Hostname())
}
