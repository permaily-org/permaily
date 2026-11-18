package receipts

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
	"github.com/permaily-org/permaily/pkg/receiptchain"
)

// The chain of an account's receipts (ADR 0037). Every receipt is still one
// sealed entry of Partition(account), written once, before the answer, as
// ADR 0031 says; what this adds is a link, written after the entry and
// before Write returns, that names it. An account's links are numbered from
// 1 with no gap, and link n is a strict anchored record of its own,
// LinkName(account, n), made once with store.Anchored.CreateNew and never
// changed: its one commit, locked in the anchors bucket, says it was made,
// so a link cannot be removed, put back as another or made twice. Each link
// names the receipts it chains by identifier and digest, and the link
// before it by the digest of its document (Prev), so whoever has the
// documents and a head they trust can check the chain below it.
//
// The commits of an account's links are kept together in the anchors
// bucket (CommitGroup), so one listing reads the full history of every link
// of a range: a walk and the audit read every link they report strictly
// (ADR 0037, "Changed after the design review", F1), and find the head by
// listing, not by numbers' contiguity (F4, F7). The chains that exist are
// named by a catalogue, a numbered series of strict records of the same
// kind, which an account's chain is entered in before its link 1 is made
// (F2); the audit's finished passes are a third series, read by every
// enclave for /status; and what any enclave found broken is a fourth, the
// record of incidents, which only grows, so a finding outlives a clean pass,
// a restart and the account (the second design review, R1).
//
// This file is the seam, committed with the design; ADR 0037's pull
// request 1 writes the appender, the record table, the walk, the export and
// the audit.

// ChainPurpose names the key that seals links, the catalogue's entries and
// the audit's passes (keys.Derive), and starts every Genesis.
const ChainPurpose = receiptchain.ChainPurpose

// LinkVersion is the version of a link's sealed document; CatalogueVersion,
// AuditVersion, IncidentVersion and ExportVersion are those of the
// catalogue's entries, the audit's passes, the record of incidents' entries
// and an export's pages.
const (
	LinkVersion      = receiptchain.LinkVersion
	CatalogueVersion = 1
	AuditVersion     = 1
	IncidentVersion  = 1
	ExportVersion    = receiptchain.ExportVersion
)

// LinkReceiptsMax bounds the receipts one link names: an enclave groups the
// receipts of one account written while that account's previous link is
// being committed (ADR 0037, "Appending"). LinkWaitingMax bounds the
// receipts of one account waiting for a link in one enclave, the group in
// flight not counted: a write past it fails at once, closed.
const (
	LinkReceiptsMax = receiptchain.LinkReceiptsMax
	LinkWaitingMax  = 4 * LinkReceiptsMax
)

// CatalogueAccountsMax bounds the accounts one entry of the catalogue
// names, as LinkReceiptsMax bounds a link's receipts; IncidentFindingsMax
// the findings one entry of the record of incidents names.
const (
	CatalogueAccountsMax = 64
	IncidentFindingsMax  = 64
)

// LinkBytesMax bounds one link's, one catalogue entry's, one audit pass's
// and one incident entry's JSON document before it is sealed; the largest
// of each is far below it (TestTheLargestLinkFits).
const LinkBytesMax = 16 << 10

// A page of a walk or of an export is bounded by each of these, apart from
// the receipts a walk asks for: WalkBytesMax of sealed entries read
// (SealedBytes of each document it holds, at least), WalkLinksMax links
// visited (a link that cannot be read counts as one that can), and
// WalkListedMax objects listed where the links' commits are, in one try
// (delete markers and objects that are not commits count). A page that
// reaches the bytes or the links ends there, with Next, and is never Done.
// A page reports only links whose full history it listed: one whose
// listing reaches WalkListedMax before the history of its top link is
// complete lists again over half as many links, and one link whose history
// alone passes it fails the page (ErrHistoryTooLarge), never a verdict
// (ADR 0037, "The bounds of a page"; R4). Finding the head and the audit
// list without that bound, within their deadlines.
const (
	WalkBytesMax  = receiptchain.WalkBytesMax
	WalkLinksMax  = receiptchain.WalkLinksMax
	WalkListedMax = 4000
)

