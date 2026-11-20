package receipts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

// The chain's four series of numbered strict records (an account's links,
// the catalogue, the record of incidents, the audit's passes) are read and
// written the same way: each record made once with CreateNew at the number
// after the head, the head found by listing the series' commits, every
// record read strictly from a listing of its full history.

// series is one of them.
type series struct {
	name func(n uint64) string
	// genesis is what record 1 names as the one before it.
	genesis Digest
	// parse checks a document read at n and says what it names before it.
	parse func(doc []byte, n uint64) (parsed, error)
	// kind starts the kinds of attestation.ReceiptsBrokenKinds a record of
	// it found broken is ("link", "catalogue"); "" for a series whose breaks
	// are counted by its head alone (the record of incidents, the passes).
	kind string
	// account is the account of a chain of links.
	account string
}

// parsed is what a series' document says: the record before it, and the
// document itself.
type parsed struct {
	prev *Digest
	br   BreakReason
	link *Link
	cat  *Catalogue
	pass *AuditPass
}

func links(account string) *series {
	return &series{
		name:    func(n uint64) string { return LinkName(account, n) },
		genesis: Genesis(account),
		kind:    "link",
		account: account,
		parse: func(doc []byte, n uint64) (parsed, error) {
			var l Link
			if err := json.Unmarshal(doc, &l); err != nil || l.Check() != nil || l.N != n {
				return parsed{}, errNotOne
			}
			return parsed{prev: l.Prev, br: l.Break, link: &l}, nil
		},
	}
}

var catalogueSeriesOf = &series{
	name:    CatalogueName,
	genesis: CatalogueGenesis(),
	kind:    "catalogue",
	parse: func(doc []byte, n uint64) (parsed, error) {
		var c Catalogue
		if err := json.Unmarshal(doc, &c); err != nil || c.Check() != nil || c.K != n {
			return parsed{}, errNotOne
		}
		return parsed{prev: c.Prev, br: c.Break, cat: &c}, nil
	},
}

var incidentSeriesOf = &series{
	name:    IncidentName,
	genesis: IncidentGenesis(),
	parse: func(doc []byte, n uint64) (parsed, error) {
		var i Incident
		if err := json.Unmarshal(doc, &i); err != nil || i.Check() != nil || i.I != n {
			return parsed{}, errNotOne
		}
		return parsed{prev: i.Prev, br: i.Break}, nil
	},
}

var passSeriesOf = &series{
	name: AuditName,
	parse: func(doc []byte, n uint64) (parsed, error) {
		var p AuditPass
		if err := json.Unmarshal(doc, &p); err != nil || p.Check() != nil || p.P != n {
			return parsed{}, errNotOne
		}
		return parsed{pass: &p}, nil
	},
}

// errNotOne is a document that is not the series' record at its number.
var errNotOne = errors.New("receipts: not a record of the chain at its number")

// keyPrefix is the partition and the key prefix of every stored version of
// record n of sr.
func (sr *series) keyPrefix(n uint64) (partition, prefix string) {
	partition, key, _ := RecordKey(sr.name(n) + "@-")
	return partition, strings.TrimSuffix(key, "-")
}

// judged is what a strict read found of one record: its verdict, and its
// document when Readable. bytes is what was read of the table for it.
type judged struct {
	n       uint64
	verdict LinkVerdict
	doc     []byte
	digest  Digest
	p       parsed
	bytes   int
}

// expiredCandidates bounds the values read under the key of a record whose
// commits expired: whoever writes the table can add more, and the record is
// then lost, as it is when they delete its value.
const expiredCandidates = 8

