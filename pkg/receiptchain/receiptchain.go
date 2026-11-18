// Package receiptchain is the protocol of an account's chain of receipts
// (ADR 0037) as a person checks it offline: the digests, the genesis, a
// link's document and its rules, the verdicts a walk gives, and an export's
// page and its contract. It holds no storage, no key and no network, so
// permaily-verify -receipts links it alone, and the receipts service
// (internal/services/receipts), which writes and reads the chain, uses the
// same definitions and checks.
package receiptchain

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ChainPurpose names the key that seals links, the catalogue's entries and
// the audit's passes (keys.Derive), and starts every Genesis.
const ChainPurpose = "permaily receipts chain v1"

// LinkVersion is the version of a link's sealed document; ExportVersion
// that of an export's pages.
const (
	LinkVersion   = 1
	ExportVersion = 1
)

// LinkReceiptsMax bounds the receipts one link names: an enclave groups the
// receipts of one account written while that account's previous link is
// being committed (ADR 0037, "Appending").
const LinkReceiptsMax = 64

// A page of a walk or of an export is bounded by WalkBytesMax of sealed
// entries read (SealedBytes of each document it holds, at least) and
// WalkLinksMax links visited (ADR 0037, "The bounds of a page").
const (
	WalkBytesMax = 8 << 20
	WalkLinksMax = 256
)

// SealedBytes is the size of a document of n bytes once sealed as text and
// stored (its version byte, commitment, nonce and tag, in base64 inside a
// JSON object): what a page charges for each document it holds against
// WalkBytesMax.
func SealedBytes(n int) int {
	return len(`{"sealed":""}`) + base64.StdEncoding.EncodedLen(1+sha256.Size+12+n+16)
}

// IDPrefix starts every receipt's identifier, which is IDPrefix, its time
// in 16 hexadecimal digits and IDRandomBytes random bytes in hexadecimal:
// IDLength characters.
const (
	IDPrefix      = "rcp_"
	IDRandomBytes = 10
	IDLength      = len(IDPrefix) + 16 + 2*IDRandomBytes
)

