package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddb "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// partsStore is what a store gives for a mailbox's messages and entries
// read newest first (ADR 0026): Memory and AWS.
type partsStore interface {
	Store
	Entries
	Scanner
	MessageParts
}

// emulated is AWS over DynamoDB Local and S3Mock, with a table and a bucket
// of its own, when PERMAILY_TEST_DYNAMODB and PERMAILY_TEST_S3 name them.
func emulated(t *testing.T) (*AWS, *s3.Client) {
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
	suffix := fmt.Sprint(time.Now().UnixNano())
	table, bucket := "permaily-parts-"+suffix, "permaily-parts-"+suffix
	if _, err := db.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   aws.String(table),
		BillingMode: ddb.BillingModePayPerRequest,
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
	if _, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}
	return &AWS{DB: db, S3: s3c, Table: table, Bucket: bucket}, s3c
}

// The cases of ADR 0026 that every store keeps, in memory and on the
// emulators.
func TestScansAndPartsInMemory(t *testing.T) {
	s := NewMemory()
	scansAndParts(t, s, func(n time.Time) { s.Now = func() time.Time { return n } }, nil)
}

func TestScansAndPartsOnAWS(t *testing.T) {
	s, s3c := emulated(t)
	scansAndParts(t, s, func(n time.Time) { s.Now = func() time.Time { return n } }, s3c)
}

