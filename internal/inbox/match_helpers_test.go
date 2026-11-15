package inbox_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/inboxtest"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/store"
)

// The matcher's tests (ADR 0027, "Evaluation over stored mail") run against
// the real query.Eval: what the matcher does with a value is what Eval says
// of the sources it read, and formulas are canonical (Canonicalise).

// qNow is when the matcher's tests read.
var qNow = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

func cond(v contracts.Variable, op contracts.Operator, value string) contracts.Condition {
	return contracts.Condition{Var: v, Op: op, Value: value}
}

func not(c contracts.Condition) contracts.Condition { c.Not = true; return c }

// formula is the canonical form of a formula of groups, each the
// conditions given; a formula that does not canonicalise is the test's bug.
func formula(groups ...[]contracts.Condition) *query.Canonical {
	f := contracts.Formula{V: contracts.FormulaVersion, All: []contracts.Group{}}
	for _, g := range groups {
		f.All = append(f.All, contracts.Group{Any: g})
	}
	c, err := query.Canonicalise(f)
	if err != nil {
		panic(fmt.Sprintf("formula %+v: %v", f, err))
	}
	return c
}

// handBuilt is a formula of groups as given, not canonical: Eval says
// Unknown of every condition that is not, and the matcher must still read it
// safely.
func handBuilt(groups ...[]contracts.Condition) *query.Canonical {
	f := &query.Canonical{Formula: contracts.Formula{V: contracts.FormulaVersion, All: []contracts.Group{}}}
	for _, g := range groups {
		f.Formula.All = append(f.Formula.All, contracts.Group{Any: g})
	}
	return f
}

func group(cs ...contracts.Condition) []contracts.Condition { return cs }

// qStore is the memory store, counting and failing what the matcher reads.
type qStore struct {
	*store.Memory
	mu      sync.Mutex
	scans   []*store.MessageScan
	befores []string
	entries []string
	raws    []string
	// Hooks: called before the read, with what it is about; an error fails
	// the read.
	onScan  func() error
	onEntry func(key string) error
	onRaw   func(id string) error
}

func newQStore() *qStore {
	mem := store.NewMemory()
	mem.Now = func() time.Time { return qNow }
	return &qStore{Memory: mem}
}

func (s *qStore) ScanMessagesDown(ctx context.Context, mailboxID, before string, maxRead int) (*store.MessageScan, error) {
	if s.onScan != nil {
		if err := s.onScan(); err != nil {
			return nil, err
		}
	}
	scan, err := s.Memory.ScanMessagesDown(ctx, mailboxID, before, maxRead)
	if err == nil {
		s.mu.Lock()
		s.scans = append(s.scans, scan)
		s.befores = append(s.befores, before)
		s.mu.Unlock()
	}
	return scan, err
}

func (s *qStore) GetEntry(ctx context.Context, partition, key string) (*store.Entry, error) {
	s.mu.Lock()
	s.entries = append(s.entries, strings.TrimPrefix(key, inbox.KeySearch))
	s.mu.Unlock()
	if s.onEntry != nil {
		if err := s.onEntry(key); err != nil {
			return nil, err
		}
	}
	return s.Memory.GetEntry(ctx, partition, key)
}

func (s *qStore) ReadRaw(ctx context.Context, mailboxID, id string, max int64) ([]byte, error) {
	s.mu.Lock()
	s.raws = append(s.raws, id)
	s.mu.Unlock()
	if s.onRaw != nil {
		if err := s.onRaw(id); err != nil {
			return nil, err
		}
	}
	return s.Memory.ReadRaw(ctx, mailboxID, id, max)
}

func (s *qStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scans, s.befores, s.entries, s.raws = nil, nil, nil, nil
}

// qMsg is a message the matcher's tests seed: its stored parts, and how
// they were written.
type qMsg struct {
	at      time.Time
	n       int
	from    []string
	to      []string
	subject string
	body    string // the message's whole text
	list    string
	attach  []contracts.MessageAttachment
	// header: the From header as sent, for an address as delivery stores
	// it (parsed); otherwise from is stored as given. signedBy: the domains
	// of DKIM signatures that passed.
	header   string
	signedBy []string
	// old: written before ADR 0027 (a meta of format 1, an entry without
	// Cut). size: the meta's size when it is not the raw length.
	old          bool
	size         int64
	partial      bool
	unparsed     bool
	confirmation bool
	// What is wrong with what is stored: an item past its days, a meta or
	// an entry that does not open, no entry, a raw message larger than the
	// item says.
	expired, badMeta, badEntry, noEntry, rawTooLarge bool
}

// unreadable reports whether the raw message is past the bound its item
// says: the smallest bound a read is given is the seal's overhead, 1 KiB,
// which a short message fits in whatever its item says.
func (q *qMsg) unreadable() bool { return q.rawTooLarge && len(q.raw()) > 2<<10 }

func (q *qMsg) id() string { return fmt.Sprintf("%016x%016x", q.at.UnixNano(), q.n) }

