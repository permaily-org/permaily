package accounts

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
)

// L16 of ADR 0037 through this service's real removal (the review of #170,
// ruling 13, and findings 1 and 2): the chain's values are entries of the
// account's receipts' partition, which the removal deletes, and again
// receipts.RemoveAgainAfter later; the audit's own writes for an account
// ask Removing, this service's, and are fenced by its answer.

// chainWorld is a closing world whose receipts are chained.
type chainWorld struct {
	*closing
	commits *pausing
	chain   *store.Anchored
	asking  *asking
	svc     *receipts.Service
}

// pausing is the store of commits of the chain, whose appends a test can
// hold or fail.
type pausing struct {
	*store.MemoryCommits
	before func(c store.Commit) error
}

func (p *pausing) Append(ctx context.Context, c store.Commit) (*store.Commit, error) {
	if p.before != nil {
		if err := p.before(c); err != nil {
			return nil, err
		}
	}
	return p.MemoryCommits.Append(ctx, c)
}

// asking is this service, counting what the chain asks of it.
type asking struct {
	*Service
	mu    sync.Mutex
	asked int
}

func (a *asking) Removing(ctx context.Context, id string) (bool, error) {
	a.mu.Lock()
	a.asked++
	a.mu.Unlock()
	return a.Service.Removing(ctx, id)
}

func (a *asking) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.asked
}

func newChainWorld(t *testing.T) *chainWorld {
	t.Helper()
	c := &chainWorld{closing: newClosing(t)}
	c.closing.commits.Group = receipts.CommitGroup
	c.commits = &pausing{MemoryCommits: c.closing.commits}
	c.chain = &store.Anchored{Records: receipts.ChainTable{Entries: c.mem}, Commits: c.commits, Strict: true, Now: c.clock}
	c.asking = &asking{Service: c.closing.svc}
	svc, err := receipts.NewChained(c.mem, c.closing.svc.cfg.Keys, c.clock, c.chain, c.asking)
	if err != nil {
		t.Fatal(err)
	}
	c.svc = svc
	return c
}

func (c *chainWorld) clock() time.Time { return c.now }

func chainReceipt() *receipts.Receipt {
	return &receipts.Receipt{Kind: receipts.KindEnded, ClientID: "https://client.example/meta", Vendor: "client.example", Resource: contracts.ResourceMCP, Ended: &receipts.Ended{Reason: contracts.EndedByReplaced}}
}

// unchained writes n receipts of account as a release before the chain
// did.
func (c *chainWorld) unchained(account string, n int) {
	c.t.Helper()
	s := receipts.New(c.mem, c.closing.svc.cfg.Keys, c.clock, nil)
	for range n {
		if err := s.Write(context.Background(), account, chainReceipt()); err != nil {
			c.t.Fatal(err)
		}
	}
}

// left is what is stored in account's receipts' partition.
func (c *chainWorld) left(account string) []string {
	var keys []string
	for _, e := range c.mem.AllEntries() {
		if e.Partition == receipts.Partition(account) {
			keys = append(keys, e.Key)
		}
	}
	return keys
}

// removeAll removes the accounts that are due, as the enclave's pass does.
func (c *chainWorld) removeAll() {
	c.t.Helper()
	if err := c.closing.svc.RemoveDue(context.Background()); err != nil {
		c.t.Fatal(err)
	}
}

// L16: Ana's chain, adopted from 1,001 receipts written before it, with a
// write whose link's staging failed, one whose link's commit is held when
// the removal begins and one queued behind it: the removal's first sweep
// and its second, RemoveAgainAfter later, leave nothing of her receipts or
// links in the table; their commits and her entry in the catalogue stay;
// the audit counts her removed, never broken; her old values put back
// start no write.
func TestL16TheRealRemovalTakesTheChain(t *testing.T) {
	ctx := context.Background()
	c := newChainWorld(t)
	ana := c.ana.Account.ID
	c.unchained(ana, 1001)
	c.now = c.now.Add(2 * receipts.AdoptAfter)
	if err := c.svc.Write(ctx, ana, chainReceipt()); err != nil {
		t.Fatal(err)
	}
	head, err := c.svc.Head(ctx, ana)
	if err != nil || head.N != 17 {
		t.Fatalf("%+v %v", head, err)
	}
	if n := len(c.left(ana)); n <= 1000 {
		t.Fatalf("%d entries", n)
	}
	// A write whose link's value is not staged.
	c.mem.Interrupt = func(_ context.Context, step store.Step, partition, key string) error {
		if step == store.StepEntry && partition == receipts.Partition(ana) && strings.HasPrefix(key, receipts.LinkKeyPrefix(18)) {
			return errors.New("down")
		}
		return nil
	}
	if err := c.svc.Write(ctx, ana, chainReceipt()); err == nil {
		t.Fatal("a write whose link was not staged succeeded")
	}
	c.mem.Interrupt = nil
	var copied []store.Entry
	for _, e := range c.mem.AllEntries() {
		if e.Partition == receipts.Partition(ana) {
			copied = append(copied, e)
		}
	}
	// A write whose link's commit is held, and one queued behind it.
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	c.commits.before = func(cm store.Commit) error {
		if cm.Name == receipts.LinkName(ana, 18) {
			once.Do(func() { close(held) })
			<-release
		}
		return nil
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Go(func() { errs[0] = c.svc.Write(ctx, ana, chainReceipt()) })
	<-held
	c.now = c.day
	wg.Go(func() { errs[1] = c.svc.Write(ctx, ana, chainReceipt()) })
	c.removeAll()
	close(release)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatal(errs)
	}
	if len(c.left(ana)) == 0 {
		t.Fatal("the writes under way stored nothing after the first sweep")
	}
	c.commits.before = nil
	c.now = c.day.Add(receipts.RemoveAgainAfter)
	c.removeAll()
	if left := c.left(ana); len(left) != 0 {
		t.Fatalf("left after both sweeps: %d", len(left))
	}
	// The commits stay; the catalogue names her; the audit counts her
	// removed.
	for _, n := range []uint64{1, 17, 19} {
		if h, _, err := c.commits.Head(ctx, receipts.LinkName(ana, n)); err != nil || h == nil {
			t.Fatalf("link %d's commit: %v", n, err)
		}
	}
	c.now = c.now.Add(time.Minute)
	pass, err := c.svc.Audit(ctx)
	if err != nil || pass == nil {
		t.Fatal(err)
	}
	if pass.Audit.Removed != 1 || pass.Audit.Chains != 1 || pass.Audit.Recovered != 0 {
		t.Fatalf("%+v", pass.Audit)
	}
	for k, n := range pass.Audit.Broken {
		if n != 0 {
			t.Fatalf("%s: %d", k, n)
		}
	}
	if got, err := c.svc.Incidents(ctx); err != nil || got.Recorded != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	// Her old values put back after both sweeps start no write.
	for _, e := range copied {
		c.mem.SetEntry(e)
	}
	c.now = c.now.Add(receipts.AuditEvery)
	if _, err := c.svc.Audit(ctx); err != nil {
		t.Fatal(err)
	}
	if h, err := c.svc.Head(ctx, ana); err != nil || h.N != 19 {
		t.Fatalf("%+v %v", h, err)
	}
}

