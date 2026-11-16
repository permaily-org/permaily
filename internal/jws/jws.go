// Package jws checks compact JSON Web Signatures (RFC 7515) signed with
// RS256 or ES256 against a set of public keys (RFC 7517), written here so no
// JOSE library is in the image (ADR 0030, "private_key_jwt"). One
// implementation checks the ID tokens of internal/idp and the client
// assertions of internal/oauth; tests/oauth-differential compares it with
// go-jose, and tests/idp-differential keeps comparing internal/idp with
// go-oidc.
//
// It is pure: no network, no clock, no storage. What a token says (its
// claims) is the caller's to check, once its signature has been checked:
// nothing is taken from a token before that. A key the token carries or
// points to (jwk, jku, x5c, x5u) is never used: a carried jwk must be a
// well-formed public key all the same, and a carried x5c is refused, since
// no certificate is read here. What a header says must be understood (crit)
// is refused, since nothing is, and so is a b64 other than true, which would
// say the claims are not base64url (RFC 7797, section 7, forbids it for a
// JWT). Every header jws takes, go-jose takes too
// (tests/oauth-differential).
package jws

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The algorithms a token may be signed with. Every other one is refused:
// "none" is no signature, and HS256 would be one made with a public key as
// its secret.
const (
	RS256 = "RS256"
	ES256 = "ES256"
)

// Bounds of what is read.
const (
	// MaxCompactBytes bounds a token as sent.
	MaxCompactBytes = 16 << 10
	// MaxKeys bounds the keys of one set.
	MaxKeys = 64
	// MaxKeyID bounds a key's identifier, which is visible ASCII.
	MaxKeyID = 256
	// MinRSABits and MaxRSABits bound an RSA key's modulus.
	MinRSABits = 2048
	MaxRSABits = 4096
)

// The ways a token or a key set is refused, each on its own so a caller
// can say which in its own words. None of them holds anything of the token.
var (
	ErrTooLong           = errors.New("jws: token too long")
	ErrNotThreeParts     = errors.New("jws: token is not three parts")
	ErrHeaderEncoding    = errors.New("jws: header is not base64url")
	ErrClaimsEncoding    = errors.New("jws: claims are not base64url")
	ErrSignatureEncoding = errors.New("jws: signature is not base64url")
	ErrHeader            = errors.New("jws: header is not one JSON object")
	ErrAlgorithm         = errors.New("jws: signed with another algorithm")
	ErrType              = errors.New("jws: a type that is not text")
	ErrCritical          = errors.New("jws: header with members that must be understood")
	ErrKeyID             = errors.New("jws: no key identifier")
	ErrHeaderMember      = errors.New("jws: header with a member it cannot have")
	ErrUnknownKey        = errors.New("jws: signed with a key the set does not hold")
	ErrUnusableKey       = errors.New("jws: signed with a key that is not for this algorithm")
	ErrSignature         = errors.New("jws: wrong signature")

	ErrKeySet       = errors.New("jws: keys are not one JSON object")
	ErrKeyList      = errors.New("jws: keys without a list, or with one too long")
	ErrKeyNotObject = errors.New("jws: a key is not one JSON object")
	ErrKeyIDTwice   = errors.New("jws: two keys with one identifier")
)

// Refusals is every error Parse, Verify and Keys return, each as it is
// (never wrapped), so a caller can name each in its own words.
var Refusals = []error{
	ErrTooLong, ErrNotThreeParts, ErrHeaderEncoding, ErrClaimsEncoding, ErrSignatureEncoding,
	ErrHeader, ErrAlgorithm, ErrType, ErrCritical, ErrKeyID, ErrHeaderMember, ErrUnknownKey, ErrUnusableKey,
	ErrSignature, ErrKeySet, ErrKeyList, ErrKeyNotObject, ErrKeyIDTwice,
}

// Token is a compact JWS as parsed, not yet verified.
type Token struct {
	// Algorithm is RS256 or ES256, KeyID the header's kid.
	Algorithm string
	KeyID     string
	// Type is the header's typ; Typed says the header had one.
	Type  string
	Typed bool
	// Claims is the payload as decoded: the caller reads it only after
	// Verify.
	Claims []byte

	signed    string
	signature []byte
}

