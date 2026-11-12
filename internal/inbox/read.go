package inbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // decode the charsets mail uses
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

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
				return page, nil
			}
		}
		if !scan.More || scan.Read == 0 {
			return page, nil
		}
		before = scan.Last
	}
	page.Next = before
	return page, nil
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
	out.Text, out.TextFrom, out.TextTruncated = readText(ctx, raw, MessageTextMax)
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
		var err error
		data, err = io.ReadAll(io.LimitReader(p.Body, int64(len(raw))))
		found = err == nil
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

// errWalkStop ends a walk early; it is not an error of the message.
var errWalkStop = errors.New("inbox: walk stopped")

// walk visits the leaf parts of a raw message in order, as go-message's
// mail.Reader does (depth first; a part is an attachment unless its
// disposition is inline, or it is text/* without disposition attachment),
// within the limits of ADR 0026 ("What a message may hold"): headers of
// MaxHeaderBytes, MaxMIMEDepth levels of nesting, MaxMIMEParts parts. visit
// returns false to stop. A part that cannot be read ends the walk: what was
// visited stands.
func walkParts(ctx context.Context, raw []byte, visit func(p *message.Entity, attachment bool) (bool, error)) error {
	opts := &message.ReadOptions{MaxHeaderBytes: MaxHeaderBytes}
	e, err := message.ReadWithOptions(bytes.NewReader(raw), opts)
	if err != nil && !message.IsUnknownCharset(err) {
		return err
	}
	parts := 0
	var visitEntity func(e *message.Entity, depth int) error
	visitEntity = func(e *message.Entity, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		parts++
		if parts > MaxMIMEParts {
			return errWalkStop
		}
		if mr := e.MultipartReader(); mr != nil {
			if depth >= MaxMIMEDepth {
				return errWalkStop
			}
			for {
				p, err := mr.NextPart()
				if errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil && !message.IsUnknownCharset(err) {
					return err
				}
				if err := visitEntity(p, depth+1); err != nil {
					return err
				}
			}
		}
		t, _, _ := e.Header.ContentType()
		disp, _, _ := e.Header.ContentDisposition()
		inline := disp == "inline" || (disp != "attachment" && strings.HasPrefix(t, "text/"))
		more, err := visit(e, !inline)
		if err != nil {
			return err
		}
		if !more {
			return errWalkStop
		}
		return nil
	}
	err = visitEntity(e, 0)
	if errors.Is(err, errWalkStop) {
		return nil
	}
	return err
}

// messageText is a message's text (ADR 0026, "What a message returns"): its
// first text/plain part, decoded; with none, the text of its first
// text/html part, taken out with x/net/html's tokenizer inside the enclave;
// with neither, nothing. At most max bytes, truncated past it.
func readText(ctx context.Context, raw []byte, max int) (text, from string, truncated bool) {
	var htmlPart []byte
	haveHTML := false
	_ = walkParts(ctx, raw, func(p *message.Entity, attachment bool) (bool, error) {
		if attachment {
			return true, nil
		}
		t, _, _ := p.Header.ContentType()
		switch t {
		case "text/plain":
			b, err := io.ReadAll(io.LimitReader(p.Body, int64(max)+1))
			if err != nil && len(b) == 0 {
				return true, nil
			}
			text, truncated = readPlain(b, max)
			from = contracts.TextFromPlain
			return false, nil
		case "text/html":
			if !haveHTML {
				b, err := io.ReadAll(io.LimitReader(p.Body, contracts.MessageHTMLMaxBytes))
				if err == nil || len(b) > 0 {
					htmlPart, haveHTML = b, true
				}
			}
		}
		return true, nil
	})
	if from == contracts.TextFromPlain {
		return text, from, truncated
	}
	if haveHTML {
		text, truncated = readHTMLText(htmlPart, max)
		return text, contracts.TextFromHTML, truncated
	}
	return "", contracts.TextFromNone, false
}

// plainText is a text part as text: valid UTF-8, LF line ends, at most max
// bytes cut at a character.
func readPlain(b []byte, max int) (string, bool) {
	s := strings.ToValidUTF8(string(b), "\uFFFD")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return cutText(s, max)
}

