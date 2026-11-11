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

// ErrConflict is returned by PutEntry when the entry changed since it was read.
var ErrConflict = errors.New("store: changed since it was read")

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
	// Plain says Value is text meant to be read by whoever runs the storage
	// (envelope.Sign), and is stored so that it can be: an account's record.
	Plain     bool
	UpdatedAt time.Time
}

// Records persists the enclave's own records.
type Records interface {
	GetRecord(ctx context.Context, name string) (*Record, error)
	// PutRecord stores r. With create it fails with ErrExists when the record
	// is already there, so two enclaves starting together cannot both create it.
	PutRecord(ctx context.Context, r *Record, create bool) error
}

// RecordTable is Records that can also be listed and deleted from: where
// Anchored keeps each version of a record under a name of its own.
type RecordTable interface {
	Records
	// ListRecords returns up to limit names of the records whose name starts
	// with prefix, in order.
	ListRecords(ctx context.Context, prefix string, limit int) ([]string, error)
	// DeleteRecord deletes a record. One that is not there is deleted already.
	DeleteRecord(ctx context.Context, name string) error
}

// Entry is a small row that belongs to an account or leads to one: a session,
// an invitation, the pointer from an address to its account. Value is text
// (JSON); what must not be made or changed by hand is signed by the caller
// (envelope.Sign), and what must not be read is not stored here.
type Entry struct {
	// Partition groups the entries that are listed together; Key names one in it.
	Partition string
	Key       string
	Value     []byte
	// Version counts the writes. PutEntry creates at 0 and otherwise replaces
	// only the version the entry was read at.
	Version uint64
	// ExpiresAt is when the entry stops existing; zero for never.
	ExpiresAt time.Time
	UpdatedAt time.Time
}

// Entries persists entries.
type Entries interface {
	// GetEntry returns ErrNotFound for an entry that is not there or expired.
	GetEntry(ctx context.Context, partition, key string) (*Entry, error)
	// PutEntry stores e and sets e.Version to the version stored. With
	// Version 0 the entry must be new (ErrExists); otherwise it must still be
	// at e.Version (ErrConflict), so two writers never undo each other.
	PutEntry(ctx context.Context, e *Entry) error
	// DeleteEntry returns ErrNotFound when there was nothing to delete: of
	// two callers, only one deletes.
	DeleteEntry(ctx context.Context, partition, key string) error
	// ListEntries returns up to limit entries of a partition whose key starts
	// with prefix and comes after the key after ("" for the first ones), in
	// the order of their keys, without the expired ones.
	ListEntries(ctx context.Context, partition, prefix, after string, limit int) ([]Entry, error)
	// DeletePartition deletes every entry of a partition, the expired ones
	// too: what is left of an account that was removed.
	DeletePartition(ctx context.Context, partition string) error
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
