package attestation_test

import (
	"testing"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
	"github.com/permaily-org/permaily/pkg/attestation/attestationtest"
)

// FuzzVerify feeds arbitrary bytes to the parser and verifier. It must never
// panic, and it must never accept a document: the trusted root is a fresh test
// CA whose key the fuzzer cannot use to sign.
func FuzzVerify(f *testing.F) {
	ca, err := attestationtest.NewCA()
	if err != nil {
		f.Fatal(err)
	}
	other, err := attestationtest.NewCA()
	if err != nil {
		f.Fatal(err)
	}
	seed, err := ca.Sign(attestationtest.Fields{PCRs: attestationtest.PCRs(0xab), Nonce: []byte("nonce"), UserData: []byte("ud")})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte{})
	f.Add([]byte{0x84, 0x40, 0xa0, 0x40, 0x40})
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, err := attestation.Verify(raw, attestation.Options{Root: other.Root, Now: time.Now()})
		if err == nil {
			t.Fatalf("accepted a document not signed under the trusted root: %x", raw)
		}
	})
}

// FuzzParsePCRFile must never panic, and accepted files always hold PCR0-2 of 48 bytes.
func FuzzParsePCRFile(f *testing.F) {
	f.Add([]byte(`{"PCR0":"00","PCR1":"00","PCR2":"00"}`))
	f.Add([]byte(`{"Measurements":{}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		pcrs, err := attestation.ParsePCRFile(data)
		if err != nil {
			return
		}
		for _, i := range attestation.MeasuredPCRs {
			if len(pcrs[i]) != 48 {
				t.Fatalf("accepted PCR%d of length %d", i, len(pcrs[i]))
			}
		}
	})
}
