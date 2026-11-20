package redact_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/redact"
)

// joined is what Join must answer, written the slow way: each byte of v.Text
// removed or not (a span that cuts a valid rune takes the whole rune; an
// invalid byte is a rune of its own), and in each part every run of removed
// bytes one marker, everything else kept byte for byte.
func joined(v redact.View, spans []redact.Span) (subject, text string) {
	gone := make([]bool, len(v.Text))
	for _, s := range spans {
		for i := max(s.Start, 0); i < min(s.End, len(v.Text)); i++ {
			gone[i] = true
		}
	}
	for i := 0; i < len(v.Text); {
		_, n := utf8.DecodeRuneInString(v.Text[i:])
		any := false
		for k := i; k < i+n; k++ {
			any = any || gone[k]
		}
		for k := i; k < i+n; k++ {
			gone[k] = any
		}
		i += n
	}
	part := func(p redact.Span) string {
		var b strings.Builder
		for i := p.Start; i < p.End; i++ {
			switch {
			case !gone[i]:
				b.WriteByte(v.Text[i])
			case i == p.Start || !gone[i-1]:
				b.WriteString(contracts.RemovedMarker)
			}
		}
		return b.String()
	}
	return part(v.Subject), part(v.Body)
}

// U23's shapes half: on the one View of a subject and a text, what leaves
// once every shape Fixed finds is removed is the input minus pieces, each
// run of touching pieces one marker, clipped to its part, whatever the
// text (invalid bytes, markers already there, pieces at the parts' ends).
func TestU23ShapesRemovedLeaveTheInputMinusPieces(t *testing.T) {
	texts := inputs(t)
	for i, text := range texts {
		subject := texts[(i*7+3)%len(texts)]
		if strings.Contains(subject, "\n") {
			subject = ""
		}
		v := redact.NewView(subject, text)
		for _, kinds := range kindSets() {
			var spans []redact.Span
			for _, p := range fixed.Shapes(v.Text, kinds) {
				spans = append(spans, p.Span)
			}
			s, b := redact.Join(v, spans)
			ws, wb := joined(v, spans)
			if s != ws || b != wb {
				t.Fatalf("%q / %q %v:\n got %q / %q\nwant %q / %q", subject, text, kinds, s, b, ws, wb)
			}
		}
	}
}

// U23: a span next to an invalid byte takes only its own runes; a span that
// cuts a valid rune takes that rune whole.
func TestU23AJoinNextToAnInvalidByteTakesNoMore(t *testing.T) {
	v := redact.NewView("", "a\x80\x805551234\x80\xffb ñ")
	i := strings.Index(v.Text, "5551234")
	for _, c := range []struct {
		span redact.Span
		want string
	}{
		{redact.Span{Start: i, End: i + 7}, "a\x80\x80[removed]\x80\xffb ñ"},
		{redact.Span{Start: i - 1, End: i}, "a\x80[removed]5551234\x80\xffb ñ"},
		{redact.Span{Start: len(v.Text) - 1, End: len(v.Text)}, "a\x80\x805551234\x80\xffb [removed]"},
	} {
		if _, b := redact.Join(v, []redact.Span{c.span}); b != c.want {
			t.Errorf("%+v: %q, want %q", c.span, b, c.want)
		}
	}
}
