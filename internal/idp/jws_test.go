package idp_test

import (
	"testing"

	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/jws"
)

// The check of an ID token is internal/jws's (ADR 0030, pull request 1):
// every refusal it can return is said here in this package's own words, so
// a refusal is never one without a reason.
func TestEveryRefusalOfJWSHasItsWords(t *testing.T) {
	for _, err := range jws.Refusals {
		if idp.Words[err] == "" {
			t.Errorf("no words for %v", err)
		}
	}
}
