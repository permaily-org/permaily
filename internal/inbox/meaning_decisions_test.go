package inbox_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/disclosetest"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/redact"
	"github.com/permaily-org/permaily/internal/redact/redacttest"
	"github.com/permaily-org/permaily/internal/rules"
)

// ADR 0040's pull request 4: meaning's records half (U18) and the decision
// side of the re-review's N2.

var meaningRef = rules.MeaningRef(rules.Ref{ID: "rul_aaaaaaaaaaaaaaaaaaaaaaaaaa", Version: 2}, inbox.TextVersion, redact.SplitVersion)

func homeAddress() query.Atom {
	return query.Atom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: "a home address"}
}

// The largest settlement a meaning decision can have: evidence at its
// bound, and its sentences at theirs.
func largestMeaning(t *testing.T, at time.Time) inbox.Settlement {
	t.Helper()
	var ev []inbox.Decided
	for i := range inbox.DecisionEvidenceMax {
		ev = append(ev, inbox.Decided{
			Atom:     query.Atom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: strings.Repeat(string(rune('a'+i)), 90)},
			Evidence: inbox.Evidence{P: 0.123456789012345, Model: "gpt-6-luna", Prompt: "s1-2026-11-20", At: at, CanaryChecked: "2026-11-15", CanaryChange: "2026-11-01"},
		})
	}
	stored, _ := json.Marshal(ev)
	if len(stored) > inbox.DecisionEvidenceBytesMax*2 {
		t.Fatalf("evidence fixture %d bytes", len(stored))
	}
	// Trim to the bound, as the largest that still fits.
	for {
		b, _ := json.Marshal(storedOf(ev))
		if len(b) <= inbox.DecisionEvidenceBytesMax {
			break
		}
		ev[0].Atom.Topic = ev[0].Atom.Topic[:len(ev[0].Atom.Topic)-1]
		if ev[0].Atom.Topic == "" {
			ev = ev[1:]
		}
	}
	kept := make([]bool, redact.MeaningSentencesMax)
	for i := range kept {
		kept[i] = true
	}
	return inbox.Settlement{State: inbox.DecisionCleared, Evidence: ev, Meaning: &inbox.MeaningKept{Map: strings.Repeat("f", 64), Kept: kept}}
}

// storedOf is evidence as the blocks store it, for measuring.
func storedOf(ds []inbox.Decided) []inbox.DecisionEvidence {
	var out []inbox.DecisionEvidence
	for _, d := range ds {
		e := d.Evidence
		out = append(out, inbox.DecisionEvidence{Var: string(d.Atom.Var), Op: string(d.Atom.Op), Topic: d.Atom.Topic, ByRule: e.ByRule, Refused: e.Refused, P: e.P, Model: e.Model, Prompt: e.Prompt, At: e.At, CanaryChecked: e.CanaryChecked, CanaryChange: e.CanaryChange})
	}
	return out
}

// U18's records half: a Pending of a meaning decision keeps room for its
// sentences besides its evidence, so a block filled with them, each then
// settled at its largest, still fits; and the default rule's Pending keep
// no more than they did.
func TestU18AMeaningPendingKeepsRoomForItsSentences(t *testing.T) {
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	var ks []inbox.DecisionKey
	for i := range 400 {
		ks = append(ks, inbox.DecisionKey{ID: inbox.NewMessageID(at.Add(time.Duration(i) * time.Millisecond)), ExpiresAt: at.Add(time.Hour)})
	}
	count := func(rule rules.Ref) (*inbox.DecisionRecord, map[string]inbox.Decision, int) {
		r := &inbox.DecisionRecord{V: 1, Mailbox: "mbx_a", Day: "20261120"}
		got, _ := r.Begin(rule, ks, at, 0)
		n := 0
		for _, k := range ks {
			if got[k.ID].State == inbox.DecisionBegun {
				n++
			}
		}
		return r, got, n
	}
	r, begun, meaning := count(meaningRef)
	_, _, plain := count(rules.Ref{ID: strings.Repeat("r", len(meaningRef.ID)), Version: 2})
	if meaning == 0 || meaning >= plain {
		t.Fatalf("%d meaning pending, %d of a rule of the same name's length", meaning, plain)
	}
	largest := largestMeaning(t, at)
	settle := map[string]inbox.Settlement{}
	for id, d := range begun {
		if d.State == inbox.DecisionBegun {
			s := largest
			s.Attempt = d.Attempt
			settle[id] = s
		}
	}
	got, changed := r.Settle(meaningRef, settle, at, 1)
	if !changed {
		t.Fatal("nothing settled")
	}
	for id := range settle {
		if got[id].State != inbox.DecisionCleared || got[id].Meaning == nil {
			t.Fatalf("%+v", got[id])
		}
	}
	if b, _ := json.Marshal(r); len(b) > inbox.DecisionsBytesMax {
		t.Fatalf("%d bytes once settled, past %d", len(b), inbox.DecisionsBytesMax)
	}
}

