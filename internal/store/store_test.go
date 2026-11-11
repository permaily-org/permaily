package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddb "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// contract checks the behavior every Store must have.
func contract(t *testing.T, s interface {
	Store
	Records
	Entries
}, setNow func(time.Time)) {
	ctx := context.Background()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	setNow(now)

	if _, err := s.GetRecord(ctx, "tls"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing record: %v", err)
	}
	rec := &Record{Name: "tls", WrappedKey: []byte("wk"), Value: []byte("v1"), UpdatedAt: now}
	if err := s.PutRecord(ctx, rec, true); err != nil {
		t.Fatal(err)
	}
	// A second enclave that also found nothing must not overwrite the first one's record.
	if err := s.PutRecord(ctx, &Record{Name: "tls", WrappedKey: []byte("wk2"), Value: []byte("other"), UpdatedAt: now}, true); !errors.Is(err, ErrExists) {
		t.Fatalf("second create: %v, want ErrExists", err)
	}
	rec.Value = []byte("v2")
	if err := s.PutRecord(ctx, rec, false); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetRecord(ctx, "tls"); err != nil || !bytes.Equal(got.Value, []byte("v2")) || !bytes.Equal(got.WrappedKey, []byte("wk")) || !got.UpdatedAt.Equal(now) {
		t.Fatalf("record round trip: %+v %v", got, err)
	}
	if _, err := s.GetRecord(ctx, "other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another record: %v", err)
	}

	// A readable record comes back as it was written, and marked as such.
	plain := &Record{Name: "account-1", Value: []byte(`{"doc":{"state":"active"},"mac":"AA=="}`), Plain: true, UpdatedAt: now}
	if err := s.PutRecord(ctx, plain, true); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetRecord(ctx, "account-1"); err != nil || !got.Plain || !bytes.Equal(got.Value, plain.Value) || len(got.WrappedKey) != 0 {
		t.Fatalf("readable record: %+v %v", got, err)
	}
	if got, err := s.GetRecord(ctx, "tls"); err != nil || got.Plain {
		t.Fatalf("a sealed record came back as readable: %v", err)
	}
	entries(t, s, setNow, now)

	if _, err := s.GetMailbox(ctx, "mb"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing mailbox: %v", err)
	}
	mb := &Mailbox{ID: "mb", WrappedKey: []byte("wk"), RetentionDays: 90, State: []byte("st"), UpdatedAt: now}
	if err := s.PutMailbox(ctx, mb); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMailbox(ctx, "mb")
	if err != nil || !bytes.Equal(got.WrappedKey, []byte("wk")) || got.RetentionDays != 90 || !bytes.Equal(got.State, []byte("st")) {
		t.Fatalf("mailbox round trip: %+v %v", got, err)
	}

	for i := range 5 {
		m := &Message{
			MailboxID: "mb", ID: fmt.Sprintf("0000000%d", i), ReceivedAt: now, Size: 10 + i,
			ExpiresAt: now.Add(time.Hour), RetentionDays: 90, Meta: []byte{byte(i)},
		}
		if i == 4 {
			m.ExpiresAt = now.Add(-time.Second) // already expired
		}
		if err := s.PutMessage(ctx, m, []byte(fmt.Sprintf("raw%d", i))); err != nil {
			t.Fatal(err)
		}
	}

	page, err := s.ListMessages(ctx, "mb", 2, "")
	if err != nil || len(page) != 2 || page[0].ID != "00000003" || page[1].ID != "00000002" {
		t.Fatalf("first page (newest first, expired hidden): %+v %v", ids(page), err)
	}
	page, err = s.ListMessages(ctx, "mb", 10, "00000002")
	if err != nil || len(page) != 2 || page[0].ID != "00000001" || page[1].ID != "00000000" {
		t.Fatalf("second page: %+v %v", ids(page), err)
	}

	m, raw, err := s.GetMessage(ctx, "mb", "00000001")
	if err != nil || string(raw) != "raw1" || m.Size != 11 || !bytes.Equal(m.Meta, []byte{1}) {
		t.Fatalf("get: %+v %q %v", m, raw, err)
	}
	if _, _, err := s.GetMessage(ctx, "mb", "00000004"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired message: %v", err)
	}
	if _, _, err := s.GetMessage(ctx, "other", "00000001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other mailbox: %v", err)
	}

	if err := s.DeleteMessage(ctx, "mb", "00000001"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetMessage(ctx, "mb", "00000001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted message still readable: %v", err)
	}
	if err := s.DeleteMessage(ctx, "mb", "00000001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

// entries checks the behavior every Entries must have.
func entries(t *testing.T, s Entries, setNow func(time.Time), now time.Time) {
	ctx := context.Background()
	if _, err := s.GetEntry(ctx, "account#1", "session#a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing entry: %v", err)
	}
	a := &Entry{Partition: "account#1", Key: "session#a", Value: []byte(`{"n":1}`), UpdatedAt: now}
	if err := s.PutEntry(ctx, a); err != nil || a.Version != 1 {
		t.Fatalf("create: version %d, %v", a.Version, err)
	}
	if err := s.PutEntry(ctx, &Entry{Partition: "account#1", Key: "session#a", Value: []byte(`{}`), UpdatedAt: now}); !errors.Is(err, ErrExists) {
		t.Fatalf("second create: %v, want ErrExists", err)
	}
	// Two readers of the same version: the second write is refused.
	first, _ := s.GetEntry(ctx, "account#1", "session#a")
	second, _ := s.GetEntry(ctx, "account#1", "session#a")
	first.Value = []byte(`{"n":2}`)
	if err := s.PutEntry(ctx, first); err != nil || first.Version != 2 {
		t.Fatalf("replace: version %d, %v", first.Version, err)
	}
	second.Value = []byte(`{"n":3}`)
	if err := s.PutEntry(ctx, second); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale replace: %v, want ErrConflict", err)
	}
	if err := s.PutEntry(ctx, &Entry{Partition: "account#1", Key: "session#none", Version: 4, UpdatedAt: now}); !errors.Is(err, ErrConflict) {
		t.Fatalf("replace of nothing: %v, want ErrConflict", err)
	}
	if got, err := s.GetEntry(ctx, "account#1", "session#a"); err != nil || string(got.Value) != `{"n":2}` || got.Version != 2 || !got.ExpiresAt.IsZero() || !got.UpdatedAt.Equal(now) {
		t.Fatalf("entry round trip: %+v %v", got, err)
	}

	// Listed by partition and prefix, in order; an expired one is not there.
	for _, e := range []*Entry{
		{Partition: "account#1", Key: "session#c", Value: []byte(`{}`), ExpiresAt: now.Add(time.Hour), UpdatedAt: now},
		{Partition: "account#1", Key: "session#b", Value: []byte(`{}`), ExpiresAt: now.Add(time.Minute), UpdatedAt: now},
		{Partition: "account#1", Key: "other", Value: []byte(`{}`), UpdatedAt: now},
		{Partition: "account#2", Key: "session#z", Value: []byte(`{}`), UpdatedAt: now},
	} {
		if err := s.PutEntry(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	keys := func(list []Entry) (out []string) {
		for _, e := range list {
			out = append(out, e.Key)
		}
		return out
	}
	if list, err := s.ListEntries(ctx, "account#1", "session#", 10); err != nil || fmt.Sprint(keys(list)) != "[session#a session#b session#c]" {
		t.Fatalf("list: %v %v", keys(list), err)
	}
	if list, err := s.ListEntries(ctx, "account#1", "session#", 2); err != nil || len(list) != 2 {
		t.Fatalf("limited list: %v %v", keys(list), err)
	}
	if got, err := s.GetEntry(ctx, "account#1", "session#c"); err != nil || !got.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("expiry round trip: %+v %v", got, err)
	}
	setNow(now.Add(2 * time.Minute))
	if _, err := s.GetEntry(ctx, "account#1", "session#b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired entry: %v", err)
	}
	if list, err := s.ListEntries(ctx, "account#1", "session#", 10); err != nil || fmt.Sprint(keys(list)) != "[session#a session#c]" {
		t.Fatalf("list after expiry: %v %v", keys(list), err)
	}
	// An expired entry can be made again, and deleting it deletes nothing.
	if err := s.DeleteEntry(ctx, "account#1", "session#b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete of an expired entry: %v", err)
	}
	again := &Entry{Partition: "account#1", Key: "session#b", Value: []byte(`{}`), UpdatedAt: now}
	if err := s.PutEntry(ctx, again); err != nil {
		t.Fatalf("create over nothing: %v", err)
	}
	setNow(now)

	// Of two deletes, one succeeds.
	if err := s.DeleteEntry(ctx, "account#1", "session#a"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEntry(ctx, "account#1", "session#a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := s.GetEntry(ctx, "account#1", "session#a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted entry still there: %v", err)
	}
}

