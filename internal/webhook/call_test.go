package webhook

import (
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// What the person is shown of an outcome is one of the fixed failures, and
// nothing for what did not fail.
func TestAnOutcomeIsShownAsAFixedFailure(t *testing.T) {
	want := map[Outcome]contracts.WebhookFailure{
		OutcomeSent: "", OutcomeGone: "",
		OutcomeRedirect: contracts.WebhookRedirect, OutcomeStatus: contracts.WebhookError, OutcomeRefused: contracts.WebhookRefused,
		OutcomeNoAnswer: contracts.WebhookNoAnswer, OutcomeTooLate: contracts.WebhookNoAnswer, "something else": "",
	}
	for o, f := range want {
		if got := o.Failure(); got != f {
			t.Errorf("%s: %q, want %q", o, got, f)
		}
	}
}
