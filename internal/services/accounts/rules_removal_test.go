package accounts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/accountrules"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
)

// U30 (ADR 0040, "Removed with the account"): removing an account removes
// every epoch of its record of rules with it, through the real rules
// service; a removal whose rules cannot be removed is not done, and the next
// pass finishes it.

type noReceipts struct{}

func (noReceipts) Write(context.Context, string, *receipts.Receipt) error { return nil }

type noMailboxes struct{}

func (noMailboxes) Catalogue(context.Context, string) ([]string, error) { return nil, nil }

func rulesOf(t *testing.T, w *world, k accountrules.Keys) *accountrules.Service {
	t.Helper()
	s, err := accountrules.New(accountrules.Config{
		Records: &store.Anchored{Records: w.mem, Commits: w.commits, Strict: true, Now: func() time.Time { return w.now }},
		Keys:    k, Receipts: noReceipts{}, Mailboxes: noMailboxes{}, Now: func() time.Time { return w.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// failingRules fails its removal while fail is set.
type failingRules struct {
	Rules
	fail error
}

func (f *failingRules) Remove(ctx context.Context, account string) error {
	if f.fail != nil {
		return f.fail
	}
	return f.Rules.Remove(ctx, account)
}

func TestU30RemovingAnAccountRemovesItsRules(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	k, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	ar := rulesOf(t, w, k)
	failing := &failingRules{Rules: ar, fail: errors.New("the rules cannot be removed now")}
	w.svc.cfg.Rules = failing
	me, cookie := w.signUp("ana@example.org")
	id := me.Account.ID
	d := contracts.RuleDraft{Name: "a", On: true, Effect: contracts.EffectHold, Scope: contracts.RuleScope{To: contracts.ScopeAll},
		Criteria: []contracts.Formula{{V: 1, All: []contracts.Group{{Any: []contracts.Condition{{Var: contracts.VarSubject, Op: contracts.OpContains, Value: "x"}}}}}}}
	c, err := ar.Check(ctx, id, d)
	if err != nil {
		t.Fatal(err)
	}
	p := accountrules.Person{Account: id, ConfirmedUntil: w.now.Add(time.Hour)}
	if _, err := ar.Save(ctx, p, "", contracts.RuleSave{DraftID: c.DraftID, Draft: c.Draft}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.StartDeletion(ctx, w.fresh(w.who(cookie))); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(domain.AccountClosing)
	if err := w.svc.RemoveDue(ctx); !errors.Is(err, failing.fail) {
		t.Fatalf("a removal whose rules could not be removed: %v", err)
	}
	if _, err := w.svc.AccountRecord(ctx, id); err != nil {
		t.Fatalf("the account went before its rules: %v", err)
	}
	failing.fail = nil
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.AccountRecord(ctx, id); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("the account stays: %v", err)
	}
	for _, name := range w.mem.RecordNames() {
		if strings.HasPrefix(name, "rules-"+id+"-") {
			t.Fatalf("its rules stay: %s", name)
		}
	}
	a, err := rulesOf(t, w, k).Applying(ctx, id, "https://claude.ai/oauth/claude-code-client-metadata")
	if err != nil || len(a.Rules) != 1 || a.Rules[0].ID != rules.DefaultID {
		t.Fatalf("what is left of its rules: %+v, %v", a, err)
	}
}
