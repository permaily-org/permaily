// Package webhooks calls connections' webhooks (ADR 0039): it keeps the
// queue of notices to send, finds new mail a connection may read, keeps
// schedules in the person's time zone, signs every notice and sends it
// through the vendor egress, retrying until it is taken or given up.
//
// Nothing it keeps decides anything: whether a notice may be sent is read,
// before every call, from the connection's own strict record
// (Connections.Subscription), so whoever writes the table can drop or repeat
// a notice (as the host can) and never send one to another URL, put mail in
// one, or call a webhook of a connection that was revoked, ended, turned off
// or whose account is closing. Its entries are sealed with a key from the
// root secret and bound to where they are stored, and written at the version
// read, so two enclaves share the work: a connection's entry is held by one
// of them while it calls (webhook.LeaseFor).
//
// This file is the seam, committed with the design: what the service is
// given, what it gives, the names of its entries. ADR 0039's pull request 3
// writes the service; webhookstest has the stand-ins and the contract
// services/connections passes (pull request 2).
package webhooks

import (
	"context"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webhook"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// Config is what the service is given.
type Config struct {
	// Entries keeps the queue (Partition*), sealed under PurposeSeal and
	// written at the version read.
	Entries store.Entries
	// Keys derives the queue's key and the signing key
	// (webhook.KeyPurpose).
	Keys Keys
	// Connections reads what a connection approved and whether a notice may
	// be sent now; evaluates its new mail.
	Connections Connections
	// Sender calls a webhook (internal/webhook/send, over
	// enclave.WebhookEgressClient).
	Sender webhook.Sender
	// Now is the clock; Random fills b with random bytes.
	Now    func() time.Time
	Random func(b []byte) error
}

// Keys derives keys for one purpose from the root secret.
type Keys interface {
	Derive(ctx context.Context, purpose string) ([]byte, error)
}

// Subscription is a connection's webhook as its record says it now.
type Subscription struct {
	Connection string
	Account    string
	// ID is the subscription the notices name (webhook.SubscriptionPrefix).
	ID       string
	ClientID string
	URL      string
	NewMail  bool
	// Mailboxes are the connection's, for the index of new mail.
	Mailboxes []string
	Schedule  *contracts.WebhookSchedule
	TimeZone  string
	MadeAt    time.Time
	State     contracts.WebhookState
	// Calls says a notice may be sent now: the connection active and before
	// its end, at least one of its devices active, its account open or only
	// not agreed to new terms, the webhook on, and Delivery DeliveryOn.
	Calls    bool
	Delivery contracts.ConnectionDelivery
	// ReadAt is when the read Calls rests on began: a call starts only
	// within oauth.ConnectionFresh of it, and its notice is written by then
	// (webhook.Call.WriteBy) or not at all.
	ReadAt time.Time
}

// Watermark is how far a connection's new mail was evaluated: the newest
// received time evaluated, and the messages received from ArrivalLag before
// it that were (at most SeenMax), so a message stored late is evaluated once
// and none twice.
type Watermark struct {
	Received time.Time `json:"received"`
	Seen     []string  `json:"seen,omitempty"`
}

// SeenMax bounds Watermark.Seen; past it, the oldest are dropped, and a
// message stored more than ArrivalLag late may make a notice twice, which a
// vendor takes as one more fetch.
const SeenMax = 64

// Evaluation is what an evaluation of new mail found: whether a message it
// evaluated would leave (every rule first, the formulas, the model where it
// had to be asked), the earliest one's received time, the watermark after
// it, and whether more messages wait (webhook.ArrivalPage were read).
type Evaluation struct {
	Leaves  bool
	Since   time.Time
	Through Watermark
	More    bool
}

// Connections is what the service needs of connections;
// *connections.Service implements it (ADR 0039's pull request 2), and
// webhookstest.Fake stands for it, both passing webhookstest.Contract.
type Connections interface {
	// Subscription reads the connection's record strictly (a read at most
	// oauth.ConnectionFresh old in this enclave, the account's list and
	// standing the same way): ErrNoWebhook for a connection that has none
	// or was removed, an error for a record that cannot be read (never a
	// subscription that calls).
	Subscription(ctx context.Context, connection string) (*Subscription, error)
	// Pending evaluates the connection's mail received after after, as a
	// read of the connection would (inbox.Discloser with its formulas and
	// the rules first), asking the model as that read would, at most
	// webhook.ArrivalPage messages: contracts.ErrModelUnavailable when the
	// model cannot be asked for the account (nothing is evaluated, and the
	// watermark stays), ErrNoWebhook as Subscription. It writes no receipt:
	// nothing leaves.
	Pending(ctx context.Context, connection string, after Watermark) (*Evaluation, error)
	// Stop records that the vendor asked to stop (its webhook answered
	// 410): the webhook's state becomes contracts.WebhookStopped, at the
	// version read.
	Stop(ctx context.Context, connection string) error
}

// Queue is what services/connections tells the service. Subscribe is called
// when a connection with a webhook is made, after its record and before its
// code is issued, and fails the approval when it fails (a connection's
// webhook always has its queue entry); Ended is a hint when one ends, and
// the next touch of its entry, or the hourly repair, drops it either way.
type Queue interface {
	Subscribe(ctx context.Context, s Subscription) error
	Ended(connection string)
}

// Skip is why a notice due was not sent, one of a fixed set.
type Skip string

const (
	SkipEnded     Skip = "ended"             // revoked, ended, removed, its account closing or blocked
	SkipNoDevice  Skip = "no_device"         // every device ended: nobody could fetch
	SkipOff       Skip = "off"               // turned off by the person, or stopped by the vendor
	SkipModel     Skip = "model_unavailable" // nothing leaves now (the owner's decision 9 of ADR 0030)
	SkipGivenUp   Skip = "given_up"          // tried for webhook.GiveUpAfter
	SkipCoalesced Skip = "coalesced"         // told by a notice already waiting
)

// Status is the service's part of /status.
type Status interface {
	Status() *attestation.WebhooksStatus
}

// KeySet is the JWK set the api host publishes at webhook.KeysPath.
type KeySet interface {
	KeySet(ctx context.Context) ([]byte, error)
}

// Entries of the queue (ADR 0039, "The queue"). None decides anything.
const (
	// PartitionState + a shard (two hexadecimal digits of the hash of the
	// connection's identifier, Shards of them) holds one entry per
	// connection with a webhook, at its identifier: the notice waiting (its
	// identifier, made, since, calls, the next), the lease, the watermark
	// of new mail, the schedule's last run, the last taken and the failures.
	PartitionState = "webhooks#state#"
	Shards         = 16
	// PartitionDue + a minute (UTC, "200601021504") holds, at a connection's
	// identifier, that its entry is due in that minute: what the workers
	// list. It expires DueKept after its minute.
	PartitionDue = "webhooks#due#"
	DueKept      = 72 * time.Hour
	// PartitionMailbox + a mailbox's identifier holds, at a connection's
	// identifier, that the connection's webhook wants its new mail: what
	// inbox.Arrivals reads.
	PartitionMailbox = "webhooks#mailbox#"
	// PurposeSeal derives the key that seals the entries.
	PurposeSeal = "permaily webhook queue v1"
)

// errors of the service, of fixed kinds.
type kind struct{ msg string }

func (k *kind) Error() string   { return k.msg }
func (k *kind) LogKind() string { return k.msg }

// ErrNoWebhook is a connection that has no webhook, or no longer exists.
var ErrNoWebhook error = &kind{"webhooks: the connection has no webhook"}