func scansAndParts(t *testing.T, s partsStore, setNow func(time.Time), s3c *s3.Client) {
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	setNow(now)
	entry := func(partition, key string, expires time.Time) {
		t.Helper()
		if err := s.PutEntry(ctx, &Entry{Partition: partition, Key: key, Value: []byte("v " + key), ExpiresAt: expires}); err != nil {
			t.Fatal(err)
		}
	}
	message := func(mailbox, id string, expires time.Time) *Message {
		t.Helper()
		m := &Message{MailboxID: mailbox, ID: id, ReceivedAt: now, ExpiresAt: expires, Size: 3, RetentionDays: 7, Meta: []byte("meta " + id)}
		if err := s.PutItem(ctx, m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	past, future := now.Add(-time.Hour), now.Add(time.Hour)

	t.Run("S5: 300 expired entries above one that is not", func(t *testing.T) {
		entry("s5", "txt#000", time.Time{})
		for i := 1; i <= 300; i++ {
			entry("s5", fmt.Sprintf("txt#%03d", i), past)
		}
		entry("s5", "zzz", time.Time{}) // another prefix, above
		before := ""
		for page := range 3 {
			got, err := s.ScanEntriesDown(ctx, "s5", "txt#", before, 100)
			want := fmt.Sprintf("txt#%03d", 300-100*page-99)
			if err != nil || got.Read != 100 || len(got.Entries) != 0 || got.Entries == nil || got.Last != want || !got.More {
				t.Fatalf("page %d: %+v, %v; want Last %s", page, got, err, want)
			}
			before = got.Last
		}
		got, err := s.ScanEntriesDown(ctx, "s5", "txt#", before, 100)
		if err != nil || got.Read != 1 || len(got.Entries) != 1 || got.Entries[0].Key != "txt#000" || string(got.Entries[0].Value) != "v txt#000" || got.More || got.Last != "txt#000" {
			t.Fatalf("last page: %+v, %v", got, err)
		}

		message("s5", "00", future)
		for i := 1; i <= 300; i++ {
			message("s5", fmt.Sprintf("%03d", i), past)
		}
		before = ""
		for page := range 3 {
			got, err := s.ScanMessagesDown(ctx, "s5", before, 100)
			want := fmt.Sprintf("%03d", 300-100*page-99)
			if err != nil || got.Read != 100 || len(got.Messages) != 0 || got.Messages == nil || got.Last != want || !got.More {
				t.Fatalf("messages, page %d: %+v, %v", page, got, err)
			}
			before = got.Last
		}
		last, err := s.ScanMessagesDown(ctx, "s5", before, 100)
		if err != nil || last.Read != 1 || len(last.Messages) != 1 || last.Messages[0].ID != "00" || string(last.Messages[0].Meta) != "meta 00" || last.More || last.Last != "00" {
			t.Fatalf("messages, last page: %+v, %v", last, err)
		}
	})

	t.Run("S12: a partition of exactly one page", func(t *testing.T) {
		for i := range 100 {
			entry("s12", fmt.Sprintf("txt#%03d", i), time.Time{})
		}
		// The first page may or may not say More (Scanner: a page that read
		// exactly maxRead may have nothing below it); the one below it is
		// empty and says no more.
		got, err := s.ScanEntriesDown(ctx, "s12", "txt#", "", 100)
		if err != nil || got.Read != 100 || len(got.Entries) != 100 || got.Entries[0].Key != "txt#099" || got.Last != "txt#000" {
			t.Fatalf("first page: read %d, %d entries, last %q, more %v, %v", got.Read, len(got.Entries), got.Last, got.More, err)
		}
		next, err := s.ScanEntriesDown(ctx, "s12", "txt#", got.Last, 100)
		if err != nil || next.Read != 0 || len(next.Entries) != 0 || next.Entries == nil || next.Last != "" || next.More {
			t.Fatalf("next page: %+v, %v", next, err)
		}
	})

	t.Run("S13: a last page of nothing but expired items", func(t *testing.T) {
		for i := range 5 {
			entry("s13", fmt.Sprintf("txt#%d", i), past)
			message("s13", fmt.Sprintf("%d", i), past)
		}
		got, err := s.ScanEntriesDown(ctx, "s13", "txt#", "", 100)
		if err != nil || got.Read != 5 || len(got.Entries) != 0 || got.Last != "txt#0" || got.More {
			t.Fatalf("entries: %+v, %v", got, err)
		}
		msgs, err := s.ScanMessagesDown(ctx, "s13", "", 100)
		if err != nil || msgs.Read != 5 || len(msgs.Messages) != 0 || msgs.Last != "0" || msgs.More {
			t.Fatalf("messages: %+v, %v", msgs, err)
		}
	})

	t.Run("where a scan starts and what it reads", func(t *testing.T) {
		for _, k := range []string{"a", "txt#1", "txt#3", "txt#5", "u"} {
			entry("bounds", k, time.Time{})
		}
		keys := func(before, prefix string, maxRead int) string {
			t.Helper()
			got, err := s.ScanEntriesDown(ctx, "bounds", prefix, before, maxRead)
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, e := range got.Entries {
				out = append(out, e.Key)
			}
			return fmt.Sprintf("%v read %d last %q", out, got.Read, got.Last)
		}
		for _, c := range []struct{ before, prefix, want string }{
			{"", "txt#", `[txt#5 txt#3 txt#1] read 3 last "txt#1"`},
			{"txt#3", "txt#", `[txt#1] read 1 last "txt#1"`},       // before itself is not read
			{"txt#4", "txt#", `[txt#3 txt#1] read 2 last "txt#1"`}, // a key nothing is at
			{"txt#", "txt#", `[] read 0 last ""`},
			{"tz", "txt#", `[txt#5 txt#3 txt#1] read 3 last "txt#1"`}, // past every key with the prefix
			{"b", "txt#", `[] read 0 last ""`},                        // below every key with the prefix
			{"", "", `[u txt#5 txt#3 txt#1 a] read 5 last "a"`},
			{"txt#3", "", `[txt#1 a] read 2 last "a"`},
		} {
			if got := keys(c.before, c.prefix, 100); got != c.want {
				t.Errorf("before %q, prefix %q: %s, want %s", c.before, c.prefix, got, c.want)
			}
		}
		// One item a page goes down one item at a time, before never read again.
		if got := keys("txt#5", "txt#", 1); got != `[txt#3] read 1 last "txt#3"` {
			t.Errorf("one a page: %s", got)
		}
		for _, n := range []int{0, -1, MaxScanRead + 1} {
			if _, err := s.ScanEntriesDown(ctx, "bounds", "", "", n); err == nil {
				t.Errorf("maxRead %d accepted", n)
			}
			if _, err := s.ScanMessagesDown(ctx, "bounds", "", n); err == nil {
				t.Errorf("maxRead %d accepted for messages", n)
			}
		}
		if got, err := s.ScanMessagesDown(ctx, "nobody", "", 10); err != nil || got.Read != 0 || got.Messages == nil || got.More || got.Last != "" {
			t.Errorf("an empty mailbox: %+v, %v", got, err)
		}
	})

	t.Run("Item reads the item alone", func(t *testing.T) {
		m := message("item", "0001", future)
		got, err := s.Item(ctx, "item", "0001")
		if err != nil || got.ID != m.ID || got.Size != 3 || got.RetentionDays != 7 || !got.ExpiresAt.Equal(future) || string(got.Meta) != "meta 0001" {
			t.Fatalf("item %+v, %v", got, err)
		}
		message("item", "0002", past)
		for _, id := range []string{"0002", "0003"} {
			if _, err := s.Item(ctx, "item", id); !errors.Is(err, ErrNotFound) {
				t.Fatalf("item %s: %v", id, err)
			}
		}
		// PutItem replaces an earlier try's item.
		m.Size = 4
		if err := s.PutItem(ctx, m); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Item(ctx, "item", "0001"); err != nil || got.Size != 4 {
			t.Fatalf("item after a retry %+v, %v", got, err)
		}
	})

	t.Run("S14: a delete that stopped after the raw object is finished by asking again", func(t *testing.T) {
		m := message("s14", "0001", future)
		if err := s.PutRaw(ctx, m, []byte("raw")); err != nil {
			t.Fatal(err)
		}
		down := errors.New("the item's delete failed")
		undo := failItemDelete(s, down)
		if err := s.DeleteMessage(ctx, "s14", "0001"); !errors.Is(err, down) {
			t.Fatalf("first try: %v", err)
		}
		undo()
		if _, err := s.ReadRaw(ctx, "s14", "0001", 100); !errors.Is(err, ErrNotFound) {
			t.Fatalf("the raw object after the first try: %v", err)
		}
		if _, err := s.Item(ctx, "s14", "0001"); err != nil {
			t.Fatalf("the item after the first try: %v", err)
		}
		if err := s.DeleteMessage(ctx, "s14", "0001"); err != nil {
			t.Fatalf("second try: %v", err)
		}
		if _, err := s.Item(ctx, "s14", "0001"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("the item after the second try: %v", err)
		}
		if err := s.DeleteMessage(ctx, "s14", "0001"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("third try: %v", err)
		}
		if _, err := s.ReadRaw(ctx, "s14", "0001", 100); !errors.Is(err, ErrNotFound) {
			t.Fatalf("the raw object at the end: %v", err)
		}
		// A raw object without its item is deleted all the same.
		if err := s.PutRaw(ctx, &Message{MailboxID: "s14", ID: "0002", RetentionDays: 7}, []byte("raw")); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteMessage(ctx, "s14", "0002"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a raw object alone: %v", err)
		}
		if _, err := s.ReadRaw(ctx, "s14", "0002", 100); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a raw object alone after its delete: %v", err)
		}
	})

	t.Run("S15: a raw object one byte past its bound", func(t *testing.T) {
		m := message("s15", "0001", future)
		m.Size = 1 // whoever writes to the table says so
		if err := s.PutItem(ctx, m); err != nil {
			t.Fatal(err)
		}
		raw := bytes.Repeat([]byte("x"), 1001)
		if err := s.PutRaw(ctx, m, raw); err != nil {
			t.Fatal(err)
		}
		read := countBodies(s)
		if _, err := s.ReadRaw(ctx, "s15", "0001", 1000); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("one byte past: %v", err)
		}
		if n := read(); n > 1001 {
			t.Fatalf("%d bytes read for a bound of 1000", n)
		}
		// Far larger than its bound: no more than the bound and one byte is read.
		huge := &Message{MailboxID: "s15", ID: "0002", RetentionDays: 7}
		if err := s.PutRaw(ctx, huge, bytes.Repeat([]byte("y"), 1<<20)); err != nil {
			t.Fatal(err)
		}
		read = countBodies(s)
		if _, err := s.ReadRaw(ctx, "s15", "0002", 1000); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("far past: %v", err)
		}
		if n := read(); n > 1001 {
			t.Fatalf("%d bytes read of an object far past a bound of 1000", n)
		}
		if got, err := s.ReadRaw(ctx, "s15", "0001", 1001); err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("at its bound: %d bytes, %v", len(got), err)
		}
		if _, err := s.ReadRaw(ctx, "s15", "0001", -1); err == nil {
			t.Fatal("a negative bound accepted")
		}
		if _, err := s.ReadRaw(ctx, "s15", "none", 10); !errors.Is(err, ErrNotFound) {
			t.Fatalf("no object: %v", err)
		}
	})

	t.Run("S16: a raw object is written once", func(t *testing.T) {
		m := &Message{MailboxID: "s16", ID: "0001", RetentionDays: 7}
		if err := s.PutRaw(ctx, m, []byte("first")); err != nil {
			t.Fatal(err)
		}
		written := lastModified(t, s, s3c, m)
		if s3c != nil {
			time.Sleep(1100 * time.Millisecond) // S3 dates objects to the second
		}
		if err := s.PutRaw(ctx, m, []byte("second")); !errors.Is(err, ErrExists) {
			t.Fatalf("a second write: %v", err)
		}
		if got, err := s.ReadRaw(ctx, "s16", "0001", 100); err != nil || string(got) != "first" {
			t.Fatalf("after a second write: %q, %v", got, err)
		}
		if again := lastModified(t, s, s3c, m); !again.Equal(written) {
			t.Fatalf("the date of the first write changed: %s, then %s", written, again)
		}
		if s3c != nil {
			// S3Mock takes two conditional creates of one key at once (both
			// answered 200 when tried on 2026-11-12); S3 takes one. Two at once
			// are shown in memory here, and against S3 itself by two commits
			// appended at once (TestS3CommitsAgainstS3), which rest on the same
			// condition.
			return
		}
		for i := range 10 {
			m := &Message{MailboxID: "s16", ID: fmt.Sprintf("1%03d", i), RetentionDays: 7}
			errs := make([]error, 2)
			var wg sync.WaitGroup
			for j := range errs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs[j] = s.PutRaw(ctx, m, []byte(fmt.Sprint("writer ", j)))
				}()
			}
			wg.Wait()
			if (errs[0] == nil) == (errs[1] == nil) {
				t.Fatalf("two writes at once: %v", errs)
			}
			winner := 0
			if errs[0] != nil {
				winner = 1
			}
			if got, err := s.ReadRaw(ctx, "s16", m.ID, 100); err != nil || string(got) != fmt.Sprint("writer ", winner) {
				t.Fatalf("after two writes at once: %q, %v (errors %v)", got, err, errs)
			}
		}
	})
}

