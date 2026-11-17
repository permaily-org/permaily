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
// longest a write to the store stays under way with the client the image
// gives the store: two operations (CreateNew's read of the plain name and
// its write), each of the SDK's tries cut at the client's timeout, with the
// SDK's longest wait between them (second review of #152, finding 2).
func TestV65TheLastPassOutlastsAStoreWrite(t *testing.T) {
	// DynamoDB's client tries once more than the SDK's default under the
	// SDK's 2026 retries (AWS_NEW_RETRIES_2026): 4.
	tries := time.Duration(max(retry.DefaultMaxAttempts, 4))
	operation := tries*enclave.EgressClient(0).Timeout + (tries-1)*retry.DefaultMaxBackoff
	if operation <= 0 || 2*operation >= connections.RemoveAgainAfter {
		t.Fatalf("a write lasts up to %v, the last pass comes %v after the removal", 2*operation, connections.RemoveAgainAfter)
	}
}
