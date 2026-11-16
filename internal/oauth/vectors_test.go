package oauth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/jws"
)

// The client assertions of V8 (ADR 0030, "Tests"), kept in
// testdata/assertions: a key set (keys.json, public keys only) and
// assertions signed with its keys (vectors.json), valid and broken, each
// with what VerifyAssertion answers. tests/oauth-differential gives the same
// vectors to go-jose. An assertion is kept as its header and claims as text
// and its signature, never as a compact token: a token-shaped string in the
// repository is what the secret scan looks for, and these are made up.
//
// go test ./internal/oauth -run TestTheAssertionVectors -update writes new
// ones with new keys, whose private halves are never written.
var update = flag.Bool("update", false, "write testdata/assertions again with new keys")

// The vectors' fixed world.
var (
	vectorNow       = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	vectorClient    = "https://chatgpt.com/oauth/client.json"
	vectorAudiences = []string{"https://api.permaily.com/oauth/token", "https://api.permaily.com"}
)

// Vector is one assertion of testdata/assertions/vectors.json.
type Vector struct {
	Name string `json:"name"`
	// DocumentAlg is the document's token_endpoint_auth_signing_alg.
	DocumentAlg string `json:"document_alg"`
	Header      string `json:"header"`
	Claims      string `json:"claims"`
	Signature   string `json:"signature"`
	// Want is the error VerifyAssertion's refusal unwraps to, or "".
	Want string `json:"want"`
}

// Compact is the vector as sent.
func (v Vector) Compact() string {
	return b64([]byte(v.Header)) + "." + b64([]byte(v.Claims)) + "." + v.Signature
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func readVectors(t testing.TB) ([]jws.Key, []Vector) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "assertions", "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := jws.Keys(body)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("testdata", "assertions", "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors []Vector
	if err := json.Unmarshal(b, &vectors); err != nil {
		t.Fatal(err)
	}
	return keys, vectors
}

func vectorDocument(alg string) *ClientDocument {
	return &ClientDocument{ClientID: vectorClient, TokenEndpointAuthMethod: AuthPrivateKeyJWT, TokenEndpointAuthAlg: alg, JWKSURI: "https://chatgpt.com/oauth/jwks.json"}
}

// V8: the vectors answer what they say.
func TestV8TheAssertionVectors(t *testing.T) {
	if *update {
		writeVectors(t)
	}
	keys, vectors := readVectors(t)
	if len(vectors) < 10 {
		t.Fatalf("%d vectors", len(vectors))
	}
	accepted := 0
	for _, v := range vectors {
		jti, exp, err := VerifyAssertion(vectorDocument(v.DocumentAlg), keys, v.Compact(), vectorAudiences, vectorNow)
		switch {
		case v.Want == "" && err != nil:
			t.Errorf("%s: refused: %v", v.Name, err)
		case v.Want == "" && (jti == "" || exp.IsZero()):
			t.Errorf("%s: accepted without its jti or exp", v.Name)
		case v.Want == "":
			accepted++
		case err == nil:
			t.Errorf("%s: accepted, want %s", v.Name, v.Want)
		case errors.Unwrap(err) == nil || errors.Unwrap(err).Error() != v.Want:
			t.Errorf("%s: %v, want %s", v.Name, err, v.Want)
		}
	}
	if accepted != 2 {
		t.Fatalf("%d accepted, want the RS256 and ES256 ones", accepted)
	}
}

