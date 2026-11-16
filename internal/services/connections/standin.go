package connections

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/vendors"
	"golang.org/x/net/idna"
)

// Stand-ins for the functions of internal/oauth and internal/jws that ADR
// 0030's pull request 1 (#133) writes, ported from it as merged into develop
// (f0fb9b6) with the same behaviour, refusals and order of checks, so that
// wiring the real ones changes nothing a test sees (the review of pull
// request 136). They
// are unexported so nothing outside the service comes to rely on them.
// Once #133 merges, each is replaced by the call to its namesake
// (newSecret: oauth.NewSecret, hashOf: oauth.Hash, familySecret:
// oauth.FamilySecret, verifyPKCE: oauth.VerifyPKCE, parseScopes:
// oauth.Scopes, partsOf: oauth.Parts, redirectAllowed: oauth.RedirectAllowed,
// redirectWith: oauth.Redirect, authorizeParams: oauth.Authorize,
// pushedParams: oauth.Pushed, verifyAssertion: oauth.VerifyAssertion with
// the resolver's keys converted to jws.Key, ID, Algorithm and Public kept),
// *oauthRefusal by *oauth.Error and errUnknownKey by jws.ErrUnknownKey;
// each call site already converts every refusal explicitly (endpointError,
// shownClient, redirectCode), failing closed on anything else. This file
// then goes.

// oauthRefusal is oauth.Error: the code to answer, a fixed reason, and the
// cause for errors.Is.
type oauthRefusal struct {
	Code   oauth.ErrorCode
	Reason string
	cause  error
}

func (e *oauthRefusal) Error() string { return "oauth: " + string(e.Code) + " (" + e.Reason + ")" }
func (e *oauthRefusal) Unwrap() error { return e.cause }

const reasonAssertion = "assertion"

func refuseOAuth(code oauth.ErrorCode) *oauthRefusal {
	return &oauthRefusal{Code: code, Reason: string(code)}
}

func refuseClient(p contracts.ClientProblem) *oauthRefusal {
	return &oauthRefusal{Code: oauth.ErrInvalidClient, Reason: string(p)}
}

func refuseAssertion(cause error) *oauthRefusal {
	return &oauthRefusal{Code: oauth.ErrInvalidClient, Reason: reasonAssertion, cause: cause}
}

var errAssertion = errors.New("connections: the client assertion is not valid")

// ---- secrets ----

