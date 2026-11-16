package idp_test

import (
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/idp"
)

// An ID token's header is refused for the first thing wrong with it in the
// order this package always checked: the algorithm, the type, crit, then the
// key identifier, then (since pull request 133's review) what changes what
// the signature covers or carries a key. A header wrong in more than one way
// says what it said before internal/jws checked it (review finding 4).
func TestIDTokenHeaderRefusedInItsOrder(t *testing.T) {
	for header, why := range map[string]string{
		`{"alg":"RS256","typ":"at+jwt"}`:                        "a token of another type",
		`{"alg":"RS256","kid":"k","typ":"at+jwt","crit":["x"]}`: "a token of another type",
		`{"alg":"RS256","typ":1,"crit":[]}`:                     "a token of another type",
		`{"alg":"ES256"}`:                                       "ID token signed with another algorithm",
		`{"alg":"ES256","kid":"k"}`:                             "ID token signed with another algorithm",
		`{"alg":"ES256","typ":"at+jwt","crit":[]}`:              "ID token signed with another algorithm",
		`{"alg":"PS256","typ":"at+jwt"}`:                        "ID token signed with another algorithm",
		`{"typ":"at+jwt"}`:                                      "ID token signed with another algorithm",
		`{"alg":"RS256","crit":[]}`:                             "ID token with a header that must be understood",
		`{"alg":"RS256","typ":"JWT"}`:                           "ID token without a key identifier",
		`{"alg":"RS256","b64":false}`:                           "ID token without a key identifier",
	} {
		token := b64([]byte(header)) + ".e30."
		id, err := idp.Verify(idp.Google, "https://accounts.google.com", "client", "nonce", token, time.Unix(1_800_000_000, 0), []byte(`{"keys":[]}`))
		if id != nil || err == nil || err.Error() != idp.ErrInvalid.Error()+": "+why {
			t.Errorf("%s: %v, want %s", header, err, why)
		}
	}
}