// ErrHistoryTooLarge is a page whose one link has more objects listed
// where its commits are than WalkListedMax: what was listed is not a
// verdict, and the page fails, as a storage that cannot be read does.
var ErrHistoryTooLarge = errors.New("receipts: a link's history is larger than a page lists")

// SealedBytes is the size of a document of n bytes once sealed as text and
// stored (envelope.SealText: its version byte, commitment, nonce and tag,
// in base64 inside a JSON object): what a page charges for each document
// it holds against WalkBytesMax, and what receiptstest.Fake charges too.
func SealedBytes(n int) int { return receiptchain.SealedBytes(n) }

// LinkPrefix starts the keys of an account's links in Partition(account),
// apart from IDPrefix, so List never meets one and DeletePartition removes
// them with the receipts. CatalogueKeyPrefix, AuditKeyPrefix and
// IncidentKeyPrefix start the keys of the catalogue's entries in
// CataloguePartition, of the audit's passes in AuditPartition and of the
// record of incidents' entries in IncidentPartition.
const (
	LinkPrefix         = "lnk_"
	CatalogueKeyPrefix = "cat_"
	AuditKeyPrefix     = "pass_"
	IncidentKeyPrefix  = "inc_"
)

// CataloguePartition, AuditPartition and IncidentPartition hold the values
// of the catalogue's entries, of the audit's passes and of the record of
// incidents' entries. None is an account's: removing an account removes
// none.
const (
	CataloguePartition = "receipts-catalogue"
	AuditPartition     = "receipts-audit"
	IncidentPartition  = "receipts-incidents"
)

// LinkTries bounds how many link numbers an appender tries for one group
// before the writes of its receipts fail: each try lost is a link another
// enclave committed first, which this one reads and goes after.
const LinkTries = 8

// AdoptAfter is how far before an account's first link a receipt written
// without a link must be to be adopted by it (ADR 0037, "Receipts written
// before the chain"): longer than a receipt's write takes and two
// enclaves' clocks differ.
const AdoptAfter = time.Minute

// RecoverAfter is how old a receipt no link names must be, by its
// identifier's time, before the audit links it in a link of kind
// LinkRecovered: far longer than any write of a receipt and its link may
// take, so no Write still waits for it. RecoverWithin is how old it may be
// at most: three daily passes, so a pass or two that did not finish lose
// nothing. A receipt without a link older than that is counted unlinked by
// the audit and never recovered (ADR 0037, "Receipts left without a
// link"; R2), so the audit's memory of which receipts links name is
// bounded by what an account wrote in RecoverWithin.
const (
	RecoverAfter  = 10 * time.Minute
	RecoverWithin = 72 * time.Hour
)

// AuditWindowRefsMax bounds the refs the audit holds for one chain while it
// joins links' refs with the stored receipts: those of links written within
// RecoverWithin and AdoptAfter of the receipts it is reading. A chain that
// needs more fails the pass, which then does not finish (R2, R6).
const AuditWindowRefsMax = 1 << 16

// AuditWriteAfterCheck bounds how long after the audit asked whether an
// account is being removed it may begin a write for that account (a
// recovered or adopted link, an entry of the catalogue, link 1): with the
// write's own ReceiptWriteMax, far within the ten minutes between account
// removal's two sweeps of the partition, so the second sweep removes any
// value it stored (R3).
const AuditWriteAfterCheck = time.Minute

// AuditEvery is how often a pass of the audit begins, and AuditRunMax how
// long one run of it may take; a pass not finished in a run begins again in
// the next, an hour later (ADR 0037, "The audit").
const (
	AuditEvery  = 24 * time.Hour
	AuditRunMax = time.Hour
)

