package cimd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/vendors"
)

const jwksURL = "https://app.vendor.dev/oauth/jwks.json"

// jwtDocument is a document for id that declares private_key_jwt with its
// key set at jwksURL.
func jwtDocument(id string) []byte {
	return fmt.Appendf(nil, `{"client_id":%q,"client_name":"Played","redirect_uris":["https://app.vendor.dev/callback"],"token_endpoint_auth_method":"private_key_jwt","token_endpoint_auth_signing_alg":"RS256","jwks_uri":%q}`, id, jwksURL)
}

var b64 = base64.RawURLEncoding.EncodeToString

func rsaJWKOf(kid string, pub *rsa.PublicKey, extra string) string {
	return fmt.Sprintf(`{"kty":"RSA","kid":%q,"n":%q,"e":%q%s}`, kid, b64(pub.N.Bytes()), b64(big.NewInt(int64(pub.E)).Bytes()), extra)
}

func rsaJWK(t testing.TB, kid string) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return rsaJWKOf(kid, &k.PublicKey, `,"use":"sig","alg":"RS256"`)
}

func ecJWK(t testing.TB, kid string, curve elliptic.Curve, crv string) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := k.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	n := (len(b) - 1) / 2
	return fmt.Sprintf(`{"kty":"EC","kid":%q,"crv":%q,"x":%q,"y":%q}`, kid, crv, b64(b[1:1+n]), b64(b[1+n:]))
}

// jwtVendor serves jwtDocument for any client and set at jwksURL, both
// with cc.
func jwtVendor(set func() []byte, cc string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/jwks.json" {
			answer(set(), "Cache-Control", cc)(w, r)
			return
		}
		answer(jwtDocument("https://"+r.Host+r.URL.Path), "Cache-Control", cc)(w, r)
	}
}

func (p *played) jwksFetches() int {
	n := 0
	for _, r := range p.v.requests() {
		if r.Path == "/oauth/jwks.json" {
			n++
		}
	}
	return n
}

// V15 (ADR 0030): a key set is fetched, cached as documents are, and
// fetched once more for an unknown kid at most once a minute.
func TestV15KeySetsAreCachedAndRefreshedOnceAMinute(t *testing.T) {
	k1 := rsaJWK(t, "k1")
	p := newPlayed(t, jwtVendor(func() []byte { return keySet(k1) }, "max-age=300"))
	ctx := context.Background()
	c, err := p.r.Client(ctx, clientURL)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		keys, err := p.r.Keys(ctx, c, false)
		if err != nil || len(keys) != 1 || keys[0].ID != "k1" || keys[0].Algorithm != "RS256" {
			t.Fatalf("keys %v %v", keys, err)
		}
		if _, ok := keys[0].Public.(*rsa.PublicKey); !ok {
			t.Fatalf("public %T", keys[0].Public)
		}
	}
	if n := p.jwksFetches(); n != 1 {
		t.Fatalf("%d fetches of a fresh set", n)
	}
	// An unknown kid: fetched again at once, then not again within the minute.
	if _, err := p.r.Keys(ctx, c, true); err != nil || p.jwksFetches() != 2 {
		t.Fatalf("refresh: %v, %d", err, p.jwksFetches())
	}
	p.clk.add(59 * time.Second)
	if _, err := p.r.Keys(ctx, c, true); err != nil || p.jwksFetches() != 2 {
		t.Fatalf("refresh within the minute: %v, %d", err, p.jwksFetches())
	}
	p.clk.add(time.Second)
	if _, err := p.r.Keys(ctx, c, true); err != nil || p.jwksFetches() != 3 {
		t.Fatalf("refresh after the minute: %v, %d", err, p.jwksFetches())
	}
	// Past its max-age it is revalidated like a document.
	p.clk.add(5 * time.Minute)
	if _, err := p.r.Keys(ctx, c, false); err != nil || p.jwksFetches() != 4 {
		t.Fatalf("stale: %v, %d", err, p.jwksFetches())
	}
}

