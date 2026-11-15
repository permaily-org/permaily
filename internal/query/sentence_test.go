package query

import (
	"reflect"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The sentence's tests, ADR 0027's Q20 to Q25 and Q43. Formulas are built by
// hand (see sentence_gen_test.go).

const (
	m1 = "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	m2 = "mbx_bbbbbbbbbbbbbbbbbbbbbbbbbb"
	m3 = "mbx_cccccccccccccccccccccccccc"
	m4 = "mbx_dddddddddddddddddddddddddd"
	m5 = "mbx_eeeeeeeeeeeeeeeeeeeeeeeeee"

	addr1 = "k7m2p9q4r8s3t6v5w2x9@permaily.com"
	addr2 = "a2b3c4d5e6f7g8h9i2j3@permaily.com"
	addr3 = "z9y8x7w6v5u4t3s2r9q8@permaily.com"
)

// goldenContext has a mailbox with a label, one without, and m4 and m5 are
// disconnected.
var goldenContext = Context{Lang: "en", Mailboxes: map[string]MailboxName{
	m1: {Label: "Work", Address: addr1},
	m2: {Label: "", Address: addr2},
}}

func sentenceCond(v contracts.Variable, op contracts.Operator, value string) contracts.Condition {
	return contracts.Condition{Var: v, Op: op, Value: value}
}

func not(c contracts.Condition) contracts.Condition { c.Not = true; return c }

// formula is a Canonical of one group per argument.
func formula(groups ...[]contracts.Condition) *Canonical {
	f := contracts.Formula{V: contracts.FormulaVersion}
	for _, g := range groups {
		f.All = append(f.All, contracts.Group{Any: g})
	}
	return &Canonical{Formula: f}
}

func one(cs ...contracts.Condition) *Canonical { return formula(cs) }

// readsBack asserts the sentence of f in c is read back as f from its plain
// text and from its parts.
func readsBack(t *testing.T, f *Canonical, c Context) contracts.Sentence {
	t.Helper()
	s := Render(f, c)
	got, err := parseSentence(s.Text, c)
	if err != nil || !sameFormula(got, f) {
		t.Fatalf("plain text not read back (%v):\n%s\nwant %+v\ngot  %+v", err, s.Text, f, got)
	}
	got, err = parseParts(s, c)
	if err != nil || !sameFormula(got, f) {
		t.Fatalf("parts not read back (%v):\n%+v\nwant %+v\ngot  %+v", err, s, f, got)
	}
	return s
}

// Q20: golden sentences in English, every template and sign, one group and
// several, every message, a domain with a Unicode form, times at midnight,
// with minutes and with seconds, each kind of size, mailboxes named by
// label, by address and disconnected. Each is read back too.
func TestGoldenSentences(t *testing.T) {
	cases := []struct {
		f    *Canonical
		want string
	}{
		{&Canonical{Formula: contracts.Formula{V: 1}}, "All mail."},
		{&Canonical{Never: true}, "No mail."},
		{one(sentenceCond("from_address", "is", "ann@example.com")), "Mail sent from “ann@example.com”."},
		{one(not(sentenceCond("from_address", "is", "ann@example.com"))), "Mail not sent from “ann@example.com”."},
		{one(sentenceCond("from_address", "is", "ann@xn--bcher-kva.example")), "Mail sent from “ann@bücher.example” (xn--bcher-kva.example)."},
		{one(sentenceCond("from_domain", "is", "holded.com")), "Mail sent from an address at holded.com or a subdomain of it."},
		{one(not(sentenceCond("from_domain", "is", "holded.com"))), "Mail not sent from any address at holded.com or a subdomain of it."},
		{one(sentenceCond("recipient_address", "is", "me+bills@example.com")), "Mail addressed to “me+bills@example.com”."},
		{one(not(sentenceCond("recipient_address", "is", "me@example.com"))), "Mail not addressed to “me@example.com”."},
		{one(sentenceCond("recipient_domain", "is", "xn--bcher-kva.example")), "Mail addressed to an address at bücher.example (xn--bcher-kva.example) or a subdomain of it."},
		{one(not(sentenceCond("recipient_domain", "is", "example.com"))), "Mail not addressed to any address at example.com or a subdomain of it."},
		{one(sentenceCond("signed_by", "is", "holded.com")), "Mail signed by holded.com or a subdomain of it."},
		{one(not(sentenceCond("signed_by", "is", "holded.com"))), "Mail not signed by holded.com or any subdomain of it."},
		{one(sentenceCond("subject", "contains", "invoice")), "Mail whose subject contains the text “invoice”."},
		{one(not(sentenceCond("subject", "contains", "invoice"))), "Mail whose subject doesn't contain the text “invoice”."},
		{one(sentenceCond("body", "contains", "calle falsa 123")), "Mail whose body contains the text “calle falsa 123”."},
		{one(not(sentenceCond("body", "contains", "unsubscribe"))), "Mail whose body doesn't contain the text “unsubscribe”."},
		{one(sentenceCond("attachments", "exists", "")), "Mail with attachments."},
		{one(not(sentenceCond("attachments", "exists", ""))), "Mail without attachments."},
		{one(sentenceCond("attachment_name", "contains", ".pdf")), "Mail with an attachment whose name contains the text “.pdf”."},
		{one(not(sentenceCond("attachment_name", "contains", ".exe"))), "Mail with no attachment whose name contains the text “.exe”."},
		{one(sentenceCond("attachment_type", "is", "application/pdf")), "Mail with an attachment of type application/pdf."},
		{one(not(sentenceCond("attachment_type", "is", "application/pdf"))), "Mail with no attachment of type application/pdf."},
		{one(sentenceCond("attachment_type", "is", "image/*")), "Mail with an attachment of any image type."},
		{one(not(sentenceCond("attachment_type", "is", "image/*"))), "Mail with no attachment of any image type."},
		{one(sentenceCond("list", "exists", "")), "Mail sent to a mailing list."},
		{one(not(sentenceCond("list", "exists", ""))), "Mail not sent to a mailing list."},
		{one(sentenceCond("list", "is", "news.example.com")), "Mail sent to the mailing list “news.example.com”."},
		{one(not(sentenceCond("list", "is", "news.example.com"))), "Mail not sent to the mailing list “news.example.com”."},
		{one(sentenceCond("received", "since", "2026-01-01T00:00:00Z")), "Mail received on or after 1 January 2026. Times are in UTC."},
		{one(sentenceCond("received", "before", "2026-03-05T09:30:00Z")), "Mail received before 5 March 2026, 09:30. Times are in UTC."},
		{one(sentenceCond("received", "since", "2026-12-31T23:59:15Z")), "Mail received on or after 31 December 2026, 23:59:15. Times are in UTC."},
		{one(sentenceCond("size", "at_least", "1000000")), "Mail of 1 MB or more."},
		{one(sentenceCond("size", "at_least", "5000")), "Mail of 5 KB or more."},
		{one(sentenceCond("size", "below", "2048")), "Mail smaller than 2,048 bytes."},
		{one(sentenceCond("size", "below", "1")), "Mail smaller than 1 byte."},
		{one(sentenceCond("size", "at_least", "1048576")), "Mail of 1,048,576 bytes or more."},
		{one(sentenceCond("size", "below", "33554432")), "Mail smaller than 33,554,432 bytes."},
		{one(sentenceCond("size", "below", "1500000000")), "Mail smaller than 1,500 MB."},
		{one(sentenceCond("mailbox", "is", m1)), "Mail that arrived at your mailbox “Work”."},
		{one(not(sentenceCond("mailbox", "is", m1))), "Mail that didn't arrive at your mailbox “Work”."},
		{one(sentenceCond("mailbox", "is", m2)), "Mail that arrived at your mailbox " + addr2 + "."},
		{one(sentenceCond("mailbox", "is", m4)), "Mail that arrived at a mailbox you disconnected (" + m4 + ")."},
		{one(sentenceCond("subject", "about", "travel")), "Mail whose subject is about “travel” (a model decides)."},
		{one(not(sentenceCond("subject", "about", "travel"))), "Mail whose subject is not about “travel” (a model decides)."},
		{one(sentenceCond("message", "about", "invoices")), "Mail about “invoices” (a model decides)."},
		{one(not(sentenceCond("message", "about", "invoices"))), "Mail not about “invoices” (a model decides)."},
		{one(sentenceCond("message", "mentions", "my home address")), "Mail that mentions “my home address” (a model decides)."},
		{one(not(sentenceCond("message", "mentions", "my home address"))), "Mail that doesn't mention “my home address” (a model decides)."},
		{one(sentenceCond("from_domain", "is", "holded.com"), sentenceCond("subject", "contains", "invoice")), "Mail sent from an address at holded.com or a subdomain of it, or whose subject contains the text “invoice”."},
		{
			formula(
				[]contracts.Condition{sentenceCond("from_domain", "is", "holded.com"), sentenceCond("subject", "contains", "invoice")},
				[]contracts.Condition{sentenceCond("received", "since", "2026-01-01T00:00:00Z")},
				[]contracts.Condition{not(sentenceCond("attachments", "exists", ""))},
			),
			"Mail that meets all of these:\n" +
				"- sent from an address at holded.com or a subdomain of it, or whose subject contains the text “invoice”\n" +
				"- received on or after 1 January 2026\n" +
				"- without attachments. Times are in UTC.",
		},
		{
			formula(
				[]contracts.Condition{sentenceCond("mailbox", "is", m1), sentenceCond("mailbox", "is", m5)},
				[]contracts.Condition{sentenceCond("message", "about", "travel")},
			),
			"Mail that meets all of these:\n" +
				"- that arrived at your mailbox “Work”, or that arrived at a mailbox you disconnected (" + m5 + ")\n" +
				"- about “travel” (a model decides).",
		},
	}
	for _, tc := range cases {
		s := readsBack(t, tc.f, goldenContext)
		if s.Text != tc.want {
			t.Errorf("got  %q\nwant %q", s.Text, tc.want)
		}
		if s.Lang != "en" || s.Lead == nil {
			t.Errorf("%q: lang %q, lead %v", tc.want, s.Lang, s.Lead)
		}
	}
}

// Q20: the parts of a sentence are its words in pieces: a fixed part for
// each template's words, a value part for each value, and the plain text
// their concatenation, with the lead and a "- " before each line when there
// are several groups.
func TestTheSentencesParts(t *testing.T) {
	s := Render(formula(
		[]contracts.Condition{sentenceCond("from_domain", "is", "xn--bcher-kva.example"), sentenceCond("message", "about", "travel")},
		[]contracts.Condition{sentenceCond("mailbox", "is", m4)},
	), goldenContext)
	want := contracts.Sentence{
		Lang: "en",
		Text: "Mail that meets all of these:\n- sent from an address at bücher.example (xn--bcher-kva.example) or a subdomain of it, or about “travel” (a model decides)\n- that arrived at a mailbox you disconnected (" + m4 + ").",
		Lead: []contracts.SentencePart{{Text: "Mail that meets all of these:"}},
		Lines: []contracts.SentenceLine{
			{Parts: []contracts.SentencePart{
				{Text: "sent from an address at "},
				{Text: "bücher.example (xn--bcher-kva.example)", Value: true},
				{Text: " or a subdomain of it"},
				{Text: ", or "},
				{Text: "about ", Model: true},
				{Text: "“travel”", Value: true, Model: true},
				{Text: " (a model decides)", Model: true},
			}},
			{Parts: []contracts.SentencePart{
				{Text: "that arrived at "},
				{Text: "a mailbox you disconnected ("},
				{Text: m4, Value: true},
				{Text: ")"},
				{Text: "."},
			}},
		},
	}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("got  %#v\nwant %#v", s, want)
	}
	s1 := Render(one(sentenceCond("size", "below", "2048")), goldenContext)
	if len(s1.Lead) != 0 || len(s1.Lines) != 1 || s1.Lines[0].Parts[0].Text != "Mail " {
		t.Fatalf("one group: %#v", s1)
	}
}

