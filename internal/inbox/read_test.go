package inbox_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddb "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/inboxtest"
	"github.com/permaily-org/permaily/internal/store"
)

// now is the tests' clock: the store reads expiry against it.
var now = time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

// readStore is what the reader's tests need of a store: what the reader
// reads, and what Seed writes.
type readStore interface {
	inbox.ReadStore
	store.Store
	store.Entries
}

type world struct {
	t   *testing.T
	st  readStore
	mem *store.Memory // nil on the emulators
	r   inbox.Reader
	b   inbox.Box
	n   int
}

func newBox(t *testing.T, id string) inbox.Box {
	t.Helper()
	key := make([]byte, envelope.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return inbox.Box{MailboxID: id, Key: key}
}

func newWorld(t *testing.T) *world {
	t.Helper()
	mem := store.NewMemory()
	mem.Now = func() time.Time { return now }
	return &world{t: t, st: mem, mem: mem, r: inbox.NewReader(mem), b: newBox(t, "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa")}
}

// emulatedWorld is a world over DynamoDB Local and S3Mock, when
// PERMAILY_TEST_DYNAMODB and PERMAILY_TEST_S3 name them.
func emulatedWorld(t *testing.T) *world {
	t.Helper()
	dynEndpoint, s3Endpoint := os.Getenv("PERMAILY_TEST_DYNAMODB"), os.Getenv("PERMAILY_TEST_S3")
	if dynEndpoint == "" || s3Endpoint == "" {
		t.Skip("PERMAILY_TEST_DYNAMODB and PERMAILY_TEST_S3 not set")
	}
	ctx := context.Background()
	cfg := aws.Config{Region: "us-east-2", Credentials: credentials.NewStaticCredentialsProvider("test", "testtest", "")}
	db := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(dynEndpoint) })
	s3c := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(s3Endpoint)
		o.UsePathStyle = true
	})
	name := fmt.Sprint("permaily-inbox-", time.Now().UnixNano())
	if _, err := db.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(name), BillingMode: ddb.BillingModePayPerRequest,
		AttributeDefinitions: []ddb.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: ddb.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: ddb.ScalarAttributeTypeS},
		},
		KeySchema: []ddb.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: ddb.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: ddb.KeyTypeRange},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(name)}); err != nil {
		t.Fatal(err)
	}
	st := &store.AWS{DB: db, S3: s3c, Table: name, Bucket: name, Now: func() time.Time { return now }}
	return &world{t: t, st: st, r: inbox.NewReader(st), b: newBox(t, "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa")}
}

// id is a message identifier for a message received at: time first.
func id(at time.Time, n int) string { return fmt.Sprintf("%016x%016x", at.UnixNano(), n) }

const plainMail = "From: Ann <ann@example.org>\r\nTo: box@inbox.permaily.test\r\nSubject: Hi\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHello there\r\n"

// seed stores a message received at, kept days, in the world's box.
func (w *world) seed(at time.Time, days int, subject, text string, raw string) string {
	w.t.Helper()
	w.n++
	mid := id(at, w.n)
	if raw == "" {
		raw = plainMail
	}
	meta := inbox.StoredMeta{From: []contracts.MailAddress{{Name: "Ann", Address: "ann@example.org"}}, Subject: subject, Snippet: text}
	m := inboxtest.Message{
		ID: mid, ReceivedAt: at, RetentionDays: days, Raw: []byte(raw), Meta: meta,
		Search: &inbox.SearchEntry{From: meta.From, Subject: subject, Text: text, ReceivedAt: at},
	}
	if err := inboxtest.Seed(context.Background(), w.st, w.st, w.b, m); err != nil {
		w.t.Fatal(err)
	}
	return mid
}

