package store

import (
	"errors"
	"fmt"
	"testing"
)

// The store's errors are of a fixed kind (domain.FixedKind): what a log
// shows of them is their own text, which holds nothing of a request, however
// a caller wraps them with %w; errors.Is still finds each, and ErrLost is
// still an ErrNotFound.
func TestTheStoresErrorsAreOfAFixedKind(t *testing.T) {
	for _, err := range []error{ErrNotFound, ErrExists, ErrConflict, ErrChanged, ErrRolledBack, ErrForked, ErrLost, ErrTooLarge} {
		wrapped := fmt.Errorf("commits of account-acc_secretvalue123: %w", err)
		var k interface{ LogKind() string }
		if !errors.As(wrapped, &k) {
			t.Fatalf("%v is not of a fixed kind", err)
		}
		if k.LogKind() != err.Error() {
			t.Errorf("kind %q, want %q", k.LogKind(), err.Error())
		}
		if !errors.Is(wrapped, err) {
			t.Errorf("errors.Is lost %v", err)
		}
	}
	if !errors.Is(ErrLost, ErrNotFound) {
		t.Error("ErrLost is no longer an ErrNotFound")
	}
	if errors.Is(ErrNotFound, ErrLost) {
		t.Error("ErrNotFound became an ErrLost")
	}
}
