// Package inboxtest writes messages in the stored format of ADR 0026, for the
// tests of what reads them (pull request 2) while delivery (pull request 1)
// is built at the same time. Before pull request 2 merges, its tests also run
// on messages that pull request 1's delivery wrote: this is a stand-in for
// it, not a second definition of the format. Never imported outside tests
// and dev mode (depguard).
package inboxtest

import (
	"context"
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
}

// Partition is where a mailbox's messages and entries are.
func Partition(mailboxID string) string { return "mailbox#" + mailboxID }

// Seed stores m in the box as delivery does: the raw message and the meta
// sealed under the box's key, the message item, its search entry and, when
// it has one, its confirmation entry.
func Seed(ctx context.Context, st store.Store, entries store.Entries, b inbox.Box, m Message) error {
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
	expires := m.ReceivedAt.AddDate(0, 0, m.RetentionDays)
	if err := st.PutMessage(ctx, &store.Message{
		MailboxID: b.MailboxID, ID: m.ID, ReceivedAt: m.ReceivedAt, ExpiresAt: expires,
		Size: len(m.Raw), RetentionDays: m.RetentionDays, Meta: sealedMeta,
	}, sealedRaw); err != nil {
		return err
	}
	search := m.Search
	if search == nil {
		search = &inbox.SearchEntry{From: m.Meta.From, Subject: m.Meta.Subject, ReceivedAt: m.ReceivedAt}
	}
	if err := put(ctx, entries, b, inbox.KeySearch, inbox.PurposeSearch, m.ID, search, expires); err != nil {
		return err
	}
	if m.Confirm != nil {
		return put(ctx, entries, b, inbox.KeyConfirm, inbox.PurposeConfirm, m.ID, m.Confirm, m.Confirm.Ends)
	}
	return nil
}

func put(ctx context.Context, entries store.Entries, b inbox.Box, prefix, purpose, id string, v any, expires time.Time) error {
	plain, err := json.Marshal(v)
	if err != nil {
		return err
	}
	sealed, err := envelope.SealText(b.Key, plain, envelope.AAD(purpose, b.MailboxID, id))
	if err != nil {
		return err
	}
	return entries.PutEntry(ctx, &store.Entry{Partition: Partition(b.MailboxID), Key: prefix + id, Value: sealed, ExpiresAt: expires})
}
