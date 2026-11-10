// Package tlsacme obtains and renews one multi-SAN TLS certificate from an
// ACME CA using the TLS-ALPN-01 challenge, entirely inside the enclave.
//
// The certificate private key is generated inside an enclave and never leaves
// one in clear: between restarts it is stored sealed with a key that KMS gives
// only to an attested enclave (Sealed). It is reused across renewals, restarts
// and upgrades, so the SPKI hash bound into attestation documents stays
// constant, and so does the ACME account, which CAA records can pin.
package tlsacme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/permaily-org/permaily/pkg/attestation"
)

// Directories of Let's Encrypt.
const (
	LetsEncryptProduction = "https://acme-v02.api.letsencrypt.org/directory"
	LetsEncryptStaging    = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// Manager serves the current certificate and answers TLS-ALPN-01 challenges.
type Manager struct {
	hosts        []string
	directoryURL string
	httpClient   *http.Client
	persist      Persistence
	log          *slog.Logger
	now          func() time.Time
	// issue asks the CA for a certificate; replaced in tests.
	issue func(ctx context.Context) error
	// retry is the first wait after a failed load of the identity.
	retry time.Duration

	mu         sync.RWMutex
	ident      *Identity
	spki       []byte
	client     *acme.Client
	cert       *tls.Certificate
	challenges map[string]*tls.Certificate
}

// New creates a Manager for hosts using the ACME directory at directoryURL.
// httpClient carries ACME traffic (in the enclave, through the egress proxy).
//
// With a nil persist the keys are generated now and live only in memory (dev
// mode, or an image without storage). Otherwise Run loads them from persist,
// and until then the Manager has no key and serves nothing.
func New(hosts []string, directoryURL string, httpClient *http.Client, persist Persistence, log *slog.Logger) (*Manager, error) {
	if len(hosts) == 0 {
		return nil, errors.New("tlsacme: no hosts")
	}
	m := &Manager{
		hosts:        slices.Clone(hosts),
		directoryURL: directoryURL,
		httpClient:   httpClient,
		persist:      persist,
		log:          log,
		now:          time.Now,
		retry:        5 * time.Second,
		challenges:   map[string]*tls.Certificate{},
	}
	m.issue = m.obtain
	if persist == nil {
		id, err := NewIdentity(m.now())
		if err != nil {
			return nil, err
		}
		if err := m.setIdentity(id); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *Manager) setIdentity(id *Identity) error {
	spki, err := x509.MarshalPKIXPublicKey(&id.Key.PublicKey)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ident, m.spki = id, spki
	m.client = &acme.Client{Key: id.AccountKey, DirectoryURL: m.directoryURL, HTTPClient: m.httpClient, UserAgent: "permaily"}
	return nil
}

// Key returns the certificate private key, or nil while it is not loaded.
func (m *Manager) Key() crypto.Signer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.ident == nil {
		return nil
	}
	return m.ident.Key
}

// UserData returns the value bound into attestation documents (the SHA-256 of
// the certificate key's SPKI), or nil while the key is not loaded.
func (m *Manager) UserData() []byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.spki == nil {
		return nil
	}
	return attestation.SPKIHashFromDER(m.spki)
}

// Status describes the TLS identity for /status, or nil while it is not loaded.
func (m *Manager) Status() *attestation.TLSStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.ident == nil {
		return nil
	}
	return &attestation.TLSStatus{
		SPKISHA256:   hex.EncodeToString(attestation.SPKIHashFromDER(m.spki)),
		KeyCreatedAt: m.ident.CreatedAt,
		Sealed:       m.persist != nil,
		ACMEAccount:  m.ident.AccountURI,
	}
}

// Ready reports whether a certificate has been obtained.
func (m *Manager) Ready() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cert != nil
}

// TLSConfig returns a server config that serves the managed certificate and
// answers ACME TLS-ALPN-01 challenges. nextProtos are the application protocols.
func (m *Manager) TLSConfig(nextProtos ...string) *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		NextProtos:     append(slices.Clone(nextProtos), acme.ALPNProto),
		GetCertificate: m.GetCertificate,
	}
}

// GetCertificate implements tls.Config.GetCertificate.
func (m *Manager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if slices.Contains(hello.SupportedProtos, acme.ALPNProto) {
		if c := m.challenges[hello.ServerName]; c != nil {
			return c, nil
		}
		return nil, fmt.Errorf("tlsacme: no pending challenge for %q", hello.ServerName)
	}
	if m.cert == nil {
		return nil, errors.New("tlsacme: certificate not ready")
	}
	return m.cert, nil
}

// SetCertificate installs a certificate directly (used by dev mode).
func (m *Manager) SetCertificate(c *tls.Certificate) {
	m.mu.Lock()
	m.cert = c
	m.mu.Unlock()
}