// Q20: a rule of several formulas reads "Any of these:" and each formula's
// sentence; a rule of one reads as its formula, and of none as no mail.
func TestARulesSentence(t *testing.T) {
	a := one(sentenceCond("from_address", "is", "a@b.com"))
	b := formula([]contracts.Condition{sentenceCond("attachments", "exists", "")}, []contracts.Condition{sentenceCond("size", "at_least", "1000000")})
	r := RenderRule([]*Canonical{a, b}, goldenContext)
	want := "Any of these:\nMail sent from “a@b.com”.\nMail that meets all of these:\n- with attachments\n- of 1 MB or more."
	if r.Text != want || len(r.Formulas) != 2 || !reflect.DeepEqual(r.Lead, []contracts.SentencePart{{Text: "Any of these:"}}) {
		t.Fatalf("got %#v", r)
	}
	fs, err := parseRule(r.Text, goldenContext)
	if err != nil || len(fs) != 2 || !sameFormula(fs[0], a) || !sameFormula(fs[1], b) {
		t.Fatalf("not read back: %v %v", fs, err)
	}
	if r := RenderRule([]*Canonical{a}, goldenContext); r.Text != "Mail sent from “a@b.com”." || len(r.Lead) != 0 {
		t.Fatalf("one formula: %#v", r)
	}
	if r := RenderRule(nil, goldenContext); r.Text != "No mail." {
		t.Fatalf("no formula: %#v", r)
	}
	if _, err := parseRule("Any of these:\nMail sent from “a@b.com”.\nnot a sentence", goldenContext); err == nil {
		t.Fatal("a rule with a line that is no sentence was read")
	}
}