// judge says what record n of sr is, from m, what a listing of its history
// found (nil when nothing was listed), and expected, the digest the record
// above names for it (nil when nothing above authenticates it): ok, prev
// (another digest than expected), lost, forked, unreadable, or expired (no
// commit left, and a value stored whose digest is expected). A storage
// that cannot be read is an error, never a verdict.
func (s *Service) judge(ctx context.Context, key []byte, sr *series, n uint64, m *store.Member, expected *Digest) (judged, error) {
	j := judged{n: n, verdict: LinkLost}
	if m == nil || m.History.Head == nil {
		if expected == nil {
			return j, nil
		}
		partition, prefix := sr.keyPrefix(n)
		es, err := s.entries.ListEntries(ctx, partition, prefix, "", expiredCandidates)
		if err != nil {
			return j, fmt.Errorf("receipts: read the chain: %w", err)
		}
		for _, e := range es {
			j.bytes += len(e.Value)
			doc, err := openDoc(key, sr.name(n), e.Value)
			p, perr := sr.parse(doc, n)
			if err == nil && perr == nil && DigestOf(doc) == *expected {
				j.verdict, j.doc, j.digest, j.p = LinkExpired, doc, *expected, p
				return j, nil
			}
		}
		return j, nil
	}
	// Made once: one version holds a commit of this deployment's. It need
	// not be version 1: an earlier deployment's commits, or anything else
	// that is not one of this deployment's, may hold the versions below.
	if h := m.History; len(h.Forks) > 0 || h.Versions != 1 {
		j.verdict = LinkForked
		return j, nil
	}
	rec, err := s.chain.anchored.ReadListed(ctx, *m)
	if errors.Is(err, store.ErrLost) {
		return j, nil
	}
	if err != nil {
		return j, fmt.Errorf("receipts: read the chain: %w", err)
	}
	j.bytes = len(rec.Value)
	doc, err := openDoc(key, sr.name(n), rec.Value)
	if err != nil {
		j.verdict = LinkUnreadable
		return j, nil
	}
	p, err := sr.parse(doc, n)
	if err != nil {
		j.verdict = LinkUnreadable
		return j, nil
	}
	j.verdict, j.doc, j.digest, j.p = LinkOK, doc, DigestOf(doc), p
	if expected != nil && j.digest != *expected {
		j.verdict = LinkPrev
	}
	return j, nil
}

// openDoc opens a sealed document of the chain's record name.
func openDoc(key []byte, name string, value []byte) ([]byte, error) {
	return envelope.OpenText(key, value, envelope.AAD(ChainPurpose, name))
}

// numberOf is the number of a member of a series' group, ok false for what
// is not one.
func numberOf(member string) (uint64, bool) {
	n, err := strconv.ParseUint(member, 16, 64)
	return n, err == nil && n > 0 && len(member) == 16
}

// readRange lists records lo to hi of sr (hi 0 for to the end) strictly
// from one listing, with at most maxListed objects (0 for no bound), and
// calls each with every number that has anything listed.
func (s *Service) readRange(ctx context.Context, sr *series, lo, hi uint64, maxListed int, each func(n uint64, m *store.Member) error) error {
	to := ""
	if hi != 0 {
		to = sr.name(hi)
	}
	return s.chain.anchored.ReadRange(ctx, sr.name(lo), to, maxListed, func(m store.Member) error {
		n, ok := numberOf(m.Member)
		if !ok {
			return nil
		}
		return each(n, &m)
	})
}

// readOne reads record n of sr strictly, authenticated by its commit.
func (s *Service) readOne(ctx context.Context, key []byte, sr *series, n uint64) (judged, error) {
	var found *store.Member
	if err := s.readRange(ctx, sr, n, n, 0, func(_ uint64, m *store.Member) error {
		found = m
		return nil
	}); err != nil {
		return judged{}, fmt.Errorf("receipts: read the chain: %w", err)
	}
	return s.judge(ctx, key, sr, n, found, nil)
}

// findHead finds the head of sr: the highest number with a commit of this
// deployment's, by listing the series' commits from a hint (the greatest
// key stored of it) to the end, and from 1 when nothing at or above the
// hint has one. Its verdict is the record's, authenticated by its commit;
// number 0 for a series not begun.
func (s *Service) findHead(ctx context.Context, key []byte, sr *series) (judged, error) {
	from := s.hint(ctx, sr)
	for {
		var (
			best  store.Member
			bestN uint64
		)
		err := s.readRange(ctx, sr, from, 0, 0, func(n uint64, m *store.Member) error {
			if m.History.Head != nil {
				best, bestN = *m, n
			}
			return nil
		})
		if err != nil {
			return judged{}, fmt.Errorf("receipts: find the head: %w", err)
		}
		if bestN != 0 {
			return s.judge(ctx, key, sr, bestN, &best, nil)
		}
		if from == 1 {
			return judged{}, nil
		}
		from = 1
	}
}

