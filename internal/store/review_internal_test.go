package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddb "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// fakeAWS is DynamoDB and S3 that write down every request and answer with
// nothing, or with what they are told: raw is every object's body, query
// every Query's answer.
type fakeAWS struct {
	requests []string
	raw      []byte
	query    *dynamodb.QueryOutput
}

func (f *fakeAWS) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.requests = append(f.requests, "get-item")
	return &dynamodb.GetItemOutput{}, nil
}

func (f *fakeAWS) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.requests = append(f.requests, "put-item")
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeAWS) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	f.requests = append(f.requests, "delete-item")
	return &dynamodb.DeleteItemOutput{}, nil
}

func (f *fakeAWS) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.requests = append(f.requests, "query")
	if f.query != nil {
		return f.query, nil
	}
	return &dynamodb.QueryOutput{}, nil
}

func (f *fakeAWS) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.requests = append(f.requests, "put-object "+aws.ToString(in.Key))
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeAWS) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.requests = append(f.requests, "get-object "+aws.ToString(in.Key))
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(f.raw))}, nil
}

func (f *fakeAWS) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.requests = append(f.requests, "delete-object "+aws.ToString(in.Key))
	return &s3.DeleteObjectOutput{}, nil
}

// Review 6: a mailbox's or a message's identifier that is empty or holds
// anything but letters, digits, _ and - makes no key: ("a/b", "c") and ("a",
// "b/c") would name one raw object. The store refuses it before any request,
// for raw objects and items alike, with an error that does not hold it; a
// read or a delete of one answers ErrNotFound (nothing can be stored under
// it). The identifiers the service makes are taken as before.
func TestReview6IdentifiersThatMakeNoKey(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

	// The alias, in memory: deleting the second removed the first's raw object.
	m := NewMemory()
	m.Now = func() time.Time { return now }
	if err := m.PutRaw(ctx, &Message{MailboxID: "a/b", ID: "c"}, []byte("first")); err == nil {
		t.Fatal("a raw object stored under a mailbox with a /")
	}
	if err := m.DeleteMessage(ctx, "a", "b/c"); err == nil {
		t.Fatal("a delete with a / in the message's identifier")
	}

	type op struct {
		name  string
		read  bool
		run   func(s partsStore, mailbox, id string) error
		boxed bool // takes the mailbox only
	}
	ops := []op{
		{"PutRaw", false, func(s partsStore, mb, id string) error {
			return s.PutRaw(ctx, &Message{MailboxID: mb, ID: id, RetentionDays: 30}, []byte("raw"))
		}, false},
		{"PutItem", false, func(s partsStore, mb, id string) error {
			return s.PutItem(ctx, &Message{MailboxID: mb, ID: id, ExpiresAt: now.Add(time.Hour)})
		}, false},
		{"PutMessage", false, func(s partsStore, mb, id string) error {
			return s.PutMessage(ctx, &Message{MailboxID: mb, ID: id, ExpiresAt: now.Add(time.Hour)}, []byte("raw"))
		}, false},
		{"Item", true, func(s partsStore, mb, id string) error { _, err := s.Item(ctx, mb, id); return err }, false},
		{"ReadRaw", true, func(s partsStore, mb, id string) error { _, err := s.ReadRaw(ctx, mb, id, 100); return err }, false},
		{"GetMessage", true, func(s partsStore, mb, id string) error { _, _, err := s.GetMessage(ctx, mb, id); return err }, false},
		{"DeleteMessage", true, func(s partsStore, mb, id string) error { return s.DeleteMessage(ctx, mb, id) }, false},
		{"GetMailbox", true, func(s partsStore, mb, _ string) error { _, err := s.GetMailbox(ctx, mb); return err }, true},
		{"PutMailbox", false, func(s partsStore, mb, _ string) error { return s.PutMailbox(ctx, &Mailbox{ID: mb}) }, true},
		{"ListMessages", true, func(s partsStore, mb, _ string) error { _, err := s.ListMessages(ctx, mb, 10, ""); return err }, true},
		{"ScanMessagesDown", true, func(s partsStore, mb, _ string) error { _, err := s.ScanMessagesDown(ctx, mb, "", 10); return err }, true},
	}
	bad := []string{"", "a/SECRET", "SECRET.x", ".", "..", "SECRET#x", "SECRET x", "SECRETé", "SECRET\x00"}
	good := []string{"test", "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa", "0000000000000001aaaaaaaaaaaaaaaa", "mb-none", "Z9"}
	for _, kind := range []string{"memory", "aws"} {
		for _, o := range ops {
			for _, id := range bad {
				for _, where := range []string{"mailbox", "message"} {
					if where == "message" && o.boxed {
						continue
					}
					mb, msg := "test", "0001"
					if where == "mailbox" {
						mb = id
					} else {
						msg = id
					}
					var s partsStore
					f := &fakeAWS{}
					if kind == "memory" {
						mem := NewMemory()
						mem.Now = func() time.Time { return now }
						s = mem
					} else {
						s = &AWS{DB: f, S3: f, Table: "t", Bucket: "b", Now: func() time.Time { return now }}
					}
					err := o.run(s, mb, msg)
					if err == nil || strings.Contains(err.Error(), "SECRET") || (o.read && !errors.Is(err, ErrNotFound)) || len(f.requests) != 0 {
						t.Fatalf("%s, %s %q as the %s: %v, requests %q", kind, o.name, id, where, err, f.requests)
					}
				}
			}
		}
		for _, id := range good {
			f := &fakeAWS{raw: []byte("raw")}
			var s partsStore = &AWS{DB: f, S3: f, Table: "t", Bucket: "b", Now: func() time.Time { return now }}
			if kind == "memory" {
				mem := NewMemory()
				mem.Now = func() time.Time { return now }
				s = mem
			}
			if err := s.PutRaw(ctx, &Message{MailboxID: id, ID: id, RetentionDays: 30}, []byte("raw")); err != nil {
				t.Fatalf("%s, %q: %v", kind, id, err)
			}
			if raw, err := s.ReadRaw(ctx, id, id, 100); err != nil || string(raw) != "raw" {
				t.Fatalf("%s, %q: %q, %v", kind, id, raw, err)
			}
			if kind == "aws" && f.requests[0] != "put-object raw/"+id+"/"+id {
				t.Fatalf("key %q", f.requests[0])
			}
		}
	}
}

