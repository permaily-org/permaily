package inbox_test

import (
	"context"
	"testing"

	"github.com/permaily-org/permaily/internal/inbox"
)

type askNothing struct{}

func (askNothing) Ask(context.Context, *inbox.Question) (*inbox.Asked, error) {
	return &inbox.Asked{}, nil
}

// TestTheAskerTravelsWithTheRequest: the matcher asks the model only when
// the route set an Asker on the request (ADR 0028, "When"), and an Asking
// without one is none.
func TestTheAskerTravelsWithTheRequest(t *testing.T) {
	ctx := context.Background()
	if _, ok := inbox.AskerFrom(ctx); ok {
		t.Fatal("a request without WithAsker asks no model")
	}
	if _, ok := inbox.AskerFrom(inbox.WithAsker(ctx, inbox.Asking{Account: "acc_x"})); ok {
		t.Fatal("an Asking without an Asker asks no model")
	}
	got, ok := inbox.AskerFrom(inbox.WithAsker(ctx, inbox.Asking{Asker: askNothing{}, Account: "acc_x"}))
	if !ok || got.Account != "acc_x" {
		t.Fatalf("AskerFrom = %+v, %v", got, ok)
	}
}

// TestAViewIsNamedWithItsVersion: answers kept for one view of a text are
// found by its name and version, so another version of how the text is
// taken out is another view (ADR 0028, "Changed after the design review", 3).
func TestAViewIsNamedWithItsVersion(t *testing.T) {
	if got := inbox.ViewOriginal.Key(); got != "original/1" || inbox.TextVersion != 1 {
		t.Fatalf("ViewOriginal.Key() = %q", got)
	}
	if (inbox.TextView{Name: "original", Version: 2}).Key() == inbox.ViewOriginal.Key() {
		t.Fatal("another version of the text is another view")
	}
}