// ValidID says whether id has a receipt identifier's shape.
func ValidID(id string) bool {
	rest, ok := strings.CutPrefix(id, IDPrefix)
	if !ok || len(id) != IDLength {
		return false
	}
	for _, c := range rest {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

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
	// (ADR 0037, "Receipts written before the chain"): what was there when
	// it began, of which nothing says what was deleted before.
	LinkAdopted LinkKind = "adopted"
	// LinkRecovered chains receipts the audit found without a link some
	// time after they were written: a Write that failed after its entry, or
	// an earlier release's. Nothing says whether their answers left; from
	// this link on, deleting them shows.
	LinkRecovered LinkKind = "recovered"
)

// BreakReason is why a link, or another record of the chain, could not
// name the one before it by its digest.
type BreakReason string

const (
	BreakLost       BreakReason = "lost"       // its commit is there and its document is not, or is not what was committed
	BreakForked     BreakReason = "forked"     // two commits of different digests at its one version
	BreakUnreadable BreakReason = "unreadable" // its document does not open under this deployment's key for its name, or is not one
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
// named twice. Those bound its JSON whatever it holds. That adopted links
// come only at the chain's start, and that a receipt is named once across
// links of kind LinkReceipts and LinkAdopted, is the walk's and the
// audit's to check, which see the links around it.
func (l *Link) Check() error {
	switch {
	case l.V != LinkVersion:
		return fmt.Errorf("receipts: a link of version %d", l.V)
	case l.N == 0:
		return errors.New("receipts: links are numbered from 1")
	case l.Kind != LinkReceipts && l.Kind != LinkAdopted && l.Kind != LinkRecovered:
		return fmt.Errorf("receipts: a link of kind %q", l.Kind)
	case l.At.IsZero():
		return errors.New("receipts: a link says when it was written")
	}
	if err := CheckPrev(l.N, l.Prev, l.Break); err != nil {
		return err
	}
	switch {
	case l.Kind != LinkAdopted && (!l.Before.IsZero() || l.More):
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

// ParseLink reads doc strictly as link n's document: one JSON object of a
// link's fields and nothing after it, that passes Check, of number n. It
// is how an export's link is read offline; its digest is DigestOf(doc), of
// the bytes as given.
func ParseLink(doc []byte, n uint64) (*Link, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.DisallowUnknownFields()
	l := &Link{}
	if err := dec.Decode(l); err != nil {
		return nil, fmt.Errorf("receipts: not a link's document: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("receipts: more after a link's document")
	}
	if err := l.Check(); err != nil {
		return nil, err
	}
	if l.N != n {
		return nil, fmt.Errorf("receipts: link %d's document says it is link %d", n, l.N)
	}
	return l, nil
}

// CheckPrev is the rule of Prev and Break, a link's and every numbered
// record of the chain's alike: one of them, and number 1 by its genesis.
func CheckPrev(n uint64, prev *Digest, br BreakReason) error {
	switch {
	case (prev == nil) == (br == ""):
		return errors.New("receipts: a record of the chain names the one before it by its digest or by a break, and not both")
	case n == 1 && prev == nil:
		return errors.New("receipts: the first record of a chain names its genesis")
	case br != "" && br != BreakLost && br != BreakForked && br != BreakUnreadable:
		return fmt.Errorf("receipts: a break of reason %q", br)
	}
	return nil
}

// Position is where a receipt is on its account's chain: its link's number
// and its index in that link's Receipts. Positions order an account's
// receipts as the person sees them. As where a page begins (before), {N, 0}
// is before every receipt of link N: the page begins at link N-1. A page
// that ends at a link with no receipt to show (one that cannot be read)
// says so as its Next.
type Position struct {
	N     uint64 `json:"n"`
	Index int    `json:"index"`
}

// Head is the newest link of an account's chain: the highest number with a
// commit of this deployment's (0 for a chain not begun, with nothing else
// set). Verdict is what was found of it, as a walk's; Digest is what its
// commit names; At is set when it is LinkOK. A head a person noted is on
// every later chain: the later chain has, at N, a link of this Digest.
type Head struct {
	N       uint64      `json:"n"`
	Verdict LinkVerdict `json:"verdict,omitempty"`
	Digest  Digest      `json:"sha256,omitzero"`
	At      time.Time   `json:"at,omitzero"`
}

// CheckShown says whether h is a head as the person's routes show it, and
// an export's file holds it: number 0 with nothing else; or a verdict a
// head can have (LinkOK, LinkLost, LinkForked, LinkUnreadable: a head is
// read against its commit alone, so never LinkPrev or LinkExpired), with a
// digest and a time when, and only when, it is LinkOK.
func (h Head) CheckShown() error {
	healthy := h.Verdict == LinkOK
	switch {
	case h.N == 0 && (h.Verdict != "" || h.Digest != Digest{} || !h.At.IsZero()):
		return errors.New("receipts: the head of a chain not begun says nothing else")
	case h.N == 0:
		return nil
	case !healthy && h.Verdict != LinkLost && h.Verdict != LinkForked && h.Verdict != LinkUnreadable:
		return fmt.Errorf("receipts: a head of verdict %q", h.Verdict)
	case healthy != (h.Digest != Digest{}) || healthy != !h.At.IsZero():
		return errors.New("receipts: a head has a digest and a time when it is ok, and only then")
	}
	return nil
}

// LinkVerdict is what a walk found of one link. A link is only ever given
// a verdict on what was read of it: a storage that cannot be read now
// fails the page (an error), never a verdict.
type LinkVerdict string

const (
	LinkOK         LinkVerdict = "ok"
	LinkLost       LinkVerdict = "lost"       // committed, its document gone or not what was committed
	LinkForked     LinkVerdict = "forked"     // two commits at its version
	LinkUnreadable LinkVerdict = "unreadable" // its document does not open for its name, or is not a link (Check)
	LinkPrev       LinkVerdict = "prev"       // its digest is not what the next link's Prev names
	// LinkExpired: its history holds no commit any more (its lock ended and
	// it was deleted), and its document is what the next link's Prev names.
	// Its bytes are authenticated; that no other commit was ever made at its
	// number is not, since the evidence of a fork may have expired with it.
	// Not a break the storage made, and never LinkOK (ADR 0037, "The end of
	// the lock"; R5).
	LinkExpired LinkVerdict = "expired"
)

// Readable says whether a link of verdict v has a document a walk shows and
// receipts it checks: LinkOK, LinkPrev and LinkExpired.
func (v LinkVerdict) Readable() bool {
	return v == LinkOK || v == LinkPrev || v == LinkExpired
}

// Known says whether v is one of the verdicts of a link.
func (v LinkVerdict) Known() bool {
	return v.Readable() || v == LinkLost || v == LinkForked || v == LinkUnreadable
}

// ReceiptVerdict is what a walk found of one receipt a link names.
type ReceiptVerdict string

const (
	ReceiptOK         ReceiptVerdict = "ok"
	ReceiptMissing    ReceiptVerdict = "missing"    // no entry under its key
	ReceiptUnreadable ReceiptVerdict = "unreadable" // the entry does not open for its partition and key
	ReceiptChanged    ReceiptVerdict = "changed"    // it opens, and its document's digest is not the link's
)

// HasDocument says whether a receipt of verdict v has a document a walk
// read: ReceiptOK and ReceiptChanged.
func (v ReceiptVerdict) HasDocument() bool {
	return v == ReceiptOK || v == ReceiptChanged
}

// Known says whether v is one of the verdicts of a receipt.
func (v ReceiptVerdict) Known() bool {
	return v.HasDocument() || v == ReceiptMissing || v == ReceiptUnreadable
}

// ExportPage is one page of an export (ADR 0037, "What a person can
// verify"): the chain's head when it was read, and the links and receipts
// the page holds, the newest first, each document as its exact bytes as
// stored before sealing (base64 in JSON), never marshalled again, so a
// receipt of a kind or version this build does not know keeps every byte.
// An export is its pages in order, one JSON object a line.
type ExportPage struct {
	V       int            `json:"v"`
	Account string         `json:"account"`
	Head    Head           `json:"head"`
	Links   []ExportedLink `json:"links"`
	Next    Position       `json:"next"`
	Done    bool           `json:"done,omitempty"`
}

// ExportedLink is one link of an export: Doc is set unless its verdict
// says it could not be read, and Receipts are those of it the page holds.
type ExportedLink struct {
	N        uint64            `json:"n"`
	Verdict  LinkVerdict       `json:"verdict"`
	Doc      []byte            `json:"doc,omitempty"`
	Receipts []ExportedReceipt `json:"receipts,omitempty"`
}

// ExportedReceipt is one receipt of an export, at Index in its link: Doc
// is set unless its verdict is ReceiptMissing or ReceiptUnreadable.
type ExportedReceipt struct {
	Index   int            `json:"index"`
	ID      string         `json:"id"`
	Verdict ReceiptVerdict `json:"verdict"`
	Doc     []byte         `json:"doc,omitempty"`
}

// Bytes is what p's documents charge against WalkBytesMax: SealedBytes of
// each document it holds. What the service read to make it is at least
// that.
func (p *ExportPage) Bytes() int {
	total := 0
	for _, l := range p.Links {
		if l.Doc != nil {
			total += SealedBytes(len(l.Doc))
		}
		for _, r := range l.Receipts {
			if r.Doc != nil {
				total += SealedBytes(len(r.Doc))
			}
		}
	}
	return total
}

// Check says whether p keeps the contract of a page, as every
// implementation of the chain must and permaily-verify -receipts requires
// of a file: its version; within WalkLinksMax links and WalkBytesMax; the
// head's number and verdict, a verdict only for a chain begun; links in
// descending number below the head, receipts in each in descending index,
// each of a verdict the protocol has; a document only where its verdict
// says one was read; Next set only on a page that is not Done, which holds
// a link, and no higher than where the page's last receipt or link is.
func (p *ExportPage) Check() error {
	switch {
	case p.V != ExportVersion:
		return fmt.Errorf("receipts: an export's page of version %d", p.V)
	case len(p.Links) > WalkLinksMax:
		return fmt.Errorf("receipts: an export's page of %d links", len(p.Links))
	case p.Bytes() > WalkBytesMax:
		return fmt.Errorf("receipts: an export's page of %d bytes sealed", p.Bytes())
	case p.Done != (p.Next == Position{}):
		return errors.New("receipts: an export's page is done or says where the next begins, not both")
	case !p.Done && len(p.Links) == 0:
		return errors.New("receipts: an export's page that is not done holds a link, so the next moves on")
	case (p.Head.N == 0) != (p.Head.Verdict == "") || (p.Head.N != 0 && !p.Head.Verdict.Known()):
		return fmt.Errorf("receipts: an export's head of verdict %q", p.Head.Verdict)
	}
	// The first link is at most the head, every other below the one before;
	// the head's number is never added to, so a head at the last number a
	// chain can have does not wrap to 0.
	last := Position{N: p.Head.N}
	for i, l := range p.Links {
		if l.N == 0 || l.N > last.N || (i > 0 && l.N == last.N) || !l.Verdict.Known() || (l.Doc != nil) != l.Verdict.Readable() || (!l.Verdict.Readable() && len(l.Receipts) > 0) {
			return fmt.Errorf("receipts: an export's link %d out of order or of a verdict it does not keep", l.N)
		}
		last = Position{N: l.N, Index: LinkReceiptsMax}
		for _, r := range l.Receipts {
			if r.Index < 0 || r.Index >= last.Index || !r.Verdict.Known() || (r.Doc != nil) != r.Verdict.HasDocument() {
				return fmt.Errorf("receipts: an export's receipt %d of link %d out of order or of a verdict it does not keep", r.Index, l.N)
			}
			last.Index = r.Index
		}
		if len(l.Receipts) == 0 {
			last.Index = 0
		}
	}
	if !p.Done && (p.Next.N > last.N || (p.Next.N == last.N && p.Next.Index > last.Index)) {
		return errors.New("receipts: an export's page continues above where it ended")
	}
	return nil
}
