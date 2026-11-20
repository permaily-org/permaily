package connections

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/services/notices"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
)

// The service (ADR 0030's pull request 3). service.go holds what every part
// shares: the keys, the records and entries as stored, and what one enclave
// keeps in memory. Everything that decides is read from the strict records
// (docs.go, lists.go, families.go, assertions.go); memory only caches reads
// for oauth.ConnectionFresh, remembers what this enclave revoked and holds
// the leases of the answers it is sending (lease.go).

var (
	_ API       = (*Service)(nil)
	_ Lifecycle = (*Service)(nil)
)

// purposeFamilies derives the key of a family's tokens (oauth.FamilySecret).
const purposeFamilies = "permaily oauth families v1"

// Service is the connections service.
type Service struct {
	cfg Config

	sealKey, signKey, cursorKey, familyKey []byte

	mu sync.Mutex
	// revoked are the connections and the devices (families) this enclave
	// revoked since it started: refused here at once, whatever a cache says.
	revoked map[string]int
	conns   map[string]cachedConn
	lists   map[string]cachedList
	stand   map[string]cachedStanding
	// epochs are the newest epoch of each list and family this enclave read:
	// epochs only go forward.
	epochs map[string]uint64
	// scopes are devices' narrower formulas, by their canonical identifier
	// (content-addressed: a scope found under an identifier is that scope).
	scopes map[string][]*query.Canonical
	// used are the requests and pushed requests ended here, and the jtis
	// accepted here, until their end: they spare a read, and decide nothing.
	used    map[string]time.Time
	refused map[string]int64
	leases  map[string]map[*lease]struct{}
	limits  limits
	// refusals are each connection's lane of refusals (refusals.go), and
	// adm the attributed work Stop waits for.
	refusals refusalLanes
	adm      admission
	// hooks are the permits of webhook calls and what else of webhooks
	// this enclave keeps (webhook_permit.go).
	hooks hooks
	// endings are the keys of the endings this enclave queued since it
	// started (notifyEnding), and enqueueMax how long a notice may hold
	// its caller (notices.EnqueueMax).
	endings    map[string]bool
	enqueueMax time.Duration
}

type cachedConn struct {
	doc  *connDoc
	at   store.Mark
	read time.Time
	err  error
}

type cachedList struct {
	closing bool
	read    time.Time
}

type cachedStanding struct {
	s    Standing
	read time.Time
}

// cacheMax bounds each cache: past it, entries older than ConnectionFresh go.
const cacheMax = 10000

// errMissing is what New says of a Config without one of what it needs.
var errMissing error = &kind{"connections: the service needs its records, entries, keys, vendors, accounts, mailboxes, the rules' source and receipts"}

// New makes the service. The keys are derived once.
func New(cfg Config) (*Service, error) {
	if cfg.Records == nil || cfg.Entries == nil || cfg.Keys == nil || cfg.Vendors == nil || cfg.Accounts == nil || cfg.Mailboxes == nil || cfg.Source == nil || cfg.Receipts == nil {
		return nil, errMissing
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = randomBytes
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	s := &Service{
		cfg: cfg, revoked: map[string]int{}, conns: map[string]cachedConn{}, lists: map[string]cachedList{},
		stand: map[string]cachedStanding{}, epochs: map[string]uint64{}, scopes: map[string][]*query.Canonical{},
		used: map[string]time.Time{}, refused: map[string]int64{}, leases: map[string]map[*lease]struct{}{},
		limits: newLimits(), endings: map[string]bool{}, enqueueMax: notices.EnqueueMax,
		refusals: refusalLanes{by: map[string]*refusalLane{}, lanesMax: RefusalLanesMax, bytesMax: RefusalBytesMax, waitMax: refusalWaitMax, size: receipts.Size},
	}
	ctx := context.Background()
	for _, k := range []struct {
		key     *[]byte
		purpose string
	}{{&s.sealKey, PurposeSeal}, {&s.signKey, PurposeSign}, {&s.cursorKey, PurposeCursor}, {&s.familyKey, purposeFamilies}, {&s.hooks.key, PurposeWebhookSeal}} {
		b, err := cfg.Keys.Derive(ctx, k.purpose)
		if err != nil {
			return nil, err
		}
		*k.key = b
	}
	return s, nil
}

func (s *Service) now() time.Time { return s.cfg.Now() }

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// newID is kind, "_" and 26 base32 characters of 16 random bytes.
func (s *Service) newID(kind string) (string, error) {
	b := make([]byte, 16)
	if err := s.cfg.Random(b); err != nil {
		return "", err
	}
	return kind + "_" + idEncoding.EncodeToString(b), nil
}

// count counts a refusal or an event by its fixed reason (Refused).
func (s *Service) count(reason string) {
	s.mu.Lock()
	s.refused[reason]++
	s.mu.Unlock()
}

// Refused implements API.
func (s *Service) Refused() map[string]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int64, len(s.refused))
	for k, v := range s.refused {
		out[k] = v
	}
	return out
}

