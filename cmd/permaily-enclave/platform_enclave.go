//go:build !dev

package main

import (
	"context"
	"errors"
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
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/msgauth"
	"github.com/permaily-org/permaily/internal/nsm"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/services/accounts"
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
	keys         keys.Service
	store        store.Store
	auth         inbox.Authenticator
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
}

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

	// Without storage (no mailbox) the TLS key lives only in memory.
	var identity tlsacme.Persistence
	if cfg.mailboxID != "" {
		if cfg.awsAccount == "" || cfg.dynamoTable == "" || cfg.s3Bucket == "" || cfg.mailboxTokenSHA256 == "" {
			return nil, errors.New("mailbox enabled but -aws-account, -dynamodb-table, -s3-bucket or -mailbox-token-sha256 is missing")
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
		p.store = awsStore
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
		p.keys, p.rootStatus, p.handoff = root, root.Status, root.HandOff
		p.sigstore, p.updateTrust = root.Trust, root.UpdateTrust
		go root.Run(ctx)
		p.auth = msgauth.DKIM(msgauth.DoH{Client: egress, Endpoint: cfg.dohEndpoint}.LookupTXT)

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
			commits := &store.S3Commits{
				S3: s3Client, Bucket: cfg.anchorBucket, Key: root.CommitKey,
				Retention: time.Duration(cfg.anchorRetentionDays) * 24 * time.Hour,
			}
			anchored := &store.Anchored{Records: awsStore, Commits: commits}
			records = anchored
			// Accounts keep their records apart: /status names the records it
			// lists, and the name of an account's record is its identifier.
			// Only the newest version of one is kept, so a name or a passkey
			// that was removed does not stay in the table (ADR 0017); the TLS
			// identity above keeps every version (ADR 0016).
			var accountRecords *store.Anchored
			if cfg.mailFrom != "" {
				accountRecords = &store.Anchored{Records: awsStore, Commits: commits, OnlyNewest: true}
				if err := p.signIn(ctx, cfg, hosts, awsCfg, egress, accountRecords, awsStore, root, log); err != nil {
					return nil, err
				}
			}
			p.anchors = func() *attestation.AnchorStatus {
				return anchorStatus(anchored, accountRecords, cfg.anchorBucket, cfg.anchorRetentionDays)
			}
		} else if cfg.mailFrom != "" {
			// An account's ways to sign in must not come back (ADR 0017).
			return nil, errors.New("-mail-from needs -anchor-bucket")
		}
		identity = &tlsacme.Sealed{Records: records, Keys: p.keys, Name: "tls-" + directory.Host}
	}
	if p.certs, err = tlsacme.New(hosts.All(), cfg.acmeDirectory, egress, identity, log); err != nil {
		return nil, err
	}
	go p.certs.Run(ctx)
	return p, nil
}

// signIn turns accounts on: the mail of a sign-in goes out through SES, and
// only while SES says nobody else gets a copy of it.
func (p *platform) signIn(ctx context.Context, cfg config, hosts router.Hosts, awsCfg aws.Config, egress *http.Client, records accounts.Records, entries store.Entries, root *keys.Root, log *slog.Logger) error {
	_, domain, ok := strings.Cut(cfg.mailFrom, "@")
	if !ok || domain == "" {
		return errors.New("-mail-from is not an address")
	}
	watch := &sesmail.Watch{
		HTTP: egress, Credentials: awsCfg.Credentials, Region: cfg.awsRegion,
		Domain: domain, Address: cfg.mailFrom, Log: log,
	}
	go watch.Run(ctx)
	p.mail = watch.Status
	var err error
	p.accounts, err = accounts.New(accounts.Config{
		Records: records, Entries: entries, Keys: root,
		Mailer: &sesmail.Sender{
			HTTP: egress, Credentials: awsCfg.Credentials, Region: cfg.awsRegion,
			Name: "Permaily", Address: cfg.mailFrom, Watch: watch, Log: log,
		},
		AppURL: "https://" + hosts.App, Log: log,
	})
	if err != nil {
		return err
	}
	// Accounts whose person asked to delete them are removed when their day comes.
	go p.accounts.Run(ctx, accounts.RemoveEvery)
	return nil
}

// accountRecord stands for the name of an account's record in /status: the
// name is the account's identifier, and what /status says is public.
const accountRecord = "the record of an account"

// anchorStatus reports the records an enclave refuses an earlier copy of, as
// far as it read or wrote them since it started. Of the records of accounts
// (nil when there are none) it reports only that one went back, never which.
func anchorStatus(a, ofAccounts *store.Anchored, bucket string, retentionDays int) *attestation.AnchorStatus {
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
	return st
}
