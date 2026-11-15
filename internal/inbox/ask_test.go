package inbox_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
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

// keptItemMax bounds a full kept entry as it is sealed, so that the item
// that holds it (with its keys, version and times) stays under the 400 KB a
// DynamoDB item may hold, with the margin MaxMetaBytes leaves the message's.
const keptItemMax = 350_000

// TestAFullKeptEntryFitsAnItem: KeptMaxAnswers answers, each with a topic of
// the query language's longest (100 characters) of the characters JSON
// writes longest (four bytes of UTF-8, or six as an escape such as \u003c),
// with the longest of every other field and sixteen views, sealed as
// KeptStore seals it, fit one item (ADR 0028, "Changed after the second
// design review", choice 8).
func TestAFullKeptEntryFitsAnItem(t *testing.T) {
	key := make([]byte, envelope.KeySize)
	hash := strings.Repeat("f", 64)
	at := time.Date(2026, 11, 15, 23, 59, 59, 999_999_999, time.FixedZone("", -(11*3600+59*60)))
	for _, r := range []rune{'\U0001D11E', '<', '\u2028', 'ñ'} {
		d := inbox.KeptDecisions{V: 1, Views: map[string]inbox.KeptView{}}
		for i := range 16 {
			d.Views["p2-2026-11-15/view-"+strconv.Itoa(i)+"/"+strconv.Itoa(1<<30)] = inbox.KeptView{Content: hash, Subject: hash}
		}
		for i := range inbox.KeptMaxAnswers {
			d.Answers = append(d.Answers, inbox.KeptAnswer{
				Content: hash, Var: "message", Op: "mentions", Topic: strings.Repeat(string(r), 100),
				Model: "gpt-6-luna", Prompt: "p2-2026-11-15", P: 0.12345678901234568 + float64(i)*1e-17,
				Refused: true, At: at, CanaryChecked: "2026-11-15", CanaryChange: "2026-11-15",
			})
		}
		plain, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		sealed, err := envelope.SealText(key, plain, envelope.AAD(inbox.PurposeDecision, strings.Repeat("m", 64), strings.Repeat("x", 128)))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%q: %d bytes of JSON, %d sealed", r, len(plain), len(sealed))
		if len(sealed) > keptItemMax {
			t.Errorf("a full entry of %q topics is %d bytes sealed, over %d", r, len(sealed), keptItemMax)
		}
	}
}
