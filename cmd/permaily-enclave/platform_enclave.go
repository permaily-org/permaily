//go:build !dev

package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/permaily-org/permaily/internal/enclave"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/msgauth"
	"github.com/permaily-org/permaily/internal/nsm"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/decisions"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/sesmail"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/tlsacme"
	"github.com/permaily-org/permaily/pkg/attestation"
	"github.com/permaily-org/permaily/pkg/provenance"
)

type platform struct {
	mode         string
	listener     net.Listener
	smtpListener net.Listener
	attester     nsm.Attester
	certs        *tlsacme.Manager
	// mailboxes is the mailboxes of accounts (ADR 0026), reader reads
	// their stored mail and matcher evaluates formulas over it (ADR 0027);
	// nil without accounts.
	mailboxes *mailboxes.Service
	reader    inbox.Reader
	matcher   inbox.Matcher
	// rootStatus describes the key that holds the root secret, and handoff
	// gives the root secret to the next release; nil without a root secret.
	rootStatus func(context.Context) (*attestation.RootStatus, error)
	handoff    func(context.Context, keys.Handoff) (*keys.HandoffResult, error)
	// sigstore says which trust root releases are checked against, and
	// updateTrust takes a newer one that Sigstore published.
	sigstore    func() *attestation.SigstoreStatus
	updateTrust func(provenance.Update) (*attestation.SigstoreStatus, error)
	// anchors describes the records that cannot be put back to an earlier copy.
	anchors func() *attestation.AnchorStatus
	// accounts is sign-in and accounts (ADR 0017); nil in an image that
	// sends no mail. devRoutes are served by the app's host in dev mode only.
	accounts  *accounts.Service
	devRoutes map[string]http.Handler
	// mail describes who else could get a copy of the mail the enclave
	// sends, as SES answers; nil when it sends none.
	mail func() *attestation.MailStatus
	// model asks the model what depends on meaning (ADR 0028), and holds
	// its key, its spend, its canary and its alarms; nil without mailboxes.
	model *decisions.Service
	// connections is the authorization server's state and the connections
	// it makes (ADR 0030); nil without the model. connectionRecords is the
	// one strict Anchored of its records.
	connections       *composedConnections
	connectionRecords *store.Anchored
	// rulesRecords is the one strict Anchored of the person's rules'
	// records (ADR 0040); nil without connections.
	rulesRecords *store.Anchored
	// notices is the queue of the notices by mail about connections and
	// the record of what left (ADR 0041), accounts its Deliverer; nil in an
	// image that sends no mail.
	notices *composedNotices
	// receipts is the record of what left, chained (ADR 0037), over its one
	// strict Anchored; nil without the model.
	receipts *composedReceipts
}

// devConfig is empty in the enclave: the image reaches vendors through the
// host's vendor egress alone, and has no flag that names another.
type devConfig struct{}

func devFlags(*flag.FlagSet, *devConfig) {}

// defaultSuperAdmin is empty in the enclave: the super admin's address is
// compiled into the image (-super-admin, from deploy/enclave.json).
const defaultSuperAdmin = ""

// logOutput writes to the console and, when a log port is set, to the parent
// instance over vsock.
func logOutput(cfg config) io.Writer {
	if cfg.logPort == 0 {
		return os.Stdout
	}
	return io.MultiWriter(os.Stdout, enclave.NewLogSink(context.Background(), cfg.logPort))
}

