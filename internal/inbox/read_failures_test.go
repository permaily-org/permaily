package inbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/inboxtest"
	"github.com/permaily-org/permaily/internal/store"
)

// failing is the store with one kind of read failing on demand.
type failing struct {
	*store.Memory
	scan, entries, item, raw error
}

func (f *failing) ScanMessagesDown(ctx context.Context, mailboxID, before string, maxRead int) (*store.MessageScan, error) {
	if f.scan != nil {
		return nil, f.scan
	}
	return f.Memory.ScanMessagesDown(ctx, mailboxID, before, maxRead)
}

func (f *failing) ScanEntriesDown(ctx context.Context, partition, prefix, before string, maxRead int) (*store.EntryScan, error) {
	if f.entries != nil {
		return nil, f.entries
	}
	return f.Memory.ScanEntriesDown(ctx, partition, prefix, before, maxRead)
}

func (f *failing) Item(ctx context.Context, mailboxID, id string) (*store.Message, error) {
	if f.item != nil {
		return nil, f.item
	}
	return f.Memory.Item(ctx, mailboxID, id)
}

func (f *failing) ReadRaw(ctx context.Context, mailboxID, id string, maxBytes int64) ([]byte, error) {
	if f.raw != nil {
		return nil, f.raw
	}
	return f.Memory.ReadRaw(ctx, mailboxID, id, maxBytes)
}

// Every failure of the store is the reader's error, never a page or a
// message that says what did not happen; a missing raw object is not_found;
// an attachment the meta lists that the raw message does not hold is
// not_found; and a context that ended ends every read.
func TestTheReaderFailsAsTheStoreFails(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	at := now.Add(-time.Hour)
	mid := id(at, 1)
	meta := inbox.StoredMeta{Subject: "needle", Attachments: []contracts.MessageAttachment{{Index: 0, Name: "a.bin"}, {Index: 3, Name: "ghost"}}}
	raw := "From: a@example.org\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nhi\r\n" +
		"--b\r\nContent-Type: application/octet-stream\r\n\r\nbytes\r\n--b--\r\n"
	if err := inboxtest.Seed(ctx, w.mem, w.mem, w.b, inboxtest.Message{ID: mid, ReceivedAt: at, RetentionDays: 30, Raw: []byte(raw), Meta: meta,
		Search: &inbox.SearchEntry{Subject: "needle", ReceivedAt: at}}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	f := &failing{Memory: w.mem}
	r := inbox.NewReader(f)

	f.scan = boom
	if _, err := r.List(ctx, w.b, "", 10); !errors.Is(err, boom) {
		t.Fatalf("list: %v", err)
	}
	f.scan, f.entries = nil, boom
	if _, err := r.Search(ctx, w.b, "needle", "", 10); !errors.Is(err, boom) {
		t.Fatalf("search: %v", err)
	}
	f.entries, f.item = nil, boom
	if _, err := r.Search(ctx, w.b, "needle", "", 10); !errors.Is(err, boom) {
		t.Fatalf("search, the item: %v", err)
	}
	if _, err := r.Get(ctx, w.b, mid); !errors.Is(err, boom) {
		t.Fatalf("get: %v", err)
	}
	f.item, f.raw = nil, boom
	if _, err := r.Get(ctx, w.b, mid); !errors.Is(err, boom) {
		t.Fatalf("get, the raw object: %v", err)
	}
	if _, _, err := r.Attachment(ctx, w.b, mid, 0); !errors.Is(err, boom) {
		t.Fatalf("attachment: %v", err)
	}
	f.raw = store.ErrNotFound
	if _, err := r.Raw(ctx, w.b, mid); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("raw, gone: %v", err)
	}
	f.raw = nil
	if _, err := r.Raw(inbox.WithReadBudget(ctx, refusingBudget{}), w.b, mid); !errors.Is(err, contracts.ErrTooManyRequests) {
		t.Fatalf("raw, no budget: %v", err)
	}
	if name, data, err := r.Attachment(ctx, w.b, mid, 0); err != nil || name != "a.bin" || string(data) != "bytes" {
		t.Fatalf("attachment 0: %q %q %v", name, data, err)
	}
	if _, _, err := r.Attachment(ctx, w.b, mid, 3); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("an attachment listed that the message does not hold: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.List(cancelled, w.b, "", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("list: %v", err)
	}
	if _, err := r.Search(cancelled, w.b, "needle", "", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("search: %v", err)
	}
	if _, _, err := r.Attachment(cancelled, w.b, mid, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("attachment: %v", err)
	}
	if n, err := r.Delete(cancelled, w.b, []string{mid}); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("delete: %d %v", n, err)
	}
	if _, err := r.Get(ctx, w.b, mid); err != nil {
		t.Fatal("deleted by a cancelled request")
	}

	if !inbox.IsMessageID(mid) || inbox.IsMessageID("x") || inbox.IsMessageID(mid+"0") {
		t.Fatal("IsMessageID")
	}
}