func (q *qMsg) raw() []byte {
	var b strings.Builder
	if q.header != "" {
		b.WriteString("From: " + q.header + "\r\n")
	} else {
		b.WriteString("From: " + strings.Join(q.from, ", ") + "\r\n")
	}
	if len(q.to) > 0 {
		b.WriteString("To: " + strings.Join(q.to, ", ") + "\r\n")
	}
	b.WriteString("Subject: " + q.subject + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + q.body)
	return []byte(b.String())
}

func (q *qMsg) meta() inbox.StoredMeta {
	meta := inbox.StoredMeta{Subject: q.subject, Attachments: q.attach, Partial: q.partial, Unparsed: q.unparsed, Confirmation: q.confirmation}
	for _, a := range q.from {
		meta.From = append(meta.From, contracts.MailAddress{Address: a})
	}
	if q.header != "" {
		meta.From = inbox.Parse(context.Background(), q.raw()).Meta.From
	}
	for _, d := range q.signedBy {
		meta.Auth = append(meta.Auth, contracts.AuthCheck{Method: "dkim", Domain: d, Result: "pass"})
	}
	for _, a := range q.to {
		meta.To = append(meta.To, contracts.MailAddress{Address: a})
	}
	if !q.old {
		meta.Format, meta.List, meta.Size = inbox.MetaFormat, q.list, int64(len(q.raw()))
		if q.size != 0 {
			meta.Size = q.size
		}
	}
	return meta
}

func (q *qMsg) entry() *inbox.SearchEntry {
	text := q.body
	cut := false
	if len(text) > inbox.SearchTextMax {
		text, cut = text[:inbox.SearchTextMax], true
		for !utf8Valid(text) {
			text = text[:len(text)-1]
		}
	}
	return &inbox.SearchEntry{Subject: q.subject, Text: text, ReceivedAt: q.at, Cut: cut && !q.old}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "\x00") == s }

