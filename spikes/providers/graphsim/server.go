package graphsim

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"sync"
	"time"
)

// The real names the simulator answers for, behind a test CA, so a client
// built for production URLs runs unchanged against it.
const (
	LoginHost = "login.microsoftonline.com"
	GraphHost = "graph.microsoft.com"
)

// Sim is one simulated identity platform and Graph, holding one mailbox per
// account.
type Sim struct {
	mu  sync.Mutex
	now time.Time

	signer *rsa.PrivateKey
	kid    string

	accounts map[string]*Account // by oid
	codes    map[string]*grant   // authorization codes
	refresh  map[string]*grant   // refresh tokens
	access   map[string]*grant   // access tokens
	seq      int

	// Scope rendering of token responses (token-scope-*).
	ScopeMode ScopeMode

	server *http.Server
	ln     net.Listener
	pool   *x509.CertPool

	// Graph state.
	throttle    []throttleRule
	inFlight    map[string]int
	windowStart map[string]time.Time
	windowCount map[string]int
	// Hold, when set, is called inside every Graph request before it is
	// answered; tests use it to keep requests concurrent.
	Hold func()
}

// ScopeMode picks how a token response renders `scope`.
type ScopeMode int

const (
	// ScopeResourceOnly lists the granted resource scopes, as the documented
	// examples do, in canonical case.
	ScopeResourceOnly ScopeMode = iota
	// ScopeDocExample reproduces the documented example literally: lower case
	// and percent-encoded.
	ScopeDocExample
	// ScopeOmitted leaves `scope` out (documented as optional).
	ScopeOmitted
	// ScopeWithOIDC also echoes the OIDC scopes (undocumented variant).
	ScopeWithOIDC
)

// New starts a simulator on a loopback port with a fresh test CA.
func New() (*Sim, error) {
	signer, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	s := &Sim{
		now:         time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC),
		signer:      signer,
		kid:         "sim-key-1",
		accounts:    map[string]*Account{},
		codes:       map[string]*grant{},
		refresh:     map[string]*grant{},
		access:      map[string]*grant{},
		inFlight:    map[string]int{},
		windowStart: map[string]time.Time{},
		windowCount: map[string]int{},
	}
	cert, pool, err := testCertificate(LoginHost, GraphHost)
	if err != nil {
		return nil, err
	}
	s.pool = pool
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.route)
	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
			// Offered on purpose: whether the real hosts do is item 8's
			// question (`http2`, not documented).
			NextProtos: []string{"h2", "http/1.1"},
		},
	}
	go func() { _ = s.server.ServeTLS(ln, "", "") }()
	return s, nil
}

// Close stops the simulator.
func (s *Sim) Close() error { return s.server.Close() }

// Client returns an HTTP client that sends requests for the real host names
// to the simulator and trusts only its test CA.
func (s *Sim) Client() *http.Client {
	addr := s.ln.Addr().String()
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
		TLSClientConfig:   &tls.Config{RootCAs: s.pool, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Advance moves the simulator's clock.
func (s *Sim) Advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = s.now.Add(d)
}

func (s *Sim) route(w http.ResponseWriter, r *http.Request) {
	switch hostOnly(r.Host) {
	case LoginHost:
		s.identity(w, r)
	case GraphHost:
		s.graph(w, r)
	default:
		http.NotFound(w, r)
	}
}

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

// testCertificate makes a throwaway CA and a leaf for the given names.
func testCertificate(names ...string) (tls.Certificate, *x509.CertPool, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "graphsim test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}, pool, nil
}
