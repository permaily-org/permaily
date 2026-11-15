package query_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/query/querytest"
)

// msg is a message whose item, meta and entry were read, changed by fs.
func msg(fs ...func(*query.Message)) *query.Message {
	m := full()
	m.Mailbox = mb1
	m.Received = time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	for _, f := range fs {
		f(m)
	}
	return m
}

var (
	partial  = func(m *query.Message) { m.Partial = true }
	unparsed = func(m *query.Message) { m.Unparsed = true }
	noMeta   = func(m *query.Message) { m.Have &^= query.FromMeta }
	noItem   = func(m *query.Message) { m.Have &^= query.FromItem }
	noEntry  = func(m *query.Message) { m.Have &^= query.FromEntry }
	cut      = func(m *query.Message) { m.BodyCut = true }
	withText = func(m *query.Message) { m.Have |= query.FromText }
	format1  = func(m *query.Message) { m.ListRecorded, m.SizeKnown = false, false }
	from     = func(a ...string) func(*query.Message) { return func(m *query.Message) { m.From = a } }
	to       = func(a ...string) func(*query.Message) { return func(m *query.Message) { m.Recipients = a } }
	signed   = func(d ...string) func(*query.Message) { return func(m *query.Message) { m.SignedBy = d } }
	subject  = func(s string) func(*query.Message) { return func(m *query.Message) { m.Subject = s } }
	body     = func(s string) func(*query.Message) { return func(m *query.Message) { m.Body = s } }
	size     = func(n int64) func(*query.Message) { return func(m *query.Message) { m.Size, m.SizeKnown = n, true } }
	list     = func(l ...string) func(*query.Message) { return func(m *query.Message) { m.List = l } }
	attach   = func(nameType ...string) func(*query.Message) {
		return func(m *query.Message) {
			for i := 0; i+1 < len(nameType); i += 2 {
				m.Attachments = append(m.Attachments, query.Attachment{Name: nameType[i], Type: nameType[i+1]})
			}
		}
	}
)

