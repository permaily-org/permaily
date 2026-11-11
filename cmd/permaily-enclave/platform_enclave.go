//go:build !dev

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
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
			anchored := &store.Anchored{Records: awsStore, Commits: &store.S3Commits{
				S3: s3Client, Bucket: cfg.anchorBucket, Key: root.CommitKey,
				Retention: time.Duration(cfg.anchorRetentionDays) * 24 * time.Hour,
			}}
			records = anchored
			p.anchors = func() *attestation.AnchorStatus {
				return anchorStatus(anchored, cfg.anchorBucket, cfg.anchorRetentionDays)
			}
		}
		identity = &tlsacme.Sealed{Records: records, Keys: p.keys, Name: "tls-" + directory.Host}
	}
	if p.certs, err = tlsacme.New(hosts.All(), cfg.acmeDirectory, egress, identity, log); err != nil {
		return nil, err
	}
	go p.certs.Run(ctx)
	return p, nil
}

// anchorStatus reports the records an enclave refuses an earlier copy of, as
// far as it read or wrote them since it started.
func anchorStatus(a *store.Anchored, bucket string, retentionDays int) *attestation.AnchorStatus {
	st := &attestation.AnchorStatus{Bucket: bucket, RetentionDays: retentionDays, Records: []attestation.AnchoredRecord{}}
	for _, c := range a.Seen() {
		st.Records = append(st.Records, attestation.AnchoredRecord{Name: c.Name, Version: c.Version, CommittedAt: c.At, LockedUntil: c.LockedUntil})
	}
	for _, r := range a.Regressions() {
		st.Regressions = append(st.Regressions, attestation.AnchorRegression{Name: r.Name, Had: r.Had, Found: r.Found, At: r.At})
	}
	return st
}
