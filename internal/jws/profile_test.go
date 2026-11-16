package jws_test

import (
	"crypto/elliptic"
	"encoding/json"
	"testing"

	"github.com/permaily-org/permaily/internal/jws"
)

// A signed header is refused when it says the claims are not base64url
// (RFC 7797, which a JWT must not use), carries or points to a key (which is
// never used), or has a member of the wrong kind that go-jose refuses: each
// verifies over what was sent, and each was accepted before pull request
// 133's review (finding 1).
func TestParseRefusesASignedHeaderOutsideTheProfile(t *testing.T) {
	rk, ek := generate(t, 2048), generateEC(t, elliptic.P256())
	keys := []jws.Key{{ID: "rs", Algorithm: jws.RS256, Public: &rk.PublicKey}, {ID: "es", Algorithm: jws.ES256, Public: &ek.PublicKey}}
	jwk, _ := json.Marshal(ecJWK(t, "x", &generateEC(t, elliptic.P256()).PublicKey))
	verify := func(token string) error {
		tok, err := jws.Parse(token)
		if err != nil {
			return err
		}
		return jws.Verify(tok, keys)
	}
	for _, member := range []string{
		`"b64":false`, `"b64":"false"`, `"b64":null`, `"b64":0`, `"b64":[]`,
		`"jwk":{"kty":"RSA"}`, `"jwk":` + string(jwk), `"jwk":null`,
		`"jku":"https://evil.example.com/keys"`, `"x5u":"https://evil.example.com/cert"`, `"x5c":["AAAA"]`, `"x5c":[]`,
		`"nonce":1`, `"nonce":null`,
	} {
		if verify(signRS(t, rk, `{"alg":"RS256","kid":"rs",`+member+`}`, `{}`)) == nil {
			t.Errorf("RS256 with %s: accepted", member)
		}
		if verify(signES(t, ek, `{"alg":"ES256","kid":"es",`+member+`}`, `{}`)) == nil {
			t.Errorf("ES256 with %s: accepted", member)
		}
	}
	// b64 true is what a header without it means; a thumbprint (Microsoft's
	// x5t) and a nonce that is text are kept, unused.
	for _, member := range []string{`"b64":true`, `"x5t":"abc"`, `"x5t#S256":"abc"`, `"nonce":"n"`, `"cty":"x"`} {
		if err := verify(signRS(t, rk, `{"alg":"RS256","kid":"rs",`+member+`}`, `{}`)); err != nil {
			t.Errorf("%s: %v", member, err)
		}
	}
}
