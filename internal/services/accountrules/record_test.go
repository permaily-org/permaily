package accountrules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/store"
)

// U2: the account's record of rules (ADR 0040, "The rules' record").

// unavailable fails unless account's connections are given nothing through
// s: an error wrapping ErrUnavailable, never the default alone.
func unavailable(t *testing.T, s *Service, account string) {
	t.Helper()
	a, err := s.Applying(context.Background(), account, claude)
	if !errors.Is(err, ErrUnavailable) || a.Rules != nil {
		t.Fatalf("a record that cannot be used gave %v, %v", ids(a), err)
	}
}

func TestU2TheRecordIsNamedPerAccountAndEpochSealedAndBound(t *testing.T) {
	w := NewTestWorld(t)
	s := w.Enclave()
	save(t, w, s, accountA, "", hold("Bank statements, private", "statement"))
	names := w.storedOf(accountA)
	if len(names) != 1 || names["0"] != 1 {
		t.Fatalf("the record is stored as %v", names)
	}
	for _, name := range w.Mem.RecordNames() {
		r, err := w.Mem.GetRecord(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(name, rules.RecordName(accountA, 0)+"@") || r.Plain {
			t.Fatalf("stored as %s, plain %v", name, r.Plain)
		}
		for _, secret := range []string{"Bank statements", "statement", "hold", "rul_"} {
			if bytes.Contains(r.Value, []byte(secret)) {
				t.Fatalf("whoever runs the table reads %q", secret)
			}
		}
	}
	// Another deployment's key opens nothing.
	other := *w
	other.secret = "another root"
	unavailable(t, other.Enclave(), accountA)
	if a, err := s.Applying(context.Background(), accountA, claude); err != nil || len(a.Rules) != 2 {
		t.Fatalf("the deployment's own: %v, %v", ids(a), err)
	}
}

// A record sealed for one name never opens under another (another account,
// another epoch), and a changed byte never opens, even committed: each is
// answered as a record that cannot be used.
func TestU2ARecordOpensOnlyUnderItsOwnNameUnchanged(t *testing.T) {
	ctx := context.Background()
	w := NewTestWorld(t)
	s, h := w.enclave()
	save(t, w, s, accountA, "", hold("a", "statement"))
	rec, _, err := h.GetRecordAt(ctx, rules.RecordName(accountA, 0))
	if err != nil {
		t.Fatal(err)
	}
	commit := func(name string, value []byte) {
		t.Helper()
		if err := h.Records.CreateNew(ctx, &store.Record{Name: name, Value: value}); err != nil {
			t.Fatal(err)
		}
	}
	commit(rules.RecordName(accountB, 0), rec.Value)
	unavailable(t, s, accountB)

	changed := bytes.Clone(rec.Value)
	changed[len(changed)/2] ^= 1
	commit(rules.RecordName("acc_cccccccccccccccccccccccccc", 0), changed)
	unavailable(t, s, "acc_cccccccccccccccccccccccccc")

	// Sealed by this deployment for this name, and not this account's
	// document, another epoch's or another format's.
	for i, doc := range []rules.Record{
		{V: rules.RecordFormat, Account: accountA, Epoch: 0},
		{V: rules.RecordFormat, Account: "acc_dddddddddddddddddddddddddd", Epoch: 3},
		{V: 9, Account: "acc_dddddddddddddddddddddddddd", Epoch: 0},
	} {
		account := "acc_ddddddddddddddddddddddddd" + strconv.Itoa(i)
		doc := doc
		if doc.Account != accountA {
			doc.Account = account
		}
		raw, _ := json.Marshal(doc)
		name := rules.RecordName(account, 0)
		value, err := envelope.SealText(s.key, raw, aad(name))
		if err != nil {
			t.Fatal(err)
		}
		commit(name, value)
		unavailable(t, s, account)
	}
	// A plain value is not one this service writes.
	if err := h.Records.CreateNew(ctx, &store.Record{Name: rules.RecordName("acc_eeeeeeeeeeeeeeeeeeeeeeeeee", 0), Value: []byte(`{}`), Plain: true}); err != nil {
		t.Fatal(err)
	}
	unavailable(t, s, "acc_eeeeeeeeeeeeeeeeeeeeeeeeee")
	// Past its bound, a document is not one this service writes either.
	big, _ := json.Marshal(rules.Record{V: rules.RecordFormat, Account: "acc_ffffffffffffffffffffffffff", Rules: []rules.Stored{{ID: strings.Repeat("x", rules.RecordBytesMax)}}})
	name := rules.RecordName("acc_ffffffffffffffffffffffffff", 0)
	value, _ := envelope.SealText(s.key, big, aad(name))
	commit(name, value)
	unavailable(t, s, "acc_ffffffffffffffffffffffffff")
	if s.Unreadable() < 6 {
		t.Fatalf("%d requests counted unreadable", s.Unreadable())
	}
}

// Whoever writes the table can delete the newest value, or put an earlier
// one back: a record lost is never answered from an older copy, nor as the
// default alone.
func TestU2ALostRecordAndAnEarlierCopyGiveNothing(t *testing.T) {
	ctx := context.Background()
	w := NewTestWorld(t)
	s := w.Enclave()
	first := save(t, w, s, accountA, "", hold("a", "statement"))
	save(t, w, s, accountA, "", hold("b", "invoice"))
	var versions []string
	for _, n := range w.Mem.RecordNames() {
		if strings.HasPrefix(n, rules.RecordName(accountA, 0)+"@") {
			versions = append(versions, n)
		}
	}
	if len(versions) != 2 {
		t.Fatalf("versions stored: %v", versions)
	}
	older, err := w.Mem.GetRecord(ctx, versions[0])
	if err != nil {
		t.Fatal(err)
	}
	// The earlier value put back under the newest version's name.
	w.Mem.SetRecord(store.Record{Name: versions[1], Value: older.Value})
	w.Clock.Move(Fresh)
	unavailable(t, s, accountA)
	unavailable(t, w.Enclave(), accountA)
	// The newest value deleted.
	if err := w.Mem.DeleteRecord(ctx, versions[1]); err != nil {
		t.Fatal(err)
	}
	unavailable(t, s, accountA)
	unavailable(t, w.Enclave(), accountA)
	_ = first
}

// A rolled-back history: the newest commit gone, which the store of commits
// lets nobody do (Object Lock; the S3 test shows a delete marker rolls
// nothing back after a restart). The enclave that saw the newest refuses
// the older one.
func TestU2ARecordRolledBackGivesNothing(t *testing.T) {
	w := NewTestWorld(t)
	s := w.Enclave()
	save(t, w, s, accountA, "", hold("a", "statement"))
	save(t, w, s, accountA, "", hold("b", "invoice"))
	w.Commits.(*store.MemoryCommits).Forget(rules.RecordName(accountA, 0))
	w.Clock.Move(Fresh)
	unavailable(t, s, accountA)
	if _, err := trySave(w, s, accountA, "", hold("c", "x")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("changed a record rolled back: %v", err)
	}
}

func TestU2AForkedRecordGivesNothing(t *testing.T) {
	w := NewTestWorld(t)
	s := w.Enclave()
	save(t, w, s, accountA, "", hold("a", "statement"))
	w.Break(t, accountA)
	unavailable(t, s, accountA)
	unavailable(t, w.Enclave(), accountA)
	if _, err := s.List(context.Background(), accountA); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("listed a forked record: %v", err)
	}
	if _, err := trySave(w, s, accountA, "", hold("b", "x")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("changed a forked record: %v", err)
	}
}