// Review 7: ReadRaw's bound, the same in memory and on AWS: zero takes an
// empty object only, a negative one is an error, the ceiling (maxRawRead)
// reads, and anything above it, math.MaxInt64 included, is an error that is
// not ErrTooLarge. Before the fix AWS answered math.MaxInt64 with nothing
// (max+1 overflowed) and memory with the bytes.
func TestReview7ReadRawsBound(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"memory", "aws"} {
		var s partsStore
		if kind == "memory" {
			mem := NewMemory()
			if err := mem.PutRaw(ctx, &Message{MailboxID: "test", ID: "0001"}, []byte("hello")); err != nil {
				t.Fatal(err)
			}
			s = mem
		} else {
			f := &fakeAWS{raw: []byte("hello")}
			s = &AWS{DB: f, S3: f, Table: "t", Bucket: "b"}
		}
		for _, c := range []struct {
			max  int64
			want string
		}{
			{0, "too large"}, {4, "too large"}, {5, "hello"}, {-1, "error"},
			{maxRawRead, "hello"}, {maxRawRead + 1, "error"}, {math.MaxInt64, "error"},
		} {
			raw, err := s.ReadRaw(ctx, "test", "0001", c.max)
			got := string(raw)
			switch {
			case errors.Is(err, ErrTooLarge):
				got = "too large"
			case err != nil && !errors.Is(err, ErrNotFound):
				got = "error"
			case err != nil:
				got = err.Error()
			}
			if got != c.want {
				t.Fatalf("%s, ReadRaw(%d): %q, %v", kind, c.max, raw, err)
			}
		}
	}
}

