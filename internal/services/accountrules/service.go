package accountrules

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"crypto/rand"

	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
)

// The service behind the seam (ADR 0040's pull request 1): the account's
// record of rules, one strict anchored record per epoch, sealed under a key
// from the root secret and bound to its name; read from the newest epoch this
// enclave knows, following Next, making again an epoch a crash left unmade;
// written at the version read, rolling to the next epoch at
// rules.RecordVersionsMax with the earlier one removed, as the account's list
// of connections does (ADR 0030, C2); and a cache per account at most Fresh
// old, counted from when its read began.

// Purpose names the key that seals the account's record of rules
// (keys.Derive).
const Purpose = "permaily rules v1"

// Records is the strict store.Anchored the composition gives the service.
type Records interface {
	GetRecordAt(ctx context.Context, name string) (*store.Record, store.Mark, error)
	PutRecordAt(ctx context.Context, r *store.Record, at store.Mark) error
	CreateNew(ctx context.Context, r *store.Record) error
	RemoveRecord(ctx context.Context, name string) error
}

// Keys derives keys for one purpose from the root secret.
type Keys interface {
	Derive(ctx context.Context, purpose string) ([]byte, error)
}

// Mailboxes is what a rule's criteria may name: the account's mailboxes, by
// identifier, as the connections service reads them.
type Mailboxes interface {
	Catalogue(ctx context.Context, account string) ([]string, error)
}

// Config is what the service is made of.
type Config struct {
	// Records is the strict store.Anchored of the account's records.
	Records Records
	Keys    Keys
	// Receipts writes a `rules` receipt before each change (ADR 0040,
	// "Receipts").
	Receipts  receipts.Writer
	Mailboxes Mailboxes
	// Notice is the rules' notice in force: domain.RulesNoticeVersion when
	// empty. The tests change it, as a new release would.
	Notice string
	// Now is the clock; Random fills b with random bytes; Log is where the
	// kinds of what could not be read go (nothing of an account).
	Now    func() time.Time
	Random func(b []byte) error
	Log    *slog.Logger
}

// Service is the person's rules (Rules, and so Source).
type Service struct {
	cfg Config
	key []byte

	mu sync.Mutex
	// epochs is the newest epoch of each account's record this enclave
	// saw: epochs only go forward.
	epochs map[string]uint64
	// cache is each account's record as last read or written here.
	cache map[string]cached
	// pruned is, by account, the epoch below which this enclave saw every
	// earlier epoch removed.
	pruned map[string]uint64

	unreadable atomic.Int64
}

var _ Rules = (*Service)(nil)

// cached is an account's record (nil: none) at its epoch and version, as a
// read that began at began found it, or as a change this enclave made after
// a read that began then wrote it.
type cached struct {
	rec     *rules.Record
	epoch   uint64
	version uint64
	began   time.Time
}

// cacheMax bounds the accounts whose record an enclave keeps: past it the
// cache is emptied, and the next request of each account reads again.
const cacheMax = 10000

// errMissing is a Config without what the service needs.
var errMissing = errors.New("accountrules: Records, Keys, Receipts and Mailboxes are needed")

// New makes the service, deriving its key.
func New(cfg Config) (*Service, error) {
	if cfg.Records == nil || cfg.Keys == nil || cfg.Receipts == nil || cfg.Mailboxes == nil {
		return nil, errMissing
	}
	if cfg.Notice == "" {
		cfg.Notice = domain.RulesNoticeVersion
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = func(b []byte) error { _, err := rand.Read(b); return err }
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	key, err := cfg.Keys.Derive(context.Background(), Purpose)
	if err != nil {
		return nil, err
	}
	return &Service{cfg: cfg, key: key, epochs: map[string]uint64{}, cache: map[string]cached{}, pruned: map[string]uint64{}}, nil
}

func (s *Service) now() time.Time { return s.cfg.Now() }

// Unreadable counts the requests answered ErrUnavailable since the enclave
// started: /status's rules_unreadable (ADR 0040).
func (s *Service) Unreadable() int64 { return s.unreadable.Load() }

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// newID is "rul_" and 26 base32 characters of 16 random bytes.
func (s *Service) newID() (string, error) {
	b := make([]byte, 16)
	if err := s.cfg.Random(b); err != nil {
		return "", err
	}
	return "rul_" + idEncoding.EncodeToString(b), nil
}

// Errors of the record, each a kind a log line may name.
var (
	// errAbsent is an epoch with no value: never written, or removed.
	errAbsent = errors.New("accountrules: no record at this epoch")
	// errOpen is a record that does not open under this deployment's key
	// bound to its name.
	errOpen = errors.New("accountrules: the record does not open")
	// errDocument is a record that opens and is not the document its name
	// says: another account's, another epoch's, another format, too large.
	errDocument = errors.New("accountrules: the record is not this account's rules")
	// errBusy is a walk over the epochs that did not end.
	errBusy = errors.New("accountrules: the epochs of the record kept changing")
)

func aad(name string) []byte { return envelope.AAD("record", name) }

func (s *Service) epoch(account string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.epochs[account]
}

func (s *Service) saw(account string, e uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e > s.epochs[account] {
		s.epochs[account] = e
	}
}

// open reads epoch e of an account's record strictly and opens it:
// errAbsent (with the mark) when it has no value.
func (s *Service) open(ctx context.Context, account string, e uint64) (*rules.Record, store.Mark, error) {
	name := rules.RecordName(account, e)
	r, at, err := s.cfg.Records.GetRecordAt(ctx, name)
	if errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrLost) {
		return nil, at, errAbsent
	}
	if err != nil {
		return nil, at, err
	}
	if r.Plain {
		return nil, at, errOpen
	}
	plain, err := envelope.OpenText(s.key, r.Value, aad(name))
	if err != nil {
		return nil, at, errOpen
	}
	if len(plain) > rules.RecordBytesMax {
		return nil, at, errDocument
	}
	var rec rules.Record
	if json.Unmarshal(plain, &rec) != nil || rec.V != rules.RecordFormat || rec.Account != account || rec.Epoch != e {
		return nil, at, errDocument
	}
	if rec.Rules == nil {
		rec.Rules = []rules.Stored{}
	}
	return &rec, at, nil
}

