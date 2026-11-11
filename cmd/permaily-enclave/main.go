// Command permaily-enclave is the service that runs inside the Nitro Enclave.
//
// It terminates TLS itself (certificate obtained with ACME TLS-ALPN-01 from
// inside the enclave), serves app./api./mcp./inbox./mta-sts.inbox. by Host,
// answers GET /attestation?nonce=<hex> with an NSM attestation document whose
// user_data is the SHA-256 of the TLS certificate's SPKI, reports the key that
// holds its root secret on GET /status, and (F1) receives mail for the mailbox
// over SMTP, encrypting it under a root secret that only an enclave running a
// public release can obtain (ADR 0013).
//
// Build with -tags dev to run locally over TCP with a self-signed certificate,
// a fake attester, local keys and an in-memory store.
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
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/smtpd"
	"github.com/permaily-org/permaily/internal/webui"
	"github.com/permaily-org/permaily/pkg/provenance"
)

type config struct {
	domain        string
	acmeDirectory string
	port          uint32
	egressPort    uint32

	// F1 mailbox. All of it is compiled into the image (entry point flags).
	mailboxID          string
	mailboxAddress     string
	mailboxTokenSHA256 string
	smtpPort           uint32
	mtaSTSMode         string
	awsRegion          string
	dynamoTable        string
	s3Bucket           string
	// Where the commits of the records that must not go back are kept (ADR 0016).
	anchorBucket        string
	anchorRetentionDays int
	credentialsPort     uint32
	dohEndpoint         string
	logPort             uint32

	// The root secret (ADR 0013). awsAccount is where release keys must live,
	// and releases says which builds may receive the root secret.
	awsAccount       string
	adminTokenSHA256 string
	releases         provenance.Policy
}

