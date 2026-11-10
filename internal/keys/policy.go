package keys

import (
	"context"
	"fmt"
	"slices"
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

// How long a reading is served before KMS is asked again, how long a failure
// is remembered, and how often Run reads on its own. /status is public, so the
// first two bound the calls to KMS.
const (
	policyTTL      = 30 * time.Second
	policyErrorTTL = 5 * time.Second
	policyInterval = time.Minute
	// maxHistory and maxGaps bound what /status carries.
	maxHistory = 32
	maxGaps    = 32
)

// PolicyReader reads the policy and grants of the pinned KMS key, so the
// enclave can publish them over its attested TLS key. KMS is reached with TLS
// from inside the enclave, so the host cannot alter the answer, and the key ARN
// (account and key id) is compiled into the image.
//
// It also remembers every different state it has read and the periods in which
// it could not read, so a policy that was changed and changed back while
// nobody was verifying still shows.
type PolicyReader struct {
	api    PolicyAPI
	keyARN string
	now    func() time.Time

	mu      sync.Mutex
	status  attestation.KMSStatus
	err     error
	readAt  time.Time
	hasRead bool

	started       time.Time // first attempt
	lastOK        time.Time // last successful reading
	failing       bool      // the last attempt failed
	history       []attestation.KMSObservation
	historyCut    bool
	gaps          []attestation.KMSGap
	gapsTruncated bool
}

// NewPolicyReader creates a reader for the key pinned in the image.
func NewPolicyReader(api PolicyAPI, keyARN string) *PolicyReader {
	return &PolicyReader{api: api, keyARN: keyARN, now: time.Now}
}

// Run reads the policy every policyInterval until ctx is done, so the history
// does not depend on anybody asking for /status.
func (p *PolicyReader) Run(ctx context.Context) {
	t := time.NewTicker(policyInterval)
	defer t.Stop()
	for {
		_, _ = p.Status(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Status returns the key policy and grant count, at most policyTTL old, with
// the history since this enclave started.
func (p *PolicyReader) Status(ctx context.Context) (attestation.KMSStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hasRead {
		ttl := policyTTL
		if p.err != nil {
			ttl = policyErrorTTL
		}
		if p.now().Sub(p.readAt) < ttl {
			return p.snapshot(), p.err
		}
	}
	st, err := p.read(ctx)
	p.observe(err == nil, st)
	p.status, p.err = st, err
	p.readAt, p.hasRead = p.now(), true
	return p.snapshot(), p.err
}

// snapshot returns the last reading with the history so far. The slices are
// copies, so a caller cannot change what the reader remembers.
func (p *PolicyReader) snapshot() attestation.KMSStatus {
	st := p.status
	if p.err != nil {
		return st
	}
	st.WatchedSince = p.started
	st.History = slices.Clone(p.history)
	st.HistoryTruncated = p.historyCut
	st.Gaps = slices.Clone(p.gaps)
	st.GapsTruncated = p.gapsTruncated
	return st
}

// observe records one attempt, successful or not, in the history.
func (p *PolicyReader) observe(ok bool, st attestation.KMSStatus) {
	at := p.now().UTC().Truncate(time.Second)
	if p.started.IsZero() {
		p.started = at
	}
	if !ok {
		p.failing = true
		return
	}
	if p.failing {
		from := p.lastOK
		if from.IsZero() {
			from = p.started
		}
		p.gaps = append(p.gaps, attestation.KMSGap{From: from, To: at})
		if len(p.gaps) > maxGaps {
			p.gaps, p.gapsTruncated = p.gaps[len(p.gaps)-maxGaps:], true
		}
		p.failing = false
	}
	p.lastOK = at
	if n := len(p.history); n > 0 && p.history[n-1].Policy == st.Policy && p.history[n-1].Grants == st.Grants && p.history[n-1].GrantsTruncated == st.GrantsTruncated {
		p.history[n-1].LastSeen = at
	} else {
		p.history = append(p.history, attestation.KMSObservation{Policy: st.Policy, Grants: st.Grants, GrantsTruncated: st.GrantsTruncated, FirstSeen: at, LastSeen: at})
		if len(p.history) > maxHistory {
			p.history, p.historyCut = p.history[len(p.history)-maxHistory:], true
		}
	}
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
