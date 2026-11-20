package notices

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// Keys derives a key for one purpose from the root secret.
type Keys interface {
	Derive(ctx context.Context, purpose string) ([]byte, error)
}

// Config is what the queue is given (ADR 0041, "What each pull request
// adds to the seams").
type Config struct {
	// Entries keeps the queue (PartitionQueue) and the marks
	// (PartitionSent), signed under PurposeSign and written at the version
	// read.
	Entries store.Entries
	// Keys derives the signing key (PurposeSign).
	Keys Keys
	// Deliverer words and sends a notice (accounts, ADR 0041's pull
	// request 2).
	Deliverer Deliverer
	// Now is the clock; Random fills b with random bytes (the name this
	// enclave writes in its leases).
	Now    func() time.Time
	Random func(b []byte) error
	// Log takes the kind and the outcome of what the queue does, never an
	// account, a key, an address or an error's words.
	Log *slog.Logger
}

// ErrConfig is a Config missing what the queue needs.
var ErrConfig = errors.New("notices: the queue's configuration is incomplete")

// errKey is a signing key that is not one.
var errKey = errors.New("notices: the signing key is not of its size")

// waitingMax bounds the first attempts that wait for admission in one
// enclave: past it, a notice queued is left to the runners, which find it
// due at once. Each waiting attempt is a goroutine holding nothing but its
// key.
const waitingMax = 1024

// What a mark says of a notice, and what the log says of one.
const (
	outcomeQueued  = "queued"
	outcomeFailed  = "failed"
	outcomeSent    = "sent"
	outcomeDropped = "dropped"
	outcomeSkipped = "skipped"
	outcomeRefused = "refused"
)

// Service is the queue of notices (ADR 0041, "The queue"): it implements
// Queue; Run is its runner, Drain one pass of it, and Status its counts.
type Service struct {
	cfg  Config
	log  *slog.Logger
	name string

	// life ends when Run's context does: the first attempts waiting for
	// admission give up, and the runners find their notices.
	life context.Context
	stop context.CancelFunc

	keyMu sync.Mutex
	key   []byte

	// Admission: one of InFlightMax workers, then the rate.
	slots      chan struct{}
	pace       *pacer
	attemptFor time.Duration
	every      time.Duration

	busy tracker

	mu      sync.Mutex
	waiting int
	cursor  string
	refused map[string]bool
	st      attestation.NoticesStatus
}

var _ Queue = (*Service)(nil)

// New is the queue over cfg.
func New(cfg Config) (*Service, error) {
	if cfg.Entries == nil || cfg.Keys == nil || cfg.Deliverer == nil {
		return nil, ErrConfig
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = func(b []byte) error { _, err := io.ReadFull(rand.Reader, b); return err }
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	name := make([]byte, 16)
	if err := cfg.Random(name); err != nil {
		return nil, err
	}
	life, stop := context.WithCancel(context.Background())
	return &Service{
		cfg: cfg, log: cfg.Log, name: hex.EncodeToString(name), life: life, stop: stop,
		slots: make(chan struct{}, InFlightMax), pace: newPacer(), attemptFor: AttemptFor, every: RunEvery,
		refused: map[string]bool{}, st: attestation.NoticesStatus{Since: cfg.Now().UTC()},
	}, nil
}

func (s *Service) now() time.Time { return s.cfg.Now().UTC() }

// Status is what this enclave did with the queue since it started.
func (s *Service) Status() attestation.NoticesStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

func (s *Service) count(f func(*attestation.NoticesStatus)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.st)
}

// told logs what became of a notice of kind: its kind and outcome alone.
func (s *Service) told(kind Kind, outcome string) {
	s.log.Info("notice", "kind", string(kind), "outcome", outcome)
}

// failed logs that a step of the storage or of the keys failed, by its
// name alone: a storage's error can name a key.
func (s *Service) failed(step string) {
	s.log.Warn("notices: a step failed", "step", step)
}

// signKey derives the signing key once.
func (s *Service) signKey(ctx context.Context) ([]byte, error) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	if s.key == nil {
		k, err := s.cfg.Keys.Derive(ctx, PurposeSign)
		if err != nil {
			return nil, err
		}
		if len(k) != envelope.KeySize {
			return nil, errKey
		}
		s.key = k
	}
	return s.key, nil
}

// entryDoc is a queued notice as signed: the notice, its attempts, when
// the next is due and the lease of the attempt under way.
type entryDoc struct {
	Notice     Notice    `json:"notice"`
	Attempts   int       `json:"attempts"`
	NextAt     time.Time `json:"next_at,omitzero"`
	LeaseUntil time.Time `json:"lease_until,omitzero"`
	By         string    `json:"by,omitempty"`
}

// markDoc is a mark as signed: what became of the notice, and when.
type markDoc struct {
	Outcome string    `json:"outcome"`
	At      time.Time `json:"at"`
}

// aad binds a signed value to its partition and key: moved, it does not
// verify.
func aad(partition, key string) []byte { return []byte(partition + "\x00" + key) }