// seedQ stores q in the box as delivery does, then spoils what q says.
func seedQ(t testing.TB, st *qStore, b inbox.Box, q *qMsg) string {
	t.Helper()
	ctx := context.Background()
	received, days := q.at, 30
	if q.expired {
		received = qNow.AddDate(0, 0, -60)
	} else if qNow.Sub(q.at) > 20*24*time.Hour {
		days = 3650
	}
	m := inboxtest.Message{ID: q.id(), ReceivedAt: received, RetentionDays: days, Raw: q.raw(), Meta: q.meta(), Search: q.entry(), Old: q.old}
	if err := inboxtest.Seed(ctx, st.Memory, st.Memory, b, m); err != nil {
		t.Fatal(err)
	}
	if q.expired {
		return q.id() // never read again: nothing to spoil
	}
	part := inbox.Partition(b.MailboxID)
	switch {
	case q.noEntry:
		if err := st.DeleteEntry(ctx, part, inbox.KeySearch+q.id()); err != nil {
			t.Fatal(err)
		}
	case q.badEntry:
		e, err := st.Memory.GetEntry(ctx, part, inbox.KeySearch+q.id())
		if err != nil {
			t.Fatal(err)
		}
		e.Value = []byte("not sealed")
		st.SetEntry(*e)
	}
	if q.badMeta || q.rawTooLarge {
		item, err := st.Item(ctx, b.MailboxID, q.id())
		if err != nil {
			t.Fatal(err)
		}
		if q.badMeta {
			item.Meta = []byte("not sealed")
		}
		if q.rawTooLarge {
			item.Size = 1
		}
		if err := st.PutItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	return q.id()
}

// listing is where a page put a message.
type listing int8

const (
	listedNone listing = iota
	listedMatch
	listedNeedsModel
	listedUnknown
)

func (l listing) String() string {
	return [...]string{"none", "match", "needs a model", "unknown"}[l]
}

// listingOf is where the whole read lists m: needing a model only when no
// source is left to read and some answers to its atoms (each 1 or 0, every
// one tried, up to eight) make fs true (the code review of #103, M3).
func listingOf(fs []*query.Canonical, m *query.Message) listing {
	res := query.Eval(fs, m, nil, query.ForQuery)
	switch {
	case res.Value == query.True:
		return listedMatch
	case res.Value == query.False:
		return listedNone
	case res.Needs != 0 || len(res.Model) == 0:
		return listedUnknown
	case len(res.Model) > 8:
		return listedNeedsModel
	}
	for bits := range 1 << len(res.Model) {
		answers := query.Answers{}
		for i, a := range res.Model {
			answers[a] = 0
			if bits&(1<<i) != 0 {
				answers[a] = 1
			}
		}
		if query.Eval(fs, m, answers, query.ForQuery).Value == query.True {
			return listedNeedsModel
		}
	}
	return listedUnknown
}

// hasBody reports whether a body condition is in any formula.
func hasBody(fs []*query.Canonical) bool {
	for _, f := range fs {
		for _, g := range f.Formula.All {
			for _, c := range g.Any {
				if c.Var == contracts.VarBody {
					return true
				}
			}
		}
	}
	return false
}

// reference is the whole read of ADR 0027's Q27: every source the
// acquisition policy reads, read at once (the item and its meta; the entry
// when a body condition is in the formulas; the raw message when such a
// condition's entry was cut; never the raw message for a size condition
// alone), and evaluated once. It also returns whether it read the raw
// message, and the listing without the size that raw read gave.
func reference(b inbox.Box, fs []*query.Canonical, q *qMsg) (want, withoutRawSize listing, readRaw bool) {
	// A message a group of one received condition rules out is not
	// considered at all, before its meta is opened (ADR 0027, "What is
	// indexed, and what is scanned").
	for _, f := range fs {
		for _, g := range f.Formula.All {
			if len(g.Any) == 1 && g.Any[0].Var == contracts.VarReceived && !g.Any[0].Not {
				at := &query.Message{Have: query.FromItem | query.FromMeta, Received: q.at}
				if query.Eval([]*query.Canonical{formula(g.Any)}, at, nil, query.ForQuery).Value == query.False {
					return listedNone, listedNone, false
				}
			}
		}
	}
	if q.badMeta {
		return listedUnknown, listedUnknown, false
	}
	meta := q.meta()
	if meta.Confirmation {
		return listedNone, listedNone, false
	}
	item := &store.Message{MailboxID: b.MailboxID, ID: q.id(), Size: len(q.raw())}
	var entry *inbox.SearchEntry
	var text *inbox.RawText
	if hasBody(fs) && !meta.Unparsed {
		entry = q.entry()
		if q.noEntry || q.badEntry {
			entry = &inbox.SearchEntry{Cut: true}
		}
		if inbox.QueryMessage(item, &meta, entry, nil).BodyCut && !q.unreadable() {
			// A raw message past its bound cannot be read: the body stays
			// as the cut entry says.
			t, _, _ := inbox.MessageText(context.Background(), q.raw(), inbox.MessageTextMax)
			text, readRaw = &inbox.RawText{Text: t, Size: int64(len(q.raw()))}, true
		}
	}
	m := inbox.QueryMessage(item, &meta, entry, text)
	want = listingOf(fs, m)
	if m.SizeKnown && meta.Format < inbox.MetaFormat {
		m.SizeKnown, m.Size = false, 0
	}
	return want, listingOf(fs, m), readRaw
}

// pages runs the matcher from the newest message until a page has no next,
// and says where each message was listed. It fails on a message listed
// twice, on a next that does not go down, and on too many pages.
func pages(t testing.TB, mt inbox.Matcher, b inbox.Box, fs []*query.Canonical, limit func() int, maxPages int) (map[string]listing, []*contracts.MatchPage) {
	t.Helper()
	got := map[string]listing{}
	var all []*contracts.MatchPage
	before := ""
	for range maxPages {
		page, err := mt.Match(context.Background(), b, fs, before, limit())
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page)
		add := func(id string, l listing) {
			if prev, ok := got[id]; ok {
				t.Fatalf("%s listed twice: %v then %v", id, prev, l)
			}
			got[id] = l
		}
		for _, m := range page.Messages {
			add(m.ID, listedMatch)
		}
		for _, id := range page.NeedsModel {
			add(id, listedNeedsModel)
		}
		for _, id := range page.Unknown {
			add(id, listedUnknown)
		}
		if page.Next == "" {
			return got, all
		}
		if before != "" && page.Next >= before {
			t.Fatalf("next %s does not go below %s", page.Next, before)
		}
		before = page.Next
	}
	t.Fatalf("more than %d pages", maxPages)
	return nil, nil
}

// qGrants is the read budget taken one raw read at a time, as the adapter's
// implements it: it records what is held.
type qGrants struct {
	mu                            sync.Mutex
	free                          int64
	held, maxHeld, granted, given int
	refuse                        error
}

func (g *qGrants) Take(context.Context, int64) error { return nil }

func (g *qGrants) Grant(_ context.Context, n int64) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.refuse != nil {
		return nil, g.refuse
	}
	if g.free > 0 && n > g.free {
		return nil, contracts.ErrTooManyRequests
	}
	g.free -= n
	g.held++
	g.granted++
	g.maxHeld = max(g.maxHeld, g.held)
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.free += n
			g.held--
			g.given++
		})
	}, nil
}

func (g *qGrants) heldNow() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.held
}

// takeOnly is a read budget that is not a ReadGrants.
type takeOnly struct{}

func (takeOnly) Take(context.Context, int64) error { return nil }

var errQ = errors.New("the store failed")

func qBox(t testing.TB) inbox.Box { return newBox(t.(*testing.T), "mbx_qqqqqqqqqqqqqqqqqqqqqqqqqq") }

func ids(ms []contracts.MessageSummary) []string {
	out := []string{}
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}
