package inbox_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
)

// The review of pull request 201 (ADR 0040's pull request 5), its P2: a
// removal of only the sender or only the attachments left the subject and
// text as they were, yet the query's model atoms were asked of no text, so
// a message that matched was never disclosed.

// TestReview201MetadataRemovalSuppressesMatchingMessage is the review's
// reproduction: a fresh message atom over the unchanged text, positive and
// negated, through a single read and a page.
func TestReview201MetadataRemovalSuppressesMatchingMessage(t *testing.T) {
	about := cond(contracts.VarMessage, contracts.OpAbout, "an invoice")
	for _, part := range []contracts.Part{contracts.PartSender, contracts.PartAttachments} {
		t.Run(string(part), func(t *testing.T) {
			for _, tc := range []struct {
				name string
				f    *query.Canonical
				p    float64
			}{
				{"about", formula(group(about)), .95},
				{"not about", formula(group(not(about))), .05},
			} {
				t.Run(tc.name, func(t *testing.T) {
					w := newDWorld(t)
					id := w.seed(qNow.Add(-time.Hour), 0, "Invoice", "The invoice is ready.")
					w.askA.p = func(string, query.Atom) (float64, bool) { return tc.p, true }
					d := with(allParts, []rules.Rule{removing("rul_meta", parts(part))}, every, tc.f)
					got, err := w.discA.Message(on(w.askA), w.b, d, id)
					if err != nil || got.Disclosed == nil {
						t.Fatalf("matching unchanged text suppressed: outcome=%+v err=%v", got, err)
					}
					if textOf(got.Disclosed) != "The invoice is ready." || subjectOf(got.Disclosed) != "Invoice" || len(got.Disclosed.Removed) != 1 {
						t.Fatalf("read %q %q, removed %+v", subjectOf(got.Disclosed), textOf(got.Disclosed), got.Disclosed.Removed)
					}
					m := got.Disclosed.Message
					if part == contracts.PartSender && len(m.From) != 0 || part == contracts.PartAttachments && len(m.Attachments) != 0 {
						t.Fatalf("the removed part left: %+v", m)
					}
					left, held := sortOut(t, w.page(on(w.askA), w.discA, d, "", 10).Items)
					if !slices.Contains(left, id) || len(held) != 0 {
						t.Fatalf("page: left %v, held %+v", left, held)
					}
				})
			}
			// What does not match is still not disclosed.
			w := newDWorld(t)
			id := w.seed(qNow.Add(-time.Hour), 0, "Invoice", "The invoice is ready.")
			w.askA.p = func(string, query.Atom) (float64, bool) { return .05, true }
			d := with(allParts, []rules.Rule{removing("rul_meta", parts(part))}, every, formula(group(about)))
			if got, err := w.discA.Message(on(w.askA), w.b, d, id); err == nil {
				t.Fatalf("a message that does not match left: %+v", got)
			}
		})
	}
}

// TestReview201MetadataRemovalAsksTheOriginalTextUnderTheReducedView: with
// no text span removed, the model reads the text byte for byte as it came,
// under the reduced view's own name, and the original's answer is not used.
func TestReview201MetadataRemovalAsksTheOriginalTextUnderTheReducedView(t *testing.T) {
	w := newDWorld(t)
	id := w.seed(qNow.Add(-time.Hour), 0, "Invoice", "The invoice is ready.")
	about := formula(group(cond(contracts.VarMessage, contracts.OpAbout, "an invoice")))
	asker := &viewAsker{p: map[string]float64{inbox.ViewOriginal.Name: .95, "": .95}}
	rs := []rules.Rule{removing("rul_sender", parts(contracts.PartSender))}
	if got, err := w.discA.Message(on(asker), w.b, with(allParts, rs, every, about), id); err == nil {
		t.Fatalf("the original's answer was used for the reduced view: %+v", got)
	}
	if len(asker.seen) != 1 || !strings.HasPrefix(asker.seen[0].View.Name, "r-") || asker.text[0] != "The invoice is ready." || asker.seen[0].Subject != "Invoice" {
		t.Fatalf("asked %+v %q", asker.seen, asker.text)
	}
	asker.p[asker.seen[0].View.Name] = .95
	if got := w.read(on(asker), w.discA, with(allParts, rs, every, about), id); textOf(got) != "The invoice is ready." || len(got.Decided) != 1 {
		t.Fatalf("%q %+v", textOf(got), got.Decided)
	}
}

// TestReview201ATextRemovalStillReadsOnlyTheReducedText: a removal of a
// span of the text, beside one of the sender, still gives the model only
// what is left of it, never the original.
func TestReview201ATextRemovalStillReadsOnlyTheReducedText(t *testing.T) {
	w := newDWorld(t)
	id := w.seed(qNow.Add(-time.Hour), 0, "Invoice", "Pay to 555 123 4567 today.")
	about := formula(group(cond(contracts.VarMessage, contracts.OpAbout, "a payment")))
	asker := &viewAsker{p: map[string]float64{}}
	rs := []rules.Rule{
		removing("rul_sender", parts(contracts.PartSender)),
		removing("rul_phone", shapes(contracts.ShapePhoneNumber)),
	}
	_, _ = w.discA.Message(on(asker), w.b, with(allParts, rs, every, about), id)
	if len(asker.seen) != 1 || !strings.HasPrefix(asker.seen[0].View.Name, "r-") || asker.text[0] != "Pay to [removed] today." {
		t.Fatalf("asked %+v %q", asker.seen, asker.text)
	}
	asker.p[asker.seen[0].View.Name] = .95
	got := w.read(on(asker), w.discA, with(allParts, rs, every, about), id)
	if textOf(got) != "Pay to [removed] today." || len(got.Message.From) != 0 {
		t.Fatalf("%q %+v", textOf(got), got.Message.From)
	}
	for _, s := range asker.text {
		if strings.Contains(s, "555") {
			t.Fatalf("the model read the removed text: %q", asker.text)
		}
	}
}
