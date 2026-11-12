package inboxtest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/store"
)

// What Seed writes opens under the box's key, bound to the box and the
// message, as ADR 0026's format says: the reader of pull request 2 reads it.
func TestSeedWritesTheFormat(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	at := time.Now().UTC().Truncate(time.Second)
	b := inbox.Box{MailboxID: "mbx_a", Key: make([]byte, envelope.KeySize)}
	m := Message{
		ID: "0000000000000001aaaaaaaaaaaaaaaa", ReceivedAt: at, RetentionDays: 30, Raw: []byte("Subject: Hi\r\n\r\nHello\r\n"),
		Meta:    inbox.StoredMeta{Subject: "Hi", From: []contracts.MailAddress{{Address: "a@example.org"}}},
		Confirm: &inbox.ConfirmEntry{Code: "123456789", ReceivedAt: at, Ends: at.Add(time.Hour)},
	}
	if err := Seed(ctx, mem, mem, b, m); err != nil {
		t.Fatal(err)
	}
	rec, raw, err := mem.GetMessage(ctx, b.MailboxID, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := envelope.Open(b.Key, raw, envelope.AAD(inbox.PurposeRaw, b.MailboxID, m.ID)); err != nil || string(plain) != string(m.Raw) {
		t.Fatalf("raw: %q %v", plain, err)
	}
	var meta inbox.StoredMeta
	plain, err := envelope.Open(b.Key, rec.Meta, envelope.AAD(inbox.PurposeMeta, b.MailboxID, m.ID))
	if err != nil || json.Unmarshal(plain, &meta) != nil || meta.Subject != "Hi" {
		t.Fatalf("meta: %+v %v", meta, err)
	}
	for _, c := range []struct{ key, purpose string }{{inbox.KeySearch, inbox.PurposeSearch}, {inbox.KeyConfirm, inbox.PurposeConfirm}} {
		e, err := mem.GetEntry(ctx, Partition(b.MailboxID), c.key+m.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := envelope.OpenText(b.Key, e.Value, envelope.AAD(c.purpose, b.MailboxID, m.ID)); err != nil {
			t.Errorf("%s: %v", c.key, err)
		}
		if _, err := envelope.OpenText(b.Key, e.Value, envelope.AAD(c.purpose, "mbx_other", m.ID)); err == nil {
			t.Errorf("%s opens as another mailbox's", c.key)
		}
	}
}
