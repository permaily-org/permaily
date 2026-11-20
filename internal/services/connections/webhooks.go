package connections

import (
	"context"

	"github.com/permaily-org/permaily/internal/contracts"
)

// A connection's webhook (ADR 0039) is part of what the person approved, so
// it is kept in the connection's record, a strict anchored record: whether
// it has one, what for and its state readable and signed; its URL, its
// subscription and the person's time zone sealed. What it decides (whether a
// notice may be sent now) is read from that record and nowhere else; the
// queue of internal/services/webhooks decides nothing.
//
// ADR 0039's pull request 2 makes *Service implement WebhookAPI and
// webhooks.Connections; the routes (pull request 4) are built against
// connectionstest.Fake, which implements WebhookAPI now.

// WebhookAPI is what the person's routes call of a connection's webhook.
type WebhookAPI interface {
	// WebhookOff turns the connection's webhook off: written at the version
	// the page showed (contracts.ErrConsentChanged when it changed), with
	// no fresh proof (it only protects) and while new terms are not agreed
	// (like Revoke). Final: only a new request of the client turns it on
	// again. A connection with no webhook is contracts.ErrNotFound.
	WebhookOff(ctx context.Context, p Person, id string, version uint64) (*contracts.Connection, error)
}

// PurposeWebhookSeal derives the key that seals a connection's webhook's
// private part (its URL, subscription and time zone) inside the
// connection's record.
const PurposeWebhookSeal = "permaily connection webhooks v1"
