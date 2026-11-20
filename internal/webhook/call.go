package webhook

import (
	"context"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Call is one call of a webhook: the notice's body and its three headers
// (contracts.WebhookHeader*), and WriteBy, the instant by which the request
// is written in full or not at all (oauth.ConnectionFresh after the read
// that allowed it): a call whose dial or handshake took longer is
// cancelled before the notice leaves.
type Call struct {
	URL       string
	ID        string
	Timestamp string
	Signature string
	Body      []byte
	WriteBy   time.Time
}

// Outcome is how a call ended, one of a fixed set: the only words kept,
// counted and logged of it.
type Outcome string

const (
	OutcomeSent     Outcome = "sent"      // a 2xx
	OutcomeGone     Outcome = "gone"      // 410: the vendor asks to stop
	OutcomeRedirect Outcome = "redirect"  // a 3xx, never followed
	OutcomeStatus   Outcome = "status"    // any other status
	OutcomeNoAnswer Outcome = "no_answer" // not reached, reset, or past AttemptTimeout
	OutcomeRefused  Outcome = "refused"   // TLS that does not verify, or no HTTP/2
	OutcomeTooLate  Outcome = "too_late"  // not written by WriteBy: nothing left
)

// Failure is what the person is shown of an outcome that failed.
func (o Outcome) Failure() contracts.WebhookFailure {
	switch o {
	case OutcomeRedirect:
		return contracts.WebhookRedirect
	case OutcomeStatus:
		return contracts.WebhookError
	case OutcomeRefused:
		return contracts.WebhookRefused
	case OutcomeNoAnswer, OutcomeTooLate:
		return contracts.WebhookNoAnswer
	}
	return ""
}

// Sender calls a webhook: a POST of the body with its headers and
// "Content-Type: application/json", through the vendor egress over HTTP/2
// only, following no redirect, sending no cookie or credential, reading at
// most AnswerBytesMax of the answer, within AttemptTimeout.
// internal/webhook/send implements it (ADR 0039's pull request 1);
// webhooktest.Sender scripts it.
type Sender interface {
	Send(ctx context.Context, c Call) Outcome
}
