package receipts

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// The audit (ADR 0037, "The audit"): a pass over every chain the catalogue
// names, each checked as a walk checks it, its links joined with the
// receipts stored in its account's partition; what it found broken
// recorded as incidents before the pass is, and the pass a strict record
// every enclave reports.

// auditChunk is how many records of a series the audit lists at once.
const auditChunk = 1000

// accountsPage is how many accounts of the accounts' list the audit reads
// at once (domain.MaxAccountsPage).
const accountsPage = 100

// errWindow is a chain whose refs the audit must hold at once pass
// AuditWindowRefsMax: the pass fails, and does not finish.
var errWindow = errors.New("receipts: a chain holds more refs in the audit's window than it may")

// errFence is an audit's write for an account that did not begin within
// AuditWriteAfterCheck of the answer that the account is live: it asks
// again.
var errFence = errors.New("receipts: the audit's write began too long after it asked whether the account is removed")

type writeByKey struct{}

// Audit implements Auditor.
func (s *Service) Audit(ctx context.Context) (*AuditPass, error) {
	if s.chain == nil {
		return nil, errNotChained
	}
	ctx, cancel := context.WithTimeout(ctx, AuditRunMax)
	defer cancel()
	newest, err := s.Audited(ctx)
	if err != nil {
		return nil, err
	}
	began := s.now().UTC()
	if newest != nil && began.Sub(newest.Audit.Began) < AuditEvery {
		return nil, nil
	}
	key, err := s.chainKey(ctx)
	if err != nil {
		return nil, err
	}
	rkey, err := s.keys.Derive(ctx, Purpose)
	if err != nil {
		return nil, err
	}
	r := &auditRun{s: s, key: key, rkey: rkey, began: began, audited: map[string]bool{}}
	r.pass.Began = began
	r.pass.Broken = map[string]int64{}
	for _, k := range attestation.ReceiptsBrokenKinds {
		r.pass.Broken[k] = 0
	}
	if err := r.catalogue(ctx); err != nil {
		return nil, err
	}
	if err := r.unchained(ctx); err != nil {
		return nil, err
	}
	return r.finish(ctx)
}

// Audited implements Auditor: the newest pass that can be read, found by
// listing the passes' commits.
func (s *Service) Audited(ctx context.Context) (*AuditPass, error) {
	if s.chain == nil {
		return nil, errNotChained
	}
	key, err := s.chainKey(ctx)
	if err != nil {
		return nil, err
	}
	j, err := s.findHead(ctx, key, passSeriesOf)
	for ; err == nil && j.n > 0; j, err = s.readOne(ctx, key, passSeriesOf, j.n-1) {
		if j.verdict == LinkOK {
			return j.p.pass, nil
		}
		if j.n == 1 {
			return nil, nil
		}
	}
	return nil, err
}

// auditRun is one pass.
type auditRun struct {
	s         *Service
	key, rkey []byte
	began     time.Time
	pass      attestation.ReceiptsAudit
	findings  []Finding
	audited   map[string]bool
}

// count adds findings to the pass.
func (r *auditRun) count(findings []Finding) {
	for _, f := range findings {
		r.pass.Broken[f.Kind]++
	}
	r.findings = append(r.findings, findings...)
}

