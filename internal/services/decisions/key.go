package decisions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The model's key (ADR 0028, "The key"): given by the operator, kept sealed
// in the strict anchored record RecordKey, read again once older than
// keyFresh, and handed to the provider's client only through the
// credential New gives Config.NewModel. No method of the service returns
// it: Key says whether one is set, since when, and its fingerprint.

// keyFresh is how long an enclave uses the key it read before it reads its
// record again: a key set, replaced or removed through another enclave is
// used, or no longer used, here within that time.
const keyFresh = time.Minute

// maxKeyLength bounds a key the operator gives.
const maxKeyLength = 1024

// ErrInvalidKey refuses a key that is empty, too long, or holds anything
// but visible ASCII. Its words never repeat the key.
var ErrInvalidKey = &contracts.Error{Code: "invalid_model_key", Message: "the key is 1 to 1024 characters of visible ASCII"}

// keyDoc is the record of the key, as sealed. A removed key is a version
// with no key, so an earlier copy that holds one is refused like any other.
type keyDoc struct {
	V         int       `json:"v"`
	Key       string    `json:"key,omitempty"`
	SetAt     time.Time `json:"set_at,omitzero"`
	RemovedAt time.Time `json:"removed_at,omitzero"`
}

// keyCache is the key as this enclave last read it.
type keyCache struct {
	mu     sync.Mutex
	readAt time.Time
	key    string
	setAt  time.Time
}

// keyView is what the service knows of the key without the key itself.
type keyView struct {
	set   bool
	setAt time.Time
	fp    string
}

// fingerprint is the first 16 hexadecimal digits of the SHA-256 of a key,
// which the operator computes from their own copy.
func fingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:16]
}

func validKey(key string) bool {
	if key == "" || len(key) > maxKeyLength {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x21 || key[i] > 0x7e {
			return false
		}
	}
	return true
}

// loadKey returns the key as read less than keyFresh ago, or reads its
// record again: "" when none is set, it was removed, the record is lost,
// forked or does not open, or the read failed (each logged by kind). A read
// that failed is not kept, so the next attempt reads again.
func (s *Service) loadKey(ctx context.Context) (string, time.Time) {
	c := &s.key
	c.mu.Lock()
	defer c.mu.Unlock()
	now := s.now()
	if !c.readAt.IsZero() && now.Sub(c.readAt) < keyFresh && !now.Before(c.readAt) {
		return c.key, c.setAt
	}
	var doc keyDoc
	_, _, err := readRecord(ctx, s, RecordKey, purposeKey, &doc)
	switch {
	case errors.Is(err, errForked):
		s.logKind(slog.LevelError, "decisions: the model's key record is forked; no key is used", "key_forked")
	case errors.Is(err, errUnreadable):
		s.logKind(slog.LevelError, "decisions: the model's key record is lost or does not open; no key is used", "key_unreadable")
	case err != nil:
		s.logKind(slog.LevelWarn, "decisions: the model's key record could not be read; no key is used", "key_read_failed")
		return "", time.Time{}
	}
	c.key, c.setAt, c.readAt = doc.Key, doc.SetAt, now
	if doc.Key == "" {
		c.setAt = time.Time{}
	}
	return c.key, c.setAt
}

// credential is the decide.Credential New gives Config.NewModel: the key
// as last read, or ok false when there is none to use.
func (s *Service) credential(ctx context.Context) (string, bool) {
	key, _ := s.loadKey(ctx)
	return key, key != ""
}

// keyState is what the service knows of the key, for the guard and
// /status.
func (s *Service) keyState(ctx context.Context) keyView {
	key, setAt := s.loadKey(ctx)
	if key == "" {
		return keyView{}
	}
	return keyView{set: true, setAt: setAt, fp: fingerprint(key)}
}

// remember sets the cache to what this enclave just wrote.
func (s *Service) remember(doc *keyDoc) {
	s.key.mu.Lock()
	s.key.key, s.key.setAt, s.key.readAt = doc.Key, doc.SetAt, s.now()
	s.key.mu.Unlock()
}

// SetKey sets or replaces the model's key (contracts.ModelKeySet), sealed
// in its record at the version read: a key replaced does not come back
// from an earlier copy. It answers what Key answers; never the key.
func (s *Service) SetKey(ctx context.Context, in contracts.ModelKeySet) (*contracts.ModelKey, error) {
	if !validKey(in.Key) {
		return nil, ErrInvalidKey
	}
	now := s.now()
	doc, err := changeRecord(ctx, s, RecordKey, purposeKey, func(d *keyDoc, _ bool) bool {
		*d = keyDoc{V: 1, Key: in.Key, SetAt: now}
		return true
	})
	if err != nil {
		s.logKind(slog.LevelError, "decisions: the model's key could not be stored", "key_not_stored")
		return nil, err
	}
	s.remember(doc)
	s.logKind(slog.LevelInfo, "decisions: the model's key was set", "key_set")
	return &contracts.ModelKey{Set: true, SetAt: doc.SetAt, Fingerprint: fingerprint(doc.Key)}, nil
}

// RemoveKey removes the model's key: from then on nothing new is asked
// (inbox.AskOff), here at once and through another enclave within
// keyFresh.
func (s *Service) RemoveKey(ctx context.Context) error {
	now := s.now()
	doc, err := changeRecord(ctx, s, RecordKey, purposeKey, func(d *keyDoc, found bool) bool {
		if !found || d.Key == "" {
			return false
		}
		*d = keyDoc{V: 1, RemovedAt: now}
		return true
	})
	if err != nil {
		s.logKind(slog.LevelError, "decisions: the model's key could not be removed", "key_not_removed")
		return err
	}
	s.remember(doc)
	s.logKind(slog.LevelInfo, "decisions: the model's key was removed", "key_removed")
	return nil
}

// Key says whether a key is set, since when, and its fingerprint, as its
// record says now. A record that is lost, forked or does not open is no
// key.
func (s *Service) Key(ctx context.Context) (*contracts.ModelKey, error) {
	var doc keyDoc
	_, _, err := readRecord(ctx, s, RecordKey, purposeKey, &doc)
	switch {
	case errors.Is(err, errUnreadable):
		s.logKind(slog.LevelError, "decisions: the model's key record is lost, forked or does not open; no key is used", "key_unreadable")
		return &contracts.ModelKey{}, nil
	case err != nil:
		return nil, err
	case doc.Key == "":
		return &contracts.ModelKey{}, nil
	}
	return &contracts.ModelKey{Set: true, SetAt: doc.SetAt, Fingerprint: fingerprint(doc.Key)}, nil
}
