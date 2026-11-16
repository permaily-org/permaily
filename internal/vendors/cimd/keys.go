package cimd

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"

	"github.com/permaily-org/permaily/internal/vendors"
)

// rsaMinBits is the smallest RSA key a client assertion may be checked with
// (ADR 0030, "private_key_jwt").
const rsaMinBits = 2048

// jwk is the members of a JSON Web Key (RFC 7517) the resolver reads.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// parseKeys reads a key set: a JSON object whose keys are JWKs. It keeps the
// signing keys a client assertion can be checked with, each named by its
// kid: RSA of at least rsaMinBits for RS256, P-256 for ES256. A key of
// another kind, use or algorithm, or without a kid, is skipped; a kid given
// twice, or a set with no key left, is not a key set that can be used.
func parseKeys(body []byte) ([]vendors.Key, error) {
	if b := bytes.TrimLeft(body, " \t\r\n"); len(b) == 0 || b[0] != '{' {
		return nil, vendors.ErrDocument
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, vendors.ErrDocument
	}
	var keys []vendors.Key
	seen := map[string]bool{}
	for _, k := range set.Keys {
		if k.Kid == "" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		if seen[k.Kid] {
			return nil, vendors.ErrDocument
		}
		seen[k.Kid] = true
		if key, ok := k.public(); ok {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, vendors.ErrDocument
	}
	return keys, nil
}

// public is k as a vendors.Key, when it is one a client assertion can be
// checked with.
func (k jwk) public() (vendors.Key, bool) {
	switch {
	case k.Kty == "RSA" && (k.Alg == "" || k.Alg == "RS256"):
		n, ok := unsigned(k.N)
		if !ok || n.BitLen() < rsaMinBits {
			return vendors.Key{}, false
		}
		e, ok := unsigned(k.E)
		if !ok || e.BitLen() > 31 || e.Int64() < 3 || e.Bit(0) == 0 {
			return vendors.Key{}, false
		}
		return vendors.Key{ID: k.Kid, Algorithm: "RS256", Public: &rsa.PublicKey{N: n, E: int(e.Int64())}}, true
	case k.Kty == "EC" && k.Crv == "P-256" && (k.Alg == "" || k.Alg == "ES256"):
		x, errX := base64.RawURLEncoding.DecodeString(k.X)
		y, errY := base64.RawURLEncoding.DecodeString(k.Y)
		if errX != nil || errY != nil || len(x) != 32 || len(y) != 32 {
			return vendors.Key{}, false
		}
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		if err != nil {
			return vendors.Key{}, false
		}
		return vendors.Key{ID: k.Kid, Algorithm: "ES256", Public: pub}, true
	}
	return vendors.Key{}, false
}

// unsigned is a base64url big-endian integer with no leading zero byte.
func unsigned(s string) (*big.Int, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) == 0 || b[0] == 0 {
		return nil, false
	}
	return new(big.Int).SetBytes(b), true
}
