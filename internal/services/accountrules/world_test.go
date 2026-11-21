package accountrules

import (
	"context"
	"crypto/sha256"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
)

// The world of the service's tests: one table (store.Memory) and one store
// of commits, enclaves over them each with its own strict Anchored, a clock
// the test moves, the receipts written, the account's mailboxes, and hooks
// on each enclave's reads and writes. Exported names are for the contract
// tests of package accountrules_test.

var start = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

const (
	accountA = "acc_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	accountB = "acc_bbbbbbbbbbbbbbbbbbbbbbbbbb"
	mailbox1 = "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	claude   = "https://claude.ai/oauth/claude-code-client-metadata"
	notice   = "2026-11-20"
)

// TestClock is the world's clock.
type TestClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *TestClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Set sets the clock to t: the contracts make their proofs with the real
// clock.
func (c *TestClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// Move moves the clock by d.
func (c *TestClock) Move(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type testKeys struct{ secret string }

func (k testKeys) Derive(_ context.Context, purpose string) ([]byte, error) {
	sum := sha256.Sum256([]byte(k.secret + "\x00" + purpose))
	return sum[:], nil
}

// ReceiptLog is the receipts written, in order.
type ReceiptLog struct {
	mu       sync.Mutex
	all      []written
	failNext bool
}

type written struct {
	account string
	r       receipts.Receipt
}

func (l *ReceiptLog) Write(_ context.Context, account string, r *receipts.Receipt) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failNext {
		l.failNext = false
		return errors.New("the receipt could not be written")
	}
	l.all = append(l.all, written{account, *r})
	return nil
}

// FailNext makes the next receipt fail to be written.
func (l *ReceiptLog) FailNext() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failNext = true
}

// Count is how many `rules` receipts account has.
func (l *ReceiptLog) Count(account string) int {
	return len(l.of(account))
}

func (l *ReceiptLog) of(account string) []receipts.Receipt {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []receipts.Receipt
	for _, w := range l.all {
		if w.account == account && w.r.Kind == receipts.KindRules {
			out = append(out, w.r)
		}
	}
	return out
}

type mailboxList struct {
	mu   sync.Mutex
	of   map[string][]string
	fail error
}

func (m *mailboxList) Catalogue(_ context.Context, account string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.of[account]), m.fail
}

// hooked is an enclave's store of records with hooks the test sets.
type hooked struct {
	Records
	mu sync.Mutex
	// lose makes the next write lose at its version, writing nothing.
	lose bool
	// beforeWrite runs before each write of name; failWrite fails it.
	beforeWrite func(name string)
	failWrite   func(name string) error
	// afterRead runs after each read of name, before it answers; failRead
	// fails it.
	afterRead func(name string)
	failRead  func(name string) error
}

func (h *hooked) hooks() (func(string), func(string) error, func(string), func(string) error, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	lose := h.lose
	h.lose = false
	return h.beforeWrite, h.failWrite, h.afterRead, h.failRead, lose
}

func (h *hooked) GetRecordAt(ctx context.Context, name string) (*store.Record, store.Mark, error) {
	h.mu.Lock()
	after, fail := h.afterRead, h.failRead
	h.mu.Unlock()
	if fail != nil {
		if err := fail(name); err != nil {
			return nil, store.Mark{}, err
		}
	}
	r, at, err := h.Records.GetRecordAt(ctx, name)
	if after != nil {
		after(name)
	}
	return r, at, err
}

func (h *hooked) write(name string, create bool, do func() error) error {
	before, fail, _, _, lose := h.hooks()
	if lose {
		if create {
			return store.ErrExists
		}
		return store.ErrChanged
	}
	if before != nil {
		before(name)
	}
	if fail != nil {
		if err := fail(name); err != nil {
			return err
		}
	}
	return do()
}

func (h *hooked) PutRecordAt(ctx context.Context, r *store.Record, at store.Mark) error {
	return h.write(r.Name, false, func() error { return h.Records.PutRecordAt(ctx, r, at) })
}

func (h *hooked) CreateNew(ctx context.Context, r *store.Record) error {
	return h.write(r.Name, true, func() error { return h.Records.CreateNew(ctx, r) })
}

// TestWorld is the world of the service's tests.
type TestWorld struct {
	t         *testing.T
	Mem       *store.Memory
	Commits   store.Commits
	Clock     *TestClock
	Receipts  *ReceiptLog
	mailboxes *mailboxList
	secret    string
	notice    string
}

// NewTestWorld is a world with nothing stored, its commits in memory.
func NewTestWorld(t *testing.T) *TestWorld {
	t.Helper()
	w := &TestWorld{
		t: t, Mem: store.NewMemory(), Commits: store.NewMemoryCommits(), Clock: &TestClock{t: start},
		Receipts: &ReceiptLog{}, mailboxes: &mailboxList{of: map[string][]string{accountA: {mailbox1}, accountB: {}}},
		secret: "root", notice: notice,
	}
	w.Mem.Now = w.Clock.now
	return w
}

