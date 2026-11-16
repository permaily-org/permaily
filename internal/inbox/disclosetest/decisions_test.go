package disclosetest

import (
	"context"
	"errors"
	"testing"

	"github.com/permaily-org/permaily/internal/inbox"
)

func TestTheFakeKeepsTheContract(t *testing.T) { Contract(t, Shared) }

// The real strict store (ADR 0016, 0026) through two enclaves: what
// services/mailboxes' sealed store of blocks is, unsealed.
func TestStrictRecordsKeepTheContract(t *testing.T) { Contract(t, Enclaves) }

// A block past an item is a write that fails, as in the store.
func TestABlockPastAnItemIsRefused(t *testing.T) {
	r := NewRecords()
	big := &inbox.DecisionRecord{Entries: make([]inbox.DecisionEntry, ItemMax/16)}
	if err := r.WriteBlock(context.Background(), "rule-decisions-mbx_a-20261116-0", big, inbox.DecisionMark{}); !errors.Is(err, errTooLarge) {
		t.Fatal(err)
	}
	if err := (Anchored{}).WriteBlock(context.Background(), "rule-decisions-mbx_a-20261116-0", big, inbox.DecisionMark{}); !errors.Is(err, errTooLarge) {
		t.Fatal(err)
	}
}
