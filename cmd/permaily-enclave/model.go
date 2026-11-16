package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/decide/openai"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/services/decisions"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// modelConfig is the model's section of deploy/enclave.json ("model"),
// compiled into the image as entry point flags (ADR 0028): the
// organisation and project the requests name, what the image says of the
// provider's retention, and the spend guard's limits.
type modelConfig struct {
	// organization and project are sent as the OpenAI-Organization and
	// OpenAI-Project headers, so a key of another organisation or project
	// is refused by OpenAI rather than used. Empty is "not pinned": the
	// header is not sent (openai.New), /status leaves the field out, and
	// permaily-verify warns.
	organization, project string
	// zeroDataRetention is what the notice in force says: false until
	// OpenAI approves zero data retention and a release changes the notice.
	zeroDataRetention bool
	limits            decisions.Limits
}

// modelFlags registers the model's flags on fs, with the limits of ADR
// 0028 as their defaults.
func modelFlags(fs *flag.FlagSet, m *modelConfig) {
	m.limits = decisions.DefaultLimits
	fs.StringVar(&m.organization, "model-organization", "", "OpenAI organisation the model's requests name (OpenAI-Organization); empty pins none")
	fs.StringVar(&m.project, "model-project", "", "OpenAI project the model's requests name (OpenAI-Project); empty pins none")
	fs.BoolVar(&m.zeroDataRetention, "model-zero-data-retention", false, "whether OpenAI approved zero data retention, as the model's notice in force says")
	fs.Func("model-month-limit-usd", "what the enclaves may reserve for the model in a calendar month, in US dollars (default 50)", usdFlag(&m.limits.Month))
	fs.Func("model-day-alarm-usd", "the day's reserved spend that sends the alarm, in US dollars (default 10)", usdFlag(&m.limits.DayAlarm))
	fs.Func("model-account-day-usd", "what one account's asking may cost in a day in one enclave, in US dollars (default 0.5)", usdFlag(&m.limits.AccountDay))
}

// usdFlag reads an amount in US dollars into billionths of one.
func usdFlag(dst *int64) func(string) error {
	return func(s string) error {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(v) || v <= 0 || v > 1e6 {
			return errors.New("not an amount of US dollars")
		}
		*dst = int64(math.Round(v * 1e9))
		return nil
	}
}

// errZeroDataRetention refuses an image that says zero data retention is
// approved: the model's notice in force (domain.ModelNoticeVersion) says
// OpenAI may keep what it is sent, and a person agrees to those words. The
// release that says it is approved changes the notice, its version and this
// check together (ADR 0028, "Consent").
var errZeroDataRetention = errors.New("-model-zero-data-retention: the model's notice in force says zero data retention is not approved")

// modelParts is what the model is composed from.
type modelParts struct {
	cfg modelConfig
	// egress is the client the provider's client copies (enclave.EgressClient
	// in the image); nil with newModel set.
	egress *http.Client
	// newModel, when set, builds the model in place of openai.New over
	// egress: dev mode's fake, which never reaches OpenAI.
	newModel func(decide.Credential) decide.Model
	// records is the one strict Anchored of the model's records, with
	// OnlyNewest; no other Anchored reads or writes their names.
	records *store.Anchored
	// entries holds the canary's lease and the kept decisions.
	entries store.Entries
	keys    decisions.Keys
	// mailer sends the alarm to superAdmin (sesmail.Sender in the image).
	mailer     decisions.Mailer
	superAdmin string
	log        *slog.Logger
	now        func() time.Time
}

// composeModel is the model's service (ADR 0028, pull request 5): the
// provider's client built by NewModel with the service's own credential,
// the model's records in one strict Anchored, the kept decisions in the
// mailboxes' partitions, and the alarm by mail to the super admin.
func composeModel(m modelParts) (*decisions.Service, error) {
	if m.cfg.zeroDataRetention {
		return nil, errZeroDataRetention
	}
	if m.records == nil || !m.records.Strict || !m.records.OnlyNewest {
		return nil, errors.New("the model's records need a strict Anchored that keeps only the newest value")
	}
	if m.cfg.limits == (decisions.Limits{}) {
		// A configuration made without the flags, as a test makes one,
		// holds the limits of ADR 0028, never none.
		m.cfg.limits = decisions.DefaultLimits
	}
	newModel := m.newModel
	if newModel == nil {
		egress, org, project := m.egress, m.cfg.organization, m.cfg.project
		newModel = func(key decide.Credential) decide.Model { return openai.New(egress, key, org, project) }
	}
	return decisions.New(decisions.Config{
		NewModel: newModel,
		Kept:     inbox.NewKeptStore(m.entries),
		Records:  m.records,
		Entries:  m.entries,
		Keys:     m.keys,
		Alarm:    &decisions.MailAlarm{Mailer: m.mailer, To: m.superAdmin},
		Limits:   m.cfg.limits,
		Log:      m.log,
		Now:      m.now,
	})
}

// modelStatus is the model's section of /status: the service's, with what
// the image compiles in (the provider, its address, the pinned
// organisation and project, and the retention the notice states).
func modelStatus(svc *decisions.Service, cfg modelConfig) func(context.Context) *attestation.ModelStatus {
	return func(ctx context.Context) *attestation.ModelStatus {
		st := svc.Status(ctx)
		st.Provider, st.Endpoint = "openai", openai.Endpoint
		st.Organization, st.Project = cfg.organization, cfg.project
		st.ZeroDataRetention = cfg.zeroDataRetention
		return st
	}
}

// canaryEvery is how often an enclave asks whether the day's canary still
// has to run: as often as a lease lasts, so a check another enclave took
// and did not complete is taken over once its lease ends.
const canaryEvery = decisions.CanaryLease

// runModel runs the model's canary and its alarms on their tickers, each
// once when it starts, until ctx ends. Their errors are logged by kind
// alone: the service already logged what it can say.
func runModel(ctx context.Context, svc *decisions.Service, log *slog.Logger) {
	go every(ctx, canaryEvery, func() {
		if err := svc.RunCanary(ctx); err != nil && ctx.Err() == nil {
			log.Warn("model: the canary did not run", "kind", "canary")
		}
	})
	go every(ctx, decisions.AlarmRetry, func() {
		if err := svc.RunAlarms(ctx); err != nil && ctx.Err() == nil {
			log.Warn("model: the alarms were not all sent", "kind", "alarms")
		}
	})
}

// every runs f now and then every d, until ctx ends.
func every(ctx context.Context, d time.Duration, f func()) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		f()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
