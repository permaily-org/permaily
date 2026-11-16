package cimd

import (
	"github.com/permaily-org/permaily/internal/jws"
	"github.com/permaily-org/permaily/internal/vendors"
)

// parseKeys reads a key set with jws.Keys, as internal/idp reads a
// provider's (ADR 0030, "private_key_jwt"): the set and each key one JSON
// object with every member once, in any case too (encoding/json would take
// the last of "kid" and "KID", or of "kty" and its Kelvin-sign spelling),
// names matched exactly, at most jws.MaxKeys keys. It keeps the signing keys
// a client assertion can be checked with, each named by its kid: RSA of
// jws.MinRSABits to jws.MaxRSABits for RS256, P-256 for ES256. A key of
// another kind, use or algorithm is skipped, and one without a visible kid
// too; a kid given twice, or a set with no key left, is not a key set that
// can be used.
func parseKeys(body []byte) ([]vendors.Key, error) {
	set, err := jws.Keys(body)
	if err != nil {
		return nil, vendors.ErrDocument
	}
	var keys []vendors.Key
	for _, k := range set {
		if k.Algorithm != "" {
			keys = append(keys, vendors.Key{ID: k.ID, Algorithm: k.Algorithm, Public: k.Public})
		}
	}
	if len(keys) == 0 {
		return nil, vendors.ErrDocument
	}
	return keys, nil
}