// Review C: Memory cuts a scan's page short when told to (PageItems), as
// DynamoDB cuts one at 1 MB: fewer items read than asked, More set, and a
// page of expired items only returns nothing with More. Going on below Last
// reads every item once.
func TestReviewCMemoryCutsAPageShort(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	m := NewMemory()
	m.Now = func() time.Time { return now }
	m.PageItems = 3
	for i := range 10 {
		expires := now.Add(time.Hour)
		if i >= 7 {
			expires = now.Add(-time.Hour)
		}
		if err := m.PutItem(ctx, &Message{MailboxID: "test", ID: fmt.Sprintf("%02d", i), ExpiresAt: expires}); err != nil {
			t.Fatal(err)
		}
		if err := m.PutEntry(ctx, &Entry{Partition: "p", Key: fmt.Sprintf("k%02d", i), Value: []byte("v")}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := m.ScanMessagesDown(ctx, "test", "", 100)
	if err != nil || first.Read != 3 || len(first.Messages) != 0 || !first.More || first.Last != "07" {
		t.Fatalf("first page %+v, %v", first, err)
	}
	var seen []string
	reads := []int{first.Read}
	for page := first; page.More; {
		if page, err = m.ScanMessagesDown(ctx, "test", page.Last, 100); err != nil {
			t.Fatal(err)
		}
		reads = append(reads, page.Read)
		for _, msg := range page.Messages {
			seen = append(seen, msg.ID)
		}
	}
	if fmt.Sprint(seen) != "[06 05 04 03 02 01 00]" || fmt.Sprint(reads) != "[3 3 3 1]" {
		t.Fatalf("read %v in pages of %v", seen, reads)
	}
	entries, err := m.ScanEntriesDown(ctx, "p", "k", "", 100)
	if err != nil || entries.Read != 3 || !entries.More || entries.Last != "k07" {
		t.Fatalf("entries %+v, %v", entries, err)
	}
	// A cut above the page's limit changes nothing.
	m.PageItems = 50
	if all, err := m.ScanEntriesDown(ctx, "p", "k", "", 4); err != nil || all.Read != 4 || !all.More {
		t.Fatalf("limited by maxRead %+v, %v", all, err)
	}
}

// Review E: on AWS, More is DynamoDB's LastEvaluatedKey being set, nothing
// derived from what was read: a page that read its limit with no key after
// it has no More; a page that read less, or nothing, with a key after it has
// More.
func TestReviewEMoreIsLastEvaluatedKey(t *testing.T) {
	ctx := context.Background()
	item := func(k string) map[string]ddb.AttributeValue {
		return map[string]ddb.AttributeValue{"pk": str("p"), "sk": str(k), "text": str("v")}
	}
	page := func(n int) []map[string]ddb.AttributeValue {
		out := make([]map[string]ddb.AttributeValue, n)
		for i := range out {
			out[i] = item(fmt.Sprintf("k%03d", n-i))
		}
		return out
	}
	for _, c := range []struct {
		read int
		last bool
		more bool
	}{{100, false, false}, {3, true, true}, {0, true, true}, {100, true, true}, {3, false, false}} {
		out := &dynamodb.QueryOutput{Items: page(c.read), Count: int32(c.read), ScannedCount: int32(c.read)} //nolint:gosec // small
		if c.last {
			out.LastEvaluatedKey = item("k000")
		}
		f := &fakeAWS{query: out}
		s := &AWS{DB: f, S3: f, Table: "t", Bucket: "b"}
		got, err := s.ScanEntriesDown(ctx, "p", "k", "", 100)
		if err != nil || got.More != c.more || got.Read != c.read {
			t.Fatalf("%+v: %+v, %v", c, got, err)
		}
	}
}
