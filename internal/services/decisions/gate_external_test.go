package decisions_test

import (
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/decisions"
)

// The service is the connections' model gate (ADR 0030's pull request 4).
var _ connections.ModelGate = (*decisions.Service)(nil)
