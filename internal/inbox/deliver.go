package inbox

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

// A delivery into one mailbox (ADR 0026, "The claim, and the order of
// writes"): the message is claimed first, by a seen# entry named by an HMAC
// of the envelope sender and the SHA-256 of the raw message, which fixes its
// identifier, when it was received and when it expires; then its parts are
// written so that it becomes visible last (the raw object, the search entry
// and any confirmation entry, the message item); then the claim is marked
// stored. Every later try, through any enclave, writes the same keys with the
// same fixed fields, so a crash never loses a message and never stores it
// twice while its claim lives.

// ErrClaimed is a message another delivery is storing in the mailbox now: its
// claim is younger than the writer's ClaimStale, or the claim was contended
// twice. The sender's retry finds it stored, or takes the claim over.
var ErrClaimed = errors.New("inbox: another delivery of the message is storing it")

// Partition is where a mailbox's messages and entries are.
func Partition(mailboxID string) string { return "mailbox#" + mailboxID }

// NewMessageID returns a message identifier: 32 hexadecimal characters, the
// time first, so an identifier says when its message arrived and sorts by it.
func NewMessageID(t time.Time) string {
	r := make([]byte, 8)
	_, _ = rand.Read(r)                                                       // crypto/rand does not fail
	return fmt.Sprintf("%016x", uint64(t.UnixNano())) + hex.EncodeToString(r) //nolint:gosec // time is after 1970
}

// seenPurpose derives, from a mailbox's key, the key of its seen# names.
const seenPurpose = "permaily seen v1"

