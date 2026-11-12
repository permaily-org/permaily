package store

import "context"

// What ADR 0026 adds to the store for reading a partition newest first. The
// types and the interface are the seam; *Memory and *AWS implement Scanner
// in the store pull request that lands first (ADR 0026, "Implementation pull
// requests", pull request 0). Nothing implements Scanner before it.

// MaxScanRead bounds what one scan reads: a DynamoDB page of items of about
// 18 KiB (a sealed search entry) stays near DynamoDB's 1 MB a page and a
// couple of MiB in memory.
const MaxScanRead = 100

// EntryScan is one page of a scan of entries, from the greatest key down.
type EntryScan struct {
	// Entries are the entries read that have not expired, the greatest key
	// first. Never nil.
	Entries []Entry
	// Read is how many items the store read (DynamoDB's ScannedCount), the
	// expired ones included: what the page cost, and what a caller bounding
	// its work counts. It may be below maxRead even when More is true:
	// DynamoDB stops a page at 1 MB of items read whatever the limit.
	Read int
	// Last is the key of the last item the scan read, expired or not; ""
	// only when it read nothing. A caller that takes every entry of the page
	// goes on below Last; a caller that stops inside the page goes on below
	// the last entry it took, never below Last, or it skips the rest.
	Last string
	// More reports whether items with the prefix may be left below Last. It
	// is DynamoDB's LastEvaluatedKey being set, never "Read reached
	// maxRead": a page cut at 1 MB has More with fewer items read, and a
	// page that read exactly maxRead may have nothing below it.
	More bool
}

// MessageScan is one page of a scan of a mailbox's messages, the newest first.
type MessageScan struct {
	// Messages are the messages read that have not expired, the newest
	// first. Never nil.
	Messages []Message
	// Read is how many message items were read, the expired ones included.
	Read int
	// Last is the identifier of the last message item read, expired or not
	// (as EntryScan.Last); "" only when the scan read nothing. Message
	// identifiers are time first, so it also says how far back the page
	// looked when every item it read had expired.
	Last string
	// More is as EntryScan.More.
	More bool
}

// Scanner reads a partition from the greatest key down, reading a bounded
// number of items whatever they are: a partition full of expired items that
// DynamoDB has not deleted yet costs one bounded page, not a walk through all
// of them (ADR 0026, "Searching").
type Scanner interface {
	// ScanEntriesDown reads at most maxRead items (1 to MaxScanRead, or an
	// error) of the partition whose key starts with prefix and is strictly
	// below before ("" for from the greatest), in descending order of key,
	// in one request, and returns the ones that have not expired. A page may
	// hold no entry and still have More. The read is consistent (what a
	// write that returned wrote is read).
	ScanEntriesDown(ctx context.Context, partition, prefix, before string, maxRead int) (*EntryScan, error)
	// ScanMessagesDown is ScanEntriesDown for the messages of a mailbox
	// (sk=msg#<id>), with before a message identifier.
	ScanMessagesDown(ctx context.Context, mailboxID, before string, maxRead int) (*MessageScan, error)
}
