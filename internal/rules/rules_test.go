package rules

import (
	"errors"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
)

// The canonical identifiers of the default rule's formulas. /status shows
// them and permaily-verify compares them with the source (ADR 0031): a change
// of default.json changes them, and needs a new DefaultVersion.
var defaultIDs = []string{
	"qry_ghi2xjhazzz25rgsy6arrif4ke",
	"qry_dq33uknaplrglzyuws6gcb2uje",
	"qry_lh52gob7oeycyjrkqg2lpn2lje",
}

func TestTheDefaultRuleIsWhatADR0031Says(t *testing.T) {
	r := Default()
	if r.ID != DefaultID || r.Version != DefaultVersion || r.Effect != Hold {
		t.Fatalf("rule %q version %d effect %q", r.ID, r.Version, r.Effect)
	}
	if len(r.Formulas) != len(defaultIDs) {
		t.Fatalf("%d formulas, want %d", len(r.Formulas), len(defaultIDs))
	}
	for i, f := range r.Formulas {
		if f.ID != defaultIDs[i] {
			t.Errorf("formula %d is %s, want %s: default.json changed; give it a new DefaultVersion and update ADR 0031", i, f.ID, defaultIDs[i])
		}
	}
	// Only the last formula is decided by a model, with two atoms.
	for i, f := range r.Formulas[:2] {
		for _, g := range f.Formula.All {
			for _, c := range g.Any {
				if c.Op == contracts.OpAbout || c.Op == contracts.OpMentions {
					t.Errorf("formula %d has a condition a model decides", i)
				}
			}
		}
	}
	if n := len(r.Formulas[2].Formula.All[0].Any); n != 2 {
		t.Errorf("the model's formula has %d conditions, want 2", n)
	}
}

// What the rule does with a message, read for a rule (H1): exact conditions
// hold without a model, and a message they do not settle is held until the
// model answers below 0.30 for both of its atoms.
func TestTheDefaultRuleHoldsWhenInDoubt(t *testing.T) {
	r := Default()
	at := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	msg := func(subject, body string) *query.Message {
		return &query.Message{
			Have:    query.FromItem | query.FromMeta | query.FromEntry,
			Mailbox: "mbx_x", Received: at, Subject: subject, Body: body,
			From: []string{"no-reply@example.com"}, ListRecorded: true,
		}
	}
	atoms := modelAtoms(r)
	if len(atoms) != 2 {
		t.Fatalf("%d atoms, want 2", len(atoms))
	}
	low := query.Answers{atoms[0]: 0.05, atoms[1]: 0.10}
	doubt := query.Answers{atoms[0]: 0.05, atoms[1]: 0.40}
	cases := []struct {
		name    string
		m       *query.Message
		answers query.Answers
		applies bool
	}{
		{"a code in the subject", msg("Your verification code is 123456", ""), nil, true},
		{"a reset in Spanish in the body", msg("Hola", "Para restablecer tu contraseña, pulsa aquí"), nil, true},
		{"case and width are ignored", msg("YOUR ＬＯＧＩＮ CODE", ""), nil, true},
		{"nothing exact and no answer: held", msg("Lunch on Friday?", "See you there"), nil, true},
		{"nothing exact, answers in doubt: held", msg("Lunch on Friday?", "See you there"), doubt, true},
		{"nothing exact, both answers low: not held", msg("Lunch on Friday?", "See you there"), low, false},
		{"a code in the subject, answers low: held all the same", msg("Your one-time code", ""), low, true},
	}
	for _, c := range cases {
		applies := false
		for _, f := range r.Formulas {
			if query.Resolve(query.Eval([]*query.Canonical{f}, c.m, c.answers, query.ForRule).Value, query.ForRule) {
				applies = true
			}
		}
		if applies != c.applies {
			t.Errorf("%s: applies %v, want %v", c.name, applies, c.applies)
		}
	}
}

func modelAtoms(r Rule) []query.Atom {
	var atoms []query.Atom
	for _, f := range r.Formulas {
		for _, g := range f.Formula.All {
			for _, c := range g.Any {
				if c.Op == contracts.OpAbout || c.Op == contracts.OpMentions {
					atoms = append(atoms, query.Atom{Var: c.Var, Op: c.Op, Topic: c.Value})
				}
			}
		}
	}
	return atoms
}

func TestARuleThatDoesNotReadIsRefused(t *testing.T) {
	cases := map[string]string{
		"not JSON":        `{`,
		"no formula":      `[]`,
		"a bad formula":   `[{"v":2,"all":[]}]`,
		"matches no mail": `[{"v":1,"all":[{"any":[{"var":"received","op":"since","value":"2026-11-16T00:00:00Z"}]},{"any":[{"var":"received","op":"before","value":"2026-11-15T00:00:00Z"}]}]}]`,
	}
	for name, data := range cases {
		if _, err := parseRule("x", 1, []byte(data)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := parseRule("x", 1, []byte(`[{"v":1,"all":[]}]`)); err != nil {
		t.Errorf("all mail: %v", err)
	}
	_, err := parseRule("x", 1, []byte(`[{"v":1,"all":[{"any":[{"var":"nope","op":"is","value":"a"}]}]}]`))
	if !errors.Is(err, contracts.ErrInvalidFormula) {
		t.Errorf("an unknown variable: %v", err)
	}
}
