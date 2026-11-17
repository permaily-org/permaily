package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// The composition of the chained receipts (ADR 0037, pull request 2): what
// it refuses, the audit's lease and schedule, /status's receipts as the
// image reports them, the chain's regressions under their fixed name, and
// logs of kinds alone.

const receiptsAccount = "acc_bbbbbbbbbbbbbbbbbbbbbbbbbb"

// receiptsAccounts is what the chain asks of accounts.
type receiptsAccounts struct {
	mu      sync.Mutex
	removed map[string]bool
	list    []string
}

func (a *receiptsAccounts) Removing(_ context.Context, account string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.removed[account], nil
}

func (a *receiptsAccounts) AccountRecords(_ context.Context, after string, limit int) (*contracts.AccountList, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := &contracts.AccountList{Accounts: []contracts.AccountRecord{}}
	for _, id := range a.list {
		if id > after && len(out.Accounts) < limit {
			out.Accounts = append(out.Accounts, contracts.AccountRecord{ID: id})
		}
	}
	return out, nil
}

// failingCommits are the deployment's commits, failing every request while
// fail is set.
type failingCommits struct {
	*store.MemoryCommits
	fail *atomic.Bool
}

var errCommits = errors.New("the anchors bucket is failing")

func (c failingCommits) Unwrap() store.Commits { return c.MemoryCommits }

func (c failingCommits) Head(ctx context.Context, name string) (*store.Commit, uint64, error) {
	if c.fail.Load() {
		return nil, 0, errCommits
	}
	return c.MemoryCommits.Head(ctx, name)
}

func (c failingCommits) Append(ctx context.Context, cm store.Commit) (*store.Commit, error) {
	if c.fail.Load() {
		return nil, errCommits
	}
	return c.MemoryCommits.Append(ctx, cm)
}

func (c failingCommits) History(ctx context.Context, name string) (*store.History, error) {
	if c.fail.Load() {
		return nil, errCommits
	}
	return c.MemoryCommits.History(ctx, name)
}

func (c failingCommits) HistoryRange(ctx context.Context, from, to string, maxListed int, each func(store.Member) error) error {
	if c.fail.Load() {
		return errCommits
	}
	return c.MemoryCommits.HistoryRange(ctx, from, to, maxListed, each)
}

// receiptsWorld is the deployment's table and commits, and enclaves'
// chained receipts composed over them as the image composes them.
type receiptsWorld struct {
	*modelWorld
	accounts *receiptsAccounts
	fail     atomic.Bool
}

func newReceiptsWorld(t *testing.T) *receiptsWorld {
	t.Helper()
	w := &receiptsWorld{modelWorld: newModelWorld(t), accounts: &receiptsAccounts{removed: map[string]bool{}}}
	w.commits.Group = receipts.CommitGroup
	return w
}

// chain is one enclave's strict Anchored of the chain's records.
func (w *receiptsWorld) chain() *store.Anchored {
	return &store.Anchored{Records: receipts.ChainTable{Entries: w.mem}, Commits: failingCommits{w.commits, &w.fail}, Strict: true, Now: w.now}
}

