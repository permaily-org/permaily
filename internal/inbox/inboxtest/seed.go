// Package inboxtest writes messages in the stored format of ADR 0026, for the
// tests of what reads them (pull request 2) while delivery (pull request 1)
// is built at the same time. Before pull request 2 merges, its tests also run
// on messages that pull request 1's delivery wrote: this is a stand-in for
// it, not a second definition of the format. Never imported outside tests
// and dev mode (depguard).
package inboxtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/store"
)

// Message is a message to seed. Search may be nil: one is made from Meta,
// with no text.
type Message struct {
	ID            string
	ReceivedAt    time.Time
	RetentionDays int
	Raw           []byte
	Meta          inbox.StoredMeta
	Search        *inbox.SearchEntry
	Confirm       *inbox.ConfirmEntry
	// Seen is the key of the message's seen# entry after inbox.KeySeen.
	// Delivery derives it from the mailbox's key, the envelope sender and
	// the raw message (pull request 1); empty here means the SHA-256 of the
	// message's identifier, which no reader looks into.
	Seen string
}

// Partition is where a mailbox's messages and entries are.
func Partition(mailboxID string) string { return "mailbox#" + mailboxID }

// Seed stores m in the box as delivery does, in its order, so that a
// listing never shows a message whose search entry is not there yet: the
// search entry and, when it has one, the confirmation entry; then the raw
// message and the meta sealed under the box's key and the message item (one
// store.Store.PutMessage, raw first, until pull request 0's MessageParts);
// then the seen# entry, marked stored, with what the claim fixed.
func Seed(ctx context.Context, st store.Store, entries store.Entries, b inbox.Box, m Message) error {
	expires := m.ReceivedAt.AddDate(0, 0, m.RetentionDays)
	search := m.Search
	if search == nil {
		search = &inbox.SearchEntry{From: m.Meta.From, Subject: m.Meta.Subject, ReceivedAt: m.ReceivedAt}
	}
	if err := put(ctx, entries, b, inbox.KeySearch+m.ID, inbox.PurposeSearch, m.ID, search, expires); err != nil {
		return err
	}
	if m.Confirm != nil {
		if err := put(ctx, entries, b, inbox.KeyConfirm+m.ID, inbox.PurposeConfirm, m.ID, m.Confirm, m.Confirm.Ends); err != nil {
			return err
		}
	}
	meta, err := json.Marshal(m.Meta)
	if err != nil {
		return err
	}
	sealedMeta, err := envelope.Seal(b.Key, meta, envelope.AAD(inbox.PurposeMeta, b.MailboxID, m.ID))
	if err != nil {
		return err
	}
	sealedRaw, err := envelope.Seal(b.Key, m.Raw, envelope.AAD(inbox.PurposeRaw, b.MailboxID, m.ID))
	if err != nil {
		return err
	}
	if err := st.PutMessage(ctx, &store.Message{
		MailboxID: b.MailboxID, ID: m.ID, ReceivedAt: m.ReceivedAt, ExpiresAt: expires,
		Size: len(m.Raw), RetentionDays: m.RetentionDays, Meta: sealedMeta,
	}, sealedRaw); err != nil {
		return err
	}
	seen := m.Seen
	if seen == "" {
		sum := sha256.Sum256([]byte(m.ID))
		seen = hex.EncodeToString(sum[:])
	}
	mark := inbox.SeenEntry{
		Stored: true, Message: m.ID, ReceivedAt: m.ReceivedAt, ExpiresAt: expires, RetentionDays: m.RetentionDays,
		ClaimedAt: m.ReceivedAt, StoredAt: m.ReceivedAt,
	}
	// The seen# entry is bound to the mailbox and its own key, not to a
	// message (inbox.PurposeSeen).
	return put(ctx, entries, b, inbox.KeySeen+seen, inbox.PurposeSeen, inbox.KeySeen+seen, mark, m.ReceivedAt.Add(seenFor))
}

// seenFor is mailboxes.SeenFor, which inboxtest does not import: the fake of
// a format does not depend on the service that writes it.
const seenFor = 7 * 24 * time.Hour

func put(ctx context.Context, entries store.Entries, b inbox.Box, key, purpose, bound string, v any, expires time.Time) error {
	plain, err := json.Marshal(v)
	if err != nil {
		return err
	}
	sealed, err := envelope.SealText(b.Key, plain, envelope.AAD(purpose, b.MailboxID, bound))
	if err != nil {
		return err
	}
	return entries.PutEntry(ctx, &store.Entry{Partition: Partition(b.MailboxID), Key: key, Value: sealed, ExpiresAt: expires})
}
