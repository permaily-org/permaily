package main

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/retry"

	"github.com/permaily-org/permaily/internal/enclave"
	"github.com/permaily-org/permaily/internal/services/connections"
)

// TestV65TheLastPassOutlastsAStoreWrite: the removal's last pass of an
// account's connections (connections.RemoveAgainAfter) comes after the
// longest a write of their values stays under way, which the store enforces
// from before the write's read of the record's commits
// (connections.WriteMax, store.WriteWithin), with 20 minutes past it for
// scheduling and clocks; and that bound holds a write of two operations with
// the client the image gives the store, each of the SDK's tries cut at the
// client's timeout, which must be one, with the SDK's longest wait between
// them (second review of #152, finding 2; third review, finding 1).
func TestV65TheLastPassOutlastsAStoreWrite(t *testing.T) {
	timeout := enclave.EgressClient(0).Timeout
	if timeout <= 0 {
		t.Fatalf("the store's client has no timeout: %v", timeout)
	}
	// DynamoDB's client tries once more than the SDK's default under the
	// SDK's 2026 retries (AWS_NEW_RETRIES_2026): 4.
	tries := time.Duration(max(retry.DefaultMaxAttempts, 4))
	operation := tries*timeout + (tries-1)*retry.DefaultMaxBackoff
	if 2*operation > connections.WriteMax {
		t.Fatalf("a write of two operations lasts up to %v, past WriteMax (%v)", 2*operation, connections.WriteMax)
	}
	if connections.RemoveAgainAfter-connections.WriteMax < 20*time.Minute {
		t.Fatalf("the last pass comes %v after the removal, writes last up to %v", connections.RemoveAgainAfter, connections.WriteMax)
	}
}