func newPlatform(ctx context.Context, cfg config, hosts router.Hosts, log *slog.Logger) (*platform, error) {
	att, err := nsm.Open()
	if err != nil {
		return nil, err
	}
	egress := enclave.EgressClient(cfg.egressPort)
	ln, err := enclave.Listen(cfg.port)
	if err != nil {
		return nil, err
	}
	p := &platform{mode: "enclave", listener: ln, attester: att}

	// Without storage (no table) the TLS key lives only in memory, and there
	// is no SMTP.
	var identity tlsacme.Persistence
	if cfg.dynamoTable != "" {
		if cfg.awsAccount == "" || cfg.s3Bucket == "" {
			return nil, errors.New("-dynamodb-table needs -aws-account and -s3-bucket")
		}
		if p.smtpListener, err = enclave.Listen(cfg.smtpPort); err != nil {
			return nil, err
		}
		awsCfg := aws.Config{
			Region:      cfg.awsRegion,
			Credentials: aws.NewCredentialsCache(enclave.HostCredentials{Port: cfg.credentialsPort}),
			HTTPClient:  egress,
		}
		kmsClient := kms.NewFromConfig(awsCfg)
		// Path-style keeps every request on s3.<region>.amazonaws.com (one allowlist entry).
		s3Client := s3.NewFromConfig(awsCfg, func(o *s3.Options) { o.UsePathStyle = true })
		awsStore := &store.AWS{
			DB:     dynamodb.NewFromConfig(awsCfg),
			S3:     s3Client,
			Table:  cfg.dynamoTable,
			Bucket: cfg.s3Bucket,
		}
		// Sigstore's trust root is compiled into the image, like the policy
		// that says which builds are releases.
		trust, err := provenance.PublicGood()
		if err != nil {
			return nil, err
		}
		root, err := keys.NewRoot(keys.RootConfig{
			API: kmsClient, Attester: att, Records: awsStore, Account: cfg.awsAccount, Region: cfg.awsRegion,
			Releases: cfg.releases, Trust: trust, Log: log,
		})
		if err != nil {
			return nil, err
		}
		p.rootStatus, p.handoff = root.Status, root.HandOff
		p.sigstore, p.updateTrust = root.Trust, root.UpdateTrust
		go root.Run(ctx)

		// One identity per CA: a production and a staging image never share
		// an ACME account.
		directory, err := url.Parse(cfg.acmeDirectory)
		if err != nil {
			return nil, err
		}
		// The TLS identity must not be put back to an earlier copy: a key that
		// was replaced because it may have leaked would come back (ADR 0016).
		var records store.Records = awsStore
		if cfg.anchorBucket != "" {
			if cfg.anchorRetentionDays < 1 {
				return nil, errors.New("-anchor-bucket needs -anchor-retention-days")
			}
			commits := anchorCommits(s3Client, cfg.anchorBucket, root.CommitKey, cfg.anchorRetentionDays)
			anchored := &store.Anchored{Records: awsStore, Commits: commits}
			records = anchored
			// Accounts keep their records apart: /status names the records it
			// lists, and the name of an account's record is its identifier.
			// Only the newest version of one is kept, so a name or a passkey
			// that was removed does not stay in the table (ADR 0017); the TLS
			// identity above keeps every version (ADR 0016).
			var accountRecords, mailboxRecords, modelRecords, connectionRecords, rulesRecords, chainRecords *store.Anchored
			if cfg.mailFrom != "" {
				accountRecords = &store.Anchored{Records: awsStore, Commits: commits, OnlyNewest: true}
				// The notices about connections (ADR 0041) are queued in the
				// table, signed with a key of the root secret, and sent by
				// accounts through SES, behind the watch, as every message.
				if p.notices, err = composeNotices(noticesParts{entries: awsStore, keys: root, log: log}); err != nil {
					return nil, err
				}
				sender, err := p.signIn(ctx, cfg, hosts, awsCfg, egress, accountRecords, awsStore, root, log)
				if err != nil {
					return nil, err
				}
				// The mailboxes' records and the accounts' lists are strict, and
				// are read and written through this Anchored alone, which reads
				// no other name: a fork anywhere in their history refuses them
				// (ADR 0026, "The store").
				mailboxRecords = &store.Anchored{Records: awsStore, Commits: commits, OnlyNewest: true, Strict: true}
				if p.mailboxes, err = mailboxes.New(mailboxes.Config{
					Records: mailboxRecords, Storage: awsStore, Keys: root, Accounts: p.accounts, Notifier: p.accounts, Domain: hosts.Inbox, Log: log,
					Auth: msgauth.DKIM(msgauth.DoH{Client: egress, Endpoint: cfg.dohEndpoint}.LookupTXT),
				}); err != nil {
					return nil, err
				}
				p.accounts.SetMailboxes(p.mailboxes)
				p.reader = inbox.NewReader(awsStore)
				p.matcher = inbox.NewMatcher(awsStore, query.Eval)
				// The model (ADR 0028): its key, each month's spend, the
				// canary's state and the alarms are strict records, read and
				// written through this Anchored alone, which reads no other
				// name; it asks OpenAI through the host's proxy over TLS that
				// ends here, and its alarms go to the super admin through SES.
				modelRecords = &store.Anchored{Records: awsStore, Commits: commits, OnlyNewest: true, Strict: true}
				if p.model, err = composeModel(modelParts{
					cfg: cfg.model, egress: egress, records: modelRecords, entries: awsStore, keys: root,
					mailer: sender, superAdmin: cfg.superAdmin, log: log,
				}); err != nil {
					return nil, err
				}
				// Connections (ADR 0030): a connection, an account's list of
				// them, a token family and a record of assertions are strict
				// records, read and written through this Anchored alone,
				// which reads no other name; vendors' documents come through
				// the host's vendor egress (port 8004) and nothing else.
				connectionRecords = &store.Anchored{Records: awsStore, Commits: commits, OnlyNewest: true, Strict: true}
				p.connectionRecords = connectionRecords
				// The person's rules (ADR 0040): an account's record of rules
				// is a strict record, read and written through this Anchored
				// alone, which reads no other name.
				rulesRecords = &store.Anchored{Records: awsStore, Commits: commits, OnlyNewest: true, Strict: true}
				p.rulesRecords = rulesRecords
				// The record of what left, chained (ADR 0037): links, the
				// catalogue of chains, the audit's passes and the record of
				// incidents are strict records made once, read and written
				// through this Anchored alone, which reads no other name; their
				// values in the chain's table (a link's in its account's
				// partition), their commits grouped under commits/g/.
				chainRecords = &store.Anchored{Records: receipts.ChainTable{Entries: awsStore}, Commits: commits, Strict: true}
				if p.receipts, err = composeReceipts(receiptsParts{chain: chainRecords, entries: awsStore, keys: root, accounts: p.accounts, notices: p.notices, log: log}); err != nil {
					return nil, err
				}
				if p.connections, err = enclaveConnections(cfg, hosts, connectionRecords, awsStore, root, p, log); err != nil {
					return nil, err
				}
			}
			p.anchors = func() *attestation.AnchorStatus {
				st := anchorStatus(anchored, accountRecords, mailboxRecords, modelRecords, connectionRecords, rulesRecords, cfg.anchorBucket, cfg.anchorRetentionDays)
				st.Regressions = append(st.Regressions, receiptRegressions(chainRecords)...)
				return st
			}
		} else if cfg.mailFrom != "" {
			// An account's ways to sign in must not come back (ADR 0017).
			return nil, errors.New("-mail-from needs -anchor-bucket")
		}
		identity = &tlsacme.Sealed{Records: records, Keys: root, Challenges: awsStore, Name: "tls-" + directory.Host}
	}
	if p.certs, err = tlsacme.New(hosts.All(), cfg.acmeDirectory, egress, identity, log); err != nil {
		return nil, err
	}
	go p.certs.Run(ctx)
	return p, nil
}