// newSecret is oauth.NewSecret.
func newSecret(prefix string, random func([]byte) error) (string, error) {
	b := make([]byte, oauth.SecretBytes)
	if err := random(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// hashOf is oauth.Hash.
func hashOf(k oauth.Kind, secret string) string {
	sum := sha256.Sum256([]byte("permaily oauth " + string(k) + " v1\x00" + secret))
	return hex.EncodeToString(sum[:])
}

var familyPrefix = map[oauth.Kind]string{oauth.KindAccess: oauth.PrefixAccess, oauth.KindRefresh: oauth.PrefixRefresh}

// familySecret is oauth.FamilySecret: its domain, the generation as eight
// big-endian bytes, and no prefix for another kind.
func familySecret(key []byte, k oauth.Kind, family string, generation uint64) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("permaily oauth family token v1\x00" + string(k) + "\x00" + family + "\x00"))
	mac.Write(binary.BigEndian.AppendUint64(nil, generation))
	return familyPrefix[k] + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ---- the authorization request ----

// authorizeParams is oauth.Authorize.
func authorizeParams(q url.Values, doc *oauth.ClientDocument, c oauth.Config) (p *oauth.AuthorizeParams, proven bool, err error) {
	if once(q, "client_id") != doc.ClientID {
		return nil, false, refuseClient(contracts.ClientIDInvalid)
	}
	if _, has := q["request_uri"]; has {
		for name := range q {
			if name != "client_id" && name != "request_uri" {
				return nil, false, refuseOAuth(oauth.ErrInvalidRequest)
			}
		}
		uri := once(q, "request_uri")
		if !pushedURI(uri) {
			return nil, false, refuseOAuth(oauth.ErrInvalidRequest)
		}
		return &oauth.AuthorizeParams{ClientID: doc.ClientID, RequestURI: uri}, false, nil
	}
	redirect := once(q, "redirect_uri")
	if !redirectAllowed(doc.RedirectURIs, redirect) {
		return nil, false, refuseClient(contracts.RedirectURIInvalid)
	}
	p = &oauth.AuthorizeParams{ClientID: doc.ClientID, RedirectURI: redirect}
	if state := once(q, "state"); len(state) <= oauth.StateMaxBytes {
		p.State = state
	}
	if err := readParams(p, q, c); err != nil {
		return p, true, err
	}
	return p, true, nil
}

// readParams is oauth.AuthorizeParams.read.
func readParams(p *oauth.AuthorizeParams, q url.Values, c oauth.Config) error {
	for name, values := range q {
		if len(values) > 1 {
			return refuseOAuth(oauth.ErrInvalidRequest)
		}
		if name == "response_mode" && values[0] != "query" {
			return refuseOAuth(oauth.ErrInvalidRequest)
		}
	}
	if p.ResponseType = q.Get("response_type"); p.ResponseType != "code" {
		return refuseOAuth(oauth.ErrUnsupportedResponse)
	}
	p.CodeChallenge, p.CodeChallengeMethod = q.Get("code_challenge"), q.Get("code_challenge_method")
	if !pkceValue(p.CodeChallenge) || p.CodeChallengeMethod != "S256" {
		return refuseOAuth(oauth.ErrInvalidRequest)
	}
	if p.Resource = q.Get("resource"); p.Resource == "" || (p.Resource != c.APIResource && p.Resource != c.MCPResource) {
		return refuseOAuth(oauth.ErrInvalidTarget)
	}
	scope, present := q["scope"]
	var err error
	if p.Scope, err = parseScopes(strings.Join(scope, ""), present); err != nil {
		return err
	}
	if len(q.Get("state")) > oauth.StateMaxBytes {
		return refuseOAuth(oauth.ErrInvalidRequest)
	}
	return nil
}

func once(q url.Values, name string) string {
	if v := q[name]; len(v) == 1 {
		return v[0]
	}
	return ""
}

func pushedURI(s string) bool {
	id, ok := strings.CutPrefix(s, oauth.RequestURIPrefix+oauth.PrefixPushed)
	return ok && secretValue(id)
}

func secretValue(s string) bool {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return err == nil && len(b) == oauth.SecretBytes && !strings.ContainsAny(s, "\r\n")
}

// pushedParams is oauth.Pushed.
func pushedParams(form url.Values, doc *oauth.ClientDocument, c oauth.Config) (*oauth.AuthorizeParams, error) {
	for _, name := range []string{"client_id", "redirect_uri"} {
		if len(form[name]) > 1 {
			return nil, refuseOAuth(oauth.ErrInvalidRequest)
		}
	}
	if form.Get("client_id") != doc.ClientID {
		return nil, refuseClient(contracts.ClientIDInvalid)
	}
	p := &oauth.AuthorizeParams{ClientID: doc.ClientID, RedirectURI: form.Get("redirect_uri")}
	if !redirectAllowed(doc.RedirectURIs, p.RedirectURI) {
		return nil, refuseClient(contracts.RedirectURIInvalid)
	}
	if _, has := form["request_uri"]; has || len(form.Encode()) > oauth.FormMaxBytes || len(form.Get("query")) > contracts.MaxFormulaBytes {
		return nil, refuseOAuth(oauth.ErrInvalidRequest)
	}
	if err := readParams(p, form, c); err != nil {
		return nil, err
	}
	p.State = form.Get("state")
	if _, has := form["query"]; has {
		p.Query = []byte(form.Get("query"))
		if _, err := query.Parse(p.Query, nil); err != nil {
			return nil, refuseOAuth(oauth.ErrInvalidRequest)
		}
	}
	if since, has := form["mail_since"]; has {
		if _, err := time.Parse(time.RFC3339, since[0]); since[0] != "new" && err != nil {
			return nil, refuseOAuth(oauth.ErrInvalidRequest)
		}
		p.Since = since[0]
	}
	if days, has := form["ends_in_days"]; has {
		n, err := strconv.Atoi(days[0])
		if err != nil || strconv.Itoa(n) != days[0] || n < 1 || n > oauth.ConnectionMaxDays {
			return nil, refuseOAuth(oauth.ErrInvalidRequest)
		}
		p.EndsInDays = n
	}
	return p, nil
}

func pkceValue(s string) bool {
	if len(s) < 43 || len(s) > 128 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0) {
			return false
		}
	}
	return true
}

