package inboxtest

import (
	"context"
	"encoding/json"
	"strings"
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
	seen, err := mem.ListEntries(ctx, Partition(b.MailboxID), inbox.KeySeen, "", 10)
	if err != nil || len(seen) != 1 {
		t.Fatalf("seen# entries %v %v", seen, err)
	}
	plainSeen, err := envelope.OpenText(b.Key, seen[0].Value, envelope.AAD(inbox.PurposeSeen, b.MailboxID, seen[0].Key))
	var mark inbox.SeenEntry
	if err != nil || json.Unmarshal(plainSeen, &mark) != nil || !mark.Stored || mark.Message != m.ID || !mark.ExpiresAt.Equal(at.AddDate(0, 0, 30)) {
		t.Fatalf("the mark %+v %v", mark, err)
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

// Seed writes what delivery adds for ADR 0027 (the meta's format and size)
// unless the message is Old; FromRaw is what delivery stores for a raw
// message, its list and its cut text included.
func TestSeedWritesWhatDeliveryAdds(t *testing.T) {
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Second)
	b := inbox.Box{MailboxID: "mbx_a", Key: make([]byte, envelope.KeySize)}
	meta := func(mem *store.Memory, id string) inbox.StoredMeta {
		rec, err := mem.Item(ctx, b.MailboxID, id)
		if err != nil {
			t.Fatal(err)
		}
		var m inbox.StoredMeta
		plain, err := envelope.Open(b.Key, rec.Meta, envelope.AAD(inbox.PurposeMeta, b.MailboxID, id))
		if err != nil || json.Unmarshal(plain, &m) != nil {
			t.Fatal(err)
		}
		return m
	}
	mem := store.NewMemory()
	raw := []byte("List-Id: <News.Example.com>\r\nSubject: Hi\r\n\r\n" + strings.Repeat("a", inbox.SearchTextMax+1))
	for i, m := range []Message{
		{ID: "0000000000000001aaaaaaaaaaaaaaaa", ReceivedAt: at, RetentionDays: 30, Raw: raw},
		{ID: "0000000000000002aaaaaaaaaaaaaaaa", ReceivedAt: at, RetentionDays: 30, Raw: raw, Old: true},
		FromRaw(ctx, "0000000000000003aaaaaaaaaaaaaaaa", at, 30, raw),
	} {
		if err := Seed(ctx, mem, mem, b, m); err != nil {
			t.Fatal(err)
		}
		got := meta(mem, m.ID)
		wantFormat, wantSize := inbox.MetaFormat, int64(len(raw))
		if m.Old {
			wantFormat, wantSize = 0, 0
		}
		if got.Format != wantFormat || got.Size != wantSize || (i == 2) != (got.List == "news.example.com") {
			t.Errorf("message %d: %+v", i, got)
		}
	}
	if m := FromRaw(ctx, "0000000000000003aaaaaaaaaaaaaaaa", at, 30, raw); !m.Search.Cut || len(m.Search.Text) != inbox.SearchTextMax || !m.Search.ReceivedAt.Equal(at) {
		t.Errorf("the entry %+v", m.Search.Cut)
	}
}
