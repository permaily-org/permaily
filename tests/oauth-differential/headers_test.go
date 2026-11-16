package differential

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/permaily-org/permaily/internal/oauth"
)

// headerDiffers names each signed header internal/jws refuses and go-jose
// takes, with why.
var headerDiffers = map[string]string{
	`"crit":[]`:                  "crit is refused whatever it lists: nothing here is understood (RFC 7515, section 4.1.11)",
	`"b64":"false"`:              "b64, when present, must be true: go-jose reads a value that is not a boolean as true",
	`"b64":null`:                 "b64, when present, must be true: go-jose reads null as absent",
	`"jwk":<a valid public key>`: "a key the token carries is never used, so a header with one is refused",
	`"jku":"http://127.0.0.1/"`:  "a key the token points to is never used, so a header with one is refused",
	`"x5u":"http://127.0.0.1/"`:  "a key the token points to is never used, so a header with one is refused",
	`"nonce":null`:               "nonce, when present, must be text: go-jose reads null as absent",
}

// V12, internal/jws and internal/oauth's VerifyAssertion against go-jose on
// assertions whose header members are signed, so the signature always
// verifies over what was sent: jws never accepts what go-jose refuses, and
// where it refuses what go-jose accepts the case says why (pull request
// 133's review, finding 1).
func TestV12SignedHeadersAgainstGoJose(t *testing.T) {
	w := newWorld(t)
	claims, err := json.Marshal(map[string]any{"iss": privateClient, "sub": privateClient, "aud": w.server.Config.Issuer, "exp": w.clock.now.Add(time.Minute).Unix(), "jti": "header"})
	if err != nil {
		t.Fatal(err)
	}
	ref := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &w.signer.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}}
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := other.PublicKey.Bytes()
	enc := base64.RawURLEncoding.EncodeToString
	members := map[string]string{
		``:                           ``,
		`"b64":true`:                 `,"b64":true`,
		`"b64":false`:                `,"b64":false`,
		`"b64":"false"`:              `,"b64":"false"`,
		`"b64":null`:                 `,"b64":null`,
		`"typ":"JWT"`:                `,"typ":"JWT"`,
		`"typ":"at+jwt"`:             `,"typ":"at+jwt"`,
		`"crit":[]`:                  `,"crit":[]`,
		`"crit":["b64"]`:             `,"crit":["b64"],"b64":false`,
		`"kid" twice`:                `,"kid":"k1"`,
		`"jwk":{"kty":"RSA"}`:        `,"jwk":{"kty":"RSA"}`,
		`"jwk":<a valid public key>`: `,"jwk":{"kty":"EC","crv":"P-256","x":"` + enc(p[1:33]) + `","y":"` + enc(p[33:]) + `"}`,
		`"jku":"http://127.0.0.1/"`:  `,"jku":"http://127.0.0.1/"`,
		`"x5u":"http://127.0.0.1/"`:  `,"x5u":"http://127.0.0.1/"`,
		`"x5c":["AAAA"]`:             `,"x5c":["AAAA"]`,
		`"x5t":"abc"`:                `,"x5t":"abc"`,
		`"nonce":1`:                  `,"nonce":1`,
		`"nonce":null`:               `,"nonce":null`,
		`"nonce":"n"`:                `,"nonce":"n"`,
	}
	differed := map[string]bool{}
	for name, extra := range members {
		header := `{"alg":"RS256","kid":"k1"` + extra + `}`
		signed := enc([]byte(header)) + "." + enc(claims)
		h := sha256.Sum256([]byte(signed))
		sig, err := rsa.SignPKCS1v15(nil, w.signer, crypto.SHA256, h[:])
		if err != nil {
			t.Fatal(err)
		}
		token := signed + "." + enc(sig)
		ours := verifyOurs(token, w.server.Keys[privateClient])
		theirs := verifyTheirs(token, ref)
		_, _, assertion := oauth.VerifyAssertion(w.server.Documents[privateClient], w.server.Keys[privateClient], token, []string{w.server.Config.Issuer}, w.clock.now)
		if (ours == nil) != (assertion == nil) {
			t.Errorf("%s: jws %v, VerifyAssertion %v", name, ours, assertion)
		}
		switch {
		case name == `` && (ours != nil || theirs != nil):
			t.Fatalf("the header without more: jws %v, go-jose %v", ours, theirs)
		case ours == nil && theirs != nil:
			t.Errorf("%s: accepted here, refused by go-jose (%v)", name, theirs)
		case ours != nil && theirs == nil:
			if headerDiffers[name] == "" {
				t.Errorf("%s: refused here (%v), accepted by go-jose, with no reason given", name, ours)
			}
			differed[name] = true
		}
	}
	for name := range headerDiffers {
		if !differed[name] {
			t.Errorf("%s: named as a difference, but the two agree", name)
		}
	}
}
