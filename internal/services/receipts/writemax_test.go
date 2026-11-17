package receipts_test

import (
	"testing"

	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/receipts"
)

// A link is written within the time a receipt's write has: the chain's
// bound is the connections service's, which receipts cannot import.
func TestALinkIsWrittenWithinAReceiptsTime(t *testing.T) {
	if receipts.ReceiptWriteMax != connections.ReceiptWriteMax {
		t.Fatalf("%v, not %v", receipts.ReceiptWriteMax, connections.ReceiptWriteMax)
	}
}
