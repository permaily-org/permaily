package query

import (
	"fmt"
	"testing"
	"unicode"

	"golang.org/x/net/idna"
)

// The code review of pull request 2 (the sentence). Every expected sentence
// here is written by hand, from ADR 0027's "Presenting values", and none is
// computed with the presentation functions it checks: the read-back tests
// share the templates, the classification of quotes and the naming of
// mailboxes with Render, so a quote or an invisible character missing from
// both sides would read back and pass them.

// Finding 2: every code point of Unicode's Quotation_Mark property is
// escaped inside a quoted value except the single quotes U+0027, U+2018 and
// U+2019, in every value: an address and a label keep what a text's NFKC
// would turn into U+0022.
func TestReviewEveryQuotationMarkIsEscaped(t *testing.T) {
	ctx := func(label string) Context {
		return Context{Lang: "en", Mailboxes: map[string]MailboxName{m1: {Label: label, Address: addr1}}}
	}
	for _, tc := range []struct {
		value, address, label, text string
	}{
		{"a＂b", `Mail sent from “a\＂b@example.com”.`, `Mail that arrived at your mailbox “a\＂b”.`, `Mail whose subject contains the text “a\＂b”.`},
		{"a〝b", `Mail sent from “a\〝b@example.com”.`, `Mail that arrived at your mailbox “a\〝b”.`, `Mail whose subject contains the text “a\〝b”.`},
		{"a〞b", `Mail sent from “a\〞b@example.com”.`, `Mail that arrived at your mailbox “a\〞b”.`, `Mail whose subject contains the text “a\〞b”.`},
		{"a„b", `Mail sent from “a\„b@example.com”.`, `Mail that arrived at your mailbox “a\„b”.`, `Mail whose subject contains the text “a\„b”.`},
		{"a«b»c", `Mail sent from “a\«b\»c@example.com”.`, `Mail that arrived at your mailbox “a\«b\»c”.`, `Mail whose subject contains the text “a\«b\»c”.`},
		{"a‹b›c", `Mail sent from “a\‹b\›c@example.com”.`, `Mail that arrived at your mailbox “a\‹b\›c”.`, `Mail whose subject contains the text “a\‹b\›c”.`},
		{"「a」『b』", `Mail sent from “\「a\」\『b\』@example.com”.`, `Mail that arrived at your mailbox “\「a\」\『b\』”.`, `Mail whose subject contains the text “\「a\」\『b\』”.`},
		{"a‚b‛c", `Mail sent from “a\‚b\‛c@example.com”.`, `Mail that arrived at your mailbox “a\‚b\‛c”.`, `Mail whose subject contains the text “a\‚b\‛c”.`},
		{"a＇b｢c｣", `Mail sent from “a\＇b\｢c\｣@example.com”.`, `Mail that arrived at your mailbox “a\＇b\｢c\｣”.`, `Mail whose subject contains the text “a\＇b\｢c\｣”.`},
		{"﹁a﹂﹃b﹄", `Mail sent from “\﹁a\﹂\﹃b\﹄@example.com”.`, `Mail that arrived at your mailbox “\﹁a\﹂\﹃b\﹄”.`, `Mail whose subject contains the text “\﹁a\﹂\﹃b\﹄”.`},
		{"o'neil", `Mail sent from “o'neil@example.com”.`, `Mail that arrived at your mailbox “o'neil”.`, `Mail whose subject contains the text “o'neil”.`},
		{"a‘b’c", `Mail sent from “a‘b’c@example.com”.`, `Mail that arrived at your mailbox “a‘b’c”.`, `Mail whose subject contains the text “a‘b’c”.`},
	} {
		for _, got := range []struct {
			f    *Canonical
			c    Context
			want string
		}{
			{one(sentenceCond("from_address", "is", tc.value+"@example.com")), goldenContext, tc.address},
			{one(sentenceCond("mailbox", "is", m1)), ctx(tc.value), tc.label},
			{one(sentenceCond("subject", "contains", tc.value)), goldenContext, tc.text},
		} {
			if s := readsBack(t, got.f, got.c); s.Text != got.want {
				t.Errorf("got  %s\nwant %s", s.Text, got.want)
			}
		}
	}
	// The review's formula, as a vendor sends it: Parse keeps U+FF02 in the
	// local part (addresses are NFC), and the sentence escapes it.
	f, err := Parse([]byte(`{"v":1,"all":[{"any":[{"var":"from_address","op":"is","value":"a＂b@example.com"}]}]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := Render(f, goldenContext).Text, `Mail sent from “a\＂b@example.com”.`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// quotationMarks is Unicode's Quotation_Mark property (PropList.txt),
// written out by hand.
var quotationMarks = []rune{
	0x0022, 0x0027, 0x00AB, 0x00BB, 0x2018, 0x2019, 0x201A, 0x201B, 0x201C, 0x201D, 0x201E, 0x201F,
	0x2039, 0x203A, 0x2E42, 0x300C, 0x300D, 0x300E, 0x300F, 0x301D, 0x301E, 0x301F,
	0xFE41, 0xFE42, 0xFE43, 0xFE44, 0xFF02, 0xFF07, 0xFF62, 0xFF63,
}

// Finding 2: the escaped quotes are exactly Quotation_Mark less the three
// single quotes, and the list above is the whole property in Go's tables.
func TestReviewTheEscapedQuotesAreTheProperty(t *testing.T) {
	n := 0
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if unicode.Is(unicode.Quotation_Mark, r) {
			n++
		}
	}
	if n != len(quotationMarks) {
		t.Fatalf("Quotation_Mark has %d code points, the list %d", n, len(quotationMarks))
	}
	for _, q := range quotationMarks {
		single := q == '\'' || q == '‘' || q == '’'
		want := fmt.Sprintf("“\\%c”", q)
		if single {
			want = fmt.Sprintf("“%c”", q)
		}
		if got := presentText(string(q)); got != want {
			t.Errorf("U+%04X: %s, want %s", q, got, want)
		}
	}
}

// Finding 3: a character that does not show as itself distinctly is
// written \u{hex}, so a person never reads it as U+0020: white space other
// than U+0020, U+2800, the Hangul fillers and every default-ignorable code
// point. In a text, an address's local part and a label; a label that is
// not printable is named by the address instead.
func TestReviewInvisibleCharactersNeverReadAsASpace(t *testing.T) {
	const byAddress = "Mail that arrived at your mailbox " + addr1 + "."
	rows := []struct {
		in, shown, label string
	}{
		{"a b", "a b", "Mail that arrived at your mailbox “a b”."},
		{"a\tb", `a\u{9}b`, byAddress},
		{"a\u0085b", `a\u{85}b`, byAddress},
		{"a\u00a0b", `a\u{A0}b`, byAddress},
		{"a\u1680b", `a\u{1680}b`, byAddress},
		{"a\u2000b", `a\u{2000}b`, byAddress},
		{"a\u2003b", `a\u{2003}b`, byAddress},
		{"a\u2007b", `a\u{2007}b`, byAddress},
		{"a\u200ab", `a\u{200A}b`, byAddress},
		{"a\u2028b", `a\u{2028}b`, byAddress},
		{"a\u2029b", `a\u{2029}b`, byAddress},
		{"a\u202fb", `a\u{202F}b`, byAddress},
		{"a\u205fb", `a\u{205F}b`, byAddress},
		{"a\u3000b", `a\u{3000}b`, byAddress},
		{"a\u2800b", `a\u{2800}b`, `Mail that arrived at your mailbox “a\u{2800}b”.`},
		{"a\u115fb", `a\u{115F}b`, `Mail that arrived at your mailbox “a\u{115F}b”.`},
		{"a\u1160b", `a\u{1160}b`, `Mail that arrived at your mailbox “a\u{1160}b”.`},
		{"a\u3164b", `a\u{3164}b`, `Mail that arrived at your mailbox “a\u{3164}b”.`},
		{"a\uffa0b", `a\u{FFA0}b`, `Mail that arrived at your mailbox “a\u{FFA0}b”.`},
		{"a\u034fb", `a\u{34F}b`, `Mail that arrived at your mailbox “a\u{34F}b”.`},
		{"a\u17b4b", `a\u{17B4}b`, `Mail that arrived at your mailbox “a\u{17B4}b”.`},
		{"a\u180bb", `a\u{180B}b`, `Mail that arrived at your mailbox “a\u{180B}b”.`},
		{"a\ufe0fb", `a\u{FE0F}b`, `Mail that arrived at your mailbox “a\u{FE0F}b”.`},
		{"a\U000E0100b", `a\u{E0100}b`, `Mail that arrived at your mailbox “a\u{E0100}b”.`},
		{"a\u00adb", `a\u{AD}b`, byAddress},
		{"a\u180eb", `a\u{180E}b`, byAddress},
		{"a\u200bb", `a\u{200B}b`, byAddress},
		{"a\u2060b", `a\u{2060}b`, byAddress},
		{"a\ufeffb", `a\u{FEFF}b`, byAddress},
		{"a\U000E0041b", `a\u{E0041}b`, byAddress},
	}
	space := map[string]string{}
	for i, row := range rows {
		ctx := Context{Lang: "en", Mailboxes: map[string]MailboxName{m1: {Label: row.in, Address: addr1}}}
		for kind, tc := range map[string]struct {
			f    *Canonical
			c    Context
			want string
		}{
			"text":    {one(sentenceCond("subject", "contains", row.in)), goldenContext, "Mail whose subject contains the text “" + row.shown + "”."},
			"address": {one(sentenceCond("from_address", "is", row.in+"@example.com")), goldenContext, "Mail sent from “" + row.shown + "@example.com”."},
			"label":   {one(sentenceCond("mailbox", "is", m1)), ctx, row.label},
		} {
			s := readsBack(t, tc.f, tc.c)
			if s.Text != tc.want {
				t.Errorf("%s %q: got  %s\nwant %s", kind, row.in, s.Text, tc.want)
			}
			if i == 0 {
				space[kind] = s.Text
			} else if s.Text == space[kind] {
				t.Errorf("%s %q reads as U+0020: %s", kind, row.in, s.Text)
			}
		}
	}
}

// Finding 3, every such code point: each is written \u{hex} alone in a
// quoted value.
func TestReviewNoInvisibleCharacterIsShownAsItself(t *testing.T) {
	blank := map[rune]bool{0x2800: true, 0x115F: true, 0x1160: true, 0x3164: true, 0xFFA0: true}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		invisible := blank[r] || r != ' ' && unicode.Is(unicode.White_Space, r) ||
			unicode.In(r, unicode.Other_Default_Ignorable_Code_Point, unicode.Cf, unicode.Variation_Selector)
		if !invisible || r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		if got, want := presentText(string(r)), fmt.Sprintf("“\\u{%X}”", r); got != want {
			t.Errorf("U+%04X: %s, want %s", r, got, want)
		}
	}
}

// The note on the reader: it finds every reading, with no cap before the
// re-render filter, so an ambiguity is never hidden behind readings that are
// dropped: four mailboxes named alike are four readings, and a sentence of
// two lines with two readings each is four formulas.
func TestReviewTheReaderFindsEveryReading(t *testing.T) {
	work := [2]string{english.yourMailbox, ""}
	r := newSentenceReader(goldenContext, nil)
	r.names[work]["“Work”"] = []string{m1, m2, m3, m4}
	if got := r.readText("Mail that arrived at your mailbox “Work”."); len(got) != 4 {
		t.Errorf("one line: %d readings, want 4", len(got))
	}
	r = newSentenceReader(goldenContext, nil)
	r.names[work]["“Work”"] = []string{m1, m2}
	if got := r.readText("Mail that meets all of these:\n- that arrived at your mailbox “Work”\n- that arrived at your mailbox “Work”."); len(got) != 4 {
		t.Errorf("two lines: %d readings, want 4", len(got))
	}
}

// Finding 3, domains: a Unicode form that would hold an invisible character
// is never shown; the domain reads in ASCII.
func TestReviewADomainNeverShowsAnInvisibleCharacter(t *testing.T) {
	for _, label := range []string{"a\u034fb", "a\u2800b", "a\u3164b", "a\ufe0fb", "a\u00adb", "a\u200bb"} {
		ascii, err := idna.Punycode.ToASCII(label)
		if err != nil {
			t.Fatal(err)
		}
		d := ascii + ".example"
		if got, want := Render(one(sentenceCond("from_domain", "is", d)), goldenContext).Text, "Mail sent from an address at "+d+" or a subdomain of it."; got != want {
			t.Errorf("%q: got  %s\nwant %s", label, got, want)
		}
	}
}
