package enclave

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/mdlayher/vsock"
)

// HostCredentials fetches the parent instance role's AWS credentials from
// permaily-host over vsock. They are the host's own credentials, so nothing is
// revealed to it; what protects mail is that KMS only releases data keys to an
// attested enclave, and the enclave pins the KMS key ARN.
type HostCredentials struct {
	Port uint32
	// Dial overrides the vsock dial (tests).
	Dial func(ctx context.Context) (net.Conn, error)
}

// HostCredentialsResponse is the JSON permaily-host returns (IMDS format).
type HostCredentialsResponse struct {
	AccessKeyID     string    `json:"AccessKeyId"`
	SecretAccessKey string    `json:"SecretAccessKey"`
	Token           string    `json:"Token"`
	Expiration      time.Time `json:"Expiration"`
}

// Retrieve implements aws.CredentialsProvider.
func (h HostCredentials) Retrieve(ctx context.Context) (aws.Credentials, error) {
	dial := h.Dial
	if dial == nil {
		dial = func(ctx context.Context) (net.Conn, error) { return vsock.DialContext(ctx, HostCID, h.Port, nil) }
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://host.vsock/credentials", nil)
	if err != nil {
		return aws.Credentials{}, err
	}
	res, err := client.Do(req)
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("enclave: host credentials: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return aws.Credentials{}, fmt.Errorf("enclave: host credentials: HTTP %d", res.StatusCode)
	}
	var c HostCredentialsResponse
	if err := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 64<<10)).Decode(&c); err != nil {
		return aws.Credentials{}, fmt.Errorf("enclave: host credentials: %w", err)
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return aws.Credentials{}, fmt.Errorf("enclave: host credentials: incomplete")
	}
	return aws.Credentials{
		AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey, SessionToken: c.Token,
		Source: "permaily-host", CanExpire: !c.Expiration.IsZero(), Expires: c.Expiration,
	}, nil
}
