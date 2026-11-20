package redact_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/redact"
	"github.com/permaily-org/permaily/internal/redact/redacttest"
)

var fixed = redact.Fixed{}

// redacted is text as it leaves with every piece of kinds removed (Shapes
// on the text, then Join), and each kind's count as RemovedSummary counts
// it: one a run of touching pieces, under the first of contracts.ShapeKinds
// among them.
func redacted(text string, kinds []contracts.ShapeKind) (string, map[contracts.ShapeKind]int) {
	ps := fixed.Shapes(text, kinds)
	v := redact.View{Text: text, Subject: redact.Span{}, Body: redact.Span{Start: 0, End: len(text)}}
	spans := make([]redact.Span, len(ps))
	for i, p := range ps {
		spans[i] = p.Span
	}
	_, out := redact.Join(v, spans)
	counts := map[contracts.ShapeKind]int{}
	order := func(k contracts.ShapeKind) int { return slices.Index(contracts.ShapeKinds, k) }
	for i := 0; i < len(ps); {
		end, first := ps[i].End, ps[i].Kind
		j := i + 1
		for ; j < len(ps) && ps[j].Start <= end; j++ {
			end = max(end, ps[j].End)
			if order(ps[j].Kind) < order(first) {
				first = ps[j].Kind
			}
		}
		counts[first]++
		i = j
	}
	return out, counts
}

// U8: every golden case of testdata/shapes.jsonl is what Fixed answers,
// its output and its counts, and its output redacted again is itself.
func TestU8EveryGoldenCaseIsWhatFixedAnswers(t *testing.T) {
	for _, c := range readCases(t) {
		out, counts := redacted(c.In, c.Kinds)
		if out != c.Out {
			t.Errorf("%s:\n in %q\ngot %q\nwant %q", c.Name, c.In, out, c.Out)
		}
		want := c.Shapes
		if want == nil {
			want = map[contracts.ShapeKind]int{}
		}
		if fmt.Sprint(counts) != fmt.Sprint(want) {
			t.Errorf("%s: counted %v, want %v", c.Name, counts, want)
		}
		if again, _ := redacted(out, c.Kinds); again != out {
			t.Errorf("%s: redacted twice %q", c.Name, again)
		}
	}
}

// U8's texts as a message gives them: decoded from base64 and from
// quoted-printable (an invisible character and a soft line break inside a
// piece), and taken from the HTML when there is no plain part, on the one
// View of the subject and the text.
func TestU8DecodedAndHTMLTextsLoseTheirPieces(t *testing.T) {
	body := "Call +1 (555) 123-4567 or ana@example.com, see example。com/reset. Code 482913."
	b64 := base64.StdEncoding.EncodeToString([]byte(body))
	for name, raw := range map[string]string{
		"base64": "Subject: Your code 482913\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n" +
			"Content-Transfer-Encoding: base64\r\n\r\n" + b64[:40] + "\r\n" + b64[40:] + "\r\n",
		"quoted-printable": "Subject: Your code 482913\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n" +
			"Content-Transfer-Encoding: quoted-printable\r\n\r\nCall +1 (555) 123=E2=80=8B-4567 or ana@exam=\r\nple.com, see example=E3=80=82com/re=\r\nset. Code 482913.\r\n",
		"html": "Subject: Your code 482913\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
			"<p>Call +1&nbsp;(555)&nbsp;123-4567 or <b>ana@example.com</b>, see <a href=\"https://example.com/reset\">example&#x3002;com/reset</a>.</p><p>Code 482913.</p>\r\n",
	} {
		text, _, _ := inbox.MessageText(context.Background(), []byte(raw), inbox.MessageTextMax)
		if !strings.Contains(text, "ana@") || !strings.Contains(text, "482913") {
			t.Fatalf("%s: the text is not what the test meant: %q", name, text)
		}
		v := redact.NewView("Your code 482913", text)
		var spans []redact.Span
		for _, p := range fixed.Shapes(v.Text, contracts.ShapeKinds) {
			spans = append(spans, p.Span)
		}
		s, b := redact.Join(v, spans)
		if s != "Your code [removed]" {
			t.Errorf("%s: subject %q", name, s)
		}
		for _, leak := range []string{"555", "4567", "ana", "example", "482913", "@"} {
			if strings.Contains(b, leak) {
				t.Errorf("%s: %q left in %q", name, leak, b)
			}
		}
		if !strings.Contains(b, "Call [removed] or [removed], see [removed]") {
			t.Errorf("%s: %q", name, b)
		}
	}
}

