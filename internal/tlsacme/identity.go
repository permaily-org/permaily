package tlsacme

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
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
	// Order says that an enclave is asking the CA for a certificate, and until
	// when the others wait for it instead of asking too; nil when none is.
	Order *Order
}

// Order is an enclave's claim to be the one that asks the CA for the
// certificate. It is part of the sealed identity, so only an enclave can make
// one, and it is written only if the identity did not change since it was
// read: of two enclaves that claim at once, one does.
type Order struct {
	// By names the Manager that claimed it: random, new at each start, and
	// nothing else.
	By string
	// Until is when the claim ends if it is not renewed or released: the time
	// one order takes, plus the wait before the next try should it fail.
	Until time.Time
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
	Version    int        `json:"version"`
	AccountKey []byte     `json:"account_key"` // PKCS #8
	Key        []byte     `json:"key"`         // PKCS #8
	CreatedAt  time.Time  `json:"created_at"`
	AccountURI string     `json:"account_uri,omitempty"`
	Chain      [][]byte   `json:"chain,omitempty"`
	Order      *orderJSON `json:"order,omitempty"`
}

// orderJSON is added to version 1 without changing it: a release from before
// ignores it, and reads the rest as it always did.
type orderJSON struct {
	By    string    `json:"by"`
	Until time.Time `json:"until"`
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
	j := identityJSON{Version: 1, AccountKey: account, Key: key, CreatedAt: id.CreatedAt, AccountURI: id.AccountURI, Chain: id.Chain}
	if id.Order != nil {
		j.Order = &orderJSON{By: id.Order.By, Until: id.Order.Until}
	}
	return json.Marshal(j)
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
	id := &Identity{AccountKey: account, Key: key, CreatedAt: j.CreatedAt, AccountURI: j.AccountURI, Chain: j.Chain}
	if j.Order != nil {
		id.Order = &Order{By: j.Order.By, Until: j.Order.Until}
	}
	return id, nil
}

// ErrChanged is returned by Persistence.Write when the identity was written
// by someone else since it was read: read it again and decide again.
var ErrChanged = store.ErrChanged

// Version is where an identity was read, for writing it back only if nobody
// wrote it since. The zero value is for an identity nobody read.
type Version struct {
	mark store.Mark
	// at says the mark holds: the records keep a commit per version.
	at bool
}

// Persistence keeps the identity outside the enclave's memory, and the
// TLS-ALPN-01 challenges of an open order, so that any enclave can answer the
// CA's check.
type Persistence interface {
	// Load returns the stored identity, creating and storing one when there is none.
	Load(ctx context.Context) (*Identity, error)
	// Read returns the identity as stored now, and where it was read.
	Read(ctx context.Context) (*Identity, Version, error)
	// Write stores id only if nothing was written since it was read at v,
	// and fails with ErrChanged otherwise.
	Write(ctx context.Context, id *Identity, v Version) error
	// PutChallenge keeps the certificate that answers the CA's check for
	// host until the given time, or until DeleteChallenge.
	PutChallenge(ctx context.Context, host string, cert *tls.Certificate, until time.Time) error
	// GetChallenge returns the certificate that answers the CA's check for
	// host, and store.ErrNotFound when no order is open for it.
	GetChallenge(ctx context.Context, host string) (*tls.Certificate, error)
	DeleteChallenge(ctx context.Context, host string) error
}

// recordsAt are records written only if nothing changed since they were read
// (store.Anchored).
type recordsAt interface {
	GetRecordAt(ctx context.Context, name string) (*store.Record, store.Mark, error)
	PutRecordAt(ctx context.Context, r *store.Record, at store.Mark) error
}

// Sealed is a Persistence that stores the identity as one record, encrypted
// with a data key that only an attested enclave can get from KMS. Outside the
// enclave the private keys exist only in that sealed form.
//
// With Records that keep a commit per version (store.Anchored), two enclaves
// never undo each other's writes. With plain records, the last write wins:
// that is for one enclave only.
type Sealed struct {
	Records store.Records
	Keys    keys.Service
	// Challenges keeps the challenges of an open order, sealed like the
	// identity, for as long as the order is open.
	Challenges store.Entries
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
			return s.open(ctx, rec)
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
		switch err := s.put(ctx, id, Version{}, true); {
		case err == nil:
			return id, nil
		case !errors.Is(err, store.ErrExists):
			return nil, err
		}
		// Another enclave created it first: use that one.
	}
	return nil, errors.New("tlsacme: identity record keeps changing")
}