// entryKeptFor is how long the storage keeps an entry after its notice's
// time: past RetryFor, so a runner drops it before the storage does. The
// window is read from the notice's signed time, never from this.
const entryKeptFor = RetryFor + time.Hour

// open returns the queued notice an entry holds, when it is one this queue
// wrote under its key: at most EntryMax, signed, bound to its place, a
// notice Check takes under that key.
func open(key []byte, e *store.Entry) (entryDoc, bool) {
	var d entryDoc
	if len(e.Value) > EntryMax {
		return d, false
	}
	doc, err := envelope.Verified(key, e.Value, aad(PartitionQueue, e.Key))
	if err != nil || json.Unmarshal(doc, &d) != nil || d.Notice.Check() != nil || d.Notice.Key != e.Key || d.Attempts < 0 {
		return entryDoc{}, false
	}
	return d, true
}

// write stores d as e at e's version (0: created), and leaves e as
// stored. A value past EntryMax is refused before it is written.
func (s *Service) write(ctx context.Context, key []byte, e *store.Entry, d entryDoc) error {
	doc, _ := json.Marshal(d)                                   // strings, numbers and times in bounds (Check)
	v, _ := envelope.Sign(key, doc, aad(PartitionQueue, e.Key)) // a key of its size and a JSON document
	if len(v) > EntryMax {
		return ErrInvalid
	}
	next := *e
	next.Value, next.ExpiresAt = v, d.Notice.At.Add(entryKeptFor)
	if err := s.cfg.Entries.PutEntry(ctx, &next); err != nil {
		return err
	}
	*e = next
	return nil
}

// mark reads the mark of k: held when it verifies and MarkHolds at now.
// The entry is returned whatever it holds, so a mark written over it is
// written at its version.
func (s *Service) mark(ctx context.Context, key []byte, k string, now time.Time) (bool, *store.Entry, error) {
	e, err := s.cfg.Entries.GetEntry(ctx, PartitionSent, k)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	var m markDoc
	doc, err := envelope.Verified(key, e.Value, aad(PartitionSent, k))
	if err != nil || json.Unmarshal(doc, &m) != nil || !MarkHolds(m.At, now) {
		return false, e, nil
	}
	return true, e, nil
}

// writeMark marks k with outcome at now, over the mark read (nil: none).
func (s *Service) writeMark(ctx context.Context, key []byte, k string, over *store.Entry, outcome string, now time.Time) error {
	doc, _ := json.Marshal(markDoc{Outcome: outcome, At: now}) // a string and a time
	v, _ := envelope.Sign(key, doc, aad(PartitionSent, k))     // a key of its size and a document
	e := &store.Entry{Partition: PartitionSent, Key: k, Value: v, ExpiresAt: now.Add(KeptFor)}
	if over != nil {
		e.Version = over.Version
	}
	return s.cfg.Entries.PutEntry(ctx, e)
}

// Enqueue implements Queue: checked, then created once under its key
// unless a mark holds there, with no lease; its first attempt waits for
// admission in the background.
func (s *Service) Enqueue(ctx context.Context, n Notice) error {
	if err := n.Check(); err != nil {
		return err
	}
	if !n.Fresh(s.now()) {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, EnqueueMax)
	defer cancel()
	key, err := s.signKey(ctx)
	if err != nil {
		s.failed("key")
		return err
	}
	held, _, err := s.mark(ctx, key, n.Key, s.now())
	if err != nil {
		s.failed("read")
		return err
	}
	if held {
		return nil
	}
	err = s.write(ctx, key, &store.Entry{Partition: PartitionQueue, Key: n.Key}, entryDoc{Notice: n})
	switch {
	case errors.Is(err, store.ErrExists):
		return nil
	case err != nil:
		s.failed("write")
		return err
	}
	s.count(func(st *attestation.NoticesStatus) { st.Queued++ })
	s.told(n.Kind, outcomeQueued)
	s.first(key, n.Key)
	return nil
}

// first starts the first attempt of k in the background: admitted as the
// runner's are, then the same attempt. Past waitingMax waiting, it is left
// to the runners.
func (s *Service) first(key []byte, k string) {
	s.mu.Lock()
	if s.waiting >= waitingMax {
		s.mu.Unlock()
		return
	}
	s.waiting++
	s.mu.Unlock()
	s.busy.add()
	go func() {
		defer s.busy.done()
		release, err := s.admit(s.life)
		s.mu.Lock()
		s.waiting--
		s.mu.Unlock()
		if err != nil {
			return
		}
		defer release()
		s.attempt(s.life, key, k, false)
	}()
}

// admit waits for a worker, then for the rate, holding nothing of a
// notice; release frees the worker.
func (s *Service) admit(ctx context.Context) (release func(), err error) {
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := s.pace.wait(ctx); err != nil {
		<-s.slots
		return nil, err
	}
	return func() { <-s.slots }, nil
}

