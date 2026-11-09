// Package tlsacme obtains and renews one multi-SAN TLS certificate from an
// ACME CA using the TLS-ALPN-01 challenge, entirely inside the enclave.
//
// The certificate private key is generated at boot, lives only in enclave
// memory, and is reused across renewals, so the SPKI hash bound into the
// attestation documents stays constant for the lifetime of the enclave.
package tlsacme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
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
	hosts  []string
	key    *ecdsa.PrivateKey
	spki   []byte
	client *acme.Client
	log    *slog.Logger

	mu         sync.RWMutex
	cert       *tls.Certificate
	challenges map[string]*tls.Certificate
}

// New creates a Manager for hosts using the ACME directory at directoryURL.
// httpClient carries ACME traffic (in the enclave, through the egress proxy).
func New(hosts []string, directoryURL string, httpClient *http.Client, log *slog.Logger) (*Manager, error) {
	if len(hosts) == 0 {
		return nil, errors.New("tlsacme: no hosts")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	accountKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	return &Manager{
		hosts:      slices.Clone(hosts),
		key:        key,
		spki:       spki,
		client:     &acme.Client{Key: accountKey, DirectoryURL: directoryURL, HTTPClient: httpClient, UserAgent: "permaily"},
		log:        log,
		challenges: map[string]*tls.Certificate{},
	}, nil
}

// Key returns the certificate private key.
func (m *Manager) Key() crypto.Signer { return m.key }

// UserData returns the value bound into attestation documents.
func (m *Manager) UserData() []byte { return attestation.SPKIHashFromDER(m.spki) }

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

// Run obtains the certificate and keeps it renewed until ctx is done.
func (m *Manager) Run(ctx context.Context) {
	backoff := time.Minute
	for {
		wait := backoff
		leaf, err := m.obtain(ctx)
		if err != nil {
			m.log.Error("acme: obtain certificate", "err", err, "retry_in", backoff)
			backoff = min(backoff*2, time.Hour)
		} else {
			backoff = time.Minute
			life := leaf.NotAfter.Sub(leaf.NotBefore)
			wait = time.Until(leaf.NotBefore.Add(life * 2 / 3))
			m.log.Info("acme: certificate installed", "not_after", leaf.NotAfter, "renew_in", wait.Round(time.Minute))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (m *Manager) obtain(ctx context.Context) (*x509.Certificate, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	if _, err := m.client.Register(ctx, &acme.Account{}, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, fmt.Errorf("register: %w", err)
	}
	order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs(m.hosts...))
	if err != nil {
		return nil, fmt.Errorf("order: %w", err)
	}
	defer m.clearChallenges()
	for _, u := range order.AuthzURLs {
		if err := m.authorize(ctx, u); err != nil {
			return nil, err
		}
	}
	order, err = m.client.WaitOrder(ctx, order.URI)
	if err != nil {
		return nil, fmt.Errorf("wait order: %w", err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: m.hosts}, m.key)
	if err != nil {
		return nil, err
	}
	der, _, err := m.client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return nil, fmt.Errorf("finalize: %w", err)
	}
	leaf, err := x509.ParseCertificate(der[0])
	if err != nil {
		return nil, err
	}
	m.SetCertificate(&tls.Certificate{Certificate: der, PrivateKey: m.key, Leaf: leaf})
	return leaf, nil
}

func (m *Manager) authorize(ctx context.Context, url string) error {
	z, err := m.client.GetAuthorization(ctx, url)
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
	cert, err := m.client.TLSALPN01ChallengeCert(chal.Token, z.Identifier.Value)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.challenges[z.Identifier.Value] = &cert
	m.mu.Unlock()
	if _, err := m.client.Accept(ctx, chal); err != nil {
		return fmt.Errorf("accept %s: %w", z.Identifier.Value, err)
	}
	if _, err := m.client.WaitAuthorization(ctx, z.URI); err != nil {
		return fmt.Errorf("validate %s: %w", z.Identifier.Value, err)
	}
	return nil
}

func (m *Manager) clearChallenges() {
	m.mu.Lock()
	clear(m.challenges)
	m.mu.Unlock()
}
