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
	n := hi
	for ; n >= lo && !ended; n-- {
		if count == limit {
			out.next, ended = Position{N: n + 1}, true
			break
		}
		j, err := s.judge(ctx, key, sr, n, members[n], expected)
		if err != nil {
			return nil, err
		}
		if len(out.links) > 0 && size+j.bytes > WalkBytesMax {
			out.next, ended = Position{N: n + 1}, true
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
				out.next, ended = Position{N: n, Index: i + 1}, true
				break
			}
			pr, cost, err := s.check(ctx, rkey, partition, i, l.Receipts[i])
			if err != nil {
				return nil, err
			}
			if size+cost > WalkBytesMax {
				out.next, ended = Position{N: n, Index: i + 1}, true
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
		out.next = Position{N: lo}
	}
	if err := s.record(ctx, findings); err != nil {
		return nil, err
	}
	return out, nil
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