// The three series of numbered strict records the chain uses.
const (
	linkNamePrefix    = "receipt-link-"
	catalogueSeries   = "receipt-chains"
	auditSeries       = "receipt-audit"
	incidentSeries    = "receipt-incidents"
	accountIDPrefix   = "acc_"
	accountIDLength   = len(accountIDPrefix) + 26
	accountIDAlphabet = "abcdefghijklmnopqrstuvwxyz234567"
)

// LinkName is the strict anchored record of an account's link n, from 1.
func LinkName(account string, n uint64) string {
	return linkNamePrefix + account + "-" + strconv.FormatUint(n, 10)
}

// CatalogueName is the strict anchored record of the catalogue's entry k,
// from 1.
func CatalogueName(k uint64) string { return catalogueSeries + "-" + strconv.FormatUint(k, 10) }

// AuditName is the strict anchored record of the audit's finished pass p,
// from 1.
func AuditName(p uint64) string { return auditSeries + "-" + strconv.FormatUint(p, 10) }

// IncidentName is the strict anchored record of the record of incidents'
// entry i, from 1.
func IncidentName(i uint64) string { return incidentSeries + "-" + strconv.FormatUint(i, 10) }

// ParseLinkName is LinkName's account and number; ok is false for a name
// that is not a link's.
func ParseLinkName(name string) (account string, n uint64, ok bool) {
	rest, found := strings.CutPrefix(name, linkNamePrefix)
	if !found {
		return "", 0, false
	}
	account, n, ok = splitNumber(rest)
	if !ok || account == "" {
		return "", 0, false
	}
	return account, n, true
}

// splitNumber cuts "<series>-<n>" at its last hyphen; n is from 1, in
// decimal with no sign or leading zero.
func splitNumber(name string) (series string, n uint64, ok bool) {
	i := strings.LastIndexByte(name, '-')
	if i < 0 {
		return "", 0, false
	}
	series, digits := name[:i], name[i+1:]
	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != digits {
		return "", 0, false
	}
	return series, n, true
}

// numbered is the series and number of a plain record name of the chain's:
// a link's (with its account), the catalogue's, the audit's or the record
// of incidents'.
func numbered(name string) (group, account string, n uint64, ok bool) {
	if account, n, ok := ParseLinkName(name); ok {
		return linkNamePrefix + account, account, n, true
	}
	series, n, ok := splitNumber(name)
	if !ok || (series != catalogueSeries && series != auditSeries && series != incidentSeries) {
		return "", "", 0, false
	}
	return series, "", n, true
}

// CommitGroup is where the commits of one of the chain's records are kept
// together in the anchors bucket: group is the series (an account's links,
// the catalogue, the audit's passes, the record of incidents), and member
// the number in 16 hexadecimal digits, so the members' order is the
// numbers' and one listing
// of a group from a member reads the full history of every record above it
// (ADR 0037, F1). ok is false for any other name, whose commits stay where
// each record's own are.
func CommitGroup(name string) (group, member string, ok bool) {
	group, _, n, ok := numbered(name)
	if !ok {
		return "", "", false
	}
	return group, fmt.Sprintf("%016x", n), true
}

// RecordKey is where one stored version of one of the chain's records is
// kept: name is the record's name followed by the suffix store.Anchored
// gives each version ("@<version>-<hash>"). A link is kept in its account's
// own partition under LinkKeyPrefix(n), the catalogue's entry k in
// CataloguePartition under CatalogueKeyPrefix and k, the audit's pass p in
// AuditPartition under AuditKeyPrefix and p, the record of incidents' entry
// i in IncidentPartition under IncidentKeyPrefix and i, each number in 16
// hexadecimal digits so the keys' order is the numbers'. A plain name has no key (ok
// false): these records are made with CreateNew and never stored under it,
// so nothing anyone writes there is ever read, and junk under it cannot
// stop one from being made.
func RecordKey(name string) (partition, key string, ok bool) {
	base, suffix, versioned := strings.Cut(name, "@")
	if !versioned || suffix == "" {
		return "", "", false
	}
	group, account, n, ok := numbered(base)
	switch {
	case !ok:
		return "", "", false
	case account != "":
		return Partition(account), LinkKeyPrefix(n) + "@" + suffix, true
	case group == catalogueSeries:
		return CataloguePartition, CatalogueKeyPrefix + fmt.Sprintf("%016x", n) + "@" + suffix, true
	case group == auditSeries:
		return AuditPartition, AuditKeyPrefix + fmt.Sprintf("%016x", n) + "@" + suffix, true
	default:
		return IncidentPartition, IncidentKeyPrefix + fmt.Sprintf("%016x", n) + "@" + suffix, true
	}
}

