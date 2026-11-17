package oauth

import (
	"errors"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Error is a refusal of one of this package's checks: the OAuth error code
// to answer and a fixed reason, one of the kinds ADR 0030's "Logging" lists
// (contracts.ClientProblem's for a client), never a value of what was sent.
// Unwrap gives the cause, for errors.Is: jws.ErrUnknownKey says a client
// assertion named a key the set did not hold, so its set may be fetched
// again.
type Error struct {
	Code   ErrorCode
	Reason string
	cause  error
}

func (e *Error) Error() string { return "oauth: " + string(e.Code) + " (" + e.Reason + ")" }
func (e *Error) Unwrap() error { return e.cause }

// The fixed reasons that are not a client problem.
const (
	ReasonAssertion = "assertion"
)

func refuse(code ErrorCode) *Error { return &Error{Code: code, Reason: string(code)} }

func refuseClient(p contracts.ClientProblem) *Error {
	return &Error{Code: ErrInvalidClient, Reason: string(p)}
}

// The causes of a client assertion refused, for tests and errors.Is; each
// is answered invalid_client and logged as ReasonAssertion.
var (
	errNotAssertionClient = errors.New("oauth: the client's document does not declare private_key_jwt")
	errAssertionAlg       = errors.New("oauth: the assertion is signed with another algorithm than the document's")
	errAssertionClaims    = errors.New("oauth: the assertion's claims are not one JSON object")
	errAssertionIssuer    = errors.New("oauth: the assertion's iss or sub is not the client")
	errAssertionAudience  = errors.New("oauth: the assertion is for another audience")
	errAssertionExpired   = errors.New("oauth: the assertion's exp is past, or absent")
	errAssertionTooLong   = errors.New("oauth: the assertion's exp is too far ahead")
	errAssertionIssuedAt  = errors.New("oauth: the assertion's iat or nbf is ahead")
	errAssertionJTI       = errors.New("oauth: the assertion's jti is absent or too long")
)

func refuseAssertion(cause error) *Error {
	return &Error{Code: ErrInvalidClient, Reason: ReasonAssertion, cause: cause}
}
