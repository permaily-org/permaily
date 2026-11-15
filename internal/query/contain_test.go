package query_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/query/querytest"
)

// f writes a formula in a short form for the tables below: groups split by
// " AND ", conditions by " OR ", a condition "[!]var op [value]".
func f(t testing.TB, s string) *query.Canonical {
	t.Helper()
	out := contracts.Formula{V: 1, All: []contracts.Group{}}
	if s == "never" {
		return canonOf(t, contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{}}}})
	}
	if s != "" {
		for _, g := range strings.Split(s, " AND ") {
			var grp contracts.Group
			for _, c := range strings.Split(g, " OR ") {
				grp.Any = append(grp.Any, cnd(c))
			}
			out.All = append(out.All, grp)
		}
	}
	return canonOf(t, out)
}

func cnd(s string) contracts.Condition {
	c := contracts.Condition{}
	if strings.HasPrefix(s, "!") {
		c.Not, s = true, s[1:]
	}
	parts := strings.SplitN(s, " ", 3)
	c.Var, c.Op = contracts.Variable(parts[0]), contracts.Operator(parts[1])
	if len(parts) == 3 {
		c.Value = parts[2]
	}
	return c
}

const (
	mb1 = "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	mb2 = "mbx_bbbbbbbbbbbbbbbbbbbbbbbbbb"
	jan = "2026-01-01T00:00:00Z"
	feb = "2026-02-01T00:00:00Z"
)

