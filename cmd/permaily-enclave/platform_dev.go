//go:build dev

package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"time"

	"github.com/permaily-org/permaily/internal/nsm"
	"github.com/permaily-org/permaily/internal/nsm/fakensm"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/tlsacme"
)

type platform struct {
	mode     string
	listener net.Listener
	attester nsm.Attester
	certs    *tlsacme.Manager
}

// newPlatform listens on TCP, uses a self-signed certificate (valid for every
// host plus localhost) and a fake attester.
func newPlatform(_ context.Context, cfg config, hosts router.Hosts, log *slog.Logger) (*platform, error) {
	att, err := fakensm.New()
	if err != nil {
		return nil, err
	}
	certs, err := tlsacme.New(hosts.All(), cfg.acmeDirectory, nil, log)
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
	return &platform{mode: "dev", listener: ln, attester: att, certs: certs}, nil
}
