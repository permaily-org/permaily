package query_test

import (
	"encoding/json"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/query/querytest"
)

// Q10 of ADR 0027: one case per rewrite and per relation of the implication
// table, before and after.
func TestQ10EveryRewriteAndRelation(t *testing.T) {
	c := func(v, op, value string, not ...bool) string {
		n := ""
		if len(not) > 0 && not[0] {
			n = `,"not":true`
		}
		if value == "" {
			return `{"var":"` + v + `","op":"` + op + `"` + n + `}`
		}
		return `{"var":"` + v + `","op":"` + op + `","value":"` + value + `"` + n + `}`
	}
	or := func(cs ...string) string {
		s := `{"any":[`
		for i, x := range cs {
			if i > 0 {
				s += ","
			}
			s += x
		}
		return s + `]}`
	}
	and := func(gs ...string) string {
		s := `{"v":1,"all":[`
		for i, g := range gs {
			if i > 0 {
				s += ","
			}
			s += g
		}
		return s + `]}`
	}
	const (
		s1 = "2026-01-01T00:00:00Z"
		s2 = "2026-02-01T00:00:00Z"
		m1 = "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa"
		m2 = "mbx_bbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	for _, x := range []struct{ name, in, want string }{
		// Rule 1: duplicates in a group.
		{"duplicates in a group", and(or(c("subject", "contains", "x"), c("subject", "contains", "X"))), and(or(c("subject", "contains", "x")))},
		// Rule 2, each relation of the table, in a group: the weaker stays.
		{"address in its domain", and(or(c("from_address", "is", "a@sub.example.com"), c("from_domain", "is", "example.com"))), and(or(c("from_domain", "is", "example.com")))},
		{"domain in its parent", and(or(c("from_domain", "is", "sub.example.com"), c("from_domain", "is", "example.com"))), and(or(c("from_domain", "is", "example.com")))},
		{"recipient address in its domain", and(or(c("recipient_address", "is", "a@example.com"), c("recipient_domain", "is", "example.com"))), and(or(c("recipient_domain", "is", "example.com")))},
		{"recipient domain in its parent", and(or(c("recipient_domain", "is", "a.example.com"), c("recipient_domain", "is", "example.com"))), and(or(c("recipient_domain", "is", "example.com")))},
		{"signer in its parent", and(or(c("signed_by", "is", "mail.example.com"), c("signed_by", "is", "example.com"))), and(or(c("signed_by", "is", "example.com")))},
		{"a text and a substring of it", and(or(c("subject", "contains", "invoice"), c("subject", "contains", "invoices"))), and(or(c("subject", "contains", "invoice")))},
		{"a body text and a substring", and(or(c("body", "contains", "invoice 2026"), c("body", "contains", "e 2"))), and(or(c("body", "contains", "e 2")))},
		{"an attachment name and a substring", and(or(c("attachment_name", "contains", "x.pdf"), c("attachment_name", "contains", ".pdf"))), and(or(c("attachment_name", "contains", ".pdf")))},
		{"an attachment name and attachments", and(or(c("attachment_name", "contains", "x.pdf"), c("attachments", "exists", ""))), and(or(c("attachments", "exists", "")))},
		{"a type and its T/*", and(or(c("attachment_type", "is", "image/png"), c("attachment_type", "is", "image/*"))), and(or(c("attachment_type", "is", "image/*")))},
		{"a type and attachments", and(or(c("attachment_type", "is", "image/png"), c("attachments", "exists", ""))), and(or(c("attachments", "exists", "")))},
		{"a list and lists", and(or(c("list", "is", "a.example"), c("list", "exists", ""))), and(or(c("list", "exists", "")))},
		{"since: the earlier stays in a group", and(or(c("received", "since", s2), c("received", "since", s1))), and(or(c("received", "since", s1)))},
		{"before: the later stays in a group", and(or(c("received", "before", s2), c("received", "before", s1))), and(or(c("received", "before", s2)))},
		{"at_least: the smaller stays", and(or(c("size", "at_least", "10"), c("size", "at_least", "20"))), and(or(c("size", "at_least", "10")))},
		{"below: the greater stays", and(or(c("size", "below", "10"), c("size", "below", "20"))), and(or(c("size", "below", "20")))},
		{"negated: not domain implies not its subdomain", and(or(c("from_domain", "is", "sub.example.com", true), c("from_domain", "is", "example.com", true))), and(or(c("from_domain", "is", "sub.example.com", true)))},
		{"negated: not a text implies not a longer one", and(or(c("subject", "contains", "car", true), c("subject", "contains", "scar", true))), and(or(c("subject", "contains", "scar", true)))},
		{"a model's condition only with itself", and(or(c("message", "about", "Invoices"), c("message", "about", "invoices"), c("message", "about", "bills"), c("message", "mentions", "invoices"))), and(or(c("message", "about", "bills"), c("message", "about", "invoices"), c("message", "mentions", "invoices")))},
		{"no two variables but an address and its domain", and(or(c("subject", "contains", "x"), c("body", "contains", "x"), c("signed_by", "is", "example.com"), c("from_domain", "is", "example.com"))), and(or(c("from_domain", "is", "example.com"), c("signed_by", "is", "example.com"), c("subject", "contains", "x"), c("body", "contains", "x")))},
		// Rule 3: a group that always holds.
		{"before t or since s with s ≤ t", and(or(c("received", "before", s2), c("received", "since", s1)), or(c("subject", "contains", "x"))), and(or(c("subject", "contains", "x")))},
		{"before t or since s with s = t", and(or(c("received", "before", s1), c("received", "since", s1))), and()},
		{"before t or since s with s > t stays", and(or(c("received", "before", s1), c("received", "since", s2))), and(or(c("received", "since", s2), c("received", "before", s1)))},
		{"a mailbox or not", and(or(c("mailbox", "is", m1), c("mailbox", "is", m1, true))), and()},
		// Rule 4: duplicates of groups.
		{"duplicates of groups", and(or(c("subject", "contains", "x")), or(c("subject", "contains", "X"))), and(or(c("subject", "contains", "x")))},
		// Rule 5: absorption between groups: the stronger group stays.
		{"a group implied by another goes", and(or(c("subject", "contains", "invoices")), or(c("subject", "contains", "invoice"))), and(or(c("subject", "contains", "invoices")))},
		{"since: the later stays across groups", and(or(c("received", "since", s1)), or(c("received", "since", s2))), and(or(c("received", "since", s2)))},
		{"a group and a wider one", and(or(c("subject", "contains", "x")), or(c("subject", "contains", "x"), c("body", "contains", "y"))), and(or(c("subject", "contains", "x")))},
		// Rule 6: a lone always-known condition rules out disjoint ones.
		{"a mailbox rules out another", and(or(c("mailbox", "is", m1)), or(c("mailbox", "is", m2), c("subject", "contains", "x"))), and(or(c("subject", "contains", "x")), or(c("mailbox", "is", m1)))},
		{"a mailbox rules out its negation", and(or(c("mailbox", "is", m1)), or(c("mailbox", "is", m1, true), c("subject", "contains", "x"))), and(or(c("subject", "contains", "x")), or(c("mailbox", "is", m1)))},
		{"since rules out an earlier before", and(or(c("received", "since", s2)), or(c("received", "before", s1), c("subject", "contains", "x"))), and(or(c("subject", "contains", "x")), or(c("received", "since", s2)))},
		{"a size rules out nothing", and(or(c("size", "at_least", "20")), or(c("size", "below", "10"), c("subject", "contains", "x"))), and(or(c("subject", "contains", "x"), c("size", "below", "10")), or(c("size", "at_least", "20")))},
		// Made positive.
		{"not since is before", and(or(c("received", "since", s1, true))), and(or(c("received", "before", s1)))},
		{"not before is since", and(or(c("received", "before", s1, true))), and(or(c("received", "since", s1)))},
		{"not at_least is below", and(or(c("size", "at_least", "5", true))), and(or(c("size", "below", "5")))},
		{"not below is at_least", and(or(c("size", "below", "5", true))), and(or(c("size", "at_least", "5")))},
	} {
		t.Run(x.name, func(t *testing.T) {
			got := canon(t, x.in)
			want := canon(t, x.want)
			if jsonOf(got) != jsonOf(want) || got.ID != want.ID {
				t.Errorf("\n got %s\nwant %s", jsonOf(got), jsonOf(want))
			}
		})
	}
	// The ADR's two substring cases, exactly.
	if s := jsonOf(canon(t, and(or(c("subject", "contains", "invoices")), or(c("subject", "contains", "invoice"))))); s != `{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"invoices"}]}]}` {
		t.Errorf("invoices and invoice: %s", s)
	}
	// "Never", from a lone mailbox and another lone one.
	got := canonOf(t, contracts.Formula{V: 1, All: []contracts.Group{
		{Any: []contracts.Condition{{Var: contracts.VarMailbox, Op: contracts.OpIs, Value: m1}}},
		{Any: []contracts.Condition{{Var: contracts.VarMailbox, Op: contracts.OpIs, Value: m2}}},
	}})
	if !got.Never || jsonOf(got) != `{"v":1,"all":[{"any":[]}]}` {
		t.Errorf("two mailboxes: never %v, %s", got.Never, jsonOf(got))
	}
}

