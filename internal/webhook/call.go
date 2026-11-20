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
// What "written" means (design review, finding 4; re-review N1): the
// request's HEADERS frame and its one DATA frame with END_STREAM, at most
// RequestWriteBytesMax together, handed whole to the TLS connection in a
// single Write on a connection whose dynamic record sizing is off, so they
// leave in exactly one TLS record (a single Write alone is not one record:
// Go's default sizes a connection's first records at about 1 KiB). The
// sender admits that Write only after the server's SETTINGS, only when the
// peer allows a stream and its stream and connection windows take the whole
// body and its header list limit the headers (else OutcomeRefused: nothing
// is written, since a request split by flow control would leave its headers
// without its body), and only while the permit's context lives and WriteBy
// has not passed (else OutcomeCut or OutcomeTooLate, nothing written: a cut
// before admission forbids it). A cut or WriteBy that comes while the Write
// blocks (the host not reading the vsock) closes the connection, which
// cannot retract what was already handed to it. When none of the record, or
// only part of it, was handed, the record is not whole and opens to nothing:
// no byte of the request is readable, and the call is OutcomeCut or
// OutcomeTooLate. When the whole record was handed, it cannot be recalled:
// the call is never cut or too_late, and only its answer is waited for,
// within AttemptTimeout (OutcomeNoAnswer when the connection was closed
// under it). The dial and the handshake run under the permit's context too.
type Call struct {
	URL       string
	ID        string
	Timestamp string
	Signature string
	Body      []byte
	WriteBy   time.Time
}

// ErrCut is the cause of a permit's context that was cut: the connection
// stopped calling through this enclave (connections' Permit), so no call of
// it is admitted to write once it is cut. A call cut before its Write was
// admitted writes nothing; one cut while its Write blocks may have handed
// part of its one TLS record, which opens to nothing, and one whose whole
// record was already handed is not cut (Call).
var ErrCut = errors.New("webhook: the call's permit was cut")

// Outcome is how a call ended, one of a fixed set: the only words kept,
// counted and logged of it.
type Outcome string

const (
	OutcomeSent     Outcome = "sent"      // a 2xx
	OutcomeGone     Outcome = "gone"      // 410: the vendor asks to stop
	OutcomeRedirect Outcome = "redirect"  // a 3xx, never followed
	OutcomeStatus   Outcome = "status"    // any other status
	OutcomeNoAnswer Outcome = "no_answer" // not reached, reset, past AttemptTimeout, or a malformed answer
	OutcomeRefused  Outcome = "refused"   // TLS that does not verify, no HTTP/2, or a peer that cannot take the request
	OutcomeTooLate  Outcome = "too_late"  // not written in full by WriteBy: nothing readable left (at most part of a record)
	OutcomeCut      Outcome = "cut"       // the permit was cut (ErrCut) before the request was written in full: the same
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
