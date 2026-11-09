// Package fakensm is an nsm.Attester for local development and tests.
package fakensm

import (
	"github.com/permaily-org/permaily/pkg/attestation/attestationtest"
)

// Fake signs attestation documents with a throwaway chain. Its documents
// never verify against the AWS root, so a verifier always rejects them; it
// exists for local development and tests.
type Fake struct {
	CA   *attestationtest.CA
	PCRs map[uint][]byte
}

// New returns a Fake whose PCRs are all 0xee.
func New() (*Fake, error) {
	ca, err := attestationtest.NewCA()
	if err != nil {
		return nil, err
	}
	return &Fake{CA: ca, PCRs: attestationtest.PCRs(0xee)}, nil
}

// Attest implements nsm.Attester.
func (f *Fake) Attest(nonce, userData []byte) ([]byte, error) {
	return f.CA.Sign(attestationtest.Fields{PCRs: f.PCRs, Nonce: nonce, UserData: userData})
}