// Q26 of ADR 0027: Eval per variable, true, false and each reason for
// Unknown, with Result.Needs naming the source that would settle it and
// Result.Model its atoms.
func TestQ26EvalPerVariable(t *testing.T) {
	inv := query.Atom{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: "invoices"}
	for _, c := range []struct {
		name    string
		formula string
		m       *query.Message
		want    query.Value
		needs   query.Sources
		model   []query.Atom
	}{
		{"no meta read", "subject contains x", msg(noMeta), query.Unknown, query.FromItem | query.FromMeta, nil},
		{"no item read", "received since " + jan, msg(noItem), query.Unknown, query.FromItem | query.FromMeta, nil},
		{"no meta, every message", "", msg(noMeta), query.Unknown, query.FromItem | query.FromMeta, nil},
		{"every message", "", msg(), query.True, 0, nil},
		{"never", "never", msg(), query.False, 0, nil},
		{"a confirmation is never listed", "", msg(func(m *query.Message) { m.Confirmation = true }), query.False, 0, nil},

		{"from_address true", "from_address is alice@example.com", msg(from("Alice@EXAMPLE.com")), query.True, 0, nil},
		{"from_address false", "from_address is alice@example.com", msg(from("bob@example.com")), query.False, 0, nil},
		{"from_address partial", "from_address is alice@example.com", msg(from("bob@example.com"), partial), query.Unknown, 0, nil},
		{"from_address true though partial", "from_address is alice@example.com", msg(from("alice@example.com"), partial), query.True, 0, nil},
		{"from_address not valid", "from_address is alice@example.com", msg(from("bad local@example.com")), query.Unknown, 0, nil},
		{"from_address unparsed", "from_address is alice@example.com", msg(from("alice@example.com"), unparsed), query.Unknown, 0, nil},
		{"from_address with a quoted local part", "from_address is alice@example.com", msg(from(`"alice"@example.com`)), query.True, 0, nil},
		{"from_domain under it", "from_domain is example.com", msg(from("a@mail.example.com")), query.True, 0, nil},
		{"from_domain not a suffix of a label", "from_domain is example.com", msg(from("a@badexample.com")), query.False, 0, nil},
		{"from_domain Unicode", "from_domain is xn--bcher-kva.example", msg(from("a@Bücher.example")), query.True, 0, nil},
		{"from_domain of an address not valid", "from_domain is other.example", msg(from(`bad local@other.example`)), query.Unknown, 0, nil},
		{"from_domain of a domain not valid", "!from_domain is example.com", msg(from("a@exa_mple.com")), query.Unknown, 0, nil},
		{"recipient_address true", "recipient_address is alice@example.com", msg(to("x@y.example", "alice@example.com")), query.True, 0, nil},
		{"recipient_domain false", "recipient_domain is example.com", msg(to("x@y.example")), query.False, 0, nil},
		{"recipient_domain partial", "recipient_domain is example.com", msg(to("x@y.example"), partial), query.Unknown, 0, nil},
		{"signed_by true", "signed_by is example.com", msg(signed("Mail.Example.com.")), query.True, 0, nil},
		{"signed_by false", "signed_by is example.com", msg(signed("other.example")), query.False, 0, nil},
		{"signed_by none", "signed_by is example.com", msg(), query.False, 0, nil},
		{"signed_by a domain that does not normalise", "signed_by is example.com", msg(signed("bad_domain!")), query.Unknown, 0, nil},
		{"signed_by unparsed", "signed_by is example.com", msg(signed("example.com"), unparsed), query.Unknown, 0, nil},

		{"subject true", "subject contains invoice", msg(subject("Your INVOICES")), query.True, 0, nil},
		{"subject false", "subject contains invoice", msg(subject("hello")), query.False, 0, nil},
		{"subject not false when unparsed", "!subject contains invoice", msg(subject("hello"), unparsed), query.Unknown, 0, nil},
		{"subject invalid UTF-8 becomes U+FFFD", "subject contains ab", msg(subject("a\xffb")), query.False, 0, nil},

		{"body from the entry", "body contains invoice", msg(body("an invoice")), query.True, 0, nil},
		{"body false from the entry", "body contains invoice", msg(body("hello")), query.False, 0, nil},
		{"body without the entry", "body contains invoice", msg(noEntry), query.Unknown, query.FromEntry, nil},
		{"body cut", "body contains invoice", msg(body("an invoice"), cut), query.Unknown, query.FromText, nil},
		{"body cut, negated", "!body contains invoice", msg(body("hello"), cut), query.Unknown, query.FromText, nil},
		{"body from the raw message", "body contains invoice", msg(body("an invoice"), cut, withText), query.True, 0, nil},
		{"body from the raw message without the entry", "body contains invoice", msg(body("x"), noEntry, withText), query.False, 0, nil},
		{"body unparsed", "body contains invoice", msg(unparsed), query.Unknown, 0, nil},

		{"attachments", "attachments exists", msg(attach("a", "text/plain")), query.True, 0, nil},
		{"no attachments", "attachments exists", msg(), query.False, 0, nil},
		{"no attachments, partial", "attachments exists", msg(partial), query.Unknown, 0, nil},
		{"attachment name", "attachment_name contains .pdf", msg(attach("Bill.PDF", "application/pdf")), query.True, 0, nil},
		{"attachment name false", "attachment_name contains .pdf", msg(attach("a.txt", "text/plain")), query.False, 0, nil},
		{"attachment name partial", "attachment_name contains .pdf", msg(attach("a.txt", "text/plain"), partial), query.Unknown, 0, nil},
		{"attachment type", "attachment_type is application/pdf", msg(attach("x", "Application/PDF; name=x")), query.True, 0, nil},
		{"attachment type T/*", "attachment_type is image/*", msg(attach("x", "image/png")), query.True, 0, nil},
		{"attachment type T/* of another type", "attachment_type is image/*", msg(attach("x", "application/pdf")), query.False, 0, nil},
		{"attachment type malformed", "attachment_type is application/*", msg(attach("x", "application/x y")), query.False, 0, nil},
		{"attachment type unlisted equal", "attachment_type is application/pdf", msg(attach("x", "application/pdf")), query.True, 0, nil},
		{"attachment type partial", "attachment_type is image/*", msg(attach("x", "text/plain"), partial), query.Unknown, 0, nil},

		{"list", "list is news.example.com", msg(list("News.Example.COM")), query.True, 0, nil},
		{"another list", "list is news.example.com", msg(list("deals.example.com")), query.False, 0, nil},
		{"list exists", "list exists", msg(list("x.example")), query.True, 0, nil},
		{"no list", "list exists", msg(), query.False, 0, nil},
		{"list of format 1", "list exists", msg(format1), query.Unknown, 0, nil},
		{"list unparsed", "list exists", msg(unparsed), query.Unknown, 0, nil},

		{"received since, equal", "received since 2026-01-15T00:00:00Z", msg(), query.True, 0, nil},
		{"received since, a nanosecond before", "received since 2026-01-15T00:00:00Z", msg(func(m *query.Message) { m.Received = m.Received.Add(-1) }), query.False, 0, nil},
		{"received before, equal", "received before 2026-01-15T00:00:00Z", msg(), query.False, 0, nil},
		{"received known though unparsed", "received before 2026-02-01T00:00:00Z", msg(unparsed), query.True, 0, nil},

		{"size at_least", "size at_least 1048576", msg(size(1048576)), query.True, 0, nil},
		{"size below", "size below 2048", msg(size(1024)), query.True, 0, nil},
		{"size below, false", "size below 2048", msg(size(2048)), query.False, 0, nil},
		{"size of format 1", "size at_least 1048576", msg(format1), query.Unknown, 0, nil},
		{"size alone needs no raw read", "size at_least 1048576", msg(format1, cut), query.Unknown, 0, nil},

		{"mailbox", "mailbox is " + mb1, msg(), query.True, 0, nil},
		{"another mailbox", "mailbox is " + mb2, msg(), query.False, 0, nil},
		{"mailbox known though unparsed", "!mailbox is " + mb2, msg(unparsed), query.True, 0, nil},

		{"a model not asked", "message about invoices", msg(), query.Unknown, 0, []query.Atom{inv}},
		{"a model not asked, negated, with a cut body", "!message about invoices OR body contains x", msg(cut), query.Unknown, query.FromText, []query.Atom{inv}},
		{"a group already true needs nothing", "message about invoices OR subject contains x AND body contains y", msg(subject("x"), cut), query.Unknown, query.FromText, nil},
		{"an exact condition settles it", "message about invoices AND subject contains x", msg(subject("y")), query.False, 0, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := query.Eval([]*query.Canonical{f(t, c.formula)}, c.m, nil, query.ForQuery)
			if r.Value != c.want || r.Needs != c.needs || !slices.Equal(r.Model, c.model) {
				t.Errorf("Eval = {%s, needs %b, model %v}, want {%s, needs %b, model %v}", name(r.Value), r.Needs, r.Model, name(c.want), c.needs, c.model)
			}
		})
	}
}

