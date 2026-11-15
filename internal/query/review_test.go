package query_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/emersion/go-message/mail"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/query/querytest"
)

// The findings of the code review of pull request 1 (ADR 0027), each a test
// named for it, and each condition checked by Eval and by the brute force.

// bothEval is Eval on the canonical form and the brute force on the formula
// as sent, which must agree; the value is Eval's.
func bothEval(t *testing.T, s string, m *query.Message, a query.Answers, mode query.Mode) query.Value {
	t.Helper()
	c := f(t, s)
	got := query.Eval([]*query.Canonical{c}, m, a, mode).Value
	if want := querytest.Eval([]contracts.Formula{c.Formula}, m, a, mode); got != want {
		t.Errorf("%s: Eval %s, brute force %s", s, name(got), name(want))
	}
	return got
}

// Finding 1: an address as stored is already decoded (go-message/mail, as
// internal/inbox parses headers): a quotation mark or a backslash left in
// its local part is a character of it, not quoting, so the address is not
// valid and every condition over it is Unknown, never read as another.
func TestReviewStoredAddressesAreNotUnquotedAgain(t *testing.T) {
	for _, raw := range []string{`"\"alice\""@example.com`, `"\"al\\ice\""@example.com`, `"a\"lice"@example.com`, `"al\\ice"@example.com`} {
		list, err := mail.ParseAddressList(raw)
		if err != nil || len(list) != 1 {
			t.Fatalf("ParseAddressList(%s): %v", raw, err)
		}
		stored := list[0].Address
		if !strings.ContainsAny(stored, `"\`) {
			t.Fatalf("%s is stored as %s, without its quotes", raw, stored)
		}
		for _, s := range []string{"from_address is alice@example.com", "from_domain is example.com", "from_address is al@example.com"} {
			for _, not := range []string{"", "!"} {
				for _, mode := range []query.Mode{query.ForQuery, query.ForRule} {
					if v := bothEval(t, not+s, msg(from(stored)), nil, mode); v != query.Unknown {
						t.Errorf("%s%s on %s (stored %s): %s, want Unknown", not, s, raw, stored, name(v))
					}
					r := strings.NewReplacer("from_", "recipient_")
					if v := bothEval(t, not+r.Replace(s), msg(to(stored)), nil, mode); v != query.Unknown {
						t.Errorf("%s%s on %s (stored %s): %s, want Unknown", not, r.Replace(s), raw, stored, name(v))
					}
				}
			}
		}
	}
	// A quoted local part as a vendor sends it is still unquoted.
	if v := bothEval(t, `from_address is "al\ice"@example.com`, msg(from("alice@example.com")), nil, query.ForQuery); v != query.True {
		t.Errorf("a vendor's quoted local part: %s", name(v))
	}
}

// Finding 2: on an Unparsed message, every condition but the item's is
// Unknown, a model's too: whatever its answer, and no atom is asked.
func TestReviewUnparsedComesBeforeAModelsAnswer(t *testing.T) {
	for _, s := range []string{"subject about invoices", "message about invoices", "message mentions invoices"} {
		v, op, _ := strings.Cut(s, " ")
		op, _, _ = strings.Cut(op, " ")
		atom := query.Atom{Var: contracts.Variable(v), Op: contracts.Operator(op), Topic: "invoices"}
		for _, not := range []string{"", "!"} {
			for _, mode := range []query.Mode{query.ForQuery, query.ForRule} {
				for _, a := range []query.Answers{nil, {atom: 0.9}, {atom: 0.1}} {
					c := f(t, not+s)
					r := query.Eval([]*query.Canonical{c}, msg(unparsed), a, mode)
					if r.Value != query.Unknown || r.Needs != 0 || len(r.Model) != 0 {
						t.Errorf("%s%s, mode %d, answers %v, on an unparsed message: %+v", not, s, mode, a, r)
					}
					if b := querytest.Eval([]contracts.Formula{c.Formula}, msg(unparsed), a, mode); b != query.Unknown {
						t.Errorf("the brute force: %s%s, mode %d, answers %v: %s", not, s, mode, a, name(b))
					}
				}
			}
		}
	}
	// The item's conditions are still known; the group is True.
	c := f(t, "message about invoices OR mailbox is "+mb1)
	if r := query.Eval([]*query.Canonical{c}, msg(unparsed), nil, query.ForQuery); r.Value != query.True || len(r.Model) != 0 {
		t.Errorf("an item's condition on an unparsed message: %+v", r)
	}
	c = f(t, "message about invoices OR mailbox is "+mb2)
	if r := query.Eval([]*query.Canonical{c}, msg(unparsed), nil, query.ForQuery); r.Value != query.Unknown || len(r.Model) != 0 {
		t.Errorf("no atom is asked for an unparsed message: %+v", r)
	}
}

// Finding 3: a message's address whose local part or a label of its domain
// mixes Latin, Greek and Cyrillic is not valid (Unknown for every condition
// over it), and a signer's such domain makes signed_by Unknown.
func TestReviewMixedScriptsOnTheMessagesSide(t *testing.T) {
	const cyrillicO, cyrillicA = "\u043e", "\u0430"
	for _, c := range []struct {
		formula string
		m       *query.Message
	}{
		{"from_domain is example.com", msg(from("inv" + cyrillicO + "ice@example.com"))},
		{"!from_domain is example.com", msg(from("inv" + cyrillicO + "ice@example.com"))},
		{"!from_address is alice@example.com", msg(from("inv" + cyrillicO + "ice@example.com"))},
		{"from_domain is example.com", msg(from("x@p" + cyrillicA + "ypal.example.com"))},
		{"!from_domain is paypal.example.com", msg(from("x@p" + cyrillicA + "ypal.example.com"))},
		{"!recipient_domain is example.com", msg(to("x@p" + cyrillicA + "ypal.com"))},
		{"!signed_by is example.com", msg(signed("p" + cyrillicA + "ypal.com"))},
		{"signed_by is example.com", msg(signed("p" + cyrillicA + "ypal.example.com"))},
	} {
		for _, mode := range []query.Mode{query.ForQuery, query.ForRule} {
			if v := bothEval(t, c.formula, c.m, nil, mode); v != query.Unknown {
				t.Errorf("%s on %v %v: %s, want Unknown", c.formula, c.m.From, c.m.SignedBy, name(v))
			}
		}
	}
	// Cyrillic alone is one script: valid.
	if v := bothEval(t, "from_domain is xn--e1afmkfd.xn--p1ai", msg(from("x@пример.рф")), nil, query.ForQuery); v != query.True {
		t.Errorf("a Cyrillic domain: %s", name(v))
	}
	// Another signature that holds settles it.
	if v := bothEval(t, "signed_by is example.com", msg(signed("p"+cyrillicA+"ypal.com", "example.com")), nil, query.ForQuery); v != query.True {
		t.Errorf("a second signature that holds: %s", name(v))
	}
}

// Finding 4: the first problem in the order of the formula as sent, inside
// a condition too: the variable and the operator are decided when they are
// read, and what needs the whole condition is placed where its member was.
func TestReviewTheFirstProblemInsideACondition(t *testing.T) {
	for _, c := range []struct {
		in     string
		at     string
		reason contracts.FormulaReason
	}{
		{`{"op":"bad_operator","var":"bad_variable","value":"x"}`, ".op", contracts.FormulaUnknownOperator},
		{`{"var":"bad_variable","op":"bad_operator","value":"x"}`, ".var", contracts.FormulaUnknownVariable},
		{`{"var":"bad_variable","op":"is","value":3}`, ".var", contracts.FormulaUnknownVariable},
		{`{"value":3,"var":"bad_variable","op":"is"}`, ".value", contracts.FormulaWrongType},
		{`{"op":"is","value":3,"var":"subject"}`, ".op", contracts.FormulaOperatorNotAllowed},
		{`{"var":"subject","value":3,"op":"is"}`, ".value", contracts.FormulaWrongType},
		{`{"value":"bad_domain!","var":"from_domain","op":"is"}`, ".value", contracts.FormulaInvalidValue},
		{`{"value":"bad_domain!","op":"is","var":"from_domain"}`, ".value", contracts.FormulaInvalidValue},
		{`{"value":"x","op":"exists","var":"attachments"}`, ".value", contracts.FormulaUnexpectedValue},
		{`{"var":"subject","op":"is","value":"x","secret":1}`, ".op", contracts.FormulaOperatorNotAllowed},
		{`{"var":"subject","op":"is","value":"x","op":"is"}`, ".op", contracts.FormulaOperatorNotAllowed},
		{`{"var":"subject","op":"is","any":[]}`, ".op", contracts.FormulaOperatorNotAllowed},
		{`{"value":"bad_domain!","var":"from_domain","op":"is","value":"x"}`, ".value", contracts.FormulaInvalidValue},
		{`{"value":"x","var":"subject","op":"contains","value":"y"}`, ".value", contracts.FormulaDuplicateField},
		{`{"var":"subject","secret":{"a":[1,{"b":2}]},"op":"is"}`, "", contracts.FormulaUnknownField},
		{`{"var":"subject","not":"yes","op":"is"}`, ".not", contracts.FormulaWrongType},
		{`{"var":"subject","op":"contains","not":[1]}`, ".not", contracts.FormulaWrongType},
		{`{"var":{"x":1},"op":"bad"}`, ".var", contracts.FormulaWrongType},
		{`{"op":"contains","var":"subject"}`, ".value", contracts.FormulaMissingValue},
		{`{"op":"contains","value":"x"}`, ".var", contracts.FormulaUnknownVariable},
		{`{"var":"subject","value":"x"}`, ".op", contracts.FormulaUnknownOperator},
		{`{"var":"mailbox","op":"is","value":"mbx_cccccccccccccccccccccccccc","not":1}`, ".value", contracts.FormulaUnknownMailbox},
	} {
		in := `{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"ok"},` + c.in + `]}]}`
		refused(t, []byte(in), []string{mb1}, "all[0].any[1]"+c.at, c.reason)
	}
	// A syntax error after a problem the condition already shows answers
	// that problem; before it, the syntax error.
	refused(t, []byte(`{"v":1,"all":[{"any":[{"var":"subject","op":"is","value":"x",`), nil, "all[0].any[0].op", contracts.FormulaOperatorNotAllowed)
	refused(t, []byte(`{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"x",`), nil, "", contracts.FormulaNotJSON)
	refused(t, []byte(`{"v":1,"all":[{"any":[{"var":"subject","op":"is","value":"x"`), nil, "all[0].any[0].op", contracts.FormulaOperatorNotAllowed)
	refused(t, []byte(`{"v":1,"all":[{"any":[{"var":"subject","secret":{"a":[1,`), nil, "", contracts.FormulaNotJSON)
	refused(t, []byte(`{"v":1,"all":[{"any":[{"var":"subject","op":"is","secret":{"a":[1,`), nil, "all[0].any[0].op", contracts.FormulaOperatorNotAllowed)
	// A list value one byte past its limit.
	refused(t, formula(`{"var":"list","op":"is","value":"`+strings.Repeat("a", 256)+`"}`), nil, "all[0].any[0].value", contracts.FormulaValueTooLong)
	// A canonical form built by hand whose variable or operator is not the
	// language's is Unknown in Eval, and proves nothing in Contains.
	for _, c := range []contracts.Condition{{Var: "nope", Op: "is", Value: "x"}, {Var: "subject", Op: "nope", Value: "x"}, {Var: "subject", Op: "is", Value: "x"}} {
		hand := &query.Canonical{Formula: contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{c}}}}}
		if r := query.Eval([]*query.Canonical{hand}, msg(subject("x")), nil, query.ForQuery); r.Value != query.Unknown {
			t.Errorf("Eval of %+v built by hand: %s", c, name(r.Value))
		}
		if query.Contains(hand, hand) {
			t.Errorf("Contains proved %+v, built by hand, inside itself", c)
		}
	}
}

// Finding 5: a mailbox is mbx_ and 26 characters of base32 (a-z, 2-7).
func TestReviewMailboxIdentifiersAreBase32(t *testing.T) {
	for _, bad := range []string{"mbx_00000000000000000000000001", "mbx_aaaaaaaaaaaaaaaaaaaaaaaaa0", "mbx_aaaaaaaaaaaaaaaaaaaaaaaaa1", "mbx_aaaaaaaaaaaaaaaaaaaaaaaaa8", "mbx_aaaaaaaaaaaaaaaaaaaaaaaaa9"} {
		in := `{"v":1,"all":[{"any":[{"var":"mailbox","op":"is","value":"` + bad + `"}]}]}`
		refused(t, []byte(in), nil, "all[0].any[0].value", contracts.FormulaInvalidValue)
		_, err := query.Canonicalise(contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{{Var: "mailbox", Op: "is", Value: bad}}}}})
		var p *query.Problem
		if !errors.As(err, &p) || p.Reason != contracts.FormulaInvalidValue {
			t.Errorf("Canonicalise of %s: %v", bad, err)
		}
	}
	for _, good := range []string{"mbx_aaaaaaaaaaaaaaaaaaaaaaaaa2", "MBX_ZZZZZZZZZZZZZZZZZZZZZZZZZ7", "mbx_abcdefghijklmnopqrstuvwxyz"} {
		in := `{"v":1,"all":[{"any":[{"var":"mailbox","op":"is","value":"` + good + `"}]}]}`
		if _, err := query.Parse([]byte(in), nil); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
}

// The reference evaluator: the four inputs on which its simplified
// normalisation once disagreed with Eval's, now both from ADR 0027's
// definitions.
func TestReviewTheBruteForceNormalisesAsTheADRSays(t *testing.T) {
	for _, c := range []struct {
		formula string
		m       *query.Message
		want    query.Value
	}{
		{"subject contains á", msg(subject("a\u00ad\u0301")), query.True},
		{"subject contains σ", msg(subject("ς")), query.True},
		{"subject contains k", msg(subject("\u212a")), query.True},
		{"subject contains ss", msg(subject("ß")), query.False},
		{"attachment_type is image/*", msg(attach("x", "image/p ng")), query.False},
		{"!from_domain is example.com", msg(from("a@other..example")), query.Unknown},
		{"!signed_by is example.com", msg(signed("other..example")), query.Unknown},
		{"!list is news.example.com", msg(list("NEWS.example.com")), query.False},
	} {
		if v := bothEval(t, c.formula, c.m, nil, query.ForQuery); v != c.want {
			t.Errorf("%s: %s, want %s", c.formula, name(v), name(c.want))
		}
	}
}
