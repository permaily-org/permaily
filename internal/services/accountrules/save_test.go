package accountrules

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
)

// U7: saving (ADR 0040, "Changes, and what they apply to", "Receipts").

func lastReceipt(t *testing.T, w *TestWorld, account string) *receipts.RulesChange {
	t.Helper()
	all := w.Receipts.of(account)
	if len(all) == 0 {
		t.Fatal("no rules receipt")
	}
	r := all[len(all)-1]
	if r.V != receipts.Version || r.RulesChange == nil {
		t.Fatalf("a rules receipt %+v", r)
	}
	return r.RulesChange
}

// A rule's version changes with its criteria, effect or removal, and only
// then; each change's receipt says what changed, never the name, a value or
// a topic.
func TestU7TheVersionChangesWithCriteriaEffectOrRemovalOnly(t *testing.T) {
	w := NewTestWorld(t)
	s := w.Enclave()
	d := hold("Private name", "secretvalue")
	r := save(t, w, s, accountA, "", d)
	rc := lastReceipt(t, w, accountA)
	if r.Version != 1 || !slices.Equal(rc.Changes, []receipts.RulesChangeKind{receipts.RulesMade}) || rc.Rule != r.ID || rc.After != 1 ||
		rc.DraftID != r.Check.DraftID || len(rc.Criteria) != 1 || !strings.HasPrefix(rc.Criteria[0], "qry_") || rc.Version != 0 || rc.Epoch != 0 {
		t.Fatalf("made: %+v, %+v", r, rc)
	}
	steps := []struct {
		change  func(*contracts.RuleDraft)
		version int
		kinds   []receipts.RulesChangeKind
	}{
		{func(d *contracts.RuleDraft) { d.Name = "Another private name" }, 1, []receipts.RulesChangeKind{receipts.RulesName}},
		{func(d *contracts.RuleDraft) {
			d.Scope = contracts.RuleScope{To: contracts.ScopeOnly, Clients: []string{claude}}
		}, 1, []receipts.RulesChangeKind{receipts.RulesScope}},
		{func(d *contracts.RuleDraft) { d.On = false }, 1, []receipts.RulesChangeKind{receipts.RulesOff}},
		{func(d *contracts.RuleDraft) { d.On = true }, 1, []receipts.RulesChangeKind{receipts.RulesOn}},
		{func(d *contracts.RuleDraft) { d.Criteria = hold("", "othervalue").Criteria }, 2, []receipts.RulesChangeKind{receipts.RulesCriteria}},
		{func(d *contracts.RuleDraft) {
			d.Effect, d.Removal = contracts.EffectRemove, &contracts.RuleRemoval{Parts: []contracts.Part{contracts.PartBody}}
		}, 3, []receipts.RulesChangeKind{receipts.RulesEffect}},
		{func(d *contracts.RuleDraft) {
			d.Removal = &contracts.RuleRemoval{Shapes: []contracts.ShapeKind{contracts.ShapeLink}}
		}, 4, []receipts.RulesChangeKind{receipts.RulesEffect}},
		{func(*contracts.RuleDraft) {}, 4, []receipts.RulesChangeKind{}},
	}
	for i, step := range steps {
		step.change(&d)
		got := save(t, w, s, accountA, r.ID, d)
		rc := lastReceipt(t, w, accountA)
		if got.Version != step.version || !slices.Equal(rc.Changes, step.kinds) || rc.Before != steps[max(i-1, 0)].version && i > 0 || rc.After != step.version {
			t.Fatalf("step %d: version %d, receipt %+v", i, got.Version, rc)
		}
	}
	for _, rcp := range w.Receipts.of(accountA) {
		raw, _ := json.Marshal(rcp)
		for _, secret := range []string{"private name", "Private name", "secretvalue", "othervalue"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("a rules receipt holds %q: %s", secret, raw)
			}
		}
	}
	// A rule that is not the account's, and a change without a fresh proof.
	if _, err := trySave(w, s, accountA, "rul_notthisaccounts", d); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("changed a rule not the account's: %v", err)
	}
	stale := Person{Account: accountA, ConfirmedUntil: w.Clock.now()}
	if err := s.Delete(context.Background(), stale, r.ID, contracts.RuleDelete{}); !errors.Is(err, contracts.ErrConfirmationNeeded) {
		t.Fatalf("deleted without a fresh proof: %v", err)
	}
	if _, err := s.SetDefault(context.Background(), stale, contracts.RuleDefaultSet{}); !errors.Is(err, contracts.ErrConfirmationNeeded) {
		t.Fatalf("turned the default off without a fresh proof: %v", err)
	}
}

