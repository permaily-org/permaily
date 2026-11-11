package contracts

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrors(t *testing.T) {
	wrapped := fmt.Errorf("finishing: %w", &Error{Code: "invalid_code", Message: "another wording"})
	if !errors.Is(wrapped, ErrInvalidCode) {
		t.Error("an error with the same code is not matched")
	}
	if errors.Is(wrapped, ErrUnauthorized) || errors.Is(errors.New("invalid_code"), ErrInvalidCode) {
		t.Error("another error is matched")
	}
	if got := ErrInvalidCode.Error(); got != "invalid_code: that code or link is not valid any more; ask for a new one" {
		t.Errorf("message: %q", got)
	}
}
