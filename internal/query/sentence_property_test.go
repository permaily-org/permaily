package query

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// generated is how many formulas each property test draws, of each kind:
// canonical values, and values a Canonical built by hand may hold.
const generated = 2000

// Q21: injective. The sentence of every generated formula, in its plain text
// and in its parts, is read back as that formula and as no other; for every
// language with a table; with two mailboxes of the formulas outside the
// context, which read back as two.
func TestNoTwoFormulasReadAlike(t *testing.T) {
	for tag := range languages {
		for _, hostile := range []bool{false, true} {
			g := newGen(21, hostile)
			disconnected := map[string]bool{}
			for range generated {
				f, c := g.formula(), g.context()
				c.Lang = tag
				readsBack(t, f, c)
				for _, grp := range f.Formula.All {
					for _, cd := range grp.Any {
						if _, ok := c.Mailboxes[cd.Value]; cd.Var == contracts.VarMailbox && !ok {
							disconnected[cd.Value] = true
						}
					}
				}
			}
			if !disconnected[m4] || !disconnected[m5] {
				t.Errorf("%s: the run had no two disconnected mailboxes: %v", tag, disconnected)
			}
		}
	}
}

// Q21: rules too: a rule of several generated formulas is read back as its
// formulas.
func TestNoTwoRulesReadAlike(t *testing.T) {
	g := newGen(211, true)
	for range generated / 10 {
		c := g.context()
		fs := make([]*Canonical, 1+g.r.IntN(4))
		for i := range fs {
			fs[i] = g.formula()
		}
		got, err := parseRule(RenderRule(fs, c).Text, c)
		if err != nil || len(got) != len(fs) {
			t.Fatalf("%v: %d formulas of %d", err, len(got), len(fs))
		}
		for i := range fs {
			if !sameFormula(got[i], fs[i]) {
				t.Fatalf("formula %d not read back", i)
			}
		}
	}
}

