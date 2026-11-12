package store

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// Memory is an in-memory Store for dev mode and tests.
type Memory struct {
	mu        sync.Mutex
	Now       func() time.Time
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

// GetRecord implements Records.
func (s *Memory) GetRecord(_ context.Context, name string) (*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[name]
	if !ok {
		return nil, ErrNotFound
	}
	return &r, nil
}

// PutRecord implements Records.
func (s *Memory) PutRecord(_ context.Context, r *Record, create bool) error {
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
func (s *Memory) ListRecords(_ context.Context, prefix string, limit int) ([]string, error) {
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
func (s *Memory) DeleteRecord(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, name)
	return nil
}

// GetMailbox implements Store.
func (s *Memory) GetMailbox(_ context.Context, id string) (*Mailbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.mailboxes[id]
	if !ok {
		return nil, ErrNotFound
	}
	return &m, nil
}

// PutMailbox implements Store.
func (s *Memory) PutMailbox(_ context.Context, m *Mailbox) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mailboxes[m.ID] = *m
	return nil
}

// PutMessage implements Store.
func (s *Memory) PutMessage(_ context.Context, m *Message, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.messages[m.MailboxID] == nil {
		s.messages[m.MailboxID] = map[string]Message{}
	}
	s.messages[m.MailboxID][m.ID] = *m
	s.raw[m.MailboxID+"/"+m.ID] = slices.Clone(raw)
	return nil
}

// ListMessages implements Store.
func (s *Memory) ListMessages(_ context.Context, mailboxID string, limit int, before string) ([]Message, error) {
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

// GetMessage implements Store.
func (s *Memory) GetMessage(_ context.Context, mailboxID, id string) (*Message, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.messages[mailboxID][id]
	if !ok || !m.ExpiresAt.After(s.Now()) {
		return nil, nil, ErrNotFound
	}
	return &m, slices.Clone(s.raw[mailboxID+"/"+id]), nil
}

// DeleteMessage implements Store.
func (s *Memory) DeleteMessage(_ context.Context, mailboxID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.messages[mailboxID][id]; !ok {
		return ErrNotFound
	}
	delete(s.messages[mailboxID], id)
	delete(s.raw, mailboxID+"/"+id)
	return nil
}

func memoryKey(partition, key string) string { return partition + "\x00" + key }

// GetEntry implements Entries.
func (s *Memory) GetEntry(_ context.Context, partition, key string) (*Entry, error) {
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
func (s *Memory) PutEntry(_ context.Context, e *Entry) error {
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
func (s *Memory) DeleteEntry(_ context.Context, partition, key string) error {
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
func (s *Memory) DeleteEntryAt(_ context.Context, e *Entry) error {
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
func (s *Memory) ListEntries(_ context.Context, partition, prefix, after string, limit int) ([]Entry, error) {
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

// DeletePartition implements Entries.
func (s *Memory) DeletePartition(_ context.Context, partition string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.entries {
		if e.Partition == partition {
			delete(s.entries, k)
		}
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
