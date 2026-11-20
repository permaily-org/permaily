package accountrulestest

import (
	"context"
	"errors"
	"testing"

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
