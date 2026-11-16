package oauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The cases pull request 133's review found (findings 1 to 3): each was
// accepted before its fix.

// httpsWithoutAPublicHost are https redirect URIs that name no host a
// browser reaches as written, or that a browser reads as loopback: an empty
// host, a port out of range, and the numeric and trailing-dot spellings of
// 127.0.0.1 and localhost the URL standard reads as them.
var httpsWithoutAPublicHost = []string{
	"https://:443/cb",
	"https://vendor.example.com:65536/cb",
	"https://vendor.example.com:0/cb",
	"https://vendor.example.com:/cb",
	"https://vendor.example.com:0443/cb",
	"https://localhost./cb",
	"https://app.localhost./cb",
	"https://127.1/cb",
	"https://2130706433/cb",
	"https://0x7f000001/cb",
	"https://0X7F000001/cb",
	"https://0177.0.0.1/cb",
	"https://127.0.0.1./cb",
	"https://127.0.1/cb",
	"https://0.0.0.0/cb",
	"https://[::]/cb",
	"https://[::ffff:127.0.0.1]/cb",
	"https://[0:0:0:0:0:0:0:1]/cb",
	"https://vendor..example.com/cb",
	"https://.vendor.example.com/cb",
}

// httpsWithAPublicHost are https redirect URIs that stay accepted.
var httpsWithAPublicHost = []string{
	"https://vendor.example.com/cb",
	"https://vendor.example.com:8443/cb",
	"https://vendor.example.com:65535/cb",
	"https://203.0.113.7/cb",
	"https://203.0.113.7:8443/cb",
	"https://[2001:db8::1]/cb",
	"https://claude.ai/api/mcp/auth_callback",
}

// V2 (review finding 2). A document whose https redirect URI names no public
// host is refused.
func TestV2DocumentRefusesAnHTTPSRedirectWithoutAPublicHost(t *testing.T) {
	for _, uri := range httpsWithoutAPublicHost {
		if d, err := Document(codeID, doc(map[string]any{"redirect_uris": []string{uri}})); err == nil {
			t.Errorf("%s: accepted, %+v", uri, d)
		} else {
			code(t, err, ErrInvalidClient, string(contracts.ClientDocumentInvalid))
		}
	}
	for _, uri := range httpsWithAPublicHost {
		if _, err := Document(codeID, doc(map[string]any{"redirect_uris": []string{uri}})); err != nil {
			t.Errorf("%s: %v", uri, err)
		}
	}
}

// V3 and V4 (review finding 2). Such a redirect URI is never allowed, so an
// authorization request naming it is refused unproven and never redirected,
// even for a document that holds it.
func TestV3AnHTTPSRedirectWithoutAPublicHostIsNeverProven(t *testing.T) {
	for _, uri := range httpsWithoutAPublicHost {
		if RedirectAllowed([]string{uri}, uri) {
			t.Errorf("%s: allowed", uri)
		}
		d := &ClientDocument{ClientID: codeID, RedirectURIs: []string{uri}, TokenEndpointAuthMethod: AuthNone}
		p, proven, err := Authorize(with(request(), set("redirect_uri", uri)), d, config)
		if proven || p != nil {
			t.Errorf("%s: proven %v, %+v", uri, proven, p)
		}
		code(t, err, ErrInvalidClient, string(contracts.RedirectURIInvalid))
	}
	for _, uri := range httpsWithAPublicHost {
		if !RedirectAllowed([]string{uri}, uri) {
			t.Errorf("%s: not allowed", uri)
		}
	}
}

// V2 (review finding 3). A token_endpoint_auth_method that is there and
// empty is not one of none and private_key_jwt: it is refused, not read as
// a public client. Absent stays none.
func TestV2AnEmptyAuthenticationMethodIsRefused(t *testing.T) {
	for _, extra := range []map[string]any{
		{"token_endpoint_auth_method": ""},
		{"token_endpoint_auth_method": "", "jwks_uri": "https://vendor.example.com/keys"},
	} {
		if d, err := Document(codeID, doc(extra)); err == nil {
			t.Errorf("%v: accepted as %q", extra, d.TokenEndpointAuthMethod)
		} else {
			code(t, err, ErrInvalidClient, string(contracts.ClientDocumentInvalid))
		}
	}
	for extra, want := range map[string]string{"": AuthNone, AuthNone: AuthNone, AuthPrivateKeyJWT: AuthPrivateKeyJWT} {
		members := map[string]any{"jwks_uri": "https://vendor.example.com/keys"}
		if extra != "" {
			members["token_endpoint_auth_method"] = extra
		}
		d, err := Document(codeID, doc(members))
		if err != nil || d.TokenEndpointAuthMethod != want {
			t.Errorf("%q: %+v %v", extra, d, err)
		}
	}
}

// V8 (review finding 1). An assertion whose signed header says its claims
// are not base64url (RFC 7797 forbids that for a JWT), or that carries or
// points to a key, or with a member of the wrong kind that go-jose refuses,
// is refused though its signature verifies over what was sent.
func TestV8AnAssertionsHeaderIsInTheProfile(t *testing.T) {
	_, _, keys := signers(t)
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := other.PublicKey.Bytes()
	enc := base64.RawURLEncoding.EncodeToString
	jwk := `{"kty":"EC","crv":"P-256","x":"` + enc(p[1:33]) + `","y":"` + enc(p[33:]) + `"}`
	for _, header := range []string{
		`{"alg":"RS256","kid":"rs","b64":false}`,
		`{"alg":"ES256","kid":"es","b64":false}`,
		`{"alg":"RS256","kid":"rs","b64":"false"}`,
		`{"alg":"RS256","kid":"rs","b64":null}`,
		`{"alg":"RS256","kid":"rs","jwk":{"kty":"RSA"}}`,
		`{"alg":"ES256","kid":"es","jwk":` + jwk + `}`,
		`{"alg":"RS256","kid":"rs","jku":"https://evil.example.com/keys"}`,
		`{"alg":"RS256","kid":"rs","x5u":"https://evil.example.com/cert"}`,
		`{"alg":"RS256","kid":"rs","x5c":["AAAA"]}`,
		`{"alg":"RS256","kid":"rs","nonce":1}`,
	} {
		_, _, err := VerifyAssertion(vectorDocument(""), keys, assertion(t, header, nil), vectorAudiences, vectorNow)
		var e *Error
		if !errors.As(err, &e) || e.Code != ErrInvalidClient || e.Reason != ReasonAssertion {
			t.Errorf("%s: %v", header, err)
		}
	}
	// b64 true is what a header without it means.
	if _, _, err := VerifyAssertion(vectorDocument(""), keys, assertion(t, `{"alg":"RS256","kid":"rs","b64":true,"typ":"JWT"}`, nil), vectorAudiences, vectorNow); err != nil {
		t.Fatal(err)
	}
}
