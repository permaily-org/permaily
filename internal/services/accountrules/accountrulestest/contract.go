package accountrulestest

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/accountrules"
)

// World is what Contract needs of a Source under test: the Source, a way to
// save an account's record as the person's change would (through the same
// enclave, so its cache must not hide it), and a way to make the account's
// record unreadable.
type World struct {
	Source  accountrules.Source
	Save    func(t *testing.T, account string, rec *rules.Record)
	Break   func(t *testing.T, account string)
	Client  string
	Account string
}

// Contract is what every Source keeps (U3, U4): the default alone for an
// account with no record; a change saved through this enclave applied by
// the next call; the default turned off gone; a record that cannot be read
// an error wrapping ErrUnavailable, never the default alone.
func Contract(t *testing.T, w World) {
	t.Helper()
	ctx := context.Background()
	a, err := w.Source.Applying(ctx, w.Account, w.Client)
	if err != nil || len(a.Rules) != 1 || a.Rules[0].ID != rules.DefaultID {
		t.Fatalf("an account with no record: %+v, %v", a, err)
	}
	hold := rules.Stored{ID: "rul_contract", Version: 1, Draft: contracts.RuleDraft{
		Name: "contract", On: true, Effect: contracts.EffectHold, Scope: contracts.RuleScope{To: contracts.ScopeAll},
		Criteria: []contracts.Formula{{V: 1, All: []contracts.Group{{Any: []contracts.Condition{
			{Var: contracts.VarSubject, Op: contracts.OpContains, Value: "contract"},
		}}}}},
	}}
	w.Save(t, w.Account, &rules.Record{V: rules.RecordFormat, Account: w.Account, Rules: []rules.Stored{hold}})
	a, err = w.Source.Applying(ctx, w.Account, w.Client)
	if err != nil || len(a.Rules) != 2 || a.Rules[1].ID != "rul_contract" {
		t.Fatalf("a rule saved through this enclave: %+v, %v", a, err)
	}
	w.Save(t, w.Account, &rules.Record{V: rules.RecordFormat, Account: w.Account, DefaultOff: true, Rules: []rules.Stored{hold}})
	a, err = w.Source.Applying(ctx, w.Account, w.Client)
	if err != nil || len(a.Rules) != 1 || a.Rules[0].ID != "rul_contract" {
		t.Fatalf("the default turned off: %+v, %v", a, err)
	}
	w.Break(t, w.Account)
	if a, err = w.Source.Applying(ctx, w.Account, w.Client); !errors.Is(err, accountrules.ErrUnavailable) || a.Rules != nil {
		t.Fatalf("a record that cannot be read: %+v, %v", a, err)
	}
}

// RulesWorld is what RulesContract needs of a Rules under test: the service,
// an account with no rules, the client its connections are of, the rules'
// notice in force, and two ways to interleave a change (U7): FailReceipt
// makes the next change's `rules` receipt fail to be written, and LoseNext
// makes the next change's write lose at its version after its receipt was
// written; Receipts counts the `rules` receipts written for the account.
type RulesWorld struct {
	Rules       accountrules.Rules
	Account     string
	Client      string
	Notice      string
	FailReceipt func(t *testing.T)
	LoseNext    func(t *testing.T)
	Receipts    func(t *testing.T) int
}

func exactDraft(name, word string) contracts.RuleDraft {
	return contracts.RuleDraft{Name: name, On: true, Effect: contracts.EffectHold, Scope: contracts.RuleScope{To: contracts.ScopeAll},
		Criteria: []contracts.Formula{{V: 1, All: []contracts.Group{{Any: []contracts.Condition{
			{Var: contracts.VarSubject, Op: contracts.OpContains, Value: word},
		}}}}}}
}