// catalogue reads the catalogue from its head down, every entry strictly
// and against the one above, and audits each chain an entry names, once.
func (r *auditRun) catalogue(ctx context.Context) error {
	sr := catalogueSeriesOf
	head, err := r.s.findHead(ctx, r.key, sr)
	if err != nil {
		return err
	}
	r.pass.Catalogue = head.n
	return r.s.down(ctx, r.key, sr, head.n, func(j judged) error {
		if f, ok := sr.finding(j); ok {
			r.count([]Finding{f})
		}
		if j.verdict == LinkExpired {
			r.pass.Expired++
		}
		if !j.verdict.Readable() {
			return nil
		}
		for _, account := range j.p.cat.Accounts {
			if !r.audited[account] {
				r.audited[account] = true
				if err := r.chain(ctx, account); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// down reads records hi to 1 of sr, the newest first, a chunk at a time,
// each strictly and authenticated by the one above (by its commit below a
// record that cannot be read).
func (s *Service) down(ctx context.Context, key []byte, sr *series, hi uint64, each func(judged) error) error {
	var expected *Digest
	for hi > 0 {
		lo := hi - min(hi, auditChunk) + 1
		members := map[uint64]*store.Member{}
		if err := s.readRange(ctx, sr, lo, hi, 0, func(n uint64, m *store.Member) error {
			members[n] = m
			return nil
		}); err != nil {
			return fmt.Errorf("receipts: read the chain: %w", err)
		}
		for n := hi; n >= lo; n-- {
			j, err := s.judge(ctx, key, sr, n, members[n], expected)
			if err != nil {
				return err
			}
			expected = j.p.prev
			if err := each(j); err != nil {
				return err
			}
		}
		hi = lo - 1
	}
	return nil
}

// fenced runs write for account, the audit's own work, only once
// Removing answered live, and asks again when write did not begin within
// AuditWriteAfterCheck of the answer (errFence, from create). removed says
// it did not run.
func (r *auditRun) fenced(ctx context.Context, account string, write func(ctx context.Context) error) (removed bool, err error) {
	for {
		asked := r.s.now()
		removed, err := r.s.chain.accounts.Removing(ctx, account)
		if err != nil || removed {
			return removed, err
		}
		err = write(context.WithValue(ctx, writeByKey{}, asked.Add(AuditWriteAfterCheck)))
		if !errors.Is(err, errFence) {
			return false, err
		}
	}
}

// adopt writes what adoption must of account's chain, begun or not: its
// entry in the catalogue and its adopted links.
func (r *auditRun) adopt(ctx context.Context, account string) error {
	_, err := r.fenced(ctx, account, func(ctx context.Context) error {
		l := r.s.chain.appender(r.s, account)
		l.write.Lock()
		defer l.write.Unlock()
		l.head = nil
		_, err := l.putLocked(ctx, r.key, nil, nil)
		return err
	})
	return err
}

// chain audits account's chain.
func (r *auditRun) chain(ctx context.Context, account string) error {
	r.pass.Chains++
	sr := links(account)
	head, err := r.s.findHead(ctx, r.key, sr)
	if err != nil {
		return err
	}
	if adopting(head) {
		// The catalogue names it: adopting does not enter it again.
		r.s.chain.mu.Lock()
		r.s.chain.entered[account] = true
		r.s.chain.mu.Unlock()
		if err := r.adopt(ctx, account); err != nil {
			return err
		}
		if head, err = r.s.findHead(ctx, r.key, sr); err != nil {
			return err
		}
	}
	j, err := r.join(ctx, sr, head)
	if err != nil {
		return err
	}
	if len(j.findings) > 0 {
		removed, err := r.s.chain.accounts.Removing(ctx, account)
		if err != nil {
			return err
		}
		if removed {
			r.pass.Removed++
			return nil
		}
		r.count(j.findings)
	}
	r.pass.Unlinked += j.unlinked
	r.pass.Expired += j.expired
	if len(j.orphans) == 0 {
		return nil
	}
	_, err = r.fenced(ctx, account, func(ctx context.Context) error {
		n, err := r.s.recover(ctx, r.key, account, head.n, j.orphans)
		r.pass.Recovered += int64(n)
		return err
	})
	return err
}

// adopting says a chain's head leaves adoption to do: not begun, or an
// adopted link with more after it.
func adopting(head judged) bool {
	return head.n == 0 || (head.verdict == LinkOK && head.p.link.Kind == LinkAdopted && head.p.link.More)
}

// unchained begins the chain of every account of the accounts' list that
// has receipts and no chain (ADR 0037, R3): entered, then adopting, under
// the fence of its removal. It only hastens adoption: what a pass covers
// is what the catalogue names.
func (r *auditRun) unchained(ctx context.Context) error {
	after := ""
	for {
		list, err := r.s.chain.accounts.AccountRecords(ctx, after, accountsPage)
		if err != nil {
			return err
		}
		for _, a := range list.Accounts {
			if r.audited[a.ID] || !validAccount(a.ID) {
				continue
			}
			es, err := r.s.entries.ListEntries(ctx, Partition(a.ID), IDPrefix, "", 1)
			if err != nil {
				return err
			}
			if len(es) == 0 {
				continue
			}
			head, err := r.s.findHead(ctx, r.key, links(a.ID))
			if err != nil {
				return err
			}
			if head.n == 0 {
				if err := r.adopt(ctx, a.ID); err != nil {
					return err
				}
			}
		}
		if list.Next == "" {
			return nil
		}
		after = list.Next
	}
}

// finish records what the pass found and makes it a strict record, at the
// number after the newest. Two enclaves finishing at once make two, unless
// their documents are the same bytes (the same instants and counts), which
// are one record.
func (r *auditRun) finish(ctx context.Context) (*AuditPass, error) {
	if err := r.s.record(ctx, r.findings); err != nil {
		return nil, err
	}
	r.pass.Finished = r.s.now().UTC()
	head, err := r.s.findHead(ctx, r.key, passSeriesOf)
	if err != nil {
		return nil, err
	}
	n := head.n
	for range LinkTries {
		if n == math.MaxUint64 {
			return nil, errFull
		}
		p := &AuditPass{V: AuditVersion, P: n + 1, Audit: r.pass}
		j, own, err := r.s.create(ctx, r.key, passSeriesOf, n+1, mustJSON(p))
		if err != nil {
			return nil, err
		}
		if own {
			return p, nil
		}
		n = j.n
	}
	return nil, errTries
}

// joined is what joining a chain's links with its account's receipts
// found.
type joined struct {
	findings []Finding
	orphans  []Ref
	unlinked int64
	expired  int64
}

// held is a ref the join holds until it meets its receipt.
type held struct {
	ref Ref
	n   uint64
}

// ids is a max-heap of identifiers.
type ids []string

func (h ids) Len() int           { return len(h) }
func (h ids) Less(i, j int) bool { return h[i] > h[j] }
func (h ids) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *ids) Push(x any)        { *h = append(*h, x.(string)) } //nolint:forcetypeassert // heap.Push's
func (h *ids) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// joiner joins a chain's links, the newest first, with its account's
// receipts, the newest first (ADR 0037, "Joining refs and receipts"; done
// from the top down, as a walk reads). It holds the refs of the links read
// whose receipts the receipts' cursor has not reached: a link is read once
// its time is no earlier than the receipt's less twice AdoptAfter (two
// clocks' minute, and a link written after its receipts), and an adopted
// link once its newest ref is no older than the receipt. held counts every
// ref it holds, each link naming a receipt counted, since a receipt written
// with its own identity can be named again by a retry's link: the window
// bounds that count, never the receipts' alone (the review of #170,
// finding 4).
type joiner struct {
	r         *auditRun
	sr        *series
	partition string
	head      uint64
	out       joined
	refs      map[string][]held
	order     ids
	held      int
	links     []judged
	next      uint64
	expected  *Digest
}

// join joins sr's links, from head, with the receipts of its account.
func (r *auditRun) join(ctx context.Context, sr *series, head judged) (joined, error) {
	j := &joiner{r: r, sr: sr, partition: Partition(sr.account), head: head.n, refs: map[string][]held{}, next: head.n}
	before := ""
	for {
		scan, err := r.s.chain.scanner.ScanEntriesDown(ctx, j.partition, IDPrefix, before, store.MaxScanRead)
		if err != nil {
			return joined{}, fmt.Errorf("receipts: read the receipts: %w", err)
		}
		for i := range scan.Entries {
			if err := j.receipt(ctx, &scan.Entries[i]); err != nil {
				return joined{}, err
			}
		}
		if !scan.More {
			break
		}
		before = scan.Last
	}
	for j.next > 0 || len(j.links) > 0 {
		if err := j.consume(ctx); err != nil {
			return joined{}, err
		}
	}
	return j.out, j.resolve(ctx, "")
}

// peek is the next link not yet consumed, read when it is not; nil when
// none is left.
func (j *joiner) peek(ctx context.Context) (*judged, error) {
	if len(j.links) == 0 && j.next > 0 {
		hi := j.next
		lo := hi - min(hi, auditChunk) + 1
		members := map[uint64]*store.Member{}
		if err := j.r.s.readRange(ctx, j.sr, lo, hi, 0, func(n uint64, m *store.Member) error {
			members[n] = m
			return nil
		}); err != nil {
			return nil, fmt.Errorf("receipts: read the chain: %w", err)
		}
		for n := hi; n >= lo; n-- {
			l, err := j.r.s.judge(ctx, j.r.key, j.sr, n, members[n], j.expected)
			if err != nil {
				return nil, err
			}
			j.expected = l.p.prev
			if f, ok := j.sr.finding(l); ok {
				j.out.findings = append(j.out.findings, f)
			}
			if l.verdict == LinkExpired {
				j.out.expired++
			}
			// A link's document holds only its refs once judged.
			l.doc = nil
			j.links = append(j.links, l)
		}
		j.next = lo - 1
	}
	if len(j.links) == 0 {
		return nil, nil
	}
	return &j.links[0], nil
}

// consume takes the next link's refs into the window.
func (j *joiner) consume(ctx context.Context) error {
	l, err := j.peek(ctx)
	if err != nil || l == nil {
		return err
	}
	j.links = j.links[1:]
	if !l.verdict.Readable() {
		return nil
	}
	for _, ref := range l.p.link.Receipts {
		if len(j.refs[ref.ID]) == 0 {
			heap.Push(&j.order, ref.ID)
		}
		j.refs[ref.ID] = append(j.refs[ref.ID], held{ref: ref, n: l.n})
		j.held++
	}
	if j.held > AuditWindowRefsMax {
		return errWindow
	}
	return nil
}

// due says the next link must be read before receipt id of time t is
// taken for one no link names.
func (j *joiner) due(ctx context.Context, id string, t time.Time) (bool, error) {
	l, err := j.peek(ctx)
	if err != nil || l == nil {
		return false, err
	}
	if !l.verdict.Readable() {
		return true, nil
	}
	link := l.p.link
	if link.Kind == LinkAdopted {
		return link.Receipts[len(link.Receipts)-1].ID >= id, nil
	}
	return !link.At.Before(t.Add(-2 * AdoptAfter)), nil
}

// receipt takes the next stored receipt, the newest first.
func (j *joiner) receipt(ctx context.Context, e *store.Entry) error {
	t, ok := idTime(e.Key)
	if !ok {
		return nil
	}
	for {
		due, err := j.due(ctx, e.Key, t)
		if err != nil {
			return err
		}
		if !due {
			break
		}
		if err := j.consume(ctx); err != nil {
			return err
		}
	}
	if err := j.resolve(ctx, e.Key); err != nil {
		return err
	}
	if refs := j.refs[e.Key]; len(refs) > 0 {
		heap.Pop(&j.order)
		delete(j.refs, e.Key)
		j.held -= len(refs)
		for _, h := range refs {
			j.verdict(h, e)
		}
		return nil
	}
	raw, _, err := openRaw(j.r.rkey, e)
	age := j.r.began.Sub(t)
	switch {
	case err != nil:
		j.out.findings = append(j.out.findings, Finding{Kind: "receipt_unreadable", Account: j.sr.account, N: max(j.head, 1), Receipt: e.Key})
	case age < RecoverAfter:
		// In flight: its Write may still link it.
	case age < RecoverWithin:
		j.out.orphans = append(j.out.orphans, Ref{ID: e.Key, Digest: DigestOf(raw)})
	default:
		j.out.unlinked++
	}
	return nil
}

// resolve reads again every held ref above id ("" for all of them), which
// the receipts' cursor passed without returning: a receipt stored behind
// it is found and checked, and one that is not there is missing.
func (j *joiner) resolve(ctx context.Context, id string) error {
	for len(j.order) > 0 && (id == "" || j.order[0] > id) {
		ref := heap.Pop(&j.order).(string) //nolint:forcetypeassert // ids holds strings
		refs := j.refs[ref]
		delete(j.refs, ref)
		j.held -= len(refs)
		e, err := j.r.s.entries.GetEntry(ctx, j.partition, ref)
		if errors.Is(err, store.ErrNotFound) {
			for _, h := range refs {
				j.out.findings = append(j.out.findings, Finding{Kind: "receipt_missing", Account: j.sr.account, N: h.n, Receipt: ref})
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("receipts: read a receipt: %w", err)
		}
		for _, h := range refs {
			j.verdict(h, e)
		}
	}
	return nil
}

// verdict checks stored receipt e against the ref h holds.
func (j *joiner) verdict(h held, e *store.Entry) {
	pr, _ := checkEntry(j.r.rkey, e, 0, h.ref)
	if pr.verdict != ReceiptOK {
		j.out.findings = append(j.out.findings, Finding{Kind: "receipt_" + string(pr.verdict), Account: j.sr.account, N: h.n, Receipt: h.ref.ID})
	}
}