// The default turned off and on, and a rule deleted, each in its receipt.
func TestU7EveryChangeHasItsReceipt(t *testing.T) {
	ctx := context.Background()
	w := NewTestWorld(t)
	s := w.Enclave()
	r := save(t, w, s, accountA, "", hold("a", "x"))
	l, _ := s.List(ctx, accountA)
	off, err := s.SetDefault(ctx, w.fresh(accountA), contracts.RuleDefaultSet{On: false, Version: l.Version})
	if err != nil || off.On || !off.ChangedAt.Equal(start) || off.Rule.ID != rules.DefaultID || len(off.Rule.Sentence) == 0 {
		t.Fatalf("turned off: %+v, %v", off, err)
	}
	if rc := lastReceipt(t, w, accountA); !slices.Equal(rc.Changes, []receipts.RulesChangeKind{receipts.RulesDefaultOff}) || rc.Rule != rules.DefaultID || rc.Version != l.Version {
		t.Fatalf("its receipt: %+v", rc)
	}
	l, _ = s.List(ctx, accountA)
	if l.Default.On || !l.Default.ChangedAt.Equal(start) {
		t.Fatalf("listed: %+v", l.Default)
	}
	if _, err := s.SetDefault(ctx, w.fresh(accountA), contracts.RuleDefaultSet{On: true, Version: l.Version}); err != nil {
		t.Fatal(err)
	}
	if rc := lastReceipt(t, w, accountA); !slices.Equal(rc.Changes, []receipts.RulesChangeKind{receipts.RulesDefaultOn}) {
		t.Fatalf("its receipt: %+v", rc)
	}
	l, _ = s.List(ctx, accountA)
	if err := s.Delete(ctx, w.fresh(accountA), r.ID, contracts.RuleDelete{Version: l.Version - 1}); !errors.Is(err, contracts.ErrRulesChanged) {
		t.Fatalf("deleted at a version not read: %v", err)
	}
	if err := s.Delete(ctx, w.fresh(accountA), r.ID, contracts.RuleDelete{Version: l.Version}); err != nil {
		t.Fatal(err)
	}
	if rc := lastReceipt(t, w, accountA); !slices.Equal(rc.Changes, []receipts.RulesChangeKind{receipts.RulesDeleted}) || rc.Rule != r.ID || rc.Before != 1 {
		t.Fatalf("its receipt: %+v", rc)
	}
	if rc := w.Receipts.of(accountA); len(rc) != 4 {
		t.Fatalf("%d receipts for four changes", len(rc))
	}
}

// The rules page reads the account's rules as saved: each by its sentence,
// what it does and who for, in the system's words.
func TestU7TheListShowsEachRuleInTheSystemsWords(t *testing.T) {
	ctx := context.Background()
	w := NewTestWorld(t)
	s := w.Enclave()
	d := removing(&contracts.RuleRemoval{Parts: []contracts.Part{contracts.PartSubject}, Shapes: []contracts.ShapeKind{contracts.ShapePhoneNumber, contracts.ShapeEmailAddress}})
	d.Scope = contracts.RuleScope{To: contracts.ScopeAllExcept, Clients: []string{"https://vendor.example.org/client-b.json", "https://vendor.example.org/client-a.json"}}
	d.Criteria[0].All[0].Any = append(d.Criteria[0].All[0].Any, contracts.Condition{Var: contracts.VarMailbox, Op: contracts.OpIs, Value: mailbox1})
	r := save(t, w, s, accountA, "", d)
	c := r.Check
	if c.Does != "Takes out the subject, email addresses, phone numbers (every number of 7 digits or more, with whatever numbers are written next to it), and the sender and attachments, which it does not read" {
		t.Fatalf("does: %q", c.Does)
	}
	if c.For != "Every connection but those of vendor.example.org/client-a.json, vendor.example.org/client-b.json" || len(c.Clients) != 2 || c.Clients[0].Shown != "vendor.example.org/client-a.json" {
		t.Fatalf("for: %q, %+v", c.For, c.Clients)
	}
	if !strings.Contains(c.Sentence.Text, mailbox1) || c.Model {
		t.Fatalf("sentence: %+v", c.Sentence)
	}
	// Read back after the mailbox went: named by its identifier, and the
	// rule still listed as saved.
	w.mailboxes.of[accountA] = nil
	l, err := s.List(ctx, accountA)
	if err != nil || len(l.Rules) != 1 || l.Rules[0].Check.DraftID != r.Check.DraftID || l.Rules[0].Check.Does != c.Does || l.Max != contracts.MaxRules {
		t.Fatalf("listed: %+v, %v", l, err)
	}
	w.mailboxes.fail = errors.New("the mailboxes cannot be read")
	if _, err := s.List(ctx, accountA); err == nil {
		t.Fatal("listed with no mailboxes read")
	}
	topics := removing(&contracts.RuleRemoval{Topics: []string{"A Home Address"}})
	if got := does(topics.Effect, &contracts.RuleRemoval{Topics: []string{"a home address"}}); got != "Takes out every sentence that mentions “a home address” (a model decides), and the sender and attachments, which it does not read" {
		t.Fatalf("does: %q", got)
	}
	if got, _ := forWhom(contracts.RuleScope{To: contracts.ScopeOnly, Clients: []string{claude}}); got != "Only the connections of claude.ai/oauth/claude-code-client-metadata" {
		t.Fatalf("for: %q", got)
	}
	for _, k := range contracts.ShapeKinds {
		if shapeWords[k] == "" {
			t.Fatalf("no words for %s", k)
		}
	}
}

