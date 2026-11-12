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
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// Directories of Let's Encrypt.
const (
	LetsEncryptProduction = "https://acme-v02.api.letsencrypt.org/directory"
	LetsEncryptStaging    = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// How enclaves that share one identity take turns with the CA.
const (
	// orderLease is how long an order may take before another enclave may
	// take it over.
	orderLease = 10 * time.Minute
	// refreshEvery is how often a running enclave reads the stored identity
	// for a certificate another enclave stored.
	refreshEvery = 10 * time.Minute
	// waitPoll is how often an enclave reads it while another one orders.
	waitPoll = time.Minute
	// challengeRecheck is how often the challenge of one name is read from
	// the storage, however many connections ask for it.
	challengeRecheck = time.Second
	// firstBackoff and maxBackoff bound the wait after a failed order.
	firstBackoff = time.Minute
	maxBackoff   = time.Hour
	// maxReads bounds how many times one step reads the identity again
	// because another enclave wrote it in between.
	maxReads = 5
)

// errOtherKey means the stored identity holds another key than the one this
// enclave serves: the identity was lost and made again. The enclave keeps
// its own key, asks the CA for nothing and writes nothing until it restarts.
var errOtherKey = errors.New("tlsacme: the stored identity holds another key; serving the one in memory until a restart")

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
	// holder names this Manager in the orders it claims.
	holder string
	// backoff is the wait after the next failed order.
	backoff time.Duration

	mu         sync.RWMutex
	ident      *Identity
	spki       []byte
	client     *acme.Client
	cert       *tls.Certificate
	challenges map[string]*tls.Certificate

	// shared are the challenges of orders another enclave opened, as last
	// read from the storage.
	sharedMu sync.Mutex
	shared   map[string]sharedChallenge
}

