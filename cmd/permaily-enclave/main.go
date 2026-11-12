// Command permaily-enclave is the service that runs inside the Nitro Enclave.
//
// It terminates TLS itself (certificate obtained with ACME TLS-ALPN-01 from
// inside the enclave), serves app./api./mcp./inbox./mta-sts.inbox. by Host,
// answers GET /attestation?nonce=<hex> with an NSM attestation document whose
// user_data is the SHA-256 of the TLS certificate's SPKI, reports the key that
// holds its root secret on GET /status, and receives mail for the mailboxes of
// accounts over SMTP (ADR 0026), encrypting it under a root secret that only
// an enclave running a public release can obtain (ADR 0013).
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
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/smtpd"
	"github.com/permaily-org/permaily/internal/webui"
	"github.com/permaily-org/permaily/pkg/attestation"
	"github.com/permaily-org/permaily/pkg/provenance"
)

type config struct {
	domain        string
	acmeDirectory string
	port          uint32
	egressPort    uint32

	// Storage and mail. All of it is compiled into the image (entry point
	// flags): storage and SMTP are on whenever a table is named, and the
	// mailboxes of accounts whenever accounts are (mailFrom).
	smtpPort    uint32
	mtaSTSMode  string
	awsRegion   string
	dynamoTable string
	s3Bucket    string
	// Where the commits of the records that must not go back are kept (ADR 0016).
	anchorBucket        string
	anchorRetentionDays int
	credentialsPort     uint32
	// mailFrom is the address the service's own mail is sent from, through
	// SES. Empty leaves accounts off: nobody could sign in.
	mailFrom string
	// superAdmin is the contact address of the super admin's account, who
	// makes and removes the other admins of the admin panel (ADR 0022).
	superAdmin  string
	dohEndpoint string
	logPort     uint32

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
	flag.StringVar(&cfg.dynamoTable, "dynamodb-table", "", "DynamoDB table for accounts, mailboxes and messages; empty leaves storage and SMTP off")
	flag.StringVar(&cfg.s3Bucket, "s3-bucket", "", "S3 bucket for raw messages")
	flag.StringVar(&cfg.anchorBucket, "anchor-bucket", "", "S3 bucket with Object Lock for the commits of the records that must not be put back to an earlier copy")
	flag.IntVar(&cfg.anchorRetentionDays, "anchor-retention-days", 0, "days S3 keeps each commit, whatever anyone asks")
	flag.StringVar(&cfg.mailFrom, "mail-from", "", "address the service's own mail is sent from, through SES; empty leaves accounts off")
	flag.StringVar(&cfg.superAdmin, "super-admin", defaultSuperAdmin, "contact address of the super admin's account; empty leaves the admin panel to nobody")
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

// providerCallback is where a provider sends the browser back, on the app's
// host. It is registered with each provider, to the letter.
const providerCallback = "/api/v1/auth/provider/callback"

// keyRoutes mounts the operator's handoff of the root secret to the next
// release, and a newer trust root of Sigstore's, each when the platform has
// it: on their own conditions, whatever else the image runs.
func keyRoutes(api *http.ServeMux, cfg config, p *platform, log *slog.Logger) {
	if p.handoff != nil {
		api.Handle("/v1/admin/handoff", httpapi.TokenAuth(cfg.adminTokenSHA256, httpapi.Handoff(p.handoff, log)))
	}
	if p.updateTrust != nil {
		api.Handle("/v1/admin/trust-root", httpapi.TokenAuth(cfg.adminTokenSHA256, httpapi.TrustRoot(p.updateTrust)))
	}
}

// The mailboxes service is what three adapters ask, and accounts asks it.
var (
	_ httpapi.MailboxesService = (*mailboxes.Service)(nil)
	_ accounts.Mailboxes       = (*mailboxes.Service)(nil)
	_ smtpd.Recipients         = (*mailboxes.Service)(nil)
	_ mailboxes.Accounts       = (*accounts.Service)(nil)
)

// nobody is the recipients of an image without mailboxes: every address is
// refused like one never made.
type nobody struct{}

func (nobody) Accept(context.Context, string) (string, error) { return "", mailboxes.ErrNoSuchMailbox }
func (nobody) Deliver(context.Context, string, []string, []byte) error {
	return mailboxes.ErrNotAllStored
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
	log.Info("starting", "commit", buildinfo.Commit, "mode", p.mode, "hosts", hosts.All(), "acme", cfg.acmeDirectory, "smtp", p.smtpListener != nil, "mailboxes", p.mailboxes != nil)

	api := http.NewServeMux()
	api.Handle("/", router.Hello("api"))
	byHost := map[string]http.Handler{
		hosts.App:    webui.Handler(),
		hosts.API:    api,
		hosts.MCP:    router.Hello("mcp"),
		hosts.Inbox:  router.Hello("inbox"),
		hosts.MTASTS: router.MTASTSPolicy(hosts.Inbox, cfg.mtaSTSMode, 604800),
	}

	if p.accounts != nil {
		// The web app and its own API share a host, so the session cookie
		// never leaves it. Invitations are the operator's, on the API host.
		app := http.NewServeMux()
		app.Handle("/api/", httpapi.AccountsAPI(p.accounts, log))
		if p.mailboxes != nil {
			// The person's mailboxes and their mail (ADR 0026, "The API").
			mine := httpapi.MailboxesAPI(httpapi.MailboxesConfig{Service: p.mailboxes, Reader: p.reader, Sessions: p.accounts, Log: log})
			app.Handle("/api/v1/mailboxes", mine)
			app.Handle("/api/v1/mailboxes/", mine)
		}
		for path, h := range p.devRoutes {
			app.Handle(path, h)
		}
		app.Handle("/", webui.Handler())
		byHost[hosts.App] = app
		invitations := httpapi.TokenAuth(cfg.adminTokenSHA256, httpapi.Invitations(p.accounts, log))
		api.Handle("/v1/admin/invitations", invitations)
		api.Handle("/v1/admin/invitations/", invitations)
		// The operator's tool: it looks at accounts, blocks, asks to delete and
		// signs devices out, and changes no address and no way to sign in.
		operator := httpapi.TokenAuth(cfg.adminTokenSHA256, httpapi.Accounts(p.accounts, log))
		api.Handle("/v1/admin/accounts", operator)
		api.Handle("/v1/admin/accounts/", operator)
		// The applications registered at Google, GitHub, Meta and Microsoft:
		// their secrets come in here, over the attested connection.
		providers := httpapi.TokenAuth(cfg.adminTokenSHA256, httpapi.ProviderApps(p.accounts, log))
		api.Handle("/v1/admin/providers", providers)
		api.Handle("/v1/admin/providers/", providers)
	}

	keyRoutes(api, cfg, p, log)

	if p.mailboxes != nil {
		// Every enclave sweeps what disconnected mailboxes left.
		go p.mailboxes.Run(ctx)
	}
	if p.smtpListener != nil {
		var recipients smtpd.Recipients = nobody{}
		if p.mailboxes != nil {
			recipients = p.mailboxes
		}
		smtpSrv := smtpd.New(recipients, smtpd.Config{Domain: hosts.Inbox, TLS: p.certs.TLSConfig()}, log)
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

	status := httpapi.StatusSources{Root: p.rootStatus, TLS: p.certs.Status, Sigstore: p.sigstore, Anchors: p.anchors, Mail: p.mail}
	if p.accounts != nil {
		// What the operator did to accounts through this enclave: kinds and
		// counts, never an account.
		status.Admin = func() *attestation.AdminStatus {
			since, actions := p.accounts.OperatorActions()
			return &attestation.AdminStatus{Since: since, Actions: actions}
		}
	}
	common := map[string]http.Handler{
		"/attestation": httpapi.Attestation(p.attester, p.certs.UserData),
		"/status":      httpapi.Status(status),
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
