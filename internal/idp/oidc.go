package idp

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Limits of an ID token and of the keys it is checked with. Each is well
// above what Google and Microsoft send: a token of one or two thousand bytes,
// a set of two to ten keys of 2048 bits (Microsoft's, with certificates, is
// thirteen thousand bytes).
const (
	maxIDToken = 16 << 10
	maxKeySet  = 256 << 10
	maxKeys    = 64
	maxKeyID   = 256
	maxSubject = 255 // OpenID Connect Core, section 2
	minKeyBits = 2048
	maxKeyBits = 4096

	// keysLife is how long a set of keys is believed without asking again, and
	// keysWait the least time between two askings, read or not: a token with a
	// key identifier nobody knows must not make the enclave ask without end.
	keysLife = time.Hour
	keysWait = time.Minute

	// clockSkew is how far the provider's clock may be ahead of the enclave's.
	clockSkew = time.Minute

	// tenantPlace stands, in Microsoft's issuer, for the tenant of each token.
	tenantPlace = "{tenantid}"
	// personalTenant is the tenant of every personal Microsoft account
	// (https://learn.microsoft.com/en-us/entra/identity-platform/id-token-claims-reference).
	personalTenant = "9188040d-6c67-4c5b-b112-36a304b66dad"
)

// signingKey is one key a provider publishes.
type signingKey struct {
	// public is nil for a key that is not one for RS256 signatures: it is
	// remembered so a token that names it is refused for that.
	public *rsa.PublicKey
	// issuer is who Microsoft says signs with the key: every tenant, or one.
	// Empty when the set does not say.
	issuer string
}

// keySet is the keys of one provider as they were last read.
type keySet struct {
	mu      sync.Mutex
	keys    map[string]*signingKey
	fetched time.Time // when keys was read
	tried   time.Time // when it was last asked for, read or not
}

// within says whether t is less than d before now. A clock set back makes
// nothing recent, so it can never keep the keys from being asked for.
func within(now, t time.Time, d time.Duration) bool {
	since := now.Sub(t)
	return !t.IsZero() && since >= 0 && since < d
}

// idToken checks the ID token a token endpoint gave, with the keys the
// provider publishes.
func (c *Client) idToken(ctx context.Context, p Provider, e Endpoints, clientID, nonce, token string) (*Identity, error) {
	// With no issuer every token would be "the provider's".
	if e.Issuer == "" || e.Keys == "" {
		return nil, errors.New("idp: a provider that signs needs an issuer and its keys")
	}
	find := func(kid string) (*signingKey, error) { return c.key(ctx, e.Keys, kid) }
	return verify(p, e.Issuer, clientID, nonce, token, c.now(), find)
}

// key returns the provider's key with an identifier. The set is read when
// there is none or it is old, and once more when it does not hold the
// identifier (the provider may have changed keys), never twice in keysWait.
func (c *Client) key(ctx context.Context, address, kid string) (*signingKey, error) {
	setsLock.Lock()
	if c.sets == nil {
		c.sets = map[string]*keySet{}
	}
	set := c.sets[address]
	if set == nil {
		set = &keySet{}
		c.sets[address] = set
	}
	setsLock.Unlock()

	// Held while asking: the sign-ins that wait for the same keys get them
	// from one request.
	set.mu.Lock()
	defer set.mu.Unlock()
	now := c.now()
	fresh := set.keys != nil && within(now, set.fetched, keysLife)
	if key, ok := set.keys[kid]; ok && fresh {
		return key, nil
	}
	if within(now, set.tried, keysWait) {
		if fresh {
			return nil, refuse("ID token signed with a key the provider does not publish")
		}
		return nil, unreachable("the keys were asked for a moment ago and not read")
	}
	before := set.tried
	set.tried = now
	keys, err := c.fetchKeys(ctx, address)
	if err != nil {
		// A person who went away in the middle must not make everyone else wait.
		if ctx.Err() != nil {
			set.tried = before
		}
		return nil, err
	}
	set.keys, set.fetched = keys, now
	if key, ok := keys[kid]; ok {
		return key, nil
	}
	return nil, refuse("ID token signed with a key the provider does not publish")
}

func (c *Client) fetchKeys(ctx context.Context, address string) (map[string]*signingKey, error) {
	body, err := c.get(ctx, address, nil, http.Header{"Accept": {"application/json"}}, "keys endpoint", maxKeySet)
	if err != nil {
		return nil, err
	}
	return parseKeys(body)
}

