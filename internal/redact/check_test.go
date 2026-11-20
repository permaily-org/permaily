package redact_test

import (
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/redact"
)

// U10 and the re-review's N1: a map that leaves text before its Cut in no
// sentence (none at all over a text, an interior or a tail sentence
// missing), cuts a rune, or holds a sentence past SentenceRunesMax is
// refused, so a decision numbered in it is never applied: what is in no
// sentence would leave without being asked about.
func TestU10N1AMapThatLeavesTextUnreadIsRefused(t *testing.T) {
	v := redact.NewView("Hello there.", "My home address is 1 Main Street. Thank you.")
	at := func(s string) redact.Span {
		i := strings.Index(v.Text, s)
		return redact.Span{Start: i, End: i + len(s)}
	}
	all := []redact.Span{at("Hello there."), at("My home address is 1 Main Street."), at("Thank you.")}
	good := redact.SentenceMap{Sentences: all, Cut: len(v.Text)}
	if err := good.Check(v); err != nil {
		t.Fatalf("the whole map: %v", err)
	}
	// Cut before a sentence, with only white space between: still good.
	if err := (redact.SentenceMap{Sentences: all[:2], Cut: all[2].Start}).Check(v); err != nil {
		t.Fatalf("a cut at a sentence's start: %v", err)
	}
	if err := (redact.SentenceMap{Cut: 0}).Check(v); err != nil {
		t.Fatalf("a cut at 0: %v", err)
	}
	if err := (redact.SentenceMap{Cut: 0}).Check(redact.NewView("", "")); err != nil {
		t.Fatalf("the empty view's map: %v", err)
	}
	if w := redact.NewView(" ", "　 "); (redact.SentenceMap{Cut: len(w.Text)}).Check(w) != nil {
		t.Fatal("white space alone, in no sentence, was refused")
	}

	u := redact.NewView("", "añb c")
	long := redact.NewView("", strings.Repeat("é", redact.SentenceRunesMax+1))
	for name, c := range map[string]struct {
		v redact.View
		m redact.SentenceMap
	}{
		"an empty map over a text":     {v, redact.SentenceMap{Cut: len(v.Text)}},
		"an interior sentence missing": {v, redact.SentenceMap{Sentences: []redact.Span{all[0], all[2]}, Cut: len(v.Text)}},
		"the tail sentence missing":    {v, redact.SentenceMap{Sentences: all[:2], Cut: len(v.Text)}},
		"the first sentence missing":   {v, redact.SentenceMap{Sentences: all[1:], Cut: len(v.Text)}},
		"part of a sentence missing":   {v, redact.SentenceMap{Sentences: []redact.Span{all[0], {Start: all[1].Start, End: all[1].End - 3}, all[2]}, Cut: len(v.Text)}},
		"a cut inside a sentence":      {v, redact.SentenceMap{Sentences: all[:2], Cut: all[2].Start + 2}},
		"a sentence that starts mid-rune": {u, redact.SentenceMap{
			Sentences: []redact.Span{{Start: u.Body.Start, End: u.Body.Start + 2}, {Start: u.Body.Start + 2, End: u.Body.Start + 4}, {Start: u.Body.Start + 5, End: u.Body.End}}, Cut: len(u.Text)}},
		"a sentence that ends mid-rune": {u, redact.SentenceMap{
			Sentences: []redact.Span{{Start: u.Body.Start, End: u.Body.Start + 2}, {Start: u.Body.Start + 2, End: u.Body.End}}, Cut: len(u.Text)}},
		"a cut mid-rune": {u, redact.SentenceMap{
			Sentences: []redact.Span{{Start: u.Body.Start, End: u.Body.Start + 1}}, Cut: u.Body.Start + 2}},
		"a sentence past its runes": {long, redact.SentenceMap{Sentences: []redact.Span{long.Body}, Cut: len(long.Text)}},
	} {
		if err := c.m.Check(c.v); err == nil {
			t.Errorf("%s: %+v kept the invariants", name, c.m)
		}
	}
	// A sentence of exactly SentenceRunesMax runes of two bytes each keeps
	// them.
	at1000 := redact.NewView("", strings.Repeat("é", redact.SentenceRunesMax))
	if err := (redact.SentenceMap{Sentences: []redact.Span{at1000.Body}, Cut: len(at1000.Text)}).Check(at1000); err != nil {
		t.Fatalf("a sentence of %d runes: %v", redact.SentenceRunesMax, err)
	}
	// An invalid byte is a rune: in a sentence it counts as one, and left
	// out of every sentence it is not white space.
	bad := redact.NewView("", "a\xffb")
	if err := (redact.SentenceMap{Sentences: []redact.Span{bad.Body}, Cut: len(bad.Text)}).Check(bad); err != nil {
		t.Fatalf("an invalid byte in a sentence: %v", err)
	}
	if (redact.SentenceMap{Sentences: []redact.Span{{Start: bad.Body.Start, End: bad.Body.Start + 1}, {Start: bad.Body.Start + 2, End: bad.Body.End}}, Cut: len(bad.Text)}).Check(bad) == nil {
		t.Fatal("an invalid byte in no sentence kept the invariants")
	}
}