// A stored rule that does not compile is an error for the account, never a
// rule skipped.
func TestU2ARuleThatDoesNotCompileGivesNothing(t *testing.T) {
	w := NewTestWorld(t)
	s := w.Enclave()
	bad := hold("bad", "x")
	bad.Criteria = nil
	Put(t, s, accountA, &rules.Record{Rules: []rules.Stored{{ID: "rul_bad", Version: 1, Draft: bad}}})
	unavailable(t, s, accountA)
	if _, err := s.List(context.Background(), accountA); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("listed a rule that does not compile: %v", err)
	}
}

// An account with no record has the default alone only when the strict read
// says its epoch 0 was never written.
func TestU2AnAbsentRecordIsTheDefaultOnlyWhenNeverWritten(t *testing.T) {
	w := NewTestWorld(t)
	s := w.Enclave()
	a, err := s.Applying(context.Background(), accountA, claude)
	if err != nil || len(a.Rules) != 1 || a.Rules[0].ID != rules.DefaultID || a.Epoch != 0 || a.Version != 0 {
		t.Fatalf("an account never written: %+v, %v", a, err)
	}
	// Its first value deleted by whoever writes the table: lost, not absent.
	save(t, w, s, "acc_lostlostlostlostlostlostlo", "", hold("a", "x"))
	for _, n := range w.Mem.RecordNames() {
		if strings.HasPrefix(n, "rules-acc_lostlostlostlostlostlostlo-") {
			_ = w.Mem.DeleteRecord(context.Background(), n)
		}
	}
	unavailable(t, w.Enclave(), "acc_lostlostlostlostlostlostlo")
}

