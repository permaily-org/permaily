package receipts

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/receiptchain"
)

// Keys derives the key that seals receipts from the root secret
// (keys.Root.Derive).
type Keys interface {
	Derive(ctx context.Context, purpose string) ([]byte, error)
}

// Service writes and lists an account's receipts: each a new entry of
// Partition(account), keyed by its ID, its value the document sealed under
// the key of Purpose and bound to its partition and key, written at version
// 0 and with no expiry. It logs nothing: a receipt's content, its identifier
// and how many an account has are the person's (H18).
type Service struct {
	entries store.Entries
	keys    Keys
	now     func() time.Time
	random  func([]byte) error
	// chain is nil for a Service made by New: it writes receipts unchained
	// and says so (Chained).
	chain *chainState
	// walkBytes is WalkBytesMax, which tests lower.
	walkBytes int
}

var _ Writer = (*Service)(nil)

// New is a Service over entries, sealing under keys, on the clock now, with
// random filling each identifier's random bytes (crypto/rand.Read's shape).
// A nil now is time.Now and a nil random is crypto/rand's.
func New(entries store.Entries, keys Keys, now func() time.Time, random func([]byte) error) *Service {
	if now == nil {
		now = time.Now
	}
	if random == nil {
		random = func(b []byte) error {
			_, err := rand.Read(b)
			return err
		}
	}
	return &Service{entries: entries, keys: keys, now: now, random: random, walkBytes: WalkBytesMax}
}

// IDPrefix starts every receipt's identifier and key.
const IDPrefix = receiptchain.IDPrefix

// idRandomBytes is how many random bytes follow the time in an identifier:
// two enclaves writing at the same nanosecond never collide (ADR 0031,
// "When a receipt is written").
const idRandomBytes = receiptchain.IDRandomBytes

// IDLength is the length of a receipt's identifier: the prefix, the time in
// nanoseconds as 16 hexadecimal digits, then the random bytes in hexadecimal.
// Hexadecimal keeps the order of the bytes, so the keys' order is the time's.
const IDLength = receiptchain.IDLength

// writeTries bounds how many identifiers Write tries when one is taken (two
// writers at the same nanosecond drawing the same random bytes, which a
// working random source never does).
const writeTries = 3

// ErrUnreadable is a stored receipt that does not open under this
// deployment's key for where it is, or whose document is not a receipt of
// that key: whoever writes the table moved, changed or made it.
var ErrUnreadable = errors.New("receipts: a stored receipt does not open")

// Write implements Writer: r, with V, ID and At set, is sealed and stored as
// a new entry before Write returns. A document past MaxDocumentBytes is
// ErrTooLarge and nothing is stored; an error of the storage, or ctx ending
// first, is returned and the caller gives nothing of its request. r's V, ID
// and At are set only when it is stored. A receipt that comes with its
// identity (NewID, and its At) is stored under it, once: see writeAs.
func (s *Service) Write(ctx context.Context, account string, r *Receipt) error {
	if account == "" || r == nil {
		return errors.New("receipts: a receipt needs an account")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	partition := Partition(account)
	if r.ID != "" {
		return s.writeAs(ctx, account, r)
	}
	var key []byte
	for range writeTries {
		at := s.now().UTC()
		id, err := s.newID(at)
		if err != nil {
			return err
		}
		doc := *r
		doc.V, doc.ID, doc.At = Version, id, at
		raw, err := json.Marshal(&doc)
		if err != nil {
			return fmt.Errorf("receipts: %w", err)
		}
		if len(raw) > MaxDocumentBytes {
			return ErrTooLarge
		}
		if key == nil {
			if key, err = s.keys.Derive(ctx, Purpose); err != nil {
				return fmt.Errorf("receipts: the key: %w", err)
			}
		}
		sealed, err := envelope.SealText(key, raw, aad(partition, id))
		if err != nil {
			return fmt.Errorf("receipts: %w", err)
		}
		e := &store.Entry{Partition: partition, Key: id, Value: sealed, UpdatedAt: at}
		switch err := s.entries.PutEntry(ctx, e); {
		case errors.Is(err, store.ErrExists):
			continue
		case err != nil:
			return fmt.Errorf("receipts: write: %w", err)
		}
		if err := s.linkStored(ctx, account, id, raw); err != nil {
			return err
		}
		r.V, r.ID, r.At = Version, id, at
		return nil
	}
	return errors.New("receipts: no identifier was free")
}

// writeAs stores r under the identity it comes with, so a write whose
// outcome was not known (the storage's answer lost, ctx ending while it was
// under way) is made again as the same receipt and stored once (ADR 0031,
// "Refusals past a window's bound"): when the entry is already there, it is
// this receipt if it opens to the very document, and Write succeeds;
// anything else there is ErrIdentityTaken, and nothing is stored.
func (s *Service) writeAs(ctx context.Context, account string, r *Receipt) error {
	partition := Partition(account)
	if !ValidID(r.ID) || r.At.IsZero() {
		return errors.New("receipts: a receipt's own identity needs its identifier and its time")
	}
	doc := *r
	doc.V, doc.At = Version, r.At.UTC()
	raw, err := json.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("receipts: %w", err)
	}
	if len(raw) > MaxDocumentBytes {
		return ErrTooLarge
	}
	key, err := s.keys.Derive(ctx, Purpose)
	if err != nil {
		return fmt.Errorf("receipts: the key: %w", err)
	}
	sealed, err := envelope.SealText(key, raw, aad(partition, doc.ID))
	if err != nil {
		return fmt.Errorf("receipts: %w", err)
	}
	e := &store.Entry{Partition: partition, Key: doc.ID, Value: sealed, UpdatedAt: doc.At}
	switch err := s.entries.PutEntry(ctx, e); {
	case errors.Is(err, store.ErrExists):
		got, gerr := s.entries.GetEntry(ctx, partition, doc.ID)
		if gerr != nil {
			return fmt.Errorf("receipts: write: %w", gerr)
		}
		stored, oerr := envelope.OpenText(key, got.Value, aad(partition, doc.ID))
		if oerr != nil || !bytes.Equal(stored, raw) {
			return ErrIdentityTaken
		}
	case err != nil:
		return fmt.Errorf("receipts: write: %w", err)
	}
	// Linked whether this call stored it or an earlier one did: a write made
	// again because its outcome was not known may have stopped before its
	// link, and a receipt may be named twice, never left out (ADR 0037).
	if err := s.linkStored(ctx, account, doc.ID, raw); err != nil {
		return err
	}
	r.V, r.At = doc.V, doc.At
	return nil
}

