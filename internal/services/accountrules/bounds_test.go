package accountrules

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/rules"
)

// U6: every bound of a rule refused with its reason, and every bound over
// the account's rules with too_many_rules (ADR 0040, "The rules' record").

func removing(rm *contracts.RuleRemoval) contracts.RuleDraft {
	d := hold("remove", "x")
	d.Effect, d.Removal = contracts.EffectRemove, rm
	return d
}

func scoped(to contracts.RuleScopeTo, clients ...string) contracts.RuleDraft {
	d := hold("scoped", "x")
	d.Scope = contracts.RuleScope{To: to, Clients: clients}
	return d
}

func criteria(n int) contracts.RuleDraft {
	d := hold("criteria", "x")
	d.Criteria = nil
	for i := range n {
		d.Criteria = append(d.Criteria, hold("", "w"+strconv.Itoa(i)).Criteria[0])
	}
	return d
}

func models(n int) contracts.RuleDraft {
	d := hold("models", "x")
	var any []contracts.Condition
	for i := range n {
		any = append(any, contracts.Condition{Var: contracts.VarMessage, Op: contracts.OpAbout, Value: "topic " + strconv.Itoa(i)})
	}
	d.Criteria = []contracts.Formula{{V: 1, All: []contracts.Group{{Any: any[:min(n, 8)]}}}}
	if n > 8 {
		d.Criteria = append(d.Criteria, contracts.Formula{V: 1, All: []contracts.Group{{Any: any[8:]}}})
	}
	return d
}

func TestU6EveryBoundOfARuleIsRefusedWithItsReason(t *testing.T) {
	w := NewTestWorld(t)
	s := w.Enclave()
	long := strings.Repeat("x", contracts.MaxRuleNameRunes+1)
	tooMany := make([]string, contracts.MaxRuleScopeClients+1)
	for i := range tooMany {
		tooMany[i] = "https://app" + strconv.Itoa(i) + ".example.org/client.json"
	}
	dup := hold("dup", "x")
	dup.Criteria = append(dup.Criteria, dup.Criteria[0])
	for name, c := range map[string]struct {
		d    contracts.RuleDraft
		want contracts.RuleProblem
	}{
		"a name past its bound":     {func() contracts.RuleDraft { d := hold(long, "x"); return d }(), contracts.RuleNameTooLong},
		"a name that is not text":   {hold("a\x00b", "x"), contracts.RuleNameTooLong},
		"a name not valid UTF-8":    {hold("a\xffb", "x"), contracts.RuleNameTooLong},
		"no criteria":               {criteria(0), contracts.RuleNoCriteria},
		"too many criteria":         {criteria(contracts.MaxRuleCriteria + 1), contracts.RuleNoCriteria},
		"a criteria twice":          {dup, contracts.RuleDuplicate},
		"too many model conditions": {models(contracts.MaxRuleModelConditions + 1), contracts.RuleTooManyModel},
		"an unknown effect":         {func() contracts.RuleDraft { d := hold("e", "x"); d.Effect = "forward"; return d }(), contracts.RuleUnknownEffect},
		"a removal on a hold": {func() contracts.RuleDraft {
			d := hold("e", "x")
			d.Removal = &contracts.RuleRemoval{Parts: []contracts.Part{contracts.PartBody}}
			return d
		}(), contracts.RuleRemovalUnexpected},
		"a removal missing":               {removing(nil), contracts.RuleRemovalMissing},
		"an empty removal":                {removing(&contracts.RuleRemoval{}), contracts.RuleRemovalMissing},
		"an unknown part":                 {removing(&contracts.RuleRemoval{Parts: []contracts.Part{"headers"}}), contracts.RuleUnknownPart},
		"a part twice":                    {removing(&contracts.RuleRemoval{Parts: []contracts.Part{contracts.PartBody, contracts.PartBody}}), contracts.RuleDuplicate},
		"an unknown shape":                {removing(&contracts.RuleRemoval{Shapes: []contracts.ShapeKind{"passport"}}), contracts.RuleUnknownShape},
		"a shape twice":                   {removing(&contracts.RuleRemoval{Shapes: []contracts.ShapeKind{contracts.ShapeLink, contracts.ShapeLink}}), contracts.RuleDuplicate},
		"too many topics":                 {removing(&contracts.RuleRemoval{Topics: []string{"a", "b", "c", "d"}}), contracts.RuleInvalidTopic},
		"a topic refused":                 {removing(&contracts.RuleRemoval{Topics: []string{""}}), contracts.RuleInvalidTopic},
		"a topic twice":                   {removing(&contracts.RuleRemoval{Topics: []string{"Home address", "home  address"}}), contracts.RuleDuplicate},
		"an unknown scope":                {scoped("some"), contracts.RuleUnknownScope},
		"clients with all":                {scoped(contracts.ScopeAll, claude), contracts.RuleScopeClients},
		"no client for only":              {scoped(contracts.ScopeOnly), contracts.RuleScopeClients},
		"no client but":                   {scoped(contracts.ScopeAllExcept), contracts.RuleScopeClients},
		"a client twice":                  {scoped(contracts.ScopeOnly, claude, claude), contracts.RuleScopeClients},
		"too many clients":                {scoped(contracts.ScopeAllExcept, tooMany...), contracts.RuleScopeClients},
		"a client not a client":           {scoped(contracts.ScopeOnly, "http://claude.ai/x"), contracts.RuleScopeClients},
		"a client on a name no app is at": {scoped(contracts.ScopeOnly, "https://vendor.example/client-a.json"), contracts.RuleScopeClients},
		"too large": {func() contracts.RuleDraft {
			// Within the query language's bounds, past the rule's: JSON
			// writes each "<" in six bytes, and no value holds another (which
			// the canonical form would drop).
			d := hold("large", "x")
			var any []contracts.Condition
			for i := range contracts.MaxGroupConditions {
				any = append(any, contracts.Condition{Var: contracts.VarBody, Op: contracts.OpContains, Value: fmt.Sprintf("%02d", i) + strings.Repeat("<", contracts.MaxTextRunes-2)})
			}
			d.Criteria[0].All[0].Any = any
			return d
		}(), contracts.RuleTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.Check(context.Background(), accountA, c.d)
			var re *contracts.RuleError
			if !errors.As(err, &re) || re.Problem != c.want || !errors.Is(err, contracts.ErrInvalidRule) {
				t.Fatalf("refused with %v, not %s", err, c.want)
			}
			// Saving refuses it the same way, and writes nothing.
			if _, err := s.Save(context.Background(), w.fresh(accountA), "", contracts.RuleSave{Draft: c.d}); !errors.As(err, &re) || re.Problem != c.want {
				t.Fatalf("saved: %v", err)
			}
			if w.Receipts.Count(accountA) != 0 {
				t.Fatal("a refused draft wrote a receipt")
			}
		})
	}
}