// cut keeps at most max bytes of s, ending on a whole character.
func cutText(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	s = s[:max(0, limit)]
	for len(s) > 0 {
		if r, size := utf8.DecodeLastRuneInString(s); r != utf8.RuneError || size != 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s, true
}

// The elements whose content is never text, and the ones that start a line.
var (
	textSkipped = map[atom.Atom]bool{atom.Script: true, atom.Style: true, atom.Head: true, atom.Title: true, atom.Template: true}
	textBlocks  = map[atom.Atom]bool{
		atom.Address: true, atom.Article: true, atom.Aside: true, atom.Blockquote: true, atom.Br: true, atom.Caption: true,
		atom.Dd: true, atom.Details: true, atom.Div: true, atom.Dl: true, atom.Dt: true, atom.Fieldset: true,
		atom.Figcaption: true, atom.Figure: true, atom.Footer: true, atom.Form: true, atom.H1: true, atom.H2: true,
		atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true, atom.Header: true, atom.Hr: true, atom.Li: true,
		atom.Main: true, atom.Nav: true, atom.Ol: true, atom.P: true, atom.Pre: true, atom.Section: true,
		atom.Summary: true, atom.Table: true, atom.Tbody: true, atom.Td: true, atom.Tfoot: true, atom.Th: true,
		atom.Thead: true, atom.Tr: true, atom.Ul: true,
	}
)

// htmlText takes the text out of an HTML part: its text nodes, a line break
// for each block element, nothing of script, style or head, entities
// decoded, and each link's target after its text in brackets. The HTML is
// only tokenized, never built into a tree or shown: what it says becomes
// text and nothing else.
func readHTMLText(b []byte, max int) (string, bool) {
	z := html.NewTokenizer(bytes.NewReader(b))
	z.SetMaxBuf(len(b) + 1)
	var w htmlTextWriter
	w.max = max
	skip := 0
	pre := 0
	var links []string // the href of each open <a>, "" for none
	for !w.full() {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return w.done()
		case html.TextToken:
			if skip == 0 {
				w.text(string(z.Text()), pre > 0)
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			a := atom.Lookup(name)
			if textSkipped[a] {
				if tt == html.StartTagToken {
					skip++
				}
				continue
			}
			if skip > 0 {
				continue
			}
			if textBlocks[a] {
				w.line(a == atom.Br)
			}
			if a == atom.Pre && tt == html.StartTagToken {
				pre++
			}
			if a == atom.A && tt == html.StartTagToken {
				href := ""
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					if string(k) == "href" {
						href = strings.TrimSpace(string(v))
					}
				}
				links = append(links, href)
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			a := atom.Lookup(name)
			if textSkipped[a] {
				if skip > 0 {
					skip--
				}
				continue
			}
			if skip > 0 {
				continue
			}
			switch {
			case a == atom.A && len(links) > 0:
				href := links[len(links)-1]
				links = links[:len(links)-1]
				if href != "" && !strings.HasPrefix(href, "#") && !strings.Contains(w.lastWords(len(href)), href) {
					w.text(" ["+href+"]", false)
				}
			case a == atom.Pre && pre > 0:
				pre--
			}
			if textBlocks[a] && a != atom.Br {
				w.line(false)
			}
		}
	}
	return w.done()
}

// htmlTextWriter writes the text of an HTML part: runs of white space become one
// space outside <pre>, blank lines are at most one, and it stops at max
// bytes.
type htmlTextWriter struct {
	b         strings.Builder
	max       int
	space     bool // a space is owed before the next word
	newlines  int  // line breaks owed before the next word
	truncated bool
}

func (w *htmlTextWriter) full() bool { return w.truncated }

// line ends the line, once however many blocks end or start there; br, which
// is a line break of its own, ends one more (a blank line at most).
func (w *htmlTextWriter) line(br bool) {
	if w.b.Len() > 0 {
		if br {
			w.newlines = min(w.newlines+1, 2)
		} else {
			w.newlines = max(w.newlines, 1)
		}
	}
	w.space = false
}

func (w *htmlTextWriter) text(s string, pre bool) {
	// Made valid before it is counted, so what is written stays within max.
	s = strings.ToValidUTF8(s, "\uFFFD")
	if pre {
		w.flush()
		w.write(strings.ReplaceAll(s, "\r\n", "\n"))
		return
	}
	for i, word := range strings.Fields(s) {
		if i > 0 || (len(s) > 0 && htmlSpace(s[0])) {
			w.space = true
		}
		w.flush()
		w.write(word)
	}
	if len(s) > 0 && htmlSpace(s[len(s)-1]) {
		w.space = true
	}
}

func htmlSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

func (w *htmlTextWriter) flush() {
	switch {
	case w.newlines > 0:
		w.write(strings.Repeat("\n", w.newlines))
	case w.space && w.b.Len() > 0:
		w.write(" ")
	}
	w.newlines, w.space = 0, false
}

func (w *htmlTextWriter) write(s string) {
	if w.truncated {
		return
	}
	if w.b.Len()+len(s) > w.max {
		s, _ = cutText(s, w.max-w.b.Len())
		w.b.WriteString(s)
		w.truncated = true
		return
	}
	w.b.WriteString(s)
}

// lastWords is the end of what was written, n bytes at most.
func (w *htmlTextWriter) lastWords(n int) string {
	s := w.b.String()
	return s[max(0, len(s)-n):]
}

func (w *htmlTextWriter) done() (string, bool) {
	return w.b.String(), w.truncated
}
