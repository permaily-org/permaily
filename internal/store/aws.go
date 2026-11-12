package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddb "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// AWS stores records in one DynamoDB table and raw messages in one S3 bucket.
//
// Table (pk, sk), TTL attribute expires_at:
//
//	pk = "mailbox#<id>", sk = "mailbox"        mailbox: wrapped_key, retention_days, state
//	pk = "mailbox#<id>", sk = "msg#<msg id>"   message: received_at, size, retention_days, meta
//	pk = "system",       sk = "record#<name>"  record: wrapped_key, and value (sealed) or text (readable)
//	pk = <partition>,    sk = <key>            entry: text, version, expires_at
//
// Bucket: raw/<mailbox id>/<msg id>, tagged retention-days=<n> so the
// lifecycle rule for that period deletes it.
type AWS struct {
	DB     DynamoAPI
	S3     S3API
	Table  string
	Bucket string
	Now    func() time.Time
}

// DynamoAPI is the subset of the DynamoDB client used here.
type DynamoAPI interface {
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	DeleteItem(ctx context.Context, in *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

// S3API is the subset of the S3 client used here.
type S3API interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

func pk(mailboxID string) ddb.AttributeValue {
	return &ddb.AttributeValueMemberS{Value: "mailbox#" + mailboxID}
}
func str(s string) ddb.AttributeValue { return &ddb.AttributeValueMemberS{Value: s} }
func num(n int64) ddb.AttributeValue {
	return &ddb.AttributeValueMemberN{Value: strconv.FormatInt(n, 10)}
}
func bin(b []byte) ddb.AttributeValue { return &ddb.AttributeValueMemberB{Value: b} }

func getS(item map[string]ddb.AttributeValue, k string) string {
	if v, ok := item[k].(*ddb.AttributeValueMemberS); ok {
		return v.Value
	}
	return ""
}

func getN(item map[string]ddb.AttributeValue, k string) int64 {
	if v, ok := item[k].(*ddb.AttributeValueMemberN); ok {
		n, _ := strconv.ParseInt(v.Value, 10, 64)
		return n
	}
	return 0
}

func getB(item map[string]ddb.AttributeValue, k string) []byte {
	if v, ok := item[k].(*ddb.AttributeValueMemberB); ok {
		return v.Value
	}
	return nil
}

func (s *AWS) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func rawKey(mailboxID, id string) string { return "raw/" + mailboxID + "/" + id }

// GetMailbox implements Store.
func (s *AWS) GetMailbox(ctx context.Context, id string) (*Mailbox, error) {
	out, err := s.DB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.Table),
		Key:            map[string]ddb.AttributeValue{"pk": pk(id), "sk": str("mailbox")},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("store: get mailbox: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, ErrNotFound
	}
	return &Mailbox{
		ID:            id,
		WrappedKey:    getB(out.Item, "wrapped_key"),
		RetentionDays: int(getN(out.Item, "retention_days")),
		State:         getB(out.Item, "state"),
		UpdatedAt:     time.Unix(getN(out.Item, "updated_at"), 0).UTC(),
	}, nil
}

// PutMailbox implements Store.
func (s *AWS) PutMailbox(ctx context.Context, m *Mailbox) error {
	_, err := s.DB.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.Table),
		Item: map[string]ddb.AttributeValue{
			"pk": pk(m.ID), "sk": str("mailbox"),
			"wrapped_key":    bin(m.WrappedKey),
			"retention_days": num(int64(m.RetentionDays)),
			"state":          bin(m.State),
			"updated_at":     num(m.UpdatedAt.Unix()),
		},
	})
	if err != nil {
		return fmt.Errorf("store: put mailbox: %w", err)
	}
	return nil
}

// Records live under their own partition, apart from every mailbox.
func recordKey(name string) map[string]ddb.AttributeValue {
	return map[string]ddb.AttributeValue{"pk": str("system"), "sk": str("record#" + name)}
}

// GetRecord implements Records.
func (s *AWS) GetRecord(ctx context.Context, name string) (*Record, error) {
	out, err := s.DB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.Table),
		Key:            recordKey(name),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("store: get record: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, ErrNotFound
	}
	rec := &Record{
		Name:       name,
		WrappedKey: getB(out.Item, "wrapped_key"),
		Value:      getB(out.Item, "value"),
		UpdatedAt:  time.Unix(getN(out.Item, "updated_at"), 0).UTC(),
	}
	// A readable record is stored as text, so that it can be read in the table.
	if text, ok := out.Item["text"].(*ddb.AttributeValueMemberS); ok {
		rec.Value, rec.Plain = []byte(text.Value), true
	}
	return rec, nil
}

