package webhooks

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webhook"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// Service is the queue and the scheduler of connections' webhooks (ADR
// 0039's pull request 3). It implements Queue, inbox.Arrivals, Status and
// KeySet; Run is its workers and its hourly repair.
type Service struct {
	cfg      Config
	log      *slog.Logger
	verified map[string]bool
	keysURL  string
	// name is this enclave's, in the leases it writes.
	name  string
	hints chan hint
	wake  chan struct{}
	work  sync.WaitGroup

	keyMu sync.Mutex
	seal  []byte
	sign  ed25519.PrivateKey

	mu       sync.Mutex
	sched    *scheduler
	scan     scanCursor
	evals    evaluations
	since    time.Time
	outcomes map[webhook.Outcome]int64
	skipped  map[Skip]int64
}

var (
	_ Queue          = (*Service)(nil)
	_ inbox.Arrivals = (*Service)(nil)
	_ Status         = (*Service)(nil)
	_ KeySet         = (*Service)(nil)
)

// Option sets what Config does not say.
type Option func(*Service)

// WithLog logs through log, naming a vendor's host only when it is among
// verified (the verified vendors' domains); every other is "other". Nothing
// else of a webhook is ever logged: kinds and outcomes only.
func WithLog(log *slog.Logger, verified ...string) Option {
	return func(s *Service) {
		s.log = log
		for _, v := range verified {
			s.verified[v] = true
		}
	}
}

// WithKeysURL is where the api host publishes the key set, for /status.
func WithKeysURL(url string) Option { return func(s *Service) { s.keysURL = url } }

// hintsMax bounds the hints waiting: past it a hint is lost, and the
// repair finds what it was about.
const hintsMax = 1024

// The outcomes and the reasons /status counts, every one from the start.
var (
	allOutcomes = []webhook.Outcome{
		webhook.OutcomeSent, webhook.OutcomeGone, webhook.OutcomeRedirect, webhook.OutcomeStatus,
		webhook.OutcomeNoAnswer, webhook.OutcomeRefused, webhook.OutcomeTooLate, webhook.OutcomeCut,
	}
	allSkips = []Skip{SkipEnded, SkipNoDevice, SkipOff, SkipModel, SkipRules, SkipChanged, SkipGivenUp, SkipCoalesced}
)

// ErrConfig is a Config missing what the service needs.
var ErrConfig error = &kind{"webhooks: the service's configuration is incomplete"}

// New is the service over cfg.
func New(cfg Config, opts ...Option) (*Service, error) {
	if cfg.Entries == nil || cfg.Keys == nil || cfg.Connections == nil || cfg.Sender == nil {
		return nil, ErrConfig
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = func(b []byte) error { _, err := io.ReadFull(rand.Reader, b); return err }
	}
	s := &Service{
		cfg: cfg, log: slog.New(slog.DiscardHandler), verified: map[string]bool{},
		hints: make(chan hint, hintsMax), wake: make(chan struct{}, 1),
		sched: newScheduler(), since: cfg.Now().UTC(),
		outcomes: map[webhook.Outcome]int64{}, skipped: map[Skip]int64{},
	}
	for _, o := range opts {
		o(s)
	}
	name := make([]byte, 16)
	if err := cfg.Random(name); err != nil {
		return nil, err
	}
	s.name = hex.EncodeToString(name)
	return s, nil
}

func (s *Service) now() time.Time { return s.cfg.Now().UTC() }

// keys derives the queue's sealing key and the signing key once.
func (s *Service) keys(ctx context.Context) ([]byte, ed25519.PrivateKey, error) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	if s.sign == nil {
		seal, err := s.cfg.Keys.Derive(ctx, PurposeSeal)
		if err != nil {
			return nil, nil, err
		}
		seed, err := s.cfg.Keys.Derive(ctx, webhook.KeyPurpose)
		if err != nil {
			return nil, nil, err
		}
		sign, err := webhook.KeyFromSeed(seed)
		if err != nil {
			return nil, nil, err
		}
		s.seal, s.sign = seal, sign
	}
	return s.seal, s.sign, nil
}

// fail logs a failure by the step it stopped and its kind, never its words
// (which may name an entry).
func (s *Service) fail(step string, err error) {
	k := "error"
	var kinded interface{ LogKind() string }
	if errors.As(err, &kinded) {
		k = kinded.LogKind()
	}
	s.log.Warn("webhooks", "step", step, "error", k)
}

// vendor is how a host is logged: by name when verified, else "other".
func (s *Service) vendor(host string) string {
	if s.verified[host] {
		return host
	}
	return "other"
}

func (s *Service) count(o webhook.Outcome) {
	s.mu.Lock()
	s.outcomes[o]++
	s.mu.Unlock()
}

func (s *Service) skip(why Skip) {
	s.mu.Lock()
	s.skipped[why]++
	s.mu.Unlock()
}