// enclave is one enclave's receipts, composed; a fresh one is an enclave
// restarted.
func (w *receiptsWorld) enclave() *composedReceipts {
	w.t.Helper()
	r, err := composeReceipts(receiptsParts{chain: w.chain(), entries: w.mem, keys: w.keys, accounts: w.accounts, now: w.now})
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

// write writes a receipt of the account through r, linked before it
// returns.
func (w *receiptsWorld) write(r *composedReceipts) {
	w.t.Helper()
	if err := r.svc.Write(context.Background(), receiptsAccount, &receipts.Receipt{
		Kind: receipts.KindEnded, ClientID: "https://client.example/meta", Vendor: "client.example",
		Resource: contracts.ResourceMCP, Ended: &receipts.Ended{Reason: contracts.EndedByReplaced},
	}); err != nil {
		w.t.Fatal(err)
	}
}

// receiptEntries are the account's receipts as stored.
func (w *receiptsWorld) receiptEntries() []store.Entry {
	w.t.Helper()
	es, err := w.mem.ListEntries(context.Background(), receipts.Partition(receiptsAccount), "rcp_", "", 100)
	if err != nil {
		w.t.Fatal(err)
	}
	return es
}

// L30 (F9) in the composition: the receipts are composed only chained, over
// one strict Anchored of the chain's table whose commits group the chain's
// names as receipts.CommitGroup does; with accounts to ask; and the
// connections are composed only with them.
func TestL30TheCompositionChainsTheReceipts(t *testing.T) {
	w := newReceiptsWorld(t)
	plainCommits := store.NewMemoryCommits()
	otherGroup := store.NewMemoryCommits()
	otherGroup.Group = func(name string) (string, string, bool) { return "all", name, true }
	for name, chain := range map[string]*store.Anchored{
		"none":               nil,
		"not strict":         {Records: receipts.ChainTable{Entries: w.mem}, Commits: w.commits},
		"the table's own":    {Records: w.mem, Commits: w.commits, Strict: true},
		"commits not listed": {Records: receipts.ChainTable{Entries: w.mem}, Commits: headOnly{w.commits}, Strict: true},
		"commits ungrouped":  {Records: receipts.ChainTable{Entries: w.mem}, Commits: plainCommits, Strict: true},
		"grouped otherwise":  {Records: receipts.ChainTable{Entries: w.mem}, Commits: otherGroup, Strict: true},
	} {
		if _, err := composeReceipts(receiptsParts{chain: chain, entries: w.mem, keys: w.keys, accounts: w.accounts}); !errors.Is(err, errReceiptsChain) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := composeReceipts(receiptsParts{chain: w.chain(), entries: w.mem, keys: w.keys}); err == nil {
		t.Error("composed without accounts")
	}
	r := w.enclave()
	if !r.svc.Chained() {
		t.Fatal("the composed receipts are not chained")
	}
	// The commits of the anchors bucket group the chain's names, and no
	// other (anchorCommits; the dev build's devStorage the same).
	if !chainGrouped(&store.S3Commits{Group: receipts.CommitGroup}) || chainGrouped(&store.S3Commits{}) || chainGrouped(headOnly{w.commits}) {
		t.Fatal("chainGrouped")
	}
}

// headOnly is commits that cannot list a group.
type headOnly struct{ c *store.MemoryCommits }

func (h headOnly) Head(ctx context.Context, name string) (*store.Commit, uint64, error) {
	return h.c.Head(ctx, name)
}

func (h headOnly) Append(ctx context.Context, c store.Commit) (*store.Commit, error) {
	return h.c.Append(ctx, c)
}

// L18 and L19 in the composition (ADR 0037, "The audit"): the audit runs
// under a lease one enclave holds at a time, only when a pass is due (a
// day after the newest began); a lease that ended, that does not open
// (forged or garbled) or that was erased is taken over; one this enclave
// holds is released when its run ends, whatever it found.
func TestTheAuditRunsUnderItsLease(t *testing.T) {
	w := newReceiptsWorld(t)
	a, b := w.enclave(), w.enclave()
	ctx := context.Background()
	lease, err := b.takeLease(ctx)
	if err != nil || lease == nil {
		t.Fatalf("b's lease: %v", err)
	}
	if got, err := a.takeLease(ctx); err != nil || got != nil {
		t.Fatalf("a took b's lease: %v", err)
	}
	// While b holds it, a runs no pass.
	if err := a.auditOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if p, err := a.svc.Audited(ctx); err != nil || p != nil {
		t.Fatalf("a pass under b's lease: %+v %v", p, err)
	}
	// Once it ended, a takes it over and runs the pass, then releases it.
	w.advance(auditLease + time.Second)
	if err := a.auditOnce(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := a.svc.Audited(ctx)
	if err != nil || first == nil || first.P != 1 {
		t.Fatalf("a's pass: %+v %v", first, err)
	}
	if _, err := w.mem.GetEntry(ctx, auditLeasePartition, auditLeaseKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the lease was not released: %v", err)
	}
	// Not due again within a day, through either enclave.
	w.advance(receipts.AuditEvery - time.Minute)
	for _, e := range []*composedReceipts{a, b} {
		if err := e.auditOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if p, _ := a.svc.Audited(ctx); p.P != 1 {
		t.Fatalf("a pass before it was due: %d", p.P)
	}
	// A lease that does not open is taken over.
	w.advance(2 * time.Minute)
	if err := w.mem.PutEntry(ctx, &store.Entry{Partition: auditLeasePartition, Key: auditLeaseKey, Value: []byte(`{"holder":"someone","until":"2999-01-01T00:00:00Z"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := b.auditOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if p, _ := b.svc.Audited(ctx); p.P != 2 {
		t.Fatalf("no pass over a lease that does not open: %d", p.P)
	}
	// A lease an enclave sealed for longer than a lease lasts is not one.
	if l, err := a.takeLease(ctx); err != nil || l == nil {
		t.Fatal(err)
	}
	e, _ := w.mem.GetEntry(ctx, auditLeasePartition, auditLeaseKey)
	key, _ := w.keys.Derive(ctx, auditLeasePurpose)
	raw, _ := json.Marshal(leaseDoc{Holder: "someone", Until: w.now().Add(100 * auditLease)})
	if e.Value, err = sealForTest(key, raw); err != nil {
		t.Fatal(err)
	}
	if err := w.mem.PutEntry(ctx, e); err != nil {
		t.Fatal(err)
	}
	if l, err := b.takeLease(ctx); err != nil || l == nil {
		t.Fatalf("a lease far ahead held: %v", err)
	}
	// Two enclaves taking an erased lease at once: one of them.
	if err := w.mem.DeleteEntry(ctx, auditLeasePartition, auditLeaseKey); err != nil {
		t.Fatal(err)
	}
	var won atomic.Int32
	var wg sync.WaitGroup
	for _, e := range []*composedReceipts{a, b, w.enclave()} {
		wg.Go(func() {
			if l, err := e.takeLease(ctx); err == nil && l != nil {
				won.Add(1)
			}
		})
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("%d enclaves took one lease", won.Load())
	}
}

// L19 (ADR 0037, "What /status and permaily-verify show"): /status's
// receipts as the image reports them, and what permaily-verify's check
// says of them. An enclave that has read nothing yet reports no record of
// incidents and no pass, which fail; once it read both after a pass, they
// pass; read again only every ten minutes, so a read that stops leaves
// them growing old until they fail. A receipt deleted is found by the
// next pass, recorded before it is reported, and fails the check; put
// back, a clean pass after it leaves the record of incidents as it is, so
// the check keeps failing (R1), through a restarted enclave too.
func TestL19TheReceiptsOnStatus(t *testing.T) {
	w := newReceiptsWorld(t)
	ctx := context.Background()
	r := w.enclave()
	check := func(r *composedReceipts) error {
		st := r.status()
		return st.Check(w.now())
	}
	st := r.status()
	if !st.Sealed || !st.Chained || st.Chain != attestation.ReceiptsChainStrict || st.Incidents != nil || st.Audit != nil || len(st.Broken) != len(attestation.ReceiptsBrokenKinds) {
		t.Fatalf("an enclave that read nothing yet: %+v", st)
	}
	if err := st.Check(w.now()); err == nil || !strings.Contains(err.Error(), "could not be read") {
		t.Fatalf("an enclave that read nothing yet: %v", err)
	}
	// The record read, no pass yet: an image with no audit is not clean.
	if err := r.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if st := r.status(); st.Incidents == nil || st.Check(w.now()) == nil || !strings.Contains(st.Check(w.now()).Error(), "no pass") {
		t.Fatalf("no pass: %+v", st)
	}
	w.write(r)
	if err := r.auditOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if st := r.status(); (&st).Check(w.now()) != nil || st.Audit.Chains != 1 || st.Audit.Catalogue != 1 {
		t.Fatalf("after a clean pass: %+v %v", st.Audit, st.Check(w.now()))
	}
	// Nothing read for 31 minutes: the record is too old.
	w.advance(31 * time.Minute)
	if err := check(r); err == nil || !strings.Contains(err.Error(), "was not read") {
		t.Fatalf("an old record: %v", err)
	}
	// Read again, and the pass two days old.
	w.advance(48 * time.Hour)
	w.fail.Store(true)
	if err := r.refresh(ctx); err == nil {
		t.Fatal("a read of a failing bucket")
	}
	w.fail.Store(false)
	if err := r.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := check(r); err == nil || !strings.Contains(err.Error(), "more than 48h0m0s old") {
		t.Fatalf("an old pass: %v", err)
	}
	// A receipt deleted from the table: the next pass finds it, records
	// it, and the check fails on the record and on the pass.
	gone := w.receiptEntries()[0]
	if err := w.mem.DeleteEntry(ctx, gone.Partition, gone.Key); err != nil {
		t.Fatal(err)
	}
	if err := r.auditOnce(ctx); err != nil {
		t.Fatal(err)
	}
	st = r.status()
	if st.Incidents.Recorded != 1 || st.Audit.Broken["receipt_missing"] != 1 || st.Broken["receipt_missing"] != 0 {
		t.Fatalf("after a deletion: %+v %+v", st.Incidents, st.Audit)
	}
	if err := st.Check(w.now()); err == nil || !strings.Contains(err.Error(), "holds 1 entries") {
		t.Fatalf("after a deletion: %v", err)
	}
	// Put back, a clean pass a day later, through a restarted enclave: the
	// record still holds it.
	gone.Version = 0
	if err := w.mem.PutEntry(ctx, &gone); err != nil {
		t.Fatal(err)
	}
	w.advance(receipts.AuditEvery)
	r = w.enclave()
	if err := r.auditOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	st = r.status()
	if st.Audit.Broken["receipt_missing"] != 0 || st.Incidents.Recorded != 1 {
		t.Fatalf("after the clean pass: %+v %+v", st.Incidents, st.Audit)
	}
	if err := st.Check(w.now()); err == nil || !strings.Contains(err.Error(), "holds 1 entries") {
		t.Fatalf("a clean pass took the incident back: %v", err)
	}
	// What /status says names no account.
	b, _ := json.Marshal(st)
	if strings.Contains(string(b), "acc_") || strings.Contains(string(b), "receipt-") {
		t.Fatalf("/status's receipts: %s", b)
	}
}

// The chain's regressions are reported under the fixed name "receipt link"
// alone: never a record's name, which holds its account, nor its version
// (ADR 0037, "When the link before cannot be read"; the review of #170,
// ruling 11); and permaily-verify fails on them as on any regression.
func TestTheChainsRegressionsNameNoAccount(t *testing.T) {
	chain := &store.Anchored{}
	if got := receiptRegressions(chain); got != nil {
		t.Fatalf("%+v", got)
	}
	if got := receiptRegressions(nil); got != nil {
		t.Fatalf("%+v", got)
	}
	w := newReceiptsWorld(t)
	r := w.enclave()
	w.write(r)
	w.write(r)
	// Link 1 forked, read through a fresh enclave's walk.
	w.commits.Fork(receipts.LinkName(receiptsAccount, 1), 1, [32]byte{1})
	fresh := w.enclave()
	if _, err := fresh.svc.Walk(context.Background(), receiptsAccount, receipts.Position{}, 10); err != nil {
		t.Fatal(err)
	}
	got := receiptRegressions(fresh.chain)
	if len(got) == 0 || !got[0].Fork {
		t.Fatalf("a forked link is no regression: %+v", got)
	}
	for _, g := range got {
		if g.Name != receiptRecord || g.Had != 0 || g.Found != 0 {
			t.Fatalf("a regression of the chain: %+v", g)
		}
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "acc_") || strings.Contains(string(b), "receipt-link") {
		t.Fatalf("%s", b)
	}
}

// sealForTest seals a lease as an enclave does.
func sealForTest(key, raw []byte) ([]byte, error) { return envelope.SealText(key, raw, leaseAAD()) }

// L20 in the composition: what runReceipts logs is its kind alone, never
// an account, a record's name, a number, a digest or a count; and it reads
// the record of incidents and runs the audit once as it starts and once
// on each tick, until the enclave stops.
func TestL20TheReceiptsLogKindsAlone(t *testing.T) {
	w := newReceiptsWorld(t)
	r := w.enclave()
	w.write(r)
	w.fail.Store(true)
	log := &lockedLog{}
	ctx, cancel := context.WithCancel(context.Background())
	audit, incidents := make(chan time.Time), make(chan time.Time)
	runReceipts(ctx, r, slog.New(slog.NewJSONHandler(log, nil)), receiptsTicks{audit: audit, incidents: incidents})
	lines := func(n int) {
		t.Helper()
		for i := 0; strings.Count(log.String(), "\n") < n; i++ {
			if i == 1000 {
				t.Fatalf("%d lines, want %d:\n%s", strings.Count(log.String(), "\n"), n, log.String())
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	lines(2)
	audit <- time.Now()
	incidents <- time.Now()
	lines(4)
	cancel()
	out := log.String()
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var l map[string]any
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			t.Fatal(err)
		}
		if k := l["kind"]; k != "audit" && k != "incidents" {
			t.Fatalf("a line of no kind: %s", line)
		}
		delete(l, "time")
		delete(l, "level")
		delete(l, "msg")
		delete(l, "kind")
		if len(l) != 0 {
			t.Fatalf("a line with more than its kind: %s", line)
		}
	}
	for _, never := range []string{"acc_", "receipt-", "anchors bucket", "lnk_", "rcp_"} {
		if strings.Contains(out, never) {
			t.Fatalf("the log holds %q:\n%s", never, out)
		}
	}
	// Once the storage answers, the next tick reads the record and runs
	// the pass.
	w.fail.Store(false)
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	runReceipts(ctx, r, slog.New(slog.DiscardHandler), receiptsTicks{audit: audit, incidents: incidents})
	for i := 0; r.status().Audit == nil; i++ {
		if i == 1000 {
			t.Fatal("no pass")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if st := r.status(); st.Check(w.now()) != nil {
		t.Fatal(st.Check(w.now()))
	}
}