// open unseals a stored identity, asking KMS for its data key only when it is
// not the one already known.
func (s *Sealed) open(ctx context.Context, rec *store.Record) (*Identity, error) {
	key := s.dataKey
	if key == nil || !bytes.Equal(rec.WrappedKey, s.wrapped) {
		var err error
		if key, err = s.Keys.Unwrap(ctx, rec.WrappedKey, s.keyContext()); err != nil {
			return nil, err
		}
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
	return id, nil
}

// Read implements Persistence.
func (s *Sealed) Read(ctx context.Context) (*Identity, Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rec *store.Record
	var v Version
	var err error
	if at, ok := s.Records.(recordsAt); ok {
		rec, v.mark, err = at.GetRecordAt(ctx, s.Name)
		v.at = true
	} else {
		rec, err = s.Records.GetRecord(ctx, s.Name)
	}
	if err != nil {
		return nil, Version{}, err
	}
	id, err := s.open(ctx, rec)
	if err != nil {
		return nil, Version{}, err
	}
	return id, v, nil
}

// Write implements Persistence.
func (s *Sealed) Write(ctx context.Context, id *Identity, v Version) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dataKey == nil {
		return errors.New("tlsacme: Write before Load")
	}
	return s.put(ctx, id, v, false)
}

func (s *Sealed) put(ctx context.Context, id *Identity, v Version, create bool) error {
	plain, err := id.marshal()
	if err != nil {
		return err
	}
	sealed, err := envelope.Seal(s.dataKey, plain, s.aad())
	if err != nil {
		return err
	}
	rec := &store.Record{Name: s.Name, WrappedKey: s.wrapped, Value: sealed, UpdatedAt: s.now().UTC()}
	if at, ok := s.Records.(recordsAt); ok && v.at {
		return at.PutRecordAt(ctx, rec, v.mark)
	}
	return s.Records.PutRecord(ctx, rec, create)
}

// A challenge is kept as an entry of its own, sealed under the identity's
// data key and bound to the identity and the host. The entry ends with the
// order; what is sealed in it says until when it may be used, so an entry
// kept longer by whoever runs the storage is refused all the same.
func (s *Sealed) challengePartition() string { return "acme-challenge#" + s.Name }

func (s *Sealed) challengeAAD(host string) []byte {
	return envelope.AAD("acme-challenge", s.Name, host)
}

type challengeJSON struct {
	Chain [][]byte  `json:"chain"`
	Key   []byte    `json:"key"` // PKCS #8
	Until time.Time `json:"until"`
}

type challengeEntry struct {
	Sealed []byte `json:"sealed"`
}

func (s *Sealed) challengeKey() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dataKey == nil {
		return nil, errors.New("tlsacme: challenge before Load")
	}
	if s.Challenges == nil {
		return nil, errors.New("tlsacme: no store for challenges")
	}
	return s.dataKey, nil
}

// PutChallenge implements Persistence.
func (s *Sealed) PutChallenge(ctx context.Context, host string, cert *tls.Certificate, until time.Time) error {
	key, err := s.challengeKey()
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return err
	}
	plain, err := json.Marshal(challengeJSON{Chain: cert.Certificate, Key: der, Until: until.UTC()})
	if err != nil {
		return err
	}
	sealed, err := envelope.Seal(key, plain, s.challengeAAD(host))
	if err != nil {
		return err
	}
	value, err := json.Marshal(challengeEntry{Sealed: sealed})
	if err != nil {
		return err
	}
	e := &store.Entry{Partition: s.challengePartition(), Key: host, Value: value, ExpiresAt: until, UpdatedAt: s.now().UTC()}
	// One left by an order that did not end well is written over: only the
	// enclave that holds the order writes here.
	if old, err := s.Challenges.GetEntry(ctx, e.Partition, host); err == nil {
		e.Version = old.Version
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return s.Challenges.PutEntry(ctx, e)
}

// GetChallenge implements Persistence.
func (s *Sealed) GetChallenge(ctx context.Context, host string) (*tls.Certificate, error) {
	key, err := s.challengeKey()
	if err != nil {
		return nil, err
	}
	e, err := s.Challenges.GetEntry(ctx, s.challengePartition(), host)
	if err != nil {
		return nil, err
	}
	var ce challengeEntry
	if err := json.Unmarshal(e.Value, &ce); err != nil {
		return nil, fmt.Errorf("tlsacme: stored challenge: %w", err)
	}
	plain, err := envelope.Open(key, ce.Sealed, s.challengeAAD(host))
	if err != nil {
		return nil, fmt.Errorf("tlsacme: stored challenge: %w", err)
	}
	var c challengeJSON
	if err := json.Unmarshal(plain, &c); err != nil {
		return nil, fmt.Errorf("tlsacme: stored challenge: %w", err)
	}
	if !s.now().Before(c.Until) {
		return nil, store.ErrNotFound
	}
	priv, err := x509.ParsePKCS8PrivateKey(c.Key)
	if err != nil || len(c.Chain) == 0 {
		return nil, errors.New("tlsacme: stored challenge: no key or no certificate")
	}
	signer, ok := priv.(crypto.Signer)
	if !ok {
		return nil, errors.New("tlsacme: stored challenge: the key cannot sign")
	}
	return &tls.Certificate{Certificate: c.Chain, PrivateKey: signer}, nil
}

// DeleteChallenge implements Persistence.
func (s *Sealed) DeleteChallenge(ctx context.Context, host string) error {
	if s.Challenges == nil {
		return nil
	}
	if err := s.Challenges.DeleteEntry(ctx, s.challengePartition(), host); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}