// verifyPKCE is oauth.VerifyPKCE.
func verifyPKCE(challenge, verifier string) bool {
	if !pkceValue(verifier) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(want), []byte(challenge)) == 1
}

// parseScopes is oauth.Scopes: scope tokens separated by one space each,
// none twice.
func parseScopes(s string, present bool) ([]string, error) {
	if !present {
		return slices.Clone(oauth.DefaultScopes), nil
	}
	asked := map[string]bool{oauth.ScopeSender: true}
	seen := map[string]bool{}
	for _, token := range strings.Split(s, " ") {
		known := false
		for _, sp := range oauth.ScopeParts {
			known = known || sp.Scope == token
		}
		if seen[token] || !known {
			return nil, refuseOAuth(oauth.ErrInvalidScope)
		}
		seen[token], asked[token] = true, true
	}
	var out []string
	for _, sp := range oauth.ScopeParts {
		if asked[sp.Scope] {
			out = append(out, sp.Scope)
		}
	}
	return out, nil
}

// partsOf is oauth.Parts.
func partsOf(scopes []string) []contracts.Part {
	out := []contracts.Part{contracts.PartSender}
	for _, sp := range oauth.ScopeParts[1:] {
		if slices.Contains(scopes, sp.Scope) {
			out = append(out, sp.Part)
		}
	}
	return out
}

// redirectWith is oauth.Redirect: the query kept as it was, params and iss
// added after it.
func redirectWith(redirectURI string, params url.Values, issuer string) string {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return ""
	}
	added := url.Values{}
	for name, values := range params {
		added[name] = slices.Clone(values)
	}
	added.Set("iss", issuer)
	if u.RawQuery != "" {
		u.RawQuery += "&" + added.Encode()
	} else {
		u.RawQuery = added.Encode()
	}
	u.ForceQuery = false
	return u.String()
}

// ---- redirect URIs ----

var loopbackHosts = []string{"localhost", "127.0.0.1", "[::1]"}

// redirectAllowed is oauth.RedirectAllowed.
func redirectAllowed(registered []string, got string) bool {
	if !redirectURI(got) {
		return false
	}
	gotHost, gotRest, gotLoop := loopbackRedirect(got)
	for _, r := range registered {
		if r == got {
			return true
		}
		if host, rest, ok := loopbackRedirect(r); ok && gotLoop && host == gotHost && rest == gotRest {
			return true
		}
	}
	return false
}

