// Package webhooks calls connections' webhooks (ADR 0039): it keeps the
// queue of notices to send, finds new mail a connection may read, keeps
// schedules in the person's time zone, signs every notice and sends it
// through the vendor egress, retrying until it is taken or given up.
//
// Nothing it keeps decides anything: whether a notice may be sent is read,
// before every call, from the connection's own strict record and the
// account's rules (Connections.Admit, Connections.Recheck), and the call is
// written under a permit connections cut on every stop, so whoever writes
// the table can drop or repeat a notice (as the host can) and never send
// one to another URL, put mail in one, tell a match a rule now holds, or
// call a webhook of a connection that was revoked, ended, turned off or
// whose account is closing. Its entries are sealed with a key from the root
// secret and bound to where they are stored, and written at the version
// read, so two enclaves share the work: a connection's entry is held by one
// of them while it calls (webhook.LeaseFor).
//
// This file is the seam, committed with the design and revised after its
// review: what the service is given, what it gives, the names of its
// entries. ADR 0039's pull request 3 writes the service; webhookstest has
// the stand-ins and the contract services/connections passes (pull request
// 2).
package webhooks

import (
	"context"
	"slices"
	"strings"
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
	// be sent now; evaluates its new mail; lists every webhook.
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

// Basis is what a read of a connection rested on: the version of the
// connection's record, and the account's rules record as read (ADR 0040,
// accountrules.Applying's Epoch and Version; zero for an account with none).
// A match found under one basis is never told under another (design review,
// finding 1): Recheck answers false, and the queue evaluates again under
// the current one.
type Basis struct {
	Connection   uint64 `json:"connection"`
	RulesEpoch   uint64 `json:"rules_epoch"`
	RulesVersion uint64 `json:"rules_version"`
}

// Subscription is a connection's webhook as its record says it now.
type Subscription struct {
	Connection string
	Account    string
	// ID is the subscription the notices name (webhook.SubscriptionPrefix),
	// and the endpoint every notice's "v2a" signature is bound to.
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
	// Basis is what the read rested on.
	Basis Basis
	// Calls says a notice may be sent now: the connection active and before
	// its end, at least one of its devices active, its account open or only
	// not agreed to new terms, the webhook on, Delivery DeliveryOn, and the
	// account's rules readable. Why says why not, one of the Skip reasons
	// (empty when Calls).
	Calls    bool
	Why      Skip
	Delivery contracts.ConnectionDelivery
	// ReadAt is when the read Calls rests on began: a call starts only
	// within oauth.ConnectionFresh of it, and its notice is written by then
	// (Permit.WriteBy) or not at all.
	ReadAt time.Time
}

// Permit is the right to write one call's request (design review, finding
// 4): registered by connections under the connection before Admit returns,
// as a read's lease is (connections.Release), and cut by every stop made
// through this enclave (the connection or its last device revoked, the
// webhook turned off, a replacement, the account's deletion asked, the
// vendor's 410 recorded) before that stop is acknowledged. Another
// enclave's stops reach it through WriteBy: oauth.ConnectionFresh after the
// read, and never after the connection's end. A change of the account's
// rules or of the model's gate does not cut a call already admitted (an
// answer being prepared finishes under the rules it began with, ADR 0040);
// every later attempt is admitted and rechecked under the new ones.
type Permit interface {
	// Start answers the context the call is dialled, handshaken and
	// written under: it ends, with cause webhook.ErrCut, when the permit is
	// cut, and at WriteBy. ErrCut when it was cut already or WriteBy has
	// passed: nothing is written.
	Start(ctx context.Context) (context.Context, error)
	// WriteBy is the instant the request is written by or not at all
	// (webhook.Call.WriteBy).
	WriteBy() time.Time
	// Finish ends it; called once, after Start or instead of it.
	Finish()
}

// Cursor is a position in a connection's new mail, in the order an
// evaluation reads it: by received time, then by mailbox, then by message,
// so a burst of messages received in the same instant, in several
// mailboxes, has a stable order and the cursor always moves forward
// (design review, finding 3). The zero Cursor is before every message.
type Cursor struct {
	Received time.Time `json:"received"`
	Mailbox  string    `json:"mailbox,omitempty"`
	Message  string    `json:"message,omitempty"`
}

// Compare orders two positions: -1 when c is before d, 0 when they are the
// same, +1 when c is after.
func (c Cursor) Compare(d Cursor) int {
	if n := c.Received.Compare(d.Received); n != 0 {
		return n
	}
	if n := strings.Compare(c.Mailbox, d.Mailbox); n != 0 {
		return n
	}
	return strings.Compare(c.Message, d.Message)
}

// Progress is how far a connection's new mail was evaluated. Cursor is the
// forward continuation: every message at or before it was evaluated, and an
// evaluation reads only after it, so it reaches the last message whatever
// the burst. Recent are the messages evaluated whose received time is
// within webhook.ArrivalLag before Cursor.Received, for the late sweep only
// (a message stored after a later one, whose position the cursor already
// passed): never the cursor. The late sweep reads, within that window, the
// messages received at or after LateFrom that are not in Recent. Recent
// holds at most RecentMax (Window): when more were evaluated in the window,
// the oldest are dropped and LateFrom moves past them, so a dropped one is
// never evaluated again and a message stored late among them is not told
// (the vendor finds it at its next fetch): a truncated set is never read
// as "not evaluated", and never holds the cursor back.
type Progress struct {
	Cursor   Cursor    `json:"cursor"`
	Recent   []Cursor  `json:"recent,omitempty"`
	LateFrom time.Time `json:"late_from,omitzero"`
}

// RecentMax bounds Progress.Recent: three times what one mailbox's
// delivery limits (600 messages an hour) let arrive within ArrivalLag, so
// only a burst across several mailboxes drops any.
const RecentMax = 64

// Window is the late sweep's part of a progress whose cursor is cursor,
// after an evaluation added to recent what it read: the messages within
// webhook.ArrivalLag before the cursor, in Cursor order, at most RecentMax,
// the oldest dropped and lateFrom moved just past the newest dropped. Every
// implementation of Connections.Pending ends with it, so they keep the same
// window.
func Window(cursor Cursor, recent []Cursor, lateFrom time.Time) ([]Cursor, time.Time) {
	from := cursor.Received.Add(-webhook.ArrivalLag)
	var kept []Cursor
	for _, r := range recent {
		if !r.Received.Before(from) && !r.Received.Before(lateFrom) && r.Compare(cursor) <= 0 && !slices.Contains(kept, r) {
			kept = append(kept, r)
		}
	}
	slices.SortFunc(kept, Cursor.Compare)
	if n := len(kept) - RecentMax; n > 0 {
		if past := kept[n-1].Received.Add(time.Nanosecond); past.After(lateFrom) {
			lateFrom = past
		}
		kept = slices.DeleteFunc(kept[n:], func(r Cursor) bool { return r.Received.Before(lateFrom) })
	}
	return kept, lateFrom
}

// Match is what an evaluation found that would leave: the basis it was
// evaluated under, the progress it began at (a match discarded is
// evaluated again from there, under the current basis), and the earliest
// messages that would leave (at most webhook.MatchMessagesMax, the first
// being the earliest, whose minute is the notice's Since).
type Match struct {
	Basis    Basis    `json:"basis"`
	From     Progress `json:"from"`
	Messages []Cursor `json:"messages"`
}

// Evaluation is what an evaluation of new mail found: a Match when a
// message it evaluated would leave (every rule first, the formulas, the
// model where it had to be asked), the progress after it, and whether more
// messages wait (webhook.ArrivalPage were read).
type Evaluation struct {
	Match   *Match
	Through Progress
	More    bool
}

// Connections is what the service needs of connections;
// *connections.Service implements it (ADR 0039's pull request 2), and
// webhookstest.Fake stands for it, both passing webhookstest.Contract.
type Connections interface {
	// Subscription reads the connection's record strictly (a read at most
	// oauth.ConnectionFresh old in this enclave, the account's list,
	// standing and rules the same way): ErrNoWebhook for a connection that
	// has none or was removed, an error for a record that cannot be read
	// (never a subscription that calls).
	Subscription(ctx context.Context, connection string) (*Subscription, error)
	// Admit is Subscription before a call: when it Calls, with a Permit
	// registered before it returns; with none (nil) when it does not.
	Admit(ctx context.Context, connection string) (*Subscription, Permit, error)
	// Pending evaluates the connection's mail after after.Cursor, in Cursor
	// order, and the late sweep within webhook.ArrivalLag before it (the
	// messages at or after after.LateFrom not in after.Recent), as a read of the connection would (the discloser
	// with the connection's formulas and the account's rules, ADR 0040's
	// accountrules.Source, first), asking the model as that read would, at
	// most webhook.ArrivalPage messages in all, the late ones first, its
	// Through ending with Window:
	// contracts.ErrModelUnavailable when the model cannot be asked for the
	// account and ErrRulesUnavailable when the rules cannot be read (nothing
	// is evaluated, and the progress stays), ErrNoWebhook as Subscription.
	// It writes no receipt: nothing leaves.
	Pending(ctx context.Context, connection string, after Progress) (*Evaluation, error)
	// Recheck says, before every attempt of a notice of new mail (the first
	// and each retry, after a restart too), whether its match still stands:
	// the basis read now, strictly, is m.Basis, and at least one of
	// m.Messages is still in one of the connection's mailboxes and within
	// its retention. false for any change, and with an error when something
	// could not be read: the queue discards the match either way and
	// evaluates again from m.From (design review, finding 1).
	Recheck(ctx context.Context, connection string, m Match) (bool, error)
	// Webhooks lists, a page at a time, every connection whose strict
	// record holds a webhook (in any state but removed), found from the
	// connections' strict records themselves and never from the queue or
	// an index the queue writes, in a stable order after after (""
	// first), at most limit; fewer than limit is the last page. The hourly
	// repair rebuilds the queue from it (design review, finding 5).
	Webhooks(ctx context.Context, after string, limit int) ([]string, error)
	// Stop records that the vendor asked to stop (its webhook answered
	// 410): the webhook's state becomes contracts.WebhookStopped, at the
	// version read, its permits cut.
	Stop(ctx context.Context, connection string) error
}

// Queue is what services/connections tells the service. Subscribe is called
// when a connection with a webhook is made, after its record and before its
// code is issued, and fails the approval when it fails (a connection's
// webhook always has its queue entry; the repair makes it again from
// Connections.Webhooks if it is lost); Ended is a hint when one ends, and
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
	SkipRules     Skip = "rules_unavailable" // the account's rules cannot be read: nothing leaves (ADR 0040)
	SkipChanged   Skip = "changed"           // a match whose basis changed or whose messages are gone: evaluated again
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
	// connection with a webhook, at its identifier: the notice in flight
	// (frozen once its first attempt starts: its identifier, body, calls,
	// the next), the generation waiting (a match, a run), the lease, the
	// progress of new mail, the schedule's last run and coverage boundary,
	// the last taken and the failures.
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
	// RepairPage is how many connections one page of Connections.Webhooks
	// the repair reads.
	RepairPage = 100
)

// errors of the service, of fixed kinds.
type kind struct{ msg string }

func (k *kind) Error() string   { return k.msg }
func (k *kind) LogKind() string { return k.msg }

// ErrNoWebhook is a connection that has no webhook, or no longer exists.
var ErrNoWebhook error = &kind{"webhooks: the connection has no webhook"}

// ErrRulesUnavailable is the account's rules that cannot be read now (ADR
// 0040, accountrules.ErrUnavailable): nothing is evaluated or told.
var ErrRulesUnavailable error = &kind{"webhooks: the account's rules cannot be read"}
