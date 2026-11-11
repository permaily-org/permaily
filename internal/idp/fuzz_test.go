package idp_test

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/idp/idptest"
)

const fuzzNonce = "nonce-of-the-fuzzed-sign-in"

// captured signs in once with each provider of the fake and returns what
// each endpoint answered, by "provider/path": the seeds are what a provider
// really sends, so the fuzzer starts inside the parsers and not at their door.
func captured(f *testing.F) map[string][]byte {
	f.Helper()
	w := newWorld(f)
	var mu sync.Mutex
	answers := map[string][]byte{}
	w.client.HTTP = &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		resp, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))
		mu.Lock()
		answers[strings.TrimPrefix(r.URL.Path, "/")] = body
		mu.Unlock()
		return resp, err
	})}
	for _, p := range idp.Providers {
		w.fake.SignInAs(p, idptest.Person{Subject: "583231", Address: "ada@example.com", Verified: true, Name: "Ada Lovelace", Object: object})
		if _, err := w.signIn(f, p); err != nil {
			f.Fatal(err)
		}
	}
	for _, name := range []string{"google/token", "google/keys", "microsoft/token", "microsoft/keys", "github/token", "github/user", "github/user/emails", "meta/token", "meta/me"} {
		if len(answers[name]) == 0 {
			f.Fatalf("nothing captured of %s", name)
		}
	}
	return answers
}

// refusal fails unless err is one of the two refusals and nothing came with it.
func refusal(t *testing.T, err error, nothing bool, sentinels ...error) {
	t.Helper()
	if !nothing {
		t.Fatalf("something came with %v", err)
	}
	if !slices.ContainsFunc(sentinels, func(sentinel error) bool {
		return errors.Is(err, sentinel) && strings.HasPrefix(err.Error(), sentinel.Error()+": ")
	}) {
		t.Fatalf("refused with %v", err)
	}
}

// shown fails unless an identity is one the rest of the service can keep.
func shown(t *testing.T, id *idp.Identity) {
	t.Helper()
	if id.Subject == "" || len(id.Subject) > 255 || strings.ContainsFunc(id.Subject, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
		t.Fatalf("accepted the subject %q", id.Subject)
	}
	if id.Address != "" {
		if normal, err := domain.NormalizeAddress(id.Address); err != nil || normal != id.Address {
			t.Fatalf("gave the address %q", id.Address)
		}
	}
	if id.Verified && id.Address == "" {
		t.Fatal("verified, and no address")
	}
	if len(id.Name) > idp.MaxName || id.Name != strings.TrimSpace(id.Name) || strings.ContainsFunc(id.Name, func(r rune) bool { return !unicode.IsPrint(r) }) {
		t.Fatalf("gave the name %q", id.Name)
	}
}

// said fails unless accepted claims say what was asked. The package refuses a
// member twice, so reading them into a map here chooses nothing.
func said(t *testing.T, p idp.Provider, application string, payload []byte, id *idp.Identity) {
	t.Helper()
	var claims map[string]any
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&claims); err != nil {
		t.Fatalf("accepted claims that are not JSON: %q", payload)
	}
	exp, _ := claims["exp"].(json.Number)
	iat, _ := claims["iat"].(json.Number)
	expires, err1 := strconv.ParseInt(exp.String(), 10, 64)
	made, err2 := strconv.ParseInt(iat.String(), 10, 64)
	if err1 != nil || err2 != nil || expires <= start.Unix() || made > start.Unix()+60 {
		t.Fatalf("accepted the times %v and %v", claims["exp"], claims["iat"])
	}
	if claims["aud"] != application || claims["nonce"] != fuzzNonce {
		t.Fatalf("accepted the audience %v and the nonce %v", claims["aud"], claims["nonce"])
	}
	if azp, has := claims["azp"]; has && azp != application {
		t.Fatalf("accepted a token given to %v", azp)
	}
	switch p {
	case idp.Google:
		if claims["iss"] != "https://accounts.google.com" && claims["iss"] != "accounts.google.com" {
			t.Fatalf("accepted the issuer %v", claims["iss"])
		}
		if id.Subject != claims["sub"] {
			t.Fatalf("the subject is %q and the token says %v", id.Subject, claims["sub"])
		}
		if id.Verified && claims["email_verified"] != true && claims["email_verified"] != "true" {
			t.Fatalf("verified, and the token says %v", claims["email_verified"])
		}
	case idp.Microsoft:
		tenant, _ := claims["tid"].(string)
		if len(tenant) != 36 || claims["iss"] != "https://login.microsoftonline.com/"+tenant+"/v2.0" {
			t.Fatalf("accepted the issuer %v for the tenant %v", claims["iss"], claims["tid"])
		}
		if oid, has := claims["oid"]; has && id.Subject != tenant+"."+oid.(string) {
			t.Fatalf("the subject is %q", id.Subject)
		}
		if id.Verified && tenant != idp.PersonalTenant && claims["xms_edov"] != true && claims["xms_edov"] != "true" {
			t.Fatalf("verified, in the tenant %v, and the token says %v", tenant, claims["xms_edov"])
		}
	}
}