// Eval of nothing, or of a nil formula, is Unknown, and delivers nothing; a
// formula built by hand is Unknown where it is not canonical.
func TestEvalOfNothingDeliversNothing(t *testing.T) {
	if r := query.Eval(nil, msg(), nil, query.ForQuery); r.Value != query.Unknown || query.Resolve(r.Value, query.ForQuery) {
		t.Errorf("Eval of no formula: %s", name(r.Value))
	}
	if r := query.Eval([]*query.Canonical{f(t, ""), nil}, msg(), nil, query.ForQuery); r.Value != query.Unknown {
		t.Errorf("Eval with a nil formula: %s", name(r.Value))
	}
	if r := query.Eval([]*query.Canonical{f(t, "")}, nil, nil, query.ForQuery); r.Value != query.Unknown || r.Needs != query.FromItem|query.FromMeta {
		t.Errorf("Eval of a nil message: %+v", r)
	}
	hand := &query.Canonical{Formula: contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{{Var: "from_domain", Op: "is", Value: "Example.com", Not: true}}}}}}
	if r := query.Eval([]*query.Canonical{hand}, msg(from("a@example.com")), nil, query.ForQuery); r.Value != query.Unknown {
		t.Errorf("a condition that is not canonical: %s", name(r.Value))
	}
	for _, c := range []struct {
		v           query.Value
		mode        query.Mode
		resolve     bool
		description string
	}{
		{query.True, query.ForQuery, true, "a query delivers True"},
		{query.Unknown, query.ForQuery, false, "a query does not deliver Unknown"},
		{query.False, query.ForQuery, false, "a query does not deliver False"},
		{query.True, query.ForRule, true, "a rule applies on True"},
		{query.Unknown, query.ForRule, true, "a rule applies on Unknown"},
		{query.False, query.ForRule, false, "a rule does not apply on False"},
		{query.True, query.Mode(9), false, "no other mode delivers"},
	} {
		if query.Resolve(c.v, c.mode) != c.resolve {
			t.Error(c.description)
		}
	}
}