// linkStored links a receipt Write stored, on a chained Service.
func (s *Service) linkStored(ctx context.Context, account, id string, raw []byte) error {
	if s.chain == nil {
		return nil
	}
	if _, err := s.Link(ctx, account, Ref{ID: id, Digest: DigestOf(raw)}); err != nil {
		return fmt.Errorf("receipts: link: %w", err)
	}
	return nil
}

// ErrIdentityTaken is a receipt written with its own identity where another
// document is stored: whoever writes the table put it there, or the
// identity was drawn twice.
var ErrIdentityTaken = errors.New("receipts: another receipt has this identifier")

// NewID draws a receipt's identity for at: a write that takes it (Write with
// r.ID and r.At set) can be made again and is stored once. Its random bytes
// are crypto/rand's, so two enclaves, or two receipts of one, never draw
// the same.
func NewID(at time.Time) (string, error) {
	return (&Service{random: func(b []byte) error {
		_, err := rand.Read(b)
		return err
	}}).newID(at.UTC())
}

// List returns up to limit of account's receipts whose identifiers come
// after after ("" for the first), oldest first, in their keys' order. A
// receipt that does not open fails the whole listing with ErrUnreadable:
// a forged or moved entry is never shown as the person's record, nor
// silently left out of it.
func (s *Service) List(ctx context.Context, account, after string, limit int) ([]Receipt, error) {
	if account == "" || limit <= 0 || (after != "" && !ValidID(after)) {
		return nil, errors.New("receipts: a listing needs an account, a limit and a receipt to start after")
	}
	partition := Partition(account)
	es, err := s.entries.ListEntries(ctx, partition, IDPrefix, after, limit)
	if err != nil {
		return nil, fmt.Errorf("receipts: list: %w", err)
	}
	if len(es) == 0 {
		return nil, nil
	}
	key, err := s.keys.Derive(ctx, Purpose)
	if err != nil {
		return nil, fmt.Errorf("receipts: the key: %w", err)
	}
	out := make([]Receipt, 0, len(es))
	for i := range es {
		r, err := open(key, &es[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, nil
}

// open reads one stored receipt: it opens only under key, bound to the
// entry's partition and key, and names that key as its ID.
func open(key []byte, e *store.Entry) (*Receipt, error) {
	_, r, err := openRaw(key, e)
	return r, err
}

// openRaw is open with the document's bytes as stored, which a link's
// digest is of.
func openRaw(key []byte, e *store.Entry) ([]byte, *Receipt, error) {
	raw, err := envelope.OpenText(key, e.Value, aad(e.Partition, e.Key))
	if err != nil {
		return nil, nil, ErrUnreadable
	}
	var r Receipt
	if json.Unmarshal(raw, &r) != nil || r.V < 1 || r.ID != e.Key {
		return nil, nil, ErrUnreadable
	}
	return raw, &r, nil
}

// newID is IDPrefix, at in nanoseconds since 1970 as 16 hexadecimal digits,
// and idRandomBytes random bytes in hexadecimal.
func (s *Service) newID(at time.Time) (string, error) {
	b := make([]byte, idRandomBytes)
	if err := s.random(b); err != nil {
		return "", fmt.Errorf("receipts: random: %w", err)
	}
	return IDPrefix + fmt.Sprintf("%016x", uint64(at.UnixNano())) + hex.EncodeToString(b), nil //nolint:gosec // receipts are written after 1970
}

// ValidID says whether id has a receipt identifier's shape.
func ValidID(id string) bool { return receiptchain.ValidID(id) }

// aad binds a sealed receipt to its partition and key.
func aad(partition, key string) []byte { return envelope.AAD(Purpose, partition, key) }