// generated is how many formulas and messages the properties below take:
// enough to meet every rewrite and every reason for Unknown many times, in
// a second or two on a workstation.
const (
	generatedFormulas = 2500
	messagesPerA      = 30
)

// Q11: the canonical form of a generated formula's canonical form is
// itself, byte for byte, and so is its identifier.
func TestQ11CanonicaliseIsIdempotent(t *testing.T) {
	t.Parallel()
	r := querytest.New(11)
	for range generatedFormulas {
		c := canonOf(t, r.Formula(5, 4))
		again := canonOf(t, c.Formula)
		if jsonOf(again) != jsonOf(c) || again.ID != c.ID || again.Never != c.Never {
			t.Fatalf("\n   once %s\n  twice %s", jsonOf(c), jsonOf(again))
		}
		if !c.Never {
			p, err := query.Parse([]byte(jsonOf(c)), nil)
			if err != nil || jsonOf(p) != jsonOf(c) {
				t.Fatalf("Parse of %s: %v", jsonOf(c), err)
			}
		}
	}
}

// Q12: simplification keeps the meaning: the brute force on the formula as
// sent and Eval on its canonical form give the same value, in three values
// and in both modes, so the same Resolve.
func TestQ12SimplificationKeepsTheMeaning(t *testing.T) {
	t.Parallel()
	r := querytest.New(12)
	n, changed := 0, 0
	for i := range generatedFormulas {
		f := r.Formula(5, 4)
		if i%2 == 1 {
			f = r.Respell(f) // as a vendor might spell it: repeated, reordered, in other cases and forms
		}
		c := canonOf(t, f)
		if !sameShape(f, c.Formula) {
			changed++
		}
		for range messagesPerA {
			m, a := r.Message(), r.Answers()
			for _, mode := range []query.Mode{query.ForQuery, query.ForRule} {
				want := querytest.Eval([]contracts.Formula{f}, m, a, mode)
				got := query.Eval([]*query.Canonical{c}, m, a, mode).Value
				n++
				if got != want || query.Resolve(got, mode) != query.Resolve(want, mode) {
					b, _ := json.Marshal(f)
					mb, _ := json.Marshal(m)
					t.Fatalf("mode %d\nformula %s\ncanonical %s\nmessage %s\nanswers %v\nEval %s, brute force %s", mode, b, jsonOf(c), mb, a, name(got), name(want))
				}
			}
		}
	}
	t.Logf("%d evaluations of %d formulas, %d changed by canonicalising", n, generatedFormulas, changed)
	if changed < generatedFormulas/4 {
		t.Errorf("only %d of %d formulas were rewritten: the generator meets too few rewrites", changed, generatedFormulas)
	}
}

