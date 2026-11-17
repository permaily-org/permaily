package receipts

import (
	"context"
	"errors"
	"fmt"

	"github.com/permaily-org/permaily/internal/store"
)

// Reading and checking an account's chain (ADR 0037, "Reading and
// checking"): the head found by listing, and pages of links and receipts,
// the newest first, every link read strictly from a listing of its full
// history and checked against the link above, every receipt against its
// ref, every broken verdict recorded as an incident before the page is
// returned.

// ErrBefore is a page asked from a position the chain does not have.
var ErrBefore = errors.New("receipts: no such position on the chain")

// headOf is the Head a judged head is: its digest when its document was
// read, and when it was written when it is LinkOK.
func headOf(j judged) Head {
	h := Head{N: j.n}
	if j.n == 0 {
		return h
	}
	h.Verdict = j.verdict
	if j.verdict == LinkOK {
		h.Digest, h.At = j.digest, j.p.link.At
	}
	return h
}

// Head implements Chain.
func (s *Service) Head(ctx context.Context, account string) (Head, error) {
	if s.chain == nil {
		return Head{}, errNotChained
	}
	if !validAccount(account) {
		return Head{}, errNoAccount
	}
	key, err := s.chainKey(ctx)
	if err != nil {
		return Head{}, err
	}
	sr := links(account)
	j, err := s.findHead(ctx, key, sr)
	if err != nil {
		return Head{}, err
	}
	if f, ok := sr.finding(j); ok {
		if err := s.record(ctx, []Finding{f}); err != nil {
			return Head{}, err
		}
	}
	return headOf(j), nil
}

var errNoAccount = errors.New("receipts: a chain is an account's")

type pageLink struct {
	j        judged
	receipts []pageReceipt
}

type pageReceipt struct {
	index   int
	ref     Ref
	verdict ReceiptVerdict
	doc     []byte
	receipt *Receipt
}

type page struct {
	head  Head
	links []pageLink
	next  Position
	done  bool
}

// page reads the page of account's chain before before, at most limit
// receipts and the bounds of a page, and records what it found broken.
func (s *Service) page(ctx context.Context, account string, before Position, limit int) (*page, error) {
	if s.chain == nil {
		return nil, errNotChained
	}
	if !validAccount(account) || limit < 1 {
		return nil, errNoAccount
	}
	key, err := s.chainKey(ctx)
	if err != nil {
		return nil, err
	}
	rkey, err := s.keys.Derive(ctx, Purpose)
	if err != nil {
		return nil, err
	}
	sr := links(account)
	head, err := s.findHead(ctx, key, sr)
	if err != nil {
		return nil, err
	}
	out := &page{head: headOf(head)}
	var findings []Finding
	if f, ok := sr.finding(head); ok {
		findings = append(findings, f)
	}
	hi, from := head.n, -1
	switch {
	case before.N > head.n || before.Index < 0 || (before.N == 0 && before.Index != 0):
		return nil, ErrBefore
	case before.N != 0:
		hi, from = before.N, before.Index
		if from == 0 {
			hi, from = hi-1, -1
		}
	}
	if hi == 0 {
		out.done = true
		return out, s.record(ctx, findings)
	}
	members, lo, err := s.pageRange(ctx, sr, hi)
	if err != nil {
		return nil, err
	}
	partition := Partition(account)
	var expected *Digest
	count, size, ended := 0, 0, false
	if m := members[hi]; hi < head.n && (m == nil || m.History.Head == nil) {
		// A continuation whose top link has no commit left: what
		// authenticates it is the link above, read again.
		var climbed uint64
		if expected, climbed, size, err = s.continuation(ctx, key, sr, hi, head.n); err != nil {
			return nil, err
		}
		lo = max(lo, hi-min(hi, WalkLinksMax-climbed)+1)
	}
	// next is where the page ends, and nextExpected what authenticates
	// the link it begins the next page with.
	var nextExpected *Digest
	n := hi
	for ; n >= lo && !ended; n-- {
		if count == limit {
			out.next, nextExpected, ended = Position{N: n + 1}, expected, true
			break
		}
		judgedWith := expected
		j, err := s.judge(ctx, key, sr, n, members[n], expected)
		if err != nil {
			return nil, err
		}
		if len(out.links) > 0 && size+j.bytes > s.walkBytes {
			out.next, nextExpected, ended = Position{N: n + 1}, expected, true
			break
		}
		size += j.bytes
		if f, ok := sr.finding(j); ok {
			findings = append(findings, f)
		}
		pl := pageLink{j: j}
		expected = nil
		if !j.verdict.Readable() {
			if from > 0 {
				return nil, ErrBefore
			}
			out.links = append(out.links, pl)
			continue
		}
		l := j.p.link
		expected = l.Prev
		i := len(l.Receipts) - 1
		if from >= 0 {
			if from > len(l.Receipts) {
				return nil, ErrBefore
			}
			i = from - 1
		}
		from = -1
		for ; i >= 0; i-- {
			if count == limit {
				out.next, nextExpected, ended = Position{N: n, Index: i + 1}, judgedWith, true
				break
			}
			pr, cost, err := s.check(ctx, rkey, partition, i, l.Receipts[i])
			if err != nil {
				return nil, err
			}
			if size+cost > s.walkBytes {
				out.next, nextExpected, ended = Position{N: n, Index: i + 1}, judgedWith, true
				if len(pl.receipts) == 0 {
					out.next = Position{N: n + 1}
				}
				break
			}
			size += cost
			count++
			if pr.verdict != ReceiptOK {
				findings = append(findings, Finding{Kind: "receipt_" + string(pr.verdict), Account: account, N: n, Receipt: pr.ref.ID})
			}
			pl.receipts = append(pl.receipts, pr)
		}
		if len(pl.receipts) > 0 || !ended {
			out.links = append(out.links, pl)
		}
	}
	switch {
	case ended:
	case lo == 1:
		out.done = true
	default:
		out.next, nextExpected = Position{N: lo}, expected
	}
	if err := s.record(ctx, findings); err != nil {
		return nil, err
	}
	if top := out.next.N; top != 0 && nextExpected != nil {
		if out.next.Index == 0 {
			top--
		}
		s.chain.authenticated(account, top, *nextExpected)
	}
	return out, nil
}

