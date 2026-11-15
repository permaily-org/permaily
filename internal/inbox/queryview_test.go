package inbox_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/store"
)

// Q32 (ADR 0027): delivery records the first List-Id's identifier, the size
// of the message as delivered and the meta's format.
func TestQ32DeliveryRecordsTheListTheSizeAndTheFormat(t *testing.T) {
	ctx := context.Background()
	raw := "From: a@example.com\r\nList-Id: Weekly News <News.Example.COM>\r\nList-Id: <second.example.com>\r\nSubject: Hi\r\n\r\nHello\r\n"
	p := inbox.Parse(ctx, []byte(raw))
	if p.Meta.List != "news.example.com" || p.Meta.Size != int64(len(raw)) || p.Meta.Format != inbox.MetaFormat || inbox.MetaFormat != 2 {
		t.Fatalf("list %q size %d format %d", p.Meta.List, p.Meta.Size, p.Meta.Format)
	}
	for header, want := range map[string]string{
		"":                                    "",
		"List-Id: bare.example.org\r\n":       "bare.example.org",
		"List-Id: Two Words\r\n":              "",
		"List-Id: Name <unclosed.example\r\n": "",
		"List-Id: <  Spaced.Example  >\r\n":   "spaced.example",
		"List-Id: <>\r\n":                     "",
	} {
		raw := "From: a@example.com\r\n" + header + "Subject: Hi\r\n\r\nHello\r\n"
		if got := inbox.Parse(ctx, []byte(raw)).Meta.List; got != want {
			t.Errorf("%q: list %q, want %q", header, got, want)
		}
	}
	// A message that cannot be read as mail still records its size.
	bad := "this is not mail at all\r\nno header here\r\n\r\n"
	if m := inbox.Parse(ctx, []byte(bad)).Meta; !m.Unparsed || m.Size != int64(len(bad)) || m.Format != inbox.MetaFormat {
		t.Fatalf("unparsed %+v", m)
	}
	// The fields are sealed in the meta delivery writes.
	mem := store.NewMemory()
	c := &clock{t: time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)}
	mem.Now = c.now
	w := writer(mem, c)
	claim, err := w.Claim(ctx, box, "seen-list", 30)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Store(ctx, box, claim, &p, []byte(raw), nil); err != nil {
		t.Fatal(err)
	}
	item, err := mem.Item(ctx, box.MailboxID, claim.Seen.Message)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := envelope.Open(box.Key, item.Meta, envelope.AAD(inbox.PurposeMeta, box.MailboxID, item.ID))
	var meta inbox.StoredMeta
	if err != nil || json.Unmarshal(plain, &meta) != nil || meta.List != "news.example.com" || meta.Size != int64(len(raw)) || meta.Format != 2 {
		t.Fatalf("stored meta %+v %v", meta, err)
	}
}

// Q32: a text longer than the 16 KiB a search entry keeps sets its Cut, as
// delivery writes it; a shorter one does not.
func TestQ32ALongTextIsMarkedCut(t *testing.T) {
	ctx := context.Background()
	for _, n := range []int{100, inbox.SearchTextMax, inbox.SearchTextMax + 1, 40 << 10} {
		raw := "From: a@example.com\r\nSubject: Hi\r\nContent-Type: text/plain\r\n\r\n" + strings.Repeat("a", n)
		p := inbox.Parse(ctx, []byte(raw))
		mem := store.NewMemory()
		c := &clock{t: time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)}
		mem.Now = c.now
		w := writer(mem, c)
		claim, err := w.Claim(ctx, box, "seen-cut", 30)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Store(ctx, box, claim, &p, []byte(raw), nil); err != nil {
			t.Fatal(err)
		}
		e, err := mem.GetEntry(ctx, inbox.Partition(box.MailboxID), inbox.KeySearch+claim.Seen.Message)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := envelope.OpenText(box.Key, e.Value, envelope.AAD(inbox.PurposeSearch, box.MailboxID, claim.Seen.Message))
		var entry inbox.SearchEntry
		if err != nil || json.Unmarshal(plain, &entry) != nil {
			t.Fatal(err)
		}
		if want := n > inbox.SearchTextMax; entry.Cut != want || p.TextCut != want || len(entry.Text) != min(n, inbox.SearchTextMax) {
			t.Errorf("%d bytes: cut %v (%v), text %d", n, entry.Cut, p.TextCut, len(entry.Text))
		}
	}
}

