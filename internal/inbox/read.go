package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // decode the charsets mail uses

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

// ReadStore is what the inbox's Reader needs of the store (ADR 0026, "The
// inbox"): message items scanned and read without their raw objects, raw
// objects read bounded, entries scanned for search, and deletions.
// *store.Memory and *store.AWS implement it.
type ReadStore interface {
	store.Scanner
	Item(ctx context.Context, mailboxID, id string) (*store.Message, error)
	ReadRaw(ctx context.Context, mailboxID, id string, max int64) ([]byte, error)
	DeleteMessage(ctx context.Context, mailboxID, id string) error
	DeleteEntry(ctx context.Context, partition, key string) error
}

// NewReader returns the inbox's Reader on a store.
func NewReader(st ReadStore) Reader { return &storeReader{st: st} }

type storeReader struct{ st ReadStore }

// ReadBudget is the budget of mail open for reading (ADR 0026, "Admission"):
// what a request takes before it reads a raw message from the bucket, and
// holds until its response is released. Take waits a bounded time and fails
// with contracts.ErrTooManyRequests when the budget has no room.
type ReadBudget interface {
	Take(ctx context.Context, n int64) error
}

type budgetKey struct{}

// WithReadBudget makes the Reader take from b, before every raw read made
// under ctx, the bound that read is given. The adapter sets it on each
// request and gives back what was taken once the response is written.
// Without one, nothing is taken (tests of the reader alone).
func WithReadBudget(ctx context.Context, b ReadBudget) context.Context {
	return context.WithValue(ctx, budgetKey{}, b)
}

// RawReadMax is the largest bound a raw read is given: the largest message
// accepted (25 MiB) and its seal, as store.MessageParts.ReadRaw allows. A
// size stored beside a message is whoever writes to the table's to set: it
// can make a read fail, never make one take more than this.
const RawReadMax = 32 << 20

// rawSealOverhead is more than what envelope.Seal adds to what it seals.
const rawSealOverhead = 1 << 10

// errRawTooLarge is a raw object larger than its item says: the bound the
// read was given came from the item, so the read stopped there. Nothing of
// the message is in it: errors reach logs.
var errRawTooLarge = errors.New("inbox: a raw message larger than its item says")

