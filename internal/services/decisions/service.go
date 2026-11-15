package decisions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"maps"
	"math/big"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// Service asks the model for one message at a time (ADR 0028). It
// implements inbox.Asker. Its memory holds only caches (the key, read again
// after keyFresh; the canary's state) and limits (this enclave's grants and
// the accounts' shares): what two enclaves must agree on is in the records.
type Service struct {
	cfg    Config
	model  decide.Model // nil when the image asks no model
	canary *decide.Canary
	since  time.Time
	// id names this service in the canary's lease.
	id string
	// slots are the attempts open at once (decide.Concurrent).
	slots chan struct{}
	// sleep waits between attempts; tests replace it.
	sleep func(ctx context.Context, d time.Duration) error
	// jitter is a random duration in [0, d); tests replace it.
	jitter func(d time.Duration) time.Duration

	key   keyCache
	guard guard

	alarmMu sync.Mutex
	// alarmsAdded are the alarms this enclave knows to be in RecordAlarms,
	// by key; alarmsPending the ones it could not add yet, which RunAlarms
	// adds first.
	alarmsAdded   map[string]string
	alarmsPending map[string]alarmEntry

	canaryMu sync.Mutex
	canaryRun
	countMu sync.Mutex
	calls   map[string]int
}

// New makes the service. It reads no storage, and calls cfg.NewModel once,
// with the service's credential, before it returns: no method of the
// service returns the key.
func New(cfg Config) (*Service, error) {
	if cfg.Records == nil || cfg.Entries == nil || cfg.Keys == nil || cfg.Kept == nil {
		return nil, errors.New("decisions: records, entries, keys and kept decisions are needed")
	}
	if cfg.Limits.Month <= 0 || cfg.Limits.Block <= 0 || cfg.Limits.DayAlarm <= 0 || cfg.Limits.AccountDay <= 0 {
		return nil, errors.New("decisions: every limit is needed")
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Service{
		cfg:           cfg,
		id:            randomID(),
		slots:         make(chan struct{}, decide.Concurrent),
		sleep:         sleepCtx,
		jitter:        randomJitter,
		alarmsAdded:   map[string]string{},
		alarmsPending: map[string]alarmEntry{},
		calls:         map[string]int{},
	}
	s.since = s.now()
	s.guard.init()
	if c, err := decide.LoadCanary(); err == nil {
		s.canary = c
	} else {
		s.cfg.Log.Error("decisions: the canary does not load; it does not run")
	}
	if cfg.NewModel != nil {
		s.model = cfg.NewModel(s.credential)
	}
	return s, nil
}

func (s *Service) now() time.Time { return s.cfg.Now().UTC() }

// dayOf and monthOf are a moment's day and month (UTC), as records name
// them.
func dayOf(t time.Time) string   { return t.UTC().Format(time.DateOnly) }
func monthOf(t time.Time) string { return t.UTC().Format("2006-01") }

// randomID is 128 random bits in hexadecimal.
func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func randomJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(d)))
	if err != nil {
		return d / 2
	}
	return time.Duration(n.Int64())
}

// count adds n calls of a kind to what /status says.
func (s *Service) count(kind string, n int) {
	if n <= 0 {
		return
	}
	s.countMu.Lock()
	s.calls[kind] += n
	s.countMu.Unlock()
}

// logKind logs one line about the model: a fixed message and its kind,
// never a topic, an atom, a subject, a text, a probability, a content hash,
// an identifier, the key or the words of an error (ADR 0028, "Logging").
func (s *Service) logKind(level slog.Level, msg, kind string) {
	s.cfg.Log.Log(context.Background(), level, msg, "kind", kind)
}

// usd turns billionths of a US dollar into dollars, for /status.
func usd(nano int64) float64 { return float64(nano) / 1e9 }

// Status is the model's section of /status: this enclave's state and what
// it holds compiled in. The provider, the endpoint, the organisation, the
// project and the retention the notice states are the composition's, which
// fills them from the image (ADR 0028, pull request 5). Never the key or
// its fingerprint, an amount spent, an account, a topic or anything of a
// message.
func (s *Service) Status(ctx context.Context) *attestation.ModelStatus {
	st := &attestation.ModelStatus{
		PromptVersion: decide.PromptVersion,
		PromptCache:   "off",
		MonthLimitUSD: usd(s.cfg.Limits.Month),
		DayAlarmUSD:   usd(s.cfg.Limits.DayAlarm),
		AccountDayUSD: usd(s.cfg.Limits.AccountDay),
		Guard:         s.guardState(ctx),
		Since:         s.since,
		Calls:         map[string]int{},
	}
	if s.model != nil {
		st.Model = s.model.Name()
	}
	if k := s.keyState(ctx); k.set {
		st.KeySet, st.KeySetAt = true, k.setAt
	}
	for _, kind := range attestation.ModelCalls {
		st.Calls[kind] = 0
	}
	s.countMu.Lock()
	maps.Copy(st.Calls, s.calls)
	s.countMu.Unlock()
	st.Canary = s.canaryStatus(ctx)
	return st
}