// ErrContinuation is a page that begins at a link whose commit expired,
// and whose authentication by the links above it (the nearest with a
// commit, read down to it) passes the bounds of a page: it is asked again,
// and goes on from what was read (ADR 0037, "Changed while building pull
// request 1"). Neither a verdict nor anything recorded.
var ErrContinuation = errors.New("receipts: the page's first link is authenticated by more links above it than a page reads; ask again")

// authenticated is a link and the digest the link above it names.
type authenticated struct {
	n uint64
	d Digest
}

// continuedMax bounds the accounts whose continuation an enclave keeps.
const continuedMax = 4096

// authenticated keeps that link n of account's chain is authenticated as
// d, one link an account.
func (c *chainState) authenticated(account string, n uint64, d Digest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.continued[account]; !ok && len(c.continued) >= continuedMax {
		clear(c.continued)
	}
	c.continued[account] = authenticated{n: n, d: d}
}

// continuation is what authenticates link hi of sr, a page's first, whose
// commit expired (ADR 0037, "Reading and checking"; the review of #170,
// finding 3): the digest the link above it names, read as a page would
// have read it had it begun higher. That is what this enclave kept when a
// page ended above hi or a continuation read down to it, or what the
// links from the nearest above hi with a commit (or above hi that this
// enclave kept) read down to hi+1 say, every one read strictly, as a page
// reads them. The links read and their bytes are charged to the page, at
// most WalkLinksMax-1 links and half its bytes, so the page still shows a
// link and its receipts; past them, it keeps where it got to and fails
// with ErrContinuation. Nil is nothing above authenticating it: a break,
// or no commit above.
func (s *Service) continuation(ctx context.Context, key []byte, sr *series, hi, head uint64) (*Digest, uint64, int, error) {
	s.chain.mu.Lock()
	kept, ok := s.chain.continued[sr.account]
	s.chain.mu.Unlock()
	if ok && kept.n == hi {
		d := kept.d
		return &d, 0, 0, nil
	}
	members := map[uint64]*store.Member{}
	from := uint64(0)
	err := s.readRange(ctx, sr, hi+1, head, WalkListedMax, func(n uint64, m *store.Member) error {
		members[n] = m
		if m.History.Head != nil {
			from = n
			return errStopped
		}
		return nil
	})
	switch {
	case errors.Is(err, store.ErrListedMax):
		return nil, 0, 0, ErrHistoryTooLarge
	case err != nil && !errors.Is(err, errStopped):
		return nil, 0, 0, fmt.Errorf("receipts: read the chain: %w", err)
	}
	var expected *Digest
	if ok && kept.n > hi && (from == 0 || kept.n <= from) {
		d := kept.d
		from, expected = kept.n, &d
	}
	var visited uint64
	size := 0
	for n := from; n > hi; n-- {
		j, err := s.judge(ctx, key, sr, n, members[n], expected)
		if err != nil {
			return nil, 0, 0, err
		}
		visited++
		size += j.bytes
		expected = nil
		if j.verdict.Readable() {
			expected = j.p.link.Prev
		}
		if n-1 > hi && (visited == WalkLinksMax-1 || size > s.walkBytes/2) {
			// Every try reads at least one link further down.
			if expected != nil {
				s.chain.authenticated(sr.account, n-1, *expected)
			}
			return nil, 0, 0, ErrContinuation
		}
	}
	return expected, visited, size, nil
}

