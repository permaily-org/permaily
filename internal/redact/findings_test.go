package redact_test

import (
	"context"
	"strings"
	"testing"
	"unicode"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/redact"
)

// The code review of pull request 2: each input is removed whole when only
// its kind is asked, on the one View, as the review reproduced it.
func TestReviewFindingsAreRemovedWhole(t *testing.T) {
	for _, c := range []struct {
		name string
		kind contracts.ShapeKind
		in   string
	}{
		{"finding 1: default-ignorable phone", contracts.ShapePhoneNumber, "555឴123឴4567"},
		{"finding 1: Hangul choseong filler", contracts.ShapePhoneNumber, "555ᅟ123ᅟ4567"},
		{"finding 1: Hangul jungseong filler", contracts.ShapePhoneNumber, "555ᅠ123ᅠ4567"},
		{"finding 1: Hangul filler", contracts.ShapePhoneNumber, "555ㅤ123ㅤ4567"},
		{"finding 1: halfwidth Hangul filler", contracts.ShapePhoneNumber, "555ﾠ123ﾠ4567"},
		{"finding 2: quoted address", contracts.ShapeEmailAddress, `"private person"@example.com`},
		{"finding 2: domain literal", contracts.ShapeEmailAddress, `alice@[192.0.2.1]`},
		{"finding 2: apostrophe local", contracts.ShapeEmailAddress, `o'connor@example.com`},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := redact.NewView("", c.in)
			var spans []redact.Span
			for _, p := range fixed.Shapes(v.Text, []contracts.ShapeKind{c.kind}) {
				spans = append(spans, p.Span)
			}
			if _, out := redact.Join(v, spans); out != contracts.RemovedMarker {
				t.Errorf("got %q, want complete removal", out)
			}
		})
	}
}

// Finding 1: every default-ignorable code point (Unicode's
// Default_Ignorable_Code_Point: Cf, Other_Default_Ignorable_Code_Point and
// the variation selectors), written once or three times between the digits
// of a group whose parts are each below a digit kind's least, and around
// the group, neither breaks the group nor is left inside the marker.
func TestFinding1EveryDefaultIgnorableRuneIsTransparentInADigitGroup(t *testing.T) {
	// Each kind's group, cut into parts each below its least.
	groups := map[contracts.ShapeKind][]string{
		contracts.ShapePhoneNumber:   {"555", "123", "4567"},
		contracts.ShapeCardNumber:    {"4111", "1111", "1111", "1111"},
		contracts.ShapeAccountNumber: {"0001", "2345", "6789"},
		contracts.ShapeNumericCode:   {"48", "29", "13"},
	}
	tables := []*unicode.RangeTable{unicode.Cf, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector}
	seen := 0
	for _, table := range tables {
		for _, rg := range table.R16 {
			for r := rune(rg.Lo); r <= rune(rg.Hi); r += rune(rg.Stride) {
				checkTransparent(t, groups, r)
				seen++
			}
		}
		for _, rg := range table.R32 {
			for r := rune(rg.Lo); r <= rune(rg.Hi); r += rune(rg.Stride) {
				checkTransparent(t, groups, r)
				seen++
			}
		}
	}
	if seen < 4000 {
		t.Fatalf("only %d runes of the property were tried", seen)
	}
}

func checkTransparent(t *testing.T, groups map[contracts.ShapeKind][]string, r rune) {
	t.Helper()
	if unicode.IsSpace(r) {
		return // white space is a separator, never part of the property
	}
	ig := string(r)
	for k, parts := range groups {
		for _, sep := range []string{ig, ig + ig + ig, ig + "-" + ig} {
			text := "Ref " + ig + strings.Join(parts, sep) + ig + " ok"
			out, _ := redacted(text, []contracts.ShapeKind{k})
			if want := "Ref " + ig + contracts.RemovedMarker + ig + " ok"; out != want {
				t.Fatalf("%s, %U: %q left %q", k, r, text, out)
			}
			if again, _ := redacted(out, []contracts.ShapeKind{k}); again != out {
				t.Fatalf("%s, %U: redacted twice %q", k, r, again)
			}
		}
	}
}

// Finding 2: an address's local part, quoted or with an apostrophe, and
// its domain literal leave nothing of the address, in a message's text
// taken from its HTML as in its plain text.
func TestFinding2AddressesLeaveNoLocalPartOrDomainLiteral(t *testing.T) {
	email := []contracts.ShapeKind{contracts.ShapeEmailAddress}
	for in, want := range map[string]string{
		`"private person"@example.com`:            "[removed]",
		`"a b"@[192.0.2.1]`:                       "[removed]",
		`<"private person"@example.com>`:          "<[removed]>",
		`Mail "private person"@example.com today`: "Mail [removed] today",
		`alice@[192.0.2.1]`:                       "[removed]",
		`alice@[IPv6:2001:db8::1]`:                "[removed]",
		`Write alice@[192.0.2.1].`:                "Write [removed].",
		`o'connor@example.com`:                    "[removed]",
		"`code`@example.com":                      "[removed]",
		`Mail o'connor@example.com, thanks`:       "Mail [removed], thanks",
	} {
		if out, _ := redacted(in, email); out != want {
			t.Errorf("%q left %q, want %q", in, out, want)
		}
	}
	raw := "Subject: Hi\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		"<p>Mail o&#39;connor@example.com or &quot;private person&quot;@example.com or alice@[192.0.2.1]</p>\r\n"
	text, _, _ := inbox.MessageText(context.Background(), []byte(raw), inbox.MessageTextMax)
	if !strings.Contains(text, "o'connor@") || !strings.Contains(text, `"private person"@`) {
		t.Fatalf("the text is not what the test meant: %q", text)
	}
	v := redact.NewView("Hi", text)
	var spans []redact.Span
	for _, p := range fixed.Shapes(v.Text, email) {
		spans = append(spans, p.Span)
	}
	_, b := redact.Join(v, spans)
	if strings.TrimSpace(b) != "Mail [removed] or [removed] or [removed]" {
		t.Errorf("%q", b)
	}
}
