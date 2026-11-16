package connections_test

import (
	"testing"

	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/connections/connectionstest"
)

// V59: the service's lease keeps the contract every Release keeps, with its
// real revocation (N2).
func TestV59TheServicesLeaseKeepsTheContract(t *testing.T) {
	connectionstest.ReleaseContract(t, connections.ReleaseMaker(t))
}

// V32: the service's Refuse keeps the contract every Refuse keeps (ruling 6
// on PR #136's second review): one receipt, under a fresh ReceiptWriteMax
// detached from the request, after the hard end too, its message only when
// it is the connection's.
func TestV32TheServicesRefuseKeepsTheContract(t *testing.T) {
	connectionstest.RefuseContract(t, func(t *testing.T) connectionstest.RefuseSubject {
		api, g, mbx, pass, written := connections.RefuseMaker(t)
		return connectionstest.RefuseSubject{API: api, Grant: g, Mailbox: mbx, PassHardEnd: pass, Written: func() []connectionstest.RefusedReceipt {
			var out []connectionstest.RefusedReceipt
			for _, w := range written() {
				out = append(out, connectionstest.RefusedReceipt{Reason: w.Reason, Mailbox: w.Mailbox, Message: w.Message, Bound: w.Bound})
			}
			return out
		}}
	})
}
