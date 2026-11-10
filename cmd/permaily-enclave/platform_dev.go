//go:build dev

package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/msgauth"
	"github.com/permaily-org/permaily/internal/nsm"
	"github.com/permaily-org/permaily/internal/nsm/fakensm"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/tlsacme"
	"github.com/permaily-org/permaily/pkg/attestation"
)

type platform struct {
	mode         string
	listener     net.Listener
	smtpListener net.Listener
	attester     nsm.Attester
	certs        *tlsacme.Manager
	keys         keys.Service
	store        store.Store
	auth         inbox.Authenticator
	// kmsStatus reads the policy of the KMS key that older images wrapped data
	// keys with, for /status; nil when there is none (no mailbox, or dev mode).
	kmsStatus func(context.Context) (attestation.KMSStatus, error)
	// rootStatus describes the key that holds the root secret, and handoff
	// gives the root secret to the next release; nil without a root secret.
	rootStatus func(context.Context) (*attestation.RootStatus, error)
	handoff    func(context.Context, keys.Handoff) (*keys.HandoffResult, error)
}

// logOutput is the terminal in dev mode.
func logOutput(config) io.Writer { return os.Stdout }

// newPlatform listens on TCP (HTTPS on -port, SMTP on -smtp-port, default
// 2525 in dev), uses a self-signed certificate (valid for every host plus
// localhost), a fake attester, local keys and an in-memory store. DKIM uses
// the system resolver.
func newPlatform(_ context.Context, cfg config, hosts router.Hosts, log *slog.Logger) (*platform, error) {
	att, err := fakensm.New()
	if err != nil {
		return nil, err
	}
	certs, err := tlsacme.New(hosts.All(), cfg.acmeDirectory, nil, nil, log)
	if err != nil {
		return nil, err
	}
	names := append(hosts.All(), "localhost")
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hosts.App},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, certs.Key().Public(), certs.Key())
	if err != nil {
		return nil, err
	}
	leaf, _ := x509.ParseCertificate(der)
	certs.SetCertificate(&tls.Certificate{Certificate: [][]byte{der}, PrivateKey: certs.Key(), Leaf: leaf})
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.port))
	if err != nil {
		return nil, err
	}
	p := &platform{mode: "dev", listener: ln, attester: att, certs: certs, store: store.NewMemory()}
	if cfg.mailboxID != "" {
		port := cfg.smtpPort
		if port == 25 {
			port = 2525 // unprivileged default for local runs
		}
		if p.smtpListener, err = net.Listen("tcp", fmt.Sprintf(":%d", port)); err != nil {
			return nil, err
		}
		if p.keys, err = keys.NewLocal(); err != nil {
			return nil, err
		}
		p.auth = msgauth.DKIM(net.DefaultResolver.LookupTXT)
	}
	return p, nil
}
