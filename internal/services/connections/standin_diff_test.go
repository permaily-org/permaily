package connections

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/jws"
	"github.com/permaily-org/permaily/internal/oauth"
)

// Every stand-in against internal/oauth and internal/jws as merged (#133),
// on the review of pull request 136's inputs and more: they are equal
// until the calls are swapped and standin.go goes.
func TestTheStandInsEqualTheOAuthCore(t *testing.T) {
	same := func(name string, a, b any) {
		t.Helper()
		if fmt.Sprintf("%v", a) != fmt.Sprintf("%v", b) {
			t.Errorf("%s: stand-in %v, #133 %v", name, a, b)
		}
	}
	errOf := func(err error) string {
		var a *oauthRefusal
		var b *oauth.Error
		switch {
		case err == nil:
			return "nil"
		case errors.As(err, &a):
			return string(a.Code) + "/" + a.Reason
		case errors.As(err, &b):
			return string(b.Code) + "/" + b.Reason
		}
		return "other"
	}
	for _, scope := range []string{"", "mail.sender", "mail.sender mail.sender", "mail.sender\tmail.subject", " mail.subject", "mail.subject  mail.body", "mail.body mail.subject", "mail.x", "mail.attachments mail.body mail.subject"} {
		a, ae := parseScopes(scope, true)
		b, be := oauth.Scopes(scope, true)
		same("scope "+scope, fmt.Sprint(a, errOf(ae)), fmt.Sprint(b, errOf(be)))
	}
	same("default scopes", fmt.Sprint(parseScopes("", false)), fmt.Sprint(oauth.Scopes("", false)))
	for _, k := range []oauth.Kind{oauth.KindAccess, oauth.KindRefresh, oauth.KindCode} {
		for _, gen := range []uint64{0, 3, 399, 400, 1 << 40} {
			same("family secret", familySecret([]byte("synthetic"), k, "fam_test", gen), oauth.FamilySecret([]byte("synthetic"), k, "fam_test", gen))
		}
	}
	same("hash", hashOf(oauth.KindCode, "x"), oauth.Hash(oauth.KindCode, "x"))
	v, c := pkce(t)
	for _, pair := range [][2]string{{c, v}, {c, v + "x"}, {c, "short"}, {"bad", v}, {c + "=", v}} {
		same("pkce", verifyPKCE(pair[0], pair[1]), oauth.VerifyPKCE(pair[0], pair[1]))
	}
	registered := []string{"https://xn--bcher-kva.example/cb", "https://EXAMPLE.org/cb", "https://ex%61mple.org/cb", loopback, "http://127.0.0.1/callback", "https://chatgpt.com/connector/oauth/back", "http://[::1]:8080/cb?x=1", "https://example.org/cb?a=b"}
	for _, got := range []string{
		loopback, "http://localhost:0/callback", "http://localhost:65536/callback", "http://localhost:0123/callback", "http://localhost:/callback",
		"http://localhost/callback#", "http://localhost/callback?", "http://localhost:8080/callback", "http://LOCALHOST/callback", "HTTP://localhost/callback",
		"http://127.0.0.1:9/callback", "http://[::1]/cb?x=1", "http://[::1]:1/cb?x=1", "http://[::1]:1/cb?x=2", "https://chatgpt.com/connector/oauth/back",
		"https://chatgpt.com/connector/oauth/back#", "https://localhost/x", "https://127.0.0.1/x", "http://user@localhost/callback", "http://localhost\\callback",
		"https://example.org/cb?a=b", "https://example.org/cb?a=b&", "", "%zz", "https://example.org:0/cb?a=b",
		"https://ex%61mple.org/cb", "https://xn--a.org/cb", "https://xn--localhost-.org/cb", "https://xn--bcher-kva.example/cb", "https://EXAMPLE.org/cb",
		"https://-x.org/cb", "https://" + strings.Repeat("a", 64) + ".org/cb", "https://a_b.org/cb", "https://1.2.3.4/cb", "https://[2001:db8::1]/cb",
	} {
		same("redirect "+got, redirectAllowed(registered, got), oauth.RedirectAllowed(registered, got))
	}
	for _, uri := range []string{"https://example.org/cb?x=a%20b&iss=prior", "https://example.org/cb", "https://example.org/cb?", "http://localhost/cb?b=2&a=1", "%zz"} {
		vals := url.Values{"code": {"example"}, "state": {"s t"}, "iss": {"mine"}}
		same("redirect with "+uri, redirectWith(uri, vals, issuer), oauth.Redirect(uri, vals, issuer))
	}
	w := newWorld(t)
	x := w.ask(claudeCode)
	doc := &w.vendors.clients[claudeCode].Document
	long := strings.Repeat("s", oauth.StateMaxBytes+1)
	pushedID, _ := newSecret(oauth.PrefixPushed, func(b []byte) error { _, err := rand.Read(b); return err })
	for i, change := range []url.Values{
		{}, {"client_id": {"https://other.example/c"}}, {"client_id": {claudeCode, claudeCode}}, {"response_mode": {"fragment"}}, {"response_mode": {"query"}},
		{"ends_in_days": {"30"}}, {"query": {"{}"}}, {"mail_since": {"new"}}, {"state": {"first", "second"}}, {"state": {long}},
		{"state": {long}, "response_type": {"token"}}, {"response_type": {"token"}}, {"code_challenge": {"short"}}, {"code_challenge_method": {"plain"}},
		{"resource": {""}}, {"resource": {"https://elsewhere"}}, {"scope": {""}}, {"scope": {"mail.body mail.body"}}, {"redirect_uri": {"http://localhost:9/callback"}},
		{"redirect_uri": {"https://evil.example/cb"}}, {"request_uri": {oauth.RequestURIPrefix + pushedID}}, {"request_uri": {"urn:x"}},
	} {
		q, _ := url.ParseQuery(x.query())
		for k, v := range change {
			q[k] = v
		}
		if _, has := change["request_uri"]; has {
			q = url.Values{"client_id": {claudeCode}, "request_uri": change["request_uri"]}
		}
		a, ap, ae := authorizeParams(q, doc, oauthConfig)
		b, bp, be := oauth.Authorize(q, doc, oauthConfig)
		same(fmt.Sprintf("authorize %d %v", i, change), fmt.Sprintf("%+v %t %s", a, ap, errOf(ae)), fmt.Sprintf("%+v %t %s", b, bp, errOf(be)))
	}
	other := &w.vendors.clients[otherApp].Document
	y := w.ask(otherApp)
	for i, change := range []url.Values{
		{}, {"query": {`{"v":999,"all":[]}`}}, {"query": {`{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"x"}]}]}`}}, {"query": {"{"}},
		{"ends_in_days": {"01"}}, {"ends_in_days": {"+1"}}, {"ends_in_days": {"30"}}, {"ends_in_days": {"0"}}, {"mail_since": {"new"}}, {"mail_since": {"yesterday"}},
		{"mail_since": {"2026-01-01T00:00:00Z"}}, {"unknown": {strings.Repeat("x", oauth.FormMaxBytes)}}, {"redirect_uri": {"https://not-listed.example/c"}},
		{"client_id": {chatgpt}}, {"client_assertion": {"a", "b"}}, {"request_uri": {"x"}}, {"response_mode": {"fragment"}}, {"state": {long}},
	} {
		q, _ := url.ParseQuery(y.query())
		for k, v := range change {
			q[k] = v
		}
		a, ae := pushedParams(q, other, oauthConfig)
		b, be := oauth.Pushed(q, other, oauthConfig)
		same(fmt.Sprintf("pushed %d %v", i, change), fmt.Sprintf("%+v %s", a, errOf(ae)), fmt.Sprintf("%+v %s", b, errOf(be)))
	}

	adoc := w.vendors.clients[chatgpt].Document
	keys := w.vendors.keys[chatgpt]
	jk := []jws.Key{}
	for _, k := range keys {
		jk = append(jk, jws.Key{ID: k.ID, Algorithm: k.Algorithm, Public: k.Public})
	}
	now := w.clock.now()
	head := map[string]any{"alg": "RS256", "kid": "k1"}
	claims := map[string]any{"iss": chatgpt, "sub": chatgpt, "aud": issuer, "exp": now.Add(time.Minute).Unix(), "iat": now.Unix(), "jti": "unique"}
	for _, tc := range []struct {
		name   string
		header bool
		key    string
		value  any
	}{
		{"good", true, "kid", "k1"}, {"crit", true, "crit", []string{"unknown"}}, {"typ number", true, "typ", 7}, {"typ text", true, "typ", "JWT"},
		{"unknown kid", true, "kid", "k9"}, {"kid on the EC key", true, "kid", "e1"}, {"HS256", true, "alg", "HS256"}, {"x5c", true, "x5c", []string{"a"}},
		{"b64 false", true, "b64", false}, {"nonce number", true, "nonce", 1}, {"jwk junk", true, "jwk", map[string]any{"kty": "oct"}},
		{"nbf future", false, "nbf", now.Add(time.Hour).Unix()}, {"nbf past", false, "nbf", now.Add(-time.Hour).Unix()}, {"iat null", false, "iat", nil},
		{"exp fraction", false, "exp", float64(now.Add(time.Minute).Unix()) + 0.5}, {"exp exponent", false, "exp", json.RawMessage("1.795e9")},
		{"exp past", false, "exp", now.Add(-time.Hour).Unix()}, {"exp far", false, "exp", now.Add(24 * time.Hour).Unix()},
		{"JTI uppercase", false, "JTI", "unique"}, {"ISS uppercase", false, "ISS", chatgpt}, {"aud list", false, "aud", []any{"x", issuer}},
		{"aud list with null", false, "aud", []any{nil, issuer}}, {"aud other", false, "aud", "https://else"}, {"jti long", false, "jti", strings.Repeat("j", 257)},
		{"large claims", false, "extension", strings.Repeat("x", 17000)},
	} {
		h, c := map[string]any{}, map[string]any{}
		for k, v := range head {
			h[k] = v
		}
		for k, v := range claims {
			c[k] = v
		}
		if tc.header {
			h[tc.key] = tc.value
		} else {
			c[tc.key] = tc.value
		}
		if tc.key == "JTI" {
			delete(c, "jti")
		}
		if tc.key == "ISS" {
			delete(c, "iss")
		}
		hb, _ := json.Marshal(h)
		cb, _ := json.Marshal(c)
		signing := b64.EncodeToString(hb) + "." + b64.EncodeToString(cb)
		digest := sha256.Sum256([]byte(signing))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, w.jwtKey, crypto.SHA256, digest[:])
		compact := signing + "." + b64.EncodeToString(sig)
		aj, ax, ae := verifyAssertion(&adoc, keys, compact, []string{issuer}, now)
		bj, bx, be := oauth.VerifyAssertion(&adoc, jk, compact, []string{issuer}, now)
		same("assertion "+tc.name, fmt.Sprint(aj, ax, errOf(ae), errors.Is(ae, errUnknownKey)), fmt.Sprint(bj, bx, errOf(be), errors.Is(be, jws.ErrUnknownKey)))
	}
	public := w.vendors.clients[claudeCode].Document
	_, _, ae := verifyAssertion(&public, keys, "a.b.c", []string{issuer}, now)
	_, _, be := oauth.VerifyAssertion(&public, jk, "a.b.c", []string{issuer}, now)
	same("assertion of a public client", errOf(ae), errOf(be))
}
