package accounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

// Sign-ins in progress (ADR 0017, "Two enclaves at once"). What a sign-in
// keeps between the request that starts it and the one that ends it is in the
// storage, so that the second request can land on another enclave: an attempt
// by mail (a sign-in, a change of address, a confirmation), a passkey ceremony,
// a trip to a provider, and a provider's sign-in that waits for its address.
//
// Each is an entry of its own, sealed (envelope.SealText) with a key that comes
// from the root secret and bound to where it is stored: whoever reads the table
// learns nothing of it, not the address, not the hash of a code (a six-digit
// code's hash falls to a million tries offline), not a token, a challenge, a
// verifier, a state, a nonce or an invitation. When it was made and when it
// ends are sealed with the rest; the entry's own expiry only lets the storage
// drop it.
//
// It works once. Ending it writes a mark that it was used next to it, where
// none is (PutEntry at version 0), and then deletes it as it was read: of two
// enclaves that end the same one, exactly one writes the mark, and a copy of
// the entry put back in the table is refused by every enclave while the mark
// is there. Each enclave also remembers, for as long as the entry could be
// ended, that it ended it and how many wrong answers it took here (marks): so
// a copy put back together with the mark deleted still ends nothing in the
// enclave that ended it. What is left is that copy ending once in each other
// enclave, for whoever holds its code or link, within its lifetime: it takes
// someone who writes to the table, and gives them nothing their own reading
// of the person's mail would not.
//
// An enclave ends nothing that was made before it started (since). A restart
// forgets the marks; with this rule it also forgets every sign-in that was
// waiting, as it did when they lived in memory, so restarting an enclave gives
// nobody more tries. A deployment ends the sign-ins in progress: the person
// asks again.

// pendingPurpose names the key that seals sign-ins in progress.
const pendingPurpose = "permaily sign-ins in progress v1"

// The kinds of sign-ins in progress, each in partitions of its own.
const (
	kindAttempt     = "attempt"
	kindCeremony    = "ceremony"
	kindTrip        = "trip"
	kindUnconfirmed = "unconfirmed"
)

// pendingKey is where a sign-in in progress is in its partition, and usedKey
// the mark that it was used.
const (
	pendingKey = "pending"
	usedKey    = "used"
)

// pendingPartition is where a sign-in in progress is kept: its kind, and its
// identifier or the hash of the secret that names it.
func pendingPartition(kind, name string) string { return "pending#" + kind + "#" + name }

// secretName is what names an entry found by a secret the browser holds (a
// trip's state, a cookie): its hash. The secret is 32 random bytes, so the
// hash cannot be worked back.
func secretName(kind, secret string) string {
	sum := sha256.Sum256([]byte("permaily " + kind + " v1\x00" + secret))
	return hex.EncodeToString(sum[:])
}

// pendingTimes are when a sign-in in progress was made and when it ends, as
// sealed with it.
type pendingTimes struct {
	Made    time.Time `json:"made"`
	Expires time.Time `json:"expires"`
}

func (p *pendingTimes) times() *pendingTimes { return p }

type pendingDoc interface{ times() *pendingTimes }

// maxMarks bounds what one enclave remembers of the sign-ins it was asked to
// end. When it is full of marks that have not ended, a sign-in that has none
// is refused, as a wrong answer: forgetting one would give its tries back.
const maxMarks = 100000

// mark is what this enclave remembers of a sign-in in progress that it was
// asked to end: the wrong answers it took here, and whether it ended here.
type mark struct {
	tries int
	used  bool
	ends  time.Time
}

func (s *Service) pendingKey(ctx context.Context) ([]byte, error) {
	key, err := s.cfg.Keys.Derive(ctx, pendingPurpose)
	if err != nil {
		return nil, fmt.Errorf("accounts: the key of sign-ins in progress: %w", err)
	}
	return key, nil
}