// LinkKeyPrefix starts the keys of every stored version of link n. The
// walk reads one key under it, the one the digest the link above names, or
// the commit, says; nothing else under it is ever read.
func LinkKeyPrefix(n uint64) string { return LinkPrefix + fmt.Sprintf("%016x", n) }

// The protocol a person checks offline is pkg/receiptchain's, so that
// permaily-verify links it without the service's storage; these are its
// names here.
type (
	// Digest is the SHA-256 of a document's bytes as stored, before
	// sealing: what a person recomputes from an export.
	Digest = receiptchain.Digest
	// LinkKind says what a link chains.
	LinkKind = receiptchain.LinkKind
	// BreakReason is why a link, or a catalogue's entry, could not name the
	// one before it by its digest.
	BreakReason = receiptchain.BreakReason
	// Ref is one receipt a link chains.
	Ref = receiptchain.Ref
	// Link is one link's sealed document.
	Link = receiptchain.Link
	// Position is where a receipt is on its account's chain.
	Position = receiptchain.Position
	// Head is the newest link of an account's chain, found by listing its
	// commits.
	Head = receiptchain.Head
	// LinkVerdict is what a walk found of one link.
	LinkVerdict = receiptchain.LinkVerdict
	// ReceiptVerdict is what a walk found of one receipt a link names.
	ReceiptVerdict = receiptchain.ReceiptVerdict
	// ExportPage is one page of an export.
	ExportPage = receiptchain.ExportPage
	// ExportedLink is one link of an export.
	ExportedLink = receiptchain.ExportedLink
	// ExportedReceipt is one receipt of an export.
	ExportedReceipt = receiptchain.ExportedReceipt
)

const (
	LinkReceipts  = receiptchain.LinkReceipts
	LinkAdopted   = receiptchain.LinkAdopted
	LinkRecovered = receiptchain.LinkRecovered

	BreakLost       = receiptchain.BreakLost
	BreakForked     = receiptchain.BreakForked
	BreakUnreadable = receiptchain.BreakUnreadable

	LinkOK         = receiptchain.LinkOK
	LinkLost       = receiptchain.LinkLost
	LinkForked     = receiptchain.LinkForked
	LinkUnreadable = receiptchain.LinkUnreadable
	LinkPrev       = receiptchain.LinkPrev
	LinkExpired    = receiptchain.LinkExpired

	ReceiptOK         = receiptchain.ReceiptOK
	ReceiptMissing    = receiptchain.ReceiptMissing
	ReceiptUnreadable = receiptchain.ReceiptUnreadable
	ReceiptChanged    = receiptchain.ReceiptChanged
)

// DigestOf is the digest of doc's bytes, as they are; never of a document
// marshalled again.
func DigestOf(doc []byte) Digest { return receiptchain.DigestOf(doc) }

// Genesis is what an account's link 1 names as the link before it.
func Genesis(account string) Digest { return receiptchain.Genesis(account) }

// CatalogueGenesis is what the catalogue's entry 1 names as the entry
// before it: the SHA-256 of ChainPurpose, a byte 1 and "catalogue", which
// no account's Genesis is.
func CatalogueGenesis() Digest {
	return sha256.Sum256([]byte(ChainPurpose + "\x01catalogue"))
}

