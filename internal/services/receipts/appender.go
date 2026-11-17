package receipts

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/store"
)

// Appending (ADR 0037, "Appending"): one appender per series in each
// enclave, one record in flight at a time. An account's links group the
// refs of receipts written while its previous link is being committed.

var (
	errWaiting = errors.New("receipts: too many receipts of the account wait for a link")
	errTries   = errors.New("receipts: another enclave took every number tried")
	errFull    = errors.New("receipts: a chain at its last number takes no more record")
	errStopped = errors.New("receipts: nothing left to link")
)

// appender writes the records of one series, one at a time, keeping the
// head it last read or wrote.
type appender struct {
	s     *Service
	sr    *series
	write sync.Mutex
	head  *judged
	// links says the series is an account's links: its first link is made
	// once the account is entered in the catalogue, and after the receipts
	// it had before are adopted.
	links bool
}

// builder makes the document of the record at n, over the record before
// it named by prev or br.
type builder func(n uint64, at time.Time, prev *Digest, br BreakReason) []byte

// mustJSON marshals a document of the chain, which always marshals.
func mustJSON(v any) []byte {
	b, _ := json.Marshal(v) // the chain's documents hold nothing that fails
	return b
}

// known is the head, found when it is not known.
func (a *appender) known(ctx context.Context, key []byte) (judged, error) {
	if a.head == nil {
		j, err := a.s.findHead(ctx, key, a.sr)
		if err != nil {
			return judged{}, err
		}
		a.head = &j
	}
	return *a.head, nil
}

// putLocked writes the next record of the series with a.write held: build
// makes its document at the number after the head, after what adoption
// must write first for a chain of links (a nil build writes only that, and
// returns the head). A number another enclave took is
// read, taken as the head, and the next tried, at most LinkTries times.
// seen, when set, is told of each record written above the head the call
// began with that is not the one it returns (another enclave's, or this
// call's adoption), and stops the call (errStopped) by answering false. A
// head that cannot be read is recorded as an incident before a record
// names it as a break. Any other error forgets the head: it is found again
// next time.
func (a *appender) putLocked(ctx context.Context, key []byte, build builder, seen func(judged) bool) (judged, error) {
	tries := 0
	for {
		head, err := a.known(ctx, key)
		if err != nil {
			return judged{}, err
		}
		if head.n == math.MaxUint64 {
			return judged{}, errFull
		}
		if f, ok := a.sr.finding(head); ok {
			if err := a.s.record(ctx, []Finding{f}); err != nil {
				return judged{}, err
			}
		}
		doc, adopting, err := a.document(ctx, key, head, build)
		if err != nil {
			a.head = nil
			return judged{}, err
		}
		if doc == nil {
			return head, nil
		}
		j, own, err := a.s.create(ctx, key, a.sr, head.n+1, doc)
		if err != nil {
			a.head = nil
			return judged{}, err
		}
		a.head = &j
		switch {
		case own && !adopting:
			return j, nil
		case seen != nil && !seen(j):
			return judged{}, errStopped
		case !own:
			tries++
			if tries == LinkTries {
				return judged{}, errTries
			}
		}
	}
}

// document is the record to write after head: for a chain of links not
// begun, or whose adoption goes on, the next adopted link (adopting true),
// once the account is in the catalogue; else build's.
func (a *appender) document(ctx context.Context, key []byte, head judged, build builder) ([]byte, bool, error) {
	at := a.s.now().UTC()
	prev, br := prevOf(a.sr, head)
	if a.links {
		if head.n == 0 {
			if err := a.s.enter(ctx, key, a.sr.account); err != nil {
				return nil, false, err
			}
		}
		refs, before, more, err := a.adoptable(ctx, head, at)
		if err != nil {
			return nil, false, err
		}
		if len(refs) > 0 {
			l := Link{V: LinkVersion, N: head.n + 1, Kind: LinkAdopted, At: at, Prev: prev, Break: br, Before: before, More: more, Receipts: refs}
			return mustJSON(&l), true, nil
		}
	}
	if build == nil {
		return nil, false, nil
	}
	return build(head.n+1, at, prev, br), false, nil
}

// adoptPage is how many receipts adoption lists at once.
const adoptPage = 100