// Run loads the identity, then obtains the certificate and keeps it renewed
// until ctx is done.
func (m *Manager) Run(ctx context.Context) {
	if !m.load(ctx) {
		return
	}
	backoff := time.Minute
	for {
		wait := backoff
		renewAt, err := m.ensure(ctx)
		if err != nil {
			m.log.Error("acme: obtain certificate", "err", err, "retry_in", backoff)
			backoff = min(backoff*2, time.Hour)
		} else {
			backoff = time.Minute
			wait = max(renewAt.Sub(m.now()), time.Minute)
			m.log.Info("acme: certificate installed", "renew_in", wait.Round(time.Minute))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// load gets the identity from persistence, retrying until it works: without
// it the enclave must not invent another key. It returns false when ctx ends.
func (m *Manager) load(ctx context.Context) bool {
	if m.persist == nil {
		return true
	}
	backoff := m.retry
	for {
		id, err := m.persist.Load(ctx)
		if err == nil {
			if err = m.setIdentity(id); err == nil {
				m.log.Info("acme: identity loaded", "key_created_at", id.CreatedAt)
				return true
			}
		}
		m.log.Error("acme: load identity", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

// ensure serves the stored certificate when it is still good, and asks the CA
// for a new one when there is none or it is due for renewal. It returns when
// the certificate in use should be renewed.
func (m *Manager) ensure(ctx context.Context) (time.Time, error) {
	if renewAt, ok := m.install(); ok && m.now().Before(renewAt) {
		return renewAt, nil
	}
	m.mu.RLock()
	previous := m.ident.Chain
	m.mu.RUnlock()
	if err := m.issue(ctx); err != nil {
		return time.Time{}, err
	}
	renewAt, ok := m.install()
	if !ok {
		// Keep whatever was there: a still valid certificate goes on being served.
		m.mu.Lock()
		m.ident.Chain = previous
		m.mu.Unlock()
		return time.Time{}, errors.New("tlsacme: the CA returned a certificate that does not match the key or the hosts")
	}
	if m.persist != nil {
		m.mu.RLock()
		id := *m.ident
		m.mu.RUnlock()
		if err := m.persist.Save(ctx, &id); err != nil {
			// The certificate works from memory; a restart would ask for another one.
			m.log.Error("acme: save identity", "err", err)
		}
	}
	return renewAt, nil
}

// install starts serving the stored chain when it is usable, and returns when
// it should be renewed.
func (m *Manager) install() (time.Time, bool) {
	leaf, renewAt, ok := m.usable()
	if !ok {
		return time.Time{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cert == nil || m.cert.Leaf == nil || !m.cert.Leaf.Equal(leaf) {
		m.cert = &tls.Certificate{Certificate: m.ident.Chain, PrivateKey: m.ident.Key, Leaf: leaf}
	}
	return renewAt, true
}

// usable reports whether the stored chain is a certificate for our key and
// every host that has not expired, and when it should be renewed (after two
// thirds of its lifetime).
func (m *Manager) usable() (*x509.Certificate, time.Time, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.ident == nil || len(m.ident.Chain) == 0 {
		return nil, time.Time{}, false
	}
	leaf, err := x509.ParseCertificate(m.ident.Chain[0])
	if err != nil {
		return nil, time.Time{}, false
	}
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&m.ident.Key.PublicKey) {
		return nil, time.Time{}, false
	}
	for _, h := range m.hosts {
		if leaf.VerifyHostname(h) != nil {
			return nil, time.Time{}, false
		}
	}
	now := m.now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, time.Time{}, false
	}
	return leaf, leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) * 2 / 3), true
}

// obtain registers the account if needed and gets a certificate from the CA.
func (m *Manager) obtain(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	m.mu.RLock()
	client, key := m.client, m.ident.Key
	m.mu.RUnlock()

	account, err := client.Register(ctx, &acme.Account{}, acme.AcceptTOS)
	if errors.Is(err, acme.ErrAccountAlreadyExists) {
		account, err = client.GetReg(ctx, "")
	}
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(m.hosts...))
	if err != nil {
		return fmt.Errorf("order: %w", err)
	}
	defer m.clearChallenges()
	for _, u := range order.AuthzURLs {
		if err := m.authorize(ctx, client, u); err != nil {
			return err
		}
	}
	order, err = client.WaitOrder(ctx, order.URI)
	if err != nil {
		return fmt.Errorf("wait order: %w", err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: m.hosts}, key)
	if err != nil {
		return err
	}
	der, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return fmt.Errorf("finalize: %w", err)
	}
	m.mu.Lock()
	m.ident.Chain, m.ident.AccountURI = der, account.URI
	m.mu.Unlock()
	return nil
}

func (m *Manager) authorize(ctx context.Context, client *acme.Client, url string) error {
	z, err := client.GetAuthorization(ctx, url)
	if err != nil {
		return fmt.Errorf("authorization: %w", err)
	}
	if z.Status == acme.StatusValid {
		return nil
	}
	var chal *acme.Challenge
	for _, c := range z.Challenges {
		if c.Type == "tls-alpn-01" {
			chal = c
			break
		}
	}
	if chal == nil {
		return fmt.Errorf("authorization %s: no tls-alpn-01 challenge offered", z.Identifier.Value)
	}
	cert, err := client.TLSALPN01ChallengeCert(chal.Token, z.Identifier.Value)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.challenges[z.Identifier.Value] = &cert
	m.mu.Unlock()
	if _, err := client.Accept(ctx, chal); err != nil {
		return fmt.Errorf("accept %s: %w", z.Identifier.Value, err)
	}
	if _, err := client.WaitAuthorization(ctx, z.URI); err != nil {
		return fmt.Errorf("validate %s: %w", z.Identifier.Value, err)
	}
	return nil
}

func (m *Manager) clearChallenges() {
	m.mu.Lock()
	clear(m.challenges)
	m.mu.Unlock()
}