// parseKeys reads a JSON Web Key Set (RFC 7517). A key of another kind is
// kept as one that cannot be used, since a provider may publish some; two
// keys with one identifier are refused, since which one signs would be a
// matter of their order.
func parseKeys(body []byte) (map[string]*signingKey, error) {
	members, ok := object(body)
	if !ok {
		return nil, refuse("keys are not one JSON object")
	}
	items, ok := list(members["keys"], maxKeys)
	if !ok {
		return nil, refuse("keys without a list, or with one too long")
	}
	keys := map[string]*signingKey{}
	for _, item := range items {
		key, ok := object(item)
		if !ok {
			return nil, refuse("a key is not one JSON object")
		}
		kid, ok := text(key["kid"])
		if !ok || !visible(kid, maxKeyID) {
			continue // no token can name it
		}
		if _, twice := keys[kid]; twice {
			return nil, refuse("two keys with one identifier")
		}
		keys[kid] = parseKey(key)
	}
	return keys, nil
}

// parseKey reads one key. It is usable only as what the token's header must
// say too: an RSA key, for signatures, and for RS256 when it names an algorithm.
func parseKey(key map[string]json.RawMessage) *signingKey {
	unusable := &signingKey{}
	if kty, ok := text(key["kty"]); !ok || kty != "RSA" {
		return unusable
	}
	if value, has := key["use"]; has {
		if use, ok := text(value); !ok || use != "sig" {
			return unusable
		}
	}
	if value, has := key["alg"]; has {
		if alg, ok := text(value); !ok || alg != "RS256" {
			return unusable
		}
	}
	issuer, ok := optional(key["issuer"])
	if !ok {
		return unusable
	}
	n, ok := number(key["n"], maxKeyBits/8)
	if !ok || n.BitLen() < minKeyBits || n.BitLen() > maxKeyBits {
		return unusable
	}
	// crypto/rsa takes an exponent that fits an int; an even one is no exponent.
	e, ok := number(key["e"], 4)
	if !ok || e.BitLen() > 31 || e.Int64() < 3 || e.Bit(0) == 0 {
		return unusable
	}
	return &signingKey{public: &rsa.PublicKey{N: n, E: int(e.Int64())}, issuer: issuer}
}

// number is a member that is a positive number of at most size bytes, as a
// key writes it: base64url of its big-endian bytes.
func number(value json.RawMessage, size int) (*big.Int, bool) {
	s, ok := text(value)
	if !ok {
		return nil, false
	}
	b, ok := decode(s)
	if !ok || len(b) == 0 || len(b) > size+1 { // some writers put a zero byte in front
		return nil, false
	}
	return new(big.Int).SetBytes(b), true
}

// decode reads base64url without padding (RFC 7515, section 2), one way only:
// encoding/base64 alone skips line breaks, and Strict refuses the spare bits
// of the last character when they are not zero.
func decode(s string) ([]byte, bool) {
	if strings.ContainsAny(s, "\r\n") {
		return nil, false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return b, err == nil
}

// verify checks an ID token (OpenID Connect Core, section 3.1.3.7): that the
// provider signed it, for this application, for this sign-in, and not long
// ago. find gives the provider's key with an identifier.
//
// The signature is checked before the claims are read, so nothing is taken
// from a token the provider did not make. A key the token carries or points
// to (jwk, jku, x5c) is never used.
func verify(p Provider, issuer, clientID, nonce, token string, now time.Time, find func(kid string) (*signingKey, error)) (*Identity, error) {
	if len(token) > maxIDToken {
		return nil, refuse("ID token too long")
	}
	// Five parts would be an encrypted token, two an unsigned one cut short.
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, refuse("ID token is not three parts")
	}
	header, ok := decode(parts[0])
	if !ok {
		return nil, refuse("ID token header is not base64url")
	}
	payload, ok := decode(parts[1])
	if !ok {
		return nil, refuse("ID token claims are not base64url")
	}
	signature, ok := decode(parts[2])
	if !ok {
		return nil, refuse("ID token signature is not base64url")
	}
	kid, err := parseHeader(header)
	if err != nil {
		return nil, err
	}
	key, err := find(kid)
	if err != nil {
		return nil, err
	}
	if key.public == nil {
		return nil, refuse("ID token signed with a key that is not for RS256 signatures")
	}
	hash := sha256.Sum256([]byte(token[:len(parts[0])+1+len(parts[1])]))
	if rsa.VerifyPKCS1v15(key.public, crypto.SHA256, hash[:], signature) != nil {
		return nil, refuse("wrong signature")
	}
	return claims(p, issuer, key.issuer, clientID, nonce, payload, now)
}

