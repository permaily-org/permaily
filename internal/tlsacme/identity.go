package tlsacme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/store"
)

// Identity is what the enclave needs to keep presenting the same TLS key and
// the same ACME account across restarts and upgrades.
type Identity struct {
	// AccountKey signs requests to the ACME CA. CAA records can pin issuance to
	// the account it belongs to (AccountURI).
	AccountKey *ecdsa.PrivateKey
	// Key is the certificate key; its SPKI hash is bound in attestation documents.
	Key *ecdsa.PrivateKey
	// CreatedAt is when the keys were generated, inside an enclave.
	CreatedAt time.Time
	// AccountURI is the ACME account URL, known after the first registration.
	AccountURI string
	// Chain is the last certificate chain issued (DER, leaf first), reused
	// until it is due for renewal so a restart does not ask for a new one.
	Chain [][]byte
}

// NewIdentity generates fresh keys.
func NewIdentity(now time.Time) (*Identity, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	account, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Identity{AccountKey: account, Key: key, CreatedAt: now.UTC().Truncate(time.Second)}, nil
}

type identityJSON struct {
	Version    int       `json:"version"`
	AccountKey []byte    `json:"account_key"` // PKCS #8
	Key        []byte    `json:"key"`         // PKCS #8
	CreatedAt  time.Time `json:"created_at"`
	AccountURI string    `json:"account_uri,omitempty"`
	Chain      [][]byte  `json:"chain,omitempty"`
}

func (id *Identity) marshal() ([]byte, error) {
	if id.AccountKey == nil || id.Key == nil {
		return nil, errors.New("tlsacme: identity without keys")
	}
	account, err := x509.MarshalPKCS8PrivateKey(id.AccountKey)
	if err != nil {
		return nil, err
	}
	key, err := x509.MarshalPKCS8PrivateKey(id.Key)
	if err != nil {
		return nil, err
	}
	return json.Marshal(identityJSON{Version: 1, AccountKey: account, Key: key, CreatedAt: id.CreatedAt, AccountURI: id.AccountURI, Chain: id.Chain})
}

func unmarshalIdentity(b []byte) (*Identity, error) {
	var j identityJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, fmt.Errorf("tlsacme: identity: %w", err)
	}
	if j.Version != 1 {
		return nil, fmt.Errorf("tlsacme: identity version %d", j.Version)
	}
	parse := func(der []byte) (*ecdsa.PrivateKey, error) {
		k, err := x509.ParsePKCS8PrivateKey(der)
		if err != nil {
			return nil, fmt.Errorf("tlsacme: identity key: %w", err)
		}
		ec, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("tlsacme: identity key is not ECDSA")
		}
		return ec, nil
	}
	account, err := parse(j.AccountKey)
	if err != nil {
		return nil, err
	}
	key, err := parse(j.Key)
	if err != nil {
		return nil, err
	}
	return &Identity{AccountKey: account, Key: key, CreatedAt: j.CreatedAt, AccountURI: j.AccountURI, Chain: j.Chain}, nil
}

// Persistence keeps the identity outside the enclave's memory.
type Persistence interface {
	// Load returns the stored identity, creating and storing one when there is none.
	Load(ctx context.Context) (*Identity, error)
	// Save stores a changed identity (a new certificate chain or account URL).
	Save(ctx context.Context, id *Identity) error
}

// Sealed is a Persistence that stores the identity as one record, encrypted
// with a data key that only an attested enclave can get from KMS. Outside the
// enclave the private keys exist only in that sealed form.
type Sealed struct {
	Records store.Records
	Keys    keys.Service
	// Name tells identities apart, e.g. one per ACME CA.
	Name string
	Now  func() time.Time

	mu      sync.Mutex
	dataKey []byte
	wrapped []byte
}

func (s *Sealed) keyContext() map[string]string {
	return map[string]string{"purpose": "tls-identity", "name": s.Name}
}

func (s *Sealed) aad() []byte { return envelope.AAD("tls-identity", s.Name) }

func (s *Sealed) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Load implements Persistence.
func (s *Sealed) Load(ctx context.Context) (*Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for range 2 {
		rec, err := s.Records.GetRecord(ctx, s.Name)
		switch {
		case err == nil:
			key, err := s.Keys.Unwrap(ctx, rec.WrappedKey, s.keyContext())
			if err != nil {
				return nil, err
			}
			plain, err := envelope.Open(key, rec.Value, s.aad())
			if err != nil {
				return nil, fmt.Errorf("tlsacme: stored identity: %w", err)
			}
			id, err := unmarshalIdentity(plain)
			if err != nil {
				return nil, err
			}
			s.dataKey, s.wrapped = key, rec.WrappedKey
			// A key that an older image wrapped directly with KMS moves under
			// the root secret the first time it is loaded.
			if rw, ok := s.Keys.(keys.Rewrapper); ok {
				wrapped, changed, err := rw.Rewrap(ctx, rec.WrappedKey, s.keyContext())
				if err != nil {
					return nil, err
				}
				if changed {
					s.wrapped = wrapped
					if err := s.put(ctx, id, false); err != nil {
						return nil, err
					}
				}
			}
			return id, nil
		case !errors.Is(err, store.ErrNotFound):
			return nil, err
		}

		id, err := NewIdentity(s.now())
		if err != nil {
			return nil, err
		}
		dk, err := s.Keys.NewDataKey(ctx, s.keyContext())
		if err != nil {
			return nil, err
		}
		s.dataKey, s.wrapped = dk.Plaintext, dk.Wrapped
		switch err := s.put(ctx, id, true); {
		case err == nil:
			return id, nil
		case !errors.Is(err, store.ErrExists):
			return nil, err
		}
		// Another enclave created it first: use that one.
	}
	return nil, errors.New("tlsacme: identity record keeps changing")
}

// Save implements Persistence.
func (s *Sealed) Save(ctx context.Context, id *Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dataKey == nil {
		return errors.New("tlsacme: Save before Load")
	}
	return s.put(ctx, id, false)
}

func (s *Sealed) put(ctx context.Context, id *Identity, create bool) error {
	plain, err := id.marshal()
	if err != nil {
		return err
	}
	sealed, err := envelope.Seal(s.dataKey, plain, s.aad())
	if err != nil {
		return err
	}
	return s.Records.PutRecord(ctx, &store.Record{Name: s.Name, WrappedKey: s.wrapped, Value: sealed, UpdatedAt: s.now().UTC()}, create)
}