// R3: a message reads back as delivery stored it: its meta, its text from
// its text/plain part, its attachments by index and its raw bytes.
func TestR3ReadingWhatDeliveryStored(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	raw := "From: Ann <ann@example.org>\r\nTo: box@inbox.permaily.test\r\nSubject: Report\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: multipart/alternative; boundary=c\r\n\r\n" +
		"--c\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nCaf=E9 at noon\r\n" +
		"--c\r\nContent-Type: text/html\r\n\r\n<p>Other words</p>\r\n--c--\r\n" +
		"--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"a.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0x\r\n" +
		"--b\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=\"notes.txt\"\r\n\r\nsecond\r\n--b--\r\n"
	at := now.Add(-time.Hour)
	w.n++
	mid := id(at, w.n)
	meta := inbox.StoredMeta{
		From: []contracts.MailAddress{{Name: "Ann", Address: "ann@example.org"}}, Subject: "Report", Date: "Thu, 12 Nov 2026 08:00:00 +0000",
		Attachments: []contracts.MessageAttachment{{Index: 0, Name: "a.pdf", Type: "application/pdf", Size: 5}, {Index: 1, Name: "notes.txt", Type: "text/plain", Size: 6}},
		Auth:        []contracts.AuthCheck{{Method: "dkim", Domain: "example.org", Result: "pass"}},
	}
	if err := inboxtest.Seed(ctx, w.st, w.st, w.b, inboxtest.Message{ID: mid, ReceivedAt: at, RetentionDays: 30, Raw: []byte(raw), Meta: meta}); err != nil {
		t.Fatal(err)
	}
	m, err := w.r.Get(ctx, w.b, mid)
	if err != nil {
		t.Fatal(err)
	}
	if m.Text != "Café at noon" || m.TextFrom != contracts.TextFromPlain || m.TextTruncated {
		t.Errorf("text %q from %s", m.Text, m.TextFrom)
	}
	if m.Subject != "Report" || m.Size != len(raw) || !m.ExpiresAt.Equal(at.AddDate(0, 0, 30)) || len(m.Attachments) != 2 || len(m.Auth) != 1 {
		t.Errorf("message %+v", m)
	}
	// Pull request 5 fills Message.HTML: the HTML of the alternative, shown.
	if m.HTML == nil || m.HTML.Status != contracts.HTMLShown || m.HTML.Links != 0 {
		t.Errorf("html %+v", m.HTML)
	}
	if m.To == nil || m.Cc == nil || m.ReplyTo == nil {
		t.Error("a list is null")
	}
	name, data, err := w.r.Attachment(ctx, w.b, mid, 0)
	if err != nil || name != "a.pdf" || string(data) != "%PDF-1" {
		t.Errorf("attachment 0: %q %q %v", name, data, err)
	}
	name, data, err = w.r.Attachment(ctx, w.b, mid, 1)
	if err != nil || name != "notes.txt" || string(data) != "second" {
		t.Errorf("attachment 1: %q %q %v", name, data, err)
	}
	for _, i := range []int{2, -1, 99} {
		if _, _, err := w.r.Attachment(ctx, w.b, mid, i); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("attachment %d: %v", i, err)
		}
	}
	got, err := w.r.Raw(ctx, w.b, mid)
	if err != nil || string(got) != raw {
		t.Errorf("raw %v", err)
	}

	t.Run("a message of another box, an identifier that is not one, an expired one", func(t *testing.T) {
		other := newBox(t, "mbx_bbbbbbbbbbbbbbbbbbbbbbbbbb")
		expired := w.seed(now.AddDate(0, 0, -8), 7, "old", "", "")
		for _, c := range []struct {
			b  inbox.Box
			id string
		}{{other, mid}, {inbox.Box{MailboxID: w.b.MailboxID, Key: other.Key}, mid}, {w.b, "nothex"}, {w.b, strings.ToUpper(mid)}, {w.b, "../" + mid[3:]}, {w.b, expired}} {
			if _, err := w.r.Get(ctx, c.b, c.id); !errors.Is(err, contracts.ErrNotFound) {
				t.Errorf("Get: %v", err)
			}
			if _, err := w.r.Raw(ctx, c.b, c.id); !errors.Is(err, contracts.ErrNotFound) {
				t.Errorf("Raw: %v", err)
			}
			if _, _, err := w.r.Attachment(ctx, c.b, c.id, 0); !errors.Is(err, contracts.ErrNotFound) {
				t.Errorf("Attachment: %v", err)
			}
		}
	})

	t.Run("text made from the HTML part inside the enclave", func(t *testing.T) {
		html := "From: a@example.org\r\nSubject: x\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
			"<html><head><title>T</title><style>p{color:red}</style></head><body><script>alert(1)</script>" +
			"<h1>Hello &amp; welcome</h1><p>Read <a href=\"https://example.org/x\">the news</a> today.</p><div>One<br>Two</div></body></html>"
		mid := w.seed(at, 30, "x", "", html)
		m, err := w.r.Get(ctx, w.b, mid)
		if err != nil {
			t.Fatal(err)
		}
		want := "Hello & welcome\nRead the news [https://example.org/x] today.\nOne\nTwo"
		if m.Text != want || m.TextFrom != contracts.TextFromHTML {
			t.Errorf("text %q from %s, want %q", m.Text, m.TextFrom, want)
		}
	})

	t.Run("no text at all, and an unparsed message read without its raw object", func(t *testing.T) {
		img := "From: a@example.org\r\nContent-Type: image/png\r\n\r\nPNG"
		mid := w.seed(at, 30, "x", "", img)
		if m, err := w.r.Get(ctx, w.b, mid); err != nil || m.Text != "" || m.TextFrom != contracts.TextFromNone {
			t.Errorf("%+v %v", m, err)
		}
		w.n++
		un := id(at, w.n)
		if err := inboxtest.Seed(ctx, w.st, w.st, w.b, inboxtest.Message{ID: un, ReceivedAt: at, RetentionDays: 30, Raw: []byte("\x00garbage"), Meta: inbox.StoredMeta{Unparsed: true}}); err != nil {
			t.Fatal(err)
		}
		reads := 0
		w.mem.Interrupt = func(_ context.Context, step store.Step, _, key string) error {
			if step == store.StepRead && key == "msg#"+un {
				reads++
			}
			return nil
		}
		defer func() { w.mem.Interrupt = nil }()
		m, err := w.r.Get(ctx, w.b, un)
		if err != nil || !m.Unparsed || m.TextFrom != contracts.TextFromNone || reads != 1 {
			t.Errorf("%+v %v, %d reads", m, err, reads)
		}
		if _, _, err := w.r.Attachment(ctx, w.b, un, 0); !errors.Is(err, contracts.ErrNotFound) {
			t.Error(err)
		}
	})

	t.Run("text past 256 KiB is cut and says so", func(t *testing.T) {
		long := "From: a@example.org\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + strings.Repeat("é", inbox.MessageTextMax)
		mid := w.seed(at, 30, "x", "", long)
		m, err := w.r.Get(ctx, w.b, mid)
		if err != nil || !m.TextTruncated || len(m.Text) > inbox.MessageTextMax || len(m.Text) < inbox.MessageTextMax-1 || strings.ContainsRune(m.Text, '�') {
			t.Errorf("%d bytes, truncated %v, %v", len(m.Text), m.TextTruncated, err)
		}
	})
}

