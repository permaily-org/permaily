package attestation_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/smtpd"
	"github.com/permaily-org/permaily/pkg/attestation"
)

const policyText = "version: STSv1\nmode: enforce\nmx: Inbox.Test\nmax_age: 604800\n"

func TestParseMTASTS(t *testing.T) {
	p, err := attestation.ParseMTASTS("version: STSv1\r\nmode: testing\r\nmx: a.test\r\nmx: *.b.test\r\nmax_age: 86400\r\nfuture: x\r\n\r\nnot a field\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != "testing" || !slices.Equal(p.MX, []string{"a.test", "*.b.test"}) || p.MaxAge != 86400 {
		t.Fatalf("policy %+v", p)
	}
	for name, text := range map[string]string{
		"empty":         "",
		"html":          "<html>not found</html>",
		"wrong version": "version: STSv2\nmode: enforce\n",
		"no mode":       "version: STSv1\nmx: a.test\n",
		"bad max_age":   "version: STSv1\nmode: enforce\nmax_age: soon\n",
		"negative":      "version: STSv1\nmode: enforce\nmax_age: -1\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := attestation.ParseMTASTS(text); !errors.Is(err, attestation.ErrMalformed) {
				t.Fatalf("got %v, want ErrMalformed", err)
			}
		})
	}
}

func TestFetchMTASTS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/mta-sts.txt" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(policyText))
	}))
	defer srv.Close()
	client := clientTrusting(srv.Certificate())

	p, err := attestation.FetchMTASTS(context.Background(), client, srv.URL, attestation.SPKIHash(srv.Certificate()))
	if err != nil || p.Mode != "enforce" || !slices.Equal(p.MX, []string{"inbox.test"}) {
		t.Fatalf("got %+v, %v", p, err)
	}
	// A policy from a server that does not hold the attested key says nothing about the enclave.
	if _, err := attestation.FetchMTASTS(context.Background(), client, srv.URL, make([]byte, 32)); !errors.Is(err, attestation.ErrNotBound) {
		t.Fatalf("got %v, want ErrNotBound", err)
	}
}

type discard struct{}

func (discard) Accept(context.Context, string) (string, error) {
	return "", errors.New("the check must not send mail")
}
func (discard) Deliver(context.Context, string, []string, []byte) error {
	return errors.New("the check must not send mail")
}

// smtpServer starts an MX like the enclave's; cert nil means no STARTTLS.
func smtpServer(t *testing.T, cert *tls.Certificate) string {
	t.Helper()
	cfg := smtpd.Config{Domain: "inbox.test"}
	if cert != nil {
		cfg.TLS = &tls.Config{Certificates: []tls.Certificate{*cert}}
	}
	srv := smtpd.New(discard{}, cfg, slog.New(slog.DiscardHandler))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func certFor(t *testing.T, name string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3), DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

func TestVerifySMTP(t *testing.T) {
	cert, pool := certFor(t, "inbox.test")
	addr := smtpServer(t, &cert)
	attested := attestation.SPKIHash(cert.Leaf)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Run("the MX holds the attested key", func(t *testing.T) {
		if err := attestation.VerifySMTP(ctx, nil, addr, "inbox.test", pool, attested); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("no deadline in the context", func(t *testing.T) {
		if err := attestation.VerifySMTP(context.Background(), nil, addr, "inbox.test", pool, attested); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("someone else answers on port 25 with a valid certificate", func(t *testing.T) {
		other, otherPool := certFor(t, "inbox.test")
		err := attestation.VerifySMTP(ctx, nil, smtpServer(t, &other), "inbox.test", otherPool, attested)
		if !errors.Is(err, attestation.ErrNotBound) {
			t.Fatalf("got %v, want ErrNotBound", err)
		}
	})
	t.Run("certificate a sender would not accept", func(t *testing.T) {
		_, strangers := certFor(t, "inbox.test")
		if err := attestation.VerifySMTP(ctx, nil, addr, "inbox.test", strangers, attested); err == nil || errors.Is(err, attestation.ErrNotBound) {
			t.Fatalf("got %v, want a TLS error", err)
		}
	})
	t.Run("certificate for another name", func(t *testing.T) {
		if err := attestation.VerifySMTP(ctx, nil, addr, "other.test", pool, attested); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("STARTTLS stripped", func(t *testing.T) {
		if err := attestation.VerifySMTP(ctx, nil, smtpServer(t, nil), "inbox.test", pool, attested); !errors.Is(err, attestation.ErrNoSTARTTLS) {
			t.Fatalf("got %v, want ErrNoSTARTTLS", err)
		}
	})
	t.Run("port closed", func(t *testing.T) {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		closed := ln.Addr().String()
		_ = ln.Close()
		if err := attestation.VerifySMTP(ctx, nil, closed, "inbox.test", pool, attested); !errors.Is(err, attestation.ErrSMTPUnreachable) {
			t.Fatalf("got %v, want ErrSMTPUnreachable", err)
		}
	})
	t.Run("not an SMTP server", func(t *testing.T) {
		web := httptest.NewServer(http.NotFoundHandler())
		defer web.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := attestation.VerifySMTP(ctx, nil, web.Listener.Addr().String(), "inbox.test", pool, attested)
		if err == nil || errors.Is(err, attestation.ErrSMTPUnreachable) {
			t.Fatalf("got %v, want a protocol error", err)
		}
	})
}