// Q16 of ADR 0027: yes and no per kind of variable. Contains(outer, inner).
func TestQ16ContainmentTable(t *testing.T) {
	for _, c := range []struct {
		outer, inner string
		want         bool
	}{
		// Addresses in domains, subdomains.
		{"from_domain is example.com", "from_address is a@example.com", true},
		{"from_domain is example.com", "from_address is a@sub.example.com", true},
		{"from_domain is sub.example.com", "from_address is a@example.com", false},
		{"from_address is a@example.com", "from_domain is example.com", false},
		{"from_domain is example.com", "from_domain is sub.example.com", true},
		{"from_domain is sub.example.com", "from_domain is example.com", false},
		{"from_domain is example.com", "from_domain is badexample.com", false},
		{"recipient_domain is example.com", "recipient_address is a@example.com", true},
		{"recipient_domain is example.com", "from_address is a@example.com", false},
		{"signed_by is example.com", "signed_by is mail.example.com", true},
		{"signed_by is example.com", "from_domain is example.com", false},
		{"!from_address is a@example.com", "!from_domain is example.com", true},
		{"!from_domain is example.com", "!from_address is a@example.com", false},
		// Texts in texts, as substrings, each way (the ADR's cases).
		{"subject contains invoice", "subject contains invoices", true},
		{"subject contains invoices", "subject contains invoice", false},
		{"subject contains car", "subject contains scar", true},
		{"subject contains scar", "subject contains car", false},
		{"!subject contains scar", "!subject contains car", true},
		{"!subject contains car", "!subject contains scar", false},
		{"body contains invoice", "body contains invoice 2026", true},
		{"body contains e 2", "body contains invoice 2026", true},
		{"body contains invoice", "subject contains invoice", false},
		{"attachments exists", "attachment_name contains x.pdf", true},
		{"attachment_name contains .pdf", "attachment_name contains x.pdf", true},
		{"attachment_name contains x.pdf", "attachments exists", false},
		// Types in T/*.
		{"attachment_type is image/*", "attachment_type is image/png", true},
		{"attachment_type is image/png", "attachment_type is image/*", false},
		{"attachment_type is image/*", "attachment_type is application/pdf", false},
		{"attachments exists", "attachment_type is image/png", true},
		// Lists.
		{"list exists", "list is news.example.com", true},
		{"list is news.example.com", "list exists", false},
		{"list is news.example.com", "list is other.example.com", false},
		// Ranges.
		{"received since " + jan, "received since " + feb, true},
		{"received since " + feb, "received since " + jan, false},
		{"received before " + feb, "received before " + jan, true},
		{"received before " + jan, "received before " + feb, false},
		{"received since " + jan, "received since " + jan + " AND received before " + feb, true},
		{"size at_least 10", "size at_least 20", true},
		{"size below 10", "size below 20", false},
		{"size at_least 20", "!size below 20", true},
		// Mailboxes.
		{"mailbox is " + mb1, "mailbox is " + mb1, true},
		{"mailbox is " + mb1, "mailbox is " + mb2, false},
		{"mailbox is " + mb1 + " OR mailbox is " + mb2, "mailbox is " + mb2, true},
		{"mailbox is " + mb2, "!mailbox is " + mb1, false},
		{"!mailbox is " + mb2, "mailbox is " + mb1, false}, // true, but not in the table: a known gap
		// Identical topics; different topics, about and mentions.
		{"message about invoices", "message about Invoices", true},
		{"message about invoices", "message about invoices AND received since " + jan, true},
		{"message about bills", "message about invoices", false},
		{"message mentions invoices", "message about invoices", false},
		{"message about invoices", "subject about invoices", false},
		{"subject contains invoice", "subject about invoice", false},
		// Several groups of B needed: no.
		{"subject contains x", "subject contains x OR body contains y AND !body contains y", false},
		// Groups: every group of A implied by one of B.
		{"subject contains x AND body contains y", "subject contains x AND body contains y AND list exists", true},
		{"subject contains x OR body contains y", "subject contains x", true},
		{"subject contains x", "subject contains x OR body contains y", false},
		// Every message and never.
		{"", "subject contains x", true},
		{"", "", true},
		{"subject contains x", "", false},
		{"subject contains x", "never", true},
		{"never", "never", true},
		{"never", "subject contains x", false},
		{"never", "", false},
		{"", "never", true},
	} {
		if got := query.Contains(f(t, c.outer), f(t, c.inner)); got != c.want {
			t.Errorf("Contains(%q, %q) = %v, want %v", c.outer, c.inner, got, c.want)
		}
	}
	// Nothing proves anything with a nil formula, or one built by hand.
	a := f(t, "subject contains x")
	if query.Contains(nil, a) || query.Contains(a, nil) {
		t.Error("Contains with nil")
	}
	hand := &query.Canonical{Formula: contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{{Var: "subject", Op: "contains", Value: "X"}}}}}}
	if query.Contains(hand, a) || query.Contains(a, hand) {
		t.Error("Contains proved something with a formula that is not canonical")
	}
}

// Q17: sound on generated formulas built so that "yes" is common: whenever
// Contains(A, B), Eval([B]) ≤ Eval([A]) on every generated message, in three
// values and both modes, with one Answers per message.
func TestQ17ContainmentIsSoundOnGeneratedFormulas(t *testing.T) {
	r := querytest.New(17)
	const pairs, messages = 2000, 25
	yes, strengthened := 0, 0
	for i := range pairs {
		fa := r.Formula(4, 3)
		var fb contracts.Formula
		if i%4 == 3 {
			fb = r.Formula(4, 3) // and unrelated pairs, now and then
		} else {
			fb = r.Stronger(fa)
			strengthened++
		}
		a, b := canonOf(t, fa), canonOf(t, fb)
		if !query.Contains(a, b) {
			continue
		}
		yes++
		for range messages {
			m, ans := r.Message(), r.Answers()
			for _, mode := range []query.Mode{query.ForQuery, query.ForRule} {
				va := query.Eval([]*query.Canonical{a}, m, ans, mode).Value
				vb := query.Eval([]*query.Canonical{b}, m, ans, mode).Value
				ba := querytest.Eval([]contracts.Formula{fa}, m, ans, mode)
				bb := querytest.Eval([]contracts.Formula{fb}, m, ans, mode)
				if vb > va || bb > ba {
					mj, _ := json.Marshal(m)
					t.Fatalf("Contains(A, B) but B > A\nA %s\nB %s\nmessage %s\nanswers %v\nEval A %s, B %s; brute A %s, B %s", jsonOf(a), jsonOf(b), mj, ans, name(va), name(vb), name(ba), name(bb))
				}
			}
		}
	}
	t.Logf("%d pairs (%d strengthened), %d proven contained, each checked on %d messages in both modes", pairs, strengthened, yes, messages)
	if yes < strengthened/3 {
		t.Errorf("only %d of %d strengthened pairs proven: the generator meets too few", yes, strengthened)
	}
}

