// Package notices is the queue of the notices by mail about connections and
// the record of what left (ADR 0041): a connection approved, renewed or
// given a device, one about to end, a device ended because a token of it
// was used twice, receipts found deleted or changed in the storage, and
// every device signed out by the operator or an admin. ADR 0017's
// principle stands: a person is told of security and access, and of
// nothing else, and none of these can be turned off.
//
// A notice is queued once and sent at least once. Enqueue writes it, under
// a key named by the event, as an entry created at version 0, so two
// enclaves, a retry or the hourly sweep that sees the same event again
// queue it once; the first attempt starts at once in the enclave that
// queued it, and a runner in every enclave tries again what is still
// queued, under a lease, for RetryFor. A notice sent is marked under the
// same key before its entry goes, so it is not queued again while the mark
// lasts (KeptFor). Only a crash after the mail system took the message and
// before its mark is written sends it twice.
//
// An entry holds the account's identifier, the kind, the key and the facts
// its words need, never the contact address, which the Deliverer reads
// from the account when it sends. It is signed (envelope.Sign under
// PurposeSign) bound to its partition and key: whoever writes the table
// can delete one (one notice fewer) or a mark (one more, for a kind the
// sweep sees again), and can make, move or edit none.
//
// This file is the seam, committed with the design: what a notice is, the
// two interfaces, the keys, the bounds and when an ending is told. ADR
// 0041's pull request 1 writes the queue and its runner; noticestest is
// the fake the callers are built against and the contract both keep. It
// knows nothing of HTTP or of SES.
package notices

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Kind is what a notice tells of.
type Kind string

const (
	// KindConnectionApproved: the person approved a request that made a
	// connection, or replaced the client's connection (Facts.Replaced).
	// Its key is the consent receipt's identifier.
	KindConnectionApproved Kind = "connection_approved"
	// KindDeviceAdded: the person approved a request that added a device
	// to an existing connection. Its key is the consent receipt's
	// identifier.
	KindDeviceAdded Kind = "device_added"
	// KindConnectionRenewed: the person renewed a connection. Its key is
	// the consent receipt's identifier.
	KindConnectionRenewed Kind = "connection_renewed"
	// KindConnectionEnding: a connection ends within EndingBefore
	// (EndingDue). Its key is the connection's identifier and its end, so
	// a renewal, which moves the end, is told again before the new one.
	KindConnectionEnding Kind = "connection_ending"
	// KindDeviceEndedReuse: a device of a connection was ended because a
	// refresh token or a code of it was used again after its grace
	// (contracts.EndedByReuse). Its key is the connection's and the
	// device's identifiers.
	KindDeviceEndedReuse Kind = "device_ended_reuse"
	// KindReceiptsIncident: a deletion or a change of the account's
	// receipts or links was recorded in the record of incidents (ADR
	// 0037). Its key is the day (UTC), so an account is told once a day
	// at most, whatever was found that day.
	KindReceiptsIncident Kind = "receipts_incident"
	// KindDevicesSignedOut: the operator or an admin signed every device
	// out of the account (Facts.By). Its key is the account's count of
	// sign-outs after it.
	KindDevicesSignedOut Kind = "devices_signed_out"
)

// Kinds are every kind, in the order the ADR lists them. ADR 0039
// (deliveries) and ADR 0040 (rules) add theirs here, with their words.
func Kinds() []Kind {
	return []Kind{
		KindConnectionApproved, KindDeviceAdded, KindConnectionRenewed, KindConnectionEnding,
		KindDeviceEndedReuse, KindReceiptsIncident, KindDevicesSignedOut,
	}
}

// Who signed every device out, for KindDevicesSignedOut.
const (
	ByOperator = "operator"
	ByAdmin    = "admin"
)

// Notice is one notice to queue.
type Notice struct {
	// Account is the account whose person is told ("acc_…").
	Account string `json:"account"`
	Kind    Kind   `json:"kind"`
	// Key names the event, made by Key: two notices of one Key are one.
	Key string `json:"key"`
	// At is when the event happened, the notice's "When".
	At    time.Time `json:"at"`
	Facts Facts     `json:"facts"`
}

// Facts are what a notice's words may say besides when. Everything here
// is shown only as fixed words: the vendor only by the image's verified
// list (vendors.Verified), the parts and the resource by their names,
// times as times. Nothing a vendor or a person typed is here: no client
// name, no query, sentence or formula, no mailbox, label or address, and
// no identifier is ever shown.
type Facts struct {
	// Vendor is the connection's vendor domain (contracts.Vendor.Domain).
	// The words name it only when the image's verified list does, as
	// "Claude, by Anthropic (claude.ai)"; any other is "an app Permaily
	// hasn't verified".
	Vendor   string             `json:"vendor,omitempty"`
	Resource contracts.Resource `json:"resource,omitempty"`
	Parts    []contracts.Part   `json:"parts,omitempty"`
	// EndsAt is the connection's end: for an approval, a renewal and an
	// ending.
	EndsAt time.Time `json:"ends_at,omitzero"`
	// Replaced says an approval replaced the client's connection
	// (contracts.ChangeReplace).
	Replaced bool `json:"replaced,omitempty"`
	// By is ByOperator or ByAdmin, for KindDevicesSignedOut.
	By string `json:"by,omitempty"`
}

// ErrInvalid is a notice Check refuses: nothing is queued.
var ErrInvalid = errors.New("notices: not a notice of a known kind with its facts")

// ErrSkip is what a Deliverer answers for an account that cannot be told
// now and never will be of this event: gone, closing or blocked. The
// notice is marked skipped and never tried again.
var ErrSkip = errors.New("notices: the account cannot be told")