// checkPrev is the rule of Prev and Break, a link's and a catalogue entry's
// alike.
func checkPrev(n uint64, prev *Digest, br BreakReason) error {
	return receiptchain.CheckPrev(n, prev, br)
}

// Catalogue is one entry of the catalogue of chains: the accounts whose
// chains it names, entered before any of their link 1 was made, so a chain
// is never begun that the catalogue does not name (ADR 0037, F2). An
// account may be named in more than one entry; the audit reads it once.
type Catalogue struct {
	V  int       `json:"v"`
	K  uint64    `json:"k"`
	At time.Time `json:"at"`
	// Prev is the digest of entry K-1's document, CatalogueGenesis for
	// entry 1; Break as a link's.
	Prev     *Digest     `json:"prev,omitempty"`
	Break    BreakReason `json:"break,omitempty"`
	Accounts []string    `json:"accounts"`
}

// Check says whether c is a catalogue entry's document as one is written:
// its version, number and time, the entry before named as a link names the
// link before, and one to CatalogueAccountsMax accounts of an account
// identifier's shape, none named twice.
func (c *Catalogue) Check() error {
	switch {
	case c.V != CatalogueVersion:
		return fmt.Errorf("receipts: a catalogue entry of version %d", c.V)
	case c.K == 0:
		return errors.New("receipts: the catalogue's entries are numbered from 1")
	case c.At.IsZero():
		return errors.New("receipts: a catalogue entry says when it was written")
	}
	if err := checkPrev(c.K, c.Prev, c.Break); err != nil {
		return err
	}
	if len(c.Accounts) == 0 || len(c.Accounts) > CatalogueAccountsMax {
		return fmt.Errorf("receipts: a catalogue entry names 1 to %d accounts, not %d", CatalogueAccountsMax, len(c.Accounts))
	}
	seen := make(map[string]bool, len(c.Accounts))
	for _, a := range c.Accounts {
		if !validAccount(a) {
			return errors.New("receipts: a catalogue entry names something that is not an account")
		}
		if seen[a] {
			return errors.New("receipts: a catalogue entry names an account twice")
		}
		seen[a] = true
	}
	return nil
}

// validAccount says whether id has an account identifier's shape: acc_ and
// 26 characters of lowercase base32 (internal/services/accounts).
func validAccount(id string) bool {
	rest, ok := strings.CutPrefix(id, accountIDPrefix)
	if !ok || len(id) != accountIDLength {
		return false
	}
	for _, c := range rest {
		if !strings.ContainsRune(accountIDAlphabet, c) {
			return false
		}
	}
	return true
}

// AuditPass is the document of the audit's finished pass P, a strict record
// made once (AuditName): what every enclave reports on /status as the
// deployment's audit, whichever enclave ran it (ADR 0037, "The audit").
type AuditPass struct {
	V     int                       `json:"v"`
	P     uint64                    `json:"p"`
	Audit attestation.ReceiptsAudit `json:"audit"`
}

// Check says whether a is a pass as one is written: its version and number,
// when it began and finished, in that order, and its counts, none
// negative, with every kind of attestation.ReceiptsBrokenKinds and no
// other.
func (a *AuditPass) Check() error {
	switch {
	case a.V != AuditVersion:
		return fmt.Errorf("receipts: an audit pass of version %d", a.V)
	case a.P == 0:
		return errors.New("receipts: the audit's passes are numbered from 1")
	case a.Audit.Began.IsZero() || a.Audit.Finished.Before(a.Audit.Began):
		return errors.New("receipts: an audit pass says when it began and finished, in that order")
	case a.Audit.Chains < 0 || a.Audit.Removed < 0 || a.Audit.Recovered < 0 || a.Audit.Unlinked < 0 || a.Audit.Expired < 0:
		return errors.New("receipts: an audit pass counts nothing below zero")
	case len(a.Audit.Broken) != len(attestation.ReceiptsBrokenKinds):
		return errors.New("receipts: an audit pass counts every kind of break, and no other")
	}
	for _, k := range attestation.ReceiptsBrokenKinds {
		if n, ok := a.Audit.Broken[k]; !ok || n < 0 {
			return fmt.Errorf("receipts: an audit pass counts %q, not below zero", k)
		}
	}
	return nil
}