// RulesContract is what every Rules keeps (U5, U6's shape, U7, U29's one
// enclave half, U30): a fresh proof for every change; the version read named
// and a change on another refused; the draft's identifier worked out again;
// a rule's version changed by its criteria and not its name; a draft a model
// decides needing the notice; at most MaxRules rules; a rule not the
// account's not found; a change applied by the next Applying; each change's
// receipt before it, none made without one, and one left by a change that
// lost; the account's removal leaving none.
func RulesContract(t *testing.T, w RulesWorld) {
	t.Helper()
	ctx := context.Background()
	fresh := accountrules.Person{Account: w.Account, ConfirmedUntil: time.Now().Add(time.Hour)}
	stale := accountrules.Person{Account: w.Account, ConfirmedUntil: time.Now().Add(-time.Second)}
	list := func() *contracts.RuleList {
		l, err := w.Rules.List(ctx, w.Account)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		return l
	}
	checked := func(d contracts.RuleDraft) *contracts.RuleCheck {
		c, err := w.Rules.Check(ctx, w.Account, d)
		if err != nil {
			t.Fatalf("check %q: %v", d.Name, err)
		}
		return c
	}
	l := list()
	if len(l.Rules) != 0 || !l.Default.On {
		t.Fatalf("an account with no rules: %+v", l)
	}
	c := checked(exactDraft("one", "invoice"))
	if _, err := w.Rules.Save(ctx, stale, "", contracts.RuleSave{DraftID: c.DraftID, Draft: c.Draft, Version: l.Version}); !errors.Is(err, contracts.ErrConfirmationNeeded) {
		t.Fatalf("saved without a fresh proof: %v", err)
	}
	if w.Receipts(t) != 0 {
		t.Fatal("a change refused for its proof wrote a receipt")
	}
	if _, err := w.Rules.Save(ctx, fresh, "", contracts.RuleSave{DraftID: c.DraftID + "x", Draft: c.Draft, Version: l.Version}); !errors.Is(err, contracts.ErrRuleChanged) {
		t.Fatalf("saved another draft than checked: %v", err)
	}
	if _, err := w.Rules.Save(ctx, fresh, "", contracts.RuleSave{DraftID: c.DraftID, Draft: c.Draft, Version: l.Version + 1}); !errors.Is(err, contracts.ErrRulesChanged) {
		t.Fatalf("saved at a version not read: %v", err)
	}
	w.FailReceipt(t)
	if _, err := w.Rules.Save(ctx, fresh, "", contracts.RuleSave{DraftID: c.DraftID, Draft: c.Draft, Version: l.Version}); err == nil || len(list().Rules) != 0 {
		t.Fatalf("a change made without its receipt: %v", err)
	}
	r, err := w.Rules.Save(ctx, fresh, "", contracts.RuleSave{DraftID: c.DraftID, Draft: c.Draft, Version: list().Version})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if n := w.Receipts(t); n != 1 {
		t.Fatalf("%d receipts for one change", n)
	}
	a, err := w.Rules.Applying(ctx, w.Account, w.Client)
	if err != nil || len(a.Rules) != 2 || a.Rules[1].ID != r.ID {
		t.Fatalf("a rule saved not applied by the next call: %+v, %v", a, err)
	}
	renamed := checked(exactDraft("renamed", "invoice"))
	r2, err := w.Rules.Save(ctx, fresh, r.ID, contracts.RuleSave{DraftID: renamed.DraftID, Draft: renamed.Draft, Version: list().Version})
	if err != nil || r2.Version != r.Version {
		t.Fatalf("a rename changed the version: %+v, %v", r2, err)
	}
	other := checked(exactDraft("renamed", "receipt"))
	w.LoseNext(t)
	before := w.Receipts(t)
	if _, err := w.Rules.Save(ctx, fresh, r.ID, contracts.RuleSave{DraftID: other.DraftID, Draft: other.Draft, Version: list().Version}); !errors.Is(err, contracts.ErrRulesChanged) {
		t.Fatalf("a write that lost: %v", err)
	}
	if w.Receipts(t) != before+1 {
		t.Fatal("a change that lost after its receipt left none: receipts say more than happened, never less")
	}
	r3, err := w.Rules.Save(ctx, fresh, r.ID, contracts.RuleSave{DraftID: other.DraftID, Draft: other.Draft, Version: list().Version})
	if err != nil || r3.Version != r.Version+1 {
		t.Fatalf("new criteria kept the version: %+v, %v", r3, err)
	}
	about := exactDraft("model", "x")
	about.Criteria[0].All[0].Any[0] = contracts.Condition{Var: contracts.VarMessage, Op: contracts.OpAbout, Value: "travel"}
	ac := checked(about)
	if !ac.Model {
		t.Fatal("a draft with about is not one a model decides")
	}
	if _, err := w.Rules.Save(ctx, fresh, "", contracts.RuleSave{DraftID: ac.DraftID, Draft: ac.Draft, Version: list().Version, Notice: w.Notice}); !errors.Is(err, contracts.ErrRulesNoticeNeeded) {
		t.Fatalf("a model's rule saved before the notice was agreed: %v", err)
	}
	if _, err := w.Rules.AgreeNotice(ctx, fresh, w.Notice+"-old"); !errors.Is(err, contracts.ErrRulesNoticeNeeded) {
		t.Fatalf("another notice agreed: %v", err)
	}
	if _, err := w.Rules.AgreeNotice(ctx, fresh, w.Notice); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Rules.Save(ctx, fresh, "", contracts.RuleSave{DraftID: ac.DraftID, Draft: ac.Draft, Version: list().Version, Notice: w.Notice}); err != nil {
		t.Fatalf("a model's rule under the notice agreed: %v", err)
	}
	if err := w.Rules.Delete(ctx, fresh, "rul_not_this_accounts", contracts.RuleDelete{Version: list().Version}); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("deleted a rule not the account's: %v", err)
	}
	if err := w.Rules.Delete(ctx, fresh, r.ID, contracts.RuleDelete{Version: list().Version}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Rules.SetDefault(ctx, fresh, contracts.RuleDefaultSet{On: false, Version: list().Version}); err != nil {
		t.Fatal(err)
	}
	a, err = w.Rules.Applying(ctx, w.Account, w.Client)
	if err != nil || len(a.Rules) != 1 || a.Rules[0].ID == r.ID || a.Rules[0].ID == rules.DefaultID {
		t.Fatalf("a rule deleted and the default turned off, applied: %+v, %v", a, err)
	}
	for i := len(list().Rules); i < contracts.MaxRules; i++ {
		d := checked(exactDraft("n"+strconv.Itoa(i), "w"+strconv.Itoa(i)))
		if _, err := w.Rules.Save(ctx, fresh, "", contracts.RuleSave{DraftID: d.DraftID, Draft: d.Draft, Version: list().Version}); err != nil {
			t.Fatalf("rule %d: %v", i, err)
		}
	}
	d := checked(exactDraft("one too many", "z"))
	if _, err := w.Rules.Save(ctx, fresh, "", contracts.RuleSave{DraftID: d.DraftID, Draft: d.Draft, Version: list().Version}); !errors.Is(err, contracts.ErrTooManyRules) {
		t.Fatalf("rule %d: %v", contracts.MaxRules+1, err)
	}
	if err := w.Rules.Remove(ctx, w.Account); err != nil {
		t.Fatal(err)
	}
	if l := list(); len(l.Rules) != 0 || !l.Default.On {
		t.Fatalf("the account's removal left rules: %+v", l)
	}
}