// Status implements Status: the bounds compiled in, this enclave's keys,
// and its calls and skipped notices since it started, every key also at
// zero.
func (s *Service) Status() *attestation.WebhooksStatus {
	st := &attestation.WebhooksStatus{
		Signature: attestation.WebhookSignatureV2a, SpecRevision: webhook.SpecRevision, Keys: []attestation.WebhookKey{}, KeysURL: s.keysURL,
		NoticeHoldsMail: false, SameHost: true, Redirects: "none", Transport: "h2", NewMail: attestation.WebhookNewMailAfterRules,
		RevocationSeconds:  int(oauth.ConnectionFresh / time.Second),
		MinIntervalSeconds: int(webhook.MinInterval / time.Second), TimeoutSeconds: int(webhook.AttemptTimeout / time.Second),
		GiveUpSeconds: int(webhook.GiveUpAfter / time.Second), DedupSeconds: int(webhook.DedupKeepFor / time.Second),
		HostInFlight: webhook.HostInFlightMax, HostAttemptsPerMinute: webhook.HostAttemptsPerMinute,
		Schedules: []string{string(contracts.EveryDay), string(contracts.EveryWeek)},
		Since:     s.since, Outcomes: map[string]int64{}, Skipped: map[string]int64{},
	}
	if _, sign, err := s.keys(context.Background()); err == nil {
		st.Keys = append(st.Keys, attestation.WebhookKey{ID: webhook.KeyID, X: base64.RawURLEncoding.EncodeToString(sign.Public().(ed25519.PublicKey))})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range allOutcomes {
		st.Outcomes[string(o)] = s.outcomes[o]
	}
	for _, k := range allSkips {
		st.Skipped[string(k)] = s.skipped[k]
	}
	return st
}

// KeySet implements KeySet.
func (s *Service) KeySet(ctx context.Context) ([]byte, error) {
	_, sign, err := s.keys(ctx)
	if err != nil {
		return nil, err
	}
	return webhook.KeySet(map[string]ed25519.PublicKey{webhook.KeyID: sign.Public().(ed25519.PublicKey)})
}

// States is what the queue knows of a connection's webhook, for the
// person's page: when its next run is, when a notice was last taken, and
// since when and why calls fail (contracts.ConnectionWebhook's NextAt,
// LastSentAt, FailingSince and Failure; services/connections fills the
// rest from the connection's record). ErrNoWebhook when it has no entry.
func (s *Service) States(ctx context.Context, connection string) (*contracts.ConnectionWebhook, error) {
	seal, _, err := s.keys(ctx)
	if err != nil {
		return nil, err
	}
	e, err := s.cfg.Entries.GetEntry(ctx, shard(connection), connection)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNoWebhook
	}
	if err != nil {
		return nil, err
	}
	var st state
	if err := openFrom(seal, e, &st); err != nil {
		return nil, err
	}
	return &contracts.ConnectionWebhook{NextAt: st.NextRun, LastSentAt: st.LastTaken, FailingSince: st.FailingSince, Failure: st.Failure}, nil
}

// Subscribe implements Queue: the connection's state entry (its cursor and
// its coverage boundary at its making), its mailboxes' index entries and
// its first due minute (now for new mail, its first run for a schedule).
// Calling it again changes nothing.
func (s *Service) Subscribe(ctx context.Context, sub Subscription) error {
	seal, _, err := s.keys(ctx)
	if err != nil {
		return err
	}
	if sub.Schedule != nil {
		if _, err := webhook.Zone(sub.TimeZone); err != nil {
			return err
		}
	}
	st := newState(&sub, sub.MadeAt)
	due := s.now()
	if !sub.NewMail {
		due = st.NextRun
	}
	return s.build(ctx, seal, &store.Entry{Partition: shard(sub.Connection), Key: sub.Connection}, &sub, st, due)
}

// newState is a subscription's state as if nothing was told yet: its new
// mail evaluated up to from (and nothing before it swept late), its
// schedule run up to its making, its coverage from its making.
func newState(sub *Subscription, from time.Time) *state {
	st := &state{
		Connection: sub.Connection, Account: sub.Account, Host: hostOf(sub.URL),
		Progress: Progress{Cursor: Cursor{Received: from}, LateFrom: from},
		LastRun:  sub.MadeAt, Coverage: webhook.Coverage(sub.MadeAt),
	}
	if sub.NewMail {
		st.Mailboxes = sub.Mailboxes
	}
	if sub.Schedule != nil {
		// Subscribe and the repair check the zone before.
		loc, _ := webhook.Zone(sub.TimeZone)
		st.NextRun = webhook.Next(*sub.Schedule, loc, sub.MadeAt)
	}
	return st
}