// PutRecord implements Records.
func (s *AWS) PutRecord(ctx context.Context, r *Record, create bool) error {
	item := recordKey(r.Name)
	// A readable record has no data key.
	if len(r.WrappedKey) > 0 {
		item["wrapped_key"] = bin(r.WrappedKey)
	}
	if r.Plain {
		item["text"] = str(string(r.Value))
	} else {
		item["value"] = bin(r.Value)
	}
	item["updated_at"] = num(r.UpdatedAt.Unix())
	in := &dynamodb.PutItemInput{TableName: aws.String(s.Table), Item: item}
	if create {
		in.ConditionExpression = aws.String("attribute_not_exists(pk)")
	}
	if _, err := s.DB.PutItem(ctx, in); err != nil {
		var exists *ddb.ConditionalCheckFailedException
		if errors.As(err, &exists) {
			return ErrExists
		}
		return fmt.Errorf("store: put record: %w", err)
	}
	return nil
}

// ListRecords implements RecordTable.
func (s *AWS) ListRecords(ctx context.Context, prefix string, limit int) ([]string, error) {
	var names []string
	var start map[string]ddb.AttributeValue
	for len(names) < limit {
		res, err := s.DB.Query(ctx, &dynamodb.QueryInput{
			TableName:                 aws.String(s.Table),
			KeyConditionExpression:    aws.String("pk = :pk AND begins_with(sk, :prefix)"),
			ExpressionAttributeValues: map[string]ddb.AttributeValue{":pk": str("system"), ":prefix": str("record#" + prefix)},
			ProjectionExpression:      aws.String("sk"),
			ConsistentRead:            aws.Bool(true),
			Limit:                     aws.Int32(int32(min(limit, 100))), //nolint:gosec // bounded by min
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return nil, fmt.Errorf("store: list records: %w", err)
		}
		for _, item := range res.Items {
			names = append(names, strings.TrimPrefix(getS(item, "sk"), "record#"))
		}
		if len(res.LastEvaluatedKey) == 0 {
			break
		}
		start = res.LastEvaluatedKey
	}
	if len(names) > limit {
		names = names[:limit]
	}
	return names, nil
}

// DeleteRecord implements RecordTable.
func (s *AWS) DeleteRecord(ctx context.Context, name string) error {
	if _, err := s.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(s.Table), Key: recordKey(name)}); err != nil {
		return fmt.Errorf("store: delete record: %w", err)
	}
	return nil
}

// PutMessage implements Store. The raw message is written first, so a record
// never points at a missing object.
func (s *AWS) PutMessage(ctx context.Context, m *Message, raw []byte) error {
	_, err := s.S3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:               aws.String(s.Bucket),
		Key:                  aws.String(rawKey(m.MailboxID, m.ID)),
		Body:                 bytes.NewReader(raw),
		ContentLength:        aws.Int64(int64(len(raw))),
		ContentType:          aws.String("application/octet-stream"),
		Tagging:              aws.String("retention-days=" + strconv.Itoa(m.RetentionDays)),
		ServerSideEncryption: s3types.ServerSideEncryptionAes256,
	})
	if err != nil {
		return fmt.Errorf("store: put raw message: %w", err)
	}
	_, err = s.DB.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.Table),
		Item: map[string]ddb.AttributeValue{
			"pk": pk(m.MailboxID), "sk": str("msg#" + m.ID),
			"received_at":    num(m.ReceivedAt.Unix()),
			"expires_at":     num(m.ExpiresAt.Unix()),
			"size":           num(int64(m.Size)),
			"retention_days": num(int64(m.RetentionDays)),
			"meta":           bin(m.Meta),
		},
	})
	if err != nil {
		return fmt.Errorf("store: put message: %w", err)
	}
	return nil
}

func (s *AWS) message(mailboxID string, item map[string]ddb.AttributeValue) Message {
	return Message{
		MailboxID:     mailboxID,
		ID:            getS(item, "sk")[len("msg#"):],
		ReceivedAt:    time.Unix(getN(item, "received_at"), 0).UTC(),
		ExpiresAt:     time.Unix(getN(item, "expires_at"), 0).UTC(),
		Size:          int(getN(item, "size")),
		RetentionDays: int(getN(item, "retention_days")),
		Meta:          getB(item, "meta"),
	}
}

