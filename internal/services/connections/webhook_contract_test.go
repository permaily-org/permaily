package connections

import (
	"testing"

	"github.com/permaily-org/permaily/internal/services/webhooks/webhookstest"
)

// K31 (ADR 0039): the real service keeps webhookstest.Contract, with the
// real records, the default rule, the account's rules through the real
// accountrules service (a rule saved with a fresh proof for HoldAll) and a
// played model.
func TestK31TheServiceKeepsTheWebhooksContract(t *testing.T) {
	webhookstest.Contract(t, func(t *testing.T) webhookstest.World {
		w := newHookWorld(t)
		return &contractWorld{w: w, e: w.hookEnclave()}
	})
}