// U18's records half through the blocks: settled with its sentences and
// answered with them ever after, the same decision for every connection;
// applied only to the map it numbered.
func TestU18AMeaningDecisionIsAnsweredWithItsSentences(t *testing.T) {
	ctx, box := context.Background(), inbox.Box{MailboxID: "mbx_a"}
	r := redacttest.Naive{}
	v := redact.NewView("My home address is 123 Main Street.", "Thank you.")
	m := r.Sentences(v)
	a, b := disclosetest.Enclaves(time.Now)
	da, db := &inbox.BlockDecisions{Store: a}, &inbox.BlockDecisions{Store: b}
	id := inbox.NewMessageID(time.Now())
	keys := []inbox.DecisionKey{{ID: id, ExpiresAt: time.Now().Add(time.Hour)}}
	begun, err := da.Begin(ctx, box, meaningRef, keys)
	if err != nil || begun[id].State != inbox.DecisionBegun {
		t.Fatal(begun, err)
	}
	asked := (&disclosetest.Sentences{P: func(s string, _ int) (float64, bool) {
		if strings.Contains(s, "address") {
			return 0.97, true
		}
		return 0.02, true
	}})
	q, err := asked.AskSentences(ctx, &inbox.SentenceQuestion{Account: "acc_a", Sentences: inbox.MaskedSentences(r, v, m), Topics: []query.Atom{homeAddress()}})
	if err != nil {
		t.Fatal(err)
	}
	s := inbox.SettleMeaning(v, m, []query.Atom{homeAddress()}, q, nil)
	s.Attempt = begun[id].Attempt
	if _, err := da.Settle(ctx, box, meaningRef, map[string]inbox.Settlement{id: s}); err != nil {
		t.Fatal(err)
	}
	// The other enclave, the model now answering low for everything:
	// nothing is asked again, and the same sentence is removed.
	got, err := db.Begin(ctx, box, meaningRef, keys)
	if err != nil || got[id].State != inbox.DecisionCleared {
		t.Fatal(got, err)
	}
	kept, ok := got[id].Meaning.For(v, m)
	if !ok {
		t.Fatal("its own map refused")
	}
	subject, text := redact.Join(v, inbox.MeaningSpans(r, v, m, kept))
	if subject != "[removed]" || text != "Thank you." {
		t.Fatalf("%q / %q", subject, text)
	}
	if ev := got[id].Evidence; len(ev) != 1 || ev[0].Evidence.P != 0.02 {
		t.Fatalf("the closest call kept: %+v", ev)
	}
	// Another text for the same message (an extractor that changed without
	// its version): the decision is never read against it.
	other := redact.NewView("My home address is 123 Main Street.", "Thank you. See you soon.")
	om := r.Sentences(other)
	if _, ok := got[id].Meaning.For(other, om); ok {
		t.Fatal("a decision applied to another map")
	}
	if s, b := redact.Join(other, inbox.MeaningSpans(r, other, om, nil)); s != "[removed]" || b != "[removed]" {
		t.Fatalf("%q / %q", s, b)
	}
}