// Q22: no injection. Values that are every template's fixed words, the
// separator, an end, an item, quotes escaped and not, backslashes,
// right-to-left text, the longest values, and code points normalisation
// removes (a line break, bidirectional controls): in every slot that holds a
// text, an address or a label, each stays one value part, its quotes and
// backslashes escaped inside its own quotes, and the formula is read back.
// The value part expected is presentText's own: this checks that every slot
// writes its value through the one presentation and stays one part, not that
// the presentation escapes the right characters, which the goldens written
// by hand check (sentence_review_test.go).
func TestValuesCannotAddWords(t *testing.T) {
	vals := append(append([]string(nil), hostileTexts...), fixedWords(&english)...)
	for _, v := range notNormalised {
		vals = append(vals, v, "x"+v+"y")
	}
	slots := []func(v string) (contracts.Condition, string){
		func(v string) (contracts.Condition, string) {
			return sentenceCond("subject", "contains", v), presentText(v)
		},
		func(v string) (contracts.Condition, string) {
			return not(sentenceCond("body", "contains", v)), presentText(v)
		},
		func(v string) (contracts.Condition, string) {
			return sentenceCond("attachment_name", "contains", v), presentText(v)
		},
		func(v string) (contracts.Condition, string) { return sentenceCond("list", "is", v), presentText(v) },
		func(v string) (contracts.Condition, string) {
			return sentenceCond("message", "about", v), presentText(v)
		},
		func(v string) (contracts.Condition, string) {
			return not(sentenceCond("message", "mentions", v)), presentText(v)
		},
		func(v string) (contracts.Condition, string) {
			return sentenceCond("from_address", "is", v+"@example.com"), presentText(v + "@example.com")
		},
		func(v string) (contracts.Condition, string) {
			return sentenceCond("recipient_address", "is", v+"@xn--bcher-kva.example"), presentText(v+"@bücher.example") + " (xn--bcher-kva.example)"
		},
	}
	for _, v := range vals {
		for _, slot := range slots {
			c, want := slot(v)
			for _, f := range []*Canonical{
				one(c),
				one(sentenceCond("attachments", "exists", ""), c, sentenceCond("list", "exists", "")),
				formula([]contracts.Condition{c}, []contracts.Condition{c, not(c)}),
			} {
				s := readsBack(t, f, goldenContext)
				for _, line := range s.Lines {
					for _, p := range line.Parts {
						if p.Value && p.Text != want {
							t.Fatalf("value %q: part %q, want %q", v, p.Text, want)
						}
					}
				}
				if got := strings.Count(s.Text, "\n"); got != max(len(f.Formula.All), 1)-1+boolInt(len(f.Formula.All) > 1) {
					t.Fatalf("value %q added a line: %q", v, s.Text)
				}
			}
		}
		label := Context{Lang: "en", Mailboxes: map[string]MailboxName{m1: {v, addr1}, m2: {"Work", addr2}}}
		s := readsBack(t, one(sentenceCond("mailbox", "is", m1), sentenceCond("mailbox", "is", m2)), label)
		if mailboxLabel(v) && !slices.Contains(s.Lines[0].Parts, contracts.SentencePart{Text: presentText(v), Value: true}) {
			t.Fatalf("label %q is not one value part: %#v", v, s.Lines[0].Parts)
		}
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// sentenceMax is the bound on a sentence of ADR 0027 ("How long a sentence
// gets"): under 100 KiB.
const sentenceMax = 100 << 10

// Q43: the longest sentence is under 100 KiB: 64 conditions, each the
// longest presentation of its kind (an address of a local part of 64
// backslashes at the domain of the longest Unicode form, a text of 100
// quotation marks, a label of 64), and every generated formula of canonical
// values.
func TestTheLongestSentenceIsUnder100KiB(t *testing.T) {
	d := longestUnicodeDomain()
	if len(d) != 253 || unicodeDomain(d) == d {
		t.Fatalf("the longest domain is %d bytes, Unicode form %v", len(d), unicodeDomain(d) != d)
	}
	worst := map[string]contracts.Condition{
		"address": not(sentenceCond("recipient_address", "is", strings.Repeat(`\`, 64)+"@"+d)),
		"domain":  not(sentenceCond("recipient_domain", "is", d)),
		"text":    not(sentenceCond("attachment_name", "contains", strings.Repeat("〝", contracts.MaxTextRunes))),
		"topic":   not(sentenceCond("subject", "about", strings.Repeat("〝", contracts.MaxTextRunes))),
		"mailbox": not(sentenceCond("mailbox", "is", m1)),
	}
	label := Context{Lang: "en", Mailboxes: map[string]MailboxName{m1: {strings.Repeat("〝", 64), addr1}}}
	longest := 0
	for kind, c := range worst {
		for _, shape := range [][2]int{{2, 32}, {32, 2}} {
			var groups [][]contracts.Condition
			for range shape[0] {
				groups = append(groups, slices.Repeat([]contracts.Condition{c}, shape[1]))
			}
			s := Render(formula(groups...), label)
			if len(s.Text) >= sentenceMax {
				t.Errorf("%s: %d bytes", kind, len(s.Text))
			}
			longest = max(longest, len(s.Text))
		}
	}
	if longest < 80_000 {
		t.Errorf("the worst case is %d bytes: not the worst", longest)
	}
	g := newGen(43, false)
	for range generated {
		if s := Render(g.formula(), g.context()); len(s.Text) >= sentenceMax {
			t.Fatalf("%d bytes", len(s.Text))
		}
	}
}

// The reader is a check only if it fails: a sentence that is not one, a
// sentence one character off, and parts with a value made fixed or a fixed
// part made a value are not read back.
func TestTheReaderRefusesWhatRenderDoesNotWrite(t *testing.T) {
	good := Render(formula([]contracts.Condition{sentenceCond("subject", "contains", "x")}, []contracts.Condition{sentenceCond("size", "below", "2048")}), goldenContext)
	for _, text := range []string{
		"", "Mail.", "Mail sent from.", "Mail whose subject contains the text “x”",
		"Mail whose subject contains the text “x”, or.", "Mail whose subject contains the text “x\\”.",
		"Mail whose subject contains the text “x“”.", "Mail whose subject contains the text “\\q”.",
		"Mail whose subject contains the text “\\u{110000}”.", "Mail whose subject contains the text “\\u{41”.",
		"Mail whose subject contains the text “\\x{100}”.", "Mail whose subject contains the text “\xff”.",
		"Mail of 1,000 bytes or more.", "Mail received on or after 1 Brumaire 2026.",
		"Mail received on or after 1 January 2026, 9:30. Times are in UTC.",
		"Mail received on or after 1 January 2026.",
		"Mail about “x”.",
		"Mail that meets all of these:\n- with attachments",
		"Mail that meets all of these:\n- with attachments.",
		"Mail that meets all of these:\nwith attachments\n- of 1 MB or more.",
		"Mail sent from an address at bücher.example (xn--zz.example) or a subdomain of it.",
		"Mail sent from “a@bücher.example” (example.com).",
		"Mail that arrived at your mailbox “Nobody”.",
		"Mail that arrived at a mailbox you disconnected (" + m1 + ").",
		strings.Replace(good.Text, "\n- ", "\n", 1),
		good.Text + " ",
	} {
		if f, err := parseSentence(text, goldenContext); err == nil {
			t.Errorf("%q was read as %+v", text, f)
		}
	}
	if _, err := parseParts(contracts.Sentence{}, goldenContext); err == nil {
		t.Error("an empty sentence was read")
	}
	for i, line := range good.Lines {
		for j := range line.Parts {
			bad := Render(formula([]contracts.Condition{sentenceCond("subject", "contains", "x")}, []contracts.Condition{sentenceCond("size", "below", "2048")}), goldenContext)
			bad.Lines[i].Parts[j].Value = !bad.Lines[i].Parts[j].Value
			if _, err := parseParts(bad, goldenContext); err == nil {
				t.Errorf("parts with %q flipped were read", line.Parts[j].Text)
			}
		}
	}
	// Two mailboxes that read alike are two readings.
	r := newSentenceReader(goldenContext, nil)
	r.names[[2]string{english.yourMailbox, ""}]["“Work”"] = []string{m1, m2}
	if got := r.readText("Mail that arrived at your mailbox “Work”."); len(got) != 2 {
		t.Errorf("%d readings", len(got))
	}
	if _, err := single([]*Canonical{{Never: true}, {}}); !errors.Is(err, errAmbiguous) {
		t.Error(err)
	}
}

// FuzzSentenceReadsBack: whatever the values and the labels, two conditions
// of any kind and a mailbox named by any label read back as themselves, from
// the plain text and from the parts.
func FuzzSentenceReadsBack(f *testing.F) {
	f.Add(uint8(0), uint8(5), true, "a”, or b", `\“`, "Work")
	f.Add(uint8(12), uint8(16), false, "2026-01-01T00:00:00Z", "x‮", "")
	f.Add(uint8(9), uint8(1), true, "application/x.", "xn--bcher-kva.example", "”")
	f.Fuzz(func(t *testing.T, k1, k2 uint8, neg bool, v1, v2, label string) {
		c := func(k uint8, v string) contracts.Condition {
			kind := conditionKinds[int(k)%len(conditionKinds)]
			if kind.op == contracts.OpExists {
				v = ""
			}
			return contracts.Condition{Var: kind.v, Op: kind.op, Value: v, Not: neg && kind.neg}
		}
		ctx := Context{Lang: "en", Mailboxes: map[string]MailboxName{m1: {label, addr1}, m2: {v2, addr2}}}
		for _, fm := range []*Canonical{
			one(c(k1, v1), c(k2, v2), sentenceCond("mailbox", "is", m1)),
			formula([]contracts.Condition{c(k1, v2)}, []contracts.Condition{c(k2, v1), sentenceCond("mailbox", "is", m2)}),
		} {
			readsBack(t, fm, ctx)
		}
	})
}