// A small universe for Q18: positive literals, every message over them, and
// each message's answers.
type universe struct {
	name     string
	literals []contracts.Condition
	messages []*query.Message
	answers  []query.Answers
}

func (u *universe) add(m *query.Message, a query.Answers) {
	m.Have |= query.FromItem | query.FromMeta
	u.messages = append(u.messages, m)
	u.answers = append(u.answers, a)
}

func subsets[T any](s []T) [][]T {
	var out [][]T
	for mask := range 1 << len(s) {
		var x []T
		for i := range s {
			if mask&(1<<i) != 0 {
				x = append(x, s[i])
			}
		}
		out = append(out, x)
	}
	return out
}

func universes() []*universe {
	t1, t2 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	c := func(v contracts.Variable, op contracts.Operator, value string) contracts.Condition {
		return contracts.Condition{Var: v, Op: op, Value: value}
	}
	var us []*universe

	addr := &universe{name: "addresses and domains", literals: []contracts.Condition{
		c("from_address", "is", "alice@example.com"), c("from_address", "is", "bob@sub.example.com"),
		c("from_domain", "is", "example.com"), c("from_domain", "is", "sub.example.com"), c("from_domain", "is", "other.example"),
	}}
	for _, from := range subsets([]string{"alice@example.com", "bob@sub.example.com", "carol@other.example", "bad local@example.com"}) {
		for _, partial := range []bool{false, true} {
			for _, unparsed := range []bool{false, true} {
				addr.add(&query.Message{From: from, Partial: partial, Unparsed: unparsed}, nil)
			}
		}
	}
	us = append(us, addr)

	sign := &universe{name: "signers and recipients", literals: []contracts.Condition{
		c("signed_by", "is", "example.com"), c("signed_by", "is", "sub.example.com"),
		c("recipient_address", "is", "alice@example.com"), c("recipient_domain", "is", "example.com"), c("from_domain", "is", "example.com"),
	}}
	for _, sb := range subsets([]string{"example.com", "sub.example.com", "bad_domain!"}) {
		for _, rc := range subsets([]string{"alice@example.com", "bad local@example.com"}) {
			for _, from := range subsets([]string{"alice@example.com"}) {
				sign.add(&query.Message{SignedBy: sb, Recipients: rc, From: from}, nil)
			}
		}
	}
	us = append(us, sign)

	text := &universe{name: "texts and attachments", literals: []contracts.Condition{
		c("subject", "contains", "car"), c("subject", "contains", "scar"),
		c("attachment_name", "contains", "car"), c("attachment_name", "contains", "scar"), c("attachments", "exists", ""),
	}}
	for _, subject := range []string{"car", "scar", "careful", "nothing"} {
		for _, names := range [][]string{nil, {"car"}, {"scar"}, {"notes"}} {
			for _, partial := range []bool{false, true} {
				m := &query.Message{Subject: subject, Partial: partial}
				for _, n := range names {
					m.Attachments = append(m.Attachments, query.Attachment{Name: n, Type: "text/plain"})
				}
				text.add(m, nil)
			}
		}
	}
	text.add(&query.Message{Unparsed: true}, nil)
	us = append(us, text)

	tm := &universe{name: "times and mailboxes", literals: []contracts.Condition{
		c("received", "since", t1.Format(time.RFC3339)), c("received", "before", t2.Format(time.RFC3339)),
		c("received", "since", t2.Format(time.RFC3339)), c("mailbox", "is", mb1), c("mailbox", "is", mb2),
	}}
	for _, at := range []time.Time{t1.Add(-time.Nanosecond), t1, t1.Add(24 * time.Hour), t2, t2.Add(time.Hour)} {
		for _, mbx := range []string{mb1, mb2, "mbx_cccccccccccccccccccccccccc"} {
			tm.add(&query.Message{Received: at, Mailbox: mbx}, nil)
		}
	}
	us = append(us, tm)

	tls := &universe{name: "types, lists and sizes", literals: []contracts.Condition{
		c("attachment_type", "is", "application/pdf"), c("attachment_type", "is", "application/*"),
		c("list", "exists", ""), c("list", "is", "news.example.com"), c("size", "at_least", "1000"),
	}}
	for _, types := range [][]string{nil, {"application/pdf"}, {"application/zip"}, {"image/png"}, {"foo/bar"}} {
		for _, partial := range []bool{false, true} {
			for li, list := range [][]string{nil, nil, {"news.example.com"}, {"deals.example.com"}} {
				for _, size := range []int64{-1, 999, 1000} {
					m := &query.Message{Partial: partial, List: list, ListRecorded: li > 0, Size: size, SizeKnown: size >= 0}
					for _, ty := range types {
						m.Attachments = append(m.Attachments, query.Attachment{Name: "x", Type: ty})
					}
					tls.add(m, nil)
				}
			}
		}
	}
	us = append(us, tls)

	model := &universe{name: "a model's conditions and the body", literals: []contracts.Condition{
		c("message", "about", "invoices"), c("message", "mentions", "invoices"), c("subject", "about", "invoices"),
		c("body", "contains", "car"), c("body", "contains", "scar"),
	}}
	atoms := []query.Atom{{Var: "message", Op: "about", Topic: "invoices"}, {Var: "message", Op: "mentions", Topic: "invoices"}, {Var: "subject", Op: "about", Topic: "invoices"}}
	probabilities := []float64{-1, 0.1, 0.5, 0.9}
	bodies := []query.Message{
		{Have: query.FromEntry, Body: "scar"}, {Have: query.FromEntry, Body: "x"},
		{Have: query.FromEntry, Body: "x", BodyCut: true}, {}, {Have: query.FromEntry | query.FromText, Body: "car", BodyCut: true},
	}
	for i := range len(probabilities) * len(probabilities) * len(probabilities) {
		a := query.Answers{}
		for k, atom := range atoms {
			if p := probabilities[(i>>(2*k))&3]; p >= 0 {
				a[atom] = p
			}
		}
		for _, b := range bodies {
			m := b
			model.add(&m, a)
		}
	}
	us = append(us, model)
	return us
}