// Key is one public key of a set.
type Key struct {
	ID string
	// Algorithm is what the key verifies (RS256 or ES256), and Public the
	// key (*rsa.PublicKey or *ecdsa.PublicKey); both empty for a key that
	// verifies nothing here (another type, another use, another algorithm,
	// too short), which is kept so a token that names it is refused for that.
	Algorithm string
	Public    any
	// Issuer is the key's "issuer" member when it has one (Microsoft says
	// with it which issuer signs with the key).
	Issuer string
}

// Profile is what a caller takes of a token's header: the algorithms (RS256,
// ES256 or both; nil is both) and the types (nil takes any text).
type Profile struct {
	Algorithms []string
	Type       func(typ string) bool
}

// Parse reads a compact JWS with the profile that takes RS256 and ES256 and
// any type: see Profile.Parse.
func Parse(compact string) (*Token, error) {
	return Profile{}.Parse(compact)
}

// Parse reads a compact JWS: three parts of base64url without padding, and
// a header that is one JSON object with every member once, checked in this
// order: alg one of p's algorithms, typ text and one of p's types when
// present, no crit, a kid of visible ASCII; then b64 true when present, a
// jwk that is a well-formed public key when present (never used), no x5c,
// and a nonce that is text when present.
func (p Profile) Parse(compact string) (*Token, error) {
	if len(compact) > MaxCompactBytes {
		return nil, ErrTooLong
	}
	// Five parts would be an encrypted token, two an unsigned one cut short.
	parts := strings.Split(compact, ".")
	if len(parts) != 3 {
		return nil, ErrNotThreeParts
	}
	header, ok := decode(parts[0])
	if !ok {
		return nil, ErrHeaderEncoding
	}
	claims, ok := decode(parts[1])
	if !ok {
		return nil, ErrClaimsEncoding
	}
	signature, ok := decode(parts[2])
	if !ok {
		return nil, ErrSignatureEncoding
	}
	members, ok := Object(header)
	if !ok {
		return nil, ErrHeader
	}
	t := &Token{Claims: claims, signed: compact[:len(parts[0])+1+len(parts[1])], signature: signature}
	algorithms := p.Algorithms
	if algorithms == nil {
		algorithms = []string{RS256, ES256}
	}
	if t.Algorithm, ok = Text(members["alg"]); !ok || (t.Algorithm != RS256 && t.Algorithm != ES256) || !slices.Contains(algorithms, t.Algorithm) {
		return nil, ErrAlgorithm
	}
	if value, has := members["typ"]; has {
		if t.Type, ok = Text(value); !ok || (p.Type != nil && !p.Type(t.Type)) {
			return nil, ErrType
		}
		t.Typed = true
	}
	// Nothing is understood that a header says must be (RFC 7515, section 4.1.11).
	if _, has := members["crit"]; has {
		return nil, ErrCritical
	}
	if t.KeyID, ok = Text(members["kid"]); !ok || !visible(t.KeyID, MaxKeyID) {
		return nil, ErrKeyID
	}
	// The signature covers the claims as base64url, and nothing else: a b64
	// that is not true would say it covers them as they are (RFC 7797).
	if value, has := members["b64"]; has && string(value) != "true" {
		return nil, ErrHeaderMember
	}
	if value, has := members["jwk"]; has && !wellFormed(value) {
		return nil, ErrHeaderMember
	}
	if _, has := members["x5c"]; has {
		return nil, ErrHeaderMember
	}
	if value, has := members["nonce"]; has {
		if _, ok := Text(value); !ok {
			return nil, ErrHeaderMember
		}
	}
	return t, nil
}

// wellFormed says a jwk a header carries is a public key this package could
// verify with, and has nothing else: an RSA key (kty, n, e) or a P-256 key
// (kty, crv, x, y), with kid, use and alg as text when present. It is never
// used; it is checked so a header jws takes is one go-jose takes too.
func wellFormed(raw json.RawMessage) bool {
	key, ok := Object(raw)
	if !ok {
		return false
	}
	kty, _ := Text(key["kty"])
	allowed := map[string][]string{"RSA": {"n", "e"}, "EC": {"crv", "x", "y"}}[kty]
	for name, value := range key {
		switch {
		case name == "kty" || slices.Contains(allowed, name):
		case name == "kid" || name == "use" || name == "alg":
			if _, ok := Text(value); !ok {
				return false
			}
		default:
			return false
		}
	}
	if kty == "RSA" {
		return rsaKey(key) != nil
	}
	return kty == "EC" && ecKey(key) != nil
}

