package receipts

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// statusOf is ReceiptsStatus as pull request 2 assembles it from the
// Auditor.
func statusOf(chained bool, incidents *attestation.ReceiptsIncidents, broken map[string]int64, pass *AuditPass) attestation.ReceiptsStatus {
	st := attestation.ReceiptsStatus{Sealed: true, Chained: chained, Chain: attestation.ReceiptsChainStrict, Incidents: incidents, Broken: broken}
	if pass != nil {
		st.Audit = &pass.Audit
	}
	return st
}

// L30 (F9): New keeps writing unchained receipts and says so; NewChained
// refuses what is not a chain; no chained Write goes without a link.
func TestL30ActivationNeedsAStrictChain(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	plain := New(w.mem, testKeys{secret: "root"}, w.clock.now, w.random.read)
	if plain.Chained() {
		t.Fatal("New is chained")
	}
	r := receipt()
	if err := plain.Write(ctx, account, r); err != nil {
		t.Fatal(err)
	}
	for _, e := range w.mem.AllEntries() {
		if strings.HasPrefix(e.Key, LinkPrefix) {
			t.Fatal("New linked a receipt")
		}
	}
	for what, call := range map[string]func() error{
		"link":      func() error { _, err := plain.Link(ctx, account, Ref{ID: r.ID}); return err },
		"head":      func() error { _, err := plain.Head(ctx, account); return err },
		"walk":      func() error { _, err := plain.Walk(ctx, account, Position{}, 1); return err },
		"export":    func() error { _, err := plain.Export(ctx, account, Position{}); return err },
		"audit":     func() error { _, err := plain.Audit(ctx); return err },
		"audited":   func() error { _, err := plain.Audited(ctx); return err },
		"incidents": func() error { _, err := plain.Incidents(ctx); return err },
	} {
		if err := call(); !errors.Is(err, errNotChained) {
			t.Errorf("%s on New: %v", what, err)
		}
	}
	if b := plain.Broken(); len(b) != len(attestation.ReceiptsBrokenKinds) {
		t.Fatalf("broken %v", b)
	}

	plainAnchored := &store.Anchored{Records: ChainTable{Entries: w.mem}, Commits: w.mc}
	notListing := &store.Anchored{Records: ChainTable{Entries: w.mem}, Commits: headOnly{w.mc}, Strict: true}
	for what, make := range map[string]func() (*Service, error){
		"nil":         func() (*Service, error) { return NewChained(w.mem, testKeys{}, nil, nil, w.accounts) },
		"plain":       func() (*Service, error) { return NewChained(w.mem, testKeys{}, nil, plainAnchored, w.accounts) },
		"no accounts": func() (*Service, error) { return NewChained(w.mem, testKeys{}, nil, w.anchored(), nil) },
		"no scans": func() (*Service, error) {
			return NewChained(entriesOnly{w.mem}, testKeys{}, nil, w.anchored(), w.accounts)
		},
		"no group list": func() (*Service, error) { return NewChained(w.mem, testKeys{}, nil, notListing, w.accounts) },
	} {
		if s, err := make(); err == nil || s != nil {
			t.Errorf("%s: %v", what, err)
		}
	}
	s := w.enclave()
	if !s.Chained() {
		t.Fatal("NewChained is not chained")
	}
	id := w.write(s, account)
	links := walkAll(t, s, account, 10)
	if shape(links) != "1:ok[ok]" || links[0].Link.Receipts[0].ID != id {
		t.Fatalf("%s", shape(links))
	}
	// A link of no account, or of something that is not a receipt, is
	// refused; so is a context that ended.
	if _, err := s.Link(ctx, "nobody", Ref{ID: id}); err == nil {
		t.Fatal("a link of no account")
	}
	if _, err := s.Link(ctx, account, Ref{ID: "rcp_x"}); err == nil {
		t.Fatal("a link of no receipt")
	}
	done, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Link(done, account, Ref{ID: id}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type headOnly struct{ m *store.MemoryCommits }

func (h headOnly) Head(ctx context.Context, name string) (*store.Commit, uint64, error) {
	return h.m.Head(ctx, name)
}

func (h headOnly) Append(ctx context.Context, c store.Commit) (*store.Commit, error) {
	return h.m.Append(ctx, c)
}

type entriesOnly struct{ store.Entries }

// L1: the chain's table keeps only the versions of the chain's records: a
// plain name is neither read nor written, a name of no record of the
// chain's neither, numbers up to 2^64-1 are kept in their order, and what
// is staged and not committed is deleted by the store.
func TestL1TheChainTableKeepsOnlyVersions(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	reads := 0
	mem.Interrupt = func(_ context.Context, step store.Step, _, _ string) error {
		if step == store.StepRead {
			reads++
		}
		return nil
	}
	table := ChainTable{Entries: mem}
	plain := LinkName(account, 1)
	if _, err := table.GetRecord(ctx, plain); !errors.Is(err, store.ErrNotFound) || reads != 0 {
		t.Fatalf("a plain name: %v, %d reads", err, reads)
	}
	for _, name := range []string{plain, "other@1", LinkName(account, 1) + "@"} {
		if err := table.PutRecord(ctx, &store.Record{Name: name, Value: []byte("v")}, true); !errors.Is(err, errNoKey) {
			t.Fatalf("%q written: %v", name, err)
		}
		if err := table.DeleteRecord(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := table.ListRecords(ctx, plain, 1); !errors.Is(err, errNotListed) {
		t.Fatal(err)
	}
	last := LinkName(account, math.MaxUint64) + "@00000000000000000001-0011223344556677"
	if err := table.PutRecord(ctx, &store.Record{Name: last, Value: []byte("a")}, true); err != nil {
		t.Fatal(err)
	}
	// The same bytes again succeed; other bytes replace a value only when
	// not creating.
	if err := table.PutRecord(ctx, &store.Record{Name: last, Value: []byte("a")}, false); err != nil {
		t.Fatal(err)
	}
	if err := table.PutRecord(ctx, &store.Record{Name: last, Value: []byte("b")}, true); !errors.Is(err, store.ErrExists) {
		t.Fatal(err)
	}
	if err := table.PutRecord(ctx, &store.Record{Name: last, Value: []byte("b")}, false); err != nil {
		t.Fatal(err)
	}
	got, err := table.GetRecord(ctx, last)
	if err != nil || string(got.Value) != "b" || got.Name != last {
		t.Fatalf("%+v %v", got, err)
	}
	if e := mem.AllEntries(); len(e) != 1 || e[0].Partition != Partition(account) || e[0].Key != "lnk_ffffffffffffffff@00000000000000000001-0011223344556677" {
		t.Fatalf("%+v", e)
	}
	if err := table.DeleteRecord(ctx, last); err != nil {
		t.Fatal(err)
	}
	if err := table.DeleteRecord(ctx, last); err != nil {
		t.Fatal("deleting what is not there")
	}
	mem.Interrupt = func(_ context.Context, step store.Step, _, _ string) error {
		if step == store.StepDeleteEntry || step == store.StepRead {
			return errBoom
		}
		return nil
	}
	if err := table.DeleteRecord(ctx, last); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	if err := table.PutRecord(ctx, &store.Record{Name: last, Value: []byte("c")}, true); err != nil {
		t.Fatal(err)
	}
	if err := table.PutRecord(ctx, &store.Record{Name: last, Value: []byte("d")}, false); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
}

// L2: the largest link, catalogue entry, pass and incident entry, sealed
// under the chain's key as the service seals them, fit one DynamoDB item.
func TestL2TheLargestOfEachSealsWithinOneItem(t *testing.T) {
	key := make([]byte, 32)
	for name, doc := range map[string]any{
		LinkName(account, math.MaxUint64): testLink(math.MaxUint64, LinkReceiptsMax),
		CatalogueName(math.MaxUint64):     testCatalogue(math.MaxUint64, CatalogueAccountsMax),
		IncidentName(math.MaxUint64):      testIncident(math.MaxUint64, IncidentFindingsMax),
		AuditName(math.MaxUint64):         testPass(),
	} {
		b := mustJSON(doc)
		sealed, err := envelope.SealText(key, b, envelope.AAD(ChainPurpose, name))
		if err != nil || len(b) > LinkBytesMax || len(sealed)+len(name)+100 > 400<<10 || len(sealed) != SealedBytes(len(b)) {
			t.Fatalf("%s: %d bytes, %d sealed: %v", name, len(b), len(sealed), err)
		}
	}
}
