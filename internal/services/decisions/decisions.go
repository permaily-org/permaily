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
// enclaves reserve together is counted in a strict anchored record
// (Records), as grants that each carry an identity of their own: a grant is
// spent from only once a fresh read after its write found it in the
// record's committed history, with no fork, so that two enclaves never
// both spend the month's last block, and an earlier copy is refused. That
// count, not the provider's limit, is what keeps the month under
// Limits.Month (ADR 0028, "Spend").
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
	"github.com/permaily-org/permaily/internal/rulewords"
	"github.com/permaily-org/permaily/internal/store"
)

// Config is what the service is given.
type Config struct {
	// NewModel builds the provider's client, or is nil when the image asks
	// no model. New calls it once, before it returns, with the service's
	// own credential: the model's key as the service last read it from
	// RecordKey, read again once it is older than a minute, or ok false
	// when none is set, it was removed, its record is lost, forked or does
	// not open, or it could not be read (each logged by kind). New reads no
	// storage itself; the credential reads the record on its first use. The
	// credential is the only way the key leaves the service: it is not a
	// method of the service, so no route, operator interface or log line
	// can reach it (ADR 0028, "The key").
	NewModel func(key decide.Credential) decide.Model
	// NewWriter builds the Responses API's client for a person's words
	// (ADR 0040, "Plain words"; internal/rulewords/openai in the image), or
	// is nil when the image asks no model for words: Write then answers
	// contracts.ErrModelUnavailable. New calls it once, before it returns,
	// with the same credential as NewModel's; every call it makes is
	// reserved through the same guard (Write).
	NewWriter func(key decide.Credential) rulewords.Model
	// Kept reads and writes the answers kept for each message, sealed in
	// its mailbox (inbox.KeptStore).
	Kept inbox.KeptStore
	// Records keeps, in a store.Anchored with Strict set (ADR 0016, ADR
	// 0026), read and written at the version read (PutRecordAt fails with
	// store.ErrChanged when another enclave wrote since; the service reads
	// again, at most RecordTries times): the model's key (RecordKey), each
	// month's spend (RecordSpend), the canary's state for each baseline
	// (RecordCanary), and the alarms of every image and month
	// (RecordAlarms), each sealed with a key derived from the root secret.
	// Each is made only with CreateNew, and the composition reads and
	// writes these names through no other Anchored. A record that is lost
	// (store.ErrLost) or forked (store.ErrForked), or does not open, is not
	// read as zero: for the spend nothing new is asked
	// (attestation.ModelGuardUnreadable); for the key there is none; for the
	// canary its state is unreadable; for the alarms nothing is sent, and no
	// cause whose alarm cannot be added is recorded.
	Records Records
	// Entries keeps the canary's lease for each baseline (pk=model-canary,
	// sk=lease#<the state's name>): made at version 0 (store.ErrExists when
	// another enclave made it), taken over at the version read once it
	// expired (store.ErrConflict when another enclave took it first). The
	// lease only spares honest enclaves running the same check twice:
	// whoever writes the table can put back an expired lease and let a
	// second enclave run the day's check beside the first (CanaryLease).
	Entries store.Entries
	// Keys derives the keys that seal the model's key ("permaily model key
	// v1"), the month's spend ("permaily model spend v1"), the canary's
	// state and lease ("permaily model canary v1") and the alarms
	// ("permaily model alarms v1") from the root secret (keys.Root).
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
	// Block is the unit an enclave reserves from the month's record in, as
	// one grant, and spends from its memory: one anchored write a grant. An
	// attempt is taken from what is left of this enclave's grants of the
	// day; when that is not enough, the enclave makes one grant of the
	// fewest whole blocks that cover the attempt's estimate alone (the
	// largest request, 1,585,176 bytes of content with 24 statements, is
	// four), within what is left of the month, and the attempt is sent only
	// once that grant is confirmed. A grant is of one month and one day:
	// what is left of it when the month or the day changes, or when the
	// enclave stops, is counted as spent, and an attempt settled after that
	// gives nothing back to a later grant.
	Block int64
}

// DefaultLimits are the figures of ADR 0028.
var DefaultLimits = Limits{
	Month:      50_000_000_000, // 50 USD
	DayAlarm:   10_000_000_000, // 10 USD
	AccountDay: 500_000_000,    // 0.50 USD
	Block:      100_000_000,    // 0.10 USD
}