// Queue is what the callers are given: the connections service at an
// approval, a renewal, a device ended for reuse and in its sweep, the
// receipts' chain when it records an incident, and accounts when the
// operator or an admin signs every device out.
type Queue interface {
	// Enqueue stores n once by its Key and returns once it is stored, or
	// when EnqueueMax has passed, whichever is first; nil when it was
	// queued or sent before. It refuses what Check refuses (ErrInvalid).
	// Its first attempt is not waited for. An error fails nothing of what
	// caused it: the caller logs its kind and goes on.
	Enqueue(ctx context.Context, n Notice) error
}

// Deliverer writes a notice's message from fixed words and sends it to
// the account's contact address as it is when it sends (accounts
// implements it, with the words of every kind, through the Mailer the
// watch of ADR 0021 guards). ErrSkip: the account cannot be told; any
// other error: tried again, until RetryFor.
type Deliverer interface {
	Deliver(ctx context.Context, n Notice) error
}

// Where the queue keeps its entries (store.Entries), and the key that
// signs them.
const (
	// PartitionQueue holds a notice not yet sent, under its Key, created
	// at version 0 and changed at the version read: its attempts and the
	// lease of the enclave trying it.
	PartitionQueue = "notices#queue"
	// PartitionSent holds the mark of a notice sent, dropped or skipped,
	// under its Key, created at version 0, kept KeptFor.
	PartitionSent = "notices#sent"
	PurposeSign   = "permaily notices v1"
)

// Bounds of the queue and its runner.
const (
	// EnqueueMax bounds how long Enqueue holds its caller.
	EnqueueMax = 2 * time.Second
	// RunEvery is how often each enclave's runner looks at the queue.
	RunEvery = time.Minute
	// LeaseFor is how long an attempt holds a notice from the other
	// enclave: longer than any send (sesmail gives one 15 seconds).
	LeaseFor = 2 * time.Minute
	// RetryFirst and RetryMax bound the wait between two attempts, which
	// doubles from the first.
	RetryFirst = time.Minute
	RetryMax   = time.Hour
	// RetryFor is how long a notice is tried before it is dropped, marked
	// and counted.
	RetryFor = 24 * time.Hour
	// KeptFor is how long a mark is kept, so the same event is not queued
	// again.
	KeptFor = 30 * 24 * time.Hour
	// SendsPerSecond and InFlightMax bound what one enclave sends of the
	// queue: SES takes 14 a second for the whole account, sign-in messages
	// included.
	SendsPerSecond = 2
	InFlightMax    = 8
)

// When a connection's end is told (KindConnectionEnding).
const (
	// EndingBefore is how long before its end a connection is told.
	EndingBefore = 7 * 24 * time.Hour
	// EndingMinDays: a connection approved or renewed for this many days or
	// fewer is not told of its end, which its approval's notice gave.
	EndingMinDays = 14
)

// EndingDue says whether a connection approved or last renewed for days,
// ending at endsAt, is to be told of its end at now: it lasts more than
// EndingMinDays, it has not ended, and its end is EndingBefore away or
// less.
func EndingDue(days int, endsAt, now time.Time) bool {
	return days > EndingMinDays && now.Before(endsAt) && !now.Before(endsAt.Add(-EndingBefore))
}

// Key names an event of an account: the SHA-256 of the account, the kind
// and the event's parts (ADR 0041, "Which notices"), each after a zero
// byte, as 32 hexadecimal digits. It carries nothing readable of them.
func Key(account string, kind Kind, parts ...string) string {
	h := sha256.New()
	h.Write([]byte("permaily notice v1"))
	for _, p := range append([]string{account, string(kind)}, parts...) {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// EndingKey is the key of the ending of a connection at endsAt.
func EndingKey(account, connection string, endsAt time.Time) string {
	return Key(account, KindConnectionEnding, connection, endsAt.UTC().Format(time.RFC3339Nano))
}

// IncidentKey is the key of what was found of an account's receipts on
// the day (UTC) of at.
func IncidentKey(account string, at time.Time) string {
	return Key(account, KindReceiptsIncident, at.UTC().Format(time.DateOnly))
}

// Check refuses a notice that is not one: an account that is not "acc_"
// and letters and digits, a kind not in Kinds, a key not made by Key, no
// time, or facts its kind does not have. A notice about a connection names
// its vendor; one that tells of a connection's end (an approval, a device
// added, a renewal, an ending) has it; only an approval may say it
// replaced, and only a sign-out who did it.
func (n Notice) Check() error {
	if !accountID(n.Account) || !slices.Contains(Kinds(), n.Kind) || !keyForm(n.Key) || n.At.IsZero() {
		return ErrInvalid
	}
	f := n.Facts
	connection := n.Kind != KindReceiptsIncident && n.Kind != KindDevicesSignedOut
	ends := connection && n.Kind != KindDeviceEndedReuse
	switch {
	case connection != (f.Vendor != ""),
		!connection && (f.Resource != "" || len(f.Parts) > 0),
		ends == f.EndsAt.IsZero(),
		f.Replaced && n.Kind != KindConnectionApproved,
		(n.Kind == KindDevicesSignedOut) != (f.By == ByOperator || f.By == ByAdmin):
		return ErrInvalid
	}
	return nil
}

func accountID(s string) bool {
	rest, ok := strings.CutPrefix(s, "acc_")
	if !ok || rest == "" || len(rest) > 64 {
		return false
	}
	for _, c := range rest {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func keyForm(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
