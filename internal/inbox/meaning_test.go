package inbox_test

import (
	"context"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/disclosetest"
	"github.com/permaily-org/permaily/internal/redact"
	"github.com/permaily-org/permaily/internal/redact/redacttest"
	"github.com/permaily-org/permaily/internal/rules"
)

// U17's rule of what a removal by meaning keeps: only a sentence the model
// answered below 0.30 for every topic; a refusal, an answer in the band of
// doubt, one at 0.70 or above, no answer at all, a probability that is not
// one, and nothing asked, each remove it.
func TestU17ASentenceIsKeptOnlyOnLowAnswersForEveryTopic(t *testing.T) {
	asked := &inbox.SentencesAsked{
		Answers: map[inbox.SentenceTopic]float64{
			{0, 0}: 0.01, {0, 1}: 0.29, // kept
			{1, 0}: 0.01, {1, 1}: 0.30, // the band's edge: removed
			{2, 0}: 0.95, {2, 1}: 0.01, // above: removed
			{3, 0}: 0.01,               // the second topic unanswered: removed
			{4, 0}: 0.01, {4, 1}: 0.01, // refused below: removed
			{5, 0}: math.NaN(), {5, 1}: 0.01, // not a probability: removed
			{6, 0}: -0.1, {6, 1}: 0.01, // outside 0 to 1: removed
		},
		Refused: map[inbox.SentenceTopic]bool{{4, 1}: true},
	}
	want := []bool{true, false, false, false, false, false, false, false}
	got := inbox.KeptSentences(asked, 8, 2)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sentence %d kept %v, want %v", i, got[i], want[i])
		}
	}
	for i, k := range inbox.KeptSentences(nil, 3, 1) {
		if k {
			t.Errorf("nothing asked kept sentence %d", i)
		}
	}
	for i, k := range inbox.KeptSentences(&inbox.SentencesAsked{Answers: map[inbox.SentenceTopic]float64{{0, 0}: 0}}, 1, 0) {
		if k {
			t.Errorf("a removal with no topic kept sentence %d", i)
		}
	}
}

// U18's identity half (finding 1): a meaning decision is applied only to the
// map it numbered. The review's case: one rule, one message; whichever parts
// a connection reads, the view and its map are the same, so one decision
// serves both; a view that differs (another extractor's text) does not take
// it, and the whole subject and text are removed.
func TestU18AMeaningDecisionIsBoundToItsMap(t *testing.T) {
	r := redacttest.Naive{}
	v := redact.NewView("My home address is 123 Main Street.", "Thank you.")
	m := r.Sentences(v)
	kept := &inbox.MeaningKept{Map: m.Digest(v), Kept: []bool{false, true}}
	got, ok := kept.For(v, m)
	if !ok || got[0] || !got[1] {
		t.Fatalf("its own map: %v %v", got, ok)
	}
	s, b := redact.Join(v, inbox.MeaningSpans(r, v, m, got))
	// A body-only connection and a subject-only one cut their parts from
	// the same result: the address is removed for both.
	if s != "[removed]" || b != "Thank you." {
		t.Fatalf("joined %q / %q", s, b)
	}
	other := redact.NewView("", "My home address is 123 Main Street.")
	om := r.Sentences(other)
	if _, ok := kept.For(other, om); ok {
		t.Fatal("a decision applied to another view's sentences")
	}
	s, b = redact.Join(other, inbox.MeaningSpans(r, other, om, nil))
	if s != "" || b != "[removed]" {
		t.Fatalf("no decision usable: %q / %q", s, b)
	}
	if _, ok := (*inbox.MeaningKept)(nil).For(v, m); ok {
		t.Fatal("no decision applied")
	}
	short := &inbox.MeaningKept{Map: m.Digest(v), Kept: []bool{true}}
	if _, ok := short.For(v, m); ok {
		t.Fatal("a bitmap of another count applied")
	}
}

