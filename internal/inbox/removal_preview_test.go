package inbox_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/disclosetest"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/redact"
	"github.com/permaily-org/permaily/internal/rules"
)

// The rule editor's preview of one message as a removal would leave it
// (ADR 0040, "Previews and consent", pull request 8): on the message's one
// view, as the discloser removes it, asking the model without beginning a
// decision.

func topic(t string) query.Atom {
	return query.Atom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: t}
}

// Parts and shapes: the subject and text left less the pieces, one marker
// each; the sender and attachments out with shapes, counted.
func TestPreviewRemovalPartsAndShapes(t *testing.T) {
	rm := &rules.Removal{Parts: []contracts.Part{contracts.PartSubject}, Shapes: []contracts.ShapeKind{contracts.ShapePhoneNumber, contracts.ShapeEmailAddress}}
	got, err := inbox.PreviewRemoval(context.Background(), redact.Fixed{}, "Call 555 123 4567", "Ring 555 123 4567 or ana@example.org.", 2, rm, nil, "acc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != nil || got.Text == nil || *got.Text != "Ring [removed] or [removed]." {
		t.Fatalf("%+v", got)
	}
	s := got.Removed
	if !slices.Equal(s.Parts, []contracts.Part{contracts.PartSubject, contracts.PartSender}) || s.Attachments != 2 ||
		s.Shapes[contracts.ShapePhoneNumber] != 2 || s.Shapes[contracts.ShapeEmailAddress] != 1 || s.Whole {
		t.Fatalf("%+v", s)
	}
	// The attachments as a part, and nothing else: the subject and text
	// left as they came.
	got, err = inbox.PreviewRemoval(context.Background(), redact.Fixed{}, "Hi", "Hello", 1, &rules.Removal{Parts: []contracts.Part{contracts.PartAttachments}}, nil, "acc")
	if err != nil || *got.Subject != "Hi" || *got.Text != "Hello" || !slices.Equal(got.Removed.Parts, []contracts.Part{contracts.PartAttachments}) || got.Removed.Attachments != 1 {
		t.Fatalf("%+v %v", got, err)
	}
}

// Meaning: the sentences asked with every fixed shape masked, in the
// account's share; one answered high removed, the rest kept, every masked
// piece removed too (N2); no asker, a failed ask or no answer remove the
// whole subject and text, undecided; the caller's giving up is an error.
func TestPreviewRemovalByMeaning(t *testing.T) {
	ctx := context.Background()
	rm := &rules.Removal{Topics: []query.Atom{topic("a home address")}}
	asker := &disclosetest.Sentences{P: func(s string, _ int) (float64, bool) {
		if strings.Contains(s, "Main Street") {
			return 0.9, true
		}
		return 0.01, true
	}}
	got, err := inbox.PreviewRemoval(ctx, redact.Fixed{}, "Hello", "I live at 1 Main Street. Call 5551234567 soon. Thanks.", 0, rm, asker, "acc_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject == nil || *got.Subject != "Hello" || got.Text == nil || *got.Text != "[removed] Call [removed] soon. Thanks." {
		t.Fatalf("%+v %q", got, *got.Text)
	}
	if s := got.Removed; s.Sentences != 1 || s.Of != 4 || s.Shapes[contracts.ShapePhoneNumber] != 1 || s.Whole ||
		!slices.Equal(s.Parts, []contracts.Part{contracts.PartSender}) {
		t.Fatalf("%+v", s)
	}
	if len(asker.Asked) != 1 || asker.Asked[0].Account != "acc_1" || strings.Contains(strings.Join(asker.Asked[0].Sentences, " "), "5551234567") {
		t.Fatalf("asked %+v", asker.Asked)
	}
	whole := func(what string, a inbox.SentenceAsker) {
		t.Helper()
		got, err := inbox.PreviewRemoval(ctx, redact.Fixed{}, "Hello", "Some text.", 0, rm, a, "acc_1")
		if err != nil || got.Subject != nil || got.Text != nil || !got.Removed.Whole || got.Removed.Why != string(inbox.HeldUndecided) {
			t.Fatalf("%s: %+v %v", what, got, err)
		}
	}
	whole("no asker", nil)
	whole("a failed ask", &disclosetest.Sentences{Err: errors.New("down")})
	whole("no answer", &disclosetest.Sentences{P: func(string, int) (float64, bool) { return 0, false }})
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := inbox.PreviewRemoval(gone, redact.Fixed{}, "Hello", "Some text.", 0, rm, &disclosetest.Sentences{}, "acc_1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("the caller gone: %v", err)
	}
}