// At its versions' bound the record rolls to its next epoch with
// everything, the earlier one removed with its fence; a restart reads the
// newest in one walk; and a writer that stopped between the epoch written
// Next and the next one made leaves it to the next reader.
func TestU2AtItsVersionsBoundTheRecordRollsToTheNextEpoch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := NewTestWorld(t)
	s := w.Enclave()
	r := save(t, w, s, accountA, "", hold("a", "statement"))
	var last uint64
	for range rules.RecordVersionsMax - 1 {
		toggle(t, w, s, accountA)
		l, err := s.List(ctx, accountA)
		if err != nil {
			t.Fatal(err)
		}
		if l.Version <= last {
			t.Fatalf("the rules' version went from %d to %d", last, l.Version)
		}
		last = l.Version
	}
	if got := w.storedOf(accountA); len(got) != 1 || got["1"] != 1 {
		t.Fatalf("after a rollover, stored: %v", got)
	}
	_, at, err := w.Enclave().cfg.Records.GetRecordAt(ctx, rules.RecordName(accountA, 0))
	if !errors.Is(err, store.ErrNotFound) || at.Version() != rules.RecordVersionsMax+1 {
		t.Fatalf("epoch 0 is not removed with its fence: version %d, %v", at.Version(), err)
	}
	fresh := w.Enclave()
	a, err := fresh.Applying(ctx, accountA, claude)
	if err != nil || a.Epoch != 1 || a.Version != 1 || ids(a)[len(a.Rules)-1] != r.ID {
		t.Fatalf("a restart after the rollover: %+v, %v", a, err)
	}
	if l, err := fresh.List(ctx, accountA); err != nil || l.Version != last {
		t.Fatalf("a restart reads version %d, %v; the enclave that wrote it, %d", l.Version, err, last)
	}

	// The next rollover stops after epoch 1 is written Next.
	s2, h := w.enclave()
	h.failWrite = func(name string) error {
		if name == rules.RecordName(accountA, 2) {
			return errors.New("the enclave stopped")
		}
		return nil
	}
	for range rules.RecordVersionsMax - 1 {
		toggle(t, w, s2, accountA)
	}
	if got := w.storedOf(accountA); got["2"] != 0 || got["1"] == 0 {
		t.Fatalf("a stopped rollover left %v", got)
	}
	next := w.Enclave()
	a, err = next.Applying(ctx, accountA, claude)
	if err != nil || a.Epoch != 2 || ids(a)[len(a.Rules)-1] != r.ID {
		t.Fatalf("the next reader after a stopped rollover: %+v, %v", a, err)
	}
	if got := w.storedOf(accountA); len(got) != 1 || got["2"] != 1 {
		t.Fatalf("after the next reader made epoch 2, stored: %v", got)
	}
}