// read is the newest epoch of an account's record, read strictly and now,
// and its mark: nil when the account has none. An epoch removed once its
// successor was committed (a removal's commit after a value: version above
// 1) is passed over to the next; the fence a removal of the account leaves
// after its last epoch (a removal's commit on a name that never had a value,
// version 1) reads as none, as epoch 0 never written does. An epoch above 0
// never written follows one written Next by a writer that stopped before it
// made this one, which is made again here. Anything else is an error, never
// taken for an account with no rules.
func (s *Service) read(ctx context.Context, account string) (*rules.Record, store.Mark, error) {
	e := s.epoch(account)
	for range rules.RecordVersionsMax {
		rec, at, err := s.open(ctx, account, e)
		switch {
		case err == nil && rec.Next:
			e++
			s.saw(account, e)
			continue
		case err == nil:
			s.saw(account, e)
			return rec, at, nil
		case !errors.Is(err, errAbsent):
			return nil, at, err
		case at.Version() > 1:
			e++
			continue
		case at.Version() == 1 || e == 0:
			return nil, at, nil
		}
		if err := s.remake(ctx, account, e); err != nil {
			return nil, at, err
		}
	}
	return nil, store.Mark{}, errBusy
}

// remake makes epoch e of an account's record again from e-1, written Next
// by a writer that stopped before it made e, and removes e-1.
func (s *Service) remake(ctx context.Context, account string, e uint64) error {
	prev, _, err := s.open(ctx, account, e-1)
	if errors.Is(err, errAbsent) || (err == nil && !prev.Next) {
		return store.ErrRolledBack
	}
	if err != nil {
		return err
	}
	next := successor(prev)
	if err := s.seal(ctx, next, store.Mark{}, true); err != nil && !errors.Is(err, store.ErrExists) {
		return err
	}
	_ = s.prune(ctx, account, e) // or the next write of e does
	return nil
}

// successor is the epoch after rec, holding everything it held.
func successor(rec *rules.Record) *rules.Record {
	next := *rec
	next.Rules = slices.Clone(rec.Rules)
	next.Epoch, next.Next = rec.Epoch+1, false
	return &next
}

// seal writes rec sealed and bound to its name: made with CreateNew when
// create, else at the mark it was read at.
func (s *Service) seal(ctx context.Context, rec *rules.Record, at store.Mark, create bool) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if len(raw) > rules.RecordBytesMax {
		return errDocument
	}
	name := rules.RecordName(rec.Account, rec.Epoch)
	value, err := envelope.SealText(s.key, raw, aad(name))
	if err != nil {
		return err
	}
	r := &store.Record{Name: name, Value: value, UpdatedAt: s.now()}
	if create {
		return s.cfg.Records.CreateNew(ctx, r)
	}
	return s.cfg.Records.PutRecordAt(ctx, r, at)
}

// write writes rec at the mark it was read at (made with CreateNew when
// create) and rolls it to its next epoch when this write reaches
// rules.RecordVersionsMax: the epoch is written Next with the change, the
// next is made with everything, and the earlier one removed. It answers
// the record now current and its version.
func (s *Service) write(ctx context.Context, rec *rules.Record, at store.Mark, create bool) (*rules.Record, uint64, error) {
	if create {
		if err := s.seal(ctx, rec, at, true); err != nil {
			return nil, 0, err
		}
		return rec, 1, nil
	}
	if rules.Rotate(at.Version()) {
		rec.Next = true
		if err := s.seal(ctx, rec, at, false); err != nil {
			rec.Next = false
			return nil, 0, err
		}
		next := successor(rec)
		s.saw(rec.Account, next.Epoch)
		// The change is committed with the epoch written Next. A stop or a
		// failure here leaves the next epoch unmade: the next reader makes
		// it (remake), with what this one holds.
		if err := s.seal(ctx, next, store.Mark{}, true); err != nil && !errors.Is(err, store.ErrExists) {
			return rec, at.Version() + 1, nil
		}
		// The successor is committed: the closed epoch goes, its removal's
		// commit its fence. A stop or a failure here leaves it to the next
		// write of the successor.
		_ = s.prune(ctx, rec.Account, next.Epoch)
		return next, 1, nil
	}
	if err := s.seal(ctx, rec, at, false); err != nil {
		return nil, 0, err
	}
	if s.prunedBelow(rec.Account) < rec.Epoch {
		_ = s.prune(ctx, rec.Account, rec.Epoch)
	}
	return rec, at.Version() + 1, nil
}

