package attestation

import (
	"crypto/sha256"
	"crypto/x509"
)

// UserDataScheme names how Permaily binds TLS to attestation: the document's
// user_data is the SHA-256 of the DER-encoded SubjectPublicKeyInfo of the TLS
// certificate the enclave serves.
const UserDataScheme = "sha256-spki-v1"

// SPKIHash returns the user data the enclave puts in its attestation for a
// given TLS certificate.
func SPKIHash(cert *x509.Certificate) []byte {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return sum[:]
}

// SPKIHashFromDER is SPKIHash for a DER-encoded SubjectPublicKeyInfo.
func SPKIHashFromDER(spki []byte) []byte {
	sum := sha256.Sum256(spki)
	return sum[:]
}
