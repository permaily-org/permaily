package attestation_test

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
)

// TestVerifyRealDocument checks a document produced by the Nitro Secure Module
// of the first production deployment (release v0.2.0, 2026-11-10) against the
// embedded AWS root, the release's PCRs and the TLS certificate the enclave
// served on that connection. Everything in testdata is public.
func TestVerifyRealDocument(t *testing.T) {
	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(read("v0.2.0-attestation.b64"))))
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := hex.DecodeString(strings.TrimSpace(string(read("v0.2.0-nonce.hex"))))
	if err != nil {
		t.Fatal(err)
	}
	pcrs, err := attestation.ParsePCRFile(read("v0.2.0-pcrs.json"))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(read("v0.2.0-tls-leaf.pem"))
	if block == nil {
		t.Fatal("no certificate in the TLS leaf fixture")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	// The document's own timestamp, so the test does not depend on the clock.
	at := time.Date(2026, 11, 10, 3, 43, 50, 0, time.UTC)
	opts := attestation.Options{Now: at, Nonce: nonce, UserData: attestation.SPKIHash(leaf), ExpectedPCRs: pcrs}

	doc, err := attestation.Verify(raw, opts)
	if err != nil {
		t.Fatalf("real document rejected: %v", err)
	}
	if !strings.HasPrefix(doc.ModuleID, "i-") || doc.Timestamp.Sub(at).Abs() > time.Minute {
		t.Fatalf("unexpected module %q or timestamp %s", doc.ModuleID, doc.Timestamp)
	}

	cases := []struct {
		name string
		opts func(o attestation.Options) attestation.Options
		want error
	}{
		{"another nonce", func(o attestation.Options) attestation.Options {
			o.Nonce = []byte("a different nonce, 32 bytes long")
			return o
		}, attestation.ErrNonce},
		{"another TLS key", func(o attestation.Options) attestation.Options {
			o.UserData = make([]byte, 32)
			return o
		}, attestation.ErrUserData},
		{"another image", func(o attestation.Options) attestation.Options {
			o.ExpectedPCRs = map[uint][]byte{0: make([]byte, 48)}
			return o
		}, attestation.ErrPCRMismatch},
		{"one day later", func(o attestation.Options) attestation.Options {
			o.Now = at.Add(24 * time.Hour)
			return o
		}, attestation.ErrStale},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := attestation.Verify(raw, c.opts(opts)); !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}

	t.Run("tampered", func(t *testing.T) {
		bad := append([]byte(nil), raw...)
		bad[len(bad)/2] ^= 0x01
		if _, err := attestation.Verify(bad, opts); err == nil {
			t.Fatal("tampered document accepted")
		}
	})
}