// Q37: one decision per address. Contains(A, B) is yes, and on a message
// from "bad local"@other.example both are Unknown; from bob@other.example
// both True; from alice@example.com both False; and on generated messages
// from one address (a list not cut), address and domain conditions are
// Unknown together: exactly when the address is not valid.
func TestQ37OneDecisionPerAddress(t *testing.T) {
	a := f(t, "!from_address is alice@example.com")
	b := f(t, "!from_domain is example.com")
	if !query.Contains(a, b) {
		t.Fatal("B does not fit inside A")
	}
	for _, c := range []struct {
		from string
		want query.Value
	}{
		{"bad local@other.example", query.Unknown},
		{`"bad local"@other.example`, query.Unknown},
		{"bob@other.example", query.True},
		{"alice@example.com", query.False},
	} {
		m := msg(from(c.from))
		if va, vb := eval(t, a, m), eval(t, b, m); va != c.want || vb != c.want {
			t.Errorf("from %s: A %s, B %s, want both %s", c.from, name(va), name(vb), name(c.want))
		}
	}
	r := querytest.New(37)
	for range 5000 {
		m := r.Message()
		m.Have |= query.FromItem | query.FromMeta
		m.Confirmation = false
		m.Partial = false // a list that may be cut is Unknown for an absence, which is another reason
		addr := r.Condition()
		addr.Var, addr.Op, addr.Value = contracts.VarFromAddress, contracts.OpIs, querytest.Addresses[r.IntN(len(querytest.Addresses))]
		dom := contracts.Condition{Var: contracts.VarFromDomain, Op: contracts.OpIs, Value: addr.Value[strings.LastIndexByte(addr.Value, '@')+1:]}
		if len(m.From) != 1 {
			continue
		}
		va := eval(t, canonOf(t, contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{addr}}}}), m)
		vd := eval(t, canonOf(t, contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{dom}}}}), m)
		if (va == query.Unknown) != (vd == query.Unknown) {
			t.Fatalf("from %v: the address is %s and its domain %s", m.From, name(va), name(vd))
		}
	}
}

// Q38: one answer per message and atom.
func TestQ38OneAnswerPerMessageAndAtom(t *testing.T) {
	inv := query.Atom{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: "invoices"}
	a := f(t, "message about invoices")
	b := f(t, "message about invoices AND received since "+jan)
	if !query.Contains(a, b) {
		t.Fatal("B does not fit inside A")
	}
	m := msg()
	for _, p := range []float64{0, 0.1, 0.2, 0.21, 0.29, 0.3, 0.5, 0.69, 0.7, 0.79, 0.8, 0.9, 1} {
		ans := query.Answers{inv: p}
		for _, mode := range []query.Mode{query.ForQuery, query.ForRule} {
			va := query.Eval([]*query.Canonical{a}, m, ans, mode).Value
			vb := query.Eval([]*query.Canonical{b}, m, ans, mode).Value
			if vb > va || va != query.Classify(p, mode) {
				t.Errorf("p %v mode %d: A %s, B %s", p, mode, name(va), name(vb))
			}
		}
	}
	// subject about x and message mentions x are two atoms with two answers.
	two := f(t, "subject about x AND message mentions x")
	sub := query.Atom{Var: contracts.VarSubject, Op: contracts.OpAbout, Topic: "x"}
	men := query.Atom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: "x"}
	if r := query.Eval([]*query.Canonical{two}, m, query.Answers{sub: 0.9, men: 0.1}, query.ForQuery); r.Value != query.False {
		t.Errorf("two atoms read one answer: %s", name(r.Value))
	}
	if r := query.Eval([]*query.Canonical{two}, m, query.Answers{sub: 0.9}, query.ForQuery); r.Value != query.Unknown || !slices.Equal(r.Model, []query.Atom{men}) {
		t.Errorf("one missing atom: %+v", r)
	}
	// An atom in two groups, negated in one, reads one answer; a missing
	// answer is listed once, even across formulas.
	both := f(t, "message about invoices OR subject contains q AND !message about invoices OR body contains q")
	if r := query.Eval([]*query.Canonical{both}, m, query.Answers{inv: 0.9}, query.ForQuery); r.Value != query.False {
		t.Errorf("about and not about with one answer of 0.9: %s", name(r.Value))
	}
	if r := query.Eval([]*query.Canonical{both}, m, query.Answers{inv: 0.5}, query.ForRule); r.Value != query.Unknown || len(r.Model) != 0 {
		t.Errorf("an answer in the band of doubt: %+v", r)
	}
	if r := query.Eval([]*query.Canonical{both, a}, m, nil, query.ForQuery); r.Value != query.Unknown || !slices.Equal(r.Model, []query.Atom{inv}) {
		t.Errorf("a missing answer: %+v", r)
	}
	// The topic of the atom is the normalised topic.
	if r := query.Eval([]*query.Canonical{f(t, "message about  INVOICES ")}, m, query.Answers{inv: 1}, query.ForQuery); r.Value != query.True {
		t.Errorf("a topic respelled reads another atom: %s", name(r.Value))
	}
}

