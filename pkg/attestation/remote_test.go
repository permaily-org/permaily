package attestation_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/nsm/fakensm"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// enclaveServer starts a TLS server that serves /attestation bound to its own
// certificate, like permaily-enclave does.
func enclaveServer(t *testing.T, att *fakensm.Fake) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	srv.StartTLS()
	leaf := srv.Certificate()
	srv.Config.Handler = httpapi.Attestation(att, func() []byte { return attestation.SPKIHash(leaf) })
	t.Cleanup(srv.Close)
	return srv
}

func clientTrusting(certs ...*x509.Certificate) *http.Client {
	pool := x509.NewCertPool()
	for _, c := range certs {
		pool.AddCert(c)
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
}

func TestVerifyRemote(t *testing.T) {
	att, err := fakensm.New()
	if err != nil {
		t.Fatal(err)
	}
	enclave := enclaveServer(t, att)
	opts := attestation.Options{Root: att.CA.Root, ExpectedPCRs: att.PCRs}

	t.Run("direct connection to enclave", func(t *testing.T) {
		res, err := attestation.VerifyRemote(context.Background(), clientTrusting(enclave.Certificate()), enclave.URL, opts)
		if err != nil {
			t.Fatal(err)
		}
		if res.Document == nil || len(res.SPKIHash) != 32 {
			t.Fatal("incomplete result")
		}
	})

	t.Run("TLS terminated by someone else (MITM)", func(t *testing.T) {
		// A proxy with its own certificate relays requests to the enclave.
		// The attestation is genuine but bound to the enclave's key, not the
		// proxy's, so verification must fail.
		target, _ := url.Parse(enclave.URL)
		rp := httputil.NewSingleHostReverseProxy(target)
		rp.Transport = clientTrusting(enclave.Certificate()).Transport
		// httptest reuses one built-in key for every server, so give the
		// proxy a key of its own.
		mitm := httptest.NewUnstartedServer(rp)
		mitm.TLS = &tls.Config{Certificates: []tls.Certificate{selfSigned(t)}}
		mitm.StartTLS()
		defer mitm.Close()
		_, err := attestation.VerifyRemote(context.Background(), clientTrusting(mitm.Certificate()), mitm.URL, opts)
		if !errors.Is(err, attestation.ErrUserData) {
			t.Fatalf("err = %v, want ErrUserData", err)
		}
	})

	t.Run("fake attester rejected against AWS root", func(t *testing.T) {
		o := opts
		o.Root = nil
		_, err := attestation.VerifyRemote(context.Background(), clientTrusting(enclave.Certificate()), enclave.URL, o)
		if !errors.Is(err, attestation.ErrChain) {
			t.Fatalf("err = %v, want ErrChain", err)
		}
	})
}

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}
