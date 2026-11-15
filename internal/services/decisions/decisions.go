// Package decisions asks the model what depends on meaning for one message
// at a time, and is everything around the call (ADR 0028): the answers
// kept for each message, the spend guard and its counters, the attempts and
// their retries, the model's key sealed in the storage, the daily canary,
// and the alarms by mail. It implements inbox.Asker; the model is a
// decide.Model, which the composition gives it (internal/decide/openai in
// the image), and which makes one attempt a call.
//
// Every attempt is reserved for before it is sent, the canary's and the
// probes' too: its estimate (decide.Estimate) is taken from this enclave's
// block of the month and from the account's share, and settled once, when
// the attempt ends, at the cost its answer reported, or at the estimate
// when it reported none or failed after it may have been sent
// (decide.AttemptError.Sent). A retry (decide.Retries, after an
// AttemptError with Retry) is another attempt, reserved again. What the
// enclaves reserve together is counted in an anchored record (Records), so
// an earlier copy of it is refused: that count, not the provider's limit,
// is what keeps the month under Limits.Month (ADR 0028, "Spend").
//
// This file is the seam, committed with the design: what the service is
// given and the limits it holds, so the pull requests that build the
// provider's client, the service, stored mail and the routes can be written
// at the same time. Nothing here is implemented yet; ADR 0028's pull
// request 2 writes the service.
package decisions

import (
	"context"
	"log/slog"
	"time"

	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/store"
)

// Config is what the service is given.
type Config struct {
	// Model is the provider's client, or nil when the image asks no model.
	Model decide.Model
	// Kept reads and writes the answers kept for each message, sealed in
	// its mailbox (inbox.KeptStore).
	Kept inbox.KeptStore
	// Records keeps, in store.Anchored, read and written at the version read
	// (PutRecordAt fails with store.ErrChanged when another enclave wrote
	// since; the service reads again, at most RecordTries times): the
	// model's key (RecordKey), each month's spend (RecordSpend), and the
	// canary's state (RecordCanary), each sealed with a key derived from
	// the root secret. A record that is lost (store.ErrLost) or forked
	// (store.ErrForked), or does not open, is not read as zero: for the
	// spend nothing new is asked (attestation.ModelGuardUnreadable); for the
	// key there is none; for the canary its state is unreadable.
	Records Records
	// Entries keeps the canary's lease (pk=model-canary, sk=lease): made at
	// version 0 (store.ErrExists when another enclave made it), taken over
	// at the version read once it expired (store.ErrConflict when another
	// enclave took it first).
	Entries store.Entries
	// Keys derives the keys that seal the model's key ("permaily model key
	// v1"), the month's spend ("permaily model spend v1") and the canary's
	// state and lease ("permaily model canary v1") from the root secret
	// (keys.Root).
	Keys Keys
	// Alarm tells the operator by mail (ADR 0028, "The alarm").
	Alarm Alarm
	// Limits are compiled into the image (deploy/enclave.json, "model").
	Limits Limits
	Log    *slog.Logger
	Now    func() time.Time
}

// Limits are the spend guard's, in billionths of a US dollar (ADR 0028,
// "Spend"). Changing one is a release.
type Limits struct {
	// Month is what the enclaves may reserve in a calendar month (UTC), all
	// of them together: the owner's 50 USD, held by the anchored record of
	// the month's spend. The provider's project limit, set by the operator
	// at the same figure with its hard enforcement on, is a defence in depth
	// the enclave can neither see nor prove.
	Month int64
	// DayAlarm is the day's reserved spend (UTC) that sends the alarm: the
	// owner's 10 USD. Crossing it asks nothing less.
	DayAlarm int64
	// AccountDay is what one account's asking may cost in a day (UTC) in
	// one enclave, so no account spends what the others need.
	AccountDay int64
	// Block is what one enclave reserves from the month's record at a
	// time, and spends from its memory: one anchored write a block. A block
	// is of one month and one day: what is left of it when the month or the
	// day changes, or when the enclave stops, is counted as spent.
	Block int64
}

// DefaultLimits are the figures of ADR 0028.
var DefaultLimits = Limits{
	Month:      50_000_000_000, // 50 USD
	DayAlarm:   10_000_000_000, // 10 USD
	AccountDay: 500_000_000,    // 0.50 USD
	Block:      100_000_000,    // 0.10 USD
}

// Records is the anchored store, read and written at the version read.
type Records interface {
	GetRecordAt(ctx context.Context, name string) (*store.Record, store.Mark, error)
	PutRecordAt(ctx context.Context, r *store.Record, at store.Mark) error
}

// The service's anchored records.
const (
	// RecordKey is the model's key.
	RecordKey = "model-key"
	// RecordSpend + "YYYY-MM" (UTC) is a month's spend: what the enclaves
	// reserved, in all and by day, and the days whose spend alarm was sent.
	RecordSpend = "model-spend-"
	// RecordCanary is the canary's state: the compiled model, prompt
	// version and canary.Baseline it is for, the last completed check, what
	// it found, and the last day a check observed a change.
	RecordCanary = "model-canary"
)

// The bounds of the service's own writes and waits.
const (
	// RecordTries is how many times a record is read again after
	// store.ErrChanged before the write is given up: for the spend, the
	// attempt is then not sent.
	RecordTries = 5
	// CanaryLease is how long the enclave that takes the canary's lease
	// holds it; another one takes it over after, and runs the day's check
	// if it has still not completed.
	CanaryLease = 10 * time.Minute
	// AlarmTimeout bounds one alarm's sending.
	AlarmTimeout = 10 * time.Second
)

// Keys derives keys for one purpose from the root secret.
type Keys interface {
	Derive(ctx context.Context, purpose string) ([]byte, error)
}

// Alarm sends the operator one message by mail, of fixed words for its
// kind and the day: never an amount tied to an account, a topic or
// anything of a message. The composition sends it through SES to the super
// admin's address compiled into the image. Raise returns nil only when the
// mail was handed to SES, within AlarmTimeout; an error is the mail
// system's or the copy watch's refusal, and the service logs it by kind
// alone, marks nothing, and raises it again with the next reservation or
// check. The service marks an alarm sent (in the month's spend record, or
// in the canary's state) only after Raise returned nil: a crash between the
// two sends it twice, which is accepted.
type Alarm interface {
	Raise(ctx context.Context, kind AlarmKind, day string) error
}

// AlarmKind is what an alarm says.
type AlarmKind string

const (
	AlarmDaySpend      AlarmKind = "day_spend"      // the day's reserved spend passed Limits.DayAlarm
	AlarmMonthLimit    AlarmKind = "month_limit"    // the month's limit was reached: nothing new is asked
	AlarmProviderLimit AlarmKind = "provider_limit" // the provider said its own limit was reached
	AlarmKeyRefused    AlarmKind = "key_refused"    // the provider refused the key
	AlarmCanaryChanged AlarmKind = "canary_changed" // an answer of the canary moved (decide.CanaryAtom.Moved)
	AlarmCanaryFailed  AlarmKind = "canary_failed"  // no check completed for two days (UTC)
)
