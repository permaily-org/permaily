package query_test

import (
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/query/querytest"
)

// Q21 and Q43 on querytest's generated formulas: canonical formulas over its
// universe of colliding values, as Canonicalise writes them from a formula
// and from the same formula respelled.

// querytestContexts name querytest's two mailboxes by label, by address, and
// not at all, so every run reads both as disconnected too.
var querytestContexts = []query.Context{
	{Lang: "en", Mailboxes: map[string]query.MailboxName{
		querytest.Mailboxes[0]: {Label: "Work", Address: "k7m2p9q4r8s3t6v5w2x9@permaily.com"},
		querytest.Mailboxes[1]: {Label: "", Address: "a2b3c4d5e6f7g8h9i2j3@permaily.com"},
	}},
	{Lang: "en", Mailboxes: map[string]query.MailboxName{
		querytest.Mailboxes[0]: {Label: "Bills", Address: "k7m2p9q4r8s3t6v5w2x9@permaily.com"},
	}},
	{Lang: "en", Mailboxes: map[string]query.MailboxName{}},
}

// Q21: the sentence of every generated canonical formula reads back as it,
// from its plain text and its parts; a respelled formula has the same
// canonical form, and so the same sentence; both of querytest's mailboxes
// read as disconnected in the run. Q43: each sentence is under 100 KiB.
func TestNoTwoGeneratedFormulasReadAlike(t *testing.T) {
	r := querytest.New(21)
	disconnected := map[string]bool{}
	for i := range 2000 {
		f := r.Formula(4, 4)
		if i%20 == 0 {
			f = r.Formula(contracts.MaxGroups, contracts.MaxConditions/contracts.MaxGroups)
		}
		c, err := query.Canonicalise(f)
		if err != nil {
			t.Fatal(err) // querytest keeps within the limits
		}
		ctx := querytestContexts[i%len(querytestContexts)]
		if err := query.ReadsBack(c, ctx); err != nil {
			t.Fatalf("%v:\n%s\n%+v", err, query.Render(c, ctx).Text, c.Formula)
		}
		s := query.Render(c, ctx)
		if len(s.Text) >= 100<<10 {
			t.Fatalf("%d bytes", len(s.Text))
		}
		if respelled, err := query.Canonicalise(r.Respell(f)); err == nil && query.Render(respelled, ctx).Text != s.Text {
			t.Fatalf("respelled, another sentence:\n%s\n%s", s.Text, query.Render(respelled, ctx).Text)
		}
		for _, g := range c.Formula.All {
			for _, cd := range g.Any {
				if _, ok := ctx.Mailboxes[cd.Value]; cd.Var == contracts.VarMailbox && !ok {
					disconnected[cd.Value] = true
				}
			}
		}
	}
	if !disconnected[querytest.Mailboxes[0]] || !disconnected[querytest.Mailboxes[1]] {
		t.Errorf("the run had no two disconnected mailboxes: %v", disconnected)
	}
}
