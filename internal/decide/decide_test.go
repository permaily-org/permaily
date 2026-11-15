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