// stash keeps a sign-in in progress, sealed, as a new entry.
func (s *Service) stash(ctx context.Context, partition string, doc pendingDoc) error {
	key, err := s.pendingKey(ctx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	e := &store.Entry{Partition: partition, Key: pendingKey, ExpiresAt: doc.times().Expires, UpdatedAt: s.now()}
	if e.Value, err = envelope.SealText(key, raw, entryAAD(partition, pendingKey)); err != nil {
		return err
	}
	return s.cfg.Entries.PutEntry(ctx, e)
}

// unstash reads a sign-in in progress into doc, and returns its entry: nil
// when there is none this enclave may end. That is one that is not there, was
// not sealed by this deployment for where it is, has ended, was made before
// this enclave started, or was used.
func (s *Service) unstash(ctx context.Context, partition string, doc pendingDoc) (*store.Entry, error) {
	e, err := s.cfg.Entries.GetEntry(ctx, partition, pendingKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	key, err := s.pendingKey(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := envelope.OpenText(key, e.Value, entryAAD(partition, pendingKey))
	if err != nil || json.Unmarshal(raw, doc) != nil {
		return nil, nil
	}
	t := doc.times()
	if t.Made.Before(s.since) || !t.Expires.After(s.now()) {
		return nil, nil
	}
	switch _, err := s.cfg.Entries.GetEntry(ctx, partition, usedKey); {
	case err == nil:
		return nil, nil // used, and put back
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}
	return e, nil
}

// marked returns the mark of a sign-in in progress, made when it has none,
// with mu held: nil when the memory is full.
func (s *Service) marked(partition string, ends, now time.Time) *mark {
	if m := s.marks[partition]; m != nil {
		return m
	}
	if len(s.marks) >= maxMarks {
		s.sweepMarks(now)
		if len(s.marks) >= maxMarks {
			s.cfg.Log.Warn("accounts: too many sign-ins in progress remembered, one was refused")
			return nil
		}
	}
	m := &mark{ends: ends}
	s.marks[partition] = m
	return m
}

func (s *Service) sweepMarks(now time.Time) {
	for partition, m := range s.marks {
		if !m.ends.After(now) {
			delete(s.marks, partition)
		}
	}
}

// claim ends a sign-in in progress that was read: it is used here from now,
// marked used in the storage, and deleted as it was read. It reports false
// when it was used here already, or when another enclave ended it first.
func (s *Service) claim(ctx context.Context, e *store.Entry, ends time.Time) (bool, error) {
	s.mu.Lock()
	m := s.marked(e.Partition, ends, s.now())
	if m == nil || m.used {
		s.mu.Unlock()
		return false, nil
	}
	m.used = true
	s.mu.Unlock()
	return s.use(ctx, e, ends)
}

// use marks a sign-in in progress used in the storage, where no mark is, and
// deletes it as it was read. It reports whether this was the use that counts.
func (s *Service) use(ctx context.Context, e *store.Entry, ends time.Time) (bool, error) {
	// The mark holds nothing: it says only that the entry next to it was used.
	used := &store.Entry{Partition: e.Partition, Key: usedKey, Value: []byte("{}"), ExpiresAt: ends, UpdatedAt: s.now()}
	switch err := s.cfg.Entries.PutEntry(ctx, used); {
	case errors.Is(err, store.ErrExists):
		return false, nil // another enclave used it first
	case err != nil:
		return false, err
	}
	// What it held goes from the table now; the mark stays until it ends.
	if _, err := s.drop(ctx, e); err != nil {
		s.cfg.Log.Warn("accounts: a sign-in that was used could not be deleted", "err", scrub(err))
	}
	return true, nil
}

// drop deletes a sign-in in progress as it was read, and reports whether this
// was what deleted it.
func (s *Service) drop(ctx context.Context, e *store.Entry) (bool, error) {
	switch err := s.cfg.Entries.DeleteEntryAt(ctx, e); {
	case errors.Is(err, store.ErrConflict):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}