// failItemDelete makes the next delete of a message's item fail.
func failItemDelete(s partsStore, err error) (undo func()) {
	switch s := s.(type) {
	case *Memory:
		s.Interrupt = func(_ context.Context, step Step, _, _ string) error {
			if step == StepDeleteItem {
				return err
			}
			return nil
		}
		return func() { s.Interrupt = nil }
	case *AWS:
		db := s.DB
		s.DB = &failingDeletes{DynamoAPI: db, err: err}
		return func() { s.DB = db }
	}
	panic("a store this test does not know")
}

type failingDeletes struct {
	DynamoAPI
	err error
}

func (f *failingDeletes) DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return nil, f.err
}

// countBodies counts what the next reads take from S3's answers.
func countBodies(s partsStore) (read func() int64) {
	a, ok := s.(*AWS)
	if !ok {
		return func() int64 { return 0 }
	}
	counting := &countingS3{S3API: a.S3}
	a.S3 = counting
	return func() int64 {
		a.S3 = counting.S3API
		return counting.n.Load()
	}
}

type countingS3 struct {
	S3API
	n atomic.Int64
}

func (c *countingS3) GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	out, err := c.S3API.GetObject(ctx, in, opts...)
	if err == nil {
		out.Body = &countingBody{ReadCloser: out.Body, n: &c.n}
	}
	return out, err
}

