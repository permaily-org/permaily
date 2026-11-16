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