// U18's records half: an ask settles as SettleAfterAsk does for a hold. An
// answer received is never forgotten: partial answers keep only the
// sentences answered low for every topic; nothing answered, with positive
// evidence (an error, or no answer and no refusal), is abandoned; a map
// with no sentence asks nothing; a map that breaks its invariants is held.
func TestU18AMeaningAskSettles(t *testing.T) {
	v := redact.NewView("Subject.", "One. Two. Three.")
	m := (redacttest.Naive{}).Sentences(v)
	if len(m.Sentences) != 4 {
		t.Fatalf("%d sentences", len(m.Sentences))
	}
	topics := []query.Atom{homeAddress(), {Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: "a phone number"}}
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	ev := inbox.Evidence{Model: "gpt-6-luna", Prompt: "s1-2026-11-20", At: at, CanaryChecked: "2026-11-19"}
	partial := &inbox.SentencesAsked{
		Answers: map[inbox.SentenceTopic]float64{
			{0, 0}: 0.01, {0, 1}: 0.2, // kept
			{1, 0}: 0.01,              // its second topic unanswered (a call failed): removed
			{2, 0}: 0.5, {2, 1}: 0.01, // in doubt: removed
			{3, 0}: 0.05, {3, 1}: 0.29, // kept
		},
		Refused:  map[inbox.SentenceTopic]bool{},
		Evidence: ev,
		Outcome:  inbox.AskLimit,
	}
	s := inbox.SettleMeaning(v, m, topics, partial, nil)
	if s.State != inbox.DecisionCleared || s.Meaning == nil || !slices.Equal(s.Meaning.Kept, []bool{true, false, false, true}) || s.Meaning.Map != m.Digest(v) {
		t.Fatalf("partial: %+v %+v", s, s.Meaning)
	}
	if len(s.Evidence) != 2 || s.Evidence[0].Evidence.P != 0.05 || s.Evidence[1].Evidence.P != 0.29 ||
		s.Evidence[0].Atom != topics[0] || s.Evidence[0].Evidence.Model != "gpt-6-luna" || s.Evidence[0].Evidence.Prompt != "s1-2026-11-20" {
		t.Fatalf("evidence %+v", s.Evidence)
	}
	for name, c := range map[string]struct {
		a   *inbox.SentencesAsked
		err error
	}{
		"an error":                  {nil, context.DeadlineExceeded},
		"nothing asked":             {nil, nil},
		"no answer and no refusal":  {&inbox.SentencesAsked{Outcome: inbox.AskOff}, nil},
		"no answer an error beside": {partial, context.Canceled},
	} {
		if got := inbox.SettleMeaning(v, m, topics, c.a, c.err); got.State != inbox.DecisionAbandoned || got.Meaning != nil {
			t.Errorf("%s: %+v", name, got)
		}
	}
	refused := &inbox.SentencesAsked{Refused: map[inbox.SentenceTopic]bool{{0, 0}: true}}
	if got := inbox.SettleMeaning(v, m, topics, refused, nil); got.State != inbox.DecisionCleared || slices.Contains(got.Meaning.Kept, true) {
		t.Fatalf("a refusal is an answer received: %+v", got)
	}
	empty := redact.NewView("", "")
	if got := inbox.SettleMeaning(empty, (redacttest.Naive{}).Sentences(empty), topics, nil, nil); got.State != inbox.DecisionCleared || got.Meaning == nil || len(got.Meaning.Kept) != 0 {
		t.Fatalf("nothing to ask: %+v", got)
	}
	broken := redact.SentenceMap{Sentences: []redact.Span{{Start: 3, End: 1}}, Cut: len(v.Text)}
	if got := inbox.SettleMeaning(v, broken, topics, partial, nil); got.State != inbox.DecisionHeld || got.Meaning != nil {
		t.Fatalf("a broken map: %+v", got)
	}
}

// An entry whose sentences do not read back keeps none: the whole subject
// and text are removed, never a bitmap guessed at.
func TestU18AnEntryWhoseSentencesDoNotReadBackKeepsNone(t *testing.T) {
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	id := inbox.NewMessageID(at)
	r := &inbox.DecisionRecord{V: 1, Entries: []inbox.DecisionEntry{{
		Message: id, Rule: meaningRef.ID, Version: meaningRef.Version, State: inbox.DecisionCleared, At: at, MeaningMap: "d", MeaningKept: "2:ff",
	}}}
	got, _ := r.Begin(meaningRef, []inbox.DecisionKey{{ID: id}}, at, 1)
	if got[id].State != inbox.DecisionCleared || got[id].Meaning != nil {
		t.Fatalf("%+v", got[id])
	}
}

