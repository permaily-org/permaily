package receipts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The chain of an account's receipts (ADR 0034). Every receipt is still one
// sealed entry of Partition(account), written once, before the answer, as
// ADR 0031 says; what this adds is a link, written after the entry and
// before Write returns, that names it. An account's links are numbered from
// 1 with no gap, and link n is a strict anchored record of its own,
// LinkName(account, n), made once with store.Anchored.CreateNew and never
// changed: its one commit, locked in the anchors bucket, says it was made,
// so a link cannot be removed, put back as another or made twice. Each link
// names the receipts it chains by identifier and digest, and the link
// before it by the digest of its document (Prev), so whoever has the
// documents can check the whole chain from Genesis to its head without
// trusting the storage, and the enclave needs a strict read only for the
// head and below a break. A receipt deleted, replaced, moved or put back is
// then a receipt its link names that is missing or does not match; a link
// deleted or replaced is a number whose commit is there and whose document
// is not, or does not match the next one's Prev. Nothing of it is the kind
// of a receipt: a link names documents, whatever they record.
//
// This file is the seam, committed with the design; ADR 0034's pull
// request 1 writes the appender, the link table and the walk.

// ChainPurpose names the key that seals links (keys.Derive), and is the
// start of every account's Genesis.
const ChainPurpose = "permaily receipts chain v1"

// LinkVersion is the version of a link's sealed document.
const LinkVersion = 1

// LinkReceiptsMax bounds the receipts one link names: an enclave groups the
// receipts of one account written while that account's previous link is
// being committed (ADR 0034, "Appending").
const LinkReceiptsMax = 64

// LinkBytesMax bounds one link's JSON document before it is sealed; a link
// with LinkReceiptsMax receipts is far below it (TestTheLargestLinkFits).
const LinkBytesMax = 16 << 10

// WalkBytesMax bounds the sealed entries one page of a walk reads: about 24
// receipts at the largest a receipt may be, and every receipt of a link at
// their usual size.
const WalkBytesMax = 8 << 20

// LinkPrefix starts the keys of an account's links in Partition(account),
// apart from IDPrefix, so List never meets one and DeletePartition removes
// them with the receipts.
const LinkPrefix = "lnk_"

// LinkTries bounds how many link numbers an appender tries for one group
// before the writes of its receipts fail: each try lost is a link another
// enclave committed first, which this one reads and goes after.
const LinkTries = 8

// AdoptAfter is how far before an account's first link a receipt written
// without a link must be to be adopted by it (ADR 0034, "Receipts written
// before the chain"): longer than a receipt's write takes and two
// enclaves' clocks differ. A receipt without a link written after it is
// left unlinked, and shown so.
const AdoptAfter = time.Minute

// LinkName is the strict anchored record of an account's link n, from 1.
func LinkName(account string, n uint64) string {
	return linkNamePrefix + account + "-" + strconv.FormatUint(n, 10)
}

const linkNamePrefix = "receipt-link-"

// ParseLinkName is LinkName's account and number; ok is false for a name
// that is not a link's.
func ParseLinkName(name string) (account string, n uint64, ok bool) {
	rest, found := strings.CutPrefix(name, linkNamePrefix)
	if !found {
		return "", 0, false
	}
	i := strings.LastIndexByte(rest, '-')
	if i <= 0 {
		return "", 0, false
	}
	account, digits := rest[:i], rest[i+1:]
	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != digits {
		return "", 0, false
	}
	return account, n, true
}

// LinkKey is where one stored version of a link's record is kept: name is a
// link's record name followed by the suffix store.Anchored gives each
// version ("@<version>-<hash>"), and the key is in the account's own
// partition, LinkPrefix and the number in 16 hexadecimal digits (so the
// keys' order is the numbers'), then the suffix. A link's plain name has no
// key (ok false): links are made with CreateNew and never stored under it,
// so nothing anyone writes there is ever read, and junk under it cannot
// stop a link from being made.
func LinkKey(name string) (partition, key string, ok bool) {
	base, suffix, versioned := strings.Cut(name, "@")
	if !versioned || suffix == "" {
		return "", "", false
	}
	account, n, ok := ParseLinkName(base)
	if !ok {
		return "", "", false
	}
	return Partition(account), LinkKeyPrefix(n) + "@" + suffix, true
}

// LinkKeyPrefix starts the keys of every stored version of link n:
// whatever another enclave staged for the same number is under it too, and
// the one the chain names is the one whose digest the next link's Prev, or
// the commit, says.
func LinkKeyPrefix(n uint64) string { return LinkPrefix + fmt.Sprintf("%016x", n) }

// Digest is the SHA-256 of a document's bytes as stored, before sealing:
// what a person recomputes from an export. In JSON it is lowercase
// hexadecimal.
type Digest [sha256.Size]byte