// ---- records ----

// refusedRecord says a record that could not be read is one that must be
// refused (lost, rolled back, forked, not opening), rather than storage that
// could not be reached.
func refusedRecord(err error) bool {
	return errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrRolledBack) || errors.Is(err, store.ErrForked) ||
		errors.Is(err, envelope.ErrSigned) || errors.Is(err, envelope.ErrOpen) || errors.Is(err, errDocument)
}

// errDocument is a record or an entry whose document does not read.
var errDocument error = &kind{"connections: a stored document does not read"}

func recordAAD(name string) []byte { return envelope.AAD("record", name) }

// readSigned reads a readable, signed record strictly, into v; found is
// false (and the mark the one to make it at) when it was never made.
func (s *Service) readSigned(ctx context.Context, name string, v any) (store.Mark, bool, error) {
	rec, at, err := s.cfg.Records.GetRecordAt(ctx, name)
	if errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrLost) {
		return at, false, nil
	}
	if err != nil {
		return at, false, err
	}
	raw, err := envelope.Verified(s.signKey, rec.Value, recordAAD(name))
	if err != nil {
		return at, false, err
	}
	if json.Unmarshal(raw, v) != nil {
		return at, false, errDocument
	}
	return at, true, nil
}

// writeSigned writes v signed: made with CreateNew when create, else at the
// mark it was read at, within WriteMax.
func (s *Service) writeSigned(ctx context.Context, name string, v any, at store.Mark, create bool) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	value, err := envelope.Sign(s.signKey, raw, recordAAD(name))
	if err != nil {
		return err
	}
	r := &store.Record{Name: name, Value: value, Plain: true, UpdatedAt: s.now()}
	ctx = store.WriteWithin(ctx, WriteMax)
	if create {
		return s.cfg.Records.CreateNew(ctx, r)
	}
	return s.cfg.Records.PutRecordAt(ctx, r, at)
}

// readSealed and writeSealed are the same for a sealed record.
func (s *Service) readSealed(ctx context.Context, name string, v any) (store.Mark, bool, error) {
	rec, at, err := s.cfg.Records.GetRecordAt(ctx, name)
	if errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrLost) {
		return at, false, nil
	}
	if err != nil {
		return at, false, err
	}
	raw, err := envelope.Open(s.sealKey, rec.Value, recordAAD(name))
	if err != nil {
		return at, false, err
	}
	if json.Unmarshal(raw, v) != nil {
		return at, false, errDocument
	}
	return at, true, nil
}

func (s *Service) writeSealed(ctx context.Context, name string, v any, at store.Mark, create bool) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	value, err := envelope.Seal(s.sealKey, raw, recordAAD(name))
	if err != nil {
		return err
	}
	r := &store.Record{Name: name, Value: value, UpdatedAt: s.now()}
	ctx = store.WriteWithin(ctx, WriteMax)
	if create {
		return s.cfg.Records.CreateNew(ctx, r)
	}
	return s.cfg.Records.PutRecordAt(ctx, r, at)
}

// retry says a write lost a race and is to be read and decided again.
func retry(err error) bool {
	return errors.Is(err, store.ErrChanged) || errors.Is(err, store.ErrExists)
}

// errBusy is a record that kept changing for RecordTries reads.
var errBusy error = &kind{"connections: a record kept changing"}