// Verify checks t's signature with the key of keys whose ID is t's kid (the
// first, when a caller gave two: Keys never does): a key for t's algorithm,
// an RSA key of MinRSABits to MaxRSABits or a P-256 key, and a signature
// that verifies.
func Verify(t *Token, keys []Key) error {
	var key *Key
	for i := range keys {
		if keys[i].ID == t.KeyID {
			key = &keys[i]
			break
		}
	}
	if key == nil {
		return ErrUnknownKey
	}
	if key.Algorithm != t.Algorithm {
		return ErrUnusableKey
	}
	hash := sha256.Sum256([]byte(t.signed))
	switch t.Algorithm {
	case RS256:
		pub, ok := key.Public.(*rsa.PublicKey)
		if !ok || pub.N == nil || pub.N.BitLen() < MinRSABits || pub.N.BitLen() > MaxRSABits {
			return ErrUnusableKey
		}
		if rsa.VerifyPKCS1v15(pub, crypto.SHA256, hash[:], t.signature) != nil {
			return ErrSignature
		}
	default: // ES256, as Parse allows nothing else
		pub, ok := key.Public.(*ecdsa.PublicKey)
		if !ok || pub.Curve != elliptic.P256() {
			return ErrUnusableKey
		}
		// RFC 7518, section 3.4: R and S, 32 bytes each, not ASN.1.
		if len(t.signature) != 64 {
			return ErrSignature
		}
		r := new(big.Int).SetBytes(t.signature[:32])
		s := new(big.Int).SetBytes(t.signature[32:])
		if !ecdsa.Verify(pub, hash[:], r, s) {
			return ErrSignature
		}
	}
	return nil
}

// Keys reads a JSON Web Key Set: one object whose "keys" is a list of at
// most MaxKeys objects. A key without a visible kid is left out (no token
// can name it); two keys with one kid are refused, since which one signs
// would be a matter of their order; a key of another kind is kept, unusable.
func Keys(body []byte) ([]Key, error) {
	members, ok := Object(body)
	if !ok {
		return nil, ErrKeySet
	}
	items, ok := list(members["keys"], MaxKeys)
	if !ok {
		return nil, ErrKeyList
	}
	keys := []Key{}
	seen := map[string]bool{}
	for _, item := range items {
		key, ok := Object(item)
		if !ok {
			return nil, ErrKeyNotObject
		}
		kid, ok := Text(key["kid"])
		if !ok || !visible(kid, MaxKeyID) {
			continue
		}
		if seen[kid] {
			return nil, ErrKeyIDTwice
		}
		seen[kid] = true
		keys = append(keys, parseKey(kid, key))
	}
	return keys, nil
}

// parseKey reads one key. It verifies something only as what a token's
// header must say too: an RSA key for RS256, or a P-256 key for ES256, for
// signatures, and for that algorithm when it names one.
func parseKey(kid string, key map[string]json.RawMessage) Key {
	unusable := Key{ID: kid}
	issuer, ok := optional(key["issuer"])
	if !ok {
		return unusable
	}
	unusable.Issuer = issuer
	if value, has := key["use"]; has {
		if use, ok := Text(value); !ok || use != "sig" {
			return unusable
		}
	}
	kty, _ := Text(key["kty"])
	alg := map[string]string{"RSA": RS256, "EC": ES256}[kty]
	if alg == "" {
		return unusable
	}
	if value, has := key["alg"]; has {
		if named, ok := Text(value); !ok || named != alg {
			return unusable
		}
	}
	var public any
	if alg == RS256 {
		public = rsaKey(key)
	} else {
		public = ecKey(key)
	}
	if public == nil {
		return unusable
	}
	return Key{ID: kid, Algorithm: alg, Public: public, Issuer: issuer}
}

func rsaKey(key map[string]json.RawMessage) any {
	n, ok := number(key["n"], MaxRSABits/8)
	if !ok || n.BitLen() < MinRSABits || n.BitLen() > MaxRSABits {
		return nil
	}
	// crypto/rsa takes an exponent that fits an int; an even one is no exponent.
	e, ok := number(key["e"], 4)
	if !ok || e.BitLen() > 31 || e.Int64() < 3 || e.Bit(0) == 0 {
		return nil
	}
	return &rsa.PublicKey{N: n, E: int(e.Int64())}
}

