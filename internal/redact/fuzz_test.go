package redact_test

import (
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/redact"
)

func seeds(f *testing.F, add func(string)) {
	f.Helper()
	for _, c := range readCases(f) {
		add(c.In)
	}
	for _, s := range []string{"", "abc.de5551234567", "GB82 WEST 1234 ABCD5551234567", "a@b.c5551234567@d", "\xff1234\x80", "你好。谢谢", strings.Repeat("x ", 600)} {
		add(s)
	}
}

// kindsOf is the kinds whose bits are set in mask.
func kindsOf(mask uint8) []contracts.ShapeKind {
	var out []contracts.ShapeKind
	for i, k := range contracts.ShapeKinds {
		if mask&(1<<i) != 0 {
			out = append(out, k)
		}
	}
	return out
}

// FuzzShapes: Shapes never panics; its pieces are within the text, sorted,
// of the kinds asked, on rune boundaries and with no invalid byte; what
// leaves is the input minus pieces, one marker a run (U23), with each
// marker counted; and redacting twice changes nothing (U9).
func FuzzShapes(f *testing.F) {
	seeds(f, func(s string) { f.Add(s, "Subject 482913", uint8(0x3f)) })
	f.Fuzz(func(t *testing.T, text, subject string, mask uint8) {
		subject = strings.ReplaceAll(subject, "\n", " ")
		kinds := kindsOf(mask)
		v := redact.NewView(subject, text)
		at := boundaries(v.Text)
		ps := fixed.Shapes(v.Text, kinds)
		spans := make([]redact.Span, len(ps))
		for i, p := range ps {
			if p.Start < 0 || p.End > len(v.Text) || p.End <= p.Start || !slices.Contains(kinds, p.Kind) || (i > 0 && p.Start < ps[i-1].Start) ||
				!at[p.Start] || !at[p.End] || !utf8.ValidString(v.Text[p.Start:p.End]) {
				t.Fatalf("piece %+v of %q", p, v.Text)
			}
			if strings.Contains(v.Text[p.Start:p.End], "\n") {
				t.Fatalf("piece %+v crosses a line", p)
			}
			spans[i] = p.Span
		}
		s, b := redact.Join(v, spans)
		ws, wb := joined(v, spans)
		if s != ws || b != wb {
			t.Fatalf("joined %q / %q, want %q / %q", s, b, ws, wb)
		}
		runs := 0
		for _, part := range []redact.Span{v.Subject, v.Body} {
			for _, u := range redact.Union(spans) {
				if min(u.End, part.End) > max(u.Start, part.Start) {
					runs++
				}
			}
		}
		kept := strings.Count(ws+"\n"+wb, contracts.RemovedMarker)
		if added := kept - countOutside(v.Text, redact.Union(spans)); added != runs {
			t.Fatalf("%d markers added for %d runs", added, runs)
		}
		if again := fixed.Shapes(s+"\n"+b, kinds); len(again) != 0 {
			t.Fatalf("redacted %q still has %+v", s+"\n"+b, again)
		}
	})
}

// countOutside is how many markers text has that no span touches.
func countOutside(text string, spans []redact.Span) int {
	n := 0
	for i := 0; ; {
		k := strings.Index(text[i:], contracts.RemovedMarker)
		if k < 0 {
			return n
		}
		start, end := i+k, i+k+len(contracts.RemovedMarker)
		touched := false
		for _, s := range spans {
			touched = touched || (s.Start < end && s.End > start)
		}
		if !touched {
			n++
		}
		i = start + 1
	}
}

// FuzzSentences: Sentences never panics, its map keeps the invariants
// (Check) and is the same twice, and every rune before its Cut that is not
// white space is in a sentence (the re-review's N1, checked here without
// Check).
func FuzzSentences(f *testing.F) {
	seeds(f, func(s string) { f.Add("Subject. Two", s) })
	f.Fuzz(func(t *testing.T, subject, text string) {
		v := redact.NewView(subject, text)
		m := fixed.Sentences(v)
		if err := m.Check(v); err != nil {
			t.Fatalf("%q: %+v: %v", v.Text, m, err)
		}
		if m.Digest(v) != fixed.Sentences(v).Digest(v) {
			t.Fatal("two maps of one view")
		}
		if m.Cut < len(v.Text) && len(m.Sentences) < redact.MeaningSentencesMax && len(v.Text) <= redact.MeaningTextMax {
			t.Fatalf("cut at %d of %d with %d sentences", m.Cut, len(v.Text), len(m.Sentences))
		}
		covered(t, v, m)
	})
}

// covered fails unless every rune of v before m.Cut that is not white
// space is in one of m's sentences.
func covered(t *testing.T, v redact.View, m redact.SentenceMap) {
	t.Helper()
	k := 0
	for i := 0; i < m.Cut; {
		r, n := utf8.DecodeRuneInString(v.Text[i:])
		for k < len(m.Sentences) && m.Sentences[k].End <= i {
			k++
		}
		in := k < len(m.Sentences) && m.Sentences[k].Start <= i
		if !in && (!unicode.IsSpace(r) || (r == utf8.RuneError && n == 1)) {
			t.Fatalf("%q at %d is in no sentence of %+v", r, i, m)
		}
		i += n
	}
}

// FuzzCheck: Check never panics on any map, and a map it keeps leaves
// nothing before its Cut that is not white space out of every sentence.
func FuzzCheck(f *testing.F) {
	f.Add("Hi there.", "Text. More text.", []byte{0, 9, 10, 15, 16, 26}, 26)
	f.Add("", "añb c", []byte{1, 3, 3, 5}, 7)
	f.Fuzz(func(t *testing.T, subject, text string, spans []byte, cut int) {
		v := redact.NewView(subject, text)
		m := redact.SentenceMap{Cut: cut}
		for i := 0; i+1 < len(spans); i += 2 {
			m.Sentences = append(m.Sentences, redact.Span{Start: int(spans[i]), End: int(spans[i+1])})
		}
		if m.Check(v) != nil {
			return
		}
		covered(t, v, m)
		for _, s := range m.Sentences {
			if utf8.RuneCountInString(v.Text[s.Start:s.End]) > redact.SentenceRunesMax || strings.TrimSpace(v.Text[s.Start:s.End]) == "" {
				t.Fatalf("kept %+v", s)
			}
		}
	})
}

// FuzzJoin: Join of any spans is what joined says, never panics, and never
// writes a marker across the line between the subject and the text.
func FuzzJoin(f *testing.F) {
	f.Add("Code 123456", "Call 5551234 now.", []byte{5, 11, 17, 24})
	f.Add("", "a\x80\x80ñ\xff", []byte{1, 2, 4, 5})
	f.Fuzz(func(t *testing.T, subject, text string, spans []byte) {
		v := redact.NewView(subject, text)
		var ss []redact.Span
		for i := 0; i+1 < len(spans); i += 2 {
			ss = append(ss, redact.Span{Start: int(spans[i]), End: int(spans[i+1])})
		}
		s, b := redact.Join(v, ss)
		ws, wb := joined(v, ss)
		if s != ws || b != wb {
			t.Fatalf("%q %v: %q / %q, want %q / %q", v.Text, ss, s, b, ws, wb)
		}
	})
}