// signIn turns accounts on: the mail of a sign-in goes out through SES, and
// only while SES says nobody else gets a copy of it.
//
// It returns the sender of that mail, which the model's alarms use too.
func (p *platform) signIn(ctx context.Context, cfg config, hosts router.Hosts, awsCfg aws.Config, egress *http.Client, records accounts.Records, entries store.Entries, root *keys.Root, log *slog.Logger) (*sesmail.Sender, error) {
	_, domain, ok := strings.Cut(cfg.mailFrom, "@")
	if !ok || domain == "" {
		return nil, errors.New("-mail-from is not an address")
	}
	watch := &sesmail.Watch{
		HTTP: egress, Credentials: awsCfg.Credentials, Region: cfg.awsRegion,
		Domain: domain, Address: cfg.mailFrom, Log: log,
	}
	go watch.Run(ctx)
	p.mail = watch.Status
	sender := &sesmail.Sender{
		HTTP: egress, Credentials: awsCfg.Credentials, Region: cfg.awsRegion,
		Name: "Permaily", Address: cfg.mailFrom, Watch: watch, Log: log,
	}
	var err error
	p.accounts, err = accounts.New(accounts.Config{
		Records: records, Entries: entries, Keys: root,
		Mailer: sender,
		// The operator's and an admin's sign-out of every device is told
		// through the queue, which accounts then sends (deliverBy).
		Notices: p.notices.svc,
		// The providers are asked who signed in over TLS that ends here, through
		// the host's proxy, and send the browser back to the app's own host.
		Providers: &idp.Client{HTTP: egress, RedirectURL: "https://" + hosts.App + providerCallback},
		AppURL:    "https://" + hosts.App, SuperAdmin: cfg.superAdmin, Log: log,
	})
	if err != nil {
		return nil, err
	}
	p.notices.deliverBy(p.accounts)
	// Accounts whose person asked to delete them are removed when their day comes.
	go p.accounts.Run(ctx, accounts.RemoveEvery)
	return sender, nil
}

// accountRecord stands for the name of an account's record in /status: the
// name is the account's identifier, and what /status says is public.
const accountRecord = "the record of an account"

// mailboxRecord stands, the same way, for a mailbox's record or an account's
// list of mailboxes, whose names hold identifiers too (ADR 0026).
const mailboxRecord = "mailbox"

// modelRecord stands for the model's records (ADR 0028) in /status: their
// names say nothing of a person, but the versions of a month's spend say
// how much was spent, which /status never says.
const modelRecord = "model"

