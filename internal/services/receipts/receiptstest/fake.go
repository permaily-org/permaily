// Package receiptstest has a fake of the chain of receipts and of its audit
// (ADR 0037), which the composition, the person's routes and their tests
// are built against while the receipts service builds the real ones: its
// pages follow the contract's (newest first, Next, Done, a link that cannot
// be read holding no receipt), and its verdicts are what a test sets.
package receiptstest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// ErrBefore is the answer to a page asked from a position the chain does
// not have.
var ErrBefore = errors.New("receiptstest: no such position")

// Fake is a receipts.Chain and a receipts.Auditor in memory. The zero value
// is usable. Every call answers Err when it is set.
type Fake struct {
	// Now is the time links are written at; a fixed time when nil.
	Now func() time.Time
	Err error
	// Pass is what Audit and Audited answer; Found what Broken counts,
	// besides zero for every kind.
	Pass  *receipts.AuditPass
	Found map[string]int64

	mu     sync.Mutex
	chains map[string][]*link
	ids    uint64
}

type link struct {
	verdict  receipts.LinkVerdict
	doc      []byte
	refs     []receipts.Ref
	docs     [][]byte
	verdicts []receipts.ReceiptVerdict
}

var (
	_ receipts.Chain   = (*Fake)(nil)
	_ receipts.Auditor = (*Fake)(nil)
)

func (f *Fake) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
}

// Append writes one link of account naming a receipt for each document, as
// stored, and returns the position of the first.
func (f *Fake) Append(account string, docs ...[]byte) receipts.Position {
	f.mu.Lock()
	defer f.mu.Unlock()
	refs := make([]receipts.Ref, len(docs))
	for i, d := range docs {
		f.ids++
		id := fmt.Sprintf("%s%016x%0*x", receipts.IDPrefix, f.now().UnixNano(), receipts.IDLength-len(receipts.IDPrefix)-16, f.ids)
		refs[i] = receipts.Ref{ID: id, Digest: receipts.DigestOf(d)}
	}
	return f.append(account, refs, docs)
}

func (f *Fake) append(account string, refs []receipts.Ref, docs [][]byte) receipts.Position {
	if f.chains == nil {
		f.chains = map[string][]*link{}
	}
	chain := f.chains[account]
	n := uint64(len(chain)) + 1
	prev := receipts.Genesis(account)
	if len(chain) > 0 {
		prev = receipts.DigestOf(chain[len(chain)-1].doc)
	}
	doc, _ := json.Marshal(&receipts.Link{V: receipts.LinkVersion, N: n, Kind: receipts.LinkReceipts, At: f.now(), Prev: &prev, Receipts: refs}) // a Link always marshals
	l := &link{verdict: receipts.LinkOK, doc: doc, refs: refs, docs: docs, verdicts: make([]receipts.ReceiptVerdict, len(refs))}
	for i := range l.verdicts {
		l.verdicts[i] = receipts.ReceiptOK
	}
	f.chains[account] = append(chain, l)
	return receipts.Position{N: n}
}

// SetLink sets what is found of account's link n: a verdict other than
// receipts.LinkOK leaves nothing of it readable.
func (f *Fake) SetLink(account string, n uint64, v receipts.LinkVerdict) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chains[account][n-1].verdict = v
}

// SetReceipt sets what is found of the receipt at p.
func (f *Fake) SetReceipt(account string, p receipts.Position, v receipts.ReceiptVerdict) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chains[account][p.N-1].verdicts[p.Index] = v
}