// build writes a subscription's state st into e (a new entry, or one read
// that is replaced), unless a new one is there already, then makes what is
// missing of its index entries and a due entry at due's minute.
func (s *Service) build(ctx context.Context, seal []byte, e *store.Entry, sub *Subscription, st *state, due time.Time) error {
	r := route{Host: st.Host, Account: st.Account}
	st.Due = later(due.UTC(), s.now()).Truncate(time.Minute)
	if err := s.put(ctx, seal, e, st); err != nil && !errors.Is(err, store.ErrExists) {
		return err
	}
	if sub.NewMail {
		for _, m := range sub.Mailboxes {
			if err := s.index(ctx, seal, m, sub.Connection, r); err != nil {
				return err
			}
		}
	}
	return s.markDue(ctx, seal, sub.Connection, r, due)
}

// index makes a mailbox's index entry naming the connection.
func (s *Service) index(ctx context.Context, seal []byte, mailbox, connection string, r route) error {
	e := &store.Entry{Partition: PartitionMailbox + mailbox, Key: connection}
	if err := s.put(ctx, seal, e, r); err != nil && !errors.Is(err, store.ErrExists) {
		return err
	}
	return nil
}

// markDue makes the connection due in at's minute. An entry already there
// is written again, so a worker that read it before deletes it no more
// (store.DeleteEntryAt): a hint that comes while a worker works is kept.
func (s *Service) markDue(ctx context.Context, seal []byte, connection string, r route, at time.Time) error {
	// Never in a minute the workers may have listed already.
	minute := later(at.UTC(), s.now()).Truncate(time.Minute)
	e := &store.Entry{Partition: minuteKey(minute), Key: connection, ExpiresAt: minute.Add(DueKept)}
	err := s.put(ctx, seal, e, r)
	if !errors.Is(err, store.ErrExists) {
		return err
	}
	old, err := s.cfg.Entries.GetEntry(ctx, e.Partition, e.Key)
	if err != nil {
		return err
	}
	return s.put(ctx, seal, old, r)
}

// hint is an arrival in a mailbox, or a connection that ended.
type hint struct {
	mailbox    string
	connection string
}

// Arrived implements inbox.Arrivals: it returns at once, never failing or
// waiting; a hint past hintsMax is lost, and the repair finds its mail.
func (s *Service) Arrived(mailbox string, _ time.Time) {
	select {
	case s.hints <- hint{mailbox: mailbox}:
	default:
	}
}

// Ended implements Queue: the connection is made due, so its next touch
// drops it; the repair does either way.
func (s *Service) Ended(connection string) {
	select {
	case s.hints <- hint{connection: connection}:
	default:
	}
}

// hinted makes due now every connection a hint is about.
func (s *Service) hinted(ctx context.Context, h hint) {
	seal, _, err := s.keys(ctx)
	if err != nil {
		s.fail("hint", err)
		return
	}
	now := s.now()
	if h.connection != "" {
		e, err := s.cfg.Entries.GetEntry(ctx, shard(h.connection), h.connection)
		var st state
		if err == nil {
			err = openFrom(seal, e, &st)
		}
		if err == nil {
			err = s.markDue(ctx, seal, h.connection, route{Host: st.Host, Account: st.Account}, now)
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.fail("ended", err)
		}
		return
	}
	for after := ""; ; {
		entries, err := s.cfg.Entries.ListEntries(ctx, PartitionMailbox+h.mailbox, "", after, RepairPage)
		if err != nil {
			s.fail("arrival", err)
			return
		}
		for i := range entries {
			var r route
			if openFrom(seal, &entries[i], &r) != nil {
				continue
			}
			if err := s.markDue(ctx, seal, entries[i].Key, r, now); err != nil {
				s.fail("arrival", err)
			}
		}
		if len(entries) < RepairPage {
			return
		}
		after = entries[len(entries)-1].Key
	}
}

// Timings of Run (set by tests only): how often the due minutes are listed, and how often the
// repair's lease is looked at (it runs once every RepairEvery).
var (
	passEvery        = 5 * time.Second
	repairCheckEvery = time.Minute
)

// Run is the service's work until ctx ends: the hints, a pass over the due
// minutes every passEvery, more work as soon as a slot frees, and the
// hourly repair. Calls under way are waited for.
func (s *Service) Run(ctx context.Context) {
	pass := time.NewTicker(passEvery)
	defer pass.Stop()
	repair := time.NewTicker(repairCheckEvery)
	defer repair.Stop()
	var repairing sync.WaitGroup
	defer repairing.Wait()
	defer s.work.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case h := <-s.hints:
			s.hinted(ctx, h)
		case <-s.wake:
			s.dispatch(ctx)
		case <-pass.C:
			s.pass(ctx)
		case <-repair.C:
			repairing.Go(func() { s.repair(ctx) })
		}
	}
}