func ids(ms []Message) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func TestMemory(t *testing.T) {
	s := NewMemory()
	contract(t, s, func(n time.Time) { s.Now = func() time.Time { return n } })
}

func TestValidRetention(t *testing.T) {
	if !ValidRetention(90) || ValidRetention(91) {
		t.Fatal("retention validation")
	}
}

// TestAWS runs the contract against DynamoDB Local and an S3-compatible
// server. Set PERMAILY_TEST_DYNAMODB and PERMAILY_TEST_S3 (endpoints) to run
// it; CI starts both as services.
func TestAWS(t *testing.T) {
	dynEndpoint, s3Endpoint := os.Getenv("PERMAILY_TEST_DYNAMODB"), os.Getenv("PERMAILY_TEST_S3")
	if dynEndpoint == "" || s3Endpoint == "" {
		t.Skip("PERMAILY_TEST_DYNAMODB and PERMAILY_TEST_S3 not set")
	}
	ctx := context.Background()
	cfg := aws.Config{
		Region:      "us-east-2",
		Credentials: credentials.NewStaticCredentialsProvider("test", "testtest", ""),
	}
	db := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(dynEndpoint) })
	s3c := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(s3Endpoint)
		o.UsePathStyle = true
	})
	suffix := fmt.Sprint(time.Now().UnixNano())
	table, bucket := "permaily-test-"+suffix, "permaily-test-"+suffix
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
	s := &AWS{DB: db, S3: s3c, Table: table, Bucket: bucket}
	contract(t, s, func(n time.Time) { s.Now = func() time.Time { return n } })
}