func provider(microsoft bool) idp.Provider {
	if microsoft {
		return idp.Microsoft
	}
	return idp.Google
}

// FuzzIDToken feeds arbitrary text as the ID token of a provider. It must
// never panic, every refusal must be ErrInvalid, and what it accepts must be
// signed by the provider's key and say what was asked.
func FuzzIDToken(f *testing.F) {
	fake := idptest.New()
	defer fake.Close()
	fake.Now = func() time.Time { return start }
	_, key := fake.SigningKey()
	sets := map[idp.Provider][]byte{}
	for _, p := range signers {
		sets[p] = fake.KeySet(p)
		for _, who := range []idptest.Person{
			{Subject: "583231", Address: "ada@example.com", Verified: true, Name: "Ada Lovelace", Object: object},
			{Subject: "subject", Address: "ada@contoso.com", Verified: true, Tenant: work, Object: object},
			{Subject: "subject", Address: "ada@contoso.com", Tenant: work},
			{Subject: "subject"},
		} {
			fake.SignInAs(p, who)
			f.Add(fake.IDToken(p, fuzzNonce), p == idp.Microsoft)
		}
		for _, fault := range []idptest.Faults{
			{EditHeader: set("alg", "none"), Sign: func(string) []byte { return nil }},
			{EditHeader: set("alg", "HS256")},
			{EditHeader: set("kid", "another-key")},
			{EditHeader: drop("typ")},
			{EditClaims: set("aud", []string{"client-of-google"})},
			{EditClaims: set("email_verified", "true")},
			{EditClaims: set("exp", 1.5e9)},
			{RawClaims: []byte(`{"sub":"a","sub":"b"}`)},
			{RawHeader: []byte(`{"alg":"none","alg":"RS256","kid":"idptest-key-0"}`)},
			{Sign: signWith(otherKey)},
		} {
			fake.Misbehave(p, fault)
			f.Add(fake.IDToken(p, fuzzNonce), p == idp.Microsoft)
		}
		fake.Misbehave(p, idptest.Faults{})
	}
	for _, v := range vectors(f) {
		if v.Kind == "id-token" {
			f.Add(b64([]byte(v.Header))+"."+b64([]byte(v.Claims))+"."+v.Signature, v.Provider == idp.Microsoft)
		}
	}
	f.Add("", false)
	f.Add("..", true)
	f.Add("e30.e30.", false)

	f.Fuzz(func(t *testing.T, token string, microsoft bool) {
		p := provider(microsoft)
		application := fake.App(p).ClientID
		id, err := idp.Verify(p, issuerOf(p), application, fuzzNonce, token, start, sets[p])
		if err != nil {
			refusal(t, err, id == nil, idp.ErrInvalid)
			return
		}
		if id.Provider != p {
			t.Fatalf("an identity of %s", id.Provider)
		}
		shown(t, id)
		parts := strings.Split(token, ".")
		if len(parts) != 3 || len(token) > idp.MaxIDToken {
			t.Fatalf("accepted a token of %d parts and %d bytes", len(parts), len(token))
		}
		// Checked again here, with nothing of the package: the provider's key signed exactly these bytes.
		signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
		hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if err != nil || rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hash[:], signature) != nil {
			t.Fatal("accepted a token the provider's key did not sign")
		}
		var header map[string]any
		raw, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
		if err != nil || json.Unmarshal(raw, &header) != nil || header["alg"] != "RS256" {
			t.Fatalf("accepted the header %q", raw)
		}
		payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
		if err != nil {
			t.Fatal("accepted claims that are not base64url")
		}
		said(t, p, application, payload, id)
	})
}