// Search entries that do not open are counted and left out: a key that is no
// message identifier, and a value that opens but is not an entry. A page
// whose last entry's key is no identifier says nothing of how far it looked.
func TestSearchEntriesThatDoNotOpen(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	at := now.Add(-time.Hour)
	good := w.seed(at, 30, "found", "needle", "")
	bad := id(at.Add(-time.Minute), 99)
	notJSON, err := envelope.SealText(w.b.Key, []byte("not json"), envelope.AAD(inbox.PurposeSearch, w.b.MailboxID, bad))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []*store.Entry{
		{Partition: inbox.Partition(w.b.MailboxID), Key: inbox.KeySearch + bad, Value: notJSON},
		{Partition: inbox.Partition(w.b.MailboxID), Key: inbox.KeySearch + "0", Value: []byte("x")},
	} {
		if err := w.mem.PutEntry(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	p, err := w.r.Search(ctx, w.b, "needle", "", 10)
	if err != nil || len(p.Messages) != 1 || p.Messages[0].ID != good || !p.SearchedTo.IsZero() || p.Next != "" {
		t.Fatalf("%+v %v", p, err)
	}
}

// cancelling is the store with a call that succeeds and then ends the
// request's context, as a deadline that passes during it does.
type cancelling struct {
	*store.Memory
	cancel                       context.CancelFunc
	onScan, onItem, onDeleteNext bool
}

func (c *cancelling) ScanMessagesDown(ctx context.Context, mailboxID, before string, maxRead int) (*store.MessageScan, error) {
	s, err := c.Memory.ScanMessagesDown(ctx, mailboxID, before, maxRead)
	if c.onScan {
		c.cancel()
	}
	return s, err
}

func (c *cancelling) Item(ctx context.Context, mailboxID, id string) (*store.Message, error) {
	m, err := c.Memory.Item(ctx, mailboxID, id)
	if c.onItem {
		c.cancel()
	}
	return m, err
}

func (c *cancelling) DeleteMessage(ctx context.Context, mailboxID, id string) error {
	err := c.Memory.DeleteMessage(ctx, mailboxID, id)
	if c.onDeleteNext {
		c.cancel()
	}
	return err
}

// The code review of PR #90: List finished its batch and answered a page
// after its context ended. Every loop of the reader over stored items checks
// the context between items and before it answers.
func TestTheReaderStopsWhenItsContextEndsMidway(t *testing.T) {
	w := newWorld(t)
	at := now.Add(-time.Hour)
	first := w.seed(at, 30, "needle one", "text", "")
	second := w.seed(at.Add(time.Minute), 30, "needle two", "text", "")
	run := func(set func(*cancelling)) (*cancelling, context.Context) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		c := &cancelling{Memory: w.mem, cancel: cancel}
		set(c)
		return c, ctx
	}

	t.Run("List: a scan that ends the context", func(t *testing.T) {
		c, ctx := run(func(c *cancelling) { c.onScan = true })
		if p, err := inbox.NewReader(c).List(ctx, w.b, "", 10); !errors.Is(err, context.Canceled) {
			t.Fatalf("a page %+v after the context ended, err %v", p, err)
		}
	})
	t.Run("Search: an item read that ends the context, on the page's last match", func(t *testing.T) {
		c, ctx := run(func(c *cancelling) { c.onItem = true })
		if p, err := inbox.NewReader(c).Search(ctx, w.b, "needle one", "", 10); !errors.Is(err, context.Canceled) {
			t.Fatalf("a page %+v after the context ended, err %v", p, err)
		}
	})
	t.Run("Delete: the context ends after the first", func(t *testing.T) {
		c, ctx := run(func(c *cancelling) { c.onDeleteNext = true })
		n, err := inbox.NewReader(c).Delete(ctx, w.b, []string{second, first})
		if n != 1 || !errors.Is(err, context.Canceled) {
			t.Fatalf("%d deleted, err %v", n, err)
		}
		if _, err := w.mem.Item(context.Background(), w.b.MailboxID, first); err != nil {
			t.Fatalf("the second id was deleted after the context ended: %v", err)
		}
	})
}