// What is not a canonical formula still reads as what it means, never as
// another formula: received and size negated read as Canonicalise makes
// them positive, a group of no condition as no mail, a nil formula as no
// mail, and a condition no template has says its variable, operator and
// value, quoted.
func TestWhatIsNotCanonicalStillSaysWhatItMeans(t *testing.T) {
	for f, want := range map[*Canonical]string{
		one(not(sentenceCond("received", "since", "2026-01-01T00:00:00Z"))):            "Mail received before 1 January 2026. Times are in UTC.",
		one(not(sentenceCond("received", "before", "2026-01-01T00:00:00Z"))):           "Mail received on or after 1 January 2026. Times are in UTC.",
		one(not(sentenceCond("size", "at_least", "1000"))):                             "Mail smaller than 1 KB.",
		one(not(sentenceCond("size", "below", "1000"))):                                "Mail of 1 KB or more.",
		formula([]contracts.Condition{sentenceCond("attachments", "exists", "")}, nil): "No mail.",
		nil:                                     "No mail.",
		one(sentenceCond("subject", "is", "x")): "Mail meeting the condition “subject” “is” “x”.",
		one(not(sentenceCond("body", "about", "x"))):        "Mail not meeting the condition “body” “about” “x” (a model decides).",
		one(sentenceCond("received", "since", "today")):     "Mail received on or after “today”.",
		one(sentenceCond("size", "below", "007")):           "Mail smaller than “007”.",
		one(sentenceCond("from_domain", "is", "a b")):       "Mail sent from an address at “a b” or a subdomain of it.",
		one(sentenceCond("attachment_type", "is", "image")): "Mail with an attachment of type “image”.",
		one(sentenceCond("mailbox", "is", "Work")):          "Mail that arrived at a mailbox you disconnected (“Work”).",
	} {
		if got := Render(f, goldenContext).Text; got != want {
			t.Errorf("got  %q\nwant %q", got, want)
		}
	}
	for _, f := range []*Canonical{one(sentenceCond("subject", "is", "x")), one(not(sentenceCond("body", "about", "x”, or y"))), one(sentenceCond("size", "below", "007"))} {
		readsBack(t, f, goldenContext)
	}
}