// A criteria the query language refuses is refused with the formula's own
// problem, and one naming a mailbox not the account's too.
func TestU6ACriteriaIsCheckedAsAFormula(t *testing.T) {
	w := NewTestWorld(t)
	s := w.Enclave()
	for _, cond := range []contracts.Condition{
		{Var: "colour", Op: contracts.OpIs, Value: "red"},
		{Var: contracts.VarMailbox, Op: contracts.OpIs, Value: "mbx_zzzzzzzzzzzzzzzzzzzzzzzzzz"},
	} {
		d := hold("f", "x")
		d.Criteria[0].All[0].Any[0] = cond
		if _, err := s.Check(context.Background(), accountA, d); !errors.Is(err, contracts.ErrInvalidFormula) {
			t.Fatalf("%v: %v", cond, err)
		}
	}
	d := hold("f", "x")
	d.Criteria[0].All[0].Any[0] = contracts.Condition{Var: contracts.VarMailbox, Op: contracts.OpIs, Value: mailbox1}
	if _, err := s.Check(context.Background(), accountA, d); err != nil {
		t.Fatalf("the account's own mailbox: %v", err)
	}
	w.mailboxes.fail = errors.New("the mailboxes cannot be read")
	if _, err := s.Check(context.Background(), accountA, d); err == nil {
		t.Fatal("checked with no mailboxes read")
	}
	if _, err := s.Save(context.Background(), w.fresh(accountA), "", contracts.RuleSave{Draft: d}); err == nil {
		t.Fatal("saved with no mailboxes read")
	}
}

// The default rule is turned on or off, never edited or deleted.
func TestU6TheDefaultRuleIsNotEditable(t *testing.T) {
	ctx := context.Background()
	w := NewTestWorld(t)
	s := w.Enclave()
	var re *contracts.RuleError
	if _, err := s.Save(ctx, w.fresh(accountA), rules.DefaultID, contracts.RuleSave{Draft: hold("d", "x")}); !errors.As(err, &re) || re.Problem != contracts.RuleDefaultNotEditable {
		t.Fatalf("edited the default: %v", err)
	}
	if err := s.Delete(ctx, w.fresh(accountA), rules.DefaultID, contracts.RuleDelete{}); !errors.As(err, &re) || re.Problem != contracts.RuleDefaultNotEditable {
		t.Fatalf("deleted the default: %v", err)
	}
}

// Over the account: MaxRules rules, MaxMeaningRules rules removing by
// meaning, MaxAccountModelConditions over the rules that are on.
func TestU6TheAccountsBoundsAreTooManyRules(t *testing.T) {
	w := NewTestWorld(t)
	s := w.Enclave()
	agree(t, w, s, accountA)
	for i := range contracts.MaxMeaningRules {
		save(t, w, s, accountA, "", removing(&contracts.RuleRemoval{Topics: []string{"topic " + strconv.Itoa(i)}}))
	}
	if _, err := trySave(w, s, accountA, "", removing(&contracts.RuleRemoval{Topics: []string{"one too many"}})); !errors.Is(err, contracts.ErrTooManyRules) {
		t.Fatalf("a fifth rule removing by meaning: %v", err)
	}
	// 8×4 model conditions on, and one rule of 8 off: within.
	for i := range 4 {
		d := models(contracts.MaxRuleModelConditions)
		d.Name = "m" + strconv.Itoa(i)
		save(t, w, s, accountA, "", d)
	}
	off := models(contracts.MaxRuleModelConditions)
	off.Name, off.On = "off", false
	r := save(t, w, s, accountA, "", off)
	off.On = true
	if _, err := trySave(w, s, accountA, r.ID, off); !errors.Is(err, contracts.ErrTooManyRules) {
		t.Fatalf("turned on past the account's model conditions: %v", err)
	}
	for i := len(mustRead(t, s, accountA).Rules); i < contracts.MaxRules; i++ {
		save(t, w, s, accountA, "", hold("n"+strconv.Itoa(i), "w"+strconv.Itoa(i)))
	}
	if _, err := trySave(w, s, accountA, "", hold("17", "z")); !errors.Is(err, contracts.ErrTooManyRules) {
		t.Fatalf("a seventeenth rule: %v", err)
	}
}
