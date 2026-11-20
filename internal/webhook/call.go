package webhook

import (
	"context"
	"errors"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Call is one call of a webhook: the notice's body and its three headers
// (contracts.WebhookHeader*), and WriteBy, the instant by which the request
// is written in full or not at all (oauth.ConnectionFresh after the read
// that allowed it, connections' Permit.WriteBy).
//
// What "written" means (design review, finding 4): the request's HEADERS
// frame and its one DATA frame with END_STREAM, at most
// RequestWriteBytesMax together, handed to the TLS connection in a single
// Write, so they leave in one TLS record, and that Write returned. The
// sender starts that Write only after the server's SETTINGS, only when the
// peer's stream and connection windows take the whole body (else
// OutcomeRefused: nothing is written, since a request split by flow control
// would leave its headers without its body), and only while the permit's
// context lives and WriteBy has not passed (else OutcomeCut or
// OutcomeTooLate). A cut or WriteBy that comes while the Write blocks (the
// host not reading the vsock) closes the connection: the record is never
// whole, and a TLS record that is not whole opens to nothing. Once the Write
// returned only the answer is waited for, within AttemptTimeout. The dial
// and the handshake run under the permit's context too.
type Call struct {
	URL       string
	ID        string
	Timestamp string
	Signature string
	Body      []byte
	WriteBy   time.Time
}

// ErrCut is the cause of a permit's context that was cut: the connection
// stopped calling through this enclave (connections' Permit), so nothing of
// the call is written.
var ErrCut = errors.New("webhook: the call's permit was cut")

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
	OutcomeCut      Outcome = "cut"       // the permit was cut (ErrCut): nothing left
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
// only, following no redirect, sending no cookie or credential, written as
// Call says or not at all, reading at most ResponseHeaderBytesMax of the
// answer's headers and AnswerBytesMax of its body, within AttemptTimeout,
// keeping no connection once it ends (IdleConnectionsMax). ctx is the
// permit's: its end with cause ErrCut is OutcomeCut. internal/webhook/send
// implements it (ADR 0039's pull request 1); webhooktest.Sender scripts it.
type Sender interface {
	Send(ctx context.Context, c Call) Outcome
}