// DigestOf is the digest of doc's bytes, as they are; never of a document
// marshalled again.
func DigestOf(doc []byte) Digest { return sha256.Sum256(doc) }

// Genesis is what an account's link 1 names as the link before it: the
// SHA-256 of ChainPurpose, a zero byte and the account's identifier.
func Genesis(account string) Digest {
	return sha256.Sum256([]byte(ChainPurpose + "\x00" + account))
}

func (d Digest) String() string { return hex.EncodeToString(d[:]) }

// MarshalText implements encoding.TextMarshaler.
func (d Digest) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler: 64 lowercase
// hexadecimal digits, nothing else.
func (d *Digest) UnmarshalText(b []byte) error {
	if len(b) != 2*sha256.Size {
		return errors.New("receipts: a digest is 64 hexadecimal digits")
	}
	for _, c := range b {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return errors.New("receipts: a digest is lowercase hexadecimal")
		}
	}
	_, err := hex.Decode(d[:], b)
	return err
}

// LinkKind says what a link chains.
type LinkKind string

const (
	// LinkReceipts chains receipts written through the chain: Write
	// returned for each only once this link was committed.
	LinkReceipts LinkKind = "receipts"
	// LinkAdopted chains receipts written before the account's chain began
	// (ADR 0034, "Receipts written before the chain"): what was there when
	// it began, of which nothing says what was deleted before.
	LinkAdopted LinkKind = "adopted"
)

// BreakReason is why a link could not name the link before it by its
// digest.
type BreakReason string

const (
	BreakLost       BreakReason = "lost"       // its commit is there and its document is not, or is not what was committed
	BreakForked     BreakReason = "forked"     // two commits of different digests at its one version
	BreakUnreadable BreakReason = "unreadable" // its document does not open under this deployment's key for its name, or is not a link
)

// Ref is one receipt a link chains: its identifier (its key) and the digest
// of its document as stored.
type Ref struct {
	ID     string `json:"id"`
	Digest Digest `json:"sha256"`
}

// Link is one link's sealed document.
type Link struct {
	V    int      `json:"v"`
	N    uint64   `json:"n"`
	Kind LinkKind `json:"kind"`
	// At is when the enclave that committed it began its write, by its
	// clock.
	At time.Time `json:"at"`
	// Prev is the digest of link N-1's document, Genesis for link 1. Break
	// is set instead when link N-1 could not be read: the chain goes on,
	// and the gap stays where its commit is.
	Prev  *Digest     `json:"prev,omitempty"`
	Break BreakReason `json:"break,omitempty"`
	// Before, on adopted links, is the instant link 1 took: the receipts
	// adopted are those without a link whose identifier's time is earlier,
	// in their keys' order. More says that another adopted link follows.
	Before time.Time `json:"before,omitzero"`
	More   bool      `json:"more,omitempty"`
	// Receipts are what it chains, in the order the person sees them.
	Receipts []Ref `json:"receipts"`
}

// Check says whether l is a link's document as one is written: its version,
// number, kind, the link before named by its digest or by a break (and by
// its digest alone for link 1), adoption's fields only where they belong,
// one to LinkReceiptsMax receipts of a receipt identifier's shape, none
// named twice. Those bound its JSON within LinkBytesMax whatever it holds
// (TestTheLargestLinkFits). That an adopted link
// comes only at the chain's start is the walk's to check, which sees the
// links around it.
func (l *Link) Check() error {
	switch {
	case l.V != LinkVersion:
		return fmt.Errorf("receipts: a link of version %d", l.V)
	case l.N == 0:
		return errors.New("receipts: links are numbered from 1")
	case l.Kind != LinkReceipts && l.Kind != LinkAdopted:
		return fmt.Errorf("receipts: a link of kind %q", l.Kind)
	case l.At.IsZero():
		return errors.New("receipts: a link says when it was written")
	case (l.Prev == nil) == (l.Break == ""):
		return errors.New("receipts: a link names the link before it by its digest or by a break, and not both")
	case l.N == 1 && l.Prev == nil:
		return errors.New("receipts: link 1 names the account's genesis")
	case l.Break != "" && l.Break != BreakLost && l.Break != BreakForked && l.Break != BreakUnreadable:
		return fmt.Errorf("receipts: a break of reason %q", l.Break)
	case l.Kind == LinkReceipts && (!l.Before.IsZero() || l.More):
		return errors.New("receipts: only adopted links say what they adopt")
	case l.Kind == LinkAdopted && l.Before.IsZero():
		return errors.New("receipts: an adopted link says before when it adopts")
	case len(l.Receipts) == 0 || len(l.Receipts) > LinkReceiptsMax:
		return fmt.Errorf("receipts: a link names 1 to %d receipts, not %d", LinkReceiptsMax, len(l.Receipts))
	}
	seen := make(map[string]bool, len(l.Receipts))
	for _, r := range l.Receipts {
		if !ValidID(r.ID) {
			return errors.New("receipts: a link names something that is not a receipt")
		}
		if seen[r.ID] {
			return errors.New("receipts: a link names a receipt twice")
		}
		seen[r.ID] = true
	}
	return nil
}

