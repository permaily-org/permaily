package inbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/disclosetest"
	"github.com/permaily-org/permaily/internal/redact"
	"github.com/permaily-org/permaily/internal/redact/redacttest"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/rulewords"
	"github.com/permaily-org/permaily/internal/rulewords/rulewordstest"
	"github.com/permaily-org/permaily/internal/services/accountrules"
	"github.com/permaily-org/permaily/internal/services/accountrules/accountrulestest"
)

// rulesSeams is what ADR 0040's pull requests 5 and 6 are written against
// (finding 7): the rules service (pull request 1), the words writer (3), the
// sentence asker and the decisions (4), and the fixed shapes and split (2),
// each its committed interface, assembled here from the shared fakes so a
// change of any seam breaks this file before it breaks a consumer.
type rulesSeams struct {
	rules     accountrules.Rules
	words     rulewords.Writer
	sentences inbox.SentenceAsker
	decisions inbox.Decisions
	redactor  redact.Redactor
}

// The seams assembled: words to a checked draft, saved under the notice,
// applied to a connection's client, and a removal by meaning asked on the
// one view and joined, as pull request 5's discloser will; nothing here is
// the discloser, only the calls it makes, in its order.
func TestADR0040SeamsAssemble(t *testing.T) {
	ctx := context.Background()
	const account, client, notice = "acc_seams", "https://vendor.example/client-a.json", "2026-11-20"
	fake := &accountrulestest.Fake{Notice: notice}
	s := rulesSeams{
		rules: fake,
		words: &rulewordstest.Fake{Answer: func(string) contracts.RuleDraft {
			return contracts.RuleDraft{Name: "Home address", Effect: contracts.EffectRemove, On: true,
				Scope:    contracts.RuleScope{To: contracts.ScopeAll},
				Criteria: []contracts.Formula{{V: contracts.FormulaVersion, All: []contracts.Group{}}},
				Removal:  &contracts.RuleRemoval{Topics: []string{"a home address"}}}
		}},
		sentences: &disclosetest.Sentences{P: func(sentence string, _ int) (float64, bool) {
			if sentence == "Thank you." {
				return 0.01, true
			}
			return 0.9, true
		}},
		decisions: disclosetest.NewDecisions(time.Now),
		redactor:  redacttest.Naive{},
	}
	person := accountrules.Person{Account: account, ConfirmedUntil: time.Now().Add(time.Minute)}
	if _, err := s.rules.AgreeNotice(ctx, person, notice); err != nil {
		t.Fatal(err)
	}
	w, err := s.words.Write(ctx, account, "take my home address out")
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.rules.Check(ctx, account, w.Draft)
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.rules.List(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.rules.Save(ctx, person, "", contracts.RuleSave{DraftID: c.DraftID, Draft: c.Draft, Version: l.Version, Notice: notice}); err != nil {
		t.Fatal(err)
	}
	a, err := s.rules.Applying(ctx, account, client)
	if err != nil || len(a.Rules) != 2 || a.Rules[1].Effect != rules.Remove || a.Rules[1].ModelOff || !a.Rules[1].Removal.Sender() {
		t.Fatalf("applying %+v, %v", a, err)
	}
	rule := a.Rules[1]
	v := redact.NewView("My home address is 1 Main St.", "Thank you.")
	m := s.redactor.Sentences(v)
	id := inbox.NewMessageID(time.Now())
	box := inbox.Box{MailboxID: "mbx_seams"}
	ref := rules.MeaningRef(rules.Ref{ID: rule.ID, Version: rule.Version}, inbox.TextVersion, redact.SplitVersion)
	begun, err := s.decisions.Begin(ctx, box, ref, []inbox.DecisionKey{{ID: id, ExpiresAt: time.Now().Add(time.Hour)}})
	if err != nil || begun[id].State != inbox.DecisionBegun {
		t.Fatalf("begin %+v, %v", begun, err)
	}
	asked, err := s.sentences.AskSentences(ctx, &inbox.SentenceQuestion{Account: account, Sentences: inbox.MaskedSentences(s.redactor, v, m), Topics: rule.Removal.Topics})
	if err != nil {
		t.Fatal(err)
	}
	kept := &inbox.MeaningKept{Map: m.Digest(v), Kept: inbox.KeptSentences(asked, len(m.Sentences), len(rule.Removal.Topics))}
	// Pull request 4 settles it with kept; until then the blocks refuse it,
	// and the discloser would remove the whole text.
	if _, err := s.decisions.Settle(ctx, box, ref, map[string]inbox.Settlement{id: {State: inbox.DecisionCleared, Attempt: begun[id].Attempt, Meaning: kept}}); err == nil {
		t.Fatal("the blocks settled a meaning decision they do not keep")
	}
	got, ok := kept.For(v, m)
	if !ok {
		t.Fatal("its own map")
	}
	subject, text := redact.Join(v, inbox.MeaningSpans(v, m, got))
	if subject != "[removed]" || text != "Thank you." {
		t.Fatalf("%q / %q", subject, text)
	}
}