// pageRange lists the histories of the links a page may visit, from hi
// down: WalkLinksMax of them, or half as many again, from hi, while their
// listing passes WalkListedMax, down to one; a link whose history alone
// passes it is ErrHistoryTooLarge.
func (s *Service) pageRange(ctx context.Context, sr *series, hi uint64) (map[uint64]*store.Member, uint64, error) {
	k := min(hi, WalkLinksMax)
	for {
		lo := hi - k + 1
		members := map[uint64]*store.Member{}
		err := s.readRange(ctx, sr, lo, hi, WalkListedMax, func(n uint64, m *store.Member) error {
			members[n] = m
			return nil
		})
		switch {
		case errors.Is(err, store.ErrListedMax) && k == 1:
			return nil, 0, ErrHistoryTooLarge
		case errors.Is(err, store.ErrListedMax):
			k /= 2
			continue
		case err != nil:
			return nil, 0, fmt.Errorf("receipts: read the chain: %w", err)
		}
		return members, lo, nil
	}
}

// check reads the receipt ref names, at index i of its link, and says what
// it found and what reading it cost.
func (s *Service) check(ctx context.Context, rkey []byte, partition string, i int, ref Ref) (pageReceipt, int, error) {
	pr := pageReceipt{index: i, ref: ref, verdict: ReceiptMissing}
	e, err := s.entries.GetEntry(ctx, partition, ref.ID)
	if errors.Is(err, store.ErrNotFound) {
		return pr, 0, nil
	}
	if err != nil {
		return pr, 0, fmt.Errorf("receipts: read a receipt: %w", err)
	}
	pr, cost := checkEntry(rkey, e, i, ref)
	return pr, cost, nil
}

// checkEntry checks stored receipt e, at index i of its link, against ref,
// and says what reading it cost.
func checkEntry(rkey []byte, e *store.Entry, i int, ref Ref) (pageReceipt, int) {
	pr := pageReceipt{index: i, ref: ref, verdict: ReceiptUnreadable}
	raw, r, err := openRaw(rkey, e)
	if err != nil {
		return pr, len(e.Value)
	}
	pr.doc, pr.verdict = raw, ReceiptChanged
	if DigestOf(raw) == ref.Digest {
		pr.verdict, pr.receipt = ReceiptOK, r
	}
	return pr, max(len(e.Value), SealedBytes(len(raw)))
}

// Walk implements Chain.
func (s *Service) Walk(ctx context.Context, account string, before Position, limit int) (*Walked, error) {
	p, err := s.page(ctx, account, before, limit)
	if err != nil {
		return nil, err
	}
	w := &Walked{Head: p.head, Links: []WalkedLink{}, Next: p.next, Done: p.done}
	for _, pl := range p.links {
		wl := WalkedLink{N: pl.j.n, Verdict: pl.j.verdict, Link: pl.j.p.link}
		for _, pr := range pl.receipts {
			wl.Receipts = append(wl.Receipts, WalkedReceipt{Index: pr.index, Ref: pr.ref, Verdict: pr.verdict, Receipt: pr.receipt})
		}
		w.Links = append(w.Links, wl)
	}
	return w, nil
}

// Export implements Chain.
func (s *Service) Export(ctx context.Context, account string, before Position) (*ExportPage, error) {
	p, err := s.page(ctx, account, before, WalkLinksMax*LinkReceiptsMax)
	if err != nil {
		return nil, err
	}
	out := &ExportPage{V: ExportVersion, Account: account, Head: p.head, Links: []ExportedLink{}, Next: p.next, Done: p.done}
	for _, pl := range p.links {
		el := ExportedLink{N: pl.j.n, Verdict: pl.j.verdict, Doc: pl.j.doc}
		for _, pr := range pl.receipts {
			el.Receipts = append(el.Receipts, ExportedReceipt{Index: pr.index, ID: pr.ref.ID, Verdict: pr.verdict, Doc: pr.doc})
		}
		out.Links = append(out.Links, el)
	}
	return out, nil
}