// sameShape reports whether canonicalising changed nothing but values and
// order: as many groups and conditions.
func sameShape(f, c contracts.Formula) bool {
	if len(f.All) != len(c.All) {
		return false
	}
	n, m := 0, 0
	for i := range f.All {
		n += len(f.All[i].Any)
		m += len(c.All[i].Any)
	}
	return n == m
}

// Q13: one form for every order: a generated formula with its conditions
// and groups shuffled, repeated and its values respelled gives the same
// bytes and identifier.
func TestQ13OneFormForEveryOrderAndSpelling(t *testing.T) {
	t.Parallel()
	r := querytest.New(13)
	for range generatedFormulas {
		f := r.Formula(5, 4)
		c := canonOf(t, f)
		for range 3 {
			g := r.Respell(f)
			d := canonOf(t, g)
			if jsonOf(d) != jsonOf(c) || d.ID != c.ID {
				a, _ := json.Marshal(f)
				b, _ := json.Marshal(g)
				t.Fatalf("\nformula %s\nrespelled %s\n gives %s\n  and %s", a, b, jsonOf(c), jsonOf(d))
			}
		}
	}
}

// Q15: three values, not two.
func TestQ15ThreeValuesNotTwo(t *testing.T) {
	keep := []string{
		`{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"x"},{"var":"subject","op":"contains","value":"x","not":true}]}]}`,
		`{"v":1,"all":[{"any":[{"var":"message","op":"about","value":"x"}]},{"any":[{"var":"message","op":"about","value":"x","not":true}]}]}`,
		`{"v":1,"all":[{"any":[{"var":"size","op":"at_least","value":"10"}]},{"any":[{"var":"size","op":"below","value":"5"}]}]}`,
	}
	for _, s := range keep {
		c := canon(t, s)
		if c.Never || len(c.Formula.All) == 0 || jsonOf(c) != jsonOf(canon(t, s)) {
			t.Errorf("%s became %s", s, jsonOf(c))
		}
		var f contracts.Formula
		_ = json.Unmarshal([]byte(s), &f)
		if !sameShape(f, c.Formula) {
			t.Errorf("%s was rewritten to %s", s, jsonOf(c))
		}
	}
	// x or not x is Unknown on a message whose subject cannot be read.
	m := full()
	m.Unparsed = true
	if v := eval(t, canon(t, keep[0]), m); v != query.Unknown {
		t.Errorf("x or not x on an unparsed message: %s", name(v))
	}
	// about x and not about x is Unknown without an answer, not False.
	if v := eval(t, canon(t, keep[1]), full()); v != query.Unknown {
		t.Errorf("about and not about without an answer: %s", name(v))
	}
	// size at_least 10 and below 5: Unknown without a recorded size, False with one.
	if v := eval(t, canon(t, keep[2]), full()); v != query.Unknown {
		t.Errorf("an impossible size range without a size: %s", name(v))
	}
	m = full()
	m.Size, m.SizeKnown = 7, true
	if v := eval(t, canon(t, keep[2]), m); v != query.False {
		t.Errorf("an impossible size range with a size: %s", name(v))
	}
	// mailbox is m or not mailbox is m goes.
	if c := canon(t, `{"v":1,"all":[{"any":[{"var":"mailbox","op":"is","value":"mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa"},{"var":"mailbox","op":"is","value":"mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa","not":true}]}]}`); len(c.Formula.All) != 0 {
		t.Errorf("m or not m: %s", jsonOf(c))
	}
	// since s and before t with t ≤ s is "never": Canonicalise returns it, Parse refuses it.
	never := `{"v":1,"all":[{"any":[{"var":"received","op":"since","value":"2026-02-01T00:00:00Z"}]},{"any":[{"var":"received","op":"before","value":"2026-02-01T00:00:00Z"}]}]}`
	var f contracts.Formula
	_ = json.Unmarshal([]byte(never), &f)
	if c := canonOf(t, f); !c.Never || eval(t, c, full()) != query.False {
		t.Errorf("since s and before s: %s", jsonOf(c))
	}
	refused(t, []byte(never), nil, "", contracts.FormulaNeverMatches)
	// not size at_least n is size below n, with the same value on every generated message.
	r := querytest.New(15)
	a := canon(t, `{"v":1,"all":[{"any":[{"var":"size","op":"at_least","value":"1000","not":true}]}]}`)
	b := canon(t, `{"v":1,"all":[{"any":[{"var":"size","op":"below","value":"1000"}]}]}`)
	if jsonOf(a) != jsonOf(b) {
		t.Errorf("not at_least: %s", jsonOf(a))
	}
	notAtLeast := one(contracts.VarSize, contracts.OpAtLeast, "1000", true)
	for range 2000 {
		m := r.Message()
		if querytest.Eval([]contracts.Formula{notAtLeast}, m, nil, query.ForQuery) != eval(t, b, m) {
			t.Fatalf("not at_least and below differ on %+v", m)
		}
	}
}