// An epoch above 0 never written whose predecessor was never written Next
// is a history that went back: nothing leaves.
func TestU2AMissingEpochIsARollback(t *testing.T) {
	w := NewTestWorld(t)
	s := w.Enclave()
	save(t, w, s, accountA, "", hold("a", "statement"))
	s.saw(accountA, 1)
	w.Clock.Move(Fresh)
	unavailable(t, s, accountA)
	s.saw(accountA, 3)
	unavailable(t, s, accountA)
}

// The largest record the bounds let a person make, sealed, with every
// attribute of its DynamoDB item, fits one item of 400 KiB.
func TestU2TheLargestRecordFitsOneItem(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := NewTestWorld(t)
	s := w.Enclave()
	var largest []byte
	for i := range contracts.MaxRules {
		d := largestDraft(t, w, s, i)
		save(t, w, s, accountA, "", d)
	}
	raw, err := json.Marshal(mustRead(t, s, accountA))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > rules.RecordBytesMax || len(raw) < contracts.MaxRules*contracts.MaxRuleBytes*9/10 {
		t.Fatalf("the largest record is %d bytes", len(raw))
	}
	for _, n := range w.Mem.RecordNames() {
		r, err := w.Mem.GetRecord(ctx, n)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Value) > len(largest) {
			largest = r.Value
		}
		// pk, sk, value, updated_at: names and values, as DynamoDB counts.
		item := len("pk") + len("system") + len("sk") + len("record#"+n) + len("value") + len(r.Value) + len("updated_at") + 21
		if item > 400<<10 {
			t.Fatalf("an item of %d bytes", item)
		}
	}
	if len(largest) < len(raw) {
		t.Fatal("the largest item was not measured")
	}
}

func mustRead(t *testing.T, s *Service, account string) *rules.Record {
	t.Helper()
	rec, _, err := s.read(context.Background(), account)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// largestDraft is a rule that removes, whose canonical JSON is as large as a
// rule may be: contains conditions on the text with the longest values the
// query language takes, each escaped by JSON at its largest, no value
// holding another, the last cut to what still fits.
func largestDraft(t *testing.T, w *TestWorld, s *Service, i int) contracts.RuleDraft {
	t.Helper()
	d := contracts.RuleDraft{
		Name: strings.Repeat("<", contracts.MaxRuleNameRunes-3) + strconv.Itoa(100+i), On: false, Effect: contracts.EffectRemove,
		Removal: &contracts.RuleRemoval{Parts: []contracts.Part{contracts.PartSender}, Shapes: contracts.ShapeKinds},
		Scope:   contracts.RuleScope{To: contracts.ScopeAll},
	}
	fits := func(conds []contracts.Condition) bool {
		d.Criteria = []contracts.Formula{{V: 1, All: []contracts.Group{{Any: conds}}}}
		_, err := s.Check(context.Background(), accountA, d)
		return err == nil
	}
	value := func(n, length int) string {
		return fmt.Sprintf("%02d%02d", i, n) + strings.Repeat("&", length)
	}
	var conds []contracts.Condition
	for n := 0; n < contracts.MaxGroupConditions; n++ {
		next := append(conds, contracts.Condition{Var: contracts.VarBody, Op: contracts.OpContains, Value: value(n, contracts.MaxTextRunes-4)})
		if fits(next) {
			conds = next
			continue
		}
		lo, hi := 0, contracts.MaxTextRunes-4
		for lo < hi {
			mid := (lo + hi + 1) / 2
			next[n].Value = value(n, mid)
			if fits(next) {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		next[n].Value = value(n, lo)
		if fits(next) {
			conds = next
		}
		break
	}
	d.Criteria = []contracts.Formula{{V: 1, All: []contracts.Group{{Any: conds}}}}
	c, err := s.Check(context.Background(), accountA, d)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := json.Marshal(rules.Stored{Draft: c.Draft})
	if len(stored) < contracts.MaxRuleBytes*95/100 {
		t.Fatalf("a largest rule of %d bytes", len(stored))
	}
	return c.Draft
}