func main() {
	var cfg config
	flag.StringVar(&cfg.domain, "domain", "permaily.com", "parent domain; serves app., api., mcp., inbox. and mta-sts.inbox. under it")
	flag.StringVar(&cfg.acmeDirectory, "acme-directory", "https://acme-v02.api.letsencrypt.org/directory", "ACME directory URL")
	cfg.port, cfg.egressPort, cfg.smtpPort, cfg.credentialsPort, cfg.logPort = 443, 8000, 25, 8002, 8003
	flag.Func("log-port", "vsock port of the host that receives the log lines; 0 disables (default 8003)", uint32Flag(&cfg.logPort))
	flag.Func("port", "HTTPS listen port (vsock in the enclave, TCP in dev mode) (default 443)", uint32Flag(&cfg.port))
	flag.Func("egress-port", "vsock port of the host's CONNECT proxy (default 8000)", uint32Flag(&cfg.egressPort))
	flag.Func("smtp-port", "SMTP listen port (vsock in the enclave, TCP in dev mode) (default 25)", uint32Flag(&cfg.smtpPort))
	flag.Func("credentials-port", "vsock port of the host's credentials endpoint (default 8002)", uint32Flag(&cfg.credentialsPort))
	flag.StringVar(&cfg.mailboxID, "mailbox-id", "", "id of the mailbox (F1 test mailbox); empty disables mail")
	flag.StringVar(&cfg.mailboxAddress, "mailbox-address", "", "email address of the mailbox")
	flag.StringVar(&cfg.mailboxTokenSHA256, "mailbox-token-sha256", "", "hex SHA-256 of the bearer token that reads the mailbox")
	flag.StringVar(&cfg.mtaSTSMode, "mta-sts-mode", "testing", "MTA-STS policy mode: testing or enforce")
	flag.StringVar(&cfg.awsRegion, "aws-region", "us-east-2", "AWS region of KMS, DynamoDB and S3")
	flag.StringVar(&cfg.awsAccount, "aws-account", "", "AWS account that holds the KMS key of each release (pinned)")
	flag.StringVar(&cfg.adminTokenSHA256, "admin-token-sha256", "", "hex SHA-256 of the bearer token that may ask for a handoff")
	flag.StringVar(&cfg.releases.Repository, "release-repository", "", "owner/name of the GitHub repository whose releases may receive the root secret")
	flag.StringVar(&cfg.releases.RepositoryID, "release-repository-id", "", "GitHub's numeric id of that repository")
	flag.StringVar(&cfg.releases.OwnerID, "release-owner-id", "", "GitHub's numeric id of the repository's owner")
	flag.StringVar(&cfg.releases.Workflow, "release-workflow", "", "path of the workflow that publishes releases")
	flag.StringVar(&cfg.releases.Ref, "release-ref", "", "the git ref whose runs of that workflow are releases")
	flag.StringVar(&cfg.releases.RefPrefix, "release-ref-prefix", "", "instead of -release-ref: every ref that starts with this (test images)")
	flag.StringVar(&cfg.dynamoTable, "dynamodb-table", "", "DynamoDB table for mailboxes and messages")
	flag.StringVar(&cfg.s3Bucket, "s3-bucket", "", "S3 bucket for raw messages")
	flag.StringVar(&cfg.anchorBucket, "anchor-bucket", "", "S3 bucket with Object Lock for the commits of the records that must not be put back to an earlier copy")
	flag.IntVar(&cfg.anchorRetentionDays, "anchor-retention-days", 0, "days S3 keeps each commit, whatever anyone asks")
	flag.StringVar(&cfg.dohEndpoint, "doh-endpoint", "https://cloudflare-dns.com/dns-query", "DNS-over-HTTPS endpoint for DKIM lookups")
	flag.Parse()

	// Logs go to the console (visible only in debug mode) and to the parent
	// instance. Both are outside the enclave: nothing sensitive may be logged.
	log := slog.New(slog.NewJSONHandler(logOutput(cfg), nil))
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
	log.Info("starting", "commit", buildinfo.Commit, "mode", p.mode, "hosts", hosts.All(), "acme", cfg.acmeDirectory, "mailbox", cfg.mailboxID != "")

	api := http.NewServeMux()
	api.Handle("/", router.Hello("api"))
	byHost := map[string]http.Handler{
		hosts.App:    webui.Handler(),
		hosts.API:    api,
		hosts.MCP:    router.Hello("mcp"),
		hosts.Inbox:  router.Hello("inbox"),
		hosts.MTASTS: router.MTASTSPolicy(hosts.Inbox, cfg.mtaSTSMode, 604800),
	}

	if cfg.mailboxID != "" {
		mailbox := inbox.New(inbox.Config{MailboxID: cfg.mailboxID, Address: cfg.mailboxAddress}, p.store, p.keys, p.auth, log)
		go func() {
			// Create or unlock the mailbox early; operations retry lazily if KMS is not reachable yet.
			if err := mailbox.Ensure(ctx); err != nil {
				log.Error("inbox: ensure mailbox", "err", err)
			}
		}()
		api.Handle("/v1/", httpapi.TokenAuth(cfg.mailboxTokenSHA256, httpapi.MailboxAPI(mailbox)))
		if p.handoff != nil {
			api.Handle("/v1/admin/handoff", httpapi.TokenAuth(cfg.adminTokenSHA256, httpapi.Handoff(p.handoff, log)))
		}
		if p.updateTrust != nil {
			api.Handle("/v1/admin/trust-root", httpapi.TokenAuth(cfg.adminTokenSHA256, httpapi.TrustRoot(p.updateTrust)))
		}

		smtpSrv := smtpd.New(mailbox, smtpd.Config{Domain: hosts.Inbox, TLS: p.certs.TLSConfig()}, log)
		go func() {
			if err := smtpSrv.Serve(p.smtpListener); err != nil && ctx.Err() == nil {
				log.Error("smtpd: stopped", "err", err)
			}
		}()
		go func() {
			<-ctx.Done()
			_ = smtpSrv.Close()
		}()
	}

	common := map[string]http.Handler{
		"/attestation": httpapi.Attestation(p.attester, p.certs.UserData),
		"/status":      httpapi.Status(httpapi.StatusSources{Root: p.rootStatus, TLS: p.certs.Status, Sigstore: p.sigstore, Anchors: p.anchors}),
		"/version":     httpapi.Version(),
	}
	srv := &http.Server{
		Handler:           router.New(common, byHost),
		TLSConfig:         p.certs.TLSConfig("h2", "http/1.1"),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
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