// connectionRecord stands for the connections' records (ADR 0030) in
// /status: their names hold a connection's, an account's or a family's
// identifier, and their versions say how often a connection was used.
const connectionRecord = "connection"

// rulesRecord stands for an account's record of rules (ADR 0040) in
// /status: its name holds the account's identifier, and its versions say
// how often the person changed their rules.
const rulesRecord = "rules"

// vendorEgress is the client vendors' documents and key sets are fetched
// with: the host's vendor egress on port, HTTP/2 only. A test replaces it
// to see which port the image uses.
var vendorEgress = enclave.VendorEgressClient

// enclaveConnections composes the image's connections over records (the
// one strict Anchored of their names), the table and the root secret's
// keys, with the platform's accounts, mailboxes, model, receipts and queue
// of notices (log takes what it logs of a notice not queued); vendors'
// documents are fetched through the host's vendor egress on
// cfg.vendorEgressPort (8004) and nothing else.
func enclaveConnections(cfg config, hosts router.Hosts, records *store.Anchored, table connectionsStore, root connections.Keys, p *platform, log *slog.Logger) (*composedConnections, error) {
	return composeConnections(connectionsParts{
		oauth: oauthConfig(hosts, cfg.oauthPort()), domain: cfg.domain, records: records, entries: table, keys: root,
		vendorHTTP: vendorEgress(cfg.vendorEgressPort), accounts: p.accounts, mailboxes: p.mailboxes, model: p.model,
		receipts: p.receipts, notices: p.notices, log: log, rulesRecords: p.rulesRecords,
	})
}

// anchorCommits is where every strict record's commits are, in the anchors
// bucket: the chain's records grouped by receipts.CommitGroup (ADR 0037,
// "Where a link is kept"), so a grouped name's commits are read and written
// there and nowhere else, by any Anchored; every other name's where they
// always were.
func anchorCommits(api store.S3CommitsAPI, bucket string, key func(context.Context) ([]byte, error), retentionDays int) *store.S3Commits {
	return &store.S3Commits{
		S3: api, Bucket: bucket, Key: key,
		Retention: time.Duration(retentionDays) * 24 * time.Hour,
		Group:     receipts.CommitGroup,
	}
}

// anchorStatus reports the records an enclave refuses an earlier copy of, as
// far as it read or wrote them since it started. Of the records of accounts
// and of mailboxes (nil when there are none) it reports only that one went
// back, was forked or has a commit S3 did not keep locked, never which; of
// the model's records and the connections' the same, without their
// versions.
func anchorStatus(a, ofAccounts, ofMailboxes, ofModel, ofConnections, ofRules *store.Anchored, bucket string, retentionDays int) *attestation.AnchorStatus {
	st := &attestation.AnchorStatus{Bucket: bucket, RetentionDays: retentionDays, Records: []attestation.AnchoredRecord{}}
	for _, c := range a.Seen() {
		st.Records = append(st.Records, attestation.AnchoredRecord{Name: c.Name, Version: c.Version, CommittedAt: c.At, LockedUntil: c.LockedUntil})
	}
	for _, r := range a.Regressions() {
		st.Regressions = append(st.Regressions, attestation.AnchorRegression{Name: r.Name, Had: r.Had, Found: r.Found, At: r.At})
	}
	if ofAccounts != nil {
		for _, r := range ofAccounts.Regressions() {
			st.Regressions = append(st.Regressions, attestation.AnchorRegression{Name: accountRecord, Had: r.Had, Found: r.Found, At: r.At})
		}
	}
	if ofMailboxes != nil {
		for _, r := range ofMailboxes.Regressions() {
			st.Regressions = append(st.Regressions, attestation.AnchorRegression{Name: mailboxRecord, Had: r.Had, Found: r.Found, At: r.At, Fork: r.Fork, Unlocked: r.Unlocked, Lost: r.Lost})
		}
	}
	if ofModel != nil {
		for _, r := range ofModel.Regressions() {
			st.Regressions = append(st.Regressions, attestation.AnchorRegression{Name: modelRecord, At: r.At, Fork: r.Fork, Unlocked: r.Unlocked, Lost: r.Lost})
		}
	}
	if ofConnections != nil {
		for _, r := range ofConnections.Regressions() {
			st.Regressions = append(st.Regressions, attestation.AnchorRegression{Name: connectionRecord, At: r.At, Fork: r.Fork, Unlocked: r.Unlocked, Lost: r.Lost})
		}
	}
	if ofRules != nil {
		for _, r := range ofRules.Regressions() {
			st.Regressions = append(st.Regressions, attestation.AnchorRegression{Name: rulesRecord, At: r.At, Fork: r.Fork, Unlocked: r.Unlocked, Lost: r.Lost})
		}
	}
	return st
}