// listed is the implication table of ADR 0027 written again here, as the
// oracle of Q18's second half: a ⇒ b for two canonical conditions.
func listed(a, b contracts.Condition) bool {
	if a.Not != b.Not {
		return false
	}
	if a.Not {
		a, b = b, a
	}
	a.Not, b.Not = false, false
	if a == b {
		return true
	}
	under := func(d, p string) bool { return d == p || strings.HasSuffix(d, "."+p) }
	domainOf := func(addr string) string { return addr[strings.LastIndexByte(addr, '@')+1:] }
	switch {
	case a.Var == "from_address" && b.Var == "from_domain", a.Var == "recipient_address" && b.Var == "recipient_domain":
		return under(domainOf(a.Value), b.Value)
	case a.Var == b.Var && (a.Var == "from_domain" || a.Var == "recipient_domain" || a.Var == "signed_by"):
		return under(a.Value, b.Value)
	case a.Var == b.Var && a.Op == "contains" && b.Op == "contains":
		return strings.Contains(a.Value, b.Value)
	case (a.Var == "attachment_name" || a.Var == "attachment_type") && b.Var == "attachments":
		return true
	case a.Var == "attachment_type" && b.Var == "attachment_type":
		return strings.HasSuffix(b.Value, "/*") && strings.HasPrefix(a.Value, strings.TrimSuffix(b.Value, "*"))
	case a.Var == "list" && a.Op == "is" && b.Var == "list" && b.Op == "exists":
		return true
	case a.Var == b.Var && a.Op == b.Op && a.Var == "received":
		ta, _ := time.Parse(time.RFC3339, a.Value)
		tb, _ := time.Parse(time.RFC3339, b.Value)
		return (a.Op == "since" && !ta.Before(tb)) || (a.Op == "before" && !ta.After(tb))
	}
	return false
}

