package decisions

import (
	"context"
	"testing"

	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/decide/decidetest"
	"github.com/permaily-org/permaily/internal/inbox"
)

// What Ask used comes back with it, as kept: the model, the prompt and the
// canary's days of each answer, a kept refusal told from a failure, and an
// answer decided by rule marked so (ADR 0030, "Changed after the design
// review", 14: the discloser and the receipts take the evidence from the
// ask itself, never from a second read of the kept entry).
func TestAskReturnsTheEvidenceOfWhatItUsed(t *testing.T) {
	w := newWorld(t)
	e := w.keyed()
	e.model.set(subj("invoices"), 0.97)
	e.model.set(about("invoices"), 0.91)
	refuse := about("a refused topic")
	e.model.onCall(func(ctx context.Context, n int, c decide.Call) (*decide.Result, error) {
		r, err := decidetest.NewFake(e.model.tab, func(context.Context) (string, bool) { return "k", true }).Decide(ctx, c)
		if err != nil {
			return nil, err
		}
		for i, s := range c.Statements {
			if s == refuse {
				r.Answers[i] = decide.Answer{Refused: true}
			}
		}
		return r, nil
	})
	q := newQuestion("m1", "Invoice 12", "Please pay invoice 12.", subj("invoices"), about("invoices"), refuse)
	first := e.ask(q)
	again := e.ask(newQuestion("m1", "Invoice 12", "Please pay invoice 12.", subj("invoices"), about("invoices"), refuse))
	for name, out := range map[string]*inbox.Asked{"asked now": first, "kept before": again} {
		ev, ok := out.Evidence[about("invoices")]
		if !ok || ev.ByRule || ev.Refused || ev.P != 0.91 || ev.Model != decidetest.Name || ev.Prompt != decide.PromptVersion || ev.At.IsZero() {
			t.Fatalf("%s: the answer's evidence %+v", name, ev)
		}
		if ev, ok := out.Evidence[subj("invoices")]; !ok || ev.P != 0.97 {
			t.Fatalf("%s: the subject's evidence %+v", name, ev)
		}
		ref, ok := out.Evidence[refuse]
		if _, answered := out.Answers[refuse]; !ok || !ref.Refused || answered {
			t.Fatalf("%s: a kept refusal %+v (answered %v)", name, ref, answered)
		}
	}
	empty := e.ask(newQuestion("m2", "", "", subj("invoices")))
	if ev := empty.Evidence[subj("invoices")]; !ev.ByRule || ev.Model != "" {
		t.Fatalf("an empty subject decided by rule: %+v", ev)
	}
}
