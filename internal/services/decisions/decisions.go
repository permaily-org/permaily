// Package decisions asks the model what depends on meaning for one message
// at a time, and is everything around the call (ADR 0028): the answers
// kept for each message, the spend guard and its counters, the model's key
// sealed in the storage, the daily canary, and the alarms by mail. It
// implements inbox.Asker; the model is a decide.Model, which the
// composition gives it (internal/decide/openai in the image).
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
	// Records keeps the model's key, sealed, as the record "model-key",
	// read and written at the version read (store.Anchored, as the
	// providers' applications are kept).
	Records Records
	// Entries keeps the month's spend counter (pk=model-spend) and the
	// canary's day (pk=model-canary), each written at the version read.
	Entries store.Entries
	// Keys derives the key that seals the model's key and the one that
	// signs the counters (keys.Root).
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
	// of them together: the owner's 50 USD. The provider's project limit,
	// set by the operator at the same figure, is the stop outside the
	// enclave.
	Month int64
	// DayAlarm is the day's reserved spend (UTC) that sends the alarm: the
	// owner's 10 USD. Crossing it asks nothing less.
	DayAlarm int64
	// AccountDay is what one account's asking may cost in a day (UTC) in
	// one enclave, so no account spends what the others need.
	AccountDay int64
	// Block is what one enclave reserves from the month's counter at a
	// time, and spends from its memory.
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

// Keys derives keys for one purpose from the root secret.
type Keys interface {
	Derive(ctx context.Context, purpose string) ([]byte, error)
}

// Alarm sends the operator one message by mail, of fixed words for its
// kind and the day: never an amount tied to an account, a topic or
// anything of a message. The composition sends it through SES to the super
// admin's address compiled into the image; it never holds the service up,
// and its failure is logged by kind alone.
type Alarm interface {
	Raise(ctx context.Context, kind AlarmKind, day string)
}

// AlarmKind is what an alarm says.
type AlarmKind string

const (
	AlarmDaySpend      AlarmKind = "day_spend"      // the day's reserved spend passed Limits.DayAlarm
	AlarmMonthLimit    AlarmKind = "month_limit"    // the month's limit was reached: nothing new is asked
	AlarmProviderLimit AlarmKind = "provider_limit" // the provider said its own limit was reached
	AlarmKeyRefused    AlarmKind = "key_refused"    // the provider refused the key
	AlarmCanaryChanged AlarmKind = "canary_changed" // an answer of the canary moved past its tolerance
	AlarmCanaryFailed  AlarmKind = "canary_failed"  // the canary could not run two days in a row
)
