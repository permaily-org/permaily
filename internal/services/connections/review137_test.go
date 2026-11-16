package connections

import (
	"fmt"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/oauth"
)

// PR #137's review, finding 2 ("arbitrary implementation of FixedKind"): an
// OAuth refusal is logged as its code only when the code is one of the
// OAuth codes; anything else put in Code is logged as a fixed word, never
// verbatim, whatever wraps it.
func TestReview137AnOAuthRefusalLogsOnlyAKnownCode(t *testing.T) {
	for _, code := range []oauth.ErrorCode{"pma_PrivateMarkAbZ_x-", "invalid_grant state=PrivateMark", "", "INVALID_GRANT"} {
		err := fmt.Errorf("refresh with PrivateMark: %w", &OAuthError{Code: code})
		got := domain.Redacted(err)
		if strings.Contains(got, "PrivateMark") || got != "connections: an OAuth refusal" {
			t.Errorf("%q: %q", code, got)
		}
	}
	for _, code := range []oauth.ErrorCode{oauth.ErrInvalidGrant, oauth.ErrInvalidClient, oauth.ErrInsufficientScope, oauth.ErrTemporarily} {
		if got := domain.Redacted(fmt.Errorf("x: %w", &OAuthError{Code: code})); got != "connections: "+string(code) {
			t.Errorf("%q: %q", code, got)
		}
	}
}