// ListMessages implements Store. DynamoDB deletes expired items lazily, so
// they are filtered here too.
func (s *AWS) ListMessages(ctx context.Context, mailboxID string, limit int, before string) ([]Message, error) {
	cond := "pk = :pk AND begins_with(sk, :msg)"
	values := map[string]ddb.AttributeValue{":pk": pk(mailboxID), ":msg": str("msg#"), ":now": num(s.now().Unix())}
	if before != "" {
		cond = "pk = :pk AND sk BETWEEN :msg AND :before"
		values[":before"] = str("msg#" + before)
	}
	var out []Message
	var start map[string]ddb.AttributeValue
	for len(out) < limit {
		res, err := s.DB.Query(ctx, &dynamodb.QueryInput{
			TableName:                 aws.String(s.Table),
			KeyConditionExpression:    aws.String(cond),
			FilterExpression:          aws.String("expires_at > :now"),
			ExpressionAttributeValues: values,
			ScanIndexForward:          aws.Bool(false),
			Limit:                     aws.Int32(int32(min(limit, 100))), //nolint:gosec // bounded by min
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return nil, fmt.Errorf("store: list messages: %w", err)
		}
		for _, item := range res.Items {
			m := s.message(mailboxID, item)
			if m.ID != before {
				out = append(out, m)
			}
		}
		if len(res.LastEvaluatedKey) == 0 {
			break
		}
		start = res.LastEvaluatedKey
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// GetMessage implements Store.
func (s *AWS) GetMessage(ctx context.Context, mailboxID, id string) (*Message, []byte, error) {
	out, err := s.DB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.Table),
		Key:       map[string]ddb.AttributeValue{"pk": pk(mailboxID), "sk": str("msg#" + id)},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("store: get message: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, nil, ErrNotFound
	}
	m := s.message(mailboxID, out.Item)
	if !m.ExpiresAt.After(s.now()) {
		return nil, nil, ErrNotFound
	}
	obj, err := s.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(rawKey(mailboxID, id))})
	if err != nil {
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("store: get raw message: %w", err)
	}
	defer obj.Body.Close()
	raw, err := io.ReadAll(obj.Body)
	if err != nil {
		return nil, nil, err
	}
	return &m, raw, nil
}

// DeleteMessage implements Store.
func (s *AWS) DeleteMessage(ctx context.Context, mailboxID, id string) error {
	out, err := s.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:    aws.String(s.Table),
		Key:          map[string]ddb.AttributeValue{"pk": pk(mailboxID), "sk": str("msg#" + id)},
		ReturnValues: ddb.ReturnValueAllOld,
	})
	if err != nil {
		return fmt.Errorf("store: delete message: %w", err)
	}
	if len(out.Attributes) == 0 {
		return ErrNotFound
	}
	if _, err := s.S3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(rawKey(mailboxID, id))}); err != nil {
		return fmt.Errorf("store: delete raw message: %w", err)
	}
	return nil
}

func entryKey(partition, key string) map[string]ddb.AttributeValue {
	return map[string]ddb.AttributeValue{"pk": str(partition), "sk": str(key)}
}

func (s *AWS) entry(item map[string]ddb.AttributeValue) Entry {
	e := Entry{
		Partition: getS(item, "pk"),
		Key:       getS(item, "sk"),
		Value:     []byte(getS(item, "text")),
		Version:   uint64(getN(item, "version")), //nolint:gosec // written by PutEntry, never negative
		UpdatedAt: time.Unix(getN(item, "updated_at"), 0).UTC(),
	}
	if exp := getN(item, "expires_at"); exp != 0 {
		e.ExpiresAt = time.Unix(exp, 0).UTC()
	}
	return e
}

func (s *AWS) expired(e *Entry) bool {
	return !e.ExpiresAt.IsZero() && !e.ExpiresAt.After(s.now())
}

// GetEntry implements Entries. DynamoDB deletes expired items lazily, so they
// are hidden here too.
func (s *AWS) GetEntry(ctx context.Context, partition, key string) (*Entry, error) {
	out, err := s.DB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.Table),
		Key:            entryKey(partition, key),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("store: get entry: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, ErrNotFound
	}
	e := s.entry(out.Item)
	if s.expired(&e) {
		return nil, ErrNotFound
	}
	return &e, nil
}

// PutEntry implements Entries.
func (s *AWS) PutEntry(ctx context.Context, e *Entry) error {
	item := entryKey(e.Partition, e.Key)
	item["text"] = str(string(e.Value))
	item["version"] = num(int64(e.Version + 1)) //nolint:gosec // a count of writes
	item["updated_at"] = num(e.UpdatedAt.Unix())
	if !e.ExpiresAt.IsZero() {
		item["expires_at"] = num(e.ExpiresAt.Unix())
	}
	in := &dynamodb.PutItemInput{TableName: aws.String(s.Table), Item: item}
	if e.Version == 0 {
		// New, or in the place of one that expired and was not removed yet.
		in.ConditionExpression = aws.String("attribute_not_exists(pk) OR (attribute_exists(expires_at) AND expires_at <= :now)")
		in.ExpressionAttributeValues = map[string]ddb.AttributeValue{":now": num(s.now().Unix())}
	} else {
		in.ConditionExpression = aws.String("version = :version")
		in.ExpressionAttributeValues = map[string]ddb.AttributeValue{":version": num(int64(e.Version))} //nolint:gosec // a count of writes
	}
	if _, err := s.DB.PutItem(ctx, in); err != nil {
		var failed *ddb.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			if e.Version == 0 {
				return ErrExists
			}
			return ErrConflict
		}
		return fmt.Errorf("store: put entry: %w", err)
	}
	e.Version++
	return nil
}

