package oauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
)

// NewSecret is prefix and SecretBytes bytes from random in unpadded
// base64url (43 characters): an access or refresh token, a code, a
// request's or a pushed request's identifier.
func NewSecret(prefix string, random func([]byte) error) (string, error) {
	b := make([]byte, SecretBytes)
	if err := random(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// Hash is what a secret of kind k is kept as: the hex SHA-256 of
// "permaily oauth <kind> v1\x00" and the secret. The kind separates the
// domains: one secret has another hash as another kind, so the table holds
// no secret and no hash that could stand for another.
func Hash(k Kind, secret string) string {
	sum := sha256.Sum256([]byte("permaily oauth " + string(k) + " v1\x00" + secret))
	return hex.EncodeToString(sum[:])
}

// familyPrefix is the prefix of a family's tokens of each kind.
var familyPrefix = map[Kind]string{KindAccess: PrefixAccess, KindRefresh: PrefixRefresh}

// FamilySecret is the access (KindAccess) or refresh (KindRefresh) token of
// a family's generation: its prefix and the HMAC-SHA-256, under key (derived
// from the root secret, "permaily oauth families v1"), of the kind, the
// family and the generation, in unpadded base64url. Every enclave makes the
// same pair for a retry (FamilyState.Refresh's RefreshRetry), and the table
// never holds it. Any other kind has no prefix: nothing hands one out.
func FamilySecret(key []byte, k Kind, family string, generation uint64) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("permaily oauth family token v1\x00" + string(k) + "\x00" + family + "\x00"))
	mac.Write(binary.BigEndian.AppendUint64(nil, generation))
	return familyPrefix[k] + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