// hint is the number of the greatest key stored of sr, 1 when there is
// none or it is not one: a hint only, never trusted.
func (s *Service) hint(ctx context.Context, sr *series) uint64 {
	partition, prefix := sr.keyPrefix(1)
	prefix = strings.TrimSuffix(prefix, fmt.Sprintf("%016x@", 1))
	scan, err := s.chain.scanner.ScanEntriesDown(ctx, partition, prefix, "", 1)
	if err != nil || len(scan.Entries) == 0 {
		return 1
	}
	digits, _, _ := strings.Cut(strings.TrimPrefix(scan.Entries[0].Key, prefix), "@")
	if n, ok := numberOf(digits); ok {
		return n
	}
	return 1
}

// create makes record n of sr with doc, once: own when this call's commit
// made it (also when a retry is refused over this very document), else the
// record another enclave made there, read strictly.
func (s *Service) create(ctx context.Context, key []byte, sr *series, n uint64, doc []byte) (j judged, own bool, err error) {
	// The audit's fence (ADR 0037, R3): its own writes for an account begin
	// within AuditWriteAfterCheck of Removing's answer. The deadline goes
	// into the store, which stages no value past it however long the write
	// took to begin (store.WriteBy), and the audit asks again.
	by, fenced := ctx.Value(writeByKey{}).(time.Time)
	if fenced && !s.now().Before(by) {
		return judged{}, false, errFence
	}
	p, err := sr.parse(doc, n)
	if err != nil {
		return judged{}, false, fmt.Errorf("receipts: a record of the chain that is not one: %w", err)
	}
	name := sr.name(n)
	sealed, err := envelope.SealText(key, doc, envelope.AAD(ChainPurpose, name))
	if err != nil {
		return judged{}, false, fmt.Errorf("receipts: seal: %w", err)
	}
	wctx := store.WriteWithin(ctx, ReceiptWriteMax)
	if fenced {
		wctx = store.WriteBy(wctx, by)
	}
	err = s.chain.anchored.CreateNew(wctx, &store.Record{Name: name, Value: sealed})
	s.chain.anchored.Forget(name)
	if fenced && err != nil && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil && !s.now().Before(by) {
		return judged{}, false, errFence
	}
	mine := judged{n: n, verdict: LinkOK, doc: doc, digest: DigestOf(doc), p: p, bytes: len(sealed)}
	switch {
	case err == nil:
		return mine, true, nil
	case !errors.Is(err, store.ErrExists) && !errors.Is(err, store.ErrForked):
		return judged{}, false, fmt.Errorf("receipts: write the chain: %w", err)
	}
	j, err = s.readOne(ctx, key, sr, n)
	if err != nil {
		return judged{}, false, err
	}
	if j.verdict == LinkOK && bytes.Equal(j.doc, doc) {
		return mine, true, nil
	}
	return j, false, nil
}

// prevOf is what the record after head names: the head's digest, the
// genesis for the first, or the break a head that cannot be read is.
func prevOf(sr *series, head judged) (*Digest, BreakReason) {
	switch {
	case head.n == 0:
		g := sr.genesis
		return &g, ""
	case head.verdict == LinkOK:
		d := head.digest
		return &d, ""
	}
	return nil, BreakReason(head.verdict)
}

// finding is what record j of sr found broken is, ok false when it is not
// broken or its series counts no finding.
func (sr *series) finding(j judged) (Finding, bool) {
	switch {
	case sr.kind == "" || j.n == 0 || j.verdict == LinkOK || j.verdict == LinkExpired:
		return Finding{}, false
	case sr.kind == "catalogue":
		return Finding{Kind: "catalogue_" + string(j.verdict), N: j.n}, true
	}
	return Finding{Kind: "link_" + string(j.verdict), Account: sr.account, N: j.n}, true
}