// Records is the strict anchored store, read and written at the version
// read. CreateNew makes a record that was never made (store.ErrExists when
// another enclave made it first: the service reads it).
//
// A PutRecordAt that returned nil is not yet a grant of the month's spend:
// whoever writes the anchors' bucket can hide a commit behind a delete
// marker while another enclave appends at the same version, and both
// appends succeed (ADR 0026, S1). So the service reads the record again,
// fresh, after its write, and spends from a grant only when that read
// succeeds and lists the grant's identity; the second of two appends at
// one version then reads store.ErrForked, and every read after both does.
// A grant whose write failed, or whose read after it failed or does not
// list it, is never spent from, even if a later read lists it: it is
// counted as reserved and burnt.
type Records interface {
	GetRecordAt(ctx context.Context, name string) (*store.Record, store.Mark, error)
	PutRecordAt(ctx context.Context, r *store.Record, at store.Mark) error
	CreateNew(ctx context.Context, r *store.Record) error
}

// The service's anchored records.
const (
	// RecordKey is the model's key.
	RecordKey = "model-key"
	// RecordSpend + "YYYY-MM" (UTC) is a month's spend: every grant of the
	// month (its identity, its blocks and its day) and what they reserved in
	// all and by day; observations only, never an alarm (RecordAlarms).
	// Every version carries every grant before it, so a grant once
	// confirmed is in every later version.
	RecordSpend = "model-spend-"
	// RecordCanary + the first 16 hexadecimal digits of the SHA-256 of the
	// compiled model, prompt version and canary.Baseline is the canary's
	// state for them: when the first enclave of that baseline started
	// (written by CreateNew before any check), every completed check, and
	// the last day a check observed a change; observations only, never an
	// alarm (RecordAlarms). Two images of different baselines, as during a
	// release, each keep their own record, and neither reads the other's. A
	// completion only adds to it: no write removes an observed change.
	RecordCanary = "model-canary-"
	// RecordAlarms is the only place an alarm is due and sent, for every
	// image, baseline and month: made with CreateNew by the first enclave of
	// any image, and read by every image's RunAlarms, so an alarm left due
	// when the last enclave of its baseline stopped, or when its month
	// ended, is still sent. An entry is keyed by its kind, its day (UTC)
	// and, for the canary's kinds, the baseline, which makes each kind due
	// at most once a day (per baseline for the canary's), and holds whether
	// it was sent: synthetic, no account, no amount, nothing of a message.
	// Entries are added at the version read, read again on store.ErrChanged
	// at most RecordTries times; adding one that is there changes nothing;
	// one sent stays sent in every later version, and sent ones older than
	// 30 days may be dropped, a due one never. The enclave that sees a cause
	// adds its alarm here first, and only then records the cause (the grant
	// in RecordSpend, the completion in RecordCanary, the guard's or the
	// canary's state): a crash between the two sends an alarm for a cause
	// observed again later, never records a cause without its alarm.
	RecordAlarms = "model-alarms"
)

// The bounds of the service's own writes and waits.
const (
	// RecordTries is how many times a record is read again after
	// store.ErrChanged before the write is given up: for the spend, the
	// attempt is then not sent.
	RecordTries = 5
	// CanaryLease is how long the enclave that takes the canary's lease
	// holds it; another one takes it over after, and runs the day's check
	// if it has still not completed. It is a hint between honest enclaves,
	// not a fence: a lease put back by whoever writes the table can make a
	// second enclave run the check beside the first. Each enclave runs one
	// check at a time and none once a check completed that day, so a
	// replay costs at most one more canary run per enclave and lease, each
	// reserved through the guard like any attempt (its estimate is about
	// 0.0072 USD), and its completion only adds a check (RecordCanary).
	CanaryLease = 10 * time.Minute
	// AlarmTimeout bounds one alarm's sending.
	AlarmTimeout = 10 * time.Second
	// AlarmRetry is how often RunAlarms sends the alarms that RecordAlarms
	// holds due and not marked sent: on its own ticker, so an alarm whose
	// sending failed is sent again whether or not anything is reserved
	// again, after the day or the month changed, and by another image than
	// the one that saw its cause.
	AlarmRetry = time.Minute
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
// alone and marks nothing. An alarm is first due: the enclave that sees
// its cause adds it to RecordAlarms before it records the cause, and while
// it cannot, records no cause (the grant is not made, the canary's run does
// not complete) and keeps the alarm in memory; then RunAlarms, every
// AlarmRetry, raises every alarm RecordAlarms holds due and not sent, with
// its own kind and day, and marks it sent only after Raise returned nil. A
// crash between sending and marking sends it twice, which is accepted.
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