// Enclave is one enclave of the deployment: its own memory of commits and
// its own cache, with the hooks of its store.
func (w *TestWorld) Enclave() *Service {
	s, _ := w.enclave()
	return s
}

func (w *TestWorld) enclave() (*Service, *hooked) {
	w.t.Helper()
	h := &hooked{Records: &store.Anchored{Records: w.Mem, Commits: w.Commits, Strict: true, Now: w.Clock.now}}
	s, err := New(Config{
		Records: h, Keys: testKeys{secret: w.secret}, Receipts: w.Receipts, Mailboxes: w.mailboxes,
		Notice: w.notice, Now: w.Clock.now,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return s, h
}

// LoseNext makes s's next write lose at its version after its receipt.
func LoseNext(s *Service) {
	h := s.cfg.Records.(*hooked)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lose = true
}

// Put writes rec as the account's record through s, at the version it
// reads, as a change through s would, so s's cache holds it at once.
func Put(t *testing.T, s *Service, account string, rec *rules.Record) {
	t.Helper()
	ctx := context.Background()
	cur, at, err := s.read(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	cp := *rec
	cp.V, cp.Account, cp.Epoch = rules.RecordFormat, account, s.epoch(account)
	if cur != nil {
		cp.Epoch = cur.Epoch
	}
	if cp.Rules == nil {
		cp.Rules = []rules.Stored{}
	}
	got, v, err := s.write(ctx, &cp, at, cur == nil)
	if err != nil {
		t.Fatal(err)
	}
	s.keep(account, cached{rec: got, epoch: got.Epoch, version: v, began: s.now()})
}

// Break forks the newest commit of the account's newest epoch, as two
// writers of the store of commits at one version would, and moves the clock
// past Fresh so no cache hides it.
func (w *TestWorld) Break(t *testing.T, account string) {
	t.Helper()
	m, ok := w.Commits.(*store.MemoryCommits)
	if !ok {
		t.Fatal("Break needs commits in memory")
	}
	for e := uint64(0); ; e++ {
		name := rules.RecordName(account, e)
		c, next, err := m.Head(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		if c == nil {
			t.Fatalf("account %s has no record to break", account)
		}
		if _, after, _ := m.Head(context.Background(), rules.RecordName(account, e+1)); after > 1 {
			continue
		}
		m.Fork(name, next-1, sha256.Sum256([]byte("another value")))
		break
	}
	w.Clock.Move(Fresh)
}

// fresh is the person of account with a fresh proof.
func (w *TestWorld) fresh(account string) Person {
	return Person{Account: account, ConfirmedUntil: w.Clock.now().Add(10 * time.Minute)}
}

// hold is a draft that holds mail whose subject contains word.
func hold(name, word string) contracts.RuleDraft {
	return contracts.RuleDraft{Name: name, On: true, Effect: contracts.EffectHold, Scope: contracts.RuleScope{To: contracts.ScopeAll},
		Criteria: []contracts.Formula{{V: 1, All: []contracts.Group{{Any: []contracts.Condition{
			{Var: contracts.VarSubject, Op: contracts.OpContains, Value: word},
		}}}}}}
}

// save checks d and saves it through s as a new rule (id "") or a change.
func save(t *testing.T, w *TestWorld, s *Service, account, id string, d contracts.RuleDraft) *contracts.Rule {
	t.Helper()
	r, err := trySave(w, s, account, id, d)
	if err != nil {
		t.Fatalf("save %q: %v", d.Name, err)
	}
	return r
}

func trySave(w *TestWorld, s *Service, account, id string, d contracts.RuleDraft) (*contracts.Rule, error) {
	ctx := context.Background()
	c, err := s.Check(ctx, account, d)
	if err != nil {
		return nil, err
	}
	l, err := s.List(ctx, account)
	if err != nil {
		return nil, err
	}
	notice := ""
	if c.Model {
		notice = w.notice
	}
	return s.Save(ctx, w.fresh(account), id, contracts.RuleSave{DraftID: c.DraftID, Draft: c.Draft, Version: l.Version, Notice: notice})
}

// toggle turns the default rule off and on through s, once.
func toggle(t *testing.T, w *TestWorld, s *Service, account string) {
	t.Helper()
	ctx := context.Background()
	l, err := s.List(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDefault(ctx, w.fresh(account), contracts.RuleDefaultSet{On: !l.Default.On, Version: l.Version}); err != nil {
		t.Fatal(err)
	}
}

// ids are the identifiers of the rules a connection of client applies.
func ids(a Applying) []string {
	var out []string
	for _, r := range a.Rules {
		out = append(out, r.ID)
	}
	return out
}

// storedOf are, by epoch, how many stored values of account's record whoever
// runs the table sees.
func (w *TestWorld) storedOf(account string) map[string]int {
	out := map[string]int{}
	for _, name := range w.Mem.RecordNames() {
		if rest, ok := strings.CutPrefix(name, "rules-"+account+"-"); ok {
			e, _, _ := strings.Cut(rest, "@")
			out[e]++
		}
	}
	return out
}
