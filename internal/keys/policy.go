package keys

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/permaily-org/permaily/pkg/attestation"
)

// PolicyAPI is the subset of the KMS client used to read who can use the key.
type PolicyAPI interface {
	GetKeyPolicy(ctx context.Context, in *kms.GetKeyPolicyInput, opts ...func(*kms.Options)) (*kms.GetKeyPolicyOutput, error)
	ListGrants(ctx context.Context, in *kms.ListGrantsInput, opts ...func(*kms.Options)) (*kms.ListGrantsOutput, error)
}

// How long a reading is served before KMS is asked again, and how long a
// failure is remembered. /status is public, so this bounds the calls to KMS.
const (
	policyTTL      = 30 * time.Second
	policyErrorTTL = 5 * time.Second
)

// PolicyReader reads the policy and grants of the pinned KMS key, so the
// enclave can publish them over its attested TLS key. KMS is reached with TLS
// from inside the enclave, so the host cannot alter the answer, and the key ARN
// (account and key id) is compiled into the image.
type PolicyReader struct {
	api    PolicyAPI
	keyARN string
	now    func() time.Time

	mu      sync.Mutex
	status  attestation.KMSStatus
	err     error
	readAt  time.Time
	hasRead bool
}

// NewPolicyReader creates a reader for the key pinned in the image.
func NewPolicyReader(api PolicyAPI, keyARN string) *PolicyReader {
	return &PolicyReader{api: api, keyARN: keyARN, now: time.Now}
}

// Status returns the key policy and grant count, at most policyTTL old.
func (p *PolicyReader) Status(ctx context.Context) (attestation.KMSStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hasRead {
		ttl := policyTTL
		if p.err != nil {
			ttl = policyErrorTTL
		}
		if p.now().Sub(p.readAt) < ttl {
			return p.status, p.err
		}
	}
	p.status, p.err = p.read(ctx)
	p.readAt, p.hasRead = p.now(), true
	return p.status, p.err
}

func (p *PolicyReader) read(ctx context.Context) (attestation.KMSStatus, error) {
	pol, err := p.api.GetKeyPolicy(ctx, &kms.GetKeyPolicyInput{KeyId: aws.String(p.keyARN), PolicyName: aws.String("default")})
	if err != nil {
		return attestation.KMSStatus{}, fmt.Errorf("keys: get key policy: %w", err)
	}
	grants, err := p.api.ListGrants(ctx, &kms.ListGrantsInput{KeyId: aws.String(p.keyARN), Limit: aws.Int32(100)})
	if err != nil {
		return attestation.KMSStatus{}, fmt.Errorf("keys: list grants: %w", err)
	}
	return attestation.KMSStatus{
		KeyARN:          p.keyARN,
		Policy:          aws.ToString(pol.Policy),
		Grants:          len(grants.Grants),
		GrantsTruncated: grants.Truncated,
		FetchedAt:       p.now().UTC().Truncate(time.Second),
	}, nil
}