// Link implements receipts.Chain: the ref, in a link of its own.
func (f *Fake) Link(ctx context.Context, account string, ref receipts.Ref) (receipts.Position, error) {
	if f.Err != nil {
		return receipts.Position{}, f.Err
	}
	if err := ctx.Err(); err != nil {
		return receipts.Position{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.append(account, []receipts.Ref{ref}, [][]byte{nil}), nil
}

// Head implements receipts.Chain.
func (f *Fake) Head(_ context.Context, account string) (receipts.Head, error) {
	if f.Err != nil {
		return receipts.Head{}, f.Err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.head(account), nil
}

func (f *Fake) head(account string) receipts.Head {
	chain := f.chains[account]
	if len(chain) == 0 {
		return receipts.Head{}
	}
	l := chain[len(chain)-1]
	h := receipts.Head{N: uint64(len(chain)), Verdict: l.verdict}
	if l.verdict != receipts.LinkForked {
		h.Digest = receipts.DigestOf(l.doc)
	}
	if l.verdict == receipts.LinkOK {
		var doc receipts.Link
		_ = json.Unmarshal(l.doc, &doc) // what append marshalled
		h.At = doc.At
	}
	return h
}

// page is the links and receipts of a page before the position before, as
// the contract pages them: newest first, at most limit receipts and
// receipts.WalkLinksMax links visited.
func (f *Fake) page(account string, before receipts.Position, limit int) (receipts.Head, []receipts.ExportedLink, receipts.Position, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	chain := f.chains[account]
	head := f.head(account)
	n, from := head.N, -1
	if before.N != 0 {
		if before.N > head.N || before.Index < 0 || before.Index > len(chain[before.N-1].refs) {
			return head, nil, receipts.Position{}, false, ErrBefore
		}
		n, from = before.N, before.Index
		if from == 0 {
			n, from = n-1, -1
		}
	}
	var out []receipts.ExportedLink
	count, visited := 0, 0
	for ; n >= 1; n-- {
		if count == limit || visited == receipts.WalkLinksMax {
			return head, out, receipts.Position{N: n + 1}, false, nil
		}
		visited++
		l := chain[n-1]
		e := receipts.ExportedLink{N: n, Verdict: l.verdict}
		if l.verdict == receipts.LinkOK || l.verdict == receipts.LinkPrev {
			e.Doc = l.doc
			i := len(l.refs) - 1
			if from >= 0 {
				i = from - 1
			}
			for ; i >= 0; i-- {
				if count == limit {
					return head, append(out, e), receipts.Position{N: n, Index: i + 1}, false, nil
				}
				r := receipts.ExportedReceipt{Index: i, ID: l.refs[i].ID, Verdict: l.verdicts[i]}
				if r.Verdict == receipts.ReceiptOK || r.Verdict == receipts.ReceiptChanged {
					r.Doc = l.docs[i]
				}
				e.Receipts = append(e.Receipts, r)
				count++
			}
		}
		from = -1
		out = append(out, e)
	}
	return head, out, receipts.Position{}, true, nil
}

// Walk implements receipts.Chain.
func (f *Fake) Walk(_ context.Context, account string, before receipts.Position, limit int) (*receipts.Walked, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	if limit < 1 {
		return nil, errors.New("receiptstest: a page of no receipt")
	}
	head, links, next, done, err := f.page(account, before, limit)
	if err != nil {
		return nil, err
	}
	w := &receipts.Walked{Head: head, Links: []receipts.WalkedLink{}, Next: next, Done: done}
	for _, e := range links {
		wl := receipts.WalkedLink{N: e.N, Verdict: e.Verdict}
		if e.Doc != nil {
			wl.Link = &receipts.Link{}
			_ = json.Unmarshal(e.Doc, wl.Link) // what append marshalled
		}
		for _, r := range e.Receipts {
			wr := receipts.WalkedReceipt{Index: r.Index, Ref: wl.Link.Receipts[r.Index], Verdict: r.Verdict}
			if r.Verdict == receipts.ReceiptOK {
				var doc receipts.Receipt
				if json.Unmarshal(r.Doc, &doc) == nil {
					wr.Receipt = &doc
				}
			}
			wl.Receipts = append(wl.Receipts, wr)
		}
		w.Links = append(w.Links, wl)
	}
	return w, nil
}

// Export implements receipts.Chain.
func (f *Fake) Export(_ context.Context, account string, before receipts.Position) (*receipts.ExportPage, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	head, links, next, done, err := f.page(account, before, receipts.WalkLinksMax*receipts.LinkReceiptsMax)
	if err != nil {
		return nil, err
	}
	if links == nil {
		links = []receipts.ExportedLink{}
	}
	return &receipts.ExportPage{V: receipts.ExportVersion, Account: account, Head: head, Links: links, Next: next, Done: done}, nil
}

// Audit implements receipts.Auditor: Pass.
func (f *Fake) Audit(context.Context) (*receipts.AuditPass, error) { return f.Pass, f.Err }

// Audited implements receipts.Auditor: Pass.
func (f *Fake) Audited(context.Context) (*receipts.AuditPass, error) { return f.Pass, f.Err }

// Broken implements receipts.Auditor: every kind, at zero unless Found
// says otherwise.
func (f *Fake) Broken() map[string]int64 {
	out := make(map[string]int64, len(attestation.ReceiptsBrokenKinds))
	for _, k := range attestation.ReceiptsBrokenKinds {
		out[k] = 0
	}
	maps.Copy(out, f.Found)
	return out
}
