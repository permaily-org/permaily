package attestation_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
	"github.com/permaily-org/permaily/pkg/attestation/attestationtest"
)

func TestAWSRootFingerprint(t *testing.T) {
	sum := sha256.Sum256(attestation.AWSRoot().Raw)
	if got := hex.EncodeToString(sum[:]); got != attestation.AWSRootFingerprint {
		t.Fatalf("embedded AWS root fingerprint = %s, want %s", got, attestation.AWSRootFingerprint)
	}
}

func TestVerify(t *testing.T) {
	ca, err := attestationtest.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	nonce := bytes.Repeat([]byte{0x01}, 32)
	userData := bytes.Repeat([]byte{0x02}, 32)
	pcrs := attestationtest.PCRs(0xab)
	now := time.Now()

	sign := func(t *testing.T, f attestationtest.Fields) []byte {
		t.Helper()
		doc, err := ca.Sign(f)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	good := attestationtest.Fields{PCRs: pcrs, Nonce: nonce, UserData: userData, Timestamp: now}
	opts := attestation.Options{Root: ca.Root, Now: now, Nonce: nonce, UserData: userData, ExpectedPCRs: pcrs}

	t.Run("valid", func(t *testing.T) {
		doc, err := attestation.Verify(sign(t, good), opts)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(doc.PCRs[0], pcrs[0]) || !bytes.Equal(doc.UserData, userData) {
			t.Fatal("document fields not returned")
		}
	})

	cases := []struct {
		name   string
		fields func(f attestationtest.Fields) attestationtest.Fields
		opts   func(o attestation.Options) attestation.Options
		raw    func(b []byte) []byte
		want   error
	}{
		{name: "wrong nonce", opts: func(o attestation.Options) attestation.Options {
			o.Nonce = bytes.Repeat([]byte{0x09}, 32)
			return o
		}, want: attestation.ErrNonce},
		{name: "wrong user data (TLS key not bound)", opts: func(o attestation.Options) attestation.Options {
			o.UserData = bytes.Repeat([]byte{0x09}, 32)
			return o
		}, want: attestation.ErrUserData},
		{name: "debug mode zero PCRs", fields: func(f attestationtest.Fields) attestationtest.Fields {
			f.PCRs = attestationtest.PCRs(0)
			return f
		}, opts: func(o attestation.Options) attestation.Options {
			o.ExpectedPCRs = nil
			return o
		}, want: attestation.ErrDebugMode},
		{name: "different image", opts: func(o attestation.Options) attestation.Options {
			o.ExpectedPCRs = attestationtest.PCRs(0xcd)
			return o
		}, want: attestation.ErrPCRMismatch},
		{name: "stale document", fields: func(f attestationtest.Fields) attestationtest.Fields {
			f.Timestamp = now.Add(-10 * time.Minute)
			return f
		}, want: attestation.ErrStale},
		{name: "untrusted root (default AWS root)", opts: func(o attestation.Options) attestation.Options {
			o.Root = nil
			return o
		}, want: attestation.ErrChain},
		{name: "tampered payload", raw: func(b []byte) []byte {
			c := bytes.Clone(b)
			i := bytes.Index(c, nonce)
			c[i] ^= 0xff
			return c
		}, want: attestation.ErrSignature},
		{name: "garbage", raw: func([]byte) []byte { return []byte{0x01, 0x02} }, want: attestation.ErrMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, o := good, opts
			if tc.fields != nil {
				f = tc.fields(f)
			}
			if tc.opts != nil {
				o = tc.opts(o)
			}
			raw := sign(t, f)
			if tc.raw != nil {
				raw = tc.raw(raw)
			}
			_, err := attestation.Verify(raw, o)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestParsePCRFile(t *testing.T) {
	h := hex.EncodeToString(bytes.Repeat([]byte{0xab}, 48))
	for _, in := range []string{
		`{"HashAlgorithm":"Sha384 { ... }","PCR0":"` + h + `","PCR1":"` + h + `","PCR2":"` + h + `"}`,
		`{"Measurements":{"PCR0":"` + h + `","PCR1":"` + h + `","PCR2":"` + h + `"}}`,
	} {
		pcrs, err := attestation.ParsePCRFile([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		if len(pcrs) != 3 || pcrs[2][0] != 0xab {
			t.Fatalf("unexpected pcrs %v", pcrs)
		}
	}
	if _, err := attestation.ParsePCRFile([]byte(`{"PCR0":"00"}`)); err == nil {
		t.Fatal("expected error for incomplete file")
	}
}