// Incident is one entry of the record of incidents, a strict record made
// once (IncidentName), numbered from 1 without gap and naming the entry
// before by digest as a catalogue's entry does: what an enclave found
// broken, recorded before it reported it, so a finding outlives the pass
// that found it, a clean pass after it, a restart, the other enclave and
// the account (ADR 0037, "The record of incidents"; R1). Its head is found
// by listing its commits, as a chain's: how many entries it has is what
// /status reports, and an entry whose value is lost, forked or unreadable
// still counts, since its commit is there.
type Incident struct {
	V  int       `json:"v"`
	I  uint64    `json:"i"`
	At time.Time `json:"at"`
	// Prev is the digest of entry I-1's document, IncidentGenesis for entry
	// 1; Break as a link's.
	Prev     *Digest     `json:"prev,omitempty"`
	Break    BreakReason `json:"break,omitempty"`
	Findings []Finding   `json:"findings"`
}

// Finding is one thing an incident records: a kind of
// attestation.ReceiptsBrokenKinds and where it was found. A receipt's kind
// names its account, its link's number and its identifier; a link's its
// account and number; the catalogue's the entry's number and no account.
type Finding struct {
	Kind    string `json:"kind"`
	Account string `json:"account,omitempty"`
	N       uint64 `json:"n"`
	Receipt string `json:"receipt,omitempty"`
}

// IncidentGenesis is what the record of incidents' entry 1 names as the
// entry before it: the SHA-256 of ChainPurpose, a byte 1 and "incidents",
// which neither an account's Genesis nor CatalogueGenesis is.
func IncidentGenesis() Digest {
	return sha256.Sum256([]byte(ChainPurpose + "\x01incidents"))
}

// Check says whether i is an incident entry's document as one is written:
// its version, number and time, the entry before named as a link names the
// link before, and one to IncidentFindingsMax findings, each of a known
// kind with where it was found in that kind's shape, none named twice.
func (i *Incident) Check() error {
	switch {
	case i.V != IncidentVersion:
		return fmt.Errorf("receipts: an incident entry of version %d", i.V)
	case i.I == 0:
		return errors.New("receipts: the record of incidents' entries are numbered from 1")
	case i.At.IsZero():
		return errors.New("receipts: an incident entry says when it was written")
	}
	if err := checkPrev(i.I, i.Prev, i.Break); err != nil {
		return err
	}
	if len(i.Findings) == 0 || len(i.Findings) > IncidentFindingsMax {
		return fmt.Errorf("receipts: an incident entry names 1 to %d findings, not %d", IncidentFindingsMax, len(i.Findings))
	}
	seen := make(map[Finding]bool, len(i.Findings))
	for _, f := range i.Findings {
		if err := f.check(); err != nil {
			return err
		}
		if seen[f] {
			return errors.New("receipts: an incident entry names a finding twice")
		}
		seen[f] = true
	}
	return nil
}

func (f Finding) check() error {
	kind, _, _ := strings.Cut(f.Kind, "_")
	switch {
	case !slices.Contains(attestation.ReceiptsBrokenKinds, f.Kind):
		return fmt.Errorf("receipts: a finding of kind %q", f.Kind)
	case f.N == 0:
		return errors.New("receipts: a finding says the number it was found at")
	case kind == "catalogue" && (f.Account != "" || f.Receipt != ""):
		return errors.New("receipts: a finding of the catalogue names no account")
	case kind != "catalogue" && !validAccount(f.Account):
		return errors.New("receipts: a finding of a chain names its account")
	case (kind == "receipt") != ValidID(f.Receipt):
		return errors.New("receipts: a finding names a receipt only when it is a receipt's")
	}
	return nil
}