// Position is where a receipt is on its account's chain: its link's number
// and its index in that link's Receipts. Positions order an account's
// receipts as the person sees them.
type Position struct {
	N     uint64 `json:"n"`
	Index int    `json:"index"`
}

// Head is the newest link of an account's chain: its number (0 for a chain
// not begun), the digest of its document, and when it was written. A head a
// person noted is extended by every later one: the later chain has, at
// N, a link of this Digest.
type Head struct {
	N      uint64    `json:"n"`
	Digest Digest    `json:"sha256"`
	At     time.Time `json:"at,omitzero"`
}

// LinkVerdict is what a walk found of one link.
type LinkVerdict string

const (
	LinkOK         LinkVerdict = "ok"
	LinkLost       LinkVerdict = "lost"       // committed, its document gone or not what was committed
	LinkForked     LinkVerdict = "forked"     // two commits at its version
	LinkUnreadable LinkVerdict = "unreadable" // its document does not open for its name, or is not a link (Check)
	LinkPrev       LinkVerdict = "prev"       // its digest is not what the next link's Prev names
)

// ReceiptVerdict is what a walk found of one receipt a link names.
type ReceiptVerdict string

const (
	ReceiptOK         ReceiptVerdict = "ok"
	ReceiptMissing    ReceiptVerdict = "missing"    // no entry under its key
	ReceiptUnreadable ReceiptVerdict = "unreadable" // the entry does not open for its partition and key (ErrUnreadable)
	ReceiptChanged    ReceiptVerdict = "changed"    // it opens, and its document's digest is not the link's
)

// WalkedLink is one link a walk read, with what it found of it and of the
// receipts of it the page holds; Link is nil unless Verdict is LinkOK or
// LinkPrev, and Receipts is then empty: a link that cannot be read names
// nothing anyone can check.
type WalkedLink struct {
	N        uint64          `json:"n"`
	Verdict  LinkVerdict     `json:"verdict"`
	Link     *Link           `json:"link,omitempty"`
	Receipts []WalkedReceipt `json:"receipts,omitempty"`
}

// WalkedReceipt is one receipt a link names, at Index in its Receipts:
// Receipt is set when Verdict is ReceiptOK.
type WalkedReceipt struct {
	Index   int            `json:"index"`
	Ref     Ref            `json:"ref"`
	Verdict ReceiptVerdict `json:"verdict"`
	Receipt *Receipt       `json:"receipt,omitempty"`
}

// Walked is one page of a walk, the newest first: links in descending
// number, and in each the receipts in descending index.
type Walked struct {
	// Head is the chain's head when the page was read.
	Head  Head         `json:"head"`
	Links []WalkedLink `json:"links"`
	// Next is the last position the page holds, where the next page
	// continues (before it); Done says the page reached link 1's first
	// receipt and nothing is left.
	Next Position `json:"next"`
	Done bool     `json:"done,omitempty"`
}

// Chain is an account's chain of receipts (ADR 0034). The receipts service
// is its one implementation, over the entries of Partition(account) and a
// strict store.Anchored that reads and writes link names and nothing else.
type Chain interface {
	// Link returns once a committed link of account names ref, a receipt
	// already stored, at the position it returns; an error says that
	// nothing is known to name it, and its request gives nothing. Refs of
	// one account given at once are grouped into one link, up to
	// LinkReceiptsMax; ctx bounds the wait. A link whose commit's outcome
	// is unknown may name ref and still return an error: a receipt may
	// record more than left, never less.
	Link(ctx context.Context, account string, ref Ref) (Position, error)
	// Head is account's head, read strictly and now.
	Head(ctx context.Context, account string) (Head, error)
	// Walk reads account's receipts before the position before (the zero
	// Position for from the head), the newest first, up to limit receipts
	// and WalkBytesMax of entries read, and says what it found of each
	// link and receipt: every link is checked against the next one's Prev,
	// or strictly where there is no next one or it names a break; every
	// receipt against its Ref. It fails only when the head cannot be read;
	// a link or a receipt it cannot read is a verdict, never an error and
	// never left out. A page ends inside a link when its bounds say so, and
	// the next continues from Next: nothing is skipped.
	Walk(ctx context.Context, account string, before Position, limit int) (*Walked, error)
}