// Q23: the tables. Every variable, operator and sign has a template in every
// language (received and size negated by being made positive), and none
// more; no fixed text holds a quote, a backslash, a line break, a NUL or the
// separator; no two templates have the same fixed words; a language with
// no table, or a region of one, reads English.
func TestTheTables(t *testing.T) {
	for tag, l := range languages {
		want := 0
		for _, k := range conditionKinds {
			for _, neg := range []bool{false, true} {
				if neg && !k.neg {
					c := sentencePositive(contracts.Condition{Var: k.v, Op: k.op, Not: true})
					if _, ok := l.templates[templateKey{Var: c.Var, Op: c.Op}]; !ok || c.Not {
						t.Errorf("%s: %s %s negated is not made positive", tag, k.v, k.op)
					}
					continue
				}
				for _, anyType := range []bool{false, true} {
					if anyType && k.v != contracts.VarAttachmentType {
						continue
					}
					want++
					tpl, ok := l.templates[templateKey{k.v, k.op, neg, anyType}]
					if !ok || (k.op == contracts.OpExists) != (len(tpl) == 1) || len(tpl) > 2 {
						t.Errorf("%s: %s %s not=%v any=%v: %q", tag, k.v, k.op, neg, anyType, tpl)
					}
				}
			}
		}
		if len(l.templates) != want {
			t.Errorf("%s: %d templates, want %d", tag, len(l.templates), want)
		}
		seen := map[string]bool{}
		for _, tpl := range append([]template{l.unknown[0], l.unknown[1]}, values(l.templates)...) {
			k := strings.Join(tpl, "\x00")
			if seen[k] {
				t.Errorf("%s: two templates of %q", tag, tpl)
			}
			seen[k] = true
		}
		for _, w := range fixedWords(l) {
			if strings.ContainsAny(w, "“”\\\n\x00") || (w != l.or && strings.Contains(w, l.or)) || strings.ContainsFunc(w, escapedQuote) {
				t.Errorf("%s: fixed text %q holds a quote, a backslash, a line break or the separator", tag, w)
			}
		}
	}
	for _, tag := range []string{"en", "en-GB", "es", "es-419", ""} {
		if languageOf(tag) != &english {
			t.Errorf("%q does not read English", tag)
		}
	}
}

