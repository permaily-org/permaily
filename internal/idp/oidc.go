package idp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/jws"
)

// Limits of an ID token and of the keys it is checked with. Each is well
// above what Google and Microsoft send: a token of one or two thousand bytes,
// a set of two to ten keys of 2048 bits (Microsoft's, with certificates, is
// thirteen thousand bytes).
const (
	maxIDToken = 16 << 10
	maxKeySet  = 256 << 10
	maxKeys    = jws.MaxKeys
	maxSubject = 255 // OpenID Connect Core, section 2

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
	// public is the key, with no Public for a key that is not one for RS256
	// signatures: it is remembered so a token that names it is refused for
	// that.
	public jws.Key
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

// parseKeys reads a JSON Web Key Set (RFC 7517) with internal/jws. A key of
// another kind is kept as one that cannot be used, since a provider may
// publish some; two keys with one identifier are refused, since which one
// signs would be a matter of their order.
func parseKeys(body []byte) (map[string]*signingKey, error) {
	set, err := jws.Keys(body)
	if err != nil {
		return nil, refuse(said(err))
	}
	keys := map[string]*signingKey{}
	for _, k := range set {
		key := &signingKey{}
		// Usable only as what the token's header must say too: RS256.
		if k.Algorithm == jws.RS256 {
			key.public, key.issuer = k, k.Issuer
		}
		keys[k.ID] = key
	}
	return keys, nil
}

// words is what internal/jws's refusals are called here: the words this
// package has always said, which its tests and logs keep. Every refusal
// jws returns is one of these (TestEveryRefusalOfJWSHasItsWords).
var words = map[error]string{
	jws.ErrTooLong:           "ID token too long",
	jws.ErrNotThreeParts:     "ID token is not three parts",
	jws.ErrHeaderEncoding:    "ID token header is not base64url",
	jws.ErrClaimsEncoding:    "ID token claims are not base64url",
	jws.ErrSignatureEncoding: "ID token signature is not base64url",
	jws.ErrHeader:            "ID token header is not one JSON object",
	jws.ErrAlgorithm:         "ID token signed with another algorithm",
	jws.ErrType:              "a token of another type",
	jws.ErrCritical:          "ID token with a header that must be understood",
	jws.ErrKeyID:             "ID token without a key identifier",
	jws.ErrHeaderMember:      "ID token with a header member it cannot have",
	jws.ErrUnknownKey:        "ID token signed with a key the provider does not publish",
	jws.ErrUnusableKey:       "ID token signed with a key that is not for RS256 signatures",
	jws.ErrSignature:         "wrong signature",
	jws.ErrKeySet:            "keys are not one JSON object",
	jws.ErrKeyList:           "keys without a list, or with one too long",
	jws.ErrKeyNotObject:      "a key is not one JSON object",
	jws.ErrKeyIDTwice:        "two keys with one identifier",
}

// said is the words for one of internal/jws's refusals, which it returns
// as they are.
func said(err error) string { return words[err] }

// idToken is how an ID token's header is read, in the order this package
// always checked it: one algorithm, RS256, and it is not the token that
// chooses; a type, when there is one, of JWT, since an access token of the
// same provider is a JWT with another type; then no crit and a kid.
var idToken = jws.Profile{
	Algorithms: []string{jws.RS256},
	Type:       func(typ string) bool { return strings.EqualFold(typ, "JWT") },
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
	t, err := idToken.Parse(token)
	if err != nil {
		return nil, refuse(said(err))
	}
	key, err := find(t.KeyID)
	if err != nil {
		return nil, err
	}
	if key.public.Public == nil {
		return nil, refuse("ID token signed with a key that is not for RS256 signatures")
	}
	if err := jws.Verify(t, []jws.Key{key.public}); err != nil {
		return nil, refuse(said(err))
	}
	return claims(p, issuer, key.issuer, clientID, nonce, t.Claims, now)
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