// R4: a page of the list reads at most 500 items and says where to go on,
// and pages of every shape read every live message once, newest first.
func TestR4ListingPages(t *testing.T) {
	ctx := context.Background()

	t.Run("a page filled inside a batch: next is the fiftieth message, and the next page starts right after it", func(t *testing.T) {
		w := newWorld(t)
		var live []string
		for i := range 100 {
			at := now.Add(-time.Duration(200-i) * time.Minute)
			if i%5 == 0 {
				w.seed(at.AddDate(0, 0, -60), 30, "expired", "", "") // 20 expired, among the others
				continue
			}
			live = append(live, w.seed(at, 30, "live", "", ""))
		}
		slices.Reverse(live)
		page, err := w.r.List(ctx, w.b, "", 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Messages) != 50 || page.Next != live[49] {
			t.Fatalf("%d messages, next %q, want %q", len(page.Messages), page.Next, live[49])
		}
		next, err := w.r.List(ctx, w.b, page.Next, 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(next.Messages) != 30 || next.Messages[0].ID != live[50] || next.Next != "" {
			t.Fatalf("second page: %d, first %s, next %q", len(next.Messages), next.Messages[0].ID, next.Next)
		}
	})

	t.Run("nothing is skipped across pages of every shape", func(t *testing.T) {
		w := newWorld(t)
		var live []string
		for i := range 230 {
			at := now.Add(-time.Duration(1000-i) * time.Minute)
			if i%7 == 3 || (i > 100 && i < 140) {
				w.seed(at.AddDate(0, 0, -60), 30, "expired", "", "")
				continue
			}
			live = append(live, w.seed(at, 30, "live", "", ""))
		}
		// One that does not open under the box's key: read, never shown.
		other := newBox(t, w.b.MailboxID)
		if err := inboxtest.Seed(ctx, w.st, w.st, other, inboxtest.Message{ID: id(now.Add(-500*time.Minute), 9999), ReceivedAt: now.Add(-500 * time.Minute), RetentionDays: 30, Raw: []byte(plainMail)}); err != nil {
			t.Fatal(err)
		}
		slices.Reverse(live)
		for _, pageItems := range []int{0, 1, 7, 64, 100} {
			for _, limit := range []int{1, 2, 49, 50, 99, 100} {
				w.mem.PageItems = pageItems
				var got []string
				after := ""
				for range 1000 {
					page, err := w.r.List(ctx, w.b, after, limit)
					if err != nil {
						t.Fatal(err)
					}
					if len(page.Messages) > limit {
						t.Fatalf("%d messages for a limit of %d", len(page.Messages), limit)
					}
					for _, m := range page.Messages {
						got = append(got, m.ID)
					}
					if page.Next == "" {
						break
					}
					after = page.Next
				}
				if !slices.Equal(got, live) {
					t.Fatalf("page items %d, limit %d: %d messages, want %d", pageItems, limit, len(got), len(live))
				}
			}
		}
	})

}

// R4, the short page: 600 expired items above a live message are read 500 at
// most a page; the first page is empty with next, the second finds it.
func TestR4AShortPageSaysWhereToGoOn(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	live := w.seed(now.Add(-48*time.Hour), 30, "live", "", "")
	var expired []string
	for i := range 600 {
		// Newer than the live one by identifier, but expired: retention set
		// back by whoever writes to the table, or a clock.
		at := now.Add(-47 * time.Hour).Add(time.Duration(i) * time.Second)
		w.n++
		mid := id(at, w.n)
		if err := inboxtest.Seed(ctx, w.st, w.st, w.b, inboxtest.Message{ID: mid, ReceivedAt: at.AddDate(0, 0, -30), RetentionDays: 7, Raw: []byte(plainMail)}); err != nil {
			t.Fatal(err)
		}
		expired = append(expired, mid)
	}
	slices.Reverse(expired)
	scans := 0
	w.mem.Interrupt = func(_ context.Context, step store.Step, partition, key string) error {
		if step == store.StepRead && strings.HasPrefix(partition, "mailbox#") && key == "msg#" {
			scans++
		}
		return nil
	}
	page, err := w.r.List(ctx, w.b, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 0 || page.Next != expired[499] || scans != 5 {
		t.Fatalf("%d messages, next %q (want %q), %d scans", len(page.Messages), page.Next, expired[499], scans)
	}
	page, err = w.r.List(ctx, w.b, page.Next, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != live || page.Next != "" {
		t.Fatalf("second page %+v", page)
	}
}

// R6: a message whose item says a smaller size than its raw object fails
// without the store reading past the bound the item gave; the bound is
// taken from the budget before the read.
func TestR6ARawObjectLargerThanItsItemSays(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	big := "From: a@example.org\r\nContent-Type: text/plain\r\n\r\n" + strings.Repeat("x", 64<<10)
	mid := w.seed(now.Add(-time.Hour), 30, "big", "", big)
	item, err := w.mem.Item(ctx, w.b.MailboxID, mid)
	if err != nil {
		t.Fatal(err)
	}
	item.Size = 1 // whoever writes to the table
	if err := w.mem.PutItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	rec := &recordingStore{ReadStore: w.mem}
	r := inbox.NewReader(rec)
	budget := &countingBudget{}
	bctx := inbox.WithReadBudget(ctx, budget)
	if _, err := r.Get(bctx, w.b, mid); err == nil || errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("Get: %v", err)
	}
	_, err = r.Raw(bctx, w.b, mid)
	if err == nil {
		t.Fatal("Raw read a raw object larger than its bound")
	}
	if len(rec.bounds) != 2 || rec.bounds[0] != 1+1024 || budget.taken[0] != rec.bounds[0] {
		t.Fatalf("bounds %v, taken %v", rec.bounds, budget.taken)
	}
	if strings.Contains(err.Error(), mid) {
		t.Error("the error names the message")
	}

	t.Run("a size larger than any message takes at most the largest bound", func(t *testing.T) {
		item.Size = 1 << 40
		if err := w.mem.PutItem(ctx, item); err != nil {
			t.Fatal(err)
		}
		rec.bounds = nil
		if _, err := r.Raw(bctx, w.b, mid); err != nil {
			t.Fatal(err)
		}
		if rec.bounds[0] != inbox.RawReadMax {
			t.Fatalf("bound %d", rec.bounds[0])
		}
	})

	t.Run("a budget that refuses: nothing is read from the bucket", func(t *testing.T) {
		rec.bounds = nil
		_, err := r.Raw(inbox.WithReadBudget(ctx, refusingBudget{}), w.b, mid)
		if !errors.Is(err, contracts.ErrTooManyRequests) || len(rec.bounds) != 0 {
			t.Fatalf("%v, %d reads", err, len(rec.bounds))
		}
	})
}

type recordingStore struct {
	inbox.ReadStore
	mu     sync.Mutex
	bounds []int64
}

func (s *recordingStore) ReadRaw(ctx context.Context, mailboxID, id string, max int64) ([]byte, error) {
	s.mu.Lock()
	s.bounds = append(s.bounds, max)
	s.mu.Unlock()
	return s.ReadStore.ReadRaw(ctx, mailboxID, id, max)
}

type countingBudget struct{ taken []int64 }

func (b *countingBudget) Take(_ context.Context, n int64) error {
	b.taken = append(b.taken, n)
	return nil
}

type refusingBudget struct{}

func (refusingBudget) Take(context.Context, int64) error { return contracts.ErrTooManyRequests }

// Deleting removes the raw object, the item, the search and confirmation
// entries, and counts only what was there.
func TestDeletingMessages(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	at := now.Add(-time.Hour)
	a := w.seed(at, 30, "a", "", "")
	w.n++
	b := id(at, w.n)
	if err := inboxtest.Seed(ctx, w.st, w.st, w.b, inboxtest.Message{ID: b, ReceivedAt: at, RetentionDays: 30, Raw: []byte(plainMail),
		Confirm: &inbox.ConfirmEntry{Code: "123456789", ReceivedAt: at, Ends: at.AddDate(0, 0, 30)}}); err != nil {
		t.Fatal(err)
	}
	c := w.seed(at, 30, "c", "", "")
	n, err := w.r.Delete(ctx, w.b, []string{a, b, a, "nothex", id(at, 999)})
	if err != nil || n != 2 {
		t.Fatalf("deleted %d, %v", n, err)
	}
	for _, mid := range []string{a, b} {
		if _, err := w.mem.Item(ctx, w.b.MailboxID, mid); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("item: %v", err)
		}
		if _, err := w.mem.ReadRaw(ctx, w.b.MailboxID, mid, 1<<20); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("raw: %v", err)
		}
		for _, k := range []string{inbox.KeySearch, inbox.KeyConfirm} {
			if _, err := w.mem.GetEntry(ctx, inboxtest.Partition(w.b.MailboxID), k+mid); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s: %v", k, err)
			}
		}
	}
	if _, err := w.r.Get(ctx, w.b, c); err != nil {
		t.Errorf("the other one: %v", err)
	}
	if n, err := w.r.Delete(ctx, w.b, []string{a}); err != nil || n != 0 {
		t.Errorf("again: %d %v", n, err)
	}
	if _, err := w.r.Delete(ctx, w.b, make([]string, inbox.MaxDelete+1)); !errors.Is(err, contracts.ErrTooManyMessages) {
		t.Errorf("101: %v", err)
	}

	t.Run("a storage error stops and says so", func(t *testing.T) {
		w.mem.Interrupt = func(_ context.Context, step store.Step, _, _ string) error {
			if step == store.StepDeleteEntry {
				return errors.New("storage down")
			}
			return nil
		}
		defer func() { w.mem.Interrupt = nil }()
		if _, err := w.r.Delete(ctx, w.b, []string{c}); err == nil {
			t.Fatal("no error")
		}
	})
}

