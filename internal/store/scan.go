package store

import (
	"context"
	"errors"
)

// What ADR 0026 adds to the store, for mailboxes per account. The types and
// the interface are the seam; *Memory and *AWS implement Scanner, and
// Anchored returns ErrForked and gains CreateNew and ForkIsLost, in the store
// pull request that lands first (ADR 0026, "Implementation pull requests",
// pull request 0). Nothing returns ErrForked or implements Scanner before it.

// ErrForked means two commits of this deployment are at the newest version of
// a record. A commit is written only where nothing is (S3Commits.Append
// creates its object with If-None-Match), so two can only be there if whoever
// writes to the bucket hid one behind a delete marker while an enclave wrote
// the second. Every read and write of the record fails with it, on every
// enclave and after every restart: both commits stay in the bucket, which
// nobody can delete from. It is not an ErrNotFound: nothing makes the record
// again, unless the Anchored that reads it is set to take a fork for a loss
// (ForkIsLost), which only the TLS identity is (ADR 0016, ADR 0024).
var ErrForked = errors.New("store: two commits at the newest version of a record: the store of commits was written to by someone else")

// MaxScanRead bounds what one scan reads: a DynamoDB page of items of about
// 18 KiB (a sealed search entry) stays under DynamoDB's 1 MB a page and a
// couple of MiB in memory.
const MaxScanRead = 100

// EntryScan is one page of a scan of entries, from the greatest key down.
type EntryScan struct {
	// Entries are the entries read that have not expired, the greatest key
	// first. Never nil.
	Entries []Entry
	// Read is how many items the store read, the expired ones included: what
	// the page cost, and what a caller bounding its work counts.
	Read int
	// Next is the key of the last item read, to scan on below it, when the
	// scan stopped because it read maxRead items; "" when nothing with the
	// prefix is left below.
	Next string
}

// MessageScan is one page of a scan of a mailbox's messages, the newest first.
type MessageScan struct {
	// Messages are the messages read that have not expired, the newest
	// first. Never nil.
	Messages []Message
	// Read is how many message items were read, the expired ones included.
	Read int
	// Next is the identifier of the last message read, to scan on below it,
	// when the scan stopped because it read maxRead items; "" when no older
	// message is left.
	Next string
}

// Scanner reads a partition from the greatest key down, reading a bounded
// number of items whatever they are: a partition full of expired items that
// DynamoDB has not deleted yet costs one bounded page, not a walk through all
// of them (ADR 0026, "Searching").
type Scanner interface {
	// ScanEntriesDown reads at most maxRead items (1 to MaxScanRead, or an
	// error) of the partition whose key starts with prefix and is strictly
	// below before ("" for from the greatest), in descending order of key,
	// and returns the ones that have not expired. A page may hold no entry
	// and still have a Next. The read is consistent (what a write that
	// returned wrote is read).
	ScanEntriesDown(ctx context.Context, partition, prefix, before string, maxRead int) (*EntryScan, error)
	// ScanMessagesDown is ScanEntriesDown for the messages of a mailbox
	// (sk=msg#<id>), with before a message identifier.
	ScanMessagesDown(ctx context.Context, mailboxID, before string, maxRead int) (*MessageScan, error)
}
