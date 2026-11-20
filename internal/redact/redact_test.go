package redact_test

import (
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/redact"
	"github.com/permaily-org/permaily/internal/redact/redacttest"
)

// U23's join (finding 1): spans of the one view, from any rule in any order,
// are joined into the same runs, each one marker; what is not removed is
// kept byte for byte; a span is clipped to its part, so the subject's and
// the text's markers never merge across the line between them.
func TestU23RemovalsAreJoinedOnTheOneView(t *testing.T) {
	v := redact.NewView("Code 123456 inside", "Call 5551234 now. Home is 1 Main St. Bye.")
	if v.Text[v.Subject.Start:v.Subject.End] != "Code 123456 inside" || v.Text[v.Body.Start:v.Body.End] != "Call 5551234 now. Home is 1 Main St. Bye." {
		t.Fatalf("view %+v", v)
	}
	at := func(s string) redact.Span {
		i := strings.Index(v.Text, s)
		return redact.Span{Start: i, End: i + len(s)}
	}
	shapes := []redact.Span{at("123456"), at("5551234")}
	meaning := []redact.Span{at("Home is 1 Main St."), at("Bye.")}
	whole := []redact.Span{v.Subject}
	for _, order := range [][][]redact.Span{{shapes, meaning, whole}, {whole, meaning, shapes}, {meaning, shapes}} {
		var all []redact.Span
		for _, l := range order {
			all = append(all, l...)
		}
		s, b := redact.Join(v, all)
		wantS := "[removed]"
		if len(order) == 2 {
			wantS = "Code [removed] inside"
		}
		if s != wantS || b != "Call [removed] now. [removed] [removed]" {
			t.Errorf("joined %q / %q", s, b)
		}
	}
	sp := func(a, b int) redact.Span { return redact.Span{Start: a, End: b} }
	if got := redact.Union([]redact.Span{sp(5, 9), sp(0, 2)}, []redact.Span{sp(2, 4), sp(8, 12), sp(20, 20)}); len(got) != 2 || got[0] != sp(0, 4) || got[1] != sp(5, 12) {
		t.Fatalf("union %v", got)
	}
	// A span over the line between the parts is cut at it.
	s, b := redact.Join(v, []redact.Span{sp(v.Subject.End-6, v.Body.Start+4)})
	if s != "Code 123456 [removed]" || b != "[removed] 5551234 now. Home is 1 Main St. Bye." {
		t.Fatalf("across the parts %q / %q", s, b)
	}
	// A span that cuts a rune takes the whole rune.
	u := redact.NewView("", "añb")
	if _, b := redact.Join(u, []redact.Span{sp(u.Body.Start+1, u.Body.Start+2)}); b != "a[removed]b" {
		t.Fatalf("a cut rune: %q", b)
	}
	if s, b := redact.Join(v, nil); s+"\n"+b != v.Text {
		t.Fatal("nothing removed changed the view")
	}
}

// U10's half (finding 1): a sentence map's invariants and its digest, which
// changes with the text and with the split, so a decision is never applied
// to other sentences.
func TestU10TheSentenceMapIsBoundToItsView(t *testing.T) {
	v := redact.NewView("My home address is 1 Main St.", "Thank you.")
	m := (redacttest.Naive{}).Sentences(v)
	if err := m.Check(v); err != nil || len(m.Sentences) != 2 {
		t.Fatalf("%+v, %v", m, err)
	}
	other := redact.NewView("", "My home address is 1 Main St.\nThank you.")
	if m.Digest(v) == (redacttest.Naive{}).Sentences(other).Digest(other) {
		t.Fatal("two views, one digest")
	}
	moved := m
	moved.Sentences = []redact.Span{m.Sentences[1], m.Sentences[0]}
	if moved.Check(v) == nil {
		t.Fatal("sentences out of order kept the invariants")
	}
	cut := m
	cut.Cut = m.Sentences[1].Start
	if cut.Check(v) == nil || cut.Digest(v) == m.Digest(v) {
		t.Fatal("a sentence past the cut")
	}
	for _, bad := range []redact.SentenceMap{
		{Cut: -1}, {Cut: len(v.Text) + 1}, {Sentences: []redact.Span{{Start: 0, End: 0}}, Cut: len(v.Text)},
		{Sentences: []redact.Span{{Start: v.Subject.End, End: v.Subject.End + 1}}, Cut: len(v.Text)},
	} {
		if bad.Check(v) == nil {
			t.Errorf("%+v kept the invariants", bad)
		}
	}
}

// The played Redactor keeps the contract every Redactor keeps.
func TestTheNaiveRedactorKeepsTheContract(t *testing.T) {
	redacttest.Contract(t, redacttest.Naive{})
	if got := (redacttest.Naive{}).Shapes("Code 123456 2026", []contracts.ShapeKind{contracts.ShapeNumericCode}); len(got) != 2 {
		t.Fatalf("%v", got)
	}
}
