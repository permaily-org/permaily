// Package store persists mailboxes and messages outside the enclave. It only
// ever receives sealed (encrypted) values plus the minimum plaintext needed to
// index and expire them: ids, timestamps, sizes and the retention period.
package store

import (
	"context"
	"errors"
	"slices"
	"time"
)

// ErrNotFound is returned when a mailbox, message or record does not exist (or has expired).
var ErrNotFound = errors.New("store: not found")

// ErrExists is returned by PutRecord when the record must be new and is not.
var ErrExists = errors.New("store: already exists")

// RetentionDays are the supported retention periods. S3 has one lifecycle rule
// per value, so a message is deleted after its mailbox's period at the time it
// was received.
var RetentionDays = []int{7, 30, 90, 180, 365}

// DefaultRetentionDays applies until the user changes it.
const DefaultRetentionDays = 90

// ValidRetention reports whether days is a supported retention period.
func ValidRetention(days int) bool { return slices.Contains(RetentionDays, days) }

// Mailbox is a stored mailbox. WrappedKey is the mailbox data key wrapped by
// KMS; State is sealed with the data key.
type Mailbox struct {
	ID            string
	WrappedKey    []byte
	RetentionDays int
	State         []byte
	UpdatedAt     time.Time
}

// Message is a stored message. Meta is sealed with the mailbox data key; the
// raw message (also sealed) is stored separately.
type Message struct {
	MailboxID     string
	ID            string
	ReceivedAt    time.Time
	ExpiresAt     time.Time
	Size          int
	RetentionDays int
	Meta          []byte
}

// Record is a small sealed value that belongs to the enclave itself rather
// than to a mailbox (its TLS identity). WrappedKey is the record's data key
// wrapped by KMS; Value is sealed with that key.
type Record struct {
	Name       string
	WrappedKey []byte
	Value      []byte
	UpdatedAt  time.Time
}

// Records persists the enclave's own records.
type Records interface {
	GetRecord(ctx context.Context, name string) (*Record, error)
	// PutRecord stores r. With create it fails with ErrExists when the record
	// is already there, so two enclaves starting together cannot both create it.
	PutRecord(ctx context.Context, r *Record, create bool) error
}

// Store is the persistence interface used by the inbox.
type Store interface {
	GetMailbox(ctx context.Context, id string) (*Mailbox, error)
	PutMailbox(ctx context.Context, m *Mailbox) error
	// PutMessage stores the record and its sealed raw message.
	PutMessage(ctx context.Context, m *Message, raw []byte) error
	// ListMessages returns up to limit messages, newest first, with ids
	// strictly lower than before (all when before is empty).
	ListMessages(ctx context.Context, mailboxID string, limit int, before string) ([]Message, error)
	GetMessage(ctx context.Context, mailboxID, id string) (*Message, []byte, error)
	DeleteMessage(ctx context.Context, mailboxID, id string) error
}