// SeenKey is the name of a message's seen# entry in a mailbox, after KeySeen:
// the hex HMAC, under a key derived from the mailbox's, of the envelope
// sender and the SHA-256 of the raw message. It says nothing of either to
// whoever reads the table.
func SeenKey(mailboxKey []byte, from string, raw []byte) (string, error) {
	k, err := hkdf.Key(sha256.New, mailboxKey, nil, seenPurpose, sha256.Size)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	mac := hmac.New(sha256.New, k)
	mac.Write([]byte(from))
	mac.Write([]byte{0})
	mac.Write(sum[:])
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// Writer writes deliveries into mailboxes.
type Writer struct {
	Entries store.Entries
	Parts   store.MessageParts
	Now     func() time.Time
	// SeenFor is how long a claim's entry lives from the first receipt, and
	// ClaimStale how long a claim stands before another delivery takes it
	// over (mailboxes.SeenFor, mailboxes.ClaimStale).
	SeenFor    time.Duration
	ClaimStale time.Duration
}

// Claim is a delivery's claim on a message in one mailbox.
type Claim struct {
	Seen  SeenEntry
	key   string
	entry *store.Entry
}

// Stored reports whether the message is stored in the mailbox already.
func (c *Claim) Stored() bool { return c.Seen.Stored }

func (w *Writer) now() time.Time { return w.Now().UTC() }

// Claim claims the message named seen in the box, or finds it stored: a
// claim made now fixes a new identifier, now as when it was received, the
// retention given and the expiry they make; a claim older than ClaimStale is
// taken over at the version read, keeping what it fixed. ErrClaimed when a
// younger claim stands, or when the entry was contended twice.
func (w *Writer) Claim(ctx context.Context, b Box, seen string, retentionDays int) (*Claim, error) {
	key := KeySeen + seen
	for range 2 {
		e, err := w.Entries.GetEntry(ctx, Partition(b.MailboxID), key)
		if errors.Is(err, store.ErrNotFound) {
			now := w.now()
			c := &Claim{key: key, Seen: SeenEntry{
				Message: NewMessageID(now), ReceivedAt: now, ExpiresAt: now.AddDate(0, 0, retentionDays),
				RetentionDays: retentionDays, ClaimedAt: now,
			}}
			if err := w.putSeen(ctx, b, c, 0); err == nil {
				return c, nil
			} else if !errors.Is(err, store.ErrExists) && !errors.Is(err, store.ErrConflict) {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		c := &Claim{key: key, entry: e}
		if err := openJSON(b, e.Value, PurposeSeen, key, &c.Seen, envelope.OpenText); err != nil {
			return nil, err
		}
		if c.Seen.Stored {
			return c, nil
		}
		if age := w.now().Sub(c.Seen.ClaimedAt); age < w.ClaimStale {
			return nil, ErrClaimed
		}
		c.Seen.ClaimedAt = w.now()
		if err := w.putSeen(ctx, b, c, e.Version); err == nil {
			return c, nil
		} else if !errors.Is(err, store.ErrExists) && !errors.Is(err, store.ErrConflict) {
			return nil, err
		}
	}
	return nil, ErrClaimed
}

func (w *Writer) putSeen(ctx context.Context, b Box, c *Claim, version uint64) error {
	sealed, err := sealJSON(b, c.Seen, PurposeSeen, c.key)
	if err != nil {
		return err
	}
	e := &store.Entry{Partition: Partition(b.MailboxID), Key: c.key, Value: sealed, Version: version, ExpiresAt: c.Seen.ReceivedAt.Add(w.SeenFor)}
	if err := w.Entries.PutEntry(ctx, e); err != nil {
		return err
	}
	c.entry = e
	return nil
}

// Store writes the message's parts under what the claim fixed, in the order
// that makes it visible last: the raw object, the search entry and, for a
// confirmation, its entry, then the message item. What an earlier try wrote
// is there already and counts as written.
func (w *Writer) Store(ctx context.Context, b Box, c *Claim, p *Parsed, raw []byte, confirm *ConfirmEntry) error {
	id := c.Seen.Message
	// The metadata as written, within MaxMetaBytes whatever delivery added
	// to it after parsing (the envelope sender, the signatures' results).
	bounded := p.Meta
	bounded.Attachments = slices.Clone(bounded.Attachments)
	bounded.Auth = slices.Clone(bounded.Auth)
	boundMeta(&bounded)
	meta, err := json.Marshal(bounded)
	if err != nil {
		return err
	}
	sealedMeta, err := envelope.Seal(b.Key, meta, envelope.AAD(PurposeMeta, b.MailboxID, id))
	if err != nil {
		return err
	}
	sealedRaw, err := envelope.Seal(b.Key, raw, envelope.AAD(PurposeRaw, b.MailboxID, id))
	if err != nil {
		return err
	}
	m := &store.Message{
		MailboxID: b.MailboxID, ID: id, ReceivedAt: c.Seen.ReceivedAt, ExpiresAt: c.Seen.ExpiresAt,
		Size: len(raw), RetentionDays: c.Seen.RetentionDays, Meta: sealedMeta,
	}
	if err := w.Parts.PutRaw(ctx, m, sealedRaw); err != nil && !errors.Is(err, store.ErrExists) {
		return err
	}
	search := SearchEntry{From: p.Meta.From, Subject: p.Meta.Subject, Text: p.Text, ReceivedAt: c.Seen.ReceivedAt}
	if err := w.putOnce(ctx, b, KeySearch+id, PurposeSearch, id, search, c.Seen.ExpiresAt); err != nil {
		return err
	}
	if confirm != nil {
		if err := w.putOnce(ctx, b, KeyConfirm+id, PurposeConfirm, id, confirm, confirm.Ends); err != nil {
			return err
		}
	}
	return w.Parts.PutItem(ctx, m)
}

// putOnce writes a sealed entry at version 0: one there already is an
// earlier try's of the same delivery.
func (w *Writer) putOnce(ctx context.Context, b Box, key, purpose, bound string, v any, expires time.Time) error {
	sealed, err := sealJSON(b, v, purpose, bound)
	if err != nil {
		return err
	}
	err = w.Entries.PutEntry(ctx, &store.Entry{Partition: Partition(b.MailboxID), Key: key, Value: sealed, ExpiresAt: expires})
	if err != nil && !errors.Is(err, store.ErrExists) {
		return err
	}
	return nil
}

// errNotMarked is a mark that could not be written: the entry holds neither
// this message's mark nor its claim. The delivery fails, and the sender's
// retry claims again.
var errNotMarked = errors.New("inbox: the claim of a message changed before its mark")

// Mark marks the claim stored, at the version of the claim; Store wrote every
// part of it before. When another delivery took the claim over meanwhile, the
// entry is read again and opened: it is this message's mark already, which
// is success; or this message's claim, which the mark is written over at the
// version read (the parts are the same keys under the same fixed values);
// anything else is an error, never success.
func (w *Writer) Mark(ctx context.Context, b Box, c *Claim) error {
	c.Seen.Stored, c.Seen.StoredAt = true, w.now()
	version := c.entry.Version
	for range 2 {
		err := w.putSeen(ctx, b, c, version)
		if !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrExists) {
			return err
		}
		e, err := w.Entries.GetEntry(ctx, Partition(b.MailboxID), c.key)
		if errors.Is(err, store.ErrNotFound) {
			return errNotMarked
		}
		if err != nil {
			return err
		}
		var now SeenEntry
		if err := openJSON(b, e.Value, PurposeSeen, c.key, &now, envelope.OpenText); err != nil || now.Message != c.Seen.Message {
			return errNotMarked
		}
		if now.Stored {
			return nil
		}
		version = e.Version
	}
	return errNotMarked
}

func sealJSON(b Box, v any, purpose, bound string) ([]byte, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return envelope.SealText(b.Key, plain, envelope.AAD(purpose, b.MailboxID, bound))
}

func openJSON(b Box, value []byte, purpose, bound string, v any, open func(key, value, aad []byte) ([]byte, error)) error {
	plain, err := open(b.Key, value, envelope.AAD(purpose, b.MailboxID, bound))
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, v)
}

// OpenConfirm opens a confirmation entry of the box: nil when it does not
// open under the box's key or has ended at now (its sealed Ends, never the
// entry's own expiry).
func OpenConfirm(b Box, e *store.Entry, now time.Time) *ConfirmEntry {
	var c ConfirmEntry
	if len(e.Key) <= len(KeyConfirm) || openJSON(b, e.Value, PurposeConfirm, e.Key[len(KeyConfirm):], &c, envelope.OpenText) != nil || !now.Before(c.Ends) {
		return nil
	}
	return &c
}