// isMessageIDShape reports whether id is a message identifier: 32 lower-case
// hexadecimal characters, time first. Anything else names no message.
func isMessageIDShape(id string) bool {
	if len(id) != 32 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// IsMessageID reports whether id has the shape of a message identifier,
// for the adapter's check of a cursor (contracts.ErrInvalidCursor).
func IsMessageID(id string) bool { return isMessageIDShape(id) }

// timeOfMessageID is when a message arrived, from its identifier: its first
// sixteen hexadecimal characters are nanoseconds since 1970.
func timeOfMessageID(id string) time.Time {
	if len(id) < 16 {
		return time.Time{}
	}
	n, err := strconv.ParseUint(id[:16], 16, 63)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(0, int64(n)).UTC() //nolint:gosec // 63 bits fit
}

func boxPartition(mailboxID string) string { return "mailbox#" + mailboxID }

// meta opens a message item's meta under the box's key.
func openMeta(b Box, m *store.Message) (*StoredMeta, error) {
	plain, err := envelope.Open(b.Key, m.Meta, envelope.AAD(PurposeMeta, b.MailboxID, m.ID))
	if err != nil {
		return nil, contracts.ErrNotFound
	}
	var meta StoredMeta
	if err := json.Unmarshal(plain, &meta); err != nil {
		return nil, contracts.ErrNotFound
	}
	return &meta, nil
}

func orEmpty[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func summaryOf(m *store.Message, meta *StoredMeta) contracts.MessageSummary {
	return contracts.MessageSummary{
		ID: m.ID, ReceivedAt: m.ReceivedAt, ExpiresAt: m.ExpiresAt, Size: m.Size,
		From: orEmpty(meta.From), Subject: meta.Subject, Snippet: meta.Snippet,
		Attachments: len(meta.Attachments), Confirmation: meta.Confirmation,
	}
}

// answer is a page, unless the context ended while it was made: a page is
// never answered for a request whose deadline passed.
func answer(ctx context.Context, page *contracts.MessagePage) (*contracts.MessagePage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return page, nil
}

// List implements Reader.
func (r *storeReader) List(ctx context.Context, b Box, before string, limit int) (*contracts.MessagePage, error) {
	page := &contracts.MessagePage{Messages: []contracts.MessageSummary{}}
	read := 0
	for read < ListMaxRead {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		scan, err := r.st.ScanMessagesDown(ctx, b.MailboxID, before, min(store.MaxScanRead, ListMaxRead-read))
		if err != nil {
			return nil, err
		}
		read += scan.Read
		for i := range scan.Messages {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			m := &scan.Messages[i]
			meta, err := openMeta(b, m)
			if err != nil {
				continue // does not open under this box's key: it is not one of its messages
			}
			page.Messages = append(page.Messages, summaryOf(m, meta))
			if len(page.Messages) == limit {
				if i < len(scan.Messages)-1 {
					// Filled inside the batch: the rest of it is read again
					// by the next page, never skipped.
					page.Next = m.ID
				} else if scan.More {
					page.Next = scan.Last
				}
				return answer(ctx, page)
			}
		}
		if !scan.More || scan.Read == 0 {
			return answer(ctx, page)
		}
		before = scan.Last
	}
	page.Next = before
	return answer(ctx, page)
}

// item reads a message's item and opens its meta: the message is the box's
// and has not expired.
func (r *storeReader) item(ctx context.Context, b Box, id string) (*store.Message, *StoredMeta, error) {
	if !isMessageIDShape(id) {
		return nil, nil, contracts.ErrNotFound
	}
	m, err := r.st.Item(ctx, b.MailboxID, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, contracts.ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	meta, err := openMeta(b, m)
	if err != nil {
		return nil, nil, err
	}
	return m, meta, nil
}

// raw reads a message's raw object, bounded by the size its item says and
// the seal, after taking that bound from the request's budget.
func (r *storeReader) raw(ctx context.Context, b Box, m *store.Message) ([]byte, error) {
	bound := int64(RawReadMax)
	if m.Size >= 0 && int64(m.Size) < RawReadMax-rawSealOverhead {
		bound = int64(m.Size) + rawSealOverhead
	}
	if budget, ok := ctx.Value(budgetKey{}).(ReadBudget); ok {
		if err := budget.Take(ctx, bound); err != nil {
			return nil, err
		}
	}
	sealed, err := r.st.ReadRaw(ctx, b.MailboxID, m.ID, bound)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, contracts.ErrNotFound
	case errors.Is(err, store.ErrTooLarge):
		return nil, errRawTooLarge
	case err != nil:
		return nil, err
	}
	raw, err := envelope.Open(b.Key, sealed, envelope.AAD(PurposeRaw, b.MailboxID, m.ID))
	if err != nil {
		return nil, contracts.ErrNotFound
	}
	return raw, nil
}

// Get implements Reader.
func (r *storeReader) Get(ctx context.Context, b Box, id string) (*contracts.Message, error) {
	m, meta, err := r.item(ctx, b, id)
	if err != nil {
		return nil, err
	}
	out := &contracts.Message{
		ID: m.ID, ReceivedAt: m.ReceivedAt, ExpiresAt: m.ExpiresAt, Size: m.Size,
		From: orEmpty(meta.From), To: orEmpty(meta.To), Cc: orEmpty(meta.Cc), ReplyTo: orEmpty(meta.ReplyTo),
		Subject: meta.Subject, Date: meta.Date, TextFrom: contracts.TextFromNone,
		Attachments: orEmpty(meta.Attachments), Auth: orEmpty(meta.Auth),
		Confirmation: meta.Confirmation, Unparsed: meta.Unparsed,
	}
	if meta.Unparsed {
		return out, nil
	}
	raw, err := r.raw(ctx, b, m)
	if err != nil {
		return nil, err
	}
	out.Text, out.TextFrom, out.TextTruncated = MessageText(ctx, raw, MessageTextMax)
	out.HTML = messageHTML(ctx, raw, b, m.ID)
	return out, ctx.Err()
}

// Raw implements Reader.
func (r *storeReader) Raw(ctx context.Context, b Box, id string) ([]byte, error) {
	m, _, err := r.item(ctx, b, id)
	if err != nil {
		return nil, err
	}
	return r.raw(ctx, b, m)
}

// Attachment implements Reader. The index is one the message's meta lists;
// its bytes are the index-th attachment of the raw message, walked as
// delivery walked it.
func (r *storeReader) Attachment(ctx context.Context, b Box, id string, index int) (string, []byte, error) {
	m, meta, err := r.item(ctx, b, id)
	if err != nil {
		return "", nil, err
	}
	at := -1
	for i, a := range meta.Attachments {
		if a.Index == index {
			at = i
			break
		}
	}
	if at < 0 || meta.Unparsed {
		return "", nil, contracts.ErrNotFound
	}
	raw, err := r.raw(ctx, b, m)
	if err != nil {
		return "", nil, err
	}
	var data []byte
	found := false
	n := 0
	walkErr := walkParts(ctx, raw, func(p *message.Entity, attachment bool) (bool, error) {
		if !attachment {
			return true, nil
		}
		if n != index {
			n++
			return true, nil
		}
		// The bytes delivery measured when it listed the part, through the
		// same bound: a body whose encoding breaks half way is the bytes
		// before the break, and no charset is converted.
		data, _ = io.ReadAll(attachmentBody(p))
		found = true
		return false, nil
	})
	if walkErr != nil && errors.Is(walkErr, ctx.Err()) {
		return "", nil, walkErr
	}
	if !found {
		return "", nil, contracts.ErrNotFound
	}
	return meta.Attachments[at].Name, data, nil
}

// Delete implements Reader.
func (r *storeReader) Delete(ctx context.Context, b Box, ids []string) (int, error) {
	if len(ids) > MaxDelete {
		return 0, contracts.ErrTooManyMessages
	}
	seen := make(map[string]bool, len(ids))
	deleted := 0
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		if !isMessageIDShape(id) || seen[id] {
			continue
		}
		seen[id] = true
		err := r.st.DeleteMessage(ctx, b.MailboxID, id)
		switch {
		case err == nil:
			deleted++
		case errors.Is(err, store.ErrNotFound):
		default:
			return deleted, err
		}
		for _, key := range []string{KeySearch + id, KeyConfirm + id} {
			if err := r.st.DeleteEntry(ctx, boxPartition(b.MailboxID), key); err != nil && !errors.Is(err, store.ErrNotFound) {
				return deleted, err
			}
		}
	}
	return deleted, nil
}