// U18 (finding 1): two meaning rules, decided apart on the same map, remove
// the same text in either order: their removals are a union, never one
// rule's applied to what another left.
func TestU18TwoMeaningRulesInEitherOrder(t *testing.T) {
	v := redact.NewView("Hello", "I live at 1 Main St. My doctor is Dr Who. Thanks.")
	m := (redacttest.Naive{}).Sentences(v)
	address := []bool{true, false, true, true}
	health := []bool{true, true, false, true}
	if len(m.Sentences) != 4 {
		t.Fatalf("%d sentences", len(m.Sentences))
	}
	a := inbox.MeaningSpans(redacttest.Naive{}, v, m, address)
	h := inbox.MeaningSpans(redacttest.Naive{}, v, m, health)
	s1, b1 := redact.Join(v, append(append([]redact.Span{}, a...), h...))
	s2, b2 := redact.Join(v, append(append([]redact.Span{}, h...), a...))
	if s1 != s2 || b1 != b2 || b1 != "[removed] [removed] Thanks." {
		t.Fatalf("%q, %q", b1, b2)
	}
}

// Meaning's past the cut is removed, never kept by a bitmap.
func TestU19WhatIsPastTheCutIsRemoved(t *testing.T) {
	v := redact.NewView("", "One. Two.")
	m := redact.SentenceMap{Sentences: []redact.Span{{Start: v.Body.Start, End: v.Body.Start + 4}}, Cut: v.Body.Start + 4}
	if _, b := redact.Join(v, inbox.MeaningSpans(redacttest.Naive{}, v, m, []bool{true})); b != "One.[removed]" {
		t.Fatalf("%q", b)
	}
}

// U18's records half: the kept sentences' encoding round-trips, refuses
// what Encode never writes, and BlockDecisions keeps a settlement with them
// and answers them with the decision ever after.
func TestU18TheKeptSentencesRoundTrip(t *testing.T) {
	for _, kept := range [][]bool{{}, {true}, {false, true, true, false, false, false, false, true, true}, make([]bool, redact.MeaningSentencesMax)} {
		m := &inbox.MeaningKept{Map: "d", Kept: kept}
		enc := m.Encode()
		if len(enc)+len("d") > inbox.MeaningKeptBytesMax {
			t.Errorf("%q past its bound", enc)
		}
		back, err := inbox.DecodeMeaningKept("d", enc)
		if err != nil || !slices.Equal(back.Kept, kept) {
			t.Errorf("%q read back %+v, %v", enc, back, err)
		}
	}
	for _, bad := range []string{"", "1", "1:", "2:c0x", "1:c0", "9:ff", "129:" + strings.Repeat("00", 17), "01:80", "-1:", "2:C0"} {
		if _, err := inbox.DecodeMeaningKept("d", bad); err == nil {
			t.Errorf("%q read", bad)
		}
	}
	if _, err := inbox.DecodeMeaningKept("", "1:80"); err == nil {
		t.Error("read with no map")
	}
	ctx, box := context.Background(), inbox.Box{MailboxID: "mbx_x"}
	d := disclosetest.NewDecisions(time.Now)
	id := inbox.NewMessageID(time.Now())
	ref := rules.MeaningRef(rules.Ref{ID: "rul_x", Version: 1}, inbox.TextVersion, redact.SplitVersion)
	begun, err := d.Begin(ctx, box, ref, []inbox.DecisionKey{{ID: id, ExpiresAt: time.Now().Add(time.Hour)}})
	if err != nil || begun[id].State != inbox.DecisionBegun {
		t.Fatal(begun, err)
	}
	got, err := d.Settle(ctx, box, ref, map[string]inbox.Settlement{id: {State: inbox.DecisionCleared, Attempt: begun[id].Attempt, Meaning: &inbox.MeaningKept{Map: "d", Kept: []bool{false, true}}}})
	if err != nil || got[id].State != inbox.DecisionCleared || got[id].Meaning == nil || !slices.Equal(got[id].Meaning.Kept, []bool{false, true}) {
		t.Fatalf("a meaning settlement: %+v, %v", got[id], err)
	}
}

// U20's half (finding 1): what the model is given of a sentence has every
// fixed shape masked, whichever rules apply.
func TestU20TheModelSeesNoShape(t *testing.T) {
	r := redacttest.Naive{}
	v := redact.NewView("Code 123456", "Write to ana@example.com today. Thanks.")
	got := inbox.MaskedSentences(r, v, r.Sentences(v))
	want := []string{"Code [removed]", "Write to [removed] today.", "Thanks."}
	if !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
}