// valueSet is a formula's values over a universe's messages in both modes,
// as two sets: where it is True, and where it is not False. B ≤ A on every
// message exactly when both of B's sets are inside A's.
type valueSet struct{ isTrue, notFalse []uint64 }

func (b valueSet) inside(a valueSet) bool {
	for i := range b.isTrue {
		if b.isTrue[i]&^a.isTrue[i] != 0 || b.notFalse[i]&^a.notFalse[i] != 0 {
			return false
		}
	}
	return true
}

// Q18: sound by brute force. Over universes small enough to list every
// message (every reason for Unknown included), for every pair of formulas of
// up to two groups of two conditions over each universe's literals and
// their negations, Contains yes implies containment over every message, in
// three values and both modes. And for two formulas of one condition each,
// Contains is yes exactly when the implication table lists the relation; a
// containment the brute force finds and the table does not list is a known
// gap, logged, not a failure.
func TestQ18ContainmentIsSoundByBruteForce(t *testing.T) {
	r := querytest.New(18)
	total, proven, contained := 0, 0, 0
	for _, u := range universes() {
		var lits []contracts.Condition
		for _, l := range u.literals {
			n := l
			n.Not = true
			lits = append(lits, l, n)
		}
		// Each literal's value on each message and mode, by brute force.
		modes := []query.Mode{query.ForQuery, query.ForRule}
		points := len(u.messages) * len(modes)
		litValue := make([][]query.Value, len(lits))
		for i, l := range lits {
			litValue[i] = make([]query.Value, points)
			for j, m := range u.messages {
				for k, mode := range modes {
					litValue[i][j*len(modes)+k] = querytest.Condition(l, m, u.answers[j], mode)
				}
			}
		}
		// Every group of one or two literals, every formula of up to two groups.
		var groups [][]int
		for i := range lits {
			groups = append(groups, []int{i})
			for j := i + 1; j < len(lits); j++ {
				groups = append(groups, []int{i, j})
			}
		}
		formulas := [][][]int{{}}
		for i := range groups {
			formulas = append(formulas, [][]int{groups[i]})
			for j := i + 1; j < len(groups); j++ {
				formulas = append(formulas, [][]int{groups[i], groups[j]})
			}
		}
		type entry struct {
			c        *query.Canonical
			compiled *query.Compiled
			values   valueSet
		}
		byID := map[string]bool{}
		var entries []entry
		words := (points + 63) / 64
		for _, fm := range formulas {
			sent := contracts.Formula{V: 1, All: []contracts.Group{}}
			vs := valueSet{make([]uint64, words), make([]uint64, words)}
			for p := range points {
				v := query.True
				for _, g := range fm {
					gv := query.False
					for _, li := range g {
						gv = max(gv, litValue[li][p])
					}
					v = min(v, gv)
				}
				if v == query.True {
					vs.isTrue[p/64] |= 1 << (p % 64)
				}
				if v != query.False {
					vs.notFalse[p/64] |= 1 << (p % 64)
				}
			}
			for _, g := range fm {
				var grp contracts.Group
				for _, li := range g {
					grp.Any = append(grp.Any, lits[li])
				}
				sent.All = append(sent.All, grp)
			}
			c := canonOf(t, sent)
			if byID[c.ID] {
				continue
			}
			byID[c.ID] = true
			compiled, ok := query.Compile(c)
			if !ok {
				t.Fatalf("a canonical form does not compile: %s", jsonOf(c))
			}
			entries = append(entries, entry{c, compiled, vs})
		}
		uProven, uContained := 0, 0
		var gaps []string
		for _, a := range entries {
			for _, b := range entries {
				total++
				yes := query.ContainsCompiled(a.compiled, b.compiled)
				in := b.values.inside(a.values)
				if in {
					uContained++
				}
				if !yes {
					if in && len(a.c.Formula.All) == 1 && len(a.c.Formula.All[0].Any) == 1 && len(b.c.Formula.All) == 1 && len(b.c.Formula.All[0].Any) == 1 {
						ca, cb := a.c.Formula.All[0].Any[0], b.c.Formula.All[0].Any[0]
						if listed(cb, ca) {
							t.Errorf("%s: the table lists %s ⇒ %s, and Contains says no", u.name, jsonOf(b.c), jsonOf(a.c))
						}
						gaps = append(gaps, jsonOf(b.c)+" inside "+jsonOf(a.c))
					}
					continue
				}
				uProven++
				if !in {
					t.Fatalf("%s: Contains(A, B) yes, but B > A on a message\nA %s\nB %s", u.name, jsonOf(a.c), jsonOf(b.c))
				}
				if len(a.c.Formula.All) == 1 && len(a.c.Formula.All[0].Any) == 1 && len(b.c.Formula.All) == 1 && len(b.c.Formula.All[0].Any) == 1 {
					if ca, cb := a.c.Formula.All[0].Any[0], b.c.Formula.All[0].Any[0]; !listed(cb, ca) {
						t.Errorf("%s: Contains proved %s inside %s, which the table does not list", u.name, jsonOf(b.c), jsonOf(a.c))
					}
				}
			}
		}
		// The public Contains gives what the compiled proof gives, on a sample.
		for range 2000 {
			a, b := entries[r.IntN(len(entries))], entries[r.IntN(len(entries))]
			if query.Contains(a.c, b.c) != query.ContainsCompiled(a.compiled, b.compiled) {
				t.Fatalf("Contains and the compiled proof differ on %s, %s", jsonOf(a.c), jsonOf(b.c))
			}
		}
		proven += uProven
		contained += uContained
		t.Logf("%s: %d messages, %d distinct formulas, %d pairs, %d contained by brute force, %d proven; known gaps between single conditions: %d", u.name, len(u.messages), len(entries), len(entries)*len(entries), uContained, uProven, len(gaps))
		for _, g := range gaps {
			t.Logf("  known gap: %s", g)
		}
	}
	t.Logf("in all: %d pairs, %d contained by brute force, %d proven, none proven that is not", total, contained, proven)
}

// Q46: canonical forms are not minimal. C means the same as Y, keeps its two
// groups, Y fits inside C and C does not fit inside Y: consent may be asked
// again, never skipped.
func TestQ46CanonicalFormsAreNotMinimal(t *testing.T) {
	c := f(t, "mailbox is "+mb1+" OR subject contains x AND !mailbox is "+mb1+" OR subject contains x")
	y := f(t, "subject contains x")
	if len(c.Formula.All) != 2 {
		t.Fatalf("C became %s", jsonOf(c))
	}
	if !query.Contains(c, y) {
		t.Error("Y does not fit inside C")
	}
	if query.Contains(y, c) {
		t.Error("C fits inside Y")
	}
	// They mean the same on every generated message.
	r := querytest.New(46)
	for range 3000 {
		m := r.Message()
		m.Mailbox = []string{mb1, mb2}[r.IntN(2)]
		if a, b := eval(t, c, m), eval(t, y, m); a != b {
			t.Fatalf("C %s and Y %s differ", name(a), name(b))
		}
	}
}