// N2 (the re-review of ADR 0040): every piece masked from the model is also
// removed from what is delivered under a meaning rule, whatever the model
// answered. Paired messages the model cannot tell apart: an email address
// alone and a phone number alone are both "[removed]" to it; it answers low
// for both, and both are removed, with no shape rule present.
func TestN2PairedMaskedIdenticalMessagesAreBothRemoved(t *testing.T) {
	r := redacttest.Naive{}
	low := &disclosetest.Sentences{P: func(string, int) (float64, bool) { return 0.01, true }}
	topics := []query.Atom{{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: "an email address"}}
	var seen [][]string
	for _, body := range []string{"ana@example.com", "5551234567"} {
		v := redact.NewView("Hi", body)
		m := r.Sentences(v)
		masked := inbox.MaskedSentences(r, v, m)
		seen = append(seen, masked)
		q, err := low.AskSentences(context.Background(), &inbox.SentenceQuestion{Sentences: masked, Topics: topics})
		if err != nil {
			t.Fatal(err)
		}
		s := inbox.SettleMeaning(v, m, topics, q, nil)
		kept, ok := s.Meaning.For(v, m)
		if !ok || !slices.Equal(kept, []bool{true, true}) {
			t.Fatalf("%q: kept %v", body, kept)
		}
		if subject, text := redact.Join(v, inbox.MeaningSpans(r, v, m, kept)); subject != "Hi" || text != "[removed]" {
			t.Fatalf("%q delivered as %q / %q", body, subject, text)
		}
	}
	if !slices.Equal(seen[0], seen[1]) {
		t.Fatalf("the model told them apart: %q, %q", seen[0], seen[1])
	}
}

// N2: standalone values of every kind the model never sees, under a
// topic-only rule whose sentences the model kept, are removed; and across
// shape-scoped rules (a rule removing phone numbers alone, its own scope)
// the meaning rule still removes the address and the link the shape rule
// does not.
func TestN2StandaloneShapesAreRemovedByATopicOnlyRule(t *testing.T) {
	r := redacttest.Naive{}
	for _, value := range []string{"ana@example.com", "5551234567", "4111111111111111", "https://example.com/x"} {
		v := redact.NewView("", "Call me at "+value+" today.")
		m := r.Sentences(v)
		kept := make([]bool, len(m.Sentences))
		for i := range kept {
			kept[i] = true
		}
		if _, text := redact.Join(v, inbox.MeaningSpans(r, v, m, kept)); text != "Call me at [removed] today." {
			t.Errorf("%q delivered as %q", value, text)
		}
	}
	v := redact.NewView("", "Mail ana@example.com, call 5551234567, see https://example.com/x.")
	m := r.Sentences(v)
	var phones []redact.Span
	for _, p := range r.Shapes(v.Text, []contracts.ShapeKind{contracts.ShapePhoneNumber}) {
		phones = append(phones, p.Span)
	}
	if _, text := redact.Join(v, phones); strings.Contains(text, "5551234567") || !strings.Contains(text, "ana@example.com") {
		t.Fatalf("the phone rule alone: %q", text)
	}
	all := make([]bool, len(m.Sentences))
	for i := range all {
		all[i] = true
	}
	meaning := inbox.MeaningSpans(r, v, m, all)
	_, text := redact.Join(v, append(append([]redact.Span{}, phones...), meaning...))
	for _, gone := range []string{"ana@", "555", "https"} {
		if strings.Contains(text, gone) {
			t.Fatalf("with the meaning rule: %q", text)
		}
	}
	if !strings.HasPrefix(text, "Mail [removed]") || !strings.Contains(text, " call [removed]") || !strings.Contains(text, " see [removed]") {
		t.Fatalf("with the meaning rule: %q", text)
	}
}

// N1's consumer half, as far as MeaningSpans goes: whatever text before the
// cut no sentence of the map covers is removed, white space apart, even
// from a map whose check would let it through.
func TestN1TextNoSentenceCoversIsRemoved(t *testing.T) {
	r := redacttest.Naive{}
	v := redact.NewView("", "My home address is 1 Main Street.")
	for name, m := range map[string]redact.SentenceMap{
		"no sentence":      {Cut: len(v.Text)},
		"a tail missing":   {Sentences: []redact.Span{{Start: v.Body.Start, End: v.Body.Start + 2}}, Cut: len(v.Text)},
		"interior missing": {Sentences: []redact.Span{{Start: v.Body.Start, End: v.Body.Start + 2}, {Start: v.Body.End - 7, End: v.Body.End}}, Cut: len(v.Text)},
	} {
		kept := make([]bool, len(m.Sentences))
		for i := range kept {
			kept[i] = true
		}
		if _, text := redact.Join(v, inbox.MeaningSpans(r, v, m, kept)); strings.Contains(text, "address") || strings.Contains(text, "Main") {
			t.Errorf("%s: %q", name, text)
		}
	}
}
