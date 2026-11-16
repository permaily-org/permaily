package connections

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
)

// The stand-ins behave as pull request 1's functions as merged (#133): each
// difference the review of pull request 136 listed, on its own input. A
// differential run against internal/oauth and internal/jws themselves is in
// the pull request's description.
func TestTheStandInsAreTheMergedCore(t *testing.T) {
	refused := func(err error, code oauth.ErrorCode, reason string) bool {
		var r *oauthRefusal
		return errors.As(err, &r) && r.Code == code && (reason == "" || r.Reason == reason)
	}

	t.Run("family secrets", func(t *testing.T) {
		key := []byte("synthetic")
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte("permaily oauth family token v1\x00access\x00fam_test\x00"))
		mac.Write(binary.BigEndian.AppendUint64(nil, 3))
		if got := familySecret(key, oauth.KindAccess, "fam_test", 3); got != oauth.PrefixAccess+base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) {
			t.Fatal(got)
		}
		if s := familySecret(key, oauth.KindCode, "fam_test", 3); strings.HasPrefix(s, oauth.PrefixAccess) || len(s) != 43 {
			t.Fatal(s)
		}
	})

	t.Run("scopes", func(t *testing.T) {
		for _, s := range []string{"", "mail.sender mail.sender", "mail.sender\tmail.subject", " mail.subject", "mail.subject  mail.body"} {
			if _, err := parseScopes(s, true); !refused(err, oauth.ErrInvalidScope, "") {
				t.Errorf("%q: %v", s, err)
			}
		}
	})

	t.Run("redirect URIs", func(t *testing.T) {
		for got, want := range map[string]bool{
			"http://localhost:8080/callback": true, "http://localhost:0/callback": false, "http://localhost:65536/callback": false,
			"http://localhost:0123/callback": false, "http://localhost:/callback": false, "http://localhost/callback#": false,
			"http://localhost/callback?": false, "https://localhost/callback": false, "https://ex%61mple.org/cb": false,
		} {
			if redirectAllowed([]string{loopback, "https://ex%61mple.org/cb", "https://localhost/callback"}, got) != want {
				t.Errorf("%s: want %t", got, want)
			}
		}
		if got := redirectWith("https://example.org/cb?x=a%20b&iss=prior", url.Values{"code": {"c"}}, issuer); got != "https://example.org/cb?x=a%20b&iss=prior&code=c&iss=https%3A%2F%2Fapi.permaily.test" {
			t.Fatal(got)
		}
	})

	t.Run("the authorization request", func(t *testing.T) {
		w := newWorld(t)
		doc := &w.vendors.clients[claudeCode].Document
		with := func(change url.Values) url.Values {
			q, _ := url.ParseQuery(w.ask(claudeCode).query())
			for k, v := range change {
				q[k] = v
			}
			return q
		}
		if _, proven, err := authorizeParams(with(url.Values{"client_id": {"https://other.example/c"}}), doc, oauthConfig); proven || !refused(err, oauth.ErrInvalidClient, string(contracts.ClientIDInvalid)) {
			t.Fatal(proven, err)
		}
		if _, proven, err := authorizeParams(with(url.Values{"response_mode": {"fragment"}}), doc, oauthConfig); !proven || !refused(err, oauth.ErrInvalidRequest, "") {
			t.Fatal(proven, err)
		}
		if p, _, err := authorizeParams(with(url.Values{"ends_in_days": {"30"}, "query": {"{}"}, "mail_since": {"new"}}), doc, oauthConfig); err != nil || p.EndsInDays != 0 || p.Query != nil || p.Since != "" {
			t.Fatal(p, err)
		}
		if p, _, err := authorizeParams(with(url.Values{"state": {"first", "second"}}), doc, oauthConfig); p.State != "" || !refused(err, oauth.ErrInvalidRequest, "") {
			t.Fatal(p, err)
		}
		long := strings.Repeat("s", oauth.StateMaxBytes+1)
		if p, _, err := authorizeParams(with(url.Values{"state": {long}, "response_type": {"token"}}), doc, oauthConfig); p.State != "" || p.ResponseType != "token" || !refused(err, oauth.ErrUnsupportedResponse, "") {
			t.Fatal(p, err)
		}
		other := &w.vendors.clients[otherApp].Document
		push := func(change url.Values) error {
			q, _ := url.ParseQuery(w.ask(otherApp).query())
			for k, v := range change {
				q[k] = v
			}
			_, err := pushedParams(q, other, oauthConfig)
			return err
		}
		for name, c := range map[string]url.Values{
			"a query that is not a formula": {"query": {`{"v":999,"all":[]}`}}, "01 days": {"ends_in_days": {"01"}}, "+1 days": {"ends_in_days": {"+1"}},
			"a form too long": {"unknown": {strings.Repeat("x", oauth.FormMaxBytes)}}, "two assertions": {"client_assertion": {"a", "b"}},
		} {
			if err := push(c); !refused(err, oauth.ErrInvalidRequest, "") {
				t.Errorf("%s: %v", name, err)
			}
		}
		if err := push(url.Values{"redirect_uri": {"https://not-listed.example/c"}}); !refused(err, oauth.ErrInvalidClient, string(contracts.RedirectURIInvalid)) {
			t.Fatal(err)
		}
		if err := push(url.Values{"client_id": {chatgpt}}); !refused(err, oauth.ErrInvalidClient, string(contracts.ClientIDInvalid)) {
			t.Fatal(err)
		}
	})

	t.Run("client assertions", func(t *testing.T) {
		w := newWorld(t)
		doc := w.vendors.clients[chatgpt].Document
		keys := w.vendors.keys[chatgpt]
		now := w.clock.now()
		sign := func(h, c map[string]any) string {
			hb, _ := json.Marshal(h)
			cb, _ := json.Marshal(c)
			signing := b64.EncodeToString(hb) + "." + b64.EncodeToString(cb)
			digest := sha256.Sum256([]byte(signing))
			sig, _ := rsa.SignPKCS1v15(rand.Reader, w.jwtKey, crypto.SHA256, digest[:])
			return signing + "." + b64.EncodeToString(sig)
		}
		claims := func(k string, v any) map[string]any {
			c := map[string]any{"iss": chatgpt, "sub": chatgpt, "aud": issuer, "exp": now.Add(time.Minute).Unix(), "iat": now.Unix(), "jti": "unique"}
			if k == "JTI" {
				delete(c, "jti")
			}
			if k != "" {
				c[k] = v
			}
			return c
		}
		head := func(k string, v any) map[string]any {
			h := map[string]any{"alg": "RS256", "kid": "k1"}
			if k != "" {
				h[k] = v
			}
			return h
		}
		if _, _, err := verifyAssertion(&doc, keys, sign(head("", nil), claims("", nil)), []string{issuer}, now); err != nil {
			t.Fatal(err)
		}
		for name, j := range map[string]string{
			"crit":               sign(head("crit", []string{"x"}), claims("", nil)),
			"a typ not text":     sign(head("typ", 7), claims("", nil)),
			"nbf ahead":          sign(head("", nil), claims("nbf", now.Add(time.Hour).Unix())),
			"iat null":           sign(head("", nil), claims("iat", nil)),
			"exp with fraction":  sign(head("", nil), claims("exp", float64(now.Add(time.Minute).Unix())+0.5)),
			"JTI not jti":        sign(head("", nil), claims("JTI", "unique")),
			"too long":           sign(head("", nil), claims("x", strings.Repeat("x", 17000))),
			"an aud with a null": sign(head("", nil), claims("aud", []any{nil, issuer})),
		} {
			if _, _, err := verifyAssertion(&doc, keys, j, []string{issuer}, now); !refused(err, oauth.ErrInvalidClient, reasonAssertion) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if _, _, err := verifyAssertion(&doc, keys, sign(head("kid", "k9"), claims("", nil)), []string{issuer}, now); !errors.Is(err, errUnknownKey) {
			t.Fatal(err)
		}
		if _, _, err := verifyAssertion(&doc, keys, sign(head("kid", "e1"), claims("", nil)), []string{issuer}, now); errors.Is(err, errUnknownKey) || err == nil {
			t.Fatal("a key of another algorithm is not refreshed for", err)
		}
		public := w.vendors.clients[claudeCode].Document
		if _, _, err := verifyAssertion(&public, keys, sign(head("", nil), claims("", nil)), []string{issuer}, now); !refused(err, oauth.ErrInvalidClient, reasonAssertion) {
			t.Fatal(err)
		}
	})

	t.Run("each call site converts every refusal, failing closed", func(t *testing.T) {
		if oauthCode(endpointError(errors.New("anything"))) != oauth.ErrInvalidRequest || oauthCode(endpointError(refuseClient(contracts.RedirectURIInvalid))) != oauth.ErrInvalidClient {
			t.Fatal("endpoint")
		}
		if shownClient(errors.New("anything")).Problem != contracts.RedirectURIInvalid || shownClient(refuseOAuth(oauth.ErrInvalidRequest)).Problem != contracts.RedirectURIInvalid ||
			shownClient(refuseClient(contracts.ClientIDInvalid)).Problem != contracts.ClientIDInvalid {
			t.Fatal("shown")
		}
		if redirectCode(errors.New("anything")) != oauth.ErrServerError || redirectCode(refuseOAuth(oauth.ErrInvalidScope)) != oauth.ErrInvalidScope {
			t.Fatal("redirect")
		}
	})
}