// Q39: the consented scope is evaluated on its own.
func TestQ39TheConsentedScopeIsEvaluatedOnItsOwn(t *testing.T) {
	t.Parallel()
	r := querytest.New(39)
	for range 3000 {
		a, b := canonOf(t, r.Formula(3, 3)), canonOf(t, r.Formula(3, 3))
		for range 5 {
			m, ans := r.Message(), r.Answers()
			for _, mode := range []query.Mode{query.ForQuery, query.ForRule} {
				ab := query.Eval([]*query.Canonical{a, b}, m, ans, mode).Value
				va := query.Eval([]*query.Canonical{a}, m, ans, mode).Value
				vb := query.Eval([]*query.Canonical{b}, m, ans, mode).Value
				if ab != min(va, vb) {
					t.Fatalf("Eval([A, B]) %s, A %s, B %s\nA %s\nB %s", name(ab), name(va), name(vb), jsonOf(a), jsonOf(b))
				}
				// With a Contains that says yes to everything, B would be
				// accepted whatever it is: still nothing evaluated with A is
				// True where A is not.
				if ab == query.True && va != query.True {
					t.Fatal("a request accepted wrongly delivers what A does not")
				}
			}
		}
	}
	a := f(t, "!from_address is alice@example.com")
	b := f(t, "!from_domain is example.com")
	m := msg(from(`"bad local"@other.example`))
	if v := query.Eval([]*query.Canonical{a, b}, m, nil, query.ForQuery).Value; v != query.Unknown || query.Resolve(v, query.ForQuery) {
		t.Errorf("A and B on the bad local message: %s", name(v))
	}
}

// Q40: no decision from a cut text.
func TestQ40NoDecisionFromACutText(t *testing.T) {
	invoiceEntry := strings.Repeat(" ", 16377) + "invoice"
	cafeEntry := strings.Repeat("x", 16380) + "cafe"
	for _, c := range []struct {
		formula     string
		entry, full string
		after       query.Value
	}{
		{"body contains invoices", invoiceEntry, invoiceEntry + "s and more", query.True},
		{"!body contains invoices", invoiceEntry, invoiceEntry + "s and more", query.False},
		{"body contains cafe", cafeEntry, cafeEntry + "\u0301 au lait", query.False},
		{"!body contains cafe", cafeEntry, cafeEntry + "\u0301 au lait", query.True},
	} {
		fm := f(t, c.formula)
		before := query.Eval([]*query.Canonical{fm}, msg(body(c.entry), cut), nil, query.ForQuery)
		if before.Value != query.Unknown || before.Needs != query.FromText {
			t.Errorf("%s before the raw message: %+v", c.formula, before)
		}
		after := query.Eval([]*query.Canonical{fm}, msg(body(c.full), cut, withText), nil, query.ForQuery)
		if after.Value != c.after || after.Needs != 0 {
			t.Errorf("%s after the raw message: %+v, want %s", c.formula, after, name(c.after))
		}
	}
}
