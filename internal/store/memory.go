package store

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Memory is an in-memory Store for dev mode and tests. Its scans gather and
// sort every item of the partition before they take a page: what one returns
// is bounded by maxRead, the work it does is not.
type Memory struct {
	mu  sync.Mutex
	Now func() time.Time
	// Interrupt, when set, is called before each read, write and delete with
	// what it is about to do; an error it returns fails that one, as a
	// failure of a request to DynamoDB or S3 would, and leaves every earlier
	// one done. It may block, as a slow request would: the store method
	// returns ctx.Err() when ctx ends first. PutMessage is two steps (StepRaw,
	// then StepItem), as it is two requests on AWS; so are GetMessage (two
	// StepRead) and DeleteMessage (StepDeleteRaw, then StepDeleteItem).
	//
	// partition and key name what the request is about, as the table names
	// it: a record is ("system", "record#<name>"), a message's item and its
	// raw object ("mailbox#<id>", "msg#<message id>"), F1's mailbox
	// ("mailbox#<id>", "mailbox"), an entry its own; a listing or a scan
	// names its partition and its prefix.
	Interrupt func(ctx context.Context, step Step, partition, key string) error

	// PageItems, when above zero, cuts every scan's page at that many items
	// read, as DynamoDB cuts one at 1 MB of items whatever maxRead asks:
	// fewer read than asked, and More while items are left below Last. So a
	// service built on Memory sees short pages, and with expired items only
	// a page that returns nothing and says More.
	PageItems int

	mailboxes map[string]Mailbox
	messages  map[string]map[string]Message
	raw       map[string][]byte
	records   map[string]Record
	entries   map[string]Entry
}

// NewMemory returns an empty Memory store.
func NewMemory() *Memory {
	return &Memory{
		Now:       time.Now,
		mailboxes: map[string]Mailbox{},
		messages:  map[string]map[string]Message{},
		raw:       map[string][]byte{},
		records:   map[string]Record{},
		entries:   map[string]Entry{},
	}
}