// R7: the store held under a request: the reader returns when the request's
// context ends, and deletes nothing after.
func TestR7AHeldStoreEndsWithTheRequest(t *testing.T) {
	w := newWorld(t)
	at := now.Add(-time.Hour)
	ids := []string{w.seed(at, 30, "a", "", ""), w.seed(at, 30, "b", "", ""), w.seed(at, 30, "c", "", "")}
	held := make(chan struct{})
	w.mem.Interrupt = func(ctx context.Context, step store.Step, _, key string) error {
		if step == store.StepDeleteRaw && key == "msg#"+ids[1] {
			close(held)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	n, err := w.r.Delete(ctx, w.b, ids)
	<-held
	if !errors.Is(err, context.DeadlineExceeded) || n != 1 || time.Since(start) > 5*time.Second {
		t.Fatalf("%d, %v", n, err)
	}
	w.mem.Interrupt = nil
	for _, mid := range ids[1:] {
		if _, err := w.r.Get(context.Background(), w.b, mid); err != nil {
			t.Errorf("deleted after the deadline: %v", err)
		}
	}
	for _, f := range []func(context.Context) error{
		func(ctx context.Context) error { _, err := w.r.List(ctx, w.b, "", 10); return err },
		func(ctx context.Context) error { _, err := w.r.Search(ctx, w.b, "a", "", 10); return err },
		func(ctx context.Context) error { _, err := w.r.Get(ctx, w.b, ids[1]); return err },
	} {
		w.mem.Interrupt = func(ctx context.Context, _ store.Step, _, _ string) error { <-ctx.Done(); return ctx.Err() }
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		if err := f(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%v", err)
		}
		cancel()
	}
}

// The text of a message never holds what its HTML would run or fetch: a
// script's or a style's content is not text.
func TestTheTextOfHTMLHoldsNoScript(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	html := "From: a@example.org\r\nContent-Type: text/html\r\n\r\n<script>steal()</script><style>.x{}</style><p onclick=\"evil()\">visible</p><img src=x onerror=alert(1)>"
	mid := w.seed(now.Add(-time.Hour), 30, "x", "", html)
	m, err := w.r.Get(ctx, w.b, mid)
	if err != nil {
		t.Fatal(err)
	}
	if m.Text != "visible" {
		t.Fatalf("%q", m.Text)
	}
}
