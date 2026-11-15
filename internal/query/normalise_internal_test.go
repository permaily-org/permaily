package query

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/contracts"
)

func parseOne(t *testing.T, v contracts.Variable, op contracts.Operator, value string) (*Canonical, *Problem) {
	t.Helper()
	b := `{"v":1,"all":[{"any":[{"var":"` + string(v) + `","op":"` + string(op) + `","value":` + jsonString(value) + `}]}]}`
	c, err := Parse([]byte(b), nil)
	var p *Problem
	if errors.As(err, &p) {
		return nil, p
	}
	return c, nil
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20:
			b.WriteString(`\u00`)
			b.WriteByte("0123456789abcdef"[r>>4])
			b.WriteByte("0123456789abcdef"[r&15])
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func value(c *Canonical) string { return c.Formula.All[0].Any[0].Value }

func accepts(t *testing.T, v contracts.Variable, op contracts.Operator, in, want string) {
	t.Helper()
	c, p := parseOne(t, v, op, in)
	if p != nil {
		t.Errorf("%s %s %q refused: %s", v, op, in, p.Reason)
		return
	}
	if got := value(c); got != want {
		t.Errorf("%s %s %q = %q, want %q", v, op, in, got, want)
	}
}

func refuses(t *testing.T, v contracts.Variable, op contracts.Operator, in string, reason contracts.FormulaReason) {
	t.Helper()
	c, p := parseOne(t, v, op, in)
	if p == nil {
		t.Errorf("%s %s %q accepted as %q", v, op, in, value(c))
		return
	}
	if p.Reason != reason || p.At != "all[0].any[0].value" {
		t.Errorf("%s %s %q refused at %q for %s, want %s", v, op, in, p.At, p.Reason, reason)
	}
}

// Q6 of ADR 0027: addresses and domains.
func TestQ6AddressesAndDomains(t *testing.T) {
	d, a := contracts.VarFromDomain, contracts.VarFromAddress
	accepts(t, d, "is", "Example.COM", "example.com")
	accepts(t, d, "is", "example.com.", "example.com")
	accepts(t, d, "is", "Bücher.Example", "xn--bcher-kva.example")
	accepts(t, d, "is", "xn--bcher-kva.example", "xn--bcher-kva.example")
	accepts(t, d, "is", "ＥＸＡＭＰＬＥ.com", "example.com") // full width, mapped by UTS 46
	refuses(t, d, "is", "localhost", contracts.FormulaInvalidValue)
	refuses(t, d, "is", "a..example", contracts.FormulaInvalidValue)
	refuses(t, d, "is", "exa_mple.com", contracts.FormulaInvalidValue)
	refuses(t, d, "is", "-a.example", contracts.FormulaInvalidValue)
	refuses(t, d, "is", "xn--zz.example", contracts.FormulaInvalidValue)
	refuses(t, d, "is", "", contracts.FormulaInvalidValue)
	refuses(t, d, "is", "p\u0430ypal.com", contracts.FormulaConfusableValue) // a Cyrillic \u0430 among Latin letters
	refuses(t, d, "is", "xn--pypal-4ve.com", contracts.FormulaConfusableValue)
	accepts(t, d, "is", "пример.рф", "xn--e1afmkfd.xn--p1ai") // Cyrillic alone is not confusable
	accepts(t, a, "is", "Alice@Example.COM", "alice@example.com")
	accepts(t, a, "is", `"alice"@example.com`, "alice@example.com")
	accepts(t, a, "is", `"al\ice"@example.com`, "alice@example.com")
	accepts(t, a, "is", "ann+tag@bücher.example", "ann+tag@xn--bcher-kva.example")
	accepts(t, a, "is", "a”b@example.com", "a”b@example.com")
	accepts(t, a, "is", "x@y@example.com", "x@y@example.com") // split at the last @
	accepts(t, a, "is", "ÅSA@example.com", "åsa@example.com")
	refuses(t, a, "is", "alice@exa_mple.com", contracts.FormulaInvalidValue)
	refuses(t, a, "is", "@example.com", contracts.FormulaInvalidValue)
	refuses(t, a, "is", "alice", contracts.FormulaInvalidValue)
	refuses(t, a, "is", `"bad local"@example.com`, contracts.FormulaInvalidValue)
	refuses(t, a, "is", `"a\"b"@example.com`, contracts.FormulaInvalidValue)
	refuses(t, a, "is", `"unclosed@example.com`, contracts.FormulaInvalidValue)
	refuses(t, a, "is", `"a"b"@example.com`, contracts.FormulaInvalidValue)
	refuses(t, a, "is", `"a\`+`@example.com`, contracts.FormulaInvalidValue)
	refuses(t, a, "is", "a\u0007b@example.com", contracts.FormulaInvalidValue)
	refuses(t, a, "is", "p\u0430ypal@example.com", contracts.FormulaConfusableValue)
	refuses(t, a, "is", "paypal@p\u0430ypal.com", contracts.FormulaConfusableValue)

	// The message's side: an address that is not valid is Unknown for its
	// address and its domain conditions, never False.
	m := &Message{Have: FromItem | FromMeta, From: []string{`bad local@other.example`}}
	for _, s := range []string{"from_address is alice@example.com", "from_domain is example.com", "from_domain is other.example"} {
		for _, not := range []bool{false, true} {
			c := mustOne(t, s, not)
			if v := Eval([]*Canonical{c}, m, nil, ForQuery).Value; v != Unknown {
				t.Errorf("%s (not %v) on a bad local part: %d", s, not, v)
			}
		}
	}
	// A message's mixed-script domain is valid, equals no value, and is
	// still under the domains it is under.
	m.From = []string{"x@p\u0430ypal.example.com"}
	if v := Eval([]*Canonical{mustOne(t, "from_domain is example.com", false)}, m, nil, ForQuery).Value; v != True {
		t.Errorf("a mixed-script subdomain: %d", v)
	}
	if v := Eval([]*Canonical{mustOne(t, "from_domain is paypal.example.com", false)}, m, nil, ForQuery).Value; v != False {
		t.Errorf("a mixed-script domain against its look-alike: %d", v)
	}
	// Invalid UTF-8 in a message's address makes it not valid.
	m.From = []string{"a\xff@example.com"}
	if v := Eval([]*Canonical{mustOne(t, "from_domain is example.com", true)}, m, nil, ForQuery).Value; v != Unknown {
		t.Errorf("invalid UTF-8 in an address: %d", v)
	}
	if _, _, r := normaliseDomain("\xff.com"); r != contracts.FormulaInvalidValue {
		t.Errorf("invalid UTF-8 in a domain: %s", r)
	}
}

func mustOne(t *testing.T, s string, not bool) *Canonical {
	t.Helper()
	parts := strings.SplitN(s, " ", 3)
	c, err := Canonicalise(contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{{Var: contracts.Variable(parts[0]), Op: contracts.Operator(parts[1]), Value: parts[2], Not: not}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Q7: text.
func TestQ7Text(t *testing.T) {
	t.Parallel()
	s := contracts.VarSubject
	accepts(t, s, "contains", "ﬁle", "file")
	accepts(t, s, "contains", "ＩＮＶＯＩＣＥ", "invoice")
	accepts(t, s, "contains", "Straße", "straße")
	accepts(t, s, "contains", "STRAẞE", "straße")
	accepts(t, s, "contains", "ΣΊΣΥΦΟΣ", "σίσυφοσ")
	accepts(t, s, "contains", "ς", "σ")
	accepts(t, s, "contains", "\u212a", "k")          // the Kelvin sign
	accepts(t, s, "contains", "İstanbul", "İstanbul") // İ stays itself, as the inbox's search keeps it
	accepts(t, s, "contains", "ıi", "ıi")
	accepts(t, s, "contains", "in\u200bvo\u2060ice", "invoice")
	accepts(t, s, "contains", "\u202eabc\u202c", "abc")
	accepts(t, s, "contains", "a\u200d\ufe0fb", "ab")
	accepts(t, s, "contains", "  a \t\n\u2028 b\u3000 ", "a b")
	accepts(t, s, "contains", "say “hi” \\ there", "say “hi” \\ there")
	accepts(t, s, "contains", "a\u00ad\u0301", "á")
	accepts(t, s, "contains", "A\u034f\u030a", "å")
	accepts(t, s, "contains", "发票", "发票")
	accepts(t, s, "contains", "x�", "x�")
	accepts(t, s, "contains", "q\u0301", "q\u0301")
	refuses(t, s, "contains", "a\u0007b", contracts.FormulaInvalidValue)
	refuses(t, s, "contains", "a\ue000", contracts.FormulaInvalidValue)         // private use
	accepts(t, s, "contains", "a\U000e0001", "a")                               // a tag: default ignorable, removed
	refuses(t, s, "contains", "a\u0600", contracts.FormulaInvalidValue)         // a prepended concatenation mark: format, kept, refused
	refuses(t, s, "contains", "a\ufff9", contracts.FormulaInvalidValue)         // an interlinear annotation: format, kept, refused
	refuses(t, s, "contains", "a\U0010fffd", contracts.FormulaInvalidValue)     // private use, plane 16
	refuses(t, s, "contains", "a\u0378", contracts.FormulaInvalidValue)         // unassigned
	refuses(t, s, "contains", "\u200b\u00ad", contracts.FormulaInvalidValue)    // nothing left
	refuses(t, s, "contains", "...", contracts.FormulaInvalidValue)             // no letter or digit
	refuses(t, s, "contains", "inv\u043eice", contracts.FormulaConfusableValue) // a Cyrillic \u043e
	accepts(t, s, "contains", "αβγ abc", "αβγ abc")                             // two scripts in two words is not confusable
	refuses(t, s, "contains", "aα", contracts.FormulaConfusableValue)

	// The rounds: a, a soft hyphen and a combining acute, and A, U+034F and a combining ring, settle in two changing rounds, which one round
	// alone does not.
	for _, in := range []string{"a\u00ad\u0301", "A\u034f\u030a"} {
		if one := textRound(in); textRound(one) == one {
			t.Errorf("%q settles in one round", in)
		}
		n, ok := normaliseText(in)
		if !ok || textRound(n) != n || rounds(in) != 2 {
			t.Errorf("%q: %q, %v, %d rounds", in, n, ok, rounds(in))
		}
	}
	// Past the cap: a step that always changes its input is refused for a
	// value and makes a field Unknown.
	grow := func(s string) string { return s + "x" }
	if _, ok := fixpoint("a", grow); ok {
		t.Error("a step that never settles settled")
	}
	three := 0
	upTo := func(s string) string {
		if three < 3 {
			three++
			return s + "y"
		}
		return s
	}
	if r, ok := fixpoint("a", upTo); !ok || r != "ayyy" {
		t.Errorf("three changing rounds: %q, %v", r, ok)
	}
	if v := contained(&text{ok: false}, "x"); v != Unknown {
		t.Errorf("a field past the cap: %d", v)
	}
	// Every code point: the bounded normaliser is idempotent wherever it
	// gives a result, alone and between a letter and a combining mark.
	most := 0
	for r := rune(0); r <= utf8.MaxRune; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		forms := []string{string(r)}
		if unicode.In(r, unicode.M, unicode.Cf, unicode.Variation_Selector, unicode.Other_Default_Ignorable_Code_Point, unicode.White_Space) {
			forms = append(forms, "a"+string(r)+"\u0301") // between a letter and a mark it composes with
		}
		for _, in := range forms {
			n, ok := normaliseText(in)
			if !ok {
				t.Fatalf("U+%04X does not settle", r)
			}
			if again, _ := normaliseText(n); again != n {
				t.Fatalf("U+%04X: %q then %q", r, n, again)
			}
			most = max(most, rounds(in))
		}
	}
	t.Logf("the most changing rounds any code point needed: %d", most)
}

// rounds is how many rounds of textRound change s before it settles.
func rounds(s string) int {
	n := 0
	for next := textRound(s); next != s; next = textRound(s) {
		s = next
		n++
	}
	return n
}

// Q8: substrings.
func TestQ8Substrings(t *testing.T) {
	for _, c := range []struct {
		value, field string
		want         Value
	}{
		{"invoice", "invoices", True},
		{"invoice", "your INVOICE,", True},
		{"invoice", "re-invoiced", True},
		{"car", "scar", True},
		{"car", "careful", True},
		{".pdf", "bill.pdf", True},
		{"Calle Falsa 123", "Calle Falsa\n123", True},
		{"invoice", "in\u00advoice", True},
		{"in\u00advoice", "invoice", True},
		{"发票", "这是您的发票，谢谢", True},
		{"ใบแจ้งหนี้", "นี่คือใบแจ้งหนี้ของคุณ", True},
		{"start", "start of it", True},
		{"end", "the end", True},
		{"cafe", "café", False},
		{"cafe", "cafe\u0301", False},
		{"café", "cafe\u0301", True},
		{"q", "q\u0301", True},
		{"q\u0301", "aq\u0301b", True},
		{"é", "cafe\u0301", True},
		{"invoice", "", False},
		{"x", "a\xffx", True},
	} {
		cf := mustOne(t, "subject contains "+c.value, false)
		m := &Message{Have: FromItem | FromMeta | FromEntry, Subject: c.field, Body: c.field}
		if v := Eval([]*Canonical{cf}, m, nil, ForQuery).Value; v != c.want {
			t.Errorf("subject %q contains %q: %d, want %d", c.field, c.value, v, c.want)
		}
		bf := mustOne(t, "body contains "+c.value, false)
		if v := Eval([]*Canonical{bf}, m, nil, ForQuery).Value; v != c.want {
			t.Errorf("body %q contains %q: %d, want %d", c.field, c.value, v, c.want)
		}
	}
}

// Q9: times and sizes.
func TestQ9TimesAndSizes(t *testing.T) {
	r := contracts.VarReceived
	accepts(t, r, "since", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z")
	accepts(t, r, "since", "2026-01-01t01:30:00z", "2026-01-01T01:30:00Z")
	accepts(t, r, "since", "2026-01-01T05:30:00+05:30", "2026-01-01T00:00:00Z")
	accepts(t, r, "since", "2025-12-31T23:00:00-01:00", "2026-01-01T00:00:00Z")
	accepts(t, r, "since", "2000-01-01T00:00:00Z", "2000-01-01T00:00:00Z")
	accepts(t, r, "since", "2100-01-01T00:00:00Z", "2100-01-01T00:00:00Z")
	for _, bad := range []string{
		"2026-01-01T00:00:00.5Z", "2026-01-01T00:00:60Z", "1999-12-31T23:59:59Z", "2100-01-01T00:00:01Z",
		"2026-01-01T00:00Z", "2026-01-01 00:00:00Z", "2026-01-01T00:00:00", "2026-01-01T00:00:00+0100",
		"2026-01-01T00:00:00+24:00", "2026-01-01T00:00:00+01:60", "2026-13-01T00:00:00Z", "2026-02-30T00:00:00Z",
		"2026-01-01T00:00:00+01:00x", "２026-01-01T00:00:00Z", "2026/01/01T00:00:00Z", "2026-01-01T00:00:00Y",
		"2000-01-01T00:30:00+01:00",
	} {
		refuses(t, r, "since", bad, contracts.FormulaInvalidValue)
	}
	z := contracts.VarSize
	accepts(t, z, "at_least", "1", "1")
	accepts(t, z, "at_least", "33554432", "33554432")
	for _, bad := range []string{"0", "-1", "+1", "01", "1,000", "1_000", "33554433", "1e3", "", "99999999999", " 1"} {
		if bad == "" {
			continue
		}
		refuses(t, z, "at_least", bad, contracts.FormulaInvalidValue)
	}
	// A time compared to the nanosecond.
	c := mustOne(t, "received since 2026-01-01T00:00:00Z", false)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, x := range []struct {
		d    time.Duration
		want Value
	}{{-1, False}, {0, True}, {1, True}} {
		m := &Message{Have: FromItem | FromMeta, Received: at.Add(x.d)}
		if v := Eval([]*Canonical{c}, m, nil, ForQuery).Value; v != x.want {
			t.Errorf("received %v after: %d", x.d, v)
		}
	}
}

// Media types, lists and mailboxes.
func TestTypesListsAndMailboxes(t *testing.T) {
	ty := contracts.VarAttachmentType
	accepts(t, ty, "is", "Image/PNG", "image/png")
	accepts(t, ty, "is", "application/vnd.ms-excel", "application/vnd.ms-excel")
	accepts(t, ty, "is", "font/*", "font/*")
	for _, bad := range []string{"image", "image/", "foo/bar", "*/*", "image/png; x=y", "image/p g", "image/pñg"} {
		refuses(t, ty, "is", bad, contracts.FormulaInvalidValue)
	}
	l := contracts.VarList
	accepts(t, l, "is", "News.Example.COM", "news.example.com")
	accepts(t, l, "is", "a+b_c'd", "a+b_c'd")
	for _, bad := range []string{"<news.example.com>", "a b", "ñ.example"} {
		refuses(t, l, "is", bad, contracts.FormulaInvalidValue)
	}
	mb := contracts.VarMailbox
	accepts(t, mb, "is", "MBX_AAAAAAAAAAAAAAAAAAAAAAAAAA", "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa")
	accepts(t, mb, "is", "mbx_00000000000000000000000001", "mbx_00000000000000000000000001")
	for _, bad := range []string{"mbx_short", "box_aaaaaaaaaaaaaaaaaaaaaaaaaa", "mbx_aaaaaaaaaaaaaaaaaaaaaaaaa-", "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		refuses(t, mb, "is", bad, contracts.FormulaInvalidValue)
	}
	// The account's mailboxes: one of them, or unknown_mailbox; a mailbox
	// disconnected later still canonicalises.
	in := []byte(`{"v":1,"all":[{"any":[{"var":"mailbox","op":"is","value":"mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa"}]}]}`)
	if _, err := Parse(in, []string{"mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa"}); err != nil {
		t.Error(err)
	}
	var p *Problem
	if _, err := Parse(in, []string{}); !errors.As(err, &p) || p.Reason != contracts.FormulaUnknownMailbox {
		t.Errorf("an account without mailboxes: %v", err)
	}
	if _, err := Canonicalise(contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{{Var: mb, Op: "is", Value: "mbx_zzzzzzzzzzzzzzzzzzzzzzzzzz"}}}}}); err != nil {
		t.Error(err)
	}
	if !typeMatches("image/png", "image/*") || typeMatches("image/p g", "image/*") || typeMatches("imagex/png", "image/*") {
		t.Error("typeMatches")
	}
}

// Canonicalise refuses what Parse refuses, at the same paths.
func TestCanonicaliseChecksAsParseDoes(t *testing.T) {
	c := func(v contracts.Variable, op contracts.Operator, value string) contracts.Condition {
		return contracts.Condition{Var: v, Op: op, Value: value}
	}
	many := func(n int, mk func(i int) contracts.Condition) []contracts.Condition {
		out := make([]contracts.Condition, n)
		for i := range out {
			out[i] = mk(i)
		}
		return out
	}
	groupsOf := func(n int) []contracts.Group {
		out := make([]contracts.Group, n)
		for i := range out {
			out[i] = contracts.Group{Any: []contracts.Condition{c("size", "at_least", string(rune('1'+i%9)))}}
		}
		return out
	}
	for _, x := range []struct {
		f      contracts.Formula
		at     string
		reason contracts.FormulaReason
	}{
		{contracts.Formula{V: 2}, "v", contracts.FormulaUnsupportedVersion},
		{contracts.Formula{V: 1, All: groupsOf(33)}, "all[32]", contracts.FormulaTooManyGroups},
		{contracts.Formula{V: 1, All: []contracts.Group{{}, {}}}, "all[0].any", contracts.FormulaEmptyGroup},
		{contracts.Formula{V: 1, All: []contracts.Group{{Any: many(33, func(int) contracts.Condition { return c("list", "exists", "") })}}}, "all[0].any[32]", contracts.FormulaTooManyConditions},
		{contracts.Formula{V: 1, All: []contracts.Group{{Any: many(9, func(i int) contracts.Condition { return c("message", "about", "t"+string(rune('a'+i))) })}}}, "all[0].any[8]", contracts.FormulaTooManyModel},
		{contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{c("subject", "contains", "")}}}}, "all[0].any[0].value", contracts.FormulaMissingValue},
		{contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{c("list", "exists", "x")}}}}, "all[0].any[0].value", contracts.FormulaUnexpectedValue},
		{contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{c("nope", "is", "x")}}}}, "all[0].any[0].var", contracts.FormulaUnknownVariable},
	} {
		_, err := Canonicalise(x.f)
		var p *Problem
		if !errors.As(err, &p) || p.At != x.at || p.Reason != x.reason {
			t.Errorf("Canonicalise: %v, want %s at %q", err, x.reason, x.at)
		}
	}
}

// FuzzNormalise: the bounded normaliser is idempotent on any string, and
// what a value check accepts it accepts again unchanged.
func FuzzNormalise(f *testing.F) {
	for _, s := range []string{"a\u00ad\u0301", "A\u034f\u030a", "ﬁ", "ΣΊΣΥΦΟΣ", "\u212a", "İ", " a \n b ", "inv\u043eice", "\xff", "q\u0301", "ẞ"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, ok := normaliseText(s)
		if ok {
			if again, ok2 := normaliseText(n); !ok2 || again != n {
				t.Fatalf("%q: %q then %q", s, n, again)
			}
			if !utf8.ValidString(n) {
				t.Fatalf("%q normalised to invalid UTF-8", s)
			}
		}
		if v, reason := checkText(s); reason == "" {
			if v2, r2 := checkText(v); r2 != "" || v2 != v {
				t.Fatalf("checkText(%q) = %q, then %q, %s", s, v, v2, r2)
			}
		}
		if d, _, reason := normaliseDomain(s); reason == "" {
			if d2, _, r2 := normaliseDomain(d); r2 != "" || d2 != d {
				t.Fatalf("domain %q = %q, then %q, %s", s, d, d2, r2)
			}
		}
		if l, d, _, reason := normaliseAddress(s); reason == "" {
			if l2, d2, _, r2 := normaliseAddress(l + "@" + d); r2 != "" || l2 != l || d2 != d {
				t.Fatalf("address %q = %q@%q, then %q@%q, %s", s, l, d, l2, d2, r2)
			}
		}
	})
}