// ---- entries ----

// entryKey is where an entry's document is in its partition; markKey is the
// mark that ends a request or a pushed request.
const (
	entryKey = "v"
	markKey  = "used"
)

func entryAAD(partition, key string) []byte { return envelope.AAD("entry", partition, key) }

// putEntry stores v sealed as a new entry of partition until expires;
// store.ErrExists when it is there already.
func (s *Service) putEntry(ctx context.Context, partition string, v any, expires time.Time) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	value, err := envelope.SealText(s.sealKey, raw, entryAAD(partition, entryKey))
	if err != nil {
		return err
	}
	return s.cfg.Entries.PutEntry(ctx, &store.Entry{Partition: partition, Key: entryKey, Value: value, ExpiresAt: expires, UpdatedAt: s.now()})
}

// getEntry reads an entry of partition into v: store.ErrNotFound for one
// that is not there, has expired, or does not open (moved, made by hand).
func (s *Service) getEntry(ctx context.Context, partition string, v any) error {
	e, err := s.cfg.Entries.GetEntry(ctx, partition, entryKey)
	if err != nil {
		return err
	}
	if !e.ExpiresAt.IsZero() && !s.now().Before(e.ExpiresAt) {
		return store.ErrNotFound
	}
	raw, err := envelope.OpenText(s.sealKey, e.Value, entryAAD(partition, entryKey))
	if err != nil || json.Unmarshal(raw, v) != nil {
		return store.ErrNotFound
	}
	return nil
}

// endOnce ends a request or a pushed request once, by a mark at version 0
// beside its entry and in memory: false when it was ended already.
func (s *Service) endOnce(ctx context.Context, partition string, until time.Time) (bool, error) {
	s.mu.Lock()
	if t, ok := s.used[partition]; ok && s.now().Before(t) {
		s.mu.Unlock()
		return false, nil
	}
	s.mu.Unlock()
	err := s.cfg.Entries.PutEntry(ctx, &store.Entry{Partition: partition, Key: markKey, Value: []byte("{}"), ExpiresAt: until, UpdatedAt: s.now()})
	if errors.Is(err, store.ErrExists) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s.remember(partition, until)
	return true, nil
}

// remember keeps a used request or jti in memory until its end.
func (s *Service) remember(key string, until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.used) >= cacheMax {
		now := s.now()
		for k, t := range s.used {
			if !now.Before(t) {
				delete(s.used, k)
			}
		}
	}
	s.used[key] = until
}

// usedHere says this enclave ended key and it has not expired.
func (s *Service) usedHere(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.used[key]
	return ok && s.now().Before(t)
}

// ---- standing ----

// standing is the account's, from a read at most ConnectionFresh old unless
// fresh, when it is read now.
func (s *Service) standing(ctx context.Context, account string, fresh bool) (Standing, time.Time, error) {
	now := s.now()
	if !fresh {
		s.mu.Lock()
		c, ok := s.stand[account]
		s.mu.Unlock()
		if ok && now.Sub(c.read) < oauthFresh {
			return c.s, c.read, nil
		}
	}
	st, err := s.cfg.Accounts.ConnectionStanding(ctx, account)
	if err != nil {
		return 0, now, err
	}
	s.mu.Lock()
	if old, ok := s.stand[account]; !ok || !old.read.After(now) {
		if len(s.stand) >= cacheMax {
			clear(s.stand)
		}
		s.stand[account] = cachedStanding{s: st, read: now}
	}
	s.mu.Unlock()
	return st, now, nil
}

// openForApproval says an account may approve or renew: open, read now.
func (s *Service) openForApproval(ctx context.Context, account string) error {
	st, _, err := s.standing(ctx, account, true)
	if err != nil {
		return err
	}
	return standingError(st)
}

// standingError is what a person is told of a standing that does not allow
// approving.
func standingError(st Standing) error {
	switch st {
	case StandingOpen:
		return nil
	case StandingNotAgreed:
		return contracts.ErrTermsChanged
	case StandingClosing:
		return contracts.ErrAccountClosing
	}
	return contracts.ErrUnauthorized
}