func values(m map[templateKey]template) []template {
	var out []template
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// Q24: mailbox names. Two mailboxes with one label, or labels that read
// alike, are named by their addresses, an empty label likewise; a renamed
// mailbox reads by its new name; labels holding quotes, backslashes and
// template words are escaped; two disconnected mailboxes read apart, each
// with its identifier as a value part; and no two mailboxes of an account
// read alike, whatever their labels and addresses.
func TestMailboxNames(t *testing.T) {
	ctx := func(l1, a1, l2, a2 string) Context {
		return Context{Lang: "en", Mailboxes: map[string]MailboxName{m1: {l1, a1}, m2: {l2, a2}}}
	}
	text := func(c Context, id string) string { return readsBack(t, one(sentenceCond("mailbox", "is", id)), c).Text }

	same := ctx("Work", addr1, "Work", addr2)
	if a, b := text(same, m1), text(same, m2); a != "Mail that arrived at your mailbox "+addr1+"." || b != "Mail that arrived at your mailbox "+addr2+"." {
		t.Errorf("one label: %q %q", a, b)
	}
	alike := ctx("ＷＯＲＫ", addr1, "work", addr2)
	if a := text(alike, m1); a != "Mail that arrived at your mailbox "+addr1+"." {
		t.Errorf("labels that read alike: %q", a)
	}
	if a := text(ctx("", addr1, "Work", addr2), m1); a != "Mail that arrived at your mailbox "+addr1+"." {
		t.Errorf("empty label: %q", a)
	}
	if a := text(ctx("Bills", addr1, "Work", addr2), m1); a != "Mail that arrived at your mailbox “Bills”." {
		t.Errorf("renamed: %q", a)
	}
	if a := text(ctx("a\tb", addr1, "", addr2), m1); a != "Mail that arrived at your mailbox "+addr1+"." {
		t.Errorf("a label that is not one printable line: %q", a)
	}
	if a := text(ctx(`”, or that arrived at your mailbox “B\`, addr1, "", addr2), m1); a != `Mail that arrived at your mailbox “\”, or that arrived at your mailbox \“B\\”.` {
		t.Errorf("escaped: %q", a)
	}
	// A context no account gives: two mailboxes with one address, or an
	// address Permaily does not make, are named by their identifiers.
	dup := ctx("Work", addr1, "Work", addr1)
	if a, b := text(dup, m1), text(dup, m2); a != "Mail that arrived at your mailbox ("+m1+")." || b != "Mail that arrived at your mailbox ("+m2+")." {
		t.Errorf("one address: %q %q", a, b)
	}
	odd := Context{Mailboxes: map[string]MailboxName{"Work": {"", "Not An Address"}, m1: {"Work", addr1}}}
	if a := text(odd, "Work"); a != "Mail that arrived at your mailbox (“Work”)." {
		t.Errorf("an identifier that is not one: %q", a)
	}
	text(odd, m1)

	// Two disconnected mailboxes.
	s4, s5 := Render(one(sentenceCond("mailbox", "is", m4)), goldenContext), Render(one(sentenceCond("mailbox", "is", m5)), goldenContext)
	if s4.Text == s5.Text || reflect.DeepEqual(s4.Lines, s5.Lines) {
		t.Fatal("two disconnected mailboxes read alike")
	}
	for id, s := range map[string]contracts.Sentence{m4: s4, m5: s5} {
		if p := s.Lines[0].Parts; !slicesContain(p, contracts.SentencePart{Text: id, Value: true}) {
			t.Errorf("%s is not a value part: %#v", id, p)
		}
	}
	both := readsBack(t, one(sentenceCond("mailbox", "is", m4), sentenceCond("mailbox", "is", m5)), goldenContext)
	if strings.Count(both.Text, "a mailbox you disconnected") != 2 {
		t.Errorf("%q", both.Text)
	}

	// No two mailboxes read alike, in every generated context.
	g := newGen(24, true)
	for range 300 {
		c := g.context()
		seen := map[string]string{}
		for _, id := range append(genMailboxes, "mbx_”x", "Work") {
			s := Render(one(sentenceCond("mailbox", "is", id)), c).Text
			if other, ok := seen[s]; ok {
				t.Fatalf("%s and %s read alike in %+v: %q", id, other, c, s)
			}
			seen[s] = id
		}
	}
}

func slicesContain(ps []contracts.SentencePart, p contracts.SentencePart) bool {
	for _, q := range ps {
		if q == p {
			return true
		}
	}
	return false
}

// Q25: the conditions a model decides are marked, in plain text by "(a
// model decides)" and in parts by Model on every part of the condition, and
// only they are: checked on every generated formula against an account of
// which conditions are the model's that does not read the sentence's words.
func TestModelConditionsAreMarkedAndOnlyThey(t *testing.T) {
	g := newGen(25, true)
	for range 1500 {
		f, c := g.formula(), g.context()
		s := Render(f, c)
		models := 0
		for _, grp := range f.Formula.All {
			for _, cd := range grp.Any {
				if cd.Op == contracts.OpAbout || cd.Op == contracts.OpMentions {
					models++
				}
			}
		}
		marks := 0
		for i, line := range s.Lines {
			parts := line.Parts
			if len(s.Lead) == 0 && len(f.Formula.All) == 1 {
				parts = parts[1:] // "Mail "
			}
			for len(parts) > 0 && !parts[len(parts)-1].Value && (parts[len(parts)-1].Text == english.utc || parts[len(parts)-1].Text == english.end) {
				parts = parts[:len(parts)-1]
			}
			if len(f.Formula.All) == 0 {
				continue
			}
			var segments [][]contracts.SentencePart
			start := 0
			for j, p := range parts {
				if !p.Value && p.Text == english.or {
					if p.Model {
						t.Fatalf("the separator is marked: %q", s.Text)
					}
					segments, start = append(segments, parts[start:j]), j+1
				}
			}
			segments = append(segments, parts[start:])
			grp := f.Formula.All[i]
			if len(segments) != len(grp.Any) {
				t.Fatalf("%d segments for %d conditions: %q", len(segments), len(grp.Any), s.Text)
			}
			for j, seg := range segments {
				model := grp.Any[j].Op == contracts.OpAbout || grp.Any[j].Op == contracts.OpMentions
				for _, p := range seg {
					if p.Model != model {
						t.Fatalf("part %q of %+v marked %v: %q", p.Text, grp.Any[j], p.Model, s.Text)
					}
					if !p.Value && p.Text == english.model {
						marks++
					}
				}
			}
		}
		if marks != models {
			t.Fatalf("%d marks for %d conditions a model decides: %q", marks, models, s.Text)
		}
		for _, p := range s.Lead {
			if p.Model {
				t.Fatalf("the lead is marked: %q", s.Text)
			}
		}
	}
}

// Q43: presentation. Mailboxes labelled “A”, or that arrived at your
// mailbox “B”, “A” and “B” give different sentences for mailbox is m1 and
// mailbox is m2 or mailbox is m3, each read back; a”b@example.com is escaped
// in its quotes; values that are template words with quotes and backslashes
// stay one part. (That a local part mixing Latin and Cyrillic is refused is
// Parse's, pull request 1, and joins this test once it merges.)
func TestPresentation(t *testing.T) {
	c := Context{Lang: "en", Mailboxes: map[string]MailboxName{
		m1: {Label: "A”, or that arrived at your mailbox “B", Address: addr1},
		m2: {Label: "A", Address: addr2},
		m3: {Label: "B", Address: addr3},
	}}
	a := readsBack(t, one(sentenceCond("mailbox", "is", m1)), c)
	b := readsBack(t, one(sentenceCond("mailbox", "is", m2), sentenceCond("mailbox", "is", m3)), c)
	if a.Text == b.Text || reflect.DeepEqual(a.Lines, b.Lines) {
		t.Fatalf("read alike: %q", a.Text)
	}
	if a.Text != `Mail that arrived at your mailbox “A\”, or that arrived at your mailbox \“B”.` {
		t.Errorf("%q", a.Text)
	}
	if b.Text != `Mail that arrived at your mailbox “A”, or that arrived at your mailbox “B”.` {
		t.Errorf("%q", b.Text)
	}

	if s := readsBack(t, one(sentenceCond("from_address", "is", "a”b@example.com")), c); s.Text != `Mail sent from “a\”b@example.com”.` {
		t.Errorf("%q", s.Text)
	}
	for _, v := range []string{`not sent from “x”, or with attachments`, `\”. Times are in UTC.`, `\`, `”`} {
		s := readsBack(t, one(sentenceCond("subject", "contains", v)), c)
		var vals []contracts.SentencePart
		for _, p := range s.Lines[0].Parts {
			if p.Value {
				vals = append(vals, p)
			}
		}
		if len(vals) != 1 || vals[0].Text != presentText(v) {
			t.Errorf("%q: value parts %#v", v, vals)
		}
	}
}

// Presenting values, each type: the escapes, the forms of a domain, of a
// time and of a size, and what is quoted because it is not of its type.
func TestPresentingValues(t *testing.T) {
	for in, want := range map[string]string{
		`a"b`:      `“a\"b”`,
		"«»„‟⹂〝〞〟": `“\«\»\„\‟\⹂\〝\〞\〟”`,
		"‘’'":      "“‘’'”", // single quotes are not delimiters
		`\`:        `“\\”`,
		"a\nb":     `“a\u{A}b”`,
		"a\u202eb": `“a\u{202E}b”`,
		"\xff":     `“\x{FF}”`,
		"\u3164":   `“\u{3164}”`,
		"x\ufe0f":  `“x\u{FE0F}”`,
		"\x00":     `“\u{0}”`,
		"a b":      "“a b”",
		"\u00a0":   `“\u{A0}”`,
	} {
		if got := presentText(in); got != want {
			t.Errorf("text %q: %q, want %q", in, got, want)
		}
		if got := scanQuoted(presentText(in), 0); len(got) != 1 || got[0].v != in {
			t.Errorf("text %q not read back: %v", in, got)
		}
	}
	for in, want := range map[string]string{
		"example.com":           "example.com",
		"xn--bcher-kva.example": "bücher.example (xn--bcher-kva.example)",
		"xn--zz.example":        "xn--zz.example",
		"xn--ab-zda.com":        "xn--ab-zda.com", // a«b.com: UTS 46 gives it, and its quote is never shown
		"xn--abc-.com":          "xn--abc-.com",   // abc.com, which is another domain
		"xn--n3h.com":           "xn--n3h.com",    // ☃.com: only letters, marks and digits are shown
		"xn--zca.de":            "ß.de (xn--zca.de)",
		"a..b":                  "a..b",
		"Example.com":           "“Example.com”",
		"":                      "“”",
	} {
		if got := presentDomain(in); got != want {
			t.Errorf("domain %q: %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"ann@xn--bcher-kva.example": "“ann@bücher.example” (xn--bcher-kva.example)",
		"a@b@example.com":           "“a@b@example.com”",
		"no-at":                     "“no-at”",
		"x@Not A Domain":            "“x@Not A Domain”",
	} {
		if got := presentAddress(in); got != want {
			t.Errorf("address %q: %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"1": "1 byte", "999": "999 bytes", "1000": "1 KB", "1001": "1,001 bytes", "1234567": "1,234,567 bytes", "2000000": "2 MB", "0": "“0”", "-1": "“-1”", "+1": "“+1”", "1e3": "“1e3”"} {
		if got := presentSize(in, &english); got != want {
			t.Errorf("size %q: %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"2000-01-01T00:00:00Z":      "1 January 2000",
		"2099-12-31T00:01:00Z":      "31 December 2099, 00:01",
		"2026-07-04T13:00:00Z":      "4 July 2026, 13:00",
		"2026-07-04T13:00:01Z":      "4 July 2026, 13:00:01",
		"2026-07-04T13:00:00+02:00": "“2026-07-04T13:00:00+02:00”",
	} {
		if got, _ := presentTime(in, &english); got != want {
			t.Errorf("time %q: %q, want %q", in, got, want)
		}
	}
	if !mailboxAddress(addr1) || mailboxAddress("a@b@c") || mailboxAddress("@b.c") || mailboxAddress("A@b.c") || mailboxID("mbx_AAAAAAAAAAAAAAAAAAAAAAAAAA") || mailboxID("mbx_a") {
		t.Error("shapes")
	}
}
