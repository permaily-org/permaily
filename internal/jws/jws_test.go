package jws_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"slices"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/jws"
)

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func generate(t *testing.T, bits int) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func generateEC(t *testing.T, c elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func signRS(t *testing.T, k *rsa.PrivateKey, header, claims string) string {
	t.Helper()
	signed := b64([]byte(header)) + "." + b64([]byte(claims))
	h := sha256.Sum256([]byte(signed))
	s, err := rsa.SignPKCS1v15(nil, k, crypto.SHA256, h[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + b64(s)
}

func signES(t *testing.T, k *ecdsa.PrivateKey, header, claims string) string {
	t.Helper()
	signed := b64([]byte(header)) + "." + b64([]byte(claims))
	h := sha256.Sum256([]byte(signed))
	r, s, err := ecdsa.Sign(rand.Reader, k, h[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signed + "." + b64(sig)
}

func rsaJWK(kid string, k *rsa.PublicKey) map[string]any {
	return map[string]any{"kty": "RSA", "kid": kid, "n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes())}
}

func ecJWK(t *testing.T, kid string, k *ecdsa.PublicKey) map[string]any {
	t.Helper()
	p, err := k.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"kty": "EC", "kid": kid, "crv": "P-256", "x": b64(p[1:33]), "y": b64(p[33:])}
}

func set(keys ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"keys": keys})
	return b
}

func with(m map[string]any, changes map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	for k, v := range changes {
		if v == nil {
			delete(out, k)
			continue
		}
		out[k] = v
	}
	return out
}

// A token signed RS256 or ES256 with a key of the set verifies, and its
// claims are what was signed.
func TestParseAndVerify(t *testing.T) {
	rk, ek := generate(t, 2048), generateEC(t, elliptic.P256())
	keys, err := jws.Keys(set(rsaJWK("rs", &rk.PublicKey), ecJWK(t, "es", &ek.PublicKey)))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ header, token string }{
		{"RS256", signRS(t, rk, `{"alg":"RS256","kid":"rs","typ":"JWT"}`, `{"a":1}`)},
		{"ES256", signES(t, ek, `{"alg":"ES256","kid":"es"}`, `{"a":1}`)},
	} {
		tok, err := jws.Parse(c.token)
		if err != nil {
			t.Fatal(err)
		}
		if tok.Algorithm != c.header || string(tok.Claims) != `{"a":1}` {
			t.Fatalf("%+v", tok)
		}
		if err := jws.Verify(tok, keys); err != nil {
			t.Fatalf("%s: %v", c.header, err)
		}
	}
	tok, _ := jws.Parse(signRS(t, rk, `{"alg":"RS256","kid":"rs","typ":"JWT"}`, `{}`))
	if !tok.Typed || tok.Type != "JWT" || tok.KeyID != "rs" {
		t.Fatalf("%+v", tok)
	}
}

// Every refusal of Parse, Verify and Keys is its own error, in Refusals.
func TestEveryRefusal(t *testing.T) {
	rk, ek := generate(t, 2048), generateEC(t, elliptic.P256())
	keys := []jws.Key{
		{ID: "rs", Algorithm: jws.RS256, Public: &rk.PublicKey},
		{ID: "es", Algorithm: jws.ES256, Public: &ek.PublicKey},
		{ID: "short", Algorithm: jws.RS256, Public: &generate(t, 1024).PublicKey},
		{ID: "nil", Algorithm: jws.RS256, Public: &rsa.PublicKey{}},
		{ID: "wrong-type", Algorithm: jws.RS256, Public: &ek.PublicKey},
		{ID: "p384", Algorithm: jws.ES256, Public: &generateEC(t, elliptic.P384()).PublicKey},
		{ID: "ec-as-rsa", Algorithm: jws.ES256, Public: &rk.PublicKey},
		{ID: "unusable"},
	}
	good := signRS(t, rk, `{"alg":"RS256","kid":"rs"}`, `{}`)
	parts := strings.Split(good, ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sig[10] ^= 1
	flipped := b64(sig)
	esGood := signES(t, ek, `{"alg":"ES256","kid":"es"}`, `{}`)
	esParts := strings.Split(esGood, ".")
	seen := map[error]bool{}
	for name, c := range map[string]struct {
		token string
		want  error
	}{
		"too long":                {strings.Repeat("a", jws.MaxCompactBytes+1), jws.ErrTooLong},
		"two parts":               {parts[0] + "." + parts[1], jws.ErrNotThreeParts},
		"five parts":              {good + ".a.b", jws.ErrNotThreeParts},
		"header not base64url":    {"*." + parts[1] + "." + parts[2], jws.ErrHeaderEncoding},
		"header padded":           {parts[0] + "=." + parts[1] + "." + parts[2], jws.ErrHeaderEncoding},
		"header with a newline":   {parts[0][:4] + "\n" + parts[0][4:] + "." + parts[1] + "." + parts[2], jws.ErrHeaderEncoding},
		"claims not base64url":    {parts[0] + ".+/." + parts[2], jws.ErrClaimsEncoding},
		"signature not base64url": {parts[0] + "." + parts[1] + ".%", jws.ErrSignatureEncoding},
		"header not an object":    {b64([]byte(`[1]`)) + "." + parts[1] + "." + parts[2], jws.ErrHeader},
		"header a member twice":   {b64([]byte(`{"alg":"RS256","alg":"RS256","kid":"rs"}`)) + "." + parts[1] + "." + parts[2], jws.ErrHeader},
		"header not UTF-8":        {b64([]byte("{\"alg\":\"RS256\",\"kid\":\"\xff\"}")) + "." + parts[1] + "." + parts[2], jws.ErrHeader},
		"header then more":        {b64([]byte(`{"alg":"RS256","kid":"rs"}{}`)) + "." + parts[1] + "." + parts[2], jws.ErrHeader},
		"header cut short":        {b64([]byte(`{"alg":"RS256","kid":"rs",`)) + "." + parts[1] + "." + parts[2], jws.ErrHeader},
		"header a bad value":      {b64([]byte(`{"alg":"RS256","kid":tru}`)) + "." + parts[1] + "." + parts[2], jws.ErrHeader},
		"alg none":                {b64([]byte(`{"alg":"none","kid":"rs"}`)) + "." + parts[1] + ".", jws.ErrAlgorithm},
		"alg HS256":               {b64([]byte(`{"alg":"HS256","kid":"rs"}`)) + "." + parts[1] + "." + parts[2], jws.ErrAlgorithm},
		"alg rs256":               {b64([]byte(`{"alg":"rs256","kid":"rs"}`)) + "." + parts[1] + "." + parts[2], jws.ErrAlgorithm},
		"alg PS256":               {b64([]byte(`{"alg":"PS256","kid":"rs"}`)) + "." + parts[1] + "." + parts[2], jws.ErrAlgorithm},
		"no alg":                  {b64([]byte(`{"kid":"rs"}`)) + "." + parts[1] + "." + parts[2], jws.ErrAlgorithm},
		"alg not text":            {b64([]byte(`{"alg":["RS256"],"kid":"rs"}`)) + "." + parts[1] + "." + parts[2], jws.ErrAlgorithm},
		"typ not text":            {b64([]byte(`{"alg":"RS256","kid":"rs","typ":1}`)) + "." + parts[1] + "." + parts[2], jws.ErrType},
		"crit":                    {b64([]byte(`{"alg":"RS256","kid":"rs","crit":[]}`)) + "." + parts[1] + "." + parts[2], jws.ErrCritical},
		"no kid":                  {b64([]byte(`{"alg":"RS256"}`)) + "." + parts[1] + "." + parts[2], jws.ErrKeyID},
		"kid empty":               {b64([]byte(`{"alg":"RS256","kid":""}`)) + "." + parts[1] + "." + parts[2], jws.ErrKeyID},
		"kid with a space":        {b64([]byte(`{"alg":"RS256","kid":"r s"}`)) + "." + parts[1] + "." + parts[2], jws.ErrKeyID},
		"kid of 257":              {b64([]byte(`{"alg":"RS256","kid":"`+strings.Repeat("k", 257)+`"}`)) + "." + parts[1] + "." + parts[2], jws.ErrKeyID},
		"kid not text":            {b64([]byte(`{"alg":"RS256","kid":null}`)) + "." + parts[1] + "." + parts[2], jws.ErrKeyID},
		"b64 false":               {signRS(t, rk, `{"alg":"RS256","kid":"rs","b64":false}`, `{}`), jws.ErrHeaderMember},
		"an unknown key":          {signRS(t, rk, `{"alg":"RS256","kid":"other"}`, `{}`), jws.ErrUnknownKey},
		"an RSA key too short":    {signRS(t, rk, `{"alg":"RS256","kid":"short"}`, `{}`), jws.ErrUnusableKey},
		"an RSA key with no N":    {signRS(t, rk, `{"alg":"RS256","kid":"nil"}`, `{}`), jws.ErrUnusableKey},
		"an EC key as RS256":      {signRS(t, rk, `{"alg":"RS256","kid":"wrong-type"}`, `{}`), jws.ErrUnusableKey},
		"a P-384 key":             {signES(t, ek, `{"alg":"ES256","kid":"p384"}`, `{}`), jws.ErrUnusableKey},
		"an RSA key as ES256":     {signES(t, ek, `{"alg":"ES256","kid":"ec-as-rsa"}`, `{}`), jws.ErrUnusableKey},
		"an unusable key":         {signRS(t, rk, `{"alg":"RS256","kid":"unusable"}`, `{}`), jws.ErrUnusableKey},
		"an ES key for RS256":     {signRS(t, rk, `{"alg":"RS256","kid":"es"}`, `{}`), jws.ErrUnusableKey},
		"RS256 bad signature":     {parts[0] + "." + parts[1] + "." + flipped, jws.ErrSignature},
		"RS256 other claims":      {parts[0] + "." + b64([]byte(`{"a":2}`)) + "." + parts[2], jws.ErrSignature},
		"ES256 other claims":      {esParts[0] + "." + b64([]byte(`{"a":2}`)) + "." + esParts[2], jws.ErrSignature},
		"ES256 63 bytes":          {esParts[0] + "." + esParts[1] + "." + b64(make([]byte, 63)), jws.ErrSignature},
		"ES256 zeros":             {esParts[0] + "." + esParts[1] + "." + b64(make([]byte, 64)), jws.ErrSignature},
	} {
		tok, err := jws.Parse(c.token)
		if err == nil {
			err = jws.Verify(tok, keys)
		}
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
		seen[err] = true
	}
	for name, c := range map[string]struct {
		body string
		want error
	}{
		"not an object":             {`[]`, jws.ErrKeySet},
		"not UTF-8":                 {"{\"keys\":[],\"x\":\"\xff\"}", jws.ErrKeySet},
		"no keys":                   {`{}`, jws.ErrKeyList},
		"keys not a list":           {`{"keys":{}}`, jws.ErrKeyList},
		"65 keys":                   {`{"keys":[` + strings.Repeat(`{},`, 64) + `{}]}`, jws.ErrKeyList},
		"a key not an object":       {`{"keys":[1]}`, jws.ErrKeyNotObject},
		"a key with a member twice": {`{"keys":[{"kid":"a","kid":"b"}]}`, jws.ErrKeyNotObject},
		"two keys with one kid":     {`{"keys":[{"kid":"a"},{"kid":"a"}]}`, jws.ErrKeyIDTwice},
	} {
		_, err := jws.Keys([]byte(c.body))
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
		seen[err] = true
	}
	for _, err := range jws.Refusals {
		if !seen[err] {
			t.Errorf("never seen: %v", err)
		}
	}
	for err := range seen {
		if !slices.Contains(jws.Refusals, err) {
			t.Errorf("not in Refusals: %v", err)
		}
	}
}

// Keys keeps a key that verifies nothing here as unusable, and leaves out a
// key no token can name.
func TestKeysKeepsWhatCannotBeUsed(t *testing.T) {
	rk, ek := generate(t, 2048), generateEC(t, elliptic.P256())
	r, e := rsaJWK("r", &rk.PublicKey), ecJWK(t, "e", &ek.PublicKey)
	bigE := new(big.Int).Lsh(big.NewInt(1), 31)
	bigE.Add(bigE, big.NewInt(1))
	offCurve := with(e, nil)
	y := []byte(b64(make([]byte, 32)))
	offCurve["y"] = string(y)
	usable := map[string]map[string]any{
		"rsa":                    r,
		"rsa for sig RS256":      with(r, map[string]any{"use": "sig", "alg": "RS256"}),
		"rsa with an issuer":     with(r, map[string]any{"issuer": "https://login.example.com/{tenantid}/v2.0"}),
		"rsa with a null issuer": with(r, map[string]any{"issuer": nil}),
		"rsa with a zero first":  with(r, map[string]any{"n": b64(append([]byte{0}, rk.N.Bytes()...))}),
		"ec":                     e,
		"ec for sig ES256":       with(e, map[string]any{"use": "sig", "alg": "ES256"}),
	}
	unusable := map[string]map[string]any{
		"rsa for enc":          with(r, map[string]any{"use": "enc"}),
		"rsa use not text":     with(r, map[string]any{"use": 1}),
		"rsa named ES256":      with(r, map[string]any{"alg": "ES256"}),
		"rsa named PS256":      with(r, map[string]any{"alg": "PS256"}),
		"rsa alg not text":     with(r, map[string]any{"alg": 1}),
		"rsa issuer a number":  with(r, map[string]any{"issuer": 1}),
		"rsa of 1024 bits":     rsaJWK("r", &generate(t, 1024).PublicKey),
		"rsa n not text":       with(r, map[string]any{"n": 1}),
		"rsa n not base64url":  with(r, map[string]any{"n": "+/"}),
		"rsa n empty":          with(r, map[string]any{"n": ""}),
		"rsa n of 4097 bits":   with(r, map[string]any{"n": b64(append([]byte{1}, make([]byte, 512)...))}),
		"rsa n too many bytes": with(r, map[string]any{"n": b64(make([]byte, 514))}),
		"rsa e even":           with(r, map[string]any{"e": b64([]byte{2})}),
		"rsa e 1":              with(r, map[string]any{"e": b64([]byte{1})}),
		"rsa e of 32 bits":     with(r, map[string]any{"e": b64(bigE.Bytes())}),
		"rsa e of 5 bytes":     with(r, map[string]any{"e": b64([]byte{1, 0, 0, 0, 1})}),
		"oct":                  {"kty": "oct", "k": "AAAA"},
		"no kty":               with(r, map[string]any{"kty": nil}),
		"OKP":                  {"kty": "OKP", "crv": "Ed25519", "x": b64(make([]byte, 32))},
		"ec P-384":             with(e, map[string]any{"crv": "P-384"}),
		"ec no crv":            with(e, map[string]any{"crv": nil}),
		"ec x short":           with(e, map[string]any{"x": b64(make([]byte, 31))}),
		"ec y not text":        with(e, map[string]any{"y": 1}),
		"ec x not base64url":   with(e, map[string]any{"x": "!"}),
		"ec off the curve":     offCurve,
		"ec named RS256":       with(e, map[string]any{"alg": "RS256"}),
	}
	for name, k := range usable {
		keys, err := jws.Keys(set(with(k, map[string]any{"kid": "k"})))
		if err != nil || len(keys) != 1 || keys[0].Public == nil || keys[0].Algorithm == "" || keys[0].ID != "k" {
			t.Errorf("%s: %+v %v", name, keys, err)
		}
	}
	for name, k := range unusable {
		keys, err := jws.Keys(set(with(k, map[string]any{"kid": "k"})))
		if err != nil || len(keys) != 1 || keys[0].Public != nil || keys[0].Algorithm != "" || keys[0].ID != "k" {
			t.Errorf("%s: %+v %v", name, keys, err)
		}
	}
	keys, err := jws.Keys(set(with(r, map[string]any{"issuer": "https://login.example.com/tenant/v2.0"})))
	if err != nil || keys[0].Issuer != "https://login.example.com/tenant/v2.0" {
		t.Fatal(keys, err)
	}
	// A key with no kid, or one no header can carry, is left out.
	keys, err = jws.Keys(set(with(r, map[string]any{"kid": nil}), with(r, map[string]any{"kid": "a b"}), with(r, map[string]any{"kid": strings.Repeat("k", 257)}), with(r, map[string]any{"kid": 1}), e))
	if err != nil || len(keys) != 1 || keys[0].ID != "e" {
		t.Fatal(keys, err)
	}
	keys, err = jws.Keys([]byte(`{"keys":[]}`))
	if err != nil || keys == nil || len(keys) != 0 {
		t.Fatal(keys, err)
	}
}

func TestObjectAndText(t *testing.T) {
	m, ok := jws.Object([]byte(`{"a":"x","b":[1]}`))
	if !ok || len(m) != 2 {
		t.Fatal(m)
	}
	if s, ok := jws.Text(m["a"]); !ok || s != "x" {
		t.Fatal(s)
	}
	for _, v := range []string{"", "null", "1", `["x"]`, `"\ud800"x`} {
		if _, ok := jws.Text(json.RawMessage(v)); ok {
			t.Errorf("%q is text", v)
		}
	}
	for _, raw := range []string{"", "{", `{"a"}`, `{"a":1,}`, `{"a":1} 1`, `"x"`, `{"a":1,"a":1}`} {
		if _, ok := jws.Object([]byte(raw)); ok {
			t.Errorf("%q is an object", raw)
		}
	}
}
