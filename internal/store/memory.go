package store

import (
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
}

// NewMemory returns an empty Memory store.
func NewMemory() *Memory {
	return &Memory{
		Now:       time.Now,
		mailboxes: map[string]Mailbox{},
		messages:  map[string]map[string]Message{},
		raw:       map[string][]byte{},
		records:   map[string]Record{},
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
