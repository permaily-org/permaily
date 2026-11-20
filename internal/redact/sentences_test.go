package redact_test

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/redact"
)

// split is the sentences Fixed finds in the View of subject and text, as
// text, after checking the map's invariants.
func split(t *testing.T, subject, text string) ([]string, redact.SentenceMap, redact.View) {
	t.Helper()
	v := redact.NewView(subject, text)
	m := fixed.Sentences(v)
	if err := m.Check(v); err != nil {
		t.Fatalf("%.60q / %.60q: %v", subject, text, err)
	}
	out := make([]string, len(m.Sentences))
	for i, s := range m.Sentences {
		out[i] = v.Text[s.Start:s.End]
	}
	return out, m, v
}

// U10: where a sentence ends: each ending punctuation before white space or
// the end, the CJK ones whatever follows, every line break; the subject
// always ends one; white space alone is no sentence; no abbreviations.
func TestU10WhereASentenceEnds(t *testing.T) {
	for _, c := range []struct {
		subject, text string
		want          []string
	}{
		{"Hello there", "and more", []string{"Hello there", "and more"}},
		{"", "One\nTwo\r\nThree\rFour Five Six\u0085Seven", []string{"One", "Two", "Three", "Four", "Five", "Six", "Seven"}},
		{"", "A. B! C? D… E; F؟ G", []string{"A.", "B!", "C?", "D…", "E;", "F؟", "G"}},
		{"", "Pi is 3.14 and e.g.this stays one!Really", []string{"Pi is 3.14 and e.g.this stays one!Really"}},
		{"", "Dr. Smith said so.", []string{"Dr.", "Smith said so."}},
		{"", "你好。谢谢！再见？好｡ 走", []string{"你好。", "谢谢！", "再见？", "好｡", "走"}},
		{"", "مرحبا؟ شكرا جزيلا.", []string{"مرحبا؟", "شكرا جزيلا."}},
		{"", "  \n \t 　\n ", nil},
		{"   ", "  Spaced   out.   \n\n  Again  ", []string{"Spaced   out.", "Again"}},
		{"", "Wait... what?! Yes.", []string{"Wait...", "what?!", "Yes."}},
		{"", "a\xffb. c", []string{"a\xffb.", "c"}},
		{"", "Quote \"Hi.\" Then", []string{"Quote \"Hi.\" Then"}},
		{"", "Zero​width. Next", []string{"Zero​width.", "Next"}},
	} {
		got, m, v := split(t, c.subject, c.text)
		if !slices.Equal(got, c.want) {
			t.Errorf("%q / %q: %q, want %q", c.subject, c.text, got, c.want)
		}
		if m.Cut != len(v.Text) {
			t.Errorf("%q: cut at %d of %d", c.text, m.Cut, len(v.Text))
		}
	}
}

// U10: a sentence longer than SentenceRunesMax runes ends at its last white
// space within the bound, or at the bound when it has none; no piece of it
// is left out of every sentence.
func TestU10ALongSentenceIsSplitAtItsLastSpace(t *testing.T) {
	words := strings.Repeat("wörd ", 300) // 1,500 runes, a space every five
	got, _, _ := split(t, "", strings.TrimSpace(words)+".")
	if len(got) != 2 || utf8.RuneCountInString(got[0]) != 999 || !strings.HasSuffix(got[0], "wörd") || got[1] != strings.TrimSpace(strings.Repeat("wörd ", 100))+"." {
		t.Fatalf("%d sentences, the first %d runes", len(got), utf8.RuneCountInString(got[0]))
	}
	// No space at all: cut at the bound, each piece SentenceRunesMax runes.
	got, _, _ = split(t, "", strings.Repeat("ñ", 2*redact.SentenceRunesMax+500))
	if len(got) != 3 || utf8.RuneCountInString(got[0]) != redact.SentenceRunesMax || utf8.RuneCountInString(got[1]) != redact.SentenceRunesMax || utf8.RuneCountInString(got[2]) != 500 {
		t.Fatalf("%d sentences", len(got))
	}
	// Exactly the bound is one sentence; one rune more is two.
	if got, _, _ = split(t, "", strings.Repeat("x", redact.SentenceRunesMax)); len(got) != 1 {
		t.Fatalf("%d sentences at the bound", len(got))
	}
	if got, _, _ = split(t, "", "a "+strings.Repeat("x", redact.SentenceRunesMax-1)); len(got) != 2 || got[0] != "a" {
		t.Fatalf("one rune past the bound: %d sentences", len(got))
	}
}

// U10: meaning reads at most MeaningSentencesMax sentences and
// MeaningTextMax bytes; Cut is where the first sentence it does not read
// starts, never inside a sentence or a rune.
func TestU10TheMapIsCutAtItsBounds(t *testing.T) {
	_, m, v := split(t, "Subject", strings.Repeat("Short one. ", 200))
	if len(m.Sentences) != redact.MeaningSentencesMax || !strings.HasPrefix(v.Text[m.Cut:], "Short one. ") || m.Cut != m.Sentences[127].End+1 {
		t.Fatalf("%d sentences, cut at %d", len(m.Sentences), m.Cut)
	}
	// The subject and 127 sentences, then white space alone: all read.
	_, m, v = split(t, "Subject", strings.Repeat("Short one. ", 127)+"  \n ")
	if len(m.Sentences) != redact.MeaningSentencesMax || m.Cut != len(v.Text) {
		t.Fatalf("%d sentences, cut at %d of %d", len(m.Sentences), m.Cut, len(v.Text))
	}
	// Long sentences of two-byte runes: the byte bound comes first, and
	// the sentence it goes through is not read.
	long := strings.Repeat(strings.Repeat("é", 899)+". ", 30)
	_, m, v = split(t, "", long)
	last := m.Sentences[len(m.Sentences)-1]
	if m.Cut > redact.MeaningTextMax || m.Cut+900*2 < redact.MeaningTextMax || !strings.HasPrefix(v.Text[m.Cut:], "é") || last.End >= m.Cut {
		t.Fatalf("cut at %d, last sentence %+v", m.Cut, last)
	}
	// With no space, the bound falls inside a rune: the cut is still at a
	// sentence's start.
	_, m, v = split(t, "", strings.Repeat("€", redact.MeaningTextMax))
	if m.Cut > redact.MeaningTextMax || m.Cut != m.Sentences[len(m.Sentences)-1].End || !utf8.RuneStart(v.Text[m.Cut]) {
		t.Fatalf("cut at %d", m.Cut)
	}
}

// U10: the map's digest changes with the View's bytes, its spans and its
// cut, so a decision is never applied to other sentences.
func TestU10TheDigestNamesTheTextTheSpansAndTheCut(t *testing.T) {
	_, m, v := split(t, "My home address is 1 Main St.", "Thank you. Bye.")
	_, other, w := split(t, "My home address is 1 Main St.", "Thank you. Bye!")
	if m.Digest(v) == other.Digest(w) {
		t.Fatal("two texts, one digest")
	}
	moved := redact.SentenceMap{Sentences: slices.Clone(m.Sentences), Cut: m.Cut}
	moved.Sentences[1].End--
	cut := redact.SentenceMap{Sentences: m.Sentences[:2], Cut: m.Sentences[2].Start}
	if moved.Digest(v) == m.Digest(v) || cut.Digest(v) == m.Digest(v) || cut.Check(v) != nil {
		t.Fatal("other spans or another cut, one digest")
	}
	if fixed.Sentences(v).Digest(v) != m.Digest(v) {
		t.Fatal("one view split twice, two digests")
	}
}
