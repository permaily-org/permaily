// Command permaily-enclave is the service that runs inside the Nitro Enclave.
//
// It terminates TLS itself (certificate obtained with ACME TLS-ALPN-01 from
// inside the enclave), serves app./api./mcp./inbox./mta-sts.inbox. by Host,
// answers GET /attestation?nonce=<hex> with an NSM attestation document whose
// user_data is the SHA-256 of the TLS certificate's SPKI, reports the key that
// holds its root secret on GET /status, and receives mail for the mailboxes of
// accounts over SMTP (ADR 0026), encrypting it under a root secret that only
// an enclave running a public release can obtain (ADR 0013). With them it
// serves connections (ADR 0030 and 0031): the authorization server and the
// API for apps on api., MCP for agents on mcp., the consent screen on app.,
// vendors' documents fetched through the host's vendor egress.
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
	// vendorEgressPort is the host's proxy that reaches any public name on
	// 443 for vendors' documents (vendorEgressPort, ADR 0030): the
	// connections' vendor client uses it and nothing else does.
	vendorEgressPort uint32

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

	// model is what the image says of the model it asks (ADR 0028,
	// deploy/enclave.json "model"); it is composed wherever the mailboxes
	// of accounts are.
	model modelConfig
	// modelTicks is never a flag: the image's model runs on tickers, and
	// only a test gives ticks of its own.
	modelTicks modelTicks
	// sweepTick, never a flag either, ticks the connections' hourly pass in
	// place of its ticker: a test's.
	sweepTick <-chan time.Time
	// refusalClock, never a flag either, is the refusals' pass's clock in
	// tests (runConnections); zero, time.Now and time.After.
	refusalClock refusalClock
	// receiptsTicks, never a flag either, tick the audit and the reads of
	// the record of incidents in tests (runReceipts); zero, their tickers.
	receiptsTicks receiptsTicks
	// clientPort, never a flag, is the port clients name in the
	// authorization server's addresses when it is not port: a test's two
	// dev enclaves behind one address, as the deployment's two are behind
	// 443 (oauthPort).
	clientPort uint32
	// dev is what only dev mode reads (the played vendors); empty in the
	// enclave build, which has no such flag.
	dev devConfig
}

