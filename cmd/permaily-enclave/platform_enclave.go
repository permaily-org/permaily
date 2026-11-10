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
	// kmsStatus reads the policy of the pinned KMS key for /status; nil when
	// there is no KMS key (no mailbox, or dev mode).
	kmsStatus func(context.Context) (attestation.KMSStatus, error)
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
		if cfg.kmsKeyARN == "" || cfg.dynamoTable == "" || cfg.s3Bucket == "" || cfg.mailboxTokenSHA256 == "" {
			return nil, errors.New("mailbox enabled but -kms-key-arn, -dynamodb-table, -s3-bucket or -mailbox-token-sha256 is missing")
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
		if p.keys, err = keys.NewKMS(kmsClient, cfg.kmsKeyARN, att); err != nil {
			return nil, err
		}
		p.kmsStatus = keys.NewPolicyReader(kmsClient, cfg.kmsKeyARN).Status
		awsStore := &store.AWS{
			DB: dynamodb.NewFromConfig(awsCfg),
			// Path-style keeps every request on s3.<region>.amazonaws.com (one allowlist entry).
			S3:     s3.NewFromConfig(awsCfg, func(o *s3.Options) { o.UsePathStyle = true }),
			Table:  cfg.dynamoTable,
			Bucket: cfg.s3Bucket,
		}
		p.store = awsStore
		p.auth = msgauth.DKIM(msgauth.DoH{Client: egress, Endpoint: cfg.dohEndpoint}.LookupTXT)

		// One identity per CA: a production and a staging image never share
		// an ACME account.
		directory, err := url.Parse(cfg.acmeDirectory)
		if err != nil {
			return nil, err
		}
		identity = &tlsacme.Sealed{Records: awsStore, Keys: p.keys, Name: "tls-" + directory.Host}
	}
	if p.certs, err = tlsacme.New(hosts.All(), cfg.acmeDirectory, egress, identity, log); err != nil {
		return nil, err
	}
	go p.certs.Run(ctx)
	return p, nil
}
