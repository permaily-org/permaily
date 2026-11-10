//go:build production

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
)

// TestProductionMITM puts a TLS-terminating proxy between the verifier and the
// live enclave, the way the parent instance or a CDN could, and checks that
// verification fails. The proxy forwards every request untouched, so the
// attestation document it relays is genuine and fresh; what gives it away is
// that the document vouches for the enclave's TLS key, not the proxy's.
//
// It talks to the deployed service, so it is not part of the normal test run:
//
//	PERMAILY_PCRS=result/pcrs.json go test -tags production -run TestProductionMITM -v ./cmd/permaily-verify
//
// Without PERMAILY_PCRS it uses the pcrs.json of PERMAILY_RELEASE (a tag).
// PERMAILY_DOMAIN changes the domain (default permaily.com).
func TestProductionMITM(t *testing.T) {
	expected, source, err := loadPCRs(os.Getenv("PERMAILY_PCRS"), os.Getenv("PERMAILY_RELEASE"))
	if err != nil {
		t.Fatalf("set PERMAILY_PCRS or PERMAILY_RELEASE: %v", err)
	}
	domain := os.Getenv("PERMAILY_DOMAIN")
	if domain == "" {
		domain = "permaily.com"
	}
	target, err := url.Parse("https://app." + domain)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("expected measurements: %s", source)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	opts := attestation.Options{ExpectedPCRs: expected}

	// Control: the same check, straight to the enclave, passes. Without this a
	// failure below could be a wrong pcrs.json and prove nothing.
	direct, err := attestation.VerifyRemote(ctx, http.DefaultClient, target.String(), opts)
	if err != nil {
		t.Fatalf("direct verification of %s failed, so the test proves nothing: %v", target, err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Host = target.Host
	}
	mitm := httptest.NewTLSServer(proxy)
	defer mitm.Close()
	client := mitm.Client() // trusts the proxy's certificate, as a browser trusts any valid one

	t.Run("attestation relayed by the proxy", func(t *testing.T) {
		_, err := attestation.VerifyRemote(ctx, client, mitm.URL, opts)
		if !errors.Is(err, attestation.ErrUserData) {
			t.Fatalf("got %v, want ErrUserData: the document is bound to the enclave's key, not the proxy's", err)
		}
	})
	t.Run("permaily-verify through the proxy", func(t *testing.T) {
		r := verify(ctx, client, mitm.URL, expected, kmsOptions{now: time.Now()})
		if r.OK || !strings.Contains(r.Error, "user data") {
			t.Fatalf("result %+v, want a user data failure", r)
		}
	})
	t.Run("status relayed by the proxy", func(t *testing.T) {
		// Even holding a valid attestation from the real enclave, what the proxy
		// serves is not accepted as coming from it.
		if _, err := attestation.FetchStatus(ctx, client, mitm.URL, direct.SPKIHash); !errors.Is(err, attestation.ErrNotBound) {
			t.Fatalf("got %v, want ErrNotBound", err)
		}
		if _, err := attestation.FetchMTASTS(ctx, client, mitm.URL, direct.SPKIHash); !errors.Is(err, attestation.ErrNotBound) {
			t.Fatalf("MTA-STS policy: got %v, want ErrNotBound", err)
		}
	})
}