// Both interleavings with the store's own refusal: a receipt that cannot be
// written makes nothing; a write that loses at its version after its
// receipt (another enclave's change came first) leaves that receipt and
// changes nothing; and an enclave that stops between the two leaves the
// receipt and the record as it was.
func TestU7AReceiptIsWrittenBeforeItsChange(t *testing.T) {
	w := NewTestWorld(t)
	a, ha := w.enclave()
	b := w.Enclave()
	first := save(t, w, a, accountA, "", hold("a", "x"))

	w.Receipts.FailNext()
	if _, err := trySave(w, a, accountA, "", hold("b", "y")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a change whose receipt failed: %v", err)
	}
	if n := len(mustRead(t, a, accountA).Rules); n != 1 {
		t.Fatalf("a change made without its receipt: %d rules", n)
	}

	var other *contracts.Rule
	ha.beforeWrite = func(string) {
		ha.beforeWrite = nil
		other = save(t, w, b, accountA, "", hold("c", "z"))
	}
	before := w.Receipts.Count(accountA)
	if _, err := trySave(w, a, accountA, "", hold("b", "y")); !errors.Is(err, contracts.ErrRulesChanged) {
		t.Fatalf("a write that lost to the other enclave: %v", err)
	}
	if got := w.Receipts.Count(accountA); got != before+2 {
		t.Fatalf("%d receipts for a change that lost and one made", got-before)
	}
	rec := mustRead(t, a, accountA)
	if len(rec.Rules) != 2 || rec.Rules[0].ID != first.ID || rec.Rules[1].ID != other.ID {
		t.Fatalf("after a write that lost: %+v", rec.Rules)
	}

	stopped := errors.New("the enclave stopped")
	ha.failWrite = func(string) error { return stopped }
	before = w.Receipts.Count(accountA)
	if _, err := trySave(w, a, accountA, "", hold("d", "q")); !errors.Is(err, stopped) {
		t.Fatalf("a write that stopped: %v", err)
	}
	if w.Receipts.Count(accountA) != before+1 || len(mustRead(t, a, accountA).Rules) != 2 {
		t.Fatal("a stopped write did not leave its receipt and the record as they were")
	}
}

// Agreeing to the notice is the same whatever the rules are: a write that
// lost is made again on the record read again, at most noticeTries times.
func TestU7AgreeingIsMadeAgainOnALostWrite(t *testing.T) {
	ctx := context.Background()
	w := NewTestWorld(t)
	a, ha := w.enclave()
	b := w.Enclave()
	ha.beforeWrite = func(string) {
		ha.beforeWrite = nil
		save(t, w, b, accountA, "", hold("c", "z"))
	}
	n, err := a.AgreeNotice(ctx, w.fresh(accountA), w.notice)
	if err != nil || n.Version != w.notice {
		t.Fatalf("agreed: %+v, %v", n, err)
	}
	rec := mustRead(t, a, accountA)
	if rec.Notice == nil || len(rec.Rules) != 1 {
		t.Fatalf("after agreeing: %+v", rec)
	}
	// Always losing: busy, after noticeTries receipts.
	w2 := NewTestWorld(t)
	c, hc := w2.enclave()
	hc.failWrite = func(string) error { return store.ErrChanged }
	if _, err := c.AgreeNotice(ctx, w2.fresh(accountA), w2.notice); !errors.Is(err, ErrUnavailable) || w2.Receipts.Count(accountA) != noticeTries {
		t.Fatalf("always losing: %v, %d receipts", err, w2.Receipts.Count(accountA))
	}
	hc.failWrite = func(string) error { return errors.New("the table does not answer") }
	if _, err := c.AgreeNotice(ctx, w2.fresh(accountA), w2.notice); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failing: %v", err)
	}
}

// The service needs its parts, and its key.
func TestU7TheServiceNeedsItsParts(t *testing.T) {
	w := NewTestWorld(t)
	if _, err := New(Config{}); !errors.Is(err, errMissing) {
		t.Fatalf("made with nothing: %v", err)
	}
	failing := errors.New("no root secret")
	_, err := New(Config{Records: w.Enclave().cfg.Records, Keys: keysFailing{failing}, Receipts: w.Receipts, Mailboxes: w.mailboxes})
	if !errors.Is(err, failing) {
		t.Fatalf("made with no key: %v", err)
	}
	s := w.Enclave()
	s.cfg.Random = func([]byte) error { return failing }
	if _, err := trySave(w, s, accountA, "", hold("a", "x")); !errors.Is(err, failing) {
		t.Fatalf("saved with no identifier: %v", err)
	}
}

type keysFailing struct{ err error }

func (k keysFailing) Derive(context.Context, string) ([]byte, error) { return nil, k.err }