func ecKey(key map[string]json.RawMessage) any {
	if crv, ok := Text(key["crv"]); !ok || crv != "P-256" {
		return nil
	}
	x, okX := coordinate(key["x"])
	y, okY := coordinate(key["y"])
	if !okX || !okY {
		return nil
	}
	// The point must be on the curve, which this checks.
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
	if err != nil {
		return nil
	}
	return pub
}

// coordinate is a P-256 coordinate: exactly 32 bytes (RFC 7518, section 6.2.1.2).
func coordinate(value json.RawMessage) ([]byte, bool) {
	s, ok := Text(value)
	if !ok {
		return nil, false
	}
	b, ok := decode(s)
	return b, ok && len(b) == 32
}

// number is a member that is a positive number of at most size bytes, as a
// key writes it: base64url of its big-endian bytes.
func number(value json.RawMessage, size int) (*big.Int, bool) {
	s, ok := Text(value)
	if !ok {
		return nil, false
	}
	b, ok := decode(s)
	if !ok || len(b) == 0 || len(b) > size+1 { // some writers put a zero byte in front
		return nil, false
	}
	return new(big.Int).SetBytes(b), true
}

// decode reads base64url without padding (RFC 7515, section 2), one way only:
// encoding/base64 alone skips line breaks, and Strict refuses the spare bits
// of the last character when they are not zero.
func decode(s string) ([]byte, bool) {
	if strings.ContainsAny(s, "\r\n") {
		return nil, false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return b, err == nil
}

// Object reads the members of one JSON object of valid UTF-8 with nothing
// after it. encoding/json alone would take the last of two members with one
// name, and match names in any case; here a name twice is refused, in any
// case too (fold), and names are matched exactly by the caller. So whoever
// else reads the same bytes, with encoding/json or exactly, reads the same
// members.
func Object(raw []byte) (map[string]json.RawMessage, bool) {
	if !utf8.Valid(raw) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if open, err := dec.Token(); err != nil || open != json.Delim('{') {
		return nil, false
	}
	members := map[string]json.RawMessage{}
	folded := map[string]bool{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, false
		}
		name, _ := token.(string) // inside an object the decoder gives only names here
		key := fold(name)
		if folded[key] {
			return nil, false
		}
		folded[key] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		members[name] = value
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) { // and nothing after it
		return nil, false
	}
	return members, true
}

// fold is name as one of the names any reader that matches names in any case
// takes for it: each letter the smallest of those Unicode's simple case
// folding makes one (A and a, k, K and the Kelvin sign, s, S and the long s),
// after the upper case of its lower case, which is how encoding/json has
// folded in some versions and which also makes the Turkish dotted and
// dotless i an I.
func fold(name string) string {
	return strings.Map(func(r rune) rune {
		r = unicode.ToUpper(unicode.ToLower(r))
		smallest := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			smallest = min(smallest, f)
		}
		return smallest
	}, name)
}

// list reads the items of one JSON array, at most limit of them.
func list(raw []byte, limit int) ([]json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if open, err := dec.Token(); err != nil || open != json.Delim('[') {
		return nil, false
	}
	var items []json.RawMessage
	for dec.More() {
		var item json.RawMessage
		if len(items) == limit || dec.Decode(&item) != nil {
			return nil, false
		}
		items = append(items, item)
	}
	// raw is a member of an object Object read, so the bracket closes it.
	return items, true
}

// Text is a member that is a JSON string. A null would otherwise read as "".
func Text(value json.RawMessage) (string, bool) {
	var s string
	if len(value) == 0 || value[0] != '"' || json.Unmarshal(value, &s) != nil {
		return "", false
	}
	return s, true
}

// optional is a member that is a JSON string, or is null, or is not there:
// the last two read as "".
func optional(value json.RawMessage) (string, bool) {
	if len(value) == 0 || string(value) == "null" {
		return "", true
	}
	return Text(value)
}

// visible says s is 1 to limit bytes of visible ASCII.
func visible(s string, limit int) bool {
	if s == "" || len(s) > limit {
		return false
	}
	for i := range len(s) {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