// step asks Interrupt whether the request may go on.
func (s *Memory) step(ctx context.Context, step Step, partition, key string) error {
	interrupt := s.Interrupt
	if interrupt == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- interrupt(ctx, step, partition, key) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

const memoryRecords = "system"

func messageKey(id string) string { return "msg#" + id }

// GetRecord implements Records.
func (s *Memory) GetRecord(ctx context.Context, name string) (*Record, error) {
	if err := s.step(ctx, StepRead, memoryRecords, "record#"+name); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[name]
	if !ok {
		return nil, ErrNotFound
	}
	return &r, nil
}

// PutRecord implements Records.
func (s *Memory) PutRecord(ctx context.Context, r *Record, create bool) error {
	if err := s.step(ctx, StepRecord, memoryRecords, "record#"+r.Name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[r.Name]; ok && create {
		return ErrExists
	}
	s.records[r.Name] = *r
	return nil
}

// RecordNames returns the names of the records stored, in order: what
// whoever runs the storage sees of them.
func (s *Memory) RecordNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.records))
	for name := range s.records {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// ListRecords implements RecordTable.
func (s *Memory) ListRecords(ctx context.Context, prefix string, limit int) ([]string, error) {
	if err := s.step(ctx, StepRead, memoryRecords, "record#"+prefix); err != nil {
		return nil, err
	}
	var names []string
	for _, name := range s.RecordNames() {
		if strings.HasPrefix(name, prefix) && len(names) < limit {
			names = append(names, name)
		}
	}
	return names, nil
}

// DeleteRecord implements RecordTable. Tests also use it to delete a record
// the way whoever runs the storage can.
func (s *Memory) DeleteRecord(ctx context.Context, name string) error {
	if err := s.step(ctx, StepRecord, memoryRecords, "record#"+name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, name)
	return nil
}

// GetMailbox implements Store.
func (s *Memory) GetMailbox(ctx context.Context, id string) (*Mailbox, error) {
	if err := checkIDs(true, id); err != nil {
		return nil, err
	}
	if err := s.step(ctx, StepRead, "mailbox#"+id, "mailbox"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.mailboxes[id]
	if !ok {
		return nil, ErrNotFound
	}
	return &m, nil
}

// PutMailbox implements Store.
func (s *Memory) PutMailbox(ctx context.Context, m *Mailbox) error {
	if err := checkIDs(false, m.ID); err != nil {
		return err
	}
	if err := s.step(ctx, StepRecord, "mailbox#"+m.ID, "mailbox"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.mailboxes[m.ID]
	switch {
	case m.Version == 0 && ok:
		return ErrExists
	case m.Version != 0 && (!ok || old.Version != m.Version):
		return ErrConflict
	}
	m.Version++
	s.mailboxes[m.ID] = *m
	return nil
}

// PutMessage implements Store: the raw message, then the item, as two steps.
func (s *Memory) PutMessage(ctx context.Context, m *Message, raw []byte) error {
	if err := checkIDs(false, m.MailboxID, m.ID); err != nil {
		return err
	}
	if err := s.step(ctx, StepRaw, "mailbox#"+m.MailboxID, messageKey(m.ID)); err != nil {
		return err
	}
	s.mu.Lock()
	s.raw[m.MailboxID+"/"+m.ID] = slices.Clone(raw)
	s.mu.Unlock()
	return s.PutItem(ctx, m)
}

// PutRaw implements MessageParts.
func (s *Memory) PutRaw(ctx context.Context, m *Message, raw []byte) error {
	if err := checkIDs(false, m.MailboxID, m.ID); err != nil {
		return err
	}
	if err := s.step(ctx, StepRaw, "mailbox#"+m.MailboxID, messageKey(m.ID)); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.raw[m.MailboxID+"/"+m.ID]; ok {
		return ErrExists
	}
	s.raw[m.MailboxID+"/"+m.ID] = slices.Clone(raw)
	return nil
}

// PutItem implements MessageParts.
func (s *Memory) PutItem(ctx context.Context, m *Message) error {
	if err := checkIDs(false, m.MailboxID, m.ID); err != nil {
		return err
	}
	if err := s.step(ctx, StepItem, "mailbox#"+m.MailboxID, messageKey(m.ID)); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.messages[m.MailboxID] == nil {
		s.messages[m.MailboxID] = map[string]Message{}
	}
	stored := *m
	stored.Meta = slices.Clone(m.Meta)
	s.messages[m.MailboxID][m.ID] = stored
	return nil
}

// Item implements MessageParts.
func (s *Memory) Item(ctx context.Context, mailboxID, id string) (*Message, error) {
	if err := checkIDs(true, mailboxID, id); err != nil {
		return nil, err
	}
	if err := s.step(ctx, StepRead, "mailbox#"+mailboxID, messageKey(id)); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.messages[mailboxID][id]
	if !ok || !m.ExpiresAt.After(s.Now()) {
		return nil, ErrNotFound
	}
	m.Meta = slices.Clone(m.Meta)
	return &m, nil
}

// ReadRaw implements MessageParts.
func (s *Memory) ReadRaw(ctx context.Context, mailboxID, id string, maxBytes int64) ([]byte, error) {
	if err := checkRawBound(maxBytes); err != nil {
		return nil, err
	}
	if err := checkIDs(true, mailboxID, id); err != nil {
		return nil, err
	}
	if err := s.step(ctx, StepRead, "mailbox#"+mailboxID, messageKey(id)); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.raw[mailboxID+"/"+id]
	if !ok {
		return nil, ErrNotFound
	}
	if int64(len(raw)) > maxBytes {
		return nil, ErrTooLarge
	}
	return slices.Clone(raw), nil
}

// ListMessages implements Store.
func (s *Memory) ListMessages(ctx context.Context, mailboxID string, limit int, before string) ([]Message, error) {
	if err := checkIDs(true, mailboxID); err != nil {
		return nil, err
	}
	if err := s.step(ctx, StepRead, "mailbox#"+mailboxID, "msg#"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	var out []Message
	for _, m := range s.messages[mailboxID] {
		if m.ExpiresAt.After(now) && (before == "" || m.ID < before) {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b Message) int { return strings.Compare(b.ID, a.ID) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// GetMessage implements Store: the item, then the raw message.
func (s *Memory) GetMessage(ctx context.Context, mailboxID, id string) (*Message, []byte, error) {
	m, err := s.Item(ctx, mailboxID, id)
	if err != nil {
		return nil, nil, err
	}
	if err := s.step(ctx, StepRead, "mailbox#"+mailboxID, messageKey(id)); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return m, slices.Clone(s.raw[mailboxID+"/"+id]), nil
}

// DeleteMessage implements Store: the raw message first, then the item, so
// that asking again finishes a delete that stopped between the two. It
// returns ErrNotFound only when the item was not there.
func (s *Memory) DeleteMessage(ctx context.Context, mailboxID, id string) error {
	if err := checkIDs(true, mailboxID, id); err != nil {
		return err
	}
	if err := s.step(ctx, StepDeleteRaw, "mailbox#"+mailboxID, messageKey(id)); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.raw, mailboxID+"/"+id)
	s.mu.Unlock()
	if err := s.step(ctx, StepDeleteItem, "mailbox#"+mailboxID, messageKey(id)); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.messages[mailboxID][id]; !ok {
		return ErrNotFound
	}
	delete(s.messages[mailboxID], id)
	return nil
}

// ScanMessagesDown implements Scanner.
func (s *Memory) ScanMessagesDown(ctx context.Context, mailboxID, before string, maxRead int) (*MessageScan, error) {
	if err := scanBound(maxRead); err != nil {
		return nil, err
	}
	if err := checkIDs(true, mailboxID); err != nil {
		return nil, err
	}
	if err := s.step(ctx, StepRead, "mailbox#"+mailboxID, "msg#"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []Message
	for _, m := range s.messages[mailboxID] {
		if before == "" || m.ID < before {
			all = append(all, m)
		}
	}
	slices.SortFunc(all, func(a, b Message) int { return strings.Compare(b.ID, a.ID) })
	n, more := s.page(len(all), maxRead)
	read := all[:n]
	out := &MessageScan{Messages: []Message{}, Read: n, More: more}
	now := s.Now()
	for _, m := range read {
		out.Last = m.ID
		if m.ExpiresAt.After(now) {
			m.Meta = slices.Clone(m.Meta)
			out.Messages = append(out.Messages, m)
		}
	}
	return out, nil
}

// ScanEntriesDown implements Scanner.
func (s *Memory) ScanEntriesDown(ctx context.Context, partition, prefix, before string, maxRead int) (*EntryScan, error) {
	if err := scanBound(maxRead); err != nil {
		return nil, err
	}
	if err := s.step(ctx, StepRead, partition, prefix); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []Entry
	for _, e := range s.entries {
		if e.Partition == partition && strings.HasPrefix(e.Key, prefix) && (before == "" || e.Key < before) {
			all = append(all, e)
		}
	}
	slices.SortFunc(all, func(a, b Entry) int { return strings.Compare(b.Key, a.Key) })
	n, more := s.page(len(all), maxRead)
	read := all[:n]
	out := &EntryScan{Entries: []Entry{}, Read: n, More: more}
	now := s.Now()
	for _, e := range read {
		out.Last = e.Key
		if e.ExpiresAt.IsZero() || e.ExpiresAt.After(now) {
			e.Value = slices.Clone(e.Value)
			out.Entries = append(out.Entries, e)
		}
	}
	return out, nil
}

// page is how many of a scan's items its page reads, and whether it says
// More. DynamoDB says there may be more whenever a page reads its limit; a
// page cut at PageItems says so while items are left.
func (s *Memory) page(items, maxRead int) (int, bool) {
	if s.PageItems > 0 && s.PageItems < maxRead {
		n := min(s.PageItems, items)
		return n, items > n
	}
	n := min(maxRead, items)
	return n, n == maxRead
}

func scanBound(maxRead int) error {
	if maxRead < 1 || maxRead > MaxScanRead {
		return fmt.Errorf("store: a scan reads 1 to %d items, not %d", MaxScanRead, maxRead)
	}
	return nil
}

func memoryKey(partition, key string) string { return partition + "\x00" + key }

// GetEntry implements Entries.
func (s *Memory) GetEntry(ctx context.Context, partition, key string) (*Entry, error) {
	if err := s.step(ctx, StepRead, partition, key); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[memoryKey(partition, key)]
	if !ok || (!e.ExpiresAt.IsZero() && !e.ExpiresAt.After(s.Now())) {
		return nil, ErrNotFound
	}
	e.Value = slices.Clone(e.Value)
	return &e, nil
}

// PutEntry implements Entries.
func (s *Memory) PutEntry(ctx context.Context, e *Entry) error {
	if err := s.step(ctx, StepEntry, e.Partition, e.Key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.entries[memoryKey(e.Partition, e.Key)]
	if ok && !old.ExpiresAt.IsZero() && !old.ExpiresAt.After(s.Now()) {
		ok = false // an expired entry is not there
	}
	switch {
	case e.Version == 0 && ok:
		return ErrExists
	case e.Version != 0 && (!ok || old.Version != e.Version):
		return ErrConflict
	}
	e.Version++
	stored := *e
	stored.Value = slices.Clone(e.Value)
	s.entries[memoryKey(e.Partition, e.Key)] = stored
	return nil
}

// DeleteEntry implements Entries.
func (s *Memory) DeleteEntry(ctx context.Context, partition, key string) error {
	if err := s.step(ctx, StepDeleteEntry, partition, key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[memoryKey(partition, key)]
	if !ok {
		return ErrNotFound
	}
	delete(s.entries, memoryKey(partition, key))
	if !e.ExpiresAt.IsZero() && !e.ExpiresAt.After(s.Now()) {
		return ErrNotFound
	}
	return nil
}

// DeleteEntryAt implements Entries.
func (s *Memory) DeleteEntryAt(ctx context.Context, e *Entry) error {
	if err := s.step(ctx, StepDeleteEntry, e.Partition, e.Key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.entries[memoryKey(e.Partition, e.Key)]
	if !ok || old.Version != e.Version || !bytes.Equal(old.Value, e.Value) {
		return ErrConflict
	}
	delete(s.entries, memoryKey(e.Partition, e.Key))
	return nil
}

// ListEntries implements Entries.
func (s *Memory) ListEntries(ctx context.Context, partition, prefix, after string, limit int) ([]Entry, error) {
	if err := s.step(ctx, StepRead, partition, prefix); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	var out []Entry
	for _, e := range s.entries {
		if e.Partition == partition && strings.HasPrefix(e.Key, prefix) && e.Key > after && (e.ExpiresAt.IsZero() || e.ExpiresAt.After(now)) {
			e.Value = slices.Clone(e.Value)
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b Entry) int { return strings.Compare(a.Key, b.Key) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// DeletePartition implements Entries: the partition is listed, then each
// entry deleted in the order of its key, a step each.
func (s *Memory) DeletePartition(ctx context.Context, partition string) error {
	if err := s.step(ctx, StepRead, partition, ""); err != nil {
		return err
	}
	s.mu.Lock()
	var keys []string
	for _, e := range s.entries {
		if e.Partition == partition {
			keys = append(keys, e.Key)
		}
	}
	s.mu.Unlock()
	slices.Sort(keys)
	for _, key := range keys {
		if err := s.step(ctx, StepDeleteEntry, partition, key); err != nil {
			return err
		}
		s.mu.Lock()
		delete(s.entries, memoryKey(partition, key))
		s.mu.Unlock()
	}
	return nil
}

// AllEntries returns every entry stored, the expired ones too, in the order
// of their partitions and keys: what whoever runs the storage sees of them.
func (s *Memory) AllEntries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		e.Value = slices.Clone(e.Value)
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b Entry) int {
		return strings.Compare(memoryKey(a.Partition, a.Key), memoryKey(b.Partition, b.Key))
	})
	return out
}

// SetEntry writes an entry as whoever runs the storage can: whatever was
// there, at whatever version.
func (s *Memory) SetEntry(e Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[memoryKey(e.Partition, e.Key)] = e
}

// SetRecord writes a record as whoever runs the storage can.
func (s *Memory) SetRecord(r Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[r.Name] = r
}