type countingBody struct {
	io.ReadCloser
	n *atomic.Int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n.Add(int64(n))
	return n, err
}

func lastModified(t *testing.T, s partsStore, s3c *s3.Client, m *Message) time.Time {
	t.Helper()
	a, ok := s.(*AWS)
	if !ok {
		return time.Time{}
	}
	out, err := s3c.HeadObject(context.Background(), &s3.HeadObjectInput{Bucket: aws.String(a.Bucket), Key: aws.String(rawKey(m.MailboxID, m.ID))})
	if err != nil {
		t.Fatal(err)
	}
	return aws.ToTime(out.LastModified)
}

// S11 (ADR 0026): DynamoDB stops a page at 1 MB of items read, whatever the
// limit. Forty entries of 30 KiB: a scan of 100 stops near the thirty-third
// with More set and fewer than 100 read; going on below Last reads the rest,
// and none is skipped.
func TestS11APageCutAtOneMegabyte(t *testing.T) {
	s, _ := emulated(t)
	ctx := context.Background()
	value := strings.Repeat("x", 30<<10)
	for i := range 40 {
		if err := s.PutEntry(ctx, &Entry{Partition: "s11", Key: fmt.Sprintf("txt#%02d", i), Value: []byte(value)}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ScanEntriesDown(ctx, "s11", "txt#", "", 100)
	if err != nil || first.Read >= 40 || first.Read < 30 || len(first.Entries) != first.Read || !first.More || first.Last != first.Entries[len(first.Entries)-1].Key {
		t.Fatalf("first page: read %d, %d entries, more %v, %v", first.Read, len(first.Entries), first.More, err)
	}
	seen := map[string]bool{}
	for _, e := range first.Entries {
		seen[e.Key] = true
	}
	rest, err := s.ScanEntriesDown(ctx, "s11", "txt#", first.Last, 100)
	if err != nil || rest.More || rest.Read != 40-first.Read {
		t.Fatalf("the rest: read %d, more %v, %v", rest.Read, rest.More, err)
	}
	for _, e := range rest.Entries {
		seen[e.Key] = true
	}
	if len(seen) != 40 {
		t.Fatalf("%d of 40 entries read", len(seen))
	}
	t.Logf("DynamoDB Local cut the first page at %d entries of 30 KiB", first.Read)
}

// A scan that DynamoDB refuses is an error, never an empty page.
func TestAWSScanFailures(t *testing.T) {
	down := errors.New("dynamodb unreachable")
	s := &AWS{DB: &brokenDB{query: func() (*dynamodb.QueryOutput, error) { return nil, down }}, Table: "t"}
	if _, err := s.ScanEntriesDown(context.Background(), "p", "", "", 10); !errors.Is(err, down) {
		t.Errorf("entries: %v", err)
	}
	if _, err := s.ScanMessagesDown(context.Background(), "m", "", 10); !errors.Is(err, down) {
		t.Errorf("messages: %v", err)
	}
}

// S17 (ADR 0026): Memory.Interrupt. A delivery's writes stopped before each
// of its steps leave exactly the steps before it, and a read held by
// Interrupt returns the context's error when the context ends.
func TestS17InterruptStopsTheStoreBetweenRequests(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	m := &Message{MailboxID: "mbx", ID: "0001", ReceivedAt: now, ExpiresAt: now.Add(time.Hour), RetentionDays: 7}
	deliver := []func(s *Memory) error{
		func(s *Memory) error { return s.PutRaw(ctx, m, []byte("raw")) },
		func(s *Memory) error {
			return s.PutEntry(ctx, &Entry{Partition: "mailbox#mbx", Key: "txt#0001", Value: []byte("search")})
		},
		func(s *Memory) error { return s.PutItem(ctx, m) },
		func(s *Memory) error {
			return s.PutEntry(ctx, &Entry{Partition: "mailbox#mbx", Key: "seen#d", Value: []byte("stored")})
		},
	}
	there := func(s *Memory) []bool {
		_, raw := s.ReadRaw(ctx, "mbx", "0001", 10)
		_, txt := s.GetEntry(ctx, "mailbox#mbx", "txt#0001")
		_, item := s.Item(ctx, "mbx", "0001")
		_, seen := s.GetEntry(ctx, "mailbox#mbx", "seen#d")
		return []bool{raw == nil, txt == nil, item == nil, seen == nil}
	}
	stopped := errors.New("stopped")
	for stop := 0; stop <= len(deliver); stop++ {
		s := NewMemory()
		s.Now = func() time.Time { return now }
		calls := 0
		s.Interrupt = func(context.Context, Step, string, string) error {
			calls++
			if calls == stop+1 {
				return stopped
			}
			return nil
		}
		for i, write := range deliver {
			err := write(s)
			if i < stop && err != nil || i == stop && !errors.Is(err, stopped) {
				t.Fatalf("stopped before %d: write %d: %v", stop, i, err)
			}
			if i == stop {
				break
			}
		}
		s.Interrupt = nil
		got := there(s)
		for i := range got {
			if got[i] != (i < stop) {
				t.Fatalf("stopped before step %d: what is there %v", stop, got)
			}
		}
	}

	// A read held until its context ends.
	s := NewMemory()
	held := make(chan struct{})
	defer close(held)
	s.Interrupt = func(context.Context, Step, string, string) error { <-held; return nil }
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := s.Item(short, "mbx", "0001"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a held read: %v", err)
	}
}

// Every request of Memory goes through Interrupt, with what it is about: a
// failure there fails the request.
func TestInterruptSeesEveryRequest(t *testing.T) {
	ctx := context.Background()
	s := NewMemory()
	var got string
	failed := errors.New("failed")
	s.Interrupt = func(_ context.Context, step Step, partition, key string) error {
		got = fmt.Sprintf("%s %s %s", step, partition, key)
		return failed
	}
	m := &Message{MailboxID: "mbx", ID: "0001"}
	e := &Entry{Partition: "p", Key: "k"}
	for _, c := range []struct {
		want string
		call func() error
	}{
		{"read system record#tls", func() error { _, err := s.GetRecord(ctx, "tls"); return err }},
		{"record system record#tls", func() error { return s.PutRecord(ctx, &Record{Name: "tls"}, false) }},
		{"read system record#tls@", func() error { _, err := s.ListRecords(ctx, "tls@", 10); return err }},
		{"record system record#tls@1", func() error { return s.DeleteRecord(ctx, "tls@1") }},
		{"read mailbox#mbx mailbox", func() error { _, err := s.GetMailbox(ctx, "mbx"); return err }},
		{"record mailbox#mbx mailbox", func() error { return s.PutMailbox(ctx, &Mailbox{ID: "mbx"}) }},
		{"raw mailbox#mbx msg#0001", func() error { return s.PutMessage(ctx, m, nil) }},
		{"raw mailbox#mbx msg#0001", func() error { return s.PutRaw(ctx, m, nil) }},
		{"item mailbox#mbx msg#0001", func() error { return s.PutItem(ctx, m) }},
		{"read mailbox#mbx msg#0001", func() error { _, err := s.Item(ctx, "mbx", "0001"); return err }},
		{"read mailbox#mbx msg#0001", func() error { _, err := s.ReadRaw(ctx, "mbx", "0001", 1); return err }},
		{"read mailbox#mbx msg#0001", func() error { _, _, err := s.GetMessage(ctx, "mbx", "0001"); return err }},
		{"read mailbox#mbx msg#", func() error { _, err := s.ListMessages(ctx, "mbx", 1, ""); return err }},
		{"read mailbox#mbx msg#", func() error { _, err := s.ScanMessagesDown(ctx, "mbx", "", 1); return err }},
		{"delete-raw mailbox#mbx msg#0001", func() error { return s.DeleteMessage(ctx, "mbx", "0001") }},
		{"read p k", func() error { _, err := s.GetEntry(ctx, "p", "k"); return err }},
		{"entry p k", func() error { return s.PutEntry(ctx, e) }},
		{"delete-entry p k", func() error { return s.DeleteEntry(ctx, "p", "k") }},
		{"delete-entry p k", func() error { return s.DeleteEntryAt(ctx, e) }},
		{"read p txt#", func() error { _, err := s.ListEntries(ctx, "p", "txt#", "", 1); return err }},
		{"read p txt#", func() error { _, err := s.ScanEntriesDown(ctx, "p", "txt#", "", 1); return err }},
		{"read p ", func() error { return s.DeletePartition(ctx, "p") }},
	} {
		got = ""
		if err := c.call(); !errors.Is(err, failed) || got != c.want {
			t.Errorf("%s: %q, %v", c.want, got, err)
		}
	}

	// The second request of a pair, and each entry of a partition.
	s = NewMemory()
	if err := s.PutMessage(ctx, &Message{MailboxID: "mbx", ID: "0001", ExpiresAt: time.Now().Add(time.Hour)}, []byte("raw")); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"b", "a"} {
		if err := s.PutEntry(ctx, &Entry{Partition: "p", Key: k}); err != nil {
			t.Fatal(err)
		}
	}
	var steps []string
	s.Interrupt = func(_ context.Context, step Step, _, key string) error {
		steps = append(steps, fmt.Sprintf("%s %s", step, key))
		if len(steps) == 2 {
			return failed
		}
		return nil
	}
	for _, call := range []func() error{
		func() error { _, _, err := s.GetMessage(ctx, "mbx", "0001"); return err },
		func() error { return s.PutMessage(ctx, &Message{MailboxID: "mbx", ID: "0002"}, nil) },
		func() error { return s.DeletePartition(ctx, "p") },
	} {
		steps = nil
		if err := call(); !errors.Is(err, failed) {
			t.Errorf("%v: %v", steps, err)
		}
	}
	if fmt.Sprint(steps) != "[read  delete-entry a]" {
		t.Errorf("a partition deleted: %v", steps)
	}
	s.Interrupt = nil
	if _, err := s.GetEntry(ctx, "p", "b"); err != nil {
		t.Errorf("what was not reached was deleted: %v", err)
	}
}

// conflictingS3 answers every write as S3 does while another conditional
// write of the same key is under way.
type conflictingS3 struct{ S3API }

func (conflictingS3) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return nil, &smithy.GenericAPIError{Code: "ConditionalRequestConflict", Message: "A conflicting conditional operation is currently in progress"}
}

