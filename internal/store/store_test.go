package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
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
	RecordTable
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
	// Records are listed by the start of their name, in order, and deleted
	// one by one: how the versions of an anchored record are found and removed.
	for i := range 120 {
		if err := s.PutRecord(ctx, &Record{Name: fmt.Sprintf("bulk@%03d", i), Value: []byte("v"), UpdatedAt: now}, true); err != nil {
			t.Fatal(err)
		}
	}
	if names, err := s.ListRecords(ctx, "tls", 10); err != nil || fmt.Sprint(names) != "[tls]" {
		t.Fatalf("records by prefix: %v %v", names, err)
	}
	if names, err := s.ListRecords(ctx, "nothing", 10); err != nil || len(names) != 0 {
		t.Fatalf("records of a prefix nothing has: %v %v", names, err)
	}
	if names, err := s.ListRecords(ctx, "bulk@", 110); err != nil || len(names) != 110 || names[0] != "bulk@000" || names[109] != "bulk@109" {
		t.Fatalf("a bounded list of records: %d %v", len(names), err)
	}
	names, err := s.ListRecords(ctx, "bulk@", 1000)
	if err != nil || len(names) != 120 {
		t.Fatalf("every record of a prefix: %d %v", len(names), err)
	}
	for _, name := range names {
		if err := s.DeleteRecord(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.GetRecord(ctx, "bulk@007"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted record: %v", err)
	}
	// Deleting what is not there is not a failure, and touches nothing else.
	if err := s.DeleteRecord(ctx, "bulk@007"); err != nil {
		t.Fatalf("deleting a record again: %v", err)
	}
	if names, err := s.ListRecords(ctx, "", 1000); err != nil || fmt.Sprint(names) != "[account-1 tls]" {
		t.Fatalf("what is left: %v %v", names, err)
	}
	anchoredContract(t, s)
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
	if list, err := s.ListEntries(ctx, "account#1", "session#", "", 10); err != nil || fmt.Sprint(keys(list)) != "[session#a session#b session#c]" {
		t.Fatalf("list: %v %v", keys(list), err)
	}
	if list, err := s.ListEntries(ctx, "account#1", "session#", "", 2); err != nil || len(list) != 2 {
		t.Fatalf("limited list: %v %v", keys(list), err)
	}
	if got, err := s.GetEntry(ctx, "account#1", "session#c"); err != nil || !got.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("expiry round trip: %+v %v", got, err)
	}
	setNow(now.Add(2 * time.Minute))
	if _, err := s.GetEntry(ctx, "account#1", "session#b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired entry: %v", err)
	}
	if list, err := s.ListEntries(ctx, "account#1", "session#", "", 10); err != nil || fmt.Sprint(keys(list)) != "[session#a session#c]" {
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

	// A list goes on after a key, whether an entry is there or not, and stays
	// within its prefix.
	if err := s.PutEntry(ctx, &Entry{Partition: "account#1", Key: "tail", Value: []byte(`{}`), UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for after, want := range map[string]string{
		"":           "[session#a session#b session#c]",
		"other":      "[session#a session#b session#c]", // before every key with the prefix
		"session#":   "[session#a session#b session#c]",
		"session#a":  "[session#b session#c]",
		"session#aa": "[session#b session#c]",
		"session#b":  "[session#c]",
		"session#c":  "[]",
		"session$":   "[]", // past every key with the prefix
		"tail":       "[]",
	} {
		if list, err := s.ListEntries(ctx, "account#1", "session#", after, 10); err != nil || fmt.Sprint(keys(list)) != want {
			t.Errorf("list after %q: %v %v, want %s", after, keys(list), err, want)
		}
	}
	if list, err := s.ListEntries(ctx, "account#1", "", "session#b", 10); err != nil || fmt.Sprint(keys(list)) != "[session#c tail]" {
		t.Fatalf("the whole partition after a key: %v %v", keys(list), err)
	}
	if list, err := s.ListEntries(ctx, "account#1", "", "", 10); err != nil || fmt.Sprint(keys(list)) != "[other session#a session#b session#c tail]" {
		t.Fatalf("the whole partition: %v %v", keys(list), err)
	}
	// Page by page through more than one request holds.
	for i := range 230 {
		if err := s.PutEntry(ctx, &Entry{Partition: "bulk", Key: fmt.Sprintf("k%03d", i), Value: []byte(`{}`), UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	seen, after := 0, ""
	for {
		page, err := s.ListEntries(ctx, "bulk", "k", after, 150)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		if page[0].Key != fmt.Sprintf("k%03d", seen) {
			t.Fatalf("a page starts at %s after %d entries", page[0].Key, seen)
		}
		seen, after = seen+len(page), page[len(page)-1].Key
	}
	if seen != 230 {
		t.Fatalf("%d entries in pages", seen)
	}

	// A partition is deleted whole, the expired entries with it, and no other.
	expired := &Entry{Partition: "bulk", Key: "expired", Value: []byte(`{}`), ExpiresAt: now.Add(-time.Hour), UpdatedAt: now}
	if err := s.PutEntry(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePartition(ctx, "bulk"); err != nil {
		t.Fatal(err)
	}
	if list, err := s.ListEntries(ctx, "bulk", "", "", 1000); err != nil || len(list) != 0 {
		t.Fatalf("a deleted partition: %d entries, %v", len(list), err)
	}
	setNow(now.Add(-2 * time.Hour)) // when the expired one was not: it is gone all the same
	if _, err := s.GetEntry(ctx, "bulk", "expired"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired entry of a deleted partition: %v", err)
	}
	setNow(now)
	if err := s.DeletePartition(ctx, "bulk"); err != nil {
		t.Fatalf("deleting a partition that is empty: %v", err)
	}
	if list, err := s.ListEntries(ctx, "account#2", "", "", 10); err != nil || fmt.Sprint(keys(list)) != "[session#z]" {
		t.Fatalf("another partition: %v %v", keys(list), err)
	}

	// An entry is deleted as it was read only while it is still that one: not
	// once it was written again, and not one made again in its place, which is
	// at the same version with another value.
	pointer := &Entry{Partition: "login#x", Key: "account", Value: []byte(`{"to":1}`), UpdatedAt: now}
	if err := s.PutEntry(ctx, pointer); err != nil {
		t.Fatal(err)
	}
	read, err := s.GetEntry(ctx, "login#x", "account")
	if err != nil {
		t.Fatal(err)
	}
	rewritten := *read
	rewritten.Value = []byte(`{"to":2}`)
	if err := s.PutEntry(ctx, &rewritten); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEntryAt(ctx, read); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete of an entry that was written again: %v", err)
	}
	if err := s.DeleteEntry(ctx, "login#x", "account"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEntryAt(ctx, read); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete of an entry that is gone: %v", err)
	}
	again = &Entry{Partition: "login#x", Key: "account", Value: []byte(`{"to":3}`), UpdatedAt: now}
	if err := s.PutEntry(ctx, again); err != nil || again.Version != read.Version {
		t.Fatalf("made again: version %d, %v", again.Version, err)
	}
	if err := s.DeleteEntryAt(ctx, read); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete of an entry made again in its place: %v", err)
	}
	if got, err := s.GetEntry(ctx, "login#x", "account"); err != nil || string(got.Value) != `{"to":3}` {
		t.Fatalf("the entry made again: %+v, %v", got, err)
	}
	if err := s.DeleteEntryAt(ctx, again); err != nil {
		t.Fatalf("delete of an entry as it is: %v", err)
	}
	if _, err := s.GetEntry(ctx, "login#x", "account"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted entry still there: %v", err)
	}

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

// anchoredContract checks, on the storage itself, what removing an anchored
// record leaves there: nothing, whatever versions were stored.
func anchoredContract(t *testing.T, table RecordTable) {
	t.Helper()
	ctx := context.Background()
	stored := func() []string {
		names, err := table.ListRecords(ctx, "account-x", 100)
		if err != nil {
			t.Fatal(err)
		}
		return names
	}
	// Every version kept, as for the TLS identity, and only the newest.
	for _, onlyNewest := range []bool{false, true} {
		a := &Anchored{Records: table, Commits: NewMemoryCommits(), OnlyNewest: onlyNewest}
		for _, v := range []string{"one", "two", "three"} {
			if err := a.PutRecord(ctx, &Record{Name: "account-x", Value: []byte(v), Plain: true, UpdatedAt: time.Unix(1, 0)}, false); err != nil {
				t.Fatal(err)
			}
		}
		want := 3
		if onlyNewest {
			want = 1
		}
		if names := stored(); len(names) != want || !strings.HasPrefix(names[want-1], "account-x@00000000000000000003-") {
			t.Fatalf("only newest %v: stored %q", onlyNewest, names)
		}
		if got, err := a.GetRecord(ctx, "account-x"); err != nil || string(got.Value) != "three" || !got.Plain {
			t.Fatalf("only newest %v: read %+v %v", onlyNewest, got, err)
		}
		// A second enclave on the same table: of two that read one version, one
		// writes, and the other leaves nothing in the table.
		b := &Anchored{Records: table, Commits: a.Commits, OnlyNewest: onlyNewest}
		_, first, err := a.GetRecordAt(ctx, "account-x")
		if err != nil {
			t.Fatal(err)
		}
		_, second, err := b.GetRecordAt(ctx, "account-x")
		if err != nil {
			t.Fatal(err)
		}
		if err := b.PutRecordAt(ctx, &Record{Name: "account-x", Value: []byte("four"), Plain: true, UpdatedAt: time.Unix(1, 0)}, second); err != nil {
			t.Fatal(err)
		}
		if err := a.PutRecordAt(ctx, &Record{Name: "account-x", Value: []byte("not this"), Plain: true, UpdatedAt: time.Unix(1, 0)}, first); !errors.Is(err, ErrChanged) {
			t.Fatalf("only newest %v: a write over another enclave's: %v", onlyNewest, err)
		}
		if names := stored(); len(names) != want+1-map[bool]int{true: 1}[onlyNewest] || !strings.HasPrefix(names[len(names)-1], "account-x@00000000000000000004-") {
			t.Fatalf("only newest %v: stored after a refused write %q", onlyNewest, names)
		}
		if got, err := a.GetRecord(ctx, "account-x"); err != nil || string(got.Value) != "four" || !got.Plain {
			t.Fatalf("only newest %v: read %+v %v", onlyNewest, got, err)
		}
		if err := a.RemoveRecord(ctx, "account-x"); err != nil {
			t.Fatal(err)
		}
		if names := stored(); len(names) != 0 {
			t.Fatalf("only newest %v: left after a removal: %q", onlyNewest, names)
		}
		if _, err := a.GetRecord(ctx, "account-x"); !errors.Is(err, ErrNotFound) || errors.Is(err, ErrLost) {
			t.Fatalf("only newest %v: a removed record: %v", onlyNewest, err)
		}
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

	// What a test sees and writes of the storage, as whoever runs it does:
	// every entry, the expired ones too, and whatever it puts there.
	s = NewMemory()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	s.SetEntry(Entry{Partition: "b", Key: "1", Value: []byte("x"), Version: 7})
	s.SetEntry(Entry{Partition: "a", Key: "2", Value: []byte("y"), ExpiresAt: now.Add(-time.Hour)})
	s.SetEntry(Entry{Partition: "a", Key: "1", Value: []byte("z")})
	all := s.AllEntries()
	if len(all) != 3 || all[0].Key != "1" || all[0].Partition != "a" || all[1].Key != "2" || all[2].Version != 7 || string(all[2].Value) != "x" {
		t.Fatalf("every entry: %+v", all)
	}
	if list, _ := s.ListEntries(context.Background(), "a", "", "", 10); len(list) != 1 {
		t.Fatalf("an expired entry is listed: %+v", list)
	}
	if err := s.DeletePartition(context.Background(), "a"); err != nil || len(s.AllEntries()) != 1 {
		t.Fatalf("after deleting a partition: %+v %v", s.AllEntries(), err)
	}
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

// brokenDB is DynamoDB that answers what it is told to.
type brokenDB struct {
	DynamoAPI
	query  func() (*dynamodb.QueryOutput, error)
	delete error
}

func (b *brokenDB) Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return b.query()
}

func (b *brokenDB) DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, b.delete
}

// A table that fails is an error for whoever lists or deletes, never an
// answer that there is nothing.
func TestAWSListAndDeleteFailures(t *testing.T) {
	ctx := context.Background()
	down := errors.New("dynamodb unreachable")
	db := &brokenDB{query: func() (*dynamodb.QueryOutput, error) { return nil, down }, delete: down}
	s := &AWS{DB: db, Table: "t"}
	if _, err := s.ListRecords(ctx, "x@", 10); !errors.Is(err, down) {
		t.Errorf("list records: %v", err)
	}
	if err := s.DeleteRecord(ctx, "x@1"); !errors.Is(err, down) {
		t.Errorf("delete record: %v", err)
	}
	if _, err := s.ListEntries(ctx, "p", "", "k", 10); !errors.Is(err, down) {
		t.Errorf("list entries: %v", err)
	}
	if err := s.DeletePartition(ctx, "p"); !errors.Is(err, down) {
		t.Errorf("delete partition, listing: %v", err)
	}
	// The partition lists, and an entry of it cannot be deleted.
	item := map[string]ddb.AttributeValue{"pk": str("p"), "sk": str("k")}
	db.query = func() (*dynamodb.QueryOutput, error) {
		return &dynamodb.QueryOutput{Items: []map[string]ddb.AttributeValue{item}}, nil
	}
	if err := s.DeletePartition(ctx, "p"); !errors.Is(err, down) {
		t.Errorf("delete partition, deleting: %v", err)
	}
	// A partition that someone fills as fast as it is emptied: it ends.
	db.delete = nil
	page := make([]map[string]ddb.AttributeValue, 100)
	for i := range page {
		page[i] = item
	}
	db.query = func() (*dynamodb.QueryOutput, error) {
		return &dynamodb.QueryOutput{Items: page, LastEvaluatedKey: item}, nil
	}
	if err := s.DeletePartition(ctx, "p"); err == nil || !strings.Contains(err.Error(), "more than 1000 entries") {
		t.Errorf("a partition without end: %v", err)
	}
}