// parseHeader checks how a token says it is signed, and returns which key with.
func parseHeader(raw []byte) (kid string, err error) {
	members, ok := object(raw)
	if !ok {
		return "", refuse("ID token header is not one JSON object")
	}
	// One algorithm, and it is not the token that chooses: "none" would be no
	// signature, and HS256 one made with the provider's public key as its secret.
	if alg, ok := text(members["alg"]); !ok || alg != "RS256" {
		return "", refuse("ID token signed with another algorithm")
	}
	// An access token of the same provider is a JWT with another type.
	if value, has := members["typ"]; has {
		if typ, ok := text(value); !ok || !strings.EqualFold(typ, "JWT") {
			return "", refuse("a token of another type")
		}
	}
	// What a header says must be understood is not: nothing is (RFC 7515, section 4.1.11).
	if _, has := members["crit"]; has {
		return "", refuse("ID token with a header that must be understood")
	}
	kid, ok = text(members["kid"])
	if !ok || !visible(kid, maxKeyID) {
		return "", refuse("ID token without a key identifier")
	}
	return kid, nil
}

// claims checks what a signed token says, and returns who it is about.
// keyIssuer is who the key that signed it is for, when its set says.
func claims(p Provider, issuer, keyIssuer, clientID, nonce string, payload []byte, now time.Time) (*Identity, error) {
	members, ok := object(payload)
	if !ok {
		return nil, refuse("ID token claims are not one JSON object")
	}

	// Microsoft has one issuer for each tenant, and signs for all of them with
	// the same keys: the issuer is the one of the tenant the token names.
	tenant := ""
	if p == Microsoft {
		if tenant, ok = text(members["tid"]); !ok || !guid(tenant) {
			return nil, refuse("ID token without a tenant")
		}
	}
	iss, ok := text(members["iss"])
	if !ok || iss == "" {
		return nil, refuse("ID token without an issuer")
	}
	want := strings.ReplaceAll(issuer, tenantPlace, tenant)
	// Google writes its issuer with and without "https://", and says so.
	if iss != want && (p != Google || "https://"+iss != want) {
		return nil, refuse("ID token of another issuer")
	}
	// Some of Microsoft's keys are for the personal accounts alone.
	if keyIssuer != "" && iss != strings.ReplaceAll(keyIssuer, tenantPlace, tenant) {
		return nil, refuse("ID token signed with a key of another issuer")
	}

	// Exactly this application: a token the provider made for another one,
	// which its owner holds, must not sign anybody in here. A list of
	// audiences is refused too; neither provider writes one.
	if aud, ok := text(members["aud"]); !ok || clientID == "" || aud != clientID {
		return nil, refuse("ID token for another application")
	}
	if value, has := members["azp"]; has {
		if azp, ok := text(value); !ok || azp != clientID {
			return nil, refuse("ID token given to another application")
		}
	}

	if exp, ok := whole(members["exp"]); !ok || !now.Before(time.Unix(exp, 0)) {
		return nil, refuse("ID token expired")
	}
	if iat, ok := whole(members["iat"]); !ok || time.Unix(iat, 0).After(now.Add(clockSkew)) {
		return nil, refuse("ID token made in the future")
	}
	if value, has := members["nbf"]; has {
		if nbf, ok := whole(value); !ok || time.Unix(nbf, 0).After(now.Add(clockSkew)) {
			return nil, refuse("ID token not valid yet")
		}
	}

	// A missing nonce must never match a token that has none either.
	got, ok := text(members["nonce"])
	if !ok || nonce == "" || subtle.ConstantTimeCompare([]byte(got), []byte(nonce)) != 1 {
		return nil, refuse("ID token of another sign-in")
	}

	sub, ok := text(members["sub"])
	if !ok || !visible(sub, maxSubject) {
		return nil, refuse("ID token without a subject")
	}
	email, ok := optional(members["email"])
	if !ok {
		return nil, refuse("ID token with an address that is not text")
	}
	given, ok := optional(members["name"])
	if !ok {
		return nil, refuse("ID token with a name that is not text")
	}
	id := &Identity{Provider: p, Subject: sub, Address: address(email), Name: name(given)}

	switch p {
	case Microsoft:
		// The subject is another for each application; the tenant and the
		// object are the account itself, whichever application asks.
		if value, has := members["oid"]; has {
			oid, ok := text(value)
			if !ok || !guid(oid) {
				return nil, refuse("ID token with an object that is not an identifier")
			}
			id.Subject = tenant + "." + oid
		}
		// The address of a work or school account is whatever its administrator
		// typed, unless the token says the owner of its domain was verified.
		id.Verified = id.Address != "" && (tenant == personalTenant || truth(members["xms_edov"]))
	default:
		id.Verified = id.Address != "" && truth(members["email_verified"])
	}
	return id, nil
}

// truth says whether a claim is true: the boolean, or the text "true" that
// Google sometimes writes. Anything else is not, which is the safe side: the
// address is then confirmed by mail.
func truth(value json.RawMessage) bool {
	return string(value) == "true" || string(value) == `"true"`
}

// guid says whether s is an identifier as Microsoft writes them: lower case,
// so one account has one way to be written.
func guid(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range len(s) {
		switch c := s[i]; {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case (c < '0' || c > '9') && (c < 'a' || c > 'f'):
			return false
		}
	}
	return true
}
