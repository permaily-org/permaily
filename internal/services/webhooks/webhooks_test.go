package webhooks

import "testing"

// The service's errors are of a fixed kind, logged as themselves.
func TestAnErrorIsLoggedAsItsKind(t *testing.T) {
	if ErrNoWebhook.(interface{ LogKind() string }).LogKind() != ErrNoWebhook.Error() {
		t.Error("a fixed kind is logged as itself")
	}
}
