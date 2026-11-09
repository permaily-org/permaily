// Command permaily-enclave is the service that runs inside the Nitro Enclave.
//
// It terminates TLS itself (certificate obtained with ACME TLS-ALPN-01 from
// inside the enclave), serves app./api./mcp./inbox. by Host, and answers
// GET /attestation?nonce=<hex> with an NSM attestation document whose
// user_data is the SHA-256 of the TLS certificate's SPKI.
//
// Build with -tags dev to run locally over TCP with a self-signed certificate
// and a fake attester whose documents never verify against the AWS root.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	_ "golang.org/x/crypto/x509roots/fallback" // CA roots for egress TLS; the enclave has no /etc/ssl

	"github.com/permaily-org/permaily/internal/buildinfo"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/webui"
)

type config struct {
	domain        string
	acmeDirectory string
	port          uint32
	egressPort    uint32
}

func main() {
	var cfg config
	flag.StringVar(&cfg.domain, "domain", "permaily.com", "parent domain; serves app., api., mcp. and inbox. under it")
	flag.StringVar(&cfg.acmeDirectory, "acme-directory", "https://acme-v02.api.letsencrypt.org/directory", "ACME directory URL")
	cfg.port, cfg.egressPort = 443, 8000
	flag.Func("port", "listen port (vsock in the enclave, TCP in dev mode) (default 443)", uint32Flag(&cfg.port))
	flag.Func("egress-port", "vsock port of the host's CONNECT proxy (default 8000)", uint32Flag(&cfg.egressPort))
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(cfg, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func uint32Flag(dst *uint32) func(string) error {
	return func(s string) error {
		v, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return err
		}
		*dst = uint32(v)
		return nil
	}
}

func run(cfg config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	hosts := router.HostsForDomain(cfg.domain)
	p, err := newPlatform(ctx, cfg, hosts, log)
	if err != nil {
		return err
	}
	log.Info("starting", "commit", buildinfo.Commit, "mode", p.mode, "hosts", hosts.All(), "acme", cfg.acmeDirectory)

	common := map[string]http.Handler{
		"/attestation": httpapi.Attestation(p.attester, p.certs.UserData),
		"/version":     httpapi.Version(),
	}
	srv := &http.Server{
		Handler:           router.New(hosts, common, webui.Handler()),
		TLSConfig:         p.certs.TLSConfig("h2", "http/1.1"),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := srv.ServeTLS(p.listener, "", ""); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