// attempt is one attempt at k, once admitted (ADR 0041, "Enqueue, then
// send", 2). It reads the entry: one that does not verify is counted
// refused, one no longer Sendable dropped, one leased or (but with force)
// not yet due left. It takes the lease at the version read, reads the mark
// (one that holds: sent before a crash, the entry is deleted), and
// delivers within attemptFor, which ends before the lease does.
func (s *Service) attempt(ctx context.Context, key []byte, k string, force bool) {
	e, err := s.cfg.Entries.GetEntry(ctx, PartitionQueue, k)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.failed("read")
		}
		return
	}
	d, ok := open(key, e)
	now := s.now()
	switch {
	case !ok:
		s.refuse(e)
		return
	case !d.Notice.Sendable(now):
		s.drop(ctx, key, e, d, now)
		return
	case now.Before(d.LeaseUntil), !force && now.Before(d.NextAt):
		return
	}
	d.LeaseUntil, d.By = now.Add(LeaseFor), s.name
	if err := s.write(ctx, key, e, d); err != nil {
		if !errors.Is(err, store.ErrConflict) {
			s.failed("write")
		}
		return // another attempt took it, or it changed under this one
	}
	held, mark, err := s.mark(ctx, key, k, now)
	if err != nil {
		s.failed("read")
		return // the lease ends, and the next attempt reads it again
	}
	if held {
		if s.cfg.Entries.DeleteEntryAt(ctx, e) != nil {
			s.failed("delete")
		}
		return
	}
	actx, cancel := context.WithTimeout(ctx, min(s.attemptFor, d.LeaseUntil.Sub(now)))
	err = s.cfg.Deliverer.Deliver(actx, d.Notice)
	cancel()
	now = s.now()
	switch {
	case err == nil:
		s.count(func(st *attestation.NoticesStatus) { st.Sent++ })
		s.finish(ctx, key, e, mark, d.Notice.Kind, outcomeSent, now)
	case errors.Is(err, ErrSkip):
		s.count(func(st *attestation.NoticesStatus) { st.Skipped++ })
		s.finish(ctx, key, e, mark, d.Notice.Kind, outcomeSkipped, now)
	case errors.Is(err, ErrInvalid):
		s.count(func(st *attestation.NoticesStatus) { st.Refused++ })
		s.finish(ctx, key, e, mark, d.Notice.Kind, outcomeRefused, now)
	default:
		s.count(func(st *attestation.NoticesStatus) { st.Failed++ })
		s.told(d.Notice.Kind, outcomeFailed)
		d.Attempts++
		d.LeaseUntil, d.By, d.NextAt = time.Time{}, "", now.Add(backoff(d.Attempts))
		if err := s.write(ctx, key, e, d); err != nil && !errors.Is(err, store.ErrConflict) {
			s.failed("write")
		}
	}
}

// finish marks a notice, then deletes its entry at the version written. A
// mark that cannot be written leaves the entry, tried again once its lease
// ends (at least once).
func (s *Service) finish(ctx context.Context, key []byte, e, mark *store.Entry, kind Kind, outcome string, now time.Time) {
	s.told(kind, outcome)
	if err := s.writeMark(ctx, key, e.Key, mark, outcome, now); err != nil {
		s.failed("mark")
		return
	}
	if s.cfg.Entries.DeleteEntryAt(ctx, e) != nil {
		s.failed("delete")
	}
}

// drop marks dropped a notice no longer Sendable, and deletes it; it is
// counted by the enclave that deletes it. One a mark holds (sent before)
// is deleted alone.
func (s *Service) drop(ctx context.Context, key []byte, e *store.Entry, d entryDoc, now time.Time) {
	held, mark, err := s.mark(ctx, key, e.Key, now)
	if err != nil {
		s.failed("read")
		return
	}
	if !held {
		if err := s.writeMark(ctx, key, e.Key, mark, outcomeDropped, now); err != nil {
			s.failed("mark")
			return
		}
	}
	if s.cfg.Entries.DeleteEntryAt(ctx, e) != nil {
		s.failed("delete")
		return
	}
	if !held {
		s.count(func(st *attestation.NoticesStatus) { st.Dropped++ })
		s.told(d.Notice.Kind, outcomeDropped)
	}
}

// refuse counts an entry that does not verify, once per value this
// enclave saw, and leaves it: nothing sends it, and deleting what this
// queue cannot read is not its to do (the storage's expiry, or whoever
// wrote it, removes it).
func (s *Service) refuse(e *store.Entry) {
	sum := sha256.Sum256(append([]byte(e.Key+"\x00"), e.Value...))
	seen := hex.EncodeToString(sum[:])
	s.mu.Lock()
	if s.refused[seen] {
		s.mu.Unlock()
		return
	}
	if len(s.refused) >= ListMax {
		clear(s.refused)
	}
	s.refused[seen] = true
	s.st.Refused++
	s.mu.Unlock()
	s.log.Info("notice", "outcome", outcomeRefused)
}

// backoff is the wait after the attempts-th failed attempt: RetryFirst,
// doubling, at most RetryMax.
func backoff(attempts int) time.Duration {
	d := RetryFirst
	for i := 1; i < attempts && d < RetryMax; i++ {
		d *= 2
	}
	return min(d, RetryMax)
}
