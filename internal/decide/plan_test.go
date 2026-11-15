package decide

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
)

func subjectAbout(t string) query.Atom {
	return query.Atom{Var: contracts.VarSubject, Op: contracts.OpAbout, Topic: t}
}
func messageAbout(t string) query.Atom {
	return query.Atom{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: t}
}
func messageMentions(t string) query.Atom {
	return query.Atom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: t}
}

// A8: subject atoms go alone with the subject only, message atoms in one
// call with the subject and the text, each atom once, the subject call
// first.
func TestA8PlanGroupsByContent(t *testing.T) {
	inv, pwd := subjectAbout("invoices"), subjectAbout("password reset")
	about, mentions := messageAbout("invoices"), messageMentions("a home address")
	exact := query.Atom{Var: contracts.VarBody, Op: contracts.OpContains, Topic: "x"}
	calls, byRule := Plan([]query.Atom{about, inv, mentions, inv, exact, pwd, about}, "Invoice 12", "Please pay.", true)
	want := []Call{
		{Content: Content{Kind: ContentSubject, Subject: "Invoice 12"}, Statements: []query.Atom{inv, pwd}},
		{Content: Content{Kind: ContentMessage, Subject: "Invoice 12", Body: "Please pay."}, Statements: []query.Atom{about, mentions}},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %+v, want %+v", calls, want)
	}
	if byRule != nil {
		t.Errorf("byRule = %v, want none", byRule)
	}
	if calls, byRule := Plan(nil, "s", "t", true); calls != nil || byRule != nil {
		t.Errorf("no atoms: %v %v", calls, byRule)
	}
}

// A8: a group past MaxStatementsPerCall is split in order, and every atom
// is in exactly one call.
func TestA8PlanSplitsPastTheLimit(t *testing.T) {
	var atoms []query.Atom
	for i := range MaxStatementsPerCall + 6 {
		atoms = append(atoms, messageAbout(fmt.Sprintf("topic %d", i)))
	}
	for i := range 2*MaxStatementsPerCall + 1 {
		atoms = append(atoms, subjectAbout(fmt.Sprintf("topic %d", i)))
	}
	calls, _ := Plan(atoms, "s", "t", true)
	sizes := []int{}
	seen := map[query.Atom]int{}
	for _, c := range calls {
		sizes = append(sizes, len(c.Statements))
		for _, a := range c.Statements {
			seen[a]++
			if KindOf(a) != c.Content.Kind {
				t.Errorf("%v in a call on %v", a, c.Content.Kind)
			}
		}
	}
	if !reflect.DeepEqual(sizes, []int{64, 64, 1, 64, 6}) {
		t.Errorf("sizes = %v", sizes)
	}
	if len(seen) != len(atoms) {
		t.Errorf("%d atoms in calls, want %d", len(seen), len(atoms))
	}
	for a, n := range seen {
		if n != 1 {
			t.Errorf("%v in %d calls", a, n)
		}
	}
	// A split call's statements are its own: appending to one does not
	// write into the next.
	first := calls[0].Statements
	_ = append(first, messageAbout("extra"))
	if calls[1].Statements[0] != atoms[MaxStatementsPerCall+6+64] {
		t.Error("the calls share their statements' backing array")
	}
}

// A8: empty fields are decided by rule, never asked; a text that could not
// be read leaves message atoms out of both, never 0.
func TestA8PlanDecidesEmptyFieldsByRule(t *testing.T) {
	sub, about, mentions := subjectAbout("invoices"), messageAbout("invoices"), messageMentions("invoices")
	all := []query.Atom{sub, about, mentions}
	for _, c := range []struct {
		name          string
		subject, text string
		hasText       bool
		calls         []Call
		byRule        query.Answers
	}{
		{
			name: "an empty subject gives its subject atoms 0 without a call", subject: "", text: "Please pay.", hasText: true,
			calls:  []Call{{Content: Content{Kind: ContentMessage, Body: "Please pay."}, Statements: []query.Atom{about, mentions}}},
			byRule: query.Answers{sub: 0},
		},
		{
			name: "an empty subject and a text read as empty give every atom 0", subject: "", text: "", hasText: true,
			byRule: query.Answers{sub: 0, about: 0, mentions: 0},
		},
		{
			name: "a subject with an empty text is still asked", subject: "Invoice", text: "", hasText: true,
			calls: []Call{
				{Content: Content{Kind: ContentSubject, Subject: "Invoice"}, Statements: []query.Atom{sub}},
				{Content: Content{Kind: ContentMessage, Subject: "Invoice"}, Statements: []query.Atom{about, mentions}},
			},
		},
		{
			name: "no text read leaves message atoms unknown", subject: "Invoice", text: "", hasText: false,
			calls: []Call{{Content: Content{Kind: ContentSubject, Subject: "Invoice"}, Statements: []query.Atom{sub}}},
		},
		{
			name: "no text read and an empty subject: message atoms are never 0", subject: "", text: "", hasText: false,
			byRule: query.Answers{sub: 0},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls, byRule := Plan(all, c.subject, c.text, c.hasText)
			if !reflect.DeepEqual(calls, c.calls) {
				t.Errorf("calls = %+v, want %+v", calls, c.calls)
			}
			if !reflect.DeepEqual(byRule, c.byRule) {
				t.Errorf("byRule = %v, want %v", byRule, c.byRule)
			}
		})
	}
}

// A8: a message's atoms at their most (MaxAtomsPerMessage) fit one call of
// each kind, so nothing splits today.
func TestA8PlanFitsAMessage(t *testing.T) {
	var atoms []query.Atom
	for i := range MaxAtomsPerMessage {
		atoms = append(atoms, messageAbout(fmt.Sprint(i)), subjectAbout(fmt.Sprint(i)))
	}
	if calls, _ := Plan(atoms, "s", "t", true); len(calls) != 2 {
		t.Errorf("%d calls, want 2", len(calls))
	}
}