// adoptable is what the next adopted link names (ADR 0037, "Receipts
// written before the chain"): for a chain not begun, the account's
// receipts written before at less AdoptAfter, in key order; for one whose
// head is an adopted link with More, those after its last, before its
// Before. An entry that does not open is never adopted. more says that
// another adoptable receipt follows the LinkReceiptsMax returned.
func (a *appender) adoptable(ctx context.Context, head judged, at time.Time) (refs []Ref, before time.Time, more bool, err error) {
	after := ""
	switch l := head.p.link; {
	case head.n == 0:
		before = at.Add(-AdoptAfter)
	case head.verdict == LinkOK && l.Kind == LinkAdopted && l.More:
		before, after = l.Before, l.Receipts[len(l.Receipts)-1].ID
	default:
		return nil, time.Time{}, false, nil
	}
	rkey, err := a.s.keys.Derive(ctx, Purpose)
	if err != nil {
		return nil, time.Time{}, false, err
	}
	partition := Partition(a.sr.account)
	for {
		es, err := a.s.entries.ListEntries(ctx, partition, IDPrefix, after, adoptPage)
		if err != nil {
			return nil, time.Time{}, false, err
		}
		for i := range es {
			after = es[i].Key
			t, ok := idTime(es[i].Key)
			if !ok {
				continue
			}
			if !t.Before(before) {
				return refs, before, false, nil
			}
			raw, _, err := openRaw(rkey, &es[i])
			if err != nil {
				continue
			}
			if len(refs) == LinkReceiptsMax {
				return refs, before, true, nil
			}
			refs = append(refs, Ref{ID: es[i].Key, Digest: DigestOf(raw)})
		}
		if len(es) < adoptPage {
			return refs, before, false, nil
		}
	}
}

// idTime is the time a receipt's identifier holds, ok false for what is
// not one.
func idTime(id string) (time.Time, bool) {
	if !ValidID(id) {
		return time.Time{}, false
	}
	ns, _ := strconv.ParseUint(id[len(IDPrefix):len(IDPrefix)+16], 16, 64) // ValidID: hexadecimal
	return time.Unix(0, int64(ns)).UTC(), true                             //nolint:gosec // a time Write made
}

// waiter is one ref waiting for its account's link.
type waiter struct {
	ref   Ref
	ctx   context.Context //nolint:containedctx // its deadline bounds the group's
	taken bool
	done  chan placed
}

type placed struct {
	at  Position
	err error
}

// linkAppender is an account's appender of links, with the refs waiting
// for the next.
type linkAppender struct {
	appender
	mu      sync.Mutex
	waiting []*waiter
	busy    bool
}

// add waits until a committed link names ref, or fails.
func (l *linkAppender) add(ctx context.Context, ref Ref) (Position, error) {
	l.mu.Lock()
	if len(l.waiting) >= LinkWaitingMax {
		l.mu.Unlock()
		return Position{}, errWaiting
	}
	w := &waiter{ref: ref, ctx: ctx, done: make(chan placed, 1)}
	l.waiting = append(l.waiting, w)
	if !l.busy {
		l.busy = true
		go l.drive()
	}
	l.mu.Unlock()
	select {
	case p := <-w.done:
		return p.at, p.err
	case <-ctx.Done():
		l.mu.Lock()
		if !w.taken {
			l.waiting = slices.DeleteFunc(l.waiting, func(x *waiter) bool { return x == w })
		}
		l.mu.Unlock()
		return Position{}, ctx.Err()
	}
}

// drive links the waiting refs, a group at a time, until none waits.
func (l *linkAppender) drive() {
	for {
		l.mu.Lock()
		if len(l.waiting) == 0 {
			l.busy = false
			l.mu.Unlock()
			return
		}
		group := l.waiting[:min(len(l.waiting), LinkReceiptsMax)]
		l.waiting = slices.Clone(l.waiting[len(group):])
		refs := make([]Ref, len(group))
		deadline := time.Time{}
		for i, w := range group {
			w.taken = true
			refs[i] = w.ref
			d, ok := w.ctx.Deadline()
			if !ok {
				d = time.Now().Add(ReceiptWriteMax)
			}
			if d.After(deadline) {
				deadline = d
			}
		}
		l.mu.Unlock()
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		n, err := l.link(ctx, refs)
		cancel()
		for i, w := range group {
			w.done <- placed{at: Position{N: n, Index: i}, err: err}
		}
	}
}