// L16, R3 and the review of #170's finding 1: a pass that asked Removing
// and was answered live, paused before the store began its write (a
// recovered link, an adoption, and link 1 of an account the catalogue
// names), resumed after the real removal's first sweep, stores what it
// writes before the second sweep, which takes it; resumed after both, it
// asks again and stores nothing. A record forked fails the pass, which
// writes nothing.
func TestL16TheAuditsFenceAgainstTheRealRemoval(t *testing.T) {
	ctx := context.Background()
	setups := map[string]func(c *chainWorld, ana string){
		// A receipt left without a link a day ago.
		"recovery": func(c *chainWorld, ana string) {
			if err := c.svc.Write(ctx, ana, chainReceipt()); err != nil {
				t.Fatal(err)
			}
			c.unchained(ana, 1)
		},
		// Receipts from before the chain, and an account the list names.
		"adoption": func(c *chainWorld, ana string) {
			c.unchained(ana, 3)
		},
		// Entered in the catalogue, its link 1 never made.
		"link 1": func(c *chainWorld, ana string) {
			c.commits.before = func(cm store.Commit) error {
				if cm.Name == receipts.LinkName(ana, 1) {
					return errors.New("down")
				}
				return nil
			}
			if err := c.svc.Write(ctx, ana, chainReceipt()); err == nil {
				t.Fatal("link 1 was made")
			}
			c.commits.before = nil
		},
	}
	for name, setup := range setups {
		for _, both := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				c := newChainWorld(t)
				ana := c.ana.Account.ID
				setup(c, ana)
				before, err := c.svc.Head(ctx, ana)
				if err != nil {
					t.Fatal(err)
				}
				// The pass runs half a minute before her day.
				c.now = c.day.Add(-30 * time.Second)
				paused := false
				c.chain.Now = func() time.Time {
					if c.asking.count() > 0 && !paused {
						paused = true
						c.now = c.day
						c.removeAll()
						if both {
							c.now = c.day.Add(receipts.RemoveAgainAfter)
							c.removeAll()
						}
					}
					return c.now
				}
				if _, err := c.svc.Audit(ctx); err != nil {
					t.Fatal(err)
				}
				c.chain.Now = c.clock
				if !paused {
					t.Fatal("the pause was not reached")
				}
				if both {
					if left := c.left(ana); len(left) != 0 || c.asking.count() < 2 {
						t.Fatalf("stored after both sweeps: %q, asked %d", left, c.asking.count())
					}
					return
				}
				// Resumed within its minute: what it stored goes with the
				// second sweep. An adoption resumed after the first sweep
				// finds nothing left to adopt, and makes no link.
				h, err := c.svc.Head(ctx, ana)
				if err != nil || (name != "adoption" && h.N <= before.N) {
					t.Fatalf("%+v %v: the audit wrote nothing", h, err)
				}
				c.now = c.day.Add(receipts.RemoveAgainAfter)
				c.removeAll()
				if left := c.left(ana); len(left) != 0 {
					t.Fatalf("left after both sweeps: %q", left)
				}
			})
		}
	}

	// Her record forked: Removing fails, and so does the pass, writing
	// nothing for her.
	c := newChainWorld(t)
	ana := c.ana.Account.ID
	setups["recovery"](c, ana)
	c.now = c.day.Add(-30 * time.Second)
	rec, _, err := c.closing.commits.Head(ctx, recordName(ana))
	if err != nil {
		t.Fatal(err)
	}
	c.closing.commits.Fork(recordName(ana), rec.Version, sha256.Sum256([]byte("another value")))
	if _, err := c.svc.Audit(ctx); !errors.Is(err, store.ErrForked) {
		t.Fatal(err)
	}
	if h, err := c.svc.Head(ctx, ana); err != nil || h.N != 1 {
		t.Fatalf("%+v %v", h, err)
	}
}