// FuzzClaims feeds arbitrary bytes as the claims of a token whose signature
// is right, which the fuzzer cannot make by itself. It must never panic,
// every refusal must be ErrInvalid, and what it accepts must say what was asked.
func FuzzClaims(f *testing.F) {
	answers := captured(f)
	for _, name := range []string{"google/token", "microsoft/token"} {
		var answer struct {
			IDToken string `json:"id_token"`
		}
		if err := json.Unmarshal(answers[name], &answer); err != nil {
			f.Fatal(err)
		}
		payload, err := base64.RawURLEncoding.DecodeString(strings.Split(answer.IDToken, ".")[1])
		if err != nil {
			f.Fatal(err)
		}
		// The nonce and the audience are the ones the target asks for.
		var claims map[string]any
		if err := json.Unmarshal(payload, &claims); err != nil {
			f.Fatal(err)
		}
		claims["nonce"] = fuzzNonce
		for _, change := range []func(map[string]any){
			func(map[string]any) {},
			set("email_verified", "true"),
			set("xms_edov", true),
			set("tid", work),
			drop("oid"),
			drop("email"),
			set("aud", []string{"client-of-google"}),
			set("exp", 1.5e9),
			set("name", nil),
		} {
			change(claims)
			seed, _ := json.Marshal(claims)
			f.Add(seed, name == "microsoft/token")
		}
	}
	for _, v := range vectors(f) {
		if v.Kind == "id-token" || v.Kind == "id-token-claims" {
			f.Add([]byte(v.Claims), v.Provider == idp.Microsoft)
		}
	}
	f.Add([]byte(`{}`), false)
	f.Add([]byte(`{"sub":"a","sub":"b"}`), true)
	f.Add([]byte{}, false)

	f.Fuzz(func(t *testing.T, payload []byte, microsoft bool) {
		p := provider(microsoft)
		application := "client-of-" + string(p)
		id, err := idp.Claims(p, issuerOf(p), application, fuzzNonce, payload, start)
		if err != nil {
			refusal(t, err, id == nil, idp.ErrInvalid)
			return
		}
		if id.Provider != p {
			t.Fatalf("an identity of %s", id.Provider)
		}
		shown(t, id)
		said(t, p, application, payload, id)
	})
}

// FuzzKeys feeds arbitrary bytes as the keys a provider publishes.
func FuzzKeys(f *testing.F) {
	answers := captured(f)
	f.Add(answers["google/keys"])
	f.Add(answers["microsoft/keys"])
	for _, v := range vectors(f) {
		if v.Kind == "keys" {
			f.Add([]byte(v.Body))
		}
	}
	f.Add([]byte(`{"keys":[]}`))
	f.Add([]byte(`{"keys":[{"kty":"RSA","kid":"a","n":"AQAB","e":"AQAB"},{"kty":"EC","kid":"a"}]}`))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, body []byte) {
		usable, kept, err := idp.UsableKeys(body)
		if err != nil {
			refusal(t, err, usable == nil && kept == 0, idp.ErrInvalid)
			return
		}
		if len(usable) > kept || kept > idp.MaxKeys || !slices.IsSorted(usable) || len(slices.Compact(slices.Clone(usable))) != len(usable) {
			t.Fatalf("kept %d keys, usable %q", kept, usable)
		}
	})
}

// FuzzTokenAnswer feeds arbitrary bytes as the answer of a token endpoint.
func FuzzTokenAnswer(f *testing.F) {
	answers := captured(f)
	for _, name := range []string{"google/token", "microsoft/token", "github/token", "meta/token"} {
		f.Add(answers[name])
	}
	f.Add([]byte(`{"error":"bad_verification_code","error_description":"The code passed is incorrect or expired."}`))
	f.Add([]byte(`{"access_token":"a","access_token":"b","token_type":"bearer"}`))
	f.Add([]byte(`access_token=abc&token_type=bearer`))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, body []byte) {
		access, id, err := idp.ParseTokenAnswer(body)
		if err != nil {
			refusal(t, err, access == "" && id == "", idp.ErrInvalid, idp.ErrUnreachable)
			return
		}
		// It goes into a header as it is.
		if len(access) > 4096 || strings.ContainsFunc(access, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
			t.Fatalf("accepted the access token %q", access)
		}
	})
}

