package decide

import (
	"errors"
	"testing"
)

// TestAnAttemptErrorIsItsFailure: the service reads an attempt's failure
// with errors.Is, and its words are the failure's, never the provider's
// (ADR 0028, "Failures").
func TestAnAttemptErrorIsItsFailure(t *testing.T) {
	var err error = &AttemptError{Err: ErrUnavailable, Sent: true, Retry: true}
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrKeyRefused) || err.Error() != ErrUnavailable.Error() {
		t.Fatalf("%v", err)
	}
	var ae *AttemptError
	if !errors.As(err, &ae) || !ae.Sent || !ae.Retry {
		t.Fatalf("errors.As: %+v", ae)
	}
}

// TestAnotherModelIsNotAnInvalidAnswer: an answer naming another model is
// a failure of its own, which the canary reads as a change and never as a
// failed run, so it must not be ErrInvalidAnswer, nor the other way round
// (ADR 0028, "Changed after the second design review", 5).
func TestAnotherModelIsNotAnInvalidAnswer(t *testing.T) {
	var changed error = &AttemptError{Err: ErrModelChanged, Sent: true}
	var invalid error = &AttemptError{Err: ErrInvalidAnswer, Sent: true}
	if !errors.Is(changed, ErrModelChanged) || errors.Is(changed, ErrInvalidAnswer) {
		t.Fatalf("another model: %v", changed)
	}
	if !errors.Is(invalid, ErrInvalidAnswer) || errors.Is(invalid, ErrModelChanged) {
		t.Fatalf("an invalid answer: %v", invalid)
	}
	all := []error{ErrUnavailable, ErrKeyRefused, ErrProviderLimit, ErrRequest, ErrInvalidAnswer, ErrModelChanged}
	for i, a := range all {
		for j, b := range all {
			if (i == j) != errors.Is(a, b) {
				t.Errorf("errors.Is(%v, %v) = %v", a, b, errors.Is(a, b))
			}
		}
	}
}