// Q32: a meta written before ADR 0027 (no format) reads list and size as
// unknown; an entry written before (no Cut) whose text ends where a cut text
// ends reads as cut.
func TestQ32WhatWasStoredBeforeReadsAsUnknownOrCut(t *testing.T) {
	item := &store.Message{MailboxID: box.MailboxID, ID: id(time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC), 1), Size: 1 << 20}
	old := &inbox.StoredMeta{List: "x.example", Size: 10}
	m := inbox.QueryMessage(item, old, nil, nil)
	if m.ListRecorded || m.List != nil || m.SizeKnown || m.Size != 0 {
		t.Fatalf("format 1: %+v", m)
	}
	for n, want := range map[int]bool{0: false, inbox.SearchTextMax - 5: false, inbox.SearchTextMax - 4: true, inbox.SearchTextMax - 1: true, inbox.SearchTextMax: true} {
		m := inbox.QueryMessage(item, old, &inbox.SearchEntry{Text: strings.Repeat("a", n)}, nil)
		if m.BodyCut != want || len(m.Body) != n {
			t.Errorf("an entry of %d bytes without Cut: cut %v", n, m.BodyCut)
		}
	}
	if m := inbox.QueryMessage(item, old, &inbox.SearchEntry{Text: "short", Cut: true}, nil); !m.BodyCut {
		t.Error("an entry that says it was cut")
	}
	// An entry longer than it may be (none is written so) gives only what
	// an entry holds.
	if m := inbox.QueryMessage(item, old, &inbox.SearchEntry{Text: strings.Repeat("a", inbox.SearchTextMax+9)}, nil); len(m.Body) != inbox.SearchTextMax || !m.BodyCut {
		t.Errorf("an overlong entry: %d %v", len(m.Body), m.BodyCut)
	}
}

// QueryMessage gives evaluation exactly what was read, each source marked:
// the time from the identifier and never the item's plain fields, the
// signatures that passed, and the size from the sealed meta or, for a meta
// of format 1, from the raw message read.
func TestQueryMessageGivesWhatWasRead(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 8, time.UTC)
	item := &store.Message{MailboxID: box.MailboxID, ID: id(at, 1), ReceivedAt: at.AddDate(1, 0, 0), Size: 1 << 20}
	if m := inbox.QueryMessage(nil, nil, nil, nil); m.Have != 0 {
		t.Fatalf("nothing read: %+v", m)
	}
	m := inbox.QueryMessage(item, nil, nil, nil)
	if m.Have != query.FromItem || m.Mailbox != box.MailboxID || !m.Received.Equal(at) || m.SizeKnown {
		t.Fatalf("the item alone: %+v", m)
	}
	meta := &inbox.StoredMeta{
		Format: inbox.MetaFormat, List: "news.example.com", Size: 1024,
		From: []contracts.MailAddress{{Name: "Ann", Address: "ann@example.com"}},
		To:   []contracts.MailAddress{{Address: "to@example.com"}}, Cc: []contracts.MailAddress{{Address: "cc@example.com"}},
		ReplyTo: []contracts.MailAddress{{Address: "reply@example.com"}},
		Subject: "Invoice", Auth: []contracts.AuthCheck{
			{Method: "dkim", Domain: "good.example", Result: "pass"},
			{Method: "dkim", Domain: "bad.example", Result: "fail: body hash"},
			{Method: "spf", Domain: "spf.example", Result: "pass"},
		},
		Attachments: []contracts.MessageAttachment{{Index: 0, Name: "bill.pdf", Type: "application/pdf", Size: 9}},
		Partial:     true, Confirmation: true,
	}
	m = inbox.QueryMessage(item, meta, nil, nil)
	if m.Have != query.FromItem|query.FromMeta || strings.Join(m.From, ",") != "ann@example.com" || strings.Join(m.Recipients, ",") != "to@example.com,cc@example.com" ||
		strings.Join(m.SignedBy, ",") != "good.example" || m.Subject != "Invoice" || len(m.Attachments) != 1 || m.Attachments[0] != (query.Attachment{Name: "bill.pdf", Type: "application/pdf"}) ||
		!m.ListRecorded || strings.Join(m.List, ",") != "news.example.com" || !m.SizeKnown || m.Size != 1024 || !m.Partial || m.Unparsed || !m.Confirmation {
		t.Fatalf("item and meta: %+v", m)
	}
	if m := inbox.QueryMessage(item, &inbox.StoredMeta{Format: inbox.MetaFormat, Size: 5, Unparsed: true}, nil, nil); !m.ListRecorded || m.List != nil || !m.Unparsed {
		t.Fatalf("no List-Id: %+v", m)
	}
	entry := &inbox.SearchEntry{Text: "the start", Cut: true}
	m = inbox.QueryMessage(item, meta, entry, nil)
	if m.Have != query.FromItem|query.FromMeta|query.FromEntry || m.Body != "the start" || !m.BodyCut {
		t.Fatalf("with the entry: %+v", m)
	}
	// The raw message's text supersedes the entry's; its length is the size
	// only when the meta has none.
	text := &inbox.RawText{Text: "the start and the rest", Size: 4096}
	m = inbox.QueryMessage(item, meta, entry, text)
	if m.Have != query.FromItem|query.FromMeta|query.FromEntry|query.FromText || m.Body != text.Text || m.BodyCut || m.Size != 1024 {
		t.Fatalf("with the text: %+v", m)
	}
	m = inbox.QueryMessage(item, &inbox.StoredMeta{}, nil, text)
	if m.Have&query.FromEntry == 0 || !m.SizeKnown || m.Size != 4096 || m.ListRecorded {
		t.Fatalf("format 1 with the text: %+v", m)
	}
}