// S16: S3's 409 for two conditional writes at once is an error to try again,
// never ErrExists: the other write may yet fail and leave nothing.
func TestS16AConflictIsNotAnObjectThatExists(t *testing.T) {
	s := &AWS{S3: conflictingS3{}, Bucket: "b"}
	err := s.PutRaw(context.Background(), &Message{MailboxID: "m", ID: "1", RetentionDays: 7}, []byte("raw"))
	if err == nil || errors.Is(err, ErrExists) {
		t.Fatalf("got %v", err)
	}
}

// brokenS3 is S3 that fails every request it is asked, or whose answers fail
// while they are read.
type brokenS3 struct {
	S3API
	err     error
	badBody bool
	deletes int
}

func (b *brokenS3) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return nil, b.err
}

func (b *brokenS3) GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if b.badBody {
		return &s3.GetObjectOutput{Body: io.NopCloser(io.MultiReader(bytes.NewReader([]byte("part")), iotestErr{b.err}))}, nil
	}
	return nil, b.err
}

func (b *brokenS3) DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	b.deletes++
	return nil, b.err
}

type iotestErr struct{ err error }

func (e iotestErr) Read([]byte) (int, error) { return 0, e.err }

// A message's parts on storage that fails: an error, never an answer that
// there is nothing, and a delete that fails at the raw object leaves the
// item for the next try.
func TestAWSPartsFailures(t *testing.T) {
	ctx := context.Background()
	down := errors.New("unreachable")
	db := &brokenDB{put: down, delete: errors.New("the item must not be deleted before the raw object")}
	s3b := &brokenS3{err: down}
	s := &AWS{DB: db, S3: s3b, Table: "t", Bucket: "b"}
	m := &Message{MailboxID: "m", ID: "1", RetentionDays: 7}
	if err := s.PutRaw(ctx, m, []byte("raw")); !errors.Is(err, down) || errors.Is(err, ErrExists) {
		t.Errorf("put raw: %v", err)
	}
	if err := s.PutItem(ctx, m); !errors.Is(err, down) {
		t.Errorf("put item: %v", err)
	}
	if _, err := s.ReadRaw(ctx, "m", "1", 10); !errors.Is(err, down) || errors.Is(err, ErrNotFound) {
		t.Errorf("read raw: %v", err)
	}
	s3b.badBody = true
	if _, err := s.ReadRaw(ctx, "m", "1", 10); !errors.Is(err, down) {
		t.Errorf("read raw, failing while read: %v", err)
	}
	if err := s.DeleteMessage(ctx, "m", "1"); !errors.Is(err, down) || s3b.deletes != 1 {
		t.Errorf("delete: %v, %d raw deletes", err, s3b.deletes)
	}
	s.DB = &failingGets{DynamoAPI: db, err: down}
	if _, err := s.Item(ctx, "m", "1"); !errors.Is(err, down) || errors.Is(err, ErrNotFound) {
		t.Errorf("item: %v", err)
	}
}