// V15: a key set is at most 16 KiB.
func TestV15AKeySetIsAtMost16KiB(t *testing.T) {
	k1 := rsaJWK(t, "k1")
	pad := func(n int) []byte {
		s := keySet(k1)
		return append(append(s[:len(s)-1:len(s)-1], bytes.Repeat([]byte(" "), n-len(s))...), '}')
	}
	for n, want := range map[int]error{oauth.JWKSMaxBytes: nil, oauth.JWKSMaxBytes + 1: vendors.ErrDocument} {
		p := newPlayed(t, jwtVendor(func() []byte { return pad(n) }, "max-age=300"))
		c, err := p.r.Client(context.Background(), clientURL)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.r.Keys(context.Background(), c, false); want == nil && err != nil || want != nil && !errors.Is(err, want) {
			t.Errorf("%d bytes: %v", n, err)
		}
	}
}

// V15: a client with no key set has none to fetch; a key set's URL is
// held to the rules of a client_id's.
func TestV15KeysOnlyForADocumentThatDeclaresThem(t *testing.T) {
	p := newPlayed(t, documents("Cache-Control", "max-age=300"))
	c, err := p.r.Client(context.Background(), clientURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.r.Keys(context.Background(), c, false); !errors.Is(err, vendors.ErrDocument) || reason(err) != ReasonNoKeys {
		t.Errorf("public client: %v", err)
	}
	if _, err := p.r.Keys(context.Background(), nil, false); !errors.Is(err, vendors.ErrDocument) {
		t.Errorf("no client: %v", err)
	}
	c.Document.TokenEndpointAuthMethod = oauth.AuthPrivateKeyJWT
	for _, u := range []string{"https://127.0.0.1/jwks.json", "http://app.vendor.dev/jwks.json", "https://keys.permaily.com/jwks.json"} {
		c.Document.JWKSURI = u
		if _, err := p.r.Keys(context.Background(), c, false); !errors.Is(err, vendors.ErrDocument) || reason(err) != ReasonRules {
			t.Errorf("%s: %v", u, err)
		}
	}
	if n := p.jwksFetches(); n != 0 {
		t.Errorf("%d fetches", n)
	}
	// A document whose jwks_uri breaks the rules is refused whole.
	bad := newPlayed(t, answer([]byte(`{"client_id":"`+clientURL+`","redirect_uris":["https://app.vendor.dev/cb"],"token_endpoint_auth_method":"private_key_jwt","jwks_uri":"https://169.254.169.254/jwks"}`)))
	if _, err := bad.r.Client(context.Background(), clientURL); !errors.Is(err, vendors.ErrDocument) {
		t.Errorf("a jwks_uri the rules refuse: %v", err)
	}
}

// V15: what of a key set can check a client assertion: RSA of 2048 bits or
// more for RS256 and P-256 for ES256, each with a kid, for signing.
func TestV15WhatOfAKeySetIsKept(t *testing.T) {
	small, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	big2048, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaOK := rsaJWKOf("rsa", &big2048.PublicKey, "")
	ec := ecJWK(t, "ec", elliptic.P256(), "P-256")
	keys, err := parseKeys(keySet(
		rsaOK, ec,
		rsaJWKOf("small", &small.PublicKey, ""),
		rsaJWKOf("enc", &big2048.PublicKey, `,"use":"enc"`),
		rsaJWKOf("rs512", &big2048.PublicKey, `,"alg":"RS512"`),
		rsaJWKOf("", &big2048.PublicKey, ""),
		ecJWK(t, "p384", elliptic.P384(), "P-384"),
		`{"kty":"oct","kid":"hmac","k":"c2VjcmV0"}`,
		`{"kty":"EC","kid":"offcurve","crv":"P-256","x":"`+b64(make([]byte, 32))+`","y":"`+b64(make([]byte, 32))+`"}`,
		`{"kty":"EC","kid":"short","crv":"P-256","x":"AQ","y":"AQ"}`,
		`{"kty":"RSA","kid":"evenE","n":"`+b64(big2048.N.Bytes())+`","e":"Ag"}`,
		`{"kty":"RSA","kid":"badN","n":"!!","e":"AQAB"}`,
		`{"kty":"RSA","kid":"bigN","n":"`+b64(append([]byte{1}, make([]byte, 512)...))+`","e":"AQAB"}`,
		`{"kty":"RSA","kid":"nullUse","use":null,"n":"`+b64(big2048.N.Bytes())+`","e":"AQAB"}`,
		// jws.Keys reads a modulus with a zero byte in front, as some
		// writers put one: the same number.
		`{"kty":"RSA","kid":"zeroN","n":"`+b64(append([]byte{0}, big2048.N.Bytes()...))+`","e":"AQAB"}`,
	))
	if err != nil || len(keys) != 3 || keys[0].ID != "rsa" || keys[0].Algorithm != "RS256" || keys[1].ID != "ec" || keys[1].Algorithm != "ES256" || keys[2].ID != "zeroN" {
		t.Fatalf("kept %v, %v", keys, err)
	}
	if _, ok := keys[1].Public.(*ecdsa.PublicKey); !ok {
		t.Fatalf("ec public %T", keys[1].Public)
	}
	for name, set := range map[string][]byte{
		"duplicate kid": keySet(rsaOK, rsaOK),
		"nothing kept":  keySet(rsaJWKOf("small", &small.PublicKey, "")),
		"empty":         []byte(`{"keys":[]}`),
		"not an object": []byte(`[]`),
		"not JSON":      []byte(`{"keys":`),
	} {
		if _, err := parseKeys(set); !errors.Is(err, vendors.ErrDocument) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// ChatGPT's key set of 2026-11-16.
	b, err := os.ReadFile(filepath.Join("..", "..", "oauth", "testdata", "vendors", "jwks-chatgpt.json"))
	if err != nil {
		t.Fatal(err)
	}
	if keys, err := parseKeys(b); err != nil || len(keys) != 1 || keys[0].Public.(*rsa.PublicKey).N.BitLen() != 2048 {
		t.Fatalf("chatgpt: %v %v", keys, err)
	}
}

// V15: a key set and each of its keys are read as jws.Object reads every
// other object a stranger sends: a member twice, also in another case
// (which encoding/json reads as one, taking the last), is refused, and a
// name is matched exactly, so no other reader of the same bytes can take
// another key, kid or use.
func TestV15AKeySetHasEachMemberOnce(t *testing.T) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	good := rsaJWKOf("good", &k.PublicKey, "")
	key := func(extra string) string { return rsaJWKOf("k1", &k.PublicKey, extra) }
	for name, set := range map[string][]byte{
		"keys twice":                    []byte(`{"keys":[` + good + `],"keys":[` + good + `]}`),
		"keys twice in another case":    []byte(`{"Keys":[],"keys":[` + good + `]}`),
		"keys twice with a Kelvin sign": []byte(`{"keys":[` + good + `],"Keys":[]}`),
		"keys twice with a long s":      []byte(`{"keys":[` + good + `],"keyſ":[]}`),
		"a kid twice":                   keySet(good, key(`,"kid":"k2"`)),
		"a kid twice in another case":   keySet(good, key(`,"KID":"k2"`)),
		"a kty twice in another case":   keySet(good, key(`,"Kty":"oct"`)),
		"a kid twice with a Kelvin":     keySet(good, key(`,"Kid":"k2"`)),
		"a use twice with a long s":     keySet(good, key(`,"use":"sig","uſe":"enc"`)),
		"an alg twice in another case":  keySet(good, key(`,"alg":"RS256","ALG":"none"`)),
		"an n twice":                    keySet(good, key(`,"n":"AQAB"`)),
		"a key that is not an object":   keySet(good, `null`),
	} {
		if _, err := parseKeys(set); !errors.Is(err, vendors.ErrDocument) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A name in another case alone is not the member: no kid, no key.
	for name, set := range map[string][]byte{
		"KEYS": []byte(`{"KEYS":[` + good + `]}`),
		"KID":  keySet(strings.Replace(good, `"kid"`, `"KID"`, 1)),
		"Kid":  keySet(strings.Replace(good, `"kid"`, `"Kid"`, 1)),
		"Kty":  keySet(strings.Replace(good, `"kty"`, `"Kty"`, 1)),
	} {
		if keys, err := parseKeys(set); !errors.Is(err, vendors.ErrDocument) {
			t.Errorf("%s: %v %v", name, keys, err)
		}
	}
	// A key whose use is not text checks nothing, and members the resolver
	// does not read are ignored.
	if keys, err := parseKeys(keySet(good, key(`,"use":1`))); err != nil || len(keys) != 1 || keys[0].ID != "good" {
		t.Errorf("a use that is a number: %v %v", keys, err)
	}
	if _, err := parseKeys([]byte(`{"keys":{}}`)); !errors.Is(err, vendors.ErrDocument) {
		t.Errorf("keys that are an object: %v", err)
	}
	keys, err := parseKeys([]byte(`{"keys":[` + strings.Replace(good, `"kty"`, `"x5c":["AQ"],"ext":true,"key_ops":["verify"],"kty"`, 1) + `],"extra":{"a":1}}`))
	if err != nil || len(keys) != 1 || keys[0].ID != "good" {
		t.Fatalf("unknown members: %v %v", keys, err)
	}
}

// V15 (review of pull request 130, finding 4): at most CacheMax key-set
// refreshes are remembered at once. Past them a refresh is refused, the key
// set held answered and nothing fetched, and none remembered is dropped
// within its minute; after it they are forgotten and a refresh is fetched.
func TestV15TheRefreshesRememberedAreBounded(t *testing.T) {
	p := newPlayed(t, jwtVendor(func() []byte { return keySet(rsaJWK(t, "k1")) }, "max-age=300"))
	ctx := context.Background()
	c, err := p.r.Client(ctx, clientURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.r.Keys(ctx, c, false); err != nil {
		t.Fatal(err)
	}
	p.r.mu.Lock()
	for i := range vendors.CacheMax {
		u := fmt.Sprintf("https://keys-%d.vendor.dev/jwks.json", i)
		if !p.r.forced.may(u, p.clk.now()) {
			t.Fatalf("%d: no room", i)
		}
		p.r.forced.mark(u, p.clk.now())
	}
	p.r.mu.Unlock()
	p.clk.add(KeysRefreshEvery - time.Second)
	if keys, err := p.r.Keys(ctx, c, true); err != nil || len(keys) != 1 || p.jwksFetches() != 1 {
		t.Fatalf("a refresh past the bound: %v, %d fetches", err, p.jwksFetches())
	}
	p.r.mu.Lock()
	n, first := len(p.r.forced.byURL), p.r.forced.byURL["https://keys-0.vendor.dev/jwks.json"] != nil
	p.r.mu.Unlock()
	if n != vendors.CacheMax || !first {
		t.Fatalf("%d refreshes remembered, the first kept %v", n, first)
	}
	p.clk.add(time.Second)
	if _, err := p.r.Keys(ctx, c, true); err != nil || p.jwksFetches() != 2 {
		t.Fatalf("a refresh after the minute: %v, %d fetches", err, p.jwksFetches())
	}
	p.r.mu.Lock()
	n = len(p.r.forced.byURL)
	p.r.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d refreshes remembered", n)
	}
}

// V15 (review of pull request 130, finding 4): a key-set refresh the
// limit of new fetches refuses leaves nothing remembered, however many
// URLs ask.
func TestV15ARefusedRefreshRemembersNothing(t *testing.T) {
	p := newPlayed(t, documents())
	p.r.mu.Lock()
	p.r.window = p.clk.now()
	p.r.fetched = vendors.FetchesPerMinute
	p.r.mu.Unlock()
	for i := range 3 * vendors.CacheMax {
		c := &vendors.Client{Document: oauth.ClientDocument{TokenEndpointAuthMethod: oauth.AuthPrivateKeyJWT, JWKSURI: fmt.Sprintf("https://app.vendor.dev/keys/%d", i)}}
		if _, err := p.r.Keys(context.Background(), c, true); reason(err) != ReasonRateLimited {
			t.Fatalf("%d: %v", i, err)
		}
	}
	p.r.mu.Lock()
	n := len(p.r.forced.byURL)
	p.r.mu.Unlock()
	if n != 0 {
		t.Errorf("%d refreshes remembered of none fetched", n)
	}
	if p.v.count() != 0 {
		t.Errorf("%d fetches", p.v.count())
	}
}