// writeVectors makes new keys and the vectors signed with them.
func writeVectors(t *testing.T) {
	t.Helper()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	shortKey, err := rsa.GenerateKey(rand.Reader, 1024) //nolint:gosec // G403: the short key a vector must refuse
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rsaJWK := func(kid string, k *rsa.PublicKey) map[string]string {
		return map[string]string{"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256", "n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes())}
	}
	point, err := ecKey.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	ecJWK := map[string]string{"kty": "EC", "kid": "es-2026", "use": "sig", "alg": "ES256", "crv": "P-256", "x": b64(point[1:33]), "y": b64(point[33:])}
	set, _ := json.Marshal(map[string]any{"keys": []any{rsaJWK("rs-2026", &rsaKey.PublicKey), ecJWK, rsaJWK("rs-short", &shortKey.PublicKey)}})

	claims := func(jti string) string {
		c, _ := json.Marshal(map[string]any{"iss": vectorClient, "sub": vectorClient, "aud": vectorAudiences[0], "jti": jti, "iat": vectorNow.Unix(), "exp": vectorNow.Add(5 * time.Minute).Unix()})
		return string(c)
	}
	signRSA := func(k *rsa.PrivateKey) func(string) string {
		return func(signed string) string {
			h := sha256.Sum256([]byte(signed))
			s, err := rsa.SignPKCS1v15(nil, k, crypto.SHA256, h[:])
			if err != nil {
				t.Fatal(err)
			}
			return b64(s)
		}
	}
	signEC := func(der bool) func(string) string {
		return func(signed string) string {
			h := sha256.Sum256([]byte(signed))
			if der {
				s, err := ecdsa.SignASN1(rand.Reader, ecKey, h[:])
				if err != nil {
					t.Fatal(err)
				}
				return b64(s)
			}
			r, s, err := ecdsa.Sign(rand.Reader, ecKey, h[:])
			if err != nil {
				t.Fatal(err)
			}
			return b64(append(pad32(r), pad32(s)...))
		}
	}
	hs := func(signed string) string {
		der, _ := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
		m := hmac.New(sha256.New, der)
		m.Write([]byte(signed))
		return b64(m.Sum(nil))
	}
	none := func(string) string { return "" }
	flip := func(sign func(string) string) func(string) string {
		return func(signed string) string {
			s, _ := base64.RawURLEncoding.DecodeString(sign(signed))
			s[len(s)-1] ^= 1
			return b64(s)
		}
	}
	type spec struct {
		name, alg, header string
		sign              func(string) string
		want              error
	}
	specs := []spec{
		{"rs256", "RS256", `{"alg":"RS256","kid":"rs-2026","typ":"JWT"}`, signRSA(rsaKey), nil},
		{"es256", "", `{"alg":"ES256","kid":"es-2026"}`, signEC(false), nil},
		{"es256 under a document that names RS256", "RS256", `{"alg":"ES256","kid":"es-2026"}`, signEC(false), errAssertionAlg},
		{"rsa key of 1024 bits", "", `{"alg":"RS256","kid":"rs-short"}`, signRSA(shortKey), jws.ErrUnusableKey},
		{"alg none", "", `{"alg":"none","kid":"rs-2026"}`, none, jws.ErrAlgorithm},
		{"hs256 with the public key as its secret", "", `{"alg":"HS256","kid":"rs-2026"}`, hs, jws.ErrAlgorithm},
		{"unknown kid", "", `{"alg":"RS256","kid":"rs-2025"}`, signRSA(rsaKey), jws.ErrUnknownKey},
		{"bad signature", "", `{"alg":"RS256","kid":"rs-2026"}`, flip(signRSA(rsaKey)), jws.ErrSignature},
		{"duplicate header member", "", `{"alg":"RS256","kid":"rs-2026","alg":"RS256"}`, signRSA(rsaKey), jws.ErrHeader},
		{"ec key named by an rs256 header", "", `{"alg":"RS256","kid":"es-2026"}`, signRSA(rsaKey), jws.ErrUnusableKey},
		{"es256 signature in asn.1", "", `{"alg":"ES256","kid":"es-2026"}`, signEC(true), jws.ErrSignature},
		{"no kid", "", `{"alg":"RS256"}`, signRSA(rsaKey), jws.ErrKeyID},
		{"crit", "", `{"alg":"RS256","kid":"rs-2026","crit":["exp"],"exp":1}`, signRSA(rsaKey), jws.ErrCritical},
	}
	var vectors []Vector
	for i, s := range specs {
		c := claims("vector-" + string(rune('a'+i)))
		v := Vector{Name: s.name, DocumentAlg: s.alg, Header: s.header, Claims: c}
		v.Signature = s.sign(b64([]byte(s.header)) + "." + b64([]byte(c)))
		if s.want != nil {
			v.Want = s.want.Error()
		}
		vectors = append(vectors, v)
	}
	out, _ := json.MarshalIndent(vectors, "", "  ")
	dir := filepath.Join("testdata", "assertions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keys.json"), append(set, '\n'), 0o644); err != nil { //nolint:gosec // G306: test data
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vectors.json"), append(out, '\n'), 0o644); err != nil { //nolint:gosec // G306: test data
		t.Fatal(err)
	}
}

func pad32(n *big.Int) []byte {
	b := make([]byte, 32)
	return n.FillBytes(b)
}
