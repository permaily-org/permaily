package oauth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/jws"
)

var (
	signerOnce sync.Once
	signerRSA  *rsa.PrivateKey
	signerEC   *ecdsa.PrivateKey
)

// signers are a key of each algorithm, made once for the package's tests.
func signers(t *testing.T) (*rsa.PrivateKey, *ecdsa.PrivateKey, []jws.Key) {
	t.Helper()
	signerOnce.Do(func() {
		var err error
		if signerRSA, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
		if signerEC, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			panic(err)
		}
	})
	keys := []jws.Key{{ID: "rs", Algorithm: jws.RS256, Public: &signerRSA.PublicKey}, {ID: "es", Algorithm: jws.ES256, Public: &signerEC.PublicKey}}
	return signerRSA, signerEC, keys
}

// assertion signs claims with header; a nil claims value removes it.
func assertion(t *testing.T, header string, claims map[string]any) string {
	t.Helper()
	rk, ek, _ := signers(t)
	m := map[string]any{"iss": vectorClient, "sub": vectorClient, "aud": vectorAudiences[0], "jti": "j-1", "iat": vectorNow.Unix(), "exp": vectorNow.Add(5 * time.Minute).Unix()}
	for k, v := range claims {
		if v == nil {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	c, _ := json.Marshal(m)
	signed := b64([]byte(header)) + "." + b64(c)
	h := sha256.Sum256([]byte(signed))
	if strings.Contains(header, "ES256") {
		r, s, err := ecdsa.Sign(rand.Reader, ek, h[:])
		if err != nil {
			t.Fatal(err)
		}
		return signed + "." + b64(append(pad32(r), pad32(s)...))
	}
	sig, err := rsa.SignPKCS1v15(nil, rk, crypto.SHA256, h[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + b64(sig)
}

const rsHeader = `{"alg":"RS256","kid":"rs"}`

// V8. What an assertion says is checked once its signature is: who it is
// from, for whom, until when, and its jti; it is accepted until
// AssertionAcceptedUntil(exp) and not at that instant (finding 9).
func TestV8VerifyAssertionChecksItsClaims(t *testing.T) {
	_, _, keys := signers(t)
	doc := vectorDocument("")
	verify := func(a string, now time.Time) (string, time.Time, error) {
		return VerifyAssertion(doc, keys, a, vectorAudiences, now)
	}
	jti, exp, err := verify(assertion(t, rsHeader, nil), vectorNow)
	if err != nil || jti != "j-1" || !exp.Equal(vectorNow.Add(5*time.Minute)) {
		t.Fatal(jti, exp, err)
	}
	if _, _, err := verify(assertion(t, `{"alg":"ES256","kid":"es","typ":"JWT"}`, nil), vectorNow); err != nil {
		t.Fatal(err)
	}

	// The acceptance's edge: exp-1s, exp and exp+1s are accepted; exp plus
	// the skew is not, nor after.
	a := assertion(t, rsHeader, nil)
	for _, at := range []time.Duration{-time.Second, 0, time.Second, ClockSkew - time.Nanosecond} {
		if _, _, err := verify(a, exp.Add(at)); err != nil {
			t.Errorf("at exp%+v: %v", at, err)
		}
	}
	for _, at := range []time.Duration{ClockSkew, ClockSkew + time.Second, time.Hour} {
		if _, _, err := verify(a, exp.Add(at)); !errors.Is(err, errAssertionExpired) {
			t.Errorf("at exp%+v: %v", at, err)
		}
		if AssertionLive(exp, exp.Add(at)) {
			t.Errorf("live at exp%+v", at)
		}
	}

	for name, c := range map[string]struct {
		header string
		claims map[string]any
		want   error
	}{
		"iss another client":          {rsHeader, map[string]any{"iss": "https://chatgpt.com/oauth/other.json"}, errAssertionIssuer},
		"sub another client":          {rsHeader, map[string]any{"sub": "https://chatgpt.com/oauth/other.json"}, errAssertionIssuer},
		"no iss":                      {rsHeader, map[string]any{"iss": nil}, errAssertionIssuer},
		"no sub":                      {rsHeader, map[string]any{"sub": nil}, errAssertionIssuer},
		"iss not text":                {rsHeader, map[string]any{"iss": 1}, errAssertionIssuer},
		"aud another endpoint":        {rsHeader, map[string]any{"aud": "https://api.permaily.com/oauth/revoke"}, errAssertionAudience},
		"aud an array of others":      {rsHeader, map[string]any{"aud": []string{"https://evil.example.com", "https://api.permaily.com/"}}, errAssertionAudience},
		"aud an empty array":          {rsHeader, map[string]any{"aud": []string{}}, errAssertionAudience},
		"aud a number":                {rsHeader, map[string]any{"aud": 1}, errAssertionAudience},
		"aud an array with a number":  {rsHeader, map[string]any{"aud": []any{1, vectorAudiences[0]}}, errAssertionAudience},
		"no aud":                      {rsHeader, map[string]any{"aud": nil}, errAssertionAudience},
		"no exp":                      {rsHeader, map[string]any{"exp": nil}, errAssertionExpired},
		"exp past":                    {rsHeader, map[string]any{"exp": vectorNow.Add(-2 * time.Minute).Unix()}, errAssertionExpired},
		"exp a fraction":              {rsHeader, map[string]any{"exp": 1794830700.5}, errAssertionExpired},
		"exp text":                    {rsHeader, map[string]any{"exp": "1794830700"}, errAssertionExpired},
		"exp negative":                {rsHeader, map[string]any{"exp": -1}, errAssertionExpired},
		"exp beyond 10 minutes":       {rsHeader, map[string]any{"exp": vectorNow.Add(AssertionMaxLife + time.Second).Unix()}, errAssertionTooLong},
		"iat ahead":                   {rsHeader, map[string]any{"iat": vectorNow.Add(ClockSkew + time.Second).Unix()}, errAssertionIssuedAt},
		"iat text":                    {rsHeader, map[string]any{"iat": "now"}, errAssertionIssuedAt},
		"nbf ahead":                   {rsHeader, map[string]any{"nbf": vectorNow.Add(2 * time.Minute).Unix()}, errAssertionIssuedAt},
		"no jti":                      {rsHeader, map[string]any{"jti": nil}, errAssertionJTI},
		"an empty jti":                {rsHeader, map[string]any{"jti": ""}, errAssertionJTI},
		"a jti of 257 bytes":          {rsHeader, map[string]any{"jti": strings.Repeat("j", 257)}, errAssertionJTI},
		"a jti that is a number":      {rsHeader, map[string]any{"jti": 7}, errAssertionJTI},
		"alg none":                    {`{"alg":"none","kid":"rs"}`, nil, jws.ErrAlgorithm},
		"HS256":                       {`{"alg":"HS256","kid":"rs"}`, nil, jws.ErrAlgorithm},
		"an unknown kid":              {`{"alg":"RS256","kid":"other"}`, nil, jws.ErrUnknownKey},
		"duplicate header members":    {`{"alg":"RS256","kid":"rs","kid":"rs"}`, nil, jws.ErrHeader},
		"a key named for another alg": {`{"alg":"RS256","kid":"es"}`, nil, jws.ErrUnusableKey},
		"a jwk in the header":         {`{"alg":"RS256","kid":"other","jwk":{"kty":"RSA"}}`, nil, jws.ErrHeaderMember},
		"crit":                        {`{"alg":"RS256","kid":"rs","crit":["b64"]}`, nil, jws.ErrCritical},
	} {
		_, _, err := verify(assertion(t, c.header, c.claims), vectorNow)
		var e *Error
		if !errors.Is(err, c.want) || !errors.As(err, &e) || e.Code != ErrInvalidClient || e.Reason != ReasonAssertion {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	// A jti of 256 bytes, an array aud holding the issuer, and iat and nbf a
	// minute ahead are taken.
	if _, _, err := verify(assertion(t, rsHeader, map[string]any{"jti": strings.Repeat("j", 256), "aud": []string{"x", config.Issuer},
		"iat": vectorNow.Add(ClockSkew).Unix(), "nbf": vectorNow.Add(ClockSkew).Unix(), "exp": vectorNow.Add(AssertionMaxLife).Unix()}), vectorNow); err != nil {
		t.Fatal(err)
	}

	// Claims that are not one object, signed as they are.
	rk, _, _ := signers(t)
	for _, claims := range []string{`[1]`, `{"jti":"a","jti":"b"}`, `{"iss":"x"} {}`, "{\"iss\":\"\xff\"}"} {
		signed := b64([]byte(rsHeader)) + "." + b64([]byte(claims))
		h := sha256.Sum256([]byte(signed))
		sig, _ := rsa.SignPKCS1v15(nil, rk, crypto.SHA256, h[:])
		if _, _, err := verify(signed+"."+b64(sig), vectorNow); !errors.Is(err, errAssertionClaims) {
			t.Errorf("%q: %v", claims, err)
		}
	}

	// A public client is not one to check an assertion for; a document that
	// names an algorithm takes that one alone.
	if _, _, err := VerifyAssertion(claudeCode(), keys, assertion(t, rsHeader, nil), vectorAudiences, vectorNow); !errors.Is(err, errNotAssertionClient) {
		t.Fatal(err)
	}
	if _, _, err := VerifyAssertion(vectorDocument("ES256"), keys, assertion(t, rsHeader, nil), vectorAudiences, vectorNow); !errors.Is(err, errAssertionAlg) {
		t.Fatal(err)
	}
	// Nothing of the assertion is in what its refusal says.
	_, _, err = verify(assertion(t, rsHeader, map[string]any{"jti": "secret-jti", "iss": "https://evil.example.com/x"}), vectorNow)
	if err == nil || strings.Contains(err.Error(), "secret-jti") || strings.Contains(err.Error(), "evil") {
		t.Fatal(err)
	}
}