func main() {
	cfg := newConfig(flag.CommandLine)
	flag.Parse()

	// Logs go to the console (visible only in debug mode) and to the parent
	// instance. Both are outside the enclave: nothing sensitive may be logged.
	log := slog.New(slog.NewJSONHandler(logOutput(*cfg), nil))
	if err := run(*cfg, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// newConfig is the image's configuration: its defaults, and the flags that
// fs parses into it. main parses the command line with it, and the tests
// that check what the image composes (V57) read their configuration from it
// too, so a default changed here is a default they see.
func newConfig(fs *flag.FlagSet) *config {
	cfg := new(config)
	fs.StringVar(&cfg.domain, "domain", "permaily.com", "parent domain; serves app., api., mcp., inbox. and mta-sts.inbox. under it")
	fs.StringVar(&cfg.acmeDirectory, "acme-directory", "https://acme-v02.api.letsencrypt.org/directory", "ACME directory URL")
	cfg.port, cfg.egressPort, cfg.smtpPort, cfg.credentialsPort, cfg.logPort = 443, 8000, 25, 8002, 8003
	cfg.vendorEgressPort = vendorEgressPort
	fs.Func("log-port", "vsock port of the host that receives the log lines; 0 disables (default 8003)", uint32Flag(&cfg.logPort))
	fs.Func("port", "HTTPS listen port (vsock in the enclave, TCP in dev mode) (default 443)", uint32Flag(&cfg.port))
	fs.Func("egress-port", "vsock port of the host's CONNECT proxy (default 8000)", uint32Flag(&cfg.egressPort))
	fs.Func("vendor-egress-port", "vsock port of the host's proxy to vendors' documents, any public name on 443 (default 8004)", uint32Flag(&cfg.vendorEgressPort))
	fs.Func("smtp-port", "SMTP listen port (vsock in the enclave, TCP in dev mode) (default 25)", uint32Flag(&cfg.smtpPort))
	fs.Func("credentials-port", "vsock port of the host's credentials endpoint (default 8002)", uint32Flag(&cfg.credentialsPort))
	fs.StringVar(&cfg.mtaSTSMode, "mta-sts-mode", "testing", "MTA-STS policy mode: testing or enforce")
	fs.StringVar(&cfg.awsRegion, "aws-region", "us-east-2", "AWS region of KMS, DynamoDB and S3")
	fs.StringVar(&cfg.awsAccount, "aws-account", "", "AWS account that holds the KMS key of each release (pinned)")
	fs.StringVar(&cfg.adminTokenSHA256, "admin-token-sha256", "", "hex SHA-256 of the bearer token that may ask for a handoff")
	fs.StringVar(&cfg.releases.Repository, "release-repository", "", "owner/name of the GitHub repository whose releases may receive the root secret")
	fs.StringVar(&cfg.releases.RepositoryID, "release-repository-id", "", "GitHub's numeric id of that repository")
	fs.StringVar(&cfg.releases.OwnerID, "release-owner-id", "", "GitHub's numeric id of the repository's owner")
	fs.StringVar(&cfg.releases.Workflow, "release-workflow", "", "path of the workflow that publishes releases")
	fs.StringVar(&cfg.releases.Ref, "release-ref", "", "the git ref whose runs of that workflow are releases")
	fs.StringVar(&cfg.releases.RefPrefix, "release-ref-prefix", "", "instead of -release-ref: every ref that starts with this (test images)")
	fs.StringVar(&cfg.dynamoTable, "dynamodb-table", "", "DynamoDB table for accounts, mailboxes and messages; empty leaves storage and SMTP off")
	fs.StringVar(&cfg.s3Bucket, "s3-bucket", "", "S3 bucket for raw messages")
	fs.StringVar(&cfg.anchorBucket, "anchor-bucket", "", "S3 bucket with Object Lock for the commits of the records that must not be put back to an earlier copy")
	fs.IntVar(&cfg.anchorRetentionDays, "anchor-retention-days", 0, "days S3 keeps each commit, whatever anyone asks")
	fs.StringVar(&cfg.mailFrom, "mail-from", "", "address the service's own mail is sent from, through SES; empty leaves accounts off")
	fs.StringVar(&cfg.superAdmin, "super-admin", defaultSuperAdmin, "contact address of the super admin's account; empty leaves the admin panel to nobody")
	fs.StringVar(&cfg.dohEndpoint, "doh-endpoint", "https://cloudflare-dns.com/dns-query", "DNS-over-HTTPS endpoint for DKIM lookups")
	modelFlags(fs, &cfg.model)
	devFlags(fs, &cfg.dev)
	return cfg
}

// providerCallback is where a provider sends the browser back, on the app's
// host: the one route under /api/ that a cross-site navigation reaches.
const providerCallback = httpapi.ProviderCallback

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

// oauthPort is the port the authorization server's addresses carry.
func (c config) oauthPort() uint32 {
	if c.clientPort != 0 {
		return c.clientPort
	}
	return c.port
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

	h := handler(cfg, hosts, p, log)

	if p.mailboxes != nil {
		// Every enclave sweeps what disconnected mailboxes left.
		go p.mailboxes.Run(ctx)
	}
	if p.model != nil {
		// Every enclave runs the day's canary, under its lease, and sends the
		// alarms any image left due (ADR 0028).
		runModel(ctx, p.model, log, cfg.modelTicks)
	}
	if p.receipts != nil {
		// Every enclave reads the record of incidents and the newest pass
		// for /status, and runs the daily audit under its lease (ADR 0037).
		runReceipts(ctx, p.receipts, log, cfg.receiptsTicks)
	}
	if p.notices != nil {
		// Every enclave runs the queue's runner: either may send a notice
		// the other queued (ADR 0041, "The queue").
		runNotices(ctx, p.notices)
	}
	var refusalsPassed <-chan struct{}
	if p.connections != nil {
		// Every enclave runs the connections' hourly pass (ADR 0030,
		// "Revocation and two enclaves").
		refusalsPassed = runConnections(ctx, p.connections.svc, log, cfg.sweepTick, cfg.refusalClock)
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

	srv := &http.Server{
		Handler: h,
		// The api and mcp hosts offer http/1.1 alone, the app's h2 too
		// (serverTLS); every connection is kept in its requests' context so
		// a lease that ends while mail is sent closes it (connContext).
		TLSConfig:         serverTLS(p.certs.TLSConfig("h2", "http/1.1"), hosts),
		ConnContext:       connContext,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}
	stopped := make(chan struct{})
	var stopErr error
	go func() {
		defer close(stopped)
		<-ctx.Done()
		// The notices' attempts under way finish while the server stops,
		// and nothing more is admitted (stopNotices).
		noticesStopped := make(chan error, 1)
		go func() {
			if p.notices == nil {
				noticesStopped <- nil
				return
			}
			noticesStopped <- stopNotices(p.notices, log)
		}()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			// Connections still open; the connections' requests among them
			// are waited for by stopConnections, which admits none more.
			log.Warn("the server's connections did not all close at the stop", "kind", "shutdown")
		}
		if p.connections != nil {
			// After the last request: what the refusals' windows counted.
			stopErr = stopConnections(p.connections.svc, refusalsPassed, log)
		}
		stopErr = errors.Join(stopErr, <-noticesStopped)
	}()
	if err := srv.ServeTLS(p.listener, "", ""); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-stopped
	return stopErr
}

// handler is every route the enclave serves, on every host: what run
// serves, and what a test drives with a platform of its own.
func handler(cfg config, hosts router.Hosts, p *platform, log *slog.Logger) http.Handler {
	api := http.NewServeMux()
	api.Handle("/", router.Hello("api"))
	mcp := http.NewServeMux()
	mcp.Handle("/", router.Hello("mcp"))
	// The api and mcp hosts' paths are answered only in canonical form,
	// before any ServeMux would redirect one with its query string (a code,
	// a token) to its cleaned path (httpapi.ResourceHost).
	byHost := map[string]http.Handler{
		hosts.App:    webui.Handler(),
		hosts.API:    httpapi.ResourceHost(api),
		hosts.MCP:    httpapi.ResourceHost(mcp),
		hosts.Inbox:  router.Hello("inbox"),
		hosts.MTASTS: router.MTASTSPolicy(hosts.Inbox, cfg.mtaSTSMode, 604800),
	}

	if p.accounts != nil {
		// The web app and its own API share a host, so the session cookie
		// never leaves it. Invitations are the operator's, on the API host.
		app := httpapi.NewAppMux()
		app.Handle("/api/", httpapi.AccountsAPI(p.accounts, log))
		var mine http.Handler
		if p.mailboxes != nil {
			// The person's mailboxes and their mail (ADR 0026, "The API"). A
			// page of matches asks the model only for an account that agreed
			// to the model's notice in force (ADR 0028, "Consent").
			mc := httpapi.MailboxesConfig{
				Service: p.mailboxes, Reader: p.reader, Matcher: p.matcher, ModelNotices: p.accounts, Sessions: p.accounts, Log: log, AppOrigin: appOrigin(hosts.App, cfg.port),
			}
			if p.model != nil {
				mc.Asker = p.model
			}
			// The rules' editor previews a draft as a page of matches is
			// admitted (ADR 0040, pull request 6): the same matcher, read
			// budget, search slots and pages a minute.
			var previews *httpapi.RulePreviews
			mine, previews = httpapi.MailboxesWithRulePreviews(mc)
			if p.connections != nil {
				// The person's own rules (ADR 0040): the list, words, the
				// changes, the previews and the rules' notice, on the app's
				// host; words through the model's egress and spend guard.
				httpapi.MountRules(app, httpapi.RulesConfig{
					Rules: p.connections.rules, Words: p.model, Standing: p.accounts, Previews: previews, Sessions: p.accounts, Log: log,
				})
			}
			app.Handle("/api/v1/mailboxes", mine)
			app.Handle("/api/v1/mailboxes/", mine)
			// A formula's canonical form and sentence, for the owner's own
			// screens (ADR 0027, "The routes"); its matches are a mailbox's.
			queries := httpapi.QueriesAPI(httpapi.QueriesConfig{Sessions: p.accounts, Mailboxes: p.mailboxes, Accounts: p.accounts, Log: log})
			app.Handle("/api/v1/queries", queries)
			app.Handle("/api/v1/queries/", queries)
		}
		if p.connections != nil {
			// Connections (ADR 0030): the authorization page and the
			// person's routes on the app's host, the authorization server
			// and the API on the api host, MCP on the mcp host.
			mountConnections(p.connections, app, api, mcp, webui.Handler(), p.accounts, log)
		}
		if p.receipts != nil {
			// The person's receipts and their export (ADR 0037, pull request 3).
			receiptsRoutes := httpapi.ReceiptsAPI(httpapi.ReceiptsConfig{Chain: p.receipts.svc, Sessions: p.accounts, Log: log})
			app.Handle("/api/v1/receipts", receiptsRoutes)
			app.Handle("/api/v1/receipts/", receiptsRoutes)
		}
		for path, h := range p.devRoutes {
			app.Handle(path, h)
		}
		app.Handle("/", webui.Handler())
		// One decision about a path, then the frame's path, then the rule
		// that every route under /api/ answers the app alone, before
		// anything else (httpapi.AppHost, ADR 0026).
		byHost[hosts.App] = httpapi.AppHost(app, mine)
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
		// Mailboxes (ADR 0026): find the one an address leads to, pause and
		// resume one; never read, disconnect or empty one. An account's
		// mailboxes are under /v1/admin/accounts.
		mailboxesOf := httpapi.TokenAuth(cfg.adminTokenSHA256, httpapi.Mailboxes(p.accounts, log))
		api.Handle("/v1/admin/mailboxes", mailboxesOf)
		api.Handle("/v1/admin/mailboxes/", mailboxesOf)
		if p.model != nil {
			// The model's key, given over the connection pinned to the
			// attested key (scripts/model-key.sh) and never answered back.
			api.Handle("/v1/admin/model-key", httpapi.TokenAuth(cfg.adminTokenSHA256, httpapi.ModelKey(p.model, p.accounts, log)))
		}
	}

	keyRoutes(api, cfg, p, log)

	status := httpapi.StatusSources{Root: p.rootStatus, TLS: p.certs.Status, Sigstore: p.sigstore, Anchors: p.anchors, Mail: p.mail}
	if p.accounts != nil {
		// What the operator did to accounts through this enclave: kinds and
		// counts, never an account.
		status.Admin = func() *attestation.AdminStatus {
			since, actions := p.accounts.OperatorActions()
			return &attestation.AdminStatus{Since: since, Actions: actions}
		}
	}
	if p.model != nil {
		status.Model = modelStatus(p.model, cfg.model)
	}
	if p.connections != nil {
		status.Connections = connectionsStatus(p.connections)
	}
	if p.notices != nil {
		// What this enclave did with the queue of notices, in mail.notices.
		status.Notices = p.notices.status
	}
	common := map[string]http.Handler{
		"/attestation": httpapi.Attestation(p.attester, p.certs.UserData),
		"/status":      httpapi.Status(status),
		"/version":     httpapi.Version(),
	}
	return router.New(common, byHost)
}

// appOrigin is the app's origin, the only one that may frame a message's
// HTML: its port written when it is not 443 (dev mode).
func appOrigin(host string, port uint32) string {
	if port == 443 {
		return "https://" + host
	}
	return "https://" + host + ":" + strconv.FormatUint(uint64(port), 10)
}
