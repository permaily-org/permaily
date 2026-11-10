package keys

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

type fakePolicyAPI struct {
	policy    string
	grants    int
	truncated bool
	policyErr error
	grantsErr error
	calls     int
	keyIDs    []string
}

func (f *fakePolicyAPI) GetKeyPolicy(_ context.Context, in *kms.GetKeyPolicyInput, _ ...func(*kms.Options)) (*kms.GetKeyPolicyOutput, error) {
	f.calls++
	f.keyIDs = append(f.keyIDs, aws.ToString(in.KeyId)+"|"+aws.ToString(in.PolicyName))
	if f.policyErr != nil {
		return nil, f.policyErr
	}
	return &kms.GetKeyPolicyOutput{Policy: aws.String(f.policy)}, nil
}

func (f *fakePolicyAPI) ListGrants(_ context.Context, in *kms.ListGrantsInput, _ ...func(*kms.Options)) (*kms.ListGrantsOutput, error) {
	f.keyIDs = append(f.keyIDs, aws.ToString(in.KeyId))
	if f.grantsErr != nil {
		return nil, f.grantsErr
	}
	return &kms.ListGrantsOutput{Grants: make([]types.GrantListEntry, f.grants), Truncated: f.truncated}, nil
}

func TestPolicyReader(t *testing.T) {
	const arn = "arn:aws:kms:us-east-2:111122223333:key/k"
	now := time.Date(2026, 11, 10, 4, 0, 0, 500, time.UTC)
	api := &fakePolicyAPI{policy: `{"Statement":[]}`, grants: 3, truncated: true}
	r := NewPolicyReader(api, arn)
	r.now = func() time.Time { return now }

	st, err := r.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.KeyARN != arn || st.Policy != api.policy || st.Grants != 3 || !st.GrantsTruncated || !st.FetchedAt.Equal(now.Truncate(time.Second)) {
		t.Fatalf("status %+v", st)
	}
	// Only the pinned key is ever asked about.
	if len(api.keyIDs) != 2 || api.keyIDs[0] != arn+"|default" || api.keyIDs[1] != arn {
		t.Fatalf("KMS asked about %v", api.keyIDs)
	}

	// A reading is reused for policyTTL, so public requests cannot flood KMS...
	api.policy = `{"Statement":[{"Effect":"Deny"}]}`
	now = now.Add(policyTTL - time.Second)
	if st, _ = r.Status(context.Background()); st.Policy == api.policy || api.calls != 1 {
		t.Fatalf("policy read again after %s (calls %d)", policyTTL-time.Second, api.calls)
	}
	// ...and a change shows up once it expires.
	now = now.Add(2 * time.Second)
	if st, _ = r.Status(context.Background()); st.Policy != api.policy || api.calls != 2 {
		t.Fatalf("stale policy served after %s (calls %d)", policyTTL, api.calls)
	}
}

func TestPolicyReaderErrors(t *testing.T) {
	now := time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)
	api := &fakePolicyAPI{policy: `{}`, policyErr: errors.New("denied")}
	r := NewPolicyReader(api, "arn")
	r.now = func() time.Time { return now }

	if _, err := r.Status(context.Background()); err == nil {
		t.Fatal("no error when the policy cannot be read")
	}
	// A failure is remembered briefly, then retried; an old success is never served in its place.
	if _, err := r.Status(context.Background()); err == nil || api.calls != 1 {
		t.Fatalf("err %v, calls %d", err, api.calls)
	}
	api.policyErr, api.grantsErr = nil, errors.New("denied")
	now = now.Add(policyErrorTTL)
	if _, err := r.Status(context.Background()); err == nil || api.calls != 2 {
		t.Fatalf("grants error not reported: err %v, calls %d", err, api.calls)
	}
	api.grantsErr = nil
	now = now.Add(policyErrorTTL)
	if st, err := r.Status(context.Background()); err != nil || st.Policy != `{}` {
		t.Fatalf("no recovery: %v %+v", err, st)
	}
	api.policyErr = errors.New("denied")
	now = now.Add(policyTTL)
	if st, err := r.Status(context.Background()); err == nil || st.Policy != "" {
		t.Fatalf("served the old reading after a failure: %v %+v", err, st)
	}
}