// link makes the account's next link of kind LinkReceipts naming refs.
func (l *linkAppender) link(ctx context.Context, refs []Ref) (uint64, error) {
	key, err := l.s.chainKey(ctx)
	if err != nil {
		return 0, err
	}
	l.write.Lock()
	defer l.write.Unlock()
	j, err := l.putLocked(ctx, key, func(n uint64, at time.Time, prev *Digest, br BreakReason) []byte {
		return mustJSON(&Link{V: LinkVersion, N: n, Kind: LinkReceipts, At: at, Prev: prev, Break: br, Receipts: refs})
	}, nil)
	return j.n, err
}

// appendersMax bounds the accounts whose appenders an enclave keeps: past
// it, idle ones are dropped, and find their head again when used.
const appendersMax = 1024

// appender is account's link appender.
func (c *chainState) appender(s *Service, account string) *linkAppender {
	c.mu.Lock()
	defer c.mu.Unlock()
	if l := c.appenders[account]; l != nil {
		return l
	}
	if len(c.appenders) >= appendersMax {
		for acc, l := range c.appenders {
			if l.idle() {
				delete(c.appenders, acc)
			}
		}
	}
	l := &linkAppender{appender: appender{s: s, sr: links(account), links: true}}
	c.appenders[account] = l
	return l
}

// idle says nothing waits for l and nothing is being written.
func (l *linkAppender) idle() bool {
	l.mu.Lock()
	busy := l.busy || len(l.waiting) > 0
	l.mu.Unlock()
	if busy || !l.write.TryLock() {
		return false
	}
	l.write.Unlock()
	return true
}

// enteredMax bounds the accounts an enclave remembers having entered in the
// catalogue: one forgotten is entered again, which costs the audit a
// listing.
const enteredMax = 4096

// enter enters account in the catalogue of chains, before its link 1 is
// made (ADR 0037, "The catalogue of chains").
func (s *Service) enter(ctx context.Context, key []byte, account string) error {
	c := s.chain
	c.mu.Lock()
	done := c.entered[account]
	c.mu.Unlock()
	if done {
		return nil
	}
	c.catalogue.write.Lock()
	defer c.catalogue.write.Unlock()
	if _, err := c.catalogue.putLocked(ctx, key, func(n uint64, at time.Time, prev *Digest, br BreakReason) []byte {
		return mustJSON(&Catalogue{V: CatalogueVersion, K: n, At: at, Prev: prev, Break: br, Accounts: []string{account}})
	}, nil); err != nil {
		return err
	}
	c.mu.Lock()
	if len(c.entered) >= enteredMax {
		clear(c.entered)
	}
	c.entered[account] = true
	c.mu.Unlock()
	return nil
}

// recover links refs, receipts of account the audit found without a link,
// in links of kind LinkRecovered (ADR 0037, "Receipts left without a
// link"): only over a head whose every link above from, the head the pass
// read the chain at, is read and names none of them; a ref such a link
// names is dropped, and a link above from that cannot be read stops it. It
// returns how many it linked.
func (s *Service) recover(ctx context.Context, key []byte, account string, from uint64, refs []Ref) (int, error) {
	l := s.chain.appender(s, account)
	l.write.Lock()
	defer l.write.Unlock()
	l.head = nil
	head, err := l.known(ctx, key)
	if err != nil {
		return 0, err
	}
	drop := func(j judged) bool {
		if !j.verdict.Readable() {
			return false
		}
		for _, r := range j.p.link.Receipts {
			refs = slices.DeleteFunc(refs, func(x Ref) bool { return x.ID == r.ID })
		}
		return true
	}
	if head.n > from {
		want := from + 1
		stopped := false
		err := s.readRange(ctx, l.sr, from+1, head.n, 0, func(n uint64, m *store.Member) error {
			j, err := s.judge(ctx, key, l.sr, n, m, nil)
			if err != nil {
				return err
			}
			stopped = stopped || n != want || !drop(j)
			want = n + 1
			return nil
		})
		if err != nil {
			return 0, err
		}
		if stopped || want != head.n+1 {
			return 0, nil
		}
	}
	if len(refs) == 0 {
		return 0, nil
	}
	_, err = l.putLocked(ctx, key, func(n uint64, at time.Time, prev *Digest, br BreakReason) []byte {
		return mustJSON(&Link{V: LinkVersion, N: n, Kind: LinkRecovered, At: at, Prev: prev, Break: br, Receipts: refs})
	}, func(j judged) bool { return drop(j) && len(refs) > 0 })
	if errors.Is(err, errStopped) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return len(refs), nil
}