// The at signs are the runes whose NFKC form is "@", as golang.org/x/text
// says over every rune: the scanner keeps the set rather than normalising
// every rune it reads.
func TestTheAtSignsAreThoseNFKCMakesAt(t *testing.T) {
	var at []rune
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if utf8.ValidRune(r) && norm.NFKC.String(string(r)) == "@" {
			at = append(at, r)
		}
	}
	if !slices.Equal(at, []rune{'@', 0xFE6B, 0xFF20}) {
		t.Fatalf("NFKC makes @ of %U", at)
	}
	for _, r := range at {
		if got := fixed.Shapes("ana"+string(r)+"example.com", []contracts.ShapeKind{contracts.ShapeEmailAddress}); len(got) != 1 {
			t.Errorf("%U is not an at sign: %v", r, got)
		}
	}
}

// Fixed keeps the contract every Redactor keeps.
func TestTheFixedRedactorKeepsTheContract(t *testing.T) {
	redacttest.Contract(t, fixed)
}

// inputs is texts for the properties: the golden cases', hostile ones, and
// random texts over an alphabet that makes pieces and near-pieces often.
func inputs(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, c := range readCases(t) {
		out = append(out, c.In, c.Out)
	}
	out = append(out,
		"abc.de5551234567 x", "GB82 WEST 1234 ABCD5551234567", "a@b.c5551234567@d", "abc5551234567tel:+1",
		"x5551234567https://e.com", "foo.com/555 1234x", "\xff5551234567\x80abc", "a\x80\x80 ana@example.com\xff",
		"[removed]555 1234[removed]", "ww\u200bw.example.org", "(555)(1234)", "+(555) 1234567", "1.2.3.4.5.6.7.8",
		"a@b@c.com", "@a.com", "a@.com", "a@b..com", "x.y.z.com.", "...com", "e.g. i.e. U.S.A.", "http://", "://x.com",
		"1://x;http://y", "DE89\u00a03704\u00a00044\u00a00532\u00a00130\u00a000", "٠١٢٣٤٥٦٧٨٩ ۰۱۲۳", "Ｇ８２", "ab\u0301c.com",
	)
	alphabet := []string{"1", "2", "9", " ", "-", ".", ",", "/", "(", ")", "+", "@", "a", "Z", "G", "B", "www", "com", "de", "://",
		"http", "tel:", "mailto:", "\u200b", "\u00a0", "\n", "。", "＠", "\xff", "\x80", "é", "例", "[removed]", ":", "?", "!", "\t", "_"}
	rng := rand.New(rand.NewPCG(40, 2))
	for range 3000 {
		var b strings.Builder
		for range rng.IntN(40) {
			b.WriteString(alphabet[rng.IntN(len(alphabet))])
		}
		out = append(out, b.String())
	}
	return out
}

// boundaries is where the runes of s start, and its end, an invalid byte
// being a rune of its own.
func boundaries(s string) map[int]bool {
	at := map[int]bool{len(s): true}
	for i := 0; i < len(s); {
		at[i] = true
		_, n := utf8.DecodeRuneInString(s[i:])
		i += n
	}
	return at
}

// kindSets is every single kind and all of them.
func kindSets() [][]contracts.ShapeKind {
	sets := [][]contracts.ShapeKind{contracts.ShapeKinds}
	for _, k := range contracts.ShapeKinds {
		sets = append(sets, []contracts.ShapeKind{k})
	}
	return append(sets, []contracts.ShapeKind{contracts.ShapePhoneNumber, contracts.ShapeLink},
		[]contracts.ShapeKind{contracts.ShapeAccountNumber, contracts.ShapeEmailAddress})
}