// WalkedLink is one link a walk read, with what it found of it and of the
// receipts of it the page holds; Link is nil unless Verdict is LinkOK,
// LinkPrev or LinkExpired (Readable), and Receipts is then empty: a link
// that cannot be read names nothing anyone can check.
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
	// Next is where the next page begins (before it); Done says the page
	// reached link 1's first receipt and nothing is left. A page ended by a
	// bound is never Done.
	Next Position `json:"next"`
	Done bool     `json:"done,omitempty"`
}

// Chain is an account's chain of receipts (ADR 0037). The receipts service
// is its one implementation, over the entries of Partition(account) and a
// strict store.Anchored that reads and writes the chain's names and
// nothing else; receiptstest.Fake stands in for it.
type Chain interface {
	// Link returns once a committed link of account names ref, a receipt
	// already stored, at the position it returns; an error says that
	// nothing is known to name it, and its request gives nothing. Refs of
	// one account given at once are grouped into one link, up to
	// LinkReceiptsMax; ctx bounds the wait. A link whose commit's outcome
	// is unknown may name ref and still return an error: a receipt may
	// record more than left, never less.
	Link(ctx context.Context, account string, ref Ref) (Position, error)
	// Head is account's head, found by listing its links' commits now,
	// with no bound on the objects listed but ctx's deadline. A head that
	// is not LinkOK is returned only once it is recorded as an incident.
	Head(ctx context.Context, account string) (Head, error)
	// Walk reads account's receipts before the position before (the zero
	// Position for from the head), the newest first, up to limit receipts
	// and the bounds of a page, and says what it found of each link and
	// receipt. Every link it reports is read strictly (its full history of
	// commits, a fork included), the page's top one against its commit and
	// every other against the next one's Prev; every receipt against its
	// Ref. A link or a receipt read and found wanting is a verdict, never
	// left out; a storage that cannot be read now is an error, and so is a
	// link whose history alone passes WalkListedMax (ErrHistoryTooLarge). A
	// page holding a verdict of a kind of attestation.ReceiptsBrokenKinds is
	// returned only once an incident recording it is committed; a failure
	// to record fails the page. A page ends at a bound, inside a link or at
	// one, and the next begins from Next: nothing is skipped. What it says
	// of a link is as of its history's listing: a commit that lands after
	// it is seen by the next page or pass that lists it.
	Walk(ctx context.Context, account string, before Position, limit int) (*Walked, error)
	// Export is Walk's page as exact bytes, within the same bounds; every
	// page it returns passes ExportPage.Check.
	Export(ctx context.Context, account string, before Position) (*ExportPage, error)
}

// Auditor is the audit of every chain the catalogue names (ADR 0037, "The
// audit"), and the record of incidents, which /status reports.
type Auditor interface {
	// Audit runs the audit for at most AuditRunMax: it begins a pass when
	// the newest finished one began AuditEvery ago or more, and returns the
	// pass it finished, nil when none did. A pass that does not finish in a
	// run begins again in the next; no pass is finished that did not read
	// every entry of the catalogue up to the head it began with and every
	// chain they name, nor before every finding it counts as broken is
	// recorded as an incident.
	Audit(ctx context.Context) (*AuditPass, error)
	// Audited is the deployment's newest finished pass, read strictly; nil
	// before the first.
	Audited(ctx context.Context) (*AuditPass, error)
	// Incidents is the record of incidents' head, found by listing its
	// commits now: how many entries any enclave of the deployment ever
	// recorded, whatever became of their values, and when it was read.
	Incidents(ctx context.Context) (attestation.ReceiptsIncidents, error)
	// Broken is what this enclave found by its walks, exports, appends and
	// audits since it started and has not recorded as an incident yet (its
	// record pending or failed), by every kind of
	// attestation.ReceiptsBrokenKinds: what a restart would forget, so it
	// is shown until it is recorded.
	Broken() map[string]int64
}
