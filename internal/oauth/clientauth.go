package oauth

import (
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"github.com/permaily-org/permaily/internal/jws"
)

// JTIMaxBytes bounds a client assertion's jti.
const JTIMaxBytes = 256

// VerifyAssertion checks a client assertion (RFC 7523, section 3; ADR 0030,
// "private_key_jwt") for a client whose document declares private_key_jwt,
// against keys (its jwks_uri's), for audiences (the endpoint's URL and the
// issuer), at now: a compact JWS signed RS256 or ES256 (the document's
// token_endpoint_auth_signing_alg when it names one) with a kid of keys;
// claims that are one JSON object with every member once; iss and sub the
// client_id; aud one of audiences, or an array holding one; exp a whole
// number of seconds with AssertionLive(exp, now) and no more than
// AssertionMaxLife ahead; iat and nbf, when present, no more than ClockSkew
// ahead; a jti of 1 to JTIMaxBytes bytes.
//
// It returns the jti and exp for the caller to decide its use by its record
// (AssertionRecordName, AssertionRecord.Accept: a replay is decided there,
// not here). Every refusal is invalid_client (ReasonAssertion); one whose
// cause is jws.ErrUnknownKey may be tried once more with the key set
// fetched again.
func VerifyAssertion(doc *ClientDocument, keys []jws.Key, assertion string, audiences []string, now time.Time) (jti string, exp time.Time, err error) {
	if doc.TokenEndpointAuthMethod != AuthPrivateKeyJWT {
		return "", time.Time{}, refuseAssertion(errNotAssertionClient)
	}
	t, err := jws.Parse(assertion)
	if err != nil {
		return "", time.Time{}, refuseAssertion(err)
	}
	if doc.TokenEndpointAuthAlg != "" && t.Algorithm != doc.TokenEndpointAuthAlg {
		return "", time.Time{}, refuseAssertion(errAssertionAlg)
	}
	if err := jws.Verify(t, keys); err != nil {
		return "", time.Time{}, refuseAssertion(err)
	}
	claims, ok := jws.Object(t.Claims)
	if !ok {
		return "", time.Time{}, refuseAssertion(errAssertionClaims)
	}
	iss, okIss := jws.Text(claims["iss"])
	sub, okSub := jws.Text(claims["sub"])
	if !okIss || !okSub || iss != doc.ClientID || sub != doc.ClientID {
		return "", time.Time{}, refuseAssertion(errAssertionIssuer)
	}
	if !audience(claims["aud"], audiences) {
		return "", time.Time{}, refuseAssertion(errAssertionAudience)
	}
	seconds, ok := whole(claims["exp"])
	exp = time.Unix(seconds, 0)
	if !ok || !AssertionLive(exp, now) {
		return "", time.Time{}, refuseAssertion(errAssertionExpired)
	}
	if exp.After(now.Add(AssertionMaxLife)) {
		return "", time.Time{}, refuseAssertion(errAssertionTooLong)
	}
	for _, name := range []string{"iat", "nbf"} {
		if value, has := claims[name]; has {
			if at, ok := whole(value); !ok || time.Unix(at, 0).After(now.Add(ClockSkew)) {
				return "", time.Time{}, refuseAssertion(errAssertionIssuedAt)
			}
		}
	}
	jti, ok = jws.Text(claims["jti"])
	if !ok || jti == "" || len(jti) > JTIMaxBytes {
		return "", time.Time{}, refuseAssertion(errAssertionJTI)
	}
	return jti, exp, nil
}

// audience says aud is one of audiences, or an array of strings holding one.
func audience(aud json.RawMessage, audiences []string) bool {
	if s, ok := jws.Text(aud); ok {
		return slices.Contains(audiences, s)
	}
	list, ok := texts(aud)
	return ok && slices.ContainsFunc(list, func(s string) bool { return slices.Contains(audiences, s) })
}

// whole is a member that is a whole number of seconds written plainly: no
// fraction, no exponent, not negative, and short enough to be exact (the
// claims are JSON already, which writes no sign and no zero in front).
func whole(value json.RawMessage) (int64, bool) {
	if len(value) == 0 || len(value) > 15 {
		return 0, false
	}
	n, err := strconv.ParseInt(string(value), 10, 64)
	return n, err == nil && n >= 0
}
