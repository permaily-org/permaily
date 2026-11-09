// Package keys creates and unwraps per-mailbox data keys.
//
// In the enclave, data keys come from AWS KMS with the Recipient parameter:
// KMS encrypts the plaintext key to an RSA key that only exists inside this
// enclave, after checking the enclave's attestation document against the key
// policy (PCR0 allowlist). Neither the parent instance nor anyone holding AWS
// credentials alone can obtain a plaintext data key.
package keys

import (
	"context"
	"errors"
)

// ErrKeyMismatch means KMS answered for a key other than the pinned one, which
// could mean the untrusted host supplied credentials for a different account.
var ErrKeyMismatch = errors.New("keys: KMS response for an unexpected key")

// DataKey is a fresh data key: Plaintext stays in memory, Wrapped is stored.
type DataKey struct {
	Plaintext []byte
	Wrapped   []byte
}

// Service creates and unwraps data keys. context is the KMS encryption
// context; the same context must be given to Unwrap.
type Service interface {
	NewDataKey(ctx context.Context, context map[string]string) (DataKey, error)
	Unwrap(ctx context.Context, wrapped []byte, context map[string]string) ([]byte, error)
}