type failingGets struct {
	DynamoAPI
	err error
}

func (f *failingGets) GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return nil, f.err
}

// PR #91's review, finding 4: on DynamoDB a mailbox's messages and its
// entries share a partition, so a listing of entries without a prefix reads
// the message rows too; the memory store keeps them apart. The sweep lists
// entries only by their kinds' prefixes, and deletes messages only through
// DeleteMessage, the raw object first (services/mailboxes, clean).
func TestReview4ListingEntriesWithoutAPrefixReadsMessages(t *testing.T) {
	s, _ := emulated(t)
	ctx := context.Background()
	m := &Message{MailboxID: "mbx_x", ID: strings.Repeat("a", 32), ReceivedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), Size: 1, RetentionDays: 90, Meta: []byte("m")}
	if err := s.PutRaw(ctx, m, []byte("raw")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutItem(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := s.PutEntry(ctx, &Entry{Partition: "mailbox#mbx_x", Key: "txt#" + m.ID, Value: []byte("t")}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListEntries(ctx, "mailbox#mbx_x", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, e := range all {
		keys = append(keys, e.Key)
	}
	if len(keys) != 2 || keys[0] != "msg#"+m.ID || keys[1] != "txt#"+m.ID {
		t.Fatalf("%v", keys)
	}
	kind, err := s.ListEntries(ctx, "mailbox#mbx_x", "txt#", "", 10)
	if err != nil || len(kind) != 1 {
		t.Fatalf("%v %v", kind, err)
	}
}
