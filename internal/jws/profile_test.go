package jws_test

import (
	"crypto/elliptic"
	"encoding/json"
	"errors"
	"testing"

	"github.com/permaily-org/permaily/internal/jws"
)

// A signed header is refused when it says the claims are not base64url
// (RFC 7797, which a JWT must not use), carries a key that is not a
// well-formed public key or a certificate chain, or has a nonce that is not
// text: each verifies over what was sent, and each was accepted before pull
// request 133's review (finding 1). A well-formed carried key, and a key the
// header points to, are taken and never used.
func TestParseRefusesASignedHeaderOutsideTheProfile(t *testing.T) {
	rk, ek := generate(t, 2048), generateEC(t, elliptic.P256())
	keys := []jws.Key{{ID: "rs", Algorithm: jws.RS256, Public: &rk.PublicKey}, {ID: "es", Algorithm: jws.ES256, Public: &ek.PublicKey}}
	carriedEC := ecJWK(t, "x", &generateEC(t, elliptic.P256()).PublicKey)
	carriedRSA := rsaJWK("y", &generate(t, 2048).PublicKey)
	jwk := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return `"jwk":` + string(b)
	}
	verify := func(token string) error {
		tok, err := jws.Parse(token)
		if err != nil {
			return err
		}
		return jws.Verify(tok, keys)
	}
	for _, member := range []string{
		`"b64":false`, `"b64":"false"`, `"b64":null`, `"b64":0`, `"b64":[]`,
		`"jwk":{"kty":"RSA"}`, `"jwk":null`, `"jwk":[]`, `"jwk":{"kty":"oct","k":"AA"}`, `"jwk":{}`,
		jwk(with(carriedEC, map[string]any{"d": "AA"})),
		jwk(with(carriedEC, map[string]any{"x5t": "abc"})),
		jwk(with(carriedEC, map[string]any{"crv": "P-384"})),
		jwk(with(carriedEC, map[string]any{"n": "AQAB"})),
		jwk(with(carriedEC, map[string]any{"kid": 1})),
		jwk(with(carriedEC, map[string]any{"Kid": "x"})),
		jwk(with(carriedRSA, map[string]any{"e": "AA"})),
		jwk(with(carriedRSA, map[string]any{"use": nil, "alg": []string{"RS256"}})),
		`"x5c":["AAAA"]`, `"x5c":[]`,
		`"nonce":1`, `"nonce":null`,
	} {
		for _, token := range []string{
			signRS(t, rk, `{"alg":"RS256","kid":"rs",`+member+`}`, `{}`),
			signES(t, ek, `{"alg":"ES256","kid":"es",`+member+`}`, `{}`),
		} {
			if err := verify(token); !errors.Is(err, jws.ErrHeaderMember) {
				t.Errorf("%s: %v", member, err)
			}
		}
	}
	// b64 true is what a header without it means; a carried key that is
	// well formed, a key a header points to, a thumbprint (Microsoft's x5t)
	// and a nonce that is text are taken, unused.
	for _, member := range []string{
		`"b64":true`, ` "b64" : true `, `"x5t":"abc"`, `"x5t#S256":"abc"`, `"nonce":"n"`, `"cty":"x"`,
		`"jku":"https://evil.example.com/keys"`, `"x5u":"https://evil.example.com/cert"`,
		jwk(carriedEC), jwk(carriedRSA), jwk(with(carriedRSA, map[string]any{"use": "sig", "alg": "RS256"})),
	} {
		if err := verify(signRS(t, rk, `{"alg":"RS256","kid":"rs",`+member+`}`, `{}`)); err != nil {
			t.Errorf("%s: %v", member, err)
		}
	}
}

// A profile takes only its algorithms and types, and says the first thing
// wrong in its order: the algorithm, the type, crit, the kid.
func TestAProfileChecksInItsOrder(t *testing.T) {
	p := jws.Profile{Algorithms: []string{jws.RS256}, Type: func(typ string) bool { return typ == "JWT" }}
	for header, want := range map[string]error{
		`{"alg":"ES256","kid":"k"}`:                jws.ErrAlgorithm,
		`{"alg":"ES256","typ":"at+jwt","crit":[]}`: jws.ErrAlgorithm,
		`{"alg":"RS256","typ":"at+jwt","crit":[]}`: jws.ErrType,
		`{"alg":"RS256","typ":"at+jwt"}`:           jws.ErrType,
		`{"alg":"RS256","typ":"JWT","crit":[]}`:    jws.ErrCritical,
		`{"alg":"RS256","typ":"JWT","b64":false}`:  jws.ErrKeyID,
		`{"alg":"RS256","kid":"k","b64":false}`:    jws.ErrHeaderMember,
		`{"alg":"RS256","kid":"k","typ":"JWT"}`:    nil,
	} {
		_, err := p.Parse(b64([]byte(header)) + "." + b64([]byte(`{}`)) + ".AA")
		if !errors.Is(err, want) || (want == nil) != (err == nil) {
			t.Errorf("%s: %v, want %v", header, err, want)
		}
	}
	if _, err := (jws.Profile{Algorithms: []string{"PS256"}}).Parse(b64([]byte(`{"alg":"PS256","kid":"k"}`)) + ".e30.AA"); !errors.Is(err, jws.ErrAlgorithm) {
		t.Errorf("PS256: %v", err)
	}
}
