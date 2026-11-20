package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/nsm/fakensm"
	"github.com/permaily-org/permaily/internal/webhook"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// imageWebhooks is /status's webhooks as the image composes them
// (testdata/webhooks-status.json, which cmd/permaily-enclave's
// TestK58TheWebhooksOnStatus checks is what the image says).
func imageWebhooks(t *testing.T) *attestation.WebhooksStatus {
	t.Helper()
	b, err := os.ReadFile("testdata/webhooks-status.json")
	if err != nil {
		t.Fatal(err)
	}
	var w attestation.WebhooksStatus
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	return &w
}

// K58 (ADR 0039, "What /status and permaily-verify show"): permaily-verify
// passes the image's own section, and fails on each case the design
// names: another signature or revision, mail in a notice, another host, a
// redirect, a transport but HTTP/2, new mail before the rules, more than
// five seconds after a revocation, the key set off the deployment's api
// host; and for another deployment, or none.
func TestK58PermailyVerifyChecksTheWebhooks(t *testing.T) {
	if _, err := checkWebhooks(imageWebhooks(t), "permaily.com"); err != nil {
		t.Fatalf("the image's own: %v", err)
	}
	for name, c := range map[string]struct {
		change func(*attestation.WebhooksStatus)
		says   string
	}{
		"v1a":                       {func(w *attestation.WebhooksStatus) { w.Signature = "standard-webhooks-v1a" }, "signed"},
		"an earlier revision":       {func(w *attestation.WebhooksStatus) { w.SpecRevision = "0123456789abcdef0123456789abcdef01234567" }, "revision"},
		"mail in a notice":          {func(w *attestation.WebhooksStatus) { w.NoticeHoldsMail = true }, "holds mail"},
		"another host":              {func(w *attestation.WebhooksStatus) { w.SameHost = false }, "another host"},
		"a redirect followed":       {func(w *attestation.WebhooksStatus) { w.Redirects = "same_host" }, "redirects"},
		"HTTP/1.1":                  {func(w *attestation.WebhooksStatus) { w.Transport = "http/1.1" }, "not h2"},
		"new mail before the rules": {func(w *attestation.WebhooksStatus) { w.NewMail = "exact" }, "new mail"},
		"six seconds":               {func(w *attestation.WebhooksStatus) { w.RevocationSeconds = 6 }, "6 seconds"},
		"keys on another host": {func(w *attestation.WebhooksStatus) {
			w.KeysURL = "https://keys.permaily.com/.well-known/permaily-webhook-keys"
		}, "published at"},
		"keys on a port": {func(w *attestation.WebhooksStatus) {
			w.KeysURL = "https://api.permaily.com:8443/.well-known/permaily-webhook-keys"
		}, "published at"},
		"another bound": {func(w *attestation.WebhooksStatus) { w.GiveUpSeconds = 3600 }, "bounds"},
		"no key":        {func(w *attestation.WebhooksStatus) { w.Keys = nil }, "no key"},
	} {
		w := imageWebhooks(t)
		c.change(w)
		if _, err := checkWebhooks(w, "permaily.com"); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := checkWebhooks(imageWebhooks(t), "staging.permaily.com"); err == nil {
		t.Fatal("permaily.com's key set passed for another deployment")
	}
	if _, err := checkWebhooks(imageWebhooks(t), ""); err == nil {
		t.Fatal("no domain")
	}
	// checkStatus checks the section whatever is skipped, and keeps it.
	for _, skip := range []bool{false, true} {
		r := checkStatus(&attestation.Status{Commit: "abc", Webhooks: imageWebhooks(t)}, hostResult{URL: "https://api.permaily.com"}, imageA, kmsOptions{now: goldenNow, skip: skip})
		if !r.OK || r.Webhooks == nil || r.Webhooks.Published {
			t.Fatalf("skip %v: %+v", skip, r)
		}
		bad := imageWebhooks(t)
		bad.Transport = "http/1.1"
		r = checkStatus(&attestation.Status{Commit: "abc", Webhooks: bad}, hostResult{URL: "https://api.permaily.com"}, imageA, kmsOptions{now: goldenNow, skip: skip})
		if r.OK || r.Webhooks != nil || !strings.HasPrefix(r.Error, "webhooks: ") {
			t.Fatalf("skip %v, HTTP/1.1: %+v", skip, r)
		}
	}
	if r := checkStatus(&attestation.Status{Commit: "abc"}, hostResult{URL: "https://api.permaily.com"}, imageA, kmsOptions{now: goldenNow, skip: true}); !r.OK || r.Webhooks != nil {
		t.Fatalf("an image without webhooks: %+v", r)
	}
}

// K58: the published key set is fetched at KeysURL over the attested key
// and must be exactly /status's keys: the same passes; another key, a key
// more, a set that is not one read exactly, or no answer fail.
func TestK58ThePublishedKeySetIsStatuss(t *testing.T) {
	w := imageWebhooks(t)
	x, _ := base64.RawURLEncoding.DecodeString(w.Keys[0].X)
	own, err := webhook.KeySet(map[string]ed25519.PublicKey{"wk1": x})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := webhook.KeySet(map[string]ed25519.PublicKey{"wk1": ed25519.PublicKey(bytes.Repeat([]byte{7}, 32))})
	more, _ := webhook.KeySet(map[string]ed25519.PublicKey{"wk1": x, "wk2": ed25519.PublicKey(bytes.Repeat([]byte{7}, 32))})
	spki := []byte("the attested key")
	for name, c := range map[string]struct {
		answer []byte
		err    error
		ok     bool
	}{
		"the same":       {answer: own, ok: true},
		"another key":    {answer: other},
		"a key more":     {answer: more},
		"KEYS":           {answer: bytes.Replace(own, []byte(`"keys"`), []byte(`"KEYS"`), 1)},
		"a stray brace":  {answer: append(append([]byte(nil), own...), '}')},
		"not the key":    {err: attestation.ErrNotBound},
		"a 404":          {err: errors.New("HTTP 404: not found")},
		"an empty set":   {answer: []byte(`{"keys":[]}`)},
		"not even JSON":  {answer: []byte(`<html>`)},
		"the same again": {answer: own, ok: true},
	} {
		r := &webhooksResult{WebhooksStatus: imageWebhooks(t)}
		var asked string
		var askedKey []byte
		fetch := func(_ context.Context, _ *http.Client, url string, key []byte) ([]byte, error) {
			asked, askedKey = url, key
			return c.answer, c.err
		}
		err := checkPublishedKeys(context.Background(), fetch, nil, r, spki)
		if asked != w.KeysURL || !bytes.Equal(askedKey, spki) {
			t.Errorf("%s: asked %s with %q", name, asked, askedKey)
		}
		if (err == nil) != c.ok || r.Published != c.ok {
			t.Errorf("%s: %v, published %v", name, err, r.Published)
		}
	}
}

// K58, end to end: a played enclave at https://api.permaily.com whose
// /status has the section and whose api host publishes a key set.
// permaily-verify passes when the set is /status's, prints the section,
// and fails, naming webhooks, when the published set holds another key.
func TestK58PermailyVerifyComparesThePublishedKeySet(t *testing.T) {
	fake, err := fakensm.New()
	if err != nil {
		t.Fatal(err)
	}
	st := imageWebhooks(t)
	x, _ := base64.RawURLEncoding.DecodeString(st.Keys[0].X)
	own, _ := webhook.KeySet(map[string]ed25519.PublicKey{"wk1": x})
	other, _ := webhook.KeySet(map[string]ed25519.PublicKey{"wk1": ed25519.PublicKey(bytes.Repeat([]byte{9}, 32))})
	var published atomic.Pointer[[]byte]
	published.Store(&own)
	var keysAsked atomic.Int32
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	mux.Handle("/attestation", httpapi.Attestation(fake, func() []byte { return attestation.SPKIHash(srv.Certificate()) }))
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(attestation.Status{Commit: "c", Webhooks: st})
	})
	mux.HandleFunc(webhook.KeysPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "api.permaily.com" {
			http.NotFound(w, r)
			return
		}
		keysAsked.Add(1)
		_, _ = w.Write(*published.Load())
	})
	// A client that reaches the played enclave for every name, trusting its
	// certificate, as a verifier reaches the deployment.
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com"},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
		},
	}}
	opts := kmsOptions{skip: true, now: time.Now(), root: fake.CA.Root}
	r := verify(context.Background(), client, "https://api.permaily.com", fake.PCRs, opts)
	if !r.OK || r.Webhooks == nil || !r.Webhooks.Published || keysAsked.Load() != 1 {
		t.Fatalf("the same key set: %+v (asked %d)", r, keysAsked.Load())
	}
	var out bytes.Buffer
	printHuman(&out, "test", fake.PCRs, []hostResult{r})
	for _, want := range []string{"webhooks: notices signed standard-webhooks-v2a (Standard Webhooks at 3d65b18", "wk1 " + st.Keys[0].X,
		"the same keys, fetched over the attested key", "over h2; new mail after rules; stopped within 5s", "calls none; notices not sent none"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the print does not say %q:\n%s", want, out.String())
		}
	}
	published.Store(&other)
	r = verify(context.Background(), client, "https://api.permaily.com", fake.PCRs, opts)
	if r.OK || r.Webhooks != nil || !strings.HasPrefix(r.Error, "webhooks: ") || !strings.Contains(r.Error, "not /status's") {
		t.Fatalf("another key set: %+v", r)
	}
}
