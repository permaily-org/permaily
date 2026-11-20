package noticestest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/permaily-org/permaily/internal/services/notices"
	"github.com/permaily-org/permaily/internal/services/notices/noticestest"
)

// The fake keeps the contract the real queue keeps (ADR 0041, E20).
func TestE20TheFakeKeepsTheContract(t *testing.T) {
	noticestest.Contract(t, func(_ *testing.T, d notices.Deliverer) noticestest.Subject {
		return &noticestest.Fake{Deliverer: d}
	})
}

// E36: a queue that cannot be written answers its error and stores
// nothing; the caller fails nothing (the callers' tests say so).
func TestE36TheFakeFailsWhenToldTo(t *testing.T) {
	broken := errors.New("the storage cannot be written")
	f := &noticestest.Fake{Fail: broken}
	if err := f.Enqueue(context.Background(), noticestest.Sample("rcp_1")); !errors.Is(err, broken) {
		t.Fatalf("Enqueue = %v, want the storage's error", err)
	}
	if len(f.Queued()) != 0 {
		t.Fatal("a notice was queued by a queue that cannot be written")
	}
}