// DeleteEntry implements Entries.
func (s *AWS) DeleteEntry(ctx context.Context, partition, key string) error {
	out, err := s.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:    aws.String(s.Table),
		Key:          entryKey(partition, key),
		ReturnValues: ddb.ReturnValueAllOld,
	})
	if err != nil {
		return fmt.Errorf("store: delete entry: %w", err)
	}
	if len(out.Attributes) == 0 {
		return ErrNotFound
	}
	if e := s.entry(out.Attributes); s.expired(&e) {
		return ErrNotFound
	}
	return nil
}

// DeleteEntryAt implements Entries.
func (s *AWS) DeleteEntryAt(ctx context.Context, e *Entry) error {
	_, err := s.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:                 aws.String(s.Table),
		Key:                       entryKey(e.Partition, e.Key),
		ConditionExpression:       aws.String("version = :version AND #value = :value"),
		ExpressionAttributeNames:  map[string]string{"#value": "text"},
		ExpressionAttributeValues: map[string]ddb.AttributeValue{":version": num(int64(e.Version)), ":value": str(string(e.Value))}, //nolint:gosec // a count of writes
	})
	if err != nil {
		var failed *ddb.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			return ErrConflict
		}
		return fmt.Errorf("store: delete entry: %w", err)
	}
	return nil
}

// ListEntries implements Entries.
func (s *AWS) ListEntries(ctx context.Context, partition, prefix, after string, limit int) ([]Entry, error) {
	cond, values := "pk = :pk", map[string]ddb.AttributeValue{":pk": str(partition)}
	switch {
	case after != "" && after >= prefix:
		// The keys with the prefix are next to one another, so the ones after
		// a key that has it are the first that follow it, up to one without.
		if !strings.HasPrefix(after, prefix) {
			return nil, nil // after is past every key with the prefix
		}
		cond, values[":from"] = cond+" AND sk > :from", str(after)
	case prefix != "":
		// DynamoDB takes no empty string for a key: no prefix is no condition.
		cond, values[":from"] = cond+" AND begins_with(sk, :from)", str(prefix)
	}
	var out []Entry
	var start map[string]ddb.AttributeValue
	for len(out) < limit {
		res, err := s.DB.Query(ctx, &dynamodb.QueryInput{
			TableName:                 aws.String(s.Table),
			KeyConditionExpression:    aws.String(cond),
			ExpressionAttributeValues: values,
			ConsistentRead:            aws.Bool(true),
			Limit:                     aws.Int32(int32(min(limit, 100))), //nolint:gosec // bounded by min
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return nil, fmt.Errorf("store: list entries: %w", err)
		}
		for _, item := range res.Items {
			e := s.entry(item)
			if !strings.HasPrefix(e.Key, prefix) {
				return out, nil
			}
			if !s.expired(&e) {
				out = append(out, e)
			}
		}
		if len(res.LastEvaluatedKey) == 0 {
			break
		}
		start = res.LastEvaluatedKey
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// maxPartitionDeletes bounds what one DeletePartition deletes: whoever writes
// to the table can fill a partition faster than it is emptied.
const maxPartitionDeletes = 1000

// DeletePartition implements Entries. DynamoDB deletes one item at a time.
func (s *AWS) DeletePartition(ctx context.Context, partition string) error {
	deleted := 0
	var start map[string]ddb.AttributeValue
	for {
		res, err := s.DB.Query(ctx, &dynamodb.QueryInput{
			TableName:                 aws.String(s.Table),
			KeyConditionExpression:    aws.String("pk = :pk"),
			ExpressionAttributeValues: map[string]ddb.AttributeValue{":pk": str(partition)},
			ProjectionExpression:      aws.String("pk, sk"),
			ConsistentRead:            aws.Bool(true),
			Limit:                     aws.Int32(100),
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return fmt.Errorf("store: list a partition: %w", err)
		}
		for _, item := range res.Items {
			if deleted++; deleted > maxPartitionDeletes {
				return fmt.Errorf("store: more than %d entries in a partition to delete", maxPartitionDeletes)
			}
			if _, err := s.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(s.Table), Key: entryKey(getS(item, "pk"), getS(item, "sk"))}); err != nil {
				return fmt.Errorf("store: delete entry: %w", err)
			}
		}
		if len(res.LastEvaluatedKey) == 0 {
			return nil
		}
		start = res.LastEvaluatedKey
	}
}