// U9: redacting twice changes nothing, so nothing a kind matches is left in
// what leaves; pieces are of the kinds asked, on rune boundaries, never
// holding an invalid byte, sorted.
func TestU9RedactingTwiceChangesNothing(t *testing.T) {
	for _, in := range inputs(t) {
		at := boundaries(in)
		for _, kinds := range kindSets() {
			ps := fixed.Shapes(in, kinds)
			for i, p := range ps {
				if p.Start < 0 || p.End > len(in) || p.End <= p.Start || !slices.Contains(kinds, p.Kind) || (i > 0 && p.Start < ps[i-1].Start) {
					t.Fatalf("%q %v: piece %+v", in, kinds, p)
				}
				if !utf8.ValidString(in[p.Start:p.End]) || !at[p.Start] || !at[p.End] {
					t.Fatalf("%q %v: piece %+v cuts a rune or holds an invalid byte", in, kinds, p)
				}
			}
			out, _ := redacted(in, kinds)
			if left := fixed.Shapes(out, kinds); len(left) != 0 {
				t.Fatalf("%q %v: %q still has %+v", in, kinds, out, left)
			}
		}
	}
}

// U9: a digit group at or above a kind's least count is removed whatever
// its length, written with any separators, in any script's digits, and no
// digit of it is left beside the marker.
func TestU9ADigitGroupAtOrAboveItsLeastIsRemovedWhateverItsLength(t *testing.T) {
	least := map[contracts.ShapeKind]int{
		contracts.ShapePhoneNumber: 7, contracts.ShapeCardNumber: 12, contracts.ShapeAccountNumber: 8, contracts.ShapeNumericCode: 4,
	}
	seps := []string{"", " ", "-", ".", ",", "/", "\u00a0", "\u3000", "–", " -", "\u200b"}
	digits := []rune{'0', '٠', '०', '０'} // each script's zero
	rng := rand.New(rand.NewPCG(9, 9))
	for k, n := range least {
		for count := 1; count < n+60; count++ {
			var b strings.Builder
			for i := range count {
				if i > 0 {
					b.WriteString(seps[rng.IntN(len(seps))])
				}
				b.WriteRune(digits[rng.IntN(len(digits))] + rune(rng.IntN(10)))
			}
			text := "Ref " + b.String() + " ok"
			out, _ := redacted(text, []contracts.ShapeKind{k})
			removed := out == "Ref [removed] ok"
			if removed != (count >= n) {
				t.Fatalf("%s: %d digits %q left %q", k, count, text, out)
			}
		}
	}
}

// U9: 256 KiB of the inputs that are worst for a scanner (digits and
// separators alternating, at signs, dots, labels, schemes, IBAN groups) is
// read within a bound only a linear scanner keeps.
func TestU9TheWorstInputsAreReadInLinearTime(t *testing.T) {
	for _, unit := range []string{
		"1-", "1 ", "1", "a@", "a.", "a.b@", "@a.", "GB82 ", "GB82 WEST ", "www.", "://", "http://", "a://", "1.a",
		"a", "(", "1\u200b", "\xff", "a.de1", "x.y/", "tel:", "A1 ", "a@b.c ",
	} {
		text := strings.Repeat(unit, (256<<10)/len(unit))
		start := time.Now()
		fixed.Shapes(text, contracts.ShapeKinds)
		fixed.Sentences(redact.NewView("", text))
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("%q×%d took %v", unit, len(text)/len(unit), d)
		}
	}
}

func BenchmarkShapes256KiB(b *testing.B) {
	text := strings.Repeat("Call +1 (555) 123-4567 or ana@example.com, see https://example.com/a. Code 4821 on 2026-11-20. ", (256<<10)/96)
	b.SetBytes(int64(len(text)))
	for b.Loop() {
		fixed.Shapes(text, contracts.ShapeKinds)
	}
}

func BenchmarkSentences32KiB(b *testing.B) {
	v := redact.NewView("Your order", strings.Repeat("This is one sentence of a message. ", (32<<10)/35))
	b.SetBytes(int64(len(v.Text)))
	for b.Loop() {
		fixed.Sentences(v)
	}
}