// FuzzGitHubUser feeds arbitrary bytes as GitHub's answer to GET /user.
func FuzzGitHubUser(f *testing.F) {
	f.Add(captured(f)["github/user"])
	for _, v := range vectors(f) {
		if v.Kind == "github-user" {
			f.Add([]byte(v.Body))
		}
	}
	f.Add([]byte(`{"id":1,"id":2}`))
	f.Add([]byte(`{"id":"1","name":7}`))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, body []byte) {
		id, err := idp.ParseGitHubUser(body)
		if err != nil {
			refusal(t, err, id == nil, idp.ErrInvalid)
			return
		}
		shown(t, id)
		// One number, one way to write it.
		if n, err := strconv.ParseInt(id.Subject, 10, 64); err != nil || n < 1 || strconv.FormatInt(n, 10) != id.Subject {
			t.Fatalf("accepted the identifier %q", id.Subject)
		}
		if id.Provider != idp.GitHub || id.Address != "" || id.Verified {
			t.Fatalf("got %+v from /user alone", id)
		}
	})
}

// FuzzGitHubEmails feeds arbitrary bytes as GitHub's answer to GET /user/emails.
func FuzzGitHubEmails(f *testing.F) {
	f.Add(captured(f)["github/user/emails"])
	for _, v := range vectors(f) {
		if v.Kind == "github-emails" {
			f.Add([]byte(v.Body))
		}
	}
	f.Add([]byte(`[{"email":"a@example.com","primary":true,"verified":true},{"email":"b@example.com","primary":true,"verified":true}]`))
	f.Add([]byte(`[]`))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, body []byte) {
		address, err := idp.ParseGitHubEmails(body)
		if err != nil {
			refusal(t, err, address == "", idp.ErrInvalid)
			return
		}
		if address == "" {
			return
		}
		shown(t, &idp.Identity{Subject: "1", Address: address, Verified: true})
		// The address given is one the answer calls primary and verified. The
		// package refuses a member twice and a second primary, so there is one.
		var entries []struct {
			Email    string
			Primary  bool
			Verified bool
		}
		if err := json.Unmarshal(body, &entries); err != nil {
			t.Fatalf("accepted addresses that are not JSON: %q", body)
		}
		if !slices.ContainsFunc(entries, func(e struct {
			Email    string
			Primary  bool
			Verified bool
		}) bool {
			normal, err := domain.NormalizeAddress(e.Email)
			return err == nil && normal == address && e.Primary && e.Verified
		}) {
			t.Fatalf("gave %q, which the answer does not call primary and verified: %q", address, body)
		}
	})
}

// FuzzMetaUser feeds arbitrary bytes as Meta's answer to GET /me.
func FuzzMetaUser(f *testing.F) {
	f.Add(captured(f)["meta/me"])
	for _, v := range vectors(f) {
		if v.Kind == "meta-user" {
			f.Add([]byte(v.Body))
		}
	}
	f.Add([]byte(`{"id":"1","id":"2"}`))
	f.Add([]byte(`{"error":{"message":"Invalid OAuth access token.","type":"OAuthException","code":190}}`))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, body []byte) {
		id, err := idp.ParseMetaUser(body)
		if err != nil {
			refusal(t, err, id == nil, idp.ErrInvalid, idp.ErrUnreachable)
			return
		}
		shown(t, id)
		if len(id.Subject) > 32 || id.Subject[0] == '0' || strings.ContainsFunc(id.Subject, func(r rune) bool { return r < '0' || r > '9' }) {
			t.Fatalf("accepted the identifier %q", id.Subject)
		}
		// Meta never vouches for an address.
		if id.Provider != idp.Meta || id.Verified {
			t.Fatalf("got %+v", id)
		}
	})
}