func (s *Service) prunedBelow(account string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruned[account]
}

// prune removes every epoch of an account's record before current that
// still has a value, the oldest first, once current is committed: its
// removal's commit stays as its fence (a removed record never reads again,
// whatever of it is put back, and CreateNew refuses its name). Removing the
// oldest first keeps the epochs that still have a value a run just below
// current, so the walk down stops at the first removed one.
func (s *Service) prune(ctx context.Context, account string, current uint64) error {
	from := current
	for from > 0 {
		_, at, err := s.cfg.Records.GetRecordAt(ctx, rules.RecordName(account, from-1))
		if errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrLost) {
			if at.Version() == 0 {
				return store.ErrRolledBack // an epoch below a committed one was never made
			}
			break
		}
		if err != nil && !errors.Is(err, store.ErrLost) {
			return err
		}
		from--
	}
	for e := from; e < current; e++ {
		if err := s.cfg.Records.RemoveRecord(ctx, rules.RecordName(account, e)); err != nil {
			return err
		}
	}
	s.mu.Lock()
	if current > s.pruned[account] {
		s.pruned[account] = current
	}
	s.mu.Unlock()
	return nil
}

// keep caches c for account unless what is cached is newer: a higher epoch
// or version, or the same one read by a read that began later. So a read
// that began before a change and answered after it never replaces the
// change, and a read that failed changes nothing.
func (s *Service) keep(account string, c cached) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.cache[account]; ok {
		switch {
		case old.epoch > c.epoch, old.epoch == c.epoch && old.version > c.version:
			return
		case old.epoch == c.epoch && old.version == c.version && old.began.After(c.began):
			return
		}
	}
	if len(s.cache) >= cacheMax {
		clear(s.cache)
	}
	s.cache[account] = c
}

// current is the account's record from a cache at most Fresh old, counted
// from when its read began, or read now.
func (s *Service) current(ctx context.Context, account string) (cached, error) {
	now := s.now()
	s.mu.Lock()
	c, ok := s.cache[account]
	s.mu.Unlock()
	if age := now.Sub(c.began); ok && age >= 0 && age < Fresh {
		return c, nil
	}
	began := s.now()
	rec, at, err := s.read(ctx, account)
	if err != nil {
		return cached{}, err
	}
	c = cached{rec: rec, began: began}
	if rec != nil {
		c.epoch, c.version = rec.Epoch, at.Version()
	}
	s.keep(account, c)
	return c, nil
}

// Applying implements Source.
func (s *Service) Applying(ctx context.Context, account, client string) (Applying, error) {
	c, err := s.current(ctx, account)
	if err == nil {
		var rs []rules.Rule
		if rs, err = c.rec.For(client, s.cfg.Notice); err == nil {
			return Applying{Rules: rs, Epoch: c.epoch, Version: c.version}, nil
		}
		err = fmt.Errorf("%w: %w", errCompile, err)
	}
	return Applying{}, s.unavailable(ctx, err)
}

// errCompile is a stored rule that does not compile.
var errCompile = errors.New("accountrules: a stored rule does not compile")

// unavailable counts and logs, by its kind only, why an account's rules
// could not be used, and answers ErrUnavailable wrapping it.
func (s *Service) unavailable(ctx context.Context, err error) error {
	s.unreadable.Add(1)
	return s.failed(ctx, err)
}

// failed logs, by its kind only, why a change could not be recorded or
// written, and answers ErrUnavailable wrapping it, without counting it as a
// record that could not be read.
func (s *Service) failed(ctx context.Context, err error) error {
	if ctx.Err() == nil {
		s.cfg.Log.Warn("accountrules: an account's rules cannot be used", "kind", kindOf(err))
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

// kindOf is the fixed words a log line says of err.
func kindOf(err error) string {
	for _, k := range []struct {
		err  error
		kind string
	}{
		{store.ErrForked, "forked"}, {store.ErrLost, "lost"}, {store.ErrRolledBack, "rolled_back"},
		{errOpen, "unopenable"}, {errDocument, "not_its_document"}, {errCompile, "does_not_compile"},
		{errBusy, "busy"}, {context.Canceled, "cancelled"}, {context.DeadlineExceeded, "deadline"},
	} {
		if errors.Is(err, k.err) {
			return k.kind
		}
	}
	return "storage"
}

// listVersion is the version of an account's rules as the rules page names
// it (contracts.RuleList.Version): one number that only goes up across
// epochs, each epoch holding at most rules.RecordVersionsMax versions; 0 for
// an account with no record.
func listVersion(rec *rules.Record, at store.Mark) uint64 {
	if rec == nil {
		return 0
	}
	return rec.Epoch*rules.RecordVersionsMax + at.Version()
}
