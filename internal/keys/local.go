package keys

import (
	"context"
	"crypto/rand"
	"maps"
	"slices"

	"github.com/permaily-org/permaily/internal/envelope"
)

// Local wraps data keys with an in-memory master key. For dev mode and tests
// only: it gives no protection beyond the process.
type Local struct {
	master []byte
}

// NewLocal returns a Local service with a random master key.
func NewLocal() (*Local, error) {
	m := make([]byte, envelope.KeySize)
	if _, err := rand.Read(m); err != nil {
		return nil, err
	}
	return &Local{master: m}, nil
}

func contextAAD(c map[string]string) []byte {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(c)) {
		parts = append(parts, k, c[k])
	}
	return envelope.AAD(parts...)
}

// NewDataKey implements Service.
func (l *Local) NewDataKey(_ context.Context, c map[string]string) (DataKey, error) {
	dk := make([]byte, envelope.KeySize)
	if _, err := rand.Read(dk); err != nil {
		return DataKey{}, err
	}
	wrapped, err := envelope.Seal(l.master, dk, contextAAD(c))
	if err != nil {
		return DataKey{}, err
	}
	return DataKey{Plaintext: dk, Wrapped: wrapped}, nil
}

// Unwrap implements Service.
func (l *Local) Unwrap(_ context.Context, wrapped []byte, c map[string]string) ([]byte, error) {
	return envelope.Open(l.master, wrapped, contextAAD(c))
}
