package keys

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/permaily-org/permaily/pkg/attestation"
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

func TestPolicyReaderHistory(t *testing.T) {
	const closed, open = `{"Statement":["closed"]}`, `{"Statement":["open"]}`
	now := time.Date(2026, 11, 10, 4, 0, 0, 700, time.UTC)
	api := &fakePolicyAPI{policy: closed}
	r := NewPolicyReader(api, "arn")
	r.now = func() time.Time { return now }
	read := func(after time.Duration) attestation.KMSStatus {
		t.Helper()
		now = now.Add(after)
		st, err := r.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	at := func(min int) time.Time { return time.Date(2026, 11, 10, 4, min, 0, 0, time.UTC) }

	st := read(0)
	if !st.WatchedSince.Equal(at(0)) || len(st.History) != 1 || st.History[0].Policy != closed || len(st.Gaps) != 0 {
		t.Fatalf("first reading: %+v", st)
	}
	st = read(time.Minute)
	if len(st.History) != 1 || !st.History[0].FirstSeen.Equal(at(0)) || !st.History[0].LastSeen.Equal(at(1)) {
		t.Fatalf("an unchanged policy must stay one entry: %+v", st.History)
	}

	// The operator opens the policy for a minute and closes it again. Whoever
	// verifies afterwards still sees it.
	api.policy = open
	read(time.Minute)
	api.policy = closed
	st = read(time.Minute)
	if len(st.History) != 3 || st.History[1].Policy != open || !st.History[1].FirstSeen.Equal(at(2)) || !st.History[1].LastSeen.Equal(at(2)) || st.History[2].Policy != closed || st.Policy != closed {
		t.Fatalf("history after opening and closing: %+v", st.History)
	}
	// A grant is a change too.
	api.grants = 1
	if st = read(time.Minute); len(st.History) != 4 || st.History[3].Grants != 1 {
		t.Fatalf("history after a grant: %+v", st.History)
	}
	api.grants, api.truncated = 0, true
	if st = read(time.Minute); len(st.History) != 5 || !st.History[4].GrantsTruncated {
		t.Fatalf("history after truncated grants: %+v", st.History)
	}

	// The caller cannot alter what the reader remembers.
	st.History[0].Policy = "tampered"
	if again, _ := r.Status(context.Background()); again.History[0].Policy != closed {
		t.Fatal("the returned history shares memory with the reader")
	}
}

func TestPolicyReaderGaps(t *testing.T) {
	now := time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)
	api := &fakePolicyAPI{policy: `{}`, policyErr: errors.New("unreachable")}
	r := NewPolicyReader(api, "arn")
	r.now = func() time.Time { return now }
	at := func(min int) time.Time { return time.Date(2026, 11, 10, 4, min, 0, 0, time.UTC) }
	step := func() (attestation.KMSStatus, error) {
		now = now.Add(time.Minute)
		return r.Status(context.Background())
	}

	// KMS unreachable from the start: once it answers, the time before is a gap.
	if _, err := r.Status(context.Background()); err == nil {
		t.Fatal("no error")
	}
	_, _ = step()
	api.policyErr = nil
	st, err := step()
	if err != nil || !st.WatchedSince.Equal(at(0)) || len(st.Gaps) != 1 || !st.Gaps[0].From.Equal(at(0)) || !st.Gaps[0].To.Equal(at(2)) {
		t.Fatalf("gap at start: %+v, %v", st.Gaps, err)
	}

	// Later the host cuts the enclave off from KMS: the gap runs from the last
	// good reading to the next one, which is when a change could go unseen.
	st, _ = step() // 4:03, fine
	api.grantsErr = errors.New("unreachable")
	_, _ = step()
	_, _ = step()
	api.grantsErr = nil
	st, _ = step() // 4:06
	if len(st.Gaps) != 2 || !st.Gaps[1].From.Equal(at(3)) || !st.Gaps[1].To.Equal(at(6)) || len(st.History) != 1 {
		t.Fatalf("gaps: %+v, history %d", st.Gaps, len(st.History))
	}
}

func TestPolicyReaderBoundsItsMemory(t *testing.T) {
	now := time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)
	api := &fakePolicyAPI{}
	r := NewPolicyReader(api, "arn")
	r.now = func() time.Time { return now }
	var st attestation.KMSStatus
	for i := range maxHistory + 5 {
		api.policy = fmt.Sprintf(`{"n":%d}`, i)
		now = now.Add(time.Minute)
		st, _ = r.Status(context.Background())
	}
	if len(st.History) != maxHistory || !st.HistoryTruncated || st.History[maxHistory-1].Policy != api.policy {
		t.Fatalf("history: %d entries, truncated %v", len(st.History), st.HistoryTruncated)
	}
	for range maxGaps + 5 {
		api.policyErr = errors.New("unreachable")
		now = now.Add(time.Minute)
		_, _ = r.Status(context.Background())
		api.policyErr = nil
		now = now.Add(time.Minute)
		st, _ = r.Status(context.Background())
	}
	if len(st.Gaps) != maxGaps || !st.GapsTruncated {
		t.Fatalf("gaps: %d entries, truncated %v", len(st.Gaps), st.GapsTruncated)
	}
}

func TestPolicyReaderRun(t *testing.T) {
	api := &fakePolicyAPI{policy: `{}`}
	r := NewPolicyReader(api, "arn")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		read := r.hasRead
		r.mu.Unlock()
		if read {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run did not read the policy on its own")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}