func redirectURI(r string) bool {
	if len(r) > oauth.ClientIDMaxBytes || !visible(r, oauth.ClientIDMaxBytes) || strings.ContainsAny(r, `#\`) {
		return false
	}
	u, err := url.Parse(r)
	if err != nil || u.User != nil || u.Opaque != "" || u.Host == "" {
		return false
	}
	switch {
	case strings.HasPrefix(r, "https://"):
		authority, _, _ := strings.Cut(strings.TrimPrefix(r, "https://"), "/")
		authority, _, _ = strings.Cut(authority, "?")
		return !strings.Contains(authority, "%") && publicHost(u)
	case strings.HasPrefix(r, "http://"):
		host, _, ok := loopbackRedirect(r)
		return ok && host != ""
	}
	return false
}

func publicHost(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	if i := strings.LastIndex(u.Host, ":"); i >= 0 && !strings.HasSuffix(u.Host, "]") && !validPort(u.Host[i+1:]) {
		return false
	}
	if strings.HasPrefix(u.Host, "[") {
		a, err := netip.ParseAddr(host)
		return err == nil && a.Is6() && !a.Unmap().IsLoopback() && !a.Unmap().IsUnspecified()
	}
	labels := strings.Split(host, ".")
	if slices.Contains(labels, "") || !slices.EqualFunc(labels, labels, func(l, _ string) bool { return hostLabel(l) }) {
		return false
	}
	if numeric(labels[len(labels)-1]) {
		a, err := netip.ParseAddr(host)
		return err == nil && a.Is4() && a.String() == host && !a.IsLoopback() && !a.IsUnspecified()
	}
	return host != "localhost" && !strings.HasSuffix(host, ".localhost")
}

// hostLabel is oauth's: letters, digits and inner hyphens, at most 63
// bytes, and an A-label only when IDNA decodes it and encodes it back to
// itself.
func hostLabel(l string) bool {
	if len(l) > 63 || strings.HasPrefix(l, "-") || strings.HasSuffix(l, "-") {
		return false
	}
	for _, c := range l {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	if !strings.HasPrefix(l, "xn--") {
		return true
	}
	u, err := idna.Lookup.ToUnicode(l)
	if err != nil || u == l {
		return false
	}
	a, err := idna.Lookup.ToASCII(u)
	return err == nil && a == l
}

func numeric(label string) bool {
	if rest, ok := strings.CutPrefix(label, "0x"); ok {
		return strings.Trim(rest, "0123456789abcdef") == ""
	}
	return strings.Trim(label, "0123456789") == ""
}

func loopbackRedirect(r string) (host, rest string, ok bool) {
	after, ok := strings.CutPrefix(r, "http://")
	if !ok {
		return "", "", false
	}
	authority, rest := after, ""
	if i := strings.IndexAny(after, "/?"); i >= 0 {
		authority, rest = after[:i], after[i:]
	}
	host, port := authority, ""
	if i := strings.LastIndex(authority, ":"); i >= 0 && !strings.HasSuffix(authority, "]") {
		host, port = authority[:i], authority[i+1:]
		if !validPort(port) {
			return "", "", false
		}
	}
	if !slices.Contains(loopbackHosts, host) || strings.Contains(r, "#") {
		return "", "", false
	}
	return host, rest, true
}

func validPort(p string) bool {
	if p == "" || len(p) > 5 || strings.Trim(p, "0123456789") != "" || p[0] == '0' {
		return false
	}
	n := 0
	for i := range len(p) {
		n = n*10 + int(p[i]-'0')
	}
	return n <= 65535
}

// ---- private_key_jwt (oauth.VerifyAssertion over jws) ----

// The causes jws gives that the service tells apart.
var (
	errUnknownKey  = errors.New("jws: signed with a key the set does not hold")
	errUnusableKey = errors.New("jws: signed with a key that is not for this algorithm")
)

// jwsToken is jws.Token.
type jwsToken struct {
	alg, kid  string
	claims    []byte
	signed    string
	signature []byte
}

const (
	jwsMaxCompact = 16 << 10
	jwsMaxKeyID   = 256
	minRSABits    = 2048
	maxRSABits    = 4096
)

// parseJWS is jws.Parse (RS256 or ES256, any typ).
func parseJWS(compact string) (*jwsToken, error) {
	if len(compact) > jwsMaxCompact {
		return nil, errAssertion
	}
	parts := strings.Split(compact, ".")
	if len(parts) != 3 {
		return nil, errAssertion
	}
	header, ok1 := decodeB64(parts[0])
	claims, ok2 := decodeB64(parts[1])
	signature, ok3 := decodeB64(parts[2])
	if !ok1 || !ok2 || !ok3 {
		return nil, errAssertion
	}
	members, ok := jsonObject(header)
	if !ok {
		return nil, errAssertion
	}
	t := &jwsToken{claims: claims, signed: compact[:len(parts[0])+1+len(parts[1])], signature: signature}
	if t.alg, ok = jsonText(members["alg"]); !ok || (t.alg != "RS256" && t.alg != "ES256") {
		return nil, errAssertion
	}
	if value, has := members["typ"]; has {
		if _, ok := jsonText(value); !ok {
			return nil, errAssertion
		}
	}
	if _, has := members["crit"]; has {
		return nil, errAssertion
	}
	if t.kid, ok = jsonText(members["kid"]); !ok || !visible(t.kid, jwsMaxKeyID) {
		return nil, errAssertion
	}
	if value, has := members["b64"]; has && string(value) != "true" {
		return nil, errAssertion
	}
	if value, has := members["jwk"]; has && !wellFormedJWK(value) {
		return nil, errAssertion
	}
	if _, has := members["x5c"]; has {
		return nil, errAssertion
	}
	if value, has := members["nonce"]; has {
		if _, ok := jsonText(value); !ok {
			return nil, errAssertion
		}
	}
	return t, nil
}

func wellFormedJWK(raw json.RawMessage) bool {
	key, ok := jsonObject(raw)
	if !ok {
		return false
	}
	kty, _ := jsonText(key["kty"])
	allowed := map[string][]string{"RSA": {"n", "e"}, "EC": {"crv", "x", "y"}}[kty]
	for name, value := range key {
		switch {
		case name == "kty" || slices.Contains(allowed, name):
		case name == "kid" || name == "use" || name == "alg":
			if _, ok := jsonText(value); !ok {
				return false
			}
		default:
			return false
		}
	}
	if kty == "RSA" {
		n, okN := jwkNumber(key["n"], maxRSABits/8)
		e, okE := jwkNumber(key["e"], 4)
		return okN && okE && n.BitLen() >= minRSABits && n.BitLen() <= maxRSABits && e.BitLen() <= 31 && e.Int64() >= 3 && e.Bit(0) == 1
	}
	if kty != "EC" {
		return false
	}
	if crv, ok := jsonText(key["crv"]); !ok || crv != "P-256" {
		return false
	}
	x, okX := jwkCoordinate(key["x"])
	y, okY := jwkCoordinate(key["y"])
	if !okX || !okY {
		return false
	}
	_, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
	return err == nil
}

func jwkCoordinate(value json.RawMessage) ([]byte, bool) {
	s, ok := jsonText(value)
	if !ok {
		return nil, false
	}
	b, ok := decodeB64(s)
	return b, ok && len(b) == 32
}

func jwkNumber(value json.RawMessage, size int) (*big.Int, bool) {
	s, ok := jsonText(value)
	if !ok {
		return nil, false
	}
	b, ok := decodeB64(s)
	if !ok || len(b) == 0 || len(b) > size+1 {
		return nil, false
	}
	return new(big.Int).SetBytes(b), true
}

// verifyJWS is jws.Verify over the resolver's keys: the first key with the
// token's kid, refused when it is not for the token's algorithm.
func verifyJWS(t *jwsToken, keys []vendors.Key) error {
	var key *vendors.Key
	for i := range keys {
		if keys[i].ID == t.kid {
			key = &keys[i]
			break
		}
	}
	if key == nil {
		return errUnknownKey
	}
	if key.Algorithm != t.alg {
		return errUnusableKey
	}
	hash := sha256.Sum256([]byte(t.signed))
	switch t.alg {
	case "RS256":
		pub, ok := key.Public.(*rsa.PublicKey)
		if !ok || pub.N == nil || pub.N.BitLen() < minRSABits || pub.N.BitLen() > maxRSABits {
			return errUnusableKey
		}
		if rsa.VerifyPKCS1v15(pub, crypto.SHA256, hash[:], t.signature) != nil {
			return errAssertion
		}
	default:
		pub, ok := key.Public.(*ecdsa.PublicKey)
		if !ok || pub.Curve != elliptic.P256() {
			return errUnusableKey
		}
		if len(t.signature) != 64 {
			return errAssertion
		}
		if !ecdsa.Verify(pub, hash[:], new(big.Int).SetBytes(t.signature[:32]), new(big.Int).SetBytes(t.signature[32:])) {
			return errAssertion
		}
	}
	return nil
}

// verifyAssertion is oauth.VerifyAssertion: every refusal invalid_client,
// errUnknownKey its cause when the key set did not hold the kid.
func verifyAssertion(doc *oauth.ClientDocument, keys []vendors.Key, assertion string, audiences []string, now time.Time) (string, time.Time, error) {
	if doc.TokenEndpointAuthMethod != oauth.AuthPrivateKeyJWT {
		return "", time.Time{}, refuseAssertion(errAssertion)
	}
	t, err := parseJWS(assertion)
	if err != nil {
		return "", time.Time{}, refuseAssertion(err)
	}
	if doc.TokenEndpointAuthAlg != "" && t.alg != doc.TokenEndpointAuthAlg {
		return "", time.Time{}, refuseAssertion(errAssertion)
	}
	if err := verifyJWS(t, keys); err != nil {
		return "", time.Time{}, refuseAssertion(err)
	}
	claims, ok := jsonObject(t.claims)
	if !ok {
		return "", time.Time{}, refuseAssertion(errAssertion)
	}
	iss, okIss := jsonText(claims["iss"])
	sub, okSub := jsonText(claims["sub"])
	if !okIss || !okSub || iss != doc.ClientID || sub != doc.ClientID {
		return "", time.Time{}, refuseAssertion(errAssertion)
	}
	if !audienceOf(claims["aud"], audiences) {
		return "", time.Time{}, refuseAssertion(errAssertion)
	}
	seconds, ok := wholeSeconds(claims["exp"])
	exp := time.Unix(seconds, 0)
	if !ok || !oauth.AssertionLive(exp, now) || exp.After(now.Add(oauth.AssertionMaxLife)) {
		return "", time.Time{}, refuseAssertion(errAssertion)
	}
	for _, name := range []string{"iat", "nbf"} {
		if value, has := claims[name]; has {
			if at, ok := wholeSeconds(value); !ok || time.Unix(at, 0).After(now.Add(oauth.ClockSkew)) {
				return "", time.Time{}, refuseAssertion(errAssertion)
			}
		}
	}
	jti, ok := jsonText(claims["jti"])
	if !ok || jti == "" || len(jti) > 256 {
		return "", time.Time{}, refuseAssertion(errAssertion)
	}
	return jti, exp, nil
}

func audienceOf(aud json.RawMessage, audiences []string) bool {
	if s, ok := jsonText(aud); ok {
		return slices.Contains(audiences, s)
	}
	var raw []json.RawMessage
	if len(aud) == 0 || aud[0] != '[' || json.Unmarshal(aud, &raw) != nil {
		return false
	}
	found := false
	for _, item := range raw {
		s, ok := jsonText(item)
		if !ok {
			return false
		}
		found = found || slices.Contains(audiences, s)
	}
	return found
}

func wholeSeconds(value json.RawMessage) (int64, bool) {
	if len(value) == 0 || len(value) > 15 {
		return 0, false
	}
	n, err := strconv.ParseInt(string(value), 10, 64)
	return n, err == nil && n >= 0
}

// decodeB64 is jws's decode: base64url without padding, strict, no line
// breaks.
func decodeB64(s string) ([]byte, bool) {
	if strings.ContainsAny(s, "\r\n") {
		return nil, false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return b, err == nil
}

// jsonObject is jws.Object: one JSON object of valid UTF-8, every member
// once, names matched exactly, nothing after it.
func jsonObject(raw []byte) (map[string]json.RawMessage, bool) {
	if !utf8.Valid(raw) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if open, err := dec.Token(); err != nil || open != json.Delim('{') {
		return nil, false
	}
	members := map[string]json.RawMessage{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, false
		}
		name, _ := token.(string)
		if _, twice := members[name]; twice {
			return nil, false
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		members[name] = value
	}
	if _, err := dec.Token(); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return members, true
}

// jsonText is jws.Text: a JSON string, never null.
func jsonText(value json.RawMessage) (string, bool) {
	var s string
	if len(value) == 0 || value[0] != '"' || json.Unmarshal(value, &s) != nil {
		return "", false
	}
	return s, true
}

func visible(s string, limit int) bool {
	if s == "" || len(s) > limit {
		return false
	}
	for i := range len(s) {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// ---- what each call site answers of a refusal ----

// endpointError is what the token, revocation and pushed request endpoints
// answer of a refusal of these functions: its code, and invalid_request for
// anything that is not one.
func endpointError(err error) error {
	var r *oauthRefusal
	if errors.As(err, &r) {
		return oauthErr(r.Code)
	}
	return oauthErr(oauth.ErrInvalidRequest)
}

// shownClient is what the person is shown of a refusal whose redirect URI
// was not proven: the client's fixed problem, and redirect_uri_invalid for
// anything else, which is never redirected.
func shownClient(err error) *clientRefusal {
	var r *oauthRefusal
	if errors.As(err, &r) && r.Code == oauth.ErrInvalidClient {
		switch p := contracts.ClientProblem(r.Reason); p {
		case contracts.ClientIDInvalid, contracts.ClientDocumentInvalid, contracts.RedirectURIInvalid:
			return &clientRefusal{p}
		}
	}
	return &clientRefusal{contracts.RedirectURIInvalid}
}

// redirectCode is the code a refusal whose redirect URI was proven is sent
// back with: server_error for anything that is not one of these functions'.
func redirectCode(err error) oauth.ErrorCode {
	var r *oauthRefusal
	if errors.As(err, &r) {
		return r.Code
	}
	return oauth.ErrServerError
}