type sharedChallenge struct {
	cert    *tls.Certificate
	checked time.Time
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
	holder := make([]byte, 16)
	if _, err := rand.Read(holder); err != nil {
		return nil, err
	}
	m := &Manager{
		hosts:        slices.Clone(hosts),
		directoryURL: directoryURL,
		httpClient:   httpClient,
		persist:      persist,
		log:          log,
		now:          time.Now,
		retry:        5 * time.Second,
		holder:       hex.EncodeToString(holder),
		backoff:      firstBackoff,
		challenges:   map[string]*tls.Certificate{},
		shared:       map[string]sharedChallenge{},
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
	if slices.Contains(hello.SupportedProtos, acme.ALPNProto) {
		m.mu.RLock()
		c := m.challenges[hello.ServerName]
		m.mu.RUnlock()
		if c == nil {
			c = m.sharedChallenge(hello)
		}
		if c == nil {
			return nil, fmt.Errorf("tlsacme: no pending challenge for %q", hello.ServerName)
		}
		return c, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cert == nil {
		return nil, errors.New("tlsacme: certificate not ready")
	}
	return m.cert, nil
}

// sharedChallenge returns the challenge certificate another enclave stored
// for a name of ours, when it opened an order: the CA's check may reach any
// enclave. The storage is read at most once a second for a name.
func (m *Manager) sharedChallenge(hello *tls.ClientHelloInfo) *tls.Certificate {
	if m.persist == nil || !slices.Contains(m.hosts, hello.ServerName) {
		return nil
	}
	m.sharedMu.Lock()
	defer m.sharedMu.Unlock()
	now := m.now()
	if c, ok := m.shared[hello.ServerName]; ok && now.Sub(c.checked) < challengeRecheck && !now.Before(c.checked) {
		return c.cert
	}
	ctx := context.Background()
	if hc := hello.Context(); hc != nil {
		ctx = hc
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := m.persist.GetChallenge(ctx, hello.ServerName)
	if err != nil {
		c = nil
		if !errors.Is(err, store.ErrNotFound) {
			m.log.Warn("acme: read a challenge", "err", err)
		}
	}
	m.shared[hello.ServerName] = sharedChallenge{cert: c, checked: now}
	return c
}

// SetCertificate installs a certificate directly (used by dev mode).
func (m *Manager) SetCertificate(c *tls.Certificate) {
	m.mu.Lock()
	m.cert = c
	m.mu.Unlock()
}

// Run loads the identity, then obtains the certificate and keeps it renewed
// until ctx is done. With other enclaves on the same storage, one of them asks
// the CA and the others serve what it stored.
func (m *Manager) Run(ctx context.Context) {
	if !m.load(ctx) {
		return
	}
	for {
		next, err := m.ensure(ctx)
		wait := max(next.Sub(m.now()), time.Minute)
		if err != nil {
			wait = m.backoff
			m.backoff = min(m.backoff*2, maxBackoff)
			m.log.Error("acme: certificate", "err", err, "retry_in", wait.String())
		} else {
			m.backoff = firstBackoff
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
		m.log.Error("acme: load identity", "err", err, "retry_in", backoff.String())
		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

// ensure serves the newest good certificate, the one in memory or one another
// enclave stored, and asks the CA for a new one when there is none or it is
// due for renewal and no other enclave is asking. It returns when to look
// again.
func (m *Manager) ensure(ctx context.Context) (time.Time, error) {
	if m.persist == nil {
		return m.ensureAlone(ctx)
	}
	claimed := false
	for range maxReads {
		stored, at, err := m.persist.Read(ctx)
		if err != nil {
			// What is in memory goes on being served.
			return time.Time{}, fmt.Errorf("tlsacme: read identity: %w", err)
		}
		if !m.adopt(stored) {
			return time.Time{}, errOtherKey
		}
		now := m.now()
		if renewAt, ok := m.install(); ok && now.Before(renewAt) {
			return earliest(renewAt, now.Add(refreshEvery)), nil
		}
		o := stored.Order
		if o != nil && o.By != m.holder && now.Before(o.Until) {
			// Another enclave is asking the CA: serve what it stores.
			m.log.Info("acme: another enclave is ordering the certificate")
			return earliest(o.Until, now.Add(waitPoll)), nil
		}
		// Each try is claimed, for one order and then the wait before the
		// next should it fail, and made only once the claim is read back.
		if !claimed || o == nil || o.By != m.holder {
			claim := *stored
			claim.Order = &Order{By: m.holder, Until: now.Add(orderLease + m.backoff)}
			switch err := m.persist.Write(ctx, &claim, at); {
			case err == nil:
				claimed = true
			case !errors.Is(err, ErrChanged):
				return time.Time{}, fmt.Errorf("tlsacme: claim the order: %w", err)
			}
			continue // read again: by the claim, or by what the other enclave wrote
		}
		return m.order(ctx, stored, at)
	}
	return time.Time{}, errors.New("tlsacme: the stored identity keeps changing")
}

func earliest(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// ensureAlone is ensure for an identity that is not stored.
func (m *Manager) ensureAlone(ctx context.Context) (time.Time, error) {
	if renewAt, ok := m.install(); ok && m.now().Before(renewAt) {
		return renewAt, nil
	}
	return m.issueAndInstall(ctx)
}

// issueAndInstall asks the CA and starts serving what it gives.
func (m *Manager) issueAndInstall(ctx context.Context) (time.Time, error) {
	m.mu.RLock()
	previous, account := m.ident.Chain, m.ident.AccountURI
	m.mu.RUnlock()
	if err := m.issue(ctx); err != nil {
		return time.Time{}, err
	}
	renewAt, ok := m.install()
	if !ok {
		// Keep whatever was there: a still valid certificate goes on being served.
		m.mu.Lock()
		m.ident.Chain, m.ident.AccountURI = previous, account
		m.mu.Unlock()
		return time.Time{}, errors.New("tlsacme: the CA returned a certificate that does not match the key or the hosts")
	}
	m.log.Info("acme: certificate obtained")
	return renewAt, nil
}

// order asks the CA under this enclave's claim, and stores what it gives,
// which ends the claim. A failed order keeps the claim until it ends, so the
// others go on waiting while this one waits to try again.
func (m *Manager) order(ctx context.Context, stored *Identity, at Version) (time.Time, error) {
	renewAt, err := m.issueAndInstall(ctx)
	if err != nil {
		return time.Time{}, err
	}
	next := earliest(renewAt, m.now().Add(refreshEvery))
	for range maxReads {
		m.mu.RLock()
		done := *stored
		done.Chain, done.AccountURI = m.ident.Chain, m.ident.AccountURI
		m.mu.RUnlock()
		if done.Order != nil && done.Order.By == m.holder {
			done.Order = nil
		}
		err := m.persist.Write(ctx, &done, at)
		if err == nil {
			return next, nil
		}
		if !errors.Is(err, ErrChanged) {
			// The certificate works from memory; the others order when theirs is due.
			m.log.Error("acme: save identity", "err", err)
			return next, nil
		}
		// Someone wrote it while this enclave ordered: keep what is newer.
		if stored, at, err = m.persist.Read(ctx); err != nil {
			m.log.Error("acme: save identity", "err", err)
			return next, nil
		}
		if !m.adopt(stored) {
			return next, errOtherKey
		}
		if renewAt, ok := m.install(); ok {
			next = earliest(renewAt, m.now().Add(refreshEvery))
		}
		m.mu.RLock()
		leaf, _, ok := m.check(stored.Chain)
		m.mu.RUnlock()
		if ok && leaf.Equal(m.currentLeaf()) {
			return next, nil // what is stored is what this enclave serves
		}
	}
	m.log.Error("acme: save identity", "err", "the stored identity keeps changing")
	return next, nil
}

// adopt takes from a stored identity what another enclave got: the account,
// and a certificate for this key that lasts longer than the one in memory. It
// reports false when the stored identity holds another key, and then takes
// nothing.
func (m *Manager) adopt(stored *Identity) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !stored.Key.Equal(m.ident.Key) || !stored.AccountKey.Equal(m.ident.AccountKey) {
		return false
	}
	if stored.AccountURI != "" {
		m.ident.AccountURI = stored.AccountURI
	}
	theirs, _, ok := m.check(stored.Chain)
	if !ok {
		return true
	}
	if mine, _, ok := m.check(m.ident.Chain); !ok || theirs.NotAfter.After(mine.NotAfter) {
		m.ident.Chain = stored.Chain
	}
	return true
}

func (m *Manager) currentLeaf() *x509.Certificate {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cert == nil {
		return nil
	}
	return m.cert.Leaf
}

// install starts serving the stored chain when it is usable, and returns when
// it should be renewed.
func (m *Manager) install() (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ident == nil {
		return time.Time{}, false
	}
	leaf, renewAt, ok := m.check(m.ident.Chain)
	if !ok {
		return time.Time{}, false
	}
	if m.cert == nil || m.cert.Leaf == nil || !m.cert.Leaf.Equal(leaf) {
		m.cert = &tls.Certificate{Certificate: m.ident.Chain, PrivateKey: m.ident.Key, Leaf: leaf}
		m.log.Info("acme: certificate installed", "renew_in", renewAt.Sub(m.now()).Round(time.Minute).String())
	}
	return renewAt, true
}

// check reports whether chain is a certificate for our key and every host
// that has not expired, and when it should be renewed (after two thirds of its
// lifetime). The caller holds m.mu.
func (m *Manager) check(chain [][]byte) (*x509.Certificate, time.Time, bool) {
	if m.ident == nil || len(chain) == 0 {
		return nil, time.Time{}, false
	}
	leaf, err := x509.ParseCertificate(chain[0])
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
	if err := m.publish(ctx, z.Identifier.Value, &cert); err != nil {
		return err
	}
	if _, err := client.Accept(ctx, chal); err != nil {
		return fmt.Errorf("accept %s: %w", z.Identifier.Value, err)
	}
	if _, err := client.WaitAuthorization(ctx, z.URI); err != nil {
		return fmt.Errorf("validate %s: %w", z.Identifier.Value, err)
	}
	return nil
}

// publish starts answering the CA's check for host with cert, here and,
// through the storage, in any other enclave the CA's connection may reach.
func (m *Manager) publish(ctx context.Context, host string, cert *tls.Certificate) error {
	m.mu.Lock()
	m.challenges[host] = cert
	m.mu.Unlock()
	if m.persist == nil {
		return nil
	}
	if err := m.persist.PutChallenge(ctx, host, cert, m.now().Add(orderLease)); err != nil {
		return fmt.Errorf("store the challenge of %s: %w", host, err)
	}
	return nil
}

// clearChallenges stops answering the challenges of this enclave's order,
// and removes them from the storage.
func (m *Manager) clearChallenges() {
	m.mu.Lock()
	hosts := slices.Collect(maps.Keys(m.challenges))
	clear(m.challenges)
	m.mu.Unlock()
	if m.persist == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, h := range hosts {
		if err := m.persist.DeleteChallenge(ctx, h); err != nil {
			// It ends by itself with the order; what is in it opens nothing.
			m.log.Warn("acme: remove a challenge", "err", err)
		}
	}
}
